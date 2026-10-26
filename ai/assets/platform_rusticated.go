//go:build wasip1

package assets

import (
	"github.com/mavity/rusticated/platform"
	"strings"
	"syscall"
)

// HostOS returns the inferred native host operating system.
func HostOS() string {
	return platform.HostOS()
}

// HostArch returns the inferred native host CPU architecture.
func HostArch() string {
	return platform.HostArch()
}

// InspectRuntimePlatform returns environmental details when running inside a
// Mohabbat / Rusticated WASM host (washmhost via wazero, Node.js, or browser).
func InspectRuntimePlatform() RuntimePlatformInfo {
	osName, arch := platform.HostOS(), platform.HostArch()
	envKind := "desktop"
	if osName == "browser" {
		envKind = "browser"
	}

	info := RuntimePlatformInfo{
		OS:              osName,
		Arch:            arch,
		ExecutionMode:   "mohabbat",
		HostRuntime:     "washmhost",
		EnvironmentKind: envKind,
	}

	if pi := syscall.GetPlatformInfo(); pi != nil {
		wPlat := strings.TrimSpace(pi.WasmPlatform)
		rName := strings.TrimSpace(pi.RusticatedName)
		if rName != "" && wPlat != "" && !strings.EqualFold(rName, wPlat) {
			info.HostRuntime = rName + " (" + wPlat + ")"
		} else if rName != "" {
			info.HostRuntime = rName
		} else if wPlat != "" {
			info.HostRuntime = wPlat
		}
		info.WasmPlatform = wPlat
		info.BuildPlatform = strings.TrimSpace(pi.BuildPlatform)
	}
	return info
}
