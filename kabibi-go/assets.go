package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	defaultModelName = "gemma-4-E2B-it.litertlm"
	defaultWheelName = "litert_lm_api.whl"
	defaultPyPIURL   = "https://pypi.org/pypi/litert-lm-api/json"
	defaultModelURL  = "https://huggingface.co/litert-community/gemma-4-E2B-it-litert-lm/resolve/main/gemma-4-E2B-it.litertlm"
)

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

type assetStageError struct {
	stage string
	err   error
}

func (e assetStageError) Error() string {
	return e.err.Error()
}

func cacheDirPath() (string, error) {
	if v := os.Getenv("LITERTLM_CACHE_DIR"); v != "" {
		return filepath.Clean(v), nil
	}

	switch runtime.GOOS {
	case "windows":
		if v := os.Getenv("LocalAppData"); v != "" {
			return filepath.Join(v, "kabibi-go", "litert_cache"), nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "AppData", "Local", "kabibi-go", "litert_cache"), nil
		}
	default:
		if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
			return filepath.Join(v, "kabibi-go", "litert_cache"), nil
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".cache", "kabibi-go", "litert_cache"), nil
		}
	}

	return "", errors.New("unable to resolve LiteRT-LM cache directory")
}

func (m *model) checkAssetsCmd() tea.Cmd {
	progress := make(chan assetProgressMsg, 32)
	done := make(chan tea.Msg, 4)

	m.assetProgress = progress
	m.assetDone = done

	// liteRT-LM worker
	go func() {
		if err := ensureLiteRT(context.Background(), progress); err != nil {
			done <- assetErrorMsg{Stage: "litertlm", err: err}
			return
		}
		done <- assetReadyMsg{Stage: "litertlm"}
	}()

	// Gemma worker
	go func() {
		if err := ensureGemma(context.Background(), progress); err != nil {
			done <- assetErrorMsg{Stage: "gemma", err: err}
			return
		}
		done <- assetReadyMsg{Stage: "gemma"}
	}()

	return m.watchAssetProgressCmd()
}

func ensureLiteRT(ctx context.Context, progress chan<- assetProgressMsg) error {
	cacheDir, err := cacheDirPath()
	if err != nil {
		return err
	}

	libDir := filepath.Join(cacheDir, "lib")
	if hasValidLiteRTCache(libDir) {
		sendProgress(progress, "litertlm", 100, "using cached runtime")
		return nil
	}

	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	sendProgress(progress, "litertlm", 0, "discovering wheel...")
	wheelURL, wheelFilename, err := selectWheelURL(ctx)
	if err != nil {
		return err
	}

	wheelPath := filepath.Join(cacheDir, wheelFilename)
	if err := downloadFile(ctx, wheelURL, wheelPath, "litertlm", progress); err != nil {
		return err
	}

	if err := extractWheelNativeFiles(wheelPath, libDir, progress); err != nil {
		return err
	}

	if !hasValidLiteRTCache(libDir) {
		return errors.New("downloaded runtime failed validation")
	}

	return nil
}

func ensureGemma(ctx context.Context, progress chan<- assetProgressMsg) error {
	cacheDir, err := cacheDirPath()
	if err != nil {
		return err
	}

	modelPath := filepath.Join(cacheDir, defaultModelName)
	if hasValidGemmaCache(modelPath) {
		sendProgress(progress, "gemma", 100, "using cached weights")
		return nil
	}

	weightURL := modelURLFromEnv()
	if err := downloadFile(ctx, weightURL, modelPath, "gemma", progress); err != nil {
		return err
	}

	if !hasValidGemmaCache(modelPath) {
		return errors.New("downloaded weights failed validation")
	}

	return nil
}

func hasValidCache(libDir, modelPath string) bool {
	return hasValidLiteRTCache(libDir) && hasValidGemmaCache(modelPath)
}

func hasValidLiteRTCache(libDir string) bool {
	if !fileExistsAny(libDir, "litert_lm_ext", nativeLibExts()...) {
		return false
	}
	// We check for at least the extension module.
	// Other libs like libLiteRt might have varying names.
	return true
}

func hasValidGemmaCache(modelPath string) bool {
	return fileInfo(modelPath) != nil
}

func fileExistsAny(dir, base string, exts ...string) bool {
	for _, ext := range exts {
		if fi := fileInfo(filepath.Join(dir, base+ext)); fi != nil {
			return true
		}
	}
	return false
}

func fileInfo(path string) os.FileInfo {
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return fi
}

func modelURLFromEnv() string {
	if v := os.Getenv("LITERTLM_MODEL_URL"); v != "" {
		return v
	}
	return defaultModelURL
}

func selectWheelURL(ctx context.Context) (string, string, error) {
	data, err := fetchPyPI(ctx)
	if err != nil {
		return "", "", err
	}

	candidates := wheelCandidates(data)
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("no compatible liteRT-LM wheel found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	return candidates[0].URL, candidates[0].Filename, nil
}

func fetchPyPI(ctx context.Context) (*pypiResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultPyPIURL, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PyPI metadata request failed: %s", resp.Status)
	}

	var data pypiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func wheelCandidates(data *pypiResponse) []pypiArtifact {
	var matches []pypiArtifact
	for version, artifacts := range data.Releases {
		for _, art := range artifacts {
			art.Version = version
			if wheelMatchesPlatform(art.Filename) {
				matches = append(matches, art)
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Version != matches[j].Version {
			return versionLess(matches[j].Version, matches[i].Version)
		}
		return wheelPreference(matches[i].Filename) < wheelPreference(matches[j].Filename)
	})

	return matches
}

func wheelPreference(filename string) int {
	filename = strings.ToLower(filename)

	if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
		if strings.Contains(filename, "win_arm64") {
			return 0
		}
		if strings.Contains(filename, "win_amd64") {
			return 1
		}
	}

	return 0
}

func wheelMatchesPlatform(filename string) bool {
	filename = strings.ToLower(filename)

	switch runtime.GOOS {
	case "windows":
		switch runtime.GOARCH {
		case "amd64":
			return strings.Contains(filename, "win_amd64")
		case "arm64":
			// Fallback: allow amd64 wheels on Windows arm64 as they usually run via emulation/prism
			return strings.Contains(filename, "win_arm64") || strings.Contains(filename, "win_amd64")
		}
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return strings.Contains(filename, "macosx") && strings.Contains(filename, "arm64")
		}
		if runtime.GOARCH == "amd64" {
			return strings.Contains(filename, "macosx") && strings.Contains(filename, "x86_64")
		}
	case "linux", "freebsd":
		if runtime.GOARCH == "amd64" {
			return strings.Contains(filename, "manylinux") && strings.Contains(filename, "x86_64")
		}
		if runtime.GOARCH == "arm64" {
			return strings.Contains(filename, "manylinux") && strings.Contains(filename, "aarch64")
		}
	}
	return false
}

func versionLess(a, b string) bool {
	aFields := strings.Split(a, ".")
	bFields := strings.Split(b, ".")
	for i := 0; i < len(aFields) || i < len(bFields); i++ {
		if i >= len(aFields) {
			return true
		}
		if i >= len(bFields) {
			return false
		}
		aNum, errA := strconv.Atoi(aFields[i])
		bNum, errB := strconv.Atoi(bFields[i])
		if errA != nil || errB != nil {
			return aFields[i] < bFields[i]
		}
		if aNum != bNum {
			return aNum < bNum
		}
	}
	return false
}

func downloadFile(ctx context.Context, url, path, phase string, progress chan<- assetProgressMsg) error {
	if fi := fileInfo(path); fi != nil && fi.Size() > 0 {
		sendProgress(progress, phase, 100, fmt.Sprintf("cached %s", filepath.Base(path)))
		return nil
	}

	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmpPath)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	// Hugging Face gated models (like Gemma) require an Authorization header.
	// We check for common environment variables used by the HF ecosystem.
	token := os.Getenv("HF_TOKEN")
	if token == "" {
		token = os.Getenv("HUGGING_FACE_HUB_TOKEN")
	}
	if token != "" && strings.Contains(url, "huggingface.co") {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s failed: %s", phase, resp.Status)
	}

	contentLength := resp.ContentLength
	sendProgress(progress, phase, 0, url)

	written := int64(0)
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			w, writeErr := f.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			written += int64(w)
			if contentLength > 0 {
				sendProgress(progress, phase, int(written*100/contentLength), fmt.Sprintf("%s/%s", humanizeMB(written), humanizeMB(contentLength)))
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return readErr
		}
	}

	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}

	return nil
}

func humanizeMB(bytes int64) string {
	val := float64(bytes) / (1024 * 1024)
	if val < 0.1 && bytes > 0 {
		return formatThousands(int(bytes/1024)) + " KB"
	}
	// For Mb, we want thousands separator for the part before the dot
	intPart := int(val)
	fracPart := int((val - float64(intPart)) * 10)
	return fmt.Sprintf("%s.%d Mb", formatThousands(intPart), fracPart)
}

func formatThousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var res []string
	for len(s) > 3 {
		res = append([]string{s[len(s)-3:]}, res...)
		s = s[:len(s)-3]
	}
	if len(s) > 0 {
		res = append([]string{s}, res...)
	}
	return strings.Join(res, ",")
}

func extractWheelNativeFiles(wheelPath, libDir string, progress chan<- assetProgressMsg) error {
	sendProgress(progress, "extracting wheel", 0, filepath.Base(wheelPath))
	zr, err := zip.OpenReader(wheelPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	extracted := 0
	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(file.Name)
		base := filepath.Base(name)
		ext := strings.ToLower(filepath.Ext(base))

		if !isValidNativeExtension(ext) {
			continue
		}

		// Ensure we flatten the structure if it's inside packages (e.g. litert_lm/litert_lm_ext.pyd)
		outName := base

		// Special handling for the main extension module name normalization
		// We want to support both litert_lm_ext and litert-lm
		root := strings.TrimSuffix(base, ext)
		// Strip Python version tags if present (e.g. .cp310-win_amd64)
		if idx := strings.Index(root, ".cp"); idx != -1 {
			root = root[:idx]
		}

		if root == "litert_lm_ext" || root == "litert-lm" {
			outName = "litert_lm_ext" + nativeLibExts()[0]
		} else if strings.HasPrefix(root, "libGemma") {
			outName = "libGemmaModelConstraintProvider" + nativeLibExts()[0]
		}

		if runtime.GOOS == "windows" && ext == ".pyd" && !strings.HasSuffix(outName, ".dll") {
			outName = strings.TrimSuffix(outName, ext) + ".dll"
		}
		if runtime.GOOS == "darwin" && ext == ".so" {
			outName = strings.TrimSuffix(outName, ext) + ".dylib"
		}

		outPath := filepath.Join(libDir, outName)
		if err := extractZipFile(file, outPath); err != nil {
			return err
		}
		extracted++

		// Also keep original name if we renamed it, just in case of internal dependencies
		if outName != base {
			altPath := filepath.Join(libDir, base)
			_ = copyFile(outPath, altPath)
		}
	}

	if extracted == 0 {
		return fmt.Errorf("no native runtime files found in wheel %s", filepath.Base(wheelPath))
	}

	sendProgress(progress, "completed wheel extraction", 100, fmt.Sprintf("extracted %d files", extracted))
	return nil
}

func sendProgress(progress chan<- assetProgressMsg, stage string, percent int, details string) {
	if progress == nil {
		return
	}
	select {
	case progress <- assetProgressMsg{Stage: stage, Percent: percent, Details: details}:
	default:
	}
}

func isValidNativeExtension(ext string) bool {
	ext = strings.ToLower(ext)
	expected := nativeLibExts()
	for _, allowed := range expected {
		if ext == allowed {
			return true
		}
	}
	return false
}

func nativeLibExts() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{".dll", ".pyd"}
	case "darwin":
		return []string{".dylib", ".so"}
	default:
		return []string{".so"}
	}
}

func extractZipFile(file *zip.File, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	in, err := file.Open()
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
