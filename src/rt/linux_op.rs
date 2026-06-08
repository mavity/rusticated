#[cfg(any(target_os = "linux", rusticated_linux))]
use super::linux_state::OpState;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll};
use alloc::rc::Rc;
use alloc::vec::Vec;

/// Future representing a Linux asynchronous file operation.
pub struct LinuxOpFuture {
    state: Rc<OpState>,
}

impl LinuxOpFuture {
    /// Initiates an asynchronous read operation.
    pub fn read(fd: i32, buf: Vec<u8>) -> Self {
        let state = OpState::new(fd, true, Some(buf));
        let state_clone = Rc::clone(&state);
        let _ = super::executor::with_driver(|d| d.submit_read(fd, state_clone));
        Self { state }
    }

    /// Initiates an asynchronous write operation.
    pub fn write(fd: i32, buf: Vec<u8>) -> Self {
        let state = OpState::new(fd, false, Some(buf));
        let state_clone = Rc::clone(&state);
        let _ = super::executor::with_driver(|d| d.submit_write(fd, state_clone));
        Self { state }
    }
}

impl Future for LinuxOpFuture {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let mut result_borrow = self.state.result.borrow_mut();
        if let Some((error_code, bytes)) = result_borrow.take() {
            // Reclaim buffer
            let buffer = unsafe { &mut *self.state.buffer.get() }.take().unwrap();
            if error_code != 0 {
                return Poll::Ready((Err(io::Error::from_raw_os_error(error_code)), buffer));
            } else {
                return Poll::Ready((Ok(bytes as usize), buffer));
            }
        }

        *self.state.waker.borrow_mut() = Some(cx.waker().clone());
        Poll::Pending
    }
}

impl Drop for LinuxOpFuture {
    fn drop(&mut self) {
        if self.state.result.borrow().is_none() {
            let _ = super::executor::with_driver(|d| d.submit_cancel(Rc::clone(&self.state)));
        }
    }
}

// ─── Networking ─────────────────────────────────────────────────────────────

use crate::net::SocketAddr;

#[repr(C)]
struct sockaddr_in {
    sin_family: u16,
    sin_port: u16,
    sin_addr: [u8; 4],
    sin_zero: [u8; 8],
}

#[repr(C)]
struct sockaddr_in6 {
    sin6_family: u16,
    sin6_port: u16,
    sin6_flowinfo: u32,
    sin6_addr: [u8; 16],
    sin6_scope_id: u32,
}

/// Future for connecting a TCP stream on Linux.
pub struct TcpConnect {
    addr: SocketAddr,
    handle: Option<i32>,
    started: bool,
}

impl TcpConnect {
    /// Creates a new TCP connect future.
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
                        sin_family: 2, // AF_INET
                        sin_port: a.port().to_be(),
                        sin_addr: a.ip().octets(),
                        sin_zero: [0; 8],
                    };
                    let (ptr, len) = {
                        let p = alloc::boxed::Box::into_raw(alloc::boxed::Box::new(sin));
                        (p as *const u8, core::mem::size_of::<sockaddr_in>())
                    };
                    (2, ptr, len)
                }
                SocketAddr::V6(ref a) => {
                    let mut sin6_addr = [0u8; 16];
                    let segments = a.ip().segments();
                    for i in 0..8 {
                        sin6_addr[i * 2] = (segments[i] >> 8) as u8;
                        sin6_addr[i * 2 + 1] = (segments[i] & 0xFF) as u8;
                    }
                    let sin6 = sockaddr_in6 {
                        sin6_family: 10, // AF_INET6
                        sin6_port: a.port().to_be(),
                        sin6_flowinfo: 0,
                        sin6_addr,
                        sin6_scope_id: 0,
                    };
                    let (ptr, len) = {
                        let p = alloc::boxed::Box::into_raw(alloc::boxed::Box::new(sin6));
                        (p as *const u8, core::mem::size_of::<sockaddr_in6>())
                    };
                    (10, ptr, len)
                }
            };

            let s = crate::syscall!(crate::os::linux::syscall::nr::SOCKET, af, 1, 6) as i32; // AF, SOCK_STREAM, IPPROTO_TCP
            if s < 0 {
                unsafe {
                    if af == 2 {
                        drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in));
                    } else {
                        drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in6));
                    }
                }
                return Poll::Ready(Err(io::Error::last_os_error()));
            }

            // Set O_NONBLOCK via fcntl
            let flags = crate::syscall!(
                crate::os::linux::syscall::nr::FCNTL,
                s as usize,
                3 /* F_GETFL */
            ) as i32;
            let _ = crate::syscall!(
                crate::os::linux::syscall::nr::FCNTL,
                s as usize,
                4, /* F_SETFL */
                (flags | 0o4000/* O_NONBLOCK */) as usize
            );

            let res = crate::syscall!(
                crate::os::linux::syscall::nr::CONNECT,
                s as usize,
                addr_buf as usize,
                addr_len
            ) as isize;

            unsafe {
                if af == 2 {
                    drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in));
                } else {
                    drop(alloc::boxed::Box::from_raw(addr_buf as *mut sockaddr_in6));
                }
            }

            if res == 0 {
                return Poll::Ready(Ok(crate::net::TcpStream { handle: s as u64 }));
            }

            if res != -115
            /* EINPROGRESS */
            {
                // close(s)
                crate::syscall!(crate::os::linux::syscall::nr::CLOSE, s as usize);
                return Poll::Ready(Err(io::Error::last_os_error()));
            }

            self.handle = Some(s);
            self.started = true;
        }

        let fd = self.handle.unwrap();
        // Check if finished via getsockopt(SO_ERROR) or just use wait_writable?
        // Connecting socket becomes writable on success.
        let mut wait = crate::rt::wait_writable(fd);
        match Pin::new(&mut wait).poll(cx) {
            Poll::Ready(Ok(())) => {
                // Double check if actually connected
                let mut err = 0i32;
                let mut len = core::mem::size_of::<i32>();
                let res = crate::syscall!(
                    crate::os::linux::syscall::nr::GETSOCKOPT,
                    fd as usize,
                    1, /* SOL_SOCKET */
                    4, /* SO_ERROR */
                    &mut err as *mut _ as usize,
                    &mut len as *mut _ as usize
                ) as isize;
                if res == 0 && err == 0 {
                    Poll::Ready(Ok(crate::net::TcpStream { handle: fd as u64 }))
                } else if err != 0 {
                    crate::syscall!(crate::os::linux::syscall::nr::CLOSE, fd as usize);
                    Poll::Ready(Err(io::Error::from_raw_os_error(err)))
                } else {
                    Poll::Pending
                }
            }
            Poll::Ready(Err(e)) => {
                crate::syscall!(crate::os::linux::syscall::nr::CLOSE, fd as usize);
                Poll::Ready(Err(e))
            }
            Poll::Pending => Poll::Pending,
        }
    }
}

/// Future for binding a TCP listener on Linux.
pub struct TcpListenerBind {
    addr: SocketAddr,
}

impl TcpListenerBind {
    /// Creates a new TCP listener bind future.
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
                    sin_family: 2,
                    sin_port: a.port().to_be(),
                    sin_addr: a.ip().octets(),
                    sin_zero: [0; 8],
                };
                (
                    2,
                    &sin as *const _ as *const u8,
                    core::mem::size_of::<sockaddr_in>(),
                )
            }
            SocketAddr::V6(ref a) => {
                let mut sin6_addr = [0u8; 16];
                let segments = a.ip().segments();
                for i in 0..8 {
                    sin6_addr[i * 2] = (segments[i] >> 8) as u8;
                    sin6_addr[i * 2 + 1] = (segments[i] & 0xFF) as u8;
                }
                let sin6 = sockaddr_in6 {
                    sin6_family: 10,
                    sin6_port: a.port().to_be(),
                    sin6_flowinfo: 0,
                    sin6_addr,
                    sin6_scope_id: 0,
                };
                (
                    10,
                    &sin6 as *const _ as *const u8,
                    core::mem::size_of::<sockaddr_in6>(),
                )
            }
        };

        let s = crate::syscall!(crate::os::linux::syscall::nr::SOCKET, af, 1, 6) as i32;
        if s < 0 {
            return Poll::Ready(Err(io::Error::last_os_error()));
        }

        // SO_REUSEADDR
        let on = 1i32;
        let _ = crate::syscall!(
            crate::os::linux::syscall::nr::SETSOCKOPT,
            s as usize,
            1, /* SOL_SOCKET */
            2, /* SO_REUSEADDR */
            &on as *const _ as usize,
            core::mem::size_of::<i32>()
        );

        let res = crate::syscall!(
            crate::os::linux::syscall::nr::BIND,
            s as usize,
            addr_buf as usize,
            addr_len
        ) as isize;
        if res < 0 {
            let err = io::Error::last_os_error();
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, s as usize);
            return Poll::Ready(Err(err));
        }

        let res = crate::syscall!(crate::os::linux::syscall::nr::LISTEN, s as usize, 128) as isize;
        if res < 0 {
            let err = io::Error::last_os_error();
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, s as usize);
            return Poll::Ready(Err(err));
        }

        // Set non-blocking
        let flags = crate::syscall!(
            crate::os::linux::syscall::nr::FCNTL,
            s as usize,
            3 /* F_GETFL */
        ) as i32;
        let _ = crate::syscall!(
            crate::os::linux::syscall::nr::FCNTL,
            s as usize,
            4, /* F_SETFL */
            (flags | 0o4000) as usize
        );

        Poll::Ready(Ok(crate::net::TcpListener { handle: s as u64 }))
    }
}

/// Future for accepting a connection on a TCP listener on Linux.
pub struct TcpAccept {
    fd: i32,
}

impl TcpAccept {
    /// Creates a new TCP accept future.
    pub fn new(fd: i32) -> Self {
        Self { fd }
    }
}

impl Future for TcpAccept {
    type Output = io::Result<(crate::net::TcpStream, crate::net::SocketAddr)>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let mut addr_buf = [0u8; 128];
        let mut addr_len = 128u32;

        let res = crate::syscall!(
            crate::os::linux::syscall::nr::ACCEPT,
            self.fd as usize,
            addr_buf.as_mut_ptr() as usize,
            &mut addr_len as *mut _ as usize
        ) as i32;
        if res >= 0 {
            // Success
            let addr = crate::net::SocketAddr::V4(crate::net::SocketAddrV4::new(
                crate::net::Ipv4Addr::new(0, 0, 0, 0),
                0,
            ));
            return Poll::Ready(Ok((crate::net::TcpStream { handle: res as u64 }, addr)));
        }

        let err = -(res as i32);
        if err != 11 /* EAGAIN */ && err != 115
        /* EINPROGRESS */
        {
            return Poll::Ready(Err(io::Error::from_raw_os_error(err)));
        }

        let mut wait = crate::rt::wait_readable(self.fd);
        match Pin::new(&mut wait).poll(cx) {
            Poll::Ready(Ok(())) => {
                // Re-poll on next tick or try again immediately?
                // For simplicity, re-poll.
                cx.waker().wake_by_ref();
                Poll::Pending
            }
            Poll::Ready(Err(e)) => Poll::Ready(Err(e)),
            Poll::Pending => Poll::Pending,
        }
    }
}
