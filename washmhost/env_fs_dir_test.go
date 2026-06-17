package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

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

	runOp := func() {
		select {
		case op := <-env.fileOpsQueue:
			op()
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timeout waiting for op")
		}
	}

	decodeNames := func(buf []byte) []string {
		var names []string
		start := 0
		for i, b := range buf {
			if b == 0 {
				names = append(names, string(buf[start:i]))
				start = i + 1
			}
		}
		return names
	}

	t.Run("1. happy path - read all in one go", func(t *testing.T) {
		h, _ := setupDir(t, []string{"fileA", "fileB"})
		stack := []uint64{200, h, 400, 100}
		env.sys_dir_read(context.Background(), mod, stack)
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		data, _ := mem.Read(400, uint32(n))
		names := decodeNames(data)
		sort.Strings(names)
		if len(names) != 2 || names[0] != "fileA" || names[1] != "fileB" {
			t.Errorf("got %v", names)
		}
	})

	t.Run("2. empty directory", func(t *testing.T) {
		h, _ := setupDir(t, nil)
		stack := []uint64{200, h, 400, 100}
		env.sys_dir_read(context.Background(), mod, stack)
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 0 {
			t.Errorf("expected 0 bytes, got %d", n)
		}
	})

	t.Run("3. fragmentation - small buffer reading one by one", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b"})
		res := ""
		for i := 0; i < 2; i++ {
			env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 2})
			runOp()
			buf, _ := mem.Read(200, 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			data, _ := mem.Read(400, uint32(n))
			res += string(data)
		}
		names := decodeNames([]byte(res))
		sort.Strings(names)
		if len(names) != 2 || names[0] != "a" || names[1] != "b" {
			t.Errorf("got %v", names)
		}
	})

	t.Run("4. fragmentation - 1 byte at a time stress", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abc"})
		res := ""
		for i := 0; i < 4; i++ {
			env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 1})
			runOp()
			buf, _ := mem.Read(200, 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			if n != 1 {
				t.Fatalf("step %d: expected 1 byte, got %d", i, n)
			}
			data, _ := mem.Read(400, 1)
			res += string(data)
		}
		if res != "abc\x00" {
			t.Errorf("got %q, want %q", res, "abc\x00")
		}
	})

	t.Run("5. buffer matches entry size exactly", func(t *testing.T) {
		h, _ := setupDir(t, []string{"longname"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 9})
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 9 {
			t.Errorf("expected 9, got %d", n)
		}
		data, _ := mem.Read(400, 9)
		if string(data) != "longname\x00" {
			t.Errorf("got %q", data)
		}
	})

	t.Run("6. large requested length, smaller available", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 1000})
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 2 { // "a\0"
			t.Errorf("expected 2, got %d", n)
		}
	})

	t.Run("7. EBADF - invalid handle", func(t *testing.T) {
		env.sys_dir_read(context.Background(), mod, []uint64{200, 99999, 400, 100})
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 9 {
			t.Error("EBADF error mismatch")
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

		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 100})
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 0 {
			t.Errorf("regular file should have 0 dir entries, got %d", n)
		}
	})

	t.Run("9. EINVAL - invalid pointer", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 2000000, 100})
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 22 {
			t.Error("EINVAL expected for bad pointer")
		}
	})

	t.Run("10. EINVAL - invalid length", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 2000000})
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 22 {
			t.Error("EINVAL expected for bad length")
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

		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 10})
		runOp()

		env.mu.Lock()
		_, isScan := env.handles[h].(*DirScan)
		env.mu.Unlock()
		if !isScan {
			t.Fatal("should have converted to *DirScan")
		}
	})

	t.Run("12. handle hijacked (closed mid-scan)", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b", "c"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 2})
		runOp()

		env.mu.Lock()
		scan := env.handles[h].(*DirScan)
		scan.File.Close()
		delete(env.handles, h)
		env.mu.Unlock()

		env.sys_dir_read(context.Background(), mod, []uint64{300, h, 500, 2})
		runOp()
		buf, _ := mem.Read(300, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 9 {
			t.Error("expected EBADF for hijacked handle")
		}
	})

	t.Run("13. concurrent reads same handle stress", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b", "c", "d", "e", "f", "g", "h"})
		for i := 0; i < 4; i++ {
			env.sys_dir_read(context.Background(), mod, []uint64{uint64(200 + i*30), h, uint64(400 + i*5), 2})
		}
		for i := 0; i < 4; i++ {
			runOp()
		}
		all := ""
		for i := 0; i < 4; i++ {
			buf, _ := mem.Read(uint32(200+i*30), 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			data, _ := mem.Read(uint32(400+i*5), uint32(n))
			all += string(data)
		}
		names := decodeNames([]byte(all))
		if len(names) != 4 {
			t.Errorf("got %d names, want 4", len(names))
		}
	})

	t.Run("14. large directory stress - 100 files", func(t *testing.T) {
		var entries []string
		for i := 0; i < 100; i++ {
			entries = append(entries, fmt.Sprintf("file%03d", i))
		}
		h, _ := setupDir(t, entries)

		totalNames := 0
		for {
			env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 64})
			runOp()
			buf, _ := mem.Read(200, 24)
			n := binary.LittleEndian.Uint64(buf[16:24])
			if n == 0 {
				break
			}
			data, _ := mem.Read(400, uint32(n))
			names := decodeNames(data)
			totalNames += len(names)
		}
		if totalNames != 100 {
			t.Errorf("got %d, want 100", totalNames)
		}
	})

	t.Run("15. pathological names - dots, spaces, unicode", func(t *testing.T) {
		entries := []string{".", "..", "file with space", "日本語.txt"}
		h, _ := setupDir(t, entries)

		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 500})
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		data, _ := mem.Read(400, uint32(n))
		names := decodeNames(data)
		hasSpace := false
		hasUnicode := false
		for _, nm := range names {
			if nm == "file with space" {
				hasSpace = true
			}
			if nm == "日本語.txt" {
				hasUnicode = true
			}
		}
		if !hasSpace || !hasUnicode {
			t.Errorf("got %v", names)
		}
	})

	t.Run("16. cancellation mid-read", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 10})
		env.sys_cancel(context.Background(), mod, []uint64{200})
		runOp()
	})

	t.Run("17. repeated calls after EOF", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 100})
		runOp()
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 100})
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 0 {
			t.Errorf("expected 0, got %d", n)
		}
	})

	t.Run("18. directory re-open freshness", func(t *testing.T) {
		entries := []string{"a"}
		h1, path := setupDir(t, entries)
		env.sys_dir_read(context.Background(), mod, []uint64{200, h1, 400, 100})
		runOp()

		f2, _ := os.Open(path)
		h2 := uint64(260)
		env.mu.Lock()
		env.handles[h2] = f2
		env.mu.Unlock()
		t.Cleanup(func() { f2.Close() })

		env.sys_dir_read(context.Background(), mod, []uint64{300, h2, 500, 100})
		runOp()
		buf, _ := mem.Read(300, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 2 {
			t.Error("re-opened dir should have content")
		}
	})

	t.Run("19. huge length check", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a"})
		stack := []uint64{200, h, 400, 0xFFFFFFFF}
		env.sys_dir_read(context.Background(), mod, stack)
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 22 {
			t.Error("should return EINVAL for huge length")
		}
	})

	t.Run("20. overwrite buffer verify", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abc"})
		mem.Write(400, []byte("garbagegarbagegarbage"))
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 100})
		runOp()
		data, _ := mem.Read(400, 4)
		if string(data) != "abc\x00" {
			t.Errorf("got %q, buffer not overwritten correctly", data)
		}
	})

	t.Run("21. leftover partial resume", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abcd"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 2})
		runOp()
		env.sys_dir_read(context.Background(), mod, []uint64{300, h, 500, 3})
		runOp()

		b1, _ := mem.Read(400, 2)
		b2, _ := mem.Read(500, 3)
		if string(b1)+string(b2) != "abcd\x00" {
			t.Errorf("got %q + %q", string(b1), string(b2))
		}
	})

	t.Run("22. multiple entries into one buffer", func(t *testing.T) {
		h, _ := setupDir(t, []string{"a", "b", "c"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 6})
		runOp()
		buf, _ := mem.Read(200, 24)
		n := binary.LittleEndian.Uint64(buf[16:24])
		if n != 6 {
			t.Errorf("got %d bytes, want 6", n)
		}
		data, _ := mem.Read(400, 6)
		if string(data) != "a\x00b\x00c\x00" {
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
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 10})
		runOp()
		buf, _ := mem.Read(200, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 9 {
			t.Error("should return EBADF")
		}
	})

	t.Run("24. interleaved dir_read and path_stat", func(t *testing.T) {
		h, path := setupDir(t, []string{"a"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 2})
		runOp()

		mem.Write(600, []byte(path))
		env.sys_path_stat(context.Background(), mod, []uint64{300, 600, uint64(len(path)), 0, 700, 64})
		runOp()

		buf, _ := mem.Read(300, 8)
		if binary.LittleEndian.Uint32(buf[4:8]) != 0 {
			t.Error("stat failed after dir_read")
		}
	})

	t.Run("25. leftovers drainage at the end", func(t *testing.T) {
		h, _ := setupDir(t, []string{"abc"})
		env.sys_dir_read(context.Background(), mod, []uint64{200, h, 400, 2})
		runOp()
		env.sys_dir_read(context.Background(), mod, []uint64{300, h, 500, 10})
		runOp()

		br, _ := mem.Read(300, 24)
		n := binary.LittleEndian.Uint64(br[16:24])
		if n != 2 {
			t.Errorf("expected 2 remaining bytes, got %d", n)
		}
		data, _ := mem.Read(500, 2)
		if string(data) != "c\x00" {
			t.Errorf("got %q", data)
		}
	})
}
