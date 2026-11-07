# Complete Mohabbat Redesign Architecture

The redesigned Mohabbat transitions from an out-of-process multi-binary extraction orchestrator (which dropped host binaries and spawned `washmhost` child processes) into a **pure in-process, memory-resident, JIT-driven execution engine**.

---

## Architectural Overview

### 1. File Format & Delivery Layer (The Vegetable)

* **The Unified Binary**: The file acts as a polyglot entry point. On Windows, it satisfies the PE/COFF specification directly, avoiding batch file parsing, command shell trampolines, or flickering console windows. On POSIX platforms, interactive shells intercept execution and evaluate it as a script.
* **Windows Execution Pathway**: The PE header targets the built-in AnyCPU .NET runtime (pre-installed across Windows client and server platforms). It allocates executable memory via unmanaged system memory interfaces, copies the payload into memory, and branches directly into the native layer without writing temporary files to disk.
* **Linux & Android (Termux) Pathway**: The script extracts a minimal loader directly into an anonymous, sealed memory file descriptor (`memfd_create`) and executes it via descriptor path lookup. This avoids touching non-volatile disk blocks and bypasses restrictive mount flags (such as `noexec` on temporary directories).
* **macOS (Darwin) Pathway**: The script streams an in-memory, read-only filesystem image directly into RAM via the system disk management utility, mounting it invisibly using privacy attributes so that Desktop and Finder displays are unaffected. The binary launches, relocates all operational components into anonymous executable memory, switches thread execution entirely to anonymous memory arenas, and immediately commands the host to unmount and detach the RAM volume. Execution continues uninterrupted within swap-backed anonymous memory.

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

The first implementation stage targets Windows only. It validates the execution handoff using a self-contained, CPU-dependent but operating-system-agnostic test probe. Linux, macOS, and Termux/Android will follow shortly, using their own platform abstractions and the same ISA-matched hello-world payloads. The unified polyglot delivery format remains the intended single-asset distribution; this Windows stage does not create separate architecture-specific EXE deliverables.

### 1. Scope & Objective

* Deliver one Windows AnyCPU EXE that runs on both AMD64 and ARM64 Windows. Its embedded manifest declares both supported architectures so supported Windows versions can select the native CLR.
* Use the CLR launcher and zstd to extract the architecture-matched movable binaries. The EXE is the only Windows deliverable; do not produce separate AMD64 and ARM64 EXE assets.
* Validate Windows anonymous executable-memory allocation, cache coherence, and native execution handoff on both architectures. This stage does not claim acceptance for Linux, macOS, or Termux.

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

Each operating system provides a platform abstraction responsible for bringing up the ISA-matched machine-code slice. The Windows stage packages two Windows abstractions (x86-64 and AArch64) in the single AnyCPU EXE; abstractions for other operating systems are added in later stages.

* **Windows Launcher**: A single AnyCPU .NET EXE uses its embedded manifest to allow native ARM64 CLR selection where supported, decompresses the matching Windows abstraction and hello-world payload with zstd, and provides the narrow low-level ABI to the abstraction. It does not itself implement the payload's standard input/output and exit primitives.
* **Windows Platform Abstraction**: The architecture-matched movable binary uses the supplied allocation and dynamic symbol-resolution primitives, prepares and populates executable memory with the hello-world payload, synchronizes the instruction cache, constructs the three high-level dispatch functions, and invokes the payload entry point.
* **Linux & Termux Platform Abstraction**: A later stage allocates an anonymous private page with read, write, and execute permissions using the kernel memory-mapping interface, copies the payload bytes into the mapped region, flushes the processor instruction cache on ARM platforms, constructs the three dispatch functions using the Linux/Android interfaces, and invokes the payload entry point.
* **macOS Platform Abstraction**: A later stage uses anonymous JIT memory-mapping flags, toggles thread-level write protection to copy the payload bytes, flushes the data cache, invalidates the instruction cache, and invokes the payload. The designed outer macOS launch path mounts the RAM disk and detaches it as specified in the delivery layer.

### 6. Acceptance Verification

The Windows stage is considered successful when the single AnyCPU EXE runs on both AMD64 and ARM64 Windows, selects the matching Windows abstraction and hello-world payload, displays the startup banner, echoes typed input lines interactively, and terminates cleanly when an empty line (a bare Enter keystroke) is supplied. The full first delivery is not complete until the corresponding OS-specific abstractions and acceptance runs are added for Linux, macOS, and Android Termux; those platforms reuse the same two ISA-specific hello-world payloads.