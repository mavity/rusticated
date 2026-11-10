#![cfg_attr(not(test), no_std)]

use core::ffi::{c_char, c_void};
#[cfg(not(test))]
use core::panic::PanicInfo;
use core::ptr;

#[path = "../../pal_abi.rs"]
mod pal_abi;
#[path = "../../pal_abi_stubs.rs"]
mod pal_abi_stubs;
use pal_abi::{
    Overlapped, PalAbiTable, ERROR_AGAIN, ERROR_BADF, ERROR_INVALID, ERROR_IO, ERROR_PIPE,
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
struct PendingIo {
    overlapped: *mut Overlapped,
    fd: i32,
    buffer: *mut c_void,
    length: usize,
    is_write: bool,
    error: u32,
    result: u64,
}

#[repr(C)]
struct PlatformState {
    read: Option<NativeReadFn>,
    write: Option<NativeWriteFn>,
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
    exit: None,
    errno_location: None,
    pthread_create: None,
    pthread_join: None,
    worker_thread: 0,
    queued_io: false,
    pending_io: PendingIo {
        overlapped: ptr::null_mut(),
        fd: 0,
        buffer: ptr::null_mut(),
        length: 0,
        is_write: false,
        error: 0,
        result: 0,
    },
    free: None,
    payload_alloc: ptr::null_mut(),
    payload_total_size: 0,
};

const PREFIX_ABI_OFFSET: usize = 0x20000;

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
    let fd = match (handle, is_write) {
        (0, false) => 0,
        (1, true) => 1,
        _ => {
            complete_io(ov, ERROR_BADF, 0);
            return;
        }
    };
    if length == 0 {
        complete_io(ov, 0, 0);
        return;
    }
    let state = state_ptr();
    let create = ptr::addr_of!((*state).pthread_create).read();
    let thread_active = ptr::addr_of!((*state).worker_thread).read() != 0;
    let queued_io = ptr::addr_of!((*state).queued_io).read();
    let Some(create) = create else {
        complete_io(ov, ERROR_IO, 0);
        return;
    };
    if thread_active || queued_io {
        complete_io(ov, ERROR_AGAIN, 0);
        return;
    }

    (*ov).flags = 0;
    (*ov).error = 0;
    (*ov).continued = 0;
    (*ov).result_ext = 0;
    ptr::addr_of_mut!((*state).pending_io).write(PendingIo {
        overlapped: ov,
        fd,
        buffer,
        length,
        is_write,
        error: 0,
        result: 0,
    });
    let mut thread = 0usize;
    let status = create(
        &mut thread,
        ptr::null(),
        Some(run_io_worker),
        ptr::null_mut(),
    );
    if status != 0 || thread == 0 {
        ptr::addr_of_mut!((*state).pending_io).write(PendingIo {
            overlapped: ptr::null_mut(),
            fd: 0,
            buffer: ptr::null_mut(),
            length: 0,
            is_write: false,
            error: 0,
            result: 0,
        });
        complete_io(ov, ERROR_AGAIN, 0);
    } else {
        ptr::addr_of_mut!((*state).worker_thread).write(thread);
        ptr::addr_of_mut!((*state).queued_io).write(true);
    }
}

unsafe extern "C" fn run_io_worker(_context: *mut c_void) -> *mut c_void {
    let state = state_ptr();
    let request = ptr::addr_of!((*state).pending_io).read();
    let read = ptr::addr_of!((*state).read).read();
    let write = ptr::addr_of!((*state).write).read();
    let errno_location = ptr::addr_of!((*state).errno_location).read();
    let (error, result) = loop {
        let result = if request.is_write {
            match write {
                Some(write) => write(request.fd, request.buffer.cast(), request.length),
                None => -1,
            }
        } else {
            match read {
                Some(read) => read(request.fd, request.buffer, request.length),
                None => -1,
            }
        };
        if result >= 0 {
            break (0, result as u64);
        }
        let errno = match errno_location {
            Some(errno_location) => *errno_location(),
            None => 0,
        };
        if errno == 4 {
            continue;
        }
        break (if errno == 32 { ERROR_PIPE } else { ERROR_IO }, 0);
    };
    ptr::addr_of_mut!((*state).pending_io.error).write(error);
    ptr::addr_of_mut!((*state).pending_io.result).write(result);
    ptr::null_mut()
}

unsafe fn publish_pending_io() {
    let state = state_ptr();
    let pending = ptr::addr_of!((*state).pending_io).read();
    complete_io(pending.overlapped, pending.error, pending.result);
    ptr::addr_of_mut!((*state).worker_thread).write(0);
    ptr::addr_of_mut!((*state).queued_io).write(false);
    ptr::addr_of_mut!((*state).pending_io).write(PendingIo {
        overlapped: ptr::null_mut(),
        fd: 0,
        buffer: ptr::null_mut(),
        length: 0,
        is_write: false,
        error: 0,
        result: 0,
    });
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
        Some(exit),
        Some(errno_location),
        Some(pthread_create),
        Some(pthread_join),
    ) = (
        read,
        write,
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
        state.exit = Some(exit);
        state.errno_location = Some(errno_location);
        state.pthread_create = Some(pthread_create);
        state.pthread_join = Some(pthread_join);
        state.worker_thread = 0;
        state.queued_io = false;
        state.pending_io = PendingIo {
            overlapped: ptr::null_mut(),
            fd: 0,
            buffer: ptr::null_mut(),
            length: 0,
            is_write: false,
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
    ptr::write(
        payload_alloc.cast::<PalAbiTable>(),
        pal_abi_stubs::for_probe(probe_read, probe_write, process_exit),
    );
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
                // A stray worker may still reference payload buffers; retain the mapping.
                return 5;
            }
            free(payload_alloc, total_payload_size);
            state_mut().payload_alloc = ptr::null_mut();
            return 0;
        }
        let join = ptr::addr_of!((*state).pthread_join).read();
        let Some(join) = join else {
            // A failed join cannot prove the worker stopped using payload buffers.
            return 5;
        };
        if thread == 0 {
            free(payload_alloc, total_payload_size);
            state_mut().payload_alloc = ptr::null_mut();
            return 5;
        }
        if join(thread, ptr::null_mut()) != 0 {
            // A failed join cannot prove the worker stopped using payload buffers.
            return 5;
        }
        publish_pending_io();
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
        write_result: isize,
        write_errno: i32,
        write_length: usize,
        write_calls: usize,
        errno: i32,
    }

    static mut TEST_IO: ScriptedIo = ScriptedIo {
        read_results: [0; 4],
        read_errnos: [0; 4],
        read_data: [0; 8],
        read_calls: 0,
        write_result: 0,
        write_errno: 0,
        write_length: 0,
        write_calls: 0,
        errno: 0,
    };

    unsafe extern "C" fn scripted_read(fd: i32, buffer: *mut c_void, length: usize) -> isize {
        assert_eq!(fd, 0);
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

    unsafe extern "C" fn scripted_write(fd: i32, buffer: *const c_void, length: usize) -> isize {
        assert_eq!(fd, 1);
        let io = &mut *ptr::addr_of_mut!(TEST_IO);
        io.write_calls += 1;
        io.write_length = length;
        io.errno = io.write_errno;
        io.write_result
    }

    unsafe extern "C" fn scripted_errno_location() -> *mut i32 {
        ptr::addr_of_mut!((*ptr::addr_of_mut!(TEST_IO)).errno)
    }

    unsafe fn reset_io_test_state() {
        ptr::addr_of_mut!(TEST_STATE).write(PlatformState {
            read: Some(scripted_read),
            write: Some(scripted_write),
            exit: None,
            errno_location: Some(scripted_errno_location),
            pthread_create: None,
            pthread_join: None,
            worker_thread: 0,
            queued_io: false,
            pending_io: PendingIo {
                overlapped: ptr::null_mut(),
                fd: 0,
                buffer: ptr::null_mut(),
                length: 0,
                is_write: false,
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
            write_result: 0,
            write_errno: 0,
            write_length: 0,
            write_calls: 0,
            errno: 0,
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
            assert_completion(&overlapped, ERROR_BADF, 0);
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
            (*ptr::addr_of_mut!(TEST_IO)).write_result = 2;
            overlapped = Overlapped {
                flags: 0,
                error: 0,
                continued: 17,
                result_ext: 23,
            };
            begin_io(&mut overlapped, 1, buffer.as_mut_ptr().cast(), buffer.len(), true);
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
            publish_pending_io();
            assert_completion(&overlapped, 0, 2);
            assert!(!ptr::addr_of!((*state).queued_io).read());
            assert_eq!(ptr::addr_of!((*state).worker_thread).read(), 0);
            assert!(ptr::addr_of!((*state).pending_io.overlapped)
                .read()
                .is_null());
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_calls, 1);
            assert_eq!((*ptr::addr_of!(TEST_IO)).write_length, 4);

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
            publish_pending_io();
            assert_completion(&overlapped, 0, 2);

            for (read_result, errno, expected_error, expected_result) in [
                (0, 0, 0, 0),
                (-1, 32, ERROR_PIPE, 0),
                (-1, 5, ERROR_IO, 0),
            ] {
                reset_io_test_state();
                let state = state_ptr();
                (*state).pthread_create = Some(mock_pthread_create);
                {
                    let io = &mut *ptr::addr_of_mut!(TEST_IO);
                    io.read_results[0] = read_result;
                    io.read_errnos[0] = errno;
                }
                let mut byte = 0u8;
                begin_io(&mut overlapped, 0, &mut byte, 1, false);
                run_io_worker(ptr::null_mut());
                publish_pending_io();
                assert_completion(&overlapped, expected_error, expected_result);
            }
        }
    }
}
