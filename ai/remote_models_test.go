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
