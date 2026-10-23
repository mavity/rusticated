# 8. Stateful Terminal Rendering Backend & Synchronized PTY Framing

## 1. Overview & Motivation

Currently, `terminal.FromANSI(raw string, width, height int) (CellBuf, CursorPos)` is a stateless, one-shot function: on every call it constructs a fresh `vt.Emulator`, feeds the entire accumulated byte stream from the beginning, and extracts a `CellBuf` and `CursorPos`.

While stateless replay works well for static text or immutable historical blocks, it has three fundamental flaws when applied to live interactive PTY child processes:

1. **Historical Reflow Corruption on Resize:**
   - When a child PTY runs at dimensions `(W1, H1)` and the window is later resized to `(W2, H2)`, replaying the entire historical byte transcript from byte 0 into a fresh `(W2, H2)` emulator misinterprets cursor movements (`\x1b[H`, `\x1b[A`, `\x1b[2J`, relative jumps) that were calculated by the child process for `(W1, H1)`.
2. **O(N) Reparsing per Frame:**
   - As an interactive process emits output over time, re-feeding the entire `block.Output` buffer on every paint cycle scales linearly with total transcript size rather than incremental chunk size.
3. **Arbitrary 24-Row Framing Mismatch:**
   - Foreground child processes were previously spawned or replayed with an arbitrary 24-row height or mismatched viewport height. A foreground child in Kabibi's shell stack always coexists with at least 1 reserved row of shell chrome (`reservedFooterRows = 1`), so framing its PTY and virtual terminal into `width × max(height - 1, 1)` matches the actual usable stack viewport. AI `exec` uses its separate compact frame policy below.

### Architectural Solution

1. **Introduce a stateful `terminal.Terminal` abstraction** in `kabibi/ui/terminal` that owns a persistent `vt.Emulator`, initializes it with LNM enabled, writes input bytes unchanged, supports live `Resize(width, height)` without replaying past bytes, and produces `(CellBuf, CursorPos)` snapshots on demand.
2. **Keep `FromANSI(raw string, width, height int)`** as a thin convenience wrapper over `terminal.Terminal` (`NewTerminal` -> `WriteString` -> `Snapshot`). It accepts one raw string and does not rewrite or normalize its bytes.
3. **Use one live-output rendering pipeline for foreground shell commands and AI `exec` output**, with per-source policies for PTY dimensions and input. Both hand output to the UI-owned per-block stateful `terminal.Terminal` through a coalescing pending-output buffer; their execution and input behavior remain distinct.
4. **Synchronize resizes across both the OS PTY and the rendering backend `Terminal`** whenever the shell receives a window resize event, preserving already-rendered terminal state while adapting to the new dimensions.

---

## 2. Viability & Technical Assessment

### 2.1 `charmbracelet/x/vt` Emulator Capabilities
- **Stateful Streaming Parser:** `vt.Emulator.Write([]byte)` and `WriteString(string)` maintain full ANSI/VT parser state across arbitrary chunk boundaries, including split CSI/OSC escape sequences and multi-byte UTF-8 graphemes.
- **In-Place Resize:** `vt.Emulator.Resize(width, height)` resizes the active screen grid in place while preserving existing cell contents and scrollback lines, clamping the cursor to the new bounds without re-evaluating historical escape sequences.
- **Cursor Visibility Tracking:** `vt.Callbacks{CursorVisibility: ...}` updates a boolean state field whenever `DECTCEM` (`\x1b[?25l` / `\x1b[?25h`) is processed in the stream.

### 2.2 LNM Initialization and Raw Byte Ingestion
- The pinned `github.com/charmbracelet/x/vt` version supports ANSI Line Feed/New Line Mode (LNM), exposed as `ansi.ModeLineFeedNewLine` (mode 20). When set, the emulator's LF handling also returns the cursor to column zero.
- The emulator resets LNM by default. `NewTerminal` will explicitly set it during initialization by sending `ansi.SetMode(ansi.ModeLineFeedNewLine)` to the emulator.
- `Terminal.Write` and `WriteString` pass caller bytes unchanged to the emulator. There is no LF-to-CRLF rewriting, `lastByteWasCR` state, or stream preprocessor.
- This is the emulator's initial LNM state, not termios `OPOST`/`ONLCR`. The emulator continues to interpret in-band mode changes in the stream, including a child explicitly resetting LNM with `CSI 20 l`.
- Chunk boundaries therefore need no special newline handling: split CRLF is parsed as the same control bytes in sequence, and bare LF follows the emulator's current LNM state.

### 2.3 Concurrency & Ownership Model
- `vt.Emulator` is not internally goroutine-safe. A `terminal.Terminal` instance therefore requires a single synchronization owner; it will not add its own mutex.
- The `Host` coordinator owns widget event handling, measurement, and rendering. `Host.Invalidate` only requests a redraw; it does not dispatch widget data. Subprocess/PTY output callbacks run on worker goroutines, so they must not write to a live widget's terminal or rendered state directly.
- Live output is represented by pointer-backed state separate from a `CommandBlock` view. A worker appends incoming bytes to that state's pending buffer under a short mutex, releases the lock, then calls `Invalidate`. The buffer coalesces chunks and preserves their order even when redraw requests are coalesced.
- Before measuring or rendering, the UI goroutine drains pending bytes in a batch, appends them to the raw transcript, and feeds them unchanged to the state's `terminal.Terminal`. Terminal resize and snapshots also occur on the UI goroutine. The consumed buffer is recycled where practical to avoid per-chunk and per-frame allocation churn.
- The pending-output lock protects only the worker/UI handoff. It is released before invalidation and before terminal parsing. Do not embed the mutex directly in `CommandBlock`, which is copied by value; keep the live-output state behind a pointer. Multiple views of one managed process share that state, so the stream is consumed and parsed only once.
- `ManagedPTY` retains its existing `ManagedPTY.mu` ownership boundary for raw output and process state. Its tool-facing raw-output API remains separate from the UI-owned terminal state, which is keyed by the managed process ID when a UI sink is present.

### 2.4 Per-Source Framing Geometry
- In `ModeStack` while a foreground command is running, `PromptInput` is hidden and only `reservedFooterRows` (`1` blank footer row) is pinned at the bottom of the shell's viewport `rect.H`.
- Therefore, the maximum vertical space available for the active command block before scrollback ejection is `max(rect.H - 1, 1)`.
- **Foreground executable command profile:** frame both the child OS PTY and its `terminal.Terminal` backend to `cols = width`, `rows = max(height - 1, 1)`. This preserves the current interactive command behavior, including its input path.
- **AI `exec` profile:** use the same live-output rendering pipeline with a different frame: `cols = max(width - 2, 1)`, `rows = min(max(height - 1, 1), 20)`. The two-column inset and 20-row cap keep tool output visually compact. A direct executable child receives stdin from its PTY; `term_input` remains the explicit AI-tool input path, while shell UI keystrokes are not routed to that background PTY. Do not separately inherit the host shell's stdin. In-process shell/script execution retains the `ExecutionSession` runner's stdin behavior (currently EOF); do not add a second input stream.
- The foreground profile ensures:
  - Full-screen or cursor-positioning child processes (such as `node`, `python`, `vim`, `htop`, or REPLs) target a screen height that fits inside the shell stack without immediately forcing their own prompt row off-screen.
- The AI `exec` profile uses the same dimensions for its child PTY and UI terminal backend so the process and rendering geometry remain aligned.

---

## 3. Component Design

### 3.1 `terminal.Terminal` (`kabibi/ui/terminal/cell.go`)

Introduce `Terminal` as the canonical stateful VT emulator backend:

```go
// Terminal is a stateful virtual terminal backend that ingests raw ANSI byte
// streams incrementally, supports live resize without replaying prior output,
// and produces CellBuf + CursorPos snapshots. It is not goroutine-safe; callers
// must serialize all operations on an instance.
type Terminal struct {
	term          *vt.Emulator
	width         int
	height        int
	cursorVisible bool
}
```

#### Core Methods:
1. **`NewTerminal(width, height int) *Terminal`**
   - Normalizes `width <= 0` to `80` and `height <= 0` to `24`.
   - Initializes `newUnicodeCoreEmulator(width, height)` with `cursorVisible = true`, enables `ansi.ModeLineFeedNewLine`, and wires `vt.Callbacks{CursorVisibility: ...}`.
2. **`Write(p []byte) (int, error)` & `WriteString(s string) (int, error)`**
   - Feeds bytes unchanged into `t.term`; no newline rewriting or byte filtering.
   - The caller must serialize this operation with `Resize`, `Size`, and `Snapshot`.
3. **`Resize(width, height int)`**
   - Normalizes bounds (`width >= 1`, `height >= 1`).
   - If `width == t.width && height == t.height`, returns early (no-op).
   - Updates `t.width, t.height` and calls `t.term.Resize(width, height)`.
4. **`Size() (width, height int)`**
   - Returns current `(t.width, t.height)`; caller serialization is required.
5. **`Snapshot() (CellBuf, CursorPos)`**
   - Computes grid height via `calculateGridHeight(t.term)` (which uses `t.term.Scrollback().Len() + t.term.Height()` when scrollback is non-empty, or scans `t.term.Touched()` from the bottom up).
   - Reads `pos := t.term.CursorPosition()` and `sbLen := t.term.ScrollbackLen()`, constructing:
     ```go
     cur := CursorPos{
         X:       pos.X,
         Y:       sbLen + pos.Y,
         Visible: t.cursorVisible,
     }
     ```
   - Extracts the `CellBuf` via `populateCellBufDirect(t.term, t.width, gridHeight)`.

### 3.2 `FromANSI` as a Simplified Wrapper (`kabibi/ui/terminal/cell.go`)

`FromANSI` becomes a 3-line wrapper around `Terminal`:

```go
// FromANSI renders a raw ANSI stream into an exact-fit CellBuf and returns the
// cursor position and visibility reported by the virtual terminal emulator.
func FromANSI(raw string, width, height int) (CellBuf, CursorPos) {
	t := NewTerminal(width, height)
	_, _ = t.WriteString(raw)
	return t.Snapshot()
}
```

This eliminates duplicate emulator setup and callback wiring. The wrapper inherits the same initialized LNM mode and raw-byte parsing as streaming users.

---

## 4. Child Process Output & Resize Architecture in `kabibi/ui/shell`

### 4.1 Per-Source PTY Frame Policies

Define helpers in `kabibi/ui/shell` for computing each source's PTY dimensions from the shell's outer dimensions `(shellW, shellH)`:

```go
func childPTYFrameSize(shellW, shellH int) (cols, rows int) {
	cols = shellW
	if cols <= 0 {
		cols = 80
	}
	if shellH <= 0 {
		shellH = 24
	}
	rows = shellH - reservedFooterRows // height - 1
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}
```

- For AI `exec`, define `aiExecPTYFrameSize(shellW, shellH int) (cols, rows int)` with `cols = max(shellW - 2, 1)` and `rows = min(max(shellH - reservedFooterRows, 1), 20)`.
- When `Shell` is initialized (`New`), initialize the foreground `ExecutionSession` PTY with `childPTYFrameSize(s.width, s.height)` (`80 × 23` by default). Use the current viewport dimensions for AI `exec`.
- When `Shell.HandleEvent` receives `input.WindowSizeEvent{Width, Height}`, or `Shell.Render` observes that the viewport dimensions differ from the dimensions last applied, use the event or `rect` dimensions as `(shellW, shellH)`:
  - Compute the foreground frame with `childPTYFrameSize(shellW, shellH)`, resize the active foreground OS PTY and block terminal, and store the frame for future child processes.
  - Compute the AI frame with `aiExecPTYFrameSize(shellW, shellH)` and resize every live AI `exec` PTY in `bgProcs` and its shared UI terminal to that frame, whether or not it currently has a view in the visible stack.

### 4.2 Shared Live-Output Rendering Backend

Use the same UI-owned live-output state for foreground executable commands and AI `exec` tool output, with a per-source frame policy. Keep process execution, input routing, and PTY lifecycle specific to each source. A `CommandBlock` is a presentation occurrence; its pointer-backed live-output state owns the pending bytes, transcript, and terminal. Foreground commands have one occurrence. All occurrences for one AI-managed process share one state keyed by `ManagedPTY.ID`.

1. **Worker-to-UI output handoff:**
   - The output callback copies incoming bytes into the live-output state's pending buffer under its short mutex, unlocks, then requests invalidation. It does not append to the transcript or call `Terminal.Write` from the worker goroutine.
   - In `Measure` and `Render`, the UI goroutine drains each live-output state once per batch, appends bytes to its raw transcript, initializes its terminal if needed, and writes the unchanged bytes to it. Recycle the drained buffer as a spare when possible.
   - For non-PTY blocks, draining updates the raw transcript and leaves the existing stateless rendering path unchanged. PTY-backed blocks use the stateful terminal as their incremental rendering backend.
2. **Process and view lifecycle:**
   - Initialize the live-output state when a foreground command or AI `exec` process begins, using the source's configured frame policy. The state survives the command block's completion and, for AI `exec`, timeout promotion into `bgProcs`.
   - Keep the initial AI `exec` block at its original position in the append-only stack. It continues to reflect output while the process runs; if its rows are ejected, they continue into scrollback/Plume.
   - When a later AI `term_read` probes a process that has been promoted to the background, append a new `CommandBlock` view at the current stack position, linked to the same `ManagedPTY.ID` and live-output state. Do not restart or move the process, and do not remove the original view. Repeated probes update the reactivated view rather than appending one block per poll.
   - Each view snapshots the shared terminal state using its source's frame geometry. Completion updates the shared status; all views remain in their respective stack positions and show the final state.
3. **Terminal rendering and resize:**
   - The foreground profile uses `(width, max(height - 1, 1))`; the AI `exec` profile uses `(max(width - 2, 1), min(max(height - 1, 1), 20))`.
   - Track how many transcript bytes have been fed into the terminal with a `termOffset` in the shared live-output state. Normal streaming advances it during the UI drain; `renderBlockCached` catches up synthetic or otherwise unpumped test transcripts.
   - On `WindowSizeEvent` or a detected initial viewport-size change, resize the active foreground PTY and every live AI `ManagedPTY` OS PTY and live-output terminal together using their respective frame policies. This includes backgrounded processes without a reactivated view; do not resize exited processes. Terminal resize occurs on the UI goroutine; invalidate affected view caches.
   - In `renderBlockCached`, snapshot the shared terminal for PTY-backed views, hide the cursor after process completion, trim trailing whitespace rows, then blit the prompt/input prefix and terminal output together, adjusting `outputCursor.Y` by the prefix height.
   - Drain all queued bytes before a completed process's final snapshot. Retain the terminal state; do not reparse its transcript at a different size.

### 4.3 AI `exec` PTY Lifecycle (`kabibi/ui/shell/pty_manager.go`, `exec_session.go`)

The AI `exec` tool is similar to a foreground command for live VT output, but it uses the AI `exec` frame and input routing defined above. Reuse the live-output rendering backend for its `SourceAITool` UI block; do not route shell UI keystrokes to its background PTY.

1. Configure AI `exec` PTYs with `cols = max(width - 2, 1)` and `rows = min(max(height - 1, 1), 20)`. Keep these dimensions in sync with the AI `exec` block's UI terminal when the viewport changes.
2. Direct executable children receive stdin through their PTY, and `term_input` remains available to the AI tool. Shell UI keystrokes continue to target only the foreground user-shell PTY. Do not add a separate inherited host-stdin path. In-process shell/script commands retain the runner's configured stdin behavior.
3. `ManagedPTY` remains the owner of raw output for the AI tool read API. Its `appendOutput` updates `rawBuf` under `m.mu`, then invokes the UI callback outside that lock. The callback enqueues bytes into the process's shared live-output state. Keep `ManagedPTY.Snapshot(onlyNew, keepANSI)` as a raw-output API.
4. Register the `ManagedPTY` and obtain its stable ID before creating its initial UI view. Associate the view and shared live-output state with that ID.
5. When `term_read` probes a background process with a UI sink, request a reactivated view in the current stack. Reuse the same process state and keep the initial view in place; later probes update the reactivated view.
6. Route viewport changes to every live AI `ManagedPTY` and its shared UI terminal using `aiExecPTYFrameSize`, regardless of whether the process currently has a visible or reactivated view. Do not resize exited processes.

---

## 5. Step-by-Step Implementation Plan

### Step 1: Stateful `terminal.Terminal` & `FromANSI` Redirect (`kabibi/ui/terminal/cell.go`)
1. Implement `type Terminal struct` with `NewTerminal(width, height int) *Terminal`, `Write(p []byte) (int, error)`, `WriteString(s string) (int, error)`, `Resize(width, height int)`, `Size() (int, int)`, and `Snapshot() (CellBuf, CursorPos)`.
2. Initialize the emulator with LNM enabled using `ansi.SetMode(ansi.ModeLineFeedNewLine)`; keep `Write` and `WriteString` as unchanged-byte feeds into `vt.Emulator`.
3. Replace the body of `FromANSI(raw string, width, height int) (CellBuf, CursorPos)` with:
   ```go
   t := NewTerminal(width, height)
   _, _ = t.WriteString(raw)
   return t.Snapshot()
   ```
4. Update `kabibi/ui/terminal/CELL.md` to document `Terminal`, initial LNM behavior, unchanged-byte ingestion, and `FromANSI`'s delegation to it.

### Step 2: Unit Tests for `terminal.Terminal` (`kabibi/ui/terminal/cell_test.go`)
1. **Streaming Chunk Split Tests:**
   - Verify that feeding ANSI sequences and multi-byte UTF-8 graphemes across arbitrary `Write` boundaries produces the same `(CellBuf, CursorPos)` as one-shot `FromANSI`.
   - Verify that split `\r\n` is consumed as the original two control bytes, and that a bare LF returns the cursor to column zero because LNM is initialized on.
   - Verify that an in-band `CSI 20 l` changes the emulator's LNM state and subsequent bare LF follows that state.
2. **Stateful Resize Preservation Tests:**
   - Write content and cursor movements at `(W1, H1)`, call `Resize(W2, H2)`, write additional content, and assert that already-rendered rows are preserved without historical replay corruption and the cursor reflects the new post-resize writes.

### Step 3: Synchronized `width × (height - 1)` Framing & Backend Pumping in `Shell` (`kabibi/ui/shell/shell.go`, `shell_widget.go`, `exec_session.go`, `pty_manager.go`)
1. Add `childPTYFrameSize(w, h int) (cols, rows int)` returning `(w, max(h-1, 1))`, and `aiExecPTYFrameSize(w, h int) (cols, rows int)` returning `(max(w-2, 1), min(max(h-1, 1), 20))`.
2. Update `NewExecutionSession` default `ptyRows` to `23` (`24 - 1`) and `Shell.New` / `Shell.HandleEvent(WindowSizeEvent)` to size foreground `ExecutionSession` PTYs with `childPTYFrameSize`. Use `aiExecPTYFrameSize` for AI `exec` processes.
3. Add pointer-backed shared live-output state and a per-source frame policy to `CommandBlock`; store the terminal and `termOffset` in that shared state.
   - Worker output callbacks append bytes to the pending buffer under its short mutex, release the lock, then invalidate. Do not write to the terminal or mutate rendered output from the worker.
   - In both `Measure` and `Render`, the UI goroutine drains each process's pending bytes in order, appends them to the shared raw transcript, and feeds them to the shared terminal. Coalesce chunks and recycle buffer capacity where practical.
   - Multiple `CommandBlock` views for one AI `ManagedPTY` reference the same live-output state. Initialize, resize, and snapshot the terminal only on the UI goroutine. Keep the stateless rendering path for non-PTY blocks.
   - Preserve completed process state and ensure queued bytes are drained before its final snapshot.
4. Preserve AI `exec` PTY stdin and the `term_input` tool; do not separately inherit host stdin or route shell UI keystrokes to background AI PTYs. Keep in-process runner stdin behavior unchanged.
5. On background promotion, keep the original AI `exec` view live in its stack position. On a later `term_read`, append a linked view for that process to the current stack without moving/removing the original.
6. Route AI `exec` viewport resizing to its active `ManagedPTY` and shared UI terminal using the compact frame policy.
7. Preserve `ManagedPTY.Snapshot(onlyNew, keepANSI)` as a raw-output API.

### Step 4: Shell & PTY Regression Tests (`kabibi/ui/shell/shell_ai_test.go`, `pty_interactive_test.go`)
1. Test that `WindowSizeEvent{Width: 100, Height: 30}` configures a foreground command PTY and terminal to `(100, 29)`, and every live AI `exec` PTY and terminal, including backgrounded processes with no current view, to `(98, 20)`; verify exited processes are not resized.
2. Test that multiple worker chunks coalesce in order in the pending buffer, are drained on the UI goroutine without loss, and produce the expected raw transcript, terminal cells, and cursor. Verify a redraw invalidation does not need to occur once per chunk.
3. Test that the final queued output is drained before a completed PTY block's terminal snapshot hides the cursor.
4. Test that streaming chunks into an active PTY block across a mid-stream `WindowSizeEvent` preserves prior terminal state in `block.term` and updates the active cursor position and trimmed height accurately.
5. Test that AI direct-child stdin remains the PTY and `term_input` reaches that PTY; verify shell UI keystrokes are not routed to background AI processes and in-process runner stdin behavior is unchanged.
6. Test that background promotion leaves the original view live and in place, and a later `term_read` creates a linked current-stack view sharing the same process state without restarting the process or duplicating output ingestion.
7. Test that `ManagedPTY.Snapshot(onlyNew, keepANSI)` retains its existing raw-output semantics.
8. Run `gofmt` and validate `go test ./ui/terminal ./ui/shell`.
