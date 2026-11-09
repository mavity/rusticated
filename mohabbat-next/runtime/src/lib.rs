//! Mohabbat-Next Movable Cranelift Runtime Slice (`#![no_std]` + `alloc`).
//!
//! Entry point `slice_entry` is located at byte offset `0x00000` (`.text.entry`).
//! It reads the host `PalAbiTable` at `-0x20000` (`__abi_base`) and its own
//! `RuntimeGlobals` at `-0x10000` (`__globals_base`) via PC-relative addressing.

#![no_std]
#![allow(unsafe_op_in_unsafe_fn)]

extern crate alloc;

pub mod abi;
pub mod allocator;
pub mod jit;

use crate::abi::{abi, globals};
use crate::allocator::PalBumpAllocator;
use core::panic::PanicInfo;

#[global_allocator]
static ALLOC: PalBumpAllocator = PalBumpAllocator;

#[panic_handler]
fn panic(_info: &PanicInfo<'_>) -> ! {
    unsafe {
        (abi().host_panic)(b"runtime panic".as_ptr(), 13);
    }
}

#[cfg(target_arch = "x86_64")]
#[unsafe(link_section = ".text.entry")]
#[unsafe(no_mangle)]
pub unsafe extern "win64" fn slice_entry() {
    slice_entry_inner();
}

#[cfg(target_arch = "aarch64")]
#[unsafe(link_section = ".text.entry")]
#[unsafe(no_mangle)]
pub unsafe extern "C" fn slice_entry() {
    slice_entry_inner();
}

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
