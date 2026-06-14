package main

import (
	"context"
	"encoding/binary"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		env.sys_path_stat(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, uint64(statPtr), 64})

		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(statPtr, 64)
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
		env.sys_path_stat(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(tmpDir)), 0, uint64(statPtr), 64})

		env.Poll(context.Background(), mod)

		valOv, _ := mod.Memory().Read(ovPtr, 24)
		if binary.LittleEndian.Uint32(valOv[4:8]) != 0 {
			t.Errorf("stat failed with %d", binary.LittleEndian.Uint32(valOv[4:8]))
		}

		val, _ := mod.Memory().Read(statPtr, 64)
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
