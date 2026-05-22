# STD-REAL-FIX: Making `rusticated` a Proper Sysroot `std`

## Goal

Users of `rusticated` should be able to write normal-looking Rust with no
boilerplate preamble:

```rust
use std::tty::{stdin, stdout};

std::spawn!(async_main());

async fn async_main() {
    let s = String::from("hello"); // String, Vec etc. just work — no imports needed
    // ...
}
```

Specifically: no `#![no_std]`, no `#![no_main]`, no `extern crate alloc`, no
explicit `use alloc::...` imports. The async entrypoint pattern via `std::spawn!`
stays — `async fn main()` is not a goal.

## How It Works

Rustc loads `String`, `Vec`, prelude macros, and lang items from the **sysroot**
— a directory of pre-compiled `.rlib` files. If we place `rusticated`'s compiled
`.rlib` there (named as `libstd`), the compiler finds it first and the prelude
injection happens automatically.

No Rust source from `rust-src` is needed for `rusticated` itself. `core` and
`alloc` still come from the nightly sysroot (or from `-Z build-std` for custom
targets without prebuilts, as the demo already does).

---

## Outstanding Work

### 1. Add `#[lang = "start"]` to `src/rt/mod.rs`

Without this, the linker errors when `#![no_main]` is absent because the compiler
emits a call to `lang_start` that has no definition. Implementing it allows
downstream crates to drop `#![no_main]` and write an ordinary `fn main()` that
calls `std::spawn!` internally. The `spawn!` macro is unchanged — this just
removes the requirement to suppress the default entrypoint.

```rust
#[lang = "start"]
fn lang_start<T: Termination + 'static>(
    main: fn() -> T,
    _argc: isize,
    _argv: *const *const u8,
    _sigpipe: u8,
) -> isize {
    main().report() as isize
}
```

WASM continues to use the `guest_init` export path via `spawn!` unchanged.
Non-WASM users either call `std::spawn!` from their `fn main()`, or omit
`#![no_main]` and let `lang_start` call their plain `fn main()`.

### 2. Add `#[alloc_error_handler]` to `src/lib.rs`

Required when `#[global_allocator]` is in use and the allocator fails.

```rust
#[alloc_error_handler]
fn oom(_: core::alloc::Layout) -> ! {
    // abort or panic
}
```

### 3. Complete `prelude::v1` in `src/lib.rs`

Current prelude is missing macros that the real `std` prelude injects:

- `format!` (currently commented out)
- `assert!`, `assert_eq!`, `assert_ne!`
- `panic!`, `todo!`, `unimplemented!`, `unreachable!`
- `dbg!`, `print!`, `println!`, `eprint!`, `eprintln!`
- `write!`, `writeln!`

These all exist in the crate already as `#[macro_export]` items — they just need
to be included in `prelude::v1`.

### 4. Enable `lang_items` feature in `src/lib.rs`

```rust
#![feature(lang_items)]
```

---

## Sysroot Build Tooling

An xtask (or shell script) that:

1. **Builds `rusticated`** for the target:
   ```
   cargo build -p rusticated --target <target> --message-format=json
   ```
   Parse JSON output to find the exact `.rlib` path and filename hash.

2. **Builds `core` + `alloc`** via `-Z build-std` (already done in demo's
   `.cargo/config.toml` — reuse that output):
   ```
   cargo build -Z build-std=core,alloc --target <target> --message-format=json
   ```

3. **Assembles the sysroot directory**:
   ```
   custom-sysroot/
     lib/rustlib/<target>/lib/
       libstd-<hash>.rlib       ← rusticated's .rlib, renamed
       libcore-<hash>.rlib      ← from build-std output
       liballoc-<hash>.rlib     ← from build-std output
       libcompiler_builtins-<hash>.rlib
   ```

4. **Builds the demo** pointing at the custom sysroot:
   ```
   RUSTFLAGS="--sysroot ./custom-sysroot" cargo build -p rusticated-demo
   ```

The xtask lives in `xtask/src/main.rs` and is invoked via `cargo xtask build-demo`.

---

## Demo Changes (after the above)

Once the sysroot works, `demo/src/main.rs` simplifies from:

```rust
#![no_std]
#![no_main]

extern crate alloc;

use alloc::format;
use alloc::string::String;
use alloc::vec::Vec;
use std::tty::{stdin, stdout};

std::spawn!(async_main());

async fn async_main() { ... }
```

To:

```rust
use std::tty::{stdin, stdout};

std::spawn!(async_main());

async fn async_main() { ... }
```

The async runtime pattern is unchanged. What drops is the `no_std`/`no_main`
attributes, the `extern crate alloc`, and all `use alloc::...` imports.

---

## Order of Implementation

1. `#[alloc_error_handler]` — 3 lines, no risk
2. Complete `prelude::v1` macros — low risk
3. `#[lang = "start"]` + `#![feature(lang_items)]` — medium, ties into executor
4. Xtask sysroot builder — the integration glue
5. Update demo to drop `no_std` boilerplate and `spawn!`
