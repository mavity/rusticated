# Mohabbat Comprehensive Guide

**Version:** Current (self-hosting vegetable)
**Last Updated:** October 2024

This guide covers everything you need to know to work with Mohabbat: from understanding the terminology to building vegetables, developing on rusticated, debugging failures, and contributing to the architecture.

---

## Table of Contents

1. [Getting Started](#1-getting-started)
2. [Terminology Deep Dive](#2-terminology-deep-dive)
3. [Building Vegetables](#3-building-vegetables)
4. [Development Workflow](#4-development-workflow)
5. [Vegetable File Format](#5-vegetable-file-format)
6. [Brot: The Native Loader](#6-brot-the-native-loader)
7. [Washmhost: The WASM Host](#7-washmhost-the-wasm-host)
8. [Rusticated Go](#8-rusticated-go)
9. [Rusticated Rust Sysroot](#9-rusticated-rust-sysroot)
10. [Network ABI](#10-network-abi)
11. [Dynamic Library (DLL) & FFI](#11-dynamic-library-dll--ffi)
12. [Callback & Cross-Island Invocation](#12-callback--cross-island-invocation)
13. [Build System Internals](#13-build-system-internals)
14. [Fallback Mechanisms](#14-fallback-mechanisms)
15. [Testing](#15-testing)
16. [Platform Selection & Node.js Fallback](#16-platform-selection--nodejs-fallback)
17. [Ghost Process Prevention](#17-ghost-process-prevention)
18. [Troubleshooting & Debugging](#18-troubleshooting--debugging)
19. [Contributing & Development Rules](#19-contributing--development-rules)

---

## 1. Getting Started

### Prerequisites

- **Go 1.23+** (for building washmhost and Mohabbat itself)
- **Rust toolchain** (for brot and rusticated targets)
- **Node.js 18+** (optional — for Node.js fallback on unsupported platforms)
- **Git** (for version control)

### Quick Start

```bash
# Build the core libraries and mohab.bat vegetable
go run .

# Build and run a demo project (development mode, no vegetable packaging)
go run . demo -r
go run . demo-go -r
go run . demo-go/curl -r https://example.com

# Build a standalone vegetable
go run . demo -o demo.bat
go run . demo-go/curl -o curl.bat

# Run the vegetable on any supported platform
./demo.bat
./curl.bat https://example.com
```

### Building Different Projects

```bash
# Demo projects
go run . demo -o demo.bat          # Rust demo (file I/O, timers, async)
go run . demo/loch -o loch.bat     # File manager UI prototype (ratatui)

# Go projects
go run . demo-go -o demo-go.bat                # Go demo
go run . demo-go/trivial -o trivial.bat        # Minimal Go example
go run . demo-go/curl -o curl.bat              # HTTP(S) client

# Flagship projects
go run . kabibi -o kabibi.bat                  # Go flagship (shell + AI)
go run . kabibi-rust -o kabibi-rust.bat        # Rust flagship

# Development mode (skip vegetable packaging)
go run . demo -r
go run . demo/loch -r
go run . demo-go/curl -r https://example.com
```

---

## 2. Terminology Deep Dive

| Term | Definition | Analogous To |
|------|-----------|--------------|
| **🍆vegetable** | A self-extracting, polyglot single-file executable (`.bat` extension). Contains everything needed to run a WASM app natively. | A static binary with a zip file embedded, but much more sophisticated. |
| **Mohabbat** | The builder vegetable itself (`mohab.bat`). Self-hosting: it can rebuild itself from source. | A build tool that is also its own output. |
| **brot** | A minimal native binary that acts as the first-stage loader. Its only job: decompress the pool and launch washmhost. | Like a `bootloader` for the application. |
| **washmhost** | The WASM host runtime. A Go program (using Wazero) that loads brain.wasm and implements all rusticated host functions. | The OS kernel for the WASM guest. |
| **brain** | The actual application code, compiled to WASM, compressed, and embedded in the vegetable. | The "userland" of the operating system analogy. |
| **pool** | A Brotli-compressed (or Zstd) blob containing all washmhost binaries + the brain, concatenated. | A compressed archive. |
| **rusticated** | The custom platform ABI — async I/O, networking, DLL loading, TLS, etc. Not sandboxed. | Like WASI but async-only and capability-rich. |
| **overlapped** | A small guest-owned memory structure used for async completion signaling. The host writes to it; the guest polls it. | Like a POSIX `io_uring` submission/completion queue, but simpler. |
| **satellite** | A spawned washmhost process that handles dangerous operations (DLL FFI) with full crash isolation. | A "microservice" in a separate address space. |
| **cross-island** | Any call between different execution contexts: guest↔host, host↔satellite, guest↔satellite, guest↔future WASM modules. | Like an RPC call. |
| **Modern Five** | The five target platforms: `linux-amd64`, `linux-arm64`, `windows-amd64`, `windows-arm64`, `darwin-arm64`. | The supported architecture matrix. |
| **regast** | An AST-aware source code patcher used for surgical modifications of Go SDK source files during overlay generation. | A focused sed/awk for Go ASTs. |

---

## 3. Building Vegetables

### The Build Pipeline

```
User command: go run . <project> -o <output.bat>
                    │
                    ▼
         ┌──────────────────────┐
         │    prebuild.go        │
         │  ┌────────────────┐   │
         │  │ Generate Go     │   │
         │  │ overlay.json    │   │
         │  └────────────────┘   │
         │  ┌────────────────┐   │
         │  │ Build Rust      │   │
         │  │ sysroots (5x)   │   │
         │  └────────────────┘   │
         └────────┬─────────────┘
                  │
                  ▼
         ┌──────────────────────┐
         │    build.go          │
         │  ┌────────────────┐   │
         │  │ Compile brot    │   │
         │  │ (N native bins)  │   │
         │  └────────────────┘   │
         │  ┌────────────────┐   │
         │  │ Compile brain   │   │
         │  │ (WASM module)   │   │
         │  └────────────────┘   │
         │  ┌────────────────┐   │
         │  │ Compile         │   │
         │  │ washmhost (Nx)  │   │
         │  └────────────────┘   │
         └────────┬─────────────┘
                  │
                  ▼
         ┌──────────────────────┐
         │    package.go         │
         │  ┌────────────────┐   │
         │  │ Concatenate     │   │
         │  │ brot binaries   │   │
         │  │ (Zone B)        │   │
         │  └────────────────┘   │
         │  ┌────────────────┐   │
         │  │ Compress pool   │   │
         │  │ (brotli/zstd)   │   │
         │  └────────────────┘   │
         │  ┌────────────────┐   │
         │  │ Polyglot header │   │
         │  │ (Zone A)        │   │
         │  └────────────────┘   │
         │  ┌────────────────┐   │
         │  │ Combine → .bat  │   │
         │  └────────────────┘   │
         └────────┬─────────────┘
                  ▼
          output.bat ready!
```

### Build Modes

| Command | Mode | Description |
|---------|------|-------------|
| `go run .` | `ModeDefault` | Rebuild core libraries + mohab.bat |
| `go run . <proj>` | `ModeBuild` | Build a full vegetable for a project |
| `go run . <proj> -o <file>` | `ModePackage` | Build vegetable with custom output path |
| `go run . <proj> -r` | `ModeRun` | Compile brain + run directly under washmhost (no vegetable packaging, fast dev cycle) |
| `go run . <proj> -t` | `ModeTest` | Compile and run unit tests under washmhost |
| `mohab.bat <proj> --refill` | `ModeRefill` | Replace the brain in an existing vegetable (fast update) |

### Prebuild Scoping

The `runPrebuild` function takes a `PrebuildScope` to avoid unnecessary work:

| Scope | Rust sysroots built | Go overlay built | When used |
|-------|--------------------|------------------|-----------|
| `GoRun` | None (skipped) | Yes | `go run . <go-proj> -r` |
| `RustRun` | WASM target only | No | `go run . <rust-proj> -r` |
| `FullAssembly` | All 5 targets | Yes | `ModeBuild`, `ModePackage`, `ModeDefault` |

This means `go run . <go-project> -r` does NOT build any Rust sysroots — they are only needed for brot/washmhost assembly, not for development runs.

### Build Commands Reference

```bash
# Build everything
go run .

# Build a Rust demo project with output
go run . demo -o demo.bat

# Build a Go demo with output
go run . demo-go -o demo-go.bat

# Full packaging (build + compress into vegetable)
go run . kabibi -o kabibi.bat

# Dev run (skip vegetable compression, run directly)
go run . demo-go/curl -r https://example.com

# With verbose output
go run . demo -r -v

# Build tests
go run . demo-go -t
go run . kabibi -t -- -test.v    # pass args to test runner
go run . kabibi -t -- -test.timeout=5m

# Refill (replace brain only)
mohab.bat demo.bat --refill
```

Guest test binaries run with the tested package directory as their working directory.
The `-test.timeout` value also bounds host execution and interrupts a guest that does not honor Go's in-process test timeout.

---

## 4. Development Workflow

### Fast Iteration Cycle

For rapid development, use `-r` mode:

```bash
# Edit code in demo-go/...
# Then run (no vegetable compression — seconds not minutes)
go run . demo-go -r
```

The `-r` mode:
1. Compiles your project to WASM (brain)
2. Compiles washmhost if needed
3. Runs washmhost with your brain directly
4. No Zone A/B/C assembly → no 5-minute compression wait

### When to Build a Full Vegetable

- **Distribution**: When you want to share your app with others.
- **Cross-platform testing**: When you want to test the full self-extraction flow.
- **Performance benchmarking**: Vegetable compression affects startup time.
- **Self-hosting**: When rebuilding mohab.bat itself.

### Verbosity Control

| Environment | Behavior |
|-------------|----------|
| Normal (no flag) | Quiet — success shows one-line summary or program output; failure shows labelled error |
| `-v` / `--verbose` | Full streaming output from all subprocesses |
| `MOHABBAT_USE_NODE=1` | Force Node.js fallback on any platform |

---

## 5. Vegetable File Format

### Zone A — Polyglot Script Header

The header is a hybrid script valid in both `cmd.exe` and POSIX `sh`. Responsibilities:

1. **Detect OS/CPU** using only built-in shell features.
2. **Look up byte offset + length** of the correct brot in Zone B (baked in at assembly time).
3. **Extract brot + Zone C** to a temp file (unique per run: PID + random).
4. **Make it executable** on POSIX (`chmod +x`).
5. **Execute** with all user arguments forwarded.
6. **Propagate exit code**.

**Technical constraints:**

- No UTF-8 BOM (CMD chokes on it).
- CMD section ends with `goto :eof` and explicit exit code.
- POSIX section uses `exit $?`.
- Binary slicing: POSIX uses `dd bs=1 skip=... count=... 2>/dev/null` (cross-platform dd).
  Windows uses PowerShell: `[IO.File]::OpenRead` + `Seek` + `Read`.
- `.bat` extension is mandatory (CMD requires it).
- Temp path includes PID + random to avoid collisions.

### Zone B — Brot Table

- Each **brot** is a complete native executable (ELF, PE, or Mach-O).
- Built in Rust with `no_std`, `no_main`, `no_CRT`.
- Concatenated with no padding.
- Offsets and lengths are baked into Zone A at assembly time.
- The header does not parse — it only slices by known numbers.

### Zone C — Pool

- A single compressed stream (currently Brotli, migrating to Zstd).
- Decompressed content: washmhost #1, washmhost #2, ..., washmhost #N, brain.
- Joint compression across all washmhosts saves significant space (they share Go stdlib code).
- Brot offset + length is also baked into the header.

---

## 6. Brot: The Native Loader

### Design Principles

- **No Rust stdlib** — `#![no_std]`, zero runtime overhead.
- **No `main`** — entry point is the OS convention (`_start` on Linux).
- **No CRT** — no C runtime, no libc.
- **Custom allocator** — `OsAllocator` routes to `mmap` (Linux), `HeapAlloc` (Windows), `memory_grow` (WASM).
- **Tiny binary** — minimal code, no static dictionary (after Zstd migration).

### What Brot Does

```rust
fn main() {
    // 1. Parse argv to find the pool offset within this binary
    // 2. Decompress the pool (brotli → zstd)
    // 3. Find the correct washmhost by offset
    // 4. Write washmhost + brain to temp files
    // 5. Fork+execve (Linux) / CreateProcess (Windows) to run washmhost
    // 6. Wait and propagate exit code
}
```

### Brot Source Layout

```
brot/
├── Cargo.toml           # Dependencies (ruzstd etc.)
├── src/
│   ├── lib.rs           # WASM target: brot_decompress export
│   ├── decompress.rs    # Core decompression logic (pool → memory)
│   ├── linux.rs         # Linux launcher (prctl PDEATHSIG, fork+execve)
│   ├── darwin.rs        # macOS launcher
│   ├── windows.rs       # Windows launcher (CreateJobObject, CreateProcess)
│   ├── alloc.rs         # Custom allocator
│   └── os_*.rs          # OS syscall wrappers (open, read, write, mmap, etc.)
├── js/
│   └── node/
│       └── starter.js   # Node.js fallback entry point
```

### Cross-Compilation

Brot is compiled for all 5 Modern Five targets via:
```bash
cargo build --target <triple> --config <target-specific-flags>
```

Build options remove fluff:
- Max optimization (`-O3` or equivalent)
- No debug symbols
- Single binary output (no dynamic linking)

---

## 7. Washmhost: The WASM Host

### Architecture

Washmhost is a Go program using the **Wazero** WASM runtime. It acts as the operating system for the WASM brain.

```
┌─────────────────────────────────────────────┐
│              washmhost (Go)                  │
│                                              │
│  ┌──────────────┐   ┌──────────────────────┐│
│  │  Wazero VM    │   │  Host Environment    ││
│  │  (brain.wasm) │   │  (HostEnv)           ││
│  └──────┬───────┘   │  ┌──────────────────┐ ││
│         │           │  │ env_file.go       │ ││
│         │           │  │ env_net.go        │ ││
│         │           │  │ env_dylib.go      │ ││
│         │           │  │ env_crypto.go     │ ││
│         │           │  │ env_impl.go       │ ││
│         │           │  └──────────────────┘ ││
│         │           │  ┌──────────────────┐ ││
│         │           │  │  Satellite IPC    │ ││
│         │           │  │  (pipe/socket)    │ ││
│         │           │  └──────────────────┘ ││
│         │           └──────────────────────┘│
│         │                                    │
│         ▼                                    │
│  Guest event loop (poll overlappeds)         │
└─────────────────────────────────────────────┘
```

### Host Functions (ABIs)

All host functions follow the overlapped I/O pattern. The guest passes an overlapped pointer; the host completes it asynchronously.

#### File I/O (`env_file.go`)

| Function | Signature | Description |
|----------|-----------|-------------|
| `sys_read` | `(ovPtr, handle, bufPtr, bufLen)` | Read from file/network handle |
| `sys_write` | `(ovPtr, handle, bufPtr, bufLen)` | Write to file/network handle |
| `sys_open` | `(ovPtr, pathPtr, pathLen, flags)` | Open a file |
| `sys_close` | `(handle)` | Close a handle |
| `sys_stat` | `(ovPtr, pathPtr, pathLen)` | Stat a path |
| `sys_list_dir` | `(ovPtr, pathPtr, pathLen)` | List directory contents |

#### Networking (`env_net.go`)

| Function | Signature | Description |
|----------|-----------|-------------|
| `sys_net_open` | `(ovPtr, addrPtr, addrLen, port, flags)` | TCP connect (client) or listen (server) |
| `sys_net_accept` | `(ovPtr, listenHandle)` | Accept a TCP connection |
| `sys_net_lookup` | `(ovPtr, namePtr, nameLen, bufPtr, bufLen)` | DNS resolution via host |
| `sys_net_cert_verify` | `(ovPtr, namePtr, nameLen, chainPtr, chainLen)` | Offload TLS cert verification to host |

#### Dynamic Libraries (`env_dylib.go`)

| Function | Signature | Description |
|----------|-----------|-------------|
| `sys_dylib_open` | `(ovPtr, pathPtr, pathLen, flags)` | Load a DLL (via satellite) |
| `sys_dylib_sym` | `(ovPtr, handle, namePtr, nameLen)` | Resolve a symbol |
| `sys_dylib_call` | `(ovPtr, sym, sigPtr, sigLen, argsPtr, argsLen, parentInvID)` | Call a DLL function |
| `sys_dylib_callback_register` | `(ovPtr, sigPtr, sigLen, ovPtr, ovSize)` | Register a callback |
| `sys_dylib_alloc` | `(ovPtr, size)` | Allocate in satellite memory |
| `sys_dylib_free` | `(ptr)` | Free satellite memory |
| `sys_dylib_read_mem` | `(ovPtr, satPtr, guestBufPtr, len)` | Read satellite memory |
| `sys_dylib_write_mem` | `(ovPtr, satPtr, guestBufPtr, len)` | Write satellite memory |

### The Overlapped I/O Pattern

```go
// Guest side (simplified):
ov := newOverlapped()
sys_read(ov, handle, buf, len)       // call host
yieldUntilComplete(ov)                // poll with goroutine yield
data := ov.result

// Host side (simplified):
func sys_read(ov, handle, buf, len) {
    fileOpsQueue.add(ReadOp{ov, handle, buf, len})
    // later, when I/O completes:
    writeOverlapped(ov, errno=0, result=data)
}
```

The guest polls overlappeds during `run()` slices. When an overlapped is found complete, the associated goroutine is resumed.

### Satellite IPC

For DLL FFI, washmhost spawns a **satellite** process:

```
washmhost (main)
    │
    ├─── Guest goroutine ── dylib_call() ──┐
    │                                       │
    │  IPC pipe (gob-encoded messages)      │
    │                                       │
    ├─── Satellite process (washmhost       │
    │    --dylib-satellite) ◄───────────────┘
    │         │
    │         ├── Loads DLL via purego
    │         ├── Calls native function
    │         ├── If crash → pipe breaks → host traps error
    │         └── Returns result via IPC
    │
    └─── Normal event loop continues
```

---

## 8. Rusticated Go

### How It Works

Go code compiles to WASM (`wasip1`) with **no source modifications**. The mechanism:

1. **Overlay Generation** (`prebuild.go:generateGoOverlay`): Creates `target/overlay.json` mapping Go SDK source files → rusticated replacements:

```json
{
    "src/syscall/net_wasip1.go": "rusticated-go/syscall/net_rusticated.go",
    "src/internal/syscall/unix/net_wasip1.go": "rusticated-go/internal/syscall/unix/net_rusticated.go",
    "src/os_wasm.go": "rusticated-go/os/os_rusticated.go",
    ...
}
```

2. **AST Patching** (`patch_go_deps.go`): Uses `regast` for surgical AST-aware substitutions in SDK files that can't be fully replaced.

3. **Compilation**: `go build -buildmode=c-shared -overlay target/overlay.json` produces a WASM reactor.

### What Gets Overlaid

| Go SDK Module | Replacement | Purpose |
|---------------|-------------|---------|
| `syscall/*` | `rusticated-go/syscall/*` | Replace all I/O syscalls with overlapped-based async calls |
| `os` | `rusticated-go/os/*` | Process env, signals, exit |
| `net` | `rusticated-go/net/*` | Network via `net_open`, DNS via `net_lookup` |
| `crypto/tls`, `crypto/x509` | `rusticated-go/crypto/*` | TLS via `net_cert_verify` |
| `runtime` | `rusticated-go/runtime/*` | Goroutine scheduler integration with overlapped polling |

### Go Version Compatibility

The rusticated overlay is designed for **Go 1.27.1**. Key compatibility notes:

- **Lock counter change**: Go 1.27 reserves the lower 4 bits of the lock counter for status flags. The increment is now 16 (not 1). The overlay must match.
- **Goroutine yield**: The scheduler calls `handleContinuation` which must include overlapped polling.
- **`wasip1` target**: Keep the standard Go WASI target; just replace SDK source files before compilation.

### The Virtual FD Table

`rusticated-go/syscall/net_rusticated.go` implements a virtual file descriptor table:

```go
var netSlots []netSlot    // Internal table
const baseOffset = 1000   // Avoid collision with real fds

func Socket(...) -> virtualFD {
    slot := allocateSlot()
    return baseOffset + slot.index
}

func Connect(virtualFD, addr) {
    slot := netSlots[virtualFD - baseOffset]
    host_handle := hostNetOpen(addr, flags=connect)
    slot.handle = host_handle
}

func Read(virtualFD, buf) {
    slot := netSlots[virtualFD - baseOffset]
    hostRead(slot.handle, buf)
}
```

---

## 9. Rusticated Rust Sysroot

### Why It's Necessary

Rust's standard library is predominantly synchronous and blocking. Rust's `std` APIs for file I/O, networking, and even `println!` assume blocking semantics. On rusticated, **all I/O is async** — there are no blocking syscalls.

### The Solution

A custom sysroot that rewrites Rust's standard library to use rusticated's async ABI:

1. **Custom targets**: e.g., `x86_64-rusticated-wasi`, defined in target spec JSON.
2. **`build-std`**: `cargo build -Z build-std=core,alloc,compiler_builtins` builds std from source with `rust-src`.
3. **`RUST_SRC_PATH`**: Points to the extracted `rust-src` library sources when `rustup component add rust-src` is unavailable.
4. **`--sysroot`**: For precompiled target std (brot targets), points to an overlay sysroot.

### What Changes

| Standard API | Rusticated Replacement | Status |
|-------------|----------------------|--------|
| `std::fs::*` | Async file I/O via overlapped calls | ✅ |
| `std::net::*` | Async TCP via `net_open`/`net_accept` | ✅ |
| `std::io::*` | Async read/write | ✅ |
| `std::time::*` | Async timers via overlapped | ✅ |
| `std::process::*` | Process spawning | 🔄 In progress |
| `std::thread::*` | Mapped to goroutines | 🔄 In progress |

### Impact on Third-Party Crates

Many crates that depend on synchronous `std::io::*` or `std::net::*` will NOT build against the rusticated sysroot. This is a known limitation. Affected projects need to be ported to use the async equivalents.

Crates that work:
- Pure computation (no I/O)
- Crypto (if no blocking I/O)
- Data structures
- Terminal UI (ratatui) — async reads from stdin

Crates that need porting:
- HTTP clients (must use rusticated net ABI)
- Database drivers (sync I/O)
- File watchers
- Thread pools

---

## 10. Network ABI

### Architecture

```
Guest (WASM)
  │
  ├── net.Dial("tcp", "host:port")
  │     │
  │     ├── syscall.Connect(virtualFD, sockaddr)
  │     │     └── hostNetOpen(addr, port, flags=connect)
  │     │           └── overlapped → host TCP connect
  │     │
  │     └── syscall.Read/Write(virtualFD, buf)
  │           └── hostRead/Write(handle, buf)
  │                 └── overlapped → host TCP send/recv
  │
  ├── net.LookupHost("example.com")
  │     └── hostNetLookup(name)
  │           └── overlapped → host DNS resolution
  │                 └── returns packed IP addresses
  │
  └── TLS handshake
        └── hostNetCertVerify(name, chain)
              └── overlapped → host uses OS verifier
                    └── returns 1 (valid) or 0 (invalid)
```

### Implemented ABIs

| ABI | Direction | Status |
|-----|-----------|--------|
| `net_open` (connect) | Client TCP | ✅ |
| `net_open` (listen) | Server TCP | ✅ |
| `net_accept` | Accept connection | ✅ |
| `net_lookup` | DNS resolution | ✅ |
| `net_cert_verify` | TLS cert verification | ✅ |
| `read`/`write` | Generic I/O (reuses file I/O) | ✅ |
| `handle_close` | Close connection | ✅ |

### DNS Wire Format

`net_lookup` returns packed IP addresses:

```
Offset Size   Field
0      4      total_size  (little-endian)
4      4      count       (number of IPs)
8      1*N    af[]        (4=IPv4, 6=IPv6)
8+N    ...    addr_data   (4 bytes per IPv4, 16 per IPv6)
```

### Cert Verification

`net_cert_verify` uses the host's native OS Platform Verifier:

- The guest sends the DER-encoded certificate chain.
- The host calls Go's `(*x509.Certificate).Verify()` with `nil` roots (uses platform trust store).
- Returns 1 (pass) or 0 (fail).

**Absolute prohibitions:**
- ❌ No `InsecureSkipVerify`
- ❌ No bundled CA PEM files
- ❌ No hardcoded pins or fingerprints
- ❌ No silent HTTP downgrade
- ❌ No `-k` or `--insecure` flags

---

## 11. Dynamic Library (DLL) & FFI

### Architecture Evolution

| Stage | Description | Status |
|-------|-------------|--------|
| **Legacy (in-process)** | purego loads DLLs directly in washmhost process. Fatal crash risk. | ❌ Being phased out |
| **Current (satellite)** | purego runs in a spawned satellite process. IPC over pipes. Crash isolated. | ✅ Primary |
| **Future (unified)** | Satellite + future WASM modules use the same cross-island model. | 🔄 In progress |

### Satellite Process Isolation

```
washmhost (main process)
    │
    ├── User goroutine calls DylibOpen("libfoo.so")
    │     │
    │     ├── host spawns "washmhost --dylib-satellite"
    │     ├── IPC pipe established (gob-encoded)
    │     ├── Satellite loads libfoo.so via purego
    │     ├── Returns handle to main process
    │     └── Main returns handle to guest
    │
    ├── User goroutine calls DylibCall(handle, "function", args)
    │     │
    │     ├── Main sends IPC message: {sym, sig, args}
    │     ├── Satellite receives, calls native function
    │     ├── If crash → pipe breaks → main gets EBROKENPIPE
    │     ├── If success → Satellite sends result via IPC
    │     └── Main completes overlapped with result
    │
    └── User goroutine calls DylibClose(handle)
          │
          └── Main sends close → Satellite unloads DLL
```

### Memory Management

Satellite memory is managed through explicit helpers:

```go
// Allocate in satellite (uses native platform allocator)
allocPtr := DylibAlloc(size)

// Write guest data to satellite memory
DylibWriteMem(allocPtr, guestBytes)

// Read satellite memory to guest buffer
DylibReadMem(satPtr, dstBuf)

// Free
DylibFree(allocPtr)

// Convenience
str := GoString(satPtr)          // Read C string from satellite
satPtr := CString("hello")       // Allocate + write NUL-terminated string
// satPtr must be freed with DylibFree later
```

### ABI Contract (64-bit Flat Ordinals)

All handles, pointers, and callback tokens are `uint64`:

```go
func DylibOpen(path string, flags int) (uint64, error)
func DylibSym(handle uint64, name string) (uint64, error)
func DylibCall(sym uint64, sig []byte, args []uint64, parentInvID uint64) ([]uint64, error)
func DylibRegisterCallback(sig []byte, cbOvPtr unsafe.Pointer, cbOvLen uint32, fn func(args []uint64) []uint64) (uint64, error)
func DylibAlloc(size uint64) (uint64, error)
func DylibFree(ptr uint64) error
func DylibReadMem(ptr uint64, dst []byte) error
func DylibWriteMem(ptr uint64, src []byte) error
```

---

## 12. Callback & Cross-Island Invocation

### The Model

Callbacks are delivered through **guest-owned overlapped cells**. The model is:

1. **Guest registers a callback**: provides a WASM-format signature and a guest-owned overlapped.
2. **Host returns a token**: a 64-bit value that is directly callable in the target universe (e.g., a native trampoline address in the satellite process).
3. **Native code invokes the callback**: The satellite sends an invocation message to the host.
4. **Host projects into the guest overlapped**: Writes arguments, sets completion bit.
5. **Guest discovers during event loop**: Polls overlappeds, sees completion=1, clears it, copies args, queues handler goroutine.
6. **Guest completes**: Writes return values, sets completion=1, yields.
7. **Host returns to satellite**: Sends completion message with results.

### Overlapped Ownership Rules

| Who | When | Can Do What |
|-----|------|-------------|
| **Host** | Before `run()` and after `run()` returns | Project new invocations, drain completed returns |
| **Guest** | During `run()` | Poll overlappeds, read invocations, write returns |
| **Neither** | During `run()` | Host must not touch active callback cells; guest owns them exclusively |

### Synchronous vs Async Handling

**Synchronous (inline):**
```go
// Guest discovers invocation
ov := pollCallbackOverlapped()
if ov.completed {
    args := readArgs(ov)
    result := callbackFn(args)     // execute immediately
    writeResult(ov, result)
    // Leave completed=1 → host drains at end of run()
}
```

**Asynchronous:**
```go
// Guest discovers invocation
ov := pollCallbackOverlapped()
if ov.completed {
    args := copyArgsFromOv(ov)
    ov.completed = 0               // signal: "I accept this async"
    spawnGoroutine(fn(args))       // run later...
}

// Later, goroutine finishes:
ov := getOvForThisRegistration()
writeResult(ov, result)
ov.completed = 1
pauseToHost()                      // yield immediately so host reads result
```

### Thread-Local Stack Discipline

Each satellite callback thread has its own LIFO stack of callback frames:

```
Thread A stack:         Thread B stack:
   [A1]                     [B1]
   [A2]                     [B2]
   [A3]                     [B3]  ← top
```

- Returns are always for the top-most frame first.
- Non-top completions are **parked** in host memory.
- When top completes, it's returned, then parked frames are unwound in LIFO order.

### Parent Invocation ID

When a callback handler makes a cross-island call (e.g., calling back into the DLL), it passes the invocation ID it received so the call is routed to the correct satellite thread:

```go
invID := ov.invocationID    // copied from overlapped
result := DylibCall(sym, sig, args, parentInvID=invID)
```

If the goroutine is not servicing a callback, `parentInvID = 0`.

---

## 13. Build System Internals

### Key Files

| File | Role |
|------|------|
| `mohabbat/build.go` | Top-level orchestration: `ModeBuild`, `ModeRun`, `ModeTest`, `ModePackage`, `ModeDefault` |
| `mohabbat/prebuild.go` | Generate Go overlay (`generateGoOverlay`), build Rust sysroots (`buildTargetSpecs`) |
| `mohabbat/build_go_project.go` | Compile Go projects to WASM |
| `mohabbat/build_rust_project.go` | Compile Rust projects to WASM |
| `mohabbat/package.go` | Assemble vegetable (Zone A + B + C) |
| `mohabbat/refill.go` | Replace brain in existing vegetable |
| `mohabbat/run.go` | Run a brain under washmhost directly |
| `mohabbat/patch_go_deps.go` | AST-aware Go SDK patching |
| `mohabbat/wasm_postprocess.go` | Post-process WASM (rename `_initialize` → `run`) |
| `mohabbat/util.go` | Shared utilities |
| `mohabbat/types.go` | Shared types |
| `mohabbat/cmdrun.go` | Subprocess execution with output capture |
| `mohabbat/rust_sysroot_fallback.go` | Failure-driven Rust toolchain fallback |

### Output Buffering

Build steps use `runCmd` which captures subprocess output:

- **Success → quiet**: Output is discarded. One-line summary (e.g., `🍆 sysroot built: x86_64-rusticated-linux`) or nothing.
- **Failure → verbose**: Labelled header + captured output is printed.
- **`-v` → streaming**: All output is passed through in real time.

### Concurrency

Independent steps run in parallel:

- Environment probes (`rustc -vV`, `rustc --print sysroot`, `go env GOROOT`, etc.) are fired simultaneously.
- Prebuild targets can use independent `--target-dir` to avoid Cargo workspace lock.
- Prebuild scope (`PrebuildScope`) avoids unnecessary work for dev runs.

---

## 14. Fallback Mechanisms

### Rust Target/Component Fallback

When `cargo build` fails because of a missing Rust target std (`rust-std`) or missing `rust-src` component, the fallback activates:

**Flow (failure-driven only):**
```
1. cargo build fails
2. Classify failure:
   ├── rustTargetStdPresent? → target lib dir exists + has libcore-*.rlib?
   └── rustSrcPresent? → src/rust/library/core/src/lib.rs exists?
3. If missing:
   ├── rustup present? → rustup target add / rustup component add
   └── no rustup? → download precompiled .tar.xz from static.rust-lang.org
4. Verify SHA256 against channel manifest
5. Make available:
   ├── Branch A (brot): compose overlay sysroot, retry with --sysroot
   └── Branch B (rusticated): set RUST_SRC_PATH, retry
6. Retry exactly once
7. If still fails → surface real error
```

**Key rules:**
- ❌ No pre-flight probes. Only runs after actual build failure.
- ❌ No writes outside `target/` tree.
- ❌ No writes to `~/.rustup`.
- ✅ HTTPS + SHA256 verification.
- ✅ `xz_url`/`xz_hash` from channel manifest (not `.tar.gz` URLs).

### Node.js Fallback

On unsupported platforms (macOS, FreeBSD, Android, ARM Linux musl, etc.), the vegetable uses Node.js ≥ 18 as the WASM host:

```
Zone A detects unsupported platform
  → Extracts brot for Node.js slot
  → Searches PATH + common locations (nvm, fnm, Volta, n, Chocolatey, Homebrew)
  → Runs: node <brot.js> <pool-offset> <pool-len>
```

Can be forced on any platform:
```bash
MOHABBAT_USE_NODE=1 ./demo-go.bat
```

---

## 15. Testing

### How It Works

Tests run through the exact same pipeline as production code:

```bash
# Run tests for a Go project
go run . demo-go -t

# With test arguments
go run . demo-go -t -- -test.v -test.run ^TestFoo$
```

The pipeline:
1. `prebuild.go` generates the overlay (same as for production).
2. `buildGoTestWasm` runs:
   ```
   go test -c -buildmode=c-shared -overlay target/overlay.json -o target/test.wasm <project_dir>
   ```
   - `-c` means "compile only, don't run".
   - `-buildmode=c-shared` produces a WASI reactor.
   - `-overlay` applies rusticated SDK overrides.
3. `postProcessWasm` renames `_initialize` → `run`.
4. `runUnderWashmhost` executes the test WASM under washmhost.

### Benefits

- **Same environment as production**: Tests exercise the same overlay, same host functions, same ABI.
- **No custom test runner**: Hijacks Go's built-in test synthesizer.
- **Exit code propagation**: 0 = pass, 1 = fail.
- **Seamless output**: Test results (PASS/FAIL/logs) print to terminal.

### Validation

Run tests both natively and under Mohabbat, compare results:
```bash
# Native (Go)
cd demo-go && go test

# Under Mohabbat
go run . demo-go -t
```

Any differences should have a genuine explanation (e.g., test uses blocking I/O not available on rusticated).

---

## 16. Platform Selection & Node.js Fallback

### Auto-Detection

The vegetable auto-detects OS and CPU from the polyglot header using built-in shell commands:

- **Linux**: `uname -m` + `uname -s`
- **macOS**: `uname -m` + `uname -s`
- **Windows**: `%PROCESSOR_ARCHITECTURE%` (CMD) or `uname -m` (PowerShell)

### Manual Override

```bash
# Force a specific native platform
./demo-go.bat --platform linux-amd64
./demo-go.bat --platform windows-arm64

# Abbreviated (auto-detects OS)
./demo-go.bat --platform amd64
./demo-go.bat --platform arm64
./demo-go.bat --platform x64

# Force Node.js fallback
./demo-go.bat --platform node
```

### Valid Platform Names

| Canonical | Meaning |
|-----------|---------|
| `node` | Use Node.js WASM host (any OS) |
| `linux-amd64` | Linux x86_64 native |
| `linux-arm64` | Linux ARM 64-bit native |
| `darwin-arm64` | macOS Apple Silicon native |
| `windows-amd64` | Windows x86_64 native |
| `windows-arm64` | Windows ARM 64-bit native |

### Slot Availability

If a platform slot was disabled during assembly, an error is shown:
```
❌ Platform 'X' is not included in this vegetable
```

---

## 17. Ghost Process Prevention

Three lines of defence prevent orphan processes on crash:

### Line 1: Explicit Polling

`washmhost` watches for `brot`'s PID:

- **UNIX**: Polls `os.Getppid()` periodically. If parent PID changes (brot died), calls `os.Exit(1)`.
- **Windows**: Opens a handle to brot's process (`OpenProcess(SYNCHRONIZE)`) and calls `WaitForSingleObject`. If brot exits, washmhost exits.

### Line 2: OS-Enforced Cascading

- **Linux**: `brot` calls `prctl(PR_SET_PDEATHSIG, SIGKILL)` after `fork()` and before `execve()`. The kernel kills washmhost if brot dies for any reason.
- **Windows**: `brot` creates a **Job Object** (`CreateJobObjectW`) with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. The washmhost process is placed inside the job. When brot exits, the job closes, and washmhost is killed.

### Line 3: Explicit Cleanup

Standard teardown path explicitly kills child processes in correct order.

---

## 18. Troubleshooting & Debugging

### Build Fails

```bash
# 1. Get verbose output
go run . -v

# 2. Check if it's a missing Rust target
#    (build should auto-fallback, but verify)
go run . 2>&1 | grep -i "fallback\|missing target\|rust-src"

# 3. Clean and retry
go run . --clean   # if supported, else manually clean target/
```

### Vegetable Won't Run

```bash
# 1. Try with explicit platform
./demo.bat --platform node
./demo.bat --platform linux-amd64

# 2. Verify the vegetable includes the slot you need
#    (error message will say if platform is missing)

# 3. Use Node.js fallback
MOHABBAT_USE_NODE=1 ./demo.bat
```

### Dev Run (`-r`) Fails But Build Succeeds

```bash
# Common causes:
# 1. Missing Go overlay → rebuild prebuild
go run . <proj> -r -v

# 2. Brain WASM has issues → check post-processing
#    The _initialize → run rename must happen
```

### DLL/Satellite Issues

```bash
# Satellite crashes show as broken pipe errors
# Check:
# 1. Is the DLL compiled for the right architecture?
# 2. Is the satellite process launched with correct --platform?
# 3. Try with MOHABBAT_USE_NODE=1 (satellite isolation still works)
```

### Network Issues

```bash
# 1. Check DNS
go run . demo-go/curl -r http://example.com

# 2. Check TLS (will fail if cert verification broken)
go run . demo-go/curl -r https://example.com

# 3. Test TCP loopback
go run . demo-go/server -r /tmp &
go run . demo-go/curl -r http://127.0.0.1:8080/
```

---

## 19. Contributing & Development Rules

### Hard Rules

1. **Async I/O only.** Do NOT port anything back to blocking I/O.
2. **Custom std/sysroot.** Dependent crates (demo, brot, washmhost, loch, etc.) use rusticated, not standard library.
3. **Self-review.** Your output will be rigorously torn apart by an adversarial filter. Find your own errors first.
4. **Report errors.** Any error NOT reported will be penalised. Any error you DO report contributes to the success metric.

### Forbidden

- ❌ Leaving stray temporary files. Clean up after yourself.
- ❌ Using terminal commands for file access. Use built-in tools (read, edit, write).
- ❌ Embarking on refactorings without prior planning and agreement.

### Developer Workflow

1. **Plan first.** Discuss architecture changes before implementing. Write a plan. Get agreement.
2. **Read the docs.** Follow cross-references in `.md` files. Understand the full picture before changing code.
3. **Follow the build scoping rules.** Don't rebuild sysroots if you only changed Go code.
4. **Test both paths.** Native Go test + `go run . <proj> -t` should agree.

### Pre-Flight Checklist for Changes

- [ ] Does it break the **async-only** rule?
- [ ] Does it require changes to the overlay or sysroot?
- [ ] Does it need satellite process changes?
- [ ] Does it affect the overlapped polling contract?
- [ ] Does it change the vegetable file format?
- [ ] Does it need a new host ABI function?
- [ ] Are there existing tests? Do they pass?
- [ ] Does the fallback mechanism handle the failure case?
- [ ] Is the happy path still untouched? (No pre-flight probes.)

---

## Appendices

### A. Related Documentation

| Document | Location | Covers |
|----------|----------|--------|
| Architecture Manifesto | `ARCHITECTURE_MANIFESTO.md` | Design principles, axioms, commitments |
| Agents Rules | `mohabbat/docs/AGENTS.md` | Agent development constraints |
| Callbacks Spec | `mohabbat/docs/CALLBACKS.md` | Cross-island invocation architecture |
| DLL/FFI | `mohabbat/docs/DLL.md` | Dynamic library handling (legacy) |
| Build Speed | `mohabbat/docs/FAST.md` | Output buffering, concurrency |
| Network ABI | `mohabbat/docs/NETWORK.md` | Full network ABI spec |
| Rust Fallback | `mohabbat/docs/RUSTUP.md` | Failure-driven rust toolchain fallback |
| Testing | `mohabbat/docs/TEST.md` | Unit testing in WASM |
| Compression | `mohabbat/docs/ZSTD.md` | Brotli → Zstd migration |

### B. Key Environment Variables

| Variable | Effect |
|----------|--------|
| `MOHABBAT_USE_NODE=1` | Force Node.js WASM host on any platform |
| `RUST_SRC_PATH` | Path to rust-src library sources (for build-std) |
| `GOROOT` | Go installation root |
| `GOMODCACHE` | Go module cache |

### C. Quick Reference Card

```
┌─────────────────────────────────────────────────────────┐
│  📦 Build vegetable ... go run . demo -o demo.bat       │
│  🏃 Dev run ............ go run . demo -r               │
│  🧪 Test ............... go run . demo-go -t            │
│  🔄 Refill ............. mohab.bat demo.bat --refill    │
│  📢 Verbose ............ go run . demo -r -v            │
│  🅿️ Platform force ..... ./demo.bat --platform linux-amd64│
│  🟢 Node fallback ...... MOHABBAT_USE_NODE=1 ./demo.bat │
└─────────────────────────────────────────────────────────┘
```