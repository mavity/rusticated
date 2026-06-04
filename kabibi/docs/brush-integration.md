# Brush integration plan

The goal is to bring brush into `kabibi` in a clean, staged way.
We will use `rusticated` as the async runtime and build the integration in three stages.
The eventual structure is:

- `/kabibi`
  - `/suklay`  ← a brush submodule inside kabibi

## STRICT MANDATE

Rusticated platform is strictly asynchronous and forbids any form of blocking io or blocking threading:
- Synchronous Read/Write traits, or synchronous read/write methods.
- Busy polling where an infinite synchronous loop polls a future until it completes.
- Methods like block_on are explicitly prohibited.
- Synchronous thread sleep is prohibited.
- Tokio is prohibited.
- DO NOT retain reedline backend.

Any methods where prohibited blocking currently is enacted in upstream brush logic, these need to be converted to async methods and convert to use corresponding async primitives.

- Synchronous Read/Write should be converted to AsyncRead/AsyncWrite.
- Synchronous busy polling converted to await on the corresponding future.
- Synchronous thread sleep converted to await on asynchronous sleep.
- Any async may necessarily require proliferation through traits converted to async, callers made async and so on.

An apt approach to handling top-level async bubbling all the way to the initial entry point can be observed in `demo` and `demo/loch`.

## Stage 1: Migrate brush onto rusticated with a simple backend

This stage is about making brush run on `rusticated` and nothing more.
We do not yet try to render shell content inside the kabibi UI.
The shell should run on a plain black terminal using a simple backend.

What to do:
- Add the brush crates to the workspace or make them path dependencies inside kabibi.
- Remove Tokio runtime setup from `brush-shell`.
- Replace `tokio::runtime::Builder`, `runtime.block_on(...)`, and `std::process::exit` control flow.
- Make `brush-core` use rusticated async primitives instead of `tokio::process`, `tokio::net`, and tokio signal helpers.
- Replace the terminal and stdin backend assumptions in `brush-interactive` with a simple basic backend that can run with rusticated I/O.
- Default `brush-shell` to the minimal stdin backend on rusticated builds.
- Keep the shell output as plain text on a terminal screen.
- Do not attempt ratatui rendering or kabibi widget integration yet.
- Do not attempt to make rusticated adopt blocking or synchronous APIs: adjust brush to be async instead.

Success criteria for stage 1:
- brush can build and run using the rusticated async runtime: `cargo run -p brush-shell --config sysroot.toml`
- brush can accept input and print output on a black terminal.
- the shell works with a simple basic backend and no advanced terminal features.

### Planned steps
- Audit `brush-shell/src/entry.rs`, `brush-interactive/src/*`, and `brush-core/src/sys/*` to locate Tokio dependencies and blocking I/O patterns.
  - `brush-shell/Cargo.toml` depends on `tokio` for native builds, and `src/entry.rs` uses `tokio::runtime::Builder`, `runtime.block_on(...)`, and `tokio::sync::Mutex`.
  - `brush-core/Cargo.toml` depends on `tokio` for unix/windows and exposes `tokio_process`, `tokio::signal`, `tokio::task::spawn`, `tokio::select!`, and `tokio::task::spawn_blocking` across `commands.rs`, `processes.rs`, `jobs.rs`, `shell.rs`, `interp.rs`, `sys/tokio_process.rs`, `sys/unix/async_pipe.rs`, and `sys/unix/signal.rs`.
  - `brush-interactive/Cargo.toml` depends on `tokio`; `basic/input_backend.rs` uses `tokio::task::block_in_place` and `Handle::current().block_on(...)`; `completion.rs` uses `tokio::select!` and `tokio::signal::ctrl_c`; the `reedline` backend has extensive `tokio::task::block_in_place` and `tokio::sync::Mutex` usage.
- Add a rusticated-only feature set or target profile for `brush-shell` / `brush-core` that excludes Tokio and Tokio-based backends.
- Replace `brush-shell` entrypoint runtime with rusticated `std::main!` / `std::spawn!`, removing `tokio::runtime::Builder`, `runtime.block_on(...)`, and Tokio task management.
- Replace `brush-core` platform adapters from `tokio_process` / `tokio::signal` to rusticated `std::process` / `std::signal` equivalents.
- Implement a minimal rusticated stdin/tty input backend in `brush-interactive` that uses `std::tty` + `std::io::AsyncRead` and avoids `block_in_place`.
- Configure the plain basic backend as the default for rusticated builds and disable advanced/async-Tokio backends like `reedline`.
- Build `brush-shell` with `--config sysroot.toml` and verify a plain terminal session starts, accepts a line, and prints output.
- Iterate on any shell command execution or process handling failures until the shell is usable on native rusticated runtime.

## Followup aspects

1. Ensure rusticated has `std::any` support.
  - Simple re-export: `pub mod any { pub use core::any::*; }`
2. Re-adopt `OsString` / `OsStr` into rusticated shell internals completely.
  - Expose it for this custom std in conventional manner.
3. Audit `clap_lex` and decide whether to replace it or isolate its use of `OsStr` behind a dedicated shim instead of exposing `OsStr` globally.
4. Keep rusticated shell runtime single-threaded for now. Any code depending on threading needs to be converted to async.
5. Fix the rusticated `brush-shell` entrypoint to use the normal rusticated async entry pattern:
  - `std::main!` / `std::spawn!`
  - avoid explicit `std::process::exit(...)` inside the spawned async task.
6. Build and verify stage 1 before moving on:
  - `cargo run -p brush-shell --config sysroot.toml`
  - shell starts on a plain terminal, accepts input, and prints output.




## Stage 2: Add raw tty support using rusticated terminal APIs

This stage makes the shell interactive using the raw tty features provided by rusticated.
We keep brush as a shell process, but the terminal becomes more like a real interactive shell.

What to do:
- Use rusticated raw tty APIs in `kabibi` and in the brush backend.
- Add support for terminal resize events, raw input mode, and direct tty write.
- Keep the brush input path simple and deterministic.
- Avoid using the existing `reedline` terminal backend if it depends on Tokio or blocking std I/O.
- Keep the UI still separate from kabibi rendering.

Success criteria for stage 2:
- brush can run in raw tty mode on rusticated.
- it can react to resize events and user keys in real time.
- the shell still uses a simple backend, but now with real terminal I/O.

## Stage 3: Custom brush backend inside kabibi with ratatui rendering

This is the final stage.
brush gets a custom backend that renders into kabibi's `ratatui` widgets, and receives input from kabibi's input stream.

What to do:
- Implement a custom brush input backend for kabibi.
- Push keypresses from kabibi into brush instead of letting brush read stdin directly.
- Capture brush output and render it through kabibi's `ratatui` layout.
- Keep shell state and application state separate, but connected.
- Make `/suklay` the brush submodule that lives inside `/kabibi` and provides the shell logic.
- Ensure kabibi can show file panels, prompt, AI chat, and shell output together.

Success criteria for stage 3:
- brush is embedded in kabibi as a shell widget.
- brush input is filtered through kabibi’s key event loop.
- brush output appears inside the kabibi UI, not as raw terminal text.

## Important aspects to keep in mind

- The first stage is mandatory. No excuses. We must get brush running on rusticated first.
- Keep the backend simple at first.
- Do not mix Tokio or blocking std I/O into the runtime layer.
- The path is: migrate brush to rusticated → add raw tty support → embed brush into kabibi.
- Keep `kabibi` and `suklay` clearly separated until stage 3.
- Use plain terminal behavior in stage 1, raw tty in stage 2, and ratatui rendering in stage 3.
- Test at every stage with a minimal shell session before moving on.

## Practical next steps

1. Audit `brush-shell/src/entry.rs`, `brush-interactive/src/*`, and `brush-core/src/sys/*`.
2. Replace Tokio runtime and async primitives with rusticated equivalents.
3. Build a minimal shell binary on black terminal and verify it runs.
4. Add raw tty support for resize and raw mode.
5. Implement the custom kabibi backend and renderer.

This plan keeps the work manageable and avoids trying to solve the full UI integration too early.
