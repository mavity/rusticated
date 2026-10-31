use super::{EINVAL, SpawnResult, SpawnSpec};
use core::ffi::c_char;

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

#[link(name = "System")]
unsafe extern "C" {
    fn posix_openpt(flags: i32) -> i32;
    fn grantpt(fd: i32) -> i32;
    fn unlockpt(fd: i32) -> i32;
    fn ptsname(fd: i32) -> *const c_char;
    fn fork() -> i32;
    fn setsid() -> i32;
    fn open(path: *const c_char, flags: i32, ...) -> i32;
    fn ioctl(fd: i32, request: u64, ...) -> i32;
    fn close(fd: i32) -> i32;
    fn dup(fd: i32) -> i32;
    fn dup2(fd: i32, new_fd: i32) -> i32;
    fn chdir(path: *const c_char) -> i32;
    fn execve(path: *const c_char, argv: *const *const c_char, envp: *const *const c_char) -> i32;
    fn _exit(status: i32) -> !;
    fn waitpid(pid: i32, status: *mut i32, options: i32) -> i32;
    fn kill(pid: i32, signal: i32) -> i32;
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

fn last_errno() -> i32 {
    unsafe extern "C" {
        fn __error() -> *mut i32;
    }
    unsafe { *__error() }
}

pub fn spawn(config: &mut [u8], cols: i32, rows: i32) -> Result<SpawnResult, i32> {
    let spec = SpawnSpec::parse(config)?;
    let Some(index) =
        (0..SESSION_COUNT).find(|&i| unsafe { core::ptr::addr_of!(SESSIONS[i]).read().pid == 0 })
    else {
        return Err(24);
    };

    let master = unsafe { posix_openpt(2 | 0x20000) };
    if master < 0 {
        return Err(last_errno());
    }
    if unsafe { grantpt(master) } != 0 || unsafe { unlockpt(master) } != 0 {
        let error = last_errno();
        unsafe { close(master) };
        return Err(error);
    }
    let slave_path = unsafe { ptsname(master) };
    if slave_path.is_null() {
        let error = last_errno();
        unsafe { close(master) };
        return Err(error);
    }

    let winsize = Winsize {
        rows: rows.clamp(1, u16::MAX as i32) as u16,
        cols: cols.clamp(1, u16::MAX as i32) as u16,
        xpixel: 0,
        ypixel: 0,
    };
    if unsafe { ioctl(master, 0x80087467, &winsize as *const Winsize) } != 0 {
        let error = last_errno();
        unsafe { close(master) };
        return Err(error);
    }
    let read_fd = unsafe { dup(master) };
    if read_fd < 0 {
        let error = last_errno();
        unsafe { close(master) };
        return Err(error);
    }
    let write_fd = unsafe { dup(master) };
    if write_fd < 0 {
        let error = last_errno();
        unsafe {
            close(master);
            close(read_fd);
        }
        return Err(error);
    }

    let child = unsafe { fork() };
    if child < 0 {
        let error = last_errno();
        unsafe {
            close(master);
            close(read_fd);
            close(write_fd);
        }
        return Err(error);
    }
    if child == 0 {
        unsafe {
            close(master);
            close(read_fd);
            close(write_fd);
            if setsid() < 0 {
                _exit(127);
            }
            let slave = open(slave_path, 2 | 0x20000);
            if slave < 0 || ioctl(slave, 0x20007461, 0) != 0 {
                _exit(127);
            }
            for fd in 0..=2 {
                if dup2(slave, fd) < 0 {
                    _exit(127);
                }
            }
            if slave > 2 {
                close(slave);
            }
            if !spec.cwd.is_null() && chdir(spec.cwd.cast()) != 0 {
                _exit(127);
            }
            execve(
                spec.program.cast(),
                spec.argv.as_ptr().cast(),
                spec.envp.as_ptr().cast(),
            );
            _exit(127);
        }
    }

    unsafe {
        core::ptr::addr_of_mut!(SESSIONS[index]).write(Session {
            pid: child,
            read_fd,
            write_fd,
            ..EMPTY
        });
        close(master);
    }
    Ok(SpawnResult::new(
        index as i32 + 1,
        child,
        read_fd,
        write_fd,
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
    if unsafe { ioctl(session.read_fd, 0x80087467, &winsize as *const Winsize) } == 0 {
        0
    } else {
        last_errno()
    }
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
    let result = unsafe { waitpid(session.pid, &mut status, 1) };
    if result == 0 {
        return 0;
    }
    if result < 0 {
        return -(last_errno() as i64);
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
    if unsafe { kill(session.pid, signum) } == 0 {
        0
    } else {
        last_errno()
    }
}

pub fn close_pty(id: i32) -> i32 {
    let Some(index) = find_session(id) else {
        return 9;
    };
    let mut session = unsafe { core::ptr::addr_of!(SESSIONS[index]).read() };
    if !session.pty_closed {
        unsafe {
            close(session.read_fd);
            close(session.write_fd);
        }
        session.pty_closed = true;
        unsafe {
            if session.child_closed && session.exited {
                core::ptr::addr_of_mut!(SESSIONS[index]).write(EMPTY);
            } else {
                core::ptr::addr_of_mut!(SESSIONS[index]).write(session);
            }
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
