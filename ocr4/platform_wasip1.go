//go:build wasip1

package main

import (
	"os"
	"strings"
)

// HostOS infers the native host OS by examining washmhost-provided environment
// and path conventions, since runtime.GOOS returns "wasip1" for the WASM guest.
func HostOS() string {
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

// HostArch infers the native host CPU architecture.
func HostArch() string {
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
