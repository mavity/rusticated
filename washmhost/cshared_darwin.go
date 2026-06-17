package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"os"
	"runtime"
	"unsafe"
)

//export run_payload
func run_payload(payload *C.char, payloadLen C.int) C.int {
	data := C.GoBytes(unsafe.Pointer(payload), payloadLen)

	if os.Getenv("MOHABBAT_HOST_TEMPDIR") == "" {
		os.Setenv("MOHABBAT_HOST_TEMPDIR", os.TempDir())
	}
	if os.Getenv("MOHABBAT_HOST_OS") == "" {
		os.Setenv("MOHABBAT_HOST_OS", runtime.GOOS)
	}
	if os.Getenv("MOHABBAT_HOST_ARCH") == "" {
		os.Setenv("MOHABBAT_HOST_ARCH", runtime.GOARCH)
	}

	args := os.Args
	if len(args) == 0 {
		args = []string{"washmhost"}
	}
	// In the dlopen path, os.Args comes from the brot host process:
	// [brot_tmp_path, vegetable_path, user_args...]
	// Set MOHABBAT_VEGETABLE_PATH so the brain knows it's inside a vegetable,
	// and fix args[0] to the vegetable path (matching the Linux execve path).
	if len(args) > 1 && os.Getenv("MOHABBAT_VEGETABLE_PATH") == "" {
		os.Setenv("MOHABBAT_VEGETABLE_PATH", args[1])
		args[0] = args[1]
	}
	ensurePosixOutputRunnable(args)

	exitCode, _ := RunWasm(context.Background(), data, args)
	return C.int(exitCode)
}
