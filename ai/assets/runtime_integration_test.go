//go:build wasip1

package assets

import (
	"context"
	"os"
	"testing"
	"time"

	"dload"
)

func TestEnsureRuntimeLoadsLiteRTLibrary(t *testing.T) {
	if os.Getenv("RUSTICATED_TEST_LITERT_RUNTIME") != "1" {
		t.Skip("set RUSTICATED_TEST_LITERT_RUNTIME=1 to download and load the real PyPI runtime wheel")
	}

	osName, arch := testRuntimeHost()
	if osName != "windows" || arch != "amd64" {
		t.Skipf("real LiteRT-LM load smoke test targets windows/amd64; host is %s/%s", osName, arch)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	libPath, err := EnsureRuntime(ctx, FetchOptions{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureRuntime() error = %v", err)
	}
	if info, err := os.Stat(libPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("EnsureRuntime() path %q is not a regular file: %v", libPath, err)
	}

	handle, err := dload.DylibOpen(libPath, dload.RTLD_NOW|dload.RTLD_GLOBAL)
	if err != nil {
		t.Fatalf("DylibOpen(%q) error = %v", libPath, err)
	}
	defer func() {
		if err := dload.DylibClose(handle); err != nil {
			t.Errorf("DylibClose() error = %v", err)
		}
	}()

	for _, symbol := range []string{
		"litert_lm_engine_settings_create",
		"litert_lm_engine_create",
		"litert_lm_conversation_send_message_stream",
	} {
		if address, err := dload.DylibSym(handle, symbol); err != nil || address == 0 {
			t.Errorf("DylibSym(%q) = %#x, %v", symbol, address, err)
		}
	}
}
