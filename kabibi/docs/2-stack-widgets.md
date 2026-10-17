# Kabibi Dynamic Stack Widgets, Incremental Inline Diffing & Scrollback Ejection Plan

## 1. Architectural Vision & Core Invariants

Kabibi's shell is an atmospheric, dynamically rendered **Stack Widget** that starts humbly inline where the user invoked it, grows downward as commands, animated artifacts, and wrapped input lines expand, and seamlessly ejects top rows into the terminal emulator's native scrollback only once the managed stack fills the available terminal rows.

### Non-Negotiable Design Rules

1. **Default `፧` Prompt Separator:**
   - The canonical default prompt template is `\w፧` (fallback `፧`), never `$ `.
   - All default prompt initializers (`Shell.New`, `PromptInputOptions`, `FormatPrompt`, and prompt unit/snapshot tests) use `፧`.
2. **Initial 3-Part Humble Stack (No Startup Screen Flooding):**
   - On startup, Kabibi does **not** switch to the alternate screen (`\x1b[?1049h`), does **not** emit 23+ padding newlines, and does **not** jump to absolute screen coordinates (`\x1b[row;colH`).
   - Instead, the initial stack consists of exactly three vertical regions:
     1. **`BlockBanner`** ($H_{\text{banner}}$ rows): Pompous retro diagnostic banner block at the top of the stack.
     2. **`PromptInput`** ($H_{\text{input}} \ge 1$ rows): The active prompt and command-line editor (`~/path፧`).
     3. **Reserved Blank Footer Row** ($H_{\text{footer}} = 1$ row): One blank line immediately below the input widget (reserved for status/keybar/transient telemetry later; blank for now).
   - At startup with a 1-row input, the total occupied stack height is $H_{\text{occ}} = H_{\text{banner}} + 1 + 1$.
3. **Incremental, Difference-Driven Inline Redraws:**
   - Every frame—whether triggered by a single typed character, cursor movement, an animation tick, or a streaming PTY/AI chunk—is diffed against `prevBuf` (`CellBuf` cell comparison).
   - `ui.Host` emits ANSI sequences **only for the dirty rows** between `lineMin` and `lineMax` (or a pure relative cursor move if only the cursor changed), navigating strictly via relative vertical cursor motion (`\x1b[nA` / `\x1b[nB` / `\r`) from the previous cursor row `prevCursorY`.
4. **The Growing Stack & Peak-Allocated Footprint ($H_{\text{alloc}}$):**
   - Terminal emulators only scroll the screen upward when a newline (`\r\n`) is emitted at the bottom of the allocated inline region.
   - If the user types a long command that wraps `PromptInput` from 1 row to 3 rows, the stack's allocated terminal footprint grows to $H_{\text{alloc}} = \max(H_{\text{alloc}}, H_{\text{occ}})$ (capped at terminal height $H_{\text{term}}$).
   - If the user then deletes that text so `PromptInput` shrinks back to 1 row, the currently occupied stack height $H_{\text{occ}}$ decreases, opening a **temporary slack gap** of $G = H_{\text{alloc}} - H_{\text{occ}}$ blank rows at the bottom of the allocated footprint:
     - Those $G$ slack rows are cleared in-place (`\r\x1b[2K`), and the hardware cursor stays on the `PromptInput` row.
     - **Gap-First Expansion (Push Down Before Ejecting Up):** When subsequent output (or input growth) increases $H_{\text{occ}}$, the stack **first expands downward into the $G$ slack rows** (consuming the gap at the bottom with zero scrollback ejection).
     - Only after $G = 0$ and $H_{\text{occ}}$ exceeds $H_{\text{term}}$ do overflowing rows at the top of the stack eject into native terminal scrollback.
5. **Animated Artifacts Live in the Managed Stack, Then Settle & Eject:**
   - Rich animated artifacts—AI deliberation tickers (`[goal]…`, `[steps]…`), tool progress spinners, progress bars, and live PTY cursor/line updates—render inside the active `CommandBlock` within the managed stack.
   - Because the active block lives in the diffed `CellBuf` viewport, animations update smoothly at 16–30 FPS via single-row dirty diffs without polluting native scrollback with intermediate frames.
   - As output grows past the viewport budget, or when an animated artifact completes and settles into static output that later scrolls off the top, the settled top rows are ejected once into native scrollback.

---

## 2. Stack Geometry & The Slack-Gap State Machine

### 2.1 Vertical Stack Anatomy

At any frame, the non-ejected visual content of the shell is composed top-to-bottom as:

```
┌──────────────────────────────────────────────────────────┐  ▲
│ [Ejected History in Native Terminal Scrollback]          │  │ Owned by terminal emulator
├══════════════════════════════════════════════════════════┤  ▼ (previouslyEjected rows)
│ Block 0: BlockBanner (pompous retro startup diagnostics) │  ▲
│ Block 1: BlockCommand ("~/rustica፧ ls")                  │  │
│          output rows...                                  │  │ Non-ejected Block Slice
│ Block k: Active BlockCommand / BlockAITurn               │  │ (H_blocks_vis rows)
│          live PTY stream / animated spinner / ticker     │  │
├──────────────────────────────────────────────────────────┤  │ Occupied Stack (H_occ)
│ PromptInput ("~/rustica፧ echo hello...")                 │  │ (H_input >= 1 rows)
├──────────────────────────────────────────────────────────┤  │
│ Reserved Footer Line (blank 1 row for now)               │  ▼ (H_footer = 1 row)
├ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─┤  ▲
│ Slack Gap (blank rows opened when H_occ shrank)          │  │ Slack Gap G = H_alloc - H_occ
└ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─┘  ▼
```

### 2.2 Exact Height Accounting

Let:
- $W, H_{\text{term}}$ be the current terminal width and height.
- $H_{\text{footer}} = 1$ (the mandatory blank line below `PromptInput`).
- $H_{\text{input}} = \text{clamp}(\text{PromptInput.Measure}(W, H_{\text{term}}).H, 1, H_{\text{term}} - H_{\text{footer}})$.
- $H_{\text{chrome}} = H_{\text{input}} + H_{\text{footer}}$ be the bottom-pinned interactive chrome height.
- $H_{\text{budget}} = \max(0, H_{\text{term}} - H_{\text{chrome}})$ be the maximum number of block rows that can fit on screen alongside the prompt and footer.

Let $H_{\text{blocks\_total}}$ be the total visual rows of all `s.blocks[0..N-1]` at width $W$, and `previouslyEjected` be the monotonic watermark of visual block rows already flushed to native terminal scrollback:
- Remaining non-ejected block rows before this frame's check:
  $$H_{\text{unedjected}} = H_{\text{blocks\_total}} - \text{previouslyEjected}$$
- Desired occupied height if nothing new is ejected:
  $$H_{\text{desired}} = H_{\text{unedjected}} + H_{\text{chrome}}$$

### 2.3 Closing the Slack Gap First vs. Ejecting Upward

`ui.Host` tracks `allocHeight` ($H_{\text{alloc}}$), the peak number of terminal lines already opened inline below the current stack origin (where $0 \le H_{\text{alloc}} \le H_{\text{term}}$):

1. **Case A — Stack Fits Within Current Allocated Footprint ($H_{\text{desired}} \le H_{\text{alloc}}$):**
   - If the user previously wrapped input to 4 rows ($H_{\text{alloc}} = H_{\text{banner}} + 4 + 1$) and deleted it back to 1 row ($H_{\text{occ}} = H_{\text{banner}} + 1 + 1$), then $G = 3$ slack rows exist below the footer.
   - When a command runs and produces 1, 2, or 3 rows of output, $H_{\text{desired}}$ grows from $H_{\text{alloc}} - 3$ up to $H_{\text{alloc}}$.
   - **No rows are ejected to scrollback.** The stack simply pushes `PromptInput` and the blank footer row downward into the existing slack rows $G$, closing the gap first.
   - When $H_{\text{desired}} < H_{\text{alloc}}$, the buffer rendered by `Host` has height $H_{\text{alloc}}$ with the bottom $G = H_{\text{alloc}} - H_{\text{desired}}$ rows filled with blank cells so any stale cells from the taller state are diffed to spaces and cleared incrementally.

2. **Case B — Stack Grows Beyond `H_alloc` But Still Fits Within Terminal ($H_{\text{alloc}} < H_{\text{desired}} \le H_{\text{term}}$):**
   - All slack rows are already closed ($G = 0$).
   - **No rows are ejected to scrollback.**
   - `Host` advances $H_{\text{alloc}} \leftarrow H_{\text{desired}}$, emitting `\r\n` only when stepping onto the newly claimed bottom rows $(H_{\text{alloc\_old}} \dots H_{\text{desired}}-1)$.

3. **Case C — Stack Exceeds Terminal Height ($H_{\text{desired}} > H_{\text{term}}$):**
   - All slack rows and free terminal rows are exhausted ($H_{\text{alloc}} = H_{\text{term}}, G = 0$).
   - The overflow $\Delta_{\text{eject}} = H_{\text{desired}} - H_{\text{term}} = H_{\text{unedjected}} - H_{\text{budget}}$ must be ejected from the top of the non-ejected block slice:
     $$\text{targetEjected} = \text{previouslyEjected} + \Delta_{\text{eject}} = H_{\text{blocks\_total}} - H_{\text{budget}}$$
   - The slice of block visual rows $[\text{previouslyEjected} : \text{targetEjected}]$ is serialized to ANSI and emitted above the active stack, advancing the terminal's native scrollback by $\Delta_{\text{eject}}$ lines, and `previouslyEjected` is updated to `targetEjected`.

### 2.4 Incremental Inline Diffing in `ui.Host`

To guarantee we **never** do unnecessary full-screen redraws on every keystroke or animation frame:

1. **Relative Origin Anchoring:**
   - `Host` maintains `prevCursorY` (0-indexed row offset from the top of the active inline region) and `allocHeight` ($H_{\text{alloc}}$).
   - Moving from `prevCursorY` to any dirty row $y$ requires only relative vertical motion:
     - If $y < \text{curY}$: emit `\x1b[%dA` ($\text{curY} - y$).
     - If $y > \text{curY}$:
       - For rows $< H_{\text{alloc\_prev}}$, emit `\x1b[%dB` ($y - \text{curY}$) so existing lines are never scrolled accidentally.
       - For newly allocated rows $\ge H_{\text{alloc\_prev}}$ (when the stack grows into virgin terminal lines), step down with `\r\n` so the terminal scrolls naturally if the cursor was near the physical bottom of the window, and update `allocHeight`.
2. **Per-Row Dirty Detection (`currBuf` vs `prevBuf`):**
   - Rather than redrawing all $0 \dots H_{\text{occ}}-1$ rows whenever any cell changes, `Host.renderInlineDiff` compares `currBuf` and `prevBuf` **row by row**:
     - If row $y$ is identical in `currBuf` and `prevBuf`, it is **skipped completely** (zero bytes emitted for that row).
     - If row $y$ differs, the cursor moves relatively to row $y$, emits `\r`, renders row $y$ via `terminal.ToANSI(rowSubBuf)` (which already appends `\x1b[K` when trailing spaces exist), and updates `curY = y`.
   - **Result:**
     - Typing a character in `PromptInput` only touches the single dirty row of `PromptInput` and repositions the cursor.
     - An animated spinner or AI `[steps]` ticker only touches the 1 dirty row where the animation sits.
     - Shrinking `PromptInput` from 3 rows to 1 row only touches the changed input row, the shifted blank footer row, and blanks the 2 newly opened slack rows (`prevBuf` had text there; `currBuf` has blank cells there; subsequent frames skip those slack rows because `currBuf == prevBuf`).

---

## 3. Five-Phase Implementation Plan

### Phase 1: Pompous Retro Banner, `፧` Prompt & Humble 3-Part Startup

#### What We Have
- `bannerFlushed` and initial welcome block in `kabibi/ui/shell/shell_widget.go`.
- `FormatPrompt` in `kabibi/ui/shell/prompt.go` already defaults empty `ps1` to `\w፧`, and `Shell.currentPromptLocked()` uses `\w፧` when `s.execSess != nil`.
- However, `Shell.New` still initializes `prompt: "$ "`, `PromptInput` defaults to `"$ "`, and several tests in `prompt_test.go` and `app_tui_e2e_test.go` still assert `$ `.

#### Exact Changes
1. **Unify Default Prompt Separator to `፧` (`kabibi/ui/shell/prompt.go`, `prompt_input.go`, `shell_widget.go`):**
   - Set default prompt fallback in `Shell.New` and `NewPromptInput` to `"፧"` (and `\w፧` when `ExecutionSession` is active).
   - Update `ExpandPS1` so `\$` remains standard bash escape (`$`/`#` when explicitly requested in custom `PS1`), while Kabibi's default unset `PS1` template is `\w፧`.
2. **Create `BlockBanner` (`kabibi/ui/shell/shell.go`, `shell_widget.go`):**
   - Introduce `BlockBanner` as the first block in `s.blocks` on startup.
   - Format the banner with atmospheric, pompous retro computing diagnostics—e.g., system banner header, build/runtime architecture (`GOOS/GOARCH`, Rusticated WASM / Native Supervisor), memory & cell-matrix status, copyright/rom-style cue, and the ready line—rendered with crisp ANSI styling through `terminal.FromANSI`.
3. **Humble Inline Startup (`kabibi/ui/app/app.go`, `boot.go`, `ui/host.go`, `ui/shell/shell_widget.go`):**
   - Make the dynamic stack mode (`ModeStack`, replacing/unifying the default behavior) the default in `Shell.New`.
   - Ensure `App.Run()` / `Boot()` never calls `startupSequence()` (the old $H-1$ newline flood).
   - On frame 1, `Shell.Render` produces:
     - `BlockBanner` ($H_{\text{banner}}$ rows),
     - `PromptInput` (`~/path፧`, 1 row),
     - 1 blank reserved footer row below `PromptInput`.
   - Cursor is placed on the `PromptInput` row (at `Y = H_banner + inputCursor.Y`, `X = inputCursor.X`), leaving the blank footer row visibly underneath.

---

### Phase 2: Infinite History Log (Unified Block Store) & Animated Artifacts

#### What We Have
- `s.blocks []CommandBlock` in `kabibi/ui/shell/shell_widget.go`.
- `ExecutionSession.records []CommandRecord` in `kabibi/ui/shell/exec_session.go` (which truncates output at `maxHistoryRecordOutput = 32768`).
- Separate representations for UI blocks vs. AI-queryable `CommandRecord`s.

#### Exact Changes
1. **Unify Block Types & Metadata (`kabibi/ui/shell/shell.go`, `exec_session.go`, `shell_widget.go`):**
   - Define `BlockKind`:
     ```go
     type BlockKind string

     const (
         BlockBanner  BlockKind = "banner"
         BlockCommand BlockKind = "command"
         BlockAITurn  BlockKind = "ai_turn"
     )
     ```
   - Extend `CommandBlock` with:
     - `ID int` (monotonic 1-based block ID shared across the session),
     - `Kind BlockKind` (`BlockBanner`, `BlockCommand`, `BlockAITurn`),
     - `Source CommandSource` (`SourceUserShell`, `SourceAITool`, or `"system"`),
     - `Cwd string`,
     - `Prompt string`, `Input string`,
     - `Output bytes.Buffer` (full raw output stream),
     - `AnimLine string` / `AnimFrame int` (optional live animated artifact line while `Status == StatusActive`),
     - `Status CommandBlockStatus`, `ExitCode int`, `StartedAt time.Time`, `Duration time.Duration`,
     - Cached visual rendering (`cachedWidth int`, `cachedLen int`, `cachedAnim string`, `cachedBuf terminal.CellBuf`) so unchanged historical blocks are not re-parsed through `terminal.FromANSI` on every keystroke!
2. **Animated Artifacts inside Active Blocks:**
   - While a command or AI turn is `StatusActive`:
     - AI deliberation updates from `OnFeedback` (`\r[goal]…`, `\r[steps]…`, tool progress) and an active spinner/pulse frame update `AnimLine` on the active block (driven by a lightweight 80ms ticker goroutine that only calls `s.invalidate()` while an animation is active).
     - When `OnContent` streams or the command finishes, the transient animation line is cleared or replaced by the settled output in `block.Output`, so only clean permanent output remains in the block and gets ejected to scrollback.
3. **Full Output Retention & LLM Inspection Tools (`kabibi/ui/shell/tools_context.go`):**
   - Remove the destructive 32KB truncation on stored block history; retain full output in the unified block store (with optional temp-file spill if a single block exceeds a high memory threshold, e.g., 8MB).
   - Expose clean query methods on `ExecutionSession` / block store:
     - `shell_history`: lists recent blocks (`id`, `kind`, `source`, `command`, `cwd`, `exit_code`, `duration`, `line_count`, `byte_len`, and bounded preview or full output).
     - `read_command_output`: new tool allowing the AI to inspect full or sliced output of any block by `id` (`offset_lines`, `limit_lines`, `search` regex/substring) at any time.

---

### Phase 3: PTY Execution & Real-Time Output Collection

#### What We Have
- Root package `pty/` (`C:\Users\mihai\rustica\pty`) providing `pty.Start(cmd, opts...)` with Windows ConPTY (`pty_windows.go`), Unix `/dev/ptmx` (`pty_unix.go`), and Rusticated WASM ABI (`pty_rusticated.go`).
- `runResolvedCommand` in `kabibi/ui/shell/shell.go:304` currently runs external programs via `exec.CommandContext` with standard pipes.
- `ExecutionSession` currently initializes `interp.Runner` with `stdin = strings.NewReader("")` and `stdout = stderr = es.streamOut` (`*dynamicWriter`).

#### Exact Changes
1. **Detect Unredirected Interactive Standard I/O (`kabibi/ui/shell/exec_session.go`, `shell.go`):**
   - Give `ExecutionSession` a sentinel `ttyStdin` reader (`type shellTTYReader struct{}`) and pass `(es.ttyStdin, es.streamOut, es.streamOut)` to `createRunner`.
   - In `runResolvedCommand(ctx, hc, program, args)`:
     - Check whether the command is running in an interactive foreground user shell context and its stdio handles are unredirected:
       ```go
       isInteractiveTTY := isUserForeground(ctx) &&
           hc.Stdin == es.ttyStdin &&
           hc.Stdout == es.streamOut &&
           hc.Stderr == es.streamOut
       ```
     - If a command is part of a pipeline (`cmd1 | cmd2`), input/output redirection (`> file`, `< file`), command substitution `$(...)`, or invoked by `SourceAITool`, `hc.Stdin`/`hc.Stdout`/`hc.Stderr` will differ from the sentinel handles and will continue to use standard pipes seamlessly.
2. **Spawn via `pty.Start` & Stream to Active Block:**
   - When `isInteractiveTTY` is true, start the child with:
     ```go
     p, err := pty.Start(cmd, pty.Options{
         Cols: currentCols,
         Rows: currentRows,
         Term: "xterm-256color",
     })
     ```
   - If `pty.Start` returns `ENOSYS` or fails (e.g., in browser WASM without host child-process spawning), fall back gracefully to standard `cmd.Run()`.
   - Register `p` as `es.activePTY` for the duration of the command:
     - Stream bytes from `p.Read(buf)` directly into `hc.Stdout.Write(buf[:n])` (`es.streamOut`), which appends to the active `CommandBlock.Output` and triggers `s.invalidate()`.
     - Forward user keystrokes (`input.KeyPressEvent` encoded to terminal bytes / `input.PasteEvent`) from `Shell.HandleEvent` into `p.Write(...)` while `es.activePTY != nil`.
     - Forward terminal resize events (`input.WindowSizeEvent`) to `p.Resize(cols, rows)`.
     - On `p.Wait()` completion, capture exit code into `CommandBlock.ExitCode` and set `StatusSuccess` (0) or `StatusError` (non-zero).

---

### Phase 4: Bottom-Up Viewport Fitting, Caching & Rendering

#### The Core Calculation (`kabibi/ui/shell/shell_widget.go`)
Instead of re-rendering every block from scratch or slicing from the top:

1. **Per-Block Visual Row Cache:**
   - Each `CommandBlock` caches its rendered `terminal.CellBuf` for `(width, outputLen, animLine)`.
   - Completed blocks (`StatusSuccess`, `StatusError`, `StatusFailure`) only re-run `terminal.FromANSI` if the terminal `width` changes!
   - Only the currently active block (`StatusActive`) re-renders when new output chunks or animation ticks arrive.
2. **Pin Bottom Chrome:**
   - Measure `PromptInput`: $H_{\text{input}} \ge 1$.
   - Reserve the blank footer line below `PromptInput`: $H_{\text{footer}} = 1$.
   - Total bottom chrome: $H_{\text{chrome}} = H_{\text{input}} + 1$.
3. **Remaining Block Budget:**
   - $H_{\text{remaining}} = \max(0, H_{\text{viewport}} - H_{\text{chrome}})$.
4. **Backward Walk Across `s.blocks` ($i = N-1 \dots 0$):**
   - Walk from newest block to oldest block, stopping once we reach `previouslyEjected` or $H_{\text{remaining}} == 0$:
     - Compute (or fetch cached) visual row height $H_i$ of block $i$ at width $W$.
     - Exclude any top rows of block $i$ that have already been ejected (`previouslyEjected`).
     - If the block's non-ejected rows fit within $H_{\text{remaining}}$, include all of them and decrement $H_{\text{remaining}}$.
     - If the block only partially fits, slice its **tail** $H_{\text{remaining}}$ visual rows to fill the budget and stop.
5. **Exact Stack Height Assembly:**
   - Let $H_{\text{collected}}$ be the total visual rows collected from the backward walk ($0 \le H_{\text{collected}} \le H_{\text{remaining}}$).
   - Total occupied height of the stack widget this frame is:
     $$H_{\text{occ}} = H_{\text{collected}} + H_{\text{input}} + 1$$
   - `Shell.Render` allocates a `CellBuf` of height $H_{\text{occ}}$ (not a forced full-screen 24 rows when $H_{\text{occ}} < H_{\text{viewport}}$):
     - Rows $[0 \dots H_{\text{collected}}-1]$: painted with the collected block rows in chronological order.
     - Rows $[H_{\text{collected}} \dots H_{\text{collected}} + H_{\text{input}} - 1]$: painted with `PromptInput`.
     - Row $H_{\text{occ}} - 1$: painted as the blank reserved footer row.
   - Cursor is returned at `(X: inputCursor.X, Y: H_collected + inputCursor.Y, Visible: !running && inputCursor.Visible)`.

---

### Phase 5: Dynamic Growth, Slack-Gap Recovery & Natural Scrollback Ejection

#### End-to-End Lifecycle During Execution and Input Wrapping

1. **Startup ($H_{\text{occ}} = H_{\text{banner}} + 2$):**
   - `Host` renders `BlockBanner` + `PromptInput` (`~/path፧`) + 1 blank footer line inline.
   - `Host.allocHeight` is initialized to $H_{\text{occ}}$.
2. **Incremental Typing & Input Wrapping:**
   - As the user types on row 1 of `PromptInput`, only that 1 row differs between `currBuf` and `prevBuf`; `Host` updates only that row in-place.
   - If the user types a multi-line wrapped command (`H_input` grows from $1 \rightarrow 3$), $H_{\text{occ}}$ grows by 2.
   - `Host` grows `allocHeight` by 2, shifting the blank footer line down and painting the wrapped input rows.
3. **Input Deletion & Slack-Gap Creation:**
   - If the user deletes the wrapped text (`Backspace` or `Ctrl+C`), `H_input` shrinks from $3 \rightarrow 1$, so $H_{\text{occ}}$ shrinks by 2 while `allocHeight` remains at its peak.
   - The 2 bottom rows become the **slack gap** ($G = 2$). `Host` diffs those 2 rows to blank spaces once and moves the cursor back up to `PromptInput`.
4. **Gap-First Output Growth (Push Down Before Ejecting Up):**
   - When the user submits a command or new output arrives, $H_{\text{occ}}$ increases.
   - While $H_{\text{occ}} \le H_{\text{alloc}}$, the stack pushes downward into the slack gap $G$ first (`G` decreases toward 0) without ejecting anything from the top.
   - Once $G = 0$ and $H_{\text{occ}} \le H_{\text{term}}$, the stack continues growing downward into unused terminal lines (`allocHeight` increases up to $H_{\text{term}}$).
5. **Top-Of-Stack Scrollback Ejection (`H_occ > H_term`):**
   - Once `allocHeight == H_term` and new block rows cause $H_{\text{blocks\_total}} - \text{previouslyEjected} > H_{\text{term}} - H_{\text{chrome}}$, the exact overflow delta $\Delta = (H_{\text{blocks\_total}} - \text{previouslyEjected}) - (H_{\text{term}} - H_{\text{chrome}})$ is rendered from the oldest non-ejected block rows $[\text{previouslyEjected} : \text{previouslyEjected} + \Delta]$.
   - Those $\Delta$ rows are emitted via `WriteToScrollback`, `previouslyEjected` advances by $\Delta$, and the remaining bottom-up slice + `PromptInput` + blank footer row are diff-rendered in the active viewport.

---

## 4. File-by-File Implementation Map

| File | Responsibility in This Plan |
|------|-----------------------------|
| `kabibi/ui/shell/prompt.go` & `prompt_test.go` | Ensure `\w፧` / `፧` is the sole default prompt everywhere; update tests expecting `$ `. |
| `kabibi/ui/shell/prompt_input.go` | Default `promptFn` returns `"፧"`. |
| `kabibi/ui/shell/shell.go` | Add `BlockKind` (`BlockBanner`, `BlockCommand`, `BlockAITurn`), block render caching fields, and PTY execution in `runResolvedCommand` when stdio is unredirected. |
| `kabibi/ui/shell/exec_session.go` | Unify block/record IDs, retain full command output without 32KB truncation, track active PTY handle (`Resize`, `Write`, `Close`), and expose block lookup by ID. |
| `kabibi/ui/shell/tools_context.go` | Enhance `shell_history` and register `read_command_output` tool for inspecting full block output by ID/range. |
| `kabibi/ui/shell/shell_widget.go` | Build retro `BlockBanner` on startup; implement bottom-up viewport fitting, per-block `CellBuf` caching, animated artifact ticker on active block, 1-row blank footer below `PromptInput`, and slack-aware scrollback ejection watermark. |
| `kabibi/ui/host.go` | Upgrade inline renderer to track `allocHeight` (slack gap $G = H_{\text{alloc}} - H_{\text{occ}}$) and perform per-row dirty diffing (`currBuf` vs `prevBuf`) using relative cursor motion. |
| `kabibi/ui/app/app_snapshot_test.go` & `app_tui_e2e_test.go` | Verify 3-part startup stack (banner + `፧` prompt + blank footer), incremental row-only diffs, wrap-then-delete slack-gap closure before upward ejection, and PTY command execution. |
