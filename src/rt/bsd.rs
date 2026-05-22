#![cfg(any(
    target_os = "macos",
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd"
))]

use crate::collections::HashMap;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll, Waker};

use super::executor::with_driver;
use super::ready::{consume_ready, mark_ready};

// ── kqueue FFI ───────────────────────────────────────────────────────────────

#[repr(C)]
#[derive(Copy, Clone)]
struct Kevent {
    /// File descriptor / signal number / etc.
    ident: usize,
    /// Filter type: `EVFILT_READ` or `EVFILT_WRITE`.
    filter: i16,
    /// Action flags: `EV_ADD`, `EV_ONESHOT`, etc.
    flags: u16,
    /// Filter-specific flags (unused here).
    fflags: u32,
    /// Filter-specific data (e.g. bytes available).
    data: isize,
    /// Opaque user data — we store the fd here as token.
    udata: usize,
}

#[repr(C)]
struct Timespec {
    tv_sec: i64,
    tv_nsec: i64,
}

unsafe extern "C" {
    fn kqueue() -> i32;
    fn kevent(
        kq: i32,
        changelist: *const Kevent,
        nchanges: i32,
        eventlist: *mut Kevent,
        nevents: i32,
        timeout: *const Timespec,
    ) -> i32;
    fn close(fd: i32) -> i32;
}

const EVFILT_READ: i16 = -1;
const EVFILT_WRITE: i16 = -2;
const EV_ADD: u16 = 0x0001;
const EV_ONESHOT: u16 = 0x0010;

// ── Driver ───────────────────────────────────────────────────────────────────

/// macOS / BSD kqueue driver.
pub struct Driver {
    /// The kqueue file descriptor.
    kq_fd: i32,
    /// Wakers indexed by token (fd cast to usize).
    wakers: HashMap<usize, Waker>,
}

impl Driver {
    /// Create a new kqueue driver instance.
    pub fn new() -> io::Result<Self> {
        // SAFETY: `kqueue()` has no preconditions.
        let kq_fd = unsafe { kqueue() };
        if kq_fd < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self {
            kq_fd,
            wakers: HashMap::new(),
        })
    }

    fn register_filter(&self, fd: i32, filter: i16) -> io::Result<()> {
        let ev = Kevent {
            ident: fd as usize,
            filter,
            flags: EV_ADD | EV_ONESHOT,
            fflags: 0,
            data: 0,
            udata: fd as usize,
        };
        // SAFETY: `ev` is a valid local struct; `kevent` with nchanges=1 and
        // nevents=0 just submits without dequeuing.
        let r = unsafe {
            kevent(
                self.kq_fd,
                &ev,
                1,
                core::ptr::null_mut(),
                0,
                core::ptr::null(),
            )
        };
        if r < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    /// Register `fd` for readability; token = fd as usize.
    pub fn register_read(&self, fd: i32) -> io::Result<()> {
        self.register_filter(fd, EVFILT_READ)
    }

    /// Register `fd` for writability; token = fd as usize.
    pub fn register_write(&self, fd: i32) -> io::Result<()> {
        self.register_filter(fd, EVFILT_WRITE)
    }

    /// Store a waker to be called when the fd (as token) next fires.
    pub(crate) fn register_waker(&mut self, token: usize, waker: Waker) {
        self.wakers.insert(token, waker);
    }

    pub fn poll_with_timeout(&mut self, timeout_ms: Option<u32>) -> io::Result<bool> {
        let ts;
        let ts_ptr = match timeout_ms {
            Some(ms) => {
                ts = Timespec {
                    tv_sec: (ms / 1000) as i64,
                    tv_nsec: ((ms % 1000) * 1_000_000) as i64,
                };
                &ts as *const Timespec
            }
            None => core::ptr::null(),
        };
        let mut evbuf = [Kevent {
            ident: 0,
            filter: 0,
            flags: 0,
            fflags: 0,
            data: 0,
            udata: 0,
        }; 64];
        let n = unsafe {
            kevent(
                self.kq_fd,
                core::ptr::null(),
                0,
                evbuf.as_mut_ptr(),
                evbuf.len() as i32,
                ts_ptr,
            )
        };
        if n < 0 {
            let e = io::Error::last_os_error();
            if e.kind() == io::ErrorKind::Interrupted {
                return Ok(false);
            }
            return Err(e);
        }
        for i in 0..n {
            let ev = &evbuf[i as usize];
            let token = ev.udata as usize;
            mark_ready(token as u64);
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
        // SAFETY: `kq_fd` is a valid fd owned by this Driver.
        unsafe { close(self.kq_fd) };
    }
}

// ── WaitReadable ─────────────────────────────────────────────────────────────

/// Future that resolves when `fd` becomes readable (kqueue `EVFILT_READ`).
pub struct WaitReadable {
    fd: i32,
    registered: bool,
}

impl WaitReadable {
    /// Create a new `WaitReadable` future for the given file descriptor.
    pub fn new(fd: i32) -> Self {
        Self {
            fd,
            registered: false,
        }
    }
}

impl Future for WaitReadable {
    type Output = io::Result<()>;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        let token = self.fd as usize;
        if consume_ready(token as u64) {
            return Poll::Ready(Ok(()));
        }

        // Store waker before registering so we never miss a completion.
        let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));

        if !self.registered {
            if let Err(e) = with_driver(|d| d.register_read(self.fd))
                .unwrap_or_else(|| Err(io::Error::other("kqueue: no driver")))
            {
                return Poll::Ready(Err(e));
            }
            self.registered = true;
        }

        Poll::Pending
    }
}

// ── WaitWritable ─────────────────────────────────────────────────────────────

/// Future that resolves when `fd` becomes writable (kqueue `EVFILT_WRITE`).
pub struct WaitWritable {
    fd: i32,
    registered: bool,
}

impl WaitWritable {
    /// Create a new `WaitWritable` future for the given file descriptor.
    pub fn new(fd: i32) -> Self {
        Self {
            fd,
            registered: false,
        }
    }
}

impl Future for WaitWritable {
    type Output = io::Result<()>;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        let token = self.fd as usize;
        if consume_ready(token as u64) {
            return Poll::Ready(Ok(()));
        }

        let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));

        if !self.registered {
            if let Err(e) = with_driver(|d| d.register_write(self.fd))
                .unwrap_or_else(|| Err(io::Error::other("kqueue: no driver")))
            {
                return Poll::Ready(Err(e));
            }
            self.registered = true;
        }

        Poll::Pending
    }
}
