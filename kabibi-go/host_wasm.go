//go:build wasm

package main

import "syscall"

func HostOS() string {
	pi := syscall.GetPlatformInfo()
	switch pi.OSKind {
	case 1:
		return "windows"
	case 2:
		return "linux"
	case 3:
		return "darwin"
	default:
		return "unknown"
	}
}

func HostArch() string {
	pi := syscall.GetPlatformInfo()
	switch pi.CPUType {
	case 1:
		return "amd64"
	case 2:
		return "arm64"
	default:
		return "unknown"
	}
}
