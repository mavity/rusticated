// Package assets manages networking, filesystem caching, PyPI discovery, and
// extraction for the LiteRT-LM runtime wheel and model files. It is entirely
// UI-agnostic: progress is reported through a simple closure.
package assets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// litertlmPyPIURL is the PyPI JSON endpoint used to discover a compatible
// LiteRT-LM runtime wheel for the current host platform.
const litertlmPyPIURL = "https://pypi.org/pypi/litert-lm-api/json"

// Default model URLs and minimum expected byte sizes.
const (
	DefaultLLMModelURL        = "https://huggingface.co/litert-community/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it.litertlm"
	DefaultLLMModelSize       = 2588147712
	DefaultWebLLMModelURL     = "https://huggingface.co/litert-community/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it-web.litertlm"
	DefaultWebLLMModelSize    = 2008432640
	DefaultEmbeddingModelURL  = "https://huggingface.co/litert-community/embeddinggemma-2-text-270m-litert-lm/resolve/main/embeddinggemma-2-text-270m.litertlm"
	DefaultEmbeddingModelSize = 164626432
)

// RuntimePlatformInfo describes the execution environment and host runtime.
type RuntimePlatformInfo struct {
	OS              string // e.g. "windows", "linux", "darwin", "browser"
	Arch            string // e.g. "amd64", "arm64", "wasm"
	ExecutionMode   string // "native" or "mohabbat"
	HostRuntime     string // "native", "washmhost (wazero)", "washmhost-node (node)", "washmhost (browser)"
	EnvironmentKind string // "desktop" or "browser"
	WasmPlatform    string // e.g. "wazero", "node", "browser" when under mohabbat
	BuildPlatform   string // e.g. "windows-amd64" when reported by mohabbat host
}

// ModelAssetSize returns the size in bytes of the model at modelPath, or 0 if unknown.
func ModelAssetSize(modelPath string) int64 {
	if modelPath == "" {
		return 0
	}
	if HostOS() == "browser" {
		if sz := browserModelSize(modelPath); sz > 0 {
			return sz
		}
		for _, tier := range gemma4Tiers {
			if modelPath == tier.WebURL {
				return tier.WebSize
			}
		}
		return 0
	}
	if fi, err := os.Stat(modelPath); err == nil && fi.Size() > 0 {
		return fi.Size()
	}
	if strings.HasSuffix(filepath.ToSlash(modelPath), "/gemma-4-E2B-it.litertlm") {
		return DefaultLLMModelSize
	}
	return 0
}

// DefaultCacheDir returns the canonical host-OS cache directory for LiteRT-LM
// runtime libraries and model files.
func DefaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("user home directory unavailable: %w", err)
	}
	if HostOS() == "windows" {
		return filepath.Join(home, "AppData", "Local", "kabibi", "litert_cache"), nil
	}
	return filepath.Join(home, ".cache", "kabibi", "litert_cache"), nil
}

// EnsureDefaultLLMModel ensures the default Gemma 4 E2B model is cached and returns its path.
func EnsureDefaultLLMModel(ctx context.Context, opts FetchOptions) (string, error) {
	return EnsureModelTier(ctx, "gemma-4-e2b", opts)
}

// EnsureDefaultEmbeddingModel ensures the default EmbeddingGemma 2 model is cached and returns its path.
func EnsureDefaultEmbeddingModel(ctx context.Context, opts FetchOptions) (string, error) {
	if HostOS() == "browser" {
		return "", nil
	}
	return EnsureModel(ctx, DefaultEmbeddingModelURL, DefaultEmbeddingModelSize, opts)
}

// FetchOptions configures asset fetching. CacheDir must already exist (or be
// creatable). OnProgress, if set, is invoked with a coarse stage label, a
// human-readable detail, and a percent in [0,100] (or -1 when unknown).
type FetchOptions struct {
	CacheDir   string
	OnProgress func(stage, details string, percent int)
}

func (o FetchOptions) progress(stage, details string, percent int) {
	if o.OnProgress != nil {
		o.OnProgress(stage, details, percent)
	}
}

// EnsureRuntime makes the LiteRT-LM native runtime library available and
// returns its path. It is offline-first: a locally cached wheel (or an
// already-extracted library) is used before any network request to PyPI. The
// returned library exposes both the LiteRT-LM Engine symbols and the TFLite C
// API used by Interpreter.
func EnsureRuntime(ctx context.Context, opts FetchOptions) (string, error) {
	if HostOS() == "browser" {
		opts.progress("runtime", "LiteRT-LM WebGPU eval bridge ready", 100)
		return "eval", nil
	}
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		return "", err
	}
	libDir := filepath.Join(opts.CacheDir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return "", err
	}

	wheelPath := findCachedWheel(opts.CacheDir, "litert_lm")
	if wheelPath == "" {
		// Offline-first: reuse an already-extracted library if present.
		if lib := resolveLMLib(libDir); lib != "" {
			opts.progress("runtime", "using cached LiteRT-LM runtime", -1)
			return lib, nil
		}
		opts.progress("discover", "LiteRT-LM wheel from PyPI", -1)
		url, filename, _, err := selectWheelURL(ctx, litertlmPyPIURL)
		if err != nil {
			return "", err
		}
		wheelPath = filepath.Join(opts.CacheDir, filename)
		if err := downloadAndValidate(ctx, url, wheelPath, validateWheel, opts); err != nil {
			return "", err
		}
	}

	if !libUpToDate(libDir, wheelPath, "litert_lm_ext") {
		opts.progress("extract", "LiteRT-LM runtime", -1)
		if err := extractWheelNativeFiles(wheelPath, libDir, opts); err != nil {
			return "", err
		}
	}

	lib := resolveLMLib(libDir)
	if lib == "" {
		return "", fmt.Errorf("LiteRT-LM library not found after extraction")
	}
	return lib, nil
}

// RuntimeDownloadSize estimates the exact runtime-wheel transfer before consent.
// It reads PyPI metadata only when a compatible wheel is not already cached.
func RuntimeDownloadSize(ctx context.Context, cacheDir string) (int64, error) {
	if HostOS() == "browser" {
		return 0, nil
	}
	if findCachedWheel(cacheDir, "litert_lm") != "" {
		return 0, nil
	}
	libDir := filepath.Join(cacheDir, "lib")
	if resolveLMLib(libDir) != "" {
		return 0, nil
	}
	_, _, size, err := selectWheelURL(ctx, litertlmPyPIURL)
	if err != nil {
		return 0, fmt.Errorf("inspect LiteRT-LM runtime download: %w", err)
	}
	if size <= 0 {
		return 0, fmt.Errorf("PyPI did not provide a valid LiteRT-LM wheel size")
	}
	return size, nil
}

// EnsureModel downloads a single-file model (e.g. a .litertlm) from url into
// the cache and returns its path. If expectedSize > 0 it is used as a minimum
// size sanity check. A valid cached copy is reused without a download.
func EnsureModel(ctx context.Context, url string, expectedSize int64, opts FetchOptions) (string, error) {
	if HostOS() == "browser" {
		return ensureModelBrowser(ctx, url, expectedSize, opts)
	}
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		return "", err
	}
	filename := filepath.Base(url)
	path := filepath.Join(opts.CacheDir, filename)
	validate := func(p string) error { return validateSize(p, expectedSize) }
	if fileValidated(path, validate) {
		return path, nil
	}
	opts.progress("download", filename, -1)
	if err := downloadAndValidate(ctx, url, path, validate, opts); err != nil {
		return "", err
	}
	return path, nil
}

// EnsureZipModels downloads a ZIP archive from url, extracts the requested
// files (matched by base name) into the cache, and returns a map from each
// requested name to its absolute path. Already-cached valid files are reused.
func EnsureZipModels(ctx context.Context, url string, requiredFiles []string, opts FetchOptions) (map[string]string, error) {
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(requiredFiles))
	allValid := true
	for _, name := range requiredFiles {
		p := filepath.Join(opts.CacheDir, name)
		result[name] = p
		if !fileValidated(p, modelValidator(name)) {
			allValid = false
		}
	}
	if allValid {
		return result, nil
	}

	zipPath := filepath.Join(opts.CacheDir, "models-archive.zip")
	opts.progress("download", filepath.Base(url), -1)
	if err := downloadAndValidate(ctx, url, zipPath, validateWheel, opts); err != nil {
		return nil, err
	}
	opts.progress("extract", "models", -1)
	if err := extractZipModels(zipPath, opts.CacheDir, requiredFiles); err != nil {
		return nil, err
	}
	for _, name := range requiredFiles {
		p := filepath.Join(opts.CacheDir, name)
		if err := modelValidator(name)(p); err != nil {
			return nil, fmt.Errorf("%s after extraction: %w", name, err)
		}
		result[name] = p
	}
	return result, nil
}

// modelValidator picks a validator for an extracted model file by extension.
func modelValidator(name string) func(string) error {
	if strings.HasSuffix(strings.ToLower(name), ".tflite") {
		return validateTFLite
	}
	return func(p string) error {
		if !fileExists(p) {
			return fmt.Errorf("file missing or empty")
		}
		return nil
	}
}
