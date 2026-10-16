# Rusticated ABI: Pseudo-Terminal (PTY) Specification

This document specifies the rusticated WASM ABI extensions required to support Pseudo-Terminals (PTYs). It covers the host-guest interface, overlapped structure layout, configuration grammar, handle lifecycle, and out-of-band terminal control.

---

## 1. Architectural Model

A Pseudo-Terminal represents an interactive bidirectional character device pair:
- **Master side (Host Handle):** Held by the host/guest supervisor. All user keystrokes are written here; all ANSI/VT output emitted by the child process is read from here.
- **Slave side (Child Process):** Attached to the spawned process as its controlling terminal (`/dev/tty` / console device), where `stdin`, `stdout`, and `stderr` are unified into one stream.

```
Guest WASM
    │
    │  process_spawn(ov, cfg, cols, rows)
    ▼
Washmhost Host Runtime
    ├── POSIX (Linux, macOS):
    │   • openpty() / forkpty() / creack/pty
    │   • TIOCSCTTY + setsid() in child pre-exec
    │   • TIOCSWINSZ for window resize
    │   • EIO on master read normalized to EOF
    └── Windows (Win10 1809+):
        • CreatePseudoConsole (ConPTY) + pipes
        • PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE
        • ResizePseudoConsole
        • ClosePseudoConsole
```

---

## 2. Configuration Grammar (`process_spawn`)

Process execution in the rusticated ABI uses a null-delimited byte stream (`cfgPtr`, `cfgLen`):

```
<program>\0[arg1\0...]\0\0[KEY=val\0...]\0\0[cwd]\0\0[stdio0\0stdio1\0stdio2\0...]
```

### Extending Section 3 (`stdio`) with PTY Specifiers

To spawn a process under a PTY, Section 3 allows specifying `"pty"`:

```
"pty"
"pty:cols,rows"
"pty:cols,rows,term_name"
```

#### Parsing Rules:
1. When `"pty"` is present in Section 3:
   - The host allocates a **single** pseudo-terminal session.
   - Child `stdin`, `stdout`, and `stderr` are all attached to the slave end of that PTY unless a specific descriptor is explicitly overridden (e.g. `stdio2 = "fd:<handle>"` for redirecting stderr).
   - If dimensions are omitted, defaults to `80,24`.
   - If `term_name` is provided (e.g., `"xterm-256color"`), the host exports or overrides `TERM=<term_name>` in the child process environment.

---

## 3. Overlapped Memory Layout

In rusticated ABI, the `Overlapped` structure prefix is standard across all async calls, while the trailing payload is call-kind specific and open-ended.

### `ProcessSpawnOverlapped`

```rust
#[repr(C)]
pub struct ProcessSpawnOverlapped {
    // ── Standard 24-byte Overlapped Header ─────────────────────────────
    pub is_completed: u32,       // Offset  0: 1 when completed, 0 when pending
    pub error_code:   u32,       // Offset  4: WASI errno (0 = success)
    pub continued:    u64,       // Offset  8: Reserved / pagination / offset
    pub result_ext:   u64,       // Offset 16: Primary result (child process handle)

    // ── Process Spawn Trailing Payload (Open-Ended) ────────────────────
    pub child_handle: u64,       // Offset 24: Process handle (for wait/kill)
    pub pty_handle:   u64,       // Offset 32: PTY master duplex handle (0 if non-PTY)
    pub in_handle:    u64,       // Offset 40: Stdin pipe handle (if piped non-PTY)
    pub out_handle:   u64,       // Offset 48: Stdout pipe handle (if piped non-PTY)
    pub err_handle:   u64,       // Offset 56: Stderr pipe handle (if piped non-PTY)
}
```

#### Field Semantics:
- **`child_handle` (Offset 24):** The process handle representing the running executable. Used with `process_wait` and `process_signal`.
- **`pty_handle` (Offset 32):** The bidirectional duplex stream representing the PTY Master.
  - Set to a valid non-zero host handle when spawned with PTY.
  - Set to `0` for regular non-PTY spawns.
- **`in_handle`, `out_handle`, `err_handle` (Offsets 40, 48, 56):** Dedicated handles for standard pipes when spawned without PTY. When `pty_handle != 0`, these are set to `0`.

---

## 4. Host Import Functions

### 1. `process_spawn` (Extended)

```wasm
(import "env" "process_spawn" (func (param i32 i32 i32)))
```
* **Parameters:**
  - `$ovPtr` (`i32`): Pointer to `ProcessSpawnOverlapped` in guest memory.
  - `$cfgPtr` (`i32`): Pointer to null-delimited configuration buffer.
  - `$cfgLen` (`i32`): Length of configuration buffer.
* **Completion:**
  - Upon completion, `is_completed` is set to `1`.
  - `child_handle` and `pty_handle` (plus `in_handle`/`out_handle`/`err_handle`) are written to their respective offsets in `$ovPtr`.

---

### 2. `tty_set_size` (Synchronous Terminal / PTY Window Resize)

Window resizing is a fast in-memory kernel/driver configuration call (`ioctl(TIOCSWINSZ)` on POSIX, `ResizePseudoConsole` on Windows). Following the pattern of `tty_get_size` and `tty_set_mode`, it is a **synchronous stack-based call** that does not require an `Overlapped` structure:

```wasm
(import "env" "tty_set_size" (func (param i64 i32 i32) (result i32)))
```
* **Parameters:**
  - `$handle` (`i64`): A PTY master handle (from `process_spawn`) or a standard terminal handle (`0`, `1`, `2`).
  - `$cols` (`i32`): Terminal width in columns (e.g., 80, 120).
  - `$rows` (`i32`): Terminal height in rows (e.g., 24, 40).
* **Return Value:**
  - `0` (`wasiSuccess`) on success.
  - WASI errno on failure (`wasiEBADF`, `wasiENOTTY` if handle is not a terminal/PTY, or `wasiEINVAL`).
* **Host Actions:**
  - **PTY Master Handle:**
    - **POSIX:** Calls `ioctl(fd, TIOCSWINSZ, &ws)` with `ws_col = cols, ws_row = rows`. The kernel automatically broadcasts `SIGWINCH` to the foreground process group.
    - **Windows:** Calls `ResizePseudoConsole(hpcon, COORD{X: cols, Y: rows})`.
  - **Standard Terminal Handle (`0`, `1`, `2`):**
    - Calls `ioctl(fd, TIOCSWINSZ, &ws)` on POSIX systems where permitted, or returns `wasiENOTTY` / `wasiEPERM` if the underlying stream is not an adjustable terminal.

---

## 5. Duplex I/O on `pty_handle`

Once obtained, the `pty_handle` is treated as a standard character stream handle using existing rusticated ABI imports:

### 1. Writing to PTY (`file_write`)
```wasm
(import "env" "file_write" (func (param i32 i64 i32 i32)))
```
- Passes input keystrokes, escape sequences, or control characters directly to the child.
- Writing byte `0x03` (`Ctrl+C`) triggers line discipline signal generation (`SIGINT`) on POSIX systems.

### 2. Reading from PTY (`file_read`)
```wasm
(import "env" "file_read" (func (param i32 i64 i32 i32)))
```
- Reads raw ANSI/VT output emitted by the child process.
- **Linux Normalization Requirement:** On Linux, when the child process closes its slave descriptor (e.g., on exit), the kernel returns `EIO` on subsequent master `read()` calls. The host **MUST** intercept `syscall.EIO` on a PTY master descriptor and treat it as a clean EOF (`error_code = 0`, `bytes_transferred = 0`).

### 3. Closing PTY (`handle_close`)
```wasm
(import "env" "handle_close" (func (param i64)))
```
- Closes the PTY master descriptor / shuts down ConPTY. Delivers EOF (hangup / `SIGHUP`) to the child process.

---

## 6. Lifecycle & Teardown Invariants

1. **Deadlock Prevention on Exit:**
   The host must not block indefinitely on child exit while unread output remains in the PTY master buffer. The PTY stream must remain readable until drained or closed by the guest.
2. **Handle Independence:**
   `child_handle` and `pty_handle` are independent handles:
   - Closing `pty_handle` sends EOF/hangup to the child, but does not invalidate `child_handle`.
   - `child_handle` remains valid until closed via `handle_close` or after `process_wait` completes.

---

## 7. The Unified `pty` Go Package Architecture

To prevent consumer code (such as `kabibi` or external tools) from branching across targets, a single unified package (e.g. `pope/pty` or `rustic/pty`) provides an identical high-level interface across **Native POSIX**, **Native Windows**, and **Rusticated WASM**:

```go
package pty

import (
    "io"
    "os/exec"
)

// PTY represents the master end of a pseudo-terminal session.
type PTY interface {
    io.ReadWriteCloser
    Resize(cols, rows int) error
    Fd() uintptr // Native descriptor on POSIX/Windows, or guest handle on rusticated
}

type Options struct {
    Cols int
    Rows int
    Term string // e.g. "xterm-256color"
}

// Start executes cmd attached to a newly minted PTY.
func Start(cmd *exec.Cmd, opts ...Options) (PTY, error)
```

### Build-Tag Target Matrix

```
                               ┌──────────────────────────────────────────────┐
                               │               package pty                    │
                               │   PTY interface: Read, Write, Close, Resize  │
                               └──────────────────────┬───────────────────────┘
                                                      │
              ┌───────────────────────────────────────┼──────────────────────────────────────┐
              │ //go:build !wasip1 && !windows        │ //go:build windows && !wasip1        │ //go:build wasip1
              ▼                                       ▼                                      ▼
       [pty_unix.go]                           [pty_windows.go]                       [pty_rusticated.go]
   • Direct POSIX openpty()                • ConPTY via x/sys/windows             • Calls rusticated ABI:
   • TIOCSCTTY + setsid in child           • STARTUPINFOEX pseudoconsole            - process_spawn("pty")
   • ioctl(TIOCSWINSZ) for Resize          • ResizePseudoConsole for Resize         - file_read / file_write
   • Normalizes Linux EIO -> EOF           • Duplex inPipe/outPipe adapter          - tty_set_size
```

1. **Native POSIX (`pty_unix.go`):**
   - Calls `openpty` via standard syscalls, sets initial window size via `TIOCSWINSZ`.
   - Sets `cmd.SysProcAttr.Setsid = true`, `Setctty = true`, `Ctty = slaveFd`.
   - Returns an `*os.File` wrapper that masks Linux `syscall.EIO` into `io.EOF`.

2. **Native Windows (`pty_windows.go`):**
   - Uses official `golang.org/x/sys/windows` bindings (no CGO, no Charm churn).
   - Creates input/output anonymous pipe pairs and calls `windows.CreatePseudoConsole`.
   - Attaches `HPCON` to `cmd.SysProcAttr` via `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE`.
   - Exposes a duplex adapter combining `outPipe` (Reader) and `inPipe` (Writer).
   - Implements `Resize()` via `windows.ResizePseudoConsole`.

3. **Rusticated WASM (`pty_rusticated.go`):**
   - Extracts `cmd.Path`, `cmd.Args`, `cmd.Env`, `cmd.Dir` from `cmd`.
   - Formats Section 3 stdio with `"pty:cols,rows,term"`.
   - Invokes `process_spawn` passing `ProcessSpawnOverlapped`.
   - Returns a `rusticatedPty` handle wrapping:
     - `Read(p)` $\rightarrow$ async `file_read` ABI.
     - `Write(p)` $\rightarrow$ async `file_write` ABI.
     - `Resize(cols, rows)` $\rightarrow$ synchronous `tty_set_size` ABI.
     - `Close()` $\rightarrow$ `handle_close` ABI.

---

## 8. Host-Side Implementation Architecture

Inside `washmhost` and the other runtimes, the host services the ABI requests:

### 1. `washmhost` (Native Go)
- Internally reuses the native parts of `pty_unix.go` and `pty_windows.go` to spawn child processes attached to real OS PTYs.
- Stores the active PTY session in `h.handles[ph]`.
- Routes `file_read` and `file_write` to that session.
- Routes `tty_set_size` to `session.Resize(cols, rows)`.

### 2. `node-host` (JavaScript / Node.js)
- When falling back to Node.js, `process_spawn` checks if stdio requests `"pty"`.
- Uses `node-pty` (or platform-specific FFI) to spawn the child process, mapping the returned duplex stream and PID into the host handle table.

### 3. `rusticated-rust` (Rust Sysroot)
- `Command` builder receives `.pty(true)` or `Stdio::Pty(cols, rows)`.
- Interacts with `ProcessSpawnOverlapped` and returns a `PtyChild` exposing async `AsyncRead` + `AsyncWrite` and a synchronous `resize(cols, rows)` method calling `tty_set_size`.

---

## 9. Consumer Ergonomics (e.g. `kabibi`)

With this layered architecture, consumer applications remain completely agnostic of whether they are compiled natively on Windows, natively on macOS/Linux, or cross-compiled into Mohabbat WASM:

```go
cmd := exec.Command("htop")
ptyMaster, err := pty.Start(cmd, pty.Options{Cols: width, Rows: height})
if err != nil {
    return err
}
defer ptyMaster.Close()

// Stream duplex ANSI I/O
go io.Copy(ptyMaster, userKeyStream)
io.Copy(terminalEmulator, ptyMaster)
```
No `//go:build` flags or platform switches needed anywhere in the UI or shell layer!

---

## 10. Low-Level Implementation Roadmap & Syscall Strategies

To preserve Mohabbat's lightweight footprint, zero external PTY libraries are used. The implementation directly leverages existing codebase idioms (`syscall`, `kernel32.dll`, `purego`).

### A. Windows ConPTY Direct Syscall Strategy (`pty_windows.go`)

Windows 10 1809+ provides ConPTY natively in `kernel32.dll`. We dynamically load and invoke these functions following the pattern of `dl_windows.go`:

```go
var (
    kernel32 = syscall.NewLazyDLL("kernel32.dll")
    
    procCreatePseudoConsole       = kernel32.NewProc("CreatePseudoConsole")
    procResizePseudoConsole       = kernel32.NewProc("ResizePseudoConsole")
    procClosePseudoConsole        = kernel32.NewProc("ClosePseudoConsole")
    procInitializeProcThreadAttr  = kernel32.NewProc("InitializeProcThreadAttributeList")
    procUpdateProcThreadAttr      = kernel32.NewProc("UpdateProcThreadAttribute")
    procDeleteProcThreadAttr      = kernel32.NewProc("DeleteProcThreadAttributeList")
)

const PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE = 0x00020016
```

#### Windows ConPTY Lifecycle & Teardown Sequencing:
1. **Pipes:** Create input anonymous pipe pair (`hInRead`, `hInWrite`) and output anonymous pipe pair (`hOutRead`, `hOutWrite`).
2. **Pseudo-Console:** `CreatePseudoConsole(COORD{X: cols, Y: rows}, hInRead, hOutWrite, 0, &hpcon)`.
3. **Attribute List:** Allocate heap memory for `PROC_THREAD_ATTRIBUTE_LIST`, call `InitializeProcThreadAttributeList`, then `UpdateProcThreadAttribute` passing `hpcon`. Assign to `cmd.SysProcAttr`.
4. **Close Intermediate Handles:** Immediately close `hInRead` and `hOutWrite` in the parent process so that the child holds the sole references.
5. **Teardown Order (Deadlock-Free):**
   - Wait for child process exit.
   - Close `hpcon` via `ClosePseudoConsole(hpcon)`.
   - Close `hInWrite`.
   - Read from `hOutRead` until EOF, then close `hOutRead`.

### B. POSIX Direct Syscall Strategy (`pty_unix.go`)

Direct invocation of `openpty` via standard libc or direct `/dev/ptmx` opening:
1. Call `openpty(&masterFd, &slaveFd, nil, &termios, &winsize)`.
2. Configure child process:
   ```go
   cmd.Stdin = slave
   cmd.Stdout = slave
   cmd.Stderr = slave
   cmd.SysProcAttr = &syscall.SysProcAttr{
       Setsid:  true,
       Setctty: true,
       Ctty:    int(slave.Fd()),
   }
   ```
3. Close `slave` in parent process immediately after `cmd.Start()`.
4. Wrap master file descriptor:
   - On Linux, intercept `syscall.EIO` from `master.Read()` and return `io.EOF`.
   - On resize, execute `syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))`.

### C. Host Integration in `washmhost` (`env_proc.go` & `env_os.go`)

1. **`h.handles` Polymorphism:**
   Introduce a unified internal `StreamDevice` / `PtyDevice` interface in `washmhost`:
   ```go
   type PtySession interface {
       io.ReadWriteCloser
       Resize(cols, rows int) error
   }
   ```
   Stored directly in `h.handles[ph]`.
2. **`sys_file_read` & `sys_file_write`:**
   Inspect handle: if it implements `io.Reader` / `io.Writer`, dispatch directly.
3. **`sys_tty_set_size`:**
   Inspect handle: if it implements `Resize(cols, rows)`, call it; if `*os.File`, run `TIOCSWINSZ`.

