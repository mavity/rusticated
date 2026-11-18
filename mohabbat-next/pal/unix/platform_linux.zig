const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");
const sys = @import("linux_sys.zig");

const UnixPlatformHost = extern struct {
    hello_payload: [*]const u8,
    hello_payload_size: usize,
    argv: [*]const [*:0]const u8,
    argc: usize,
    envp: [*]const [*:0]const u8,
    envc: usize,
    pid: u32,
    wasm_bytes: ?[*]const u8,
    wasm_len: usize,
};

const MAX_HANDLES = 256;
const MAX_OPS = 256;

const HandleEntry = struct {
    used: bool = false,
    token: u64 = 0,
    fd: i32 = -1,
    is_dir: bool = false,
    is_socket: bool = false,
    is_udp: bool = false,
    is_process: bool = false,
    is_pty: bool = false,
    pty_read_fd: i32 = -1,
    pty_write_fd: i32 = -1,
};

const OpKind = enum(u8) {
    none = 0,
    read_fd = 1,
    timer = 2,
    accept = 3,
    wait_proc = 4,
    udp_read = 5,
};

const PendingOp = struct {
    used: bool = false,
    kind: OpKind = .none,
    guest_ov: ?*pal_abi.Overlapped = null,
    fd: i32 = -1,
    pid: i32 = -1,
    buf: ?[*]u8 = null,
    len: usize = 0,
    deadline_ns: u64 = 0,
};

const PalState = struct {
    host: UnixPlatformHost = undefined,
    next_token: u64 = 3,
    handles: [MAX_HANDLES]HandleEntry = @splat(.{}),
    ops: [MAX_OPS]PendingOp = @splat(.{}),
    pending_count: usize = 0,
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

fn pageAlign(n: usize) usize {
    return (n + 4095) & ~@as(usize, 4095);
}

fn completeInline(ov: *pal_abi.Overlapped, err: u32, continued: u64, result_ext: u64) void {
    ov.@"error" = err;
    ov.continued = continued;
    ov.result_ext = result_ext;
    @atomicStore(u32, &ov.flags, pal_abi.FLAG_COMPLETED, .release);
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

fn monotonicNs() u64 {
    var ts: sys.Timespec = .{ .sec = 0, .nsec = 0 };
    _ = sys.sys2(sys.NR.clock_gettime, 1, @intFromPtr(&ts)); // CLOCK_MONOTONIC = 1
    return @as(u64, @intCast(@max(ts.sec, 0))) * 1_000_000_000 + @as(u64, @intCast(@max(ts.nsec, 0)));
}

// ─── PalAbiTable Implementations ─────────────────────────────────────────────

fn abiAllocMem(size: usize, _: usize, flags: u32) callconv(pal_abi.pal_cc) ?[*]u8 {
    const prot: usize = if (flags == pal_abi.MEM_CODE_EXEC_RX)
        sys.PROT_READ | sys.PROT_EXEC
    else
        sys.PROT_READ | sys.PROT_WRITE;
    const res = sys.sys6(sys.NR.mmap, 0, pageAlign(@max(size, 1)), prot, sys.MAP_PRIVATE | sys.MAP_ANONYMOUS, @bitCast(@as(isize, -1)), 0);
    if (sys.isErr(res) != null) return null;
    return @ptrFromInt(res);
}

fn abiProtectMem(ptr: [*]u8, size: usize, flags: u32) callconv(pal_abi.pal_cc) i32 {
    const prot: usize = if (flags == pal_abi.MEM_CODE_EXEC_RX)
        sys.PROT_READ | sys.PROT_EXEC
    else
        sys.PROT_READ | sys.PROT_WRITE;
    const res = sys.sys3(sys.NR.mprotect, @intFromPtr(ptr), pageAlign(@max(size, 1)), prot);
    return if (sys.isErr(res) != null) -1 else 0;
}

fn abiFreeMem(ptr: [*]u8, size: usize) callconv(pal_abi.pal_cc) void {
    _ = sys.sys2(sys.NR.munmap, @intFromPtr(ptr), pageAlign(@max(size, 1)));
}

fn abiFlushIcache(ptr: [*]const u8, size: usize) callconv(pal_abi.pal_cc) void {
    if (builtin.cpu.arch == .aarch64) {
        const start = @intFromPtr(ptr) & ~@as(usize, 63);
        const end = (@intFromPtr(ptr) + size + 63) & ~@as(usize, 63);
        var addr = start;
        while (addr < end) : (addr += 64) {
            asm volatile ("dc cvau, %[addr]"
                :
                : [addr] "r" (addr),
                : .{ .memory = true });
        }
        asm volatile ("dsb ish" ::: .{ .memory = true });
        addr = start;
        while (addr < end) : (addr += 64) {
            asm volatile ("ic ivau, %[addr]"
                :
                : [addr] "r" (addr),
                : .{ .memory = true });
        }
        asm volatile ("dsb ish\nisb" ::: .{ .memory = true });
    }
}

fn abiHostPanic(msg: [*]const u8, len: usize) callconv(pal_abi.pal_cc) noreturn {
    _ = sys.sys3(sys.NR.write, 2, @intFromPtr(msg), len);
    _ = sys.sys1(sys.NR.exit_group, 99);
    while (true) {}
}

fn abiRusticatedDebug(_: i32) callconv(pal_abi.pal_cc) void {}

fn abiProcessExit(code: i32) callconv(pal_abi.pal_cc) noreturn {
    _ = sys.sys1(sys.NR.exit_group, @as(u32, @bitCast(code)) & 0xff);
    while (true) {}
}

fn abiGetTime() callconv(pal_abi.pal_cc) u64 {
    var ts: sys.Timespec = .{ .sec = 0, .nsec = 0 };
    _ = sys.sys2(sys.NR.clock_gettime, 0, @intFromPtr(&ts)); // CLOCK_REALTIME = 0
    return @as(u64, @intCast(@max(ts.sec, 0))) * 1_000_000_000 + @as(u64, @intCast(@max(ts.nsec, 0)));
}

fn abiGetRandom(buf: [*]u8, len: usize) callconv(pal_abi.pal_cc) void {
    var off: usize = 0;
    while (off < len) {
        const res = sys.sys3(sys.NR.getrandom, @intFromPtr(buf + off), len - off, 0);
        if (sys.isErr(res) != null) break;
        if (res == 0) break;
        off += res;
    }
}

fn packCStrList(items: [*]const [*:0]const u8, count: usize, buf: [*]u8, cap: usize) u64 {
    var needed: usize = 0;
    for (0..count) |i| {
        const s = std.mem.sliceTo(items[i], 0);
        needed += s.len + 1;
    }
    if (needed > cap) return (@as(u64, @intCast(count)) << 32) | @as(u64, @intCast(needed));
    var off: usize = 0;
    for (0..count) |i| {
        const s = std.mem.sliceTo(items[i], 0);
        @memcpy(buf[off..][0..s.len], s);
        buf[off + s.len] = 0;
        off += s.len + 1;
    }
    return (@as(u64, @intCast(count)) << 32) | @as(u64, @intCast(off));
}

fn abiGetArgs(buf: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u64 {
    const s = statePtr();
    return packCStrList(s.host.argv, s.host.argc, buf, cap);
}

fn abiGetEnv(buf: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u64 {
    const s = statePtr();
    return packCStrList(s.host.envp, s.host.envc, buf, cap);
}

fn abiGetCwd(buf: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u64 {
    var tmp: [4096]u8 = undefined;
    const res = sys.sys2(sys.NR.getcwd, @intFromPtr(&tmp), tmp.len);
    if (sys.isErr(res) != null or res == 0) return 0;
    const n = std.mem.sliceTo(@as([*:0]const u8, @ptrCast(&tmp)), 0).len;
    if (n > cap) return @as(u64, @intCast(n));
    @memcpy(buf[0..n], tmp[0..n]);
    return @as(u64, @intCast(n));
}

fn abiSetCwd(path: [*]const u8, len: usize) callconv(pal_abi.pal_cc) u32 {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse return pal_abi.ERROR_INVALID;
    const res = sys.sys1(sys.NR.chdir, @intFromPtr(cpath));
    if (sys.isErr(res)) |e| return sys.mapLinuxErrno(e);
    return 0;
}

fn copyFixed(dst: []u8, src: []const u8) void {
    const n = @min(dst.len, src.len);
    @memcpy(dst[0..n], src[0..n]);
}

fn abiGetPlatformInfo(_: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u32 {
    if (cap < @sizeOf(pal_abi.AbiPlatformInfo)) return pal_abi.ERROR_INVALID;
    return 0;
}

fn abiGetPlatformInfoFull(out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) u32 {
    if (cap < @sizeOf(pal_abi.AbiPlatformInfo)) return pal_abi.ERROR_INVALID;
    var info: pal_abi.AbiPlatformInfo = .{};
    info.path_separator = '/';
    info.path_list_separator = ':';
    info.os_kind = pal_abi.OS_KIND_LINUX;
    info.os_version = .{ 6, 6, 0, 0 };
    info.cpu_type = switch (builtin.cpu.arch) {
        .x86_64 => pal_abi.CPU_TYPE_X86_64,
        .aarch64 => pal_abi.CPU_TYPE_AARCH64,
        else => 0,
    };
    info.cpu_bitness = 64;
    copyFixed(&info.os_name, "linux");
    copyFixed(&info.wasm_platform, "mohabbat-next-zig");
    copyFixed(&info.rusticated_name, "rusticated");
    copyFixed(&info.build_platform, if (builtin.cpu.arch == .x86_64) "linux-amd64" else "linux-arm64");
    copyFixed(&info.temp_dir, "/tmp");
    const s = statePtr();
    for (0..s.host.envc) |i| {
        const entry = std.mem.sliceTo(s.host.envp[i], 0);
        if (std.mem.startsWith(u8, entry, "HOME=")) {
            copyFixed(&info.user_home_dir, entry[5..]);
        } else if (std.mem.startsWith(u8, entry, "TMPDIR=")) {
            copyFixed(&info.temp_dir, entry[7..]);
        }
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
    op.deadline_ns = monotonicNs() + @as(u64, delay_ms) * 1_000_000;
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
    if (he.is_pty) {
        const res = sys.sys3(sys.NR.read, @intCast(he.pty_read_fd), @intFromPtr(buf), len);
        if (sys.isErr(res)) |e| {
            completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
            return;
        }
        completeInline(ov, 0, 0, res);
        return;
    }
    if (he.is_udp) {
        const op = allocOp(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .udp_read;
        op.guest_ov = ov;
        op.fd = he.fd;
        op.buf = buf;
        op.len = len;
        return;
    }
    const positional = (ov.flags & pal_abi.READ_FLAG_POSITIONAL) != 0;
    const res = if (positional) blk: {
        const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
        const off = trailing.*;
        if (off < 0) {
            completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        break :blk sys.sys4(sys.NR.pread64, @intCast(he.fd), @intFromPtr(buf), len, @bitCast(off));
    } else sys.sys3(sys.NR.read, @intCast(he.fd), @intFromPtr(buf), len);

    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, res);
}

fn abiWrite(ov: *pal_abi.Overlapped, handle: u64, buf: [*]const u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    if (handle >= 3) {
        if (findHandle(s, handle)) |he| {
            if (he.is_pty) {
                const res = sys.sys3(sys.NR.write, @intCast(he.pty_write_fd), @intFromPtr(buf), len);
                if (sys.isErr(res)) |e| {
                    completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
                    return;
                }
                completeInline(ov, 0, 0, res);
                return;
            }
        }
    }
    if (handle >= 3) {
        if (findHandle(s, handle)) |he| {
            if (he.is_udp) {
                const res = sys.sys6(sys.NR.sendto, @intCast(he.fd), @intFromPtr(buf), len, 0, 0, 0);
                if (sys.isErr(res)) |e| {
                    completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
                    return;
                }
                completeInline(ov, 0, 0, res);
                return;
            }
        }
    }
    const fd: i32 = if (handle == 1) 1 else if (handle == 2) 2 else blk: {
        const he = findHandle(s, handle) orelse {
            completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
            return;
        };
        break :blk he.fd;
    };
    const positional = (ov.flags & pal_abi.WRITE_FLAG_POSITIONAL) != 0;
    const res = if (positional) blk: {
        if (len == 0) {
            completeInline(ov, 0, 0, 0);
            return;
        }
        const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
        const off = trailing.*;
        if (off < 0) {
            completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        break :blk sys.sys4(sys.NR.pwrite64, @intCast(fd), @intFromPtr(buf), len, @bitCast(off));
    } else sys.sys3(sys.NR.write, @intCast(fd), @intFromPtr(buf), len);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, res);
}

fn abiSeek(ov: *pal_abi.Overlapped, handle: u64, off: i64, whence: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (whence > 2) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const res = sys.sys3(sys.NR.lseek, @intCast(he.fd), @bitCast(off), whence);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, res);
}

fn abiHandleTruncate(ov: *pal_abi.Overlapped, handle: u64, size: i64) callconv(pal_abi.pal_cc) void {
    if (size < 0) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    const res = sys.sys2(sys.NR.ftruncate, @intCast(he.fd), @bitCast(size));
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiHandleClose(handle: u64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse return;
    if (he.is_pty) {
        if (he.pty_read_fd >= 0) _ = sys.sys1(sys.NR.close, @intCast(he.pty_read_fd));
        if (he.pty_write_fd >= 0) _ = sys.sys1(sys.NR.close, @intCast(he.pty_write_fd));
        he.* = .{};
        return;
    }
    if (he.fd >= 0) _ = sys.sys1(sys.NR.close, @intCast(he.fd));
    he.* = .{};
}

fn abiPathOpen(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    var oflags: u32 = switch (flags & 3) {
        0 => sys.O_RDONLY,
        1 => sys.O_WRONLY,
        else => sys.O_RDWR,
    };
    if ((flags & pal_abi.OPEN_FLAG_CREATE) != 0) oflags |= sys.O_CREAT;
    if ((flags & pal_abi.OPEN_FLAG_EXCL) != 0) oflags |= sys.O_EXCL;
    if ((flags & pal_abi.OPEN_FLAG_TRUNC) != 0) oflags |= sys.O_TRUNC;
    if ((flags & pal_abi.OPEN_FLAG_APPEND) != 0) oflags |= sys.O_APPEND;
    if ((flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0) oflags |= sys.O_DIRECTORY;

    const res = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), oflags, 0o666);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    const he = allocHandle(s) orelse {
        _ = sys.sys1(sys.NR.close, res);
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    he.fd = @intCast(res);
    he.is_dir = (flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0;
    completeInline(ov, 0, 0, he.token);
}

fn abiDirRead(ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    var raw: [4096]u8 = undefined;
    const res = sys.sys3(sys.NR.getdents64, @intCast(he.fd), @intFromPtr(&raw), raw.len);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    if (res == 0) {
        completeInline(ov, 0, 0, 0);
        return;
    }
    var pos: usize = 0;
    var out_pos: usize = 0;
    while (pos + 19 <= res) {
        const reclen = std.mem.readInt(u16, raw[pos + 16 ..][0..2], .little);
        if (reclen < 19 or pos + reclen > res) break;
        const d_type = raw[pos + 18];
        const name = std.mem.sliceTo(@as([*:0]const u8, @ptrCast(&raw[pos + 19])), 0);
        if (!std.mem.eql(u8, name, ".") and !std.mem.eql(u8, name, "..")) {
            const rec_len = 1 + name.len + 1;
            if (out_pos + rec_len > len) break;
            buf[out_pos] = switch (d_type) {
                4 => @intCast(pal_abi.STAT_KIND_DIR),
                10 => @intCast(pal_abi.STAT_KIND_SYMLINK),
                8 => @intCast(pal_abi.STAT_KIND_FILE),
                else => @intCast(pal_abi.STAT_KIND_UNKNOWN),
            };
            @memcpy(buf[out_pos + 1 ..][0..name.len], name);
            buf[out_pos + 1 + name.len] = 0;
            out_pos += rec_len;
        }
        pos += reclen;
    }
    completeInline(ov, 0, 0, out_pos);
}

fn abiPathStat(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    if (cap < @sizeOf(pal_abi.AbiStat)) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    var lst: sys.LinuxStat = undefined;
    const at_flags: u32 = if ((flags & pal_abi.PATH_STAT_FLAG_NOFOLLOW) != 0) sys.AT_SYMLINK_NOFOLLOW else 0;
    const res = sys.sys4(sys.NR.newfstatat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), @intFromPtr(&lst), at_flags);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    var st: pal_abi.AbiStat = .{};
    const ftype = lst.mode & 0o170000;
    st.kind = switch (ftype) {
        0o100000 => pal_abi.STAT_KIND_FILE,
        0o040000 => pal_abi.STAT_KIND_DIR,
        0o120000 => pal_abi.STAT_KIND_SYMLINK,
        0o020000 => pal_abi.STAT_KIND_CHAR,
        0o010000 => pal_abi.STAT_KIND_FIFO,
        0o140000 => pal_abi.STAT_KIND_SOCKET,
        else => pal_abi.STAT_KIND_UNKNOWN,
    };
    st.mode = lst.mode & 0o7777;
    st.uid = lst.uid;
    st.gid = lst.gid;
    st.size = @intCast(@max(lst.size, 0));
    st.mtime_ns = lst.mtime_sec * 1_000_000_000 + lst.mtime_nsec;
    st.atime_ns = lst.atime_sec * 1_000_000_000 + lst.atime_nsec;
    st.ctime_ns = lst.ctime_sec * 1_000_000_000 + lst.ctime_nsec;
    st.nlink = lst.nlink;
    st.ino = lst.ino;
    st.dev = lst.dev;
    st.rdev = lst.rdev;
    st.blksize = lst.blksize;
    st.blocks = lst.blocks;
    st.valid_mask = 0x1f;
    const bytes = std.mem.asBytes(&st);
    @memcpy(out[0..bytes.len], bytes);
    completeInline(ov, 0, 0, 0);
}

fn abiPathLink(ov: *pal_abi.Overlapped, src: [*]const u8, s_len: usize, dst: [*]const u8, d_len: usize, _: u32) callconv(pal_abi.pal_cc) void {
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
    const cwd: usize = @bitCast(@as(isize, sys.AT_FDCWD));
    const res = sys.sys6(sys.NR.linkat, cwd, @intFromPtr(s1), cwd, @intFromPtr(s2), 0, 0);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiPathSymlink(ov: *pal_abi.Overlapped, target: [*]const u8, t_len: usize, link: [*]const u8, l_len: usize, _: u32) callconv(pal_abi.pal_cc) void {
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
    const res = sys.sys3(sys.NR.symlinkat, @intFromPtr(s1), @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(s2));
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiPathReadlink(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const res = sys.sys4(sys.NR.readlinkat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), @intFromPtr(out), cap);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, res);
}

fn abiPathChmod(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const res = sys.sys4(sys.NR.fchmodat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), mode, 0);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiPathSetTimes(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, at_sec: i64, at_nsec: u32, mt_sec: i64, mt_nsec: u32, flags: u32) callconv(pal_abi.pal_cc) void {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    var times = [2]sys.Timespec{
        .{ .sec = at_sec, .nsec = at_nsec },
        .{ .sec = mt_sec, .nsec = mt_nsec },
    };
    const at_flags: u32 = if ((flags & pal_abi.PATH_SET_TIMES_FLAG_NOFOLLOW) != 0) sys.AT_SYMLINK_NOFOLLOW else 0;
    const res = sys.sys4(sys.NR.utimensat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), @intFromPtr(&times), at_flags);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiPathRemove(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const at_flags: u32 = if ((flags & (pal_abi.PATH_REMOVE_FLAG_DIR | 0x200)) != 0) sys.AT_REMOVEDIR else 0;
    const res = sys.sys3(sys.NR.unlinkat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), at_flags);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiPathMkdir(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(path, len, &cbuf) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const res = sys.sys3(sys.NR.mkdirat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), mode);
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn abiPathRename(ov: *pal_abi.Overlapped, old_p: [*]const u8, o_len: usize, new_p: [*]const u8, n_len: usize) callconv(pal_abi.pal_cc) void {
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
    const cwd: usize = @bitCast(@as(isize, sys.AT_FDCWD));
    const res = sys.sys4(sys.NR.renameat, cwd, @intFromPtr(s1), cwd, @intFromPtr(s2));
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    completeInline(ov, 0, 0, 0);
}

fn parseIpv4(addr: []const u8) ?[4]u8 {
    var out: [4]u8 = undefined;
    var part: usize = 0;
    var val: u32 = 0;
    var has_digit = false;
    for (addr) |b| {
        if (b == '.') {
            if (!has_digit or part >= 3 or val > 255) return null;
            out[part] = @intCast(val);
            part += 1;
            val = 0;
            has_digit = false;
        } else if (b >= '0' and b <= '9') {
            val = val * 10 + (b - '0');
            if (val > 255) return null;
            has_digit = true;
        } else {
            return null;
        }
    }
    if (!has_digit or part != 3) return null;
    out[3] = @intCast(val);
    return out;
}

fn abiNetOpen(ov: *pal_abi.Overlapped, addr_ptr: [*]const u8, addr_len: usize, port: u32, flags: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const is_udp = (flags & (1 << 2)) != 0;
    const sock_res = if (is_udp)
        sys.sys3(sys.NR.socket, 2, 2 | 0o2000000, 17) // AF_INET, SOCK_DGRAM|SOCK_CLOEXEC, IPPROTO_UDP
    else
        sys.sys3(sys.NR.socket, 2, 1 | 0o2000000, 6); // AF_INET, SOCK_STREAM|SOCK_CLOEXEC, IPPROTO_TCP
    if (sys.isErr(sock_res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    const fd: i32 = @intCast(sock_res);

    const ip = if (addr_len == 0) [4]u8{ 0, 0, 0, 0 } else (parseIpv4(addr_ptr[0..addr_len]) orelse {
        _ = sys.sys1(sys.NR.close, @intCast(fd));
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    });
    var sa: sys.SockAddrIn = .{
        .family = 2,
        .port_be = std.mem.nativeToBig(u16, @intCast(port & 0xffff)),
        .addr = ip,
    };

    const listen_mode = (flags & 1) == 0;
    if (listen_mode) {
        const one: i32 = 1;
        _ = sys.sys6(sys.NR.setsockopt, @intCast(fd), 1, 2, @intFromPtr(&one), @sizeOf(i32), 0); // SOL_SOCKET=1, SO_REUSEADDR=2
        const bind_res = sys.sys3(sys.NR.bind, @intCast(fd), @intFromPtr(&sa), @sizeOf(sys.SockAddrIn));
        if (sys.isErr(bind_res)) |e| {
            _ = sys.sys1(sys.NR.close, @intCast(fd));
            completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
            return;
        }
        if (!is_udp) {
            const listen_res = sys.sys2(sys.NR.listen, @intCast(fd), 128);
            if (sys.isErr(listen_res)) |e| {
                _ = sys.sys1(sys.NR.close, @intCast(fd));
                completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
                return;
            }
        }
        var eff_port: u64 = port;
        var slen: u32 = @sizeOf(sys.SockAddrIn);
        if (sys.isErr(sys.sys3(sys.NR.getsockname, @intCast(fd), @intFromPtr(&sa), @intFromPtr(&slen))) == null) {
            eff_port = std.mem.bigToNative(u16, sa.port_be);
        }
        // Set O_NONBLOCK for accept polling
        const fl = sys.sys3(sys.NR.fcntl, @intCast(fd), 3, 0);
        if (sys.isErr(fl) == null) {
            _ = sys.sys3(sys.NR.fcntl, @intCast(fd), 4, fl | sys.O_NONBLOCK);
        }
        const he = allocHandle(s) orelse {
            _ = sys.sys1(sys.NR.close, @intCast(fd));
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        he.fd = fd;
        he.is_socket = true;
        he.is_udp = is_udp;
        completeInline(ov, 0, eff_port, he.token);
        return;
    }

    const conn_res = sys.sys3(sys.NR.connect, @intCast(fd), @intFromPtr(&sa), @sizeOf(sys.SockAddrIn));
    if (sys.isErr(conn_res)) |e| {
        _ = sys.sys1(sys.NR.close, @intCast(fd));
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    const fl = sys.sys3(sys.NR.fcntl, @intCast(fd), 3, 0);
    if (sys.isErr(fl) == null) {
        _ = sys.sys3(sys.NR.fcntl, @intCast(fd), 4, fl | sys.O_NONBLOCK);
    }
    const he = allocHandle(s) orelse {
        _ = sys.sys1(sys.NR.close, @intCast(fd));
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    he.fd = fd;
    he.is_socket = true;
    he.is_udp = is_udp;
    completeInline(ov, 0, port, he.token);
}

fn abiNetAccept(ov: *pal_abi.Overlapped, listener: u64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, listener) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_socket) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const res = sys.sys3(sys.NR.accept, @intCast(he.fd), 0, 0);
    if (sys.isErr(res)) |e| {
        if (e == 11) { // EAGAIN
            const op = allocOp(s) orelse {
                completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
                return;
            };
            op.kind = .accept;
            op.guest_ov = ov;
            op.fd = he.fd;
            return;
        }
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    const cli_fd: i32 = @intCast(res);
    const fl = sys.sys3(sys.NR.fcntl, @intCast(cli_fd), 3, 0);
    if (sys.isErr(fl) == null) {
        _ = sys.sys3(sys.NR.fcntl, @intCast(cli_fd), 4, fl | sys.O_NONBLOCK);
    }
    const cli_he = allocHandle(s) orelse {
        _ = sys.sys1(sys.NR.close, @intCast(cli_fd));
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    cli_he.fd = cli_fd;
    cli_he.is_socket = true;
    completeInline(ov, 0, 0, cli_he.token);
}

fn writeDnsQname(out: []u8, name: []const u8) ?usize {
    var pos: usize = 0;
    var start: usize = 0;
    for (name, 0..) |b, i| {
        if (b == '.') {
            const seg = i - start;
            if (seg == 0 or seg > 63 or pos + 1 + seg >= out.len) return null;
            out[pos] = @intCast(seg);
            pos += 1;
            @memcpy(out[pos..][0..seg], name[start..i]);
            pos += seg;
            start = i + 1;
        }
    }
    if (start < name.len) {
        const seg = name.len - start;
        if (seg > 63 or pos + 1 + seg >= out.len) return null;
        out[pos] = @intCast(seg);
        pos += 1;
        @memcpy(out[pos..][0..seg], name[start..]);
        pos += seg;
    }
    if (pos >= out.len) return null;
    out[pos] = 0;
    return pos + 1;
}

fn skipDnsName(pkt: []const u8, start: usize) ?usize {
    var pos = start;
    while (pos < pkt.len) {
        const b = pkt[pos];
        if (b == 0) return pos + 1;
        if ((b & 0xc0) == 0xc0) {
            if (pos + 1 >= pkt.len) return null;
            return pos + 2;
        }
        pos += 1 + @as(usize, b);
    }
    return null;
}

fn readResolvNameserver() [4]u8 {
    const path = "/etc/resolv.conf";
    const fd_res = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(path.ptr), sys.O_RDONLY, 0);
    if (sys.isErr(fd_res) != null) return .{ 127, 0, 0, 53 };
    const fd: usize = fd_res;
    defer _ = sys.sys1(sys.NR.close, fd);
    var buf: [2048]u8 = undefined;
    const n_res = sys.sys3(sys.NR.read, fd, @intFromPtr(&buf), buf.len);
    if (sys.isErr(n_res) != null) return .{ 127, 0, 0, 53 };
    const text = buf[0..n_res];
    var it = std.mem.splitScalar(u8, text, '\n');
    while (it.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (std.mem.startsWith(u8, line, "nameserver")) {
            const rest = std.mem.trim(u8, line["nameserver".len..], " \t");
            if (parseIpv4(rest)) |ip| return ip;
        }
    }
    return .{ 127, 0, 0, 53 };
}

fn abiNetLookup(ov: *pal_abi.Overlapped, name_ptr: [*]const u8, name_len: usize, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    if (name_len == 0 or name_len > 255) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const name = name_ptr[0..name_len];
    if (std.mem.eql(u8, name, "localhost")) {
        if (cap < 5) {
            completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        out[0] = 4;
        out[1] = 127;
        out[2] = 0;
        out[3] = 0;
        out[4] = 1;
        completeInline(ov, 0, 0, 5);
        return;
    }
    if (parseIpv4(name)) |ip| {
        if (cap < 5) {
            completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        out[0] = 4;
        @memcpy(out[1..5], &ip);
        completeInline(ov, 0, 0, 5);
        return;
    }

    var pkt: [512]u8 = @splat(0);
    pkt[0] = 0x12;
    pkt[1] = 0x34;
    pkt[2] = 0x01; // RD=1
    pkt[5] = 0x01; // QDCOUNT=1
    const qend = writeDnsQname(pkt[12..], name) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    const qoff = 12 + qend;
    if (qoff + 4 > pkt.len) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    pkt[qoff] = 0;
    pkt[qoff + 1] = 1; // QTYPE = A
    pkt[qoff + 2] = 0;
    pkt[qoff + 3] = 1; // QCLASS = IN
    const pkt_len = qoff + 4;

    const sock_res = sys.sys3(sys.NR.socket, 2, 2 | 0o2000000, 17); // AF_INET, SOCK_DGRAM|SOCK_CLOEXEC, IPPROTO_UDP
    if (sys.isErr(sock_res) != null) {
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    const fd: usize = sock_res;
    defer _ = sys.sys1(sys.NR.close, fd);

    var sa: sys.SockAddrIn = .{
        .family = 2,
        .port_be = std.mem.nativeToBig(u16, 53),
        .addr = readResolvNameserver(),
    };
    if (sys.isErr(sys.sys3(sys.NR.connect, fd, @intFromPtr(&sa), @sizeOf(sys.SockAddrIn))) != null) {
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    if (sys.isErr(sys.sys3(sys.NR.write, fd, @intFromPtr(&pkt), pkt_len)) != null) {
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    var pfd: sys.PollFd = .{ .fd = @intCast(fd), .events = sys.POLLIN, .revents = 0 };
    var ts: sys.Timespec = .{ .sec = 2, .nsec = 0 };
    const pres = sys.sys4(sys.NR.ppoll, @intFromPtr(&pfd), 1, @intFromPtr(&ts), 0);
    if (sys.isErr(pres) != null or pres == 0) {
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    var resp: [512]u8 = undefined;
    const rlen_res = sys.sys3(sys.NR.read, fd, @intFromPtr(&resp), resp.len);
    if (sys.isErr(rlen_res) != null or rlen_res < 12) {
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    const r = resp[0..rlen_res];
    const qdcount = std.mem.readInt(u16, r[4..6], .big);
    const ancount = std.mem.readInt(u16, r[6..8], .big);
    var pos: usize = 12;
    for (0..qdcount) |_| {
        pos = skipDnsName(r, pos) orelse {
            completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
            return;
        };
        if (pos + 4 > r.len) break;
        pos += 4;
    }
    var written: usize = 0;
    for (0..ancount) |_| {
        pos = skipDnsName(r, pos) orelse break;
        if (pos + 10 > r.len) break;
        const rtype = std.mem.readInt(u16, r[pos..][0..2], .big);
        const rdlen = std.mem.readInt(u16, r[pos + 8 ..][0..2], .big);
        pos += 10;
        if (pos + rdlen > r.len) break;
        if (rtype == 1 and rdlen == 4 and written + 5 <= cap) {
            out[written] = 4;
            @memcpy(out[written + 1 ..][0..4], r[pos..][0..4]);
            written += 5;
        }
        pos += rdlen;
    }
    if (written == 0) {
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    completeInline(ov, 0, 0, written);
}
fn abiNetCertVerify(ov: *pal_abi.Overlapped, _: [*]const u8, _: usize, _: [*]const u8, _: usize) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
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

fn abiProcessSpawn(ov: *pal_abi.Overlapped, cfg_ptr: [*]const u8, cfg_len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const cfg = cfg_ptr[0..cfg_len];
    var pos: usize = 0;

    const prog = nextNullStr(cfg, &pos) orelse {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    };
    if (prog.len == 0 or prog.len >= 4095) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }

    var arg_storage: [4096]u8 = undefined;
    var argv_ptrs: [64]?[*:0]const u8 = @splat(null);
    var arg_off: usize = 0;
    var argc: usize = 0;

    @memcpy(arg_storage[0..prog.len], prog);
    arg_storage[prog.len] = 0;
    argv_ptrs[0] = @ptrCast(&arg_storage[0]);
    arg_off = prog.len + 1;
    argc = 1;

    while (nextNullStr(cfg, &pos)) |arg| {
        if (arg.len == 0) break;
        if (argc < 63 and arg_off + arg.len + 1 <= arg_storage.len) {
            @memcpy(arg_storage[arg_off..][0..arg.len], arg);
            arg_storage[arg_off + arg.len] = 0;
            argv_ptrs[argc] = @ptrCast(&arg_storage[arg_off]);
            arg_off += arg.len + 1;
            argc += 1;
        }
    }
    argv_ptrs[argc] = null;

    while (nextNullStr(cfg, &pos)) |env_item| {
        if (env_item.len == 0) break;
    }

    var cwd_buf: [4096]u8 = undefined;
    var has_cwd = false;
    if (nextNullStr(cfg, &pos)) |c| {
        if (c.len > 0 and c.len < cwd_buf.len) {
            @memcpy(cwd_buf[0..c.len], c);
            cwd_buf[c.len] = 0;
            has_cwd = true;
        }
    }

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

    var in_fds: [2]i32 = .{ -1, -1 };
    var out_fds: [2]i32 = .{ -1, -1 };
    var err_fds: [2]i32 = .{ -1, -1 };
    var pty_in_fds: [2]i32 = .{ -1, -1 };
    var pty_out_fds: [2]i32 = .{ -1, -1 };
    var pty_enabled = false;
    var child_fds: [3]i32 = .{ 0, 1, 2 };

    if (has_stdio) {
        for (0..3) |idx| {
            const spec = stdio_specs[idx];
            if (std.mem.eql(u8, spec, "null")) {
                const null_p = "/dev/null";
                const flags: u32 = if (idx == 0) sys.O_RDONLY else sys.O_WRONLY;
                const nfd = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(null_p.ptr), flags, 0);
                if (sys.isErr(nfd) == null) child_fds[idx] = @intCast(nfd);
            } else if (std.mem.startsWith(u8, spec, "pty:")) {
                pty_enabled = true;
                if (sys.isErr(sys.sys2(sys.NR.pipe2, @intFromPtr(&pty_in_fds), 0o2000000)) == null and
                    sys.isErr(sys.sys2(sys.NR.pipe2, @intFromPtr(&pty_out_fds), 0o2000000)) == null)
                {
                    child_fds[0] = pty_in_fds[0];
                    child_fds[1] = pty_out_fds[1];
                    child_fds[2] = pty_out_fds[1];
                }
            } else if (std.mem.startsWith(u8, spec, "fd:")) {
                const tok = std.fmt.parseInt(u64, spec[3..], 10) catch 0;
                if (tok <= 2) {
                    child_fds[idx] = @intCast(tok);
                } else if (findHandle(s, tok)) |fhe| {
                    child_fds[idx] = fhe.fd;
                }
            }
        }
    } else {
        if (sys.isErr(sys.sys2(sys.NR.pipe2, @intFromPtr(&in_fds), 0)) != null or
            sys.isErr(sys.sys2(sys.NR.pipe2, @intFromPtr(&out_fds), 0)) != null or
            sys.isErr(sys.sys2(sys.NR.pipe2, @intFromPtr(&err_fds), 0)) != null)
        {
            completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        }
        child_fds = .{ in_fds[0], out_fds[1], err_fds[1] };
    }

    const pid_res = switch (builtin.cpu.arch) {
        .x86_64 => sys.sys1(sys.NR.fork, 0),
        .aarch64 => sys.sys2(sys.NR.clone, 17, 0), // SIGCHLD
        else => @compileError("Unsupported arch"),
    };
    if (sys.isErr(pid_res)) |e| {
        if (!has_stdio) {
            _ = sys.sys1(sys.NR.close, @intCast(in_fds[0]));
            _ = sys.sys1(sys.NR.close, @intCast(in_fds[1]));
            _ = sys.sys1(sys.NR.close, @intCast(out_fds[0]));
            _ = sys.sys1(sys.NR.close, @intCast(out_fds[1]));
            _ = sys.sys1(sys.NR.close, @intCast(err_fds[0]));
            _ = sys.sys1(sys.NR.close, @intCast(err_fds[1]));
        }
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }

    const pid: i32 = @intCast(pid_res);
    if (pid == 0) {
        // Child
        if (!has_stdio) {
            _ = sys.sys1(sys.NR.close, @intCast(in_fds[1]));
            _ = sys.sys1(sys.NR.close, @intCast(out_fds[0]));
            _ = sys.sys1(sys.NR.close, @intCast(err_fds[0]));
        } else if (pty_enabled) {
            _ = sys.sys1(sys.NR.close, @intCast(pty_in_fds[1]));
            _ = sys.sys1(sys.NR.close, @intCast(pty_out_fds[0]));
        }
        for (0..3) |idx| {
            const target: i32 = @intCast(idx);
            if (child_fds[idx] != target) {
                _ = sys.sys3(sys.NR.dup3, @intCast(child_fds[idx]), @intCast(target), 0);
            }
        }
        if (has_cwd) {
            _ = sys.sys1(sys.NR.chdir, @intFromPtr(&cwd_buf));
        }
        const envp = s.host.envp;
        _ = sys.sys3(sys.NR.execve, @intFromPtr(argv_ptrs[0].?), @intFromPtr(&argv_ptrs), @intFromPtr(envp));
        if (std.mem.indexOfScalar(u8, prog, '/') == null) {
            var fallback: [4096]u8 = undefined;
            const bin = "/bin/";
            if (prog.len + bin.len < fallback.len) {
                @memcpy(fallback[0..bin.len], bin);
                @memcpy(fallback[bin.len..][0..prog.len], prog);
                fallback[bin.len + prog.len] = 0;
                argv_ptrs[0] = @ptrCast(&fallback[0]);
                _ = sys.sys3(sys.NR.execve, @intFromPtr(argv_ptrs[0].?), @intFromPtr(&argv_ptrs), @intFromPtr(envp));
            }
        }
        _ = sys.sys1(sys.NR.exit_group, 127);
        unreachable;
    }

    // Parent
    if (!has_stdio) {
        _ = sys.sys1(sys.NR.close, @intCast(in_fds[0]));
        _ = sys.sys1(sys.NR.close, @intCast(out_fds[1]));
        _ = sys.sys1(sys.NR.close, @intCast(err_fds[1]));
    } else if (pty_enabled) {
        _ = sys.sys1(sys.NR.close, @intCast(pty_in_fds[0]));
        _ = sys.sys1(sys.NR.close, @intCast(pty_out_fds[1]));
    }

    const proc_he = allocHandle(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    proc_he.fd = pid;
    proc_he.is_process = true;

    const pso: *pal_abi.ProcessSpawnOverlapped = @ptrCast(@alignCast(ov));
    pso.child_handle = proc_he.token;
    pso.pty_handle = 0;
    if (pty_enabled) {
        const pty_he = allocHandle(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        pty_he.fd = pty_out_fds[0];
        pty_he.is_pty = true;
        pty_he.pty_read_fd = pty_out_fds[0];
        pty_he.pty_write_fd = pty_in_fds[1];
        pso.pty_handle = pty_he.token;
        pso.in_handle = 0;
        pso.out_handle = pty_he.token;
        pso.err_handle = 0;
    } else if (!has_stdio) {
        const in_he = allocHandle(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        in_he.fd = in_fds[1];
        const out_he = allocHandle(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        out_he.fd = out_fds[0];
        const err_he = allocHandle(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        err_he.fd = err_fds[0];
        pso.in_handle = in_he.token;
        pso.out_handle = out_he.token;
        pso.err_handle = err_he.token;
    } else {
        pso.in_handle = 0;
        pso.out_handle = 0;
        pso.err_handle = 0;
    }
    completeInline(ov, 0, 0, proc_he.token);
}

fn abiProcessPipe(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    var fds: [2]i32 = .{ -1, -1 };
    const res = sys.sys2(sys.NR.pipe2, @intFromPtr(&fds), 0o2000000); // O_CLOEXEC
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    const r_he = allocHandle(s) orelse {
        _ = sys.sys1(sys.NR.close, @intCast(fds[0]));
        _ = sys.sys1(sys.NR.close, @intCast(fds[1]));
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    const w_he = allocHandle(s) orelse {
        r_he.used = false;
        _ = sys.sys1(sys.NR.close, @intCast(fds[0]));
        _ = sys.sys1(sys.NR.close, @intCast(fds[1]));
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    r_he.fd = fds[0];
    w_he.fd = fds[1];
    completeInline(ov, 0, 0, (w_he.token << 32) | (r_he.token & 0xffff_ffff));
}

fn abiProcessWait(ov: *pal_abi.Overlapped, proc_handle: u64) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, proc_handle) orelse {
        completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
        return;
    };
    if (!he.is_process) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    var status: i32 = 0;
    const res = sys.sys4(sys.NR.wait4, @intCast(he.fd), @intFromPtr(&status), 1, 0); // WNOHANG = 1
    if (sys.isErr(res)) |e| {
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
    }
    if (res == 0) {
        const op = allocOp(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .wait_proc;
        op.guest_ov = ov;
        op.pid = he.fd;
        return;
    }
    const code: u32 = @intCast((status >> 8) & 0xff);
    completeInline(ov, 0, 0, code);
}

fn abiProcessSignal(proc_handle: u64, signum: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const he = findHandle(s, proc_handle) orelse return;
    if (!he.is_process) return;
    _ = sys.sys2(sys.NR.kill, @intCast(he.fd), signum);
}
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
fn abiFdIsatty(_: u32) callconv(pal_abi.pal_cc) u32 {
    return 0;
}
fn abiSystemInfoWait(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    completeInline(ov, pal_abi.ERROR_UNSUPPORTED, 0, 0);
}

// ─── PAL Entry & poll(2) Event Loop ──────────────────────────────────────────

pub fn unixPlatformMain() callconv(.c) i32 {
    const s = statePtr();
    s.* = .{};
    s.host = hostPtr().*;

    const payload_ptr = s.host.hello_payload;
    const payload_len = s.host.hello_payload_size;
    if (payload_len == 0) return 12;

    const code_aligned = pageAlign(payload_len);
    const total_size = pal_abi.PREFIX_ABI_OFFSET + code_aligned;
    const mem_res = sys.sys6(sys.NR.mmap, 0, total_size, sys.PROT_READ | sys.PROT_WRITE, sys.MAP_PRIVATE | sys.MAP_ANONYMOUS, @bitCast(@as(isize, -1)), 0);
    if (sys.isErr(mem_res) != null) return 13;
    const mem_base: [*]u8 = @ptrFromInt(mem_res);

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
        .get_platform_info = abiGetPlatformInfoFull,
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
    _ = sys.sys3(sys.NR.mprotect, @intFromPtr(code_ptr), code_aligned, sys.PROT_READ | sys.PROT_EXEC);
    abiFlushIcache(code_ptr, payload_len);

    const RuntimeEntry = *const fn () callconv(pal_abi.pal_cc) void;
    const entry: RuntimeEntry = @ptrCast(@alignCast(code_ptr));

    while (true) {
        entry();
        if (s.pending_count == 0) break;

        var pfds: [32]sys.PollFd = undefined;
        var op_idx: [32]usize = undefined;
        var pfd_count: usize = 0;

        const now = monotonicNs();
        var min_wait_ns: ?u64 = null;
        var fired = false;

        for (&s.ops, 0..) |*op, idx| {
            if (!op.used) continue;
            if (op.kind == .timer) {
                if (op.deadline_ns <= now) {
                    const gov = op.guest_ov.?;
                    freeOp(s, op);
                    completeInline(gov, 0, 0, 0);
                    fired = true;
                } else {
                    const rem = op.deadline_ns - now;
                    if (min_wait_ns == null or rem < min_wait_ns.?) min_wait_ns = rem;
                }
            } else if ((op.kind == .read_fd or op.kind == .accept or op.kind == .udp_read) and pfd_count < pfds.len) {
                pfds[pfd_count] = .{ .fd = op.fd, .events = sys.POLLIN, .revents = 0 };
                op_idx[pfd_count] = idx;
                pfd_count += 1;
            } else if (op.kind == .wait_proc) {
                var status: i32 = 0;
                const wres = sys.sys4(sys.NR.wait4, @intCast(op.pid), @intFromPtr(&status), 1, 0);
                if (sys.isErr(wres)) |e| {
                    const gov = op.guest_ov.?;
                    freeOp(s, op);
                    completeInline(gov, sys.mapLinuxErrno(e), 0, 0);
                    fired = true;
                } else if (wres != 0) {
                    const gov = op.guest_ov.?;
                    freeOp(s, op);
                    completeInline(gov, 0, 0, @intCast((status >> 8) & 0xff));
                    fired = true;
                } else {
                    const poll_ns: u64 = 10_000_000;
                    if (min_wait_ns == null or poll_ns < min_wait_ns.?) min_wait_ns = poll_ns;
                }
            }
        }
        if (fired) continue;

        var ts: sys.Timespec = undefined;
        const ts_ptr: usize = if (min_wait_ns) |w| blk: {
            ts = .{ .sec = @intCast(w / 1_000_000_000), .nsec = @intCast(w % 1_000_000_000) };
            break :blk @intFromPtr(&ts);
        } else 0;

        _ = sys.sys4(sys.NR.ppoll, @intFromPtr(&pfds), pfd_count, ts_ptr, 0);

        for (0..pfd_count) |i| {
            if (pfds[i].revents != 0) {
                const op = &s.ops[op_idx[i]];
                if (!op.used) continue;
                const gov = op.guest_ov.?;
                if (op.kind == .accept) {
                    const ares = sys.sys3(sys.NR.accept, @intCast(op.fd), 0, 0);
                    freeOp(s, op);
                    if (sys.isErr(ares)) |e| {
                        completeInline(gov, sys.mapLinuxErrno(e), 0, 0);
                    } else {
                        const cli_fd: i32 = @intCast(ares);
                        const fl = sys.sys3(sys.NR.fcntl, @intCast(cli_fd), 3, 0);
                        if (sys.isErr(fl) == null) {
                            _ = sys.sys3(sys.NR.fcntl, @intCast(cli_fd), 4, fl | sys.O_NONBLOCK);
                        }
                        if (allocHandle(s)) |cli_he| {
                            cli_he.fd = cli_fd;
                            cli_he.is_socket = true;
                            completeInline(gov, 0, 0, cli_he.token);
                        } else {
                            _ = sys.sys1(sys.NR.close, @intCast(cli_fd));
                            completeInline(gov, pal_abi.ERROR_NOMEM, 0, 0);
                        }
                    }
                } else if (op.kind == .udp_read) {
                    var sa: sys.SockAddrIn = undefined;
                    var sa_len: u32 = @sizeOf(sys.SockAddrIn);
                    const res = sys.sys6(sys.NR.recvfrom, @intCast(op.fd), @intFromPtr(op.buf.?), op.len, 0, @intFromPtr(&sa), @intFromPtr(&sa_len));
                    freeOp(s, op);
                    if (sys.isErr(res)) |e| {
                        completeInline(gov, sys.mapLinuxErrno(e), 0, 0);
                    } else {
                        completeInline(gov, 0, 0, res);
                    }
                } else {
                    const res = sys.sys3(sys.NR.read, @intCast(op.fd), @intFromPtr(op.buf.?), op.len);
                    freeOp(s, op);
                    if (sys.isErr(res)) |e| {
                        completeInline(gov, sys.mapLinuxErrno(e), 0, 0);
                    } else {
                        completeInline(gov, 0, 0, res);
                    }
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

test "linux PAL errno mapping" {
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, sys.mapLinuxErrno(2));
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, sys.mapLinuxErrno(125));
}
