#![no_std]

use core::mem::MaybeUninit;
use core::panic::PanicInfo;

#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
compile_error!("The probe payload supports only x86-64 and AArch64.");

#[cfg(target_arch = "x86_64")]
type WriteFn = unsafe extern "win64" fn(*const u8, usize) -> i64;
#[cfg(target_arch = "aarch64")]
type WriteFn = unsafe extern "C" fn(*const u8, usize) -> i64;

#[cfg(target_arch = "x86_64")]
type ReadFn = unsafe extern "win64" fn(*mut u8, usize) -> i64;
#[cfg(target_arch = "aarch64")]
type ReadFn = unsafe extern "C" fn(*mut u8, usize) -> i64;

#[cfg(target_arch = "x86_64")]
type ExitFn = unsafe extern "win64" fn(i32);
#[cfg(target_arch = "aarch64")]
type ExitFn = unsafe extern "C" fn(i32);

#[repr(C)]
pub struct ProbeDispatch {
    write: Option<WriteFn>,
    read: Option<ReadFn>,
    exit: Option<ExitFn>,
}

#[repr(C)]
pub struct ProbeGlobals {
    pub initialized: bool,
    pub lines_echoed: usize,
}

#[inline(always)]
unsafe fn slice_origin() -> *mut u8 {
    let addr: usize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "lea {0}, [rip + probe_entry]",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "adr {0}, probe_entry",
        out(reg) addr,
        options(pure, nomem, nostack),
    );
    addr as *mut u8
}

#[inline(always)]
unsafe fn abi() -> &'static ProbeDispatch {
    &*(slice_origin().sub(0x20000) as *const ProbeDispatch)
}

#[inline(always)]
unsafe fn globals() -> &'static mut ProbeGlobals {
    &mut *(slice_origin().sub(0x10000) as *mut ProbeGlobals)
}

const GREETING_PREFIX: &[u8] = b"Anonymous-memory probe ready (compiled ";
const BUILD_TIME: &str = match option_env!("BUILD_TIME") {
    Some(val) => val,
    None => "unknown",
};
const GREETING_SUFFIX: &[u8] = b"). Enter an empty line to exit.\n> ";
const ECHO_PREFIX: &[u8] = b"echo: ";
const NEWLINE: &[u8] = b"\n";
const PROMPT: &[u8] = b"> ";
const LINE_CAPACITY: usize = 256;

fn write_all(dispatch: &ProbeDispatch, bytes: &[u8]) -> bool {
    match dispatch.write {
        Some(write) => unsafe { write(bytes.as_ptr(), bytes.len()) == bytes.len() as i64 },
        None => false,
    }
}

fn terminate(dispatch: &ProbeDispatch, status: i32) -> ! {
    if let Some(exit) = dispatch.exit {
        unsafe {
            exit(status);
        }
    }
    loop {}
}

unsafe fn run() {
    let dispatch = abi();
    let g = globals();
    g.initialized = true;

    let mut line = MaybeUninit::<[u8; LINE_CAPACITY]>::uninit();
    let line_ptr = line.as_mut_ptr().cast::<u8>();
    if !write_all(dispatch, GREETING_PREFIX)
        || !write_all(dispatch, BUILD_TIME.as_bytes())
        || !write_all(dispatch, GREETING_SUFFIX)
    {
        terminate(dispatch, 3);
    }

    loop {
        let mut length = 0usize;
        loop {
            let mut byte = 0u8;
            let count = match dispatch.read {
                Some(read) => read(&mut byte, 1),
                None => terminate(dispatch, 3),
            };

            if count < 0 {
                terminate(dispatch, 3);
            }
            if count == 0 {
                terminate(dispatch, 0);
            }
            if byte == b'\n' {
                break;
            }
            if length == LINE_CAPACITY {
                terminate(dispatch, 2);
            }
            line_ptr.add(length).write(byte);
            length += 1;
        }

        if length > 0 && line_ptr.add(length - 1).read() == b'\r' {
            length -= 1;
        }
        if length == 0 {
            terminate(dispatch, 0);
        }
        let line = core::slice::from_raw_parts(line_ptr, length);
        if !write_all(dispatch, ECHO_PREFIX)
            || !write_all(dispatch, line)
            || !write_all(dispatch, NEWLINE)
            || !write_all(dispatch, PROMPT)
        {
            terminate(dispatch, 3);
        }
        g.lines_echoed = g.lines_echoed.wrapping_add(1);
    }
}

#[cfg(target_arch = "x86_64")]
#[link_section = ".text.probe_entry"]
#[no_mangle]
pub unsafe extern "win64" fn probe_entry() {
    run();
}

#[cfg(target_arch = "aarch64")]
#[link_section = ".text.probe_entry"]
#[no_mangle]
pub unsafe extern "C" fn probe_entry() {
    run();
}

#[panic_handler]
fn panic(_info: &PanicInfo) -> ! {
    loop {}
}
