const builtin = @import("builtin");
const std = @import("std");
const pal_abi = @import("pal_abi");

pub const AT_FDCWD: i32 = -100;
pub const AT_REMOVEDIR: u32 = 0x200;
pub const AT_SYMLINK_NOFOLLOW: u32 = 0x100;

pub const O_RDONLY: u32 = 0;
pub const O_WRONLY: u32 = 1;
pub const O_RDWR: u32 = 2;
pub const O_CREAT: u32 = 0o100;
pub const O_EXCL: u32 = 0o200;
pub const O_TRUNC: u32 = 0o1000;
pub const O_APPEND: u32 = 0o2000;
pub const O_NONBLOCK: u32 = 0o4000;
pub const O_DIRECTORY: u32 = switch (builtin.cpu.arch) {
    .aarch64 => 0o40000,
    else => 0o200000,
};

pub const PROT_READ: usize = 1;
pub const PROT_WRITE: usize = 2;
pub const PROT_EXEC: usize = 4;
pub const MAP_PRIVATE: usize = 0x02;
pub const MAP_ANONYMOUS: usize = 0x20;

pub const POLLIN: i16 = 0x0001;
pub const POLLOUT: i16 = 0x0004;
pub const POLLERR: i16 = 0x0008;
pub const POLLHUP: i16 = 0x0010;

pub const PollFd = extern struct {
    fd: i32,
    events: i16,
    revents: i16,
};

pub const Timespec = extern struct {
    sec: i64,
    nsec: i64,
};

pub const SockAddrIn = extern struct {
    family: u16 = 2,
    port_be: u16 = 0,
    addr: [4]u8 = @splat(0),
    zero: [8]u8 = @splat(0),
};

pub const LinuxStat = switch (builtin.cpu.arch) {
    .x86_64 => extern struct {
        dev: u64,
        ino: u64,
        nlink: u64,
        mode: u32,
        uid: u32,
        gid: u32,
        _pad0: u32,
        rdev: u64,
        size: i64,
        blksize: i64,
        blocks: i64,
        atime_sec: i64,
        atime_nsec: i64,
        mtime_sec: i64,
        mtime_nsec: i64,
        ctime_sec: i64,
        ctime_nsec: i64,
        _unused: [3]i64,
    },
    .aarch64 => extern struct {
        dev: u64,
        ino: u64,
        mode: u32,
        nlink: u32,
        uid: u32,
        gid: u32,
        rdev: u64,
        _pad1: u64,
        size: i64,
        blksize: i32,
        _pad2: i32,
        blocks: i64,
        atime_sec: i64,
        atime_nsec: i64,
        mtime_sec: i64,
        mtime_nsec: i64,
        ctime_sec: i64,
        ctime_nsec: i64,
        _unused: [2]i32,
    },
    else => @compileError("Unsupported arch"),
};

pub fn isErr(res: usize) ?u32 {
    const signed: isize = @bitCast(res);
    if (signed < 0 and signed >= -4095) {
        return @intCast(-signed);
    }
    return null;
}

pub fn mapLinuxErrno(errno: u32) u32 {
    return switch (errno) {
        0 => 0,
        1 => pal_abi.ERROR_PERM,
        2 => pal_abi.ERROR_NOENT,
        9 => pal_abi.ERROR_BADF,
        11 => pal_abi.ERROR_AGAIN,
        12 => pal_abi.ERROR_NOMEM,
        13 => pal_abi.ERROR_ACCES,
        17 => pal_abi.ERROR_EXIST,
        20 => pal_abi.ERROR_NOTDIR,
        21 => pal_abi.ERROR_ISDIR,
        22 => pal_abi.ERROR_INVALID,
        29 => pal_abi.ERROR_SPIPE,
        32 => pal_abi.ERROR_PIPE,
        38 => pal_abi.ERROR_UNSUPPORTED,
        110 => pal_abi.ERROR_TIMEDOUT,
        111 => pal_abi.ERROR_CONNREFUSED,
        125 => pal_abi.ERROR_CANCELED,
        else => pal_abi.ERROR_IO,
    };
}

pub inline fn sys0(nr: usize) usize {
    return switch (builtin.cpu.arch) {
        .x86_64 => asm volatile ("syscall"
            : [ret] "={rax}" (-> usize),
            : [nr] "{rax}" (nr),
            : .{ .rcx = true, .r11 = true, .memory = true }),
        .aarch64 => asm volatile ("svc #0"
            : [ret] "={x0}" (-> usize),
            : [nr] "{x8}" (nr),
            : .{ .memory = true }),
        else => unreachable,
    };
}

pub inline fn sys1(nr: usize, a0: usize) usize {
    return switch (builtin.cpu.arch) {
        .x86_64 => asm volatile ("syscall"
            : [ret] "={rax}" (-> usize),
            : [nr] "{rax}" (nr),
              [a0] "{rdi}" (a0),
            : .{ .rcx = true, .r11 = true, .memory = true }),
        .aarch64 => asm volatile ("svc #0"
            : [ret] "={x0}" (-> usize),
            : [nr] "{x8}" (nr),
              [a0] "{x0}" (a0),
            : .{ .memory = true }),
        else => unreachable,
    };
}

pub inline fn sys2(nr: usize, a0: usize, a1: usize) usize {
    return switch (builtin.cpu.arch) {
        .x86_64 => asm volatile ("syscall"
            : [ret] "={rax}" (-> usize),
            : [nr] "{rax}" (nr),
              [a0] "{rdi}" (a0),
              [a1] "{rsi}" (a1),
            : .{ .rcx = true, .r11 = true, .memory = true }),
        .aarch64 => asm volatile ("svc #0"
            : [ret] "={x0}" (-> usize),
            : [nr] "{x8}" (nr),
              [a0] "{x0}" (a0),
              [a1] "{x1}" (a1),
            : .{ .memory = true }),
        else => unreachable,
    };
}

pub inline fn sys3(nr: usize, a0: usize, a1: usize, a2: usize) usize {
    return switch (builtin.cpu.arch) {
        .x86_64 => asm volatile ("syscall"
            : [ret] "={rax}" (-> usize),
            : [nr] "{rax}" (nr),
              [a0] "{rdi}" (a0),
              [a1] "{rsi}" (a1),
              [a2] "{rdx}" (a2),
            : .{ .rcx = true, .r11 = true, .memory = true }),
        .aarch64 => asm volatile ("svc #0"
            : [ret] "={x0}" (-> usize),
            : [nr] "{x8}" (nr),
              [a0] "{x0}" (a0),
              [a1] "{x1}" (a1),
              [a2] "{x2}" (a2),
            : .{ .memory = true }),
        else => unreachable,
    };
}

pub inline fn sys4(nr: usize, a0: usize, a1: usize, a2: usize, a3: usize) usize {
    return switch (builtin.cpu.arch) {
        .x86_64 => asm volatile ("syscall"
            : [ret] "={rax}" (-> usize),
            : [nr] "{rax}" (nr),
              [a0] "{rdi}" (a0),
              [a1] "{rsi}" (a1),
              [a2] "{rdx}" (a2),
              [a3] "{r10}" (a3),
            : .{ .rcx = true, .r11 = true, .memory = true }),
        .aarch64 => asm volatile ("svc #0"
            : [ret] "={x0}" (-> usize),
            : [nr] "{x8}" (nr),
              [a0] "{x0}" (a0),
              [a1] "{x1}" (a1),
              [a2] "{x2}" (a2),
              [a3] "{x3}" (a3),
            : .{ .memory = true }),
        else => unreachable,
    };
}

pub inline fn sys6(nr: usize, a0: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize) usize {
    return switch (builtin.cpu.arch) {
        .x86_64 => asm volatile ("syscall"
            : [ret] "={rax}" (-> usize),
            : [nr] "{rax}" (nr),
              [a0] "{rdi}" (a0),
              [a1] "{rsi}" (a1),
              [a2] "{rdx}" (a2),
              [a3] "{r10}" (a3),
              [a4] "{r8}" (a4),
              [a5] "{r9}" (a5),
            : .{ .rcx = true, .r11 = true, .memory = true }),
        .aarch64 => asm volatile ("svc #0"
            : [ret] "={x0}" (-> usize),
            : [nr] "{x8}" (nr),
              [a0] "{x0}" (a0),
              [a1] "{x1}" (a1),
              [a2] "{x2}" (a2),
              [a3] "{x3}" (a3),
              [a4] "{x4}" (a4),
              [a5] "{x5}" (a5),
            : .{ .memory = true }),
        else => unreachable,
    };
}

pub const NR = switch (builtin.cpu.arch) {
    .x86_64 => struct {
        pub const read = 0;
        pub const write = 1;
        pub const close = 3;
        pub const fstat = 5;
        pub const lseek = 8;
        pub const mmap = 9;
        pub const mprotect = 10;
        pub const munmap = 11;
        pub const pread64 = 17;
        pub const pwrite64 = 18;
        pub const pipe2 = 293;
        pub const dup3 = 292;
        pub const socket = 41;
        pub const connect = 42;
        pub const sendto = 44;
        pub const accept = 43;
        pub const recvfrom = 45;
        pub const bind = 49;
        pub const listen = 50;
        pub const getsockname = 51;
        pub const setsockopt = 54;
        pub const clone = 56;
        pub const fork = 57;
        pub const execve = 59;
        pub const wait4 = 61;
        pub const kill = 62;
        pub const fcntl = 72;
        pub const ftruncate = 77;
        pub const getcwd = 79;
        pub const chdir = 80;
        pub const getdents64 = 217;
        pub const clock_gettime = 228;
        pub const exit_group = 231;
        pub const openat = 257;
        pub const mkdirat = 258;
        pub const newfstatat = 262;
        pub const unlinkat = 263;
        pub const renameat = 264;
        pub const linkat = 265;
        pub const symlinkat = 266;
        pub const readlinkat = 267;
        pub const fchmodat = 268;
        pub const ppoll = 271;
        pub const utimensat = 280;
        pub const getrandom = 318;
    },
    .aarch64 => struct {
        pub const fork = 107;
        pub const getcwd = 17;
        pub const dup3 = 24;
        pub const fcntl = 25;
        pub const mkdirat = 34;
        pub const unlinkat = 35;
        pub const symlinkat = 36;
        pub const linkat = 37;
        pub const renameat = 38;
        pub const ftruncate = 46;
        pub const chdir = 49;
        pub const fchmodat = 53;
        pub const openat = 56;
        pub const close = 57;
        pub const pipe2 = 59;
        pub const getdents64 = 61;
        pub const lseek = 62;
        pub const read = 63;
        pub const write = 64;
        pub const pread64 = 67;
        pub const pwrite64 = 66;
        pub const ppoll = 73;
        pub const readlinkat = 78;
        pub const newfstatat = 79;
        pub const fstat = 80;
        pub const utimensat = 88;
        pub const exit_group = 94;
        pub const clock_gettime = 113;
        pub const kill = 129;
        pub const socket = 198;
        pub const bind = 200;
        pub const listen = 201;
        pub const accept = 202;
        pub const recvfrom = 207;
        pub const connect = 203;
        pub const sendto = 206;
        pub const getsockname = 204;
        pub const setsockopt = 208;
        pub const munmap = 215;
        pub const clone = 220;
        pub const execve = 221;
        pub const mmap = 222;
        pub const mprotect = 226;
        pub const wait4 = 260;
        pub const getrandom = 278;
    },
    else => @compileError("Unsupported arch"),
};
