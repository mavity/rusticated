package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
)

func main() {
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
