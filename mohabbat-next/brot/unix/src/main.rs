#![cfg_attr(not(test), no_std)]
#![cfg_attr(not(test), no_main)]

extern crate alloc;

#[cfg(not(test))]
mod allocator;
mod decompress;

#[cfg(target_os = "linux")]
mod linux;

#[cfg(all(not(test), target_os = "macos"))]
mod darwin;

#[cfg(not(test))]
#[global_allocator]
static ALLOC: allocator::OsAllocator = allocator::OsAllocator;

// ─── Intrinsics ──────────────────────────────────────────────────────────────

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n {
        *dest.add(i) = *src.add(i);
        i += 1;
    }
    dest
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(s: *mut u8, c: i32, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n {
        *s.add(i) = c as u8;
        i += 1;
    }
    s
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    if (dest as usize) < (src as usize) {
        memcpy(dest, src, n)
    } else {
        let mut i = n;
        while i > 0 {
            i -= 1;
            *dest.add(i) = *src.add(i);
        }
        dest
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 {
    let mut i = 0;
    while i < n {
        let v1 = *s1.add(i);
        let v2 = *s2.add(i);
        if v1 != v2 {
            return v1 as i32 - v2 as i32;
        }
        i += 1;
    }
    0
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn bcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 {
    memcmp(s1, s2, n)
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn strlen(s: *const u8) -> usize {
    let mut n = 0;
    while *s.add(n) != 0 {
        n += 1;
    }
    n
}

// ─── Panic Handler ───────────────────────────────────────────────────────────

#[cfg(not(test))]
#[panic_handler]
fn panic(_info: &core::panic::PanicInfo<'_>) -> ! {
    #[cfg(target_os = "linux")]
    unsafe {
        linux::sys_exit(200);
    }
    #[cfg(target_os = "macos")]
    unsafe {
        #[link(name = "System")]
        unsafe extern "C" {
            fn exit(code: i32) -> !;
        }
        exit(200);
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    loop {}
}

#[cfg(not(test))]
#[unsafe(no_mangle)]
pub extern "C" fn rust_eh_personality() {}

// ─── Linux Self-Relocation & Entry Point ─────────────────────────────────────

#[cfg(all(not(test), target_os = "linux"))]
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
        let tag = *d;
        if tag == DT_NULL {
            break;
        }
        let val = *d.add(1);
        if tag == DT_RELA {
            rela = val;
        } else if tag == DT_RELASZ {
            rela_sz = val;
        } else if tag == DT_REL {
            rel = val;
        } else if tag == DT_RELSZ {
            rel_sz = val;
        }
        d = d.add(2);
    }

    if rela != 0 {
        let mut p = base.wrapping_add(rela) as *const usize;
        let end = base.wrapping_add(rela).wrapping_add(rela_sz) as *const usize;
        while p < end {
            let offset = *p;
            let info = *p.add(1);
            let addend = *p.add(2);
            if info & TYPE_MASK == R_RELATIVE {
                *(base.wrapping_add(offset) as *mut usize) = base.wrapping_add(addend);
            }
            p = p.add(3);
        }
    }

    if rel != 0 {
        let mut p = base.wrapping_add(rel) as *const usize;
        let end = base.wrapping_add(rel).wrapping_add(rel_sz) as *const usize;
        while p < end {
            let offset = *p;
            let info = *p.add(1);
            if info & TYPE_MASK == R_RELATIVE {
                let slot = base.wrapping_add(offset) as *mut usize;
                *slot = (*slot).wrapping_add(base);
            }
            p = p.add(2);
        }
    }
}

#[cfg(all(not(test), target_os = "linux", target_arch = "x86_64"))]
#[unsafe(naked)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    core::arch::naked_asm!(
        "mov rbx, rsp",
        "xor rbp, rbp",
        "and rsp, -16",
        "lea rdi, [rip + _DYNAMIC]",
        "lea rsi, [rip + __ehdr_start]",
        "call {reloc}",
        "mov rdi, rbx",
        "call {run}",
        reloc = sym brot_self_relocate,
        run = sym linux::run,
    );
}

#[cfg(all(not(test), target_os = "linux", target_arch = "aarch64"))]
#[unsafe(naked)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _start() -> ! {
    core::arch::naked_asm!(
        "mov x19, sp",
        "mov x29, xzr",
        "mov x30, xzr",
        "adrp x0, _DYNAMIC",
        "add x0, x0, :lo12:_DYNAMIC",
        "adrp x1, __ehdr_start",
        "add x1, x1, :lo12:__ehdr_start",
        "bl {reloc}",
        "mov x0, x19",
        "b {run}",
        reloc = sym brot_self_relocate,
        run = sym linux::run,
    );
}

// ─── macOS Entry Point ───────────────────────────────────────────────────────

#[cfg(all(not(test), target_os = "macos"))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn main(argc: i32, argv: *const *const u8, _envp: *const *const u8) -> i32 {
    darwin::run(argc, argv)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_brot_intrinsics() {
        unsafe {
            let src = b"mohabbat-next-brot-test\0";
            let mut dst = [0u8; 24];
            memcpy(dst.as_mut_ptr(), src.as_ptr(), src.len());
            assert_eq!(&dst, src);
            assert_eq!(memcmp(dst.as_ptr(), src.as_ptr(), src.len()), 0);
            assert_eq!(strlen(dst.as_ptr()), 23);

            memset(dst.as_mut_ptr(), b'A' as i32, 4);
            assert_eq!(&dst[..4], b"AAAA");

            memmove(dst.as_mut_ptr().add(2), dst.as_ptr(), 4);
            assert_eq!(&dst[..6], b"AAAAAA");
        }
    }

    #[test]
    fn test_zstd_decompress_invalid_rejects() {
        let mut out = [0u8; 64];
        assert!(decompress::decompress(b"not-a-valid-zstd-frame", &mut out).is_err());
    }
}
