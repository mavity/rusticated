//! Native async runtime — minimal single-threaded executor with a platform
//! proactor.
//!
//! # Design
//!
//! [`block_on`] drives a single future to completion using a "poll everything"
//! loop: after each [`Poll::Pending`] the executor blocks in the platform
//! driver until at least one I/O event arrives, then re-polls the root future.
//!
//! Individual futures register interest via [`wait_readable`] /
//! [`wait_writable`], or offload blocking work via [`spawn_blocking`].  A
//! noop waker is used because the driver's `wait` return itself serves as the
//! wake signal.
//!
//! ## Platform support
//!
//! | Target        | Driver | Status           |
//! |---------------|--------|------------------|
//! | Linux         | epoll  | Implemented      |
//! | Windows       | IOCP   | Stub             |
//! | macOS / BSD   | kqueue | Stub             |

use std::{
    cell::RefCell,
    collections::HashSet,
    future::Future,
    io,
    pin::pin,
    task::{Context, Poll, RawWaker, RawWakerVTable, Waker},
};

// ── Ready-token registry ──────────────────────────────────────────────────────

thread_local! {
    /// Tokens for I/O events that have fired but whose futures have not yet
    /// been re-polled to observe the result.
    static READY: RefCell<HashSet<u64>> = RefCell::new(HashSet::new());
}

fn mark_ready(token: u64) {
    READY.with(|r| {
        r.borrow_mut().insert(token);
    });
}

#[cfg(any(target_os = "linux", all(unix, not(target_os = "linux"))))]
fn consume_ready(token: u64) -> bool {
    READY.with(|r| r.borrow_mut().remove(&token))
}

// ── Noop waker ────────────────────────────────────────────────────────────────

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

// ── block_on ──────────────────────────────────────────────────────────────────

/// Run `f` to completion on the current thread.
///
/// Alternates between polling `f` and blocking in the platform driver until
/// at least one I/O event arrives.
///
/// # Errors
///
/// Returns [`Err`] only if the platform driver fails to initialise or
/// encounters an unrecoverable error.
pub fn block_on<F: Future>(f: F) -> io::Result<F::Output> {
    // Ensure the driver is initialised before the first poll.
    with_driver(|_| {})?;

    let waker = noop_waker();
    let mut cx = Context::from_waker(&waker);
    let mut f = pin!(f);

    loop {
        if let Poll::Ready(v) = f.as_mut().poll(&mut cx) {
            return Ok(v);
        }
        // Block until something fires; the READY set is updated by the driver.
        with_driver(|d| d.wait(-1))??;
    }
}

// ── Thread-local driver ───────────────────────────────────────────────────────

thread_local! {
    static DRIVER: RefCell<Option<Driver>> = const { RefCell::new(None) };
}

pub(crate) fn with_driver<R>(f: impl FnOnce(&mut Driver) -> R) -> io::Result<R> {
    DRIVER.with(|cell| {
        let mut borrow = cell.borrow_mut();
        if borrow.is_none() {
            *borrow = Some(Driver::new()?);
        }
        Ok(f(borrow.as_mut().unwrap()))
    })
}

// ── Linux epoll driver ────────────────────────────────────────────────────────

#[cfg(target_os = "linux")]
use linux::Driver;

#[cfg(target_os = "linux")]
pub use linux::{JoinHandle, WaitReadable, WaitWritable, spawn_blocking};

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
        sync::{Arc, Mutex},
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

    /// epoll_event layout: packed 4+8 bytes on x86_64/aarch64 Linux.
    #[repr(C, packed)]
    struct EpollEvent {
        events: u32,
        data: u64,
    }

    /// pipe flags
    const O_CLOEXEC: i32 = 0o2_000_000;
    const O_NONBLOCK: i32 = 0o0_004_000;

    extern "C" {
        fn epoll_create1(flags: i32) -> i32;
        fn epoll_ctl(epfd: i32, op: i32, fd: i32, event: *mut EpollEvent) -> i32;
        fn epoll_wait(epfd: i32, events: *mut EpollEvent, maxevents: i32, timeout: i32) -> i32;
        fn pipe2(pipefd: *mut i32, flags: i32) -> i32;
        pub(super) fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        pub(super) fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        pub(super) fn close(fd: i32) -> i32;
    }

    // ── Driver ────────────────────────────────────────────────────────────────

    /// Linux epoll driver.
    pub struct Driver {
        epfd: i32,
        /// fds currently registered (even if ONESHOT-disabled) so we can
        /// choose EPOLL_CTL_MOD vs EPOLL_CTL_ADD correctly.
        registered_fds: HashMap<i32, ()>,
        next_token: u64,
    }

    impl Driver {
        /// Create a new epoll instance.
        pub fn new() -> io::Result<Self> {
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
            let r = unsafe { epoll_ctl(self.epfd, op, fd, &mut ev) };
            if r < 0 {
                let e = io::Error::last_os_error();
                if e.raw_os_error() == Some(EEXIST) {
                    // Race: try MOD instead.
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

        /// Block until ≥ 1 event arrives (or `timeout_ms` milliseconds pass).
        ///
        /// Fired tokens are inserted into the thread-local `READY` set.
        pub fn wait(&mut self, timeout_ms: i32) -> io::Result<()> {
            let mut evbuf = [EpollEvent { events: 0, data: 0 }; 64];
            let n = loop {
                let n = unsafe {
                    epoll_wait(
                        self.epfd,
                        evbuf.as_mut_ptr(),
                        evbuf.len() as i32,
                        timeout_ms,
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
                // Remove from our tracking so the next registration uses ADD.
                // We reconstruct the fd from the `registered_fds` map ... but
                // we only track fd→(), not token→fd.  Instead, leave the fd
                // in `registered_fds`; the next registration will use MOD
                // which re-enables it with the new ONESHOT token.
                mark_ready(ev.data);
            }
            Ok(())
        }
    }

    impl Drop for Driver {
        fn drop(&mut self) {
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
                Ok(token) => {
                    self.token = token;
                    self.registered = true;
                    Poll::Pending
                }
                Err(e) => Poll::Ready(Err(e)),
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
                Ok(token) => {
                    self.token = token;
                    self.registered = true;
                    Poll::Pending
                }
                Err(e) => Poll::Ready(Err(e)),
            }
        }
    }

    // ── spawn_blocking ────────────────────────────────────────────────────────

    /// Handle to a blocking operation running on a background thread.
    ///
    /// Resolves when the blocking function completes.
    pub struct JoinHandle<T> {
        read_fd: i32,
        result: Arc<Mutex<Option<T>>>,
        token: u64,
        registered: bool,
    }

    impl<T: 'static> Future for JoinHandle<T> {
        type Output = io::Result<T>;

        fn poll(mut self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<Self::Output> {
            if self.registered {
                if !consume_ready(self.token) {
                    return Poll::Pending;
                }
                // Drain the signalling byte written by the worker thread.
                let mut b = 0u8;
                unsafe { read(self.read_fd, &mut b, 1) };
                let val = self
                    .result
                    .lock()
                    .unwrap()
                    .take()
                    .ok_or_else(|| io::Error::other("spawn_blocking: result missing"))?;
                return Poll::Ready(Ok(val));
            }
            match with_driver(|d| d.register_read(self.read_fd)) {
                Ok(token) => {
                    self.token = token;
                    self.registered = true;
                    Poll::Pending
                }
                Err(e) => Poll::Ready(Err(e)),
            }
        }
    }

    impl<T> Drop for JoinHandle<T> {
        fn drop(&mut self) {
            unsafe { close(self.read_fd) };
        }
    }

    /// Run `f` on a new background thread; the returned [`JoinHandle`] future
    /// resolves when `f` completes.
    ///
    /// # Errors
    ///
    /// Returns [`Err`] if the signalling pipe cannot be created.
    pub fn spawn_blocking<T: Send + 'static>(
        f: impl FnOnce() -> T + Send + 'static,
    ) -> io::Result<JoinHandle<T>> {
        let result: Arc<Mutex<Option<T>>> = Arc::new(Mutex::new(None));
        let result2 = Arc::clone(&result);
        let mut fds = [0i32; 2];
        if unsafe { pipe2(fds.as_mut_ptr(), O_CLOEXEC | O_NONBLOCK) } < 0 {
            return Err(io::Error::last_os_error());
        }
        let [rx, tx] = fds;
        std::thread::spawn(move || {
            *result2.lock().unwrap() = Some(f());
            // Signal completion by writing one byte; ignore errors (the
            // JoinHandle may have been dropped).
            unsafe { write(tx, b"\x00".as_ptr(), 1) };
            unsafe { close(tx) };
        });
        Ok(JoinHandle {
            read_fd: rx,
            result,
            token: 0,
            registered: false,
        })
    }
}

// ── Windows IOCP driver (stub) ────────────────────────────────────────────────

#[cfg(windows)]
use windows_stub::Driver;

#[cfg(windows)]
mod windows_stub {
    use super::mark_ready;
    use std::{
        future::Future,
        io,
        pin::Pin,
        task::{Context, Poll},
    };

    /// Windows IOCP driver — not yet implemented.
    pub struct Driver;

    impl Driver {
        /// Create driver (stub).
        pub fn new() -> io::Result<Self> {
            Err(io::Error::other("IOCP driver not yet implemented"))
        }
        /// Wait for events (stub).
        pub fn wait(&mut self, _timeout_ms: i32) -> io::Result<()> {
            let _ = mark_ready; // suppress dead-code warnings
            Err(io::Error::other("IOCP driver not yet implemented"))
        }
    }

    /// Stub join handle for Windows.
    pub struct JoinHandle<T>(std::marker::PhantomData<T>);

    impl<T: 'static> Future for JoinHandle<T> {
        type Output = io::Result<T>;
        fn poll(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<Self::Output> {
            Poll::Ready(Err(io::Error::other("IOCP not yet implemented")))
        }
    }

    /// Stub for Windows.
    pub fn spawn_blocking<T: Send + 'static>(
        _f: impl FnOnce() -> T + Send + 'static,
    ) -> io::Result<JoinHandle<T>> {
        Err(io::Error::other("IOCP not yet implemented"))
    }

    /// Wait-readable stub.
    pub struct WaitReadable;

    impl WaitReadable {
        /// Create stub.
        pub fn new(_fd: u64) -> Self {
            Self
        }
    }

    impl Future for WaitReadable {
        type Output = io::Result<()>;
        fn poll(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
            Poll::Ready(Err(io::Error::other("IOCP not yet implemented")))
        }
    }

    /// Wait-writable stub.
    pub struct WaitWritable;

    impl WaitWritable {
        /// Create stub.
        pub fn new(_fd: u64) -> Self {
            Self
        }
    }

    impl Future for WaitWritable {
        type Output = io::Result<()>;
        fn poll(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
            Poll::Ready(Err(io::Error::other("IOCP not yet implemented")))
        }
    }
}

#[cfg(windows)]
pub use windows_stub::{JoinHandle, WaitReadable, WaitWritable, spawn_blocking};

// ── macOS / BSD kqueue driver (stub) ─────────────────────────────────────────

#[cfg(all(unix, not(target_os = "linux")))]
use kqueue_stub::Driver;

#[cfg(all(unix, not(target_os = "linux")))]
mod kqueue_stub {
    use super::mark_ready;
    use std::{
        future::Future,
        io,
        pin::Pin,
        sync::{Arc, Mutex},
        task::{Context, Poll},
    };

    /// kqueue driver — not yet implemented.
    pub struct Driver;

    impl Driver {
        /// Create driver (stub).
        pub fn new() -> io::Result<Self> {
            Err(io::Error::other("kqueue driver not yet implemented"))
        }
        /// Wait for events (stub).
        pub fn wait(&mut self, _timeout_ms: i32) -> io::Result<()> {
            let _ = mark_ready;
            Err(io::Error::other("kqueue driver not yet implemented"))
        }
    }

    /// Stub join handle for macOS/BSD.
    pub struct JoinHandle<T>(std::marker::PhantomData<T>);

    impl<T: 'static> Future for JoinHandle<T> {
        type Output = io::Result<T>;
        fn poll(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<Self::Output> {
            Poll::Ready(Err(io::Error::other("kqueue not yet implemented")))
        }
    }

    /// Stub for macOS/BSD.
    pub fn spawn_blocking<T: Send + 'static>(
        _f: impl FnOnce() -> T + Send + 'static,
    ) -> io::Result<JoinHandle<T>> {
        Err(io::Error::other("kqueue not yet implemented"))
    }

    /// Wait-readable stub.
    pub struct WaitReadable;

    impl WaitReadable {
        /// Create stub.
        pub fn new(_fd: i32) -> Self {
            Self
        }
    }

    impl Future for WaitReadable {
        type Output = io::Result<()>;
        fn poll(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
            Poll::Ready(Err(io::Error::other("kqueue not yet implemented")))
        }
    }

    /// Wait-writable stub.
    pub struct WaitWritable;

    impl WaitWritable {
        /// Create stub.
        pub fn new(_fd: i32) -> Self {
            Self
        }
    }

    impl Future for WaitWritable {
        type Output = io::Result<()>;
        fn poll(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<io::Result<()>> {
            Poll::Ready(Err(io::Error::other("kqueue not yet implemented")))
        }
    }
}

#[cfg(all(unix, not(target_os = "linux")))]
pub use kqueue_stub::{JoinHandle, WaitReadable, WaitWritable, spawn_blocking};

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
