# Kabibi Dual Blue File Manager HUD & Split Event Routing Plan (`3-blue.md`)

## 1. Architectural Vision & The Spatial Illusion

Kabibi's dual-pane file manager ("the blue panels") is designed around a deliberate, high-craft terminal illusion: making a standard inline terminal session behave like a non-destructive floating HUD overlay while preserving native terminal scrollback.

### The Five Mechanics of the Blue Panel Illusion

1. **The Perfect Fit at the Bottom:**
   - On startup, Kabibi's stack consists of `BlockBanner` (3 rows), `PromptInput` (`~/path፧`, 1 row), and the reserved blank footer row (1 row) — an initial stack of $H_{\text{gap}} \approx 5\text{–}6$ rows.
   - When the user presses `Ctrl+O` (or `Esc` when panels are open and `PromptInput` is empty) to summon the dual blue panels, the panels hang down from the top of the terminal window with height:
     $$H_{\text{panels}} = H_{\text{term}} - H_{\text{gap}}$$
   - The bottom gap $H_{\text{gap}}$ left beneath the blue panels **exactingly matches** the height of the initial banner + prompt + footer stack (clamped so the lower region never exceeds 40% of a short terminal).
2. **The Upward Push & "Slapped-On-Top" HUD Illusion:**
   - The first time `Ctrl+O` opens the panels, the composite `AppWidget` transitions its occupied height from its compact inline height ($H_{\text{occ}} < H_{\text{term}}$) to the full terminal height $H_{\text{term}}$.
   - Before painting the blue panels on that transition frame, `ui.Host` advances the inline allocation to $H_{\text{term}}$, pushing the user's prior external terminal lines cleanly into native terminal scrollback above row 0.
   - Immediately in the same flush, `DualPaneWidget` paints rows $0 \dots H_{\text{panels}}-1$ in classic Norton/Far blue (`#0000A8`), while `BlockBanner`, `PromptInput`, and the blank footer row sit in rows $H_{\text{panels}} \dots H_{\text{term}}-1$.
   - Because the blue panels cover the top and the banner fills the bottom gap, the user never sees a blank jump—visually, the blue panels appear slapped directly on top of the terminal like a floating HUD.
3. **Sliding Underneath the Panels (The Hidden Conveyor Belt):**
   - While the blue panels are visible, `Shell` renders its bottom-up stack over a **masked full-screen viewport** of height $H_{\text{term}}$, and `AppWidget` blits `DualPaneWidget` over the top $H_{\text{panels}}$ rows.
   - As the user runs commands in the shell below the panels, new command blocks enter at the bottom (`row H_term - H_chrome - 1`) and push `BlockBanner` and older command lines **upward into the hidden region** ($0 \dots H_{\text{panels}}-1$) behind the blue panels.
   - Crucially, while those lines are climbing through rows $0 \dots H_{\text{panels}}-1$, **nothing is ejected via `WriteToScrollback`** because the real terminal history was already pushed above the screen when the panels opened!
4. **Catching Up to the Physical Top of the Screen:**
   - Only after the user generates enough shell output to fill the entire hidden height behind the panels ($H_{\text{unedjected}} > H_{\text{term}} - H_{\text{chrome}}$) do the oldest non-ejected lines reach row 0 at the physical top of the terminal.
   - From that point onward, each additional line of shell output ejects the top-most line above row 0 into native terminal scrollback via `WriteToScrollback`, while the visible gap below the panels continues showing the newest lines and `DualPaneWidget` remains rock-solid on top.
5. **The Known Tradeoff (Quick `Ctrl+O` Double-Tap):**
   - If the user presses `Ctrl+O` to open the panels (claiming $H_{\text{term}}$ rows) and immediately presses `Ctrl+O` again before generating enough shell output to fill the $H_{\text{panels}}$ rows behind the panels, unmasking the top of the screen reveals the blank rows above `BlockBanner` inside the $H_{\text{term}}$ allocated footprint (with `BlockBanner` + `PromptInput` + footer anchored at the bottom, or the revealed partial history).

---

## 2. Current Codebase Inventory & Gaps to Bridge

### What Already Exists in `kabibi/ui/`

| Package / File | Current State | What Needs to Be Done |
|----------------|---------------|-----------------------|
| `kabibi/ui/filemanager/filepane_widget.go` | Has `FilePaneWidget` and `DualPaneWidget` with multi-column pagination (`innerW / 18`), `..` parent entry, sorting (dirs first, case-insensitive), selection bar, item marking (`•`), and single-column divider `│`. | Currently written against legacy types (`tea.KeyMsg`, `Cell{R, FG, BG}`, `colorBlue` constants from `kabibi/old`). Needs to be ported to canonical `ui.Widget` (`terminal.CellBuf`, `terminal.Cell`, `ui.FilePanelStyle` / `ui.SelectedFileStyle` from `ui/styles.go`, and `github.com/charmbracelet/x/input.Event`). |
| `kabibi/ui/filemanager/fileops.go` | Complete recursive `copyPath`, `movePath`, `deletePath`, byte-rate progress accounting (`opSink`), and overwrite collision resolution (`colOverwrite`, `colSkip`, `colOverwriteAll`, `colSkipAll`). | Decouple from `tea.Cmd` / `tea.Msg`; drive progress updates via a callback + `Invalidate()` on `AppWidget` so the progress/collision `DialogWidget` updates smoothly in `ui.Host`. |
| `kabibi/ui/dialog/dialog.go` | Has `DialogWidget` and input/confirm/choice/progress modal layouts (`mkdir`, `rename`, `copy`, `move`, `delete`, wildcard mask). | Port from `lipgloss` / `tea.KeyMsg` to native `terminal.CellBuf` box rendering and `x/input.Event` handling so modals composite cleanly over the 2D `CellBuf` grid. |
| `kabibi/ui/editor/editor.go` | Full syntax-highlighted editor (`chroma/v2`), tab expansion, selection, clipboard (`atotto/clipboard`), vertical/horizontal scroll. | Port from `tea.KeyMsg` / `lipgloss` to `ui.Widget` (`Measure`, `Render`, `HandleEvent`) so `F4` opens a full-screen editor takeover inside `AppWidget`. |
| `kabibi/ui/app/app_widget.go` | Currently a thin wrapper around `sh *shell.Shell`. | Upgrade `AppWidget` into the composite coordinator owning `sh *shell.Shell`, `panels *filemanager.DualPaneWidget`, `panelsVisible bool`, and optional modal/editor overlays. |

---

## 3. Exact Geometry & Rendering Protocol (`AppWidget` + `Shell` + `DualPaneWidget`)

### 3.1 Calculating `H_gap` and `H_panels`

Let $W, H_{\text{term}}$ be the terminal dimensions passed to `AppWidget.Render(rect, ctx)`:

1. **Bottom Gap Height ($H_{\text{gap}}$):**
   - Measure the initial stack height:
     $$H_{\text{base}} = H_{\text{banner}} + H_{\text{input}} + H_{\text{footer}}$$
     With the 3-row retro banner, 1-row `PromptInput`, and 1-row blank footer, $H_{\text{base}} = 5$ (or $6$ if a 1-line separator/peek is desired; configurable via `DefaultPanelGapRows = 5`, expanding dynamically if `PromptInput` wraps to multiple lines: $H_{\text{gap}} = \max(5, 3 + H_{\text{input}} + 1)$).
   - Apply the historical 40% ceiling on short terminals so panels always remain usable:
     $$H_{\text{maxGap}} = \max(3, \lfloor 0.40 \times H_{\text{term}} \rfloor)$$
     $$H_{\text{gap}} = \min(H_{\text{gap}}, H_{\text{maxGap}})$$
   - Panel height from the top of the screen:
     $$H_{\text{panels}} = \max(5, H_{\text{term}} - H_{\text{gap}})$$

2. **How `Shell` Renders When Panels Are Open vs. Closed:**
   - Rather than destroying `Shell`'s stack state when toggling `Ctrl+O`, `Shell` supports a **minimum viewport height** (or `SetBottomAligned(fullHeight bool)` when `panelsVisible` or after `panelsEverOpened`):
     - **Before `Ctrl+O` is ever pressed (`!panelsVisible && !fullScreenClaimed`):**
       - `AppWidget.Measure` delegates to `Shell.Measure`: height starts at $H_{\text{occ}} = 5$ rows and grows inline as commands run.
     - **When `Ctrl+O` is pressed (`panelsVisible == true`):**
       - `AppWidget` marks `fullScreenClaimed = true` and `Measure` returns `{W: W, H: H_term}`.
       - `Shell.Render` runs with `rect = {X: 0, Y: 0, W: W, H: H_term}` in **Bottom-Pinned Stack Mode**:
         - The non-ejected block slice of height $H_{\text{collected}} \le H_{\text{term}} - H_{\text{chrome}}$ is placed **immediately above `PromptInput`** at rows:
           $$Y_{\text{blocks}} = (H_{\text{term}} - H_{\text{chrome}}) - H_{\text{collected}} \dots (H_{\text{term}} - H_{\text{chrome}} - 1)$$
         - `PromptInput` is placed at row $H_{\text{term}} - H_{\text{chrome}}$.
         - The 1 blank footer row sits at row $H_{\text{term}} - 1$.
         - **Why Bottom-Pinning Makes the Illusion Exact:**
           - On startup, $H_{\text{collected}} = H_{\text{banner}} = 3$ and $H_{\text{chrome}} = 2$, so the banner + prompt + footer occupy the bottom $5$ rows $[H_{\text{term}}-5 \dots H_{\text{term}}-1]$.
           - `DualPaneWidget` renders into `{X: 0, Y: 0, W: W, H: H_panels}` where $H_{\text{panels}} = H_{\text{term}} - 5$, and is blitted onto rows $[0 \dots H_{\text{term}}-6]$ of the composite `CellBuf`.
           - The bottom border of `DualPaneWidget` (row $H_{\text{term}}-6$) touches the top line of `BlockBanner` (row $H_{\text{term}}-5$) with **zero gap and zero overlap**!
           - As commands run and $H_{\text{collected}}$ grows from $3 \rightarrow 4 \rightarrow \dots \rightarrow H_{\text{term}} - H_{\text{chrome}}$, the block rows naturally climb upward from row $H_{\text{term}}-5$ toward row $0$ *behind* `DualPaneWidget`.
           - `Shell`'s ejection watermark (`hTarget = totalBlockRows - (H_term - chromeH)`) remains `0` the entire time the blocks are climbing behind `DualPaneWidget`, and only becomes $> 0$ once $H_{\text{blocks\_total}} > H_{\text{term}} - H_{\text{chrome}}$ (when the hidden stack reaches row 0 at the very top of the terminal)!

3. **What Happens in `ui.Host` on the First `Ctrl+O` Transition:**
   - Before `Ctrl+O`, `Host.inlineMode` is `true` with `allocHeight` $= H_{\text{occ}} < H_{\text{term}}$ (e.g., 5 rows).
   - When `Ctrl+O` flips `panelsVisible = true`, `AppWidget` measures and renders $H_{\text{term}}$ rows, and `Host` expands `allocHeight` from $5 \rightarrow H_{\text{term}}$:
     - To push the prior external terminal history cleanly above row 0 (so the blue panels don't overwrite the user's pre-Kabibi terminal lines), `Host` emits $H_{\text{term}} - \text{prevAlloc}$ newlines at the bottom of the existing inline region when transitioning to full-height HUD mode, then diffs/renders the composite `CellBuf`.
     - Because `Host` stays on the main terminal screen (never entering `\x1b[?1049h` alt-screen), native terminal scrollback remains 100% live and scrollable with the mouse wheel!

---

## 4. Far Manager Split Key-Routing & Executable Launching

### 4.1 Complete Input Routing Matrix (`AppWidget.HandleEvent`)

When no modal dialog or full-screen editor is open:

1. **If `Shell.IsRunning()` is `true`:**
   - All key and paste events route directly to `Shell.HandleEvent` (`Ctrl+C` cancels the running command/AI turn; other keys forward to `es.WriteActivePTY` if an interactive PTY child is running).
2. **If `Ctrl+O` is pressed (`keyStr == "ctrl+o"`):**
   - Toggles `w.panelsVisible = !w.panelsVisible`.
   - When opening panels for the first time, syncs the active pane's directory to `w.sh.ExecutionSession().Dir()`.
   - Calls `w.invalidate()` and returns `true`.
3. **If `w.panelsVisible` is `false`:**
   - All events route directly to `w.sh.HandleEvent(e)`.
4. **If `w.panelsVisible` is `true` (and `!w.sh.IsRunning()`):**
   - Let `hasInput := w.sh.Input().Value() != ""`.

| Key (`ev.String()` / `ev.Code`) | Condition | Target & Action |
|---------------------------------|-----------|-----------------|
| **`ctrl+o`** | Always | Toggle blue panels OFF (`w.panelsVisible = false`). |
| **`esc`** | `!hasInput` | Hide blue panels (`w.panelsVisible = false`). |
| **`esc`** | `hasInput` | Clear `PromptInput` (`w.sh.Input().Reset()`). |
| **`tab`** | Always | Toggle active blue pane (`Left` $\leftrightarrow$ `Right`) and sync `ExecutionSession` `cwd` to the newly active pane's directory (`w.syncShellCwdToActivePane()`). |
| **`up` / `down` / `pgup` / `pgdn`** | Always | Route to active `FilePaneWidget` (moves selection bar by 1 item or 1 page). |
| **`home` / `end`** | `!hasInput` | Route to active `FilePaneWidget` (jumps selection to first / last item). |
| **`home` / `end`** | `hasInput` | Route to `PromptInput` (moves text cursor to start / end of command line). |
| **`left` / `right`** | `!hasInput` | Route to active `FilePaneWidget` (moves selection left/right by 1 column `itemsPerCol` in multi-column view). |
| **`left` / `right`** | `hasInput` | Route to `PromptInput` (moves text cursor left/right in command line). |
| **`insert`** | Always | Toggle selection mark (`•`) on highlighted item in active pane and advance cursor down 1 row. |
| **`ctrl+enter`** / **`ctrl+f`** | Always | Insert highlighted filename (shell-quoted if needed) into `PromptInput` at cursor. |
| **`enter`** | `hasInput` | Submit `PromptInput` to `Shell` (`w.sh.HandleEvent(e)`), then refresh both panels on command completion and sync active pane to `ExecutionSession.Dir()`. |
| **`enter`** | `!hasInput` | **Active Panel Action (see §4.2 below):**<br>• If `..` or directory: step into directory (`LoadDir`) and sync `ExecutionSession` `cwd`.<br>• If executable file (or `.sh` / shebang / `PATHEXT` / mode `0111`): execute `./<name>` in `Shell`!<br>• Otherwise: no-op (or open in editor if configured). |
| **`f2` / `f4` / `f5` / `f6` / `f7` / `f8` / `delete`** | Always | Trigger file manager operation (`Rename`, `Edit`, `Copy`, `Move`, `Mkdir`, `Delete`). |
| **Printable characters, `space`, `backspace`** | Always | Route to `PromptInput` via `w.sh.HandleEvent(e)` so typing a command works seamlessly at any time. |

### 4.2 Pressing `Enter` on an Executable in the Active Panel

When `panelsVisible == true`, `PromptInput` is empty (`!hasInput`), and the user presses `Enter`:

1. Inspect `item, ok := activePane.SelectedItem()`:
   - **Case 1 — Directory (`item.isDir`):**
     - Resolve target path (`filepath.Dir(activePane.Dir())` for `".."` or `filepath.Join(activePane.Dir(), item.name)`).
     - If stepping into `".."`, remember the child directory basename (`prevBase := filepath.Base(activePane.Dir())`) and focus it after loading the parent directory (`SelectByName(prevBase)` — classic Far Manager behavior!).
     - Update `ExecutionSession` working directory (`es.SetDir(target)`) so the `\w፧` prompt updates immediately on the same frame.
   - **Case 2 — Executable File (`isItemExecutable(fullPath, info)`):**
     - A file is considered executable if:
       - On Unix/wasip1: `info.Mode()&0o111 != 0`, or it has a `.sh` extension, or starts with `#!`.
       - On Windows: its extension matches `PATHEXT` (`.exe`, `.bat`, `.cmd`, `.com`, `.ps1`) or `.sh`, or it starts with `#!`.
     - Format the invocation command as `./<quotedName>` (or `.\<quotedName>` / `./<name>` since `mvdan/sh` and `resolvePlatformExecutable` handle `./name` across platforms).
     - Set `w.sh.SetInput("./" + quoteIfNeeded(item.name))` and trigger `w.sh.HandleEvent(KeyPressEvent{Code: KeyEnter})` so the executable runs as a real `BlockCommand` in the shell stack below the panels (with full PTY support and output streaming underneath the panels)!

### 4.3 Bidirectional CWD Synchronization (`ExecutionSession` $\leftrightarrow$ Active Panel)

1. **`ExecutionSession.SetDir(dir string)`:**
   - Add a thread-safe `SetDir(dir string) error` method on `ExecutionSession` that cleans `dir`, verifies it is an existing directory via `os.Stat`, and updates `es.runner.Dir = dir` (and `PWD` in `es.runner.Vars`).
   - Whenever the user navigates directories inside the active blue panel (`Enter` on a folder or `..`), or presses `Tab` to switch the active panel between Left and Right, `AppWidget` calls `es.SetDir(activePane.Dir())`.
   - Because `PromptInput`'s prompt closure reads `es.Dir()`, the bottom prompt (`~/new/path፧`) updates instantaneously!
2. **Shell $\rightarrow$ Active Panel Hook (`OnCommandFinished`):**
   - Add an optional `OnCommandFinished func(cwd string)` callback to `ShellOptions` (invoked when a shell command or AI turn completes).
   - When `cd foo`, `mkdir bar`, `rm baz`, or an AI file tool finishes, `AppWidget` receives `OnCommandFinished(newCwd)`:
     - If `newCwd != activePane.Dir()`, the active pane loads `newCwd`.
     - Both Left and Right panes reload their directory entries (preserving their current selected filename if it still exists) so any newly created/deleted files appear immediately in the blue panels.

---

## 5. Phased Implementation Roadmap

### Phase 1: Port `FilePaneWidget` & `DualPaneWidget` to Native `ui.Widget` (`kabibi/ui/filemanager/`)
1. Replace legacy `tea.KeyMsg` and `Cell{R, FG, BG}` in `kabibi/ui/filemanager/filepane_widget.go` with canonical `ui.Widget` (`Measure(ui.Constraints) ui.Size`, `Render(ui.Rect, ui.RenderContext) (ui.CellBuf, ui.CursorPos)`, `HandleEvent(input.Event) bool`).
2. Use `terminal.Cell` and the palette in `kabibi/ui/styles.go` (`FilePanelStyle`, `FolderStyle`, `FileStyle`, `SelectedFileStyle`, `SelectedFolderStyle`, `MarkedStyle`, `ActiveTitleStyle`), adding an `ExecutableStyle` (bright green `#00FF87` on `#0000A8` blue, with `*` indicator) so executables stand out visually in the panels.
3. Preserve multi-column pagination (`numCols = max(1, innerW / 18)`), `..` parent navigation (focusing the previous child folder name when ascending via `..`), and the single-column `│` divider between Left and Right panes.
4. Update `kabibi/ui/filemanager/filepane_widget_test.go` so all unit tests pass against `ui.Widget` and `terminal.CellBuf`.

### Phase 2: Upgrade `Shell` & `Host` for Bottom-Pinned HUD Masking
1. Add `SetBottomAligned(v bool)` (or `ModeHUDStack`) and `OnCommandFinished func(cwd string)` to `Shell`:
   - When `bottomAligned == true`, `Shell.Render` places the collected block rows immediately above `PromptInput` (at `Y = (maxH - chromeH) - collectedRows`) rather than at `Y = 0`, while keeping the ejection threshold at `totalBlockRows - (maxH - chromeH)`.
   - Expose `Shell.SetDir(dir string)` / `ExecutionSession.SetDir(dir string)` for instant panel-to-shell directory sync.
2. In `ui.Host`:
   - When an inline widget expands `allocHeight` to `ht` (`H_term`) on `Ctrl+O`, emit the exact delta of `\r\n` lines needed to push prior external terminal lines into native scrollback before painting the top panel rows.

### Phase 3: Composite `AppWidget` & Far Manager Split Key Router (`kabibi/ui/app/`)
1. Expand `AppWidget` in `kabibi/ui/app/app_widget.go`:
   - Own `sh *shell.Shell`, `panels *filemanager.DualPaneWidget`, `panelsVisible bool`, and `fullScreenClaimed bool`.
   - Compute `H_gap` and `H_panels = rect.H - H_gap`.
   - When `panelsVisible` is `true`, render `sh` into the full `rect`, then render `panels` into `{X: 0, Y: 0, W: rect.W, H: H_panels}` and blit `panels` over rows `0 .. H_panels-1`.
2. Implement the Far Manager Split Key-Routing table (§4.1), executable launching on `Enter` (§4.2), and bidirectional CWD synchronization (§4.3).

### Phase 4: Port Modal Dialogs, File Ops & Editor (`F2`–`F8`) to `ui.Widget`
1. Port `kabibi/ui/dialog/dialog.go` and `kabibi/ui/filemanager/fileops.go` from BubbleTea messages to native `ui.Widget` modals driven by `AppWidget` callbacks and `Invalidate()`.
2. Port `kabibi/ui/editor/editor.go` to `ui.Widget` so `F4` opens the Chroma-highlighted editor over the viewport and `Esc` returns to the panels + shell stack.

### Phase 5: End-to-End VT Emulator & Snapshot Verification
1. Add unit and VT-emulator E2E tests in `kabibi/ui/filemanager/filepane_widget_test.go` and `kabibi/ui/app/app_tui_e2e_test.go`:
   - Verify `Ctrl+O` summons the dual blue panels at the top of the screen while keeping `BlockBanner`, `PromptInput` (`\w፧`), and the blank footer line in the bottom gap.
   - Verify running commands while panels are open slides output lines upward *underneath* the blue panels without premature scrollback ejection until output catches up to row 0.
   - Verify Far Manager split key routing (`Up`/`Down`/`Left`/`Right`/`Tab`/`Enter` when input is empty vs. non-empty, `Enter` on directory vs. executable script/binary, and `cd` syncing both ways).
