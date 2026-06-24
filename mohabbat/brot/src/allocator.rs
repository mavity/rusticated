use core::alloc::{GlobalAlloc, Layout};

pub struct OsAllocator;

fn page_align(n: usize) -> usize {
    (n + 4095) & !4095
}

// ─── Windows: GetProcessHeap + HeapAlloc / HeapFree / HeapReAlloc ───────────

#[cfg(windows)]
unsafe impl GlobalAlloc for OsAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetProcessHeap() -> *mut core::ffi::c_void;
            fn HeapAlloc(
                hHeap: *mut core::ffi::c_void,
                dwFlags: u32,
                dwBytes: usize,
            ) -> *mut core::ffi::c_void;
        }
        let heap = unsafe { GetProcessHeap() };
        if heap.is_null() {
            return core::ptr::null_mut();
        }
        unsafe { HeapAlloc(heap, 0, layout.size().max(1)) as *mut u8 }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, _layout: Layout) {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetProcessHeap() -> *mut core::ffi::c_void;
            fn HeapFree(
                hHeap: *mut core::ffi::c_void,
                dwFlags: u32,
                lpMem: *mut core::ffi::c_void,
            ) -> i32;
        }
        let heap = unsafe { GetProcessHeap() };
        if !heap.is_null() {
            unsafe { HeapFree(heap, 0, ptr as *mut core::ffi::c_void) };
        }
    }

    unsafe fn realloc(&self, ptr: *mut u8, _layout: Layout, new_size: usize) -> *mut u8 {
        #[link(name = "kernel32", kind = "raw-dylib")]
        unsafe extern "system" {
            fn GetProcessHeap() -> *mut core::ffi::c_void;
            fn HeapReAlloc(
                hHeap: *mut core::ffi::c_void,
                dwFlags: u32,
                lpMem: *mut core::ffi::c_void,
                dwBytes: usize,
            ) -> *mut core::ffi::c_void;
        }
        let heap = unsafe { GetProcessHeap() };
        if heap.is_null() {
            return core::ptr::null_mut();
        }
        unsafe { HeapReAlloc(heap, 0, ptr as *mut core::ffi::c_void, new_size.max(1)) as *mut u8 }
    }
}

// ─── Linux: mmap / munmap via inline syscalls ────────────────────────────────

#[cfg(target_os = "linux")]
#[cfg(target_arch = "x86_64")]
unsafe fn sys_mmap(size: usize) -> *mut u8 {
    let res: usize;
    unsafe {
        core::arch::asm!(
            "syscall",
            in("rax") 9usize,           // SYS_mmap
            in("rdi") 0usize,           // addr = NULL
            in("rsi") size,             // len
            in("rdx") 3usize,           // PROT_READ | PROT_WRITE
            in("r10") 0x22usize,        // MAP_PRIVATE | MAP_ANONYMOUS
            in("r8") (-1i64) as usize,  // fd = -1
            in("r9") 0usize,            // offset = 0
            lateout("rax") res,
            clobber_abi("system"),
        );
    }
    if (res as isize) < 0 && (res as isize) >= -4096 {
        core::ptr::null_mut()
    } else {
        res as *mut u8
    }
}

#[cfg(target_os = "linux")]
#[cfg(target_arch = "aarch64")]
unsafe fn sys_mmap(size: usize) -> *mut u8 {
    let res: usize;
    unsafe {
        core::arch::asm!(
            "svc #0",
            in("x8") 222usize,          // SYS_mmap
            in("x0") 0usize,            // addr = NULL
            in("x1") size,              // len
            in("x2") 3usize,            // PROT_READ | PROT_WRITE
            in("x3") 0x22usize,         // MAP_PRIVATE | MAP_ANONYMOUS
            in("x4") (-1i64) as usize,  // fd = -1
            in("x5") 0usize,            // offset = 0
            lateout("x0") res,
            clobber_abi("system"),
        );
    }
    if (res as isize) < 0 && (res as isize) >= -4096 {
        core::ptr::null_mut()
    } else {
        res as *mut u8
    }
}

#[cfg(target_os = "linux")]
#[cfg(target_arch = "x86_64")]
unsafe fn sys_munmap(ptr: *mut u8, size: usize) {
    unsafe {
        let _: usize;
        core::arch::asm!(
            "syscall",
            in("rax") 11usize,  // SYS_munmap
            in("rdi") ptr as usize,
            in("rsi") size,
            lateout("rax") _,
            clobber_abi("system"),
        );
    }
}

#[cfg(target_os = "linux")]
#[cfg(target_arch = "aarch64")]
unsafe fn sys_munmap(ptr: *mut u8, size: usize) {
    unsafe {
        let _: usize;
        core::arch::asm!(
            "svc #0",
            in("x8") 215usize,  // SYS_munmap
            in("x0") ptr as usize,
            in("x1") size,
            lateout("x0") _,
            clobber_abi("system"),
        );
    }
}

#[cfg(target_os = "linux")]
unsafe impl GlobalAlloc for OsAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        unsafe { sys_mmap(page_align(layout.size().max(1))) }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { sys_munmap(ptr, page_align(layout.size().max(1))) };
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let old_rounded = page_align(layout.size().max(1));
        let new_rounded = page_align(new_size.max(1));
        let new_ptr = unsafe { sys_mmap(new_rounded) };
        if !new_ptr.is_null() {
            unsafe { core::ptr::copy_nonoverlapping(ptr, new_ptr, layout.size().min(new_size)) };
            unsafe { sys_munmap(ptr, old_rounded) };
        }
        new_ptr
    }
}

// ─── macOS: mmap / munmap via libSystem (always present in the shared cache) ─

#[cfg(target_os = "macos")]
#[link(name= "System" )]
unsafe extern "C" {
    fn mmap(addr: *mut u8, len: usize, prot: i32, flags: i32, fd: i32, offset: i64) -> *mut u8;
    fn munmap(addr: *mut u8, len: usize) -> i32;
}

#[cfg(target_os = "macos")]
unsafe impl GlobalAlloc for OsAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let size = page_align(layout.size().max(1));
        // PROT_READ|PROT_WRITE = 3, MAP_PRIVATE|MAP_ANON = 0x1002
        let ptr = unsafe { mmap(core::ptr::null_mut(), size, 3, 0x1002, -1, 0) };
        if ptr.is_null() || ptr as usize == usize::MAX {
            core::ptr::null_mut()
        } else {
            ptr
        }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { munmap(ptr, page_align(layout.size().max(1))) };
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let old_rounded = page_align(layout.size().max(1));
        let new_rounded = page_align(new_size.max(1));
        let new_ptr = unsafe { mmap(core::ptr::null_mut(), new_rounded, 3, 0x1002, -1, 0) };
        if new_ptr.is_null() || new_ptr as usize == usize::MAX {
            return core::ptr::null_mut();
        }
        unsafe { core::ptr::copy_nonoverlapping(ptr, new_ptr, layout.size().min(new_size)) };
        unsafe { munmap(ptr, old_rounded) };
        new_ptr
    }
}

// ─── WASM: memory_grow ──────────────────────────────────────────────────────

#[cfg(target_family = "wasm")]
unsafe impl GlobalAlloc for OsAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let size = page_align(layout.size().max(1));
        let pages = size / 65536 + if size % 65536 != 0 { 1 } else { 0 };
        let prev = core::arch::wasm32::memory_grow(0, pages);
        if prev == usize::MAX {
            core::ptr::null_mut()
        } else {
            (prev * 65536) as *mut u8
        }
    }

    unsafe fn dealloc(&self, _ptr: *mut u8, _layout: Layout) {
        // Bump allocator: no-op dealloc.
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let new_ptr = self.alloc(Layout::from_size_align_unchecked(new_size, layout.align()));
        if !new_ptr.is_null() {
            core::ptr::copy_nonoverlapping(ptr, new_ptr, layout.size().min(new_size));
        }
        new_ptr
    }
}

#[global_allocator]
static ALLOCATOR: OsAllocator = OsAllocator;
