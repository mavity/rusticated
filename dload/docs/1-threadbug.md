# `dload` Background Callback Reader Lock Deadlock (`1-threadbug.md`)

## 1. The Bug

The `dload` native worker deadlocks when a background OS thread invokes a registered C callback while the main worker thread is running its normal request loop (`run()`).

In `dload/src/main.rs`, the main thread releases `READER_BUSY` immediately after `read_frame()` returns, before `dispatch_frame()` executes:

```rust
loop {
    while !try_acquire_reader_lock() {
        core::hint::spin_loop();
    }
    let frame_res = read_frame();
    release_reader_lock();
    match frame_res {
        Ok((hdr, body)) => dispatch_frame(hdr, &body),
        Err(()) => break,
    }
}
```

Meanwhile, in `dload/src/callbacks.rs`, a background thread executing `on_callback()` sends `OP_CB_INVOKE` and loops until its `InvocationSlot` transitions to `SLOT_HAS_CALL` or `SLOT_RETURNED`. On every iteration where the slot is still `SLOT_WAITING`, the background thread attempts `try_acquire_reader_lock()` and, if acquired, calls `read_frame()` on `stdin`:

```rust
if is_reader_thread || crate::try_acquire_reader_lock() {
    let is_temp_pump = !is_reader_thread;
    if let Ok((hdr, body)) = read_frame() {
        crate::dispatch_frame(hdr, &body);
    }
    ...
}
```

This temporary `stdin` pump was intended only for the case where the main reader thread is blocked inside a synchronous outer C call (such as `WaitForSingleObject`). Because `run()` releases `READER_BUSY` before `dispatch_frame()`, `READER_BUSY` is `false` while the main thread is mid-dispatch and between loop iterations.

When Go handles a background callback (such as LiteRT-LM's `sharedTokenCallback`), it issues multiple re-entrant requests (`OP_CALL_REQ` with `parent_inv_id = invID` and `OP_READ_MEM_REQ`) followed by `OP_CB_RETURN`. The deadlock occurs via the following sequence:

1. The main thread finishes `read_frame()` for `OP_CB_RETURN` (or a re-entrant request) and calls `release_reader_lock()`.
2. Before the main thread runs `dispatch_frame()` to set the slot state to `SLOT_RETURNED` (or `SLOT_HAS_CALL`), the background callback thread wakes from `Sleep(1)`.
3. The background callback thread observes that its slot state is still `SLOT_WAITING`, calls `try_acquire_reader_lock()`, succeeds, and calls `read_frame()` (`ReadFile(hStdin)`).
4. The main thread finishes `dispatch_frame()`, updates the slot state, and spins forever in `while !try_acquire_reader_lock()` waiting for the background thread to release `READER_BUSY`.
5. The background thread remains blocked forever inside `ReadFile(hStdin)` waiting for a frame that Go will never send, because Go already sent `OP_CB_RETURN` (or is waiting for the delegated call response).

Existing tests in `dload_test.go` missed this because `TestDloadBackgroundThreadCallbackFunnelsParentInvIDToSameOSThread` only fired a single callback and immediately blocked the main worker thread inside `WaitForSingleObject`. In asynchronous streaming APIs such as `litert_lm_conversation_send_message_stream`, the outer C call returns `0` immediately and the main worker thread remains active in `run()` while background threads fire repeated callbacks.

---

## 2. Deterministic Cross-Platform Reproduction Test (Windows, Linux, macOS)

We can reproduce this deadlock deterministically in `dload_test.go` across Windows, Linux, and macOS using only standard OS libraries (`kernel32.dll`, `libc.so.6` / `libpthread.so.0`, and `libSystem.B.dylib`) without LiteRT-LM:

1. Resolve the platform's fire-and-forget thread spawn primitive and thread-ID / pid query function:
   - **Windows (`kernel32.dll`):** `CreateThread(NULL, 0, trampoline, param, 0, NULL)` + `CloseHandle(hThread)` (note: Windows CFG/ntdll rejects `QueueUserWorkItem` targets located in dynamically allocated `VirtualAlloc` pages, whereas `CreateThread` invokes dynamic `PAGE_EXECUTE_READ` thunks directly) and `GetCurrentThreadId()`.
   - **Linux (`libc.so.6` / `libpthread.so.0`) & macOS (`libSystem.B.dylib`):** `pthread_create(&tidBuf, NULL, trampoline, param)` + `pthread_detach(tid)` (which spawns a background OS thread and returns immediately on the main `dload` thread) and `getpid()` / `pthread_self()`.
2. Allocate a shared memory buffer via `DylibAlloc` + `DylibWriteMem`.
3. Register a callback (`void* (*start_routine)(void*)` / `DWORD WINAPI (*LPTHREAD_START_ROUTINE)(LPVOID)`) via `DylibRegisterCallbackWithContext`.
4. Inside the Go callback handler, issue a re-entrant `DylibCall(symSelf, selfSig, manyArgs, invID)` (where `manyArgs` is a slice of 16,000,000 `uint64`s so `dispatch_frame`'s `for _ in 0..argc` loop in `dload/src/main.rs:217-223` takes ~20ms between `release_reader_lock()` and `try_delegate_call()`, exceeding one 15.6ms Windows `Sleep(1)` tick), followed by `DylibReadMem`.
5. From the test goroutine, spawn the async background callback with `parentInvID = 0` (returning immediately so the main `dload` thread stays active in `run()`) and wait on the Go channel with a 3-second timeout.
6. Before the fix, on **attempt 1** (`100%` reproducibility), the main worker thread releases `READER_BUSY` after `read_frame()`, and while it is unpacking `manyArgs` prior to `try_delegate_call()`, the background callback thread wakes from `Sleep(1)` / `usleep(500)`, sees `inv_slot.state == SLOT_WAITING` and `READER_BUSY == false`, steals `READER_BUSY`, and blocks forever inside `read_frame()` on the empty `stdin` pipe.

---

## 3. Fix Plan

1. **Hold `READER_BUSY` across the entire main loop in `dload/src/main.rs`:**
   - Acquire `READER_BUSY` once before entering the `run()` loop and keep it held across both `read_frame()` and `dispatch_frame()`.
   - Release `READER_BUSY` **only** around synchronous outer C function calls in `OP_CALL_REQ` (where `try_delegate_call` returned `false` and the current thread is the main reader thread), and re-acquire `READER_BUSY` immediately after `arch_call::call_c_func` returns before sending `OP_CALL_RESP`.
2. **Ensure background callback threads never steal `READER_BUSY` while the main thread is active:**
   - Because the main thread now holds `READER_BUSY` whenever it is reading or dispatching frames, `try_acquire_reader_lock()` in `on_callback()` can only succeed when the main thread is genuinely parked inside an outer `call_c_func` (such as `WaitForSingleObject`).
   - When a background callback thread does temporarily acquire `READER_BUSY` during a blocking outer C call, check `inv_slot.state` immediately after acquiring `READER_BUSY` (before calling `read_frame()`) so it never enters a blocking `read_frame()` if the main thread already satisfied its slot just before releasing the lock.
3. **Dynamic `RW -> RX` Trampoline Page Allocator (`dload/src/callbacks.rs`, `os_win.rs`, `os_unix.rs`):**
   - Replace the static 16-slot trampoline table with a dynamic page allocator (`SLOTS_PER_PAGE = 128`, 32-byte machine-code thunks for `x86_64`, `aarch64`, and `arm` backed by heap `Box<[AtomicU64; 128]>` `cb_id` arrays).
   - Each 4 KB (16 KB on macOS) page is populated once while `RW`, flushed from the instruction cache, and permanently locked down to `RX` (`PAGE_EXECUTE_READ` / `PROT_READ | PROT_EXEC`). Registering or unregistering (`OP_CB_UNREG_REQ`) only mutates the `RW` `AtomicU64` array and allocates additional `RX` pages on demand.
4. **Zero-NDK ELF `.so` Stubs for Linux & Android (`mohabbat/prebuild.go`):**
   - Generate minimal 32-bit and 64-bit ELF `ET_DYN` shared-object stubs (`libdl.so` and `libc.so` with Glibc `libdl.so.2`/`libc.so.6` and Bionic `libdl.so`/`libc.so` `DT_SONAME`s) and link the 5 Linux/Android `dload` flavours with `-Bdynamic -L<stubDir> -ldl -lc`.
5. **Verify:**
   - Confirm `TestDloadAsyncStreamingCallbacksWithoutBlockingMainThread` and `TestDloadDynamicTrampolineMultiPageScaling` (300 simultaneous callbacks across 3 pages) pass on Windows, Rusticated WASM (`go run . dload -t`), and WSL Linux (`aarch64`).
