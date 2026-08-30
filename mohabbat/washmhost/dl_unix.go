//go:build !windows

package main

import "github.com/ebitengine/purego"

const (
	RTLD_NOW    = 0x2
	RTLD_GLOBAL = 0x100
)

func dlopen(path string, flags int) (uintptr, error) {
	return purego.Dlopen(path, flags)
}

func dlsym(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}

func dlclose(handle uintptr) error {
	return purego.Dlclose(handle)
}
