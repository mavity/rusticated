//! Core traits for I/O and other basic operations.

/// Trait for reading (asynchronous).
pub trait AsyncRead {
    /// Read bytes into the spare capacity of `buf`.
    async fn read(
        &mut self,
        buf: crate::vec::Vec<u8>,
    ) -> (crate::io::Result<usize>, crate::vec::Vec<u8>);
}

/// Trait for writing (asynchronous).
pub trait AsyncWrite {
    /// Write bytes from `buf`.
    async fn write(
        &mut self,
        buf: crate::vec::Vec<u8>,
    ) -> (crate::io::Result<usize>, crate::vec::Vec<u8>);

    /// Flush this output stream.
    async fn flush(&mut self) -> crate::io::Result<()>;
}
