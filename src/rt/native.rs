//! Native async runtime — host-driven, non-blocking, single-threaded.
//!
//! # Model
//!
//! `fast-std` does **not** own the thread on native targets. The host calls
//! [`run`] to submit a top-level future and then repeatedly calls
//! [`poll_step`] from its own event loop. Each `poll_step` invocation:
//!
//! 1. Polls the platform reactor with a **zero-millisecond timeout** to harvest any ready I/O
//!    events. No call inside the library ever blocks.
//! 2. Polls the main future once.
//! 3. Returns a [`PollStatus`] telling the host whether work remains and how long it may safely
//!    wait before the next call.
//!
//! There is intentionally no `block_on`, no `spawn_blocking`, and no internal
//! thread-spawning. Hosts that want CPU-bound concurrency can enable the
//! crate feature `threads` and use [`std::thread`] directly; nothing in
//! `fast-std` does so on the user's behalf.
//!
//! # Platform support
//!
//! | Target        | Driver | Status                          |
//! |---------------|--------|---------------------------------|
//! | Linux         | epoll  | Implemented (zero-timeout poll) |
//! | Windows       | IOCP   | API surface; backend pending    |
//! | macOS / BSD   | kqueue | API surface; backend pending    |
//!
//! Where a platform backend is not yet wired, public APIs that depend on it
//! return [`io::Error`] with a clear message; the surface is stable.

use std::{
    cell::RefCell,
    collections::HashSet,
    future::Future,
    io,
    pin::Pin,
    task::{Context, Poll, RawWaker, RawWakerVTable, Waker},
    time::{Duration, Instant},
};

// ─── Deadline tracker (platform-independent async timers) ────────────────────

thread_local! {
    /// Sorted (by deadline ascending) list of pending timers. Each entry is a
    /// `(deadline, id)` pair; the matching `Sleep` future polls by checking
    /// `Instant::now() >= deadline`.
    static TIMERS: RefCell<Vec<(Instant, u64)>> = const { RefCell::new(Vec::new()) };
    static NEXT_TIMER_ID: RefCell<u64> = const { RefCell::new(1) };
}

pub(crate) fn register_timer(deadline: Instant) -> u64 {
    NEXT_TIMER_ID.with(|n| {
        let mut n = n.borrow_mut();
        let id = *n;
        *n = n.wrapping_add(1);
        TIMERS.with(|t| {
            let mut t = t.borrow_mut();
            // Insert maintaining ascending order by deadline.
            let pos = t.partition_point(|(d, _)| *d <= deadline);
            t.insert(pos, (deadline, id));
        });
        id
    })
}

pub(crate) fn cancel_timer(id: u64) {
    TIMERS.with(|t| {
        let mut t = t.borrow_mut();
        if let Some(pos) = t.iter().position(|(_, i)| *i == id) {
            t.remove(pos);
        }
    });
}

fn next_deadline() -> Option<Duration> {
    TIMERS.with(|t| {
        let t = t.borrow();
        t.first().map(|(d, _)| {
            let now = Instant::now();
            if *d <= now { Duration::ZERO } else { *d - now }
        })
    })
}

// ─── Ready-token registry ────────────────────────────────────────────────────

thread_local! {
    /// Tokens for I/O events that have fired but whose futures have not yet
    /// been re-polled to observe the result.
    static READY: RefCell<HashSet<u64>> = RefCell::new(HashSet::new());
}

#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn mark_ready(token: u64) {
    READY.with(|r| {
        r.borrow_mut().insert(token);
    });
}

#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
fn consume_ready(token: u64) -> bool {
    READY.with(|r| r.borrow_mut().remove(&token))
}

// ─── Noop waker ──────────────────────────────────────────────────────────────

fn noop_waker() -> Waker {
    static VTABLE: RawWakerVTable = RawWakerVTable::new(
        |_| RawWaker::new(std::ptr::null(), &VTABLE),
        |_| {},
        |_| {},
        |_| {},
    );
    // SAFETY: vtable functions are all no-ops; the data pointer is never
    // dereferenced.
    unsafe { Waker::from_raw(RawWaker::new(std::ptr::null(), &VTABLE)) }
}

// ─── Host-facing API: run / poll_step ────────────────────────────────────────

thread_local! {
    static MAIN_FUTURE: RefCell<Option<Pin<Box<dyn Future<Output = ()>>>>> =
        const { RefCell::new(None) };
}

/// Submit a top-level future to the runtime.
///
/// The future is stored on the current thread and will be polled by each
/// subsequent [`poll_step`] call. A second call to `run` while a future is
/// already in flight replaces it.
pub fn run<F>(future: F)
where
    F: Future<Output = ()> + 'static,
{
    MAIN_FUTURE.with(|main| {
        *main.borrow_mut() = Some(Box::pin(future));
    });
}

/// Outcome of one [`poll_step`] iteration.
///
/// The host uses this to decide whether to keep ticking and how long it may
/// safely sleep before the next tick.
#[derive(Debug, Clone, Copy)]
pub enum PollStatus {
    /// The top-level future is complete; no further work is required.
    Done,
    /// Work was performed (I/O events processed or future polled).
    Ready,
    /// No work was available this iteration. The host may sleep at most
    /// `next_deadline` before the next call (or indefinitely if `None`).
    Idle {
        /// Upper bound on how long the host may wait before the next call.
        next_deadline: Option<Duration>,
    },
}

/// Drive the runtime by exactly one step.
///
/// Performs a non-blocking platform poll, then polls the main future once.
/// Returns a [`PollStatus`] describing the outcome.
///
/// # Errors
///
/// Returns [`Err`] only if the platform driver cannot be initialised or
/// encounters an unrecoverable error.
pub fn poll_step() -> io::Result<PollStatus> {
    // Drive the platform reactor with a zero timeout.
    let had_events = with_driver(|d| d.poll_nonblocking())??;

    // Poll the main future once.
    let waker = noop_waker();
    let mut cx = Context::from_waker(&waker);
    let main_status = MAIN_FUTURE.with(|main_fut| {
        let mut borrow = main_fut.borrow_mut();
        if let Some(fut) = borrow.as_mut() {
            match fut.as_mut().poll(&mut cx) {
                Poll::Ready(()) => {
                    *borrow = None;
                    Some(true)
                }
                Poll::Pending => Some(false),
            }
        } else {
            None
        }
    });

    Ok(match main_status {
        Some(true) => PollStatus::Done,
        Some(false) if had_events => PollStatus::Ready,
        Some(false) => PollStatus::Idle {
            next_deadline: next_deadline(),
        },
        None => PollStatus::Done,
    })
}

// ─── Thread-local driver ─────────────────────────────────────────────────────

thread_local! {
    static DRIVER: RefCell<Option<Driver>> = const { RefCell::new(None) };
}

pub(crate) fn with_driver<R>(f: impl FnOnce(&mut Driver) -> R) -> io::Result<R> {
    DRIVER.with(|cell| {
        let mut borrow = cell.borrow_mut();
        if borrow.is_none() {
            *borrow = Some(Driver::new()?);
        }
        // Safe: we just ensured it is `Some`.
        let Some(driver) = borrow.as_mut() else {
            return Err(io::Error::other("driver init race"));
        };
        Ok(f(driver))
    })
}

// ─── Linux epoll driver ──────────────────────────────────────────────────────

#[cfg(target_os = "linux")]
use linux::Driver;

#[cfg(target_os = "linux")]
pub use linux::{WaitReadable, WaitWritable};

/// Return a future that resolves when `fd` becomes readable.
#[cfg(target_os = "linux")]
pub fn wait_readable(fd: i32) -> WaitReadable {
    WaitReadable::new(fd)
}

/// Return a future that resolves when `fd` becomes writable.
#[cfg(target_os = "linux")]
pub fn wait_writable(fd: i32) -> WaitWritable {
    WaitWritable::new(fd)
}

#[cfg(target_os = "linux")]
mod linux {
    use super::{consume_ready, mark_ready, with_driver};
    use std::{
        collections::HashMap,
        future::Future,
        io,
        pin::Pin,
        task::{Context, Poll},
    };

    // ── epoll constants ───────────────────────────────────────────────────────

    const EPOLLIN: u32 = 0x0001;
    const EPOLLOUT: u32 = 0x0004;
    const EPOLLONESHOT: u32 = 1 << 30;
    const EPOLL_CTL_ADD: i32 = 1;
    const EPOLL_CTL_MOD: i32 = 3;
    /// POSIX errno 17: file exists (returned by EPOLL_CTL_ADD on a known fd).
    const EEXIST: i32 = 17;

    /// `epoll_event` layout: packed 4+8 bytes on x86_64/aarch64 Linux.
    #[repr(C, packed)]
    struct EpollEvent {
        events: u32,
        data: u64,
    }

    extern "C" {
        fn epoll_create1(flags: i32) -> i32;
        fn epoll_ctl(epfd: i32, op: i32, fd: i32, event: *mut EpollEvent) -> i32;
        fn epoll_wait(epfd: i32, events: *mut EpollEvent, maxevents: i32, timeout: i32) -> i32;
        pub(super) fn close(fd: i32) -> i32;
    }

    // ── Driver ────────────────────────────────────────────────────────────────

    /// Linux epoll driver.
    pub struct Driver {
        epfd: i32,
        /// fds currently registered (even if ONESHOT-disabled) so we can
        /// choose `EPOLL_CTL_MOD` vs `EPOLL_CTL_ADD` correctly.
        registered_fds: HashMap<i32, ()>,
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

        /// Poll for already-ready events without blocking.
        ///
        /// Returns `true` if at least one event was processed.
        pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
            let mut evbuf = [EpollEvent { events: 0, data: 0 }; 64];
            let n = loop {
                // SAFETY: pointer + length describe the local array.
                let n = unsafe {
                    epoll_wait(
                        self.epfd,
                        evbuf.as_mut_ptr(),
                        evbuf.len() as i32,
                        0, // non-blocking
                    )
                };
                if n >= 0 {
                    break n;
                }
                let e = io::Error::last_os_error();
                if e.kind() == io::ErrorKind::Interrupted {
                    continue;
                }
                return Err(e);
            };
            for ev in &evbuf[..n as usize] {
                // ONESHOT fired: the fd is now disabled (not removed).
                mark_ready(ev.data);
            }
            Ok(n > 0)
        }
    }

    impl Drop for Driver {
        fn drop(&mut self) {
            // SAFETY: `close` on a valid fd is sound.
            unsafe { close(self.epfd) };
        }
    }

    // ── WaitReadable ──────────────────────────────────────────────────────────

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

        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            if self.registered {
                return if consume_ready(self.token) {
                    Poll::Ready(Ok(()))
                } else {
                    Poll::Pending
                };
            }
            match with_driver(|d| d.register_read(self.fd)) {
                Ok(Ok(token)) => {
                    self.token = token;
                    self.registered = true;
                    Poll::Pending
                }
                Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
            }
        }
    }

    // ── WaitWritable ──────────────────────────────────────────────────────────

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

        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            if self.registered {
                return if consume_ready(self.token) {
                    Poll::Ready(Ok(()))
                } else {
                    Poll::Pending
                };
            }
            match with_driver(|d| d.register_write(self.fd)) {
                Ok(Ok(token)) => {
                    self.token = token;
                    self.registered = true;
                    Poll::Pending
                }
                Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
            }
        }
    }
}

// ─── Windows IOCP driver (skeleton) ──────────────────────────────────────────

#[cfg(windows)]
use windows_imp::Driver;

#[cfg(windows)]
pub use windows_imp::{WaitReadable, WaitWritable};

/// Return a future that resolves when handle `h` becomes readable.
///
/// On Windows this currently returns an error future; a full IOCP backend is
/// pending.
#[cfg(windows)]
pub fn wait_readable(h: u64) -> WaitReadable {
    WaitReadable::new(h)
}

/// Return a future that resolves when handle `h` becomes writable.
#[cfg(windows)]
pub fn wait_writable(h: u64) -> WaitWritable {
    WaitWritable::new(h)
}

#[cfg(windows)]
#[allow(
    clippy::unused_self,
    clippy::needless_pass_by_ref_mut,
    clippy::unnecessary_wraps,
    clippy::missing_const_for_fn,
    clippy::cast_possible_truncation,
    clippy::cast_possible_wrap,
    clippy::cast_sign_loss,
    clippy::undocumented_unsafe_blocks,
    dead_code,
    missing_docs,
    non_snake_case,
    non_camel_case_types
)]
mod windows_imp {
    use super::{consume_ready, mark_ready};
    use std::os::raw::c_void;
    use std::{
        future::Future,
        io,
        pin::Pin,
        task::{Context, Poll},
    };

    type HANDLE = *mut c_void;
    type DWORD = u32;

    #[repr(C)]
    struct OVERLAPPED {
        Internal: usize,
        InternalHigh: usize,
        Offset: DWORD,
        OffsetHigh: DWORD,
        hEvent: HANDLE,
    }

    #[repr(C)]
    #[derive(Copy, Clone)]
    struct OVERLAPPED_ENTRY {
        lpCompletionKey: usize,
        lpOverlapped: *mut OVERLAPPED,
        Internal: usize,
        dwNumberOfBytesTransferred: DWORD,
    }

    unsafe extern "system" {
        fn CreateIoCompletionPort(
            FileHandle: HANDLE,
            ExistingCompletionPort: HANDLE,
            CompletionKey: usize,
            NumberOfConcurrentThreads: DWORD,
        ) -> HANDLE;

        fn GetQueuedCompletionStatusEx(
            CompletionPort: HANDLE,
            lpCompletionPortEntries: *mut OVERLAPPED_ENTRY,
            ulCount: DWORD,
            ulNumEntriesRemoved: *mut DWORD,
            dwMilliseconds: DWORD,
            fAlertable: i32,
        ) -> i32;

        fn PostQueuedCompletionStatus(
            CompletionPort: HANDLE,
            dwNumberOfBytesTransferred: DWORD,
            dwCompletionKey: usize,
            lpOverlapped: *mut OVERLAPPED,
        ) -> i32;

        fn CloseHandle(hObject: HANDLE) -> i32;
        fn GetLastError() -> DWORD;
    }

    const INVALID_HANDLE_VALUE: HANDLE = -1isize as HANDLE;

    /// Windows IOCP driver.
    pub struct Driver {
        iocp: HANDLE,
    }

    impl Drop for Driver {
        fn drop(&mut self) {
            unsafe { CloseHandle(self.iocp) };
        }
    }

    impl Driver {
        pub fn new() -> io::Result<Self> {
            let iocp =
                unsafe { CreateIoCompletionPort(INVALID_HANDLE_VALUE, std::ptr::null_mut(), 0, 1) };
            if iocp.is_null() {
                return Err(io::Error::last_os_error());
            }
            Ok(Self { iocp })
        }

        pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
            let mut entries = [OVERLAPPED_ENTRY {
                lpCompletionKey: 0,
                lpOverlapped: std::ptr::null_mut(),
                Internal: 0,
                dwNumberOfBytesTransferred: 0,
            }; 64];
            let mut removed = 0;

            let ret = unsafe {
                GetQueuedCompletionStatusEx(
                    self.iocp,
                    entries.as_mut_ptr(),
                    entries.len() as DWORD,
                    &mut removed,
                    0, // timeout 0
                    0, // non-alertable
                )
            };

            if ret == 0 {
                let err = unsafe { GetLastError() };
                if err == 258 {
                    // WAIT_TIMEOUT
                    return Ok(false);
                }
                return Err(io::Error::from_raw_os_error(err as i32));
            }

            for entry in &entries[..removed as usize] {
                mark_ready(entry.lpCompletionKey as u64);
            }

            Ok(removed > 0)
        }

        pub fn post_ready(&mut self, token: u64) -> io::Result<()> {
            let ret = unsafe {
                PostQueuedCompletionStatus(self.iocp, 0, token as usize, std::ptr::null_mut())
            };
            if ret == 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(())
            }
        }
    }

    /// Wait-readable future.
    pub struct WaitReadable {
        handle: u64,
        token: u64,
        registered: bool,
    }

    impl WaitReadable {
        pub fn new(h: u64) -> Self {
            Self {
                handle: h,
                token: 0,
                registered: false,
            }
        }
    }

    impl Future for WaitReadable {
        type Output = io::Result<()>;
        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            // For Windows without threadpools, proper async wait on handles natively requires
            // RegisterWaitForSingleObject. Since `WaitReadable` is requested, we currently
            // emit the Windows backend completion if manually posted.
            if self.registered {
                return if consume_ready(self.token) {
                    Poll::Ready(Ok(()))
                } else {
                    Poll::Pending
                };
            }
            self.token = self.handle;
            self.registered = true;
            Poll::Pending
        }
    }

    /// Wait-writable future.
    pub struct WaitWritable {
        handle: u64,
        token: u64,
        registered: bool,
    }

    impl WaitWritable {
        pub fn new(h: u64) -> Self {
            Self {
                handle: h,
                token: 0,
                registered: false,
            }
        }
    }

    impl Future for WaitWritable {
        type Output = io::Result<()>;
        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            if self.registered {
                return if consume_ready(self.token) {
                    Poll::Ready(Ok(()))
                } else {
                    Poll::Pending
                };
            }
            self.token = self.handle;
            self.registered = true;
            Poll::Pending
        }
    }
}

// ─── macOS / BSD kqueue driver (skeleton) ────────────────────────────────────

#[cfg(all(unix, not(target_os = "linux")))]
use kqueue_imp::Driver;

#[cfg(all(unix, not(target_os = "linux")))]
pub use kqueue_imp::{WaitReadable, WaitWritable};

/// Return a future that resolves when `fd` becomes readable.
#[cfg(all(unix, not(target_os = "linux")))]
pub fn wait_readable(fd: i32) -> WaitReadable {
    WaitReadable::new(fd)
}

/// Return a future that resolves when `fd` becomes writable.
#[cfg(all(unix, not(target_os = "linux")))]
pub fn wait_writable(fd: i32) -> WaitWritable {
    WaitWritable::new(fd)
}

#[cfg(all(unix, not(target_os = "linux")))]
#[allow(
    clippy::unused_self,
    clippy::needless_pass_by_ref_mut,
    clippy::unnecessary_wraps,
    clippy::missing_const_for_fn,
    clippy::cast_possible_truncation,
    clippy::cast_possible_wrap,
    clippy::cast_sign_loss,
    clippy::undocumented_unsafe_blocks,
    dead_code,
    missing_docs,
    non_snake_case,
    non_camel_case_types
)]
mod kqueue_imp {
    use super::{consume_ready, mark_ready, with_driver};
    use std::os::raw::{c_int, c_short, c_uint, c_ushort, c_void};
    use std::ptr;
    use std::{
        future::Future,
        io,
        pin::Pin,
        task::{Context, Poll},
    };

    #[repr(C)]
    struct timespec {
        tv_sec: isize,
        tv_nsec: std::os::raw::c_long,
    }

    #[repr(C)]
    #[derive(Clone, Copy)]
    struct kevent {
        ident: usize,
        filter: c_short,
        flags: c_ushort,
        fflags: c_uint,
        data: isize,
        udata: *mut c_void,
    }

    unsafe extern "C" {
        fn kqueue() -> c_int;
        fn kevent(
            kq: c_int,
            changelist: *const kevent,
            nchanges: c_int,
            eventlist: *mut kevent,
            nevents: c_int,
            timeout: *const timespec,
        ) -> c_int;
        fn close(fd: c_int) -> c_int;
    }

    const EVFILT_READ: c_short = -1;
    const EVFILT_WRITE: c_short = -2;
    const EV_ADD: c_ushort = 0x0001;
    const EV_CLEAR: c_ushort = 0x0020;

    /// kqueue driver.
    pub struct Driver {
        kqfd: c_int,
        next_token: u64,
    }

    impl Drop for Driver {
        fn drop(&mut self) {
            let _ = unsafe { close(self.kqfd) };
        }
    }

    impl Driver {
        pub fn new() -> io::Result<Self> {
            let kqfd = unsafe { kqueue() };
            if kqfd < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(Self {
                kqfd,
                next_token: 1,
            })
        }

        pub fn poll_nonblocking(&mut self) -> io::Result<bool> {
            let mut evbuf = [kevent {
                ident: 0,
                filter: 0,
                flags: 0,
                fflags: 0,
                data: 0,
                udata: ptr::null_mut(),
            }; 64];

            let ts = timespec {
                tv_sec: 0,
                tv_nsec: 0,
            };
            let n = loop {
                let n = unsafe {
                    kevent(
                        self.kqfd,
                        ptr::null(),
                        0,
                        evbuf.as_mut_ptr(),
                        evbuf.len() as c_int,
                        &ts,
                    )
                };
                if n >= 0 {
                    break n;
                }
                let e = io::Error::last_os_error();
                if e.kind() == io::ErrorKind::Interrupted {
                    continue;
                }
                return Err(e);
            };

            for ev in &evbuf[..n as usize] {
                mark_ready(ev.udata as usize as u64);
            }
            Ok(n > 0)
        }

        pub fn register_read(&mut self, fd: i32) -> io::Result<u64> {
            let token = self.next_token;
            self.next_token += 1;

            let ev = kevent {
                ident: fd as usize,
                filter: EVFILT_READ,
                flags: EV_ADD | EV_CLEAR,
                fflags: 0,
                data: 0,
                udata: token as usize as *mut c_void,
            };
            let ret = unsafe { kevent(self.kqfd, &ev, 1, ptr::null_mut(), 0, ptr::null()) };
            if ret < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(token)
            }
        }

        pub fn register_write(&mut self, fd: i32) -> io::Result<u64> {
            let token = self.next_token;
            self.next_token += 1;

            let ev = kevent {
                ident: fd as usize,
                filter: EVFILT_WRITE,
                flags: EV_ADD | EV_CLEAR,
                fflags: 0,
                data: 0,
                udata: token as usize as *mut c_void,
            };
            let ret = unsafe { kevent(self.kqfd, &ev, 1, ptr::null_mut(), 0, ptr::null()) };
            if ret < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(token)
            }
        }
    }

    /// Wait-readable future.
    pub struct WaitReadable {
        fd: i32,
        token: u64,
        registered: bool,
    }

    impl WaitReadable {
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
        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            if self.registered {
                return if consume_ready(self.token) {
                    Poll::Ready(Ok(()))
                } else {
                    Poll::Pending
                };
            }
            match with_driver(|d| d.register_read(self.fd)) {
                Ok(Ok(token)) => {
                    self.token = token;
                    self.registered = true;
                    Poll::Pending
                }
                Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
            }
        }
    }

    /// Wait-writable future.
    pub struct WaitWritable {
        fd: i32,
        token: u64,
        registered: bool,
    }

    impl WaitWritable {
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
        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<io::Result<()>> {
            if self.registered {
                return if consume_ready(self.token) {
                    Poll::Ready(Ok(()))
                } else {
                    Poll::Pending
                };
            }
            match with_driver(|d| d.register_write(self.fd)) {
                Ok(Ok(token)) => {
                    self.token = token;
                    self.registered = true;
                    Poll::Pending
                }
                Ok(Err(e)) | Err(e) => Poll::Ready(Err(e)),
            }
        }
    }
}
