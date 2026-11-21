const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");
const win = @import("win_ffi.zig");
const mem = @import("mem.zig");
const PalState = mem.PalState;

pub fn getTime(s: *PalState) u64 {
    const get_ft = s.p_get_system_time_ft orelse return 0;
    var ft: win.FileTime = .{};
    get_ft(&ft);
    const ns = win.fileTimeToUnixNs(ft);
    return if (ns < 0) 0 else @intCast(ns);
}

pub fn getRandom(s: *PalState, buf: [*]u8, len: usize) void {
    if (len == 0) return;
    const p_fn = mem.resolveDll(s, &s.bcrypt, "bcrypt.dll", "BCryptGenRandom") orelse return;
    const BCryptGenRandomFn = *const fn (?*anyopaque, [*]u8, u32, u32) callconv(win.win_cc) i32;
    const gen: BCryptGenRandomFn = @ptrCast(@alignCast(p_fn));
    // BCRYPT_USE_SYSTEM_PREFERRED_RNG = 0x2
    _ = gen(null, buf, @intCast(@min(len, 0x7fff_ffff)), 0x2);
}

pub fn getArgs(s: *PalState, buf: [*]u8, cap: usize) u64 {
    const argc = s.host.argc;
    const argv = s.host.argv orelse {
        return 0;
    };
    var needed: usize = 0;
    for (0..argc) |i| {
        const arg = std.mem.sliceTo(argv[i], 0);
        needed += arg.len + 1;
    }
    if (needed > cap) {
        return (@as(u64, @intCast(argc)) << 32) | @as(u64, @intCast(needed));
    }
    var off: usize = 0;
    for (0..argc) |i| {
        const arg = std.mem.sliceTo(argv[i], 0);
        @memcpy(buf[off..][0..arg.len], arg);
        buf[off + arg.len] = 0;
        off += arg.len + 1;
    }
    return (@as(u64, @intCast(argc)) << 32) | @as(u64, @intCast(off));
}

pub fn getEnv(s: *PalState, buf: [*]u8, cap: usize) u64 {
    const p_get_env = mem.resolveKernel32(s, "GetEnvironmentStringsW") orelse return 0;
    const p_free_env = mem.resolveKernel32(s, "FreeEnvironmentStringsW") orelse return 0;
    const GetEnvFn = *const fn () callconv(win.win_cc) ?[*]const u16;
    const FreeEnvFn = *const fn ([*]const u16) callconv(win.win_cc) i32;
    const get_env: GetEnvFn = @ptrCast(@alignCast(p_get_env));
    const free_env: FreeEnvFn = @ptrCast(@alignCast(p_free_env));

    const env_w = get_env() orelse return 0;
    defer _ = free_env(env_w);

    var count: usize = 0;
    var off: usize = 0;
    var needed: usize = 0;
    var i: usize = 0;
    while (true) {
        var len_w: usize = 0;
        while (env_w[i + len_w] != 0) : (len_w += 1) {}
        if (len_w == 0) break;
        if (env_w[i] != '=') {
            var tmp: [4096]u8 = undefined;
            const n = mem.wideToUtf8(s, env_w + i, @intCast(len_w), &tmp, tmp.len);
            if (n > 0) {
                count += 1;
                needed += n + 1;
                if (off + n + 1 <= cap) {
                    @memcpy(buf[off..][0..n], tmp[0..n]);
                    buf[off + n] = 0;
                    off += n + 1;
                }
            }
        }
        i += len_w + 1;
    }
    if (needed > cap) {
        return (@as(u64, @intCast(count)) << 32) | @as(u64, @intCast(needed));
    }
    return (@as(u64, @intCast(count)) << 32) | @as(u64, @intCast(off));
}

pub fn getCwd(s: *PalState, buf: [*]u8, cap: usize) u64 {
    const p_fn = mem.resolveKernel32(s, "GetCurrentDirectoryW") orelse return 0;
    const GetCwdWFn = *const fn (u32, ?[*]u16) callconv(win.win_cc) u32;
    const get_cwd_w: GetCwdWFn = @ptrCast(@alignCast(p_fn));
    var wbuf: [1024]u16 = undefined;
    const wlen = get_cwd_w(wbuf.len, &wbuf);
    if (wlen == 0 or wlen >= wbuf.len) return 0;
    var tmp: [2048]u8 = undefined;
    const n = mem.wideToUtf8(s, &wbuf, @intCast(wlen), &tmp, tmp.len);
    if (n == 0) return 0;
    if (n > cap) return @as(u64, @intCast(n));
    @memcpy(buf[0..n], tmp[0..n]);
    return @as(u64, @intCast(n));
}

pub fn setCwd(s: *PalState, path: [*]const u8, len: usize) u32 {
    const p_fn = mem.resolveKernel32(s, "SetCurrentDirectoryW") orelse return pal_abi.ERROR_UNSUPPORTED;
    const SetCwdWFn = *const fn ([*:0]const u16) callconv(win.win_cc) i32;
    const set_cwd_w: SetCwdWFn = @ptrCast(@alignCast(p_fn));
    const wpath = mem.utf8ToWideAlloc(s, path, len) orelse return pal_abi.ERROR_INVALID;
    defer mem.freeWideAlloc(s, @ptrCast(@constCast(wpath)));
    if (set_cwd_w(wpath) == 0) {
        const gle = s.p_get_last_error orelse return pal_abi.ERROR_IO;
        return win.mapWinError(gle());
    }
    return 0;
}

fn copyFixed(dst: []u8, src: []const u8) void {
    const n = @min(dst.len, src.len);
    @memcpy(dst[0..n], src[0..n]);
}

const SystemPowerStatus = extern struct {
    ac_line_status: u8 = 255,
    battery_flag: u8 = 255,
    battery_life_percent: u8 = 255,
    system_status_flag: u8 = 0,
    battery_life_time: u32 = 0xffff_ffff,
    battery_full_life_time: u32 = 0xffff_ffff,
};

const TimeZoneInformation = extern struct {
    bias: i32 = 0,
    standard_name: [32]u16 = @splat(0),
    standard_date: [8]u16 = @splat(0),
    standard_bias: i32 = 0,
    daylight_name: [32]u16 = @splat(0),
    daylight_date: [8]u16 = @splat(0),
    daylight_bias: i32 = 0,
};

var test_system_context_override: ?*const fn (*pal_abi.AbiPlatformInfo) void = null;

pub fn setTestSystemContextOverride(hook: ?*const fn (*pal_abi.AbiPlatformInfo) void) void {
    if (!builtin.is_test) return;
    test_system_context_override = hook;
}

pub fn populateSystemContext(s: *PalState, info: *pal_abi.AbiPlatformInfo) void {
    info.system_context_version = s.system_info_version;

    if (builtin.is_test) {
        if (test_system_context_override) |hook| {
            hook(info);
            return;
        }
    }

    if (mem.resolveKernel32(s, "GetUserDefaultLocaleName")) |p_loc| {
        const LocFn = *const fn ([*]u16, i32) callconv(win.win_cc) i32;
        const get_loc: LocFn = @ptrCast(@alignCast(p_loc));
        var wbuf: [85]u16 = @splat(0);
        const wlen = get_loc(&wbuf, wbuf.len);
        if (wlen > 1) {
            _ = mem.wideToUtf8(s, &wbuf, wlen - 1, &info.locale, info.locale.len);
        }
    }

    if (mem.resolveKernel32(s, "GetTimeZoneInformation")) |p_tz| {
        const TzFn = *const fn (*TimeZoneInformation) callconv(win.win_cc) u32;
        const get_tz: TzFn = @ptrCast(@alignCast(p_tz));
        var tzi: TimeZoneInformation = .{};
        const tz_id = get_tz(&tzi);
        if (tz_id != 0xffff_ffff) {
            const name_slice = if (tz_id == 2 and tzi.daylight_name[0] != 0) &tzi.daylight_name else &tzi.standard_name;
            var len_w: usize = 0;
            while (len_w < name_slice.len and name_slice[len_w] != 0) : (len_w += 1) {}
            if (len_w > 0) {
                _ = mem.wideToUtf8(s, name_slice, @intCast(len_w), &info.time_zone, info.time_zone.len);
            }
        }
    }

    if (mem.resolveKernel32(s, "GetSystemPowerStatus")) |p_pwr| {
        const PwrFn = *const fn (*SystemPowerStatus) callconv(win.win_cc) i32;
        const get_pwr: PwrFn = @ptrCast(@alignCast(p_pwr));
        var sps: SystemPowerStatus = .{};
        if (get_pwr(&sps) != 0) {
            if (sps.ac_line_status == 1) {
                copyFixed(&info.power_source, "ac");
            } else if (sps.ac_line_status == 0) {
                copyFixed(&info.power_source, "battery");
            }
        }
    }

    if (mem.resolveDll(s, &s.user32, "user32.dll", "GetKeyboardLayoutNameW")) |p_kbd| {
        const KbdFn = *const fn ([*]u16) callconv(win.win_cc) i32;
        const get_kbd: KbdFn = @ptrCast(@alignCast(p_kbd));
        var wbuf: [16]u16 = @splat(0);
        if (get_kbd(&wbuf) != 0) {
            var len_w: usize = 0;
            while (len_w < wbuf.len and wbuf[len_w] != 0) : (len_w += 1) {}
            if (len_w > 0) {
                _ = mem.wideToUtf8(s, &wbuf, @intCast(len_w), &info.keyboard_layout_id, info.keyboard_layout_id.len);
            }
        }
    }

    if (mem.resolveDll(s, &s.advapi32, "advapi32.dll", "RegGetValueW")) |p_reg| {
        const RegGetFn = *const fn (usize, [*:0]const u16, [*:0]const u16, u32, ?*u32, ?*u32, ?*u32) callconv(win.win_cc) i32;
        const reg_get: RegGetFn = @ptrCast(@alignCast(p_reg));
        const hkcu: usize = 0x8000_0001;
        const subkey = std.unicode.utf8ToUtf16LeStringLiteral("Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize");
        const val_name = std.unicode.utf8ToUtf16LeStringLiteral("AppsUseLightTheme");
        var light_theme: u32 = 1;
        var data_size: u32 = @sizeOf(u32);
        // RRF_RT_REG_DWORD = 0x00000010
        if (reg_get(hkcu, subkey, val_name, 0x10, null, &light_theme, &data_size) == 0) {
            copyFixed(&info.appearance, if (light_theme == 0) "dark" else "light");
        }
    }
}

fn hashSlice(h: u64, bytes: []const u8) u64 {
    var v = h;
    for (bytes) |b| {
        v ^= b;
        v *%= 0x100000001b3;
    }
    return v;
}

pub fn captureSystemContextHash(s: *PalState) u64 {
    var info: pal_abi.AbiPlatformInfo = .{};
    populateSystemContext(s, &info);
    var h: u64 = 0xcbf29ce484222325;
    h = hashSlice(h, &info.locale);
    h = hashSlice(h, &info.time_zone);
    h = hashSlice(h, &info.keyboard_layout_id);
    h = hashSlice(h, &info.appearance);
    h = hashSlice(h, &info.power_source);
    return h;
}

pub fn pollSystemContextChanged(s: *PalState) bool {
    const h = captureSystemContextHash(s);
    if (!s.has_system_info_hash) {
        s.system_info_hash = h;
        s.has_system_info_hash = true;
        return false;
    }
    if (h != s.system_info_hash) {
        s.system_info_hash = h;
        s.system_info_version +%= 1;
        return true;
    }
    return false;
}

fn expectFilled(actual: []const u8, value: u8) !void {
    for (actual) |byte| try std.testing.expectEqual(value, byte);
}

pub fn getPlatformInfo(s: *PalState, out: [*]u8, cap: usize) u32 {
    if (cap < @sizeOf(pal_abi.AbiPlatformInfo)) return pal_abi.ERROR_INVALID;
    var info: pal_abi.AbiPlatformInfo = .{};
    info.path_separator = '\\';
    info.path_list_separator = ';';
    info.os_kind = pal_abi.OS_KIND_WINDOWS;
    info.os_version = .{ 10, 0, 26100, 0 };
    info.cpu_type = switch (builtin.cpu.arch) {
        .x86_64 => pal_abi.CPU_TYPE_X86_64,
        .aarch64 => pal_abi.CPU_TYPE_AARCH64,
        else => 0,
    };
    info.cpu_bitness = 64;
    copyFixed(&info.os_name, "windows");
    copyFixed(&info.wasm_platform, "mohabbat-next-zig");
    copyFixed(&info.rusticated_name, "rusticated");
    copyFixed(&info.build_platform, if (builtin.cpu.arch == .x86_64) "windows-amd64" else "windows-arm64");

    // Populate user_home_dir via GetEnvironmentVariableW(L"USERPROFILE")
    if (mem.resolveKernel32(s, "GetEnvironmentVariableW")) |p_gev| {
        const GetEnvWFn = *const fn ([*:0]const u16, [*]u16, u32) callconv(win.win_cc) u32;
        const get_env: GetEnvWFn = @ptrCast(@alignCast(p_gev));
        const key_up = [_:0]u16{ 'U', 'S', 'E', 'R', 'P', 'R', 'O', 'F', 'I', 'L', 'E' };
        var wbuf: [260]u16 = undefined;
        const wlen = get_env(&key_up, &wbuf, wbuf.len);
        if (wlen > 0 and wlen < wbuf.len) {
            _ = mem.wideToUtf8(s, &wbuf, @intCast(wlen), &info.user_home_dir, info.user_home_dir.len);
        }
    }

    // Populate temp_dir via GetTempPathW
    if (mem.resolveKernel32(s, "GetTempPathW")) |p_tmp| {
        const GetTempWFn = *const fn (u32, [*]u16) callconv(win.win_cc) u32;
        const get_tmp: GetTempWFn = @ptrCast(@alignCast(p_tmp));
        var wbuf: [260]u16 = undefined;
        const wlen = get_tmp(wbuf.len, &wbuf);
        if (wlen > 0 and wlen < wbuf.len) {
            _ = mem.wideToUtf8(s, &wbuf, @intCast(wlen), &info.temp_dir, info.temp_dir.len);
        }
    }
    populateSystemContext(s, &info);
    const bytes = std.mem.asBytes(&info);
    @memcpy(out[0..bytes.len], bytes);
    return 0;
}

test "Windows environment arguments report required size without partial writes" {
    const args = [_][*:0]const u8{ "program", "--x", "" };
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;

    var out: [13]u8 = @splat(0xcc);
    const result = getArgs(&state, &out, out.len - 1);
    try std.testing.expectEqual(@as(u32, 3), @as(u32, @truncate(result >> 32)));
    try std.testing.expectEqual(@as(u32, 13), @as(u32, @truncate(result)));
    const untouched: [13]u8 = @splat(0xcc);
    try std.testing.expectEqualSlices(u8, &untouched, &out);

    const written = getArgs(&state, &out, out.len);
    try std.testing.expectEqual(result, written);
    try std.testing.expectEqualSlices(u8, "program\x00--x\x00\x00", &out);
}

test "Windows environment arguments handle absent and empty argv" {
    var state: PalState = .{};
    var out: [1]u8 = .{0xaa};
    try std.testing.expectEqual(@as(u64, 0), getArgs(&state, &out, out.len));

    const args = [_][*:0]const u8{};
    state.host.argv = &args;
    try std.testing.expectEqual(@as(u64, 0), getArgs(&state, &out, out.len));
    try std.testing.expectEqual(@as(u8, 0xaa), out[0]);
}

const test_env_strings = [_]u16{
    '=', 'C', ':', '=', 'C', ':', '\\', 'w', 'o', 'r', 'k', 0,
    'A', '=', '1', 0,
    'B', '=', '2', 0,
    0,
};
var test_env_free_calls: usize = 0;
var test_env_override: ?[*]const u16 = null;

fn testGetEnvironmentStrings() callconv(win.win_cc) ?[*]const u16 {
    return test_env_override orelse &test_env_strings;
}

fn testFreeEnvironmentStrings(_: [*]const u16) callconv(win.win_cc) i32 {
    test_env_free_calls += 1;
    return 1;
}

fn testGetProcAddress(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    const symbol = std.mem.sliceTo(name, 0);
    if (std.mem.eql(u8, symbol, "GetEnvironmentStringsW")) return @ptrCast(@constCast(&testGetEnvironmentStrings));
    if (std.mem.eql(u8, symbol, "FreeEnvironmentStringsW")) return @ptrCast(@constCast(&testFreeEnvironmentStrings));
    return null;
}

fn testWideToMultiByte(
    _: u32,
    _: u32,
    wide: [*]const u16,
    wide_len: i32,
    out: ?[*]u8,
    _: i32,
    _: ?[*]const u8,
    _: ?*i32,
) callconv(win.win_cc) i32 {
    if (wide_len < 0) return 0;
    const len: usize = @intCast(wide_len);
    if (out) |bytes| {
        for (0..len) |i| {
            if (wide[i] > 0x7f) return 0;
            bytes[i] = @intCast(wide[i]);
        }
    }
    return wide_len;
}

test "Windows environment enumeration filters drive entries and reports short buffers" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = testGetProcAddress;
    state.p_wide_to_mb = testWideToMultiByte;
    test_env_free_calls = 0;

    var out: [8]u8 = @splat(0xcc);
    const short = getEnv(&state, &out, out.len - 1);
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(short >> 32)));
    try std.testing.expectEqual(@as(u32, 8), @as(u32, @truncate(short)));
    try std.testing.expectEqualSlices(u8, "A=1\x00", out[0..4]);
    const untouched: [4]u8 = @splat(0xcc);
    try std.testing.expectEqualSlices(u8, &untouched, out[4..]);
    try std.testing.expectEqual(@as(usize, 1), test_env_free_calls);

    const full = getEnv(&state, &out, out.len);
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(full >> 32)));
    try std.testing.expectEqual(@as(u32, 8), @as(u32, @truncate(full)));
    try std.testing.expectEqualSlices(u8, "A=1\x00B=2\x00", &out);
    try std.testing.expectEqual(@as(usize, 2), test_env_free_calls);
}

fn testSystemTime(out: *win.FileTime) callconv(win.win_cc) void {
    out.* = .{ .low = 0x9f7b_7600, .high = 0x01dc_0000 };
}

fn testPreEpochSystemTime(out: *win.FileTime) callconv(win.win_cc) void {
    out.* = .{};
}

var time_read_calls: usize = 0;

fn testCountedSystemTime(out: *win.FileTime) callconv(win.win_cc) void {
    time_read_calls += 1;
    out.* = .{};
}

test "Windows environment time handles unavailable and pre-Unix-epoch clocks" {
    var state: PalState = .{};
    try std.testing.expectEqual(@as(u64, 0), getTime(&state));

    state.p_get_system_time_ft = testPreEpochSystemTime;
    try std.testing.expectEqual(@as(u64, 0), getTime(&state));
}

test "Windows environment time converts FILETIME and saturates its upper range" {
    var state: PalState = .{};
    state.p_get_system_time_ft = testSystemTime;
    const raw: u64 = (@as(u64, 0x01dc_0000) << 32) | 0x9f7b_7600;
    const expected = win.fileTimeToUnixNs(.{ .low = @truncate(raw), .high = @truncate(raw >> 32) });
    try std.testing.expect(expected > 0);
    try std.testing.expectEqual(@as(u64, @intCast(expected)), getTime(&state));

    const max_file_time = win.FileTime{ .low = 0xffff_ffff, .high = 0xffff_ffff };
    try std.testing.expectEqual(@as(i64, 0x7fff_ffff_ffff_ffff), win.fileTimeToUnixNs(max_file_time));
}

test "Windows platform fixed strings truncate to the destination" {
    var dst: [4]u8 = @splat(0xaa);
    copyFixed(&dst, "windows");
    try std.testing.expectEqualSlices(u8, "wind", &dst);

    copyFixed(&dst, "");
    try std.testing.expectEqualSlices(u8, "wind", &dst);
}

test "Windows environment time returns the Unix epoch as zero" {
    var state: PalState = .{};
    state.p_get_system_time_ft = struct {
        fn read(out: *win.FileTime) callconv(win.win_cc) void {
            const raw_time: u64 = 116_444_736_000_000_000;
            out.* = .{ .low = @truncate(raw_time), .high = @truncate(raw_time >> 32) };
        }
    }.read;
    try std.testing.expectEqual(@as(u64, 0), getTime(&state));
}

test "Windows environment time preserves one hundred nanosecond ticks" {
    const raw: u64 = 116_444_736_000_000_001;
    var state: PalState = .{};
    state.p_get_system_time_ft = struct {
        fn read(out: *win.FileTime) callconv(win.win_cc) void {
            const raw_time: u64 = 116_444_736_000_000_001;
            out.* = .{ .low = @truncate(raw_time), .high = @truncate(raw_time >> 32) };
        }
    }.read;
    try std.testing.expectEqual(@as(u64, (raw - 116_444_736_000_000_000) * 100), getTime(&state));
}

test "Windows environment time converts whole seconds after the epoch" {
    var state: PalState = .{};
    state.p_get_system_time_ft = struct {
        fn read(out: *win.FileTime) callconv(win.win_cc) void {
            const raw_time: u64 = 116_444_736_000_000_000 + 12_345_678 * 10_000_000;
            out.* = .{ .low = @truncate(raw_time), .high = @truncate(raw_time >> 32) };
        }
    }.read;
    try std.testing.expectEqual(@as(u64, 12_345_678 * 1_000_000_000), getTime(&state));
}

test "Windows environment time clamps the maximum FILETIME to signed nanoseconds" {
    var state: PalState = .{};
    state.p_get_system_time_ft = struct {
        fn read(out: *win.FileTime) callconv(win.win_cc) void {
            out.* = .{ .low = 0xffff_ffff, .high = 0xffff_ffff };
        }
    }.read;
    try std.testing.expectEqual(@as(u64, 0x7fff_ffff_ffff_ffff), getTime(&state));
}

test "Windows environment arguments report total size on short capacity" {
    const args = [_][*:0]const u8{ "a", "bc" };
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;
    var out: [5]u8 = @splat(0xa5);
    const result = getArgs(&state, &out, 3);
    try std.testing.expectEqual(@as(u32, 5), @as(u32, @truncate(result)));
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(result >> 32)));
    try expectFilled(&out, 0xa5);
}

test "Windows environment arguments leave unused output capacity intact" {
    const args = [_][*:0]const u8{"xy"};
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;
    var out: [6]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0x1_0000_0003), getArgs(&state, &out, out.len));
    try std.testing.expectEqualSlices(u8, "xy\x00", out[0..3]);
    try expectFilled(out[3..], 0xa5);
}

test "Windows environment arguments encode an empty argument as a terminator" {
    const args = [_][*:0]const u8{""};
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;
    var out: [1]u8 = .{0xa5};
    try std.testing.expectEqual(@as(u64, 0x1_0000_0001), getArgs(&state, &out, out.len));
    try std.testing.expectEqual(@as(u8, 0), out[0]);
}

test "Windows environment arguments support zero capacity for no arguments" {
    const args = [_][*:0]const u8{};
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = 0;
    try std.testing.expectEqual(@as(u64, 0), getArgs(&state, undefined, 0));
}

test "Windows environment arguments report required size at zero capacity" {
    const args = [_][*:0]const u8{"x"};
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;
    var out: [1]u8 = .{0x5a};
    try std.testing.expectEqual(@as(u64, 0x1_0000_0002), getArgs(&state, &out, 0));
    try std.testing.expectEqual(@as(u8, 0x5a), out[0]);
}

test "Windows environment arguments support multiple empty arguments" {
    const args = [_][*:0]const u8{ "", "", "" };
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;
    var out: [3]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0x3_0000_0003), getArgs(&state, &out, out.len));
    try std.testing.expectEqualSlices(u8, "\x00\x00\x00", &out);
}

test "Windows environment enumeration returns zero when kernel32 is unavailable" {
    var state: PalState = .{};
    var out: [8]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getEnv(&state, &out, out.len));
    try expectFilled(&out, 0xa5);
}

test "Windows environment enumeration returns zero when environment acquisition fails" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            const symbol = std.mem.sliceTo(name, 0);
            if (std.mem.eql(u8, symbol, "GetEnvironmentStringsW")) return @ptrCast(@constCast(&failed));
            if (std.mem.eql(u8, symbol, "FreeEnvironmentStringsW")) return @ptrCast(@constCast(&testFreeEnvironmentStrings));
            return null;
        }
        fn failed() callconv(win.win_cc) ?[*]const u16 {
            return null;
        }
    }.lookup;
    state.p_wide_to_mb = testWideToMultiByte;
    var out: [8]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getEnv(&state, &out, out.len));
}

test "Windows environment enumeration skips drive pseudo-variables" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = testGetProcAddress;
    state.p_wide_to_mb = testWideToMultiByte;
    test_env_free_calls = 0;
    test_env_override = &[_]u16{ '=', 'C', ':', '=', 'x', 0, 0 };
    defer test_env_override = null;
    var out: [8]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getEnv(&state, &out, out.len));
    try std.testing.expectEqual(@as(usize, 1), test_env_free_calls);
    try expectFilled(&out, 0xa5);
}

test "Windows environment enumeration fits an exact output capacity" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = testGetProcAddress;
    state.p_wide_to_mb = testWideToMultiByte;
    test_env_override = &[_]u16{ 'X', '=', 'v', 0, 0 };
    defer test_env_override = null;
    var out: [4]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0x1_0000_0004), getEnv(&state, &out, out.len));
    try std.testing.expectEqualSlices(u8, "X=v\x00", &out);
}

test "Windows environment enumeration accepts an empty environment block" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = testGetProcAddress;
    state.p_wide_to_mb = testWideToMultiByte;
    test_env_free_calls = 0;
    test_env_override = &[_]u16{0};
    defer test_env_override = null;
    var out: [1]u8 = .{0xa5};
    try std.testing.expectEqual(@as(u64, 0), getEnv(&state, &out, out.len));
    try std.testing.expectEqual(@as(usize, 1), test_env_free_calls);
    try std.testing.expectEqual(@as(u8, 0xa5), out[0]);
}

test "Windows environment enumeration requires the matching free API" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "GetEnvironmentStringsW")) {
                return @ptrCast(@constCast(&testGetEnvironmentStrings));
            }
            return null;
        }
    }.lookup;
    state.p_wide_to_mb = testWideToMultiByte;
    var out: [8]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getEnv(&state, &out, out.len));
}

var test_cwd_wide: [1024]u16 = @splat(0);
var test_cwd_length: u32 = 0;

fn testGetCurrentDirectory(cap: u32, out: ?[*]u16) callconv(win.win_cc) u32 {
    if (out) |wide| {
        const n = @min(@as(usize, @intCast(cap)), @as(usize, @intCast(test_cwd_length)));
        @memcpy(wide[0..n], test_cwd_wide[0..n]);
    }
    return test_cwd_length;
}

fn cwdTestLookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "GetCurrentDirectoryW")) {
        return @ptrCast(@constCast(&testGetCurrentDirectory));
    }
    return null;
}

fn setTestCwd(value: []const u8) void {
    @memset(&test_cwd_wide, 0);
    for (value, 0..) |byte, i| test_cwd_wide[i] = byte;
    test_cwd_length = @intCast(value.len);
}

test "Windows current directory returns unsupported when kernel32 is absent" {
    var state: PalState = .{};
    var out: [16]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, &out, out.len));
}

test "Windows current directory returns unsupported when the API is absent" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    var out: [16]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, &out, out.len));
}

test "Windows current directory rejects a zero native result" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    test_cwd_length = 0;
    var out: [16]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, &out, out.len));
}

test "Windows current directory rejects a truncated native result" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    test_cwd_length = 1024;
    var out: [16]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, &out, out.len));
}

test "Windows current directory reports exact UTF-8 bytes written" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    state.p_wide_to_mb = testWideToMultiByte;
    setTestCwd("C:\\work");
    var out: [16]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 7), getCwd(&state, &out, out.len));
    try std.testing.expectEqualSlices(u8, "C:\\work", out[0..7]);
    try std.testing.expectEqual(@as(u8, 0xa5), out[7]);
}

test "Windows current directory reports required capacity without writing" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    state.p_wide_to_mb = testWideToMultiByte;
    setTestCwd("C:\\work");
    var out: [8]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 7), getCwd(&state, &out, 6));
    try expectFilled(&out, 0xa5);
}

test "Windows current directory accepts a zero capacity for an empty path" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    state.p_wide_to_mb = testWideToMultiByte;
    setTestCwd("");
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, undefined, 0));
}

test "Windows current directory fails safely without a UTF-8 converter" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    setTestCwd("C:\\work");
    var out: [16]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, &out, out.len));
}

test "Windows current directory preserves a path at an exact capacity" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    state.p_wide_to_mb = testWideToMultiByte;
    setTestCwd("C:\\");
    var out: [3]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 3), getCwd(&state, &out, out.len));
    try std.testing.expectEqualSlices(u8, "C:\\", &out);
}

test "Windows platform information rejects a short destination" {
    var state: PalState = .{};
    var out: [16]u8 = @splat(0xa5);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, getPlatformInfo(&state, &out, out.len));
    try expectFilled(&out, 0xa5);
}

test "Windows platform information reports native OS and path separators" {
    var state: PalState = .{};
    var info: pal_abi.AbiPlatformInfo = .{};
    try std.testing.expectEqual(@as(u32, 0), getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info))));
    try std.testing.expectEqual(pal_abi.OS_KIND_WINDOWS, info.os_kind);
    try std.testing.expectEqual(@as(u8, '\\'), info.path_separator);
    try std.testing.expectEqual(@as(u8, ';'), info.path_list_separator);
}

test "Windows platform information identifies the supported CPU and bitness" {
    var state: PalState = .{};
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    try std.testing.expect(info.cpu_type == pal_abi.CPU_TYPE_X86_64 or info.cpu_type == pal_abi.CPU_TYPE_AARCH64);
    try std.testing.expectEqual(@as(u8, 64), info.cpu_bitness);
}

test "Windows platform information reports Windows identity strings" {
    var state: PalState = .{};
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    try std.testing.expectEqualStrings("windows", std.mem.sliceTo(&info.os_name, 0));
    try std.testing.expectEqualStrings("mohabbat-next-zig", std.mem.sliceTo(&info.wasm_platform, 0));
    try std.testing.expectEqualStrings("rusticated", std.mem.sliceTo(&info.rusticated_name, 0));
}

test "Windows platform information includes a build platform label" {
    var state: PalState = .{};
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    const label = std.mem.sliceTo(&info.build_platform, 0);
    try std.testing.expect(std.mem.eql(u8, label, "windows-amd64") or std.mem.eql(u8, label, "windows-arm64"));
}

test "Windows platform information leaves optional paths empty without Windows APIs" {
    var state: PalState = .{};
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    try std.testing.expectEqual(@as(u8, 0), info.user_home_dir[0]);
    try std.testing.expectEqual(@as(u8, 0), info.temp_dir[0]);
}

var random_calls: usize = 0;
var random_last_len: u32 = 0;
var random_last_flags: u32 = 0;
var random_result: i32 = 0;
var random_load_calls: usize = 0;

fn testLoadLibrary(name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    random_load_calls += 1;
    return if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "bcrypt.dll")) @ptrFromInt(2) else null;
}

fn testRandomGenerator(_: ?*anyopaque, out: [*]u8, len: u32, flags: u32) callconv(win.win_cc) i32 {
    random_calls += 1;
    random_last_len = len;
    random_last_flags = flags;
    if (random_result == 0) {
        for (0..@min(@as(usize, len), 8)) |i| out[i] = @intCast(0x30 + i);
    }
    return random_result;
}

fn randomTestGetProcAddress(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "BCryptGenRandom")) {
        return @ptrCast(@constCast(&testRandomGenerator));
    }
    return null;
}

fn resetRandomTest() void {
    random_calls = 0;
    random_last_len = 0;
    random_last_flags = 0;
    random_result = 0;
    random_load_calls = 0;
}

test "Windows random generation ignores a zero-length request" {
    var state: PalState = .{};
    var out: [1]u8 = .{0xa5};
    resetRandomTest();
    getRandom(&state, &out, 0);
    try std.testing.expectEqual(@as(usize, 0), random_load_calls);
    try std.testing.expectEqual(@as(usize, 0), random_calls);
    try std.testing.expectEqual(@as(u8, 0xa5), out[0]);
}

test "Windows random generation safely handles missing library support" {
    var state: PalState = .{};
    state.host.get_proc_address = randomTestGetProcAddress;
    var out: [4]u8 = @splat(0xa5);
    getRandom(&state, &out, out.len);
    try expectFilled(&out, 0xa5);
    try std.testing.expectEqual(@as(usize, 0), random_calls);
}

test "Windows random generation safely handles a missing symbol" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    resetRandomTest();
    var out: [4]u8 = @splat(0xa5);
    getRandom(&state, &out, out.len);
    try std.testing.expectEqual(@as(usize, 1), random_load_calls);
    try expectFilled(&out, 0xa5);
}

test "Windows random generation requests the system-preferred provider" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    state.host.get_proc_address = randomTestGetProcAddress;
    resetRandomTest();
    var out: [4]u8 = @splat(0xa5);
    getRandom(&state, &out, out.len);
    try std.testing.expectEqual(@as(usize, 1), random_calls);
    try std.testing.expectEqual(@as(u32, 4), random_last_len);
    try std.testing.expectEqual(@as(u32, 2), random_last_flags);
    try std.testing.expectEqualSlices(u8, &.{ 0x30, 0x31, 0x32, 0x33 }, &out);
}

test "Windows random generation leaves caller bytes alone when BCrypt fails" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    state.host.get_proc_address = randomTestGetProcAddress;
    resetRandomTest();
    random_result = -1;
    var out: [4]u8 = @splat(0xa5);
    getRandom(&state, &out, out.len);
    try std.testing.expectEqual(@as(usize, 1), random_calls);
    try expectFilled(&out, 0xa5);
}

test "Windows random generation resolves bcrypt only once per state" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    state.host.get_proc_address = randomTestGetProcAddress;
    resetRandomTest();
    var out: [2]u8 = @splat(0);
    getRandom(&state, &out, out.len);
    getRandom(&state, &out, out.len);
    try std.testing.expectEqual(@as(usize, 1), random_load_calls);
    try std.testing.expectEqual(@as(usize, 2), random_calls);
}

test "Windows random generation accepts a one-byte buffer" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    state.host.get_proc_address = randomTestGetProcAddress;
    resetRandomTest();
    var out: [1]u8 = .{0};
    getRandom(&state, &out, out.len);
    try std.testing.expectEqual(@as(u32, 1), random_last_len);
    try std.testing.expectEqual(@as(u8, 0x30), out[0]);
}

test "Windows random generation clamps an oversized request to BCrypt's length" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    state.host.get_proc_address = randomTestGetProcAddress;
    resetRandomTest();
    var out: [8]u8 = @splat(0);
    getRandom(&state, &out, 0x8000_0000);
    try std.testing.expectEqual(@as(u32, 0x7fff_ffff), random_last_len);
    try std.testing.expectEqual(@as(usize, 1), random_calls);
}

test "Windows random generation requests exactly the provided size" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    state.host.get_proc_address = randomTestGetProcAddress;
    resetRandomTest();
    var out: [7]u8 = @splat(0);
    getRandom(&state, &out, out.len);
    try std.testing.expectEqual(@as(u32, 7), random_last_len);
}

test "Windows random generation tolerates a missing resolver callback" {
    var state: PalState = .{};
    state.host.load_library = testLoadLibrary;
    resetRandomTest();
    var out: [2]u8 = @splat(0xa5);
    getRandom(&state, &out, out.len);
    try std.testing.expectEqual(@as(usize, 0), random_load_calls);
    try expectFilled(&out, 0xa5);
}

var cwd_set_result: i32 = 1;
var cwd_set_error: u32 = 0;
var cwd_set_calls: usize = 0;
var cwd_set_value: [32]u16 = @splat(0);
var cwd_set_length: usize = 0;
var cwd_alloc_calls: usize = 0;
var cwd_free_calls: usize = 0;
var cwd_conversion_fails: bool = false;
var cwd_alloc_storage: [65536]u8 align(16) = @splat(0);

fn testHostAlloc(_: usize) callconv(win.win_cc) ?*anyopaque {
    cwd_alloc_calls += 1;
    return &cwd_alloc_storage;
}

fn testHostFree(_: ?*anyopaque) callconv(win.win_cc) void {
    cwd_free_calls += 1;
}

fn testUtf8ToWide(_: u32, _: u32, input: [*]const u8, input_len: i32, out: ?[*]u16, _: i32) callconv(win.win_cc) i32 {
    if (input_len <= 0 or cwd_conversion_fails) return 0;
    const len: usize = @intCast(input_len);
    if (out) |wide| {
        for (input[0..len], 0..) |byte, i| wide[i] = byte;
    }
    return input_len;
}

fn testSetCurrentDirectory(path: [*:0]const u16) callconv(win.win_cc) i32 {
    cwd_set_calls += 1;
    cwd_set_length = 0;
    while (cwd_set_length < cwd_set_value.len and path[cwd_set_length] != 0) : (cwd_set_length += 1) {
        cwd_set_value[cwd_set_length] = path[cwd_set_length];
    }
    return cwd_set_result;
}

fn testGetLastError() callconv(win.win_cc) u32 {
    return cwd_set_error;
}

fn setCwdTestLookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "SetCurrentDirectoryW")) {
        return @ptrCast(@constCast(&testSetCurrentDirectory));
    }
    return null;
}

fn resetCwdSetTest(state: *PalState) void {
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = setCwdTestLookup;
    state.host.alloc = testHostAlloc;
    state.host.free = testHostFree;
    state.p_mb_to_wide = testUtf8ToWide;
    state.p_get_last_error = testGetLastError;
    cwd_set_result = 1;
    cwd_set_error = 0;
    cwd_set_calls = 0;
    cwd_set_length = 0;
    cwd_alloc_calls = 0;
    cwd_free_calls = 0;
    cwd_conversion_fails = false;
}

test "Windows set current directory reports unsupported without kernel32" {
    var state: PalState = .{};
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, setCwd(&state, "C:\\".ptr, 3));
}

test "Windows set current directory reports unsupported when its API is absent" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, setCwd(&state, "C:\\".ptr, 3));
}

test "Windows set current directory validates required conversion hooks" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = setCwdTestLookup;
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, setCwd(&state, "C:\\".ptr, 3));
}

test "Windows set current directory rejects an empty path" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, setCwd(&state, "".ptr, 0));
    try std.testing.expectEqual(@as(usize, 0), cwd_set_calls);
}

test "Windows set current directory forwards a converted path" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    try std.testing.expectEqual(@as(u32, 0), setCwd(&state, "C:\\work".ptr, 7));
    try std.testing.expectEqual(@as(usize, 1), cwd_set_calls);
    try std.testing.expectEqualSlices(u16, &.{ 'C', ':', '\\', 'w', 'o', 'r', 'k' }, cwd_set_value[0..cwd_set_length]);
    try std.testing.expectEqual(@as(usize, 1), cwd_free_calls);
}

test "Windows set current directory maps access denied" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    cwd_set_result = 0;
    cwd_set_error = 5;
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, setCwd(&state, "C:\\".ptr, 3));
}

test "Windows set current directory maps a missing path" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    cwd_set_result = 0;
    cwd_set_error = 3;
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, setCwd(&state, "Z:\\".ptr, 3));
}

test "Windows set current directory reports an I/O error without last-error support" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    state.p_get_last_error = null;
    cwd_set_result = 0;
    try std.testing.expectEqual(pal_abi.ERROR_IO, setCwd(&state, "C:\\".ptr, 3));
}

test "Windows set current directory reports conversion failure as invalid input" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    cwd_conversion_fails = true;
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, setCwd(&state, "C:\\work".ptr, 7));
    try std.testing.expectEqual(@as(usize, 0), cwd_set_calls);
}

test "Windows set current directory frees converted paths after native failure" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    cwd_set_result = 0;
    cwd_set_error = 87;
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, setCwd(&state, "bad".ptr, 3));
    try std.testing.expectEqual(@as(usize, 1), cwd_free_calls);
}

test "Windows set current directory rejects input beyond the conversion limit" {
    var state: PalState = .{};
    resetCwdSetTest(&state);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, setCwd(&state, "x".ptr, 32768));
    try std.testing.expectEqual(@as(usize, 0), cwd_alloc_calls);
}

test "Windows environment enumeration reports required capacity at zero bytes" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = testGetProcAddress;
    state.p_wide_to_mb = testWideToMultiByte;
    test_env_free_calls = 0;
    var out: [1]u8 = .{0xa5};
    const result = getEnv(&state, &out, 0);
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(result >> 32)));
    try std.testing.expectEqual(@as(u32, 8), @as(u32, @truncate(result)));
    try std.testing.expectEqual(@as(usize, 1), test_env_free_calls);
    try std.testing.expectEqual(@as(u8, 0xa5), out[0]);
}

test "Windows environment enumeration skips entries that cannot be converted" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = testGetProcAddress;
    state.p_wide_to_mb = testWideToMultiByte;
    test_env_override = &[_]u16{ 'A', '=', '1', 0, 0x100, '=', 'x', 0, 0 };
    defer test_env_override = null;
    var out: [8]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0x1_0000_0004), getEnv(&state, &out, out.len));
    try std.testing.expectEqualSlices(u8, "A=1\x00", out[0..4]);
}

test "Windows current directory rejects an unrepresentable UTF-16 path" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    state.p_wide_to_mb = testWideToMultiByte;
    test_cwd_wide = @splat(0);
    test_cwd_wide[0] = 0x100;
    test_cwd_length = 1;
    var out: [4]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, &out, out.len));
    try expectFilled(&out, 0xa5);
}

test "Windows current directory handles a path exactly at the API buffer bound" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = cwdTestLookup;
    test_cwd_length = 1023;
    var out: [1]u8 = .{0xa5};
    try std.testing.expectEqual(@as(u64, 0), getCwd(&state, &out, out.len));
}

test "Windows platform information accepts an exactly sized destination" {
    var state: PalState = .{};
    var info: pal_abi.AbiPlatformInfo = .{};
    try std.testing.expectEqual(@as(u32, 0), getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info))));
    try std.testing.expectEqual(pal_abi.OS_KIND_WINDOWS, info.os_kind);
}

test "Windows platform information populates the optional profile directory" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "GetEnvironmentVariableW")) {
                return @ptrCast(@constCast(&getUserProfile));
            }
            return null;
        }
        fn getUserProfile(_: [*:0]const u16, out: [*]u16, _: u32) callconv(win.win_cc) u32 {
            const value = "C:\\Users\\tester";
            for (value, 0..) |byte, i| out[i] = byte;
            return value.len;
        }
    }.lookup;
    state.p_wide_to_mb = testWideToMultiByte;
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    try std.testing.expectEqualStrings("C:\\Users\\tester", std.mem.sliceTo(&info.user_home_dir, 0));
}

test "Windows platform information populates the optional temporary directory" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "GetTempPathW")) {
                return @ptrCast(@constCast(&getTempPath));
            }
            return null;
        }
        fn getTempPath(_: u32, out: [*]u16) callconv(win.win_cc) u32 {
            const value = "C:\\Temp\\";
            for (value, 0..) |byte, i| out[i] = byte;
            return value.len;
        }
    }.lookup;
    state.p_wide_to_mb = testWideToMultiByte;
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    try std.testing.expectEqualStrings("C:\\Temp\\", std.mem.sliceTo(&info.temp_dir, 0));
}

test "Windows platform information ignores an overlong home directory result" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "GetEnvironmentVariableW")) {
                return @ptrCast(@constCast(&tooLong));
            }
            return null;
        }
        fn tooLong(_: [*:0]const u16, _: [*]u16, _: u32) callconv(win.win_cc) u32 {
            return 260;
        }
    }.lookup;
    state.p_wide_to_mb = testWideToMultiByte;
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    try std.testing.expectEqual(@as(u8, 0), info.user_home_dir[0]);
}

test "Windows platform information ignores a zero-length temporary directory result" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "GetTempPathW")) {
                return @ptrCast(@constCast(&empty));
            }
            return null;
        }
        fn empty(_: u32, _: [*]u16) callconv(win.win_cc) u32 {
            return 0;
        }
    }.lookup;
    state.p_wide_to_mb = testWideToMultiByte;
    var info: pal_abi.AbiPlatformInfo = .{};
    _ = getPlatformInfo(&state, @ptrCast(&info), @sizeOf(@TypeOf(info)));
    try std.testing.expectEqual(@as(u8, 0), info.temp_dir[0]);
}

test "Windows environment time clamps a tick before the epoch to zero" {
    var state: PalState = .{};
    state.p_get_system_time_ft = struct {
        fn read(out: *win.FileTime) callconv(win.win_cc) void {
            const raw_time: u64 = 116_444_736_000_000_000 - 1;
            out.* = .{ .low = @truncate(raw_time), .high = @truncate(raw_time >> 32) };
        }
    }.read;
    try std.testing.expectEqual(@as(u64, 0), getTime(&state));
}

test "Windows environment time converts a single second after epoch" {
    var state: PalState = .{};
    state.p_get_system_time_ft = struct {
        fn read(out: *win.FileTime) callconv(win.win_cc) void {
            const raw_time: u64 = 116_444_736_000_000_000 + 10_000_000;
            out.* = .{ .low = @truncate(raw_time), .high = @truncate(raw_time >> 32) };
        }
    }.read;
    try std.testing.expectEqual(@as(u64, 1_000_000_000), getTime(&state));
}

test "Windows environment time preserves sub-microsecond precision" {
    var state: PalState = .{};
    state.p_get_system_time_ft = struct {
        fn read(out: *win.FileTime) callconv(win.win_cc) void {
            const raw_time: u64 = 116_444_736_000_000_042;
            out.* = .{ .low = @truncate(raw_time), .high = @truncate(raw_time >> 32) };
        }
    }.read;
    try std.testing.expectEqual(@as(u64, 4_200), getTime(&state));
}

test "Windows environment time reads the native FILETIME once per request" {
    time_read_calls = 0;
    var state: PalState = .{};
    state.p_get_system_time_ft = testCountedSystemTime;
    try std.testing.expectEqual(@as(u64, 0), getTime(&state));
    try std.testing.expectEqual(@as(usize, 1), time_read_calls);
}

test "Windows environment arguments report zero when argc has no argv" {
    var state: PalState = .{};
    state.host.argc = 2;
    var out: [4]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getArgs(&state, &out, out.len));
    try expectFilled(&out, 0xa5);
}

test "Windows environment arguments copy multibyte UTF-8 bytes unchanged" {
    const arg = [_:0]u8{ 0xc3, 0xa9 };
    const args = [_][*:0]const u8{&arg};
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;
    var out: [3]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0x1_0000_0003), getArgs(&state, &out, out.len));
    try std.testing.expectEqualSlices(u8, &.{ 0xc3, 0xa9, 0 }, &out);
}

test "Windows environment enumeration returns zero when no conversion API is present" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = testGetProcAddress;
    var out: [8]u8 = @splat(0xa5);
    test_env_free_calls = 0;
    try std.testing.expectEqual(@as(u64, 0), getEnv(&state, &out, out.len));
    try std.testing.expectEqual(@as(usize, 1), test_env_free_calls);
    try expectFilled(&out, 0xa5);
}

test "Windows environment enumeration returns zero when proc lookup is unavailable" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    var out: [8]u8 = @splat(0xa5);
    try std.testing.expectEqual(@as(u64, 0), getEnv(&state, &out, out.len));
}
