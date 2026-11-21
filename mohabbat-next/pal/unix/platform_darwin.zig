const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");
const sys = @import("darwin_sys.zig");
const pool_mod = @import("darwin_pool.zig");

const UnixPlatformHost = extern struct {
    load_library: ?*const fn (?[*:0]const u8, c_int) callconv(.c) ?*anyopaque,
    get_proc_address: ?*const fn (?*anyopaque, [*:0]const u8) callconv(.c) ?*anyopaque,
    alloc: ?*const fn (usize) callconv(.c) ?*anyopaque,
    free: ?*const fn (?*anyopaque, usize) callconv(.c) void,
    flush_cache: ?*const fn (?*anyopaque, usize) callconv(.c) void,
    set_jit_write_protect: ?*const fn (c_int) callconv(.c) void,
    runtime_slice: [*]const u8,
    runtime_slice_size: usize,
    argv: [*]const [*:0]const u8,
    argc: usize,
    wasm_bytes: ?[*]const u8,
    wasm_len: usize,
};

const MAX_HANDLES = 64;
const MAX_OPS = 80;

const HandleEntry = struct {
    used: bool = false,
    token: u64 = 0,
    fd: i32 = -1,
    dir_ptr: ?*anyopaque = null,
    is_dir: bool = false,
    is_socket: bool = false,
    is_listener: bool = false,
    is_udp: bool = false,
    is_process: bool = false,
    is_pty: bool = false,
    pid: i32 = -1,
    pty_read_fd: i32 = -1,
    pty_write_fd: i32 = -1,
};

const OpKind = enum(u8) {
    none = 0,
    read_fd = 1,
    write_fd = 2,
    timer = 3,
    net_connect = 4,
    net_accept = 5,
    signal_wait = 6,
    system_info_wait = 7,
    worker_op = 8,
};

const WorkOpKind = enum(u8) {
    none = 0,
    path_open = 1,
    dir_read = 2,
    path_stat = 3,
    path_link = 4,
    path_symlink = 5,
    path_readlink = 6,
    path_chmod = 7,
    path_set_times = 8,
    path_remove = 9,
    path_mkdir = 10,
    path_rename = 11,
    handle_truncate = 12,
    file_seek = 13,
    file_read = 14,
    file_write = 15,
    net_lookup = 16,
    net_cert_verify = 17,
    proc_wait = 18,
};

const PendingOp = struct {
    used: bool = false,
    kind: OpKind = .none,
    work_kind: WorkOpKind = .none,
    guest_ov: ?*pal_abi.Overlapped = null,
    handle_token: u64 = 0,
    fd: i32 = -1,
    pid: i32 = -1,
    dir_ptr: ?*anyopaque = null,
    buf: ?[*]u8 = null,
    len: usize = 0,
    buf2: ?[*]const u8 = null,
    len2: usize = 0,
    flags: u32 = 0,
    arg_i64_a: i64 = 0,
    arg_u32_a: u32 = 0,
    arg_i64_b: i64 = 0,
    arg_u32_b: u32 = 0,
    deadline_ms: u64 = 0,
    canceled: bool = false,
    res_err: u32 = 0,
    res_cont: u64 = 0,
    res_ext: u64 = 0,
    res_fd: i32 = -1,
    res_dir: ?*anyopaque = null,
    res_is_dir: bool = false,
};

const PalState = struct {
    host: UnixPlatformHost = undefined,
    libsystem: ?*anyopaque = null,
    kq_fd: i32 = -1,
    wake_rd_fd: i32 = -1,
    wake_wr_fd: i32 = -1,
    pool_ptr: ?*pool_mod.DarwinWorkerPool = null,
    handles_mu: [64]u8 align(8) = @splat(0),
    next_token: u64 = 3,
    handles: [MAX_HANDLES]HandleEntry = @splat(.{}),
    ops: [MAX_OPS]PendingOp = @splat(.{}),
    pending_count: usize = 0,
    system_info_hash: u64 = 0,
    has_system_info_hash: bool = false,
    system_info_version: u32 = 1,

    p_read: ?*const fn (i32, [*]u8, usize) callconv(.c) isize = null,
    p_write: ?*const fn (i32, [*]const u8, usize) callconv(.c) isize = null,
    p_pread: ?*const fn (i32, [*]u8, usize, i64) callconv(.c) isize = null,
    p_pwrite: ?*const fn (i32, [*]const u8, usize, i64) callconv(.c) isize = null,
    p_open: ?*const fn ([*:0]const u8, c_int, u32) callconv(.c) i32 = null,
    p_lseek: ?*const fn (i32, i64, c_int) callconv(.c) i64 = null,
    p_ftruncate: ?*const fn (i32, i64) callconv(.c) c_int = null,
    p_close: ?*const fn (i32) callconv(.c) c_int = null,
    p_mkdir: ?*const fn ([*:0]const u8, u32) callconv(.c) c_int = null,
    p_unlink: ?*const fn ([*:0]const u8) callconv(.c) c_int = null,
    p_rmdir: ?*const fn ([*:0]const u8) callconv(.c) c_int = null,
    p_rename: ?*const fn ([*:0]const u8, [*:0]const u8) callconv(.c) c_int = null,
    p_link: ?*const fn ([*:0]const u8, [*:0]const u8) callconv(.c) c_int = null,
    p_symlink: ?*const fn ([*:0]const u8, [*:0]const u8) callconv(.c) c_int = null,
    p_readlink: ?*const fn ([*:0]const u8, [*]u8, usize) callconv(.c) isize = null,
    p_chmod: ?*const fn ([*:0]const u8, u32) callconv(.c) c_int = null,
    p_utimensat: ?*const fn (c_int, [*:0]const u8, *const [2]sys.Timespec, c_int) callconv(.c) c_int = null,
    p_stat: ?*const fn ([*:0]const u8, *sys.Stat) callconv(.c) c_int = null,
    p_lstat: ?*const fn ([*:0]const u8, *sys.Stat) callconv(.c) c_int = null,
    p_fstat: ?*const fn (i32, *sys.Stat) callconv(.c) c_int = null,
    p_opendir: ?*const fn ([*:0]const u8) callconv(.c) ?*anyopaque = null,
    p_readdir: ?*const fn (?*anyopaque) callconv(.c) ?*sys.Dirent = null,
    p_closedir: ?*const fn (?*anyopaque) callconv(.c) c_int = null,
    p_getcwd: ?*const fn ([*]u8, usize) callconv(.c) ?[*]u8 = null,
    p_chdir: ?*const fn ([*:0]const u8) callconv(.c) c_int = null,
    p_getenv: ?*const fn ([*:0]const u8) callconv(.c) ?[*:0]const u8 = null,
    p_ns_get_environ: ?*const fn () callconv(.c) *?[*]?[*:0]const u8 = null,
    p_pipe: ?*const fn (*[2]i32) callconv(.c) c_int = null,
    p_fcntl: ?*const fn (i32, c_int, ...) callconv(.c) c_int = null,
    p_kqueue: ?*const fn () callconv(.c) i32 = null,
    p_kevent: ?*const fn (i32, ?[*]const sys.Kevent, c_int, ?[*]sys.Kevent, c_int, ?*const sys.Timespec) callconv(.c) c_int = null,
    p_socket: ?*const fn (c_int, c_int, c_int) callconv(.c) i32 = null,
    p_connect: ?*const fn (i32, *const anyopaque, u32) callconv(.c) c_int = null,
    p_bind: ?*const fn (i32, *const anyopaque, u32) callconv(.c) c_int = null,
    p_listen: ?*const fn (i32, c_int) callconv(.c) c_int = null,
    p_accept: ?*const fn (i32, ?*anyopaque, ?*u32) callconv(.c) i32 = null,
    p_getsockopt: ?*const fn (i32, c_int, c_int, *anyopaque, *u32) callconv(.c) c_int = null,
    p_setsockopt: ?*const fn (i32, c_int, c_int, *const anyopaque, u32) callconv(.c) c_int = null,
    p_getaddrinfo: ?*const fn ([*:0]const u8, ?[*:0]const u8, ?*const sys.AddrInfo, *?*sys.AddrInfo) callconv(.c) c_int = null,
    p_freeaddrinfo: ?*const fn (?*sys.AddrInfo) callconv(.c) void = null,
    p_fork: ?*const fn () callconv(.c) i32 = null,
    p_execvp: ?*const fn ([*:0]const u8, [*]?[*:0]const u8) callconv(.c) c_int = null,
    p_dup2: ?*const fn (i32, i32) callconv(.c) c_int = null,
    p_setsid: ?*const fn () callconv(.c) i32 = null,
    p_waitpid: ?*const fn (i32, *c_int, c_int) callconv(.c) i32 = null,
    p_kill: ?*const fn (i32, c_int) callconv(.c) c_int = null,
    p_openpty: ?*const fn (*i32, *i32, ?[*:0]u8, ?*anyopaque, ?*anyopaque) callconv(.c) c_int = null,
    p_ioctl: ?*const fn (i32, c_ulong, ...) callconv(.c) c_int = null,
    p_tcgetattr: ?*const fn (i32, *sys.Termios) callconv(.c) c_int = null,
    p_tcsetattr: ?*const fn (i32, c_int, *const sys.Termios) callconv(.c) c_int = null,
    p_isatty: ?*const fn (i32) callconv(.c) c_int = null,
    p_arc4random_buf: ?*const fn ([*]u8, usize) callconv(.c) void = null,
    p_gettimeofday: ?*const fn (*sys.Timeval, ?*anyopaque) callconv(.c) c_int = null,
    p_clock_gettime: ?*const fn (c_int, *sys.Timespec) callconv(.c) c_int = null,
    p_mutex_init: ?*const fn (*[64]u8, ?*anyopaque) callconv(.c) c_int = null,
    p_mutex_lock: ?*const fn (*[64]u8) callconv(.c) c_int = null,
    p_mutex_unlock: ?*const fn (*[64]u8) callconv(.c) c_int = null,
    p_exit: ?*const fn (c_int) callconv(.c) noreturn = null,
    p_errno: ?*const fn () callconv(.c) *c_int = null,
};

inline fn sliceOrigin() [*]u8 {
    const addr = switch (builtin.cpu.arch) {
        .x86_64 => asm ("lea unix_platform_entry(%rip), %[ret]"
            : [ret] "=r" (-> usize),
        ),
        .aarch64 => asm ("adr %[ret], unix_platform_entry"
            : [ret] "=r" (-> usize),
        ),
        else => @compileError("Unsupported arch"),
    };
    return @ptrFromInt(addr);
}

inline fn hostPtr() *const UnixPlatformHost {
    return @ptrCast(@alignCast(sliceOrigin() - pal_abi.PREFIX_ABI_OFFSET));
}

inline fn statePtr() *PalState {
    return @ptrCast(@alignCast(sliceOrigin() - pal_abi.PREFIX_GLOBALS_OFFSET));
}

fn sym(s: *PalState, name: [*:0]const u8) ?*anyopaque {
    const gp = s.host.get_proc_address orelse return null;
    return gp(s.libsystem, name);
}

fn completeInline(ov: *pal_abi.Overlapped, err: u32, continued: u64, result_ext: u64) void {
    ov.@"error" = err;
    ov.continued = continued;
    ov.result_ext = result_ext;
    @atomicStore(u32, &ov.flags, pal_abi.FLAG_COMPLETED, .release);
}

fn rawErrno(s: *PalState) i32 {
    const p_err = s.p_errno orelse return 5;
    return p_err().*;
}

fn mapDarwinErrno(s: *PalState) u32 {
    return sys.mapErrno(rawErrno(s));
}

fn lockHandles(s: *PalState) void {
    if (s.p_mutex_lock) |l| _ = l(&s.handles_mu);
}

fn unlockHandles(s: *PalState) void {
    if (s.p_mutex_unlock) |u| _ = u(&s.handles_mu);
}

fn findHandle(s: *PalState, token: u64) ?*HandleEntry {
    if (token < 3) return null;
    lockHandles(s);
    defer unlockHandles(s);
    for (&s.handles) |*h| {
        if (h.used and h.token == token) return h;
    }
    return null;
}

fn allocHandle(s: *PalState) ?*HandleEntry {
    lockHandles(s);
    defer unlockHandles(s);
    for (&s.handles) |*h| {
        if (!h.used) {
            h.* = .{ .used = true, .token = s.next_token };
            s.next_token += 1;
            return h;
        }
    }
    return null;
}

fn allocOp(s: *PalState) ?*PendingOp {
    for (&s.ops) |*op| {
        if (!op.used) {
            op.* = .{ .used = true };
            s.pending_count += 1;
            return op;
        }
    }
    return null;
}

fn freeOp(s: *PalState, op: *PendingOp) void {
    if (op.used) {
        op.used = false;
        op.kind = .none;
        op.work_kind = .none;
        if (s.pending_count > 0) s.pending_count -= 1;
    }
}

fn toCPath(ptr: [*]const u8, len: usize, buf: *[4096]u8) ?[*:0]const u8 {
    if (len == 0 or len >= buf.len) return null;
    @memcpy(buf[0..len], ptr[0..len]);
    buf[len] = 0;
    return @ptrCast(buf);
}

fn nowMs(s: *PalState) u64 {
    if (s.p_clock_gettime) |cg| {
        var ts: sys.Timespec = .{};
        if (cg(6, &ts) == 0) {
            return @as(u64, @intCast(@max(ts.tv_sec, 0))) * 1000 + @as(u64, @intCast(@divTrunc(@max(ts.tv_nsec, 0), 1_000_000)));
        }
    }
    const gtv = s.p_gettimeofday orelse return 0;
    var tv: sys.Timeval = .{};
    _ = gtv(&tv, null);
    return @as(u64, @intCast(@max(tv.sec, 0))) * 1000 + @as(u64, @intCast(@divTrunc(@max(tv.usec, 0), 1000)));
}

fn setNonBlock(s: *PalState, fd: i32) void {
    const fcntl = s.p_fcntl orelse return;
    const fl = fcntl(fd, sys.F_GETFL);
    if (fl >= 0) _ = fcntl(fd, sys.F_SETFL, fl | sys.O_NONBLOCK);
}

fn kqUpdate(s: *PalState, ident: usize, filter: i16, flags: u16, udata: ?*anyopaque) void {
    if (s.kq_fd < 0) return;
    const kev = s.p_kevent orelse return;
    var ev: sys.Kevent = .{
        .ident = ident,
        .filter = filter,
        .flags = flags,
        .udata = udata,
    };
    _ = kev(s.kq_fd, @ptrCast(&ev), 1, null, 0, null);
}

fn isRegularFile(s: *PalState, fd: i32) bool {
    const fs = s.p_fstat orelse return false;
    var st: sys.Stat = .{};
    if (fs(fd, &st) != 0) return false;
    return (st.st_mode & 0o170000) == 0o100000;
}

fn opIndex(s: *PalState, op: *PendingOp) u16 {
    return @intCast((@intFromPtr(op) - @intFromPtr(&s.ops[0])) / @sizeOf(PendingOp));
}

// ─── PalAbiTable Implementations ─────────────────────────────────────────────

fn abiAllocMem(size: usize, _: usize, flags: u32) callconv(pal_abi.pal_cc) ?[*]u8 {
    const s = statePtr();
    if (flags == pal_abi.MEM_CODE_JIT_RW) {
        if (s.host.set_jit_write_protect) |jp| jp(0);
    }
    const alloc = s.host.alloc orelse return null;
    return @ptrCast(alloc(@max(size, 1)));
}

fn abiProtectMem(ptr: [*]u8, size: usize, flags: u32) callconv(pal_abi.pal_cc) i32 {
    const s = statePtr();
    if (s.host.set_jit_write_protect) |jp| {
        jp(if (flags == pal_abi.MEM_CODE_EXEC_RX) 1 else 0);
    }
    if (flags == pal_abi.MEM_CODE_EXEC_RX) {
        if (s.host.flush_cache) |fc| fc(ptr, size);
    }
    return 0;
}

fn abiFreeMem(ptr: [*]u8, size: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (s.host.free) |f| f(ptr, size);
}

fn abiFlushIcache(ptr: [*]const u8, size: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (s.host.flush_cache) |fc| fc(@constCast(ptr), size);
}

fn abiHostPanic(msg: [*]const u8, len: usize) callconv(pal_abi.pal_cc) noreturn {
    const s = statePtr();
    if (s.p_write) |w| _ = w(2, msg, len);
    if (s.p_exit) |e| e(99);
    while (true) {}
}

fn abiRusticatedDebug(_: i32) callconv(pal_abi.pal_cc) void {}

fn abiProcessExit(code: i32) callconv(pal_abi.pal_cc) noreturn {
    const s = statePtr();
    if (s.p_exit) |e| e(code);
    while (true) {}
}

fn abiGetTime() callconv(pal_abi.pal_cc) u64 {
    const s = statePtr();
    const gtv = s.p_gettimeofday orelse return 0;
    var tv: sys.Timeval = .{ .sec = 0, .usec = 0 };
    _ = gtv(&tv, null);
    return @as(u64, @intCast(@max(tv.sec, 0))) * 1_000_000_000 + @as(u64, @intCast(@max(tv.usec, 0))) * 1000;
}

fn abiGetRandom(buf: [*]u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (s.p_arc4random_buf) |rb| rb(buf, len);
}

fn abiGetArgs(buf: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u64 {
    const s = statePtr();
    var needed: usize = 0;
    for (0..s.host.argc) |i| needed += std.mem.sliceTo(s.host.argv[i], 0).len + 1;
    if (needed > cap) return (@as(u64, @intCast(s.host.argc)) << 32) | @as(u64, @intCast(needed));
    var off: usize = 0;
    for (0..s.host.argc) |i| {
        const a = std.mem.sliceTo(s.host.argv[i], 0);
        @memcpy(buf[off..][0..a.len], a);
        buf[off + a.len] = 0;
        off += a.len + 1;
    }
    return (@as(u64, @intCast(s.host.argc)) << 32) | @as(u64, @intCast(off));
}

fn abiGetEnv(buf: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u64 {
    const s = statePtr();
    const nge = s.p_ns_get_environ orelse return 0;
    const envp = nge().* orelse return 0;
    var count: usize = 0;
    var needed: usize = 0;
    while (envp[count]) |entry_z| : (count += 1) {
        needed += std.mem.sliceTo(entry_z, 0).len + 1;
    }
    if (needed > cap) return (@as(u64, @intCast(count)) << 32) | @as(u64, @intCast(needed));
    var off: usize = 0;
    for (0..count) |i| {
        const e = std.mem.sliceTo(envp[i].?, 0);
        @memcpy(buf[off..][0..e.len], e);
        buf[off + e.len] = 0;
        off += e.len + 1;
    }
    return (@as(u64, @intCast(count)) << 32) | @as(u64, @intCast(off));
}

fn abiGetCwd(buf: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u64 {
    const s = statePtr();
    const gc = s.p_getcwd orelse return 0;
    var tmp: [4096]u8 = undefined;
    _ = gc(&tmp, tmp.len) orelse return 0;
    const n = std.mem.sliceTo(@as([*:0]const u8, @ptrCast(&tmp)), 0).len;
    if (n > cap) return @as(u64, @intCast(n));
    @memcpy(buf[0..n], tmp[0..n]);
    return @as(u64, @intCast(n));
}

fn abiSetCwd(path: [*]const u8, len: usize) callconv(pal_abi.pal_cc) u32 {
    const s = statePtr();
    const cd = s.p_chdir orelse return pal_abi.ERROR_UNSUPPORTED;
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse return pal_abi.ERROR_INVALID;
    return if (cd(cpath) != 0) mapDarwinErrno(s) else 0;
}

fn copyFixed(dst: []u8, src: []const u8) void {
    const n = @min(dst.len, src.len);
    @memcpy(dst[0..n], src[0..n]);
}

fn envLookup(s: *PalState, key: [*:0]const u8) ?[]const u8 {
    const ge = s.p_getenv orelse return null;
    const val = ge(key) orelse return null;
    return std.mem.sliceTo(val, 0);
}

fn abiGetPlatformInfo(out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u32 {
    if (cap < @sizeOf(pal_abi.AbiPlatformInfo)) return pal_abi.ERROR_INVALID;
    const s = statePtr();
    var info: pal_abi.AbiPlatformInfo = .{};
    info.path_separator = '/';
    info.path_list_separator = ':';
    info.os_kind = pal_abi.OS_KIND_DARWIN;
    info.os_version = .{ 14, 0, 0, 0 };
    info.cpu_type = switch (builtin.cpu.arch) {
        .x86_64 => pal_abi.CPU_TYPE_X86_64,
        .aarch64 => pal_abi.CPU_TYPE_AARCH64,
        else => 0,
    };
    info.cpu_bitness = 64;
    copyFixed(&info.os_name, "darwin");
    copyFixed(&info.wasm_platform, "mohabbat-next-zig");
    copyFixed(&info.rusticated_name, "rusticated");
    copyFixed(&info.build_platform, if (builtin.cpu.arch == .x86_64) "darwin-amd64" else "darwin-arm64");
    copyFixed(&info.temp_dir, envLookup(s, "TMPDIR") orelse "/tmp");
    if (envLookup(s, "HOME")) |home| {
        copyFixed(&info.user_home_dir, home);
    }
    const bytes = std.mem.asBytes(&info);
    @memcpy(out[0..bytes.len], bytes);
    return 0;
}

fn abiTimerSet(ov: *pal_abi.Overlapped, delay_ms: u32) callconv(pal_abi.pal_cc) void {
    if (delay_ms == 0) {
        completeInline(ov, 0, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .timer;
    op.guest_ov = ov;
    op.deadline_ms = nowMs(s) + delay_ms;
}

fn abiCancel(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    for (&s.ops) |*op| {
        if (op.used and op.guest_ov == ov) {
            @atomicStore(bool, &op.canceled, true, .release);
            if (op.kind == .worker_op) {
                completeInline(ov, pal_abi.ERROR_CANCELED, 0, 0);
                return;
            }
            if (op.kind == .read_fd or op.kind == .net_accept) {
                if (op.fd >= 0) kqUpdate(s, @intCast(op.fd), sys.EVFILT_READ, sys.EV_DELETE, null);
            } else if (op.kind == .write_fd or op.kind == .net_connect) {
                if (op.fd >= 0) kqUpdate(s, @intCast(op.fd), sys.EVFILT_WRITE, sys.EV_DELETE, null);
            } else if (op.kind == .signal_wait) {
                for (1..32) |sig| {
                    if ((op.flags & (@as(u32, 1) << @intCast(sig))) != 0) {
                        kqUpdate(s, sig, sys.EVFILT_SIGNAL, sys.EV_DELETE, null);
                    }
                }
            }
            freeOp(s, op);
            completeInline(ov, pal_abi.ERROR_CANCELED, 0, 0);
            return;
        }
    }
}

fn submitOrRunInline(s: *PalState, op: *PendingOp) void {
    if (s.pool_ptr) |pool| {
        if (pool.submit(opIndex(s, op))) return;
    }
    executeWorkItem(s, opIndex(s, op));
    finishWorkerOp(s, op);
}

fn abiRead(ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (len == 0) {
        completeInline(ov, 0, 0, 0);
        return;
    }
    var fd: i32 = 0;
    if (handle != 0) {
        const he = findHandle(s, handle) orelse {
            completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
            return;
        };
        fd = if (he.is_pty) he.pty_read_fd else he.fd;
    }
    const positional = (ov.flags & pal_abi.READ_FLAG_POSITIONAL) != 0;
    if (positional or isRegularFile(s, fd)) {
        const op = allocOp(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .worker_op;
        op.work_kind = .file_read;
        op.guest_ov = ov;
        op.fd = fd;
        op.buf = buf;
        op.len = len;
        op.flags = ov.flags;
        if (positional) {
            const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
            op.arg_i64_a = trailing.*;
        }
        submitOrRunInline(s, op);
        return;
    }

    setNonBlock(s, fd);
    const r = s.p_read orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const n = r(fd, buf, len);
    if (n >= 0) {
        completeInline(ov, 0, 0, @intCast(n));
        return;
    }
    const err = rawErrno(s);
    if (err != 35) { // EAGAIN
        completeInline(ov, sys.mapErrno(err), 0, 0);
        return;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .read_fd;
    op.guest_ov = ov;
    op.fd = fd;
    op.buf = buf;
    op.len = len;
    kqUpdate(s, @intCast(fd), sys.EVFILT_READ, sys.EV_ADD | sys.EV_ONESHOT, op);
}

fn abiWrite(ov: *pal_abi.Overlapped, handle: u64, buf: [*]const u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (len == 0) {
        completeInline(ov, 0, 0, 0);
        return;
    }
    const fd: i32 = if (handle == 1) 1 else if (handle == 2) 2 else blk: {
        const he = findHandle(s, handle) orelse {
            completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
            return;
        };
        break :blk if (he.is_pty) he.pty_write_fd else he.fd;
    };
    const positional = (ov.flags & pal_abi.WRITE_FLAG_POSITIONAL) != 0;
    if (positional or isRegularFile(s, fd)) {
        if (positional) {
            const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
            if (trailing.* < 0) {
                completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
                return;
            }
        }
        const op = allocOp(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .worker_op;
        op.work_kind = .file_write;
        op.guest_ov = ov;
        op.fd = fd;
        op.buf2 = buf;
        op.len2 = len;
        op.flags = ov.flags;
        if (positional) {
            const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
            op.arg_i64_a = trailing.*;
        }
        submitOrRunInline(s, op);
        return;
    }

    setNonBlock(s, fd);
    const w = s.p_write orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const n = w(fd, buf, len);
    if (n >= 0) {
        completeInline(ov, 0, 0, @intCast(n));
        return;
    }
    const err = rawErrno(s);
    if (err != 35) {
        completeInline(ov, sys.mapErrno(err), 0, 0);
        return;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .write_fd;
    op.guest_ov = ov;
    op.fd = fd;
    op.buf2 = buf;
    op.len2 = len;
    kqUpdate(s, @intCast(fd), sys.EVFILT_WRITE, sys.EV_ADD | sys.EV_ONESHOT, op);
}

fn execFileRead(s: *PalState, op: *PendingOp) void {
    const positional = (op.flags & pal_abi.READ_FLAG_POSITIONAL) != 0;
    const n: isize = if (positional) blk: {
        const pr = s.p_pread orelse {
            op.res_err = pal_abi.ERROR_UNSUPPORTED;
            return;
        };
        break :blk pr(op.fd, op.buf.?, op.len, op.arg_i64_a);
    } else blk: {
        const r = s.p_read orelse {
            op.res_err = pal_abi.ERROR_UNSUPPORTED;
            return;
        };
        break :blk r(op.fd, op.buf.?, op.len);
    };
    if (n < 0) {
        op.res_err = mapDarwinErrno(s);
    } else {
        op.res_ext = @intCast(n);
    }
}

fn execFileWrite(s: *PalState, op: *PendingOp) void {
    const positional = (op.flags & pal_abi.WRITE_FLAG_POSITIONAL) != 0;
    const n: isize = if (positional) blk: {
        const pw = s.p_pwrite orelse {
            op.res_err = pal_abi.ERROR_UNSUPPORTED;
            return;
        };
        break :blk pw(op.fd, op.buf2.?, op.len2, op.arg_i64_a);
    } else blk: {
        const w = s.p_write orelse {
            op.res_err = pal_abi.ERROR_UNSUPPORTED;
            return;
        };
        break :blk w(op.fd, op.buf2.?, op.len2);
    };
    if (n < 0) {
        op.res_err = mapDarwinErrno(s);
    } else {
        op.res_ext = @intCast(n);
    }
}

fn execPathOpen(s: *PalState, op: *PendingOp) void {
    const open_fn = s.p_open orelse {
        op.res_err = pal_abi.ERROR_UNSUPPORTED;
        return;
    };
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(op.buf.?, op.len, &cbuf) orelse {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    };
    var oflags: c_int = switch (op.flags & 3) {
        0 => sys.O_RDONLY,
        1 => sys.O_WRONLY,
        else => sys.O_RDWR,
    };
    if ((op.flags & pal_abi.OPEN_FLAG_CREATE) != 0) oflags |= sys.O_CREAT;
    if ((op.flags & pal_abi.OPEN_FLAG_TRUNC) != 0) oflags |= sys.O_TRUNC;
    if ((op.flags & pal_abi.OPEN_FLAG_EXCL) != 0) oflags |= sys.O_EXCL;
    if ((op.flags & pal_abi.OPEN_FLAG_APPEND) != 0) oflags |= sys.O_APPEND;
    if ((op.flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0) oflags |= sys.O_DIRECTORY;

    var is_dir = (op.flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0;
    if (!is_dir and (op.flags & pal_abi.OPEN_FLAG_CREATE) == 0) {
        if (s.p_stat) |st_fn| {
            var st: sys.Stat = .{};
            if (st_fn(cpath, &st) == 0 and (st.st_mode & 0o170000) == 0o040000) {
                is_dir = true;
            }
        }
    }
    if (is_dir) {
        const od = s.p_opendir orelse {
            op.res_err = pal_abi.ERROR_UNSUPPORTED;
            return;
        };
        const d = od(cpath) orelse {
            op.res_err = mapDarwinErrno(s);
            return;
        };
        op.res_dir = d;
        op.res_is_dir = true;
        return;
    }

    const fd = open_fn(cpath, oflags, 0o666);
    if (fd < 0) {
        op.res_err = mapDarwinErrno(s);
        return;
    }
    op.res_fd = fd;
}

fn execDirRead(s: *PalState, op: *PendingOp) void {
    const rd = s.p_readdir orelse {
        op.res_err = pal_abi.ERROR_UNSUPPORTED;
        return;
    };
    const d = op.dir_ptr orelse {
        op.res_err = pal_abi.ERROR_BADF;
        return;
    };
    const buf = op.buf.?;
    const cap = op.len;
    var out_pos: usize = 0;
    while (rd(d)) |ent| {
        const nlen: usize = @min(@as(usize, ent.d_namlen), ent.d_name.len - 1);
        const name = ent.d_name[0..nlen];
        if (std.mem.eql(u8, name, ".") or std.mem.eql(u8, name, "..")) continue;
        const rec_len = 1 + nlen + 1;
        if (out_pos + rec_len > cap) break;
        buf[out_pos] = switch (ent.d_type) {
            4 => @intCast(pal_abi.STAT_KIND_DIR),
            10 => @intCast(pal_abi.STAT_KIND_SYMLINK),
            8 => @intCast(pal_abi.STAT_KIND_FILE),
            else => @intCast(pal_abi.STAT_KIND_UNKNOWN),
        };
        @memcpy(buf[out_pos + 1 ..][0..nlen], name);
        buf[out_pos + 1 + nlen] = 0;
        out_pos += rec_len;
    }
    op.res_ext = out_pos;
}

fn execPathStat(s: *PalState, op: *PendingOp) void {
    if (op.len < @sizeOf(pal_abi.AbiStat)) {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    }
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(op.buf2.?, op.len2, &cbuf) orelse {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    };
    var st: sys.Stat = .{};
    const rc = if ((op.flags & pal_abi.PATH_STAT_FLAG_NOFOLLOW) != 0)
        (s.p_lstat orelse {
            op.res_err = pal_abi.ERROR_UNSUPPORTED;
            return;
        })(cpath, &st)
    else
        (s.p_stat orelse {
            op.res_err = pal_abi.ERROR_UNSUPPORTED;
            return;
        })(cpath, &st);
    if (rc != 0) {
        op.res_err = mapDarwinErrno(s);
        return;
    }
    var abi_st: pal_abi.AbiStat = .{};
    sys.statToAbi(&st, &abi_st);
    const bytes = std.mem.asBytes(&abi_st);
    @memcpy(op.buf.?[0..bytes.len], bytes);
}

fn execNetLookup(s: *PalState, op: *PendingOp) void {
    const gai = s.p_getaddrinfo orelse {
        op.res_err = pal_abi.ERROR_UNSUPPORTED;
        return;
    };
    const fai = s.p_freeaddrinfo orelse {
        op.res_err = pal_abi.ERROR_UNSUPPORTED;
        return;
    };
    var cbuf: [512]u8 = undefined;
    if (op.len2 == 0 or op.len2 >= cbuf.len) {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    }
    @memcpy(cbuf[0..op.len2], op.buf2.?[0..op.len2]);
    cbuf[op.len2] = 0;
    const host_z: [*:0]const u8 = @ptrCast(&cbuf);

    var hints: sys.AddrInfo = .{ .ai_socktype = sys.SOCK_STREAM };
    var res_list: ?*sys.AddrInfo = null;
    if (gai(host_z, null, &hints, &res_list) != 0 or res_list == null) {
        op.res_err = pal_abi.ERROR_NOENT;
        return;
    }
    defer fai(res_list);

    var families: [16]u8 = undefined;
    var addr_bytes: [16 * 16]u8 = undefined;
    var count: usize = 0;
    var addr_off: usize = 0;
    var cur = res_list;
    while (cur) |ai| : (cur = ai.ai_next) {
        if (count >= families.len) break;
        const sa = ai.ai_addr orelse continue;
        if (ai.ai_family == sys.AF_INET and addr_off + 4 <= addr_bytes.len) {
            const sin: *const sys.SockAddrIn = @ptrCast(@alignCast(sa));
            families[count] = 4;
            @memcpy(addr_bytes[addr_off..][0..4], &sin.sin_addr);
            count += 1;
            addr_off += 4;
        } else if (ai.ai_family == sys.AF_INET6 and addr_off + 16 <= addr_bytes.len) {
            const sin6: *const sys.SockAddrIn6 = @ptrCast(@alignCast(sa));
            families[count] = 6;
            @memcpy(addr_bytes[addr_off..][0..16], &sin6.sin6_addr);
            count += 1;
            addr_off += 16;
        }
    }
    if (count == 0) {
        op.res_err = pal_abi.ERROR_NOENT;
        return;
    }
    const total_size: usize = 8 + count + addr_off;
    if (op.len < total_size) {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    }
    const out = op.buf.?;
    std.mem.writeInt(u32, out[0..4], @intCast(total_size), .little);
    std.mem.writeInt(u32, out[4..8], @intCast(count), .little);
    @memcpy(out[8..][0..count], families[0..count]);
    @memcpy(out[8 + count ..][0..addr_off], addr_bytes[0..addr_off]);
    op.res_ext = @intCast(total_size);
}

fn execNetCertVerify(s: *PalState, op: *PendingOp) void {
    const name = op.buf.?[0..op.len];
    const payload = op.buf2.?[0..op.len2];
    if (payload.len < 4) {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    }
    const cert_count = std.mem.readInt(u32, payload[0..4], .little);
    if (cert_count == 0 or cert_count > 16 or payload.len < 4 + @as(usize, cert_count) * 4) {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    }
    var off: usize = 4 + @as(usize, cert_count) * 4;
    var parsed_buf: [16]std.crypto.Certificate.Parsed = undefined;
    for (0..cert_count) |i| {
        const dlen = std.mem.readInt(u32, payload[4 + i * 4 ..][0..4], .little);
        if (dlen == 0 or off + dlen > payload.len) {
            op.res_err = pal_abi.ERROR_INVALID;
            return;
        }
        const der = payload[off .. off + dlen];
        off += dlen;
        if (!sys.isPlausibleDerSequence(der)) {
            op.res_err = pal_abi.ERROR_INVALID;
            return;
        }
        const cert: std.crypto.Certificate = .{ .buffer = der, .index = 0 };
        parsed_buf[i] = cert.parse() catch {
            op.res_err = pal_abi.ERROR_INVALID;
            return;
        };
    }

    const scratch_len: usize = 1024 * 1024;
    const alloc_fn = s.host.alloc orelse {
        op.res_err = pal_abi.ERROR_NOMEM;
        return;
    };
    const scratch_raw = alloc_fn(scratch_len) orelse {
        op.res_err = pal_abi.ERROR_NOMEM;
        return;
    };
    defer if (s.host.free) |ff| ff(scratch_raw, scratch_len);
    const scratch_ptr: [*]u8 = @ptrCast(scratch_raw);
    const pem_region = scratch_ptr[0 .. 512 * 1024];
    const arena_region = scratch_ptr[512 * 1024 .. scratch_len];

    var bundle: std.crypto.Certificate.Bundle = .empty;
    var fba = std.heap.FixedBufferAllocator.init(arena_region);
    const alloc = fba.allocator();
    defer bundle.deinit(alloc);

    const now_sec: i64 = @intCast(abiGetTime() / 1_000_000_000);
    if (s.p_open) |open_fn| {
        const fd = open_fn("/etc/ssl/cert.pem", sys.O_RDONLY, 0);
        if (fd >= 0) {
            defer if (s.p_close) |c| {
                _ = c(fd);
            };
            var total: usize = 0;
            if (s.p_read) |r_fn| {
                while (total < pem_region.len) {
                    const n = r_fn(fd, pem_region[total..].ptr, pem_region.len - total);
                    if (n <= 0) break;
                    total += @intCast(n);
                }
            }
            var it = std.mem.splitSequence(u8, pem_region[0..total], "-----BEGIN CERTIFICATE-----");
            _ = it.next();
            while (it.next()) |rest| {
                const end_idx = std.mem.indexOf(u8, rest, "-----END CERTIFICATE-----") orelse continue;
                const b64_raw = rest[0..end_idx];
                var compact_buf: [8192]u8 = undefined;
                var compact_len: usize = 0;
                for (b64_raw) |ch| {
                    if (ch != '\r' and ch != '\n' and ch != ' ' and ch != '\t') {
                        if (compact_len >= compact_buf.len) break;
                        compact_buf[compact_len] = ch;
                        compact_len += 1;
                    }
                }
                const der_start = bundle.bytes.items.len;
                const decoded_len = std.base64.standard.Decoder.calcSizeForSlice(compact_buf[0..compact_len]) catch continue;
                bundle.bytes.ensureUnusedCapacity(alloc, decoded_len) catch break;
                const dst = bundle.bytes.unusedCapacitySlice()[0..decoded_len];
                std.base64.standard.Decoder.decode(dst, compact_buf[0..compact_len]) catch continue;
                bundle.bytes.items.len += decoded_len;
                bundle.parseCert(alloc, @intCast(der_start), now_sec) catch continue;
            }
        }
    }

    var cur = parsed_buf[0];
    cur.verifyHostName(name) catch {
        op.res_ext = 0;
        return;
    };
    var depth: usize = 0;
    while (depth < cert_count) : (depth += 1) {
        if (bundle.verify(cur, now_sec)) |_| {
            op.res_ext = 1;
            return;
        } else |_| {}
        var found = false;
        for (parsed_buf[1..cert_count]) |cand| {
            if (std.mem.eql(u8, cur.issuer(), cand.subject())) {
                cur.verify(cand, now_sec) catch {
                    op.res_ext = 0;
                    return;
                };
                cur = cand;
                found = true;
                break;
            }
        }
        if (!found) break;
    }
    op.res_ext = 0;
}

fn executeWorkItem(ctx: *anyopaque, op_idx: u16) void {
    const s: *PalState = @ptrCast(@alignCast(ctx));
    const op = &s.ops[op_idx];
    op.res_err = 0;
    op.res_cont = 0;
    op.res_ext = 0;
    op.res_fd = -1;
    op.res_dir = null;
    op.res_is_dir = false;

    switch (op.work_kind) {
        .file_read => execFileRead(s, op),
        .file_write => execFileWrite(s, op),
        .file_seek => {
            const ls = s.p_lseek orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            const pos = ls(op.fd, op.arg_i64_a, @intCast(op.flags));
            if (pos < 0) op.res_err = mapDarwinErrno(s) else op.res_ext = @intCast(pos);
        },
        .handle_truncate => {
            const ft = s.p_ftruncate orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            if (ft(op.fd, op.arg_i64_a) != 0) op.res_err = mapDarwinErrno(s);
        },
        .path_open => execPathOpen(s, op),
        .dir_read => execDirRead(s, op),
        .path_stat => execPathStat(s, op),
        .path_link => {
            const lk = s.p_link orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var b1: [4096]u8 = undefined;
            var b2: [4096]u8 = undefined;
            const s1 = toCPath(op.buf.?, op.len, &b1) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            const s2 = toCPath(op.buf2.?, op.len2, &b2) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            if (lk(s1, s2) != 0) op.res_err = mapDarwinErrno(s);
        },
        .path_symlink => {
            const sl = s.p_symlink orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var b1: [4096]u8 = undefined;
            var b2: [4096]u8 = undefined;
            const s1 = toCPath(op.buf.?, op.len, &b1) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            const s2 = toCPath(op.buf2.?, op.len2, &b2) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            if (sl(s1, s2) != 0) op.res_err = mapDarwinErrno(s);
        },
        .path_readlink => {
            const rl = s.p_readlink orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var cbuf: [4096]u8 = undefined;
            const cpath = toCPath(op.buf2.?, op.len2, &cbuf) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            const n = rl(cpath, op.buf.?, op.len);
            if (n < 0) op.res_err = mapDarwinErrno(s) else op.res_ext = @intCast(n);
        },
        .path_chmod => {
            const cm = s.p_chmod orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var cbuf: [4096]u8 = undefined;
            const cpath = toCPath(op.buf.?, op.len, &cbuf) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            if (cm(cpath, op.flags) != 0) op.res_err = mapDarwinErrno(s);
        },
        .path_set_times => {
            const ut = s.p_utimensat orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var cbuf: [4096]u8 = undefined;
            const cpath = toCPath(op.buf.?, op.len, &cbuf) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            const UTIME_OMIT: isize = -2;
            const times = [2]sys.Timespec{
                .{ .tv_sec = @intCast(op.arg_i64_a), .tv_nsec = if ((op.flags & 1) != 0) @intCast(op.arg_u32_a) else UTIME_OMIT },
                .{ .tv_sec = @intCast(op.arg_i64_b), .tv_nsec = if ((op.flags & 2) != 0) @intCast(op.arg_u32_b) else UTIME_OMIT },
            };
            const at_flags: c_int = if ((op.flags & 4) != 0) sys.AT_SYMLINK_NOFOLLOW else 0;
            if (ut(sys.AT_FDCWD, cpath, &times, at_flags) != 0) op.res_err = mapDarwinErrno(s);
        },
        .path_remove => {
            var cbuf: [4096]u8 = undefined;
            const cpath = toCPath(op.buf.?, op.len, &cbuf) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            const rc = if ((op.flags & (pal_abi.PATH_REMOVE_FLAG_DIR | 0x200)) != 0)
                (s.p_rmdir orelse {
                    op.res_err = pal_abi.ERROR_UNSUPPORTED;
                    return;
                })(cpath)
            else
                (s.p_unlink orelse {
                    op.res_err = pal_abi.ERROR_UNSUPPORTED;
                    return;
                })(cpath);
            if (rc != 0) op.res_err = mapDarwinErrno(s);
        },
        .path_mkdir => {
            const mk = s.p_mkdir orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var cbuf: [4096]u8 = undefined;
            const cpath = toCPath(op.buf.?, op.len, &cbuf) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            if (mk(cpath, op.flags) != 0) op.res_err = mapDarwinErrno(s);
        },
        .path_rename => {
            const rn = s.p_rename orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var b1: [4096]u8 = undefined;
            var b2: [4096]u8 = undefined;
            const s1 = toCPath(op.buf.?, op.len, &b1) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            const s2 = toCPath(op.buf2.?, op.len2, &b2) orelse {
                op.res_err = pal_abi.ERROR_INVALID;
                return;
            };
            if (rn(s1, s2) != 0) op.res_err = mapDarwinErrno(s);
        },
        .net_lookup => execNetLookup(s, op),
        .net_cert_verify => execNetCertVerify(s, op),
        .proc_wait => {
            const wp = s.p_waitpid orelse {
                op.res_err = pal_abi.ERROR_UNSUPPORTED;
                return;
            };
            var status: c_int = 0;
            const pid = wp(op.pid, &status, 0);
            if (pid < 0) {
                op.res_err = mapDarwinErrno(s);
                return;
            }
            const exit_code: u32 = if ((status & 0x7f) == 0)
                @intCast((status >> 8) & 0xff)
            else
                @intCast(128 + (status & 0x7f));
            op.res_ext = exit_code;
        },
        .none => {},
    }
}

fn finishWorkerOp(s: *PalState, op: *PendingOp) void {
    const canceled = @atomicLoad(bool, &op.canceled, .acquire);
    const wkind = op.work_kind;
    const gov = op.guest_ov.?;
    const err = op.res_err;
    const cont = op.res_cont;
    const ext = op.res_ext;
    const rfd = op.res_fd;
    const rdir = op.res_dir;
    const ris_dir = op.res_is_dir;
    freeOp(s, op);

    if (canceled) {
        if (wkind == .path_open and err == 0) {
            if (ris_dir and rdir != null) {
                if (s.p_closedir) |cd| _ = cd(rdir);
            } else if (rfd >= 0) {
                if (s.p_close) |c| _ = c(rfd);
            }
        }
        return;
    }
    if (wkind == .path_open and err == 0) {
        const he = allocHandle(s) orelse {
            if (ris_dir and rdir != null) {
                if (s.p_closedir) |cd| _ = cd(rdir);
            } else if (rfd >= 0) {
                if (s.p_close) |c| _ = c(rfd);
            }
            completeInline(gov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        he.fd = rfd;
        he.dir_ptr = rdir;
        he.is_dir = ris_dir;
        completeInline(gov, 0, 0, he.token);
        return;
    }
    completeInline(gov, err, cont, ext);
}

fn abiSeek(ov: *pal_abi.Overlapped, handle: u64, off: i64, whence: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .file_seek;
    op.guest_ov = ov;
    op.fd = he.fd;
    op.arg_i64_a = off;
    op.flags = whence;
    submitOrRunInline(s, op);
}

fn abiHandleTruncate(ov: *pal_abi.Overlapped, handle: u64, size: i64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .handle_truncate;
    op.guest_ov = ov;
    op.fd = he.fd;
    op.arg_i64_a = size;
    submitOrRunInline(s, op);
}

fn abiHandleClose(handle: u64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse return;
    if (he.is_pty) {
        if (s.p_close) |c| {
            if (he.pty_read_fd >= 0) _ = c(he.pty_read_fd);
            if (he.pty_write_fd >= 0 and he.pty_write_fd != he.pty_read_fd) _ = c(he.pty_write_fd);
        }
    } else if (he.is_dir and he.dir_ptr != null) {
        if (s.p_closedir) |cd| _ = cd(he.dir_ptr);
    } else if (he.fd >= 0) {
        if (s.p_close) |c| _ = c(he.fd);
    }
    he.* = .{};
}

fn submitPathOp(ov: *pal_abi.Overlapped, wkind: WorkOpKind, buf: ?[*]u8, len: usize, buf2: ?[*]const u8, len2: usize, flags: u32) void {
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = wkind;
    op.guest_ov = ov;
    op.buf = buf;
    op.len = len;
    op.buf2 = buf2;
    op.len2 = len2;
    op.flags = flags;
    submitOrRunInline(s, op);
}

fn abiPathOpen(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_open, @constCast(path), len, null, 0, flags);
}
fn abiDirRead(ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_dir) {
        completeInline(ov, pal_abi.ERROR_NOTDIR, 0, 0);
        return;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .dir_read;
    op.guest_ov = ov;
    op.dir_ptr = he.dir_ptr;
    op.buf = buf;
    op.len = len;
    submitOrRunInline(s, op);
}
fn abiPathStat(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_stat, out, cap, path, len, flags);
}
fn abiPathLink(ov: *pal_abi.Overlapped, src: [*]const u8, s_len: usize, dst: [*]const u8, d_len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_link, @constCast(src), s_len, dst, d_len, flags);
}
fn abiPathSymlink(ov: *pal_abi.Overlapped, target: [*]const u8, t_len: usize, link: [*]const u8, l_len: usize, kind: u32) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_symlink, @constCast(target), t_len, link, l_len, kind);
}
fn abiPathReadlink(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_readlink, out, cap, path, len, 0);
}
fn abiPathChmod(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_chmod, @constCast(path), len, null, 0, mode);
}
fn abiPathSetTimes(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, at_sec: i64, at_nsec: u32, mt_sec: i64, mt_nsec: u32, flags: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_set_times;
    op.guest_ov = ov;
    op.buf = @constCast(path);
    op.len = len;
    op.arg_i64_a = at_sec;
    op.arg_u32_a = at_nsec;
    op.arg_i64_b = mt_sec;
    op.arg_u32_b = mt_nsec;
    op.flags = flags;
    submitOrRunInline(s, op);
}
fn abiPathRemove(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_remove, @constCast(path), len, null, 0, flags);
}
fn abiPathMkdir(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_mkdir, @constCast(path), len, null, 0, mode);
}
fn abiPathRename(ov: *pal_abi.Overlapped, old_p: [*]const u8, o_len: usize, new_p: [*]const u8, n_len: usize) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .path_rename, @constCast(old_p), o_len, new_p, n_len, 0);
}
fn parseIpv4(s: []const u8) ?[4]u8 {
    var out: [4]u8 = undefined;
    var part: usize = 0;
    var val: u32 = 0;
    var digits: usize = 0;
    for (s) |c| {
        if (c == '.') {
            if (digits == 0 or part >= 3 or val > 255) return null;
            out[part] = @intCast(val);
            part += 1;
            val = 0;
            digits = 0;
        } else if (c >= '0' and c <= '9') {
            val = val * 10 + (c - '0');
            digits += 1;
            if (digits > 3 or val > 255) return null;
        } else return null;
    }
    if (digits == 0 or part != 3 or val > 255) return null;
    out[3] = @intCast(val);
    return out;
}

fn abiNetOpen(ov: *pal_abi.Overlapped, addr_ptr: [*]const u8, addr_len: usize, port: u32, flags: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const sock_fn = s.p_socket orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const is_udp = (flags & 0x4) != 0;
    const is_listen = (flags & pal_abi.NET_OPEN_FLAG_LISTEN) != 0;
    const is_v6 = (flags & pal_abi.NET_OPEN_FLAG_IPV6) != 0;
    const fam: c_int = if (is_v6) sys.AF_INET6 else sys.AF_INET;
    const stype: c_int = if (is_udp) sys.SOCK_DGRAM else sys.SOCK_STREAM;
    const fd = sock_fn(fam, stype, 0);
    if (fd < 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    setNonBlock(s, fd);

    var sa4: sys.SockAddrIn = .{ .sin_port = std.mem.nativeToBig(u16, @intCast(port & 0xffff)) };
    var sa6: sys.SockAddrIn6 = .{ .sin6_port = std.mem.nativeToBig(u16, @intCast(port & 0xffff)) };
    const addr_slice = addr_ptr[0..addr_len];
    if (!is_v6 and addr_len > 0) {
        if (parseIpv4(addr_slice)) |ip4| {
            sa4.sin_addr = ip4;
        } else if (addr_len == 4) {
            @memcpy(&sa4.sin_addr, addr_slice);
        }
    } else if (is_v6 and addr_len == 16) {
        @memcpy(&sa6.sin6_addr, addr_slice);
    }
    const sa_ptr: *const anyopaque = if (is_v6) @ptrCast(&sa6) else @ptrCast(&sa4);
    const sa_len: u32 = if (is_v6) @sizeOf(sys.SockAddrIn6) else @sizeOf(sys.SockAddrIn);

    if (is_listen or is_udp) {
        if (is_listen) {
            var one: c_int = 1;
            if (s.p_setsockopt) |ss| _ = ss(fd, sys.SOL_SOCKET, sys.SO_REUSEADDR, @ptrCast(&one), @sizeOf(c_int));
        }
        const b_fn = s.p_bind orelse {
            if (s.p_close) |c| _ = c(fd);
            completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        if (b_fn(fd, sa_ptr, sa_len) != 0) {
            const err = mapDarwinErrno(s);
            if (s.p_close) |c| _ = c(fd);
            completeInline(ov, err, 0, 0);
            return;
        }
        if (is_listen) {
            const l_fn = s.p_listen orelse {
                if (s.p_close) |c| _ = c(fd);
                completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
                return;
            };
            if (l_fn(fd, 128) != 0) {
                const err = mapDarwinErrno(s);
                if (s.p_close) |c| _ = c(fd);
                completeInline(ov, err, 0, 0);
                return;
            }
        }
        const he = allocHandle(s) orelse {
            if (s.p_close) |c| _ = c(fd);
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        he.fd = fd;
        he.is_socket = true;
        he.is_listener = is_listen;
        he.is_udp = is_udp;
        completeInline(ov, 0, 0, he.token);
        return;
    }

    const c_fn = s.p_connect orelse {
        if (s.p_close) |c| _ = c(fd);
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    if (c_fn(fd, sa_ptr, sa_len) == 0) {
        const he = allocHandle(s) orelse {
            if (s.p_close) |c| _ = c(fd);
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        he.fd = fd;
        he.is_socket = true;
        completeInline(ov, 0, 0, he.token);
        return;
    }
    const err = rawErrno(s);
    if (err != 36) { // EINPROGRESS = 36 on Darwin
        if (s.p_close) |c| _ = c(fd);
        completeInline(ov, sys.mapErrno(err), 0, 0);
        return;
    }
    const op = allocOp(s) orelse {
        if (s.p_close) |c| _ = c(fd);
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .net_connect;
    op.guest_ov = ov;
    op.fd = fd;
    kqUpdate(s, @intCast(fd), sys.EVFILT_WRITE, sys.EV_ADD | sys.EV_ONESHOT, op);
}

fn abiNetAccept(ov: *pal_abi.Overlapped, handle: u64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const ac_fn = s.p_accept orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const cfd = ac_fn(he.fd, null, null);
    if (cfd >= 0) {
        setNonBlock(s, cfd);
        const cli = allocHandle(s) orelse {
            if (s.p_close) |c| _ = c(cfd);
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        cli.fd = cfd;
        cli.is_socket = true;
        completeInline(ov, 0, 0, cli.token);
        return;
    }
    const err = rawErrno(s);
    if (err != 35) { // EAGAIN
        completeInline(ov, sys.mapErrno(err), 0, 0);
        return;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .net_accept;
    op.guest_ov = ov;
    op.fd = he.fd;
    kqUpdate(s, @intCast(he.fd), sys.EVFILT_READ, sys.EV_ADD | sys.EV_ONESHOT, op);
}

fn abiNetLookup(ov: *pal_abi.Overlapped, name_ptr: [*]const u8, name_len: usize, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .net_lookup, out, cap, name_ptr, name_len, 0);
}

fn abiNetCertVerify(ov: *pal_abi.Overlapped, name_ptr: [*]const u8, name_len: usize, chain_ptr: [*]const u8, chain_len: usize) callconv(pal_abi.pal_cc) void {
    submitPathOp(ov, .net_cert_verify, @constCast(name_ptr), name_len, chain_ptr, chain_len, 0);
}

fn nextNullStr(buf: []const u8, pos: *usize) ?[]const u8 {
    if (pos.* >= buf.len) return null;
    const start = pos.*;
    var end = start;
    while (end < buf.len and buf[end] != 0) : (end += 1) {}
    pos.* = if (end < buf.len) end + 1 else buf.len;
    return buf[start..end];
}

fn abiProcessSpawn(ov: *pal_abi.Overlapped, cfg_ptr: [*]const u8, cfg_len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const fork_fn = s.p_fork orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const exec_fn = s.p_execvp orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const cfg = cfg_ptr[0..cfg_len];
    var pos: usize = 0;
    const prog = nextNullStr(cfg, &pos) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    var argv_buf: [65]?[*:0]const u8 = @splat(null);
    var arg_storage: [4096]u8 = undefined;
    var arg_off: usize = 0;
    var argc: usize = 0;
    while (argc < 64) {
        const a = nextNullStr(cfg, &pos) orelse break;
        if (a.len == 0) break;
        if (arg_off + a.len + 1 > arg_storage.len) break;
        @memcpy(arg_storage[arg_off..][0..a.len], a);
        arg_storage[arg_off + a.len] = 0;
        argv_buf[argc] = @ptrCast(&arg_storage[arg_off]);
        arg_off += a.len + 1;
        argc += 1;
    }
    var prog_buf: [512]u8 = undefined;
    if (prog.len == 0 or prog.len >= prog_buf.len) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    @memcpy(prog_buf[0..prog.len], prog);
    prog_buf[prog.len] = 0;
    const prog_z: [*:0]const u8 = @ptrCast(&prog_buf);
    if (argc == 0) {
        argv_buf[0] = prog_z;
    }
    const cwd = nextNullStr(cfg, &pos) orelse "";
    var cwd_buf: [1024]u8 = undefined;
    var cwd_z: ?[*:0]const u8 = null;
    if (cwd.len > 0 and cwd.len < cwd_buf.len) {
        @memcpy(cwd_buf[0..cwd.len], cwd);
        cwd_buf[cwd.len] = 0;
        cwd_z = @ptrCast(&cwd_buf);
    }

    var master_fd: i32 = -1;
    var slave_fd: i32 = -1;
    const op_pty = s.p_openpty orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    if (op_pty(&master_fd, &slave_fd, null, null, null) != 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }

    const pid = fork_fn();
    if (pid < 0) {
        if (s.p_close) |c| {
            _ = c(master_fd);
            _ = c(slave_fd);
        }
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    if (pid == 0) {
        if (s.p_close) |c| _ = c(master_fd);
        if (s.p_setsid) |ss| _ = ss();
        if (s.p_ioctl) |io| _ = io(slave_fd, sys.TIOCSCTTY, @as(c_int, 0));
        if (s.p_dup2) |d2| {
            _ = d2(slave_fd, 0);
            _ = d2(slave_fd, 1);
            _ = d2(slave_fd, 2);
        }
        if (slave_fd > 2) {
            if (s.p_close) |c| _ = c(slave_fd);
        }
        if (cwd_z) |cz| {
            if (s.p_chdir) |cd| _ = cd(cz);
        }
        _ = exec_fn(prog_z, &argv_buf);
        if (s.p_exit) |ex| ex(127);
        while (true) {}
    }
    if (s.p_close) |c| _ = c(slave_fd);
    setNonBlock(s, master_fd);

    const p_he = allocHandle(s) orelse {
        if (s.p_close) |c| _ = c(master_fd);
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    const tty_he = allocHandle(s) orelse {
        p_he.used = false;
        if (s.p_close) |c| _ = c(master_fd);
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    p_he.is_process = true;
    p_he.pid = pid;
    tty_he.is_pty = true;
    tty_he.pty_read_fd = master_fd;
    tty_he.pty_write_fd = master_fd;
    completeInline(ov, 0, 0, (tty_he.token << 32) | (p_he.token & 0xffff_ffff));
}

fn abiProcessPipe(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const pp = s.p_pipe orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var fds: [2]i32 = .{ -1, -1 };
    if (pp(&fds) != 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    const r_he = allocHandle(s) orelse {
        if (s.p_close) |c| {
            _ = c(fds[0]);
            _ = c(fds[1]);
        }
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    const w_he = allocHandle(s) orelse {
        r_he.used = false;
        if (s.p_close) |c| {
            _ = c(fds[0]);
            _ = c(fds[1]);
        }
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    r_he.fd = fds[0];
    w_he.fd = fds[1];
    completeInline(ov, 0, 0, (w_he.token << 32) | (r_he.token & 0xffff_ffff));
}

fn abiProcessWait(ov: *pal_abi.Overlapped, handle: u64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_process or he.pid <= 0) {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .proc_wait;
    op.guest_ov = ov;
    op.pid = he.pid;
    submitOrRunInline(s, op);
}

fn abiProcessSignal(handle: u64, sig: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse return;
    if (!he.is_process or he.pid <= 0) return;
    if (s.p_kill) |k| _ = k(he.pid, @intCast(sig));
}

fn abiSignalWait(ov: *pal_abi.Overlapped, mask: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (mask == 0 or s.kq_fd < 0) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .signal_wait;
    op.guest_ov = ov;
    op.flags = mask;
    for (1..32) |sig| {
        if ((mask & (@as(u32, 1) << @intCast(sig))) != 0) {
            kqUpdate(s, sig, sys.EVFILT_SIGNAL, sys.EV_ADD | sys.EV_ONESHOT, op);
        }
    }
}

fn abiTtySetMode(handle: u64, mode: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const tg = s.p_tcgetattr orelse return;
    const ts = s.p_tcsetattr orelse return;
    const fd: i32 = if (handle <= 2) @intCast(handle) else blk: {
        const he = findHandle(s, handle) orelse return;
        break :blk if (he.is_pty) he.pty_read_fd else he.fd;
    };
    var t: sys.Termios = .{};
    if (tg(fd, &t) != 0) return;
    if (mode == 1) { // raw
        t.c_iflag &= ~@as(u64, 0x0200 | 0x0100); // ~(IXON | ICRNL)
        t.c_oflag &= ~@as(u64, 0x0001); // ~OPOST
        t.c_lflag &= ~@as(u64, 0x0100 | 0x0008 | 0x0080); // ~(ICANON | ECHO | ISIG)
        t.c_cc[16] = 1; // VMIN
        t.c_cc[17] = 0; // VTIME
    } else {
        t.c_iflag |= 0x0200 | 0x0100;
        t.c_oflag |= 0x0001;
        t.c_lflag |= 0x0100 | 0x0008 | 0x0080;
    }
    _ = ts(fd, 0, &t);
}

fn abiTtyGetSize(handle: u64) callconv(pal_abi.pal_cc) u32 {
    const s = statePtr();
    const io = s.p_ioctl orelse return (80 << 16) | 24;
    const fd: i32 = if (handle <= 2) @intCast(handle) else blk: {
        const he = findHandle(s, handle) orelse return (80 << 16) | 24;
        break :blk if (he.is_pty) he.pty_read_fd else he.fd;
    };
    var ws: sys.Winsize = .{};
    if (io(fd, sys.TIOCGWINSZ, &ws) != 0 or ws.ws_col == 0 or ws.ws_row == 0) {
        return (80 << 16) | 24;
    }
    return (@as(u32, ws.ws_col) << 16) | @as(u32, ws.ws_row);
}

fn abiTtySetSize(handle: u64, cols: u32, rows: u32) callconv(pal_abi.pal_cc) u32 {
    if (cols == 0 or rows == 0 or cols > 65535 or rows > 65535) return pal_abi.ERROR_INVALID;
    const s = statePtr();
    const io = s.p_ioctl orelse return pal_abi.ERROR_UNSUPPORTED;
    const fd: i32 = if (handle <= 2) @intCast(handle) else blk: {
        const he = findHandle(s, handle) orelse return pal_abi.ERROR_BADF;
        break :blk if (he.is_pty) he.pty_read_fd else he.fd;
    };
    var ws: sys.Winsize = .{
        .ws_row = @intCast(rows),
        .ws_col = @intCast(cols),
    };
    return if (io(fd, sys.TIOCSWINSZ, &ws) != 0) mapDarwinErrno(s) else 0;
}

fn abiFdIsatty(handle: u32) callconv(pal_abi.pal_cc) u32 {
    const s = statePtr();
    const it = s.p_isatty orelse return 0;
    const fd: i32 = if (handle <= 2) @intCast(handle) else blk: {
        const he = findHandle(s, handle) orelse return 0;
        if (he.is_pty) return 1;
        break :blk he.fd;
    };
    return if (it(fd) != 0) 1 else 0;
}

fn computeSystemInfoHash(s: *PalState) u64 {
    var h: u64 = 0xcbf29ce484222325;
    const keys = [_][*:0]const u8{ "LANG", "LC_ALL", "TZ", "COLORFGBG" };
    for (keys) |k| {
        if (envLookup(s, k)) |v| {
            for (v) |b| {
                h ^= b;
                h *%= 0x100000001b3;
            }
        }
        h ^= 0xff;
        h *%= 0x100000001b3;
    }
    return h;
}

fn abiSystemInfoWait(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const h = computeSystemInfoHash(s);
    if (!s.has_system_info_hash) {
        s.system_info_hash = h;
        s.has_system_info_hash = true;
    } else if (h != s.system_info_hash) {
        s.system_info_hash = h;
        s.system_info_version +%= 1;
        completeInline(ov, 0, 0, s.system_info_version);
        return;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .system_info_wait;
    op.guest_ov = ov;
    op.deadline_ms = nowMs(s) + 250;
}

pub fn unixPlatformMain() callconv(.c) i32 {
    const s = statePtr();
    s.* = .{};
    s.host = hostPtr().*;
    const ll = s.host.load_library orelse return 10;
    s.libsystem = ll("/usr/lib/libSystem.B.dylib", 1) orelse return 11;

    s.p_read = @ptrCast(@alignCast(sym(s, "read") orelse return 12));
    s.p_write = @ptrCast(@alignCast(sym(s, "write") orelse return 12));
    s.p_pread = @ptrCast(@alignCast(sym(s, "pread") orelse return 12));
    s.p_pwrite = @ptrCast(@alignCast(sym(s, "pwrite") orelse return 12));
    s.p_open = @ptrCast(@alignCast(sym(s, "open") orelse return 12));
    s.p_lseek = @ptrCast(@alignCast(sym(s, "lseek") orelse return 12));
    s.p_ftruncate = @ptrCast(@alignCast(sym(s, "ftruncate") orelse return 12));
    s.p_close = @ptrCast(@alignCast(sym(s, "close") orelse return 12));
    s.p_mkdir = @ptrCast(@alignCast(sym(s, "mkdir") orelse return 12));
    s.p_unlink = @ptrCast(@alignCast(sym(s, "unlink") orelse return 12));
    s.p_rmdir = @ptrCast(@alignCast(sym(s, "rmdir") orelse return 12));
    s.p_rename = @ptrCast(@alignCast(sym(s, "rename") orelse return 12));
    s.p_link = @ptrCast(@alignCast(sym(s, "link") orelse return 12));
    s.p_symlink = @ptrCast(@alignCast(sym(s, "symlink") orelse return 12));
    s.p_readlink = @ptrCast(@alignCast(sym(s, "readlink") orelse return 12));
    s.p_chmod = @ptrCast(@alignCast(sym(s, "chmod") orelse return 12));
    s.p_utimensat = @ptrCast(@alignCast(sym(s, "utimensat")));
    s.p_stat = @ptrCast(@alignCast(sym(s, "stat$INODE64") orelse sym(s, "stat")));
    s.p_lstat = @ptrCast(@alignCast(sym(s, "lstat$INODE64") orelse sym(s, "lstat")));
    s.p_fstat = @ptrCast(@alignCast(sym(s, "fstat$INODE64") orelse sym(s, "fstat")));
    s.p_opendir = @ptrCast(@alignCast(sym(s, "opendir$INODE64") orelse sym(s, "opendir")));
    s.p_readdir = @ptrCast(@alignCast(sym(s, "readdir$INODE64") orelse sym(s, "readdir")));
    s.p_closedir = @ptrCast(@alignCast(sym(s, "closedir")));
    s.p_getcwd = @ptrCast(@alignCast(sym(s, "getcwd") orelse return 12));
    s.p_chdir = @ptrCast(@alignCast(sym(s, "chdir") orelse return 12));
    s.p_getenv = @ptrCast(@alignCast(sym(s, "getenv")));
    s.p_ns_get_environ = @ptrCast(@alignCast(sym(s, "_NSGetEnviron")));
    s.p_pipe = @ptrCast(@alignCast(sym(s, "pipe") orelse return 12));
    s.p_fcntl = @ptrCast(@alignCast(sym(s, "fcntl")));
    s.p_kqueue = @ptrCast(@alignCast(sym(s, "kqueue")));
    s.p_kevent = @ptrCast(@alignCast(sym(s, "kevent")));
    s.p_socket = @ptrCast(@alignCast(sym(s, "socket")));
    s.p_connect = @ptrCast(@alignCast(sym(s, "connect")));
    s.p_bind = @ptrCast(@alignCast(sym(s, "bind")));
    s.p_listen = @ptrCast(@alignCast(sym(s, "listen")));
    s.p_accept = @ptrCast(@alignCast(sym(s, "accept")));
    s.p_getsockopt = @ptrCast(@alignCast(sym(s, "getsockopt")));
    s.p_setsockopt = @ptrCast(@alignCast(sym(s, "setsockopt")));
    s.p_getaddrinfo = @ptrCast(@alignCast(sym(s, "getaddrinfo")));
    s.p_freeaddrinfo = @ptrCast(@alignCast(sym(s, "freeaddrinfo")));
    s.p_fork = @ptrCast(@alignCast(sym(s, "fork")));
    s.p_execvp = @ptrCast(@alignCast(sym(s, "execvp")));
    s.p_dup2 = @ptrCast(@alignCast(sym(s, "dup2")));
    s.p_setsid = @ptrCast(@alignCast(sym(s, "setsid")));
    s.p_waitpid = @ptrCast(@alignCast(sym(s, "waitpid")));
    s.p_kill = @ptrCast(@alignCast(sym(s, "kill")));
    s.p_openpty = @ptrCast(@alignCast(sym(s, "openpty")));
    s.p_ioctl = @ptrCast(@alignCast(sym(s, "ioctl")));
    s.p_tcgetattr = @ptrCast(@alignCast(sym(s, "tcgetattr")));
    s.p_tcsetattr = @ptrCast(@alignCast(sym(s, "tcsetattr")));
    s.p_isatty = @ptrCast(@alignCast(sym(s, "isatty") orelse return 12));
    s.p_arc4random_buf = @ptrCast(@alignCast(sym(s, "arc4random_buf") orelse return 12));
    s.p_gettimeofday = @ptrCast(@alignCast(sym(s, "gettimeofday") orelse return 12));
    s.p_clock_gettime = @ptrCast(@alignCast(sym(s, "clock_gettime")));
    s.p_mutex_init = @ptrCast(@alignCast(sym(s, "pthread_mutex_init")));
    s.p_mutex_lock = @ptrCast(@alignCast(sym(s, "pthread_mutex_lock")));
    s.p_mutex_unlock = @ptrCast(@alignCast(sym(s, "pthread_mutex_unlock")));
    s.p_exit = @ptrCast(@alignCast(sym(s, "exit") orelse return 12));
    s.p_errno = @ptrCast(@alignCast(sym(s, "__error") orelse return 12));

    if (s.p_mutex_init) |mi| _ = mi(&s.handles_mu, null);
    if (s.p_kqueue) |kq| s.kq_fd = kq();
    if (s.p_pipe) |pp| {
        var wfds: [2]i32 = .{ -1, -1 };
        if (pp(&wfds) == 0) {
            setNonBlock(s, wfds[0]);
            setNonBlock(s, wfds[1]);
            s.wake_rd_fd = wfds[0];
            s.wake_wr_fd = wfds[1];
            kqUpdate(s, @intCast(wfds[0]), sys.EVFILT_READ, sys.EV_ADD | sys.EV_CLEAR, null);
        }
    }
    if (s.host.alloc) |alloc_fn| {
        if (alloc_fn(@sizeOf(pool_mod.DarwinWorkerPool))) |pmem| {
            const pool: *pool_mod.DarwinWorkerPool = @ptrCast(@alignCast(pmem));
            pool.* = .{};
            pool.wake_wr_fd = s.wake_wr_fd;
            pool.ctx = s;
            pool.exec_fn = executeWorkItem;
            pool.p_mutex_init = s.p_mutex_init;
            pool.p_mutex_lock = s.p_mutex_lock;
            pool.p_mutex_unlock = s.p_mutex_unlock;
            pool.p_cond_init = @ptrCast(@alignCast(sym(s, "pthread_cond_init")));
            pool.p_cond_signal = @ptrCast(@alignCast(sym(s, "pthread_cond_signal")));
            pool.p_cond_timedwait = @ptrCast(@alignCast(sym(s, "pthread_cond_timedwait")));
            pool.p_pthread_create = @ptrCast(@alignCast(sym(s, "pthread_create")));
            pool.p_pthread_detach = @ptrCast(@alignCast(sym(s, "pthread_detach")));
            pool.p_attr_init = @ptrCast(@alignCast(sym(s, "pthread_attr_init")));
            pool.p_attr_setstacksize = @ptrCast(@alignCast(sym(s, "pthread_attr_setstacksize")));
            pool.p_attr_destroy = @ptrCast(@alignCast(sym(s, "pthread_attr_destroy")));
            pool.p_write = s.p_write;
            pool.p_clock_gettime = @ptrCast(s.p_clock_gettime);
            if (pool.initSync()) {
                s.pool_ptr = pool;
            }
        }
    }

    const runtime_len = s.host.runtime_slice_size;
    if (runtime_len == 0 or s.host.wasm_bytes == null or s.host.wasm_len == 0) return 13;

    const alloc = s.host.alloc orelse return 14;
    if (s.host.set_jit_write_protect) |jp| jp(0);
    const mem_raw = alloc(pal_abi.PREFIX_ABI_OFFSET + runtime_len) orelse return 15;
    const mem_base: [*]u8 = @ptrCast(mem_raw);

    const table: *pal_abi.PalAbiTable = @ptrCast(@alignCast(mem_base));
    table.* = .{
        .wasm_bytes = s.host.wasm_bytes,
        .wasm_len = s.host.wasm_len,
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
    @memcpy(code_ptr[0..runtime_len], s.host.runtime_slice[0..runtime_len]);
    if (s.host.set_jit_write_protect) |jp| jp(1);
    if (s.host.flush_cache) |fc| fc(code_ptr, runtime_len);

    const RuntimeEntry = *const fn () callconv(pal_abi.pal_cc) void;
    const entry: RuntimeEntry = @ptrCast(@alignCast(code_ptr));

    while (true) {
        entry();
        if (s.pending_count == 0) break;

        if (s.pool_ptr) |pool| {
            var drained = false;
            while (pool.popDone()) |done_idx| {
                finishWorkerOp(s, &s.ops[done_idx]);
                drained = true;
            }
            if (drained) continue;
        }

        const now = nowMs(s);
        var min_ms: c_int = -1;
        var fired = false;

        for (&s.ops) |*op| {
            if (!op.used) continue;
            if (op.kind == .timer or op.kind == .system_info_wait) {
                if (op.deadline_ms <= now) {
                    if (op.kind == .timer) {
                        const gov = op.guest_ov.?;
                        freeOp(s, op);
                        completeInline(gov, 0, 0, 0);
                        fired = true;
                    } else {
                        const h = computeSystemInfoHash(s);
                        if (h != s.system_info_hash) {
                            s.system_info_hash = h;
                            s.system_info_version +%= 1;
                            const gov = op.guest_ov.?;
                            freeOp(s, op);
                            completeInline(gov, 0, 0, s.system_info_version);
                            fired = true;
                        } else {
                            op.deadline_ms = now + 250;
                            if (min_ms < 0 or 250 < min_ms) min_ms = 250;
                        }
                    }
                } else {
                    const rem: c_int = @intCast(@min(op.deadline_ms - now, 0x7fff_ffff));
                    if (min_ms < 0 or rem < min_ms) min_ms = rem;
                }
            }
        }
        if (fired) continue;

        var events: [32]sys.Kevent = undefined;
        var ts: sys.Timespec = undefined;
        const ts_ptr: ?*const sys.Timespec = if (min_ms >= 0) blk: {
            ts = .{
                .tv_sec = @divTrunc(min_ms, 1000),
                .tv_nsec = @rem(min_ms, 1000) * 1_000_000,
            };
            break :blk &ts;
        } else null;

        const nev = if (s.p_kevent) |kev| kev(s.kq_fd, null, 0, &events, events.len, ts_ptr) else 0;
        if (nev > 0) {
            for (events[0..@intCast(nev)]) |ev| {
                if (ev.ident == @as(usize, @intCast(s.wake_rd_fd)) and ev.filter == sys.EVFILT_READ) {
                    var drain_buf: [64]u8 = undefined;
                    if (s.p_read) |r| _ = r(s.wake_rd_fd, &drain_buf, drain_buf.len);
                    if (s.pool_ptr) |pool| {
                        while (pool.popDone()) |done_idx| {
                            finishWorkerOp(s, &s.ops[done_idx]);
                        }
                    }
                    continue;
                }
                const op: *PendingOp = @ptrCast(@alignCast(ev.udata orelse continue));
                if (!op.used) continue;
                const gov = op.guest_ov.?;
                switch (op.kind) {
                    .read_fd => {
                        const n = s.p_read.?(op.fd, op.buf.?, op.len);
                        if (n < 0) {
                            const err = rawErrno(s);
                            if (err == 35) {
                                kqUpdate(s, @intCast(op.fd), sys.EVFILT_READ, sys.EV_ADD | sys.EV_ONESHOT, op);
                                continue;
                            }
                            freeOp(s, op);
                            completeInline(gov, sys.mapErrno(err), 0, 0);
                        } else {
                            freeOp(s, op);
                            completeInline(gov, 0, 0, @intCast(n));
                        }
                    },
                    .write_fd => {
                        const n = s.p_write.?(op.fd, op.buf2.?, op.len2);
                        if (n < 0) {
                            const err = rawErrno(s);
                            if (err == 35) {
                                kqUpdate(s, @intCast(op.fd), sys.EVFILT_WRITE, sys.EV_ADD | sys.EV_ONESHOT, op);
                                continue;
                            }
                            freeOp(s, op);
                            completeInline(gov, sys.mapErrno(err), 0, 0);
                        } else {
                            freeOp(s, op);
                            completeInline(gov, 0, 0, @intCast(n));
                        }
                    },
                    .net_connect => {
                        var so_err: c_int = 0;
                        var slen: u32 = @sizeOf(c_int);
                        if (s.p_getsockopt) |gs| {
                            _ = gs(op.fd, sys.SOL_SOCKET, sys.SO_ERROR, @ptrCast(&so_err), &slen);
                        }
                        const fd = op.fd;
                        freeOp(s, op);
                        if (so_err != 0) {
                            if (s.p_close) |c| _ = c(fd);
                            completeInline(gov, sys.mapErrno(so_err), 0, 0);
                        } else if (allocHandle(s)) |he| {
                            he.fd = fd;
                            he.is_socket = true;
                            completeInline(gov, 0, 0, he.token);
                        } else {
                            if (s.p_close) |c| _ = c(fd);
                            completeInline(gov, pal_abi.ERROR_NOMEM, 0, 0);
                        }
                    },
                    .net_accept => {
                        const cfd = s.p_accept.?(op.fd, null, null);
                        if (cfd < 0) {
                            const err = rawErrno(s);
                            if (err == 35) {
                                kqUpdate(s, @intCast(op.fd), sys.EVFILT_READ, sys.EV_ADD | sys.EV_ONESHOT, op);
                                continue;
                            }
                            freeOp(s, op);
                            completeInline(gov, sys.mapErrno(err), 0, 0);
                        } else {
                            setNonBlock(s, cfd);
                            freeOp(s, op);
                            if (allocHandle(s)) |cli| {
                                cli.fd = cfd;
                                cli.is_socket = true;
                                completeInline(gov, 0, 0, cli.token);
                            } else {
                                if (s.p_close) |c| _ = c(cfd);
                                completeInline(gov, pal_abi.ERROR_NOMEM, 0, 0);
                            }
                        }
                    },
                    .signal_wait => {
                        const mask = op.flags;
                        for (1..32) |sig| {
                            if (sig != ev.ident and (mask & (@as(u32, 1) << @intCast(sig))) != 0) {
                                kqUpdate(s, sig, sys.EVFILT_SIGNAL, sys.EV_DELETE, null);
                            }
                        }
                        freeOp(s, op);
                        completeInline(gov, 0, 0, @intCast(ev.ident));
                    },
                    else => {},
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
        @export(&unixPlatformMain, .{ .name = "unix_platform_main" });
        switch (builtin.cpu.arch) {
            .x86_64 => asm (
                \\.section .text.unix_platform_entry,"ax"
                \\.globl unix_platform_entry
                \\unix_platform_entry:
                \\jmp unix_platform_main
            ),
            .aarch64 => asm (
                \\.section .text.unix_platform_entry,"ax"
                \\.globl unix_platform_entry
                \\unix_platform_entry:
                \\b unix_platform_main
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
            fn _lshrti3(a: u128, b: i32) callconv(.c) u128 {
                const shift: u7 = @intCast(@as(u32, @bitCast(b)) & 127);
                if (shift == 0) return a;
                const lo: u64 = @truncate(a);
                const hi: u64 = @truncate(a >> 64);
                if (shift >= 64) {
                    const s: u6 = @intCast(shift - 64);
                    return @as(u128, hi >> s);
                }
                const s: u6 = @intCast(shift);
                const inv: u6 = @intCast(64 - @as(u7, s));
                const new_lo = (lo >> s) | (hi << inv);
                const new_hi = hi >> s;
                return (@as(u128, new_hi) << 64) | new_lo;
            }
        };
        @export(&Intrinsics._memcpy, .{ .name = "memcpy" });
        @export(&Intrinsics._memset, .{ .name = "memset" });
        @export(&Intrinsics._memmove, .{ .name = "memmove" });
        @export(&Intrinsics._memcmp, .{ .name = "memcmp" });
        @export(&Intrinsics._memcmp, .{ .name = "bcmp" });
        @export(&Intrinsics._strlen, .{ .name = "strlen" });
        @export(&Intrinsics._lshrti3, .{ .name = "__lshrti3" });
    }
}

test "Darwin errno mapping, stat projection, DER guard, and IPv4 parsing" {
    _ = pool_mod;
    try std.testing.expectEqual(@as(u32, pal_abi.ERROR_AGAIN), sys.mapErrno(35));
    try std.testing.expectEqual(@as(u32, pal_abi.ERROR_AGAIN), sys.mapErrno(36));
    try std.testing.expectEqual(@as(u32, pal_abi.ERROR_NOTTY), sys.mapErrno(25));
    try std.testing.expectEqual(@as(u32, pal_abi.ERROR_TIMEDOUT), sys.mapErrno(60));

    var st: sys.Stat = .{
        .st_dev = 7,
        .st_mode = 0o100644,
        .st_nlink = 2,
        .st_ino = 12345,
        .st_uid = 501,
        .st_gid = 20,
        .st_size = 4096,
        .st_mtimespec = .{ .tv_sec = 10, .tv_nsec = 200 },
    };
    var out: pal_abi.AbiStat = .{};
    sys.statToAbi(&st, &out);
    try std.testing.expectEqual(@as(u32, pal_abi.STAT_KIND_FILE), out.kind);
    try std.testing.expectEqual(@as(u64, 4096), out.size);
    try std.testing.expectEqual(@as(i64, 10_000_000_200), out.mtime_ns);

    try std.testing.expect(!sys.isPlausibleDerSequence(&[_]u8{ 0x30, 0x82, 0x01, 0x00 }));
    try std.testing.expect(sys.isPlausibleDerSequence(&[_]u8{ 0x30, 0x02, 0x05, 0x00 }));

    const ip = parseIpv4("127.0.0.1") orelse return error.TestExpectedEqual;
    try std.testing.expectEqualSlices(u8, &[_]u8{ 127, 0, 0, 1 }, &ip);
    try std.testing.expect(parseIpv4("999.0.0.1") == null);
}
