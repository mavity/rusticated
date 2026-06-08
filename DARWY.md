# macOS/ARM Support Plan

This document outlines the steps to add support for macOS on Apple Silicon (ARM64). The goal is to allow the `rusticated` project to run on Mac hardware by extending the custom standard library and updating the build tools.

### 1. Unified Target Specification
The `prebuild` tool must be updated to generate a custom target for macOS.
- **Action**: Add `aarch64-apple-darwin` to the target list in the prebuild tool.
- **Linker Configuration**: Unlike Linux, macOS does not have a stable syscall ABI. We must link against `libSystem.dylib` while keeping the binary "rusticated" (independent of a heavy C runtime).

### 2. Standard Library Implementation (rusticated)
The core `std` replacement needs macOS-specific logic to handle memory, time, and threading.
- **OS Primitives**: Create a Darwin-specific module to implement low-level functions like `mmap`, `munmap`, and clock management.
- **Dynamic Wiring**: To avoid Apple changing syscall numbers, the standard library will resolve symbols from `libSystem` at runtime.
- **Async Reactor**: Update the existing `kqueue` reactor to activate when building for the custom Darwin target.
- **Threading**: Use `pthread` functions from the system library instead of the Linux-specific `clone` method.

### 3. Native Loader (brot)
The loader is a small native binary that handles decompression and starts the Go host.
- **Action**: Compile the loader for macOS/ARM using the new `rusticated` target.
- **Compatibility**: Ensure the loader uses the standard `std` API, which will now be backed by the new Darwin implementation in the core library.

### 4. Builder and Patcher (mohabbat-go)
The tool that assembles the final executable must be updated to recognize and handle Mac targets.
- **New Slot**: Add a "darwin-arm64" slot to the hardware matrix.
- **Polyglot Header**: Update the script header in the final file to detect macOS and extract the correct loader.
- **Code Signing**: This is the most critical step for Apple Silicon. Any time the builder patches or modifies the loader, it must re-apply an "ad-hoc" signature. Without this, macOS will kill the process immediately.

### 5. Runtime Host (washmhost-go)
The Go-based host uses a library called `wazero` to run the WASM payload.
- **Status**: This component is already mostly compatible because Go and its WASM engine handle platform differences natively. No major changes are expected here.

### Summary of Tasks:
1.  **Prebuild**: Enable the `aarch64-rusticated-darwin` target spec.
2.  **rusticated**: Add the Darwin OS module and `libSystem` wiring.
3.  **mohabbat-go**: Add the Mac slot, update the script header, and implement automatic signing.
4.  **Verification**: Build and test the "vegetable" on an ARM Mac to ensure the signature and syscalls are valid.