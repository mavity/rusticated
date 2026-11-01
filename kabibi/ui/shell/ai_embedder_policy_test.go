package shell

import (
	"context"
	"strings"
	"testing"

	"github.com/mavity/rusticated/kabibi/config"
)

func TestOneShotLocalEmbedderRequiresExplicitEmbeddingConsent(t *testing.T) {
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())
	if err := config.SaveAI(config.AIConfig{
		Configured:   true,
		Mode:         "local",
		TierID:       "gemma-4-e2b",
		ConsentGiven: true,
	}); err != nil {
		t.Fatal(err)
	}

	embedder, closeEmbedder, _, err := resolveOneShotAIToolEmbedder(context.Background())
	if err == nil || !strings.Contains(err.Error(), "run `ai setup`") {
		t.Fatalf("local embedder resolution error = %v, want setup-consent error", err)
	}
	if embedder != nil || closeEmbedder != nil {
		t.Fatal("local embedder was returned without embedding consent")
	}
}

func TestOneShotRemoteEmbedderDoesNotFallBackToLocal(t *testing.T) {
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())
	if err := config.SaveAI(config.AIConfig{
		Configured:   true,
		Mode:         "remote",
		Provider:     "anthropic",
		Model:        "claude",
		ConsentGiven: true,
	}); err != nil {
		t.Fatal(err)
	}

	embedder, _, diagnostic, err := resolveOneShotAIToolEmbedder(context.Background())
	if err != nil {
		t.Fatalf("remote embedder resolution: %v", err)
	}
	if embedder != nil {
		t.Fatal("unsupported remote provider unexpectedly supplied an embedder")
	}
	if !strings.Contains(diagnostic, "remote embeddings unavailable") {
		t.Fatalf("remote embedder diagnostic = %q", diagnostic)
	}
}
