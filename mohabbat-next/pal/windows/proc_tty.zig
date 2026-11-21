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
    const resolved_prog: []const u8 = if (std.mem.eql(u8, prog, "cmd.exe"))
        "C:\\Windows\\System32\\cmd.exe"
    else
        prog;
    if (std.mem.eql(u8, prog, "cmd.exe")) {
        appendCmdArg(&cmd_buf, &cmd_len, "C:\\Windows\\System32\\cmd.exe");
    } else {
        appendCmdArg(&cmd_buf, &cmd_len, prog);
    }
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
    defer mem.freeWideAlloc(s, @ptrCast(@constCast(wcmd)));
    const wprog = mem.utf8ToWideAlloc(s, resolved_prog.ptr, resolved_prog.len) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    defer mem.freeWideAlloc(s, @ptrCast(@constCast(wprog)));

    var wcwd: ?[*:0]u16 = null;
    if (cwd_slice.len > 0) {
        wcwd = mem.utf8ToWideAlloc(s, cwd_slice.ptr, cwd_slice.len);
    }
    defer if (wcwd) |w| {
        mem.freeWideAlloc(s, @ptrCast(@constCast(w)));
    };

    var pipe_sa: win.SecurityAttributes = .{ .inherit_handle = 1 };
    const input_pipe: ?ChildPipe = null;
    const output_pipe: ?ChildPipe = null;
    const error_pipe: ?ChildPipe = null;

    var child_in: win.HANDLE = null;
    var child_out: win.HANDLE = null;
    var child_err: win.HANDLE = null;
    var dup_to_close: [3]win.HANDLE = .{ null, null, null };
    var pty_read_handle: win.HANDLE = null;
    var pty_write_handle: win.HANDLE = null;
    var pty_enabled = false;

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
            if (std.mem.startsWith(u8, spec, "pty:")) {
                pty_enabled = true;
                if (mem.resolveKernel32(s, "CreatePipe")) |p_create_pipe| {
                    const CreatePipeFn = *const fn (*win.HANDLE, *win.HANDLE, *win.SecurityAttributes, u32) callconv(win.win_cc) i32;
                    const create_pipe: CreatePipeFn = @ptrCast(@alignCast(p_create_pipe));
                    var in_r: win.HANDLE = null;
                    var in_w: win.HANDLE = null;
                    var out_r: win.HANDLE = null;
                    var out_w: win.HANDLE = null;
                    if (create_pipe(&in_r, &in_w, &pipe_sa, 4096) != 0 and create_pipe(&out_r, &out_w, &pipe_sa, 4096) != 0) {
                        pty_read_handle = out_r;
                        pty_write_handle = in_w;
                        child_in = in_r;
                        child_out = out_w;
                        child_err = out_w;
                        continue;
                    }
                }
            }
            if (std.mem.eql(u8, spec, "null")) {
                if (s.p_create_file_w) |cf| {
                    const access: u32 = if (idx == 0) 0x8000_0000 else 0x4000_0000;
                    const nh = cf(&nul_w, access, 3, &pipe_sa, 3, 0x80, null);
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
    var ok = create_proc(wprog, @constCast(wcmd), null, null, 1, 0x0800_0000, null, wcwd, &si, &pi);
    if (ok == 0) {
        @memset(std.mem.asBytes(&pi), 0);
        ok = create_proc(null, @constCast(wcmd), null, null, 1, 0x0800_0000, null, wcwd, &si, &pi);
    }
    if (ok == 0) {
        @memset(std.mem.asBytes(&pi), 0);
        const bare_cmd = mem.utf8ToWideAlloc(s, "cmd.exe", "cmd.exe".len) orelse null;
        defer if (bare_cmd) |b| {
            mem.freeWideAlloc(s, @ptrCast(@constCast(b)));
        };
        if (bare_cmd) |b| {
            ok = create_proc(b, @constCast(wcmd), null, null, 1, 0x0800_0000, null, wcwd, &si, &pi);
        }
    }
    if (s.p_close_handle) |ch| {
        if (has_stdio) {
            for (dup_to_close) |dh| {
                if (dh != null) _ = ch(dh);
            }
            if (pty_enabled) {
                if (child_in != null) _ = ch(child_in);
                if (child_out != null) _ = ch(child_out);
                if (child_err != null and child_err != child_out) _ = ch(child_err);
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
        const raw = gle();
        mem.completeInline(ov, win.mapWinError(raw), raw, 0);
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
    if (pty_enabled) {
        const pty_he = mem.allocHandle(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        pty_he.is_pty = true;
        pty_he.pty_read = pty_read_handle;
        pty_he.pty_write = pty_write_handle;
        pso.pty_handle = pty_he.token;
        pso.in_handle = 0;
        pso.out_handle = pty_he.token;
        pso.err_handle = 0;
    } else if (!has_stdio) {
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

pub fn ttySetSize(s: *PalState, handle: u64, cols: u32, rows: u32) u32 {
    if (cols == 0 or rows == 0 or cols > 32767 or rows > 32767) {
        return pal_abi.ERROR_INVALID;
    }
    if (handle >= 3) {
        const he = mem.findHandle(s, handle) orelse return pal_abi.ERROR_BADF;
        if (he.is_pty) {
            const p_resize = mem.resolveKernel32(s, "ResizePseudoConsole") orelse return pal_abi.ERROR_UNSUPPORTED;
            const ResizeFn = *const fn (win.HANDLE, win.Coord) callconv(win.win_cc) i32;
            const resize: ResizeFn = @ptrCast(@alignCast(p_resize));
            const hr = resize(he.handle, .{ .x = @intCast(cols), .y = @intCast(rows) });
            return if (hr == 0) 0 else pal_abi.ERROR_IO;
        }
        return pal_abi.ERROR_NOTTY;
    }
    if (fdIsatty(s, @intCast(handle)) == 0) {
        return pal_abi.ERROR_NOTTY;
    }
    return 0;
}

pub fn signalWait(s: *PalState, ov: *pal_abi.Overlapped, signum: u32) void {
    if (signum == 0) {
        mem.completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    // Replace any existing waiter for the same signal number (matches washmhost semantics).
    for (&s.ops) |*op| {
        if (op.used and op.kind == .signal_wait and op.handle_token == signum) {
            const prev_ov = op.guest_ov.?;
            mem.freeOp(s, op);
            mem.completeInline(prev_ov, pal_abi.ERROR_CANCELED, 0, 0);
            break;
        }
    }
    const op = mem.allocOp(s) orelse {
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .signal_wait;
    op.guest_ov = ov;
    op.handle_token = signum;
}

pub fn dispatchSignal(s: *PalState, signum: u32) void {
    for (&s.ops) |*op| {
        if (op.used and op.kind == .signal_wait and op.handle_token == signum) {
            const gov = op.guest_ov.?;
            mem.freeOp(s, op);
            mem.completeInline(gov, 0, 0, 0);
        }
    }
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

test "process configuration strings preserve empty fields and stop at buffer end" {
    const config = [_]u8{ 0, 'p', 'r', 'o', 'g', 0, 0, 'x' };
    var pos: usize = 0;

    try std.testing.expectEqualStrings("", nextNullStr(&config, &pos).?);
    try std.testing.expectEqual(@as(usize, 1), pos);
    try std.testing.expectEqualStrings("prog", nextNullStr(&config, &pos).?);
    try std.testing.expectEqualStrings("", nextNullStr(&config, &pos).?);
    try std.testing.expectEqualStrings("x", nextNullStr(&config, &pos).?);
    try std.testing.expect(nextNullStr(&config, &pos) == null);
    try std.testing.expectEqual(config.len, pos);
}

test "process command line quoting handles empty spaced and quoted arguments" {
    var buf: [64]u8 = undefined;
    var pos: usize = 0;

    appendCmdArg(&buf, &pos, "program");
    appendCmdArg(&buf, &pos, "two words");
    appendCmdArg(&buf, &pos, "say\"hi");
    appendCmdArg(&buf, &pos, "");

    try std.testing.expectEqualStrings("program \"two words\" \"say\\\"hi\" \"\"", buf[0..pos]);
}

test "process wait rejects invalid handles and reports missing worker support" {
    var s: PalState = .{};
    var ov: pal_abi.Overlapped = .{};

    processWait(&s, &ov, 0);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);

    const non_process = mem.allocHandle(&s).?;
    processWait(&s, &ov, non_process.token);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, ov.@"error");

    const process = mem.allocHandle(&s).?;
    process.is_process = true;
    processWait(&s, &ov, process.token);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), s.pending_count);
}

test "TTY helpers return safe fallbacks for unsupported handles" {
    var s: PalState = .{};

    try std.testing.expectEqual((@as(u32, 80) << 16) | 24, ttyGetSize(&s, 1));
    try std.testing.expectEqual((@as(u32, 80) << 16) | 24, ttyGetSize(&s, 3));
    try std.testing.expectEqual(@as(u32, 0), fdIsatty(&s, 3));
}

var pipe_alloc_storage: [4096]u8 align(16) = @splat(0);
var pipe_alloc_calls: usize = 0;
var pipe_free_calls: usize = 0;
var pipe_server_calls: usize = 0;
var pipe_client_calls: usize = 0;
var pipe_iocp_calls: usize = 0;
var pipe_connect_calls: usize = 0;
var pipe_post_calls: usize = 0;
var pipe_close_calls: usize = 0;
var pipe_last_error: u32 = 0;
var pipe_server_result: win.HANDLE = @ptrFromInt(0x10);
var pipe_client_result: win.HANDLE = @ptrFromInt(0x20);
var pipe_iocp_fail_at: usize = 0;
var pipe_connect_result: i32 = 1;
var pipe_connect_error: u32 = 0;

fn pipeTestAlloc(_: usize) callconv(win.win_cc) ?*anyopaque {
    pipe_alloc_calls += 1;
    return &pipe_alloc_storage;
}

fn pipeTestFree(_: ?*anyopaque) callconv(win.win_cc) void {
    pipe_free_calls += 1;
}

fn pipeTestUtf8ToWide(_: u32, _: u32, input: [*]const u8, input_len: i32, out: ?[*]u16, _: i32) callconv(win.win_cc) i32 {
    if (input_len <= 0) return 0;
    const len: usize = @intCast(input_len);
    if (out) |wide| for (input[0..len], 0..) |byte, i| {
        wide[i] = byte;
    };
    return input_len;
}

fn pipeTestCreateServer(_: [*:0]const u16, _: u32, _: u32, _: u32, _: u32, _: u32, _: u32, _: ?*anyopaque) callconv(win.win_cc) win.HANDLE {
    pipe_server_calls += 1;
    return pipe_server_result;
}

fn pipeTestOpenClient(_: [*:0]const u16, _: u32, _: u32, _: ?*anyopaque, _: u32, _: u32, _: win.HANDLE) callconv(win.win_cc) win.HANDLE {
    pipe_client_calls += 1;
    return pipe_client_result;
}

fn pipeTestAssociate(_: win.HANDLE, _: win.HANDLE, _: usize, _: u32) callconv(win.win_cc) win.HANDLE {
    pipe_iocp_calls += 1;
    return if (pipe_iocp_fail_at == pipe_iocp_calls) null else @ptrFromInt(0x30);
}

fn pipeTestConnect(_: win.HANDLE, _: *win.WinOverlapped) callconv(win.win_cc) i32 {
    pipe_connect_calls += 1;
    return pipe_connect_result;
}

fn pipeTestPost(_: win.HANDLE, _: u32, _: usize, _: ?*win.WinOverlapped) callconv(win.win_cc) i32 {
    pipe_post_calls += 1;
    return 1;
}

fn pipeTestClose(_: win.HANDLE) callconv(win.win_cc) i32 {
    pipe_close_calls += 1;
    return 1;
}

fn pipeTestLastError() callconv(win.win_cc) u32 {
    return pipe_last_error;
}

fn resetPipeTest(state: *PalState) void {
    pipe_alloc_calls = 0;
    pipe_free_calls = 0;
    pipe_server_calls = 0;
    pipe_client_calls = 0;
    pipe_iocp_calls = 0;
    pipe_connect_calls = 0;
    pipe_post_calls = 0;
    pipe_close_calls = 0;
    pipe_last_error = 5;
    pipe_server_result = @ptrFromInt(0x10);
    pipe_client_result = @ptrFromInt(0x20);
    pipe_iocp_fail_at = 0;
    pipe_connect_result = 1;
    pipe_connect_error = 0;
    state.host.alloc = pipeTestAlloc;
    state.host.free = pipeTestFree;
    state.p_mb_to_wide = pipeTestUtf8ToWide;
    state.p_create_named_pipe_w = pipeTestCreateServer;
    state.p_create_file_w = pipeTestOpenClient;
    state.p_create_iocp = pipeTestAssociate;
    state.p_connect_named_pipe = pipeTestConnect;
    state.p_post_queued = pipeTestPost;
    state.p_close_handle = pipeTestClose;
    state.p_get_last_error = pipeTestLastError;
    state.iocp = @ptrFromInt(0x30);
}

test "Windows process pipe reports missing name-conversion support" {
    var state: PalState = .{};
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_NOMEM, ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
}

test "Windows process pipe maps server-creation failure" {
    var state: PalState = .{};
    resetPipeTest(&state);
    pipe_server_result = null;
    pipe_last_error = 5;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), pipe_close_calls);
}

test "Windows process pipe maps a missing server error to no-entry" {
    var state: PalState = .{};
    resetPipeTest(&state);
    pipe_server_result = null;
    pipe_last_error = 2;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), pipe_server_calls);
}

test "Windows process pipe closes its server when IOCP association fails" {
    var state: PalState = .{};
    resetPipeTest(&state);
    pipe_iocp_fail_at = 1;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), pipe_close_calls);
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
}

test "Windows process pipe reports handle-table exhaustion" {
    var state: PalState = .{};
    resetPipeTest(&state);
    for (&state.handles) |*handle| handle.used = true;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_NOMEM, ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), pipe_close_calls);
}

test "Windows process pipe rejects unavailable ConnectNamedPipe" {
    var state: PalState = .{};
    resetPipeTest(&state);
    state.p_connect_named_pipe = null;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_IO, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
    try std.testing.expectEqual(@as(usize, 1), pipe_close_calls);
}

test "Windows process pipe keeps pending connections in the IOCP queue" {
    var state: PalState = .{};
    resetPipeTest(&state);
    pipe_connect_result = 0;
    pipe_connect_error = 997;
    state.p_get_last_error = struct {
        fn lastError() callconv(win.win_cc) u32 {
            return pipe_connect_error;
        }
    }.lastError;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), state.pending_count);
    try std.testing.expectEqual(@as(usize, 0), pipe_post_calls);
}

test "Windows process pipe queues an already-connected server" {
    var state: PalState = .{};
    resetPipeTest(&state);
    pipe_connect_result = 0;
    pipe_connect_error = 535;
    state.p_get_last_error = struct {
        fn lastError() callconv(win.win_cc) u32 {
            return pipe_connect_error;
        }
    }.lastError;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), pipe_post_calls);
    try std.testing.expectEqual(@as(usize, 1), state.pending_count);
}

test "Windows process pipe closes the server when client open fails" {
    var state: PalState = .{};
    resetPipeTest(&state);
    pipe_client_result = null;
    pipe_last_error = 109;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_PIPE, ov.@"error");
    try std.testing.expectEqual(@as(usize, 1), pipe_client_calls);
    try std.testing.expectEqual(@as(usize, 1), pipe_close_calls);
}

test "Windows process pipe closes both endpoints when client association fails" {
    var state: PalState = .{};
    resetPipeTest(&state);
    pipe_iocp_fail_at = 2;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    try std.testing.expectEqual(@as(usize, 2), pipe_close_calls);
}

test "Windows process pipe cleans up the server when the client handle table is full" {
    var state: PalState = .{};
    resetPipeTest(&state);
    for (&state.handles) |*handle| handle.used = true;
    state.handles[0].used = false;
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(pal_abi.ERROR_NOMEM, ov.@"error");
    try std.testing.expectEqual(@as(usize, 2), pipe_close_calls);
    try std.testing.expect(!state.handles[0].used);
}

test "Windows process pipe returns distinct read and write tokens on success" {
    var state: PalState = .{};
    resetPipeTest(&state);
    var ov: pal_abi.Overlapped = .{};
    processPipe(&state, &ov);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(@as(usize, 2), pipe_iocp_calls);
    const read_token: u64 = ov.result_ext & 0xffff_ffff;
    const write_token: u64 = ov.result_ext >> 32;
    try std.testing.expect(read_token >= 3);
    try std.testing.expect(write_token > read_token);
    try std.testing.expect(mem.findHandle(&state, read_token).?.is_pipe);
    try std.testing.expectEqual(@as(u8, 1), mem.findHandle(&state, write_token).?.access_mode);
}

var process_alloc_storage: [8][8192]u8 align(16) = @splat(@splat(0));
var process_alloc_index: usize = 0;
var spawn_calls: usize = 0;
var spawn_failures: usize = 0;
var spawn_error: u32 = 0;
var spawn_last_application: [512]u16 = @splat(0);
var spawn_last_command: [2048]u16 = @splat(0);
var spawn_last_cwd: [512]u16 = @splat(0);
var spawn_last_has_application: bool = false;
var spawn_last_has_cwd: bool = false;
var spawn_last_flags: u32 = 0;
var spawn_last_startup_flags: u32 = 0;
var spawn_last_stdin: win.HANDLE = null;
var spawn_last_stdout: win.HANDLE = null;
var spawn_last_stderr: win.HANDLE = null;
var spawn_close_calls: usize = 0;

fn processTestAlloc(_: usize) callconv(win.win_cc) ?*anyopaque {
    if (process_alloc_index >= process_alloc_storage.len) return null;
    const ptr: *anyopaque = &process_alloc_storage[process_alloc_index];
    process_alloc_index += 1;
    return ptr;
}

fn processTestFree(_: ?*anyopaque) callconv(win.win_cc) void {}

fn processTestUtf8ToWide(_: u32, _: u32, input: [*]const u8, input_len: i32, out: ?[*]u16, _: i32) callconv(win.win_cc) i32 {
    if (input_len <= 0) return 0;
    const len: usize = @intCast(input_len);
    if (out) |wide| for (input[0..len], 0..) |byte, i| {
        wide[i] = byte;
    };
    return input_len;
}

fn copyWideForTest(dst: []u16, src: ?[*:0]const u16) void {
    @memset(dst, 0);
    if (src) |wide| {
        var i: usize = 0;
        while (i < dst.len and wide[i] != 0) : (i += 1) dst[i] = wide[i];
    }
}

fn expectWideAscii(expected: []const u8, actual: []const u16) !void {
    var len: usize = 0;
    while (len < actual.len and actual[len] != 0) : (len += 1) {}
    try std.testing.expectEqual(expected.len, len);
    for (expected, 0..) |byte, i| try std.testing.expectEqual(@as(u16, byte), actual[i]);
}

fn processTestCreateProcess(
    app: ?[*:0]const u16,
    command: ?[*:0]u16,
    _: ?*anyopaque,
    _: ?*anyopaque,
    _: i32,
    flags: u32,
    _: ?*anyopaque,
    cwd: ?[*:0]const u16,
    startup: *win.StartupInfoW,
    info: *win.ProcessInformation,
) callconv(win.win_cc) i32 {
    spawn_calls += 1;
    spawn_last_has_application = app != null;
    spawn_last_has_cwd = cwd != null;
    copyWideForTest(&spawn_last_application, app);
    copyWideForTest(&spawn_last_command, if (command) |cmd| @ptrCast(cmd) else null);
    copyWideForTest(&spawn_last_cwd, cwd);
    spawn_last_flags = flags;
    spawn_last_startup_flags = startup.flags;
    spawn_last_stdin = startup.std_input;
    spawn_last_stdout = startup.std_output;
    spawn_last_stderr = startup.std_error;
    if (spawn_calls <= spawn_failures) return 0;
    info.* = .{
        .process = @ptrFromInt(0x40),
        .thread = @ptrFromInt(0x41),
        .process_id = 123,
        .thread_id = 124,
    };
    return 1;
}

fn processTestClose(_: win.HANDLE) callconv(win.win_cc) i32 {
    spawn_close_calls += 1;
    return 1;
}

fn processTestLookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    const symbol = std.mem.sliceTo(name, 0);
    if (std.mem.eql(u8, symbol, "CreateProcessW")) return @ptrCast(@constCast(&processTestCreateProcess));
    if (std.mem.eql(u8, symbol, "WaitForSingleObject")) return @ptrCast(@constCast(&waitTestWait));
    if (std.mem.eql(u8, symbol, "GetExitCodeProcess")) return @ptrCast(@constCast(&waitTestGetExit));
    if (std.mem.eql(u8, symbol, "TerminateProcess")) return @ptrCast(@constCast(&signalTestTerminate));
    if (std.mem.eql(u8, symbol, "GetConsoleMode")) return @ptrCast(@constCast(&ttyTestGetMode));
    if (std.mem.eql(u8, symbol, "SetConsoleMode")) return @ptrCast(@constCast(&ttyTestSetMode));
    if (std.mem.eql(u8, symbol, "GetConsoleScreenBufferInfo")) return @ptrCast(@constCast(&ttyTestGetScreenInfo));
    return null;
}

fn resetSpawnTest(state: *PalState) void {
    process_alloc_index = 0;
    spawn_calls = 0;
    spawn_failures = 0;
    spawn_error = 2;
    spawn_last_application = @splat(0);
    spawn_last_command = @splat(0);
    spawn_last_cwd = @splat(0);
    spawn_last_has_application = false;
    spawn_last_has_cwd = false;
    spawn_last_flags = 0;
    spawn_last_startup_flags = 0;
    spawn_last_stdin = null;
    spawn_last_stdout = null;
    spawn_last_stderr = null;
    spawn_close_calls = 0;
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = processTestLookup;
    state.host.alloc = processTestAlloc;
    state.host.free = processTestFree;
    state.p_mb_to_wide = processTestUtf8ToWide;
    state.p_get_last_error = struct {
        fn lastError() callconv(win.win_cc) u32 {
            return spawn_error;
        }
    }.lastError;
    state.p_close_handle = processTestClose;
}

test "Windows process spawn distinguishes an unavailable CreateProcess API" {
    var state: PalState = .{};
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    var ov: pal_abi.Overlapped = .{};
    processSpawn(&state, &ov, undefined, 0);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
}

test "Windows process spawn rejects an empty configuration" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    const config = [_]u8{};
    var ov: pal_abi.Overlapped = .{};
    processSpawn(&state, &ov, &config, config.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), spawn_calls);
}

test "Windows process spawn rejects an empty program name" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    const config = [_]u8{ 0, 0 };
    var ov: pal_abi.Overlapped = .{};
    processSpawn(&state, &ov, &config, config.len);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), spawn_calls);
}

test "Windows process spawn quotes whitespace arguments in the command line" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    const config = [_]u8{ 't', 'o', 'o', 'l', 0, 't', 'w', 'o', ' ', 'w', 'o', 'r', 'd', 's', 0, 'l', 'a', 's', 't', 0, 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expectEqual(@as(u32, 0), ov.header.@"error");
    try expectWideAscii("tool \"two words\" last", &spawn_last_command);
    try std.testing.expect(spawn_last_has_application);
    try std.testing.expectEqual(@as(u64, 3), ov.child_handle);
}

test "Windows process spawn resolves cmd.exe to its system path" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    const config = [_]u8{ 'c', 'm', 'd', '.', 'e', 'x', 'e', 0, 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try expectWideAscii("C:\\Windows\\System32\\cmd.exe", &spawn_last_application);
    try expectWideAscii("C:\\Windows\\System32\\cmd.exe", &spawn_last_command);
}

test "Windows process spawn passes a converted optional working directory" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    const config = [_]u8{ 't', 0, 0, 0, 'C', ':', '\\', 'w', 'o', 'r', 'k', 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expect(spawn_last_has_cwd);
    try expectWideAscii("C:\\work", &spawn_last_cwd);
}

test "Windows process spawn leaves the working directory null when omitted" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    const config = [_]u8{ 't', 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expect(!spawn_last_has_cwd);
}

test "Windows process spawn retries without an explicit application name" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    spawn_failures = 1;
    const config = [_]u8{ 't', 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expectEqual(@as(usize, 2), spawn_calls);
    try std.testing.expect(!spawn_last_has_application);
    try std.testing.expectEqual(@as(u32, 0), ov.header.@"error");
}

test "Windows process spawn tries the cmd fallback after both direct forms fail" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    spawn_failures = 2;
    const config = [_]u8{ 't', 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expectEqual(@as(usize, 3), spawn_calls);
    try expectWideAscii("cmd.exe", &spawn_last_application);
    try std.testing.expectEqual(@as(u32, 0), ov.header.@"error");
}

test "Windows process spawn maps native creation failures" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    spawn_failures = 3;
    spawn_error = 5;
    const config = [_]u8{ 't', 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.header.@"error");
    try std.testing.expectEqual(@as(u32, 5), ov.header.continued);
}

test "Windows process spawn closes thread and process handles when allocation is exhausted" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    for (&state.handles) |*handle| handle.used = true;
    const config = [_]u8{ 't', 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expectEqual(pal_abi.ERROR_NOMEM, ov.header.@"error");
    try std.testing.expectEqual(@as(usize, 2), spawn_close_calls);
}

test "Windows process spawn sets no-window and standard-handle flags" {
    var state: PalState = .{};
    resetSpawnTest(&state);
    state.stdin_h = @ptrFromInt(0x51);
    state.stdout_h = @ptrFromInt(0x52);
    state.stderr_h = @ptrFromInt(0x53);
    const config = [_]u8{ 't', 0, 0, 0 };
    var ov: pal_abi.ProcessSpawnOverlapped = .{};
    processSpawn(&state, @ptrCast(&ov), &config, config.len);
    try std.testing.expectEqual(@as(u32, 0x0800_0000), spawn_last_flags);
    try std.testing.expectEqual(@as(u32, 0x100), spawn_last_startup_flags);
    try std.testing.expectEqual(@as(usize, 1), spawn_close_calls);
}

var wait_result: u32 = 0;
var wait_results: [4]u32 = @splat(0);
var wait_result_count: usize = 0;
var wait_calls: usize = 0;
var wait_exit_code: u32 = 0;
var wait_exit_result: i32 = 1;
var wait_exit_calls: usize = 0;
var wait_thread_result: win.HANDLE = @ptrFromInt(0x61);
var wait_thread_calls: usize = 0;
var wait_worker: ?*const fn (?*anyopaque) callconv(win.win_cc) u32 = null;
var wait_worker_ctx: ?*anyopaque = null;
var wait_last_error: u32 = 5;
var wait_post_calls: usize = 0;
var wait_post_error: u32 = 0;

fn waitTestWait(_: win.HANDLE, _: u32) callconv(win.win_cc) u32 {
    const index = wait_calls;
    wait_calls += 1;
    if (index < wait_result_count) return wait_results[index];
    return wait_result;
}

fn waitTestGetExit(_: win.HANDLE, code: *u32) callconv(win.win_cc) i32 {
    wait_exit_calls += 1;
    code.* = wait_exit_code;
    return wait_exit_result;
}

fn waitTestGetLastError() callconv(win.win_cc) u32 {
    return wait_last_error;
}

fn waitTestPost(_: win.HANDLE, _: u32, _: usize, _: ?*win.WinOverlapped) callconv(win.win_cc) i32 {
    wait_post_calls += 1;
    return if (wait_post_error == 0) 1 else 0;
}

fn waitTestCreateThread(
    _: ?*anyopaque,
    _: usize,
    worker: *const fn (?*anyopaque) callconv(win.win_cc) u32,
    ctx: ?*anyopaque,
    _: u32,
    _: ?*u32,
) callconv(win.win_cc) win.HANDLE {
    wait_thread_calls += 1;
    wait_worker = worker;
    wait_worker_ctx = ctx;
    return wait_thread_result;
}

fn resetWaitTest(state: *PalState) void {
    wait_result = 0;
    wait_results = @splat(0);
    wait_result_count = 0;
    wait_calls = 0;
    wait_exit_code = 0;
    wait_exit_result = 1;
    wait_exit_calls = 0;
    wait_thread_result = @ptrFromInt(0x61);
    wait_thread_calls = 0;
    wait_worker = null;
    wait_worker_ctx = null;
    wait_last_error = 5;
    wait_post_calls = 0;
    wait_post_error = 0;
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = processTestLookup;
    state.p_create_thread = waitTestCreateThread;
    state.p_post_queued = waitTestPost;
    state.p_get_last_error = waitTestGetLastError;
    state.p_wait_single = waitTestWait;
}

fn newProcessHandle(state: *PalState) *mem.HandleEntry {
    const entry = mem.allocHandle(state).?;
    entry.handle = @ptrFromInt(0x70);
    entry.is_process = true;
    return entry;
}

test "Windows process wait completes synchronously with the child exit code" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    wait_exit_code = 37;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    try std.testing.expectEqual(@as(u32, 0), ov.@"error");
    try std.testing.expectEqual(@as(u64, 37), ov.result_ext);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov.flags);
    try std.testing.expectEqual(@as(usize, 0), wait_thread_calls);
}

test "Windows process wait falls back to a worker while the child is running" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    wait_result = 258;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    try std.testing.expectEqual(@as(usize, 1), wait_thread_calls);
    try std.testing.expectEqual(@as(usize, 1), state.pending_count);
    try std.testing.expectEqual(@as(u32, 0), ov.flags);
}

test "Windows process wait reports unsupported worker creation" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    state.p_create_thread = null;
    wait_result = 258;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
}

test "Windows process wait maps worker creation failure" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    wait_result = 258;
    wait_thread_result = null;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, ov.@"error");
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
}

test "Windows process wait falls back when the native wait API is unavailable" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            if (std.mem.eql(u8, std.mem.sliceTo(name, 0), "GetExitCodeProcess")) {
                return @ptrCast(@constCast(&waitTestGetExit));
            }
            return null;
        }
    }.lookup;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    try std.testing.expectEqual(@as(usize, 1), wait_thread_calls);
}

test "Windows process wait falls back when exit-code retrieval fails" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    wait_exit_result = 0;
    wait_result = 0;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    try std.testing.expectEqual(@as(usize, 1), wait_thread_calls);
    try std.testing.expectEqual(@as(usize, 1), wait_exit_calls);
}

test "Windows process wait worker retries timeout before collecting exit status" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    wait_result = 258;
    wait_results = .{ 258, 258, 0, 0 };
    wait_result_count = 3;
    wait_exit_code = 99;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    try std.testing.expect(wait_worker != null);
    _ = wait_worker.?(wait_worker_ctx);
    const op = &state.ops[0];
    try std.testing.expectEqual(@as(u32, 0), op.win_error);
    try std.testing.expectEqual(@as(usize, 99), op.win_ov.internal_high);
    try std.testing.expectEqual(@as(usize, 1), wait_post_calls);
    try std.testing.expectEqual(@as(usize, 3), wait_calls);
}

test "Windows process wait worker propagates cancellation without waiting" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    wait_result = 258;
    var ov: pal_abi.Overlapped = .{};
    processWait(&state, &ov, process.token);
    state.ops[0].canceled = true;
    _ = wait_worker.?(wait_worker_ctx);
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, state.ops[0].win_error);
    try std.testing.expectEqual(@as(usize, 1), wait_calls);
    try std.testing.expectEqual(@as(usize, 1), wait_post_calls);
}

test "Windows process wait worker reports vanished process handles" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const op = mem.allocOp(&state).?;
    op.kind = .proc_wait;
    op.pal_state = &state;
    op.handle_token = 900;
    _ = processWaitWorker(op);
    try std.testing.expectEqual(pal_abi.ERROR_BADF, op.win_error);
    try std.testing.expectEqual(@as(usize, 1), wait_post_calls);
}

test "Windows process wait worker posts unsupported when wait support disappears" {
    var state: PalState = .{};
    resetWaitTest(&state);
    state.p_wait_single = null;
    const process = newProcessHandle(&state);
    const op = mem.allocOp(&state).?;
    op.kind = .proc_wait;
    op.pal_state = &state;
    op.handle_token = process.token;
    _ = processWaitWorker(op);
    try std.testing.expectEqual(pal_abi.ERROR_UNSUPPORTED, op.win_error);
    try std.testing.expectEqual(@as(usize, 1), wait_post_calls);
}

test "Windows process wait worker maps exit-code query failure" {
    var state: PalState = .{};
    resetWaitTest(&state);
    const process = newProcessHandle(&state);
    const op = mem.allocOp(&state).?;
    op.kind = .proc_wait;
    op.pal_state = &state;
    op.handle_token = process.token;
    wait_exit_result = 0;
    wait_last_error = 5;
    _ = processWaitWorker(op);
    try std.testing.expectEqual(pal_abi.ERROR_ACCES, op.win_error);
    try std.testing.expectEqual(@as(usize, 1), wait_post_calls);
}

var signal_calls: usize = 0;
var signal_last_handle: win.HANDLE = null;
var signal_last_code: u32 = 0;
var signal_result: i32 = 1;

fn signalTestTerminate(handle: win.HANDLE, code: u32) callconv(win.win_cc) i32 {
    signal_calls += 1;
    signal_last_handle = handle;
    signal_last_code = code;
    return signal_result;
}

fn resetSignalTest(state: *PalState) void {
    signal_calls = 0;
    signal_last_handle = null;
    signal_last_code = 0;
    signal_result = 1;
    signal_api_available = true;
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = signalTestLookup;
}

test "Windows process signal ignores token zero" {
    var state: PalState = .{};
    resetSignalTest(&state);
    processSignal(&state, 0, 9);
    try std.testing.expectEqual(@as(usize, 0), signal_calls);
}

test "Windows process signal ignores reserved standard-handle tokens" {
    var state: PalState = .{};
    resetSignalTest(&state);
    processSignal(&state, 1, 9);
    processSignal(&state, 2, 9);
    try std.testing.expectEqual(@as(usize, 0), signal_calls);
}

test "Windows process signal ignores stale tokens" {
    var state: PalState = .{};
    resetSignalTest(&state);
    processSignal(&state, 99, 9);
    try std.testing.expectEqual(@as(usize, 0), signal_calls);
}

test "Windows process signal refuses non-process handles" {
    var state: PalState = .{};
    resetSignalTest(&state);
    const handle = mem.allocHandle(&state).?;
    handle.handle = @ptrFromInt(0x81);
    processSignal(&state, handle.token, 9);
    try std.testing.expectEqual(@as(usize, 0), signal_calls);
}

test "Windows process signal refuses pipe handles even when marked as pipes" {
    var state: PalState = .{};
    resetSignalTest(&state);
    const handle = mem.allocHandle(&state).?;
    handle.handle = @ptrFromInt(0x82);
    handle.is_pipe = true;
    processSignal(&state, handle.token, 15);
    try std.testing.expectEqual(@as(usize, 0), signal_calls);
}

test "Windows process signal does nothing when termination is unavailable" {
    var state: PalState = .{};
    resetSignalTest(&state);
    const handle = newProcessHandle(&state);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    processSignal(&state, handle.token, 15);
    try std.testing.expectEqual(@as(usize, 0), signal_calls);
}

test "Windows process signal terminates the registered process handle" {
    var state: PalState = .{};
    resetSignalTest(&state);
    const handle = newProcessHandle(&state);
    processSignal(&state, handle.token, 15);
    try std.testing.expectEqual(@as(usize, 1), signal_calls);
    try std.testing.expectEqual(handle.handle, signal_last_handle);
}

test "Windows process signal maps every request to the supported termination code" {
    var state: PalState = .{};
    resetSignalTest(&state);
    const handle = newProcessHandle(&state);
    processSignal(&state, handle.token, 2);
    try std.testing.expectEqual(@as(u32, 1), signal_last_code);
}

test "Windows process signal preserves the handle entry after termination" {
    var state: PalState = .{};
    resetSignalTest(&state);
    const handle = newProcessHandle(&state);
    processSignal(&state, handle.token, 9);
    try std.testing.expect(mem.findHandle(&state, handle.token) != null);
    try std.testing.expect(mem.findHandle(&state, handle.token).?.is_process);
}

test "Windows process signal ignores the native termination result" {
    var state: PalState = .{};
    resetSignalTest(&state);
    const handle = newProcessHandle(&state);
    signal_result = 0;
    processSignal(&state, handle.token, 9);
    try std.testing.expectEqual(@as(usize, 1), signal_calls);
    try std.testing.expectEqual(handle.handle, signal_last_handle);
}

var signal_api_available: bool = true;

var tty_native_mode: u32 = 0;
var tty_getmode_result: i32 = 1;
var tty_setmode_result: i32 = 1;
var tty_getmode_calls: usize = 0;
var tty_setmode_calls: usize = 0;
var tty_last_mode_handle: win.HANDLE = null;
var tty_last_set_handle: win.HANDLE = null;
var tty_last_set_mode: u32 = 0;
var tty_screen_result: i32 = 1;
var tty_screen_info: win.ConsoleScreenBufferInfo = .{};
var tty_screen_calls: usize = 0;
var tty_screen_handle: win.HANDLE = null;

fn ttyTestGetMode(handle: win.HANDLE, mode: *u32) callconv(win.win_cc) i32 {
    tty_getmode_calls += 1;
    tty_last_mode_handle = handle;
    mode.* = tty_native_mode;
    return tty_getmode_result;
}

fn ttyTestSetMode(handle: win.HANDLE, mode: u32) callconv(win.win_cc) i32 {
    tty_setmode_calls += 1;
    tty_last_set_handle = handle;
    tty_last_set_mode = mode;
    return tty_setmode_result;
}

fn ttyTestGetScreenInfo(handle: win.HANDLE, info: *win.ConsoleScreenBufferInfo) callconv(win.win_cc) i32 {
    tty_screen_calls += 1;
    tty_screen_handle = handle;
    info.* = tty_screen_info;
    return tty_screen_result;
}

fn signalTestLookup(_: ?*anyopaque, name: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
    const symbol = std.mem.sliceTo(name, 0);
    if (std.mem.eql(u8, symbol, "TerminateProcess") and signal_api_available) {
        return @ptrCast(@constCast(&signalTestTerminate));
    }
    return null;
}

fn resetTtyTest(state: *PalState) void {
    tty_native_mode = 0;
    tty_getmode_result = 1;
    tty_setmode_result = 1;
    tty_getmode_calls = 0;
    tty_setmode_calls = 0;
    tty_last_mode_handle = null;
    tty_last_set_handle = null;
    tty_last_set_mode = 0;
    tty_screen_result = 1;
    tty_screen_info = .{};
    tty_screen_calls = 0;
    tty_screen_handle = null;
    state.kernel32 = @ptrFromInt(1);
    state.host.get_proc_address = processTestLookup;
    state.stdin_h = @ptrFromInt(0x91);
    state.stdout_h = @ptrFromInt(0x92);
    state.stderr_h = @ptrFromInt(0x93);
}

fn processInfo(
    left: i16,
    top: i16,
    right: i16,
    bottom: i16,
) win.ConsoleScreenBufferInfo {
    return .{
        .window = .{ .left = left, .top = top, .right = right, .bottom = bottom },
    };
}

test "Windows TTY mode ignores non-standard descriptors" {
    var state: PalState = .{};
    resetTtyTest(&state);
    ttySetMode(&state, 3, 1);
    try std.testing.expectEqual(@as(usize, 0), tty_getmode_calls);
    try std.testing.expectEqual(@as(usize, 0), tty_setmode_calls);
}

test "Windows TTY mode is a no-op without kernel32" {
    var state: PalState = .{};
    resetTtyTest(&state);
    state.kernel32 = null;
    ttySetMode(&state, 0, 1);
    try std.testing.expectEqual(@as(usize, 0), tty_getmode_calls);
}

test "Windows TTY mode is a no-op when console mode APIs are absent" {
    var state: PalState = .{};
    resetTtyTest(&state);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    ttySetMode(&state, 0, 1);
    try std.testing.expectEqual(@as(usize, 0), tty_getmode_calls);
    try std.testing.expectEqual(@as(usize, 0), tty_setmode_calls);
}

test "Windows TTY raw mode clears cooked input flags and enables VT input" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_native_mode = 0xffff_ffff;
    ttySetMode(&state, 0, 1);
    try std.testing.expectEqual(@as(u32, (0xffff_ffff & ~@as(u32, 7)) | 0x200), tty_last_set_mode);
    try std.testing.expectEqual(state.stdin_h, tty_last_set_handle);
}

test "Windows TTY reset restores the first saved input mode" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_native_mode = 0x17;
    ttySetMode(&state, 0, 1);
    tty_native_mode = 0x200;
    ttySetMode(&state, 0, 1);
    ttySetMode(&state, 0, 0);
    try std.testing.expectEqual(@as(u32, 0x17), tty_last_set_mode);
    try std.testing.expectEqual(@as(usize, 3), tty_setmode_calls);
    try std.testing.expectEqual(@as(u32, 0x17), state.saved_in_mode);
}

test "Windows TTY reset without raw mode restores the mode just queried" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_native_mode = 0x18;
    ttySetMode(&state, 0, 0);
    try std.testing.expectEqual(@as(usize, 1), tty_getmode_calls);
    try std.testing.expectEqual(@as(usize, 1), tty_setmode_calls);
    try std.testing.expect(state.has_saved_in_mode);
    try std.testing.expectEqual(@as(u32, 0x18), tty_last_set_mode);
}

test "Windows TTY mode does not change standard output" {
    var state: PalState = .{};
    resetTtyTest(&state);
    ttySetMode(&state, 1, 1);
    try std.testing.expectEqual(state.stdout_h, tty_last_mode_handle);
    try std.testing.expectEqual(@as(usize, 0), tty_setmode_calls);
}

test "Windows TTY mode does not change standard error" {
    var state: PalState = .{};
    resetTtyTest(&state);
    ttySetMode(&state, 2, 1);
    try std.testing.expectEqual(state.stderr_h, tty_last_mode_handle);
    try std.testing.expectEqual(@as(usize, 0), tty_setmode_calls);
}

test "Windows TTY mode does not set flags when querying console mode fails" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_getmode_result = 0;
    ttySetMode(&state, 0, 1);
    try std.testing.expectEqual(@as(usize, 0), tty_setmode_calls);
    try std.testing.expect(!state.has_saved_in_mode);
}

test "Windows TTY mode records the input mode even if setting raw mode fails" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_native_mode = 0x21;
    tty_setmode_result = 0;
    ttySetMode(&state, 0, 1);
    try std.testing.expect(state.has_saved_in_mode);
    try std.testing.expectEqual(@as(u32, 0x21), state.saved_in_mode);
    try std.testing.expectEqual(@as(usize, 1), tty_setmode_calls);
}

test "Windows TTY size uses the safe default for an unsupported descriptor" {
    var state: PalState = .{};
    resetTtyTest(&state);
    try std.testing.expectEqual((@as(u32, 80) << 16) | 24, ttyGetSize(&state, 3));
    try std.testing.expectEqual(@as(usize, 0), tty_screen_calls);
}

test "Windows TTY size uses the safe default without kernel32" {
    var state: PalState = .{};
    resetTtyTest(&state);
    state.kernel32 = null;
    try std.testing.expectEqual((@as(u32, 80) << 16) | 24, ttyGetSize(&state, 1));
}

test "Windows TTY size uses the safe default when screen-info API is absent" {
    var state: PalState = .{};
    resetTtyTest(&state);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    try std.testing.expectEqual((@as(u32, 80) << 16) | 24, ttyGetSize(&state, 1));
}

test "Windows TTY size uses the safe default when querying screen info fails" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_screen_result = 0;
    try std.testing.expectEqual((@as(u32, 80) << 16) | 24, ttyGetSize(&state, 1));
}

test "Windows TTY size packs inclusive visible columns and rows" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_screen_info = processInfo(2, 3, 91, 28);
    try std.testing.expectEqual((@as(u32, 90) << 16) | 26, ttyGetSize(&state, 1));
}

test "Windows TTY size clamps empty windows to one row and column" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_screen_info = processInfo(5, 5, 4, 4);
    try std.testing.expectEqual((@as(u32, 1) << 16) | 1, ttyGetSize(&state, 1));
}

test "Windows TTY size selects standard output handle" {
    var state: PalState = .{};
    resetTtyTest(&state);
    _ = ttyGetSize(&state, 1);
    try std.testing.expectEqual(state.stdout_h, tty_screen_handle);
}

test "Windows TTY size selects standard error handle" {
    var state: PalState = .{};
    resetTtyTest(&state);
    _ = ttyGetSize(&state, 2);
    try std.testing.expectEqual(state.stderr_h, tty_screen_handle);
}

test "Windows TTY size ignores backing buffer size in favor of visible window bounds" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_screen_info.size = .{ .x = 240, .y = 120 };
    tty_screen_info.window = .{ .left = 0, .top = 0, .right = 79, .bottom = 23 };
    try std.testing.expectEqual((@as(u32, 80) << 16) | 24, ttyGetSize(&state, 1));
}

test "Windows TTY isatty returns false without kernel32" {
    var state: PalState = .{};
    resetTtyTest(&state);
    state.kernel32 = null;
    try std.testing.expectEqual(@as(u32, 0), fdIsatty(&state, 0));
}

test "Windows TTY isatty returns false when console mode API is absent" {
    var state: PalState = .{};
    resetTtyTest(&state);
    state.host.get_proc_address = struct {
        fn lookup(_: ?*anyopaque, _: [*:0]const u8) callconv(win.win_cc) ?*anyopaque {
            return null;
        }
    }.lookup;
    try std.testing.expectEqual(@as(u32, 0), fdIsatty(&state, 1));
}

test "Windows TTY isatty rejects non-standard descriptors" {
    var state: PalState = .{};
    resetTtyTest(&state);
    try std.testing.expectEqual(@as(u32, 0), fdIsatty(&state, 3));
    try std.testing.expectEqual(@as(usize, 0), tty_getmode_calls);
}

test "Windows TTY isatty recognizes standard input" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_native_mode = 0;
    try std.testing.expectEqual(@as(u32, 1), fdIsatty(&state, 0));
    try std.testing.expectEqual(state.stdin_h, tty_last_mode_handle);
}

test "Windows TTY isatty recognizes standard output" {
    var state: PalState = .{};
    resetTtyTest(&state);
    try std.testing.expectEqual(@as(u32, 1), fdIsatty(&state, 1));
    try std.testing.expectEqual(state.stdout_h, tty_last_mode_handle);
}

test "Windows TTY isatty recognizes standard error" {
    var state: PalState = .{};
    resetTtyTest(&state);
    try std.testing.expectEqual(@as(u32, 1), fdIsatty(&state, 2));
    try std.testing.expectEqual(state.stderr_h, tty_last_mode_handle);
}

test "Windows TTY isatty returns false when GetConsoleMode fails" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_getmode_result = 0;
    try std.testing.expectEqual(@as(u32, 0), fdIsatty(&state, 0));
}

test "Windows TTY isatty depends on successful handle probing rather than mode bits" {
    var state: PalState = .{};
    resetTtyTest(&state);
    tty_native_mode = 0;
    tty_getmode_result = 1;
    try std.testing.expectEqual(@as(u32, 1), fdIsatty(&state, 2));
}

test "Windows TTY isatty passes null standard handles through to the native probe" {
    var state: PalState = .{};
    resetTtyTest(&state);
    state.stdout_h = null;
    try std.testing.expectEqual(@as(u32, 1), fdIsatty(&state, 1));
    try std.testing.expectEqual(@as(win.HANDLE, null), tty_last_mode_handle);
}

test "Windows TTY isatty probes each selected standard descriptor once" {
    var state: PalState = .{};
    resetTtyTest(&state);
    _ = fdIsatty(&state, 0);
    _ = fdIsatty(&state, 1);
    _ = fdIsatty(&state, 2);
    try std.testing.expectEqual(@as(usize, 3), tty_getmode_calls);
}
