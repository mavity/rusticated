const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");
const sys = @import("linux_sys.zig");
const worker_pool = @import("worker_pool.zig");

const UnixPlatformHost = extern struct {
    runtime_slice: [*]const u8,
    runtime_slice_size: usize,
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
    is_pipe: bool = false,
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
    signal_wait = 6,
    system_info_wait = 7,
    dns_lookup = 8,
    write_fd = 9,
    net_connect = 10,
    worker_op = 11,
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
    net_cert_verify = 13,
    file_read = 14,
    file_write = 15,
    file_seek = 16,
};

const PendingOp = struct {
    used: bool = false,
    kind: OpKind = .none,
    work_kind: WorkOpKind = .none,
    guest_ov: ?*pal_abi.Overlapped = null,
    fd: i32 = -1,
    pid: i32 = -1,
    signum: u32 = 0,
    buf: ?[*]u8 = null,
    len: usize = 0,
    buf2: ?[*]const u8 = null,
    len2: usize = 0,
    flags: u32 = 0,
    arg_i64_a: i64 = 0,
    arg_u32_a: u32 = 0,
    arg_i64_b: i64 = 0,
    arg_u32_b: u32 = 0,
    is_dir: bool = false,
    is_udp: bool = false,
    canceled: bool = false,
    worker_done: u32 = 0,
    res_err: u32 = 0,
    res_ext: u64 = 0,
    res_cont: u64 = 0,
    deadline_ns: u64 = 0,
};

const PalState = struct {
    host: UnixPlatformHost = undefined,
    next_token: u64 = 3,
    handles: [MAX_HANDLES]HandleEntry = @splat(.{}),
    ops: [MAX_OPS]PendingOp = @splat(.{}),
    pending_count: usize = 0,
    saved_termios: sys.Termios = .{},
    has_saved_termios: bool = false,
    signal_fd: i32 = -1,
    signal_mask: u64 = 0,
    event_fd: i32 = -1,
    pool_ptr: ?*worker_pool.LinuxWorkerPool = null,
    system_info_hash: u64 = 0,
    has_system_info_hash: bool = false,
    system_info_version: u32 = 1,
    resolv_ns: [4]u8 = .{ 1, 1, 1, 1 },
    has_resolv_ns: bool = false,
};

var test_state: ?*PalState = null;

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
    if (builtin.is_test) {
        return test_state orelse unreachable;
    } else {
        return @ptrCast(@alignCast(sliceOrigin() - pal_abi.PREFIX_GLOBALS_OFFSET));
    }
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
        const ctr = asm ("mrs %[ret], ctr_el0"
            : [ret] "=r" (-> usize),
        );
        const idc = (ctr & (1 << 28)) != 0;
        const dic = (ctr & (1 << 29)) != 0;
        if (idc and dic) {
            asm volatile ("dsb ish\nisb" ::: .{ .memory = true });
            return;
        }
        const d_line: usize = @as(usize, 4) << @intCast(ctr & 0xf);
        const i_line: usize = @as(usize, 4) << @intCast((ctr >> 16) & 0xf);
        if (!idc) {
            const start = @intFromPtr(ptr) & ~(d_line - 1);
            const end = (@intFromPtr(ptr) + size + d_line - 1) & ~(d_line - 1);
            var addr = start;
            while (addr < end) : (addr += d_line) {
                asm volatile ("dc cvau, %[addr]"
                    :
                    : [addr] "r" (addr),
                    : .{ .memory = true });
            }
        }
        asm volatile ("dsb ish" ::: .{ .memory = true });
        if (!dic) {
            const start = @intFromPtr(ptr) & ~(i_line - 1);
            const end = (@intFromPtr(ptr) + size + i_line - 1) & ~(i_line - 1);
            var addr = start;
            while (addr < end) : (addr += i_line) {
                asm volatile ("ic ivau, %[addr]"
                    :
                    : [addr] "r" (addr),
                    : .{ .memory = true });
            }
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
    const s = statePtr();
    if (s.has_saved_termios) {
        _ = sys.sys3(sys.NR.ioctl, 0, sys.TCSETS, @intFromPtr(&s.saved_termios));
    }
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

fn envLookup(s: *PalState, key: []const u8) ?[]const u8 {
    if (s.host.envc == 0) return null;
    for (0..s.host.envc) |i| {
        const entry = std.mem.sliceTo(s.host.envp[i], 0);
        if (entry.len > key.len and entry[key.len] == '=' and std.mem.eql(u8, entry[0..key.len], key)) {
            return entry[key.len + 1 ..];
        }
    }
    return null;
}

fn readSmallFile(path_z: [*:0]const u8, buf: []u8) ?[]const u8 {
    const fd_res = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(path_z), sys.O_RDONLY, 0);
    if (sys.isErr(fd_res) != null) return null;
    const fd: usize = fd_res;
    defer _ = sys.sys1(sys.NR.close, fd);
    const n_res = sys.sys3(sys.NR.read, fd, @intFromPtr(buf.ptr), buf.len);
    if (sys.isErr(n_res) != null) return null;
    return buf[0..n_res];
}

var test_linux_sys_ctx_override: ?*const fn (*pal_abi.AbiPlatformInfo) void = null;

fn populateSystemContext(s: *PalState, info: *pal_abi.AbiPlatformInfo) void {
    info.system_context_version = s.system_info_version;
    if (builtin.is_test) {
        if (test_linux_sys_ctx_override) |hook| {
            hook(info);
            return;
        }
    }

    if (envLookup(s, "LC_ALL") orelse envLookup(s, "LC_MESSAGES") orelse envLookup(s, "LANG")) |loc| {
        if (loc.len > 0) copyFixed(&info.locale, loc);
    }

    if (envLookup(s, "TZ")) |tz| {
        if (tz.len > 0) copyFixed(&info.time_zone, tz);
    } else {
        const lc_path = "/etc/localtime";
        var link_buf: [256]u8 = undefined;
        const rl = sys.sys4(sys.NR.readlinkat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(lc_path.ptr), @intFromPtr(&link_buf), link_buf.len);
        if (sys.isErr(rl) == null and rl > 0) {
            const target = link_buf[0..rl];
            if (std.mem.indexOf(u8, target, "zoneinfo/")) |idx| {
                copyFixed(&info.time_zone, target[idx + "zoneinfo/".len ..]);
            }
        }
    }

    if (envLookup(s, "GTK_THEME")) |theme| {
        if (std.mem.indexOf(u8, theme, "dark") != null or std.mem.indexOf(u8, theme, "Dark") != null) {
            copyFixed(&info.appearance, "dark");
        } else if (theme.len > 0) {
            copyFixed(&info.appearance, "light");
        }
    } else if (envLookup(s, "COLORFGBG")) |cfg| {
        if (std.mem.lastIndexOfScalar(u8, cfg, ';')) |idx| {
            const bg = cfg[idx + 1 ..];
            if (std.mem.eql(u8, bg, "0") or std.mem.eql(u8, bg, "8")) {
                copyFixed(&info.appearance, "dark");
            } else {
                copyFixed(&info.appearance, "light");
            }
        }
    }

    var pwr_buf: [16]u8 = undefined;
    if (readSmallFile("/sys/class/power_supply/AC/online", &pwr_buf) orelse
        readSmallFile("/sys/class/power_supply/ACAD/online", &pwr_buf) orelse
        readSmallFile("/sys/class/power_supply/ADP1/online", &pwr_buf)) |raw|
    {
        const trimmed = std.mem.trim(u8, raw, " \t\r\n");
        if (std.mem.eql(u8, trimmed, "1")) {
            copyFixed(&info.power_source, "ac");
        } else if (std.mem.eql(u8, trimmed, "0")) {
            copyFixed(&info.power_source, "battery");
        }
    }
}

fn hashSlice(h: u64, bytes: []const u8) u64 {
    var v = h;
    for (bytes) |b| {
        v ^= b;
        v *%= 0x100000001b3;
    }
    return v;
}

fn captureSystemContextHash(s: *PalState) u64 {
    var info: pal_abi.AbiPlatformInfo = .{};
    populateSystemContext(s, &info);
    var h: u64 = 0xcbf29ce484222325;
    h = hashSlice(h, &info.locale);
    h = hashSlice(h, &info.time_zone);
    h = hashSlice(h, &info.keyboard_layout_id);
    h = hashSlice(h, &info.appearance);
    h = hashSlice(h, &info.power_source);
    return h;
}

fn pollSystemContextChanged(s: *PalState) bool {
    const h = captureSystemContextHash(s);
    if (!s.has_system_info_hash) {
        s.system_info_hash = h;
        s.has_system_info_hash = true;
        return false;
    }
    if (h != s.system_info_hash) {
        s.system_info_hash = h;
        s.system_info_version +%= 1;
        return true;
    }
    return false;
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
    const s = statePtr();
    if (envLookup(s, "HOME")) |home| {
        copyFixed(&info.user_home_dir, home);
    }
    if (envLookup(s, "TMPDIR")) |tmp| {
        copyFixed(&info.temp_dir, tmp);
    } else if (envLookup(s, "PREFIX")) |prefix| {
        var tmp_buf: [64]u8 = undefined;
        if (std.fmt.bufPrint(&tmp_buf, "{s}/tmp", .{prefix})) |p| {
            copyFixed(&info.temp_dir, p);
        } else |_| {
            copyFixed(&info.temp_dir, "/tmp");
        }
    } else {
        copyFixed(&info.temp_dir, "/tmp");
    }
    populateSystemContext(s, &info);
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
            if (op.kind == .worker_op) {
                @atomicStore(bool, &op.canceled, true, .release);
                completeInline(ov, pal_abi.ERROR_CANCELED, 0, 0);
                return;
            }
            if ((op.kind == .dns_lookup or op.kind == .net_connect) and op.fd >= 0) {
                _ = sys.sys1(sys.NR.close, @intCast(op.fd));
            }
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
        if (len == 0) {
            completeInline(ov, 0, 0, 0);
            return;
        }
        const res = sys.sys3(sys.NR.read, @intCast(he.pty_read_fd), @intFromPtr(buf), len);
        if (sys.isErr(res)) |e| {
            if (e == 11) {
                const op = allocOp(s) orelse {
                    completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
                    return;
                };
                op.kind = .read_fd;
                op.guest_ov = ov;
                op.fd = he.pty_read_fd;
                op.buf = buf;
                op.len = len;
                return;
            }
            completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
            return;
        }
        completeInline(ov, 0, 0, res);
        return;
    }
    if (he.is_udp) {
        var sa: sys.SockAddrIn = undefined;
        var sa_len: u32 = @sizeOf(sys.SockAddrIn);
        const res = sys.sys6(sys.NR.recvfrom, @intCast(he.fd), @intFromPtr(buf), len, 0, @intFromPtr(&sa), @intFromPtr(&sa_len));
        if (sys.isErr(res)) |e| {
            if (e == 11) {
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
            completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
            return;
        }
        completeInline(ov, 0, 0, res);
        return;
    }
    const positional = (ov.flags & pal_abi.READ_FLAG_POSITIONAL) != 0;
    const is_stream = he.is_socket or he.is_pipe;
    if (!is_stream) {
        var off: i64 = 0;
        if (positional) {
            const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
            off = trailing.*;
            if (off < 0) {
                completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
                return;
            }
        }
        const op = allocOp(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        op.kind = .worker_op;
        op.work_kind = .file_read;
        op.guest_ov = ov;
        op.fd = he.fd;
        op.buf = buf;
        op.len = len;
        op.flags = @intFromBool(positional);
        op.arg_i64_a = off;
        submitOrRunInline(s, op);
        return;
    }

    const res = sys.sys3(sys.NR.read, @intCast(he.fd), @intFromPtr(buf), len);
    if (sys.isErr(res)) |e| {
        if (e == 11) {
            const op = allocOp(s) orelse {
                completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
                return;
            };
            op.kind = .read_fd;
            op.guest_ov = ov;
            op.fd = he.fd;
            op.buf = buf;
            op.len = len;
            return;
        }
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
                    if (e == 11) {
                        const op = allocOp(s) orelse {
                            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
                            return;
                        };
                        op.kind = .write_fd;
                        op.guest_ov = ov;
                        op.fd = he.pty_write_fd;
                        op.buf = @constCast(buf);
                        op.len = len;
                        return;
                    }
                    completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
                    return;
                }
                completeInline(ov, 0, 0, res);
                return;
            }
            if (he.is_udp) {
                const res = sys.sys6(sys.NR.sendto, @intCast(he.fd), @intFromPtr(buf), len, 0, 0, 0);
                if (sys.isErr(res)) |e| {
                    if (e == 11) {
                        const op = allocOp(s) orelse {
                            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
                            return;
                        };
                        op.kind = .write_fd;
                        op.guest_ov = ov;
                        op.fd = he.fd;
                        op.is_udp = true;
                        op.buf = @constCast(buf);
                        op.len = len;
                        return;
                    }
                    completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
                    return;
                }
                completeInline(ov, 0, 0, res);
                return;
            }
        }
    }
    var is_stream = (handle == 1 or handle == 2);
    const fd: i32 = if (handle == 1) 1 else if (handle == 2) 2 else blk: {
        const he = findHandle(s, handle) orelse {
            completeInline(ov, pal_abi.ERROR_BADF, 0, 0);
            return;
        };
        is_stream = he.is_socket or he.is_pipe;
        break :blk he.fd;
    };
    const positional = (ov.flags & pal_abi.WRITE_FLAG_POSITIONAL) != 0;
    if (!is_stream) {
        if (positional and len == 0) {
            completeInline(ov, 0, 0, 0);
            return;
        }
        var off: i64 = 0;
        if (positional) {
            const trailing: *const i64 = @ptrCast(@alignCast(@as([*]const u8, @ptrCast(ov)) + @sizeOf(pal_abi.Overlapped)));
            off = trailing.*;
            if (off < 0) {
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
        op.buf = @constCast(buf);
        op.len = len;
        op.flags = @intFromBool(positional);
        op.arg_i64_a = off;
        submitOrRunInline(s, op);
        return;
    }
    const res = sys.sys3(sys.NR.write, @intCast(fd), @intFromPtr(buf), len);
    if (sys.isErr(res)) |e| {
        if (e == 11) {
            const op = allocOp(s) orelse {
                completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
                return;
            };
            op.kind = .write_fd;
            op.guest_ov = ov;
            op.fd = fd;
            op.buf = @constCast(buf);
            op.len = len;
            return;
        }
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

fn execPathOpen(op: *PendingOp) void {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(op.buf.?, op.len, &cbuf) orelse {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    };
    var oflags: u32 = switch (op.flags & 3) {
        0 => sys.O_RDONLY,
        1 => sys.O_WRONLY,
        else => sys.O_RDWR,
    };
    if ((op.flags & pal_abi.OPEN_FLAG_CREATE) != 0) oflags |= sys.O_CREAT;
    if ((op.flags & pal_abi.OPEN_FLAG_EXCL) != 0) oflags |= sys.O_EXCL;
    if ((op.flags & pal_abi.OPEN_FLAG_TRUNC) != 0) oflags |= sys.O_TRUNC;
    if ((op.flags & pal_abi.OPEN_FLAG_APPEND) != 0) oflags |= sys.O_APPEND;
    if ((op.flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0) oflags |= sys.O_DIRECTORY;

    const res = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), oflags, 0o666);
    if (sys.isErr(res)) |e| {
        op.res_err = sys.mapLinuxErrno(e);
        return;
    }
    op.fd = @intCast(res);
    op.is_dir = (op.flags & pal_abi.OPEN_FLAG_DIRECTORY) != 0;
    op.res_err = 0;
}

fn execDirRead(op: *PendingOp) void {
    const buf = op.buf.?;
    const len = op.len;
    var raw: [4096]u8 = undefined;
    const res = sys.sys3(sys.NR.getdents64, @intCast(op.fd), @intFromPtr(&raw), raw.len);
    if (sys.isErr(res)) |e| {
        op.res_err = sys.mapLinuxErrno(e);
        return;
    }
    if (res == 0) {
        op.res_err = 0;
        op.res_ext = 0;
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
    op.res_err = 0;
    op.res_ext = out_pos;
}

fn execPathStat(op: *PendingOp) void {
    var cbuf: [4096]u8 = undefined;
    const cpath = toCPath(op.buf2.?, op.len2, &cbuf) orelse {
        op.res_err = pal_abi.ERROR_INVALID;
        return;
    };
    var lst: sys.LinuxStat = undefined;
    const at_flags: u32 = if ((op.flags & pal_abi.PATH_STAT_FLAG_NOFOLLOW) != 0) sys.AT_SYMLINK_NOFOLLOW else 0;
    const res = sys.sys4(sys.NR.newfstatat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), @intFromPtr(&lst), at_flags);
    if (sys.isErr(res)) |e| {
        op.res_err = sys.mapLinuxErrno(e);
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
    @memcpy(op.buf.?[0..bytes.len], bytes);
    op.res_err = 0;
}

fn executeWorkItem(ctx: *anyopaque, op_idx: u16) void {
    const s: *PalState = @ptrCast(@alignCast(ctx));
    const op = &s.ops[op_idx];
    switch (op.work_kind) {
        .path_open => execPathOpen(op),
        .dir_read => execDirRead(op),
        .path_stat => execPathStat(op),
        .handle_truncate => {
            const res = sys.sys2(sys.NR.ftruncate, @intCast(op.fd), @bitCast(op.arg_i64_a));
            op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
        },
        .path_link => {
            var b1: [4096]u8 = undefined;
            var b2: [4096]u8 = undefined;
            const s1 = toCPath(op.buf.?, op.len, &b1);
            const s2 = toCPath(op.buf2.?, op.len2, &b2);
            if (s1 == null or s2 == null) {
                op.res_err = pal_abi.ERROR_INVALID;
            } else {
                const cwd: usize = @bitCast(@as(isize, sys.AT_FDCWD));
                const res = sys.sys6(sys.NR.linkat, cwd, @intFromPtr(s1.?), cwd, @intFromPtr(s2.?), 0, 0);
                op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
            }
        },
        .path_symlink => {
            var b1: [4096]u8 = undefined;
            var b2: [4096]u8 = undefined;
            const s1 = toCPath(op.buf.?, op.len, &b1);
            const s2 = toCPath(op.buf2.?, op.len2, &b2);
            if (s1 == null or s2 == null) {
                op.res_err = pal_abi.ERROR_INVALID;
            } else {
                const res = sys.sys3(sys.NR.symlinkat, @intFromPtr(s1.?), @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(s2.?));
                op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
            }
        },
        .path_readlink => {
            var cbuf: [4096]u8 = undefined;
            if (toCPath(op.buf2.?, op.len2, &cbuf)) |cpath| {
                const res = sys.sys4(sys.NR.readlinkat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), @intFromPtr(op.buf.?), op.len);
                if (sys.isErr(res)) |e| {
                    op.res_err = sys.mapLinuxErrno(e);
                } else {
                    op.res_err = 0;
                    op.res_ext = res;
                }
            } else {
                op.res_err = pal_abi.ERROR_INVALID;
            }
        },
        .path_chmod => {
            var cbuf: [4096]u8 = undefined;
            if (toCPath(op.buf.?, op.len, &cbuf)) |cpath| {
                const res = sys.sys4(sys.NR.fchmodat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), op.flags, 0);
                op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
            } else {
                op.res_err = pal_abi.ERROR_INVALID;
            }
        },
        .path_set_times => {
            var cbuf: [4096]u8 = undefined;
            if (toCPath(op.buf.?, op.len, &cbuf)) |cpath| {
                var times = [2]sys.Timespec{
                    .{ .sec = op.arg_i64_a, .nsec = op.arg_u32_a },
                    .{ .sec = op.arg_i64_b, .nsec = op.arg_u32_b },
                };
                const at_flags: u32 = if ((op.flags & pal_abi.PATH_SET_TIMES_FLAG_NOFOLLOW) != 0) sys.AT_SYMLINK_NOFOLLOW else 0;
                const res = sys.sys4(sys.NR.utimensat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), @intFromPtr(&times), at_flags);
                op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
            } else {
                op.res_err = pal_abi.ERROR_INVALID;
            }
        },
        .path_remove => {
            var cbuf: [4096]u8 = undefined;
            if (toCPath(op.buf.?, op.len, &cbuf)) |cpath| {
                const at_flags: u32 = if ((op.flags & (pal_abi.PATH_REMOVE_FLAG_DIR | 0x200)) != 0) sys.AT_REMOVEDIR else 0;
                const res = sys.sys3(sys.NR.unlinkat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), at_flags);
                op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
            } else {
                op.res_err = pal_abi.ERROR_INVALID;
            }
        },
        .path_mkdir => {
            var cbuf: [4096]u8 = undefined;
            if (toCPath(op.buf.?, op.len, &cbuf)) |cpath| {
                const res = sys.sys3(sys.NR.mkdirat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(cpath), op.flags);
                op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
            } else {
                op.res_err = pal_abi.ERROR_INVALID;
            }
        },
        .path_rename => {
            var b1: [4096]u8 = undefined;
            var b2: [4096]u8 = undefined;
            const s1 = toCPath(op.buf.?, op.len, &b1);
            const s2 = toCPath(op.buf2.?, op.len2, &b2);
            if (s1 == null or s2 == null) {
                op.res_err = pal_abi.ERROR_INVALID;
            } else {
                const cwd: usize = @bitCast(@as(isize, sys.AT_FDCWD));
                const res = sys.sys4(sys.NR.renameat, cwd, @intFromPtr(s1.?), cwd, @intFromPtr(s2.?));
                op.res_err = if (sys.isErr(res)) |e| sys.mapLinuxErrno(e) else 0;
            }
        },
        .net_cert_verify => {
            op.res_err = verifyCertChainBytes(s, op.buf.?[0..op.len], op.buf2.?[0..op.len2]);
        },
        .file_read => {
            const res = if (op.flags != 0)
                sys.sys4(sys.NR.pread64, @intCast(op.fd), @intFromPtr(op.buf.?), op.len, @bitCast(op.arg_i64_a))
            else
                sys.sys3(sys.NR.read, @intCast(op.fd), @intFromPtr(op.buf.?), op.len);
            if (sys.isErr(res)) |e| {
                op.res_err = sys.mapLinuxErrno(e);
            } else {
                op.res_err = 0;
                op.res_ext = res;
            }
        },
        .file_write => {
            const res = if (op.flags != 0)
                sys.sys4(sys.NR.pwrite64, @intCast(op.fd), @intFromPtr(op.buf.?), op.len, @bitCast(op.arg_i64_a))
            else
                sys.sys3(sys.NR.write, @intCast(op.fd), @intFromPtr(op.buf.?), op.len);
            if (sys.isErr(res)) |e| {
                op.res_err = sys.mapLinuxErrno(e);
            } else {
                op.res_err = 0;
                op.res_ext = res;
            }
        },
        .file_seek => {
            const res = sys.sys3(sys.NR.lseek, @intCast(op.fd), @bitCast(op.arg_i64_a), op.flags);
            if (sys.isErr(res)) |e| {
                op.res_err = sys.mapLinuxErrno(e);
            } else {
                op.res_err = 0;
                op.res_ext = res;
            }
        },
        .none => {},
    }
    @atomicStore(u32, &op.worker_done, 1, .release);
}

fn opIndex(s: *PalState, op: *PendingOp) u16 {
    return @intCast((@intFromPtr(op) - @intFromPtr(&s.ops[0])) / @sizeOf(PendingOp));
}

fn submitOrRunInline(s: *PalState, op: *PendingOp) void {
    if (s.pool_ptr) |pool| {
        if (pool.submit(opIndex(s, op))) return;
    }
    executeWorkItem(s, opIndex(s, op));
    finishWorkerOp(s, op);
}

fn finishWorkerOp(s: *PalState, op: *PendingOp) void {
    const canceled = @atomicLoad(bool, &op.canceled, .acquire);
    const gov = op.guest_ov.?;
    const wkind = op.work_kind;
    const err = op.res_err;
    const ext = op.res_ext;
    const cont = op.res_cont;
    const fd = op.fd;
    const is_dir = op.is_dir;
    freeOp(s, op);
    if (canceled) {
        if (wkind == .path_open and err == 0 and fd >= 0) {
            _ = sys.sys1(sys.NR.close, @intCast(fd));
        }
        return;
    }
    if (wkind == .path_open and err == 0) {
        const he = allocHandle(s) orelse {
            _ = sys.sys1(sys.NR.close, @intCast(fd));
            completeInline(gov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        he.fd = fd;
        he.is_dir = is_dir;
        completeInline(gov, 0, 0, he.token);
        return;
    }
    completeInline(gov, err, cont, ext);
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
        if (he.pty_read_fd >= 0) _ = sys.sys1(sys.NR.close, @intCast(he.pty_read_fd));
        if (he.pty_write_fd >= 0 and he.pty_write_fd != he.pty_read_fd) _ = sys.sys1(sys.NR.close, @intCast(he.pty_write_fd));
        he.* = .{};
        return;
    }
    if (he.fd >= 0) _ = sys.sys1(sys.NR.close, @intCast(he.fd));
    he.* = .{};
}

fn abiPathOpen(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32) callconv(pal_abi.pal_cc) void {
    if (len == 0 or len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_open;
    op.guest_ov = ov;
    op.buf = @constCast(path);
    op.len = len;
    op.flags = flags;
    submitOrRunInline(s, op);
}

fn abiDirRead(ov: *pal_abi.Overlapped, handle: u64, buf: [*]u8, len: usize) callconv(pal_abi.pal_cc) void {
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
    op.work_kind = .dir_read;
    op.guest_ov = ov;
    op.fd = he.fd;
    op.buf = buf;
    op.len = len;
    submitOrRunInline(s, op);
}

fn abiPathStat(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, flags: u32, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    if (cap < @sizeOf(pal_abi.AbiStat) or len == 0 or len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_stat;
    op.guest_ov = ov;
    op.buf2 = path;
    op.len2 = len;
    op.buf = out;
    op.len = cap;
    op.flags = flags;
    submitOrRunInline(s, op);
}

fn abiPathLink(ov: *pal_abi.Overlapped, src: [*]const u8, s_len: usize, dst: [*]const u8, d_len: usize, _: u32) callconv(pal_abi.pal_cc) void {
    if (s_len == 0 or s_len >= 4096 or d_len == 0 or d_len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_link;
    op.guest_ov = ov;
    op.buf = @constCast(src);
    op.len = s_len;
    op.buf2 = dst;
    op.len2 = d_len;
    submitOrRunInline(s, op);
}

fn abiPathSymlink(ov: *pal_abi.Overlapped, target: [*]const u8, t_len: usize, link: [*]const u8, l_len: usize, _: u32) callconv(pal_abi.pal_cc) void {
    if (t_len == 0 or t_len >= 4096 or l_len == 0 or l_len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_symlink;
    op.guest_ov = ov;
    op.buf = @constCast(target);
    op.len = t_len;
    op.buf2 = link;
    op.len2 = l_len;
    submitOrRunInline(s, op);
}

fn abiPathReadlink(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    if (len == 0 or len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_readlink;
    op.guest_ov = ov;
    op.buf2 = path;
    op.len2 = len;
    op.buf = out;
    op.len = cap;
    submitOrRunInline(s, op);
}

fn abiPathChmod(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    if (len == 0 or len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_chmod;
    op.guest_ov = ov;
    op.buf = @constCast(path);
    op.len = len;
    op.flags = mode;
    submitOrRunInline(s, op);
}

fn abiPathSetTimes(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, at_sec: i64, at_nsec: u32, mt_sec: i64, mt_nsec: u32, flags: u32) callconv(pal_abi.pal_cc) void {
    if (len == 0 or len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
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
    if (len == 0 or len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_remove;
    op.guest_ov = ov;
    op.buf = @constCast(path);
    op.len = len;
    op.flags = flags;
    submitOrRunInline(s, op);
}

fn abiPathMkdir(ov: *pal_abi.Overlapped, path: [*]const u8, len: usize, mode: u32) callconv(pal_abi.pal_cc) void {
    if (len == 0 or len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_mkdir;
    op.guest_ov = ov;
    op.buf = @constCast(path);
    op.len = len;
    op.flags = mode;
    submitOrRunInline(s, op);
}

fn abiPathRename(ov: *pal_abi.Overlapped, old_p: [*]const u8, o_len: usize, new_p: [*]const u8, n_len: usize) callconv(pal_abi.pal_cc) void {
    if (o_len == 0 or o_len >= 4096 or n_len == 0 or n_len >= 4096) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .path_rename;
    op.guest_ov = ov;
    op.buf = @constCast(old_p);
    op.len = o_len;
    op.buf2 = new_p;
    op.len2 = n_len;
    submitOrRunInline(s, op);
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

    const fl = sys.sys3(sys.NR.fcntl, @intCast(fd), 3, 0);
    if (sys.isErr(fl) == null) {
        _ = sys.sys3(sys.NR.fcntl, @intCast(fd), 4, fl | sys.O_NONBLOCK);
    }
    const conn_res = sys.sys3(sys.NR.connect, @intCast(fd), @intFromPtr(&sa), @sizeOf(sys.SockAddrIn));
    if (sys.isErr(conn_res)) |e| {
        if (e == 115) { // EINPROGRESS
            const op = allocOp(s) orelse {
                _ = sys.sys1(sys.NR.close, @intCast(fd));
                completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
                return;
            };
            op.kind = .net_connect;
            op.guest_ov = ov;
            op.fd = fd;
            op.arg_u32_a = port;
            op.is_udp = is_udp;
            return;
        }
        _ = sys.sys1(sys.NR.close, @intCast(fd));
        completeInline(ov, sys.mapLinuxErrno(e), 0, 0);
        return;
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

fn parseNameserverFromText(text: []const u8) ?[4]u8 {
    var it = std.mem.splitScalar(u8, text, '\n');
    while (it.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (std.mem.startsWith(u8, line, "nameserver")) {
            const rest = std.mem.trim(u8, line["nameserver".len..], " \t");
            if (parseIpv4(rest)) |ip| return ip;
        }
    }
    return null;
}

fn fmtZ(buf: []u8, comptime fmt: []const u8, args: anytype) ?[:0]const u8 {
    if (buf.len == 0) return null;
    const s = std.fmt.bufPrint(buf[0 .. buf.len - 1], fmt, args) catch return null;
    buf[s.len] = 0;
    return buf[0..s.len :0];
}

fn readResolvNameserver() [4]u8 {
    const s = statePtr();
    if (s.has_resolv_ns) return s.resolv_ns;
    var buf: [2048]u8 = undefined;
    var found: [4]u8 = .{ 1, 1, 1, 1 };
    if (readSmallFile("/etc/resolv.conf", &buf)) |text| {
        if (parseNameserverFromText(text)) |ip| found = ip;
    } else if (envLookup(s, "PREFIX")) |prefix| {
        var path_buf: [512]u8 = undefined;
        if (fmtZ(&path_buf, "{s}/etc/resolv.conf", .{prefix})) |pz| {
            if (readSmallFile(pz, &buf)) |text| {
                if (parseNameserverFromText(text)) |ip| found = ip;
            }
        }
    }
    s.resolv_ns = found;
    s.has_resolv_ns = true;
    return found;
}

fn writeNetLookupResult(out: [*]u8, cap: usize, families: []const u8, addrs_data: []const u8) u32 {
    const count: u32 = @intCast(families.len);
    const total_size: usize = 8 + families.len + addrs_data.len;
    if (cap < total_size) return 0;
    std.mem.writeInt(u32, out[0..4], @intCast(total_size), .little);
    std.mem.writeInt(u32, out[4..8], count, .little);
    @memcpy(out[8..][0..families.len], families);
    @memcpy(out[8 + families.len ..][0..addrs_data.len], addrs_data);
    return @intCast(total_size);
}

fn parseDnsResponse(r: []const u8, out: [*]u8, cap: usize) struct { err: u32, total: u32 } {
    if (r.len < 12) return .{ .err = pal_abi.ERROR_NOENT, .total = 0 };
    const qdcount = std.mem.readInt(u16, r[4..6], .big);
    const ancount = std.mem.readInt(u16, r[6..8], .big);
    var pos: usize = 12;
    for (0..qdcount) |_| {
        pos = skipDnsName(r, pos) orelse return .{ .err = pal_abi.ERROR_NOENT, .total = 0 };
        if (pos + 4 > r.len) break;
        pos += 4;
    }
    var families: [16]u8 = undefined;
    var addr_bytes: [16 * 16]u8 = undefined;
    var count: usize = 0;
    var addr_off: usize = 0;
    for (0..ancount) |_| {
        pos = skipDnsName(r, pos) orelse break;
        if (pos + 10 > r.len) break;
        const rtype = std.mem.readInt(u16, r[pos..][0..2], .big);
        const rdlen = std.mem.readInt(u16, r[pos + 8 ..][0..2], .big);
        pos += 10;
        if (pos + rdlen > r.len) break;
        if (rtype == 1 and rdlen == 4 and count < families.len and addr_off + 4 <= addr_bytes.len) {
            families[count] = 4;
            @memcpy(addr_bytes[addr_off..][0..4], r[pos..][0..4]);
            count += 1;
            addr_off += 4;
        } else if (rtype == 28 and rdlen == 16 and count < families.len and addr_off + 16 <= addr_bytes.len) {
            families[count] = 6;
            @memcpy(addr_bytes[addr_off..][0..16], r[pos..][0..16]);
            count += 1;
            addr_off += 16;
        }
        pos += rdlen;
    }
    if (count == 0) return .{ .err = pal_abi.ERROR_NOENT, .total = 0 };
    const total = writeNetLookupResult(out, cap, families[0..count], addr_bytes[0..addr_off]);
    if (total == 0) return .{ .err = pal_abi.ERROR_INVALID, .total = 0 };
    return .{ .err = 0, .total = total };
}

fn abiNetLookup(ov: *pal_abi.Overlapped, name_ptr: [*]const u8, name_len: usize, out: [*]u8, cap: usize) callconv(pal_abi.pal_cc) void {
    if (name_len == 0 or name_len > 255) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const name = name_ptr[0..name_len];
    if (std.mem.eql(u8, name, "localhost")) {
        const fam = [_]u8{4};
        const ip = [_]u8{ 127, 0, 0, 1 };
        const total = writeNetLookupResult(out, cap, &fam, &ip);
        if (total == 0) {
            completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        completeInline(ov, 0, 0, total);
        return;
    }
    if (parseIpv4(name)) |ip| {
        const fam = [_]u8{4};
        const total = writeNetLookupResult(out, cap, &fam, &ip);
        if (total == 0) {
            completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
            return;
        }
        completeInline(ov, 0, 0, total);
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

    const sock_res = sys.sys3(sys.NR.socket, 2, 2 | sys.O_NONBLOCK | 0o2000000, 17); // AF_INET, SOCK_DGRAM|SOCK_NONBLOCK|SOCK_CLOEXEC, IPPROTO_UDP
    if (sys.isErr(sock_res) != null) {
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }
    const fd: i32 = @intCast(sock_res);

    var sa: sys.SockAddrIn = .{
        .family = 2,
        .port_be = std.mem.nativeToBig(u16, 53),
        .addr = readResolvNameserver(),
    };
    if (sys.isErr(sys.sys3(sys.NR.connect, @intCast(fd), @intFromPtr(&sa), @sizeOf(sys.SockAddrIn))) != null or
        sys.isErr(sys.sys3(sys.NR.write, @intCast(fd), @intFromPtr(&pkt), pkt_len)) != null)
    {
        _ = sys.sys1(sys.NR.close, @intCast(fd));
        completeInline(ov, pal_abi.ERROR_NOENT, 0, 0);
        return;
    }

    const s = statePtr();
    const op = allocOp(s) orelse {
        _ = sys.sys1(sys.NR.close, @intCast(fd));
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .dns_lookup;
    op.guest_ov = ov;
    op.fd = fd;
    op.buf = out;
    op.len = cap;
    op.deadline_ns = monotonicNs() + 2_000_000_000;
}
fn openSystemCaBundle(s: *PalState, out_buf: []u8) ?[]const u8 {
    if (readSystemCaFile("/etc/ssl/certs/ca-certificates.crt", out_buf)) |data| return data;
    if (readSystemCaFile("/etc/pki/tls/certs/ca-bundle.crt", out_buf)) |data| return data;
    if (readSystemCaFile("/etc/ssl/cert.pem", out_buf)) |data| return data;
    if (envLookup(s, "PREFIX")) |prefix| {
        var path_buf: [512]u8 = undefined;
        if (fmtZ(&path_buf, "{s}/etc/tls/cert.pem", .{prefix})) |pz| {
            if (readSystemCaFile(pz, out_buf)) |data| return data;
        }
    }
    return null;
}

fn readSystemCaFile(path_z: [*:0]const u8, out_buf: []u8) ?[]const u8 {
    const fd_res = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(path_z), sys.O_RDONLY, 0);
    if (sys.isErr(fd_res) != null) return null;
    const fd: usize = fd_res;
    defer _ = sys.sys1(sys.NR.close, fd);
    var total: usize = 0;
    while (total < out_buf.len) {
        const n_res = sys.sys3(sys.NR.read, fd, @intFromPtr(out_buf.ptr + total), out_buf.len - total);
        if (sys.isErr(n_res) != null) return null;
        if (n_res == 0) break;
        total += n_res;
    }
    return if (total > 0) out_buf[0..total] else null;
}

fn isPlausibleDerSequence(der: []const u8) bool {
    if (der.len < 4 or der[0] != 0x30) return false;
    const b1 = der[1];
    if (b1 < 0x80) {
        return 2 + @as(usize, b1) == der.len;
    }
    const len_bytes = b1 & 0x7f;
    if (len_bytes == 0 or len_bytes > 4 or 2 + len_bytes > der.len) return false;
    var body_len: usize = 0;
    for (0..len_bytes) |i| {
        body_len = (body_len << 8) | der[2 + i];
    }
    return 2 + len_bytes + body_len == der.len;
}

fn verifyCertChainBytes(s: *PalState, name: []const u8, payload: []const u8) u32 {
    if (name.len == 0 or name.len > 255 or payload.len < 4) return pal_abi.ERROR_INVALID;
    const count = std.mem.readInt(u32, payload[0..4], .little);
    if (count == 0 or 4 + @as(usize, count) * 4 > payload.len) return pal_abi.ERROR_INVALID;

    var off: usize = 4 + @as(usize, count) * 4;
    var parsed_buf: [16]std.crypto.Certificate.Parsed = undefined;
    var cert_count: usize = 0;
    for (0..count) |i| {
        const len_off = 4 + i * 4;
        const cert_len = std.mem.readInt(u32, payload[len_off..][0..4], .little);
        if (cert_len == 0 or off + cert_len > payload.len) return pal_abi.ERROR_INVALID;
        const der = payload[off..][0..cert_len];
        if (!isPlausibleDerSequence(der)) return pal_abi.ERROR_INVALID;
        const cert = std.crypto.Certificate{ .buffer = der, .index = 0 };
        const parsed = cert.parse() catch return pal_abi.ERROR_INVALID;
        if (cert_count < parsed_buf.len) {
            parsed_buf[cert_count] = parsed;
            cert_count += 1;
        }
        off += cert_len;
    }

    // Allocate a temporary mmap scratch buffer for CA PEM + Bundle so PalState stays small.
    const scratch_len: usize = 2 * 1024 * 1024;
    const mres = sys.sys6(sys.NR.mmap, 0, scratch_len, sys.PROT_READ | sys.PROT_WRITE, sys.MAP_PRIVATE | sys.MAP_ANONYMOUS, @bitCast(@as(isize, -1)), 0);
    if (sys.isErr(mres) != null) return pal_abi.ERROR_NOMEM;
    const scratch_ptr: [*]u8 = @ptrFromInt(mres);
    defer _ = sys.sys2(sys.NR.munmap, mres, scratch_len);

    const ca_region = scratch_ptr[0 .. 1024 * 1024];
    const arena_region = scratch_ptr[1024 * 1024 .. scratch_len];
    const ca_pem = openSystemCaBundle(s, ca_region) orelse return pal_abi.ERROR_NOENT;

    var fba = std.heap.FixedBufferAllocator.init(arena_region);
    const alloc = fba.allocator();

    var bundle: std.crypto.Certificate.Bundle = .empty;
    defer bundle.deinit(alloc);

    var it = std.mem.splitSequence(u8, ca_pem, "-----BEGIN CERTIFICATE-----");
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
        bundle.bytes.ensureUnusedCapacity(alloc, decoded_len) catch return pal_abi.ERROR_NOMEM;
        const dst = bundle.bytes.unusedCapacitySlice()[0..decoded_len];
        std.base64.standard.Decoder.decode(dst, compact_buf[0..compact_len]) catch continue;
        bundle.bytes.items.len += decoded_len;
        bundle.parseCert(alloc, @intCast(der_start), @intCast(abiGetTime() / 1_000_000_000)) catch continue;
    }

    const now_sec: i64 = @intCast(abiGetTime() / 1_000_000_000);
    var leaf = parsed_buf[0];
    leaf.verifyHostName(name) catch return pal_abi.ERROR_ACCES;

    // Verify chain from leaf up to any trusted root in bundle.
    var cur = leaf;
    var depth: usize = 0;
    while (depth < cert_count) : (depth += 1) {
        if (bundle.verify(cur, now_sec)) |_| {
            return 0;
        } else |_| {}

        var found_issuer = false;
        for (parsed_buf[1..cert_count]) |cand| {
            if (std.mem.eql(u8, cur.issuer(), cand.subject())) {
                cur.verify(cand, now_sec) catch return pal_abi.ERROR_ACCES;
                cur = cand;
                found_issuer = true;
                break;
            }
        }
        if (!found_issuer) break;
    }
    return pal_abi.ERROR_ACCES;
}

fn abiNetCertVerify(ov: *pal_abi.Overlapped, name_ptr: [*]const u8, name_len: usize, chain_ptr: [*]const u8, chain_len: usize) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .worker_op;
    op.work_kind = .net_cert_verify;
    op.guest_ov = ov;
    op.buf = @constCast(name_ptr);
    op.len = name_len;
    op.buf2 = chain_ptr;
    op.len2 = chain_len;
    submitOrRunInline(s, op);
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
    var pty_master_fd: i32 = -1;
    var pty_slave_fd: i32 = -1;
    var pty_enabled = false;
    var pty_real = false;
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
                if (!pty_enabled) {
                    pty_enabled = true;
                    const ptmx_p = "/dev/ptmx";
                    const mres = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(ptmx_p.ptr), sys.O_RDWR | sys.O_NONBLOCK, 0);
                    if (sys.isErr(mres) == null) {
                        const mfd: i32 = @intCast(mres);
                        var unlock: i32 = 0;
                        var ptn: i32 = 0;
                        if (sys.isErr(sys.sys3(sys.NR.ioctl, @intCast(mfd), sys.TIOCSPTLCK, @intFromPtr(&unlock))) == null and
                            sys.isErr(sys.sys3(sys.NR.ioctl, @intCast(mfd), sys.TIOCGPTN, @intFromPtr(&ptn))) == null)
                        {
                            var pts_buf: [64]u8 = undefined;
                            if (fmtZ(&pts_buf, "/dev/pts/{d}", .{ptn})) |pts_z| {
                                const sres = sys.sys4(sys.NR.openat, @bitCast(@as(isize, sys.AT_FDCWD)), @intFromPtr(pts_z.ptr), sys.O_RDWR, 0);
                                if (sys.isErr(sres) == null) {
                                    pty_master_fd = mfd;
                                    pty_slave_fd = @intCast(sres);
                                    pty_real = true;
                                    child_fds[0] = pty_slave_fd;
                                    child_fds[1] = pty_slave_fd;
                                    child_fds[2] = pty_slave_fd;
                                } else {
                                    _ = sys.sys1(sys.NR.close, @intCast(mfd));
                                }
                            } else {
                                _ = sys.sys1(sys.NR.close, @intCast(mfd));
                            }
                        } else {
                            _ = sys.sys1(sys.NR.close, @intCast(mfd));
                        }
                    }
                    if (!pty_real and
                        sys.isErr(sys.sys2(sys.NR.pipe2, @intFromPtr(&pty_in_fds), 0o2000000)) == null and
                        sys.isErr(sys.sys2(sys.NR.pipe2, @intFromPtr(&pty_out_fds), 0o2000000)) == null)
                    {
                        child_fds[0] = pty_in_fds[0];
                        child_fds[1] = pty_out_fds[1];
                        child_fds[2] = pty_out_fds[1];
                    }
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
            if (pty_real) {
                _ = sys.sys1(sys.NR.close, @intCast(pty_master_fd));
                _ = sys.sys0(sys.NR.setsid);
                _ = sys.sys3(sys.NR.ioctl, @intCast(pty_slave_fd), sys.TIOCSCTTY, 0);
            } else {
                _ = sys.sys1(sys.NR.close, @intCast(pty_in_fds[1]));
                _ = sys.sys1(sys.NR.close, @intCast(pty_out_fds[0]));
            }
        }
        for (0..3) |idx| {
            const target: i32 = @intCast(idx);
            if (child_fds[idx] != target) {
                _ = sys.sys3(sys.NR.dup3, @intCast(child_fds[idx]), @intCast(target), 0);
            }
        }
        if (pty_real and pty_slave_fd > 2) {
            _ = sys.sys1(sys.NR.close, @intCast(pty_slave_fd));
        }
        if (has_cwd) {
            _ = sys.sys1(sys.NR.chdir, @intFromPtr(&cwd_buf));
        }
        const envp = s.host.envp;
        _ = sys.sys3(sys.NR.execve, @intFromPtr(argv_ptrs[0].?), @intFromPtr(&argv_ptrs), @intFromPtr(envp));
        if (std.mem.indexOfScalar(u8, prog, '/') == null) {
            var fallback: [4096]u8 = undefined;
            if (envLookup(s, "PATH")) |path_env| {
                var pit = std.mem.splitScalar(u8, path_env, ':');
                while (pit.next()) |dir| {
                    if (dir.len == 0) continue;
                    if (fmtZ(&fallback, "{s}/{s}", .{ dir, prog })) |cand| {
                        argv_ptrs[0] = cand.ptr;
                        _ = sys.sys3(sys.NR.execve, @intFromPtr(argv_ptrs[0].?), @intFromPtr(&argv_ptrs), @intFromPtr(envp));
                    }
                }
            }
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
        if (pty_real) {
            _ = sys.sys1(sys.NR.close, @intCast(pty_slave_fd));
        } else {
            _ = sys.sys1(sys.NR.close, @intCast(pty_in_fds[0]));
            _ = sys.sys1(sys.NR.close, @intCast(pty_out_fds[1]));
        }
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
        if (pty_real) {
            pty_he.fd = pty_master_fd;
            pty_he.is_pty = true;
            pty_he.pty_read_fd = pty_master_fd;
            pty_he.pty_write_fd = pty_master_fd;
        } else {
            pty_he.fd = pty_out_fds[0];
            pty_he.is_pty = true;
            pty_he.pty_read_fd = pty_out_fds[0];
            pty_he.pty_write_fd = pty_in_fds[1];
        }
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
        in_he.is_pipe = true;
        setNonBlock(in_fds[1]);
        const out_he = allocHandle(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        out_he.fd = out_fds[0];
        out_he.is_pipe = true;
        setNonBlock(out_fds[0]);
        const err_he = allocHandle(s) orelse {
            completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
            return;
        };
        err_he.fd = err_fds[0];
        err_he.is_pipe = true;
        setNonBlock(err_fds[0]);
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

fn setNonBlock(fd: i32) void {
    const fl = sys.sys3(sys.NR.fcntl, @intCast(fd), 3, 0);
    if (sys.isErr(fl) == null) {
        _ = sys.sys3(sys.NR.fcntl, @intCast(fd), 4, fl | sys.O_NONBLOCK);
    }
}

fn abiProcessPipe(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    var fds: [2]i32 = .{ -1, -1 };
    const res = sys.sys2(sys.NR.pipe2, @intFromPtr(&fds), 0o2000000 | sys.O_NONBLOCK); // O_CLOEXEC | O_NONBLOCK
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
    r_he.is_pipe = true;
    w_he.fd = fds[1];
    w_he.is_pipe = true;
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
fn ensureSignalFd(s: *PalState, signum: u32) bool {
    if (signum == 0 or signum > 64) return false;
    s.signal_mask |= @as(u64, 1) << @intCast(signum - 1);
    var mask: u64 = s.signal_mask;
    if (sys.isErr(sys.sys4(sys.NR.rt_sigprocmask, sys.SIG_BLOCK, @intFromPtr(&mask), 0, @sizeOf(u64))) != null) {
        return false;
    }
    const sfd_res = sys.sys4(sys.NR.signalfd4, @bitCast(@as(isize, s.signal_fd)), @intFromPtr(&mask), @sizeOf(u64), sys.SFD_NONBLOCK | sys.SFD_CLOEXEC);
    if (sys.isErr(sfd_res) != null) return false;
    s.signal_fd = @intCast(sfd_res);
    return true;
}

fn dispatchSignal(s: *PalState, signum: u32) void {
    for (&s.ops) |*op| {
        if (op.used and op.kind == .signal_wait and op.signum == signum) {
            const gov = op.guest_ov.?;
            freeOp(s, op);
            completeInline(gov, 0, 0, 0);
        }
    }
}

fn abiSignalWait(ov: *pal_abi.Overlapped, signum: u32) callconv(pal_abi.pal_cc) void {
    if (signum == 0 or signum > 64) {
        completeInline(ov, pal_abi.ERROR_INVALID, 0, 0);
        return;
    }
    const s = statePtr();
    if (!builtin.is_test) {
        if (!ensureSignalFd(s, signum)) {
            completeInline(ov, pal_abi.ERROR_IO, 0, 0);
            return;
        }
    }
    for (&s.ops) |*op| {
        if (op.used and op.kind == .signal_wait and op.signum == signum) {
            const prev_ov = op.guest_ov.?;
            freeOp(s, op);
            completeInline(prev_ov, pal_abi.ERROR_CANCELED, 0, 0);
            break;
        }
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .signal_wait;
    op.guest_ov = ov;
    op.signum = signum;
}

fn resolveTtyFd(s: *PalState, handle: u64) ?i32 {
    if (handle <= 2) return @intCast(handle);
    const he = findHandle(s, handle) orelse return null;
    if (he.is_pty) return he.pty_read_fd;
    return he.fd;
}

fn abiTtySetMode(handle: u64, raw_mode: u32) callconv(pal_abi.pal_cc) void {
    const s = statePtr();
    const fd = resolveTtyFd(s, handle) orelse return;
    var t: sys.Termios = .{};
    if (sys.isErr(sys.sys3(sys.NR.ioctl, @intCast(fd), sys.TCGETS, @intFromPtr(&t))) != null) return;
    if (fd == 0) {
        if (!s.has_saved_termios) {
            s.saved_termios = t;
            s.has_saved_termios = true;
        }
        if (raw_mode != 0) {
            // Clear ICANON(2)|ECHO(8)|ISIG(1)|IEXTEN(0x8000), IXON(0x400)|ICRNL(0x100)
            t.c_lflag &= ~@as(u32, 0x800b);
            t.c_iflag &= ~@as(u32, 0x0500);
            t.c_cc[6] = 1; // VMIN
            t.c_cc[5] = 0; // VTIME
            _ = sys.sys3(sys.NR.ioctl, @intCast(fd), sys.TCSETS, @intFromPtr(&t));
        } else if (s.has_saved_termios) {
            _ = sys.sys3(sys.NR.ioctl, @intCast(fd), sys.TCSETS, @intFromPtr(&s.saved_termios));
        }
    }
}

fn abiTtyGetSize(handle: u64) callconv(pal_abi.pal_cc) u32 {
    const s = statePtr();
    const fd = resolveTtyFd(s, handle) orelse return (80 << 16) | 24;
    var ws: sys.Winsize = .{};
    if (sys.isErr(sys.sys3(sys.NR.ioctl, @intCast(fd), sys.TIOCGWINSZ, @intFromPtr(&ws))) != null) {
        return (80 << 16) | 24;
    }
    const cols: u32 = if (ws.ws_col > 0) ws.ws_col else 80;
    const rows: u32 = if (ws.ws_row > 0) ws.ws_row else 24;
    return (cols << 16) | (rows & 0xffff);
}

fn abiTtySetSize(handle: u64, cols: u32, rows: u32) callconv(pal_abi.pal_cc) u32 {
    if (cols == 0 or rows == 0 or cols > 32767 or rows > 32767) {
        return pal_abi.ERROR_INVALID;
    }
    const s = statePtr();
    const fd = resolveTtyFd(s, handle) orelse return pal_abi.ERROR_BADF;
    var ws: sys.Winsize = .{
        .ws_row = @intCast(rows),
        .ws_col = @intCast(cols),
    };
    const res = sys.sys3(sys.NR.ioctl, @intCast(fd), sys.TIOCSWINSZ, @intFromPtr(&ws));
    if (sys.isErr(res)) |e| return sys.mapLinuxErrno(e);
    return 0;
}

fn abiFdIsatty(handle: u32) callconv(pal_abi.pal_cc) u32 {
    const s = statePtr();
    const fd = resolveTtyFd(s, handle) orelse return 0;
    var t: sys.Termios = .{};
    return if (sys.isErr(sys.sys3(sys.NR.ioctl, @intCast(fd), sys.TCGETS, @intFromPtr(&t))) == null) 1 else 0;
}

const SYSTEM_INFO_POLL_NS: u64 = 250 * 1_000_000;

fn armSystemInfoWait(s: *PalState, ov: *pal_abi.Overlapped) void {
    var has_waiter = false;
    for (&s.ops) |*existing| {
        if (existing.used and existing.kind == .system_info_wait) {
            has_waiter = true;
            break;
        }
    }
    if (!has_waiter or !s.has_system_info_hash) {
        s.system_info_hash = captureSystemContextHash(s);
        s.has_system_info_hash = true;
    }
    const op = allocOp(s) orelse {
        completeInline(ov, pal_abi.ERROR_NOMEM, 0, 0);
        return;
    };
    op.kind = .system_info_wait;
    op.guest_ov = ov;
    op.deadline_ns = monotonicNs() + SYSTEM_INFO_POLL_NS;
}

fn processSystemInfoTimers(s: *PalState, now: u64) bool {
    var due = false;
    for (&s.ops) |*op| {
        if (op.used and op.kind == .system_info_wait and op.deadline_ns <= now) {
            due = true;
            break;
        }
    }
    if (!due) return false;
    if (pollSystemContextChanged(s)) {
        for (&s.ops) |*op| {
            if (op.used and op.kind == .system_info_wait) {
                const gov = op.guest_ov.?;
                freeOp(s, op);
                completeInline(gov, 0, 0, 0);
            }
        }
        return true;
    }
    for (&s.ops) |*op| {
        if (op.used and op.kind == .system_info_wait and op.deadline_ns <= now) {
            op.deadline_ns = now + SYSTEM_INFO_POLL_NS;
        }
    }
    return false;
}

fn abiSystemInfoWait(ov: *pal_abi.Overlapped) callconv(pal_abi.pal_cc) void {
    armSystemInfoWait(statePtr(), ov);
}

// ─── PAL Entry & poll(2) Event Loop ──────────────────────────────────────────

pub fn unixPlatformMain() callconv(.c) i32 {
    const s = statePtr();
    s.* = .{};
    s.host = hostPtr().*;

    const efd_res = sys.sys2(sys.NR.eventfd2, 0, sys.EFD_NONBLOCK | sys.EFD_CLOEXEC);
    if (sys.isErr(efd_res) == null) {
        s.event_fd = @intCast(efd_res);
    }
    const pool_bytes = pageAlign(@sizeOf(worker_pool.LinuxWorkerPool));
    const pool_res = sys.sys6(sys.NR.mmap, 0, pool_bytes, sys.PROT_READ | sys.PROT_WRITE, sys.MAP_PRIVATE | sys.MAP_ANONYMOUS, @bitCast(@as(isize, -1)), 0);
    if (sys.isErr(pool_res) == null) {
        const pool: *worker_pool.LinuxWorkerPool = @ptrFromInt(pool_res);
        pool.* = worker_pool.LinuxWorkerPool.init(s.event_fd, s, executeWorkItem);
        s.pool_ptr = pool;
    }

    const runtime_ptr = s.host.runtime_slice;
    const runtime_len = s.host.runtime_slice_size;
    if (runtime_len == 0 or s.host.wasm_bytes == null or s.host.wasm_len == 0) return 12;

    const code_aligned = pageAlign(runtime_len);
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
    @memcpy(code_ptr[0..runtime_len], runtime_ptr[0..runtime_len]);
    _ = sys.sys3(sys.NR.mprotect, @intFromPtr(code_ptr), code_aligned, sys.PROT_READ | sys.PROT_EXEC);
    abiFlushIcache(code_ptr, runtime_len);

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
        var fired = processSystemInfoTimers(s, now);
        var signal_pfd_idx: ?usize = null;
        var event_pfd_idx: ?usize = null;

        // Harvest any worker_op items already completed before sleeping.
        for (&s.ops) |*op| {
            if (op.used and op.kind == .worker_op and @atomicLoad(u32, &op.worker_done, .acquire) != 0) {
                finishWorkerOp(s, op);
                fired = true;
            }
        }

        if (s.event_fd >= 0 and pfd_count < pfds.len) {
            var has_worker = false;
            for (&s.ops) |*op| {
                if (op.used and op.kind == .worker_op) {
                    has_worker = true;
                    break;
                }
            }
            if (has_worker) {
                event_pfd_idx = pfd_count;
                pfds[pfd_count] = .{ .fd = s.event_fd, .events = sys.POLLIN, .revents = 0 };
                op_idx[pfd_count] = 0;
                pfd_count += 1;
            }
        }

        if (s.signal_fd >= 0 and pfd_count < pfds.len) {
            var has_sig_waiter = false;
            for (&s.ops) |*op| {
                if (op.used and op.kind == .signal_wait) {
                    has_sig_waiter = true;
                    break;
                }
            }
            if (has_sig_waiter) {
                signal_pfd_idx = pfd_count;
                pfds[pfd_count] = .{ .fd = s.signal_fd, .events = sys.POLLIN, .revents = 0 };
                op_idx[pfd_count] = 0;
                pfd_count += 1;
            }
        }

        for (&s.ops, 0..) |*op, idx| {
            if (!op.used) continue;
            if (op.kind == .timer or op.kind == .system_info_wait) {
                if (op.kind == .timer and op.deadline_ns <= now) {
                    const gov = op.guest_ov.?;
                    freeOp(s, op);
                    completeInline(gov, 0, 0, 0);
                    fired = true;
                } else if (op.deadline_ns > now) {
                    const rem = op.deadline_ns - now;
                    if (min_wait_ns == null or rem < min_wait_ns.?) min_wait_ns = rem;
                }
            } else if (op.kind == .dns_lookup) {
                if (op.deadline_ns <= now) {
                    const gov = op.guest_ov.?;
                    if (op.fd >= 0) _ = sys.sys1(sys.NR.close, @intCast(op.fd));
                    freeOp(s, op);
                    completeInline(gov, pal_abi.ERROR_TIMEDOUT, 0, 0);
                    fired = true;
                } else {
                    const rem = op.deadline_ns - now;
                    if (min_wait_ns == null or rem < min_wait_ns.?) min_wait_ns = rem;
                    if (pfd_count < pfds.len) {
                        pfds[pfd_count] = .{ .fd = op.fd, .events = sys.POLLIN, .revents = 0 };
                        op_idx[pfd_count] = idx;
                        pfd_count += 1;
                    }
                }
            } else if ((op.kind == .read_fd or op.kind == .accept or op.kind == .udp_read) and pfd_count < pfds.len) {
                pfds[pfd_count] = .{ .fd = op.fd, .events = sys.POLLIN, .revents = 0 };
                op_idx[pfd_count] = idx;
                pfd_count += 1;
            } else if ((op.kind == .write_fd or op.kind == .net_connect) and pfd_count < pfds.len) {
                pfds[pfd_count] = .{ .fd = op.fd, .events = sys.POLLOUT, .revents = 0 };
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
        if (fired and pfd_count == 0) continue;

        var ts: sys.Timespec = undefined;
        const ts_ptr: usize = if (fired) blk: {
            ts = .{ .sec = 0, .nsec = 0 };
            break :blk @intFromPtr(&ts);
        } else if (min_wait_ns) |w| blk: {
            ts = .{ .sec = @intCast(w / 1_000_000_000), .nsec = @intCast(w % 1_000_000_000) };
            break :blk @intFromPtr(&ts);
        } else 0;

        _ = sys.sys4(sys.NR.ppoll, @intFromPtr(&pfds), pfd_count, ts_ptr, 0);

        for (0..pfd_count) |i| {
            if (pfds[i].revents != 0) {
                if (event_pfd_idx != null and i == event_pfd_idx.?) {
                    var cnt: u64 = 0;
                    _ = sys.sys3(sys.NR.read, @intCast(s.event_fd), @intFromPtr(&cnt), @sizeOf(u64));
                    for (&s.ops) |*wop| {
                        if (wop.used and wop.kind == .worker_op and @atomicLoad(u32, &wop.worker_done, .acquire) != 0) {
                            finishWorkerOp(s, wop);
                        }
                    }
                    continue;
                }
                if (signal_pfd_idx != null and i == signal_pfd_idx.?) {
                    var si: sys.SignalfdSiginfo = .{};
                    while (true) {
                        const rres = sys.sys3(sys.NR.read, @intCast(s.signal_fd), @intFromPtr(&si), @sizeOf(sys.SignalfdSiginfo));
                        if (sys.isErr(rres) != null or rres < @sizeOf(sys.SignalfdSiginfo)) break;
                        dispatchSignal(s, si.ssi_signo);
                    }
                    continue;
                }
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
                } else if (op.kind == .write_fd) {
                    const res = if (op.is_udp)
                        sys.sys6(sys.NR.sendto, @intCast(op.fd), @intFromPtr(op.buf.?), op.len, 0, 0, 0)
                    else
                        sys.sys3(sys.NR.write, @intCast(op.fd), @intFromPtr(op.buf.?), op.len);
                    freeOp(s, op);
                    if (sys.isErr(res)) |e| {
                        completeInline(gov, sys.mapLinuxErrno(e), 0, 0);
                    } else {
                        completeInline(gov, 0, 0, res);
                    }
                } else if (op.kind == .net_connect) {
                    var so_err: i32 = 0;
                    var so_len: u32 = @sizeOf(i32);
                    const sock_fd = op.fd;
                    const port_val = op.arg_u32_a;
                    const udp_val = op.is_udp;
                    const gres = sys.sys6(sys.NR.getsockopt, @intCast(sock_fd), sys.SOL_SOCKET, sys.SO_ERROR, @intFromPtr(&so_err), @intFromPtr(&so_len), 0);
                    freeOp(s, op);
                    if (sys.isErr(gres)) |e| {
                        _ = sys.sys1(sys.NR.close, @intCast(sock_fd));
                        completeInline(gov, sys.mapLinuxErrno(e), 0, 0);
                    } else if (so_err != 0) {
                        _ = sys.sys1(sys.NR.close, @intCast(sock_fd));
                        completeInline(gov, sys.mapLinuxErrno(@intCast(@abs(so_err))), 0, 0);
                    } else if (allocHandle(s)) |nhe| {
                        nhe.fd = sock_fd;
                        nhe.is_socket = true;
                        nhe.is_udp = udp_val;
                        completeInline(gov, 0, port_val, nhe.token);
                    } else {
                        _ = sys.sys1(sys.NR.close, @intCast(sock_fd));
                        completeInline(gov, pal_abi.ERROR_NOMEM, 0, 0);
                    }
                } else if (op.kind == .dns_lookup) {
                    var resp: [512]u8 = undefined;
                    const rlen_res = sys.sys3(sys.NR.read, @intCast(op.fd), @intFromPtr(&resp), resp.len);
                    _ = sys.sys1(sys.NR.close, @intCast(op.fd));
                    const out_ptr = op.buf.?;
                    const out_cap = op.len;
                    freeOp(s, op);
                    if (sys.isErr(rlen_res) != null) {
                        completeInline(gov, pal_abi.ERROR_NOENT, 0, 0);
                    } else {
                        const parsed = parseDnsResponse(resp[0..rlen_res], out_ptr, out_cap);
                        completeInline(gov, parsed.err, 0, parsed.total);
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

test "linux PAL errno mapping" {
    try std.testing.expectEqual(pal_abi.ERROR_NOENT, sys.mapLinuxErrno(2));
    try std.testing.expectEqual(pal_abi.ERROR_NOTTY, sys.mapLinuxErrno(25));
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, sys.mapLinuxErrno(125));
}

test "linux PAL signal_wait validates, replaces, dispatches, and cancels" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var bad_ov: pal_abi.Overlapped = .{};
    abiSignalWait(&bad_ov, 0);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, bad_ov.flags);
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, bad_ov.@"error");

    var ov1: pal_abi.Overlapped = .{};
    var ov_winch: pal_abi.Overlapped = .{};
    abiSignalWait(&ov1, 2);
    abiSignalWait(&ov_winch, 28);
    try std.testing.expectEqual(@as(usize, 2), state.pending_count);

    var ov2: pal_abi.Overlapped = .{};
    abiSignalWait(&ov2, 2);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov1.flags);
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, ov1.@"error");

    dispatchSignal(&state, 2);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov2.flags);
    try std.testing.expectEqual(@as(u32, 0), ov2.@"error");
    try std.testing.expectEqual(@as(u32, 0), ov_winch.flags);

    abiCancel(&ov_winch);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov_winch.flags);
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, ov_winch.@"error");
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
}

test "linux PAL system_info_wait polls, completes on context change, and cancels" {
    const H = struct {
        var loc: []const u8 = "en_US.UTF-8";
        var app: []const u8 = "light";
        fn fill(info: *pal_abi.AbiPlatformInfo) void {
            copyFixed(&info.locale, loc);
            copyFixed(&info.appearance, app);
        }
    };
    H.loc = "en_US.UTF-8";
    H.app = "light";
    test_linux_sys_ctx_override = H.fill;
    defer test_linux_sys_ctx_override = null;

    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    var ov1: pal_abi.Overlapped = .{};
    var ov2: pal_abi.Overlapped = .{};
    abiSystemInfoWait(&ov1);
    abiSystemInfoWait(&ov2);
    try std.testing.expectEqual(@as(usize, 2), state.pending_count);

    const future_unchanged = monotonicNs() + SYSTEM_INFO_POLL_NS + 1_000_000;
    try std.testing.expect(!processSystemInfoTimers(&state, future_unchanged));
    try std.testing.expectEqual(@as(u32, 0), ov1.flags);
    try std.testing.expectEqual(@as(usize, 2), state.pending_count);

    const prev_ver = state.system_info_version;
    H.app = "dark";
    try std.testing.expect(processSystemInfoTimers(&state, future_unchanged + SYSTEM_INFO_POLL_NS + 1_000_000));
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov1.flags);
    try std.testing.expectEqual(@as(u32, 0), ov1.@"error");
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov2.flags);
    try std.testing.expectEqual(@as(u32, 0), ov2.@"error");
    try std.testing.expectEqual(prev_ver + 1, state.system_info_version);

    var ov3: pal_abi.Overlapped = .{};
    abiSystemInfoWait(&ov3);
    abiCancel(&ov3);
    try std.testing.expectEqual(pal_abi.FLAG_COMPLETED, ov3.flags);
    try std.testing.expectEqual(pal_abi.ERROR_CANCELED, ov3.@"error");
    try std.testing.expectEqual(@as(usize, 0), state.pending_count);
}

test "linux PAL tty_set_size validates bounds and unknown handles" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    try std.testing.expectEqual(pal_abi.ERROR_INVALID, abiTtySetSize(1, 0, 24));
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, abiTtySetSize(1, 80, 0));
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, abiTtySetSize(1, 32768, 24));
    try std.testing.expectEqual(pal_abi.ERROR_BADF, abiTtySetSize(99, 80, 24));
}

test "linux PAL net_cert_verify rejects malformed DER and empty hostnames" {
    var state: PalState = .{};
    test_state = &state;
    defer test_state = null;

    try std.testing.expectEqual(pal_abi.ERROR_INVALID, verifyCertChainBytes(&state, "", &.{ 1, 0, 0, 0, 1, 0, 0, 0, 0x30 }));
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, verifyCertChainBytes(&state, "example.com", &.{ 0, 0, 0, 0 }));
    try std.testing.expectEqual(pal_abi.ERROR_INVALID, verifyCertChainBytes(&state, "example.com", &.{ 1, 0, 0, 0, 4, 0, 0, 0, 0x01, 0x02, 0x03, 0x04 }));
}

test "linux PAL resolv.conf parser extracts IPv4 nameservers" {
    const conf = "# comment\nsearch local\nnameserver 10.0.0.2\nnameserver 1.1.1.1\n";
    const ip = parseNameserverFromText(conf).?;
    try std.testing.expectEqualSlices(u8, &.{ 10, 0, 0, 2 }, &ip);
}

test "linux PAL net_lookup encodes [totalSize][count][families][ips] wire format" {
    var out: [64]u8 = @splat(0);
    const fams = [_]u8{ 4, 4 };
    const ips = [_]u8{ 93, 184, 216, 34, 93, 184, 216, 35 };
    const total = writeNetLookupResult(&out, out.len, &fams, &ips);
    try std.testing.expectEqual(@as(u32, 18), total);
    try std.testing.expectEqual(@as(u32, 18), std.mem.readInt(u32, out[0..4], .little));
    try std.testing.expectEqual(@as(u32, 2), std.mem.readInt(u32, out[4..8], .little));
    try std.testing.expectEqualSlices(u8, &fams, out[8..10]);
    try std.testing.expectEqualSlices(u8, &ips, out[10..18]);
}

test "linux PAL DNS response parser handles multi-record A responses" {
    // Minimal DNS response: 1 question ("a.b"), 2 A answers (1.2.3.4, 5.6.7.8)
    const pkt = [_]u8{
        0x12, 0x34, 0x81, 0x80, 0x00, 0x01, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00,
        0x01, 'a',  0x01, 'b',  0x00, 0x00, 0x01, 0x00, 0x01,
        0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04, 1, 2, 3, 4,
        0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04, 5, 6, 7, 8,
    };
    var out: [64]u8 = @splat(0);
    const parsed = parseDnsResponse(&pkt, &out, out.len);
    try std.testing.expectEqual(@as(u32, 0), parsed.err);
    try std.testing.expectEqual(@as(u32, 18), parsed.total);
    try std.testing.expectEqual(@as(u32, 2), std.mem.readInt(u32, out[4..8], .little));
    try std.testing.expectEqualSlices(u8, &.{ 4, 4 }, out[8..10]);
    try std.testing.expectEqualSlices(u8, &.{ 1, 2, 3, 4, 5, 6, 7, 8 }, out[10..18]);
}
