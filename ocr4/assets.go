package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"ai/assets"
)

const (
	gemmaModelURL = "https://huggingface.co/litert-community/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it.litertlm"

	// Qualcomm EasyOCR TFLite models (detector + recognizer in a single ZIP from
	// AWS S3, public access, no auth required).
	easyOCRTFLiteZipURL = "https://qaihub-public-assets.s3.us-west-2.amazonaws.com/qai-hub-models/models/easyocr/releases/v0.63.0/easyocr-tflite-float.zip"

	// gemmaModelSize is the expected size of the Gemma 4 .litertlm, used as a
	// download sanity check.
	gemmaModelSize = 2038755678
)

func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("user home directory unavailable: %w", err)
	}
	// Share cache with kabibi so the LM wheel + Gemma are reused.
	if assets.HostOS() == "windows" {
		return filepath.Join(home, "AppData", "Local", "kabibi", "litert_cache"), nil
	}
	return filepath.Join(home, ".cache", "kabibi", "litert_cache"), nil
}

func ensureCacheDir() (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	return dir, os.MkdirAll(dir, 0o755)
}

// EnsureAssets fetches all 3 assets concurrently (LM runtime, Gemma, EasyOCR
// models) via the ai/assets package and maps the results onto cfg. All
// failures are fatal.
func EnsureAssets(ctx context.Context, cfg *Config, log func(string)) error {
	dir, err := ensureCacheDir()
	if err != nil {
		return err
	}

	opts := assets.FetchOptions{
		CacheDir: dir,
		OnProgress: func(stage, details string, percent int) {
			if percent >= 0 {
				log(fmt.Sprintf("[%s] %s (%d%%)", stage, details, percent))
			} else {
				log(fmt.Sprintf("[%s] %s", stage, details))
			}
		},
	}

	type res struct {
		key  string
		val  string
		val2 string
		err  error
	}
	ch := make(chan res, 3)

	go func() {
		lib, err := assets.EnsureRuntime(ctx, opts)
		ch <- res{"lm", lib, "", err}
	}()

	go func() {
		path, err := assets.EnsureModel(ctx, gemmaModelURL, gemmaModelSize, opts)
		ch <- res{"gemma", path, "", err}
	}()

	go func() {
		req := []string{"detector.tflite", "recognizer.tflite"}
		m, err := assets.EnsureZipModels(ctx, easyOCRTFLiteZipURL, req, opts)
		if err != nil {
			ch <- res{"ocr", "", "", err}
			return
		}
		ch <- res{"ocr", m["detector.tflite"], m["recognizer.tflite"], nil}
	}()

	for range [3]struct{}{} {
		r := <-ch
		switch r.key {
		case "lm":
			if r.err != nil {
				return fmt.Errorf("LiteRT-LM: %w", r.err)
			}
			// The LiteRT-LM runtime library also exposes the TFLite C API.
			cfg.LMLibPath = r.val
			cfg.TFLLibPath = r.val
		case "gemma":
			if r.err != nil {
				return fmt.Errorf("Gemma 4: %w", r.err)
			}
			cfg.GemmaModelPath = r.val
		case "ocr":
			if r.err != nil {
				return fmt.Errorf("EasyOCR models: %w", r.err)
			}
			cfg.DBModelPath = r.val   // detector -> DBModelPath
			cfg.RecModelPath = r.val2 // recognizer -> RecModelPath
		}
	}

	return nil
}
