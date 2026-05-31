use crate::META;

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
}

const O_RDONLY: i32 = 0;
const SEEK_SET: i32 = 0;
const SEEK_END: i32 = 2;
const RTLD_LAZY: i32 = 1;
const RTLD_GLOBAL: i32 = 0x100;

pub unsafe fn run() {
    let bat_path = match std::env::args().nth(1) {
        Some(arg) if !arg.is_empty() => arg.into_bytes(),
        _ => std::process::exit(102),
    };

    let mut path_cstr = bat_path;
    path_cstr.push(0);

    let fd = open(path_cstr.as_ptr(), O_RDONLY);
    if fd < 0 {
        std::process::exit(2);
    }

    let file_size = lseek(fd, 0, SEEK_END);
    if file_size < 0 {
        close(fd);
        std::process::exit(3);
    }

    let pool_len = META.pool_len as usize;
    if pool_len == 0 {
        close(fd);
        std::process::exit(4);
    }

    let pool_start = file_size - pool_len as i64;
    if pool_start < 0 {
        close(fd);
        std::process::exit(5);
    }

    if lseek(fd, pool_start, SEEK_SET) < 0 {
        close(fd);
        std::process::exit(6);
    }

    let mut compressed_data = vec![0u8; pool_len];
    let mut total_read = 0usize;
    while total_read < pool_len {
        let n = read(
            fd,
            compressed_data.as_mut_ptr().add(total_read),
            pool_len - total_read,
        );
        if n <= 0 {
            close(fd);
            std::process::exit(7);
        }
        total_read += n as usize;
    }
    close(fd);

    let total_pool = (META.payload_offset + META.payload_len) as usize;
    let mut decompressed_pool = vec![0u8; total_pool];

    let mut out_offset = 0usize;
    let _ = crate::decompress::decompress_to_writer(&compressed_data, |chunk| {
        let end = out_offset + chunk.len();
        if end <= decompressed_pool.len() {
            decompressed_pool[out_offset..end].copy_from_slice(chunk);
            out_offset = end;
        }
    });

    let washmhost_start = META.washmhost_offset as usize;
    let washmhost_end = (META.washmhost_offset + META.washmhost_len) as usize;
    let washmhost_data = &decompressed_pool[washmhost_start..washmhost_end];

    let payload_start = META.payload_offset as usize;
    let payload_end = (META.payload_offset + META.payload_len) as usize;
    let payload_data = &decompressed_pool[payload_start..payload_end];

    let mut template = *b"/tmp/moh-XXXXXX\0";
    let tmp_fd = mkstemp(template.as_mut_ptr());
    if tmp_fd < 0 {
        std::process::exit(10);
    }

    let mut offset = 0usize;
    while offset < washmhost_data.len() {
        let n = write(
            tmp_fd,
            washmhost_data.as_ptr().add(offset),
            washmhost_data.len() - offset,
        );
        if n <= 0 {
            close(tmp_fd);
            unlink(template.as_ptr());
            std::process::exit(11);
        }
        offset += n as usize;
    }
    close(tmp_fd);

    let handle = dlopen(template.as_ptr(), RTLD_LAZY | RTLD_GLOBAL);
    if handle.is_null() {
        unlink(template.as_ptr());
        std::process::exit(12);
    }

    let run_payload_name = b"run_payload\0";
    let run_payload_ptr = dlsym(handle, run_payload_name.as_ptr());
    if run_payload_ptr.is_null() {
        unlink(template.as_ptr());
        std::process::exit(13);
    }

    let run_payload: RunPayloadFunc = core::mem::transmute(run_payload_ptr);
    let exit_code = run_payload(payload_data.as_ptr(), payload_data.len());

    unlink(template.as_ptr());
    std::process::exit(exit_code as i32);
}
