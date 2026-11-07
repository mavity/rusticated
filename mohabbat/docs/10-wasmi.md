# WASMI Redesign and Unified Execution Topology

The WASMI redesign propels forth the core unification goal: **`facade.wasm` is the single, universal implementation of the `"rusticated"` platform across all targets** (Native Desktop, Node.js, and Browser).

Below is the revised, considered and eloquently unified specification where **every host** (Native `microhost`, Node.js, and Browser `index.js`) implements only the `"narrow"` ABI, and **`facade.wasm` owns 100% of the `"rusticated"` logic everywhere**.



## 1. Unified Execution Topology Across All Hosts

In every environment—Native Desktop (`wasmi`), Node.js (`WebAssembly` + N-API / JS bridge), and Browser (`WebAssembly` + `eval`)—the two-module guest stack is identical:

```text
┌────────────────────────────────────────────────────────────────────────────┐
│                brain.wasm (Go / Rust Userland Application)                 │
│     Imports ONLY: "rusticated" ABI (sys_open, sys_read, sys_net_*, ...)    │
└─────────────────────────────────────┬──────────────────────────────────────┘
                                      │ Cross-Module WASM Link
                                      ▼
┌────────────────────────────────────────────────────────────────────────────┐
│             facade.wasm (Universal Rusticated OS Kernel in WASM)           │
│     Exports: "rusticated" ABI   |   Imports ONLY: "narrow" ABI             │
│                                                                            │
│  • Owns VFS, path resolution, O_TRUNC/O_APPEND, fd tables, overlapped I/O  │
│  • Owns PlatformInfo, [16]MemorySlot, signal routing (27/28), env, cwd     │
│  • Dispatches to backend driver by NarrowHostInfo.os_id:                   │
│      ├─ Native (1=Linux, 2=macOS, 3=Win): Machine-code trampolines + dlsym │
│      ├─ Node.js (4): N-API dlopen/call OR JS eval bridge                   │
│      └─ Browser (5): JS eval bridge (IndexedDB, vscode.workspace.fs, etc.) │
└─────────────────────────────────────┬──────────────────────────────────────┘
                                      │ 10-Function "narrow" ABI
                                      ▼
┌────────────────────────────────────────────────────────────────────────────┐
│                   Thin Host Runner (Micro-Host / JS Host)                  │
│  • Native Zone B: Rust microhost (wasmi + ruzstd + 10 narrow functions)    │
│  • Node.js / Browser: ~150 LOC JS loader (WebAssembly + 10 narrow funcs)   │
└────────────────────────────────────────────────────────────────────────────┘

```

### What This Eliminates

1. Kills the 1,600-line `mohabbat/washmhost/js/browser/index.js` implementation: All file descriptor tables, path normalization (`resolvePath`), `O_TRUNC`/`O_APPEND` offset state, `marshalStat`, `PlatformInfo` binary packing, and `overlapped` flag marshalling move into `facade.wasm` once, written in Rust.


2. Kills all 5 Go `washmhost` binaries in Zone C: Replaced by the single ~200 KB `facade.wasm`.


3. Eliminates `sys_dylib_*` from the platform: As already established, `dload` is an `ai`-internal child-process side-load, and in-browser AI / HTTP uses `/dev/eval`. `facade.wasm` has zero `sys_dylib_*` surface.



---

## 2. Complete Specification: The Narrow ABI (`"narrow"` Namespace)

To support `facade.wasm` without the unmoveable-memory optimization (using explicit copy/alloc primitives across the boundary), the `"narrow"` import namespace consists of the **10 host primitives** plus **2 guest-memory bridge primitives** (`read_brain` / `write_brain`) so `facade.wasm` can read and write `brain.wasm`'s buffers and `overlapped` structs.

### 2.1 WASM Import Signatures (`"narrow"`)

```wat
(module
  ;; 1. Host Platform Discovery
  (import "narrow" "host_info"      (func $host_info      (param i32) (result i32)))

  ;; 2-5. Native / Host Heap Memory & Data Transfer
  (import "narrow" "alloc_native"   (func $alloc_native   (param i64 i32) (result i64)))
  (import "narrow" "free_native"    (func $free_native    (param i64 i64)))
  (import "narrow" "read_native"    (func $read_native    (param i64 i32 i32) (result i32)))
  (import "narrow" "write_native"   (func $write_native   (param i32 i64 i32) (result i32)))

  ;; 6-8. Dynamic Library / Host Capability Resolution
  (import "narrow" "load_library"   (func $load_library   (param i32 i32) (result i64)))
  (import "narrow" "get_symbol"     (func $get_symbol     (param i64 i32 i32) (result i64)))
  (import "narrow" "free_library"   (func $free_library   (param i64) (result i32)))

  ;; 9-10. Page Protection & Execution
  (import "narrow" "protect_page"   (func $protect_page   (param i64 i64 i32) (result i32)))
  (import "narrow" "call_native"    (func $call_native    (param i64) (result i64)))

  ;; 11-12. Cross-Module Memory Bridge (facade.wasm <-> brain.wasm)
  (import "narrow" "read_brain"     (func $read_brain     (param i32 i32 i32) (result i32)))
  (import "narrow" "write_brain"    (func $write_brain    (param i32 i32 i32) (result i32)))
)

```

---

### 2.2 Exact Semantics Across All Three Host Environments

| Function | Native `microhost` (`wasmi`) | Node.js Host (`starter.js`)

 | Browser Host (`index.js`)

 |
| --- | --- | --- | --- |
| **`host_info(ptr)`** | `os_id`: `1`=Linux, `2`=macOS, `3`=Win; `arch_id`: `1`=x64, `2`=arm64; `page_size`; `pid`. | `os_id`: `4` (or native OS if using N-API addon); `arch_id`: `1`/`2`/`3`; `pid`: `process.pid`. | `os_id`: `5` (Browser); `arch_id`: `3` (`wasm32`); `page_size`: `65536`; `pid`: `3`. |
| **`alloc_native(sz, al)`** | `mmap` (POSIX) or `VirtualAlloc` / `HeapAlloc` (Win) returning 64-bit address. | Allocates `Uint8Array(sz)` in JS host map, returns 64-bit slot ID (or raw pointer via N-API). | Allocates `Uint8Array(sz)` in a `Map<bigint, Uint8Array>`, returns 64-bit handle ID. |
| **`free_native(ptr, sz)`** | `munmap` / `VirtualFree` / `HeapFree`. | Deletes slot ID from JS `Map` (or frees via N-API). | Deletes handle ID from JS `Map`. |
| **`read_native(src, dst, len)`** | `ptr::copy_nonoverlapping(src as *const u8, wasm_mem + dst, len)`. | Copies bytes from JS buffer `src` into `facadeMemory.buffer` at `dst`. | Copies bytes from JS buffer `src` into `facadeMemory.buffer` at `dst`. |
| **`write_native(src, dst, len)`** | `ptr::copy_nonoverlapping(wasm_mem + src, dst as *mut u8, len)`. | Copies bytes from `facadeMemory.buffer` at `src` into JS buffer `dst`. | Copies bytes from `facadeMemory.buffer` at `src` into JS buffer `dst`. |
| **`load_library(p, l)`** | `dlopen` (POSIX) / `LoadLibraryW` (Win). | Opens `"eval"` (`0xEEEE0001n`) or loads native library via built-in `process.dlopen` / N-API. | Recognizes `"eval"` and returns virtual handle `0xEEEE0001n`.

 |
| **`get_symbol(h, n, l)`** | `dlsym` (POSIX) / `GetProcAddress` (Win). | For `"eval"`, returns `0xEEEE0002n` (or resolves symbol via N-API).

 | For `(0xEEEE0001n, "eval")`, returns `0xEEEE0002n`.

 |
| **`free_library(h)`** | `dlclose` (POSIX) / `FreeLibrary` (Win). | No-op for `"eval"` (`0`); `dlclose` for native. | Returns `0`. |
| **`protect_page(p, s, pr)`** | `mprotect` (+ `sys_icache_invalidate` on macOS) / `VirtualProtect` (Win). | Returns `0` in JS mode; `mprotect`/`VirtualProtect` in N-API mode. | No-op in Browser (`returns 0`). |
| **`call_native(ptr)`** | Jumps to `transmute::<usize, extern "C" fn() -> i64>(ptr)()`. | Executes `(0, eval)` on the `CallFrame` buffer in JS mode, or jumps to native code in N-API mode. | Reads `CallFrame` from JS buffer `ptr`, runs `(0, eval)(code)`, stores result/Promise status into `CallFrame`.

 |
| **`read_brain(b, f, l)`** | Copies `l` bytes from `brain.wasm` memory at `b` to `facade.wasm` memory at `f`. | `new Uint8Array(facadeMem).set(new Uint8Array(brainMem, b, l), f)` | `new Uint8Array(facadeMem).set(new Uint8Array(brainMem, b, l), f)` |
| **`write_brain(f, b, l)`** | Copies `l` bytes from `facade.wasm` memory at `f` to `brain.wasm` memory at `b`. | `new Uint8Array(brainMem).set(new Uint8Array(facadeMem, f, l), b)` | `new Uint8Array(brainMem).set(new Uint8Array(facadeMem, f, l), b)` |

---

## 3. How `facade.wasm` Drives Browser & Node.js Over `"narrow"`

Because `facade.wasm` is the single implementation of `"rusticated"` across all platforms:

### 3.1 Browser (`os_id == 5`) and Pure-JS Node.js (`os_id == 4`)

Instead of `index.js` implementing 30+ `"rusticated"` syscalls in JavaScript, `index.js` shrinks to **~120 lines of code** that:

1. Decompresses `facade.wasm` and `brain.wasm` from the vegetable pool.


2. Instantiates `facade.wasm` with the 12 `"narrow"` imports.
3. Instantiates `brain.wasm` with its `"rusticated"` imports wired directly to `facade.wasm`'s exports.

At `facade.wasm` startup when `os_id == 4` or `os_id == 5`:

1. `facade.wasm` calls `load_library("eval")` $\rightarrow$ `0xEEEE0001` and `get_symbol(0xEEEE0001, "eval")` $\rightarrow$ `0xEEEE0002`.


2. `facade.wasm` allocates a `CallFrame` in host memory via `alloc_native` and smuggles its own minimal JS storage/DOM/Node adapter into `globalThis.__facade_io` via `call_native` (using `(0, eval)`):


* **In Browser (`os_id == 5`):** `facade.wasm` owns the entire VFS state machine, file descriptor table, `O_TRUNC`/`O_APPEND` logic, `marshalStat`, `PlatformInfo` layout, and `/dev/eval` device. It uses its smuggled JS snippets solely to persist blocks to `IndexedDB` / `vscode.workspace.fs` and push/pull terminal stdin/stdout bytes!


* **In Node.js (`os_id == 4`):** `facade.wasm` invokes Node's built-in `fs`, `net`, `tls`, and `tty` modules through its smuggled JS bridge (or loads the native N-API 30-line bridge via `process.dlopen` to use raw machine code identically to native desktop).



### 3.2 Native Desktop (`os_id == 1, 2, 3`)

At `facade.wasm` startup when `os_id` is Linux (`1`), macOS (`2`), or Windows (`3`):

1. `facade.wasm` allocates one executable page via `alloc_native(page_size, page_size)`, writes the ~64-byte architecture-specific **Universal Dispatcher Stub** (`x86_64` SysV, `x86_64` Win64, or `aarch64`), and calls `protect_page(..., PROT_READ | PROT_EXEC)` once.
2. `facade.wasm` calls `load_library` + `get_symbol` to resolve the host OS functions (`open`, `read`, `write`, `close`, `epoll_wait` / `kevent` / `GetQueuedCompletionStatusEx`, `pthread_create` / `_beginthreadex`, `getaddrinfo`, and native OS TLS cert verification via `Security.framework` / `crypt32.dll` / `libssl`).


3. Every `"rusticated"` syscall from `brain.wasm` is handled by the **exact same Rust state machine inside `facade.wasm**`, dispatching the low-level OS call through `write_native(CallFrame)` + `call_native(universal_stub)`.

---

## 4. Concrete Size & Performance Impact Across the Platform

| Metric | Legacy (`brot` + 5 Go `washmhost`s + fat `index.js`)

 | Unified (`microhost` + `facade.wasm` + thin `index.js`) |
| --- | --- | --- |
| **Zone B (5 Native Binaries)** | 5 × `brot` (~200 KB total)

 | 5 × `microhost` (`wasmi` + `ruzstd` + 12 narrow funcs: ~250 KB each, ~1.25 MB total) |
| **Zone C (Compressed Pool)** | 5 × Go `washmhost` (~40 MB uncompressed) + `brain.wasm`

 | **1 × `facade.wasm` (~200 KB)** + `brain.wasm` (0 native binaries in Zone C) |
| **Browser / Node JS Host** | ~1,630 LOC in `washmhost/js/browser/index.js` + `starter.js` duplicating `washmhost` logic

 | **~120 LOC thin `"narrow"` loader**; 100% of `"rusticated"` logic shared via `facade.wasm` |
| **Desktop Cold-Start** | Decompress 40 MB $\rightarrow$ write temp binary $\rightarrow$ `execve`/`CreateProcessW` $\rightarrow$ Go init (~80–350 ms)

 | Decompress `facade.wasm` + `brain.wasm` in RAM $\rightarrow$ `wasmi` in-process start (**< 10 ms**) |
| **Process Tree & Watchdogs** | 2 processes (`brot` + `washmhost`) + 3-layer Ghost Process watchdog

 | **1 single process**; zero temp files, zero `execve`, zero Ghost Process watchdog needed |
| **Bug / Feature Parity** | Every bug (`O_TRUNC`, `SIGWINCH` 27 vs 28, `[16]MemorySlot`) had to be fixed twice (in Go `washmhost` AND JS `index.js`)

 | **Fixed once in `facade.wasm**`; automatically identical across Linux, macOS, Windows, Node.js, and Browser |

---

## 5. Implementation Structure

```text
mohabbat/
├── microhost/                  # Zone B: Native #![no_std] Rust runner (~250 KB/target)
│   ├── Cargo.toml              # wasmi (no_std), ruzstd
│   └── src/
│       ├── main.rs             # Slices Zone C, decompresses facade.wasm + brain.wasm in RAM
│       ├── narrow.rs           # 10 "narrow" primitives + read_brain/write_brain (~250 LOC)
│       ├── sys_unix.rs         # mmap, mprotect, dlopen, dlsym, dlclose
│       └── sys_windows.rs      # VirtualAlloc, VirtualProtect, LoadLibraryW, GetProcAddress
├── host-js/                    # Universal ~120 LOC JS Narrow Host (Browser & Node.js)
│   └── narrow_host.js          # Implements the exact same 12 "narrow" imports + "eval" dylib
└── facade/                     # Zone C: Universal Rusticated Kernel (wasm32-unknown-unknown)
    ├── Cargo.toml              # #![no_std] + alloc
    └── src/
        ├── lib.rs              # Exports all "rusticated" ABI functions to brain.wasm
        ├── narrow_sys.rs       # Raw imports from "narrow"
        ├── core/               # Shared OS-agnostic state (fd table, VFS overlay, overlapped, signals)
        └── pal/                # Platform Abstraction Layer dispatched by NarrowHostInfo.os_id
            ├── native_trampoline.rs # x86_64 (SysV/Win64) & aarch64 universal CallFrame stubs
            ├── posix.rs             # Linux (os_id=1) & macOS (os_id=2) via libc/libSystem symbols
            ├── windows.rs           # Windows (os_id=3) via kernel32/ws2_32/crypt32 symbols
            └── js_eval.rs           # Node.js (os_id=4) & Browser (os_id=5) via "eval" symbol

```

This keeps the boundary strict: **hosts only ever implement `"narrow"**`, and **`facade.wasm` is the sole implementation of `"rusticated"` across Desktop, Node.js, and Browser.**

### 6. How the Facade Triggers and Loads the Brain

Instead of handing control insecurely or introducing circular boot dependencies, the boot sequence follows a strict kernel-bootstrapping model:

1. **Host Decompression:** The native `microhost` (Zone B) decompresses Zone C, holding raw binary blobs for both `facade.wasm` and `brain.wasm` in RAM.
2. **Facade Initialization:** The host instantiates `facade.wasm` first, providing it with the **Narrow ABI** and a pointer to the raw `brain.wasm` binary payload in memory.
3. **Kernel Bootstrapping:** `facade.wasm` acts as the WebAssembly-based OS kernel. It parses, validates, and instantiates `brain.wasm` (using its internal WASM compilation/linking logic or via host-assisted instantiation), binds the `"rusticated"` import namespace, and invokes `brain.wasm`'s entry point (`_start` / `run`).

---

### 2. Handling Synchronous Local I/O & Blocking OS Calls

Local file access, standard I/O (stdin/stdout/stderr), and certain OS system calls are notoriously synchronous and blocking on Linux and Windows. Because WebAssembly execution within an instance is single-threaded, executing a blocking file read directly on the guest thread would freeze the entire application.

To solve this without bloating the Narrow ABI, **`facade.wasm` orchestrates a native background worker threadpool entirely via raw machine code and dynamic symbol resolution.**

#### The Native Worker Threadpool Architecture

1. **Spawning Native Threads at Startup:**
* During initialization, `facade.wasm` uses its Narrow ABI privileges (`get_symbol`) to resolve native threading symbols from the host OS (`pthread_create` on POSIX, `_beginthreadex` on Windows).
* It allocates an executable worker stub and spawns **2 to 4 native OS background worker threads** directly in the host process address space, bypassing Go or JS runtimes entirely.


2. **Shared Ring-Buffer & Overlapped Queue:**
* `facade.wasm` maintains a lockless or mutex-protected **I/O Request Ring-Buffer** in shared native memory (`alloc_native`).
* When `brain.wasm` issues an asynchronous file operation (e.g., `sys_read` on a file handle) via the `"rusticated"` ABI, `facade.wasm` packages the request and pushes it to the native ring-buffer.


3. **Non-Blocking Return:**
* `facade.wasm` immediately marks the guest's `overlapped` structure as pending and returns control back to `brain.wasm`, allowing userland green threads (goroutines) to continue executing.


4. **Worker Execution & Completion:**
* One of the native background worker threads picks up the request from the ring-buffer, executes the blocking OS system call (`read()` / `WriteFile()` / `stat()`), writes the result directly into the designated buffer memory, and updates the completion flag.
* On the next guest poll tick or yield cycle, `facade.wasm` detects the completed native operation and resumes the waiting guest task.



---

### Summary of Responsibilities

* **`microhost` (Zone B):** Provides raw OS primitives (`mmap`, `mprotect`, `dlopen`, `dlsym`, `call_native`) and holds initial bytes. Contains zero business logic.
* **`facade.wasm` (Zone C Kernel):** Owns the virtual filesystem state, file descriptor tables, path resolution, `overlapped` completion tracking, and spawns/manages the native background threadpool for blocking local file and stdio operations.
* **`brain.wasm` (Zone C Userland):** Runs user code (like Kabibi) purely asynchronously against the `"rusticated"` ABI, completely agnostic of whether underlying file I/O is handled by non-blocking polling or offloaded native worker threads.