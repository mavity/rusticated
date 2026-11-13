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
    pub argv: *const *const u8,
    pub argc: usize,
}

type PlatformEntry = unsafe extern "C" fn() -> i32;
const PREFIX_ABI_OFFSET: usize = 0x20000;

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
    fn _NSGetExecutablePath(buf: *mut c_char, bufsize: *mut u32) -> i32;
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

pub unsafe fn run(argc: i32, argv: *const *const u8) -> ! {
    let argc = argc as usize;

    // Scan argv[0..argc] to find the LAST argument whose basename begins with "🐚︎"
    let mut last_match_idx = None;
    let mut i = 0;
    while i < argc {
        let arg_ptr = *argv.add(i);
        let arg_len = str_len(arg_ptr);
        let (arg_base_ptr, arg_base_len) = get_basename(arg_ptr, arg_len);
        if has_shell_prefix(arg_base_ptr, arg_base_len) {
            last_match_idx = Some(i);
        }
        i += 1;
    }

    let Some(match_idx) = last_match_idx else {
        exit(101);
    };

    // Unlink the temporary launcher
    let launcher_path = *argv.add(match_idx);
    unlink(launcher_path);

    let bundle_idx = match_idx + 1;
    if bundle_idx + 4 >= argc {
        exit(101);
    }

    let bundle_path = *argv.add(bundle_idx);
    let plat_off_str = *argv.add(bundle_idx + 1);
    let plat_len_str = *argv.add(bundle_idx + 2);
    let hello_off_str = *argv.add(bundle_idx + 3);
    let hello_len_str = *argv.add(bundle_idx + 4);

    // 3. Parse component ranges
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
    let guest_args_idx = bundle_idx + 5;
    let guest_argv = argv.add(guest_args_idx);
    let guest_argc = argc - guest_args_idx;

    if plat_len == 0 || plat_len > 8192 || hello_len == 0 || hello_len > 65536 {
        exit(106);
    }

    // 4. Open bundle
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
        let n = read(
            fd,
            comp_plat.as_mut_ptr().add(read_total),
            plat_len - read_total,
        );
        if n <= 0 {
            exit(109);
        }
        read_total += n as usize;
    }

    // Read compressed hello payload
    let mut comp_hello = [0u8; 65536];
    if lseek(fd, hello_off, 0) < 0 {
        exit(110);
    }
    let mut read_total = 0;
    while read_total < hello_len {
        let n = read(
            fd,
            comp_hello.as_mut_ptr().add(read_total),
            hello_len - read_total,
        );
        if n <= 0 {
            exit(111);
        }
        read_total += n as usize;
    }
    close(fd);

    // 4. Decompress components
    let mut dec_plat = [0u8; 8192];
    let plat_decompressed_len =
        match crate::decompress::decompress(&comp_plat[..plat_len], &mut dec_plat) {
            Ok(n) => n,
            Err(_) => exit(112),
        };

    let mut dec_hello = [0u8; 65536];
    let hello_decompressed_len =
        match crate::decompress::decompress(&comp_hello[..hello_len], &mut dec_hello) {
            Ok(n) => n,
            Err(_) => exit(113),
        };

    // 5. Allocate executable memory for platform (including 128 KiB prefix)
    let total_plat_size = PREFIX_ABI_OFFSET + plat_decompressed_len;
    let plat_alloc = alloc_executable(total_plat_size);
    if plat_alloc.is_null() {
        exit(114);
    }

    pthread_jit_write_protect_np(0);
    core::ptr::write(
        plat_alloc as *mut UnixPlatformHost,
        UnixPlatformHost {
            load_library: Some(load_lib),
            get_proc_address: Some(get_proc),
            alloc: Some(alloc_cb),
            free: Some(free_cb),
            flush_cache: Some(flush_cb),
            set_jit_write_protect: Some(jit_protect_cb),
            hello_payload: dec_hello.as_ptr(),
            hello_payload_size: hello_decompressed_len,
            argv: guest_argv,
            argc: guest_argc,
        },
    );
    let plat_code = (plat_alloc as *mut u8).add(PREFIX_ABI_OFFSET) as *mut c_void;
    core::ptr::copy_nonoverlapping(
        dec_plat.as_ptr(),
        plat_code as *mut u8,
        plat_decompressed_len,
    );
    pthread_jit_write_protect_np(1);
    sys_icache_invalidate(plat_code, plat_decompressed_len);

    // 6. Invoke PAL at +0x00000
    let entry: PlatformEntry = core::mem::transmute(plat_code);
    let status = entry();

    free_executable(plat_alloc, total_plat_size);
    exit(status);
}
