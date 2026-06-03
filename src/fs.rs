//! File system utilities

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

use crate::io;
use crate::traits::{AsyncRead, AsyncWrite};
use alloc::string::String;
use alloc::vec::Vec;

#[cfg(all(any(unix, rusticated_linux), target_pointer_width = "64"))]
#[repr(C)]
struct Stat {
    st_dev: u64,
    st_ino: u64,
    st_nlink: u64,
    st_mode: u32,
    st_uid: u32,
    st_gid: u32,
    __pad0: u32,
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

#[cfg(all(any(unix, rusticated_linux), not(target_pointer_width = "64")))]
compile_error!("rusticated unix metadata support currently requires a 64-bit target");

pub use File as FileNative;

// --- Metadata Struct ---

#[cfg(not(target_family = "wasm"))]
#[derive(Clone, Debug)]
/// Metadata for a filesystem entry.
pub struct Metadata {
    pub(crate) size: u64,
    pub(crate) mode: u32,
    pub(crate) modified_time_ns: u64,
    pub(crate) accessed_time_ns: u64,
    pub(crate) created_time_ns: u64,
    pub(crate) nlink: u64,
    pub(crate) uid: u32,
    pub(crate) gid: u32,
    pub(crate) inode: u64,
}

#[cfg(not(target_family = "wasm"))]
impl Metadata {
    /// Returns the size of the entry, in bytes.
    pub fn len(&self) -> u64 {
        self.size
    }

    /// Returns `true` if this metadata describes a regular file.
    pub fn is_file(&self) -> bool {
        #[cfg(any(target_family = "unix", rusticated_linux))]
        {
            (self.mode & 0o170000) == 0o100000
        }
        #[cfg(target_family = "windows")]
        {
            (self.mode & 0x10) == 0
        }
        #[cfg(not(any(target_family = "unix", target_family = "windows")))]
        {
            false
        }
    }

    /// Returns `true` if this metadata describes a directory.
    pub fn is_dir(&self) -> bool {
        #[cfg(any(target_family = "unix", rusticated_linux))]
        {
            (self.mode & 0o170000) == 0o040000
        }
        #[cfg(target_family = "windows")]
        {
            (self.mode & 0x10) != 0
        }
        #[cfg(not(any(target_family = "unix", target_family = "windows")))]
        {
            false
        }
    }

    /// Returns `true` if this metadata describes a symbolic link.
    pub fn is_symlink(&self) -> bool {
        #[cfg(any(target_family = "unix", rusticated_linux))]
        {
            (self.mode & 0o170000) == 0o120000
        }
        #[cfg(target_family = "windows")]
        {
            (self.mode & 0x400) != 0
        }
        #[cfg(not(any(target_family = "unix", target_family = "windows")))]
        {
            false
        }
    }

    /// Returns `true` if this metadata describes a block device.
    pub fn is_block_device(&self) -> bool {
        #[cfg(any(unix, rusticated_linux))]
        {
            (self.mode & 0o170000) == 0o060000
        }
        #[cfg(not(any(unix, rusticated_linux)))]
        {
            false
        }
    }

    /// Returns `true` if this metadata describes a character device.
    pub fn is_char_device(&self) -> bool {
        #[cfg(any(unix, rusticated_linux))]
        {
            (self.mode & 0o170000) == 0o020000
        }
        #[cfg(not(any(unix, rusticated_linux)))]
        {
            false
        }
    }

    /// Returns `true` if this metadata describes a FIFO.
    pub fn is_fifo(&self) -> bool {
        #[cfg(any(unix, rusticated_linux))]
        {
            (self.mode & 0o170000) == 0o010000
        }
        #[cfg(not(any(unix, rusticated_linux)))]
        {
            false
        }
    }

    /// Returns `true` if this metadata describes a socket.
    pub fn is_socket(&self) -> bool {
        #[cfg(any(unix, rusticated_linux))]
        {
            (self.mode & 0o170000) == 0o140000
        }
        #[cfg(not(any(unix, rusticated_linux)))]
        {
            false
        }
    }

    /// Returns `true` if the entry is read-only.
    pub fn readonly(&self) -> bool {
        #[cfg(any(target_family = "unix", rusticated_linux))]
        {
            (self.mode & 0o222) == 0
        }
        #[cfg(target_family = "windows")]
        {
            (self.mode & 0x1) != 0
        }
        #[cfg(not(any(target_family = "unix", target_family = "windows")))]
        {
            false
        }
    }

    /// Returns the last modified time in nanoseconds.
    pub fn modified_ns(&self) -> u64 {
        self.modified_time_ns
    }

    /// Returns the last accessed time in nanoseconds.
    pub fn accessed_ns(&self) -> u64 {
        self.accessed_time_ns
    }

    /// Returns the creation time in nanoseconds.
    pub fn created_ns(&self) -> u64 {
        self.created_time_ns
    }

    /// Returns the raw file mode bits.
    pub fn mode(&self) -> u32 {
        self.mode
    }

    /// Returns the number of hard links to the file.
    pub fn nlink(&self) -> u64 {
        self.nlink
    }

    /// Returns the owning user ID.
    pub fn uid(&self) -> u32 {
        self.uid
    }

    /// Returns the owning group ID.
    pub fn gid(&self) -> u32 {
        self.gid
    }

    /// Returns the inode number.
    pub fn inode(&self) -> u64 {
        self.inode
    }

    /// Converts the modification timestamp into a `SystemTime`.
    pub fn modified(&self) -> io::Result<crate::time::SystemTime> {
        Ok(crate::time::SystemTime::from_nanos(self.modified_time_ns))
    }

    /// Returns the last access timestamp as a `SystemTime`.
    pub fn accessed(&self) -> io::Result<crate::time::SystemTime> {
        Ok(crate::time::SystemTime::from_nanos(self.accessed_time_ns))
    }

    /// Returns the creation timestamp as a `SystemTime`.
    pub fn created(&self) -> io::Result<crate::time::SystemTime> {
        Ok(crate::time::SystemTime::from_nanos(self.created_time_ns))
    }

    /// Converts the raw mode bits into a `Permissions` object.
    pub fn permissions(&self) -> Permissions {
        Permissions { mode: self.mode }
    }
}

/// File permissions.
#[derive(Clone, Debug)]
pub struct Permissions {
    pub(crate) mode: u32,
}

impl Permissions {
    /// Returns whether the file is read-only.
    pub fn readonly(&self) -> bool {
        #[cfg(any(target_family = "unix", rusticated_linux))]
        {
            (self.mode & 0o222) == 0
        }
        #[cfg(target_family = "windows")]
        {
            (self.mode & 0x1) != 0
        }
        #[cfg(not(any(target_family = "unix", target_family = "windows")))]
        {
            false
        }
    }

    /// Returns the raw platform-specific mode bits.
    pub fn mode(&self) -> u32 {
        self.mode
    }

    /// Replaces the current mode bits.
    pub fn set_mode(&mut self, mode: u32) {
        self.mode = mode;
    }

    /// Sets or clears the read-only flag.
    pub fn set_readonly(&mut self, readonly: bool) {
        #[cfg(any(unix, rusticated_linux, target_family = "wasm"))]
        {
            if readonly {
                self.mode &= !0o222;
            } else {
                self.mode |= 0o200;
            }
        }
        #[cfg(windows)]
        {
            if readonly {
                self.mode |= 0x1;
            } else {
                self.mode &= !0x1;
            }
        }
    }
}

// --- WASM Metadata ---

#[cfg(target_family = "wasm")]
#[derive(Clone)]
/// Metadata payload returned by the WASM host for a path stat request.
pub struct Metadata {
    pub(crate) stat: crate::abi::AbiStat,
}

#[cfg(target_family = "wasm")]
impl Metadata {
    /// Returns the size of the file in bytes.
    pub fn len(&self) -> u64 {
        self.stat.size
    }

    /// Returns `true` if the metadata represents a regular file.
    pub fn is_file(&self) -> bool {
        self.stat.kind == crate::abi::STAT_KIND_FILE
    }

    /// Returns `true` if the metadata represents a directory.
    pub fn is_dir(&self) -> bool {
        self.stat.kind == crate::abi::STAT_KIND_DIR
    }

    /// Returns `true` if the metadata represents a symbolic link.
    pub fn is_symlink(&self) -> bool {
        self.stat.kind == crate::abi::STAT_KIND_SYMLINK
    }

    /// Last modification time from the host, in nanoseconds.
    pub fn modified_ns(&self) -> u64 {
        self.stat.modified_ns
    }

    /// Last access time from the host, in nanoseconds.
    pub fn accessed_ns(&self) -> u64 {
        self.stat.accessed_ns
    }

    /// Creation time from the host, in nanoseconds.
    pub fn created_ns(&self) -> u64 {
        self.stat.created_ns
    }

    /// Returns the raw mode bits from the host ABI.
    pub fn mode(&self) -> u32 {
        self.stat.mode
    }

    /// Number of hard links reported by the host.
    pub fn nlink(&self) -> u64 {
        self.stat.nlink
    }

    /// Owning user ID reported by the host.
    pub fn uid(&self) -> u32 {
        self.stat.uid
    }

    /// Owning group ID reported by the host.
    pub fn gid(&self) -> u32 {
        self.stat.gid
    }

    /// Host-provided filesystem object identifier.
    pub fn inode(&self) -> u64 {
        self.stat.inode
    }

    /// Returns the file permissions derived from the host metadata.
    pub fn permissions(&self) -> Permissions {
        Permissions {
            mode: self.stat.mode,
        }
    }
}

// --- File Type ---

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
/// Describes the kind of filesystem entry for a path.
pub struct FileType {
    is_dir: bool,
    is_file: bool,
    is_symlink: bool,
}

impl FileType {
    /// Returns `true` if the type is a directory.
    pub fn is_dir(&self) -> bool {
        self.is_dir
    }

    /// Returns `true` if the type is a regular file.
    pub fn is_file(&self) -> bool {
        self.is_file
    }

    /// Returns `true` if the type is a symbolic link.
    pub fn is_symlink(&self) -> bool {
        self.is_symlink
    }
}

// --- DirEntry ---

/// Directory entry returned by `ReadDir` iteration.
pub struct DirEntry {
    /// The directory entry name.
    pub name: String,
    /// Optional metadata for the directory entry.
    pub metadata: Option<Metadata>,
}

impl DirEntry {
    /// Returns the file or directory name for this entry.
    pub fn file_name(&self) -> &str {
        &self.name
    }

    /// Returns the path of the entry relative to the directory iterator.
    pub fn path(&self) -> crate::path::PathBuf {
        crate::path::PathBuf::from(self.name.clone())
    }

    /// Returns metadata for the directory entry.
    pub fn metadata(&self) -> io::Result<Metadata> {
        self.metadata
            .clone()
            .ok_or_else(|| io::Error::other("metadata unavailable"))
    }

    /// Returns the file type for this directory entry.
    pub fn file_type(&self) -> io::Result<FileType> {
        let md = self.metadata()?;
        Ok(FileType {
            is_dir: md.is_dir(),
            is_file: md.is_file(),
            is_symlink: md.is_symlink(),
        })
    }
}

// --- ReadDir ---

/// Iterator over directory entries.
pub struct ReadDir {
    /// Directory entries produced by the iterator.
    pub entries: Vec<DirEntry>,
    /// The current iterator position inside `entries`.
    pub pos: usize,
}

impl Iterator for ReadDir {
    type Item = io::Result<DirEntry>;
    fn next(&mut self) -> Option<Self::Item> {
        if self.pos < self.entries.len() {
            let entry = core::mem::replace(
                &mut self.entries[self.pos],
                DirEntry {
                    name: String::new(),
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

// --- OpenOptions ---

#[derive(Clone, Debug, Default)]
/// Builder for file open/create options.
pub struct OpenOptions {
    read: bool,
    write: bool,
    append: bool,
    truncate: bool,
    create: bool,
    create_new: bool,
}

impl OpenOptions {
    /// Creates a new set of file open options.
    pub fn new() -> Self {
        Self::default()
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

    /// Enable or disable truncation of an existing file.
    pub fn truncate(&mut self, v: bool) -> &mut Self {
        self.truncate = v;
        self
    }

    /// Enable or disable file creation if the target does not exist.
    pub fn create(&mut self, v: bool) -> &mut Self {
        self.create = v;
        self
    }

    /// Enable or disable exclusive create semantics.
    pub fn create_new(&mut self, v: bool) -> &mut Self {
        self.create_new = v;
        self
    }

    /// Opens a file with the configured options.
    #[allow(unused_variables)]
    #[cfg(not(target_family = "wasm"))]
    pub async fn open<P: AsRef<str>>(&self, path: P) -> io::Result<File> {
        #[cfg(windows)]
        {
            let path = crate::string::String::from(path.as_ref());
            let options = self.clone();
            crate::rt::blocking::BlockingOpFuture::new(move || {
                let mut wchars: alloc::vec::Vec<u16> = path.encode_utf16().collect();
                wchars.push(0);

                let mut access = 0;
                if options.read {
                    access |= 0x80000000;
                } // GENERIC_READ
                if options.write {
                    access |= 0x40000000;
                } // GENERIC_WRITE
                if options.append {
                    access |= 0x00000004;
                } // FILE_APPEND_DATA

                let share = 0x00000001 | 0x00000002; // FILE_SHARE_READ | FILE_SHARE_WRITE

                let creation = if options.create_new {
                    1 // CREATE_NEW
                } else if options.truncate && options.create {
                    2 // CREATE_ALWAYS
                } else if options.truncate {
                    5 // TRUNCATE_EXISTING
                } else if options.create {
                    4 // OPEN_ALWAYS
                } else {
                    3 // OPEN_EXISTING
                };

                #[link(name = "kernel32", kind = "raw-dylib")]
                unsafe extern "system" {
                    fn CreateFileW(
                        lpFileName: *const u16,
                        dwDesiredAccess: u32,
                        dwShareMode: u32,
                        lpSecurityAttributes: *mut core::ffi::c_void,
                        dwCreationDisposition: u32,
                        dwFlagsAndAttributes: u32,
                        hTemplateFile: *mut core::ffi::c_void,
                    ) -> usize;
                }

                let handle = unsafe {
                    CreateFileW(
                        wchars.as_ptr(),
                        access,
                        share,
                        core::ptr::null_mut(),
                        creation,
                        0x40000000 | 0x80, // FILE_FLAG_OVERLAPPED | FILE_ATTRIBUTE_NORMAL
                        core::ptr::null_mut(),
                    )
                };

                let invalid_handle = !0usize;
                if handle == invalid_handle {
                    return Err(io::Error::last_os_error());
                }

                Ok(File {
                    handle: handle as u64,
                })
            })
            .await
        }
        #[cfg(not(windows))]
        {
            let path = crate::string::String::from(path.as_ref());
            let options = self.clone();
            crate::rt::blocking::BlockingOpFuture::new(move || {
                const O_RDONLY: i32 = 0;
                const O_WRONLY: i32 = 1;
                const O_RDWR: i32 = 2;
                const O_CREAT: i32 = 64;
                const O_TRUNC: i32 = 512;
                const O_APPEND: i32 = 1024;
                const O_EXCL: i32 = 128;

                let mut flags = 0;
                if options.read && options.write {
                    flags |= O_RDWR;
                } else if options.write {
                    flags |= O_WRONLY;
                } else {
                    flags |= O_RDONLY;
                }
                if options.create {
                    flags |= O_CREAT;
                }
                if options.truncate {
                    flags |= O_TRUNC;
                }
                if options.append {
                    flags |= O_APPEND;
                }
                if options.create_new {
                    flags |= O_CREAT | O_EXCL;
                }

                let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
                path_bytes.push(0);

                #[cfg(all(any(target_os = "linux", rusticated_linux), target_arch = "x86_64"))]
                let handle = crate::syscall!(
                    crate::os::linux::syscall::nr::OPEN,
                    path_bytes.as_ptr() as usize,
                    flags as usize,
                    0o666usize
                ) as i32;

                #[cfg(all(any(target_os = "linux", rusticated_linux), target_arch = "aarch64"))]
                let handle = crate::syscall!(
                    crate::os::linux::syscall::nr::OPENAT,
                    -100isize as usize, // AT_FDCWD
                    path_bytes.as_ptr() as usize,
                    flags as usize,
                    0o666usize
                ) as i32;

                #[cfg(not(any(target_os = "linux", rusticated_linux)))]
                let handle = {
                    unsafe extern "C" {
                        fn open(pathname: *const u8, flags: i32, mode: u32) -> i32;
                    }
                    unsafe { open(path_bytes.as_ptr(), flags, 0o666) }
                };

                if handle < 0 {
                    return Err(io::Error::last_os_error());
                }
                Ok(File {
                    handle: handle as u64,
                })
            })
            .await
        }
    }

    #[allow(unused_variables)]
    #[cfg(target_family = "wasm")]
    /// Opens a file using the configured `OpenOptions` on wasm.
    pub async fn open<P: AsRef<str>>(&self, path: P) -> io::Result<File> {
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

        let path_bytes = path.as_ref().as_bytes().to_vec();
        let (err, handle, _, _) =
            crate::rt::wasm::OverlappedBufferFuture::new(path_bytes, move |ov, ptr, len| {
                unsafe { crate::abi::imports::path_open(ov, ptr, len, flags) };
            })
            .await;

        if err != 0 {
            return Err(io::Error::from_raw_os_error(err as i32));
        }
        Ok(File { handle })
    }
}

// --- File ---

/// Represents an open filesystem handle.
pub struct File {
    pub(crate) handle: u64,
}

impl Drop for File {
    fn drop(&mut self) {
        #[cfg(target_family = "wasm")]
        unsafe {
            crate::abi::imports::handle_close(self.handle);
        }
        #[cfg(windows)]
        unsafe {
            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn CloseHandle(hObject: usize) -> i32;
            }
            CloseHandle(self.handle as usize);
        }
        #[cfg(any(unix, rusticated_linux))]
        {
            #[cfg(any(target_os = "linux", rusticated_linux))]
            crate::syscall!(crate::os::linux::syscall::nr::CLOSE, self.handle as usize);
            #[cfg(not(any(target_os = "linux", rusticated_linux)))]
            unsafe {
                unsafe extern "C" {
                    fn close(fd: i32) -> i32;
                }
                close(self.handle as i32);
            }
        }
    }
}

impl File {
    /// Returns a fresh builder for opening files.
    pub fn options() -> OpenOptions {
        OpenOptions::new()
    }

    /// Opens an existing file for reading.
    #[cfg(not(target_family = "wasm"))]
    pub async fn open<P: AsRef<str>>(path: P) -> io::Result<Self> {
        OpenOptions::new().read(true).open(path).await
    }

    /// Opens an existing file for reading.
    #[cfg(target_family = "wasm")]
    pub async fn open<P: AsRef<str>>(path: P) -> io::Result<Self> {
        OpenOptions::new().read(true).open(path).await
    }

    /// Creates or truncates a file for writing.
    #[cfg(not(target_family = "wasm"))]
    pub async fn create<P: AsRef<str>>(path: P) -> io::Result<Self> {
        OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)
            .await
    }

    #[cfg(target_family = "wasm")]
    /// Creates or truncates a file for writing on wasm.
    pub async fn create<P: AsRef<str>>(path: P) -> io::Result<Self> {
        OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)
            .await
    }

    /// Returns metadata for the open file handle.
    pub fn metadata(&self) -> io::Result<Metadata> {
        Err(io::Error::other("not implemented"))
    }

    /// Returns the raw file descriptor or handle value.
    pub fn as_raw_fd(&self) -> i32 {
        self.handle as i32
    }

    /// Attempts to duplicate the file handle.
    pub fn try_clone(&self) -> io::Result<Self> {
        #[cfg(any(target_family = "unix", rusticated_linux))]
        {
            #[cfg(any(target_os = "linux", rusticated_linux))]
            let new_fd =
                crate::syscall!(crate::os::linux::syscall::nr::DUP, self.handle as usize) as i32;
            #[cfg(not(any(target_os = "linux", rusticated_linux)))]
            let new_fd = unsafe {
                unsafe extern "C" {
                    fn dup(oldfd: i32) -> i32;
                }
                dup(self.handle as i32)
            };

            if new_fd < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(File {
                handle: new_fd as u64,
            })
        }
        #[cfg(target_family = "windows")]
        {
            // Simplified: we'd need DuplicateHandle, but let's just use stubs for now if complicated.
            // Actually let's just return unimplemented for windows if we don't want to bring in winapi.
            Err(io::Error::other("try_clone not implemented on Windows yet"))
        }
        #[cfg(not(any(target_family = "unix", target_family = "windows")))]
        {
            Err(io::Error::other(
                "try_clone not implemented on this platform",
            ))
        }
    }

    /// Returns `true` when the file points at a terminal device.
    pub fn is_terminal(&self) -> bool {
        #[cfg(any(target_family = "unix", rusticated_linux))]
        {
            #[cfg(any(target_os = "linux", rusticated_linux))]
            {
                let mut termios = [0u8; 1024]; // Large enough for any termios
                let res = crate::syscall!(
                    crate::os::linux::syscall::nr::IOCTL,
                    self.handle as usize,
                    0x5401usize, // TCGETS
                    termios.as_mut_ptr() as usize
                ) as isize;
                res >= 0
            }
            #[cfg(not(any(target_os = "linux", rusticated_linux)))]
            {
                unsafe {
                    unsafe extern "C" {
                        fn isatty(fd: i32) -> i32;
                    }
                    isatty(self.handle as i32) != 0
                }
            }
        }
        #[cfg(target_family = "windows")]
        {
            false // TODO
        }
        #[cfg(not(any(target_family = "unix", target_family = "windows")))]
        {
            false
        }
    }
}

impl AsyncRead for File {
    async fn read(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
        #[cfg(target_family = "wasm")]
        {
            let handle = self.handle;
            let (err, read, _, buf) =
                crate::rt::wasm::OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                    unsafe { crate::abi::imports::read(ov, handle, ptr, len) };
                })
                .await;
            if err != 0 {
                return (Err(io::Error::from_raw_os_error(err as i32)), buf);
            }
            let mut buf = buf;
            unsafe { buf.set_len(read as usize) };
            (Ok(read as usize), buf)
        }
        #[cfg(windows)]
        {
            crate::rt::windows::OverlappedRead::new(self.handle, buf).await
        }
        #[cfg(all(not(target_family = "wasm"), not(windows)))]
        {
            crate::rt::linux_op::LinuxOpFuture::read(self.handle as i32, buf).await
        }
    }
}

impl AsyncWrite for File {
    async fn write(&mut self, buf: Vec<u8>) -> (io::Result<usize>, Vec<u8>) {
        #[cfg(target_family = "wasm")]
        {
            let handle = self.handle;
            let (err, written, _, buf) =
                crate::rt::wasm::OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                    unsafe { crate::abi::imports::write(ov, handle, ptr, len) };
                })
                .await;
            if err != 0 {
                return (Err(io::Error::from_raw_os_error(err as i32)), buf);
            }
            (Ok(written as usize), buf)
        }
        #[cfg(windows)]
        {
            crate::rt::windows::OverlappedWrite::new(self.handle, buf).await
        }
        #[cfg(all(not(target_family = "wasm"), not(windows)))]
        {
            crate::rt::linux_op::LinuxOpFuture::write(self.handle as i32, buf).await
        }
    }
    async fn flush(&mut self) -> io::Result<()> {
        #[cfg(target_family = "wasm")]
        {
            Ok(())
        }
        #[cfg(any(unix, rusticated_linux))]
        {
            let fd = self.handle as i32;
            crate::rt::blocking::BlockingOpFuture::new(move || {
                #[cfg(any(target_os = "linux", rusticated_linux))]
                let res = crate::syscall!(crate::os::linux::syscall::nr::FSYNC, fd as usize) as i32;
                #[cfg(not(any(target_os = "linux", rusticated_linux)))]
                let res = unsafe {
                    unsafe extern "C" {
                        fn fsync(fd: i32) -> i32;
                    }
                    fsync(fd)
                };
                if res < 0 {
                    Err(io::Error::last_os_error())
                } else {
                    Ok(())
                }
            })
            .await
        }
        #[cfg(windows)]
        {
            let handle = self.handle as usize;
            crate::rt::blocking::BlockingOpFuture::new(move || {
                #[link(name = "kernel32", kind = "raw-dylib")]
                unsafe extern "system" {
                    fn FlushFileBuffers(hObject: usize) -> i32;
                }
                let res = unsafe { FlushFileBuffers(handle) };
                if res == 0 {
                    // 0 means failure in WinAPI for this function
                    Err(io::Error::last_os_error())
                } else {
                    Ok(())
                }
            })
            .await
        }
    }
}

// --- Global Fns ---

/// Reads a directory and returns an iterator over its entries.
pub async fn read_dir<P: AsRef<str>>(path: P) -> io::Result<ReadDir> {
    #[cfg(target_family = "wasm")]
    {
        let path_bytes = path.as_ref().as_bytes().to_vec();
        // 1. Open the directory
        let (err, handle, _, _) =
            crate::rt::wasm::OverlappedBufferFuture::new(path_bytes, |ov, ptr, len| {
                unsafe { crate::abi::imports::path_open(ov, ptr, len, 0) }; // 0 flags
            })
            .await;

        if err != 0 {
            return Err(io::Error::from_raw_os_error(err as i32));
        }

        // 2. Read entries into a buffer until empty
        let mut all_bytes = alloc::vec::Vec::new();
        loop {
            let buf = alloc::vec![0u8; 4096];
            let (err, read_len, _, buf) =
                crate::rt::wasm::OverlappedBufferFuture::new(buf, move |ov, ptr, len| {
                    unsafe { crate::abi::imports::dir_read(ov, handle, ptr, len) };
                })
                .await;

            if err != 0 {
                // close handle and return error
                unsafe { crate::abi::imports::handle_close(handle) };
                return Err(io::Error::from_raw_os_error(err as i32));
            }

            if read_len == 0 {
                break;
            }
            all_bytes.extend_from_slice(&buf[..read_len as usize]);
        }

        unsafe { crate::abi::imports::handle_close(handle) };

        // Format is null-separated UTF-8 strings
        let mut entries = Vec::new();
        let base_path = path.as_ref();
        let mut start = 0;
        for (i, &b) in all_bytes.iter().enumerate() {
            if b == 0 {
                if start < i {
                    let name = String::from_utf8_lossy(&all_bytes[start..i]).into_owned();
                    let full_path = if base_path.is_empty() || base_path == "." {
                        name.clone()
                    } else if base_path.ends_with('/') || base_path.ends_with('\\') {
                        format!("{}{}", base_path, name)
                    } else {
                        format!("{}/{}", base_path, name)
                    };
                    let md = metadata(&full_path).await.ok();
                    entries.push(DirEntry { name, metadata: md });
                }
                start = i + 1;
            }
        }
        if start < all_bytes.len() {
            let name = String::from_utf8_lossy(&all_bytes[start..]).into_owned();
            let full_path = if base_path.is_empty() || base_path == "." {
                name.clone()
            } else if base_path.ends_with('/') || base_path.ends_with('\\') {
                format!("{}{}", base_path, name)
            } else {
                format!("{}/{}", base_path, name)
            };
            let md = metadata(&full_path).await.ok();
            entries.push(DirEntry { name, metadata: md });
        }
        Ok(ReadDir { entries, pos: 0 })
    }
    #[cfg(windows)]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            let mut search_path = path.clone();
            if !search_path.ends_with('\\') && !search_path.ends_with('/') {
                search_path.push_str("\\*");
            } else {
                search_path.push('*');
            }
            let mut wchars: alloc::vec::Vec<u16> = search_path.encode_utf16().collect();
            wchars.push(0);

            #[repr(C)]
            #[allow(non_snake_case)]
            struct WIN32_FIND_DATAW {
                dwFileAttributes: u32,
                ftCreationTimeLow: u32,
                ftCreationTimeHigh: u32,
                ftLastAccessTimeLow: u32,
                ftLastAccessTimeHigh: u32,
                ftLastWriteTimeLow: u32,
                ftLastWriteTimeHigh: u32,
                nFileSizeHigh: u32,
                nFileSizeLow: u32,
                dwReserved0: u32,
                dwReserved1: u32,
                cFileName: [u16; 260],
                cAlternateFileName: [u16; 14],
            }

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn FindFirstFileW(
                    lpFileName: *const u16,
                    lpFindFileData: *mut WIN32_FIND_DATAW,
                ) -> usize;
                fn FindNextFileW(hFindFile: usize, lpFindFileData: *mut WIN32_FIND_DATAW) -> i32;
                fn FindClose(hFindFile: usize) -> i32;
            }

            let mut data = core::mem::MaybeUninit::<WIN32_FIND_DATAW>::uninit();
            let handle = unsafe { FindFirstFileW(wchars.as_ptr(), data.as_mut_ptr()) };
            if handle == !0usize {
                return Err(io::Error::last_os_error());
            }

            let mut entries = alloc::vec::Vec::new();
            unsafe {
                let to_unix_ns = |low: u32, high: u32| -> u64 {
                    let filetime = ((high as u64) << 32) | (low as u64);
                    if filetime > 116444736000000000 {
                        (filetime - 116444736000000000) * 100
                    } else {
                        0
                    }
                };

                loop {
                    let d = data.assume_init_ref();
                    let len = d.cFileName.iter().position(|&x| x == 0).unwrap_or(260);
                    let name = alloc::string::String::from_utf16_lossy(&d.cFileName[..len]);
                    if name != "." && name != ".." {
                        let metadata = Metadata {
                            size: ((d.nFileSizeHigh as u64) << 32) | (d.nFileSizeLow as u64),
                            mode: d.dwFileAttributes,
                            modified_time_ns: to_unix_ns(
                                d.ftLastWriteTimeLow,
                                d.ftLastWriteTimeHigh,
                            ),
                            accessed_time_ns: to_unix_ns(
                                d.ftLastAccessTimeLow,
                                d.ftLastAccessTimeHigh,
                            ),
                            created_time_ns: to_unix_ns(d.ftCreationTimeLow, d.ftCreationTimeHigh),
                            nlink: 1,
                            uid: 0,
                            gid: 0,
                            inode: 0,
                        };

                        entries.push(DirEntry {
                            name,
                            metadata: Some(metadata),
                        });
                    }
                    if FindNextFileW(handle, data.as_mut_ptr()) == 0 {
                        break;
                    }
                }
                FindClose(handle);
            }
            Ok(ReadDir { entries, pos: 0 })
        })
        .await
    }
    #[cfg(all(not(target_family = "wasm"), not(windows)))]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
            path_bytes.push(0);

            #[cfg(not(any(target_os = "linux", rusticated_linux)))]
            #[repr(C)]
            struct Dirent {
                d_ino: u64,
                d_off: i64,
                d_reclen: u16,
                d_type: u8,
                d_name: [u8; 256],
            }

            #[cfg(any(target_os = "linux", rusticated_linux))]
            {
                // On Linux we'll use open + getdents64
                #[cfg(target_arch = "x86_64")]
                let fd = crate::syscall!(
                    crate::os::linux::syscall::nr::OPEN,
                    path_bytes.as_ptr() as usize,
                    0x10000usize | 0usize, // O_DIRECTORY | O_RDONLY
                    0usize
                ) as i32;
                #[cfg(target_arch = "aarch64")]
                let fd = crate::syscall!(
                    crate::os::linux::syscall::nr::OPENAT,
                    -100isize as usize, // AT_FDCWD
                    path_bytes.as_ptr() as usize,
                    0x10000usize | 0usize, // O_DIRECTORY | O_RDONLY
                    0usize
                ) as i32;

                if fd < 0 {
                    return Err(io::Error::last_os_error());
                }

                let mut entries = alloc::vec::Vec::new();
                let mut buf = [0u8; 4096];
                loop {
                    let nread = crate::syscall!(
                        crate::os::linux::syscall::nr::GETDENTS64,
                        fd as usize,
                        buf.as_mut_ptr() as usize,
                        buf.len()
                    ) as isize;
                    if nread < 0 {
                        crate::syscall!(crate::os::linux::syscall::nr::CLOSE, fd as usize);
                        return Err(io::Error::last_os_error());
                    }
                    if nread == 0 {
                        break;
                    }

                    let mut bpos = 0usize;
                    while bpos < nread as usize {
                        #[repr(C)]
                        struct LinuxDirent64 {
                            d_ino: u64,
                            d_off: i64,
                            d_reclen: u16,
                            d_type: u8,
                            d_name: [u8; 0],
                        }
                        let d = unsafe { &*(buf.as_ptr().add(bpos) as *const LinuxDirent64) };
                        let name_ptr = unsafe {
                            buf.as_ptr()
                                .add(bpos + core::mem::offset_of!(LinuxDirent64, d_name))
                        };
                        let name_len = (d.d_reclen as usize)
                            - core::mem::offset_of!(LinuxDirent64, d_name)
                            - 1;
                        let name_slice = unsafe { core::slice::from_raw_parts(name_ptr, name_len) };
                        // Find actual null terminator in case of padding
                        let actual_len =
                            name_slice.iter().position(|&b| b == 0).unwrap_or(name_len);
                        let name =
                            alloc::string::String::from_utf8_lossy(&name_slice[..actual_len])
                                .into_owned();

                        if name != "." && name != ".." {
                            let mut mode = 0;
                            if d.d_type == 4 {
                                // DT_DIR
                                mode = 0o040000;
                            } else if d.d_type == 8 {
                                // DT_REG
                                mode = 0o100000;
                            }
                            entries.push(DirEntry {
                                name,
                                metadata: Some(Metadata {
                                    size: 0,
                                    mode,
                                    modified_time_ns: 0,
                                    accessed_time_ns: 0,
                                    created_time_ns: 0,
                                    nlink: 1,
                                    uid: 0,
                                    gid: 0,
                                    inode: d.d_ino,
                                }),
                            });
                        }
                        bpos += d.d_reclen as usize;
                    }
                }
                crate::syscall!(crate::os::linux::syscall::nr::CLOSE, fd as usize);
                Ok(ReadDir { entries, pos: 0 })
            }
            #[cfg(not(any(target_os = "linux", rusticated_linux)))]
            {
                unsafe extern "C" {
                    fn opendir(name: *const u8) -> *mut core::ffi::c_void;
                    fn readdir(dirp: *mut core::ffi::c_void) -> *mut Dirent;
                    fn closedir(dirp: *mut core::ffi::c_void) -> i32;
                }

                let dir = unsafe { opendir(path_bytes.as_ptr()) };
                if dir.is_null() {
                    return Err(io::Error::last_os_error());
                }

                let mut entries = alloc::vec::Vec::new();
                loop {
                    let ent = unsafe { readdir(dir) };
                    if ent.is_null() {
                        break;
                    }
                    let ent = unsafe { &*ent };
                    let mut len = 0;
                    while ent.d_name[len] != 0 && len < 256 {
                        len += 1;
                    }
                    let name =
                        alloc::string::String::from_utf8_lossy(&ent.d_name[..len]).into_owned();
                    if name != "." && name != ".." {
                        let mut mode = 0;
                        if ent.d_type == 4 {
                            // DT_DIR
                            mode = 0o040000;
                        } else if ent.d_type == 8 {
                            // DT_REG
                            mode = 0o100000;
                        }
                        let metadata = Metadata {
                            size: 0,
                            mode,
                            modified_time_ns: 0,
                            accessed_time_ns: 0,
                            created_time_ns: 0,
                            nlink: 1,
                            uid: 0,
                            gid: 0,
                            inode: ent.d_ino,
                        };
                        entries.push(DirEntry {
                            name,
                            metadata: Some(metadata),
                        });
                    }
                }
                unsafe { closedir(dir) };
                Ok(ReadDir { entries, pos: 0 })
            }
        })
        .await
    }
}

/// Returns metadata for the given path.
pub async fn metadata<P: AsRef<str>>(path: P) -> io::Result<Metadata> {
    #[cfg(target_family = "wasm")]
    {
        metadata_wasm(path.as_ref(), 0).await
    }
    #[cfg(not(target_family = "wasm"))]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            #[cfg(any(unix, rusticated_linux))]
            {
                let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
                path_bytes.push(0);

                let mut meta = core::mem::MaybeUninit::<Stat>::uninit();

                #[cfg(any(target_os = "linux", rusticated_linux))]
                let result = {
                    #[cfg(target_arch = "x86_64")]
                    let r = crate::syscall!(
                        crate::os::linux::syscall::nr::STAT,
                        path_bytes.as_ptr() as usize,
                        meta.as_mut_ptr() as usize
                    ) as i32;
                    #[cfg(target_arch = "aarch64")]
                    let r = crate::syscall!(
                        79usize,            // FSTATAT
                        -100isize as usize, // AT_FDCWD
                        path_bytes.as_ptr() as usize,
                        meta.as_mut_ptr() as usize,
                        0usize
                    ) as i32;
                    r
                };
                #[cfg(not(any(target_os = "linux", rusticated_linux)))]
                let result = {
                    unsafe extern "C" {
                        fn stat(pathname: *const u8, buf: *mut Stat) -> i32;
                    }
                    unsafe { stat(path_bytes.as_ptr(), meta.as_mut_ptr()) }
                };

                if result != 0 {
                    return Err(io::Error::last_os_error());
                }
                let meta = unsafe { meta.assume_init() };

                return Ok(Metadata {
                    size: meta.st_size as u64,
                    mode: meta.st_mode as u32,
                    modified_time_ns: 0,
                    accessed_time_ns: 0,
                    created_time_ns: 0,
                    nlink: meta.st_nlink as u64,
                    uid: meta.st_uid,
                    gid: meta.st_gid,
                    inode: meta.st_ino as u64,
                });
            }

            #[cfg(windows)]
            {
                #[repr(C)]
                struct FILETIME {
                    low: u32,
                    high: u32,
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

                #[link(name = "kernel32", kind = "raw-dylib")]
                unsafe extern "system" {
                    fn CreateFileW(
                        lpFileName: *const u16,
                        dwDesiredAccess: u32,
                        dwShareMode: u32,
                        lpSecurityAttributes: *mut core::ffi::c_void,
                        dwCreationDisposition: u32,
                        dwFlagsAndAttributes: u32,
                        hTemplateFile: *mut core::ffi::c_void,
                    ) -> usize;
                    fn GetFileInformationByHandle(
                        hFile: usize,
                        lpFileInformation: *mut BY_HANDLE_FILE_INFORMATION,
                    ) -> i32;
                    fn CloseHandle(hObject: usize) -> i32;
                }

                let mut wchars: alloc::vec::Vec<u16> = path.encode_utf16().collect();
                wchars.push(0);
                let handle = unsafe {
                    CreateFileW(
                        wchars.as_ptr(),
                        0x8000_0000,
                        0x0000_0001 | 0x0000_0002 | 0x0000_0004,
                        core::ptr::null_mut(),
                        3,
                        0x0200_0000,
                        core::ptr::null_mut(),
                    )
                };
                if handle == !0usize {
                    return Err(io::Error::last_os_error());
                }

                let mut info = core::mem::MaybeUninit::<BY_HANDLE_FILE_INFORMATION>::uninit();
                let ok = unsafe { GetFileInformationByHandle(handle, info.as_mut_ptr()) };
                let _ = unsafe { CloseHandle(handle) };
                if ok == 0 {
                    return Err(io::Error::last_os_error());
                }
                let info = unsafe { info.assume_init() };

                let to_ns = |ft: FILETIME| -> u64 {
                    let filetime = ((ft.high as u64) << 32) | ft.low as u64;
                    if filetime > 116444736000000000 {
                        (filetime - 116444736000000000) * 100
                    } else {
                        0
                    }
                };

                return Ok(Metadata {
                    size: ((info.nFileSizeHigh as u64) << 32) | info.nFileSizeLow as u64,
                    mode: info.dwFileAttributes,
                    modified_time_ns: to_ns(info.ftLastWriteTime),
                    accessed_time_ns: to_ns(info.ftLastAccessTime),
                    created_time_ns: to_ns(info.ftCreationTime),
                    nlink: info.nNumberOfLinks as u64,
                    uid: 0,
                    gid: 0,
                    inode: ((info.nFileIndexHigh as u64) << 32) | info.nFileIndexLow as u64,
                });
            }

            #[cfg(not(any(unix, windows)))]
            {
                Err(io::Error::other("metadata not implemented on this host"))
            }
        })
        .await
    }
}

/// Returns metadata for the given path without following symbolic links.
pub async fn symlink_metadata<P: AsRef<str>>(path: P) -> io::Result<Metadata> {
    #[cfg(target_family = "wasm")]
    {
        metadata_wasm(path.as_ref(), crate::abi::STAT_FLAG_NOFOLLOW).await
    }
    #[cfg(not(target_family = "wasm"))]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            #[cfg(any(unix, rusticated_linux))]
            {
                let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
                path_bytes.push(0);

                let mut meta = core::mem::MaybeUninit::<Stat>::uninit();

                #[cfg(any(target_os = "linux", rusticated_linux))]
                let result = {
                    #[cfg(target_arch = "x86_64")]
                    let r = crate::syscall!(
                        crate::os::linux::syscall::nr::LSTAT,
                        path_bytes.as_ptr() as usize,
                        meta.as_mut_ptr() as usize
                    ) as i32;
                    #[cfg(target_arch = "aarch64")]
                    let r = crate::syscall!(
                        79usize,            // FSTATAT
                        -100isize as usize, // AT_FDCWD
                        path_bytes.as_ptr() as usize,
                        meta.as_mut_ptr() as usize,
                        0x100usize // AT_SYMLINK_NOFOLLOW
                    ) as i32;
                    r
                };
                #[cfg(not(any(target_os = "linux", rusticated_linux)))]
                let result = {
                    unsafe extern "C" {
                        fn lstat(pathname: *const u8, buf: *mut Stat) -> i32;
                    }
                    unsafe { lstat(path_bytes.as_ptr(), meta.as_mut_ptr()) }
                };

                if result != 0 {
                    return Err(io::Error::last_os_error());
                }
                let meta = unsafe { meta.assume_init() };

                return Ok(Metadata {
                    size: meta.st_size as u64,
                    mode: meta.st_mode as u32,
                    modified_time_ns: 0,
                    accessed_time_ns: 0,
                    created_time_ns: 0,
                    nlink: meta.st_nlink as u64,
                    uid: meta.st_uid,
                    gid: meta.st_gid,
                    inode: meta.st_ino as u64,
                });
            }

            #[cfg(windows)]
            {
                #[repr(C)]
                struct FILETIME {
                    low: u32,
                    high: u32,
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

                #[link(name = "kernel32", kind = "raw-dylib")]
                unsafe extern "system" {
                    fn CreateFileW(
                        lpFileName: *const u16,
                        dwDesiredAccess: u32,
                        dwShareMode: u32,
                        lpSecurityAttributes: *mut core::ffi::c_void,
                        dwCreationDisposition: u32,
                        dwFlagsAndAttributes: u32,
                        hTemplateFile: *mut core::ffi::c_void,
                    ) -> usize;
                    fn GetFileInformationByHandle(
                        hFile: usize,
                        lpFileInformation: *mut BY_HANDLE_FILE_INFORMATION,
                    ) -> i32;
                    fn CloseHandle(hObject: usize) -> i32;
                }

                let mut wchars: alloc::vec::Vec<u16> = path.encode_utf16().collect();
                wchars.push(0);
                let handle = unsafe {
                    CreateFileW(
                        wchars.as_ptr(),
                        0x8000_0000,
                        0x0000_0001 | 0x0000_0002 | 0x0000_0004,
                        core::ptr::null_mut(),
                        3,
                        0x0200_0000 | 0x0020_0000,
                        core::ptr::null_mut(),
                    )
                };
                if handle == !0usize {
                    return Err(io::Error::last_os_error());
                }

                let mut info = core::mem::MaybeUninit::<BY_HANDLE_FILE_INFORMATION>::uninit();
                let ok = unsafe { GetFileInformationByHandle(handle, info.as_mut_ptr()) };
                let _ = unsafe { CloseHandle(handle) };
                if ok == 0 {
                    return Err(io::Error::last_os_error());
                }
                let info = unsafe { info.assume_init() };

                let to_ns = |ft: FILETIME| -> u64 {
                    let filetime = ((ft.high as u64) << 32) | ft.low as u64;
                    if filetime > 116444736000000000 {
                        (filetime - 116444736000000000) * 100
                    } else {
                        0
                    }
                };

                return Ok(Metadata {
                    size: ((info.nFileSizeHigh as u64) << 32) | info.nFileSizeLow as u64,
                    mode: info.dwFileAttributes,
                    modified_time_ns: to_ns(info.ftLastWriteTime),
                    accessed_time_ns: to_ns(info.ftLastAccessTime),
                    created_time_ns: to_ns(info.ftCreationTime),
                    nlink: info.nNumberOfLinks as u64,
                    uid: 0,
                    gid: 0,
                    inode: ((info.nFileIndexHigh as u64) << 32) | info.nFileIndexLow as u64,
                });
            }

            #[cfg(not(any(unix, windows)))]
            {
                Err(io::Error::other(
                    "symlink metadata not implemented on this host",
                ))
            }
        })
        .await
    }
}

/// Changes permissions for the given path.
/// Changes the permissions of a filesystem object.
pub async fn set_permissions<P: AsRef<str>>(path: P, permissions: Permissions) -> io::Result<()> {
    #[cfg(target_family = "wasm")]
    {
        let path_bytes = path.as_ref().as_bytes().to_vec();
        let mode = permissions.mode();
        let (err, _, _) = crate::rt::wasm::OverlappedFuture::new(move |ov| {
            unsafe {
                crate::abi::imports::path_chmod(
                    ov,
                    path_bytes.as_ptr(),
                    path_bytes.len() as u32,
                    mode,
                )
            };
        })
        .await;

        if err != 0 {
            return Err(io::Error::from_raw_os_error(err as i32));
        }
        return Ok(());
    }

    #[cfg(not(target_family = "wasm"))]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            #[cfg(any(unix, rusticated_linux))]
            {
                let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
                path_bytes.push(0);

                #[cfg(any(target_os = "linux", rusticated_linux))]
                let rc = {
                    #[cfg(target_arch = "x86_64")]
                    let r = crate::syscall!(
                        crate::os::linux::syscall::nr::CHMOD,
                        path_bytes.as_ptr() as usize,
                        permissions.mode() as usize
                    ) as i32;
                    #[cfg(target_arch = "aarch64")]
                    let r = crate::syscall!(
                        53usize,            // FCHMODAT
                        -100isize as usize, // AT_FDCWD
                        path_bytes.as_ptr() as usize,
                        permissions.mode() as usize,
                        0usize
                    ) as i32;
                    r
                };
                #[cfg(not(any(target_os = "linux", rusticated_linux)))]
                let rc = unsafe {
                    unsafe extern "C" {
                        fn chmod(pathname: *const u8, mode: u32) -> i32;
                    }
                    chmod(path_bytes.as_ptr(), permissions.mode())
                };

                if rc != 0 {
                    return Err(io::Error::last_os_error());
                }
                Ok(())
            }
            #[cfg(windows)]
            {
                #[link(name = "kernel32", kind = "raw-dylib")]
                unsafe extern "system" {
                    fn GetFileAttributesW(lpFileName: *const u16) -> u32;
                    fn SetFileAttributesW(lpFileName: *const u16, dwFileAttributes: u32) -> i32;
                }

                let mut wchars: alloc::vec::Vec<u16> = path.encode_utf16().collect();
                wchars.push(0);
                let mut attrs = unsafe { GetFileAttributesW(wchars.as_ptr()) };
                if attrs == u32::MAX {
                    return Err(io::Error::last_os_error());
                }

                if permissions.readonly() {
                    attrs |= 0x1;
                } else {
                    attrs &= !0x1;
                }

                if unsafe { SetFileAttributesW(wchars.as_ptr(), attrs) } == 0 {
                    return Err(io::Error::last_os_error());
                }
                Ok(())
            }
            #[cfg(not(any(unix, windows)))]
            {
                let _ = path;
                let _ = permissions;
                Ok(())
            }
        })
        .await
    }
}

#[cfg(target_family = "wasm")]
const ABI_STAT_WIRE_SIZE: usize = core::mem::size_of::<crate::abi::AbiStat>();

#[cfg(target_family = "wasm")]
fn read_u32_le(buf: &[u8], off: usize) -> u32 {
    let mut tmp = [0u8; 4];
    tmp.copy_from_slice(&buf[off..off + 4]);
    u32::from_le_bytes(tmp)
}

#[cfg(target_family = "wasm")]
fn read_u64_le(buf: &[u8], off: usize) -> u64 {
    let mut tmp = [0u8; 8];
    tmp.copy_from_slice(&buf[off..off + 8]);
    u64::from_le_bytes(tmp)
}

#[cfg(target_family = "wasm")]
fn decode_abi_stat(buf: &[u8]) -> io::Result<crate::abi::AbiStat> {
    if buf.len() < ABI_STAT_WIRE_SIZE {
        return Err(io::Error::other("short stat payload"));
    }
    Ok(crate::abi::AbiStat {
        kind: read_u32_le(buf, 0),
        mode: read_u32_le(buf, 4),
        uid: read_u32_le(buf, 8),
        gid: read_u32_le(buf, 12),
        size: read_u64_le(buf, 16),
        modified_ns: read_u64_le(buf, 24),
        accessed_ns: read_u64_le(buf, 32),
        created_ns: read_u64_le(buf, 40),
        nlink: read_u64_le(buf, 48),
        inode: read_u64_le(buf, 56),
    })
}

#[cfg(target_family = "wasm")]
async fn metadata_wasm(path: &str, flags: u32) -> io::Result<Metadata> {
    let path_bytes = path.as_bytes();
    let path_len = path_bytes.len();
    let mut io_buf = alloc::vec![0u8; path_len + ABI_STAT_WIRE_SIZE];
    io_buf[..path_len].copy_from_slice(path_bytes);
    let path_len_u32 = path_len as u32;
    let out_len_u32 = ABI_STAT_WIRE_SIZE as u32;

    let (err, result_ext, _, io_buf) =
        crate::rt::wasm::OverlappedBufferFuture::new(io_buf, move |ov, ptr, _| {
            let out_ptr = unsafe { ptr.add(path_len) };
            unsafe {
                crate::abi::imports::path_stat(
                    ov,
                    ptr as *const u8,
                    path_len_u32,
                    flags,
                    out_ptr,
                    out_len_u32,
                )
            };
        })
        .await;

    if err != 0 {
        return Err(io::Error::from_raw_os_error(err as i32));
    }
    if result_ext < ABI_STAT_WIRE_SIZE as u64 {
        return Err(io::Error::other("incomplete stat payload"));
    }

    let stat = decode_abi_stat(&io_buf[path_len..path_len + ABI_STAT_WIRE_SIZE])?;
    Ok(Metadata { stat })
}

/// Opens the platform's null device as a file.
pub fn open_null_file() -> io::Result<File> {
    Err(io::Error::other("not implemented"))
}
/// Returns whether path expansion should be case-insensitive on this platform.
pub fn default_case_insensitive_path_expansion() -> bool {
    false
}
/// Resolves the executable path for a child process, if known.
pub fn resolve_executable<P: AsRef<str>>(_path: P) -> Option<String> {
    None
}
/// Splits a path for matching against wildcard patterns.
pub fn split_path_for_pattern(_path: &str) -> Vec<&str> {
    Vec::new()
}
/// Returns the root component of a pattern path when present.
pub fn pattern_path_root(_path: &str) -> Option<String> {
    None
}
/// Normalizes path separators for the current platform.
pub fn normalize_path_separators(path: &str) -> alloc::borrow::Cow<'_, str> {
    alloc::borrow::Cow::Borrowed(path)
}
/// Appends a path component to the pattern path builder.
pub fn push_path_for_pattern(path: &mut crate::path::PathBuf, piece: &str) {
    let mut s = path.to_string();
    if !s.is_empty() && !s.ends_with('/') && !s.ends_with('\\') {
        s.push('/');
    }
    s.push_str(piece);
    *path = crate::path::PathBuf::from(s);
}
