package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const maxRemoteModelCatalogPages = 100

// RemoteModelInfo contains provider-declared limits for a selectable model.
type RemoteModelInfo struct {
	ID             string
	MaxInputTokens int
}

// RemoteModelDisplayName removes a duplicated namespace prefix from a model ID.
func RemoteModelDisplayName(model string) string {
	namespace, name, ok := strings.Cut(model, "/")
	if ok && strings.HasPrefix(strings.ToLower(name), strings.ToLower(namespace)+"-") {
		return name
	}
	return model
}

// DefaultRemoteModel returns the preferred default model ID for a provider.
func DefaultRemoteModel(provider RemoteProvider) string {
	switch provider {
	case RemoteOpenRouter:
		return "openai/gpt-6-luna"
	case RemoteOpenAI, RemoteCopilot:
		return "gpt-6-luna"
	default:
		return ""
	}
}

// ListRemoteModels fetches the provider's selectable text-generation model IDs.
func ListRemoteModels(ctx context.Context, cfg RemoteEngineConfig) ([]string, error) {
	details, err := ListRemoteModelDetails(ctx, cfg)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(details))
	for _, model := range details {
		models = append(models, model.ID)
	}
	return models, nil
}

// ListRemoteModelDetails fetches selectable models and any declared input limits.
func ListRemoteModelDetails(ctx context.Context, cfg RemoteEngineConfig) ([]RemoteModelInfo, error) {
	if cfg.APIKey == "" && cfg.Credential != nil {
		cfg.APIKey = cfg.Credential.SecretToken()
	}
	if cfg.APIKey == "" && cfg.Provider != RemoteCustom {
		return nil, fmt.Errorf("remote AI credential must not be empty")
	}
	catalogProvider := cfg.Provider
	if cfg.Provider == RemoteCustom {
		catalogProvider = remoteCatalogProvider(cfg.Protocol)
	}
	if cfg.Provider == RemoteCopilot {
		enterpriseDomain := ""
		if cfg.Credential != nil {
			enterpriseDomain = cfg.Credential.MetaString("enterprise_url")
		}
		catalog, _, err := fetchGitHubCopilotModelCatalogDetails(ctx, cfg.APIKey, enterpriseDomain, cfg.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("fetch %s model catalog: %w", cfg.Provider, err)
		}
		return sortedUniqueRemoteModels(catalog), nil
	}

	endpoint, err := remoteModelsEndpoint(cfg)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"Accept": "application/json"}
	switch cfg.Provider {
	case RemoteOpenAI, RemoteOpenRouter:
		if cfg.APIKey != "" {
			headers["Authorization"] = "Bearer " + cfg.APIKey
		}
	case RemoteAnthropic:
		headers["anthropic-version"] = "2023-06-01"
		if isAnthropicOAuthToken(cfg.Credential, cfg.APIKey) {
			headers["Authorization"] = "Bearer " + cfg.APIKey
			headers["anthropic-beta"] = "claude-code-20250219,oauth-2025-04-20"
		} else if cfg.APIKey != "" {
			headers["x-api-key"] = cfg.APIKey
		}
	case RemoteGemini:
		if cfg.APIKey != "" {
			headers["x-goog-api-key"] = cfg.APIKey
		}
	case RemoteCustom:
		switch cfg.Protocol {
		case RemoteProtocolOpenAIChatCompletions, RemoteProtocolOpenAIResponses:
			if cfg.APIKey != "" {
				headers["Authorization"] = "Bearer " + cfg.APIKey
			}
		case RemoteProtocolAnthropicMessages:
			headers["anthropic-version"] = "2023-06-01"
			if cfg.APIKey != "" {
				headers["x-api-key"] = cfg.APIKey
			}
		case RemoteProtocolGeminiGenerateContent:
			if cfg.APIKey != "" {
				headers["x-goog-api-key"] = cfg.APIKey
			}
		case RemoteProtocolCopilot:
			if cfg.APIKey != "" {
				headers["Authorization"] = "Bearer " + cfg.APIKey
			}
			headers["X-GitHub-Api-Version"] = copilotAPIVersion
			for key, value := range copilotStaticHeaders {
				headers[key] = value
			}
		}
	default:
		return nil, fmt.Errorf("unsupported remote AI provider %q", cfg.Provider)
	}

	var models []RemoteModelInfo
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := 0; page < maxRemoteModelCatalogPages; page++ {
		pageURL, err := remoteModelsPageURL(endpoint, catalogProvider, cursor)
		if err != nil {
			return nil, fmt.Errorf("build %s model catalog request: %w", cfg.Provider, err)
		}
		response, err := remoteHTTPRequest(ctx, cfg.HTTPClient, remoteRequest{
			method:  http.MethodGet,
			url:     pageURL,
			headers: headers,
		})
		if err != nil {
			return nil, fmt.Errorf("fetch %s model catalog: %w", cfg.Provider, err)
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			body := strings.TrimSpace(string(response.Body))
			if body == "" {
				body = "empty response body"
			}
			err := fmt.Errorf("fetch %s model catalog returned HTTP %s: %s", cfg.Provider, response.Status, body)
			if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
				return nil, fmt.Errorf("%w: %w", ErrRemoteCredentialRejected, err)
			}
			return nil, err
		}

		var payload struct {
			Data []struct {
				ID              string `json:"id"`
				ContextLength   int    `json:"context_length"`
				MaxInputTokens  int    `json:"max_input_tokens"`
				MaxPromptTokens int    `json:"max_prompt_tokens"`
				ContextWindow   int    `json:"context_window"`
				TopProvider     *struct {
					ContextLength int `json:"context_length"`
				} `json:"top_provider"`
				Capabilities *struct {
					Limits *struct {
						MaxPromptTokens        int `json:"max_prompt_tokens"`
						MaxContextWindowTokens int `json:"max_context_window_tokens"`
						MaxInputTokens         int `json:"max_input_tokens"`
					} `json:"limits"`
				} `json:"capabilities"`
			} `json:"data"`
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
				InputTokenLimit            int      `json:"inputTokenLimit"`
				MaxInputTokens             int      `json:"max_input_tokens"`
			} `json:"models"`
			HasMore       bool   `json:"has_more"`
			LastID        string `json:"last_id"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return nil, fmt.Errorf("decode %s model catalog: %w", cfg.Provider, err)
		}
		for _, model := range payload.Data {
			if model.ID != "" {
				limits := []int{
					model.ContextLength,
					model.MaxInputTokens,
					model.MaxPromptTokens,
					model.ContextWindow,
				}
				if model.TopProvider != nil {
					limits = append(limits, model.TopProvider.ContextLength)
				}
				if model.Capabilities != nil && model.Capabilities.Limits != nil {
					limits = append(limits,
						model.Capabilities.Limits.MaxPromptTokens,
						model.Capabilities.Limits.MaxContextWindowTokens,
						model.Capabilities.Limits.MaxInputTokens,
					)
				}
				models = append(models, RemoteModelInfo{ID: model.ID, MaxInputTokens: conservativePositiveLimit(limits...)})
			}
		}
		for _, model := range payload.Models {
			if model.Name == "" || !hasGenerationMethod(model.SupportedGenerationMethods) {
				continue
			}
			models = append(models, RemoteModelInfo{
				ID:             strings.TrimPrefix(model.Name, "models/"),
				MaxInputTokens: conservativePositiveLimit(model.InputTokenLimit, model.MaxInputTokens),
			})
		}

		next := ""
		switch catalogProvider {
		case RemoteOpenAI:
			if payload.HasMore {
				next = payload.LastID
				if next == "" && len(payload.Data) > 0 {
					next = payload.Data[len(payload.Data)-1].ID
				}
			}
		case RemoteAnthropic:
			if payload.HasMore {
				next = payload.LastID
				if next == "" && len(payload.Data) > 0 {
					next = payload.Data[len(payload.Data)-1].ID
				}
			}
		case RemoteGemini:
			next = payload.NextPageToken
		}
		if next == "" {
			return sortedUniqueRemoteModels(models), nil
		}
		if _, exists := seenCursors[next]; exists {
			return nil, fmt.Errorf("%s model catalog repeated pagination cursor %q", cfg.Provider, next)
		}
		seenCursors[next] = struct{}{}
		cursor = next
	}
	return nil, fmt.Errorf("%s model catalog exceeded %d pages", cfg.Provider, maxRemoteModelCatalogPages)
}

func remoteModelsEndpoint(cfg RemoteEngineConfig) (string, error) {
	if cfg.BaseURL != "" {
		if err := validateRemoteBaseURL(cfg.BaseURL); err != nil {
			return "", fmt.Errorf("validate %s model catalog endpoint: %w", cfg.Provider, err)
		}
		if cfg.Provider == RemoteCustom {
			switch cfg.Protocol {
			case RemoteProtocolOpenAIChatCompletions, RemoteProtocolOpenAIResponses:
				return customEndpoint(cfg.BaseURL, "/v1/models"), nil
			case RemoteProtocolAnthropicMessages:
				return customEndpoint(cfg.BaseURL, "/v1/models"), nil
			case RemoteProtocolGeminiGenerateContent:
				return customEndpoint(cfg.BaseURL, "/v1beta/models"), nil
			case RemoteProtocolCopilot:
				return customEndpoint(cfg.BaseURL, "/models"), nil
			default:
				return "", fmt.Errorf("unsupported custom remote API protocol %q", cfg.Protocol)
			}
		}
		return cfg.BaseURL, nil
	}
	switch cfg.Provider {
	case RemoteOpenAI:
		return "https://api.openai.com/v1/models", nil
	case RemoteOpenRouter:
		return "https://openrouter.ai/api/v1/models", nil
	case RemoteAnthropic:
		return "https://api.anthropic.com/v1/models", nil
	case RemoteGemini:
		return "https://generativelanguage.googleapis.com/v1beta/models", nil
	default:
		return "", fmt.Errorf("unsupported remote AI provider %q", cfg.Provider)
	}
}

func remoteCatalogProvider(protocol RemoteAPIProtocol) RemoteProvider {
	switch protocol {
	case RemoteProtocolOpenAIChatCompletions, RemoteProtocolOpenAIResponses, RemoteProtocolCopilot:
		return RemoteOpenAI
	case RemoteProtocolAnthropicMessages:
		return RemoteAnthropic
	case RemoteProtocolGeminiGenerateContent:
		return RemoteGemini
	default:
		return RemoteCustom
	}
}

func remoteModelsPageURL(endpoint string, provider RemoteProvider, cursor string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	switch provider {
	case RemoteOpenAI:
		query.Set("limit", "100")
		if cursor != "" {
			query.Set("after", cursor)
		}
	case RemoteAnthropic:
		query.Set("limit", "1000")
		if cursor != "" {
			query.Set("after_id", cursor)
		}
	case RemoteGemini:
		if cursor != "" {
			query.Set("pageToken", cursor)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func hasGenerationMethod(methods []string) bool {
	for _, method := range methods {
		if method == "generateContent" {
			return true
		}
	}
	return false
}

func conservativePositiveLimit(limits ...int) int {
	result := 0
	for _, limit := range limits {
		if limit <= 0 {
			continue
		}
		if result == 0 || limit < result {
			result = limit
		}
	}
	return result
}

func sortedUniqueRemoteModels(models []RemoteModelInfo) []RemoteModelInfo {
	limitsByID := make(map[string]int, len(models))
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		if existing, ok := limitsByID[model.ID]; !ok {
			limitsByID[model.ID] = model.MaxInputTokens
		} else {
			limitsByID[model.ID] = conservativePositiveLimit(existing, model.MaxInputTokens)
		}
	}
	ids := make([]string, 0, len(limitsByID))
	for id := range limitsByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	unique := make([]RemoteModelInfo, 0, len(ids))
	for _, id := range ids {
		unique = append(unique, RemoteModelInfo{ID: id, MaxInputTokens: limitsByID[id]})
	}
	return unique
}
