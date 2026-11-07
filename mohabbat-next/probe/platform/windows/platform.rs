#![no_std]

use core::ffi::c_void;
use core::panic::PanicInfo;
use core::ptr;

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

#[cfg(target_arch = "x86_64")]
type ExitProcessFn = unsafe extern "win64" fn(u32);
#[cfg(target_arch = "aarch64")]
type ExitProcessFn = unsafe extern "C" fn(u32);

#[cfg(target_arch = "x86_64")]
type ProbeWriteFn = unsafe extern "win64" fn(*const u8, usize) -> i64;
#[cfg(target_arch = "aarch64")]
type ProbeWriteFn = unsafe extern "C" fn(*const u8, usize) -> i64;

#[cfg(target_arch = "x86_64")]
type ProbeReadFn = unsafe extern "win64" fn(*mut u8, usize) -> i64;
#[cfg(target_arch = "aarch64")]
type ProbeReadFn = unsafe extern "C" fn(*mut u8, usize) -> i64;

#[cfg(target_arch = "x86_64")]
type ProbeExitFn = unsafe extern "win64" fn(i32);
#[cfg(target_arch = "aarch64")]
type ProbeExitFn = unsafe extern "C" fn(i32);

#[repr(C)]
pub struct ProbeDispatch {
    write: Option<ProbeWriteFn>,
    read: Option<ProbeReadFn>,
    exit: Option<ProbeExitFn>,
}

struct PlatformState {
    input_handle: *mut c_void,
    output_handle: *mut c_void,
    error_handle: *mut c_void,
    read_file: Option<ReadFileFn>,
    write_file: Option<WriteFileFn>,
    get_last_error: Option<GetLastErrorFn>,
    exit_process: Option<ExitProcessFn>,
    free_payload: Option<FreeFn>,
    payload_code: *mut c_void,
    previous_was_cr: bool,
}

impl PlatformState {
    const fn empty() -> Self {
        Self {
            input_handle: ptr::null_mut(),
            output_handle: ptr::null_mut(),
            error_handle: ptr::null_mut(),
            read_file: None,
            write_file: None,
            get_last_error: None,
            exit_process: None,
            free_payload: None,
            payload_code: ptr::null_mut(),
            previous_was_cr: false,
        }
    }
}

static mut STATE: PlatformState = PlatformState::empty();

const STD_INPUT_HANDLE: u32 = (-10i32) as u32;
const STD_OUTPUT_HANDLE: u32 = (-11i32) as u32;
const STD_ERROR_HANDLE: u32 = (-12i32) as u32;
const ERROR_BROKEN_PIPE: u32 = 109;

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

unsafe fn state_mut() -> &'static mut PlatformState {
    &mut *ptr::addr_of_mut!(STATE)
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

unsafe fn write_stderr(bytes: &[u8]) {
    let (write_file, error_handle) = {
        let state = state_mut();
        (state.write_file, state.error_handle)
    };
    if let Some(write_file) = write_file {
        let mut written = 0;
        write_file(
            error_handle,
            bytes.as_ptr().cast(),
            bytes.len() as u32,
            &mut written,
            ptr::null_mut(),
        );
    }
}

unsafe fn write_stdout_impl(buffer: *const u8, length: usize) -> i64 {
    if length > u32::MAX as usize {
        return -1;
    }
    let (write_file, output_handle) = {
        let state = state_mut();
        (state.write_file, state.output_handle)
    };
    let Some(write_file) = write_file else {
        return -1;
    };
    let mut written = 0;
    if write_file(
        output_handle,
        buffer.cast(),
        length as u32,
        &mut written,
        ptr::null_mut(),
    ) == 0
    {
        -1
    } else {
        written as i64
    }
}

unsafe fn read_stdin_impl(buffer: *mut u8, capacity: usize) -> i64 {
    if capacity == 0 || buffer.is_null() {
        return -1;
    }

    loop {
        let (read_file, input_handle) = {
            let state = state_mut();
            (state.read_file, state.input_handle)
        };
        let Some(read_file) = read_file else {
            return -1;
        };

        let mut byte = 0u8;
        let mut count = 0u32;
        if read_file(
            input_handle,
            (&mut byte as *mut u8).cast(),
            1,
            &mut count,
            ptr::null_mut(),
        ) == 0
        {
            let error = match state_mut().get_last_error {
                Some(get_last_error) => get_last_error(),
                None => return -1,
            };
            if error == ERROR_BROKEN_PIPE {
                return 0;
            }

            write_stderr(b"ReadFile(stdin) failed with Win32 error ");
            let mut digits = core::mem::MaybeUninit::<[u8; 10]>::uninit();
            let digits_ptr = digits.as_mut_ptr().cast::<u8>();
            let mut digit_count = 0usize;
            let mut value = error;
            loop {
                digits_ptr.add(digit_count).write(b'0' + (value % 10) as u8);
                digit_count += 1;
                value /= 10;
                if value == 0 || digit_count == 10 {
                    break;
                }
            }
            for index in 0..digit_count / 2 {
                let left = digits_ptr.add(index);
                let right = digits_ptr.add(digit_count - index - 1);
                let temporary = left.read();
                left.write(right.read());
                right.write(temporary);
            }
            write_stderr(core::slice::from_raw_parts(digits_ptr, digit_count));
            write_stderr(b"\r\n");
            return -1;
        }
        if count == 0 {
            return 0;
        }

        let state = state_mut();
        if state.previous_was_cr {
            state.previous_was_cr = false;
            if byte == b'\n' {
                continue;
            }
        }
        if byte == b'\r' {
            state.previous_was_cr = true;
            *buffer = b'\n';
        } else {
            *buffer = byte;
        }
        return 1;
    }
}

unsafe fn exit_process_impl(status: i32) {
    let (payload_code, free, exit) = {
        let state = state_mut();
        let payload_code = state.payload_code;
        state.payload_code = ptr::null_mut();
        (payload_code, state.free_payload, state.exit_process)
    };
    if !payload_code.is_null() {
        if let Some(free) = free {
            free(payload_code);
        }
    }
    if let Some(exit) = exit {
        exit(status as u32);
    }
    loop {}
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_write_stdout(buffer: *const u8, length: usize) -> i64 {
    write_stdout_impl(buffer, length)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_write_stdout(buffer: *const u8, length: usize) -> i64 {
    write_stdout_impl(buffer, length)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_read_stdin(buffer: *mut u8, capacity: usize) -> i64 {
    read_stdin_impl(buffer, capacity)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_read_stdin(buffer: *mut u8, capacity: usize) -> i64 {
    read_stdin_impl(buffer, capacity)
}

#[cfg(target_arch = "x86_64")]
unsafe extern "win64" fn probe_exit(status: i32) {
    exit_process_impl(status)
}

#[cfg(target_arch = "aarch64")]
unsafe extern "C" fn probe_exit(status: i32) {
    exit_process_impl(status)
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
    if get_std_handle.is_none()
        || read_file.is_none()
        || write_file.is_none()
        || get_last_error.is_none()
        || flush_instruction_cache.is_none()
        || exit_process.is_none()
        || get_current_process.is_none()
    {
        return 12;
    }

    let Some(get_std_handle) = get_std_handle else {
        return 12;
    };
    let input_handle = get_std_handle(STD_INPUT_HANDLE);
    let output_handle = get_std_handle(STD_OUTPUT_HANDLE);
    let error_handle = get_std_handle(STD_ERROR_HANDLE);
    if input_handle.is_null() || output_handle.is_null() || error_handle.is_null() {
        return 13;
    }

    let payload_code = alloc(host.hello_payload_size);
    if payload_code.is_null() {
        return 14;
    }
    let destination = payload_code.cast::<u8>();
    let mut index = 0usize;
    while index < host.hello_payload_size {
        let byte = ptr::read_volatile(host.hello_payload.add(index));
        ptr::write_volatile(destination.add(index), byte);
        index += 1;
    }

    let Some(get_current_process) = get_current_process else {
        free(payload_code);
        return 12;
    };
    let Some(flush_instruction_cache) = flush_instruction_cache else {
        free(payload_code);
        return 12;
    };
    let process_handle = get_current_process();
    if flush_instruction_cache(process_handle, payload_code, host.hello_payload_size) == 0 {
        free(payload_code);
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
        state.exit_process = exit_process;
        state.free_payload = Some(free);
        state.payload_code = payload_code;
    }
    let dispatch = ProbeDispatch {
        write: Some(probe_write_stdout),
        read: Some(probe_read_stdin),
        exit: Some(probe_exit),
    };
    let entry: ProbeEntryFn = core::mem::transmute(payload_code);
    entry(&dispatch);

    free(payload_code);
    state_mut().payload_code = ptr::null_mut();
    70
}

#[cfg(target_arch = "x86_64")]
type ProbeEntryFn = unsafe extern "win64" fn(*const ProbeDispatch);
#[cfg(target_arch = "aarch64")]
type ProbeEntryFn = unsafe extern "C" fn(*const ProbeDispatch);

#[cfg(target_arch = "x86_64")]
#[no_mangle]
pub unsafe extern "win64" fn windows_platform_entry(host: *const WindowsPlatformHost) -> i32 {
    match host.as_ref() {
        Some(host) => run_platform(host),
        None => 10,
    }
}

#[cfg(target_arch = "aarch64")]
#[no_mangle]
pub unsafe extern "C" fn windows_platform_entry(host: *const WindowsPlatformHost) -> i32 {
    match host.as_ref() {
        Some(host) => run_platform(host),
        None => 10,
    }
}

#[panic_handler]
fn panic(_info: &PanicInfo) -> ! {
    loop {}
}
