//! File system utilities

#[cfg(not(target_family = "wasm"))]
mod native_fs {
    use std::{io, path::Path};

    use compio_buf::{BufResult, IntoInner};
    use compio_driver::{AsRawFd, SharedFd, op};
    use compio_driver::op::BufResultExt;

    use crate::io::{AsyncRead, AsyncWrite};
    use crate::rt::native::{OpFuture, with_proactor};

    /// An open file handle providing async positioned I/O.
    pub struct File {
        inner: SharedFd<std::fs::File>,
        pos: u64,
    }

    impl File {
        /// Open a file in read-only mode.
        pub fn open<P: AsRef<Path>>(path: P) -> io::Result<Self> {
            OpenOptions::new().read(true).open(path)
        }

        /// Create a file, truncating if it already exists.
        pub fn create<P: AsRef<Path>>(path: P) -> io::Result<Self> {
            OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .open(path)
        }
    }

    impl AsyncRead for File {
        async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            // The OS writes into the buffer starting at byte 0.  Callers must
            // pass a Vec with `len == 0` so that `advance_to(n)` sets the
            // correct length after the read.
            let op = op::ReadAt::new(self.inner.clone(), self.pos, buf);
            match OpFuture::new(op).await {
                Err(e) => (Err(e), Vec::new()),
                Ok(buf_result) => {
                    // Extract the Vec<u8> from inside ReadAt.
                    let buf_result = buf_result.into_inner();
                    // SAFETY: compio-driver wrote exactly `n` valid bytes into
                    // the buffer beginning at position 0.  `map_advanced` calls
                    // `Vec::set_len(n)` only when the result is `Ok(n)`.
                    let BufResult(result, buf) = unsafe { buf_result.map_advanced() };
                    if let Ok(n) = result {
                        self.pos += n as u64;
                    }
                    (result, buf)
                }
            }
        }
    }

    impl AsyncWrite for File {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            let op = op::WriteAt::new(self.inner.clone(), self.pos, buf);
            match OpFuture::new(op).await {
                Err(e) => (Err(e), Vec::new()),
                Ok(buf_result) => {
                    // Extract the Vec<u8> from inside WriteAt.
                    let BufResult(result, buf) = buf_result.into_inner();
                    if let Ok(n) = result {
                        self.pos += n as u64;
                    }
                    (result, buf)
                }
            }
        }
    }

    /// Builder for opening files with specific options.
    pub struct OpenOptions {
        inner: std::fs::OpenOptions,
    }

    impl OpenOptions {
        /// Create a blank set of options.
        pub fn new() -> Self {
            Self {
                inner: std::fs::OpenOptions::new(),
            }
        }

        /// Enable or disable read access.
        pub fn read(&mut self, read: bool) -> &mut Self {
            self.inner.read(read);
            self
        }

        /// Enable or disable write access.
        pub fn write(&mut self, write: bool) -> &mut Self {
            self.inner.write(write);
            self
        }

        /// Enable or disable append mode.
        pub fn append(&mut self, append: bool) -> &mut Self {
            self.inner.append(append);
            self
        }

        /// Enable or disable truncation on open.
        pub fn truncate(&mut self, truncate: bool) -> &mut Self {
            self.inner.truncate(truncate);
            self
        }

        /// Create the file if it does not exist.
        pub fn create(&mut self, create: bool) -> &mut Self {
            self.inner.create(create);
            self
        }

        /// Fail if the file already exists.
        pub fn create_new(&mut self, create_new: bool) -> &mut Self {
            self.inner.create_new(create_new);
            self
        }

        /// Open the file at `path` according to the options.
        ///
        /// The underlying system call is synchronous (file open is not
        /// typically the bottleneck), but the file handle is registered with
        /// the proactor so that subsequent reads and writes are asynchronous.
        pub fn open<P: AsRef<Path>>(&self, path: P) -> io::Result<File> {
            let std_file = self.inner.open(path.as_ref())?;
            let fd = SharedFd::new(std_file);
            // Register with the IOCP (no-op on io_uring and polling drivers).
            with_proactor(|p| p.attach(fd.as_raw_fd()))??;
            Ok(File { inner: fd, pos: 0 })
        }
    }

    impl Default for OpenOptions {
        fn default() -> Self {
            Self::new()
        }
    }
}

#[cfg(not(target_family = "wasm"))]
pub use native_fs::{File, OpenOptions};

// ——— WASM ——————————————————————————————————————————————————————————————————

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedFuture;

/// WASM file handle.
#[cfg(target_family = "wasm")]
pub struct File {
    handle: u64,
}

#[cfg(target_family = "wasm")]
impl File {
    /// Open a file in read-only mode.
    pub async fn open<P: AsRef<std::path::Path>>(path: P) -> std::io::Result<File> {
        OpenOptions::new().read(true).open(path).await
    }

    /// Create a file, truncating if it already exists.
    pub async fn create<P: AsRef<std::path::Path>>(path: P) -> std::io::Result<File> {
        OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)
            .await
    }
}

#[cfg(target_family = "wasm")]
impl crate::io::AsyncRead for File {
    async fn read(&mut self, mut buf: Vec<u8>) -> (std::io::Result<usize>, Vec<u8>) {
        let ptr = buf.as_mut_ptr();
        let len = buf.capacity() as u32;
        let handle = self.handle;

        let (err, bytes_read, _) = OverlappedFuture::new(move |ov| {
            // SAFETY: `ptr` and `len` are valid for the duration of this
            // overlapped operation — `buf` lives in the enclosing async frame.
            unsafe { imports::read(ov, handle, ptr, len) };
        })
        .await;

        if err != 0 {
            return (
                Err(std::io::Error::from_raw_os_error(err as i32)),
                buf,
            );
        }

        // SAFETY: The WASM host wrote `bytes_read` valid bytes at position 0.
        unsafe { buf.set_len(bytes_read as usize) };
        (Ok(bytes_read as usize), buf)
    }
}

#[cfg(target_family = "wasm")]
impl crate::io::AsyncWrite for File {
    async fn write(&mut self, buf: Vec<u8>) -> (std::io::Result<usize>, Vec<u8>) {
        let ptr = buf.as_ptr();
        let len = buf.len() as u32;
        let handle = self.handle;

        let (err, bytes_written, _) = OverlappedFuture::new(move |ov| {
            // SAFETY: `ptr` and `len` are valid for the duration of this
            // overlapped operation — `buf` lives in the enclosing async frame.
            unsafe { imports::write(ov, handle, ptr, len) };
        })
        .await;

        if err != 0 {
            return (
                Err(std::io::Error::from_raw_os_error(err as i32)),
                buf,
            );
        }

        (Ok(bytes_written as usize), buf)
    }
}

/// WASM file-open options builder.
#[cfg(target_family = "wasm")]
pub struct OpenOptions {
    read: bool,
    write: bool,
    append: bool,
    truncate: bool,
    create: bool,
    create_new: bool,
}

#[cfg(target_family = "wasm")]
impl OpenOptions {
    /// Create a blank set of options.
    pub fn new() -> Self {
        Self {
            read: false,
            write: false,
            append: false,
            truncate: false,
            create: false,
            create_new: false,
        }
    }

    /// Enable or disable read access.
    pub fn read(&mut self, read: bool) -> &mut Self {
        self.read = read;
        self
    }

    /// Enable or disable write access.
    pub fn write(&mut self, write: bool) -> &mut Self {
        self.write = write;
        self
    }

    /// Enable or disable append mode.
    pub fn append(&mut self, append: bool) -> &mut Self {
        self.append = append;
        self
    }

    /// Enable or disable truncation on open.
    pub fn truncate(&mut self, truncate: bool) -> &mut Self {
        self.truncate = truncate;
        self
    }

    /// Create the file if it does not exist.
    pub fn create(&mut self, create: bool) -> &mut Self {
        self.create = create;
        self
    }

    /// Fail if the file already exists.
    pub fn create_new(&mut self, create_new: bool) -> &mut Self {
        self.create_new = create_new;
        self
    }

    /// Open the file at `path` according to the options.
    pub async fn open<P: AsRef<std::path::Path>>(&self, path: P) -> std::io::Result<File> {
        let path_str = path
            .as_ref()
            .to_str()
            .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::InvalidInput, "invalid path"))?;
        let path_ptr = path_str.as_ptr();
        let path_len = path_str.len() as u32;

        let mut flags = 0u32;
        if self.read {
            flags |= 1;
        }
        if self.write {
            flags |= 2;
        }
        if self.create {
            flags |= 4;
        }
        if self.truncate {
            flags |= 8;
        }
        if self.append {
            flags |= 16;
        }
        if self.create_new {
            flags |= 32;
        }

        let (err, handle, _) = OverlappedFuture::new(move |ov| {
            // SAFETY: `path_ptr` and `path_len` refer to the `path_str` slice
            // which lives in the enclosing async frame.
            unsafe { imports::path_open(ov, path_ptr, path_len, flags) };
        })
        .await;

        if err != 0 {
            return Err(std::io::Error::from_raw_os_error(err as i32));
        }

        Ok(File { handle })
    }
}

#[cfg(target_family = "wasm")]
impl Default for OpenOptions {
    fn default() -> Self {
        Self::new()
    }
}

/// WASM directory reader.
#[cfg(target_family = "wasm")]
pub struct DirReader {
    handle: u64,
    continued: u64,
}

#[cfg(target_family = "wasm")]
impl DirReader {
    /// Read the next batch of directory entries.
    ///
    /// Returns `None` when all entries have been read.
    pub async fn read_entries(&mut self) -> std::io::Result<Option<Vec<String>>> {
        let mut buf = vec![0u8; 4096];
        let ptr = buf.as_mut_ptr();
        let len = buf.len() as u32;

        let (err, bytes_read, next_continued) = OverlappedFuture::new({
            let handle = self.handle;
            let continued = self.continued;
            move |ov| {
                // SAFETY: `ptr` and `len` are valid; `buf` lives in this frame.
                unsafe {
                    (*ov).continued = continued;
                    imports::dir_read(ov, handle, ptr, len);
                }
            }
        })
        .await;

        if err != 0 {
            return Err(std::io::Error::from_raw_os_error(err as i32));
        }

        self.continued = next_continued;

        let entries = buf[..bytes_read as usize]
            .split(|&b| b == 0)
            .filter(|s| !s.is_empty())
            .map(|s| String::from_utf8_lossy(s).to_string())
            .collect();

        Ok(Some(entries))
    }
}

/// File metadata for WASM.
#[cfg(target_family = "wasm")]
pub struct Metadata {
    #[allow(dead_code)]
    pub(crate) handle: u64,
}

#[cfg(target_family = "wasm")]
impl Metadata {
    /// Returns `true` if this metadata describes a regular file.
    pub fn is_file(&self) -> bool {
        true
    }

    /// Returns `true` if this metadata describes a directory.
    pub fn is_dir(&self) -> bool {
        false
    }

    /// Returns the size of the file in bytes.
    pub fn len(&self) -> u64 {
        0
    }
}

/// Query metadata for `path`.
#[cfg(target_family = "wasm")]
pub async fn metadata<P: AsRef<std::path::Path>>(path: P) -> std::io::Result<Metadata> {
    let path_str = path
        .as_ref()
        .to_str()
        .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::InvalidInput, "invalid path"))?;
    let path_ptr = path_str.as_ptr();
    let path_len = path_str.len() as u32;

    let (err, handle, _) = OverlappedFuture::new(move |ov| {
        // SAFETY: `path_ptr` and `path_len` refer to `path_str` in this frame.
        unsafe { imports::path_stat(ov, path_ptr, path_len) };
    })
    .await;

    if err != 0 {
        return Err(std::io::Error::from_raw_os_error(err as i32));
    }

    Ok(Metadata { handle })
}

