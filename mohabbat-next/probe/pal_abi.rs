#![allow(dead_code)]
#![allow(clippy::type_complexity)]

#[cfg(target_arch = "x86_64")]
macro_rules! pal_fn {
    (fn($($arg:ident : $aty:ty),* $(,)?) $(-> $ret:ty)?) => {
        unsafe extern "win64" fn($($arg : $aty),*) $(-> $ret)?
    };
}

#[cfg(target_arch = "aarch64")]
macro_rules! pal_fn {
    (fn($($arg:ident : $aty:ty),* $(,)?) $(-> $ret:ty)?) => {
        unsafe extern "C" fn($($arg : $aty),*) $(-> $ret)?
    };
}

#[allow(unused_imports)]
pub(crate) use pal_fn;

pub const FLAG_COMPLETED: u32 = 1 << 0;
#[allow(dead_code)]
pub const READ_FLAG_POSITIONAL: u32 = 1 << 24;
#[allow(dead_code)]
pub const MEM_READWRITE: u32 = 1;
#[allow(dead_code)]
pub const MEM_CODE_JIT_RW: u32 = 2;
#[allow(dead_code)]
pub const MEM_CODE_EXEC_RX: u32 = 3;
pub const ERROR_AGAIN: u32 = 6;
pub const ERROR_BADF: u32 = 8;
pub const ERROR_CANCELED: u32 = 11;
pub const ERROR_INVALID: u32 = 28;
pub const ERROR_IO: u32 = 29;
pub const ERROR_PIPE: u32 = 64;
pub const ERROR_UNSUPPORTED: u32 = 52;

#[repr(C)]
#[derive(Clone, Copy, Default)]
pub struct Overlapped {
    pub flags: u32,
    pub error: u32,
    pub continued: u64,
    pub result_ext: u64,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct ProcessSpawnOverlapped {
    pub header: Overlapped,
    pub child_handle: u64,
    pub pty_handle: u64,
    pub in_handle: u64,
    pub out_handle: u64,
    pub err_handle: u64,
}

#[repr(C)]
pub struct PalAbiTable {
    pub wasm_bytes: *const u8,
    pub wasm_len: usize,

    pub alloc_mem: pal_fn!(fn(size: usize, align: usize, flags: u32) -> *mut u8),
    pub protect_mem: pal_fn!(fn(ptr: *mut u8, size: usize, flags: u32) -> i32),
    pub free_mem: pal_fn!(fn(ptr: *mut u8, size: usize)),
    pub flush_icache: pal_fn!(fn(ptr: *const u8, size: usize)),

    pub host_panic: pal_fn!(fn(msg_ptr: *const u8, msg_len: usize) -> !),
    pub rusticated_debug: pal_fn!(fn(val: i32)),
    pub process_exit: pal_fn!(fn(code: i32) -> !),

    pub get_time: pal_fn!(fn() -> u64),
    pub get_random: pal_fn!(fn(buf_ptr: *mut u8, buf_len: usize)),
    pub get_args: pal_fn!(fn(buf_ptr: *mut u8, buf_cap: usize) -> u64),
    pub get_env: pal_fn!(fn(buf_ptr: *mut u8, buf_cap: usize) -> u64),
    pub get_cwd: pal_fn!(fn(buf_ptr: *mut u8, buf_cap: usize) -> u64),
    pub set_cwd: pal_fn!(fn(path_ptr: *const u8, path_len: usize) -> u32),
    pub get_platform_info: pal_fn!(fn(out_ptr: *mut u8, out_cap: usize) -> u32),

    pub timer_set: pal_fn!(fn(ov: *mut Overlapped, delay_ms: u32)),
    pub cancel: pal_fn!(fn(ov: *mut Overlapped)),

    pub read: pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *mut u8, buf_len: usize)),
    pub write: pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *const u8, buf_len: usize)),
    pub seek: pal_fn!(fn(ov: *mut Overlapped, handle: u64, offset: i64, whence: u32)),
    pub handle_truncate: pal_fn!(fn(ov: *mut Overlapped, handle: u64, size: i64)),
    pub handle_close: pal_fn!(fn(handle: u64)),

    pub path_open:
        pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32)),
    pub dir_read: pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *mut u8, buf_len: usize)),
    pub path_stat: pal_fn!(
        fn(
            ov: *mut Overlapped,
            path_ptr: *const u8,
            path_len: usize,
            flags: u32,
            stat_ptr: *mut u8,
            stat_cap: usize,
        )
    ),
    pub path_link: pal_fn!(
        fn(
            ov: *mut Overlapped,
            old_ptr: *const u8,
            old_len: usize,
            new_ptr: *const u8,
            new_len: usize,
            flags: u32,
        )
    ),
    pub path_symlink: pal_fn!(
        fn(
            ov: *mut Overlapped,
            target_ptr: *const u8,
            target_len: usize,
            link_ptr: *const u8,
            link_len: usize,
            target_kind: u32,
        )
    ),
    pub path_readlink: pal_fn!(
        fn(
            ov: *mut Overlapped,
            path_ptr: *const u8,
            path_len: usize,
            buf_ptr: *mut u8,
            buf_len: usize,
        )
    ),
    pub path_chmod:
        pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32)),
    pub path_set_times: pal_fn!(
        fn(
            ov: *mut Overlapped,
            path_ptr: *const u8,
            path_len: usize,
            atime_sec: i64,
            atime_nsec: u32,
            mtime_sec: i64,
            mtime_nsec: u32,
            flags: u32,
        )
    ),
    pub path_remove:
        pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32)),
    pub path_mkdir:
        pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32)),
    pub path_rename: pal_fn!(
        fn(
            ov: *mut Overlapped,
            old_ptr: *const u8,
            old_len: usize,
            new_ptr: *const u8,
            new_len: usize,
        )
    ),

    pub net_open: pal_fn!(
        fn(ov: *mut Overlapped, addr_ptr: *const u8, addr_len: usize, port: u32, flags: u32)
    ),
    pub net_accept: pal_fn!(fn(ov: *mut Overlapped, listener_handle: u64)),
    pub net_lookup: pal_fn!(
        fn(
            ov: *mut Overlapped,
            name_ptr: *const u8,
            name_len: usize,
            buf_ptr: *mut u8,
            buf_cap: usize,
        )
    ),
    pub net_cert_verify: pal_fn!(
        fn(
            ov: *mut Overlapped,
            name_ptr: *const u8,
            name_len: usize,
            chain_ptr: *const u8,
            chain_len: usize,
        )
    ),

    pub process_spawn: pal_fn!(fn(ov: *mut Overlapped, cfg_ptr: *const u8, cfg_len: usize)),
    pub process_pipe: pal_fn!(fn(ov: *mut Overlapped)),
    pub process_wait: pal_fn!(fn(ov: *mut Overlapped, proc_handle: u64)),
    pub process_signal: pal_fn!(fn(proc_handle: u64, signum: u32)),
    pub signal_wait: pal_fn!(fn(ov: *mut Overlapped, signum: u32)),

    pub tty_set_mode: pal_fn!(fn(handle: u64, raw_mode: u32)),
    pub tty_get_size: pal_fn!(fn(handle: u64) -> u32),
    pub tty_set_size: pal_fn!(fn(handle: u64, cols: u32, rows: u32) -> u32),
    pub fd_isatty: pal_fn!(fn(handle: u32) -> u32),
}

const _: () = {
    assert!(core::mem::size_of::<Overlapped>() == 24);
    assert!(core::mem::offset_of!(Overlapped, flags) == 0);
    assert!(core::mem::offset_of!(Overlapped, error) == 4);
    assert!(core::mem::offset_of!(Overlapped, continued) == 8);
    assert!(core::mem::offset_of!(Overlapped, result_ext) == 16);
    assert!(core::mem::size_of::<ProcessSpawnOverlapped>() == 64);
    assert!(core::mem::size_of::<PalAbiTable>() == 47 * 8);
    assert!(core::mem::offset_of!(PalAbiTable, wasm_bytes) == 0);
    assert!(core::mem::offset_of!(PalAbiTable, wasm_len) == 8);
    assert!(core::mem::offset_of!(PalAbiTable, alloc_mem) == 16);
    assert!(core::mem::offset_of!(PalAbiTable, protect_mem) == 24);
    assert!(core::mem::offset_of!(PalAbiTable, free_mem) == 32);
    assert!(core::mem::offset_of!(PalAbiTable, flush_icache) == 40);
    assert!(core::mem::offset_of!(PalAbiTable, host_panic) == 48);
    assert!(core::mem::offset_of!(PalAbiTable, rusticated_debug) == 56);
    assert!(core::mem::offset_of!(PalAbiTable, process_exit) == 64);
    assert!(core::mem::offset_of!(PalAbiTable, get_time) == 72);
    assert!(core::mem::offset_of!(PalAbiTable, get_random) == 80);
    assert!(core::mem::offset_of!(PalAbiTable, get_args) == 88);
    assert!(core::mem::offset_of!(PalAbiTable, get_env) == 96);
    assert!(core::mem::offset_of!(PalAbiTable, get_cwd) == 104);
    assert!(core::mem::offset_of!(PalAbiTable, set_cwd) == 112);
    assert!(core::mem::offset_of!(PalAbiTable, get_platform_info) == 120);
    assert!(core::mem::offset_of!(PalAbiTable, timer_set) == 128);
    assert!(core::mem::offset_of!(PalAbiTable, cancel) == 136);
    assert!(core::mem::offset_of!(PalAbiTable, read) == 144);
    assert!(core::mem::offset_of!(PalAbiTable, write) == 152);
    assert!(core::mem::offset_of!(PalAbiTable, seek) == 160);
    assert!(core::mem::offset_of!(PalAbiTable, handle_truncate) == 168);
    assert!(core::mem::offset_of!(PalAbiTable, handle_close) == 176);
    assert!(core::mem::offset_of!(PalAbiTable, path_open) == 184);
    assert!(core::mem::offset_of!(PalAbiTable, dir_read) == 192);
    assert!(core::mem::offset_of!(PalAbiTable, path_stat) == 200);
    assert!(core::mem::offset_of!(PalAbiTable, path_link) == 208);
    assert!(core::mem::offset_of!(PalAbiTable, path_symlink) == 216);
    assert!(core::mem::offset_of!(PalAbiTable, path_readlink) == 224);
    assert!(core::mem::offset_of!(PalAbiTable, path_chmod) == 232);
    assert!(core::mem::offset_of!(PalAbiTable, path_set_times) == 240);
    assert!(core::mem::offset_of!(PalAbiTable, path_remove) == 248);
    assert!(core::mem::offset_of!(PalAbiTable, path_mkdir) == 256);
    assert!(core::mem::offset_of!(PalAbiTable, path_rename) == 264);
    assert!(core::mem::offset_of!(PalAbiTable, net_open) == 272);
    assert!(core::mem::offset_of!(PalAbiTable, net_accept) == 280);
    assert!(core::mem::offset_of!(PalAbiTable, net_lookup) == 288);
    assert!(core::mem::offset_of!(PalAbiTable, net_cert_verify) == 296);
    assert!(core::mem::offset_of!(PalAbiTable, process_spawn) == 304);
    assert!(core::mem::offset_of!(PalAbiTable, process_pipe) == 312);
    assert!(core::mem::offset_of!(PalAbiTable, process_wait) == 320);
    assert!(core::mem::offset_of!(PalAbiTable, process_signal) == 328);
    assert!(core::mem::offset_of!(PalAbiTable, signal_wait) == 336);
    assert!(core::mem::offset_of!(PalAbiTable, tty_set_mode) == 344);
    assert!(core::mem::offset_of!(PalAbiTable, tty_get_size) == 352);
    assert!(core::mem::offset_of!(PalAbiTable, tty_set_size) == 360);
    assert!(core::mem::offset_of!(PalAbiTable, fd_isatty) == 368);
};
