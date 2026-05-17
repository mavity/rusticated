# FAST-STD

This is a custom target crate meant to handle extremely efficiency-consciouis and resource-frugal implementation of Rust std for embedded and performance-sensitive environments.

The plan is to build it for Linux, Windows, MacOS and WASM using strictly-async public APIs and enabling fully async implementations such as IOPC, io_uring and custom WASM host functions (as opposed to things like wasip1 for example).

The plan is intentionally meant to be highlighy frugal and not targeting wide compatibility with existing crates as is as its goal.

The crate is meant to be used for projects that achieve cross-platform ultimate performance.

So that is what it is: a frugal, completion-based async platform layer for Linux, macOS, Windows, and WASM.

## What it is

A minimal replacement for the async I/O portions of `std`, shaped like `std` but built on native completion APIs:

- **Linux**: io_uring (kernel 5.1+) with automatic epoll fallback for older kernels — selected at runtime, not at build time
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
- `rt` on WASM is a self-contained proactor (`OverlappedFuture` + completion registry + `tick()`). On native it is a minimal single-threaded executor with its own proactor — no thread pinning, no cross-thread wakers.

## What it is not

Not a general async runtime. Not compatible with `tokio` traits. Not targeting broad ecosystem compatibility. Does not include networking, TLS, or any protocol-level code.

## Dependency strategy

No external async or I/O dependencies. All OS bindings are `extern "C"` / `extern "system"` declarations against libraries the OS always provides:

- **Linux**: `syscall(2)` for io_uring (syscall numbers 425/426/427 are stable kernel ABI, identical on glibc and musl); standard libc for epoll, signalfd, timerfd, fork, execve. `#[repr(C)]` struct definitions for `io_uring_sqe`/`io_uring_cqe`/`epoll_event` inline — these are stable kernel ABI.
- **Windows**: `extern "system" #[link(name = "kernel32")]` for IOCP, file, and process APIs — always present, no install step.
- **macOS**: `extern "C"` against `libSystem.dylib` for kqueue, kevent, and POSIX calls — always linked.
- **WASM**: `extern "C" #[link(wasm_import_module)]` host imports already in `abi.rs`.

All executor and scheduling logic is self-contained in `rt/`. Logic derived from `compio-driver` source is ported directly, not imported as a crate dependency.


# Gaps


Based on a thorough review of the fast-std codebase, there are significant gaps. While the foundational loop and token registry are correctly modeled as a proactor (completion-based) system matching the WASM host logic, many actual OS-level I/O integrations are either completely stubbed out or relying on non-compliant fallbacks.

Here is an in-depth breakdown of the outstanding features and I/O implementations required to achieve parity across all platforms:

### 1. Underlying Runtime Backends (`src/rt/`)
The event loop drivers are the bridges between Rust's `Future` model and the OS.
* **`linux_uring.rs` (Missing)**: As discussed, the `io_uring` backend is entirely absent. `epoll` acts via readiness (telling you *when* to read), but io.rs expressly declares `AsyncRead`/`AsyncWrite` as an owned-buffer model (proactor). You need `io_uring` to natively pass buffer ownership to the kernel via `SQE` and reap them via `CQE`.
* **bsd.rs (Stubbed)**: macOS and FreeBSD currently use dead stubs. Requires `kqueue`/`kevent` integration mapping `EVFILT_READ` and `EVFILT_WRITE` to the token registry.
* **windows.rs (Overlapped OPs Missing)**: We just implemented the IOCP loop and handle registration, but we have not implemented the actual I/O operations (like `OverlappedBufferFuture` found in `wasm.rs`). You cannot do pure non-blocking file I/O on Windows via "readiness" polling; you *must* use `ReadFile`/`WriteFile` populated with an `OVERLAPPED` structure, which currently does not exist for Windows in this tree.

### 2. File System I/O (`src/fs.rs`)
The file system abstraction is exceptionally incomplete.
* **Windows, macOS, BSD**: **Completely missing.** `fs::File` uses a `native_stub` module that immediately returns `io::Error::other("fs::File not yet implemented on this platform")`. 
* **Linux**: **Fundamentally flawed (Blocking).** The Unix file abstraction currently drops down to raw `unsafe { read(self.fd, buf.as_mut_ptr(), ...) }` bypassing the async loop entirely. File I/O on Linux is notoriously unsuited for `epoll` (local disks always report "ready"). You must either bridge this to the missing `linux_uring.rs` or spawn a blocking threadpool.
* **General Missing Features**: Missing directories (`DirBuilder`, `read_dir`, `remove_dir_all`), metadata reading (`metadata`, `symlink_metadata`), and permission manipulations. 

### 3. Networking (`src/net.rs`)
* **Completely Missing**: There is no `src/net.rs` present in the library. A functioning standard library replacement fundamentally requires `TcpListener`, `TcpStream`, and `UdpSocket`. To support the `AsyncRead`/`AsyncWrite` ownership model, these need `WSARecv`/`WSASend` with IOCP on Windows, and `io_uring` or `epoll` + non-blocking socket loops on Linux/macOS.

### 4. Process Management (`src/process.rs`)
Child process tracking relies on blocking `wait()` methods unless special OS facilities are tapped.
* **Linux**: Functional. Seamlessly maps child PIDs to `pidfd_open` and yields back to `WaitReadable` on the `epoll` runtime.
* **Windows & macOS / BSD**: **Stubbed**. Awaiting a process will immediately return `"Child::wait: async backend pending on this platform"`.
  * *Windows Resolution*: You need to call `RegisterWaitForSingleObject` to push the Process Handle onto the system thread pool and signal your IOCP Queue when it terminates.
  * *macOS Resolution*: Must be bridged to `kqueue` using the `EVFILT_PROC` filter.

### 5. Signal Handling (`src/signal.rs`)
* **Windows**: **Stubbed.** Calling `ctrl_c()` returns an error: `"ctrl_c: Windows console-event backend pending"`. Resolution requires invoking `SetConsoleCtrlHandler` natively and notifying the IOCP driver.


# Demo executable

With all the above in mind we want to produce an executable that would be like a demo or a hello world inside fast-std for the demonstration of the fast-std facilities.

Themandate (non-negotiable) is to have an executable that depends visibly only on std, no overt sign of fast-std references anywhere. However it must be built on top of our custom fast-std as target. It should write to console (1), read single line input from console (2) check if that input resolves into a file and if so, read the last byte of that file and print it out (3).

NOTES:

1. `fast-std` is a custom target, not a create to import. Its exports therefore are not imported from crates, but as the target std.

