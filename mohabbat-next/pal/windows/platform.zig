const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");
const win = @import("win_ffi.zig");
const mem = @import("mem.zig");
const env_time = @import("env_time.zig");
const fs_ops = @import("fs_ops.zig");
const net_ops = @import("net_ops.zig");
const proc_tty = @import("proc_tty.zig");

const PalState = mem.PalState;

inline fn sliceOrigin() [*]u8 {
    const addr = switch (builtin.cpu.arch) {
        .x86_64 => asm ("lea windows_platform_entry(%rip), %[ret]"
            : [ret] "=r" (-> usize),
        ),
        .aarch64 => asm ("adr %[ret], windows_platform_entry"
            : [ret] "=r" (-> usize),
        ),
        else => @compileError("Unsupported arch"),
    };
    return @ptrFromInt(addr);
}

inline fn hostPtr() *const win.WindowsPlatformHost {
    return @ptrCast(@alignCast(sliceOrigin() - pal_abi.PREFIX_ABI_OFFSET));
}

inline fn statePtr() *PalState {
    return @ptrCast(@alignCast(sliceOrigin() - pal_abi.PREFIX_GLOBALS_OFFSET));
}

fn pageAlign(n: usize) usize {
    return (n + 4095) & ~@as(usize, 4095);
}

// ─── PalAbiTable Implementations ─────────────────────────────────────────────

fn abiAllocMem(size: usize, _: usize, flags: u32) callconv(win.win_cc) ?[*]u8 {
    const s = statePtr();
    const va = s.p_virtual_alloc orelse return null;
    const prot: u32 = if (flags == pal_abi.MEM_CODE_EXEC_RX) 0x20 else if (flags == pal_abi.MEM_CODE_JIT_RW) 0x04 else 0x04;
    // MEM_COMMIT | MEM_RESERVE = 0x3000
    return va(null, pageAlign(@max(size, 1)), 0x3000, prot);
}

fn abiProtectMem(ptr: [*]u8, size: usize, flags: u32) callconv(win.win_cc) i32 {
    const s = statePtr();
    const vp = s.p_virtual_protect orelse return -1;
    const prot: u32 = if (flags == pal_abi.MEM_CODE_EXEC_RX) 0x20 else 0x04;
    var old: u32 = 0;
    return if (vp(ptr, pageAlign(@max(size, 1)), prot, &old) != 0) 0 else -1;
}

fn abiFreeMem(ptr: [*]u8, _: usize) callconv(win.win_cc) void {
    const s = statePtr();
    if (s.p_virtual_free) |vf| {
        _ = vf(ptr, 0, 0x8000); // MEM_RELEASE
    }
}

fn abiFlushIcache(ptr: [*]const u8, size: usize) callconv(win.win_cc) void {
    const s = statePtr();
    if (s.p_flush_icache) |fi| {
        if (s.p_get_current_process) |gcp| {
            _ = fi(gcp(), ptr, size);
        }
    }
}

fn abiHostPanic(msg: [*]const u8, len: usize) callconv(win.win_cc) noreturn {
    const s = statePtr();
    if (s.p_write_file) |wf| {
        var written: u32 = 0;
        _ = wf(s.stderr_h, msg, @intCast(@min(len, 0x7fff_ffff)), &written, null);
    }
    if (s.p_exit_process) |ep| ep(99);
    while (true) {}
}

fn abiRusticatedDebug(val: i32) callconv(win.win_cc) void {
    _ = val;
}

fn abiProcessExit(code: i32) callconv(win.win_cc) noreturn {
    const s = statePtr();
    proc_tty.ttySetMode(s, 0, 0);
    if (s.p_exit_process) |ep| ep(code);
    while (true) {}
}

fn abiGetTime() callconv(win.win_cc) u64 {
    return env_time.getTime(statePtr());
}

fn abiGetRandom(buf: [*]u8, len: usize) callconv(win.win_cc) void {
    env_time.getRandom(statePtr(), buf, len);
}

fn abiGetArgs(buf: [*]u8, cap: usize) callconv(win.win_cc) u64 {
    return env_time.getArgs(statePtr(), buf, cap);
}

fn abiGetEnv(buf: [*]u8, cap: usize) callconv(win.win_cc) u64 {
    return env_time.getEnv(statePtr(), buf, cap);
}

fn abiGetCwd(buf: [*]u8, cap: usize) callconv(win.win_cc) u64 {
    return env_time.getCwd(statePtr(), buf, cap);
}

fn abiSetCwd(path: [*]const u8, len: usize) callconv(win.win_cc) u32 {
    return env_time.setCwd(statePtr(), path, len);
}

fn abiGetPlatformInfo(out: [*]u8, cap: usize) callconv(win.win_cc) u32 {
    return env_time.getPlatformInfo(statePtr(), out, cap);
}

fn abiTimerSet(ov: *pal_abi.Overlapped, delay_ms: u32) callconv(win.win_cc) void {
    if (delay_ms == 0) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }
    const s = statePtr();
    const op = mem.allocOp(s) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    const now = if (s.p_get_tick_count64) |gtc| gtc() else 0;
    op.kind = .timer;
    op.guest_ov = ov;
    op.deadline_ms = now + delay_ms;
}

fn abiCancel(ov: *pal_abi.Overlapped) callconv(win.win_cc) void {
    const s = statePtr();
    for (&s.ops) |*op| {
        if (op.used and op.guest_ov == ov) {
            op.canceled = true;
            if (op.kind == .timer) {
                const gov = op.guest_ov.?;
                mem.freeOp(s, op);
                mem.completeInline(gov, pal_abi.ERROR_CANCELED, 0, 0);
            }
            return;
        }
    }
}

fn stdHandleReadWorker(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
    const op: *mem.PendingOp = @ptrCast(@alignCast(ctx.?));
    const s = statePtr();
    const rf = s.p_read_file orelse return 0;
    const pq = s.p_post_queued orelse return 0;
    var n: u32 = 0;
    const ok = rf(s.stdin_h, op.buf, @intCast(@min(op.len, 0x7fff_ffff)), &n, null);
    if (ok == 0) {
        const err = if (s.p_get_last_error) |gle| gle() else 109;
        if (err == 109 or err == 232) {
            op.win_ov.internal = 0;
            n = 0;
        } else {
            op.win_ov.internal = win.mapWinError(err);
        }
    } else {
        op.win_ov.internal = 0;
    }
    _ = pq(s.iocp, n, @intFromPtr(op), &op.win_ov);
    return 0;
}

fn abiRead(ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) callconv(win.win_cc) void {
    const s = statePtr();
    if (handle == 0) {
        if (len == 0) {
            mem.completeInline(ov, 0, 0, 0);
            return;
        }
        const op = mem.allocOp(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .read;
        op.guest_ov = ov;
        op.handle_token = handle;
        op.buf = buf;
        op.len = len;
        if (s.p_create_thread) |ct| {
            const th = ct(null, 0, stdHandleReadWorker, op, 0, null);
            if (th != null) {
                if (s.p_close_handle) |ch| _ = ch(th);
                return;
            }
        }
        mem.freeOp(s, op);
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    }

    const he = mem.findHandle(s, handle) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_socket and he.access_mode == 1) {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    }
    if (he.is_socket) {
        if (mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "recv")) |p_recv| {
            const RecvFn = *const fn (usize, [*]u8, i32, i32) callconv(win.win_cc) i32;
            const recv_fn: RecvFn = @ptrCast(@alignCast(p_recv));
            const n = recv_fn(@intFromPtr(he.handle), buf, @intCast(@min(len, 0x7fff_ffff)), 0);
            if (n < 0) {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            } else {
                mem.completeInline(ov, 0, 0, @intCast(n));
            }
            return;
        }
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    }

    const rf = s.p_read_file orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const positional = (ov.flags & pal_abi.READ_FLAG_POSITIONAL) != 0;
    var saved_pos: i64 = 0;
    if (positional) {
        const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
        const off = trailing.*;
        if (off < 0) {
            mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        if (s.p_set_file_pointer_ex) |sfp| {
            _ = sfp(he.handle, 0, &saved_pos, 1);
            if (sfp(he.handle, off, null, 0) == 0) {
                const err = if (s.p_get_last_error) |gle| win.mapWinError(gle()) else pal_abi.ERROR_IO;
                mem.completeInline(ov, err, 0, 0);
                return;
            }
        }
    }
    var n: u32 = 0;
    const ok = rf(he.handle, buf, @intCast(@min(len, 0x7fff_ffff)), &n, null);
    var out_err: u32 = 0;
    if (ok == 0) {
        const gle_code = if (s.p_get_last_error) |gle| gle() else 0;
        if (gle_code == 38 or gle_code == 109) { // ERROR_HANDLE_EOF / ERROR_BROKEN_PIPE
            n = 0;
            out_err = 0;
        } else {
            out_err = win.mapWinError(gle_code);
        }
    }
    if (positional) {
        if (s.p_set_file_pointer_ex) |sfp| _ = sfp(he.handle, saved_pos, null, 0);
    }
    mem.completeInline(ov, out_err, 0, n);
}

fn abiWrite(ov: *pal_abi.Overlapped, handle: u64, buf: [*]const u8, len: usize) callconv(win.win_cc) void {
    const s = statePtr();
    if (handle == 1 or handle == 2) {
        const h = if (handle == 1) s.stdout_h else s.stderr_h;
        const wf = s.p_write_file orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        var n: u32 = 0;
        if (wf(h, buf, @intCast(@min(len, 0x7fff_ffff)), &n, null) == 0) {
            const err = if (s.p_get_last_error) |gle| win.mapWinError(gle()) else pal_abi.ERROR_IO;
            mem.completeInline(ov, err, 0, 0);
            return;
        }
        mem.completeInline(ov, 0, 0, n);
        return;
    }

    const he = mem.findHandle(s, handle) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_socket and he.access_mode == 0) {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    }
    if (he.is_socket) {
        if (mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "send")) |p_send| {
            const SendFn = *const fn (usize, [*]const u8, i32, i32) callconv(win.win_cc) i32;
            const send_fn: SendFn = @ptrCast(@alignCast(p_send));
            const n = send_fn(@intFromPtr(he.handle), buf, @intCast(@min(len, 0x7fff_ffff)), 0);
            if (n < 0) {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            } else {
                mem.completeInline(ov, 0, 0, @intCast(n));
            }
            return;
        }
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    }

    const wf = s.p_write_file orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    if (he.append_mode) {
        if (s.p_set_file_pointer_ex) |sfp| {
            _ = sfp(he.handle, 0, null, 2); // FILE_END
        }
    }
    var n: u32 = 0;
    if (wf(he.handle, buf, @intCast(@min(len, 0x7fff_ffff)), &n, null) == 0) {
        const err = if (s.p_get_last_error) |gle| win.mapWinError(gle()) else pal_abi.ERROR_IO;
        mem.completeInline(ov, err, 0, 0);
        return;
    }
    mem.completeInline(ov, 0, 0, n);
}

fn abiSeek(ov: *pal_abi.Overlapped, handle: u64, off: i64, whence: u32) callconv(win.win_cc) void {
    fs_ops.seek(statePtr(), ov, handle, off, whence);
}

fn abiHandleTruncate(ov: *pal_abi.Overlapped, handle: u64, size: i64) callconv(win.win_cc) void {
    fs_ops.handleTruncate(statePtr(), ov, handle, size);
}

fn abiHandleClose(handle: u64) callconv(win.win_cc) void {
    const s = statePtr();
    const he = mem.findHandle(s, handle) orelse return;
    if (he.is_dir) {
        if (he.dir_find != win.INVALID_HANDLE_VALUE) {
            if (mem.resolveKernel32(s, "FindClose")) |p_fc| {
                const FindCloseFn = *const fn (win.HANDLE) callconv(win.win_cc) i32;
                const fc: FindCloseFn = @ptrCast(@alignCast(p_fc));
                _ = fc(he.dir_find);
            }
        }
        if (he.dir_path_w) |p| {
            if (s.host.free) |free| free(@ptrCast(@constCast(p)));
        }
    } else if (he.is_socket) {
        if (mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "closesocket")) |p_cs| {
            const CloseSockFn = *const fn (usize) callconv(win.win_cc) i32;
            const cs: CloseSockFn = @ptrCast(@alignCast(p_cs));
            _ = cs(@intFromPtr(he.handle));
        }
    } else if (he.handle != null and he.handle != win.INVALID_HANDLE_VALUE) {
        if (s.p_close_handle) |ch| _ = ch(he.handle);
    }
    he.* = .{};
}

fn abiPathOpen(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(win.win_cc) void {
    fs_ops.pathOpen(statePtr(), ov, path, len, flags);
}

fn abiDirRead(ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) callconv(win.win_cc) void {
    fs_ops.dirRead(statePtr(), ov, handle, buf, len);
}

fn abiPathStat(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32, out: [*]u8, cap: usize) callconv(win.win_cc) void {
    fs_ops.pathStat(statePtr(), ov, path, len, flags, out, cap);
}

fn abiPathLink(ov: *pal_abi.Overlapped, src: [*]const u8, s_len: usize, dst: [*]const u8, d_len: usize, flags: u32) callconv(win.win_cc) void {
    fs_ops.pathLink(statePtr(), ov, src, s_len, dst, d_len, flags);
}

fn abiPathSymlink(ov: *pal_abi.Overlapped, target: [*]const u8, t_len: usize, link: [*]const u8, l_len: usize, kind: u32) callconv(win.win_cc) void {
    fs_ops.pathSymlink(statePtr(), ov, target, t_len, link, l_len, kind);
}

fn abiPathReadlink(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: [*]u8, _: usize) callconv(win.win_cc) void {
    mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}

fn abiPathChmod(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: u32) callconv(win.win_cc) void {
    mem.completeInline(ov, 0, 0, 0);
}

fn abiPathSetTimes(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: i64, _: u32, _: i64, _: u32, _: u32) callconv(win.win_cc) void {
    mem.completeInline(ov, 0, 0, 0);
}

fn abiPathRemove(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(win.win_cc) void {
    fs_ops.pathRemove(statePtr(), ov, path, len, flags);
}

fn abiPathMkdir(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(win.win_cc) void {
    fs_ops.pathMkdir(statePtr(), ov, path, len, mode);
}

fn abiPathRename(ov: *pal_abi.Overlapped, old_p: [*]const u8, o_len: usize, new_p: [*]const u8, n_len: usize) callconv(win.win_cc) void {
    fs_ops.pathRename(statePtr(), ov, old_p, o_len, new_p, n_len);
}

fn abiNetOpen(ov: *pal_abi.Overlapped, addr: [*]const u8, len: usize, port: u32, flags: u32) callconv(win.win_cc) void {
    net_ops.netOpen(statePtr(), ov, addr, len, port, flags);
}

fn abiNetAccept(ov: *pal_abi.Overlapped, listener: u64) callconv(win.win_cc) void {
    net_ops.netAccept(statePtr(), ov, listener);
}

fn abiNetLookup(ov: *pal_abi.Overlapped, name: [*]const u8, len: usize, out: [*]u8, cap: usize) callconv(win.win_cc) void {
    net_ops.netLookup(statePtr(), ov, name, len, out, cap);
}

fn abiNetCertVerify(ov: *pal_abi.Overlapped, name: [*]const u8, n_len: usize, chain: [*]const u8, c_len: usize) callconv(win.win_cc) void {
    net_ops.netCertVerify(statePtr(), ov, name, n_len, chain, c_len);
}

fn abiProcessSpawn(ov: *pal_abi.Overlapped, cfg: [*]const u8, len: usize) callconv(win.win_cc) void {
    proc_tty.processSpawn(statePtr(), ov, cfg, len);
}

fn abiProcessPipe(ov: *pal_abi.Overlapped) callconv(win.win_cc) void {
    proc_tty.processPipe(statePtr(), ov);
}

fn abiProcessWait(ov: *pal_abi.Overlapped, proc_handle: u64) callconv(win.win_cc) void {
    proc_tty.processWait(statePtr(), ov, proc_handle);
}

fn abiProcessSignal(proc_handle: u64, signum: u32) callconv(win.win_cc) void {
    proc_tty.processSignal(statePtr(), proc_handle, signum);
}

fn abiSignalWait(ov: *pal_abi.Overlapped, _: u32) callconv(win.win_cc) void {
    mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}

fn abiTtySetMode(handle: u64, raw_mode: u32) callconv(win.win_cc) void {
    proc_tty.ttySetMode(statePtr(), handle, raw_mode);
}

fn abiTtyGetSize(handle: u64) callconv(win.win_cc) u32 {
    return proc_tty.ttyGetSize(statePtr(), handle);
}

fn abiTtySetSize(_: u64, _: u32, _: u32) callconv(win.win_cc) u32 {
    return 0;
}

fn abiFdIsatty(handle: u32) callconv(win.win_cc) u32 {
    return proc_tty.fdIsatty(statePtr(), handle);
}

fn abiSystemInfoWait(ov: *pal_abi.Overlapped) callconv(win.win_cc) void {
    mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}

// ─── PAL Entry & IOCP Event Loop ─────────────────────────────────────────────

fn initKernel32(s: *PalState) bool {
    const ll = s.host.load_library orelse return false;
    s.kernel32 = ll("kernel32.dll") orelse return false;
    s.p_get_std_handle = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetStdHandle") orelse return false));
    s.p_read_file = @ptrCast(@alignCast(mem.resolveKernel32(s, "ReadFile") orelse return false));
    s.p_write_file = @ptrCast(@alignCast(mem.resolveKernel32(s, "WriteFile") orelse return false));
    s.p_get_current_process = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetCurrentProcess") orelse return false));
    s.p_get_last_error = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetLastError") orelse return false));
    s.p_flush_icache = @ptrCast(@alignCast(mem.resolveKernel32(s, "FlushInstructionCache") orelse return false));
    s.p_exit_process = @ptrCast(@alignCast(mem.resolveKernel32(s, "ExitProcess") orelse return false));
    s.p_virtual_alloc = @ptrCast(@alignCast(mem.resolveKernel32(s, "VirtualAlloc") orelse return false));
    s.p_virtual_protect = @ptrCast(@alignCast(mem.resolveKernel32(s, "VirtualProtect") orelse return false));
    s.p_virtual_free = @ptrCast(@alignCast(mem.resolveKernel32(s, "VirtualFree") orelse return false));
    s.p_close_handle = @ptrCast(@alignCast(mem.resolveKernel32(s, "CloseHandle") orelse return false));
    s.p_create_file_w = @ptrCast(@alignCast(mem.resolveKernel32(s, "CreateFileW") orelse return false));
    s.p_set_file_pointer_ex = @ptrCast(@alignCast(mem.resolveKernel32(s, "SetFilePointerEx") orelse return false));
    s.p_set_end_of_file = @ptrCast(@alignCast(mem.resolveKernel32(s, "SetEndOfFile") orelse return false));
    s.p_get_file_size_ex = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetFileSizeEx") orelse return false));
    s.p_mb_to_wide = @ptrCast(@alignCast(mem.resolveKernel32(s, "MultiByteToWideChar") orelse return false));
    s.p_wide_to_mb = @ptrCast(@alignCast(mem.resolveKernel32(s, "WideCharToMultiByte") orelse return false));
    s.p_get_tick_count64 = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetTickCount64") orelse return false));
    s.p_get_system_time_ft = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetSystemTimePreciseAsFileTime") orelse mem.resolveKernel32(s, "GetSystemTimeAsFileTime") orelse return false));
    s.p_sleep = @ptrCast(@alignCast(mem.resolveKernel32(s, "Sleep") orelse return false));
    s.p_create_iocp = @ptrCast(@alignCast(mem.resolveKernel32(s, "CreateIoCompletionPort") orelse return false));
    s.p_get_queued_ex = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetQueuedCompletionStatusEx") orelse return false));
    s.p_post_queued = @ptrCast(@alignCast(mem.resolveKernel32(s, "PostQueuedCompletionStatus") orelse return false));
    s.p_create_thread = @ptrCast(@alignCast(mem.resolveKernel32(s, "CreateThread") orelse return false));
    s.p_get_file_type = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetFileType") orelse return false));

    const gsh = s.p_get_std_handle.?;
    s.stdin_h = gsh(@bitCast(@as(i32, -10)));
    s.stdout_h = gsh(@bitCast(@as(i32, -11)));
    s.stderr_h = gsh(@bitCast(@as(i32, -12)));
    s.iocp = s.p_create_iocp.?(win.INVALID_HANDLE_VALUE, null, 0, 1);
    return s.iocp != null;
}

pub fn windowsPlatformMain() callconv(win.win_cc) i32 {
    const s = statePtr();
    s.* = .{};
    s.host = hostPtr().*;
    if (!initKernel32(s)) return 10;

    const payload_ptr = s.host.hello_payload orelse return 11;
    const payload_len = s.host.hello_payload_size;
    if (payload_len == 0) return 12;

    const va = s.p_virtual_alloc.?;
    const vp = s.p_virtual_protect.?;
    const code_aligned = pageAlign(payload_len);
    const total_size = pal_abi.PREFIX_ABI_OFFSET + code_aligned;
    const mem_base = va(null, total_size, 0x3000, 0x04) orelse return 13; // PAGE_READWRITE

    const table: *pal_abi.PalAbiTable = @ptrCast(@alignCast(mem_base));
    table.* = .{
        .wasm_bytes = s.host.wasm_payload,
        .wasm_len = s.host.wasm_payload_size,
        .alloc_mem = abiAllocMem,
        .protect_mem = abiProtectMem,
        .free_mem = abiFreeMem,
        .flush_icache = abiFlushIcache,
        .host_panic = abiHostPanic,
        .rusticated_debug = abiRusticatedDebug,
        .process_exit = abiProcessExit,
        .get_time = abiGetTime,
        .get_random = abiGetRandom,
        .get_args = abiGetArgs,
        .get_env = abiGetEnv,
        .get_cwd = abiGetCwd,
        .set_cwd = abiSetCwd,
        .get_platform_info = abiGetPlatformInfo,
        .timer_set = abiTimerSet,
        .cancel = abiCancel,
        .read = abiRead,
        .write = abiWrite,
        .seek = abiSeek,
        .handle_truncate = abiHandleTruncate,
        .handle_close = abiHandleClose,
        .path_open = abiPathOpen,
        .dir_read = abiDirRead,
        .path_stat = abiPathStat,
        .path_link = abiPathLink,
        .path_symlink = abiPathSymlink,
        .path_readlink = abiPathReadlink,
        .path_chmod = abiPathChmod,
        .path_set_times = abiPathSetTimes,
        .path_remove = abiPathRemove,
        .path_mkdir = abiPathMkdir,
        .path_rename = abiPathRename,
        .net_open = abiNetOpen,
        .net_accept = abiNetAccept,
        .net_lookup = abiNetLookup,
        .net_cert_verify = abiNetCertVerify,
        .process_spawn = abiProcessSpawn,
        .process_pipe = abiProcessPipe,
        .process_wait = abiProcessWait,
        .process_signal = abiProcessSignal,
        .signal_wait = abiSignalWait,
        .tty_set_mode = abiTtySetMode,
        .tty_get_size = abiTtyGetSize,
        .tty_set_size = abiTtySetSize,
        .fd_isatty = abiFdIsatty,
        .system_info_wait = abiSystemInfoWait,
    };

    const code_ptr = mem_base + pal_abi.PREFIX_ABI_OFFSET;
    @memcpy(code_ptr[0..payload_len], payload_ptr[0..payload_len]);
    var old_prot: u32 = 0;
    if (vp(code_ptr, code_aligned, 0x20, &old_prot) == 0) return 14; // PAGE_EXECUTE_READ
    abiFlushIcache(code_ptr, payload_len);

    const RuntimeEntry = *const fn () callconv(win.win_cc) void;
    const entry: RuntimeEntry = @ptrCast(@alignCast(code_ptr));

    while (true) {
        entry();
        if (s.pending_count == 0) break;

        // Compute earliest timer deadline
        const now = if (s.p_get_tick_count64) |gtc| gtc() else 0;
        var timeout_ms: u32 = 0xffff_ffff;
        var fired_timer = false;
        for (&s.ops) |*op| {
            if (op.used and op.kind == .timer) {
                if (op.deadline_ms <= now) {
                    const gov = op.guest_ov.?;
                    mem.freeOp(s, op);
                    mem.completeInline(gov, 0, 0, 0);
                    fired_timer = true;
                } else {
                    const rem: u32 = @intCast(@min(op.deadline_ms - now, 0xffff_fffe));
                    if (rem < timeout_ms) timeout_ms = rem;
                }
            }
        }
        if (fired_timer) continue;

        var entries: [32]win.OverlappedEntry = @splat(.{});
        var removed: u32 = 0;
        if (s.p_get_queued_ex.?(s.iocp, &entries, entries.len, &removed, timeout_ms, 0) != 0) {
            for (entries[0..removed]) |e| {
                if (e.completion_key != 0) {
                    const op: *mem.PendingOp = @ptrFromInt(e.completion_key);
                    if (op.used) {
                        const gov = op.guest_ov.?;
                        const err: u32 = if (op.canceled) pal_abi.ERROR_CANCELED else @intCast(op.win_ov.internal);
                        mem.freeOp(s, op);
                        mem.completeInline(gov, err, 0, e.bytes_transferred);
                    }
                }
            }
        }
    }
    return 0;
}

comptime {
    if (@sizeOf(PalState) > pal_abi.PREFIX_GLOBALS_OFFSET) {
        @compileError("PalState exceeds PREFIX_GLOBALS_OFFSET (64 KiB)");
    }
    if (!builtin.is_test) {
        @export(&windowsPlatformMain, .{ .name = "windows_platform_main" });
        switch (builtin.cpu.arch) {
            .x86_64 => asm (
                \\.section .text.windows_platform_entry,"ax"
                \\.globl windows_platform_entry
                \\windows_platform_entry:
                \\jmp windows_platform_main
            ),
            .aarch64 => asm (
                \\.section .text.windows_platform_entry,"ax"
                \\.globl windows_platform_entry
                \\windows_platform_entry:
                \\b windows_platform_main
            ),
            else => @compileError("Unsupported arch"),
        }
        const Intrinsics = struct {
            fn _memcpy(dest: [*]u8, src: [*]const u8, len: usize) callconv(.c) [*]u8 {
                for (0..len) |i| dest[i] = src[i];
                return dest;
            }
            fn _memset(dest: [*]u8, value: c_int, len: usize) callconv(.c) [*]u8 {
                const b: u8 = @truncate(@as(c_uint, @bitCast(value)));
                for (0..len) |i| dest[i] = b;
                return dest;
            }
            fn _memmove(dest: [*]u8, src: [*]const u8, len: usize) callconv(.c) [*]u8 {
                const d = @intFromPtr(dest);
                const s = @intFromPtr(src);
                if (d <= s or d >= s + len) {
                    for (0..len) |i| dest[i] = src[i];
                } else {
                    var i = len;
                    while (i > 0) {
                        i -= 1;
                        dest[i] = src[i];
                    }
                }
                return dest;
            }
            fn _memcmp(left: [*]const u8, right: [*]const u8, len: usize) callconv(.c) c_int {
                for (0..len) |i| {
                    if (left[i] != right[i]) return @as(c_int, left[i]) - @as(c_int, right[i]);
                }
                return 0;
            }
            fn _strlen(str: [*:0]const u8) callconv(.c) usize {
                var i: usize = 0;
                while (str[i] != 0) : (i += 1) {}
                return i;
            }
        };
        @export(&Intrinsics._memcpy, .{ .name = "memcpy" });
        @export(&Intrinsics._memset, .{ .name = "memset" });
        @export(&Intrinsics._memmove, .{ .name = "memmove" });
        @export(&Intrinsics._memcmp, .{ .name = "memcmp" });
        @export(&Intrinsics._memcmp, .{ .name = "bcmp" });
        @export(&Intrinsics._strlen, .{ .name = "strlen" });
    }
}

test "windows PAL state and error mapping" {
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, win.mapWinError(2));
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, win.mapWinError(995));
    try std.testing.expectEqual(@as(usize, 80), @sizeOf(win.WindowsPlatformHost));
}
