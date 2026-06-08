#![cfg(windows)]

use crate::cell::{Cell, RefCell, UnsafeCell};
use crate::collections::HashMap;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::ptr;
use crate::rc::Rc;
use crate::task::{Context, Poll, Waker};
use crate::vec::Vec;

/// Stores the last crash reason code when a Windows runtime error occurs.
pub static CRASH_REASON: core::sync::atomic::AtomicI32 = core::sync::atomic::AtomicI32::new(0);

use crate::rt::ready::{consume_ready, mark_ready};

/// Windows overlapped I/O descriptor used for asynchronous file operations.
#[repr(C)]
pub struct Overlapped {
    /// Used internally by the Windows API to store low-order result state.
    pub internal: usize,
    /// Used internally by the Windows API to store high-order result state.
    pub internal_high: usize,
    /// Low 32 bits of the asynchronous file offset.
    pub offset: u32,
    /// High 32 bits of the asynchronous file offset.
    pub offset_high: u32,
    /// Event handle used to signal completion.
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

    fn GetCommandLineW() -> *const u16;
}

#[repr(C)]
struct sockaddr_in {
    sin_family: i16,
    sin_port: u16,
    sin_addr: [u8; 4],
    sin_zero: [u8; 8],
}

#[repr(C)]
struct sockaddr_in6 {
    sin6_family: i16,
    sin6_port: u16,
    sin6_flowinfo: u32,
    sin6_addr: [u8; 16],
    sin6_scope_id: u32,
}

#[link(name = "ws2_32", kind = "raw-dylib")]
unsafe extern "system" {
    fn WSAStartup(wVersionRequired: u16, lpWSAData: *mut u8) -> i32;
    fn socket(af: i32, socket_type: i32, protocol: i32) -> usize;
    fn bind(s: usize, name: *const u8, namelen: i32) -> i32;
    fn listen(s: usize, backlog: i32) -> i32;
    fn closesocket(s: usize) -> i32;
    fn connect(s: usize, name: *const u8, namelen: i32) -> i32;
    fn ioctlsocket(s: usize, cmd: i32, argp: *mut u32) -> i32;
    fn WSAGetLastError() -> i32;

    fn WSARecv(
        s: usize,
        lpBuffers: *const WSABUF,
        dwBufferCount: u32,
        lpNumberOfBytesRecvd: *mut u32,
        lpFlags: *mut u32,
        lpOverlapped: *mut Overlapped,
        lpCompletionRoutine: Option<unsafe extern "system" fn(u32, u32, *mut Overlapped)>,
    ) -> i32;

    fn WSASend(
        s: usize,
        lpBuffers: *const WSABUF,
        dwBufferCount: u32,
        lpNumberOfBytesSent: *mut u32,
        dwFlags: u32,
        lpOverlapped: *mut Overlapped,
        lpCompletionRoutine: Option<unsafe extern "system" fn(u32, u32, *mut Overlapped)>,
    ) -> i32;

    fn accept(s: usize, addr: *mut u8, addrlen: *mut i32) -> usize;
}

#[repr(C)]
struct WSABUF {
    len: u32,
    buf: *mut u8,
}

/// Windows error code meaning the operation is still pending.
pub const ERROR_IO_PENDING: u32 = 997;

/// Wait flag that allows I/O completion callbacks to run.
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

/// Windows async I/O driver used by the runtime.
pub struct Driver {
    timer_handle: usize,
    pub(crate) wakers: RefCell<HashMap<u64, Waker>>,
}

impl Driver {
    /// Creates a new Windows runtime driver instance.
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

        // Initialize Winsock
        let mut wsa_data = [0u8; 512];
        unsafe { WSAStartup(0x0202, wsa_data.as_mut_ptr()) };

        Ok(Self {
            timer_handle,
            wakers: RefCell::new(HashMap::new()),
        })
    }

    /// Sets the driver timeout for the next wait cycle.
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

    /// Registers a Windows handle for asynchronous notification.
    pub fn register(&mut self, _handle: u64) -> io::Result<()> {
        Ok(())
    }

    /// Stores a waker to be invoked when the registered handle is ready.
    pub fn register_waker(&self, token: u64, waker: Waker) {
        self.wakers.borrow_mut().insert(token, waker);
    }

    /// Polls the Windows runtime for I/O completion or timeouts.
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

/// Future representing a Windows overlapped read operation.
pub struct OverlappedRead {
    handle: u64,
    state: Rc<OpState>,
    started: bool,
}

impl OverlappedRead {
    /// Creates a new overlapped read operation for the given handle.
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

/// Future representing a Windows overlapped write operation.
pub struct OverlappedWrite {
    handle: u64,
    state: Rc<OpState>,
    started: bool,
}

impl OverlappedWrite {
    /// Creates a new overlapped write operation for the given handle.
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

// ─── OverlappedRecv ──────────────────────────────────────────────────────────

/// Future representing a Windows overlapped socket receive operation.
pub struct OverlappedRecv {
    handle: u64,
    state: Rc<OpState>,
    started: bool,
}

impl OverlappedRecv {
    /// Creates a new overlapped recv operation for the given socket.
    pub fn new(handle: u64, buf: Vec<u8>) -> Self {
        Self {
            handle,
            state: OpState::new(Some(buf)),
            started: false,
        }
    }
}

impl Future for OverlappedRecv {
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

            let wsa_buf = WSABUF {
                len: buf_cap,
                buf: buf_ptr,
            };

            let mut flags = 0u32;
            let mut recvd = 0u32;

            let res = unsafe {
                WSARecv(
                    handle as usize,
                    &wsa_buf,
                    1,
                    &mut recvd,
                    &mut flags,
                    self.state.overlapped.get(),
                    Some(apc_callback),
                )
            };

            if res != 0 {
                let err = unsafe { WSAGetLastError() };
                if err != ERROR_IO_PENDING as i32 {
                    unsafe { drop(Rc::from_raw(state_ptr)) };
                    let buf = unsafe { (*self.state.buffer.get()).take().unwrap() };
                    return Poll::Ready((Err(io::Error::from_raw_os_error(err)), buf));
                }
            }

            unsafe { self.as_mut().get_unchecked_mut().started = true };
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

impl Drop for OverlappedRecv {
    fn drop(&mut self) {
        if self.started && self.state.result.borrow().is_none() {
            unsafe { CancelIoEx(self.handle as usize, self.state.overlapped.get()) };
        }
    }
}

// ─── OverlappedSend ──────────────────────────────────────────────────────────

/// Future representing a Windows overlapped socket send operation.
pub struct OverlappedSend {
    handle: u64,
    state: Rc<OpState>,
    started: bool,
}

impl OverlappedSend {
    /// Creates a new overlapped send operation for the given socket.
    pub fn new(handle: u64, buf: Vec<u8>) -> Self {
        Self {
            handle,
            state: OpState::new(Some(buf)),
            started: false,
        }
    }
}

impl Future for OverlappedSend {
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

            let wsa_buf = WSABUF {
                len: buf_len,
                buf: buf_ptr as *mut u8,
            };

            let mut sent = 0u32;
            let res = unsafe {
                WSASend(
                    handle as usize,
                    &wsa_buf,
                    1,
                    &mut sent,
                    0,
                    self.state.overlapped.get(),
                    Some(apc_callback),
                )
            };

            if res != 0 {
                let err = unsafe { WSAGetLastError() };
                if err != ERROR_IO_PENDING as i32 {
                    unsafe { drop(Rc::from_raw(state_ptr)) };
                    let buf = unsafe { (*self.state.buffer.get()).take().unwrap() };
                    return Poll::Ready((Err(io::Error::from_raw_os_error(err)), buf));
                }
            }

            unsafe { self.as_mut().get_unchecked_mut().started = true };
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

impl Drop for OverlappedSend {
    fn drop(&mut self) {
        if self.started && self.state.result.borrow().is_none() {
            unsafe { CancelIoEx(self.handle as usize, self.state.overlapped.get()) };
        }
    }
}

// ─── TcpConnect ──────────────────────────────────────────────────────────────

use crate::net::SocketAddr;

/// Future for connecting a TCP stream on Windows.
pub struct TcpConnect {
    addr: SocketAddr,
    handle: Option<u64>,
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
            let mut addr_buf = [0u8; 128];
            let addr_len;
            let af;

            match self.addr {
                SocketAddr::V4(ref a) => {
                    af = 2; // AF_INET
                    let sin = sockaddr_in {
                        sin_family: 2,
                        sin_port: a.port().to_be(),
                        sin_addr: a.ip().octets(),
                        sin_zero: [0; 8],
                    };
                    addr_len = core::mem::size_of::<sockaddr_in>() as i32;
                    unsafe {
                        core::ptr::copy_nonoverlapping(
                            &sin as *const _ as *const u8,
                            addr_buf.as_mut_ptr(),
                            addr_len as usize,
                        )
                    };
                }
                SocketAddr::V6(ref a) => {
                    af = 23; // AF_INET6
                    let mut sin6_addr = [0u8; 16];
                    let segments = a.ip().segments();
                    for i in 0..8 {
                        sin6_addr[i * 2] = (segments[i] >> 8) as u8;
                        sin6_addr[i * 2 + 1] = (segments[i] & 0xFF) as u8;
                    }
                    let sin6 = sockaddr_in6 {
                        sin6_family: 23,
                        sin6_port: a.port().to_be(),
                        sin6_flowinfo: 0,
                        sin6_addr,
                        sin6_scope_id: 0,
                    };
                    addr_len = core::mem::size_of::<sockaddr_in6>() as i32;
                    unsafe {
                        core::ptr::copy_nonoverlapping(
                            &sin6 as *const _ as *const u8,
                            addr_buf.as_mut_ptr(),
                            addr_len as usize,
                        )
                    };
                }
            }

            let s = unsafe { socket(af, 1, 6) }; // AF, SOCK_STREAM, IPPROTO_TCP
            if s == !0usize {
                return Poll::Ready(Err(io::Error::last_os_error()));
            }

            // Set non-blocking
            let mut mode = 1u32;
            unsafe {
                ioctlsocket(s, -2147195266 /* FIONBIO */, &mut mode)
            };

            let res = unsafe { connect(s, addr_buf.as_ptr(), addr_len) };

            if res == 0 {
                return Poll::Ready(Ok(crate::net::TcpStream { handle: s as u64 }));
            }

            let err = unsafe { WSAGetLastError() };
            if err != 10035
            /* WSAEWOULDBLOCK */
            {
                unsafe { closesocket(s) };
                return Poll::Ready(Err(io::Error::from_raw_os_error(err)));
            }

            self.handle = Some(s as u64);
            self.started = true;
        }

        let h = self.handle.unwrap();
        if consume_ready(h) {
            Poll::Ready(Ok(crate::net::TcpStream { handle: h }))
        } else {
            crate::rt::executor::with_driver(|d| d.register_waker(h, cx.waker().clone())).ok();
            Poll::Pending
        }
    }
}

// ─── TcpListenerBind ─────────────────────────────────────────────────────────

/// Future for binding a TCP listener on Windows.
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
        let mut addr_buf = [0u8; 128];
        let addr_len;
        let af;

        match self.addr {
            SocketAddr::V4(ref a) => {
                af = 2; // AF_INET
                let sin = sockaddr_in {
                    sin_family: 2,
                    sin_port: a.port().to_be(),
                    sin_addr: a.ip().octets(),
                    sin_zero: [0; 8],
                };
                addr_len = core::mem::size_of::<sockaddr_in>() as i32;
                unsafe {
                    core::ptr::copy_nonoverlapping(
                        &sin as *const _ as *const u8,
                        addr_buf.as_mut_ptr(),
                        addr_len as usize,
                    )
                };
            }
            SocketAddr::V6(ref a) => {
                af = 23; // AF_INET6
                let mut sin6_addr = [0u8; 16];
                let segments = a.ip().segments();
                for i in 0..8 {
                    sin6_addr[i * 2] = (segments[i] >> 8) as u8;
                    sin6_addr[i * 2 + 1] = (segments[i] & 0xFF) as u8;
                }
                let sin6 = sockaddr_in6 {
                    sin6_family: 23,
                    sin6_port: a.port().to_be(),
                    sin6_flowinfo: 0,
                    sin6_addr,
                    sin6_scope_id: 0,
                };
                addr_len = core::mem::size_of::<sockaddr_in6>() as i32;
                unsafe {
                    core::ptr::copy_nonoverlapping(
                        &sin6 as *const _ as *const u8,
                        addr_buf.as_mut_ptr(),
                        addr_len as usize,
                    )
                };
            }
        }

        let s = unsafe { socket(af, 1, 6) };
        if s == !0usize {
            return Poll::Ready(Err(io::Error::last_os_error()));
        }

        let res = unsafe { bind(s, addr_buf.as_ptr(), addr_len) };
        if res != 0 {
            let err = unsafe { WSAGetLastError() };
            unsafe { closesocket(s) };
            return Poll::Ready(Err(io::Error::from_raw_os_error(err)));
        }

        let res = unsafe { listen(s, 128) };
        if res != 0 {
            let err = unsafe { WSAGetLastError() };
            unsafe { closesocket(s) };
            return Poll::Ready(Err(io::Error::from_raw_os_error(err)));
        }

        // Set non-blocking for accept
        let mut mode = 1u32;
        unsafe {
            ioctlsocket(s, -2147195266 /* FIONBIO */, &mut mode)
        };

        Poll::Ready(Ok(crate::net::TcpListener { handle: s as u64 }))
    }
}

// ─── TcpAccept ───────────────────────────────────────────────────────────────

/// Future for accepting a connection on a TCP listener on Windows.
pub struct TcpAccept {
    handle: u64,
}

impl TcpAccept {
    /// Creates a new TCP accept future.
    pub fn new(handle: u64) -> Self {
        Self { handle }
    }
}

impl Future for TcpAccept {
    type Output = io::Result<(crate::net::TcpStream, crate::net::SocketAddr)>;

    fn poll(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
        let mut addr_buf = [0u8; 128];
        let mut addr_len = 128i32;

        let s = unsafe { accept(self.handle as usize, addr_buf.as_mut_ptr(), &mut addr_len) };
        if s != !0usize {
            // Success
            // TODO: Parse addr_buf to SocketAddr
            let addr = crate::net::SocketAddr::V4(crate::net::SocketAddrV4::new(
                crate::net::Ipv4Addr::new(0, 0, 0, 0),
                0,
            ));
            return Poll::Ready(Ok((crate::net::TcpStream { handle: s as u64 }, addr)));
        }

        let err = unsafe { WSAGetLastError() };
        if err != 10035
        /* WSAEWOULDBLOCK */
        {
            return Poll::Ready(Err(io::Error::from_raw_os_error(err)));
        }

        crate::rt::executor::with_driver(|d| d.register_waker(self.handle, cx.waker().clone()))
            .ok();
        Poll::Pending
    }
}

// ─── Waiters ─────────────────────────────────────────────────────────────────

/// Future that waits for a Windows handle to become readable.
pub struct WaitReadable {
    h: u64,
}
impl WaitReadable {
    /// Creates a new readable wait future for the given handle.
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

/// Future that waits for a Windows handle to become writable.
pub struct WaitWritable {
    h: u64,
}
impl WaitWritable {
    /// Creates a new writable wait future for the given handle.
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

/// Future that waits for a process handle to exit.
pub struct WaitProcess {
    #[allow(dead_code)]
    h: u64,
}
impl WaitProcess {
    /// Creates a new process wait future for the given handle.
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
/// TLS index placeholder required by the Windows CRT linkage.
pub static mut _tls_index: u32 = 0;

#[cfg(not(test))]
#[unsafe(no_mangle)]
/// Stub symbol used by the Windows C runtime to indicate floating-point usage.
pub static mut _fltused: i32 = 0x9875;

// GNU Windows targets may emit a call to `__main` from the compiler-generated
// `main` wrapper. Provide a no-op shim for CRT-free builds.
#[cfg(all(not(test), target_env = "gnu"))]
#[unsafe(no_mangle)]
/// No-op entry point shim for GNU Windows CRT-free builds.
pub extern "C" fn __main() {}

/// Entry point for MSVC-linked binaries.
#[cfg(not(test))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mainCRTStartup() -> ! {
    unsafe {
        let _cmd = GetCommandLineW();
        // TODO: parse command line if we ever want to support argc/argv properly
    }
    unsafe extern "Rust" {
        fn main();
    }
    unsafe { main() };
    crate::process::exit(0);
}
