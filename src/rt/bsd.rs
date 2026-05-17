#![cfg(any(
    target_os = "macos",
    target_os = "freebsd",
    target_os = "openbsd",
    target_os = "netbsd"
))]

use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll};

/// macOS / BSD kqueue driver.
pub struct Driver {
    // kqueue backend pending
}

impl Driver {
    /// Create a new driver instance.
    pub fn new() -> io::Result<Self> {
        Ok(Self {})
    }

    /// Poll for already-ready events without blocking.
    ///
    /// Returns `true` if at least one event was processed.
    pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
        Ok(false)
    }
}

/// Future that resolves when fd becomes readable.
pub struct WaitReadable {
    _fd: i32,
}

impl WaitReadable {
    pub fn new(fd: i32) -> Self {
        Self { _fd: fd }
    }
}

impl Future for WaitReadable {
    type Output = io::Result<()>;

    fn poll(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Err(io::Error::other(
            "wait_readable: kqueue backend pending",
        )))
    }
}

/// Future that resolves when fd becomes writable.
pub struct WaitWritable {
    _fd: i32,
}

impl WaitWritable {
    pub fn new(fd: i32) -> Self {
        Self { _fd: fd }
    }
}

impl Future for WaitWritable {
    type Output = io::Result<()>;

    fn poll(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        Poll::Ready(Err(io::Error::other(
            "wait_writable: kqueue backend pending",
        )))
    }
}
