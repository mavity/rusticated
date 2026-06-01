use crate::META;
use std::os::unix::fs::PermissionsExt;
use std::os::unix::process::CommandExt;
use std::process::Command;

unsafe extern "C" {
    fn open(path: *const u8, flags: i32) -> i32;
    fn lseek(fd: i32, offset: i64, whence: i32) -> i64;
    fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
    fn close(fd: i32) -> i32;
    fn write(fd: i32, buf: *const u8, count: usize) -> isize;
    fn mkstemp(template: *mut u8) -> i32;
    fn unlink(path: *const u8) -> i32;
}

const O_RDONLY: i32 = 0;
const SEEK_SET: i32 = 0;
const SEEK_END: i32 = 2;

pub unsafe fn run() {
    // argv[1] is the path to mohab.bat, passed by the shell one-liner.
    // Read from /proc/self/cmdline (NUL-separated args) to avoid std::env::args dependency.
    let bat_path: Vec<u8> = {
        let path = b"/proc/self/cmdline\0";
        let fd = open(path.as_ptr(), O_RDONLY);
        if fd < 0 {
            std::process::exit(102);
        }
        let mut buf = vec![0u8; 4096];
        let mut total = 0usize;
        loop {
            let n = read(fd, buf.as_mut_ptr().add(total), buf.len() - total);
            if n < 0 {
                close(fd);
                std::process::exit(102);
            }
            if n == 0 {
                break;
            }
            total += n as usize;
            if total == buf.len() {
                buf.resize(buf.len() * 2, 0);
            }
        }
        close(fd);
        // cmdline: argv[0]\0argv[1]\0...
        let after0 = match buf[..total].iter().position(|&b| b == 0) {
            Some(i) => i + 1,
            None => std::process::exit(102),
        };
        let end1 = buf[after0..total]
            .iter()
            .position(|&b| b == 0)
            .map(|i| after0 + i)
            .unwrap_or(total);
        if after0 >= end1 {
            std::process::exit(102);
        }
        buf[after0..end1].to_vec()
    };

    let mut path_cstr = bat_path;
    path_cstr.push(0);
    let vegetable_path =
        std::string::String::from_utf8_lossy(&path_cstr[..path_cstr.len() - 1]).into_owned();

    let fd = open(path_cstr.as_ptr(), O_RDONLY);
    if fd < 0 {
        std::process::exit(2);
    }

    // Get file size via seek-to-end
    let file_size = lseek(fd, 0, SEEK_END);
    if file_size < 0 {
        std::process::exit(3);
    }

    let pool_len = META.pool_len as usize;
    if pool_len == 0 {
        std::process::exit(4);
    }

    let pool_start = file_size - pool_len as i64;
    if pool_start < 0 {
        std::process::exit(5);
    }

    if lseek(fd, pool_start, SEEK_SET) < 0 {
        std::process::exit(6);
    }

    // Read the compressed pool
    let mut compressed_data = vec![0u8; pool_len];
    let mut total_read = 0;
    while total_read < pool_len {
        let n = read(
            fd,
            compressed_data.as_mut_ptr().add(total_read),
            pool_len - total_read,
        );
        if n <= 0 {
            std::process::exit(7);
        }
        total_read += n as usize;
    }
    close(fd);

    // Decompress pool
    let total_pool = (META.payload_offset + META.payload_len) as usize;
    let mut decompressed_pool = vec![0u8; total_pool];

    let mut out_offset = 0;
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

    // Write washmhost to a temp file
    let mut template = *b"/tmp/moh-XXXXXX\0";
    let tmp_fd = mkstemp(template.as_mut_ptr());
    if tmp_fd < 0 {
        std::process::exit(10);
    }

    let mut offset = 0;
    while offset < washmhost_data.len() {
        let n = write(
            tmp_fd,
            washmhost_data.as_ptr().add(offset),
            washmhost_data.len() - offset,
        );
        if n <= 0 {
            std::process::exit(11);
        }
        offset += n as usize;
    }
    close(tmp_fd);

    let washmhost_path_end = template
        .iter()
        .position(|&b| b == 0)
        .unwrap_or(template.len());
    let washmhost_path =
        std::string::String::from_utf8_lossy(&template[..washmhost_path_end]).into_owned();
    if std::fs::set_permissions(&washmhost_path, std::fs::Permissions::from_mode(0o755)).is_err() {
        unlink(template.as_ptr());
        std::process::exit(12);
    }

    // Write payload to a temp file and pass its path through MOHABBAT_WASM_FD.
    let mut payload_template = *b"/tmp/mohp-XXXXXX\0";
    let payload_fd = mkstemp(payload_template.as_mut_ptr());
    if payload_fd < 0 {
        unlink(template.as_ptr());
        std::process::exit(14);
    }

    let mut payload_off = 0usize;
    while payload_off < payload_data.len() {
        let n = write(
            payload_fd,
            payload_data.as_ptr().add(payload_off),
            payload_data.len() - payload_off,
        );
        if n <= 0 {
            close(payload_fd);
            unlink(payload_template.as_ptr());
            unlink(template.as_ptr());
            std::process::exit(15);
        }
        payload_off += n as usize;
    }
    close(payload_fd);

    let payload_path_end = payload_template
        .iter()
        .position(|&b| b == 0)
        .unwrap_or(payload_template.len());
    let payload_path =
        std::string::String::from_utf8_lossy(&payload_template[..payload_path_end]).into_owned();

    let status = Command::new(&washmhost_path)
        .arg0(&vegetable_path)
        .args(std::env::args().skip(2))
        .env("MOHABBAT_WASM_FD", &payload_path)
        .status();

    unlink(payload_template.as_ptr());
    unlink(template.as_ptr());

    match status {
        Ok(s) => std::process::exit(s.code().unwrap_or(1)),
        Err(_) => std::process::exit(16),
    }
}
