#![allow(missing_docs)]

use core::alloc::{GlobalAlloc, Layout};
use core::ptr;
use spin::Mutex;

#[cfg(any(target_os = "linux", rusticated_linux))]
mod backend_linux {
    use core::ffi::{c_int, c_void};
    use core::ptr;

    const PROT_READ: c_int = 1;
    const PROT_WRITE: c_int = 2;
    const MAP_PRIVATE: c_int = 2;
    const MAP_ANONYMOUS: c_int = 0x20;

    pub unsafe fn reserve(size: usize) -> *mut u8 {
        let addr = unsafe {
            crate::os::linux::mmap(
                ptr::null_mut(),
                size,
                PROT_READ | PROT_WRITE,
                MAP_PRIVATE | MAP_ANONYMOUS,
                -1,
                0,
            )
        };
        if addr == crate::os::linux::MAP_FAILED {
            ptr::null_mut()
        } else {
            addr as *mut u8
        }
    }

    pub unsafe fn release(ptr: *mut u8, size: usize) {
        let _ = unsafe { crate::os::linux::munmap(ptr as *mut c_void, size) };
    }
}

#[cfg(windows)]
mod backend_windows {
    use core::ffi::c_void;

    #[link(name = "kernel32", kind = "raw-dylib")]
    unsafe extern "system" {
        fn GetProcessHeap() -> usize;
        fn HeapAlloc(hHeap: usize, dwFlags: u32, dwBytes: usize) -> *mut c_void;
        fn HeapFree(hHeap: usize, dwFlags: u32, lpMem: *mut c_void) -> i32;
    }

    pub unsafe fn reserve(size: usize) -> *mut u8 {
        unsafe { HeapAlloc(GetProcessHeap(), 0, size) as *mut u8 }
    }

    pub unsafe fn release(ptr: *mut u8, _size: usize) {
        unsafe { HeapFree(GetProcessHeap(), 0, ptr as *mut c_void) };
    }
}

#[cfg(target_family = "wasm")]
mod backend_wasm {
    use core::arch::wasm32;
    use core::ptr;

    const PAGE_SIZE: usize = 65536;

    pub unsafe fn reserve(size: usize) -> *mut u8 {
        let pages = (size + PAGE_SIZE - 1) / PAGE_SIZE;
        let old_pages = wasm32::memory_grow::<0>(pages);
        if old_pages == usize::MAX {
            ptr::null_mut()
        } else {
            (old_pages * PAGE_SIZE) as *mut u8
        }
    }

    pub unsafe fn release(_ptr: *mut u8, _size: usize) {}
}

#[cfg(any(target_os = "linux", rusticated_linux))]
use backend_linux as backend;
#[cfg(windows)]
use backend_windows as backend;
#[cfg(target_family = "wasm")]
use backend_wasm as backend;

const PAGE_SIZE: usize = 4096;
const BLOCK_HEADER_SIZE: usize = 32;
const MIN_ALLOC_CLASS: usize = 16;
const SMALL_CLASSES: [usize; 8] = [16, 32, 64, 128, 256, 512, 1024, 2048];
const KIND_SMALL: u8 = 1;
const KIND_LARGE: u8 = 2;

#[repr(C)]
struct BlockHeader {
    kind: u8,
    class_index: u8,
    _reserved: [u8; 6],
    region_size: usize,
    raw_base: usize,
    payload_offset: usize,
}

#[repr(C)]
struct FreePayload {
    next: *mut BlockHeader,
}

struct AllocatorState {
    free_lists: [*mut BlockHeader; SMALL_CLASSES.len()],
}

unsafe impl Sync for AllocatorState {}
unsafe impl Send for AllocatorState {}

impl AllocatorState {
    const fn new() -> Self {
        Self {
            free_lists: [ptr::null_mut(); SMALL_CLASSES.len()],
        }
    }
}

static STATE: Mutex<AllocatorState> = Mutex::new(AllocatorState::new());

pub struct RusticatedAllocator;

unsafe impl GlobalAlloc for RusticatedAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let size = layout.size().max(1);
        let align = layout.align().max(16);
        let request = size.max(align);

        if let Some(class_index) = select_class(request, align) {
            unsafe { allocate_small(class_index) }
        } else {
            unsafe { allocate_large(request, align) }
        }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, _layout: Layout) {
        if ptr.is_null() {
            return;
        }

        let header = block_header_from_ptr(ptr);
        match unsafe { (*header).kind } {
            KIND_SMALL => unsafe { dealloc_small(ptr, header) },
            KIND_LARGE => unsafe { dealloc_large(header) },
            _ => {}
        }
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        if ptr.is_null() {
            return unsafe { self.alloc(Layout::from_size_align_unchecked(new_size, layout.align())) };
        }

        if new_size == 0 {
            unsafe { self.dealloc(ptr, layout) };
            return ptr::null_mut();
        }

        let header = block_header_from_ptr(ptr);
        let old_size = if unsafe { (*header).kind } == KIND_SMALL {
            SMALL_CLASSES[unsafe { (*header).class_index as usize }]
        } else {
            unsafe { (*header).region_size }
        };

        if new_size <= old_size {
            return ptr;
        }

        let new_layout = unsafe { Layout::from_size_align_unchecked(new_size, layout.align()) };
        let new_ptr = unsafe { self.alloc(new_layout) };
        if new_ptr.is_null() {
            return ptr::null_mut();
        }

        unsafe { ptr::copy_nonoverlapping(ptr, new_ptr, layout.size().min(new_size)) };
        unsafe { self.dealloc(ptr, layout) };
        new_ptr
    }

    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        let ptr = unsafe { self.alloc(layout) };
        if !ptr.is_null() {
            unsafe { ptr::write_bytes(ptr, 0, layout.size()) };
        }
        ptr
    }
}

unsafe fn allocate_small(class_index: usize) -> *mut u8 {
    let mut state = STATE.lock();
    if let Some(block) = unsafe { state.free_lists[class_index].as_mut() } {
        let payload = block_payload(block);
        let next = unsafe { (payload as *mut FreePayload).as_mut().unwrap().next };
        state.free_lists[class_index] = next;
        return payload;
    }

    let class_size = SMALL_CLASSES[class_index];
    let stride = align_up(BLOCK_HEADER_SIZE + class_size, 16);
    let page = unsafe { backend::reserve(PAGE_SIZE) };
    if page.is_null() {
        return ptr::null_mut();
    }

    let base = page as usize;
    let start = align_up(base + BLOCK_HEADER_SIZE, 16);
    let mut current = start;
    let mut free_list: *mut BlockHeader = ptr::null_mut();

    while current + BLOCK_HEADER_SIZE + class_size <= base + PAGE_SIZE {
        let block = current as *mut BlockHeader;
        unsafe {
            ptr::write(
                block,
                BlockHeader {
                    kind: KIND_SMALL,
                    class_index: class_index as u8,
                    _reserved: [0u8; 6],
                    region_size: 0,
                    raw_base: 0,
                    payload_offset: 0,
                },
            );

            let payload = block_payload(block);
            let payload_next = payload as *mut FreePayload;
            ptr::write(payload_next, FreePayload { next: free_list });
        }

        free_list = block;
        current += stride;
    }

    if free_list.is_null() {
        unsafe { backend::release(page, PAGE_SIZE) };
        return ptr::null_mut();
    }

    let allocated = free_list;
    let payload = block_payload(allocated);
    let next = unsafe { (payload as *mut FreePayload).as_mut().unwrap().next };
    state.free_lists[class_index] = next;
    payload
}

unsafe fn dealloc_small(ptr: *mut u8, header: *mut BlockHeader) {
    let class_index = unsafe { (*header).class_index as usize };
    let mut state = STATE.lock();
    let payload_next = ptr as *mut FreePayload;
    unsafe {
        ptr::write(payload_next, FreePayload { next: state.free_lists[class_index] });
    }
    state.free_lists[class_index] = header;
}

unsafe fn allocate_large(size: usize, align: usize) -> *mut u8 {
    let region_size = align_up(size + BLOCK_HEADER_SIZE + align, PAGE_SIZE);
    let raw = unsafe { backend::reserve(region_size) };
    if raw.is_null() {
        return ptr::null_mut();
    }

    let raw_base = raw as usize;
    let payload_addr = align_up(raw_base + BLOCK_HEADER_SIZE, align);
    let header = (payload_addr - BLOCK_HEADER_SIZE) as *mut BlockHeader;
    unsafe {
        ptr::write(
            header,
            BlockHeader {
                kind: KIND_LARGE,
                class_index: 0,
                _reserved: [0u8; 6],
                region_size,
                raw_base,
                payload_offset: payload_addr - raw_base,
            },
        );
    }

    payload_addr as *mut u8
}

unsafe fn dealloc_large(header: *mut BlockHeader) {
    let raw = unsafe { (*header).raw_base as *mut u8 };
    unsafe { backend::release(raw, (*header).region_size) };
}

fn select_class(size: usize, align: usize) -> Option<usize> {
    let required = align_up(size.max(align).max(MIN_ALLOC_CLASS), 16);
    SMALL_CLASSES.iter().position(|&class| class >= required)
}

fn block_header_from_ptr(ptr: *mut u8) -> *mut BlockHeader {
    (ptr as usize - BLOCK_HEADER_SIZE) as *mut BlockHeader
}

fn block_payload(block: *mut BlockHeader) -> *mut u8 {
    (block as usize + BLOCK_HEADER_SIZE) as *mut u8
}

const fn align_up(value: usize, align: usize) -> usize {
    (value + align - 1) & !(align - 1)
}
