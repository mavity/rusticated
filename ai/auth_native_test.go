//go:build !wasip1

package ai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRemoteCredentialSendsProviderAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("request = %s %s, want GET /models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	err := ValidateRemoteCredential(context.Background(), RemoteEngineConfig{
		Provider: RemoteOpenAI,
		Model:    "test-model",
		APIKey:   "test-token",
		BaseURL:  server.URL + "/models",
	})
	if err != nil {
		t.Fatalf("ValidateRemoteCredential() error = %v", err)
	}
}

func TestValidateRemoteCredentialRejectsUnauthorizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "invalid credential")
	}))
	defer server.Close()

	err := ValidateRemoteCredential(context.Background(), RemoteEngineConfig{
		Provider: RemoteOpenAI,
		Model:    "test-model",
		APIKey:   "test-token",
		BaseURL:  server.URL + "/models",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("ValidateRemoteCredential() error = %v, want HTTP 401", err)
	}
}

func TestValidateRemoteCredentialRejectsInsecureRemoteEndpoint(t *testing.T) {
	err := ValidateRemoteCredential(context.Background(), RemoteEngineConfig{
		Provider: RemoteOpenAI,
		Model:    "test-model",
		APIKey:   "test-token",
		BaseURL:  "http://example.com/v1/models",
	})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("ValidateRemoteCredential() error = %v, want insecure endpoint rejection", err)
	}
}
