#![no_std]
#![no_main]
#![allow(dead_code)]
#![allow(non_snake_case)]

extern crate alloc;

mod allocator;
mod decompress;

#[cfg(windows)]
mod win32;

#[cfg(windows)]
mod load_win;

#[cfg(windows)]
mod windows;

#[cfg(target_os = "linux")]
mod linux;

#[cfg(target_os = "macos")]
mod darwin;

pub mod test_symbols;

// ─── Shared metadata section ──────────────────────────────────────────────────

#[repr(C, packed)]
#[derive(Clone, Copy)]
pub struct MohabbatMeta {
    pub magic: [u8; 8],
    pub pool_len: u64,
    pub washmhost_offset: u64,
    pub washmhost_len: u64,
    pub payload_offset: u64,
    pub payload_len: u64,
    pub reserved: u64,
}

#[cfg_attr(windows, unsafe(link_section = ".mohmeta"))]
#[cfg_attr(
    any(target_os = "linux", target_vendor = "apple"),
    unsafe(link_section = ".mohabbat_meta")
)]
#[used]
pub static mut META: MohabbatMeta = MohabbatMeta {
    magic: *b"MOHABBAT",
    pool_len: 0,
    washmhost_offset: 0,
    washmhost_len: 0,
    payload_offset: 0,
    payload_len: 0,
    reserved: 0,
};

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n {
        unsafe {
            *dest.add(i) = *src.add(i);
        }
        i += 1;
    }
    dest
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(s: *mut u8, c: i32, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n {
        unsafe {
            *s.add(i) = c as u8;
        }
        i += 1;
    }
    s
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    if dest < src as *mut u8 {
        unsafe {
            memcpy(dest, src, n)
        }
    } else {
        let mut i = n;
        while i > 0 {
            i -= 1;
            unsafe {
                *dest.add(i) = *src.add(i);
            }
        }
        dest
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 {
    let mut i = 0;
    while i < n {
        unsafe {
            let a = *s1.add(i);
            let b = *s2.add(i);
            if a != b {
                return a as i32 - b as i32;
            }
        }
        i += 1;
    }
    0
}

// ─── Panic handler ────────────────────────────────────────────────────────────

#[cfg(not(test))]
#[panic_handler]
fn panic(_info: &core::panic::PanicInfo<'_>) -> ! {
    #[cfg(windows)]
    unsafe {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn ExitProcess(uExitCode: u32) -> !;
        }
        ExitProcess(200)
    }
    #[cfg(target_os = "linux")]
    unsafe {
        #[cfg(target_arch = "x86_64")]
        core::arch::asm!(
            "syscall",
            in("rax") 231usize,  // SYS_exit_group
            in("rdi") 200usize,
            options(noreturn),
        );
        #[cfg(target_arch = "aarch64")]
        core::arch::asm!(
            "svc #0",
            in("x8") 94usize,   // SYS_exit_group
            in("x0") 200usize,
            options(noreturn),
        );
    }
    #[cfg(target_os = "macos")]
    unsafe {
        unsafe extern "C" {
            fn exit(code: i32) -> !;
        }
        exit(200)
    }
    #[cfg(not(any(windows, target_os = "linux", target_os = "macos")))]
    loop {}
}

// ─── Windows: called by MinGW crt2.o mainCRTStartup ─────────────────────────

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn main(
    _argc: i32,
    _argv: *const *const u8,
    _envp: *const *const u8,
) -> i32 {
    unsafe { windows::run() }
}

/// Global-constructor stub expected by MinGW crt2.o.
#[cfg(windows)]
#[unsafe(no_mangle)]
pub extern "C" fn __main() {}

/// exit() stub: crt2.o calls this after main() returns.
/// Never reached in practice because windows::run() calls ExitProcess,
/// but the linker requires the symbol to be defined.
#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn exit(code: i32) -> ! {
    #[link(name = "kernel32", kind = "raw-dylib")]
    unsafe extern "system" {
        fn ExitProcess(uExitCode: u32) -> !;
    }
    unsafe { ExitProcess(code as u32) }
}

/// Required by the LLVM x86_64 backend for Windows targets.
#[cfg(windows)]
#[unsafe(no_mangle)]
pub static _fltused: i32 = 0;

// ─── Windows stderr write helper used by windows.rs ──────────────────────────

#[cfg(windows)]
pub fn print_err(msg: &str) {
    unsafe {
        let h_err = crate::win32::Win32::System::Console::GetStdHandle(
            crate::win32::Win32::System::Console::STD_ERROR_HANDLE,
        );
        if !h_err.is_null() && h_err as isize != -1 {
            let mut written = 0u32;
            crate::win32::Win32::Storage::FileSystem::WriteFile(
                h_err,
                msg.as_ptr() as *const _,
                msg.len() as u32,
                &mut written,
                core::ptr::null_mut(),
            );
        }
    }
}

// ─── Linux: naked _start — no CRT startup objects ────────────────────────────

#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
#[unsafe(naked)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    core::arch::naked_asm!(
        "xor rbp, rbp",
        "and rsp, -16",
        "call {f}",
        f = sym linux::run,
    );
}

#[cfg(all(target_os = "linux", target_arch = "aarch64"))]
#[unsafe(naked)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    core::arch::naked_asm!(
        "mov x29, xzr",
        "mov x30, xzr",
        "b {f}",
        f = sym linux::run,
    );
}

// ─── macOS: main() called by dyld via LC_MAIN ────────────────────────────────

#[cfg(target_os = "macos")]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn main(argc: i32, argv: *const *const u8) -> i32 {
    unsafe { darwin::run(argc, argv) }
}
