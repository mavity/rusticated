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
            @atomicStore(bool, &op.canceled, true, .release);
            if (op.kind == .timer) {
                const gov = op.guest_ov.?;
                mem.freeOp(s, op);
                mem.completeInline(gov, pal_abi.ERROR_CANCELED, 0, 0);
            } else if (op.native_io) {
                if (mem.findHandle(s, op.handle_token)) |he| {
                    if (s.p_cancel_io_ex) |cancel| _ = cancel(he.handle, &op.win_ov);
                    var transferred: u32 = 0;
                    if (s.p_get_overlapped_result) |get_result| {
                        _ = get_result(he.handle, &op.win_ov, &transferred, 1);
                    }
                }
            } else if (op.worker_handle != null and op.sock_fd == 0 and
                (op.kind == .read or op.kind == .write))
            {
                if (s.p_cancel_sync_io) |cancel| _ = cancel(op.worker_handle);
                if (s.p_wait_single) |wait| _ = wait(op.worker_handle, 0xffff_ffff);
            }
            return;
        }
    }
}

fn stdHandleReadWorker(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
    const op: *mem.PendingOp = @ptrCast(@alignCast(ctx.?));
    const s = statePtr();
    const rf = s.p_read_file orelse {
        op.win_error = pal_abi.ERROR_UNSUPPORTED;
        if (s.p_post_queued) |pq| _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    const pq = s.p_post_queued orelse return 0;
    var n: u32 = 0;
    const ok = rf(s.stdin_h, op.buf, @intCast(@min(op.len, 0x7fff_ffff)), &n, null);
    if (ok == 0) {
        const err = if (s.p_get_last_error) |gle| gle() else 109;
        if (err == 109 or err == 232) {
            op.win_error = 0;
            n = 0;
        } else {
            op.win_error = win.mapWinError(err);
        }
    } else {
        op.win_error = 0;
    }
    _ = pq(s.iocp, n, @intFromPtr(op), &op.win_ov);
    return 0;
}

fn stdHandleWriteWorker(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
    const op: *mem.PendingOp = @ptrCast(@alignCast(ctx.?));
    const s = statePtr();
    const wf = s.p_write_file orelse {
        op.win_error = pal_abi.ERROR_UNSUPPORTED;
        if (s.p_post_queued) |pq| _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    const pq = s.p_post_queued orelse return 0;
    const h = if (op.handle_token == 1) s.stdout_h else s.stderr_h;
    var n: u32 = 0;
    if (wf(h, op.buf, @intCast(@min(op.len, 0x7fff_ffff)), &n, null) == 0) {
        const err = if (s.p_get_last_error) |gle| gle() else 5;
        op.win_error = win.mapWinError(err);
    } else {
        op.win_error = 0;
    }
    _ = pq(s.iocp, n, @intFromPtr(op), &op.win_ov);
    return 0;
}

fn sockRecvWorker(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
    const op: *mem.PendingOp = @ptrCast(@alignCast(ctx.?));
    const s = statePtr();
    const pq = s.p_post_queued orelse return 0;
    const p_recv = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "recv") orelse {
        op.win_error = pal_abi.ERROR_UNSUPPORTED;
        _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    const RecvFn = *const fn (usize, ?[*]u8, i32, i32) callconv(win.win_cc) i32;
    const recv_fn: RecvFn = @ptrCast(@alignCast(p_recv));
    const n = recv_fn(op.sock_fd, op.buf, @intCast(@min(op.len, 0x7fff_ffff)), 0);
    if (n < 0) {
        op.win_error = pal_abi.ERROR_IO;
        _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
    } else {
        op.win_error = 0;
        _ = pq(s.iocp, @intCast(n), @intFromPtr(op), &op.win_ov);
    }
    return 0;
}

fn sockSendWorker(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
    const op: *mem.PendingOp = @ptrCast(@alignCast(ctx.?));
    const s = statePtr();
    const pq = s.p_post_queued orelse return 0;
    const p_send = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "send") orelse {
        op.win_error = pal_abi.ERROR_UNSUPPORTED;
        _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    const SendFn = *const fn (usize, [*]const u8, i32, i32) callconv(win.win_cc) i32;
    const send_fn: SendFn = @ptrCast(@alignCast(p_send));
    const n = send_fn(op.sock_fd, @ptrCast(op.buf.?), @intCast(@min(op.len, 0x7fff_ffff)), 0);
    if (n < 0) {
        op.win_error = pal_abi.ERROR_IO;
        _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
    } else {
        op.win_error = 0;
        _ = pq(s.iocp, @intCast(n), @intFromPtr(op), &op.win_ov);
    }
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
                op.worker_handle = th;
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
    if (len == 0) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }
    if (he.is_socket) {
        const op = mem.allocOp(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .sock_recv;
        op.guest_ov = ov;
        op.sock_fd = @intFromPtr(he.handle);
        op.buf = buf;
        op.len = len;
        if (s.p_create_thread) |ct| {
            const th = ct(null, 0, sockRecvWorker, op, 0, null);
            if (th != null) {
                op.worker_handle = th;
                return;
            }
        }
        mem.freeOp(s, op);
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    }

    if (!he.is_overlapped) {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    }
    const rf = s.p_read_file orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const positional = (ov.flags & pal_abi.READ_FLAG_POSITIONAL) != 0;
    const op = mem.allocOp(s) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .read;
    op.guest_ov = ov;
    op.handle_token = handle;
    op.buf = buf;
    op.len = len;
    op.native_io = true;
    if (he.is_pipe) {
        op.file_offset = 0;
    } else if (positional) {
        const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
        const off = trailing.*;
        if (off < 0) {
            mem.freeOp(s, op);
            mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        op.file_offset = @intCast(off);
    } else {
        op.file_offset = he.seq_pos;
        op.advance_seq = true;
    }
    op.win_ov.offset = @truncate(op.file_offset);
    op.win_ov.offset_high = @truncate(op.file_offset >> 32);
    if (rf(he.handle, buf, @intCast(@min(len, 0x7fff_ffff)), null, &op.win_ov) == 0) {
        const err = if (s.p_get_last_error) |gle| gle() else 5;
        if (err == 38 or err == 109 or err == 232) {
            mem.freeOp(s, op);
            mem.completeInline(ov, 0, 0, 0);
            return;
        }
        if (err != 997) {
            mem.freeOp(s, op);
            mem.completeInline(ov, win.mapWinError(err), 0, 0);
        }
    }
}

fn abiWrite(ov: *pal_abi.Overlapped, handle: u64, buf: [*]const u8, len: usize) callconv(win.win_cc) void {
    const s = statePtr();
    if (handle == 1 or handle == 2) {
        if (len == 0) {
            mem.completeInline(ov, 0, 0, 0);
            return;
        }
        const op = mem.allocOp(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .write;
        op.guest_ov = ov;
        op.handle_token = handle;
        op.buf = @ptrCast(@constCast(buf));
        op.len = len;
        if (s.p_create_thread) |ct| {
            const th = ct(null, 0, stdHandleWriteWorker, op, 0, null);
            if (th != null) {
                op.worker_handle = th;
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
    if (len == 0) {
        mem.completeInline(ov, 0, 0, 0);
        return;
    }
    if (!he.is_socket and he.access_mode == 0) {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    }
    if (he.is_socket) {
        const op = mem.allocOp(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .write;
        op.guest_ov = ov;
        op.sock_fd = @intFromPtr(he.handle);
        op.buf = @ptrCast(@constCast(buf));
        op.len = len;
        if (s.p_create_thread) |ct| {
            const th = ct(null, 0, sockSendWorker, op, 0, null);
            if (th != null) {
                op.worker_handle = th;
                return;
            }
        }
        mem.freeOp(s, op);
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    }

    if (!he.is_overlapped) {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    }
    const wf = s.p_write_file orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const op = mem.allocOp(s) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .write;
    op.guest_ov = ov;
    op.handle_token = handle;
    op.buf = @ptrCast(@constCast(buf));
    op.len = len;
    op.native_io = true;
    if (he.is_pipe) {
        op.file_offset = 0;
    } else if ((ov.flags & pal_abi.WRITE_FLAG_POSITIONAL) != 0) {
        const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
        if (trailing.* < 0) {
            mem.freeOp(s, op);
            mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        op.file_offset = @intCast(trailing.*);
        op.advance_seq = false;
    } else {
        op.file_offset = he.seq_pos;
        op.advance_seq = true;
    }
    if (he.append_mode) {
        const get_size = s.p_get_file_size_ex orelse {
            mem.freeOp(s, op);
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        var append_offset: i64 = 0;
        if (get_size(he.handle, &append_offset) == 0) {
            mem.freeOp(s, op);
            const gle = s.p_get_last_error orelse {
                mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
                return;
            };
            mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
            return;
        }
        op.file_offset = @intCast(@max(append_offset, 0));
        op.append_write = true;
        op.win_ov.offset = 0xffff_ffff;
        op.win_ov.offset_high = 0xffff_ffff;
    } else {
        op.win_ov.offset = @truncate(op.file_offset);
        op.win_ov.offset_high = @truncate(op.file_offset >> 32);
    }
    if (wf(he.handle, buf, @intCast(@min(len, 0x7fff_ffff)), null, &op.win_ov) == 0) {
        const err = if (s.p_get_last_error) |gle| gle() else 5;
        if (err != 997) {
            mem.freeOp(s, op);
            mem.completeInline(ov, win.mapWinError(err), 0, 0);
        }
    }
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

fn abiPathReadlink(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, out: [*]u8, cap: usize) callconv(win.win_cc) void {
    fs_ops.pathReadlink(statePtr(), ov, path, len, out, cap);
}

fn abiPathChmod(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(win.win_cc) void {
    fs_ops.pathChmod(statePtr(), ov, path, len, mode);
}

fn abiPathSetTimes(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, at_sec: i64, at_nsec: u32, mt_sec: i64, mt_nsec: u32, flags: u32) callconv(win.win_cc) void {
    fs_ops.pathSetTimes(statePtr(), ov, path, len, at_sec, at_nsec, mt_sec, mt_nsec, flags);
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
    s.p_create_named_pipe_w = @ptrCast(@alignCast(mem.resolveKernel32(s, "CreateNamedPipeW") orelse return false));
    s.p_connect_named_pipe = @ptrCast(@alignCast(mem.resolveKernel32(s, "ConnectNamedPipe") orelse return false));
    s.p_cancel_io_ex = @ptrCast(@alignCast(mem.resolveKernel32(s, "CancelIoEx") orelse return false));
    s.p_get_overlapped_result = @ptrCast(@alignCast(mem.resolveKernel32(s, "GetOverlappedResult") orelse return false));
    s.p_cancel_sync_io = @ptrCast(@alignCast(mem.resolveKernel32(s, "CancelSynchronousIo") orelse return false));
    s.p_wait_single = @ptrCast(@alignCast(mem.resolveKernel32(s, "WaitForSingleObject") orelse return false));
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
                const win_ov = e.overlapped orelse continue;
                const op: *mem.PendingOp = @fieldParentPtr("win_ov", win_ov);
                if (!op.used) continue;

                var transferred = e.bytes_transferred;
                if (op.native_io) {
                    if (mem.findHandle(s, op.handle_token)) |he| {
                        var actual: u32 = 0;
                        if (s.p_get_overlapped_result.?(he.handle, &op.win_ov, &actual, 0) != 0) {
                            op.win_error = 0;
                            transferred = actual;
                        } else {
                            const err = s.p_get_last_error.?();
                            if (op.kind == .read and (err == 38 or err == 109 or err == 232)) {
                                op.win_error = 0;
                                transferred = 0;
                            } else {
                                op.win_error = win.mapWinError(err);
                            }
                        }
                    } else {
                        op.win_error = pal_abi.ERROR_BADF;
                    }
                }

                const canceled = @atomicLoad(bool, &op.canceled, .acquire);
                const kind = op.kind;
                const guest_ov = op.guest_ov;
                const raw_sock = op.win_ov.internal_high;
                const worker_handle = op.worker_handle;
                if (op.advance_seq) {
                    if (mem.findHandle(s, op.handle_token)) |he| {
                        if (op.append_write and op.win_error == 0) {
                            var end_offset: i64 = 0;
                            if (s.p_get_file_size_ex.?(he.handle, &end_offset) == 0) {
                                op.win_error = win.mapWinError(s.p_get_last_error.?());
                            } else {
                                he.seq_pos = @intCast(@max(end_offset, 0));
                            }
                        } else {
                            he.seq_pos = op.file_offset + transferred;
                        }
                    }
                }
                const completion_error = if (kind == .accept)
                    @as(u32, @intCast(op.win_ov.internal))
                else
                    op.win_error;
                const result_ext = if (kind == .proc_wait) op.win_ov.internal_high else transferred;
                mem.freeOp(s, op);
                if (worker_handle != null) {
                    if (s.p_close_handle) |ch| _ = ch(worker_handle);
                }

                if (canceled or guest_ov == null or kind == .pipe_connect) continue;
                const gov = guest_ov.?;
                if (kind == .accept) {
                    if (completion_error != 0) {
                        mem.completeInline(gov, completion_error, 0, 0);
                    } else if (mem.allocHandle(s)) |cli_he| {
                        cli_he.handle = @ptrFromInt(raw_sock);
                        cli_he.is_socket = true;
                        mem.completeInline(gov, 0, 0, cli_he.token);
                    } else {
                        mem.completeInline(gov, pal_abi.ERROR_NOMEM, 0, 0);
                    }
                } else {
                    mem.completeInline(gov, completion_error, 0, result_ext);
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
