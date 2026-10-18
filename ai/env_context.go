package ai

import (
	"fmt"
	"path/filepath"
	"strings"

	"ai/assets"
)

// EnvironmentConfig holds runtime, hardware, and model facts to populate a
// succinct environmental context block in the AI system prompt.
type EnvironmentConfig struct {
	ModelPath   string
	ModelSize   int64
	ModelInfo   *ModelInfo
	Backend     string
	MaxTokens   int
	HasEmbedder bool
	WorkingDir  string
	ExtraLines  []string
}

// FormatEnvironmentContext produces a compact, high-signal environmental summary
// describing the OS, platform (native vs. Mohabbat and specific host), browser vs.
// desktop form factor, loaded LLM model and size, active AI backend, and capabilities.
func FormatEnvironmentContext(cfg EnvironmentConfig) string {
	plat := assets.InspectRuntimePlatform()

	var platDesc string
	if plat.ExecutionMode == "mohabbat" {
		platDesc = fmt.Sprintf("Mohabbat WASM via %s (%s/%s, %s)",
			plat.HostRuntime, plat.OS, plat.Arch, plat.EnvironmentKind)
		if plat.BuildPlatform != "" && plat.BuildPlatform != plat.OS+"-"+plat.Arch {
			platDesc += fmt.Sprintf(" [host build: %s]", plat.BuildPlatform)
		}
	} else {
		platDesc = fmt.Sprintf("native (%s/%s, %s)",
			plat.OS, plat.Arch, plat.EnvironmentKind)
	}

	modelName := strings.TrimSpace(cfg.ModelPath)
	if modelName != "" {
		slashPath := filepath.ToSlash(modelName)
		if idx := strings.LastIndex(slashPath, "/"); idx >= 0 && idx+1 < len(slashPath) {
			modelName = slashPath[idx+1:]
		} else {
			modelName = filepath.Base(modelName)
		}
	}
	if modelName == "" {
		modelName = "unknown"
	}

	sizeBytes := cfg.ModelSize
	if sizeBytes <= 0 && cfg.ModelPath != "" {
		sizeBytes = assets.ModelAssetSize(cfg.ModelPath)
	}

	var modelDetails []string
	if sizeBytes > 0 {
		modelDetails = append(modelDetails, formatHumanByteSize(sizeBytes))
	}

	backend := strings.TrimSpace(cfg.Backend)
	if backend == "" {
		backend = "auto"
	}
	if plat.EnvironmentKind == "browser" && strings.EqualFold(backend, "gpu") {
		backend = "gpu (WebGPU GPU_ARTISAN)"
	}
	modelDetails = append(modelDetails, "backend: "+backend)

	if cfg.MaxTokens > 0 {
		if cfg.ModelInfo != nil && cfg.ModelInfo.MaxContextTokens > 0 {
			dyn := ""
			if cfg.ModelInfo.IsDynamicContext {
				dyn = ", dynamic"
			}
			modelDetails = append(modelDetails, fmt.Sprintf("context: %d/%d tokens%s",
				cfg.MaxTokens, cfg.ModelInfo.MaxContextTokens, dyn))
		} else {
			modelDetails = append(modelDetails, fmt.Sprintf("context: %d tokens", cfg.MaxTokens))
		}
	}

	if cfg.ModelInfo != nil {
		modelDetails = append(modelDetails,
			fmt.Sprintf("thinking: %v", cfg.ModelInfo.SupportsThinking),
			fmt.Sprintf("tools: %v", cfg.ModelInfo.SupportsFunctionCalling),
		)
	}

	if cfg.HasEmbedder {
		modelDetails = append(modelDetails, "rag_embedder: EmbeddingGemma-2")
	}

	var lines []string
	lines = append(lines, "Environment:")
	lines = append(lines, "- Platform: "+platDesc)
	lines = append(lines, fmt.Sprintf("- LLM Model: %s (%s)", modelName, strings.Join(modelDetails, ", ")))
	if wd := strings.TrimSpace(cfg.WorkingDir); wd != "" {
		lines = append(lines, "- Working Directory: "+wd)
	}
	for _, extra := range cfg.ExtraLines {
		extra = strings.TrimSpace(extra)
		if extra != "" {
			if !strings.HasPrefix(extra, "- ") {
				extra = "- " + extra
			}
			lines = append(lines, extra)
		}
	}
	return strings.Join(lines, "\n")
}

func formatHumanByteSize(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB / %d bytes", float64(b)/float64(gb), b)
	case b >= mb:
		return fmt.Sprintf("%.1f MB / %d bytes", float64(b)/float64(mb), b)
	case b >= kb:
		return fmt.Sprintf("%.1f KB / %d bytes", float64(b)/float64(kb), b)
	default:
		return fmt.Sprintf("%d bytes", b)
	}
}
