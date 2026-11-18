#![allow(dead_code)]

use core::ffi::c_void;

use crate::{state_mut, state_ptr, LoadLibraryFn, GetProcAddressFn};

pub(crate) unsafe fn resolve_kernel32(name: &'static [u8]) -> *mut c_void {
    let state = state_ptr();
    let k32 = (*state).kernel32;
    let Some(get_proc) = (*state).get_proc_address else {
        return core::ptr::null_mut();
    };
    if k32.is_null() {
        return core::ptr::null_mut();
    }
    get_proc(k32, name.as_ptr())
}

pub(crate) unsafe fn resolve_dll(
    slot: &mut *mut c_void,
    dll: &'static [u8],
    name: &'static [u8],
) -> *mut c_void {
    let state = state_mut();
    let (Some(load_lib), Some(get_proc)) = (state.load_library, state.get_proc_address) else {
        return core::ptr::null_mut();
    };
    if (*slot).is_null() {
        *slot = load_lib(dll.as_ptr());
    }
    if (*slot).is_null() {
        return core::ptr::null_mut();
    }
    get_proc(*slot, name.as_ptr())
}

pub(crate) unsafe fn resolve_ws2(name: &'static [u8]) -> *mut c_void {
    let mut mod_ptr = (*state_ptr()).ws2_32;
    let p = resolve_dll(&mut mod_ptr, b"ws2_32.dll\0", name);
    (*state_ptr()).ws2_32 = mod_ptr;
    p
}

pub(crate) unsafe fn resolve_bcrypt(name: &'static [u8]) -> *mut c_void {
    let mut mod_ptr = (*state_ptr()).bcrypt;
    let p = resolve_dll(&mut mod_ptr, b"bcrypt.dll\0", name);
    (*state_ptr()).bcrypt = mod_ptr;
    p
}

pub(crate) unsafe fn resolve_crypt32(name: &'static [u8]) -> *mut c_void {
    let mut mod_ptr = (*state_ptr()).crypt32;
    let p = resolve_dll(&mut mod_ptr, b"crypt32.dll\0", name);
    (*state_ptr()).crypt32 = mod_ptr;
    p
}

pub(crate) unsafe fn utf8_to_wide_alloc(utf8: *const u8, len: usize) -> *mut u16 {
    let state = state_ptr();
    let (Some(alloc), Some(free), Some(mb_to_wide)) = (
        (*state).alloc,
        (*state).free,
        (*state).multi_byte_to_wide_char,
    ) else {
        return core::ptr::null_mut();
    };
    if utf8.is_null() || len == 0 || len > i32::MAX as usize {
        return core::ptr::null_mut();
    }
    let wlen = mb_to_wide(65001, 8, utf8, len as i32, core::ptr::null_mut(), 0);
    if wlen <= 0 {
        return core::ptr::null_mut();
    }
    let buf = alloc(((wlen as usize) + 1) * 2).cast::<u16>();
    if buf.is_null() {
        return core::ptr::null_mut();
    }
    let written = mb_to_wide(65001, 8, utf8, len as i32, buf, wlen);
    if written <= 0 {
        free(buf.cast());
        return core::ptr::null_mut();
    }
    buf.add(written as usize).write(0);
    buf
}

pub(crate) unsafe fn path_utf8_to_wide_alloc(utf8: *const u8, len: usize) -> *mut u16 {
    if utf8.is_null() || len == 0 || len > i32::MAX as usize {
        return core::ptr::null_mut();
    }
    let slice = core::slice::from_raw_parts(utf8, len);
    if slice == b"/tmp" || slice.starts_with(b"/tmp/") {
        let p_tmp = resolve_kernel32(b"GetTempPathW\0");
        if !p_tmp.is_null() {
            #[cfg(target_arch = "x86_64")]
            type GetTempPathWFn = unsafe extern "win64" fn(u32, *mut u16) -> u32;
            #[cfg(target_arch = "aarch64")]
            type GetTempPathWFn = unsafe extern "C" fn(u32, *mut u16) -> u32;
            let get_temp: GetTempPathWFn = core::mem::transmute(p_tmp);
            let mut tbuf = [0u16; 520];
            let tlen = get_temp(512, tbuf.as_mut_ptr()) as usize;
            if tlen > 0 && tlen < 512 {
                let rest = if slice.len() > 4 { &slice[5..] } else { &[][..] };
                let state = state_ptr();
                let (Some(alloc), Some(free), Some(mb_to_wide)) = (
                    (*state).alloc,
                    (*state).free,
                    (*state).multi_byte_to_wide_char,
                ) else {
                    return core::ptr::null_mut();
                };
                let rest_wlen = if rest.is_empty() {
                    0
                } else {
                    let w = mb_to_wide(
                        65001,
                        8,
                        rest.as_ptr(),
                        rest.len() as i32,
                        core::ptr::null_mut(),
                        0,
                    );
                    if w <= 0 {
                        return core::ptr::null_mut();
                    }
                    w as usize
                };
                // Strip trailing backslash if rest is empty (so CreateFileW/GetFileAttributesExW is happy)
                let base_len = if rest.is_empty() && tlen > 3 && tbuf[tlen - 1] == b'\\' as u16 {
                    tlen - 1
                } else {
                    tlen
                };
                let buf = alloc((base_len + rest_wlen + 1) * 2).cast::<u16>();
                if buf.is_null() {
                    return core::ptr::null_mut();
                }
                let mut i = 0usize;
                while i < base_len {
                    buf.add(i).write(tbuf[i]);
                    i += 1;
                }
                if rest_wlen > 0 {
                    let written = mb_to_wide(
                        65001,
                        8,
                        rest.as_ptr(),
                        rest.len() as i32,
                        buf.add(base_len),
                        rest_wlen as i32,
                    );
                    if written <= 0 {
                        free(buf.cast());
                        return core::ptr::null_mut();
                    }
                }
                buf.add(base_len + rest_wlen).write(0);
                return buf;
            }
        }
    }
    utf8_to_wide_alloc(utf8, len)
}

#[allow(unused_variables)]
pub(crate) fn dummy_types(_a: Option<LoadLibraryFn>, _b: Option<GetProcAddressFn>) {}
