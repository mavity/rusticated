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

#[repr(C)]
struct FileHandle {
    token: u64,
    fd: i32,
    next: *mut FileHandle,
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
}

#[repr(C)]
#[derive(Clone, Copy)]
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
struct PendingIo {
    overlapped: *mut Overlapped,
    kind: IoKind,
    fd: i32,
    buffer: *mut c_void,
    length: usize,
    offset: i64,
    whence: i32,
    error: u32,
    result: u64,
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
    worker_thread: usize,
    queued_io: bool,
    pending_io: PendingIo,
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
    worker_thread: 0,
    queued_io: false,
    pending_io: PendingIo {
        overlapped: ptr::null_mut(),
        kind: IoKind::Read,
        fd: 0,
        buffer: ptr::null_mut(),
        length: 0,
        offset: 0,
        whence: 0,
        error: 0,
        result: 0,
    },
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

unsafe fn resolve_handle(handle: u64) -> Option<i32> {
    if handle <= 2 {
        return Some(handle as i32);
    }
    let mut entry = state_mut().file_handles;
    while !entry.is_null() {
        if (*entry).token == handle {
            return Some((*entry).fd);
        }
        entry = (*entry).next;
    }
    None
}

unsafe fn release_pending_resources(request: PendingIo) {
    if let (IoKind::Open, Some(free)) = (request.kind, state_mut().libc_free) {
        if !request.buffer.is_null() {
            free(request.buffer);
        }
    }
}

unsafe fn reset_pending_io(state: *mut PlatformState) {
    ptr::addr_of_mut!((*state).worker_thread).write(0);
    ptr::addr_of_mut!((*state).queued_io).write(false);
    ptr::addr_of_mut!((*state).pending_io).write(PendingIo {
        overlapped: ptr::null_mut(),
        kind: IoKind::Read,
        fd: 0,
        buffer: ptr::null_mut(),
        length: 0,
        offset: 0,
        whence: 0,
        error: 0,
        result: 0,
    });
}

unsafe fn submit_io(ov: *mut Overlapped, request: PendingIo) {
    if ov.is_null() {
        release_pending_resources(request);
        return;
    }
    let state = state_ptr();
    let create = ptr::addr_of!((*state).pthread_create).read();
    let thread_active = ptr::addr_of!((*state).worker_thread).read() != 0;
    let queued_io = ptr::addr_of!((*state).queued_io).read();
    let Some(create) = create else {
        release_pending_resources(request);
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    if thread_active || queued_io {
        release_pending_resources(request);
        complete_io(ov, ERROR_AGAIN, 0);
        return;
    }

    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;
    ptr::addr_of_mut!((*state).pending_io).write(request);
    ptr::addr_of_mut!((*state).pending_io.overlapped).write(ov);
    let mut thread = 0usize;
    let status = create(
        &mut thread,
        ptr::null(),
        Some(run_io_worker),
        ptr::null_mut(),
    );
    if status != 0 || thread == 0 {
        release_pending_resources(request);
        reset_pending_io(state);
        complete_io(ov, ERROR_AGAIN, 0);
    } else {
        ptr::addr_of_mut!((*state).worker_thread).write(thread);
        ptr::addr_of_mut!((*state).queued_io).write(true);
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
    let standard_fd = match (handle, is_write) {
        (0, false) => Some(0),
        (1 | 2, true) => Some(handle as i32),
        (0..=2, _) => None,
        _ => resolve_handle(handle),
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
    let (kind, offset) = if positional {
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
        (IoKind::PositionalRead, offset)
    } else if is_write {
        (IoKind::Write, 0)
    } else {
        (IoKind::Read, 0)
    };
    submit_io(
        ov,
        PendingIo {
            overlapped: ov,
            kind,
            fd,
            buffer,
            length,
            offset,
            whence: 0,
            error: 0,
            result: 0,
        },
    );
}

unsafe fn begin_seek(ov: *mut Overlapped, handle: u64, offset: i64, whence: u32) {
    if ov.is_null() || whence > 2 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let Some(fd) = resolve_handle(handle) else {
        complete_io(ov, ERROR_BADF, 0);
        return;
    };
    submit_io(
        ov,
        PendingIo {
            overlapped: ov,
            kind: IoKind::Seek,
            fd,
            buffer: ptr::null_mut(),
            length: 0,
            offset,
            whence: whence as i32,
            error: 0,
            result: 0,
        },
    );
}

unsafe fn begin_truncate(ov: *mut Overlapped, handle: u64, length: i64) {
    if ov.is_null() || length < 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let Some(fd) = resolve_handle(handle) else {
        complete_io(ov, ERROR_BADF, 0);
        return;
    };
    submit_io(
        ov,
        PendingIo {
            overlapped: ov,
            kind: IoKind::Truncate,
            fd,
            buffer: ptr::null_mut(),
            length: 0,
            offset: length,
            whence: 0,
            error: 0,
            result: 0,
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
        PendingIo {
            overlapped: ov,
            kind: IoKind::Open,
            fd: native_flags,
            buffer: path_buffer.cast(),
            length,
            offset: 0,
            whence: 0,
            error: 0,
            result: 0,
        },
    );
}

unsafe extern "C" fn run_io_worker(_context: *mut c_void) -> *mut c_void {
    let state = state_ptr();
    let request = ptr::addr_of!((*state).pending_io).read();
    let read = ptr::addr_of!((*state).read).read();
    let write = ptr::addr_of!((*state).write).read();
    let pread = ptr::addr_of!((*state).pread).read();
    let open = ptr::addr_of!((*state).open).read();
    let lseek = ptr::addr_of!((*state).lseek).read();
    let ftruncate = ptr::addr_of!((*state).ftruncate).read();
    let errno_location = ptr::addr_of!((*state).errno_location).read();
    let (error, result) = loop {
        let result = match request.kind {
            IoKind::Read => match read {
                Some(read) => read(request.fd, request.buffer, request.length),
                None => -1,
            },
            IoKind::Write => match write {
                Some(write) => write(request.fd, request.buffer.cast(), request.length),
                None => -1,
            },
            IoKind::PositionalRead => match pread {
                Some(pread) => pread(request.fd, request.buffer, request.length, request.offset),
                None => -1,
            },
            IoKind::Open => {
                let opened = match open {
                    Some(open) => open(request.buffer.cast(), request.fd, 0o666),
                    None => -1,
                };
                opened as isize
            }
            IoKind::Seek => match lseek {
                Some(lseek) => lseek(request.fd, request.offset, request.whence) as isize,
                None => -1,
            },
            IoKind::Truncate => match ftruncate {
                Some(ftruncate) => ftruncate(request.fd, request.offset) as isize,
                None => -1,
            },
        };
        let success = if matches!(request.kind, IoKind::Truncate) {
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
    ptr::addr_of_mut!((*state).pending_io.error).write(error);
    ptr::addr_of_mut!((*state).pending_io.result).write(result);
    ptr::null_mut()
}

unsafe fn publish_pending_io() {
    let state = state_ptr();
    let mut pending = ptr::addr_of!((*state).pending_io).read();
    release_pending_resources(pending);
    if matches!(pending.kind, IoKind::Open) && pending.error == 0 {
        let next_handle = ptr::addr_of!((*state).next_handle).read();
        let malloc = ptr::addr_of!((*state).malloc).read();
        let close = ptr::addr_of!((*state).close).read();
        let next_token = next_handle.checked_add(1);
        let entry = malloc
            .map(|malloc| malloc(core::mem::size_of::<FileHandle>()).cast::<FileHandle>())
            .unwrap_or(ptr::null_mut());
        if next_token.is_none() || entry.is_null() {
            if let Some(close) = close {
                let _ = close(pending.result as i32);
            }
            if !entry.is_null() {
                if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
                    free(entry.cast());
                }
            }
            pending.error = if next_token.is_none() {
                ERROR_OVERFLOW
            } else {
                ERROR_NOMEM
            };
            pending.result = 0;
        } else {
            entry.write(FileHandle {
                token: next_handle,
                fd: pending.result as i32,
                next: ptr::addr_of!((*state).file_handles).read(),
            });
            ptr::addr_of_mut!((*state).file_handles).write(entry);
            if let Some(next_token) = next_token {
                ptr::addr_of_mut!((*state).next_handle).write(next_token);
            }
            pending.result = next_handle;
        }
    }
    complete_io(pending.overlapped, pending.error, pending.result);
    reset_pending_io(state);
}

unsafe fn join_and_publish_pending_io() -> bool {
    let state = state_ptr();
    let join = ptr::addr_of!((*state).pthread_join).read();
    let thread = ptr::addr_of!((*state).worker_thread).read();
    let Some(join) = join else {
        return false;
    };
    if thread == 0 || join(thread, ptr::null_mut()) != 0 {
        return false;
    }
    publish_pending_io();
    true
}

unsafe fn terminate_after_worker_failure(status: i32) -> ! {
    if let Some(exit) = state_mut().exit {
        exit(status);
    }
    loop {}
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
            if let Some(close) = ptr::addr_of!((*state).close).read() {
                let _ = close((*entry).fd);
            }
            if let Some(free) = ptr::addr_of!((*state).libc_free).read() {
                free(entry.cast());
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
    let pthread_create =
        function_pointer!(resolve(host, module, b"pthread_create\0"), PthreadCreateFn);
    let pthread_join = function_pointer!(resolve(host, module, b"pthread_join\0"), PthreadJoinFn);
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
        state.worker_thread = 0;
        state.queued_io = false;
        state.pending_io = PendingIo {
            overlapped: ptr::null_mut(),
            kind: IoKind::Read,
            fd: 0,
            buffer: ptr::null_mut(),
            length: 0,
            offset: 0,
            whence: 0,
            error: 0,
            result: 0,
        };
        state.free = Some(free);
        state.payload_alloc = payload_alloc;
        state.payload_total_size = total_payload_size;
    }
    #[cfg(target_arch = "x86_64")]
    let process_exit = probe_exit;
    #[cfg(target_arch = "aarch64")]
    let process_exit = exit;
    let mut abi_table = pal_abi_stubs::for_payload(probe_read, probe_write, process_exit);
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
        let queued_io = ptr::addr_of!((*state).queued_io).read();
        let thread = ptr::addr_of!((*state).worker_thread).read();
        if !queued_io {
            if thread != 0 {
                terminate_after_worker_failure(5);
            }
            close_all_file_handles();
            free(payload_alloc, total_payload_size);
            state_mut().payload_alloc = ptr::null_mut();
            return 0;
        }
        if !join_and_publish_pending_io() {
            // The host unloads this code when the entry returns; do not return while a worker may execute it.
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

    unsafe extern "C" fn fail_pthread_create(
        _thread: *mut usize,
        _attributes: *const c_void,
        _start: Option<unsafe extern "C" fn(*mut c_void) -> *mut c_void>,
        _context: *mut c_void,
    ) -> i32 {
        11
    }

    unsafe extern "C" fn zero_pthread_create(
        thread: *mut usize,
        _attributes: *const c_void,
        _start: Option<unsafe extern "C" fn(*mut c_void) -> *mut c_void>,
        _context: *mut c_void,
    ) -> i32 {
        thread.write(0);
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

    static mut TEST_PATH_BUFFER: [usize; 4] = [0; 4];

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
        assert!(size <= core::mem::size_of::<[usize; 4]>());
        ptr::addr_of_mut!(TEST_PATH_BUFFER).cast()
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
            worker_thread: 0,
            queued_io: false,
            pending_io: PendingIo {
                overlapped: ptr::null_mut(),
                kind: IoKind::Read,
                fd: 0,
                buffer: ptr::null_mut(),
                length: 0,
                offset: 0,
                whence: 0,
                error: 0,
                result: 0,
            },
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
        assert_eq!(size_of::<UnixPlatformHost>(), 64);
        assert_eq!(offset_of!(UnixPlatformHost, load_library), 0);
        assert_eq!(offset_of!(UnixPlatformHost, get_proc_address), 8);
        assert_eq!(offset_of!(UnixPlatformHost, alloc), 16);
        assert_eq!(offset_of!(UnixPlatformHost, free), 24);
        assert_eq!(offset_of!(UnixPlatformHost, flush_cache), 32);
        assert_eq!(offset_of!(UnixPlatformHost, set_jit_write_protect), 40);
        assert_eq!(offset_of!(UnixPlatformHost, hello_payload), 48);
        assert_eq!(offset_of!(UnixPlatformHost, hello_payload_size), 56);
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
    fn test_io_submission_worker_errors_and_completion_publication() {
        unsafe {
            reset_io_test_state();
            let state = state_ptr();
            let mut overlapped = Overlapped::default();
            let mut second_overlapped = Overlapped {
                flags: 99,
                error: 88,
                continued: 77,
                result_ext: 66,
            };
            let mut buffer = [0u8; 4];

            overlapped.continued = 17;
            overlapped.result_ext = 23;
            begin_io(&mut overlapped, 1, ptr::null_mut(), 0, true);
            assert_completion(&overlapped, 0, 0);

            begin_io(ptr::null_mut(), 1, ptr::null_mut(), 1, true);
            begin_io(&mut overlapped, 1, ptr::null_mut(), 1, true);
            assert_completion(&overlapped, ERROR_INVALID, 0);

            begin_io(&mut overlapped, 2, buffer.as_mut_ptr().cast(), 1, true);
            assert_completion(&overlapped, ERROR_IO, 0);
            begin_io(&mut overlapped, 1, buffer.as_mut_ptr().cast(), 1, false);
            assert_completion(&overlapped, ERROR_BADF, 0);

            begin_io(&mut overlapped, 1, buffer.as_mut_ptr().cast(), 1, true);
            assert_completion(&overlapped, ERROR_IO, 0);

            (*state).pthread_create = Some(fail_pthread_create);
            begin_io(&mut overlapped, 1, buffer.as_mut_ptr().cast(), 1, true);
            assert_completion(&overlapped, ERROR_AGAIN, 0);
            assert!(!(*state).queued_io);

            (*state).pthread_create = Some(zero_pthread_create);
            begin_io(&mut overlapped, 1, buffer.as_mut_ptr().cast(), 1, true);
            assert_completion(&overlapped, ERROR_AGAIN, 0);
            assert!(!(*state).queued_io);

            (*state).pthread_create = Some(mock_pthread_create);
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
            assert!((*state).queued_io);
            assert_eq!(overlapped.flags, 0);
            assert_eq!(overlapped.error, 0);
            assert_eq!(overlapped.continued, 0);
            assert_eq!(overlapped.result_ext, 0);

            begin_io(
                &mut second_overlapped,
                1,
                buffer.as_mut_ptr().cast(),
                1,
                true,
            );
            assert_completion(&second_overlapped, ERROR_AGAIN, 0);
            assert!(ptr::addr_of!((*state).queued_io).read());
            assert_eq!(
                ptr::addr_of!((*state).pending_io.overlapped).read(),
                ptr::addr_of_mut!(overlapped)
            );

            run_io_worker(ptr::null_mut());
            assert_eq!(ptr::addr_of!((*state).pending_io.result).read(), 2);
            assert_eq!(overlapped.flags, 0);
            assert!(join_and_publish_pending_io());
            assert_completion(&overlapped, 0, 2);
            assert!(!ptr::addr_of!((*state).queued_io).read());
            assert_eq!(ptr::addr_of!((*state).worker_thread).read(), 0);
            assert!(ptr::addr_of!((*state).pending_io.overlapped)
                .read()
                .is_null());
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_length, 4);

            second_overlapped = Overlapped {
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
            assert!((*state).queued_io);
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
            let mut failed_join = Overlapped::default();
            let mut byte = 0u8;
            begin_io(&mut failed_join, 0, (&mut byte as *mut u8).cast(), 1, false);
            run_io_worker(ptr::null_mut());
            (*ptr::addr_of_mut!(TEST_IO)).join_result = 11;
            assert!(!join_and_publish_pending_io());
            assert_eq!(failed_join.flags, 0);
            assert!(ptr::addr_of!((*state).queued_io).read());
            assert_eq!(
                ptr::addr_of!((*state).pending_io.overlapped).read(),
                ptr::addr_of_mut!(failed_join)
            );
            assert_eq!((*ptr::addr_of!(TEST_IO)).join_calls, 1);

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
            assert!((*state).queued_io);
            assert_eq!(
                ptr::addr_of!((*state).pending_io.fd).read(),
                2 | NATIVE_O_CREATE | NATIVE_O_APPEND | NATIVE_O_DIRECTORY
            );
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&file_ov, 0, 3);
            assert_eq!((*ptr::addr_of!(TEST_IO)).open_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).open_mode, 0o666);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 1);

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
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 2);
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
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 4);

            (*ptr::addr_of_mut!(TEST_IO)).open_errno = ERRNO_ENOENT;
            let mut failed_open = Overlapped::default();
            begin_path_open(&mut failed_open, b"file.txt".as_ptr(), 8, 0);
            run_io_worker(ptr::null_mut());
            assert!(join_and_publish_pending_io());
            assert_completion(&failed_open, 44, 0);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 5);

            let mut invalid_path = Overlapped::default();
            begin_path_open(&mut invalid_path, b"bad\0path".as_ptr(), 8, 0);
            assert_completion(&invalid_path, ERROR_INVALID, 0);
            assert_eq!((*ptr::addr_of!(TEST_IO)).free_calls, 6);

            assert_eq!(map_io_errno(ERRNO_EACCES), ERROR_ACCES);
            assert_eq!(map_io_errno(ERRNO_EEXIST), ERROR_EXIST);
            assert_eq!(map_io_errno(ERRNO_EISDIR), ERROR_ISDIR);
            assert_eq!(map_io_errno(ERRNO_ENOTDIR), ERROR_NOTDIR);
            assert_eq!(map_io_errno(ERRNO_EPERM), ERROR_PERM);
        }
    }
}
