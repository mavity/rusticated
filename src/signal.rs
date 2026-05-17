//! OS signal abstractions

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

    #[cfg(unix)]
    extern "C" {
        fn pipe2(pipefd: *mut i32, flags: i32) -> i32;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
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
    extern "C" fn sigint_handler(_sig: libc::c_int) {
        if let Some(&[_, tx]) = SIGNAL_PIPE.get() {
            // SAFETY: `write(2)` is async-signal-safe.
            unsafe { write(tx, b"\x00".as_ptr(), 1) };
        }
    }

    // ── Windows: AtomicBool set by console ctrl handler ───────────────────────

    #[cfg(windows)]
    static CTRL_C_RECEIVED: AtomicBool = AtomicBool::new(false);

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
            libc::signal(libc::SIGINT, sigint_handler as libc::sighandler_t);
        }
        Ok(())
    }

    #[cfg(windows)]
    fn install_handler() -> io::Result<()> {
        // SAFETY: `console_ctrl_handler` is a valid PHANDLER_ROUTINE.
        let ok = unsafe {
            windows_sys::Win32::System::Console::SetConsoleCtrlHandler(
                Some(console_ctrl_handler),
                windows_sys::Win32::Foundation::TRUE,
            )
        };
        if ok == 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(())
        }
    }

    #[cfg(windows)]
    unsafe extern "system" fn console_ctrl_handler(event: u32) -> i32 {
        const CTRL_C_EVENT: u32 = 0;
        if event == CTRL_C_EVENT {
            CTRL_C_RECEIVED.store(true, Ordering::Release);
            1i32
        } else {
            0i32
        }
    }

    #[cfg(not(any(unix, windows)))]
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
    async fn ctrl_c_impl() -> io::Result<()> {
        // Spin in a background thread until the flag is set.
        crate::rt::native::spawn_blocking(|| {
            loop {
                if CTRL_C_RECEIVED.swap(false, Ordering::AcqRel) {
                    break;
                }
                std::thread::sleep(std::time::Duration::from_millis(50));
            }
        })?
        .await
    }

    #[cfg(not(any(unix, windows)))]
    async fn ctrl_c_impl() -> io::Result<()> {
        Err(io::Error::other("ctrl_c not supported on this platform"))
    }

    /// Wait asynchronously until Ctrl-C (SIGINT) is received.
    ///
    /// Installs a platform signal handler on first call.  On Unix the
    /// implementation waits on a pipe that the signal handler writes to;
    /// no spinning occurs.
    pub async fn ctrl_c() -> io::Result<()> {
        setup_handler()?;
        ctrl_c_impl().await
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_signal::ctrl_c;

// ——— WASM ——————————————————————————————————————————————————————————————————

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
