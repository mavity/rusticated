use alloc::vec;
use alloc::vec::Vec;

#[link(name = "System")]
unsafe extern "C" {
    fn open(path: *const u8, flags: i32, mode: u32) -> i32;
    fn lseek(fd: i32, offset: i64, whence: i32) -> i64;
    fn read(fd: i32, buf: *mut u8, count: usize) -> isize;
    fn close(fd: i32) -> i32;
    fn write(fd: i32, buf: *const u8, count: usize) -> isize;
    fn unlink(path: *const u8) -> i32;
    fn fchmod(fd: i32, mode: u32) -> i32;
    fn getpid() -> i32;
    fn fork() -> i32;
    fn execve(path: *const u8, argv: *const *const u8, envp: *const *const u8) -> i32;
    fn waitpid(pid: i32, status: *mut i32, options: i32) -> i32;
    fn exit(code: i32) -> !;
}

// macOS open(2) flag values (differ from Linux).
const O_RDONLY: i32 = 0;
const O_RDWR: i32 = 2;
const O_CREAT: i32 = 0x200;
const O_EXCL: i32 = 0x800;
const SEEK_SET: i32 = 0;
const SEEK_END: i32 = 2;

// ─── Helpers ─────────────────────────────────────────────────────────────────

unsafe fn write_all(fd: i32, data: &[u8]) -> bool {
    let mut off = 0;
    while off < data.len() {
        let n = unsafe { write(fd, data.as_ptr().add(off), data.len() - off) };
        if n <= 0 { return false; }
        off += n as usize;
    }
    true
}

fn append_decimal(buf: &mut Vec<u8>, mut n: u32) {
    if n == 0 { buf.push(b'0'); return; }
    let start = buf.len();
    while n > 0 { buf.push(b'0' + (n % 10) as u8); n /= 10; }
    buf[start..].reverse();
}

fn make_tmp_path(prefix: &[u8], pid: u32, idx: u8) -> Vec<u8> {
    let mut path = Vec::with_capacity(prefix.len() + 12);
    path.extend_from_slice(prefix);
    append_decimal(&mut path, pid);
    path.push(b'-');
    path.push(b'0' + idx);
    path.push(0);
    path
}

unsafe fn from_cstr<'a>(p: *const u8) -> &'a [u8] {
    if p.is_null() { return &[]; }
    let mut len = 0;
    while unsafe { *p.add(len) } != 0 { len += 1; }
    unsafe { core::slice::from_raw_parts(p, len) }
}

pub unsafe fn run(argc: i32, argv: *const *const u8, envp: *const *const u8) -> ! {
    if argc < 7 || argv.is_null() {
        unsafe { exit(102) };
    }
    let bat_path_ptr = unsafe { *argv.add(1) };
    if bat_path_ptr.is_null() {
        unsafe { exit(102) };
    }

    let pool_len = unsafe {
        let p = *argv.add(2);
        if p.is_null() { exit(102); }
        match crate::parse_u64_bytes(from_cstr(p)) {
            Some(v) => v as usize,
            None => exit(103),
        }
    };
    let washmhost_offset = unsafe {
        let p = *argv.add(3);
        if p.is_null() { exit(102); }
        match crate::parse_u64_bytes(from_cstr(p)) {
            Some(v) => v as usize,
            None => exit(103),
        }
    };
    let washmhost_len = unsafe {
        let p = *argv.add(4);
        if p.is_null() { exit(102); }
        match crate::parse_u64_bytes(from_cstr(p)) {
            Some(v) => v as usize,
            None => exit(103),
        }
    };
    let payload_offset = unsafe {
        let p = *argv.add(5);
        if p.is_null() { exit(102); }
        match crate::parse_u64_bytes(from_cstr(p)) {
            Some(v) => v as usize,
            None => exit(103),
        }
    };
    let payload_len = unsafe {
        let p = *argv.add(6);
        if p.is_null() { exit(102); }
        match crate::parse_u64_bytes(from_cstr(p)) {
            Some(v) => v as usize,
            None => exit(103),
        }
    };

    // ── Find TMPDIR from environment ──────────────────────────────────────
    let mut tmp_prefix: &[u8] = b"/tmp";
    if !envp.is_null() {
        let mut ptr = envp;
        while !unsafe { (*ptr).is_null() } {
            let entry = unsafe { from_cstr(*ptr) };
            if entry.starts_with(b"TMPDIR=") {
                tmp_prefix = &entry[7..];
                break;
            }
            ptr = unsafe { ptr.add(1) };
        }
    }

    // ── Open the vegetable file and read the compressed pool ──────────────
    let mut path_cstr = unsafe { from_cstr(bat_path_ptr) }.to_vec();
    path_cstr.push(0);

    let fd = unsafe { open(path_cstr.as_ptr(), O_RDONLY, 0) };
    if fd < 0 {
        unsafe { exit(2) };
    }

    let file_size = unsafe { lseek(fd, 0, SEEK_END) };
    if file_size < 0 {
        unsafe { close(fd) };
        unsafe { exit(3) };
    }

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

    // ── Decompress pool ───────────────────────────────────────────────────
    let total_pool = payload_offset + payload_len;
    let mut decompressed_pool = vec![0u8; total_pool];

    let mut out_offset = 0usize;
    let _ = crate::decompress::decompress_to_writer(&compressed_data, |chunk| {
        let end = out_offset + chunk.len();
        if end <= decompressed_pool.len() {
            decompressed_pool[out_offset..end].copy_from_slice(chunk);
            out_offset = end;
        }
    });

    let washmhost_start = washmhost_offset;
    let washmhost_end = washmhost_offset + washmhost_len;
    let washmhost_data = &decompressed_pool[washmhost_start..washmhost_end];

    let payload_start = payload_offset;
    let payload_end = payload_offset + payload_len;
    let payload_data = &decompressed_pool[payload_start..payload_end];

    // ── Write washmhost to a temp file ────────────────────────────────────
    let pid = unsafe { getpid() } as u32;

    let mut washmhost_prefix = Vec::from(tmp_prefix);
    if !washmhost_prefix.ends_with(b"/") { washmhost_prefix.push(b'/'); }
    washmhost_prefix.extend_from_slice(b"moh-");

    let mut payload_prefix = Vec::from(tmp_prefix);
    if !payload_prefix.ends_with(b"/") { payload_prefix.push(b'/'); }
    payload_prefix.extend_from_slice(b"mohp-");

    let washmhost_path = make_tmp_path(&washmhost_prefix, pid, 0);
    let payload_path   = make_tmp_path(&payload_prefix,   pid, 0);

    let tmp_fd = unsafe { open(washmhost_path.as_ptr(), O_RDWR | O_CREAT | O_EXCL, 0o600) };
    if tmp_fd < 0 { unsafe { exit(10) }; }

    if !unsafe { write_all(tmp_fd, washmhost_data) } {
        unsafe { close(tmp_fd) };
        unsafe { unlink(washmhost_path.as_ptr()) };
        unsafe { exit(11) };
    }
    unsafe { fchmod(tmp_fd, 0o755) };
    unsafe { close(tmp_fd) };

    // ── Write payload to a temp file ──────────────────────────────────────
    let payload_fd = unsafe { open(payload_path.as_ptr(), O_RDWR | O_CREAT | O_EXCL, 0o600) };
    if payload_fd < 0 {
        unsafe { unlink(washmhost_path.as_ptr()) };
        unsafe { exit(14) };
    }
    unsafe { fchmod(payload_fd, 0o644) };

    if !unsafe { write_all(payload_fd, payload_data) } {
        unsafe { close(payload_fd) };
        unsafe { unlink(payload_path.as_ptr()) };
        unsafe { unlink(washmhost_path.as_ptr()) };
        unsafe { exit(15) };
    }
    unsafe { close(payload_fd) };

    // ── Build env and argv for the child process ──────────────────────────
    let mut wasm_fd_var: Vec<u8> = b"MOHABBAT_WASM_FD=".to_vec();
    wasm_fd_var.extend_from_slice(&payload_path[..payload_path.len() - 1]); // skip NUL
    wasm_fd_var.push(0);

    let mut envp_ptrs: Vec<*const u8> = Vec::new();
    if !envp.is_null() {
        let mut e_ptr = envp;
        while !unsafe { (*e_ptr).is_null() } {
            let entry = unsafe { from_cstr(*e_ptr) };
            if !entry.starts_with(b"MOHABBAT_VEGETABLE_PATH=") {
                envp_ptrs.push(unsafe { *e_ptr });
            }
            e_ptr = unsafe { e_ptr.add(1) };
        }
    }
    envp_ptrs.push(wasm_fd_var.as_ptr());
    envp_ptrs.push(core::ptr::null());

    // argv[0] = vegetable path, remaining = extra user args (argv[7..]).
    let mut argv_ptrs: Vec<*const u8> = Vec::new();
    argv_ptrs.push(bat_path_ptr);
    if argc > 7 {
        let mut a_ptr = unsafe { argv.add(7) };
        while !unsafe { (*a_ptr).is_null() } {
            argv_ptrs.push(unsafe { *a_ptr });
            a_ptr = unsafe { a_ptr.add(1) };
        }
    }
    argv_ptrs.push(core::ptr::null());

    // ── fork + execve ─────────────────────────────────────────────────────
    let child_pid = unsafe { fork() };
    if child_pid < 0 {
        unsafe { unlink(payload_path.as_ptr()) };
        unsafe { unlink(washmhost_path.as_ptr()) };
        unsafe { exit(16) };
    }

    if child_pid == 0 {
        // Child: exec washmhost.
        unsafe {
            execve(
                washmhost_path.as_ptr(),
                argv_ptrs.as_ptr(),
                envp_ptrs.as_ptr(),
            )
        };
        // execve returned → failure.
        unsafe { exit(17) };
    }

    // ── Parent: wait for child, then clean up ─────────────────────────────
    let mut wait_status: i32 = 0;
    unsafe { waitpid(child_pid, &mut wait_status, 0) };

    unsafe { unlink(payload_path.as_ptr()) };
    unsafe { unlink(washmhost_path.as_ptr()) };

    // WIFEXITED(s): (s & 0x7f) == 0;  WEXITSTATUS(s): (s >> 8) & 0xff
    let exit_code: i32 = if (wait_status & 0x7f) == 0 {
        (wait_status >> 8) & 0xff
    } else {
        1
    };
    unsafe { exit(exit_code) };
}
