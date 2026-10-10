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
		url, filename, err := selectWheelURL(ctx, litertlmPyPIURL)
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

// EnsureModel downloads a single-file model (e.g. a .litertlm) from url into
// the cache and returns its path. If expectedSize > 0 it is used as a minimum
// size sanity check. A valid cached copy is reused without a download.
func EnsureModel(ctx context.Context, url string, expectedSize int64, opts FetchOptions) (string, error) {
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
