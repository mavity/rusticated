use crate::pal_abi::{pal_fn, Overlapped, PalAbiTable, ERROR_UNSUPPORTED, FLAG_COMPLETED};

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" {
    fn pal_abi_unsupported_exit(code: i32) -> !;
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" {
    fn pal_abi_unsupported_exit(code: i32) -> !;
}

#[inline(never)]
fn unsupported_sync() -> ! {
    unsafe { pal_abi_unsupported_exit(ERROR_UNSUPPORTED as i32) }
}

macro_rules! sync_stub {
    ($name:ident($($arg:ident: $type:ty),* $(,)?) $(-> $result:ty)?) => {
        #[cfg(target_arch = "x86_64")]
        unsafe extern "win64" fn $name($($arg: $type),*) $(-> $result)? {
            let _ = ($($arg,)*);
            unsupported_sync()
        }
        #[cfg(target_arch = "aarch64")]
        unsafe extern "C" fn $name($($arg: $type),*) $(-> $result)? {
            let _ = ($($arg,)*);
            unsupported_sync()
        }
    };
}

macro_rules! async_stub {
    ($name:ident($ov:ident: *mut Overlapped $(, $arg:ident: $type:ty)* $(,)?)) => {
        #[cfg(target_arch = "x86_64")]
        unsafe extern "win64" fn $name($ov: *mut Overlapped $(, $arg: $type)*) {
            let _ = ($($arg,)*);
            if !$ov.is_null() {
                (*$ov).error = ERROR_UNSUPPORTED;
                (*$ov).continued = 0;
                (*$ov).result_ext = 0;
                (*$ov).flags = FLAG_COMPLETED;
            }
        }
        #[cfg(target_arch = "aarch64")]
        unsafe extern "C" fn $name($ov: *mut Overlapped $(, $arg: $type)*) {
            let _ = ($($arg,)*);
            if !$ov.is_null() {
                (*$ov).error = ERROR_UNSUPPORTED;
                (*$ov).continued = 0;
                (*$ov).result_ext = 0;
                (*$ov).flags = FLAG_COMPLETED;
            }
        }
    };
}

sync_stub!(unsupported_alloc_mem(size: usize, align: usize, flags: u32) -> *mut u8);
sync_stub!(unsupported_protect_mem(ptr: *mut u8, size: usize, flags: u32) -> i32);
sync_stub!(unsupported_free_mem(ptr: *mut u8, size: usize));
sync_stub!(unsupported_flush_icache(ptr: *const u8, size: usize));
sync_stub!(unsupported_host_panic(msg_ptr: *const u8, msg_len: usize) -> !);
sync_stub!(unsupported_debug(val: i32));
sync_stub!(unsupported_get_time() -> u64);
sync_stub!(unsupported_get_random(buf_ptr: *mut u8, buf_len: usize));
sync_stub!(unsupported_get_args(buf_ptr: *mut u8, buf_cap: usize) -> u64);
sync_stub!(unsupported_get_env(buf_ptr: *mut u8, buf_cap: usize) -> u64);
sync_stub!(unsupported_get_cwd(buf_ptr: *mut u8, buf_cap: usize) -> u64);
sync_stub!(unsupported_set_cwd(path_ptr: *const u8, path_len: usize) -> u32);
sync_stub!(unsupported_get_platform_info(out_ptr: *mut u8, out_cap: usize) -> u32);
async_stub!(unsupported_timer_set(ov: *mut Overlapped, delay_ms: u32));
sync_stub!(unsupported_cancel(ov: *mut Overlapped));
async_stub!(unsupported_seek(ov: *mut Overlapped, handle: u64, offset: i64, whence: u32));
async_stub!(unsupported_handle_truncate(ov: *mut Overlapped, handle: u64, size: i64));
sync_stub!(unsupported_handle_close(handle: u64));
async_stub!(unsupported_path_open(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32));
async_stub!(unsupported_dir_read(ov: *mut Overlapped, handle: u64, buf_ptr: *mut u8, buf_len: usize));
async_stub!(unsupported_path_stat(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32, stat_ptr: *mut u8, stat_cap: usize));
async_stub!(unsupported_path_link(ov: *mut Overlapped, old_ptr: *const u8, old_len: usize, new_ptr: *const u8, new_len: usize, flags: u32));
async_stub!(unsupported_path_symlink(ov: *mut Overlapped, target_ptr: *const u8, target_len: usize, link_ptr: *const u8, link_len: usize, target_kind: u32));
async_stub!(unsupported_path_readlink(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, buf_ptr: *mut u8, buf_len: usize));
async_stub!(unsupported_path_chmod(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32));
async_stub!(unsupported_path_set_times(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, atime_sec: i64, atime_nsec: u32, mtime_sec: i64, mtime_nsec: u32, flags: u32));
async_stub!(unsupported_path_remove(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32));
async_stub!(unsupported_path_mkdir(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32));
async_stub!(unsupported_path_rename(ov: *mut Overlapped, old_ptr: *const u8, old_len: usize, new_ptr: *const u8, new_len: usize));
async_stub!(unsupported_net_open(ov: *mut Overlapped, addr_ptr: *const u8, addr_len: usize, port: u32, flags: u32));
async_stub!(unsupported_net_accept(ov: *mut Overlapped, listener_handle: u64));
async_stub!(unsupported_net_lookup(ov: *mut Overlapped, name_ptr: *const u8, name_len: usize, buf_ptr: *mut u8, buf_cap: usize));
async_stub!(unsupported_net_cert_verify(ov: *mut Overlapped, name_ptr: *const u8, name_len: usize, chain_ptr: *const u8, chain_len: usize));
async_stub!(unsupported_process_spawn(ov: *mut Overlapped, cfg_ptr: *const u8, cfg_len: usize));
async_stub!(unsupported_process_pipe(ov: *mut Overlapped));
async_stub!(unsupported_process_wait(ov: *mut Overlapped, proc_handle: u64));
sync_stub!(unsupported_process_signal(proc_handle: u64, signum: u32));
async_stub!(unsupported_signal_wait(ov: *mut Overlapped, signum: u32));
sync_stub!(unsupported_tty_set_mode(handle: u64, raw_mode: u32));
sync_stub!(unsupported_tty_get_size(handle: u64) -> u32);
sync_stub!(unsupported_tty_set_size(handle: u64, cols: u32, rows: u32) -> u32);
sync_stub!(unsupported_fd_isatty(handle: u32) -> u32);

pub fn for_payload(
    read: pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *mut u8, buf_len: usize)),
    write: pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *const u8, buf_len: usize)),
    process_exit: pal_fn!(fn(code: i32) -> !),
) -> PalAbiTable {
    PalAbiTable {
        wasm_bytes: core::ptr::null(),
        wasm_len: 0,
        alloc_mem: unsupported_alloc_mem,
        protect_mem: unsupported_protect_mem,
        free_mem: unsupported_free_mem,
        flush_icache: unsupported_flush_icache,
        host_panic: unsupported_host_panic,
        rusticated_debug: unsupported_debug,
        process_exit,
        get_time: unsupported_get_time,
        get_random: unsupported_get_random,
        get_args: unsupported_get_args,
        get_env: unsupported_get_env,
        get_cwd: unsupported_get_cwd,
        set_cwd: unsupported_set_cwd,
        get_platform_info: unsupported_get_platform_info,
        timer_set: unsupported_timer_set,
        cancel: unsupported_cancel,
        read,
        write,
        seek: unsupported_seek,
        handle_truncate: unsupported_handle_truncate,
        handle_close: unsupported_handle_close,
        path_open: unsupported_path_open,
        dir_read: unsupported_dir_read,
        path_stat: unsupported_path_stat,
        path_link: unsupported_path_link,
        path_symlink: unsupported_path_symlink,
        path_readlink: unsupported_path_readlink,
        path_chmod: unsupported_path_chmod,
        path_set_times: unsupported_path_set_times,
        path_remove: unsupported_path_remove,
        path_mkdir: unsupported_path_mkdir,
        path_rename: unsupported_path_rename,
        net_open: unsupported_net_open,
        net_accept: unsupported_net_accept,
        net_lookup: unsupported_net_lookup,
        net_cert_verify: unsupported_net_cert_verify,
        process_spawn: unsupported_process_spawn,
        process_pipe: unsupported_process_pipe,
        process_wait: unsupported_process_wait,
        process_signal: unsupported_process_signal,
        signal_wait: unsupported_signal_wait,
        tty_set_mode: unsupported_tty_set_mode,
        tty_get_size: unsupported_tty_get_size,
        tty_set_size: unsupported_tty_set_size,
        fd_isatty: unsupported_fd_isatty,
    }
}
