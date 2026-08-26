//go:build windows

package main

import (
	"syscall"
)

const (
	RTLD_NOW    = 0
	RTLD_GLOBAL = 0
)

func dlopen(path string, flags int) (uintptr, error) {
	h, err := syscall.LoadLibrary(path)
	return uintptr(h), err
}

func dlsym(handle uintptr, name string) (uintptr, error) {
	p, err := syscall.GetProcAddress(syscall.Handle(handle), name)
	return p, err
}

func dlclose(handle uintptr) error {
	return syscall.FreeLibrary(syscall.Handle(handle))
}
