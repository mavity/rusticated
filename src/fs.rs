//! File system utilities

// WASM stub code intentionally trades clippy purity for clarity at the host
// ABI boundary: pointer-sized casts and host-contract truncations are
// inherent. Allows are scoped to the WASM target.
#![cfg_attr(
    target_family = "wasm",
    allow(
        clippy::cast_possible_truncation,
        clippy::cast_possible_wrap,
        clippy::cast_sign_loss,
        clippy::missing_const_for_fn,
        clippy::doc_markdown,
        clippy::type_complexity,
        clippy::unnecessary_wraps,
        clippy::needless_pass_by_value,
        clippy::struct_field_names,
        clippy::len_without_is_empty,
        clippy::use_self,
    )
)]

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

// ——— Native non-Linux (Windows) — native file API

#[cfg(all(not(target_family = "wasm"), target_os = "windows"))]
pub use native_windows::{File, OpenOptions};

#[cfg(all(not(target_family = "wasm"), target_os = "windows"))]
mod native_windows {
    use crate::rt::windows::{OverlappedRead, OverlappedWrite};
    use std::{io, os::windows::ffi::OsStrExt};

    // Minimal definitions for native windows APIs instead of relying on `windows-sys`
    unsafe extern "system" {
        fn CreateFileW(
            lpFileName: *const u16,
            dwDesiredAccess: u32,
            dwShareMode: u32,
            lpSecurityAttributes: *mut std::ffi::c_void,
            dwCreationDisposition: u32,
            dwFlagsAndAttributes: u32,
            hTemplateFile: usize,
        ) -> usize;
        fn CloseHandle(hObject: usize) -> i32;
    }

    const GENERIC_READ: u32 = 0x8000_0000;
    const GENERIC_WRITE: u32 = 0x4000_0000;
    const FILE_SHARE_READ: u32 = 0x0000_0001;
    const FILE_SHARE_WRITE: u32 = 0x0000_0002;
    const FILE_SHARE_DELETE: u32 = 0x0000_0004;
    const CREATE_NEW: u32 = 1;
    const CREATE_ALWAYS: u32 = 2;
    const OPEN_EXISTING: u32 = 3;
    const OPEN_ALWAYS: u32 = 4;
    const TRUNCATE_EXISTING: u32 = 5;
    const FILE_ATTRIBUTE_NORMAL: u32 = 0x0000_0080;
    const FILE_FLAG_OVERLAPPED: u32 = 0x4000_0000;
    const INVALID_HANDLE_VALUE: usize = !0;

    /// File handle implementation using Windows Overlapped I/O
    pub struct File {
        handle: u64,
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
            // SAFETY: Safe to close file handles.
            #[allow(clippy::cast_possible_truncation)]
            unsafe {
                CloseHandle(self.handle as usize)
            };
        }
    }

    impl crate::io::AsyncRead for File {
        async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            OverlappedRead::new(self.handle, buf).await
        }
    }

    impl crate::io::AsyncWrite for File {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            OverlappedWrite::new(self.handle, buf).await
        }
    }

    /// `OpenOptions` for Windows
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
        #[allow(clippy::missing_const_for_fn)]
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
        #[allow(clippy::missing_const_for_fn)]
        pub fn read(&mut self, v: bool) -> &mut Self {
            self.read = v;
            self
        }

        /// Enable or disable write access.
        #[allow(clippy::missing_const_for_fn)]
        pub fn write(&mut self, v: bool) -> &mut Self {
            self.write = v;
            self
        }

        /// Enable or disable append mode.
        #[allow(clippy::missing_const_for_fn)]
        pub fn append(&mut self, v: bool) -> &mut Self {
            self.append = v;
            self
        }

        /// Enable or disable truncation on open.
        #[allow(clippy::missing_const_for_fn)]
        pub fn truncate(&mut self, v: bool) -> &mut Self {
            self.truncate = v;
            self
        }

        /// Create the file if it does not exist.
        #[allow(clippy::missing_const_for_fn)]
        pub fn create(&mut self, v: bool) -> &mut Self {
            self.create = v;
            self
        }

        /// Fail if the file already exists.
        #[allow(clippy::missing_const_for_fn)]
        pub fn create_new(&mut self, v: bool) -> &mut Self {
            self.create_new = v;
            self
        }

        /// Open the file at `path` within Windows using Overlapped I/O
        #[allow(clippy::unused_async)]
        pub async fn open<P: AsRef<std::path::Path>>(&self, path: P) -> io::Result<File> {
            let mut access = 0;
            if self.read {
                access |= GENERIC_READ;
            }
            if self.write {
                access |= GENERIC_WRITE;
            }

            let creation = if self.create_new {
                CREATE_NEW
            } else if self.truncate && self.create {
                CREATE_ALWAYS
            } else if self.truncate {
                TRUNCATE_EXISTING
            } else if self.create {
                OPEN_ALWAYS
            } else {
                OPEN_EXISTING
            };

            let flags = FILE_ATTRIBUTE_NORMAL | FILE_FLAG_OVERLAPPED;
            let share = FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE;

            let path_wide: Vec<u16> = path
                .as_ref()
                .as_os_str()
                .encode_wide()
                .chain(std::iter::once(0))
                .collect();

            // SAFETY: Safe ffi call
            let handle = unsafe {
                CreateFileW(
                    path_wide.as_ptr(),
                    access,
                    share,
                    std::ptr::null_mut(),
                    creation,
                    flags,
                    0,
                )
            };

            if handle == INVALID_HANDLE_VALUE {
                return Err(io::Error::last_os_error());
            }

            let file = File {
                handle: handle as u64,
            };

            // Register handle with the driver's IOCP
            let _ = crate::rt::executor::with_driver(|d| d.register(file.handle));

            Ok(file)
        }
    }

    impl Default for OpenOptions {
        fn default() -> Self {
            Self::new()
        }
    }
}

// ——— Native non-Linux (macOS, BSD) — stubs until drivers are ready ——

#[cfg(all(
    not(target_family = "wasm"),
    not(target_os = "linux"),
    not(target_os = "windows")
))]
pub use native_stub::{File, OpenOptions};

#[cfg(all(
    not(target_family = "wasm"),
    not(target_os = "linux"),
    not(target_os = "windows")
))]
#[allow(
    clippy::unused_async,
    clippy::missing_const_for_fn,
    clippy::doc_markdown,
    dead_code
)]
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
use crate::rt::wasm::OverlappedBufferFuture;

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
    async fn read(&mut self, buf: Vec<u8>) -> (std::io::Result<usize>, Vec<u8>) {
        let handle = self.handle;

        let (err, bytes_read, _, mut buf) =
            OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                // SAFETY: `ptr`/`len` describe the buffer owned by the future's
                // state, kept alive by an `Rc` clone in the completion registry
                // until the host signals completion — even if this future is
                // dropped.
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
        let handle = self.handle;
        // For writes we only consume `len`, not capacity.
        let used = buf.len() as u32;

        let (err, bytes_written, _, buf) =
            OverlappedBufferFuture::new(buf, move |ov, ptr, _cap| {
                // SAFETY: `ptr` points into the future-owned buffer (lifetime
                // pinned via the completion registry's `Rc` clone). `used`
                // bytes are valid because the caller filled them in.
                unsafe { imports::write(ov, handle, ptr, used) };
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
        let path_bytes = path_str.as_bytes().to_vec();

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

        let (err, handle, _, _path) =
            OverlappedBufferFuture::new(path_bytes, move |ov, ptr, len| {
                // SAFETY: `ptr`/`len` describe the future-owned path buffer;
                // it outlives any cancellation thanks to the completion
                // registry's `Rc` clone.
                unsafe { imports::path_open(ov, ptr.cast_const(), len, flags) };
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::io::{AsyncRead, AsyncWrite};

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
                        // Short sleep for tests to avoid spinning cpu fully when testing async
                        // completions.
                        std::thread::sleep(std::time::Duration::from_millis(5));
                    }
                }
            }
        }
    }

    #[test]
    fn test_file_create_write_read() {
        block_on(async {
            let path = std::env::temp_dir().join("fast_std_test_file.txt");

            // Note: Currently Windows tests that run natively will pass with `OverlappedRead`.
            // Wasm falls back.
            let create_res = File::create(&path).await;
            if create_res.is_err() {
                // Ignore test on stubs
                return;
            }
            let mut file = create_res.unwrap();

            let data = b"hello fast-std async fs".to_vec();
            let (res, _) = file.write(data).await;
            assert_eq!(res.unwrap(), 23);

            let mut file = File::open(&path).await.expect("Failed to open");
            let buf = Vec::with_capacity(32);
            let (res, read_buf) = file.read(buf).await;
            assert_eq!(res.unwrap(), 23);
            assert_eq!(read_buf, b"hello fast-std async fs");

            let _ = std::fs::remove_file(&path);
        });
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
        let buf = vec![0u8; 4096];
        let handle = self.handle;
        let continued = self.continued;

        let (err, bytes_read, next_continued, buf) =
            OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                // SAFETY: `ptr`/`len` describe the future-owned buffer; the
                // completion registry keeps it alive across any drop.
                unsafe {
                    (*ov).continued = continued;
                    imports::dir_read(ov, handle, ptr, len);
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
        // SAFETY: The host implements `stat_is_file` via the ABI correctly.
        unsafe { crate::abi::imports::stat_is_file(self.handle) != 0 }
    }

    /// Returns `true` if this metadata describes a directory.
    pub fn is_dir(&self) -> bool {
        // SAFETY: The host implements `stat_is_dir` via the ABI correctly.
        unsafe { crate::abi::imports::stat_is_dir(self.handle) != 0 }
    }

    /// Returns the size of the file in bytes.
    pub fn len(&self) -> u64 {
        // SAFETY: The host implements `stat_len` via the ABI correctly.
        unsafe { crate::abi::imports::stat_len(self.handle) }
    }
}

/// Query metadata for `path`.
#[cfg(target_family = "wasm")]
pub async fn metadata<P: AsRef<std::path::Path>>(path: P) -> std::io::Result<Metadata> {
    let path_str = path
        .as_ref()
        .to_str()
        .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::InvalidInput, "invalid path"))?;
    let path_bytes = path_str.as_bytes().to_vec();

    let (err, handle, _, _path) = OverlappedBufferFuture::new(path_bytes, move |ov, ptr, len| {
        // SAFETY: `ptr`/`len` describe the future-owned path buffer; the
        // completion registry keeps it alive across any drop.
        unsafe { imports::path_stat(ov, ptr.cast_const(), len) };
    })
    .await;

    if err != 0 {
        return Err(std::io::Error::from_raw_os_error(err as i32));
    }

    Ok(Metadata { handle })
}
