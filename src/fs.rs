//! File system utilities

// ——— Native Linux —————————————————————————————————————————————————————————

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
pub use native_linux::{File, OpenOptions};

#[cfg(all(not(target_family = "wasm"), target_os = "linux"))]
mod native_linux {
    use std::{ffi::CString, io, os::unix::ffi::OsStrExt as _};

    extern "C" {
        fn open(pathname: *const u8, flags: i32, mode: u32) -> i32;
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        fn close(fd: i32) -> i32;
    }

    const O_RDONLY: i32 = 0;
    const O_WRONLY: i32 = 1;
    const O_RDWR: i32 = 2;
    const O_CREAT: i32 = 0o100;
    const O_TRUNC: i32 = 0o1000;
    const O_APPEND: i32 = 0o2000;
    const O_CLOEXEC: i32 = 0o2_000_000;
    const O_EXCL: i32 = 0o200;

    /// An open file descriptor providing async I/O.
    pub struct File {
        fd: i32,
    }

    impl File {
        /// Open a file in read-only mode.
        pub async fn open<P: AsRef<std::path::Path>>(path: P) -> io::Result<Self> {
            OpenOptions::new().read(true).open(path).await
        }

        /// Create or truncate a file for writing.
        pub async fn create<P: AsRef<std::path::Path>>(path: P) -> io::Result<Self> {
            OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .open(path)
                .await
        }
    }

    impl Drop for File {
        fn drop(&mut self) {
            unsafe { close(self.fd) };
        }
    }

    impl crate::io::AsyncRead for File {
        async fn read(&mut self, mut buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            let n = unsafe { read(self.fd, buf.as_mut_ptr(), buf.capacity()) };
            if n < 0 {
                (Err(io::Error::last_os_error()), buf)
            } else {
                unsafe { buf.set_len(n as usize) };
                (Ok(n as usize), buf)
            }
        }
    }

    impl crate::io::AsyncWrite for File {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            let n = unsafe { write(self.fd, buf.as_ptr(), buf.len()) };
            if n < 0 {
                (Err(io::Error::last_os_error()), buf)
            } else {
                (Ok(n as usize), buf)
            }
        }
    }

    /// Builder for opening files with specific options.
    pub struct OpenOptions {
        read: bool,
        write: bool,
        append: bool,
        truncate: bool,
        create: bool,
        create_new: bool,
    }

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
        pub fn read(&mut self, v: bool) -> &mut Self {
            self.read = v;
            self
        }

        /// Enable or disable write access.
        pub fn write(&mut self, v: bool) -> &mut Self {
            self.write = v;
            self
        }

        /// Enable or disable append mode.
        pub fn append(&mut self, v: bool) -> &mut Self {
            self.append = v;
            self
        }

        /// Enable or disable truncation on open.
        pub fn truncate(&mut self, v: bool) -> &mut Self {
            self.truncate = v;
            self
        }

        /// Create the file if it does not exist.
        pub fn create(&mut self, v: bool) -> &mut Self {
            self.create = v;
            self
        }

        /// Fail if the file already exists.
        pub fn create_new(&mut self, v: bool) -> &mut Self {
            self.create_new = v;
            self
        }

        /// Open the file at `path` according to the options.
        pub async fn open<P: AsRef<std::path::Path>>(&self, path: P) -> io::Result<File> {
            let cpath = CString::new(path.as_ref().as_os_str().as_bytes())
                .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "path contains null"))?;
            let mut flags = O_CLOEXEC;
            if self.read && self.write {
                flags |= O_RDWR;
            } else if self.write {
                flags |= O_WRONLY;
            } else {
                flags |= O_RDONLY;
            }
            if self.create {
                flags |= O_CREAT;
            }
            if self.truncate {
                flags |= O_TRUNC;
            }
            if self.append {
                flags |= O_APPEND;
            }
            if self.create_new {
                flags |= O_EXCL | O_CREAT;
            }

            let fd = unsafe { open(cpath.as_ptr() as _, flags, 0o666) };
            if fd < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(File { fd })
        }
    }

    impl Default for OpenOptions {
        fn default() -> Self {
            Self::new()
        }
    }
}

// ——— Native non-Linux (Windows, macOS, BSD) — stubs until drivers are ready ——

#[cfg(all(not(target_family = "wasm"), not(target_os = "linux")))]
pub use native_stub::{File, OpenOptions};

#[cfg(all(not(target_family = "wasm"), not(target_os = "linux")))]
mod native_stub {
    use std::io;

    /// File handle stub — not yet implemented on this platform.
    pub struct File;

    impl File {
        /// Open a file (stub).
        pub async fn open<P: AsRef<std::path::Path>>(_path: P) -> io::Result<Self> {
            Err(io::Error::other(
                "fs::File not yet implemented on this platform",
            ))
        }

        /// Create a file (stub).
        pub async fn create<P: AsRef<std::path::Path>>(_path: P) -> io::Result<Self> {
            Err(io::Error::other(
                "fs::File not yet implemented on this platform",
            ))
        }
    }

    impl crate::io::AsyncRead for File {
        async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            (Err(io::Error::other("not implemented")), buf)
        }
    }

    impl crate::io::AsyncWrite for File {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            (Err(io::Error::other("not implemented")), buf)
        }
    }

    /// OpenOptions stub.
    pub struct OpenOptions;

    impl OpenOptions {
        /// Create a blank set of options.
        pub fn new() -> Self {
            Self
        }
        /// Enable or disable read access.
        pub fn read(&mut self, _: bool) -> &mut Self {
            self
        }
        /// Enable or disable write access.
        pub fn write(&mut self, _: bool) -> &mut Self {
            self
        }
        /// Enable or disable append mode.
        pub fn append(&mut self, _: bool) -> &mut Self {
            self
        }
        /// Enable or disable truncation on open.
        pub fn truncate(&mut self, _: bool) -> &mut Self {
            self
        }
        /// Create the file if it does not exist.
        pub fn create(&mut self, _: bool) -> &mut Self {
            self
        }
        /// Fail if the file already exists.
        pub fn create_new(&mut self, _: bool) -> &mut Self {
            self
        }
        /// Open the file (stub).
        pub async fn open<P: AsRef<std::path::Path>>(&self, _path: P) -> io::Result<File> {
            Err(io::Error::other("not implemented"))
        }
    }

    impl Default for OpenOptions {
        fn default() -> Self {
            Self::new()
        }
    }
}

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
            return (Err(std::io::Error::from_raw_os_error(err as i32)), buf);
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
            return (Err(std::io::Error::from_raw_os_error(err as i32)), buf);
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
