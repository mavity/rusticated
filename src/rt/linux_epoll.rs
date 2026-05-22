#![cfg(target_os = "linux")]

use crate::collections::HashMap;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll};

use super::executor::with_driver;
use super::ready::{consume_ready, mark_ready};

// -- epoll constants
// -------------------------------------------------------

const EPOLLIN: u32 = 0x0001;
const EPOLLOUT: u32 = 0x0004;
const EPOLLONESHOT: u32 = 1 << 30;
const EPOLL_CTL_ADD: i32 = 1;
const EPOLL_CTL_MOD: i32 = 3;
/// POSIX errno 17: file exists (returned by EPOLL_CTL_ADD on a known fd).
const EEXIST: i32 = 17;

/// `epoll_event` layout: the kernel uses `__attribute__((packed))` only on
/// x86/x86_64. On aarch64/riscv64/etc. the struct has natural alignment —
/// `data: u64` sits at offset 8 (4 bytes padding after `events: u32`).
#[cfg_attr(any(target_arch = "x86", target_arch = "x86_64"), repr(C, packed))]
#[cfg_attr(not(any(target_arch = "x86", target_arch = "x86_64")), repr(C))]
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

// - Driver
// ----------------------------------------------------------------

/// Linux epoll driver.
pub struct Driver {
    epfd: i32,
    /// fds currently registered (even if ONESHOT-disabled) so we can
    /// choose `EPOLL_CTL_MOD` vs `EPOLL_CTL_ADD` correctly.
    registered_fds: HashMap<i32, ()>,
    /// Wakers indexed by completion token; fired when epoll reports readiness.
    wakers: HashMap<u64, crate::task::Waker>,
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
            wakers: HashMap::new(),
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

    /// Store a waker to be called when `token` next fires.
    pub(crate) fn register_waker(&mut self, token: u64, waker: crate::task::Waker) {
        self.wakers.insert(token, waker);
    }

    pub fn poll_with_timeout(&mut self, timeout_ms: Option<u32>) -> io::Result<bool> {
        let mut evbuf = [EpollEvent { events: 0, data: 0 }; 64];
        let timeout = timeout_ms.map(|t| t as i32).unwrap_or(-1);
        let n = loop {
            // SAFETY: pointer + length describe the local array.
            let n =
                unsafe { epoll_wait(self.epfd, evbuf.as_mut_ptr(), evbuf.len() as i32, timeout) };
            if n >= 0 {
                break n;
            }
            let e = io::Error::last_os_error();
            if e.kind() == crate::io::ErrorKind::Interrupted {
                continue;
            }
            return Err(e);
        };
        for ev in &evbuf[..n as usize] {
            // SAFETY: `data` is a field of a `#[repr(C, packed)]` struct;
            // we copy it via `read_unaligned` to avoid a misaligned reference.
            let token = unsafe { core::ptr::read_unaligned(core::ptr::addr_of!(ev.data)) };
            // ONESHOT fired: the fd is now disabled (not removed).
            mark_ready(token);
            if let Some(waker) = self.wakers.remove(&token) {
                waker.wake();
            }
        }
        Ok(n > 0)
    }

    /// Poll for already-ready events without blocking.
    ///
    /// Returns `true` if at least one event was processed.
    pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
        self.poll_with_timeout(Some(0))
    }
}

impl Drop for Driver {
    fn drop(&mut self) {
        // SAFETY: `close` on a valid fd is sound.
        unsafe { close(self.epfd) };
    }
}

// -- WaitReadable
// ----------------------------------------------------------

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

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.registered {
            if consume_ready(self.token) {
                return Poll::Ready(Ok(()));
            }
            let _ = with_driver(|d| d.register_waker(self.token, cx.waker().clone()));
            return Poll::Pending;
        }
        match with_driver(|d| d.register_read(self.fd)) {
            Ok(Ok(token)) => {
                self.token = token;
                self.registered = true;
                let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));
                Poll::Pending
            }
            Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
        }
    }
}

// ── WaitWritable
// ----------------------------------------------------------

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

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if self.registered {
            if consume_ready(self.token) {
                return Poll::Ready(Ok(()));
            }
            let _ = with_driver(|d| d.register_waker(self.token, cx.waker().clone()));
            return Poll::Pending;
        }
        match with_driver(|d| d.register_write(self.fd)) {
            Ok(Ok(token)) => {
                self.token = token;
                self.registered = true;
                let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));
                Poll::Pending
            }
            Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
        }
    }
}
