use crate::META;
use alloc::vec;
use alloc::vec::Vec;

type RunPayloadFunc = unsafe extern "C" fn(*const u8, usize) -> u32;

unsafe extern "C" {
    fn open(path: *const u8, flags: i32) -> i32;
    fn lseek(fd: i32, offset: i64, whence: i32) -> i64;
    fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
    fn close(fd: i32) -> i32;
    fn write(fd: i32, buf: *const u8, count: usize) -> isize;
    fn mkstemp(template: *mut u8) -> i32;
    fn unlink(path: *const u8) -> i32;
    fn dlopen(filename: *const u8, flags: i32) -> *mut core::ffi::c_void;
    fn dlsym(handle: *mut core::ffi::c_void, symbol: *const u8) -> *mut core::ffi::c_void;
    fn exit(code: i32) -> !;
}

const O_RDONLY: i32 = 0;
const SEEK_SET: i32 = 0;
const SEEK_END: i32 = 2;
const RTLD_LAZY: i32 = 1;
const RTLD_GLOBAL: i32 = 0x100;

pub unsafe fn run(argc: i32, argv: *const *const u8) -> ! {
    // Extract argv[1] as the bat file path.
    let bat_path: Vec<u8> = unsafe {
        if argc < 2 || argv.is_null() {
            exit(102);
        }
        let arg1 = *argv.offset(1);
        if arg1.is_null() {
            exit(102);
        }
        let mut len = 0usize;
        while *arg1.add(len) != 0 {
            len += 1;
        }
        if len == 0 {
            exit(102);
        }
        core::slice::from_raw_parts(arg1, len).to_vec()
    };

    let mut path_cstr = bat_path;
    path_cstr.push(0);

    let fd = unsafe { open(path_cstr.as_ptr(), O_RDONLY) };
    if fd < 0 {
        unsafe { exit(2) };
    }

    let file_size = unsafe { lseek(fd, 0, SEEK_END) };
    if file_size < 0 {
        unsafe { close(fd) };
        unsafe { exit(3) };
    }

    let pool_len = unsafe { META.pool_len as usize };
    if pool_len == 0 {
        unsafe { close(fd) };
        unsafe { exit(4) };
    }

    let pool_start = file_size - pool_len as i64;
    if pool_start < 0 {
        unsafe { close(fd) };
        unsafe { exit(5) };
    }

    if unsafe { lseek(fd, pool_start, SEEK_SET) } < 0 {
        unsafe { close(fd) };
        unsafe { exit(6) };
    }

    let mut compressed_data = vec![0u8; pool_len];
    let mut total_read = 0usize;
    while total_read < pool_len {
        let n = unsafe {
            read(
                fd,
                compressed_data.as_mut_ptr().add(total_read),
                pool_len - total_read,
            )
        };
        if n <= 0 {
            unsafe { close(fd) };
            unsafe { exit(7) };
        }
        total_read += n as usize;
    }
    unsafe { close(fd) };

    let total_pool = unsafe { (META.payload_offset + META.payload_len) as usize };
    let mut decompressed_pool = vec![0u8; total_pool];

    let mut out_offset = 0usize;
    let _ = crate::decompress::decompress_to_writer(&compressed_data, |chunk| {
        let end = out_offset + chunk.len();
        if end <= decompressed_pool.len() {
            decompressed_pool[out_offset..end].copy_from_slice(chunk);
            out_offset = end;
        }
    });

    let washmhost_start = unsafe { META.washmhost_offset as usize };
    let washmhost_end = unsafe { (META.washmhost_offset + META.washmhost_len) as usize };
    let washmhost_data = &decompressed_pool[washmhost_start..washmhost_end];

    let payload_start = unsafe { META.payload_offset as usize };
    let payload_end = unsafe { (META.payload_offset + META.payload_len) as usize };
    let payload_data = &decompressed_pool[payload_start..payload_end];

    let mut template = *b"/tmp/moh-XXXXXX\0";
    let tmp_fd = unsafe { mkstemp(template.as_mut_ptr()) };
    if tmp_fd < 0 {
        unsafe { exit(10) };
    }

    let mut offset = 0usize;
    while offset < washmhost_data.len() {
        let n = unsafe {
            write(
                tmp_fd,
                washmhost_data.as_ptr().add(offset),
                washmhost_data.len() - offset,
            )
        };
        if n <= 0 {
            unsafe { close(tmp_fd) };
            unsafe { unlink(template.as_ptr()) };
            unsafe { exit(11) };
        }
        offset += n as usize;
    }
    unsafe { close(tmp_fd) };

    let handle = unsafe { dlopen(template.as_ptr(), RTLD_LAZY | RTLD_GLOBAL) };
    if handle.is_null() {
        unsafe { unlink(template.as_ptr()) };
        unsafe { exit(12) };
    }

    let run_payload_name = b"run_payload\0";
    let run_payload_ptr = unsafe { dlsym(handle, run_payload_name.as_ptr()) };
    if run_payload_ptr.is_null() {
        unsafe { unlink(template.as_ptr()) };
        unsafe { exit(13) };
    }

    let run_payload: RunPayloadFunc = unsafe { core::mem::transmute(run_payload_ptr) };
    let exit_code = unsafe { run_payload(payload_data.as_ptr(), payload_data.len()) };

    unsafe { unlink(template.as_ptr()) };
    unsafe { exit(exit_code as i32) };
}
