# Dynamic Library (DLL) Handling & FFI Architecture

## Current Architecture (In-Process)
Currently, washmhost loads dynamic libraries directly into its own process space using purego.

### Mechanism
* **Async APIs:** Host functions like sys_dylib_open, sys_dylib_call, and sys_dylib_sym are exposed to the Wasm guest using the overlapped I/O ABI.
* **Opaque Pointers:** When C functions return pointers to structs or strings, the host simply passes them to Wasm as opaque 64-bit integers (uint64). The host does not track these allocations.
* **Memory Reading:** To read data behind an opaque pointer, the Wasm guest must explicitly invoke further host calls (e.g., sys_dylib_read_cstr).
* **Callbacks:** When C invokes a Wasm callback, the host passes the raw callback arguments (opaque pointers/scalars) straight through to the guest export; it never interprets library-specific structs. The guest reads any struct fields it needs via the generic read primitives (sys_dylib_read_mem / sys_dylib_read_cstr) and decides call/stream semantics itself. Because native callbacks arrive on a background thread, the guest drains them via standard generic yielding (like `time.Sleep`) so each one runs nested on the guest's own stack during the resulting host round-trips.

### Drawbacks
* **Fatal Vulnerability:** If the loaded DLL triggers a Segmentation Fault, ABI boundary violation, or Out-Of-Memory (OOM) error, the entire washmhost process crashes.
* **Rigid Deserialization:** (Resolved) A generic sys_dylib_read_mem reads arbitrary binary structs without hardcoding type tags, so no library-specific layout knowledge lives in the host.

---

## Proposed Architecture (Satellite Process Isolation)
To achieve robust fault isolation without sacrificing the async ABI, DLL loading (purego) should be moved to a spawned satellite process.

### Mechanism
* **Self-Execution:** washmhost spawns a copy of itself with a hidden command-line flag (e.g., washmhost --dylib-satellite).
  * **Cross-Architecture Support:** The satellite executable can be chosen based on the specific architecture of the target DLL. If the DLL is x64 and the host is ARM, the host can launch an x64 build of the satellite process (e.g., via a --platform option) to leverage OS-level translation (like Rosetta 2 or Windows 11 emulation) seamlessly.
* **IPC Pipeline:** The parent and child communicate over I/O pipes (or a shared memory block) using a lightweight binary protocol.
* **Fault Isolation:** The satellite process executes all dangerous C/C++ FFI. If it crashes, the pipe breaks (EOF), and the parent washmhost gracefully traps the Wasm call instead of crashing.

### Solving the "Chatty" IPC Bottleneck
A naive IPC proxy for pointer dereferencing would incur massive cross-process overhead for every single pointer hop. However, the **rusticated async ABI** naturally solves this through batching:
* **Overlapped Operations:** The Wasm guest operates asynchronously. When traversing complex returned C-structs, the guest can queue dozens of sys_dylib_read_mem and sys_dylib_read_cstr overlapped reads simultaneously, then yield to the event loop.
* **Batching:** The parent washmhost can batch these overlapped I/O requests into a single IPC payload sent to the satellite.
* **Zero-Chattiness:** The satellite resolves all pointers in its native memory space in one go and returns the memory chunks together, fulfilling all overlapped completions simultaneously.

This architecture ensures uncrashable stability for the main Wasm host while preserving high-throughput FFI memory access for the guest.
