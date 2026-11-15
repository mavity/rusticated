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

#[allow(unused_variables)]
pub(crate) fn dummy_types(_a: Option<LoadLibraryFn>, _b: Option<GetProcAddressFn>) {}
