use crate::META;
use alloc::vec::Vec;

// ─── Syscall wrappers ─────────────────────────────────────────────────────────

#[cfg(target_arch = "x86_64")]
unsafe fn sys_read(fd: i32, buf: *mut u8, n: usize) -> isize {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 0usize,   // SYS_read
            in("rdi") fd as usize,
            in("rsi") buf as usize,
            in("rdx") n,
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    ret
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_read(fd: i32, buf: *mut u8, n: usize) -> isize {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 63usize,   // SYS_read
            in("x0") fd as usize,
            in("x1") buf as usize,
            in("x2") n,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret
}

#[cfg(target_arch = "x86_64")]
unsafe fn sys_write(fd: i32, buf: *const u8, n: usize) -> isize {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 1usize,   // SYS_write
            in("rdi") fd as usize,
            in("rsi") buf as usize,
            in("rdx") n,
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    ret
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_write(fd: i32, buf: *const u8, n: usize) -> isize {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 64usize,   // SYS_write
            in("x0") fd as usize,
            in("x1") buf as usize,
            in("x2") n,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret
}

#[cfg(target_arch = "x86_64")]
unsafe fn sys_open(path: *const u8, flags: i32, mode: u32) -> i32 {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 2usize,   // SYS_open
            in("rdi") path as usize,
            in("rsi") flags as usize,
            in("rdx") mode as usize,
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_open(path: *const u8, flags: i32, mode: u32) -> i32 {
    // aarch64 uses openat with AT_FDCWD = -100
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 56usize,                   // SYS_openat
            in("x0") (-100isize) as usize,       // AT_FDCWD
            in("x1") path as usize,
            in("x2") flags as usize,
            in("x3") mode as usize,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

#[cfg(target_arch = "x86_64")]
unsafe fn sys_close(fd: i32) {
    unsafe {
        let _: isize;
        core::arch::asm!(
            "syscall",
            in("rax") 3usize,   // SYS_close
            in("rdi") fd as usize,
            lateout("rax") _,
            clobber_abi("system"),
        );
    }
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_close(fd: i32) {
    unsafe {
        let _: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") 57usize,   // SYS_close
            in("x0") fd as usize,
            lateout("x0") _,
            clobber_abi("system"),
        );
    }
}

#[cfg(target_arch = "x86_64")]
unsafe fn sys_lseek(fd: i32, offset: i64, whence: i32) -> i64 {
    let ret: i64;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 8usize,   // SYS_lseek
            in("rdi") fd as usize,
            in("rsi") offset as usize,
            in("rdx") whence as usize,
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    ret
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_lseek(fd: i32, offset: i64, whence: i32) -> i64 {
    let ret: i64;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 62usize,   // SYS_lseek
            in("x0") fd as usize,
            in("x1") offset as usize,
            in("x2") whence as usize,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret
}

#[cfg(target_arch = "x86_64")]
unsafe fn sys_fchmod(fd: i32, mode: u32) -> i32 {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 91usize,  // SYS_fchmod
            in("rdi") fd as usize,
            in("rsi") mode as usize,
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_fchmod(fd: i32, mode: u32) -> i32 {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 52usize,   // SYS_fchmod
            in("x0") fd as usize,
            in("x1") mode as usize,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

#[cfg(target_arch = "x86_64")]
unsafe fn sys_unlink(path: *const u8) {
    unsafe {
        let _: isize;
        core::arch::asm!(
            "syscall",
            in("rax") 87usize,  // SYS_unlink
            in("rdi") path as usize,
            lateout("rax") _,
            clobber_abi("system"),
        );
    }
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_unlink(path: *const u8) {
    // aarch64 uses unlinkat with AT_FDCWD = -100 and flags = 0
    unsafe {
        let _: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") 35usize,                   // SYS_unlinkat
            in("x0") (-100isize) as usize,       // AT_FDCWD
            in("x1") path as usize,
            in("x2") 0usize,                    // flags
            lateout("x0") _,
            clobber_abi("system"),
        );
    }
}

#[cfg(target_arch = "x86_64")]
unsafe fn sys_getpid() -> i32 {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 39usize,  // SYS_getpid
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_getpid() -> i32 {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 172usize,  // SYS_getpid
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

/// Fork on x86_64, emulated via clone(SIGCHLD) on aarch64 (no fork syscall there).
#[cfg(target_arch = "x86_64")]
unsafe fn sys_fork() -> i32 {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 57usize,  // SYS_fork
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

#[cfg(target_arch = "aarch64")]
unsafe fn sys_fork() -> i32 {
    let ret: isize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 220usize,  // SYS_clone
            in("x0") 17usize,   // SIGCHLD — equivalent to fork semantics
            in("x1") 0usize,    // stack: 0 = inherit parent stack
            in("x2") 0usize,
            in("x3") 0usize,
            in("x4") 0usize,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

unsafe fn sys_execve(path: *const u8, argv: *const *const u8, envp: *const *const u8) -> i32 {
    let ret: isize;
    #[cfg(target_arch = "x86_64")]
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 59usize,  // SYS_execve
            in("rdi") path as usize,
            in("rsi") argv as usize,
            in("rdx") envp as usize,
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    #[cfg(target_arch = "aarch64")]
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 221usize,  // SYS_execve
            in("x0") path as usize,
            in("x1") argv as usize,
            in("x2") envp as usize,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

unsafe fn sys_wait4(pid: i32, status: *mut i32) -> i32 {
    let ret: isize;
    #[cfg(target_arch = "x86_64")]
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 61usize,  // SYS_wait4
            in("rdi") pid as usize,
            in("rsi") status as usize,
            in("rdx") 0usize,   // options
            in("r10") 0usize,   // rusage = NULL
            lateout("rax") ret,
            clobber_abi("system"),
        );
    }
    #[cfg(target_arch = "aarch64")]
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 260usize,  // SYS_wait4
            in("x0") pid as usize,
            in("x1") status as usize,
            in("x2") 0usize,
            in("x3") 0usize,
            lateout("x0") ret,
            clobber_abi("system"),
        );
    }
    ret as i32
}

pub unsafe fn sys_exit_group(code: i32) -> ! {
    #[cfg(target_arch = "x86_64")]
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 231usize,  // SYS_exit_group
            in("rdi") code as usize,
            options(noreturn),
        );
    }
    #[cfg(target_arch = "aarch64")]
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 94usize,   // SYS_exit_group
            in("x0") code as usize,
            options(noreturn),
        );
    }
}

// ─── I/O helpers ─────────────────────────────────────────────────────────────

unsafe fn write_all(fd: i32, data: &[u8]) -> bool {
    let mut off = 0;
    while off < data.len() {
        let n = unsafe { sys_write(fd, data.as_ptr().add(off), data.len() - off) };
        if n <= 0 {
            return false;
        }
        off += n as usize;
    }
    true
}

unsafe fn read_fd_to_vec(fd: i32) -> Vec<u8> {
    let mut buf: Vec<u8> = Vec::new();
    let mut tmp = [0u8; 4096];
    loop {
        let n = unsafe { sys_read(fd, tmp.as_mut_ptr(), tmp.len()) };
        if n <= 0 {
            break;
        }
        buf.extend_from_slice(&tmp[..n as usize]);
    }
    buf
}

// ─── Numeric helpers ──────────────────────────────────────────────────────────

fn append_decimal(buf: &mut Vec<u8>, mut n: u32) {
    if n == 0 {
        buf.push(b'0');
        return;
    }
    let start = buf.len();
    while n > 0 {
        buf.push(b'0' + (n % 10) as u8);
        n /= 10;
    }
    buf[start..].reverse();
}

fn make_tmp_path(prefix: &[u8], pid: u32, idx: u8) -> Vec<u8> {
    let mut path = Vec::with_capacity(prefix.len() + 12);
    path.extend_from_slice(prefix);
    append_decimal(&mut path, pid);
    path.push(b'-');
    path.push(b'0' + idx);
    path.push(0); // NUL terminator
    path
}

// ─── Main logic ──────────────────────────────────────────────────────────────

pub unsafe fn run() -> ! {
    const O_RDONLY: i32 = 0;
    const O_RDWR: i32 = 2;
    const O_CREAT: i32 = 64;
    const O_EXCL: i32 = 128;
    const SEEK_SET: i32 = 0;
    const SEEK_END: i32 = 2;

    // ── Read /proc/self/cmdline to obtain argv entries ────────────────────
    let cmdline_path = b"/proc/self/cmdline\0";
    let cmdline_fd = unsafe { sys_open(cmdline_path.as_ptr(), O_RDONLY, 0) };
    if cmdline_fd < 0 {
        unsafe { sys_exit_group(102) };
    }
    let cmdline = unsafe { read_fd_to_vec(cmdline_fd) };
    unsafe { sys_close(cmdline_fd) };

    // Split cmdline on NUL bytes to recover argv entries.
    let args: Vec<&[u8]> = cmdline
        .split(|&b| b == 0)
        .filter(|s| !s.is_empty())
        .collect();

    if args.len() < 2 {
        unsafe { sys_exit_group(102) };
    }

    // argv[1] is the bat file path (the "vegetable").
    let bat_path_bytes = args[1];
    let mut bat_path_cstr: Vec<u8> = Vec::with_capacity(bat_path_bytes.len() + 1);
    bat_path_cstr.extend_from_slice(bat_path_bytes);
    bat_path_cstr.push(0);

    // Extra args (argv[2..]) forwarded to washmhost.
    let extra_args: Vec<Vec<u8>> = args[2..]
        .iter()
        .map(|s| {
            let mut v = Vec::with_capacity(s.len() + 1);
            v.extend_from_slice(s);
            v.push(0);
            v
        })
        .collect();

    // ── Open the vegetable file and read the compressed pool ──────────────
    let fd = unsafe { sys_open(bat_path_cstr.as_ptr(), O_RDONLY, 0) };
    if fd < 0 {
        unsafe { sys_exit_group(2) };
    }

    let file_size = unsafe { sys_lseek(fd, 0, SEEK_END) };
    if file_size < 0 {
        unsafe { sys_close(fd) };
        unsafe { sys_exit_group(3) };
    }

    let pool_len = unsafe { META.pool_len as usize };
    if pool_len == 0 {
        unsafe { sys_close(fd) };
        unsafe { sys_exit_group(4) };
    }

    let pool_start = file_size - pool_len as i64;
    if pool_start < 0 {
        unsafe { sys_close(fd) };
        unsafe { sys_exit_group(5) };
    }

    if unsafe { sys_lseek(fd, pool_start, SEEK_SET) } < 0 {
        unsafe { sys_close(fd) };
        unsafe { sys_exit_group(6) };
    }

    let mut compressed_data = alloc::vec![0u8; pool_len];
    let mut total_read = 0;
    while total_read < pool_len {
        let n = unsafe {
            sys_read(
                fd,
                compressed_data.as_mut_ptr().add(total_read),
                pool_len - total_read,
            )
        };
        if n <= 0 {
            unsafe { sys_close(fd) };
            unsafe { sys_exit_group(7) };
        }
        total_read += n as usize;
    }
    unsafe { sys_close(fd) };

    // ── Decompress pool ───────────────────────────────────────────────────
    let total_pool = unsafe { (META.payload_offset + META.payload_len) as usize };
    let mut decompressed = alloc::vec![0u8; total_pool];
    let mut out_off = 0usize;
    let _ = crate::decompress::decompress_to_writer(&compressed_data, |chunk| {
        let end = out_off + chunk.len();
        if end <= decompressed.len() {
            decompressed[out_off..end].copy_from_slice(chunk);
            out_off = end;
        }
    });
    drop(compressed_data);

    let washmhost_data = unsafe {
        &decompressed
            [META.washmhost_offset as usize..(META.washmhost_offset + META.washmhost_len) as usize]
    };
    let payload_data = unsafe {
        &decompressed
            [META.payload_offset as usize..(META.payload_offset + META.payload_len) as usize]
    };

    // ── Write washmhost to a temp file ────────────────────────────────────
    let pid = unsafe { sys_getpid() } as u32;
    let washmhost_path = make_tmp_path(b"/tmp/moh-", pid, 0);
    let payload_path = make_tmp_path(b"/tmp/mohp-", pid, 0);

    let tmp_fd = unsafe { sys_open(washmhost_path.as_ptr(), O_RDWR | O_CREAT | O_EXCL, 0o600) };
    if tmp_fd < 0 {
        unsafe { sys_exit_group(10) };
    }

    if !unsafe { write_all(tmp_fd, washmhost_data) } {
        unsafe { sys_close(tmp_fd) };
        unsafe { sys_unlink(washmhost_path.as_ptr()) };
        unsafe { sys_exit_group(11) };
    }
    // Make washmhost executable via fchmod (we still have the fd from open).
    unsafe { sys_fchmod(tmp_fd, 0o755) };
    unsafe { sys_close(tmp_fd) };

    // ── Write payload to a temp file ──────────────────────────────────────
    let payload_fd = unsafe { sys_open(payload_path.as_ptr(), O_RDWR | O_CREAT | O_EXCL, 0o600) };
    if payload_fd < 0 {
        unsafe { sys_unlink(washmhost_path.as_ptr()) };
        unsafe { sys_exit_group(14) };
    }

    if !unsafe { write_all(payload_fd, payload_data) } {
        unsafe { sys_close(payload_fd) };
        unsafe { sys_unlink(payload_path.as_ptr()) };
        unsafe { sys_unlink(washmhost_path.as_ptr()) };
        unsafe { sys_exit_group(15) };
    }
    unsafe { sys_close(payload_fd) };

    // ── Build environment for the child ──────────────────────────────────
    // Read /proc/self/environ (NUL-separated KEY=VALUE strings).
    let environ_path = b"/proc/self/environ\0";
    let env_fd = unsafe { sys_open(environ_path.as_ptr(), O_RDONLY, 0) };
    let env_data: Vec<u8> = if env_fd >= 0 {
        let d = unsafe { read_fd_to_vec(env_fd) };
        unsafe { sys_close(env_fd) };
        d
    } else {
        Vec::new()
    };

    // Build "MOHABBAT_WASM_FD=<path>\0"
    let mut wasm_fd_var: Vec<u8> = b"MOHABBAT_WASM_FD=".to_vec();
    wasm_fd_var.extend_from_slice(&payload_path[..payload_path.len() - 1]); // skip NUL
    wasm_fd_var.push(0);

    // Collect pointers to each env var entry from env_data (split on NUL).
    let mut envp_ptrs: Vec<*const u8> = Vec::new();
    {
        let mut start = 0usize;
        while start < env_data.len() {
            let entry_start = start;
            while start < env_data.len() && env_data[start] != 0 {
                start += 1;
            }
            if start > entry_start {
                unsafe { envp_ptrs.push(env_data.as_ptr().add(entry_start)) };
            }
            start += 1; // skip NUL
        }
    }
    envp_ptrs.push(wasm_fd_var.as_ptr());
    envp_ptrs.push(core::ptr::null());

    // ── Build argv for the child ──────────────────────────────────────────
    // argv[0] = vegetable_path (bat file), argv[1..] = extra args forwarded.
    let mut argv_ptrs: Vec<*const u8> = Vec::new();
    argv_ptrs.push(bat_path_cstr.as_ptr());
    for a in &extra_args {
        argv_ptrs.push(a.as_ptr());
    }
    argv_ptrs.push(core::ptr::null());

    // ── fork + execve ─────────────────────────────────────────────────────
    let child_pid = unsafe { sys_fork() };

    if child_pid < 0 {
        // fork failed
        unsafe { sys_unlink(payload_path.as_ptr()) };
        unsafe { sys_unlink(washmhost_path.as_ptr()) };
        unsafe { sys_exit_group(16) };
    }

    if child_pid == 0 {
        // Child: exec washmhost
        unsafe {
            sys_execve(
                washmhost_path.as_ptr(),
                argv_ptrs.as_ptr(),
                envp_ptrs.as_ptr(),
            )
        };
        // execve returned → failure
        unsafe { sys_exit_group(17) };
    }

    // ── Parent: wait for child ─────────────────────────────────────────────
    let mut wait_status: i32 = 0;
    unsafe { sys_wait4(child_pid, &mut wait_status) };

    unsafe { sys_unlink(payload_path.as_ptr()) };
    unsafe { sys_unlink(washmhost_path.as_ptr()) };

    // WIFEXITED(status): (status & 0x7f) == 0
    // WEXITSTATUS(status): (status >> 8) & 0xff
    let exit_code = if (wait_status & 0x7f) == 0 {
        (wait_status >> 8) & 0xff
    } else {
        1
    };

    unsafe { sys_exit_group(exit_code) };
}
