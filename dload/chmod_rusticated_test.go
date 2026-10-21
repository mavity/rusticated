//go:build wasip1

package dload

import (
	"os"
	"syscall"
	"testing"
)

func TestRusticatedWorkerPermissionsAreRepairedOnCacheHit(t *testing.T) {
	platform := syscall.GetPlatformInfo()
	const flavour = "darwin-arm64"
	path, err := ensureWorkerExtracted(flavour)
	if err != nil {
		t.Fatalf("ensureWorkerExtracted(%q): %v", flavour, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Errorf("restore worker executable mode: %v", err)
		}
	})

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("remove executable bits from extracted worker: %v", err)
	}
	if _, err := ensureWorkerExtracted(flavour); err != nil {
		t.Fatalf("ensureWorkerExtracted(%q) after permission change: %v", flavour, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat extracted worker: %v", err)
	}
	if platform.OSKind != 1 && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("worker mode = %04o, want at least one executable bit", info.Mode().Perm())
	}
}
