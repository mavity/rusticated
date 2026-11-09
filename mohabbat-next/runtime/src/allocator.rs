//! Zero-context `#![no_std]` bump/arena `GlobalAlloc` backed by `PalAbiTable::alloc_mem`
//! and `RuntimeGlobals` at `-0x10000`.

use crate::abi::{MEM_READWRITE, abi, globals};
use core::alloc::{GlobalAlloc, Layout};
use core::ptr;

const DEFAULT_ARENA_CHUNK_SIZE: usize = 16 * 1024 * 1024; // 16 MiB chunks

pub struct PalBumpAllocator;

#[inline(always)]
fn align_up(val: usize, align: usize) -> usize {
    (val + align - 1) & !(align - 1)
}

unsafe impl GlobalAlloc for PalBumpAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let g = globals();
        let align = layout.align().max(8);
        let size = layout.size();

        let aligned_used = align_up(g.heap_used, align);
        if !g.heap_base.is_null() && aligned_used.saturating_add(size) <= g.heap_cap {
            let ptr = g.heap_base.add(aligned_used);
            g.heap_used = aligned_used + size;
            return ptr;
        }

        let chunk_size = align_up(size.max(DEFAULT_ARENA_CHUNK_SIZE), 65536);
        let new_chunk = (abi().alloc_mem)(chunk_size, 65536, MEM_READWRITE);
        if new_chunk.is_null() {
            return ptr::null_mut();
        }
        g.heap_base = new_chunk;
        g.heap_cap = chunk_size;
        g.heap_used = size;
        new_chunk
    }

    unsafe fn dealloc(&self, _ptr: *mut u8, _layout: Layout) {
        // Bump arena freed in bulk or reclaimed after cold-start compilation completes.
    }
}
