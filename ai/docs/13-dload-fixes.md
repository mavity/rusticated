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

### Proposed Plan (For Your Approval Before Any Code Changes)

1. **`dload` Rust Worker (`dload/src/callbacks.rs` & `dload/src/main.rs`):**
   - Replace the single global `CB_RETURN_INV: AtomicU64` with a fixed table of active callback invocation slots (`[CallbackSlot; 32]`), each tracking `inv_id`, `thread_id`, a delegated call mailbox (`sym_addr`, `args`, `result`), and `returned` / `return_val`.
   - Record the reader thread's OS thread ID (`GetCurrentThreadId` / `gettid` / `pthread_self`).
   - In `on_callback`:
     - Send `OP_CB_INVOKE` with the real OS `thread_id` and up to 8 arguments, and return the callback's `u64` result.
     - If `current_thread_id == reader_thread_id` (synchronous callback on main thread), pump `read_frame()` / `dispatch_frame()` directly until this slot's `inv_id` receives `OP_CB_RETURN`!
     - If on a background thread (async C++ thread), loop waiting until either:
       - A delegated `OP_CALL_REQ` with `parent_inv_id == inv_id` is placed in this slot's mailbox -> execute `call_c_func` **on this callback thread**, send `OP_CALL_RESP`, and continue waiting; or
       - `OP_CB_RETURN(inv_id)` arrives -> return `return_val` to C++.
2. **`dload` Go Client (`dload/client.go` & `dload/dload.go`):**
   - Pass `invID` into registered callbacks (or provide `DylibRegisterCallbackWithInvID` / thread-local callback context) so re-entrant `DylibCall`s pass `parentInvID = invID`.
3. **`ai/backend.go` (`sharedTokenCallback` & `streamState`):**
   - Move the `deliveryIdle -> deliveryActive` transition and `close(st.bodyDone)` to wrap the **entire** `sharedTokenCallback` (so `st.stop()` in `c.abort()` never unblocks until `sharedTokenCallback` has finished all `drv.streamChunk*` calls and is returning `opCbReturn`).
   - Revert the proactive `8 KiB` `compressLargeBlock` snippet in `ai/session.go`.
4. **Unit Tests (`dload/dload_test.go` & `ai/backend_stream_test.go`):**
   - Implement all 6 unit tests above and run them on both native Go and inside `washmhost` (`go run . ./dload --test` and `go run . ./ai --test`).

