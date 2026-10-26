//go:build wasip1

package platform

import "testing"

func TestHostPlatformIsReported(t *testing.T) {
	hostOS := HostOS()
	if hostOS == "" || hostOS == "wasip1" {
		t.Fatalf("HostOS() = %q, want a host operating system", hostOS)
	}
	hostArch := HostArch()
	if hostArch == "" || (hostArch == "wasm" && hostOS != "browser") {
		t.Fatalf("HostArch() = %q for HostOS() = %q", hostArch, hostOS)
	}
}
