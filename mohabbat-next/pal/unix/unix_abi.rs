#![allow(dead_code)]

use core::ptr;

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
use crate::linux_syscall as sys;

// ABI error codes matching pal/abi/guest.rs
pub const ABI_OK: u32 = 0;
pub const ABI_ERROR_IO: u32 = 1;
pub const ABI_ERROR_NOT_FOUND: u32 = 2;
pub const ABI_ERROR_ACCESS_DENIED: u32 = 3;
pub const ABI_ERROR_ALREADY_EXISTS: u32 = 4;
pub const ABI_ERROR_INVALID_INPUT: u32 = 5;
pub const ABI_ERROR_WOULD_BLOCK: u32 = 6;
pub const ABI_ERROR_NOT_SUPPORTED: u32 = 7;
pub const ABI_ERROR_CANCELLED: u32 = 8;
pub const ABI_ERROR_CONNREFUSED: u32 = 9;
pub const ABI_ERROR_TIMEDOUT: u32 = 10;

pub const ABI_OS_LINUX: u32 = 1;
pub const ABI_OS_DARWIN: u32 = 2;
pub const ABI_CPU_X86_64: u32 = 1;
pub const ABI_CPU_AARCH64: u32 = 2;

pub const ABI_STAT_KIND_FILE: u32 = 1;
pub const ABI_STAT_KIND_DIR: u32 = 2;
pub const ABI_STAT_KIND_SYMLINK: u32 = 3;
pub const ABI_STAT_KIND_OTHER: u32 = 4;

pub const ABI_OPEN_READ: u32 = 1 << 0;
pub const ABI_OPEN_WRITE: u32 = 1 << 1;
pub const ABI_OPEN_CREATE: u32 = 1 << 2;
pub const ABI_OPEN_EXCL: u32 = 1 << 3;
pub const ABI_OPEN_TRUNC: u32 = 1 << 4;
pub const ABI_OPEN_APPEND: u32 = 1 << 5;
pub const ABI_OPEN_DIRECTORY: u32 = 1 << 6;

pub const ABI_PATH_FOLLOW_SYMLINKS: u32 = 1 << 0;
pub const ABI_PATH_REMOVE_DIR: u32 = 1 << 1;

pub const ABI_NET_LISTEN: u32 = 1 << 0;
pub const ABI_NET_UDP: u32 = 1 << 1;
pub const ABI_NET_IPV6: u32 = 1 << 2;

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct AbiDirEntryHeader {
    pub ino: u32,
    pub kind: u16,
    pub name_len: u16,
}

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct AbiStat {
    pub dev: u64,
    pub ino: u64,
    pub kind: u32,
    pub mode: u32,
    pub nlink: u32,
    pub _pad0: u32,
    pub size: u64,
    pub atime_sec: i64,
    pub atime_nsec: u32,
    pub _pad1: u32,
    pub mtime_sec: i64,
    pub mtime_nsec: u32,
    pub _pad2: u32,
    pub ctime_sec: i64,
    pub ctime_nsec: u32,
    pub _pad3: u32,
    pub uid: u32,
    pub gid: u32,
    pub blksize: u32,
    pub _pad4: u32,
    pub blocks: u64,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct AbiPlatformInfo {
    pub os_kind: u32,
    pub cpu_type: u32,
    pub page_size: u32,
    pub pid: u32,
    pub hardware_concurrency: u32,
    pub _reserved: [u32; 187],
}

impl Default for AbiPlatformInfo {
    fn default() -> Self {
        Self {
            os_kind: 0,
            cpu_type: 0,
            page_size: 4096,
            pid: 0,
            hardware_concurrency: 1,
            _reserved: [0; 187],
        }
    }
}

const _: () = {
    assert!(core::mem::size_of::<AbiDirEntryHeader>() == 8);
    assert!(core::mem::size_of::<AbiStat>() == 112);
    assert!(core::mem::size_of::<AbiPlatformInfo>() == 768);
};

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
pub fn error_from_errno(errno: i32) -> u32 {
    match errno {
        0 => ABI_OK,
        sys::ENOENT | sys::ENOTDIR => ABI_ERROR_NOT_FOUND,
        sys::EPERM | sys::EACCES => ABI_ERROR_ACCESS_DENIED,
        sys::EEXIST | sys::ENOTEMPTY => ABI_ERROR_ALREADY_EXISTS,
        sys::EINVAL | sys::EBADF | sys::EISDIR => ABI_ERROR_INVALID_INPUT,
        sys::EAGAIN => ABI_ERROR_WOULD_BLOCK,
        sys::ENOSYS => ABI_ERROR_NOT_SUPPORTED,
        sys::ECONNREFUSED => ABI_ERROR_CONNREFUSED,
        sys::ETIMEDOUT => ABI_ERROR_TIMEDOUT,
        _ => ABI_ERROR_IO,
    }
}

#[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
pub fn error_from_errno(errno: i32) -> u32 {
    match errno {
        0 => ABI_OK,
        2 | 20 => ABI_ERROR_NOT_FOUND,
        1 | 13 => ABI_ERROR_ACCESS_DENIED,
        17 | 66 => ABI_ERROR_ALREADY_EXISTS,
        22 | 9 | 21 => ABI_ERROR_INVALID_INPUT,
        35 | 11 => ABI_ERROR_WOULD_BLOCK,
        78 | 38 => ABI_ERROR_NOT_SUPPORTED,
        61 | 111 => ABI_ERROR_CONNREFUSED,
        60 | 110 => ABI_ERROR_TIMEDOUT,
        _ => ABI_ERROR_IO,
    }
}

#[inline(always)]
pub unsafe fn flush_icache_impl(ptr: *const u8, size: usize) {
    #[cfg(target_arch = "aarch64")]
    {
        if size == 0 {
            return;
        }
        let start = ptr as usize;
        let end = start.saturating_add(size);
        let ctr: u64;
        core::arch::asm!("mrs {0}, ctr_el0", out(reg) ctr);
        let dline = 4usize << ((ctr >> 16) & 0xf);
        let iline = 4usize << (ctr & 0xf);
        let mut addr = start & !(dline - 1);
        while addr < end {
            core::arch::asm!("dc cvau, {0}", in(reg) addr, options(nostack));
            addr += dline;
        }
        core::arch::asm!("dsb ish", options(nostack));
        addr = start & !(iline - 1);
        while addr < end {
            core::arch::asm!("ic ivau, {0}", in(reg) addr, options(nostack));
            addr += iline;
        }
        core::arch::asm!("dsb ish", "isb", options(nostack));
    }
    #[cfg(not(target_arch = "aarch64"))]
    let _ = (ptr, size);
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
pub unsafe fn copy_cstr(ptr: *const u8, len: usize, buf: &mut [u8; 4096]) -> Option<*const u8> {
    if ptr.is_null() || len >= buf.len() {
        return None;
    }
    ptr::copy_nonoverlapping(ptr, buf.as_mut_ptr(), len);
    buf[len] = 0;
    Some(buf.as_ptr())
}

#[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
pub unsafe fn linux_stat_to_abi(st: &sys::LinuxStat) -> AbiStat {
    let ftype = st.st_mode & 0o170000;
    let kind = match ftype {
        0o100000 => ABI_STAT_KIND_FILE,
        0o040000 => ABI_STAT_KIND_DIR,
        0o120000 => ABI_STAT_KIND_SYMLINK,
        _ => ABI_STAT_KIND_OTHER,
    };
    AbiStat {
        dev: st.st_dev,
        ino: st.st_ino,
        kind,
        mode: st.st_mode & 0o7777,
        nlink: st.st_nlink as u32,
        _pad0: 0,
        size: st.st_size.max(0) as u64,
        atime_sec: st.st_atime,
        atime_nsec: st.st_atime_nsec as u32,
        _pad1: 0,
        mtime_sec: st.st_mtime,
        mtime_nsec: st.st_mtime_nsec as u32,
        _pad2: 0,
        ctime_sec: st.st_ctime,
        ctime_nsec: st.st_ctime_nsec as u32,
        _pad3: 0,
        uid: st.st_uid,
        gid: st.st_gid,
        blksize: st.st_blksize as u32,
        _pad4: 0,
        blocks: st.st_blocks.max(0) as u64,
    }
}

pub fn pack_env_entries(
    envp: *const *const u8,
    envc: usize,
    buf_ptr: *mut u8,
    buf_cap: usize,
) -> u64 {
    if envc > u32::MAX as usize || (envc != 0 && envp.is_null()) {
        return u64::MAX;
    }
    let mut total: usize = 4;
    let mut i = 0;
    unsafe {
        while i < envc {
            let s = *envp.add(i);
            let mut len = 0usize;
            if !s.is_null() {
                while *s.add(len) != 0 {
                    len += 1;
                }
            }
            total = match total.checked_add(4 + len) {
                Some(v) => v,
                None => return u64::MAX,
            };
            i += 1;
        }
        if buf_ptr.is_null() || buf_cap < total {
            return total as u64;
        }
        ptr::copy_nonoverlapping((envc as u32).to_le_bytes().as_ptr(), buf_ptr, 4);
        let mut off = 4usize;
        i = 0;
        while i < envc {
            let s = *envp.add(i);
            let mut len = 0usize;
            if !s.is_null() {
                while *s.add(len) != 0 {
                    len += 1;
                }
            }
            ptr::copy_nonoverlapping((len as u32).to_le_bytes().as_ptr(), buf_ptr.add(off), 4);
            off += 4;
            if len > 0 {
                ptr::copy_nonoverlapping(s, buf_ptr.add(off), len);
                off += len;
            }
            i += 1;
        }
    }
    total as u64
}

pub unsafe fn fill_platform_info(out_ptr: *mut u8, out_cap: usize, pid: u32) -> u32 {
    if out_ptr.is_null() || out_cap < core::mem::size_of::<AbiPlatformInfo>() {
        return ABI_ERROR_INVALID_INPUT;
    }
    let mut info = AbiPlatformInfo::default();
    #[cfg(feature = "darwin")]
    {
        info.os_kind = ABI_OS_DARWIN;
    }
    #[cfg(not(feature = "darwin"))]
    {
        info.os_kind = ABI_OS_LINUX;
    }
    #[cfg(target_arch = "x86_64")]
    {
        info.cpu_type = ABI_CPU_X86_64;
    }
    #[cfg(target_arch = "aarch64")]
    {
        info.cpu_type = ABI_CPU_AARCH64;
    }
    info.pid = pid;
    info.page_size = 4096;
    info.hardware_concurrency = 4;
    ptr::write_unaligned(out_ptr.cast::<AbiPlatformInfo>(), info);
    ABI_OK
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn abi_structs_match_spec_sizes() {
        assert_eq!(core::mem::size_of::<AbiDirEntryHeader>(), 8);
        assert_eq!(core::mem::size_of::<AbiStat>(), 112);
        assert_eq!(core::mem::size_of::<AbiPlatformInfo>(), 768);
    }

    #[test]
    fn errno_mapping_covers_common_codes() {
        assert_eq!(error_from_errno(0), ABI_OK);
        assert_eq!(error_from_errno(2), ABI_ERROR_NOT_FOUND);
        assert_eq!(error_from_errno(13), ABI_ERROR_ACCESS_DENIED);
        assert_eq!(error_from_errno(17), ABI_ERROR_ALREADY_EXISTS);
        assert_eq!(error_from_errno(22), ABI_ERROR_INVALID_INPUT);
    }
}
