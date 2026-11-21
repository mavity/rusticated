const std = @import("std");
const pal_abi = @import("pal_abi");

pub const EVFILT_READ: i16 = -1;
pub const EVFILT_WRITE: i16 = -2;
pub const EVFILT_SIGNAL: i16 = -6;

pub const EV_ADD: u16 = 0x0001;
pub const EV_DELETE: u16 = 0x0002;
pub const EV_ENABLE: u16 = 0x0004;
pub const EV_ONESHOT: u16 = 0x0010;
pub const EV_CLEAR: u16 = 0x0020;
pub const EV_EOF: u16 = 0x8000;

pub const AF_INET: c_int = 2;
pub const AF_INET6: c_int = 30;
pub const SOCK_STREAM: c_int = 1;
pub const SOCK_DGRAM: c_int = 2;
pub const SOL_SOCKET: c_int = 0xffff;
pub const SO_REUSEADDR: c_int = 0x0004;
pub const SO_ERROR: c_int = 0x1007;

pub const F_GETFL: c_int = 3;
pub const F_SETFL: c_int = 4;
pub const O_NONBLOCK: c_int = 0x0004;
pub const O_RDONLY: c_int = 0;
pub const O_WRONLY: c_int = 1;
pub const O_RDWR: c_int = 2;
pub const O_CREAT: c_int = 0x0200;
pub const O_TRUNC: c_int = 0x0400;
pub const O_EXCL: c_int = 0x0800;
pub const O_APPEND: c_int = 0x0008;
pub const O_NOFOLLOW: c_int = 0x0100;
pub const O_NOCTTY: c_int = 0x20000;
pub const O_DIRECTORY: c_int = 0x100000;

pub const AT_FDCWD: c_int = -2;
pub const AT_SYMLINK_NOFOLLOW: c_int = 0x0020;

pub const TIOCGWINSZ: c_ulong = 0x40087468;
pub const TIOCSWINSZ: c_ulong = 0x80087467;
pub const TIOCSCTTY: c_ulong = 0x20007461;

pub const WNOHANG: c_int = 1;

pub const Kevent = extern struct {
    ident: usize = 0,
    filter: i16 = 0,
    flags: u16 = 0,
    fflags: u32 = 0,
    data: isize = 0,
    udata: ?*anyopaque = null,
};

pub const Timespec = extern struct {
    tv_sec: isize = 0,
    tv_nsec: isize = 0,
};

pub const Timeval = extern struct {
    sec: i64 = 0,
    usec: i32 = 0,
    _pad: i32 = 0,
};

/// 64-bit Darwin struct stat ($INODE64 layout, 144 bytes).
pub const Stat = extern struct {
    st_dev: i32 = 0,
    st_mode: u16 = 0,
    st_nlink: u16 = 0,
    st_ino: u64 = 0,
    st_uid: u32 = 0,
    st_gid: u32 = 0,
    st_rdev: i32 = 0,
    _pad0: i32 = 0,
    st_atimespec: Timespec = .{},
    st_mtimespec: Timespec = .{},
    st_ctimespec: Timespec = .{},
    st_birthtimespec: Timespec = .{},
    st_size: i64 = 0,
    st_blocks: i64 = 0,
    st_blksize: i32 = 0,
    st_flags: u32 = 0,
    st_gen: u32 = 0,
    st_lspare: i32 = 0,
    st_qspare: [2]i64 = .{ 0, 0 },
};

/// 64-bit Darwin struct dirent ($INODE64 layout).
pub const Dirent = extern struct {
    d_ino: u64 = 0,
    d_seekoff: u64 = 0,
    d_reclen: u16 = 0,
    d_namlen: u16 = 0,
    d_type: u8 = 0,
    d_name: [1024]u8 = @splat(0),
};

pub const Termios = extern struct {
    c_iflag: u64 = 0,
    c_oflag: u64 = 0,
    c_cflag: u64 = 0,
    c_lflag: u64 = 0,
    c_cc: [20]u8 = @splat(0),
    _pad: [4]u8 = @splat(0),
    c_ispeed: u64 = 0,
    c_ospeed: u64 = 0,
};

pub const Winsize = extern struct {
    ws_row: u16 = 0,
    ws_col: u16 = 0,
    ws_xpixel: u16 = 0,
    ws_ypixel: u16 = 0,
};

pub const SockAddrIn = extern struct {
    sin_len: u8 = 16,
    sin_family: u8 = @intCast(AF_INET),
    sin_port: u16 = 0,
    sin_addr: [4]u8 = .{ 0, 0, 0, 0 },
    sin_zero: [8]u8 = @splat(0),
};

pub const SockAddrIn6 = extern struct {
    sin6_len: u8 = 28,
    sin6_family: u8 = @intCast(AF_INET6),
    sin6_port: u16 = 0,
    sin6_flowinfo: u32 = 0,
    sin6_addr: [16]u8 = @splat(0),
    sin6_scope_id: u32 = 0,
};

pub const AddrInfo = extern struct {
    ai_flags: c_int = 0,
    ai_family: c_int = 0,
    ai_socktype: c_int = 0,
    ai_protocol: c_int = 0,
    ai_addrlen: u32 = 0,
    _pad: u32 = 0,
    ai_canonname: ?[*:0]u8 = null,
    ai_addr: ?*anyopaque = null,
    ai_next: ?*AddrInfo = null,
};

pub fn mapErrno(e: i32) u32 {
    return switch (e) {
        0 => 0,
        1 => pal_abi.ERROR_ACCES, // EPERM
        2 => pal_abi.ERROR_NOENT, // ENOENT
        4 => pal_abi.ERROR_CANCELED, // EINTR
        5 => pal_abi.ERROR_IO, // EIO
        9 => pal_abi.ERROR_BADF, // EBADF
        12 => pal_abi.ERROR_NOMEM, // ENOMEM
        13 => pal_abi.ERROR_ACCES, // EACCES
        17 => pal_abi.ERROR_EXIST, // EEXIST
        20 => pal_abi.ERROR_NOTDIR, // ENOTDIR
        21 => pal_abi.ERROR_ISDIR, // EISDIR
        22 => pal_abi.ERROR_INVALID, // EINVAL
        25 => pal_abi.ERROR_NOTTY, // ENOTTY
        28 => pal_abi.ERROR_IO, // ENOSPC
        32 => pal_abi.ERROR_PIPE, // EPIPE
        35 => pal_abi.ERROR_AGAIN, // EAGAIN
        36 => pal_abi.ERROR_AGAIN, // EINPROGRESS
        45 => pal_abi.ERROR_UNSUPPORTED, // ENOTSUP
        48 => pal_abi.ERROR_ACCES, // EADDRINUSE
        54 => pal_abi.ERROR_IO, // ECONNRESET
        60 => pal_abi.ERROR_TIMEDOUT, // ETIMEDOUT
        61 => pal_abi.ERROR_CONNREFUSED, // ECONNREFUSED
        66 => pal_abi.ERROR_EXIST, // ENOTEMPTY
        else => pal_abi.ERROR_IO,
    };
}

pub fn statToAbi(st: *const Stat, out: *pal_abi.AbiStat) void {
    const fkind: u32 = switch (st.st_mode & 0o170000) {
        0o040000 => pal_abi.STAT_KIND_DIR,
        0o120000 => pal_abi.STAT_KIND_SYMLINK,
        0o100000 => pal_abi.STAT_KIND_FILE,
        0o020000 => pal_abi.STAT_KIND_CHAR,
        0o010000 => pal_abi.STAT_KIND_FIFO,
        0o140000 => pal_abi.STAT_KIND_SOCKET,
        else => pal_abi.STAT_KIND_UNKNOWN,
    };
    const atime_ns: i64 = @as(i64, @intCast(@max(st.st_atimespec.tv_sec, 0))) * 1_000_000_000 + @as(i64, @intCast(@max(st.st_atimespec.tv_nsec, 0)));
    const mtime_ns: i64 = @as(i64, @intCast(@max(st.st_mtimespec.tv_sec, 0))) * 1_000_000_000 + @as(i64, @intCast(@max(st.st_mtimespec.tv_nsec, 0)));
    const ctime_ns: i64 = @as(i64, @intCast(@max(st.st_ctimespec.tv_sec, 0))) * 1_000_000_000 + @as(i64, @intCast(@max(st.st_ctimespec.tv_nsec, 0)));
    const btime_ns: i64 = @as(i64, @intCast(@max(st.st_birthtimespec.tv_sec, 0))) * 1_000_000_000 + @as(i64, @intCast(@max(st.st_birthtimespec.tv_nsec, 0)));
    out.* = .{
        .kind = fkind,
        .mode = st.st_mode & 0o7777,
        .uid = st.st_uid,
        .gid = st.st_gid,
        .size = @intCast(@max(st.st_size, 0)),
        .mtime_ns = mtime_ns,
        .atime_ns = atime_ns,
        .ctime_ns = ctime_ns,
        .btime_ns = btime_ns,
        .nlink = st.st_nlink,
        .ino = st.st_ino,
        .dev = @intCast(@max(st.st_dev, 0)),
        .rdev = @intCast(@max(st.st_rdev, 0)),
        .blksize = st.st_blksize,
        .blocks = st.st_blocks,
        .valid_mask = 0xffff,
    };
}

pub fn isPlausibleDerSequence(der: []const u8) bool {
    if (der.len < 2 or der[0] != 0x30) return false;
    const l0 = der[1];
    if (l0 < 0x80) {
        return 2 + @as(usize, l0) == der.len;
    }
    const n_len_bytes = l0 & 0x7f;
    if (n_len_bytes == 0 or n_len_bytes > 4 or 2 + n_len_bytes > der.len) return false;
    var body_len: usize = 0;
    for (der[2 .. 2 + n_len_bytes]) |b| {
        body_len = (body_len << 8) | b;
    }
    return 2 + n_len_bytes + body_len == der.len;
}
