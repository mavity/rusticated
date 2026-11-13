#![cfg_attr(not(test), no_std)]

use core::ffi::c_void;
#[cfg(not(test))]
use core::panic::PanicInfo;
use core::ptr;
use core::sync::atomic::{AtomicU32, Ordering};

#[path = "../abi/guest.rs"]
mod pal_abi;
#[path = "../abi/guest_stubs.rs"]
mod pal_abi_stubs;
use pal_abi::{
    pal_fn, Overlapped, PalAbiTable, ERROR_AGAIN, ERROR_BADF, ERROR_INVALID, ERROR_IO, ERROR_PIPE,
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
compile_error!("The Windows platform abstraction supports only x86-64 and AArch64.");

#[cfg(target_arch = "x86_64")]
type LoadLibraryFn = unsafe extern "win64" fn(*const u8) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type LoadLibraryFn = unsafe extern "C" fn(*const u8) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type GetProcAddressFn = unsafe extern "win64" fn(*mut c_void, *const u8) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type GetProcAddressFn = unsafe extern "C" fn(*mut c_void, *const u8) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type AllocFn = unsafe extern "win64" fn(usize) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type AllocFn = unsafe extern "C" fn(usize) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type FreeFn = unsafe extern "win64" fn(*mut c_void);
#[cfg(target_arch = "aarch64")]
type FreeFn = unsafe extern "C" fn(*mut c_void);

#[repr(C)]
#[derive(Clone, Copy)]
pub struct WindowsPlatformHost {
    load_library: Option<LoadLibraryFn>,
    get_proc_address: Option<GetProcAddressFn>,
    alloc: Option<AllocFn>,
    free: Option<FreeFn>,
    hello_payload: *const u8,
    hello_payload_size: usize,
    argv: *const *const u8,
    argc: usize,
}

#[cfg(target_arch = "x86_64")]
type GetStdHandleFn = unsafe extern "win64" fn(u32) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type GetStdHandleFn = unsafe extern "C" fn(u32) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type ReadFileFn =
    unsafe extern "win64" fn(*mut c_void, *mut c_void, u32, *mut u32, *mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type ReadFileFn = unsafe extern "C" fn(*mut c_void, *mut c_void, u32, *mut u32, *mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type WriteFileFn =
    unsafe extern "win64" fn(*mut c_void, *const c_void, u32, *mut u32, *mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type WriteFileFn =
    unsafe extern "C" fn(*mut c_void, *const c_void, u32, *mut u32, *mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type GetCurrentProcessFn = unsafe extern "win64" fn() -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type GetCurrentProcessFn = unsafe extern "C" fn() -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type GetLastErrorFn = unsafe extern "win64" fn() -> u32;
#[cfg(target_arch = "aarch64")]
type GetLastErrorFn = unsafe extern "C" fn() -> u32;

#[cfg(target_arch = "x86_64")]
type FlushInstructionCacheFn = unsafe extern "win64" fn(*mut c_void, *const c_void, usize) -> i32;
#[cfg(target_arch = "aarch64")]
type FlushInstructionCacheFn = unsafe extern "C" fn(*mut c_void, *const c_void, usize) -> i32;

type ExitProcessFn = pal_fn!(fn(code: i32) -> !);

#[cfg(target_arch = "x86_64")]
type CreateThreadFn = unsafe extern "win64" fn(
    *mut c_void,
    usize,
    Option<unsafe extern "win64" fn(*mut c_void) -> u32>,
    *mut c_void,
    u32,
    *mut u32,
) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type CreateThreadFn = unsafe extern "C" fn(
    *mut c_void,
    usize,
    Option<unsafe extern "C" fn(*mut c_void) -> u32>,
    *mut c_void,
    u32,
    *mut u32,
) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type WaitForSingleObjectFn = unsafe extern "win64" fn(*mut c_void, u32) -> u32;
#[cfg(target_arch = "aarch64")]
type WaitForSingleObjectFn = unsafe extern "C" fn(*mut c_void, u32) -> u32;

#[cfg(target_arch = "x86_64")]
type CloseHandleFn = unsafe extern "win64" fn(*mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type CloseHandleFn = unsafe extern "C" fn(*mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type CreateIoCompletionPortFn =
    unsafe extern "win64" fn(*mut c_void, *mut c_void, usize, u32) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type CreateIoCompletionPortFn =
    unsafe extern "C" fn(*mut c_void, *mut c_void, usize, u32) -> *mut c_void;

#[repr(C)]
#[derive(Clone, Copy)]
struct OverlappedEntry {
    completion_key: usize,
    overlapped: *mut c_void,
    internal: usize,
    bytes_transferred: u32,
}

#[cfg(target_arch = "x86_64")]
type GetQueuedCompletionStatusExFn =
    unsafe extern "win64" fn(*mut c_void, *mut OverlappedEntry, u32, *mut u32, u32, i32) -> i32;
#[cfg(target_arch = "aarch64")]
type GetQueuedCompletionStatusExFn =
    unsafe extern "C" fn(*mut c_void, *mut OverlappedEntry, u32, *mut u32, u32, i32) -> i32;

#[cfg(target_arch = "x86_64")]
type PostQueuedCompletionStatusFn =
    unsafe extern "win64" fn(*mut c_void, u32, usize, *mut c_void) -> i32;
#[cfg(target_arch = "aarch64")]
type PostQueuedCompletionStatusFn =
    unsafe extern "C" fn(*mut c_void, u32, usize, *mut c_void) -> i32;

#[cfg(target_arch = "x86_64")]
type CreateFileWFn = unsafe extern "win64" fn(
    *const u16,
    u32,
    u32,
    *mut c_void,
    u32,
    u32,
    *mut c_void,
) -> *mut c_void;
#[cfg(target_arch = "aarch64")]
type CreateFileWFn =
    unsafe extern "C" fn(*const u16, u32, u32, *mut c_void, u32, u32, *mut c_void) -> *mut c_void;

#[cfg(target_arch = "x86_64")]
type MultiByteToWideCharFn =
    unsafe extern "win64" fn(u32, u32, *const u8, i32, *mut u16, i32) -> i32;
#[cfg(target_arch = "aarch64")]
type MultiByteToWideCharFn = unsafe extern "C" fn(u32, u32, *const u8, i32, *mut u16, i32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetOverlappedResultFn =
    unsafe extern "win64" fn(*mut c_void, *mut c_void, *mut u32, i32) -> i32;
#[cfg(target_arch = "aarch64")]
type GetOverlappedResultFn = unsafe extern "C" fn(*mut c_void, *mut c_void, *mut u32, i32) -> i32;

#[cfg(target_arch = "x86_64")]
type GetFileSizeExFn = unsafe extern "win64" fn(*mut c_void, *mut i64) -> i32;
#[cfg(target_arch = "aarch64")]
type GetFileSizeExFn = unsafe extern "C" fn(*mut c_void, *mut i64) -> i32;

#[cfg(target_arch = "x86_64")]
type GetFileInformationByHandleExFn =
    unsafe extern "win64" fn(*mut c_void, u32, *mut c_void, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type GetFileInformationByHandleExFn =
    unsafe extern "C" fn(*mut c_void, u32, *mut c_void, u32) -> i32;

#[cfg(target_arch = "x86_64")]
type SetFileInformationByHandleFn =
    unsafe extern "win64" fn(*mut c_void, u32, *const c_void, u32) -> i32;
#[cfg(target_arch = "aarch64")]
type SetFileInformationByHandleFn =
    unsafe extern "C" fn(*mut c_void, u32, *const c_void, u32) -> i32;

#[repr(C)]
#[derive(Clone, Copy)]
struct NativeOverlapped {
    internal: usize,
    internal_high: usize,
    offset: u32,
    offset_high: u32,
    event: *mut c_void,
}

#[repr(u8)]
#[derive(Clone, Copy, PartialEq, Eq)]
enum IoKind {
    None,
    Stdio,
    FileRead,
    FileWrite,
    FileSeek,
    FileTruncate,
}

#[repr(C)]
struct IoOperation {
    native_overlapped: NativeOverlapped,
    overlapped: *mut Overlapped,
    handle: *mut c_void,
    buffer: *mut u8,
    length: u32,
    is_write: bool,
    kind: IoKind,
    token: u64,
    file_offset: i64,
    seek_offset: i64,
    whence: u32,
    positional: bool,
    immediate: bool,
    error: u32,
    result: u64,
    worker_completion_ready: AtomicU32,
    worker_thread: *mut c_void,
    file: *mut FileHandle,
    next: *mut IoOperation,
    next_for_file: *mut IoOperation,
}

#[repr(C)]
struct FileHandle {
    token: u64,
    native_handle: *mut c_void,
    cursor: i64,
    access: u32,
    append: bool,
    next: *mut FileHandle,
    closing: bool,
    pending_operations: usize,
    sequential_head: *mut IoOperation,
    sequential_tail: *mut IoOperation,
    sequential_active: bool,
}

#[repr(C)]
struct PlatformState {
    input_handle: *mut c_void,
    output_handle: *mut c_void,
    error_handle: *mut c_void,
    alloc: Option<AllocFn>,
    free: Option<FreeFn>,
    read_file: Option<ReadFileFn>,
    write_file: Option<WriteFileFn>,
    get_last_error: Option<GetLastErrorFn>,
    exit_process: Option<ExitProcessFn>,
    create_thread: Option<CreateThreadFn>,
    wait_for_single_object: Option<WaitForSingleObjectFn>,
    close_handle: Option<CloseHandleFn>,
    completion_port: *mut c_void,
    get_queued_completion_status_ex: Option<GetQueuedCompletionStatusExFn>,
    post_queued_completion_status: Option<PostQueuedCompletionStatusFn>,
    create_io_completion_port: Option<CreateIoCompletionPortFn>,
    create_file_w: Option<CreateFileWFn>,
    multi_byte_to_wide_char: Option<MultiByteToWideCharFn>,
    get_overlapped_result: Option<GetOverlappedResultFn>,
    get_file_size_ex: Option<GetFileSizeExFn>,
    get_file_information_by_handle_ex: Option<GetFileInformationByHandleExFn>,
    set_file_information_by_handle: Option<SetFileInformationByHandleFn>,
    file_handles: *mut FileHandle,
    next_file_token: u64,
    io_operations: *mut IoOperation,
}

#[cfg(test)]
static mut TEST_STATE: PlatformState = PlatformState {
    input_handle: ptr::null_mut(),
    output_handle: ptr::null_mut(),
    error_handle: ptr::null_mut(),
    alloc: None,
    free: None,
    read_file: None,
    write_file: None,
    get_last_error: None,
    exit_process: None,
    create_thread: None,
    wait_for_single_object: None,
    close_handle: None,
    completion_port: ptr::null_mut(),
    get_queued_completion_status_ex: None,
    post_queued_completion_status: None,
    create_io_completion_port: None,
    create_file_w: None,
    multi_byte_to_wide_char: None,
    get_overlapped_result: None,
    get_file_size_ex: None,
    get_file_information_by_handle_ex: None,
    set_file_information_by_handle: None,
    file_handles: ptr::null_mut(),
    next_file_token: 3,
    io_operations: ptr::null_mut(),
};

const PREFIX_ABI_OFFSET: usize = 0x20000;
const STD_INPUT_HANDLE: u32 = (-10i32) as u32;
const STD_OUTPUT_HANDLE: u32 = (-11i32) as u32;
const STD_ERROR_HANDLE: u32 = (-12i32) as u32;
const ERROR_BROKEN_PIPE: u32 = 109;
const ERROR_ACCESS_DENIED_CODE: u32 = 5;
const ERROR_FILE_NOT_FOUND_CODE: u32 = 2;
const ERROR_PATH_NOT_FOUND_CODE: u32 = 3;
const ERROR_FILE_EXISTS_CODE: u32 = 80;
const ERROR_INVALID_HANDLE_CODE: u32 = 6;
const ERROR_HANDLE_EOF_CODE: u32 = 38;
const ERROR_INVALID_PARAMETER_CODE: u32 = 87;
const ERROR_IO_PENDING_CODE: u32 = 997;
const ERROR_NO_DATA: u32 = 232;
const ERROR_ALREADY_EXISTS_CODE: u32 = 183;
const ERROR_DIRECTORY_CODE: u32 = 267;
const ERROR_NOT_ENOUGH_MEMORY_CODE: u32 = 8;
const ERROR_NO_UNICODE_TRANSLATION_CODE: u32 = 1113;
const ERROR_OPERATION_ABORTED_CODE: u32 = 995;
const WAIT_OBJECT_0: u32 = 0;
const INFINITE: u32 = u32::MAX;
const STDIO_COMPLETION_KEY: usize = 0x535444494f;
const IOCP_BATCH_SIZE: usize = 64;
const CP_UTF8: u32 = 65001;
const MB_ERR_INVALID_CHARS: u32 = 8;
const GENERIC_READ: u32 = 0x8000_0000;
const GENERIC_WRITE: u32 = 0x4000_0000;
const FILE_APPEND_DATA: u32 = 0x0000_0004;
const FILE_SHARE_ALL: u32 = 0x0000_0007;
const CREATE_NEW: u32 = 1;
const CREATE_ALWAYS: u32 = 2;
const OPEN_EXISTING: u32 = 3;
const OPEN_ALWAYS: u32 = 4;
const TRUNCATE_EXISTING: u32 = 5;
const FILE_ATTRIBUTE_NORMAL: u32 = 0x0000_0080;
const FILE_FLAG_OVERLAPPED: u32 = 0x4000_0000;
const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
const FILE_STANDARD_INFO_CLASS: u32 = 1;
const FILE_END_OF_FILE_INFO_CLASS: u32 = 6;
const ABI_O_CREATE: u32 = 0x40;
const ABI_O_EXCL: u32 = 0x80;
const ABI_O_TRUNC: u32 = 0x200;
const ABI_O_APPEND: u32 = 0x400;
const ABI_O_DIRECTORY: u32 = 0x2000;

#[inline(always)]
unsafe fn slice_origin() -> *mut u8 {
    let addr: usize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "lea {0}, [rip + windows_platform_entry]",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "adr {0}, windows_platform_entry",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    addr as *mut u8
}

#[inline(always)]
unsafe fn host_abi() -> &'static WindowsPlatformHost {
    &*(slice_origin().sub(0x20000) as *const WindowsPlatformHost)
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

unsafe fn resolve(
    host: &WindowsPlatformHost,
    module: *mut c_void,
    name: &'static [u8],
) -> *mut c_void {
    match host.get_proc_address {
        Some(get_proc_address) => get_proc_address(module, name.as_ptr()),
        None => ptr::null_mut(),
    }
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

fn empty_io_operation() -> IoOperation {
    IoOperation {
        native_overlapped: NativeOverlapped {
            internal: 0,
            internal_high: 0,
            offset: 0,
            offset_high: 0,
            event: ptr::null_mut(),
        },
        overlapped: ptr::null_mut(),
        handle: ptr::null_mut(),
        buffer: ptr::null_mut(),
        length: 0,
        is_write: false,
        kind: IoKind::None,
        token: 0,
        file_offset: 0,
        seek_offset: 0,
        whence: 0,
        positional: false,
        immediate: false,
        error: 0,
        result: 0,
        worker_completion_ready: AtomicU32::new(0),
        worker_thread: ptr::null_mut(),
        file: ptr::null_mut(),
        next: ptr::null_mut(),
        next_for_file: ptr::null_mut(),
    }
}

unsafe fn find_file_handle(token: u64) -> *mut FileHandle {
    let mut handle = ptr::addr_of!((*state_ptr()).file_handles).read();
    while !handle.is_null() {
        if (*handle).token == token && !(*handle).closing {
            return handle;
        }
        handle = (*handle).next;
    }
    ptr::null_mut()
}

unsafe fn find_io_operation(native_overlapped: *mut c_void) -> *mut IoOperation {
    let mut operation = ptr::addr_of!((*state_ptr()).io_operations).read();
    while !operation.is_null() {
        if ptr::addr_of_mut!((*operation).native_overlapped).cast() == native_overlapped {
            return operation;
        }
        operation = (*operation).next;
    }
    ptr::null_mut()
}

unsafe fn allocate_io_operation() -> *mut IoOperation {
    let state = state_ptr();
    let (Some(alloc), Some(free)) = (
        ptr::addr_of!((*state).alloc).read(),
        ptr::addr_of!((*state).free).read(),
    ) else {
        return ptr::null_mut();
    };
    let operation = alloc(core::mem::size_of::<IoOperation>()).cast::<IoOperation>();
    if operation.is_null() {
        return ptr::null_mut();
    }
    operation.write(empty_io_operation());
    if ptr::addr_of!((*state).free).read().is_none() {
        free(operation.cast());
        return ptr::null_mut();
    }
    operation
}

unsafe fn link_io_operation(operation: *mut IoOperation) {
    let state = state_ptr();
    (*operation).next = ptr::addr_of!((*state).io_operations).read();
    ptr::addr_of_mut!((*state).io_operations).write(operation);
}

fn map_windows_error(error: u32) -> u32 {
    match error {
        ERROR_FILE_NOT_FOUND_CODE | ERROR_PATH_NOT_FOUND_CODE => pal_abi::ERROR_NOENT,
        ERROR_ACCESS_DENIED_CODE => pal_abi::ERROR_ACCES,
        ERROR_INVALID_HANDLE_CODE => ERROR_BADF,
        ERROR_FILE_EXISTS_CODE | ERROR_ALREADY_EXISTS_CODE => pal_abi::ERROR_EXIST,
        ERROR_INVALID_PARAMETER_CODE => ERROR_INVALID,
        ERROR_NO_UNICODE_TRANSLATION_CODE => ERROR_INVALID,
        123 => ERROR_INVALID,
        ERROR_DIRECTORY_CODE => pal_abi::ERROR_NOTDIR,
        ERROR_NOT_ENOUGH_MEMORY_CODE => pal_abi::ERROR_NOMEM,
        ERROR_OPERATION_ABORTED_CODE => pal_abi::ERROR_CANCELED,
        ERROR_BROKEN_PIPE | ERROR_NO_DATA => ERROR_PIPE,
        _ => ERROR_IO,
    }
}

unsafe fn unlink_io_operation(operation: *mut IoOperation) {
    let state = state_ptr();
    let mut link = ptr::addr_of_mut!((*state).io_operations);
    while !(*link).is_null() {
        if *link == operation {
            link.write((*operation).next);
            return;
        }
        link = ptr::addr_of_mut!((*(*link)).next);
    }
    exit_process_impl(18);
}

unsafe fn free_io_operation(operation: *mut IoOperation) {
    unlink_io_operation(operation);
    if let Some(free) = ptr::addr_of!((*state_ptr()).free).read() {
        free(operation.cast());
    }
}

unsafe fn close_file_record(file: *mut FileHandle) {
    let state = state_ptr();
    if let Some(close) = ptr::addr_of!((*state).close_handle).read() {
        let _ = close((*file).native_handle);
    }
    if let Some(free) = ptr::addr_of!((*state).free).read() {
        free(file.cast());
    }
}

unsafe fn release_file_reference(file: *mut FileHandle) {
    if file.is_null() || (*file).pending_operations == 0 {
        exit_process_impl(18);
    }
    (*file).pending_operations -= 1;
    if (*file).closing && (*file).pending_operations == 0 {
        close_file_record(file);
    }
}

unsafe fn post_io_completion(operation: *mut IoOperation, key: usize) {
    let state = state_ptr();
    let port = ptr::addr_of!((*state).completion_port).read();
    let Some(post) = ptr::addr_of!((*state).post_queued_completion_status).read() else {
        exit_process_impl(16);
    };
    if port.is_null() {
        exit_process_impl(16);
    }
    if post(
        port,
        0,
        key,
        ptr::addr_of_mut!((*operation).native_overlapped).cast(),
    ) == 0
    {
        exit_process_impl(16);
    }
}

unsafe fn dispatch_file_operation(operation: *mut IoOperation) {
    let state = state_ptr();
    let file = (*operation).file;
    if file.is_null() || (*file).closing && (*file).pending_operations == 0 {
        exit_process_impl(18);
    }
    if !(*operation).positional {
        (*operation).file_offset = (*file).cursor;
    }

    match (*operation).kind {
        IoKind::FileSeek => {
            (*operation).immediate = true;
            let base = match (*operation).whence {
                0 => 0,
                1 => (*file).cursor,
                2 => {
                    let Some(get_size) = ptr::addr_of!((*state).get_file_size_ex).read() else {
                        (*operation).error = ERROR_IO;
                        post_io_completion(operation, (*operation).token as usize);
                        return;
                    };
                    let mut size = 0i64;
                    if get_size((*file).native_handle, &mut size) == 0 {
                        (*operation).error = ptr::addr_of!((*state).get_last_error)
                            .read()
                            .map(|get_last_error| map_windows_error(get_last_error()))
                            .unwrap_or(ERROR_IO);
                        post_io_completion(operation, (*operation).token as usize);
                        return;
                    }
                    size
                }
                _ => {
                    (*operation).error = ERROR_INVALID;
                    post_io_completion(operation, (*operation).token as usize);
                    return;
                }
            };
            match base.checked_add((*operation).seek_offset) {
                Some(position) if position >= 0 => (*operation).result = position as u64,
                Some(_) => (*operation).error = ERROR_INVALID,
                None => (*operation).error = pal_abi::ERROR_OVERFLOW,
            }
            post_io_completion(operation, (*operation).token as usize);
            return;
        }
        IoKind::FileTruncate => {
            (*operation).immediate = true;
            let info = FileEndOfFileInfo {
                end_of_file: (*operation).seek_offset,
            };
            let success = ptr::addr_of!((*state).set_file_information_by_handle)
                .read()
                .map(|set_info| {
                    set_info(
                        (*file).native_handle,
                        FILE_END_OF_FILE_INFO_CLASS,
                        (&info as *const FileEndOfFileInfo).cast(),
                        core::mem::size_of::<FileEndOfFileInfo>() as u32,
                    ) != 0
                })
                .unwrap_or(false);
            if !success {
                (*operation).error = ptr::addr_of!((*state).get_last_error)
                    .read()
                    .map(|get_last_error| map_windows_error(get_last_error()))
                    .unwrap_or(ERROR_IO);
            }
            post_io_completion(operation, (*operation).token as usize);
            return;
        }
        IoKind::FileRead | IoKind::FileWrite => {}
        _ => exit_process_impl(18),
    }

    let append = (*operation).kind == IoKind::FileWrite && (*file).append;
    (*operation).native_overlapped = NativeOverlapped {
        internal: 0,
        internal_high: 0,
        offset: if append {
            u32::MAX
        } else {
            (*operation).file_offset as u32
        },
        offset_high: if append {
            u32::MAX
        } else {
            ((*operation).file_offset as u64 >> 32) as u32
        },
        event: ptr::null_mut(),
    };
    let mut transferred = 0u32;
    let success = if (*operation).kind == IoKind::FileWrite {
        ptr::addr_of!((*state).write_file)
            .read()
            .map(|write_file| {
                write_file(
                    (*operation).handle,
                    (*operation).buffer.cast(),
                    (*operation).length,
                    &mut transferred,
                    ptr::addr_of_mut!((*operation).native_overlapped).cast(),
                ) != 0
            })
            .unwrap_or(false)
    } else {
        ptr::addr_of!((*state).read_file)
            .read()
            .map(|read_file| {
                read_file(
                    (*operation).handle,
                    (*operation).buffer.cast(),
                    (*operation).length,
                    &mut transferred,
                    ptr::addr_of_mut!((*operation).native_overlapped).cast(),
                ) != 0
            })
            .unwrap_or(false)
    };
    if !success {
        let error = ptr::addr_of!((*state).get_last_error)
            .read()
            .map(|get_last_error| get_last_error())
            .unwrap_or(0);
        if error != ERROR_IO_PENDING_CODE {
            (*operation).error = map_windows_error(error);
            (*operation).immediate = true;
            post_io_completion(operation, (*operation).token as usize);
        }
    }
}

unsafe fn enqueue_file_operation(operation: *mut IoOperation) {
    let file = (*operation).file;
    if (*operation).positional {
        dispatch_file_operation(operation);
        return;
    }
    (*operation).next_for_file = ptr::null_mut();
    let tail = (*file).sequential_tail;
    if tail.is_null() {
        ptr::addr_of_mut!((*file).sequential_head).write(operation);
    } else {
        ptr::addr_of_mut!((*tail).next_for_file).write(operation);
    }
    ptr::addr_of_mut!((*file).sequential_tail).write(operation);
    if !(*file).sequential_active {
        (*file).sequential_active = true;
        dispatch_file_operation(operation);
    }
}

unsafe fn begin_io(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *mut u8,
    length: usize,
    is_write: bool,
) {
    if ov.is_null() || (buffer.is_null() && length != 0) || length > u32::MAX as usize {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    if handle > 2 {
        begin_file_io(ov, handle, buffer, length, is_write);
        return;
    }
    let state = state_ptr();
    let native_handle = match (handle, is_write) {
        (0, false) => ptr::addr_of!((*state).input_handle).read(),
        (1, true) => ptr::addr_of!((*state).output_handle).read(),
        _ => ptr::null_mut(),
    };
    if native_handle.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    if length == 0 {
        complete_io(ov, 0, 0);
        return;
    }
    let Some(create_thread) = ptr::addr_of!((*state).create_thread).read() else {
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    let operation = allocate_io_operation();
    if operation.is_null() {
        complete_io(ov, pal_abi::ERROR_NOMEM, 0);
        return;
    }
    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;
    (*operation).overlapped = ov;
    (*operation).handle = native_handle;
    (*operation).buffer = buffer;
    (*operation).length = length as u32;
    (*operation).is_write = is_write;
    (*operation).kind = IoKind::Stdio;
    link_io_operation(operation);
    #[cfg(target_arch = "x86_64")]
    let start = run_io_worker as unsafe extern "win64" fn(*mut c_void) -> u32;
    #[cfg(target_arch = "aarch64")]
    let start = run_io_worker as unsafe extern "C" fn(*mut c_void) -> u32;
    let thread = create_thread(
        ptr::null_mut(),
        0,
        Some(start),
        operation.cast(),
        0,
        ptr::null_mut(),
    );
    if thread.is_null() {
        free_io_operation(operation);
        complete_io(ov, ERROR_AGAIN, 0);
    } else {
        (*operation).worker_thread = thread;
    }
}

unsafe fn begin_file_io(
    ov: *mut Overlapped,
    token: u64,
    buffer: *mut u8,
    length: usize,
    is_write: bool,
) {
    if ov.is_null() || (buffer.is_null() && length != 0) || length > u32::MAX as usize {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file = find_file_handle(token);
    if file.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    let access = ptr::addr_of!((*file).access).read();
    if (is_write && access & 2 == 0) || (!is_write && access & 1 == 0) {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    if length == 0 {
        complete_io(ov, 0, 0);
        return;
    }
    let positional = !is_write && (*ov).flags & pal_abi::READ_FLAG_POSITIONAL != 0;
    let file_offset = if positional {
        let offset = ptr::read_unaligned(
            ov.cast::<u8>()
                .add(core::mem::size_of::<Overlapped>())
                .cast::<i64>(),
        );
        if offset < 0 {
            complete_io(ov, ERROR_INVALID, 0);
            return;
        }
        offset
    } else {
        (*file).cursor
    };
    if file_offset < 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    if (*file).pending_operations == usize::MAX {
        complete_io(ov, pal_abi::ERROR_OVERFLOW, 0);
        return;
    }
    let operation = allocate_io_operation();
    if operation.is_null() {
        complete_io(ov, pal_abi::ERROR_NOMEM, 0);
        return;
    }
    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;
    (*operation).overlapped = ov;
    (*operation).handle = ptr::addr_of!((*file).native_handle).read();
    (*operation).buffer = buffer;
    (*operation).length = length as u32;
    (*operation).is_write = is_write;
    (*operation).kind = if is_write {
        IoKind::FileWrite
    } else {
        IoKind::FileRead
    };
    (*operation).token = token;
    (*operation).file_offset = file_offset;
    (*operation).positional = positional;
    (*operation).file = file;
    (*file).pending_operations += 1;
    link_io_operation(operation);
    enqueue_file_operation(operation);
}

#[repr(C)]
struct FileStandardInfo {
    allocation_size: i64,
    end_of_file: i64,
    number_of_links: u32,
    delete_pending: u8,
    directory: u8,
}

unsafe fn probe_path_open(ov: *mut Overlapped, path: *const u8, path_length: usize, flags: u32) {
    if ov.is_null() || path.is_null() || path_length > i32::MAX as usize {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    if path_length == 0 {
        complete_io(ov, pal_abi::ERROR_NOENT, 0);
        return;
    }
    let access_mode = flags & 3;
    let supported = 3 | ABI_O_CREATE | ABI_O_EXCL | ABI_O_TRUNC | ABI_O_APPEND | ABI_O_DIRECTORY;
    if access_mode == 3
        || flags & !supported != 0
        || (flags & ABI_O_EXCL != 0 && flags & ABI_O_CREATE == 0)
    {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let access = match access_mode {
        0 => 1,
        1 => 2,
        _ => 3,
    };
    if (flags & (ABI_O_TRUNC | ABI_O_APPEND) != 0) && access & 2 == 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let state = state_ptr();
    let (Some(alloc), Some(free), Some(convert), Some(create_file), Some(create_port)) = (
        ptr::addr_of!((*state).alloc).read(),
        ptr::addr_of!((*state).free).read(),
        ptr::addr_of!((*state).multi_byte_to_wide_char).read(),
        ptr::addr_of!((*state).create_file_w).read(),
        ptr::addr_of!((*state).create_io_completion_port).read(),
    ) else {
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    let mut byte_index = 0usize;
    while byte_index < path_length {
        if path.add(byte_index).read() == 0 {
            complete_io(ov, ERROR_INVALID, 0);
            return;
        }
        byte_index += 1;
    }
    let wide_length = convert(
        CP_UTF8,
        MB_ERR_INVALID_CHARS,
        path,
        path_length as i32,
        ptr::null_mut(),
        0,
    );
    if wide_length <= 0 {
        let error = ptr::addr_of!((*state).get_last_error)
            .read()
            .map(|get_last_error| get_last_error())
            .unwrap_or(ERROR_INVALID_PARAMETER_CODE);
        complete_io(ov, map_windows_error(error), 0);
        return;
    }
    let Some(wide_capacity) = (wide_length as usize).checked_add(1) else {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    };
    let Some(wide_bytes) = wide_capacity.checked_mul(core::mem::size_of::<u16>()) else {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    };
    let wide_path = alloc(wide_bytes).cast::<u16>();
    if wide_path.is_null() {
        complete_io(ov, pal_abi::ERROR_NOMEM, 0);
        return;
    }
    let converted = convert(
        CP_UTF8,
        MB_ERR_INVALID_CHARS,
        path,
        path_length as i32,
        wide_path,
        wide_length,
    );
    if converted != wide_length {
        free(wide_path.cast());
        let error = ptr::addr_of!((*state).get_last_error)
            .read()
            .map(|get_last_error| get_last_error())
            .unwrap_or(ERROR_INVALID_PARAMETER_CODE);
        complete_io(ov, map_windows_error(error), 0);
        return;
    }
    wide_path.add(wide_length as usize).write(0);

    let mut desired_access = 0u32;
    if access & 1 != 0 {
        desired_access |= GENERIC_READ;
    }
    if access & 2 != 0 {
        desired_access |= GENERIC_WRITE;
        if flags & ABI_O_APPEND != 0 {
            desired_access |= FILE_APPEND_DATA;
        }
    }
    let disposition = if flags & ABI_O_CREATE != 0 && flags & ABI_O_EXCL != 0 {
        CREATE_NEW
    } else if flags & ABI_O_CREATE != 0 && flags & ABI_O_TRUNC != 0 {
        CREATE_ALWAYS
    } else if flags & ABI_O_CREATE != 0 {
        OPEN_ALWAYS
    } else if flags & ABI_O_TRUNC != 0 {
        TRUNCATE_EXISTING
    } else {
        OPEN_EXISTING
    };
    let native_handle = create_file(
        wide_path,
        desired_access,
        FILE_SHARE_ALL,
        ptr::null_mut(),
        disposition,
        FILE_ATTRIBUTE_NORMAL | FILE_FLAG_OVERLAPPED | FILE_FLAG_BACKUP_SEMANTICS,
        ptr::null_mut(),
    );
    free(wide_path.cast());
    if native_handle as isize == -1 {
        let error = ptr::addr_of!((*state).get_last_error)
            .read()
            .map(|get_last_error| get_last_error())
            .unwrap_or(0);
        complete_io(ov, map_windows_error(error), 0);
        return;
    }

    let next_token = ptr::addr_of!((*state).next_file_token).read();
    let Some(after_token) = next_token.checked_add(1) else {
        ptr::addr_of!((*state).close_handle)
            .read()
            .map(|close| close(native_handle));
        complete_io(ov, pal_abi::ERROR_OVERFLOW, 0);
        return;
    };
    let file = alloc(core::mem::size_of::<FileHandle>()).cast::<FileHandle>();
    if file.is_null() {
        ptr::addr_of!((*state).close_handle)
            .read()
            .map(|close| close(native_handle));
        complete_io(ov, pal_abi::ERROR_NOMEM, 0);
        return;
    }
    let port = ptr::addr_of!((*state).completion_port).read();
    if port.is_null() || create_port(native_handle, port, next_token as usize, 0) != port {
        free(file.cast());
        ptr::addr_of!((*state).close_handle)
            .read()
            .map(|close| close(native_handle));
        let error = ptr::addr_of!((*state).get_last_error)
            .read()
            .map(|get_last_error| get_last_error())
            .unwrap_or(0);
        complete_io(ov, map_windows_error(error), 0);
        return;
    }
    if flags & ABI_O_DIRECTORY != 0 {
        let Some(get_info) = ptr::addr_of!((*state).get_file_information_by_handle_ex).read()
        else {
            free(file.cast());
            ptr::addr_of!((*state).close_handle)
                .read()
                .map(|close| close(native_handle));
            complete_io(ov, ERROR_IO, 0);
            return;
        };
        let mut info = FileStandardInfo {
            allocation_size: 0,
            end_of_file: 0,
            number_of_links: 0,
            delete_pending: 0,
            directory: 0,
        };
        if get_info(
            native_handle,
            FILE_STANDARD_INFO_CLASS,
            (&mut info as *mut FileStandardInfo).cast(),
            core::mem::size_of::<FileStandardInfo>() as u32,
        ) == 0
            || info.directory == 0
        {
            let error = if info.directory == 0 {
                pal_abi::ERROR_NOTDIR
            } else {
                ptr::addr_of!((*state).get_last_error)
                    .read()
                    .map(|get_last_error| map_windows_error(get_last_error()))
                    .unwrap_or(ERROR_IO)
            };
            free(file.cast());
            ptr::addr_of!((*state).close_handle)
                .read()
                .map(|close| close(native_handle));
            complete_io(ov, error, 0);
            return;
        }
    }
    file.write(FileHandle {
        token: next_token,
        native_handle,
        cursor: 0,
        access,
        append: flags & ABI_O_APPEND != 0,
        next: ptr::addr_of!((*state).file_handles).read(),
        closing: false,
        pending_operations: 0,
        sequential_head: ptr::null_mut(),
        sequential_tail: ptr::null_mut(),
        sequential_active: false,
    });
    ptr::addr_of_mut!((*state).file_handles).write(file);
    ptr::addr_of_mut!((*state).next_file_token).write(after_token);
    complete_io(ov, 0, next_token);
}

unsafe fn probe_seek(ov: *mut Overlapped, token: u64, offset: i64, whence: u32) {
    if ov.is_null() || whence > 2 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file = find_file_handle(token);
    if file.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    if (*file).pending_operations == usize::MAX {
        complete_io(ov, pal_abi::ERROR_OVERFLOW, 0);
        return;
    }
    let operation = allocate_io_operation();
    if operation.is_null() {
        complete_io(ov, pal_abi::ERROR_NOMEM, 0);
        return;
    }
    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;
    (*operation).overlapped = ov;
    (*operation).handle = (*file).native_handle;
    (*operation).kind = IoKind::FileSeek;
    (*operation).token = token;
    (*operation).seek_offset = offset;
    (*operation).whence = whence;
    (*operation).file = file;
    (*file).pending_operations += 1;
    link_io_operation(operation);
    enqueue_file_operation(operation);
}

#[repr(C)]
struct FileEndOfFileInfo {
    end_of_file: i64,
}

unsafe fn probe_handle_truncate(ov: *mut Overlapped, token: u64, size: i64) {
    if ov.is_null() || size < 0 {
        complete_io(ov, ERROR_INVALID, 0);
        return;
    }
    let file = find_file_handle(token);
    if file.is_null() {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    if (*file).access & 2 == 0 {
        complete_io(ov, ERROR_BADF, 0);
        return;
    }
    if (*file).pending_operations == usize::MAX {
        complete_io(ov, pal_abi::ERROR_OVERFLOW, 0);
        return;
    }
    let operation = allocate_io_operation();
    if operation.is_null() {
        complete_io(ov, pal_abi::ERROR_NOMEM, 0);
        return;
    }
    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;
    (*operation).overlapped = ov;
    (*operation).handle = (*file).native_handle;
    (*operation).kind = IoKind::FileTruncate;
    (*operation).token = token;
    (*operation).seek_offset = size;
    (*operation).file = file;
    (*file).pending_operations += 1;
    link_io_operation(operation);
    enqueue_file_operation(operation);
}

unsafe fn probe_handle_close(token: u64) {
    let state = state_ptr();
    let mut link = ptr::addr_of_mut!((*state).file_handles);
    while !link.read().is_null() {
        let file = link.read();
        if (*file).token == token {
            link.write((*file).next);
            (*file).next = ptr::null_mut();
            (*file).closing = true;
            if (*file).pending_operations == 0 {
                close_file_record(file);
            }
            return;
        }
        link = ptr::addr_of_mut!((*file).next);
    }
}

unsafe fn perform_io_worker(operation: *mut IoOperation) -> u32 {
    let state = state_ptr();
    let read_file = ptr::addr_of!((*state).read_file).read();
    let write_file = ptr::addr_of!((*state).write_file).read();
    let get_last_error = ptr::addr_of!((*state).get_last_error).read();
    let mut transferred = 0u32;
    let ok = if (*operation).is_write {
        match write_file {
            Some(write_file) => {
                write_file(
                    (*operation).handle,
                    (*operation).buffer.cast(),
                    (*operation).length,
                    &mut transferred,
                    ptr::null_mut(),
                ) != 0
            }
            None => false,
        }
    } else {
        match read_file {
            Some(read_file) => {
                read_file(
                    (*operation).handle,
                    (*operation).buffer.cast(),
                    (*operation).length,
                    &mut transferred,
                    ptr::null_mut(),
                ) != 0
            }
            None => false,
        }
    };
    let (error, result) = if ok {
        (0, transferred as u64)
    } else {
        let error = match get_last_error {
            Some(get_last_error) => get_last_error(),
            None => 0,
        };
        if !(*operation).is_write && error == ERROR_BROKEN_PIPE {
            (0, 0)
        } else if error == ERROR_BROKEN_PIPE || error == ERROR_NO_DATA {
            (ERROR_PIPE, 0)
        } else if error == ERROR_INVALID_HANDLE_CODE {
            (ERROR_BADF, 0)
        } else if error == ERROR_INVALID_PARAMETER_CODE {
            (ERROR_INVALID, 0)
        } else {
            (ERROR_IO, 0)
        }
    };
    (*operation).error = error;
    (*operation).result = result;
    (*operation)
        .worker_completion_ready
        .store(1, Ordering::Release);
    post_io_completion(operation, STDIO_COMPLETION_KEY);
    0
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn run_io_worker(context: *mut c_void) -> u32 {
    let status = perform_io_worker(context.cast());
    if status != 0 {
        exit_process_impl(16);
    }
    status
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn run_io_worker(context: *mut c_void) -> u32 {
    let status = perform_io_worker(context.cast());
    if status != 0 {
        exit_process_impl(16);
    }
    status
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_write(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *const u8,
    length: usize,
) {
    begin_io(ov, handle, buffer as *mut u8, length, true);
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_write(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *const u8,
    length: usize,
) {
    begin_io(ov, handle, buffer as *mut u8, length, true)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_read(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *mut u8,
    length: usize,
) {
    begin_io(ov, handle, buffer, length, false)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_read(ov: *mut Overlapped, handle: u64, buffer: *mut u8, length: usize) {
    begin_io(ov, handle, buffer, length, false)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_path_open_abi(
    ov: *mut Overlapped,
    path: *const u8,
    path_length: usize,
    flags: u32,
) {
    probe_path_open(ov, path, path_length, flags)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_path_open_abi(
    ov: *mut Overlapped,
    path: *const u8,
    path_length: usize,
    flags: u32,
) {
    probe_path_open(ov, path, path_length, flags)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_seek_abi(ov: *mut Overlapped, token: u64, offset: i64, whence: u32) {
    probe_seek(ov, token, offset, whence)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_seek_abi(ov: *mut Overlapped, token: u64, offset: i64, whence: u32) {
    probe_seek(ov, token, offset, whence)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_handle_truncate_abi(ov: *mut Overlapped, token: u64, size: i64) {
    probe_handle_truncate(ov, token, size)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_handle_truncate_abi(ov: *mut Overlapped, token: u64, size: i64) {
    probe_handle_truncate(ov, token, size)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_handle_close_abi(token: u64) {
    probe_handle_close(token)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_handle_close_abi(token: u64) {
    probe_handle_close(token)
}

unsafe fn exit_process_impl(status: i32) -> ! {
    let exit = {
        let state = state_mut();
        state.exit_process
    };
    if let Some(exit) = exit {
        exit(status);
    }
    loop {}
}

#[cfg(target_arch = "x86_64")]
#[no_mangle]
unsafe extern "win64" fn pal_abi_unsupported_exit(status: i32) -> ! {
    exit_process_impl(status)
}

#[cfg(target_arch = "aarch64")]
#[no_mangle]
unsafe extern "C" fn pal_abi_unsupported_exit(status: i32) -> ! {
    exit_process_impl(status)
}

unsafe fn publish_one_io_completion(entry: OverlappedEntry) -> bool {
    let state = state_ptr();
    let operation = find_io_operation(entry.overlapped);
    if operation.is_null() {
        return false;
    }

    let mut error = (*operation).error;
    let mut result = (*operation).result;
    if (*operation).kind == IoKind::Stdio {
        if entry.completion_key != STDIO_COMPLETION_KEY
            || entry.internal != 0
            || (*operation)
                .worker_completion_ready
                .load(Ordering::Acquire)
                != 1
        {
            return false;
        }
        let (Some(wait), Some(close)) = (
            ptr::addr_of!((*state).wait_for_single_object).read(),
            ptr::addr_of!((*state).close_handle).read(),
        ) else {
            return false;
        };
        let thread = (*operation).worker_thread;
        if thread.is_null() || wait(thread, INFINITE) != WAIT_OBJECT_0 || close(thread) == 0 {
            return false;
        }
        (*operation).worker_thread = ptr::null_mut();
    } else {
        if entry.completion_key != (*operation).token as usize {
            return false;
        }
        if !(*operation).immediate {
            let Some(get_result) = ptr::addr_of!((*state).get_overlapped_result).read() else {
                return false;
            };
            let mut transferred = 0u32;
            let succeeded = get_result(
                (*operation).handle,
                ptr::addr_of_mut!((*operation).native_overlapped).cast(),
                &mut transferred,
                0,
            ) != 0;
            if succeeded {
                if entry.bytes_transferred != transferred {
                    error = ERROR_IO;
                    result = 0;
                } else {
                    error = 0;
                    result = transferred as u64;
                }
            } else {
                let os_error = ptr::addr_of!((*state).get_last_error)
                    .read()
                    .map(|get_last_error| get_last_error())
                    .unwrap_or(0);
                if (*operation).kind == IoKind::FileRead && os_error == ERROR_HANDLE_EOF_CODE {
                    error = 0;
                    result = 0;
                } else {
                    error = map_windows_error(os_error);
                    result = 0;
                }
            }
        }

        let file = (*operation).file;
        if error == 0 && !file.is_null() {
            if (*operation).kind == IoKind::FileSeek {
                (*file).cursor = result as i64;
            } else if !(*operation).positional
                && ((*operation).kind == IoKind::FileRead
                    || (*operation).kind == IoKind::FileWrite)
            {
                let cursor = if (*operation).kind == IoKind::FileWrite && (*file).append {
                    let mut size = 0i64;
                    match ptr::addr_of!((*state).get_file_size_ex).read() {
                        Some(get_size) if get_size((*file).native_handle, &mut size) != 0 => size,
                        _ => match (*operation)
                            .file_offset
                            .checked_add(result as i64)
                        {
                            Some(position) => position,
                            None => {
                                error = pal_abi::ERROR_OVERFLOW;
                                0
                            }
                        },
                    }
                } else {
                    match (*operation).file_offset.checked_add(result as i64) {
                        Some(position) => position,
                        None => {
                            error = pal_abi::ERROR_OVERFLOW;
                            0
                        }
                    }
                };
                if error == 0 {
                    (*file).cursor = cursor;
                }
            }
        }
    }

    let file = (*operation).file;
    if !file.is_null() && !(*operation).positional {
        if (*file).sequential_head != operation {
            return false;
        }
        let next = (*operation).next_for_file;
        (*file).sequential_head = next;
        if next.is_null() {
            (*file).sequential_tail = ptr::null_mut();
            (*file).sequential_active = false;
        } else {
            dispatch_file_operation(next);
        }
    }
    complete_io((*operation).overlapped, error, result);
    free_io_operation(operation);
    if !file.is_null() {
        release_file_reference(file);
    }
    true
}

unsafe fn wait_and_publish_pending_io() -> bool {
    let state = state_ptr();
    let port = ptr::addr_of!((*state).completion_port).read();
    let Some(get_completion) = ptr::addr_of!((*state).get_queued_completion_status_ex).read()
    else {
        return false;
    };
    if port.is_null() {
        return false;
    }
    let mut entries = [OverlappedEntry {
        completion_key: 0,
        overlapped: ptr::null_mut(),
        internal: 0,
        bytes_transferred: 0,
    }; IOCP_BATCH_SIZE];
    let mut removed = 0u32;
    if get_completion(
        port,
        entries.as_mut_ptr(),
        IOCP_BATCH_SIZE as u32,
        &mut removed,
        INFINITE,
        0,
    ) == 0
        || removed == 0
        || removed as usize > IOCP_BATCH_SIZE
    {
        return false;
    }
    let mut index = 0usize;
    while index < removed as usize {
        if !publish_one_io_completion(entries.as_ptr().add(index).read()) {
            return false;
        }
        index += 1;
    }
    true
}

unsafe fn run_platform(host: &WindowsPlatformHost) -> i32 {
    let Some(load_library) = host.load_library else {
        return 10;
    };
    if host.get_proc_address.is_none() {
        return 10;
    }
    let Some(alloc) = host.alloc else {
        return 10;
    };
    let Some(free) = host.free else {
        return 10;
    };
    if host.hello_payload.is_null() || host.hello_payload_size == 0 {
        return 10;
    }

    let kernel32 = load_library(b"kernel32.dll\0".as_ptr());
    if kernel32.is_null() {
        return 11;
    }

    let get_std_handle =
        function_pointer!(resolve(host, kernel32, b"GetStdHandle\0"), GetStdHandleFn);
    let read_file = function_pointer!(resolve(host, kernel32, b"ReadFile\0"), ReadFileFn);
    let write_file = function_pointer!(resolve(host, kernel32, b"WriteFile\0"), WriteFileFn);
    let get_last_error =
        function_pointer!(resolve(host, kernel32, b"GetLastError\0"), GetLastErrorFn);
    let flush_instruction_cache = function_pointer!(
        resolve(host, kernel32, b"FlushInstructionCache\0"),
        FlushInstructionCacheFn
    );
    let exit_process = function_pointer!(resolve(host, kernel32, b"ExitProcess\0"), ExitProcessFn);
    let get_current_process = function_pointer!(
        resolve(host, kernel32, b"GetCurrentProcess\0"),
        GetCurrentProcessFn
    );
    let create_thread =
        function_pointer!(resolve(host, kernel32, b"CreateThread\0"), CreateThreadFn);
    let wait_for_single_object = function_pointer!(
        resolve(host, kernel32, b"WaitForSingleObject\0"),
        WaitForSingleObjectFn
    );
    let close_handle = function_pointer!(resolve(host, kernel32, b"CloseHandle\0"), CloseHandleFn);
    let create_completion_port = function_pointer!(
        resolve(host, kernel32, b"CreateIoCompletionPort\0"),
        CreateIoCompletionPortFn
    );
    let get_completion = function_pointer!(
        resolve(host, kernel32, b"GetQueuedCompletionStatusEx\0"),
        GetQueuedCompletionStatusExFn
    );
    let post_completion = function_pointer!(
        resolve(host, kernel32, b"PostQueuedCompletionStatus\0"),
        PostQueuedCompletionStatusFn
    );
    let create_file_w = function_pointer!(resolve(host, kernel32, b"CreateFileW\0"), CreateFileWFn);
    let multi_byte_to_wide_char = function_pointer!(
        resolve(host, kernel32, b"MultiByteToWideChar\0"),
        MultiByteToWideCharFn
    );
    let get_overlapped_result = function_pointer!(
        resolve(host, kernel32, b"GetOverlappedResult\0"),
        GetOverlappedResultFn
    );
    let get_file_size_ex =
        function_pointer!(resolve(host, kernel32, b"GetFileSizeEx\0"), GetFileSizeExFn);
    let get_file_information_by_handle_ex = function_pointer!(
        resolve(host, kernel32, b"GetFileInformationByHandleEx\0"),
        GetFileInformationByHandleExFn
    );
    let set_file_information_by_handle = function_pointer!(
        resolve(host, kernel32, b"SetFileInformationByHandle\0"),
        SetFileInformationByHandleFn
    );
    if get_std_handle.is_none()
        || read_file.is_none()
        || write_file.is_none()
        || get_last_error.is_none()
        || flush_instruction_cache.is_none()
        || exit_process.is_none()
        || get_current_process.is_none()
        || create_thread.is_none()
        || wait_for_single_object.is_none()
        || close_handle.is_none()
        || create_completion_port.is_none()
        || get_completion.is_none()
        || post_completion.is_none()
        || create_file_w.is_none()
        || multi_byte_to_wide_char.is_none()
        || get_overlapped_result.is_none()
        || get_file_size_ex.is_none()
        || get_file_information_by_handle_ex.is_none()
        || set_file_information_by_handle.is_none()
    {
        return 12;
    }

    let Some(exit_process) = exit_process else {
        return 12;
    };
    let Some(get_std_handle) = get_std_handle else {
        return 12;
    };
    let Some(create_completion_port) = create_completion_port else {
        return 12;
    };
    let Some(get_completion) = get_completion else {
        return 12;
    };
    let Some(post_completion) = post_completion else {
        return 12;
    };
    let Some(close_handle) = close_handle else {
        return 12;
    };
    let input_handle = get_std_handle(STD_INPUT_HANDLE);
    let output_handle = get_std_handle(STD_OUTPUT_HANDLE);
    let error_handle = get_std_handle(STD_ERROR_HANDLE);
    if input_handle.is_null() || output_handle.is_null() || error_handle.is_null() {
        return 13;
    }

    let total_payload_size = PREFIX_ABI_OFFSET + host.hello_payload_size;
    let payload_alloc = alloc(total_payload_size);
    if payload_alloc.is_null() {
        return 14;
    }
    let alloc_bytes = payload_alloc.cast::<u8>();
    let mut z = 0usize;
    while z < PREFIX_ABI_OFFSET {
        ptr::write_volatile(alloc_bytes.add(z), 0);
        z += 1;
    }
    let payload_code = alloc_bytes.add(PREFIX_ABI_OFFSET).cast::<c_void>();
    let destination = payload_code.cast::<u8>();
    let mut index = 0usize;
    while index < host.hello_payload_size {
        let byte = ptr::read_volatile(host.hello_payload.add(index));
        ptr::write_volatile(destination.add(index), byte);
        index += 1;
    }

    let Some(get_current_process) = get_current_process else {
        free(payload_alloc);
        return 12;
    };
    let Some(flush_instruction_cache) = flush_instruction_cache else {
        free(payload_alloc);
        return 12;
    };
    let process_handle = get_current_process();
    if flush_instruction_cache(process_handle, payload_code, host.hello_payload_size) == 0 {
        free(payload_alloc);
        return 15;
    }
    let completion_port = create_completion_port((-1isize) as *mut c_void, ptr::null_mut(), 0, 0);
    if completion_port.is_null() {
        free(payload_alloc);
        return 15;
    }

    ptr::write(
        state_ptr(),
        PlatformState {
            input_handle,
            output_handle,
            error_handle,
            alloc: Some(alloc),
            free: Some(free),
            read_file,
            write_file,
            get_last_error,
            exit_process: Some(exit_process),
            create_thread,
            wait_for_single_object,
            close_handle: Some(close_handle),
            completion_port,
            get_queued_completion_status_ex: Some(get_completion),
            post_queued_completion_status: Some(post_completion),
            create_io_completion_port: Some(create_completion_port),
            create_file_w,
            multi_byte_to_wide_char,
            get_overlapped_result,
            get_file_size_ex,
            get_file_information_by_handle_ex,
            set_file_information_by_handle,
            file_handles: ptr::null_mut(),
            next_file_token: 3,
            io_operations: ptr::null_mut(),
        },
    );
    let mut abi = pal_abi_stubs::for_payload(probe_read, probe_write, exit_process);
    abi.get_args = probe_get_args;
    abi.path_open = probe_path_open_abi;
    abi.seek = probe_seek_abi;
    abi.handle_truncate = probe_handle_truncate_abi;
    abi.handle_close = probe_handle_close_abi;
    ptr::write(payload_alloc.cast::<PalAbiTable>(), abi);
    loop {
        invoke_raw_entry(payload_code);
        let state = state_ptr();
        if ptr::addr_of!((*state).io_operations).read().is_null() {
            close_all_file_handles();
            if close_handle(completion_port) == 0 {
                exit_process_impl(16);
            }
            free(payload_alloc);
            return 0;
        }
        if !wait_and_publish_pending_io() {
            // The host unloads this code when the entry returns; do not return while a worker may execute it.
            exit_process_impl(16);
        }
    }
}

unsafe fn close_all_file_handles() {
    let state = state_ptr();
    let close = ptr::addr_of!((*state).close_handle).read();
    let free = ptr::addr_of!((*state).free).read();
    let mut file = ptr::addr_of!((*state).file_handles).read();
    ptr::addr_of_mut!((*state).file_handles).write(ptr::null_mut());
    while !file.is_null() {
        let next = (*file).next;
        if let Some(close) = close {
            close((*file).native_handle);
        }
        if let Some(free) = free {
            free(file.cast());
        }
        file = next;
    }
}

#[cfg(target_arch = "x86_64")]
#[link_section = ".text.windows_platform_entry"]
#[no_mangle]
pub unsafe extern "win64" fn windows_platform_entry() -> i32 {
    run_platform(host_abi())
}

#[cfg(target_arch = "aarch64")]
#[link_section = ".text.windows_platform_entry"]
#[no_mangle]
pub unsafe extern "C" fn windows_platform_entry() -> i32 {
    run_platform(host_abi())
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
    #[cfg(target_os = "windows")]
    use std::path::PathBuf;
    use std::sync::Mutex;

    static mut TEST_RAW_ENTRY_CALLS: u32 = 0;
    static TEST_STATE_LOCK: Mutex<()> = Mutex::new(());

    #[cfg(target_os = "windows")]
    #[link(name = "kernel32")]
    unsafe extern "system" {
        fn GetModuleHandleA(name: *const u8) -> *mut c_void;
        fn GetProcAddress(module: *mut c_void, name: *const u8) -> *mut c_void;
        fn VirtualAlloc(
            address: *mut c_void,
            size: usize,
            alloc_type: u32,
            protect: u32,
        ) -> *mut c_void;
        fn VirtualFree(address: *mut c_void, size: usize, free_type: u32) -> i32;
    }

    #[cfg(target_os = "windows")]
    const TEST_MEM_COMMIT: u32 = 0x1000;
    #[cfg(target_os = "windows")]
    const TEST_MEM_RESERVE: u32 = 0x2000;
    #[cfg(target_os = "windows")]
    const TEST_MEM_RELEASE: u32 = 0x8000;
    #[cfg(target_os = "windows")]
    const TEST_PAGE_READWRITE: u32 = 0x04;

    #[cfg(target_os = "windows")]
    unsafe fn native_test_alloc_impl(size: usize) -> *mut c_void {
        VirtualAlloc(
            ptr::null_mut(),
            size,
            TEST_MEM_COMMIT | TEST_MEM_RESERVE,
            TEST_PAGE_READWRITE,
        )
    }

    #[cfg(all(target_os = "windows", target_arch = "x86_64"))]
    unsafe extern "win64" fn native_test_alloc(size: usize) -> *mut c_void {
        native_test_alloc_impl(size)
    }

    #[cfg(all(target_os = "windows", target_arch = "aarch64"))]
    unsafe extern "C" fn native_test_alloc(size: usize) -> *mut c_void {
        native_test_alloc_impl(size)
    }

    #[cfg(target_os = "windows")]
    unsafe fn native_test_free_impl(address: *mut c_void) {
        if VirtualFree(address, 0, TEST_MEM_RELEASE) == 0 {
            panic!("VirtualFree failed for test allocation");
        }
    }

    #[cfg(all(target_os = "windows", target_arch = "x86_64"))]
    unsafe extern "win64" fn native_test_free(address: *mut c_void) {
        native_test_free_impl(address)
    }

    #[cfg(all(target_os = "windows", target_arch = "aarch64"))]
    unsafe extern "C" fn native_test_free(address: *mut c_void) {
        native_test_free_impl(address)
    }

    #[cfg(target_os = "windows")]
    unsafe fn initialize_native_file_io_state() {
        let module = GetModuleHandleA(b"kernel32.dll\0".as_ptr());
        assert!(!module.is_null());
        let get_proc_address: GetProcAddressFn = core::mem::transmute(GetProcAddress as *const ());
        let host = WindowsPlatformHost {
            load_library: None,
            get_proc_address: Some(get_proc_address),
            alloc: None,
            free: None,
            hello_payload: ptr::null(),
            hello_payload_size: 0,
            argv: ptr::null(),
            argc: 0,
        };
        let get_last_error =
            function_pointer!(resolve(&host, module, b"GetLastError\0"), GetLastErrorFn).unwrap();
        let close_handle =
            function_pointer!(resolve(&host, module, b"CloseHandle\0"), CloseHandleFn).unwrap();
        let create_io_completion_port = function_pointer!(
            resolve(&host, module, b"CreateIoCompletionPort\0"),
            CreateIoCompletionPortFn
        )
        .unwrap();
        let completion_port =
            create_io_completion_port((-1isize) as *mut c_void, ptr::null_mut(), 0, 0);
        assert!(!completion_port.is_null());
        ptr::addr_of_mut!(TEST_STATE).write(PlatformState {
            input_handle: ptr::null_mut(),
            output_handle: ptr::null_mut(),
            error_handle: ptr::null_mut(),
            alloc: Some(native_test_alloc),
            free: Some(native_test_free),
            read_file: function_pointer!(resolve(&host, module, b"ReadFile\0"), ReadFileFn),
            write_file: function_pointer!(resolve(&host, module, b"WriteFile\0"), WriteFileFn),
            get_last_error: Some(get_last_error),
            exit_process: None,
            create_thread: None,
            wait_for_single_object: None,
            close_handle: Some(close_handle),
            completion_port,
            get_queued_completion_status_ex: function_pointer!(
                resolve(&host, module, b"GetQueuedCompletionStatusEx\0"),
                GetQueuedCompletionStatusExFn
            ),
            post_queued_completion_status: function_pointer!(
                resolve(&host, module, b"PostQueuedCompletionStatus\0"),
                PostQueuedCompletionStatusFn
            ),
            create_io_completion_port: Some(create_io_completion_port),
            create_file_w: function_pointer!(
                resolve(&host, module, b"CreateFileW\0"),
                CreateFileWFn
            ),
            multi_byte_to_wide_char: function_pointer!(
                resolve(&host, module, b"MultiByteToWideChar\0"),
                MultiByteToWideCharFn
            ),
            get_overlapped_result: function_pointer!(
                resolve(&host, module, b"GetOverlappedResult\0"),
                GetOverlappedResultFn
            ),
            get_file_size_ex: function_pointer!(
                resolve(&host, module, b"GetFileSizeEx\0"),
                GetFileSizeExFn
            ),
            get_file_information_by_handle_ex: function_pointer!(
                resolve(&host, module, b"GetFileInformationByHandleEx\0"),
                GetFileInformationByHandleExFn
            ),
            set_file_information_by_handle: function_pointer!(
                resolve(&host, module, b"SetFileInformationByHandle\0"),
                SetFileInformationByHandleFn
            ),
            file_handles: ptr::null_mut(),
            next_file_token: 3,
            io_operations: ptr::null_mut(),
        });
    }

    #[cfg(target_os = "windows")]
    struct RemoveOnDrop(PathBuf);

    #[cfg(target_os = "windows")]
    impl Drop for RemoveOnDrop {
        fn drop(&mut self) {
            if let Err(error) = std::fs::remove_file(&self.0) {
                if error.kind() != std::io::ErrorKind::NotFound {
                    panic!("remove test file {}: {error}", self.0.display());
                }
            }
        }
    }

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

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn mock_create_thread(
        _attributes: *mut c_void,
        _stack_size: usize,
        _start: Option<unsafe extern "win64" fn(*mut c_void) -> u32>,
        _context: *mut c_void,
        _creation_flags: u32,
        _thread_id: *mut u32,
    ) -> *mut c_void {
        7usize as *mut c_void
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn mock_create_thread(
        _attributes: *mut c_void,
        _stack_size: usize,
        _start: Option<unsafe extern "C" fn(*mut c_void) -> u32>,
        _context: *mut c_void,
        _creation_flags: u32,
        _thread_id: *mut u32,
    ) -> *mut c_void {
        7usize as *mut c_void
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn fail_create_thread(
        _attributes: *mut c_void,
        _stack_size: usize,
        _start: Option<unsafe extern "win64" fn(*mut c_void) -> u32>,
        _context: *mut c_void,
        _creation_flags: u32,
        _thread_id: *mut u32,
    ) -> *mut c_void {
        ptr::null_mut()
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn fail_create_thread(
        _attributes: *mut c_void,
        _stack_size: usize,
        _start: Option<unsafe extern "C" fn(*mut c_void) -> u32>,
        _context: *mut c_void,
        _creation_flags: u32,
        _thread_id: *mut u32,
    ) -> *mut c_void {
        ptr::null_mut()
    }

    struct ScriptedIo {
        read_success: i32,
        read_error: u32,
        read_transferred: u32,
        read_data: [u8; 8],
        read_calls: usize,
        write_success: i32,
        write_error: u32,
        write_transferred: u32,
        write_calls: usize,
        write_length: u32,
        last_error: u32,
        wait_result: u32,
        wait_calls: usize,
        close_calls: usize,
        completion_success: i32,
        completion_error: u32,
        completion_count: u32,
        completion_entries: [OverlappedEntry; IOCP_BATCH_SIZE],
        posted_key: usize,
        posted_overlapped: *mut c_void,
        post_success: i32,
        post_calls: usize,
    }

    static mut TEST_IO: ScriptedIo = ScriptedIo {
        read_success: 1,
        read_error: 0,
        read_transferred: 0,
        read_data: [0; 8],
        read_calls: 0,
        write_success: 1,
        write_error: 0,
        write_transferred: 0,
        write_calls: 0,
        write_length: 0,
        last_error: 0,
        wait_result: WAIT_OBJECT_0,
        wait_calls: 0,
        close_calls: 0,
        completion_success: 1,
        completion_error: 0,
        completion_count: 0,
        completion_entries: [OverlappedEntry {
            completion_key: 0,
            overlapped: ptr::null_mut(),
            internal: 0,
            bytes_transferred: 0,
        }; IOCP_BATCH_SIZE],
        posted_key: 0,
        posted_overlapped: ptr::null_mut(),
        post_success: 1,
        post_calls: 0,
    };
    static mut TEST_HEAP: [usize; 1024] = [0; 1024];
    static mut TEST_HEAP_USED: usize = 0;

    unsafe fn scripted_alloc_impl(size: usize) -> *mut c_void {
        let alignment = core::mem::align_of::<usize>();
        let start = (TEST_HEAP_USED + alignment - 1) & !(alignment - 1);
        let Some(end) = start.checked_add(size) else {
            return ptr::null_mut();
        };
        if end > core::mem::size_of::<[usize; 1024]>() {
            return ptr::null_mut();
        }
        TEST_HEAP_USED = end;
        ptr::addr_of_mut!(TEST_HEAP).cast::<u8>().add(start).cast()
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_alloc(size: usize) -> *mut c_void {
        scripted_alloc_impl(size)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_alloc(size: usize) -> *mut c_void {
        scripted_alloc_impl(size)
    }

    unsafe fn scripted_free_impl(_address: *mut c_void) {}

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_free(address: *mut c_void) {
        scripted_free_impl(address)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_free(address: *mut c_void) {
        scripted_free_impl(address)
    }

    unsafe fn scripted_read_file_impl(
        _handle: *mut c_void,
        buffer: *mut c_void,
        length: u32,
        transferred: *mut u32,
        _overlapped: *mut c_void,
    ) -> i32 {
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.read_calls += 1;
        io.last_error = io.read_error;
        transferred.write(io.read_transferred);
        if io.read_success != 0 {
            let count = core::cmp::min(io.read_transferred as usize, length as usize);
            ptr::copy_nonoverlapping(io.read_data.as_ptr(), buffer.cast(), count);
        }
        io.read_success
    }

    unsafe fn scripted_write_file_impl(
        _handle: *mut c_void,
        _buffer: *const c_void,
        length: u32,
        transferred: *mut u32,
        _overlapped: *mut c_void,
    ) -> i32 {
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.write_calls += 1;
        io.write_length = length;
        io.last_error = io.write_error;
        transferred.write(io.write_transferred);
        io.write_success
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_read_file(
        handle: *mut c_void,
        buffer: *mut c_void,
        length: u32,
        transferred: *mut u32,
        overlapped: *mut c_void,
    ) -> i32 {
        scripted_read_file_impl(handle, buffer, length, transferred, overlapped)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_read_file(
        handle: *mut c_void,
        buffer: *mut c_void,
        length: u32,
        transferred: *mut u32,
        overlapped: *mut c_void,
    ) -> i32 {
        scripted_read_file_impl(handle, buffer, length, transferred, overlapped)
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_write_file(
        handle: *mut c_void,
        buffer: *const c_void,
        length: u32,
        transferred: *mut u32,
        overlapped: *mut c_void,
    ) -> i32 {
        scripted_write_file_impl(handle, buffer, length, transferred, overlapped)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_write_file(
        handle: *mut c_void,
        buffer: *const c_void,
        length: u32,
        transferred: *mut u32,
        overlapped: *mut c_void,
    ) -> i32 {
        scripted_write_file_impl(handle, buffer, length, transferred, overlapped)
    }

    unsafe fn scripted_get_last_error_impl() -> u32 {
        (*ptr::addr_of!(TEST_IO)).last_error
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_get_last_error() -> u32 {
        scripted_get_last_error_impl()
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_get_last_error() -> u32 {
        scripted_get_last_error_impl()
    }

    unsafe fn scripted_get_completion_impl(
        port: *mut c_void,
        entries: *mut OverlappedEntry,
        count: u32,
        removed: *mut u32,
        timeout: u32,
        alertable: i32,
    ) -> i32 {
        assert_eq!(port, 8usize as *mut c_void);
        assert!(count > 0);
        assert_eq!(timeout, INFINITE);
        assert_eq!(alertable, 0);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        if io.completion_success == 0 {
            io.last_error = io.completion_error;
            removed.write(0);
            return 0;
        }
        if io.completion_count == 0 {
            removed.write(0);
            return 0;
        }
        let completed = core::cmp::min(count as usize, io.completion_count as usize);
        ptr::copy_nonoverlapping(io.completion_entries.as_ptr(), entries, completed);
        let remaining = io.completion_count as usize - completed;
        if remaining > 0 {
            ptr::copy(
                io.completion_entries.as_ptr().add(completed),
                io.completion_entries.as_mut_ptr(),
                remaining,
            );
        }
        io.completion_count = remaining as u32;
        removed.write(completed as u32);
        1
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_get_completion(
        port: *mut c_void,
        entries: *mut OverlappedEntry,
        count: u32,
        removed: *mut u32,
        timeout: u32,
        alertable: i32,
    ) -> i32 {
        scripted_get_completion_impl(port, entries, count, removed, timeout, alertable)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_get_completion(
        port: *mut c_void,
        entries: *mut OverlappedEntry,
        count: u32,
        removed: *mut u32,
        timeout: u32,
        alertable: i32,
    ) -> i32 {
        scripted_get_completion_impl(port, entries, count, removed, timeout, alertable)
    }

    unsafe fn scripted_post_completion_impl(
        port: *mut c_void,
        _bytes: u32,
        key: usize,
        overlapped: *mut c_void,
    ) -> i32 {
        assert_eq!(port, 8usize as *mut c_void);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.post_calls += 1;
        io.posted_key = key;
        io.posted_overlapped = overlapped;
        if io.post_success != 0 {
            if io.completion_count as usize == IOCP_BATCH_SIZE {
                return 0;
            }
            let index = io.completion_count as usize;
            io.completion_entries[index] = OverlappedEntry {
                completion_key: key,
                overlapped,
                internal: 0,
                bytes_transferred: 0,
            };
            io.completion_count += 1;
        }
        io.post_success
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_post_completion(
        port: *mut c_void,
        bytes: u32,
        key: usize,
        overlapped: *mut c_void,
    ) -> i32 {
        scripted_post_completion_impl(port, bytes, key, overlapped)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_post_completion(
        port: *mut c_void,
        bytes: u32,
        key: usize,
        overlapped: *mut c_void,
    ) -> i32 {
        scripted_post_completion_impl(port, bytes, key, overlapped)
    }

    unsafe fn scripted_wait_impl(_handle: *mut c_void, timeout: u32) -> u32 {
        assert_eq!(timeout, INFINITE);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.wait_calls += 1;
        io.wait_result
    }

    unsafe fn scripted_close_impl(handle: *mut c_void) -> i32 {
        assert_eq!(handle, 7usize as *mut c_void);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.close_calls += 1;
        1
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_wait(handle: *mut c_void, timeout: u32) -> u32 {
        scripted_wait_impl(handle, timeout)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_wait(handle: *mut c_void, timeout: u32) -> u32 {
        scripted_wait_impl(handle, timeout)
    }

    #[cfg(target_arch = "x86_64")]
    unsafe extern "win64" fn scripted_close(handle: *mut c_void) -> i32 {
        scripted_close_impl(handle)
    }

    #[cfg(target_arch = "aarch64")]
    unsafe extern "C" fn scripted_close(handle: *mut c_void) -> i32 {
        scripted_close_impl(handle)
    }

    unsafe fn reset_io_test_state() {
        TEST_HEAP_USED = 0;
        ptr::addr_of_mut!(TEST_STATE).write(PlatformState {
            input_handle: 1usize as *mut c_void,
            output_handle: 2usize as *mut c_void,
            error_handle: ptr::null_mut(),
            alloc: Some(scripted_alloc),
            free: Some(scripted_free),
            read_file: Some(scripted_read_file),
            write_file: Some(scripted_write_file),
            get_last_error: Some(scripted_get_last_error),
            exit_process: None,
            create_thread: None,
            wait_for_single_object: Some(scripted_wait),
            close_handle: Some(scripted_close),
            completion_port: 8usize as *mut c_void,
            get_queued_completion_status_ex: Some(scripted_get_completion),
            post_queued_completion_status: Some(scripted_post_completion),
            create_io_completion_port: None,
            create_file_w: None,
            multi_byte_to_wide_char: None,
            get_overlapped_result: None,
            get_file_size_ex: None,
            get_file_information_by_handle_ex: None,
            set_file_information_by_handle: None,
            file_handles: ptr::null_mut(),
            next_file_token: 3,
            io_operations: ptr::null_mut(),
        });
        ptr::addr_of_mut!(TEST_IO).write(ScriptedIo {
            read_success: 1,
            read_error: 0,
            read_transferred: 0,
            read_data: *b"OK\0\0\0\0\0\0",
            read_calls: 0,
            write_success: 1,
            write_error: 0,
            write_transferred: 0,
            write_calls: 0,
            write_length: 0,
            last_error: 0,
            wait_result: WAIT_OBJECT_0,
            wait_calls: 0,
            close_calls: 0,
            completion_success: 1,
            completion_error: 0,
            completion_count: 0,
            completion_entries: [OverlappedEntry {
                completion_key: 0,
                overlapped: ptr::null_mut(),
                internal: 0,
                bytes_transferred: 0,
            }; IOCP_BATCH_SIZE],
            posted_key: 0,
            posted_overlapped: ptr::null_mut(),
            post_success: 1,
            post_calls: 0,
        });
    }

    fn assert_completion(ov: &Overlapped, error: u32, result: u64) {
        assert_eq!(ov.flags, pal_abi::FLAG_COMPLETED);
        assert_eq!(ov.error, error);
        assert_eq!(ov.continued, 0);
        assert_eq!(ov.result_ext, result);
    }

    #[test]
    fn test_windows_platform_prefix_layout() {
        assert_eq!(PREFIX_ABI_OFFSET, 0x20000);
        assert_eq!(size_of::<WindowsPlatformHost>(), 64);
        assert_eq!(offset_of!(WindowsPlatformHost, load_library), 0);
        assert_eq!(offset_of!(WindowsPlatformHost, get_proc_address), 8);
        assert_eq!(offset_of!(WindowsPlatformHost, alloc), 16);
        assert_eq!(offset_of!(WindowsPlatformHost, free), 24);
        assert_eq!(offset_of!(WindowsPlatformHost, hello_payload), 32);
        assert_eq!(offset_of!(WindowsPlatformHost, hello_payload_size), 40);
        assert_eq!(offset_of!(WindowsPlatformHost, argv), 48);
        assert_eq!(offset_of!(WindowsPlatformHost, argc), 56);
        assert_eq!(size_of::<NativeOverlapped>(), 32);
        assert_eq!(offset_of!(IoOperation, native_overlapped), 0);
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
        let _guard = TEST_STATE_LOCK
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        unsafe {
            reset_io_test_state();
            let state = state_ptr();
            (*state).create_thread = Some(mock_create_thread);
            let mut first = Overlapped::default();
            let mut second = Overlapped::default();
            let mut buffer = [0u8; 4];
            (*ptr::addr_of_mut!(TEST_IO)).write_transferred = 1;

            begin_io(&mut first, 1, buffer.as_mut_ptr(), 1, true);
            begin_io(&mut second, 1, buffer.as_mut_ptr(), 1, true);

            assert_eq!(first.flags, 0);
            assert_eq!(first.error, 0);
            assert_eq!(second.flags, 0);
            assert_eq!(second.error, 0);
            let first_operation = (*state).io_operations;
            assert!(!first_operation.is_null());
            let second_operation = (*first_operation).next;
            assert!(!second_operation.is_null());
            assert_ne!(
                ptr::addr_of_mut!((*first_operation).native_overlapped),
                ptr::addr_of_mut!((*second_operation).native_overlapped)
            );
            assert_eq!(perform_io_worker(first_operation), 0);
            assert_eq!(perform_io_worker(second_operation), 0);
            assert_eq!((*ptr::addr_of!(TEST_IO)).completion_count, 2);

            assert!(wait_and_publish_pending_io());
            assert_completion(&first, 0, 1);
            assert_completion(&second, 0, 1);
            assert!((*state).io_operations.is_null());
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 2);
            assert_eq!((*ptr::addr_of!(TEST_IO)).wait_calls, 2);
            assert_eq!((*ptr::addr_of!(TEST_IO)).close_calls, 2);
        }
    }

    #[test]
    fn test_io_submission_worker_errors_and_completion_publication() {
        let _guard = TEST_STATE_LOCK
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        unsafe {
            reset_io_test_state();
            let state = state_ptr();
            let mut overlapped = Overlapped::default();
            let mut buffer = [0u8; 4];

            overlapped.continued = 17;
            overlapped.result_ext = 23;
            begin_io(&mut overlapped, 1, ptr::null_mut(), 0, true);
            assert_completion(&overlapped, 0, 0);

            begin_io(&mut overlapped, 1, ptr::null_mut(), 1, true);
            assert_completion(&overlapped, ERROR_INVALID, 0);

            begin_io(&mut overlapped, 3, buffer.as_mut_ptr(), 1, true);
            assert_completion(&overlapped, ERROR_BADF, 0);
            begin_io(&mut overlapped, 1, buffer.as_mut_ptr(), 1, false);
            assert_completion(&overlapped, ERROR_BADF, 0);

            begin_io(
                &mut overlapped,
                1,
                buffer.as_mut_ptr(),
                u32::MAX as usize + 1,
                true,
            );
            assert_completion(&overlapped, ERROR_INVALID, 0);

            begin_io(&mut overlapped, 1, buffer.as_mut_ptr(), 1, true);
            assert_completion(&overlapped, ERROR_IO, 0);
            assert!((*state).io_operations.is_null());

            (*state).create_thread = Some(fail_create_thread);
            begin_io(&mut overlapped, 1, buffer.as_mut_ptr(), 1, true);
            assert_completion(&overlapped, ERROR_AGAIN, 0);
            assert!((*state).io_operations.is_null());

            (*state).create_thread = Some(mock_create_thread);
            (*ptr::addr_of_mut!(TEST_IO)).write_transferred = 2;
            begin_io(&mut overlapped, 1, buffer.as_mut_ptr(), buffer.len(), true);
            let operation = (*state).io_operations;
            assert!(!operation.is_null());
            assert_eq!(overlapped.flags, 0);
            assert_eq!((*operation).worker_thread, 7usize as *mut c_void);

            assert_eq!(perform_io_worker(operation), 0);
            assert_eq!((*operation).result, 2);
            assert_eq!((*operation).worker_completion_ready.load(Ordering::Acquire), 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).post_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).posted_key, STDIO_COMPLETION_KEY);
            assert_eq!(
                (*ptr::addr_of!(TEST_IO)).posted_overlapped,
                ptr::addr_of_mut!((*operation).native_overlapped).cast()
            );
            assert_eq!(overlapped.flags, 0);
            assert!(wait_and_publish_pending_io());
            assert_completion(&overlapped, 0, 2);
            assert!((*state).io_operations.is_null());
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_length, 4);
            assert_eq!((*ptr::addr_of!(TEST_IO)).post_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).wait_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).close_calls, 1);

            let mut second_overlapped = Overlapped::default();
            (*ptr::addr_of_mut!(TEST_IO)).write_transferred = 1;
            begin_io(&mut second_overlapped, 1, buffer.as_mut_ptr(), 1, true);
            assert_eq!(perform_io_worker((*state).io_operations), 0);
            assert!(wait_and_publish_pending_io());
            assert_completion(&second_overlapped, 0, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 2);

            reset_io_test_state();
            let state = state_ptr();
            (*state).create_thread = Some(mock_create_thread);
            (*ptr::addr_of_mut!(TEST_IO)).read_transferred = 2;
            let mut read_buffer = [0u8; 2];
            begin_io(
                &mut overlapped,
                0,
                read_buffer.as_mut_ptr(),
                read_buffer.len(),
                false,
            );
            assert_eq!(perform_io_worker((*state).io_operations), 0);
            assert_eq!(read_buffer, *b"OK");
            assert!(wait_and_publish_pending_io());
            assert_completion(&overlapped, 0, 2);

            for (is_write, last_error, expected_error) in [
                (false, ERROR_BROKEN_PIPE, 0),
                (false, ERROR_NO_DATA, ERROR_PIPE),
                (true, ERROR_BROKEN_PIPE, ERROR_PIPE),
                (true, ERROR_NO_DATA, ERROR_PIPE),
                (true, ERROR_INVALID_HANDLE_CODE, ERROR_BADF),
                (true, ERROR_INVALID_PARAMETER_CODE, ERROR_INVALID),
                (true, 5, ERROR_IO),
            ] {
                reset_io_test_state();
                let state = state_ptr();
                (*state).create_thread = Some(mock_create_thread);
                {
                    let io = &mut *ptr::addr_of_mut!(TEST_IO);
                    io.read_success = 0;
                    io.read_error = last_error;
                    io.write_success = 0;
                    io.write_error = last_error;
                }
                let handle = if is_write { 1 } else { 0 };
                begin_io(&mut overlapped, handle, buffer.as_mut_ptr(), 1, is_write);
                assert_eq!(perform_io_worker((*state).io_operations), 0);
                assert!(wait_and_publish_pending_io());
                assert_completion(&overlapped, expected_error, 0);
            }
        }
    }

    #[cfg(target_os = "windows")]
    #[test]
    fn test_file_io_uses_overlapped_iocp_and_preserves_file_semantics() {
        let _guard = TEST_STATE_LOCK
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        unsafe {
            let unique = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos();
            let path = std::env::temp_dir()
                .join(format!("mohabbat-iocp-{}-{unique}.tmp", std::process::id()));
            let _cleanup = RemoveOnDrop(path.clone());
            let path_bytes = path
                .to_str()
                .expect("test path must be valid UTF-8")
                .as_bytes();

            initialize_native_file_io_state();
            let state = state_ptr();
            let mut opened = Overlapped::default();
            probe_path_open_abi(
                &mut opened,
                path_bytes.as_ptr(),
                path_bytes.len(),
                2 | ABI_O_CREATE | ABI_O_EXCL,
            );
            assert_eq!(
                opened.error,
                0,
                "path_open returned error {}; Win32 last error is {}",
                opened.error,
                (*state_ptr()).get_last_error.unwrap()()
            );
            assert_completion(&opened, 0, 3);
            let token = opened.result_ext;

            let mut duplicate = Overlapped::default();
            probe_path_open_abi(
                &mut duplicate,
                path_bytes.as_ptr(),
                path_bytes.len(),
                2 | ABI_O_CREATE | ABI_O_EXCL,
            );
            assert_completion(&duplicate, pal_abi::ERROR_EXIST, 0);

            let invalid_utf8 = [0xff];
            let mut invalid_path = Overlapped::default();
            probe_path_open_abi(
                &mut invalid_path,
                invalid_utf8.as_ptr(),
                invalid_utf8.len(),
                0,
            );
            assert_completion(&invalid_path, ERROR_INVALID, 0);

            let mut non_directory = Overlapped::default();
            probe_path_open_abi(
                &mut non_directory,
                path_bytes.as_ptr(),
                path_bytes.len(),
                ABI_O_DIRECTORY,
            );
            assert_completion(&non_directory, pal_abi::ERROR_NOTDIR, 0);
            let directory_path = std::env::temp_dir();
            let directory_bytes = directory_path
                .to_str()
                .expect("temporary directory must be valid UTF-8")
                .as_bytes();
            let mut directory_open = Overlapped::default();
            probe_path_open_abi(
                &mut directory_open,
                directory_bytes.as_ptr(),
                directory_bytes.len(),
                ABI_O_DIRECTORY,
            );
            assert_completion(&directory_open, 0, 4);
            probe_handle_close_abi(directory_open.result_ext);

            let contents = *b"abcdef";
            let mut first_write = Overlapped::default();
            let mut second_write = Overlapped::default();
            probe_write(&mut first_write, token, contents.as_ptr(), 3);
            probe_write(&mut second_write, token, contents.as_ptr().add(3), 3);
            assert_eq!(first_write.flags, 0);
            assert_eq!(second_write.flags, 0);
            while first_write.flags != pal_abi::FLAG_COMPLETED
                || second_write.flags != pal_abi::FLAG_COMPLETED
            {
                assert!(wait_and_publish_pending_io());
            }
            assert_completion(&first_write, 0, 3);
            assert_completion(&second_write, 0, 3);

            #[repr(C)]
            #[derive(Clone, Copy)]
            struct ConcurrentRead {
                overlapped: Overlapped,
                offset: i64,
            }
            let mut concurrent_reads = [ConcurrentRead {
                overlapped: Overlapped {
                    flags: pal_abi::READ_FLAG_POSITIONAL,
                    ..Overlapped::default()
                },
                offset: 0,
            }; 64];
            let mut concurrent_buffers = [[0u8; 2]; 64];
            for index in 0..concurrent_reads.len() {
                let offset = (index % 5) as i64;
                concurrent_reads[index].offset = offset;
                probe_read(
                    &mut concurrent_reads[index].overlapped,
                    token,
                    concurrent_buffers[index].as_mut_ptr(),
                    concurrent_buffers[index].len(),
                );
            }
            assert!(concurrent_reads.iter().all(|read| read.overlapped.flags == 0));
            let mut completed = 0usize;
            while completed < concurrent_reads.len() {
                assert!(wait_and_publish_pending_io());
                completed = concurrent_reads
                    .iter()
                    .filter(|read| read.overlapped.flags == pal_abi::FLAG_COMPLETED)
                    .count();
            }
            for index in 0..concurrent_reads.len() {
                let offset = concurrent_reads[index].offset as usize;
                assert_completion(&concurrent_reads[index].overlapped, 0, 2);
                assert_eq!(
                    concurrent_buffers[index],
                    contents[offset..offset + 2]
                );
            }

            let mut seek = Overlapped::default();
            probe_seek_abi(&mut seek, token, 0, 0);
            assert!(wait_and_publish_pending_io());
            assert_completion(&seek, 0, 0);
            let mut first_read = [0u8; 2];
            let mut read = Overlapped::default();
            probe_read(&mut read, token, first_read.as_mut_ptr(), first_read.len());
            assert!(wait_and_publish_pending_io());
            assert_completion(&read, 0, 2);
            assert_eq!(first_read, *b"ab");

            #[repr(C)]
            struct PositionalRead {
                overlapped: Overlapped,
                offset: i64,
            }
            let mut positional = PositionalRead {
                overlapped: Overlapped {
                    flags: pal_abi::READ_FLAG_POSITIONAL,
                    ..Overlapped::default()
                },
                offset: 4,
            };
            let mut positional_bytes = [0u8; 2];
            probe_read(
                &mut positional.overlapped,
                token,
                positional_bytes.as_mut_ptr(),
                positional_bytes.len(),
            );
            assert!(wait_and_publish_pending_io());
            assert_completion(&positional.overlapped, 0, 2);
            assert_eq!(positional_bytes, *b"ef");

            let mut tell = Overlapped::default();
            probe_seek_abi(&mut tell, token, 0, 1);
            assert!(wait_and_publish_pending_io());
            assert_completion(&tell, 0, 2);
            let mut remaining = [0u8; 8];
            let mut read_remaining = Overlapped::default();
            probe_read(
                &mut read_remaining,
                token,
                remaining.as_mut_ptr(),
                remaining.len(),
            );
            assert!(wait_and_publish_pending_io());
            assert_completion(&read_remaining, 0, 4);
            assert_eq!(&remaining[..4], b"cdef");
            let mut eof = Overlapped::default();
            probe_read(&mut eof, token, remaining.as_mut_ptr(), 1);
            assert!(wait_and_publish_pending_io());
            assert_completion(&eof, 0, 0);

            let mut truncated = Overlapped::default();
            probe_handle_truncate_abi(&mut truncated, token, 4);
            assert!(wait_and_publish_pending_io());
            assert_completion(&truncated, 0, 0);
            let mut seek_end = Overlapped::default();
            probe_seek_abi(&mut seek_end, token, 0, 2);
            assert!(wait_and_publish_pending_io());
            assert_completion(&seek_end, 0, 4);
            probe_handle_close_abi(token);
            assert!(find_file_handle(token).is_null());

            let mut append_open = Overlapped::default();
            probe_path_open_abi(
                &mut append_open,
                path_bytes.as_ptr(),
                path_bytes.len(),
                1 | ABI_O_APPEND,
            );
            assert_completion(&append_open, 0, 5);
            let append_token = append_open.result_ext;
            let append_bytes = *b"xy";
            let mut append_write = Overlapped::default();
            probe_write(
                &mut append_write,
                append_token,
                append_bytes.as_ptr(),
                append_bytes.len(),
            );
            assert!(wait_and_publish_pending_io());
            assert_completion(&append_write, 0, 2);
            let mut append_tell = Overlapped::default();
            probe_seek_abi(&mut append_tell, append_token, 0, 1);
            assert!(wait_and_publish_pending_io());
            assert_completion(&append_tell, 0, 6);
            probe_handle_close_abi(append_token);
            assert_eq!(std::fs::read(&path).unwrap(), b"abcdxy");

            let mut read_only = Overlapped::default();
            probe_path_open_abi(&mut read_only, path_bytes.as_ptr(), path_bytes.len(), 0);
            assert_completion(&read_only, 0, 6);
            let mut denied_write = Overlapped::default();
            probe_write(
                &mut denied_write,
                read_only.result_ext,
                append_bytes.as_ptr(),
                append_bytes.len(),
            );
            assert_completion(&denied_write, ERROR_BADF, 0);
            probe_handle_close_abi(read_only.result_ext);

            let mut bad_handle_read = Overlapped::default();
            probe_read(&mut bad_handle_read, token, remaining.as_mut_ptr(), 1);
            assert_completion(&bad_handle_read, ERROR_BADF, 0);
            close_all_file_handles();
            assert!((*state).file_handles.is_null());
            let close = (*state).close_handle.unwrap();
            assert_ne!(close((*state).completion_port), 0);
        }
    }
}
