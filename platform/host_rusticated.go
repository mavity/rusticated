//go:build wasip1

package platform

import (
	"os"
	"strings"
	"sync"
	"syscall"
)

var hostPlatform struct {
	once sync.Once
	os   string
	arch string
}

// HostOS returns the inferred host operating system.
func HostOS() string {
	hostPlatform.once.Do(initHostPlatform)
	return hostPlatform.os
}

// HostArch returns the inferred host CPU architecture.
func HostArch() string {
	hostPlatform.once.Do(initHostPlatform)
	return hostPlatform.arch
}

func initHostPlatform() {
	hostPlatform.os, hostPlatform.arch = detectHostPlatform()
}

func detectHostPlatform() (string, string) {
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

func parseBuildPlatform(buildPlatform string) (string, string, bool) {
	parts := strings.Split(strings.TrimSpace(buildPlatform), "-")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		goos := strings.ToLower(parts[0])
		switch goos {
		case "win32":
			goos = "windows"
		}

		goarch := strings.ToLower(parts[1])
		switch goarch {
		case "x64":
			goarch = "amd64"
		case "ia32":
			goarch = "386"
		}
		return goos, goarch, true
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
	switch bits {
	case 64:
		return hostArchEnv()
	case 32:
		return "386"
	}
	return ""
}

func hostOSEnv() string {
	if os.Getenv("OS") == "Windows_NT" {
		return "windows"
	}
	if home, _ := os.UserHomeDir(); strings.Contains(home, `\`) {
		return "windows"
	}
	if home, _ := os.UserHomeDir(); strings.HasPrefix(home, "/Users/") {
		return "darwin"
	}
	return "linux"
}

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
