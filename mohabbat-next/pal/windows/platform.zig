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

var test_state: ?*PalState = null;

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
    if (builtin.is_test) {
        return test_state orelse unreachable;
    } else {
        return @ptrCast(@alignCast(sliceOrigin() - pal_abi.PREFIX_GLOBALS_OFFSET));
    }
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

const HOST_PANIC_EXIT_CODE: i32 = 99;

fn writeHostPanicMessage(s: *PalState, msg: [*]const u8, len: usize) void {
    if (s.p_write_file) |wf| {
        var written: u32 = 0;
        _ = wf(s.stderr_h, msg, @intCast(@min(len, 0x7fff_ffff)), &written, null);
    }
}

fn abiHostPanic(msg: [*]const u8, len: usize) callconv(win.win_cc) noreturn {
    const s = statePtr();
    writeHostPanicMessage(s, msg, len);
    if (s.p_exit_process) |ep| ep(HOST_PANIC_EXIT_CODE);
    while (true) {}
}

fn abiRusticatedDebug(val: i32) callconv(win.win_cc) void {
    _ = val;
}

fn prepareProcessExit(s: *PalState, code: i32) i32 {
    proc_tty.ttySetMode(s, 0, 0);
    return code;
}

fn abiProcessExit(code: i32) callconv(win.win_cc) noreturn {
    const s = statePtr();
    const exit_code = prepareProcessExit(s, code);
    if (s.p_exit_process) |ep| ep(exit_code);
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
    if (op.is_udp) {
        const p_recvfrom = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "recvfrom") orelse {
            op.win_error = pal_abi.ERROR_UNSUPPORTED;
            _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
            return 0;
        };
        const RecvFromFn = *const fn (usize, ?[*]u8, i32, i32, ?*anyopaque, ?*i32) callconv(win.win_cc) i32;
        const recvfrom_fn: RecvFromFn = @ptrCast(@alignCast(p_recvfrom));
        const n = recvfrom_fn(op.sock_fd, op.buf, @intCast(@min(op.len, 0x7fff_ffff)), 0, null, null);
        if (n < 0) {
            op.win_error = pal_abi.ERROR_IO;
            _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        } else {
            op.win_error = 0;
            _ = pq(s.iocp, @intCast(n), @intFromPtr(op), &op.win_ov);
        }
        return 0;
    }
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
    if (op.is_udp) {
        const p_sendto = mem.resolveDll(s, &s.ws2_32, "ws2_32.dll", "sendto") orelse {
            op.win_error = pal_abi.ERROR_UNSUPPORTED;
            _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
            return 0;
        };
        const SendToFn = *const fn (usize, [*]const u8, i32, i32, ?*anyopaque, i32) callconv(win.win_cc) i32;
        const sendto_fn: SendToFn = @ptrCast(@alignCast(p_sendto));
        const n = sendto_fn(op.sock_fd, @ptrCast(op.buf.?), @intCast(@min(op.len, 0x7fff_ffff)), 0, null, 0);
        if (n < 0) {
            op.win_error = pal_abi.ERROR_IO;
            _ = pq(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        } else {
            op.win_error = 0;
            _ = pq(s.iocp, @intCast(n), @intFromPtr(op), &op.win_ov);
        }
        return 0;
    }
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
    if (he.is_pty) {
        if (len == 0) {
            mem.completeInline(ov, 0, 0, 0);
            return;
        }
        if (mem.resolveKernel32(s, "PeekNamedPipe")) |p_peek| {
            const PeekFn = *const fn (win.HANDLE, ?[*]u8, u32, ?*u32, ?*u32, ?*u32) callconv(win.win_cc) i32;
            const peek: PeekFn = @ptrCast(@alignCast(p_peek));
            var available: u32 = 0;
            if (peek(he.pty_read, null, 0, null, &available, null) == 0) {
                mem.completeInline(ov, 0, 0, 0);
                return;
            }
            if (available == 0) {
                mem.completeInline(ov, 0, 0, 0);
                return;
            }
        }
        const rf = s.p_read_file orelse {
            mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        var transferred: u32 = 0;
        if (rf(he.pty_read, buf, @intCast(@min(len, 0x7fff_ffff)), &transferred, null) == 0) {
            const err = if (s.p_get_last_error) |gle| gle() else 5;
            if (err == 109 or err == 232) {
                mem.completeInline(ov, 0, 0, 0);
            } else {
                mem.completeInline(ov, win.mapWinError(err), 0, 0);
            }
        } else {
            mem.completeInline(ov, 0, 0, transferred);
        }
        return;
    }
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
        op.is_udp = he.is_udp;
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
    if (handle >= 3) {
        if (mem.findHandle(s, handle)) |he| {
            if (he.is_pty) {
                if (len == 0) {
                    mem.completeInline(ov, 0, 0, 0);
                    return;
                }
                const wf = s.p_write_file orelse {
                    mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
                    return;
                };
                var transferred: u32 = 0;
                if (wf(he.pty_write, buf, @intCast(@min(len, 0x7fff_ffff)), &transferred, null) == 0) {
                    const err = if (s.p_get_last_error) |gle| gle() else 5;
                    mem.completeInline(ov, win.mapWinError(err), 0, 0);
                } else {
                    mem.completeInline(ov, 0, 0, transferred);
                }
                return;
            }
        }
    }
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
        op.is_udp = he.is_udp;
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
    if (he.is_pty) {
        if (s.p_close_handle) |ch| {
            if (he.pty_read != null) _ = ch(he.pty_read);
            if (he.pty_write != null) _ = ch(he.pty_write);
        }
        he.* = .{};
    } else if (he.is_dir) {
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

fn initializeAbiTable(table: *pal_abi.PalAbiTable, wasm_bytes: ?[*]const u8, wasm_len: usize) void {
    table.* = .{
        .wasm_bytes = wasm_bytes,
        .wasm_len = wasm_len,
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

    const runtime_ptr = s.host.runtime_slice orelse return 11;
    const runtime_len = s.host.runtime_slice_size;
    if (runtime_len == 0 or s.host.wasm_payload == null or s.host.wasm_payload_size == 0) return 12;

    const va = s.p_virtual_alloc.?;
    const vp = s.p_virtual_protect.?;
    const code_aligned = pageAlign(runtime_len);
    const total_size = pal_abi.PREFIX_ABI_OFFSET + code_aligned;
    const mem_base = va(null, total_size, 0x3000, 0x04) orelse return 13; // PAGE_READWRITE

    const table: *pal_abi.PalAbiTable = @ptrCast(@alignCast(mem_base));
    initializeAbiTable(table, s.host.wasm_payload, s.host.wasm_payload_size);

    const code_ptr = mem_base + pal_abi.PREFIX_ABI_OFFSET;
    @memcpy(code_ptr[0..runtime_len], runtime_ptr[0..runtime_len]);
    var old_prot: u32 = 0;
    if (vp(code_ptr, code_aligned, 0x20, &old_prot) == 0) return 14; // PAGE_EXECUTE_READ
    abiFlushIcache(code_ptr, runtime_len);

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

test "Windows PAL maps native status and errno classes" {
    const Case = struct { native: u32, expected: u32 };
    const cases = [_]Case{
        .{ .native = 0, .expected = 0 },
        .{ .native = 2, .expected = pal_abi.ERROR_NOENT },
        .{ .native = 3, .expected = pal_abi.ERROR_NOENT },
        .{ .native = 5, .expected = pal_abi.ERROR_ACCES },
        .{ .native = 6, .expected = pal_abi.ERROR_BADF },
        .{ .native = 8, .expected = pal_abi.ERROR_NOMEM },
        .{ .native = 14, .expected = pal_abi.ERROR_NOMEM },
        .{ .native = 32, .expected = pal_abi.ERROR_EXIST },
        .{ .native = 33, .expected = pal_abi.ERROR_EXIST },
        .{ .native = 80, .expected = pal_abi.ERROR_EXIST },
        .{ .native = 87, .expected = pal_abi.ERROR_INVALID },
        .{ .native = 123, .expected = pal_abi.ERROR_INVALID },
        .{ .native = 131, .expected = pal_abi.ERROR_INVALID },
        .{ .native = 161, .expected = pal_abi.ERROR_INVALID },
        .{ .native = 109, .expected = pal_abi.ERROR_PIPE },
        .{ .native = 232, .expected = pal_abi.ERROR_PIPE },
        .{ .native = 145, .expected = pal_abi.ERROR_ISDIR },
        .{ .native = 267, .expected = pal_abi.ERROR_NOTDIR },
        .{ .native = 995, .expected = pal_abi.ERROR_CANCELED },
        .{ .native = 997, .expected = pal_abi.ERROR_AGAIN },
        .{ .native = 10035, .expected = pal_abi.ERROR_AGAIN },
        .{ .native = 10060, .expected = pal_abi.ERROR_TIMEDOUT },
        .{ .native = 10061, .expected = pal_abi.ERROR_CONNREFUSED },
        .{ .native = 12345, .expected = pal_abi.ERROR_IO },
    };
    for (cases) |case| {
        try std.testing.expectEqual(case.expected, win.mapWinError(case.native));
    }
}

fn testAbiSystemTime(out: *win.FileTime) callconv(win.win_cc) void {
    out.* = .{ .low = 0x9f7b_7600, .high = 0x01dc_0000 };
}

test "Windows ABI table dispatches argument, time, and platform handlers" {
    const args = [_][*:0]const u8{ "C:\\app.exe", "--native-abi" };
    var state: PalState = .{};
    state.host.argv = &args;
    state.host.argc = args.len;
    state.p_get_system_time_ft = testAbiSystemTime;
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    var arg_buf: [24]u8 = undefined;
    const arg_result = table.get_args(&arg_buf, arg_buf.len);
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(arg_result >> 32)));
    try std.testing.expectEqual(@as(u32, arg_buf.len), @as(u32, @truncate(arg_result)));
    try std.testing.expectEqualSlices(u8, "C:\\app.exe\x00--native-abi\x00", &arg_buf);

    const raw_time: u64 = (@as(u64, 0x01dc_0000) << 32) | 0x9f7b_7600;
    const expected_time = win.fileTimeToUnixNs(.{
        .low = @truncate(raw_time),
        .high = @truncate(raw_time >> 32),
    });
    try std.testing.expectEqual(@as(u64, @intCast(expected_time)), table.get_time());

    var platform: pal_abi.AbiPlatformInfo = .{};
    try std.testing.expectEqual(
        @as(u32, 0),
        table.get_platform_info(@ptrCast(&platform), @sizeOf(pal_abi.AbiPlatformInfo)),
    );
    try std.testing.expectEqual(pal_abi.OS_KIND_WINDOWS, platform.os_kind);
    try std.testing.expectEqual(@as(u8, '\\'), platform.path_separator);
    try std.testing.expectEqualSlices(u8, "windows", platform.os_name[0..7]);
}

test "Windows ABI table dispatches filesystem state and input validation" {
    var state: PalState = .{};
    const handle = mem.allocHandle(&state).?;
    handle.seq_pos = 10;
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    var ov: pal_abi.Overlapped = .{};
    table.seek(&ov, handle.token, 5, 1);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 15), ov.result_ext);
    try std.testing.expectEqual(@as(u64, 15), handle.seq_pos);

    ov = .{};
    table.seek(&ov, handle.token, -20, 1);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    try std.testing.expectEqual(@as(u64, 15), handle.seq_pos);

    ov = .{ .flags = pal_abi.READ_FLAG_POSITIONAL };
    var read_buf: [1]u8 = .{0};
    table.read(&ov, 0xffff, &read_buf, read_buf.len);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");
    try std.testing.expectEqual(@as(u64, 0), ov.continued);
    try std.testing.expectEqual(@as(u64, 0), ov.result_ext);

    ov = .{};
    var stat_buf: [1]u8 = .{0};
    table.path_stat(&ov, "missing", 7, 0, &stat_buf, stat_buf.len);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
}

test "Windows ABI table cancellation completes an outstanding timer" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    var ov: pal_abi.Overlapped = .{};
    table.timer_set(&ov, 10);
    try std.testing.expectEqual(@as(usize, 1), state.pending_count);
    try std.testing.expectEqual(@as(u32, 0), ov.flags);

    table.cancel(&ov);
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, ov.@"error");
    try std.testing.expectEqual(@as(u64, 0), ov.continued);
    try std.testing.expectEqual(@as(u64, 0), ov.result_ext);
}

test "Windows ABI table completes invalid handle operations" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    var ov: pal_abi.Overlapped = .{};
    var buf: [1]u8 = .{0};
    table.read(&ov, 0xffff, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    ov = .{};
    table.write(&ov, 0xffff, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    ov = .{};
    table.seek(&ov, 0xffff, 0, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    ov = .{};
    table.handle_truncate(&ov, 0xffff, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    table.handle_close(0xffff);

    ov = .{};
    table.dir_read(&ov, 0xffff, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    ov = .{};
    table.net_accept(&ov, 0xffff);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    ov = .{};
    table.process_wait(&ov, 0xffff);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");
}

test "Windows ABI table returns explicit unsupported statuses" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    var ov: pal_abi.Overlapped = .{};
    var empty: [0]u8 = .{};
    table.net_open(&ov, "", 0, 0, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    ov = .{};
    table.net_lookup(&ov, "", 0, &empty, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    ov = .{};
    table.net_cert_verify(&ov, "", 0, "", 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");

    ov = .{};
    table.process_spawn(&ov, "", 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    ov = .{};
    table.signal_wait(&ov, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    ov = .{};
    table.system_info_wait(&ov);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
}

test "Windows ABI table preserves TTY fallback behavior" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    try std.testing.expectEqual(@as(u32, (80 << 16) | 24), table.tty_get_size(3));
    try std.testing.expectEqual(@as(u32, 0), table.tty_set_size(1, 120, 40));
    try std.testing.expectEqual(@as(u32, 0), table.fd_isatty(3));
    table.tty_set_mode(3, 1);
    table.process_signal(0xffff, 15);
}

test "Windows ABI table get_args validates capacities and preserves bounded writes" {
    var state: PalState = .{};
    state.host.argv = null;
    state.host.argc = 0;
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    var out: [16]u8 = @splat(0xcc);

    // Missing argv is distinct from an explicitly empty argument vector.
    var result = table.get_args(&out, out.len);
    try std.testing.expectEqual(@as(u64, 0), result);
    try std.testing.expectEqual(@as(u8, 0xcc), out[0]);

    const no_args = [_][*:0]const u8{};
    state.host.argv = &no_args;
    state.host.argc = no_args.len;
    result = table.get_args(&out, out.len);
    try std.testing.expectEqual(@as(u64, 0), result);
    try std.testing.expectEqual(@as(u8, 0xcc), out[0]);

    const one = [_][*:0]const u8{"app"};
    state.host.argv = &one;
    state.host.argc = one.len;
    result = table.get_args(&out, 4);
    try std.testing.expectEqual(@as(u32, 1), @as(u32, @truncate(result >> 32)));
    try std.testing.expectEqual(@as(u32, 4), @as(u32, @truncate(result)));
    try std.testing.expectEqualSlices(u8, "app\x00", out[0..4]);

    @memset(&out, 0xcc);
    result = table.get_args(&out, 3);
    try std.testing.expectEqual(@as(u32, 4), @as(u32, @truncate(result)));
    try std.testing.expectEqualSlices(u8, &@as([16]u8, @splat(0xcc)), &out);

    const two = [_][*:0]const u8{ "a", "bc" };
    state.host.argv = &two;
    state.host.argc = two.len;
    result = table.get_args(&out, 5);
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(result >> 32)));
    try std.testing.expectEqualSlices(u8, "a\x00bc\x00", out[0..5]);

    const with_empty = [_][*:0]const u8{ "a", "" };
    state.host.argv = &with_empty;
    state.host.argc = with_empty.len;
    @memset(&out, 0xcc);
    result = table.get_args(&out, 2);
    try std.testing.expectEqual(@as(u32, 3), @as(u32, @truncate(result)));
    try std.testing.expectEqualSlices(u8, &@as([16]u8, @splat(0xcc)), &out);

    const empty_edges = [_][*:0]const u8{ "", "x", "" };
    state.host.argv = &empty_edges;
    state.host.argc = empty_edges.len;
    result = table.get_args(&out, 4);
    try std.testing.expectEqual(@as(u32, 3), @as(u32, @truncate(result >> 32)));
    try std.testing.expectEqualSlices(u8, "\x00x\x00\x00", out[0..4]);

    const cap_zero = [_][*:0]const u8{"x"};
    state.host.argv = &cap_zero;
    state.host.argc = cap_zero.len;
    @memset(&out, 0xcc);
    result = table.get_args(&out, 0);
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(result)));
    try std.testing.expectEqual(@as(u8, 0xcc), out[0]);

    const utf8 = [_][*:0]const u8{"caf\xc3\xa9"};
    state.host.argv = &utf8;
    state.host.argc = utf8.len;
    result = table.get_args(&out, 6);
    try std.testing.expectEqual(@as(u32, 6), @as(u32, @truncate(result)));
    try std.testing.expectEqualSlices(u8, "caf\xc3\xa9\x00", out[0..6]);
    try std.testing.expectEqual(@as(u8, 0xcc), out[6]);

    const larger = [_][*:0]const u8{"z"};
    state.host.argv = &larger;
    state.host.argc = larger.len;
    @memset(&out, 0xcc);
    result = table.get_args(&out, out.len);
    try std.testing.expectEqual(@as(u32, 2), @as(u32, @truncate(result)));
    try std.testing.expectEqualSlices(u8, "z\x00", out[0..2]);
    try std.testing.expectEqualSlices(u8, &@as([14]u8, @splat(0xcc)), out[2..]);
}

test "Windows ABI table bounds platform info output" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    const size = @sizeOf(pal_abi.AbiPlatformInfo);
    var storage: [@sizeOf(pal_abi.AbiPlatformInfo) + 4]u8 align(@alignOf(pal_abi.AbiPlatformInfo)) = @splat(0xcc);
    for ([_]usize{ 0, 1, size - 1 }) |cap| {
        try std.testing.expectEqual(pal_abi.ERROR_INVALID, table.get_platform_info(&storage, cap));
        try std.testing.expectEqualSlices(u8, &@as([@sizeOf(pal_abi.AbiPlatformInfo) + 4]u8, @splat(0xcc)), &storage);
    }

    try std.testing.expectEqual(@as(u32, 0), table.get_platform_info(&storage, size));
    const info: *const pal_abi.AbiPlatformInfo = @ptrCast(@alignCast(&storage));
    try std.testing.expectEqual(pal_abi.OS_KIND_WINDOWS, info.os_kind);
    try std.testing.expectEqual(@as(u8, '\\'), info.path_separator);
    try std.testing.expectEqualSlices(u8, "windows", info.os_name[0..7]);

    @memset(&storage, 0xcc);
    try std.testing.expectEqual(@as(u32, 0), table.get_platform_info(&storage, storage.len));
    try std.testing.expectEqualSlices(u8, &@as([4]u8, @splat(0xcc)), storage[size..]);
}

var test_virtual_alloc_size: usize = 0;
var test_virtual_alloc_protection: u32 = 0;
var test_virtual_alloc_success = true;
var test_virtual_alloc_calls: usize = 0;
var test_virtual_protect_size: usize = 0;
var test_virtual_protect_protection: u32 = 0;
var test_virtual_protect_success = true;
var test_virtual_protect_calls: usize = 0;
var test_memory_storage: [8192]u8 align(4096) = undefined;

fn testVirtualAlloc(_: ?*anyopaque, size: usize, _: u32, protection: u32) callconv(win.win_cc) ?[*]u8 {
    test_virtual_alloc_calls += 1;
    test_virtual_alloc_size = size;
    test_virtual_alloc_protection = protection;
    return if (test_virtual_alloc_success) test_memory_storage[0..].ptr else null;
}

fn testVirtualProtect(_: ?*anyopaque, size: usize, protection: u32, old: *u32) callconv(win.win_cc) i32 {
    test_virtual_protect_calls += 1;
    test_virtual_protect_size = size;
    test_virtual_protect_protection = protection;
    old.* = 0x40;
    return @intFromBool(test_virtual_protect_success);
}

test "Windows ABI table alloc_mem rounds page sizes and selects protection" {
    var state: PalState = .{};
    state.p_virtual_alloc = testVirtualAlloc;
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    const Case = struct { size: usize, flags: u32, rounded: usize, protection: u32 };
    const cases = [_]Case{
        .{ .size = 0, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 1, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 4095, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 4096, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 4097, .flags = 0, .rounded = 8192, .protection = 0x04 },
        .{ .size = 8191, .flags = 0, .rounded = 8192, .protection = 0x04 },
        .{ .size = 8192, .flags = 0, .rounded = 8192, .protection = 0x04 },
        .{ .size = 64, .flags = pal_abi.MEM_CODE_EXEC_RX, .rounded = 4096, .protection = 0x20 },
        .{ .size = 64, .flags = pal_abi.MEM_CODE_JIT_RW, .rounded = 4096, .protection = 0x04 },
        .{ .size = 64, .flags = 0xffff_ffff, .rounded = 4096, .protection = 0x04 },
    };
    test_virtual_alloc_calls = 0;
    test_virtual_alloc_success = true;
    for (cases) |case| {
        const ptr = table.alloc_mem(case.size, 0, case.flags);
        try std.testing.expect(ptr != null);
        try std.testing.expectEqual(case.rounded, test_virtual_alloc_size);
        try std.testing.expectEqual(case.protection, test_virtual_alloc_protection);
    }

    test_virtual_alloc_success = false;
    try std.testing.expect(table.alloc_mem(1, 0, 0) == null);
    try std.testing.expectEqual(@as(usize, 11), test_virtual_alloc_calls);

    state.p_virtual_alloc = null;
    try std.testing.expect(table.alloc_mem(1, 0, 0) == null);
    try std.testing.expectEqual(@as(usize, 11), test_virtual_alloc_calls);
}

test "Windows ABI table protect_mem rounds sizes and reports API failures" {
    var state: PalState = .{};
    state.p_virtual_protect = testVirtualProtect;
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);

    const Case = struct { size: usize, flags: u32, rounded: usize, protection: u32 };
    const cases = [_]Case{
        .{ .size = 0, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 1, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 4095, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 4096, .flags = 0, .rounded = 4096, .protection = 0x04 },
        .{ .size = 4097, .flags = 0, .rounded = 8192, .protection = 0x04 },
        .{ .size = 8192, .flags = 0, .rounded = 8192, .protection = 0x04 },
        .{ .size = 128, .flags = pal_abi.MEM_CODE_EXEC_RX, .rounded = 4096, .protection = 0x20 },
        .{ .size = 128, .flags = pal_abi.MEM_CODE_JIT_RW, .rounded = 4096, .protection = 0x04 },
        .{ .size = 128, .flags = 0xffff_ffff, .rounded = 4096, .protection = 0x04 },
    };
    test_virtual_protect_calls = 0;
    test_virtual_protect_success = true;
    const target: [*]u8 = test_memory_storage[0..].ptr;
    for (cases) |case| {
        try std.testing.expectEqual(@as(i32, 0), table.protect_mem(target, case.size, case.flags));
        try std.testing.expectEqual(case.rounded, test_virtual_protect_size);
        try std.testing.expectEqual(case.protection, test_virtual_protect_protection);
    }

    test_virtual_protect_success = false;
    try std.testing.expectEqual(@as(i32, -1), table.protect_mem(target, 1, 0));
    state.p_virtual_protect = null;
    try std.testing.expectEqual(@as(i32, -1), table.protect_mem(target, 1, 0));
    try std.testing.expectEqual(@as(usize, 10), test_virtual_protect_calls);
}

test "Windows ABI table read and write validate handle classes and zero lengths" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, null, 0);
    var ov: pal_abi.Overlapped = .{};
    var buf: [1]u8 = .{0};

    table.read(&ov, 0, &buf, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");

    ov = .{};
    table.read(&ov, 0, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");

    ov = .{};
    table.read(&ov, 0xffff, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    const write_only = mem.allocHandle(&state).?;
    write_only.access_mode = 1;
    ov = .{};
    table.read(&ov, write_only.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    const ordinary = mem.allocHandle(&state).?;
    ov = .{};
    table.read(&ov, ordinary.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    const pty = mem.allocHandle(&state).?;
    pty.is_pty = true;
    ov = .{};
    table.read(&ov, pty.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    const read_only = mem.allocHandle(&state).?;
    read_only.access_mode = 0;
    ov = .{};
    table.write(&ov, read_only.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    const non_overlapped_write = mem.allocHandle(&state).?;
    non_overlapped_write.access_mode = 1;
    ov = .{};
    table.write(&ov, non_overlapped_write.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    const socket = mem.allocHandle(&state).?;
    socket.is_socket = true;
    socket.handle = @ptrFromInt(0x10);
    ov = .{};
    table.read(&ov, socket.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");

    ov = .{};
    table.write(&ov, socket.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");

    ov = .{};
    table.write(&ov, non_overlapped_write.token, &buf, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
}

var abi_test_write_calls: usize = 0;
var abi_test_write_handle: win.HANDLE = null;
var abi_test_write_buffer: ?[*]const u8 = null;
var abi_test_write_len: u32 = 0;
var abi_test_write_result: i32 = 1;
var abi_test_write_transferred: u32 = 0;
var abi_test_write_offset: u32 = 0;
var abi_test_write_offset_high: u32 = 0;

fn abiTestWriteFile(
    handle: win.HANDLE,
    buffer: ?[*]const u8,
    len: u32,
    written: ?*u32,
    overlapped: ?*win.WinOverlapped,
) callconv(win.win_cc) i32 {
    abi_test_write_calls += 1;
    abi_test_write_handle = handle;
    abi_test_write_buffer = buffer;
    abi_test_write_len = len;
    if (overlapped) |ov| {
        abi_test_write_offset = ov.offset;
        abi_test_write_offset_high = ov.offset_high;
    }
    if (written) |out| out.* = abi_test_write_transferred;
    return abi_test_write_result;
}

var abi_test_read_calls: usize = 0;
var abi_test_read_handle: win.HANDLE = null;
var abi_test_read_len: u32 = 0;
var abi_test_read_offset: u32 = 0;
var abi_test_read_offset_high: u32 = 0;

fn abiTestReadFile(
    handle: win.HANDLE,
    _: ?[*]u8,
    len: u32,
    _: ?*u32,
    overlapped: ?*win.WinOverlapped,
) callconv(win.win_cc) i32 {
    abi_test_read_calls += 1;
    abi_test_read_handle = handle;
    abi_test_read_len = len;
    if (overlapped) |ov| {
        abi_test_read_offset = ov.offset;
        abi_test_read_offset_high = ov.offset_high;
    }
    return 1;
}

var abi_test_file_size: i64 = 0;

fn abiTestGetFileSize(_: win.HANDLE, size: *i64) callconv(win.win_cc) i32 {
    size.* = abi_test_file_size;
    return 1;
}

var abi_test_exit_mode: u32 = 0;
var abi_test_exit_set_mode: u32 = 0;
var abi_test_exit_get_calls: usize = 0;
var abi_test_exit_set_calls: usize = 0;

fn abiTestGetConsoleMode(_: win.HANDLE, mode: *u32) callconv(win.win_cc) i32 {
    abi_test_exit_get_calls += 1;
    mode.* = abi_test_exit_mode;
    return 1;
}

fn abiTestSetConsoleMode(_: win.HANDLE, mode: u32) callconv(win.win_cc) i32 {
    abi_test_exit_set_calls += 1;
    abi_test_exit_set_mode = mode;
    return 1;
}

fn abiTestProcessExitLookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    if (std.mem.eql(u8, std.mem.span(name), "GetConsoleMode")) {
        return @ptrCast(@constCast(&abiTestGetConsoleMode));
    }
    if (std.mem.eql(u8, std.mem.span(name), "SetConsoleMode")) {
        return @ptrCast(@constCast(&abiTestSetConsoleMode));
    }
    return null;
}

var abi_test_tick_now: u64 = 0;

fn abiTestTickCount() callconv(win.win_cc) u64 {
    return abi_test_tick_now;
}

var abi_test_close_calls: usize = 0;
var abi_test_closed_handles: [8]win.HANDLE = @splat(null);

fn abiTestCloseHandle(handle: win.HANDLE) callconv(win.win_cc) i32 {
    if (abi_test_close_calls < abi_test_closed_handles.len) {
        abi_test_closed_handles[abi_test_close_calls] = handle;
    }
    abi_test_close_calls += 1;
    return 1;
}

var abi_test_free_calls: usize = 0;
var abi_test_free_address: ?*anyopaque = null;
var abi_test_free_size: usize = 0;
var abi_test_free_flags: u32 = 0;
var abi_test_free_result: i32 = 1;

fn abiTestVirtualFree(address: ?*anyopaque, size: usize, flags: u32) callconv(win.win_cc) i32 {
    abi_test_free_calls += 1;
    abi_test_free_address = address;
    abi_test_free_size = size;
    abi_test_free_flags = flags;
    return abi_test_free_result;
}

var abi_test_flush_calls: usize = 0;
var abi_test_flush_process: win.HANDLE = null;
var abi_test_flush_address: ?*const anyopaque = null;
var abi_test_flush_size: usize = 0;
var abi_test_flush_result: i32 = 1;

fn abiTestCurrentProcess() callconv(win.win_cc) win.HANDLE {
    return @ptrFromInt(0x44);
}

fn abiTestFlushInstructionCache(
    process: win.HANDLE,
    address: ?*const anyopaque,
    size: usize,
) callconv(win.win_cc) i32 {
    abi_test_flush_calls += 1;
    abi_test_flush_process = process;
    abi_test_flush_address = address;
    abi_test_flush_size = size;
    return abi_test_flush_result;
}

fn testAbiTable(
    state: *PalState,
    wasm_bytes: ?[*]const u8,
    wasm_len: usize,
) pal_abi.PalAbiTable {
    test_state = state;
    var table: pal_abi.PalAbiTable = undefined;
    initializeAbiTable(&table, wasm_bytes, wasm_len);
    return table;
}

test "Windows ABI host_panic forwards message lengths and ignores write failures" {
    var state: PalState = .{};
    state.stderr_h = @ptrFromInt(0x101);
    state.p_write_file = abiTestWriteFile;
    const table = testAbiTable(&state, null, 0);
    const message = "panic";
    const Case = struct { len: usize, expected: u32, write_result: i32 };
    const cases = [_]Case{
        .{ .len = 0, .expected = 0, .write_result = 1 },
        .{ .len = 1, .expected = 1, .write_result = 1 },
        .{ .len = 2, .expected = 2, .write_result = 0 },
        .{ .len = 3, .expected = 3, .write_result = 1 },
        .{ .len = 4, .expected = 4, .write_result = 0 },
        .{ .len = 5, .expected = 5, .write_result = 1 },
        .{ .len = 16, .expected = 16, .write_result = 1 },
        .{ .len = 4096, .expected = 4096, .write_result = 0 },
        .{ .len = 0x7fff_ffff, .expected = 0x7fff_ffff, .write_result = 1 },
        .{ .len = 0x8000_0000, .expected = 0x7fff_ffff, .write_result = 0 },
        .{ .len = std.math.maxInt(usize), .expected = 0x7fff_ffff, .write_result = 1 },
    };

    try std.testing.expectEqual(@as(i32, 99), HOST_PANIC_EXIT_CODE);
    try std.testing.expect(@intFromPtr(table.host_panic) == @intFromPtr(&abiHostPanic));
    for (cases) |case| {
        abi_test_write_calls = 0;
        abi_test_write_result = case.write_result;
        abi_test_write_transferred = 17;
        writeHostPanicMessage(&state, message.ptr, case.len);
        try std.testing.expectEqual(@as(usize, 1), abi_test_write_calls);
        try std.testing.expectEqual(state.stderr_h, abi_test_write_handle);
        try std.testing.expectEqual(@as(u32, case.expected), abi_test_write_len);
        try std.testing.expectEqual(@intFromPtr(message.ptr), @intFromPtr(abi_test_write_buffer.?));
    }
}

test "Windows ABI rusticated_debug preserves PAL state for signed boundary values" {
    var state: PalState = .{};
    state.next_token = 91;
    const table = testAbiTable(&state, null, 0);
    const values = [_]i32{
        std.math.minInt(i32),
        -1_000_000,
        -65_536,
        -256,
        -2,
        -1,
        0,
        1,
        2,
        65_536,
        std.math.maxInt(i32),
    };

    for (values) |value| {
        table.rusticated_debug(value);
        try std.testing.expectEqual(@as(usize, 0), state.pending_count);
        try std.testing.expectEqual(@as(u64, 91), state.next_token);
        try std.testing.expect(!state.initialized);
    }
}

test "Windows ABI process_exit restores console mode and preserves exit codes" {
    const Case = struct { code: i32, saved_mode: u32 };
    const cases = [_]Case{
        .{ .code = std.math.minInt(i32), .saved_mode = 0 },
        .{ .code = -1024, .saved_mode = 1 },
        .{ .code = -128, .saved_mode = 2 },
        .{ .code = -1, .saved_mode = 4 },
        .{ .code = 0, .saved_mode = 7 },
        .{ .code = 1, .saved_mode = 0x10 },
        .{ .code = 2, .saved_mode = 0x20 },
        .{ .code = 127, .saved_mode = 0x80 },
        .{ .code = 255, .saved_mode = 0x100 },
        .{ .code = 256, .saved_mode = 0x200 },
        .{ .code = std.math.maxInt(i32), .saved_mode = 0xffff },
    };

    for (cases) |case| {
        var state: PalState = .{};
        state.kernel32 = @ptrFromInt(1);
        state.host = .{
            .load_library = null,
            .get_proc_address = abiTestProcessExitLookup,
            .alloc = null,
            .free = null,
            .runtime_slice = null,
            .runtime_slice_size = 0,
            .argv = null,
            .argc = 0,
            .wasm_payload = null,
            .wasm_payload_size = 0,
        };
        state.stdin_h = @ptrFromInt(0x22);
        state.saved_in_mode = case.saved_mode;
        state.has_saved_in_mode = true;
        abi_test_exit_mode = 0x207;
        abi_test_exit_get_calls = 0;
        abi_test_exit_set_calls = 0;

        try std.testing.expectEqual(case.code, prepareProcessExit(&state, case.code));
        try std.testing.expectEqual(@as(usize, 1), abi_test_exit_get_calls);
        try std.testing.expectEqual(@as(usize, 1), abi_test_exit_set_calls);
        try std.testing.expectEqual(case.saved_mode, abi_test_exit_set_mode);
    }
}

test "Windows ABI timer_set records each nonzero delay against the host clock" {
    const delays = [_]u32{ 1, 2, 3, 7, 10, 16, 100, 1000, 65_535, 0x7fff_ffff };
    abi_test_tick_now = 500;

    for (delays) |delay| {
        var state: PalState = .{};
        state.p_get_tick_count64 = abiTestTickCount;
        const table = testAbiTable(&state, null, 0);
        var ov: pal_abi.Overlapped = .{};

        table.timer_set(&ov, delay);
        try std.testing.expectEqual(@as(u32, 0), ov.flags);
        try std.testing.expectEqual(@as(usize, 1), state.pending_count);
        try std.testing.expectEqual(mem.OpKind.timer, state.ops[0].kind);
        try std.testing.expectEqual(@as(u64, 500) + delay, state.ops[0].deadline_ms);
        try std.testing.expect(state.ops[0].guest_ov.? == &ov);
    }

    var full_state: PalState = .{};
    full_state.ops = @splat(.{ .used = true });
    full_state.pending_count = mem.MAX_OPS;
    const full_table = testAbiTable(&full_state, null, 0);
    var full_ov: pal_abi.Overlapped = .{};
    full_table.timer_set(&full_ov, 1);
    try std.testing.expectEqual(pal_abi.ERROR_NOMEM, full_ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, full_ov.flags);
}

test "Windows ABI cancel handles timer matching and nonmatching cases" {
    const delays = [_]u32{ 1, 2, 10, 100, 1000, 60_000, 0x7fff_ffff, 0xffff_ffff };
    for (delays) |delay| {
        var state: PalState = .{};
        const table = testAbiTable(&state, null, 0);
        var ov: pal_abi.Overlapped = .{};

        table.timer_set(&ov, delay);
        table.cancel(&ov);
        try std.testing.expectEqual(@as(usize, 0), state.pending_count);
        try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
        try std.testing.expectEqual(pal_abi.ERROR_CANCELED, ov.@"error");
    }

    var state: PalState = .{};
    const table = testAbiTable(&state, null, 0);
    var first: pal_abi.Overlapped = .{};
    var second: pal_abi.Overlapped = .{};
    table.timer_set(&first, 1);
    table.timer_set(&second, 2);
    table.cancel(&first);
    try std.testing.expectEqual(@as(usize, 1), state.pending_count);
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, first.@"error");
    try std.testing.expectEqual(@as(u32, 0), second.flags);
    table.cancel(&first);
    try std.testing.expectEqual(@as(usize, 1), state.pending_count);
    table.cancel(&second);
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
}

test "Windows ABI handle_close releases and clears handle variants" {
    const invalid_tokens = [_]u64{ 0, 1, 2, 0xffff };
    for (invalid_tokens) |token| {
        var state: PalState = .{};
        state.p_close_handle = abiTestCloseHandle;
        const table = testAbiTable(&state, null, 0);
        abi_test_close_calls = 0;
        abi_test_closed_handles = @splat(null);
        table.handle_close(token);
        try std.testing.expectEqual(@as(usize, 0), abi_test_close_calls);
    }

    const RegularCase = struct {
        handle: win.HANDLE,
        install_close: bool,
        expected_close_calls: usize,
    };
    const regular_cases = [_]RegularCase{
        .{ .handle = @ptrFromInt(0x10), .install_close = true, .expected_close_calls = 1 },
        .{ .handle = null, .install_close = true, .expected_close_calls = 0 },
        .{ .handle = win.INVALID_HANDLE_VALUE, .install_close = true, .expected_close_calls = 0 },
        .{ .handle = @ptrFromInt(0x20), .install_close = false, .expected_close_calls = 0 },
    };
    for (regular_cases) |case| {
        var state: PalState = .{};
        if (case.install_close) state.p_close_handle = abiTestCloseHandle;
        const entry = mem.allocHandle(&state).?;
        entry.handle = case.handle;
        const token = entry.token;
        const table = testAbiTable(&state, null, 0);
        abi_test_close_calls = 0;
        abi_test_closed_handles = @splat(null);
        table.handle_close(token);
        try std.testing.expectEqual(case.expected_close_calls, abi_test_close_calls);
        try std.testing.expect(mem.findHandle(&state, token) == null);
    }

    const PtyCase = struct { read: win.HANDLE, write: win.HANDLE, expected: usize };
    const pty_cases = [_]PtyCase{
        .{ .read = @ptrFromInt(0x31), .write = @ptrFromInt(0x32), .expected = 2 },
        .{ .read = @ptrFromInt(0x41), .write = null, .expected = 1 },
        .{ .read = null, .write = @ptrFromInt(0x52), .expected = 1 },
    };
    for (pty_cases) |case| {
        var state: PalState = .{};
        state.p_close_handle = abiTestCloseHandle;
        const entry = mem.allocHandle(&state).?;
        entry.is_pty = true;
        entry.pty_read = case.read;
        entry.pty_write = case.write;
        const token = entry.token;
        const table = testAbiTable(&state, null, 0);
        abi_test_close_calls = 0;
        abi_test_closed_handles = @splat(null);
        table.handle_close(token);
        try std.testing.expectEqual(case.expected, abi_test_close_calls);
        try std.testing.expect(mem.findHandle(&state, token) == null);
    }
}

test "Windows ABI signal_wait completes unsupported requests for every signal class" {
    const signals = [_]u32{ 0, 1, 2, 3, 9, 15, 31, 32, 127, 255, std.math.maxInt(u32) };
    var state: PalState = .{};
    const table = testAbiTable(&state, null, 0);

    for (signals) |signal| {
        var ov: pal_abi.Overlapped = .{
            .flags = 0xffff_ffff,
            .@"error" = 0xffff_ffff,
            .continued = 1,
            .result_ext = 2,
        };
        table.signal_wait(&ov, signal);
        try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
        try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
        try std.testing.expectEqual(@as(u64, 0), ov.continued);
        try std.testing.expectEqual(@as(u64, 0), ov.result_ext);
    }
}

test "Windows ABI tty_set_size preserves the explicit no-op success result" {
    const Case = struct { handle: u64, cols: u32, rows: u32 };
    const cases = [_]Case{
        .{ .handle = 0, .cols = 0, .rows = 0 },
        .{ .handle = 1, .cols = 1, .rows = 1 },
        .{ .handle = 2, .cols = 80, .rows = 24 },
        .{ .handle = 3, .cols = 120, .rows = 40 },
        .{ .handle = 0xffff, .cols = 255, .rows = 255 },
        .{ .handle = 1, .cols = 0, .rows = 24 },
        .{ .handle = 2, .cols = 80, .rows = 0 },
        .{ .handle = 3, .cols = 0xffff, .rows = 0xffff },
        .{ .handle = std.math.maxInt(u64), .cols = 1, .rows = 0xffff },
        .{ .handle = 0, .cols = std.math.maxInt(u32), .rows = 1 },
        .{ .handle = 2, .cols = 1, .rows = std.math.maxInt(u32) },
    };
    var state: PalState = .{};
    const table = testAbiTable(&state, null, 0);

    for (cases) |case| {
        try std.testing.expectEqual(@as(u32, 0), table.tty_set_size(case.handle, case.cols, case.rows));
    }
}

test "Windows ABI system_info_wait initializes unsupported completion fields" {
    const seeds = [_]u64{ 0, 1, 2, 3, 7, 15, 31, 63, 127, 255, std.math.maxInt(u64) };
    var state: PalState = .{};
    const table = testAbiTable(&state, null, 0);

    for (seeds) |seed| {
        var ov: pal_abi.Overlapped = .{
            .flags = @truncate(seed),
            .@"error" = @truncate(seed >> 32),
            .continued = seed,
            .result_ext = ~seed,
        };
        table.system_info_wait(&ov);
        try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
        try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
        try std.testing.expectEqual(@as(u64, 0), ov.continued);
        try std.testing.expectEqual(@as(u64, 0), ov.result_ext);
    }
}

test "Windows ABI free_mem releases every supplied range with MEM_RELEASE" {
    const Case = struct { offset: usize, requested_size: usize, result: i32 };
    const cases = [_]Case{
        .{ .offset = 0, .requested_size = 0, .result = 1 },
        .{ .offset = 1, .requested_size = 1, .result = 1 },
        .{ .offset = 2, .requested_size = 4095, .result = 0 },
        .{ .offset = 3, .requested_size = 4096, .result = 1 },
        .{ .offset = 4, .requested_size = 4097, .result = 0 },
        .{ .offset = 5, .requested_size = 8192, .result = 1 },
        .{ .offset = 6, .requested_size = 65_535, .result = 1 },
        .{ .offset = 7, .requested_size = 1 << 20, .result = 0 },
        .{ .offset = 8, .requested_size = std.math.maxInt(u32), .result = 1 },
        .{ .offset = 9, .requested_size = std.math.maxInt(usize), .result = 1 },
    };
    var state: PalState = .{};
    state.p_virtual_free = abiTestVirtualFree;
    const table = testAbiTable(&state, null, 0);

    for (cases) |case| {
        abi_test_free_calls = 0;
        abi_test_free_result = case.result;
        const ptr = test_memory_storage[case.offset..].ptr;
        table.free_mem(ptr, case.requested_size);
        try std.testing.expectEqual(@as(usize, 1), abi_test_free_calls);
        try std.testing.expectEqual(@intFromPtr(ptr), @intFromPtr(abi_test_free_address.?));
        try std.testing.expectEqual(@as(usize, 0), abi_test_free_size);
        try std.testing.expectEqual(@as(u32, 0x8000), abi_test_free_flags);
    }

    abi_test_free_calls = 0;
    state.p_virtual_free = null;
    table.free_mem(test_memory_storage[0..].ptr, 0);
    try std.testing.expectEqual(@as(usize, 0), abi_test_free_calls);
}

test "Windows ABI flush_icache forwards ranges and tolerates missing host APIs" {
    const Case = struct { offset: usize, size: usize, result: i32 };
    const cases = [_]Case{
        .{ .offset = 0, .size = 0, .result = 1 },
        .{ .offset = 1, .size = 1, .result = 1 },
        .{ .offset = 2, .size = 7, .result = 0 },
        .{ .offset = 3, .size = 8, .result = 1 },
        .{ .offset = 4, .size = 15, .result = 0 },
        .{ .offset = 5, .size = 4095, .result = 1 },
        .{ .offset = 6, .size = 4096, .result = 0 },
        .{ .offset = 7, .size = 8192, .result = 1 },
        .{ .offset = 8, .size = std.math.maxInt(u32), .result = 0 },
    };
    var state: PalState = .{};
    state.p_get_current_process = abiTestCurrentProcess;
    state.p_flush_icache = abiTestFlushInstructionCache;
    const table = testAbiTable(&state, null, 0);

    for (cases) |case| {
        abi_test_flush_calls = 0;
        abi_test_flush_result = case.result;
        const ptr = test_memory_storage[case.offset..].ptr;
        table.flush_icache(ptr, case.size);
        try std.testing.expectEqual(@as(usize, 1), abi_test_flush_calls);
        try std.testing.expectEqual(@as(usize, 0x44), @intFromPtr(abi_test_flush_process.?));
        try std.testing.expectEqual(@intFromPtr(ptr), @intFromPtr(abi_test_flush_address.?));
        try std.testing.expectEqual(case.size, abi_test_flush_size);
    }

    abi_test_flush_calls = 0;
    state.p_get_current_process = null;
    table.flush_icache(test_memory_storage[0..].ptr, 1);
    try std.testing.expectEqual(@as(usize, 0), abi_test_flush_calls);

    state.p_get_current_process = abiTestCurrentProcess;
    state.p_flush_icache = null;
    table.flush_icache(test_memory_storage[0..].ptr, 1);
    try std.testing.expectEqual(@as(usize, 0), abi_test_flush_calls);
}

test "Windows ABI wasm_bytes preserves optional payload pointers" {
    const payload = [_]u8{ 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11 };
    const pointers = [_]?[*]const u8{
        null,
        payload[0..].ptr,
        payload[1..].ptr,
        payload[2..].ptr,
        payload[3..].ptr,
        payload[4..].ptr,
        payload[5..].ptr,
        payload[6..].ptr,
        payload[7..].ptr,
        payload[8..].ptr,
        payload[9..].ptr,
    };
    var state: PalState = .{};

    for (pointers) |expected| {
        const table = testAbiTable(&state, expected, 0);
        try std.testing.expect(table.wasm_bytes == expected);
    }
}

test "Windows ABI wasm_len preserves payload lengths" {
    const payload = [_]u8{ 0, 1, 2, 3 };
    const lengths = [_]usize{
        0,
        1,
        2,
        3,
        4,
        7,
        8,
        15,
        16,
        65_536,
        std.math.maxInt(usize),
    };
    var state: PalState = .{};

    for (lengths) |expected| {
        const table = testAbiTable(&state, payload[0..].ptr, expected);
        try std.testing.expectEqual(expected, table.wasm_len);
    }
}

test "Windows ABI read covers zero-length handle classes and positional errors" {
    var buf: [1]u8 = .{0};

    var pty_state: PalState = .{};
    const pty = mem.allocHandle(&pty_state).?;
    pty.is_pty = true;
    const pty_table = testAbiTable(&pty_state, null, 0);
    var ov: pal_abi.Overlapped = .{};
    pty_table.read(&ov, pty.token, &buf, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);

    var socket_state: PalState = .{};
    const socket = mem.allocHandle(&socket_state).?;
    socket.is_socket = true;
    const socket_table = testAbiTable(&socket_state, null, 0);
    ov = .{};
    socket_table.read(&ov, socket.token, &buf, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);

    var missing_read_state: PalState = .{};
    const missing_read_handle = mem.allocHandle(&missing_read_state).?;
    missing_read_handle.is_overlapped = true;
    const missing_read_table = testAbiTable(&missing_read_state, null, 0);
    ov = .{};
    missing_read_table.read(&ov, missing_read_handle.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    var positional_state: PalState = .{};
    positional_state.p_read_file = abiTestReadFile;
    const positional_handle = mem.allocHandle(&positional_state).?;
    positional_handle.is_overlapped = true;
    const positional_table = testAbiTable(&positional_state, null, 0);
    var storage: [@sizeOf(pal_abi.Overlapped) + @sizeOf(i64)]u8 align(@alignOf(pal_abi.Overlapped)) = @splat(0);
    const positional_ov: *pal_abi.Overlapped = @ptrCast(@alignCast(&storage));
    positional_ov.flags = pal_abi.READ_FLAG_POSITIONAL;
    const offset: *i64 = @ptrCast(@alignCast(storage[@sizeOf(pal_abi.Overlapped)..].ptr));
    offset.* = -1;
    positional_table.read(positional_ov, positional_handle.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, positional_ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), positional_state.pending_count);
}

test "Windows ABI write covers stream, pty, handle, and positional cases" {
    var buf: [1]u8 = .{0};

    var stdout_state: PalState = .{};
    const stdout_table = testAbiTable(&stdout_state, null, 0);
    var ov: pal_abi.Overlapped = .{};
    stdout_table.write(&ov, 1, &buf, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);

    ov = .{};
    stdout_table.write(&ov, 2, &buf, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);

    var pty_state: PalState = .{};
    const pty = mem.allocHandle(&pty_state).?;
    pty.is_pty = true;
    const pty_table = testAbiTable(&pty_state, null, 0);
    ov = .{};
    pty_table.write(&ov, pty.token, &buf, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");

    var invalid_state: PalState = .{};
    const invalid_table = testAbiTable(&invalid_state, null, 0);
    ov = .{};
    invalid_table.write(&ov, 0xffff, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    var missing_write_state: PalState = .{};
    const missing_write_handle = mem.allocHandle(&missing_write_state).?;
    missing_write_handle.is_overlapped = true;
    missing_write_handle.access_mode = 1;
    const missing_write_table = testAbiTable(&missing_write_state, null, 0);
    ov = .{};
    missing_write_table.write(&ov, missing_write_handle.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");

    var positional_state: PalState = .{};
    positional_state.p_write_file = abiTestWriteFile;
    const positional_handle = mem.allocHandle(&positional_state).?;
    positional_handle.is_overlapped = true;
    positional_handle.access_mode = 1;
    const positional_table = testAbiTable(&positional_state, null, 0);
    var storage: [@sizeOf(pal_abi.Overlapped) + @sizeOf(i64)]u8 align(@alignOf(pal_abi.Overlapped)) = @splat(0);
    const positional_ov: *pal_abi.Overlapped = @ptrCast(@alignCast(&storage));
    positional_ov.flags = pal_abi.WRITE_FLAG_POSITIONAL;
    const offset: *i64 = @ptrCast(@alignCast(storage[@sizeOf(pal_abi.Overlapped)..].ptr));
    offset.* = -1;
    positional_table.write(positional_ov, positional_handle.token, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, positional_ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), positional_state.pending_count);

    var unavailable_worker_state: PalState = .{};
    const unavailable_worker_table = testAbiTable(&unavailable_worker_state, null, 0);
    ov = .{};
    unavailable_worker_table.write(&ov, 1, &buf, buf.len);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), unavailable_worker_state.pending_count);

    var pipe_state: PalState = .{};
    const pipe = mem.allocHandle(&pipe_state).?;
    pipe.is_pipe = true;
    pipe.is_overlapped = true;
    pipe.access_mode = 1;
    pipe_state.p_write_file = null;
    const pipe_table = testAbiTable(&pipe_state, null, 0);
    ov = .{};
    pipe_table.write(&ov, pipe.token, &buf, 0);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
}

test "Windows ABI read submits sequential and positional offsets" {
    const Case = struct { offset: u64, positional: bool };
    const cases = [_]Case{
        .{ .offset = 0, .positional = false },
        .{ .offset = 1, .positional = false },
        .{ .offset = 0xffff, .positional = false },
        .{ .offset = 0x1_0000, .positional = false },
        .{ .offset = 0x1_0000_0001, .positional = false },
        .{ .offset = 0, .positional = true },
        .{ .offset = 1, .positional = true },
        .{ .offset = 0x1_0000, .positional = true },
        .{ .offset = 0x1_0000_0001, .positional = true },
    };
    var buf: [4]u8 = .{ 1, 2, 3, 4 };

    for (cases) |case| {
        var state: PalState = .{};
        state.p_read_file = abiTestReadFile;
        const handle = mem.allocHandle(&state).?;
        handle.handle = @ptrFromInt(0x71);
        handle.is_overlapped = true;
        handle.seq_pos = case.offset;
        const table = testAbiTable(&state, null, 0);
        var storage: [@sizeOf(pal_abi.Overlapped) + @sizeOf(i64)]u8 align(@alignOf(pal_abi.Overlapped)) = @splat(0);
        const ov: *pal_abi.Overlapped = @ptrCast(@alignCast(&storage));
        if (case.positional) {
            ov.flags = pal_abi.READ_FLAG_POSITIONAL;
            const offset: *i64 = @ptrCast(@alignCast(storage[@sizeOf(pal_abi.Overlapped)..].ptr));
            offset.* = @intCast(case.offset);
        }

        abi_test_read_calls = 0;
        table.read(ov, handle.token, &buf, buf.len);
        try std.testing.expectEqual(@as(usize, 1), abi_test_read_calls);
        try std.testing.expectEqual(handle.handle, abi_test_read_handle);
        try std.testing.expectEqual(@as(u32, buf.len), abi_test_read_len);
        try std.testing.expectEqual(@as(u32, @truncate(case.offset)), abi_test_read_offset);
        try std.testing.expectEqual(@as(u32, @truncate(case.offset >> 32)), abi_test_read_offset_high);
        try std.testing.expectEqual(@as(usize, 1), state.pending_count);
        try std.testing.expectEqual(case.offset, state.ops[0].file_offset);
        try std.testing.expectEqual(!case.positional, state.ops[0].advance_seq);
    }
}

test "Windows ABI write submits sequential positional pipe and append requests" {
    const Case = struct {
        offset: u64,
        positional: bool = false,
        pipe: bool = false,
        append: bool = false,
    };
    const cases = [_]Case{
        .{ .offset = 0 },
        .{ .offset = 1 },
        .{ .offset = 0xffff },
        .{ .offset = 0x1_0000_0001 },
        .{ .offset = 0, .positional = true },
        .{ .offset = 0x1_0000_0001, .positional = true },
        .{ .offset = 0x1_0000_0001, .pipe = true },
        .{ .offset = 0x1_0000_0001, .append = true },
    };
    var buf: [4]u8 = .{ 4, 3, 2, 1 };

    for (cases) |case| {
        var state: PalState = .{};
        state.p_write_file = abiTestWriteFile;
        state.p_get_file_size_ex = abiTestGetFileSize;
        abi_test_file_size = @intCast(case.offset);
        const handle = mem.allocHandle(&state).?;
        handle.handle = @ptrFromInt(0x72);
        handle.is_overlapped = true;
        handle.access_mode = 1;
        handle.seq_pos = case.offset;
        handle.is_pipe = case.pipe;
        handle.append_mode = case.append;
        const table = testAbiTable(&state, null, 0);
        var storage: [@sizeOf(pal_abi.Overlapped) + @sizeOf(i64)]u8 align(@alignOf(pal_abi.Overlapped)) = @splat(0);
        const ov: *pal_abi.Overlapped = @ptrCast(@alignCast(&storage));
        if (case.positional) {
            ov.flags = pal_abi.WRITE_FLAG_POSITIONAL;
            const offset: *i64 = @ptrCast(@alignCast(storage[@sizeOf(pal_abi.Overlapped)..].ptr));
            offset.* = @intCast(case.offset);
        }

        abi_test_write_calls = 0;
        table.write(ov, handle.token, &buf, buf.len);
        try std.testing.expectEqual(@as(usize, 1), abi_test_write_calls);
        try std.testing.expectEqual(handle.handle, abi_test_write_handle);
        try std.testing.expectEqual(@as(u32, buf.len), abi_test_write_len);
        try std.testing.expectEqual(@as(usize, 1), state.pending_count);
        try std.testing.expectEqual(mem.OpKind.write, state.ops[0].kind);
        try std.testing.expectEqual(if (case.pipe) @as(u64, 0) else case.offset, state.ops[0].file_offset);
        try std.testing.expectEqual(case.append, state.ops[0].append_write);
        if (case.append) {
            try std.testing.expectEqual(@as(u32, 0xffff_ffff), abi_test_write_offset);
            try std.testing.expectEqual(@as(u32, 0xffff_ffff), abi_test_write_offset_high);
        } else if (case.pipe) {
            try std.testing.expectEqual(@as(u32, 0), abi_test_write_offset);
            try std.testing.expectEqual(@as(u32, 0), abi_test_write_offset_high);
        } else {
            try std.testing.expectEqual(@as(u32, @truncate(case.offset)), abi_test_write_offset);
            try std.testing.expectEqual(@as(u32, @truncate(case.offset >> 32)), abi_test_write_offset_high);
        }
    }
}
