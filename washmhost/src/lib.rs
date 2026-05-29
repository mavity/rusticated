mod env_impl;
mod handles;

use std::prelude::rust_2024::*;

use handles::HostState;
use wasmtime::{Config, Engine, Linker, Module, Store};

// MinGW startup code (crtexewin.o) expects WinMain when linked as a Windows subsystem binary.
// Provide a stub so the cdylib links without errors on Windows GNU targets.
#[cfg(all(target_os = "windows", target_env = "gnu"))]
#[unsafe(no_mangle)]
extern "system" fn WinMain(
    _: *mut core::ffi::c_void,
    _: *mut core::ffi::c_void,
    _: *mut core::ffi::c_void,
    _: i32,
) -> i32 {
    0
}

#[unsafe(no_mangle)]
pub extern "C" fn run_payload(ptr: *const u8, len: usize) -> u32 {
    let wasm_bytes = unsafe { std::slice::from_raw_parts(ptr, len) };
    match run_internal(wasm_bytes) {
        Ok(_) => 0,
        Err(e) => {
            eprintln!("Error in washmhost: {:?}", e);
            1
        }
    }
}

fn run_internal(wasm_bytes: &[u8]) -> anyhow::Result<()> {
    let mut config = Config::new();
    config.signals_based_traps(false);
    config.wasm_backtrace_details(wasmtime::WasmBacktraceDetails::Enable);
    let engine = Engine::new(&config).map_err(|e| anyhow::anyhow!("{:?}", e))?;
    let module = unsafe { Module::deserialize(&engine, &wasm_bytes).map_err(|e| anyhow::anyhow!("{:?}", e))? };

    let host_state = HostState::new();
    let mut store = Store::new(&engine, host_state);

    let mut linker: Linker<HostState> = Linker::new(&engine);
    env_impl::register(&mut linker).map_err(|e| anyhow::anyhow!("{:?}", e))?;
    let instance = linker.instantiate(&mut store, &module).map_err(|e| anyhow::anyhow!("{:?}", e))?;

    let memory = instance
        .get_memory(&mut store, "memory")
        .ok_or_else(|| anyhow::anyhow!("WASM module exports no 'memory'"))?;

    let run = instance.get_typed_func::<(), ()>(&mut store, "run").map_err(|e| anyhow::anyhow!("{:?}", e))?;
    let is_done = instance.get_typed_func::<(), u32>(&mut store, "is_done").map_err(|e| anyhow::anyhow!("{:?}", e))?;

    loop {
        if let Err(e) = run.call(&mut store, ()) {
            eprintln!("[washmhost] run error: {:?}", e);
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

#[unsafe(no_mangle)]
pub extern "C" fn wasmtime_tls_get() -> *mut u8 { std::ptr::null_mut() }
#[unsafe(no_mangle)]
pub extern "C" fn wasmtime_tls_set(_ptr: *mut u8) {}
#[unsafe(no_mangle)]
pub extern "C" fn main() -> i32 { 0 }

