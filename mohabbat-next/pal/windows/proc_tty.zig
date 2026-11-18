const std = @import("std");
const pal_abi = @import("pal_abi");
const win = @import("win_ffi.zig");
const mem = @import("mem.zig");
const PalState = mem.PalState;

fn createPipeName(s: *PalState, buf: *[96]u8) ?[*:0]u16 {
    const name = std.fmt.bufPrint(buf, "\\\\.\\pipe\\rusticated-{x}-{x}", .{ @intFromPtr(s), s.next_pipe_id }) catch return null;
    s.next_pipe_id += 1;
    return mem.utf8ToWideAlloc(s, name.ptr, name.len);
}

fn createPipeServer(s: *PalState, name: [*:0]const u16, direction: u32, inheritable: bool) ?win.HANDLE {
    const create = s.p_create_named_pipe_w orelse return null;
    var sa: win.SecurityAttributes = .{ .inherit_handle = @intFromBool(inheritable) };
    const h = create(name, direction | 0x4000_0000, 0, 1, 4096, 4096, 0, &sa);
    if (h == null or h == win.INVALID_HANDLE_VALUE) return null;
    return h;
}

fn openPipeClient(s: *PalState, name: [*:0]const u16, access: u32, inheritable: bool, overlapped: bool) ?win.HANDLE {
    const create = s.p_create_file_w orelse return null;
    var sa: win.SecurityAttributes = .{ .inherit_handle = @intFromBool(inheritable) };
    const flags: u32 = 0x80 | if (overlapped) @as(u32, 0x4000_0000) else 0;
    const h = create(name, access, 0, &sa, 3, flags, null);
    if (h == null or h == win.INVALID_HANDLE_VALUE) return null;
    return h;
}

fn associateWithIocp(s: *PalState, h: win.HANDLE) bool {
    const associate = s.p_create_iocp orelse return false;
    return associate(h, s.iocp, 0, 0) != null;
}

fn beginPipeConnect(s: *PalState, h: win.HANDLE, token: u64) bool {
    const connect = s.p_connect_named_pipe orelse return false;
    const op = mem.allocOp(s) orelse return false;
    op.kind = .pipe_connect;
    op.handle_token = token;
    op.native_io = true;
    if (connect(h, &op.win_ov) != 0) return true;

    const err = if (s.p_get_last_error) |get_error| get_error() else 0;
    if (err == 997) return true;
    if (err == 535) {
        op.native_io = false;
        const post = s.p_post_queued orelse {
            mem.freeOp(s, op);
            return false;
        };
        if (post(s.iocp, 0, @intFromPtr(op), &op.win_ov) != 0) return true;
    }
    mem.freeOp(s, op);
    return false;
}

const ChildPipe = struct {
    parent: *mem.HandleEntry,
    child: win.HANDLE,
};

fn createChildPipe(s: *PalState, direction: u32, client_access: u32, parent_access: u8) ?ChildPipe {
    var name_buf: [96]u8 = undefined;
    const name = createPipeName(s, &name_buf) orelse return null;
    defer if (s.host.free) |free| free(@ptrCast(@constCast(name)));

    const server = createPipeServer(s, name, direction, false) orelse return null;
    if (!associateWithIocp(s, server)) {
        if (s.p_close_handle) |close| _ = close(server);
        return null;
    }
    const parent = mem.allocHandle(s) orelse {
        if (s.p_close_handle) |close| _ = close(server);
        return null;
    };
    parent.handle = server;
    parent.is_pipe = true;
    parent.is_overlapped = true;
    parent.access_mode = parent_access;
    if (!beginPipeConnect(s, server, parent.token)) {
        parent.* = .{};
        if (s.p_close_handle) |close| _ = close(server);
        return null;
    }
    const child = openPipeClient(s, name, client_access, true, false) orelse {
        parent.* = .{};
        if (s.p_close_handle) |close| _ = close(server);
        return null;
    };
    return .{ .parent = parent, .child = child };
}

fn closeChildPipe(s: *PalState, pipe: ChildPipe) void {
    if (s.p_close_handle) |close| {
        _ = close(pipe.parent.handle);
        _ = close(pipe.child);
    }
    pipe.parent.* = .{};
}

fn processWaitWorker(ctx: ?*anyopaque) callconv(win.win_cc) u32 {
    const op: *mem.PendingOp = @ptrCast(@alignCast(ctx.?));
    const s: *PalState = @ptrCast(@alignCast(op.pal_state.?));
    const wait = s.p_wait_single orelse {
        op.win_error = pal_abi.ERROR_UNSUPPORTED;
        if (s.p_post_queued) |post| _ = post(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    const he = mem.findHandle(s, op.handle_token) orelse {
        op.win_error = pal_abi.ERROR_BADF;
        if (s.p_post_queued) |post| _ = post(s.iocp, 0, @intFromPtr(op), &op.win_ov);
        return 0;
    };
    while (true) {
        if (@atomicLoad(bool, &op.canceled, .acquire)) {
            op.win_error = pal_abi.ERROR_CANCELED;
            break;
        }
        const result = wait(he.handle, 50);
        if (result == 0) break;
        if (result == 258) continue; // WAIT_TIMEOUT
        op.win_error = if (s.p_get_last_error) |get_error| win.mapWinError(get_error()) else pal_abi.ERROR_IO;
        break;
    }

    if (op.win_error == 0) {
        const get_exit = mem.resolveKernel32(s, "GetExitCodeProcess") orelse {
            op.win_error = pal_abi.ERROR_UNSUPPORTED;
            if (s.p_post_queued) |post| _ = post(s.iocp, 0, @intFromPtr(op), &op.win_ov);
            return 0;
        };
        const ExitCodeFn = *const fn (win.HANDLE, *u32) callconv(win.win_cc) i32;
        const get_exit_code: ExitCodeFn = @ptrCast(@alignCast(get_exit));
        var code: u32 = 0;
        if (get_exit_code(he.handle, &code) == 0) {
            op.win_error = if (s.p_get_last_error) |get_error| win.mapWinError(get_error()) else pal_abi.ERROR_IO;
        } else {
            op.win_ov.internal_high = code;
        }
    }
    if (s.p_post_queued) |post| _ = post(s.iocp, 0, @intFromPtr(op), &op.win_ov);
    return 0;
}

pub fn processPipe(s: *PalState, ov: *pal_abi.Overlapped) void {
    var name_buf: [96]u8 = undefined;
    const name = createPipeName(s, &name_buf) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(name)));

    const r_h = createPipeServer(s, name, 1, false) orelse {
        const err = if (s.p_get_last_error) |get_error| get_error() else 0;
        mem.completeInline(ov, win.mapWinError(err), 0, 0);
        return;
    };
    if (!associateWithIocp(s, r_h)) {
        if (s.p_close_handle) |close| {
            _ = close(r_h);
        }
        const err = if (s.p_get_last_error) |get_error| get_error() else 0;
        mem.completeInline(ov, win.mapWinError(err), 0, 0);
        return;
    }
    const r_he = mem.allocHandle(s) orelse {
        if (s.p_close_handle) |close| _ = close(r_h);
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    r_he.handle = r_h;
    r_he.is_pipe = true;
    r_he.is_overlapped = true;
    if (!beginPipeConnect(s, r_h, r_he.token)) {
        r_he.* = .{};
        if (s.p_close_handle) |close| _ = close(r_h);
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    }
    const w_h = openPipeClient(s, name, 0x4000_0000, false, true) orelse {
        r_he.* = .{};
        if (s.p_close_handle) |close| _ = close(r_h);
        const err = if (s.p_get_last_error) |get_error| get_error() else 0;
        mem.completeInline(ov, win.mapWinError(err), 0, 0);
        return;
    };
    if (!associateWithIocp(s, w_h)) {
        r_he.* = .{};
        if (s.p_close_handle) |close| {
            _ = close(r_h);
            _ = close(w_h);
        }
        const err = if (s.p_get_last_error) |get_error| get_error() else 0;
        mem.completeInline(ov, win.mapWinError(err), 0, 0);
        return;
    }
    const w_he = mem.allocHandle(s) orelse {
        r_he.* = .{};
        if (s.p_close_handle) |close| {
            _ = close(r_h);
            _ = close(w_h);
        }
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    w_he.handle = w_h;
    w_he.is_pipe = true;
    w_he.is_overlapped = true;
    w_he.access_mode = 1;
    const packed_handles: u64 = (w_he.token << 32) | (r_he.token & 0xffff_ffff);
    mem.completeInline(ov, 0, 0, packed_handles);
}

fn nextNullStr(buf: []const u8, pos: *usize) ?[]const u8 {
    if (pos.* >= buf.len) return null;
    const start = pos.*;
    var end = start;
    while (end < buf.len and buf[end] != 0) : (end += 1) {}
    if (end == start) {
        pos.* = if (end < buf.len) end + 1 else buf.len;
        return "";
    }
    pos.* = if (end < buf.len) end + 1 else buf.len;
    return buf[start..end];
}

fn appendCmdArg(buf: []u8, pos: *usize, arg: []const u8) void {
    if (pos.* > 0 and pos.* < buf.len) {
        buf[pos.*] = ' ';
        pos.* += 1;
    }
    const needs_quotes = arg.len == 0 or std.mem.indexOfAny(u8, arg, " \t\"") != null;
    if (needs_quotes and pos.* < buf.len) {
        buf[pos.*] = '"';
        pos.* += 1;
    }
    for (arg) |b| {
        if (b == '"' and pos.* + 2 <= buf.len) {
            buf[pos.*] = '\\';
            buf[pos.* + 1] = '"';
            pos.* += 2;
        } else if (pos.* < buf.len) {
            buf[pos.*] = b;
            pos.* += 1;
        }
    }
    if (needs_quotes and pos.* < buf.len) {
        buf[pos.*] = '"';
        pos.* += 1;
    }
}

pub fn processSpawn(s: *PalState, ov: *pal_abi.Overlapped, cfg_ptr: [*]const u8, cfg_len: usize) void {
    const p_cp = mem.resolveKernel32(s, "CreateProcessW") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const CreateProcFn = *const fn (?[*:0]const u16, ?[*:0]u16, ?*anyopaque, ?*anyopaque, i32, u32, ?*anyopaque, ?[*:0]const u16, *win.StartupInfoW, *win.ProcessInformation) callconv(win.win_cc) i32;
    const create_proc: CreateProcFn = @ptrCast(@alignCast(p_cp));

    const cfg = cfg_ptr[0..cfg_len];
    var pos: usize = 0;
    const prog = nextNullStr(cfg, &pos) orelse {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    if (prog.len == 0) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }

    var cmd_buf: [8192]u8 = undefined;
    var cmd_len: usize = 0;
    appendCmdArg(&cmd_buf, &cmd_len, prog);
    while (nextNullStr(cfg, &pos)) |arg| {
        if (arg.len == 0) break;
        appendCmdArg(&cmd_buf, &cmd_len, arg);
    }

    // Skip env section (until empty string)
    while (nextNullStr(cfg, &pos)) |env_item| {
        if (env_item.len == 0) break;
    }

    // Optional cwd (single null-terminated string; empty if none)
    var cwd_slice: []const u8 = "";
    if (nextNullStr(cfg, &pos)) |c| {
        cwd_slice = c;
    }

    // Optional stdio section: up to 3 specs ("inherit", "null", "fd:<token>")
    var stdio_specs: [3][]const u8 = .{ "", "", "" };
    var has_stdio = false;
    var si_idx: usize = 0;
    while (nextNullStr(cfg, &pos)) |spec| {
        if (spec.len == 0) break;
        has_stdio = true;
        if (si_idx < 3) {
            stdio_specs[si_idx] = spec;
            si_idx += 1;
        }
    }

    const wcmd = mem.utf8ToWideAlloc(s, &cmd_buf, cmd_len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    defer if (s.host.free) |free| free(@ptrCast(@constCast(wcmd)));

    var wcwd: ?[*:0]u16 = null;
    if (cwd_slice.len > 0) {
        wcwd = mem.utf8ToWideAlloc(s, cwd_slice.ptr, cwd_slice.len);
    }
    defer if (wcwd) |w| {
        if (s.host.free) |free| free(@ptrCast(@constCast(w)));
    };

    var sa: win.SecurityAttributes = .{ .inherit_handle = 1 };
    const input_pipe: ?ChildPipe = null;
    const output_pipe: ?ChildPipe = null;
    const error_pipe: ?ChildPipe = null;

    var child_in: win.HANDLE = null;
    var child_out: win.HANDLE = null;
    var child_err: win.HANDLE = null;
    var dup_to_close: [3]win.HANDLE = .{ null, null, null };

    if (has_stdio) {
        const p_dup = mem.resolveKernel32(s, "DuplicateHandle");
        const p_cur = s.p_get_current_process;
        const DupFn = *const fn (win.HANDLE, win.HANDLE, win.HANDLE, *win.HANDLE, u32, i32, u32) callconv(win.win_cc) i32;
        const cur_proc = if (p_cur) |cp| cp() else null;
        const nul_w = [_:0]u16{ 'N', 'U', 'L' };
        const defaults: [3]win.HANDLE = .{ s.stdin_h, s.stdout_h, s.stderr_h };
        const child_slots: [3]*win.HANDLE = .{ &child_in, &child_out, &child_err };
        for (0..3) |idx| {
            const spec = stdio_specs[idx];
            var src: win.HANDLE = defaults[idx];
            if (std.mem.eql(u8, spec, "null")) {
                if (s.p_create_file_w) |cf| {
                    const access: u32 = if (idx == 0) 0x8000_0000 else 0x4000_0000;
                    const nh = cf(&nul_w, access, 3, &sa, 3, 0x80, null);
                    if (nh != null and nh != win.INVALID_HANDLE_VALUE) {
                        child_slots[idx].* = nh;
                        dup_to_close[idx] = nh;
                        continue;
                    }
                }
            } else if (std.mem.startsWith(u8, spec, "fd:")) {
                const tok = std.fmt.parseInt(u64, spec[3..], 10) catch 0;
                if (tok == 0) {
                    src = s.stdin_h;
                } else if (tok == 1) {
                    src = s.stdout_h;
                } else if (tok == 2) {
                    src = s.stderr_h;
                } else if (mem.findHandle(s, tok)) |fhe| {
                    src = fhe.handle;
                }
            }
            if (p_dup != null and cur_proc != null and src != null and src != win.INVALID_HANDLE_VALUE) {
                const dup: DupFn = @ptrCast(@alignCast(p_dup.?));
                var dh: win.HANDLE = null;
                if (dup(cur_proc, src, cur_proc, &dh, 0, 1, 2) != 0) { // DUPLICATE_SAME_ACCESS=2, bInheritHandle=1
                    child_slots[idx].* = dh;
                    dup_to_close[idx] = dh;
                    continue;
                }
            }
            child_slots[idx].* = src;
        }
    } else {
        child_in = s.stdin_h;
        child_out = s.stdout_h;
        child_err = s.stderr_h;
    }

    var si: win.StartupInfoW = .{
        .flags = 0x100, // STARTF_USESTDHANDLES
        .std_input = child_in,
        .std_output = child_out,
        .std_error = child_err,
    };
    var pi: win.ProcessInformation = .{};

    // CREATE_NO_WINDOW = 0x08000000
    const ok = create_proc(null, @constCast(wcmd), null, null, 1, 0x0800_0000, null, wcwd, &si, &pi);
    if (s.p_close_handle) |ch| {
        if (has_stdio) {
            for (dup_to_close) |dh| {
                if (dh != null) _ = ch(dh);
            }
        }
    }
    if (ok == 0) {
        if (!has_stdio) {
            if (input_pipe) |pipe| closeChildPipe(s, pipe);
            if (output_pipe) |pipe| closeChildPipe(s, pipe);
            if (error_pipe) |pipe| closeChildPipe(s, pipe);
        }
        const gle = s.p_get_last_error orelse {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        };
        mem.completeInline(ov, win.mapWinError(gle()), 0, 0);
        return;
    }
    if (s.p_close_handle) |ch| _ = ch(pi.thread);

    const proc_he = mem.allocHandle(s) orelse {
        if (s.p_close_handle) |close| _ = close(pi.process);
        if (!has_stdio) {
            if (input_pipe) |pipe| closeChildPipe(s, pipe);
            if (output_pipe) |pipe| closeChildPipe(s, pipe);
            if (error_pipe) |pipe| closeChildPipe(s, pipe);
        }
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    proc_he.handle = pi.process;
    proc_he.is_process = true;

    const pso: *pal_abi.ProcessSpawnOverlapped = @ptrCast(@alignCast(ov));
    pso.child_handle = proc_he.token;
    pso.pty_handle = 0;
    if (!has_stdio) {
        pso.in_handle = 0;
        pso.out_handle = 0;
        pso.err_handle = 0;
    } else {
        pso.in_handle = 0;
        pso.out_handle = 0;
        pso.err_handle = 0;
    }
    mem.completeInline(ov, 0, 0, proc_he.token);
}

pub fn processWait(s: *PalState, ov: *pal_abi.Overlapped, proc_handle: u64) void {
    const he = mem.findHandle(s, proc_handle) orelse {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_process) {
        mem.completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    }
    if (mem.resolveKernel32(s, "WaitForSingleObject")) |p_wait| {
        if (mem.resolveKernel32(s, "GetExitCodeProcess")) |p_exit| {
            const WaitFn = *const fn (win.HANDLE, u32) callconv(win.win_cc) u32;
            const GetExitFn = *const fn (win.HANDLE, *u32) callconv(win.win_cc) i32;
            const wait_fn: WaitFn = @ptrCast(@alignCast(p_wait));
            const get_exit: GetExitFn = @ptrCast(@alignCast(p_exit));
            var code: u32 = 0;
            const result = wait_fn(he.handle, 100);
            if (result == 0) {
                if (get_exit(he.handle, &code) != 0) {
                    mem.completeInline(ov, 0, 0, code);
                    return;
                }
            }
        }
    }
    const op = mem.allocOp(s) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .proc_wait;
    op.guest_ov = ov;
    op.handle_token = proc_handle;
    op.pal_state = s;
    const create_thread = s.p_create_thread orelse {
        mem.freeOp(s, op);
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const thread = create_thread(null, 0, processWaitWorker, op, 0, null);
    if (thread == null) {
        mem.freeOp(s, op);
        const err = if (s.p_get_last_error) |get_error| win.mapWinError(get_error()) else pal_abi.ERROR_IO;
        mem.completeInline(ov, err, 0, 0);
        return;
    }
    op.worker_handle = thread;
}

pub fn processSignal(s: *PalState, proc_handle: u64, _: u32) void {
    const he = mem.findHandle(s, proc_handle) orelse return;
    if (!he.is_process) return;
    if (mem.resolveKernel32(s, "TerminateProcess")) |p_term| {
        const TermFn = *const fn (win.HANDLE, u32) callconv(win.win_cc) i32;
        const term: TermFn = @ptrCast(@alignCast(p_term));
        _ = term(he.handle, 1);
    }
}

pub fn ttySetMode(s: *PalState, handle: u64, raw_mode: u32) void {
    const h = if (handle == 0) s.stdin_h else if (handle == 1) s.stdout_h else if (handle == 2) s.stderr_h else return;
    const p_get = mem.resolveKernel32(s, "GetConsoleMode") orelse return;
    const p_set = mem.resolveKernel32(s, "SetConsoleMode") orelse return;
    const GetModeFn = *const fn (win.HANDLE, *u32) callconv(win.win_cc) i32;
    const SetModeFn = *const fn (win.HANDLE, u32) callconv(win.win_cc) i32;
    const get_mode: GetModeFn = @ptrCast(@alignCast(p_get));
    const set_mode: SetModeFn = @ptrCast(@alignCast(p_set));

    var mode: u32 = 0;
    if (get_mode(h, &mode) == 0) return;
    if (handle == 0) {
        if (!s.has_saved_in_mode) {
            s.saved_in_mode = mode;
            s.has_saved_in_mode = true;
        }
        if (raw_mode != 0) {
            // Disable line input (2), echo (4), processed input (1); enable VT input (0x200)
            _ = set_mode(h, (mode & ~@as(u32, 0x7)) | 0x200);
        } else if (s.has_saved_in_mode) {
            _ = set_mode(h, s.saved_in_mode);
        }
    }
}

pub fn ttyGetSize(s: *PalState, handle: u64) u32 {
    const h = if (handle == 1) s.stdout_h else if (handle == 2) s.stderr_h else return (80 << 16) | 24;
    const p_fn = mem.resolveKernel32(s, "GetConsoleScreenBufferInfo") orelse return (80 << 16) | 24;
    const CsbiFn = *const fn (win.HANDLE, *win.ConsoleScreenBufferInfo) callconv(win.win_cc) i32;
    const get_csbi: CsbiFn = @ptrCast(@alignCast(p_fn));
    var info: win.ConsoleScreenBufferInfo = .{};
    if (get_csbi(h, &info) == 0) return (80 << 16) | 24;
    const cols: u32 = @intCast(@max(1, info.window.right - info.window.left + 1));
    const rows: u32 = @intCast(@max(1, info.window.bottom - info.window.top + 1));
    return (cols << 16) | (rows & 0xffff);
}

pub fn fdIsatty(s: *PalState, handle: u32) u32 {
    const h = switch (handle) {
        0 => s.stdin_h,
        1 => s.stdout_h,
        2 => s.stderr_h,
        else => return 0,
    };
    const p_get = mem.resolveKernel32(s, "GetConsoleMode") orelse return 0;
    const GetModeFn = *const fn (win.HANDLE, *u32) callconv(win.win_cc) i32;
    const get_mode: GetModeFn = @ptrCast(@alignCast(p_get));
    var mode: u32 = 0;
    return if (get_mode(h, &mode) != 0) 1 else 0;
}
