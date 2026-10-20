package dload

import (
	"runtime"
	"testing"
)

func TestDloadBasic(t *testing.T) {
	libName := "kernel32.dll"
	fnName := "GetCurrentProcessId"
	if runtime.GOOS != "windows" {
		libName = "libc.so.6"
		fnName = "getpid"
	}

	handle, err := DylibOpen(libName, RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(%q): %v", libName, err)
	}
	defer DylibClose(handle)

	sym, err := DylibSym(handle, fnName)
	if err != nil {
		t.Fatalf("DylibSym(%q): %v", fnName, err)
	}
	if sym == 0 {
		t.Fatalf("expected non-zero symbol address")
	}

	// Call function (0 args, returns uint64)
	res, err := DylibCall(sym, []byte{0x60, 0, 1, WasmTypeI64}, nil, 0)
	if err != nil {
		t.Fatalf("DylibCall: %v", err)
	}
	if len(res) == 0 || res[0] == 0 {
		t.Fatalf("expected non-zero PID, got %v", res)
	}
	t.Logf("Called %s -> returned %d", fnName, res[0])

	// Test Memory Allocation, Write, and Read
	allocSize := uint64(64)
	memPtr, err := DylibAlloc(handle, allocSize)
	if err != nil {
		t.Fatalf("DylibAlloc: %v", err)
	}
	defer DylibFree(handle, memPtr)

	testData := []byte("hello from dload!")
	if err := DylibWriteMem(handle, memPtr, testData); err != nil {
		t.Fatalf("DylibWriteMem: %v", err)
	}

	readBuf := make([]byte, len(testData))
	if err := DylibReadMem(handle, memPtr, readBuf); err != nil {
		t.Fatalf("DylibReadMem: %v", err)
	}
	if string(readBuf) != string(testData) {
		t.Fatalf("memory read mismatch: got %q, want %q", string(readBuf), string(testData))
	}
}
