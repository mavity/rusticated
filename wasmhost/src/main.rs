mod env_impl;
mod handles;

use handles::HostState;
use wasmtime::{Engine, Linker, Module, Store};

fn main() -> anyhow::Result<()> {
    let wasm_path = std::env::args()
        .nth(1)
        .ok_or_else(|| anyhow::anyhow!("Usage: rusticated-wasmtime <path.wasm>"))?;

    let engine = Engine::default();
    let module = Module::from_file(&engine, &wasm_path)?;

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
        // Tick the guest runtime: wake completed ops, poll the main future.
        if let Err(e) = run.call(&mut store, ()) {
            eprintln!("Error calling run: {:?}", e);
            break;
        }

        if is_done.call(&mut store, ()).unwrap_or_else(|e| {
            eprintln!("Error calling is_done: {:?}", e);
            0
        }) == 1
        {
            break;
        }

        // Non-blocking pass first to pick up any immediately-ready events.
        let had_event = env_impl::poll_completions(&mut store, &memory, 0).unwrap_or_else(|e| {
            eprintln!("Error calling poll_completions 0: {:?}", e);
            false
        });

        if !had_event && !store.data().epoll.pending.is_empty() {
            // No immediate events but async ops are in flight — block briefly
            // so we do not busy-spin while waiting for stdin / pipe I/O.
            env_impl::poll_completions(&mut store, &memory, 50).unwrap_or_else(|e| {
                eprintln!("Error calling poll_completions 50: {:?}", e);
                false
            });
        }
    }

    Ok(())
}
