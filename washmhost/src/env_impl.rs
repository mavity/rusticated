use anyhow::Context as _;
use wasmtime::{Caller, Linker, Memory, Store};

use crate::handles::{HandleKind, HostState, PendingOp, StatInfo};

// ── Overlapped layout (matches src/abi.rs) ───────────────────────────────────
//   offset  0: flags: u32      (1 = FLAG_COMPLETED)
//   offset  4: error: u32
//   offset  8: continued: u64
//   offset 16: result_ext: u64
//   total: 24 bytes, little-endian

fn write_overlapped(
    mem: &mut [u8],
    ov_ptr: u32,
    error: u32,
    continued: u64,
    result_ext: u64,
) -> anyhow::Result<()> {
    let base = ov_ptr as usize;
    let end = base.checked_add(24).context("ov_ptr overflow")?;
    let slice = mem
        .get_mut(base..end)
        .context("ov_ptr out of WASM memory bounds")?;
    slice[0..4].copy_from_slice(&1u32.to_le_bytes()); // flags = FLAG_COMPLETED
    slice[4..8].copy_from_slice(&error.to_le_bytes());
    slice[8..16].copy_from_slice(&continued.to_le_bytes());
    slice[16..24].copy_from_slice(&result_ext.to_le_bytes());
    Ok(())
}

fn guest_slice<'a>(mem: &'a mut [u8], ptr: u32, len: u32) -> anyhow::Result<&'a mut [u8]> {
    let start = ptr as usize;
    let end = start
        .checked_add(len as usize)
        .context("ptr+len overflow")?;
    mem.get_mut(start..end)
        .with_context(|| format!("guest ptr={ptr} len={len} out of WASM memory bounds"))
}

fn get_memory(caller: &mut Caller<'_, HostState>) -> anyhow::Result<Memory> {
    caller
        .get_export("memory")
        .and_then(|e| e.into_memory())
        .context("WASM module has no 'memory' export")
}

// ── Register all env imports with the Linker ─────────────────────────────────

pub fn register(linker: &mut Linker<HostState>) -> anyhow::Result<()> {
    linker.func_wrap(
        "env",
        "host_panic",
        |_: Caller<'_, HostState>| -> anyhow::Result<()> {
            println!("*** WASM INVOKED HOST PANIC ***");
            let _ = std::io::Write::flush(&mut std::io::stdout());
            Err(anyhow::anyhow!("WASM Panicked!"))
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
        |mut caller: Caller<'_, HostState>, ptr: u32, len: u32| -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            let buf = guest_slice(data, ptr, len)?;
            // Read cryptographic random bytes from /dev/urandom.
            let mut f = std::fs::File::open("/dev/urandom").context("open /dev/urandom")?;
            std::io::Read::read_exact(&mut f, buf).context("read /dev/urandom")?;
            Ok(())
        },
    )?;

    // get_args(ptr, len) -> u64
    // Protocol: first call with ptr=0 returns (count<<32)|bytes_needed.
    //           second call with real ptr fills the buffer.
    linker.func_wrap(
        "env",
        "get_args",
        |mut caller: Caller<'_, HostState>, ptr: u32, len: u32| -> anyhow::Result<u64> {
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
        |mut caller: Caller<'_, HostState>, ptr: u32, len: u32| -> anyhow::Result<u64> {
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
        |mut caller: Caller<'_, HostState>, ov_ptr: u32, delay_ms: u32| -> anyhow::Result<()> {
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
    // Regular files: synchronous read inside the import.
    // Streams (stdin, pipes, sockets): register with epoll; complete later.
    linker.func_wrap(
        "env",
        "read",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         handle: u64,
         guest_ptr: u32,
         guest_len: u32|
         -> anyhow::Result<()> {
            let mem_ok = get_memory(&mut caller)?;
            let (data, host) = mem_ok.data_and_store_mut(&mut caller);

            let is_file = host.is_regular_file(handle);

            if is_file {
                // Files are always ready — read synchronously.
                let buf = guest_slice(data, guest_ptr, guest_len)?;

                let mut error = 0;
                let mut read = 0;

                if let Some(HandleKind::File(f)) = host.handles.get_mut(&handle) {
                    use std::io::Read;
                    match f.read(buf) {
                        Ok(n) => read = n,
                        Err(e) => error = e.raw_os_error().unwrap_or(5) as u32,
                    }
                } else {
                    anyhow::bail!("read: invalid handle {}", handle);
                }

                write_overlapped(data, ov_ptr, error, 0, read as u64)?;
            } else {
                let fd = host
                    .fd_for(handle)
                    .with_context(|| format!("read: invalid handle {}", handle))?;
                if fd == 0 {
                    // stdin: fulfilled by the reader thread in poll_completions.
                    host.stdin_pending = Some(PendingOp {
                        ov_ptr,
                        guest_ptr,
                        guest_len,
                        _fd: fd,
                    });
                } else {
                    // Other stream fds: register with epoll; completion happens in poll_completions().
                    let token = host.epoll.register_read(fd)?;
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
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);
            let src = guest_slice(data, buf_ptr, buf_len)?.to_vec();
            use std::io::Write;

            let mut error = 0;
            let mut written = 0;

            if let Some(kind) = host.handles.get_mut(&handle) {
                match kind {
                    HandleKind::Fd(fd) => {
                        let fd = *fd;
                        if fd == 1 {
                            let _ = std::io::stdout().write_all(&src);
                            let _ = std::io::stdout().flush();
                            written = src.len();
                        } else if fd == 2 {
                            let _ = std::io::stderr().write_all(&src);
                            let _ = std::io::stderr().flush();
                            written = src.len();
                        } else {
                            error = 9; // EBADF
                        }
                    }
                    HandleKind::File(f) => {
                        match f.write(&src) {
                            Ok(n) => written = n,
                            Err(e) => error = e.raw_os_error().unwrap_or(5) as u32, // EIO
                        }
                    }
                }
            } else {
                anyhow::bail!("write: invalid handle {}", handle);
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
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);
            let path_bytes = guest_slice(data, path_ptr, path_len)?.to_vec();
            let path = std::str::from_utf8(&path_bytes).context("path_open: non-UTF-8 path")?;
            let read_flag = (flags & 1) != 0;
            let write_flag = (flags & 2) != 0;
            let create = (flags & 4) != 0;
            let truncate = (flags & 8) != 0;
            let append = (flags & 16) != 0;
            let create_new = (flags & 32) != 0;
            // Default to read if neither read nor write is set.
            let do_read = read_flag || (!write_flag && !create);
            let do_write = write_flag || create;
            let f = std::fs::OpenOptions::new()
                .read(do_read)
                .write(do_write)
                .create(create && !create_new)
                .create_new(create_new)
                .truncate(truncate)
                .append(append)
                .open(path)
                .with_context(|| format!("path_open: {path:?}"))?;
            let new_handle = host.alloc_handle(HandleKind::File(f));
            write_overlapped(data, ov_ptr, 0, 0, new_handle)
        },
    )?;

    // path_stat(ov, path_ptr, path_len)
    linker.func_wrap(
        "env",
        "path_stat",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         path_ptr: u32,
         path_len: u32|
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let (data, host) = mem.data_and_store_mut(&mut caller);
            let path_bytes = guest_slice(data, path_ptr, path_len)?.to_vec();
            let path = std::str::from_utf8(&path_bytes).context("path_stat: non-UTF-8 path")?;
            // symlink_metadata (lstat semantics) so is_symlink is meaningful.
            let meta =
                std::fs::symlink_metadata(path).with_context(|| format!("path_stat: {path:?}"))?;
            let to_ns = |t: std::io::Result<std::time::SystemTime>| {
                t.ok()
                    .and_then(|t| t.duration_since(std::time::UNIX_EPOCH).ok())
                    .map(|d| d.as_nanos() as u64)
                    .unwrap_or(0)
            };
            let mtime_ns = to_ns(meta.modified());
            let atime_ns = to_ns(meta.accessed());
            let ctime_ns = to_ns(meta.created());
            let readonly = meta.permissions().readonly();
            let is_symlink = meta.is_symlink();
            #[cfg(unix)]
            let (mode, nlink, uid, gid, inode) = {
                use std::os::unix::fs::MetadataExt;
                (
                    meta.mode(),
                    meta.nlink(),
                    meta.uid(),
                    meta.gid(),
                    meta.ino(),
                )
            };
            #[cfg(windows)]
            let (mode, nlink, uid, gid, inode) = {
                use std::os::windows::fs::MetadataExt;
                let attrs = meta.file_attributes();
                // Synthesise mode from attributes: FILE_ATTRIBUTE_READONLY = 0x1
                let ro = (attrs & 1) != 0;
                let base: u32 = if meta.is_dir() { 0o040_000 } else { 0o100_000 };
                let perms: u32 = if ro { 0o444 } else { 0o666 };
                let idx = 0; // MOCK for Windows
                (base | perms, 0u64, 0u32, 0u32, idx)
            };
            #[cfg(not(any(unix, windows)))]
            let (mode, nlink, uid, gid, inode) = {
                let perms: u32 = if readonly { 0o444 } else { 0o666 };
                (perms, 0u64, 0u32, 0u32, 0u64)
            };
            let stat = StatInfo {
                len: meta.len(),
                is_dir: meta.is_dir(),
                is_symlink,
                readonly,
                mode,
                nlink,
                uid,
                gid,
                inode,
                mtime_ns,
                atime_ns,
                ctime_ns,
            };
            let stat_handle = host.alloc_stat(stat);
            write_overlapped(data, ov_ptr, 0, 0, stat_handle)
        },
    )?;

    // stat_len(stat_handle) -> u64
    linker.func_wrap(
        "env",
        "stat_len",
        |caller: Caller<'_, HostState>, h: u64| -> u64 {
            caller.data().stats.get(&h).map(|s| s.len).unwrap_or(0)
        },
    )?;

    // stat_is_dir(stat_handle) -> u32
    linker.func_wrap(
        "env",
        "stat_is_dir",
        |caller: Caller<'_, HostState>, h: u64| -> u32 {
            caller
                .data()
                .stats
                .get(&h)
                .map(|s| s.is_dir as u32)
                .unwrap_or(0)
        },
    )?;

    // stat_is_file(stat_handle) -> u32
    linker.func_wrap(
        "env",
        "stat_is_file",
        |caller: Caller<'_, HostState>, h: u64| -> u32 {
            caller
                .data()
                .stats
                .get(&h)
                .map(|s| if s.is_dir { 0 } else { 1 })
                .unwrap_or(0)
        },
    )?;

    // stat_mtime(stat_handle) -> u64  (nanoseconds since UNIX epoch)
    linker.func_wrap(
        "env",
        "stat_mtime",
        |caller: Caller<'_, HostState>, h: u64| -> u64 {
            caller.data().stats.get(&h).map(|s| s.mtime_ns).unwrap_or(0)
        },
    )?;

    // stat_atime(stat_handle) -> u64
    linker.func_wrap(
        "env",
        "stat_atime",
        |caller: Caller<'_, HostState>, h: u64| -> u64 {
            caller.data().stats.get(&h).map(|s| s.atime_ns).unwrap_or(0)
        },
    )?;

    // stat_ctime(stat_handle) -> u64
    linker.func_wrap(
        "env",
        "stat_ctime",
        |caller: Caller<'_, HostState>, h: u64| -> u64 {
            caller.data().stats.get(&h).map(|s| s.ctime_ns).unwrap_or(0)
        },
    )?;

    // stat_is_symlink(stat_handle) -> u32
    linker.func_wrap(
        "env",
        "stat_is_symlink",
        |caller: Caller<'_, HostState>, h: u64| -> u32 {
            caller
                .data()
                .stats
                .get(&h)
                .map(|s| s.is_symlink as u32)
                .unwrap_or(0)
        },
    )?;

    // stat_readonly(stat_handle) -> u32
    linker.func_wrap(
        "env",
        "stat_readonly",
        |caller: Caller<'_, HostState>, h: u64| -> u32 {
            caller
                .data()
                .stats
                .get(&h)
                .map(|s| s.readonly as u32)
                .unwrap_or(0)
        },
    )?;

    // stat_mode(stat_handle) -> u32
    linker.func_wrap(
        "env",
        "stat_mode",
        |caller: Caller<'_, HostState>, h: u64| -> u32 {
            caller.data().stats.get(&h).map(|s| s.mode).unwrap_or(0)
        },
    )?;

    // stat_nlink(stat_handle) -> u64
    linker.func_wrap(
        "env",
        "stat_nlink",
        |caller: Caller<'_, HostState>, h: u64| -> u64 {
            caller.data().stats.get(&h).map(|s| s.nlink).unwrap_or(0)
        },
    )?;

    // stat_uid(stat_handle) -> u32
    linker.func_wrap(
        "env",
        "stat_uid",
        |caller: Caller<'_, HostState>, h: u64| -> u32 {
            caller.data().stats.get(&h).map(|s| s.uid).unwrap_or(0)
        },
    )?;

    // stat_gid(stat_handle) -> u32
    linker.func_wrap(
        "env",
        "stat_gid",
        |caller: Caller<'_, HostState>, h: u64| -> u32 {
            caller.data().stats.get(&h).map(|s| s.gid).unwrap_or(0)
        },
    )?;

    // stat_inode(stat_handle) -> u64
    linker.func_wrap(
        "env",
        "stat_inode",
        |caller: Caller<'_, HostState>, h: u64| -> u64 {
            caller.data().stats.get(&h).map(|s| s.inode).unwrap_or(0)
        },
    )?;

    // dir_read(ov, handle, ptr, len) — stub: no entries
    linker.func_wrap(
        "env",
        "dir_read",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         _handle: u64,
         _ptr: u32,
         _len: u32|
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, 0, 0, 0)
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
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, libc::ENOSYS as u32, 0, 0)
        },
    )?;

    // net_accept(ov, listen_handle) — ENOSYS
    linker.func_wrap(
        "env",
        "net_accept",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         _listen_handle: u64|
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, libc::ENOSYS as u32, 0, 0)
        },
    )?;

    // process_spawn(ov, cfg_ptr, cfg_len) — ENOSYS
    linker.func_wrap(
        "env",
        "process_spawn",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         _cfg_ptr: u32,
         _cfg_len: u32|
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, libc::ENOSYS as u32, 0, 0)
        },
    )?;

    // process_wait(ov, process_handle) — ENOSYS
    linker.func_wrap(
        "env",
        "process_wait",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         _process_handle: u64|
         -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, libc::ENOSYS as u32, 0, 0)
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
        |mut caller: Caller<'_, HostState>, ov_ptr: u32, _signum: u32| -> anyhow::Result<()> {
            let mem = get_memory(&mut caller)?;
            let data = mem.data_mut(&mut caller);
            write_overlapped(data, ov_ptr, libc::ENOSYS as u32, 0, 0)
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
) -> anyhow::Result<bool> {
    let mut progress = false;
    let now = std::time::Instant::now();

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
    let fired = store.data_mut().epoll.poll(0)?;
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

    Ok(progress)
}
