#![cfg(windows)]
#![allow(
    clippy::pedantic,
    clippy::undocumented_unsafe_blocks,
    clippy::borrow_as_ptr,
    clippy::cast_possible_truncation
)]

use crate::boxed::Box;
use crate::collections::HashMap;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::ptr;
use crate::task::{Context, Poll, Waker};
use crate::vec::Vec;

use crate::rt::executor::with_driver;
use crate::rt::ready::{consume_ready, mark_ready};

#[repr(C)]
pub struct Overlapped {
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

    fn PostQueuedCompletionStatus(
        comp_port: usize,
        number_of_bytes: u32,
        comp_key: usize,
        overlapped: *mut Overlapped,
    ) -> i32;

    fn RegisterWaitForSingleObject(
        new_wait_object: *mut usize,
        object: usize,
        callback: Option<unsafe extern "system" fn(*mut core::ffi::c_void, u8)>,
        context: *mut core::ffi::c_void,
        milliseconds: u32,
        flags: u32,
    ) -> i32;

    fn UnregisterWaitEx(wait_handle: usize, completion_event: usize) -> i32;

    fn CloseHandle(object: usize) -> i32;

    fn ReadFile(
        file: usize,
        buffer: *mut u8,
        number_of_bytes_to_read: u32,
        number_of_bytes_read: *mut u32,
        overlapped: *mut Overlapped,
    ) -> i32;

    fn WriteFile(
        file: usize,
        buffer: *const u8,
        number_of_bytes_to_write: u32,
        number_of_bytes_written: *mut u32,
        overlapped: *mut Overlapped,
    ) -> i32;

    fn GetLastError() -> u32;
}

pub struct Driver {
    pub(crate) iocp: usize,
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
        let res = unsafe { CreateIoCompletionPort(handle as usize, self.iocp, handle as usize, 1) };
        if res == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    pub fn post_custom_status(&mut self, token: u64) {
        unsafe {
            PostQueuedCompletionStatus(self.iocp, 0, token as usize, ptr::null_mut());
        }
    }

    pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
        let mut had_events = false;
        loop {
            let mut bytes: u32 = 0;
            let mut key: usize = 0;
            let mut overlapped: *mut Overlapped = ptr::null_mut();

            let res = unsafe {
                GetQueuedCompletionStatus(
                    self.iocp,
                    &mut bytes,
                    &mut key,
                    &mut overlapped,
                    0, // zero timeout — non-blocking
                )
            };

            if res != 0 {
                let token = key as u64;
                mark_ready(token);
                if let Some(waker) = self.wakers.remove(&token) {
                    waker.wake();
                }
                had_events = true;
            } else {
                // GetLastError() == 258 (WAIT_TIMEOUT) means the queue is empty.
                let err = unsafe { GetLastError() };
                if err == 258 {
                    break;
                }
                return Err(io::Error::from_raw_os_error(err as i32));
            }
        }
        Ok(had_events)
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

unsafe extern "system" fn wait_callback(context: *mut core::ffi::c_void, _timer_fired: u8) {
    // context points to WaitProcess
    let wp = context as *const WaitProcess;
    let token = unsafe { (*wp).token };
    let iocp = unsafe { (*wp).iocp };
    unsafe {
        PostQueuedCompletionStatus(iocp, 0, token as usize, ptr::null_mut());
    }
}

const WT_EXECUTEONLYONCE: u32 = 0x0000_0008;
const INFINITE: u32 = 0xFFFFFFFF;
pub const ERROR_IO_PENDING: u32 = 997;

/// A specialized structure containing exactly what's needed for an overlapped operation, ensuring
/// stable addresses while in flight.
pub struct OverlappedOp {
    pub overlapped: Overlapped,
    pub buffer: Option<Vec<u8>>,
    pub token: u64,
}

impl OverlappedOp {
    #[allow(clippy::missing_const_for_fn)]
    pub fn new(token: u64, buffer: Vec<u8>) -> Self {
        Self {
            overlapped: Overlapped {
                internal: 0,
                internal_high: 0,
                offset: 0,
                offset_high: 0,
                h_event: 0,
            },
            buffer: Some(buffer),
            token,
        }
    }
}

pub struct OverlappedRead {
    handle: u64,
    op: Option<Pin<Box<OverlappedOp>>>,
    started: bool,
}

impl OverlappedRead {
    #[allow(clippy::missing_const_for_fn)]
    pub fn new(handle: u64, buf: Vec<u8>) -> Self {
        Self {
            handle,
            op: Some(Box::pin(OverlappedOp::new(handle, buf))),
            started: false,
        }
    }
}

impl Future for OverlappedRead {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            // First transition
            let handle = self.handle;
            if let Some(op) = self.op.as_mut() {
                if let Some(buf) = op.buffer.as_mut() {
                    let res = unsafe {
                        ReadFile(
                            handle as usize,
                            buf.as_mut_ptr(),
                            buf.capacity() as u32,
                            ptr::null_mut(),
                            &mut op.overlapped,
                        )
                    };

                    self.started = true;

                    if res == 0 {
                        let err = unsafe { GetLastError() };
                        if err != ERROR_IO_PENDING {
                            if let Some(mut op_owned) = self.op.take() {
                                if let Some(buf_owned) = op_owned.buffer.take() {
                                    return Poll::Ready((
                                        Err(io::Error::from_raw_os_error(err as i32)),
                                        buf_owned,
                                    ));
                                }
                            }
                        }
                    }
                }
            }
        }

        let token = self.handle;
        if consume_ready(token) {
            if let Some(mut op_owned) = self.op.take() {
                if let Some(mut buf_owned) = op_owned.buffer.take() {
                    let bytes_transferred = op_owned.overlapped.internal_high;
                    let ntstatus = op_owned.overlapped.internal as isize;

                    if ntstatus < 0 {
                        return Poll::Ready((
                            Err(io::Error::from_raw_os_error(ntstatus as i32)),
                            buf_owned,
                        ));
                    }

                    unsafe { buf_owned.set_len(bytes_transferred) };
                    return Poll::Ready((Ok(bytes_transferred), buf_owned));
                }
            }
            Poll::Ready((Err(io::Error::last_os_error()), Vec::new()))
        } else {
            let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));
            Poll::Pending
        }
    }
}

pub struct OverlappedWrite {
    handle: u64,
    op: Option<Pin<Box<OverlappedOp>>>,
    started: bool,
}

impl OverlappedWrite {
    #[allow(clippy::missing_const_for_fn)]
    pub fn new(handle: u64, buf: Vec<u8>) -> Self {
        Self {
            handle,
            op: Some(Box::pin(OverlappedOp::new(handle, buf))),
            started: false,
        }
    }
}

impl Future for OverlappedWrite {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            let handle = self.handle;
            if let Some(op) = self.op.as_mut() {
                if let Some(buf) = op.buffer.as_mut() {
                    let res = unsafe {
                        WriteFile(
                            handle as usize,
                            buf.as_ptr(),
                            buf.len() as u32,
                            ptr::null_mut(),
                            &mut op.overlapped,
                        )
                    };

                    self.started = true;

                    if res == 0 {
                        let err = unsafe { GetLastError() };
                        if err != ERROR_IO_PENDING {
                            if let Some(mut op_owned) = self.op.take() {
                                if let Some(buf_owned) = op_owned.buffer.take() {
                                    return Poll::Ready((
                                        Err(io::Error::from_raw_os_error(err as i32)),
                                        buf_owned,
                                    ));
                                }
                            }
                        }
                    }
                }
            }
        }

        let token = self.handle;
        if consume_ready(token) {
            if let Some(mut op_owned) = self.op.take() {
                if let Some(buf_owned) = op_owned.buffer.take() {
                    let bytes_transferred = op_owned.overlapped.internal_high;
                    let ntstatus = op_owned.overlapped.internal as isize;

                    if ntstatus < 0 {
                        return Poll::Ready((
                            Err(io::Error::from_raw_os_error(ntstatus as i32)),
                            buf_owned,
                        ));
                    }

                    return Poll::Ready((Ok(bytes_transferred), buf_owned));
                }
            }
            Poll::Ready((Err(io::Error::last_os_error()), Vec::new()))
        } else {
            let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));
            Poll::Pending
        }
    }
}

pub struct WaitProcess {
    h: u64,
    wait_handle: usize,
    token: u64,
    registered: bool,
    iocp: usize,
}

impl WaitProcess {
    #[allow(clippy::missing_const_for_fn)]
    pub fn new(h: u64) -> Self {
        let iocp = with_driver(|d| d.iocp).unwrap_or(0);
        Self {
            h,
            wait_handle: 0,
            token: h,
            registered: false,
            iocp,
        }
    }
}

impl Future for WaitProcess {
    type Output = io::Result<()>;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<io::Result<()>> {
        if consume_ready(self.token) {
            return Poll::Ready(Ok(()));
        }

        let _ = with_driver(|d| d.register_waker(self.token, cx.waker().clone()));

        if !self.registered {
            // SAFETY: Calling RegisterWaitForSingleObject
            let self_ptr = self.as_mut().get_mut() as *mut Self as *mut core::ffi::c_void;
            let res = unsafe {
                RegisterWaitForSingleObject(
                    &mut self.wait_handle,
                    self.h as usize,
                    Some(wait_callback),
                    self_ptr,
                    INFINITE,
                    WT_EXECUTEONLYONCE,
                )
            };

            if res == 0 {
                return Poll::Ready(Err(io::Error::last_os_error()));
            }
            self.registered = true;
        }

        Poll::Pending
    }
}

impl Drop for WaitProcess {
    fn drop(&mut self) {
        if self.registered {
            // SAFETY: Unregistering the wait handle. !0 blocks until callback completes.
            unsafe { UnregisterWaitEx(self.wait_handle, !0) };
        }
    }
}
