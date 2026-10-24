//go:build wasip1

package assets

import (
	"os"
	"strings"
	"syscall"
)

// hostOSArch resolves the underlying native host OS and architecture from the
// rusticated platform ABI. Under wasip1 runtime.GOOS/GOARCH report
// "wasip1"/"wasm", so we must consult syscall.GetPlatformInfo, whose
// BuildPlatform field carries the host "<goos>-<goarch>" (e.g. "darwin-arm64")
// and whose OSName/CPUBitness describe the running host.
func hostOSArch() (string, string) {
	pi := syscall.GetPlatformInfo()
	if pi != nil {
		if os := osFromName(pi.OSName); os == "browser" {
			return "browser", "wasm"
		}
		if goos, goarch, ok := parseBuildPlatform(pi.BuildPlatform); ok {
			return goos, goarch
		}
		os := osFromName(pi.OSName)
		arch := archFromBitness(pi.CPUBitness)
		if os != "" && arch != "" {
			return os, arch
		}
	}
	return hostOSEnv(), hostArchEnv()
}

func parseBuildPlatform(bp string) (string, string, bool) {
	bp = strings.TrimSpace(bp)
	parts := strings.Split(bp, "-")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func osFromName(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "browser"):
		return "browser"
	case strings.Contains(n, "windows"):
		return "windows"
	case strings.Contains(n, "darwin"), strings.Contains(n, "mac"):
		return "darwin"
	case strings.Contains(n, "linux"):
		return "linux"
	}
	return ""
}

func archFromBitness(bits uint8) string {
	// CPUBitness alone is ambiguous (64 could be amd64 or arm64); only used as a
	// weak hint, so prefer the environment heuristics when available.
	switch bits {
	case 64:
		return hostArchEnv()
	case 32:
		return "386"
	}
	return ""
}

// TODO: must be removed, environment variables have no role to play in it
// hostOSEnv / hostArchEnv mirror the washmhost-provided environment conventions
// as a last-resort fallback.
func hostOSEnv() string {
	if os.Getenv("OS") == "Windows_NT" {
		return "windows"
	}
	if h, _ := os.UserHomeDir(); strings.Contains(h, `\`) {
		return "windows"
	}
	if h, _ := os.UserHomeDir(); strings.HasPrefix(h, "/Users/") {
		return "darwin"
	}
	return "linux"
}

// TODO: must be removed, environment variables have no role to play in it
func hostArchEnv() string {
	switch os.Getenv("PROCESSOR_ARCHITECTURE") {
	case "AMD64":
		return "amd64"
	case "ARM64":
		return "arm64"
	}
	switch os.Getenv("HOSTTYPE") {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return "amd64"
}

// HostOS returns the inferred native host operating system.
func HostOS() string {
	os, _ := hostOSArch()
	return os
}

// HostArch returns the inferred native host CPU architecture.
func HostArch() string {
	_, arch := hostOSArch()
	return arch
}

// InspectRuntimePlatform returns environmental details when running inside a
// Mohabbat / Rusticated WASM host (washmhost via wazero, Node.js, or browser).
func InspectRuntimePlatform() RuntimePlatformInfo {
	osName, arch := hostOSArch()
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
