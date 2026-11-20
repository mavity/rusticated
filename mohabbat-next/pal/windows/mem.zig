const std = @import("std");
const pal_abi = @import("pal_abi");
const win = @import("win_ffi.zig");

pub const MAX_HANDLES = 64;
pub const MAX_OPS = 128;

pub const HandleEntry = struct {
    used: bool = false,
    token: u64 = 0,
    handle: win.HANDLE = null,
    is_socket: bool = false,
    is_udp: bool = false,
    is_listener: bool = false,
    is_pty: bool = false,
    pty_read: win.HANDLE = null,
    pty_write: win.HANDLE = null,
    is_process: bool = false,
    is_dir: bool = false,
    is_pipe: bool = false,
    is_overlapped: bool = false,
    append_mode: bool = false,
    access_mode: u8 = 2,
    dir_path_w: ?[*:0]u16 = null,
    dir_find: win.HANDLE = win.INVALID_HANDLE_VALUE,
    dir_done: bool = false,
    dir_first: bool = false,
    dir_find_data: win.Win32FindDataW = .{},
    seq_pos: u64 = 0,
};

pub const OpKind = enum(u8) {
    none = 0,
    read = 1,
    write = 2,
    timer = 3,
    accept = 4,
    proc_wait = 5,
    sock_recv = 6,
    pipe_connect = 7,
    signal_wait = 8,
    system_info_wait = 9,
};

pub const PendingOp = struct {
    used: bool = false,
    kind: OpKind = .none,
    guest_ov: ?*pal_abi.Overlapped = null,
    handle_token: u64 = 0,
    sock_fd: usize = 0,
    pal_state: ?*anyopaque = null,
    buf: ?[*]u8 = null,
    len: usize = 0,
    positional: bool = false,
    offset: i64 = 0,
    deadline_ms: u64 = 0,
    canceled: bool = false,
    native_io: bool = false,
    is_udp: bool = false,
    file_offset: u64 = 0,
    advance_seq: bool = false,
    append_write: bool = false,
    worker_handle: win.HANDLE = null,
    win_error: u32 = 0,
    win_ov: win.WinOverlapped = .{},
};

pub const PalState = struct {
    initialized: bool = false,
    host: win.WindowsPlatformHost = undefined,
    kernel32: ?*anyopaque = null,
    ws2_32: ?*anyopaque = null,
    crypt32: ?*anyopaque = null,
    bcrypt: ?*anyopaque = null,
    user32: ?*anyopaque = null,
    advapi32: ?*anyopaque = null,
    iocp: win.HANDLE = null,
    stdin_h: win.HANDLE = null,
    stdout_h: win.HANDLE = null,
    stderr_h: win.HANDLE = null,
    next_token: u64 = 3,
    handles: [MAX_HANDLES]HandleEntry = @splat(.{}),
    ops: [MAX_OPS]PendingOp = @splat(.{}),
    pending_count: usize = 0,
    next_pipe_id: u64 = 1,
    wsa_started: bool = false,
    saved_in_mode: u32 = 0,
    has_saved_in_mode: bool = false,
    system_info_hash: u64 = 0,
    has_system_info_hash: bool = false,
    system_info_version: u32 = 1,

    // Resolved kernel32 functions
    p_get_std_handle: ?*const fn (u32) callconv(win.win_cc) win.HANDLE = null,
    p_read_file: ?*const fn (win.HANDLE, ?[*]u8, u32, ?*u32, ?*win.WinOverlapped) callconv(win.win_cc) i32 = null,
    p_write_file: ?*const fn (win.HANDLE, ?[*]const u8, u32, ?*u32, ?*win.WinOverlapped) callconv(win.win_cc) i32 = null,
    p_get_current_process: ?*const fn () callconv(win.win_cc) win.HANDLE = null,
    p_get_last_error: ?*const fn () callconv(win.win_cc) u32 = null,
    p_flush_icache: ?*const fn (win.HANDLE, ?*const anyopaque, usize) callconv(win.win_cc) i32 = null,
    p_exit_process: ?*const fn (i32) callconv(win.win_cc) noreturn = null,
    p_virtual_alloc: ?*const fn (?*anyopaque, usize, u32, u32) callconv(win.win_cc) ?[*]u8 = null,
    p_virtual_protect: ?*const fn (?*anyopaque, usize, u32, *u32) callconv(win.win_cc) i32 = null,
    p_virtual_free: ?*const fn (?*anyopaque, usize, u32) callconv(win.win_cc) i32 = null,
    p_close_handle: ?*const fn (win.HANDLE) callconv(win.win_cc) i32 = null,
    p_create_file_w: ?*const fn ([*:0]const u16, u32, u32, ?*anyopaque, u32, u32, win.HANDLE) callconv(win.win_cc) win.HANDLE = null,
    p_create_named_pipe_w: ?*const fn ([*:0]const u16, u32, u32, u32, u32, u32, u32, ?*anyopaque) callconv(win.win_cc) win.HANDLE = null,
    p_connect_named_pipe: ?*const fn (win.HANDLE, *win.WinOverlapped) callconv(win.win_cc) i32 = null,
    p_cancel_io_ex: ?*const fn (win.HANDLE, ?*win.WinOverlapped) callconv(win.win_cc) i32 = null,
    p_get_overlapped_result: ?*const fn (win.HANDLE, *win.WinOverlapped, *u32, i32) callconv(win.win_cc) i32 = null,
    p_cancel_sync_io: ?*const fn (win.HANDLE) callconv(win.win_cc) i32 = null,
    p_wait_single: ?*const fn (win.HANDLE, u32) callconv(win.win_cc) u32 = null,
    p_set_file_pointer_ex: ?*const fn (win.HANDLE, i64, ?*i64, u32) callconv(win.win_cc) i32 = null,
    p_set_end_of_file: ?*const fn (win.HANDLE) callconv(win.win_cc) i32 = null,
    p_get_file_size_ex: ?*const fn (win.HANDLE, *i64) callconv(win.win_cc) i32 = null,
    p_mb_to_wide: ?*const fn (u32, u32, [*]const u8, i32, ?[*]u16, i32) callconv(win.win_cc) i32 = null,
    p_wide_to_mb: ?*const fn (u32, u32, [*]const u16, i32, ?[*]u8, i32, ?[*]const u8, ?*i32) callconv(win.win_cc) i32 = null,
    p_get_tick_count64: ?*const fn () callconv(win.win_cc) u64 = null,
    p_get_system_time_ft: ?*const fn (*win.FileTime) callconv(win.win_cc) void = null,
    p_sleep: ?*const fn (u32) callconv(win.win_cc) void = null,
    p_create_iocp: ?*const fn (win.HANDLE, win.HANDLE, usize, u32) callconv(win.win_cc) win.HANDLE = null,
    p_get_queued_ex: ?*const fn (win.HANDLE, [*]win.OverlappedEntry, u32, *u32, u32, i32) callconv(win.win_cc) i32 = null,
    p_post_queued: ?*const fn (win.HANDLE, u32, usize, ?*win.WinOverlapped) callconv(win.win_cc) i32 = null,
    p_create_thread: ?*const fn (?*anyopaque, usize, *const fn (?*anyopaque) callconv(win.win_cc) u32, ?*anyopaque, u32, ?*u32) callconv(win.win_cc) win.HANDLE = null,
    p_get_file_type: ?*const fn (win.HANDLE) callconv(win.win_cc) u32 = null,
};

pub fn resolveKernel32(s: *PalState, name: [*:0]const u8) ?*anyopaque {
    const k32 = s.kernel32 orelse return null;
    const gp = s.host.get_proc_address orelse return null;
    return gp(k32, name);
}

pub fn resolveDll(s: *PalState, mod_slot: *?*anyopaque, dll: [*:0]const u8, name: [*:0]const u8) ?*anyopaque {
    const ll = s.host.load_library orelse return null;
    const gp = s.host.get_proc_address orelse return null;
    if (mod_slot.* == null) {
        mod_slot.* = ll(dll);
    }
    const m = mod_slot.* orelse return null;
    return gp(m, name);
}

pub fn completeInline(ov: *pal_abi.Overlapped, err: u32, continued: u64, result_ext: u64) void {
    ov.@"error" = err;
    ov.continued = continued;
    ov.result_ext = result_ext;
    @atomicStore(u32, &ov.flags, pal_abi.FLAG_COMPLETED, .release);
}

pub fn utf8ToWideAlloc(s: *PalState, ptr: [*]const u8, len: usize) ?[*:0]u16 {
    const alloc = s.host.alloc orelse return null;
    const free = s.host.free orelse return null;
    const mb = s.p_mb_to_wide orelse return null;
    if (len == 0 or len > 32767) return null;
    const wlen = mb(65001, 0, ptr, @intCast(len), null, 0);
    if (wlen <= 0) return null;
    const buf_raw = alloc(@as(usize, @intCast(wlen + 1)) * 2) orelse return null;
    const wbuf: [*]u16 = @ptrCast(@alignCast(buf_raw));
    const written = mb(65001, 0, ptr, @intCast(len), wbuf, wlen);
    if (written <= 0) {
        free(buf_raw);
        return null;
    }
    wbuf[@intCast(written)] = 0;
    return @ptrCast(wbuf);
}

pub fn wideToUtf8(s: *PalState, wptr: [*]const u16, wlen: i32, out: [*]u8, cap: usize) usize {
    const wb = s.p_wide_to_mb orelse return 0;
    if (cap == 0) return 0;
    const n = wb(65001, 0, wptr, wlen, out, @intCast(@min(cap, 0x7fff_ffff)), null, null);
    if (n <= 0) return 0;
    return @intCast(n);
}

pub fn findHandle(s: *PalState, token: u64) ?*HandleEntry {
    if (token < 3) return null;
    for (&s.handles) |*h| {
        if (h.used and h.token == token) return h;
    }
    return null;
}

pub fn allocHandle(s: *PalState) ?*HandleEntry {
    for (&s.handles) |*h| {
        if (!h.used) {
            h.* = .{ .used = true, .token = s.next_token };
            s.next_token += 1;
            return h;
        }
    }
    return null;
}

pub fn allocOp(s: *PalState) ?*PendingOp {
    for (&s.ops) |*op| {
        if (!op.used) {
            op.* = .{ .used = true };
            s.pending_count += 1;
            return op;
        }
    }
    return null;
}

pub fn freeOp(s: *PalState, op: *PendingOp) void {
    if (op.used) {
        op.used = false;
        op.kind = .none;
        if (s.pending_count > 0) s.pending_count -= 1;
    }
}
