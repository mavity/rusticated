package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestChooseRemoteEmbeddingModel(t *testing.T) {
	tests := []struct {
		name       string
		provider   RemoteProvider
		candidates []remoteEmbeddingCandidate
		want       string
	}{
		{
			name:     "preferred model",
			provider: RemoteOpenAI,
			candidates: []remoteEmbeddingCandidate{
				{id: "text-embedding-3-large", pricePerMillionTokens: 0.13},
				{id: "text-embedding-3-small", pricePerMillionTokens: 0.02},
			},
			want: "text-embedding-3-small",
		},
		{
			name:     "cheapest known fallback",
			provider: RemoteOpenAI,
			candidates: []remoteEmbeddingCandidate{
				{id: "text-embedding-3-large", pricePerMillionTokens: 0.13},
				{id: "text-embedding-ada-002", pricePerMillionTokens: 0.10},
			},
			want: "text-embedding-ada-002",
		},
		{
			name:     "no candidate",
			provider: RemoteOpenAI,
			want:     "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := chooseRemoteEmbeddingModel(test.provider, test.candidates); got != test.want {
				t.Fatalf("chooseRemoteEmbeddingModel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveRemoteEmbedderRejectsOpenAIOAuthBeforeRefresh(t *testing.T) {
	embedder, diagnostic := ResolveRemoteEmbedder(context.Background(), RemoteEngineConfig{
		Provider:   RemoteOpenAI,
		Credential: &Credential{Type: CredentialOAuth, Key: "oauth-token", ExpiresAt: 1},
	})
	if embedder != nil {
		t.Fatal("OAuth credential unexpectedly initialized an OpenAI embedder")
	}
	if diagnostic == "" {
		t.Fatal("OAuth rejection did not explain why embeddings are unavailable")
	}
}

func TestResolveRemoteEmbedderUsesCheapestPricedOpenRouterFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("output_modalities") != "embeddings" {
			t.Errorf("output_modalities = %q, want embeddings", r.URL.Query().Get("output_modalities"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":           "other/embed-expensive",
					"architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"embeddings"}},
					"pricing":      map[string]string{"prompt": "0.00000008"},
				},
				{
					"id":           "other/embed-cheapest",
					"architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"embeddings"}},
					"pricing":      map[string]string{"prompt": "0.00000002"},
				},
				{
					"id":           "other/not-an-embedder",
					"architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"text"}},
					"pricing":      map[string]string{"prompt": "0.00000001"},
				},
			},
		})
	}))
	defer server.Close()

	embedder, diagnostic := ResolveRemoteEmbedder(context.Background(), RemoteEngineConfig{
		Provider:   RemoteOpenRouter,
		APIKey:     "test-key",
		BaseURL:    server.URL + "/api/v1/models",
		HTTPClient: server.Client(),
	})
	if embedder == nil {
		t.Fatalf("ResolveRemoteEmbedder() returned nil: %s", diagnostic)
	}
	if embedder.model != "other/embed-cheapest" {
		t.Fatalf("selected model = %q, want cheapest supported catalog model", embedder.model)
	}
}

func TestResolveRemoteEmbedderBatchesAndOrdersOpenAIResponses(t *testing.T) {
	var embeddingRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{
					{"id": "text-embedding-3-large"},
					{"id": "text-embedding-ada-002"},
				},
			})
		case "/v1/embeddings":
			embeddingRequests++
			var request struct {
				Model string   `json:"model"`
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode embeddings request: %v", err)
			}
			if request.Model != "text-embedding-ada-002" {
				t.Errorf("request model = %q, want cheapest available fallback", request.Model)
			}
			if !reflect.DeepEqual(request.Input, []string{"query", "document"}) {
				t.Errorf("request input = %#v", request.Input)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"index": 1, "embedding": []float64{0, 3}},
					{"index": 0, "embedding": []float64{3, 4}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	embedder, diagnostic := ResolveRemoteEmbedder(context.Background(), RemoteEngineConfig{
		Provider:   RemoteOpenAI,
		APIKey:     "test-key",
		BaseURL:    server.URL + "/v1/models",
		HTTPClient: server.Client(),
	})
	if embedder == nil {
		t.Fatalf("ResolveRemoteEmbedder() returned nil: %s", diagnostic)
	}
	vectors, err := embedder.EmbedBatchContext(context.Background(), []PromptPart{
		{Role: RoleUser, Text: "query"},
		{Role: RoleModel, Text: "document"},
	})
	if err != nil {
		t.Fatalf("EmbedBatchContext(): %v", err)
	}
	if len(vectors) != 2 || !reflect.DeepEqual(vectors[0], []float32{0.6, 0.8}) || !reflect.DeepEqual(vectors[1], []float32{0, 1}) {
		t.Fatalf("EmbedBatchContext() = %#v, want ordered normalized vectors", vectors)
	}
	if embeddingRequests != 1 {
		t.Fatalf("embedding requests = %d, want one batched request", embeddingRequests)
	}
}
