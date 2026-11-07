# Complete Mohabbat Redesign Architecture

The redesigned Mohabbat transitions from an out-of-process multi-binary extraction orchestrator (which dropped host binaries and spawned `washmhost` child processes) into a **pure in-process, memory-resident, JIT-driven execution engine**.

---

## Architectural Overview

### 1. File Format & Delivery Layer (The Vegetable)

* **The Unified Binary**: The file acts as a polyglot entry point. On Windows, it satisfies the PE/COFF specification directly, avoiding batch file parsing, command shell trampolines, or flickering console windows. On POSIX platforms, interactive shells intercept execution and evaluate it as a script.
* **Windows Execution Pathway**: The PE header targets the built-in AnyCPU .NET runtime (pre-installed across Windows client and server platforms). It acts as Brot on Windows: it decompresses the bundled single Zstandard-compressed data slice, allocates executable memory via unmanaged system memory interfaces, copies PAL into memory, and branches directly into the native layer without writing temporary files to disk.
* **Linux & Android (Termux) Pathway**: The shell stub extracts the matching target-specific Brot executable to a securely created, randomly named temporary file prefixed with `🐚︎`, executes it, and Brot unlinks its own path immediately on startup. Brot decompresses the bundled single Zstandard-compressed data slice containing the OS-specific PAL and ISA-shared runtime. `memfd_create` may be used as an optional launch optimization when a syscall-capable helper is already available; it is not required by the baseline.
* **macOS (Darwin) Pathway**: The baseline also launches the target-specific Brot from a random temporary file and unlinks it at startup. Brot decompresses the single bundled data slice. The macOS PAL uses anonymous JIT memory, toggles thread-level write protection while populating code, and synchronizes the instruction cache. RAM-disk staging and immediate volume detachment are not assumed by this baseline; a running executable can keep its source volume busy.
* **Storage caveat**: Unlinking removes the temporary pathname, not its backing pages. The temporary directory may be disk-backed or `noexec`, and anonymous memory may be swapped. The baseline therefore promises prompt pathname removal, not zero persistent-storage writes. An in-memory launch path is an optional optimization, not a prerequisite.

### 2. The In-Process Execution Engine (Brot, PAL, and Cranelift)

The architecture establishes a strict layered execution chain:
```
Brot --(PAL bindings)--> PAL --(PAL-to-runtime ABI)--> runtime/Cranelift --(Rusticated ABI)--> WASM brain
```

* **Layer Responsibilities**:
  * **Brot**: The entry binary launcher (AnyCPU PE on Windows; tiny target-specific binary on Unix). It decompresses the single bundled Zstandard-compressed data slice into memory, sets up PAL bindings, and jumps to PAL. Brot launchers are strictly target-specific micro-loaders that avoid generic Rust `std` to eliminate CRT overhead, thread-local bloat, and platform lock-in.
  * **PAL bindings**: The low-level interface between Brot and PAL. This interface is strictly per-OS+CPU. On Windows and macOS, it provides dynamic symbol lookup and memory/JIT protection; on Linux, PAL may invoke kernel system calls directly without dynamic loader bindings.
  * **PAL (Platform Abstraction Layer)**: Pure relocatable machine code (1 per OS+CPU combination), built without headers, standard libraries, or relocations. It adapts host OS services into the native PAL-to-runtime ABI.
  * **PAL-to-runtime ABI**: The native interface between PAL and the runtime. In full Mohabbat-next, it operates at roughly the complexity of the Rusticated ABI, providing host I/O primitives, event loop integration, and potential thread management hooks.
  * **Runtime / Cranelift**: The native execution engine (1 per CPU architecture, shared across all OSes on that CPU). In the first delivery, this is the minimal interactive hello-world probe; in full delivery, it is Cranelift JIT compiling the WASM bytecode.
  * **Rusticated ABI**: The guest ABI exposed by the runtime to the compiled WASM brain.
  * **WASM brain**: The portable application WebAssembly bytecode.

* **Zero Child Processes & No Watchdogs**: The standalone Go/Wazero runner binary (`washmhost`) is retired on desktop hosts. All host services—including file system virtualization, asynchronous input/output, and terminal handles—are implemented directly inside this in-process stack. Because execution never forks or spawns secondary processes, ghost-process watchdogs, parent-death signals, and OS job objects are eliminated.
* **Deduplicated ISA Pool (Zone C)**: Instead of packaging five redundant operating-system-specific host binaries, the compressed pool is reduced to two position-independent machine-code slices of the Cranelift compiler:
* One compilation slice for sixty-four-bit x86 architectures.
* One compilation slice for sixty-four-bit ARM architectures.
* Thin native PAL machine-code modules providing dynamic system call and host interface resolution.
* The target WebAssembly application payload (the brain).

* **In-Memory Compilation & Execution**: Brot allocates anonymous code pages, decompresses the bundled data slice, hands execution to PAL via PAL bindings, which in turn invokes Cranelift via the PAL-to-runtime ABI. Cranelift compiles the WebAssembly bytecode directly into that arena, synchronizes the CPU instruction cache, transitions memory protection flags to read-and-execute, binds native host system-call vectors into the guest execution context, and branches directly into the compiled application.

---

## First Delivery: The Minimal Interactive Probe

The first delivery validates the handoff using a self-contained, CPU-dependent but operating-system-agnostic test probe on Windows, Linux, macOS, and Termux/Android. The unified polyglot remains one MZ/PE distribution asset. Its DOS stub is also a shell-valid assignment and dispatches POSIX execution to Bash, which reads the appended bootstrap through process substitution so the probe retains the original terminal input. Platform-specific Brot launchers and PAL modules are selected by the shell/CLR bootstrap, while the two ISA-matched hello-world runtime payloads remain shared.

### 1. Scope & Objective

* Deliver one Windows AnyCPU EXE that runs on both AMD64 and ARM64 Windows. Its embedded manifest declares both supported architectures so supported Windows versions can select the native CLR.
* The Windows EXE acts as Brot on Windows: it decompresses the bundled single Zstandard-compressed data slice and sets up the architecture-matched PAL and runtime binaries. The EXE is the only Windows deliverable; do not produce separate AMD64 and ARM64 EXE assets.
* Support Linux AMD64/AArch64, Android Termux AArch64, and macOS x86-64/AArch64 through the Unix shell bootstrap and matching tiny Brot launcher. The bootstrap extracts Brot to the random temporary path, and Brot removes that path immediately after startup. Brot launchers avoid generic Rust `std`.
* Validate anonymous executable-memory allocation, cache coherence, and native execution handoff on every supported OS/architecture. Temporary-file backing is the baseline; optional memory-only launch methods do not gate support.

The unified vegetable packages the movable binaries inside a single Zstandard-compressed data slice:

* Windows PAL for x86-64 and AArch64.
* Linux PAL for x86-64 and AArch64 (shared with Android Termux).
* macOS PAL for x86-64 and AArch64.
* OS-agnostic hello-world runtime for x86-64.
* OS-agnostic hello-world runtime for AArch64.

Brot selects the matching PAL and runtime for the host OS and CPU architecture. The PAL is a separate binary from the runtime; neither requires a second EXE.

### 2. The Brot–PAL Boundary (PAL Bindings)

Brot supplies the selected PAL with OS+CPU-specific PAL bindings:
* **Windows**: Dynamic library and symbol lookup (`LoadLibrary`/`GetProcAddress`), executable memory allocation and release (`VirtualAlloc`/`VirtualFree`), and runtime pointer and size.
* **macOS**: Dynamic symbol lookup (`dlopen`/`dlsym`), memory mapping (`mmap`/`munmap`), cache invalidation (`sys_icache_invalidate`), and JIT write-protection toggling (`pthread_jit_write_protect_np`).
* **Linux & Android**: Memory mapping (`mmap`/`munmap`) and cache synchronization, or direct kernel syscalls where dynamic loader bindings are not needed.

PAL uses these primitives to establish the OS execution environment, allocates executable memory, maps the runtime payload into place, constructs the dispatch table conforming to the PAL-to-runtime ABI, and transfers control to the runtime.

### 3. The PAL-to-Runtime Boundary (Prototype ABI)

In this minimal probe delivery, the PAL-to-runtime ABI is prototyped as three basic C-ABI function pointers passed inside a single dispatch structure:

1. **Standard Output Writer**: Accepts a pointer to a buffer in memory and a byte count; writes the bytes out to the host console or terminal stream and returns the number of bytes written.
2. **Standard Input Reader**: Accepts a pointer to a destination buffer and a maximum capacity; reads incoming raw bytes from the host console or terminal stream and returns the number of bytes read.
3. **Exit Handler**: Accepts an integer termination status and exits the process immediately, never returning to the caller.

In the full Mohabbat-next delivery, this boundary expands into the comprehensive native PAL-to-runtime ABI, providing asynchronous file/stream I/O, event notification, and thread management to the Cranelift runtime.

### 4. The Bare Machine-Code Runtime Specification

* **Compilation Constraints**: Exactly two OS-agnostic raw, flat machine-code slices are shared across platforms: one for x86-64 and one for AArch64. They are built without operating-system headers, standard libraries, runtime preambles, C runtime dependencies, or thread-local storage. Each contains zero relocations, assumes no fixed load address, and makes zero direct kernel system calls or supervisor traps.
* **Execution Flow**:
1. The entry point receives the pointer to the host dispatch table.
2. It calls the host output primitive to emit a diagnostic greeting indicating that execution is active inside anonymous memory.
3. It allocates a fixed-size buffer on its own stack and enters an input evaluation loop.
4. It invokes the host input primitive to block and read input from the user.
5. If the input reader indicates end-of-stream (zero or negative bytes read), it terminates execution with a successful exit code via the exit primitive.
6. It inspects the captured buffer, discarding trailing carriage return or newline characters.
7. **Empty Line Detection**: If the line length is zero (the user pressed Enter without typing any characters), it interprets this as an exit signal and calls the exit primitive with code zero.
8. For non-empty input, it prepends an echo indicator, writes the received characters back out using the host output primitive, emits a newline and prompt indicator, and resumes waiting for the next input line.

### 5. Platform Host & PAL Specification

Each operating system provides a PAL responsible for bringing up the ISA-matched machine-code runtime slice. One shared x86-64 runtime and one shared AArch64 runtime are paired with OS-specific PAL modules and Brot launchers in the unified asset.

* **Windows Brot Launcher**: A single AnyCPU .NET EXE uses its embedded manifest to allow native ARM64 CLR selection where supported, decompresses the single bundled data slice, and provides the Windows PAL bindings to the PAL. It does not itself implement the runtime's standard input/output and exit primitives.
* **Windows PAL**: The architecture-matched movable binary uses the supplied allocation and dynamic symbol-resolution primitives, prepares and populates executable memory with the hello-world runtime, synchronizes the instruction cache, constructs the three high-level dispatch functions conforming to the PAL-to-runtime ABI, and invokes the runtime entry point.
* **Unix Brot Launcher**: The shell stub selects the OS/ISA launcher and extracts its raw executable bytes to a unique `🐚︎`-prefixed temporary path. Brot unlinks that path immediately, reads the single bundled data slice from the original bundle, decompresses the required components, supplies PAL bindings (dynamic symbol lookup where applicable, executable allocation/release, cache synchronization, and JIT-write-protection primitives) to the PAL, then transfers control. Brot avoids generic Rust `std`.
* **Linux & Termux PAL**: The x86-64 or AArch64 flat Rust binary resolves the Linux/Android I/O and exit interfaces, allocates executable memory through Brot's PAL bindings, copies and synchronizes the hello runtime, creates the three-function dispatch table, and invokes the runtime.
* **macOS PAL**: The x86-64 or AArch64 flat Rust binary uses Brot's JIT-memory and thread-level write-protection primitives to populate executable memory, synchronizes the instruction cache, constructs the dispatch table, and invokes the runtime.

### 6. Acceptance Verification

Acceptance requires the interactive banner/echo/empty-line-exit behavior on Windows AMD64/ARM64, Linux AMD64/AArch64, Android Termux AArch64, and macOS x86-64/AArch64, using each matching abstraction and shared ISA payload. Native target testing is required; cross-compilation alone does not count as acceptance.