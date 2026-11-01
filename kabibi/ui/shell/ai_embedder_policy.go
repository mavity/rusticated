package shell

import (
	"context"
	"fmt"
	"os"

	"ai"
	"ai/assets"
	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/credstore"
)

func remoteAIEngineConfig(ctx context.Context, cfg config.AIConfig) (ai.RemoteEngineConfig, error) {
	provider := ai.RemoteProvider(cfg.Provider)
	store := credstore.NewStore()
	credential, err := store.Read(ctx, credentialStoreID(cfg))
	if err != nil {
		return ai.RemoteEngineConfig{}, fmt.Errorf("load %s AI credential: %w", cfg.Provider, err)
	}
	if credential == nil && provider != ai.RemoteCustom {
		return ai.RemoteEngineConfig{}, fmt.Errorf("load %s AI credential: no stored credential; run `ai login %s` or `ai setup`", cfg.Provider, cfg.Provider)
	}
	var credentials ai.CredentialStore
	if provider != ai.RemoteCustom {
		credentials = store
	}
	return ai.RemoteEngineConfig{
		Provider:    provider,
		Protocol:    ai.RemoteAPIProtocol(cfg.Protocol),
		Model:       cfg.Model,
		Credential:  credential,
		Credentials: credentials,
		BaseURL:     cfg.BaseURL,
	}, nil
}

func resolveOneShotAIToolEmbedder(ctx context.Context) (ai.PartEmbedder, func(), string, error) {
	cfg, err := config.LoadAI()
	if err != nil {
		return nil, nil, "", err
	}
	if !cfg.Configured {
		return nil, nil, "AI is not configured; semantic embeddings are unavailable", nil
	}
	switch cfg.Mode {
	case "local":
		if !cfg.ConsentGiven || cfg.LocalEmbeddingConsentVersion < localEmbeddingConsentVersion {
			return nil, nil, "", fmt.Errorf("local embeddings require updated setup consent; run `ai setup`")
		}
		cacheDir, err := assets.DefaultCacheDir()
		if err != nil {
			return nil, nil, "", err
		}
		opts := assets.FetchOptions{
			CacheDir: cacheDir,
			OnProgress: func(stage, details string, percent int) {
				if percent >= 0 {
					fmt.Fprintf(os.Stderr, "\r[embedding %s] %s (%d%%)", stage, details, percent)
				} else {
					fmt.Fprintf(os.Stderr, "\r[embedding %s] %s", stage, details)
				}
			},
		}
		libPath, err := assets.EnsureRuntime(ctx, opts)
		if err != nil {
			return nil, nil, "", fmt.Errorf("prepare local embedding runtime: %w", err)
		}
		modelPath, err := assets.EnsureDefaultEmbeddingModel(ctx, opts)
		if err != nil {
			return nil, nil, "", fmt.Errorf("prepare local embedding model: %w", err)
		}
		backend := "auto"
		if assets.HostOS() == "browser" {
			backend = "gpu"
		}
		embedder, err := ai.NewEmbedder(libPath, modelPath, backend)
		if err != nil {
			return nil, nil, "", fmt.Errorf("initialize required local embedder: %w", err)
		}
		return embedder, embedder.Close, "", nil
	case "remote":
		engineConfig, err := remoteAIEngineConfig(ctx, cfg)
		if err != nil {
			return nil, nil, fmt.Sprintf("remote embeddings unavailable: %v", err), nil
		}
		embedder, diagnostic := ai.ResolveRemoteEmbedder(ctx, engineConfig)
		return embedder, func() {}, diagnostic, nil
	default:
		return nil, nil, "", fmt.Errorf("unsupported AI mode %q; run `ai setup`", cfg.Mode)
	}
}
