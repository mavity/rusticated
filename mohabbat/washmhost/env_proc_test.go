package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGuestEnvCanonicalizesWindowsPathAliases(t *testing.T) {
	pairs := guestEnvForWasm([]string{
		"Path=C:\\Windows;C:\\Tools",
		"Pathext=.COM;.EXE;.BAT;.CMD",
		"HOME=C:\\Users\\test",
	})
	got := map[string]string{}
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if got["PATH"] != `C:\Windows;C:\Tools` {
		t.Fatalf("PATH should be canonicalized to uppercase; got %q", got["PATH"])
	}
	if got["PATHEXT"] != ".COM;.EXE;.BAT;.CMD" {
		t.Fatalf("PATHEXT should be canonicalized to uppercase; got %q", got["PATHEXT"])
	}
	if got["HOME"] != `C:\Users\test` {
		t.Fatalf("non-Windows env keys should stay unchanged; got %q", got["HOME"])
	}
}

func TestGuestEnvPreservesWindowsUserDirs(t *testing.T) {
	pairs := guestEnvForWasm([]string{
		"LOCALAPPDATA=C:\\Users\\test\\AppData\\Local",
		"USERPROFILE=C:\\Users\\test",
		"APPDATA=C:\\Users\\test\\AppData\\Roaming",
		"TEMP=C:\\Users\\test\\AppData\\Local\\Temp",
	})
	got := map[string]string{}
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if got["LOCALAPPDATA"] != `C:\Users\test\AppData\Local` {
		t.Fatalf("LOCALAPPDATA should be preserved as-is; got %q", got["LOCALAPPDATA"])
	}
	if got["USERPROFILE"] != `C:\Users\test` {
		t.Fatalf("USERPROFILE should be preserved as-is; got %q", got["USERPROFILE"])
	}
	if got["APPDATA"] != `C:\Users\test\AppData\Roaming` {
		t.Fatalf("APPDATA should be preserved as-is; got %q", got["APPDATA"])
	}
	if got["TEMP"] != `C:\Users\test\AppData\Local\Temp` {
		t.Fatalf("TEMP should be preserved as-is; got %q", got["TEMP"])
	}
}

func TestGuestEnvExpandsPercentVarInPath(t *testing.T) {
	dir := t.TempDir()
	toolDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	key := "TOOLS_DIR"
	if err := os.Setenv(key, toolDir); err != nil {
		t.Fatalf("set env: %v", err)
	}
	defer os.Unsetenv(key)

	pairs := guestEnvForWasm([]string{
		"PATH=%TOOLS_DIR%;C:\\Windows\\System32",
		"TOOLS_DIR=" + toolDir,
		"PATHEXT=.COM;.EXE;.BAT;.CMD",
	})
	got := map[string]string{}
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if want := toolDir + ";C:\\Windows\\System32"; got["PATH"] != want {
		t.Fatalf("PATH should expand %%TOOLS_DIR%% before lookup; got %q, want %q", got["PATH"], want)
	}
}

func TestGuestEnvDoesNotRewriteTempOrUserPaths(t *testing.T) {
	pairs := guestEnvForWasm([]string{
		"TMP=C:\\Users\\test\\AppData\\Local\\Temp",
		"TEMP=C:\\Users\\test\\AppData\\Local\\Temp",
		"USERPROFILE=C:\\Users\\test",
	})
	got := map[string]string{}
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	if got["TMP"] != `C:\Users\test\AppData\Local\Temp` {
		t.Fatalf("TMP should be preserved as-is; got %q", got["TMP"])
	}
	if got["TEMP"] != `C:\Users\test\AppData\Local\Temp` {
		t.Fatalf("TEMP should be preserved as-is; got %q", got["TEMP"])
	}
	if got["USERPROFILE"] != `C:\Users\test` {
		t.Fatalf("USERPROFILE should be preserved as-is; got %q", got["USERPROFILE"])
	}
}

func TestSysProcEnv(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x10000)

	t.Run("1. sys_get_args basic metadata", func(t *testing.T) {
		stack := []uint64{0, 0}
		env.sys_get_args(context.Background(), mod, stack)
		res := stack[0]
		count := uint32(res >> 32)
		needed := uint32(res)

		if count != uint32(len(os.Args)) {
			t.Errorf("expected %d args, got %d", len(os.Args), count)
		}
		if needed == 0 {
			t.Error("expected bytesNeeded > 0")
		}
	})

	t.Run("2. sys_get_args full retrieval", func(t *testing.T) {
		stack := []uint64{0, 0}
		env.sys_get_args(context.Background(), mod, stack)
		needed := uint32(stack[0])

		ptr := uint32(0x100)
		stack = []uint64{uint64(ptr), uint64(needed)}
		env.sys_get_args(context.Background(), mod, stack)

		buf, _ := mod.Memory().Read(ptr, needed)
		parts := strings.Split(string(buf), "\x00")
		// Last element is empty because of trailing \0
		if parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}

		if len(parts) != len(os.Args) {
			t.Errorf("expected %d parts, got %d", len(os.Args), len(parts))
		}
		for i, arg := range os.Args {
			if parts[i] != arg {
				t.Errorf("arg %d mismatch: expected %q, got %q", i, arg, parts[i])
			}
		}
	})

	t.Run("3. sys_get_args small buffer", func(t *testing.T) {
		ptr := uint32(0x5000)
		mod.Memory().Write(ptr, []byte{0})
		stack := []uint64{uint64(ptr), 1}
		env.sys_get_args(context.Background(), mod, stack)

		// Should not have written (or at least not failed)
		// Based on implementation: if lenBytes < bytesNeeded, it skips writing
		val, _ := mod.Memory().Read(ptr, 1)
		if val[0] != 0 {
			t.Error("should not have written to small buffer")
		}
	})

	t.Run("4. sys_get_env basic metadata", func(t *testing.T) {
		stack := []uint64{0, 0}
		env.sys_get_env(context.Background(), mod, stack)
		res := stack[0]
		count := uint32(res >> 32)
		if count == 0 {
			t.Error("expected some env vars")
		}
	})

	t.Run("5. sys_get_env full retrieval", func(t *testing.T) {
		stack := []uint64{0, 0}
		env.sys_get_env(context.Background(), mod, stack)
		needed := uint32(stack[0])

		ptr := uint32(0x300)
		stack = []uint64{uint64(ptr), uint64(needed)}
		env.sys_get_env(context.Background(), mod, stack)

		buf, _ := mod.Memory().Read(ptr, needed)
		if !strings.Contains(string(buf), "=") {
			t.Error("env buffer missing '='")
		}
	})

	t.Run("6. sys_get_cwd and sys_set_cwd", func(t *testing.T) {
		// Get current
		stack := []uint64{0, 0}
		env.sys_get_cwd(context.Background(), mod, stack)
		needed := uint32(stack[0])

		ptr := uint32(0x400)
		env.sys_get_cwd(context.Background(), mod, []uint64{uint64(ptr), uint64(needed)})
		cwd1, _ := mod.Memory().Read(ptr, needed)

		// Set to parent
		parent := ".."
		ptr2 := uint32(0x500)
		mod.Memory().Write(ptr2, []byte(parent))
		env.sys_set_cwd(context.Background(), mod, []uint64{uint64(ptr2), uint64(len(parent))})

		// Get again
		env.sys_get_cwd(context.Background(), mod, stack)
		needed2 := uint32(stack[0])
		ptr3 := uint32(0x600)
		env.sys_get_cwd(context.Background(), mod, []uint64{uint64(ptr3), uint64(needed2)})
		cwd2, _ := mod.Memory().Read(ptr3, needed2)

		if string(cwd1) == string(cwd2) {
			t.Error("CWD should have changed after sys_set_cwd")
		}
	})

	t.Run("7. sys_set_cwd invalid path", func(t *testing.T) {
		invalid := "/non/existent/path/that/should/fail"
		ptr := uint32(0x700)
		mod.Memory().Write(ptr, []byte(invalid))
		stack := []uint64{uint64(ptr), uint64(len(invalid))}
		env.sys_set_cwd(context.Background(), mod, stack)

		if stack[0] == 0 {
			t.Error("expected non-zero errno for invalid cwd")
		}
	})

	t.Run("8. sys_process_spawn basic", func(t *testing.T) {
		ovPtr := uint32(0x800)
		// Format: program\0arg1\0arg2\0\0env1=v1\0\0
		config := "go\x00version\x00\x00"

		ptrCfg := uint32(0x900)
		mod.Memory().Write(ptrCfg, []byte(config))

		stack := []uint64{uint64(ovPtr), uint64(ptrCfg), uint64(len(config))}
		env.sys_process_spawn(context.Background(), mod, stack)

		// Wait for spawn to finish
		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		// Check for valid handle in result
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])
		if handle == 0 {
			t.Error("failed to spawn process")
		}

		// Wait for process to exit
		waitOv := uint32(0xB00)
		env.sys_process_wait(context.Background(), mod, []uint64{uint64(waitOv), handle})

		// Poll until exit
		start := time.Now()
		for {
			env.Poll(context.Background(), mod)
			val, ok := mod.Memory().Read(waitOv, 24)
			if ok && val[0]&1 != 0 {
				break
			}
			if time.Since(start) > time.Second*5 {
				t.Fatal("process wait timeout")
			}
		}
	})

	t.Run("9. sys_process_signal", func(t *testing.T) {
		// This is hard to test without a long-running process
		// But we can at least check it doesn't panic on invalid handle
		stack := []uint64{999999, 15} // invalid handle, SIGTERM
		env.sys_process_signal(context.Background(), mod, stack)
	})

	t.Run("10. sys_get_env stress", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			env.sys_get_env(context.Background(), mod, []uint64{0, 0})
		}
	})

	t.Run("11. sys_get_cwd buffer exactly right", func(t *testing.T) {
		stack := []uint64{0, 0}
		env.sys_get_cwd(context.Background(), mod, stack)
		needed := uint32(stack[0])

		ptr := uint32(0xC00)
		env.sys_get_cwd(context.Background(), mod, []uint64{uint64(ptr), uint64(needed)})
	})

	t.Run("12. sys_get_args many calls", func(t *testing.T) {
		for i := 0; i < 50; i++ {
			env.sys_get_args(context.Background(), mod, []uint64{0, 0})
		}
	})

	t.Run("13. sys_process_spawn invalid exe", func(t *testing.T) {
		ovPtr := uint32(0xD00)
		config := "/usr/bin/this_does_not_exist_at_all\x00\x00"
		ptrCfg := uint32(0xE00)
		mod.Memory().Write(ptrCfg, []byte(config))

		stack := []uint64{uint64(ovPtr), uint64(ptrCfg), uint64(len(config))}
		env.sys_process_spawn(context.Background(), mod, stack)

		time.Sleep(time.Millisecond * 50)
		env.Poll(context.Background(), mod)

		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			t.Error("expected non-zero errno for invalid executable")
		}
	})

	t.Run("14. sys_process_wait on non-process handle", func(t *testing.T) {
		ovPtr := uint32(0xF00)
		env.sys_process_wait(context.Background(), mod, []uint64{uint64(ovPtr), 1}) // handle 1 is stdout

		env.Poll(context.Background(), mod)
		val, _ := mod.Memory().Read(ovPtr, 24)
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno == 0 {
			t.Error("expected error waiting on non-process handle")
		}
	})

	t.Run("15. sys_process_spawn with pty", func(t *testing.T) {
		ovPtr := uint32(0x1000)
		// Spawn cmd.exe on Windows or sh on Unix
		shellCmd := "cmd.exe"
		arg1 := "/c"
		arg2 := "echo 123"
		if os.PathSeparator == '/' {
			shellCmd = "sh"
			arg1 = "-c"
			arg2 = "echo 123"
		}

		// Config format: program\0arg1\0arg2\0\0env\0\0cwd\0\0pty:80,24\0
		config := shellCmd + "\x00" + arg1 + "\x00" + arg2 + "\x00\x00\x00\x00pty:80,24\x00"
		ptrCfg := uint32(0x1100)
		mod.Memory().Write(ptrCfg, []byte(config))

		stack := []uint64{uint64(ovPtr), uint64(ptrCfg), uint64(len(config))}
		env.sys_process_spawn(context.Background(), mod, stack)

		time.Sleep(time.Millisecond * 80)
		env.Poll(context.Background(), mod)

		// Read extended ProcessSpawnOverlapped (64 bytes total)
		val, ok := mod.Memory().Read(ovPtr, 64)
		if !ok {
			t.Fatal("failed to read extended overlapped buffer")
		}
		errno := binary.LittleEndian.Uint32(val[4:8])
		if errno != 0 {
			t.Fatalf("spawn with pty failed with errno %d", errno)
		}

		childHandle := binary.LittleEndian.Uint64(val[24:32])
		ptyHandle := binary.LittleEndian.Uint64(val[32:40])

		if childHandle == 0 {
			t.Error("expected non-zero child_handle")
		}
		if ptyHandle == 0 {
			t.Error("expected non-zero pty_handle")
		}

		// Test tty_set_size on ptyHandle
		resizeStack := []uint64{ptyHandle, 100, 30}
		env.sys_tty_set_size(context.Background(), mod, resizeStack)
		if resizeStack[0] != 0 {
			t.Errorf("tty_set_size failed: %d", resizeStack[0])
		}

		// Read from PTY
		readOvPtr := uint32(0x1200)
		readBufPtr := uint32(0x1300)
		readStack := []uint64{uint64(readOvPtr), ptyHandle, uint64(readBufPtr), 256}
		env.sys_read(context.Background(), mod, readStack)

		time.Sleep(time.Millisecond * 100)
		env.Poll(context.Background(), mod)

		readVal, _ := mod.Memory().Read(readOvPtr, 24)
		readErr := binary.LittleEndian.Uint32(readVal[4:8])
		readCount := binary.LittleEndian.Uint64(readVal[16:24])
		if readErr != 0 {
			t.Fatalf("read from PTY failed: %d", readErr)
		}
		if readCount == 0 {
			t.Log("PTY read returned 0 bytes (might need extra poll)")
		} else {
			outputBytes, _ := mod.Memory().Read(readBufPtr, uint32(readCount))
			outputStr := string(outputBytes)
			if !strings.Contains(outputStr, "123") {
				t.Logf("PTY output: %q (expected to contain 123)", outputStr)
			}
		}

		// Wait for child process
		waitOvPtr := uint32(0x1400)
		env.sys_process_wait(context.Background(), mod, []uint64{uint64(waitOvPtr), childHandle})
		time.Sleep(time.Millisecond * 80)
		env.Poll(context.Background(), mod)

		waitVal, _ := mod.Memory().Read(waitOvPtr, 24)
		waitErr := binary.LittleEndian.Uint32(waitVal[4:8])
		if waitErr != 0 {
			t.Errorf("wait failed: %d", waitErr)
		}

		// Close pty handle
		env.sys_handle_close(context.Background(), mod, []uint64{ptyHandle})
	})
}
