# 8. Stateful Terminal Rendering Backend & Synchronized PTY Framing

## 1. Overview & Motivation

Currently, `terminal.FromANSI(raw string, width, height int) (CellBuf, CursorPos)` is a stateless, one-shot function: on every call it constructs a fresh `vt.Emulator`, feeds the entire accumulated byte stream from the beginning, and extracts a `CellBuf` and `CursorPos`.

While stateless replay works well for static text or immutable historical blocks, it has three fundamental flaws when applied to live interactive PTY child processes:

1. **Historical Reflow Corruption on Resize:**
   - When a child PTY runs at dimensions `(W1, H1)` and the window is later resized to `(W2, H2)`, replaying the entire historical byte transcript from byte 0 into a fresh `(W2, H2)` emulator misinterprets cursor movements (`\x1b[H`, `\x1b[A`, `\x1b[2J`, relative jumps) that were calculated by the child process for `(W1, H1)`.
2. **O(N) Reparsing per Frame:**
   - As an interactive process emits output over time, re-feeding the entire `block.Output` buffer on every paint cycle scales linearly with total transcript size rather than incremental chunk size.
3. **Arbitrary 24-Row Framing Mismatch:**
   - Child processes were previously spawned or replayed with an arbitrary 24-row height or mismatched viewport height. A child process in Kabibi's shell stack always coexists with at least 1 reserved row of shell chrome (`reservedFooterRows = 1`), so framing the child PTY and its virtual terminal into `width × max(height - 1, 1)` matches the actual usable stack viewport.

### Architectural Solution

1. **Introduce a stateful `terminal.Terminal` abstraction** in `kabibi/ui/terminal` that owns a persistent `vt.Emulator`, handles streaming byte writes with stateful LF-to-CRLF normalization across chunk boundaries, supports live `Resize(width, height)` without replaying past bytes, and produces `(CellBuf, CursorPos)` snapshots on demand.
2. **Redirect `FromANSI(raw, width, height)`** to be a thin convenience wrapper over `terminal.Terminal` (`NewTerminal` -> `WriteString` -> `Snapshot`), guaranteeing a single canonical VT ingestion implementation across one-shot and streaming callers.
3. **Frame child PTYs into `width × max(height - 1, 1)`** and pump their live output chunks directly into a per-block stateful `terminal.Terminal` instance (acting as the block's rendering backend).
4. **Synchronize resizes across both the OS PTY and the rendering backend `Terminal`** whenever the shell receives a window resize event, preserving already-rendered terminal state while adapting to the new dimensions.

---

## 2. Viability & Technical Assessment

### 2.1 `charmbracelet/x/vt` Emulator Capabilities
- **Stateful Streaming Parser:** `vt.Emulator.Write([]byte)` and `WriteString(string)` maintain full ANSI/VT parser state across arbitrary chunk boundaries, including split CSI/OSC escape sequences and multi-byte UTF-8 graphemes.
- **In-Place Resize:** `vt.Emulator.Resize(width, height)` resizes the active screen grid in place while preserving existing cell contents and scrollback lines, clamping the cursor to the new bounds without re-evaluating historical escape sequences.
- **Cursor Visibility Tracking:** `vt.Callbacks{CursorVisibility: ...}` updates a boolean state field whenever `DECTCEM` (`\x1b[?25l` / `\x1b[?25h`) is processed in the stream.

### 2.2 Stateful LF Normalization Across Chunk Boundaries
- In `FromANSI`, bare `\n` bytes (not preceded by `\r`) are converted to `\r\n` because the VT emulator does not enable LNM (Linefeed/Newline Mode) by default.
- In a streaming `Terminal.Write(p []byte)` implementation, a `\r\n` sequence may be split across two consecutive `Write` calls (`chunk 1` ends with `'\r'`, `chunk 2` starts with `'\n'`).
- **Solution:** `Terminal` tracks `lastByteWasCR bool`. When scanning incoming bytes:
  - If `b == '\n'` and `!lastByteWasCR`, emit `\r\n` to the underlying emulator.
  - If `b == '\n'` and `lastByteWasCR`, emit `\n` directly (since `\r` was already written at the end of the previous chunk or earlier in the current chunk).
  - Update `lastByteWasCR = (b == '\r')` after each byte.
- Because ` '\r' ` (0x0D) and ` '\n' ` (0x0A) are ASCII control characters (0x00–0x7F), they never appear as continuation bytes in valid UTF-8 sequences and only terminate CSI/OSC sequences when malformed, making byte-level `lastByteWasCR` tracking completely safe across arbitrary chunk splits.

### 2.3 Concurrency & Ownership Model
- `vt.Emulator` is not internally goroutine-safe.
- **Solution:** Either guard `terminal.Terminal` methods (`Write`, `Resize`, `Snapshot`) with an internal `sync.Mutex`, or access the block's `Terminal` exclusively under `Shell.mu` (and `ManagedPTY.mu`). Adding an internal `sync.Mutex` inside `terminal.Terminal` is low-cost, prevents data races if `Snapshot()` and `Write()` ever overlap, and makes `Terminal` safe as a general-purpose primitive.

### 2.4 Framing Geometry: `width × max(height - 1, 1)`
- In `ModeStack` while a foreground command is running, `PromptInput` is hidden and only `reservedFooterRows` (`1` blank footer row) is pinned at the bottom of the shell's viewport `rect.H`.
- Therefore, the maximum vertical space available for the active command block before scrollback ejection is `max(rect.H - 1, 1)`.
- Framing both the child OS PTY and its `terminal.Terminal` backend to `cols = width`, `rows = max(height - 1, 1)` ensures:
  - Full-screen or cursor-positioning child processes (such as `node`, `python`, `vim`, `htop`, or REPLs) target a screen height that fits inside the shell stack without immediately forcing their own prompt row off-screen.
  - For `SourceAITool` blocks (which render inside a 2-column left gutter stripe), the width is `max(width - 2, 20)` and height is `max(height - 1, 1)`.

---

## 3. Component Design

### 3.1 `terminal.Terminal` (`kabibi/ui/terminal/cell.go`)

Introduce `Terminal` as the canonical stateful VT emulator backend:

```go
// Terminal is a stateful virtual terminal backend that ingests raw ANSI byte
// streams incrementally, supports live resize without replaying prior output,
// and produces CellBuf + CursorPos snapshots.
type Terminal struct {
	mu            sync.Mutex
	term          *vt.Emulator
	width         int
	height        int
	cursorVisible bool
	lastByteWasCR bool
	hasWritten    bool
}
```

#### Core Methods:
1. **`NewTerminal(width, height int) *Terminal`**
   - Normalizes `width <= 0` to `80` and `height <= 0` to `24`.
   - Initializes `newUnicodeCoreEmulator(width, height)` with `cursorVisible = true` and wires `vt.Callbacks{CursorVisibility: ...}`.
2. **`Write(p []byte) (int, error)` & `WriteString(s string) (int, error)`**
   - Acquires `t.mu`.
   - Streams bytes into `t.term`, expanding bare `\n` (where `!t.lastByteWasCR`) into `\r\n` and updating `t.lastByteWasCR`.
   - Marks `t.hasWritten = true` when `len(p) > 0`.
3. **`Resize(width, height int)`**
   - Acquires `t.mu`.
   - Normalizes bounds (`width >= 1`, `height >= 1`).
   - If `width == t.width && height == t.height`, returns early (no-op).
   - Updates `t.width, t.height` and calls `t.term.Resize(width, height)`.
4. **`Size() (width, height int)`**
   - Returns current `(t.width, t.height)`.
5. **`Snapshot() (CellBuf, CursorPos)`**
   - Acquires `t.mu`.
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

This eliminates any duplicate emulator setup, callback wiring, or newline-normalization loops.

---

## 4. Child Process Output & Resize Architecture in `kabibi/ui/shell`

### 4.1 Framing Formula: `childPTYFrameSize`

Define a single helper in `kabibi/ui/shell` for computing child PTY dimensions from the shell's outer dimensions `(shellW, shellH)`:

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

- When `Shell` is initialized (`New`), initialize `execSess.ResizePTY(childPTYFrameSize(s.width, s.height))` (`80 × 23` by default until the first `WindowSizeEvent` or initial render).
- When `Shell.HandleEvent` receives `input.WindowSizeEvent{Width, Height}` (or when `Shell.Render` observes an outer viewport size change Before any active PTY has been sized):
  - Compute `ptyCols, ptyRows := childPTYFrameSize(evt.Width, evt.Height)`.
  - Forward `execSess.ResizePTY(ptyCols, ptyRows)` to resize the active foreground OS PTY (and update `ptyCols, ptyRows` for future child processes).
  - Resize the active block's stateful `terminal.Terminal` backend (`block.term.Resize(ptyCols, ptyRows)`) so the rendering backend stays in lockstep with the OS PTY.

### 4.2 Per-Block Stateful Rendering Backend (`CommandBlock`)

Update `CommandBlock` in `kabibi/ui/shell/shell.go`:

1. Add `Term *terminal.Terminal` (or unexported `term *terminal.Terminal` + `termOffset int` watermark) to `CommandBlock`:
   - **Why track `termOffset int` (or pump directly on append)?**
   - Pumping directly when chunks arrive in `appendBlockText` / `OnAICommandStart` is primary.
   - Additionally, tracking `termOffset int` (how many bytes of `block.Output` have been fed into `block.term`) allows `renderBlockCached` to lazily initialize `block.term` and catch up any unpumped bytes if a block is constructed synthetically in unit tests (`CommandBlock{UsesPTY: true, Output: ...}`).
2. **Lifecycle of `block.term` for Foreground PTY Commands:**
   - When a foreground command attaches a PTY (`execSess.SetActivePTY(p)` or first PTY chunk in `appendBlockText`):
     - Ensure `block.UsesPTY = true`.
     - Ensure `block.term` is initialized at the session's current PTY frame size `(ptyCols, ptyRows)` (`width × (height - 1)`).
   - As each chunk arrives from the PTY (`appendBlockText`):
     - Append `chunk` to `block.Output` (preserving the raw transcript for history records).
     - Feed `chunk` into `block.term.WriteString(text)` and advance `block.termOffset = block.Output.Len()`.
   - On window resize (`WindowSizeEvent`):
     - Compute `cols, rows := childPTYFrameSize(evt.Width, evt.Height)`.
     - Resize the OS PTY via `execSess.ResizePTY(cols, rows)`.
     - Under `s.mu.Lock()`, if the active block has `block.term != nil`:
       - Call `block.term.Resize(cols, rows)`.
       - Invalidate `block.cachePopulated = false` so the next frame snapshots the resized `block.term`.
   - Inside `renderBlockCached(block, width, height)`:
     - When `block.UsesPTY` is true:
       - If `block.term == nil`, lazily create `block.term = terminal.NewTerminal(ptyW, ptyH)`.
       - If `block.termOffset < block.Output.Len()`, feed `block.Output.String()[block.termOffset:]` into `block.term` and update `block.termOffset`.
       - Call `outputBuf, outputCursor := block.term.Snapshot()`.
       - If `block.Status != StatusActive`, set `outputCursor.Visible = false`.
       - Run `outputBuf, outputCursor = terminal.TrimTrailingWhitespaceRows(outputBuf, outputCursor)`.
       - Blit `prefixBuf` (`block.Prompt + block.Input + "\n"`) and `outputBuf` together, adjusting `outputCursor.Y += prefixBuf.Height`.
   - When the foreground command finishes:
     - `block.Status` transitions from `StatusActive` to `StatusSuccess` / `StatusError`.
     - `block.term` is retained (or its final settled state is snapshotted with `outputCursor.Visible = false`), so completed PTY blocks never re-parse their raw transcript at a different size and never expand with trailing blank cursor rows.

### 4.3 `ManagedPTY` & `SourceAITool` Alignment (`kabibi/ui/shell/pty_manager.go`, `exec_session.go`)

Apply the same stateful `terminal.Terminal` backend to `ManagedPTY` (used by AI `exec` tool commands and backgrounded PTYs):

1. Add `term *terminal.Terminal` to `ManagedPTY`, initialized in `ExecuteAIToolCommand` with `terminal.NewTerminal(ptyCols, ptyRows)` where `ptyCols = max(termCols - 2, 20)` and `ptyRows = max(termRows, 1)` (since `termRows` on `ExecutionSession` is already `height - 1`).
2. In `ManagedPTY.appendOutput(p []byte)`, write `p` to both `m.rawBuf` and `m.term.Write(p)`.
3. Add `ManagedPTY.Resize(cols, rows int)` so if the window resizes while a `ManagedPTY` is running, both `m.ptyHandle.Resize(cols, rows)` and `m.term.Resize(cols, rows)` are updated.
4. In `ManagedPTY.Snapshot(onlyNew, keepANSI)`:
   - When `onlyNew == false`, snapshot directly from `m.term.Snapshot()` and pass the `CellBuf` to `CleanCellBufToText(buf, keepANSI)`.
5. For `SourceAITool` UI blocks in `Shell.OnAICommandStart`:
   - Give the `SourceAITool` `CommandBlock` its own `block.term = terminal.NewTerminal(innerW, ptyRows)` and pump chunks into it in `onChunk`, resizing it to `(max(w-2, 1), max(h-1, 1))` on window resize.

---

## 5. Step-by-Step Implementation Plan

### Step 1: Stateful `terminal.Terminal` & `FromANSI` Redirect (`kabibi/ui/terminal/cell.go`)
1. Implement `type Terminal struct` with `NewTerminal(width, height int) *Terminal`, `Write(p []byte) (int, error)`, `WriteString(s string) (int, error)`, `Resize(width, height int)`, `Size() (int, int)`, and `Snapshot() (CellBuf, CursorPos)`.
2. Move the stateful `lastByteWasCR` LF-to-CRLF normalization into `Terminal.Write`.
3. Replace the body of `FromANSI(raw string, width, height int) (CellBuf, CursorPos)` with:
   ```go
   t := NewTerminal(width, height)
   _, _ = t.WriteString(raw)
   return t.Snapshot()
   ```
4. Update `kabibi/ui/terminal/CELL.md` to document `Terminal` and `FromANSI`'s delegation to it.

### Step 2: Unit Tests for `terminal.Terminal` (`kabibi/ui/terminal/cell_test.go`)
1. **Streaming Chunk Split Tests:**
   - Verify that feeding ANSI sequences, multi-byte UTF-8 graphemes, and `\r\n` split across byte-by-byte `Write` calls produces the exact same `(CellBuf, CursorPos)` as one-shot `FromANSI`.
   - Specifically test `\r` at the end of chunk 1 followed by `\n` at the start of chunk 2 (asserting it advances 1 row, not 2).
2. **Stateful Resize Preservation Tests:**
   - Write content and cursor movements at `(W1, H1)`, call `Resize(W2, H2)`, write additional content, and assert that already-rendered rows are preserved without historical replay corruption and the cursor reflects the new post-resize writes.

### Step 3: Synchronized `width × (height - 1)` Framing & Backend Pumping in `Shell` (`kabibi/ui/shell/shell.go`, `shell_widget.go`, `exec_session.go`, `pty_manager.go`)
1. Add `childPTYFrameSize(w, h int) (cols, rows int)` returning `(w, max(h-1, 1))`.
2. Update `NewExecutionSession` default `ptyRows` to `23` (`24 - 1`) and `Shell.New` / `Shell.HandleEvent(WindowSizeEvent)` to size `ExecutionSession` PTY dimensions to `childPTYFrameSize(w, h)`.
3. Add `term *terminal.Terminal` and `termOffset int` to `CommandBlock`:
   - Initialize `block.term` when a PTY command starts or receives output, sized to `execSess.PTYSize()`.
   - Pump incoming chunks in `appendBlockText` (and `OnAICommandStart`) directly into `block.term.WriteString(...)`.
   - On `WindowSizeEvent`, resize `execSess.ResizePTY(cols, rows)` and resize any active `block.term` (`block.term.Resize(cols, rows)`), invalidating `block.cachePopulated` so the next render takes a fresh snapshot.
   - In `renderBlockCached`, use `block.term.Snapshot()` for `UsesPTY` blocks (catching up any unpumped `block.Output` bytes via `block.termOffset` for test-constructed blocks).
4. Wire `ManagedPTY` in `pty_manager.go` to maintain a `terminal.Terminal` instance alongside `rawBuf`.

### Step 4: Shell & PTY Regression Tests (`kabibi/ui/shell/shell_ai_test.go`, `pty_interactive_test.go`)
1. Test that `WindowSizeEvent{Width: 100, Height: 30}` configures `ExecutionSession.PTYSize()` to `(100, 29)` (`width × (height - 1)`), resizes the active OS PTY to `(100, 29)`, and resizes the active block's `terminal.Terminal` backend to `(100, 29)`.
2. Test that streaming chunks into an active PTY block across a mid-stream `WindowSizeEvent` preserves prior terminal state in `block.term` and updates the active cursor position and trimmed height accurately.
3. Run `gofmt` and validate `go test ./ui/terminal ./ui/shell`.
