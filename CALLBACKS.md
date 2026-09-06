# Callback And Cross-Island Invocation Architecture Spec

## Overview

This document defines the required architecture for typed cross-island invocation and callback delivery across DLL-backed satellite execution and future satellite WASM modules.

The governing principle is consistency: all cross-island calls use the same structural model regardless of target, and callback completion is carried through guest-owned overlappeds.

The model is:

1. Function signatures use WASM function-type encoding.
2. Cross-island invocations are always fully typed and never variant-based.
3. Pointers are treated only as flat ordinals.
4. Callbacks are represented as flat ordinals too.
5. Callback invocation and return are handled through guest-owned overlappeds with explicit async ownership transfer.
6. The same machinery is reused for arbitrary satellite WASM modules, not just DLLs.

"Cross-island" means any invocation that crosses between:

1. the primary rusticated WASM guest,
2. the satellite process that hosts DLL calls,
3. future satellite WASM modules.

This document is intentionally DLL-first because DLL support is the immediate operational target, while the model is selected for its future WASM applicability.

### Note On DLL.md Precedence

[DLL.md](DLL.md) is a legacy document. This specification (`CALLBACKS.md`) supersedes it and takes precedence wherever contradictions arise, notably:

1. **Callback Delivery**: [DLL.md](DLL.md) suggests calling guest exports directly for native callbacks. This specification strictly mandates delivery through guest-owned `Overlapped` transport cells discovered during normal event-loop polling, preventing WASM scheduler stack corruption (`morestack on g0`).
2. **Structural Memory ABI vs. Bespoke Helpers**: [DLL.md](DLL.md) introduces specific host primitives like `sys_dylib_read_cstr`. This specification strictly restricts the host ABI to four primitive byte-level operations (`alloc`, `free`, `read_mem`, `write_mem`), keeping all string decoding and struct layout logic inside the guest runtime.
3. **Architecture Baseline**: [DLL.md](DLL.md) frames satellite process isolation as a proposed future alternative to in-process execution. This specification establishes satellite process isolation, 64-bit flat ordinal pointers, and WASM-encoded functypes as the baseline architecture.

## Design Axioms

### 1. Use WASM Signature Format

The binary description of call shape must follow WASM function type encoding.

Each callable entity is described only by:

1. ordered parameter types,
2. ordered result types.

No semantic categories such as "string", "buffer", or "callback" belong to the signature language.

### 2. Keep Signatures Purely Structural

The signature format describes raw shape only.

It does not encode:

1. async meaning,
2. ownership,
3. caller intent,
4. buffer direction,
5. whether a number is a pointer, handle, callback, errno, or length in semantic terms.

Those are ABI conventions between caller and callee.

### 3. Pointers Are Just Ordinals

Pointer-like values are not declared as special pointer types in the signature language.

For DLL-facing ABI we standardize on 64-bit ordinals for pointers and callback values.

That means:

1. DLL pointers are carried as 64-bit integers.
2. Callback tokens are carried as 64-bit integers.
3. The guest packs and unpacks pointed-to structures itself.
4. Any string, struct, array, or opaque native allocation behind such a number is accessed through explicit helper ABIs.

This keeps the signature system clean and avoids another bespoke type universe.

### 4. Callbacks Must Be Immediately Valid In The Target Universe

Registering a callback must produce a value that is already directly callable in the target execution universe.

For DLL land, that means the returned 64-bit value must be a native callable address in the satellite process.

For future satellite WASM land, that means the returned value must be directly usable by that satellite WASM calling convention.

This requirement exists because callback values may be:

1. passed as direct arguments,
2. stored into foreign-owned data structures,
3. retained for later invocation,
4. called without a host-controlled outbound call boundary at the moment of use.

Lazy substitution is therefore not sufficient.

### 5. Overlappeds Are Guest-Owned Transport Cells, Not Durable Storage

Guest callback overlappeds are reusable transport cells.

They are not the durable storage for:

1. callback invocation arguments,
2. callback return values,
3. invocation lifetime identity.

Arguments may be overwritten by later re-entry into the same callback. Therefore a guest that wishes to execute a callback asynchronously must copy out the arguments it needs.

Similarly, when the host cannot immediately return a completed callback result to the satellite, washmhost must copy that result out into host-side durable storage.

## Cross-Island Invocation

### Canonical Representation

The canonical signature representation is the standard WASM functype encoding.

Where a signature representation crosses an ABI boundary, that representation must itself be fixed explicitly and remain stable for callers. It may be raw functype bytes, an interned canonical handle, or another explicitly defined canonical form, but it must not be left as an ad hoc backend detail when callers are required to supply or persist it.

Conceptually a signature is:

1. params: `[t1, t2, ...]`
2. results: `[r1, r2, ...]`

Where each `tN` and `rN` is a WASM value type.

For current needs the important primitive cases are:

1. `i32`
2. `i64`
3. `f32`
4. `f64`

Future extensions like `v128`, `externref`, or `funcref` are outside the immediate scope of this specification.

### DLL Convention On Top Of WASM Signatures

The signature itself remains pure WASM shape. DLL-specific ABI conventions are then layered above it.

For DLL usage:

1. native pointers are passed as `i64` values,
2. callback values are passed as `i64` values,
3. sizes and offsets may be `i32` or `i64` according to ABI choice,
4. pointed-to memory is not interpreted by the signature layer.

### No Variant Calls

Every cross-island invocation must carry a complete signature.

There is no dynamically variant call payload whose meaning is interpreted by tag names like "buf", "cstr", or "callback".

Instead:

1. callee name,
2. exact WASM-format signature,
3. ordered raw argument values

must be fully known at invocation time.

### DLL Call Contract

The DLL subsystem uses the same typed cross-island model end-to-end.

#### Call Invocation Requirements

1. The call signature is expressed using WASM-format function types.
2. Buffer, string, pointer, and callback semantics are not encoded as pseudo-types in the signature layer.
3. Native pointers are carried as 64-bit ordinals.
4. Callback values are carried as 64-bit ordinals.
5. Pointer-target interpretation happens only through explicit helper ABIs.
6. Callback delivery uses callback-overlapped signaling through guest-owned overlappeds.

#### DLL Call Resolution

To invoke a DLL function the guest supplies:

1. target library handle,
2. target symbol name or symbol handle,
3. WASM-format signature,
4. raw argument list matching that signature,
5. an optional parent invocation id (0 when absent).

The satellite resolves the symbol and performs the call according to native calling rules, but the call shape visible at the ABI boundary remains WASM-style.

Calling convention details required by native FFI remain an implementation concern of the DLL backend and not part of the general cross-island signature language.

#### Reentrant Call Routing Via Parent Invocation Id

A call made by the guest while it is servicing a callback invocation may need to run on the exact satellite thread that is currently blocked inside that callback's native trampoline (for example, to read data through a pointer that is only valid for the duration of that call). A call unrelated to any callback has no such requirement.

Rather than binding "any call performed while handling a callback" to a thread by fiat, the guest makes this explicit per call:

1. if the guest is not acting on behalf of any callback invocation, it passes parent invocation id `0`, and the host routes the call to its ordinary satellite dispatch worker,
2. if the guest is acting inside the goroutine handling callback invocation `N`, it passes that same invocation id as the parent invocation id, and the host routes the call to the specific satellite thread currently parked in invocation `N`'s trampoline pump.

The guest already possesses the invocation id for any callback it is actively handling, because it copied it out of the callback overlapped on discovery (see Callback Discovery And Dispatch). No new identity concept, and no notion of an OS thread, is introduced on the guest side: the guest only ever passes back an id it was already given.

This replaces any requirement that calls be implicitly bound to "the active callback thread": binding is explicit, per call, and opt-in via the parent invocation id, so unrelated calls made while a callback happens to be in flight are never accidentally routed onto a callback's satellite thread.

## Satellite Memory Management ABI

Because pointers are just ordinals and the guest is responsible for packing and unpacking pointee data, the guest needs explicit ability to allocate and release native memory inside the satellite universe.

Therefore a memory-management ABI is required.

Minimum required operations:

1. allocate native memory in the satellite,
2. free native memory in the satellite,
3. copy bytes from guest memory to satellite memory,
4. copy bytes from satellite memory to guest memory.

Additional convenience helpers may exist, but the core model assumes only explicit memory transfer, not semantic interpretation.

If alignment guarantees are caller-visible, the ABI must expose them explicitly. If alignment is not caller-controlled, the ABI must still define the minimum guarantees that callers may rely on.

This is required both for ordinary DLL arguments and for callback argument preparation when the guest needs to construct foreign-owned data structures.

Allocation in the satellite must be served by the platform's native allocator rather than by the satellite's own Go heap, so that returned addresses carry the platform's standard alignment guarantee and are never subject to Go garbage collection or stack-copy relocation:

1. Windows: `HeapAlloc` / `HeapFree` against `GetProcessHeap()`, from `kernel32.dll`,
2. macOS: `malloc` / `free`, from `libSystem.B.dylib`,
3. Linux: `malloc` / `free`, from the process's linked libc.

All three guarantee at least 16-byte alignment for any allocation on 64-bit targets, which the ABI adopts as its minimum caller-visible alignment guarantee.

## Callback Model

### Callback Registration ABI

### Registration Inputs

Registering a callback must take at least:

1. callback signature in WASM format,
2. pointer to a guest-owned callback overlapped,
3. total size of that callback overlapped,
4. any optional backend-specific registration flags.

The registration call may also include an identifier of the logical guest callback target if needed by the host for bookkeeping, but the callback execution model must not require direct host invocation of a named guest export.

The guest-side callback runtime is expected to be generic over registrations and signatures rather than specialized to one application callback shape.

### Registration Output

Registration returns a 64-bit callback value that is directly meaningful inside satellite land.

For DLL land that value must be a native callable trampoline address in satellite process memory.

The guest may then:

1. pass it as an argument,
2. store it into foreign structures,
3. hand it to native code for later invocation.

### Callback Overlapped Layout

The callback registration call receives a guest-owned overlapped whose layout is custom-sized for that callback.

Only the top header portion is shared with ordinary rusticated overlappeds.

The callback overlapped must then include enough additional payload space for:

1. callback metadata,
2. input argument values,
3. return values.

The exact payload layout should be rigidly specified. At minimum it needs:

1. standard overlapped header,
2. callback id,
3. invocation id,
4. encoded argument count or argument area length,
5. encoded result area length,
6. raw argument payload,
7. raw result payload.

The payload contract must explicitly define whether argument and result areas are fixed-size per registration, indirect through referenced storage, or allowed to mix both strategies. It must also support arbitrary callback shapes within that declared contract and must not rely on an implicit small fixed-arity limit.

The callback id identifies the callback registration.

The invocation id identifies the specific callback firing instance currently being projected through this transport cell.

### Callback Invocation Model

### Overview

Callbacks are handled through a single-thread stack discipline inside each satellite callback thread.

The host signals callback invocations by writing into the guest-owned callback overlapped and setting its completed bit.

The guest discovers callback invocations by polling its registered callback overlappeds during its normal event-loop processing, exactly like polling any other overlapped for file I/O or timers.

### Key Ownership Rule

For a registered callback, the guest-visible callback overlapped is a shuttle cell that is reused over time.

When the host sets the completed bit on that overlapped, it means:

1. a new invocation for that callback has been delivered, or
2. the host is projecting the next innermost pending invocation of that callback into the cell.

When the guest clears the completed bit, it means:

1. the guest has accepted responsibility for asynchronous handling of that invocation, and
2. the host may later reuse the same overlapped cell for a deeper invocation of the same callback.

When the guest later sets the completed bit again with return values filled in, it means:

1. the guest now declares completion of the innermost unresolved invocation of that callback.

### Synchronous Inline Callback Handling

If the guest decides to handle the callback inline during the current `run` slice, it may:

1. read the invocation payload from the callback overlapped,
2. write return values into the callback overlapped,
3. leave the completed bit set.

At the end of `run`, the host observes that the callback overlapped remains complete and treats it as a completed callback return for the innermost unresolved invocation of that callback.

### Asynchronous Callback Handling

If the guest decides to execute the callback asynchronously, it must:

1. copy out any arguments it will need later,
2. clear the completed bit on the callback overlapped,
3. begin its async processing.

Later, after that async work finishes, the guest must:

1. write the intended return values back into the callback overlapped,
2. set the completed bit again,
3. return from the current `run` slice.

The host then interprets that as a completed return for the innermost unresolved invocation of that callback.

### Host-Side Durable State

Because guest overlappeds are reusable and not durable, washmhost must keep durable callback state in host memory.

At minimum the host must maintain, per outstanding callback invocation:

1. callback id,
2. invocation id,
3. satellite callback thread identity,
4. parent nesting relation,
5. current state,
6. copied argument payload if needed for redelivery,
7. copied return payload if completion cannot yet be returned to satellite.

Possible states include:

1. pending,
2. projected-into-guest,
3. guest-owned-async,
4. guest-completed,
5. returned-to-satellite.

### Thread-Local Stack Discipline

The active callback-frame rule is per satellite callback thread, not one per whole satellite process.

Each satellite callback thread that can enter guest callback handling requires its own host-side callback invocation stack.

Nested invocations on that thread form a stack.

Multi-threaded foreign callback activity is therefore serialized into one guest-facing execution path per callback thread. If multiple satellite callback threads may target one guest, each thread must keep an isolated callback stack and isolated parked-return state. If that mode is not supported, the ABI must reject or constrain it explicitly rather than collapsing multiple threads into one implicit global callback context.

### Reentrant Callback Projection Rule

For a given callback registration there is one guest-visible callback overlapped.

If the guest has taken ownership of an outer invocation asynchronously and the same callback is invoked again reentrantly, the host may reuse the same guest-visible callback overlapped for the new top-most invocation.

That is valid because:

1. the guest should already have copied out the outer invocation arguments if it needed them,
2. durable invocation identity lives in host-side callback frame state,
3. the overlapped is only the transport surface for the current projected invocation or return.

A cross-island call performed while the guest is handling a callback binds to that callback's satellite thread only when the guest explicitly supplies that callback invocation's id as the call's parent invocation id (see DLL Call Resolution). There is no implicit binding by mere timing or by the guest's current call stack.

### Return Ordering And Parked Returns

Completed callback results may not be immediately returnable to the satellite.

The ordering rule is:

1. a callback completion always applies to the innermost unresolved invocation of that callback on that satellite callback thread,
2. but the satellite may only be resumed from the current top frame of that thread-local callback stack,
3. therefore completed non-top frames must be parked in host memory until all frames above them have been returned.

### Example

If the callback thread stack is:

1. `A1`
2. `A2`
3. `A3`
4. `B`

and `B` is still unresolved, but the guest successively completes `A3`, then `A2`, then `A1`, the host must:

1. capture and park the return payloads for `A3`, `A2`, and `A1`,
2. keep waiting for `B`,
3. return `B` first when it completes,
4. then unwind and return parked `A3`, `A2`, and `A1` in stack order.

This is mandatory for correctness.

## Host-Satellite Communication Protocol

The satellite and host communicate callback events through a bidirectional message channel (implementation detail: gob-encoded messages over a socket or pipe).

**Satellite→Host:** When native code invokes a registered callback:
1. The satellite sends a callback invocation message containing:
   - The callback handle (registration identity)
   - The satellite callback thread identity (which thread is invoking this)
   - The invocation identity for that specific callback firing
   - The argument values matching the callback signature
2. The host receives this message and creates a new callback frame in the per-thread stack for that thread.

**Host→Guest:** The host projects the callback into the guest overlapped via memory write.

**Guest→Host:** The guest polls the overlapped, detects completion=1, and yields.

**Host→Satellite:** The host reads the result from the overlapped and sends a completion message back to the satellite with the return value.

The protocol must carry enough identity to let the host associate every projection, completion, and reentrant cross-island call with the correct callback registration, invocation, and satellite callback thread.

### Host Processing At Run Boundaries

At entry to each `run` slice, washmhost must:

1. receive any new callback invocation messages from the satellite,
2. create new callback frames in the appropriate per-thread stack for each invocation,
3. inspect callback overlappeds for newly signaled guest completions from previous projections,
4. project any newly ready top-most callback invocations into their corresponding guest callback overlappeds by writing payloads and setting completion bits.

At exit from each `run` slice, washmhost must:

1. inspect callback overlappeds whose projected invocation was outstanding,
2. if completion is still set, capture the return payload and mark the invocation guest-completed,
3. if completion was cleared, mark the invocation guest-owned-async,
4. parse the invocation id from the overlapped to correctly associate the completion with its frame,
5. park any non-top completed frames in host memory until parent frames above them have been returned,
6. send completion messages to the satellite for any top-most completed frames that can now be returned,
7. unwind parked frames in LIFO order and send their results to the satellite.

This bookkeeping must be maintained per satellite callback thread rather than through one process-global callback queue or one process-global reentrancy mode.

## Responsibilities

### Guest Responsibilities

The guest callback runtime must obey the following rules.

### Callback Discovery And Dispatch

1. Check upon all registered callback overlappeds as part of normal event-loop processing (no special callback wait syscall).
2. When a callback overlapped has its completion flag set to 1, a new invocation is available.
3. Upon detecting a new invocation, the guest **must immediately and atomically**:
   - Copy all callback argument values from the overlapped to guest-owned buffers
   - Queue a new goroutine to execute the callback function with the copied arguments
   - Clear the completion flag to 0
   - Continue polling other overlappeds
4. This extraction and flag reset must complete before the guest re-enters the host or allows any nested callbacks to be projected into the same overlapped.
5. The guest callback runtime must support multiple callback registrations under the same polling discipline rather than depending on application-specific special cases.

### Callback Execution And Return

6. Treat callback overlappeds as reusable transport cells, not persistent invocation storage.
7. Preserve its own bookkeeping of asynchronously executing callback logic and invocation identities.
8. When a callback goroutine finishes execution:
   - Write return data into the callback overlapped
   - Set the completion flag to 1
   - **Immediately exit the current `run()` slice and yield control back to the host**
   - Do not execute additional callbacks or perform other work before yielding
9. This immediate yield ensures the host can read the result before any nested re-invocation overwrites it.
10. Be prepared for a callback overlapped that it currently owns asynchronously to later be reused by the host for a deeper reentrant invocation of the same callback, because the guest already copied out the outer invocation's arguments in step 3.

### Host Responsibilities

Washmhost must:

1. use callback-overlapped signaling as the primary callback mechanism,
2. treat callback overlappeds as signaling cells only,
3. maintain durable invocation stacks per satellite callback thread,
4. copy out completed return payloads when they cannot yet be returned to the satellite,
5. correctly associate completed callback overlappeds with the innermost unresolved invocation of that callback,
6. enforce top-of-stack return order per satellite callback thread,
7. support repeated ping-pong reuse of one callback overlapped per callback registration,
8. carry enough durable frame state to distinguish projected-into-guest, guest-owned-async, guest-completed, and returned-to-satellite states,
9. store the callback thread identity and parent nesting relation for each outstanding frame,
10. avoid process-global callback routing that loses the identity of the active satellite callback thread.

## ABI Contract

The architecture requires at least these ABI families.

### 1. DLL Loading And Symbol Resolution

1. load library,
2. resolve symbol,
3. close library.

### 2. Typed Invocation

1. invoke symbol by name or handle,
2. pass explicit WASM-format signature,
3. pass raw values only.

### 3. Satellite Memory Management

1. allocate native memory,
2. free native memory,
3. write guest bytes to native memory,
4. read native bytes to guest memory.

### 4. Callback Registration And Driving

1. register callback with signature and guest-owned overlapped,
2. produce a satellite-valid callable token,
3. drive invocation and return through callback overlappeds,
4. satellite sends invocation messages (not syscalls) to host,
5. host projects into overlapped and guest polls for new invocations,
6. guest immediately extracts parameters, queues execution, and clears flag,
7. guest yields on callback completion to allow result to be read before re-invocation.

## Guest-Side Go API

The guest-side Go package exposes this ABI directly through `syscall` with 64-bit flat ordinals (`uint64`), avoiding pointer truncation on `wasm32` (where `uintptr` is 32 bits) and eliminating fragile reflection-based wrappers. Everything that is a mechanical consequence of process isolation (memory helpers, allocation) operates directly on 64-bit flat ordinals.

### Library And Symbol Handling

```go
func DylibOpen(path string, flags int) (uint64, error)
func DylibSym(handle uint64, name string) (uint64, error)
func DylibClose(handle uint64) error
```

Each of these issues the matching overlapped host call (`sys_dylib_open`, `sys_dylib_sym`, `sys_dylib_close`) and calls `awaitOverlapped` on it. To the caller the function is ordinary blocking Go code; underneath, the calling goroutine parks and the scheduler runs other goroutines until the satellite responds and the host completes the overlapped.

### Typed Invocation

```go
func DylibCall(sym uint64, sig []byte, args []uint64, parentInvID uint64) ([]uint64, error)
```

Direct, unboxed FFI invocation using canonical WASM function type signatures and 64-bit flat ordinal arguments and results:

1. `sym` is the 64-bit native symbol address in the satellite.
2. `sig` is the canonical WASM functype encoding (`[0x60, param_count, params..., result_count, results...]`).
3. `args` is the ordered list of 64-bit argument ordinals.
4. `parentInvID` is optional (0 to infer from the calling goroutine's active callback context).
5. returns all ordered result ordinals as `[]uint64` without truncation.

### Callback Registration

```go
func DylibRegisterCallback(sig []byte, cbOvPtr unsafe.Pointer, cbOvLen uint32, fn func(args []uint64) []uint64) (uint64, error)
func DylibUnregisterCallback(cbOvPtr unsafe.Pointer)
```

`fn` is a callback function receiving ordered 64-bit argument ordinals and returning result ordinals:

1. `cbOvPtr` is a guest-owned callback overlapped cell registered with runtime continuation.
2. `sys_dylib_callback_register(sigBytes, ovPtr, ovSize)` is issued; the value it returns (a 64-bit native callable trampoline address inside the satellite) is returned to the caller.
3. When the host projects an invocation into the cell, runtime continuation extracts the arguments, clears the completion flag to accept the invocation, and runs `fn`.
4. When `fn` returns, return values are written to the cell, completion is set to 1, and the guest immediately yields back to the host (`pauseToHost()`).

### Implicit Parent Invocation Id

The invocation id a callback goroutine is handling is stored on goroutine-local state maintained by the callback dispatch runtime, not threaded through any public function signature. `DylibCall` reads that state automatically when `parentInvID == 0`, and it is zero for any goroutine not currently inside callback dispatch. This keeps the Reentrant Call Routing rule (see DLL Call Resolution) fully transparent to application code: a callback handler calls into the DLL exactly the way any other Go code would, and the correct routing happens underneath.

### Satellite Memory Management

Under satellite isolation the guest and the satellite are disjoint address spaces, so pointee access always goes through explicit helpers operating on 64-bit flat ordinals:

```go
func DylibAlloc(size uint64) (uint64, error)
func DylibFree(ptr uint64) error

func DylibReadMem(ptr uint64, dst []byte) error
func DylibWriteMem(ptr uint64, src []byte) error

func Alloc(size uint64) (uint64, error)
func Free(ptr uint64) error
func ReadMem(ptr uint64, dst []byte) error
func WriteMem(ptr uint64, src []byte) error

func GoString(ptr uint64) (string, error)
func GoStringN(ptr uint64, length int) (string, error)
func GoBytes(ptr uint64, length int) ([]byte, error)
func CString(s string) (ptr uint64, free func(), err error)
```

`DylibAlloc`/`DylibFree` call the satellite's native platform allocator (see Satellite Memory Management ABI) rather than the satellite's Go heap, so returned pointers carry the platform's standard alignment guarantee and are never subject to Go garbage collection or goroutine stack relocation. `CString` allocates in the satellite, writes the NUL-terminated bytes over via `DylibWriteMem`, and returns a `free` closure that calls `DylibFree`; callers use `defer free()`.

For structured, offset-based traversal of satellite memory, `syscall.RemoteReader` and `syscall.RemoteWriter` provide `io.ReaderAt`/`io.WriterAt`-shaped views over 64-bit ordinal address ranges.

## Future Satellite WASM Use

The same model should then be used for loading arbitrary satellite WASM modules.

### Initial Scope

For now, imported host functions of such loaded WASM modules should be implemented purely as callbacks back into the primary rusticated WASM guest.

That means:

1. a loaded satellite WASM module may have arbitrary imported function shape,
2. washmhost treats those imports as callback registrations and invocations under this same model,
3. the primary guest can decide how to satisfy those imports,
4. if desired, the primary guest may manually forward them to its own rusticated ABI from its own identity.

This avoids hard-coding pass-through of rusticated ABI at the host layer.

### Why This Is Enough Initially

This model is sufficient for early satellite WASM support because it allows:

1. WASI-like modules,
2. rusticated modules,
3. completely custom modules,
4. mixed environments where the primary guest manually adapts one ABI into another.

The host therefore does not need to know whether a loaded satellite module is WASI, rusticated, or something else. It only needs to know:

1. function name,
2. function signature,
3. callback-overlapped delivery and return protocol.

### Long-Term Benefit

Once DLL and future satellite WASM calls both use:

1. WASM-format signatures,
2. raw ordinal values,
3. explicit memory helper ABIs,
4. callback-overlapped async return discipline,

then washmhost gains one consistent cross-island invocation substrate instead of separate ad hoc subsystems.

That consistency is the main purpose of this architecture.

# TEMPORARY FAILURES

## Executive Assessment

The implementation follows the core structural axioms of `CALLBACKS.md` at the host and IPC layers: satellite process isolation, canonical WASM functype encoding, 64-bit flat ordinal pointers, and thread-local stack unwinding are successfully in place. 

However, it is not complete in guest-side runtime execution, memory boundary discipline, and FFI calling conventions:
1. **Broken callback lifecycle**: The guest callback driver uses an unsynchronized in-guest polling loop that triggers a feedback loop on its own returns, rather than synchronizing across `run()` execution boundaries.
2. **Defective Purego mimicry**: The purego wrapper zeros out returned native pointers, forcing downstream consumers ([kabibi/ai_purego_wasip1.go](kabibi/ai_purego_wasip1.go)) to bypass the package and hand-roll bespoke syscall wrappers.
3. **Floating-point ABI corruption**: The satellite dispatcher routes all calls through integer registers via `purego.SyscallN`, corrupting floating-point arguments and returns.
4. **Registration caps and return truncation**: Distinct callback registrations are capped at 16 in the runtime, and outbound cross-island calls truncate multiple return values.
5. **Non-deterministic projection**: Host projection relies on randomized Go map iteration over thread stacks.

---

### 1. Boundary-Driven Overlapped Ownership & Callback Lifecycle
- **Mandate**:
  Callback delivery and return must use guest-owned `Overlapped` cells discovered during normal event-loop continuation. Ownership transitions must be completely deterministic, allowing asynchronous execution and yielding without memory corruption or polling races.
- **Root Cause**:
  1. [purego/purego_wasip1.go](purego/purego_wasip1.go#L77-L135) bypasses the Go runtime's continuation hook ([rusticated-go/runtime/os_rusticated.go](rusticated-go/runtime/os_rusticated.go#L230-L247)). It instead starts an internal background goroutine `monitorLoop()` polling with `runtime.Gosched()`.
  2. When a callback goroutine finishes in [purego/purego_wasip1.go](purego/purego_wasip1.go#L162-L167), it writes its return values to the overlapped cell and sets `completed = 1`. `monitorLoop()` immediately awakes inside the guest, misinterprets the completion flag as a *new* invocation from the host, clears `completed = 0`, and re-executes the callback with stale data.
  3. When `run()` exits, [mohabbat/washmhost/env_dylib.go](mohabbat/washmhost/env_dylib.go#L419-L424) sees `completed == 0`, categorizes the invocation as still in-flight (`FrameStateGuestAsync`), and fails to collect the return payload.
- **Remediation Specification (Boundary-Driven Invariants)**:
  - **Cell-Scoped Ownership**: Ownership of each registered callback overlapped is anchored strictly to `run()` boundaries:
    - **Host Outside `run()`**: The host owns the callback cell during `BeforeRun` and `AfterRun`. It may project new invocations or drain completed returns.
    - **Guest Inside `run()`**: The guest exclusively owns the callback cell during `run()`. The host may continue running background operations (satellite IPC, timers, network), but must never read or write active callback cells.
    - **No Host Polling**: The host must never poll guest overlapped memory while `run()` is executing.
  - **Inline vs. Asynchronous Yielding**:
    - **Inline Completion**: If the guest executes the callback synchronously within the current slice, it leaves `completed = 1` set. At `run()` exit, `AfterRun` drains the result and resets the cell to `Idle`.
    - **Asynchronous Yielding**: If the guest executes the callback asynchronously, `handleContinuation` atomically copies the arguments to the Go heap, clears `completed = 0`, and queues the goroutine. When the goroutine finishes in a subsequent slice, it writes the result, sets `completed = 1`, and calls `pauseToHost()` to yield immediately.
  - **Single-Transaction Rule & Host Parking**: Because each callback registration has exactly one shuttle cell, at most one invocation may be projected into that cell at a time. If additional invocations arrive from other satellite threads while the cell is active (`Projected` or `GuestAsync`), the host must park them in a FIFO queue and only project the next invocation after the preceding transaction reaches `Idle`.

---

### 2. Elimination of Purego Mimicry & 64-Bit Flat Ordinal Enforcement
- **Mandate**:
  Guest-side FFI calls must use 64-bit flat ordinals (`uint64`) across all boundaries, preventing pointer truncation on `wasm32` (where `uintptr` is 32 bits), and interface directly via `syscall` instead of fragile purego wrappers.
- **Root Cause**:
  1. `dylib_rusticated.go` used `uintptr` for library handles, symbol handles, allocated memory pointers, and callback tokens. On `wasm32`, `uintptr` is 32-bit, truncating native 64-bit host/satellite addresses.
  2. Emulating `github.com/ebitengine/purego` via an in-tree package created package resolution dependencies in `go.mod`, introduced pointer reflection zeroing bugs, and added unnecessary indirection.
- **Remediation**:
  - Enforced `uint64` for all library handles, symbols, pointers, and tokens across `rusticated-go/syscall/dylib_rusticated.go`.
  - Moved memory helpers (`Alloc`, `Free`, `CString`, `GoString`, `GoStringN`, `GoBytes`, `RemoteReader`, `RemoteWriter`) directly into `syscall` operating on `uint64` ordinals.
  - Removed in-tree `purego/` package and updated [kabibi/ai_purego_wasip1.go](kabibi/ai_purego_wasip1.go) to use `syscall` and `uint64` ordinals directly.

---

### 3. Distinct Callback Registration Capacity Limit
- **Mandate**:
  The callback runtime must support arbitrary callback shapes and registrations without small fixed limits.
- **Root Cause**:
  [rusticated-go/runtime/os_rusticated.go](rusticated-go/runtime/os_rusticated.go#L196) allocates a fixed array:
  ```go
  var registeredCallbacks [16]*callbackRegistration
  ```
  While the 512-byte buffer easily accommodates up to 58 parameters per call, this array limits the *total number of distinct `NewCallback` registrations* to 16 for the entire process lifetime. Attempting to register a 17th callback silently fails.
- **Remediation Specification**:
  - Replace the fixed-size array with a dynamically expanding slice or locked table in [rusticated-go/runtime/os_rusticated.go](rusticated-go/runtime/os_rusticated.go#L196-L208).
  - Explicitly return an error if registration capacity or memory cannot be allocated.

---

### 4. Floating-Point Calling Convention Invalidation in the Satellite
- **Mandate**:
  WASM signatures support primitive types `f32` and `f64`. Floating-point arguments and returns must adhere to the target architecture's C ABI.
- **Root Cause**:
  In [mohabbat/washmhost/satellite.go](mohabbat/washmhost/satellite.go#L274-L297), `executeCall` casts all arguments to `uintptr`, passes them to `purego.SyscallN(sym, sysArgs...)`, and discards the signature parameter types (`_ = params`).
- **Failure Mode**:
  On x86_64 (System V and Windows x64) and AArch64 (AAPCS64), float and double parameters must be passed in floating-point/vector registers (`XMM0-XMM7` or `D0-D7`). `purego.SyscallN` populates only general-purpose integer registers (`RDI, RSI...` or `X0, X1...`), causing float parameters to be skipped or misaligned and producing corrupted results in floating-point C APIs.
- **Remediation Specification**:
  - Update [mohabbat/washmhost/satellite.go](mohabbat/washmhost/satellite.go#L274-L297) to inspect parameter and result types from `req.SigBytes`.
  - Use architecture-appropriate FFI dispatch (such as dynamic assembly stubs or Cgo/libffi wrappers) that routes `f32` and `f64` ordinals into hardware floating-point registers.

---

### 5. Multiple Return Value Truncation on Outbound Calls
- **Mandate**:
  Cross-island invocations support arbitrary ordered result lists (`[r1, r2, ...]`).
- **Root Cause**:
  While the satellite collects multiple return values in [mohabbat/washmhost/satellite.go](mohabbat/washmhost/satellite.go#L286-L293), the host's `finishOp` in [mohabbat/washmhost/env_dylib.go](mohabbat/washmhost/env_dylib.go#L281) only writes a single 64-bit integer (`resultExt`) into the guest overlapped. [rusticated-go/syscall/dylib_rusticated.go](rusticated-go/syscall/dylib_rusticated.go#L141) returns only `(uint64, error)`, and [purego/purego_wasip1.go](purego/purego_wasip1.go#L46) hardcodes `[]uint64{res}`.
- **Remediation Specification**:
  - Extend the dylib call overlapped contract to support writing multiple result ordinals into a guest return buffer when `len(results) > 1`.
  - Propagate all result ordinals through `syscall.DylibCall` into [purego/purego.go](purego/purego.go#L112-L119).

---

### 6. Non-Deterministic Callback Projection in Host `BeforeRun`
- **Mandate**:
  The host must project the top-most ready invocation into the guest overlapped deterministically.
- **Root Cause**:
  [mohabbat/washmhost/env_dylib.go](mohabbat/washmhost/env_dylib.go#L355-L370) iterates over `m.threadStacks` with a Go map range loop (`for _, stack := range m.threadStacks`), which has randomized order. Furthermore, the inner loop searches forward from index 0 (oldest/outermost frame) rather than inspecting the top frame of each thread stack.
- **Remediation Specification**:
  - Replace randomized map iteration with a deterministic FIFO queue of ready invocations per callback registration.
  - When inspecting thread-local stacks, always project from the top of the stack (`stack[len(stack)-1]`) to preserve strict LIFO nesting.

---

### 7. Inefficient and Non-Transitive Goroutine ID Tracking
- **Mandate**:
  Calls made while servicing a callback must route back to the parked satellite thread using the callback's invocation ID as `parentInvocationID`.
- **Root Cause**:
  [purego/purego.go](purego/purego.go#L53-L67) parses stringified stack traces via `runtime.Stack()` on every outbound FFI call to discover the current goroutine ID.
- **Failure Mode**:
  Parsing stack traces on every FFI call causes severe runtime latency. Furthermore, child goroutines spawned within a callback handler do not inherit the parent invocation ID because the association is strictly keyed by goroutine ID.
- **Remediation Specification**:
  - Provide a fast runtime intrinsic or linkname accessor to read the goroutine ID directly from `g`.
  - Add explicit context propagation (`context.Context` or task-local inheritance) so child goroutines spawned to service an FFI callback inherit the active `parentInvocationID`.
