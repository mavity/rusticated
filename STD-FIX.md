# STD-FIX — Required changes to make rusticated build without real std

Rules: no `extern crate std`, no aliasing tricks, no `#[cfg(test)]` gates that
smuggle real-std in, no blocking syscalls (sleep, nanosleep, blocking read/write
without a prior async-readiness guard).

---

## A — `Cargo.toml` (rusticated root): name the lib `std`

File: `Cargo.toml`

```toml
[lib]
name = "std"
bench = false
```

Tells Cargo to emit `--extern std=<rusticated.rlib>` to every crate that
declares rusticated as a dependency, so `std::` in those crates resolves to
rusticated.

---

## B — `src/lib.rs`: remove both `extern crate std` declarations

File: `src/lib.rs`

Delete lines:
```rust
#[cfg(test)]
extern crate std;
```
and
```rust
#[cfg(all(not(target_family = "wasm"), not(test)))]
extern crate std;
```

Neither is permitted. Rusticated may not import real std under any condition.

---

## C — `src/fs.rs`: replace `Metadata` and `metadata()` with raw syscalls

File: `src/fs.rs`

### C1 — `Metadata` struct

Current (banned):
```rust
pub struct Metadata {
    inner: std::fs::Metadata,
}
```

Replace with a struct that holds raw OS fields populated by `stat`/`lstat`
(Linux/BSD) or `GetFileInformationByHandle` (Windows). Example for Linux:
```rust
pub struct Metadata {
    size: u64,
    mode: u32,
    modified_ns: u64,
    accessed_ns: u64,
}
```
Populate from a raw `libc::stat`-equivalent struct obtained via the `lstat`
syscall.

### C2 — `Metadata` impl methods

Current (banned): `self.inner.len()`, `self.inner.modified()`, etc. via
`std::fs::Metadata` delegation.

Replace each accessor with reads from the raw fields above.

### C3 — `metadata()` async function

Current (banned):
```rust
pub async fn metadata<P: AsRef<str>>(path: P) -> crate::io::Result<Metadata> {
    std::fs::symlink_metadata(path.as_ref())
        .map(|inner| Metadata { inner })
        ...
}
```

Replace with a raw `lstat` call (Linux/BSD) or `GetFileAttributesExW` /
`GetFileInformationByHandle` (Windows), constructing `Metadata` from the raw
result.

---

## D — `src/time.rs`: replace `std::time::SystemTime` with raw syscall

File: `src/time.rs`

Current (banned):
```rust
std::time::SystemTime::now()
    .duration_since(std::time::UNIX_EPOCH)
    .map(|d| d.as_nanos() as u64)
    .unwrap_or(0)
```

Replace with:
- Linux/BSD: `clock_gettime(CLOCK_REALTIME, &mut ts)` raw FFI, return
  `ts.tv_sec as u64 * 1_000_000_000 + ts.tv_nsec as u64`
- Windows: `GetSystemTimeAsFileTime`, convert from 100ns-since-1601 to
  ns-since-1970

---

## E — `src/rt/executor.rs` + platform drivers: add timeout-aware poll

Files: `src/rt/executor.rs`, `src/rt/linux_epoll.rs`, `src/rt/windows.rs`,
`src/rt/bsd.rs`

### E1 — Add `poll_with_timeout(timeout_ms: Option<u32>)` to each Driver

- Linux: call `epoll_wait(epfd, events, maxevents, timeout_ms.unwrap_or(-1 as u32 as i32))`
- Windows: call `GetQueuedCompletionStatus(port, ..., timeout_ms.unwrap_or(INFINITE))`
- BSD: call `kevent` with a `timespec` timeout

### E2 — Expose a blocking step from the executor

Add `poll_step_idle(deadline: Option<Duration>) -> io::Result<PollStatus>` (or
extend `poll_step`) so that when the runtime has no ready tasks it blocks inside
the driver using the deadline from `next_deadline()` rather than returning
`Idle` and expecting the caller to sleep.

The `PollStatus::Idle` variant may remain for the WASM target where the host
drives the loop; on native it should never require a caller-side sleep.

---

## F — Remove all `block_on` test helpers that use `std::thread::sleep`

Files: `src/fs.rs`, `src/tty.rs`, `src/process.rs`

Each file contains a `block_on` helper (inside `#[cfg(test)]`) that calls
`std::thread::sleep`. These must be rewritten to use the timeout-aware driver
poll introduced in E instead. The idle branch becomes:

```rust
PollStatus::Idle { next_deadline } => {
    crate::rt::executor::poll_step_idle(next_deadline)?;
}
```

No sleep call of any kind.

---

## G — Remove `std::fs::*` usages from `tty.rs` tests

File: `src/tty.rs`

The Windows tty test `write_to_real_file_handle` uses:
- `std::fs::OpenOptions` — replace with `crate::fs::OpenOptions`
- `std::os::windows::io::AsRawHandle` — replace with direct access to the
  file's internal `handle` field (the test is inside the same module)
- `std::fs::read` — replace with a raw `ReadFile` call or `crate::fs::File::open`
- `std::fs::remove_file` — replace with a raw `DeleteFileW` FFI call

---

## H — `demo/Cargo.toml`: declare rusticated as the `std` dependency

File: `demo/Cargo.toml`

Ensure that `rusticated` is explicitly linked as the `std` dependency using a local path.

```toml
[dependencies]
std = { path = "..", package = "rusticated" }
```

This exploits Cargo's dependency graph. It forces Cargo to compile the custom crate and emit the `--extern std=...` flag correctly, satisfying the `#![no_std]` environment while avoiding manual sysroot hacks.

---

## I — `demo/src/main.rs`: add `#![no_std]`

File: `demo/src/main.rs`

Add as the first line:
```rust
#![no_std]
```

Without this, `rustc` auto-links sysroot std, which does not exist for the
custom target.

Note: `std::rt::spawn!(async_main())` (line 6) must be the entry-point macro
expanded by the `threading` module (see K). All other `std::` usage in this
file resolves to rusticated once H and the `[lib] name = "std"` change (A) are
in place.

---

## J — `demo/.cargo/config.toml`: remove `std` from `build-std`

File: `demo/.cargo/config.toml`

Current (banned):
```toml
build-std = ["core", "alloc", "std"]
```

Change to:
```toml
build-std = ["core", "alloc"]
```

Rusticated IS std. Building upstream std from rust-src on top of it is wrong.

---

## K — `src/rt/mod.rs` or new `src/threading.rs`: implement `spawn!` macro

The demo calls `std::rt::spawn!(async_main())` at crate scope. This macro must
expand to a `fn main()` that:

1. Calls `crate::rt::executor::run(async_main())` to enqueue the root task.
2. Loops calling `crate::rt::executor::poll_step_idle(deadline)` (from E2)
   until `PollStatus::Done`.

No `std::thread::spawn`, no `std::thread::sleep`, no blocking calls other than
the driver's internal timeout-wait.

---

## L — `src/lib.rs` or platform entry: provide `#[panic_handler]`

A `#![no_std]` binary requires a `#[panic_handler]`. Rusticated must supply
one. A minimal implementation:

```rust
#[cfg(not(test))]
#[panic_handler]
fn panic(_info: &core::panic::PanicInfo) -> ! {
    loop {}   // or a raw write to stderr then abort syscall
}
```

A more useful implementation writes the panic message to fd 2 (stderr) via raw
`write` before calling the `abort` syscall.

---

## M — Target JSON: set `"std": false` in metadata

File: `demo/.cargo/rusticated-target.json`

The current JSON has `"metadata": { "std": true }`. Change to `"std": false`
(or remove the key) to reflect that the sysroot std is not being built.

One target JSON per supported platform is required. Generate each with:
```
rustc --print target-spec-json --target <triple> -Z unstable-options
```
then rename the file to a custom name (non-matching any known triple) to force
`build-std` to activate.

---

## Dependency order

```
C (raw Metadata impl)  ──┐
D (raw now_ns)           ├──► B (remove extern crate std) ──► A (lib name = std)
E (driver timeout poll)  │                                         │
F (fix block_on)    ─────┘                                         │
G (fix tty tests)                                                  │
                                                         H + I + J + K + L + M
                                                         (demo wiring, all parallel)
```

C, D, E, F, G must be complete before B can compile cleanly.
B must be complete before A produces a valid rusticated that the demo can link.
H–M are independent of each other and can proceed in parallel once A–B land.
