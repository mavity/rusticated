use core::alloc::{GlobalAlloc, Layout};

pub struct OsAllocator;

fn page_align(n: usize) -> usize {
    (n + 4095) & !4095
}

#[cfg(target_os = "linux")]
#[cfg(target_arch = "x86_64")]
unsafe fn sys_mmap(size: usize) -> *mut u8 {
    let res: usize;
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
    if (res as isize) < 0 && (res as isize) >= -4096 {
        core::ptr::null_mut()
    } else {
        res as *mut u8
    }
}

#[cfg(target_os = "linux")]
#[cfg(target_arch = "x86_64")]
unsafe fn sys_munmap(ptr: *mut u8, size: usize) {
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

#[cfg(target_os = "linux")]
#[cfg(target_arch = "aarch64")]
unsafe fn sys_munmap(ptr: *mut u8, size: usize) {
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

#[cfg(target_os = "linux")]
unsafe impl GlobalAlloc for OsAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        sys_mmap(page_align(layout.size().max(1)))
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        sys_munmap(ptr, page_align(layout.size().max(1)));
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let old_rounded = page_align(layout.size().max(1));
        let new_rounded = page_align(new_size.max(1));
        let new_ptr = sys_mmap(new_rounded);
        if !new_ptr.is_null() {
            core::ptr::copy_nonoverlapping(ptr, new_ptr, layout.size().min(new_size));
            sys_munmap(ptr, old_rounded);
        }
        new_ptr
    }
}

#[cfg(target_os = "macos")]
#[link(name = "System")]
unsafe extern "C" {
    fn mmap(addr: *mut core::ffi::c_void, len: usize, prot: i32, flags: i32, fd: i32, offset: i64) -> *mut core::ffi::c_void;
    fn munmap(addr: *mut core::ffi::c_void, len: usize) -> i32;
}

#[cfg(target_os = "macos")]
unsafe impl GlobalAlloc for OsAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let size = page_align(layout.size().max(1));
        let ptr = mmap(core::ptr::null_mut(), size, 3, 0x1002, -1, 0);
        if ptr.is_null() || ptr as usize == usize::MAX {
            core::ptr::null_mut()
        } else {
            ptr as *mut u8
        }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        let size = page_align(layout.size().max(1));
        munmap(ptr as *mut core::ffi::c_void, size);
    }

    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, new_size: usize) -> *mut u8 {
        let new_rounded = page_align(new_size.max(1));
        let new_ptr = self.alloc(Layout::from_size_align_unchecked(new_rounded, layout.align()));
        if !new_ptr.is_null() {
            core::ptr::copy_nonoverlapping(ptr, new_ptr, layout.size().min(new_size));
            self.dealloc(ptr, layout);
        }
        new_ptr
    }
}
