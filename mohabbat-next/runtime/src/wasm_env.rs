//! WASM32 `"env"` import adapter translating 32-bit linear-memory offsets
//! to bounds-checked 64-bit host pointers and forwarding to `PalAbiTable`.

use crate::abi::{FLAG_COMPLETED, Overlapped, abi, globals};

const ABI_ERROR_INVALID_INPUT: u32 = 5;

#[inline(always)]
unsafe fn guest_slice(ptr32: u32, len32: u32) -> Option<*const u8> {
    let g = globals();
    if g.guest_mem_base.is_null() {
        return None;
    }
    let end = (ptr32 as usize).checked_add(len32 as usize)?;
    if end > g.guest_mem_len {
        return None;
    }
    Some(g.guest_mem_base.add(ptr32 as usize))
}

#[inline(always)]
unsafe fn guest_slice_mut(ptr32: u32, len32: u32) -> Option<*mut u8> {
    let g = globals();
    if g.guest_mem_base.is_null() {
        return None;
    }
    let end = (ptr32 as usize).checked_add(len32 as usize)?;
    if end > g.guest_mem_len {
        return None;
    }
    Some(g.guest_mem_base.add(ptr32 as usize))
}

#[inline(always)]
unsafe fn guest_ov(ov32: u32) -> Option<*mut Overlapped> {
    if ov32 == 0 || (ov32 as usize) % 4 != 0 {
        return None;
    }
    let g = globals();
    if g.guest_mem_base.is_null() {
        return None;
    }
    let end = (ov32 as usize).checked_add(core::mem::size_of::<Overlapped>())?;
    if end > g.guest_mem_len {
        return None;
    }
    Some(g.guest_mem_base.add(ov32 as usize).cast::<Overlapped>())
}

#[inline(always)]
unsafe fn fail_ov(ov: *mut Overlapped, err: u32) {
    if !ov.is_null() {
        (*ov).error = err;
        (*ov).continued = 0;
        (*ov).result_ext = 0;
        (*ov).flags = FLAG_COMPLETED;
    }
}

macro_rules! env_fn {
    (fn $name:ident ($($arg:ident : $ty:ty),* $(,)?) $(-> $ret:ty)? $body:block) => {
        #[cfg(target_arch = "x86_64")]
        pub unsafe extern "win64" fn $name($($arg: $ty),*) $(-> $ret)? $body
        #[cfg(target_arch = "aarch64")]
        pub unsafe extern "C" fn $name($($arg: $ty),*) $(-> $ret)? $body
    };
}

env_fn!(fn env_host_panic(msg_ptr: u32, msg_len: u32) -> ! {
    match guest_slice(msg_ptr, msg_len) {
        Some(p) => (abi().host_panic)(p, msg_len as usize),
        None => (abi().host_panic)(b"wasm invalid panic ptr".as_ptr(), 22),
    }
});

env_fn!(fn env_rusticated_debug(val: i32) {
    (abi().rusticated_debug)(val);
});

env_fn!(fn env_process_exit(code: i32) -> ! {
    (abi().process_exit)(code)
});

env_fn!(fn env_get_time() -> u64 {
    (abi().get_time)()
});

env_fn!(fn env_get_random(buf_ptr: u32, buf_len: u32) {
    if let Some(p) = guest_slice_mut(buf_ptr, buf_len) {
        (abi().get_random)(p, buf_len as usize);
    }
});

env_fn!(fn env_get_args(buf_ptr: u32, buf_cap: u32) -> u64 {
    if buf_ptr == 0 && buf_cap == 0 {
        return (abi().get_args)(core::ptr::null_mut(), 0);
    }
    match guest_slice_mut(buf_ptr, buf_cap) {
        Some(p) => (abi().get_args)(p, buf_cap as usize),
        None => u64::MAX,
    }
});

env_fn!(fn env_get_env(buf_ptr: u32, buf_cap: u32) -> u64 {
    if buf_ptr == 0 && buf_cap == 0 {
        return (abi().get_env)(core::ptr::null_mut(), 0);
    }
    match guest_slice_mut(buf_ptr, buf_cap) {
        Some(p) => (abi().get_env)(p, buf_cap as usize),
        None => u64::MAX,
    }
});

env_fn!(fn env_get_cwd(buf_ptr: u32, buf_cap: u32) -> u64 {
    match guest_slice_mut(buf_ptr, buf_cap) {
        Some(p) => (abi().get_cwd)(p, buf_cap as usize),
        None => u64::MAX,
    }
});

env_fn!(fn env_set_cwd(path_ptr: u32, path_len: u32) -> u32 {
    match guest_slice(path_ptr, path_len) {
        Some(p) => (abi().set_cwd)(p, path_len as usize),
        None => ABI_ERROR_INVALID_INPUT,
    }
});

env_fn!(fn env_get_platform_info(out_ptr: u32, out_cap: u32) -> u32 {
    match guest_slice_mut(out_ptr, out_cap) {
        Some(p) => (abi().get_platform_info)(p, out_cap as usize),
        None => ABI_ERROR_INVALID_INPUT,
    }
});

env_fn!(fn env_timer_set(ov_ptr: u32, delay_ms: u32) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().timer_set)(ov, delay_ms);
    }
});

env_fn!(fn env_cancel(ov_ptr: u32) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().cancel)(ov);
    }
});

env_fn!(fn env_read(ov_ptr: u32, handle: u64, buf_ptr: u32, buf_len: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice_mut(buf_ptr, buf_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().read)(ov, handle, p, buf_len as usize);
});

env_fn!(fn env_write(ov_ptr: u32, handle: u64, buf_ptr: u32, buf_len: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice(buf_ptr, buf_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().write)(ov, handle, p, buf_len as usize);
});

env_fn!(fn env_seek(ov_ptr: u32, handle: u64, offset: i64, whence: u32) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().seek)(ov, handle, offset, whence);
    }
});

env_fn!(fn env_handle_truncate(ov_ptr: u32, handle: u64, size: i64) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().handle_truncate)(ov, handle, size);
    }
});

env_fn!(fn env_handle_close(handle: u64) {
    (abi().handle_close)(handle);
});

env_fn!(fn env_path_open(ov_ptr: u32, path_ptr: u32, path_len: u32, flags: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice(path_ptr, path_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_open)(ov, p, path_len as usize, flags);
});

env_fn!(fn env_dir_read(ov_ptr: u32, handle: u64, buf_ptr: u32, buf_len: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice_mut(buf_ptr, buf_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().dir_read)(ov, handle, p, buf_len as usize);
});

env_fn!(fn env_path_stat(
    ov_ptr: u32,
    path_ptr: u32,
    path_len: u32,
    flags: u32,
    stat_ptr: u32,
    stat_cap: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let (Some(p), Some(s)) = (guest_slice(path_ptr, path_len), guest_slice_mut(stat_ptr, stat_cap)) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_stat)(ov, p, path_len as usize, flags, s, stat_cap as usize);
});

env_fn!(fn env_path_link(
    ov_ptr: u32,
    old_ptr: u32,
    old_len: u32,
    new_ptr: u32,
    new_len: u32,
    flags: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let (Some(o), Some(n)) = (guest_slice(old_ptr, old_len), guest_slice(new_ptr, new_len)) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_link)(ov, o, old_len as usize, n, new_len as usize, flags);
});

env_fn!(fn env_path_symlink(
    ov_ptr: u32,
    target_ptr: u32,
    target_len: u32,
    link_ptr: u32,
    link_len: u32,
    target_kind: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let (Some(t), Some(l)) = (guest_slice(target_ptr, target_len), guest_slice(link_ptr, link_len)) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_symlink)(ov, t, target_len as usize, l, link_len as usize, target_kind);
});

env_fn!(fn env_path_readlink(
    ov_ptr: u32,
    path_ptr: u32,
    path_len: u32,
    buf_ptr: u32,
    buf_len: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let (Some(p), Some(b)) = (guest_slice(path_ptr, path_len), guest_slice_mut(buf_ptr, buf_len)) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_readlink)(ov, p, path_len as usize, b, buf_len as usize);
});

env_fn!(fn env_path_chmod(ov_ptr: u32, path_ptr: u32, path_len: u32, mode: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice(path_ptr, path_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_chmod)(ov, p, path_len as usize, mode);
});

env_fn!(fn env_path_set_times(
    ov_ptr: u32,
    path_ptr: u32,
    path_len: u32,
    atime_sec: i64,
    atime_nsec: u32,
    mtime_sec: i64,
    mtime_nsec: u32,
    flags: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice(path_ptr, path_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_set_times)(ov, p, path_len as usize, atime_sec, atime_nsec, mtime_sec, mtime_nsec, flags);
});

env_fn!(fn env_path_remove(ov_ptr: u32, path_ptr: u32, path_len: u32, flags: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice(path_ptr, path_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_remove)(ov, p, path_len as usize, flags);
});

env_fn!(fn env_path_mkdir(ov_ptr: u32, path_ptr: u32, path_len: u32, mode: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(p) = guest_slice(path_ptr, path_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_mkdir)(ov, p, path_len as usize, mode);
});

env_fn!(fn env_path_rename(
    ov_ptr: u32,
    old_ptr: u32,
    old_len: u32,
    new_ptr: u32,
    new_len: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let (Some(o), Some(n)) = (guest_slice(old_ptr, old_len), guest_slice(new_ptr, new_len)) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().path_rename)(ov, o, old_len as usize, n, new_len as usize);
});

env_fn!(fn env_net_open(ov_ptr: u32, addr_ptr: u32, addr_len: u32, port: u32, flags: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(a) = guest_slice(addr_ptr, addr_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().net_open)(ov, a, addr_len as usize, port, flags);
});

env_fn!(fn env_net_accept(ov_ptr: u32, listener_handle: u64) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().net_accept)(ov, listener_handle);
    }
});

env_fn!(fn env_net_lookup(
    ov_ptr: u32,
    name_ptr: u32,
    name_len: u32,
    buf_ptr: u32,
    buf_cap: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let (Some(n), Some(b)) = (guest_slice(name_ptr, name_len), guest_slice_mut(buf_ptr, buf_cap)) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().net_lookup)(ov, n, name_len as usize, b, buf_cap as usize);
});

env_fn!(fn env_net_cert_verify(
    ov_ptr: u32,
    name_ptr: u32,
    name_len: u32,
    chain_ptr: u32,
    chain_len: u32,
) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let (Some(n), Some(c)) = (guest_slice(name_ptr, name_len), guest_slice(chain_ptr, chain_len)) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().net_cert_verify)(ov, n, name_len as usize, c, chain_len as usize);
});

env_fn!(fn env_process_spawn(ov_ptr: u32, cfg_ptr: u32, cfg_len: u32) {
    let Some(ov) = guest_ov(ov_ptr) else { return };
    let Some(c) = guest_slice(cfg_ptr, cfg_len) else {
        fail_ov(ov, ABI_ERROR_INVALID_INPUT);
        return;
    };
    (abi().process_spawn)(ov, c, cfg_len as usize);
});

env_fn!(fn env_process_pipe(ov_ptr: u32) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().process_pipe)(ov);
    }
});

env_fn!(fn env_process_wait(ov_ptr: u32, proc_handle: u64) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().process_wait)(ov, proc_handle);
    }
});

env_fn!(fn env_process_signal(proc_handle: u64, signum: u32) {
    (abi().process_signal)(proc_handle, signum);
});

env_fn!(fn env_signal_wait(ov_ptr: u32, signum: u32) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().signal_wait)(ov, signum);
    }
});

env_fn!(fn env_tty_set_mode(handle: u64, raw_mode: u32) {
    (abi().tty_set_mode)(handle, raw_mode);
});

env_fn!(fn env_tty_get_size(handle: u64) -> u32 {
    (abi().tty_get_size)(handle)
});

env_fn!(fn env_tty_set_size(handle: u64, cols: u32, rows: u32) -> u32 {
    (abi().tty_set_size)(handle, cols, rows)
});

env_fn!(fn env_fd_isatty(handle: u32) -> u32 {
    (abi().fd_isatty)(handle)
});

env_fn!(fn env_system_info_wait(ov_ptr: u32) {
    if let Some(ov) = guest_ov(ov_ptr) {
        (abi().system_info_wait)(ov);
    }
});

pub fn resolve_env_import(name: &str) -> Option<usize> {
    match name {
        "host_panic" => Some(env_host_panic as *const () as usize),
        "rusticated_debug" => Some(env_rusticated_debug as *const () as usize),
        "process_exit" => Some(env_process_exit as *const () as usize),
        "get_time" => Some(env_get_time as *const () as usize),
        "get_random" => Some(env_get_random as *const () as usize),
        "get_args" => Some(env_get_args as *const () as usize),
        "get_env" => Some(env_get_env as *const () as usize),
        "get_cwd" => Some(env_get_cwd as *const () as usize),
        "set_cwd" => Some(env_set_cwd as *const () as usize),
        "get_platform_info" => Some(env_get_platform_info as *const () as usize),
        "timer_set" => Some(env_timer_set as *const () as usize),
        "cancel" => Some(env_cancel as *const () as usize),
        "read" => Some(env_read as *const () as usize),
        "write" => Some(env_write as *const () as usize),
        "seek" => Some(env_seek as *const () as usize),
        "handle_truncate" => Some(env_handle_truncate as *const () as usize),
        "handle_close" => Some(env_handle_close as *const () as usize),
        "path_open" => Some(env_path_open as *const () as usize),
        "dir_read" => Some(env_dir_read as *const () as usize),
        "path_stat" => Some(env_path_stat as *const () as usize),
        "path_link" => Some(env_path_link as *const () as usize),
        "path_symlink" => Some(env_path_symlink as *const () as usize),
        "path_readlink" => Some(env_path_readlink as *const () as usize),
        "path_chmod" => Some(env_path_chmod as *const () as usize),
        "path_set_times" => Some(env_path_set_times as *const () as usize),
        "path_remove" => Some(env_path_remove as *const () as usize),
        "path_mkdir" => Some(env_path_mkdir as *const () as usize),
        "path_rename" => Some(env_path_rename as *const () as usize),
        "net_open" => Some(env_net_open as *const () as usize),
        "net_accept" => Some(env_net_accept as *const () as usize),
        "net_lookup" => Some(env_net_lookup as *const () as usize),
        "net_cert_verify" => Some(env_net_cert_verify as *const () as usize),
        "process_spawn" => Some(env_process_spawn as *const () as usize),
        "process_pipe" => Some(env_process_pipe as *const () as usize),
        "process_wait" => Some(env_process_wait as *const () as usize),
        "process_signal" => Some(env_process_signal as *const () as usize),
        "signal_wait" => Some(env_signal_wait as *const () as usize),
        "tty_set_mode" => Some(env_tty_set_mode as *const () as usize),
        "tty_get_size" => Some(env_tty_get_size as *const () as usize),
        "tty_set_size" => Some(env_tty_set_size as *const () as usize),
        "fd_isatty" => Some(env_fd_isatty as *const () as usize),
        "system_info_wait" => Some(env_system_info_wait as *const () as usize),
        _ => None,
    }
}

#[inline(always)]
unsafe fn sync_guest_memory(caller: &mut wasmtime::Caller<'_, ()>) {
    if let Some(wasmtime::Extern::Memory(mem)) = caller.get_export("memory") {
        let g = globals();
        g.guest_mem_base = mem.data_ptr(&*caller);
        g.guest_mem_len = mem.data_size(&*caller);
    }
}

macro_rules! wrap_env {
    ($linker:expr, $name:literal, $func:ident, ($($arg:ident : $ty:ty),*) -> $ret:ty) => {
        $linker.func_wrap(
            "env",
            $name,
            |mut caller: wasmtime::Caller<'_, ()>, $($arg: $ty),*| -> $ret {
                unsafe {
                    sync_guest_memory(&mut caller);
                    $func($($arg),*)
                }
            },
        )?;
    };
    ($linker:expr, $name:literal, $func:ident, ($($arg:ident : $ty:ty),*)) => {
        wrap_env!($linker, $name, $func, ($($arg: $ty),*) -> ());
    };
}

pub fn register_env_imports(linker: &mut wasmtime::Linker<()>) -> wasmtime::Result<()> {
    wrap_env!(linker, "host_panic", env_host_panic, (msg_ptr: u32, msg_len: u32));
    wrap_env!(linker, "rusticated_debug", env_rusticated_debug, (val: i32));
    wrap_env!(linker, "process_exit", env_process_exit, (code: i32));
    wrap_env!(linker, "get_time", env_get_time, () -> u64);
    wrap_env!(linker, "get_random", env_get_random, (buf_ptr: u32, buf_len: u32));
    wrap_env!(linker, "get_args", env_get_args, (buf_ptr: u32, buf_cap: u32) -> u64);
    wrap_env!(linker, "get_env", env_get_env, (buf_ptr: u32, buf_cap: u32) -> u64);
    wrap_env!(linker, "get_cwd", env_get_cwd, (buf_ptr: u32, buf_cap: u32) -> u64);
    wrap_env!(linker, "set_cwd", env_set_cwd, (path_ptr: u32, path_len: u32) -> u32);
    wrap_env!(linker, "get_platform_info", env_get_platform_info, (out_ptr: u32, out_cap: u32) -> u32);
    wrap_env!(linker, "timer_set", env_timer_set, (ov_ptr: u32, delay_ms: u32));
    wrap_env!(linker, "cancel", env_cancel, (ov_ptr: u32));
    wrap_env!(linker, "read", env_read, (ov_ptr: u32, handle: u64, buf_ptr: u32, buf_len: u32));
    wrap_env!(linker, "write", env_write, (ov_ptr: u32, handle: u64, buf_ptr: u32, buf_len: u32));
    wrap_env!(linker, "seek", env_seek, (ov_ptr: u32, handle: u64, offset: i64, whence: u32));
    wrap_env!(linker, "handle_truncate", env_handle_truncate, (ov_ptr: u32, handle: u64, size: i64));
    wrap_env!(linker, "handle_close", env_handle_close, (handle: u64));
    wrap_env!(linker, "path_open", env_path_open, (ov_ptr: u32, path_ptr: u32, path_len: u32, flags: u32));
    wrap_env!(linker, "dir_read", env_dir_read, (ov_ptr: u32, handle: u64, buf_ptr: u32, buf_len: u32));
    wrap_env!(linker, "path_stat", env_path_stat, (ov_ptr: u32, path_ptr: u32, path_len: u32, flags: u32, stat_ptr: u32, stat_cap: u32));
    wrap_env!(linker, "path_link", env_path_link, (ov_ptr: u32, old_ptr: u32, old_len: u32, new_ptr: u32, new_len: u32, flags: u32));
    wrap_env!(linker, "path_symlink", env_path_symlink, (ov_ptr: u32, target_ptr: u32, target_len: u32, link_ptr: u32, link_len: u32, target_kind: u32));
    wrap_env!(linker, "path_readlink", env_path_readlink, (ov_ptr: u32, path_ptr: u32, path_len: u32, buf_ptr: u32, buf_len: u32));
    wrap_env!(linker, "path_chmod", env_path_chmod, (ov_ptr: u32, path_ptr: u32, path_len: u32, mode: u32));
    wrap_env!(linker, "path_set_times", env_path_set_times, (ov_ptr: u32, path_ptr: u32, path_len: u32, atime_sec: i64, atime_nsec: u32, mtime_sec: i64, mtime_nsec: u32, flags: u32));
    wrap_env!(linker, "path_remove", env_path_remove, (ov_ptr: u32, path_ptr: u32, path_len: u32, flags: u32));
    wrap_env!(linker, "path_mkdir", env_path_mkdir, (ov_ptr: u32, path_ptr: u32, path_len: u32, mode: u32));
    wrap_env!(linker, "path_rename", env_path_rename, (ov_ptr: u32, old_ptr: u32, old_len: u32, new_ptr: u32, new_len: u32));
    wrap_env!(linker, "net_open", env_net_open, (ov_ptr: u32, addr_ptr: u32, addr_len: u32, port: u32, flags: u32));
    wrap_env!(linker, "net_accept", env_net_accept, (ov_ptr: u32, listener_handle: u64));
    wrap_env!(linker, "net_lookup", env_net_lookup, (ov_ptr: u32, name_ptr: u32, name_len: u32, buf_ptr: u32, buf_cap: u32));
    wrap_env!(linker, "net_cert_verify", env_net_cert_verify, (ov_ptr: u32, name_ptr: u32, name_len: u32, chain_ptr: u32, chain_len: u32));
    wrap_env!(linker, "process_spawn", env_process_spawn, (ov_ptr: u32, cfg_ptr: u32, cfg_len: u32));
    wrap_env!(linker, "process_pipe", env_process_pipe, (ov_ptr: u32));
    wrap_env!(linker, "process_wait", env_process_wait, (ov_ptr: u32, proc_handle: u64));
    wrap_env!(linker, "process_signal", env_process_signal, (proc_handle: u64, signum: u32));
    wrap_env!(linker, "signal_wait", env_signal_wait, (ov_ptr: u32, signum: u32));
    wrap_env!(linker, "tty_set_mode", env_tty_set_mode, (handle: u64, raw_mode: u32));
    wrap_env!(linker, "tty_get_size", env_tty_get_size, (handle: u64) -> u32);
    wrap_env!(linker, "tty_set_size", env_tty_set_size, (handle: u64, cols: u32, rows: u32) -> u32);
    wrap_env!(linker, "fd_isatty", env_fd_isatty, (handle: u32) -> u32);
    wrap_env!(linker, "system_info_wait", env_system_info_wait, (ov_ptr: u32));
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolve_env_import_covers_all_41_slots() {
        let names = [
            "host_panic", "rusticated_debug", "process_exit", "get_time", "get_random",
            "get_args", "get_env", "get_cwd", "set_cwd", "get_platform_info",
            "timer_set", "cancel", "read", "write", "seek", "handle_truncate", "handle_close",
            "path_open", "dir_read", "path_stat", "path_link", "path_symlink", "path_readlink",
            "path_chmod", "path_set_times", "path_remove", "path_mkdir", "path_rename",
            "net_open", "net_accept", "net_lookup", "net_cert_verify",
            "process_spawn", "process_pipe", "process_wait", "process_signal", "signal_wait",
            "tty_set_mode", "tty_get_size", "tty_set_size", "fd_isatty", "system_info_wait",
        ];
        for name in names {
            assert!(resolve_env_import(name).is_some(), "missing env import {name}");
        }
        assert!(resolve_env_import("unknown_import").is_none());
    }

    #[test]
    fn guest_slice_bounds_check_rejects_oob_and_overflow() {
        crate::tests::init_test_env();
        let mut mem = [0u8; 64];
        unsafe {
            let g = globals();
            g.guest_mem_base = mem.as_mut_ptr();
            g.guest_mem_len = mem.len();
            assert!(guest_slice(0, 64).is_some());
            assert!(guest_slice(32, 32).is_some());
            assert!(guest_slice(32, 33).is_none());
            assert!(guest_slice(u32::MAX, 1).is_none());
            assert!(guest_ov(0).is_none());
            assert!(guest_ov(2).is_none());
            assert!(guest_ov(8).is_some());
            let oob_ov = (mem.len() - core::mem::size_of::<Overlapped>() + 4) as u32;
            assert!(guest_ov(oob_ov).is_none());
        }
    }
}
