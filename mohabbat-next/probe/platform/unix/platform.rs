#![cfg_attr(not(test), no_std)]

use core::ffi::{c_char, c_void};
#[cfg(not(test))]
use core::panic::PanicInfo;
use core::ptr;

#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
compile_error!("The Unix platform abstraction supports only x86-64 and AArch64.");

#[cfg(all(feature = "linux", feature = "darwin"))]
compile_error!("Select exactly one Unix platform.");

#[cfg(all(not(test), not(any(feature = "linux", feature = "darwin"))))]
compile_error!("Select the linux or darwin platform feature.");

#[cfg(target_arch = "x86_64")]
type PayloadWriteFn = unsafe extern "win64" fn(*const u8, usize) -> i64;
#[cfg(target_arch = "aarch64")]
type PayloadWriteFn = unsafe extern "C" fn(*const u8, usize) -> i64;

#[cfg(target_arch = "x86_64")]
type PayloadReadFn = unsafe extern "win64" fn(*mut u8, usize) -> i64;
#[cfg(target_arch = "aarch64")]
type PayloadReadFn = unsafe extern "C" fn(*mut u8, usize) -> i64;

#[cfg(target_arch = "x86_64")]
type PayloadExitFn = unsafe extern "win64" fn(i32);
#[cfg(target_arch = "aarch64")]
type PayloadExitFn = unsafe extern "C" fn(i32);

type LoadLibraryFn = unsafe extern "C" fn(*const c_char, i32) -> *mut c_void;
type GetProcAddressFn = unsafe extern "C" fn(*mut c_void, *const c_char) -> *mut c_void;
type AllocFn = unsafe extern "C" fn(usize) -> *mut c_void;
type FreeFn = unsafe extern "C" fn(*mut c_void, usize);
type FlushCacheFn = unsafe extern "C" fn(*mut c_void, usize);
type SetJitWriteProtectFn = unsafe extern "C" fn(i32);
type NativeReadFn = unsafe extern "C" fn(i32, *mut c_void, usize) -> isize;
type NativeWriteFn = unsafe extern "C" fn(i32, *const c_void, usize) -> isize;
type NativeExitFn = unsafe extern "C" fn(i32) -> !;

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
struct ProbeDispatch {
    write: Option<PayloadWriteFn>,
    read: Option<PayloadReadFn>,
    exit: Option<PayloadExitFn>,
}

#[repr(C)]
struct PlatformState {
    read: Option<NativeReadFn>,
    write: Option<NativeWriteFn>,
    exit: Option<NativeExitFn>,
    free: Option<FreeFn>,
    payload_alloc: *mut c_void,
    payload_total_size: usize,
}

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
unsafe fn state_mut() -> &'static mut PlatformState {
    &mut *(slice_origin().sub(0x10000) as *mut PlatformState)
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

macro_rules! payload_callback {
    ($name:ident($($argument:ident: $type:ty),*) -> $result:ty $body:block) => {
        #[cfg(target_arch = "x86_64")]
        unsafe extern "win64" fn $name($($argument: $type),*) -> $result $body

        #[cfg(target_arch = "aarch64")]
        unsafe extern "C" fn $name($($argument: $type),*) -> $result $body
    };
}

payload_callback!(write_stdout(buffer: *const u8, length: usize) -> i64 {
    if length == 0 {
        return 0;
    }
    if buffer.is_null() && length != 0 {
        return -1;
    }
    let bytes = core::slice::from_raw_parts(buffer, length);
    if write_fd(1, bytes) {
        length as i64
    } else {
        -1
    }
});

payload_callback!(read_stdin(buffer: *mut u8, capacity: usize) -> i64 {
    if buffer.is_null() || capacity == 0 {
        return -1;
    }
    let Some(read) = state_mut().read else {
        return -1;
    };
    read(0, buffer.cast(), capacity) as i64
});

payload_callback!(exit_process(status: i32) -> () {
    if let Some(exit) = state_mut().exit {
        exit(status);
    }
    loop {}
});

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
    let (Some(read), Some(write), Some(exit)) = (read, write, exit) else {
        write_fd(2, b"Unix platform: required libc symbol lookup failed.\n");
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
    ptr::write(
        payload_alloc.cast::<ProbeDispatch>(),
        ProbeDispatch {
            write: Some(write_stdout),
            read: Some(read_stdin),
            exit: Some(exit_process),
        },
    );

    let payload_code = alloc_bytes.add(PREFIX_ABI_OFFSET).cast::<c_void>();
    let payload_bytes = payload_code.cast::<u8>();
    let mut offset = 0;
    while offset < host.hello_payload_size {
        let byte = host.hello_payload.add(offset).read_volatile();
        payload_bytes.add(offset).write_volatile(byte);
        offset += 1;
    }
    #[cfg(feature = "darwin")]
    set_jit_write_protect(1);
    flush_cache(payload_code, host.hello_payload_size);

    {
        let state = state_mut();
        state.read = Some(read);
        state.write = Some(write);
        state.exit = Some(exit);
        state.free = Some(free);
        state.payload_alloc = payload_alloc;
        state.payload_total_size = total_payload_size;
    }

    #[cfg(target_arch = "x86_64")]
    let entry: unsafe extern "win64" fn() = core::mem::transmute(payload_code);
    #[cfg(target_arch = "aarch64")]
    let entry: unsafe extern "C" fn() = core::mem::transmute(payload_code);
    entry();
    70
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
}
