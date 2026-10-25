package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"ai/assets"
)

type RemoteProvider string

const (
	RemoteOpenAI     RemoteProvider = "openai"
	RemoteOpenRouter RemoteProvider = "openrouter"
	RemoteAnthropic  RemoteProvider = "anthropic"
	RemoteGemini     RemoteProvider = "gemini"
	RemoteCopilot    RemoteProvider = "github-copilot"
)

type RemoteEngineConfig struct {
	Provider    RemoteProvider
	Model       string
	APIKey      string
	Credential  *Credential
	Credentials CredentialStore
	BaseURL     string
	HTTPClient  *http.Client
}

func NewRemoteEngine(cfg RemoteEngineConfig) (*Engine, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("remote AI model must not be empty")
	}
	if cfg.APIKey == "" && cfg.Credential == nil && cfg.Credentials == nil {
		return nil, fmt.Errorf("remote AI credential must not be empty")
	}
	switch cfg.Provider {
	case RemoteOpenAI, RemoteOpenRouter, RemoteAnthropic, RemoteGemini, RemoteCopilot:
	default:
		return nil, fmt.Errorf("unsupported remote AI provider %q", cfg.Provider)
	}
	if cfg.BaseURL != "" {
		if err := validateRemoteBaseURL(cfg.BaseURL); err != nil {
			return nil, err
		}
	}
	backend := &remoteEngineBackend{cfg: cfg}
	return &Engine{impl: &engineImpl{
		backend:     backend,
		backendName: string(cfg.Provider),
		modelPath:   string(cfg.Provider) + ":" + cfg.Model,
	}}, nil
}

func validateRemoteBaseURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("invalid remote AI base URL")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
		return fmt.Errorf("remote AI base URL must use HTTPS except for loopback test servers")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || func() bool {
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}()
}

type remoteEngineBackend struct {
	cfg RemoteEngineConfig
}

func (e *remoteEngineBackend) NewConversation(cfg ConversationConfig) (conversationBackend, error) {
	history, err := clonePromptParts(cfg.History)
	if err != nil {
		return nil, fmt.Errorf("copy remote conversation history: %w", err)
	}
	cfg.History = nil
	return &remoteConversationBackend{
		engine:  e,
		config:  cfg,
		history: history,
	}, nil
}

func (e *remoteEngineBackend) Close() error { return nil }

type remoteConversationBackend struct {
	engine  *remoteEngineBackend
	config  ConversationConfig
	history []PromptPart

	mu     sync.Mutex
	closed bool
	active bool
	cancel context.CancelFunc
}

func (c *remoteConversationBackend) Clone() (conversationBackend, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	history, err := clonePromptParts(c.history)
	if err != nil {
		return nil, fmt.Errorf("clone remote conversation history: %w", err)
	}
	config, err := cloneConversationConfig(c.config)
	if err != nil {
		return nil, fmt.Errorf("clone remote conversation config: %w", err)
	}
	return &remoteConversationBackend{engine: c.engine, config: config, history: history}, nil
}

func (c *remoteConversationBackend) Close() error {
	c.mu.Lock()
	c.closed = true
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (c *remoteConversationBackend) SendStream(ctx context.Context, parts []PromptPart, onEvent func(ModelEvent)) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.active {
		c.mu.Unlock()
		return errStreamActive
	}
	c.active = true
	streamCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	history := append([]PromptPart(nil), c.history...)
	config := c.config
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.active = false
		c.cancel = nil
		c.mu.Unlock()
	}()

	requestParts := append(history, parts...)
	effectiveCfg, err := c.engine.resolveEffectiveConfig(streamCtx)
	if err != nil {
		return err
	}
	req, wireKind, err := buildRemoteRequestWithWire(effectiveCfg, config, requestParts)
	if err != nil {
		return err
	}
	acc := newRemoteAccumulatorWithWire(effectiveCfg.Provider, wireKind, config.Tools, onEvent)
	if err := remoteStreamRequest(streamCtx, effectiveCfg.HTTPClient, req, acc.consume); err != nil {
		if effectiveCfg.Provider != RemoteCopilot || wireKind != wireOpenAICompletions || !errors.Is(err, errUnsupportedRemoteAPI) {
			return err
		}
		req, err = buildGitHubCopilotResponsesRequest(effectiveCfg, config, requestParts)
		if err != nil {
			return err
		}
		acc = newRemoteAccumulatorWithWire(effectiveCfg.Provider, wireOpenAIResponses, config.Tools, onEvent)
		if err := remoteStreamRequest(streamCtx, effectiveCfg.HTTPClient, req, acc.consume); err != nil {
			return err
		}
	}
	calls, err := acc.toolCalls()
	if err != nil {
		return err
	}
	if err := acc.finish(calls); err != nil {
		return err
	}

	c.mu.Lock()
	if !c.closed {
		c.history = append(c.history, parts...)
		if acc.text.Len() > 0 || len(calls) > 0 {
			c.history = append(c.history, PromptPart{
				Role:      RoleModel,
				Text:      acc.text.String(),
				ToolCalls: calls,
			})
		}
	}
	c.mu.Unlock()
	return nil
}

func clonePromptParts(parts []PromptPart) ([]PromptPart, error) {
	if len(parts) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(parts)
	if err != nil {
		return nil, err
	}
	var clone []PromptPart
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	for i := range clone {
		for j := range clone[i].ToolCalls {
			if j < len(parts[i].ToolCalls) {
				clone[i].ToolCalls[j].RawArguments = append(json.RawMessage(nil), parts[i].ToolCalls[j].RawArguments...)
			}
		}
	}
	return clone, nil
}

func cloneConversationConfig(cfg ConversationConfig) (ConversationConfig, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return ConversationConfig{}, err
	}
	var clone ConversationConfig
	if err := json.Unmarshal(data, &clone); err != nil {
		return ConversationConfig{}, err
	}
	clone.History = nil
	return clone, nil
}

type remoteRequest struct {
	method  string
	url     string
	headers map[string]string
	body    []byte
}

type remoteWireKind string

const (
	wireOpenAICompletions remoteWireKind = "openai-completions"
	wireOpenAIResponses   remoteWireKind = "openai-responses"
	wireAnthropicMessages remoteWireKind = "anthropic-messages"
	wireGeminiContent     remoteWireKind = "gemini-content"
)

func (e *remoteEngineBackend) resolveEffectiveConfig(ctx context.Context) (RemoteEngineConfig, error) {
	cfg := e.cfg
	if cfg.Credentials != nil {
		cred, err := ResolveProviderCredential(ctx, cfg.Provider, cfg.Credentials, cfg.HTTPClient)
		if err != nil {
			return RemoteEngineConfig{}, err
		}
		cfg.Credential = cred
		cfg.APIKey = cred.SecretToken()
	} else if cfg.Credential != nil && cfg.APIKey == "" {
		cfg.APIKey = cfg.Credential.SecretToken()
	}
	if cfg.APIKey == "" {
		return RemoteEngineConfig{}, fmt.Errorf("remote AI credential must not be empty")
	}
	return cfg, nil
}

func buildRemoteRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error) {
	req, _, err := buildRemoteRequestWithWire(remote, cfg, parts)
	return req, err
}

func buildRemoteRequestWithWire(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, remoteWireKind, error) {
	if remote.APIKey == "" && remote.Credential != nil {
		remote.APIKey = remote.Credential.SecretToken()
	}
	switch remote.Provider {
	case RemoteOpenRouter:
		req, err := buildOpenAIRequest(remote, cfg, parts)
		return req, wireOpenAICompletions, err
	case RemoteOpenAI:
		if isOpenAIChatGPTOAuth(remote.Credential, remote.APIKey) {
			req, err := buildOpenAIResponsesRequest(remote, cfg, parts)
			return req, wireOpenAIResponses, err
		}
		req, err := buildOpenAIRequest(remote, cfg, parts)
		return req, wireOpenAICompletions, err
	case RemoteAnthropic:
		req, err := buildAnthropicRequest(remote, cfg, parts)
		return req, wireAnthropicMessages, err
	case RemoteGemini:
		req, err := buildGeminiRequest(remote, cfg, parts)
		return req, wireGeminiContent, err
	case RemoteCopilot:
		return buildGitHubCopilotRequest(remote, cfg, parts)
	default:
		return remoteRequest{}, "", fmt.Errorf("unsupported remote AI provider %q", remote.Provider)
	}
}

func buildGitHubCopilotRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, remoteWireKind, error) {
	enterpriseDomain := ""
	if remote.Credential != nil {
		enterpriseDomain = remote.Credential.MetaString("enterprise_url")
	}
	baseURL := remote.BaseURL
	if baseURL == "" {
		baseURL = DeriveGitHubCopilotBaseURL(remote.APIKey, enterpriseDomain)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	dynHeaders := BuildCopilotDynamicHeaders(parts)

	if strings.HasPrefix(strings.ToLower(remote.Model), "claude-") {
		anthRemote := remote
		if !strings.HasSuffix(baseURL, "/v1/messages") {
			anthRemote.BaseURL = baseURL + "/v1/messages"
		}
		req, err := buildAnthropicRequest(anthRemote, cfg, parts)
		if err != nil {
			return remoteRequest{}, "", err
		}
		delete(req.headers, "x-api-key")
		req.headers["Authorization"] = "Bearer " + remote.APIKey
		for k, v := range dynHeaders {
			req.headers[k] = v
		}
		return req, wireAnthropicMessages, nil
	}

	openAIRemote := remote
	if !strings.HasSuffix(baseURL, "/chat/completions") {
		openAIRemote.BaseURL = baseURL + "/chat/completions"
	}
	req, err := buildOpenAIRequest(openAIRemote, cfg, parts)
	if err != nil {
		return remoteRequest{}, "", err
	}
	for k, v := range dynHeaders {
		req.headers[k] = v
	}
	return req, wireOpenAICompletions, nil
}

func buildGitHubCopilotResponsesRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error) {
	enterpriseDomain := ""
	if remote.Credential != nil {
		enterpriseDomain = remote.Credential.MetaString("enterprise_url")
	}
	baseURL := remote.BaseURL
	if baseURL == "" {
		baseURL = DeriveGitHubCopilotBaseURL(remote.APIKey, enterpriseDomain)
	}
	responsesRemote := remote
	responsesRemote.BaseURL = strings.TrimRight(baseURL, "/") + "/responses"
	req, err := buildOpenAIResponsesRequest(responsesRemote, cfg, parts)
	if err != nil {
		return remoteRequest{}, err
	}
	for name, value := range BuildCopilotDynamicHeaders(parts) {
		req.headers[name] = value
	}
	return req, nil
}

func buildOpenAIResponsesRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error) {
	inputItems, err := openAIResponsesInput(cfg.SystemMessage, parts)
	if err != nil {
		return remoteRequest{}, err
	}
	// Note: Sign in with ChatGPT tokens reject temperature, max_output_tokens, and prompt_cache_retention
	// (external/pi/packages/ai/src/api/openai-responses.ts:368-385).
	body := map[string]any{
		"model":  remote.Model,
		"input":  inputItems,
		"stream": true,
		"store":  false,
	}
	if len(cfg.Tools) > 0 {
		tools := make([]map[string]any, 0, len(cfg.Tools))
		for _, decl := range cfg.Tools {
			fn, ok := decl["function"].(map[string]any)
			if !ok {
				continue
			}
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        fn["name"],
				"description": fn["description"],
				"parameters":  fn["parameters"],
			})
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return remoteRequest{}, fmt.Errorf("encode OpenAI Responses request: %w", err)
	}
	endpoint := remote.BaseURL
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/responses"
	} else if !strings.HasSuffix(endpoint, "/responses") && !strings.HasSuffix(endpoint, "/v1/responses") {
		endpoint = strings.TrimRight(endpoint, "/") + "/responses"
	}
	headers := map[string]string{
		"Authorization": "Bearer " + remote.APIKey,
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
	}
	if remote.Credential != nil {
		if accountID := remote.Credential.MetaString("account_id"); accountID != "" {
			headers["chatgpt-account-id"] = accountID
		}
		if remote.Credential.MetaString("flow") == "codex" {
			headers["originator"] = "kabibi"
		}
	}
	return remoteRequest{method: http.MethodPost, url: endpoint, headers: headers, body: payload}, nil
}

func openAIResponsesInput(system string, parts []PromptPart) ([]map[string]any, error) {
	items := make([]map[string]any, 0, len(parts)+1)
	if system != "" {
		items = append(items, map[string]any{
			"role":    "developer",
			"content": system,
		})
	}
	var lastDate string
	for _, part := range parts {
		switch part.Role {
		case RoleTool:
			text, err := json.Marshal(formatWireToolResponse(part, &lastDate))
			if err != nil {
				return nil, fmt.Errorf("encode tool response: %w", err)
			}
			callID := part.ToolCallID
			if callID == "" {
				callID = part.ToolName
			}
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  string(text),
			})
		case RoleModel:
			if text := formatWirePartText(part, &lastDate); text != "" {
				items = append(items, map[string]any{
					"role":    "assistant",
					"content": text,
				})
			}
			for _, call := range part.ToolCalls {
				args, err := remoteToolArguments(call)
				if err != nil {
					return nil, err
				}
				callID := call.ID
				if callID == "" {
					callID = call.Name
				}
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   callID,
					"name":      call.Name,
					"arguments": string(args),
				})
			}
		default:
			if part.ImageBase64 != "" {
				items = append(items, map[string]any{
					"role": "user",
					"content": []map[string]any{
						{"type": "input_text", "text": formatWirePartText(part, &lastDate)},
						{"type": "input_image", "image_url": "data:image/jpeg;base64," + part.ImageBase64},
					},
				})
			} else {
				items = append(items, map[string]any{
					"role":    "user",
					"content": formatWirePartText(part, &lastDate),
				})
			}
		}
	}
	return items, nil
}

func buildOpenAIRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error) {
	messages, err := openAIMessages(cfg.SystemMessage, parts)
	if err != nil {
		return remoteRequest{}, err
	}
	body := map[string]any{"model": remote.Model, "stream": true, "messages": messages}
	if len(cfg.Tools) > 0 {
		body["tools"] = cfg.Tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return remoteRequest{}, fmt.Errorf("encode OpenAI request: %w", err)
	}
	url := remote.BaseURL
	if url == "" {
		switch remote.Provider {
		case RemoteOpenRouter:
			url = "https://openrouter.ai/api/v1/chat/completions"
		default:
			url = "https://api.openai.com/v1/chat/completions"
		}
	}
	headers := map[string]string{
		"Authorization": "Bearer " + remote.APIKey,
		"Content-Type":  "application/json",
		"Accept":        "text/event-stream",
	}
	return remoteRequest{method: http.MethodPost, url: url, headers: headers, body: payload}, nil
}

func openAIMessages(system string, parts []PromptPart) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(parts)+1)
	if system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	var lastDate string
	for _, part := range parts {
		switch part.Role {
		case RoleTool:
			text, err := json.Marshal(formatWireToolResponse(part, &lastDate))
			if err != nil {
				return nil, fmt.Errorf("encode tool response: %w", err)
			}
			message := map[string]any{"role": "tool", "name": part.ToolName, "content": string(text)}
			if part.ToolCallID != "" {
				message["tool_call_id"] = part.ToolCallID
			}
			messages = append(messages, message)
		case RoleModel:
			message := map[string]any{"role": "assistant", "content": formatWirePartText(part, &lastDate)}
			if len(part.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(part.ToolCalls))
				for _, call := range part.ToolCalls {
					args, err := remoteToolArguments(call)
					if err != nil {
						return nil, err
					}
					calls = append(calls, map[string]any{
						"id":   call.ID,
						"type": "function",
						"function": map[string]any{
							"name":      call.Name,
							"arguments": string(args),
						},
					})
				}
				message["tool_calls"] = calls
			}
			messages = append(messages, message)
		default:
			message := map[string]any{"role": "user", "content": formatWirePartText(part, &lastDate)}
			if part.ImageBase64 != "" {
				message["content"] = []map[string]any{
					{"type": "text", "text": part.Text},
					{"type": "image_url", "image_url": map[string]string{"url": "data:image/jpeg;base64," + part.ImageBase64}},
				}
			}
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func buildAnthropicRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error) {
	oauthMode := isAnthropicOAuthToken(remote.Credential, remote.APIKey)
	messages, err := anthropicMessagesWithOAuth(parts, oauthMode)
	if err != nil {
		return remoteRequest{}, err
	}
	body := map[string]any{
		"model":      remote.Model,
		"max_tokens": 8192,
		"stream":     true,
		"messages":   messages,
	}
	if oauthMode {
		// Anthropic Claude Pro/Max OAuth requires the Claude Code system identity block first
		// (external/pi/packages/ai/src/api/anthropic-messages.ts:1246-1262).
		sysBlocks := []map[string]any{
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
		}
		if cfg.SystemMessage != "" {
			sysBlocks = append(sysBlocks, map[string]any{"type": "text", "text": cfg.SystemMessage})
		}
		body["system"] = sysBlocks
	} else if cfg.SystemMessage != "" {
		body["system"] = cfg.SystemMessage
	}
	if len(cfg.Tools) > 0 {
		tools := make([]map[string]any, 0, len(cfg.Tools))
		for _, decl := range cfg.Tools {
			fn, ok := decl["function"].(map[string]any)
			if !ok {
				return remoteRequest{}, fmt.Errorf("invalid Anthropic tool declaration")
			}
			toolName, _ := fn["name"].(string)
			if oauthMode {
				toolName = toClaudeCodeToolName(toolName)
			}
			tools = append(tools, map[string]any{
				"name":         toolName,
				"description":  fn["description"],
				"input_schema": fn["parameters"],
			})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return remoteRequest{}, fmt.Errorf("encode Anthropic request: %w", err)
	}
	url := remote.BaseURL
	if url == "" {
		url = "https://api.anthropic.com/v1/messages"
	}
	headers := map[string]string{
		"anthropic-version": "2023-06-01",
		"Content-Type":      "application/json",
		"Accept":            "text/event-stream",
	}
	if oauthMode {
		headers["Authorization"] = "Bearer " + remote.APIKey
		headers["anthropic-beta"] = "claude-code-20250219,oauth-2025-04-20"
		headers["user-agent"] = "claude-cli/" + anthropicClaudeCodeVersion
		headers["x-app"] = "cli"
	} else {
		headers["x-api-key"] = remote.APIKey
	}
	if assets.HostOS() == "browser" {
		headers["anthropic-dangerous-direct-browser-access"] = "true"
	}
	return remoteRequest{method: http.MethodPost, url: url, headers: headers, body: payload}, nil
}

func anthropicMessages(parts []PromptPart) ([]map[string]any, error) {
	return anthropicMessagesWithOAuth(parts, false)
}

func anthropicMessagesWithOAuth(parts []PromptPart, oauthMode bool) ([]map[string]any, error) {
	messages := make([]map[string]any, 0, len(parts))
	var lastDate string
	for _, part := range parts {
		if part.Role == RoleTool {
			response, err := json.Marshal(formatWireToolResponse(part, &lastDate))
			if err != nil {
				return nil, fmt.Errorf("encode Anthropic tool response: %w", err)
			}
			messages = append(messages, map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": part.ToolCallID,
					"content":     string(response),
				}},
			})
			continue
		}
		role := "user"
		if part.Role == RoleModel {
			role = "assistant"
		}
		content := make([]map[string]any, 0, 2+len(part.ToolCalls))
		if text := formatWirePartText(part, &lastDate); text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		if part.ImageBase64 != "" {
			content = append(content, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type": "base64", "media_type": "image/jpeg", "data": part.ImageBase64,
				},
			})
		}
		for _, call := range part.ToolCalls {
			args, err := remoteToolArguments(call)
			if err != nil {
				return nil, err
			}
			var input any
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("decode tool arguments: %w", err)
			}
			callName := call.Name
			if oauthMode {
				callName = toClaudeCodeToolName(callName)
			}
			content = append(content, map[string]any{
				"type": "tool_use", "id": call.ID, "name": callName, "input": input,
			})
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}
	return messages, nil
}

func buildGeminiRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error) {
	contents, err := geminiContents(parts)
	if err != nil {
		return remoteRequest{}, err
	}
	body := map[string]any{"contents": contents}
	if cfg.SystemMessage != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": cfg.SystemMessage}}}
	}
	if len(cfg.Tools) > 0 {
		decls := make([]map[string]any, 0, len(cfg.Tools))
		for _, decl := range cfg.Tools {
			fn, ok := decl["function"].(map[string]any)
			if !ok {
				return remoteRequest{}, fmt.Errorf("invalid Gemini tool declaration")
			}
			decls = append(decls, map[string]any{
				"name":        fn["name"],
				"description": fn["description"],
				"parameters":  fn["parameters"],
			})
		}
		body["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return remoteRequest{}, fmt.Errorf("encode Gemini request: %w", err)
	}
	url := remote.BaseURL
	if url == "" {
		url = "https://generativelanguage.googleapis.com/v1beta/models/" + remote.Model + ":streamGenerateContent?alt=sse"
	}
	return remoteRequest{
		method: http.MethodPost,
		url:    url,
		headers: map[string]string{
			"x-goog-api-key": remote.APIKey,
			"Content-Type":   "application/json",
			"Accept":         "text/event-stream",
		},
		body: payload,
	}, nil
}

func geminiContents(parts []PromptPart) ([]map[string]any, error) {
	contents := make([]map[string]any, 0, len(parts))
	var lastDate string
	for _, part := range parts {
		role := "user"
		if part.Role == RoleModel {
			role = "model"
		}
		if part.Role == RoleTool {
			response, err := json.Marshal(formatWireToolResponse(part, &lastDate))
			if err != nil {
				return nil, fmt.Errorf("encode Gemini tool response: %w", err)
			}
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{{
					"functionResponse": map[string]any{
						"name":     part.ToolName,
						"response": map[string]any{"result": json.RawMessage(response)},
					},
				}},
			})
			continue
		}
		partsList := make([]map[string]any, 0, 2+len(part.ToolCalls))
		if text := formatWirePartText(part, &lastDate); text != "" {
			partsList = append(partsList, map[string]any{"text": text})
		}
		if part.ImageBase64 != "" {
			partsList = append(partsList, map[string]any{
				"inlineData": map[string]string{"mimeType": "image/jpeg", "data": part.ImageBase64},
			})
		}
		for _, call := range part.ToolCalls {
			args, err := remoteToolArguments(call)
			if err != nil {
				return nil, err
			}
			var input any
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("decode Gemini tool arguments: %w", err)
			}
			partsList = append(partsList, map[string]any{
				"functionCall": map[string]any{"name": call.Name, "args": input},
			})
		}
		contents = append(contents, map[string]any{"role": role, "parts": partsList})
	}
	return contents, nil
}

func remoteToolArguments(call ToolCall) ([]byte, error) {
	if len(call.RawArguments) > 0 {
		var value any
		if err := json.Unmarshal(call.RawArguments, &value); err != nil {
			return nil, fmt.Errorf("decode raw arguments for tool %q: %w", call.Name, err)
		}
		return json.Marshal(value)
	}
	if call.Arguments == nil {
		return []byte("{}"), nil
	}
	data, err := json.Marshal(call.Arguments)
	if err != nil {
		return nil, fmt.Errorf("encode arguments for tool %q: %w", call.Name, err)
	}
	return data, nil
}

type remoteAccumulator struct {
	provider      RemoteProvider
	wire          remoteWireKind
	declaredTools []map[string]any
	onEvent       func(ModelEvent)
	text          strings.Builder
	tools         map[int]*remoteToolAssembly
}

type remoteToolAssembly struct {
	id        string
	name      string
	arguments strings.Builder
}

func newRemoteAccumulator(provider RemoteProvider, onEvent func(ModelEvent)) *remoteAccumulator {
	var wire remoteWireKind
	switch provider {
	case RemoteOpenAI, RemoteOpenRouter, RemoteCopilot:
		wire = wireOpenAICompletions
	case RemoteAnthropic:
		wire = wireAnthropicMessages
	case RemoteGemini:
		wire = wireGeminiContent
	}
	return newRemoteAccumulatorWithWire(provider, wire, nil, onEvent)
}

func newRemoteAccumulatorWithWire(provider RemoteProvider, wire remoteWireKind, declaredTools []map[string]any, onEvent func(ModelEvent)) *remoteAccumulator {
	return &remoteAccumulator{
		provider:      provider,
		wire:          wire,
		declaredTools: declaredTools,
		onEvent:       onEvent,
		tools:         make(map[int]*remoteToolAssembly),
	}
}

func (a *remoteAccumulator) consume(event, data string) error {
	if data == "" || data == "[DONE]" {
		return nil
	}
	switch a.wire {
	case wireOpenAICompletions:
		return a.consumeOpenAI(data)
	case wireOpenAIResponses:
		return a.consumeOpenAIResponses(event, data)
	case wireAnthropicMessages:
		return a.consumeAnthropic(event, data)
	case wireGeminiContent:
		return a.consumeGemini(data)
	default:
		switch a.provider {
		case RemoteOpenAI, RemoteOpenRouter, RemoteCopilot:
			return a.consumeOpenAI(data)
		case RemoteAnthropic:
			return a.consumeAnthropic(event, data)
		case RemoteGemini:
			return a.consumeGemini(data)
		default:
			return fmt.Errorf("unsupported remote AI provider %q", a.provider)
		}
	}
}

func (a *remoteAccumulator) consumeOpenAIResponses(event, data string) error {
	var item struct {
		Type        string `json:"type"`
		OutputIndex int    `json:"output_index"`
		Delta       string `json:"delta"`
		Arguments   string `json:"arguments"`
		Error       *struct {
			Message string `json:"message"`
		} `json:"error"`
		Response *struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
		Item *struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(data), &item); err != nil {
		return fmt.Errorf("decode OpenAI Responses stream event: %w", err)
	}
	if item.Type == "" {
		item.Type = event
	}
	if item.Error != nil && item.Error.Message != "" {
		return fmt.Errorf("OpenAI Responses: %s", item.Error.Message)
	}
	if item.Response != nil && item.Response.Error != nil && item.Response.Error.Message != "" {
		return fmt.Errorf("OpenAI Responses: %s", item.Response.Error.Message)
	}
	switch item.Type {
	case "response.output_text.delta":
		if item.Delta != "" {
			a.text.WriteString(item.Delta)
			a.onEvent(TextEvent{Text: item.Delta})
		}
	case "response.output_item.added", "response.output_item.done":
		if item.Item != nil && item.Item.Type == "function_call" {
			call := a.tool(item.OutputIndex)
			if item.Item.CallID != "" {
				call.id = item.Item.CallID
			} else if item.Item.ID != "" && call.id == "" {
				call.id = item.Item.ID
			}
			if item.Item.Name != "" {
				call.name = item.Item.Name
			}
			if item.Type == "response.output_item.done" && item.Item.Arguments != "" && call.arguments.Len() == 0 {
				call.arguments.WriteString(item.Item.Arguments)
			}
		}
	case "response.function_call_arguments.delta":
		if item.Delta != "" {
			a.tool(item.OutputIndex).arguments.WriteString(item.Delta)
		}
	case "response.function_call_arguments.done":
		call := a.tool(item.OutputIndex)
		if item.Arguments != "" && call.arguments.Len() == 0 {
			call.arguments.WriteString(item.Arguments)
		}
	}
	return nil
}

func (a *remoteAccumulator) consumeOpenAI(data string) error {
	var chunk struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return fmt.Errorf("decode OpenAI stream event: %w", err)
	}
	if chunk.Error != nil {
		return fmt.Errorf("remote provider: %s", chunk.Error.Message)
	}
	for _, choice := range chunk.Choices {
		if text := choice.Delta.Content; text != "" {
			a.text.WriteString(text)
			a.onEvent(TextEvent{Text: text})
		}
		for _, delta := range choice.Delta.ToolCalls {
			call := a.tool(delta.Index)
			if delta.ID != "" {
				call.id = delta.ID
			}
			if delta.Function.Name != "" {
				call.name += delta.Function.Name
			}
			call.arguments.WriteString(delta.Function.Arguments)
		}
	}
	return nil
}

func (a *remoteAccumulator) consumeAnthropic(event, data string) error {
	var item struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		ContentBlock struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &item); err != nil {
		return fmt.Errorf("decode Anthropic stream event: %w", err)
	}
	if item.Type == "" {
		item.Type = event
	}
	if item.Type == "error" && item.Error != nil {
		return fmt.Errorf("Anthropic: %s", item.Error.Message)
	}
	switch item.Type {
	case "content_block_start":
		if item.ContentBlock.Type == "tool_use" {
			call := a.tool(item.Index)
			call.id = item.ContentBlock.ID
			call.name = fromClaudeCodeToolName(item.ContentBlock.Name, a.declaredTools)
			if len(item.ContentBlock.Input) > 0 {
				data, err := json.Marshal(item.ContentBlock.Input)
				if err != nil {
					return fmt.Errorf("encode Anthropic tool input: %w", err)
				}
				call.arguments.Write(data)
			}
		}
	case "content_block_delta":
		switch item.Delta.Type {
		case "text_delta":
			if item.Delta.Text != "" {
				a.text.WriteString(item.Delta.Text)
				a.onEvent(TextEvent{Text: item.Delta.Text})
			}
		case "input_json_delta":
			a.tool(item.Index).arguments.WriteString(item.Delta.PartialJSON)
		}
	}
	return nil
}

func (a *remoteAccumulator) consumeGemini(data string) error {
	var response struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(data), &response); err != nil {
		return fmt.Errorf("decode Gemini stream event: %w", err)
	}
	if response.Error != nil {
		return fmt.Errorf("Gemini: %s", response.Error.Message)
	}
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				a.text.WriteString(part.Text)
				a.onEvent(TextEvent{Text: part.Text})
			}
			if part.FunctionCall != nil {
				args, err := json.Marshal(part.FunctionCall.Args)
				if err != nil {
					return fmt.Errorf("encode Gemini tool arguments: %w", err)
				}
				a.tools[len(a.tools)] = &remoteToolAssembly{
					name:      part.FunctionCall.Name,
					arguments: strings.Builder{},
				}
				a.tools[len(a.tools)-1].id = fmt.Sprintf("gemini-call-%d", len(a.tools)-1)
				a.tools[len(a.tools)-1].arguments.Write(args)
			}
		}
	}
	return nil
}

func (a *remoteAccumulator) tool(index int) *remoteToolAssembly {
	call := a.tools[index]
	if call == nil {
		call = &remoteToolAssembly{}
		a.tools[index] = call
	}
	return call
}

func (a *remoteAccumulator) toolCalls() ([]ToolCall, error) {
	indices := make([]int, 0, len(a.tools))
	for index := range a.tools {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]ToolCall, 0, len(indices))
	for _, index := range indices {
		assembled := a.tools[index]
		raw := strings.TrimSpace(assembled.arguments.String())
		if raw == "" {
			raw = "{}"
		}
		var arguments map[string]any
		if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
			return nil, fmt.Errorf("decode arguments for remote tool %q: %w", assembled.name, err)
		}
		calls = append(calls, ToolCall{
			ID:           assembled.id,
			Name:         assembled.name,
			Arguments:    arguments,
			RawArguments: json.RawMessage(raw),
		})
	}
	return calls, nil
}

func (a *remoteAccumulator) finish(calls []ToolCall) error {
	if len(calls) > 0 {
		for _, call := range calls {
			if call.Name == "" {
				return errors.New("remote provider returned a tool call without a name")
			}
			if call.ID == "" {
				return fmt.Errorf("remote provider returned tool %q without a call ID", call.Name)
			}
		}
		a.onEvent(ToolCallEvent{ToolCalls: calls})
	}
	return nil
}
