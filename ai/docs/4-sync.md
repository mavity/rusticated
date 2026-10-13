# 4. Synchronisation Model: Lock-Free Streaming, Cancellation and Teardown

Status: **IMPLEMENTED** (`ai/backend.go`, `ai/conversation.go`, `ai/engine.go`, both drivers). Where the implementation differs from the first plan, the differences are listed in section 9.

## 1. Motivation

A generation can get stuck in a degenerate loop when the context is stretched too far. Pope needs to detect that, interrupt the generation, compact, and retry the turn. That requires a real cancellation path. LiteRT-LM has one (`litert_lm_conversation_cancel_process`), but the Go backend does not bind it, and the current streaming path carries machinery that is hard to reason about:

- a global `tokenCallbackRegistry` map guarded by `tokenCallbackMu`;
- an event channel plus a worker goroutine in `Conversation.SendStream`;
- `SendStream` that ignores `ctx` and blocks until the terminal chunk.

## 2. Facts about LiteRT-LM that constrain the design

All references are to `external/LiteRT-LM`.

1. **Streaming is asynchronous.** `litert_lm_conversation_send_message_stream` is documented as non-blocking; callbacks arrive on a background thread (`c/conversation.h`). The FFI call returns *before* the final callback. Completion is signalled only by the terminal chunk (`is_final == true`, `text == NULL`; `error_msg` set on failure).
2. **Cancellation is cooperative and reported through the stream.** `litert_lm_conversation_cancel_process` returns `void`; `CancelAllTasksInSession` only sets an atomic `cancelled` flag per task (`threaded_execution_manager.cc`). A cancelled task later produces `absl::CancelledError("Task cancelled")`, which the C adapter turns into a terminal chunk carrying an error (`c/conversation.cc`). Cancellation may lose the race against normal completion.
3. **A cancelled conversation is poisoned.** Upstream: "Reusing the Conversation object after calling CancelProcess() is neither recommended nor supported" (`runtime/conversation/conversation.h`). After cancel, the conversation must be discarded and replaced.
4. **Callbacks run on the engine's single callback-pool thread** (`callback_thread_pool_`, one thread; execution has its own single thread). A slow callback stalls all callbacks.
5. **Deleting a conversation waits on its tasks.** `~SessionAdvanced` calls `WaitUntilDone()` and then releases the session (`runtime/core/session_advanced.cc`). Consequence: `convDelete` called *from inside* a callback can deadlock on the callback it is running in.
   - *Assumption, not yet verified:* `WaitUntilSessionDone` also waits for a callback still executing on the callback pool. The teardown design below relies on it; confirm by reading `WaitUntilSessionDone` or by a stress test before relying on it.

## 3. Invariants

1. **Single source of truth for validity.** The conversation handle is held in an atomic. Once it is swapped to `nil`, every later `SendStream` fails fast without touching native code.
2. **No Go mutex held across an FFI call** (`convSendStream`, `convCancelProcess`, `convDelete`).
3. **No global registry.** `userData` identifies the request's `*streamState` directly. No `tokenCallbackRegistry`, no `tokenCallbackMu`.
4. **`streamState` is anchored explicitly.** The trampoline is registered once per process (native: purego callbacks cannot be created per request; WASI: a single `DylibRegisterCallback` behind a `sync.Once`). So there is **no per-request closure** that keeps `st` alive. The GC does not see `userData` held by C. `st` must be pinned (`runtime.Pinner`) from before `convSendStream` until after the last possible callback, and unpinned only after `convDelete` has returned.
5. **No callback after cancel reaches user code.** Late callbacks after a cancel are by design, not a C bug. `st.cancelled` is an atomic bool, checked before every `onChunk`/`yield`. Calling `yield` after the loop exited is a runtime panic.
6. **No native deletion inside a callback frame.** The callback only signals.

## 4. Layering

### 4.1 Low level: `backend.go`, `backend_native.go`, `backend_wasip1.go`

- `streamState`: `onChunk`, `cancelled atomic.Bool`, `doneCh chan error` (1-buffered; completion signal only, never token transport).
- `drvConversationBackend` carries the active stream as `atomic.Pointer[streamState]`; starting a stream is `CompareAndSwap(nil, st)`, a second concurrent stream is an error.
- `userData` is the pinned `*streamState` (as `uint64` on WASI). The trampoline recovers it and calls `sharedTokenCallback`. Converting `uintptr` back to a pointer will trip `go vet` (`unsafeptr`); do this in one small, commented helper, or use a `runtime/cgo.Handle`-style indirection if vet cannot be satisfied.
- Bind `litert_lm_conversation_cancel_process` in both drivers (`purego.RegisterLibFunc` natively; `syscall.DylibSym` on WASI) and add `convCancelProcess` to `LMDriver`.
- Check `ctx.Err()` **before** `convSendStream`. Cancelling before a task exists is a native no-op.

### 4.2 High level: `conversation.go`

`ai.go` only holds the engine; the stream code is in `conversation.go`. The stream-count helpers named in earlier drafts (`acquireStream`, `releaseStream`, `onDone`, `closeAndInvalidateAsync`, `ErrClosed`) do **not** exist in this repository, so nothing is "deleted" for them; `ErrClosed` is new.

- Remove the event channel, the worker goroutine and the drain loop from `SendStream`.
- Parse each chunk into `ModelEvent`s and call `yield` directly in the callback frame.
- `Conversation.impl` becomes an atomic pointer. `SendStream` loads it; `nil` means the conversation is closed or invalidated and the call fails fast with `ErrClosed` without starting a stream.

## 5. Lifecycle

### 5.1 Normal completion

1. `SendStream` pins `st`, CASes it into `active`, calls `convSendStream`.
2. Native thread calls the trampoline per chunk; `onChunk` -> `yield` in-frame.
3. Terminal chunk: `sharedTokenCallback` sends `nil` or the native error to `doneCh`.
4. `SendStream` receives from `doneCh`, clears `active`, unpins `st`, returns.

### 5.2 Cancellation (`ctx.Done()`, `yield` returning false, or the loop detector)

1. Set `st.cancelled`; call `convCancelProcess`; swap the conversation handle to `nil`.
2. Return to the caller immediately (`ctx.Err()` for `ctx`).
3. Later `SendStream` calls on this `Conversation` fail fast with `ErrClosed`. No stream is started and the native layer is not touched.
4. The terminal chunk eventually arrives. `sharedTokenCallback` sees `cancelled`, skips `onChunk`, and signals `doneCh`.
5. **A teardown goroutine owned by the cancel path** (not spawned from inside the callback) receives from `doneCh`, then calls `convDelete`, then unpins `st`. The order matters: `convDelete` returns before `st` is unpinned, because a tail callback may still read it.

This is the only asynchronous element in the design, and it exists solely because `convDelete` must not run in the callback frame and must not run before native unwinding.

### 5.3 `Close()` while a stream is active

Same as 5.2: invalidate, cancel, let the teardown goroutine delete after `doneCh`. `Close()` with no active stream calls `convDelete` directly.

### 5.4 Pope's retry loop

Detecting a degenerate loop or the KV-limit error cancels the stream, then Pope compacts and creates a **new** `Conversation` (the old one is poisoned) and retries the turn. A retry cap is required so a failing compaction cannot loop forever.

## 6. Decisions (agreed)

1. **An early `break` invalidates the conversation.** This matches LiteRT-LM, where a cancelled conversation is poisoned. `TestSendStreamEarlyBreakDrainsUntilGenerationFinishes` and the `SendStream` doc comment are replaced accordingly. DECIDED: accepted.
2. **The loop body runs on the native callback thread.** A slow body delaying later chunks is inherent to ordered text delivery. Accepted. Consequences that remain the caller's responsibility:
   - a panic in the body unwinds through C++ frames and kills the process;
   - `Close()` from inside the body is allowed: it only signals, never waits.
3. **`ErrClosed` reporting.** DECIDED:
   - `SendStream` on an already-closed conversation: the backend returns `ErrClosed` without starting a stream; the public iterator, which can only report errors by yielding, yields `(nil, ErrClosed)` once.
   - Conversation closed *in the middle of* a stream: `ErrClosed` is yielded in the loop and the iteration ends.

## 7. Out of scope

- Any rollback or reuse of a cancelled conversation (upstream does not support it; see TODO b/450903294).
- Changing `Send`/`SendText`.

## 8. Verification

Done:

- Unit tests with a fake driver (`ai/backend_stream_test.go`): normal completion and reuse; callbacks arriving before `convSendStream` returns; terminal-chunk error; pre-cancelled ctx never starts a stream; ctx cancel returns early, drops later chunks and deletes only after the terminal chunk; cancel waits for a running `onChunk`; `Close()` mid-stream; `Close()` from inside `onChunk`; second concurrent stream rejected; `convDelete` exactly once. Public-layer tests in `ai/stream_test.go` cover break, `ErrClosed` and mid-stream `ErrClosed`. The concurrency tests were run 300 times without failure. (`-race` is not supported on windows/arm64.)
- `go vet` introduces no new findings; the remaining `unsafeptr` warnings are in the pre-existing C-string helpers of `backend_native.go`.
- Real model through Rusticated WASM (`go run . pope -r -p ...`): a normal turn, plus a temporary harness, since removed, that did 3x break after 5 events, a ctx deadline, and `Close()` from another goroutine mid-stream. In every case the iterator returned promptly (deadline at 1.50s), later `SendStream` calls yielded `ErrClosed`, a new conversation on the same engine answered correctly straight afterwards, and the process exited cleanly.

Not verified:

- Assumption in section 2.5 (the destructor waiting for a callback still running) is not proven by reading; it is only supported by the real-model runs above. Native (non-WASM) cancellation is covered by the fake-driver tests only.

## 9. Differences from the first plan

- The validity flag (`closed`) and the active stream (`active`) live in `drvConversationBackend`, not in an atomic `Conversation.impl`; `convImpl`/`Conversation` are unchanged in shape.
- `Close()` and the loop-detector/`break` path never call native cancel directly. Only the `SendStream` goroutine that owns the stream cancels and tears down, so cancel and `convDelete` cannot race. `Close()` sets `closed` and signals the stream through `closeCh`; whichever of "closed" and "stream finished" happens last deletes the conversation, exactly once.
- `onChunk` delivery has a four-state atomic (`idle`, `active`, `stop requested`, `stopped`). Cancelling waits for an `onChunk` that is mid-flight, so the caller never carries on while a loop body is still running on the callback thread, and no chunk reaches the loop after cancellation.
- `break` is implemented as: the iterator cancels its derived context, and closes the conversation once `SendStream` has returned. It does not rely on which of "stream completed" and "ctx cancelled" the `select` happens to pick.
- `createTrampoline` no longer takes a callback id.
