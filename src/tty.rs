//! TTY management — provides [`Tty`], [`stdin`], [`stdout`], [`get_size`],
//! [`set_mode`], [`enable_raw_mode`], [`disable_raw_mode`], and
//! [`cursor_position`] across all platforms.

#[cfg(unix)]
pub use unix_tty::{Tty, cursor_position, disable_raw_mode, enable_raw_mode, get_size, set_mode, stdin, stdout};

#[cfg(windows)]
pub use windows_tty::{Tty, cursor_position, disable_raw_mode, enable_raw_mode, get_size, set_mode, stdin, stdout};

#[cfg(target_family = "wasm")]
pub use wasm_tty::{Tty, cursor_position, disable_raw_mode, enable_raw_mode, get_size, set_mode, stdin, stdout};

/// Returns `true` if standard input is a terminal.
pub fn is_stdin_a_tty() -> bool {
    false
}

// ─── Unix (Linux + BSD/macOS) ─────────────────────────────────────────────────

#[cfg(unix)]
mod unix_tty {
    use crate::io;
    use crate::vec::Vec;

    unsafe extern "C" {
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        fn ioctl(fd: i32, request: usize, ...) -> i32;
    }

    // TIOCGWINSZ value per platform (ioctl to query terminal size).
    #[cfg(target_os = "linux")]
    const TIOCGWINSZ: usize = 0x5413;
    #[cfg(any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd"
    ))]
    const TIOCGWINSZ: usize = 0x4008_7468;

    #[repr(C)]
    struct Winsize {
        ws_row: u16,
        ws_col: u16,
        ws_xpixel: u16,
        ws_ypixel: u16,
    }

    /// Async handle to a TTY file descriptor.
    pub struct Tty {
        fd: i32,
    }

    /// Return an async handle to the process's standard input (`fd 0`).
    pub fn stdin() -> Tty {
        Tty { fd: 0 }
    }

    /// Return an async handle to the process's standard output (`fd 1`).
    pub fn stdout() -> Tty {
        Tty { fd: 1 }
    }

    /// Query the terminal size for `handle` (interpreted as a file descriptor).
    ///
    /// Falls back to `(80, 24)` if the `ioctl` is unavailable on this target.
    pub fn get_size(handle: u64) -> io::Result<(u16, u16)> {
        let mut ws = Winsize {
            ws_row: 0,
            ws_col: 0,
            ws_xpixel: 0,
            ws_ypixel: 0,
        };
        // SAFETY: fd is caller-supplied; `ws` is a valid local struct.
        let ret = unsafe { ioctl(handle as i32, TIOCGWINSZ, &mut ws as *mut Winsize) };
        if ret < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok((ws.ws_col, ws.ws_row))
    }

    /// Set the terminal mode for `handle`.
    ///
    /// Currently a stub — returns `Ok(())`.  Wire up `tcsetattr` as needed.
    pub const fn set_mode(_handle: u64, _mode: u32) -> io::Result<()> {
        Ok(())
    }

    // ── Raw mode ──────────────────────────────────────────────────────────────

    /// Per-platform termios layout and flag constants.
    #[cfg(target_os = "linux")]
    #[allow(dead_code)]
    mod tc {
        /// c_lflag: disable canonical (line-buffered) input.
        pub const ICANON: u32 = 0x0000_0002;
        /// c_lflag: disable input echo.
        pub const ECHO: u32 = 0x0000_0008;
        /// c_lflag: disable signal generation for special characters.
        pub const ISIG: u32 = 0x0000_0001;
        /// c_lflag: disable extended processing.
        pub const IEXTEN: u32 = 0x0000_8000;
        /// c_iflag: disable XON/XOFF flow control.
        pub const IXON: u32 = 0x0000_0400;
        /// c_iflag: disable stripping of 8th bit.
        pub const ISTRIP: u32 = 0x0000_0020;
        /// c_iflag: disable CR to NL translation.
        pub const ICRNL: u32 = 0x0000_0100;
        /// c_oflag: disable output post-processing.
        pub const OPOST: u32 = 0x0000_0001;
        /// c_cflag: character-size mask.
        pub const CSIZE: u32 = 0x0000_0030;
        /// c_cflag: 8-bit characters.
        pub const CS8: u32 = 0x0000_0030;
        /// c_cflag: parity enable.
        pub const PARENB: u32 = 0x0000_0100;
        /// c_cflag: mark parity errors.
        pub const PARMRK: u32 = 0x0000_0008;
        /// c_iflag: ignore break condition.
        pub const IGNBRK: u32 = 0x0000_0001;
        /// c_iflag: send SIGINT on break.
        pub const BRKINT: u32 = 0x0000_0002;
        /// c_iflag: mark parity and framing errors.
        pub const PARMRK_IFLAG: u32 = 0x0000_0008;
        /// c_iflag: enable input parity checking.
        pub const INPCK: u32 = 0x0000_0010;
        /// c_iflag: translate NL to CR on input.
        pub const INLCR: u32 = 0x0000_0080;
        /// c_iflag: ignore CR.
        pub const IGNCR: u32 = 0x0000_0100;
        /// Index into c_cc for minimum characters.
        pub const VMIN: usize = 6;
        /// Index into c_cc for timeout.
        pub const VTIME: usize = 5;
        pub const TCSANOW: i32 = 0;

        #[repr(C)]
        pub struct Termios {
            pub c_iflag: u32,
            pub c_oflag: u32,
            pub c_cflag: u32,
            pub c_lflag: u32,
            pub c_line: u8,
            pub c_cc: [u8; 19],
            pub c_ispeed: u32,
            pub c_ospeed: u32,
        }
    }

    #[cfg(any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
    ))]
    mod tc {
        pub const ICANON: u32 = 0x0000_0100;
        pub const ECHO: u32 = 0x0000_0008;
        pub const ISIG: u32 = 0x0000_0080;
        pub const IEXTEN: u32 = 0x0000_0400;
        pub const IXON: u32 = 0x0000_0200;
        pub const ISTRIP: u32 = 0x0000_0020;
        pub const ICRNL: u32 = 0x0000_0100;
        pub const OPOST: u32 = 0x0000_0001;
        pub const CSIZE: u32 = 0x0000_0300;
        pub const CS8: u32 = 0x0000_0300;
        pub const PARENB: u32 = 0x0000_1000;
        pub const PARMRK: u32 = 0x0000_0008;
        pub const IGNBRK: u32 = 0x0000_0001;
        pub const BRKINT: u32 = 0x0000_0002;
        pub const PARMRK_IFLAG: u32 = 0x0000_0008;
        pub const INPCK: u32 = 0x0000_0010;
        pub const INLCR: u32 = 0x0000_0040;
        pub const IGNCR: u32 = 0x0000_0080;
        pub const VMIN: usize = 16;
        pub const VTIME: usize = 17;
        pub const TCSANOW: i32 = 0;

        /// Termios struct layout for macOS/BSD (64-bit targets).
        #[repr(C)]
        pub struct Termios {
            pub c_iflag: u32,
            pub c_oflag: u32,
            pub c_cflag: u32,
            pub c_lflag: u32,
            pub c_cc: [u8; 20],
            pub _pad: [u8; 4],
            pub c_ispeed: u64,
            pub c_ospeed: u64,
        }
    }

    unsafe extern "C" {
        fn tcgetattr(fd: i32, termios: *mut tc::Termios) -> i32;
        fn tcsetattr(fd: i32, optional_actions: i32, termios: *const tc::Termios) -> i32;
    }

    use crate::sync::Mutex;

    static SAVED_TERMIOS: Mutex<Option<tc::Termios>> = Mutex::new(None);

    // SAFETY: `tc::Termios` contains only POD integer/byte fields — safe to
    // send across thread boundaries.
    unsafe impl Send for tc::Termios {}

    /// Switch stdin into raw mode (no echo, no line buffering).
    ///
    /// Saves the original termios so [`disable_raw_mode`] can restore it.
    pub fn enable_raw_mode() -> io::Result<()> {
        let mut orig = core::mem::MaybeUninit::<tc::Termios>::uninit();
        // SAFETY: fd 0 is stdin; `orig` is valid memory for the call.
        let ret = unsafe { tcgetattr(0, orig.as_mut_ptr()) };
        if ret < 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: tcgetattr initialised the struct.
        let orig = unsafe { orig.assume_init() };

        // Build raw-mode termios from the original.
        let mut raw = tc::Termios {
            c_iflag: orig.c_iflag
                & !(tc::IGNBRK
                    | tc::BRKINT
                    | tc::PARMRK_IFLAG
                    | tc::ISTRIP
                    | tc::INLCR
                    | tc::IGNCR
                    | tc::ICRNL
                    | tc::IXON),
            c_oflag: orig.c_oflag & !tc::OPOST,
            c_cflag: (orig.c_cflag & !tc::CSIZE & !tc::PARENB) | tc::CS8,
            c_lflag: orig.c_lflag & !(tc::ECHO | tc::ISIG | tc::ICANON | tc::IEXTEN),
            ..orig
        };
        raw.c_cc[tc::VMIN] = 1;
        raw.c_cc[tc::VTIME] = 0;

        // SAFETY: fd 0 is stdin; `raw` is a valid termios.
        let ret = unsafe { tcsetattr(0, tc::TCSANOW, &raw) };
        if ret < 0 {
            return Err(io::Error::last_os_error());
        }

        *SAVED_TERMIOS.lock() = Some(orig);
        Ok(())
    }

    /// Restore the terminal to the state saved by the last [`enable_raw_mode`] call.
    pub fn disable_raw_mode() -> io::Result<()> {
        let guard = SAVED_TERMIOS.lock();
        if let Some(ref orig) = *guard {
            // SAFETY: fd 0 is stdin; `orig` is a valid termios saved earlier.
            let ret = unsafe { tcsetattr(0, tc::TCSANOW, orig) };
            if ret < 0 {
                return Err(io::Error::last_os_error());
            }
        }
        Ok(())
    }

    /// Query the current cursor position by sending a DSR escape to stdout
    /// and reading the `ESC [ row ; col R` response from stdin.
    ///
    /// Returns `(col, row)` both 0-indexed.  Falls back to `(0, 0)` on error.
    pub fn cursor_position() -> io::Result<(u16, u16)> {
        // Send Device Status Report (DSR) to stdout.
        let dsr = b"\x1b[6n";
        let n = unsafe { write(1, dsr.as_ptr(), dsr.len()) };
        if n < 0 {
            return Err(io::Error::last_os_error());
        }

        // Read the response `ESC [ row ; col R` from stdin byte-by-byte.
        let mut buf = [0u8; 32];
        let mut len = 0usize;
        loop {
            let n = unsafe { read(0, buf.as_mut_ptr().add(len), 1) };
            if n <= 0 {
                return Err(io::Error::last_os_error());
            }
            len += 1;
            if buf[len - 1] == b'R' {
                break;
            }
            if len >= buf.len() {
                break;
            }
        }

        // Parse `ESC [ row ; col R`.
        parse_cursor_response(&buf[..len]).ok_or(io::Error::other("bad DSR response"))
    }

    fn parse_cursor_response(buf: &[u8]) -> Option<(u16, u16)> {
        // Expect: 0x1b '[' digits ';' digits 'R'
        if buf.len() < 6 || buf[0] != 0x1b || buf[1] != b'[' || buf[buf.len() - 1] != b'R' {
            return None;
        }
        let inner = &buf[2..buf.len() - 1];
        let sep = inner.iter().position(|&b| b == b';')?;
        let row = parse_ascii_u16(&inner[..sep])?;
        let col = parse_ascii_u16(&inner[sep + 1..])?;
        Some((col.saturating_sub(1), row.saturating_sub(1)))
    }

    fn parse_ascii_u16(bytes: &[u8]) -> Option<u16> {
        if bytes.is_empty() {
            return None;
        }
        let mut n: u16 = 0;
        for &b in bytes {
            if b < b'0' || b > b'9' {
                return None;
            }
            n = n.checked_mul(10)?.checked_add((b - b'0') as u16)?;
        }
        Some(n)
    }

    impl crate::io::IsTerminal for Tty {
        fn is_terminal(&self) -> bool {
            unsafe extern "C" {
                // SAFETY: `isatty` is a pure query with no side effects.
                fn isatty(fd: i32) -> i32;
            }
            // SAFETY: Any `i32` fd value is safe to pass; returns 0 for non-ttys.
            unsafe { isatty(self.fd) != 0 }
        }
    }

    impl crate::io::AsyncRead for Tty {
        async fn read(&mut self, mut buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            // Wait until the fd is readable (epoll/kqueue), then do a direct
            // non-blocking read.
            if let Err(e) = crate::rt::wait_readable(self.fd).await {
                return (Err(e), buf);
            }
            // SAFETY: `buf.as_mut_ptr()` is valid for `buf.capacity()` bytes.
            let n = unsafe { read(self.fd, buf.as_mut_ptr(), buf.capacity()) };
            if n < 0 {
                (Err(io::Error::last_os_error()), buf)
            } else {
                // SAFETY: the kernel wrote exactly `n` bytes.
                unsafe { buf.set_len(n as usize) };
                (Ok(n as usize), buf)
            }
        }
    }

    impl crate::io::Read for Tty {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            let n = unsafe { read(self.fd, buf.as_mut_ptr(), buf.len()) };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        }
    }

    impl crate::io::AsyncWrite for Tty {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            // SAFETY: `buf.as_ptr()` is valid for `buf.len()` bytes.
            let n = unsafe { write(self.fd, buf.as_ptr(), buf.len()) };
            if n < 0 {
                (Err(io::Error::last_os_error()), buf)
            } else {
                (Ok(n as usize), buf)
            }
        }
        async fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    impl crate::io::Write for Tty {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            let n = unsafe { write(self.fd, buf.as_ptr(), buf.len()) };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }
}

// ─── Windows ──────────────────────────────────────────────────────────────────

#[cfg(windows)]
mod windows_tty {
    #![allow(
        clippy::pedantic,
        clippy::undocumented_unsafe_blocks,
        clippy::cast_possible_truncation,
        clippy::borrow_as_ptr
    )]

    use crate::boxed::Box;
    use crate::io;
    use crate::pin::Pin;
    use crate::ptr;
    use crate::sync::atomic::{AtomicU32, AtomicUsize, Ordering};
    use crate::task::{Context, Poll};
    use crate::vec::Vec;

    use crate::future::Future;
    use crate::rt::executor::with_driver;
    use crate::rt::ready::consume_ready;
    // Use the Overlapped type declared in the windows driver to avoid clashing
    // extern declarations.
    use crate::rt::windows::Overlapped;

    // ── Windows API ─────────────────────────────────────────────────────────

    #[link(name = "kernel32", kind = "raw-dylib")]
    unsafe extern "system" {
        fn GetStdHandle(n_std_handle: u32) -> usize;
        fn GetFileType(hFile: usize) -> u32;
        fn ReadFile(
            file: usize,
            buffer: *mut u8,
            number_of_bytes_to_read: u32,
            number_of_bytes_read: *mut u32,
            overlapped: *mut Overlapped,
        ) -> i32;
        fn WriteFile(
            file: usize,
            buffer: *const u8,
            number_of_bytes_to_write: u32,
            number_of_bytes_written: *mut u32,
            overlapped: *mut core::ffi::c_void,
        ) -> i32;
        fn CreateThread(
            lpThreadAttributes: *mut core::ffi::c_void,
            dwStackSize: usize,
            lpStartAddress: Option<unsafe extern "system" fn(*mut core::ffi::c_void) -> u32>,
            lpParameter: *mut core::ffi::c_void,
            dwCreationFlags: u32,
            lpThreadId: *mut u32,
        ) -> usize;
        fn CancelSynchronousIo(hThread: usize) -> i32;
        fn WaitForSingleObject(hHandle: usize, dwMilliseconds: u32) -> u32;
        fn CloseHandle(hObject: usize) -> i32;
    }

    const STD_INPUT_HANDLE: u32 = 0xFFFF_FFF6;
    const STD_OUTPUT_HANDLE: u32 = 0xFFFF_FFF5;
    const FILE_TYPE_CHAR: u32 = 0x0002;
    #[allow(dead_code)]
    const INFINITE: u32 = 0xFFFF_FFFF;

    // ── Tty ─────────────────────────────────────────────────────────────────

    /// Async handle to a Windows console handle.
    pub struct Tty {
        handle: usize,
    }

    /// Return an async handle to `STDIN` (console input).
    pub fn stdin() -> Tty {
        // SAFETY: GetStdHandle with a valid constant is always safe to call.
        Tty {
            handle: unsafe { GetStdHandle(STD_INPUT_HANDLE) },
        }
    }

    /// Return an async handle to `STDOUT` (console output).
    pub fn stdout() -> Tty {
        // SAFETY: GetStdHandle with a valid constant is always safe to call.
        Tty {
            handle: unsafe { GetStdHandle(STD_OUTPUT_HANDLE) },
        }
    }

    /// Query the console window size.
    ///
    /// Currently returns `(80, 24)` as a stub — wire up
    /// `GetConsoleScreenBufferInfo` as needed.
    pub const fn get_size(_handle: u64) -> io::Result<(u16, u16)> {
        Ok((80, 24))
    }

    /// Set the console mode for `handle`.
    ///
    /// Currently a stub — returns `Ok(())`.
    pub const fn set_mode(_handle: u64, _mode: u32) -> io::Result<()> {
        Ok(())
    }

    // ── Console mode constants ────────────────────────────────────────────────

    const ENABLE_LINE_INPUT: u32 = 0x0002;
    const ENABLE_ECHO_INPUT: u32 = 0x0004;
    const ENABLE_VIRTUAL_TERMINAL_INPUT: u32 = 0x0200;
    const ENABLE_VIRTUAL_TERMINAL_PROCESSING: u32 = 0x0004;

    #[link(name = "kernel32", kind = "raw-dylib")]
    unsafe extern "system" {
        fn GetConsoleMode(hConsoleHandle: usize, lpMode: *mut u32) -> i32;
        fn SetConsoleMode(hConsoleHandle: usize, dwMode: u32) -> i32;
    }

    #[repr(C)]
    struct COORD {
        x: i16,
        y: i16,
    }
    #[repr(C)]
    struct SMALL_RECT {
        left: i16,
        top: i16,
        right: i16,
        bottom: i16,
    }
    #[repr(C)]
    struct CONSOLE_SCREEN_BUFFER_INFO {
        dw_size: COORD,
        dw_cursor_position: COORD,
        w_attributes: u16,
        sr_window: SMALL_RECT,
        dw_maximum_window_size: COORD,
    }

    #[link(name = "kernel32", kind = "raw-dylib")]
    unsafe extern "system" {
        fn GetConsoleScreenBufferInfo(
            hConsoleOutput: usize,
            lpConsoleScreenBufferInfo: *mut CONSOLE_SCREEN_BUFFER_INFO,
        ) -> i32;
    }

    /// Saved original console input mode.
    static SAVED_IN_MODE: AtomicU32 = AtomicU32::new(0);
    /// Saved original console output mode.
    static SAVED_OUT_MODE: AtomicU32 = AtomicU32::new(0);

    /// Enable raw (character-at-a-time) console input and VT output processing.
    pub fn enable_raw_mode() -> io::Result<()> {
        let stdin_h = unsafe { GetStdHandle(STD_INPUT_HANDLE) };
        let stdout_h = unsafe { GetStdHandle(STD_OUTPUT_HANDLE) };

        let mut in_mode: u32 = 0;
        let mut out_mode: u32 = 0;
        if unsafe { GetConsoleMode(stdin_h, &mut in_mode) } == 0 {
            return Err(io::Error::last_os_error());
        }
        if unsafe { GetConsoleMode(stdout_h, &mut out_mode) } == 0 {
            return Err(io::Error::last_os_error());
        }

        SAVED_IN_MODE.store(in_mode, Ordering::Relaxed);
        SAVED_OUT_MODE.store(out_mode, Ordering::Relaxed);

        let new_in = (in_mode & !(ENABLE_LINE_INPUT | ENABLE_ECHO_INPUT))
            | ENABLE_VIRTUAL_TERMINAL_INPUT;
        let new_out = out_mode | ENABLE_VIRTUAL_TERMINAL_PROCESSING;

        if unsafe { SetConsoleMode(stdin_h, new_in) } == 0 {
            return Err(io::Error::last_os_error());
        }
        if unsafe { SetConsoleMode(stdout_h, new_out) } == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    /// Restore console modes saved by the last [`enable_raw_mode`] call.
    pub fn disable_raw_mode() -> io::Result<()> {
        let stdin_h = unsafe { GetStdHandle(STD_INPUT_HANDLE) };
        let stdout_h = unsafe { GetStdHandle(STD_OUTPUT_HANDLE) };

        let in_mode = SAVED_IN_MODE.load(Ordering::Relaxed);
        let out_mode = SAVED_OUT_MODE.load(Ordering::Relaxed);

        if in_mode != 0 && unsafe { SetConsoleMode(stdin_h, in_mode) } == 0 {
            return Err(io::Error::last_os_error());
        }
        if out_mode != 0 && unsafe { SetConsoleMode(stdout_h, out_mode) } == 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(())
    }

    /// Query the current cursor position via `GetConsoleScreenBufferInfo`.
    ///
    /// Returns `(col, row)` both 0-indexed.
    pub fn cursor_position() -> io::Result<(u16, u16)> {
        let h = unsafe { GetStdHandle(STD_OUTPUT_HANDLE) };
        let mut info = core::mem::MaybeUninit::<CONSOLE_SCREEN_BUFFER_INFO>::uninit();
        if unsafe { GetConsoleScreenBufferInfo(h, info.as_mut_ptr()) } == 0 {
            return Err(io::Error::last_os_error());
        }
        let info = unsafe { info.assume_init() };
        Ok((
            info.dw_cursor_position.x as u16,
            info.dw_cursor_position.y as u16,
        ))
    }

    /// Pinned state for one in-flight console `ReadFile` operation.
    ///
    /// Ownership invariant: while the read wait is registered, only the
    /// thread accesses `buffer` and `bytes_read`. The thread runs and executes
    /// exactly once; after it posts to IOCP and returns, the future safely
    /// reclaims sole ownership.
    struct ConsoleReadState {
        /// Buffer owned exclusively by this struct while the read is in-flight.
        buffer: core::cell::UnsafeCell<Vec<u8>>,
        /// Number of bytes written by `ReadFile` in the thread.
        bytes_read: AtomicU32,
        /// Win32 thread handle returned by `CreateThread`.
        thread_handle: AtomicUsize,
        /// Unique token: address of this struct cast to `u64`.
        token: u64,
        /// The console handle being read.
        console: usize,
    }

    // SAFETY: `ConsoleReadState` is pinned for its lifetime and the buffer is
    // only accessed from one context at a time (thread XOR future).
    unsafe impl Send for ConsoleReadState {}
    unsafe impl Sync for ConsoleReadState {}

    /// Thread invoked by `CreateThread` when the console handle
    /// needs reading. Performs the `ReadFile` directly into the owned
    /// buffer, then signals the APC bridge so the executor can wake the future.
    unsafe extern "system" fn console_read_thread(context: *mut core::ffi::c_void) -> u32 {
        // SAFETY: `context` was set to a pinned `ConsoleReadState` by
        // `ConsoleReadFuture::poll`. The state is kept alive by the future
        // (or its Drop impl) until `WaitForSingleObject` returns, which happens
        // after this thread completes.
        let state = unsafe { &*(context as *const ConsoleReadState) };
        let buf: &mut Vec<u8> = unsafe { &mut *state.buffer.get() };
        let mut n: u32 = 0;
        // SAFETY: `buf` is valid for `capacity()` bytes; `ReadFile` writes up
        // to that many bytes. No OVERLAPPED needed for console handles.
        unsafe {
            ReadFile(
                state.console,
                buf.as_mut_ptr(),
                buf.capacity() as u32,
                &mut n,
                ptr::null_mut(),
            )
        };
        // Store byte count with Release so the future's Acquire load sees it.
        state.bytes_read.store(n, Ordering::Release);

        // Signal the main thread via APC bridge
        crate::rt::windows::queue_wake(state.token);
        0
    }

    /// Future that reads from a Windows console handle.
    pub struct ConsoleReadFuture {
        state: Option<Pin<Box<ConsoleReadState>>>,
        registered: bool,
    }

    impl ConsoleReadFuture {
        fn new(console: usize, buf: Vec<u8>) -> Self {
            // Box+pin the state first so we have a stable heap address, then
            // derive the token from that address.
            let boxed = Box::new(ConsoleReadState {
                buffer: core::cell::UnsafeCell::new(buf),
                bytes_read: AtomicU32::new(0),
                thread_handle: AtomicUsize::new(0),
                token: 0,
                console,
            });
            let token = boxed.as_ref() as *const ConsoleReadState as usize as u64;
            let mut pinned = Box::into_pin(boxed);
            // SAFETY: We set `token` before sharing the struct with any
            // callback.  `get_unchecked_mut` is sound here because we hold the
            // only reference and haven't shared the address yet.
            unsafe {
                pinned.as_mut().get_unchecked_mut().token = token;
            }
            Self {
                state: Some(pinned),
                registered: false,
            }
        }
    }

    impl Future for ConsoleReadFuture {
        type Output = (io::Result<usize>, Vec<u8>);

        fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Self::Output> {
            let token = self.state.as_ref().map_or(0, |s| s.token);

            if consume_ready(token) {
                if let Some(mut state) = self.state.take() {
                    // Reclaim I/O count
                    crate::rt::windows::outstanding_io().set(crate::rt::windows::outstanding_io().get() - 1);

                    let n = state.bytes_read.load(Ordering::Acquire) as usize;
                    // SAFETY: the thread has finished (WaitForSingleObject in
                    // Drop blocks until it does); we regain sole ownership.
                    let buf_ref: &mut Vec<u8> =
                        unsafe { &mut *state.as_mut().get_unchecked_mut().buffer.get() };
                    let mut buf = core::mem::replace(buf_ref, Vec::new());

                    let th = state.thread_handle.load(Ordering::Relaxed);
                    if th != 0 {
                        unsafe { CloseHandle(th) };
                    }

                    // SAFETY: `ReadFile` wrote `n` valid bytes.
                    unsafe { buf.set_len(n) };
                    return Poll::Ready((Ok(n), buf));
                }
                return Poll::Ready((
                    Err(io::Error::other("ConsoleReadFuture: state missing")),
                    Vec::new(),
                ));
            }

            // Store waker so the executor can wake us on APC bridge completion.
            let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));

            if !self.registered {
                // SAFETY: `state` is pinned for the lifetime of the future.
                // The thread will fire exactly once and then stop
                // referencing `state`; Drop waits before dropping state.
                let self_ptr = if let Some(ref state) = self.state {
                    state.as_ref().get_ref() as *const ConsoleReadState as *mut core::ffi::c_void
                } else {
                    return Poll::Ready((
                        Err(io::Error::other("ConsoleReadFuture: state missing")),
                        Vec::new(),
                    ));
                };

                let mut thread_id: u32 = 0;
                let thread_handle = unsafe {
                    CreateThread(
                        ptr::null_mut(),
                        0,
                        Some(console_read_thread),
                        self_ptr,
                        0,
                        &mut thread_id,
                    )
                };
                if thread_handle == 0 {
                    let err = io::Error::last_os_error();
                    let buf = if let Some(mut state) = self.state.take() {
                        // SAFETY: no thread registered, sole owner.
                        let buf_ref: &mut Vec<u8> =
                            unsafe { &mut *state.as_mut().get_unchecked_mut().buffer.get() };
                        core::mem::replace(buf_ref, Vec::new())
                    } else {
                        Vec::new()
                    };
                    return Poll::Ready((Err(err), buf));
                }

                if let Some(ref state) = self.state {
                    state.thread_handle.store(thread_handle, Ordering::Relaxed);
                }

                // Track live I/O
                crate::rt::windows::outstanding_io().set(crate::rt::windows::outstanding_io().get() + 1);
                self.registered = true;
            }

            Poll::Pending
        }
    }

    impl Drop for ConsoleReadFuture {
        fn drop(&mut self) {
            if self.registered {
                let th = self
                    .state
                    .as_ref()
                    .map_or(0, |s| s.thread_handle.load(Ordering::Relaxed));
                if th != 0 {
                    // Cancel the thread's blocked ReadFile.
                    // This unblocks the thread so it can exit on its own.
                    unsafe { CancelSynchronousIo(th) };

                    // Windows CRT attempts to lock `stdin` streams during standard shutdown.
                    // Since CancelSynchronousIo guarantees the blocking call will abort,
                    // we can safely wait for the thread to exit cleanly.
                    // We use a 0 timeout here because CancelSynchronousIo is unreliable
                    // for console handles, and we must not hang the process.
                    unsafe { WaitForSingleObject(th, 0) };

                    unsafe { CloseHandle(th) };
                    crate::rt::windows::outstanding_io().set(crate::rt::windows::outstanding_io().get() - 1);
                }
            }
        }
    }

    // ── AsyncRead / AsyncWrite ────────────────────────────────────────────────

    impl crate::io::IsTerminal for Tty {
        fn is_terminal(&self) -> bool {
            // FILE_TYPE_CHAR means a character device (console or serial port).
            let ft = unsafe { GetFileType(self.handle) };
            ft == FILE_TYPE_CHAR
        }
    }

    impl crate::io::AsyncRead for Tty {
        async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            // Check if it is a character file (console/tty).
            if unsafe { GetFileType(self.handle) } == FILE_TYPE_CHAR {
                ConsoleReadFuture::new(self.handle, buf).await
            } else {
                crate::rt::windows::OverlappedRead::new(self.handle as u64, buf).await
            }
        }
    }

    impl crate::io::Read for Tty {
        fn read(&mut self, _buf: &mut [u8]) -> io::Result<usize> {
            Err(io::Error::other("sync read not implemented for Windows Tty"))
        }
    }

    impl crate::io::AsyncWrite for Tty {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            let mut n: u32 = 0;
            // SAFETY: `buf` is valid for `buf.len()` bytes; console `WriteFile`
            // is synchronous so no OVERLAPPED is needed.
            let ret = unsafe {
                WriteFile(
                    self.handle,
                    buf.as_ptr(),
                    buf.len() as u32,
                    &mut n,
                    ptr::null_mut(),
                )
            };
            if ret == 0 {
                (Err(io::Error::last_os_error()), buf)
            } else {
                (Ok(n as usize), buf)
            }
        }
        async fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    impl crate::io::Write for Tty {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            let mut n: u32 = 0;
            let ret = unsafe {
                WriteFile(
                    self.handle,
                    buf.as_ptr(),
                    buf.len() as u32,
                    &mut n,
                    ptr::null_mut(),
                )
            };
            if ret == 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    // ─── Tests ───────────────────────────────────────────────────────────────

    #[cfg(test)]
    mod tests {
        extern crate std;

        use super::*;
        use crate::io::AsyncWrite;
        use crate::traits::AsyncRead;
        use crate::vec::Vec;

        // ── Stubs ────────────────────────────────────────────────────────────

        #[test]
        fn get_size_stub() {
            assert_eq!(get_size(0).unwrap(), (80, 24));
            assert_eq!(get_size(u64::MAX).unwrap(), (80, 24));
        }

        #[test]
        fn set_mode_stub() {
            assert!(set_mode(0, 0).is_ok());
            assert!(set_mode(1, 0xFFFF_FFFF).is_ok());
        }

        // ── Handle validity ──────────────────────────────────────────────────

        #[test]
        fn stdin_handle_is_valid() {
            let t = stdin();
            // GetStdHandle returns NULL (0) on failure; INVALID_HANDLE_VALUE
            // (usize::MAX) is also an error sentinel.
            assert_ne!(t.handle, 0, "stdin handle should not be NULL");
            assert_ne!(
                t.handle,
                usize::MAX,
                "stdin handle should not be INVALID_HANDLE_VALUE"
            );
        }

        #[test]
        fn stdout_handle_is_valid() {
            let t = stdout();
            assert_ne!(t.handle, 0, "stdout handle should not be NULL");
            assert_ne!(
                t.handle,
                usize::MAX,
                "stdout handle should not be INVALID_HANDLE_VALUE"
            );
        }

        // ── ConsoleReadFuture internal state ─────────────────────────────────

        #[test]
        fn console_read_state_token_equals_ptr() {
            // The token stored inside ConsoleReadState must equal the stable
            // heap address of that state (used as the IOCP completion key).
            let buf = Vec::with_capacity(8);
            // Use an arbitrary handle value — we are not polling, so no I/O occurs.
            let f = ConsoleReadFuture::new(stdout().handle, buf);
            if let Some(ref state) = f.state {
                let expected_token =
                    state.as_ref().get_ref() as *const ConsoleReadState as usize as u64;
                assert_eq!(
                    state.token, expected_token,
                    "token must equal the address of the pinned ConsoleReadState"
                );
            } else {
                panic!("ConsoleReadFuture::new should initialise state");
            }
            // Drop: `registered == false`, so Drop is a no-op — no crash.
        }

        // ── Drop safety ──────────────────────────────────────────────────────

        #[test]
        fn drop_console_read_future_before_poll() {
            // Creating and immediately dropping must not panic or leak.
            let buf = Vec::with_capacity(16);
            drop(ConsoleReadFuture::new(stdout().handle, buf));
        }

        // ── Write path (synchronous WriteFile, tested with a real file handle) ──

        #[test]
        fn write_to_real_file_handle() {
            crate::rt::run(async {
                let path = "rusticated_tty_write_test.bin";
                let file = crate::fs::OpenOptions::new()
                    .write(true)
                    .create(true)
                    .truncate(true)
                    .open(path)
                    .await
                    .expect("create test file");
                let raw: usize = file.as_raw_fd() as usize;
                // Wrap the raw handle in our Tty (private field, accessible here).
                let mut tty = Tty { handle: raw };

                let data = b"rusticated tty write test".to_vec();
                let expected_len = data.len();
                let (res, _returned_buf) = tty.write(data).await;
                assert_eq!(
                    res.unwrap(),
                    expected_len,
                    "WriteFile should report all bytes written"
                );
                // Flush by closing the file before reading back.
                drop(file);

                let mut read_file = crate::fs::OpenOptions::new()
                    .read(true)
                    .open(path)
                    .await
                    .expect("open read file");
                let mut buf = Vec::new();
                for _ in 0..128 {
                    buf.push(0);
                }
                let (res, on_disk) = AsyncRead::read(&mut read_file, buf).await;
                let n = res.unwrap();
                assert_eq!(&on_disk[..n], b"rusticated tty write test");
                drop(read_file);

                unsafe {
                    unsafe extern "system" {
                        fn DeleteFileW(lpFileName: *const u16) -> i32;
                    }
                    let mut path_u16: Vec<u16> = path.encode_utf16().collect();
                    path_u16.push(0);
                    DeleteFileW(path_u16.as_ptr());
                }
            });
        }

        // ── Error paths ──────────────────────────────────────────────────────

        #[test]
        fn write_to_invalid_handle_returns_err() {
            crate::rt::run(async {
                // usize::MAX == INVALID_HANDLE_VALUE on Windows — WriteFile always
                // rejects it with ERROR_INVALID_HANDLE.  Using this sentinel avoids
                // the manual-close + handle-recycling race that occurs when tests
                // run in parallel and a freshly closed handle integer is reused by
                // another concurrent open.
                let mut tty = Tty { handle: usize::MAX };
                let data = b"should fail".to_vec();
                let (res, returned_buf) = tty.write(data).await;
                assert!(
                    res.is_err(),
                    "WriteFile on INVALID_HANDLE_VALUE must return Err"
                );
                // Buffer must always be returned to the caller regardless of error.
                assert_eq!(returned_buf, b"should fail");
            });
        }

        #[test]
        fn console_read_future_null_handle_returns_err() {
            crate::rt::run(async {
                // NULL (0) is rejected by ReadFile inside the dedicated thread,
                // or fails thread creation entirely. Tests the res==0 error branch
                // inside ConsoleReadFuture::poll and verifies the buffer is returned.
                let buf = Vec::with_capacity(8);
                let (res, returned_buf) = ConsoleReadFuture::new(0, buf).await;
                assert!(
                    res.is_err(),
                    "NULL handle must cause ConsoleReadFuture to fail"
                );
                assert_eq!(
                    returned_buf.capacity(),
                    8,
                    "buffer must be returned with original capacity"
                );
            });
        }
    }
}

// ─── WASM ────────────────────────────────────────────────────────────────────

#[cfg(target_family = "wasm")]
mod wasm_tty {
    #![allow(
        clippy::cast_possible_truncation,
        clippy::unnecessary_wraps,
        clippy::cast_possible_wrap,
        clippy::cast_sign_loss,
        clippy::undocumented_unsafe_blocks
    )]

    use crate::abi::imports;
    use crate::io;
    use crate::rt::OverlappedBufferFuture;
    use crate::vec::Vec;

    /// Async handle to a WASM host TTY.
    pub struct Tty {
        handle: u64,
    }

    /// Return an async handle to the host's standard input (handle `0`).
    pub fn stdin() -> Tty {
        Tty { handle: 0 }
    }

    /// Return an async handle to the host's standard output (handle `1`).
    pub fn stdout() -> Tty {
        Tty { handle: 1 }
    }

    /// Query the terminal size for `handle` via the WASM host.
    pub fn get_size(handle: u64) -> io::Result<(u16, u16)> {
        // SAFETY: `tty_get_size` is a side-effect-free host import.
        let res = unsafe { imports::tty_get_size(handle) };
        let cols = (res >> 16) as u16;
        let rows = (res & 0xFFFF) as u16;
        Ok((cols, rows))
    }

    /// Set the terminal mode for `handle` via the WASM host.
    pub fn set_mode(handle: u64, mode: u32) -> io::Result<()> {
        // SAFETY: `tty_set_mode` is a host import with no preconditions.
        unsafe { imports::tty_set_mode(handle, mode) };
        Ok(())
    }

    /// Enable raw mode via the WASM host (mode flag 1).
    pub fn enable_raw_mode() -> io::Result<()> {
        set_mode(0, 1)
    }

    /// Disable raw mode via the WASM host (mode flag 0 = restore).
    pub fn disable_raw_mode() -> io::Result<()> {
        set_mode(0, 0)
    }

    /// Cursor position is tracked by the WASM host; returns `(0, 0)` here.
    pub fn cursor_position() -> io::Result<(u16, u16)> {
        Ok((0, 0))
    }

    impl crate::io::IsTerminal for Tty {
        fn is_terminal(&self) -> bool {
            // On WASM, assume handles 0 and 1 (stdin/stdout) are always TTYs.
            // The host may override this assumption in future via a host import.
            self.handle <= 1
        }
    }

    impl crate::io::AsyncRead for Tty {
        async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            let handle = self.handle;
            let (err, bytes_read, _, mut buf) =
                OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                    // SAFETY: `ptr`/`len` describe the buffer owned by the
                    // future's state, kept alive by an `Rc` clone in the
                    // completion registry until the host signals completion.
                    unsafe { imports::read(ov, handle, ptr, len) };
                })
                .await;

            if err != 0 {
                return (Err(io::Error::from_raw_os_error(err as i32)), buf);
            }
            // SAFETY: The WASM host wrote `bytes_read` valid bytes.
            unsafe { buf.set_len(bytes_read as usize) };
            (Ok(bytes_read as usize), buf)
        }
    }

    impl crate::io::Read for Tty {
        fn read(&mut self, _buf: &mut [u8]) -> io::Result<usize> {
            Err(io::Error::other("sync read not implemented for WASM Tty"))
        }
    }

    impl crate::io::AsyncWrite for Tty {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            let handle = self.handle;
            let used = buf.len() as u32;
            let (err, bytes_written, _, buf) =
                OverlappedBufferFuture::new(buf, move |ov, ptr, _cap| {
                    // SAFETY: `ptr` points into the future-owned buffer.
                    unsafe { imports::write(ov, handle, ptr, used) };
                })
                .await;

            if err != 0 {
                return (Err(io::Error::from_raw_os_error(err as i32)), buf);
            }
            (Ok(bytes_written as usize), buf)
        }
        async fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    impl crate::io::Write for Tty {
        fn write(&mut self, _buf: &[u8]) -> io::Result<usize> {
            Err(io::Error::other("sync write not implemented for WASM Tty"))
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }
}

// ─── Shared tests ─────────────────────────────────────────────────────────────

#[cfg(all(test, not(target_family = "wasm")))]
mod shared_tests {
    /// `set_mode` is a const stub that always returns `Ok(())` on every native
    /// platform.  This test confirms the public re-export dispatches correctly.
    #[test]
    fn set_mode_always_returns_ok() {
        assert!(crate::tty::set_mode(0, 0).is_ok());
        assert!(crate::tty::set_mode(1, 0xFFFF_FFFF).is_ok());
    }
}
