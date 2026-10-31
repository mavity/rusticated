use super::{EINVAL, SpawnResult, SpawnSpec};

const SESSION_COUNT: usize = 32;
const EMPTY: Session = Session {
    pid: 0,
    read_fd: -1,
    write_fd: -1,
    exit_code: 0,
    exited: false,
    pty_closed: false,
    child_closed: false,
};

#[derive(Clone, Copy)]
struct Session {
    pid: i32,
    read_fd: i32,
    write_fd: i32,
    exit_code: i32,
    exited: bool,
    pty_closed: bool,
    child_closed: bool,
}

static mut SESSIONS: [Session; SESSION_COUNT] = [EMPTY; SESSION_COUNT];

#[cfg(target_arch = "x86_64")]
unsafe fn syscall6(
    number: usize,
    a1: usize,
    a2: usize,
    a3: usize,
    a4: usize,
    a5: usize,
    a6: usize,
) -> isize {
    let result: isize;
    unsafe {
        core::arch::asm!(
            "syscall",
            inlateout("rax") number as isize => result,
            in("rdi") a1,
            in("rsi") a2,
            in("rdx") a3,
            in("r10") a4,
            in("r8") a5,
            in("r9") a6,
            lateout("rcx") _,
            lateout("r11") _,
            options(nostack),
        );
    }
    result
}

#[cfg(target_arch = "aarch64")]
unsafe fn syscall6(
    number: usize,
    a1: usize,
    a2: usize,
    a3: usize,
    a4: usize,
    a5: usize,
    a6: usize,
) -> isize {
    let result: isize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            inlateout("x8") number => _,
            inlateout("x0") a1 => result,
            in("x1") a2,
            in("x2") a3,
            in("x3") a4,
            in("x4") a5,
            in("x5") a6,
            options(nostack),
        );
    }
    result
}

#[cfg(target_arch = "x86_64")]
const SYS_OPENAT: usize = 257;
#[cfg(target_arch = "x86_64")]
const SYS_IOCTL: usize = 16;
#[cfg(target_arch = "x86_64")]
const SYS_CLOSE: usize = 3;
#[cfg(target_arch = "x86_64")]
const SYS_DUP: usize = 32;
#[cfg(target_arch = "x86_64")]
const SYS_FORK: usize = 57;
#[cfg(target_arch = "x86_64")]
const SYS_SETSID: usize = 112;
#[cfg(target_arch = "x86_64")]
const SYS_DUP2: usize = 33;
#[cfg(target_arch = "x86_64")]
const SYS_CHDIR: usize = 80;
#[cfg(target_arch = "x86_64")]
const SYS_EXECVE: usize = 59;
#[cfg(target_arch = "x86_64")]
const SYS_EXIT_GROUP: usize = 231;
#[cfg(target_arch = "x86_64")]
const SYS_WAIT4: usize = 61;
#[cfg(target_arch = "x86_64")]
const SYS_KILL: usize = 62;

#[cfg(target_arch = "aarch64")]
const SYS_OPENAT: usize = 56;
#[cfg(target_arch = "aarch64")]
const SYS_IOCTL: usize = 29;
#[cfg(target_arch = "aarch64")]
const SYS_CLOSE: usize = 57;
#[cfg(target_arch = "aarch64")]
const SYS_DUP: usize = 23;
#[cfg(target_arch = "aarch64")]
const SYS_CLONE: usize = 220;
#[cfg(target_arch = "aarch64")]
const SYS_SETSID: usize = 157;
#[cfg(target_arch = "aarch64")]
const SYS_DUP3: usize = 24;
#[cfg(target_arch = "aarch64")]
const SYS_CHDIR: usize = 49;
#[cfg(target_arch = "aarch64")]
const SYS_EXECVE: usize = 221;
#[cfg(target_arch = "aarch64")]
const SYS_EXIT_GROUP: usize = 94;
#[cfg(target_arch = "aarch64")]
const SYS_WAIT4: usize = 260;
#[cfg(target_arch = "aarch64")]
const SYS_KILL: usize = 129;

fn errno(result: isize) -> i32 {
    if result < 0 && result >= -4095 {
        (-result) as i32
    } else {
        0
    }
}

fn find_session(id: i32) -> Option<usize> {
    if id <= 0 || id as usize > SESSION_COUNT {
        return None;
    }
    let index = id as usize - 1;
    if unsafe { core::ptr::addr_of!(SESSIONS[index]).read() }.pid != 0 {
        Some(index)
    } else {
        None
    }
}

fn child_exit() -> ! {
    unsafe {
        syscall6(SYS_EXIT_GROUP, 127, 0, 0, 0, 0, 0);
    }
    loop {
        core::hint::spin_loop();
    }
}

pub fn spawn(config: &mut [u8], cols: i32, rows: i32) -> Result<SpawnResult, i32> {
    let spec = SpawnSpec::parse(config)?;
    let Some(index) =
        (0..SESSION_COUNT).find(|&i| unsafe { core::ptr::addr_of!(SESSIONS[i]).read().pid == 0 })
    else {
        return Err(24);
    };

    let master = unsafe {
        syscall6(
            SYS_OPENAT,
            (-100isize) as usize,
            b"/dev/ptmx\0".as_ptr() as usize,
            0x80000 | 0x100 | 2,
            0,
            0,
            0,
        )
    };
    if errno(master) != 0 {
        return Err(errno(master));
    }
    let master = master as i32;

    let mut unlock: i32 = 0;
    let unlock_result = unsafe {
        syscall6(
            SYS_IOCTL,
            master as usize,
            0x40045431,
            &mut unlock as *mut i32 as usize,
            0,
            0,
            0,
        )
    };
    if errno(unlock_result) != 0 {
        unsafe { syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0) };
        return Err(errno(unlock_result));
    }

    let mut pty_number: u32 = 0;
    let number_result = unsafe {
        syscall6(
            SYS_IOCTL,
            master as usize,
            0x80045430,
            &mut pty_number as *mut u32 as usize,
            0,
            0,
            0,
        )
    };
    if errno(number_result) != 0 {
        unsafe { syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0) };
        return Err(errno(number_result));
    }

    let mut slave_path = [0u8; 32];
    let prefix = b"/dev/pts/";
    slave_path[..prefix.len()].copy_from_slice(prefix);
    let mut digits = [0u8; 10];
    let mut digit_count = 0;
    let mut n = pty_number;
    loop {
        digits[digit_count] = b'0' + (n % 10) as u8;
        digit_count += 1;
        n /= 10;
        if n == 0 {
            break;
        }
    }
    for i in 0..digit_count {
        slave_path[prefix.len() + i] = digits[digit_count - i - 1];
    }
    let path_len = prefix.len() + digit_count;
    slave_path[path_len] = 0;

    let winsize = Winsize {
        rows: rows.clamp(1, u16::MAX as i32) as u16,
        cols: cols.clamp(1, u16::MAX as i32) as u16,
        xpixel: 0,
        ypixel: 0,
    };
    let size_result = unsafe {
        syscall6(
            SYS_IOCTL,
            master as usize,
            0x5414,
            &winsize as *const Winsize as usize,
            0,
            0,
            0,
        )
    };
    if errno(size_result) != 0 {
        unsafe { syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0) };
        return Err(errno(size_result));
    }

    let read_fd = unsafe { syscall6(SYS_DUP, master as usize, 0, 0, 0, 0, 0) };
    if errno(read_fd) != 0 {
        unsafe { syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0) };
        return Err(errno(read_fd));
    }
    let write_fd = unsafe { syscall6(SYS_DUP, master as usize, 0, 0, 0, 0, 0) };
    if errno(write_fd) != 0 {
        unsafe {
            syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0);
            syscall6(SYS_CLOSE, read_fd as usize, 0, 0, 0, 0, 0);
        }
        return Err(errno(write_fd));
    }

    #[cfg(target_arch = "x86_64")]
    let child = unsafe { syscall6(SYS_FORK, 0, 0, 0, 0, 0, 0) };
    #[cfg(target_arch = "aarch64")]
    let child = unsafe { syscall6(SYS_CLONE, 17, 0, 0, 0, 0, 0) };
    if errno(child) != 0 {
        unsafe {
            syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0);
            syscall6(SYS_CLOSE, read_fd as usize, 0, 0, 0, 0, 0);
            syscall6(SYS_CLOSE, write_fd as usize, 0, 0, 0, 0, 0);
        }
        return Err(errno(child));
    }
    if child == 0 {
        let session_id = unsafe { syscall6(SYS_SETSID, 0, 0, 0, 0, 0, 0) };
        if errno(session_id) != 0 {
            child_exit();
        }
        unsafe {
            syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0);
            syscall6(SYS_CLOSE, read_fd as usize, 0, 0, 0, 0, 0);
            syscall6(SYS_CLOSE, write_fd as usize, 0, 0, 0, 0, 0);
        }
        let slave = unsafe {
            syscall6(
                SYS_OPENAT,
                (-100isize) as usize,
                slave_path.as_ptr() as usize,
                2 | 0x100,
                0,
                0,
                0,
            )
        };
        if errno(slave) != 0 {
            child_exit();
        }
        if errno(unsafe { syscall6(SYS_IOCTL, slave as usize, 0x540e, 0, 0, 0, 0) }) != 0 {
            child_exit();
        }
        unsafe {
            for fd in 0..=2 {
                #[cfg(target_arch = "x86_64")]
                let result = syscall6(SYS_DUP2, slave as usize, fd, 0, 0, 0, 0);
                #[cfg(target_arch = "aarch64")]
                let result = syscall6(SYS_DUP3, slave as usize, fd, 0, 0, 0, 0);
                if errno(result) != 0 {
                    child_exit();
                }
            }
            if slave > 2 {
                syscall6(SYS_CLOSE, slave as usize, 0, 0, 0, 0, 0);
            }
            if !spec.cwd.is_null() {
                if errno(syscall6(SYS_CHDIR, spec.cwd as usize, 0, 0, 0, 0, 0)) != 0 {
                    child_exit();
                }
            }
            let result = syscall6(
                SYS_EXECVE,
                spec.program as usize,
                spec.argv.as_ptr() as usize,
                spec.envp.as_ptr() as usize,
                0,
                0,
                0,
            );
            if errno(result) != 0 {
                child_exit();
            }
        }
        child_exit();
    }

    unsafe {
        syscall6(SYS_CLOSE, master as usize, 0, 0, 0, 0, 0);
        core::ptr::addr_of_mut!(SESSIONS[index]).write(Session {
            pid: child as i32,
            read_fd: read_fd as i32,
            write_fd: write_fd as i32,
            ..EMPTY
        });
    }
    Ok(SpawnResult::new(
        index as i32 + 1,
        child as i32,
        read_fd as i32,
        write_fd as i32,
        b"",
        b"",
    ))
}

pub fn resize(id: i32, cols: i32, rows: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    if cols <= 0 || rows <= 0 {
        return EINVAL;
    }
    let session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if session.pty_closed {
        return 9;
    }
    let winsize = Winsize {
        rows: rows.min(u16::MAX as i32) as u16,
        cols: cols.min(u16::MAX as i32) as u16,
        xpixel: 0,
        ypixel: 0,
    };
    errno(unsafe {
        syscall6(
            SYS_IOCTL,
            session.read_fd as usize,
            0x5414,
            &winsize as *const Winsize as usize,
            0,
            0,
            0,
        )
    })
}

pub fn poll(id: i32) -> i64 {
    let Some(index) = find_session(id) else {
        return -9;
    };
    let mut session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if session.exited {
        return (1i64 << 32) | session.exit_code as u32 as i64;
    }
    let mut status = 0i32;
    let result = unsafe {
        syscall6(
            SYS_WAIT4,
            session.pid as usize,
            &mut status as *mut i32 as usize,
            1,
            0,
            0,
            0,
        )
    };
    if result == 0 {
        return 0;
    }
    if errno(result) != 0 {
        return -(errno(result) as i64);
    }
    session.exited = true;
    session.exit_code = if status & 0x7f == 0 {
        (status >> 8) & 0xff
    } else {
        128 + (status & 0x7f)
    };
    unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(session) };
    (1i64 << 32) | session.exit_code as u32 as i64
}

pub fn signal(id: i32, signum: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    let session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    errno(unsafe { syscall6(SYS_KILL, session.pid as usize, signum as usize, 0, 0, 0, 0) })
}

pub fn close_pty(id: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    let mut session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if session.pty_closed {
        return 0;
    }
    unsafe {
        syscall6(SYS_CLOSE, session.read_fd as usize, 0, 0, 0, 0, 0);
        syscall6(SYS_CLOSE, session.write_fd as usize, 0, 0, 0, 0, 0);
    }
    session.pty_closed = true;
    unsafe {
        if session.child_closed && session.exited {
            core::ptr::addr_of_mut!(SESSIONS[index]).write(EMPTY);
        } else {
            core::ptr::addr_of_mut!(SESSIONS[index]).write(session);
        }
    }
    0
}

pub fn close_child(id: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    let mut session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    session.child_closed = true;
    if session.exited && session.pty_closed {
        unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(EMPTY) };
    } else {
        unsafe { core::ptr::addr_of_mut!(SESSIONS[index]).write(session) };
    }
    0
}

#[repr(C)]
struct Winsize {
    rows: u16,
    cols: u16,
    xpixel: u16,
    ypixel: u16,
}
