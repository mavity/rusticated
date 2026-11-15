//! Wasmtime (`min-platform` + Cranelift JIT) module compiler and re-entry driver.

use crate::abi::{abi, globals};
use alloc::boxed::Box;
use wasmtime::{Config, Engine, Linker, Module, OptLevel, Store, TypedFunc};

const MAX_GUEST_LINEAR_BYTES: u64 = 64 * 1024 * 1024;

pub struct WasmInstanceState {
    store: Store<()>,
    run: TypedFunc<(), ()>,
}

pub fn build_engine() -> wasmtime::Result<Engine> {
    let mut config = Config::new();
    config.signals_based_traps(false);
    config.memory_guard_size(0);
    config.memory_reservation(MAX_GUEST_LINEAR_BYTES);
    config.memory_may_move(true);
    config.cranelift_opt_level(OptLevel::None);
    Engine::new(&config)
}

unsafe fn host_abort(msg: &[u8]) -> ! {
    (abi().host_panic)(msg.as_ptr(), msg.len());
}

pub unsafe fn compile_and_initialize_wasm() {
    let engine = match build_engine() {
        Ok(e) => e,
        Err(_) => host_abort(b"wasmtime Engine::new failed"),
    };

    let abi_tbl = abi();
    if abi_tbl.wasm_bytes.is_null() || abi_tbl.wasm_len == 0 {
        host_abort(b"missing wasm_bytes in PalAbiTable");
    }
    let wasm_bytes = core::slice::from_raw_parts(abi_tbl.wasm_bytes, abi_tbl.wasm_len);

    let module = match Module::new(&engine, wasm_bytes) {
        Ok(m) => m,
        Err(err) => {
            let msg = alloc::format!("wasmtime Module::new failed: {}", err);
            host_abort(msg.as_bytes());
        }
    };

    let mut store = Store::new(&engine, ());
    let mut linker = Linker::new(&engine);
    if let Err(err) = crate::wasm_env::register_env_imports(&mut linker) {
        let msg = alloc::format!("wasmtime Linker registration failed: {}", err);
        host_abort(msg.as_bytes());
    }

    let instance = match linker.instantiate(&mut store, &module) {
        Ok(i) => i,
        Err(err) => {
            let msg = alloc::format!("wasmtime Linker::instantiate failed: {}", err);
            host_abort(msg.as_bytes());
        }
    };

    if let Some(mem) = instance.get_memory(&mut store, "memory") {
        let g = globals();
        g.guest_mem_base = mem.data_ptr(&store);
        g.guest_mem_len = mem.data_size(&store);
    }

    let run = match instance
        .get_typed_func::<(), ()>(&mut store, "run")
        .or_else(|_| instance.get_typed_func::<(), ()>(&mut store, "_initialize"))
        .or_else(|_| instance.get_typed_func::<(), ()>(&mut store, "_start"))
    {
        Ok(f) => f,
        Err(_) => host_abort(b"wasm module missing export 'run' or '_start'"),
    };

    let state = Box::new(WasmInstanceState { store, run });
    globals().wasm_instance_state = Box::into_raw(state).cast::<u8>();
}

pub unsafe fn reenter_wasm() {
    let ptr = globals().wasm_instance_state;
    if ptr.is_null() {
        return;
    }
    let state = &mut *ptr.cast::<WasmInstanceState>();
    if let Err(err) = state.run.call(&mut state.store, ()) {
        let trap_name = match err.downcast_ref::<wasmtime::Trap>() {
            Some(wasmtime::Trap::StackOverflow) => "StackOverflow",
            Some(wasmtime::Trap::MemoryOutOfBounds) => "MemoryOutOfBounds",
            Some(wasmtime::Trap::HeapMisaligned) => "HeapMisaligned",
            Some(wasmtime::Trap::TableOutOfBounds) => "TableOutOfBounds",
            Some(wasmtime::Trap::IndirectCallToNull) => "IndirectCallToNull",
            Some(wasmtime::Trap::BadSignature) => "BadSignature",
            Some(wasmtime::Trap::IntegerOverflow) => "IntegerOverflow",
            Some(wasmtime::Trap::IntegerDivisionByZero) => "IntegerDivisionByZero",
            Some(wasmtime::Trap::BadConversionToInteger) => "BadConversionToInteger",
            Some(wasmtime::Trap::UnreachableCodeReached) => "UnreachableCodeReached",
            Some(_) => "OtherTrap",
            None => "NonTrapError",
        };
        let msg = alloc::format!("wasm trap ({}) during run(): {}", trap_name, err);
        host_abort(msg.as_bytes());
    }
}
