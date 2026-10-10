# Shell widget and shell session

This document explains how the shell widget and the shell session fit together,
how we wrap mvdan's `interp.Runner` for shell scripting and builtins, and how we
selectively allocate PTYs for external subprocesses to provide native terminal
behavior. The session collects raw ANSI byte streams; the widget renders them
on-demand through the terminal emulator at current window dimensions.

## The split, in one sentence

- The **session** owns the running shell (`interp.Runner`), the working
  directory, the environment, and the stdin/stdout wiring. It runs commands one
  at a time and reports output via messages.
- The **widget** owns what you see: it holds the list of `CommandBlock`s, draws
  them, takes keystrokes, and feeds them to the session.

Neither side reaches into the other. The widget never imports `interp`; the
session never draws anything. That line is what lets us test each half on its
own.

## The session interface

The widget controls the session through a small interface:

```go
type Session interface {
    Run(line string) *Command           // start the next command, get its handle
    WriteStdin(p []byte) (int, error)   // feed stdin to the running command
    CtrlDCloseActiveCommandStdin() error                  // close stdin pipe (Ctrl-D / EOF)
    CtrlCInterruptActiveCommand()                         // cancel the running command (Ctrl-C)
    Dir() string                        // current working directory from runner
    Resize(width, height int)           // propagate terminal size to active PTYs and emulator
    Close() error                       // shut the session down
}
```

A `Command` is the handle for one running command:

```go
type Command struct {
    // internal unexported state
}

// Done returns a channel that is closed when foreground execution finishes
// and control returns to the prompt.
func (c *Command) Done() <-chan struct{}

// Result returns nil while the foreground command is running,
// and the retained Result once Done is closed.
func (c *Command) Result() *Result

type Result struct {
    ExitCode int
    Err      error
}
```

## How input flows in

Instead of one persistent input stream that lives for the entire session, the
session creates a **fresh input channel for each command**:

- **Why per-command input isolation**: If an input stream remains open across
  commands, programs waiting for input to end (like `cat`, `read`, or `sort`)
  will never see EOF and will hang forever. A dedicated input channel allows
  closing the write end to deliver a genuine EOF without breaking future commands.
- **For external subprocesses with PTY**: Input is routed to the PTY master's
  write end. Ctrl-D closes the PTY input, delivering EOF to the child process.
  The PTY handles translation of control sequences (raw bytes → signals).
- **For shell builtins and pipes**: Input flows through mvdan's standard I/O
  wiring using regular pipes.
- **When user presses Ctrl-D**: The widget invokes
  `session.CtrlDCloseActiveCommandStdin()`, which closes the write end of the
  active input channel (PTY master or pipe). The running program sees EOF and exits.
- **When user presses Ctrl-C**: The widget invokes
  `session.CtrlCInterruptActiveCommand()`. For PTY-backed processes, this writes
  the interrupt character to the PTY; for others, it cancels the context.
- **Input routing**:
  - When no command is running, typing edits the local command-line buffer
    (`inputValue`).
  - When a command is running, typing forwards bytes directly to
    `session.WriteStdin`.

## How output flows out and handling background jobs (`&`)

Each command gets a **per-command byte buffer** that collects raw output from both
shell builtins and external subprocesses. External commands run on PTYs when their
stdout is not redirected or piped.

- **The backgrounding problem**: When a user runs a background command like
  `sleep 5 &`, mvdan forks off a subshell (`r2 := r.subshell(true)`) in a separate
  goroutine. The main `runner.Run` returns immediately. If output was tied to an
  ephemeral channel that closed when `Run` returned, the background job would
  panic writing to a closed channel. If it wrote to a shared global stream, its
  output would corrupt the next command's output.
- **The per-command buffer solution**:
  1. Before starting command $N$, the session creates a byte buffer bound to
     Command $N$.
  2. The session assigns the runner's stdout and stderr to write into this buffer.
  3. When mvdan executes a **shell builtin** (like `echo`, `cd`), the text flows
     directly into the buffer as plain bytes.
  4. When mvdan's `ExecHandler` executes an **external subprocess** and detects
     that stdout is the session writer (not a pipe or file redirect), it allocates
     a PTY, spawns the process on the PTY, and feeds the PTY master's output
     (raw ANSI sequences) into the same buffer.
  5. As bytes arrive, the session sends incremental update messages to the widget
     containing raw byte chunks.
  6. When mvdan forks `r2` for background execution, `r2` inherits the buffer
     writer (for builtins) or the PTY handle (for external commands).
  7. When the foreground command finishes, control returns to the prompt, but the
     buffer and any PTY handles remain alive.
  8. When command $N+1$ runs, the session creates a fresh buffer and PTY pool
     for Command $N+1$.
  9. The background job from command $N$ still holds its original buffer/PTY.
     When it emits output later, that output is appended to Command $N$'s buffer,
     triggers a background burst message to the widget, never leaks into Command
     $N+1$, and cannot crash.

## History data model and timing

Each command block captures precise lifecycle timestamps and stores background
output in debounced bursts rather than a flattened wall of text:

```go
type CommandBlock struct {
    Prompt   string
    Input    string
    Output   bytes.Buffer       // Raw ANSI byte stream (source of truth)
    Status   CommandBlockStatus
    ExitCode int

    // Timing
    Started  time.Time          // When Enter was pressed
    Returned *time.Time         // When control returned to the prompt

    // Background execution (if the command spawned background jobs)
    Background *BackgroundExecution
}

type BackgroundExecution struct {
    Exited *time.Time           // When background processes finished
    Chunks []BackgroundChunk    // Debounced output bursts
}

type BackgroundChunk struct {
    Posted time.Time            // Time first byte of the burst arrived
    Data   bytes.Buffer         // Raw ANSI bytes for this burst
}
```

### Timing semantics
- **`Started`**: Recorded immediately when the user submits the command.
- **`Returned`**: Recorded when the foreground execution ends and control
  returns to the shell prompt (either on process exit or when detached via `&`).
- **Background bursts**: Any output emitted after `Returned` is captured into
  `Background.Chunks`. A silence interval (e.g. 3 seconds) debounces bursts:
  the first incoming byte records `Posted`, subsequent bytes within 3 seconds
  are appended to the same chunk's `Data` buffer, and a pause exceeding 3 seconds
  seals the chunk.
- **Output representation**: `CommandBlock.Output` holds the raw ANSI byte stream
  (the canonical source of truth). The widget renders this on-demand by passing
  bytes through the terminal emulator at current window dimensions. This means
  blocks automatically adapt to window resizes without pre-computing renderings.
- **UI decoupling**: The data model only records timestamps and raw bytes. The
  visual presentation (inline, separate drawer, or collapsable folds) is decided
  independently by the UI layer.

## Concurrency and execution model (Who touches what)

State ownership follows a strict single-goroutine coordinator model using
message passing, matching the existing `Host.processLoop`:

- **Coordinator Goroutine (`Host.processLoop`)**:
  - Exclusively owns all UI and session state: the slice of `CommandBlock`s,
    the active prompt, line editing buffers, and screen cell buffers.
  - Exclusively runs `HandleEvent` and `Render`.
  - No mutexes are needed around widget data structures because no other
    goroutine touches them directly.
- **Session Worker Goroutine**:
  - A dedicated background goroutine owns mvdan's `interp.Runner`.
  - It pulls commands from an internal FIFO queue and executes them one at a
    time, ensuring `interp.Runner` is never accessed concurrently.
  - For each command, the worker creates a byte buffer and configures the runner
    to write to it.
- **Per-subprocess PTY Reader Goroutines**:
  - When an external command is spawned on a PTY, a dedicated goroutine reads
    from the PTY master and feeds raw bytes into the command's buffer.
  - The buffer is owned by the coordinator, so PTY readers send byte chunks via
    channels to avoid concurrent access.
  - When output arrives, the reader sends a byte chunk message to the coordinator.
  - The PTY reader continues running after foreground execution finishes to
    capture background job output.
- **Message-passing updates**:
  - Background goroutines (the session worker, PTY readers) never mutate widget data.
  - When output arrives or a command completes, the producer sends an update
    message to the host's event channel (`msgCh`).
  - The coordinator loop picks up the message, appends the byte chunk to the
    target `CommandBlock.Output` buffer, and schedules a redraw.

## How the widget turns output into blocks

1. You type a command line and press Enter.
2. The widget appends an active `CommandBlock` with `Started = time.Now()` and
   sends the command line to the session worker.
3. The session worker creates a byte buffer for this command and configures
   mvdan's runner to write to it.
4. As the command runs, output bytes flow into the buffer (from builtins or PTY
   subprocesses).
5. The session sends incremental byte chunk messages to the coordinator.
6. The coordinator receives byte chunks, appends them to the active block's
   `Output` buffer, and schedules a redraw.
7. On redraw, the widget passes `block.Output.Bytes()` through
   `terminal.FromANSI(bytes, currentWidth)` to get a `CellBuf` for rendering.
8. When the foreground command finishes, a completion message arrives containing
   the exit code and updated `Dir()`.
9. The coordinator sets `Returned = time.Now()`, marks the block status
   (success/error), updates the prompt path from `Dir()`, and re-enables line
   editing for the next command.
10. If background processes later emit output, their bursts are appended to
    Command $N$'s `Output` buffer, trigger byte chunk messages, and the widget
    re-renders that block without disturbing the active prompt.

## Rendering strategy: from raw bytes to screen cells

The widget **never** stores rendered `CellBuf` objects in the data model. Instead,
rendering happens on-demand during the paint cycle by passing raw bytes through
the terminal emulator.

### Pure function rendering (current implementation)

The `terminal` package provides `FromANSI(bytes []byte, width int) CellBuf`, which:
- Creates a fresh `vt.Emulator` instance at the specified width
- Feeds the entire byte stream into it
- Extracts the resulting terminal grid as a `CellBuf`
- Discards the emulator

This is a **pure function**: same bytes + same width → same CellBuf. This approach:
- Automatically handles window resizes (re-render all visible blocks at new width)
- Keeps the data model simple (just bytes)
- Makes testing trivial (`ToANSI(FromANSI(bytes, w))` produces normalized output)

**Tradeoff**: For long-running active commands with large output, re-parsing the
entire byte stream on every frame could become expensive.

### Stateful emulator optimization (future extension)

For the **active command only**, the terminal package could provide:
```go
type StatefulEmulator struct {
    // Wraps vt.Emulator, maintains state across writes
}
func (e *StatefulEmulator) Write(p []byte) (int, error)  // Incremental parse
func (e *StatefulEmulator) Snapshot() CellBuf            // Extract current grid
func (e *StatefulEmulator) Resize(w, h int)              // Handle window resize
```

The widget could maintain a `StatefulEmulator` for the active block, feeding
incremental byte chunks to it. On window resize, the widget would:
1. Discard the stateful emulator
2. Create a new one at the new dimensions
3. Feed the entire `block.Output.Bytes()` to re-initialize state

**Historical blocks** (where `Returned != nil`) should always use the pure function
approach. Their bytes are immutable, so the widget can cache the rendered `CellBuf`
and invalidate the cache on window resize.

### Rendering layer separation

- **Session layer**: No emulator knowledge. Collects raw bytes only.
- **Terminal package** (`kabibi/ui/terminal/cell.go`): Owns all emulator integration.
  Provides pure `FromANSI()` function and (optionally) stateful emulator wrapper.
- **Widget layer**: Calls terminal package functions to render blocks. Never directly
  touches `vt.Emulator` or ANSI parsing logic.

### Window resize handling

When the terminal window is resized:

1. **Active command**: The session's `Resize(width, height)` method:
   - Sends `TIOCSWINSZ` ioctl to all active PTYs
   - If using a stateful emulator (future), the widget discards it, creates a
     fresh one at new dimensions, and re-feeds `block.Output.Bytes()`
2. **Historical blocks**: The widget simply re-renders them from raw bytes at the
   new width on the next paint cycle. If caching is implemented, the cache is
   invalidated.
3. **Data model**: Window resize does not touch `CommandBlock` structures at all.
   The raw bytes remain unchanged.

This is the key advantage of storing raw bytes instead of pre-rendered `CellBuf`:
resize is a pure rendering concern, not a data migration problem.

## PTY allocation for external subprocesses

External commands are detected and routed through PTYs when appropriate, giving
them native terminal behavior (colors, proper buffering, correct `isatty()` results).

### When PTY is allocated

The session's `ExecHandler` (the interception point for all command execution in
mvdan's runner) inspects each command invocation:

- **Shell builtins** (`cd`, `echo`, `export`, etc.): No PTY. Output goes directly
  to the buffer.
- **Shell scripts** (detected by `.sh` extension or shebang): No PTY. Recursively
  executed through a new mvdan runner, and commands within the script follow
  these same rules.
- **External commands in pipelines** (e.g., `ls | grep foo`): No PTY. The child's
  stdout is a pipe to the next command, not the session writer. `isatty()` returns
  false, which matches real shell behavior (`ls --color=auto | cat` disables colors).
- **External commands with redirections** (e.g., `ls > output.txt`): No PTY. The
  child's stdout is a file, not the session writer.
- **Standalone external commands** (e.g., `ls`, `git status`): PTY is allocated.
  The child's stdout/stderr/stdin are connected to the PTY slave, and the PTY
  master feeds the buffer. `isatty()` returns true, so `ls --color=auto` emits
  ANSI color codes.

### PTY lifecycle

1. When `ExecHandler` detects a standalone external command, it allocates a PTY
   (platform-specific: Unix `pty.Start`, Windows `ConPTY`).
2. The PTY is configured with the current terminal size (from the last `Resize()` call).
3. The child process is spawned with its file descriptors connected to the PTY slave.
4. A dedicated goroutine reads from the PTY master and writes bytes into the
   command's buffer.
5. User input (via `session.WriteStdin()`) is routed to the PTY master's write end.
6. When the foreground command exits, the PTY remains open to capture output from
   any background processes spawned by the command.
7. The PTY is closed when all child processes exit or when the session shuts down.

### Each command starts with fresh output buffer

Every command gets its own byte buffer, which starts empty. This is a **feature**
of the structured history model:

- Each `CommandBlock.Output` is self-contained and independently renderable.
- No hidden state leaks from one command to the next.
- Background job output is routed to the correct block and cannot corrupt later commands.
- The widget can render any block in isolation (useful for scrollback, collapsing,
  or side-by-side diff views).
- Window resizes automatically adapt: each block's raw bytes are re-rendered
  through the terminal emulator at the new dimensions.

This differs from a traditional terminal's persistent scrollback, which is a single
linear stream with shared state. The tradeoff is that **full-screen TUI applications**
(`vim`, `less`, `top`) that expect to manipulate persistent screen state across
invocations won't work correctly. Each command's ANSI stream is independent, so
sequences that assume pre-existing screen content will render incorrectly. This is
an acceptable limitation for v1, where the focus is on command-line tools and
short-lived processes.

## Cross-platform PTY strategy

PTY allocation is implemented using platform-specific build tags and a unified
abstraction:

### Unix (Linux, macOS, BSD)
- Use `github.com/creack/pty` (or direct `syscall` on Linux).
- Allocate via `/dev/ptmx`, configure with `TIOCSWINSZ` for resize.
- Mature, battle-tested, minimal dependency.

### Windows 10 1809+ (ConPTY)
- Use Windows ConPTY API (`CreatePseudoConsole`, `ResizePseudoConsole`, `ClosePseudoConsole`).
- Accessed via `kernel32.dll` using `purego` (pure-Go, no CGo) or an existing
  wrapper like `github.com/UserExistsError/conpty`.
- ConPTY is a first-class native Windows feature with full ANSI/VT support.

### Older Windows (<1809)
- Graceful degradation: Fall back to plain pipes.
- Set `NO_COLOR=1` environment variable to hint programs to disable color codes.
- Display a one-time warning: "PTY requires Windows 10 1809 or newer."

### Unified abstraction

```go
// pty/pty.go (common interface)
type PTY interface {
    io.ReadWriteCloser
    Resize(width, height int) error
}

func Start(cmd *exec.Cmd, width, height int) (PTY, error)
```

Platform-specific implementations in `pty_unix.go` and `pty_windows.go` (build-tagged).
The session layer only imports the common interface, keeping platform concerns isolated.

## Testing the two halves

- **Widget alone**: Drive the widget via synthetic host messages containing raw
  byte chunks and assert on the resulting screen cell buffer output. No real shell
  processes are needed.
- **Session alone**: Feed command lines to the session worker and inspect the
  resulting raw byte output collected in `CommandBlock.Output`. Assert on:
  - Exit codes and timing (`Started`, `Returned`)
  - Raw bytes match expected output
  - Correct routing of background output to the originating `CommandBlock`
  - Working directory updates
- **Rendering correctness**: To verify visual output, pass `block.Output.Bytes()`
  through `terminal.FromANSI(bytes, width)` to get a `CellBuf`, then call
  `terminal.ToANSI(cellBuf)` to produce normalized ANSI output. Assert against
  golden normalized ANSI strings. This gives a single source of truth that's both
  human-readable and stable across rendering implementations.
- **Terminal package boundary**: The `terminal.FromANSI()` function becomes a
  testable seam. You can feed mock ANSI sequences and verify the resulting
  `CellBuf` output without spawning real subprocesses or PTYs.
