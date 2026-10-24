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
)

type RemoteEngineConfig struct {
	Provider   RemoteProvider
	Model      string
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func NewRemoteEngine(cfg RemoteEngineConfig) (*Engine, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("remote AI model must not be empty")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("remote AI credential must not be empty")
	}
	switch cfg.Provider {
	case RemoteOpenAI, RemoteOpenRouter, RemoteAnthropic, RemoteGemini:
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
	req, err := buildRemoteRequest(c.engine.cfg, config, requestParts)
	if err != nil {
		return err
	}
	acc := newRemoteAccumulator(c.engine.cfg.Provider, onEvent)
	if err := remoteStreamRequest(streamCtx, c.engine.cfg.HTTPClient, req, acc.consume); err != nil {
		return err
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

func buildRemoteRequest(remote RemoteEngineConfig, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error) {
	switch remote.Provider {
	case RemoteOpenAI, RemoteOpenRouter:
		return buildOpenAIRequest(remote, cfg, parts)
	case RemoteAnthropic:
		return buildAnthropicRequest(remote, cfg, parts)
	case RemoteGemini:
		return buildGeminiRequest(remote, cfg, parts)
	default:
		return remoteRequest{}, fmt.Errorf("unsupported remote AI provider %q", remote.Provider)
	}
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
	messages, err := anthropicMessages(parts)
	if err != nil {
		return remoteRequest{}, err
	}
	body := map[string]any{
		"model":      remote.Model,
		"max_tokens": 8192,
		"stream":     true,
		"messages":   messages,
	}
	if cfg.SystemMessage != "" {
		body["system"] = cfg.SystemMessage
	}
	if len(cfg.Tools) > 0 {
		tools := make([]map[string]any, 0, len(cfg.Tools))
		for _, decl := range cfg.Tools {
			fn, ok := decl["function"].(map[string]any)
			if !ok {
				return remoteRequest{}, fmt.Errorf("invalid Anthropic tool declaration")
			}
			tools = append(tools, map[string]any{
				"name":         fn["name"],
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
		"x-api-key":         remote.APIKey,
		"anthropic-version": "2023-06-01",
		"Content-Type":      "application/json",
		"Accept":            "text/event-stream",
	}
	if assets.HostOS() == "browser" {
		headers["anthropic-dangerous-direct-browser-access"] = "true"
	}
	return remoteRequest{method: http.MethodPost, url: url, headers: headers, body: payload}, nil
}

func anthropicMessages(parts []PromptPart) ([]map[string]any, error) {
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
			content = append(content, map[string]any{
				"type": "tool_use", "id": call.ID, "name": call.Name, "input": input,
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
	provider RemoteProvider
	onEvent  func(ModelEvent)
	text     strings.Builder
	tools    map[int]*remoteToolAssembly
}

type remoteToolAssembly struct {
	id        string
	name      string
	arguments strings.Builder
}

func newRemoteAccumulator(provider RemoteProvider, onEvent func(ModelEvent)) *remoteAccumulator {
	return &remoteAccumulator{provider: provider, onEvent: onEvent, tools: make(map[int]*remoteToolAssembly)}
}

func (a *remoteAccumulator) consume(event, data string) error {
	if data == "" || data == "[DONE]" {
		return nil
	}
	switch a.provider {
	case RemoteOpenAI, RemoteOpenRouter:
		return a.consumeOpenAI(data)
	case RemoteAnthropic:
		return a.consumeAnthropic(event, data)
	case RemoteGemini:
		return a.consumeGemini(data)
	default:
		return fmt.Errorf("unsupported remote AI provider %q", a.provider)
	}
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
			call.name = item.ContentBlock.Name
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
