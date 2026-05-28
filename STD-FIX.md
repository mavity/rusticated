# Rust std/sysroot Replacement

## Goal

Consumer crates (`demo`, `brot`, `mohabbat`, `washmhost`) should be able to write normal
Rust without `#![no_std]`, `#![no_main]`, `extern crate alloc`, and without explicit
`use alloc::...` imports. The prelude (`String`, `Vec`, `format!`, etc.) must be in 
scope automatically, exactly as it is with the real standard library.

## The Architecture (New Approach)

Attempting to seamlessly build a custom standard library and a consumer crate in 
the same `cargo build` command runs into fundamental limitations with Cargo's design. 
Cargo resolves its dependency graph and loads `.cargo/config.toml` configurations 
*before* any builds execute. A single-step process creates unavoidable Catch-22s:
waiting on config files that are not yet generated, or deadlocking against directory locks.

Thus, the reliable architecture separates the sysroot compilation from the consumer compilation 
into two distinct steps:

1. **The Sysroot Build:** An explicit script/command builds `rusticated` as a standalone `sysroot` 
   and produces a generated config for inclusion (e.g., in `target/rusticated-spec/`) containing the `rustflags`.
2. **The Consumer Build:** A standard `cargo build -p demo` executes, reliably picking up the 
   pre-generated configuration and linking perfectly against the prebuilt `libstd.rlib`.

---

## The Build Plan

### 1. Simplify the Crate Structure
Abandon the division between the `rusticated` wrapper shim and the `sysroot` crate. 
Since `rusticated` is no longer acting as a dummy synchronization barrier in Cargo's 
dependency graph, it can just be the sysroot directly. `rusticated` should be a `#![no_std]` 
library crate containing the allocator, I/O, async runtime, etc.

### 2. Target Naming Convention

Each custom target spec is named `<arch>-<os>-rusticated`, where `<arch>` and `<os>` come
directly from the host or cross-compilation target. This encodes both architecture and OS
in the filename so all specs can coexist in `target/rusticated-spec/` simultaneously.

The full target matrix:

| Target JSON spec                    | `llvm-target`                    | `os`      | Primary use          |
|-------------------------------------|----------------------------------|-----------|----------------------|
| `x86_64-windows-rusticated.json`    | `x86_64-pc-windows-msvc`         | `windows` | Native Windows dev   |
| `x86_64-linux-rusticated.json`      | `x86_64-unknown-linux-gnu`       | `linux`   | Native Linux dev     |
| `aarch64-windows-rusticated.json`   | `aarch64-pc-windows-msvc`        | `windows` | ARM Windows          |
| `aarch64-linux-rusticated.json`     | `aarch64-unknown-linux-gnu`      | `linux`   | ARM Linux / servers  |
| `wasm32-rusticated.json`            | `wasm32-unknown-unknown`         | `none`    | WebAssembly          |

Note: there is no separate `gnu` vs `musl` split. `rusticated` uses direct syscalls and
bypasses libc entirely — the gnu/musl distinction (which C library to link) is irrelevant
for a no_std crate that makes raw kernel calls. A single Linux spec per architecture covers both.

All specs share the same `rusticated` source code. Platform-specific behaviour is gated with
ordinary `#[cfg(target_os = "...")]` / `#[cfg(target_arch = "...")]` attributes; rustc derives
those cfg values from the `"os"` and `"arch"` fields in the JSON spec.

### 3. The Sysroot Generation Command
Create a Rust `prebuild` binary (just a binary that can be invoked by cargo) that handles generating the sysroot. This replaces the problematic `build.rs` hacks:

- Determines the host triple via `rustc -vV` and derives the default rusticated target
  (e.g., `x86_64-pc-windows-msvc` → `x86_64-windows-rusticated`).
- Generates the custom target JSON spec(s) into `target/rusticated-spec/`, embedding
  the correct `"os"`, `"arch"`, and `"llvm-target"` for each requested target.
- Compiles `rusticated` for each target via
  `cargo build -Z build-std=core,alloc,compiler_builtins --target <spec.json>`.
- Reads the exact output rlib path (including the cargo-generated hash) from the build
  output and records it directly — no copying or renaming.
- Writes `target/rusticated-spec/config.toml` containing:
  - `[build] target = "target/rusticated-spec/<host-derived-spec>.json"` (the default)
  - A `[target.<name>]` section per built target, each with its own `rustflags`
    pointing to the exact hashed rlib path produced for that target.

### 4. The Consumer Crates (No Special Hacks)
The workspace `.cargo/config.toml` points to the pre-generated config file:
`include = ["../target/rusticated-spec/config.toml"]`

Consumer crates simply build natively. Because the config exists before Cargo runs,
`cargo build -p demo` picks up the host-default target and the correct `--extern std=...`
flag automatically. Cross-compiling to a different target is explicit:

```
cargo build -p demo --target target/rusticated-spec/x86_64-linux-rusticated.json
cargo build -p demo --target target/rusticated-spec/aarch64-linux-rusticated.json
cargo build -p demo --target target/rusticated-spec/wasm32-rusticated.json
```

Each of those targets has its own pre-built rlib entry in the generated config, so the
correct `--extern std=...` path is selected per target automatically.

### 5. Trade-off: Manual Re-invocations
Because the consumer crate no longer falsely lists the custom standard library in its 
`[dependencies]`, Cargo will not track the sysroot source files for changes. 
If any code inside the custom `sysroot` is modified, the developer must manually 
invoke the sysroot generation command (Step 2) again before running `cargo build` 
on consumer crates.

---

## Checklist for Implementation

1. **Un-split the Crates:** Remove `sysroot` as a crate. The actual files are already inside `src` so they fall back into `rusticated` on their own. Remove the proxy dependencies and ensure project is properly configured as a single crate again.
2. **Remove the Graph Hacks:** Remove `std = { package = "rusticated" }` and `build.rs` dependencies that were attempting to trick Cargo.
3. **Write the Build Script:** Make an `prebuild` binary that generates the included `.cargo/config.toml` inside `target/` and builds `rusticated` as a standalone binary step.
4. **Test Clean Builds:** Delete `target/` and ensure that running cargo prebuild followed by `cargo build -p demo` successfully completes without hangs or missing file errors.
5. **Prelude Injection:** Ensure `#[prelude_import]` is attached to the module in `rusticated` where the standard prelude items are defined. Test it by compiling `demo` using bare `String`, `Vec`, and macros without `#![no_std]` or explicit `use` paths. Then strip out `#![no_std]` from things like `washmhost` entirely.