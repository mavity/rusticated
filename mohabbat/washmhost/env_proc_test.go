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

func TestGuestEnvCanonicalizesWindowsUserDirs(t *testing.T) {
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
	if got["LocalAppData"] != `C:\Users\test\AppData\Local` {
		t.Fatalf("LOCALAPPDATA should be canonicalized to LocalAppData; got %q", got["LocalAppData"])
	}
	if got["UserProfile"] != `C:\Users\test` {
		t.Fatalf("USERPROFILE should be canonicalized to UserProfile; got %q", got["UserProfile"])
	}
	if got["AppData"] != `C:\Users\test\AppData\Roaming` {
		t.Fatalf("APPDATA should be canonicalized to AppData; got %q", got["AppData"])
	}
	if got["Temp"] != `C:\Users\test\AppData\Local\Temp` {
		t.Fatalf("TEMP should be canonicalized to Temp; got %q", got["Temp"])
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
}
