//go:build !wasm

package main

import "runtime"

func HostOS() string {
	return runtime.GOOS
}

func HostArch() string {
	return runtime.GOARCH
}
