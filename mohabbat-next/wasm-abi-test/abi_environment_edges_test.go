//go:build wasip1

package wasmabitest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

//go:wasmimport env get_args
func rusticatedABIGetArgs(buf *byte, cap uint32) uint64

//go:wasmimport env get_env
func rusticatedABIGetEnv(buf *byte, cap uint32) uint64

//go:wasmimport env get_platform_info
func rusticatedABIGetPlatformInfo(buf *byte, cap uint32) uint32

//go:wasmimport env get_time
func rusticatedABIGetTime() uint64

//go:wasmimport env timer_set
func rusticatedABITimerSet(overlapped *byte, delayMS uint32)

//go:wasmimport env cancel
func rusticatedABICancel(overlapped *byte)

const (
	abiOverlappedSize      = 24
	abiOverlappedCompleted = 1
	abiErrorCanceled       = 11
)

func TestRusticatedABIEnvironmentEdges(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, string)
	}{
		{
			name: "missing differs from empty",
			run: func(t *testing.T, key string) {
				if err := os.Unsetenv(key); err != nil {
					t.Fatalf("unset environment: %v", err)
				}
				if value, ok := os.LookupEnv(key); ok || value != "" {
					t.Fatalf("lookup missing variable = (%q, %t), want (empty, false)", value, ok)
				}
				if value := os.Getenv(key); value != "" {
					t.Fatalf("get missing variable = %q, want empty", value)
				}
			},
		},
		{
			name: "empty value remains present",
			run: func(t *testing.T, key string) {
				if err := os.Setenv(key, ""); err != nil {
					t.Fatalf("set empty environment value: %v", err)
				}
				if value, ok := os.LookupEnv(key); !ok || value != "" {
					t.Fatalf("lookup empty variable = (%q, %t), want (empty, true)", value, ok)
				}
			},
		},
		{
			name: "set and replace value",
			run: func(t *testing.T, key string) {
				for _, value := range []string{"first", "replacement"} {
					if err := os.Setenv(key, value); err != nil {
						t.Fatalf("set %q: %v", value, err)
					}
					if got := os.Getenv(key); got != value {
						t.Fatalf("get after setting %q = %q", value, got)
					}
				}
			},
		},
		{
			name: "equals sign belongs to value",
			run: func(t *testing.T, key string) {
				want := "left=middle=right"
				if err := os.Setenv(key, want); err != nil {
					t.Fatalf("set environment value: %v", err)
				}
				if got := os.Getenv(key); got != want {
					t.Fatalf("environment value = %q, want %q", got, want)
				}
			},
		},
		{
			name: "environ encodes the complete value",
			run: func(t *testing.T, key string) {
				const value = "one=two"
				if err := os.Setenv(key, value); err != nil {
					t.Fatalf("set environment value: %v", err)
				}
				want := key + "=" + value
				for _, entry := range os.Environ() {
					if entry == want {
						return
					}
				}
				t.Fatalf("environment list does not contain %q", want)
			},
		},
		{
			name: "environ is a snapshot",
			run: func(t *testing.T, key string) {
				if err := os.Setenv(key, "before"); err != nil {
					t.Fatalf("set initial environment value: %v", err)
				}
				snapshot := os.Environ()
				if err := os.Setenv(key, "after"); err != nil {
					t.Fatalf("replace environment value: %v", err)
				}
				foundBefore, foundAfter := false, false
				for _, entry := range snapshot {
					foundBefore = foundBefore || entry == key+"=before"
					foundAfter = foundAfter || entry == key+"=after"
				}
				if !foundBefore || foundAfter {
					t.Fatalf("environment snapshot has before=%t after=%t", foundBefore, foundAfter)
				}
			},
		},
		{
			name: "unset removes a variable",
			run: func(t *testing.T, key string) {
				if err := os.Setenv(key, "remove-me"); err != nil {
					t.Fatalf("set environment value: %v", err)
				}
				if err := os.Unsetenv(key); err != nil {
					t.Fatalf("unset environment value: %v", err)
				}
				if value, ok := os.LookupEnv(key); ok || value != "" {
					t.Fatalf("lookup after unset = (%q, %t), want (empty, false)", value, ok)
				}
			},
		},
		{
			name: "unsetting absent variable succeeds",
			run: func(t *testing.T, key string) {
				if err := os.Unsetenv(key); err != nil {
					t.Fatalf("unset absent variable: %v", err)
				}
				if _, ok := os.LookupEnv(key); ok {
					t.Fatal("absent variable became present after unset")
				}
			},
		},
		{
			name: "empty key is rejected",
			run: func(t *testing.T, _ string) {
				if err := os.Setenv("", "value"); err == nil {
					t.Fatal("Setenv with empty key succeeded")
				}
			},
		},
		{
			name: "equals in key is rejected",
			run: func(t *testing.T, key string) {
				if err := os.Setenv(key+"=invalid", "value"); err == nil {
					t.Fatal("Setenv with equals in key succeeded")
				}
			},
		},
		{
			name: "nul in key is rejected",
			run: func(t *testing.T, key string) {
				if err := os.Setenv(key+"\x00suffix", "value"); err == nil {
					t.Fatal("Setenv with NUL in key succeeded")
				}
			},
		},
		{
			name: "nul in value is rejected",
			run: func(t *testing.T, key string) {
				if err := os.Setenv(key, "prefix\x00suffix"); err == nil {
					t.Fatal("Setenv with NUL in value succeeded")
				}
			},
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := fmt.Sprintf("RUSTICATED_ABI_EDGE_%02d", i)
			restoreEnvironment(t, key)
			test.run(t, key)
		})
	}
}

func restoreEnvironment(t *testing.T, key string) {
	t.Helper()
	value, present := os.LookupEnv(key)
	t.Cleanup(func() {
		var err error
		if present {
			err = os.Setenv(key, value)
		} else {
			err = os.Unsetenv(key)
		}
		if err != nil {
			t.Errorf("restore environment variable %q: %v", key, err)
		}
	})
}

func TestRusticatedABIWorkingDirectoryEdges(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("get original working directory: %v", err)
	}
	root := t.TempDir()
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatalf("create child directory: %v", err)
	}
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatalf("create regular file: %v", err)
	}

	t.Run("relative path is based on current directory", func(t *testing.T) {
		if err := os.Chdir(root); err != nil {
			t.Fatalf("change to root: %v", err)
		}
		if err := os.Chdir("child"); err != nil {
			t.Fatalf("change to relative child: %v", err)
		}
		got, err := os.Getwd()
		if err != nil {
			t.Fatalf("get relative working directory: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(child) {
			t.Fatalf("working directory = %q, want %q", got, child)
		}
	})

	t.Run("dot-dot resolves to parent", func(t *testing.T) {
		if err := os.Chdir(child); err != nil {
			t.Fatalf("change to child: %v", err)
		}
		if err := os.Chdir(".."); err != nil {
			t.Fatalf("change to parent: %v", err)
		}
		got, err := os.Getwd()
		if err != nil {
			t.Fatalf("get parent working directory: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(root) {
			t.Fatalf("working directory = %q, want %q", got, root)
		}
	})

	t.Run("dot leaves working directory unchanged", func(t *testing.T) {
		if err := os.Chdir(root); err != nil {
			t.Fatalf("change to root: %v", err)
		}
		if err := os.Chdir("."); err != nil {
			t.Fatalf("change to dot: %v", err)
		}
		got, err := os.Getwd()
		if err != nil {
			t.Fatalf("get working directory: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(root) {
			t.Fatalf("working directory = %q, want %q", got, root)
		}
	})

	t.Run("empty path is invalid", func(t *testing.T) {
		if err := os.Chdir(""); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("change to empty path error = %v, want EINVAL", err)
		}
	})

	t.Run("missing directory reports not-exist", func(t *testing.T) {
		missing := filepath.Join(root, "missing")
		if err := os.Chdir(missing); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("change to missing directory error = %v, want not-exist", err)
		}
	})

	t.Run("regular file is rejected", func(t *testing.T) {
		if err := os.Chdir(file); err == nil {
			t.Fatal("change to regular file succeeded")
		}
	})

	t.Run("failed change preserves current directory", func(t *testing.T) {
		if err := os.Chdir(root); err != nil {
			t.Fatalf("change to root: %v", err)
		}
		if err := os.Chdir(filepath.Join(root, "absent")); err == nil {
			t.Fatal("change to missing directory succeeded")
		}
		got, err := os.Getwd()
		if err != nil {
			t.Fatalf("get working directory after failure: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(root) {
			t.Fatalf("working directory after failed change = %q, want %q", got, root)
		}
	})
}

func TestRusticatedABIPlatformInformationEdges(t *testing.T) {
	first := syscall.GetPlatformInfo()
	if first == nil {
		t.Fatal("platform information is unavailable")
	}
	if second := syscall.GetPlatformInfo(); second != first {
		t.Fatal("GetPlatformInfo did not return its cached snapshot")
	}

	refreshed := syscall.RefreshPlatformInfo()
	if refreshed == nil {
		t.Fatal("RefreshPlatformInfo returned nil")
	}
	if cached := syscall.GetPlatformInfo(); cached != refreshed {
		t.Fatal("GetPlatformInfo did not return the refreshed snapshot")
	}
	if refreshed.OSKind < 1 || refreshed.OSKind > 4 {
		t.Fatalf("OS kind = %d, want a supported platform kind", refreshed.OSKind)
	}
	if expectedName := map[uint16]string{1: "windows", 2: "linux", 3: "darwin"}[refreshed.OSKind]; expectedName != "" && refreshed.OSName != expectedName {
		t.Fatalf("OS name = %q for OS kind %d, want %q", refreshed.OSName, refreshed.OSKind, expectedName)
	}
	if !strings.HasPrefix(refreshed.BuildPlatform, refreshed.OSName+"-") {
		t.Fatalf("build platform %q is inconsistent with OS name %q", refreshed.BuildPlatform, refreshed.OSName)
	}
	if refreshed.CPUBitness != 32 && refreshed.CPUBitness != 64 {
		t.Fatalf("CPU bitness = %d, want 32 or 64", refreshed.CPUBitness)
	}
	if refreshed.OSName == "" || refreshed.WasmPlatform == "" || refreshed.RusticatedName == "" {
		t.Fatalf("platform identity fields are incomplete: OS=%q WASM=%q Rusticated=%q",
			refreshed.OSName, refreshed.WasmPlatform, refreshed.RusticatedName)
	}
	wantSeparator, wantListSeparator := byte('/'), byte(':')
	if refreshed.OSKind == 1 {
		wantSeparator, wantListSeparator = '\\', ';'
	}
	if refreshed.PathSeparator != wantSeparator || refreshed.PathListSeparator != wantListSeparator {
		t.Fatalf("path separators = (%q, %q), want (%q, %q)",
			refreshed.PathSeparator, refreshed.PathListSeparator, wantSeparator, wantListSeparator)
	}
}

func TestRusticatedABIPlatformInfoBufferProtocol(t *testing.T) {
	const sentinel = byte(0xa5)
	size := syscall.PlatformInfoSize

	for _, capacity := range []int{0, size - 1} {
		t.Run(fmt.Sprintf("short-capacity-%d", capacity), func(t *testing.T) {
			out := bytes.Repeat([]byte{sentinel}, size+1)
			if got := rusticatedABIGetPlatformInfo(&out[0], uint32(capacity)); got == 0 {
				t.Fatalf("get_platform_info with capacity %d succeeded, want a short-buffer error", capacity)
			}
			for i, value := range out {
				if value != sentinel {
					t.Fatalf("short-buffer call modified byte %d: %#x", i, value)
				}
			}
		})
	}

	t.Run("exact-capacity encodes public fields", func(t *testing.T) {
		out := make([]byte, size)
		if got := rusticatedABIGetPlatformInfo(&out[0], uint32(len(out))); got != 0 {
			t.Fatalf("get_platform_info with exact capacity returned %d", got)
		}
		info := syscall.GetPlatformInfo()
		if info == nil {
			t.Fatal("platform information is unavailable")
		}
		if got := binary.LittleEndian.Uint32(out[0:4]); got != info.Flags {
			t.Errorf("encoded flags = %#x, public field = %#x", got, info.Flags)
		}
		if got := out[4]; got != info.PathSeparator {
			t.Errorf("encoded path separator = %q, public field = %q", got, info.PathSeparator)
		}
		if got := out[5]; got != info.PathListSeparator {
			t.Errorf("encoded path-list separator = %q, public field = %q", got, info.PathListSeparator)
		}
		if got := binary.LittleEndian.Uint16(out[6:8]); got != info.OSKind {
			t.Errorf("encoded OS kind = %d, public field = %d", got, info.OSKind)
		}
		if got := abiVersion(out[8:16]); got != info.OSVersion {
			t.Errorf("encoded OS version = %v, public field = %v", got, info.OSVersion)
		}
		if got := binary.LittleEndian.Uint16(out[16:18]); got != info.CPUType {
			t.Errorf("encoded CPU type = %d, public field = %d", got, info.CPUType)
		}
		if got := out[18]; got != info.CPUBitness {
			t.Errorf("encoded CPU bitness = %d, public field = %d", got, info.CPUBitness)
		}
		if got := abiVersion(out[148:156]); got != info.WasmVersion {
			t.Errorf("encoded WASM version = %v, public field = %v", got, info.WasmVersion)
		}
		if got := abiVersion(out[284:292]); got != info.RusticatedVersion {
			t.Errorf("encoded Rusticated version = %v, public field = %v", got, info.RusticatedVersion)
		}
		for _, field := range []struct {
			name string
			data []byte
			want string
		}{
			{"OS name", out[20:84], info.OSName},
			{"WASM platform", out[84:148], info.WasmPlatform},
			{"WASM version", out[156:220], info.WasmVersionStr},
			{"Rusticated name", out[220:284], info.RusticatedName},
			{"Rusticated version", out[292:356], info.RusticatedVersionStr},
			{"build version", out[356:420], info.BuildVersion},
			{"build platform", out[484:548], info.BuildPlatform},
			{"home directory", out[548:612], info.UserHomeDir},
			{"temporary directory", out[612:676], info.TempDir},
			{"locale", out[940:1004], info.Locale},
			{"time zone", out[1004:1068], info.TimeZone},
			{"keyboard layout", out[1068:1196], info.KeyboardLayoutID},
			{"appearance", out[1196:1228], info.Appearance},
			{"power source", out[1228:1260], info.PowerSource},
		} {
			if got := abiFixedString(field.data); got != field.want {
				t.Errorf("encoded %s = %q, public field = %q", field.name, got, field.want)
			}
		}
		if got := binary.LittleEndian.Uint32(out[936:940]); got != info.SystemContextVersion {
			t.Errorf("encoded system context version = %d, public field = %d", got, info.SystemContextVersion)
		}
		for i, slot := range info.MemorySlots {
			offset := 680 + i*16
			if got := out[offset]; got != slot.Kind {
				t.Errorf("memory slot %d kind = %d, public field = %d", i, got, slot.Kind)
			}
			if got := out[offset+1]; got != slot.Flags {
				t.Errorf("memory slot %d flags = %d, public field = %d", i, got, slot.Flags)
			}
			got := binary.LittleEndian.Uint64(out[offset+8 : offset+16])
			if slot.Kind == syscall.MemKindSystemTotal && got != slot.Bytes {
				t.Errorf("memory slot %d size = %d, public field = %d", i, got, slot.Bytes)
			}
		}
		var totalMemory, availableMemory uint64
		for i := 0; i < syscall.MemorySlotCount; i++ {
			offset := 680 + i*16
			switch out[offset] {
			case syscall.MemKindSystemTotal:
				totalMemory = binary.LittleEndian.Uint64(out[offset+8 : offset+16])
			case syscall.MemKindSystemAvail:
				availableMemory = binary.LittleEndian.Uint64(out[offset+8 : offset+16])
			}
		}
		if totalMemory != 0 && availableMemory > totalMemory {
			t.Errorf("available memory %d exceeds total memory %d", availableMemory, totalMemory)
		}
	})

	t.Run("oversized capacity preserves trailing bytes", func(t *testing.T) {
		out := bytes.Repeat([]byte{sentinel}, size+8)
		if got := rusticatedABIGetPlatformInfo(&out[0], uint32(len(out))); got != 0 {
			t.Fatalf("get_platform_info with oversized capacity returned %d", got)
		}
		for i, value := range out[size:] {
			if value != sentinel {
				t.Fatalf("oversized call modified trailing byte %d: %#x", i+size, value)
			}
		}
	})
}

func abiVersion(encoded []byte) [4]uint16 {
	return [4]uint16{
		binary.LittleEndian.Uint16(encoded[0:2]),
		binary.LittleEndian.Uint16(encoded[2:4]),
		binary.LittleEndian.Uint16(encoded[4:6]),
		binary.LittleEndian.Uint16(encoded[6:8]),
	}
}

func abiFixedString(encoded []byte) string {
	if nul := bytes.IndexByte(encoded, 0); nul >= 0 {
		encoded = encoded[:nul]
	}
	return string(encoded)
}

func TestRusticatedABIArgumentsBufferProtocol(t *testing.T) {
	query := rusticatedABIGetArgs(nil, 0)
	count, needed := uint32(query>>32), uint32(query)
	if count != uint32(len(os.Args)) {
		t.Fatalf("get_args query count = %d, os.Args has %d entries", count, len(os.Args))
	}

	expected := make([]byte, 0)
	for _, arg := range os.Args {
		expected = append(expected, arg...)
		expected = append(expected, 0)
	}
	if needed != uint32(len(expected)) || needed == 0 {
		t.Fatalf("get_args query size = %d, encoded os.Args size = %d", needed, len(expected))
	}

	t.Run("short capacity reports full size without writing", func(t *testing.T) {
		out := bytes.Repeat([]byte{0xa5}, int(needed))
		got := rusticatedABIGetArgs(&out[0], needed-1)
		if uint32(got>>32) != count || uint32(got) != needed {
			t.Fatalf("short-buffer result = (%d, %d), want (%d, %d)",
				uint32(got>>32), uint32(got), count, needed)
		}
		if !bytes.Equal(out, bytes.Repeat([]byte{0xa5}, len(out))) {
			t.Fatal("short-buffer call partially modified its output")
		}
	})

	t.Run("exact capacity writes nul-separated arguments", func(t *testing.T) {
		out := make([]byte, needed)
		got := rusticatedABIGetArgs(&out[0], needed)
		if uint32(got>>32) != count || uint32(got) != needed {
			t.Fatalf("exact-buffer result = (%d, %d), want (%d, %d)",
				uint32(got>>32), uint32(got), count, needed)
		}
		if !bytes.Equal(out, expected) {
			t.Fatalf("encoded arguments = %q, want %q", out, expected)
		}
	})

	t.Run("oversized capacity preserves trailing bytes", func(t *testing.T) {
		out := bytes.Repeat([]byte{0xa5}, int(needed)+8)
		got := rusticatedABIGetArgs(&out[0], uint32(len(out)))
		if uint32(got>>32) != count || uint32(got) != needed {
			t.Fatalf("oversized-buffer result = (%d, %d), want (%d, %d)",
				uint32(got>>32), uint32(got), count, needed)
		}
		if !bytes.Equal(out[:needed], expected) {
			t.Fatalf("encoded argument prefix = %q, want %q", out[:needed], expected)
		}
		for i, value := range out[needed:] {
			if value != 0xa5 {
				t.Fatalf("oversized call modified trailing byte %d: %#x", i+int(needed), value)
			}
		}
	})
}

func TestRusticatedABIEnvironmentBufferProtocol(t *testing.T) {
	query := rusticatedABIGetEnv(nil, 0)
	count, needed := uint32(query>>32), uint32(query)
	if (count == 0) != (needed == 0) {
		t.Fatalf("get_env query returned count=%d and byte size=%d", count, needed)
	}

	expected := make([]byte, int(needed))
	var outPtr *byte
	if len(expected) > 0 {
		outPtr = &expected[0]
	}
	got := rusticatedABIGetEnv(outPtr, needed)
	if uint32(got>>32) != count || uint32(got) != needed {
		t.Fatalf("exact-buffer result = (%d, %d), want (%d, %d)",
			uint32(got>>32), uint32(got), count, needed)
	}
	entries := decodeABIEnvironment(t, expected, count)
	if len(entries) > 0 {
		startupKeyFound := false
		for _, entry := range entries {
			equals := strings.IndexByte(entry, '=')
			if equals <= 0 {
				t.Fatalf("environment record %q has no non-empty key", entry)
			}
			if value, ok := os.LookupEnv(entry[:equals]); ok && value == entry[equals+1:] {
				startupKeyFound = true
			}
		}
		if !startupKeyFound {
			t.Fatal("no direct host environment entry matches a guest startup variable")
		}
	}

	shortCaseName := "one-byte-short capacity writes only complete entries"
	capacity := uint32(0)
	if needed > 0 {
		capacity = needed - 1
	} else {
		shortCaseName = "empty environment accepts zero capacity"
	}
	t.Run(shortCaseName, func(t *testing.T) {
		out := bytes.Repeat([]byte{0xa5}, int(capacity)+1)
		result := rusticatedABIGetEnv(&out[0], capacity)
		if uint32(result>>32) != count || uint32(result) != needed {
			t.Fatalf("short-buffer result = (%d, %d), want (%d, %d)",
				uint32(result>>32), uint32(result), count, needed)
		}
		buffer := out[:capacity]
		lastNul := bytes.LastIndexByte(buffer, 0)
		written := lastNul + 1
		for i, value := range out[written:] {
			if value != 0xa5 {
				t.Fatalf("short-buffer call modified byte %d beyond complete records", i+written)
			}
		}
		if written > 0 {
			// Windows may emit whole entries that fit; other hosts may write none.
			partial := decodeABIEnvironment(t, buffer[:written], uint32(bytes.Count(buffer[:written], []byte{0})))
			nextExpected := 0
			for _, entry := range partial {
				for nextExpected < len(entries) && entries[nextExpected] != entry {
					nextExpected++
				}
				if nextExpected == len(entries) {
					t.Fatalf("short-buffer record %q is not an ordered startup entry", entry)
				}
				nextExpected++
			}
		}
	})

	t.Run("oversized capacity preserves trailing bytes", func(t *testing.T) {
		out := bytes.Repeat([]byte{0xa5}, int(needed)+8)
		result := rusticatedABIGetEnv(&out[0], uint32(len(out)))
		if uint32(result>>32) != count || uint32(result) != needed {
			t.Fatalf("oversized-buffer result = (%d, %d), want (%d, %d)",
				uint32(result>>32), uint32(result), count, needed)
		}
		if !bytes.Equal(out[:needed], expected) {
			t.Fatalf("encoded environment prefix differs from exact-capacity result")
		}
		for i, value := range out[needed:] {
			if value != 0xa5 {
				t.Fatalf("oversized call modified trailing byte %d: %#x", i+int(needed), value)
			}
		}
	})
}

func decodeABIEnvironment(t *testing.T, encoded []byte, count uint32) []string {
	t.Helper()
	if len(encoded) == 0 {
		if count != 0 {
			t.Fatalf("empty environment encoding has count %d", count)
		}
		return nil
	}
	if encoded[len(encoded)-1] != 0 {
		t.Fatal("environment encoding is not NUL-terminated")
	}
	records := bytes.Split(encoded[:len(encoded)-1], []byte{0})
	if len(records) != int(count) {
		t.Fatalf("environment encoding has %d records, count says %d", len(records), count)
	}
	entries := make([]string, len(records))
	for i, record := range records {
		if len(record) == 0 || bytes.IndexByte(record, '=') <= 0 {
			t.Fatalf("environment record %d is malformed: %q", i, record)
		}
		entries[i] = string(record)
	}
	return entries
}

func TestRusticatedABICancelPendingTimerImport(t *testing.T) {
	t.Run("cancel completes pending timer and prevents late completion", func(t *testing.T) {
		overlapped := make([]byte, abiOverlappedSize)
		rusticatedABITimerSet(&overlapped[0], 80)
		if flags := binary.LittleEndian.Uint32(overlapped[0:4]); flags&abiOverlappedCompleted != 0 {
			t.Fatalf("timer_set completed before cancellation: flags=%#x error=%d",
				flags, binary.LittleEndian.Uint32(overlapped[4:8]))
		}

		rusticatedABICancel(&overlapped[0])
		if flags := binary.LittleEndian.Uint32(overlapped[0:4]); flags&abiOverlappedCompleted == 0 {
			t.Fatal("cancel did not complete the pending operation")
		}
		if got := binary.LittleEndian.Uint32(overlapped[4:8]); got != abiErrorCanceled {
			t.Fatalf("canceled operation error = %d, want ECANCELED (%d)", got, abiErrorCanceled)
		}
		cancelled := append([]byte(nil), overlapped...)

		rusticatedABICancel(&overlapped[0])
		if !bytes.Equal(overlapped, cancelled) {
			t.Fatal("repeated cancellation changed the completed operation")
		}
		time.Sleep(120 * time.Millisecond)
		if !bytes.Equal(overlapped, cancelled) {
			t.Fatal("canceled timer completed again after its original deadline")
		}
	})

	t.Run("cancel after successful completion is harmless", func(t *testing.T) {
		overlapped := make([]byte, abiOverlappedSize)
		rusticatedABITimerSet(&overlapped[0], 0)
		time.Sleep(20 * time.Millisecond)
		if flags := binary.LittleEndian.Uint32(overlapped[0:4]); flags&abiOverlappedCompleted == 0 {
			t.Fatal("zero-delay timer did not complete")
		}
		if got := binary.LittleEndian.Uint32(overlapped[4:8]); got != 0 {
			t.Fatalf("zero-delay timer error = %d, want success", got)
		}
		completed := append([]byte(nil), overlapped...)
		rusticatedABICancel(&overlapped[0])
		if !bytes.Equal(overlapped, completed) {
			t.Fatal("cancel changed an already completed successful operation")
		}
	})
}

func TestRusticatedABIRandomRequestEdges(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 7, 16, 31, 64, 127, 256, 1024} {
		t.Run(fmt.Sprintf("length-%d", size), func(t *testing.T) {
			backing := make([]byte, size+2)
			backing[0] = 0xa5
			backing[len(backing)-1] = 0x5a
			if err := syscall.RandomGet(backing[1 : len(backing)-1]); err != nil {
				t.Fatalf("random request of %d bytes: %v", size, err)
			}
			if backing[0] != 0xa5 || backing[len(backing)-1] != 0x5a {
				t.Fatalf("random request of %d bytes changed surrounding sentinels", size)
			}
		})
	}

	first, second := make([]byte, 128), make([]byte, 128)
	if err := syscall.RandomGet(first); err != nil {
		t.Fatalf("first random request: %v", err)
	}
	if err := syscall.RandomGet(second); err != nil {
		t.Fatalf("second random request: %v", err)
	}
	if string(first) == string(second) {
		t.Fatal("independent random requests returned identical 128-byte sequences")
	}
}

func TestRusticatedABIClockEdges(t *testing.T) {
	t.Run("raw import encodes Unix nanoseconds", func(t *testing.T) {
		rawTime := rusticatedABIGetTime()
		if rawTime == 0 {
			t.Fatal("get_time returned the zero timestamp")
		}
		fromImport := time.Unix(int64(rawTime/1_000_000_000), int64(rawTime%1_000_000_000))
		delta := time.Now().Sub(fromImport)
		if delta < 0 {
			delta = -delta
		}
		if delta > 2*time.Second {
			t.Fatalf("get_time value %v differs from time.Now by %s", fromImport, delta)
		}
	})

	t.Run("Gettimeofday projects microseconds", func(t *testing.T) {
		var timeval syscall.Timeval
		if err := syscall.Gettimeofday(&timeval); err != nil {
			t.Fatalf("gettimeofday: %v", err)
		}
		if timeval.Sec <= 0 || timeval.Usec < 0 || timeval.Usec >= 1_000_000 {
			t.Fatalf("gettimeofday returned invalid timestamp: %+v", timeval)
		}
		fromSyscall := time.Unix(timeval.Sec, timeval.Usec*1_000)
		now := time.Now()
		delta := now.Sub(fromSyscall)
		if delta < 0 {
			delta = -delta
		}
		if delta > 2*time.Second {
			t.Fatalf("gettimeofday timestamp %v differs from time.Now %v by %s", fromSyscall, now, delta)
		}
	})

	t.Run("time.Now advances across a sleep", func(t *testing.T) {
		first := time.Now()
		if first.IsZero() || first.UnixNano() <= 0 {
			t.Fatalf("first clock sample is not a positive wall time: %v", first)
		}
		time.Sleep(2 * time.Millisecond)
		second := time.Now()
		if second.Before(first) {
			t.Fatalf("clock moved backwards: first=%v second=%v", first, second)
		}
		if elapsed := second.Sub(first); elapsed < time.Millisecond {
			t.Fatalf("clock advanced by %s after a 2ms sleep", elapsed)
		}
		if second.Nanosecond() < 0 || second.Nanosecond() >= int(time.Second) {
			t.Fatalf("clock returned invalid nanosecond field %d", second.Nanosecond())
		}
	})
}

func TestRusticatedABITimerAndCancellationEdges(t *testing.T) {
	t.Run("zero timer fires", func(t *testing.T) {
		timer := time.NewTimer(0)
		defer timer.Stop()
		waitForTimerEvent(t, timer.C, time.Second, "zero-duration timer did not fire")
	})

	t.Run("negative timer fires", func(t *testing.T) {
		timer := time.NewTimer(-time.Millisecond)
		defer timer.Stop()
		waitForTimerEvent(t, timer.C, time.Second, "negative-duration timer did not fire")
	})

	t.Run("stopping pending timer cancels delivery", func(t *testing.T) {
		timer := time.NewTimer(80 * time.Millisecond)
		if !timer.Stop() {
			t.Fatal("Stop on a pending timer returned false")
		}
		assertNoTimerEvent(t, timer.C, 100*time.Millisecond, "stopped timer delivered a value")
	})

	t.Run("stopping fired timer reports inactive", func(t *testing.T) {
		timer := time.NewTimer(time.Millisecond)
		waitForTimerEvent(t, timer.C, time.Second, "timer did not fire")
		if timer.Stop() {
			t.Fatal("Stop after delivery returned true")
		}
	})

	t.Run("stopped timer can be reset", func(t *testing.T) {
		timer := time.NewTimer(time.Hour)
		if !timer.Stop() {
			t.Fatal("Stop on a new timer returned false")
		}
		if timer.Reset(time.Millisecond) {
			t.Fatal("Reset on a stopped timer reported it was active")
		}
		waitForTimerEvent(t, timer.C, time.Second, "reset timer did not fire")
	})

	t.Run("active timer reset replaces old deadline", func(t *testing.T) {
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		if !timer.Reset(time.Millisecond) {
			t.Fatal("Reset on an active timer reported it was stopped")
		}
		waitForTimerEvent(t, timer.C, time.Second, "reset active timer did not fire")
		assertNoTimerEvent(t, timer.C, 20*time.Millisecond, "reset timer delivered more than once")
	})

	t.Run("AfterFunc fires once", func(t *testing.T) {
		called := make(chan struct{}, 2)
		timer := time.AfterFunc(time.Millisecond, func() { called <- struct{}{} })
		defer timer.Stop()
		timeout := time.NewTimer(time.Second)
		defer timeout.Stop()
		select {
		case <-called:
		case <-timeout.C:
			t.Fatal("AfterFunc callback did not run")
		}
		assertNoSignal(t, called, 20*time.Millisecond, "AfterFunc callback ran more than once")
	})

	t.Run("stopping AfterFunc suppresses callback", func(t *testing.T) {
		called := make(chan struct{}, 1)
		timer := time.AfterFunc(80*time.Millisecond, func() { called <- struct{}{} })
		if !timer.Stop() {
			t.Fatal("Stop on pending AfterFunc returned false")
		}
		assertNoSignal(t, called, 100*time.Millisecond, "stopped AfterFunc callback ran")
	})

	t.Run("ticker stops producing values", func(t *testing.T) {
		ticker := time.NewTicker(5 * time.Millisecond)
		for i := 0; i < 2; i++ {
			waitForTimerEvent(t, ticker.C, time.Second, fmt.Sprintf("ticker did not produce tick %d", i+1))
		}
		ticker.Stop()
		assertNoTimerEvent(t, ticker.C, 25*time.Millisecond, "stopped ticker delivered another tick")
	})

	t.Run("deadline context expires", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		timeout := time.NewTimer(time.Second)
		defer timeout.Stop()
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
			}
		case <-timeout.C:
			t.Fatal("deadline context did not expire")
		}
	})

	t.Run("canceling deadline context prevents expiration", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		cancel()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error after cancellation = %v, want canceled", ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error after its deadline = %v, want canceled", ctx.Err())
		}
	})

	t.Run("time after fires", func(t *testing.T) {
		waitForTimerEvent(t, time.After(time.Millisecond), time.Second, "time.After did not fire")
	})
}

func waitForTimerEvent(t *testing.T, event <-chan time.Time, timeout time.Duration, message string) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-event:
	case <-timer.C:
		t.Fatal(message)
	}
}

func assertNoTimerEvent(t *testing.T, event <-chan time.Time, wait time.Duration, message string) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-event:
		t.Fatal(message)
	case <-timer.C:
	}
}

func assertNoSignal(t *testing.T, event <-chan struct{}, wait time.Duration, message string) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-event:
		t.Fatal(message)
	case <-timer.C:
	}
}

func TestRusticatedABIArgumentVectorEncoding(t *testing.T) {
	for i, arg := range os.Args {
		if strings.IndexByte(arg, 0) >= 0 {
			t.Errorf("argument %d contains an embedded NUL: %q", i, arg)
		}
	}
}
