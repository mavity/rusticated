//! OS signal abstractions.
//!
//! - **Unix**: a `SIGINT` handler writes one byte to a self-pipe; [`ctrl_c`] awaits readability on
//!   the pipe through the runtime's epoll/kqueue driver. No polling.
//! - **Windows**: backend pending — [`ctrl_c`] returns an error until the IOCP/console event
//!   integration lands.
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
    use std::{
        io,
        sync::atomic::{AtomicBool, Ordering},
    };

    #[cfg(unix)]
    use std::sync::OnceLock;

    // ── Unix: pipe-based async signal ─────────────────────────────────────────

    /// Pipe fds `[read_end, write_end]` used for async Ctrl-C notification.
    #[cfg(unix)]
    static SIGNAL_PIPE: OnceLock<[i32; 2]> = OnceLock::new();

    #[cfg(unix)]
    const O_CLOEXEC: i32 = 0o2_000_000;
    #[cfg(unix)]
    const O_NONBLOCK: i32 = 0o0_004_000;

    /// POSIX `SIGINT`.
    #[cfg(unix)]
    const SIGINT: i32 = 2;

    /// Signal handler function pointer type, matching `libc::sighandler_t`.
    #[cfg(unix)]
    type SigHandlerFn = extern "C" fn(i32);

    #[cfg(unix)]
    extern "C" {
        fn pipe2(pipefd: *mut i32, flags: i32) -> i32;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        fn signal(signum: i32, handler: SigHandlerFn) -> usize;
    }

    #[cfg(unix)]
    fn get_signal_pipe() -> io::Result<[i32; 2]> {
        SIGNAL_PIPE
            .get_or_try_init(|| {
                let mut fds = [0i32; 2];
                // SAFETY: `fds` is a valid 2-element array.
                if unsafe { pipe2(fds.as_mut_ptr(), O_CLOEXEC | O_NONBLOCK) } < 0 {
                    return Err(io::Error::last_os_error());
                }
                Ok(fds)
            })
            .copied()
    }

    /// Async-signal-safe handler: writes one byte to the signal pipe.
    #[cfg(unix)]
    extern "C" fn sigint_handler(_sig: i32) {
        if let Some(&[_, tx]) = SIGNAL_PIPE.get() {
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
    #[allow(clippy::unnecessary_wraps, clippy::missing_const_for_fn)]
    fn install_handler() -> io::Result<()> {
        // The Windows console-event integration is pending. We accept the
        // request so `setup_handler` succeeds and `ctrl_c_impl` returns a
        // clear error.
        Ok(())
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
        crate::rt::native::wait_readable(rx).await?;
        // Drain the signalling byte so the pipe doesn't re-fire immediately.
        let mut b = 0u8;
        // SAFETY: `rx` is readable; the read will not block (O_NONBLOCK).
        unsafe { read(rx, &mut b, 1) };
        Ok(())
    }

    #[cfg(windows)]
    #[allow(clippy::unused_async, clippy::unnecessary_wraps)]
    async fn ctrl_c_impl() -> io::Result<()> {
        Err(io::Error::other(
            "ctrl_c: Windows console-event backend pending",
        ))
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
pub async fn signal_wait(signum: u32) -> std::io::Result<()> {
    let (err, _, _) = OverlappedFuture::new(move |ov| {
        // SAFETY: `ov` is a valid overlapped pointer supplied by the runtime.
        unsafe { imports::signal_wait(ov, signum) };
    })
    .await;

    if err != 0 {
        return Err(std::io::Error::from_raw_os_error(err as i32));
    }

    Ok(())
}

/// Wait asynchronously until Ctrl-C (SIGINT = 2) is received.
#[cfg(target_family = "wasm")]
pub async fn ctrl_c() -> std::io::Result<()> {
    signal_wait(2).await
}
