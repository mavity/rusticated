#![no_main]

use wasmtime::{Config, Engine, Linker, Module, Store};

mod env_impl;
mod handles;
use handles::HostState;

#[cfg(target_arch = "x86_64")]
#[no_mangle]
pub unsafe extern "C" fn _start(payload_ptr: *const u8, payload_len: usize) -> u32 {
    let payload = core::slice::from_raw_parts(payload_ptr, payload_len);
    match run(payload) {
        Ok(_) => 0,
        Err(_) => 1, // Add proper error handling
    }
}

// Windows entry point
#[cfg(target_arch = "x86_64")]
#[no_mangle]
pub unsafe extern "C" fn mainCRTStartup(payload_ptr: *const u8, payload_len: usize) -> u32 {
    _start(payload_ptr, payload_len)
}

fn run(wasm_bytes: &[u8]) -> anyhow::Result<()> {
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
        if let Err(_e) = run.call(&mut store, ()) {
            break;
        }

        if is_done.call(&mut store, ()).unwrap_or(0) == 1 {
            break;
        }

        // Drive host-side completions (timers, stdin, child processes) into
        // WASM memory so the next tick can observe them.
        let _ = env_impl::poll_completions(&mut store, &memory, 0);
    }

    Ok(())
}
