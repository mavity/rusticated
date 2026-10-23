# 8. Stateful Terminal Rendering Backend & Synchronized PTY Framing

## 1. Overview & Motivation

Currently, `terminal.FromANSI(raw string, width, height int) (CellBuf, CursorPos)` is a stateless, one-shot function: on every call it constructs a fresh `vt.Emulator`, feeds the entire accumulated byte stream from the beginning, and extracts a `CellBuf` and `CursorPos`. Foreground commands, including in-process built-ins, need a persistent terminal state so output, input, and resize behavior are not limited to external child processes.

While stateless replay works well for static text or immutable historical blocks, it has three fundamental flaws when applied to live interactive commands:

1. **Historical Reflow Corruption on Resize:**
   - When a command runs at dimensions `(W1, H1)` and the window is later resized to `(W2, H2)`, replaying the entire historical byte transcript from byte 0 into a fresh `(W2, H2)` emulator misinterprets cursor movements (`\x1b[H`, `\x1b[A`, `\x1b[2J`, relative jumps) that were calculated for `(W1, H1)`.
2. **O(N) Reparsing per Frame:**
   - As an interactive command emits output over time, re-feeding the entire `block.Output` buffer on every paint cycle scales linearly with total transcript size rather than incremental chunk size.
3. **Arbitrary 24-Row Framing Mismatch:**
   - Foreground commands were previously spawned or replayed with an arbitrary 24-row height or mismatched viewport height. While a command runs in Kabibi's shell stack, at least 1 reserved row of shell chrome (`reservedFooterRows = 1`) remains, so its terminal frame is `width × max(height - 1, 1)`. AI `exec` uses its separate compact frame policy below.

### Architectural Solution

1. **Introduce a stateful `terminal.Terminal` abstraction** in `kabibi/ui/terminal` that owns a persistent `vt.Emulator`, initializes it with LNM enabled, writes output bytes unchanged, supports live `Resize(width, height)` without replaying past bytes, and produces `(CellBuf, CursorPos)` snapshots on demand.
2. **Keep `FromANSI(raw string, width, height int)`** as a thin convenience wrapper over `terminal.Terminal` (`NewTerminal` -> `WriteString` -> `Snapshot`). It accepts one raw string and does not rewrite or normalize its bytes.
3. **Give every foreground command one logical command-terminal capability**, whether it is a built-in, shell/script command, or external executable. The Shell owns input routing, dimensions, terminal emulation, rendering, and plume policy. Built-ins use the Go-level endpoint; native child processes require an OS-PTY adapter because they consume OS file descriptors, not Go interfaces. Both adapters feed the same active-command rendering state.
4. **Use one live-output rendering pipeline for foreground shell commands and AI `exec` output**, with per-source framing and input policies. Worker output is handed to UI-owned terminal state through a coalescing pending-output buffer.
5. **Synchronize resizes across the logical command terminal, any attached OS PTY, and the rendering backend** while preserving already-rendered terminal state.
6. **Separate terminal paging from Git history traversal.** A pager can wait for command-terminal input, but prompt first-page output from `git log` also requires an incremental history walk that preserves Git's ordering semantics.

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
- **Foreground executable command profile:** frame the logical command terminal to `cols = width`, `rows = max(height - 1, 1)`. When a native child has terminal-bound streams, its OS-PTY adapter uses that same frame; streams redirected or piped by the shell retain their effective endpoints and must not become TTYs as a side effect.
- **AI `exec` profile:** use the same live-output rendering pipeline with a different frame: `cols = max(width - 2, 1)`, `rows = min(max(height - 1, 1), 20)`. The two-column inset and 20-row cap keep tool output visually compact. A direct executable child receives stdin from its PTY; `term_input` remains the explicit AI-tool input path, while shell UI keystrokes are not routed to that background PTY. Do not separately inherit the host shell's stdin. In-process shell/script execution retains the `ExecutionSession` runner's stdin behavior (currently EOF); do not add a second input stream.
- The foreground profile ensures:
  - Full-screen or cursor-positioning child processes (such as `node`, `python`, `vim`, `htop`, or REPLs) target a screen height that fits inside the shell stack without immediately forcing their own prompt row off-screen.
- The AI `exec` profile uses the same dimensions for its child PTY and UI terminal backend so the process and rendering geometry remain aligned.

### 2.5 Command-Terminal Contract and I/O Context

The command terminal is a logical capability owned by the active `ExecutionSession` and presented by the Shell. It is not an OS PTY and does not grant a command control of the Shell widget or host terminal. It provides:

- The command's effective stdin, stdout, and stderr, which retain normal shell pipeline and redirection behavior.
- Per-stream TTY status and current terminal dimensions when a stream is connected to the interactive Shell.
- A controlling-terminal input path for foreground interactive commands that need keys independently of stdin. This is necessary for cases such as `producer | more`, where pager input is a pipe but paging keys still come from the Shell.
- Resize and close/cancellation notification for the active foreground terminal session.

TTY status is determined from the command's effective I/O after the shell has applied pipes and redirections, not merely from command source or whether a command appears inside a script:

- A direct foreground command whose relevant standard stream is connected to the Shell may use that stream as a TTY.
- A stream redirected to a file or connected to a pipeline is not a TTY. For example, `git log | grep x` must not page its output.
- A command reading piped stdin but writing to the Shell may use the controlling terminal for interactive keys if its behavior permits it; ordinary stdin remains the pipe.
- Nested scripts and shell invocations inherit the effective stream capabilities they receive. Script nesting alone neither grants nor removes TTY status.
- Headless execution, AI `exec`, and commands without an attached interactive Shell have no controlling-terminal input. AI `exec` retains its existing explicit `term_input` control path.

Built-ins access this capability through the execution context; they do not receive a `ui.Widget` and do not render directly. Their ordinary output continues through the shell's stdout writer. A pager or other interactive built-in may use the controlling-terminal input path and dimensions while Shell remains responsible for drawing the active command's terminal state.

Native child processes cannot consume the Go-level endpoint directly. When a foreground external executable requires terminal semantics, an OS PTY adapter supplies its file descriptors and receives its input and resize operations. The adapter is attached to the same logical foreground session and its output is fed into the same UI-owned terminal state as built-in output. No OS PTY is allocated for built-ins merely to provide the Go-level capability.

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

## 4. Command Terminal, Child Process Output & Resize Architecture in `kabibi/ui/shell`

### 4.1 Per-Source PTY Frame Policies

Define helpers in `kabibi/ui/shell` for computing each source's terminal-frame dimensions from the shell's outer dimensions `(shellW, shellH)`:

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
- When `Shell` is initialized (`New`), initialize the logical foreground command terminal with `childPTYFrameSize(s.width, s.height)` (`80 × 23` by default). Use the current viewport dimensions for AI `exec`.
- When `Shell.HandleEvent` receives `input.WindowSizeEvent{Width, Height}`, or `Shell.Render` observes that the viewport dimensions differ from the dimensions last applied, use the event or `rect` dimensions as `(shellW, shellH)`:
  - Compute the foreground frame with `childPTYFrameSize(shellW, shellH)`, resize the active logical command terminal and any attached foreground OS PTY, and store the frame for future commands.
  - Compute the AI frame with `aiExecPTYFrameSize(shellW, shellH)` and resize every live AI `exec` PTY in `bgProcs` and its shared UI terminal to that frame, whether or not it currently has a view in the visible stack.

### 4.2 Shared Live-Output Rendering Backend

Use the same UI-owned live-output state for every active foreground command and AI `exec` output, with a per-source frame policy. Foreground built-ins and shell/script commands use the logical command terminal directly. For native executables, attach OS-PTY descriptors only to streams the logical contract marks as terminal-bound; retain effective pipe/redirection endpoints for non-TTY streams. Adapter output and resize state connect to the same logical session. Piped or redirected command streams do not become interactive merely because the Shell has a TUI. Keep process execution, input routing, and PTY lifecycle specific to each source. A `CommandBlock` is a presentation occurrence; its pointer-backed live-output state owns the pending bytes, transcript, terminal, and any frozen terminal snapshot. Foreground commands have one occurrence. All occurrences for one AI-managed process share one state keyed by `ManagedPTY.ID`.

1. **Worker-to-UI output handoff:**
   - The output callback copies incoming bytes into the live-output state's pending buffer under its short mutex, unlocks, then requests invalidation. It does not append to the transcript or call `Terminal.Write` from the worker goroutine.
   - In `Measure` and `Render`, the UI goroutine drains each live-output state once per batch, appends bytes to its raw transcript, initializes its terminal if needed, and writes the unchanged bytes to it. Recycle the drained buffer as a spare when possible.
   - Every active foreground command uses its stateful terminal as the incremental rendering backend, regardless of whether it is a built-in or an external process. Historical, completed blocks without live terminal state may continue to use the stateless renderer or a retained frozen snapshot.
2. **Process and view lifecycle:**
   - Initialize the live-output state when a foreground command or AI `exec` process begins, using the source's configured frame policy. The state survives command completion long enough to freeze the final display and, for AI `exec`, timeout promotion into `bgProcs`.
   - Keep the initial AI `exec` block at its original position in the append-only stack. It continues to reflect output while the process runs; if its rows are ejected, they continue into scrollback/Plume.
   - When a later AI `term_read` probes a process that has been promoted to the background, append a new `CommandBlock` view at the current stack position, linked to the same `ManagedPTY.ID` and live-output state. Do not restart or move the process, and do not remove the original view. Repeated probes update the reactivated view rather than appending one block per poll.
   - Each view snapshots the shared terminal state using its source's frame geometry. Completion updates the shared status; all views remain in their respective stack positions and show the final state without ingesting or ejecting the same output more than once.
3. **Terminal rendering and resize:**
   - The foreground profile uses `(width, max(height - 1, 1))`; the AI `exec` profile uses `(max(width - 2, 1), min(max(height - 1, 1), 20))`.
   - Track how many transcript bytes have been fed into the terminal with a `termOffset` in the shared live-output state. Normal streaming advances it during the UI drain; `renderBlockCached` catches up synthetic or otherwise unpumped test transcripts.
   - On `WindowSizeEvent` or a detected initial viewport-size change, resize the active foreground PTY and every live AI `ManagedPTY` OS PTY and live-output terminal together using their respective frame policies. This includes backgrounded processes without a reactivated view; do not resize exited processes. Terminal resize occurs on the UI goroutine; invalidate affected view caches.
   - In `renderBlockCached`, snapshot the shared terminal for active command views, hide the cursor after process completion, trim trailing whitespace rows, then blit the prompt/input prefix and terminal output together, adjusting `outputCursor.Y` by the prefix height.
   - Drain all queued bytes before a completed process's final snapshot. Retain the terminal state; do not reparse its transcript at a different size.

4. **Screen and plume policy:**
   - Treat the active emulator screen and its scrollback as terminal state, not as a growing transcript to replay. The UI consumes each newly scrolled-off main-screen row at most once; redraws and snapshots must never duplicate plume output.
   - Detect and preserve alternate-screen transitions if supported by the pinned `vt` emulator. While the alternate screen is active, its transient rows must not be ejected into Shell plume history. Leaving alternate screen restores the main-screen state.
   - On command completion, freeze the final terminal state for the command block. Normal main-screen output and its terminal scrollback remain available as command output; alternate-screen contents are discarded when the command leaves that screen, matching terminal application behavior. Keep the raw output transcript separately for command history/tool inspection, but do not treat raw escape sequences as rendered plume text.
   - Verify the pinned emulator's alternate-screen behavior before relying on it. If it cannot distinguish active alternate-screen state from main-screen scrollback, add the smallest required adapter support or defer full-screen applications; do not silently route their redraws into plume.

### 4.3 AI `exec` PTY Lifecycle (`kabibi/ui/shell/pty_manager.go`, `exec_session.go`)

The AI `exec` tool is similar to a foreground command for live VT output, but it uses the AI `exec` frame and input routing defined above. Reuse the live-output rendering backend for its `SourceAITool` UI block; do not route shell UI keystrokes to its background PTY.

1. Configure AI `exec` PTYs with `cols = max(width - 2, 1)` and `rows = min(max(height - 1, 1), 20)`. Keep these dimensions in sync with the AI `exec` block's UI terminal when the viewport changes.
2. Direct executable children receive stdin through their PTY, and `term_input` remains available to the AI tool. Shell UI keystrokes continue to target only the foreground user-shell PTY. Do not add a separate inherited host-stdin path. In-process shell/script commands retain the runner's configured stdin behavior.
3. `ManagedPTY` remains the owner of raw output for the AI tool read API. Its `appendOutput` updates `rawBuf` under `m.mu`, then invokes the UI callback outside that lock. The callback enqueues bytes into the process's shared live-output state. Keep `ManagedPTY.Snapshot(onlyNew, keepANSI)` as a raw-output API.
4. Register the `ManagedPTY` and obtain its stable ID before creating its initial UI view. Associate the view and shared live-output state with that ID.
5. When `term_read` probes a background process with a UI sink, request a reactivated view in the current stack. Reuse the same process state and keep the initial view in place; later probes update the reactivated view.
6. Route viewport changes to every live AI `ManagedPTY` and its shared UI terminal using `aiExecPTYFrameSize`, regardless of whether the process currently has a visible or reactivated view. Do not resize exited processes.

### 4.4 Built-In Paging and Incremental `git log`

Paging is a consumer of the command-terminal contract, not a separate widget or alternate rendering path:

- A pager reads its content from the command's normal input/output streams and obtains paging keys from the controlling-terminal input path. It uses the current terminal frame and never takes over Shell rendering or plume ownership.
- Page only when the effective output is a TTY and a controlling terminal is available. `--no-pager`, `-P`, and pager configuration that explicitly requests pass-through must disable paging. With redirected output, pipelines, headless execution, and AI tools, emit the complete stream without waiting for keys.
- `producer | more` is the important split-stream case: `more` consumes the pipe as stdin, renders to the Shell if stdout is attached to it, and reads keys from the controlling terminal. Conversely, `git log | grep x` must not page `git log`'s piped output.
- Use the active terminal frame to decide when to pause. Handle page/line advance, backward page movement where buffered content permits it, quit, and cancellation. Do not encode pager prompts as permanent transcript lines; redraw them in the active terminal screen.
- A pager fixes excessive output and interaction, but not delayed first output if its producer buffers all content. The ordinary `git log` path therefore needs an incremental commit-and-format path that preserves the current output order and formatting.
- Preserve the existing implementation for log modes whose ordering or filtering requires a complete walk, including modes such as `--topo-order` or `--reverse`, unless an equivalent incremental algorithm is proven. Paging may still limit what the user sees, but these modes are not considered first-page-streaming.
- Before changing the log implementation, compare the incremental path with the current engine for default order and supported common format, limit, path, and revision-range options. Unsupported or non-streamable combinations must continue through the existing engine rather than silently changing results.

---

## 5. Step-by-Step Implementation Plan

Implementation proceeds in dependency order. Each step has a gate that must pass before dependent behavior is built on top of it. Paging and Git streaming are deliberately last: they depend on correct terminal input, output, and frame semantics.

### Step 1: Stateful terminal backend (`kabibi/ui/terminal/cell.go`)

- Implement `terminal.Terminal` with construction, incremental byte/string writes, resize, size query, and `(CellBuf, CursorPos)` snapshots. Keep it single-owner; do not add internal goroutine synchronization.
- Initialize LNM as specified and feed bytes unchanged. Keep `FromANSI` as a convenience wrapper over the stateful implementation.
- Verify the pinned VT emulator's resize preservation, cursor visibility, alternate-screen switching, and scrollback behavior. Resolve limitations before integrating plume handling; do not claim unsupported behavior.
- Update `kabibi/ui/terminal/CELL.md` to describe the stateful API, ownership, LNM, resize, and alternate-screen guarantees actually verified.

**Gate:** tests establish chunk-boundary equivalence for ANSI and UTF-8, expected LF/CRLF behavior, state preservation across resize, cursor visibility, and isolation of alternate-screen content from main-screen scrollback.

### Step 2: Terminal backend tests (`kabibi/ui/terminal/cell_test.go`)

- Cover split CSI/OSC sequences, split multi-byte graphemes, CRLF boundaries, bare LF under initial LNM, and an in-band LNM reset.
- Resize after writes and cursor movement; verify prior cells remain valid and subsequent writes use the new dimensions without replay.
- Exercise main/alternate-screen entry and exit, scrollback growth, and cursor snapshots at each transition.

**Gate:** `go test ./ui/terminal` passes and existing `FromANSI` behavior is preserved except for the documented initial LNM semantics.

### Step 3: Logical command-terminal contract (`kabibi/ui/shell/exec_session.go`, `shell.go`, `uroot.go`)

- Add a command-scoped logical terminal capability owned by `ExecutionSession`. It exposes the current frame, per-stream TTY status, controlling-terminal input for interactive foreground use, and cancellation/close behavior. Keep this as a contract at this stage; choose concrete Go interfaces to fit the existing runner and command wiring rather than adding a parallel abstraction.
- Bind it only while an interactive foreground command executes with an attached Shell. Without that attachment, a command gets no controlling-terminal input and retains EOF/non-interactive behavior.
- Determine stdin, stdout, and stderr TTY status independently from each command's effective streams after pipelines and redirections are applied. Do not infer TTY from `CommandSource`, command name, or script nesting.
- Keep controlling-terminal input distinct from stdin. For example, `more` in `producer | more` reads content from its pipe and keys from the controlling terminal.
- Propagate the logical terminal context through nested shell/script runners. Effective streams govern terminal status; script nesting alone does not disable terminal access.
- Keep AI `exec` outside foreground key routing. Its direct child continues using its managed PTY and `term_input`; in-process AI shell commands retain their current runner stdin behavior.
- Do not pass a `ui.Widget` to commands or let commands write to the host terminal. Shell remains the sole owner of event routing, rendering, and plume policy.

**Gate:** tests cover interactive foreground, headless execution, independently redirected streams, `git log | grep x`, `producer | more`, and interactive/piped nested scripts. Confirm both pointer and value forms of shell events where applicable.

### Step 4: Foreground live-output integration and native-child adapter (`kabibi/ui/shell/shell.go`, `shell_widget.go`, `exec_session.go`)

- Apply the foreground frame policy to the logical terminal at initialization and on each effective viewport resize. Keep the default frame at `80 × 23`; clamp dimensions to at least one cell.
- Give each active foreground command whose output reaches the Shell a pointer-backed live-output state containing pending bytes, raw transcript, terminal, terminal offset, and finalized snapshot. This applies to built-ins, scripts, and external commands, not only commands with an OS PTY.
- Output workers only append bytes to the pending buffer under a short lock, release it, then request invalidation. Drain ordered batches, append them to the transcript, and feed the terminal only on the UI goroutine. Resize and snapshot terminal state only on that owner.
- Use an OS-PTY adapter for native external executable streams that the logical contract marks as terminal-bound. Route non-TTY streams to their effective pipe/redirection endpoints; attaching the adapter must not change their TTY status. The adapter receives terminal input and resize through the active logical session, and its terminal-bound output enters the same command live-output state. Do not allocate OS PTYs for in-process built-ins.
- Preserve normal pipeline, redirection, and headless behavior. Bytes not directed to the Shell terminal must not be injected into its VT state.
- Use stateful rendering for the active foreground command. Historical blocks may use a frozen snapshot or the existing stateless renderer; do not reparse the active transcript on every redraw.

**Gate:** built-in and child output share resize/render behavior; child key input and resize work; redirected/piped output stays out of the Shell terminal; chunks preserve order; completion drains pending bytes before freezing; mid-command resize preserves terminal state.

### Step 5: Shell composition, scrollback, and completion (`kabibi/ui/shell/shell_widget.go`, `ui/terminal`)

- Feed only newly accumulated main-screen scrollback rows into Shell plume, exactly once. Re-rendering or snapshotting must not re-eject rows.
- Keep alternate-screen redraws out of plume and restore main-screen state on exit from alternate screen.
- On completion, freeze terminal state for the command block and hide its live cursor. Preserve raw output separately for command history and tool inspection.
- Define and test visible behavior for completed main-screen and alternate-screen commands. Never turn raw escape sequences or discarded alternate-screen frames into plume text.
- Fit the active command frame to the Shell's actual `rect.H` after reserved chrome. Geometry helpers use outer Shell dimensions, not an assumed fixed terminal height.

**Gate:** repeated render/snapshot cycles do not duplicate plume rows; alternate-screen redraw-and-exit does not pollute history; normal scrolled output remains available after exit; the frozen result remains stable after later resizes.

### Step 6: AI `exec` state and framing (`kabibi/ui/shell/pty_manager.go`, `exec_session.go`, `tools_exec.go`)

- Keep the AI `exec` frame policy at `max(width - 2, 1)` columns and `min(max(height - 1, 1), 20)` rows. Resize every live managed PTY and shared UI terminal, including background processes with no current view; do not resize exited processes.
- Preserve direct-child stdin through the managed PTY and `term_input` as the AI input path. Never route Shell key events to background AI processes or separately inherit host stdin.
- Keep `ManagedPTY.Snapshot(onlyNew, keepANSI)` as a raw-output API. Invoke callbacks outside `ManagedPTY.mu`; callbacks enqueue to shared live-output state and do not mutate its emulator.
- Register a managed process before creating its initial UI view. A later `term_read` may add or update a linked view of the same state; it must not restart the process, re-ingest bytes, or remove the initial view.

**Gate:** AI frame dimensions, background resize, input isolation, raw snapshot semantics, promotion, and reactivated-view sharing pass regression tests.

### Step 7: Built-in pager and incremental Git log (`kabibi/ui/shell`, Git implementation)

- Implement paging as an in-process consumer of the command-terminal contract. It writes through normal command output, gets keys only from controlling-terminal input, and leaves layout and plume ownership with Shell.
- Support page/line advance, backward movement over buffered pages, quit, and cancellation. Keep the prompt transient in terminal screen state and derive page height from the active frame.
- Bypass paging when the pager's effective stdout is non-TTY, in headless/AI execution, for redirected pager output, and when pager-disable settings apply. A pipeline is not by itself a bypass: `producer | more` pages when `more` writes to the Shell and uses the pipe only for stdin. `git log | grep` does not page because `git log`'s stdout is piped.
- Add incremental commit traversal/formatting only where current ordering and formatting can be preserved. Prove first-page availability before traversal reaches its end with a measurable test.
- Compare supported common options against the existing engine. Modes requiring complete ordering, including `--topo-order` and `--reverse` unless equivalent streaming is proven, remain on the existing path. Unsupported combinations must fall back without silently changing results.

**Gate:** interactive logs pause at a page boundary and respond to keys; short logs finish without pausing; `q` stops traversal where streaming permits; non-TTY output completes without waiting; supported output matches the existing implementation.

### Step 8: Final validation

- Run `gofmt` on changed Go files and `go test ./ui/terminal ./ui/shell`.
- Validate supported behavior on Windows, POSIX, and `wasip1` where applicable. The logical built-in endpoint must not require host-only syscalls; OS-PTY child behavior must be gated by platform capability.
- Run focused tests for resize, key routing, stream-specific TTY status, pipes, redirection, scripts, plume, teardown, and paging. Document unsupported VT features or platform limitations rather than counting them as passing behavior.
