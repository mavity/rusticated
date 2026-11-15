//! Zero-context `#![no_std]` bump/arena `GlobalAlloc` backed by `PalAbiTable::alloc_mem`
//! and `RuntimeGlobals` at `-0x10000`.

use crate::abi::{MEM_READWRITE, abi, globals};
use core::alloc::{GlobalAlloc, Layout};
use core::ptr;

const DEFAULT_ARENA_CHUNK_SIZE: usize = 16 * 1024 * 1024; // 16 MiB chunks
const SMALL_BIN_MAX: usize = 131_072; // 128 KiB (bins 0..=13 for 16..=131072)
const MAGIC_SMALL: usize = 0x4D48_534D_414C_4C31; // "MHSMALL1"
const MAGIC_LARGE: usize = 0x4D48_4C41_5247_4531; // "MHLARGE1"

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

        if size <= SMALL_BIN_MAX && align <= 16 {
            let bin_size = size.max(16).next_power_of_two();
            let bin_idx = (bin_size.trailing_zeros() - 4) as usize;
            let cached = g.heap_free_bins[bin_idx];
            if !cached.is_null() {
                let user_ptr = cached.add(16);
                g.heap_free_bins[bin_idx] = *user_ptr.cast::<*mut u8>();
                return user_ptr;
            }

            let total = 16 + bin_size;
            let aligned_used = align_up(g.heap_used, 16);
            let block = if !g.heap_base.is_null() && aligned_used.saturating_add(total) <= g.heap_cap
            {
                let p = g.heap_base.add(aligned_used);
                g.heap_used = aligned_used + total;
                p
            } else {
                let chunk_size = align_up(total.max(DEFAULT_ARENA_CHUNK_SIZE), 65536);
                let new_chunk = (abi().alloc_mem)(chunk_size, 65536, MEM_READWRITE);
                if new_chunk.is_null() {
                    return ptr::null_mut();
                }
                g.heap_base = new_chunk;
                g.heap_cap = chunk_size;
                g.heap_used = total;
                new_chunk
            };
            *block.cast::<usize>() = bin_idx;
            *block.add(8).cast::<usize>() = MAGIC_SMALL;
            return block.add(16);
        }

        let eff_align = align.max(16);
        let header_pad = align_up(24, eff_align);
        let total_size = align_up(header_pad.saturating_add(size.max(1)), 65536);
        let base = (abi().alloc_mem)(total_size, 65536, MEM_READWRITE);
        if base.is_null() {
            return ptr::null_mut();
        }
        let user_ptr = base.add(header_pad);
        *user_ptr.sub(24).cast::<usize>() = base as usize;
        *user_ptr.sub(16).cast::<usize>() = total_size;
        *user_ptr.sub(8).cast::<usize>() = MAGIC_LARGE;
        user_ptr
    }

    unsafe fn dealloc(&self, ptr: *mut u8, _layout: Layout) {
        if ptr.is_null() {
            return;
        }
        let magic = *ptr.sub(8).cast::<usize>();
        if magic == MAGIC_SMALL {
            let block = ptr.sub(16);
            let bin_idx = *block.cast::<usize>();
            let g = globals();
            *ptr.cast::<*mut u8>() = g.heap_free_bins[bin_idx];
            g.heap_free_bins[bin_idx] = block;
        } else if magic == MAGIC_LARGE {
            let base = *ptr.sub(24).cast::<*mut u8>();
            let total_size = *ptr.sub(16).cast::<usize>();
            (abi().free_mem)(base, total_size);
        }
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        if new_size == 0 {
            self.dealloc(ptr, layout);
            return ptr::null_mut();
        }
        if ptr.is_null() {
            let Ok(new_layout) = Layout::from_size_align(new_size, layout.align()) else {
                return ptr::null_mut();
            };
            return self.alloc(new_layout);
        }

        let magic = *ptr.sub(8).cast::<usize>();
        if magic == MAGIC_SMALL && layout.align() <= 16 {
            let bin_idx = *ptr.sub(16).cast::<usize>();
            if new_size <= (16usize << bin_idx) {
                return ptr;
            }
        } else if magic == MAGIC_LARGE {
            let base = *ptr.sub(24).cast::<*mut u8>();
            let total_size = *ptr.sub(16).cast::<usize>();
            let used_offset = (ptr as usize) - (base as usize);
            if used_offset.saturating_add(new_size) <= total_size {
                return ptr;
            }
        }

        let Ok(new_layout) = Layout::from_size_align(new_size, layout.align()) else {
            return ptr::null_mut();
        };
        let new_ptr = self.alloc(new_layout);
        if new_ptr.is_null() {
            return ptr::null_mut();
        }
        ptr::copy_nonoverlapping(ptr, new_ptr, layout.size().min(new_size));
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
