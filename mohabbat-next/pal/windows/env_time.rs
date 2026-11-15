#![allow(dead_code)]

use core::ffi::c_void;

#[allow(unused_imports)]
use crate::pal_abi::{
    AbiPlatformInfo, Overlapped, CPU_TYPE_AARCH64, CPU_TYPE_X86_64, ERROR_CANCELED,
    ERROR_INVALID, ERROR_IO, ERROR_NOMEM, OS_KIND_WINDOWS,
};
use crate::win_ffi::{resolve_bcrypt, resolve_kernel32, utf8_to_wide_alloc};
use crate::{complete_io, map_windows_error, state_ptr};

#[repr(C)]
struct FileTime {
    low: u32,
    high: u32,
}

#[cfg(target_arch = "x86_64")]
type GetSystemTimePreciseAsFileTimeFn = unsafe extern "win64" fn(*mut FileTime);
#[cfg(target_arch = "aarch64")]
type GetSystemTimePreciseAsFileTimeFn = unsafe extern "C" fn(*mut FileTime);

#[cfg(target_arch = "x86_64")]
type BCryptGenRandomFn = unsafe extern "win64" fn(*mut c_void, *mut u8, u32, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type BCryptGenRandomFn = unsafe extern "C" fn(*mut c_void, *mut u8, u32, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetEnvironmentStringsAFn = unsafe extern "win64" fn() -> *mut u8;
#[cfg(target_arch = "aarch64")]
type GetEnvironmentStringsAFn = unsafe extern "C" fn() -> *mut u8;

#[cfg(target_arch = "x86_64")]
type FreeEnvironmentStringsAFn = unsafe extern "win64" fn(*mut u8) -> i32;
#[cfg(target_arch = "aarch64")]
type FreeEnvironmentStringsAFn = unsafe extern "C" fn(*mut u8) -> i32;

#[cfg(target_arch = "x86_64")]
type GetCurrentDirectoryWFn = unsafe extern "win64" fn(u32, *mut u16) -> u32;
#[cfg(target_arch = "aarch64")]
type GetCurrentDirectoryWFn = unsafe extern "C" fn(u32, *mut u16) -> u32;

#[cfg(target_arch = "x86_64")]
type SetCurrentDirectoryWFn = unsafe extern "win64" fn(*const u16) -> i32;
#[cfg(target_arch = "aarch64")]
type SetCurrentDirectoryWFn = unsafe extern "C" fn(*const u16) -> i32;

#[cfg(target_arch = "x86_64")]
type WideCharToMultiByteFn =
    unsafe extern "win64" fn(u32, u32, *const u16, i32, *mut u8, i32, *const u8, *mut i32) -> i32;
#[cfg(target_arch = "aarch64")]
type WideCharToMultiByteFn =
    unsafe extern "C" fn(u32, u32, *const u16, i32, *mut u8, i32, *const u8, *mut i32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetCurrentProcessIdFn = unsafe extern "win64" fn() -> u32;
#[cfg(target_arch = "aarch64")]
type GetCurrentProcessIdFn = unsafe extern "C" fn() -> u32;

#[cfg(target_arch = "x86_64")]
type GetComputerNameAFn = unsafe extern "win64" fn(*mut u8, *mut u32) -> i32;
#[cfg(target_arch = "aarch64")]
type GetComputerNameAFn = unsafe extern "C" fn(*mut u8, *mut u32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetModuleFileNameAFn = unsafe extern "win64" fn(*mut c_void, *mut u8, u32) -> u32;
#[cfg(target_arch = "aarch64")]
type GetModuleFileNameAFn = unsafe extern "C" fn(*mut c_void, *mut u8, u32) -> u32;

#[cfg(target_arch = "x86_64")]
type GetTempPathAFn = unsafe extern "win64" fn(u32, *mut u8) -> u32;
#[cfg(target_arch = "aarch64")]
type GetTempPathAFn = unsafe extern "C" fn(u32, *mut u8) -> u32;

#[cfg(target_arch = "x86_64")]
type CreateThreadpoolTimerFn = unsafe extern "win64" fn(
    Option<unsafe extern "win64" fn(*mut c_void, *mut c_void, *mut c_void)>,
    *mut c_void,
    *mut c_void,
) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type CreateThreadpoolTimerFn = unsafe extern "C" fn(
    Option<unsafe extern "C" fn(*mut c_void, *mut c_void, *mut c_void)>,
    *mut c_void,
    *mut c_void,
) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type SetThreadpoolTimerFn = unsafe extern "win64" fn(*mut c_void, *const i64, u32, u32);
#[cfg(target_arch = "aarch64")]
type SetThreadpoolTimerFn = unsafe extern "C" fn(*mut c_void, *const i64, u32, u32);

#[cfg(target_arch = "x86_64")]
type CloseThreadpoolTimerFn = unsafe extern "win64" fn(*mut c_void);
#[cfg(target_arch = "aarch64")]
type CloseThreadpoolTimerFn = unsafe extern "C" fn(*mut c_void);

#[cfg(target_arch = "x86_64")]
type CancelIoExFn = unsafe extern "win64" fn(*mut c_void, *mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type CancelIoExFn = unsafe extern "C" fn(*mut c_void, *mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_get_time() -> u64 {
    get_time_impl()
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_get_time() -> u64 {
    get_time_impl()
}

unsafe fn get_time_impl() -> u64 {
    let mut p = resolve_kernel32(b"GetSystemTimePreciseAsFileTime\0");
    if p.is_null() {
        p = resolve_kernel32(b"GetSystemTimeAsFileTime\0");
    }
    if p.is_null() {
        return 0;
    }
    let f: GetSystemTimePreciseAsFileTimeFn = core::mem::transmute(p);
    let mut ft = FileTime { low: 0, high: 0 };
    f(&mut ft);
    let ticks = ((ft.high as u64) << 32) | ft.low as u64;
    const EPOCH_DIFF_100NS: u64 = 116_444_736_000_000_000;
    ticks.saturating_sub(EPOCH_DIFF_100NS).saturating_mul(100)
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_get_random(buf: *mut u8, len: usize) {
    get_random_impl(buf, len);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_get_random(buf: *mut u8, len: usize) {
    get_random_impl(buf, len);
}

unsafe fn get_random_impl(buf: *mut u8, len: usize) {
    if buf.is_null() || len == 0 {
        return;
    }
    let p = resolve_bcrypt(b"BCryptGenRandom\0");
    if p.is_null() {
        return;
    }
    let gen: BCryptGenRandomFn = core::mem::transmute(p);
    let _ = gen(
        core::ptr::null_mut(),
        buf,
        len.min(u32::MAX as usize) as u32,
        2, // BCRYPT_USE_SYSTEM_PREFERRED_RNG
    );
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_get_env(buf: *mut u8, cap: usize) -> u64 {
    get_env_impl(buf, cap)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_get_env(buf: *mut u8, cap: usize) -> u64 {
    get_env_impl(buf, cap)
}

unsafe fn get_env_impl(buf: *mut u8, cap: usize) -> u64 {
    let p_get = resolve_kernel32(b"GetEnvironmentStringsA\0");
    let p_free = resolve_kernel32(b"FreeEnvironmentStringsA\0");
    if p_get.is_null() {
        return u64::MAX;
    }
    let get_env: GetEnvironmentStringsAFn = core::mem::transmute(p_get);
    let env_ptr = get_env();
    if env_ptr.is_null() {
        return 0;
    }
    let mut total = 0usize;
    let mut count = 0u64;
    while *env_ptr.add(total) != 0 {
        count += 1;
        while *env_ptr.add(total) != 0 {
            total += 1;
        }
        total += 1;
    }
    if cap >= total && !buf.is_null() {
        let mut i = 0usize;
        while i < total {
            buf.add(i).write(*env_ptr.add(i));
            i += 1;
        }
    }
    if !p_free.is_null() {
        let free_env: FreeEnvironmentStringsAFn = core::mem::transmute(p_free);
        let _ = free_env(env_ptr);
    }
    (count << 32) | (total as u64 & 0xFFFF_FFFF)
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_get_cwd(buf: *mut u8, cap: usize) -> u64 {
    get_cwd_impl(buf, cap)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_get_cwd(buf: *mut u8, cap: usize) -> u64 {
    get_cwd_impl(buf, cap)
}

unsafe fn get_cwd_impl(buf: *mut u8, cap: usize) -> u64 {
    let p_cwd = resolve_kernel32(b"GetCurrentDirectoryW\0");
    let p_w2m = resolve_kernel32(b"WideCharToMultiByte\0");
    if p_cwd.is_null() || p_w2m.is_null() {
        return u64::MAX;
    }
    let get_cwd: GetCurrentDirectoryWFn = core::mem::transmute(p_cwd);
    let w2m: WideCharToMultiByteFn = core::mem::transmute(p_w2m);
    let mut wbuf = [0u16; 512];
    let wlen = get_cwd(wbuf.len() as u32, wbuf.as_mut_ptr());
    if wlen == 0 || wlen as usize >= wbuf.len() {
        return u64::MAX;
    }
    let u8_len = w2m(
        65001,
        0,
        wbuf.as_ptr(),
        wlen as i32,
        core::ptr::null_mut(),
        0,
        core::ptr::null(),
        core::ptr::null_mut(),
    );
    if u8_len <= 0 {
        return u64::MAX;
    }
    let needed = u8_len as usize;
    if cap >= needed && !buf.is_null() {
        let _ = w2m(
            65001,
            0,
            wbuf.as_ptr(),
            wlen as i32,
            buf,
            needed as i32,
            core::ptr::null(),
            core::ptr::null_mut(),
        );
    }
    needed as u64
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_set_cwd(path: *const u8, len: usize) -> u32 {
    set_cwd_impl(path, len)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_set_cwd(path: *const u8, len: usize) -> u32 {
    set_cwd_impl(path, len)
}

unsafe fn set_cwd_impl(path: *const u8, len: usize) -> u32 {
    let p_set = resolve_kernel32(b"SetCurrentDirectoryW\0");
    if p_set.is_null() {
        return ERROR_IO;
    }
    let wide = utf8_to_wide_alloc(path, len);
    if wide.is_null() {
        return ERROR_INVALID;
    }
    let set_cwd: SetCurrentDirectoryWFn = core::mem::transmute(p_set);
    let ok = set_cwd(wide);
    if let Some(free) = (*state_ptr()).free {
        free(wide.cast());
    }
    if ok != 0 {
        0
    } else if let Some(gle) = (*state_ptr()).get_last_error {
        map_windows_error(gle())
    } else {
        ERROR_IO
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_get_platform_info(out: *mut u8, cap: usize) -> u32 {
    get_platform_info_impl(out, cap)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_get_platform_info(out: *mut u8, cap: usize) -> u32 {
    get_platform_info_impl(out, cap)
}

unsafe fn get_platform_info_impl(out: *mut u8, cap: usize) -> u32 {
    let needed = core::mem::size_of::<AbiPlatformInfo>();
    if out.is_null() || cap < needed {
        return ERROR_INVALID;
    }
    let mut info: AbiPlatformInfo = core::mem::zeroed();
    info.os_kind = OS_KIND_WINDOWS;
    #[cfg(target_arch = "x86_64")]
    {
        info.cpu_type = CPU_TYPE_X86_64;
    }
    #[cfg(target_arch = "aarch64")]
    {
        info.cpu_type = CPU_TYPE_AARCH64;
    }
    info.abi_version = 1;
    info.page_size = 4096;
    let p = resolve_kernel32(b"GetCurrentProcessId\0");
    if !p.is_null() {
        let f: GetCurrentProcessIdFn = core::mem::transmute(p);
        info.pid = f() as u64;
    }
    info.cpu_count = 1;
    let p = resolve_kernel32(b"GetComputerNameA\0");
    if !p.is_null() {
        let f: GetComputerNameAFn = core::mem::transmute(p);
        let mut n = info.hostname.len() as u32;
        let _ = f(info.hostname.as_mut_ptr(), &mut n);
    }
    let p = resolve_kernel32(b"GetModuleFileNameA\0");
    if !p.is_null() {
        let f: GetModuleFileNameAFn = core::mem::transmute(p);
        let _ = f(core::ptr::null_mut(), info.exe_path.as_mut_ptr(), info.exe_path.len() as u32);
    }
    let p = resolve_kernel32(b"GetTempPathA\0");
    if !p.is_null() {
        let f: GetTempPathAFn = core::mem::transmute(p);
        let _ = f(info.temp_dir.len() as u32, info.temp_dir.as_mut_ptr());
    }
    core::ptr::write(out.cast::<AbiPlatformInfo>(), info);
    0
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn timer_callback(
    _instance: *mut c_void,
    context: *mut c_void,
    timer: *mut c_void,
) {
    timer_callback_impl(context, timer);
}
#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn timer_callback(
    _instance: *mut c_void,
    context: *mut c_void,
    timer: *mut c_void,
) {
    timer_callback_impl(context, timer);
}

unsafe fn timer_callback_impl(context: *mut c_void, timer: *mut c_void) {
    let op = context.cast::<crate::IoOperation>();
    if let Some(post) = (*state_ptr()).post_queued_completion_status {
        let _ = post(
            (*state_ptr()).completion_port,
            0,
            crate::STDIO_COMPLETION_KEY,
            core::ptr::addr_of_mut!((*op).native_overlapped).cast(),
        );
    }
    let p_close = resolve_kernel32(b"CloseThreadpoolTimer\0");
    if !p_close.is_null() && !timer.is_null() {
        let close: CloseThreadpoolTimerFn = core::mem::transmute(p_close);
        close(timer);
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_timer_set(ov: *mut Overlapped, delay_ms: u32) {
    timer_set_impl(ov, delay_ms);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_timer_set(ov: *mut Overlapped, delay_ms: u32) {
    timer_set_impl(ov, delay_ms);
}

unsafe fn timer_set_impl(ov: *mut Overlapped, delay_ms: u32) {
    if ov.is_null() {
        return;
    }
    if delay_ms == 0 {
        complete_io(ov, 0, 0);
        return;
    }
    let p_create = resolve_kernel32(b"CreateThreadpoolTimer\0");
    let p_set = resolve_kernel32(b"SetThreadpoolTimer\0");
    if p_create.is_null() || p_set.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let op = crate::allocate_io_operation();
    if op.is_null() {
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    }
    (*op).overlapped = ov;
    (*op).kind = crate::IoKind::Timer;
    let create: CreateThreadpoolTimerFn = core::mem::transmute(p_create);
    let set: SetThreadpoolTimerFn = core::mem::transmute(p_set);
    let timer = create(Some(timer_callback), op.cast(), core::ptr::null_mut());
    if timer.is_null() {
        if let Some(free) = (*state_ptr()).free {
            free(op.cast());
        }
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    }
    (*op).handle = timer;
    crate::link_io_operation(op);
    let due: i64 = -(delay_ms as i64) * 10_000;
    set(timer, &due, 0, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_cancel(ov: *mut Overlapped) {
    cancel_impl(ov);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_cancel(ov: *mut Overlapped) {
    cancel_impl(ov);
}

unsafe fn cancel_impl(ov: *mut Overlapped) {
    if ov.is_null() {
        return;
    }
    let mut op = (*state_ptr()).io_operations;
    while !op.is_null() {
        if (*op).overlapped == ov {
            if (*op).kind == crate::IoKind::Timer && !(*op).handle.is_null() {
                let p_set = resolve_kernel32(b"SetThreadpoolTimer\0");
                let p_close = resolve_kernel32(b"CloseThreadpoolTimer\0");
                if !p_set.is_null() {
                    let set: SetThreadpoolTimerFn = core::mem::transmute(p_set);
                    set((*op).handle, core::ptr::null(), 0, 0);
                }
                if !p_close.is_null() {
                    let close: CloseThreadpoolTimerFn = core::mem::transmute(p_close);
                    close((*op).handle);
                }
                (*op).handle = core::ptr::null_mut();
                (*op).error = ERROR_CANCELED;
                (*op).immediate = true;
                if let Some(post) = (*state_ptr()).post_queued_completion_status {
                    let _ = post(
                        (*state_ptr()).completion_port,
                        0,
                        crate::STDIO_COMPLETION_KEY,
                        core::ptr::addr_of_mut!((*op).native_overlapped).cast(),
                    );
                }
            } else if !(*op).handle.is_null() {
                let p_cancel = resolve_kernel32(b"CancelIoEx\0");
                if !p_cancel.is_null() {
                    let cancel_ex: CancelIoExFn = core::mem::transmute(p_cancel);
                    let _ = cancel_ex(
                        (*op).handle,
                        core::ptr::addr_of_mut!((*op).native_overlapped).cast(),
                    );
                }
            }
            return;
        }
        op = (*op).next;
    }
}
