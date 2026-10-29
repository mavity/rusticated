use alloc::string::String;
use alloc::vec::Vec;

unsafe extern "C" {
    fn dlopen(path: *const u8, flags: i32) -> usize;
    fn dlsym(handle: usize, symbol: *const u8) -> usize;
    fn dlclose(handle: usize) -> i32;
    fn dlerror() -> *const u8;
    #[link_name = "malloc"]
    fn c_malloc(size: usize) -> usize;
    #[link_name = "free"]
    fn c_free(ptr: usize);
}

#[cfg(target_os = "linux")]
pub fn current_thread_id() -> u64 {
    let tid: usize;
    unsafe {
        #[cfg(target_arch = "x86_64")]
        core::arch::asm!("syscall", in("rax") 186usize, lateout("rax") tid, clobber_abi("system"));
        #[cfg(target_arch = "aarch64")]
        core::arch::asm!("svc #0", in("x8") 178usize, lateout("x0") tid, clobber_abi("system"));
        #[cfg(target_arch = "arm")]
        core::arch::asm!("svc #0", in("r7") 224usize, lateout("r0") tid, clobber_abi("system"));
    }
    tid as u64
}

#[cfg(target_os = "macos")]
pub fn current_thread_id() -> u64 {
    let mut sp: usize = 0;
    (&mut sp as *mut usize as u64) & !0x3fff
}

#[cfg(target_os = "linux")]
pub fn alloc_executable_page(init: impl FnOnce(&mut [u8])) -> Option<*const u8> {
    const PAGE_SIZE: usize = 4096;
    let ptr: usize;
    unsafe {
        #[cfg(target_arch = "x86_64")]
        core::arch::asm!(
            "syscall",
            in("rax") 9usize,           // SYS_mmap
            in("rdi") 0usize,
            in("rsi") PAGE_SIZE,
            in("rdx") 3usize,           // PROT_READ | PROT_WRITE
            in("r10") 0x22usize,        // MAP_PRIVATE | MAP_ANONYMOUS
            in("r8") (-1isize) as usize,
            in("r9") 0usize,
            lateout("rax") ptr,
            clobber_abi("system"),
        );
        #[cfg(target_arch = "aarch64")]
        core::arch::asm!(
            "svc #0",
            in("x8") 222usize,          // SYS_mmap
            in("x0") 0usize,
            in("x1") PAGE_SIZE,
            in("x2") 3usize,           // PROT_READ | PROT_WRITE
            in("x3") 0x22usize,        // MAP_PRIVATE | MAP_ANONYMOUS
            in("x4") (-1isize) as usize,
            in("x5") 0usize,
            lateout("x0") ptr,
            clobber_abi("system"),
        );
        #[cfg(target_arch = "arm")]
        core::arch::asm!(
            "svc #0",
            in("r7") 192usize,          // SYS_mmap2
            in("r0") 0usize,
            in("r1") PAGE_SIZE,
            in("r2") 3usize,           // PROT_READ | PROT_WRITE
            in("r3") 0x22usize,        // MAP_PRIVATE | MAP_ANONYMOUS
            in("r4") (-1isize) as usize,
            in("r5") 0usize,
            lateout("r0") ptr,
            clobber_abi("system"),
        );
    }
    if ptr == 0 || ((ptr as isize) < 0 && (ptr as isize) >= -4096) {
        return None;
    }
    let slice = unsafe { core::slice::from_raw_parts_mut(ptr as *mut u8, PAGE_SIZE) };
    init(slice);

    let rc: isize;
    unsafe {
        #[cfg(target_arch = "x86_64")]
        core::arch::asm!(
            "syscall",
            in("rax") 10usize,          // SYS_mprotect
            in("rdi") ptr,
            in("rsi") PAGE_SIZE,
            in("rdx") 5usize,           // PROT_READ | PROT_EXEC
            lateout("rax") rc,
            clobber_abi("system"),
        );
        #[cfg(target_arch = "aarch64")]
        core::arch::asm!(
            "svc #0",
            in("x8") 226usize,          // SYS_mprotect
            in("x0") ptr,
            in("x1") PAGE_SIZE,
            in("x2") 5usize,           // PROT_READ | PROT_EXEC
            lateout("x0") rc,
            clobber_abi("system"),
        );
        #[cfg(target_arch = "arm")]
        core::arch::asm!(
            "svc #0",
            in("r7") 125usize,          // SYS_mprotect
            in("r0") ptr,
            in("r1") PAGE_SIZE,
            in("r2") 5usize,           // PROT_READ | PROT_EXEC
            lateout("r0") rc,
            clobber_abi("system"),
        );
    }
    if rc < 0 {
        return None;
    }

    #[cfg(target_arch = "aarch64")]
    unsafe {
        let mut addr = ptr;
        let end = ptr + PAGE_SIZE;
        while addr < end {
            core::arch::asm!("dc cvau, {0}", in(reg) addr, options(nostack, preserves_flags));
            addr += 64;
        }
        core::arch::asm!("dsb ish", options(nostack, preserves_flags));
        addr = ptr;
        while addr < end {
            core::arch::asm!("ic ivau, {0}", in(reg) addr, options(nostack, preserves_flags));
            addr += 64;
        }
        core::arch::asm!("dsb ish", "isb", options(nostack, preserves_flags));
    }
    #[cfg(target_arch = "arm")]
    unsafe {
        let _: usize;
        core::arch::asm!(
            "svc #0",
            in("r7") 0x0f0002usize,     // __ARM_NR_cacheflush
            in("r0") ptr,
            in("r1") ptr + PAGE_SIZE,
            in("r2") 0usize,
            lateout("r0") _,
            clobber_abi("system"),
        );
    }
    Some(ptr as *const u8)
}

#[cfg(target_os = "macos")]
pub fn alloc_executable_page(init: impl FnOnce(&mut [u8])) -> Option<*const u8> {
    #[link(name = "System")]
    unsafe extern "C" {
        fn mmap(addr: *mut u8, len: usize, prot: i32, flags: i32, fd: i32, offset: i64) -> *mut u8;
        fn mprotect(addr: *mut u8, len: usize, prot: i32) -> i32;
        fn sys_icache_invalidate(start: *const u8, len: usize);
    }
    const PAGE_SIZE: usize = 16384; // Apple Silicon uses 16 KB pages
    let ptr = unsafe { mmap(core::ptr::null_mut(), PAGE_SIZE, 3, 0x1002, -1, 0) };
    if ptr.is_null() || ptr as usize == usize::MAX {
        return None;
    }
    let slice = unsafe { core::slice::from_raw_parts_mut(ptr, PAGE_SIZE) };
    init(slice);
    if unsafe { mprotect(ptr, PAGE_SIZE, 5) } != 0 {
        return None;
    }
    unsafe { sys_icache_invalidate(ptr as *const u8, PAGE_SIZE) };
    Some(ptr as *const u8)
}

fn last_dlerror() -> String {
    let p = unsafe { dlerror() };
    if p.is_null() {
        return String::from("unknown dynamic linker error");
    }
    let mut len = 0;
    while unsafe { *p.add(len) } != 0 {
        len += 1;
    }
    let slice = unsafe { core::slice::from_raw_parts(p, len) };
    String::from(core::str::from_utf8(slice).unwrap_or("non-utf8 dlerror"))
}

pub fn open(path: &str, mut flags: i32) -> Result<u64, String> {
    if flags == 0 {
        flags = 2; // RTLD_NOW
    }
    let mut c_path = Vec::from(path.as_bytes());
    c_path.push(0);

    // clear any pending dlerror
    unsafe { dlerror() };

    let h = unsafe { dlopen(c_path.as_ptr(), flags) };
    if h == 0 {
        return Err(last_dlerror());
    }
    Ok(h as u64)
}

pub fn sym(handle: u64, name: &str) -> Result<u64, String> {
    let mut c_name = Vec::from(name.as_bytes());
    c_name.push(0);

    unsafe { dlerror() };

    let s = unsafe { dlsym(handle as usize, c_name.as_ptr()) };
    if s == 0 {
        return Err(last_dlerror());
    }
    Ok(s as u64)
}

pub fn close(handle: u64) -> Result<(), String> {
    unsafe { dlerror() };
    let rc = unsafe { dlclose(handle as usize) };
    if rc != 0 {
        return Err(last_dlerror());
    }
    Ok(())
}

pub fn alloc(size: u64) -> Result<u64, String> {
    let p = unsafe { c_malloc(size.max(1) as usize) };
    if p == 0 {
        return Err(String::from("malloc failed"));
    }
    Ok(p as u64)
}

pub fn free(ptr: u64) -> Result<(), String> {
    if ptr != 0 {
        unsafe { c_free(ptr as usize) };
    }
    Ok(())
}

pub fn read_mem(ptr: u64, len: u32) -> Result<Vec<u8>, String> {
    if ptr < 4096 {
        return Err(String::from("invalid memory address"));
    }
    let mut out = alloc::vec![0u8; len as usize];
    unsafe {
        core::ptr::copy_nonoverlapping(ptr as *const u8, out.as_mut_ptr(), len as usize);
    }
    Ok(out)
}

pub fn write_mem(ptr: u64, data: &[u8]) -> Result<(), String> {
    if ptr < 4096 {
        return Err(String::from("invalid memory address"));
    }
    unsafe {
        core::ptr::copy_nonoverlapping(data.as_ptr(), ptr as *mut u8, data.len());
    }
    Ok(())
}
