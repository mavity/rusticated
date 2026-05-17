#![cfg(target_os = "linux")]

use std::{
    collections::HashMap,
    future::Future,
    io,
    pin::Pin,
    task::{Context, Poll},
};

use super::executor::with_driver;
use super::ready::{consume_ready, mark_ready};

// â”€â”€ epoll constants â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

const EPOLLIN: u32 = 0x0001;
const EPOLLOUT: u32 = 0x0004;
const EPOLLONESHOT: u32 = 1 << 30;
const EPOLL_CTL_ADD: i32 = 1;
const EPOLL_CTL_MOD: i32 = 3;
/// POSIX errno 17: file exists (returned by EPOLL_CTL_ADD on a known fd).
const EEXIST: i32 = 17;

/// `epoll_event` layout: packed 4+8 bytes on x86_64/aarch64 Linux.
#[repr(C, packed)]
#[derive(Copy, Clone)]
struct EpollEvent {
    events: u32,
    data: u64,
}

unsafe extern "C" {
    fn epoll_create1(flags: i32) -> i32;
    fn epoll_ctl(epfd: i32, op: i32, fd: i32, event: *mut EpollEvent) -> i32;
    fn epoll_wait(epfd: i32, events: *mut EpollEvent, maxevents: i32, timeout: i32) -> i32;
    pub(crate) fn close(fd: i32) -> i32;
}

// â”€â”€ Driver â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

/// Linux epoll driver.
pub struct Driver {
    epfd: i32,
    /// fds currently registered (even if ONESHOT-disabled) so we can
    /// choose `EPOLL_CTL_MOD` vs `EPOLL_CTL_ADD` correctly.
    registered_fds: HashMap<i32, ()>,
    next_token: u64,
}

impl Driver {
    /// Create a new epoll instance.
    pub fn new() -> io::Result<Self> {
        // SAFETY: FFI call with no precondition.
        let epfd = unsafe { epoll_create1(0) };
        if epfd < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self {
            epfd,
            registered_fds: HashMap::new(),
            next_token: 1,
        })
    }

    fn do_register(&mut self, fd: i32, events: u32, token: u64) -> io::Result<()> {
        let mut ev = EpollEvent {
            events: events | EPOLLONESHOT,
            data: token,
        };
        let op = if self.registered_fds.contains_key(&fd) {
            EPOLL_CTL_MOD
        } else {
            EPOLL_CTL_ADD
        };
        // SAFETY: `&mut ev` is a valid pointer for the call duration.
        let r = unsafe { epoll_ctl(self.epfd, op, fd, &mut ev) };
        if r < 0 {
            let e = io::Error::last_os_error();
            if e.raw_os_error() == Some(EEXIST) {
                // Race: try MOD instead.
                // SAFETY: same as above.
                let r2 = unsafe { epoll_ctl(self.epfd, EPOLL_CTL_MOD, fd, &mut ev) };
                if r2 < 0 {
                    return Err(io::Error::last_os_error());
                }
            } else {
                return Err(e);
            }
        }
        self.registered_fds.insert(fd, ());
        Ok(())
    }

    /// Register `fd` for readability; returns the unique token.
    pub fn register_read(&mut self, fd: i32) -> io::Result<u64> {
        let token = self.next_token;
        self.next_token += 1;
        self.do_register(fd, EPOLLIN, token)?;
        Ok(token)
    }

    /// Register `fd` for writability; returns the unique token.
    pub fn register_write(&mut self, fd: i32) -> io::Result<u64> {
        let token = self.next_token;
        self.next_token += 1;
        self.do_register(fd, EPOLLOUT, token)?;
        Ok(token)
    }

    /// Poll for already-ready events without blocking.
    ///
    /// Returns `true` if at least one event was processed.
    pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
        let mut evbuf = [EpollEvent { events: 0, data: 0 }; 64];
        let n = loop {
            // SAFETY: pointer + length describe the local array.
            let n = unsafe {
                epoll_wait(
                    self.epfd,
                    evbuf.as_mut_ptr(),
                    evbuf.len() as i32,
                    0, // non-blocking
                )
            };
            if n >= 0 {
                break n;
            }
            let e = io::Error::last_os_error();
            if e.kind() == io::ErrorKind::Interrupted {
                continue;
            }
            return Err(e);
        };
        for ev in &evbuf[..n as usize] {
            // ONESHOT fired: the fd is now disabled (not removed).
            mark_ready(ev.data);
        }
        Ok(n > 0)
    }
}

impl Drop for Driver {
    fn drop(&mut self) {
        // SAFETY: `close` on a valid fd is sound.
        unsafe { close(self.epfd) };
    }
}

// â”€â”€ WaitReadable â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

/// Future that resolves when `fd` becomes readable.
pub struct WaitReadable {
    fd: i32,
    token: u64,
    registered: bool,
}

impl WaitReadable {
    /// Create a new future for `fd`.
    pub fn new(fd: i32) -> Self {
        Self {
            fd,
            token: 0,
            registered: false,
        }
    }
}

impl Future for WaitReadable {
    type Output = io::Result<()>;

    fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.registered {
            return if consume_ready(self.token) {
                Poll::Ready(Ok(()))
            } else {
                Poll::Pending
            };
        }
        match with_driver(|d| d.register_read(self.fd)) {
            Ok(Ok(token)) => {
                self.token = token;
                self.registered = true;
                Poll::Pending
            }
            Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
        }
    }
}

// â”€â”€ WaitWritable â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

/// Future that resolves when `fd` becomes writable.
pub struct WaitWritable {
    fd: i32,
    token: u64,
    registered: bool,
}

impl WaitWritable {
    /// Create a new future for `fd`.
    pub fn new(fd: i32) -> Self {
        Self {
            fd,
            token: 0,
            registered: false,
        }
    }
}

impl Future for WaitWritable {
    type Output = io::Result<()>;

    fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.registered {
            return if consume_ready(self.token) {
                Poll::Ready(Ok(()))
            } else {
                Poll::Pending
            };
        }
        match with_driver(|d| d.register_write(self.fd)) {
            Ok(Ok(token)) => {
                self.token = token;
                self.registered = true;
                Poll::Pending
            }
            Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
        }
    }
}