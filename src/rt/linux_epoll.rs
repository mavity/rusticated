#![cfg(target_os = "linux")]
use super::linux_state::OpState;
use alloc::rc::Rc;
use core::sync::atomic::{AtomicI32, Ordering};

use crate::collections::HashMap;
use crate::future::Future;
use crate::io;
use crate::pin::Pin;
use crate::task::{Context, Poll};

use super::executor::with_driver;
use super::ready::{consume_ready, mark_ready};

struct Completion {
    token: u64,
    buf: alloc::vec::Vec<u8>,
    error_code: i32,
    bytes: u32,
}

static COMPLETION_QUEUE: crate::sync::SpinMutex<alloc::vec::Vec<Completion>> =
    crate::sync::SpinMutex::new(alloc::vec::Vec::new());

// -- epoll constants
// -------------------------------------------------------

const EPOLLIN: u32 = 0x0001;
const EPOLLOUT: u32 = 0x0004;
const EPOLLONESHOT: u32 = 1 << 30;
const EPOLL_CTL_ADD: i32 = 1;
const EPOLL_CTL_MOD: i32 = 3;
/// POSIX errno 17: file exists (returned by EPOLL_CTL_ADD on a known fd).
const EEXIST: i32 = 17;

/// `eventfd` flags.
const EFD_NONBLOCK: i32 = 0x800;
const EFD_CLOEXEC: i32 = 0x80000;

/// Sentinel token used to identify eventfd wake-up events in the epoll loop.
const WAKE_TOKEN: u64 = u64::MAX;

/// Global eventfd used by worker threads to interrupt `epoll_wait`.
pub(crate) static GLOBAL_WAKE_FD: AtomicI32 = AtomicI32::new(-1);

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

// Low-level syscalls are now performed via the `syscall!` macro.

/// Write to the global `eventfd` to interrupt a sleeping `epoll_wait`.
pub(crate) fn queue_wake() {
    let fd = GLOBAL_WAKE_FD.load(Ordering::Relaxed);
    if fd >= 0 {
        let val: u64 = 1;
        // SAFETY: `fd` is a valid eventfd; writing 8 bytes is the required protocol.
        crate::syscall!(
            crate::os::linux::syscall::nr::WRITE,
            fd as usize,
            &val as *const u64 as usize,
            8
        );
    }
}

// - Driver
// ----------------------------------------------------------------

/// Linux epoll driver.
pub struct EpollDriver {
    epfd: i32,
    /// fds currently registered (even if ONESHOT-disabled) so we can
    /// choose `EPOLL_CTL_MOD` vs `EPOLL_CTL_ADD` correctly.
    registered_fds: HashMap<i32, ()>,
    /// Wakers indexed by completion token; fired when epoll reports readiness.
    wakers: HashMap<u64, crate::task::Waker>,
    next_token: u64,
    pending_ops: HashMap<u64, PendingOp>,
}

enum PendingOp {
    Read(i32, alloc::rc::Rc<super::linux_state::OpState>),
    Write(i32, alloc::rc::Rc<super::linux_state::OpState>),
}

impl EpollDriver {
    /// Create a new epoll instance.
    pub fn new() -> io::Result<Self> {
        // SAFETY: FFI call with no precondition.
        let epfd = crate::syscall!(crate::os::linux::syscall::nr::EPOLL_CREATE1, 0usize) as i32;
        if epfd < 0 {
            return Err(io::Error::last_os_error());
        }

        // Create the eventfd that worker threads use to wake epoll_wait.
        // SAFETY: FFI call with valid constant flags.
        let evfd = crate::syscall!(
            crate::os::linux::syscall::nr::EVENTFD2,
            0usize,
            (EFD_NONBLOCK | EFD_CLOEXEC) as usize
        ) as i32;
        if evfd < 0 {
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, epfd as usize);
            return Err(io::Error::last_os_error());
        }
        GLOBAL_WAKE_FD.store(evfd, Ordering::SeqCst);

        // Register the eventfd with epoll using WAKE_TOKEN (no EPOLLONESHOT —
        // we want it to stay armed so every write wakes us up).
        let mut ev = EpollEvent {
            events: EPOLLIN,
            data: WAKE_TOKEN,
        };
        // SAFETY: `epfd` and `evfd` are valid; `ev` outlives the call.
        if (crate::syscall!(
            crate::os::linux::syscall::nr::EPOLL_CTL,
            epfd as usize,
            EPOLL_CTL_ADD as usize,
            evfd as usize,
            &mut ev as *mut _ as usize
        ) as i32)
            < 0
        {
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, evfd as usize);
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, epfd as usize);
            return Err(io::Error::last_os_error());
        }

        Ok(Self {
            epfd,
            registered_fds: HashMap::new(),
            wakers: HashMap::new(),
            next_token: 1,
            pending_ops: HashMap::new(),
        })
    }

    /// Returns the current number of pending operations.
    pub fn outstanding_io(&self) -> usize {
        self.pending_ops.len()
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
        let r = crate::syscall!(
            crate::os::linux::syscall::nr::EPOLL_CTL,
            self.epfd as usize,
            op as usize,
            fd as usize,
            &mut ev as *mut _ as usize
        ) as i32;
        if r < 0 {
            let e = io::Error::last_os_error();
            if r == -EEXIST {
                // Race: try MOD instead.
                // SAFETY: same as above.
                let r2 = crate::syscall!(
                    crate::os::linux::syscall::nr::EPOLL_CTL,
                    self.epfd as usize,
                    EPOLL_CTL_MOD as usize,
                    fd as usize,
                    &mut ev as *mut _ as usize
                ) as i32;
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
    /// Registers the given file descriptor for read readiness notifications.
    pub fn register_read(&mut self, fd: i32) -> io::Result<u64> {
        let token = self.next_token;
        self.next_token += 1;
        self.do_register(fd, EPOLLIN, token)?;
        Ok(token)
    }

    /// Register `fd` for writability; returns the unique token.
    /// Registers the given file descriptor for write readiness notifications.
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

    /// Polls the epoll instance for events, returning `true` if any were delivered.
    pub fn poll_with_timeout(&mut self, timeout_ms: Option<u32>) -> io::Result<bool> {
        let mut evbuf = [EpollEvent { events: 0, data: 0 }; 64];
        let timeout = timeout_ms.map(|t| t as i32).unwrap_or(-1);
        let n = loop {
            #[cfg(target_arch = "x86_64")]
            let n = crate::syscall!(
                crate::os::linux::syscall::nr::EPOLL_WAIT,
                self.epfd as usize,
                evbuf.as_mut_ptr() as usize,
                evbuf.len() as usize,
                timeout as usize
            ) as i32;

            #[cfg(target_arch = "aarch64")]
            let n = crate::syscall!(
                crate::os::linux::syscall::nr::EPOLL_PWAIT,
                self.epfd as usize,
                evbuf.as_mut_ptr() as usize,
                evbuf.len() as usize,
                timeout as usize,
                0usize, // sigmask = NULL
                8usize  // sigsetsize
            ) as i32;

            if n >= 0 {
                break n;
            }
            let err = (-n) as i32;
            unsafe {
                crate::io::ERRNO = err;
            }
            let e = io::Error::last_os_error();
            if e.kind() == crate::io::ErrorKind::Interrupted {
                crate::tty::check_sigwinch();
                break 0;
            }
            return Err(e);
        };
        // Process readiness events.
        for ev in &evbuf[..n as usize] {
            let token = unsafe { core::ptr::read_unaligned(core::ptr::addr_of!(ev.data)) };
            if token == WAKE_TOKEN {
                // A worker thread fired the eventfd to interrupt epoll_wait.
                // Drain the 8-byte counter so the fd is no longer readable.
                let evfd = GLOBAL_WAKE_FD.load(Ordering::Relaxed);
                let mut buf = [0u8; 8];
                // SAFETY: `evfd` is a valid eventfd; buf has exactly 8 bytes.
                crate::syscall!(
                    crate::os::linux::syscall::nr::READ,
                    evfd as usize,
                    buf.as_mut_ptr() as usize,
                    8
                );

                let completions = {
                    let mut q = COMPLETION_QUEUE.lock();
                    core::mem::take(&mut *q)
                };

                for comp in completions {
                    if let Some(op) = self.pending_ops.remove(&comp.token) {
                        let state = match op {
                            PendingOp::Read(_, state) => state,
                            PendingOp::Write(_, state) => state,
                        };
                        *state.result.borrow_mut() = Some((comp.error_code, comp.bytes));
                        unsafe { &mut *state.buffer.get() }.replace(comp.buf);
                        if let Some(w) = state.waker.borrow().as_ref() {
                            w.wake_by_ref();
                        }
                    }
                }

                continue;
            }
            if let Some(op) = self.pending_ops.remove(&token) {
                match op {
                    PendingOp::Read(fd, state) => {
                        let buf_opt = unsafe { &mut *state.buffer.get() }.take();
                        if let Some(mut buf) = buf_opt {
                            let cap = buf.capacity();
                            let res = crate::syscall!(
                                crate::os::linux::syscall::nr::READ,
                                fd as usize,
                                buf.as_mut_ptr() as usize,
                                cap
                            ) as isize;
                            if res < 0 {
                                *state.result.borrow_mut() = Some(((-res) as i32, 0));
                            } else {
                                unsafe {
                                    buf.set_len(res as usize);
                                }
                                *state.result.borrow_mut() = Some((0, res as u32));
                            }
                            unsafe { &mut *state.buffer.get() }.replace(buf);
                        }
                        if let Some(w) = state.waker.borrow().as_ref() {
                            w.wake_by_ref();
                        }
                    }
                    PendingOp::Write(fd, state) => {
                        let buf_opt = unsafe { &mut *state.buffer.get() }.take();
                        if let Some(buf) = buf_opt {
                            let len = buf.len();
                            let res = crate::syscall!(
                                crate::os::linux::syscall::nr::WRITE,
                                fd as usize,
                                buf.as_ptr() as usize,
                                len
                            ) as isize;
                            if res < 0 {
                                *state.result.borrow_mut() = Some(((-res) as i32, 0));
                            } else {
                                *state.result.borrow_mut() = Some((0, res as u32));
                            }
                            unsafe { &mut *state.buffer.get() }.replace(buf);
                        }
                        if let Some(w) = state.waker.borrow().as_ref() {
                            w.wake_by_ref();
                        }
                    }
                }
            } else {
                mark_ready(token);
                if let Some(waker) = self.wakers.remove(&token) {
                    waker.wake();
                }
            }
        }

        crate::rt::ready::consume_ready(0); // Dummy consume

        Ok(n > 0)
    }

    /// Poll for already-ready events without blocking.
    ///
    /// Returns `true` if at least one event was processed.
    pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
        self.poll_with_timeout(Some(0))
    }
}

impl Drop for EpollDriver {
    fn drop(&mut self) {
        // SAFETY: `close` on a valid fd is sound.
        crate::syscall!(crate::os::linux::syscall::nr::CLOSE, self.epfd as usize);
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

impl EpollDriver {
    pub(crate) fn submit_read(&mut self, fd: i32, state: Rc<OpState>) -> crate::io::Result<()> {
        let buf_opt = unsafe { &mut *state.buffer.get() }.take();
        if let Some(mut buf) = buf_opt {
            let token = self.next_token;
            self.next_token += 1;
            self.pending_ops.insert(token, PendingOp::Read(fd, state));

            let cap = buf.capacity();
            crate::rt::blocking::pool().spawn(move || {
                let res = crate::syscall!(
                    crate::os::linux::syscall::nr::READ,
                    fd as usize,
                    buf.as_mut_ptr() as usize,
                    cap as usize
                ) as isize;

                let (error_code, bytes) = if res < 0 {
                    let err = (-res) as i32;
                    unsafe {
                        crate::io::ERRNO = err;
                    }
                    (err, 0)
                } else {
                    unsafe {
                        buf.set_len(res as usize);
                    }
                    (0, res as u32)
                };

                COMPLETION_QUEUE.lock().push(Completion {
                    token,
                    buf,
                    error_code,
                    bytes,
                });
                queue_wake();
            });
        }
        Ok(())
    }

    pub(crate) fn submit_write(&mut self, fd: i32, state: Rc<OpState>) -> crate::io::Result<()> {
        let buf_opt = unsafe { &mut *state.buffer.get() }.take();
        if let Some(buf) = buf_opt {
            let token = self.next_token;
            self.next_token += 1;
            self.pending_ops.insert(token, PendingOp::Write(fd, state));

            crate::rt::blocking::pool().spawn(move || {
                let len = buf.len();
                let res = crate::syscall!(
                    crate::os::linux::syscall::nr::WRITE,
                    fd as usize,
                    buf.as_ptr() as usize,
                    len
                ) as isize;
                let (error_code, bytes) = if res < 0 {
                    let err = (-res) as i32;
                    unsafe {
                        crate::io::ERRNO = err;
                    }
                    (err, 0)
                } else {
                    (0, res as u32)
                };

                COMPLETION_QUEUE.lock().push(Completion {
                    token,
                    buf,
                    error_code,
                    bytes,
                });
                queue_wake();
            });
        }
        Ok(())
    }
}
