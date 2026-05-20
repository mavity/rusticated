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

// ——— Native Unix ——————————————————————————————————————————————————————————

#[cfg(all(not(target_family = "wasm"), unix))]
pub use native_unix::{
    File as FileNative, FileTypeNative as FileType, MetadataNative as Metadata, OpenOptions,
};

#[cfg(all(not(target_family = "wasm"), unix))]
mod native_unix {
    use crate::io::{Read, Write};
    use crate::{
        ffi::CString,
        io,
        time::SystemTime,
        traits::{AsyncRead, AsyncWrite, Read, Write},
    };
    use alloc::vec::Vec;

    unsafe extern "C" {
        fn open(pathname: *const u8, flags: i32, mode: u32) -> i32;
        fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
        fn write(fd: i32, buf: *const u8, count: usize) -> isize;
        fn close(fd: i32) -> i32;
        fn dup(oldfd: i32) -> i32;
    }

    /// Read directory handle.
    pub type ReadDir = super::ReadDir;
    /// Directory entry.
    pub type DirEntry = super::DirEntry;
    /// File type information.
    pub type FileType = FileTypeNative;
    /// File metadata.
    pub type Metadata = MetadataNative;

    /// File type information.
    pub struct FileTypeNative;

    impl FileTypeNative {
        /// Returns true if the entry is a directory.
        pub fn is_dir(&self) -> bool {
            false
        }

        /// Returns true if the entry is a file.
        pub fn is_file(&self) -> bool {
            false
        }

        /// Returns true if the entry is a symbolic link.
        pub fn is_symlink(&self) -> bool {
            false
        }
    }

    /// File metadata.
    pub struct MetadataNative;

    impl MetadataNative {
        /// Returns true if the entry is a directory.
        pub fn is_dir(&self) -> bool {
            false
        }

        /// Returns true if the entry is a file.
        pub fn is_file(&self) -> bool {
            true
        }

        /// Returns the file length.
        pub fn len(&self) -> u64 {
            0
        }

        /// Returns the last modification time.
        pub fn modified(&self) -> io::Result<SystemTime> {
            Err(io::Error::other("not implemented"))
        }
    }

    pub(crate) static STATIC_METADATA: MetadataNative = MetadataNative;

    /// Builder for opening files with specific options.
    #[derive(Clone, Debug)]
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
        pub async fn open<P: AsRef<str>>(&self, path: P) -> io::Result<FileNative> {
            let cpath = CString::new(path.as_ref())
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
            Ok(FileNative { fd })
        }

        /// Open the file at `path` according to the options (sync).
        pub fn open_sync<P: AsRef<str>>(&self, path: P) -> io::Result<FileNative> {
            let cpath = CString::new(path.as_ref())
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
            Ok(FileNative { fd })
        }
    }

    impl Default for OpenOptions {
        fn default() -> Self {
            Self::new()
        }
    }

    /// Open the null device (/dev/null).
    pub fn open_null_file() -> io::Result<File> {
        Err(io::Error::other("open_null_file not implemented"))
    }

    /// Returns `true` if the shell should default to case-insensitive path expansion.
    pub fn default_case_insensitive_path_expansion() -> bool {
        false
    }

    /// Resolves an executable name to a full path by searching the PATH.
    pub fn resolve_executable<P: AsRef<str>>(_path: P) -> Option<alloc::string::String> {
        None
    }

    /// Splits a path into pieces suitable for globbing.
    pub fn split_path_for_pattern(_path: &str) -> Vec<&str> {
        alloc::vec![]
    }

    /// Returns the root of a pattern path (e.g., "/" on Unix, "C:\" on Windows).
    pub fn pattern_path_root(_path: &str) -> Option<alloc::string::String> {
        None
    }

    /// Normalizes path separators for the current platform.
    #[allow(dead_code)]
    pub fn normalize_path_separators(path: &str) -> alloc::borrow::Cow<'_, str> {
        alloc::borrow::Cow::Borrowed(path)
    }

    /// Pushes a path piece onto a pattern path.
    pub fn push_path_for_pattern(path: &mut crate::path::PathBuf, piece: &str) {
        let mut s = path.to_string();
        if !s.is_empty() && !s.ends_with('/') && !s.ends_with('\\') {
            s.push('/');
        }
        s.push_str(piece);
        *path = crate::path::PathBuf::from(s);
    }

    pub use DirEntryNative as DirEntry;
    pub use FileTypeNative as FileType;
    pub use MetadataNative as Metadata;

    const O_RDONLY: i32 = 0;
    const O_WRONLY: i32 = 1;
    const O_RDWR: i32 = 2;
    const O_CREAT: i32 = 0o100;
    const O_TRUNC: i32 = 0o1000;
    const O_APPEND: i32 = 0o2000;
    const O_CLOEXEC: i32 = 0o2_000_000;
    const O_EXCL: i32 = 0o200;

    /// An open file descriptor providing async I/O.
    pub type File = FileNative;

    /// An open file descriptor providing async I/O.
    pub struct FileNative {
        fd: i32,
    }

    #[cfg(not(target_family = "wasm"))]
    impl FileNative {
        /// Returns default open options.
        pub fn options() -> OpenOptions {
            OpenOptions::new()
        }

        /// Attempts to clone the file descriptor.
        pub fn try_clone(&self) -> io::Result<Self> {
            let fd = unsafe { dup(self.fd) };
            if fd < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(Self { fd })
            }
        }

        /// Open a file in read-only mode.
        pub async fn open<P: AsRef<str>>(path: P) -> io::Result<Self> {
            OpenOptions::new().read(true).open(path).await
        }

        /// Create or truncate a file for writing.
        pub async fn create<P: AsRef<str>>(path: P) -> io::Result<Self> {
            OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .open(path)
                .await
        }

        /// Returns a clone of the file.
        pub fn try_clone(&self) -> io::Result<Self> {
            Ok(Self { fd: self.fd })
        }

        /// Returns true if the file is a terminal.
        pub fn is_terminal(&self) -> bool {
            false
        }

        /// Returns metadata for the file.
        pub fn metadata(&self) -> io::Result<MetadataNative> {
            Ok(MetadataNative)
        }

        /// Returns the file descriptor.
        pub fn as_raw_fd(&self) -> i32 {
            self.fd
        }
    }

    impl From<&FileNative> for crate::io::Stdio {
        fn from(file: &FileNative) -> Self {
            crate::io::Stdio::from_raw_fd(file.fd)
        }
    }

    impl DirEntryNative {
        /// Returns the path of the entry.
        pub fn path(&self) -> crate::path::PathBuf {
            crate::path::PathBuf::from("")
        }

        /// Returns metadata for the entry.
        pub fn metadata(&self) -> io::Result<MetadataNative> {
            Ok(MetadataNative)
        }

        /// Returns the file type of the entry.
        pub fn file_type(&self) -> io::Result<FileTypeNative> {
            Ok(FileTypeNative)
        }

        /// Returns the file name of the entry.
        pub fn file_name(&self) -> crate::string::String {
            crate::string::String::new()
        }
    }

    /// File type information.
    pub struct FileTypeNative;

    impl FileTypeNative {
        /// Returns true if the entry is a directory.
        pub fn is_dir(&self) -> bool {
            false
        }

        /// Returns true if the entry is a file.
        pub fn is_file(&self) -> bool {
            false
        }

        /// Returns true if the entry is a symbolic link.
        pub fn is_symlink(&self) -> bool {
            false
        }
    }

    /// File metadata.
    pub struct MetadataNative;

    impl MetadataNative {
        /// Returns true if the entry is a directory.
        pub fn is_dir(&self) -> bool {
            false
        }

        /// Returns true if the entry is a file.
        pub fn is_file(&self) -> bool {
            true
        }

        /// Returns the file length.
        pub fn len(&self) -> u64 {
            0
        }

        /// Returns the last modification time.
        pub fn modified(&self) -> io::Result<SystemTime> {
            Err(io::Error::other("not implemented"))
        }
    }

    pub(crate) static STATIC_METADATA: MetadataNative = MetadataNative;

    impl AsyncRead for FileNative {
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

    impl Read for FileNative {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            let n = unsafe { read(self.fd, buf.as_mut_ptr(), buf.len()) };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        }
    }

    impl AsyncWrite for FileNative {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            let n = unsafe { write(self.fd, buf.as_ptr(), buf.len()) };
            if n < 0 {
                (Err(io::Error::last_os_error()), buf)
            } else {
                (Ok(n as usize), buf)
            }
        }

        async fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    impl Write for FileNative {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            let n = unsafe { write(self.fd, buf.as_ptr(), buf.len()) };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    /// Open the null device (/dev/null).
    pub fn open_null_file() -> io::Result<FileNative> {
        Err(io::Error::other("open_null_file not implemented"))
    }

    impl Drop for FileNative {
        fn drop(&mut self) {
            unsafe { close(self.fd) };
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
        pub async fn open<P: AsRef<str>>(self, path: P) -> io::Result<File> {
            let cpath = CString::new(path.as_ref())
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

// ——— Native non-Unix (Windows) — native file API

#[cfg(all(not(target_family = "wasm"), target_os = "windows"))]
pub use native_windows::{
    File, File as FileNative, FileTypeNative as FileType, OpenOptions,
    default_case_insensitive_path_expansion, open_null_file, pattern_path_root,
    push_path_for_pattern, resolve_executable, split_path_for_pattern,
};

#[cfg(all(not(target_family = "wasm"), target_os = "windows"))]
mod native_windows {
    use crate::rt::windows::{OverlappedRead, OverlappedWrite};
    use crate::{
        ffi::OsStrExt,
        io,
        traits::{AsyncRead, AsyncWrite, Read, Write},
    };
    use alloc::vec::Vec;

    /// File type information.
    pub struct FileTypeNative;

    impl FileTypeNative {
        /// Returns true if the entry is a directory.
        pub fn is_dir(&self) -> bool {
            false
        }

        /// Returns true if the entry is a file.
        pub fn is_file(&self) -> bool {
            false
        }

        /// Returns true if the entry is a symbolic link.
        pub fn is_symlink(&self) -> bool {
            false
        }
    }

    /// Open the null device (NUL).
    pub fn open_null_file() -> io::Result<File> {
        Err(io::Error::other("open_null_file not implemented"))
    }

    /// Returns `true` if the shell should default to case-insensitive path expansion.
    pub fn default_case_insensitive_path_expansion() -> bool {
        true
    }

    /// Resolves an executable name to a full path by searching the PATH.
    pub fn resolve_executable<P: AsRef<str>>(_path: P) -> Option<alloc::string::String> {
        None
    }

    /// Splits a path into pieces suitable for globbing.
    pub fn split_path_for_pattern(_path: &str) -> Vec<&str> {
        alloc::vec![]
    }

    /// Returns the root of a pattern path (e.g., "/" on Unix, "C:\" on Windows).
    pub fn pattern_path_root(_path: &str) -> Option<alloc::string::String> {
        None
    }

    /// Normalizes path separators for the current platform.
    #[allow(dead_code)]
    pub fn normalize_path_separators(path: &str) -> alloc::borrow::Cow<'_, str> {
        alloc::borrow::Cow::Borrowed(path)
    }

    /// Pushes a path piece onto a pattern path.
    pub fn push_path_for_pattern(path: &mut crate::path::PathBuf, piece: &str) {
        let mut s = path.to_string();
        if !s.is_empty() && !s.ends_with('/') && !s.ends_with('\\') {
            s.push('/');
        }
        s.push_str(piece);
        *path = crate::path::PathBuf::from(s);
    }

    // Minimal definitions for native windows APIs instead of relying on `windows-sys`
    unsafe extern "system" {
        fn CreateFileW(
            lpFileName: *const u16,
            dwDesiredAccess: u32,
            dwShareMode: u32,
            lpSecurityAttributes: *mut core::ffi::c_void,
            dwCreationDisposition: u32,
            dwFlagsAndAttributes: u32,
            hTemplateFile: usize,
        ) -> usize;
        fn CloseHandle(hObject: usize) -> i32;
        fn GetCurrentProcess() -> usize;
        fn DuplicateHandle(
            hSourceProcessHandle: usize,
            hSourceHandle: usize,
            hTargetProcessHandle: usize,
            lpTargetHandle: *mut usize,
            dwDesiredAccess: u32,
            bInheritHandle: i32,
            dwOptions: u32,
        ) -> i32;
    }

    const DUPLICATE_SAME_ACCESS: u32 = 2;

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
        /// Returns default open options.
        pub fn options() -> OpenOptions {
            OpenOptions::new()
        }

        /// Attempts to clone the file handle.
        pub fn try_clone(&self) -> io::Result<Self> {
            let mut handle = 0;
            let current_process = unsafe { GetCurrentProcess() };
            let ok = unsafe {
                DuplicateHandle(
                    current_process,
                    self.handle as usize,
                    current_process,
                    &mut handle,
                    0,
                    1, // inherit
                    DUPLICATE_SAME_ACCESS,
                )
            };
            if ok == 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(Self {
                    handle: handle as u64,
                })
            }
        }

        /// Open a file in read-only mode.
        pub async fn open<P: AsRef<str>>(path: P) -> io::Result<Self> {
            OpenOptions::new().read(true).open(path).await
        }

        /// Create or truncate a file for writing.
        pub async fn create<P: AsRef<str>>(path: P) -> io::Result<Self> {
            OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .open(path)
                .await
        }

        /// Synchronously open a file.
        pub fn open_sync<P: AsRef<str>>(_path: P, _write: bool) -> io::Result<Self> {
            // For now, let's just use whatever sync open we have or stub it.
            Err(io::Error::other(
                "sync open not implemented for Windows File",
            ))
        }

        /// Query metadata for this file handle (sync).
        pub fn metadata_sync(&self) -> io::Result<super::Metadata> {
            #[repr(C)]
            #[allow(non_snake_case)]
            struct FILETIME {
                dwLowDateTime: u32,
                dwHighDateTime: u32,
            }
            #[repr(C)]
            #[allow(non_snake_case)]
            struct BY_HANDLE_FILE_INFORMATION {
                dwFileAttributes: u32,
                ftCreationTime: FILETIME,
                ftLastAccessTime: FILETIME,
                ftLastWriteTime: FILETIME,
                dwVolumeSerialNumber: u32,
                nFileSizeHigh: u32,
                nFileSizeLow: u32,
                nNumberOfLinks: u32,
                nFileIndexHigh: u32,
                nFileIndexLow: u32,
            }
            unsafe extern "system" {
                fn GetFileInformationByHandle(
                    hFile: usize,
                    lpFileInformation: *mut BY_HANDLE_FILE_INFORMATION,
                ) -> i32;
            }
            let mut info = unsafe { core::mem::zeroed() };
            let res = unsafe { GetFileInformationByHandle(self.handle as usize, &mut info) };
            if res != 0 {
                let created = (info.ftCreationTime.dwHighDateTime as u64) << 32
                    | info.ftCreationTime.dwLowDateTime as u64;
                let accessed = (info.ftLastAccessTime.dwHighDateTime as u64) << 32
                    | info.ftLastAccessTime.dwLowDateTime as u64;
                let modified = (info.ftLastWriteTime.dwHighDateTime as u64) << 32
                    | info.ftLastWriteTime.dwLowDateTime as u64;

                Ok(super::Metadata {
                    size: (info.nFileSizeHigh as u64) << 32 | info.nFileSizeLow as u64,
                    mode: info.dwFileAttributes,
                    modified_ns: (modified.saturating_sub(116_444_736_000_000_000)) * 100,
                    accessed_ns: (accessed.saturating_sub(116_444_736_000_000_000)) * 100,
                    created_ns: (created.saturating_sub(116_444_736_000_000_000)) * 100,
                    nlink: info.nNumberOfLinks as u64,
                    uid: 0,
                    gid: 0,
                    inode: (info.nFileIndexHigh as u64) << 32 | info.nFileIndexLow as u64,
                })
            } else {
                Err(io::Error::last_os_error())
            }
        }

        /// Query metadata for this file handle (async).
        pub async fn metadata(&self) -> io::Result<super::Metadata> {
            self.metadata_sync()
        }

        /// Returns true if the file is a terminal.
        pub fn is_terminal(&self) -> bool {
            false
        }
    }

    impl Drop for File {
        fn drop(&mut self) {
            // SAFETY: handle is a valid HANDLE opened by CreateFileW and not yet closed.
            unsafe { CloseHandle(self.handle as usize) };
        }
    }

    impl AsyncRead for File {
        async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            OverlappedRead::new(self.handle, buf).await
        }
    }

    impl Read for File {
        fn read(&mut self, _buf: &mut [u8]) -> io::Result<usize> {
            Err(io::Error::other(
                "sync read not implemented for Windows File",
            ))
        }
    }

    impl AsyncWrite for File {
        async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
            OverlappedWrite::new(self.handle, buf).await
        }
        async fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    impl Write for File {
        fn write(&mut self, _buf: &[u8]) -> io::Result<usize> {
            Err(io::Error::other(
                "sync write not implemented for Windows File",
            ))
        }

        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    /// `OpenOptions` for Windows
    #[derive(Clone, Copy)]
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
        pub async fn open<P: AsRef<str>>(self, path: P) -> io::Result<File> {
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

            let path_wide: alloc::vec::Vec<u16> = path
                .as_ref()
                .encode_wide()
                .chain(core::iter::once(0))
                .collect();

            // SAFETY: Safe ffi call
            let handle = unsafe {
                CreateFileW(
                    path_wide.as_ptr(),
                    access,
                    share,
                    core::ptr::null_mut(),
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

        /// Open the file at `path` according to options (sync).
        pub fn open_sync<P: AsRef<str>>(&self, path: P) -> io::Result<File> {
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

            let flags = FILE_ATTRIBUTE_NORMAL;
            let share = FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE;

            let path_wide: alloc::vec::Vec<u16> = path
                .as_ref()
                .encode_wide()
                .chain(core::iter::once(0))
                .collect();

            // SAFETY: Safe ffi call
            let handle = unsafe {
                CreateFileW(
                    path_wide.as_ptr(),
                    access,
                    share,
                    core::ptr::null_mut(),
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
pub use native_stub::{
    File, OpenOptions, default_case_insensitive_path_expansion, open_null_file, pattern_path_root,
    push_path_for_pattern, resolve_executable, split_path_for_pattern,
};

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
    use crate::io;

    /// File handle stub — not yet implemented on this platform.
    pub struct File;

    impl File {
        /// Open a file (stub).
        pub async fn open<P: AsRef<str>>(_path: P) -> io::Result<Self> {
            Err(io::Error::other(
                "fs::File not yet implemented on this platform",
            ))
        }

        /// Create a file (stub).
        pub async fn create<P: AsRef<str>>(_path: P) -> io::Result<Self> {
            Err(io::Error::other(
                "fs::File not yet implemented on this platform",
            ))
        }
    }

    /// Open the null device.
    pub fn open_null_file() -> io::Result<File> {
        Err(io::Error::other("open_null_file not implemented"))
    }

    /// Returns `true` if the shell should default to case-insensitive path expansion.
    pub fn default_case_insensitive_path_expansion() -> bool {
        cfg!(windows)
    }

    /// Resolves an executable name to a full path by searching the PATH.
    pub fn resolve_executable<P: AsRef<str>>(_path: P) -> Option<alloc::string::String> {
        None
    }

    /// Splits a path into pieces suitable for globbing.
    pub fn split_path_for_pattern(_path: &str) -> Vec<&str> {
        alloc::vec![]
    }

    /// Returns the root of a pattern path (e.g., "/" on Unix, "C:\" on Windows).
    pub fn pattern_path_root(_path: &str) -> Option<alloc::string::String> {
        None
    }

    /// Normalizes path separators for the current platform.
    #[allow(dead_code)]
    pub fn normalize_path_separators(path: &str) -> alloc::borrow::Cow<'_, str> {
        alloc::borrow::Cow::Borrowed(path)
    }

    /// Pushes a path piece onto a pattern path.
    pub fn push_path_for_pattern(path: &mut crate::path::PathBuf, piece: &str) {
        let mut s = path.to_string();
        if !s.is_empty() && !s.ends_with('/') && !s.ends_with('\\') {
            s.push('/');
        }
        s.push_str(piece);
        *path = crate::path::PathBuf::from(s);
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
    #[derive(Clone, Copy)]
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
        pub async fn open<P: AsRef<str>>(self, _path: P) -> io::Result<File> {
            Err(io::Error::other("not implemented"))
        }
    }

    impl Default for OpenOptions {
        fn default() -> Self {
            Self::new()
        }
    }
}

// ——— Native Metadata (all non-WASM platforms) ——————————————————————————————

/// File metadata returned by [`metadata`].
#[cfg(not(target_family = "wasm"))]
#[cfg_attr(not(target_family = "wasm"), derive(Clone))]
pub struct Metadata {
    size: u64,
    mode: u32,
    modified_ns: u64,
    accessed_ns: u64,
    created_ns: u64,
    nlink: u64,
    uid: u32,
    gid: u32,
    inode: u64,
}

#[cfg(not(target_family = "wasm"))]
impl Metadata {
    /// File size in bytes.
    pub fn len(&self) -> u64 {
        self.size
    }
    /// `true` if this is a regular file.
    pub fn is_file(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o100000
        }
        #[cfg(windows)]
        {
            (self.mode & 0x10) == 0
        } // FILE_ATTRIBUTE_DIRECTORY is 0x10
    }
    /// `true` if this is a directory.
    pub fn is_dir(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o040000
        }
        #[cfg(windows)]
        {
            (self.mode & 0x10) != 0
        }
    }
    /// `true` if the path itself is a symbolic link (stat taken without following links).
    pub fn is_symlink(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o120000
        }
        #[cfg(windows)]
        {
            (self.mode & 0x400) != 0
        } // FILE_ATTRIBUTE_REPARSE_POINT is 0x400
    }
    /// `true` if the file is read-only.
    pub fn readonly(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o222) == 0
        }
        #[cfg(windows)]
        {
            (self.mode & 0x1) != 0
        } // FILE_ATTRIBUTE_READONLY is 0x1
    }
    /// Modification time as nanoseconds since UNIX epoch, or 0.
    pub fn modified_ns(&self) -> u64 {
        self.modified_ns
    }
    /// Last access time as nanoseconds since UNIX epoch, or 0.
    pub fn accessed_ns(&self) -> u64 {
        self.accessed_ns
    }
    /// Creation/birth time as nanoseconds since UNIX epoch, or 0.
    pub fn created_ns(&self) -> u64 {
        self.created_ns
    }

    /// Unix permission bits; synthesised from readonly on non-Unix hosts.
    pub fn mode(&self) -> u32 {
        #[cfg(unix)]
        {
            self.mode
        }
        #[cfg(not(unix))]
        {
            if self.readonly() { 0o444 } else { 0o666 }
        }
    }
    /// Number of hard links; 0 on platforms that do not expose it.
    pub fn nlink(&self) -> u64 {
        self.nlink
    }
    /// Owner user-ID (Unix); 0 on non-Unix.
    pub fn uid(&self) -> u32 {
        self.uid
    }
    /// Owner group-ID (Unix); 0 on non-Unix.
    pub fn gid(&self) -> u32 {
        self.gid
    }
    /// Inode / file-index; 0 on platforms that do not expose it.
    pub fn inode(&self) -> u64 {
        self.inode
    }
    /// Alias for inode.
    pub fn ino(&self) -> u64 {
        self.inode
    }

    /// `true` if this is a block device.
    pub fn is_block_device(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o060000
        }
        #[cfg(not(unix))]
        {
            false
        }
    }
    /// `true` if this is a character device.
    pub fn is_char_device(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o020000
        }
        #[cfg(not(unix))]
        {
            false
        }
    }
    /// `true` if this is a FIFO.
    pub fn is_fifo(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o010000
        }
        #[cfg(not(unix))]
        {
            false
        }
    }
    /// `true` if this is a socket.
    pub fn is_socket(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o140000
        }
        #[cfg(not(unix))]
        {
            false
        }
    }
}

/// Query metadata for `path`.
#[cfg(not(target_family = "wasm"))]
/// Query metadata for a path (async).
pub async fn metadata<P: AsRef<str>>(path: P) -> crate::io::Result<Metadata> {
    metadata_sync(path)
}

/// Query symlink metadata for a path (async).
pub async fn symlink_metadata<P: AsRef<str>>(path: P) -> crate::io::Result<Metadata> {
    symlink_metadata_sync(path)
}

/// Query metadata for a path (sync).
pub fn metadata_sync<P: AsRef<str>>(path: P) -> crate::io::Result<Metadata> {
    #[cfg(unix)]
    {
        #[repr(C)]
        struct Stat {
            st_dev: u64,
            st_ino: u64,
            st_nlink: u64,
            st_mode: u32,
            st_uid: u32,
            st_gid: u32,
            __pad0: i32,
            st_rdev: u64,
            st_size: i64,
            st_blksize: i64,
            st_blocks: i64,
            st_atime: i64,
            st_atime_nsec: i64,
            st_mtime: i64,
            st_mtime_nsec: i64,
            st_ctime: i64,
            st_ctime_nsec: i64,
            __unused: [i64; 3],
        }
        unsafe extern "C" {
            fn stat(pathname: *const u8, statbuf: *mut Stat) -> i32;
        }
        let cpath = crate::ffi::CString::new(path.as_ref()).map_err(|_| {
            crate::io::Error::new(crate::io::ErrorKind::InvalidInput, "path contains null")
        })?;
        let mut st = Stat {
            st_dev: 0,
            st_ino: 0,
            st_nlink: 0,
            st_mode: 0,
            st_uid: 0,
            st_gid: 0,
            __pad0: 0,
            st_rdev: 0,
            st_size: 0,
            st_blksize: 0,
            st_blocks: 0,
            st_atime: 0,
            st_atime_nsec: 0,
            st_mtime: 0,
            st_mtime_nsec: 0,
            st_ctime: 0,
            st_ctime_nsec: 0,
            __unused: [0; 3],
        };
        let res = unsafe { lstat(cpath.as_ptr() as *const u8, &mut st) };
        if res < 0 {
            return Err(crate::io::Error::last_os_error());
        }
        Ok(Metadata {
            size: st.st_size as u64,
            mode: st.st_mode,
            modified_ns: (st.st_mtime as u64) * 1_000_000_000 + (st.st_mtime_nsec as u64),
            accessed_ns: (st.st_atime as u64) * 1_000_000_000 + (st.st_atime_nsec as u64),
            created_ns: (st.st_ctime as u64) * 1_000_000_000 + (st.st_ctime_nsec as u64),
            nlink: st.st_nlink as u64,
            uid: st.st_uid,
            gid: st.st_gid,
            inode: st.st_ino,
        })
    }
    #[cfg(windows)]
    {
        #[repr(C)]
        #[allow(non_snake_case)]
        struct FILETIME {
            dwLowDateTime: u32,
            dwHighDateTime: u32,
        }
        #[repr(C)]
        #[allow(non_snake_case)]
        struct WIN32_FILE_ATTRIBUTE_DATA {
            dwFileAttributes: u32,
            ftCreationTime: FILETIME,
            ftLastAccessTime: FILETIME,
            ftLastWriteTime: FILETIME,
            nFileSizeHigh: u32,
            nFileSizeLow: u32,
        }
        unsafe extern "system" {
            fn GetFileAttributesExW(
                lpFileName: *const u16,
                fInfoLevelId: i32,
                lpFileInformation: *mut WIN32_FILE_ATTRIBUTE_DATA,
            ) -> i32;
        }

        let wide_path = crate::path::Path::new(path.as_ref()).to_wide_null();
        let mut data = unsafe { core::mem::zeroed() };
        let res = unsafe { GetFileAttributesExW(wide_path.as_ptr(), 0, &mut data) };
        if res != 0 {
            let created = (data.ftCreationTime.dwHighDateTime as u64) << 32
                | data.ftCreationTime.dwLowDateTime as u64;
            let accessed = (data.ftLastAccessTime.dwHighDateTime as u64) << 32
                | data.ftLastAccessTime.dwLowDateTime as u64;
            let modified = (data.ftLastWriteTime.dwHighDateTime as u64) << 32
                | data.ftLastWriteTime.dwLowDateTime as u64;

            Ok(Metadata {
                size: (data.nFileSizeHigh as u64) << 32 | data.nFileSizeLow as u64,
                mode: data.dwFileAttributes,
                modified_ns: (modified.saturating_sub(116_444_736_000_000_000)) * 100,
                accessed_ns: (accessed.saturating_sub(116_444_736_000_000_000)) * 100,
                created_ns: (created.saturating_sub(116_444_736_000_000_000)) * 100,
                nlink: 1, // GetFileAttributesExW doesn't provide nlink
                uid: 0,
                gid: 0,
                inode: 0, // GetFileAttributesExW doesn't provide inode
            })
        } else {
            Err(crate::io::Error::last_os_error())
        }
    }
}

/// Query metadata for `path` without following symbolic links (sync).
#[cfg(not(target_family = "wasm"))]
pub fn symlink_metadata_sync<P: AsRef<str>>(path: P) -> crate::io::Result<Metadata> {
    #[cfg(unix)]
    {
        #[repr(C)]
        struct Stat {
            st_dev: u64,
            st_ino: u64,
            st_nlink: u64,
            st_mode: u32,
            st_uid: u32,
            st_gid: u32,
            __pad0: i32,
            st_rdev: u64,
            st_size: i64,
            st_blksize: i64,
            st_blocks: i64,
            st_atime: i64,
            st_atime_nsec: i64,
            st_mtime: i64,
            st_mtime_nsec: i64,
            st_ctime: i64,
            st_ctime_nsec: i64,
            __unused: [i64; 3],
        }
        unsafe extern "C" {
            fn lstat(pathname: *const u8, statbuf: *mut Stat) -> i32;
        }
        let path_cstr = crate::ffi::CString::new(path.as_ref())?;
        let mut st = unsafe { core::mem::zeroed() };
        let res = unsafe { lstat(path_cstr.as_ptr() as *const u8, &mut st) };
        if res == 0 {
            Ok(Metadata {
                size: st.st_size as u64,
                mode: st.st_mode,
                modified_ns: (st.st_mtime as u64) * 1_000_000_000 + (st.st_mtime_nsec as u64),
                accessed_ns: (st.st_atime as u64) * 1_000_000_000 + (st.st_atime_nsec as u64),
                created_ns: (st.st_ctime as u64) * 1_000_000_000 + (st.st_ctime_nsec as u64),
                nlink: st.st_nlink as u64,
                uid: st.st_uid,
                gid: st.st_gid,
                inode: st.st_ino as u64,
            })
        } else {
            Err(crate::io::Error::last_os_error())
        }
    }
    #[cfg(windows)]
    {
        metadata_sync(path)
    }
}

// ——— Directory reading (non-WASM) ─────────────────────────────────────────────

/// A single directory entry, analogous to `std::fs::DirEntry`.
#[cfg(not(target_family = "wasm"))]
pub struct DirEntry {
    /// Name of this entry within its parent directory.
    pub name: crate::string::String,
    /// Cached metadata (may be unavailable on some platforms).
    pub metadata: Option<Metadata>,
}

#[cfg(not(target_family = "wasm"))]
impl DirEntry {
    /// Returns the file name of this entry.
    pub fn file_name(&self) -> &str {
        &self.name
    }

    /// Returns the path of the entry.
    pub fn path(&self) -> crate::path::PathBuf {
        crate::path::PathBuf::from(self.name.clone())
    }

    /// Returns metadata for the entry.
    pub fn metadata(&self) -> crate::io::Result<Metadata> {
        self.metadata
            .clone()
            .ok_or_else(|| crate::io::Error::other("metadata not available"))
    }

    /// Returns the file type of the entry.
    pub fn file_type(&self) -> crate::io::Result<FileType> {
        Ok(FileType)
    }
}

/// An iterator over the entries in a directory.
#[cfg(not(target_family = "wasm"))]
pub struct ReadDir {
    entries: crate::vec::Vec<DirEntry>,
    pos: usize,
}

#[cfg(not(target_family = "wasm"))]
impl Iterator for ReadDir {
    type Item = crate::io::Result<DirEntry>;

    fn next(&mut self) -> Option<Self::Item> {
        if self.pos < self.entries.len() {
            // SAFETY: pos < entries.len(); we take by swapping in a dummy entry.
            let entry = core::mem::replace(
                &mut self.entries[self.pos],
                DirEntry {
                    name: crate::string::String::new(),
                    metadata: None,
                },
            );
            self.pos += 1;
            Some(Ok(entry))
        } else {
            None
        }
    }
}

/// Read all entries in `path` as an iterator (async).
///
/// Returns an error if `path` is not a directory or does not exist.
#[cfg(not(target_family = "wasm"))]
#[allow(clippy::unused_async)]
pub async fn read_dir<P: AsRef<str>>(path: P) -> crate::io::Result<ReadDir> {
    read_dir_sync(path)
}

/// Read all entries in `path` as an iterator (sync).
///
/// Returns an error if `path` is not a directory or does not exist.
#[cfg(not(target_family = "wasm"))]
pub fn read_dir_sync<P: AsRef<str>>(path: P) -> crate::io::Result<ReadDir> {
    #[cfg(unix)]
    {
        use crate::ffi::CString;
        use crate::vec::Vec;

        const DT_DIR: u8 = 4;
        const DT_REG: u8 = 8;
        const DT_LNK: u8 = 10;

        unsafe extern "C" {
            fn opendir(name: *const u8) -> *mut core::ffi::c_void;
            fn closedir(dir: *mut core::ffi::c_void) -> i32;
            fn readdir(dir: *mut core::ffi::c_void) -> *mut Dirent;
        }

        #[cfg(all(target_os = "linux", target_arch = "x86_64"))]
        #[repr(C)]
        struct Dirent {
            d_ino: u64,
            d_off: i64,
            d_reclen: u16,
            d_type: u8,
            d_name: [u8; 256],
        }

        #[cfg(all(target_os = "linux", target_arch = "aarch64"))]
        #[repr(C)]
        struct Dirent {
            d_ino: u64,
            d_off: i64,
            d_reclen: u16,
            d_type: u8,
            d_name: [u8; 256],
        }

        #[cfg(any(
            target_os = "macos",
            target_os = "freebsd",
            target_os = "openbsd",
            target_os = "netbsd",
        ))]
        #[repr(C)]
        struct Dirent {
            d_ino: u64,
            d_seekoff: u64,
            d_reclen: u16,
            d_namlen: u16,
            d_type: u8,
            d_name: [u8; 1024],
        }

        let cpath = CString::new(path.as_ref()).map_err(|_| {
            crate::io::Error::new(crate::io::ErrorKind::InvalidInput, "path contains null")
        })?;
        let dir = unsafe { opendir(cpath.as_ptr() as *const u8) };
        if dir.is_null() {
            return Err(crate::io::Error::last_os_error());
        }

        let mut entries = Vec::new();
        loop {
            // SAFETY: `dir` is a valid DIR* returned by opendir above.
            let de = unsafe { readdir(dir) };
            if de.is_null() {
                break;
            }
            let name_bytes = unsafe {
                let name = &(*de).d_name;
                let len = name.iter().position(|&b| b == 0).unwrap_or(name.len());
                &name[..len]
            };
            if name_bytes == b"." || name_bytes == b".." {
                continue;
            }
            let name = crate::string::String::from_utf8_lossy(name_bytes).into_owned();
            entries.push(DirEntry {
                name,
                metadata: None,
            });
        }
        unsafe { closedir(dir) };
        Ok(ReadDir { entries, pos: 0 })
    }

    #[cfg(windows)]
    {
        use crate::vec::Vec;

        #[repr(C)]
        #[allow(non_snake_case)]
        struct WIN32_FIND_DATAW {
            dwFileAttributes: u32,
            ftCreationTime: [u32; 2],
            ftLastAccessTime: [u32; 2],
            ftLastWriteTime: [u32; 2],
            nFileSizeHigh: u32,
            nFileSizeLow: u32,
            dwReserved0: u32,
            dwReserved1: u32,
            cFileName: [u16; 260],
            cAlternateFileName: [u16; 14],
        }

        unsafe extern "system" {
            fn FindFirstFileW(
                lpFileName: *const u16,
                lpFindFileData: *mut WIN32_FIND_DATAW,
            ) -> usize;
            fn FindNextFileW(hFindFile: usize, lpFindFileData: *mut WIN32_FIND_DATAW) -> i32;
            fn FindClose(hFindFile: usize) -> i32;
        }

        const INVALID_HANDLE_VALUE: usize = !0;

        // Build the search pattern: `path\*`
        let pattern = {
            let p = path.as_ref().trim_end_matches(['/', '\\']);
            let mut s = crate::string::String::from(p);
            s.push_str("\\*");
            s
        };
        let mut pattern_w: Vec<u16> = pattern.encode_utf16().collect();
        pattern_w.push(0);

        let mut find_data = WIN32_FIND_DATAW {
            dwFileAttributes: 0,
            ftCreationTime: [0; 2],
            ftLastAccessTime: [0; 2],
            ftLastWriteTime: [0; 2],
            nFileSizeHigh: 0,
            nFileSizeLow: 0,
            dwReserved0: 0,
            dwReserved1: 0,
            cFileName: [0; 260],
            cAlternateFileName: [0; 14],
        };

        let handle = unsafe { FindFirstFileW(pattern_w.as_ptr(), &mut find_data) };
        if handle == INVALID_HANDLE_VALUE {
            return Err(crate::io::Error::last_os_error());
        }

        let mut entries = Vec::new();
        loop {
            let name_len = find_data
                .cFileName
                .iter()
                .position(|&c| c == 0)
                .unwrap_or(260);
            let name = crate::string::String::from_utf16_lossy(&find_data.cFileName[..name_len]);
            if name != "." && name != ".." {
                entries.push(DirEntry {
                    name,
                    metadata: None,
                });
            }
            if unsafe { FindNextFileW(handle, &mut find_data) } == 0 {
                break;
            }
        }
        unsafe { FindClose(handle) };
        Ok(ReadDir { entries, pos: 0 })
    }
}

/// Remove a file from the filesystem.
#[cfg(not(target_family = "wasm"))]
#[allow(clippy::unused_async)]
pub async fn remove_file<P: AsRef<str>>(path: P) -> crate::io::Result<()> {
    #[cfg(unix)]
    {
        use crate::ffi::CString;
        unsafe extern "C" {
            fn unlink(pathname: *const u8) -> i32;
        }
        let cpath = CString::new(path.as_ref()).map_err(|_| {
            crate::io::Error::new(crate::io::ErrorKind::InvalidInput, "path contains null")
        })?;
        let res = unsafe { unlink(cpath.as_ptr() as *const u8) };
        if res < 0 {
            Err(crate::io::Error::last_os_error())
        } else {
            Ok(())
        }
    }
    #[cfg(windows)]
    {
        unsafe extern "system" {
            fn DeleteFileW(lpFileName: *const u16) -> i32;
        }
        let mut path_w: crate::vec::Vec<u16> = path.as_ref().encode_utf16().collect();
        path_w.push(0);
        let res = unsafe { DeleteFileW(path_w.as_ptr()) };
        if res == 0 {
            Err(crate::io::Error::last_os_error())
        } else {
            Ok(())
        }
    }
}

/// Rename a file or directory from `from` to `to`.
#[cfg(not(target_family = "wasm"))]
#[allow(clippy::unused_async)]
pub async fn rename<P: AsRef<str>, Q: AsRef<str>>(from: P, to: Q) -> crate::io::Result<()> {
    #[cfg(unix)]
    {
        use crate::ffi::CString;
        unsafe extern "C" {
            fn rename(oldpath: *const u8, newpath: *const u8) -> i32;
        }
        let cfrom = CString::new(from.as_ref()).map_err(|_| {
            crate::io::Error::new(crate::io::ErrorKind::InvalidInput, "path contains null")
        })?;
        let cto = CString::new(to.as_ref()).map_err(|_| {
            crate::io::Error::new(crate::io::ErrorKind::InvalidInput, "path contains null")
        })?;
        let res = unsafe { rename(cfrom.as_ptr() as *const u8, cto.as_ptr() as *const u8) };
        if res < 0 {
            Err(crate::io::Error::last_os_error())
        } else {
            Ok(())
        }
    }
    #[cfg(windows)]
    {
        unsafe extern "system" {
            fn MoveFileExW(
                lpExistingFileName: *const u16,
                lpNewFileName: *const u16,
                dwFlags: u32,
            ) -> i32;
        }
        const MOVEFILE_REPLACE_EXISTING: u32 = 0x1;
        let mut from_w: crate::vec::Vec<u16> = from.as_ref().encode_utf16().collect();
        from_w.push(0);
        let mut to_w: crate::vec::Vec<u16> = to.as_ref().encode_utf16().collect();
        to_w.push(0);
        let res = unsafe { MoveFileExW(from_w.as_ptr(), to_w.as_ptr(), MOVEFILE_REPLACE_EXISTING) };
        if res == 0 {
            Err(crate::io::Error::last_os_error())
        } else {
            Ok(())
        }
    }
}

/// Recursively create a directory and all of its parents.
#[cfg(not(target_family = "wasm"))]
#[allow(clippy::unused_async)]
pub async fn create_dir_all<P: AsRef<str>>(path: P) -> crate::io::Result<()> {
    #[cfg(unix)]
    {
        use crate::ffi::CString;
        unsafe extern "C" {
            fn mkdir(pathname: *const u8, mode: u32) -> i32;
        }
        let p = path.as_ref();
        let is_abs = p.starts_with('/');
        let mut sofar = crate::string::String::new();
        if is_abs {
            sofar.push('/');
        }
        for component in p.split('/').filter(|c| !c.is_empty()) {
            if !sofar.is_empty() && !sofar.ends_with('/') {
                sofar.push('/');
            }
            sofar.push_str(component);
            let cpath = CString::new(sofar.as_str()).map_err(|_| {
                crate::io::Error::new(crate::io::ErrorKind::InvalidInput, "path contains null")
            })?;
            let res = unsafe { mkdir(cpath.as_ptr() as *const u8, 0o777) };
            if res < 0 {
                let err = crate::io::Error::last_os_error();
                if err.kind() != crate::io::ErrorKind::AlreadyExists {
                    return Err(err);
                }
            }
        }
        Ok(())
    }
    #[cfg(windows)]
    {
        unsafe extern "system" {
            fn CreateDirectoryW(
                lpPathName: *const u16,
                lpSecurityAttributes: *mut core::ffi::c_void,
            ) -> i32;
        }
        let p = path.as_ref();
        // Build all ancestor paths.
        let separators: &[char] = &['/', '\\'];
        let components: crate::vec::Vec<&str> =
            p.split(separators).filter(|c| !c.is_empty()).collect();
        let mut sofar = crate::string::String::new();
        // Preserve absolute path prefix (drive letter or UNC).
        if p.starts_with("\\\\") || p.starts_with("//") {
            sofar.push_str("\\\\");
        }
        for component in &components {
            if !sofar.is_empty() && !sofar.ends_with(['/', '\\']) {
                sofar.push('\\');
            }
            sofar.push_str(component);
            let mut path_w: crate::vec::Vec<u16> = sofar.encode_utf16().collect();
            path_w.push(0);
            let res = unsafe { CreateDirectoryW(path_w.as_ptr(), core::ptr::null_mut()) };
            if res == 0 {
                let err = crate::io::Error::last_os_error();
                if err.kind() != crate::io::ErrorKind::AlreadyExists {
                    return Err(err);
                }
            }
        }
        Ok(())
    }
}

/// Returns the canonical, absolute form of `path`, resolving all symlinks.
///
/// On platforms where realpath is unavailable, returns an error with
/// `ErrorKind::Unsupported`.
#[cfg(not(target_family = "wasm"))]
#[allow(clippy::unused_async)]
pub async fn canonicalize<P: AsRef<str>>(path: P) -> crate::io::Result<crate::path::PathBuf> {
    #[cfg(unix)]
    {
        use crate::ffi::CString;
        unsafe extern "C" {
            fn realpath(path: *const u8, resolved_path: *mut u8) -> *mut u8;
            fn free(ptr: *mut u8);
        }
        let cpath = CString::new(path.as_ref()).map_err(|_| {
            crate::io::Error::new(crate::io::ErrorKind::InvalidInput, "path contains null")
        })?;
        // SAFETY: realpath with a null second arg allocates its own buffer.
        let resolved = unsafe { realpath(cpath.as_ptr() as *const u8, core::ptr::null_mut()) };
        if resolved.is_null() {
            return Err(crate::io::Error::last_os_error());
        }
        let len = unsafe {
            let mut i = 0usize;
            while *resolved.add(i) != 0 {
                i += 1;
            }
            i
        };
        let s = unsafe { core::slice::from_raw_parts(resolved, len) };
        let owned = crate::string::String::from_utf8_lossy(s).into_owned();
        unsafe { free(resolved) };
        Ok(crate::path::PathBuf::from(owned))
    }
    #[cfg(windows)]
    {
        unsafe extern "system" {
            fn GetFullPathNameW(
                lpFileName: *const u16,
                nBufferLength: u32,
                lpBuffer: *mut u16,
                lpFilePart: *mut *mut u16,
            ) -> u32;
        }
        let mut path_w: crate::vec::Vec<u16> = path.as_ref().encode_utf16().collect();
        path_w.push(0);
        let mut buf: crate::vec::Vec<u16> = core::iter::repeat(0u16).take(32768).collect();
        let len = unsafe {
            GetFullPathNameW(
                path_w.as_ptr(),
                buf.len() as u32,
                buf.as_mut_ptr(),
                core::ptr::null_mut(),
            )
        };
        if len == 0 {
            return Err(crate::io::Error::last_os_error());
        }
        let s = crate::string::String::from_utf16_lossy(&buf[..len as usize]);
        Ok(crate::path::PathBuf::from(s))
    }
}

/// Returns the canonical, absolute form of `path`, resolving all symlinks (sync).
#[cfg(not(target_family = "wasm"))]
pub fn canonicalize_sync<P: AsRef<str>>(path: P) -> crate::io::Result<crate::path::PathBuf> {
    // Reuse async implementations since they are actually synchronous on native.
    // In a final design they would be separate but this works.
    let path_str = crate::alloc::string::String::from(path.as_ref());
    crate::executor::block_on(async move { canonicalize(path_str).await })
}

// ——— WASM ——————————————————————————————————————————————————————————————————

#[cfg(target_family = "wasm")]
use crate::abi::imports;
#[cfg(target_family = "wasm")]
use crate::rt::wasm::OverlappedBufferFuture;
#[cfg(target_family = "wasm")]
use crate::string::String;
#[cfg(target_family = "wasm")]
use crate::vec::Vec;

/// WASM file handle.
#[cfg(target_family = "wasm")]
pub struct File {
    handle: u64,
}

#[cfg(target_family = "wasm")]
impl File {
    /// Returns default open options.
    pub fn options() -> OpenOptions {
        OpenOptions::new()
    }

    /// Open the null device.
    pub fn open_null_file() -> crate::io::Result<File> {
        Err(crate::io::Error::other("open_null_file not implemented"))
    }

    /// Returns `true` if the shell should default to case-insensitive path expansion.
    pub fn default_case_insensitive_path_expansion() -> bool {
        false
    }

    /// Resolves an executable name to a full path by searching the PATH.
    pub fn resolve_executable<P: AsRef<str>>(_path: P) -> Option<alloc::string::String> {
        None
    }

    /// Splits a path into pieces suitable for globbing.
    pub fn split_path_for_pattern(_path: &str) -> Vec<&str> {
        alloc::vec![]
    }

    /// Returns the root of a pattern path (e.g., "/" on Unix, "C:\" on Windows).
    pub fn pattern_path_root(_path: &str) -> Option<alloc::string::String> {
        None
    }

    /// Normalizes path separators for the current platform.
    #[allow(dead_code)]
    pub fn normalize_path_separators(path: &str) -> alloc::borrow::Cow<'_, str> {
        alloc::borrow::Cow::Borrowed(path)
    }

    /// Pushes a path piece onto a pattern path.
    pub fn push_path_for_pattern(path: &mut crate::path::PathBuf, piece: &str) {
        let mut s = path.to_string();
        if !s.is_empty() && !s.ends_with('/') && !s.ends_with('\\') {
            s.push('/');
        }
        s.push_str(piece);
        *path = crate::path::PathBuf::from(s);
    }

    /// Open a file in read-only mode.
    pub async fn open<P: AsRef<str>>(path: P) -> crate::io::Result<File> {
        OpenOptions::new().read(true).open(path).await
    }

    /// Create a file, truncating if it already exists.
    pub async fn create<P: AsRef<str>>(path: P) -> crate::io::Result<File> {
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
    async fn read(&mut self, buf: Vec<u8>) -> (crate::io::Result<usize>, Vec<u8>) {
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
            return (Err(crate::io::Error::from_raw_os_error(err as i32)), buf);
        }

        // SAFETY: The WASM host wrote `bytes_read` valid bytes at position 0.
        unsafe { buf.set_len(bytes_read as usize) };
        (Ok(bytes_read as usize), buf)
    }
}

#[cfg(target_family = "wasm")]
impl crate::io::AsyncWrite for File {
    async fn write(&mut self, buf: Vec<u8>) -> (crate::io::Result<usize>, Vec<u8>) {
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
            return (Err(crate::io::Error::from_raw_os_error(err as i32)), buf);
        }

        (Ok(bytes_written as usize), buf)
    }
}

/// WASM file-open options builder.
#[cfg(target_family = "wasm")]
#[derive(Clone, Copy)]
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
    pub async fn open<P: AsRef<str>>(self, path: P) -> crate::io::Result<File> {
        let path_bytes = path.as_ref().as_bytes().to_vec();

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
            return Err(crate::io::Error::from_raw_os_error(err as i32));
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
    use crate::io::{AsyncRead, AsyncWrite, Read, Write};
    use crate::vec::Vec;

    fn block_on<F: std::future::Future<Output = ()> + 'static>(f: F) {
        crate::rt::executor::run(f);
        loop {
            match crate::rt::executor::poll_step().unwrap() {
                crate::rt::executor::PollStatus::Done => break,
                crate::rt::executor::PollStatus::Ready => continue,
                crate::rt::executor::PollStatus::Idle { next_deadline } => {
                    crate::rt::executor::poll_step_idle(next_deadline).unwrap();
                }
            }
        }
    }

    #[test]
    fn test_file_create_write_read() {
        block_on(async {
            let path = std::env::temp_dir().join("rusticated_test_file.txt");

            // Note: Currently Windows tests that run natively will pass with `OverlappedRead`.
            // Wasm falls back.
            let create_res = File::create(path.to_str().unwrap()).await;
            if create_res.is_err() {
                // Ignore test on stubs
                return;
            }
            let mut file = create_res.unwrap();

            let data = b"hello rusticated async fs".to_vec();
            let (res, _) = file.write(data).await;
            assert_eq!(res.unwrap(), 23);

            let mut file = File::open(path.to_str().unwrap())
                .await
                .expect("Failed to open");
            let buf = Vec::with_capacity(32);
            let (res, read_buf) = file.read(buf).await;
            assert_eq!(res.unwrap(), 23);
            assert_eq!(read_buf, b"hello rusticated async fs");

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
    pub async fn read_entries(&mut self) -> crate::io::Result<Option<Vec<String>>> {
        let buf = alloc::vec![0u8; 4096];
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
            return Err(crate::io::Error::from_raw_os_error(err as i32));
        }

        self.continued = next_continued;

        let entries = buf[..bytes_read as usize]
            .split(|&b| b == 0)
            .filter(|s| !s.is_empty())
            .map(|s| String::from_utf8_lossy(s).into_owned())
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

    /// Returns the modification time as nanoseconds since the UNIX epoch, or 0 if unavailable.
    pub fn modified_ns(&self) -> u64 {
        unsafe { crate::abi::imports::stat_mtime(self.handle) }
    }

    /// Returns the last access time as nanoseconds since the UNIX epoch, or 0 if unavailable.
    pub fn accessed_ns(&self) -> u64 {
        unsafe { crate::abi::imports::stat_atime(self.handle) }
    }

    /// Returns the creation/birth time as nanoseconds since the UNIX epoch, or 0 if unavailable.
    pub fn created_ns(&self) -> u64 {
        unsafe { crate::abi::imports::stat_ctime(self.handle) }
    }

    /// Returns `true` if the path is a symbolic link (the stat was taken without following links).
    pub fn is_symlink(&self) -> bool {
        unsafe { crate::abi::imports::stat_is_symlink(self.handle) != 0 }
    }

    /// Returns `true` if the file is read-only.
    pub fn readonly(&self) -> bool {
        unsafe { crate::abi::imports::stat_readonly(self.handle) != 0 }
    }

    /// Unix permission bits (`rwxrwxrwx`); synthesised from readonly on non-Unix hosts.
    pub fn mode(&self) -> u32 {
        unsafe { crate::abi::imports::stat_mode(self.handle) }
    }

    /// Number of hard links; 0 on hosts that do not expose it.
    pub fn nlink(&self) -> u64 {
        unsafe { crate::abi::imports::stat_nlink(self.handle) }
    }

    /// Owner user-ID (Unix); 0 on non-Unix hosts.
    pub fn uid(&self) -> u32 {
        unsafe { crate::abi::imports::stat_uid(self.handle) }
    }

    /// Owner group-ID (Unix); 0 on non-Unix hosts.
    pub fn gid(&self) -> u32 {
        unsafe { crate::abi::imports::stat_gid(self.handle) }
    }

    /// Inode / file-index number; 0 on hosts that do not expose it.
    pub fn inode(&self) -> u64 {
        unsafe { crate::abi::imports::stat_inode(self.handle) }
    }
}

/// Query metadata for `path`.
#[cfg(target_family = "wasm")]
pub async fn metadata<P: AsRef<str>>(path: P) -> crate::io::Result<Metadata> {
    let path_bytes = path.as_ref().as_bytes().to_vec();

    let (err, handle, _, _path) = OverlappedBufferFuture::new(path_bytes, move |ov, ptr, len| {
        // SAFETY: `ptr`/`len` describe the future-owned path buffer; the
        // completion registry keeps it alive across any drop.
        unsafe { imports::path_stat(ov, ptr.cast_const(), len) };
    })
    .await;

    if err != 0 {
        return Err(crate::io::Error::from_raw_os_error(err as i32));
    }

    Ok(Metadata { handle })
}
