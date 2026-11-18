package assets

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type testRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return roundTripper(req)
}

func TestRuntimeWheelSourcesWithPyPIOffline(t *testing.T) {
	platform := RuntimePlatformInfo{OS: "windows", Arch: "arm64", ExecutionMode: "mohabbat"}
	const filename = "litert_lm_api-0.19.0-py3-none-win_amd64.whl"
	root := "https://assets.kabi.bi/" + filename
	prefixed := "https://assets.kabi.bi/gemma4/" + filename
	var requests []string
	client := &http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method+" "+req.URL.String())
		switch req.URL.String() {
		case litertlmPyPIURL:
			return nil, errors.New("PyPI is unreachable")
		case root:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", ContentLength: 123, Body: http.NoBody}, nil
		case prefixed:
			return &http.Response{StatusCode: http.StatusOK, ContentLength: 456, Body: http.NoBody}, nil
		default:
			return nil, errors.New("unexpected URL")
		}
	})}
	urls, name, size, err := runtimeWheelSources(context.Background(), client, platform)
	if err != nil || name != filename || size != 456 || !slices.Equal(urls, []string{prefixed}) {
		t.Fatalf("wheel discovery = (%v, %q, %d, %v), want prefixed mirror and its exact size", urls, name, size, err)
	}
	if want := []string{"GET " + litertlmPyPIURL, "HEAD " + root, "HEAD " + prefixed}; !slices.Equal(requests, want) {
		t.Fatalf("requests = %v, want %v (no wheel downloads before consent)", requests, want)
	}
}

func TestRuntimeWheelSourcesPrefersPyPI(t *testing.T) {
	platform := RuntimePlatformInfo{OS: "linux", Arch: "arm64"}
	const filename = "litert_lm_api-0.19.0-py3-none-manylinux_2_27_aarch64.whl"
	const primary = "https://files.pythonhosted.org/" + filename
	client := &http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != litertlmPyPIURL {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL)
		}
		payload := `{"releases":{"0.19.0":[{"filename":"` + filename + `","url":"` + primary + `","size":123}]}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})}
	urls, name, size, err := runtimeWheelSources(context.Background(), client, platform)
	want := []string{primary, "https://assets.kabi.bi/" + filename, "https://assets.kabi.bi/gemma4/" + filename}
	if err != nil || name != filename || size != 123 || !slices.Equal(urls, want) {
		t.Fatalf("wheel discovery = (%v, %q, %d, %v), want (%v, %q, 123, nil)", urls, name, size, err, want, filename)
	}
}

func TestDownloadAndValidateFromSourcesFallsBackInOrder(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/primary":
			http.NotFound(w, r)
		case "/fallback":
			_, _ = w.Write([]byte("model-data"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "model.litertlm")
	err := downloadAndValidateFromSources(
		context.Background(),
		[]string{server.URL + "/primary", server.URL + "/fallback"},
		dest,
		func(path string) error { return validateSize(path, 1) },
		FetchOptions{},
	)
	if err != nil {
		t.Fatalf("downloadAndValidateFromSources() error = %v", err)
	}
	if got, want := strings.Join(requests, ","), "HEAD /primary,GET /primary,HEAD /fallback,GET /fallback"; got != want {
		t.Fatalf("requests = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "model-data" {
		t.Fatalf("downloaded file = %q, %v; want model-data", data, err)
	}
}

func TestDownloadAndValidateFromSourcesReportsAllFailures(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	err := downloadAndValidateFromSources(
		context.Background(),
		[]string{server.URL + "/first", server.URL + "/second"},
		filepath.Join(t.TempDir(), "model.litertlm"),
		func(path string) error { return validateSize(path, 1) },
		FetchOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "/first") || !strings.Contains(err.Error(), "/second") {
		t.Fatalf("downloadAndValidateFromSources() error = %v, want both source failures", err)
	}
}

func TestSelectWheelURLUsesHostCompatibleWheel(t *testing.T) {
	hostOS, hostArch := testRuntimeHost()
	want := testRuntimeWheelFilename()
	if want == "" {
		t.Skipf("no LiteRT-LM wheel is defined for %s/%s", hostOS, hostArch)
	}

	payload, err := json.Marshal(pypiResponse{
		Releases: map[string][]pypiArtifact{
			"0.18.0": {
				{Filename: strings.Replace(want, PinnedLiteRTLMVersion, "0.18.0", 1), URL: "https://example.test/old.whl", Size: 456},
			},
			PinnedLiteRTLMVersion: {
				{Filename: want, URL: "https://example.test/" + want, Size: 123},
				{Filename: "litert_lm_api-0.19.0-py3-none-any.whl", URL: "https://example.test/incompatible.whl", Size: 456},
			},
			"0.20.0": {
				{Filename: strings.Replace(want, PinnedLiteRTLMVersion, "0.20.0", 1), URL: "https://example.test/new.whl", Size: 789},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode PyPI fixture: %v", err)
	}
	const pypiURL = "https://pypi.example.test/project/json"
	client := &http.Client{
		Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet || req.URL.String() != pypiURL {
				t.Errorf("request = %s %s, want GET %s", req.Method, req.URL, pypiURL)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Request:    req,
			}, nil
		}),
	}

	url, filename, size, err := selectWheelURLForPlatformWithClient(context.Background(), pypiURL, client, testRuntimePlatform())
	if err != nil {
		t.Fatalf("selectWheelURL() error = %v", err)
	}
	if filename != want {
		t.Fatalf("selected wheel = %q, want %q for %s/%s", filename, want, hostOS, hostArch)
	}
	if url != "https://example.test/"+want || size != 123 {
		t.Fatalf("selected artifact = (%q, %d), want (%q, 123)", url, size, "https://example.test/"+want)
	}
}

func TestWheelMatchesRuntimePlatform(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		platform RuntimePlatformInfo
		want     bool
	}{
		{
			name:     "native Windows ARM64 rejects x64 wheel",
			filename: "litert_lm_api-0.19.0-py3-none-win_amd64.whl",
			platform: RuntimePlatformInfo{OS: "windows", Arch: "arm64", ExecutionMode: "native"},
		},
		{
			name:     "Rusticated Windows ARM64 can use x64 worker",
			filename: "litert_lm_api-0.19.0-py3-none-win_amd64.whl",
			platform: RuntimePlatformInfo{OS: "windows", Arch: "arm64", ExecutionMode: "mohabbat"},
			want:     true,
		},
		{
			name:     "Linux x64 requires glibc 2.27 tag",
			filename: "litert_lm_api-0.19.0-py3-none-manylinux_2_27_x86_64.whl",
			platform: RuntimePlatformInfo{OS: "linux", Arch: "amd64", ExecutionMode: "native"},
			want:     true,
		},
		{
			name:     "Linux rejects other manylinux baseline",
			filename: "litert_lm_api-0.19.0-py3-none-manylinux_2_17_x86_64.whl",
			platform: RuntimePlatformInfo{OS: "linux", Arch: "amd64", ExecutionMode: "native"},
		},
		{
			name:     "Linux rejects musllinux",
			filename: "litert_lm_api-0.19.0-py3-none-musllinux_1_2_x86_64.whl",
			platform: RuntimePlatformInfo{OS: "linux", Arch: "amd64", ExecutionMode: "native"},
		},
		{
			name:     "Android is unsupported",
			filename: "litert_lm_api-0.19.0-py3-none-android_23_arm64_v8a.whl",
			platform: RuntimePlatformInfo{OS: "android", Arch: "arm64", ExecutionMode: "native"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wheelMatchesRuntimePlatform(tt.filename, tt.platform); got != tt.want {
				t.Fatalf("wheelMatchesRuntimePlatform(%q, %+v) = %v, want %v", tt.filename, tt.platform, got, tt.want)
			}
		})
	}
}

func TestSelectWheelURLRejectsAndroidExplicitly(t *testing.T) {
	_, _, _, err := selectWheelURLForPlatformWithClient(
		context.Background(),
		"",
		nil,
		RuntimePlatformInfo{OS: "android", Arch: "arm64", ExecutionMode: "native"},
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported on Android") {
		t.Fatalf("selectWheelURL() error = %v, want explicit Android unsupported error", err)
	}
}

func TestFindCachedWheelRequiresPinnedVersionAndPlatform(t *testing.T) {
	cacheDir := t.TempDir()
	for _, filename := range []string{
		"litert_lm_api-0.18.0-py3-none-win_amd64.whl",
		"litert_lm_api-0.19.0-py3-none-win_amd64.whl",
		"litert_lm_api-0.20.0-py3-none-win_amd64.whl",
	} {
		if err := os.WriteFile(filepath.Join(cacheDir, filename), []byte("wheel"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	platform := RuntimePlatformInfo{OS: "windows", Arch: "amd64", ExecutionMode: "native"}
	got := findCachedWheelForPlatform(cacheDir, "litert_lm", platform)
	want := filepath.Join(cacheDir, "litert_lm_api-0.19.0-py3-none-win_amd64.whl")
	if got != want {
		t.Fatalf("findCachedWheelForPlatform() = %q, want %q", got, want)
	}
}

func TestRuntimeDownloadSizeUsesCompatibleCachedWheel(t *testing.T) {
	filename := testRuntimeWheelFilename()
	if filename == "" {
		t.Skip("host has no compatible LiteRT-LM wheel")
	}
	cacheDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDir, filename), []byte("cached wheel"), 0o644); err != nil {
		t.Fatal(err)
	}
	size, err := RuntimeDownloadSize(context.Background(), cacheDir)
	if err != nil {
		t.Fatalf("RuntimeDownloadSize() error = %v", err)
	}
	if size != 0 {
		t.Fatalf("RuntimeDownloadSize() = %d, want 0 for compatible cached wheel", size)
	}
}

func TestRuntimeCacheRequiresMatchingWheelMetadata(t *testing.T) {
	cacheDir := t.TempDir()
	filename := "litert_lm_api-0.19.0-py3-none-win_amd64.whl"
	libDir := runtimeLibDir(cacheDir, filename)
	if err := os.MkdirAll(filepath.Join(libDir, "litert_lm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "litert_lm", "litert-lm.dll"), []byte("library"), 0o644); err != nil {
		t.Fatal(err)
	}
	platform := RuntimePlatformInfo{OS: "windows", Arch: "amd64", ExecutionMode: "native"}

	if _, ok := findCachedRuntime(cacheDir, platform); ok {
		t.Fatal("unidentified extracted runtime was reused")
	}
	if err := writeRuntimeCacheMetadata(libDir, filename); err != nil {
		t.Fatal(err)
	}
	if lib, ok := findCachedRuntime(cacheDir, platform); !ok || !strings.HasSuffix(lib, filepath.Join("litert_lm", "litert-lm.dll")) {
		t.Fatalf("findCachedRuntime() = %q, %v; want identified runtime", lib, ok)
	}
}

func TestExtractWheelNativeFilesPreservesPackagePaths(t *testing.T) {
	wheelPath := filepath.Join(t.TempDir(), "runtime.whl")
	wheelFile, err := os.Create(wheelPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(wheelFile)
	ext := nativeLibExts()[0]
	paths := map[string]string{
		"litert_lm/litert-lm" + ext:                                      "main",
		"litert_lm/vendors/intel_openvino/dispatch/LiteRtDispatch" + ext: "dispatch",
		"litert_lm/dxcompiler" + ext:                                     "compiler",
		"litert_lm/ignored.py":                                           "python",
	}
	for name, contents := range paths {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wheelFile.Close(); err != nil {
		t.Fatal(err)
	}

	libDir := filepath.Join(t.TempDir(), "lib")
	if err := extractWheelNativeFiles(wheelPath, libDir, FetchOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"litert_lm/litert-lm" + ext:                                      "main",
		"litert_lm/vendors/intel_openvino/dispatch/LiteRtDispatch" + ext: "dispatch",
		"litert_lm/dxcompiler" + ext:                                     "compiler",
	} {
		got, err := os.ReadFile(filepath.Join(libDir, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("extracted %s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(libDir, "litert_lm", "ignored.py")); !os.IsNotExist(err) {
		t.Errorf("Python package file should not be extracted, stat error = %v", err)
	}
}
