#![cfg_attr(not(test), no_std)]

use core::ffi::c_void;
#[cfg(not(test))]
use core::panic::PanicInfo;
use core::ptr;

#[path = "../../pal_abi.rs"]
mod pal_abi;
#[path = "../../pal_abi_stubs.rs"]
mod pal_abi_stubs;
use pal_abi::{pal_fn, Overlapped, PalAbiTable};

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

#[repr(C)]
#[derive(Clone, Copy)]
struct PendingIo {
    overlapped: *mut Overlapped,
    handle: *mut c_void,
    buffer: *mut u8,
    length: u32,
    is_write: bool,
    error: u32,
    result: u64,
}

#[repr(C)]
struct PlatformState {
    input_handle: *mut c_void,
    output_handle: *mut c_void,
    error_handle: *mut c_void,
    read_file: Option<ReadFileFn>,
    write_file: Option<WriteFileFn>,
    get_last_error: Option<GetLastErrorFn>,
    exit_process: Option<ExitProcessFn>,
    create_thread: Option<CreateThreadFn>,
    wait_for_single_object: Option<WaitForSingleObjectFn>,
    close_handle: Option<CloseHandleFn>,
    worker_thread: *mut c_void,
    queued_io: bool,
    pending_io: PendingIo,
}

#[cfg(test)]
static mut TEST_STATE: PlatformState = PlatformState {
    input_handle: ptr::null_mut(),
    output_handle: ptr::null_mut(),
    error_handle: ptr::null_mut(),
    read_file: None,
    write_file: None,
    get_last_error: None,
    exit_process: None,
    create_thread: None,
    wait_for_single_object: None,
    close_handle: None,
    worker_thread: ptr::null_mut(),
    queued_io: false,
    pending_io: PendingIo {
        overlapped: ptr::null_mut(),
        handle: ptr::null_mut(),
        buffer: ptr::null_mut(),
        length: 0,
        is_write: false,
        error: 0,
        result: 0,
    },
};

const PREFIX_ABI_OFFSET: usize = 0x20000;
const STD_INPUT_HANDLE: u32 = (-10i32) as u32;
const STD_OUTPUT_HANDLE: u32 = (-11i32) as u32;
const STD_ERROR_HANDLE: u32 = (-12i32) as u32;
const ERROR_BROKEN_PIPE: u32 = 109;
const WAIT_OBJECT_0: u32 = 0;
const INFINITE: u32 = u32::MAX;

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
    (*ov).result_ext = result;
    (*ov).flags = pal_abi::FLAG_COMPLETED;
}

unsafe fn begin_io(
    ov: *mut Overlapped,
    handle: u64,
    buffer: *mut u8,
    length: usize,
    is_write: bool,
) {
    if ov.is_null() || (buffer.is_null() && length != 0) || length > u32::MAX as usize {
        complete_io(ov, 29, 0);
        return;
    }
    if length == 0 {
        complete_io(ov, 0, 0);
        return;
    }
    let state = state_ptr();
    let native_handle = match handle {
        0 => ptr::addr_of!((*state).input_handle).read(),
        1 => ptr::addr_of!((*state).output_handle).read(),
        _ => ptr::null_mut(),
    };
    let create_thread = ptr::addr_of!((*state).create_thread).read();
    let worker_thread = ptr::addr_of!((*state).worker_thread).read();
    let queued_io = ptr::addr_of!((*state).queued_io).read();
    let Some(create_thread) = create_thread else {
        complete_io(ov, 29, 0);
        return;
    };
    if native_handle.is_null() || !worker_thread.is_null() || queued_io {
        complete_io(ov, 29, 0);
        return;
    }

    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;
    ptr::addr_of_mut!((*state).pending_io).write(PendingIo {
        overlapped: ov,
        handle: native_handle,
        buffer,
        length: length as u32,
        is_write,
        error: 0,
        result: 0,
    });
    #[cfg(target_arch = "x86_64")]
    let start = run_io_worker as unsafe extern "win64" fn(*mut c_void) -> u32;
    #[cfg(target_arch = "aarch64")]
    let start = run_io_worker as unsafe extern "C" fn(*mut c_void) -> u32;
    let thread = create_thread(
        ptr::null_mut(),
        0,
        Some(start),
        ptr::null_mut(),
        0,
        ptr::null_mut(),
    );
    if thread.is_null() {
        ptr::addr_of_mut!((*state).pending_io).write(PendingIo {
            overlapped: ptr::null_mut(),
            handle: ptr::null_mut(),
            buffer: ptr::null_mut(),
            length: 0,
            is_write: false,
            error: 0,
            result: 0,
        });
        complete_io(ov, 29, 0);
    } else {
        ptr::addr_of_mut!((*state).worker_thread).write(thread);
        ptr::addr_of_mut!((*state).queued_io).write(true);
    }
}

unsafe fn perform_io_worker() -> u32 {
    let state = state_ptr();
    let request = ptr::addr_of!((*state).pending_io).read();
    let read_file = ptr::addr_of!((*state).read_file).read();
    let write_file = ptr::addr_of!((*state).write_file).read();
    let get_last_error = ptr::addr_of!((*state).get_last_error).read();
    let mut transferred = 0u32;
    let ok = if request.is_write {
        match write_file {
            Some(write_file) => {
                write_file(
                    request.handle,
                    request.buffer.cast(),
                    request.length,
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
                    request.handle,
                    request.buffer.cast(),
                    request.length,
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
        if !request.is_write && error == ERROR_BROKEN_PIPE {
            (0, 0)
        } else if error == ERROR_BROKEN_PIPE {
            (64, 0)
        } else {
            (29, 0)
        }
    };
    ptr::addr_of_mut!((*state).pending_io.error).write(error);
    ptr::addr_of_mut!((*state).pending_io.result).write(result);
    0
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn run_io_worker(_context: *mut c_void) -> u32 {
    perform_io_worker()
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn run_io_worker(_context: *mut c_void) -> u32 {
    perform_io_worker()
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

unsafe fn publish_pending_io() {
    let state = state_ptr();
    let pending = ptr::addr_of!((*state).pending_io).read();
    complete_io(pending.overlapped, pending.error, pending.result);
    ptr::addr_of_mut!((*state).worker_thread).write(ptr::null_mut());
    ptr::addr_of_mut!((*state).queued_io).write(false);
    ptr::addr_of_mut!((*state).pending_io).write(PendingIo {
        overlapped: ptr::null_mut(),
        handle: ptr::null_mut(),
        buffer: ptr::null_mut(),
        length: 0,
        is_write: false,
        error: 0,
        result: 0,
    });
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
    {
        return 12;
    }

    let Some(exit_process) = exit_process else {
        return 12;
    };
    let Some(get_std_handle) = get_std_handle else {
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

    {
        let state = state_mut();
        state.input_handle = input_handle;
        state.output_handle = output_handle;
        state.error_handle = error_handle;
        state.read_file = read_file;
        state.write_file = write_file;
        state.get_last_error = get_last_error;
        state.exit_process = Some(exit_process);
        state.create_thread = create_thread;
        state.wait_for_single_object = wait_for_single_object;
        state.close_handle = close_handle;
        state.worker_thread = ptr::null_mut();
        state.queued_io = false;
        state.pending_io = PendingIo {
            overlapped: ptr::null_mut(),
            handle: ptr::null_mut(),
            buffer: ptr::null_mut(),
            length: 0,
            is_write: false,
            error: 0,
            result: 0,
        };
    }
    ptr::write(
        payload_alloc.cast::<PalAbiTable>(),
        pal_abi_stubs::for_probe(
            probe_read,
            probe_write,
            exit_process,
        ),
    );
    loop {
        invoke_raw_entry(payload_code);
        let state = state_ptr();
        let queued_io = ptr::addr_of!((*state).queued_io).read();
        let thread = ptr::addr_of!((*state).worker_thread).read();
        if !queued_io {
            if !thread.is_null() {
                // A stray worker may still reference payload buffers; retain the mapping.
                return 16;
            }
            free(payload_alloc);
            return 0;
        }
        let wait = ptr::addr_of!((*state).wait_for_single_object).read();
        let close = ptr::addr_of!((*state).close_handle).read();
        let (Some(wait), Some(close)) = (wait, close) else {
            // A missing wait function cannot prove the worker stopped using payload buffers.
            return 16;
        };
        if thread.is_null() {
            free(payload_alloc);
            return 16;
        }
        if wait(thread, INFINITE) != WAIT_OBJECT_0 {
            close(thread);
            // A failed wait cannot prove the worker stopped using payload buffers.
            return 16;
        }
        close(thread);
        publish_pending_io();
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

    #[test]
    fn test_windows_platform_prefix_layout() {
        assert_eq!(PREFIX_ABI_OFFSET, 0x20000);
        assert_eq!(size_of::<WindowsPlatformHost>(), 48);
        assert_eq!(offset_of!(WindowsPlatformHost, load_library), 0);
        assert_eq!(offset_of!(WindowsPlatformHost, get_proc_address), 8);
        assert_eq!(offset_of!(WindowsPlatformHost, alloc), 16);
        assert_eq!(offset_of!(WindowsPlatformHost, free), 24);
        assert_eq!(offset_of!(WindowsPlatformHost, hello_payload), 32);
        assert_eq!(offset_of!(WindowsPlatformHost, hello_payload_size), 40);
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
    fn test_io_queue_tracks_submission_through_completion_publication() {
        unsafe {
            let state = state_ptr();
            (*state).input_handle = 1usize as *mut c_void;
            (*state).output_handle = 2usize as *mut c_void;
            (*state).worker_thread = ptr::null_mut();
            (*state).queued_io = false;
            (*state).create_thread = Some(mock_create_thread);
            let mut overlapped = Overlapped::default();
            let mut byte = 0u8;

            begin_io(
                &mut overlapped,
                1,
                &mut byte,
                1,
                true,
            );

            assert!((*state).queued_io);
            assert_eq!(overlapped.flags, 0);
            assert_eq!((*state).worker_thread, 7usize as *mut c_void);

            (*state).pending_io.result = 1;
            publish_pending_io();

            assert!(!(*state).queued_io);
            assert!((*state).worker_thread.is_null());
            assert_eq!(overlapped.flags, pal_abi::FLAG_COMPLETED);
            assert_eq!(overlapped.result_ext, 1);
            assert!(ptr::addr_of!((*state).pending_io.overlapped).read().is_null());

            (*state).create_thread = Some(fail_create_thread);
            begin_io(&mut overlapped, 1, &mut byte, 1, true);
            assert!(!(*state).queued_io);
            assert_eq!(overlapped.flags, pal_abi::FLAG_COMPLETED);
            assert_eq!(overlapped.error, 29);
        }
    }
}
