//! I/O abstractions — owned-buffer async traits, and error types.

use crate::vec::Vec;

// ─── Error types ─────────────────────────────────────────────────────────────

/// Category of an I/O error.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[non_exhaustive]
pub enum ErrorKind {
    /// Unexpected end of file.
    UnexpectedEof,
    /// Invalid input (e.g. a null byte in a path).
    InvalidInput,
    /// Entity not found.
    NotFound,
    /// Permission denied.
    PermissionDenied,
    /// Entity already exists.
    AlreadyExists,
    /// Operation interrupted by a signal.
    Interrupted,
    /// The pipe being written to was closed.
    BrokenPipe,
    /// Invalid data (e.g. non-UTF8 when expected).
    InvalidData,
    /// A writer could not accept any more data.
    WriteZero,
    /// Is a directory.
    IsADirectory,
    /// Operation would block.
    WouldBlock,
    /// Other, unclassified error.
    Other,
}

/// An I/O error backed by an OS error code or a static message.
#[derive(Debug)]
pub struct Error {
    kind: ErrorKind,
    /// Raw OS error code; 0 means this is a synthesised error.
    code: i32,
    msg: alloc::borrow::Cow<'static, str>,
}

/// `core::result::Result<T, crate::io::Error>`.
pub type Result<T> = core::result::Result<T, Error>;

pub use crate::traits::{AsyncRead, AsyncWrite, BufRead, Read, Seek, Write};

/// Enumeration of possible methods to seek within an I/O object.
#[derive(Copy, Clone, Debug, PartialEq, Eq)]
pub enum SeekFrom {
    /// Seek to an absolute byte offset.
    Start(u64),
    /// Seek to an offset relative to the end of the object.
    End(i64),
    /// Seek to an offset relative to the current position.
    Current(i64),
}

/// Standard input stream.
pub struct Stdin;
/// Standard output stream.
pub struct Stdout;
/// Standard error stream.
pub struct Stderr;

/// Handle to the standard input of the process.
pub fn stdin() -> Stdin {
    Stdin
}
/// Handle to the standard output of the process.
pub fn stdout() -> Stdout {
    Stdout
}
/// Handle to the standard error of the process.
pub fn stderr() -> Stderr {
    Stderr
}

impl AsyncRead for Stdin {
    async fn read(&mut self, _buf: Vec<u8>) -> (Result<usize>, Vec<u8>) {
        (Err(Error::other("stdin read not implemented")), _buf)
    }
}

impl Read for Stdin {
    fn read(&mut self, _buf: &mut [u8]) -> Result<usize> {
        Err(Error::other("stdin read not implemented"))
    }
}

impl AsyncWrite for Stdout {
    async fn write(&mut self, _buf: Vec<u8>) -> (Result<usize>, Vec<u8>) {
        (Err(Error::other("stdout write not implemented")), _buf)
    }
    async fn flush(&mut self) -> Result<()> {
        Ok(())
    }
}

impl Write for Stdout {
    fn write(&mut self, _buf: &[u8]) -> Result<usize> {
        #[cfg(windows)]
        {
            #[allow(clashing_extern_declarations)]
            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn GetStdHandle(n_std_handle: u32) -> usize;
                fn WriteFile(
                    h_file: usize,
                    lp_buffer: *const u8,
                    n_number_of_bytes_to_write: u32,
                    lp_number_of_bytes_written: *mut u32,
                    lp_overlapped: *mut core::ffi::c_void,
                ) -> i32;
            }
            let handle = unsafe { GetStdHandle(0xFFFFFFF5) }; // STD_OUTPUT_HANDLE
            let mut written = 0;
            let res = unsafe {
                WriteFile(
                    handle,
                    _buf.as_ptr(),
                    _buf.len() as u32,
                    &mut written,
                    core::ptr::null_mut(),
                )
            };
            if res == 0 {
                Err(Error::from(ErrorKind::Other))
            } else {
                Ok(written as usize)
            }
        }
        #[cfg(not(windows))]
        Err(Error::other("stdout write not implemented"))
    }
    fn flush(&mut self) -> Result<()> {
        Ok(())
    }
}

impl AsyncWrite for Stderr {
    async fn write(&mut self, _buf: Vec<u8>) -> (Result<usize>, Vec<u8>) {
        (Err(Error::other("stderr write not implemented")), _buf)
    }
    async fn flush(&mut self) -> Result<()> {
        Ok(())
    }
}

impl Write for Stderr {
    fn write(&mut self, _buf: &[u8]) -> Result<usize> {
        #[cfg(windows)]
        {
            #[allow(clashing_extern_declarations)]
            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn GetStdHandle(n_std_handle: u32) -> usize;
                fn WriteFile(
                    h_file: usize,
                    lp_buffer: *const u8,
                    n_number_of_bytes_to_write: u32,
                    lp_number_of_bytes_written: *mut u32,
                    lp_overlapped: *mut core::ffi::c_void,
                ) -> i32;
            }
            let handle = unsafe { GetStdHandle(0xFFFFFFF4) }; // STD_ERROR_HANDLE
            let mut written = 0;
            let res = unsafe {
                WriteFile(
                    handle,
                    _buf.as_ptr(),
                    _buf.len() as u32,
                    &mut written,
                    core::ptr::null_mut(),
                )
            };
            if res == 0 {
                Err(Error::from(ErrorKind::Other))
            } else {
                Ok(written as usize)
            }
        }
        #[cfg(not(windows))]
        Err(Error::other("stderr write not implemented"))
    }
    fn flush(&mut self) -> Result<()> {
        Ok(())
    }
}

impl IsTerminal for Stdin {
    fn is_terminal(&self) -> bool {
        false
    }
}
impl IsTerminal for Stdout {
    fn is_terminal(&self) -> bool {
        false
    }
}
impl IsTerminal for Stderr {
    fn is_terminal(&self) -> bool {
        false
    }
}

/// Reader end of a redirectable pipe.
pub struct PipeReader;

impl PipeReader {
    /// Clones the pipe reader.
    pub fn try_clone(&self) -> Result<Self> {
        Ok(Self)
    }
}

/// Writer end of a redirectable pipe.
pub struct PipeWriter;

impl PipeWriter {
    /// Clones the pipe writer.
    pub fn try_clone(&self) -> Result<Self> {
        Ok(Self)
    }
}

/// Creates a new anonymous pipe.
pub fn pipe() -> Result<(PipeReader, PipeWriter)> {
    Ok((PipeReader, PipeWriter))
}

/// Copies the entire contents of a reader into a writer.
pub fn copy<R: crate::traits::Read + ?Sized, W: crate::traits::Write + ?Sized>(
    reader: &mut R,
    writer: &mut W,
) -> Result<u64> {
    let mut buf = [0u8; 8192];
    let mut total = 0;
    loop {
        match reader.read(&mut buf) {
            Ok(0) => break,
            Ok(n) => {
                writer.write_all(&buf[..n])?;
                total += n as u64;
            }
            Err(e) if e.kind() == ErrorKind::Interrupted => continue,
            Err(e) => return Err(e),
        }
    }
    Ok(total)
}

impl AsyncRead for PipeReader {
    async fn read(&mut self, _buf: Vec<u8>) -> (Result<usize>, Vec<u8>) {
        (Err(Error::other("pipereader read not implemented")), _buf)
    }
}

impl Read for PipeReader {
    fn read(&mut self, _buf: &mut [u8]) -> Result<usize> {
        Err(Error::other("pipereader read not implemented"))
    }
}

impl AsyncWrite for PipeWriter {
    async fn write(&mut self, _buf: Vec<u8>) -> (Result<usize>, Vec<u8>) {
        (Err(Error::other("pipewriter write not implemented")), _buf)
    }
    async fn flush(&mut self) -> Result<()> {
        Ok(())
    }
}

impl Write for PipeWriter {
    fn write(&mut self, _buf: &[u8]) -> Result<usize> {
        Err(Error::other("pipewriter write not implemented"))
    }
    fn flush(&mut self) -> Result<()> {
        Ok(())
    }
}

impl Error {
    /// Returns the last OS-level error (reads `errno` on Unix, `GetLastError` on Windows).
    #[inline]
    pub fn last_os_error() -> Self {
        Self {
            kind: ErrorKind::Other,
            code: last_error_code(),
            msg: alloc::borrow::Cow::Borrowed(""),
        }
    }

    /// Constructs an error from a raw OS error code.
    #[inline]
    pub fn from_raw_os_error(code: i32) -> Self {
        Self {
            kind: ErrorKind::Other,
            code,
            msg: alloc::borrow::Cow::Borrowed(""),
        }
    }

    /// Constructs a synthesised error from any displayable object.
    pub fn error<T: core::fmt::Display>(msg: T) -> Self {
        Self {
            kind: ErrorKind::Other,
            code: 0,
            msg: alloc::borrow::Cow::Owned(alloc::format!("{}", msg)),
        }
    }

    /// Alias for error (consistent with std::io::Error::other)
    pub fn other<T: core::fmt::Display>(msg: T) -> Self {
        Self::error(msg)
    }

    /// Constructs a synthesised error with the specified kind and message.
    #[inline]
    pub fn new(kind: ErrorKind, msg: &'static str) -> Self {
        Self {
            kind,
            code: 0,
            msg: alloc::borrow::Cow::Borrowed(msg),
        }
    }

    /// Returns the raw OS error code if this is an OS-level error.
    #[inline]
    pub fn raw_os_error(&self) -> Option<i32> {
        if self.code != 0 {
            Some(self.code)
        } else {
            None
        }
    }

    /// Returns the error kind.
    #[inline]
    pub fn kind(&self) -> ErrorKind {
        self.kind
    }
}

impl core::fmt::Display for Error {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        if self.code != 0 {
            write!(f, "os error {}", self.code)
        } else {
            f.write_str(&self.msg)
        }
    }
}

impl core::error::Error for Error {}

impl From<ErrorKind> for Error {
    fn from(kind: ErrorKind) -> Self {
        Self {
            kind,
            code: 0,
            msg: alloc::borrow::Cow::Borrowed(""),
        }
    }
}

// ─── Platform errno / GetLastError ───────────────────────────────────────────

#[cfg(all(target_os = "linux", not(target_family = "wasm")))]
fn last_error_code() -> i32 {
    unsafe extern "C" {
        fn __errno_location() -> *mut i32;
    }
    // SAFETY: `__errno_location` returns a valid thread-local pointer.
    unsafe { *__errno_location() }
}

#[cfg(all(
    any(
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
    ),
    not(target_family = "wasm"),
))]
fn last_error_code() -> i32 {
    unsafe extern "C" {
        fn __error() -> *mut i32;
    }
    // SAFETY: `__error` returns a valid thread-local pointer.
    unsafe { *__error() }
}

#[cfg(all(windows, not(target_family = "wasm")))]
fn last_error_code() -> i32 {
    unsafe extern "system" {
        fn GetLastError() -> u32;
    }
    // SAFETY: `GetLastError` has no preconditions.
    unsafe { GetLastError() as i32 }
}

#[cfg(target_family = "wasm")]
fn last_error_code() -> i32 {
    0
}

// ─── BufReader ───────────────────────────────────────────────────────────────

/// A reader that buffers its input.
pub struct BufReader<R> {
    inner: R,
    buf: Vec<u8>,
    pos: usize,
    cap: usize,
}

impl<R: crate::traits::Read> BufReader<R> {
    /// Creates a new `BufReader` with default buffer size.
    pub fn new(inner: R) -> Self {
        Self {
            inner,
            buf: alloc::vec![0u8; 8192],
            pos: 0,
            cap: 0,
        }
    }

    /// Fill the buffer if empty.
    pub fn fill_buf(&mut self) -> Result<&[u8]> {
        if self.pos >= self.cap {
            self.cap = self.inner.read(&mut self.buf)?;
            self.pos = 0;
        }
        Ok(&self.buf[self.pos..self.cap])
    }

    /// Consume bytes.
    pub fn consume(&mut self, amt: usize) {
        self.pos = core::cmp::min(self.pos + amt, self.cap);
    }

    /// Read a line.
    pub fn read_line(&mut self, buf: &mut crate::string::String) -> Result<usize> {
        let mut total = 0;
        loop {
            let available = self.fill_buf()?;
            if available.is_empty() {
                break;
            }
            if let Some(i) = available.iter().position(|&b| b == b'\n') {
                let bytes = &available[..=i];
                buf.push_str(
                    core::str::from_utf8(bytes).map_err(|_| Error::from(ErrorKind::Other))?,
                );
                self.pos += i + 1;
                total += i + 1;
                break;
            } else {
                let n = available.len();
                buf.push_str(
                    core::str::from_utf8(available).map_err(|_| Error::from(ErrorKind::Other))?,
                );
                self.pos += n;
                total += n;
            }
        }
        Ok(total)
    }
}

impl<R: Read> Read for BufReader<R> {
    fn read(&mut self, buf: &mut [u8]) -> Result<usize> {
        let n = {
            let rem = self.fill_buf()?;
            let n = core::cmp::min(buf.len(), rem.len());
            buf[..n].copy_from_slice(&rem[..n]);
            n
        };
        self.consume(n);
        Ok(n)
    }
}

impl<R: Read> BufRead for BufReader<R> {
    fn fill_buf(&mut self) -> Result<&[u8]> {
        self.fill_buf()
    }

    fn consume(&mut self, amt: usize) {
        self.consume(amt);
    }
}

// ─── Async traits ────────────────────────────────────────────────────────────

/// Types that can report whether they are connected to a terminal.
pub trait IsTerminal {
    /// Returns `true` if this instance is connected to a terminal (TTY).
    fn is_terminal(&self) -> bool;
}

// ─── BufWriter ───────────────────────────────────────────────────────────────

/// A writer that buffers its output.
pub struct BufWriter<W: Write> {
    inner: W,
    buf: Vec<u8>,
}

impl<W: Write> BufWriter<W> {
    /// Creates a new `BufWriter` with default buffer size.
    pub fn new(inner: W) -> Self {
        Self {
            inner,
            buf: Vec::with_capacity(8192),
        }
    }

    /// Gets a reference to the underlying writer.
    pub fn get_ref(&self) -> &W {
        &self.inner
    }

    /// Gets a mutable reference to the underlying writer.
    pub fn get_mut(&mut self) -> &mut W {
        &mut self.inner
    }

    /// Flushes the buffer to the underlying writer.
    pub fn flush_buf(&mut self) -> Result<()> {
        if self.buf.is_empty() {
            return Ok(());
        }
        self.inner.write_all(&self.buf)?;
        self.buf.clear();
        Ok(())
    }
}

impl<W: Write> Write for BufWriter<W> {
    fn write(&mut self, buf: &[u8]) -> Result<usize> {
        self.buf.extend_from_slice(buf);
        if self.buf.len() >= 8192 {
            self.flush_buf()?;
        }
        Ok(buf.len())
    }

    fn flush(&mut self) -> Result<()> {
        self.flush_buf()?;
        self.inner.flush()
    }
}

impl<W: Write> Drop for BufWriter<W> {
    fn drop(&mut self) {
        let _ = self.flush_buf();
    }
}

// ─── Sink ──────────────────────────────────────────────────────────────────

/// A writer that discards all output.
pub struct Sink;

/// Creates a writer that discards all output.
pub fn sink() -> Sink {
    Sink
}

impl Write for Sink {
    fn write(&mut self, buf: &[u8]) -> Result<usize> {
        Ok(buf.len())
    }
    fn flush(&mut self) -> Result<()> {
        Ok(())
    }
}
