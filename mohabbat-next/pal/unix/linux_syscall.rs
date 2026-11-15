#![allow(dead_code)]

use core::ffi::c_void;

pub const AT_FDCWD: i32 = -100;
pub const AT_REMOVEDIR: i32 = 0x200;
pub const AT_SYMLINK_NOFOLLOW: i32 = 0x100;

pub const O_RDONLY: i32 = 0;
pub const O_WRONLY: i32 = 1;
pub const O_RDWR: i32 = 2;
pub const O_CREAT: i32 = 0o100;
pub const O_EXCL: i32 = 0o200;
pub const O_TRUNC: i32 = 0o1000;
pub const O_APPEND: i32 = 0o2000;
pub const O_NONBLOCK: i32 = 0o4000;
pub const O_CLOEXEC: i32 = 0o2000000;
#[cfg(target_arch = "x86_64")]
pub const O_DIRECTORY: i32 = 0o200000;
#[cfg(target_arch = "aarch64")]
pub const O_DIRECTORY: i32 = 0o40000;

pub const PROT_NONE: usize = 0;
pub const PROT_READ: usize = 1;
pub const PROT_WRITE: usize = 2;
pub const PROT_EXEC: usize = 4;

pub const MAP_PRIVATE: usize = 0x02;
pub const MAP_ANONYMOUS: usize = 0x20;
pub const MAP_SHARED: usize = 0x01;

pub const CLONE_VM: usize = 0x00000100;
pub const CLONE_FS: usize = 0x00000200;
pub const CLONE_FILES: usize = 0x00000400;
pub const CLONE_SIGHAND: usize = 0x00000800;
pub const CLONE_THREAD: usize = 0x00010000;
pub const CLONE_SYSVSEM: usize = 0x00040000;
pub const CLONE_PARENT_SETTID: usize = 0x00100000;
pub const CLONE_CHILD_CLEARTID: usize = 0x00200000;

pub const FUTEX_WAIT: usize = 0;
pub const FUTEX_WAKE: usize = 1;
pub const FUTEX_PRIVATE_FLAG: usize = 128;

pub const CLOCK_REALTIME: i32 = 0;
pub const CLOCK_MONOTONIC: i32 = 1;
pub const TFD_NONBLOCK: i32 = O_NONBLOCK;
pub const TFD_CLOEXEC: i32 = O_CLOEXEC;

pub const POLLIN: i16 = 0x0001;
pub const POLLOUT: i16 = 0x0004;
pub const POLLERR: i16 = 0x0008;
pub const POLLHUP: i16 = 0x0010;

pub const EPERM: i32 = 1;
pub const ENOENT: i32 = 2;
pub const ESRCH: i32 = 3;
pub const EINTR: i32 = 4;
pub const EIO: i32 = 5;
pub const EBADF: i32 = 9;
pub const ECHILD: i32 = 10;
pub const EAGAIN: i32 = 11;
pub const ENOMEM: i32 = 12;
pub const EACCES: i32 = 13;
pub const EFAULT: i32 = 14;
pub const EBUSY: i32 = 16;
pub const EEXIST: i32 = 17;
pub const ENOTDIR: i32 = 20;
pub const EISDIR: i32 = 21;
pub const EINVAL: i32 = 22;
pub const EPIPE: i32 = 32;
pub const ENOSYS: i32 = 38;
pub const ENOTEMPTY: i32 = 39;
pub const ETIMEDOUT: i32 = 110;
pub const ECONNREFUSED: i32 = 111;
pub const EINPROGRESS: i32 = 115;

#[cfg(target_arch = "x86_64")]
pub mod nr {
    pub const READ: usize = 0;
    pub const WRITE: usize = 1;
    pub const CLOSE: usize = 3;
    pub const FSTAT: usize = 5;
    pub const LSEEK: usize = 8;
    pub const MMAP: usize = 9;
    pub const MPROTECT: usize = 10;
    pub const MUNMAP: usize = 11;
    pub const IOCTL: usize = 16;
    pub const PREAD64: usize = 17;
    pub const PWRITE64: usize = 18;
    pub const PIPE: usize = 22;
    pub const NANOSLEEP: usize = 35;
    pub const GETPID: usize = 39;
    pub const SOCKET: usize = 41;
    pub const CONNECT: usize = 42;
    pub const ACCEPT: usize = 43;
    pub const SENDTO: usize = 44;
    pub const RECVFROM: usize = 45;
    pub const SHUTDOWN: usize = 48;
    pub const BIND: usize = 49;
    pub const LISTEN: usize = 50;
    pub const GETSOCKNAME: usize = 51;
    pub const GETPEERNAME: usize = 52;
    pub const SETSOCKOPT: usize = 54;
    pub const GETSOCKOPT: usize = 55;
    pub const CLONE: usize = 56;
    pub const FORK: usize = 57;
    pub const EXECVE: usize = 59;
    pub const EXIT: usize = 60;
    pub const WAIT4: usize = 61;
    pub const KILL: usize = 62;
    pub const FCNTL: usize = 72;
    pub const FSYNC: usize = 74;
    pub const FDATASYNC: usize = 75;
    pub const FTRUNCATE: usize = 77;
    pub const GETCWD: usize = 79;
    pub const CHDIR: usize = 80;
    pub const STATFS: usize = 137;
    pub const FSTATFS: usize = 138;
    pub const GETTID: usize = 186;
    pub const FUTEX: usize = 202;
    pub const GETDENTS64: usize = 217;
    pub const CLOCK_GETTIME: usize = 228;
    pub const CLOCK_GETRES: usize = 229;
    pub const EXIT_GROUP: usize = 231;
    pub const OPENAT: usize = 257;
    pub const MKDIRAT: usize = 258;
    pub const NEWFSTATAT: usize = 262;
    pub const UNLINKAT: usize = 263;
    pub const SYMLINKAT: usize = 266;
    pub const READLINKAT: usize = 267;
    pub const FCHMODAT: usize = 268;
    pub const PPOLL: usize = 271;
    pub const UTIMENSAT: usize = 280;
    pub const TIMERFD_CREATE: usize = 283;
    pub const TIMERFD_SETTIME: usize = 286;
    pub const ACCEPT4: usize = 288;
    pub const DUP3: usize = 292;
    pub const PIPE2: usize = 293;
    pub const RENAMEAT2: usize = 316;
    pub const GETRANDOM: usize = 318;
    pub const IO_URING_SETUP: usize = 425;
    pub const IO_URING_ENTER: usize = 426;
    pub const IO_URING_REGISTER: usize = 427;
}

#[cfg(target_arch = "aarch64")]
pub mod nr {
    pub const GETCWD: usize = 17;
    pub const DUP3: usize = 24;
    pub const FCNTL: usize = 25;
    pub const IOCTL: usize = 29;
    pub const MKDIRAT: usize = 34;
    pub const UNLINKAT: usize = 35;
    pub const SYMLINKAT: usize = 36;
    pub const READLINKAT: usize = 78;
    pub const NEWFSTATAT: usize = 79;
    pub const FSTAT: usize = 80;
    pub const FTRUNCATE: usize = 46;
    pub const FCHMODAT: usize = 53;
    pub const CHDIR: usize = 49;
    pub const OPENAT: usize = 56;
    pub const CLOSE: usize = 57;
    pub const PIPE2: usize = 59;
    pub const GETDENTS64: usize = 61;
    pub const LSEEK: usize = 62;
    pub const READ: usize = 63;
    pub const WRITE: usize = 64;
    pub const PREAD64: usize = 67;
    pub const PWRITE64: usize = 68;
    pub const PPOLL: usize = 73;
    pub const FSYNC: usize = 82;
    pub const FDATASYNC: usize = 83;
    pub const TIMERFD_CREATE: usize = 85;
    pub const TIMERFD_SETTIME: usize = 86;
    pub const UTIMENSAT: usize = 88;
    pub const EXIT: usize = 93;
    pub const EXIT_GROUP: usize = 94;
    pub const FUTEX: usize = 98;
    pub const NANOSLEEP: usize = 101;
    pub const CLOCK_GETTIME: usize = 113;
    pub const CLOCK_GETRES: usize = 114;
    pub const KILL: usize = 129;
    pub const GETPID: usize = 172;
    pub const GETTID: usize = 178;
    pub const SOCKET: usize = 198;
    pub const BIND: usize = 200;
    pub const LISTEN: usize = 201;
    pub const ACCEPT: usize = 202;
    pub const CONNECT: usize = 203;
    pub const GETSOCKNAME: usize = 204;
    pub const GETPEERNAME: usize = 205;
    pub const SENDTO: usize = 206;
    pub const RECVFROM: usize = 207;
    pub const SETSOCKOPT: usize = 208;
    pub const GETSOCKOPT: usize = 209;
    pub const SHUTDOWN: usize = 210;
    pub const MUNMAP: usize = 215;
    pub const CLONE: usize = 220;
    pub const EXECVE: usize = 221;
    pub const MMAP: usize = 222;
    pub const MPROTECT: usize = 226;
    pub const ACCEPT4: usize = 242;
    pub const WAIT4: usize = 260;
    pub const STATFS: usize = 43;
    pub const FSTATFS: usize = 44;
    pub const RENAMEAT2: usize = 276;
    pub const GETRANDOM: usize = 278;
    pub const IO_URING_SETUP: usize = 425;
    pub const IO_URING_ENTER: usize = 426;
    pub const IO_URING_REGISTER: usize = 427;
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct Timespec {
    pub tv_sec: i64,
    pub tv_nsec: i64,
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct ITimerSpec {
    pub it_interval: Timespec,
    pub it_value: Timespec,
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct PollFd {
    pub fd: i32,
    pub events: i16,
    pub revents: i16,
}

#[cfg(target_arch = "x86_64")]
#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct LinuxStat {
    pub st_dev: u64,
    pub st_ino: u64,
    pub st_nlink: u64,
    pub st_mode: u32,
    pub st_uid: u32,
    pub st_gid: u32,
    pub __pad0: u32,
    pub st_rdev: u64,
    pub st_size: i64,
    pub st_blksize: i64,
    pub st_blocks: i64,
    pub st_atime: i64,
    pub st_atime_nsec: i64,
    pub st_mtime: i64,
    pub st_mtime_nsec: i64,
    pub st_ctime: i64,
    pub st_ctime_nsec: i64,
    pub __unused: [i64; 3],
}

#[cfg(target_arch = "aarch64")]
#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct LinuxStat {
    pub st_dev: u64,
    pub st_ino: u64,
    pub st_mode: u32,
    pub st_nlink: u32,
    pub st_uid: u32,
    pub st_gid: u32,
    pub st_rdev: u64,
    pub __pad1: u64,
    pub st_size: i64,
    pub st_blksize: i32,
    pub __pad2: i32,
    pub st_blocks: i64,
    pub st_atime: i64,
    pub st_atime_nsec: i64,
    pub st_mtime: i64,
    pub st_mtime_nsec: i64,
    pub st_ctime: i64,
    pub st_ctime_nsec: i64,
    pub __unused: [i32; 2],
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct LinuxStatFs {
    pub f_type: i64,
    pub f_bsize: i64,
    pub f_blocks: u64,
    pub f_bfree: u64,
    pub f_bavail: u64,
    pub f_files: u64,
    pub f_ffree: u64,
    pub f_fsid: [i32; 2],
    pub f_namelen: i64,
    pub f_frsize: i64,
    pub f_flags: i64,
    pub f_spare: [i64; 4],
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct IoUringParams {
    pub sq_entries: u32,
    pub cq_entries: u32,
    pub flags: u32,
    pub sq_thread_cpu: u32,
    pub sq_thread_idle: u32,
    pub features: u32,
    pub wq_fd: u32,
    pub resv: [u32; 3],
    pub sq_off: [u32; 10],
    pub cq_off: [u32; 10],
}

impl Default for IoUringParams {
    fn default() -> Self {
        Self {
            sq_entries: 0,
            cq_entries: 0,
            flags: 0,
            sq_thread_cpu: 0,
            sq_thread_idle: 0,
            features: 0,
            wq_fd: 0,
            resv: [0; 3],
            sq_off: [0; 10],
            cq_off: [0; 10],
        }
    }
}

#[inline(always)]
pub fn errno_from_ret(ret: isize) -> Option<i32> {
    if ret < 0 && ret >= -4095 {
        Some((-ret) as i32)
    } else {
        None
    }
}

#[inline(always)]
pub unsafe fn syscall0(num: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") num,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

#[inline(always)]
pub unsafe fn syscall1(num: usize, a1: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") num,
            in("rdi") a1,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            in("x0") a1,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

#[inline(always)]
pub unsafe fn syscall2(num: usize, a1: usize, a2: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") num,
            in("rdi") a1,
            in("rsi") a2,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            in("x0") a1,
            in("x1") a2,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

#[inline(always)]
pub unsafe fn syscall3(num: usize, a1: usize, a2: usize, a3: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") num,
            in("rdi") a1,
            in("rsi") a2,
            in("rdx") a3,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            in("x0") a1,
            in("x1") a2,
            in("x2") a3,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

#[inline(always)]
pub unsafe fn syscall4(num: usize, a1: usize, a2: usize, a3: usize, a4: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") num,
            in("rdi") a1,
            in("rsi") a2,
            in("rdx") a3,
            in("r10") a4,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            in("x0") a1,
            in("x1") a2,
            in("x2") a3,
            in("x3") a4,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

#[inline(always)]
pub unsafe fn syscall5(num: usize, a1: usize, a2: usize, a3: usize, a4: usize, a5: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") num,
            in("rdi") a1,
            in("rsi") a2,
            in("rdx") a3,
            in("r10") a4,
            in("r8") a5,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            in("x0") a1,
            in("x1") a2,
            in("x2") a3,
            in("x3") a4,
            in("x4") a5,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

#[inline(always)]
pub unsafe fn syscall6(
    num: usize,
    a1: usize,
    a2: usize,
    a3: usize,
    a4: usize,
    a5: usize,
    a6: usize,
) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") num,
            in("rdi") a1,
            in("rsi") a2,
            in("rdx") a3,
            in("r10") a4,
            in("r8") a5,
            in("r9") a6,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") num,
            in("x0") a1,
            in("x1") a2,
            in("x2") a3,
            in("x3") a4,
            in("x4") a5,
            in("x5") a6,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

pub type ThreadStartFn = unsafe extern "C" fn(*mut c_void) -> !;

#[cfg(target_arch = "x86_64")]
#[unsafe(naked)]
pub unsafe extern "C" fn clone_thread(
    _flags: usize,
    _stack_top: *mut u8,
    _child_tid: *mut i32,
    _start: ThreadStartFn,
    _arg: *mut c_void,
) -> isize {
    core::arch::naked_asm!(
        "push r12",
        "push r13",
        "mov r12, rcx",
        "mov r13, r8",
        "mov r10, rdx",
        "xor r8d, r8d",
        "mov eax, 56",
        "syscall",
        "test rax, rax",
        "jz 2f",
        "pop r13",
        "pop r12",
        "ret",
        "2:",
        "mov rdi, r13",
        "call r12",
        "mov eax, 60",
        "xor edi, edi",
        "syscall",
        "ud2",
    )
}

#[cfg(target_arch = "aarch64")]
#[unsafe(naked)]
pub unsafe extern "C" fn clone_thread(
    _flags: usize,
    _stack_top: *mut u8,
    _child_tid: *mut i32,
    _start: ThreadStartFn,
    _arg: *mut c_void,
) -> isize {
    core::arch::naked_asm!(
        "stp x19, x20, [sp, #-16]!",
        "mov x19, x3",
        "mov x20, x4",
        "mov x4, x2",
        "mov x3, xzr",
        "mov x8, #220",
        "svc #0",
        "cbz x0, 2f",
        "ldp x19, x20, [sp], #16",
        "ret",
        "2:",
        "mov x0, x20",
        "blr x19",
        "mov x0, #0",
        "mov x8, #93",
        "svc #0",
        "brk #0",
    )
}

pub unsafe fn mmap(
    addr: usize,
    len: usize,
    prot: usize,
    flags: usize,
    fd: i32,
    offset: usize,
) -> isize {
    syscall6(nr::MMAP, addr, len, prot, flags, fd as usize, offset)
}

pub unsafe fn mprotect(addr: usize, len: usize, prot: usize) -> isize {
    syscall3(nr::MPROTECT, addr, len, prot)
}

pub unsafe fn munmap(addr: usize, len: usize) -> isize {
    syscall2(nr::MUNMAP, addr, len)
}

pub unsafe fn futex_wait(uaddr: *mut i32, val: i32) -> isize {
    syscall4(
        nr::FUTEX,
        uaddr as usize,
        FUTEX_WAIT | FUTEX_PRIVATE_FLAG,
        val as usize,
        0,
    )
}

pub unsafe fn futex_wait_timeout(
    uaddr: *mut i32,
    val: i32,
    timeout: *const Timespec,
) -> isize {
    syscall4(
        nr::FUTEX,
        uaddr as usize,
        FUTEX_WAIT | FUTEX_PRIVATE_FLAG,
        val as usize,
        timeout as usize,
    )
}

pub unsafe fn futex_wake(uaddr: *mut i32, count: i32) -> isize {
    syscall3(
        nr::FUTEX,
        uaddr as usize,
        FUTEX_WAKE | FUTEX_PRIVATE_FLAG,
        count as usize,
    )
}

pub unsafe fn pipe2(fds: *mut [i32; 2], flags: i32) -> isize {
    syscall2(nr::PIPE2, fds as usize, flags as usize)
}

pub unsafe fn ppoll(fds: *mut PollFd, nfds: usize, timeout: *const Timespec) -> isize {
    syscall4(nr::PPOLL, fds as usize, nfds, timeout as usize, 0)
}

pub unsafe fn timerfd_create(clockid: i32, flags: i32) -> isize {
    syscall2(nr::TIMERFD_CREATE, clockid as usize, flags as usize)
}

pub unsafe fn timerfd_settime(
    fd: i32,
    flags: i32,
    new_value: *const ITimerSpec,
    old_value: *mut ITimerSpec,
) -> isize {
    syscall4(
        nr::TIMERFD_SETTIME,
        fd as usize,
        flags as usize,
        new_value as usize,
        old_value as usize,
    )
}

pub unsafe fn clock_gettime_ns(clock_id: i32) -> u64 {
    let mut ts = Timespec::default();
    let ret = syscall2(
        nr::CLOCK_GETTIME,
        clock_id as usize,
        (&mut ts as *mut Timespec) as usize,
    );
    if ret == 0 && ts.tv_sec >= 0 && ts.tv_nsec >= 0 {
        (ts.tv_sec as u64)
            .saturating_mul(1_000_000_000)
            .saturating_add(ts.tv_nsec as u64)
    } else {
        0
    }
}

pub unsafe fn io_uring_setup(entries: u32, params: *mut IoUringParams) -> isize {
    syscall2(nr::IO_URING_SETUP, entries as usize, params as usize)
}

pub unsafe fn io_uring_enter(
    fd: i32,
    to_submit: u32,
    min_complete: u32,
    flags: u32,
    sig: usize,
) -> isize {
    syscall6(
        nr::IO_URING_ENTER,
        fd as usize,
        to_submit as usize,
        min_complete as usize,
        flags as usize,
        sig,
        8,
    )
}

pub unsafe fn io_uring_register(
    fd: i32,
    opcode: u32,
    arg: *const c_void,
    nr_args: u32,
) -> isize {
    syscall4(
        nr::IO_URING_REGISTER,
        fd as usize,
        opcode as usize,
        arg as usize,
        nr_args as usize,
    )
}

/// Probes whether `io_uring_setup` is permitted in the current kernel/sandbox.
/// Returns `Some(fd)` if supported, `None` on `-ENOSYS` / `-EPERM` / error.
pub unsafe fn probe_io_uring(entries: u32) -> Option<(i32, IoUringParams)> {
    let mut params = IoUringParams::default();
    let fd = io_uring_setup(entries, &mut params);
    if fd >= 0 {
        Some((fd as i32, params))
    } else {
        None
    }
}

pub unsafe fn exit_thread(code: i32) -> ! {
    syscall1(nr::EXIT, code as usize);
    loop {
        core::hint::spin_loop();
    }
}

pub unsafe fn exit_group(code: i32) -> ! {
    syscall1(nr::EXIT_GROUP, code as usize);
    loop {
        core::hint::spin_loop();
    }
}
