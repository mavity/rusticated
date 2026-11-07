### 1. Executive Assessment: Will `wasmi` + the 10-Function Narrow ABI Deliver Massive Gains?

**Yes.** Even before applying the unmoveable direct-memory optimization—retaining explicit `host_info`, `alloc_native`, `free_native`, `read_native`, and `write_native` calls across the boundary—replacing the `brot` + `washmhost` (Go/Wazero) two-process architecture with an in-process Rust `microhost` (`wasmi` + `ruzstd` + 10-function Narrow ABI) and `facade.wasm` delivers an order-of-magnitude reduction in binary size and startup latency.

#### A. Binary Footprint Reduction (Zone B & Zone C)

In the legacy architecture:

* **Zone B (`brot` Table):** Five `#![no_std]` Rust loaders (~30–50 KB each = ~200 KB total).


* **Zone C (Compressed Pool):** Five complete Go binaries (`washmhost` for `linux-amd64`, `linux-arm64`, `windows-amd64`, `windows-arm64`, `darwin-arm64`) plus `brain.wasm`. Each `washmhost` embeds the Go runtime, garbage collector, standard library (`net`, `os`, `crypto/tls`, `x509`), and the Wazero runtime (~6–10 MB uncompressed per target; ~35–45 MB total uncompressed before pool compression).



In the **Micro-Host + Narrow ABI Architecture**:

* **Zone B (`microhost` Table):** Each of the five native binaries is `brot` upgraded with `wasmi` (a `#![no_std]`-compatible pure-Rust interpreter), `ruzstd`, and the 10 Narrow ABI functions. A stripped `#![no_std]` Rust binary containing `wasmi` + `ruzstd` compiles to **~220 KB – 280 KB per target** (~1.25 MB total across all 5 targets).
* **Zone C (Compressed Pool):** Contains **zero native host binaries**. It holds only `facade.wasm` (~150–300 KB uncompressed) + `brain.wasm`.
* **Net Size Win:** Eliminates ~35–45 MB of uncompressed Go host binaries from Zone C, shrinking the compressed `.bat` vegetable baseline by **8–14 MB** and reducing build/packaging times because 5 Go cross-compilations of `washmhost` are eliminated.



#### B. Cold-Start & Execution Performance

In the legacy architecture, every invocation of a `.bat` vegetable executes a heavy multi-stage boot sequence:

1. `brot` decompresses tens of megabytes of `washmhost` binaries + `brain.wasm`.


2. `brot` writes the target `washmhost` executable and `brain.wasm` to temporary files on disk.


3. `brot` calls `fork`+`execve` (Linux/macOS) or `CreateProcessW` (Windows) to launch `washmhost`, inducing OS process creation and Windows Defender / macOS Gatekeeper scan penalties on the newly written temp executable.


4. `washmhost` initializes the Go runtime/GC, reads `brain.wasm` from disk, and compiles/instantiates it.


5. `brot` and `washmhost` maintain a 3-layer Ghost Process watchdog (`prctl`, `CreateJobObjectW`, parent PID polling).



In the **Micro-Host + Narrow ABI Architecture**:

1. **Zero Temp Files & Zero Process Spawning:** `microhost` decompresses `facade.wasm` and `brain.wasm` directly into RAM and executes them **in-process**. No `execve`, no `CreateProcessW`, no disk I/O, no antivirus scan delay on freshly written binaries, and **zero Ghost Process watchdog machinery**.


2. **Sub-5ms Cold Start:** `wasmi`'s lazy translation/validation instantiates modules in `< 2 ms`. Cold start drops from `80–350 ms` (disk write + process spawn) to **`< 10 ms`**.
3. **Runtime Overhead of Explicit `read_native`/`write_native`:** Because `rusticated` is an I/O-orchestration ABI (file I/O, network sockets, TLS verification, DNS), the cost of `memcpy` inside `read_native` and `write_native` (operating at ~20–40 GB/s in L1/L2 cache) is negligible compared to OS kernel syscall and network latencies.



---

### 2. System Architecture & Execution Topology

```text
┌────────────────────────────────────────────────────────────────────────────┐
│                         🍆 VEGETABLE (.bat)                                │
├────────────────────────────────────────────────────────────────────────────┤
│ Zone A: Polyglot Shell/Batch Header (Slices Zone B & Zone C by offset)     │
│ Zone B: 5 × Native Micro-Host Runners (~250 KB each, Rust, no-CRT)         │
│ Zone C: Single Zstd Stream -> [ facade.wasm ] + [ brain.wasm ] + [payload] │
└─────────────────────────────────────┬──────────────────────────────────────┘
                                      │ In-Process Decompression (RAM only)
                                      ▼
┌────────────────────────────────────────────────────────────────────────────┐
│                    SINGLE PROCESS: Native Micro-Host                       │
│                                                                            │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ Guest Instance 2: brain.wasm (Go/Rust Userland App, e.g. Kabibi)     │  │
│  │   Imports: "rusticated" (sys_read, sys_write, sys_net_open, ...)     │  │
│  └──────────────────────────────────┬───────────────────────────────────┘  │
│                                     │ Host-Routed Cross-Module Call        │
│                                     ▼                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ Guest Instance 1: facade.wasm (Rusticated OS Kernel in WASM)         │  │
│  │   Exports: "rusticated" ABI  |  Imports: "narrow" (10 functions)     │  │
│  │   • Emits raw x86_64 / arm64 machine-code trampolines                │  │
│  │   • Manages async worker threads, epoll/kqueue/IOCP & overlapped I/O │  │
│  └──────────────────────────────────┬───────────────────────────────────┘  │
│                                     │ 10-Function "narrow" ABI             │
│                                     ▼                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ Micro-Host Engine (wasmi + ruzstd + 10 Narrow Primitives)            │  │
│  │   • Zero C-FFI marshalling, zero knowledge of OS syscalls            │  │
│  │   • Exposes alloc/free/read/write, dlopen/dlsym, mprotect, call      │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────────┘

```

---

### 3. Complete Specification: The 10-Function Narrow ABI (`"narrow"` Namespace)

All functions are imported by `facade.wasm` under the module namespace `"narrow"`. All native pointers and handles are represented as 64-bit integers (`i64` / `u64`). All WASM pointers and lengths are 32-bit integers (`i32` / `u32`).

#### 3.1 ABI Signatures

```wat
(module
  ;; 1. Host Platform & Memory Discovery
  (import "narrow" "host_info"      (func $host_info      (param i32) (result i32)))

  ;; 2-5. Native Heap Memory Management & Copy Primitives
  (import "narrow" "alloc_native"   (func $alloc_native   (param i64 i32) (result i64)))
  (import "narrow" "free_native"    (func $free_native    (param i64 i64)))
  (import "narrow" "read_native"    (func $read_native    (param i64 i32 i32) (result i32)))
  (import "narrow" "write_native"   (func $write_native   (param i32 i64 i32) (result i32)))

  ;; 6-8. In-Process Dynamic Library Resolution
  (import "narrow" "load_library"   (func $load_library   (param i32 i32) (result i64)))
  (import "narrow" "get_symbol"     (func $get_symbol     (param i64 i32 i32) (result i64)))
  (import "narrow" "free_library"   (func $free_library   (param i64) (result i32)))

  ;; 9-10. Page Protection & Raw Machine Code Invocation
  (import "narrow" "protect_page"   (func $protect_page   (param i64 i64 i32) (result i32)))
  (import "narrow" "call_native"    (func $call_native    (param i64) (result i64)))
)

```

#### 3.2 Detailed Function Contracts

##### 1. `host_info(info_wasm_ptr: i32) -> i32`

Writes a fixed 32-byte `NarrowHostInfo` structure into the caller's WASM linear memory at `info_wasm_ptr`.

```rust
#[repr(C)]
pub struct NarrowHostInfo {
    pub os_id: u32,          // 1 = Linux, 2 = macOS, 3 = Windows, 4 = Node.js, 5 = Browser
    pub arch_id: u32,        // 1 = x86_64, 2 = aarch64, 3 = wasm32
    pub page_size: u32,      // Host OS virtual memory page size in bytes (4096 or 16384)
    pub pid: u32,            // Host OS process ID
    pub reserved: [u64; 2],  // Reserved (zeroed; future slot for unmoveable base pointer)
}

```

* **Return:** `0` on success, `-1` if `[info_wasm_ptr .. info_wasm_ptr + 32]` is out of bounds.

##### 2. `alloc_native(size: i64, align: i32) -> i64`

Allocates `size` bytes of zero-initialized memory in the host's native 64-bit address space with alignment `align` (power of two).

* **Implementation:**
* If `align >= page_size` or `size >= page_size`, uses OS page allocation (`mmap(MAP_ANON|MAP_PRIVATE)` on POSIX; `VirtualAlloc(MEM_RESERVE|MEM_COMMIT, PAGE_READWRITE)` on Windows) so the returned region can later be passed to `protect_page`.
* For smaller alignments, uses the host heap allocator (`HeapAlloc` on Windows, `mmap`-backed arena on POSIX).


* **Return:** 64-bit native address on success, `0` on allocation failure.

##### 3. `free_native(nat_ptr: i64, size: i64)`

Frees a native memory block previously allocated by `alloc_native`. No-op if `nat_ptr == 0`.

##### 4. `read_native(nat_src: i64, wasm_dst: i32, len: i32) -> i32`

Copies `len` bytes from native memory address `nat_src` into the caller's WASM linear memory at `[wasm_dst .. wasm_dst + len]`.

* **Return:** `0` on success, `-1` if WASM destination range is out of bounds or `nat_src == 0`.

##### 5. `write_native(wasm_src: i32, nat_dst: i64, len: i32) -> i32`

Copies `len` bytes from the caller's WASM linear memory at `[wasm_src .. wasm_src + len]` into native memory address `nat_dst`.

* **Return:** `0` on success, `-1` if WASM source range is out of bounds or `nat_dst == 0`.

##### 6. `load_library(path_ptr: i32, path_len: i32) -> i64`

Maps a dynamic shared library into the host process address space.

* Reads UTF-8 string `path` from `[path_ptr .. path_ptr + path_len]`.
* **Linux / macOS:** Calls `dlopen(c_path, RTLD_LAZY | RTLD_LOCAL)`. If `path_len == 0`, calls `dlopen(NULL, RTLD_LAZY)` to return the global process symbol handle.
* **Windows:** Converts UTF-8 to UTF-16LE and calls `LoadLibraryW(w_path)`.
* **Return:** Opaque 64-bit handle (`HMODULE` or `void*`), or `0` on failure.

##### 7. `get_symbol(lib_handle: i64, name_ptr: i32, name_len: i32) -> i64`

Resolves a symbol name from `lib_handle`.

* Reads ASCII/UTF-8 symbol name from `[name_ptr .. name_ptr + name_len]`, appends NUL terminator on stack.
* **Linux / macOS:** Calls `dlsym(lib_handle as *mut c_void, c_name)`.
* **Windows:** Calls `GetProcAddress(lib_handle as HMODULE, c_name)`.
* **Return:** 64-bit native memory address of the function or exported data variable, or `0` if not found.

##### 8. `free_library(lib_handle: i64) -> i32`

Decrements reference count of `lib_handle` via `dlclose` (POSIX) or `FreeLibrary` (Windows).

* **Return:** `0` on success, `-1` on error.

##### 9. `protect_page(nat_ptr: i64, size: i64, prot: i32) -> i32`

Changes hardware page permissions on page-aligned native memory `[nat_ptr .. nat_ptr + size]`.

* `prot` bitmask:
* `1` (`PROT_READ`)
* `2` (`PROT_WRITE`)
* `4` (`PROT_EXEC`)


* **Linux:** `mprotect(nat_ptr, size, flags)`.
* **macOS (Apple Silicon W^X Enforcement):** Calls `mprotect(nat_ptr, size, flags)` and invokes `sys_icache_invalidate(nat_ptr, size)` when `PROT_EXEC` is set.
* **Windows:** Translates bitmask to `PAGE_READWRITE` (`3`), `PAGE_EXECUTE_READ` (`5`), or `PAGE_EXECUTE_READWRITE` (`7`), calls `VirtualProtect`, and invokes `FlushInstructionCache(GetCurrentProcess(), nat_ptr, size)` when `PROT_EXEC` is set.
* **Return:** `0` on success, negative OS error code on failure.

##### 10. `call_native(code_ptr: i64) -> i64`

Performs an indirect zero-argument function call to the native machine code located at `code_ptr`:

```rust
#[inline(always)]
unsafe fn call_native_impl(code_ptr: i64) -> i64 {
    let f: extern "C" fn() -> i64 = core::mem::transmute(code_ptr as usize);
    f()
}

```

* **Return:** Raw 64-bit integer returned in `rax` (`x86_64`) or `x0` (`aarch64`).

---

### 4. Cross-Module Bridging: How `brain.wasm` Calls `facade.wasm`

Because the Narrow ABI avoids direct WASM-to-WASM shared memory assumptions in this baseline phase, the `microhost` links `brain.wasm` and `facade.wasm` together inside the same `wasmi::Store` and provides a zero-privilege **Guest Memory Bridge** so `facade.wasm` can read/write `brain.wasm`'s buffers (including `overlapped` completion structures):

```wat
;; Imported by facade.wasm from the microhost ("narrow" namespace extension for guest access)
(import "narrow" "read_brain"  (func $read_brain  (param i32 i32 i32) (result i32)))
(import "narrow" "write_brain" (func $write_brain (param i32 i32 i32) (result i32)))

```

* `read_brain(brain_src_ptr: i32, facade_dst_ptr: i32, len: i32) -> i32`: Copies `len` bytes from `brain.wasm` linear memory to `facade.wasm` linear memory using `wasmi::Memory::data(&store)`.
* `write_brain(facade_src_ptr: i32, brain_dst_ptr: i32, len: i32) -> i32`: Copies `len` bytes from `facade.wasm` linear memory into `brain.wasm` linear memory (used to write read buffers and flip the `overlapped` completion flag).


* **Routing `rusticated` Imports:** Every `"rusticated"` import requested by `brain.wasm` (`sys_read`, `sys_write`, `sys_open`, `sys_net_open`, etc.) is directly bound by `wasmi::Linker` to the matching exported function of `facade.wasm`. When `brain.wasm` calls `sys_read(ovPtr, handle, bufPtr, bufLen)`, execution enters `facade.wasm` directly inside the same `wasmi::Store` with zero host intervention.



---

### 5. `facade.wasm` Architecture & Trampoline Codegen Engine

`facade.wasm` is a single Rust crate (`#![no_std]` + `alloc`, target `wasm32-unknown-unknown`) that replaces the entire Go `washmhost` codebase.

#### 5.1 Trampoline Execution Pattern (Zero Host FFI Logic)

To invoke any native OS function resolved via `get_symbol` (e.g., `open`, `read`, `write`, `CreateFileW`, `ReadFile`, `pthread_create`), `facade.wasm` allocates a persistent trampoline page via `alloc_native(page_size, page_size)` and uses a parameterized **Call Frame Block** in native memory:

```rust
#[repr(C)]
pub struct CallFrame {
    pub target_fn: u64,   // +0x00: Address of OS function from get_symbol()
    pub args: [u64; 8],   // +0x08: Up to 8 64-bit arguments
    pub ret_val: u64,     // +0x48: Return value (rax / x0)
    pub errno_val: i32,   // +0x50: Captured thread-local errno / GetLastError()
    pub _pad: i32,
}

```

At startup, `facade.wasm` checks `NarrowHostInfo.arch_id` and `os_id`, emits a single reusable **Universal Dispatcher Trampoline** (~64 bytes of machine code) into the executable page, and calls `protect_page(exec_page, page_size, PROT_READ | PROT_EXEC)` **once**.

1. **`x86_64` System V ABI (Linux / macOS) Universal Stub:**
* Loads `CallFrame` base pointer (embedded as a 64-bit immediate in the stub).
* Moves `args[0..5]` into `rdi, rsi, rdx, rcx, r8, r9`, pushes `args[6..7]` onto aligned stack, calls `[target_fn]`, stores `rax` into `ret_val`, calls `__errno_location` / `__error`, stores `errno` into `errno_val`, and returns `rax`.


2. **`x86_64` Microsoft x64 ABI (Windows) Universal Stub:**
* Reserves 32-byte shadow space + stack arguments, moves `args[0..3]` into `rcx, rdx, r8, r9`, moves `args[4..7]` onto stack, calls `[target_fn]`, calls `GetLastError()`, stores into `errno_val`, and returns `rax`.


3. **`aarch64` AAPCS64 (Linux / macOS / Windows ARM64) Universal Stub:**
* Loads `args[0..7]` via `ldp` into `x0..x7`, branches with link (`blr`) to `target_fn`, stores `x0` into `ret_val`, captures `errno`/`GetLastError()`, and returns `x0`.



Because the stub is universal, **Subsequent OS calls do not call `protect_page`!** To execute any native function:

1. `facade.wasm` writes the `CallFrame` struct (72 bytes) into native memory via `write_native`.
2. `facade.wasm` calls `call_native(universal_stub_ptr)`.
3. `facade.wasm` reads `ret_val` and `errno_val` (if needed) via `read_native`.

#### 5.2 Asynchronous Overlapped I/O Without Blocking WASM

`rusticated` mandates that all I/O is asynchronous via guest-owned `overlapped` structs. Because WebAssembly is single-threaded per instance, `facade.wasm` implements true non-blocking I/O over the Narrow ABI using two mechanisms:

1. **Non-Blocking Poll Reactor (File, TTY, TCP Sockets):**
* Sockets and pipes are opened with `O_NONBLOCK` (POSIX) or `ioctlsocket(FIONBIO)` / Overlapped `WSASocketW` (Windows).
* When `brain.wasm` enters its idle poll / yield loop (`sys_poll` / `sched_yield`), `facade.wasm` runs a single batch poll (`epoll_wait` on Linux, `kevent` on macOS, `GetQueuedCompletionStatusEx` on Windows), transfers completed native buffers into `brain.wasm` via `write_brain`, and flips the `overlapped` completion bit.




2. **Native Background Worker Pool (Blocking FS, DNS `getaddrinfo`, Native TLS Verification):**
* Operations that lack non-blocking OS primitives (e.g., `getaddrinfo` for `sys_net_lookup`, `SecTrustEvaluateWithError` / `CertVerifyCertificateChainPolicy` for `sys_net_cert_verify`) are dispatched to a small ring-buffer queue in native memory serviced by 2–4 native OS threads spawned at `facade.wasm` startup via `pthread_create` / `_beginthreadex` (executing a small native worker loop emitted into the executable page).


* Workers never call back into `wasmi`; they simply execute the C function specified in the native ring buffer, write the result into the native slot, and set an atomic flag that `facade.wasm` harvests on the next poll tick.



---

### 6. Handling Out-of-Process Satellite Isolation (`sys_dylib_*`)

The Mohabbat architecture requires that dangerous third-party C dynamic libraries invoked by `brain.wasm` (such as `litert_lm_ext`) can run in an isolated **satellite** process so a native segfault does not kill the main application.

With the Micro-Host + `facade.wasm` architecture:

* **In-Process Fast Path:** Trusted OS libraries (`libc`, `libSystem`, `kernel32`, `ws2_32`, `crypt32`, `Security.framework`) are loaded directly in-process by `facade.wasm` via `narrow::load_library`.
* **Isolated Satellite Path (`sys_dylib_*`):** When `brain.wasm` calls `sys_dylib_open`, `facade.wasm` spawns a child instance of the current executable (`argv[0] --satellite`) over an anonymous pipe using its native process-spawn syscall (`posix_spawn` / `CreateProcessW`), binding `PR_SET_PDEATHSIG` / `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` exclusively to that optional satellite child.



---

### 7. Node.js & Browser Fallback Alignment

Every vegetable supports a Node.js fallback and Browser execution (`washmhost.js`):

1. **Browser (`washmhost.js`):**
* The browser already provides a native JS implementation of the `"rusticated"` ABI (`washmhost.js` with `AsyncIndexedDBFS`, `vscode.workspace.fs`, and `/dev/eval`).


* In the browser, `washmhost.js` simply skips `facade.wasm` and instantiates `brain.wasm` directly against the JS `"rusticated"` imports—zero change to browser compatibility!


2. **Node.js Fallback:**
* Can either continue instantiating `brain.wasm` directly against the JS `"rusticated"` host (sharing code with `washmhost.js`), **or** load a tiny prebuilt N-API `.node` addon (15 KB) exposing the 10 `"narrow"` functions so Node.js runs the exact same `facade.wasm` + `brain.wasm` pair.



---

### 8. Crate Layout & Implementation Plan

```text
mohabbat/
├── microhost/                  # Replaces brot/ (Zone B native binary, ~250 KB)
│   ├── Cargo.toml              # #![no_std], depends on wasmi (no_std), ruzstd
│   └── src/
│       ├── main.rs             # Entrypoint, slices Zone C, decompresses facade + brain
│       ├── narrow_abi.rs       # Implements the 10 "narrow" host functions (~250 LOC)
│       ├── sys_unix.rs         # mmap, mprotect, dlopen, dlsym, dlclose
│       └── sys_windows.rs      # VirtualAlloc, VirtualProtect, LoadLibraryW, GetProcAddress
└── facade/                     # Replaces washmhost/ (Zone C WASM kernel, ~200 KB)
    ├── Cargo.toml              # Crate target: wasm32-unknown-unknown
    └── src/
        ├── lib.rs              # Exports "rusticated" ABI functions to brain.wasm
        ├── narrow.rs           # Safe wrappers around the 10 "narrow" imports
        ├── trampoline/
        │   ├── mod.rs          # CallFrame & universal dispatcher lifecycle
        │   ├── x86_64.rs       # SysV & Win64 machine code emitters
        │   └── aarch64.rs      # ARM64 AAPCS64 machine code emitter
        ├── reactor.rs          # Non-blocking poll loop & native worker thread ring
        ├── env_file.rs         # sys_open, sys_read, sys_write, sys_stat, sys_list_dir
        ├── env_net.rs          # sys_net_open, sys_net_accept, sys_net_lookup, sys_net_cert_verify
        └── env_dylib.rs        # sys_dylib_* satellite manager

```
