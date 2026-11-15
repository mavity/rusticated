#![allow(dead_code)]

use core::ffi::c_void;

use crate::pal_abi::{
    Overlapped, ProcessSpawnOverlapped, ERROR_BADF, ERROR_INVALID, ERROR_IO, ERROR_NOMEM,
    ERROR_UNSUPPORTED, FLAG_COMPLETED,
};
use crate::win_ffi::{resolve_kernel32, utf8_to_wide_alloc};
use crate::{complete_io, find_file_handle, map_windows_error, state_ptr};

#[repr(C)]
struct SecurityAttributes {
    length: u32,
    security_descriptor: *mut c_void,
    inherit_handle: i32,
}

#[repr(C)]
struct StartupInfoW {
    cb: u32,
    reserved: *mut u16,
    desktop: *mut u16,
    title: *mut u16,
    x: u32,
    y: u32,
    x_size: u32,
    y_size: u32,
    x_count_chars: u32,
    y_count_chars: u32,
    fill_attribute: u32,
    flags: u32,
    show_window: u16,
    cb_reserved2: u16,
    lp_reserved2: *mut u8,
    std_input: *mut c_void,
    std_output: *mut c_void,
    std_error: *mut c_void,
}

#[repr(C)]
struct ProcessInformation {
    process: *mut c_void,
    thread: *mut c_void,
    process_id: u32,
    thread_id: u32,
}

#[repr(C)]
struct ConsoleScreenBufferInfo {
    size_x: i16,
    size_y: i16,
    cursor_x: i16,
    cursor_y: i16,
    attributes: u16,
    win_left: i16,
    win_top: i16,
    win_right: i16,
    win_bottom: i16,
    max_x: i16,
    max_y: i16,
}

#[repr(C)]
struct Coord {
    x: i16,
    y: i16,
}

#[cfg(target_arch = "x86_64")]
type CreatePipeFn =
    unsafe extern "win64" fn(*mut *mut c_void, *mut *mut c_void, *const SecurityAttributes, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type CreatePipeFn =
    unsafe extern "C" fn(*mut *mut c_void, *mut *mut c_void, *const SecurityAttributes, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type CreateProcessWFn = unsafe extern "win64" fn(
    *const u16,
    *mut u16,
    *mut c_void,
    *mut c_void,
    i32,
    u32,
    *mut c_void,
    *const u16,
    *const StartupInfoW,
    *mut ProcessInformation,
) -> i32;
#[cfg(target_arch = "aarch64")]
type CreateProcessWFn = unsafe extern "C" fn(
    *const u16,
    *mut u16,
    *mut c_void,
    *mut c_void,
    i32,
    u32,
    *mut c_void,
    *const u16,
    *const StartupInfoW,
    *mut ProcessInformation,
) -> i32;

#[cfg(target_arch = "x86_64")]
type GetExitCodeProcessFn = unsafe extern "win64" fn(*mut c_void, *mut u32) -> i32;
#[cfg(target_arch = "aarch64")]
type GetExitCodeProcessFn = unsafe extern "C" fn(*mut c_void, *mut u32) -> i32;

#[cfg(target_arch = "x86_64")]
type TerminateProcessFn = unsafe extern "win64" fn(*mut c_void, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type TerminateProcessFn = unsafe extern "C" fn(*mut c_void, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetConsoleModeFn = unsafe extern "win64" fn(*mut c_void, *mut u32) -> i32;
#[cfg(target_arch = "aarch64")]
type GetConsoleModeFn = unsafe extern "C" fn(*mut c_void, *mut u32) -> i32;

#[cfg(target_arch = "x86_64")]
type SetConsoleModeFn = unsafe extern "win64" fn(*mut c_void, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type SetConsoleModeFn = unsafe extern "C" fn(*mut c_void, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetConsoleScreenBufferInfoFn =
    unsafe extern "win64" fn(*mut c_void, *mut ConsoleScreenBufferInfo) -> i32;
#[cfg(target_arch = "aarch64")]
type GetConsoleScreenBufferInfoFn =
    unsafe extern "C" fn(*mut c_void, *mut ConsoleScreenBufferInfo) -> i32;

#[cfg(target_arch = "x86_64")]
type SetConsoleScreenBufferSizeFn = unsafe extern "win64" fn(*mut c_void, Coord) -> i32;
#[cfg(target_arch = "aarch64")]
type SetConsoleScreenBufferSizeFn = unsafe extern "C" fn(*mut c_void, Coord) -> i32;

#[cfg(target_arch = "x86_64")]
type GetFileTypeFn = unsafe extern "win64" fn(*mut c_void) -> u32;
#[cfg(target_arch = "aarch64")]
type GetFileTypeFn = unsafe extern "C" fn(*mut c_void) -> u32;

unsafe fn register_native_handle(native_handle: *mut c_void) -> Option<u64> {
    crate::register_extra_handle(native_handle)
}

unsafe fn resolve_std_or_handle(handle: u64) -> *mut c_void {
    let state = state_ptr();
    match handle {
        0 => (*state).input_handle,
        1 => (*state).output_handle,
        2 => (*state).error_handle,
        _ => {
            let f = find_file_handle(handle);
            if f.is_null() {
                core::ptr::null_mut()
            } else {
                (*f).native_handle
            }
        }
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_process_spawn(
    ov: *mut Overlapped,
    cfg: *const u8,
    len: usize,
) {
    process_spawn_impl(ov, cfg, len);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_process_spawn(
    ov: *mut Overlapped,
    cfg: *const u8,
    len: usize,
) {
    process_spawn_impl(ov, cfg, len);
}

unsafe fn process_spawn_impl(ov: *mut Overlapped, cfg: *const u8, len: usize) {
    if ov.is_null() || cfg.is_null() || len == 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let p_cp = resolve_kernel32(b"CreateProcessW\0");
    if p_cp.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let mut cmd_len = 0usize;
    while cmd_len < len && *cfg.add(cmd_len) != 0 {
        cmd_len += 1;
    }
    let wide = utf8_to_wide_alloc(cfg, cmd_len);
    if wide.is_null() {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let cp: CreateProcessWFn = core::mem::transmute(p_cp);
    let mut si: StartupInfoW = core::mem::zeroed();
    si.cb = core::mem::size_of::<StartupInfoW>() as u32;
    let mut pi: ProcessInformation = core::mem::zeroed();
    let ok = cp(
        core::ptr::null(),
        wide,
        core::ptr::null_mut(),
        core::ptr::null_mut(),
        0,
        0,
        core::ptr::null_mut(),
        core::ptr::null(),
        &si,
        &mut pi,
    );
    let err = if ok == 0 {
        if let Some(gle) = (*state_ptr()).get_last_error {
            map_windows_error(gle())
        } else {
            ERROR_IO
        }
    } else {
        0
    };
    if let Some(free) = (*state_ptr()).free {
        free(wide.cast());
    }
    if err != 0 {
        complete_io(ov, err, 0);
        return;
    }
    if let Some(close) = (*state_ptr()).close_handle {
        let _ = close(pi.thread);
    }
    let Some(token) = register_native_handle(pi.process) else {
        if let Some(close) = (*state_ptr()).close_handle {
            let _ = close(pi.process);
        }
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    };
    let ps_ov = ov.cast::<ProcessSpawnOverlapped>();
    (*ps_ov).child_handle = token;
    (*ps_ov).pty_handle = 0;
    (*ps_ov).in_handle = 0;
    (*ps_ov).out_handle = 0;
    (*ps_ov).err_handle = 0;
    (*ps_ov).header.error = 0;
    (*ps_ov).header.continued = 0;
    (*ps_ov).header.result_ext = token;
    (*ps_ov).header.flags = FLAG_COMPLETED;
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_process_pipe(ov: *mut Overlapped) {
    process_pipe_impl(ov);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_process_pipe(ov: *mut Overlapped) {
    process_pipe_impl(ov);
}

unsafe fn process_pipe_impl(ov: *mut Overlapped) {
    if ov.is_null() {
        return;
    }
    let p_pipe = resolve_kernel32(b"CreatePipe\0");
    if p_pipe.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let cp: CreatePipeFn = core::mem::transmute(p_pipe);
    let mut rd = core::ptr::null_mut();
    let mut wr = core::ptr::null_mut();
    let sa = SecurityAttributes {
        length: core::mem::size_of::<SecurityAttributes>() as u32,
        security_descriptor: core::ptr::null_mut(),
        inherit_handle: 0,
    };
    if cp(&mut rd, &mut wr, &sa, 0) == 0 {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let (Some(r_tok), Some(w_tok)) = (register_native_handle(rd), register_native_handle(wr))
    else {
        if let Some(close) = (*state_ptr()).close_handle {
            let _ = close(rd);
            let _ = close(wr);
        }
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    };
    (*ov).error = 0;
    (*ov).continued = w_tok;
    (*ov).result_ext = r_tok;
    (*ov).flags = FLAG_COMPLETED;
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_process_wait(ov: *mut Overlapped, proc_handle: u64) {
    process_wait_impl(ov, proc_handle);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_process_wait(ov: *mut Overlapped, proc_handle: u64) {
    process_wait_impl(ov, proc_handle);
}

unsafe fn process_wait_impl(ov: *mut Overlapped, proc_handle: u64) {
    let file = find_file_handle(proc_handle);
    if file.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    let (Some(wait), p_exit) = (
        (*state_ptr()).wait_for_single_object,
        resolve_kernel32(b"GetExitCodeProcess\0"),
    ) else {
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    if p_exit.is_null() {
        complete_io(ov, ERROR_IO, 0);
        return;
    }
    let _ = wait((*file).native_handle, u32::MAX);
    let gecp: GetExitCodeProcessFn = core::mem::transmute(p_exit);
    let mut code = 0u32;
    let _ = gecp((*file).native_handle, &mut code);
    complete_io(ov, 0, code as u64);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_process_signal(proc_handle: u64, _signum: u32) {
    process_signal_impl(proc_handle);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_process_signal(proc_handle: u64, _signum: u32) {
    process_signal_impl(proc_handle);
}

unsafe fn process_signal_impl(proc_handle: u64) {
    let file = find_file_handle(proc_handle);
    if file.is_null() {
        return;
    }
    let p_tp = resolve_kernel32(b"TerminateProcess\0");
    if !p_tp.is_null() {
        let tp: TerminateProcessFn = core::mem::transmute(p_tp);
        let _ = tp((*file).native_handle, 1);
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_signal_wait(ov: *mut Overlapped, _signum: u32) {
    complete_io(ov, ERROR_UNSUPPORTED, 0);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_signal_wait(ov: *mut Overlapped, _signum: u32) {
    complete_io(ov, ERROR_UNSUPPORTED, 0);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_tty_set_mode(handle: u64, raw_mode: u32) {
    tty_set_mode_impl(handle, raw_mode);
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_tty_set_mode(handle: u64, raw_mode: u32) {
    tty_set_mode_impl(handle, raw_mode);
}

unsafe fn tty_set_mode_impl(handle: u64, raw_mode: u32) {
    let h = resolve_std_or_handle(handle);
    if h.is_null() {
        return;
    }
    let p_get = resolve_kernel32(b"GetConsoleMode\0");
    let p_set = resolve_kernel32(b"SetConsoleMode\0");
    if p_get.is_null() || p_set.is_null() {
        return;
    }
    let get_m: GetConsoleModeFn = core::mem::transmute(p_get);
    let set_m: SetConsoleModeFn = core::mem::transmute(p_set);
    let mut mode = 0u32;
    if get_m(h, &mut mode) == 0 {
        return;
    }
    let new_mode = if raw_mode != 0 {
        mode & !(0x0002 | 0x0004)
    } else {
        mode | 0x0002 | 0x0004
    };
    let _ = set_m(h, new_mode);
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_tty_get_size(handle: u64) -> u32 {
    tty_get_size_impl(handle)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_tty_get_size(handle: u64) -> u32 {
    tty_get_size_impl(handle)
}

unsafe fn tty_get_size_impl(handle: u64) -> u32 {
    let h = resolve_std_or_handle(handle);
    if h.is_null() {
        return 0;
    }
    let p = resolve_kernel32(b"GetConsoleScreenBufferInfo\0");
    if p.is_null() {
        return 0;
    }
    let f: GetConsoleScreenBufferInfoFn = core::mem::transmute(p);
    let mut info: ConsoleScreenBufferInfo = core::mem::zeroed();
    if f(h, &mut info) == 0 {
        return 0;
    }
    let cols = (info.win_right - info.win_left + 1).max(0) as u32;
    let rows = (info.win_bottom - info.win_top + 1).max(0) as u32;
    (cols << 16) | (rows & 0xFFFF)
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_tty_set_size(handle: u64, cols: u32, rows: u32) -> u32 {
    tty_set_size_impl(handle, cols, rows)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_tty_set_size(handle: u64, cols: u32, rows: u32) -> u32 {
    tty_set_size_impl(handle, cols, rows)
}

unsafe fn tty_set_size_impl(handle: u64, cols: u32, rows: u32) -> u32 {
    let h = resolve_std_or_handle(handle);
    if h.is_null() {
        return ERROR_BADF;
    }
    let p = resolve_kernel32(b"SetConsoleScreenBufferSize\0");
    if p.is_null() {
        return ERROR_IO;
    }
    let f: SetConsoleScreenBufferSizeFn = core::mem::transmute(p);
    if f(
        h,
        Coord {
            x: cols as i16,
            y: rows as i16,
        },
    ) != 0
    {
        0
    } else {
        ERROR_IO
    }
}

#[cfg(target_arch = "x86_64")]
pub(crate) unsafe extern "win64" fn pal_fd_isatty(handle: u32) -> u32 {
    fd_isatty_impl(handle as u64)
}
#[cfg(target_arch = "aarch64")]
pub(crate) unsafe extern "C" fn pal_fd_isatty(handle: u32) -> u32 {
    fd_isatty_impl(handle as u64)
}

unsafe fn fd_isatty_impl(handle: u64) -> u32 {
    let h = resolve_std_or_handle(handle);
    if h.is_null() {
        return 0;
    }
    let p_ft = resolve_kernel32(b"GetFileType\0");
    let p_gm = resolve_kernel32(b"GetConsoleMode\0");
    if !p_ft.is_null() {
        let ft: GetFileTypeFn = core::mem::transmute(p_ft);
        if ft(h) == 2 && !p_gm.is_null() {
            let gm: GetConsoleModeFn = core::mem::transmute(p_gm);
            let mut mode = 0u32;
            if gm(h, &mut mode) != 0 {
                return 1;
            }
        }
    }
    0
}
