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
    /// Other, unclassified error.
    Other,
}

/// An I/O error backed by an OS error code or a static message.
#[derive(Debug)]
pub struct Error {
    kind: ErrorKind,
    /// Raw OS error code; 0 means this is a synthesised error.
    code: i32,
    msg: &'static str,
}

/// `core::result::Result<T, crate::io::Error>`.
pub type Result<T> = core::result::Result<T, Error>;

impl Error {
    /// Returns the last OS-level error (reads `errno` on Unix, `GetLastError` on Windows).
    #[inline]
    pub fn last_os_error() -> Self {
        Self {
            kind: ErrorKind::Other,
            code: last_error_code(),
            msg: "",
        }
    }

    /// Constructs an error from a raw OS error code.
    #[inline]
    pub fn from_raw_os_error(code: i32) -> Self {
        Self {
            kind: ErrorKind::Other,
            code,
            msg: "",
        }
    }

    /// Constructs a synthesised error with a static message and `Other` kind.
    #[inline]
    pub fn other(msg: &'static str) -> Self {
        Self {
            kind: ErrorKind::Other,
            code: 0,
            msg,
        }
    }

    /// Constructs a synthesised error with the specified kind and message.
    #[inline]
    pub fn new(kind: ErrorKind, msg: &'static str) -> Self {
        Self { kind, code: 0, msg }
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
            f.write_str(self.msg)
        }
    }
}

impl core::error::Error for Error {}

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

// ─── Async traits ────────────────────────────────────────────────────────────

/// Async reader that passes buffer ownership to the implementor.
///
/// Unlike `std::io::Read`, the buffer is passed by value and returned with
/// the result. This matches the proactor completion model, where the OS holds
/// the buffer during the operation.
///
/// # Buffer contract
///
/// Callers should pass a [`Vec<u8>`] with `len == 0` and sufficient
/// `capacity`. The implementation writes bytes starting at position `0` and
/// returns the Vec with `len` set to the number of bytes read.
pub trait AsyncRead {
    /// Read bytes into the spare capacity of `buf`.
    ///
    /// Returns `(result, buf)` where `result` holds the number of bytes read
    /// on success.
    async fn read(&mut self, buf: Vec<u8>) -> (Result<usize>, Vec<u8>);
}

/// Async writer that passes buffer ownership to the implementor.
///
/// Unlike `std::io::Write`, the buffer is passed by value and returned with
/// the result. This matches the proactor completion model, where the OS holds
/// the buffer during the operation.
pub trait AsyncWrite {
    /// Write bytes from `buf`.
    ///
    /// Returns `(result, buf)` where `result` holds the number of bytes
    /// written on success.
    async fn write(&mut self, buf: Vec<u8>) -> (Result<usize>, Vec<u8>);
}
