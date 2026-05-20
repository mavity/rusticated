import sys
content = open('wasmtime-host/src/env_impl.rs', 'r', encoding='utf-8').read()

start_str = '    // write(ov, handle, ptr, len)\n'
end_str = '    // handle_close(handle)\n'

start_idx = content.find(start_str)
end_idx = content.find(end_str)

if start_idx == -1 or end_idx == -1:
    print('Failed to find blocks')
    sys.exit(1)

new_write = '''    // write(ov, handle, ptr, len)
    linker.func_wrap(
        "env",
        "write",
        |mut caller: Caller<'_, HostState>,
         ov_ptr: u32,
         handle: u64,
         buf_ptr: u32,
         buf_len: u32|
         -> anyhow::Result<()> {
            let mut mem_ok = get_memory(&mut caller)?;
            let (data, host) = mem_ok.data_and_store_mut(&mut caller);
            
            // We need to borrow guest memory first to copy it, because we can't
            // borrow data and host exclusively in multiple places if we aren't careful.
            // Oh actually data_and_store_mut borrows the Store contexts, giving us separate data and host.
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
                            error = 9;
                        }
                    }
                    HandleKind::File(ref mut f) => {
                        match f.write(&src) {
                            Ok(n) => written = n,
                            Err(e) => error = e.raw_os_error().unwrap_or(5) as u32,
                        }
                    }
                }
            } else {
                anyhow::bail!("write: invalid handle {}", handle);
            }
            write_overlapped(data, ov_ptr, error, 0, written as u64)
        },
    )?;

'''

with open('wasmtime-host/src/env_impl.rs', 'w', encoding='utf-8') as f:
    f.write(content[:start_idx] + new_write + content[end_idx:])
print('Success write')
