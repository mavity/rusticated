package assets

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

// tfliteMagics contains both known TFLite FlatBuffer file identifiers.
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

// ── Validation functions ─────────────────────────────────────────────────────

func validateWheel(path string) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("archive is not a valid ZIP: %w", err)
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

// validateSize checks that a file is non-empty and at least expectedSize bytes
// (when expectedSize > 0). It guards against truncated/corrupt downloads.
func validateSize(path string, expectedSize int64) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() <= 0 {
		return fmt.Errorf("file is empty")
	}
	if expectedSize > 0 && fi.Size() < expectedSize {
		return fmt.Errorf("file too small (%d bytes, expected >= %d), likely corrupt", fi.Size(), expectedSize)
	}
	return nil
}

// ── Download primitives ──────────────────────────────────────────────────────

// downloadAndValidate downloads url to path using an atomic .tmp→rename pattern.
// If the existing file passes validate it is reused without a download.
func downloadAndValidate(ctx context.Context, url, path string, validate func(string) error, opts FetchOptions) error {
	if fileValidated(path, validate) {
		return nil
	}
	// Remove corrupt partial file if present.
	_ = os.Remove(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

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

	name := filepath.Base(path)
	written, lastPct := int64(0), -1
	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			written += int64(n)
			if contentLength > 0 {
				pct := int(written * 100 / contentLength)
				if pct != lastPct {
					opts.progress("download", name, pct)
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
		return fmt.Errorf("post-download validation failed for %s: %w", name, err)
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

func extractWheelNativeFiles(wheelPath, libDir string, opts FetchOptions) error {
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
	opts.progress("extract", fmt.Sprintf("%d native files from %s", extracted, filepath.Base(wheelPath)), -1)
	return nil
}

// extractZipModels extracts the requested files (matched by base name) from a
// ZIP archive into dir.
func extractZipModels(zipPath, dir string, requiredFiles []string) error {
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(requiredFiles))
	for _, name := range requiredFiles {
		want[name] = false
	}
	for _, file := range zr.File {
		if file.FileInfo().IsDir() {
			continue
		}
		base := filepath.Base(filepath.ToSlash(file.Name))
		if _, isWanted := want[base]; !isWanted {
			continue
		}
		if err := extractZipEntry(file, filepath.Join(dir, base)); err != nil {
			return err
		}
		want[base] = true
	}
	for name, found := range want {
		if !found {
			return fmt.Errorf("file %s not found in archive", name)
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

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}
