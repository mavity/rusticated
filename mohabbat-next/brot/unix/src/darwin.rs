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
    pub wasm_bytes: *const u8,
    pub wasm_len: usize,
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
    if bundle_idx + 3 >= argc {
        exit(101);
    }

    let bundle_path = *argv.add(bundle_idx);
    let pool_off_str = *argv.add(bundle_idx + 1);
    let pool_len_str = *argv.add(bundle_idx + 2);
    let mode_str = *argv.add(bundle_idx + 3);

    // 3. Parse pool range
    let pool_off = match parse_u64(pool_off_str) {
        Some(v) => v as i64,
        None => exit(102),
    };
    let pool_len = match parse_u64(pool_len_str) {
        Some(v) => v as usize,
        None => exit(103),
    };
    let is_fileio = !mode_str.is_null()
        && *mode_str == b'f'
        && *mode_str.add(1) == b'i'
        && *mode_str.add(2) == b'l'
        && *mode_str.add(3) == b'e'
        && *mode_str.add(4) == b'i'
        && *mode_str.add(5) == b'o'
        && *mode_str.add(6) == 0;
    let guest_args_idx = bundle_idx + 4;
    let raw_guest_argv = argv.add(guest_args_idx);
    let raw_guest_argc = argc - guest_args_idx;

    if pool_len == 0 || pool_len > 128 * 1024 * 1024 {
        exit(106);
    }

    // 4. Open bundle and read single shared compressed pool
    let fd = open(bundle_path, 0, 0); // O_RDONLY = 0
    if fd < 0 {
        exit(107);
    }

    let mut comp_pool = alloc::vec![0u8; pool_len];
    if lseek(fd, pool_off, 0) < 0 {
        exit(108);
    }
    let mut read_total = 0;
    while read_total < pool_len {
        let n = read(
            fd,
            comp_pool.as_mut_ptr().add(read_total),
            pool_len - read_total,
        );
        if n <= 0 {
            exit(109);
        }
        read_total += n as usize;
    }
    close(fd);

    let pool = match crate::decompress::decompress_to_vec(&comp_pool) {
        Ok(v) => v,
        Err(_) => exit(112),
    };
    drop(comp_pool);

    #[cfg(target_arch = "x86_64")]
    const PLAT_NAME: &[u8] = b"platform-darwin-x86_64";
    #[cfg(target_arch = "aarch64")]
    const PLAT_NAME: &[u8] = b"platform-darwin-aarch64";
    #[cfg(target_arch = "x86_64")]
    const RUNTIME_NAME: &[u8] = b"runtime-x86_64";
    #[cfg(target_arch = "aarch64")]
    const RUNTIME_NAME: &[u8] = b"runtime-aarch64";
    #[cfg(target_arch = "x86_64")]
    const HELLO_NAME: &[u8] = b"hello-x86_64";
    #[cfg(target_arch = "aarch64")]
    const HELLO_NAME: &[u8] = b"hello-aarch64";
    #[cfg(target_arch = "x86_64")]
    const FILEIO_NAME: &[u8] = b"fileio-x86_64";
    #[cfg(target_arch = "aarch64")]
    const FILEIO_NAME: &[u8] = b"fileio-aarch64";

    let Some(plat_slice) = crate::pool::find_entry(&pool, PLAT_NAME) else {
        exit(113);
    };
    let brain_slice = crate::pool::find_entry(&pool, b"brain.wasm");
    let use_brain = brain_slice.is_some() && !is_fileio;
    let payload_name = if is_fileio {
        FILEIO_NAME
    } else if use_brain {
        RUNTIME_NAME
    } else {
        HELLO_NAME
    };
    let Some(payload_slice) = crate::pool::find_entry(&pool, payload_name) else {
        exit(113);
    };

    let mut brain_argv = alloc::vec::Vec::new();
    let (guest_argv, guest_argc) = if use_brain {
        brain_argv.push(bundle_path);
        let mut idx = 0;
        while idx < raw_guest_argc {
            brain_argv.push(*raw_guest_argv.add(idx));
            idx += 1;
        }
        (brain_argv.as_ptr(), brain_argv.len())
    } else {
        (raw_guest_argv, raw_guest_argc)
    };

    // 5. Allocate executable memory for platform (including 128 KiB prefix)
    let total_plat_size = PREFIX_ABI_OFFSET + plat_slice.len();
    let plat_alloc = alloc_executable(total_plat_size);
    if plat_alloc.is_null() {
        exit(114);
    }

    let (wasm_ptr, wasm_len) = match brain_slice {
        Some(w) if use_brain => (w.as_ptr(), w.len()),
        _ => (core::ptr::null(), 0),
    };

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
            hello_payload: payload_slice.as_ptr(),
            hello_payload_size: payload_slice.len(),
            argv: guest_argv,
            argc: guest_argc,
            wasm_bytes: wasm_ptr,
            wasm_len,
        },
    );
    let plat_code = (plat_alloc as *mut u8).add(PREFIX_ABI_OFFSET) as *mut c_void;
    core::ptr::copy_nonoverlapping(
        plat_slice.as_ptr(),
        plat_code as *mut u8,
        plat_slice.len(),
    );
    pthread_jit_write_protect_np(1);
    sys_icache_invalidate(plat_code, plat_slice.len());

    // 6. Invoke PAL at +0x00000
    let entry: PlatformEntry = core::mem::transmute(plat_code);
    let status = entry();

    free_executable(plat_alloc, total_plat_size);
    exit(status);
}
