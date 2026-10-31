//go:build wasip1

package assets

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDefaultCacheDirUsesHostOSDirectory(t *testing.T) {
	info := syscall.GetPlatformInfo()
	if info == nil {
		t.Fatal("GetPlatformInfo() returned nil")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}

	want := filepath.Join(home, ".cache", "kabibi", "litert_cache")
	if info.OSKind == 1 {
		want = filepath.Join(home, "AppData", "Local", "kabibi", "litert_cache")
	}

	got, err := DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir() error = %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("DefaultCacheDir() = %q, want %q for OS kind %d", got, want, info.OSKind)
	}
}
