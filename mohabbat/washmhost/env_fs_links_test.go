package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func runLinkABICall(t *testing.T, env *HostEnv, mod *mockModule, call func()) (uint32, uint64) {
	t.Helper()
	const ovPtr = uint32(0x300)
	if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
		t.Fatal("failed to clear operation result")
	}
	call()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		env.Poll(ctx, mod)
		result, ok := mod.Memory().Read(ovPtr, 24)
		if !ok {
			t.Fatal("failed to read operation result")
		}
		if binary.LittleEndian.Uint32(result)&1 != 0 {
			return binary.LittleEndian.Uint32(result[4:]), binary.LittleEndian.Uint64(result[16:])
		}
		if ctx.Err() != nil {
			t.Fatal("timed out waiting for link operation")
		}
	}
}

func writeLinkTestString(t *testing.T, mod *mockModule, ptr uint32, value string) uint32 {
	t.Helper()
	if !mod.Memory().Write(ptr, []byte(value)) {
		t.Fatal("failed to write link-operation path")
	}
	return uint32(len(value))
}

func TestPathLinkOperations(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(4096)
	dir := t.TempDir()
	source := filepath.Join(dir, "target")
	hardLink := filepath.Join(dir, "hard-link")
	symbolicLink := filepath.Join(dir, "symbolic-link")
	if err := os.WriteFile(source, []byte("link data"), 0o644); err != nil {
		t.Fatal(err)
	}

	sourceLen := writeLinkTestString(t, mod, 0x100, source)
	hardLen := writeLinkTestString(t, mod, 0x180, hardLink)
	errno, _ := runLinkABICall(t, env, mod, func() {
		env.sys_path_link(context.Background(), mod, []uint64{0x300, 0x100, uint64(sourceLen), 0x180, uint64(hardLen), 0})
	})
	if errno != wasiSuccess {
		t.Fatalf("path_link returned WASI errno %d", errno)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Stat(hardLink)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sourceInfo, linkInfo) {
		t.Fatal("path_link did not create a hard link to the same filesystem object")
	}

	sourceSymlink := filepath.Join(dir, "source-link")
	followedLink := filepath.Join(dir, "followed-link")
	if err := os.Symlink("target", sourceSymlink); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("host policy does not permit creating a source symlink: %v", err)
		}
		t.Fatalf("creating source symlink for follow-source test: %v", err)
	}
	sourceSymlinkLen := writeLinkTestString(t, mod, 0x100, sourceSymlink)
	followedLinkLen := writeLinkTestString(t, mod, 0x180, followedLink)
	errno, _ = runLinkABICall(t, env, mod, func() {
		env.sys_path_link(context.Background(), mod, []uint64{0x300, 0x100, uint64(sourceSymlinkLen), 0x180, uint64(followedLinkLen), 1})
	})
	if errno != wasiSuccess {
		t.Fatalf("path_link with follow-source flag returned WASI errno %d", errno)
	}
	followedInfo, err := os.Stat(followedLink)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err = os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(sourceInfo, followedInfo) {
		t.Fatal("follow-source path_link did not link the symbolic link's referent")
	}

	targetLen := writeLinkTestString(t, mod, 0x200, "target")
	symbolicLen := writeLinkTestString(t, mod, 0x280, symbolicLink)
	errno, _ = runLinkABICall(t, env, mod, func() {
		env.sys_path_symlink(context.Background(), mod, []uint64{0x300, 0x200, uint64(targetLen), 0x280, uint64(symbolicLen), 0})
	})
	if errno == wasiEACCES || errno == wasiEPERM || errno == wasiENOTSUP {
		t.Skipf("host filesystem or policy does not permit symbolic links (WASI errno %d)", errno)
	}
	if errno != wasiSuccess {
		t.Fatalf("path_symlink returned WASI errno %d", errno)
	}

	readPathLen := writeLinkTestString(t, mod, 0x100, symbolicLink)
	errno, required := runLinkABICall(t, env, mod, func() {
		env.sys_path_readlink(context.Background(), mod, []uint64{0x300, 0x100, uint64(readPathLen), 0x400, 2})
	})
	if errno != wasiERANGE || required != uint64(len("target")) {
		t.Fatalf("undersized path_readlink returned errno=%d required=%d", errno, required)
	}

	errno, count := runLinkABICall(t, env, mod, func() {
		env.sys_path_readlink(context.Background(), mod, []uint64{0x300, 0x100, uint64(readPathLen), 0x400, 16})
	})
	if errno != wasiSuccess || count != uint64(len("target")) {
		t.Fatalf("path_readlink returned errno=%d count=%d", errno, count)
	}
	got, ok := mod.Memory().Read(0x400, uint32(count))
	if !ok || string(got) != "target" {
		t.Fatalf("path_readlink returned target %q, want %q", got, "target")
	}

	errno, _ = runLinkABICall(t, env, mod, func() {
		env.sys_path_link(context.Background(), mod, []uint64{0x300, 0x100, uint64(readPathLen), 0x180, uint64(hardLen), 2})
	})
	if errno != wasiEINVAL {
		t.Fatalf("path_link accepted unknown flags: got %d, want %d", errno, wasiEINVAL)
	}
}
