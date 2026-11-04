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

func TestUserCacheDirUsesHostPlatformInfoInsteadOfGuestEnvironment(t *testing.T) {
	info := syscall.GetPlatformInfo()
	if info == nil || info.UserHomeDir == "" {
		t.Fatalf("host ABI did not provide a user home directory: %#v", info)
	}
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "LOCALAPPDATA", "home"} {
		t.Setenv(name, "/guest/cache-must-not-be-used")
	}

	want := filepath.Join(info.UserHomeDir, ".cache")
	if info.OSKind == 1 {
		want = filepath.Join(info.UserHomeDir, "AppData", "Local")
	} else if info.OSKind == 3 {
		want = filepath.Join(info.UserHomeDir, "Library", "Caches")
	}
	got, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir() error = %v", err)
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("UserCacheDir() = %q, want ABI-derived host path %q", got, want)
	}
}
