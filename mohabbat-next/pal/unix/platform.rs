#![cfg_attr(not(test), no_std)]

use core::ffi::{c_char, c_void};
#[cfg(not(test))]
use core::panic::PanicInfo;
use core::ptr;

#[path = "../abi/guest.rs"]
mod pal_abi;
#[path = "../abi/guest_stubs.rs"]
mod pal_abi_stubs;
mod linux_syscall;
mod unix_abi;
mod worker_pool;
use pal_abi::{
    Overlapped, PalAbiTable, ERROR_ACCES, ERROR_AGAIN, ERROR_BADF, ERROR_EXIST, ERROR_INVALID,
    ERROR_IO, ERROR_ISDIR, ERROR_NOENT, ERROR_NOMEM, ERROR_NOTDIR, ERROR_OVERFLOW, ERROR_PERM,
    ERROR_PIPE, ERROR_SPIPE,
};

#[cfg(target_arch = "aarch64")]
core::arch::global_asm!(
    ".text",
    ".globl pal_invoke_raw_entry",
    "pal_invoke_raw_entry:",
    "mov x16, x0",
    "mov x0, xzr",
    "mov x1, xzr",
    "mov x2, xzr",
    "mov x3, xzr",
    "mov x4, xzr",
    "mov x5, xzr",
    "mov x6, xzr",
    "mov x7, xzr",
    "br x16",
);

#[cfg(target_arch = "aarch64")]
unsafe extern "C" {
    fn pal_invoke_raw_entry(code_base: *mut c_void);
}

#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
compile_error!("The Unix platform abstraction supports only x86-64 and AArch64.");

#[cfg(all(feature = "linux", feature = "darwin"))]
compile_error!("Select exactly one Unix platform.");

#[cfg(all(not(test), not(any(feature = "linux", feature = "darwin"))))]
compile_error!("Select the linux or darwin platform feature.");

type LoadLibraryFn = unsafe extern "C" fn(*const c_char, i32) -> *mut c_void;
type GetProcAddressFn = unsafe extern "C" fn(*mut c_void, *const c_char) -> *mut c_void;
type AllocFn = unsafe extern "C" fn(usize) -> *mut c_void;
type FreeFn = unsafe extern "C" fn(*mut c_void, usize);
type FlushCacheFn = unsafe extern "C" fn(*mut c_void, usize);
type SetJitWriteProtectFn = unsafe extern "C" fn(i32);
type NativeReadFn = unsafe extern "C" fn(i32, *mut c_void, usize) -> isize;
type NativeWriteFn = unsafe extern "C" fn(i32, *const c_void, usize) -> isize;
type NativePreadFn = unsafe extern "C" fn(i32, *mut c_void, usize, i64) -> isize;
type NativeOpenFn = unsafe extern "C" fn(*const c_char, i32, u32) -> i32;
type NativeLseekFn = unsafe extern "C" fn(i32, i64, i32) -> i64;
type NativeFtruncateFn = unsafe extern "C" fn(i32, i64) -> i32;
type NativeCloseFn = unsafe extern "C" fn(i32) -> i32;
type NativeMallocFn = unsafe extern "C" fn(usize) -> *mut c_void;
type NativeFreeFn = unsafe extern "C" fn(*mut c_void);
type NativeExitFn = unsafe extern "C" fn(i32) -> !;
type ErrnoLocationFn = unsafe extern "C" fn() -> *mut i32;
type PthreadCreateFn = unsafe extern "C" fn(
    *mut usize,
    *const c_void,
    Option<unsafe extern "C" fn(*mut c_void) -> *mut c_void>,
    *mut c_void,
) -> i32;
type PthreadJoinFn = unsafe extern "C" fn(usize, *mut *mut c_void) -> i32;
type PthreadMutexInitFn = unsafe extern "C" fn(*mut c_void, *const c_void) -> i32;
type PthreadMutexDestroyFn = unsafe extern "C" fn(*mut c_void) -> i32;
type PthreadMutexLockFn = unsafe extern "C" fn(*mut c_void) -> i32;
type PthreadMutexUnlockFn = unsafe extern "C" fn(*mut c_void) -> i32;
type PthreadCondInitFn = unsafe extern "C" fn(*mut c_void, *const c_void) -> i32;
type PthreadCondDestroyFn = unsafe extern "C" fn(*mut c_void) -> i32;
type PthreadCondWaitFn = unsafe extern "C" fn(*mut c_void, *mut c_void) -> i32;
type PthreadCondSignalFn = unsafe extern "C" fn(*mut c_void) -> i32;
type PthreadCondBroadcastFn = unsafe extern "C" fn(*mut c_void) -> i32;

const IO_WORKER_COUNT: usize = 4;

#[repr(C, align(16))]
struct PthreadSyncStorage {
    words: [u64; 8],
}

impl PthreadSyncStorage {
    const fn new() -> Self {
        Self { words: [0; 8] }
    }
}

#[repr(C)]
struct IoSequence {
    head: *mut IoOperation,
    tail: *mut IoOperation,
    active: bool,
}

impl IoSequence {
    const fn empty() -> Self {
        Self {
            head: ptr::null_mut(),
            tail: ptr::null_mut(),
            active: false,
        }
    }
}

#[repr(C)]
struct FileHandle {
    token: u64,
    fd: i32,
    next: *mut FileHandle,
    references: usize,
    closing: bool,
    sequence: IoSequence,
}

#[cfg(feature = "darwin")]
#[repr(C)]
pub struct UnixPlatformHost {
    load_library: Option<LoadLibraryFn>,
    get_proc_address: Option<GetProcAddressFn>,
    alloc: Option<AllocFn>,
    free: Option<FreeFn>,
    flush_cache: Option<FlushCacheFn>,
    set_jit_write_protect: Option<SetJitWriteProtectFn>,
    hello_payload: *const u8,
    hello_payload_size: usize,
    argv: *const *const u8,
    argc: usize,
}

#[cfg(not(feature = "darwin"))]
#[repr(C)]
pub struct UnixPlatformHost {
    hello_payload: *const u8,
    hello_payload_size: usize,
    argv: *const *const u8,
    argc: usize,
    envp: *const *const u8,
    envc: usize,
    pid: u32,
    wasm_bytes: *const u8,
    wasm_len: usize,
}

#[repr(C)]
#[derive(Clone, Copy, PartialEq, Eq)]
#[allow(dead_code)]
enum IoKind {
    Read,
    Write,
    PositionalRead,
    Open,
    Seek,
    Truncate,
    Timer,
    DirRead,
    PathStat,
    PathLink,
    PathSymlink,
    PathReadlink,
    PathChmod,
    PathSetTimes,
    PathRemove,
    PathMkdir,
    PathRename,
    NetOpen,
    NetAccept,
    NetLookup,
    NetCertVerify,
    ProcessSpawn,
    ProcessPipe,
    ProcessWait,
    SignalWait,
}

#[repr(C)]
#[derive(Clone, Copy)]
struct IoOperation {
    active_next: *mut IoOperation,
    queue_next: *mut IoOperation,
    sequence_next: *mut IoOperation,
    overlapped: *mut Overlapped,
    kind: IoKind,
    fd: i32,
    buffer: *mut c_void,
    length: usize,
    offset: i64,
    whence: i32,
    error: u32,
    result: u64,
    file_handle: *mut FileHandle,
    sequence: *mut IoSequence,
    extra_ptr: *mut c_void,
    extra_len: usize,
    aux_u32: u32,
    aux_u64: u64,
    cancelled: bool,
}

impl IoOperation {
    const fn new(overlapped: *mut Overlapped, kind: IoKind) -> Self {
        Self {
            active_next: ptr::null_mut(),
            queue_next: ptr::null_mut(),
            sequence_next: ptr::null_mut(),
            overlapped,
            kind,
            fd: -1,
            buffer: ptr::null_mut(),
            length: 0,
            offset: 0,
            whence: 0,
            error: 0,
            result: 0,
            file_handle: ptr::null_mut(),
            sequence: ptr::null_mut(),
            extra_ptr: ptr::null_mut(),
            extra_len: 0,
            aux_u32: 0,
            aux_u64: 0,
            cancelled: false,
        }
    }
}

#[repr(C)]
struct IoWorker {
    thread: usize,
    started: bool,
}

impl IoWorker {
    const fn empty() -> Self {
        Self {
            thread: 0,
            started: false,
        }
    }
}

#[repr(C)]
struct PlatformState {
    read: Option<NativeReadFn>,
    write: Option<NativeWriteFn>,
    pread: Option<NativePreadFn>,
    open: Option<NativeOpenFn>,
    lseek: Option<NativeLseekFn>,
    ftruncate: Option<NativeFtruncateFn>,
    close: Option<NativeCloseFn>,
    malloc: Option<NativeMallocFn>,
    libc_free: Option<NativeFreeFn>,
    file_handles: *mut FileHandle,
    next_handle: u64,
    exit: Option<NativeExitFn>,
    errno_location: Option<ErrnoLocationFn>,
    pthread_create: Option<PthreadCreateFn>,
    pthread_join: Option<PthreadJoinFn>,
    pthread_mutex_init: Option<PthreadMutexInitFn>,
    pthread_mutex_destroy: Option<PthreadMutexDestroyFn>,
    pthread_mutex_lock: Option<PthreadMutexLockFn>,
    pthread_mutex_unlock: Option<PthreadMutexUnlockFn>,
    pthread_cond_init: Option<PthreadCondInitFn>,
    pthread_cond_destroy: Option<PthreadCondDestroyFn>,
    pthread_cond_wait: Option<PthreadCondWaitFn>,
    pthread_cond_signal: Option<PthreadCondSignalFn>,
    pthread_cond_broadcast: Option<PthreadCondBroadcastFn>,
    queue_mutex: PthreadSyncStorage,
    work_ready: PthreadSyncStorage,
    completion_ready: PthreadSyncStorage,
    synchronization_initialized: bool,
    stopping_workers: bool,
    workers: [IoWorker; IO_WORKER_COUNT],
    worker_count: usize,
    active_operations: *mut IoOperation,
    active_count: usize,
    work_head: *mut IoOperation,
    work_tail: *mut IoOperation,
    completion_head: *mut IoOperation,
    completion_tail: *mut IoOperation,
    standard_sequences: [IoSequence; 3],
    free: Option<FreeFn>,
    payload_alloc: *mut c_void,
    payload_total_size: usize,
    envp: *const *const u8,
    envc: usize,
    pid: u32,
    pipe_fds: [i32; 2],
    io_uring_fd: i32,
    comp_lock: i32,
    linux_pool: *mut c_void,
}

#[cfg(test)]
static mut TEST_STATE: PlatformState = PlatformState {
    read: None,
    write: None,
    pread: None,
    open: None,
    lseek: None,
    ftruncate: None,
    close: None,
    malloc: None,
    libc_free: None,
    file_handles: ptr::null_mut(),
    next_handle: 3,
    exit: None,
    errno_location: None,
    pthread_create: None,
    pthread_join: None,
    pthread_mutex_init: None,
    pthread_mutex_destroy: None,
    pthread_mutex_lock: None,
    pthread_mutex_unlock: None,
    pthread_cond_init: None,
    pthread_cond_destroy: None,
    pthread_cond_wait: None,
    pthread_cond_signal: None,
    pthread_cond_broadcast: None,
    queue_mutex: PthreadSyncStorage::new(),
    work_ready: PthreadSyncStorage::new(),
    completion_ready: PthreadSyncStorage::new(),
    synchronization_initialized: false,
    stopping_workers: false,
    workers: [const { IoWorker::empty() }; IO_WORKER_COUNT],
    worker_count: 0,
    active_operations: ptr::null_mut(),
    active_count: 0,
    work_head: ptr::null_mut(),
    work_tail: ptr::null_mut(),
    completion_head: ptr::null_mut(),
    completion_tail: ptr::null_mut(),
    standard_sequences: [
        IoSequence::empty(),
        IoSequence::empty(),
        IoSequence::empty(),
    ],
    free: None,
    payload_alloc: ptr::null_mut(),
    payload_total_size: 0,
    envp: ptr::null(),
    envc: 0,
    pid: 0,
    pipe_fds: [-1, -1],
    io_uring_fd: -1,
    comp_lock: 0,
    linux_pool: ptr::null_mut(),
};

const PREFIX_ABI_OFFSET: usize = 0x20000;
const ERRNO_EINTR: i32 = 4;
const ERRNO_EACCES: i32 = 13;
const ERRNO_EBADF: i32 = 9;
const ERRNO_EEXIST: i32 = 17;
const ERRNO_EINVAL: i32 = 22;
const ERRNO_ENOENT: i32 = 2;
const ERRNO_EPIPE: i32 = 32;
const ERRNO_ENOTDIR: i32 = 20;
const ERRNO_EISDIR: i32 = 21;
const ERRNO_EPERM: i32 = 1;
#[cfg(feature = "darwin")]
const ERRNO_EAGAIN: i32 = 35;
#[cfg(not(feature = "darwin"))]
const ERRNO_EAGAIN: i32 = 11;

fn map_io_errno(errno: i32) -> u32 {
    match errno {
        ERRNO_EACCES => ERROR_ACCES,
        ERRNO_EBADF => ERROR_BADF,
        ERRNO_EEXIST => ERROR_EXIST,
        ERRNO_EINVAL => ERROR_INVALID,
        ERRNO_EISDIR => ERROR_ISDIR,
        ERRNO_ENOENT => ERROR_NOENT,
        ERRNO_ENOTDIR => ERROR_NOTDIR,
        ERRNO_EPERM => ERROR_PERM,
        ERRNO_EPIPE => ERROR_PIPE,
        ERRNO_EAGAIN => ERROR_AGAIN,
        _ => ERROR_IO,
    }
}

#[inline(always)]
unsafe fn slice_origin() -> *mut u8 {
    let addr: usize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "lea {0}, [rip + unix_platform_entry]",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "adr {0}, unix_platform_entry",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    addr as *mut u8
}

#[inline(always)]
unsafe fn host_abi() -> &'static UnixPlatformHost {
    &*(slice_origin().sub(0x20000) as *const UnixPlatformHost)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_get_args(buffer: *mut u8, capacity: usize) -> u64 {
    let host = host_abi();
    pal_abi::pack_args(host.argv, host.argc, buffer, capacity)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_get_args(buffer: *mut u8, capacity: usize) -> u64 {
    let host = host_abi();
    pal_abi::pack_args(host.argv, host.argc, buffer, capacity)
}

#[inline(always)]
unsafe fn state_ptr() -> *mut PlatformState {
    #[cfg(not(test))]
    {
        slice_origin().sub(0x10000) as *mut PlatformState
    }
    #[cfg(test)]
    {
        core::ptr::addr_of_mut!(TEST_STATE)
    }
}

#[inline(always)]
unsafe fn state_mut() -> &'static mut PlatformState {
    &mut *state_ptr()
}

#[inline(always)]
unsafe fn invoke_raw_entry(code_base: *mut c_void) {
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "mov r11, {entry}",
        "xor ecx, ecx",
        "xor edx, edx",
        "xor r8d, r8d",
        "xor r9d, r9d",
        "call r11",
        entry = in(reg) code_base,
        clobber_abi("win64"),
    );
    #[cfg(target_arch = "aarch64")]
    pal_invoke_raw_entry(code_base);
}

unsafe fn write_fd(fd: i32, bytes: &[u8]) -> bool {
    let write = match state_mut().write {
        Some(write) => write,
        None => return false,
    };
    let mut offset = 0;
    while offset < bytes.len() {
        let count = write(fd, bytes.as_ptr().add(offset).cast(), bytes.len() - offset);
        if count <= 0 {
            return false;
        }
        offset += count as usize;
    }
    true
}

#[cfg(feature = "darwin")]
unsafe fn resolve(
    host: &UnixPlatformHost,
    module: *mut c_void,
    name: &'static [u8],
) -> *mut c_void {
    match host.get_proc_address {
        Some(get_proc_address) => get_proc_address(module, name.as_ptr().cast()),
        None => ptr::null_mut(),
    }
}

#[cfg(feature = "darwin")]
macro_rules! function_pointer {
    ($address:expr, $function_type:ty) => {{
        let address: *mut c_void = $address;
        if address.is_null() {
            None
        } else {
            Some(core::mem::transmute::<*mut c_void, $function_type>(address))
        }
    }};
}

unsafe fn complete_io(ov: *mut Overlapped, error: u32, result: u64) {
    if ov.is_null() {
        return;
    }
    (*ov).error = error;
    (*ov).continued = 0;
    (*ov).result_ext = result;
    (*ov).flags = pal_abi::FLAG_COMPLETED;
}

unsafe fn find_file_handle(handle: u64) -> *mut FileHandle {
    let mut entry = state_mut().file_handles;
    while !entry.is_null() {
        if (*entry).token == handle {
            return entry;
        }
        entry = (*entry).next;
    }
    ptr::null_mut()
}

unsafe fn resolve_handle(handle: u64) -> Option<i32> {
    if handle <= 2 {
        return Some(handle as i32);
    }
    let file = find_file_handle(handle);
    if file.is_null() {
        None
    } else {
        Some((*file).fd)
    }
}

unsafe fn release_operation_resources(operation: *mut IoOperation) {
    let state = state_ptr();
    if (*operation).kind == IoKind::Open {
        if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
            if !(*operation).buffer.is_null() {
                free((*operation).buffer);
            }
        }
    }
}

unsafe fn queue_lock(state: *mut PlatformState) {
    if (*state).synchronization_initialized {
        if let Some(lock) = (*state).pthread_mutex_lock {
            if lock(ptr::addr_of_mut!((*state).queue_mutex).cast()) != 0 {
                terminate_after_worker_failure(17);
            }
        }
    }
}

unsafe fn queue_unlock(state: *mut PlatformState) {
    if (*state).synchronization_initialized {
        if let Some(unlock) = (*state).pthread_mutex_unlock {
            if unlock(ptr::addr_of_mut!((*state).queue_mutex).cast()) != 0 {
                terminate_after_worker_failure(17);
            }
        }
    }
}

unsafe fn enqueue_work(state: *mut PlatformState, operation: *mut IoOperation) {
    #[cfg(all(
        not(test),
        not(feature = "darwin"),
        any(feature = "linux", target_os = "linux", target_os = "android")
    ))]
    {
        let _ = state;
        let handle_key = (*operation).sequence as usize;
        let _ = linux_runtime::pool().submit(operation as usize, handle_key);
        return;
    }
    #[cfg(not(all(
        not(test),
        not(feature = "darwin"),
        any(feature = "linux", target_os = "linux", target_os = "android")
    )))]
    {
        (*operation).queue_next = ptr::null_mut();
        queue_lock(state);
        let tail = ptr::addr_of!((*state).work_tail).read();
        if tail.is_null() {
            ptr::addr_of_mut!((*state).work_head).write(operation);
        } else {
            ptr::addr_of_mut!((*tail).queue_next).write(operation);
        }
        ptr::addr_of_mut!((*state).work_tail).write(operation);
        if (*state).synchronization_initialized {
            if let Some(signal) = (*state).pthread_cond_signal {
                if signal(ptr::addr_of_mut!((*state).work_ready).cast()) != 0 {
                    queue_unlock(state);
                    terminate_after_worker_failure(17);
                }
            }
        }
        queue_unlock(state);
    }
}

unsafe fn append_sequence(sequence: *mut IoSequence, operation: *mut IoOperation) -> bool {
    (*operation).sequence_next = ptr::null_mut();
    if (*sequence).tail.is_null() {
        (*sequence).head = operation;
    } else {
        (*(*sequence).tail).sequence_next = operation;
    }
    (*sequence).tail = operation;
    if (*sequence).active {
        false
    } else {
        (*sequence).active = true;
        true
    }
}

unsafe fn submit_io(ov: *mut Overlapped, request: IoOperation) {
    if ov.is_null() {
        if request.kind == IoKind::Open {
            if let Some(free) = state_mut().libc_free {
                if !request.buffer.is_null() {
                    free(request.buffer);
                }
            }
        }
        return;
    }
    let state = state_ptr();
    let Some(malloc) = ptr::addr_of!((*state).malloc).read() else {
        if request.kind == IoKind::Open {
            if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
                if !request.buffer.is_null() {
                    free(request.buffer);
                }
            }
        }
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    let operation = malloc(core::mem::size_of::<IoOperation>()).cast::<IoOperation>();
    if operation.is_null() {
        if request.kind == IoKind::Open {
            if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
                if !request.buffer.is_null() {
                    free(request.buffer);
                }
            }
        }
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    }
    operation.write(request);
    (*operation).overlapped = ov;

    let mut active = ptr::addr_of!((*state).active_operations).read();
    while !active.is_null() {
        if (*active).overlapped == ov {
            release_operation_resources(operation);
            if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
                free(operation.cast());
            }
            terminate_after_worker_failure(18);
        }
        active = (*active).active_next;
    }
    let Some(active_count) = ptr::addr_of!((*state).active_count).read().checked_add(1) else {
        release_operation_resources(operation);
        if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
            free(operation.cast());
        }
        complete_io(ov, ERROR_OVERFLOW, 0);
        return;
    };
    if !(*operation).file_handle.is_null()
        && (*(*operation).file_handle).references == usize::MAX
    {
        release_operation_resources(operation);
        if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
            free(operation.cast());
        }
        complete_io(ov, ERROR_OVERFLOW, 0);
        return;
    }
    (*operation).active_next = ptr::addr_of!((*state).active_operations).read();
    ptr::addr_of_mut!((*state).active_operations).write(operation);
    ptr::addr_of_mut!((*state).active_count).write(active_count);
    if !(*operation).file_handle.is_null() {
        (*(*operation).file_handle).references =
            (*(*operation).file_handle).references + 1;
    }

    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;

    if !(*operation).sequence.is_null() {
        if append_sequence((*operation).sequence, operation) {
            enqueue_work(state, operation);
        }
    } else {
        enqueue_work(state, operation);
    }
}

unsafe fn begin_io(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *mut c_void,
    length: usize,
    is_write: bool,
) {
    if ov.is_null() || (buffer.is_null() && length != 0) {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file_handle = if handle > 2 {
        find_file_handle(handle)
    } else {
        ptr::null_mut()
    };
    let standard_fd = match (handle, is_write) {
        (0, false) => Some(0),
        (1 | 2, true) => Some(handle as i32),
        (0..=2, _) => None,
        _ if !file_handle.is_null() => Some((*file_handle).fd),
        _ => None,
    };
    let Some(fd) = standard_fd else {
        complete_io(ov, ERROR_BADF, 0);
        return;
    };
    if length == 0 {
        complete_io(ov, 0, 0);
        return;
    }
    let positional = !is_write && (*ov).flags & pal_abi::READ_FLAG_POSITIONAL != 0;
    let (kind, offset, sequence) = if positional {
        if handle <= 2 {
            complete_io(ov, ERROR_SPIPE, 0);
            return;
        }
        let pread = ptr::addr_of!((*state_ptr()).pread).read();
        if pread.is_none() {
            complete_io(ov, pal_abi::ERROR_UNSUPPORTED, 0);
            return;
        }
        let offset = ptr::read_unaligned(
            ov.cast::<u8>()
                .add(core::mem::size_of::<Overlapped>())
                .cast::<i64>(),
        );
        if offset < 0 {
            complete_io(ov, ERROR_INVALID, 0);
            return;
        }
        (IoKind::PositionalRead, offset, ptr::null_mut())
    } else if is_write {
        let sequence = if handle <= 2 {
            ptr::addr_of_mut!((*state_ptr()).standard_sequences[handle as usize])
        } else {
            ptr::addr_of_mut!((*file_handle).sequence)
        };
        (IoKind::Write, 0, sequence)
    } else {
        let sequence = if handle <= 2 {
            ptr::addr_of_mut!((*state_ptr()).standard_sequences[handle as usize])
        } else {
            ptr::addr_of_mut!((*file_handle).sequence)
        };
        (IoKind::Read, 0, sequence)
    };
    submit_io(
        ov,
        IoOperation {
            fd,
            buffer,
            length,
            offset,
            file_handle,
            sequence,
            ..IoOperation::new(ov, kind)
        },
    );
}

unsafe fn begin_seek(ov: *mut Overlapped, handle: u64, offset: i64, whence: u32) {
    if ov.is_null() || whence > 2 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file_handle = find_file_handle(handle);
    if file_handle.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    submit_io(
        ov,
        IoOperation {
            fd: (*file_handle).fd,
            offset,
            whence: whence as i32,
            file_handle,
            sequence: ptr::addr_of_mut!((*file_handle).sequence),
            ..IoOperation::new(ov, IoKind::Seek)
        },
    );
}

unsafe fn begin_truncate(ov: *mut Overlapped, handle: u64, length: i64) {
    if ov.is_null() || length < 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file_handle = find_file_handle(handle);
    if file_handle.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    submit_io(
        ov,
        IoOperation {
            fd: (*file_handle).fd,
            offset: length,
            file_handle,
            sequence: ptr::addr_of_mut!((*file_handle).sequence),
            ..IoOperation::new(ov, IoKind::Truncate)
        },
    );
}

const ABI_O_CREATE: u32 = 0x40;
const ABI_O_EXCL: u32 = 0x80;
const ABI_O_TRUNC: u32 = 0x200;
const ABI_O_APPEND: u32 = 0x400;
const ABI_O_DIRECTORY: u32 = 0x2000;

#[cfg(any(feature = "linux", all(test, not(feature = "darwin"))))]
const NATIVE_O_CREATE: i32 = 0x40;
#[cfg(feature = "darwin")]
const NATIVE_O_CREATE: i32 = 0x200;
#[cfg(any(feature = "linux", all(test, not(feature = "darwin"))))]
const NATIVE_O_EXCL: i32 = 0x80;
#[cfg(feature = "darwin")]
const NATIVE_O_EXCL: i32 = 0x800;
#[cfg(any(feature = "linux", all(test, not(feature = "darwin"))))]
const NATIVE_O_TRUNC: i32 = 0x200;
#[cfg(feature = "darwin")]
const NATIVE_O_TRUNC: i32 = 0x400;
#[cfg(any(feature = "linux", all(test, not(feature = "darwin"))))]
const NATIVE_O_APPEND: i32 = 0x400;
#[cfg(feature = "darwin")]
const NATIVE_O_APPEND: i32 = 0x8;
#[cfg(any(feature = "linux", all(test, not(feature = "darwin"))))]
const NATIVE_O_DIRECTORY: i32 = 0x10000;
#[cfg(feature = "darwin")]
const NATIVE_O_DIRECTORY: i32 = 0x100000;

unsafe fn begin_path_open(ov: *mut Overlapped, path: *const u8, length: usize, flags: u32) {
    if ov.is_null() || (path.is_null() && length != 0) {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let access = match flags & 3 {
        0 => 0,
        1 => 1,
        2 => 2,
        _ => {
            complete_io(ov, ERROR_INVALID, 0);
            return;
        }
    };
    let supported = 3 | ABI_O_CREATE | ABI_O_EXCL | ABI_O_TRUNC | ABI_O_APPEND | ABI_O_DIRECTORY;
    if flags & !supported != 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    if length == usize::MAX {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let state = state_ptr();
    let (Some(malloc), Some(free), Some(_open)) = (
        ptr::addr_of!((*state).malloc).read(),
        ptr::addr_of!((*state).libc_free).read(),
        ptr::addr_of!((*state).open).read(),
    ) else {
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    let path_buffer = malloc(length + 1).cast::<u8>();
    if path_buffer.is_null() {
        complete_io(ov, ERROR_NOMEM, 0);
        return;
    }
    let mut index = 0;
    while index < length {
        let byte = path.add(index).read();
        if byte == 0 {
            free(path_buffer.cast());
            complete_io(ov, ERROR_INVALID, 0);
            return;
        }
        path_buffer.add(index).write(byte);
        index += 1;
    }
    path_buffer.add(length).write(0);

    let mut native_flags = access;
    if flags & ABI_O_CREATE != 0 {
        native_flags |= NATIVE_O_CREATE;
    }
    if flags & ABI_O_EXCL != 0 {
        native_flags |= NATIVE_O_EXCL;
    }
    if flags & ABI_O_TRUNC != 0 {
        native_flags |= NATIVE_O_TRUNC;
    }
    if flags & ABI_O_APPEND != 0 {
        native_flags |= NATIVE_O_APPEND;
    }
    if flags & ABI_O_DIRECTORY != 0 {
        native_flags |= NATIVE_O_DIRECTORY;
    }
    submit_io(
        ov,
        IoOperation {
            fd: native_flags,
            buffer: path_buffer.cast(),
            length,
            ..IoOperation::new(ov, IoKind::Open)
        },
    );
}

unsafe fn execute_io_operation(operation: *mut IoOperation) {
    let state = state_ptr();
    let read = ptr::addr_of!((*state).read).read();
    let write = ptr::addr_of!((*state).write).read();
    let pread = ptr::addr_of!((*state).pread).read();
    let open = ptr::addr_of!((*state).open).read();
    let lseek = ptr::addr_of!((*state).lseek).read();
    let ftruncate = ptr::addr_of!((*state).ftruncate).read();
    let errno_location = ptr::addr_of!((*state).errno_location).read();
    let (error, result) = loop {
        let result = match (*operation).kind {
            IoKind::Read => match read {
                Some(read) => read((*operation).fd, (*operation).buffer, (*operation).length),
                None => -1,
            },
            IoKind::Write => match write {
                Some(write) => write(
                    (*operation).fd,
                    (*operation).buffer.cast(),
                    (*operation).length,
                ),
                None => -1,
            },
            IoKind::PositionalRead => match pread {
                Some(pread) => pread(
                    (*operation).fd,
                    (*operation).buffer,
                    (*operation).length,
                    (*operation).offset,
                ),
                None => -1,
            },
            IoKind::Open => {
                let opened = match open {
                    Some(open) => open((*operation).buffer.cast(), (*operation).fd, 0o666),
                    None => -1,
                };
                opened as isize
            }
            IoKind::Seek => match lseek {
                Some(lseek) => lseek(
                    (*operation).fd,
                    (*operation).offset,
                    (*operation).whence,
                ) as isize,
                None => -1,
            },
            IoKind::Truncate => match ftruncate {
                Some(ftruncate) => {
                    ftruncate((*operation).fd, (*operation).offset) as isize
                }
                None => -1,
            },
            IoKind::Timer => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let delay_ms = (*operation).offset.max(0) as u64;
                    let req = linux_syscall::Timespec {
                        tv_sec: (delay_ms / 1000) as i64,
                        tv_nsec: ((delay_ms % 1000) * 1_000_000) as i64,
                    };
                    let mut rem = linux_syscall::Timespec::default();
                    let _ = linux_syscall::syscall2(
                        linux_syscall::nr::NANOSLEEP,
                        (&req as *const linux_syscall::Timespec) as usize,
                        (&mut rem as *mut linux_syscall::Timespec) as usize,
                    );
                    0
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    0
                }
            }
            IoKind::DirRead => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let mut raw_buf = [0u8; 4096];
                    let n = linux_syscall::syscall3(
                        linux_syscall::nr::GETDENTS64,
                        (*operation).fd as usize,
                        raw_buf.as_mut_ptr() as usize,
                        raw_buf.len(),
                    );
                    if n < 0 {
                        n
                    } else {
                        let out = (*operation).buffer.cast::<u8>();
                        let cap = (*operation).length;
                        let mut pos = 0usize;
                        let mut out_pos = 0usize;
                        let total = n as usize;
                        while pos + 19 <= total {
                            let d_ino = u64::from_le_bytes(
                                raw_buf[pos..pos + 8].try_into().unwrap_or([0; 8]),
                            );
                            let d_reclen = u16::from_le_bytes([raw_buf[pos + 16], raw_buf[pos + 17]]) as usize;
                            let d_type = raw_buf[pos + 18];
                            if d_reclen < 19 || pos + d_reclen > total {
                                break;
                            }
                            let name_start = pos + 19;
                            let mut name_end = name_start;
                            while name_end < pos + d_reclen && raw_buf[name_end] != 0 {
                                name_end += 1;
                            }
                            let name = &raw_buf[name_start..name_end];
                            let entry_bytes = 8 + name.len();
                            if out_pos + entry_bytes > cap {
                                break;
                            }
                            let kind: u16 = match d_type {
                                4 => unix_abi::ABI_STAT_KIND_DIR as u16,
                                8 => unix_abi::ABI_STAT_KIND_FILE as u16,
                                10 => unix_abi::ABI_STAT_KIND_SYMLINK as u16,
                                _ => unix_abi::ABI_STAT_KIND_OTHER as u16,
                            };
                            let hdr = unix_abi::AbiDirEntryHeader {
                                ino: d_ino as u32,
                                kind,
                                name_len: name.len() as u16,
                            };
                            ptr::write_unaligned(out.add(out_pos).cast(), hdr);
                            ptr::copy_nonoverlapping(name.as_ptr(), out.add(out_pos + 8), name.len());
                            out_pos += entry_bytes;
                            pos += d_reclen;
                        }
                        out_pos as isize
                    }
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    0
                }
            }
            IoKind::PathStat => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let mut cpath = [0u8; 4096];
                    if let Some(p) = unix_abi::copy_cstr(
                        (*operation).buffer.cast(),
                        (*operation).length,
                        &mut cpath,
                    ) {
                        let mut st = linux_syscall::LinuxStat::default();
                        let flags = if ((*operation).aux_u32 & unix_abi::ABI_PATH_FOLLOW_SYMLINKS) != 0 {
                            0
                        } else {
                            linux_syscall::AT_SYMLINK_NOFOLLOW
                        };
                        let r = linux_syscall::syscall4(
                            linux_syscall::nr::NEWFSTATAT,
                            linux_syscall::AT_FDCWD as usize,
                            p as usize,
                            (&mut st as *mut linux_syscall::LinuxStat) as usize,
                            flags as usize,
                        );
                        if r == 0 && !(*operation).extra_ptr.is_null() && (*operation).extra_len >= core::mem::size_of::<unix_abi::AbiStat>() {
                            let abi = unix_abi::linux_stat_to_abi(&st);
                            ptr::write_unaligned((*operation).extra_ptr.cast(), abi);
                        }
                        r
                    } else {
                        -(linux_syscall::EINVAL as isize)
                    }
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    -(38isize)
                }
            }
            IoKind::PathMkdir => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let mut cpath = [0u8; 4096];
                    if let Some(p) = unix_abi::copy_cstr((*operation).buffer.cast(), (*operation).length, &mut cpath) {
                        linux_syscall::syscall3(
                            linux_syscall::nr::MKDIRAT,
                            linux_syscall::AT_FDCWD as usize,
                            p as usize,
                            (*operation).aux_u32 as usize,
                        )
                    } else {
                        -(linux_syscall::EINVAL as isize)
                    }
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    -(38isize)
                }
            }
            IoKind::PathRemove => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let mut cpath = [0u8; 4096];
                    if let Some(p) = unix_abi::copy_cstr((*operation).buffer.cast(), (*operation).length, &mut cpath) {
                        let flags = if ((*operation).aux_u32 & unix_abi::ABI_PATH_REMOVE_DIR) != 0 {
                            linux_syscall::AT_REMOVEDIR
                        } else {
                            0
                        };
                        linux_syscall::syscall3(
                            linux_syscall::nr::UNLINKAT,
                            linux_syscall::AT_FDCWD as usize,
                            p as usize,
                            flags as usize,
                        )
                    } else {
                        -(linux_syscall::EINVAL as isize)
                    }
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    -(38isize)
                }
            }
            IoKind::PathRename => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let mut old_c = [0u8; 4096];
                    let mut new_c = [0u8; 4096];
                    match (
                        unix_abi::copy_cstr((*operation).buffer.cast(), (*operation).length, &mut old_c),
                        unix_abi::copy_cstr((*operation).extra_ptr.cast(), (*operation).extra_len, &mut new_c),
                    ) {
                        (Some(o), Some(n)) => linux_syscall::syscall5(
                            linux_syscall::nr::RENAMEAT2,
                            linux_syscall::AT_FDCWD as usize,
                            o as usize,
                            linux_syscall::AT_FDCWD as usize,
                            n as usize,
                            0,
                        ),
                        _ => -(linux_syscall::EINVAL as isize),
                    }
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    -(38isize)
                }
            }
            IoKind::PathLink | IoKind::PathSymlink | IoKind::PathReadlink | IoKind::PathChmod | IoKind::PathSetTimes => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let mut c1 = [0u8; 4096];
                    let mut c2 = [0u8; 4096];
                    match (*operation).kind {
                        IoKind::PathSymlink => {
                            match (
                                unix_abi::copy_cstr((*operation).buffer.cast(), (*operation).length, &mut c1),
                                unix_abi::copy_cstr((*operation).extra_ptr.cast(), (*operation).extra_len, &mut c2),
                            ) {
                                (Some(target), Some(linkp)) => linux_syscall::syscall3(
                                    linux_syscall::nr::SYMLINKAT,
                                    target as usize,
                                    linux_syscall::AT_FDCWD as usize,
                                    linkp as usize,
                                ),
                                _ => -(linux_syscall::EINVAL as isize),
                            }
                        }
                        IoKind::PathReadlink => {
                            if let Some(p) = unix_abi::copy_cstr((*operation).buffer.cast(), (*operation).length, &mut c1) {
                                linux_syscall::syscall4(
                                    linux_syscall::nr::READLINKAT,
                                    linux_syscall::AT_FDCWD as usize,
                                    p as usize,
                                    (*operation).extra_ptr as usize,
                                    (*operation).extra_len,
                                )
                            } else {
                                -(linux_syscall::EINVAL as isize)
                            }
                        }
                        IoKind::PathChmod => {
                            if let Some(p) = unix_abi::copy_cstr((*operation).buffer.cast(), (*operation).length, &mut c1) {
                                linux_syscall::syscall4(
                                    linux_syscall::nr::FCHMODAT,
                                    linux_syscall::AT_FDCWD as usize,
                                    p as usize,
                                    (*operation).aux_u32 as usize,
                                    0,
                                )
                            } else {
                                -(linux_syscall::EINVAL as isize)
                            }
                        }
                        _ => -(linux_syscall::ENOSYS as isize),
                    }
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    -(38isize)
                }
            }
            IoKind::ProcessPipe => {
                #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
                {
                    let mut fds = [0i32; 2];
                    let r = linux_syscall::pipe2(&mut fds, linux_syscall::O_CLOEXEC);
                    if r == 0 {
                        (*operation).aux_u64 = ((fds[0] as u64) & 0xffff_ffff) | (((fds[1] as u64) & 0xffff_ffff) << 32);
                    }
                    r
                }
                #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
                {
                    -(38isize)
                }
            }
            IoKind::NetOpen | IoKind::NetAccept | IoKind::NetLookup | IoKind::NetCertVerify | IoKind::ProcessSpawn | IoKind::ProcessWait | IoKind::SignalWait => {
                -(38isize)
            }
        };
        let success = if matches!(
            (*operation).kind,
            IoKind::Truncate
                | IoKind::Timer
                | IoKind::PathStat
                | IoKind::PathMkdir
                | IoKind::PathRemove
                | IoKind::PathRename
                | IoKind::PathSymlink
                | IoKind::PathChmod
                | IoKind::ProcessPipe
        ) {
            result == 0
        } else {
            result >= 0
        };
        if success {
            break (0, result as u64);
        }
        #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
        let errno = if result < 0 && result >= -4095 {
            (-result) as i32
        } else {
            match errno_location {
                Some(errno_location) => *errno_location(),
                None => 0,
            }
        };
        #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
        let errno = match errno_location {
            Some(errno_location) => *errno_location(),
            None => 0,
        };
        if errno == ERRNO_EINTR {
            continue;
        }
        break (map_io_errno(errno), 0);
    };
    ptr::addr_of_mut!((*operation).error).write(error);
    ptr::addr_of_mut!((*operation).result).write(result);
}

unsafe fn pop_work(state: *mut PlatformState) -> *mut IoOperation {
    let operation = ptr::addr_of!((*state).work_head).read();
    if operation.is_null() {
        return ptr::null_mut();
    }
    let next = ptr::addr_of!((*operation).queue_next).read();
    ptr::addr_of_mut!((*state).work_head).write(next);
    if next.is_null() {
        ptr::addr_of_mut!((*state).work_tail).write(ptr::null_mut());
    }
    ptr::addr_of_mut!((*operation).queue_next).write(ptr::null_mut());
    operation
}

unsafe fn push_completion(state: *mut PlatformState, operation: *mut IoOperation) {
    ptr::addr_of_mut!((*operation).queue_next).write(ptr::null_mut());
    let tail = ptr::addr_of!((*state).completion_tail).read();
    if tail.is_null() {
        ptr::addr_of_mut!((*state).completion_head).write(operation);
    } else {
        ptr::addr_of_mut!((*tail).queue_next).write(operation);
    }
    ptr::addr_of_mut!((*state).completion_tail).write(operation);
}

unsafe fn process_one_io_operation(state: *mut PlatformState) -> bool {
    queue_lock(state);
    let operation = pop_work(state);
    queue_unlock(state);
    if operation.is_null() {
        return false;
    }
    execute_io_operation(operation);
    queue_lock(state);
    push_completion(state, operation);
    if (*state).synchronization_initialized {
        if let Some(signal) = (*state).pthread_cond_signal {
            if signal(ptr::addr_of_mut!((*state).completion_ready).cast()) != 0 {
                queue_unlock(state);
                terminate_after_worker_failure(17);
            }
        }
    }
    queue_unlock(state);
    true
}

#[cfg(not(test))]
unsafe extern "C" fn run_io_worker(context: *mut c_void) -> *mut c_void {
    let state = context.cast::<PlatformState>();
    loop {
        queue_lock(state);
        while ptr::addr_of!((*state).work_head).read().is_null()
            && !ptr::addr_of!((*state).stopping_workers).read()
        {
            let Some(wait) = ptr::addr_of!((*state).pthread_cond_wait).read() else {
                queue_unlock(state);
                terminate_after_worker_failure(17);
            };
            if wait(
                ptr::addr_of_mut!((*state).work_ready).cast(),
                ptr::addr_of_mut!((*state).queue_mutex).cast(),
            ) != 0
            {
                queue_unlock(state);
                terminate_after_worker_failure(17);
            }
        }
        if ptr::addr_of!((*state).stopping_workers).read()
            && ptr::addr_of!((*state).work_head).read().is_null()
        {
            queue_unlock(state);
            return ptr::null_mut();
        }
        let operation = pop_work(state);
        queue_unlock(state);
        if operation.is_null() {
            continue;
        }

        execute_io_operation(operation);

        queue_lock(state);
        push_completion(state, operation);
        if let Some(signal) = ptr::addr_of!((*state).pthread_cond_signal).read() {
            if signal(ptr::addr_of_mut!((*state).completion_ready).cast()) != 0 {
                queue_unlock(state);
                terminate_after_worker_failure(17);
            }
        }
        queue_unlock(state);
    }
}

#[cfg(test)]
unsafe extern "C" fn run_io_worker(_context: *mut c_void) -> *mut c_void {
    let _ = process_one_io_operation(state_ptr());
    ptr::null_mut()
}

unsafe fn pop_completion(state: *mut PlatformState) -> *mut IoOperation {
    let operation = ptr::addr_of!((*state).completion_head).read();
    if operation.is_null() {
        return ptr::null_mut();
    }
    let next = ptr::addr_of!((*operation).queue_next).read();
    ptr::addr_of_mut!((*state).completion_head).write(next);
    if next.is_null() {
        ptr::addr_of_mut!((*state).completion_tail).write(ptr::null_mut());
    }
    ptr::addr_of_mut!((*operation).queue_next).write(ptr::null_mut());
    operation
}

unsafe fn unlink_active_operation(state: *mut PlatformState, operation: *mut IoOperation) {
    let mut link = ptr::addr_of_mut!((*state).active_operations);
    while !(*link).is_null() {
        if *link == operation {
            link.write((*operation).active_next);
            (*state).active_count -= 1;
            return;
        }
        link = ptr::addr_of_mut!((*(*link)).active_next);
    }
    terminate_after_worker_failure(18);
}

unsafe fn release_file_reference(file: *mut FileHandle) {
    if file.is_null() {
        return;
    }
    if (*file).references == 0 {
        terminate_after_worker_failure(18);
    }
    (*file).references -= 1;
    if (*file).closing && (*file).references == 0 {
        let state = state_ptr();
        if let Some(close) = ptr::addr_of!((*state).close).read() {
            let _ = close((*file).fd);
        }
        if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
            free(file.cast());
        }
    }
}

unsafe fn advance_sequence(operation: *mut IoOperation) {
    let sequence = (*operation).sequence;
    if sequence.is_null() {
        return;
    }
    if (*sequence).head != operation {
        terminate_after_worker_failure(18);
    }
    let next = (*operation).sequence_next;
    (*sequence).head = next;
    if next.is_null() {
        (*sequence).tail = ptr::null_mut();
        (*sequence).active = false;
    } else {
        enqueue_work(state_ptr(), next);
    }
}

unsafe fn publish_one_completion(state: *mut PlatformState, operation: *mut IoOperation) {
    if (*operation).kind == IoKind::Open && (*operation).error == 0 {
        let next_handle = ptr::addr_of!((*state).next_handle).read();
        let malloc = ptr::addr_of!((*state).malloc).read();
        let close = ptr::addr_of!((*state).close).read();
        let next_token = next_handle.checked_add(1);
        let entry = malloc
            .map(|malloc| malloc(core::mem::size_of::<FileHandle>()).cast::<FileHandle>())
            .unwrap_or(ptr::null_mut());
        if next_token.is_none() || entry.is_null() {
            if let Some(close) = close {
                let _ = close((*operation).result as i32);
            }
            if !entry.is_null() {
                if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
                    free(entry.cast());
                }
            }
            (*operation).error = if next_token.is_none() {
                ERROR_OVERFLOW
            } else {
                ERROR_NOMEM
            };
            (*operation).result = 0;
        } else {
            entry.write(FileHandle {
                token: next_handle,
                fd: (*operation).result as i32,
                next: ptr::addr_of!((*state).file_handles).read(),
                references: 0,
                closing: false,
                sequence: IoSequence::empty(),
            });
            ptr::addr_of_mut!((*state).file_handles).write(entry);
            if let Some(next_token) = next_token {
                ptr::addr_of_mut!((*state).next_handle).write(next_token);
            }
            (*operation).result = next_handle;
        }
    }
    complete_io(
        (*operation).overlapped,
        (*operation).error,
        (*operation).result,
    );
    advance_sequence(operation);
    unlink_active_operation(state, operation);
    release_file_reference((*operation).file_handle);
    release_operation_resources(operation);
    if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
        free(operation.cast());
    }
}

unsafe fn publish_ready_completions(state: *mut PlatformState) -> usize {
    let mut count = 0;
    loop {
        #[cfg(all(
            not(test),
            not(feature = "darwin"),
            any(feature = "linux", target_os = "linux", target_os = "android")
        ))]
        let operation = {
            linux_runtime::comp_lock().lock();
            let op = pop_completion(state);
            linux_runtime::comp_lock().unlock();
            op
        };
        #[cfg(not(all(
            not(test),
            not(feature = "darwin"),
            any(feature = "linux", target_os = "linux", target_os = "android")
        )))]
        let operation = {
            queue_lock(state);
            let op = pop_completion(state);
            queue_unlock(state);
            op
        };
        if operation.is_null() {
            return count;
        }
        publish_one_completion(state, operation);
        count += 1;
    }
}

unsafe fn wait_and_publish_io() -> bool {
    let state = state_ptr();
    #[cfg(all(
        not(test),
        not(feature = "darwin"),
        any(feature = "linux", target_os = "linux", target_os = "android")
    ))]
    {
        loop {
            linux_runtime::comp_lock().lock();
            let has_comp = !ptr::addr_of!((*state).completion_head).read().is_null();
            linux_runtime::comp_lock().unlock();
            if has_comp {
                return publish_ready_completions(state) != 0;
            }
            if ptr::addr_of!((*state).active_count).read() == 0 {
                return false;
            }
            let rfd = (*state).pipe_fds[0];
            let mut pfd = linux_syscall::PollFd {
                fd: rfd,
                events: linux_syscall::POLLIN,
                revents: 0,
            };
            let _ = linux_syscall::ppoll(&mut pfd, 1, ptr::null());
            let mut drain = [0u8; 64];
            let _ = linux_syscall::syscall3(
                linux_syscall::nr::READ,
                rfd as usize,
                drain.as_mut_ptr() as usize,
                drain.len(),
            );
        }
    }
    #[cfg(not(all(
        not(test),
        not(feature = "darwin"),
        any(feature = "linux", target_os = "linux", target_os = "android")
    )))]
    {
    queue_lock(state);
    while ptr::addr_of!((*state).completion_head).read().is_null()
        && ptr::addr_of!((*state).active_count).read() != 0
    {
        let Some(wait) = ptr::addr_of!((*state).pthread_cond_wait).read() else {
            queue_unlock(state);
            return false;
        };
        if wait(
            ptr::addr_of_mut!((*state).completion_ready).cast(),
            ptr::addr_of_mut!((*state).queue_mutex).cast(),
        ) != 0
        {
            queue_unlock(state);
            return false;
        }
    }
    let has_completion = !ptr::addr_of!((*state).completion_head).read().is_null();
    queue_unlock(state);
    if !has_completion {
        return false;
    }
    publish_ready_completions(state) != 0
    }
}

unsafe fn publish_pending_io() {
    let state = state_ptr();
    let _ = publish_ready_completions(state);
}

#[cfg(test)]
unsafe fn join_and_publish_pending_io() -> bool {
    let state = state_ptr();
    publish_ready_completions(state) != 0
}

unsafe fn terminate_after_worker_failure(status: i32) -> ! {
    if let Some(exit) = state_mut().exit {
        exit(status);
    }
    loop {}
}

unsafe fn destroy_worker_pool(state: *mut PlatformState) {
    if !(*state).synchronization_initialized {
        return;
    }
    queue_lock(state);
    ptr::addr_of_mut!((*state).stopping_workers).write(true);
    if let Some(broadcast) = ptr::addr_of!((*state).pthread_cond_broadcast).read() {
        if broadcast(ptr::addr_of_mut!((*state).work_ready).cast()) != 0 {
            queue_unlock(state);
            terminate_after_worker_failure(17);
        }
    }
    queue_unlock(state);

    let join = ptr::addr_of!((*state).pthread_join).read();
    let Some(join) = join else {
        terminate_after_worker_failure(17);
    };
    let mut index = 0;
    while index < (*state).worker_count {
        let worker = ptr::addr_of_mut!((*state).workers)
            .cast::<IoWorker>()
            .add(index);
        if (*worker).started && join((*worker).thread, ptr::null_mut()) != 0 {
            terminate_after_worker_failure(17);
        }
        (*worker).started = false;
        (*worker).thread = 0;
        index += 1;
    }
    (*state).worker_count = 0;

    let cond_destroy = ptr::addr_of!((*state).pthread_cond_destroy).read();
    let mutex_destroy = ptr::addr_of!((*state).pthread_mutex_destroy).read();
    if cond_destroy
        .map(|destroy| {
            destroy(ptr::addr_of_mut!((*state).work_ready).cast()) == 0
                && destroy(ptr::addr_of_mut!((*state).completion_ready).cast()) == 0
        })
        != Some(true)
        || mutex_destroy
            .map(|destroy| destroy(ptr::addr_of_mut!((*state).queue_mutex).cast()) == 0)
            != Some(true)
    {
        terminate_after_worker_failure(17);
    }
    (*state).synchronization_initialized = false;
    (*state).stopping_workers = false;
}

unsafe fn initialize_worker_pool(state: *mut PlatformState) -> bool {
    let (Some(mutex_init), Some(cond_init), Some(create)) = (
        ptr::addr_of!((*state).pthread_mutex_init).read(),
        ptr::addr_of!((*state).pthread_cond_init).read(),
        ptr::addr_of!((*state).pthread_create).read(),
    ) else {
        return false;
    };
    if mutex_init(ptr::addr_of_mut!((*state).queue_mutex).cast(), ptr::null()) != 0 {
        return false;
    }
    if cond_init(ptr::addr_of_mut!((*state).work_ready).cast(), ptr::null()) != 0 {
        if let Some(destroy) = (*state).pthread_mutex_destroy {
            let _ = destroy(ptr::addr_of_mut!((*state).queue_mutex).cast());
        }
        return false;
    }
    if cond_init(ptr::addr_of_mut!((*state).completion_ready).cast(), ptr::null()) != 0 {
        if let Some(destroy) = (*state).pthread_cond_destroy {
            let _ = destroy(ptr::addr_of_mut!((*state).work_ready).cast());
        }
        if let Some(destroy) = (*state).pthread_mutex_destroy {
            let _ = destroy(ptr::addr_of_mut!((*state).queue_mutex).cast());
        }
        return false;
    }
    (*state).synchronization_initialized = true;
    (*state).stopping_workers = false;
    (*state).worker_count = 0;

    while (*state).worker_count < IO_WORKER_COUNT {
        let index = (*state).worker_count;
        let mut thread = 0usize;
        let status = create(
            &mut thread,
            ptr::null(),
            Some(run_io_worker),
            state.cast(),
        );
        if status != 0 {
            destroy_worker_pool(state);
            return false;
        }
        let worker = ptr::addr_of_mut!((*state).workers)
            .cast::<IoWorker>()
            .add(index);
        (*worker).thread = thread;
        (*worker).started = true;
        (*state).worker_count += 1;
    }
    true
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_read(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *mut u8,
    length: usize,
) {
    begin_io(ov, handle, buffer.cast(), length, false)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_read(ov: *mut Overlapped, handle: u64, buffer: *mut u8, length: usize) {
    begin_io(ov, handle, buffer.cast(), length, false)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_seek(ov: *mut Overlapped, handle: u64, offset: i64, whence: u32) {
    begin_seek(ov, handle, offset, whence)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_seek(ov: *mut Overlapped, handle: u64, offset: i64, whence: u32) {
    begin_seek(ov, handle, offset, whence)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_truncate(ov: *mut Overlapped, handle: u64, length: i64) {
    begin_truncate(ov, handle, length)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_truncate(ov: *mut Overlapped, handle: u64, length: i64) {
    begin_truncate(ov, handle, length)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_path_open(
    ov: *mut Overlapped,
    path: *const u8,
    length: usize,
    flags: u32,
) {
    begin_path_open(ov, path, length, flags)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_path_open(
    ov: *mut Overlapped,
    path: *const u8,
    length: usize,
    flags: u32,
) {
    begin_path_open(ov, path, length, flags)
}

unsafe fn close_file_handle(handle: u64) {
    if handle < 3 {
        return;
    }
    let state = state_ptr();
    let mut link = ptr::addr_of_mut!((*state).file_handles);
    while !(*link).is_null() {
        let entry = *link;
        if (*entry).token == handle {
            *link = (*entry).next;
            (*entry).next = ptr::null_mut();
            (*entry).closing = true;
            if (*entry).references == 0 {
                if let Some(close) = ptr::addr_of!((*state).close).read() {
                    let _ = close((*entry).fd);
                }
                if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
                    free(entry.cast());
                }
            }
            return;
        }
        link = ptr::addr_of_mut!((*entry).next);
    }
}

unsafe fn close_all_file_handles() {
    let state = state_ptr();
    let mut entry = ptr::addr_of!((*state).file_handles).read();
    ptr::addr_of_mut!((*state).file_handles).write(ptr::null_mut());
    while !entry.is_null() {
        let next = (*entry).next;
        if (*entry).references != 0 {
            terminate_after_worker_failure(18);
        }
        if let Some(close) = ptr::addr_of!((*state).close).read() {
            let _ = close((*entry).fd);
        }
        if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
            free(entry.cast());
        }
        entry = next;
    }
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_handle_close(handle: u64) {
    close_file_handle(handle)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_handle_close(handle: u64) {
    close_file_handle(handle)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_exit(status: i32) -> ! {
    if let Some(exit) = state_mut().exit {
        exit(status);
    }
    loop {}
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_write(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *const u8,
    length: usize,
) {
    begin_io(ov, handle, buffer as *mut u8 as *mut c_void, length, true)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_write(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *const u8,
    length: usize,
) {
    begin_io(ov, handle, buffer as *mut u8 as *mut c_void, length, true)
}

#[cfg(target_arch = "x86_64")]
#[no_mangle]
unsafe extern "win64" fn pal_abi_unsupported_exit(status: i32) -> ! {
    if let Some(exit) = state_mut().exit {
        exit(status);
    }
    loop {}
}

#[cfg(target_arch = "aarch64")]
#[no_mangle]
unsafe extern "C" fn pal_abi_unsupported_exit(status: i32) -> ! {
    if let Some(exit) = state_mut().exit {
        exit(status);
    }
    loop {}
}

#[cfg(all(
    not(test),
    not(feature = "darwin"),
    any(feature = "linux", target_os = "linux", target_os = "android")
))]
mod linux_runtime {
    use super::*;
    use crate::linux_syscall as sys;
    use crate::worker_pool::{FutexMutex, LinuxWorkerPool};

    pub unsafe fn comp_lock() -> &'static FutexMutex {
        &*ptr::addr_of!((*super::state_ptr()).comp_lock).cast::<FutexMutex>()
    }

    pub unsafe fn pool() -> &'static mut LinuxWorkerPool {
        &mut *(ptr::addr_of!((*super::state_ptr()).linux_pool).read() as *mut LinuxWorkerPool)
    }

    pub unsafe fn execute_work_item(op_id: usize) {
        let op = op_id as *mut IoOperation;
        if (*op).cancelled {
            (*op).error = unix_abi::ABI_ERROR_CANCELLED;
            (*op).result = 0;
        } else {
            super::execute_io_operation(op);
        }
        comp_lock().lock();
        super::push_completion(super::state_ptr(), op);
        comp_lock().unlock();
    }

    pub unsafe extern "C" fn raw_read(fd: i32, buf: *mut c_void, count: usize) -> isize {
        sys::syscall3(sys::nr::READ, fd as usize, buf as usize, count)
    }
    pub unsafe extern "C" fn raw_write(fd: i32, buf: *const c_void, count: usize) -> isize {
        sys::syscall3(sys::nr::WRITE, fd as usize, buf as usize, count)
    }
    pub unsafe extern "C" fn raw_pread(
        fd: i32,
        buf: *mut c_void,
        count: usize,
        offset: i64,
    ) -> isize {
        sys::syscall4(
            sys::nr::PREAD64,
            fd as usize,
            buf as usize,
            count,
            offset as usize,
        )
    }
    pub unsafe extern "C" fn raw_open(path: *const c_char, flags: i32, mode: u32) -> i32 {
        sys::syscall4(
            sys::nr::OPENAT,
            sys::AT_FDCWD as usize,
            path as usize,
            flags as usize,
            mode as usize,
        ) as i32
    }
    pub unsafe extern "C" fn raw_lseek(fd: i32, offset: i64, whence: i32) -> i64 {
        sys::syscall3(sys::nr::LSEEK, fd as usize, offset as usize, whence as usize) as i64
    }
    pub unsafe extern "C" fn raw_ftruncate(fd: i32, len: i64) -> i32 {
        sys::syscall2(sys::nr::FTRUNCATE, fd as usize, len as usize) as i32
    }
    pub unsafe extern "C" fn raw_close(fd: i32) -> i32 {
        sys::syscall1(sys::nr::CLOSE, fd as usize) as i32
    }
    pub unsafe extern "C" fn raw_malloc(size: usize) -> *mut c_void {
        let total = (size.saturating_add(16).saturating_add(4095)) & !4095;
        let ptr = sys::mmap(
            0,
            total,
            sys::PROT_READ | sys::PROT_WRITE,
            sys::MAP_PRIVATE | sys::MAP_ANONYMOUS,
            -1,
            0,
        );
        if ptr < 0 && ptr >= -4095 {
            ptr::null_mut()
        } else {
            let base = ptr as *mut usize;
            base.write(total);
            base.cast::<u8>().add(16).cast()
        }
    }
    pub unsafe extern "C" fn raw_free(ptr: *mut c_void) {
        if ptr.is_null() {
            return;
        }
        let base = ptr.cast::<u8>().sub(16).cast::<usize>();
        let total = base.read();
        let _ = sys::munmap(base as usize, total);
    }
    pub unsafe extern "C" fn raw_exit(status: i32) -> ! {
        sys::exit_group(status)
    }
}

macro_rules! abi_fn {
    (fn $name:ident ($($arg:ident : $ty:ty),* $(,)?) $(-> $ret:ty)? $body:block) => {
        #[cfg(target_arch = "x86_64")]
        unsafe extern "win64" fn $name($($arg: $ty),*) $(-> $ret)? $body
        #[cfg(target_arch = "aarch64")]
        unsafe extern "C" fn $name($($arg: $ty),*) $(-> $ret)? $body
    };
}

abi_fn!(fn abi_alloc_mem(size: usize, _align: usize, flags: u32) -> *mut u8 {
    #[cfg(all(not(test), not(feature = "darwin"), any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        let total = (size.saturating_add(4095)) & !4095;
        if total == 0 {
            return ptr::null_mut();
        }
        let prot = if flags & 0x4 != 0 {
            linux_syscall::PROT_READ | linux_syscall::PROT_WRITE | linux_syscall::PROT_EXEC
        } else {
            linux_syscall::PROT_READ | linux_syscall::PROT_WRITE
        };
        let r = linux_syscall::mmap(0, total, prot, linux_syscall::MAP_PRIVATE | linux_syscall::MAP_ANONYMOUS, -1, 0);
        if r < 0 && r >= -4095 { ptr::null_mut() } else { r as *mut u8 }
    }
    #[cfg(not(all(not(test), not(feature = "darwin"), any(feature = "linux", target_os = "linux", target_os = "android"))))]
    {
        let _ = flags;
        match ptr::addr_of!((*state_ptr()).malloc).read() {
            Some(m) => m(size).cast(),
            None => ptr::null_mut(),
        }
    }
});

abi_fn!(fn abi_protect_mem(ptr_addr: *mut u8, size: usize, flags: u32) -> i32 {
    #[cfg(all(not(test), not(feature = "darwin"), any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        let mut prot = 0usize;
        if flags & 1 != 0 { prot |= linux_syscall::PROT_READ; }
        if flags & 2 != 0 { prot |= linux_syscall::PROT_WRITE; }
        if flags & 4 != 0 { prot |= linux_syscall::PROT_EXEC; }
        let r = linux_syscall::mprotect(ptr_addr as usize, (size + 4095) & !4095, prot);
        if r < 0 { -1 } else { 0 }
    }
    #[cfg(not(all(not(test), not(feature = "darwin"), any(feature = "linux", target_os = "linux", target_os = "android"))))]
    {
        let _ = (ptr_addr, size, flags);
        0
    }
});

abi_fn!(fn abi_free_mem(ptr_addr: *mut u8, size: usize) {
    #[cfg(all(not(test), not(feature = "darwin"), any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        if !ptr_addr.is_null() && size > 0 {
            let _ = linux_syscall::munmap(ptr_addr as usize, (size + 4095) & !4095);
        }
    }
    #[cfg(not(all(not(test), not(feature = "darwin"), any(feature = "linux", target_os = "linux", target_os = "android"))))]
    {
        let _ = size;
        if let Some(free) = ptr::addr_of!((*state_ptr()).libc_free).read() {
            free(ptr_addr.cast());
        }
    }
});

abi_fn!(fn abi_flush_icache(ptr_addr: *const u8, size: usize) {
    unix_abi::flush_icache_impl(ptr_addr, size);
});

abi_fn!(fn abi_host_panic(msg_ptr: *const u8, msg_len: usize) -> ! {
    if !msg_ptr.is_null() && msg_len > 0 {
        let slice = core::slice::from_raw_parts(msg_ptr, msg_len.min(4096));
        write_fd(2, slice);
        write_fd(2, b"\n");
    }
    if let Some(exit) = ptr::addr_of!((*state_ptr()).exit).read() {
        exit(101);
    }
    loop {}
});

abi_fn!(fn abi_rusticated_debug(val: i32) {
    let _ = val;
});

abi_fn!(fn abi_get_time() -> u64 {
    #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
    {
        linux_syscall::clock_gettime_ns(linux_syscall::CLOCK_MONOTONIC)
    }
    #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        0
    }
});

abi_fn!(fn abi_get_random(buf_ptr: *mut u8, buf_len: usize) {
    #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
    if !buf_ptr.is_null() && buf_len > 0 {
        let mut off = 0usize;
        while off < buf_len {
            let n = linux_syscall::syscall3(
                linux_syscall::nr::GETRANDOM,
                buf_ptr.add(off) as usize,
                buf_len - off,
                0,
            );
            if n <= 0 {
                break;
            }
            off += n as usize;
        }
    }
    #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
    let _ = (buf_ptr, buf_len);
});

abi_fn!(fn abi_get_env(buf_ptr: *mut u8, buf_cap: usize) -> u64 {
    let state = state_ptr();
    unix_abi::pack_env_entries(
        ptr::addr_of!((*state).envp).read(),
        ptr::addr_of!((*state).envc).read(),
        buf_ptr,
        buf_cap,
    )
});

abi_fn!(fn abi_get_cwd(buf_ptr: *mut u8, buf_cap: usize) -> u64 {
    #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
    {
        if buf_ptr.is_null() || buf_cap == 0 {
            return u64::MAX;
        }
        let r = linux_syscall::syscall2(linux_syscall::nr::GETCWD, buf_ptr as usize, buf_cap);
        if r <= 0 {
            return u64::MAX;
        }
        (r as usize).saturating_sub(1) as u64
    }
    #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        let _ = (buf_ptr, buf_cap);
        u64::MAX
    }
});

abi_fn!(fn abi_set_cwd(path_ptr: *const u8, path_len: usize) -> u32 {
    #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
    {
        let mut buf = [0u8; 4096];
        let Some(p) = unix_abi::copy_cstr(path_ptr, path_len, &mut buf) else {
            return unix_abi::ABI_ERROR_INVALID_INPUT;
        };
        let r = linux_syscall::syscall1(linux_syscall::nr::CHDIR, p as usize);
        if r == 0 { 0 } else { unix_abi::error_from_errno((-r) as i32) }
    }
    #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        let _ = (path_ptr, path_len);
        unix_abi::ABI_ERROR_NOT_SUPPORTED
    }
});

abi_fn!(fn abi_get_platform_info(out_ptr: *mut u8, out_cap: usize) -> u32 {
    unix_abi::fill_platform_info(out_ptr, out_cap, ptr::addr_of!((*state_ptr()).pid).read())
});

abi_fn!(fn abi_timer_set(ov: *mut Overlapped, delay_ms: u32) {
    if ov.is_null() {
        return;
    }
    submit_io(
        ov,
        IoOperation {
            offset: delay_ms as i64,
            ..IoOperation::new(ov, IoKind::Timer)
        },
    );
});

abi_fn!(fn abi_cancel(ov: *mut Overlapped) {
    if ov.is_null() {
        return;
    }
    let state = state_ptr();
    let mut active = ptr::addr_of!((*state).active_operations).read();
    while !active.is_null() {
        if (*active).overlapped == ov {
            (*active).cancelled = true;
            break;
        }
        active = (*active).active_next;
    }
});

abi_fn!(fn abi_dir_read(ov: *mut Overlapped, handle: u64, buf_ptr: *mut u8, buf_len: usize) {
    if ov.is_null() || (buf_ptr.is_null() && buf_len != 0) {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file_handle = find_file_handle(handle);
    if file_handle.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    submit_io(
        ov,
        IoOperation {
            fd: (*file_handle).fd,
            buffer: buf_ptr.cast(),
            length: buf_len,
            file_handle,
            sequence: ptr::addr_of_mut!((*file_handle).sequence),
            ..IoOperation::new(ov, IoKind::DirRead)
        },
    );
});

abi_fn!(fn abi_path_stat(
    ov: *mut Overlapped,
    path_ptr: *const u8,
    path_len: usize,
    flags: u32,
    stat_ptr: *mut u8,
    stat_cap: usize,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: path_ptr as *mut c_void,
            length: path_len,
            extra_ptr: stat_ptr.cast(),
            extra_len: stat_cap,
            aux_u32: flags,
            ..IoOperation::new(ov, IoKind::PathStat)
        },
    );
});

abi_fn!(fn abi_path_link(
    ov: *mut Overlapped,
    old_ptr: *const u8,
    old_len: usize,
    new_ptr: *const u8,
    new_len: usize,
    flags: u32,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: old_ptr as *mut c_void,
            length: old_len,
            extra_ptr: new_ptr as *mut c_void,
            extra_len: new_len,
            aux_u32: flags,
            ..IoOperation::new(ov, IoKind::PathLink)
        },
    );
});

abi_fn!(fn abi_path_symlink(
    ov: *mut Overlapped,
    target_ptr: *const u8,
    target_len: usize,
    link_ptr: *const u8,
    link_len: usize,
    target_kind: u32,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: target_ptr as *mut c_void,
            length: target_len,
            extra_ptr: link_ptr as *mut c_void,
            extra_len: link_len,
            aux_u32: target_kind,
            ..IoOperation::new(ov, IoKind::PathSymlink)
        },
    );
});

abi_fn!(fn abi_path_readlink(
    ov: *mut Overlapped,
    path_ptr: *const u8,
    path_len: usize,
    buf_ptr: *mut u8,
    buf_len: usize,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: path_ptr as *mut c_void,
            length: path_len,
            extra_ptr: buf_ptr.cast(),
            extra_len: buf_len,
            ..IoOperation::new(ov, IoKind::PathReadlink)
        },
    );
});

abi_fn!(fn abi_path_chmod(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32) {
    submit_io(
        ov,
        IoOperation {
            buffer: path_ptr as *mut c_void,
            length: path_len,
            aux_u32: mode,
            ..IoOperation::new(ov, IoKind::PathChmod)
        },
    );
});

abi_fn!(fn abi_path_set_times(
    ov: *mut Overlapped,
    path_ptr: *const u8,
    path_len: usize,
    atime_sec: i64,
    atime_nsec: u32,
    mtime_sec: i64,
    mtime_nsec: u32,
    flags: u32,
) {
    let _ = (atime_sec, atime_nsec, mtime_sec, mtime_nsec);
    submit_io(
        ov,
        IoOperation {
            buffer: path_ptr as *mut c_void,
            length: path_len,
            aux_u32: flags,
            ..IoOperation::new(ov, IoKind::PathSetTimes)
        },
    );
});

abi_fn!(fn abi_path_remove(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, flags: u32) {
    submit_io(
        ov,
        IoOperation {
            buffer: path_ptr as *mut c_void,
            length: path_len,
            aux_u32: flags,
            ..IoOperation::new(ov, IoKind::PathRemove)
        },
    );
});

abi_fn!(fn abi_path_mkdir(ov: *mut Overlapped, path_ptr: *const u8, path_len: usize, mode: u32) {
    submit_io(
        ov,
        IoOperation {
            buffer: path_ptr as *mut c_void,
            length: path_len,
            aux_u32: mode,
            ..IoOperation::new(ov, IoKind::PathMkdir)
        },
    );
});

abi_fn!(fn abi_path_rename(
    ov: *mut Overlapped,
    old_ptr: *const u8,
    old_len: usize,
    new_ptr: *const u8,
    new_len: usize,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: old_ptr as *mut c_void,
            length: old_len,
            extra_ptr: new_ptr as *mut c_void,
            extra_len: new_len,
            ..IoOperation::new(ov, IoKind::PathRename)
        },
    );
});

abi_fn!(fn abi_net_open(
    ov: *mut Overlapped,
    addr_ptr: *const u8,
    addr_len: usize,
    port: u32,
    flags: u32,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: addr_ptr as *mut c_void,
            length: addr_len,
            aux_u32: port,
            aux_u64: flags as u64,
            ..IoOperation::new(ov, IoKind::NetOpen)
        },
    );
});

abi_fn!(fn abi_net_accept(ov: *mut Overlapped, listener_handle: u64) {
    submit_io(
        ov,
        IoOperation {
            aux_u64: listener_handle,
            ..IoOperation::new(ov, IoKind::NetAccept)
        },
    );
});

abi_fn!(fn abi_net_lookup(
    ov: *mut Overlapped,
    name_ptr: *const u8,
    name_len: usize,
    buf_ptr: *mut u8,
    buf_cap: usize,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: name_ptr as *mut c_void,
            length: name_len,
            extra_ptr: buf_ptr.cast(),
            extra_len: buf_cap,
            ..IoOperation::new(ov, IoKind::NetLookup)
        },
    );
});

abi_fn!(fn abi_net_cert_verify(
    ov: *mut Overlapped,
    name_ptr: *const u8,
    name_len: usize,
    chain_ptr: *const u8,
    chain_len: usize,
) {
    submit_io(
        ov,
        IoOperation {
            buffer: name_ptr as *mut c_void,
            length: name_len,
            extra_ptr: chain_ptr as *mut c_void,
            extra_len: chain_len,
            ..IoOperation::new(ov, IoKind::NetCertVerify)
        },
    );
});

abi_fn!(fn abi_process_spawn(ov: *mut Overlapped, cfg_ptr: *const u8, cfg_len: usize) {
    submit_io(
        ov,
        IoOperation {
            buffer: cfg_ptr as *mut c_void,
            length: cfg_len,
            ..IoOperation::new(ov, IoKind::ProcessSpawn)
        },
    );
});

abi_fn!(fn abi_process_pipe(ov: *mut Overlapped) {
    submit_io(ov, IoOperation::new(ov, IoKind::ProcessPipe));
});

abi_fn!(fn abi_process_wait(ov: *mut Overlapped, proc_handle: u64) {
    submit_io(
        ov,
        IoOperation {
            aux_u64: proc_handle,
            ..IoOperation::new(ov, IoKind::ProcessWait)
        },
    );
});

abi_fn!(fn abi_process_signal(proc_handle: u64, signum: u32) {
    #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
    if proc_handle > 0 {
        let _ = linux_syscall::syscall2(
            linux_syscall::nr::KILL,
            proc_handle as usize,
            signum as usize,
        );
    }
    #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
    let _ = (proc_handle, signum);
});

abi_fn!(fn abi_signal_wait(ov: *mut Overlapped, signum: u32) {
    submit_io(
        ov,
        IoOperation {
            aux_u32: signum,
            ..IoOperation::new(ov, IoKind::SignalWait)
        },
    );
});

abi_fn!(fn abi_tty_set_mode(handle: u64, raw_mode: u32) {
    let _ = (handle, raw_mode);
});

abi_fn!(fn abi_tty_get_size(handle: u64) -> u32 {
    #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
    {
        let Some(fd) = resolve_handle(handle) else {
            return 0;
        };
        #[repr(C)]
        #[derive(Default)]
        struct WinSize {
            ws_row: u16,
            ws_col: u16,
            ws_xpixel: u16,
            ws_ypixel: u16,
        }
        let mut ws = WinSize::default();
        const TIOCGWINSZ: usize = 0x5413;
        let r = linux_syscall::syscall3(
            linux_syscall::nr::IOCTL,
            fd as usize,
            TIOCGWINSZ,
            (&mut ws as *mut WinSize) as usize,
        );
        if r == 0 {
            ((ws.ws_col as u32) << 16) | (ws.ws_row as u32)
        } else {
            0
        }
    }
    #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        let _ = handle;
        0
    }
});

abi_fn!(fn abi_tty_set_size(handle: u64, cols: u32, rows: u32) -> u32 {
    let _ = (handle, cols, rows);
    0
});

abi_fn!(fn abi_fd_isatty(handle: u32) -> u32 {
    #[cfg(any(feature = "linux", target_os = "linux", target_os = "android"))]
    {
        let Some(fd) = resolve_handle(handle as u64) else {
            return 0;
        };
        let mut ws = [0u16; 4];
        const TIOCGWINSZ: usize = 0x5413;
        let r = linux_syscall::syscall3(
            linux_syscall::nr::IOCTL,
            fd as usize,
            TIOCGWINSZ,
            ws.as_mut_ptr() as usize,
        );
        if r == 0 { 1 } else { 0 }
    }
    #[cfg(not(any(feature = "linux", target_os = "linux", target_os = "android")))]
    {
        let _ = handle;
        0
    }
});

unsafe fn populate_all_abi_slots(
    abi: &mut PalAbiTable,
    #[cfg(target_arch = "x86_64")] process_exit: unsafe extern "win64" fn(i32) -> !,
    #[cfg(target_arch = "aarch64")] process_exit: unsafe extern "C" fn(i32) -> !,
) {
    *abi = pal_abi_stubs::for_payload(probe_read, probe_write, process_exit);
    abi.alloc_mem = abi_alloc_mem;
    abi.protect_mem = abi_protect_mem;
    abi.free_mem = abi_free_mem;
    abi.flush_icache = abi_flush_icache;
    abi.host_panic = abi_host_panic;
    abi.rusticated_debug = abi_rusticated_debug;
    abi.process_exit = process_exit;
    abi.get_time = abi_get_time;
    abi.get_random = abi_get_random;
    abi.get_args = probe_get_args;
    abi.get_env = abi_get_env;
    abi.get_cwd = abi_get_cwd;
    abi.set_cwd = abi_set_cwd;
    abi.get_platform_info = abi_get_platform_info;
    abi.timer_set = abi_timer_set;
    abi.cancel = abi_cancel;
    abi.read = probe_read;
    abi.write = probe_write;
    abi.seek = probe_seek;
    abi.handle_truncate = probe_truncate;
    abi.handle_close = probe_handle_close;
    abi.path_open = probe_path_open;
    abi.dir_read = abi_dir_read;
    abi.path_stat = abi_path_stat;
    abi.path_link = abi_path_link;
    abi.path_symlink = abi_path_symlink;
    abi.path_readlink = abi_path_readlink;
    abi.path_chmod = abi_path_chmod;
    abi.path_set_times = abi_path_set_times;
    abi.path_remove = abi_path_remove;
    abi.path_mkdir = abi_path_mkdir;
    abi.path_rename = abi_path_rename;
    abi.net_open = abi_net_open;
    abi.net_accept = abi_net_accept;
    abi.net_lookup = abi_net_lookup;
    abi.net_cert_verify = abi_net_cert_verify;
    abi.process_spawn = abi_process_spawn;
    abi.process_pipe = abi_process_pipe;
    abi.process_wait = abi_process_wait;
    abi.process_signal = abi_process_signal;
    abi.signal_wait = abi_signal_wait;
    abi.tty_set_mode = abi_tty_set_mode;
    abi.tty_get_size = abi_tty_get_size;
    abi.tty_set_size = abi_tty_set_size;
    abi.fd_isatty = abi_fd_isatty;
}

unsafe fn run() -> i32 {
    #[cfg(all(
        not(test),
        not(feature = "darwin"),
        any(feature = "linux", target_os = "linux", target_os = "android")
    ))]
    {
        return run_linux();
    }

    #[cfg(not(all(
        not(test),
        not(feature = "darwin"),
        any(feature = "linux", target_os = "linux", target_os = "android")
    )))]
    run_darwin_or_host()
}

#[cfg(all(
    not(test),
    not(feature = "darwin"),
    any(feature = "linux", target_os = "linux", target_os = "android")
))]
unsafe fn run_linux() -> i32 {
    let host = host_abi();
    if host.hello_payload.is_null() || host.hello_payload_size == 0 {
        return 2;
    }

    let total_payload_size = PREFIX_ABI_OFFSET + host.hello_payload_size;
    let total_aligned = (total_payload_size + 4095) & !4095;
    let payload_raw = linux_syscall::mmap(
        0,
        total_aligned,
        linux_syscall::PROT_READ | linux_syscall::PROT_WRITE,
        linux_syscall::MAP_PRIVATE | linux_syscall::MAP_ANONYMOUS,
        -1,
        0,
    );
    if payload_raw < 0 && payload_raw >= -4095 {
        return 4;
    }
    let payload_alloc = payload_raw as *mut c_void;
    let alloc_bytes = payload_alloc.cast::<u8>();
    let mut z = 0;
    while z < PREFIX_ABI_OFFSET {
        alloc_bytes.add(z).write_volatile(0);
        z += 1;
    }
    let payload_code = alloc_bytes.add(PREFIX_ABI_OFFSET).cast::<c_void>();
    let payload_bytes = payload_code.cast::<u8>();
    let mut offset = 0;
    while offset < host.hello_payload_size {
        let byte = host.hello_payload.add(offset).read_volatile();
        payload_bytes.add(offset).write_volatile(byte);
        offset += 1;
    }

    {
        let state = state_mut();
        state.read = Some(linux_runtime::raw_read);
        state.write = Some(linux_runtime::raw_write);
        state.pread = Some(linux_runtime::raw_pread);
        state.open = Some(linux_runtime::raw_open);
        state.lseek = Some(linux_runtime::raw_lseek);
        state.ftruncate = Some(linux_runtime::raw_ftruncate);
        state.close = Some(linux_runtime::raw_close);
        state.malloc = Some(linux_runtime::raw_malloc);
        state.libc_free = Some(linux_runtime::raw_free);
        state.exit = Some(linux_runtime::raw_exit);
        state.file_handles = ptr::null_mut();
        state.next_handle = 3;
        state.active_operations = ptr::null_mut();
        state.active_count = 0;
        state.work_head = ptr::null_mut();
        state.work_tail = ptr::null_mut();
        state.completion_head = ptr::null_mut();
        state.completion_tail = ptr::null_mut();
        state.standard_sequences = [
            IoSequence::empty(),
            IoSequence::empty(),
            IoSequence::empty(),
        ];
        state.payload_alloc = payload_alloc;
        state.payload_total_size = total_aligned;
        state.envp = host.envp;
        state.envc = host.envc;
        state.pid = host.pid;
        state.pipe_fds = [-1, -1];
        state.io_uring_fd = -1;
        state.comp_lock = 0;
        state.linux_pool = ptr::null_mut();
    }

    if linux_syscall::pipe2(
        ptr::addr_of_mut!((*state_ptr()).pipe_fds),
        linux_syscall::O_NONBLOCK | linux_syscall::O_CLOEXEC,
    ) < 0
    {
        let _ = linux_syscall::munmap(payload_alloc as usize, total_aligned);
        return 5;
    }
    let pool_mem = linux_runtime::raw_malloc(core::mem::size_of::<worker_pool::LinuxWorkerPool>());
    if pool_mem.is_null() {
        let _ = linux_syscall::munmap(payload_alloc as usize, total_aligned);
        return 5;
    }
    ptr::write(
        pool_mem.cast::<worker_pool::LinuxWorkerPool>(),
        worker_pool::LinuxWorkerPool::new((*state_ptr()).pipe_fds[1], linux_runtime::execute_work_item),
    );
    (*state_ptr()).linux_pool = pool_mem;
    if let Some((ring_fd, _)) = linux_syscall::probe_io_uring(64) {
        (*state_ptr()).io_uring_fd = ring_fd;
    }

    #[cfg(target_arch = "x86_64")]
    let process_exit = probe_exit;
    #[cfg(target_arch = "aarch64")]
    let process_exit = linux_runtime::raw_exit;

    let mut abi_table = pal_abi_stubs::for_payload(probe_read, probe_write, process_exit);
    populate_all_abi_slots(&mut abi_table, process_exit);
    ptr::write(payload_alloc.cast::<PalAbiTable>(), abi_table);

    unix_abi::flush_icache_impl(payload_code.cast(), host.hello_payload_size);
    // Strict W^X: ABI table is RO, guest state (+0x10000..+0x20000) is RW, code (+0x20000..) is RX.
    let _ = linux_syscall::mprotect(
        payload_alloc as usize,
        0x10000,
        linux_syscall::PROT_READ,
    );
    let _ = linux_syscall::mprotect(
        (payload_alloc as usize) + PREFIX_ABI_OFFSET,
        total_aligned - PREFIX_ABI_OFFSET,
        linux_syscall::PROT_READ | linux_syscall::PROT_EXEC,
    );

    loop {
        invoke_raw_entry(payload_code);
        let state = state_ptr();
        if ptr::addr_of!((*state).active_count).read() == 0 {
            linux_runtime::pool().shutdown();
            linux_runtime::raw_free((*state).linux_pool);
            (*state).linux_pool = ptr::null_mut();
            let pfds = (*state).pipe_fds;
            if pfds[0] >= 0 {
                let _ = linux_syscall::syscall1(linux_syscall::nr::CLOSE, pfds[0] as usize);
            }
            if pfds[1] >= 0 {
                let _ = linux_syscall::syscall1(linux_syscall::nr::CLOSE, pfds[1] as usize);
            }
            if (*state).io_uring_fd >= 0 {
                let _ = linux_syscall::syscall1(
                    linux_syscall::nr::CLOSE,
                    (*state).io_uring_fd as usize,
                );
            }
            close_all_file_handles();
            let _ = linux_syscall::munmap(payload_alloc as usize, total_aligned);
            state_mut().payload_alloc = ptr::null_mut();
            return 0;
        }
        if !wait_and_publish_io() {
            terminate_after_worker_failure(5);
        }
    }
}

unsafe fn run_darwin_or_host() -> i32 {
    #[cfg(not(feature = "darwin"))]
    {
        return 2;
    }
    #[cfg(feature = "darwin")]
    {
    let host = host_abi();
    let (Some(load_library), Some(alloc), Some(free), Some(flush_cache)) =
        (host.load_library, host.alloc, host.free, host.flush_cache)
    else {
        return 2;
    };
    if host.hello_payload.is_null() || host.hello_payload_size == 0 {
        return 2;
    }

    #[cfg(not(feature = "darwin"))]
    let library_name = b"libc.so.6\0";
    #[cfg(feature = "darwin")]
    let library_name = b"/usr/lib/libSystem.B.dylib\0";

    #[cfg(not(feature = "darwin"))]
    let module = {
        let module = load_library(library_name.as_ptr().cast(), 2);
        if module.is_null() {
            load_library(b"libc.so\0".as_ptr().cast(), 2)
        } else {
            module
        }
    };
    #[cfg(feature = "darwin")]
    let module = load_library(library_name.as_ptr().cast(), 2);
    if module.is_null() {
        return 3;
    }

    let read = function_pointer!(resolve(host, module, b"read\0"), NativeReadFn);
    let write = function_pointer!(resolve(host, module, b"write\0"), NativeWriteFn);
    let pread = function_pointer!(resolve(host, module, b"pread\0"), NativePreadFn);
    let open = function_pointer!(resolve(host, module, b"open\0"), NativeOpenFn);
    let lseek = function_pointer!(resolve(host, module, b"lseek\0"), NativeLseekFn);
    let ftruncate = function_pointer!(resolve(host, module, b"ftruncate\0"), NativeFtruncateFn);
    let close = function_pointer!(resolve(host, module, b"close\0"), NativeCloseFn);
    let malloc = function_pointer!(resolve(host, module, b"malloc\0"), NativeMallocFn);
    let libc_free = function_pointer!(resolve(host, module, b"free\0"), NativeFreeFn);
    let exit = function_pointer!(resolve(host, module, b"_exit\0"), NativeExitFn);
    #[cfg(not(feature = "darwin"))]
    let errno_location = function_pointer!(
        resolve(host, module, b"__errno_location\0"),
        ErrnoLocationFn
    );
    #[cfg(feature = "darwin")]
    let errno_location = function_pointer!(resolve(host, module, b"__error\0"), ErrnoLocationFn);
    if let Some(write) = write {
        state_mut().write = Some(write);
    }
    let pthread_create =
        function_pointer!(resolve(host, module, b"pthread_create\0"), PthreadCreateFn);
    let pthread_join = function_pointer!(resolve(host, module, b"pthread_join\0"), PthreadJoinFn);
    let pthread_mutex_init =
        function_pointer!(resolve(host, module, b"pthread_mutex_init\0"), PthreadMutexInitFn);
    let pthread_mutex_destroy = function_pointer!(
        resolve(host, module, b"pthread_mutex_destroy\0"),
        PthreadMutexDestroyFn
    );
    let pthread_mutex_lock =
        function_pointer!(resolve(host, module, b"pthread_mutex_lock\0"), PthreadMutexLockFn);
    let pthread_mutex_unlock = function_pointer!(
        resolve(host, module, b"pthread_mutex_unlock\0"),
        PthreadMutexUnlockFn
    );
    let pthread_cond_init =
        function_pointer!(resolve(host, module, b"pthread_cond_init\0"), PthreadCondInitFn);
    let pthread_cond_destroy = function_pointer!(
        resolve(host, module, b"pthread_cond_destroy\0"),
        PthreadCondDestroyFn
    );
    let pthread_cond_wait =
        function_pointer!(resolve(host, module, b"pthread_cond_wait\0"), PthreadCondWaitFn);
    let pthread_cond_signal =
        function_pointer!(resolve(host, module, b"pthread_cond_signal\0"), PthreadCondSignalFn);
    let pthread_cond_broadcast = function_pointer!(
        resolve(host, module, b"pthread_cond_broadcast\0"),
        PthreadCondBroadcastFn
    );
    let (
        Some(read),
        Some(write),
        Some(pread),
        Some(open),
        Some(lseek),
        Some(ftruncate),
        Some(close),
        Some(malloc),
        Some(libc_free),
        Some(exit),
        Some(errno_location),
        Some(pthread_create),
        Some(pthread_join),
        Some(pthread_mutex_init),
        Some(pthread_mutex_destroy),
        Some(pthread_mutex_lock),
        Some(pthread_mutex_unlock),
        Some(pthread_cond_init),
        Some(pthread_cond_destroy),
        Some(pthread_cond_wait),
        Some(pthread_cond_signal),
        Some(pthread_cond_broadcast),
    ) = (
        read,
        write,
        pread,
        open,
        lseek,
        ftruncate,
        close,
        malloc,
        libc_free,
        exit,
        errno_location,
        pthread_create,
        pthread_join,
        pthread_mutex_init,
        pthread_mutex_destroy,
        pthread_mutex_lock,
        pthread_mutex_unlock,
        pthread_cond_init,
        pthread_cond_destroy,
        pthread_cond_wait,
        pthread_cond_signal,
        pthread_cond_broadcast,
    )
    else {
        write_fd(
            2,
            b"Unix platform: required libc or pthread symbol lookup failed.\n",
        );
        return 3;
    };

    let total_payload_size = PREFIX_ABI_OFFSET + host.hello_payload_size;
    let payload_alloc = alloc(total_payload_size);
    if payload_alloc.is_null() {
        write_fd(2, b"Unix platform: executable memory allocation failed.\n");
        return 4;
    }
    #[cfg(feature = "darwin")]
    let set_jit_write_protect = match host.set_jit_write_protect {
        Some(set_jit_write_protect) => set_jit_write_protect,
        None => {
            free(payload_alloc, total_payload_size);
            return 4;
        }
    };
    #[cfg(feature = "darwin")]
    set_jit_write_protect(0);

    let alloc_bytes = payload_alloc.cast::<u8>();
    let mut z = 0;
    while z < PREFIX_ABI_OFFSET {
        alloc_bytes.add(z).write_volatile(0);
        z += 1;
    }
    let payload_code = alloc_bytes.add(PREFIX_ABI_OFFSET).cast::<c_void>();
    let payload_bytes = payload_code.cast::<u8>();
    let mut offset = 0;
    while offset < host.hello_payload_size {
        let byte = host.hello_payload.add(offset).read_volatile();
        payload_bytes.add(offset).write_volatile(byte);
        offset += 1;
    }
    {
        let state = state_mut();
        state.read = Some(read);
        state.write = Some(write);
        state.pread = Some(pread);
        state.open = Some(open);
        state.lseek = Some(lseek);
        state.ftruncate = Some(ftruncate);
        state.close = Some(close);
        state.malloc = Some(malloc);
        state.libc_free = Some(libc_free);
        state.file_handles = ptr::null_mut();
        state.next_handle = 3;
        state.exit = Some(exit);
        state.errno_location = Some(errno_location);
        state.pthread_create = Some(pthread_create);
        state.pthread_join = Some(pthread_join);
        state.pthread_mutex_init = Some(pthread_mutex_init);
        state.pthread_mutex_destroy = Some(pthread_mutex_destroy);
        state.pthread_mutex_lock = Some(pthread_mutex_lock);
        state.pthread_mutex_unlock = Some(pthread_mutex_unlock);
        state.pthread_cond_init = Some(pthread_cond_init);
        state.pthread_cond_destroy = Some(pthread_cond_destroy);
        state.pthread_cond_wait = Some(pthread_cond_wait);
        state.pthread_cond_signal = Some(pthread_cond_signal);
        state.pthread_cond_broadcast = Some(pthread_cond_broadcast);
        state.synchronization_initialized = false;
        state.stopping_workers = false;
        state.workers = [const { IoWorker::empty() }; IO_WORKER_COUNT];
        state.worker_count = 0;
        state.active_operations = ptr::null_mut();
        state.active_count = 0;
        state.work_head = ptr::null_mut();
        state.work_tail = ptr::null_mut();
        state.completion_head = ptr::null_mut();
        state.completion_tail = ptr::null_mut();
        state.standard_sequences = [
            IoSequence::empty(),
            IoSequence::empty(),
            IoSequence::empty(),
        ];
        state.free = Some(free);
        state.payload_alloc = payload_alloc;
        state.payload_total_size = total_payload_size;
    }
    if !initialize_worker_pool(state_ptr()) {
        free(payload_alloc, total_payload_size);
        write_fd(2, b"Unix platform: failed to initialize I/O workers.\n");
        return 5;
    }
    #[cfg(target_arch = "x86_64")]
    let process_exit = probe_exit;
    #[cfg(target_arch = "aarch64")]
    let process_exit = exit;
    let mut abi_table = pal_abi_stubs::for_payload(probe_read, probe_write, process_exit);
    populate_all_abi_slots(&mut abi_table, process_exit);
    ptr::write(payload_alloc.cast::<PalAbiTable>(), abi_table);
    #[cfg(feature = "darwin")]
    set_jit_write_protect(1);
    flush_cache(payload_code, host.hello_payload_size);

    loop {
        invoke_raw_entry(payload_code);
        let state = state_ptr();
        if ptr::addr_of!((*state).active_count).read() == 0 {
            destroy_worker_pool(state);
            close_all_file_handles();
            free(payload_alloc, total_payload_size);
            state_mut().payload_alloc = ptr::null_mut();
            return 0;
        }
        if !wait_and_publish_io() {
            terminate_after_worker_failure(5);
        }
    }
    }
}

#[link_section = ".text.unix_platform_entry"]
#[no_mangle]
pub unsafe extern "C" fn unix_platform_entry() -> i32 {
    run()
}

#[cfg(not(test))]
#[panic_handler]
fn panic(_info: &PanicInfo) -> ! {
    loop {}
}

#[cfg(test)]
mod tests {
    use super::*;
    use core::mem::{offset_of, size_of};

    static mut TEST_RAW_ENTRY_CALLS: u32 = 0;

    #[cfg(target_arch = "x86_64")]
    #[inline(never)]
    unsafe extern "win64" fn test_raw_entry_body() {
        TEST_RAW_ENTRY_CALLS += 1;
    }

    #[cfg(target_arch = "aarch64")]
    #[inline(never)]
    unsafe extern "C" fn test_raw_entry_body() {
        TEST_RAW_ENTRY_CALLS += 1;
    }

    unsafe extern "C" fn mock_pthread_create(
        thread: *mut usize,
        _attributes: *const c_void,
        _start: Option<unsafe extern "C" fn(*mut c_void) -> *mut c_void>,
        _context: *mut c_void,
    ) -> i32 {
        thread.write(7);
        0
    }

    struct ScriptedIo {
        read_results: [isize; 4],
        read_errnos: [i32; 4],
        read_data: [u8; 8],
        read_calls: usize,
        pread_calls: usize,
        pread_offset: i64,
        write_results: [isize; 4],
        write_errnos: [i32; 4],
        write_length: usize,
        write_calls: usize,
        open_calls: usize,
        open_flags: i32,
        open_mode: u32,
        open_errno: i32,
        seek_calls: usize,
        truncate_calls: usize,
        truncate_size: i64,
        close_calls: usize,
        free_calls: usize,
        errno: i32,
        join_result: i32,
        join_calls: usize,
    }

    static mut TEST_HEAP: [usize; 512] = [0; 512];
    static mut TEST_HEAP_USED: usize = 0;

    static mut TEST_IO: ScriptedIo = ScriptedIo {
        read_results: [0; 4],
        read_errnos: [0; 4],
        read_data: [0; 8],
        read_calls: 0,
        pread_calls: 0,
        pread_offset: 0,
        write_results: [0; 4],
        write_errnos: [0; 4],
        write_length: 0,
        write_calls: 0,
        open_calls: 0,
        open_flags: 0,
        open_mode: 0,
        open_errno: 0,
        seek_calls: 0,
        truncate_calls: 0,
        truncate_size: 0,
        close_calls: 0,
        free_calls: 0,
        errno: 0,
        join_result: 0,
        join_calls: 0,
    };

    unsafe extern "C" fn scripted_read(fd: i32, buffer: *mut c_void, length: usize) -> isize {
        assert!(fd == 0 || fd == 11);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        let index = io.read_calls;
        io.read_calls += 1;
        io.errno = io.read_errnos[index];
        let result = io.read_results[index];
        if result > 0 {
            let count = core::cmp::min(result as usize, length);
            ptr::copy_nonoverlapping(io.read_data.as_ptr(), buffer.cast(), count);
        }
        result
    }

    unsafe extern "C" fn scripted_pread(
        fd: i32,
        buffer: *mut c_void,
        length: usize,
        offset: i64,
    ) -> isize {
        assert_eq!(fd, 11);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.pread_calls += 1;
        io.pread_offset = offset;
        ptr::copy_nonoverlapping(b"pos".as_ptr(), buffer.cast(), core::cmp::min(length, 3));
        3
    }

    unsafe extern "C" fn scripted_open(path: *const c_char, flags: i32, mode: u32) -> i32 {
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.open_calls += 1;
        io.open_flags = flags;
        io.open_mode = mode;
        let mut length = 0;
        while *path.add(length) != 0 {
            length += 1;
        }
        assert_eq!(
            core::slice::from_raw_parts(path.cast::<u8>(), length),
            b"file.txt"
        );
        io.errno = io.open_errno;
        if io.open_errno == 0 {
            11
        } else {
            -1
        }
    }

    unsafe extern "C" fn scripted_lseek(fd: i32, offset: i64, whence: i32) -> i64 {
        assert_eq!(fd, 11);
        assert_eq!(offset, 5);
        assert_eq!(whence, 2);
        (*ptr::addr_of_mut!(TEST_IO)).seek_calls += 1;
        27
    }

    unsafe extern "C" fn scripted_ftruncate(fd: i32, length: i64) -> i32 {
        assert_eq!(fd, 11);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.truncate_calls += 1;
        io.truncate_size = length;
        0
    }

    unsafe extern "C" fn scripted_close(fd: i32) -> i32 {
        assert_eq!(fd, 11);
        (*ptr::addr_of_mut!(TEST_IO)).close_calls += 1;
        0
    }

    unsafe extern "C" fn scripted_malloc(size: usize) -> *mut c_void {
        let alignment = core::mem::align_of::<usize>();
        let start = (TEST_HEAP_USED + alignment - 1) & !(alignment - 1);
        let Some(end) = start.checked_add(size) else {
            return ptr::null_mut();
        };
        if end > core::mem::size_of::<[usize; 512]>() {
            return ptr::null_mut();
        }
        TEST_HEAP_USED = end;
        ptr::addr_of_mut!(TEST_HEAP)
            .cast::<u8>()
            .add(start)
            .cast()
    }

    unsafe extern "C" fn scripted_free(_buffer: *mut c_void) {
        (*ptr::addr_of_mut!(TEST_IO)).free_calls += 1;
    }

    unsafe extern "C" fn scripted_write(fd: i32, _buffer: *const c_void, length: usize) -> isize {
        assert!(fd == 1 || fd == 11);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        let index = io.write_calls;
        io.write_calls += 1;
        io.write_length = length;
        io.errno = io.write_errnos[index];
        io.write_results[index]
    }

    unsafe extern "C" fn scripted_errno_location() -> *mut i32 {
        ptr::addr_of_mut!((*ptr::addr_of_mut!(TEST_IO)).errno)
    }

    unsafe extern "C" fn scripted_pthread_join(thread: usize, _result: *mut *mut c_void) -> i32 {
        assert_eq!(thread, 7);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.join_calls += 1;
        io.join_result
    }

    unsafe fn reset_io_test_state() {
        TEST_HEAP_USED = 0;
        ptr::addr_of_mut!(TEST_STATE).write(PlatformState {
            read: Some(scripted_read),
            write: Some(scripted_write),
            pread: Some(scripted_pread),
            open: Some(scripted_open),
            lseek: Some(scripted_lseek),
            ftruncate: Some(scripted_ftruncate),
            close: Some(scripted_close),
            malloc: Some(scripted_malloc),
            libc_free: Some(scripted_free),
            file_handles: ptr::null_mut(),
            next_handle: 3,
            exit: None,
            errno_location: Some(scripted_errno_location),
            pthread_create: None,
            pthread_join: Some(scripted_pthread_join),
            pthread_mutex_init: None,
            pthread_mutex_destroy: None,
            pthread_mutex_lock: None,
            pthread_mutex_unlock: None,
            pthread_cond_init: None,
            pthread_cond_destroy: None,
            pthread_cond_wait: None,
            pthread_cond_signal: None,
            pthread_cond_broadcast: None,
            queue_mutex: PthreadSyncStorage::new(),
            work_ready: PthreadSyncStorage::new(),
            completion_ready: PthreadSyncStorage::new(),
            synchronization_initialized: false,
            stopping_workers: false,
            workers: [const { IoWorker::empty() }; IO_WORKER_COUNT],
            worker_count: 0,
            active_operations: ptr::null_mut(),
            active_count: 0,
            work_head: ptr::null_mut(),
            work_tail: ptr::null_mut(),
            completion_head: ptr::null_mut(),
            completion_tail: ptr::null_mut(),
            standard_sequences: [
                IoSequence::empty(),
                IoSequence::empty(),
                IoSequence::empty(),
            ],
            free: None,
            payload_alloc: ptr::null_mut(),
            payload_total_size: 0,
            envp: ptr::null(),
            envc: 0,
            pid: 0,
            pipe_fds: [-1, -1],
            io_uring_fd: -1,
            comp_lock: 0,
            linux_pool: ptr::null_mut(),
        });
        ptr::addr_of_mut!(TEST_IO).write(ScriptedIo {
            read_results: [0; 4],
            read_errnos: [0; 4],
            read_data: [0; 8],
            read_calls: 0,
            pread_calls: 0,
            pread_offset: 0,
            write_results: [0; 4],
            write_errnos: [0; 4],
            write_length: 0,
            write_calls: 0,
            open_calls: 0,
            open_flags: 0,
            open_mode: 0,
            open_errno: 0,
            seek_calls: 0,
            truncate_calls: 0,
            truncate_size: 0,
            close_calls: 0,
            free_calls: 0,
            errno: 0,
            join_result: 0,
            join_calls: 0,
        });
    }

    fn assert_completion(ov: &Overlapped, error: u32, result: u64) {
        assert_eq!(ov.flags, pal_abi::FLAG_COMPLETED);
        assert_eq!(ov.error, error);
        assert_eq!(ov.continued, 0);
        assert_eq!(ov.result_ext, result);
    }

    #[test]
    fn test_unix_platform_prefix_layout() {
        assert_eq!(PREFIX_ABI_OFFSET, 0x20000);
        #[cfg(feature = "darwin")]
        {
            assert_eq!(size_of::<UnixPlatformHost>(), 80);
            assert_eq!(offset_of!(UnixPlatformHost, load_library), 0);
            assert_eq!(offset_of!(UnixPlatformHost, get_proc_address), 8);
            assert_eq!(offset_of!(UnixPlatformHost, alloc), 16);
            assert_eq!(offset_of!(UnixPlatformHost, free), 24);
            assert_eq!(offset_of!(UnixPlatformHost, flush_cache), 32);
            assert_eq!(offset_of!(UnixPlatformHost, set_jit_write_protect), 40);
            assert_eq!(offset_of!(UnixPlatformHost, hello_payload), 48);
            assert_eq!(offset_of!(UnixPlatformHost, hello_payload_size), 56);
            assert_eq!(offset_of!(UnixPlatformHost, argv), 64);
            assert_eq!(offset_of!(UnixPlatformHost, argc), 72);
        }
        #[cfg(not(feature = "darwin"))]
        {
            assert_eq!(size_of::<UnixPlatformHost>(), 72);
            assert_eq!(offset_of!(UnixPlatformHost, hello_payload), 0);
            assert_eq!(offset_of!(UnixPlatformHost, hello_payload_size), 8);
            assert_eq!(offset_of!(UnixPlatformHost, argv), 16);
            assert_eq!(offset_of!(UnixPlatformHost, argc), 24);
            assert_eq!(offset_of!(UnixPlatformHost, envp), 32);
            assert_eq!(offset_of!(UnixPlatformHost, envc), 40);
            assert_eq!(offset_of!(UnixPlatformHost, pid), 48);
            assert_eq!(offset_of!(UnixPlatformHost, wasm_bytes), 56);
            assert_eq!(offset_of!(UnixPlatformHost, wasm_len), 64);
        }
        assert!(size_of::<PlatformState>() <= 0x10000);
    }

    #[test]
    fn test_raw_entry_call_shim_invokes_code_without_arguments_or_result() {
        unsafe {
            TEST_RAW_ENTRY_CALLS = 0;
            invoke_raw_entry(test_raw_entry_body as *const () as *mut c_void);
            assert_eq!(ptr::addr_of!(TEST_RAW_ENTRY_CALLS).read(), 1);
        }
    }

    #[test]
    fn test_second_pending_io_request_is_accepted() {
        unsafe {
            reset_io_test_state();
            let io = &mut *ptr::addr_of_mut!(TEST_IO);
            io.write_results = [1, 2, 0, 0];
            let mut first = Overlapped::default();
            let mut second = Overlapped::default();
            let mut buffer = [0u8; 4];

            begin_io(&mut first, 1, buffer.as_mut_ptr().cast(), 1, true);
            begin_io(&mut second, 1, buffer.as_mut_ptr().cast(), 1, true);

            assert_eq!(first.flags, 0);
            assert_eq!(first.error, 0);
            assert_eq!(second.flags, 0);
            assert_eq!(second.error, 0);

            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&first, 0, 1);
            assert_eq!(second.flags, 0);

            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&second, 0, 2);
        }
    }

    #[test]
    fn test_io_submission_worker_errors_and_completion_publication() {
        unsafe {
            reset_io_test_state();
            let state = state_ptr();
            let mut overlapped = Overlapped::default();
            let mut buffer = [0u8; 4];

            overlapped.continued = 17;
            overlapped.result_ext = 23;
            begin_io(&mut overlapped, 1, ptr::null_mut(), 0, true);
            assert_completion(&overlapped, 0, 0);

            begin_io(ptr::null_mut(), 1, ptr::null_mut(), 1, true);
            begin_io(&mut overlapped, 1, ptr::null_mut(), 1, true);
            assert_completion(&overlapped, ERROR_INVALID, 0);

            begin_io(&mut overlapped, 99, buffer.as_mut_ptr().cast(), 1, true);
            assert_completion(&overlapped, ERROR_BADF, 0);

            {
                let io = &mut *ptr::addr_of_mut!(TEST_IO);
                io.write_results[0] = 2;
            }
            overlapped = Overlapped {
                flags: 0,
                error: 0,
                continued: 17,
                result_ext: 23,
            };
            begin_io(
                &mut overlapped,
                1,
                buffer.as_mut_ptr().cast(),
                buffer.len(),
                true,
            );
            assert_eq!((*state).active_count, 1);
            assert!(!(*state).work_head.is_null());
            assert_eq!(overlapped.flags, 0);
            assert_eq!(overlapped.error, 0);
            assert_eq!(overlapped.continued, 0);
            assert_eq!(overlapped.result_ext, 0);

            run_io_worker(ptr::null_mut());
            assert_eq!((*(*state).completion_head).result, 2);
            assert_eq!(overlapped.flags, 0);
            assert!(join_and_publish_pending_io());
            assert_completion(&overlapped, 0, 2);
            assert_eq!((*state).active_count, 0);
            assert!((*state).active_operations.is_null());
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_length, 4);

            let mut second_overlapped = Overlapped {
                flags: 0,
                error: 0,
                continued: 0,
                result_ext: 0,
            };
            (*ptr::addr_of_mut!(TEST_IO)).write_results[1] = 1;
            begin_io(
                &mut second_overlapped,
                1,
                buffer.as_mut_ptr().cast(),
                1,
                true,
            );
            assert_eq!((*state).active_count, 1);
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&second_overlapped, 0, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 2);

            overlapped = Overlapped {
                flags: 91,
                error: 92,
                continued: 93,
                result_ext: 94,
            };
            publish_pending_io();
            assert_eq!(overlapped.flags, 91);
            assert_eq!(overlapped.error, 92);
            assert_eq!(overlapped.continued, 93);
            assert_eq!(overlapped.result_ext, 94);

            reset_io_test_state();
            let state = state_ptr();
            (*state).pthread_create = Some(mock_pthread_create);
            {
                let io = &mut *ptr::addr_of_mut!(TEST_IO);
                io.read_results = [-1, 2, 0, 0];
                io.read_errnos = [4, 0, 0, 0];
                io.read_data = *b"OK\0\0\0\0\0\0";
            }
            let mut read_buffer = [0u8; 2];
            begin_io(
                &mut overlapped,
                0,
                read_buffer.as_mut_ptr().cast(),
                read_buffer.len(),
                false,
            );
            run_io_worker(ptr::null_mut());
            assert_eq!((*ptr::addr_of!(TEST_IO)).read_calls, 2);
            assert_eq!(read_buffer, *b"OK");
            assert!(join_and_publish_pending_io());
            assert_completion(&overlapped, 0, 2);

            reset_io_test_state();
            let state = state_ptr();
            (*state).pthread_create = Some(mock_pthread_create);
            {
                let io = &mut *ptr::addr_of_mut!(TEST_IO);
                io.write_results = [-1, 1, 0, 0];
                io.write_errnos = [ERRNO_EINTR, 0, 0, 0];
            }
            let mut write_byte = 0u8;
            begin_io(
                &mut overlapped,
                1,
                (&mut write_byte as *mut u8).cast(),
                1,
                true,
            );
            run_io_worker(ptr::null_mut());
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 2);
            assert!(join_and_publish_pending_io());
            assert_completion(&overlapped, 0, 1);

            for (is_write, result, errno, expected_error) in [
                (false, 0, 0, 0),
                (false, -1, ERRNO_EBADF, ERROR_BADF),
                (false, -1, ERRNO_EINVAL, ERROR_INVALID),
                (false, -1, ERRNO_EAGAIN, ERROR_AGAIN),
                (false, -1, ERRNO_EPIPE, ERROR_PIPE),
                (false, -1, 5, ERROR_IO),
                (true, -1, ERRNO_EPIPE, ERROR_PIPE),
                (true, -1, 5, ERROR_IO),
            ] {
                reset_io_test_state();
                let state = state_ptr();
                (*state).pthread_create = Some(mock_pthread_create);
                {
                    let io = &mut *ptr::addr_of_mut!(TEST_IO);
                    if is_write {
                        io.write_results[0] = result;
                        io.write_errnos[0] = errno;
                    } else {
                        io.read_results[0] = result;
                        io.read_errnos[0] = errno;
                    }
                }
                let mut byte = 0u8;
                let handle = if is_write { 1 } else { 0 };
                begin_io(
                    &mut overlapped,
                    handle,
                    (&mut byte as *mut u8).cast(),
                    1,
                    is_write,
                );
                run_io_worker(ptr::null_mut());
                assert!(join_and_publish_pending_io());
                assert_completion(&overlapped, expected_error, 0);
            }

            reset_io_test_state();
            let state = state_ptr();
            (*state).pthread_create = Some(mock_pthread_create);
            let mut file_ov = Overlapped::default();
            begin_path_open(
                &mut file_ov,
                b"file.txt".as_ptr(),
                8,
                2 | ABI_O_CREATE | ABI_O_APPEND | ABI_O_DIRECTORY,
            );
            assert_eq!((*state).active_count, 1);
            assert_eq!(
                ptr::addr_of!((*(*state).work_head).fd).read(),
                2 | NATIVE_O_CREATE | NATIVE_O_APPEND | NATIVE_O_DIRECTORY
            );
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&file_ov, 0, 3);
            assert_eq!((*ptr::addr_of!(TEST_IO)).open_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).open_mode, 0o666);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 2);

            let mut file_read = Overlapped::default();
            (*ptr::addr_of_mut!(TEST_IO)).read_results[0] = 2;
            (*ptr::addr_of_mut!(TEST_IO)).read_data = *b"OK\0\0\0\0\0\0";
            let mut file_read_buffer = [0u8; 2];
            begin_io(
                &mut file_read,
                3,
                file_read_buffer.as_mut_ptr().cast(),
                file_read_buffer.len(),
                false,
            );
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&file_read, 0, 2);
            assert_eq!(file_read_buffer, *b"OK");

            let mut file_write = Overlapped::default();
            (*ptr::addr_of_mut!(TEST_IO)).write_results[0] = 2;
            begin_io(
                &mut file_write,
                3,
                file_read_buffer.as_mut_ptr().cast(),
                file_read_buffer.len(),
                true,
            );
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&file_write, 0, 2);

            let mut seek_ov = Overlapped::default();
            begin_seek(&mut seek_ov, 3, 5, 2);
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&seek_ov, 0, 27);
            assert_eq!((*ptr::addr_of!(TEST_IO)).seek_calls, 1);

            let mut truncate_ov = Overlapped::default();
            begin_truncate(&mut truncate_ov, 3, 19);
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&truncate_ov, 0, 0);
            assert_eq!((*ptr::addr_of!(TEST_IO)).truncate_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).truncate_size, 19);

            #[repr(C)]
            struct PositionedOverlapped {
                header: Overlapped,
                offset: i64,
            }
            let mut positioned = PositionedOverlapped {
                header: Overlapped {
                    flags: pal_abi::READ_FLAG_POSITIONAL,
                    ..Overlapped::default()
                },
                offset: 37,
            };
            let mut positioned_buffer = [0u8; 3];
            begin_io(
                &mut positioned.header,
                3,
                positioned_buffer.as_mut_ptr().cast(),
                positioned_buffer.len(),
                false,
            );
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&positioned.header, 0, 3);
            assert_eq!(positioned_buffer, *b"pos");
            assert_eq!((*ptr::addr_of!(TEST_IO)).pread_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).pread_offset, 37);

            let mut invalid_handle = Overlapped::default();
            begin_seek(&mut invalid_handle, 99, 0, 0);
            assert_completion(&invalid_handle, ERROR_BADF, 0);

            probe_handle_close(2);
            assert_eq!((*ptr::addr_of!(TEST_IO)).close_calls, 0);
            probe_handle_close(3);
            assert_eq!((*ptr::addr_of!(TEST_IO)).close_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 8);
            assert_eq!(resolve_handle(3), None);

            let mut reopened = Overlapped::default();
            begin_path_open(&mut reopened, b"file.txt".as_ptr(), 8, 0);
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&reopened, 0, 4);
            assert_eq!(resolve_handle(3), None);
            assert_eq!(resolve_handle(4), Some(11));
            probe_handle_close(4);
            assert_eq!((*ptr::addr_of!(TEST_IO)).close_calls, 2);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 11);

            (*ptr::addr_of_mut!(TEST_IO)).open_errno = ERRNO_ENOENT;
            let mut failed_open = Overlapped::default();
            begin_path_open(&mut failed_open, b"file.txt".as_ptr(), 8, 0);
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&failed_open, 44, 0);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 13);

            let mut invalid_path = Overlapped::default();
            begin_path_open(&mut invalid_path, b"bad\0path".as_ptr(), 8, 0);
            assert_completion(&invalid_path, ERROR_INVALID, 0);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 14);

            assert_eq!(map_io_errno(ERRNO_EACCES), ERROR_ACCES);
            assert_eq!(map_io_errno(ERRNO_EEXIST), ERROR_EXIST);
            assert_eq!(map_io_errno(ERRNO_EISDIR), ERROR_ISDIR);
            assert_eq!(map_io_errno(ERRNO_ENOTDIR), ERROR_NOTDIR);
            assert_eq!(map_io_errno(ERRNO_EPERM), ERROR_PERM);
        }
    }
}
