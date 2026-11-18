const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");

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

const MAX_HANDLES = 256;
const MAX_OPS = 256;

const HandleEntry = struct {
    used: bool = false,
    token: u64 = 0,
    fd: i32 = -1,
};

const PollFd = extern struct {
    fd: i32,
    events: i16,
    revents: i16,
};

const Timeval = extern struct {
    sec: i64,
    usec: i32,
    _pad: i32 = 0,
};

const OpKind = enum(u8) {
    none = 0,
    read_fd = 1,
    timer = 2,
};

const PendingOp = struct {
    used: bool = false,
    kind: OpKind = .none,
    guest_ov: ?*pal_abi.Overlapped = null,
    fd: i32 = -1,
    buf: ?[*]u8 = null,
    len: usize = 0,
    deadline_ms: u64 = 0,
};

const PalState = struct {
    host: UnixPlatformHost = undefined,
    libsystem: ?*anyopaque = null,
    next_token: u64 = 3,
    handles: [MAX_HANDLES]HandleEntry = @splat(.{}),
    ops: [MAX_OPS]PendingOp = @splat(.{}),
    pending_count: usize = 0,

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
    p_getcwd: ?*const fn ([*]u8, usize) callconv(.c) ?[*]u8 = null,
    p_chdir: ?*const fn ([*:0]const u8) callconv(.c) c_int = null,
    p_pipe: ?*const fn (*[2]i32) callconv(.c) c_int = null,
    p_poll: ?*const fn ([*]PollFd, u32, c_int) callconv(.c) c_int = null,
    p_arc4random_buf: ?*const fn ([*]u8, usize) callconv(.c) void = null,
    p_gettimeofday: ?*const fn (*Timeval, ?*anyopaque) callconv(.c) c_int = null,
    p_isatty: ?*const fn (i32) callconv(.c) c_int = null,
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

fn mapDarwinErrno(s: *PalState) u32 {
    const p_err = s.p_errno orelse return pal_abi.ERROR_IO;
    const e = p_err().*;
    return switch (e) {
        0 => 0,
        1 => pal_abi.ERROR_PERM,
        2 => pal_abi.ERROR_NOENT,
        9 => pal_abi.ERROR_BADF,
        12 => pal_abi.ERROR_NOMEM,
        13 => pal_abi.ERROR_ACCES,
        17 => pal_abi.ERROR_EXIST,
        20 => pal_abi.ERROR_NOTDIR,
        21 => pal_abi.ERROR_ISDIR,
        22 => pal_abi.ERROR_INVALID,
        29 => pal_abi.ERROR_SPIPE,
        32 => pal_abi.ERROR_PIPE,
        35 => pal_abi.ERROR_AGAIN,
        60 => pal_abi.ERROR_TIMEDOUT,
        61 => pal_abi.ERROR_CONNREFUSED,
        78 => pal_abi.ERROR_UNSUPPORTED,
        89 => pal_abi.ERROR_CANCELED,
        else => pal_abi.ERROR_IO,
    };
}

fn findHandle(s: *PalState, token: u64) ?*HandleEntry {
    if (token < 3) return null;
    for (&s.handles) |*h| {
        if (h.used and h.token == token) return h;
    }
    return null;
}

fn allocHandle(s: *PalState) ?*HandleEntry {
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
    const gtv = s.p_gettimeofday orelse return 0;
    var tv: Timeval = .{ .sec = 0, .usec = 0 };
    _ = gtv(&tv, null);
    return @as(u64, @intCast(@max(tv.sec, 0))) * 1000 + @as(u64, @intCast(@divTrunc(@max(tv.usec, 0), 1000)));
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
    var tv: Timeval = .{ .sec = 0, .usec = 0 };
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

fn abiGetEnv(_: [*]u8, _: usize) callconv(pal_abi.pal_cc) u64 {
    return 0;
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

fn abiGetPlatformInfo(out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u32 {
    if (cap < @sizeOf(pal_abi.AbiPlatformInfo)) return pal_abi.ERROR_INVALID;
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
    copyFixed(&info.temp_dir, "/tmp");
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
            freeOp(s, op);
            completeInline(ov, pal_abi.ERROR_CANCELED, 0, 0);
            return;
        }
    }
}

fn abiRead(ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (handle == 0) {
        if (len == 0) {
            completeInline(ov, 0, 0, 0);
            return;
        }
        const op = allocOp(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .read_fd;
        op.guest_ov = ov;
        op.fd = 0;
        op.buf = buf;
        op.len = len;
        return;
    }
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const positional = (ov.flags & pal_abi.READ_FLAG_POSITIONAL) != 0;
    const n: isize = if (positional) blk: {
        const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
        const pr = s.p_pread orelse {
            completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        break :blk pr(he.fd, buf, len, trailing.*);
    } else blk: {
        const r = s.p_read orelse {
            completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        break :blk r(he.fd, buf, len);
    };
    if (n < 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, @intCast(n));
}

fn abiWrite(ov: *pal_abi.Overlapped, handle: u64, buf: [*]const u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const w = s.p_write orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const fd: i32 = if (handle == 1) 1 else if (handle == 2) 2 else blk: {
        const he = findHandle(s, handle) orelse {
            completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
            return;
        };
        break :blk he.fd;
    };
    const positional = (ov.flags & pal_abi.WRITE_FLAG_POSITIONAL) != 0;
    const n: isize = if (positional) blk: {
        const pwrite = s.p_pwrite orelse {
            completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        };
        const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
        if (trailing.* < 0) {
            completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        break :blk pwrite(fd, buf, len, trailing.*);
    } else w(fd, buf, len);
    if (n < 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, @intCast(n));
}

fn abiSeek(ov: *pal_abi.Overlapped, handle: u64, off: i64, whence: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const ls = s.p_lseek orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    const pos = ls(he.fd, off, @intCast(whence));
    if (pos < 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, @intCast(pos));
}

fn abiHandleTruncate(ov: *pal_abi.Overlapped, handle: u64, size: i64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const ft = s.p_ftruncate orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    if (ft(he.fd, size) != 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiHandleClose(handle: u64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse return;
    if (he.fd >= 0) {
        if (s.p_close) |c| _ = c(he.fd);
    }
    he.* = .{};
}

fn abiPathOpen(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const op = s.p_open orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    var oflags: c_int = switch (flags & 3) {
        0 => 0, // O_RDONLY
        1 => 1, // O_WRONLY
        else => 2, // O_RDWR
    };
    // Darwin flags: O_CREAT=0x200, O_TRUNC=0x400, O_EXCL=0x800, O_APPEND=0x008, O_DIRECTORY=0x100000
    if ((flags & pal_abi.OPEN_FLAG_CREATE) != 0) oflags |= 0x200;
    if ((flags & pal_abi.OPEN_FLAG_TRUNC) != 0) oflags |= 0x400;
    if ((flags & pal_abi.OPEN_FLAG_EXCL) != 0) oflags |= 0x800;
    if ((flags & pal_abi.OPEN_FLAG_APPEND) != 0) oflags |= 0x008;
    if ((flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0) oflags |= 0x100000;

    const fd = op(cpath, oflags, 0o666);
    if (fd < 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    const he = allocHandle(s) orelse {
        if (s.p_close) |c| _ = c(fd);
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    he.fd = fd;
    completeInline(ov, 0, 0, he.token);
}

fn abiDirRead(ov: *pal_abi.Overlapped, _: u64, _: [*]u8, _: usize) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiPathStat(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: u32, _: [*]u8, _: usize) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiPathLink(ov: *pal_abi.Overlapped, src: [*]const u8, s_len: usize, dst: [*]const u8, d_len: usize, _: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const lk = s.p_link orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var b1: [4096]u8 = undefined;
    var b2: [4096]u8 = undefined;
    const s1 = toCPath(src, s_len, &b1) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const s2 = toCPath(dst, d_len, &b2) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    completeInline(ov, if (lk(s1, s2) != 0) mapDarwinErrno(s) else 0, 0, 0);
}
fn abiPathSymlink(ov: *pal_abi.Overlapped, target: [*]const u8, t_len: usize, link: [*]const u8, l_len: usize, _: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const sl = s.p_symlink orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var b1: [4096]u8 = undefined;
    var b2: [4096]u8 = undefined;
    const s1 = toCPath(target, t_len, &b1) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const s2 = toCPath(link, l_len, &b2) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    completeInline(ov, if (sl(s1, s2) != 0) mapDarwinErrno(s) else 0, 0, 0);
}
fn abiPathReadlink(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const rl = s.p_readlink orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const n = rl(cpath, out, cap);
    if (n < 0) {
        completeInline(ov, mapDarwinErrno(s), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, @intCast(n));
}
fn abiPathChmod(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const cm = s.p_chmod orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    completeInline(ov, if (cm(cpath, mode) != 0) mapDarwinErrno(s) else 0, 0, 0);
}
fn abiPathSetTimes(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: i64, _: u32, _: i64, _: u32, _: u32) callconv(pal_abi.pal_cc) void {
    completeInline(ov, 0, 0, 0);
}
fn abiPathRemove(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const rc: c_int = if ((flags & (pal_abi.PATH_REMOVE_FLAG_DIR | 0x200)) != 0)
        (s.p_rmdir orelse {
            completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        })(cpath)
    else
        (s.p_unlink orelse {
            completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
            return;
        })(cpath);
    completeInline(ov, if (rc != 0) mapDarwinErrno(s) else 0, 0, 0);
}
fn abiPathMkdir(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const mk = s.p_mkdir orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    completeInline(ov, if (mk(cpath, mode) != 0) mapDarwinErrno(s) else 0, 0, 0);
}
fn abiPathRename(ov: *pal_abi.Overlapped, old_p: [*]const u8, o_len: usize, new_p: [*]const u8, n_len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const rn = s.p_rename orelse {
        completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
        return;
    };
    var b1: [4096]u8 = undefined;
    var b2: [4096]u8 = undefined;
    const s1 = toCPath(old_p, o_len, &b1) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const s2 = toCPath(new_p, n_len, &b2) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    completeInline(ov, if (rn(s1, s2) != 0) mapDarwinErrno(s) else 0, 0, 0);
}
fn abiNetOpen(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: u32, _: u32) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiNetAccept(ov: *pal_abi.Overlapped, _: u64) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiNetLookup(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: [*]u8, _: usize) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiNetCertVerify(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: [*]const u8, _: usize) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiProcessSpawn(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
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
fn abiProcessWait(ov: *pal_abi.Overlapped, _: u64) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiProcessSignal(_: u64, _: u32) callconv(pal_abi.pal_cc) void {}
fn abiSignalWait(ov: *pal_abi.Overlapped, _: u32) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}
fn abiTtySetMode(_: u64, _: u32) callconv(pal_abi.pal_cc) void {}
fn abiTtyGetSize(_: u64) callconv(pal_abi.pal_cc) u32 {
    return (80 << 16) | 24;
}
fn abiTtySetSize(_: u64, _: u32, _: u32) callconv(pal_abi.pal_cc) u32 {
    return 0;
}
fn abiFdIsatty(handle: u32) callconv(pal_abi.pal_cc) u32 {
    const s = statePtr();
    if (s.p_isatty) |it| return if (it(@intCast(handle)) != 0) 1 else 0;
    return 0;
}
fn abiSystemInfoWait(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
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
    s.p_getcwd = @ptrCast(@alignCast(sym(s, "getcwd") orelse return 12));
    s.p_chdir = @ptrCast(@alignCast(sym(s, "chdir") orelse return 12));
    s.p_pipe = @ptrCast(@alignCast(sym(s, "pipe") orelse return 12));
    s.p_poll = @ptrCast(@alignCast(sym(s, "poll") orelse return 12));
    s.p_arc4random_buf = @ptrCast(@alignCast(sym(s, "arc4random_buf") orelse return 12));
    s.p_gettimeofday = @ptrCast(@alignCast(sym(s, "gettimeofday") orelse return 12));
    s.p_isatty = @ptrCast(@alignCast(sym(s, "isatty") orelse return 12));
    s.p_exit = @ptrCast(@alignCast(sym(s, "exit") orelse return 12));
    s.p_errno = @ptrCast(@alignCast(sym(s, "__error") orelse return 12));

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

        var pfds: [32]PollFd = undefined;
        var op_idx: [32]usize = undefined;
        var pfd_count: usize = 0;
        const now = nowMs(s);
        var min_ms: c_int = -1;
        var fired = false;

        for (&s.ops, 0..) |*op, idx| {
            if (!op.used) continue;
            if (op.kind == .timer) {
                if (op.deadline_ms <= now) {
                    const gov = op.guest_ov.?;
                    freeOp(s, op);
                    completeInline(gov, 0, 0, 0);
                    fired = true;
                } else {
                    const rem: c_int = @intCast(@min(op.deadline_ms - now, 0x7fff_ffff));
                    if (min_ms < 0 or rem < min_ms) min_ms = rem;
                }
            } else if (op.kind == .read_fd and pfd_count < pfds.len) {
                pfds[pfd_count] = .{ .fd = op.fd, .events = 1, .revents = 0 };
                op_idx[pfd_count] = idx;
                pfd_count += 1;
            }
        }
        if (fired) continue;

        _ = s.p_poll.?(&pfds, @intCast(pfd_count), min_ms);
        for (0..pfd_count) |i| {
            if (pfds[i].revents != 0) {
                const op = &s.ops[op_idx[i]];
                if (!op.used) continue;
                const gov = op.guest_ov.?;
                const n = s.p_read.?(op.fd, op.buf.?, op.len);
                freeOp(s, op);
                if (n < 0) {
                    completeInline(gov, mapDarwinErrno(s), 0, 0);
                } else {
                    completeInline(gov, 0, 0, @intCast(n));
                }
            }
        }
    }
    return 0;
}

comptime {
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
        };
        @export(&Intrinsics._memcpy, .{ .name = "memcpy" });
        @export(&Intrinsics._memset, .{ .name = "memset" });
        @export(&Intrinsics._memmove, .{ .name = "memmove" });
        @export(&Intrinsics._memcmp, .{ .name = "memcmp" });
        @export(&Intrinsics._memcmp, .{ .name = "bcmp" });
        @export(&Intrinsics._strlen, .{ .name = "strlen" });
    }
}
