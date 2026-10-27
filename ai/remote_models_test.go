package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestDefaultRemoteModel(t *testing.T) {
	for provider, want := range map[RemoteProvider]string{
		RemoteOpenAI:     "gpt-6-luna",
		RemoteOpenRouter: "openai/gpt-6-luna",
		RemoteCopilot:    "gpt-6-luna",
		RemoteAnthropic:  "",
		RemoteGemini:     "",
	} {
		if got := DefaultRemoteModel(provider); got != want {
			t.Errorf("DefaultRemoteModel(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestIsExcludedRemoteModel(t *testing.T) {
	tests := []struct {
		model   string
		exclude bool
	}{
		{model: "grok-3", exclude: true},
		{model: "x-ai/model", exclude: true},
		{model: "xai/model", exclude: true},
		{model: "twitter/model", exclude: true},
		{model: "x/model", exclude: true},
		{model: "text-embedding-3-small", exclude: false},
		{model: "gpt-6-luna", exclude: false},
	}
	for _, test := range tests {
		if got := IsExcludedRemoteModel(test.model); got != test.exclude {
			t.Errorf("IsExcludedRemoteModel(%q) = %t, want %t", test.model, got, test.exclude)
		}
	}
}

func TestListRemoteModelsOpenAIPaginatesAndSorts(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("limit") != "100" {
			t.Errorf("limit = %q, want 100", r.URL.Query().Get("limit"))
		}
		if requests == 1 {
			if r.URL.Query().Get("after") != "" {
				t.Errorf("first page after = %q, want empty", r.URL.Query().Get("after"))
			}
			_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6-sol"}],"has_more":true,"last_id":"gpt-6-sol"}`)
			return
		}
		if r.URL.Query().Get("after") != "gpt-6-sol" {
			t.Errorf("second page after = %q, want gpt-6-sol", r.URL.Query().Get("after"))
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-6-astra"},{"id":"gpt-6-luna"}],"has_more":false}`)
	}))
	defer server.Close()

	models, err := ListRemoteModels(context.Background(), RemoteEngineConfig{
		Provider:   RemoteOpenAI,
		Credential: &Credential{Type: CredentialAPIKey, Key: "test-token"},
		BaseURL:    server.URL + "/v1/models",
	})
	if err != nil {
		t.Fatalf("ListRemoteModels() error = %v", err)
	}
	if want := []string{"gpt-6-astra", "gpt-6-luna", "gpt-6-sol"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("ListRemoteModels() = %v, want %v", models, want)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestListRemoteModelsFiltersXTwitterFamilies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"grok-3"},{"id":"x-ai/model"},{"id":"xai/model"},{"id":"twitter/model"},{"id":"x/model"},{"id":"text-embedding-3-small"}]}`)
	}))
	defer server.Close()

	models, err := ListRemoteModels(context.Background(), RemoteEngineConfig{
		Provider:   RemoteOpenAI,
		Credential: &Credential{Type: CredentialAPIKey, Key: "test-token"},
		BaseURL:    server.URL + "/v1/models",
	})
	if err != nil {
		t.Fatalf("ListRemoteModels() error = %v", err)
	}
	if want := []string{"gpt-6-luna", "text-embedding-3-small"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("ListRemoteModels() = %v, want X/Twitter families removed: %v", models, want)
	}
}

func TestListRemoteModelsGeminiPaginatesGenerationModels(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("x-goog-api-key") != "test-token" {
			t.Errorf("x-goog-api-key = %q, want test-token", r.Header.Get("x-goog-api-key"))
		}
		if requests == 1 {
			_, _ = fmt.Fprint(w, `{"models":[{"name":"models/gemini-z","supportedGenerationMethods":["generateContent"]},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"page-2"}`)
			return
		}
		if got := r.URL.Query().Get("pageToken"); got != "page-2" {
			t.Errorf("pageToken = %q, want page-2", got)
		}
		_, _ = fmt.Fprint(w, `{"models":[{"name":"models/gemini-a","supportedGenerationMethods":["generateContent"]}]}`)
	}))
	defer server.Close()

	models, err := ListRemoteModels(context.Background(), RemoteEngineConfig{
		Provider:   RemoteGemini,
		Credential: &Credential{Type: CredentialAPIKey, Key: "test-token"},
		BaseURL:    server.URL + "/v1beta/models",
	})
	if err != nil {
		t.Fatalf("ListRemoteModels() error = %v", err)
	}
	if want := []string{"gemini-a", "gemini-z"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("ListRemoteModels() = %v, want %v", models, want)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestListRemoteModelsAnthropicPaginatesByLastID(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("x-api-key") != "test-token" {
			t.Errorf("x-api-key = %q, want test-token", r.Header.Get("x-api-key"))
		}
		if r.URL.Query().Get("limit") != "1000" {
			t.Errorf("limit = %q, want 1000", r.URL.Query().Get("limit"))
		}
		if requests == 1 {
			_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-a"}],"has_more":true,"last_id":"claude-a"}`)
			return
		}
		if got := r.URL.Query().Get("after_id"); got != "claude-a" {
			t.Errorf("after_id = %q, want claude-a", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-b"}],"has_more":false}`)
	}))
	defer server.Close()

	models, err := ListRemoteModels(context.Background(), RemoteEngineConfig{
		Provider:   RemoteAnthropic,
		Credential: &Credential{Type: CredentialAPIKey, Key: "test-token"},
		BaseURL:    server.URL + "/v1/models",
	})
	if err != nil {
		t.Fatalf("ListRemoteModels() error = %v", err)
	}
	if want := []string{"claude-a", "claude-b"}; !reflect.DeepEqual(models, want) {
		t.Fatalf("ListRemoteModels() = %v, want %v", models, want)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestListRemoteModelsMarksRejectedCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "invalid credential")
	}))
	defer server.Close()

	_, err := ListRemoteModels(context.Background(), RemoteEngineConfig{
		Provider:   RemoteOpenRouter,
		APIKey:     "bad-token",
		BaseURL:    server.URL + "/models",
		HTTPClient: server.Client(),
	})
	if !errors.Is(err, ErrRemoteCredentialRejected) {
		t.Fatalf("ListRemoteModels() error = %v, want ErrRemoteCredentialRejected", err)
	}
}

func TestListRemoteModelDetailsReadsDeclaredInputLimits(t *testing.T) {
	tests := []struct {
		name     string
		provider RemoteProvider
		response string
		want     int
	}{
		{
			name:     "OpenRouter uses conservative route limit",
			provider: RemoteOpenRouter,
			response: `{"data":[{"id":"model-a","context_length":131072,"top_provider":{"context_length":65536}}]}`,
			want:     65536,
		},
		{
			name:     "Anthropic input limit",
			provider: RemoteAnthropic,
			response: `{"data":[{"id":"model-a","max_input_tokens":200000}]}`,
			want:     200000,
		},
		{
			name:     "Gemini input limit",
			provider: RemoteGemini,
			response: `{"models":[{"name":"models/model-a","supportedGenerationMethods":["generateContent"],"inputTokenLimit":1048576}]}`,
			want:     1048576,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.response)
			}))
			defer server.Close()

			models, err := ListRemoteModelDetails(context.Background(), RemoteEngineConfig{
				Provider:   test.provider,
				Credential: &Credential{Type: CredentialAPIKey, Key: "test-token"},
				BaseURL:    server.URL + "/models",
			})
			if err != nil {
				t.Fatalf("ListRemoteModelDetails() error = %v", err)
			}
			if len(models) != 1 || models[0].ID != "model-a" || models[0].MaxInputTokens != test.want {
				t.Fatalf("ListRemoteModelDetails() = %+v, want model-a with %d input tokens", models, test.want)
			}
		})
	}
}

func TestListRemoteModelDetailsReadsCopilotNestedInputLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("request path = %q, want /models", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"copilot-model","model_picker_enabled":true,"capabilities":{"limits":{"max_prompt_tokens":128000,"max_context_window_tokens":200000}}}]}`)
	}))
	defer server.Close()

	models, err := ListRemoteModelDetails(context.Background(), RemoteEngineConfig{
		Provider:   RemoteCopilot,
		Credential: &Credential{Type: CredentialAPIKey, Key: "proxy-ep=" + server.URL + ";"},
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("ListRemoteModelDetails() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "copilot-model" || models[0].MaxInputTokens != 128000 {
		t.Fatalf("ListRemoteModelDetails() = %+v, want copilot-model with 128000 input tokens", models)
	}
}

func TestListRemoteModelDetailsLeavesMissingLimitsUnset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"model-without-limit"}]}`)
	}))
	defer server.Close()

	models, err := ListRemoteModelDetails(context.Background(), RemoteEngineConfig{
		Provider:   RemoteOpenAI,
		Credential: &Credential{Type: CredentialAPIKey, Key: "test-token"},
		BaseURL:    server.URL + "/models",
	})
	if err != nil {
		t.Fatalf("ListRemoteModelDetails() error = %v", err)
	}
	if len(models) != 1 || models[0].MaxInputTokens != 0 {
		t.Fatalf("ListRemoteModelDetails() = %+v, want unset input limit", models)
	}
}
