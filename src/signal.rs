//! OS signal abstractions.
//!
//! - **Unix**: a `SIGINT` handler writes one byte to a self-pipe; [`ctrl_c`] awaits readability on
//!   the pipe through the runtime's epoll/kqueue driver. No polling.
//! - **Windows**: `SetConsoleCtrlHandler` routes console events through a future-based interface.
//! - **WASM**: host import [`crate::abi::imports::signal_wait`] drives the completion.

#![cfg_attr(
    target_family = "wasm",
    allow(
        clippy::cast_possible_truncation,
        clippy::cast_possible_wrap,
        clippy::cast_sign_loss,
    )
)]

#[cfg(not(target_family = "wasm"))]
mod native_signal {
    use crate::io;
    #[cfg(unix)]
    use core::sync::atomic::AtomicI32;
    use core::sync::atomic::{AtomicBool, Ordering};

    // ── Unix: pipe-based async signal ─────────────────────────────────────────

    /// Read end of the async Ctrl-C notification pipe (-1 = uninitialised).
    #[cfg(unix)]
    static SIGNAL_PIPE_READ: AtomicI32 = AtomicI32::new(-1);
    /// Write end of the async Ctrl-C notification pipe (-1 = uninitialised).
    #[cfg(unix)]
    static SIGNAL_PIPE_WRITE: AtomicI32 = AtomicI32::new(-1);

    // O_CLOEXEC differs between Linux and BSD/macOS.
    #[cfg(target_os = "linux")]
    const O_CLOEXEC: i32 = 0o2_000_000;
    #[cfg(any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
    ))]
    const O_CLOEXEC: i32 = 0x0100_0000;

    // O_NONBLOCK differs between Linux and BSD/macOS.
    #[cfg(target_os = "linux")]
    const O_NONBLOCK: i32 = 0o0_004_000;
    #[cfg(any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
    ))]
    const O_NONBLOCK: i32 = 0x0000_0004;

    /// POSIX `SIGINT`.
    #[cfg(unix)]
    const SIGINT: i32 = 2;

    /// Signal handler function pointer type, matching `libc::sighandler_t`.
    #[cfg(unix)]
    type SigHandlerFn = extern "C" fn(i32);

    #[cfg(unix)]
    unsafe extern "C" {
        fn pipe2(pipefd: *mut i32, flags: i32) -> i32;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        fn signal(signum: i32, handler: SigHandlerFn) -> usize;
    }

    #[cfg(unix)]
    fn get_signal_pipe() -> io::Result<[i32; 2]> {
        let r = SIGNAL_PIPE_READ.load(Ordering::Acquire);
        if r != -1 {
            let w = SIGNAL_PIPE_WRITE.load(Ordering::Acquire);
            return Ok([r, w]);
        }
        let mut fds = [0i32; 2];
        // SAFETY: `fds` is a valid 2-element array.
        if unsafe { pipe2(fds.as_mut_ptr(), O_CLOEXEC | O_NONBLOCK) } < 0 {
            return Err(io::Error::last_os_error());
        }
        // Store write end before read end so sigint_handler never observes a
        // valid read fd paired with an uninitialised write fd.
        SIGNAL_PIPE_WRITE.store(fds[1], Ordering::Release);
        SIGNAL_PIPE_READ.store(fds[0], Ordering::Release);
        Ok(fds)
    }

    /// Async-signal-safe handler: writes one byte to the signal pipe.
    #[cfg(unix)]
    extern "C" fn sigint_handler(_sig: i32) {
        let tx = SIGNAL_PIPE_WRITE.load(Ordering::Acquire);
        if tx != -1 {
            // SAFETY: `write(2)` is async-signal-safe.
            unsafe { write(tx, b"\x00".as_ptr(), 1) };
        }
    }

    // ── Handler installation ──────────────────────────────────────────────────

    /// Guard against installing the handler more than once.
    static HANDLER_INSTALLED: AtomicBool = AtomicBool::new(false);

    fn setup_handler() -> io::Result<()> {
        if HANDLER_INSTALLED
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
            .is_err()
        {
            return Ok(());
        }
        install_handler()
    }

    #[cfg(unix)]
    fn install_handler() -> io::Result<()> {
        // Initialise the pipe before the handler so it's available immediately.
        get_signal_pipe()?;
        // SAFETY: `sigint_handler` only calls `write(2)`, which is
        // async-signal-safe.
        unsafe {
            signal(SIGINT, sigint_handler);
        }
        Ok(())
    }

    #[cfg(windows)]
    static WINDOWS_WAKER: crate::sync::SpinMutex<Option<core::task::Waker>> =
        crate::sync::SpinMutex::new(None);
    #[cfg(windows)]
    static CTRL_C_TRIGGERED: core::sync::atomic::AtomicBool =
        core::sync::atomic::AtomicBool::new(false);

    #[cfg(windows)]
    extern "system" fn windows_ctrl_handler(ctrl_type: u32) -> i32 {
        if ctrl_type == 0 {
            // CTRL_C_EVENT
            CTRL_C_TRIGGERED.store(true, core::sync::atomic::Ordering::Release);
            {
                let mut lock = WINDOWS_WAKER.lock();
                if let Some(waker) = lock.take() {
                    waker.wake();
                }
            }
            1 // TRUE: we handled it
        } else {
            0 // FALSE: not handled
        }
    }

    #[cfg(windows)]
    #[allow(clippy::unnecessary_wraps, clippy::missing_const_for_fn)]
    fn install_handler() -> io::Result<()> {
        unsafe extern "system" {
            fn SetConsoleCtrlHandler(
                handler: Option<extern "system" fn(u32) -> i32>,
                add: i32,
            ) -> i32;
        }
        // SAFETY: We pass a valid function pointer to SetConsoleCtrlHandler.
        let res = unsafe { SetConsoleCtrlHandler(Some(windows_ctrl_handler), 1) };
        if res == 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(())
        }
    }

    #[cfg(not(any(unix, windows)))]
    #[allow(clippy::unnecessary_wraps)]
    fn install_handler() -> io::Result<()> {
        Ok(())
    }

    // ── Platform-specific ctrl_c body ─────────────────────────────────────────

    #[cfg(unix)]
    async fn ctrl_c_impl() -> io::Result<()> {
        let [rx, _] = get_signal_pipe()?;
        crate::rt::wait_readable(rx).await?;
        // Drain ALL bytes so accumulated signals don't re-fire ctrl_c immediately.
        let mut b = 0u8;
        loop {
            // SAFETY: `rx` is O_NONBLOCK; read returns -1/EAGAIN when empty.
            let n = unsafe { read(rx, &mut b, 1) };
            if n <= 0 {
                break;
            }
        }
        Ok(())
    }

    #[cfg(windows)]
    #[allow(clippy::unused_async, clippy::unnecessary_wraps)]
    async fn ctrl_c_impl() -> io::Result<()> {
        struct CtrlC;
        impl core::future::Future for CtrlC {
            type Output = io::Result<()>;
            fn poll(
                self: core::pin::Pin<&mut Self>,
                cx: &mut core::task::Context<'_>,
            ) -> core::task::Poll<Self::Output> {
                *WINDOWS_WAKER.lock() = Some(cx.waker().clone());

                if CTRL_C_TRIGGERED.swap(false, core::sync::atomic::Ordering::AcqRel) {
                    core::task::Poll::Ready(Ok(()))
                } else {
                    core::task::Poll::Pending
                }
            }
        }
        CtrlC.await
    }

    #[cfg(not(any(unix, windows)))]
    #[allow(clippy::unused_async)]
    async fn ctrl_c_impl() -> io::Result<()> {
        Err(io::Error::other("ctrl_c not supported on this platform"))
    }

    /// Wait asynchronously until Ctrl-C (SIGINT) is received.
    ///
    /// On Unix this installs a signal handler on first call and awaits the
    /// self-pipe through the runtime's reactor — no polling, no extra
    /// thread.
    pub async fn ctrl_c() -> io::Result<()> {
        setup_handler()?;
        ctrl_c_impl().await
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_signal::ctrl_c;

// ─── WASM ────────────────────────────────────────────────────────────────────

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

/// Wait for a POSIX signal by number on WASM.
#[cfg(target_family = "wasm")]
pub async fn signal_wait(signum: u32) -> crate::io::Result<()> {
    let (err, _, _) = OverlappedFuture::new(move |ov| {
        // SAFETY: `ov` is a valid overlapped pointer supplied by the runtime.
        unsafe { imports::signal_wait(ov, signum) };
    })
    .await;

    if err != 0 {
        return Err(crate::io::Error::from_raw_os_error(err as i32));
    }

    Ok(())
}

/// Wait asynchronously until Ctrl-C (SIGINT = 2) is received.
#[cfg(target_family = "wasm")]
pub async fn ctrl_c() -> crate::io::Result<()> {
    signal_wait(2).await
}
