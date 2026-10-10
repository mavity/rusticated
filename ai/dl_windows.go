//go:build windows

package ai

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	RTLD_NOW    = 0
	RTLD_GLOBAL = 0
)

func dlopen(path string, flags int) (uintptr, error) {
	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] dlopen: loading DLL: %s\n", path)
	}

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
	if err != nil && Verbose {
		fmt.Fprintf(os.Stderr, "[ai] dlopen: LoadLibrary failed for '%s': %v\n", path, err)
	}
	if err == nil && Verbose {
		// Resolve the actual loaded path
		var buf [1024]uint16
		k32 := syscall.NewLazyDLL("kernel32.dll")
		getModuleFileName := k32.NewProc("GetModuleFileNameW")
		if getModuleFileName.Find() == nil {
			r, _, _ := getModuleFileName.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			if r > 0 {
				actualPath := syscall.UTF16ToString(buf[:r])
				fmt.Fprintf(os.Stderr, "[ai] dlopen: loaded DLL actual path: %s\n", actualPath)
			}
		}
	}
	return uintptr(h), err
}

func dlsym(handle uintptr, name string) (uintptr, error) {
	p, err := syscall.GetProcAddress(syscall.Handle(handle), name)
	return p, err
}

func dlclose(handle uintptr) error {
	return syscall.FreeLibrary(syscall.Handle(handle))
}
