//! I/O abstractions — owned-buffer async traits.
use std::io;

/// Async reader that passes buffer ownership to the implementor.
///
/// Unlike [`std::io::Read`], the buffer is passed by value and returned with
/// the result. This matches the proactor completion model used by
/// `compio-driver`, where the OS holds the buffer during the operation.
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
    async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>);
}

/// Async writer that passes buffer ownership to the implementor.
///
/// Unlike [`std::io::Write`], the buffer is passed by value and returned with
/// the result. This matches the proactor completion model used by
/// `compio-driver`, where the OS holds the buffer during the operation.
pub trait AsyncWrite {
    /// Write bytes from `buf`.
    ///
    /// Returns `(result, buf)` where `result` holds the number of bytes
    /// written on success.
    async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>);
}
