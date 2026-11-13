#![cfg_attr(not(test), no_std)]

use core::ffi::{c_char, c_void};
#[cfg(not(test))]
use core::panic::PanicInfo;
use core::ptr;

#[path = "../abi/guest.rs"]
mod pal_abi;
#[path = "../abi/guest_stubs.rs"]
mod pal_abi_stubs;
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
            active_next: ptr::null_mut(),
            queue_next: ptr::null_mut(),
            sequence_next: ptr::null_mut(),
            overlapped: ov,
            kind,
            fd,
            buffer,
            length,
            offset,
            whence: 0,
            error: 0,
            result: 0,
            file_handle,
            sequence,
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
            active_next: ptr::null_mut(),
            queue_next: ptr::null_mut(),
            sequence_next: ptr::null_mut(),
            overlapped: ov,
            kind: IoKind::Seek,
            fd: (*file_handle).fd,
            buffer: ptr::null_mut(),
            length: 0,
            offset,
            whence: whence as i32,
            error: 0,
            result: 0,
            file_handle,
            sequence: ptr::addr_of_mut!((*file_handle).sequence),
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
            active_next: ptr::null_mut(),
            queue_next: ptr::null_mut(),
            sequence_next: ptr::null_mut(),
            overlapped: ov,
            kind: IoKind::Truncate,
            fd: (*file_handle).fd,
            buffer: ptr::null_mut(),
            length: 0,
            offset: length,
            whence: 0,
            error: 0,
            result: 0,
            file_handle,
            sequence: ptr::addr_of_mut!((*file_handle).sequence),
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
            active_next: ptr::null_mut(),
            queue_next: ptr::null_mut(),
            sequence_next: ptr::null_mut(),
            overlapped: ov,
            kind: IoKind::Open,
            fd: native_flags,
            buffer: path_buffer.cast(),
            length,
            offset: 0,
            whence: 0,
            error: 0,
            result: 0,
            file_handle: ptr::null_mut(),
            sequence: ptr::null_mut(),
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
        };
        let success = if matches!((*operation).kind, IoKind::Truncate) {
            result == 0
        } else {
            result >= 0
        };
        if success {
            break (0, result as u64);
        }
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
        queue_lock(state);
        let operation = pop_completion(state);
        queue_unlock(state);
        if operation.is_null() {
            return count;
        }
        publish_one_completion(state, operation);
        count += 1;
    }
}

unsafe fn wait_and_publish_io() -> bool {
    let state = state_ptr();
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

unsafe fn run() -> i32 {
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
    abi_table.get_args = probe_get_args;
    abi_table.seek = probe_seek;
    abi_table.handle_truncate = probe_truncate;
    abi_table.handle_close = probe_handle_close;
    abi_table.path_open = probe_path_open;
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
