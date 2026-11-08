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

#[link(name = "System")]
unsafe extern "C" {
    fn open(path: *const u8, flags: i32, mode: u32) -> i32;
    fn lseek(fd: i32, offset: i64, whence: i32) -> i64;
    fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
    fn close(fd: i32) -> i32;
    fn unlink(path: *const u8) -> i32;
    fn exit(code: i32) -> !;
    fn dlopen(path: *const c_char, flags: i32) -> *mut c_void;
    fn dlsym(handle: *mut c_void, symbol: *const c_char) -> *mut c_void;
    fn sys_icache_invalidate(start: *const c_void, len: usize);
    fn pthread_jit_write_protect_np(enabled: i32);
    fn mmap(
        addr: *mut c_void,
        len: usize,
        prot: i32,
        flags: i32,
        fd: i32,
        offset: i64,
    ) -> *mut c_void;
    fn munmap(addr: *mut c_void, len: usize) -> i32;
}

pub unsafe fn alloc_executable(size: usize) -> *mut c_void {
    let size = (size + 4095) & !4095;
    // PROT_READ | PROT_WRITE | PROT_EXEC = 7, MAP_PRIVATE | MAP_ANON | MAP_JIT = 0x1802
    let ptr = mmap(core::ptr::null_mut(), size, 7, 0x1802, -1, 0);
    if ptr.is_null() || ptr as usize == usize::MAX {
        core::ptr::null_mut()
    } else {
        ptr
    }
}

pub unsafe fn free_executable(ptr: *mut c_void, size: usize) {
    if !ptr.is_null() {
        let size = (size + 4095) & !4095;
        munmap(ptr, size);
    }
}

unsafe extern "C" fn load_lib(name: *const c_char, flags: i32) -> *mut c_void {
    dlopen(name, flags)
}

unsafe extern "C" fn get_proc(module: *mut c_void, name: *const c_char) -> *mut c_void {
    dlsym(module, name)
}

unsafe extern "C" fn alloc_cb(size: usize) -> *mut c_void {
    alloc_executable(size)
}

unsafe extern "C" fn free_cb(ptr: *mut c_void, size: usize) {
    free_executable(ptr, size)
}

unsafe extern "C" fn flush_cb(ptr: *mut c_void, size: usize) {
    sys_icache_invalidate(ptr, size);
}

unsafe extern "C" fn jit_protect_cb(enabled: i32) {
    pthread_jit_write_protect_np(enabled);
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

unsafe fn streq_str(a: *const u8, b: *const u8) -> bool {
    if a.is_null() || b.is_null() {
        return false;
    }
    let mut i = 0;
    while *a.add(i) != 0 && *b.add(i) != 0 {
        if *a.add(i) != *b.add(i) {
            return false;
        }
        i += 1;
    }
    *a.add(i) == 0 && *b.add(i) == 0
}

pub unsafe fn run(argc: i32, argv: *const *const u8) -> ! {
    if argc < 6 || argv.is_null() {
        exit(101);
    }

    let mut arg_idx = 0;
    let argv0 = *argv.add(arg_idx);
    unlink(argv0);
    arg_idx += 1;

    let next_arg = *argv.add(arg_idx);
    if streq_str(argv0, next_arg) {
        arg_idx += 1;
    }

    let bundle_path = *argv.add(arg_idx);
    arg_idx += 1;
    let plat_off_str = *argv.add(arg_idx);
    arg_idx += 1;
    let plat_len_str = *argv.add(arg_idx);
    arg_idx += 1;
    let hello_off_str = *argv.add(arg_idx);
    arg_idx += 1;
    let hello_len_str = *argv.add(arg_idx);

    // 2. Parse component ranges
    let plat_off = match parse_u64(plat_off_str) {
        Some(v) => v as i64,
        None => exit(102),
    };
    let plat_len = match parse_u64(plat_len_str) {
        Some(v) => v as usize,
        None => exit(103),
    };
    let hello_off = match parse_u64(hello_off_str) {
        Some(v) => v as i64,
        None => exit(104),
    };
    let hello_len = match parse_u64(hello_len_str) {
        Some(v) => v as usize,
        None => exit(105),
    };

    if plat_len == 0 || plat_len > 8192 || hello_len == 0 || hello_len > 8192 {
        exit(106);
    }

    // 3. Open bundle
    let fd = open(bundle_path, 0, 0); // O_RDONLY = 0
    if fd < 0 {
        exit(107);
    }

    // Read compressed platform
    let mut comp_plat = [0u8; 8192];
    if lseek(fd, plat_off, 0) < 0 {
        exit(108);
    }
    let mut read_total = 0;
    while read_total < plat_len {
        let n = read(fd, comp_plat.as_mut_ptr().add(read_total), plat_len - read_total);
        if n <= 0 {
            exit(109);
        }
        read_total += n as usize;
    }

    // Read compressed hello payload
    let mut comp_hello = [0u8; 8192];
    if lseek(fd, hello_off, 0) < 0 {
        exit(110);
    }
    let mut read_total = 0;
    while read_total < hello_len {
        let n = read(fd, comp_hello.as_mut_ptr().add(read_total), hello_len - read_total);
        if n <= 0 {
            exit(111);
        }
        read_total += n as usize;
    }
    close(fd);

    // 4. Decompress components
    let mut dec_plat = [0u8; 4096];
    let plat_decompressed_len = match crate::decompress::decompress(&comp_plat[..plat_len], &mut dec_plat) {
        Ok(n) => n,
        Err(_) => exit(112),
    };

    let mut dec_hello = [0u8; 4096];
    let hello_decompressed_len = match crate::decompress::decompress(&comp_hello[..hello_len], &mut dec_hello) {
        Ok(n) => n,
        Err(_) => exit(113),
    };

    // 5. Allocate executable memory for platform
    let plat_code = alloc_executable(plat_decompressed_len);
    if plat_code.is_null() {
        exit(114);
    }

    pthread_jit_write_protect_np(0);
    core::ptr::copy_nonoverlapping(dec_plat.as_ptr(), plat_code as *mut u8, plat_decompressed_len);
    pthread_jit_write_protect_np(1);
    sys_icache_invalidate(plat_code, plat_decompressed_len);

    // 6. Invoke PAL
    let host = UnixPlatformHost {
        load_library: Some(load_lib),
        get_proc_address: Some(get_proc),
        alloc: Some(alloc_cb),
        free: Some(free_cb),
        flush_cache: Some(flush_cb),
        set_jit_write_protect: Some(jit_protect_cb),
        hello_payload: dec_hello.as_ptr(),
        hello_payload_size: hello_decompressed_len,
    };

    let entry: PlatformEntry = core::mem::transmute(plat_code);
    let status = entry(&host);

    free_executable(plat_code, plat_decompressed_len);
    exit(status);
}
