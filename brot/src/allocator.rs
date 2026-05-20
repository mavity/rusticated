use core::alloc::{GlobalAlloc, Layout};
use core::cell::UnsafeCell;
use core::ptr::null_mut;

const ARENA_SIZE: usize = 32 * 1024 * 1024; // 32MB for testing
static mut ARENA: [u8; ARENA_SIZE] = [0; ARENA_SIZE];

struct BumpAllocator {
    offset: UnsafeCell<usize>,
}

unsafe impl Sync for BumpAllocator {}

unsafe impl GlobalAlloc for BumpAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        unsafe {
            let offset = self.offset.get();
            let align = layout.align();
            let start = (*offset + align - 1) & !(align - 1);

            if start + layout.size() > ARENA_SIZE {
                return null_mut();
            }

            *offset = start + layout.size();
            let arena_ptr = core::ptr::addr_of_mut!(ARENA) as *mut u8;
            arena_ptr.add(start)
        }
    }

    unsafe fn dealloc(&self, _ptr: *mut u8, _layout: Layout) {
        // no-op
    }
}

#[global_allocator]
static ALLOCATOR: BumpAllocator = BumpAllocator {
    offset: UnsafeCell::new(0),
};
