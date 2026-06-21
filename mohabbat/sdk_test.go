package mohabbat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Test goBinFromRoot helper to verify it resolves the correct go binary path
func TestGoBinFromRoot(t *testing.T) {
	tmpDir := t.TempDir()

	// Create an httptest.Server to simulate the github release API / artifact download
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("mock_tarball_data"))
	}))
	defer ts.Close()

	// In a real environment, we'd override SDK info const URLs, but to test sdk.go directly:
	// If sdk.go uses hardcoded globals, integration testing might just test the verify logics
	// skipping full integration unless we inject via some internal variable or monkey patch.

	// For now, let's verify cache detection works locally
	testVersion := "go1.26.4"
	fakeSdkDir := filepath.Join(tmpDir, testVersion)

	// Create dummy go bin so goBinFromRoot finds it
	binDir := filepath.Join(fakeSdkDir, "bin")
	os.MkdirAll(binDir, 0755)

	expectedBin := filepath.Join(binDir, "go.exe")
	if runtime.GOOS != "windows" {
		expectedBin = filepath.Join(binDir, "go")
	}
	os.WriteFile(expectedBin, []byte("dummy exe"), 0755)

	goBin := goBinFromRoot(fakeSdkDir)

	if goBin != expectedBin {
		t.Errorf("expected go bin to resolve to %s, got %s", expectedBin, goBin)
	}
}

// EnsureSDK Test using local httptest mocks to avoid hitting github/internet during standard tests.
// TODO: Refactor EnsureSDK to accept injectable baseURL and http.Client to enable full end-to-end
// testing with httptest.Server for download+cache behavior validation.
func TestEnsureSDK_DownloadAndCache(t *testing.T) {
	_ = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("mock_tarball_data"))
	}))

	t.Skip("EnsureSDK requires injection of baseURL and client for testability")
}
