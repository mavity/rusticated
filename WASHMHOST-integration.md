# WASHMHOST Integration: Wasmtime on Rusticated

## Vision

`washmhost` is the bridge between two worlds:

- **Below** — a strictly `no_std` Wasmtime runtime, stripped of all assumptions
  about host I/O, blocking syscalls, threads, and OS memory.
- **Above** — the rusticated custom `std`, with its async I/O, executor, and
  collections, running on rusticated target triples.

`washmhost` itself is built **with full `std`** (the rusticated sysroot std).
Wasmtime is pulled in as a `no_std` dependency, providing only the pure WASM
state machine. `washmhost` supplies the platform glue Wasmtime needs, and in
return exposes rusticated's async capabilities into the WASM sandbox.

## Mandate

### 1. Wasmtime stays `no_std`

In `washmhost/Cargo.toml`:

```toml
wasmtime = { version = "26", default-features = false, features = ["runtime", "pulley"] }
```

- No Cranelift, no JIT, no host signal handling, no native virtual memory
  assumptions baked into the crate.
- This eliminates Wasmtime's reliance on blocking I/O and traditional OS
  primitives — exactly what rusticated needs.
- Pulley is the interpreter backend; AOT-compiled artifacts are the input.

### 2. `washmhost` uses rusticated `std`

- Drop `#![no_std]` from `washmhost/src/lib.rs` and the other modules.
- Because `washmhost` is compiled for rusticated targets, plain `use std::...`
  resolves to the rusticated sysroot std automatically.
- This gives `washmhost` direct access to async I/O, the custom executor,
  custom collections, and every other rusticated-specific behavior — without
  any of it leaking into Wasmtime's internals.

### 3. AOT pipeline replaces JIT

- `Module::new(&engine, wasm_bytes)` is not available in `no_std` Wasmtime
  (it requires Cranelift). Use `Module::deserialize(&engine, cwasm_bytes)`
  instead.
- A separate offline step (likely an extension of `prebuild`, run with a
  standard host toolchain where Cranelift is available) compiles `.wasm` →
  `.cwasm`.
- `washmhost` only ever loads pre-serialized `.cwasm` artifacts.

### 4. `washmhost` provides the Wasmtime platform C API

The `no_std` Wasmtime expects the embedder to supply the symbols described in
`wasmtime-platform.h` (from the Wasmtime release artifacts). `washmhost`
implements them as `#[unsafe(no_mangle)] pub extern "C"` functions backed by
rusticated's primitives:

- Virtual memory: `wasmtime_mmap_new`, `wasmtime_mmap_remap`,
  `wasmtime_munmap`, `wasmtime_mprotect`, `wasmtime_page_size`.
- Trap / unwinding: `wasmtime_setjmp`, `wasmtime_longjmp`,
  `wasmtime_init_traps`.
- Memory images: `wasmtime_memory_image_new`,
  `wasmtime_memory_image_map_at`, `wasmtime_memory_image_free`.
- TLS shim: `wasmtime_tls_get`, `wasmtime_tls_set`.

Some of these are gated behind the off-by-default Wasmtime features
`custom-virtual-memory` and `custom-native-signals`. We enable them only if
and when we actually want Wasmtime to use virtual memory / signal-based traps;
otherwise Pulley runs against fixed linear memory and the surface stays
minimal.

### 5. Bridging async I/O into WASM

- All host-exposed functions in `washmhost/src/env_impl.rs` are implemented
  using rusticated's async I/O primitives.
- The polling loop (`run` / `is_done` / `poll_completions` in
  `washmhost/src/lib.rs`) drives the rusticated executor between WASM
  invocations, so guest code sees a non-blocking, event-driven host.
- Wasmtime itself never sees an `std::io::Read` or a blocking syscall — it
  only sees the WASM-level imports `washmhost` exports.

## Architectural Summary

```
+----------------------------------------------------------+
|  mohabbat (guest .wasm, AOT-compiled offline to .cwasm)  |
+----------------------------------------------------------+
              ^                            |
              | WASM imports               | run / is_done
              | (rusticated async I/O)     v
+----------------------------------------------------------+
|  washmhost  —  built WITH rusticated std                 |
|    - env_impl.rs: async-backed host functions            |
|    - platform C API: wasmtime_mmap_*, setjmp/longjmp,    |
|      tls_get/set, memory_image_*                         |
|    - polling loop driving rusticated executor            |
+----------------------------------------------------------+
              ^                            |
              | host calls                 | extern "C" platform symbols
              v                            |
+----------------------------------------------------------+
|  wasmtime  —  built WITHOUT std (no_std, no Cranelift)   |
|    features = ["runtime", "pulley"]                      |
|    Pulley interpreter, deserialize-only module loading   |
+----------------------------------------------------------+
              ^
              | rusticated sysroot std (custom target)
              v
+----------------------------------------------------------+
|  rusticated std / sysroot                                |
|    async I/O, custom executor, custom collections        |
+----------------------------------------------------------+
```

## Non-Goals

- No JIT compilation inside `washmhost`. Ever.
- No re-introduction of blocking I/O paths to satisfy Wasmtime.
- No use of Wasmtime's default features (cache, profiling, async-trait,
  Cranelift, debug builtins, etc.) unless explicitly justified.
- No `#![no_std]` in `washmhost` — it deliberately lives in the `std` world
  so it can leverage rusticated's full surface.

## Current State

- `washmhost/Cargo.toml` already requests `wasmtime` with
  `default-features = false, features = ["runtime", "pulley"]`. Good.
- `washmhost/src/lib.rs` still declares `#![no_std]` and still calls
  `Module::new(...)`. Both need to change per this mandate.
- The platform C API symbols are not yet implemented; the link-time contract
  with `no_std` Wasmtime is not yet satisfied.
- No AOT compilation step exists yet for producing `.cwasm` from guest
  `.wasm` artifacts.

## Next Steps (sequenced, not yet executed)

1. Remove `#![no_std]` from `washmhost`; verify it builds against the
   rusticated sysroot std as a plain `std` crate.
2. Switch module loading to `Module::deserialize`.
3. Add an offline AOT step (extend `prebuild` or add a host-side helper) that
   produces `.cwasm` from `mohabbat`'s `.wasm` output.
4. Implement the required `wasmtime_*` platform C API symbols in `washmhost`,
   backed by rusticated primitives. Decide whether to enable
   `custom-virtual-memory` / `custom-native-signals` or keep Pulley on a
   fixed linear memory.
5. Wire `env_impl.rs` host functions to rusticated's async I/O end-to-end and
   confirm the polling loop drives the executor as intended.
