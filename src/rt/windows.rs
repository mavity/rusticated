#![cfg(windows)]
#![allow(
    clippy::pedantic,
    clippy::undocumented_unsafe_blocks,
    clippy::borrow_as_ptr,
    clippy::cast_possible_truncation
)]

use std::collections::HashMap;
use std::future::Future;
use std::io;
use std::pin::Pin;
use std::ptr;
use std::task::{Context, Poll, Waker};

use crate::rt::executor::with_driver;
use crate::rt::ready::{consume_ready, mark_ready};

#[repr(C)]
struct Overlapped {
    internal: usize,
    internal_high: usize,
    offset: u32,
    offset_high: u32,
    h_event: usize,
}

unsafe extern "system" {
    fn CreateIoCompletionPort(
        filehandle: usize,
        existing_cmp_port: usize,
        comp_key: usize,
        num_concurrent_threads: u32,
    ) -> usize;

    fn GetQueuedCompletionStatus(
        comp_port: usize,
        number_of_bytes: *mut u32,
        comp_key: *mut usize,
        overlapped: *mut *mut Overlapped,
        milliseconds: u32,
    ) -> i32;

    fn CloseHandle(object: usize) -> i32;
}

pub struct Driver {
    iocp: usize,
    wakers: HashMap<u64, Waker>,
}

impl Driver {
    pub fn new() -> io::Result<Self> {
        let iocp = unsafe { CreateIoCompletionPort(!0, 0, 0, 1) };
        if iocp == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self {
            iocp,
            wakers: HashMap::new(),
        })
    }

    pub fn register(&mut self, handle: u64) -> io::Result<()> {
        let res = unsafe {
            CreateIoCompletionPort(handle as usize, self.iocp, handle as usize, 1)
        };
        if res == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
        let mut bytes = 0;
        let mut key = 0;
        let mut overlapped: *mut Overlapped = ptr::null_mut();

        let res = unsafe {
            GetQueuedCompletionStatus(
                self.iocp,
                &mut bytes,
                &mut key,
                &mut overlapped,
                0, // No block
            )
        };

        if res != 0 {
            let token = key as u64;
            mark_ready(token);
            if let Some(waker) = self.wakers.remove(&token) {
                waker.wake();
            }
            Ok(true)
        } else {
            let err = io::Error::last_os_error();
            if err.raw_os_error() == Some(258) {
                // WAIT_TIMEOUT
                Ok(false)
            } else {
                Err(err)
            }
        }
    }

    pub(crate) fn register_waker(&mut self, token: u64, waker: Waker) {
        self.wakers.insert(token, waker);
    }
}

impl Drop for Driver {
    fn drop(&mut self) {
        unsafe { CloseHandle(self.iocp) };
    }
}

pub struct WaitReadable {
    h: u64,
}

impl WaitReadable {
    pub fn new(h: u64) -> Self {
        let _ = with_driver(|d| {
            let _ = d.register(h);
        });
        Self { h }
    }
}

impl Future for WaitReadable {
    type Output = io::Result<()>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if consume_ready(self.h) {
            Poll::Ready(Ok(()))
        } else {
            let _ = with_driver(|d| d.register_waker(self.h, cx.waker().clone()));
            Poll::Pending
        }
    }
}

pub struct WaitWritable {
    h: u64,
}

impl WaitWritable {
    pub fn new(h: u64) -> Self {
        let _ = with_driver(|d| {
            let _ = d.register(h);
        });
        Self { h }
    }
}

impl Future for WaitWritable {
    type Output = io::Result<()>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if consume_ready(self.h) {
            Poll::Ready(Ok(()))
        } else {
            let _ = with_driver(|d| d.register_waker(self.h, cx.waker().clone()));
            Poll::Pending
        }
    }
}
