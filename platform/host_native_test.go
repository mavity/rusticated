//go:build !wasip1

package platform

import (
	"runtime"
	"strings"
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

func TestNativeSystemInfoQueries(t *testing.T) {
	appearance := SystemAppearance()
	if appearance != "" && appearance != "dark" && appearance != "light" {
		t.Fatalf("SystemAppearance() = %q, want \"\", \"dark\", or \"light\"", appearance)
	}
	power := PowerSource()
	if power != "" && power != "ac" && power != "battery" {
		t.Fatalf("PowerSource() = %q, want \"\", \"ac\", or \"battery\"", power)
	}
	for name, val := range map[string]string{
		"KeyboardLayoutID": KeyboardLayoutID(),
		"SystemLocale":     SystemLocale(),
		"SystemTimeZone":   SystemTimeZone(),
		"SystemAppearance": appearance,
		"PowerSource":      power,
	} {
		if strings.ContainsRune(val, 0) {
			t.Fatalf("%s() contains NUL byte: %q", name, val)
		}
	}
	if runtime.GOOS == "windows" {
		if KeyboardLayoutID() == "" {
			t.Fatal("KeyboardLayoutID() returned empty on Windows")
		}
		if SystemLocale() == "" {
			t.Fatal("SystemLocale() returned empty on Windows")
		}
		if SystemTimeZone() == "" {
			t.Fatal("SystemTimeZone() returned empty on Windows")
		}
	}
}
