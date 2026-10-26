//go:build !wasip1

package platform

import (
	"runtime"
	"testing"
)

func TestHostPlatformMatchesRuntime(t *testing.T) {
	if got := HostOS(); got != runtime.GOOS {
		t.Fatalf("HostOS() = %q, want %q", got, runtime.GOOS)
	}
	if got := HostArch(); got != runtime.GOARCH {
		t.Fatalf("HostArch() = %q, want %q", got, runtime.GOARCH)
	}
}
