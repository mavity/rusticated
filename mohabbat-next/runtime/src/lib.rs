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
fn panic(_info: &PanicInfo<'_>) -> ! {
    unsafe {
        (abi().host_panic)(b"runtime panic".as_ptr(), 13);
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

#[cfg(not(test))]
#[inline(always)]
unsafe fn slice_entry_inner() {
    let g = globals();
    if !g.initialized {
        jit::compile_and_initialize_wasm();
        g.initialized = true;
    }
    if let Some(run_fn) = g.guest_run_fn {
        run_fn();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::abi::{FLAG_COMPLETED, Overlapped, PalAbiTable, ProcessSpawnOverlapped, RuntimeGlobals};
    use core::mem::{offset_of, size_of};
    use cranelift_codegen::Context;
    use cranelift_codegen::ir::types::I32;
    use cranelift_codegen::ir::{AbiParam, Function, InstBuilder, Signature, UserFuncName};
    use cranelift_codegen::isa::CallConv;
    use cranelift_frontend::{FunctionBuilder, FunctionBuilderContext};

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

    fn init_test_env() {
        unsafe {
            let g = globals();
            if g.bump_empty_chunk.is_null() {
                let chunk = alloc::alloc::alloc(core::alloc::Layout::from_size_align(48, 16).unwrap());
                core::ptr::write(chunk.cast::<*mut u8>(), chunk);
                core::ptr::write(chunk.add(8).cast::<usize>(), 48);
                core::ptr::write(chunk.add(16).cast::<usize>(), 16);
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
    }

    #[test]
    fn test_cranelift_target_isa_and_codegen() {
        init_test_env();
        let isa = jit::create_target_isa();
        let mut sig = Signature::new(CallConv::SystemV);
        sig.params.push(AbiParam::new(I32));
        sig.returns.push(AbiParam::new(I32));

        let mut ctx = Context::new();
        ctx.func = Function::with_name_signature(UserFuncName::user(0, 0), sig);

        let mut fb_ctx = FunctionBuilderContext::new();
        {
            let mut builder = FunctionBuilder::new(&mut ctx.func, &mut fb_ctx);
            let block = builder.create_block();
            builder.append_block_params_for_function_params(block);
            builder.switch_to_block(block);
            builder.seal_block(block);
            let x = builder.block_params(block)[0];
            let y = builder.ins().iadd_imm_s(x, 100);
            builder.ins().return_(&[y]);
            builder.finalize(jit::default_frontend_config());
        }

        let mut ctrl = Default::default();
        let compiled = ctx.compile(&isa, &mut ctrl).expect("compile simple function");
        assert!(!compiled.code_buffer().is_empty());
    }

    #[test]
    fn test_in_memory_wasm_jit_cold_and_warm_retrigger() {
        init_test_env();
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

        let isa = jit::create_target_isa();
        let call_conv = jit::default_call_conv();

        let mut types = alloc::vec::Vec::new();
        let mut func_type_indices = alloc::vec::Vec::new();
        let mut code_bodies = alloc::vec::Vec::new();

        for payload in wasmparser::Parser::new(0).parse_all(wasm_bytes) {
            match payload.expect("valid wasm payload") {
                wasmparser::Payload::TypeSection(r) => {
                    for ft in r.into_iter_err_on_gc_types() {
                        types.push(ft.unwrap());
                    }
                }
                wasmparser::Payload::FunctionSection(r) => {
                    for idx in r {
                        func_type_indices.push(idx.unwrap());
                    }
                }
                wasmparser::Payload::CodeSectionEntry(body) => {
                    code_bodies.push(body);
                }
                _ => {}
            }
        }

        assert_eq!(code_bodies.len(), 1);
        let mut guest_mem = alloc::vec![0u8; 65536];
        let mut sig = Signature::new(call_conv);
        let _ = &mut sig;
        let mut ctx = Context::new();
        ctx.func = Function::with_name_signature(UserFuncName::user(0, 0), Signature::new(call_conv));
        let mut fb_ctx = FunctionBuilderContext::new();
        jit::translate_wasm_body_with_mem(
            &code_bodies[0],
            &mut ctx.func,
            &mut fb_ctx,
            jit::default_frontend_config(),
            &types,
            &func_type_indices,
            call_conv,
            guest_mem.as_mut_ptr(),
            guest_mem.len(),
        )
        .expect("translate wasm body");

        let mut ctrl = Default::default();
        let compiled = ctx.compile(&isa, &mut ctrl).expect("compile wasm body to native machine code");
        assert!(!compiled.code_buffer().is_empty());
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
    fn test_target_isa_flags_schema() {
        use cranelift_codegen::isa::TargetIsa;
        let isa = jit::create_target_isa();
        let flags = isa.flags();
        assert_eq!(flags.probestack_size_log2(), 12);
        assert_eq!(flags.opt_level(), cranelift_codegen::settings::OptLevel::None);
        assert_eq!(
            flags.regalloc_algorithm(),
            cranelift_codegen::settings::RegallocAlgorithm::SinglePass
        );
        assert_eq!(
            flags.libcall_call_conv(),
            cranelift_codegen::settings::LibcallCallConv::IsaDefault
        );
    }

    #[test]
    fn test_translate_malformed_wasm_fails_cleanly() {
        init_test_env();
        let mut func = Function::with_name_signature(
            UserFuncName::user(0, 0),
            Signature::new(jit::default_call_conv()),
        );
        let mut fb_ctx = FunctionBuilderContext::new();
        let types = alloc::vec::Vec::new();
        let func_type_indices = alloc::vec::Vec::new();
        let mut guest_mem = [0u8; 64];

        // Truncated/corrupted bytecode (not a valid function body)
        let malformed_wasm = [0x00, 0x00, 0x00];
        let mut reader = wasmparser::BinaryReader::new(&malformed_wasm, 0);
        if let Ok(body) = reader.read() {
            let res = jit::translate_wasm_body_with_mem(
                &body,
                &mut func,
                &mut fb_ctx,
                jit::default_frontend_config(),
                &types,
                &func_type_indices,
                jit::default_call_conv(),
                guest_mem.as_mut_ptr(),
                guest_mem.len(),
            );
            assert!(res.is_err(), "malformed wasm must return Err");
        }
    }

    #[test]
    fn test_import_name_to_abi_offset() {
        use crate::abi::PalAbiTable;
        use core::mem::offset_of;
        assert_eq!(jit::import_name_to_abi_offset("host_panic"), Some(offset_of!(PalAbiTable, host_panic)));
        assert_eq!(jit::import_name_to_abi_offset("rusticated_debug"), Some(offset_of!(PalAbiTable, rusticated_debug)));
        assert_eq!(jit::import_name_to_abi_offset("process_exit"), Some(offset_of!(PalAbiTable, process_exit)));
        assert_eq!(jit::import_name_to_abi_offset("get_time"), Some(offset_of!(PalAbiTable, get_time)));
        assert_eq!(jit::import_name_to_abi_offset("get_random"), Some(offset_of!(PalAbiTable, get_random)));
        assert_eq!(jit::import_name_to_abi_offset("get_args"), Some(offset_of!(PalAbiTable, get_args)));
        assert_eq!(jit::import_name_to_abi_offset("get_env"), Some(offset_of!(PalAbiTable, get_env)));
        assert_eq!(jit::import_name_to_abi_offset("get_cwd"), Some(offset_of!(PalAbiTable, get_cwd)));
        assert_eq!(jit::import_name_to_abi_offset("set_cwd"), Some(offset_of!(PalAbiTable, set_cwd)));
        assert_eq!(jit::import_name_to_abi_offset("get_platform_info"), Some(offset_of!(PalAbiTable, get_platform_info)));
        assert_eq!(jit::import_name_to_abi_offset("read"), Some(offset_of!(PalAbiTable, read)));
        assert_eq!(jit::import_name_to_abi_offset("write"), Some(offset_of!(PalAbiTable, write)));
        assert_eq!(jit::import_name_to_abi_offset("process_spawn"), Some(offset_of!(PalAbiTable, process_spawn)));
        assert_eq!(jit::import_name_to_abi_offset("fd_isatty"), Some(offset_of!(PalAbiTable, fd_isatty)));
        assert_eq!(jit::import_name_to_abi_offset("nonexistent_symbol"), None);
    }

    #[test]
    fn test_import_wasm_parsing() {
        let wasm_bytes: &[u8] = &[
            0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic + version
            // Type section (id=1, len=8, 2 types: ()->i64, ()->())
            0x01, 0x08, 0x02,
            0x60, 0x00, 0x01, 0x7e,
            0x60, 0x00, 0x00,
            // Import section (id=2, len=16, 1 import: env.get_time -> func 0)
            0x02, 0x10, 0x01,
            0x03, b'e', b'n', b'v',
            0x08, b'g', b'e', b't', b'_', b't', b'i', b'm', b'e',
            0x00, 0x00,
            // Function section (id=3, len=2, 1 func: func 1 has type 1)
            0x03, 0x02, 0x01, 0x01,
            // Memory section (id=5, len=3, 1 page min)
            0x05, 0x03, 0x01, 0x00, 0x01,
            // Export section (id=7, len=7, "run" -> func 1)
            0x07, 0x07, 0x01, 0x03, b'r', b'u', b'n', 0x00, 0x01,
            // Code section (id=10, len=11, 1 body)
            0x0a, 0x0b, 0x01,
            0x09, 0x00,
            0x41, 0x00,
            0x10, 0x00,
            0x37, 0x03, 0x00,
            0x0b
        ];

        let mut imported_func_names = alloc::vec::Vec::new();
        let mut func_type_indices = alloc::vec::Vec::new();
        let mut num_imported_funcs = 0u32;

        for payload in wasmparser::Parser::new(0).parse_all(wasm_bytes) {
            let p = payload.expect("payload must parse");
            if let wasmparser::Payload::ImportSection(reader) = p {
                for imp in reader {
                    let imp = imp.expect("import entry must parse");
                    if let wasmparser::Imports::Single(_, imp) = imp {
                        if let wasmparser::TypeRef::Func(type_idx) = imp.ty {
                            imported_func_names.push(imp.name);
                            func_type_indices.push(type_idx);
                            num_imported_funcs += 1;
                        }
                    }
                }
            }
        }
        assert_eq!(num_imported_funcs, 1);
        assert_eq!(imported_func_names[0], "get_time");
        assert_eq!(func_type_indices[0], 0);
    }
}
