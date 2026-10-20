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

pub fn parse_u64_bytes(bytes: &[u8]) -> Option<u64> {
    let mut s = bytes;
    while let Some((&b' ', rest)) = s.split_first() {
        s = rest;
    }
    while let Some((&b' ', rest)) = s.split_last() {
        s = rest;
    }
    if s.is_empty() {
        return None;
    }
    let mut res: u64 = 0;
    for &b in s {
        if b < b'0' || b > b'9' {
            return None;
        }
        res = res.checked_mul(10)?.checked_add((b - b'0') as u64)?;
    }
    Some(res)
}

// ─── C stdlib intrinsics (binary link-unit, separate from lib.rs) ─────────────

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n { unsafe { *dest.add(i) = *src.add(i); } i += 1; }
    dest
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(s: *mut u8, c: i32, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n { unsafe { *s.add(i) = c as u8; } i += 1; }
    s
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    if dest < src as *mut u8 { unsafe { memcpy(dest, src, n) } }
    else {
        let mut i = n;
        while i > 0 { i -= 1; unsafe { *dest.add(i) = *src.add(i); } }
        dest
    }
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 {
    let mut i = 0;
    while i < n {
        let v1 = unsafe { *s1.add(i) }; let v2 = unsafe { *s2.add(i) };
        if v1 != v2 { return v1 as i32 - v2 as i32; }
        i += 1;
    }
    0
}
#[unsafe(no_mangle)]
pub unsafe extern "C" fn bcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 { unsafe { memcmp(s1, s2, n) } }
#[unsafe(no_mangle)]
pub unsafe extern "C" fn strlen(s: *const u8) -> usize {
    let mut n = 0; while unsafe { *s.add(n) } != 0 { n += 1; } n
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
        #[cfg(target_arch = "arm")]
        core::arch::asm!(
            "svc #0",
            in("r7") 248usize,  // SYS_exit_group
            in("r0") 200usize,
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

#[cfg(all(not(windows), not(target_family = "wasm")))]
#[unsafe(no_mangle)]
pub extern "C" fn rust_eh_personality() {}

// ─── Windows: Standalone Entry Points ──────────────────────────────────────────

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn mainCRTStartup() -> ! {
    unsafe { windows::run() }
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn main(_argc: i32, _argv: *const *const u8, _envp: *const *const u8) -> i32 {
    unsafe { windows::run() }
}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _Unwind_Resume() {}

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn rust_eh_personality() {}

/// Stub for code that expects a main() call but we are standalone.
#[cfg(windows)]
#[unsafe(no_mangle)]
pub extern "C" fn __main() {}

/// exit() stub used only if something transitively calls it.
/// Our windows::run() calls ExitProcess directly.
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
//
// Brot is linked as a static-PIE (ET_DYN with no PT_INTERP). Nothing loads it
// except the kernel, so nobody applies its dynamic relocations: _start must do
// it before anything reads a pointer-bearing static, vtable or GOT slot.
// _DYNAMIC and __ehdr_start are defined by the linker; both are addressed
// PC-relative so no relocation is needed to reach them. __ehdr_start is the
// load bias because the image is linked at base 0.

/// Applies the RELATIVE relocations of a static-PIE image. Must not use any
/// static data, vtable or panic path: it runs before relocations are applied.
///
/// # Safety
/// `dynamic` must point at this image's `_DYNAMIC` array and `base` at its
/// load address.
#[cfg(target_os = "linux")]
#[inline(never)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn brot_self_relocate(dynamic: *const usize, base: usize) {
    const DT_NULL: usize = 0;
    const DT_RELA: usize = 7;
    const DT_RELASZ: usize = 8;
    const DT_REL: usize = 17;
    const DT_RELSZ: usize = 18;
    #[cfg(target_arch = "x86_64")]
    const R_RELATIVE: usize = 8; // R_X86_64_RELATIVE
    #[cfg(target_arch = "aarch64")]
    const R_RELATIVE: usize = 1027; // R_AARCH64_RELATIVE
    #[cfg(target_arch = "arm")]
    const R_RELATIVE: usize = 23; // R_ARM_RELATIVE

    #[cfg(target_pointer_width = "64")]
    const TYPE_MASK: usize = 0xffff_ffff;
    #[cfg(target_pointer_width = "32")]
    const TYPE_MASK: usize = 0xff;

    let mut rela: usize = 0;
    let mut rela_sz: usize = 0;
    let mut rel: usize = 0;
    let mut rel_sz: usize = 0;

    let mut d = dynamic;
    loop {
        let tag = unsafe { *d };
        if tag == DT_NULL {
            break;
        }
        let val = unsafe { *d.add(1) };
        if tag == DT_RELA {
            rela = val;
        } else if tag == DT_RELASZ {
            rela_sz = val;
        } else if tag == DT_REL {
            rel = val;
        } else if tag == DT_RELSZ {
            rel_sz = val;
        }
        d = unsafe { d.add(2) };
    }

    // Elf{32,64}_Rela: offset, info, addend.
    if rela != 0 {
        let mut p = base.wrapping_add(rela) as *const usize;
        let end = base.wrapping_add(rela).wrapping_add(rela_sz) as *const usize;
        while p < end {
            let offset = unsafe { *p };
            let info = unsafe { *p.add(1) };
            let addend = unsafe { *p.add(2) };
            if info & TYPE_MASK == R_RELATIVE {
                unsafe { *(base.wrapping_add(offset) as *mut usize) = base.wrapping_add(addend) };
            }
            p = unsafe { p.add(3) };
        }
    }

    // Elf{32,64}_Rel: offset, info; the addend is stored in place.
    if rel != 0 {
        let mut p = base.wrapping_add(rel) as *const usize;
        let end = base.wrapping_add(rel).wrapping_add(rel_sz) as *const usize;
        while p < end {
            let offset = unsafe { *p };
            let info = unsafe { *p.add(1) };
            if info & TYPE_MASK == R_RELATIVE {
                let slot = base.wrapping_add(offset) as *mut usize;
                unsafe { *slot = (*slot).wrapping_add(base) };
            }
            p = unsafe { p.add(2) };
        }
    }
}

#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
#[unsafe(naked)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    core::arch::naked_asm!(
        "mov rbx, rsp", // Preserve original stack pointer for linux::run
        "xor rbp, rbp",
        "and rsp, -16",
        "lea rdi, [rip + _DYNAMIC]",
        "lea rsi, [rip + __ehdr_start]",
        "call {reloc}",
        "mov rdi, rbx", // Pass original stack pointer to linux::run
        "call {f}",
        reloc = sym brot_self_relocate,
        f = sym linux::run,
    );
}

#[cfg(all(target_os = "linux", target_arch = "aarch64"))]
#[unsafe(naked)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    core::arch::naked_asm!(
        "mov x19, sp", // Preserve original stack pointer for linux::run
        "mov x29, xzr",
        "mov x30, xzr",
        "adrp x0, _DYNAMIC",
        "add x0, x0, :lo12:_DYNAMIC",
        "adrp x1, __ehdr_start",
        "add x1, x1, :lo12:__ehdr_start",
        "bl {reloc}",
        "mov x0, x19", // Pass original stack pointer to linux::run
        "b {f}",
        reloc = sym brot_self_relocate,
        f = sym linux::run,
    );
}

#[cfg(all(target_os = "linux", target_arch = "arm"))]
#[unsafe(naked)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    core::arch::naked_asm!(
        "mov r4, sp", // Preserve original stack pointer for linux::run
        "mov r11, #0",
        "mov lr, #0",
        "ldr r0, 2f",
        "1: add r0, pc, r0", // pc reads as . + 8 in ARM state
        "ldr r1, 3f",
        "4: add r1, pc, r1",
        "bl {reloc}",
        "mov r0, r4", // Pass original stack pointer to linux::run
        "bl {f}",
        "2: .word _DYNAMIC - (1b + 8)",
        "3: .word __ehdr_start - (4b + 8)",
        reloc = sym brot_self_relocate,
        f = sym linux::run,
    );
}

// ─── macOS: main() called by dyld via LC_MAIN ────────────────────────────────
// envp is the POSIX extension third argument; dyld/libSystem passes it on macOS.

#[cfg(target_os = "macos")]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn main(argc: i32, argv: *const *const u8, envp: *const *const u8) -> i32 {
    unsafe { darwin::run(argc, argv, envp) }
}
