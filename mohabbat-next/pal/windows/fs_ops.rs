#![allow(dead_code)]

use core::ffi::c_void;

use crate::pal_abi::{
    AbiStat, Overlapped, ERROR_BADF, ERROR_INVALID, ERROR_IO, ERROR_NOENT, ERROR_NOMEM,
    PATH_REMOVE_FLAG_DIR, PATH_STAT_FLAG_NOFOLLOW, STAT_KIND_DIR, STAT_KIND_FILE,
    STAT_KIND_SYMLINK,
};
use crate::win_ffi::{resolve_kernel32, utf8_to_wide_alloc};
use crate::{complete_io, find_file_handle, map_windows_error, state_ptr};

const INVALID_HANDLE_VALUE: *mut c_void = (-1isize) as *mut c_void;
const FILE_ATTRIBUTE_DIRECTORY: u32 = 0x10;
const FILE_ATTRIBUTE_REPARSE_POINT: u32 = 0x400;

#[repr(C)]
struct Win32FileAttributeData {
    attributes: u32,
    creation_low: u32,
    creation_high: u32,
    access_low: u32,
    access_high: u32,
    write_low: u32,
    write_high: u32,
    size_high: u32,
    size_low: u32,
}

#[repr(C)]
struct Win32FindDataW {
    attributes: u32,
    creation_low: u32,
    creation_high: u32,
    access_low: u32,
    access_high: u32,
    write_low: u32,
    write_high: u32,
    size_high: u32,
    size_low: u32,
    reserved0: u32,
    reserved1: u32,
    file_name: [u16; 260],
    alt_name: [u16; 14],
}

#[repr(C)]
struct WinFileTime {
    low: u32,
    high: u32,
}

#[cfg(target_arch = "x86_64")]
type GetFileAttributesExWFn = unsafe extern "win64" fn(*const u16, u32, *mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type GetFileAttributesExWFn = unsafe extern "C" fn(*const u16, u32, *mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type CreateHardLinkWFn = unsafe extern "win64" fn(*const u16, *const u16, *mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type CreateHardLinkWFn = unsafe extern "C" fn(*const u16, *const u16, *mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type CreateSymbolicLinkWFn = unsafe extern "win64" fn(*const u16, *const u16, u32) -> u8;
#[cfg(target_arch = "aarch64")]
type CreateSymbolicLinkWFn = unsafe extern "C" fn(*const u16, *const u16, u32) -> u8;

#[cfg(target_arch = "x86_64")]
type SetFileAttributesWFn = unsafe extern "win64" fn(*const u16, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type SetFileAttributesWFn = unsafe extern "C" fn(*const u16, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type DeleteFileWFn = unsafe extern "win64" fn(*const u16) -> i32;
#[cfg(target_arch = "aarch64")]
type DeleteFileWFn = unsafe extern "C" fn(*const u16) -> i32;

#[cfg(target_arch = "x86_64")]
type RemoveDirectoryWFn = unsafe extern "win64" fn(*const u16) -> i32;
#[cfg(target_arch = "aarch64")]
type RemoveDirectoryWFn = unsafe extern "C" fn(*const u16) -> i32;

#[cfg(target_arch = "x86_64")]
type CreateDirectoryWFn = unsafe extern "win64" fn(*const u16, *mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type CreateDirectoryWFn = unsafe extern "C" fn(*const u16, *mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type MoveFileExWFn = unsafe extern "win64" fn(*const u16, *const u16, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type MoveFileExWFn = unsafe extern "C" fn(*const u16, *const u16, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type SetFileTimeFn =
    unsafe extern "win64" fn(*mut c_void, *const WinFileTime, *const WinFileTime, *const WinFileTime) -> i32;
#[cfg(target_arch = "aarch64")]
type SetFileTimeFn =
    unsafe extern "C" fn(*mut c_void, *const WinFileTime, *const WinFileTime, *const WinFileTime) -> i32;

#[cfg(target_arch = "x86_64")]
type GetFinalPathNameByHandleWFn =
    unsafe extern "win64" fn(*mut c_void, *mut u16, u32, u32) -> u32;
#[cfg(target_arch = "aarch64")]
type GetFinalPathNameByHandleWFn = unsafe extern "C" fn(*mut c_void, *mut u16, u32, u32) -> u32;

#[cfg(target_arch = "x86_64")]
type FindFirstFileWFn = unsafe extern "win64" fn(*const u16, *mut Win32FindDataW) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type FindFirstFileWFn = unsafe extern "C" fn(*const u16, *mut Win32FindDataW) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type FindNextFileWFn = unsafe extern "win64" fn(*mut c_void, *mut Win32FindDataW) -> i32;
#[cfg(target_arch = "aarch64")]
type FindNextFileWFn = unsafe extern "C" fn(*mut c_void, *mut Win32FindDataW) -> i32;

#[cfg(target_arch = "x86_64")]
type FindCloseFn = unsafe extern "win64" fn(*mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type FindCloseFn = unsafe extern "C" fn(*mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type WideCharToMultiByteFn =
    unsafe extern "win64" fn(u32, u32, *const u16, i32, *mut u8, i32, *const u8, *mut i32) -> i32;
#[cfg(target_arch = "aarch64")]
type WideCharToMultiByteFn =
    unsafe extern "C" fn(u32, u32, *const u16, i32, *mut u8, i32, *const u8, *mut i32) -> i32;

unsafe fn last_err() -> u32 {
    if let Some(gle) = (*state_ptr()).get_last_error {
        map_windows_error(gle())
    } else {
        ERROR_IO
    }
}

unsafe fn free_wide(w: *mut u16) {
    if !w.is_null() {
        if let Some(free) = (*state_ptr()).free {
            free(w.cast());
        }
    }
}

fn filetime_to_unix_ns(low: u32, high: u32) -> (i64, u32) {
    let ticks = ((high as u64) << 32) | low as u64;
    const EPOCH_DIFF_100NS: u64 = 116_444_736_000_000_000;
    if ticks < EPOCH_DIFF_100NS {
        return (0, 0);
    }
    let rel = ticks - EPOCH_DIFF_100NS;
    ((rel / 10_000_000) as i64, ((rel % 10_000_000) * 100) as u32)
}

fn unix_to_filetime(sec: i64, nsec: u32) -> WinFileTime {
    const EPOCH_DIFF_100NS: i64 = 116_444_736_000_000_000;
    let ticks = (sec.saturating_mul(10_000_000))
        .saturating_add((nsec / 100) as i64)
        .saturating_add(EPOCH_DIFF_100NS) as u64;
    WinFileTime {
        low: ticks as u32,
        high: (ticks >> 32) as u32,
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_dir_read(
    ov: *mut Overlapped,
    handle: u64,
    buf: *mut u8,
    len: usize,
) {
    dir_read_impl(ov, handle, buf, len);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_dir_read(
    ov: *mut Overlapped,
    handle: u64,
    buf: *mut u8,
    len: usize,
) {
    dir_read_impl(ov, handle, buf, len);
}

unsafe fn dir_read_impl(ov: *mut Overlapped, handle: u64, buf: *mut u8, len: usize) {
    if ov.is_null() || buf.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file = find_file_handle(handle);
    if file.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    if (*file).dir_done && (*file).dir_pending_len == 0 {
        complete_io(ov, 0, 0);
        return;
    }

    let mut written = 0usize;
    if (*file).dir_pending_len > 0 {
        let plen = (*file).dir_pending_len as usize;
        let need = 1 + plen + 1;
        if need > len {
            complete_io(ov, ERROR_INVALID, 0);
            return;
        }
        buf.write((*file).dir_pending_kind);
        let mut k = 0usize;
        while k < plen {
            buf.add(1 + k).write((*file).dir_pending_name[k]);
            k += 1;
        }
        buf.add(1 + plen).write(0);
        written += need;
        (*file).dir_pending_len = 0;
    }

    if (*file).dir_done {
        complete_io(ov, 0, written as u64);
        return;
    }

    let p_gfp = resolve_kernel32(b"GetFinalPathNameByHandleW\0");
    let p_fff = resolve_kernel32(b"FindFirstFileW\0");
    let p_fnf = resolve_kernel32(b"FindNextFileW\0");
    let p_fc = resolve_kernel32(b"FindClose\0");
    let p_w2m = resolve_kernel32(b"WideCharToMultiByte\0");
    if p_gfp.is_null() || p_fff.is_null() || p_fnf.is_null() || p_fc.is_null() || p_w2m.is_null()
    {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let gfp: GetFinalPathNameByHandleWFn = core::mem::transmute(p_gfp);
    let fff: FindFirstFileWFn = core::mem::transmute(p_fff);
    let fnf: FindNextFileWFn = core::mem::transmute(p_fnf);
    let fc: FindCloseFn = core::mem::transmute(p_fc);
    let w2m: WideCharToMultiByteFn = core::mem::transmute(p_w2m);

    let mut data: Win32FindDataW = core::mem::zeroed();
    let mut have_entry = false;
    if (*file).dir_find_handle.is_null() {
        let mut path_w = [0u16; 520];
        let n = gfp((*file).native_handle, path_w.as_mut_ptr(), 516, 0);
        if n == 0 || n as usize >= 516 {
            complete_io(ov, last_err(), 0);
            return;
        }
        let mut idx = n as usize;
        path_w.as_mut_ptr().add(idx).write(b'\\' as u16);
        idx += 1;
        path_w.as_mut_ptr().add(idx).write(b'*' as u16);
        idx += 1;
        path_w.as_mut_ptr().add(idx).write(0);

        let find_h = fff(path_w.as_ptr(), &mut data);
        if find_h == INVALID_HANDLE_VALUE || find_h.is_null() {
            let err = last_err();
            if err == ERROR_NOENT {
                (*file).dir_done = true;
                complete_io(ov, 0, written as u64);
                return;
            }
            complete_io(ov, err, 0);
            return;
        }
        (*file).dir_find_handle = find_h;
        have_entry = true;
    }

    loop {
        if !have_entry {
            if fnf((*file).dir_find_handle, &mut data) == 0 {
                let _ = fc((*file).dir_find_handle);
                (*file).dir_find_handle = core::ptr::null_mut();
                (*file).dir_done = true;
                break;
            }
        }
        have_entry = false;

        let mut wlen = 0i32;
        while (wlen as usize) < 260 && *data.file_name.as_ptr().add(wlen as usize) != 0 {
            wlen += 1;
        }
        let mut name_utf8 = [0u8; 520];
        let u8len = w2m(
            65001,
            0,
            data.file_name.as_ptr(),
            wlen,
            name_utf8.as_mut_ptr(),
            520,
            core::ptr::null(),
            core::ptr::null_mut(),
        );
        if u8len > 0 {
            let ulen = u8len as usize;
            if (ulen == 1 && name_utf8[0] == b'.')
                || (ulen == 2 && name_utf8[0] == b'.' && name_utf8[1] == b'.')
            {
                continue;
            }
            let kind = if data.attributes & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
                STAT_KIND_SYMLINK as u8
            } else if data.attributes & FILE_ATTRIBUTE_DIRECTORY != 0 {
                STAT_KIND_DIR as u8
            } else {
                STAT_KIND_FILE as u8
            };
            let need = 1 + ulen + 1;
            if written + need > len {
                (*file).dir_pending_kind = kind;
                (*file).dir_pending_len = ulen as u16;
                let mut k = 0usize;
                while k < ulen {
                    (*file).dir_pending_name[k] = name_utf8[k];
                    k += 1;
                }
                break;
            }
            buf.add(written).write(kind);
            let mut k = 0usize;
            while k < ulen {
                buf.add(written + 1 + k).write(*name_utf8.as_ptr().add(k));
                k += 1;
            }
            buf.add(written + 1 + ulen).write(0);
            written += need;
        }
    }
    complete_io(ov, 0, written as u64);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_stat(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    flags: u32,
    out: *mut u8,
    cap: usize,
) {
    path_stat_impl(ov, path, len, flags, out, cap);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_stat(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    flags: u32,
    out: *mut u8,
    cap: usize,
) {
    path_stat_impl(ov, path, len, flags, out, cap);
}

unsafe fn path_stat_impl(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    _flags: u32,
    out: *mut u8,
    cap: usize,
) {
    let need = core::mem::size_of::<AbiStat>();
    if ov.is_null() || out.is_null() || cap < need {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let p_get = resolve_kernel32(b"GetFileAttributesExW\0");
    if p_get.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let wide = crate::win_ffi::path_utf8_to_wide_alloc(path, len);
    if wide.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let get: GetFileAttributesExWFn = core::mem::transmute(p_get);
    let mut data: Win32FileAttributeData = core::mem::zeroed();
    let ok = get(wide, 0, (&mut data as *mut Win32FileAttributeData).cast());
    let err = if ok == 0 { last_err() } else { 0 };
    free_wide(wide);
    if err != 0 {
        complete_io(ov, err, 0);
        return;
    }
    let mut st: AbiStat = core::mem::zeroed();
    st.kind = if data.attributes & FILE_ATTRIBUTE_REPARSE_POINT != 0 {
        STAT_KIND_SYMLINK
    } else if data.attributes & FILE_ATTRIBUTE_DIRECTORY != 0 {
        STAT_KIND_DIR
    } else {
        STAT_KIND_FILE
    };
    st.mode = if st.kind == STAT_KIND_DIR { 0o755 } else { 0o644 };
    st.nlink = 1;
    st.size = ((data.size_high as u64) << 32) | data.size_low as u64;
    let (as_s, as_n) = filetime_to_unix_ns(data.access_low, data.access_high);
    let (ms_s, ms_n) = filetime_to_unix_ns(data.write_low, data.write_high);
    let (cs_s, cs_n) = filetime_to_unix_ns(data.creation_low, data.creation_high);
    st.atime_ns = as_s.saturating_mul(1_000_000_000).saturating_add(as_n as i64);
    st.mtime_ns = ms_s.saturating_mul(1_000_000_000).saturating_add(ms_n as i64);
    st.ctime_ns = cs_s.saturating_mul(1_000_000_000).saturating_add(cs_n as i64);
    st.btime_ns = st.ctime_ns;
    st.blksize = 4096;
    st.blocks = ((st.size + 511) / 512) as i64;
    st.valid_mask = 0xFFFF;
    core::ptr::write(out.cast::<AbiStat>(), st);
    let _ = PATH_STAT_FLAG_NOFOLLOW;
    complete_io(ov, 0, need as u64);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_link(
    ov: *mut Overlapped,
    old: *const u8,
    old_len: usize,
    new: *const u8,
    new_len: usize,
    _flags: u32,
) {
    path_link_impl(ov, old, old_len, new, new_len);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_link(
    ov: *mut Overlapped,
    old: *const u8,
    old_len: usize,
    new: *const u8,
    new_len: usize,
    _flags: u32,
) {
    path_link_impl(ov, old, old_len, new, new_len);
}

unsafe fn path_link_impl(
    ov: *mut Overlapped,
    old: *const u8,
    old_len: usize,
    new: *const u8,
    new_len: usize,
) {
    let p = resolve_kernel32(b"CreateHardLinkW\0");
    if p.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let w_old = utf8_to_wide_alloc(old, old_len);
    let w_new = utf8_to_wide_alloc(new, new_len);
    if w_old.is_null() || w_new.is_null() {
        free_wide(w_old);
        free_wide(w_new);
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let f: CreateHardLinkWFn = core::mem::transmute(p);
    let ok = f(w_new, w_old, core::ptr::null_mut());
    let err = if ok == 0 { last_err() } else { 0 };
    free_wide(w_old);
    free_wide(w_new);
    complete_io(ov, err, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_symlink(
    ov: *mut Overlapped,
    target: *const u8,
    t_len: usize,
    link: *const u8,
    l_len: usize,
    kind: u32,
) {
    path_symlink_impl(ov, target, t_len, link, l_len, kind);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_symlink(
    ov: *mut Overlapped,
    target: *const u8,
    t_len: usize,
    link: *const u8,
    l_len: usize,
    kind: u32,
) {
    path_symlink_impl(ov, target, t_len, link, l_len, kind);
}

unsafe fn path_symlink_impl(
    ov: *mut Overlapped,
    target: *const u8,
    t_len: usize,
    link: *const u8,
    l_len: usize,
    kind: u32,
) {
    let p = resolve_kernel32(b"CreateSymbolicLinkW\0");
    if p.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let w_target = utf8_to_wide_alloc(target, t_len);
    let w_link = utf8_to_wide_alloc(link, l_len);
    if w_target.is_null() || w_link.is_null() {
        free_wide(w_target);
        free_wide(w_link);
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let f: CreateSymbolicLinkWFn = core::mem::transmute(p);
    let flags = if kind == STAT_KIND_DIR { 1 | 2 } else { 2 };
    let ok = f(w_link, w_target, flags);
    let err = if ok == 0 { last_err() } else { 0 };
    free_wide(w_target);
    free_wide(w_link);
    complete_io(ov, err, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_readlink(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    out: *mut u8,
    cap: usize,
) {
    path_readlink_impl(ov, path, len, out, cap);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_readlink(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    out: *mut u8,
    cap: usize,
) {
    path_readlink_impl(ov, path, len, out, cap);
}

unsafe fn path_readlink_impl(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    out: *mut u8,
    cap: usize,
) {
    if ov.is_null() || out.is_null() || cap == 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let Some(cf) = (*state_ptr()).create_file_w else {
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    let p_gfp = resolve_kernel32(b"GetFinalPathNameByHandleW\0");
    let p_w2m = resolve_kernel32(b"WideCharToMultiByte\0");
    if p_gfp.is_null() || p_w2m.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let wide = utf8_to_wide_alloc(path, len);
    if wide.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let h = cf(
        wide,
        0x8000_0000,
        7,
        core::ptr::null_mut(),
        3,
        0x0200_0000,
        core::ptr::null_mut(),
    );
    free_wide(wide);
    if h == INVALID_HANDLE_VALUE || h.is_null() {
        complete_io(ov, last_err(), 0);
        return;
    }
    let gfp: GetFinalPathNameByHandleWFn = core::mem::transmute(p_gfp);
    let w2m: WideCharToMultiByteFn = core::mem::transmute(p_w2m);
    let mut wbuf = [0u16; 512];
    let n = gfp(h, wbuf.as_mut_ptr(), 512, 0);
    if let Some(close) = (*state_ptr()).close_handle {
        let _ = close(h);
    }
    if n == 0 {
        complete_io(ov, last_err(), 0);
        return;
    }
    let written = w2m(
        65001,
        0,
        wbuf.as_ptr(),
        n as i32,
        out,
        cap.min(i32::MAX as usize) as i32,
        core::ptr::null(),
        core::ptr::null_mut(),
    );
    if written <= 0 {
        complete_io(ov, ERROR_NOMEM, 0);
    } else {
        complete_io(ov, 0, written as u64);
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_chmod(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    mode: u32,
) {
    path_chmod_impl(ov, path, len, mode);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_chmod(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    mode: u32,
) {
    path_chmod_impl(ov, path, len, mode);
}

unsafe fn path_chmod_impl(ov: *mut Overlapped, path: *const u8, len: usize, mode: u32) {
    let p = resolve_kernel32(b"SetFileAttributesW\0");
    if p.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let wide = utf8_to_wide_alloc(path, len);
    if wide.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let f: SetFileAttributesWFn = core::mem::transmute(p);
    let attrs = if (mode & 0o200) == 0 { 1 } else { 0x80 };
    let ok = f(wide, attrs);
    let err = if ok == 0 { last_err() } else { 0 };
    free_wide(wide);
    complete_io(ov, err, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_set_times(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    at_sec: i64,
    at_nsec: u32,
    mt_sec: i64,
    mt_nsec: u32,
    _flags: u32,
) {
    path_set_times_impl(ov, path, len, at_sec, at_nsec, mt_sec, mt_nsec);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_set_times(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    at_sec: i64,
    at_nsec: u32,
    mt_sec: i64,
    mt_nsec: u32,
    _flags: u32,
) {
    path_set_times_impl(ov, path, len, at_sec, at_nsec, mt_sec, mt_nsec);
}

unsafe fn path_set_times_impl(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    at_sec: i64,
    at_nsec: u32,
    mt_sec: i64,
    mt_nsec: u32,
) {
    let Some(cf) = (*state_ptr()).create_file_w else {
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    let p_sft = resolve_kernel32(b"SetFileTime\0");
    if p_sft.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let wide = utf8_to_wide_alloc(path, len);
    if wide.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let h = cf(
        wide,
        0x0000_0100, // FILE_WRITE_ATTRIBUTES
        7,
        core::ptr::null_mut(),
        3,
        0x0200_0000,
        core::ptr::null_mut(),
    );
    free_wide(wide);
    if h == INVALID_HANDLE_VALUE || h.is_null() {
        complete_io(ov, last_err(), 0);
        return;
    }
    let sft: SetFileTimeFn = core::mem::transmute(p_sft);
    let at = unix_to_filetime(at_sec, at_nsec);
    let mt = unix_to_filetime(mt_sec, mt_nsec);
    let ok = sft(h, core::ptr::null(), &at, &mt);
    let err = if ok == 0 { last_err() } else { 0 };
    if let Some(close) = (*state_ptr()).close_handle {
        let _ = close(h);
    }
    complete_io(ov, err, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_remove(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    flags: u32,
) {
    path_remove_impl(ov, path, len, flags);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_remove(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    flags: u32,
) {
    path_remove_impl(ov, path, len, flags);
}

unsafe fn path_remove_impl(ov: *mut Overlapped, path: *const u8, len: usize, flags: u32) {
    let sym = if flags & PATH_REMOVE_FLAG_DIR != 0 {
        b"RemoveDirectoryW\0".as_slice()
    } else {
        b"DeleteFileW\0".as_slice()
    };
    let p = resolve_kernel32(if flags & PATH_REMOVE_FLAG_DIR != 0 {
        b"RemoveDirectoryW\0"
    } else {
        b"DeleteFileW\0"
    });
    let _ = sym;
    if p.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let wide = utf8_to_wide_alloc(path, len);
    if wide.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let ok = if flags & PATH_REMOVE_FLAG_DIR != 0 {
        let f: RemoveDirectoryWFn = core::mem::transmute(p);
        f(wide)
    } else {
        let f: DeleteFileWFn = core::mem::transmute(p);
        f(wide)
    };
    let err = if ok == 0 { last_err() } else { 0 };
    free_wide(wide);
    complete_io(ov, err, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_mkdir(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    _mode: u32,
) {
    path_mkdir_impl(ov, path, len);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_mkdir(
    ov: *mut Overlapped,
    path: *const u8,
    len: usize,
    _mode: u32,
) {
    path_mkdir_impl(ov, path, len);
}

unsafe fn path_mkdir_impl(ov: *mut Overlapped, path: *const u8, len: usize) {
    let p = resolve_kernel32(b"CreateDirectoryW\0");
    if p.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let wide = utf8_to_wide_alloc(path, len);
    if wide.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let f: CreateDirectoryWFn = core::mem::transmute(p);
    let ok = f(wide, core::ptr::null_mut());
    let err = if ok == 0 { last_err() } else { 0 };
    free_wide(wide);
    complete_io(ov, err, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_path_rename(
    ov: *mut Overlapped,
    old: *const u8,
    o_len: usize,
    new: *const u8,
    n_len: usize,
) {
    path_rename_impl(ov, old, o_len, new, n_len);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_path_rename(
    ov: *mut Overlapped,
    old: *const u8,
    o_len: usize,
    new: *const u8,
    n_len: usize,
) {
    path_rename_impl(ov, old, o_len, new, n_len);
}

unsafe fn path_rename_impl(
    ov: *mut Overlapped,
    old: *const u8,
    o_len: usize,
    new: *const u8,
    n_len: usize,
) {
    let p = resolve_kernel32(b"MoveFileExW\0");
    if p.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let w_old = utf8_to_wide_alloc(old, o_len);
    let w_new = utf8_to_wide_alloc(new, n_len);
    if w_old.is_null() || w_new.is_null() {
        free_wide(w_old);
        free_wide(w_new);
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let f: MoveFileExWFn = core::mem::transmute(p);
    let ok = f(w_old, w_new, 1); // MOVEFILE_REPLACE_EXISTING
    let err = if ok == 0 { last_err() } else { 0 };
    free_wide(w_old);
    free_wide(w_new);
    complete_io(ov, err, 0);
}
