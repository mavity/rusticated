package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
)

func ensurePosixOutputRunnable(args []string) {
	if runtime.GOOS == "windows" {
		return
	}

	for i := 1; i+1 < len(args); i++ {
		if args[i] != "-o" {
			continue
		}
		output := args[i+1]
		f, err := os.OpenFile(output, os.O_CREATE, 0o755)
		if err == nil {
			_ = f.Close()
			_ = os.Chmod(output, 0o755)
		}
		return
	}
}

func main() {
	// Set an environment variable for the guest to know the host's temp directory if not already set.
	if os.Getenv("MOHABBAT_HOST_TEMPDIR") == "" {
		os.Setenv("MOHABBAT_HOST_TEMPDIR", os.TempDir())
	}

	ref := os.Getenv("MOHABBAT_WASM_FD")
	if ref == "" {
		fmt.Fprintf(os.Stderr, "washmhost: MOHABBAT_WASM_FD not set\n")
		os.Exit(1)
	}

	var r io.Reader
	if n, err := strconv.ParseUint(ref, 10, 64); err == nil {
		r = os.NewFile(uintptr(n), "wasm")
	} else {
		f, err := os.Open(ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "washmhost: failed to open payload: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		r = f
	}

	payloadBytes, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "washmhost: failed to read payload: %v\n", err)
		os.Exit(1)
	}

	argSlice := os.Args
	ensurePosixOutputRunnable(argSlice)

	// Inject host OS/ARCH for the guest.
	os.Setenv("MOHABBAT_HOST_OS", runtime.GOOS)
	os.Setenv("MOHABBAT_HOST_ARCH", runtime.GOARCH)

	exitCode, err := RunWasm(context.Background(), payloadBytes, argSlice)
	if err != nil {
		fmt.Fprintf(os.Stderr, "washmhost: %v\n", err)
		if exitCode == 0 {
			os.Exit(1)
		}
		os.Exit(exitCode)
	}

	os.Exit(exitCode)
}
