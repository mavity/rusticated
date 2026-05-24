#![cfg(windows)]
#![allow(
    clippy::pedantic,
    clippy::undocumented_unsafe_blocks,
    clippy::borrow_as_ptr,
    clippy::cast_possible_truncation
)]

use crate::cell::{Cell, RefCell, UnsafeCell};
use crate::collections::HashMap;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::ptr;
use crate::rc::Rc;
use crate::task::{Context, Poll, Waker};
use crate::vec::Vec;

pub static CRASH_REASON: core::sync::atomic::AtomicI32 = core::sync::atomic::AtomicI32::new(0);

use crate::rt::ready::{consume_ready, mark_ready};

#[repr(C)]
pub struct Overlapped {
    pub internal: usize,
    pub internal_high: usize,
    pub offset: u32,
    pub offset_high: u32,
    pub h_event: usize,
}

impl Default for Overlapped {
    fn default() -> Self {
        Self {
            internal: 0,
            internal_high: 0,
            offset: 0,
            offset_high: 0,
            h_event: 0,
        }
    }
}

#[link(name = "kernel32", kind = "raw-dylib")]
unsafe extern "system" {
    fn SleepEx(dwMilliseconds: u32, bAlertable: i32) -> u32;

    fn ReadFileEx(
        hFile: usize,
        lpBuffer: *mut u8,
        nNumberOfBytesToRead: u32,
        lpOverlapped: *mut Overlapped,
        lpCompletionRoutine: Option<unsafe extern "system" fn(u32, u32, *mut Overlapped)>,
    ) -> i32;

    fn WriteFileEx(
        hFile: usize,
        lpBuffer: *const u8,
        nNumberOfBytesToWrite: u32,
        lpOverlapped: *mut Overlapped,
        lpCompletionRoutine: Option<unsafe extern "system" fn(u32, u32, *mut Overlapped)>,
    ) -> i32;

    fn GetLastError() -> u32;

    fn CreateWaitableTimerW(
        timer_attributes: *mut core::ffi::c_void,
        bManualReset: i32,
        timer_name: *const u16,
    ) -> usize;

    fn SetWaitableTimer(
        timer: usize,
        due_time: *const i64,
        period: i32,
        completion_routine: Option<unsafe extern "system" fn(*mut core::ffi::c_void, u32, u32)>,
        arg_to_completion_routine: *mut core::ffi::c_void,
        resume: i32,
    ) -> i32;

    fn CancelIoEx(hFile: usize, lpOverlapped: *mut Overlapped) -> i32;

    fn CloseHandle(object: usize) -> i32;

    fn QueueUserAPC(
        pfnAPC: Option<unsafe extern "system" fn(usize)>,
        hThread: usize,
        dwData: usize,
    ) -> u32;

    fn GetCurrentThread() -> usize;
    fn GetCurrentProcess() -> usize;
    fn DuplicateHandle(
        hSourceProcessHandle: usize,
        hSourceHandle: usize,
        hTargetProcessHandle: usize,
        lpTargetHandle: *mut usize,
        dwDesiredAccess: u32,
        bInheritHandle: i32,
        dwOptions: u32,
    ) -> i32;
}

pub const ERROR_IO_PENDING: u32 = 997;
pub const WAIT_IO_COMPLETION: u32 = 0x000000C0;

#[repr(align(8))]
struct MainThreadHandleStorage(UnsafeCell<Cell<usize>>);

unsafe impl Sync for MainThreadHandleStorage {}

static MAIN_THREAD_HANDLE_STORAGE: MainThreadHandleStorage =
    MainThreadHandleStorage(UnsafeCell::new(Cell::new(0)));

fn main_thread_handle() -> &'static mut Cell<usize> {
    unsafe { &mut *MAIN_THREAD_HANDLE_STORAGE.0.get() }
}

/// Opportunistically flush the APC queue.
pub fn flush_completions() {
    unsafe { SleepEx(0, 1) };
}

// ─── OpState ─────────────────────────────────────────────────────────────────

/// Shared, pinned state for one in-flight overlapped operation.
#[repr(C)]
struct OpState {
    /// MUST be first for pointer casting from *mut Overlapped
    overlapped: UnsafeCell<Overlapped>,
    buffer: UnsafeCell<Option<Vec<u8>>>,
    waker: RefCell<Option<Waker>>,
    result: RefCell<Option<(u32, u32)>>, // (error_code, bytes_transferred)
}

impl OpState {
    fn new(buf: Option<Vec<u8>>) -> Rc<Self> {
        Rc::new(Self {
            overlapped: UnsafeCell::new(Overlapped::default()),
            buffer: UnsafeCell::new(buf),
            waker: RefCell::new(None),
            result: RefCell::new(None),
        })
    }
}

unsafe extern "system" fn apc_callback(error_code: u32, bytes: u32, overlapped: *mut Overlapped) {
    let state_ptr = overlapped as *mut OpState;
    // Reclaim the Rc clone submitted during I/O start.
    let state = unsafe { Rc::from_raw(state_ptr) };

    *state.result.borrow_mut() = Some((error_code, bytes));

    if let Some(waker) = state.waker.borrow_mut().take() {
        waker.wake();
    }
}

// ─── Driver ──────────────────────────────────────────────────────────────────

pub struct Driver {
    timer_handle: usize,
    pub(crate) wakers: RefCell<HashMap<u64, Waker>>,
}

impl Driver {
    pub fn new() -> io::Result<Self> {
        let timer_handle = unsafe { CreateWaitableTimerW(ptr::null_mut(), 0, ptr::null()) };
        if timer_handle == 0 {
            return Err(io::Error::last_os_error());
        }

        // Initialize main thread handle for QueueUserAPC
        if main_thread_handle().get() == 0 {
            unsafe {
                let mut h = 0;
                DuplicateHandle(
                    GetCurrentProcess(),
                    GetCurrentThread(),
                    GetCurrentProcess(),
                    &mut h,
                    0,
                    0,
                    2, // DUPLICATE_SAME_ACCESS
                );
                main_thread_handle().set(h);
            }
        }

        Ok(Self {
            timer_handle,
            wakers: RefCell::new(HashMap::new()),
        })
    }

    pub fn set_timeout(&mut self, ms: Option<u32>) -> io::Result<()> {
        let due_time: i64 = match ms {
            Some(0) => return Ok(()),           // SleepEx handles 0 timeout natively
            Some(ms) => -((ms as i64) * 10000), // relative time in 100ns units (negative)
            None => return Ok(()),
        };

        let res = unsafe {
            SetWaitableTimer(
                self.timer_handle,
                &due_time,
                0,
                Some(timer_apc_callback),
                ptr::null_mut(),
                0,
            )
        };

        if res == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    pub fn register(&mut self, _handle: u64) -> io::Result<()> {
        Ok(())
    }

    pub fn register_waker(&self, token: u64, waker: Waker) {
        self.wakers.borrow_mut().insert(token, waker);
    }

    pub fn poll(&mut self, blocking: bool, explicit_ms: Option<u32>) -> io::Result<bool> {
        let timeout = if let Some(ms) = explicit_ms {
            ms
        } else if blocking {
            0xFFFFFFFF // INFINITE
        } else {
            0
        };
        let res = unsafe { SleepEx(timeout, 1) };
        Ok(res == WAIT_IO_COMPLETION)
    }
}

unsafe extern "system" fn timer_apc_callback(_arg: *mut core::ffi::c_void, _low: u32, _high: u32) {
    // Wake-up call
}

unsafe extern "system" fn wake_apc_callback(data: usize) {
    let token = data as u64;
    mark_ready(token);
    let waker = crate::rt::executor::with_driver(|d| d.wakers.borrow_mut().remove(&token))
        .ok()
        .flatten();
    if let Some(w) = waker {
        w.wake();
    }
}

/// Queue an APC to the main thread to wake up a specific token.
/// Useful for bridging thread-pool callbacks (like TTY) to the main loop.
pub fn queue_wake(token: u64) {
    let h = main_thread_handle().get();
    if h != 0 {
        unsafe {
            QueueUserAPC(Some(wake_apc_callback), h, token as usize);
        }
    }
}

impl Drop for Driver {
    fn drop(&mut self) {
        unsafe { CloseHandle(self.timer_handle) };
    }
}

// ─── OverlappedRead ──────────────────────────────────────────────────────────

pub struct OverlappedRead {
    handle: u64,
    state: Rc<OpState>,
    started: bool,
}

impl OverlappedRead {
    pub fn new(handle: u64, buf: Vec<u8>) -> Self {
        Self {
            handle,
            state: OpState::new(Some(buf)),
            started: false,
        }
    }
}

impl Future for OverlappedRead {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            let handle = self.handle;
            let state_ptr = Rc::into_raw(Rc::clone(&self.state));

            let (buf_ptr, buf_cap) = unsafe {
                let buf = &mut *self.state.buffer.get();
                let b = buf.as_mut().unwrap();
                (b.as_mut_ptr(), b.capacity() as u32)
            };

            let res = unsafe {
                ReadFileEx(
                    handle as usize,
                    buf_ptr,
                    buf_cap,
                    self.state.overlapped.get(),
                    Some(apc_callback),
                )
            };

            if res == 0 {
                let err = unsafe { GetLastError() };
                unsafe { drop(Rc::from_raw(state_ptr)) };
                let buf = unsafe { (*self.state.buffer.get()).take().unwrap() };
                return Poll::Ready((Err(io::Error::from_raw_os_error(err as i32)), buf));
            }

            unsafe { self.as_mut().get_unchecked_mut().started = true };

            // Injection Hook B: Flush instantly
            flush_completions();
        }

        if let Some((err, bytes)) = *self.state.result.borrow() {
            let mut buf = unsafe { (*self.state.buffer.get()).take().unwrap() };
            if err != 0 {
                return Poll::Ready((Err(io::Error::from_raw_os_error(err as i32)), buf));
            }
            unsafe { buf.set_len(bytes as usize) };
            Poll::Ready((Ok(bytes as usize), buf))
        } else {
            *self.state.waker.borrow_mut() = Some(cx.waker().clone());
            Poll::Pending
        }
    }
}

impl Drop for OverlappedRead {
    fn drop(&mut self) {
        if self.started && self.state.result.borrow().is_none() {
            unsafe { CancelIoEx(self.handle as usize, self.state.overlapped.get()) };
        }
    }
}

// ─── OverlappedWrite ─────────────────────────────────────────────────────────

pub struct OverlappedWrite {
    handle: u64,
    state: Rc<OpState>,
    started: bool,
}

impl OverlappedWrite {
    pub fn new(handle: u64, buf: Vec<u8>) -> Self {
        Self {
            handle,
            state: OpState::new(Some(buf)),
            started: false,
        }
    }
}

impl Future for OverlappedWrite {
    type Output = (io::Result<usize>, Vec<u8>);

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if !self.started {
            let handle = self.handle;
            let state_ptr = Rc::into_raw(Rc::clone(&self.state));

            let (buf_ptr, buf_len) = unsafe {
                let buf = &*self.state.buffer.get();
                let b = buf.as_ref().unwrap();
                (b.as_ptr(), b.len() as u32)
            };

            let res = unsafe {
                WriteFileEx(
                    handle as usize,
                    buf_ptr,
                    buf_len,
                    self.state.overlapped.get(),
                    Some(apc_callback),
                )
            };

            if res == 0 {
                let err = unsafe { GetLastError() };
                unsafe { drop(Rc::from_raw(state_ptr)) };
                let buf = unsafe { (*self.state.buffer.get()).take().unwrap() };
                return Poll::Ready((Err(io::Error::from_raw_os_error(err as i32)), buf));
            }

            self.as_mut().get_mut().started = true;

            // Injection Hook B: Flush instantly
            flush_completions();
        }

        if let Some((err, bytes)) = *self.state.result.borrow() {
            let buf = unsafe { (*self.state.buffer.get()).take().unwrap() };
            if err != 0 {
                return Poll::Ready((Err(io::Error::from_raw_os_error(err as i32)), buf));
            }
            Poll::Ready((Ok(bytes as usize), buf))
        } else {
            *self.state.waker.borrow_mut() = Some(cx.waker().clone());
            Poll::Pending
        }
    }
}

impl Drop for OverlappedWrite {
    fn drop(&mut self) {
        if self.started && self.state.result.borrow().is_none() {
            unsafe { CancelIoEx(self.handle as usize, self.state.overlapped.get()) };
        }
    }
}

// ─── Waiters ─────────────────────────────────────────────────────────────────

pub struct WaitReadable {
    h: u64,
}
impl WaitReadable {
    pub fn new(h: u64) -> Self {
        Self { h }
    }
}
impl Future for WaitReadable {
    type Output = io::Result<()>;
    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if consume_ready(self.h) {
            Poll::Ready(Ok(()))
        } else {
            crate::rt::executor::with_driver(|d| d.register_waker(self.h, cx.waker().clone())).ok();
            Poll::Pending
        }
    }
}

pub struct WaitWritable {
    h: u64,
}
impl WaitWritable {
    pub fn new(h: u64) -> Self {
        Self { h }
    }
}
impl Future for WaitWritable {
    type Output = io::Result<()>;
    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        if consume_ready(self.h) {
            Poll::Ready(Ok(()))
        } else {
            crate::rt::executor::with_driver(|d| d.register_waker(self.h, cx.waker().clone())).ok();
            Poll::Pending
        }
    }
}

pub struct WaitProcess {
    h: u64,
}
impl WaitProcess {
    pub fn new(h: u64) -> Self {
        Self { h }
    }
}
impl Future for WaitProcess {
    type Output = io::Result<()>;
    fn poll(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Self::Output> {
        Poll::Ready(Ok(()))
    }
}

#[cfg(not(test))]
#[unsafe(no_mangle)]
static mut _tls_index: u32 = 0;

/// Stack probe for AArch64 Windows.
#[cfg(all(target_arch = "aarch64", not(test)))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn __chkstk() {}

/// Entry point for MSVC-linked binaries.
#[cfg(not(test))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mainCRTStartup() -> ! {
    unsafe extern "Rust" {
        fn main();
    }
    unsafe { main() };
    crate::process::exit(0);
}
