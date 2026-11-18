const builtin = @import("builtin");
const std = @import("std");

pub const pal_cc: std.builtin.CallingConvention = switch (builtin.cpu.arch) {
    .x86_64 => .{ .x86_64_win = .{} },
    .aarch64 => .c,
    else => @compileError("Unsupported CPU architecture for Rusticated PAL ABI"),
};

pub const PREFIX_ABI_OFFSET: usize = 0x20000;
pub const PREFIX_GLOBALS_OFFSET: usize = 0x10000;

pub const FLAG_COMPLETED: u32 = 1 << 0;
pub const READ_FLAG_POSITIONAL: u32 = 1 << 24;
pub const WRITE_FLAG_POSITIONAL: u32 = 1 << 25;

pub const MEM_READWRITE: u32 = 1;
pub const MEM_CODE_JIT_RW: u32 = 2;
pub const MEM_CODE_EXEC_RX: u32 = 3;

pub const ERROR_ACCES: u32 = 2;
pub const ERROR_AGAIN: u32 = 6;
pub const ERROR_BADF: u32 = 8;
pub const ERROR_CANCELED: u32 = 11;
pub const ERROR_CONNREFUSED: u32 = 14;
pub const ERROR_EXIST: u32 = 20;
pub const ERROR_INVALID: u32 = 28;
pub const ERROR_IO: u32 = 29;
pub const ERROR_ISDIR: u32 = 31;
pub const ERROR_NOENT: u32 = 44;
pub const ERROR_NOMEM: u32 = 48;
pub const ERROR_UNSUPPORTED: u32 = 52;
pub const ERROR_NOTDIR: u32 = 54;
pub const ERROR_OVERFLOW: u32 = 61;
pub const ERROR_PERM: u32 = 63;
pub const ERROR_PIPE: u32 = 64;
pub const ERROR_SPIPE: u32 = 70;
pub const ERROR_TIMEDOUT: u32 = 73;

pub const OS_KIND_WINDOWS: u16 = 1;
pub const OS_KIND_LINUX: u16 = 2;
pub const OS_KIND_DARWIN: u16 = 3;

pub const CPU_TYPE_X86_64: u16 = 1;
pub const CPU_TYPE_AARCH64: u16 = 2;

pub const STAT_KIND_UNKNOWN: u32 = 0;
pub const STAT_KIND_FILE: u32 = 1;
pub const STAT_KIND_DIR: u32 = 2;
pub const STAT_KIND_SYMLINK: u32 = 3;
pub const STAT_KIND_CHAR: u32 = 4;
pub const STAT_KIND_FIFO: u32 = 5;
pub const STAT_KIND_SOCKET: u32 = 6;

pub const OPEN_FLAG_CREATE: u32 = 0x40;
pub const OPEN_FLAG_EXCL: u32 = 0x80;
pub const OPEN_FLAG_TRUNC: u32 = 0x200;
pub const OPEN_FLAG_APPEND: u32 = 0x400;
pub const OPEN_FLAG_DIRECTORY: u32 = 0x2000;

pub const PATH_STAT_FLAG_NOFOLLOW: u32 = 1 << 0;
pub const PATH_REMOVE_FLAG_DIR: u32 = 1 << 0;
pub const PATH_SET_TIMES_FLAG_NOFOLLOW: u32 = 1 << 0;
pub const NET_OPEN_FLAG_LISTEN: u32 = 1 << 0;
pub const NET_OPEN_FLAG_IPV6: u32 = 1 << 1;

pub const AbiDirEntryHeader = extern struct {
    kind: u32,
    name_len: u32,
};

pub const AbiStat = extern struct {
    kind: u32 = 0,
    mode: u32 = 0,
    uid: u32 = 0,
    gid: u32 = 0,
    size: u64 = 0,
    mtime_ns: i64 = 0,
    atime_ns: i64 = 0,
    ctime_ns: i64 = 0,
    btime_ns: i64 = 0,
    nlink: u64 = 0,
    ino: u64 = 0,
    dev: u64 = 0,
    rdev: u64 = 0,
    blksize: i64 = 0,
    blocks: i64 = 0,
    valid_mask: u64 = 0,
};

pub const AbiMemorySlot = extern struct {
    kind: u8 = 0,
    flags: u8 = 0,
    _pad: [6]u8 = @splat(0),
    bytes: u64 = 0,
};

pub const AbiPlatformInfo = extern struct {
    flags: u32 = 0,
    path_separator: u8 = 0,
    path_list_separator: u8 = 0,
    os_kind: u16 = 0,
    os_version: [4]u16 = @splat(0),
    cpu_type: u16 = 0,
    cpu_bitness: u8 = 0,
    _pad0: u8 = 0,
    os_name: [64]u8 = @splat(0),
    wasm_platform: [64]u8 = @splat(0),
    wasm_version: [4]u16 = @splat(0),
    wasm_version_str: [64]u8 = @splat(0),
    rusticated_name: [64]u8 = @splat(0),
    rusticated_version: [4]u16 = @splat(0),
    rusticated_version_str: [64]u8 = @splat(0),
    build_version: [64]u8 = @splat(0),
    build_time: [64]u8 = @splat(0),
    build_platform: [64]u8 = @splat(0),
    user_home_dir: [64]u8 = @splat(0),
    temp_dir: [64]u8 = @splat(0),
    _pad1: [4]u8 = @splat(0),
    memory_slots: [16]AbiMemorySlot = @splat(.{}),
    system_context_version: u32 = 0,
    locale: [64]u8 = @splat(0),
    time_zone: [64]u8 = @splat(0),
    keyboard_layout_id: [128]u8 = @splat(0),
    appearance: [32]u8 = @splat(0),
    power_source: [32]u8 = @splat(0),
    _pad2: [4]u8 = @splat(0),
};

pub const Overlapped = extern struct {
    flags: u32 = 0,
    @"error": u32 = 0,
    continued: u64 = 0,
    result_ext: u64 = 0,
};

pub const ProcessSpawnOverlapped = extern struct {
    header: Overlapped = .{},
    child_handle: u64 = 0,
    pty_handle: u64 = 0,
    in_handle: u64 = 0,
    out_handle: u64 = 0,
    err_handle: u64 = 0,
};

pub const PalAbiTable = extern struct {
    wasm_bytes: ?[*]const u8,
    wasm_len: usize,

    alloc_mem: *const fn (usize, usize, u32) callconv(pal_cc) ?[*]u8,
    protect_mem: *const fn ([*]u8, usize, u32) callconv(pal_cc) i32,
    free_mem: *const fn ([*]u8, usize) callconv(pal_cc) void,
    flush_icache: *const fn ([*]const u8, usize) callconv(pal_cc) void,

    host_panic: *const fn ([*]const u8, usize) callconv(pal_cc) noreturn,
    rusticated_debug: *const fn (i32) callconv(pal_cc) void,
    process_exit: *const fn (i32) callconv(pal_cc) noreturn,

    get_time: *const fn () callconv(pal_cc) u64,
    get_random: *const fn ([*]u8, usize) callconv(pal_cc) void,
    get_args: *const fn ([*]u8, usize) callconv(pal_cc) u64,
    get_env: *const fn ([*]u8, usize) callconv(pal_cc) u64,
    get_cwd: *const fn ([*]u8, usize) callconv(pal_cc) u64,
    set_cwd: *const fn ([*]const u8, usize) callconv(pal_cc) u32,
    get_platform_info: *const fn ([*]u8, usize) callconv(pal_cc) u32,

    timer_set: *const fn (*Overlapped, u32) callconv(pal_cc) void,
    cancel: *const fn (*Overlapped) callconv(pal_cc) void,

    read: *const fn (*Overlapped, u64, [*]u8, usize) callconv(pal_cc) void,
    write: *const fn (*Overlapped, u64, [*]const u8, usize) callconv(pal_cc) void,
    seek: *const fn (*Overlapped, u64, i64, u32) callconv(pal_cc) void,
    handle_truncate: *const fn (*Overlapped, u64, i64) callconv(pal_cc) void,
    handle_close: *const fn (u64) callconv(pal_cc) void,

    path_open: *const fn (*Overlapped, [*]const u8, usize, u32) callconv(pal_cc) void,
    dir_read: *const fn (*Overlapped, u64, [*]u8, usize) callconv(pal_cc) void,
    path_stat: *const fn (*Overlapped, [*]const u8, usize, u32, [*]u8, usize) callconv(pal_cc) void,
    path_link: *const fn (*Overlapped, [*]const u8, usize, [*]const u8, usize, u32) callconv(pal_cc) void,
    path_symlink: *const fn (*Overlapped, [*]const u8, usize, [*]const u8, usize, u32) callconv(pal_cc) void,
    path_readlink: *const fn (*Overlapped, [*]const u8, usize, [*]u8, usize) callconv(pal_cc) void,
    path_chmod: *const fn (*Overlapped, [*]const u8, usize, u32) callconv(pal_cc) void,
    path_set_times: *const fn (*Overlapped, [*]const u8, usize, i64, u32, i64, u32, u32) callconv(pal_cc) void,
    path_remove: *const fn (*Overlapped, [*]const u8, usize, u32) callconv(pal_cc) void,
    path_mkdir: *const fn (*Overlapped, [*]const u8, usize, u32) callconv(pal_cc) void,
    path_rename: *const fn (*Overlapped, [*]const u8, usize, [*]const u8, usize) callconv(pal_cc) void,

    net_open: *const fn (*Overlapped, [*]const u8, usize, u32, u32) callconv(pal_cc) void,
    net_accept: *const fn (*Overlapped, u64) callconv(pal_cc) void,
    net_lookup: *const fn (*Overlapped, [*]const u8, usize, [*]u8, usize) callconv(pal_cc) void,
    net_cert_verify: *const fn (*Overlapped, [*]const u8, usize, [*]const u8, usize) callconv(pal_cc) void,

    process_spawn: *const fn (*Overlapped, [*]const u8, usize) callconv(pal_cc) void,
    process_pipe: *const fn (*Overlapped) callconv(pal_cc) void,
    process_wait: *const fn (*Overlapped, u64) callconv(pal_cc) void,
    process_signal: *const fn (u64, u32) callconv(pal_cc) void,
    signal_wait: *const fn (*Overlapped, u32) callconv(pal_cc) void,

    tty_set_mode: *const fn (u64, u32) callconv(pal_cc) void,
    tty_get_size: *const fn (u64) callconv(pal_cc) u32,
    tty_set_size: *const fn (u64, u32, u32) callconv(pal_cc) u32,
    fd_isatty: *const fn (u32) callconv(pal_cc) u32,
    system_info_wait: *const fn (*Overlapped) callconv(pal_cc) void,
};

comptime {
    if (@sizeOf(Overlapped) != 24) @compileError("Overlapped must be 24 bytes");
    if (@sizeOf(ProcessSpawnOverlapped) != 64) @compileError("ProcessSpawnOverlapped must be 64 bytes");
    if (@sizeOf(AbiStat) != 112) @compileError("AbiStat must be 112 bytes");
    if (@sizeOf(AbiMemorySlot) != 16) @compileError("AbiMemorySlot must be 16 bytes");
    if (@sizeOf(AbiPlatformInfo) != 1264) @compileError("AbiPlatformInfo must be 1264 bytes");
    if (@sizeOf(PalAbiTable) != 48 * 8) @compileError("PalAbiTable must be 48 slots (384 bytes)");
}

test "ABI struct sizes and offsets" {
    try std.testing.expectEqual(@as(usize, 24), @sizeOf(Overlapped));
    try std.testing.expectEqual(@as(usize, 64), @sizeOf(ProcessSpawnOverlapped));
    try std.testing.expectEqual(@as(usize, 112), @sizeOf(AbiStat));
    try std.testing.expectEqual(@as(usize, 1264), @sizeOf(AbiPlatformInfo));
    try std.testing.expectEqual(@as(usize, 384), @sizeOf(PalAbiTable));
    try std.testing.expectEqual(@as(usize, 0x178), @offsetOf(PalAbiTable, "system_info_wait"));
}
