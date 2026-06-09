# GOGUEST — Running Go Inside the Rusticated WASM Host

## Goal

Compile Go programs with `GOOS=wasip1` and run them inside the `rusticated` WASM host without patching the Go installation.

**Mandatory Constraint: Total WASI Elimination.**
The rusticated host is a strictly single-threaded, async-only, completion-based proactor. It does **not** implement the `wasi_snapshot_preview1` ABI. Any Go binary containing imports from the `wasi_snapshot_preview1` module will fail to load or crash at runtime. The standard WASI ABI is blocking and fundamentally incompatible with the rusticated concurrency model.

The mechanism for achieving this is **`go build -overlay target/overlay.json`**: Go's build system reads a JSON map of `GOROOT` source paths → local replacement paths and substitutes files at compile time, never touching the installed toolchain. The overlay MUST replace every single file in the runtime and syscall layers that imports from WASI.

---

## Core Architectural Design: Cooperative Parking & Re-entry State Machine

The guest Go binary functions as an event-driven state machine driven by the host. Execution context yields back to the host whenever an asynchronous operation blocks, returning naturally from the exported WASM entry function without modifying or breaking the standard WASM execution frame.

### 1. Dual-Mode Entry Gate (`run`)

The host exposes and invokes a single, parameterless `run` export for both initial execution and subsequent completion wake-ups. This entry point is managed by an internal global guard flag (`var initialized uint32`).

* **First Entry (Cold Boot):** When `initialized == 0`, the flag is set to `1` and execution is routed straight to the Go runtime's internal bootstrap routines (`rt0_go`). This hydrates the linear memory heap, initializes the garbage collector, runs package `init()` functions, and spawns the main goroutine. When the execution path hits its first asynchronous stall point, the scheduler empties, and the function returns naturally to the host.
* **Subsequent Entries (Continuation):** When `initialized == 1`, the entire bootstrap block is completely bypassed. Execution jumps directly to the `poll_and_resume` dispatcher loop to process completions before ticking the scheduler.

### 2. The Overlapped Registry Layout

To map host-side completion events back to specific parked goroutines, the guest maintains a fixed-size, flat registry array in linear memory composed of `OverlappedContext` blocks. This avoids dynamic Go maps and allows the host to touch completion status bits safely:

```go
type OverlappedContext struct {
    gp          uintptr    // Opaque pointer to the parked goroutine structure (*g)
    overlapped  Overlapped // Flat 24-byte host status structure matching the host ABI
}

```

---

## Concurrency and Yielding Mechanics

### 1. The Syscall Yield Path (Parking)

When a goroutine executes an I/O operation (e.g., file or socket read), the overlaid syscall wrapper prepares the request:

1. It claims an available slot in the flat `OverlappedContext` array.
2. It writes the current goroutine pointer (`getg()`) to the `gp` field.
3. It invokes the non-blocking host ABI import function (e.g., `rusticated_read`), passing the pointer to the `Overlapped` structure.
4. The host ABI function initiates the background worker tracking and immediately returns a status code.
5. If the status is pending, the wrapper calls the internal runtime primitive `runtime.gopark()`. This moves the calling goroutine into a waiting state, leaving its call stack intact inside Go memory.
6. **The Idle Yield Transition:**
    * The Go scheduler, running on the `g0` stack, enters `runtime.findRunnable` ([src/runtime/proc.go:3389](C:\Users\mihai\sdk\go1.26.4\src\runtime\proc.go#L3389)). 
    * Finding no runnable goroutines, it executes the target-specific `runtime.beforeIdle` hook ([src/runtime/proc.go:3565](C:\Users\mihai\sdk\go1.26.4\src\runtime\proc.go#L3565)).
    * Our overlay implementation in [overlay-go/runtime/lock_rusticated.go](overlay-go/runtime/lock_rusticated.go) invokes `runtime.pause()`.
    * The assembly routine `runtime.pause` ([overlay-go/runtime/asm_rusticated.s:628](overlay-go/runtime/asm_rusticated.s#L628)) sets a global `PAUSE` flag to `1` and executes the `RETUNWIND` instruction.
    * This rips the WebAssembly stack back to the central `wasm_pc_f_loop` trampoline ([overlay-go/runtime/asm_rusticated.s:515](overlay-go/runtime/asm_rusticated.s#L515)), which detects the `PAUSE` signal, breaks its loop, and returns naturally to the host thread.

### 2. The Host Resume Path (Waking)

When the host's async completion loop processes an I/O event, it operates purely via shared memory and re-entry:

1. The host directly modifies the `flags` field inside the target `Overlapped` structure in linear memory, flipping the completion bit to `1`.
2. The host invokes the parameterless module export `run`.
3. Upon re-entry, the `poll_and_resume` dispatcher loops over the fixed `OverlappedContext` array, evaluating the host status fields.
4. For every completed operation found, the dispatcher extracts the cached `gp` pointer and runs `runtime.goready(gp)`. This transitions the goroutine from the wait pool back onto the active run queue.
5. The dispatcher completes its scan and invokes the standard Go scheduler loop. The ready goroutines execute, process their data buffers, and return out to the host loop when the run queues clear again.

---

## Full Overlay Surface Area

A full-tree scan of `GOROOT/src` for `*wasip1*` (non-test) files defines the complete surface area of file replacements mapping to your local repository directory:

### Runtime Packages (Scheduling & OS Core)

| GOROOT Target Path | Local Replacement File Path | Description |
| --- | --- | --- |
| `runtime/lock_wasip1.go` | `overlay-go/runtime/lock_rusticated.go` | Implements `beforeIdle` to return to host and provides low-level lock structures. |
| `runtime/os_wasip1.go` | `overlay-go/runtime/os_rusticated.go` | Replaces standard WASI system calls with custom environment time, random, and argument imports. |
| `runtime/netpoll_wasip1.go` | `overlay-go/runtime/netpoll_rusticated.go` | Disables `poll_oneoff` tracking, stubbing out readiness loops since the host handles events. |

### Syscall Packages (I/O Translation)

| GOROOT Target Path | Local Replacement File Path | Description |
| --- | --- | --- |
| `src/syscall/fs_wasip1.go` | `overlay-go/syscall/fs_rusticated.go` | Transforms `Open`, `Read`, and `Write` to map small fds to u64 handles and trigger `gopark`. |
| `src/syscall/syscall_wasip1.go` | `overlay-go/syscall/syscall_rusticated.go` | Overlays low-level clock and system timing utilities. |
| `src/syscall/net_wasip1.go` | `overlay-go/syscall/net_rusticated.go` | Maps network socket layers to completion-based primitives. |
| `src/syscall/os_wasip1.go` | `overlay-go/syscall/os_rusticated.go` | Redirects early runtime stderr and logging streams. |

### Internal & Higher-Level Packages

The following packages utilize `wasi_snapshot_preview1` at the internal level and are systematically overlaid to ensure complete linkage isolation when compiling standard `os` or `net` libraries:

* `internal/syscall/unix/at_wasip1.go`
* `internal/syscall/unix/utimes_wasip1.go`
* `internal/syscall/unix/net_wasip1.go`
* `internal/syscall/unix/nonblocking_wasip1.go`
* `internal/poll/fd_wasip1.go`

---

## Repository Layout

```
rustic/
├── overlay-go/
│   ├── runtime/
│   │   ├── lock_rusticated.go       # replaces runtime/lock_wasip1.go
│   │   ├── os_rusticated.go         # replaces runtime/os_wasip1.go
│   │   └── netpoll_rusticated.go    # replaces runtime/netpoll_wasip1.go
│   └── syscall/
│       ├── fs_rusticated.go         # replaces syscall/fs_wasip1.go
│       ├── syscall_rusticated.go    # replaces syscall/syscall_wasip1.go
│       ├── net_rusticated.go        # replaces syscall/net_wasip1.go
│       └── os_rusticated.go         # replaces syscall/os_wasip1.go
│   ├── main.go
│   └── resume.go                    # houses the main dual-mode run() export
├── demo-go/
│   ├── main.go                      # Go implementation of `demo`
│   └── go.mod
├── prebuild/
│   └── src/main.rs                  # generates target/overlay.json
└── target/
    └── overlay.json                 # generated compiler manifest

```

---

## Host Execution Protocol (`washmhost`)

The host binary orchestrates the execution lifecycle using a basic non-blocking loop interface:

1. **Instantiation:** The host pre-allocates opaque virtual handles `0`, `1`, `2` to stdin, stdout, and stderr within its tracking layer.
2. **Initial Call:** The host invokes `instance.call("run")`. The guest cold-boots, setups internals, runs initialization files, spawns `main`, hits an outstanding async execution boundary, locks itself using `gopark`, and returns control to the host.
3. **The Polling Loop:**
```
loop {
    tick()                   // process host I/O & guest-initiated timer expirations
    if guest_exited { break }
    if completions_ready || timer_expired {
        instance.call("run") // re-enters continuation block
    }
}
```

### 3. Timer Coordination (Guest to Host)

To ensure Go `time.Sleep` and timer-based goroutines wake up correctly, the guest communicates its next required execution deadline to the host:

1. **Deadline Discovery**: During the `beforeIdle` hook, the scheduler provides a `pollUntil` timestamp ([src/runtime/proc.go:3389](C:\Users\mihai\sdk\go1.26.4\src\runtime\proc.go#L3389)).
2. **Host Notification**: Our `lock_rusticated.go` implementation invokes a custom `wasmimport` (e.g., `env.sched_pause(ns)`) passing the nanosecond deadline before calling `runtime.pause()`.
3. **Host Blocking**: The `washmhost` proactor registers this deadline in its internal timer heap. The host's `Poll()` function will then include this deadline in its own blocking wait logic.
4. **Re-entry**: When the deadline is reached, the host considers a "timer completion" to be ready and invokes `run` again, allowing Go to process its internal timer heap.



4. **Process Teardown:** When the guest hits `env.process_exit(code)`, the host immediately tears down the instance and bubbles the error code to the parent process.

---

## Build Pipeline

Compilation matches the multi-stage pipeline configuration using the unified generation tools:

```bash
# Step 1: Generate the local overlay JSON configuration mapping file
cargo run -p prebuild

# Step 2: Cross-compile Go source code using the overlay configurations
go build \
    -overlay target/overlay.json \
    -o target/mohabbat-go.wasm \
    -tags wasip1 \
    ./mohabbat-go/

# Step 3: Launch module inside the proactor host
cargo run -p washmhost -- target/mohabbat-go.wasm

```

---

## Validation and The Ultimate Success Metric

The transition to a Go-enabled rusticated stack is binary: there is no partial success. The project is considered successful **ONLY** if the following validation script runs to completion and produces the full expected output (diagnostics, successfully handled input/timeout, and file verification):

```bash
cargo run -p prebuild && go -C mohabbat-go run . && mohab.bat demo-go -o demo-go.bat && echo . | demo-go.bat

```

**If this script fails to run at any step, or if `demo-go.bat` deadlocks, crashes, or fails to produce the verified output, the implementation has fully failed.** This script is the single and final source of truth for the Go Guest integration.
