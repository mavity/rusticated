package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

type unknownTypeDirEntry struct{}

func (unknownTypeDirEntry) Name() string               { return "unknown" }
func (unknownTypeDirEntry) IsDir() bool                { return false }
func (unknownTypeDirEntry) Type() os.FileMode          { return 0 }
func (unknownTypeDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func TestDirEntryUnknownTypeUsesFileKind(t *testing.T) {
	if kind := dirEntryKind(unknownTypeDirEntry{}); kind != statKindFile {
		t.Fatalf("dirEntryKind(unknown type) = %d, want file kind %d", kind, statKindFile)
	}
}

func decodeDirEntries(buf []byte) []dirEntryInfo {
	var entries []dirEntryInfo
	for len(buf) >= 2 {
		terminator := bytes.IndexByte(buf[1:], 0)
		if terminator < 0 {
			break
		}
		nameEnd := terminator + 1
		entries = append(entries, dirEntryInfo{
			Kind: uint32(buf[0]),
			Name: string(buf[1:nameEnd]),
		})
		buf = buf[nameEnd+1:]
	}
	return entries
}

func TestSysDirReadExtended(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(1024 * 1024)
	mem := mod.Memory()

	setupDir := func(t *testing.T, entries []string) (uint64, string) {
		tmpDir := t.TempDir()
		for _, name := range entries {
			_ = os.WriteFile(filepath.Join(tmpDir, name), []byte("content"), 0644)
		}
		f, _ := os.Open(tmpDir)
		env.mu.Lock()
		h := uint64(len(env.handles) + 200)
		env.handles[h] = f
		env.mu.Unlock()
		// Important for Windows: close handle before TempDir cleanup
		t.Cleanup(func() {
			env.mu.Lock()
			if f, ok := env.handles[h].(*os.File); ok {
				f.Close()
			} else if scan, ok := env.handles[h].(*DirScan); ok {
				if scan.File != nil {
					scan.File.Close()
				}
			}
			delete(env.handles, h)
			env.mu.Unlock()
		})
		return h, tmpDir
	}

	runOp := func(t *testing.T, ovPtr uint32) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			buf, ok := mem.Read(ovPtr, 4)
			if !ok {
				t.Fatalf("overlapped pointer %d is out of bounds", ovPtr)
			}
			if binary.LittleEndian.Uint32(buf) == 1 {
				if env.PendingOps() == 0 {
					for {
						select {
						case op := <-env.fileOpsQueue:
							op()
						default:
							return
						}
					}
				}
				return
			}

			select {
			case op := <-env.fileOpsQueue:
				op()
			case <-timer.C:
				t.Fatalf("timeout waiting for overlapped completion at %d", ovPtr)
			}
		}
	}

	beginDirRead := func(t *testing.T, stack []uint64) {
		t.Helper()
		ovPtr := uint32(stack[0])
		if !mem.Write(ovPtr, make([]byte, 24)) {
			t.Fatalf("failed to clear overlapped header at %d", ovPtr)
		}
		env.sys_dir_read(context.Background(), mod, stack)
	}

	readDir := func(t *testing.T, stack []uint64) {
		t.Helper()
		beginDirRead(t, stack)
		runOp(t, uint32(stack[0]))
	}

	waitForCancelledRead := func(t *testing.T) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for len(env.fileOpsQueue) < 2 {
			select {
			case <-timer.C:
				t.Fatal("timeout waiting for cancelled read completion")
			case <-ticker.C:
			}
		}
		for len(env.fileOpsQueue) > 0 {
			op := <-env.fileOpsQueue
			op()
		}
	}

	t.Run("1. happy path - read all in one go", func(t *testing.T) {
		h, _ := setupDir(t, []string{"fileA", "fileB"})
		stack := []uint64{200, h, 400, 100}
		readDir(t, stack)
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		data, _ := mem.Read(400, uint32(n))
		entries := decodeDirEntries(data)
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name
			if entry.Kind != statKindFile {
				t.Errorf("entry %q has kind %d, want file kind %d", entry.Name, entry.Kind, statKindFile)
			}
		}
		sort.Strings(names)
		if len(names) != 2 || names[0] != "fileA" || names[1] != "fileB" {
			t.Errorf("got %v", names)
		}
	})

	t.Run("2. empty directory", func(t *testing.T) {
		h, _ := setupDir(t, nil)
		stack := []uint64{200, h, 400, 100}
		readDir(t, stack)
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 0 {
			t.Errorf("expected 0 bytes, got %d", n)
		}
	})

	t.Run("3. fragmentation - small buffer reading one by one", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b"})
		res := ""
		for i := 0; i < 3; i++ {
			readDir(t, []uint64{200, h, 400, 2})
			buf, _ := mem.Read(200, 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			data, _ := mem.Read(400, uint32(n))
			res += string(data)
		}
		entries := decodeDirEntries([]byte(res))
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name
		}
		sort.Strings(names)
		if len(names) != 2 || names[0] != "a" || names[1] != "b" {
			t.Errorf("got %v", names)
		}
	})

	t.Run("4. fragmentation - 1 byte at a time stress", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abc"})
		res := ""
		for i := 0; i < 5; i++ {
			readDir(t, []uint64{200, h, 400, 1})
			buf, _ := mem.Read(200, 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			if n != 1 {
				t.Fatalf("step %d: expected 1 byte, got %d", i, n)
			}
			data, _ := mem.Read(400, 1)
			res += string(data)
		}
		entries := decodeDirEntries([]byte(res))
		if len(entries) != 1 || entries[0].Name != "abc" || entries[0].Kind != statKindFile {
			t.Errorf("got entries %+v", entries)
		}
	})

	t.Run("5. buffer matches entry size exactly", func(t *testing.T) {
		h, _ := setupDir(t, []string{"longname"})
		readDir(t, []uint64{200, h, 400, 10})
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 10 {
			t.Errorf("expected 10, got %d", n)
		}
		data, _ := mem.Read(400, 10)
		want := append([]byte{byte(statKindFile)}, []byte("longname\x00")...)
		if !bytes.Equal(data, want) {
			t.Errorf("got %q", data)
		}
	})

	t.Run("6. large requested length, smaller available", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		readDir(t, []uint64{200, h, 400, 1000})
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 3 { // kind, "a", and NUL
			t.Errorf("expected 3, got %d", n)
		}
	})

	t.Run("7. EBADF - invalid handle", func(t *testing.T) {
		readDir(t, []uint64{200, 99999, 400, 100})
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEBADF {
			t.Errorf("EBADF errno = %d, want %d", got, wasiEBADF)
		}
	})

	t.Run("8. handle is a file instead of directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		tmp := tmpDir + "/regular.txt"
		_ = os.WriteFile(tmp, []byte("xxx"), 0644)
		f, _ := os.Open(tmp)
		h := uint64(250)
		env.mu.Lock()
		env.handles[h] = f
		env.mu.Unlock()
		defer f.Close()

		readDir(t, []uint64{200, h, 400, 100})
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 0 {
			t.Errorf("regular file should have 0 dir entries, got %d", n)
		}
	})

	t.Run("9. EINVAL - invalid pointer", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		readDir(t, []uint64{200, h, 2000000, 100})
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEINVAL {
			t.Errorf("invalid pointer errno = %d, want %d", got, wasiEINVAL)
		}
	})

	t.Run("10. EINVAL - invalid length", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		readDir(t, []uint64{200, h, 400, 2000000})
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEINVAL {
			t.Errorf("invalid length errno = %d, want %d", got, wasiEINVAL)
		}
	})

	t.Run("11. state persistence - DirScan conversion", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		env.mu.Lock()
		_, isFile := env.handles[h].(*os.File)
		env.mu.Unlock()
		if !isFile {
			t.Fatal("initially should be *os.File")
		}

		readDir(t, []uint64{200, h, 400, 10})

		env.mu.Lock()
		_, isScan := env.handles[h].(*DirScan)
		env.mu.Unlock()
		if !isScan {
			t.Fatal("should have converted to *DirScan")
		}
	})

	t.Run("12. handle hijacked (closed mid-scan)", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b", "c"})
		readDir(t, []uint64{200, h, 400, 2})

		env.mu.Lock()
		scan, ok := env.handles[h].(*DirScan)
		env.mu.Unlock()
		if !ok {
			t.Fatal("directory handle was not converted to DirScan")
		}
		scan.File.Close()
		env.mu.Lock()
		delete(env.handles, h)
		env.mu.Unlock()

		readDir(t, []uint64{300, h, 500, 2})
		buf, _ := mem.Read(300, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEBADF {
			t.Errorf("hijacked handle errno = %d, want %d", got, wasiEBADF)
		}
	})

	t.Run("13. concurrent reads same handle stress", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b", "c", "d", "e", "f", "g", "h"})
		for i := 0; i < 6; i++ {
			beginDirRead(t, []uint64{uint64(200 + i*30), h, uint64(400 + i*5), 2})
		}
		for i := 0; i < 6; i++ {
			runOp(t, uint32(200+i*30))
		}
		totalBytes := uint64(0)
		for i := 0; i < 6; i++ {
			buf, _ := mem.Read(uint32(200+i*30), 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			if n != 2 {
				t.Errorf("read %d returned %d bytes, want 2", i, n)
			}
			totalBytes += n
		}
		if totalBytes != 12 {
			t.Errorf("concurrent reads returned %d bytes, want 12", totalBytes)
		}
	})

	t.Run("14. large directory stress - 100 files", func(t *testing.T) {
		var entries []string
		for i := 0; i < 100; i++ {
			entries = append(entries, fmt.Sprintf("file%03d", i))
		}
		h, _ := setupDir(t, entries)

		var allData []byte
		for {
			readDir(t, []uint64{200, h, 400, 64})
			buf, _ := mem.Read(200, 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			if n == 0 {
				break
			}
			data, _ := mem.Read(400, uint32(n))
			allData = append(allData, data...)
		}
		if totalNames := len(decodeDirEntries(allData)); totalNames != 100 {
			t.Errorf("got %d entries, want 100", totalNames)
		}
	})

	t.Run("15. pathological names - dots, spaces, unicode", func(t *testing.T) {
		entries := []string{".", "..", "file with space", "日本語.txt"}
		h, _ := setupDir(t, entries)

		readDir(t, []uint64{200, h, 400, 500})
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		data, _ := mem.Read(400, uint32(n))
		decoded := decodeDirEntries(data)
		hasSpace := false
		hasUnicode := false
		for _, entry := range decoded {
			if entry.Name == "file with space" {
				hasSpace = true
			}
			if entry.Name == "日本語.txt" {
				hasUnicode = true
			}
		}
		if !hasSpace || !hasUnicode {
			t.Errorf("got %v", decoded)
		}
	})

	t.Run("16. cancellation mid-read", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		beginDirRead(t, []uint64{200, h, 400, 10})
		env.sys_cancel(context.Background(), mod, []uint64{200})
		waitForCancelledRead(t)
	})

	t.Run("17. repeated calls after EOF", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		readDir(t, []uint64{200, h, 400, 100})
		readDir(t, []uint64{200, h, 400, 100})
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 0 {
			t.Errorf("expected 0, got %d", n)
		}
	})

	t.Run("18. directory re-open freshness", func(t *testing.T) {
		entries := []string{"a"}
		h1, path := setupDir(t, entries)
		readDir(t, []uint64{200, h1, 400, 100})

		f2, _ := os.Open(path)
		h2 := uint64(260)
		env.mu.Lock()
		env.handles[h2] = f2
		env.mu.Unlock()
		t.Cleanup(func() { f2.Close() })

		readDir(t, []uint64{300, h2, 500, 100})
		buf, _ := mem.Read(300, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 3 {
			t.Error("re-opened dir should have content")
		}
	})

	t.Run("19. huge length check", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		stack := []uint64{200, h, 400, 0xFFFFFFFF}
		readDir(t, stack)
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEINVAL {
			t.Errorf("huge length errno = %d, want %d", got, wasiEINVAL)
		}
	})

	t.Run("20. overwrite buffer verify", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abc"})
		mem.Write(400, []byte("garbagegarbagegarbage"))
		readDir(t, []uint64{200, h, 400, 100})
		data, _ := mem.Read(400, 5)
		want := append([]byte{byte(statKindFile)}, []byte("abc\x00")...)
		if !bytes.Equal(data, want) {
			t.Errorf("got %q, buffer not overwritten correctly", data)
		}
	})

	t.Run("21. leftover partial resume", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abcd"})
		readDir(t, []uint64{200, h, 400, 2})
		readDir(t, []uint64{300, h, 500, 4})

		b1, _ := mem.Read(400, 2)
		b2, _ := mem.Read(500, 4)
		want := append([]byte{byte(statKindFile)}, []byte("abcd\x00")...)
		if !bytes.Equal(append(b1, b2...), want) {
			t.Errorf("got %q + %q", string(b1), string(b2))
		}
	})

	t.Run("22. multiple entries into one buffer", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b", "c"})
		readDir(t, []uint64{200, h, 400, 9})
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 9 {
			t.Errorf("got %d bytes, want 9", n)
		}
		data, _ := mem.Read(400, 9)
		want := []byte{byte(statKindFile), 'a', 0, byte(statKindFile), 'b', 0, byte(statKindFile), 'c', 0}
		if !bytes.Equal(data, want) {
			t.Errorf("got %q", data)
		}
	})

	t.Run("23. handle gone from map before call", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		env.mu.Lock()
		f := env.handles[h].(*os.File)
		delete(env.handles, h)
		env.mu.Unlock()
		f.Close()
		readDir(t, []uint64{200, h, 400, 10})
		buf, _ := mem.Read(200, 8)
		if got := binary.LittleEndian.Uint32(buf[4:8]); got != wasiEBADF {
			t.Errorf("invalid handle errno = %d, want %d", got, wasiEBADF)
		}
	})

	t.Run("24. interleaved dir_read and path_stat", func(t *testing.T) {
		h, path := setupDir(t, []string{"a"})
		readDir(t, []uint64{200, h, 400, 2})

		mem.Write(600, []byte(path))
		if !mem.Write(300, make([]byte, 24)) {
			t.Fatal("failed to clear path_stat overlapped header")
		}
		env.sys_path_stat(context.Background(), mod, []uint64{300, 600, uint64(len(path)), 0, 700, 112})
		runOp(t, 300)

		buf, _ := mem.Read(300, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 0 {
			t.Error("stat failed after dir_read")
		}
	})

	t.Run("25. leftovers drainage at the end", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abc"})
		readDir(t, []uint64{200, h, 400, 2})
		readDir(t, []uint64{300, h, 500, 10})

		br, _ := mem.Read(300, 24)
		n := binary.LittleEndian.Uint64(br[16:24])
		if n != 3 {
			t.Errorf("expected 3 remaining bytes, got %d", n)
		}
		data, _ := mem.Read(500, 3)
		if string(data) != "bc\x00" {
			t.Errorf("got %q", data)
		}
	})
}
