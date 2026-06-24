#![no_std]
extern crate alloc;

pub mod allocator;
pub mod decompress;

// ── C stdlib intrinsics required by alloc/core on all non-WASM targets ────────
// These are defined here (lib) so BOTH the cdylib and the binary (via rlib)
// get them.  The binary no longer re-defines them in main.rs.

#[cfg(not(target_family = "wasm"))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n { unsafe { *dest.add(i) = *src.add(i); } i += 1; }
    dest
}

#[cfg(not(target_family = "wasm"))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memset(s: *mut u8, c: i32, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n { unsafe { *s.add(i) = c as u8; } i += 1; }
    s
}

#[cfg(not(target_family = "wasm"))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    if dest < src as *mut u8 { unsafe { memcpy(dest, src, n) } }
    else {
        let mut i = n;
        while i > 0 { i -= 1; unsafe { *dest.add(i) = *src.add(i); } }
        dest
    }
}

#[cfg(not(target_family = "wasm"))]
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

#[cfg(not(target_family = "wasm"))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn bcmp(s1: *const u8, s2: *const u8, n: usize) -> i32 { unsafe { memcmp(s1, s2, n) } }

#[cfg(not(target_family = "wasm"))]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn strlen(s: *const u8) -> usize {
    let mut n = 0;
    while unsafe { *s.add(n) } != 0 { n += 1; }
    n
}

// ── Windows cdylib entry point + exception stubs ──────────────────────────────

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "system" fn DllMainCRTStartup(_: *mut u8, _: u32, _: *mut u8) -> i32 { 1 }

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn _Unwind_Resume(_: *mut u8) -> ! { loop {} }

#[cfg(windows)]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn rust_eh_personality() {}

#[cfg(target_family = "wasm")]
#[unsafe(no_mangle)]
pub extern "C" fn run() {}

#[cfg(target_family = "wasm")]
#[unsafe(no_mangle)]
pub extern "C" fn guest_init() {}

#[cfg(target_family = "wasm")]
#[unsafe(no_mangle)]
pub extern "C" fn brot_alloc(size: u32) -> u32 {
    let layout = core::alloc::Layout::from_size_align(size as usize, 8).unwrap();
    unsafe { alloc::alloc::alloc(layout) as u32 }
}

#[cfg(target_family = "wasm")]
#[unsafe(no_mangle)]
pub extern "C" fn brot_decompress(in_ptr: u32, in_len: u32) -> u64 {
    let input = unsafe { core::slice::from_raw_parts(in_ptr as *const u8, in_len as usize) };
    let mut decompressed_pool = alloc::vec::Vec::new();
    
    // Use the decompress_to_writer from brot's decompress
    if decompress::decompress_to_writer(input, |chunk| {
        decompressed_pool.extend_from_slice(chunk);
    }).is_err() {
        return 0;
    }
    
    let out_len = decompressed_pool.len() as u64;
    // Shrink into boxed slice to "leak" it safely from dropping, returning the ptr
    let boxed = decompressed_pool.into_boxed_slice();
    let out_ptr = boxed.as_ptr() as u64;
    core::mem::forget(boxed);
    
    // Return (len << 32) | ptr
    (out_len << 32) | out_ptr
}

#[panic_handler]
fn panic(_info: &core::panic::PanicInfo) -> ! {
    #[cfg(target_family = "wasm")]
    core::arch::wasm32::unreachable();
    // HACK: looping forever is not an acceptable solution
    #[cfg(not(target_family = "wasm"))]
    loop {}
}
