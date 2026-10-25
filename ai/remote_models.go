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
	if cfg.APIKey == "" && cfg.Credential != nil {
		cfg.APIKey = cfg.Credential.SecretToken()
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("remote AI credential must not be empty")
	}
	if cfg.Provider == RemoteCopilot {
		enterpriseDomain := ""
		if cfg.Credential != nil {
			enterpriseDomain = cfg.Credential.MetaString("enterprise_url")
		}
		catalog, _, err := fetchGitHubCopilotModelCatalog(ctx, cfg.APIKey, enterpriseDomain, cfg.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("fetch %s model catalog: %w", cfg.Provider, err)
		}
		return sortedUniqueModels(catalog), nil
	}

	endpoint, err := remoteModelsEndpoint(cfg)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"Accept": "application/json"}
	switch cfg.Provider {
	case RemoteOpenAI, RemoteOpenRouter:
		headers["Authorization"] = "Bearer " + cfg.APIKey
	case RemoteAnthropic:
		headers["anthropic-version"] = "2023-06-01"
		if isAnthropicOAuthToken(cfg.Credential, cfg.APIKey) {
			headers["Authorization"] = "Bearer " + cfg.APIKey
			headers["anthropic-beta"] = "claude-code-20250219,oauth-2025-04-20"
		} else {
			headers["x-api-key"] = cfg.APIKey
		}
	case RemoteGemini:
		headers["x-goog-api-key"] = cfg.APIKey
	default:
		return nil, fmt.Errorf("unsupported remote AI provider %q", cfg.Provider)
	}

	var models []string
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := 0; page < maxRemoteModelCatalogPages; page++ {
		pageURL, err := remoteModelsPageURL(endpoint, cfg.Provider, cursor)
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
				ID string `json:"id"`
			} `json:"data"`
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
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
				models = append(models, model.ID)
			}
		}
		for _, model := range payload.Models {
			if model.Name == "" || !hasGenerationMethod(model.SupportedGenerationMethods) {
				continue
			}
			models = append(models, strings.TrimPrefix(model.Name, "models/"))
		}

		next := ""
		switch cfg.Provider {
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
			return sortedUniqueModels(models), nil
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

func sortedUniqueModels(models []string) []string {
	sort.Strings(models)
	unique := models[:0]
	for _, model := range models {
		if model == "" || (len(unique) > 0 && unique[len(unique)-1] == model) {
			continue
		}
		unique = append(unique, model)
	}
	return unique
}
