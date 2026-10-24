package ai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"ai/assets"
)

func ValidateRemoteCredential(ctx context.Context, cfg RemoteEngineConfig) error {
	if cfg.APIKey == "" && cfg.Credential != nil {
		cfg.APIKey = cfg.Credential.SecretToken()
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("remote AI credential must not be empty")
	}
	request := remoteRequest{
		method: http.MethodGet,
		headers: map[string]string{
			"Accept": "application/json",
		},
	}
	switch cfg.Provider {
	case RemoteOpenAI:
		request.url = cfg.BaseURL
		if request.url == "" {
			request.url = "https://api.openai.com/v1/models"
		}
		request.headers["Authorization"] = "Bearer " + cfg.APIKey
	case RemoteOpenRouter:
		request.url = cfg.BaseURL
		if request.url == "" {
			request.url = "https://openrouter.ai/api/v1/models"
		}
		request.headers["Authorization"] = "Bearer " + cfg.APIKey
	case RemoteAnthropic:
		request.url = cfg.BaseURL
		if request.url == "" {
			request.url = "https://api.anthropic.com/v1/models"
		}
		request.headers["anthropic-version"] = "2023-06-01"
		if isAnthropicOAuthToken(cfg.Credential, cfg.APIKey) {
			request.headers["Authorization"] = "Bearer " + cfg.APIKey
			request.headers["anthropic-beta"] = "claude-code-20250219,oauth-2025-04-20"
		} else {
			request.headers["x-api-key"] = cfg.APIKey
		}
	case RemoteGemini:
		request.url = cfg.BaseURL
		if request.url == "" {
			request.url = "https://generativelanguage.googleapis.com/v1beta/models"
		}
		request.headers["x-goog-api-key"] = cfg.APIKey
	case RemoteCopilot:
		enterpriseDomain := ""
		if cfg.Credential != nil {
			enterpriseDomain = cfg.Credential.MetaString("enterprise_url")
		}
		request.url = cfg.BaseURL
		if request.url == "" {
			request.url = DeriveGitHubCopilotBaseURL(cfg.APIKey, enterpriseDomain) + "/models"
		}
		request.headers["Authorization"] = "Bearer " + cfg.APIKey
		request.headers["X-GitHub-Api-Version"] = copilotAPIVersion
		for k, v := range copilotStaticHeaders {
			request.headers[k] = v
		}
	default:
		return fmt.Errorf("unsupported remote AI provider %q", cfg.Provider)
	}
	if err := validateRemoteBaseURL(request.url); err != nil {
		return fmt.Errorf("validate %s credential endpoint: %w", cfg.Provider, err)
	}
	response, err := remoteHTTPRequest(ctx, cfg.HTTPClient, request)
	if err != nil {
		if assets.HostOS() == "browser" {
			return fmt.Errorf("validate %s credential; browser network/CORS restrictions may block the provider: %w", cfg.Provider, err)
		}
		return fmt.Errorf("validate %s credential: %w", cfg.Provider, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body := strings.TrimSpace(string(response.Body))
		if body == "" {
			body = "empty response body"
		}
		return fmt.Errorf("validate %s credential returned HTTP %s: %s", cfg.Provider, response.Status, body)
	}
	return nil
}
