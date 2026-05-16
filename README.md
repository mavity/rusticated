# FAST-STD

This is a custom target crate meant to handle extremely efficiency-consciouis and resource-frugal implementation of Rust std for embedded and performance-sensitive environments.

The plan is to build it for Linux, Windows, MacOS and WASM using strictly-async public APIs and enabling fully async implementations such as IOPC, io_uring and custom WASM host functions (as opposed to things like wasip1 for example).

The plan is intentionally meant to be highlighy frugal and not targeting wide compatibility with existing crates as is as its goal.

The crate is meant to be used for projects that achieve cross-platform ultimate performance.

So that is what it is: a frugal, completion-based async platform layer for Linux, macOS, Windows, and WASM.

## What it is

A minimal replacement for the async I/O portions of `std`, shaped like `std` but built on native completion APIs:

- **Linux**: io_uring
- **Windows**: IOCP
- **macOS**: kqueue
- **WASM**: custom host function imports (not WASIP1)

## What it provides

| Module | Content |
|--------|---------|
| `io` | `AsyncRead` / `AsyncWrite` — one method each, `Vec<u8>` ownership transfer, no extension traits |
| `fs` | `File`, `OpenOptions`, `DirReader`, `metadata` |
| `process` | `Command`, `Child`, `Stdio` |
| `signal` | `ctrl_c()`, `signal_wait()` |
| `tty` | Terminal size/mode (sync); `Tty` implementing `AsyncRead`/`AsyncWrite` |
| `time` | `sleep()` (async), `Instant::now()` (sync) |
| `env` | `get_args()`, `get_env()` (sync) |
| `path` | Sync path utilities |
| `rt` | Completion registry and proactor driver; minimal single-threaded executor on native |
| `abi` | `Overlapped` struct and WASM host import declarations |

## Design constraints

- **No `tokio`**. No `async-trait`. No vtable-based platform abstraction.
- **No `flush` / `shutdown`** on base I/O traits — those belong on concrete types.
- **No extension traits** in this crate — callers write their own loops.
- `AsyncRead::read` and `AsyncWrite::write` take and return `Vec<u8>` by value. The caller owns the buffer; the kernel borrows it for the duration of the operation.
- Sync for: env, path, time (Instant), terminal control. Async for: all I/O.
- `rt` on WASM is a self-contained proactor (`OverlappedFuture` + completion registry + `tick()`). On native it is a minimal single-threaded executor layered over `compio-driver` (io_uring/IOCP/kqueue) — no thread pinning, no cross-thread wakers.

## What it is not

Not a general async runtime. Not compatible with `tokio` traits. Not targeting broad ecosystem compatibility. Does not include networking, TLS, or any protocol-level code.

## Dependency strategy

The only external async dependency is `compio-driver` (raw OS completion queue bindings). All executor and scheduling logic is internal to `rt/`. `compio-buf` is used internally by the driver for kernel-pinned buffers but does not appear in the public API.
