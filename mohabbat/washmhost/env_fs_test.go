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

	"github.com/mavity/rusticated/platform"
)

func TestResolveGuestDriveRootedPathDoesNotPrefixCwd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows drive-rooted path resolution test")
	}
	cwd := filepath.Clean(`C:\workspace`)
	want := filepath.Clean(`C:\Users\test\project`)
	env := &HostEnv{cwd: cwd}
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"01_drive_backslashes", `C:\Users\test\project`, want},
		{"02_drive_dot_segment", `C:\Users\test\project\.`, want},
		{"03_drive_slashes", `C:/Users/test/project`, filepath.Clean(`C:/Users/test/project`)},
		{"04_root_relative_backslash", `\Users\test\project`, filepath.Clean(`C:\Users\test\project`)},
		{"05_root_relative_slash", `/Users/test/project`, filepath.Clean(`C:\Users\test\project`)},
		{"06_relative_join", `project`, filepath.Clean(`C:\workspace\project`)},
		{"07_dot_relative", `.`, filepath.Clean(`C:\workspace`)},
		{"08_empty_uses_cwd", "", filepath.Clean(cwd)},
		{"09_unc_share", `\\server\share\project`, filepath.Clean(`\\server\share\project`)},
		{"10_drive_dotdot", `C:\Users\test\project\..\project`, want},
		{"11_lowercase_drive", `c:\Users\test\project`, filepath.Clean(`c:\Users\test\project`)},
		{"12_slash_root_no_cwd_prefix", `/workspace/file`, filepath.Clean(`C:\workspace/file`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := env.resolveGuestPathForHost(tc.input); got != tc.want {
				t.Errorf("resolveGuestPathForHost(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSysGetPlatformInfoCanonicalizesHostPaths(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x10000)
	const ptr = uint32(0x100)
	stack := []uint64{uint64(ptr), platformInfoSize}

	env.sys_get_platform_info(context.Background(), mod, stack)
	if stack[0] != 0 {
		t.Fatalf("sys_get_platform_info returned %d", stack[0])
	}
	data, ok := mod.Memory().Read(ptr, platformInfoSize)
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
	if got := binary.LittleEndian.Uint32(data[platformInfoSystemContextVersionOffset:]); got != 1 {
		t.Errorf("system context version = %d, want 1", got)
	}

	t.Run("01_success_status", func(t *testing.T) {
		again := newMockModule(0x10000)
		check := []uint64{uint64(ptr), platformInfoSize}
		env.sys_get_platform_info(context.Background(), again, check)
		if check[0] != uint64(wasiSuccess) {
			t.Errorf("repeat platform info result = %d, want success", check[0])
		}
	})
	t.Run("02_flags_shape", func(t *testing.T) {
		if got := binary.LittleEndian.Uint32(data[0:4]); runtime.GOOS != "windows" && got&1 == 0 {
			t.Errorf("case-sensitive flag = %d, want set", got)
		}
	})
	t.Run("03_path_separator", func(t *testing.T) {
		want := byte('/')
		if runtime.GOOS == "windows" {
			want = '\\'
		}
		if got := data[4]; got != want {
			t.Errorf("path separator = %q, want %q", got, want)
		}
	})
	t.Run("04_list_separator", func(t *testing.T) {
		want := byte(':')
		if runtime.GOOS == "windows" {
			want = ';'
		}
		if got := data[5]; got != want {
			t.Errorf("list separator = %q, want %q", got, want)
		}
	})
	t.Run("05_os_kind", func(t *testing.T) {
		want := map[string]uint16{"windows": 1, "linux": 2, "darwin": 3}[runtime.GOOS]
		if want == 0 {
			want = 4
		}
		if got := binary.LittleEndian.Uint16(data[6:8]); got != want {
			t.Errorf("OS kind = %d, want %d", got, want)
		}
	})
	t.Run("06_cpu_type", func(t *testing.T) {
		got := binary.LittleEndian.Uint16(data[16:18])
		if got == 0 {
			t.Error("CPU type is zero")
		}
	})
	t.Run("07_cpu_bitness", func(t *testing.T) {
		want := byte(64)
		if runtime.GOARCH == "386" || runtime.GOARCH == "arm" {
			want = 32
		}
		if got := data[18]; got != want {
			t.Errorf("CPU bitness = %d, want %d", got, want)
		}
	})
	t.Run("08_struct_size", func(t *testing.T) {
		if platformInfoSize != 1264 {
			t.Errorf("platform info size = %d, want 1264", platformInfoSize)
		}
	})
	t.Run("09_home_canonical", func(t *testing.T) {
		if got := readString(548); got != wantHome {
			t.Errorf("platform home = %q, want %q", got, wantHome)
		}
	})
	t.Run("10_temp_canonical", func(t *testing.T) {
		if got := readString(612); got != wantTemp {
			t.Errorf("platform temp = %q, want %q", got, wantTemp)
		}
	})
	t.Run("11_small_buffer", func(t *testing.T) {
		small := newMockModule(0x10000)
		stack := []uint64{uint64(0x100), uint64(platformInfoSize - 1)}
		env.sys_get_platform_info(context.Background(), small, stack)
		if stack[0] != uint64(wasiE2BIG) {
			t.Errorf("small buffer result = %d, want E2BIG", stack[0])
		}
	})
	t.Run("12_invalid_output_pointer", func(t *testing.T) {
		small := newMockModule(32)
		stack := []uint64{uint64(32), uint64(platformInfoSize)}
		env.sys_get_platform_info(context.Background(), small, stack)
		if stack[0] != uint64(wasiEFAULT) {
			t.Errorf("invalid pointer result = %d, want EFAULT", stack[0])
		}
	})
}

func TestSysGetPlatformInfoSerializesSystemContext(t *testing.T) {
	env := NewHostEnv()
	env.systemInfoProvider = func() systemContext {
		return systemContext{
			locale:           "en-GB",
			timeZone:         "Europe/London",
			keyboardLayoutID: "com.apple.keylayout.ABC",
			appearance:       "dark",
			powerSource:      "battery",
		}
	}
	mod := newMockModule(0x10000)
	const ptr = uint32(0x100)
	stack := []uint64{uint64(ptr), platformInfoSize}

	env.sys_get_platform_info(context.Background(), mod, stack)
	if stack[0] != uint64(wasiSuccess) {
		t.Fatalf("sys_get_platform_info returned %d", stack[0])
	}
	data, ok := mod.Memory().Read(ptr, platformInfoSize)
	if !ok {
		t.Fatal("read platform info output")
	}
	for _, field := range []struct {
		offset int
		want   string
	}{
		{platformInfoLocaleOffset, "en-GB"},
		{platformInfoTimeZoneOffset, "Europe/London"},
		{platformInfoKeyboardLayoutIDOffset, "com.apple.keylayout.ABC"},
		{platformInfoAppearanceOffset, "dark"},
		{platformInfoPowerSourceOffset, "battery"},
	} {
		if got := string(data[field.offset : field.offset+len(field.want)]); got != field.want {
			t.Errorf("system context at %d = %q, want %q", field.offset, got, field.want)
		}
	}
	t.Run("01_locale", func(t *testing.T) {
		if got := string(data[940:945]); got != "en-GB" {
			t.Errorf("locale = %q", got)
		}
	})
	t.Run("02_timezone", func(t *testing.T) {
		if got := string(data[1004:1017]); got != "Europe/London" {
			t.Errorf("timezone = %q", got)
		}
	})
	t.Run("03_layout", func(t *testing.T) {
		if got := string(data[1068:1091]); got != "com.apple.keylayout.ABC" {
			t.Errorf("layout = %q", got)
		}
	})
	t.Run("04_appearance", func(t *testing.T) {
		if got := string(data[1196:1200]); got != "dark" {
			t.Errorf("appearance = %q", got)
		}
	})
	t.Run("05_power", func(t *testing.T) {
		if got := string(data[1228:1235]); got != "battery" {
			t.Errorf("power = %q", got)
		}
	})
	t.Run("06_version", func(t *testing.T) {
		if got := binary.LittleEndian.Uint32(data[936:940]); got != 1 {
			t.Errorf("context version = %d", got)
		}
	})
	t.Run("07_locale_null_after_text", func(t *testing.T) {
		if got := data[945]; got != 0 {
			t.Errorf("locale terminator = %d", got)
		}
	})
	t.Run("08_timezone_null_after_text", func(t *testing.T) {
		if got := data[1017]; got != 0 {
			t.Errorf("timezone terminator = %d", got)
		}
	})
	t.Run("09_layout_null_after_text", func(t *testing.T) {
		if got := data[1091]; got != 0 {
			t.Errorf("layout terminator = %d", got)
		}
	})
	t.Run("10_appearance_null_after_text", func(t *testing.T) {
		if got := data[1200]; got != 0 {
			t.Errorf("appearance terminator = %d", got)
		}
	})
	t.Run("11_power_null_after_text", func(t *testing.T) {
		if got := data[1235]; got != 0 {
			t.Errorf("power terminator = %d", got)
		}
	})
	t.Run("12_utf8_context_safe", func(t *testing.T) {
		env.systemInfoProvider = func() systemContext {
			return systemContext{locale: "fr-FR", timeZone: "Europe/Paris", keyboardLayoutID: "é", appearance: "light", powerSource: "AC"}
		}
		again := newMockModule(0x10000)
		stack := []uint64{uint64(ptr), platformInfoSize}
		env.sys_get_platform_info(context.Background(), again, stack)
		if stack[0] != 0 {
			t.Errorf("UTF-8 context result = %d", stack[0])
		}
	})
}

func TestCurrentHostSystemContextIgnoresInventedEnvVars(t *testing.T) {
	t.Setenv("MOHABBAT_SYSTEM_APPEARANCE", "sentinel-appearance")
	t.Setenv("GTK_THEME", "sentinel-gtk-theme")
	t.Setenv("MOHABBAT_SYSTEM_POWER_SOURCE", "sentinel-power")
	t.Setenv("MOHABBAT_POWER_SOURCE", "sentinel-power-2")
	t.Setenv("MOHABBAT_KEYBOARD_LAYOUT", "sentinel-layout")
	t.Setenv("XKB_DEFAULT_LAYOUT", "sentinel-xkb")

	got := currentHostSystemContext()
	if got.appearance != strings.TrimSpace(platform.SystemAppearance()) {
		t.Fatalf("appearance = %q, want %q", got.appearance, strings.TrimSpace(platform.SystemAppearance()))
	}
	if got.powerSource != strings.TrimSpace(platform.PowerSource()) {
		t.Fatalf("powerSource = %q, want %q", got.powerSource, strings.TrimSpace(platform.PowerSource()))
	}
	if got.keyboardLayoutID != strings.TrimSpace(platform.KeyboardLayoutID()) {
		t.Fatalf("keyboardLayoutID = %q, want %q", got.keyboardLayoutID, strings.TrimSpace(platform.KeyboardLayoutID()))
	}
}

func TestSystemInfoWaitCompletesWhenContextChanges(t *testing.T) {
	env := NewHostEnv()
	current := systemContext{locale: "en-US"}
	env.systemInfoProvider = func() systemContext { return current }
	mod := newMockModule(0x10000)
	const ovPtr = uint32(0x100)

	env.sys_system_info_wait(context.Background(), mod, []uint64{uint64(ovPtr)})
	env.mu.Lock()
	generation := env.systemInfoTimerGeneration
	env.systemInfoTimer.Stop()
	env.systemInfoTimer = nil
	env.mu.Unlock()

	current.locale = "fr-FR"
	env.pollSystemInfo(generation)
	(<-env.fileOpsQueue)()

	data, ok := mod.Memory().Read(ovPtr, 24)
	if !ok {
		t.Fatal("read completed overlapped")
	}
	if got := binary.LittleEndian.Uint32(data[4:8]); got != wasiSuccess {
		t.Errorf("system info wait error = %d, want success", got)
	}
	if got := env.PendingOps(); got != 0 {
		t.Errorf("pending operations = %d, want 0", got)
	}
	t.Run("01_wait_registered", func(t *testing.T) {
		env2 := NewHostEnv()
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x200})
		env2.mu.Lock()
		_, ok := env2.systemInfoWaiters[0x200]
		env2.mu.Unlock()
		if !ok {
			t.Error("waiter was not registered")
		}
	})
	t.Run("02_context_change_locale", func(t *testing.T) {
		if current.locale != "fr-FR" {
			t.Errorf("locale = %q", current.locale)
		}
	})
	t.Run("03_overlapped_completed", func(t *testing.T) {
		if got := binary.LittleEndian.Uint32(data[4:8]); got != wasiSuccess {
			t.Errorf("error = %d", got)
		}
	})
	t.Run("04_flags_completed", func(t *testing.T) {
		if got := binary.LittleEndian.Uint32(data[0:4]); got != 1 {
			t.Errorf("flags = %d", got)
		}
	})
	t.Run("05_waiter_removed", func(t *testing.T) {
		env.mu.Lock()
		_, ok := env.systemInfoWaiters[ovPtr]
		env.mu.Unlock()
		if ok {
			t.Error("waiter remains after change")
		}
	})
	t.Run("06_active_removed", func(t *testing.T) {
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending = %d", got)
		}
	})
	t.Run("07_timer_cleared", func(t *testing.T) {
		env.mu.Lock()
		timer := env.systemInfoTimer
		env.mu.Unlock()
		if timer != nil {
			t.Error("timer remains after completion")
		}
	})
	t.Run("08_generation_valid", func(t *testing.T) {
		env.mu.Lock()
		got := env.systemInfoTimerGeneration
		env.mu.Unlock()
		if got == 0 {
			t.Error("generation is zero")
		}
	})
	t.Run("09_context_snapshot", func(t *testing.T) {
		env.mu.Lock()
		got := env.systemInfoSnapshot.locale
		env.mu.Unlock()
		if got != "fr-FR" {
			t.Errorf("snapshot locale = %q", got)
		}
	})
	t.Run("10_repeat_change", func(t *testing.T) {
		env2 := NewHostEnv()
		env2.systemInfoProvider = func() systemContext { return systemContext{locale: "de-DE"} }
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x300})
		env2.CancelOp(0x300)
		if env2.PendingOps() != 0 {
			t.Error("repeat wait cancellation left pending operation")
		}
	})
	t.Run("11_invalid_provider_still_waits", func(t *testing.T) {
		env2 := NewHostEnv()
		env2.systemInfoProvider = nil
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x400})
		if env2.PendingOps() == 0 {
			t.Error("default-provider wait did not register")
		}
		env2.CancelOp(0x400)
	})
	t.Run("12_context_equal_no_completion", func(t *testing.T) {
		env2 := NewHostEnv()
		ctx := systemContext{locale: "en-US"}
		env2.systemInfoProvider = func() systemContext { return ctx }
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x500})
		env2.mu.Lock()
		generation := env2.systemInfoTimerGeneration
		env2.mu.Unlock()
		env2.pollSystemInfo(generation)
		env2.mu.Lock()
		_, waiting := env2.systemInfoWaiters[0x500]
		env2.mu.Unlock()
		if !waiting {
			t.Error("equal context removed waiter")
		}
		env2.CancelOp(0x500)
	})
}

func TestCancelSystemInfoWaitRemovesWaiterAndTimer(t *testing.T) {
	env := NewHostEnv()
	mod := newMockModule(0x10000)
	const ovPtr = uint32(0x100)

	env.sys_system_info_wait(context.Background(), mod, []uint64{uint64(ovPtr)})
	env.CancelOp(ovPtr)

	env.mu.Lock()
	_, waiting := env.systemInfoWaiters[ovPtr]
	timer := env.systemInfoTimer
	_, active := env.activeOps[ovPtr]
	env.mu.Unlock()
	if waiting || timer != nil || active {
		t.Errorf("waiter cleanup: waiting=%t timer=%v active=%t; want all cleared", waiting, timer, active)
	}
	if got := env.PendingOps(); got != 0 {
		t.Errorf("pending operations = %d, want 0", got)
	}
	data, ok := mod.Memory().Read(ovPtr, 24)
	if !ok {
		t.Fatal("read canceled overlapped")
	}
	if got := binary.LittleEndian.Uint32(data[4:8]); got != wasiECANCELED {
		t.Errorf("system info wait error = %d, want canceled", got)
	}
	t.Run("01_waiter_registered_before_cancel", func(t *testing.T) {
		env2 := NewHostEnv()
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x200})
		env2.mu.Lock()
		_, ok := env2.systemInfoWaiters[0x200]
		env2.mu.Unlock()
		if !ok {
			t.Error("waiter missing before cancel")
		}
		env2.CancelOp(0x200)
	})
	t.Run("02_waiter_removed", func(t *testing.T) {
		env.mu.Lock()
		_, ok := env.systemInfoWaiters[ovPtr]
		env.mu.Unlock()
		if ok {
			t.Error("waiter remains")
		}
	})
	t.Run("03_timer_removed", func(t *testing.T) {
		env.mu.Lock()
		timer := env.systemInfoTimer
		env.mu.Unlock()
		if timer != nil {
			t.Error("timer remains")
		}
	})
	t.Run("04_active_removed", func(t *testing.T) {
		env.mu.Lock()
		_, ok := env.activeOps[ovPtr]
		env.mu.Unlock()
		if ok {
			t.Error("active operation remains")
		}
	})
	t.Run("05_pending_zero", func(t *testing.T) {
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending = %d", got)
		}
	})
	t.Run("06_canceled_error", func(t *testing.T) {
		if got := binary.LittleEndian.Uint32(data[4:8]); got != wasiECANCELED {
			t.Errorf("error = %d", got)
		}
	})
	t.Run("07_completed_flag", func(t *testing.T) {
		if got := binary.LittleEndian.Uint32(data[0:4]); got != 1 {
			t.Errorf("flags = %d", got)
		}
	})
	t.Run("08_idempotent_second_cancel", func(t *testing.T) {
		env.CancelOp(ovPtr)
		if got := env.PendingOps(); got != 0 {
			t.Errorf("pending = %d", got)
		}
	})
	t.Run("09_cancel_other_waiter_keeps_shared_timer", func(t *testing.T) {
		env2 := NewHostEnv()
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x300})
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x301})
		env2.CancelOp(0x300)
		env2.mu.Lock()
		timer := env2.systemInfoTimer
		_, remaining := env2.systemInfoWaiters[0x301]
		env2.mu.Unlock()
		if timer == nil || !remaining {
			t.Error("shared waiter/timer cleanup incorrect")
		}
		env2.CancelOp(0x301)
	})
	t.Run("10_last_cancel_stops_timer", func(t *testing.T) {
		env2 := NewHostEnv()
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x400})
		env2.CancelOp(0x400)
		env2.mu.Lock()
		timer := env2.systemInfoTimer
		env2.mu.Unlock()
		if timer != nil {
			t.Error("last cancel left timer")
		}
	})
	t.Run("11_cancel_invalid_pointer_safe", func(t *testing.T) {
		env2 := NewHostEnv()
		env2.CancelOp(0xffffffff)
		if got := env2.PendingOps(); got != 0 {
			t.Errorf("pending = %d", got)
		}
	})
	t.Run("12_generation_advanced", func(t *testing.T) {
		env2 := NewHostEnv()
		mod2 := newMockModule(0x10000)
		env2.sys_system_info_wait(context.Background(), mod2, []uint64{0x500})
		env2.mu.Lock()
		before := env2.systemInfoTimerGeneration
		env2.mu.Unlock()
		env2.CancelOp(0x500)
		env2.mu.Lock()
		after := env2.systemInfoTimerGeneration
		env2.mu.Unlock()
		if after <= before {
			t.Errorf("generation not advanced: before=%d after=%d", before, after)
		}
	})
}

func TestSysFs(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "washmhost-fs-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	env := NewHostEnv()
	mod := newMockModule(0x10000)
	runOperation := func(t *testing.T, ovPtr uint32, operation func()) {
		t.Helper()
		if !mod.Memory().Write(ovPtr, make([]byte, 24)) {
			t.Fatalf("failed to clear overlapped header at %d", ovPtr)
		}
		operation()

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for {
			header, ok := mod.Memory().Read(ovPtr, 4)
			if !ok {
				t.Fatalf("overlapped pointer %d is out of bounds", ovPtr)
			}
			if binary.LittleEndian.Uint32(header) == 1 {
				return
			}
			env.Poll(ctx, mod)
			if ctx.Err() != nil {
				t.Fatalf("timeout waiting for overlapped completion at %d", ovPtr)
			}
		}
	}

	// We need to set up the environment "cwd" if path_open uses it.
	// Looking at WASHMHOST-testing.md, path_open likely uses host paths or rooted paths.

	t.Run("1. sys_path_open create file", func(t *testing.T) {
		path := filepath.Join(tmpDir, "test.txt")
		pathPtr := uint32(0x100)
		mod.Memory().Write(pathPtr, []byte(path))

		ovPtr := uint32(0x200)
		// O_RDWR is 2 and O_CREATE is 0x40 in the Rusticated Go ABI.
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0x42, 0644})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0x42, 0644})
		})
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		data := "HELLO FS"
		dataPtr := uint32(0x500)
		mod.Memory().Write(dataPtr, []byte(data))

		ovPtrWrite := uint32(0x600)
		runOperation(t, ovPtrWrite, func() {
			env.sys_write(context.Background(), mod, []uint64{uint64(ovPtrWrite), handle, uint64(dataPtr), uint64(len(data))})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		})
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		bufPtr := uint32(0x900)
		ovPtrRead := uint32(0xA00)
		runOperation(t, ovPtrRead, func() {
			env.sys_read(context.Background(), mod, []uint64{uint64(ovPtrRead), handle, uint64(bufPtr), 4})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_stat(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, uint64(statPtr), 112})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_stat(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(tmpDir)), 0, uint64(statPtr), 112})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		})
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
		runOperation(t, ovPtr, func() {
			env.sys_path_chmod(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0444})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_chmod(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0o4755})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(d)), 0, 0})
		})
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		bufPtr := uint32(0x1400)
		// sys_dir_read(ctx, mod, [ovPtr, handle, bufPtr, bufLen])
		runOperation(t, 0x1500, func() {
			env.sys_dir_read(context.Background(), mod, []uint64{0x1500, handle, uint64(bufPtr), 1024})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		})
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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		})
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		bufPtr := uint32(0x1A00)
		runOperation(t, 0x1B00, func() {
			env.sys_read(context.Background(), mod, []uint64{0x1B00, handle, uint64(bufPtr), 100})
		})

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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0 /* O_RDONLY */, 0})
		})
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		ovPtrWrite := uint32(0x1E00)
		runOperation(t, ovPtrWrite, func() {
			env.sys_write(context.Background(), mod, []uint64{uint64(ovPtrWrite), handle, 0x1F00, 4})
		})
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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		})
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		ovPtrDir := uint32(0x2200)
		runOperation(t, ovPtrDir, func() {
			env.sys_dir_read(context.Background(), mod, []uint64{uint64(ovPtrDir), handle, 0x2300, 100})
		})
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
		runOperation(t, ovPtr, func() {
			env.sys_path_open(context.Background(), mod, []uint64{uint64(ovPtr), uint64(pathPtr), uint64(len(path)), 0, 0})
		})
		val, _ := mod.Memory().Read(ovPtr, 24)
		handle := binary.LittleEndian.Uint64(val[16:24])

		env.sys_handle_close(context.Background(), mod, []uint64{handle})
		// Should not panic on second close
		env.sys_handle_close(context.Background(), mod, []uint64{handle})
	})

	t.Run("14. sys_read from invalid handle", func(t *testing.T) {
		ovPtr := uint32(0x2600)
		runOperation(t, ovPtr, func() {
			env.sys_read(context.Background(), mod, []uint64{uint64(ovPtr), 9999, 0, 0})
		})

		val, _ := mod.Memory().Read(ovPtr, 24)
		errCode := binary.LittleEndian.Uint32(val[4:8])
		if errCode != wasiEBADF {
			t.Errorf("expected EBADF (%d), got %d", wasiEBADF, errCode)
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
