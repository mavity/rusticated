package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	litertlmPyPIURL = "https://pypi.org/pypi/litert-lm-api/json"
	gemmaModelURL   = "https://huggingface.co/litert-community/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it.litertlm"

	// Qualcomm EasyOCR TFLite models (detector + recognizer in a single ZIP from AWS S3, public access, no auth required).
	easyOCRTFLiteZipURL = "https://qaihub-public-assets.s3.us-west-2.amazonaws.com/qai-hub-models/models/easyocr/releases/v0.63.0/easyocr-tflite-float.zip"
)

// tfliteMagic contains both known TFLite FlatBuffer file identifiers.
var tfliteMagics = []string{"TFL3", "TFL1"}

type pypiResponse struct {
	Info struct {
		Version string `json:"version"`
	} `json:"info"`
	Releases map[string][]pypiArtifact `json:"releases"`
}

type pypiArtifact struct {
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Version  string `json:"-"`
}

func cacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("user home directory unavailable: %w", err)
	}
	// Share cache with kabibi so the LM wheel + Gemma are reused.
	if HostOS() == "windows" {
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

// EnsureAssets fetches all 3 assets concurrently (LM wheel, Gemma, EasyOCR models).
// Each goroutine: HEAD → download to .tmp → validate → atomic rename.
// All failures are fatal.
func EnsureAssets(ctx context.Context, cfg *Config, log func(string)) error {
	dir, err := ensureCacheDir()
	if err != nil {
		return err
	}
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return err
	}

	type res struct {
		key  string
		val  string
		val2 string
		err  error
	}
	ch := make(chan res, 3)

	// ── goroutine 1: LiteRT-LM runtime wheel ─────────────────────────────────
	go func() {
		err := ensureLiteRTLM(ctx, dir, libDir, log)
		lib := ""
		if err == nil {
			lib = resolveLMLib(libDir)
			if lib == "" {
				err = fmt.Errorf("library not found after extraction")
			}
		}
		ch <- res{"lm", lib, "", err}
	}()

	// ── goroutine 2: Gemma 4 model ────────────────────────────────────────────
	go func() {
		path, err := ensureGemmaModel(ctx, dir, log)
		ch <- res{"gemma", path, "", err}
	}()

	// ── goroutine 3: EasyOCR detector + recognizer models ─────────────────────
	go func() {
		detector, recognizer, err := ensureEasyOCRModels(ctx, dir, log)
		ch <- res{"ocr", detector, recognizer, err}
	}()

	for range [3]struct{}{} {
		r := <-ch
		switch r.key {
		case "lm":
			if r.err != nil {
				return fmt.Errorf("LiteRT-LM: %w", r.err)
			}
			cfg.LMLibPath = r.val
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

	// Resolve TFLite library (may be embedded in LM wheel or a separate download).
	tflLibDir := filepath.Join(dir, "tfl_lib")
	_ = os.MkdirAll(tflLibDir, 0o755)
	cfg.TFLLibPath = resolveTFLLib(tflLibDir)
	// Fall back to LM lib dir — litert_lm_ext ships TFLite C API symbols too.
	if cfg.TFLLibPath == "" {
		cfg.TFLLibPath = resolveTFLLib(libDir)
	}

	return nil
}

// ── Asset-specific ensure functions ─────────────────────────────────────────

func ensureLiteRTLM(ctx context.Context, cacheDir, libDir string, log func(string)) error {
	wheelPath := findCachedWheel(cacheDir, "litert_lm")
	if wheelPath == "" {
		log("discovering LiteRT-LM wheel from PyPI...")
		url, filename, err := selectWheelURL(ctx, litertlmPyPIURL)
		if err != nil {
			return err
		}
		wheelPath = filepath.Join(cacheDir, filename)
		if err := downloadAndValidate(ctx, url, wheelPath, validateWheel, log); err != nil {
			return err
		}
	}
	if libUpToDate(libDir, wheelPath, "litert_lm_ext") {
		log("LiteRT-LM runtime up to date")
		return nil
	}
	log("extracting LiteRT-LM runtime...")
	return extractWheelNativeFiles(wheelPath, libDir, log)
}

func ensureGemmaModel(ctx context.Context, dir string, log func(string)) (string, error) {
	// Extract filename from the canonical URL.
	filename := filepath.Base(gemmaModelURL)
	path := filepath.Join(dir, filename)
	if fileValidated(path, validateLitertLM) {
		return path, nil
	}
	log("downloading Gemma 4 model...")
	if err := downloadAndValidate(ctx, gemmaModelURL, path, validateLitertLM, log); err != nil {
		return "", err
	}
	return path, nil
}

func ensureEasyOCRModels(ctx context.Context, dir string, log func(string)) (string, string, error) {
	// Check if both models are already cached and validated.
	detectorPath := filepath.Join(dir, "detector.tflite")
	recognizerPath := filepath.Join(dir, "recognizer.tflite")

	if fileValidated(detectorPath, validateTFLite) && fileValidated(recognizerPath, validateTFLite) {
		return detectorPath, recognizerPath, nil
	}

	// Download ZIP and extract both models.
	zipPath := filepath.Join(dir, "easyocr.zip")
	log("downloading Qualcomm EasyOCR TFLite models...")
	if err := downloadAndValidate(ctx, easyOCRTFLiteZipURL, zipPath, validateWheel, log); err != nil {
		return "", "", err
	}

	log("extracting EasyOCR detector and recognizer...")
	if err := extractEasyOCRModels(zipPath, dir); err != nil {
		return "", "", err
	}

	// Verify both models exist and are valid.
	if err := validateTFLite(detectorPath); err != nil {
		return "", "", fmt.Errorf("detector after extraction: %w", err)
	}
	if err := validateTFLite(recognizerPath); err != nil {
		return "", "", fmt.Errorf("recognizer after extraction: %w", err)
	}

	return detectorPath, recognizerPath, nil
}

func ensureTFLiteModel(ctx context.Context, dir, name, url string, log func(string)) (string, error) {
	path := filepath.Join(dir, name)
	if fileValidated(path, validateTFLite) {
		return path, nil
	}
	log(fmt.Sprintf("downloading %s...", name))
	if err := downloadAndValidate(ctx, url, path, validateTFLite, log); err != nil {
		return "", err
	}
	return path, nil
}

// ── Validation functions ─────────────────────────────────────────────────────

func validateWheel(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("wheel is not a valid ZIP: %w", err)
	}
	r.Close()
	return nil
}

// validateTFLite checks that bytes [4:8] of the file contain a known TFLite magic.
func validateTFLite(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf [8]byte
	if _, err := io.ReadFull(f, buf[:]); err != nil {
		return fmt.Errorf("file too short to contain TFLite header: %w", err)
	}
	id := string(buf[4:8])
	for _, m := range tfliteMagics {
		if id == m {
			return nil
		}
	}
	return fmt.Errorf("invalid TFLite magic %q (expected TFL3 or TFL1)", id)
}

// validateLitertLM checks that the .litertlm file is plausibly non-corrupt.
func validateLitertLM(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	const minSize = 10 * 1024 * 1024 // 10 MB
	if fi.Size() < minSize {
		return fmt.Errorf("file too small (%d bytes), likely corrupt", fi.Size())
	}
	return nil
}

// ── Download primitives ──────────────────────────────────────────────────────

// downloadAndValidate downloads url to path using an atomic .tmp→rename pattern.
// If the existing file passes validate it is reused without a download.
// On download: a HEAD request is made first for content-length metadata.
func downloadAndValidate(ctx context.Context, url, path string, validate func(string) error, log func(string)) error {
	// Reuse if already valid.
	if fileValidated(path, validate) {
		return nil
	}
	// Remove corrupt partial file if present.
	_ = os.Remove(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// HEAD request for content-length metadata.
	contentLength := fetchContentLength(ctx, url)

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", filepath.Base(path), resp.Status)
	}

	if contentLength <= 0 {
		contentLength = resp.ContentLength
	}

	written, lastPct := int64(0), -1
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			written += int64(n)
			if log != nil && contentLength > 0 {
				pct := int(written * 100 / contentLength)
				if pct != lastPct {
					log(fmt.Sprintf("%s: %d%%", filepath.Base(path), pct))
					lastPct = pct
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := f.Close(); err != nil {
		return err
	}

	// Validate before making the file visible.
	if err := validate(tmp); err != nil {
		return fmt.Errorf("post-download validation failed for %s: %w", filepath.Base(path), err)
	}
	return os.Rename(tmp, path)
}

func fetchContentLength(ctx context.Context, url string) int64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.ContentLength
}

func fileValidated(path string, validate func(string) error) bool {
	return fileExists(path) && validate(path) == nil
}

// ── Wheel discovery & extraction ─────────────────────────────────────────────

func resolveLMLib(libDir string) string {
	for _, name := range []string{"litert_lm_ext", "litert-lm", "liblitert-lm"} {
		for _, ext := range nativeLibExts() {
			if p := filepath.Join(libDir, name+ext); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func resolveTFLLib(libDir string) string {
	for _, name := range []string{"libtensorflowlite_c", "liblitert_c", "liblitert", "tensorflowlite_c", "litert_c"} {
		for _, ext := range nativeLibExts() {
			if p := filepath.Join(libDir, name+ext); fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func findCachedWheel(cacheDir, namePrefix string) string {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return ""
	}
	var best, bestVer string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".whl") {
			continue
		}
		if namePrefix != "" && !strings.HasPrefix(strings.ToLower(e.Name()), strings.ToLower(namePrefix)) {
			continue
		}
		if !wheelMatchesPlatform(e.Name()) {
			continue
		}
		if ver := wheelVersion(e.Name()); best == "" || versionLess(bestVer, ver) {
			best, bestVer = filepath.Join(cacheDir, e.Name()), ver
		}
	}
	return best
}

func wheelVersion(filename string) string {
	parts := strings.SplitN(filename, "-", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func libUpToDate(libDir, wheelPath, libPrefix string) bool {
	wheelFI, err := os.Stat(wheelPath)
	if err != nil {
		return false
	}
	entries, _ := os.ReadDir(libDir)
	for _, e := range entries {
		if strings.HasPrefix(strings.ToLower(e.Name()), strings.ToLower(libPrefix)) {
			if fi, err := e.Info(); err == nil && !fi.ModTime().Before(wheelFI.ModTime()) {
				return true
			}
		}
	}
	return false
}

func selectWheelURL(ctx context.Context, pypiURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pypiURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("PyPI %s: %s", pypiURL, resp.Status)
	}
	var data pypiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", err
	}
	var matches []pypiArtifact
	for version, arts := range data.Releases {
		for _, art := range arts {
			art.Version = version
			if strings.HasSuffix(art.Filename, ".whl") && wheelMatchesPlatform(art.Filename) {
				matches = append(matches, art)
			}
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no compatible wheel found for %s/%s", HostOS(), HostArch())
	}
	sort.Slice(matches, func(i, j int) bool {
		return versionLess(matches[j].Version, matches[i].Version)
	})
	return matches[0].URL, matches[0].Filename, nil
}

func wheelMatchesPlatform(filename string) bool {
	f := strings.ToLower(filename)
	switch HostOS() {
	case "windows":
		switch HostArch() {
		case "amd64":
			return strings.Contains(f, "win_amd64")
		case "arm64":
			return strings.Contains(f, "win_arm64") || strings.Contains(f, "win_amd64")
		}
	case "darwin":
		if HostArch() == "arm64" {
			return strings.Contains(f, "macosx") && strings.Contains(f, "arm64")
		}
		return strings.Contains(f, "macosx") && strings.Contains(f, "x86_64")
	case "linux":
		if HostArch() == "amd64" {
			return strings.Contains(f, "manylinux") && strings.Contains(f, "x86_64")
		}
		if HostArch() == "arm64" {
			return strings.Contains(f, "manylinux") && strings.Contains(f, "aarch64")
		}
	}
	return false
}

func versionLess(a, b string) bool {
	af, bf := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(af) || i < len(bf); i++ {
		if i >= len(af) {
			return true
		}
		if i >= len(bf) {
			return false
		}
		an, ea := strconv.Atoi(af[i])
		bn, eb := strconv.Atoi(bf[i])
		if ea != nil || eb != nil {
			if af[i] != bf[i] {
				return af[i] < bf[i]
			}
			continue
		}
		if an != bn {
			return an < bn
		}
	}
	return false
}

func extractWheelNativeFiles(wheelPath, libDir string, log func(string)) error {
	data, err := os.ReadFile(wheelPath)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	extracted := 0
	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(filepath.ToSlash(file.Name))
		ext := strings.ToLower(filepath.Ext(base))
		if !isNativeExt(ext) {
			continue
		}
		root := strings.TrimSuffix(base, ext)
		if idx := strings.Index(root, ".cp"); idx != -1 {
			root = root[:idx]
		}
		outName := base
		if root == "litert_lm_ext" || root == "litert-lm" || root == "liblitert-lm" {
			outName = "litert_lm_ext" + nativeLibExts()[0]
		} else if strings.HasPrefix(root, "libGemma") {
			outName = "libGemmaModelConstraintProvider" + nativeLibExts()[0]
		}
		if HostOS() == "windows" && ext == ".pyd" {
			outName = strings.TrimSuffix(outName, ext) + ".dll"
		}
		if HostOS() == "darwin" && ext == ".so" {
			outName = strings.TrimSuffix(outName, ext) + ".dylib"
		}
		outPath := filepath.Join(libDir, outName)
		if err := extractZipEntry(file, outPath); err != nil {
			return err
		}
		if outName != base {
			_ = copyFile(outPath, filepath.Join(libDir, base))
		}
		extracted++
	}
	if extracted == 0 {
		return fmt.Errorf("no native files found in %s", filepath.Base(wheelPath))
	}
	if log != nil {
		log(fmt.Sprintf("extracted %d native files from %s", extracted, filepath.Base(wheelPath)))
	}
	return nil
}

// extractEasyOCRModels extracts detector.tflite and recognizer.tflite from the EasyOCR ZIP.
func extractEasyOCRModels(zipPath, dir string) error {
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	models := map[string]bool{"detector.tflite": false, "recognizer.tflite": false}
	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(filepath.ToSlash(file.Name))
		if _, isModel := models[base]; !isModel {
			continue
		}
		outPath := filepath.Join(dir, base)
		if err := extractZipEntry(file, outPath); err != nil {
			return err
		}
		models[base] = true
	}
	for model, found := range models {
		if !found {
			return fmt.Errorf("model %s not found in EasyOCR ZIP", model)
		}
	}
	return nil
}

func extractZipEntry(file *zip.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := file.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func nativeLibExts() []string {
	switch HostOS() {
	case "windows":
		return []string{".dll", ".pyd"}
	case "darwin":
		return []string{".dylib", ".so"}
	default:
		return []string{".so"}
	}
}

func isNativeExt(ext string) bool {
	for _, e := range nativeLibExts() {
		if ext == e {
			return true
		}
	}
	return ext == ".pyd"
}

func resolveModelFile(dir, name string) string {
	target := strings.ToLower(name)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.ToLower(e.Name()) == target {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return filepath.Join(dir, name)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}
