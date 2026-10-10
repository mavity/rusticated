//go:build !windows && !wasip1

package ai

import (
	"fmt"
	"os"

	"github.com/ebitengine/purego"
)

const (
	RTLD_NOW    = 0x2
	RTLD_GLOBAL = 0x100
)

func dlopen(path string, flags int) (uintptr, error) {
	if Verbose {
		fmt.Fprintf(os.Stderr, "[ai] dlopen: loading shared library: %s\n", path)
	}
	h, err := purego.Dlopen(path, flags)
	if err != nil && Verbose {
		fmt.Fprintf(os.Stderr, "[ai] dlopen: failed to load '%s': %v\n", path, err)
	}
	if err == nil && Verbose {
		fmt.Fprintf(os.Stderr, "[ai] dlopen: loaded shared library: %s\n", path)
	}
	return h, err
}

func dlsym(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}

func dlclose(handle uintptr) error {
	return purego.Dlclose(handle)
}
