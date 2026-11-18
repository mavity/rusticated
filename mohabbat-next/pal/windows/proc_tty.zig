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
    mem.completeInline(ov, 0, r_he.token, w_he.token);
}

pub fn processSpawn(_: *PalState, ov: *pal_abi.Overlapped, _: [*]const u8, _: usize) void {
    mem.completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
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
