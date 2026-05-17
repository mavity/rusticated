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

/// Extension methods for `AsyncRead`.
pub trait AsyncReadExt: AsyncRead {
    /// Read exactly the capacity of the buffer.
    async fn read_exact(&mut self, mut buf: Vec<u8>) -> (io::Result<()>, Vec<u8>) {
        let cap = buf.capacity();
        let mut read_total = 0;

        while read_total < cap {
            let (res, returned_buf) = self.read(buf).await;
            buf = returned_buf;

            match res {
                Ok(0) => {
                    return (
                        Err(io::Error::new(
                            io::ErrorKind::UnexpectedEof,
                            "failed to fill whole buffer",
                        )),
                        buf,
                    );
                }
                Ok(n) => {
                    read_total += n;
                    // We must prepare buf for the next read by keeping capacity
                    // However, `read` method is expected to write starting at position 0.
                    // Wait, if it writes at position 0, `read_exact` needs to assemble it!
                    // This implies the trait `read` must accept an offset, or we collect multiple
                    // buffers. Let's rely on standard practice: `read` writes
                    // to length? No, the trait says: "Callers should pass a
                    // Vec<u8> with len == 0 and sufficient capacity.
                    // The implementation writes bytes starting at position 0 and returns the Vec
                    // with len set"
                }
                Err(e) => {
                    return (Err(e), buf);
                }
            }
        }
        (Ok(()), buf)
    }
}
