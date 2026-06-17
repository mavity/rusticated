//! Linux-specific extensions.
#![allow(missing_docs)]

pub mod syscall;

use core::ffi::{c_int, c_long, c_void};

/// Memory mapping flags.
pub const MAP_FAILED: *mut c_void = !0isize as *mut c_void;

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mmap(
    addr: *mut c_void,
    len: usize,
    prot: c_int,
    flags: c_int,
    fd: c_int,
    offset: c_long,
) -> *mut c_void {
    let res = crate::syscall!(
        crate::os::linux::syscall::nr::MMAP,
        addr,
        len,
        prot,
        flags,
        fd,
        offset
    );
    res as *mut c_void
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn munmap(addr: *mut c_void, len: usize) -> c_int {
    let res = crate::syscall!(crate::os::linux::syscall::nr::MUNMAP, addr, len);
    res as c_int
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn mremap(
    old_addr: *mut c_void,
    old_size: usize,
    new_size: usize,
    flags: c_int,
    new_addr: *mut c_void,
) -> *mut c_void {
    let res = crate::syscall!(
        crate::os::linux::syscall::nr::MREMAP,
        old_addr,
        old_size,
        new_size,
        flags,
        new_addr
    );
    res as *mut c_void
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn clock_gettime(clock_id: c_int, tp: *mut c_void) -> c_int {
    let res = crate::syscall!(crate::os::linux::syscall::nr::CLOCK_GETTIME, clock_id, tp);
    res as c_int
}

// Dummy pthread stubs for single-threaded allocator usage.
// If we need real threads, we'll need to implement clone syscall in src/thread.rs.

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pthread_mutex_lock(_mutex: *mut c_void) -> c_int {
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pthread_mutex_unlock(_mutex: *mut c_void) -> c_int {
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pthread_mutex_init(_mutex: *mut c_void, _attr: *const c_void) -> c_int {
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pthread_mutex_destroy(_mutex: *mut c_void) -> c_int {
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pthread_create(
    _thread: *mut core::ffi::c_ulong,
    _attr: *const c_void,
    _start_routine: unsafe extern "C" fn(*mut c_void) -> *mut c_void,
    _arg: *mut c_void,
) -> c_int {
    -1
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn pthread_detach(_thread: core::ffi::c_ulong) -> c_int {
    0
}
