#![allow(dead_code)]

use core::ffi::c_void;

use crate::pal_abi::{
    pal_fn, MEM_CODE_EXEC_RX, MEM_CODE_JIT_RW, MEM_READWRITE,
};
use crate::win_ffi::resolve_kernel32;
use crate::{exit_process_impl, state_ptr};

const MEM_COMMIT_RESERVE: u32 = 0x3000;
const MEM_RELEASE: u32 = 0x8000;
const PAGE_READWRITE: u32 = 0x04;
const PAGE_EXECUTE_READ: u32 = 0x20;
const PAGE_EXECUTE_READWRITE: u32 = 0x40;

#[cfg(target_arch = "x86_64")]
type VirtualAllocFn = unsafe extern "win64" fn(*mut c_void, usize, u32, u32) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type VirtualAllocFn = unsafe extern "C" fn(*mut c_void, usize, u32, u32) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type VirtualProtectFn = unsafe extern "win64" fn(*mut c_void, usize, u32, *mut u32) -> i32;
#[cfg(target_arch = "aarch64")]
type VirtualProtectFn = unsafe extern "C" fn(*mut c_void, usize, u32, *mut u32) -> i32;

#[cfg(target_arch = "x86_64")]
type VirtualFreeFn = unsafe extern "win64" fn(*mut c_void, usize, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type VirtualFreeFn = unsafe extern "C" fn(*mut c_void, usize, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type FlushInstructionCacheFn = unsafe extern "win64" fn(*mut c_void, *const c_void, usize) -> i32;
#[cfg(target_arch = "aarch64")]
type FlushInstructionCacheFn = unsafe extern "C" fn(*mut c_void, *const c_void, usize) -> i32;

#[cfg(target_arch = "x86_64")]
type GetCurrentProcessFn = unsafe extern "win64" fn() -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type GetCurrentProcessFn = unsafe extern "C" fn() -> *mut c_void;

fn win_protect(flags: u32) -> u32 {
    match flags {
        MEM_CODE_EXEC_RX => PAGE_EXECUTE_READ,
        MEM_CODE_JIT_RW => PAGE_EXECUTE_READWRITE,
        MEM_READWRITE | _ => PAGE_READWRITE,
    }
}

pub(crate) unsafe fn virtual_protect_rx(ptr: *mut c_void, size: usize) -> bool {
    let p = resolve_kernel32(b"VirtualProtect\0");
    if p.is_null() {
        return false;
    }
    let vp: VirtualProtectFn = core::mem::transmute(p);
    let mut old = 0u32;
    vp(ptr, size, PAGE_EXECUTE_READ, &mut old) != 0
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_alloc_mem(
    size: usize,
    _align: usize,
    flags: u32,
) -> *mut u8 {
    alloc_mem_impl(size, flags)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_alloc_mem(size: usize, _align: usize, flags: u32) -> *mut u8 {
    alloc_mem_impl(size, flags)
}

unsafe fn alloc_mem_impl(size: usize, flags: u32) -> *mut u8 {
    if size == 0 {
        return core::ptr::null_mut();
    }
    let p = resolve_kernel32(b"VirtualAlloc\0");
    if p.is_null() {
        return core::ptr::null_mut();
    }
    let va: VirtualAllocFn = core::mem::transmute(p);
    va(
        core::ptr::null_mut(),
        size,
        MEM_COMMIT_RESERVE,
        win_protect(flags),
    )
    .cast()
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_protect_mem(ptr: *mut u8, size: usize, flags: u32) -> i32 {
    protect_mem_impl(ptr, size, flags)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_protect_mem(ptr: *mut u8, size: usize, flags: u32) -> i32 {
    protect_mem_impl(ptr, size, flags)
}

unsafe fn protect_mem_impl(ptr: *mut u8, size: usize, flags: u32) -> i32 {
    if ptr.is_null() || size == 0 {
        return -1;
    }
    let p = resolve_kernel32(b"VirtualProtect\0");
    if p.is_null() {
        return -1;
    }
    let vp: VirtualProtectFn = core::mem::transmute(p);
    let mut old = 0u32;
    if vp(ptr.cast(), size, win_protect(flags), &mut old) != 0 {
        0
    } else {
        -1
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_free_mem(ptr: *mut u8, _size: usize) {
    free_mem_impl(ptr);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_free_mem(ptr: *mut u8, _size: usize) {
    free_mem_impl(ptr);
}

unsafe fn free_mem_impl(ptr: *mut u8) {
    if ptr.is_null() {
        return;
    }
    let p = resolve_kernel32(b"VirtualFree\0");
    if p.is_null() {
        return;
    }
    let vf: VirtualFreeFn = core::mem::transmute(p);
    let _ = vf(ptr.cast(), 0, MEM_RELEASE);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_flush_icache(ptr: *const u8, size: usize) {
    flush_icache_impl(ptr, size);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_flush_icache(ptr: *const u8, size: usize) {
    flush_icache_impl(ptr, size);
}

unsafe fn flush_icache_impl(ptr: *const u8, size: usize) {
    let p_flush = resolve_kernel32(b"FlushInstructionCache\0");
    let p_proc = resolve_kernel32(b"GetCurrentProcess\0");
    if !p_flush.is_null() && !p_proc.is_null() {
        let flush: FlushInstructionCacheFn = core::mem::transmute(p_flush);
        let get_proc: GetCurrentProcessFn = core::mem::transmute(p_proc);
        let _ = flush(get_proc(), ptr.cast(), size);
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_host_panic(msg: *const u8, len: usize) -> ! {
    host_panic_impl(msg, len)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_host_panic(msg: *const u8, len: usize) -> ! {
    host_panic_impl(msg, len)
}

unsafe fn host_panic_impl(msg: *const u8, len: usize) -> ! {
    let state = state_ptr();
    if let Some(write) = (*state).write_file {
        let mut written = 0u32;
        if !msg.is_null() && len > 0 {
            let _ = write(
                (*state).error_handle,
                msg.cast(),
                len.min(u32::MAX as usize) as u32,
                &mut written,
                core::ptr::null_mut(),
            );
        }
        let nl = b"\n";
        let _ = write(
            (*state).error_handle,
            nl.as_ptr().cast(),
            1,
            &mut written,
            core::ptr::null_mut(),
        );
    }
    exit_process_impl(99);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_rusticated_debug(val: i32) {
    rusticated_debug_impl(val);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_rusticated_debug(val: i32) {
    rusticated_debug_impl(val);
}

unsafe fn rusticated_debug_impl(val: i32) {
    let mut buf = [0u8; 24];
    let prefix = b"[debug] ";
    let mut i = 0usize;
    while i < prefix.len() {
        buf[i] = prefix[i];
        i += 1;
    }
    let mut v = val as i64;
    if v < 0 {
        buf[i] = b'-';
        i += 1;
        v = -v;
    }
    let mut digits = [0u8; 16];
    let mut d = 0usize;
    if v == 0 {
        digits[0] = b'0';
        d = 1;
    } else {
        while v > 0 && d < digits.len() {
            digits[d] = b'0' + (v % 10) as u8;
            v /= 10;
            d += 1;
        }
    }
    while d > 0 && i + 1 < buf.len() {
        d -= 1;
        buf[i] = digits[d];
        i += 1;
    }
    buf[i] = b'\n';
    i += 1;
    let state = state_ptr();
    if let Some(write) = (*state).write_file {
        let mut written = 0u32;
        let _ = write(
            (*state).error_handle,
            buf.as_ptr().cast(),
            i as u32,
            &mut written,
            core::ptr::null_mut(),
        );
    }
}

#[allow(dead_code)]
fn _check_fn(_: pal_fn!(fn(size: usize, align: usize, flags: u32) -> *mut u8)) {}
