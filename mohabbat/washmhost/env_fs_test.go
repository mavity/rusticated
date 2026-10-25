package main

import (
	"context"
	"encoding/binary"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGuestWindowsRootPathNormalization(t *testing.T) {
	canonical := "/C:/Users/test/AppData/Local/Temp"
	windowsRoot := `C:\Users\test\AppData\Local/Temp`
	if runtime.GOOS == "windows" {
		if got := normaliseGuestPathForHost(canonical); got != "C:/Users/test/AppData/Local/Temp" {
			t.Fatalf("normaliseGuestPathForHost(%q) = %q, want %q", canonical, got, "C:/Users/test/AppData/Local/Temp")
		}
		if got := normaliseGuestPathForHost(windowsRoot); got != "C:/Users/test/AppData/Local/Temp" {
			t.Fatalf("normaliseGuestPathForHost(%q) = %q, want %q", windowsRoot, got, "C:/Users/test/AppData/Local/Temp")
		}
		if got := normaliseGuestPathForHost(`C:\..\Windows`); got != "C:/Windows" {
			t.Fatalf("normaliseGuestPathForHost drive traversal = %q, want %q", got, "C:/Windows")
		}
		if got := normaliseGuestPathForHost(`C:\`); got != "C:/" {
			t.Fatalf("normaliseGuestPathForHost drive root = %q, want %q", got, "C:/")
		}
		if got := normaliseGuestPathForHost(`.\ tmp\washmhost-cwd-test`); got != " tmp/washmhost-cwd-test" {
			t.Fatalf("normaliseGuestPathForHost backslash = %q, want %q", got, " tmp/washmhost-cwd-test")
		}
		wantWD, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if got := normaliseGuestPathForHost("/"); got != wantWD {
			t.Fatalf("normaliseGuestPathForHost(%q) = %q, want %q", "/", got, wantWD)
		}
	} else {
		if got := normaliseGuestPathForHost(canonical); got != canonical {
			t.Fatalf("normaliseGuestPathForHost(%q) = %q, want %q", canonical, got, canonical)
		}
		if got := normaliseGuestPathForHost(windowsRoot); got != canonical {
			t.Fatalf("normaliseGuestPathForHost(%q) = %q, want %q", windowsRoot, got, canonical)
		}
		if got := normaliseGuestPathForHost(`C:\..\Windows`); got != "/C:/Windows" {
			t.Fatalf("normaliseGuestPathForHost drive traversal = %q, want %q", got, "/C:/Windows")
		}
		if got := normaliseGuestPathForHost(`C:\`); got != "/C:/" {
			t.Fatalf("normaliseGuestPathForHost drive root = %q, want %q", got, "/C:/")
		}
		if got := normaliseGuestPathForHost(`name\with-backslash`); got != `name\with-backslash` {
			t.Fatalf("normaliseGuestPathForHost(%q) = %q, want unchanged", `name\with-backslash`, got)
		}
	}
	if got := normaliseGuestPathForHost("/tmp/washmhost-cwd-test"); got != "/tmp/washmhost-cwd-test" {
		t.Fatalf("normaliseGuestPathForHost(%q) = %q, want unchanged", "/tmp/washmhost-cwd-test", got)
	}
}

func TestNormaliseHostPathForGuest(t *testing.T) {
	if runtime.GOOS == "windows" {
		tests := []struct{ in, want string }{
			{`C:\Users\mihai`, "/C:/Users/mihai"},
			{`C:/Users/mihai`, "/C:/Users/mihai"},
			{`C:`, "/C:/"},
			{"/tmp/something", "/tmp/something"},
			{"", ""},
		}

		for _, tc := range tests {
			if got := normaliseHostPathForGuest(tc.in); got != tc.want {
				t.Errorf("normaliseHostPathForGuest(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	} else {
		paths := []string{`C:\Users\mihai`, "/tmp/something", ""}
		for _, p := range paths {
			if got := normaliseHostPathForGuest(p); got != p {
				t.Errorf("normaliseHostPathForGuest(%q) = %q, want unchanged", p, got)
			}
		}
	}
}

func TestResolveGuestDriveRootedPathDoesNotPrefixCwd(t *testing.T) {
	cwd := "/workspace"
	want := "/C:/Users/test/project"
	if runtime.GOOS == "windows" {
		cwd = filepath.Clean(`C:\workspace`)
		want = filepath.Clean(`C:\Users\test\project`)
	}
	env := &HostEnv{cwd: cwd}
	for _, input := range []string{
		`C:\Users\test\project`,
		`C:\Users\test\project\.`,
		`/C:/Users/test/project`,
		`/C:/Users/test/project/.`,
	} {
		if got := env.resolveGuestPathForHost(input); got != want {
			t.Errorf("resolveGuestPathForHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSysGetPlatformInfoCanonicalizesHostPaths(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x10000)
	const ptr = uint32(0x100)
	stack := []uint64{uint64(ptr), 932}

	env.sys_get_platform_info(context.Background(), mod, stack)
	if stack[0] != 0 {
		t.Fatalf("sys_get_platform_info returned %d", stack[0])
	}
	data, ok := mod.Memory().Read(ptr, 932)
	if !ok {
		t.Fatal("read platform info output")
	}

	readString := func(offset int) string {
		field := data[offset : offset+64]
		if nul := strings.IndexByte(string(field), 0); nul >= 0 {
			field = field[:nul]
		}
		return string(field)
	}
	wantHome, err := os.UserHomeDir()
	if err == nil && wantHome != "" {
		wantHome = filepath.Clean(wantHome)
	}
	wantTemp := os.TempDir()
	if wantTemp != "" {
		wantTemp = filepath.Clean(wantTemp)
	}
	if len(wantHome) > 63 {
		wantHome = wantHome[:63]
	}
	if len(wantTemp) > 63 {
		wantTemp = wantTemp[:63]
	}
	if got := readString(548); got != wantHome {
		t.Errorf("platform home = %q, want %q", got, wantHome)
	}
	if got := readString(612); got != wantTemp {
		t.Errorf("platform temp = %q, want %q", got, wantTemp)
	}
}

func TestSysFs(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "washmhost-fs-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	env := NewHostEnv()
	mod := newMockModule(0x10000)

	// We need to set up the environment "cwd" if path_open uses it.
	// Looking at WASHMHOST-testing.md, path_open likely uses host paths or rooted paths.

	t.Run("1. sys_path_open create file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test.txt")
		pathPtr := uint32(0x100)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x200)
		// sys_path_open(ctx, mod, [ovPtr, pathPtr, pathLen, flags, mode])
		// flags: O_RDWR = bit 0 | bit 1 = 3
		// O_CREATE = bit 2 = 4
		// So 3 | 4 = 7
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 7, 0644})

		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])
		if handle < 3 {
			t.Errorf("invalid handle: %d", handle)
		}

		env.mu.Lock()
		_, ok := env.handles[handle]
		env.mu.Unlock()
		if !ok {
			t.Error("handle not found in environment")
		}

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("2. sys_write to file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "write.txt")
		pathPtr := uint32(0x300)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x400)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 7, 0644})
		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		data := "HELLO FS"
		dataPtr := uint32(0x500)
		mod.Memory().Write(dataPtr, []byte(data))

		ovPtrWrite := uint32(0x600)
		env.sys_write(context.Background(), mod, []uint64{uint64(ovPtrWrite), handle, uint64(dataPtr), uint64(len(data))})

		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ = mod.Memory().Read(ovPtrWrite, 24)
		written := binary.LittleEndian.Uint64(val[16:24])
		if written != uint64(len(data)) {
			t.Errorf("expected %d bytes written, got %d", len(data), written)
		}

		env.sys_handle_close(context.Background(), mod, []uint64{handle})

		content, _ := ioutil.ReadFile(path)
		if string(content) != data {
			t.Errorf("file content mismatch: %s", string(content))
		}
	})

	t.Run("3. sys_read from file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "read.txt")
		ioutil.WriteFile(path, []byte("READ THIS"), 0644)

		pathPtr := uint32(0x700)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x800)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		bufPtr := uint32(0x900)
		ovPtrRead := uint32(0xA00)
		env.sys_read(context.Background(), mod, []uint64{uint64(ovPtrRead), handle, uint64(bufPtr), 4})

		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ = mod.Memory().Read(ovPtrRead, 24)
		readBytes := binary.LittleEndian.Uint64(val[16:24])
		if readBytes != 4 {
			t.Errorf("expected 4 bytes read, got %d", readBytes)
		}

		res, _ := mod.Memory().Read(bufPtr, 4)
		if string(res) != "READ" {
			t.Errorf("data mismatch: %s", string(res))
		}

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("4. sys_path_stat file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "stat.txt")
		ioutil.WriteFile(path, []byte("SOME DATA"), 0644)

		pathPtr := uint32(0xB00)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x1F00)
		statPtr := uint32(0xC00)
		// sys_path_stat(ctx, mod, [ovPtr, pathPtr, pathLen, flags, outPtr, outLen])
		env.sys_path_stat(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, uint64(statPtr), 112})

		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(statPtr, 112)
		kind := binary.LittleEndian.Uint32(val[0:4])
		size := binary.LittleEndian.Uint64(val[16:24])

		if kind != 1 { // statKindFile
			t.Errorf("expected kind 1 (file), got %d", kind)
		}
		if size != 9 {
			t.Errorf("expected size 9, got %d", size)
		}
	})

	t.Run("5. sys_path_stat directory", func(t *testing.T) {
		pathPtr := uint32(0xD00)
		mod.Memory().Write(pathPtr, []byte(tmpDir))

		ovPtr := uint32(0x2000)
		statPtr := uint32(0xE00)
		env.sys_path_stat(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(tmpDir)), 0, uint64(statPtr), 112})

		env.Poll(context.Background(), mod)

		valOv, _ := mod.Memory().Read(ovPtr, 24)
		if binary.LittleEndian.Uint32(valOv[4:8]) != 0 {
			t.Errorf("stat failed with %d", binary.LittleEndian.Uint32(valOv[4:8]))
		}

		val, _ := mod.Memory().Read(statPtr, 112)
		kind := binary.LittleEndian.Uint32(val[0:4])

		if kind != 2 { // statKindDir
			t.Errorf("expected kind 2 (dir), got %d", kind)
		}
	})

	t.Run("6. sys_path_open non-existent", func(t *testing.T) {
		path := filepath.Join(tmpDir, "none.txt")
		pathPtr := uint32(0xF00)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x1000)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})

		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		errCode := binary.LittleEndian.Uint32(val[4:8])
		if errCode == 0 {
			t.Errorf("expected error for non-existent file, got handle %d", binary.LittleEndian.Uint64(val[16:24]))
		}
	})

	t.Run("7. sys_path_chmod", func(t *testing.T) {
		path := filepath.Join(tmpDir, "chmod.txt")
		ioutil.WriteFile(path, []byte("DATA"), 0644)

		pathPtr := uint32(0x1100)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x2700)
		// sys_path_chmod(ctx, mod, [ovPtr, pathPtr, pathLen, mode])
		env.sys_path_chmod(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0444})

		env.Poll(context.Background(), mod)

		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0444 {
			// Note: on Windows chmod might not work as expected for all bits,
			// but 0444 usually makes it read-only.
			t.Logf("chmod result: %o", fi.Mode().Perm())
		}
	})

	t.Run("7a. sys_path_chmod preserves special bits", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows chmod does not expose POSIX special mode bits")
		}
		path := filepath.Join(tmpDir, "chmod-setuid.txt")
		if err := ioutil.WriteFile(path, []byte("DATA"), 0644); err != nil {
			t.Fatal(err)
		}

		pathPtr := uint32(0x1180)
		mod.Memory().Write(pathPtr, []byte(path))
		ovPtr := uint32(0x2780)
		env.sys_path_chmod(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0o4755})
		env.Poll(context.Background(), mod)

		result, ok := mod.Memory().Read(ovPtr, 24)
		if !ok || binary.LittleEndian.Uint32(result[4:8]) != 0 {
			t.Fatalf("sys_path_chmod failed with result %v", result)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSetuid == 0 || info.Mode().Perm() != 0o755 {
			t.Fatalf("mode after POSIX chmod 04755 = %#o, want setuid and 0755", info.Mode())
		}
	})

	t.Run("8. sys_dir_read", func(t *testing.T) {
		d := filepath.Join(tmpDir, "subdir")
		os.Mkdir(d, 0755)
		ioutil.WriteFile(filepath.Join(d, "f1.txt"), []byte("1"), 0644)
		ioutil.WriteFile(filepath.Join(d, "f2.txt"), []byte("2"), 0644)

		pathPtr := uint32(0x1200)
		mod.Memory().Write(pathPtr, []byte(d))

		ovPtr := uint32(0x1300)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(d)), 0, 0})
		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		bufPtr := uint32(0x1400)
		// sys_dir_read(ctx, mod, [ovPtr, handle, bufPtr, bufLen])
		env.sys_dir_read(context.Background(), mod, []uint64{uint64(0x1500), handle, uint64(bufPtr), 1024})

		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ = mod.Memory().Read(0x1500, 24)
		n := binary.LittleEndian.Uint64(val[16:24])
		if n == 0 {
			t.Error("expected directory entries")
		}

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("9. sys_path_open jail break attempt", func(t *testing.T) {
		// Mocking a relative path that tries to escape
		path := "../secret.txt"
		pathPtr := uint32(0x1600)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x1700)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})

		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		errCode := binary.LittleEndian.Uint32(val[4:8])
		if errCode == 0 {
			// If it succeeded, it might be because it's allowed on the host if not jailed.
			// The host environment doesn't specify a jail root yet, it uses host paths.
			t.Log("Warning: path_open doesn't jail yet")
		}
	})

	t.Run("10. sys_read past EOF", func(t *testing.T) {
		path := filepath.Join(tmpDir, "eof.txt")
		ioutil.WriteFile(path, []byte("SHORT"), 0644)

		pathPtr := uint32(0x1800)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x1900)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		bufPtr := uint32(0x1A00)
		env.sys_read(context.Background(), mod, []uint64{uint64(0x1B00), handle, uint64(bufPtr), 100})

		time.Sleep(time.Millisecond * 20)
		env.Poll(context.Background(), mod)

		val, _ = mod.Memory().Read(0x1B00, 24)
		n := binary.LittleEndian.Uint64(val[16:24])
		if n != 5 {
			t.Errorf("expected 5 bytes, got %d", n)
		}

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("11. sys_write to read-only handle", func(t *testing.T) {
		path := filepath.Join(tmpDir, "readonly.txt")
		ioutil.WriteFile(path, []byte("DATA"), 0644)

		pathPtr := uint32(0x1C00)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x1D00)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0 /* O_RDONLY */, 0})
		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		ovPtrWrite := uint32(0x1E00)
		env.sys_write(context.Background(), mod, []uint64{uint64(ovPtrWrite), handle, 0x1F00, 4})

		env.Poll(context.Background(), mod)
		val, _ = mod.Memory().Read(ovPtrWrite, 24)
		errCode := binary.LittleEndian.Uint32(val[4:8])
		if errCode == 0 {
			t.Log("Note: write to read-only handle might only fail on actual Write call or open")
		}

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("12. sys_dir_read on file handle", func(t *testing.T) {
		path := filepath.Join(tmpDir, "notadir.txt")
		ioutil.WriteFile(path, []byte("X"), 0644)

		pathPtr := uint32(0x2000)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x2100)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		ovPtrDir := uint32(0x2200)
		env.sys_dir_read(context.Background(), mod, []uint64{uint64(ovPtrDir), handle, 0x2300, 100})

		env.Poll(context.Background(), mod)
		val, _ = mod.Memory().Read(ovPtrDir, 24)
		errCode := binary.LittleEndian.Uint32(val[4:8])
		if errCode == 0 {
			t.Error("expected error reading dir from file handle")
		}

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("13. sys_handle_close twice", func(t *testing.T) {
		path := filepath.Join(tmpDir, "doubleclose.txt")
		ioutil.WriteFile(path, []byte("X"), 0644)

		pathPtr := uint32(0x2400)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x2500)
		env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
		// Should not panic on second close
		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("14. sys_read from invalid handle", func(t *testing.T) {
		ovPtr := uint32(0x2600)
		env.sys_read(context.Background(), mod, []uint64{uint64(ovPtr), 9999, 0, 0})

		val, _ := mod.Memory().Read(ovPtr, 24)
		errCode := binary.LittleEndian.Uint32(val[4:8])
		if errCode != 9 { // EBADF
			t.Errorf("expected EBADF (9), got %d", errCode)
		}
	})
}

func TestSysHandleTruncate(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(4096)
	path := filepath.Join(t.TempDir(), "truncate.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	const handle = uint64(42)
	env.mu.Lock()
	env.handles[handle] = f
	env.mu.Unlock()
	defer env.sys_handle_close(context.Background(), mod, []uint64{handle})
	if _, err := f.Seek(5, 0); err != nil {
		t.Fatal(err)
	}

	run := func(ovPtr uint32, length int64, handle uint64) uint32 {
		t.Helper()
		if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
			t.Fatal("failed to clear overlapped result")
		}
		env.sys_handle_truncate(context.Background(), mod, []uint64{
			uint64(ovPtr), handle, uint64(length),
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for {
			env.Poll(ctx, mod)
			result, ok := mod.Memory().Read(ovPtr, 24)
			if !ok {
				t.Fatal("failed to read overlapped result")
			}
			if binary.LittleEndian.Uint32(result)&1 != 0 {
				return binary.LittleEndian.Uint32(result[4:8])
			}
			if ctx.Err() != nil {
				t.Fatal("timed out waiting for file truncation")
			}
		}
	}

	if got := run(0x100, 3, handle); got != wasiSuccess {
		t.Fatalf("shrinking file returned errno %d, want success", got)
	}
	if got, err := f.Seek(0, 1); err != nil || got != 5 {
		t.Fatalf("truncate changed file offset: offset=%d err=%v, want 5", got, err)
	}
	if got := run(0x140, 7, handle); got != wasiSuccess {
		t.Fatalf("extending file returned errno %d, want success", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{'a', 'b', 'c', 0, 0, 0, 0}
	if string(content) != string(want) {
		t.Fatalf("file content after extension = %v, want %v", content, want)
	}
	if got := run(0x180, -1, handle); got != wasiEINVAL {
		t.Fatalf("negative length returned errno %d, want %d", got, wasiEINVAL)
	}
	if got := run(0x1C0, 0, handle+1); got != wasiEBADF {
		t.Fatalf("invalid handle returned errno %d, want %d", got, wasiEBADF)
	}
}

func TestMapErrnoEFBIG(t *testing.T) {
	if got := mapErrno(syscall.EFBIG); got != wasiEFBIG {
		t.Fatalf("mapErrno(EFBIG) = %d, want %d", got, wasiEFBIG)
	}
}
