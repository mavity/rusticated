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
    fn fcntl(fd: i32, cmd: i32, ...) -> i32;
    fn socket(domain: i32, type_: i32, protocol: i32) -> i32;
    fn bind(fd: i32, addr: *const u8, len: u32) -> i32;
    fn listen(fd: i32, backlog: i32) -> i32;
    fn accept(fd: i32, addr: *mut u8, len: *mut u32) -> i32;
    fn connect(fd: i32, addr: *const u8, len: u32) -> i32;
    fn getsockopt(fd: i32, level: i32, name: i32, val: *mut core::ffi::c_void, len: *mut u32) -> i32;
    fn setsockopt(fd: i32, level: i32, name: i32, val: *const core::ffi::c_void, len: u32) -> i32;
}

const EVFILT_READ: i16 = -1;
const EVFILT_WRITE: i16 = -2;
const EV_ADD: u16 = 0x0001;
const EV_ONESHOT: u16 = 0x0010;

// ── Status ───────────────────────────────────────────────────────────────────

#[repr(C)]
struct sockaddr_in {
    sin_len: u8,
    sin_family: u8,
    sin_port: u16,
    sin_addr: [u8; 4],
    sin_zero: [u8; 8],
}

#[repr(C)]
struct sockaddr_in6 {
    sin6_len: u8,
    sin6_family: u8,
    sin6_port: u16,
    sin6_flowinfo: u32,
    sin6_addr: [u8; 16],
    sin6_scope_id: u32,
}

const AF_INET: i32 = 2;
const AF_INET6: i32 = 30; // macOS AF_INET6 is 30
const SOCK_STREAM: i32 = 1;
const IPPROTO_TCP: i32 = 6;
const F_GETFL: i32 = 3;
const F_SETFL: i32 = 4;
const O_NONBLOCK: i32 = 0x0004;
const SOL_SOCKET: i32 = 0xffff;
const SO_REUSEADDR: i32 = 0x0004;
const SO_ERROR: i32 = 0x1007;

// ── Networking Futures ───────────────────────────────────────────────────────

use crate::net::SocketAddr;

/// Future for connecting a TCP stream on BSD/macOS.
pub struct TcpConnect {
    addr: SocketAddr,
    handle: Option<i32>,
    started: bool,
}

impl TcpConnect {
    pub fn new(addr: SocketAddr) -> Self {
        Self {
            addr,
            handle: None,
            started: false,
        }
    }
}

impl Future for TcpConnect {
    type Output = io::Result<crate::net::TcpStream>;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            let (af, addr_buf, addr_len) = match self.addr {
                SocketAddr::V4(ref a) => {
                    let sin = sockaddr_in {
                        sin_len: core::mem::size_of::<sockaddr_in>() as u8,
                        sin_family: AF_INET as u8,
                        sin_port: a.port().to_be(),
                        sin_addr: a.ip().octets(),
                        sin_zero: [0; 8],
                    };
                    let (ptr, len) = unsafe {
                        let p = alloc::boxed::Box::into_raw(alloc::boxed::Box::new(sin));
                        (p as *const u8, core::mem::size_of::<sockaddr_in>())
                    };
                    (AF_INET, ptr, len as u32)
                }
                SocketAddr::V6(ref a) => {
                    let mut sin6_addr = [0u8; 16];
                    let segments = a.ip().segments();
                    for i in 0..8 {
                        sin6_addr[i * 2] = (segments[i] >> 8) as u8;
                        sin6_addr[i * 2 + 1] = (segments[i] & 0xFF) as u8;
                    }
                    let sin6 = sockaddr_in6 {
                        sin6_len: core::mem::size_of::<sockaddr_in6>() as u8,
                        sin6_family: AF_INET6 as u8,
                        sin6_port: a.port().to_be(),
                        sin6_flowinfo: 0,
                        sin6_addr,
                        sin6_scope_id: 0,
                    };
                    let (ptr, len) = unsafe {
                        let p = alloc::boxed::Box::into_raw(alloc::boxed::Box::new(sin6));
                        (p as *const u8, core::mem::size_of::<sockaddr_in6>())
                    };
                    (AF_INET6, ptr, len as u32)
                }
            };

            let s = unsafe { socket(af, SOCK_STREAM, IPPROTO_TCP) };
            if s < 0 {
                unsafe {
                    if af == AF_INET {
                        drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in));
                    } else {
                        drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in6));
                    }
                }
                return Poll::Ready(Err(io::Error::last_os_error()));
            }

            // Set non-blocking
            unsafe {
                let flags = fcntl(s, F_GETFL, 0);
                fcntl(s, F_SETFL, flags | O_NONBLOCK);
            }

            let res = unsafe { connect(s, addr_buf, addr_len) };
            
            unsafe {
                if af == AF_INET {
                    drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in));
                } else {
                    drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in6));
                }
            }

            if res == 0 {
                return Poll::Ready(Ok(crate::net::TcpStream { handle: s as u64 }));
            }

            let err = io::Error::last_os_error();
            if err.raw_os_error() != Some(36) /* EINPROGRESS */ {
                unsafe { close(s) };
                return Poll::Ready(Err(err));
            }

            self.handle = Some(s);
            self.started = true;
        }

        let fd = self.handle.unwrap();
        let mut wait = wait_writable(fd);
        match Pin::new(&mut wait).poll(cx) {
            Poll::Ready(Ok(())) => {
                let mut err = 0i32;
                let mut len = core::mem::size_of::<i32>() as u32;
                let res = unsafe { getsockopt(fd, SOL_SOCKET, SO_ERROR, &mut err as *mut _ as *mut core::ffi::c_void, &mut len) };
                if res == 0 && err == 0 {
                    Poll::Ready(Ok(crate::net::TcpStream { handle: fd as u64 }))
                } else {
                    unsafe { close(fd) };
                    if err != 0 {
                        Poll::Ready(Err(io::Error::from_raw_os_error(err)))
                    } else {
                        Poll::Ready(Err(io::Error::last_os_error()))
                    }
                }
            }
            Poll::Ready(Err(e)) => {
                unsafe { close(fd) };
                Poll::Ready(Err(e))
            }
            Poll::Pending => Poll::Pending,
        }
    }
}

/// Future for binding a TCP listener on BSD/macOS.
pub struct TcpListenerBind {
    addr: SocketAddr,
}

impl TcpListenerBind {
    pub fn new(addr: SocketAddr) -> Self {
        Self { addr }
    }
}

impl Future for TcpListenerBind {
    type Output = io::Result<crate::net::TcpListener>;

    fn poll(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Self::Output> {
        let (af, addr_buf, addr_len) = match self.addr {
            SocketAddr::V4(ref a) => {
                let sin = sockaddr_in {
                    sin_len: core::mem::size_of::<sockaddr_in>() as u8,
                    sin_family: AF_INET as u8,
                    sin_port: a.port().to_be(),
                    sin_addr: a.ip().octets(),
                    sin_zero: [0; 8],
                };
                let (ptr, len) = unsafe {
                    let p = alloc::boxed::Box::into_raw(alloc::boxed::Box::new(sin));
                    (p as *const u8, core::mem::size_of::<sockaddr_in>())
                };
                (AF_INET, ptr, len as u32)
            }
            SocketAddr::V6(ref a) => {
                let mut sin6_addr = [0u8; 16];
                let segments = a.ip().segments();
                for i in 0..8 {
                    sin6_addr[i * 2] = (segments[i] >> 8) as u8;
                    sin6_addr[i * 2 + 1] = (segments[i] & 0xFF) as u8;
                }
                let sin6 = sockaddr_in6 {
                    sin6_len: core::mem::size_of::<sockaddr_in6>() as u8,
                    sin6_family: AF_INET6 as u8,
                    sin6_port: a.port().to_be(),
                    sin6_flowinfo: 0,
                    sin6_addr,
                    sin6_scope_id: 0,
                };
                let (ptr, len) = unsafe {
                    let p = alloc::boxed::Box::into_raw(alloc::boxed::Box::new(sin6));
                    (p as *const u8, core::mem::size_of::<sockaddr_in6>())
                };
                (AF_INET6, ptr, len as u32)
            }
        };

        let s = unsafe { socket(af, SOCK_STREAM, IPPROTO_TCP) };
        if s < 0 {
            unsafe {
                if af == AF_INET {
                    drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in));
                } else {
                    drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in6));
                }
            }
            return Poll::Ready(Err(io::Error::last_os_error()));
        }

        let on = 1i32;
        unsafe {
            setsockopt(s, SOL_SOCKET, SO_REUSEADDR, &on as *const _ as *const core::ffi::c_void, core::mem::size_of::<i32>() as u32);
        }

        let res = unsafe { bind(s, addr_buf, addr_len) };
        unsafe {
            if af == AF_INET {
                drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in));
            } else {
                drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in6));
            }
        }

        if res < 0 {
            let err = io::Error::last_os_error();
            unsafe { close(s) };
            return Poll::Ready(Err(err));
        }

        let res = unsafe { listen(s, 128) };
        if res < 0 {
            let err = io::Error::last_os_error();
            unsafe { close(s) };
            return Poll::Ready(Err(err));
        }

        // Set non-blocking
        unsafe {
            let flags = fcntl(s, F_GETFL, 0);
            fcntl(s, F_SETFL, flags | O_NONBLOCK);
        }

        Poll::Ready(Ok(crate::net::TcpListener { handle: s as u64 }))
    }
}

/// Future for accepting a connection on a TCP listener on BSD/macOS.
pub struct TcpAccept {
    fd: i32,
}

impl TcpAccept {
    pub fn new(fd: i32) -> Self {
        Self { fd }
    }
}

impl Future for TcpAccept {
    type Output = io::Result<(crate::net::TcpStream, crate::net::SocketAddr)>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let mut addr_buf = [0u8; 128];
        let mut addr_len = 128u32;

        let res = unsafe { accept(self.fd, addr_buf.as_mut_ptr(), &mut addr_len) };
        if res >= 0 {
            let addr = crate::net::SocketAddr::V4(crate::net::SocketAddrV4::new(crate::net::Ipv4Addr::new(0,0,0,0), 0));
            return Poll::Ready(Ok((crate::net::TcpStream { handle: res as u64 }, addr)));
        }

        let err = io::Error::last_os_error();
        if err.raw_os_error() != Some(35) /* EAGAIN */ {
            return Poll::Ready(Err(err));
        }

        let mut wait = wait_readable(self.fd);
        match Pin::new(&mut wait).poll(cx) {
            Poll::Ready(Ok(())) => {
                cx.waker().wake_by_ref();
                Poll::Pending
            }
            Poll::Ready(Err(e)) => Poll::Ready(Err(e)),
            Poll::Pending => Poll::Pending,
        }
    }
}

/// Future for receiving data from a TCP stream on BSD/macOS.
pub struct OverlappedRecv {
    fd: i32,
    buf: Vec<u8>,
}

impl OverlappedRecv {
    pub fn new(fd: u64, buf: Vec<u8>) -> Self {
        Self { fd: fd as i32, buf }
    }
}

impl Future for OverlappedRecv {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        unsafe {
            extern "C" {
                fn read(fd: i32, buf: *mut u8, len: usize) -> isize;
            }
            let res = read(self.fd, self.buf.as_mut_ptr(), self.buf.capacity());
            if res >= 0 {
                unsafe { self.buf.set_len(res as usize); }
                return Poll::Ready((Ok(res as usize), core::mem::take(&mut self.buf)));
            }
            let err = io::Error::last_os_error();
            if err.raw_os_error() != Some(35) /* EAGAIN */ {
                return Poll::Ready((Err(err), core::mem::take(&mut self.buf)));
            }
        }

        let mut wait = wait_readable(self.fd);
        match Pin::new(&mut wait).poll(cx) {
            Poll::Ready(Ok(())) => {
                cx.waker().wake_by_ref();
                Poll::Pending
            }
            Poll::Ready(Err(e)) => Poll::Ready((Err(e), core::mem::take(&mut self.buf))),
            Poll::Pending => Poll::Pending,
        }
    }
}

/// Future for sending data to a TCP stream on BSD/macOS.
pub struct OverlappedSend {
    fd: i32,
    buf: Vec<u8>,
}

impl OverlappedSend {
    pub fn new(fd: u64, buf: Vec<u8>) -> Self {
        Self { fd: fd as i32, buf }
    }
}

impl Future for OverlappedSend {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let this = self.get_mut();
        unsafe {
            extern "C" {
                fn write(fd: i32, buf: *const u8, len: usize) -> isize;
            }
            let res = write(this.fd, this.buf.as_ptr(), this.buf.len());
            if res >= 0 {
                return Poll::Ready((Ok(res as usize), core::mem::take(&mut this.buf)));
            }
            let err = io::Error::last_os_error();
            if err.raw_os_error() != Some(35) /* EAGAIN */ {
                return Poll::Ready((Err(err), core::mem::take(&mut this.buf)));
            }
        }

        let mut wait = wait_writable(this.fd);
        match Pin::new(&mut wait).poll(cx) {
            Poll::Ready(Ok(())) => {
                cx.waker().wake_by_ref();
                Poll::Pending
            }
            Poll::Ready(Err(e)) => Poll::Ready((Err(e), core::mem::take(&mut this.buf))),
            Poll::Pending => Poll::Pending,
        }
    }
}



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

    /// Polls the BSD event queue for readiness with an optional timeout.
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
