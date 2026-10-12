package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"ai/assets"
)

// Config holds the parsed command-line flags.
type Config struct {
	PromptFlag  string
	ModelFlag   string
	BackendFlag string
	SystemFlag  string
	VerboseFlag bool
	InfoFlag    bool
}

func parseConfig() Config {
	var cfg Config

	flag.StringVar(&cfg.PromptFlag, "p", "", "One-shot prompt execution")
	flag.StringVar(&cfg.PromptFlag, "prompt", "", "One-shot prompt execution")
	flag.StringVar(&cfg.ModelFlag, "m", "", "Path to .litertlm model file (optional)")
	flag.StringVar(&cfg.ModelFlag, "model", "", "Path to .litertlm model file (optional)")
	flag.StringVar(&cfg.BackendFlag, "b", "gpu", "LiteRT-LM backend (gpu, cpu, npu, auto)")
	flag.StringVar(&cfg.BackendFlag, "backend", "gpu", "LiteRT-LM backend (gpu, cpu, npu, auto)")
	flag.StringVar(&cfg.SystemFlag, "s", "You are Pope, a helpful and precise assistant equipped with system tools.", "System message")
	flag.StringVar(&cfg.SystemFlag, "system", "You are Pope, a helpful and precise assistant equipped with system tools.", "System message")
	flag.BoolVar(&cfg.VerboseFlag, "v", false, "Verbose output")
	flag.BoolVar(&cfg.VerboseFlag, "verbose", false, "Verbose output")
	flag.BoolVar(&cfg.InfoFlag, "i", false, "Print model info and exit")
	flag.BoolVar(&cfg.InfoFlag, "info", false, "Print model info and exit")
	flag.Parse()

	return cfg
}

func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("user home directory unavailable: %w", err)
	}
	if assets.HostOS() == "windows" {
		return filepath.Join(home, "AppData", "Local", "kabibi", "litert_cache"), nil
	}
	return filepath.Join(home, ".cache", "kabibi", "litert_cache"), nil
}
