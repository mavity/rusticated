//! TTY management — provides [`Tty`], [`stdin`], [`stdout`], [`get_size`],
//! and [`set_mode`] across all platforms.

#[cfg(unix)]
pub use unix_tty::{Tty, get_size, set_mode, stdin, stdout};

#[cfg(windows)]
pub use windows_tty::{Tty, get_size, set_mode, stdin, stdout};

#[cfg(target_family = "wasm")]
pub use wasm_tty::{Tty, get_size, set_mode, stdin, stdout};

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

    unsafe extern "system" {
        fn GetStdHandle(n_std_handle: u32) -> usize;
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
            overlapped: *mut Overlapped,
        ) -> i32;
        fn RegisterWaitForSingleObject(
            new_wait_object: *mut usize,
            object: usize,
            callback: Option<unsafe extern "system" fn(*mut core::ffi::c_void, u8)>,
            context: *mut core::ffi::c_void,
            milliseconds: u32,
            flags: u32,
        ) -> i32;
        fn UnregisterWaitEx(wait_handle: usize, completion_event: usize) -> i32;
        fn PostQueuedCompletionStatus(
            comp_port: usize,
            number_of_bytes: u32,
            comp_key: usize,
            overlapped: *mut Overlapped,
        ) -> i32;
    }

    const STD_INPUT_HANDLE: u32 = 0xFFFF_FFF6;
    const STD_OUTPUT_HANDLE: u32 = 0xFFFF_FFF5;
    const WT_EXECUTEONLYONCE: u32 = 0x0000_0008;
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

    // ── ConsoleReadState ─────────────────────────────────────────────────────

    /// Pinned state for one in-flight console `ReadFile` operation.
    ///
    /// Ownership invariant: while the read wait is registered, only the
    /// callback accesses `buffer` and `bytes_read`.  The callback runs on a
    /// system thread-pool thread but executes exactly once; after it posts to
    /// IOCP and returns, the future safely reclaims sole ownership.
    struct ConsoleReadState {
        /// Buffer owned exclusively by this struct while the read is in-flight.
        buffer: core::cell::UnsafeCell<Vec<u8>>,
        /// Number of bytes written by `ReadFile` in the callback.
        bytes_read: AtomicU32,
        /// Win32 wait handle returned by `RegisterWaitForSingleObject`.
        wait_handle: AtomicUsize,
        /// IOCP port used to post completion notification.
        iocp: usize,
        /// Unique token: address of this struct cast to `u64`.
        token: u64,
        /// The console handle being read.
        console: usize,
    }

    // SAFETY: `ConsoleReadState` is pinned for its lifetime and the buffer is
    // only accessed from one context at a time (callback XOR future).
    unsafe impl Send for ConsoleReadState {}
    unsafe impl Sync for ConsoleReadState {}

    /// Callback invoked by the Windows thread pool when the console handle
    /// becomes readable.  Performs the `ReadFile` directly into the owned
    /// buffer, then signals the IOCP so the executor can wake the future.
    unsafe extern "system" fn console_read_callback(
        context: *mut core::ffi::c_void,
        _timer_fired: u8,
    ) {
        // SAFETY: `context` was set to a pinned `ConsoleReadState` by
        // `ConsoleReadFuture::poll`.  The state is kept alive by the future
        // (or its Drop impl) until `UnregisterWaitEx` returns, which happens
        // after this callback completes thanks to the blocking wait flag.
        let state = unsafe { &*(context as *const ConsoleReadState) };
        let buf: &mut Vec<u8> = unsafe { &mut *state.buffer.get() };
        let mut n: u32 = 0;
        // SAFETY: `buf` is valid for `capacity()` bytes; `ReadFile` writes up
        // to that many bytes.  No OVERLAPPED needed for console handles.
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
        // SAFETY: `state.iocp` is a valid IOCP handle; `state.token` is the
        // unique address of the pinned state.
        unsafe { PostQueuedCompletionStatus(state.iocp, 0, state.token as usize, ptr::null_mut()) };
    }

    /// Future that reads from a Windows console handle.
    pub struct ConsoleReadFuture {
        state: Option<Pin<Box<ConsoleReadState>>>,
        registered: bool,
    }

    impl ConsoleReadFuture {
        fn new(console: usize, buf: Vec<u8>) -> Self {
            let iocp = with_driver(|d| d.iocp).unwrap_or(0);
            // Box+pin the state first so we have a stable heap address, then
            // derive the token from that address.
            let boxed = Box::new(ConsoleReadState {
                buffer: core::cell::UnsafeCell::new(buf),
                bytes_read: AtomicU32::new(0),
                wait_handle: AtomicUsize::new(0),
                iocp,
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
                    let n = state.bytes_read.load(Ordering::Acquire) as usize;
                    // SAFETY: the callback has finished (UnregisterWaitEx in
                    // Drop blocks until it does); we regain sole ownership.
                    let buf_ref: &mut Vec<u8> =
                        unsafe { &mut *state.as_mut().get_unchecked_mut().buffer.get() };
                    let mut buf = core::mem::replace(buf_ref, Vec::new());
                    // SAFETY: `ReadFile` wrote `n` valid bytes.
                    unsafe { buf.set_len(n) };
                    return Poll::Ready((Ok(n), buf));
                }
                return Poll::Ready((
                    Err(io::Error::other("ConsoleReadFuture: state missing")),
                    Vec::new(),
                ));
            }

            // Store waker so the executor can wake us on IOCP completion.
            let _ = with_driver(|d| d.register_waker(token, cx.waker().clone()));

            if !self.registered {
                // SAFETY: `state` is pinned for the lifetime of the future.
                // The callback will fire exactly once and then stop
                // referencing `state`; Drop unregisters before dropping state.
                let (self_ptr, console) = if let Some(ref state) = self.state {
                    (
                        state.as_ref().get_ref() as *const ConsoleReadState
                            as *mut core::ffi::c_void,
                        state.console,
                    )
                } else {
                    return Poll::Ready((
                        Err(io::Error::other("ConsoleReadFuture: state missing")),
                        Vec::new(),
                    ));
                };

                let mut wait_handle: usize = 0;
                let res = unsafe {
                    RegisterWaitForSingleObject(
                        &mut wait_handle,
                        console,
                        Some(console_read_callback),
                        self_ptr,
                        INFINITE,
                        WT_EXECUTEONLYONCE,
                    )
                };
                if res == 0 {
                    let err = io::Error::last_os_error();
                    let buf = if let Some(mut state) = self.state.take() {
                        // SAFETY: no callback registered, sole owner.
                        let buf_ref: &mut Vec<u8> =
                            unsafe { &mut *state.as_mut().get_unchecked_mut().buffer.get() };
                        core::mem::replace(buf_ref, Vec::new())
                    } else {
                        Vec::new()
                    };
                    return Poll::Ready((Err(err), buf));
                }

                if let Some(ref state) = self.state {
                    state.wait_handle.store(wait_handle, Ordering::Relaxed);
                }
                self.registered = true;
            }

            Poll::Pending
        }
    }

    impl Drop for ConsoleReadFuture {
        fn drop(&mut self) {
            if self.registered {
                if let Some(ref state) = self.state {
                    let wh = state.wait_handle.load(Ordering::Relaxed);
                    if wh != 0 {
                        // !0 = INVALID_HANDLE_VALUE — blocks until callback completes.
                        unsafe { UnregisterWaitEx(wh, !0) };
                    }
                }
            }
        }
    }

    // ── AsyncRead / AsyncWrite ────────────────────────────────────────────────

    impl crate::io::AsyncRead for Tty {
        async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            ConsoleReadFuture::new(self.handle, buf).await
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
    }

    // ─── Tests ───────────────────────────────────────────────────────────────

    #[cfg(test)]
    mod tests {
        use super::*;
        use crate::io::AsyncWrite;
        use crate::vec::Vec;
        use std::os::windows::io::AsRawHandle;

        // Drive the single-threaded executor to completion, sleeping when idle.
        // Identical in structure to the helper in `fs.rs` tests.
        fn block_on<F: std::future::Future<Output = ()> + 'static>(f: F) {
            crate::rt::executor::run(f);
            loop {
                match crate::rt::executor::poll_step().unwrap() {
                    crate::rt::executor::PollStatus::Done => break,
                    crate::rt::executor::PollStatus::Ready => continue,
                    crate::rt::executor::PollStatus::Idle { next_deadline } => {
                        if let Some(d) = next_deadline {
                            std::thread::sleep(d);
                        } else {
                            std::thread::sleep(std::time::Duration::from_millis(5));
                        }
                    }
                }
            }
        }

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
            assert_ne!(t.handle, usize::MAX, "stdin handle should not be INVALID_HANDLE_VALUE");
        }

        #[test]
        fn stdout_handle_is_valid() {
            let t = stdout();
            assert_ne!(t.handle, 0, "stdout handle should not be NULL");
            assert_ne!(t.handle, usize::MAX, "stdout handle should not be INVALID_HANDLE_VALUE");
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
            block_on(async {
                let path = std::env::temp_dir().join("fast_std_tty_write_test.bin");
                // Open with std so we get a proper Windows HANDLE via AsRawHandle.
                let file = std::fs::OpenOptions::new()
                    .write(true)
                    .create(true)
                    .truncate(true)
                    .open(&path)
                    .expect("create temp file");
                let raw: usize = file.as_raw_handle() as usize;
                // Wrap the raw handle in our Tty (private field, accessible here).
                let mut tty = Tty { handle: raw };

                let data = b"fast-std tty write test".to_vec();
                let expected_len = data.len();
                let (res, _returned_buf) = tty.write(data).await;
                assert_eq!(
                    res.unwrap(),
                    expected_len,
                    "WriteFile should report all bytes written"
                );
                // Flush by closing the std::fs::File before reading back.
                drop(file);

                let on_disk = std::fs::read(&path).expect("read back temp file");
                assert_eq!(on_disk, b"fast-std tty write test");
                let _ = std::fs::remove_file(&path);
            });
        }

        // ── Error paths ──────────────────────────────────────────────────────

        #[test]
        fn write_to_invalid_handle_returns_err() {
            block_on(async {
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
            block_on(async {
                // NULL (0) is rejected by RegisterWaitForSingleObject before
                // any callback is ever registered — tests the res==0 error branch
                // inside ConsoleReadFuture::poll and verifies the buffer is returned.
                let buf = Vec::with_capacity(8);
                let (res, returned_buf) = ConsoleReadFuture::new(0, buf).await;
                assert!(res.is_err(), "NULL handle must cause RegisterWaitForSingleObject to fail");
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
