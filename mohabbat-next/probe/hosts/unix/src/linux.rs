use core::ffi::{c_char, c_void};

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
}

type PlatformEntry = unsafe extern "C" fn(*const UnixPlatformHost) -> i32;

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
    sys_read(fd, buf as *mut u8, count)
}

unsafe extern "C" fn sys_write_cb(fd: i32, buf: *const c_void, count: usize) -> isize {
    sys_write(fd, buf as *const u8, count)
}

unsafe extern "C" fn sys_exit_cb(status: i32) -> ! {
    sys_exit(status)
}

unsafe extern "C" fn get_proc_address(_module: *mut c_void, name: *const c_char) -> *mut c_void {
    if streq(name, b"read\0") {
        sys_read_cb as *mut c_void
    } else if streq(name, b"write\0") {
        sys_write_cb as *mut c_void
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

    if plat_len == 0 || plat_len > 8192 || hello_len == 0 || hello_len > 8192 {
        print_err(b"brot: error: component length out of bounds (0..8192)\n");
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
    let mut comp_plat = [0u8; 8192];
    if sys_lseek(fd, plat_off, 0) < 0 {
        print_err(b"brot: error: lseek platform offset failed\n");
        sys_exit(108);
    }
    let mut read_total = 0;
    while read_total < plat_len {
        let n = sys_read(fd, comp_plat.as_mut_ptr().add(read_total), plat_len - read_total);
        if n <= 0 {
            print_err(b"brot: error: read platform bytes failed\n");
            sys_exit(109);
        }
        read_total += n as usize;
    }

    // Read compressed hello payload
    let mut comp_hello = [0u8; 8192];
    if sys_lseek(fd, hello_off, 0) < 0 {
        print_err(b"brot: error: lseek hello offset failed\n");
        sys_exit(110);
    }
    let mut read_total = 0;
    while read_total < hello_len {
        let n = sys_read(fd, comp_hello.as_mut_ptr().add(read_total), hello_len - read_total);
        if n <= 0 {
            print_err(b"brot: error: read hello bytes failed\n");
            sys_exit(111);
        }
        read_total += n as usize;
    }
    sys_close(fd);

    // 4. Decompress components
    let mut dec_plat = [0u8; 4096];
    let plat_decompressed_len = match crate::decompress::decompress(&comp_plat[..plat_len], &mut dec_plat) {
        Ok(n) => n,
        Err(_) => {
            print_err(b"brot: error: decompress platform failed\n");
            sys_exit(112);
        }
    };

    let mut dec_hello = [0u8; 4096];
    let hello_decompressed_len = match crate::decompress::decompress(&comp_hello[..hello_len], &mut dec_hello) {
        Ok(n) => n,
        Err(_) => {
            print_err(b"brot: error: decompress hello payload failed\n");
            sys_exit(113);
        }
    };

    // 5. Allocate executable memory for platform
    let plat_code = alloc_executable(plat_decompressed_len);
    if plat_code.is_null() {
        print_err(b"brot: error: allocate executable memory failed (mmap RWX)\n");
        sys_exit(114);
    }

    core::ptr::copy_nonoverlapping(dec_plat.as_ptr(), plat_code as *mut u8, plat_decompressed_len);
    flush_instruction_cache(plat_code, plat_decompressed_len);

    // 6. Invoke PAL
    let host = UnixPlatformHost {
        load_library: Some(load_library),
        get_proc_address: Some(get_proc_address),
        alloc: Some(alloc_cb),
        free: Some(free_cb),
        flush_cache: Some(flush_cb),
        set_jit_write_protect: None,
        hello_payload: dec_hello.as_ptr(),
        hello_payload_size: hello_decompressed_len,
    };

    let entry: PlatformEntry = core::mem::transmute(plat_code);
    let status = entry(&host);

    free_executable(plat_code, plat_decompressed_len);
    sys_exit(status);
}
