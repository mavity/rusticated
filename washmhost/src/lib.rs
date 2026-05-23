mod env_impl;
mod handles;

use handles::HostState;
use wasmtime::{Config, Engine, Linker, Module, Store};

fn write_diag(msg: &[u8]) {
    // Use libc::write directly (fd=2 = stderr) to bypass Rust I/O initialization
    unsafe {
        libc::write(
            2,
            msg.as_ptr() as *const libc::c_void,
            msg.len() as libc::c_uint,
        )
    };
}

#[unsafe(no_mangle)]
pub extern "C" fn run_payload(ptr: *const u8, len: usize) -> u32 {
    let wasm_bytes = unsafe { std::slice::from_raw_parts(ptr, len) };
    match run_internal(wasm_bytes) {
        Ok(_) => 0,
        Err(e) => {
            let msg = format!("Error in washmhost: {:?}\n", e);
            write_diag(msg.as_bytes());
            1
        }
    }
}

fn run_internal(wasm_bytes: &[u8]) -> anyhow::Result<()> {
    let mut config = Config::new();
    config.signals_based_traps(false);
    config.static_memory_maximum_size(0);
    config.dynamic_memory_guard_size(0);
    let engine = Engine::new(&config)?;
    let module = Module::new(&engine, &wasm_bytes)?;

    let host_state = HostState::new()?;
    let mut store = Store::new(&engine, host_state);

    let mut linker: Linker<HostState> = Linker::new(&engine);
    env_impl::register(&mut linker)?;
    let instance = linker.instantiate(&mut store, &module)?;

    let memory = instance
        .get_memory(&mut store, "memory")
        .ok_or_else(|| anyhow::anyhow!("WASM module exports no 'memory'"))?;

    let run = instance.get_typed_func::<(), ()>(&mut store, "run")?;
    let is_done = instance.get_typed_func::<(), u32>(&mut store, "is_done")?;

    loop {
        if let Err(e) = run.call(&mut store, ()) {
            let msg = format!("[washmhost] run error: {:?}\n", e);
            write_diag(msg.as_bytes());
            break;
        }

        if is_done.call(&mut store, ()).unwrap_or_else(|_| 0) == 1 {
            break;
        }

        let had_event = env_impl::poll_completions(&mut store, &memory, 0).unwrap_or(false);

        if !had_event && !store.data().epoll.pending.is_empty() {
            env_impl::poll_completions(&mut store, &memory, 50).unwrap_or(false);
        }
    }

    Ok(())
}
