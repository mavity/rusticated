# `dload` Migration Plan: Ejecting Dynamic Library Loading to Userland (`0-migration.md`)

## 1. Objective & Architectural Ground Truth

Dynamic library loading (`dylib_*`) does not belong in the Rusticated kernel/WASM host ABI:

1. **It poisoned `washmhost`'s static linking on Linux/Android:** Importing `github.com/ebitengine/purego` inside `mohabbat/washmhost` injected `//go:cgo_import_dynamic` directives for glibc (`libdl.so.2`, `libpthread.so.0`, `libc.so.6`) and forced Go's internal linker to emit `PT_INTERP = /lib/ld-linux*.so*`. On Android/Termux (and musl/distroless systems), `/lib/ld-linux*` does not exist, causing `brot`'s `execve(washmhost)` to fail immediately with `-ENOENT` (`-2`) before the WASM host even starts.
2. **Only userland `/ai` uses dynamic libraries:** Across the entire repository, 100% of `syscall.Dylib*` calls live in `ai/` (`ai/backend_rusticated.go`, `ai/tflite_rusticated.go`, `ai/eval_bridge_rusticated.go`, and `ai/assets/eval_bridge_rusticated.go`). Neither `kabibi`, `loch`, `demo`, `mohabbat`, nor the Go standard library uses `dylib_*`.
3. **Userland already has `process_spawn` + pipes + temp files:** A WASM guest running on `rusticated-go` can extract a tiny native satellite worker binary into `os.TempDir()` and spawn it via `os/exec.Command` over `stdin`/`stdout` pipes (`process_spawn`), speaking a framing protocol directly from userland Go without a single `dylib_*` import in the WASM host ABI.

### Target End-State

- **`rusticated-go` & `mohabbat/washmhost` are 100% DLL-free and `purego`-free:**
  - All 8 `env.dylib_*` WASM host imports (`dylib_open`, `dylib_sym`, `dylib_close`, `dylib_call`, `dylib_alloc`, `dylib_free`, `dylib_read_mem`, `dylib_write_mem`, `dylib_callback_register`) are removed from `rusticated-go`, `washmhost` (Go), `washmhost` (Node.js), and `washmhost` (Browser).
  - Every Linux `washmhost` binary (`linux-amd64`, `linux-arm64`, `linux-arm32`) is a **100% static ELF with zero `PT_INTERP` and zero `DT_NEEDED`**, executed directly by the Linux/Android kernel without invoking `/lib/ld-linux*` or `/system/bin/linker*`.
- **`/dload` is a self-contained hybrid Rust + Go userland package:**
  - **Rust worker (`dload` Cargo binary):** A tiny `#![no_std]` `#![no_main]` native satellite worker compiled into platform/ABI-specific flavours (Windows x64/ARM64, macOS ARM64, Linux glibc x64/ARM64/ARM32, Android Bionic ARM64/ARM32).
  - **Prebuild integration (`mohabbat/prebuild.go`):** Treated as a universal build dependency alongside the `rusticated-go` overlay. Every builder invocation triggers the Rust `dload` build, which completes near-instantly (Cargo no-op when unchanged) and stages the stripped binaries into `dload/embedded/`.
  - **Go package (`package dload` in `/dload`):** Embeds all built worker flavours via `//go:embed`, extracts the matching worker on demand into `os.TempDir()`, spawns it over `stdin`/`stdout` pipes, and exposes the exact Go API previously in `rusticated-go/syscall/dylib_rusticated.go`.
- **`/ai` relies exclusively on `/dload` (for native DLLs) and `/dev/eval` (for browser JS eval):**
  - `ai/backend_rusticated.go` and `ai/tflite_rusticated.go` switch from `syscall.Dylib*` to `dload.*`.
  - In the browser (`mohabbat/washmhost/js/browser/index.js`), the virtual `"eval"` DLL is replaced by a virtual filesystem device `/dev/eval` backed by the browser host's existing async `proxy` file handle (`path_open` + `write` + `read`).

---

## 2. Component 1: Rust `dload` Worker Flavours & Cross-Compilation

### 2.1 Why a Rust `#![no_std]` `#![no_main]` Worker?

Like `mohabbat/brot`, a `#![no_std]` `#![no_main]` Rust binary linked with `rust-lld`:
- Weighs **~15–35 KB** per flavour (so embedding all 8 flavours inside `/dload` adds < 200 KB total before vegetable zstd compression, and ~50 KB after zstd compression).
- Requires **no external C cross-compiler, NDK, or sysroot** on the build host: `rust-lld` links against tiny generated `.tbd` / `.a` / `.so` import stubs (extending the pattern already proven in `mohabbat/package.go` with `ensureBrotStubs` and `ensureDarwinStubs`).

### 2.2 Supported Worker Flavours

Each flavour is built into `dload/embedded/dload-<flavour>` (with `.dat` suffix on Windows to avoid Defender false-positive scans during build):

| Flavour ID | Rust Target Triple | Linker & Interpreter (`PT_INTERP`) | Dynamic Imports (`DT_NEEDED` / DLL) | Target Environment |
|---|---|---|---|---|
| `windows-amd64` | `x86_64-pc-windows-gnullvm` | `rust-lld` (`gnu-lld`) | `kernel32.dll` (`raw-dylib`) | Windows x64 (and ARM64 x64-emulation) |
| `windows-arm64` | `aarch64-pc-windows-gnullvm` | `rust-lld` (`gnu-lld`) | `kernel32.dll` (`raw-dylib`) | Windows ARM64 |
| `darwin-arm64` | `aarch64-apple-darwin` | `rust-lld` (`darwin-lld`) | `/usr/lib/libSystem.B.dylib` (`.tbd` stub) | macOS Apple Silicon |
| `linux-gnu-amd64` | `x86_64-unknown-linux-musl` | `rust-lld` (`--dynamic-linker=/lib64/ld-linux-x86-64.so.2`) | `libdl.so.2`, `libc.so.6` (ELF `.so` stubs) | Desktop/Server Linux x86_64 (glibc) |
| `linux-gnu-arm64` | `aarch64-unknown-linux-musl` | `rust-lld` (`--dynamic-linker=/lib/ld-linux-aarch64.so.1`) | `libdl.so.2`, `libc.so.6` (ELF `.so` stubs) | Desktop/Server Linux ARM64 (glibc) |
| `linux-gnu-arm32` | `armv7-unknown-linux-musleabihf` | `rust-lld` (`--dynamic-linker=/lib/ld-linux-armhf.so.3`) | `libdl.so.2`, `libc.so.6` (ELF `.so` stubs) | Desktop/Embedded Linux ARMv7 (glibc) |
| `linux-android-arm64` | `aarch64-unknown-linux-musl` | `rust-lld` (`--dynamic-linker=/system/bin/linker64`) | `libdl.so`, `libc.so` (ELF `.so` stubs) | Android / Termux 64-bit (`aarch64`) |
| `linux-android-arm32` | `armv7-unknown-linux-musleabihf` | `rust-lld` (`--dynamic-linker=/system/bin/linker`) | `libdl.so`, `libc.so` (ELF `.so` stubs) | Android / Termux 32-bit (`armv8l` / `armv7l`) |

### 2.3 Zero-NDK ELF Stub Generation for `linux-gnu-*` and `linux-android-*`

To cross-link dynamic ELF PIE binaries (`ET_DYN`) using `rust-lld` on Windows/macOS/Linux without installing a Linux glibc or Android NDK sysroot:
- During prebuild, a Go helper `ensureElfDloadStubs(stubDir)` generates minimal (< 1 KB) valid 32-bit and 64-bit ELF `ET_DYN` shared-object stub files containing only an ELF header, `.dynsym`, `.dynstr`, `.hash`, and `.dynamic` (`DT_SONAME`):
  - **Glibc stub dir:** `libdl.so` (`DT_SONAME = libdl.so.2`) exporting `dlopen`, `dlsym`, `dlclose`, `dlerror`; and `libc.so` (`DT_SONAME = libc.so.6`) exporting `malloc`, `free`, `read`, `write`, `_exit`.
  - **Android Bionic stub dir:** `libdl.so` (`DT_SONAME = libdl.so`) exporting `dlopen`, `dlsym`, `dlclose`, `dlerror`; and `libc.so` (`DT_SONAME = libc.so`) exporting `malloc`, `free`, `read`, `write`, `_exit`.
- When `dload` is linked with `-C relocation-model=pic -C link-arg=-pie -C link-arg=--dynamic-linker=<interp> -L <stubDir> -ldl -lc`, `rust-lld` records the exact `PT_INTERP` and `DT_NEEDED` entries and emits PLT/GOT relocations resolved by the host's dynamic linker (`/system/bin/linker64`, `/system/bin/linker`, or `/lib*/ld-linux*`) before `_start` runs.

### 2.4 Rust Worker Internal Architecture (`dload/src/`)

1. **OS Primitives (`dlopen`, `dlsym`, `dlclose`, `malloc`, `free`, `read`, `write`):**
   - **Windows (`win.rs`):** `#[link(name = "kernel32", kind = "raw-dylib")]` binding `SetDllDirectoryW`, `LoadLibraryW`, `GetProcAddress`, `FreeLibrary`, `GetLastError`, `GetProcessHeap`, `HeapAlloc`, `HeapFree`, `GetStdHandle`, `ReadFile`, `WriteFile`, `ExitProcess`, `GetCurrentThreadId`. Before `LoadLibraryW(path)`, automatically calls `SetDllDirectoryW(parent_dir(path))` so sibling DLLs (such as `dxcompiler.dll` / `dxil.dll` next to `litert_lm_ext.dll`) are found.
   - **Unix / Darwin / Android (`unix.rs`):** Links `dlopen`, `dlsym`, `dlclose`, `dlerror`, `malloc`, `free` from the platform dynamic loader (`libSystem.B.dylib`, `libdl.so.2`+`libc.so.6`, or Bionic `libdl.so`+`libc.so`), and uses raw syscalls (or `libc` `read`/`write`) on `fd 0` (`stdin`) and `fd 1` (`stdout`).

2. **Dynamic Call Dispatch (`arch_call.rs`):**
   - Decodes the canonical WASM functype signature `[0x60, param_count, params..., result_count, results...]` (`0x7F`=`i32`, `0x7E`=`i64`, `0x7D`=`f32`, `0x7C`=`f64`).
   - Provides a small per-architecture assembly/ABI invoker function `invoke_c_func(sym, params_types, args_u64, result_types) -> [u64; 2]`:
     - **`x86_64` Windows (Win64 ABI):** Loads first 4 integer/float slots into `rcx`/`xmm0`, `rdx`/`xmm1`, `r8`/`xmm2`, `r9`/`xmm3`, pushes remaining slots on 16-byte aligned stack with 32-byte shadow space, calls `sym`, reads `rax` / `xmm0`.
     - **`x86_64` Linux (SysV AMD64 ABI):** Separates integer args into `rdi, rsi, rdx, rcx, r8, r9` and float/double args into `xmm0..xmm7`, pushes overflow args onto 16-byte aligned stack, calls `sym`, reads `rax, rdx` / `xmm0, xmm1`.
     - **`aarch64` (Windows, Linux, Android, macOS — AAPCS64):** Separates integer args into `x0..x7` and float/double args into `d0..d7` (`v0..v7`), pushes overflow args onto 16-byte aligned stack (packed 8-byte slots on Linux/Android/Windows; Darwin ABI rules on macOS), calls `sym`, reads `x0, x1` / `d0, d1`.
     - **`arm` (Linux/Android ARMv7 hard-float — AAPCS-VFP):** Places `i32` in `r0..r3` (`i64` in even-aligned register pairs `r0:r1`, `r2:r3`), `f32`/`f64` in `s0..s15`/`d0..d7`, overflow on stack, calls `sym`, reads `r0, r1` / `s0, d0`.

3. **C-to-Go Callback Trampolines & Re-entrant Thread Pumping (`callbacks.rs`):**
   - Pre-allocates a static table of N (e.g. 64) native entry stub slots (`trampoline_0` .. `trampoline_63`). Each stub captures its slot index `cb_id`, saves the incoming C ABI registers (`x0..x7` / `rcx..r9` / `rdi..r9` / `r0..r3`), and calls `rust_callback_handler(cb_id, &saved_regs)`.
   - Notice why this works across **both** synchronous re-entrant callbacks and asynchronous background-thread callbacks (like LiteRT-LM's `callback_thread_pool_` thread!):
     - The `dload` worker spawns a dedicated reader thread (or per-call worker thread via `CreateThread` / `pthread_create` / `clone`) so `convSendStream` or background C++ threads can emit `OpCbInvoke` frames over `stdout` at any time while the main loop reads `OpCbReturn` and re-entrant `OpCall` frames (such as `streamChunkGetText`, `streamChunkIsFinal`, `GoString` `OpReadMem`) from `stdin`!
     - Specifically: while a native C thread is blocked inside `rust_callback_handler` waiting for its `inv_id` to receive `OpCbReturn`, the Go callback (`sharedTokenCallback`) calls `dload.DylibCall(symLMStreamChunkGetText, ...)` and `dload.GoString(...)` (`OpReadMem`). The `dload` worker executes those incoming `OpCall` / `OpReadMem` requests immediately and writes their responses to `stdout`, then receives `OpCbReturn` and unblocks the C callback thread!

---

## 3. Component 2: Binary Wire Protocol (`stdin` / `stdout` Pipe IPC)

Replacing `encoding/gob` with a fixed little-endian binary frame eliminates reflection and `std` bloat in the Rust worker while remaining trivial to parse in Go.

### 3.1 Frame Envelope
Every message in either direction (Go -> Worker on `stdin`, Worker -> Go on `stdout`) has a 13-byte header followed by `payload_len` bytes:
```
Offset  Size  Type   Description
0       4     u32    payload_len (byte length of payload following header)
4       1     u8     opcode
5       8     u64    id (req_id for RPCs, inv_id for callback events/returns)
13      N     [u8]   payload
```

### 3.2 Opcodes & Payloads

| Opcode | Name | Direction | Payload Layout (Little-Endian) |
|---|---|---|---|
| `0x01` | `Handshake` | Worker -> Go | `[u8 os_kind][u8 arch_kind][u16 version]` (sent immediately on worker startup) |
| `0x10` | `OpenReq` | Go -> Worker | `[i32 flags][u32 path_len][path_utf8...]` |
| `0x11` | `OpenResp` | Worker -> Go | `[u8 status][u64 lib_handle][u32 err_len][err_utf8...]` |
| `0x12` | `SymReq` | Go -> Worker | `[u64 lib_handle][u32 name_len][name_utf8...]` |
| `0x13` | `SymResp` | Worker -> Go | `[u8 status][u64 sym_addr][u32 err_len][err_utf8...]` |
| `0x14` | `CloseReq` | Go -> Worker | `[u64 lib_handle]` |
| `0x15` | `CloseResp` | Worker -> Go | `[u8 status][u32 err_len][err_utf8...]` |
| `0x20` | `CallReq` | Go -> Worker | `[u64 sym_addr][u64 parent_inv_id][u32 sig_len][sig...][u32 argc][u64 args...]` |
| `0x21` | `CallResp` | Worker -> Go | `[u8 status][u32 res_count][u64 results...][u32 err_len][err_utf8...]` |
| `0x30` | `AllocReq` | Go -> Worker | `[u64 size][u32 align]` |
| `0x31` | `AllocResp` | Worker -> Go | `[u8 status][u64 ptr][u32 err_len][err_utf8...]` |
| `0x32` | `FreeReq` | Go -> Worker | `[u64 ptr]` |
| `0x33` | `FreeResp` | Worker -> Go | `[u8 status][u32 err_len][err_utf8...]` |
| `0x34` | `ReadMemReq` | Go -> Worker | `[u64 ptr][u32 len]` |
| `0x35` | `ReadMemResp` | Worker -> Go | `[u8 status][u32 data_len][data...][u32 err_len][err_utf8...]` |
| `0x36` | `WriteMemReq` | Go -> Worker | `[u64 ptr][u32 data_len][data...]` |
| `0x37` | `WriteMemResp`| Worker -> Go | `[u8 status][u32 err_len][err_utf8...]` |
| `0x40` | `CbRegReq` | Go -> Worker | `[u64 cb_id][u32 sig_len][sig...]` |
| `0x41` | `CbRegResp` | Worker -> Go | `[u8 status][u64 trampoline_ptr][u32 err_len][err_utf8...]` |
| `0x42` | `CbInvoke` | Worker -> Go | `[u64 cb_id][u64 thread_id][u32 argc][u64 args...]` (`id` in header = `inv_id`) |
| `0x43` | `CbReturn` | Go -> Worker | `[u8 status][u32 res_count][u64 results...]` (`id` in header = `inv_id`) |

---

## 4. Component 3: Universal Prebuild Integration (`mohabbat/prebuild.go`)

To satisfy the requirement that `dload` is treated as a universal build dependency (just like the `rusticated-go` overlay):

1. **Workspace Registration:**
   - Add `"dload"` to `members` in the root `Cargo.toml`.
   - Create `dload/go.mod` (`module dload`, `go 1.27.1`) and add `replace dload => ../dload` in `ai/go.mod`, `kabibi/go.mod`, and root `go.mod`.
2. **Hook in `runPrebuild` (`mohabbat/prebuild.go`):**
   - In `runPrebuild(ws, verbose, scope)`: whenever `scope != PrebuildScopeRustWasmOnly` (i.e. whenever building any Go project, test, dev-run, or full package), call `buildDloadWorkers(ws, verbose)`.
   - `buildDloadWorkers(ws, verbose)`:
     1. Ensures linker stubs exist in `target/dload-stubs/` (`ensureBrotStubs`, `ensureDarwinStubs`, `ensureElfDloadStubs`).
     2. Invokes `cargo build --release --bin dload` for each flavour into `target/dload-build/`. Because Cargo tracks file mtimes and Fingerprints in `target/dload-build/`, when `dload/src/*` has not changed, Cargo finishes in ~100ms as a no-op.
     3. Copies each built binary into `dload/embedded/dload-<flavour>` using `writeFileIfChanged` so Go's build cache for `package dload` (`//go:embed embedded/*`) is also never invalidated unless the `dload` Rust source actually changed!

---

## 5. Component 4: Go `dload` Package (`/dload`)

### 5.1 Embedded Binaries & On-Demand Extraction (`dload/embed.go`)

```go
package dload

import "embed"

//go:embed embedded/*
var embeddedWorkers embed.FS
```

When `dload` needs a worker for a target flavour (e.g., `linux-android-arm64`, `linux-gnu-amd64`, `windows-amd64`, `darwin-arm64`):
1. **Binary Inspection + Host Detection (`detectFlavour(libPath string)`):**
   - Reads the first 64 bytes of `libPath`:
     - **PE (`MZ`..`PE\0\0`):** Checks `Machine` (`0x8664` -> `windows-amd64`, `0xAA64` -> `windows-arm64`).
     - **Mach-O (`0xFEEDFACF`):** Selects `darwin-arm64`.
     - **ELF (`\x7fELF`):** Checks `e_machine` (`EM_X86_64`=62 -> `amd64`, `EM_AARCH64`=183 -> `arm64`, `EM_ARM`=40 -> `arm32`), and checks whether the runtime environment is Android Bionic (`os.Stat("/system/bin/linker64")` or `os.Stat("/system/bin/linker")` succeeds and `/lib/ld-linux*` is absent) to pick `linux-android-<arch>` vs `linux-gnu-<arch>`.
2. **Deterministic Temp Extraction:**
   - Extracts `embedded/dload-<flavour>` into `filepath.Join(os.TempDir(), fmt.Sprintf("dload-%s-%s", flavour, shortHash))` (with `.exe` on Windows) and `0o755` permissions.
   - Reuses the extracted file if its size/hash already matches, or writes to a temp file and atomically renames it into place.
3. **Process Launch (`os/exec.Command`):**
   - Spawns the extracted worker with `cmd.StdinPipe()`, `cmd.StdoutPipe()`, and `cmd.Stderr = os.Stderr`.
   - Reads the initial `Handshake` frame (`0x01`) to confirm the worker started cleanly.

### 5.2 Mirroring the Existing `rusticated-go/syscall/dylib_rusticated.go` API (`dload/dload.go`)

`package dload` exports the exact functions and types currently in `syscall` so `/ai` needs only a package-prefix rename (`syscall.Dylib*` -> `dload.Dylib*` or `dload.*`):

- **Constants:**
  - `RTLD_LAZY`, `RTLD_NOW`, `RTLD_GLOBAL`, `RTLD_LOCAL`
  - `WasmTypeI32` (`0x7F`), `WasmTypeI64` (`0x7E`), `WasmTypeF32` (`0x7D`), `WasmTypeF64` (`0x7C`)
- **Library & Symbol Lifecycle:**
  - `DylibOpen(path string, flags int) (uint64, error)` / `Open(path string, flags int) (uint64, error)`
  - `DylibSym(handle uint64, name string) (uint64, error)` / `Sym(handle uint64, name string) (uint64, error)`
  - `DylibClose(handle uint64) error` / `Close(handle uint64) error`
- **Function Calls & Callbacks:**
  - `DylibCall(sym uint64, sig []byte, args []uint64, parentInvID uint64) ([]uint64, error)` / `Call(...)`
  - `DylibRegisterCallback(libHandle uint64, sig []byte, cbOvPtr unsafe.Pointer, cbOvLen uint32, fn func(args []uint64) []uint64) (uint64, error)`
  - `DylibUnregisterCallback(cbOvPtr unsafe.Pointer)`
  - `RegisterCallback(libHandle uint64, sig []byte, fn func(args []uint64) []uint64) (uint64, func(), error)` (clean pointer-free form)
- **Remote Memory & String Helpers:**
  - `DylibAlloc(libHandle uint64, size uint64) (uint64, error)` / `Alloc(...)`
  - `DylibFree(libHandle uint64, ptr uint64) error` / `Free(...)`
  - `DylibReadMem(libHandle uint64, ptr uint64, dst []byte) error` / `ReadMem(...)`
  - `DylibWriteMem(libHandle uint64, ptr uint64, src []byte) error` / `WriteMem(...)`
  - `CString` / `DylibCString`, `GoString` / `DylibGoString`, `GoStringN` / `DylibGoStringN`, `GoBytes` / `DylibGoBytes`
  - `RemoteReader`, `NewRemoteReader`, `RemoteWriter`, `NewRemoteWriter`

### 5.3 How Userland Callbacks Work Without `rusticated-go` Runtime Hooks

In the old design, `washmhost` had to project `DylibCbInvokeEvent` into guest WASM Overlapped memory and wake a special runtime hook (`registerCallbackRuntime` in `rusticated-go/runtime/os_rusticated.go`) during `handleContinuation`.

In `/dload`, the pipe to the satellite worker is a normal `os.File` pipe inside the Go guest!
- A background reader goroutine in `dload` continuously reads frames from the worker's `stdout` pipe:
  - When a response frame (`OpenResp`, `SymResp`, `CallResp`, `ReadMemResp`, etc.) arrives for `req_id`, it delivers the frame to `pendingReqs[req_id]`'s channel.
  - When an `OpCbInvoke` (`0x42`) frame arrives for `cb_id` and `inv_id`, the reader goroutine dispatches a goroutine (or per-callback sequential worker) that invokes the registered Go `fn(args)`:
    - Inside `fn(args)` (e.g., LiteRT-LM's `sharedTokenCallback`), any calls to `dload.DylibCall(symLMStreamChunkGetText, ...)` or `dload.GoString(...)` send `CallReq` / `ReadMemReq` on `stdin` and receive their `CallResp` / `ReadMemResp` from the reader goroutine without deadlocking!
    - When `fn(args)` returns `results`, `dload` sends `OpCbReturn` (`0x43`) with `inv_id` on `stdin`, unblocking the native C thread in the `dload` worker.
- Zero runtime/scheduler modifications in `rusticated-go` are needed.

---

## 6. Component 5: Browser `eval` via Virtual Filesystem (`/dev/eval`)

With `dylib_*` removed from the Rusticated ABI, browser guest-to-host JavaScript evaluation moves to the virtual filesystem:

### 6.1 Browser Host (`mohabbat/washmhost/js/browser/index.js`)
- `index.js` already supports `{ type: 'proxy', proxy: { write(chunk), read(len) } }` handles in `env.write` and `env.read` (lines 684–696 and 744–752), where `proxy.read(len)` returns a `Promise<Uint8Array>` that asynchronously completes the guest's `rusticated_read` Overlapped!
- In `env.path_open`:
  - When the normalized path is `"/dev/eval"`:
    - Immediately allocate and return a new `{ type: 'proxy', proxy: createEvalProxy() }` handle (bypassing IndexedDB `fs`).
  - `createEvalProxy()` implementation:
    - `write(chunk)`: appends `chunk` bytes to an internal request buffer.
    - `read(maxLen)`:
      - On the first `read` call after `write`, decodes the accumulated UTF-8 JS source string, executes `(0, eval)(code)` in global scope, and awaits `Promise.resolve(result)`.
      - Encodes the result into a response `Uint8Array` prefixed with a 1-byte status header:
        - `0x00` followed by UTF-8 string bytes of `result ?? ""` on success.
        - `0x01` followed by UTF-8 string bytes of `err?.stack || err?.message || String(err)` on exception/rejection.
      - Slices and returns up to `maxLen` bytes from the response buffer, advancing its read offset; subsequent `read` calls return the remaining bytes until offset reaches the end, where `read` returns `new Uint8Array(0)` (`EOF`).
- Remove `dylib_open`, `dylib_sym`, `dylib_resolve`, `dylib_call`, `EVAL_LIB_HANDLE`, and `EVAL_SYM_HANDLE` from `mohabbat/washmhost/js/browser/index.js` (and `mohabbat/washmhost/js/node/index.js`).

### 6.2 Go Guest `EvalJS` (`ai/eval_bridge_rusticated.go` & `ai/assets/eval_bridge_rusticated.go`)
- `EvalJS(jsCode string) (string, error)` becomes pure standard-library file I/O:
  ```go
  func EvalJS(jsCode string) (string, error) {
      if jsCode == "" {
          return "", nil
      }
      f, err := os.OpenFile("/dev/eval", os.O_RDWR, 0)
      if err != nil {
          return "", fmt.Errorf("open /dev/eval: %w", err)
      }
      defer f.Close()

      if _, err := f.Write([]byte(jsCode)); err != nil {
          return "", fmt.Errorf("write /dev/eval: %w", err)
      }
      resp, err := io.ReadAll(f)
      if err != nil {
          return "", fmt.Errorf("read /dev/eval: %w", err)
      }
      if len(resp) == 0 {
          return "", nil
      }
      if resp[0] != 0 {
          return "", errors.New(string(resp[1:]))
      }
      return string(resp[1:]), nil
  }
  ```
- This also eliminates the 16 KB buffer limit and the `globalThis.__rustica_take_overflow` second-round-trip workaround.

---

## 7. Component 6: Cleaning Up `/ai`, `rusticated-go`, and `mohabbat`

### 7.1 `/ai` Migration
1. Add `dload` to `ai/go.mod` (`require dload v0.0.0`, `replace dload => ../dload`).
2. In `ai/backend_rusticated.go` and `ai/tflite_rusticated.go`:
   - Replace `"syscall"` dynamic-library calls (`syscall.DylibOpen`, `syscall.DylibSym`, `syscall.DylibCall`, `syscall.DylibAlloc`, `syscall.DylibFree`, `syscall.DylibReadMem`, `syscall.DylibWriteMem`, `syscall.DylibRegisterCallback`, `syscall.CString`, `syscall.GoString`, `syscall.RTLD_NOW`, `syscall.RTLD_GLOBAL`) with `dload.*`.
3. Update `ai/eval_bridge_rusticated.go` and `ai/assets/eval_bridge_rusticated.go` to use `/dev/eval`.

### 7.2 `rusticated-go` Cleanup
1. Delete `rusticated-go/syscall/dylib_rusticated.go`.
2. In `rusticated-go/runtime/os_rusticated.go`:
   - Remove `callbackRegistration`, `registeredCallbacksMu`, `registeredCallbacks`, `registerCallbackRuntime_syscall`, `unregisterCallbackRuntime_syscall`, `pauseToHost_syscall`, `currentG_syscall`, and step 1 of `handleContinuation()` that iterates `registeredCallbacks`.

### 7.3 `mohabbat/washmhost` & Launcher Cleanup
1. Delete from `mohabbat/washmhost/`:
   - `env_dylib.go`
   - `env_dylib_test.go`
   - `satellite.go`
   - `satellite_proto.go`
   - `satellite_test.go`
   - `dylib_arch.go`
   - `dl_unix.go`
   - `dl_windows.go`
2. In `mohabbat/washmhost/env_impl.go` and `runtime.go`:
   - Remove `dylibRegistry`, `dylibMgr`, `dylibLibraries`, `dylibSymbols`, `DylibManagersForPolling()`, and the 8 `dylib_*` host function exports in `Register()`.
3. In `mohabbat/washmhost/main.go`:
   - Remove `--dylib-satellite` and `--probe-dlopen` flags and `purego` probe logic.
4. Fix the two bugs found during the Android/Termux investigation:
   - **`mohabbat/package.go` (`tmplPOSIX`):** Capture `DD_RC=$?`, `CHMOD_RC=$?`, and `RET=$?` on the **same line** as `dd`, `chmod`, and `"$TMP_EXE"` so the leading `:;` on the following line never resets `$?` to `0`.
   - **`mohabbat/build_go_project.go` & `mohabbat/washmhost/env_utils.go`:** Set `-X main.BuildPlatform=%s-%s` to the **slot's** `s.goos` and `s.goarch` (instead of the build machine's OS/arch), and set `CPUType = 3` / `CPUBitness = 32` when `runtime.GOARCH == "arm"`.

---

## 8. Step-by-Step Execution Order & Verification Checklist

1. **Phase 1 — Build `/dload` Rust Worker & Prebuild Stubs:**
   - Create `dload/Cargo.toml` and `dload/src/` (`main.rs`, `protocol.rs`, `win.rs`, `unix.rs`, `arch_call.rs`, `callbacks.rs`).
   - Add `buildDloadWorkers` and `ensureElfDloadStubs` to `mohabbat/prebuild.go`.
   - Verify all 8 worker flavours compile cleanly via `cargo build` and inspect their ELF/PE/Mach-O headers (`PT_INTERP` = `/system/bin/linker64` and `/system/bin/linker` for Android, `/lib*/ld-linux*` for glibc Linux).
2. **Phase 2 — Implement Go `package dload` & Unit Tests:**
   - Create `dload/go.mod`, `dload/embed.go`, `dload/detect.go`, `dload/client.go`, `dload/dload.go`, and `dload/dload_test.go`.
   - Test loading a native shared library, allocating/reading/writing memory, invoking integer and floating-point functions, and firing re-entrant and background-thread C-to-Go callbacks.
3. **Phase 3 — Switch Browser `eval` to `/dev/eval`:**
   - Implement `/dev/eval` virtual proxy file in `mohabbat/washmhost/js/browser/index.js` and update `mohabbat/washmhost/js/browser/index.test.js`.
   - Update `ai/eval_bridge_rusticated.go` and `ai/assets/eval_bridge_rusticated.go` to use `/dev/eval`.
4. **Phase 4 — Switch `/ai` to `/dload` & Eject `dylib_*` from `rusticated-go` + `washmhost`:**
   - Update `ai/backend_rusticated.go` and `ai/tflite_rusticated.go` to import `dload`.
   - Remove `dylib_*` and `purego` from `rusticated-go/` and `mohabbat/washmhost/`.
   - Fix `tmplPOSIX` `$?` capture in `mohabbat/package.go` and slot `BuildPlatform` in `mohabbat/build_go_project.go`.
5. **Phase 5 — End-to-End Verification:**
   - Verify `washmhost-linux-amd64`, `washmhost-linux-arm64`, and `washmhost-linux-arm32` have **`PT_INTERP: []` and `DT_NEEDED: []`** (100% static).
   - Run `dload`, `mohabbat/washmhost`, `ai`, and `kabibi` tests.
   - Package `release/dist/kabibi.bat` and verify both `--help` and interactive startup.
