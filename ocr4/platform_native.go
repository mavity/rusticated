//go:build !wasip1

package main

import "runtime"

func HostOS() string   { return runtime.GOOS }
func HostArch() string { return runtime.GOARCH }
