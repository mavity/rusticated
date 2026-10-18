package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"ai"
	"ai/assets"
)

func main() {
	cfg := parseConfig()

	ctx := context.Background()
	dir, err := assets.DefaultCacheDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pope: cache dir error: %v\n", err)
		os.Exit(1)
	}

	opts := assets.FetchOptions{
		CacheDir: dir,
		OnProgress: func(stage, details string, percent int) {
			if cfg.VerboseFlag {
				if percent >= 0 {
					fmt.Printf("[%s] %s (%d%%)\n", stage, details, percent)
				} else {
					fmt.Printf("[%s] %s\n", stage, details)
				}
			}
		},
	}

	libPath, err := assets.EnsureRuntime(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pope: runtime library error: %v\n", err)
		os.Exit(1)
	}

	// Propagate verbose flag to ai package for DLL-load logging
	if cfg.VerboseFlag {
		ai.Verbose = true
		fmt.Fprintf(os.Stderr, "[pope] runtime library path: %s\n", libPath)
	}

	modelPath := cfg.ModelFlag
	if modelPath == "" {
		m, err := assets.EnsureDefaultLLMModel(ctx, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pope: model download error: %v\n", err)
			os.Exit(1)
		}
		modelPath = m
	}

	// 1. Model Introspection
	info, err := ai.LoadModelInfo(libPath, modelPath)

	if cfg.InfoFlag {
		fmt.Printf("Model Path: %s\n", modelPath)
		if err != nil {
			fmt.Printf("Model introspection unavailable: %v\n", err)
			os.Exit(1)
		}
		if info != nil {
			fmt.Printf("Max Context Tokens: %d\n", info.MaxContextTokens)
			fmt.Printf("Dynamic Context: %v\n", info.IsDynamicContext)
			fmt.Printf("Supports Thinking: %v\n", info.SupportsThinking)
			fmt.Printf("Supports Function Calling: %v\n", info.SupportsFunctionCalling)
			fmt.Printf("Supported Backends: %s\n", strings.Join(info.SupportedBackends, ", "))
		}
		return
	}

	if err != nil && cfg.VerboseFlag {
		fmt.Fprintf(os.Stderr, "[info] warning: model introspection unavailable: %v\n", err)
	}

	if info != nil && cfg.VerboseFlag {
		fmt.Printf("[info] Max Context Tokens: %d (dynamic: %v)\n", info.MaxContextTokens, info.IsDynamicContext)
		fmt.Printf("[info] Supports Function Calling: %v\n", info.SupportsFunctionCalling)
		fmt.Printf("[info] Supports Thinking: %v\n", info.SupportsThinking)
		fmt.Printf("[info] Supported Backends: %v\n", info.SupportedBackends)
	}

	// 2. Validate backend
	chosenBackend := cfg.BackendFlag
	if info != nil && len(info.SupportedBackends) > 0 {
		supported := false
		for _, b := range info.SupportedBackends {
			if strings.EqualFold(b, chosenBackend) {
				supported = true
				break
			}
		}
		if !supported && chosenBackend != "auto" {
			if cfg.VerboseFlag {
				fmt.Printf("[info] backend %s not in model supported backends %v; falling back to %s\n",
					chosenBackend, info.SupportedBackends, info.SupportedBackends[0])
			}
			chosenBackend = info.SupportedBackends[0]
		}
	}

	// 3. Initialize Engine
	// We allocate 75% of the model's reported max capacity (or a reasonable 8k default)
	// to avoid OOMs on limited devices while giving enough room for chat history.
	maxTokens := 8192
	if info != nil && info.MaxContextTokens > 0 {
		maxTokens = int(float64(info.MaxContextTokens) * 0.5)
	}

	engine, err := ai.NewEngine(libPath, modelPath, chosenBackend, maxTokens)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pope: failed to create engine: %v\n", err)
		os.Exit(1)
	}
	defer engine.Close()

	// 3b. Initialize Embedder (EmbeddingGemma 2) unless disabled
	var embedder ai.PartEmbedder
	if !strings.EqualFold(cfg.EmbedModelFlag, "none") {
		embedPath := cfg.EmbedModelFlag
		if embedPath == "" {
			if ep, err := assets.EnsureDefaultEmbeddingModel(ctx, opts); err == nil {
				embedPath = ep
			} else if cfg.VerboseFlag {
				fmt.Fprintf(os.Stderr, "[pope] warning: embedding model fetch skipped: %v\n", err)
			}
		}
		if embedPath != "" {
			if emb, err := ai.NewEmbedder(libPath, embedPath, chosenBackend); err == nil {
				embedder = emb
				defer emb.Close()
			} else if cfg.VerboseFlag {
				fmt.Fprintf(os.Stderr, "[pope] warning: embedder init failed: %v\n", err)
			}
		}
	}

	// 4. Setup Tools
	// The Gemma 4 E2B model metadata incorrectly reports SupportsFunctionCalling: false.
	// We force it to true and register tools anyway, as the model is instruction-tuned for it.
	tools := Tools

	// 5. Setup Compaction Threshold & Environmental System Prompt
	// We proactively trigger compaction well before we hit the allocated limit.
	compactionLimit := int(float64(maxTokens) * 0.80)

	cwd, _ := os.Getwd()
	envContext := ai.FormatEnvironmentContext(ai.EnvironmentConfig{
		ModelPath:   modelPath,
		ModelSize:   assets.ModelAssetSize(modelPath),
		ModelInfo:   info,
		Backend:     engine.Backend(),
		MaxTokens:   maxTokens,
		HasEmbedder: embedder != nil,
		WorkingDir:  cwd,
	})
	sysMsg := strings.TrimSpace(cfg.SystemFlag)
	if envContext != "" {
		if sysMsg != "" {
			sysMsg += "\n\n" + envContext
		} else {
			sysMsg = envContext
		}
	}

	session, err := ai.NewSession(ai.SessionConfig{
		Engine:          engine,
		Embedder:        embedder,
		SystemMessage:   sysMsg,
		Tools:           tools,
		CompactionLimit: compactionLimit,
		OnContent:       func(text string) { fmt.Print(text) },
		OnFeedback:      func(text string) { fmt.Print(text) },
		Verbose:         cfg.VerboseFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pope: session config error: %v\n", err)
		os.Exit(1)
	}

	if err := session.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "pope: conversation init failed: %v\n", err)
		os.Exit(1)
	}
	defer session.Shutdown()

	// 6. Execution: One-Shot or REPL
	if cfg.PromptFlag != "" {
		if err := session.RunTurn(ctx, cfg.PromptFlag); err != nil {
			fmt.Fprintf(os.Stderr, "pope: turn error: %v\n", err)
			os.Exit(1)
		}
		session.FlushEmbeddings()
		return
	}

	// Interactive REPL
	runTerminalLoop(ctx, session)
}
