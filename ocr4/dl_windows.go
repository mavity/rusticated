//go:build windows

package main

import (
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	RTLD_NOW    = 0
	RTLD_GLOBAL = 0
)

func dlopen(path string, flags int) (uintptr, error) {
	// Add the directory containing the DLL to the DLL search path.
	// This ensures that dependent DLLs (dxcompiler.dll, dxil.dll, etc.) can be found.
	dir := filepath.Dir(path)

	// Try to set the DLL directory, but don't fail if it's not available
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setDllDir := kernel32.NewProc("SetDllDirectory")
	if setDllDir.Find() == nil {
		setDllDir.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(dir))))
	}

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
