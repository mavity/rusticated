# `dload` Correctness Audit and Fixes

Here is a complete, line-by-line correctness audit of `dload` (both the Rust worker in `dload/src/` and the Go client in `dload/`) against its specification in `0-migration.md` and against how C/C++ callbacks and `parent_inv_id` actually work.

---

### 1. Is `parent_inv_id` (Invocation ID) Handled in `dload`? **No — It Is Completely Unimplemented**

1. **In Rust (`dload/src/main.rs`):**
   ```rust
   let sym_addr = u64::from_le_bytes(body[0..8].try_into().unwrap());
   let _parent_inv_id = u64::from_le_bytes(body[8..16].try_into().unwrap());
   ```
   `_parent_inv_id` is parsed from the `OP_CALL_REQ` payload and **immediately discarded**.
2. **In Rust (`dload/src/callbacks.rs`):**
   ```rust
   payload.extend_from_slice(&0u64.to_le_bytes()); // thread_id
   ```
   `thread_id` in `OP_CB_INVOKE` is hardcoded to `0`.
3. **In Go (`dload/client.go`):**
   ```go
   func (c *WorkerClient) handleCbInvoke(invID uint64, body []byte) {
       ...
       if fn != nil {
           results = fn(args)
       }
   ```
   `handleCbInvoke` receives `invID` from the frame header, but **never passes `invID` to `fn`**, so the Go callback (`sharedTokenCallback` in `ai/backend_rusticated.go`) doesn't even know its own `invID` and always calls `dload.DylibCall(..., 0)`.

---

### 2. Are Callbacks and Calls From Inside Callbacks Funnelled to the Right Thread? **No — There Is Only One Thread in Rust**

Look at `dload/src/main.rs`:
```rust
    while let Ok((hdr, body)) = read_frame() {
        dispatch_frame(hdr, &body);
    }
```
And look at `dload/src/callbacks.rs`:
```rust
    let _ = send_frame(OP_CB_INVOKE, inv_id, &payload);

    // Wait for main reader thread to signal OP_CB_RETURN for this inv_id
    while CB_RETURN_INV.load(Ordering::Acquire) != inv_id {
        Sleep(1); // or usleep(500)
    }
```
This has **four severe correctness flaws**:

#### Flaw A: Synchronous Callbacks Deadlock 100% of the Time
If any C function called via `OP_CALL_REQ` invokes a registered callback *on the same thread* before returning:
- The `main` Rust thread enters `on_callback()` and spins in `while CB_RETURN_INV.load(...) != inv_id`.
- Because the `main` Rust thread is the **only** thread calling `read_frame()`, **nobody is reading `stdin`**!
- Go receives `OP_CB_INVOKE`, runs the callback, and writes `OP_CALL_REQ` or `OP_CB_RETURN` to `stdin`—which sits unread in the OS pipe buffer forever.

#### Flaw B: Re-entrant Calls (`parent_inv_id != 0`) Run on the Main Thread Instead of the Callback Thread
When an asynchronous C++ thread (like LiteRT-LM's stream worker thread) calls `trampoline_0`:
- The C++ worker thread blocks inside `on_callback()`.
- Go's callback calls `streamChunkGetText(chunkPtr)`, `GoString(textPtr)` (`OP_READ_MEM_REQ`), and `streamChunkIsFinal(chunkPtr)`.
- Those calls are executed by `dload`'s **`main` thread**, **not** by the C++ worker thread that called `trampoline_0`!
- Any C/C++ library that uses thread-local storage (TLS) or thread-affinity checks inside callback helpers (`streamChunkGetText`) will see the wrong thread ID, because the call ran on `dload`'s `main` thread instead of the callback thread.

#### Flaw C: Concurrent / Nested Callbacks Corrupt `CB_RETURN_INV` and Drop Return Values
Look at `dload/src/callbacks.rs`:
```rust
static CB_RETURN_INV: AtomicU64 = AtomicU64::new(0);

pub fn on_cb_return(inv_id: u64) {
    CB_RETURN_INV.store(inv_id, Ordering::Release);
}
```
- There is only a single `AtomicU64` (`CB_RETURN_INV`) for the entire process! If two threads are in callbacks simultaneously (or if a callback triggers a nested callback), storing the inner `inv_id` into `CB_RETURN_INV` can be overwritten before the outer/other thread wakes up from `Sleep(1)`.
- Furthermore, `OP_CB_RETURN`'s payload (`[status][res_count][results...]`) is **not even passed to `on_cb_return`** (`main.rs:314`), and `trampoline_0..7` have signature `unsafe extern "C" fn(usize, usize)` (only 2 arguments, `void` return)—even though `0-migration.md` Section 2.4 specified saving 8 argument registers and returning a `u64` result!

#### Flaw D: `arch_call.rs` Ignores the Signature and Float Arguments
Look at `dload/src/arch_call.rs`:
- `main.rs:201` parses `sig_len` and **skips right over the signature bytes** (`let off = 20 + sig_len;`).
- `arch_call::call_c_func` casts every function pointer to `unsafe extern "C" fn(usize, ...) -> usize`, passing all arguments in integer registers (`rcx, rdx, r8, r9` / `x0..x7` / `rdi..r9`) and reading only the integer return register. (For LiteRT-LM and TFLite C APIs, all arguments and return values happen to be pointers/integers/booleans, which is why integer-only worked, though on 32-bit ARMv7 `usize` is 32-bit while `u64` args are truncated to `usize`).

---

### 3. How `dload` Should Work to Be Truly Sound

To make `dload` genuinely correct for both synchronous callbacks, asynchronous worker-thread callbacks, and thread-affiliated re-entrant calls (`parent_inv_id`):

1. **Funneling Calls into the Callback Thread (or Pumping in `on_callback`):**
   - Each active callback invocation `inv_id` on a thread registers an invocation slot in `callbacks.rs`:
     - Holds `inv_id`, a mailbox for a delegated `OP_CALL_REQ` (when `parent_inv_id == inv_id`), and a completion flag + return `u64` value for `OP_CB_RETURN(inv_id)`.
   - **If `on_callback` is called on the reader thread itself (synchronous callback):**
     - `on_callback` directly calls `read_frame()` / `dispatch_frame()` in a loop until `OP_CB_RETURN(inv_id)` arrives! That way synchronous callbacks on the main thread **never deadlock**—they recursively pump incoming frames on the exact same thread until `OP_CB_RETURN(inv_id)` completes!
   - **If `on_callback` is called on a C++ background thread (like LiteRT-LM's stream thread):**
     - While waiting for `OP_CB_RETURN(inv_id)`, if `main`'s `dispatch_frame` receives an `OP_CALL_REQ` whose `parent_inv_id == inv_id`, `main` hands that `OP_CALL_REQ` to that callback slot's mailbox; the C++ callback thread wakes up, executes `call_c_func` **on the actual C++ callback thread itself**, sends `OP_CALL_RESP`, and goes back to waiting for `OP_CB_RETURN(inv_id)`!
2. **Passing `inv_id` Through to Go Callbacks:**
   - In Go (`dload`), `handleCbInvoke(invID, body)` passes `invID` (or stores it in a callback context / passes it to `DylibCall`) so any `DylibCall` made inside `sharedTokenCallback` automatically carries `parentInvID = invID` and executes on the C++ callback thread!
3. **Serialized Cancellation / Outer Calls (`parent_inv_id == 0`):**
   - When an outer call (`parent_inv_id == 0`, like `litert_lm_conversation_cancel_process` from `c.abort()`) arrives while a C++ thread is currently inside `on_callback`:
   - If the `main` thread blocks inside `call_c_func(litert_lm_conversation_cancel_process)` while the C++ callback thread is waiting for `OP_CB_RETURN`, `main` would stop reading `stdin` and never read `OP_CB_RETURN`!
   - Therefore, either:
     - `OP_CALL_REQ` with `parent_inv_id == 0` is dispatched or queued so the `stdin` reader thread **never blocks** while any callback slot is active (or `c.abort()` in Go waits for the active callback to send `OP_CB_RETURN` before sending `convCancelProcess`).

## Testing Strategy for `dload` Fixes

Yes—every single one of these failure modes can and should be tested deterministically in fast unit tests (under 1 second, with zero LLM/GPU dependency) using only standard OS libraries (`kernel32.dll` / `libc`) and `dload`'s own trampolines!

Here is how we can test every case both finely-grained and holistically without needing a 3 GB model file:

### 1. How to Test `dload` Callbacks & Thread Funneling Using Only `kernel32.dll` / `libc` (or Self-Calling Trampolines)

Notice a neat trick: a registered trampoline (`trampolinePtr := DylibRegisterCallback(...)`) is **itself a valid C function pointer inside the `dload` worker process**!
And on Windows (`kernel32.dll`) / Unix (`libc` / `libpthread`), we also have standard OS functions like `GetCurrentThreadId()` (`pthread_self()`), `CreateThread` (`pthread_create`), and `EnumSystemLocalesA` / `qsort` (which take a C callback function pointer and call it synchronously on the calling thread!).

That gives us **six deterministic, fine-grained unit tests** in `dload/dload_test.go` that run on both native Go and inside `washmhost`:

1. **`TestDloadSynchronousCallbackOnMainThread` (Catches Flaw A — main-thread deadlock):**
   - Register a Go callback via `DylibRegisterCallback` -> returns `trampolinePtr`.
   - Call `DylibCall(trampolinePtr, ...)` directly (or via `qsort` / `EnumSystemLocalesA`).
   - On the current code, `DylibCall(trampolinePtr)` **deadlocks 100% of the time** because `dload`'s main thread calls `trampoline_0`, which spins waiting for `OP_CB_RETURN` while nobody reads `stdin`!
   - With the fix, `DylibCall(trampolinePtr)` pumps frames on the main thread, invokes the Go callback, and returns the callback's return value cleanly.

2. **`TestDloadCallbackReentryFunnelsToSameOSThread` (Catches Flaw B — `parent_inv_id` thread affinity):**
   - Look up `GetCurrentThreadId` in `kernel32.dll` (or `pthread_self` on Unix).
   - Spawn a background OS thread inside the worker (`CreateThread` / `pthread_create` pointing at `trampolinePtr`), or invoke `trampolinePtr` on a worker thread.
   - Inside the Go callback (using `parentInvID = invID`), call `DylibCall(symGetCurrentThreadId, ..., invID)`.
   - Assert that the OS thread ID returned by `GetCurrentThreadId` inside the re-entrant call **equals the OS thread ID of the thread that entered `on_callback`**, proving `parent_inv_id` actually funnelled execution onto the callback's exact OS thread!

3. **`TestDloadNestedCallbacks` (Catches Flaw C — single `CB_RETURN_INV` overwrite):**
   - Register callback A and callback B.
   - Call `trampolineA` (`inv_id = 1`). Inside Go's handler for A, call `DylibCall(trampolineB, ..., invID_A)` (`inv_id = 2`).
   - Inside Go's handler for B, read/write worker memory and return a distinct value `0xBEEF`, then A returns `0xCAFE`.
   - Assert both inner (`inv_id = 2`) and outer (`inv_id = 1`) callbacks complete in LIFO order without corrupting each other's completion state or return values.

4. **`TestDloadConcurrentBackgroundCallbacks` (Catches Flaw C — multi-thread callback race):**
   - Launch 4 concurrent native threads in the worker (via `CreateThread` / `pthread_create` calling `trampolinePtr`), each firing 50 callbacks concurrently while making re-entrant `DylibReadMem` / `DylibCall` requests.
   - Assert all 200 callbacks complete without a single dropped `inv_id` or deadlock.

5. **`TestDloadOuterCallWhileCallbackBlocked` (Catches the Exact LiteRT-LM Cancel Deadlock):**
   - Start a callback on a worker thread (`inv_id = 1`).
   - Inside the Go callback, simulating `loopDetector` triggering `cancel()`, have a second Go goroutine issue an outer `DylibCall` (`parentInvID = 0`) that waits on a lock held by the callback thread (or tries to cancel the stream) **before** the Go callback returns.
   - Assert that `dload`'s stdin reader is **never** blocked by an outer call while a callback is in flight, and that `st.stop()` in `ai/backend.go` waits until the entire `sharedTokenCallback` has returned (`opCbReturn` sent) before `convCancelProcess` is called!

6. **`TestStreamAbortFromInsideChunkCallbackDoesNotDeadlock` in `ai/backend_stream_test.go` (Holistic Stream + LoopDetector Test):**
   - Simulate `drvConversationBackend.SendStream` where a C/mock callback thread holds a lock for the duration of `sharedTokenCallback`, emits a repeating token that triggers `loopDetector` (`cancel()`), and `convCancelProcess` attempts to acquire that same lock.
   - On the current `backend.go` (where `st.deliver` closes `st.bodyDone` *before* `streamChunkIsFinal` and *before* `sharedTokenCallback` returns), this test **deadlocks immediately**.
   - With `sharedTokenCallback` guarding the entire callback body before signalling `bodyDone`, `convCancelProcess` is guaranteed to run only after the callback thread has exited `sharedTokenCallback` and released its lock.

---

## 4. Detailed Engineering Specification for `dload` & `ai` Fixes

### 4.1 Rust Worker: Invocation Slot Table & Thread-Affiliated Dispatch (`dload/src/callbacks.rs`, `dload/src/main.rs`)

#### 4.1.1 OS Thread ID Discovery (`os_win.rs` / `os_unix.rs`)
Each worker flavour exposes `pub fn current_thread_id() -> u64`:
- **Windows (`os_win.rs`):** Calls `GetCurrentThreadId() -> u32` from `kernel32.dll` (`raw-dylib`), cast to `u64`.
- **Linux (`os_unix.rs`):** Executes raw `SYS_gettid` syscall (`186` on `x86_64`, `178` on `aarch64`, `224` on `arm`).
- **macOS (`os_unix.rs`):** Calls `pthread_mach_thread_np(pthread_self())` or `pthread_self() as usize as u64` from `libSystem.B.dylib`.

When `run()` starts in `dload/src/main.rs`, it records `READER_THREAD_ID.store(os::current_thread_id(), Ordering::Release)` before entering the main `read_frame()` loop.

#### 4.1.2 Active Callback Invocation Table (`callbacks.rs`)
Replace the single global `CB_RETURN_INV: AtomicU64` with a fixed lock-free/spinlocked table of `MAX_ACTIVE_INVOCATIONS = 32` slots so concurrent and nested callbacks never overwrite each other:

```rust
pub struct DelegatedCall {
    pub req_id: u64,
    pub sym_addr: usize,
    pub args: [u64; 8],
    pub argc: usize,
}

pub struct InvocationSlot {
    pub state: AtomicU32, // 0 = Free, 1 = Waiting, 2 = HasCall, 3 = Returned
    pub inv_id: AtomicU64,
    pub thread_id: AtomicU64,
    pub return_val: AtomicU64,
    pub call: UnsafeCell<DelegatedCall>,
}
```
State transitions for an `InvocationSlot`:
1. **Allocation on Callback Entry (`on_callback`):**
   - Generates `inv_id = NEXT_INV_ID.fetch_add(1, Ordering::SeqCst)`.
   - Claims a free slot (`state: 0 -> 1`), stores `inv_id` and `thread_id = os::current_thread_id()`.
   - Sends `OP_CB_INVOKE` with header `id = inv_id` and payload `[cb_id: u64][thread_id: u64][argc: u32][args: u64 * 8]`.
2. **Waiting & Re-entrant Execution Loop (`on_callback`):**
   - **Case 1 — Synchronous Callback on the Reader Thread (`thread_id == READER_THREAD_ID`):**
     - Because this callback was invoked *by* `call_c_func` on the main reader thread itself, nobody else is reading `stdin`!
     - `on_callback` loops:
       - If `slot.state == 3` (`Returned`), reads `ret = slot.return_val`, resets `slot.state = 0`, and returns `ret` to the C caller.
       - If `slot.state == 2` (`HasCall`), executes `call_c_func` directly, sends `OP_CALL_RESP`, sets `slot.state = 1`, and continues.
       - Otherwise, calls `read_frame()` and `dispatch_frame(hdr, &body)` directly on this thread!
       - If a nested synchronous callback (`trampoline_B`) is triggered inside `dispatch_frame`, the inner `on_callback` allocates a second `InvocationSlot` and recursively pumps `read_frame()` until the inner `inv_id` receives `OP_CB_RETURN`, then returns to the outer `on_callback` loop!
   - **Case 2 — Asynchronous Callback on a Background C/C++ Thread (`thread_id != READER_THREAD_ID`):**
     - The main reader thread is still running `while let Ok((hdr, body)) = read_frame()`.
     - The background C++ thread loops until `slot.state` leaves `1` (`Waiting`):
       - If `slot.state == 2` (`HasCall`): a re-entrant `OP_CALL_REQ` with `parent_inv_id == inv_id` (or matching `thread_id`) was routed to this slot by `dispatch_frame`. The C++ callback thread itself executes `arch_call::call_c_func(call.sym_addr, &call.args[..call.argc])`, sends `OP_CALL_RESP(call.req_id, ret_val)`, transitions `slot.state: 2 -> 1`, and continues waiting!
       - If `slot.state == 3` (`Returned`): reads `ret = slot.return_val`, releases the slot (`slot.state = 0`), and returns `ret` to the C++ caller.
3. **Routing `OP_CALL_REQ` in `dispatch_frame` (`main.rs`):**
   - Parse `parent_inv_id = u64::from_le_bytes(body[8..16])`.
   - **If `parent_inv_id != 0`:**
     - Find the active `InvocationSlot` with `slot.inv_id == parent_inv_id`.
     - If `slot.thread_id == os::current_thread_id()` (we are already on that thread inside a synchronous pump), execute `call_c_func` immediately and send `OP_CALL_RESP`.
     - Otherwise (the target callback is on a background C++ thread waiting in Case 2), write `DelegatedCall { req_id: hdr.id, sym_addr, args, argc }` into `slot.call` and transition `slot.state: 1 -> 2` (`HasCall`)! The `main` reader thread returns **immediately** to `read_frame()` without blocking, while the background C++ callback thread executes the call on its own OS thread and sends `OP_CALL_RESP`!
   - **If `parent_inv_id == 0` (Outer call):**
     - Execute on the main thread (or if an asynchronous callback is currently active in `Waiting`, see Section 4.3 below for how `backend.go` + `dload` prevent outer cancellation from blocking `stdin`).
4. **Handling `OP_CB_RETURN` in `dispatch_frame` (`main.rs`):**
   - Parse `ret_val` from `body` (if `body.len() >= 13` and `res_count >= 1`, `u64::from_le_bytes(body[5..13])`, else `0`).
   - Find the active `InvocationSlot` with `slot.inv_id == hdr.id`, store `slot.return_val = ret_val`, and transition `slot.state = 3` (`Returned`).
5. **Expanding Trampoline Signatures (`callbacks.rs`):**
   - Expand `TRAMPOLINES` from 8 slots of `(usize, usize) -> ()` to 16 slots of `unsafe extern "C" fn(usize, usize, usize, usize, usize, usize, usize, usize) -> usize`, passing all 8 register arguments and returning the `usize` (`u64`) result from `on_callback`.

---

### 4.2 Go Client: Propagating `invID` for Re-Entrant Calls (`dload/dload.go`, `dload/client.go`, `ai/backend_rusticated.go`)

1. **Callback Context & `invID` Tracking in `dload`:**
   - In `dload/client.go`, when `handleCbInvoke(invID uint64, body []byte)` runs in its goroutine:
     - Store the active `invID` (or pass `invID` directly to callbacks registered via `DylibRegisterCallback` / an `activeInvID` atomic/context helper).
     - Even simpler and zero-allocation for callers:
       - Provide `DylibCall(sym uint64, sig []byte, args []uint64, parentInvID uint64)` (which already accepts `parentInvID`!).
       - Add `CurrentCallbackInvID() uint64` or pass `invID` into `sharedTokenCallback` by storing the active callback's `invID` in `rusticatedDriver` (or passing it via a callback registration helper `DylibRegisterCallbackWithInvID` / `atomic.Uint64` per active callback).
       - In `ai/backend_rusticated.go`, `rusticatedDriver` tracks the current callback's `invID` (or passes it through `callDylib`) so every `streamChunkGetText(chunkPtr)` and `streamChunkIsFinal(chunkPtr)` made during `sharedTokenCallback` passes `parentInvID = activeInvID` instead of `0`!

---

### 4.3 Fixing the Stream Cancellation / `convCancelProcess` Race (`ai/backend.go`, `ai/session.go`)

Even with `parent_inv_id` funnelled onto the C++ callback thread, look at what happens when `loopDetector` (or caller `ctx` cancellation) aborts a stream:
1. In `ai/backend.go`:
   - Currently, `sharedTokenCallback` calls `st.deliver(token)` **before** calling `drv.streamChunkIsFinal(chunkPtr)`, and `st.deliver(token)` transitions `deliveryActive -> deliveryIdle` (or closes `st.bodyDone`) **before** `sharedTokenCallback` returns to `dload` (`opCbReturn`).
   - That allows `c.abort()` on the `SendStream` goroutine to unblock from `st.stop()` and call `c.drv.convCancelProcess(c.conv)` (`parent_inv_id = 0`) **while `sharedTokenCallback` is still running `streamChunkIsFinal` and before `opCbReturn` has been sent to `dload`**!
   - When `dload`'s main reader thread receives `OP_CALL_REQ(litert_lm_conversation_cancel_process, parent_inv_id=0)` before `OP_CB_RETURN`, `dload`'s main thread calls `litert_lm_conversation_cancel_process`, which blocks inside C++ waiting for the C++ stream callback thread to exit `on_callback()`—while the C++ stream callback thread is waiting for `dload`'s main thread to read `OP_CB_RETURN`!
2. **The Two-Part Fix (Defense in Depth):**
   - **Part 1 (`ai/backend.go` — `sharedTokenCallback` lifecycle):**
     - Move the `deliveryIdle -> deliveryActive` guard to wrap the **entire** `sharedTokenCallback(drv, userData, chunkPtr)` function!
     - Specifically: `st.beginCallback()` at the very top of `sharedTokenCallback` (before `streamChunkGetText`), and `defer st.endCallback()` at the very end of `sharedTokenCallback` (after `streamChunkIsFinal` and `doneCh` send).
     - `st.stop()` waits for `<-st.bodyDone` (which is closed by `st.endCallback()`).
   - **Part 2 (`dload/src/main.rs` — Non-blocking Outer Calls While a Callback Is Active):**
     - What if an external goroutine (e.g., user pressing `Ctrl+C` on `ctx.Done()`) sends `OP_CALL_REQ(litert_lm_conversation_cancel_process, parent_inv_id = 0)` at the exact microsecond that the C++ stream thread just emitted `OP_CB_INVOKE` over `stdout`?
     - If `dload`'s main reader thread calls `arch_call::call_c_func` synchronously while any background callback slot is in `state == 1` (`Waiting`), and that C function waits on the callback thread's lock, the main reader thread will block and never read `OP_CB_RETURN`!
     - **Solution in `dload`:** When `OP_CALL_REQ` arrives with `parent_inv_id == 0` while any background callback slot is active (`callbacks::has_active_background_callback()`), either:
       1. Spawn a short-lived OS worker thread (`CreateThread` / `pthread_create`) to execute that `parent_inv_id == 0` `call_c_func` and send `OP_CALL_RESP` when it finishes, keeping the `main` stdin reader thread 100% unblocked so it can always read `OP_CB_RETURN`! **OR**
       2. In Go (`dload/client.go`), serialize `opCallReq` (`parentInvID == 0`) behind any in-flight `handleCbInvoke` on the same `WorkerClient` so Go never sends a `parentInvID == 0` `opCallReq` down the pipe while an `opCbInvoke` is currently being handled in Go and hasn't sent `opCbReturn` yet!
     - Doing **both** (serializing `parentInvID == 0` behind active `opCbInvoke` in `dload/client.go` **and** spawning a worker thread in Rust when an outer call runs concurrently) guarantees 100% freedom from pipe/reader deadlocks under all possible interleavings!
3. **Reverting the `8 KiB` `compressLargeBlock` Workaround in `ai/session.go`:**
   - Remove the proactive `len(b) > 8192 -> s.compressLargeBlock` loop in `runStage5Execution` (`ai/session.go:1658-1668`) so tool outputs (like `README.md`) are not crushed into 20-word gists or subjected to 3 extra LLM compressor calls mid-turn.

---

## 5. Total Test Coverage Specification

All tests below must run **both** natively (`go test`) and inside `washmhost` (`go run . ./dload --test` and `go run . ./ai --test`), with zero skips and strict timeouts (`<= 5s` per test) so any deadlock fails loudly and immediately.

### 5.1 `dload/dload_test.go` — Fine-Grained & Stress Unit Tests

1. **`TestDloadSynchronousCallbackOnMainThread` (Verifies Flaw A Fix & Return Value):**
   - Registers a callback `cb` via `DylibRegisterCallback` that receives `(a0, a1)` and returns `a0 + a1 + 42`.
   - Calls `DylibCall(trampolinePtr, sig, []uint64{100, 200}, 0)` directly from Go (causing `dload`'s main reader thread to call `trampoline_0(100, 200)` synchronously).
   - Verifies that `on_callback` detects `current_thread_id() == READER_THREAD_ID`, pumps `OP_CB_RETURN` without deadlocking, and returns `342`.

2. **`TestDloadSynchronousCallbackWithReentrantCallsAndMemoryOps` (Verifies Re-entrant `DylibCall` + `DylibReadMem` + `DylibWriteMem`):**
   - Registers a callback that, while inside the callback:
     - Calls `DylibAlloc`, `DylibWriteMem`, `DylibReadMem`, and `DylibFree`.
     - Calls `GetCurrentProcessId` / `getpid` via `DylibCall(..., parentInvID)`.
   - Invokes `trampolinePtr` via `DylibCall` and asserts the inner memory round-trip and PID call succeed and return the expected value.

3. **`TestDloadNestedCallbacksLIFO` (Verifies Flaw C Fix — Multi-Slot Table):**
   - Registers two distinct callbacks: `cbOuter` and `cbInner`.
   - Calls `trampolineOuter(10, 20)`.
   - Inside `cbOuter`, makes a re-entrant `DylibCall(trampolineInner, ..., []uint64{3, 4}, outerInvID)`.
   - Inside `cbInner`, verifies args `(3, 4)` and returns `3 * 4 = 12`.
   - `cbOuter` receives `12` from the inner `DylibCall` and returns `10 + 20 + 12 = 42`.
   - Asserts the outer `DylibCall` returns `42` without slot collision.

4. **`TestDloadBackgroundThreadCallbackFunnelsParentInvIDToSameOSThread` (Verifies Flaw B Fix — Strict OS Thread Affinity):**
   - Resolves `CreateThread` + `WaitForSingleObject` + `GetCurrentThreadId` in `kernel32.dll` (or `pthread_create` + `pthread_join` + `pthread_self` on Unix).
   - Registers a callback `cbWorker`.
   - Spawns a real native OS thread inside the `dload` worker whose entry point is `trampolineWorker`.
   - When `cbWorker` is invoked in Go:
     - Reads the `thread_id` reported in `OP_CB_INVOKE`.
     - Calls `GetCurrentThreadId` / `pthread_self` with `parentInvID = invID` (funnelled to the callback thread) and with `parentInvID = 0` (executed on the reader/main thread).
     - Asserts that:
       1. `tid(parentInvID == invID) == callback_thread_id` (proving the re-entrant call ran on the exact OS thread of the callback!).
       2. `tid(parentInvID == 0) != callback_thread_id` (proving the callback is genuinely on a separate background OS thread from the main reader thread!).

5. **`TestDloadConcurrentBackgroundCallbacksStress` (Verifies Multi-Thread Slot Concurrency):**
   - Spawns 4 native OS threads via `CreateThread` / `pthread_create` pointing at `trampolinePtr` (or loops 50 parallel callback invocations across multiple goroutines and native threads), each performing re-entrant `DylibReadMem` and `DylibCall(..., invID)` calls and returning a unique checksum.
   - Asserts all invocations complete with exact checksums and zero timeouts.

6. **`TestDloadOuterCallWhileCallbackInFlightDoesNotDeadlock` (Verifies Section 4.3 Pipe Serialization):**
   - Starts a callback on a background thread (`CreateThread` -> `trampolinePtr`).
   - While the Go callback is paused inside `handleCbInvoke` (before `opCbReturn` is sent), a second Go goroutine issues an outer `DylibCall(..., parentInvID = 0)` that would contend or run concurrently.
   - Verifies that `opCbReturn` is never starved behind the outer call and both complete within `< 1s`.

### 5.2 `ai/backend_stream_test.go` — Holistic Stream & Cancellation Tests

7. **`TestStreamAbortFromInsideChunkCallbackWaitsForFullCallbackExit`:**
   - Uses a mock `LMDriver` where the simulated C stream thread holds a `sync.Mutex` (`nativeStreamMu`) across the **entire** `sharedTokenCallback(drv, userData, chunkPtr)` call (including `streamChunkGetText` **and** `streamChunkIsFinal`), and where `convCancelProcess` acquires `nativeStreamMu` (mirroring LiteRT-LM's internal C++ conversation mutex).
   - Feeds repeating tokens so `Conversation.SendStream`'s `loopDetector` triggers `cancel()` inside `st.deliver(token)`, while `streamChunkIsFinal` is deliberately delayed by `20ms` after `st.deliver(token)` returns.
   - Without the `sharedTokenCallback` lifecycle fix, `c.abort()` calls `convCancelProcess` during that `20ms` window while `nativeStreamMu` is still held by `sharedTokenCallback`, causing an immediate deadlock/test failure.
   - With the fix, `st.stop()` waits until `sharedTokenCallback` has completely exited and released `nativeStreamMu` before calling `convCancelProcess`, completing cleanly in `< 100ms`.

8. **Manual Verification of the Original `-p` Ukrainian Poem Prompt:**
   - After all unit tests pass on both native Go and inside `washmhost`, run the exact command:
     `go run . kabibi -r -p "Now review the project's sources and codebase, plus documentation in this project that is nestled in this directory, and write a short and succint summary in a form of a small-sized to medium-sized poem in Ukrainian language. Sophisticated? Yes."`
   - Verify it completes end-to-end without hanging on `[work]`.


