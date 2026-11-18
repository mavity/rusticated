const std = @import("std");
const pal_abi = @import("pal_abi");
const win = @import("win_ffi.zig");
const mem = @import("mem.zig");
const PalState = mem.PalState;

pub fn processPipe(s: *PalState, ov: *pal_abi.Overlapped) void {
    const p_fn = mem.resolveKernel32(s, "CreatePipe") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const PipeFn = *const fn (*win.HANDLE, *win.HANDLE, ?*win.SecurityAttributes, u32) callconv(win.win_cc) i32;
    const create_pipe: PipeFn = @ptrCast(@alignCast(p_fn));
    var r_h: win.HANDLE = null;
    var w_h: win.HANDLE = null;
    var sa: win.SecurityAttributes = .{ .inherit_handle = 0 };
    if (create_pipe(&r_h, &w_h, &sa, 0) == 0) {
        mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
        return;
    }
    const r_he = mem.allocHandle(s) orelse {
        if (s.p_close_handle) |ch| {
            _ = ch(r_h);
            _ = ch(w_h);
        }
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    const w_he = mem.allocHandle(s) orelse {
        r_he.used = false;
        if (s.p_close_handle) |ch| {
            _ = ch(r_h);
            _ = ch(w_h);
        }
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    r_he.handle = r_h;
    w_he.handle = w_h;
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
    const p_pipe = mem.resolveKernel32(s, "CreatePipe") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_shi = mem.resolveKernel32(s, "SetHandleInformation") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const CreateProcFn = *const fn (?[*:0]const u16, ?[*:0]u16, ?*anyopaque, ?*anyopaque, i32, u32, ?*anyopaque, ?[*:0]const u16, *win.StartupInfoW, *win.ProcessInformation) callconv(win.win_cc) i32;
    const PipeFn = *const fn (*win.HANDLE, *win.HANDLE, ?*win.SecurityAttributes, u32) callconv(win.win_cc) i32;
    const ShiFn = *const fn (win.HANDLE, u32, u32) callconv(win.win_cc) i32;
    const create_proc: CreateProcFn = @ptrCast(@alignCast(p_cp));
    const create_pipe: PipeFn = @ptrCast(@alignCast(p_pipe));
    const set_hi: ShiFn = @ptrCast(@alignCast(p_shi));

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
    var in_r: win.HANDLE = null;
    var in_w: win.HANDLE = null;
    var out_r: win.HANDLE = null;
    var out_w: win.HANDLE = null;
    var err_r: win.HANDLE = null;
    var err_w: win.HANDLE = null;

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
        if (create_pipe(&in_r, &in_w, &sa, 0) == 0 or
            create_pipe(&out_r, &out_w, &sa, 0) == 0 or
            create_pipe(&err_r, &err_w, &sa, 0) == 0)
        {
            mem.completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        }
        // Parent-side ends must NOT be inherited (HANDLE_FLAG_INHERIT = 1)
        _ = set_hi(in_w, 1, 0);
        _ = set_hi(out_r, 1, 0);
        _ = set_hi(err_r, 1, 0);
        child_in = in_r;
        child_out = out_w;
        child_err = err_w;
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
        } else {
            _ = ch(in_r);
            _ = ch(out_w);
            _ = ch(err_w);
        }
    }
    if (ok == 0) {
        if (!has_stdio) {
            if (s.p_close_handle) |ch| {
                _ = ch(in_w);
                _ = ch(out_r);
                _ = ch(err_r);
            }
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
        mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    proc_he.handle = pi.process;
    proc_he.is_process = true;

    const pso: *pal_abi.ProcessSpawnOverlapped = @ptrCast(@alignCast(ov));
    pso.child_handle = proc_he.token;
    pso.pty_handle = 0;
    if (!has_stdio) {
        const in_he = mem.allocHandle(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        in_he.handle = in_w;
        in_he.access_mode = 1;

        const out_he = mem.allocHandle(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        out_he.handle = out_r;

        const err_he = mem.allocHandle(s) orelse {
            mem.completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        err_he.handle = err_r;

        pso.in_handle = in_he.token;
        pso.out_handle = out_he.token;
        pso.err_handle = err_he.token;
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
    const p_wait = mem.resolveKernel32(s, "WaitForSingleObject") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const p_code = mem.resolveKernel32(s, "GetExitCodeProcess") orelse {
        mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const WaitFn = *const fn (win.HANDLE, u32) callconv(win.win_cc) u32;
    const ExitCodeFn = *const fn (win.HANDLE, *u32) callconv(win.win_cc) i32;
    const wait_fn: WaitFn = @ptrCast(@alignCast(p_wait));
    const exit_fn: ExitCodeFn = @ptrCast(@alignCast(p_code));
    _ = wait_fn(he.handle, 0xffff_ffff);
    var code: u32 = 0;
    _ = exit_fn(he.handle, &code);
    mem.completeInline(ov, 0, 0, code);
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
