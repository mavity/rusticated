# GOGUEST — Running Go Inside the Rusticated WASM Host

## Goal

Compile Go programs with `GOOS=wasip1` and run them inside the `rusticated`
WASM host without patching the Go installation. The host is a strictly
single-threaded, async-only, completion-based proactor; the standard
`wasi_snapshot_preview1` ABI is blocking and incompatible with it.

The mechanism is **`go build -overlay target/overlay.json`**: Go's build system
reads a JSON map of `GOROOT` source paths → local replacement paths and
substitutes files at compile time, never touching the installed toolchain.

---

## Key Architectural Insight: `runtime.pause` and `RETUNWIND`

Go's WASM backend uses a shadow-stack dispatch loop called `wasm_pc_f_loop`
(defined in `runtime/asm_wasm.s`). The loop runs until the internal `PAUSE`
global is set to 1, at which point it executes a `RETUNWIND` instruction and
returns control to whoever called the exported WASM entry point (`run`). This is documented in `runtime/stubs_wasm.go`:

```
// pause sets SP to newsp and pauses the execution of Go's WebAssembly
// code until an event is triggered, or call back into Go.
func pause(newsp uintptr)
```

`pause` is implemented in `runtime/asm_wasm.s` as:

```asm
TEXT runtime·pause(SB), NOSPLIT, $0-8
    MOVD newsp+0(FP), SP
    I32Const $1
    Set PAUSE
    RETUNWIND
```

When the Go scheduler has nothing runnable, it calls `beforeIdle`. On
`GOOS=js`, `beforeIdle` calls `pause`, unwinding WASM back to the JavaScript
host. On `GOOS=wasip1`, `beforeIdle` currently just returns `nil, false` —
meaning Go busy-loops with `poll_oneoff` instead of giving control back.

**The entire plan hinges on replacing `beforeIdle` in the overlay so it calls
`pause`, which causes `RETUNWIND` and yields control natively back to
`washmhost`.** The host then drives its async I/O loop, and when something
completes it calls the `run` export we add, which re-enters
`wasm_pc_f_loop` and wakes the suspended goroutine.

The `-16` offset passed to `pause` is required by the ABI: `pause`'s epilogue
pops 8 bytes from the stack, and another 8 accounts for the fake return PC, so
`pause(sys.GetCallerSP() - 16)` makes resumption appear to return from
`pause`'s caller's caller (i.e. transparent to the goroutine).

---

## Why Busy-Loops Are Fatal in Rusticated

The original blueprint contained this idiom:

```go
for {
    status := host_check_events(0)  // non-blocking poll
    if status == token { break }
    runtime_do_yield()              // alias for runtime.goschedimpl
}
```

This is a deadlock in the rusticated host because:

1. `runtime_do_yield()` / `Gosched()` only yield to **other Go goroutines**
   inside the WASM virtual machine. They never return control to Wasmtime.
2. `washmhost` is single-threaded. Its `tick()` and I/O completion loop are
   on the **same** thread as the WASM execution.
3. Because the Go guest never exits the `wasm_pc_f_loop`, the host's event
   loop never runs, I/O is never processed, and the `flags` field in
   `Overlapped` is never set — permanent deadlock.

The `pause` / `RETUNWIND` approach avoids this entirely: Go unwinding to the
host is a proper return from the WASM function call, freeing the call stack and
allowing `tick()` to run.

---

## How the Host Side Works

Current rusticated guest protocol (Rust guests):

```
host calls run()  →  guest polls futures  →  run() returns
                                  ↑
                   host sets Overlapped.flags
```

For Go guests the protocol becomes:

```
host calls run  →  Go boots → runs main → eventually hits pause()
                     ↗  run returns (RETUNWIND)
host runs tick()   ←
                     ↘  I/O completes, Overlapped.flags set
host calls run  →  wasm_pc_f_loop re-enters
                                 → goroutine wakes → continues
                                 → eventually hits pause() again
```

`washmhost` must:
1. Instantiate the module and call `run` once (Go init + main goroutine).
2. After `run` returns (due to `pause`), enter the usual `tick()` loop.
3. When the I/O completion signals, call the module export `run`.
4. Repeat until the module calls `process_exit`.

---

## Full Overlay Surface Area

A full-tree scan of `GOROOT/src` for `*wasip1*` (non-test) files reveals the
following files that use `wasi_snapshot_preview1` imports or contain
WASI-specific scheduling logic:

### Runtime (critical — must overlay)

| File | Contains |
|------|----------|
| `runtime/lock_wasip1.go` | `sched_yield`, `beforeIdle`, `notetsleepg` |
| `runtime/os_wasip1.go` | `proc_exit`, `fd_write`, `clock_time_get`, `poll_oneoff`, `random_get`, `args_get`, `environ_get` |
| `runtime/netpoll_wasip1.go` | Entire `poll_oneoff`-based network poller |

### Syscall (must overlay for file/net I/O)

| File | Contains |
|------|----------|
| `syscall/fs_wasip1.go` | All file descriptor I/O (`fd_read`, `fd_write`, `path_open`, etc.) |
| `syscall/syscall_wasip1.go` | `clock_time_get` |
| `syscall/net_wasip1.go` | Socket-level operations |
| `syscall/os_wasip1.go` | `fd_write` for stderr |

### Internal packages (must overlay for complete elimination of `wasi_snapshot_preview1`)

| File | Contains |
|------|----------|
| `internal/syscall/unix/at_wasip1.go` | `path_open` AT-style wrappers |
| `internal/syscall/unix/utimes_wasip1.go` | `path_filestat_set_times` |
| `internal/syscall/unix/net_wasip1.go` | Socket shims |
| `internal/syscall/unix/nonblocking_wasip1.go` | Non-blocking fd flags |
| `internal/poll/fd_wasip1.go` | File descriptor poll layer |

### Higher-level packages (overlay only if networking/file is needed)

`os/file_wasip1.go`, `os/stat_wasip1.go`, `net/fd_wasip1.go` — these compose
on top of `syscall` and may not need overlaying if the syscall layer is
correct.

**Practical approach**: start with the 3 runtime files (which control
scheduling) and the 4 syscall files (which control I/O). The internal and
higher-level packages will link correctly if the syscall layer is consistent.

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
│   └── resume.go                    # exports run via //go:wasmexport
├── prebuild/
│   └── src/main.rs                  # generates target/overlay.json
└── target/
    └── overlay.json                 # generated; gitignored
```

---

## Overlay File Contents

### `overlay-go/runtime/lock_rusticated.go`

This is the most critical file. It replaces `beforeIdle` to call `pause`
instead of returning `nil, false`, and replaces `notetsleepg` to yield to the
host instead of busy-looping with `sched_yield`.

```go
//go:build wasip1

package runtime

import (
    "internal/runtime/sys"
    "unsafe"
)

// pause is defined in asm_wasm.s. It sets PAUSE=1 and executes RETUNWIND,
// unwinding the WASM shadow stack and returning control to the host.
func pause(newsp uintptr)

const (
    mutex_unlocked = 0
    mutex_locked   = 1
    active_spin     = 4
    active_spin_cnt = 30
)

type mWaitList struct{}

func lockVerifyMSize() {}
func mutexContended(l *mutex) bool { return false }

func lock(l *mutex)  { lockWithRank(l, getLockRank(l)) }
func unlock(l *mutex) { unlockWithRank(l) }

func lock2(l *mutex) {
    if l.key == mutex_locked {
        throw("self deadlock")
    }
    gp := getg()
    if gp.m.locks < 0 {
        throw("lock count")
    }
    gp.m.locks++
    l.key = mutex_locked
}

func unlock2(l *mutex) {
    if l.key == mutex_unlocked {
        throw("unlock of unlocked lock")
    }
    gp := getg()
    gp.m.locks--
    if gp.m.locks < 0 {
        throw("lock count")
    }
    l.key = mutex_unlocked
}

func noteclear(n *note)  { n.key = 0 }
func notewakeup(n *note) {
    if n.key != 0 {
        throw("notewakeup - double wakeup")
    }
    n.key = 1
}
func notesleep(n *note)              { throw("notesleep not supported") }
func notetsleep(n *note, ns int64) bool { throw("notetsleep not supported"); return false }

func notetsleepg(n *note, ns int64) bool {
    gp := getg()
    if gp == gp.m.g0 {
        throw("notetsleepg on g0")
    }
    deadline := nanotime() + ns
    for {
        if n.key != 0 {
            return true
        }
        // Yield to the rusticated host: unwind the WASM stack entirely.
        // The host will call rusticated_resume when I/O completes.
        // The -16 offset: pausens 8, plus 8 for the fake return PC.
        pause(sys.GetCallerSP() - 16)
        if ns >= 0 && nanotime() >= deadline {
            return false
        }
    }
}

// beforeIdle is called by the Go scheduler when no goroutine is runnable.
// Instead of busy-looping with poll_oneoff (which would starve the host),
// we unwind the WASM stack and return to the rusticated event loop.
// The host will call run after completing pending I/O.
//
//go:yeswritebarrierrec
func beforeIdle(now, pollUntil int64) (gp *g, otherReady bool) {
    pause(sys.GetCallerSP() - 16)
    return nil, false
}

func checkTimeouts() {}

// Unused: type alias kept so overlay compiles without importing unsafe directly.
var _ unsafe.Pointer
```

### `overlay-go/runtime/os_rusticated.go`

Replaces all `wasi_snapshot_preview1` runtime imports with rusticated ABI.
Note that `fd_write` in `os_wasip1.go` is the runtime's stderr path (used
before the syscall package initialises); we redirect it to `env.write` but
need a pre-allocated `Overlapped` because the runtime cannot block here.

```go
//go:build wasip1

package runtime

import (
    "structs"
    "unsafe"
)

type uintptr32 = uint32
type size      = uint32
type errno     = uint32
type filesize  = uint64
type timestamp = uint64
type clockid   = uint32

const (
    clockRealtime  clockid = 0
    clockMonotonic clockid = 1
)

type iovec struct {
    _      structs.HostLayout
    buf    uintptr32
    bufLen size
}

// ── Rusticated host ABI ────────────────────────────────────────────────────

//go:wasmimport env process_exit
func exit(code int32)

//go:wasmimport env get_time
func rusticated_get_time() uint64

//go:wasmimport env get_random
//go:noescape
func random_get(buf *byte, bufLen size)

//go:wasmimport env get_args
func rusticated_get_args(stringsPtr *byte, stringsLen size) uint64

//go:wasmimport env get_env
func rusticated_get_env(stringsPtr *byte, stringsLen size) uint64

//go:wasmimport env write
func rusticated_write_sync(overlappedPtr uintptr, handle uint64, bufPtr *byte, bufLen size)

// ── Runtime-level write (stderr, panics) ──────────────────────────────────
//
// The runtime needs to write to fd 0/1/2 before the async scheduler is
// running (e.g. during panic). We use a fire-and-forget synchronous write:
// submit the overlapped and spin on the flags word. This is acceptable only
// for panic/fatal paths and startup writes, never for goroutine-level I/O.

//go:nosplit
func write1(fd uintptr, p unsafe.Pointer, n int32) int32 {
    // Inline Overlapped: flags(u32) error(u32) continued(u64) result_ext(u64)
    var ov [4]uint64
    rusticated_write_sync(
        uintptr(unsafe.Pointer(&ov[0])),
        uint64(fd),
        (*byte)(p),
        size(n),
    )
    // Spin until FLAG_COMPLETED (bit 0 of flags word, first u32).
    flagsPtr := (*uint32)(unsafe.Pointer(&ov[0]))
    for *flagsPtr&1 == 0 {
        // tight spin; only on panic/init paths
    }
    return n
}

// ── Time ──────────────────────────────────────────────────────────────────

func nanotime1() int64 {
    return int64(rusticated_get_time())
}

func walltime() (sec int64, nsec int32) { return walltime1() }
func walltime1() (sec int64, nsec int32) {
    t := rusticated_get_time()
    return int64(t / 1_000_000_000), int32(t % 1_000_000_000)
}

// ── Randomness ────────────────────────────────────────────────────────────

func readRandom(r []byte) int {
    if len(r) == 0 { return 0 }
    random_get(&r[0], size(len(r)))
    return len(r)
}

// ── Args / Env ────────────────────────────────────────────────────────────
//
// rusticated get_args / get_env return:
//   high 32 bits = count of items
//   low  32 bits = total bytes written
// Items are newline-delimited in the buffer (not null-delimited).

func goenvs() {
    // ---- argv ----
    // First call with nil to size the buffer.
    packed := rusticated_get_args(nil, 0)
    count := int(packed >> 32)
    bufLen := size(packed & 0xFFFFFFFF)

    argslice = make([]string, count)
    if count > 0 {
        buf := make([]byte, bufLen)
        rusticated_get_args(&buf[0], bufLen)
        splitNL(buf, argslice)
    }

    // ---- environ ----
    packed = rusticated_get_env(nil, 0)
    count = int(packed >> 32)
    bufLen = size(packed & 0xFFFFFFFF)

    envs = make([]string, count)
    if count > 0 {
        buf := make([]byte, bufLen)
        rusticated_get_env(&buf[0], bufLen)
        splitNL(buf, envs)
    }
}

func splitNL(buf []byte, dst []string) {
    idx := 0
    start := 0
    for i, b := range buf {
        if b == '\n' {
            if idx < len(dst) {
                dst[idx] = string(buf[start:i])
                idx++
            }
            start = i + 1
        }
    }
}

// usleep is used by the runtime for short sleeps; on rusticated we cannot
// block the host, so we no-op it. The scheduler's beforeIdle path handles
// real waiting.
func usleep(usec uint32) {}
```

### `overlay-go/runtime/netpoll_rusticated.go`

Replaces the `poll_oneoff`-based poller. Rusticated's async I/O handles fd
readiness through the `Overlapped` completion mechanism, not `poll_oneoff`.
The network poller is therefore a no-op stub: readiness events are driven by
the host completing read/write operations submitted by the syscall layer.

```go
//go:build wasip1

package runtime

func netpollinit()                            {}
func netpollIsPollDescriptor(fd uintptr) bool { return false }
func netpollopen(fd uintptr, pd *pollDesc) int32 { return 0 }
func netpollarm(pd *pollDesc, mode int)       {}
func netpolldisarm(pd *pollDesc, mode int32)  {}
func netpollclose(fd uintptr) int32           { return 0 }
func netpollBreak()                           {}

func netpoll(delay int64) (gList, int32) {
    if delay > 0 {
        // We cannot block here; beforeIdle/pause handles waiting.
        // Return immediately; the scheduler will call beforeIdle.
    }
    return gList{}, 0
}
```

### `overlay-go/syscall/fs_rusticated.go`

File system I/O using the rusticated overlapped ABI. Each blocking operation
submits an `Overlapped` to the host and then calls `pause` to yield back.
When the host completes the I/O it calls `run`, and execution
continues after the `pause` call.

```go
//go:build wasip1

package syscall

import (
    "internal/runtime/sys"
    "runtime"
    "structs"
    "unsafe"
)

// Overlapped mirrors abi::Overlapped in the rusticated sysroot.
// Layout: flags(u32) error(u32) continued(u64) result_ext(u64)  → 24 bytes, align 8.
type Overlapped struct {
    _ structs.HostLayout
    flags     uint32
    hostError uint32
    continued uint64
    resultExt uint64
}

func (o *Overlapped) isComplete() bool { return o.flags&1 != 0 }

// pause is defined in runtime/asm_wasm.s.
//go:linkname pause runtime.pause
func pause(newsp uintptr)

func awaitOverlapped(o *Overlapped) {
    for !o.isComplete() {
        pause(sys.GetCallerSP() - 16)
    }
}

// ── Rusticated host ABI ────────────────────────────────────────────────────

//go:wasmimport env path_open
func rusticated_path_open(ov uintptr, pathPtr *byte, pathLen uint32, flags uint32)

//go:wasmimport env read
func rusticated_read(ov uintptr, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env write
func rusticated_write(ov uintptr, handle uint64, bufPtr *byte, bufLen uint32)

//go:wasmimport env handle_close
func rusticated_handle_close(handle uint64)

//go:wasmimport env path_stat
func rusticated_path_stat(ov uintptr, pathPtr *byte, pathLen uint32, flags uint32, outPtr *byte, outLen uint32)

//go:wasmimport env get_random
func rusticated_random_get(buf *byte, bufLen uint32)

// ── Handle ↔ fd mapping ───────────────────────────────────────────────────
//
// WASI assigns small integer file descriptors; rusticated uses opaque u64
// handles. We keep a simple table: fd (int32) → handle (uint64).
// fds 0, 1, 2 are pre-assigned at init to stdin, stdout, stderr handles.

var (
    fdTable   [1024]uint64
    fdInUse   [1024]bool
)

func init() {
    // Handles 0, 1, 2 are stdin, stdout, stderr by rusticated convention.
    fdTable[0] = 0; fdInUse[0] = true
    fdTable[1] = 1; fdInUse[1] = true
    fdTable[2] = 2; fdInUse[2] = true
}

func allocFD(handle uint64) (int32, Errno) {
    for i := 3; i < len(fdTable); i++ {
        if !fdInUse[i] {
            fdTable[i] = handle
            fdInUse[i] = true
            return int32(i), 0
        }
    }
    return -1, EMFILE
}

func fdToHandle(fd int32) (uint64, Errno) {
    if fd < 0 || int(fd) >= len(fdTable) || !fdInUse[fd] {
        return 0, EBADF
    }
    return fdTable[fd], 0
}

// ── Open ──────────────────────────────────────────────────────────────────

func Open(path string, mode int, perm uint32) (int, error) {
    if len(path) == 0 { return -1, EINVAL }
    var ov Overlapped
    p := unsafe.SliceData([]byte(path))
    rusticated_path_open(uintptr(unsafe.Pointer(&ov)), p, uint32(len(path)), uint32(mode))
    runtime.KeepAlive(path)
    awaitOverlapped(&ov)
    if ov.hostError != 0 { return -1, errnoErr(Errno(ov.hostError)) }
    fd, errno := allocFD(ov.resultExt)
    if errno != 0 { rusticated_handle_close(ov.resultExt); return -1, errnoErr(errno) }
    return int(fd), nil
}

// ── Close ─────────────────────────────────────────────────────────────────

func Close(fd int) error {
    handle, err := fdToHandle(int32(fd))
    if err != 0 { return errnoErr(err) }
    rusticated_handle_close(handle)
    fdInUse[fd] = false
    return nil
}

// ── Read ──────────────────────────────────────────────────────────────────

func Read(fd int, p []byte) (int, error) {
    if len(p) == 0 { return 0, nil }
    handle, err := fdToHandle(int32(fd))
    if err != 0 { return 0, errnoErr(err) }
    var ov Overlapped
    rusticated_read(uintptr(unsafe.Pointer(&ov)), handle, &p[0], uint32(len(p)))
    runtime.KeepAlive(p)
    awaitOverlapped(&ov)
    if ov.hostError != 0 { return 0, errnoErr(Errno(ov.hostError)) }
    return int(ov.resultExt), nil
}

// ── Write ─────────────────────────────────────────────────────────────────

func Write(fd int, p []byte) (int, error) {
    if len(p) == 0 { return 0, nil }
    handle, err := fdToHandle(int32(fd))
    if err != 0 { return 0, errnoErr(err) }
    var ov Overlapped
    rusticated_write(uintptr(unsafe.Pointer(&ov)), handle, &p[0], uint32(len(p)))
    runtime.KeepAlive(p)
    awaitOverlapped(&ov)
    if ov.hostError != 0 { return 0, errnoErr(Errno(ov.hostError)) }
    return int(ov.resultExt), nil
}

// ── Random ────────────────────────────────────────────────────────────────

func RandomGet(b []byte) error {
    if len(b) == 0 { return nil }
    rusticated_random_get(&b[0], uint32(len(b)))
    return nil
}

// ── Seek / Pread / Pwrite (stubs — extend as needed) ──────────────────────

func Seek(fd int, offset int64, whence int) (int64, error)      { return 0, ENOSYS }
func Pread(fd int, b []byte, offset int64) (int, error)        { return 0, ENOSYS }
func Pwrite(fd int, b []byte, offset int64) (int, error)       { return 0, ENOSYS }
func Dup(fd int) (int, error)                                   { return 0, ENOSYS }
func Dup2(fd, newfd int) error                                  { return ENOSYS }
func Pipe(fd []int) error                                       { return ENOSYS }
func SetNonblock(fd int, nonblocking bool) error                { return nil }

// ── init replaces the original fs_wasip1.go init ──────────────────────────
// The original init called SetNonblock(0/1/2) to enable the WASI net poller.
// We don't need that; stdin/stdout/stderr are already mapped as handles.
func init() {}
```

### `overlay-go/syscall/syscall_rusticated.go`

```go
//go:build wasip1

package syscall

//go:wasmimport env get_time
func rusticated_clock() uint64

func clock_time_get(id uint32, precision uint64, time *uint64) Errno {
    *time = rusticated_clock()
    return 0
}
```

### `overlay-go/syscall/net_rusticated.go`

```go
//go:build wasip1

package syscall

// Socket operations are not supported in the MVP overlay.
// They can be implemented using env.net_open / env.net_accept / env.read / env.write.

func Socket(proto, sotype, unused int) (fd int, err error)     { return -1, ENOSYS }
func Bind(fd int, sa Sockaddr) error                            { return ENOSYS }
func Listen(fd int, backlog int) error                          { return ENOSYS }
func Accept(fd int) (int, Sockaddr, error)                      { return -1, nil, ENOSYS }
func Connect(fd int, sa Sockaddr) error                         { return ENOSYS }
func GetsockoptInt(fd, level, opt int) (int, error)            { return 0, ENOSYS }
func SetsockoptInt(fd, level, opt, value int) error             { return ENOSYS }
func Shutdown(fd int, how int) error                            { return ENOSYS }
func RecvFrom(fd int, p []byte, flags int, from *RawSockaddrAny, fromlen *_Socklen) (int, error) {
    return 0, ENOSYS
}
func SendTo(fd int, p []byte, flags int, to Sockaddr) error     { return ENOSYS }
```

### `overlay-go/syscall/os_rusticated.go`

```go
//go:build wasip1

package syscall

// This file replaces syscall/os_wasip1.go.
// fd_write for stderr is handled by the runtime overlay's write1.
// No wasi_snapshot_preview1 imports are needed here.
```

---

## Resume Export (`mohabbat-go/resume.go`)

Go 1.24 introduced `//go:wasmexport`, which exports a Go function as a named
WASM export. We use it to give the rusticated host a way to re-enter the Go
scheduler after completing I/O. (Go 1.25.1 is installed; this feature is
available.)

```go
//go:build wasip1

package main

// run is called by the rusticated host (washmhost) after one or
// more I/O completions have been written into guest Overlapped memory. It
// re-enters wasm_pc_f_loop and resumes any goroutines that were waiting.
//
//go:wasmexport run
func run() {
    // The Go runtime automatically re-enters wasm_pc_f_loop when this
    // wasmexport function returns. No explicit action is needed here;
    // the goroutines waiting in notetsleepg/beforeIdle will be woken by the
    // scheduler once their note.key or completion flag is set by the caller.
}
```

The host sets `Overlapped.flags |= 1` (FLAG_COMPLETED) before calling
`run`. The goroutine spinning in `awaitOverlapped` will
observe the flag on its next iteration after `pause` returns.

---

## Prebuild Tool — Generating `target/overlay.json`

Add to `prebuild/src/main.rs` (alongside the existing sysroot generation):

```rust
use std::collections::HashMap;
use std::fs;
use std::path::PathBuf;
use std::process::Command;

fn generate_go_overlay(target_dir: &PathBuf, repo_root: &PathBuf) -> std::io::Result<()> {
    // Resolve GOROOT dynamically so we never hardcode the installation path.
    let out = Command::new("go")
        .args(["env", "GOROOT"])
        .output()
        .expect("go env GOROOT failed; is Go in PATH?");
    let goroot = PathBuf::from(String::from_utf8_lossy(&out.stdout).trim().to_string());

    // Map of GOROOT source file → local overlay replacement.
    // Keys must be absolute paths; values are repo-relative overlay files.
    let overlay_dir = repo_root.join("overlay-go");
    let replacements: HashMap<PathBuf, PathBuf> = [
        // Runtime
        (goroot.join("src/runtime/lock_wasip1.go"),    overlay_dir.join("runtime/lock_rusticated.go")),
        (goroot.join("src/runtime/os_wasip1.go"),      overlay_dir.join("runtime/os_rusticated.go")),
        (goroot.join("src/runtime/netpoll_wasip1.go"), overlay_dir.join("runtime/netpoll_rusticated.go")),
        // Syscall
        (goroot.join("src/syscall/fs_wasip1.go"),      overlay_dir.join("syscall/fs_rusticated.go")),
        (goroot.join("src/syscall/syscall_wasip1.go"), overlay_dir.join("syscall/syscall_rusticated.go")),
        (goroot.join("src/syscall/net_wasip1.go"),     overlay_dir.join("syscall/net_rusticated.go")),
        (goroot.join("src/syscall/os_wasip1.go"),      overlay_dir.join("syscall/os_rusticated.go")),
    ].into_iter().collect();

    // Serialize to the overlay JSON format Go expects.
    // Paths must use forward slashes on all platforms per Go spec.
    let mut entries = String::new();
    let mut first = true;
    for (src, dst) in &replacements {
        if !first { entries.push_str(",\n"); }
        first = false;
        let src_str = src.to_string_lossy().replace('\\', "/");
        let dst_str = dst.canonicalize()
            .unwrap_or_else(|_| dst.clone())
            .to_string_lossy()
            .replace('\\', "/");
        entries.push_str(&format!("    \"{src_str}\": \"{dst_str}\""));
    }

    let json = format!("{{\n  \"Replace\": {{\n{entries}\n  }}\n}}\n");

    let overlay_path = target_dir.join("overlay.json");
    fs::write(&overlay_path, json)?;
    println!("wrote {}", overlay_path.display());
    Ok(())
}
```

---

## Host Changes (`washmhost`)

The `washmhost` crate must be extended to:

1. **Detect Go guests** — by checking whether the module uses
   `run` (completion re-entry) and the `env` imports.

2. **Drive the Go guest**:
   ```
   instance.call("run")      // Go boot + main; returns when pause() fires
   loop {
       tick()                   // process pending I/O completions
       if guest_exited { break }
       // Set Overlapped.flags for any completed ops
       instance.call("run")
   }
   ```

3. **Export `process_exit` as `!`** — when Go calls `env.process_exit(code)`,
   the host should stop the loop and propagate the exit code.

4. **Map file descriptors 0/1/2** — the host must provide handles 0, 1, 2 as
   stdin/stdout/stderr to the Go guest on initialisation.

---

## Build Commands

```bash
# Step 1: generate Rust sysroot + Go overlay.json
cargo run -p prebuild

# Step 2: compile Go → WASM with overlay
go build \
    -overlay target/overlay.json \
    -o target/mohabbat-go.wasm \
    -tags wasip1 \
    ./mohabbat-go/

# Step 3: run with washmhost (once washmhost supports Go guests)
cargo run -p washmhost -- target/mohabbat-go.wasm
```

Or as the one-liner that matches the README pattern:

```
cargo run -p prebuild && go build -overlay target/overlay.json -o target/mohabbat-go.wasm -tags wasip1 ./mohabbat-go/ && cargo run -p washmhost -- target/mohabbat-go.wasm
```

---

## Outstanding Work

### Must complete before first successful run

- [ ] **`overlay-go/runtime/os_rusticated.go`**: verify the `goenvs` split
  logic matches how rusticated actually encodes args/env (newline-delimited vs
  NUL-terminated; check `abi.rs` `get_args` contract).
- [ ] **`fs_rusticated.go` `init()`**: the original `init` in `fs_wasip1.go`
  called `SetNonblock(0,1,2)` to enable the WASI net poller. Our overlay
  no-ops `init`. Confirm the net poller replacement (`netpoll_rusticated.go`)
  does not need any initialisation.
- [ ] **`washmhost`**: add Go guest detection and the `run` + resume loop.
- [ ] **`washmhost`**: expose `process_exit` so Go exit works cleanly.
- [ ] **Handle table thread-safety**: the `fdTable` array in
  `fs_rusticated.go` is fine for single-goroutine use; if multiple goroutines
  call `Open` concurrently, a mutex is required.

### Internal package overlays (needed for `os` and `net` packages)

The following files use `wasi_snapshot_preview1` at the `internal/` level.
They must be overlaid if the application imports `os` or `net`:

- `internal/syscall/unix/at_wasip1.go`
- `internal/syscall/unix/utimes_wasip1.go`
- `internal/syscall/unix/net_wasip1.go`
- `internal/syscall/unix/nonblocking_wasip1.go`
- `internal/poll/fd_wasip1.go`

Each follows the same pattern: replace `wasi_snapshot_preview1` imports with
`env` imports, replace blocking I/O with overlapped + `pause`.

### Networking (future)

`net_rusticated.go` currently stubs everything with `ENOSYS`. Full TCP/UDP
requires mapping `env.net_open`, `env.net_accept`, `env.read`, `env.write` to
`net.Conn` semantics. The handle-based model maps cleanly; the main work is
correctly translating Go's `Sockaddr` types to rusticated's `addr_ptr/addr_len`
format.
