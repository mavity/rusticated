use core::ffi::{c_char, c_void};
use core::sync::atomic::{AtomicBool, AtomicI32, AtomicPtr, AtomicUsize, Ordering};

#[repr(C)]
pub struct UnixPlatformHost {
    pub load_library: Option<unsafe extern "C" fn(*const c_char, i32) -> *mut c_void>,
    pub get_proc_address: Option<unsafe extern "C" fn(*mut c_void, *const c_char) -> *mut c_void>,
    pub alloc: Option<unsafe extern "C" fn(usize) -> *mut c_void>,
    pub free: Option<unsafe extern "C" fn(*mut c_void, usize)>,
    pub flush_cache: Option<unsafe extern "C" fn(*mut c_void, usize)>,
    pub set_jit_write_protect: Option<unsafe extern "C" fn(i32)>,
    pub hello_payload: *const u8,
    pub hello_payload_size: usize,
    pub argv: *const *const u8,
    pub argc: usize,
}

type PlatformEntry = unsafe extern "C" fn() -> i32;
const PREFIX_ABI_OFFSET: usize = 0x20000;
const THREAD_STACK_SIZE: usize = 1024 * 1024;
const THREAD_SLOT_COUNT: usize = 8;
const CLONE_VM: usize = 0x00000100;
const CLONE_FS: usize = 0x00000200;
const CLONE_FILES: usize = 0x00000400;
const CLONE_SIGHAND: usize = 0x00000800;
const CLONE_THREAD: usize = 0x00010000;
const CLONE_SYSVSEM: usize = 0x00040000;
const CLONE_PARENT_SETTID: usize = 0x00100000;
const CLONE_CHILD_CLEARTID: usize = 0x00200000;
const EAGAIN: i32 = 11;
const EINTR: i32 = 4;
const EINVAL: i32 = 22;
const ENOMEM: i32 = 12;
const ESRCH: i32 = 3;
const EPERM: i32 = 1;
const EBUSY: i32 = 16;
const FUTEX_WAIT: usize = 0;
const FUTEX_WAKE: usize = 1;

type ThreadStartFn = unsafe extern "C" fn(*mut c_void) -> *mut c_void;

struct ThreadRecord {
    active: AtomicBool,
    child_tid: AtomicI32,
    thread_id: AtomicUsize,
    stack: AtomicPtr<c_void>,
    errno: AtomicI32,
}

impl ThreadRecord {
    const fn new() -> Self {
        Self {
            active: AtomicBool::new(false),
            child_tid: AtomicI32::new(0),
            thread_id: AtomicUsize::new(0),
            stack: AtomicPtr::new(core::ptr::null_mut()),
            errno: AtomicI32::new(0),
        }
    }
}

static mut THREADS: [ThreadRecord; THREAD_SLOT_COUNT] =
    [const { ThreadRecord::new() }; THREAD_SLOT_COUNT];
static mut MAIN_THREAD_ERRNO: i32 = 0;

// ─── Syscall primitives ──────────────────────────────────────────────────────

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_read(fd: i32, buf: *mut u8, n: usize) -> isize {
    let ret: isize;
    core::arch::asm!(
        "syscall",
        in("rax") 0usize,   // SYS_read
        in("rdi") fd as usize,
        in("rsi") buf as usize,
        in("rdx") n,
        lateout("rax") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_read(fd: i32, buf: *mut u8, n: usize) -> isize {
    let ret: isize;
    core::arch::asm!(
        "svc #0",
        in("x8") 63usize,   // SYS_read
        in("x0") fd as usize,
        in("x1") buf as usize,
        in("x2") n,
        lateout("x0") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_write(fd: i32, buf: *const u8, n: usize) -> isize {
    let ret: isize;
    core::arch::asm!(
        "syscall",
        in("rax") 1usize,   // SYS_write
        in("rdi") fd as usize,
        in("rsi") buf as usize,
        in("rdx") n,
        lateout("rax") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_write(fd: i32, buf: *const u8, n: usize) -> isize {
    let ret: isize;
    core::arch::asm!(
        "svc #0",
        in("x8") 64usize,   // SYS_write
        in("x0") fd as usize,
        in("x1") buf as usize,
        in("x2") n,
        lateout("x0") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_open(path: *const u8, flags: i32, mode: u32) -> i32 {
    let ret: isize;
    core::arch::asm!(
        "syscall",
        in("rax") 2usize,   // SYS_open
        in("rdi") path as usize,
        in("rsi") flags as usize,
        in("rdx") mode as usize,
        lateout("rax") ret,
        clobber_abi("system"),
    );
    ret as i32
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_open(path: *const u8, flags: i32, mode: u32) -> i32 {
    let ret: isize;
    core::arch::asm!(
        "svc #0",
        in("x8") 56usize,               // SYS_openat
        in("x0") (-100isize) as usize,   // AT_FDCWD
        in("x1") path as usize,
        in("x2") flags as usize,
        in("x3") mode as usize,
        lateout("x0") ret,
        clobber_abi("system"),
    );
    ret as i32
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_close(fd: i32) {
    let _: isize;
    core::arch::asm!(
        "syscall",
        in("rax") 3usize,   // SYS_close
        in("rdi") fd as usize,
        lateout("rax") _,
        clobber_abi("system"),
    );
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_close(fd: i32) {
    let _: isize;
    core::arch::asm!(
        "svc #0",
        in("x8") 57usize,   // SYS_close
        in("x0") fd as usize,
        lateout("x0") _,
        clobber_abi("system"),
    );
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_lseek(fd: i32, offset: i64, whence: i32) -> i64 {
    let ret: i64;
    core::arch::asm!(
        "syscall",
        in("rax") 8usize,   // SYS_lseek
        in("rdi") fd as usize,
        in("rsi") offset as usize,
        in("rdx") whence as usize,
        lateout("rax") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_pread(fd: i32, buf: *mut c_void, n: usize, offset: i64) -> isize {
    let ret: isize;
    core::arch::asm!(
        "syscall",
        in("rax") 17usize, // SYS_pread64
        in("rdi") fd as usize,
        in("rsi") buf as usize,
        in("rdx") n,
        in("r10") offset as usize,
        lateout("rax") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_pread(fd: i32, buf: *mut c_void, n: usize, offset: i64) -> isize {
    let ret: isize;
    core::arch::asm!(
        "svc #0",
        in("x8") 67usize, // SYS_pread64
        in("x0") fd as usize,
        in("x1") buf as usize,
        in("x2") n,
        in("x3") offset as usize,
        lateout("x0") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_ftruncate(fd: i32, length: i64) -> i32 {
    let ret: isize;
    core::arch::asm!(
        "syscall",
        in("rax") 77usize, // SYS_ftruncate
        in("rdi") fd as usize,
        in("rsi") length as usize,
        lateout("rax") ret,
        clobber_abi("system"),
    );
    ret as i32
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_ftruncate(fd: i32, length: i64) -> i32 {
    let ret: isize;
    core::arch::asm!(
        "svc #0",
        in("x8") 46usize, // SYS_ftruncate
        in("x0") fd as usize,
        in("x1") length as usize,
        lateout("x0") ret,
        clobber_abi("system"),
    );
    ret as i32
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_lseek(fd: i32, offset: i64, whence: i32) -> i64 {
    let ret: i64;
    core::arch::asm!(
        "svc #0",
        in("x8") 62usize,   // SYS_lseek
        in("x0") fd as usize,
        in("x1") offset as usize,
        in("x2") whence as usize,
        lateout("x0") ret,
        clobber_abi("system"),
    );
    ret
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_unlink(path: *const u8) {
    let _: isize;
    core::arch::asm!(
        "syscall",
        in("rax") 87usize,  // SYS_unlink
        in("rdi") path as usize,
        lateout("rax") _,
        clobber_abi("system"),
    );
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_unlink(path: *const u8) {
    let _: isize;
    core::arch::asm!(
        "svc #0",
        in("x8") 35usize,               // SYS_unlinkat
        in("x0") (-100isize) as usize,   // AT_FDCWD
        in("x1") path as usize,
        in("x2") 0usize,                // flags
        lateout("x0") _,
        clobber_abi("system"),
    );
}

#[cfg(target_arch = "x86_64")]
pub unsafe fn sys_exit(code: i32) -> ! {
    core::arch::asm!(
        "syscall",
        in("rax") 60usize,  // SYS_exit
        in("rdi") code as usize,
        options(noreturn),
    );
}

#[cfg(target_arch = "aarch64")]
pub unsafe fn sys_exit(code: i32) -> ! {
    core::arch::asm!(
        "svc #0",
        in("x8") 93usize,   // SYS_exit
        in("x0") code as usize,
        options(noreturn),
    );
}

#[cfg(target_arch = "x86_64")]
#[unsafe(naked)]
unsafe extern "C" fn clone_thread(
    _flags: usize,
    _stack: *mut u8,
    _child_tid: *mut i32,
    _start: ThreadStartFn,
    _arg: *mut c_void,
) -> isize {
    core::arch::naked_asm!(
        "push r12",
        "push r13",
        "mov r12, rcx",
        "mov r13, r8",
        "mov r10, rdx",
        "xor r8d, r8d",
        "mov eax, 56",
        "syscall",
        "test rax, rax",
        "jz 2f",
        "pop r13",
        "pop r12",
        "ret",
        "2:",
        "mov rdi, r13",
        "call r12",
        "mov eax, 60",
        "xor edi, edi",
        "syscall",
        "ud2",
    )
}

#[cfg(target_arch = "aarch64")]
#[unsafe(naked)]
unsafe extern "C" fn clone_thread(
    _flags: usize,
    _stack: *mut u8,
    _child_tid: *mut i32,
    _start: ThreadStartFn,
    _arg: *mut c_void,
) -> isize {
    core::arch::naked_asm!(
        "stp x19, x20, [sp, #-16]!",
        "mov x19, x3",
        "mov x20, x4",
        "mov x4, x2",
        "mov x3, xzr",
        "mov x8, #220",
        "svc #0",
        "cbz x0, 2f",
        "ldp x19, x20, [sp], #16",
        "ret",
        "2:",
        "mov x0, x20",
        "blr x19",
        "mov x0, #0",
        "mov x8, #93",
        "svc #0",
        "brk #0",
    )
}

unsafe fn alloc_thread_stack() -> *mut c_void {
    #[cfg(target_arch = "x86_64")]
    let result: isize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "syscall",
        in("rax") 9usize, // SYS_mmap
        in("rdi") 0usize,
        in("rsi") THREAD_STACK_SIZE,
        in("rdx") 3usize, // PROT_READ | PROT_WRITE
        in("r10") 0x22usize, // MAP_PRIVATE | MAP_ANONYMOUS
        in("r8") (-1i64) as usize,
        in("r9") 0usize,
        lateout("rax") result,
        clobber_abi("system"),
    );
    #[cfg(target_arch = "aarch64")]
    let result: isize;
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "svc #0",
        in("x8") 222usize, // SYS_mmap
        in("x0") 0usize,
        in("x1") THREAD_STACK_SIZE,
        in("x2") 3usize, // PROT_READ | PROT_WRITE
        in("x3") 0x22usize, // MAP_PRIVATE | MAP_ANONYMOUS
        in("x4") (-1i64) as usize,
        in("x5") 0usize,
        lateout("x0") result,
        clobber_abi("system"),
    );
    if result < 0 && result >= -4096 {
        core::ptr::null_mut()
    } else {
        result as *mut c_void
    }
}

unsafe fn futex_wait(address: *mut i32, expected: i32) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let result: isize;
        core::arch::asm!(
            "syscall",
            in("rax") 202usize, // SYS_futex
            in("rdi") address as usize,
            in("rsi") FUTEX_WAIT,
            in("rdx") expected as usize,
            in("r10") 0usize, // timeout = NULL
            lateout("rax") result,
            clobber_abi("system"),
        );
        result
    }
    #[cfg(target_arch = "aarch64")]
    {
        let result: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") 98usize, // SYS_futex
            in("x0") address as usize,
            in("x1") FUTEX_WAIT,
            in("x2") expected as usize,
            in("x3") 0usize, // timeout = NULL
            lateout("x0") result,
            clobber_abi("system"),
        );
        result
    }
}

unsafe fn futex_wake(address: *mut i32, count: i32) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let result: isize;
        core::arch::asm!(
            "syscall",
            in("rax") 202usize,
            in("rdi") address as usize,
            in("rsi") FUTEX_WAKE,
            in("rdx") count as usize,
            lateout("rax") result,
            clobber_abi("system"),
        );
        result
    }
    #[cfg(target_arch = "aarch64")]
    {
        let result: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") 98usize,
            in("x0") address as usize,
            in("x1") FUTEX_WAKE,
            in("x2") count as usize,
            lateout("x0") result,
            clobber_abi("system"),
        );
        result
    }
}

unsafe fn sys_gettid() -> usize {
    #[cfg(target_arch = "x86_64")]
    {
        let result: isize;
        core::arch::asm!(
            "syscall",
            in("rax") 186usize,
            lateout("rax") result,
            clobber_abi("system"),
        );
        result as usize
    }
    #[cfg(target_arch = "aarch64")]
    {
        let result: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") 178usize,
            lateout("x0") result,
            clobber_abi("system"),
        );
        result as usize
    }
}

pub unsafe fn alloc_executable(size: usize) -> *mut c_void {
    let size = (size + 4095) & !4095;
    #[cfg(target_arch = "x86_64")]
    let res: usize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "syscall",
        in("rax") 9usize,           // SYS_mmap
        in("rdi") 0usize,           // addr = NULL
        in("rsi") size,             // len
        in("rdx") 7usize,           // PROT_READ | PROT_WRITE | PROT_EXEC
        in("r10") 0x22usize,        // MAP_PRIVATE | MAP_ANONYMOUS
        in("r8") (-1i64) as usize,  // fd = -1
        in("r9") 0usize,            // offset = 0
        lateout("rax") res,
        clobber_abi("system"),
    );
    #[cfg(target_arch = "aarch64")]
    let res: usize;
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "svc #0",
        in("x8") 222usize,          // SYS_mmap
        in("x0") 0usize,            // addr = NULL
        in("x1") size,              // len
        in("x2") 7usize,            // PROT_READ | PROT_WRITE | PROT_EXEC
        in("x3") 0x22usize,         // MAP_PRIVATE | MAP_ANONYMOUS
        in("x4") (-1i64) as usize,  // fd = -1
        in("x5") 0usize,            // offset = 0
        lateout("x0") res,
        clobber_abi("system"),
    );
    if (res as isize) < 0 && (res as isize) >= -4096 {
        core::ptr::null_mut()
    } else {
        res as *mut c_void
    }
}

pub unsafe fn free_executable(ptr: *mut c_void, size: usize) {
    if !ptr.is_null() {
        let size = (size + 4095) & !4095;
        #[cfg(target_arch = "x86_64")]
        {
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
        #[cfg(target_arch = "aarch64")]
        {
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
}

unsafe fn map_pal_memory(size: usize) -> *mut c_void {
    let Some(size) = size.checked_add(4095).map(|size| size & !4095) else {
        return core::ptr::null_mut();
    };
    #[cfg(target_arch = "x86_64")]
    let result: isize;
    #[cfg(target_arch = "x86_64")]
    core::arch::asm!(
        "syscall",
        in("rax") 9usize, // SYS_mmap
        in("rdi") 0usize,
        in("rsi") size,
        in("rdx") 3usize, // PROT_READ | PROT_WRITE
        in("r10") 0x22usize, // MAP_PRIVATE | MAP_ANONYMOUS
        in("r8") (-1i64) as usize,
        in("r9") 0usize,
        lateout("rax") result,
        clobber_abi("system"),
    );
    #[cfg(target_arch = "aarch64")]
    let result: isize;
    #[cfg(target_arch = "aarch64")]
    core::arch::asm!(
        "svc #0",
        in("x8") 222usize, // SYS_mmap
        in("x0") 0usize,
        in("x1") size,
        in("x2") 3usize, // PROT_READ | PROT_WRITE
        in("x3") 0x22usize, // MAP_PRIVATE | MAP_ANONYMOUS
        in("x4") (-1i64) as usize,
        in("x5") 0usize,
        lateout("x0") result,
        clobber_abi("system"),
    );
    if result < 0 && result >= -4095 {
        core::ptr::null_mut()
    } else {
        result as *mut c_void
    }
}

unsafe fn unmap_pal_memory(ptr: *mut c_void, size: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let result: isize;
        core::arch::asm!(
            "syscall",
            in("rax") 11usize, // SYS_munmap
            in("rdi") ptr as usize,
            in("rsi") size,
            lateout("rax") result,
            clobber_abi("system"),
        );
        result
    }
    #[cfg(target_arch = "aarch64")]
    {
        let result: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") 215usize, // SYS_munmap
            in("x0") ptr as usize,
            in("x1") size,
            lateout("x0") result,
            clobber_abi("system"),
        );
        result
    }
}

unsafe extern "C" fn pal_malloc_cb(size: usize) -> *mut c_void {
    let Some(total) = size
        .checked_add(core::mem::size_of::<usize>())
        .and_then(|total| total.checked_add(4095))
        .map(|total| total & !4095)
    else {
        thread_errno_ptr().write_volatile(ENOMEM);
        return core::ptr::null_mut();
    };
    let base = map_pal_memory(total);
    if base.is_null() {
        thread_errno_ptr().write_volatile(ENOMEM);
        return core::ptr::null_mut();
    }
    base.cast::<usize>().write(total);
    base.cast::<usize>().add(1).cast()
}

unsafe extern "C" fn pal_free_cb(ptr: *mut c_void) {
    if ptr.is_null() {
        return;
    }
    let base = ptr.cast::<usize>().sub(1);
    let size = base.read();
    let result = unmap_pal_memory(base.cast(), size);
    if result < 0 && result >= -4095 {
        thread_errno_ptr().write_volatile((-result) as i32);
    }
}

pub unsafe fn flush_instruction_cache(memory: *mut c_void, size: usize) {
    #[cfg(target_arch = "aarch64")]
    {
        let start = memory as usize;
        let end = start.saturating_add(size);
        let ctr: u64;
        core::arch::asm!("mrs {0}, ctr_el0", out(reg) ctr);
        let data_line = 4usize << ((ctr >> 16) & 0xf);
        let instruction_line = 4usize << (ctr & 0xf);
        let mut address = start & !(data_line - 1);
        while address < end {
            core::arch::asm!("dc cvau, {0}", in(reg) address, options(nostack));
            address += data_line;
        }
        core::arch::asm!("dsb ish", options(nostack));
        address = start & !(instruction_line - 1);
        while address < end {
            core::arch::asm!("ic ivau, {0}", in(reg) address, options(nostack));
            address += instruction_line;
        }
        core::arch::asm!("dsb ish", "isb", options(nostack));
    }
    #[cfg(target_arch = "x86_64")]
    let _ = (memory, size);
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

unsafe fn streq(a: *const c_char, b: &[u8]) -> bool {
    let mut i = 0;
    while i < b.len() {
        if *a.add(i) as u8 != b[i] {
            return false;
        }
        i += 1;
    }
    true
}

unsafe extern "C" fn load_library(_name: *const c_char, _flags: i32) -> *mut c_void {
    1 as *mut c_void
}

unsafe extern "C" fn sys_read_cb(fd: i32, buf: *mut c_void, count: usize) -> isize {
    let result = sys_read(fd, buf as *mut u8, count);
    if result < 0 && result >= -4095 {
        thread_errno_ptr().write_volatile((-result) as i32);
        -1
    } else {
        result
    }
}

unsafe extern "C" fn sys_write_cb(fd: i32, buf: *const c_void, count: usize) -> isize {
    let result = sys_write(fd, buf as *const u8, count);
    if result < 0 && result >= -4095 {
        thread_errno_ptr().write_volatile((-result) as i32);
        -1
    } else {
        result
    }
}

unsafe extern "C" fn sys_pread_cb(fd: i32, buf: *mut c_void, count: usize, offset: i64) -> isize {
    let result = sys_pread(fd, buf, count, offset);
    if result < 0 && result >= -4095 {
        thread_errno_ptr().write_volatile((-result) as i32);
        -1
    } else {
        result
    }
}

unsafe extern "C" fn sys_open_cb(path: *const c_char, flags: i32, mode: u32) -> i32 {
    let result = sys_open(path.cast(), flags, mode);
    if result < 0 && result >= -4095 {
        thread_errno_ptr().write_volatile(-result);
        -1
    } else {
        result
    }
}

unsafe extern "C" fn sys_lseek_cb(fd: i32, offset: i64, whence: i32) -> i64 {
    let result = sys_lseek(fd, offset, whence);
    if result < 0 && result >= -4095 {
        thread_errno_ptr().write_volatile((-result) as i32);
        -1
    } else {
        result
    }
}

unsafe extern "C" fn sys_ftruncate_cb(fd: i32, length: i64) -> i32 {
    let result = sys_ftruncate(fd, length);
    if result < 0 && result >= -4095 {
        thread_errno_ptr().write_volatile(-result);
        -1
    } else {
        result
    }
}

unsafe extern "C" fn sys_close_cb(fd: i32) -> i32 {
    sys_close(fd);
    0
}

unsafe fn thread_errno_ptr() -> *mut i32 {
    let tid = sys_gettid();
    let threads = core::ptr::addr_of!(THREADS).cast::<ThreadRecord>();
    for index in 0..THREAD_SLOT_COUNT {
        let record = &*threads.add(index);
        if tid != 0
            && record.active.load(Ordering::Acquire)
            && (record.thread_id.load(Ordering::Acquire) == tid
                || record.child_tid.load(Ordering::Acquire) as usize == tid)
        {
            return record.errno.as_ptr();
        }
    }
    core::ptr::addr_of_mut!(MAIN_THREAD_ERRNO)
}

unsafe extern "C" fn errno_location_cb() -> *mut i32 {
    thread_errno_ptr()
}

unsafe fn atomic_state(storage: *mut c_void) -> Result<*mut AtomicI32, i32> {
    if storage.is_null() || (storage as usize) % core::mem::align_of::<AtomicI32>() != 0 {
        Err(EINVAL)
    } else {
        Ok(storage.cast())
    }
}

unsafe extern "C" fn pthread_mutex_init_cb(mutex: *mut c_void, attr: *const c_void) -> i32 {
    if !attr.is_null() {
        return EINVAL;
    }
    let state = match atomic_state(mutex) {
        Ok(state) => state,
        Err(error) => return error,
    };
    (*state).store(0, Ordering::Relaxed);
    0
}

unsafe extern "C" fn pthread_mutex_destroy_cb(mutex: *mut c_void) -> i32 {
    let state = match atomic_state(mutex) {
        Ok(state) => state,
        Err(error) => return error,
    };
    if (*state).load(Ordering::Acquire) == 0 {
        0
    } else {
        EBUSY
    }
}

unsafe extern "C" fn pthread_mutex_lock_cb(mutex: *mut c_void) -> i32 {
    let state = match atomic_state(mutex) {
        Ok(state) => state,
        Err(error) => return error,
    };
    if (*state)
        .compare_exchange(0, 1, Ordering::Acquire, Ordering::Relaxed)
        .is_ok()
    {
        return 0;
    }
    loop {
        if (*state).swap(2, Ordering::Acquire) == 0 {
            return 0;
        }
        let result = futex_wait(state.cast(), 2);
        if result < 0 {
            let error = (-result) as i32;
            if error != EINTR && error != EAGAIN {
                return error;
            }
        }
    }
}

unsafe extern "C" fn pthread_mutex_unlock_cb(mutex: *mut c_void) -> i32 {
    let state = match atomic_state(mutex) {
        Ok(state) => state,
        Err(error) => return error,
    };
    match (*state).swap(0, Ordering::Release) {
        0 => EPERM,
        1 => 0,
        _ => {
            let result = futex_wake(state.cast(), 1);
            if result < 0 {
                (-result) as i32
            } else {
                0
            }
        }
    }
}

unsafe extern "C" fn pthread_cond_init_cb(cond: *mut c_void, attr: *const c_void) -> i32 {
    if !attr.is_null() {
        return EINVAL;
    }
    let sequence = match atomic_state(cond) {
        Ok(sequence) => sequence,
        Err(error) => return error,
    };
    (*sequence).store(0, Ordering::Relaxed);
    0
}

unsafe extern "C" fn pthread_cond_destroy_cb(cond: *mut c_void) -> i32 {
    match atomic_state(cond) {
        Ok(_) => 0,
        Err(error) => error,
    }
}

unsafe extern "C" fn pthread_cond_wait_cb(cond: *mut c_void, mutex: *mut c_void) -> i32 {
    let sequence = match atomic_state(cond) {
        Ok(sequence) => sequence,
        Err(error) => return error,
    };
    if let Err(error) = atomic_state(mutex) {
        return error;
    }
    let observed = (*sequence).load(Ordering::Acquire);
    let unlock_result = pthread_mutex_unlock_cb(mutex);
    if unlock_result != 0 {
        return unlock_result;
    }
    loop {
        if (*sequence).load(Ordering::Acquire) != observed {
            break;
        }
        let result = futex_wait(sequence.cast(), observed);
        if result < 0 {
            let error = (-result) as i32;
            if error != EINTR && error != EAGAIN {
                let lock_result = pthread_mutex_lock_cb(mutex);
                if lock_result != 0 {
                    return lock_result;
                }
                return error;
            }
        }
    }
    pthread_mutex_lock_cb(mutex)
}

unsafe extern "C" fn pthread_cond_signal_cb(cond: *mut c_void) -> i32 {
    let sequence = match atomic_state(cond) {
        Ok(sequence) => sequence,
        Err(error) => return error,
    };
    (*sequence).fetch_add(1, Ordering::Release);
    let result = futex_wake(sequence.cast(), 1);
    if result < 0 {
        (-result) as i32
    } else {
        0
    }
}

unsafe extern "C" fn pthread_cond_broadcast_cb(cond: *mut c_void) -> i32 {
    let sequence = match atomic_state(cond) {
        Ok(sequence) => sequence,
        Err(error) => return error,
    };
    (*sequence).fetch_add(1, Ordering::Release);
    let result = futex_wake(sequence.cast(), i32::MAX);
    if result < 0 {
        (-result) as i32
    } else {
        0
    }
}

unsafe extern "C" fn pthread_create_cb(
    thread: *mut usize,
    attr: *const c_void,
    start: Option<ThreadStartFn>,
    arg: *mut c_void,
) -> i32 {
    if thread.is_null() || !attr.is_null() {
        return EINVAL;
    }
    let Some(start) = start else {
        return EINVAL;
    };
    let threads = core::ptr::addr_of!(THREADS).cast::<ThreadRecord>();
    let mut slot = None;
    for index in 0..THREAD_SLOT_COUNT {
        let record = &*threads.add(index);
        if record
            .active
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Relaxed)
            .is_ok()
        {
            slot = Some(record);
            break;
        }
    }
    let Some(record) = slot else {
        return EAGAIN;
    };

    let stack = alloc_thread_stack();
    if stack.is_null() {
        record.active.store(false, Ordering::Release);
        return ENOMEM;
    }
    record.child_tid.store(0, Ordering::Relaxed);
    record.thread_id.store(0, Ordering::Relaxed);
    record.stack.store(stack, Ordering::Relaxed);
    record.errno.store(0, Ordering::Relaxed);
    let flags = CLONE_VM
        | CLONE_FS
        | CLONE_FILES
        | CLONE_SIGHAND
        | CLONE_THREAD
        | CLONE_SYSVSEM
        | CLONE_PARENT_SETTID
        | CLONE_CHILD_CLEARTID;
    let stack_top = stack.add(THREAD_STACK_SIZE).cast::<u8>();
    let result = clone_thread(flags, stack_top, record.child_tid.as_ptr(), start, arg);
    if result <= 0 {
        record.stack.store(core::ptr::null_mut(), Ordering::Relaxed);
        record.active.store(false, Ordering::Release);
        free_executable(stack, THREAD_STACK_SIZE);
        return if result < 0 { (-result) as i32 } else { EAGAIN };
    }
    let id = result as usize;
    record.thread_id.store(id, Ordering::Release);
    thread.write(id);
    0
}

unsafe extern "C" fn pthread_join_cb(thread: usize, retval: *mut *mut c_void) -> i32 {
    if thread == 0 {
        return ESRCH;
    }
    let threads = core::ptr::addr_of!(THREADS).cast::<ThreadRecord>();
    let mut found = None;
    for index in 0..THREAD_SLOT_COUNT {
        let record = &*threads.add(index);
        if record.active.load(Ordering::Acquire)
            && record.thread_id.load(Ordering::Acquire) == thread
        {
            found = Some(record);
            break;
        }
    }
    let Some(record) = found else {
        return ESRCH;
    };
    loop {
        let current_tid = record.child_tid.load(Ordering::Acquire);
        if current_tid == 0 {
            break;
        }
        let result = futex_wait(record.child_tid.as_ptr(), current_tid);
        if result < 0 {
            let error = (-result) as i32;
            if error == EAGAIN || error == EINTR {
                continue;
            }
            return error;
        }
    }
    if !retval.is_null() {
        retval.write(core::ptr::null_mut());
    }
    let stack = record.stack.swap(core::ptr::null_mut(), Ordering::AcqRel);
    record.thread_id.store(0, Ordering::Relaxed);
    record.errno.store(0, Ordering::Relaxed);
    free_executable(stack, THREAD_STACK_SIZE);
    record.active.store(false, Ordering::Release);
    0
}

unsafe extern "C" fn sys_exit_cb(status: i32) -> ! {
    sys_exit(status)
}

unsafe extern "C" fn get_proc_address(_module: *mut c_void, name: *const c_char) -> *mut c_void {
    if streq(name, b"read\0") {
        sys_read_cb as *mut c_void
    } else if streq(name, b"write\0") {
        sys_write_cb as *mut c_void
    } else if streq(name, b"pread\0") {
        sys_pread_cb as *mut c_void
    } else if streq(name, b"open\0") {
        sys_open_cb as *mut c_void
    } else if streq(name, b"lseek\0") {
        sys_lseek_cb as *mut c_void
    } else if streq(name, b"ftruncate\0") {
        sys_ftruncate_cb as *mut c_void
    } else if streq(name, b"close\0") {
        sys_close_cb as *mut c_void
    } else if streq(name, b"malloc\0") {
        pal_malloc_cb as *mut c_void
    } else if streq(name, b"free\0") {
        pal_free_cb as *mut c_void
    } else if streq(name, b"__errno_location\0") {
        errno_location_cb as *mut c_void
    } else if streq(name, b"pthread_create\0") {
        pthread_create_cb as *mut c_void
    } else if streq(name, b"pthread_join\0") {
        pthread_join_cb as *mut c_void
    } else if streq(name, b"pthread_mutex_init\0") {
        pthread_mutex_init_cb as *mut c_void
    } else if streq(name, b"pthread_mutex_destroy\0") {
        pthread_mutex_destroy_cb as *mut c_void
    } else if streq(name, b"pthread_mutex_lock\0") {
        pthread_mutex_lock_cb as *mut c_void
    } else if streq(name, b"pthread_mutex_unlock\0") {
        pthread_mutex_unlock_cb as *mut c_void
    } else if streq(name, b"pthread_cond_init\0") {
        pthread_cond_init_cb as *mut c_void
    } else if streq(name, b"pthread_cond_destroy\0") {
        pthread_cond_destroy_cb as *mut c_void
    } else if streq(name, b"pthread_cond_wait\0") {
        pthread_cond_wait_cb as *mut c_void
    } else if streq(name, b"pthread_cond_signal\0") {
        pthread_cond_signal_cb as *mut c_void
    } else if streq(name, b"pthread_cond_broadcast\0") {
        pthread_cond_broadcast_cb as *mut c_void
    } else if streq(name, b"_exit\0") || streq(name, b"exit\0") {
        sys_exit_cb as *mut c_void
    } else {
        core::ptr::null_mut()
    }
}

unsafe extern "C" fn alloc_cb(size: usize) -> *mut c_void {
    alloc_executable(size)
}

unsafe extern "C" fn free_cb(ptr: *mut c_void, size: usize) {
    free_executable(ptr, size)
}

unsafe extern "C" fn flush_cb(ptr: *mut c_void, size: usize) {
    flush_instruction_cache(ptr, size)
}

fn parse_u64(s: *const u8) -> Option<u64> {
    if s.is_null() {
        return None;
    }
    let mut val: u64 = 0;
    let mut p = s;
    unsafe {
        if *p == 0 {
            return None;
        }
        while *p >= b'0' && *p <= b'9' {
            val = val.checked_mul(10)?.checked_add((*p - b'0') as u64)?;
            p = p.add(1);
        }
        if *p != 0 {
            return None;
        }
    }
    Some(val)
}

#[allow(dead_code)]
unsafe fn sys_readlink_self(buf: *mut u8, bufsiz: usize) -> isize {
    #[cfg(target_arch = "x86_64")]
    {
        let ret: isize;
        core::arch::asm!(
            "syscall",
            in("rax") 89usize, // SYS_readlink
            in("rdi") b"/proc/self/exe\0".as_ptr() as usize,
            in("rsi") buf as usize,
            in("rdx") bufsiz,
            lateout("rax") ret,
            clobber_abi("system"),
        );
        ret
    }
    #[cfg(target_arch = "aarch64")]
    {
        let ret: isize;
        core::arch::asm!(
            "svc #0",
            in("x8") 78usize, // SYS_readlinkat
            in("x0") (-100isize) as usize, // AT_FDCWD
            in("x1") b"/proc/self/exe\0".as_ptr() as usize,
            in("x2") buf as usize,
            in("x3") bufsiz,
            lateout("x0") ret,
            clobber_abi("system"),
        );
        ret
    }
}

unsafe fn get_basename(path: *const u8, len: usize) -> (*const u8, usize) {
    if path.is_null() || len == 0 {
        return (path, 0);
    }
    let mut last_slash = None;
    let mut i = 0;
    while i < len {
        if *path.add(i) == b'/' {
            last_slash = Some(i);
        }
        i += 1;
    }
    match last_slash {
        Some(idx) => (path.add(idx + 1), len - idx - 1),
        None => (path, len),
    }
}

unsafe fn str_len(s: *const u8) -> usize {
    if s.is_null() {
        return 0;
    }
    let mut len = 0;
    while *s.add(len) != 0 {
        len += 1;
    }
    len
}

const SHELL_PREFIX: &[u8] = b"\xf0\x9f\x90\x9a\xef\xb8\x8e";

unsafe fn has_shell_prefix(ptr: *const u8, len: usize) -> bool {
    if len < SHELL_PREFIX.len() {
        return false;
    }
    let mut i = 0;
    while i < SHELL_PREFIX.len() {
        if *ptr.add(i) != SHELL_PREFIX[i] {
            return false;
        }
        i += 1;
    }
    true
}

unsafe fn print_err(msg: &[u8]) {
    sys_write(2, msg.as_ptr(), msg.len());
}

unsafe fn print_cstr(s: *const u8) {
    if !s.is_null() {
        let mut len = 0;
        while *s.add(len) != 0 {
            len += 1;
        }
        sys_write(2, s, len);
    }
}

// ─── Main Execution ──────────────────────────────────────────────────────────

pub unsafe fn run(sp: *const usize) -> ! {
    let argc = *sp;

    // Scan argv[0..argc] to find the LAST argument whose basename starts with "🐚︎"
    let mut last_match_idx = None;
    let mut i = 0;
    while i < argc {
        let arg_ptr = *(sp.add(1 + i)) as *const u8;
        let arg_len = str_len(arg_ptr);
        let (arg_base_ptr, arg_base_len) = get_basename(arg_ptr, arg_len);
        if has_shell_prefix(arg_base_ptr, arg_base_len) {
            last_match_idx = Some(i);
        }
        i += 1;
    }

    let Some(match_idx) = last_match_idx else {
        print_err(b"brot: error: could not locate temporary launcher in argv\n");
        sys_exit(101);
    };

    // Unlink the temporary launcher
    let launcher_path = *(sp.add(1 + match_idx)) as *const u8;
    sys_unlink(launcher_path);

    let bundle_idx = match_idx + 1;
    if bundle_idx + 4 >= argc {
        print_err(b"brot: error: missing required component offsets in argv\n");
        sys_exit(101);
    }

    let bundle_path = *(sp.add(1 + bundle_idx)) as *const u8;
    let plat_off_str = *(sp.add(1 + bundle_idx + 1)) as *const u8;
    let plat_len_str = *(sp.add(1 + bundle_idx + 2)) as *const u8;
    let hello_off_str = *(sp.add(1 + bundle_idx + 3)) as *const u8;
    let hello_len_str = *(sp.add(1 + bundle_idx + 4)) as *const u8;

    // 3. Parse component ranges
    let plat_off = match parse_u64(plat_off_str) {
        Some(v) => v as i64,
        None => {
            print_err(b"brot: error: invalid platform offset: [");
            print_cstr(plat_off_str);
            print_err(b"]\n");
            sys_exit(102);
        }
    };
    let plat_len = match parse_u64(plat_len_str) {
        Some(v) => v as usize,
        None => {
            print_err(b"brot: error: invalid platform length\n");
            sys_exit(103);
        }
    };
    let hello_off = match parse_u64(hello_off_str) {
        Some(v) => v as i64,
        None => {
            print_err(b"brot: error: invalid hello offset\n");
            sys_exit(104);
        }
    };
    let hello_len = match parse_u64(hello_len_str) {
        Some(v) => v as usize,
        None => {
            print_err(b"brot: error: invalid hello length\n");
            sys_exit(105);
        }
    };
    let guest_args_idx = bundle_idx + 5;
    let guest_argv = sp.add(1 + guest_args_idx).cast::<*const u8>();
    let guest_argc = argc - guest_args_idx;

    if plat_len == 0 || plat_len > 12 * 1024 || hello_len == 0 || hello_len > 65536 {
        print_err(b"brot: error: component length out of bounds\n");
        sys_exit(106);
    }

    // 4. Open bundle
    let fd = sys_open(bundle_path, 0, 0); // O_RDONLY = 0
    if fd < 0 {
        print_err(b"brot: error: cannot open bundle: ");
        print_cstr(bundle_path);
        print_err(b"\n");
        sys_exit(107);
    }

    // Read compressed platform
    let mut comp_plat = [0u8; 12 * 1024];
    if sys_lseek(fd, plat_off, 0) < 0 {
        print_err(b"brot: error: lseek platform offset failed\n");
        sys_exit(108);
    }
    let mut read_total = 0;
    while read_total < plat_len {
        let n = sys_read(
            fd,
            comp_plat.as_mut_ptr().add(read_total),
            plat_len - read_total,
        );
        if n <= 0 {
            print_err(b"brot: error: read platform bytes failed\n");
            sys_exit(109);
        }
        read_total += n as usize;
    }

    // Read compressed hello payload
    let mut comp_hello = [0u8; 65536];
    if sys_lseek(fd, hello_off, 0) < 0 {
        print_err(b"brot: error: lseek hello offset failed\n");
        sys_exit(110);
    }
    let mut read_total = 0;
    while read_total < hello_len {
        let n = sys_read(
            fd,
            comp_hello.as_mut_ptr().add(read_total),
            hello_len - read_total,
        );
        if n <= 0 {
            print_err(b"brot: error: read hello bytes failed\n");
            sys_exit(111);
        }
        read_total += n as usize;
    }
    sys_close(fd);

    // 4. Decompress components
    let mut dec_plat = [0u8; 12 * 1024];
    let plat_decompressed_len =
        match crate::decompress::decompress(&comp_plat[..plat_len], &mut dec_plat) {
            Ok(n) => n,
            Err(_) => {
                print_err(b"brot: error: decompress platform failed\n");
                sys_exit(112);
            }
        };

    let mut dec_hello = [0u8; 65536];
    let hello_decompressed_len =
        match crate::decompress::decompress(&comp_hello[..hello_len], &mut dec_hello) {
            Ok(n) => n,
            Err(_) => {
                print_err(b"brot: error: decompress hello payload failed\n");
                sys_exit(113);
            }
        };

    // 5. Allocate executable memory for platform (including 128 KiB prefix)
    let total_plat_size = PREFIX_ABI_OFFSET + plat_decompressed_len;
    let plat_alloc = alloc_executable(total_plat_size);
    if plat_alloc.is_null() {
        print_err(b"brot: error: allocate executable memory failed (mmap RWX)\n");
        sys_exit(114);
    }

    let plat_code = (plat_alloc as *mut u8).add(PREFIX_ABI_OFFSET) as *mut c_void;
    core::ptr::copy_nonoverlapping(
        dec_plat.as_ptr(),
        plat_code as *mut u8,
        plat_decompressed_len,
    );
    flush_instruction_cache(plat_code, plat_decompressed_len);

    // 6. Populate prefix ABI at -0x20000 and invoke PAL at +0x00000
    core::ptr::write(
        plat_alloc as *mut UnixPlatformHost,
        UnixPlatformHost {
            load_library: Some(load_library),
            get_proc_address: Some(get_proc_address),
            alloc: Some(alloc_cb),
            free: Some(free_cb),
            flush_cache: Some(flush_cb),
            set_jit_write_protect: None,
            hello_payload: dec_hello.as_ptr(),
            hello_payload_size: hello_decompressed_len,
            argv: guest_argv,
            argc: guest_argc,
        },
    );

    let entry: PlatformEntry = core::mem::transmute(plat_code);
    let status = entry();

    free_executable(plat_alloc, total_plat_size);
    sys_exit(status);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[repr(C, align(16))]
    struct SyncStorage {
        _words: [u64; 8],
    }

    struct TestContext {
        mutex: SyncStorage,
        cond: SyncStorage,
        started: AtomicUsize,
        completed: AtomicI32,
        error: AtomicI32,
        errno_pointers: [AtomicUsize; 4],
    }

    unsafe extern "C" fn sync_worker(arg: *mut c_void) -> *mut c_void {
        let context = &*arg.cast::<TestContext>();
        let index = context.started.fetch_add(1, Ordering::Relaxed);
        let errno = thread_errno_ptr();
        errno.write_volatile(index as i32 + 1);
        if index < context.errno_pointers.len() {
            context.errno_pointers[index].store(errno as usize, Ordering::Release);
        }

        let mutex = core::ptr::addr_of!(context.mutex).cast_mut().cast();
        let cond = core::ptr::addr_of!(context.cond).cast_mut().cast();
        let lock_result = pthread_mutex_lock_cb(mutex);
        if lock_result != 0 {
            context.error.store(lock_result, Ordering::Relaxed);
            return core::ptr::null_mut();
        }
        context.completed.fetch_add(1, Ordering::Release);
        let signal_result = pthread_cond_broadcast_cb(cond);
        let unlock_result = pthread_mutex_unlock_cb(mutex);
        let error = if signal_result != 0 {
            signal_result
        } else {
            unlock_result
        };
        if error != 0 {
            let _ = context
                .error
                .compare_exchange(0, error, Ordering::Relaxed, Ordering::Relaxed);
        }
        core::ptr::null_mut()
    }

    #[test]
    fn supports_concurrent_threads_and_futex_synchronization() {
        unsafe {
            let mut context = TestContext {
                mutex: SyncStorage { _words: [0; 8] },
                cond: SyncStorage { _words: [0; 8] },
                started: AtomicUsize::new(0),
                completed: AtomicI32::new(0),
                error: AtomicI32::new(0),
                errno_pointers: [const { AtomicUsize::new(0) }; 4],
            };
            let mutex = core::ptr::addr_of_mut!(context.mutex).cast();
            let cond = core::ptr::addr_of_mut!(context.cond).cast();
            assert_eq!(pthread_mutex_init_cb(mutex, core::ptr::null()), 0);
            assert_eq!(pthread_cond_init_cb(cond, core::ptr::null()), 0);
            assert_eq!(pthread_mutex_lock_cb(mutex), 0);

            let mut workers = [0usize; 4];
            for worker in &mut workers {
                assert_eq!(
                    pthread_create_cb(
                        worker,
                        core::ptr::null(),
                        Some(sync_worker),
                        core::ptr::addr_of_mut!(context).cast(),
                    ),
                    0
                );
            }
            while context.completed.load(Ordering::Acquire) < workers.len() as i32 {
                assert_eq!(pthread_cond_wait_cb(cond, mutex), 0);
            }
            assert_eq!(pthread_mutex_unlock_cb(mutex), 0);

            for worker in workers {
                assert_eq!(pthread_join_cb(worker, core::ptr::null_mut()), 0);
            }

            assert_eq!(context.error.load(Ordering::Relaxed), 0);
            let mut errno_pointers = [0usize; 4];
            for (index, pointer) in context.errno_pointers.iter().enumerate() {
                errno_pointers[index] = pointer.load(Ordering::Acquire);
                assert_ne!(errno_pointers[index], 0);
            }
            errno_pointers.sort_unstable();
            assert!(errno_pointers.windows(2).all(|pair| pair[0] != pair[1]));
            assert_eq!(pthread_cond_destroy_cb(cond), 0);
            assert_eq!(pthread_mutex_destroy_cb(mutex), 0);
        }
    }
}
