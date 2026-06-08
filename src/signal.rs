//! OS signal abstractions.
//!
//! - **Unix**: a `SIGINT` handler writes one byte to a self-pipe; [`ctrl_c`] awaits readability on
//!   the pipe through the runtime's epoll/kqueue driver. No polling.
//! - **Windows**: `SetConsoleCtrlHandler` routes console events through a future-based interface.
//! - **WASM**: host import [`crate::abi::imports::signal_wait`] drives the completion.

/// Sends a signal to a process.
pub fn kill_process(_pid: crate::process::ProcessId, _signal: u32) -> crate::io::Result<()> {
    Err(crate::io::Error::other("kill_process not implemented"))
}

/// Standard POSIX signals.
#[derive(Clone, Copy, Debug, Eq, Hash, PartialEq)]
#[repr(i32)]
#[expect(clippy::upper_case_acronyms)]
pub enum Signal {
    /// Hangup detected on controlling terminal or death of controlling process.
    SIGHUP = 1,
    /// Interrupt from keyboard.
    SIGINT = 2,
    /// Quit from keyboard.
    SIGQUIT = 3,
    /// Illegal Instruction.
    SIGILL = 4,
    /// Trace/breakpoint trap.
    SIGTRAP = 5,
    /// Abort signal from abort(3).
    SIGABRT = 6,
    /// Bus error (bad memory access).
    SIGBUS = 7,
    /// Floating-point exception.
    SIGFPE = 8,
    /// Kill signal.
    SIGKILL = 9,
    /// User-defined signal 1.
    SIGUSR1 = 10,
    /// Invalid memory reference.
    SIGSEGV = 11,
    /// User-defined signal 2.
    SIGUSR2 = 12,
    /// Broken pipe: write to pipe with no readers.
    SIGPIPE = 13,
    /// Timer signal from alarm(2).
    SIGALRM = 14,
    /// Termination signal.
    SIGTERM = 15,
    /// Child stopped or terminated.
    SIGCHLD = 17,
    /// Continue if stopped.
    SIGCONT = 18,
    /// Stop process.
    SIGSTOP = 19,
    /// Stop typed at terminal.
    SIGTSTP = 20,
    /// Terminal input for background process.
    SIGTTIN = 21,
    /// Terminal output for background process.
    SIGTTOU = 22,
    /// Urgent condition on socket.
    SIGURG = 23,
    /// CPU time limit exceeded.
    SIGXCPU = 24,
    /// File size limit exceeded.
    SIGXFSZ = 25,
    /// Virtual alarm clock.
    SIGVTALRM = 26,
    /// Profiling timer expired.
    SIGPROF = 27,
    /// Windows resize signal.
    SIGWINCH = 28,
    /// I/O now possible.
    SIGIO = 29,
    /// Power failure (System V).
    SIGPWR = 30,
    /// Bad system call.
    SIGSYS = 31,
}

impl Signal {
    /// Returns an iterator over all standard signals.
    pub fn iterator() -> impl Iterator<Item = Self> {
        [
            Self::SIGHUP,
            Self::SIGINT,
            Self::SIGQUIT,
            Self::SIGILL,
            Self::SIGTRAP,
            Self::SIGABRT,
            Self::SIGBUS,
            Self::SIGFPE,
            Self::SIGKILL,
            Self::SIGUSR1,
            Self::SIGSEGV,
            Self::SIGUSR2,
            Self::SIGPIPE,
            Self::SIGALRM,
            Self::SIGTERM,
            Self::SIGCHLD,
            Self::SIGCONT,
            Self::SIGSTOP,
            Self::SIGTSTP,
            Self::SIGTTIN,
            Self::SIGTTOU,
            Self::SIGURG,
            Self::SIGXCPU,
            Self::SIGXFSZ,
            Self::SIGVTALRM,
            Self::SIGPROF,
            Self::SIGWINCH,
            Self::SIGIO,
            Self::SIGPWR,
            Self::SIGSYS,
        ]
        .into_iter()
    }

    /// Returns the name of the signal.
    pub const fn as_str(&self) -> &'static str {
        match self {
            Self::SIGHUP => "SIGHUP",
            Self::SIGINT => "SIGINT",
            Self::SIGQUIT => "SIGQUIT",
            Self::SIGILL => "SIGILL",
            Self::SIGTRAP => "SIGTRAP",
            Self::SIGABRT => "SIGABRT",
            Self::SIGBUS => "SIGBUS",
            Self::SIGFPE => "SIGFPE",
            Self::SIGKILL => "SIGKILL",
            Self::SIGUSR1 => "SIGUSR1",
            Self::SIGSEGV => "SIGSEGV",
            Self::SIGUSR2 => "SIGUSR2",
            Self::SIGPIPE => "SIGPIPE",
            Self::SIGALRM => "SIGALRM",
            Self::SIGTERM => "SIGTERM",
            Self::SIGCHLD => "SIGCHLD",
            Self::SIGCONT => "SIGCONT",
            Self::SIGSTOP => "SIGSTOP",
            Self::SIGTSTP => "SIGTSTP",
            Self::SIGTTIN => "SIGTTIN",
            Self::SIGTTOU => "SIGTTOU",
            Self::SIGURG => "SIGURG",
            Self::SIGXCPU => "SIGXCPU",
            Self::SIGXFSZ => "SIGXFSZ",
            Self::SIGVTALRM => "SIGVTALRM",
            Self::SIGPROF => "SIGPROF",
            Self::SIGWINCH => "SIGWINCH",
            Self::SIGIO => "SIGIO",
            Self::SIGPWR => "SIGPWR",
            Self::SIGSYS => "SIGSYS",
        }
    }
}

impl core::str::FromStr for Signal {
    type Err = crate::error::SystemError;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        for sig in Self::iterator() {
            if sig.as_str() == s {
                return Ok(sig);
            }
        }
        Err(crate::error::SystemError::Other(alloc::format!(
            "invalid signal: {s}"
        )))
    }
}

impl TryFrom<i32> for Signal {
    type Error = crate::error::SystemError;

    fn try_from(value: i32) -> Result<Self, Self::Error> {
        for sig in Self::iterator() {
            if sig as i32 == value {
                return Ok(sig);
            }
        }
        Err(crate::error::SystemError::Other(alloc::format!(
            "invalid signal number: {value}"
        )))
    }
}

impl TryFrom<u32> for Signal {
    type Error = crate::error::SystemError;

    fn try_from(value: u32) -> Result<Self, Self::Error> {
        Self::try_from(value as i32)
    }
}

#[cfg_attr(
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
    #[cfg(any(unix, rusticated_linux))]
    use core::sync::atomic::AtomicI32;
    use core::sync::atomic::{AtomicBool, Ordering};

    // ── Unix: pipe-based async signal ─────────────────────────────────────────

    /// Read end of the async Ctrl-C notification pipe (-1 = uninitialised).
    #[cfg(any(unix, rusticated_linux))]
    static SIGNAL_PIPE_READ: AtomicI32 = AtomicI32::new(-1);
    /// Write end of the async Ctrl-C notification pipe (-1 = uninitialised).
    #[cfg(any(unix, rusticated_linux))]
    static SIGNAL_PIPE_WRITE: AtomicI32 = AtomicI32::new(-1);

    // O_CLOEXEC differs between Linux and BSD/macOS.
    #[cfg(any(target_os = "linux", rusticated_linux))]
    const O_CLOEXEC: i32 = 0o2_000_000;
    #[cfg(any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
    ))]
    const O_CLOEXEC: i32 = 0x0100_0000;

    // O_NONBLOCK differs between Linux and BSD/macOS.
    #[cfg(any(target_os = "linux", rusticated_linux))]
    const O_NONBLOCK: i32 = 0o0_004_000;
    #[cfg(any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
    ))]
    const O_NONBLOCK: i32 = 0x0000_0004;

    /// POSIX `SIGINT`.
    #[cfg(any(unix, rusticated_linux))]
    const SIGINT: i32 = 2;

    /// Signal handler function pointer type.
    #[cfg(any(unix, rusticated_linux))]
    type SigHandlerFn = extern "C" fn(i32);

    #[cfg(all(unix, not(any(target_os = "linux", rusticated_linux))))]
    unsafe extern "C" {
        fn pipe2(pipefd: *mut i32, flags: i32) -> i32;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        fn signal(signum: i32, handler: SigHandlerFn) -> usize;
    }

    #[cfg(any(target_os = "linux", rusticated_linux))]
    fn linux_pipe2(fds: *mut i32, flags: i32) -> i32 {
        crate::syscall!(crate::os::linux::syscall::nr::PIPE2, fds as usize, flags as usize) as i32
    }

    #[cfg(any(target_os = "linux", rusticated_linux))]
    fn linux_write(fd: i32, buf: *const u8, count: usize) -> isize {
        crate::syscall!(
            crate::os::linux::syscall::nr::WRITE,
            fd as usize,
            buf as usize,
            count
        ) as isize
    }

    #[cfg(any(target_os = "linux", rusticated_linux))]
    fn linux_read(fd: i32, buf: *mut u8, count: usize) -> isize {
        crate::syscall!(
            crate::os::linux::syscall::nr::READ,
            fd as usize,
            buf as usize,
            count
        ) as isize
    }

    #[cfg(any(target_os = "linux", rusticated_linux))]
    fn linux_signal(signum: i32, handler: SigHandlerFn) -> usize {
        #[repr(C)]
        struct Sigaction {
            sa_handler: SigHandlerFn,
            sa_flags: usize,
            sa_restorer: usize,
            sa_mask: [u64; 1],
        }
        let sa = Sigaction {
            sa_handler: handler,
            sa_flags: 0x04000000, // SA_RESTORER (mostly ignored on modern architectures but good to set if we had one) or 0
            sa_restorer: 0,
            sa_mask: [0; 1],
        };
        // On Linux we should use rt_sigaction
        crate::syscall!(
            crate::os::linux::syscall::nr::RT_SIGACTION,
            signum as usize,
            &sa as *const _ as usize,
            0usize,
            8usize // size of sigset_t
        );
        0
    }

    #[cfg(any(unix, rusticated_linux))]
    fn get_signal_pipe() -> io::Result<[i32; 2]> {
        let r = SIGNAL_PIPE_READ.load(Ordering::Acquire);
        if r != -1 {
            let w = SIGNAL_PIPE_WRITE.load(Ordering::Acquire);
            return Ok([r, w]);
        }
        let mut fds = [0i32; 2];

        let res = {
            #[cfg(any(target_os = "linux", rusticated_linux))]
            {
                linux_pipe2(fds.as_mut_ptr(), O_CLOEXEC | O_NONBLOCK)
            }
            #[cfg(all(unix, not(any(target_os = "linux", rusticated_linux))))]
            {
                unsafe { pipe2(fds.as_mut_ptr(), O_CLOEXEC | O_NONBLOCK) }
            }
        };

        if res < 0 {
            return Err(io::Error::last_os_error());
        }
        // Store write end before read end so sigint_handler never observes a
        // valid read fd paired with an uninitialised write fd.
        SIGNAL_PIPE_WRITE.store(fds[1], Ordering::Release);
        SIGNAL_PIPE_READ.store(fds[0], Ordering::Release);
        Ok(fds)
    }

    /// Async-signal-safe handler: writes one byte to the signal pipe.
    #[cfg(any(unix, rusticated_linux))]
    extern "C" fn sigint_handler(_sig: i32) {
        let tx = SIGNAL_PIPE_WRITE.load(Ordering::Acquire);
        if tx != -1 {
            #[cfg(any(target_os = "linux", rusticated_linux))]
            {
                linux_write(tx, b"\x00".as_ptr(), 1);
            }
            #[cfg(all(unix, not(any(target_os = "linux", rusticated_linux))))]
            {
                // SAFETY: `write(2)` is async-signal-safe.
                unsafe { write(tx, b"\x00".as_ptr(), 1) };
            }
        }
    }

    // â”€â”€ Handler installation â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

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

    #[cfg(any(unix, rusticated_linux))]
    fn install_handler() -> io::Result<()> {
        // Initialise the pipe before the handler so it's available immediately.
        get_signal_pipe()?;
        // SAFETY: `sigint_handler` only calls `write(2)`, which is
        // async-signal-safe.
        #[cfg(any(target_os = "linux", rusticated_linux))]
        {
            linux_signal(SIGINT, sigint_handler);
        }
        #[cfg(all(unix, not(any(target_os = "linux", rusticated_linux))))]
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
        #[link(name = "kernel32", kind = "raw-dylib")]
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

    #[cfg(any(unix, rusticated_linux))]
    async fn ctrl_c_impl() -> io::Result<()> {
        let [rx, _] = get_signal_pipe()?;
        crate::rt::wait_readable(rx).await?;
        // Drain ALL bytes so accumulated signals don't re-fire ctrl_c immediately.
        let mut b = 0u8;
        loop {
            let n = {
                #[cfg(any(target_os = "linux", rusticated_linux))]
                {
                    linux_read(rx, &mut b, 1)
                }
                #[cfg(all(unix, not(any(target_os = "linux", rusticated_linux))))]
                {
                    // SAFETY: `rx` is O_NONBLOCK; read returns -1/EAGAIN when empty.
                    unsafe { read(rx, &mut b, 1) }
                }
            };
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
