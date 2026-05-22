//! Core traits for I/O and other basic operations.

/// Trait for reading (synchronous).
pub trait Read {
    /// Read some bytes from this source into the specified buffer.
    fn read(&mut self, buf: &mut [u8]) -> crate::io::Result<usize>;

    /// Read the exact number of bytes required to fill `buf`.
    fn read_exact(&mut self, mut buf: &mut [u8]) -> crate::io::Result<()> {
        while !buf.is_empty() {
            match self.read(buf) {
                Ok(0) => break,
                Ok(n) => buf = &mut buf[n..],
                Err(ref e) if e.kind() == crate::io::ErrorKind::Interrupted => {
                    continue;
                }
                Err(e) => return Err(e),
            }
        }
        if !buf.is_empty() {
            Err(crate::io::Error::new(
                crate::io::ErrorKind::UnexpectedEof,
                "failed to fill whole buffer",
            ))
        } else {
            Ok(())
        }
    }

    /// Read all bytes from this source and append them to `buf`.
    fn read_to_end(&mut self, buf: &mut crate::vec::Vec<u8>) -> crate::io::Result<usize>
    where
        Self: Sized,
    {
        let mut temp = [0u8; 8192];
        let mut total = 0;
        loop {
            match self.read(&mut temp) {
                Ok(0) => return Ok(total),
                Ok(n) => {
                    buf.extend_from_slice(&temp[..n]);
                    total += n;
                }
                Err(e) if e.kind() == crate::io::ErrorKind::Interrupted => {}
                Err(e) => return Err(e),
            }
        }
    }

    /// Read all bytes from this source and append them to `buf`.
    fn read_to_string(&mut self, buf: &mut crate::string::String) -> crate::io::Result<usize>
    where
        Self: Sized,
    {
        let mut temp = crate::vec::Vec::new();
        let n = self.read_to_end(&mut temp)?;
        let s = core::str::from_utf8(&temp).map_err(|_| {
            crate::io::Error::new(crate::io::ErrorKind::InvalidData, "invalid utf-8")
        })?;
        buf.push_str(s);
        Ok(n)
    }
}

/// Trait for reading (asynchronous).
pub trait AsyncRead {
    /// Read bytes into the spare capacity of `buf`.
    async fn read(
        &mut self,
        buf: crate::vec::Vec<u8>,
    ) -> (crate::io::Result<usize>, crate::vec::Vec<u8>);
}

/// Trait for writing (synchronous).
pub trait Write {
    /// Write a buffer into this object, returning how many bytes were written.
    fn write(&mut self, buf: &[u8]) -> crate::io::Result<usize>;

    /// Flush this output stream.
    fn flush(&mut self) -> crate::io::Result<()>;

    /// Attempts to write an entire buffer into this writer.
    fn write_all(&mut self, mut buf: &[u8]) -> crate::io::Result<()> {
        while !buf.is_empty() {
            match self.write(buf) {
                Ok(0) => {
                    return Err(crate::io::Error::new(
                        crate::io::ErrorKind::WriteZero,
                        "failed to write whole buffer",
                    ));
                }
                Ok(n) => buf = &buf[n..],
                Err(ref e) if e.kind() == crate::io::ErrorKind::Interrupted => {}
                Err(e) => return Err(e),
            }
        }
        Ok(())
    }

    /// Writes a formatted string into this writer, returning any error encountered.
    fn write_fmt(&mut self, fmt: core::fmt::Arguments<'_>) -> crate::io::Result<()> {
        let mut s = crate::string::String::new();
        core::fmt::write(&mut s, fmt).map_err(|_| crate::io::Error::other("formatting failed"))?;
        self.write_all(s.as_bytes())
    }
}

impl Write for crate::vec::Vec<u8> {
    fn write(&mut self, buf: &[u8]) -> crate::io::Result<usize> {
        self.extend_from_slice(buf);
        Ok(buf.len())
    }
    fn flush(&mut self) -> crate::io::Result<()> {
        Ok(())
    }
}

impl Write for &mut crate::vec::Vec<u8> {
    fn write(&mut self, buf: &[u8]) -> crate::io::Result<usize> {
        self.extend_from_slice(buf);
        Ok(buf.len())
    }
    fn flush(&mut self) -> crate::io::Result<()> {
        Ok(())
    }
}

impl<W: Write + ?Sized> Write for alloc::boxed::Box<W> {
    fn write(&mut self, buf: &[u8]) -> crate::io::Result<usize> {
        (**self).write(buf)
    }
    fn flush(&mut self) -> crate::io::Result<()> {
        (**self).flush()
    }
    fn write_all(&mut self, buf: &[u8]) -> crate::io::Result<()> {
        (**self).write_all(buf)
    }
    fn write_fmt(&mut self, fmt: core::fmt::Arguments<'_>) -> crate::io::Result<()> {
        (**self).write_fmt(fmt)
    }
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

/// Trait for buffered reading.
pub trait BufRead: Read {
    /// Fill the buffer if empty.
    fn fill_buf(&mut self) -> crate::io::Result<&[u8]>;
    /// Consume bytes.
    fn consume(&mut self, amt: usize);
}

/// Trait for seeking.
pub trait Seek {
    /// Seek to an offset, in bytes, in a stream.
    fn seek(&mut self, pos: crate::io::SeekFrom) -> crate::io::Result<u64>;
}
