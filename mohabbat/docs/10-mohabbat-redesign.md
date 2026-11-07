# Complete Mohabbat Redesign Architecture

The redesigned Mohabbat transitions from an out-of-process multi-binary extraction orchestrator (which dropped host binaries and spawned `washmhost` child processes) into a **pure in-process, memory-resident, JIT-driven execution engine**.

---

## Architectural Overview

### 1. File Format & Delivery Layer (The Vegetable)

* **The Unified Binary**: The file acts as a polyglot entry point. On Windows, it satisfies the PE/COFF specification directly, avoiding batch file parsing, command shell trampolines, or flickering console windows. On POSIX platforms, interactive shells intercept execution and evaluate it as a script.
* **Windows Execution Pathway**: The PE header targets the built-in AnyCPU .NET runtime (pre-installed across Windows client and server platforms). It allocates executable memory via unmanaged system memory interfaces, copies the payload into memory, and branches directly into the native layer without writing temporary files to disk.
* **Linux & Android (Termux) Pathway**: The shell stub extracts the matching Brot executable to a securely created, randomly named temporary file prefixed with `🐚︎`, executes it, and Brot unlinks its own path immediately on startup. Brot decompresses the OS-specific platform abstraction and ISA-shared hello payload from the bundle. `memfd_create` may be used as an optional launch optimization when a syscall-capable helper is already available; it is not required by the baseline.
* **macOS (Darwin) Pathway**: The baseline also launches Brot from a random temporary file and unlinks it at startup. The macOS platform abstraction uses anonymous JIT memory, toggles thread-level write protection while populating code, and synchronizes the instruction cache. RAM-disk staging and immediate volume detachment are not assumed by this baseline; a running executable can keep its source volume busy.
* **Storage caveat**: Unlinking removes the temporary pathname, not its backing pages. The temporary directory may be disk-backed or `noexec`, and anonymous memory may be swapped. The baseline therefore promises prompt pathname removal, not zero persistent-storage writes. An in-memory launch path is an optional optimization, not a prerequisite.

### 2. The In-Process Execution Engine (Brot & Cranelift)

* **Zero Child Processes & No Watchdogs**: The standalone Go/Wazero runner binary (`washmhost`) is retired on desktop hosts. All host services—including file system virtualization, asynchronous input/output, and terminal handles—are implemented directly inside Brot's non-standard, runtime-free Rust implementation. Because execution never forks or spawns secondary processes, ghost-process watchdogs, parent-death signals, and OS job objects are eliminated.
* **Deduplicated ISA Pool (Zone C)**: Instead of packaging five redundant operating-system-specific host binaries, the compressed pool is reduced to two position-independent machine-code slices of the Cranelift compiler:
* One compilation slice for sixty-four-bit x86 architectures.
* One compilation slice for sixty-four-bit ARM architectures.
* Thin native platform adapters providing dynamic system call and host interface resolution.
* The target WebAssembly application payload (the brain).


* **In-Memory Compilation & Execution**: Brot allocates anonymous code pages, directs the architecture-matched Cranelift slice to compile the WebAssembly bytecode directly into that arena, synchronizes the CPU instruction cache, transitions memory protection flags to read-and-execute, binds native host system-call vectors into the guest execution context, and branches directly into the compiled application.

---

## First Delivery: The Minimal Interactive Probe

The first delivery validates the handoff using a self-contained, CPU-dependent but operating-system-agnostic test probe on Windows, Linux, macOS, and Termux/Android. The unified polyglot remains one MZ/PE distribution asset. Its DOS stub is also a shell-valid assignment and dispatches POSIX execution to Bash, which reads the appended bootstrap through process substitution so the probe retains the original terminal input. Platform-specific Brot runners and abstractions are selected by the shell/CLR bootstrap, while the two ISA-matched hello-world payloads remain shared.

### 1. Scope & Objective

* Deliver one Windows AnyCPU EXE that runs on both AMD64 and ARM64 Windows. Its embedded manifest declares both supported architectures so supported Windows versions can select the native CLR.
* Use the CLR launcher and zstd to extract the architecture-matched movable binaries. The EXE is the only Windows deliverable; do not produce separate AMD64 and ARM64 EXE assets.
* Support Linux AMD64/AArch64, Android Termux AArch64, and macOS x86-64/AArch64 through the Unix shell bootstrap and matching Brot runner. The bootstrap extracts Brot to the random temporary path, and Brot removes that path immediately after startup.
* Validate anonymous executable-memory allocation, cache coherence, and native execution handoff on every supported OS/architecture. Temporary-file backing is the baseline; optional memory-only launch methods do not gate support.

The Windows EXE packages four independently compiled movable binaries:

* A Windows platform abstraction for x86-64.
* A Windows platform abstraction for AArch64.
* The OS-agnostic x86-64 hello-world payload.
* The OS-agnostic AArch64 hello-world payload.

The CLR selects the matching abstraction and hello-world payload for the process architecture. The platform abstraction is a separate binary from the hello-world payload; neither requires a second EXE.

### 2. Windows Launcher–Abstraction–Payload Boundary

The AnyCPU CLR launcher extracts the matching binaries from the single EXE using zstd and supplies the selected platform abstraction with a narrow low-level ABI. That ABI provides dynamic library and symbol lookup (LoadLibrary/GetProcAddress), memory allocation and release, and the hello-world payload entry-point pointer.

The platform abstraction uses those primitives to establish the Windows execution environment. It constructs the higher-level three-primitive dispatch table—standard output, standard input, and process exit—and invokes the matching hello-world payload with that table. Windows-specific behavior stays in the abstraction; the same hello-world payload can be paired with a different OS's abstraction.

### 3. The Payload–Platform Boundary (The Three-Primitive Contract)

The communication contract between the platform abstraction and the machine-code payload is limited to three basic C-ABI function pointers passed inside a single dispatch structure:

1. **Standard Output Writer**: Accepts a pointer to a buffer in memory and a byte count; writes the bytes out to the host console or terminal stream and returns the number of bytes written.
2. **Standard Input Reader**: Accepts a pointer to a destination buffer and a maximum capacity; reads incoming raw bytes from the host console or terminal stream and returns the number of bytes read.
3. **Exit Handler**: Accepts an integer termination status and exits the process immediately, never returning to the caller.

### 4. The Bare Machine-Code Payload Specification

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



### 5. The Platform Host Runner Specification

Each operating system provides a platform abstraction responsible for bringing up the ISA-matched machine-code slice. One shared x86-64 payload and one shared AArch64 payload are paired with OS-specific abstractions and Brot runners in the unified asset.

* **Windows Launcher**: A single AnyCPU .NET EXE uses its embedded manifest to allow native ARM64 CLR selection where supported, decompresses the matching Windows abstraction and hello-world payload with zstd, and provides the narrow low-level ABI to the abstraction. It does not itself implement the payload's standard input/output and exit primitives.
* **Windows Platform Abstraction**: The architecture-matched movable binary uses the supplied allocation and dynamic symbol-resolution primitives, prepares and populates executable memory with the hello-world payload, synchronizes the instruction cache, constructs the three high-level dispatch functions, and invokes the payload entry point.
* **Unix Brot Runner**: The shell stub selects the OS/ISA runner and extracts its raw executable bytes to a unique `🐚︎`-prefixed temporary path. Brot unlinks that path immediately, reads the compressed component ranges from the original bundle, decompresses them, supplies dynamic symbol lookup, executable allocation/release, cache synchronization, and JIT-write-protection primitives to the platform abstraction, then transfers control.
* **Linux & Termux Platform Abstraction**: The x86-64 or AArch64 flat Rust binary resolves the Linux/Android I/O and exit interfaces, allocates executable memory through Brot's ABI, copies and synchronizes the hello payload, creates the three-function dispatch table, and invokes the payload.
* **macOS Platform Abstraction**: The x86-64 or AArch64 flat Rust binary uses Brot's JIT-memory and thread-level write-protection primitives to populate executable memory, synchronizes the instruction cache, constructs the dispatch table, and invokes the payload.

### 6. Acceptance Verification

Acceptance requires the interactive banner/echo/empty-line-exit behavior on Windows AMD64/ARM64, Linux AMD64/AArch64, Android Termux AArch64, and macOS x86-64/AArch64, using each matching abstraction and shared ISA payload. Native target testing is required; cross-compilation alone does not count as acceptance.