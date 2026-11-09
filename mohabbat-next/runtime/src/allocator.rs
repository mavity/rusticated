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

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        if new_size == 0 {
            self.dealloc(ptr, layout);
            return ptr::null_mut();
        }

        let new_layout = match Layout::from_size_align(new_size, layout.align()) {
            Ok(layout) => layout,
            Err(_) => return ptr::null_mut(),
        };
        let new_ptr = self.alloc(new_layout);
        if new_ptr.is_null() {
            return ptr::null_mut();
        }

        let copy_size = layout.size().min(new_size);
        let mut i = 0;
        while i < copy_size {
            let value = ptr::read_volatile(ptr.add(i).cast::<core::mem::MaybeUninit<u8>>());
            ptr::write_volatile(
                new_ptr.add(i).cast::<core::mem::MaybeUninit<u8>>(),
                value,
            );
            i += 1;
        }
        self.dealloc(ptr, layout);
        new_ptr
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_align_up_properties() {
        assert_eq!(align_up(0, 8), 0);
        assert_eq!(align_up(1, 8), 8);
        assert_eq!(align_up(7, 8), 8);
        assert_eq!(align_up(8, 8), 8);
        assert_eq!(align_up(9, 8), 16);
        assert_eq!(align_up(15, 16), 16);
        assert_eq!(align_up(16, 16), 16);
        assert_eq!(align_up(17, 16), 32);
        assert_eq!(align_up(65535, 65536), 65536);
        assert_eq!(align_up(65536, 65536), 65536);
    }
}
