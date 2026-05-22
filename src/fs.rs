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

pub use File as FileNative;

pub fn canonicalize_sync<P: AsRef<str>>(_path: P) -> io::Result<crate::path::PathBuf> {
    Err(io::Error::other("not implemented"))
}

// --- Metadata Struct ---

#[cfg(not(target_family = "wasm"))]
#[derive(Clone, Debug)]
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
    pub fn len(&self) -> u64 {
        self.size
    }
    pub fn is_file(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o100000
        }
        #[cfg(windows)]
        {
            (self.mode & 0x10) == 0
        }
    }
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
    pub fn is_symlink(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o170000) == 0o120000
        }
        #[cfg(windows)]
        {
            (self.mode & 0x400) != 0
        }
    }
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
    pub fn readonly(&self) -> bool {
        #[cfg(unix)]
        {
            (self.mode & 0o222) == 0
        }
        #[cfg(windows)]
        {
            (self.mode & 0x1) != 0
        }
    }
    pub fn modified_ns(&self) -> u64 {
        self.modified_time_ns
    }
    pub fn accessed_ns(&self) -> u64 {
        self.accessed_time_ns
    }
    pub fn created_ns(&self) -> u64 {
        self.created_time_ns
    }
    pub fn mode(&self) -> u32 {
        self.mode
    }
    pub fn nlink(&self) -> u64 {
        self.nlink
    }
    pub fn uid(&self) -> u32 {
        self.uid
    }
    pub fn gid(&self) -> u32 {
        self.gid
    }
    pub fn inode(&self) -> u64 {
        self.inode
    }
}

// --- WASM Metadata ---

#[cfg(target_family = "wasm")]
#[derive(Clone)]
pub struct Metadata {
    pub(crate) handle: u64,
}

#[cfg(target_family = "wasm")]
impl Metadata {
    pub fn len(&self) -> u64 {
        unsafe { crate::abi::imports::stat_len(self.handle) }
    }
    pub fn is_file(&self) -> bool {
        unsafe { crate::abi::imports::stat_is_file(self.handle) != 0 }
    }
    pub fn is_dir(&self) -> bool {
        unsafe { crate::abi::imports::stat_is_dir(self.handle) != 0 }
    }
    pub fn is_symlink(&self) -> bool {
        unsafe { crate::abi::imports::stat_is_symlink(self.handle) != 0 }
    }
    pub fn modified_ns(&self) -> u64 {
        unsafe { crate::abi::imports::stat_mtime(self.handle) }
    }
    pub fn accessed_ns(&self) -> u64 {
        unsafe { crate::abi::imports::stat_atime(self.handle) }
    }
    pub fn created_ns(&self) -> u64 {
        unsafe { crate::abi::imports::stat_ctime(self.handle) }
    }
    pub fn mode(&self) -> u32 {
        unsafe { crate::abi::imports::stat_mode(self.handle) }
    }
    pub fn nlink(&self) -> u64 {
        unsafe { crate::abi::imports::stat_nlink(self.handle) }
    }
    pub fn uid(&self) -> u32 {
        unsafe { crate::abi::imports::stat_uid(self.handle) }
    }
    pub fn gid(&self) -> u32 {
        unsafe { crate::abi::imports::stat_gid(self.handle) }
    }
    pub fn inode(&self) -> u64 {
        unsafe { crate::abi::imports::stat_inode(self.handle) }
    }
}

// --- File Type ---

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct FileType;

impl FileType {
    pub fn is_dir(&self) -> bool {
        false
    }
    pub fn is_file(&self) -> bool {
        true
    }
    pub fn is_symlink(&self) -> bool {
        false
    }
}

// --- DirEntry ---

pub struct DirEntry {
    pub name: String,
    pub metadata: Option<Metadata>,
}

impl DirEntry {
    pub fn file_name(&self) -> &str {
        &self.name
    }
    pub fn path(&self) -> crate::path::PathBuf {
        crate::path::PathBuf::from(self.name.clone())
    }
    pub fn metadata(&self) -> io::Result<Metadata> {
        self.metadata
            .clone()
            .ok_or_else(|| io::Error::other("metadata unavailable"))
    }
    pub fn file_type(&self) -> io::Result<FileType> {
        Ok(FileType)
    }
}

// --- ReadDir ---

pub struct ReadDir {
    pub entries: Vec<DirEntry>,
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
pub struct OpenOptions {
    read: bool,
    write: bool,
    append: bool,
    truncate: bool,
    create: bool,
    create_new: bool,
}

impl OpenOptions {
    pub fn new() -> Self {
        Self::default()
    }
    pub fn read(&mut self, v: bool) -> &mut Self {
        self.read = v;
        self
    }
    pub fn write(&mut self, v: bool) -> &mut Self {
        self.write = v;
        self
    }
    pub fn append(&mut self, v: bool) -> &mut Self {
        self.append = v;
        self
    }
    pub fn truncate(&mut self, v: bool) -> &mut Self {
        self.truncate = v;
        self
    }
    pub fn create(&mut self, v: bool) -> &mut Self {
        self.create = v;
        self
    }
    pub fn create_new(&mut self, v: bool) -> &mut Self {
        self.create_new = v;
        self
    }

    #[allow(unused_variables)]
    pub async fn open<P: AsRef<str>>(&self, path: P) -> io::Result<File> {
        #[cfg(target_family = "wasm")]
        {
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
        #[cfg(windows)]
        {
            let mut wchars: alloc::vec::Vec<u16> = path.as_ref().encode_utf16().collect();
            wchars.push(0);

            let mut access = 0;
            if self.read {
                access |= 0x80000000;
            } // GENERIC_READ
            if self.write {
                access |= 0x40000000;
            } // GENERIC_WRITE
            if self.append {
                access |= 0x00000004;
            } // FILE_APPEND_DATA

            let share = 0x00000001 | 0x00000002; // FILE_SHARE_READ | FILE_SHARE_WRITE

            let creation = if self.create_new {
                1 // CREATE_NEW
            } else if self.truncate && self.create {
                2 // CREATE_ALWAYS
            } else if self.truncate {
                5 // TRUNCATE_EXISTING
            } else if self.create {
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
                    0x80, // FILE_ATTRIBUTE_NORMAL
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
        }
        #[cfg(all(not(target_family = "wasm"), not(windows)))]
        {
            const O_RDONLY: i32 = 0;
            const O_WRONLY: i32 = 1;
            const O_RDWR: i32 = 2;
            const O_CREAT: i32 = 64;
            const O_TRUNC: i32 = 512;
            const O_APPEND: i32 = 1024;
            const O_EXCL: i32 = 128;

            let mut flags = 0;
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
                flags |= O_CREAT | O_EXCL;
            }

            unsafe extern "C" {
                fn open(pathname: *const u8, flags: i32, mode: u32) -> i32;
            }
            let mut path_bytes = alloc::vec::Vec::from(path.as_ref().as_bytes());
            path_bytes.push(0);
            let handle = unsafe { open(path_bytes.as_ptr(), flags, 0o666) };
            if handle < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(File {
                handle: handle as u64,
            })
        }
    }
}

// --- File ---

pub struct File {
    pub(crate) handle: u64,
}

impl File {
    pub fn options() -> OpenOptions {
        OpenOptions::new()
    }

    pub async fn open<P: AsRef<str>>(path: P) -> io::Result<Self> {
        OpenOptions::new().read(true).open(path).await
    }

    pub async fn create<P: AsRef<str>>(path: P) -> io::Result<Self> {
        OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)
            .await
    }

    pub fn metadata(&self) -> io::Result<Metadata> {
        Err(io::Error::other("not implemented"))
    }

    pub fn as_raw_fd(&self) -> i32 {
        self.handle as i32
    }

    pub fn try_clone(&self) -> io::Result<Self> {
        #[cfg(unix)]
        {
            unsafe extern "C" {
                fn dup(oldfd: i32) -> i32;
            }
            let new_fd = unsafe { dup(self.handle as i32) };
            if new_fd < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(File {
                handle: new_fd as u64,
            })
        }
        #[cfg(windows)]
        {
            // Simplified: we'd need DuplicateHandle, but let's just use stubs for now if complicated.
            // Actually let's just return unimplemented for windows if we don't want to bring in winapi.
            Err(io::Error::other("try_clone not implemented on Windows yet"))
        }
        #[cfg(target_family = "wasm")]
        {
            Err(io::Error::other("try_clone not implemented on WASM"))
        }
    }

    pub fn is_terminal(&self) -> bool {
        #[cfg(unix)]
        {
            unsafe extern "C" {
                fn isatty(fd: i32) -> i32;
            }
            unsafe { isatty(self.handle as i32) != 0 }
        }
        #[cfg(windows)]
        {
            // Stub for now.
            false
        }
        #[cfg(target_family = "wasm")]
        {
            false
        }
    }
}

impl io::Write for File {
    fn write(&mut self, _buf: &[u8]) -> io::Result<usize> {
        #[cfg(unix)]
        {
            unsafe extern "C" {
                fn write(fd: i32, buf: *const u8, count: usize) -> isize;
            }
            let res = unsafe { write(self.handle as i32, _buf.as_ptr(), _buf.len()) };
            if res < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(res as usize)
        }
        #[cfg(not(unix))]
        {
            Err(io::Error::other("sync write not implemented"))
        }
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

impl io::Read for File {
    fn read(&mut self, _buf: &mut [u8]) -> io::Result<usize> {
        #[cfg(unix)]
        {
            unsafe extern "C" {
                fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
            }
            let res = unsafe { read(self.handle as i32, _buf.as_mut_ptr(), _buf.len()) };
            if res < 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(res as usize)
        }
        #[cfg(not(unix))]
        {
            Err(io::Error::other("sync read not implemented"))
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
            #[allow(clashing_extern_declarations)]
            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn ReadFile(
                    hFile: usize,
                    lpBuffer: *mut u8,
                    nNumberOfBytesToRead: u32,
                    lpNumberOfBytesRead: *mut u32,
                    lpOverlapped: *mut crate::rt::windows::Overlapped,
                ) -> i32;
            }

            let mut buf = buf;
            let mut bytes_read = 0u32;
            let res = unsafe {
                ReadFile(
                    self.handle as usize,
                    buf.as_mut_ptr(),
                    buf.capacity() as u32,
                    &mut bytes_read,
                    core::ptr::null_mut(),
                )
            };

            if res == 0 {
                let err = io::Error::last_os_error();
                // EOF might be ERROR_HANDLE_EOF which is 38
                if err.raw_os_error() == Some(38) {
                    unsafe {
                        buf.set_len(0);
                    }
                    (Ok(0), buf)
                } else {
                    (Err(err), buf)
                }
            } else {
                unsafe {
                    buf.set_len(bytes_read as usize);
                }
                (Ok(bytes_read as usize), buf)
            }
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
            let len = buf.len() as u32;
            let (err, written, _, buf) =
                crate::rt::wasm::OverlappedBufferFuture::new(buf, move |ov, ptr, _| {
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
            #[allow(clashing_extern_declarations)]
            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn WriteFile(
                    hFile: usize,
                    lpBuffer: *const u8,
                    nNumberOfBytesToWrite: u32,
                    lpNumberOfBytesWritten: *mut u32,
                    lpOverlapped: *mut core::ffi::c_void,
                ) -> i32;
            }

            let mut bytes_written = 0u32;
            let res = unsafe {
                WriteFile(
                    self.handle as usize,
                    buf.as_ptr(),
                    buf.len() as u32,
                    &mut bytes_written,
                    core::ptr::null_mut(),
                )
            };

            if res == 0 {
                (Err(io::Error::last_os_error()), buf)
            } else {
                (Ok(bytes_written as usize), buf)
            }
        }
        #[cfg(all(not(target_family = "wasm"), not(windows)))]
        {
            crate::rt::linux_op::LinuxOpFuture::write(self.handle as i32, buf).await
        }
    }
    async fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

// --- Global Fns ---

pub async fn metadata<P: AsRef<str>>(path: P) -> io::Result<Metadata> {
    #[cfg(target_family = "wasm")]
    {
        let path_bytes = path.as_ref().as_bytes().to_vec();
        let (err, handle, _, _) =
            crate::rt::wasm::OverlappedBufferFuture::new(path_bytes, |ov, ptr, len| {
                unsafe { crate::abi::imports::path_stat(ov, ptr, len) };
            })
            .await;
        if err != 0 {
            return Err(io::Error::from_raw_os_error(err as i32));
        }
        Ok(Metadata { handle })
    }
    #[cfg(windows)]
    {
        let mut wchars: alloc::vec::Vec<u16> = path.as_ref().encode_utf16().collect();
        wchars.push(0);

        #[repr(C)]
        #[allow(non_snake_case)]
        struct WIN32_FILE_ATTRIBUTE_DATA {
            dwFileAttributes: u32,
            ftCreationTimeLow: u32,
            ftCreationTimeHigh: u32,
            ftLastAccessTimeLow: u32,
            ftLastAccessTimeHigh: u32,
            ftLastWriteTimeLow: u32,
            ftLastWriteTimeHigh: u32,
            nFileSizeHigh: u32,
            nFileSizeLow: u32,
        }

        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetFileAttributesExW(
                lpFileName: *const u16,
                fInfoLevelId: u32,
                lpFileInformation: *mut core::ffi::c_void,
            ) -> i32;
        }

        let mut info = WIN32_FILE_ATTRIBUTE_DATA {
            dwFileAttributes: 0,
            ftCreationTimeLow: 0,
            ftCreationTimeHigh: 0,
            ftLastAccessTimeLow: 0,
            ftLastAccessTimeHigh: 0,
            ftLastWriteTimeLow: 0,
            ftLastWriteTimeHigh: 0,
            nFileSizeHigh: 0,
            nFileSizeLow: 0,
        };

        let res = unsafe {
            GetFileAttributesExW(
                wchars.as_ptr(),
                0, // GetFileExInfoStandard
                &mut info as *mut _ as *mut core::ffi::c_void,
            )
        };

        if res == 0 {
            return Err(io::Error::last_os_error());
        }

        let to_unix_ns = |low: u32, high: u32| -> u64 {
            let filetime = ((high as u64) << 32) | (low as u64);
            // FILETIME is 100-nanosecond intervals since Jan 1, 1601
            // Jan 1, 1970 is 116444736000000000 intervals after Jan 1, 1601
            if filetime > 116444736000000000 {
                (filetime - 116444736000000000) * 100
            } else {
                0
            }
        };

        Ok(Metadata {
            size: ((info.nFileSizeHigh as u64) << 32) | (info.nFileSizeLow as u64),
            mode: info.dwFileAttributes,
            modified_time_ns: to_unix_ns(info.ftLastWriteTimeLow, info.ftLastWriteTimeHigh),
            accessed_time_ns: to_unix_ns(info.ftLastAccessTimeLow, info.ftLastAccessTimeHigh),
            created_time_ns: to_unix_ns(info.ftCreationTimeLow, info.ftCreationTimeHigh),
            nlink: 1,
            uid: 0,
            gid: 0,
            inode: 0,
        })
    }
    #[cfg(all(not(target_family = "wasm"), not(windows)))]
    {
        // struct stat layout on Linux aarch64 (kernel stat64 / __NR_newfstatat)
        #[repr(C)]
        struct LinuxStat {
            st_dev: u64,
            st_ino: u64,
            st_mode: u32,
            st_nlink: u32,
            st_uid: u32,
            st_gid: u32,
            st_rdev: u64,
            _pad1: u64,
            st_size: i64,
            st_blksize: i32,
            _pad2: i32,
            st_blocks: i64,
            st_atime: i64,
            st_atime_nsec: i64,
            st_mtime: i64,
            st_mtime_nsec: i64,
            st_ctime: i64,
            st_ctime_nsec: i64,
            _unused: [i32; 2],
        }
        unsafe extern "C" {
            fn stat(pathname: *const u8, statbuf: *mut LinuxStat) -> i32;
        }
        let mut path_bytes = alloc::vec::Vec::from(path.as_ref().as_bytes());
        path_bytes.push(0);
        let mut st = core::mem::MaybeUninit::<LinuxStat>::uninit();
        let ret = unsafe { stat(path_bytes.as_ptr(), st.as_mut_ptr()) };
        if ret < 0 {
            return Err(io::Error::last_os_error());
        }
        let st = unsafe { st.assume_init() };
        let to_ns = |secs: i64, nsec: i64| -> u64 {
            if secs < 0 {
                0
            } else {
                secs as u64 * 1_000_000_000 + nsec as u64
            }
        };
        Ok(Metadata {
            size: st.st_size as u64,
            mode: st.st_mode,
            modified_time_ns: to_ns(st.st_mtime, st.st_mtime_nsec),
            accessed_time_ns: to_ns(st.st_atime, st.st_atime_nsec),
            created_time_ns: to_ns(st.st_ctime, st.st_ctime_nsec),
            nlink: st.st_nlink as u64,
            uid: st.st_uid,
            gid: st.st_gid,
            inode: st.st_ino,
        })
    }
}

pub async fn symlink_metadata<P: AsRef<str>>(path: P) -> io::Result<Metadata> {
    #[cfg(target_family = "wasm")]
    {
        let path_bytes = path.as_ref().as_bytes().to_vec();
        let (err, handle, _, _) =
            crate::rt::wasm::OverlappedBufferFuture::new(path_bytes, |ov, ptr, len| {
                unsafe { crate::abi::imports::path_lstat(ov, ptr, len) };
            })
            .await;
        if err != 0 {
            return Err(io::Error::from_raw_os_error(err as i32));
        }
        Ok(Metadata { handle })
    }
    #[cfg(not(target_family = "wasm"))]
    {
        let _ = path;
        Err(io::Error::other("not implemented"))
    }
}

pub fn metadata_sync<P: AsRef<str>>(_path: P) -> io::Result<Metadata> {
    Err(io::Error::other("not supported"))
}
pub fn symlink_metadata_sync<P: AsRef<str>>(_path: P) -> io::Result<Metadata> {
    Err(io::Error::other("not supported"))
}
pub async fn read_dir<P: AsRef<str>>(_path: P) -> io::Result<ReadDir> {
    Err(io::Error::other("not implemented"))
}
pub fn read_dir_sync<P: AsRef<str>>(_path: P) -> io::Result<ReadDir> {
    Err(io::Error::other("not implemented"))
}
pub async fn remove_file<P: AsRef<str>>(_path: P) -> io::Result<()> {
    Err(io::Error::other("not implemented"))
}
pub async fn rename<P: AsRef<str>, Q: AsRef<str>>(_from: P, _to: Q) -> io::Result<()> {
    Err(io::Error::other("not implemented"))
}
pub async fn create_dir_all<P: AsRef<str>>(_path: P) -> io::Result<()> {
    Err(io::Error::other("not implemented"))
}
pub async fn canonicalize<P: AsRef<str>>(_path: P) -> io::Result<crate::path::PathBuf> {
    Err(io::Error::other("not implemented"))
}
pub fn open_null_file() -> io::Result<File> {
    Err(io::Error::other("not implemented"))
}
pub fn default_case_insensitive_path_expansion() -> bool {
    false
}
pub fn resolve_executable<P: AsRef<str>>(_path: P) -> Option<String> {
    None
}
pub fn split_path_for_pattern(_path: &str) -> Vec<&str> {
    Vec::new()
}
pub fn pattern_path_root(_path: &str) -> Option<String> {
    None
}
pub fn normalize_path_separators(path: &str) -> alloc::borrow::Cow<'_, str> {
    alloc::borrow::Cow::Borrowed(path)
}
pub fn push_path_for_pattern(path: &mut crate::path::PathBuf, piece: &str) {
    let mut s = path.to_string();
    if !s.is_empty() && !s.ends_with('/') && !s.ends_with('\\') {
        s.push('/');
    }
    s.push_str(piece);
    *path = crate::path::PathBuf::from(s);
}
