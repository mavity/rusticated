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
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wpath)));
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
    const bytes = std.mem.asBytes(&info);
    @memcpy(out[0..bytes.len], bytes);
    return 0;
}
