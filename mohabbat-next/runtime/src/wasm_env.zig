const std = @import("std");
const zwasm = @import("zwasm");
const pal_abi = @import("pal_abi");
const pal_bridge = @import("pal_bridge.zig");

fn guestSlice(caller: *zwasm.Caller, ptr_i32: i32, len_i32: i32) ?[]u8 {
    const mem_obj = caller.memory() orelse return null;
    const mem = mem_obj.slice();
    if (ptr_i32 < 0 or len_i32 < 0) return null;
    const off: usize = @intCast(ptr_i32);
    const len: usize = @intCast(len_i32);
    const end = std.math.add(usize, off, len) catch return null;
    if (end > mem.len) return null;
    return mem[off..end];
}

fn guestSliceChecked(caller: *zwasm.Caller, ptr_i32: i32, len_i32: i32) []u8 {
    return guestSlice(caller, ptr_i32, len_i32) orelse {
        const msg = "rusticated guest memory span out of bounds";
        pal_bridge.abi().host_panic(msg.ptr, msg.len);
    };
}

fn guestOverlapped(caller: *zwasm.Caller, ov_i32: i32) *pal_abi.Overlapped {
    const sl = guestSliceChecked(caller, ov_i32, @sizeOf(pal_abi.Overlapped));
    return @ptrCast(@alignCast(sl.ptr));
}

fn envHostPanic(caller: *zwasm.Caller, msg_ptr: i32, msg_len: i32) void {
    if (guestSlice(caller, msg_ptr, msg_len)) |s| {
        pal_bridge.abi().host_panic(s.ptr, s.len);
    }
    const fallback = "guest panic (invalid span)";
    pal_bridge.abi().host_panic(fallback.ptr, fallback.len);
}

fn envRusticatedDebug(_: *zwasm.Caller, val: i32) void {
    pal_bridge.abi().rusticated_debug(val);
}

fn envProcessExit(_: *zwasm.Caller, code: i32) void {
    pal_bridge.abi().process_exit(code);
}

fn envGetTime(_: *zwasm.Caller) i64 {
    return @bitCast(pal_bridge.abi().get_time());
}

fn envGetRandom(caller: *zwasm.Caller, buf_ptr: i32, buf_len: i32) void {
    const s = guestSliceChecked(caller, buf_ptr, buf_len);
    pal_bridge.abi().get_random(s.ptr, s.len);
}

fn envGetArgs(caller: *zwasm.Caller, buf_ptr: i32, buf_cap: i32) i64 {
    const s = guestSliceChecked(caller, buf_ptr, buf_cap);
    return @bitCast(pal_bridge.abi().get_args(s.ptr, s.len));
}

fn envGetEnv(caller: *zwasm.Caller, buf_ptr: i32, buf_cap: i32) i64 {
    const s = guestSliceChecked(caller, buf_ptr, buf_cap);
    return @bitCast(pal_bridge.abi().get_env(s.ptr, s.len));
}

fn envGetCwd(caller: *zwasm.Caller, buf_ptr: i32, buf_cap: i32) i64 {
    const s = guestSliceChecked(caller, buf_ptr, buf_cap);
    return @bitCast(pal_bridge.abi().get_cwd(s.ptr, s.len));
}

fn envSetCwd(caller: *zwasm.Caller, path_ptr: i32, path_len: i32) i32 {
    const s = guestSliceChecked(caller, path_ptr, path_len);
    return @bitCast(pal_bridge.abi().set_cwd(s.ptr, s.len));
}

fn envGetPlatformInfo(caller: *zwasm.Caller, out_ptr: i32, out_cap: i32) i32 {
    const s = guestSliceChecked(caller, out_ptr, out_cap);
    return @bitCast(pal_bridge.abi().get_platform_info(s.ptr, s.len));
}

fn envTimerSet(caller: *zwasm.Caller, ov_ptr: i32, delay_ms: i32) void {
    pal_bridge.abi().timer_set(guestOverlapped(caller, ov_ptr), @bitCast(delay_ms));
}

fn envCancel(caller: *zwasm.Caller, ov_ptr: i32) void {
    pal_bridge.abi().cancel(guestOverlapped(caller, ov_ptr));
}

fn envRead(caller: *zwasm.Caller, ov_ptr: i32, handle: i64, buf_ptr: i32, buf_len: i32) void {
    const ov = guestOverlapped(caller, ov_ptr);
    if ((ov.flags & pal_abi.READ_FLAG_POSITIONAL) != 0) {
        _ = guestSliceChecked(caller, ov_ptr, @sizeOf(pal_abi.Overlapped) + 8);
    }
    const s = guestSliceChecked(caller, buf_ptr, buf_len);
    pal_bridge.abi().read(ov, @bitCast(handle), s.ptr, s.len);
}

fn envWrite(caller: *zwasm.Caller, ov_ptr: i32, handle: i64, buf_ptr: i32, buf_len: i32) void {
    const s = guestSliceChecked(caller, buf_ptr, buf_len);
    pal_bridge.abi().write(guestOverlapped(caller, ov_ptr), @bitCast(handle), s.ptr, s.len);
}

fn envSeek(caller: *zwasm.Caller, ov_ptr: i32, handle: i64, offset: i64, whence: i32) void {
    pal_bridge.abi().seek(guestOverlapped(caller, ov_ptr), @bitCast(handle), offset, @bitCast(whence));
}

fn envHandleTruncate(caller: *zwasm.Caller, ov_ptr: i32, handle: i64, size: i64) void {
    pal_bridge.abi().handle_truncate(guestOverlapped(caller, ov_ptr), @bitCast(handle), size);
}

fn envHandleClose(_: *zwasm.Caller, handle: i64) void {
    pal_bridge.abi().handle_close(@bitCast(handle));
}

fn envPathOpen(caller: *zwasm.Caller, ov_ptr: i32, path_ptr: i32, path_len: i32, flags: i32) void {
    const s = guestSliceChecked(caller, path_ptr, path_len);
    pal_bridge.abi().path_open(guestOverlapped(caller, ov_ptr), s.ptr, s.len, @bitCast(flags));
}

fn envDirRead(caller: *zwasm.Caller, ov_ptr: i32, handle: i64, buf_ptr: i32, buf_len: i32) void {
    const s = guestSliceChecked(caller, buf_ptr, buf_len);
    pal_bridge.abi().dir_read(guestOverlapped(caller, ov_ptr), @bitCast(handle), s.ptr, s.len);
}

fn envPathStat(caller: *zwasm.Caller, ov_ptr: i32, path_ptr: i32, path_len: i32, flags: i32, out_ptr: i32, out_cap: i32) void {
    const p = guestSliceChecked(caller, path_ptr, path_len);
    const o = guestSliceChecked(caller, out_ptr, out_cap);
    pal_bridge.abi().path_stat(guestOverlapped(caller, ov_ptr), p.ptr, p.len, @bitCast(flags), o.ptr, o.len);
}

fn envPathLink(caller: *zwasm.Caller, ov_ptr: i32, s_ptr: i32, s_len: i32, d_ptr: i32, d_len: i32, flags: i32) void {
    const s = guestSliceChecked(caller, s_ptr, s_len);
    const d = guestSliceChecked(caller, d_ptr, d_len);
    pal_bridge.abi().path_link(guestOverlapped(caller, ov_ptr), s.ptr, s.len, d.ptr, d.len, @bitCast(flags));
}

fn envPathSymlink(caller: *zwasm.Caller, ov_ptr: i32, t_ptr: i32, t_len: i32, l_ptr: i32, l_len: i32, kind: i32) void {
    const t = guestSliceChecked(caller, t_ptr, t_len);
    const l = guestSliceChecked(caller, l_ptr, l_len);
    pal_bridge.abi().path_symlink(guestOverlapped(caller, ov_ptr), t.ptr, t.len, l.ptr, l.len, @bitCast(kind));
}

fn envPathReadlink(caller: *zwasm.Caller, ov_ptr: i32, p_ptr: i32, p_len: i32, o_ptr: i32, o_cap: i32) void {
    const p = guestSliceChecked(caller, p_ptr, p_len);
    const o = guestSliceChecked(caller, o_ptr, o_cap);
    pal_bridge.abi().path_readlink(guestOverlapped(caller, ov_ptr), p.ptr, p.len, o.ptr, o.len);
}

fn envPathChmod(caller: *zwasm.Caller, ov_ptr: i32, p_ptr: i32, p_len: i32, mode: i32) void {
    const p = guestSliceChecked(caller, p_ptr, p_len);
    pal_bridge.abi().path_chmod(guestOverlapped(caller, ov_ptr), p.ptr, p.len, @bitCast(mode));
}

fn envPathSetTimes(caller: *zwasm.Caller, ov_ptr: i32, p_ptr: i32, p_len: i32, at_sec: i64, at_nsec: i32, mt_sec: i64, mt_nsec: i32, flags: i32) void {
    const p = guestSliceChecked(caller, p_ptr, p_len);
    pal_bridge.abi().path_set_times(guestOverlapped(caller, ov_ptr), p.ptr, p.len, at_sec, @bitCast(at_nsec), mt_sec, @bitCast(mt_nsec), @bitCast(flags));
}

fn envPathRemove(caller: *zwasm.Caller, ov_ptr: i32, p_ptr: i32, p_len: i32, flags: i32) void {
    const p = guestSliceChecked(caller, p_ptr, p_len);
    pal_bridge.abi().path_remove(guestOverlapped(caller, ov_ptr), p.ptr, p.len, @bitCast(flags));
}

fn envPathMkdir(caller: *zwasm.Caller, ov_ptr: i32, p_ptr: i32, p_len: i32, mode: i32) void {
    const p = guestSliceChecked(caller, p_ptr, p_len);
    pal_bridge.abi().path_mkdir(guestOverlapped(caller, ov_ptr), p.ptr, p.len, @bitCast(mode));
}

fn envPathRename(caller: *zwasm.Caller, ov_ptr: i32, o_ptr: i32, o_len: i32, n_ptr: i32, n_len: i32) void {
    const o = guestSliceChecked(caller, o_ptr, o_len);
    const n = guestSliceChecked(caller, n_ptr, n_len);
    pal_bridge.abi().path_rename(guestOverlapped(caller, ov_ptr), o.ptr, o.len, n.ptr, n.len);
}

fn envNetOpen(caller: *zwasm.Caller, ov_ptr: i32, a_ptr: i32, a_len: i32, port: i32, flags: i32) void {
    const a = guestSliceChecked(caller, a_ptr, a_len);
    pal_bridge.abi().net_open(guestOverlapped(caller, ov_ptr), a.ptr, a.len, @bitCast(port), @bitCast(flags));
}

fn envNetAccept(caller: *zwasm.Caller, ov_ptr: i32, listener: i64) void {
    pal_bridge.abi().net_accept(guestOverlapped(caller, ov_ptr), @bitCast(listener));
}

fn envNetLookup(caller: *zwasm.Caller, ov_ptr: i32, n_ptr: i32, n_len: i32, o_ptr: i32, o_cap: i32) void {
    const n = guestSliceChecked(caller, n_ptr, n_len);
    const o = guestSliceChecked(caller, o_ptr, o_cap);
    pal_bridge.abi().net_lookup(guestOverlapped(caller, ov_ptr), n.ptr, n.len, o.ptr, o.len);
}

fn envNetCertVerify(caller: *zwasm.Caller, ov_ptr: i32, n_ptr: i32, n_len: i32, c_ptr: i32, c_len: i32) void {
    const n = guestSliceChecked(caller, n_ptr, n_len);
    const c = guestSliceChecked(caller, c_ptr, c_len);
    pal_bridge.abi().net_cert_verify(guestOverlapped(caller, ov_ptr), n.ptr, n.len, c.ptr, c.len);
}

fn envProcessSpawn(caller: *zwasm.Caller, ov_ptr: i32, cfg_ptr: i32, cfg_len: i32) void {
    _ = guestSliceChecked(caller, ov_ptr, @sizeOf(pal_abi.ProcessSpawnOverlapped));
    const cfg = guestSliceChecked(caller, cfg_ptr, cfg_len);
    pal_bridge.abi().process_spawn(guestOverlapped(caller, ov_ptr), cfg.ptr, cfg.len);
}

fn envProcessPipe(caller: *zwasm.Caller, ov_ptr: i32) void {
    pal_bridge.abi().process_pipe(guestOverlapped(caller, ov_ptr));
}

fn envProcessWait(caller: *zwasm.Caller, ov_ptr: i32, proc_handle: i64) void {
    pal_bridge.abi().process_wait(guestOverlapped(caller, ov_ptr), @bitCast(proc_handle));
}

fn envProcessSignal(_: *zwasm.Caller, proc_handle: i64, signum: i32) void {
    pal_bridge.abi().process_signal(@bitCast(proc_handle), @bitCast(signum));
}

fn envSignalWait(caller: *zwasm.Caller, ov_ptr: i32, signum: i32) void {
    pal_bridge.abi().signal_wait(guestOverlapped(caller, ov_ptr), @bitCast(signum));
}

fn envTtySetMode(_: *zwasm.Caller, handle: i64, raw_mode: i32) void {
    pal_bridge.abi().tty_set_mode(@bitCast(handle), @bitCast(raw_mode));
}

fn envTtyGetSize(_: *zwasm.Caller, handle: i64) i32 {
    return @bitCast(pal_bridge.abi().tty_get_size(@bitCast(handle)));
}

fn envTtySetSize(_: *zwasm.Caller, handle: i64, cols: i32, rows: i32) i32 {
    return @bitCast(pal_bridge.abi().tty_set_size(@bitCast(handle), @bitCast(cols), @bitCast(rows)));
}

fn envFdIsatty(_: *zwasm.Caller, handle: i32) i32 {
    return @bitCast(pal_bridge.abi().fd_isatty(@bitCast(handle)));
}

fn envSystemInfoWait(caller: *zwasm.Caller, ov_ptr: i32) void {
    pal_bridge.abi().system_info_wait(guestOverlapped(caller, ov_ptr));
}

fn reg(linker: *zwasm.Linker, name: []const u8, comptime f: anytype) !void {
    try linker.defineFunc("env", name, @TypeOf(f), f);
}

pub fn registerAll(linker: *zwasm.Linker) !void {
    try reg(linker, "host_panic", envHostPanic);
    try reg(linker, "rusticated_debug", envRusticatedDebug);
    try reg(linker, "process_exit", envProcessExit);
    try reg(linker, "get_time", envGetTime);
    try reg(linker, "get_random", envGetRandom);
    try reg(linker, "get_args", envGetArgs);
    try reg(linker, "get_env", envGetEnv);
    try reg(linker, "get_cwd", envGetCwd);
    try reg(linker, "set_cwd", envSetCwd);
    try reg(linker, "get_platform_info", envGetPlatformInfo);
    try reg(linker, "timer_set", envTimerSet);
    try reg(linker, "cancel", envCancel);
    try reg(linker, "read", envRead);
    try reg(linker, "write", envWrite);
    try reg(linker, "seek", envSeek);
    try reg(linker, "handle_truncate", envHandleTruncate);
    try reg(linker, "handle_close", envHandleClose);
    try reg(linker, "path_open", envPathOpen);
    try reg(linker, "dir_read", envDirRead);
    try reg(linker, "path_stat", envPathStat);
    try reg(linker, "path_link", envPathLink);
    try reg(linker, "path_symlink", envPathSymlink);
    try reg(linker, "path_readlink", envPathReadlink);
    try reg(linker, "path_chmod", envPathChmod);
    try reg(linker, "path_set_times", envPathSetTimes);
    try reg(linker, "path_remove", envPathRemove);
    try reg(linker, "path_mkdir", envPathMkdir);
    try reg(linker, "path_rename", envPathRename);
    try reg(linker, "net_open", envNetOpen);
    try reg(linker, "net_accept", envNetAccept);
    try reg(linker, "net_lookup", envNetLookup);
    try reg(linker, "net_cert_verify", envNetCertVerify);
    try reg(linker, "process_spawn", envProcessSpawn);
    try reg(linker, "process_pipe", envProcessPipe);
    try reg(linker, "process_wait", envProcessWait);
    try reg(linker, "process_signal", envProcessSignal);
    try reg(linker, "signal_wait", envSignalWait);
    try reg(linker, "tty_set_mode", envTtySetMode);
    try reg(linker, "tty_get_size", envTtyGetSize);
    try reg(linker, "tty_set_size", envTtySetSize);
    try reg(linker, "fd_isatty", envFdIsatty);
    try reg(linker, "system_info_wait", envSystemInfoWait);
}
