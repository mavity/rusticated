package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"unsafe"
)

func main() {
	ptrStr := os.Getenv("WASHMHOST_PAYLOAD_PTR")
	lenStr := os.Getenv("WASHMHOST_PAYLOAD_LEN")

	if ptrStr == "" || lenStr == "" {
		fmt.Fprintf(os.Stderr, "WASHMHOST_PAYLOAD_PTR or WASHMHOST_PAYLOAD_LEN not set\n")
		os.Exit(1)
	}

	ptrVal, err := strconv.ParseUint(ptrStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid WASHMHOST_PAYLOAD_PTR: %v\n", err)
		os.Exit(1)
	}

	lenVal, err := strconv.ParseUint(lenStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid WASHMHOST_PAYLOAD_LEN: %v\n", err)
		os.Exit(1)
	}

	// Reconstruct the Go slice from the passed pointer
	var payloadBytes []byte
	if ptrVal != 0 && lenVal > 0 {
		payloadBytes = unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptrVal))), lenVal)
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
