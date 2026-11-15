//! Mohabbat-Next Movable Cranelift Runtime Slice (`#![no_std]` + `alloc`).
//!
//! Raw entry label `runtime_code_start` is the first machine instruction at byte
//! offset `0x00000` (`.text.entry`).
//! It reads the host `PalAbiTable` at `-0x20000` (`__abi_base`) and its own
//! `RuntimeGlobals` at `-0x10000` (`__globals_base`) via PC-relative addressing.

#![cfg_attr(not(test), no_std)]
#![allow(unsafe_op_in_unsafe_fn)]

extern crate alloc;

pub mod abi;
pub mod allocator;
pub mod jit;
pub mod wasm_env;
pub mod wasmtime_pal;

#[allow(unused_imports)]
use crate::abi::abi;
use crate::abi::globals;
#[cfg(not(test))]
use crate::allocator::PalBumpAllocator;
#[cfg(not(test))]
use core::panic::PanicInfo;

#[inline(always)]
pub(crate) unsafe fn slice_memcpy(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n {
        *dest.add(i) = *src.add(i);
        i += 1;
    }
    dest
}

#[inline(always)]
pub(crate) unsafe fn slice_memset(dest: *mut u8, value: i32, n: usize) -> *mut u8 {
    let mut i = 0;
    while i < n {
        *dest.add(i) = value as u8;
        i += 1;
    }
    dest
}

#[inline(always)]
pub(crate) unsafe fn slice_memmove(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    if (dest as usize) < (src as usize) {
        let mut i = 0;
        while i < n {
            *dest.add(i) = *src.add(i);
            i += 1;
        }
    } else {
        let mut i = n;
        while i > 0 {
            i -= 1;
            *dest.add(i) = *src.add(i);
        }
    }
    dest
}

#[inline(always)]
pub(crate) unsafe fn slice_memcmp(lhs: *const u8, rhs: *const u8, n: usize) -> i32 {
    let mut i = 0;
    while i < n {
        let left = *lhs.add(i);
        let right = *rhs.add(i);
        if left != right {
            return left as i32 - right as i32;
        }
        i += 1;
    }
    0
}

#[inline(always)]
pub(crate) unsafe fn slice_bcmp(lhs: *const u8, rhs: *const u8, n: usize) -> i32 {
    slice_memcmp(lhs, rhs, n)
}

#[cfg(not(test))]
#[unsafe(no_mangle)]
unsafe extern "C" fn memcpy(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    slice_memcpy(dest, src, n)
}

#[cfg(not(test))]
#[unsafe(no_mangle)]
unsafe extern "C" fn memset(dest: *mut u8, value: i32, n: usize) -> *mut u8 {
    slice_memset(dest, value, n)
}

#[cfg(not(test))]
#[unsafe(no_mangle)]
unsafe extern "C" fn memmove(dest: *mut u8, src: *const u8, n: usize) -> *mut u8 {
    slice_memmove(dest, src, n)
}

#[cfg(not(test))]
#[unsafe(no_mangle)]
unsafe extern "C" fn memcmp(lhs: *const u8, rhs: *const u8, n: usize) -> i32 {
    slice_memcmp(lhs, rhs, n)
}

#[cfg(not(test))]
#[unsafe(no_mangle)]
unsafe extern "C" fn bcmp(lhs: *const u8, rhs: *const u8, n: usize) -> i32 {
    slice_bcmp(lhs, rhs, n)
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn get_bump_empty_chunk() -> *mut u8 {
    globals().bump_empty_chunk
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn get_hashbrown_empty_tags() -> *const u8 {
    globals().hashbrown_empty_tags
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn rusticated_debug_trampoline(v: i32) {
    (crate::abi::abi().rusticated_debug)(v);
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn get_heap_rc_int() -> *const u8 {
    globals().heap_rc_int
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn get_heap_rc_float() -> *const u8 {
    globals().heap_rc_float
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn get_heap_rc_int_int() -> *const u8 {
    globals().heap_rc_int_int
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn get_heap_ty_i64_i64() -> *const u16 {
    globals().heap_ty_i64_i64
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn get_heap_ty_i8x16() -> *const u16 {
    globals().heap_ty_i8x16
}

#[cfg(not(test))]
#[global_allocator]
static ALLOC: PalBumpAllocator = PalBumpAllocator;

#[cfg(not(test))]
#[panic_handler]
fn panic(info: &PanicInfo<'_>) -> ! {
    let g = unsafe { globals() };
    let (file, line) = match info.location() {
        Some(loc) => (loc.file(), loc.line()),
        None => ("<unknown>", 0),
    };
    let msg = alloc::format!(
        "runtime panic: {} at {}:{} (heap_base={:#x}, used={}, cap={})",
        info.message(),
        file,
        line,
        g.heap_base as usize,
        g.heap_used,
        g.heap_cap,
    );
    unsafe {
        (abi().host_panic)(msg.as_ptr(), msg.len());
    }
}

#[cfg(target_arch = "x86_64")]
#[cfg(not(test))]
#[inline(never)]
unsafe extern "win64" fn runtime_activation() {
    slice_entry_inner();
}

#[cfg(target_arch = "aarch64")]
#[cfg(not(test))]
#[inline(never)]
unsafe extern "C" fn runtime_activation() {
    slice_entry_inner();
}

#[cfg(all(not(test), target_arch = "x86_64"))]
core::arch::global_asm!(
    ".section .text.entry,\"ax\"",
    ".globl runtime_code_start",
    "runtime_code_start:",
    "jmp {activation}",
    activation = sym runtime_activation,
);

#[cfg(all(not(test), target_arch = "aarch64"))]
core::arch::global_asm!(
    ".section .text.entry,\"ax\"",
    ".globl runtime_code_start",
    "runtime_code_start:",
    "b {activation}",
    activation = sym runtime_activation,
);

unsafe fn init_overlay_globals() {
    let g = globals();
    if g.bump_empty_chunk.is_null() {
        let chunk = alloc::alloc::alloc(core::alloc::Layout::from_size_align(48, 16).unwrap());
        core::ptr::write(chunk.cast::<*mut u8>(), chunk);
        core::ptr::write(
            chunk.add(8).cast::<core::alloc::Layout>(),
            core::alloc::Layout::from_size_align(48, 16).unwrap(),
        );
        core::ptr::write(chunk.add(24).cast::<*mut u8>(), chunk);
        core::ptr::write(chunk.add(32).cast::<*mut u8>(), chunk);
        core::ptr::write(chunk.add(40).cast::<usize>(), 0);
        g.bump_empty_chunk = chunk;
    }
    if g.hashbrown_empty_tags.is_null() {
        let tags = alloc::alloc::alloc(core::alloc::Layout::from_size_align(64, 16).unwrap());
        core::ptr::write_bytes(tags, 0xff, 64);
        g.hashbrown_empty_tags = tags;
    }
    if g.heap_rc_int.is_null() {
        let rc_int = alloc::boxed::Box::leak(alloc::boxed::Box::new([0u8; 1]));
        g.heap_rc_int = rc_int.as_ptr();
        let rc_float = alloc::boxed::Box::leak(alloc::boxed::Box::new([1u8; 1]));
        g.heap_rc_float = rc_float.as_ptr();
        let rc_int_int = alloc::boxed::Box::leak(alloc::boxed::Box::new([0u8; 2]));
        g.heap_rc_int_int = rc_int_int.as_ptr();
        let ty_i64_i64 = alloc::boxed::Box::leak(alloc::boxed::Box::new([0x0073u16; 2]));
        g.heap_ty_i64_i64 = ty_i64_i64.as_ptr();
        let ty_i8x16 = alloc::boxed::Box::leak(alloc::boxed::Box::new([0x00e0u16; 1]));
        g.heap_ty_i8x16 = ty_i8x16.as_ptr();
    }
}

#[cfg(not(test))]
unsafe fn relocate_self() {
    let origin = abi::slice_origin() as usize;
    let end = abi::slice_end();
    let magic = (end as *const u32).read_unaligned();
    if magic != 0x5245_4C4F {
        return;
    }
    let count = (end.add(4) as *const u32).read_unaligned() as usize;
    let offsets = end.add(8) as *const u32;
    for i in 0..count {
        let off = offsets.add(i).read_unaligned() as usize;
        let slot = (origin + off) as *mut usize;
        slot.write_unaligned(slot.read_unaligned().wrapping_add(origin));
    }
}

#[cfg(not(test))]
#[inline(always)]
unsafe fn slice_entry_inner() {
    let g = globals();
    if !g.initialized {
        relocate_self();
        init_overlay_globals();
        jit::compile_and_initialize_wasm();
        g.initialized = true;
    }
    jit::reenter_wasm();
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::abi::{FLAG_COMPLETED, Overlapped, PalAbiTable, ProcessSpawnOverlapped, RuntimeGlobals};
    use core::mem::{offset_of, size_of};

    #[test]
    fn test_overlapped_and_pal_abi_layouts() {
        assert_eq!(size_of::<Overlapped>(), 24);
        assert_eq!(offset_of!(Overlapped, flags), 0);
        assert_eq!(offset_of!(Overlapped, error), 4);
        assert_eq!(offset_of!(Overlapped, continued), 8);
        assert_eq!(offset_of!(Overlapped, result_ext), 16);
        assert_eq!(FLAG_COMPLETED, 1);

        assert_eq!(size_of::<ProcessSpawnOverlapped>(), 64);
        assert_eq!(offset_of!(ProcessSpawnOverlapped, header), 0);
        assert_eq!(offset_of!(ProcessSpawnOverlapped, child_handle), 24);
        assert_eq!(offset_of!(ProcessSpawnOverlapped, pty_handle), 32);
        assert_eq!(offset_of!(ProcessSpawnOverlapped, in_handle), 40);
        assert_eq!(offset_of!(ProcessSpawnOverlapped, out_handle), 48);
        assert_eq!(offset_of!(ProcessSpawnOverlapped, err_handle), 56);

        // 2 boot fields + 45 function pointers = 47 * 8 = 376 bytes
        assert_eq!(size_of::<PalAbiTable>(), 47 * 8);
        assert!(size_of::<RuntimeGlobals>() <= 0x10000);
    }

    pub(crate) fn init_test_env() {
        unsafe {
            init_overlay_globals();
        }
    }

    #[test]
    fn test_wasmtime_engine_smoke() {
        init_test_env();
        let engine = jit::build_engine().expect("build wasmtime engine");
        // Minimal valid WASM module:
        //   (module
        //     (memory (export "memory") 1)
        //     (func (export "run")
        //       (i32.store (i32.const 0)
        //         (i32.add (i32.load (i32.const 0)) (i32.const 7)))))
        let wasm_bytes: &[u8] = &[
            0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic + version
            0x01, 0x04, 0x01, 0x60, 0x00, 0x00,             // Type section: 1 type () -> ()
            0x03, 0x02, 0x01, 0x00,                         // Function section: func 0 has type 0
            0x05, 0x03, 0x01, 0x00, 0x01,                   // Memory section: 1 page min
            0x07, 0x07, 0x01, 0x03, b'r', b'u', b'n', 0x00, 0x00, // Export "run" -> func 0
            0x0a, 0x11, 0x01,                               // Code section: size 17, 1 body
            0x0f, 0x00,                                     // body size 15, 0 locals
            0x41, 0x00,                                     // i32.const 0
            0x41, 0x00,                                     // i32.const 0
            0x28, 0x02, 0x00,                               // i32.load align=4 offset=0
            0x41, 0x07,                                     // i32.const 7
            0x6a,                                           // i32.add
            0x36, 0x02, 0x00,                               // i32.store align=4 offset=0
            0x0b,                                           // end
        ];
        let module = wasmtime::Module::new(&engine, wasm_bytes).expect("compile wasm module");
        let mut store = wasmtime::Store::new(&engine, ());
        let instance = wasmtime::Instance::new(&mut store, &module, &[]).expect("instantiate");
        let run = instance
            .get_typed_func::<(), ()>(&mut store, "run")
            .expect("get run export");
        run.call(&mut store, ()).expect("call run");
    }

    #[test]
    fn test_memory_intrinsics() {
        let mut buf = [0u8; 32];
        unsafe {
            slice_memset(buf.as_mut_ptr(), 0xaa, 16);
            assert_eq!(&buf[..16], &[0xaa; 16]);
            assert_eq!(&buf[16..], &[0; 16]);

            let src = [1u8, 2, 3, 4, 5, 6, 7, 8];
            slice_memcpy(buf.as_mut_ptr(), src.as_ptr(), 8);
            assert_eq!(&buf[..8], &[1, 2, 3, 4, 5, 6, 7, 8]);

            // Forward and backward overlapping memmove
            let mut move_buf = [1u8, 2, 3, 4, 5, 6, 7, 8];
            slice_memmove(move_buf.as_mut_ptr().add(2), move_buf.as_ptr(), 4);
            assert_eq!(&move_buf, &[1, 2, 1, 2, 3, 4, 7, 8]);

            let mut move_buf2 = [1u8, 2, 3, 4, 5, 6, 7, 8];
            slice_memmove(move_buf2.as_mut_ptr(), move_buf2.as_ptr().add(2), 4);
            assert_eq!(&move_buf2[..4], &[3, 4, 5, 6]);

            assert_eq!(slice_memcmp(b"abc".as_ptr(), b"abc".as_ptr(), 3), 0);
            assert!(slice_memcmp(b"abc".as_ptr(), b"abd".as_ptr(), 3) < 0);
            assert!(slice_memcmp(b"abd".as_ptr(), b"abc".as_ptr(), 3) > 0);
            assert_eq!(slice_bcmp(b"xyz".as_ptr(), b"xyz".as_ptr(), 3), 0);
        }
    }

    #[test]
    fn test_malformed_wasm_fails_cleanly() {
        init_test_env();
        let engine = jit::build_engine().expect("build wasmtime engine");
        let malformed_wasm = [0x00, 0x00, 0x00];
        assert!(wasmtime::Module::new(&engine, &malformed_wasm).is_err());
    }

    #[test]
    fn test_linker_env_import_and_memory_grow() {
        use core::sync::atomic::{AtomicI32, Ordering};
        static LAST_DEBUG_VAL: AtomicI32 = AtomicI32::new(0);
        #[cfg(target_arch = "x86_64")]
        unsafe extern "win64" fn test_debug(val: i32) {
            LAST_DEBUG_VAL.store(val, Ordering::SeqCst);
        }
        #[cfg(not(target_arch = "x86_64"))]
        unsafe extern "C" fn test_debug(val: i32) {
            LAST_DEBUG_VAL.store(val, Ordering::SeqCst);
        }

        init_test_env();
        unsafe {
            abi::test_abi_mut().rusticated_debug = test_debug;
        }
        let engine = jit::build_engine().expect("build wasmtime engine");
        let wasm_bytes: &[u8] = &[
            0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
            // Type section: 2 types: (i32)->(), ()->()
            0x01, 0x08, 0x02, 0x60, 0x01, 0x7f, 0x00, 0x60, 0x00, 0x00,
            // Import section: env.rusticated_debug : type 0
            0x02, 0x18, 0x01,
            0x03, b'e', b'n', b'v',
            0x10, b'r', b'u', b's', b't', b'i', b'c', b'a', b't', b'e', b'd', b'_', b'd', b'e', b'b', b'u', b'g',
            0x00, 0x00,
            // Function section: func 1 -> type 1
            0x03, 0x02, 0x01, 0x01,
            // Memory section: 1 page min
            0x05, 0x03, 0x01, 0x00, 0x01,
            // Export section: "memory" -> mem 0, "run" -> func 1
            0x07, 0x10, 0x02,
            0x06, b'm', b'e', b'm', b'o', b'r', b'y', 0x02, 0x00,
            0x03, b'r', b'u', b'n', 0x00, 0x01,
            // Code section: memory.grow(1); drop; call 0(42);
            0x0a, 0x0d, 0x01, 0x0b, 0x00,
            0x41, 0x01, 0x40, 0x00, 0x1a,
            0x41, 0x2a, 0x10, 0x00,
            0x0b,
        ];
        let module = wasmtime::Module::new(&engine, wasm_bytes).expect("compile wasm module");
        let mut store = wasmtime::Store::new(&engine, ());
        let mut linker = wasmtime::Linker::new(&engine);
        wasm_env::register_env_imports(&mut linker).expect("register env imports");
        let instance = linker.instantiate(&mut store, &module).expect("instantiate");
        let run = instance
            .get_typed_func::<(), ()>(&mut store, "run")
            .expect("get run export");
        run.call(&mut store, ()).expect("call run");
        unsafe {
            assert_eq!(abi::globals().guest_mem_len, 2 * 65536);
        }
        assert_eq!(LAST_DEBUG_VAL.load(Ordering::SeqCst), 42);
    }
}
