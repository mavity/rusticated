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

func TestParseBuildPlatformCanonicalizesNodeNames(t *testing.T) {
	tests := []struct {
		name     string
		build    string
		wantOS   string
		wantArch string
	}{
		{name: "Windows x64", build: "win32-x64", wantOS: "windows", wantArch: "amd64"},
		{name: "Linux x64", build: "linux-x64", wantOS: "linux", wantArch: "amd64"},
		{name: "macOS arm64", build: "darwin-arm64", wantOS: "darwin", wantArch: "arm64"},
		{name: "Wazero canonical names", build: "windows-amd64", wantOS: "windows", wantArch: "amd64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			goos, goarch, ok := parseBuildPlatform(tt.build)
			if !ok {
				t.Fatalf("parseBuildPlatform(%q) failed", tt.build)
			}
			if goos != tt.wantOS || goarch != tt.wantArch {
				t.Fatalf("parseBuildPlatform(%q) = %s/%s, want %s/%s",
					tt.build, goos, goarch, tt.wantOS, tt.wantArch)
			}
		})
	}
}
