# Rusticated ABI: Browser Host Specification (`mohabbat/washmhost.js`)

This document specifies the architectural model, API design, extension mechanics, and asynchronous IndexedDB storage for driving **Mohabbat vegetables** in the browser using a unified, reusable `mohabbat/washmhost.js` host module.

---

## 1. Architectural Model

A Mohabbat vegetable running in the browser requires a browser-side host that implements the **rusticated WASM ABI** (`env` imports). The rusticated ABI is asynchronous and overlapped by design: guest operations register work with an overlapped pointer (`ovPtr`) and yield control back to the host, which notifies completion via completion flags and queues.

Instead of coupling the host logic to a specific user interface or terminal component (such as xterm.js in `index.html`), the browser host is encapsulated into a standalone functional closure: `washmhost(params)`.

```
┌─────────────────────────────────────────────────────────────────┐
│               Consumer Layer (UI / Test Runner)                 │
│  - Terminal Emulators (xterm.js, web consoles)                  │
│  - Application Logic & AI (Kabibi, LiteRT Web, Model Caches)    │
│  - Unit Test Suites (Mocha / Jest / Browser Test Harnesses)     │
└────────────────────────────────┬────────────────────────────────┘
                                 │
                     calls washmhost(params)
                                 │
┌────────────────────────────────▼────────────────────────────────┐
│              mohabbat/washmhost.js (Single File)                │
│                                                                 │
│  ┌────────────────────────┐         ┌────────────────────────┐  │
│  │     Host Controller    │         │    WASM Import ("env") │  │
│  │ - stdin/stdout/stderr  │         │ - Overlapped ABI pump  │  │
│  │ - signal dispatching   │◄───────►│ - Path resolution      │  │
│  │ - cooperative run loop │         │ - File/Socket handles  │  │
│  └───────────┬────────────┘         └───────────┬────────────┘  │
│              │                                  │               │
│  ┌───────────▼──────────────────────────────────▼────────────┐  │
│  │            Asynchronous IndexedDB Storage                 │  │
│  │  - Direct async IndexedDB queries & transactions          │  │
│  │  - No fake synchronous RAM cache: native async completion │  │
│  │  - Seed files & standard POSIX layout                     │  │
│  └───────────────────────────┬───────────────────────────────┘  │
│                              │                                  │
│  ┌───────────────────────────▼───────────────────────────────┐  │
│  │                 Pluggable Extension Hooks                 │  │
│  │  - dylib: Dynamic library interception (e.g. LiteRT)      │  │
│  │  - net: Network / proxy interception (HTTP proxy shims)   │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 2. High-Level Factory Interface

`washmhost` is exported as a standalone functional closure supporting both ES module imports and direct `<script>` inclusion.

```typescript
interface WashmhostParams {
  // --- Target Payload ---
  wasm?: string | ArrayBuffer | Response | WebAssembly.Module;

  // --- Stdio & Terminal Wiring ---
  stdout?: (data: Uint8Array) => void;
  stderr?: (data: Uint8Array) => void;
  getTtySize?: () => { cols: number; rows: number };
  setTtySize?: (cols: number, rows: number) => void;

  // --- Process Environment ---
  args?: string[];                      // Default: ['vegetable']
  env?: Record<string, string> | string[]; // Default: TERM=xterm-256color, PATH=/bin, HOME=/home/user
  cwd?: string;                         // Default: '/home/user'

  // --- Storage & IndexedDB Filesystem ---
  dbName?: string;                      // Default: 'RusticatedFS'
  fs?: AsyncFS;                         // Optional custom async FS instance
  initialFiles?: SeedEntry[];           // Additional seed files/directories

  // --- Extensions (Kabibi, AI, Custom Host APIs) ---
  extensions?: {
    dylib?: DylibExtensionHandler;
    net?: NetExtensionHandler;
  };

  // --- Lifecycle Callbacks ---
  onExit?: (code: number) => void;

  // --- Test & Inspection Mode ---
  test?: boolean;                       // When true, exposes internal guts for unit testing
}
```

### Return Modes

1. **Production Mode (`test !== true`)**:
   Returns `Promise<WashmhostController>`, resolving when the IndexedDB storage is initialized, WASM is instantiated, and the cooperative run loop is started:
   ```javascript
   const host = await washmhost({
     wasm: './try/kabibi.wasm',
     stdout: (bytes) => term.write(bytes),
     getTtySize: () => ({ cols: term.cols, rows: term.rows })
   });

   host.pushStdin('ls -la\r\n');
   host.triggerSignal(28); // SIGWINCH
   ```

2. **Test / Whitebox Mode (`test === true`)**:
   Returns the internal API dictionary synchronously without launching the run loop, allowing unit tests to assert directly on ABI handlers and filesystem transformations:
   ```javascript
   const internals = washmhost({ test: true });
   const { safeWriteOverlapped, resolvePath, env, fs, hostState } = internals;

   // Test path resolution
   assert.equal(resolvePath('../etc/hosts'), '/etc/hosts');
   ```

---

## 3. Controller API & Cooperative Scheduling Loop

### Controller API (`WashmhostController`)

When instantiated, the running host exposes clean, high-level control methods:

```typescript
interface WashmhostController {
  // Guest interaction
  pushStdin(data: string | Uint8Array): void;
  triggerSignal(signum: number): number; // Returns completed wait count

  // State inspection
  isRunning(): boolean;
  getExitCode(): number | null;
  getMemory(): WebAssembly.Memory;
  getInstance(): WebAssembly.Instance;
  getFS(): AsyncFS;

  // Lifecycle control
  terminate(exitCode?: number): void;
}
```

### Cooperative Scheduler (No Hot-Spinning)

Matching `runtime.go` in Go and `index.js` in Node, the host drives the guest cooperatively without hammering the WASM instance when it is waiting:

```javascript
while (isRunning) {
  // 1. Drain any pending completions into WASM memory
  hostState.drainQueue();

  // 2. Execute guest run loop; yields when waiting on async ops or idle
  const retVal = runFunc();
  if (hostState.forcedExitCode !== -1) {
    onExit(hostState.forcedExitCode);
    break;
  }

  // 3. If run() synchronously enqueued completions, deliver on next cycle
  if (hostState.completionQueue.length > 0) {
    continue;
  }

  // 4. If no outstanding operations, guest is fully parked or done
  if (!hostState.hasPending()) {
    if (retVal !== -1) {
      onExit(retVal);
      break;
    }
    // Guest is idling waiting for external stimuli (e.g. user typing into stdin)
    await hostState.waitForExternalStimulus();
    continue;
  }

  // 5. Operations are outstanding (IndexedDB I/O, timers, net socket)
  // Host sleeps until at least one completion is enqueued; NEVER hammer guest
  await hostState.waitForOpCompletion();
}
```

---

## 4. Asynchronous IndexedDB Storage (`AsyncFS`)

The rusticated ABI is asynchronous and overlapped by design. There is **no need to shoehorn IndexedDB into a synchronous in-memory RAM cache**:
- File reads, writes, directory listings, and stats map directly to IndexedDB asynchronous operations.
- The host registers an operation (`opId = registerOp(ovPtr)`) and returns immediately to the guest.
- When the IndexedDB transaction completes, the host schedules `completeOp(ovPtr, opId, errno, flags, outLen)` into `completionQueue` and wakes the scheduler.

```typescript
interface AsyncFSEntry {
  path: string;
  type: 'file' | 'dir';
  content?: Uint8Array;
  size: number;
  mode: number;
  mtime: number;
}

interface AsyncFS {
  init(): Promise<void>;
  seed(entries?: AsyncFSEntry[]): Promise<void>;

  getEntry(path: string): Promise<AsyncFSEntry | null>;
  putEntry(entry: AsyncFSEntry): Promise<void>;
  removeEntry(path: string): Promise<boolean>;
  readdir(dirPath: string): Promise<AsyncFSEntry[]>;
}
```

### Standard Base Seeding
During initial `init()`, the host checks if the database is populated. If empty, it creates standard POSIX structures:
- Directories: `/bin`, `/etc`, `/home/user`, `/tmp`, `/dev`, `/var`, `/usr`
- Seed files: `/etc/hosts`, `/etc/hostname`, `/etc/passwd`, `/etc/motd`
- `/dev/tty` character device mapping

---

## 5. Extension API: Decoupling Host from Application Logic

Specialized vegetables like Kabibi utilize custom HTTP proxies and pseudo-dynamic libraries (`.so` / `.litertlm`). The host delegates these through explicit extension interfaces, keeping `washmhost.js` generic.

### Dynamic Library Extension (`DylibExtensionHandler`)

```typescript
interface DylibExtensionHandler {
  open(libPath: string, flags: number): Promise<number | bigint | null> | number | bigint | null;
  resolveSymbol(libHandle: number | bigint, symbolName: string): number | bigint | null;
  call(symHandle: number, descPtr: number, descLen: number, ctx: HostContext): Promise<void> | void;
  alloc?(libHandle: number | bigint, size: bigint, align: number): Promise<bigint | null> | bigint | null;
  free?(libHandle: number | bigint, ptr: bigint): Promise<void> | void;
  readMem?(libHandle: number | bigint, ptr: bigint, length: number): Promise<Uint8Array> | Uint8Array;
  writeMem?(libHandle: number | bigint, ptr: bigint, bytes: Uint8Array): Promise<void> | void;
  registerCallback?(
    libHandle: number | bigint,
    signature: Uint8Array,
    callbackOverlappedPtr: number,
    callbackOverlappedLen: number,
    ctx: HostContext
  ): Promise<bigint | null> | bigint | null;
  close?(libHandle: number | bigint): void;
}
```

### Network Interception Extension (`NetExtensionHandler`)

```typescript
interface NetExtensionHandler {
  open(addr: string, port: number, flags: number): NetProxyHandle | null;
  lookup?(hostname: string): Promise<string[]> | string[];
  verifyCertificate?(serverName: string, encodedChain: Uint8Array): Promise<boolean> | boolean;
}

interface NetProxyHandle {
  write(chunk: Uint8Array): void;
  read(maxLen: number): Uint8Array | Promise<Uint8Array>;
  close(): void;
}
```

---

## 6. Rusticated ABI Implementation ("env" Mapping)

The host provides the complete `env` import dictionary required by Go (`wasip1` / `rusticated-go`) and Rust (`wasm32-unknown-unknown` + `rusticated sysroot`). Every handler conforms strictly to the overlapped asynchronous contract:

| ABI Call | Purpose | Browser Implementation Strategy |
|:---|:---|:---|
| `get_args` | Program command line | Encodes `params.args` as null-terminated strings into guest memory |
| `get_env` | Environment block | Encodes `params.env` as `KEY=VAL\0` sequences |
| `get_cwd` / `set_cwd` | Current working dir | Updates internal `hostState.cwd` validated against `AsyncFS` |
| `get_time` | High-res monotonic/wall time | Writes nanoseconds: `BigInt(Date.now()) * 1000000n` |
| `get_random` | Cryptographic entropy | `crypto.getRandomValues(guestSlice)` |
| `timer_set` | High-res timer wait | Queues `setTimeout`; completes `ovPtr` on expiry |
| `cancel` | Cancel pending overlapped op | Marks active `ovPtr` cancelled; returns `wasiECANCELED` |
| `read` | Overlapped async read | Reads from stdin queue, IndexedDB file content, or net proxy handle |
| `write` | Overlapped async write | Routes fd 1/2 to `stdout`/`stderr`; writes chunk to IndexedDB or net handle |
| `seek` | Move a file handle offset | Supports absolute, current-relative, and end-relative offsets |
| `handle_truncate` | Set an open file's logical length | Resizes file content, zero-fills growth, and persists through `AsyncFS` |
| `handle_close` | Close resource handle | Frees open file, directory, or socket descriptor |
| `path_open` | File/dir handle creation | Resolves mode/flags against IndexedDB asynchronously |
| `path_stat` | File metadata inspection | Emits 64-byte `AbiStat` structure filled from `AsyncFS` |
| `dir_read` | Directory enumeration | Paginates entries encoded as kind byte + NUL-terminated name |
| `path_chmod` | Change file permissions | Updates `mode` in `AsyncFS` |
| `path_remove` | File or empty dir deletion | Deletes entry from IndexedDB; returns `wasiENOENT` / `wasiENOTEMPTY` |
| `path_mkdir` | Directory creation | Creates dir entry in `AsyncFS` |
| `path_rename` | File/dir move | Renames key in IndexedDB |
| `signal_wait` | Signal registration | Queues overlapped wait; completed immediately on buffered or incoming signals |
| `tty_get_size` | Terminal dimensions | Calls `params.getTtySize()` -> encodes `(cols << 16) | rows` |
| `tty_set_size` | Set terminal dimensions | Calls `params.setTtySize(cols, rows)` for standard TTY handles; returns `wasiENOTTY` when no setter is provided |
| `tty_set_mode` | Raw / cooked TTY mode | Stores mode flag; echoes / line buffering configured accordingly |
| `fd_isatty` | Is handle a TTY | Returns 1 for handles 0, 1, 2, or `/dev/tty` |
| `get_platform_info` | Modern Four platform descriptor | Emits the 676-byte descriptor with browser OS metadata and POSIX path separators |
| `dylib_alloc` / `dylib_free` | Dynamic-library memory allocation | Delegates to `DylibExtensionHandler`; returns `wasiENOSYS` if unavailable |
| `dylib_read_mem` / `dylib_write_mem` | Transfer data to/from library memory | Delegates to `DylibExtensionHandler`; returns `wasiENOSYS` if unavailable |
| `dylib_callback_register` | Register guest callback trampoline | Delegates to `DylibExtensionHandler`; returns `wasiENOSYS` if unavailable |
| `host_panic` | Guest fatal panic reporting | Logs panic message from guest memory and aborts host |
| `rusticated_debug` | Host debug trace | Emits debug value to host logger |
| `process_exit` | Program exit | Sets `hostState.forcedExitCode` and triggers `params.onExit` |
| `net_open` / `net_lookup` | Socket & DNS | Delegated to `NetExtensionHandler` or stubbed with `wasiENOTSUP` |
| `net_cert_verify` | Verify a TLS certificate chain | Delegated to `NetExtensionHandler.verifyCertificate`; returns `wasiENOSYS` if unavailable |
| `process_pipe` | In-memory process pipe | Creates asynchronous read/write handles backed by a shared byte queue |
| `process_spawn` / `process_wait` | Process control stubs | Stubbed with `wasiENOSYS`; browser host does not launch child processes |

---

## 7. Delivery, Migration & Comprehensive Unit Testing Plan

1. **Artifact Location**:
   - Create `mohabbat/washmhost/js/browser/index.js` (core reusable browser implementation).
   - Bundle / expose as `mohabbat/washmhost.js` in the repository root for direct consumption by web clients.
2. **Refactoring `index.html`**:
   - Include `<script type="module" src="mohabbat/washmhost.js"></script>` so the
     ES module runs and publishes `window.washmhost` for the existing page script.
   - Extract UI concerns (xterm.js initialization, DOM rendering).
   - Pass LiteRT and proxy shims via `washmhost({ extensions: ... })`.
3. **Automated Unit Testing Suite**:
   - Unit tests must be provided covering **all** points of the rusticated ABI:
     - **Path resolution & normalization** (handling relative paths, `..`, root boundaries).
     - **Overlapped buffer mechanics**: verify `registerOp`, `completeOp`, `drainQueue`, and cancellation with `FLAG_COMPLETED`.
     - **Async IndexedDB file operations**: test `putEntry`, `getEntry`, `readdir`, `removeEntry`, and binary file read/write roundtrips.
     - **Scheduler idle & wake behavior**: verify the host sleeps when waiting on timers or stdin and does not spin loop or hammer the guest.
     - **Errno mapping**: verify standard POSIX errors map precisely to `wasi` errno values.
     - **TTY sizing and signal dispatch**: verify `SIGWINCH` and `tty_get_size` integrations.
