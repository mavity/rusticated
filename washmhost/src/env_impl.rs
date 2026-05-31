use std::prelude::rust_2024::*;

use wasmtime::{Caller, Linker, Memory, Store};

use crate::handles::{FileOpResult, HandleKind, HostState, PendingOp};

// ── Overlapped layout (matches src/abi.rs) ───────────────────────────────────
//   offset  0: flags: u32      (1 = FLAG_COMPLETED)
//   offset  4: error: u32
//   offset  8: continued: u64
//   offset 16: result_ext: u64
//   total: 24 bytes, little-endian

const EIO: i32 = 5;
const EWOULDBLOCK: u32 = 11;
const EINVAL: u32 = 22;
const ERANGE: u32 = 34;
const ENOSYS: u32 = 38;
const STAT_FLAG_NOFOLLOW: u32 = 1;
const STAT_KIND_UNKNOWN: u32 = 0;
const STAT_KIND_FILE: u32 = 1;
const STAT_KIND_DIR: u32 = 2;
const STAT_KIND_SYMLINK: u32 = 3;
const ABI_STAT_SIZE: usize = 64;

fn encode_stat_payload(meta: &std::fs::Metadata) -> std::vec::Vec<u8> {
    let kind = if meta.is_dir() {
        STAT_KIND_DIR
    } else if meta.is_symlink() {
        STAT_KIND_SYMLINK
    } else if meta.is_file() {
        STAT_KIND_FILE
    } else {
        STAT_KIND_UNKNOWN
    };

    let mtime_ns = meta
        .modified()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    let atime_ns = meta
        .accessed()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    let ctime_ns = meta
        .created()
        .ok()
        .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);

    #[cfg(unix)]
    let (mode, nlink, uid, gid, inode) = {
        use std::os::unix::fs::MetadataExt;
        (meta.mode(), meta.nlink(), meta.uid(), meta.gid(), meta.ino())
    };
    #[cfg(windows)]
    let (mode, nlink, uid, gid, inode) = {
        use std::os::windows::fs::MetadataExt;
        let attrs = meta.file_attributes();
        let ro = (attrs & 1) != 0;
        let base: u32 = if meta.is_dir() { 0o040_000 } else { 0o100_000 };
        let perms: u32 = if ro { 0o444 } else { 0o666 };
        let idx = 0u64;
        (base | perms, 0u64, 0u32, 0u32, idx)
    };
    #[cfg(not(any(unix, windows)))]
    let (mode, nlink, uid, gid, inode) = {
        let ro = meta.permissions().readonly();
        let perms: u32 = if ro { 0o444 } else { 0o666 };
        (perms, 0u64, 0u32, 0u32, 0u64)
    };

    let mut out = vec![0u8; ABI_STAT_SIZE];
    out[0..4].copy_from_slice(&kind.to_le_bytes());
    out[4..8].copy_from_slice(&mode.to_le_bytes());
    out[8..12].copy_from_slice(&uid.to_le_bytes());
    out[12..16].copy_from_slice(&gid.to_le_bytes());
    out[16..24].copy_from_slice(&(meta.len()).to_le_bytes());
    out[24..32].copy_from_slice(&mtime_ns.to_le_bytes());
    out[32..40].copy_from_slice(&atime_ns.to_le_bytes());
    out[40..48].copy_from_slice(&ctime_ns.to_le_bytes());
    out[48..56].copy_from_slice(&nlink.to_le_bytes());
    out[56..64].copy_from_slice(&inode.to_le_bytes());
    out
}

fn write_overlapped(
    mem: &mut [u8],
    ov_ptr: u32,
    error: u32,
    continued: u64,
    result_ext: u64,
) -> wasmtime::Result<()> {
    let base = ov_ptr as usize;
    let end = base
        .checked_add(24)
        .ok_or_else(|| wasmtime::format_err!("ov_ptr overflow"))?;
    let slice = mem
        .get_mut(base..end)
        .ok_or_else(|| wasmtime::format_err!("ov_ptr out of WASM memory bounds"))?;
    slice[0..4].copy_from_slice(&1u32.to_le_bytes()); // flags = FLAG_COMPLETED
    slice[4..8].copy_from_slice(&error.to_le_bytes());
    slice[8..16].copy_from_slice(&continued.to_le_bytes());
    slice[16..24].copy_from_slice(&result_ext.to_le_bytes());
    Ok(())
}

fn guest_slice<'a>(mem: &'a mut [u8], ptr: u32, len: u32) -> wasmtime::Result<&'a mut [u8]> {
    let start = ptr as usize;
    let end = start
        .checked_add(len as usize)
        .ok_or_else(|| wasmtime::format_err!("ptr+len overflow"))?;
    mem.get_mut(start..end)
        .ok_or_else(|| wasmtime::format_err!("guest ptr={} len={} out of WASM bounds", ptr, len))
}

fn get_memory(caller: &mut Caller<'_, HostState>) -> wasmtime::Result<Memory> {
    caller
        .get_export("memory")
        .and_then(|e| e.into_memory())
        .ok_or_else(|| wasmtime::format_err!("WASM module has no 'memory' export"))
}

// ── Register all env imports with the Linker ─────────────────────────────────

pub fn register(linker: &mut Linker<HostState>) -> wasmtime::Result<()> {
    linker.func_wrap(
        "env",
        "host_panic",
        |_: Caller<'_, HostState>| -> wasmtime::Result<()> {
            println!("*** WASM INVOKED HOST PANIC ***");
            Err(wasmtime::format_err!("WASM Panicked!"))
        },
    )?;

    // get_time() -> u64  (nanoseconds since Unix epoch)
    linker.func_wrap("env", "get_time", |_caller: Caller<'_, HostState>| -> u64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos() as u64)
            .unwrap_or(0)
    })?;

    // get_random(ptr, len)
    linker.func_wrap(
        "env",
        "get_random",
        |mut caller: Caller<'_, HostState>, ptr: u32, len: u32| -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            let buf = guest_slice(data, ptr, len)?;
            #[cfg(windows)]
            {
                #[link(name = "bcrypt", kind = "raw-dylib")]
                unsafe extern "system" {
                    fn BCryptGenRandom(
                        h_algorithm: usize,
                        buffer: *mut u8,
                        size: u32,
                        flags: u32,
                    ) -> i32;
                }
                const BCRYPT_USE_SYSTEM_PREFERRED_RNG: u32 = 0x0000_0002;

                let status = unsafe {
                    BCryptGenRandom(
                        0,
                        buf.as_mut_ptr(),
                        buf.len() as u32,
                        BCRYPT_USE_SYSTEM_PREFERRED_RNG,
                    )
                };
                if status != 0 {
                    return Err(wasmtime::format_err!(
                        "BCryptGenRandom failed with status {}",
                        status
                    ));
                }
            }

            #[cfg(unix)]
            {
                let mut filled = 0usize;
                while filled < buf.len() {
                    let n = unsafe {
                        libc::getrandom(
                            buf[filled..].as_mut_ptr() as *mut libc::c_void,
                            buf.len() - filled,
                            0,
                        )
                    };
                    if n < 0 {
                        return Err(wasmtime::format_err!(
                            "os error: {:?}",
                            std::io::Error::last_os_error()
                        ));
                    }
                    if n == 0 {
                        continue;
                    }
                    filled += n as usize;
                }
            }

            #[cfg(not(any(windows, unix)))]
            {
                return Err(wasmtime::format_err!(
                    "get_random is unsupported on this host"
                ));
            }

            Ok(())
        },
    )?;

    // get_args(ptr, len) -> u64
    // Protocol: first call with ptr=0 returns (count<<32)|bytes_needed.
    //           second call with real ptr fills the buffer.
    linker.func_wrap(
        "env",
        "get_args",
        |mut caller: Caller<'_, HostState>, ptr: u32, len: u32| -> wasmtime::Result<u64> {
            // Skip argv[0] (the wasmtime host exe itself); the WASM path becomes the
            // guest's argv[0], matching how the native binary sees its own path.
            let args: Vec<Vec<u8>> = std::env::args_os()
                .skip(1)
                .map(|a| a.into_encoded_bytes())
                .collect();
            let bytes_needed: u32 = args.iter().map(|a| a.len() as u32 + 1).sum();
            let count = args.len() as u64;
            if ptr != 0 && len >= bytes_needed {
                let mem = get_memory(&mut caller)?;
                let data = mem.data_mut(&mut caller);
                let buf = guest_slice(data, ptr, bytes_needed)?;
                let mut off = 0usize;
                for arg in &args {
                    buf[off..off + arg.len()].copy_from_slice(arg);
                    off += arg.len();
                    buf[off] = 0;
                    off += 1;
                }
            }
            Ok((count << 32) | bytes_needed as u64)
        },
    )?;

    // get_env(ptr, len) -> u64
    linker.func_wrap(
        "env",
        "get_env",
        |mut caller: Caller<'_, HostState>, ptr: u32, len: u32| -> wasmtime::Result<u64> {
            let vars: Vec<(Vec<u8>, Vec<u8>)> = std::env::vars_os()
                .map(|(k, v)| (k.into_encoded_bytes(), v.into_encoded_bytes()))
                .collect();
            let bytes_needed: u32 = vars
                .iter()
                .map(|(k, v)| k.len() as u32 + 1 + v.len() as u32 + 1)
                .sum();
            let count = vars.len() as u64;
            if ptr != 0 && len >= bytes_needed {
                let mem = get_memory(&mut caller)?;
                let data = mem.data_mut(&mut caller);
                let buf = guest_slice(data, ptr, bytes_needed)?;
                let mut off = 0usize;
                for (k, v) in &vars {
                    buf[off..off + k.len()].copy_from_slice(k);
                    off += k.len();
                    buf[off] = b'=';
                    off += 1;
                    buf[off..off + v.len()].copy_from_slice(v);
                    off += v.len();
                    buf[off] = 0;
                    off += 1;
                }
            }
            Ok((count << 32) | bytes_needed as u64)
        },
    )?;

    // timer_set(ov, delay_ms)
    linker.func_wrap(
        "env",
        "timer_set",
        |mut caller: Caller<'_, HostState>, ov_ptr: u32, delay_ms: u32| -> wasmtime::Result<()> {
            let deadline =
                std::time::Instant::now() + std::time::Duration::from_millis(delay_ms as u64);
            caller.data_mut().timers.insert(ov_ptr, deadline);
            Ok(())
        },
    )?;

    // timer_cancel(ov)
    linker.func_wrap(
        "env",
        "timer_cancel",
        |mut caller: Caller<'_, HostState>, ov_ptr: u32| {
            caller.data_mut().timers.remove(&ov_ptr);
        },
    )?;

    // read(ov, handle, ptr, len)
    // Regular files: queued async read; completed in poll_completions.
    // Streams (stdin, pipes, sockets): register with epoll; complete later.
    linker.func_wrap(
        "env",
        "read",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         handle: u64,
         guest_ptr: u32,
         guest_len: u32|
         -> wasmtime::Result<()> {
            let mem_ok = get_memory(&mut caller)?;
            let (data, host) = mem_ok.data_and_store_mut(&mut caller);

            let is_file = host.is_regular_file(handle);

            if is_file {
                let mut file = match host.handles.remove(&handle) {
                    Some(HandleKind::File(file)) => file,
                    _ => wasmtime::bail!("read: invalid handle {}", handle),
                };
                let tx = host.file_op_tx.clone();

                std::rt::executor::spawn(async move {
                    use std::traits::AsyncRead;

                    let (result, mut buf) = file.read(vec![0u8; guest_len as usize]).await;
                    let (error, read_len) = match result {
                        Ok(n) => (0, n),
                        Err(e) => (e.raw_os_error().unwrap_or(EIO) as u32, 0),
                    };
                    if buf.len() > read_len {
                        buf.truncate(read_len);
                    }
                    let _ = tx.send(FileOpResult::Read {
                        ov_ptr,
                        handle,
                        guest_ptr,
                        guest_len,
                        data: buf,
                        error,
                        file,
                    });
                });

                return Ok(());
            } else {
                let fd = host
                    .fd_for(handle)
                    .ok_or_else(|| wasmtime::format_err!("read: invalid handle {}", handle))?;
                if fd == 0 {
                    // Blocking stdin reads are forbidden in strict async mode.
                    write_overlapped(data, ov_ptr, EWOULDBLOCK, 0, 0)?;
                } else {
                    // Other stream fds: register with epoll; completion happens in poll_completions().
                    let token = host.epoll.register_read(fd);
                    host.epoll.pending.insert(
                        token,
                        PendingOp {
                            ov_ptr,
                            guest_ptr,
                            guest_len,
                            _fd: fd,
                        },
                    );
                }
            }
            Ok(())
        },
    )?;

    // write(ov, handle, ptr, len)
    linker.func_wrap(
        "env",
        "write",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         handle: u64,
         buf_ptr: u32,
         buf_len: u32|
         -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);
            let src = guest_slice(data, buf_ptr, buf_len)?.to_vec();

            if host.is_regular_file(handle) {
                let mut file = match host.handles.remove(&handle) {
                    Some(HandleKind::File(file)) => file,
                    _ => wasmtime::bail!("write: invalid handle {}", handle),
                };
                let tx = host.file_op_tx.clone();

                std::rt::executor::spawn(async move {
                    use std::traits::AsyncWrite;

                    let (result, _buf) = file.write(src).await;
                    let (error, written) = match result {
                        Ok(n) => (0, n as u64),
                        Err(e) => (e.raw_os_error().unwrap_or(EIO) as u32, 0),
                    };
                    let _ = tx.send(FileOpResult::Write {
                        ov_ptr,
                        handle,
                        written,
                        error,
                        file,
                    });
                });

                return Ok(());
            }

            let mut error = 0;
            let mut written = 0;

            if let Some(kind) = host.handles.get_mut(&handle) {
                match kind {
                    HandleKind::Fd(fd) => {
                        let fd = *fd;
                        if fd == 1 {
                            let _ = src;
                            error = EWOULDBLOCK;
                        } else if fd == 2 {
                            let _ = src;
                            error = EWOULDBLOCK;
                        } else {
                            error = 9; // EBADF
                        }
                    }
                    HandleKind::File(_) => {
                        error = 9; // EBADF - async file path should have been taken above.
                    }
                    HandleKind::Process(_) | HandleKind::Dir(_, _) => {
                        error = 9; // EBADF — cannot write to a process handle
                    }
                }
            } else {
                wasmtime::bail!("write: invalid handle {}", handle);
            }
            write_overlapped(data, ov_ptr, error, 0, written as u64)
        },
    )?;

    // handle_close(handle)
    linker.func_wrap(
        "env",
        "handle_close",
        |mut caller: Caller<'_, HostState>, handle: u64| {
            let host = caller.data_mut();
            if let Some(kind) = host.handles.remove(&handle) {
                if let HandleKind::Fd(_fd) = kind {}
                // HandleKind::File is dropped here, which closes the file.
            }
        },
    )?;

    // path_open(ov, path_ptr, path_len, flags)
    // flags: 1=read, 2=write, 4=create, 8=truncate, 16=append, 32=create_new
    linker.func_wrap(
        "env",
        "path_open",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         path_ptr: u32,
         path_len: u32,
         flags: u32|
         -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);
            let path_bytes = guest_slice(data, path_ptr, path_len)?.to_vec();
            let path = std::str::from_utf8(&path_bytes)
                .map_err(|_| wasmtime::format_err!("path_open: non-UTF-8 path"))?;
            let path_owned = path.to_owned();
            let read_flag = (flags & 1) != 0;
            let write_flag = (flags & 2) != 0;
            let create = (flags & 4) != 0;
            let truncate = (flags & 8) != 0;
            let append = (flags & 16) != 0;
            let create_new = (flags & 32) != 0;
            // Default to read if neither read nor write is set.
            let do_read = read_flag || (!write_flag && !create);
            let do_write = write_flag || create;
            let tx = host.file_op_tx.clone();

            std::rt::executor::spawn(async move {
                let is_dir = std::fs::metadata(&path_owned)
                    .await
                    .map(|m| m.is_dir())
                    .unwrap_or(false);
                let result = if is_dir {
                    match std::fs::read_dir(&path_owned).await {
                        Ok(rd) => Ok(HandleKind::Dir(rd, Vec::new())),
                        Err(e) => Err(e.raw_os_error().unwrap_or(5) as u32),
                    }
                } else {
                    let mut opts = std::fs::OpenOptions::new();
                    opts.read(do_read)
                        .write(do_write)
                        .create(create && !create_new)
                        .create_new(create_new)
                        .truncate(truncate)
                        .append(append);
                    match opts.open(&path_owned).await {
                        Ok(f) => Ok(HandleKind::File(f)),
                        Err(e) => Err(e.raw_os_error().unwrap_or(5) as u32),
                    }
                };

                let _ = tx.send(FileOpResult::PathOpen { ov_ptr, result });
            });

            Ok(())
        },
    )?;

    // path_stat(ov, path_ptr, path_len, flags, out_ptr, out_len)
    linker.func_wrap(
        "env",
        "path_stat",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         path_ptr: u32,
         path_len: u32,
         flags: u32,
         out_ptr: u32,
         out_len: u32|
         -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);
            let path_bytes = guest_slice(data, path_ptr, path_len)?.to_vec();
            let path = std::str::from_utf8(&path_bytes)
                .map_err(|_| wasmtime::format_err!("path_stat: non-UTF-8 path"))?;
            let path_owned = path.to_owned();
            let tx = host.file_op_tx.clone();

            std::rt::executor::spawn(async move {
                let result = if (flags & STAT_FLAG_NOFOLLOW) != 0 {
                    std::fs::symlink_metadata(&path_owned)
                        .await
                        .map(|m| encode_stat_payload(&m))
                        .map_err(|e| e.raw_os_error().unwrap_or(EIO) as u32)
                } else {
                    std::fs::metadata(&path_owned)
                        .await
                        .map(|m| encode_stat_payload(&m))
                        .map_err(|e| e.raw_os_error().unwrap_or(EIO) as u32)
                };

                let _ = tx.send(FileOpResult::PathStat {
                    ov_ptr,
                    out_ptr,
                    out_len,
                    result,
                });
            });

            Ok(())
        },
    )?;

    // dir_read(ov, handle, ptr, len) — stub: no entries
    linker.func_wrap(
        "env",
        "dir_read",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         handle: u64,
         ptr: u32,
         len: u32|
         -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);

            let mut read_bytes = 0;
            let mut error = 0;

            if let Some(HandleKind::Dir(rd, leftovers)) = host.handles.get_mut(&handle) {
                let max_len = len as usize;

                while leftovers.len() < max_len {
                    if let Some(entry_res) = rd.next() {
                        match entry_res {
                            Ok(entry) => {
                                let bytes = entry.file_name().as_bytes().to_vec();
                                leftovers.extend_from_slice(&bytes);
                                leftovers.push(0); // null terminator
                            }
                            Err(e) => {
                                if leftovers.is_empty() {
                                    error = e.raw_os_error().unwrap_or(5) as u32; // EIO
                                }
                                break;
                            }
                        }
                    } else {
                        break;
                    }
                }

                if error == 0 {
                    let take_len = std::cmp::min(max_len, leftovers.len());
                    if take_len > 0 {
                        let chunk: std::vec::Vec<u8> = leftovers.drain(..take_len).collect();
                        let guest_slice = &mut guest_slice(data, ptr, len)?[..take_len];
                        guest_slice.copy_from_slice(&chunk);
                        read_bytes = take_len as u64;
                    }
                }
            } else {
                error = 9; // EBADF
            }

            write_overlapped(data, ov_ptr, error, read_bytes, 0)
        },
    )?;

    // net_open(ov, addr_ptr, addr_len, port, flags) — ENOSYS
    linker.func_wrap(
        "env",
        "net_open",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         _addr_ptr: u32,
         _addr_len: u32,
         _port: u32,
         _flags: u32|
         -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, ENOSYS, 0, 0)
        },
    )?;

    // net_accept(ov, listen_handle) — ENOSYS
    linker.func_wrap(
        "env",
        "net_accept",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         _listen_handle: u64|
         -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, ENOSYS, 0, 0)
        },
    )?;

    // process_spawn(ov, cfg_ptr, cfg_len)
    // Config format: program\0arg1\0arg2\0\0KEY=val\0KEY2=val2\0\0
    linker.func_wrap(
        "env",
        "process_spawn",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         cfg_ptr: u32,
         cfg_len: u32|
         -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);
            let cfg = guest_slice(data, cfg_ptr, cfg_len)?.to_vec();

            // Parse: program\0arg1\0arg2\0\0KEY=val\0\0
            let mut iter = cfg.split(|&b| b == 0);
            let program = match iter.next() {
                Some(p) if !p.is_empty() => match std::str::from_utf8(p) {
                    Ok(s) => s.to_string(),
                    Err(_) => {
                        return write_overlapped(data, ov_ptr, EINVAL, 0, 0);
                    }
                },
                _ => {
                    return write_overlapped(data, ov_ptr, EINVAL, 0, 0);
                }
            };

            let mut args: Vec<String> = Vec::new();
            let mut env_vars: Vec<(String, String)> = Vec::new();
            let mut in_env = false;
            for part in iter {
                if part.is_empty() {
                    if !in_env {
                        in_env = true;
                    } else {
                        break;
                    }
                    continue;
                }
                if in_env {
                    if let Ok(kv) = std::str::from_utf8(part) {
                        if let Some(eq) = kv.find('=') {
                            env_vars.push((kv[..eq].to_string(), kv[eq + 1..].to_string()));
                        }
                    }
                } else if let Ok(arg) = std::str::from_utf8(part) {
                    args.push(arg.to_string());
                }
            }

            let mut cmd = std::process::Command::new(&program);
            cmd.args(&args);
            for (k, v) in env_vars {
                cmd.env(k, v);
            }

            match cmd.spawn() {
                Ok(child) => {
                    let handle = host.alloc_handle(HandleKind::Process(child));
                    write_overlapped(data, ov_ptr, 0, 0, handle)
                }
                Err(e) => {
                    let err = e.raw_os_error().unwrap_or(ENOSYS as i32) as u32;
                    write_overlapped(data, ov_ptr, err, 0, 0)
                }
            }
        },
    )?;

    // process_wait(ov, process_handle)
    // Registers a pending wait; completion is handled in poll_completions.
    linker.func_wrap(
        "env",
        "process_wait",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         process_handle: u64|
         -> wasmtime::Result<()> {
            caller
                .data_mut()
                .child_wait_pending
                .push((ov_ptr, process_handle));
            Ok(())
        },
    )?;

    // process_signal(process_handle, signum) — no-op
    linker.func_wrap(
        "env",
        "process_signal",
        |_: Caller<'_, HostState>, _process_handle: u64, _signum: u32| {},
    )?;

    // signal_wait(ov, signum) — ENOSYS
    linker.func_wrap(
        "env",
        "signal_wait",
        |mut caller: Caller<'_, HostState>, ov_ptr: u32, _signum: u32| -> wasmtime::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, ENOSYS, 0, 0)
        },
    )?;

    // tty_set_mode(handle, mode) — no-op
    linker.func_wrap(
        "env",
        "tty_set_mode",
        |_: Caller<'_, HostState>, _h: u64, _mode: u32| {},
    )?;

    // tty_get_size(handle) -> u32  (80 cols × 24 rows)
    linker.func_wrap(
        "env",
        "tty_get_size",
        |_: Caller<'_, HostState>, _h: u64| -> u32 { (80u32 << 16) | 24 },
    )?;

    Ok(())
}

// ── Epoll completion draining ─────────────────────────────────────────────────

/// Drain fired epoll events and complete the corresponding guest Overlapped
/// structs in WASM linear memory.
///
/// `timeout_ms = 0`: non-blocking poll.
/// `timeout_ms > 0`: block up to that many milliseconds waiting for I/O.
pub fn poll_completions(
    store: &mut Store<HostState>,
    memory: &Memory,
    timeout_ms: i32,
) -> wasmtime::Result<bool> {
    let mut progress = false;
    let now = std::time::Instant::now();

    // Drive futures spawned by host imports (e.g. async path_open).
    let _ = std::rt::executor::poll_step();

    // 0. Complete async file operations.
    loop {
        let op = match store.data_mut().file_op_rx.try_recv() {
            Ok(op) => op,
            Err(std::sync::mpsc::TryRecvError::Empty) => break,
            Err(std::sync::mpsc::TryRecvError::Disconnected) => break,
        };

        let (mem, host) = memory.data_and_store_mut(&mut *store);
        match op {
            FileOpResult::PathOpen { ov_ptr, result } => {
                match result {
                    Ok(kind) => {
                        let handle = host.alloc_handle(kind);
                        write_overlapped(mem, ov_ptr, 0, 0, handle)?;
                    }
                    Err(error) => {
                        write_overlapped(mem, ov_ptr, error, 0, 0)?;
                    }
                }
                progress = true;
            }
            FileOpResult::PathStat {
                ov_ptr,
                out_ptr,
                out_len,
                result,
            } => {
                match result {
                    Ok(payload) => {
                        if (out_len as usize) < ABI_STAT_SIZE {
                            write_overlapped(mem, ov_ptr, ERANGE, 0, ABI_STAT_SIZE as u64)?;
                        } else {
                            let out = guest_slice(mem, out_ptr, out_len)?;
                            out[..ABI_STAT_SIZE].copy_from_slice(&payload[..ABI_STAT_SIZE]);
                            write_overlapped(mem, ov_ptr, 0, 0, ABI_STAT_SIZE as u64)?;
                        }
                    }
                    Err(error) => {
                        write_overlapped(mem, ov_ptr, error, 0, ABI_STAT_SIZE as u64)?;
                    }
                }
                progress = true;
            }
            FileOpResult::Read {
                ov_ptr,
                handle,
                guest_ptr,
                guest_len,
                data,
                error,
                file,
            } => {
                host.handles.insert(handle, HandleKind::File(file));

                let mut read_len = 0usize;
                if error == 0 {
                    let to_copy = core::cmp::min(data.len(), guest_len as usize);
                    if to_copy > 0 {
                        let dest = guest_slice(mem, guest_ptr, guest_len)?;
                        dest[..to_copy].copy_from_slice(&data[..to_copy]);
                    }
                    read_len = to_copy;
                }

                write_overlapped(mem, ov_ptr, error, 0, read_len as u64)?;
                progress = true;
            }
            FileOpResult::Write {
                ov_ptr,
                handle,
                written,
                error,
                file,
            } => {
                host.handles.insert(handle, HandleKind::File(file));
                write_overlapped(mem, ov_ptr, error, 0, written)?;
                progress = true;
            }
        }
    }

    // 1. Check timers
    let expired: Vec<u32> = store
        .data()
        .timers
        .iter()
        .filter(|&(_, &deadline)| now >= deadline)
        .map(|(&ov, _)| ov)
        .collect();

    if !expired.is_empty() {
        let (mem, host) = memory.data_and_store_mut(&mut *store);
        for ov_ptr in expired {
            host.timers.remove(&ov_ptr);
            write_overlapped(mem, ov_ptr, 0, 0, 0)?;
            progress = true;
        }
    }

    // 2. Check stdin reader thread — drain any available bytes into the buffer,
    //    then fulfill a pending read op if there is one and data is available.
    loop {
        match store.data_mut().stdin_rx.try_recv() {
            Ok(bytes) => store.data_mut().stdin_buf.extend_from_slice(&bytes),
            Err(_) => break,
        }
    }
    if store.data().stdin_pending.is_some() && !store.data().stdin_buf.is_empty() {
        let (mem, host) = memory.data_and_store_mut(&mut *store);
        let op = host.stdin_pending.take().unwrap();
        let to_copy = host.stdin_buf.len().min(op.guest_len as usize);
        let dest_start = op.guest_ptr as usize;
        let dest_end = dest_start + to_copy;
        if dest_end <= mem.len() {
            mem[dest_start..dest_end].copy_from_slice(&host.stdin_buf[..to_copy]);
            host.stdin_buf.drain(..to_copy);
            write_overlapped(mem, op.ov_ptr, 0, 0, to_copy as u64)?;
            progress = true;
        } else {
            write_overlapped(mem, op.ov_ptr, 22, 0, 0)?; // EINVAL
            host.stdin_buf.clear();
            progress = true;
        }
    } else if store.data().stdin_pending.is_some() && timeout_ms > 0 {
        // No data yet but caller is willing to block briefly — wait for stdin.
        match store
            .data_mut()
            .stdin_rx
            .recv_timeout(std::time::Duration::from_millis(timeout_ms as u64))
        {
            Ok(bytes) => {
                store.data_mut().stdin_buf.extend_from_slice(&bytes);
                // Retry immediately now that we have data.
                if !store.data().stdin_buf.is_empty() {
                    let (mem, host) = memory.data_and_store_mut(&mut *store);
                    let op = host.stdin_pending.take().unwrap();
                    let to_copy = host.stdin_buf.len().min(op.guest_len as usize);
                    let dest_start = op.guest_ptr as usize;
                    let dest_end = dest_start + to_copy;
                    if dest_end <= mem.len() {
                        mem[dest_start..dest_end].copy_from_slice(&host.stdin_buf[..to_copy]);
                        host.stdin_buf.drain(..to_copy);
                        write_overlapped(mem, op.ov_ptr, 0, 0, to_copy as u64)?;
                        progress = true;
                    }
                }
            }
            Err(_) => {} // timeout or disconnect — keep op pending
        }
    }

    // 3. Check other I/O (non-stdin epoll)
    let fired = store.data_mut().epoll.poll(0);
    if !fired.is_empty() {
        let (mem, host) = memory.data_and_store_mut(&mut *store);
        for token in fired {
            let Some(op) = host.epoll.pending.remove(&token) else {
                continue;
            };
            let _ = write_overlapped(mem, op.ov_ptr, 0, 0, 0); // EOF stub
            progress = true;
        }
    }

    // 4. Check pending child process waits.
    if !store.data().child_wait_pending.is_empty() {
        let pending: Vec<(u32, u64)> = store.data().child_wait_pending.clone();
        let mut completed_handles: Vec<u64> = Vec::new();
        let mut results: Vec<(u32, i32)> = Vec::new();

        let (mem, host) = memory.data_and_store_mut(&mut *store);
        for (ov_ptr, handle) in &pending {
            if let Some(HandleKind::Process(child)) = host.handles.get_mut(handle) {
                match child.try_wait() {
                    Ok(Some(status)) => {
                        let code = status.code().unwrap_or(1);
                        results.push((*ov_ptr, code));
                        completed_handles.push(*handle);
                    }
                    Ok(None) => {} // still running
                    Err(_) => {
                        results.push((*ov_ptr, 1));
                        completed_handles.push(*handle);
                    }
                }
            }
        }
        for handle in &completed_handles {
            host.handles.remove(handle);
        }
        host.child_wait_pending
            .retain(|(_, h)| !completed_handles.contains(h));
        for (ov_ptr, code) in results {
            write_overlapped(mem, ov_ptr, 0, 0, (code as u64) << 32)?;
            progress = true;
        }
    }

    Ok(progress)
}
