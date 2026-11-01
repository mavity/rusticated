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
	"path"
	"path/filepath"
	"sort"
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

type runtimeCacheMetadata struct {
	PackageVersion string `json:"package_version"`
	WheelFilename  string `json:"wheel_filename"`
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
	for _, root := range []string{libDir, filepath.Join(libDir, "litert_lm")} {
		for _, name := range []string{"litert_lm_ext", "litert-lm", "liblitert-lm"} {
			for _, ext := range nativeLibExts() {
				if p := filepath.Join(root, name+ext); fileExists(p) {
					return p
				}
			}
		}
	}
	return ""
}

func findCachedWheel(cacheDir, namePrefix string) string {
	return findCachedWheelForPlatform(cacheDir, namePrefix, InspectRuntimePlatform())
}

func findCachedWheelForPlatform(cacheDir, namePrefix string, platform RuntimePlatformInfo) string {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return ""
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".whl") {
			continue
		}
		if namePrefix != "" && !strings.HasPrefix(strings.ToLower(e.Name()), strings.ToLower(namePrefix)) {
			continue
		}
		if wheelVersion(e.Name()) != PinnedLiteRTLMVersion || !wheelMatchesRuntimePlatform(e.Name(), platform) {
			continue
		}
		matches = append(matches, e.Name())
	}
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return filepath.Join(cacheDir, matches[0])
}

func wheelVersion(filename string) string {
	parts := strings.SplitN(filename, "-", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func runtimeLibDir(cacheDir, wheelFilename string) string {
	return filepath.Join(cacheDir, "lib", runtimeLibCacheDirName(wheelFilename))
}

func runtimeCacheMetadataMatches(libDir, wheelFilename string, platform RuntimePlatformInfo) bool {
	if wheelVersion(wheelFilename) != PinnedLiteRTLMVersion || !wheelMatchesRuntimePlatform(wheelFilename, platform) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(libDir, "runtime.json"))
	if err != nil {
		return false
	}
	var metadata runtimeCacheMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return false
	}
	return metadata.PackageVersion == PinnedLiteRTLMVersion && metadata.WheelFilename == filepath.Base(wheelFilename)
}

func writeRuntimeCacheMetadata(libDir, wheelFilename string) error {
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(runtimeCacheMetadata{
		PackageVersion: PinnedLiteRTLMVersion,
		WheelFilename:  filepath.Base(wheelFilename),
	})
	if err != nil {
		return err
	}
	tmpPath := filepath.Join(libDir, "runtime.json.tmp")
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(libDir, "runtime.json")); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func findCachedRuntime(cacheDir string, platform RuntimePlatformInfo) (string, bool) {
	libRoot := filepath.Join(cacheDir, "lib")
	entries, err := os.ReadDir(libRoot)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		libDir := filepath.Join(libRoot, entry.Name())
		data, err := os.ReadFile(filepath.Join(libDir, "runtime.json"))
		if err != nil {
			continue
		}
		var metadata runtimeCacheMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			continue
		}
		if entry.Name() != strings.TrimSuffix(metadata.WheelFilename, ".whl") ||
			!runtimeCacheMetadataMatches(libDir, metadata.WheelFilename, platform) {
			continue
		}
		if lib := resolveLMLib(libDir); lib != "" {
			return lib, true
		}
	}
	return "", false
}

func selectWheelURL(ctx context.Context, pypiURL string) (string, string, int64, error) {
	return selectWheelURLWithClient(ctx, pypiURL, &http.Client{Timeout: 60 * time.Second})
}

func selectWheelURLWithClient(ctx context.Context, pypiURL string, client *http.Client) (string, string, int64, error) {
	return selectWheelURLForPlatformWithClient(ctx, pypiURL, client, InspectRuntimePlatform())
}

func selectWheelURLForPlatform(ctx context.Context, pypiURL string, platform RuntimePlatformInfo) (string, string, int64, error) {
	return selectWheelURLForPlatformWithClient(ctx, pypiURL, &http.Client{Timeout: 60 * time.Second}, platform)
}

func selectWheelURLForPlatformWithClient(ctx context.Context, pypiURL string, client *http.Client, platform RuntimePlatformInfo) (string, string, int64, error) {
	if platform.OS == "android" {
		return "", "", 0, fmt.Errorf("LiteRT-LM runtime loading is unsupported on Android in this migration")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pypiURL, nil)
	if err != nil {
		return "", "", 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", 0, fmt.Errorf("PyPI %s: %s", pypiURL, resp.Status)
	}
	var data pypiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", 0, err
	}
	var matches []pypiArtifact
	for _, art := range data.Releases[PinnedLiteRTLMVersion] {
		art.Version = PinnedLiteRTLMVersion
		if strings.HasSuffix(art.Filename, ".whl") &&
			wheelVersion(art.Filename) == PinnedLiteRTLMVersion &&
			wheelMatchesRuntimePlatform(art.Filename, platform) {
			matches = append(matches, art)
		}
	}
	if len(matches) == 0 {
		return "", "", 0, fmt.Errorf("no compatible LiteRT-LM %s wheel for %s/%s in %s mode", PinnedLiteRTLMVersion, platform.OS, platform.Arch, platform.ExecutionMode)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Filename < matches[j].Filename })
	return matches[0].URL, matches[0].Filename, matches[0].Size, nil
}

func wheelMatchesPlatform(filename string) bool {
	return wheelMatchesRuntimePlatform(filename, InspectRuntimePlatform())
}

func wheelMatchesRuntimePlatform(filename string, platform RuntimePlatformInfo) bool {
	f := strings.ToLower(filename)
	switch platform.OS {
	case "windows":
		switch platform.Arch {
		case "amd64":
			return strings.HasSuffix(f, "-win_amd64.whl")
		case "arm64":
			return platform.ExecutionMode == "mohabbat" && strings.HasSuffix(f, "-win_amd64.whl")
		}
	case "darwin":
		return platform.Arch == "arm64" && strings.HasSuffix(f, "-macosx_12_0_arm64.whl")
	case "linux":
		switch platform.Arch {
		case "amd64":
			return strings.HasSuffix(f, "-manylinux_2_27_x86_64.whl")
		case "arm64":
			return strings.HasSuffix(f, "-manylinux_2_27_aarch64.whl")
		}
	}
	return false
}

func runtimeLibCacheDirName(wheelFilename string) string {
	return strings.TrimSuffix(filepath.Base(wheelFilename), ".whl")
}

func isSafeWheelEntry(name string) bool {
	if strings.Contains(name, "\\") || path.IsAbs(name) {
		return false
	}
	clean := path.Clean(name)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && clean == name
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
		if !isSafeWheelEntry(file.Name) {
			return fmt.Errorf("unsafe path in LiteRT-LM wheel: %q", file.Name)
		}
		ext := strings.ToLower(filepath.Ext(file.Name))
		if !isNativeExt(ext) {
			continue
		}
		outPath := filepath.Join(libDir, filepath.FromSlash(file.Name))
		if err := extractZipEntry(file, outPath); err != nil {
			return err
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
