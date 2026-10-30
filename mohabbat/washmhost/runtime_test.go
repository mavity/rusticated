package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunHostHonorsTestTimeout(t *testing.T) {
	wasm := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x07, 0x01, 0x03, 'r', 'u', 'n', 0x00, 0x00,
		0x0a, 0x09, 0x01, 0x07, 0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x0b,
	}
	wasmPath := filepath.Join(t.TempDir(), "infinite-loop.wasm")
	if err := os.WriteFile(wasmPath, wasm, 0o600); err != nil {
		t.Fatalf("write test module: %v", err)
	}
	t.Setenv("MOHABBAT_WASM_FD", wasmPath)
	t.Setenv(washmhostTestTimeoutEnv, "100ms")

	started := time.Now()
	if code := runHost([]string{"guest"}, ""); code == 0 {
		t.Fatal("runHost succeeded despite the guest exceeding its test timeout")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("runHost did not interrupt the guest promptly: elapsed %s", elapsed)
	}
}
