//! OS signal abstractions

#[cfg(not(target_family = "wasm"))]
mod native_signal {
    use std::{
        io,
        sync::atomic::{AtomicBool, Ordering},
    };

    use compio_buf::{BufResult, IntoInner};

    /// Set to `true` by the platform signal handler when Ctrl-C is received.
    static CTRL_C_RECEIVED: AtomicBool = AtomicBool::new(false);

    /// Guard against installing the handler more than once.
    static HANDLER_INSTALLED: AtomicBool = AtomicBool::new(false);

    /// Install the platform-specific Ctrl-C handler exactly once.
    fn setup_handler() -> io::Result<()> {
        if HANDLER_INSTALLED
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
            .is_err()
        {
            // Already installed by a previous call.
            return Ok(());
        }
        install_handler()
    }

    #[cfg(unix)]
    fn install_handler() -> io::Result<()> {
        // SAFETY: `sigint_handler` is async-signal-safe (stores to AtomicBool).
        unsafe {
            libc::signal(libc::SIGINT, sigint_handler as libc::sighandler_t);
        }
        Ok(())
    }

    #[cfg(unix)]
    extern "C" fn sigint_handler(_sig: libc::c_int) {
        CTRL_C_RECEIVED.store(true, Ordering::Release);
    }

    #[cfg(windows)]
    fn install_handler() -> io::Result<()> {
        // SAFETY: `console_ctrl_handler` is a valid `PHANDLER_ROUTINE`.
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

    // Fallback for platforms that are neither unix nor windows (e.g. stub builds).
    #[cfg(not(any(unix, windows)))]
    fn install_handler() -> io::Result<()> {
        Ok(())
    }

    /// Wait asynchronously until Ctrl-C (SIGINT) is received.
    ///
    /// Installs a platform signal handler on first call.  Subsequent calls
    /// share the same handler.  The function polls an [`AtomicBool`] flag
    /// (with 50 ms sleep between checks) in the proactor thread pool so that
    /// the executor is free to service other futures while waiting.
    pub async fn ctrl_c() -> io::Result<()> {
        setup_handler()?;
        let op = compio_driver::op::Asyncify::<_, ()>::new(|| {
            loop {
                // `swap(false)` clears the flag and returns whether it was set.
                if CTRL_C_RECEIVED.swap(false, Ordering::AcqRel) {
                    break;
                }
                std::thread::sleep(std::time::Duration::from_millis(50));
            }
            BufResult(Ok(0usize), ())
        });
        match crate::rt::native::OpFuture::new(op).await {
            Err(e) => Err(e),
            Ok(buf_result) => {
                let BufResult(res, ()) = buf_result.into_inner();
                res.map(|_| ())
            }
        }
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

