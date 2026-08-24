package main

import (
	"debug/pe"
	"fmt"
	"runtime"
)

type dylibTarget struct {
	goos   string
	goarch string
}

// detectDylibTarget inspects a native dynamic library and returns the GOOS/GOARCH
// a satellite must be built for in order to load it.
func detectDylibTarget(path string) (dylibTarget, error) {
	switch runtime.GOOS {
	case "windows":
		f, err := pe.Open(path)
		if err != nil {
			return dylibTarget{}, err
		}
		defer f.Close()
		switch f.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			return dylibTarget{"windows", "amd64"}, nil
		case pe.IMAGE_FILE_MACHINE_ARM64:
			return dylibTarget{"windows", "arm64"}, nil
		case pe.IMAGE_FILE_MACHINE_I386:
			return dylibTarget{"windows", "386"}, nil
		default:
			return dylibTarget{}, fmt.Errorf("unsupported PE machine 0x%x", f.Machine)
		}
	default:
		// ELF/Mach-O detection not yet implemented; assume the host arch.
		return dylibTarget{runtime.GOOS, runtime.GOARCH}, nil
	}
}
