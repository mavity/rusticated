//! Canonical Native Rusticated PAL ABI (`PalAbiTable` at `-0x20000`) and
//! Private Runtime Globals (`RuntimeGlobals` at `-0x10000`).

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

pub(crate) use pal_fn;

pub const FLAG_COMPLETED: u32       = 1 << 0;
pub const READ_FLAG_POSITIONAL: u32 = 1 << 24;

/// Canonical 24-byte `Overlapped` completion header shared between guest WASM and the PAL.
#[repr(C)]
#[derive(Clone, Copy)]
pub struct Overlapped {
    pub flags:      u32,
    pub error:      u32,
    pub continued:  u64,
    pub result_ext: u64,
}

const _: [(); 24] = [(); core::mem::size_of::<Overlapped>()];

/// Extended 64-byte `Overlapped` structure for `process_spawn`.
#[repr(C)]
#[derive(Clone, Copy)]
pub struct ProcessSpawnOverlapped {
    pub header:       Overlapped,
    pub child_handle: u64,
    pub pty_handle:   u64,
    pub in_handle:    u64,
    pub out_handle:   u64,
    pub err_handle:   u64,
}

const _: [(); 64] = [(); core::mem::size_of::<ProcessSpawnOverlapped>()];

/// Read-Write data allocation (used for Cranelift GlobalAlloc heap, linear memory, and tables).
pub const MEM_READWRITE: u32 = 1;

/// Writable JIT code arena allocation (`MAP_JIT` + write-enabled on Apple Silicon).
pub const MEM_CODE_JIT_RW: u32 = 2;

/// Finalize JIT code arena as Read-Execute (`PROT_READ | PROT_EXEC` / `PAGE_EXECUTE_READ`).
pub const MEM_CODE_EXEC_RX: u32 = 3;

/// Canonical `PalAbiTable` populated by the PAL at `-0x20000` (`__abi_base`).
#[repr(C)]
pub struct PalAbiTable {
    // ── 1. Boot Payload (Raw WASM Bytes in Decompressed Pool) ──────────
    pub wasm_bytes: *const u8,
    pub wasm_len:   usize,

    // ── 2. Memory & JIT Primitives (4 Functions) ───────────────────────
    pub alloc_mem:    pal_fn!(fn(size: usize, align: usize, flags: u32) -> *mut u8),
    pub protect_mem:  pal_fn!(fn(ptr: *mut u8, size: usize, flags: u32) -> i32),
    pub free_mem:     pal_fn!(fn(ptr: *mut u8, size: usize)),
    pub flush_icache: pal_fn!(fn(ptr: *const u8, size: usize)),

    // ── 3. Core & Diagnostics (3 Functions) ────────────────────────────
    pub host_panic:       pal_fn!(fn(msg_ptr: *const u8, msg_len: usize) -> !),
    pub rusticated_debug: pal_fn!(fn(val: i32)),
    pub process_exit:     pal_fn!(fn(code: i32) -> !),

    // ── 4. System Environment & Time (7 Functions, Synchronous) ────────
    pub get_time:          pal_fn!(fn() -> u64),
    pub get_random:        pal_fn!(fn(buf_ptr: *mut u8, buf_len: usize)),
    pub get_args:          pal_fn!(fn(buf_ptr: *mut u8, buf_cap: usize) -> u64),
    pub get_env:           pal_fn!(fn(buf_ptr: *mut u8, buf_cap: usize) -> u64),
    pub get_cwd:           pal_fn!(fn(buf_ptr: *mut u8, buf_cap: usize) -> u64),
    pub set_cwd:           pal_fn!(fn(path_ptr: *const u8, path_len: usize) -> u32),
    pub get_platform_info: pal_fn!(fn(out_ptr: *mut u8, out_cap: usize) -> u32),

    // ── 5. Async Timers & Cancellation (2 Functions) ───────────────────
    pub timer_set: pal_fn!(fn(ov: *mut Overlapped, delay_ms: u32)),
    pub cancel:    pal_fn!(fn(ov: *mut Overlapped)),

    // ── 6. Async Stream & File I/O (5 Functions) ───────────────────────
    pub read:            pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *mut u8, buf_len: usize)),
    pub write:           pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *const u8, buf_len: usize)),
    pub seek:            pal_fn!(fn(ov: *mut Overlapped, handle: u64, offset: i64, whence: u32)),
    pub handle_truncate: pal_fn!(fn(ov: *mut Overlapped, handle: u64, size: i64)),
    pub handle_close:    pal_fn!(fn(handle: u64)),

    // ── 7. Filesystem Operations (11 Functions) ────────────────────────
    pub path_open:      pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32)),
    pub dir_read:       pal_fn!(fn(ov: *mut Overlapped, handle: u64, buf_ptr: *mut u8, buf_len: usize)),
    pub path_stat:      pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32, stat_ptr: *mut u8, stat_cap: usize)),
    pub path_link:      pal_fn!(fn(ov: *mut Overlapped, old_ptr: *const u8, old_len: usize, new_ptr: *const u8, new_len: usize, flags: u32)),
    pub path_symlink:   pal_fn!(fn(ov: *mut Overlapped, target_ptr: *const u8, target_len: usize, link_ptr: *const u8, link_len: usize, target_kind: u32)),
    pub path_readlink:  pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, buf_ptr: *mut u8, buf_len: usize)),
    pub path_chmod:     pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32)),
    pub path_set_times: pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, atime_sec: i64, atime_nsec: u32, mtime_sec: i64, mtime_nsec: u32, flags: u32)),
    pub path_remove:    pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32)),
    pub path_mkdir:     pal_fn!(fn(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32)),
    pub path_rename:    pal_fn!(fn(ov: *mut Overlapped, old_ptr: *const u8, old_len: usize, new_ptr: *const u8, new_len: usize)),

    // ── 8. Networking (4 Functions) ────────────────────────────────────
    pub net_open:        pal_fn!(fn(ov: *mut Overlapped, addr_ptr: *const u8, addr_len: usize, port: u32, flags: u32)),
    pub net_accept:      pal_fn!(fn(ov: *mut Overlapped, listener_handle: u64)),
    pub net_lookup:      pal_fn!(fn(ov: *mut Overlapped, name_ptr: *const u8, name_len: usize, buf_ptr: *mut u8, buf_cap: usize)),
    pub net_cert_verify: pal_fn!(fn(ov: *mut Overlapped, name_ptr: *const u8, name_len: usize, chain_ptr: *const u8, chain_len: usize)),

    // ── 9. Subprocesses & Signals (5 Functions) ────────────────────────
    pub process_spawn:  pal_fn!(fn(ov: *mut Overlapped, cfg_ptr: *const u8, cfg_len: usize)),
    pub process_pipe:   pal_fn!(fn(ov: *mut Overlapped)),
    pub process_wait:   pal_fn!(fn(ov: *mut Overlapped, proc_handle: u64)),
    pub process_signal: pal_fn!(fn(proc_handle: u64, signum: u32)),
    pub signal_wait:    pal_fn!(fn(ov: *mut Overlapped, signum: u32)),

    // ── 10. Terminal / TTY (4 Functions, Synchronous) ──────────────────
    pub tty_set_mode: pal_fn!(fn(handle: u64, raw_mode: u32)),
    pub tty_get_size: pal_fn!(fn(handle: u64) -> u32),
    pub tty_set_size: pal_fn!(fn(handle: u64, cols: u32, rows: u32) -> u32),
    pub fd_isatty:    pal_fn!(fn(handle: u32) -> u32),
}

/// Runtime private state located at `-0x10000` (`__globals_base`).
#[repr(C)]
pub struct RuntimeGlobals {
    pub initialized: bool,

    pub guest_mem_base: *mut u8,
    pub guest_mem_len:  usize,
    pub guest_mem_cap:  usize,

    pub heap_base: *mut u8,
    pub heap_used: usize,
    pub heap_cap:  usize,

    pub jit_code_base: *mut u8,
    pub jit_code_len:  usize,

    pub guest_run_fn: Option<pal_fn!(fn())>,
}

#[inline(always)]
pub unsafe fn slice_origin() -> *mut u8 {
    let addr: usize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "lea {0}, [rip + slice_entry]",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "adr {0}, slice_entry",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    addr as *mut u8
}

#[inline(always)]
pub unsafe fn abi() -> &'static PalAbiTable {
    &*(slice_origin().sub(0x20000) as *const PalAbiTable)
}

#[inline(always)]
pub unsafe fn globals() -> &'static mut RuntimeGlobals {
    &mut *(slice_origin().sub(0x10000) as *mut RuntimeGlobals)
}
