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
        #[cfg(all(not(target_family = "wasm"), not(windows)))]
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

                unsafe extern "C" {
                    fn open(pathname: *const u8, flags: i32, mode: u32) -> i32;
                }
                let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
                path_bytes.push(0);
                let handle = unsafe { open(path_bytes.as_ptr(), flags, 0o666) };
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
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            let mut wchars: alloc::vec::Vec<u16> = path.encode_utf16().collect();
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
        })
        .await
    }
    #[cfg(all(not(target_family = "wasm"), not(windows)))]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
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
            let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
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
        })
        .await
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
    #[cfg(windows)]
    {
        metadata(path).await
    }
    #[cfg(all(not(target_family = "wasm"), not(windows)))]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
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
                fn lstat(pathname: *const u8, statbuf: *mut LinuxStat) -> i32;
            }
            let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
            path_bytes.push(0);
            let mut st = core::mem::MaybeUninit::<LinuxStat>::uninit();
            let ret = unsafe { lstat(path_bytes.as_ptr(), st.as_mut_ptr()) };
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
        })
        .await
    }
}

pub async fn canonicalize<P: AsRef<str>>(path: P) -> io::Result<crate::path::PathBuf> {
    #[cfg(target_family = "wasm")]
    {
        let _ = path;
        Err(io::Error::other("not implemented"))
    }
    #[cfg(windows)]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            let mut wchars: alloc::vec::Vec<u16> = path.encode_utf16().collect();
            wchars.push(0);

            #[link(name = "kernel32", kind = "raw-dylib")]
            unsafe extern "system" {
                fn GetFullPathNameW(
                    lpFileName: *const u16,
                    nBufferLength: u32,
                    lpBuffer: *mut u16,
                    lpFilePart: *mut *mut u16,
                ) -> u32;
            }

            let mut out_buf = [0u16; 260];
            let res = unsafe {
                GetFullPathNameW(
                    wchars.as_ptr(),
                    260,
                    out_buf.as_mut_ptr(),
                    core::ptr::null_mut(),
                )
            };
            if res == 0 {
                return Err(io::Error::last_os_error());
            }
            if res > 260 {
                let mut v = alloc::vec![0u16; res as usize];
                let res2 = unsafe {
                    GetFullPathNameW(wchars.as_ptr(), res, v.as_mut_ptr(), core::ptr::null_mut())
                };
                if res2 == 0 {
                    return Err(io::Error::last_os_error());
                }
                Ok(crate::path::PathBuf::from(
                    alloc::string::String::from_utf16_lossy(&v[..res2 as usize]),
                ))
            } else {
                Ok(crate::path::PathBuf::from(
                    alloc::string::String::from_utf16_lossy(&out_buf[..res as usize]),
                ))
            }
        })
        .await
    }
    #[cfg(all(not(target_family = "wasm"), not(windows)))]
    {
        let path = crate::string::String::from(path.as_ref());
        crate::rt::blocking::BlockingOpFuture::new(move || {
            let mut path_bytes = alloc::vec::Vec::from(path.as_bytes());
            path_bytes.push(0);

            unsafe extern "C" {
                fn realpath(path: *const u8, resolved: *mut u8) -> *mut u8;
                fn free(ptr: *mut u8);
            }

            let res = unsafe { realpath(path_bytes.as_ptr(), core::ptr::null_mut()) };
            if res.is_null() {
                return Err(io::Error::last_os_error());
            }

            let mut len = 0;
            while unsafe { *res.add(len) } != 0 {
                len += 1;
            }
            let s = unsafe { core::slice::from_raw_parts(res, len) };
            let s = alloc::string::String::from_utf8_lossy(s).into_owned();
            unsafe { free(res) };
            Ok(crate::path::PathBuf::from(s))
        })
        .await
    }
}

pub async fn read_dir<P: AsRef<str>>(path: P) -> io::Result<ReadDir> {
    #[cfg(target_family = "wasm")]
    {
        let path_bytes = path.as_ref().as_bytes().to_vec();
        let (err, _, _, entries_buf) =
            crate::rt::wasm::OverlappedBufferFuture::new(path_bytes, |ov, ptr, len| {
                unsafe { crate::abi::imports::dir_read(ov, ptr, len) };
            })
            .await;

        if err != 0 {
            return Err(io::Error::from_raw_os_error(err as i32));
        }

        // Format is: [len:u32] [name:utf8...] [len:u32] [name:utf8...]
        let mut entries = Vec::new();
        let mut pos = 0;
        while pos + 4 <= entries_buf.len() {
            let len = u32::from_le_bytes(entries_buf[pos..pos + 4].try_into().unwrap()) as usize;
            pos += 4;
            if pos + len > entries_buf.len() {
                break;
            }
            let name = String::from_utf8_lossy(&entries_buf[pos..pos + len]).into_owned();
            pos += len;
            entries.push(DirEntry {
                name,
                metadata: None,
            });
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
                fn FindFirstFileW(lpFileName: *const u16, lpFindFileData: *mut WIN32_FIND_DATAW) -> usize;
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
                            created_time_ns: to_unix_ns(
                                d.ftCreationTimeLow,
                                d.ftCreationTimeHigh,
                            ),
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

            unsafe extern "C" {
                fn opendir(name: *const u8) -> *mut core::ffi::c_void;
                fn readdir(dirp: *mut core::ffi::c_void) -> *mut Dirent;
                fn closedir(dirp: *mut core::ffi::c_void) -> i32;
            }

            #[repr(C)]
            struct Dirent {
                d_ino: u64,
                d_off: i64,
                d_reclen: u16,
                d_type: u8,
                d_name: [u8; 256],
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
                let name = alloc::string::String::from_utf8_lossy(&ent.d_name[..len]).into_owned();
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
        })
        .await
    }
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

