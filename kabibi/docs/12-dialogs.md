# Kabibi Modal Dialogs & Reusable Micro-Widget Library (`12-dialogs.md`)

## 1. Architectural Vision & Design Philosophy

Kabibi's modal dialogs follow the same core philosophy as the rest of the shell UI: **compact, keyboard-first, zero external TUI framework bloat, and rendered as ANSI text**. `terminal.CellBuf` is an internal composition and host-diff representation, not the widget output contract.

Inspired by FAR Manager's ergonomics while retaining Kabibi's modern dark-gray aesthetic (`#1A1F26` surface, `#FFD700` gold borders/highlights, `#E5E5E5` text), the dialog system is built around four principles:

1. **Start Simple, Intervene Only When Needed:**
   - Upfront operation dialogs (`Copy`, `Move`, `Rename`, `Mkdir`, `Delete`) ask only for the essential path parameters—no pre-emptive conflict policy matrix (`Ask / Overwrite / Skip`) cluttering the initial form.
   - When a file collision or recoverable error actually occurs mid-operation, the **active progress dialog dynamically expands in-place** to reveal conflict-resolution controls (`[Overwrite]`, `[Skip]`, `[All]`, `[Skip all]`, `[Cancel]`), pausing the worker until resolved and then shrinking back to progress view.
2. **Self-Sufficient Without Blue Panels:**
   - Invoking `/copy`, `/move`, `/rename`, `/delete` (or `/remove`), and `/edit` without arguments when the blue file panels are hidden **must not** force-open the blue panels.
   - Instead, the dialog opens directly over the inline shell stack (`ShellMode = ModeStack`) with editable path inputs (resolved against the shell's current working directory), Tab path auto-completion, and Up/Down history recall of previously used paths.
   - When the blue panels *are* visible, panel selection/marks and opposite-pane directory continue to pre-fill the dialog fields for one-key (`F5`/`F6`/`F2`/`F8`) workflows.
3. **Dynamic Layout Across Small and Large Screens:**
   - No hardcoded `54x10` geometry. Every dialog computes its `Measure(c ui.Constraints) ui.Size` from its child controls and current terminal constraints, growing comfortably on wide terminals (up to a sensible max width) and compacting gracefully on narrow/short viewports without clipping essential controls.
   - When a modal dialog is active while blue panels are hidden, `AppWidget` overlays the dialog onto the current viewport without permanently claiming full-screen panel mode after the dialog closes.
4. **Small, Context-Neutral Widget Library:**
   - Reusable primitives—`TextInput`, `Checkbox`, `Button` / `ButtonRow`, and `ProgressBar`—belong in the shared `kabibi/ui/widgets` package, not under `dialog`.
   - They must not depend on dialogs or assume modal focus, layout, colors, or actions. Dialogs, onboarding flows, settings, and other UI surfaces compose and configure the same widgets according to their own needs.
   - Each widget follows the existing `ui.Widget` contract where practical. Parents remain responsible for composition, focus order, and routing; focused controls handle their own editing or activation behavior.

---

## 2. Existing Kabibi Patterns & Codebase Integration Points

Research across the existing `kabibi/ui` packages establishes the following patterns to reuse and unify:

| Area | Current Implementation | What We Reuse / Change |
|---|---|---|
| **Widget Contract** (`kabibi/ui/widget.go`) | `Measure(Constraints) Size`, `Render(Rect, RenderContext) (CellBuf, CursorPos)`, `HandleEvent(input.Event) bool`. | Keep unchanged. Shared widgets and `DialogWidget` both follow this contract without depending on each other. |
| **Single-Line / Wrapped Input** (`kabibi/ui/shell/prompt_input.go`) | `PromptInput` handles `input.PasteEvent`, `Home`/`End`, `Ctrl+A`/`Ctrl+E`, `Left`/`Right`, `Backspace`/`Delete`, and `Up`/`Down` history. | `PromptInput` wraps across lines for shell commands; dialogs need a **single-line horizontal-scrolling `TextInput`** that adds selection/clipboard (`Ctrl+C`/`Ctrl+X`/`Ctrl+V` like `editor.go`), edge overflow indicators (`…`), Tab path completion, and history recall. |
| **Selection & Clipboard** (`kabibi/ui/editor/editor.go`) | Uses `github.com/atotto/clipboard` with an in-memory fallback string (`clip`), `Shift+Left/Right/Home/End`, `Ctrl+Left/Right` word jumps, `Ctrl+A` select all, `Ctrl+C`/`Ctrl+X`/`Ctrl+V`, plus `input.PasteEvent`. | Extract/share the single-line selection, word-motion, and clipboard fallback mechanics in `TextInput` so pasting paths or editing filenames works identically in dialogs and the editor. |
| **File Operations & Collisions** (`kabibi/ui/filemanager/fileops.go`) | `RunFileOp` emits `FileOpProgress` (throttled to 40ms) and blocks on `resume <-chan CollisionChoice` when `onCollide(dst)` fires. Supports `ColOverwrite`, `ColSkip`, `ColOverwriteAll`, `ColSkipAll`, `ColCancel`. | Keep `RunFileOp` channel protocol intact! Instead of swapping `w.dialog` to a separate `DialogChoice` and losing progress context, `AppWidget` mutates the existing `DialogProgress` into its expanded collision state and exposes `ColSkipAll` alongside `ColOverwriteAll`. |
| **Slash Command Dispatch** (`kabibi/ui/app/app_widget.go`) | `/copy`, `/move`, `/delete`, `/rename`, `/edit` with 0 args call `showPanelsForFileAction()`, which forces `SetPanelsVisible(true)` and returns early. | Remove `showPanelsForFileAction()`. Zero-arg slash commands open their respective dialog directly (with a source field when no panel selection exists). Also add `/mkdir` slash command for symmetry with `F7`. |

---

## 3. Shared Reusable Widget Library Specification

To keep the footprint minimal and prevent dialog-specific coupling, reusable controls live in **`kabibi/ui/widgets`**. This package may depend on the core `kabibi/ui` contract and terminal/input primitives, but must not import `dialog`, `app`, `shell`, `wizard`, or feature-specific packages. Feature surfaces may import `ui/widgets` and compose the controls. The initial library is intentionally limited to **four primitives**—`TextInput`, `Checkbox`, `Button` / `ButtonRow`, and `ProgressBar`—plus, only if composition proves it useful, a small generic layout helper.

The package is not a mandate to replace every existing input or UI component at once. In particular, the shell's multiline `PromptInput` and the editor's document editing model have distinct responsibilities. Share well-isolated editing behavior where it reduces duplication, but keep migrations incremental and driven by real reuse.

### 3.1 Caller-Configurable Styling

- Shared widgets expose compact style/options configuration rather than hard-coding the dialog palette or drawing a dialog surface. The default Kabibi theme can use `#1A1F26`, `#FFD700`, `#E5E5E5`, and `#8A95A5`; dialog containers independently own their background, border, padding, title, and spacing.
- Controls distinguish normal, focused, disabled, and (where relevant) selected/error states. A host may use its own theme while retaining the same interaction behavior.
- Focused input fields visibly differ from unfocused fields and expose the terminal cursor when appropriate. Selection and overflow indicators remain legible under configured styles.
- Buttons provide clear focused/unfocused/disabled appearances; labels and hotkeys are data, not embedded assumptions about a particular dialog.

### 3.2 `TextInput` (Single-Line Horizontally Scrolling Input)

A compact single-line editor for paths, filenames, search queries, settings values, and other short text. It is not intrinsically a path field; completion and history are optional caller-provided behaviors.

- **State:**
  - `Label string` (optional caption rendered above the field, or inline when vertical space is tight)
  - `Value []rune`
  - `Cursor int` (rune index `0..len(Value)`)
  - `Anchor int` + `Selecting bool` (selection span `[min(Cursor,Anchor) : max(Cursor,Anchor))`)
  - `ScrollCol int` (horizontal scroll offset in visual terminal columns)
  - `History []string` + `HistoryIdx int` (Up/Down recall of past entries for this field kind)
  - Optional completion and history providers supplied by the composing feature; filesystem path completion is one use, not a dependency of the widget library.
- **Horizontal Scrolling & Edge Ellipsis (`…`) Algorithm:**
  - Given inner field width $W \ge 4$:
  - Compute total visual width of `Value` and visual column `curCol` of `Cursor` using `runewidth.RuneWidth`.
  - Maintain `ScrollCol` so `curCol` is always within the visible interior window, accounting for 1-column left/right overflow indicators:
    - `hasLeftOverflow = ScrollCol > 0` (reserves column `0` for left `…`).
    - `hasRightOverflow = totalVisWidth - ScrollCol > W - (1 if hasLeftOverflow else 0)` (reserves column `W-1` for right `…`).
    - When `Cursor` moves left of the visible text slice, decrease `ScrollCol`; when `Cursor` moves right of the visible text slice (including 1 cell for the cursor at `len(Value)`), increase `ScrollCol`.
  - Render the visible slice into `[0 .. W-1]`, placing `…` at column `0` if `hasLeftOverflow` and `…` at column `W-1` if `hasRightOverflow`.
- **Keyboard & Clipboard Handling:**
  - **Movement:** `Left`, `Right`, `Ctrl+Left`, `Ctrl+Right` (word boundaries), `Home`/`Ctrl+A` (start, or select-all on `Ctrl+A`), `End`/`Ctrl+E`.
  - **Selection:** `Shift+Left`, `Shift+Right`, `Shift+Home`, `Shift+End`, `Ctrl+A` (select all). Typing any character, Backspace, Delete, or pasting replaces the active selection.
  - **Initial Selection Convenience:** When a `TextInput` is initialized with a default value (e.g., destination path in `Copy`/`Move` or current filename in `Rename`), its initial content is selected (`Anchor = 0, Cursor = len(Value), Selecting = true`) just like FAR Manager—so typing immediately replaces the default, while pressing `Left`/`Right`/`End` clears selection and edits in-place.
  - **Copy / Cut / Paste:**
    - `input.PasteEvent` (bracketed paste from terminal/browser): strips `\r`/`\n` into spaces (or first line) and inserts at cursor/replaces selection.
    - `Ctrl+V`: reads from `clipboard.ReadAll()` (falling back to shared in-process clipboard string) and inserts.
    - `Ctrl+C` (when selection is non-empty): copies selection to clipboard without cancelling the dialog.
    - `Ctrl+X` (when selection is non-empty): cuts selection to clipboard.
    - `Ctrl+U` / `Ctrl+K`: kill to start / end of line.
  - **History & Completion:**
    - History navigation is enabled only when the caller provides history and assigns keys to it; e.g., recent paths in file dialogs or recent queries in another surface.
    - Completion is a caller-provided operation. For path fields it may complete filesystem paths relative to the shell working directory; another surface may use it for a different domain or leave it disabled.

### 3.3 `Checkbox`

A 1-row boolean toggle for compact options.

- **Visual:** `[x] Label text` or `[ ] Label text` (when focused, the `[x]` box or row is highlighted in `#FFD700` / `#005F87`, with mnemonic letter highlighted).
- **Interaction:** `Space` toggled when focused; Alt+Hotkey (or direct hotkey when no `TextInput` is focused) toggles from anywhere in the dialog.

### 3.4 `ButtonRow`

A horizontal group of action buttons anchored at the bottom of the dialog.

- **Visual:** Centered row of buttons, e.g. `[  OK  ]  [ Cancel ]` or `[ Overwrite ] [ Skip ] [ All ] [ Skip all ] [ Cancel ]`.
- **Responsive Wrapping:** If the total width of buttons exceeds inner dialog width $W_{\text{inner}}$ on a narrow terminal, buttons tighten padding (`[OK] [Cancel]`) and, if still wider than $W_{\text{inner}}$, wrap across 2 rows automatically during `Measure` and `Render`.
- **Interaction:**
  - `Left` / `Right` cycles the active button within `ButtonRow`.
  - `Enter` or `Space` activates the focused button.
  - Pressing a button's `Hotkey` activates it immediately whenever focus is on `ButtonRow` or a non-text control (Confirmation / Choice / Progress collision state).

### 3.5 `ProgressBar`

A responsive progress indicator for any operation that can report progress. The widget renders supplied progress state; its parent owns the operation lifecycle, surrounding messages, and cancellation actions.

- **Visual:**
  - Row 1: Current file/item name (truncated in the middle ` prefix…suffix ` if longer than $W_{\text{inner}}$ so both directory context and filename remain visible).
  - Row 2: Full-width adaptive bar `████████░░░░░░ 64%` where bar width scales dynamically to $\max(6, W_{\text{inner}} - 6)$.
  - Row 3: Byte counters and throughput `12.4 MB / 19.2 MB  4.1 MB/s` (hidden if vertical space is constrained).

---

## 4. Dynamic Dialog Layout & Focus Architecture

### 4.1 Responsive Sizing (`Measure` & `Render`)

Instead of fixed `Width: 54, Height: 10`, `DialogWidget.Measure(c ui.Constraints)` calculates geometry dynamically:

1. **Width Calculation:**
   - Preferred width $W_{\text{pref}}$ depends on dialog content:
     - Standard forms (`Input`, `Transfer`, `Progress`): target $\min(68, \max(44, \lfloor 0.75 \times c.\text{MaxW} \rfloor))$.
     - Compact confirmations/errors (`Confirm`, `Choice`): target $\min(60, \max(36, \text{contentWidth} + 6))$.
   - Clamp to terminal width:
     - If $c.\text{MaxW} \le 0$, default to $80$.
     - Max allowed width is $\max(16, c.\text{MaxW} - 2)$ (leaving 1 cell margin on left/right when possible, or full $c.\text{MaxW}$ if $c.\text{MaxW} < 24$).
     - Never collapse to `H: 1` on narrow screens (`c.MaxW < 24`); continue rendering the vertical stack within whatever width is available.
2. **Height Calculation (Content-Driven):**
   - Top border (with embedded title): `1` row.
   - Top padding: `1` row (omitted if $c.\text{MaxH} < 10$).
   - Child controls stacked vertically, each measured with `Constraints{MaxW: innerW, MaxH: remainingH}`, with `1` blank spacer row between logical groups (collapsed to `0` spacer rows when $c.\text{MaxH}$ is tight so all controls fit without clipping).
   - Bottom padding + Bottom border: `1–2` rows.
   - Clamped to $\min(\text{desiredH}, c.\text{MaxH})$.

### 4.2 Focus Ring & Keyboard Routing

A `DialogWidget` maintains an ordered slice of focusable items (`focusIndex int`):

- **Focus Traversal:**
  - `Tab` / `Down`: moves focus to the next focusable control in the dialog (`TextInput` $\rightarrow$ next `TextInput` / `Checkbox` $\rightarrow$ `ButtonRow` $\rightarrow$ wraps to first control).
  - `Shift+Tab` / `Up`: moves focus to the previous focusable control.
  - *History vs. Focus in `TextInput`:* Inside a focused `TextInput`, `Alt+Up`/`Alt+Down` (or `Ctrl+P`/`Ctrl+N`, and `Up`/`Down` when the dialog has only a single `TextInput`) recalls input history; `Tab` when a path prefix has completion candidates triggers path auto-completion first, or advances focus when at the end of a completed token / in non-path inputs (with `Shift+Tab` always moving focus backward and `Down` always moving focus to the next field/button).
- **Default Enter & Escape Semantics:**
  - Pressing `Enter` when focused on any `TextInput` or `Checkbox` submits the dialog's primary default action immediately (FAR Manager fast-submit behavior), unless the dialog is in a state where `Cancel` is explicitly focused in `ButtonRow`.
  - Pressing `Enter` or `Space` when focused on `ButtonRow` invokes the currently highlighted button.
  - Pressing `Esc` always cancels/closes the dialog (or triggers cancellation in `DialogProgress`).

---

## 5. Dialog Flows & In-Place Progress Collision Handling

### 5.1 Progress Dialog with Embedded Mid-Operation Confirmations

When `startFileOp` runs `Copy`, `Move`, or `Delete`:

1. **Normal Running State (`DialogProgress`):**
   - Shows Title (`Copying`, `Moving`, `Deleting`), `ProgressBar` (current path, adaptive bar, throughput), and a bottom `[ Cancel ]` button (`Esc` or `Enter` on Cancel aborts via `opCancel()`).
2. **Collision State (In-Place Expansion):**
   - When `RunFileOp` invokes `onCollide(collidePath)`, `AppWidget` **does not** replace `w.dialog` with a separate `ChoiceDialog`.
   - Instead, `w.dialog.SetCollision(collidePath, resolveFn)` transitions the active `DialogProgress` into its expanded **Collision Prompt** state:
     - The top half of the dialog continues displaying the `ProgressBar` (frozen at the current percentage and byte count so the user sees how far the batch has progressed).
     - Below a horizontal divider `├──────────┤`, the dialog expands vertically by 4 rows to display:
       - Warning prompt: `File already exists: <filename>` (and target directory/size context).
       - Embedded `ButtonRow`: `[ Overwrite ]  [ Skip ]  [ All ]  [ Skip all ]  [ Cancel ]` (hotkeys `O`, `S`, `A`, `K`, `C`).
     - Focus automatically moves to the collision `ButtonRow` (defaulting to `[ Overwrite ]`).
   - Once the user picks an option (via `Enter`/`Space` or hotkey `o`/`s`/`a`/`k`/`c` or `Esc` $\rightarrow$ `ColCancel`), `resolveFn(choice)`:
     - Clears the collision state on `DialogProgress` (shrinking the dialog smoothly back to its compact progress height).
     - Sends `choice` over the `resume` channel to unblock `RunFileOp`.
3. **Mid-Operation Error State:**
   - If a file operation fails (or `Mkdir` / `Rename` returns an OS error), the error is surfaced in a modal error dialog rather than silently ignored.

### 5.2 Standalone Slash-Command Dialogs (No Forced Blue Panels)

When the user executes a file-oriented slash command with **no arguments** (or missing destination):

| Command | Panels Visible (`panelsVisible == true`) | Panels Hidden (`panelsVisible == false`) |
|---|---|---|
| `/mkdir` or `F7` | Opens **Make Directory** dialog with 1 `TextInput` (`Create directory:`). | Opens **Make Directory** dialog over the inline shell stack. Creates dir relative to `w.sh.Dir()`. |
| `/rename` or `F2` | Pre-fills source from selected item; opens 1-field **Rename** dialog (`Rename <item> to:`) initialized with `<item>`. | Opens 2-field **Rename** dialog (`Rename:` source `TextInput`, `To:` target `TextInput`), focused on source. |
| `/copy` (`F5`) / `/move` (`F6`) | Pre-fills sources from selected/marked items and destination from `OtherDir()`. Opens 1-field **Copy/Move** dialog focused on destination. | Opens 2-field **Copy / Move** dialog (`Source path(s):` `TextInput` + `Destination:` `TextInput` pre-filled with `.`), focused on `Source`. |
| `/delete` (`/remove`, `F8`) | Opens **Delete** confirmation dialog for selected/marked panel items (`[ Delete ] [ Cancel ]`, safe focus on `Cancel` or `Delete` per FAR style with `Y`/`N` hotkeys). | Opens **Delete** input/confirm dialog (`Delete path:` `TextInput` + `[ Delete ] [ Cancel ]`). |
| `/edit` (`F4`) | Opens editor on selected file in active pane. | Opens **Edit File** dialog with 1 `TextInput` (`File to edit:`) with path completion, then launches editor on submit. |

*Note on Inline Shell Viewport (`ModeStack`):*
In `AppWidget.Measure`, when `dlg != nil` and `panelsVisible == false`, `AppWidget` ensures the returned `ui.Size` is at least large enough to host the dialog (`max(shellSize.H, dlgSize.H + 2)` capped at `c.MaxH`), rather than forcing `fullScreenClaimed = true`. When the dialog closes, the shell stack remains in its inline height without leaving empty full-screen slack.

---

## 6. Phased Implementation Plan

### Phase 1: Shared Widget Primitives (`kabibi/ui/widgets`)
1. Implement `TextInput` with:
   - Rune-width-aware horizontal scrolling (`ScrollCol`) and left/right `…` overflow indicators.
   - Cursor movement (`Left`/`Right`, `Home`/`End`, `Ctrl+Left`/`Right`, `Ctrl+A`/`E`), selection (`Shift+Arrows`, initial select-all on default value), and clipboard/paste (`input.PasteEvent`, `Ctrl+C`/`X`/`V` with `atotto/clipboard` + in-memory fallback).
   - Optional caller-supplied history and completion behavior; no dependency on filesystem paths or dialogs.
2. Implement `Checkbox`, `ButtonRow` (with hotkey rendering and narrow-width wrapping), and `ProgressBar`.
3. Keep styling configurable and focused behavior independent from any parent layout or modal semantics.
4. Write unit tests in `kabibi/ui/widgets/widgets_test.go` covering:
   - Horizontal scrolling left/right with `…` indicators on both edges, CJK/wide runes, and cursor clamping.
   - `PasteEvent` and `Ctrl+C`/`Ctrl+X`/`Ctrl+V` selection replacement.
   - Button navigation, hotkeys, and narrow-screen button wrapping.

### Phase 2: Dynamic `DialogWidget` Layout & Embedded Progress Collisions
1. Refactor `DialogWidget` in `kabibi/ui/dialog/dialog.go` to compose the micro-widgets and implement dynamic `Measure(Constraints)` and `Render(Rect, RenderContext)` across small (`24x8`) and large (`120x40`) viewports.
2. Add multi-field form support (1 or 2 `TextInput` fields + optional `Checkbox`es + `ButtonRow`) with `Tab` / `Shift+Tab` focus ring traversal.
3. Extend `DialogProgress` with in-place collision state (`SetCollision` / `ClearCollision`) and add `[Skip all]` (`filemanager.ColSkipAll`) support.
4. Update and expand `kabibi/ui/dialog/dialog_test.go` for dynamic layout, multi-field focus traversal, and embedded progress collision resolution. Add focused integration tests showing that the same shared controls can be configured/composed outside dialog styling or behavior; do not force unrelated surfaces to migrate as part of this phase.

### Phase 3: `AppWidget` Integration & Panel-Free Slash Commands
1. Update `startFileOp` in `kabibi/ui/app/app_widget.go` so `onCollide` expands the existing `DialogProgress` in-place instead of replacing it, and maps all 5 collision choices (`ColOverwrite`, `ColSkip`, `ColOverwriteAll`, `ColSkipAll`, `ColCancel`).
2. Surface `os.MkdirAll` and `os.Rename` errors via `w.showSlashError(err)` instead of silently dropping them.
3. Update `dispatchSlashCommand` in `kabibi/ui/app/app_widget.go` (and `kabibi/slashcmd/commands.go` to include `/mkdir`) so zero-argument `/copy`, `/move`, `/rename`, `/delete`, `/remove`, `/edit`, and `/mkdir` open self-contained dialogs without forcing blue panels visible, while preserving pre-fill when panels are visible.
4. Maintain shared path input history on `AppWidget` so paths entered in dialogs can be recalled across subsequent dialogs.
5. Add integration and snapshot tests in `kabibi/ui/app/app_tui_e2e_test.go` and `kabibi/ui/app/slashcmd_test.go` verifying:
   - Zero-arg slash commands opening dialogs without showing blue panels.
   - Mid-operation collision expanding the progress dialog and resuming on choice selection.
   - Copy-paste and horizontal scrolling inside dialog inputs.

## 7. Implementation Status

The shared controls, responsive dialog composition, collision-in-progress flow, and panel-free slash-command forms are implemented. The widget contract now returns ANSI strings from every production `Render` method; parents interpret child frames in an isolated VT and compose the resulting cells internally. The shell prompt, editor, file panes, dialogs, wizard, and `CommandBlock` use this contract. The app integration also includes `/mkdir`, path history and completion relative to the shell working directory, and surfaced errors for directory creation and rename.

Focused tests cover ANSI round-tripping, widget editing and layout, dialog sizing/focus/collision behavior, slash-command routing, panel-free copy, collision continuation, and asynchronous relative-path completion. Some broader app snapshots still encode the older startup banner and therefore need separate fixture maintenance.

# Widgets Followups

**We want to redesign Widget system—100%.** Putting those three pieces together turns Kabibi's UI from two parallel rendering worlds (`ui.Widget` building `CellBuf`s vs. `CommandBlock` building ANSI strings) into **one unified, composable widget tree** where every component speaks the exact same language.

> **Implementation update:** The original sketches below are historical where they conflict with the implemented API. The authoritative contract is `Widget.Measure(Constraints) Size`, `Widget.Render(Rect, RenderContext) string`, and `Widget.HandleEvent(input.Event) bool`. Focus and invalidation are supplied through `RenderContext`; `Measure` declares desired size and the parent chooses the allocation. Widgets return ANSI, never `CellBuf` or a separate cursor result.

### Implemented composition model

- Composite parents parse each child ANSI frame in a reusable, resettable `terminal.Terminal`, then blit its exact allocation into an internal `CellBuf`. `RenderContext.CompositeANSI` preserves the cursor only from the focused child and translates it into parent coordinates.
- `CharResetForegroundDefault` and `CharResetBackgroundDefault` are per-channel inheritance markers during composition. They copy the matching destination color and reset marker independently; the child's grapheme and other attributes replace the destination cell. This is reset-as-fallback, not general alpha transparency.
- The host interprets the root ANSI frame into its internal diff buffer. Widget output remains ANSI throughout the public tree; cell buffers are private implementation details of composition and terminal diffing.
- `CommandBlock` implements `ui.Widget` and is rendered through the same ANSI-to-VT boundary as other shell blocks. `Shell` retains command metadata, PTY state, and settled-block cell caches; it remains responsible for routing active PTY input.
- `ANSIFrame` serializes composed rows as positioned ANSI lines and appends cursor visibility/position controls. The composition parser uses a spare margin column to avoid VT wide-glyph-at-margin loss without shifting explicitly positioned widget rows.

The sketches following this update describe the previous proposal and should not be read as the current method signatures or runtime behavior.

Look at how cleanly the three parts of your design click together:

---

### 1. The Redesigned `Widget` Interface (`Render` Deals in Strings)

Instead of widgets importing `terminal.CellBuf`, calculating 2D `(X, Y)` rune offsets, and returning a separate `CursorPos` struct:

```go
type Widget interface {
    Render(w, h int) string
    HandleEvent(e input.Event) bool
}

```

*(Or if a widget only needs width to flow vertically like a block or prompt, `h` is simply its max height budget!)*

* **Text + Styling + Cursor in One String:**
A widget just returns an ANSI string. If the widget wants to place the hardware cursor at an edit point (like `PromptInput`, `Dialog`, `Editor`, or an interactive `CommandBlock`), it emits `\x1b[s` (save cursor) + `\x1b[?25h` (show cursor) right at the cursor position in its text stream, writes the rest of its content, and ends with `\x1b[u` (restore cursor). If it doesn't want the cursor shown, it emits `\x1b[?25l` (or leaves it off).


* **Zero Manual Coordinate Math:**
Line wrapping, wide UTF-8 characters, SGR styling, and cursor `(X, Y)` coordinates are no longer calculated by hand inside `PromptInput` or `CommandBlock`—the parent's terminal emulator computes them automatically when ingesting the string.



---

### 2. `CommandBlock` Becomes a First-Class `Widget`

Turning `CommandBlock` (and `BlockBanner`, or even the AI Onboarding Wizard) from a special-case struct inside `Shell` into a `Widget` removes a ton of special-case plumbing in `Shell`:

* **Unified Stack of Child Widgets in `Shell`:**
Instead of `Shell` managing `s.blocks []CommandBlock` separately from `BlockBanner`, `PromptInput`, or an active onboarding `WizardWidget`, `Shell` simply holds a vertical stack of child `Widget`s!


* **`BannerWidget`**: `Render(w, h) string` returns the pompous retro diagnostic ANSI string.


* **`CommandBlockWidget`** (for both shell commands and AI turns):


* `Render(w, h) string` returns the formatted prompt + input header + `Output.String()` + transient `AnimLine` spinner/ticker (if `Status == StatusActive`).


* While active and running an interactive foreground PTY, `CommandBlockWidget.HandleEvent(e)` handles `Ctrl+C` or forwards keystrokes straight to the PTY!




* **`WizardWidget`** (when AI setup/onboarding runs): Just another block widget in the stack that renders its interactive menu as an ANSI string and handles arrow keys / Enter until completed!


* **`PromptInput`**: Sits at the bottom of the stack and returns the syntax-highlighted `~/path፧ ...` string with `\x1b[s` at the edit cursor.





(And for `shell_history` / `read_command_output` AI tools, `ExecutionSession` already keeps its own clean `CommandRecord` log, or inspects the `CommandBlockWidget` metadata directly!)

---

### 3. Parents Compose Using a Cached, Re-Wiped `terminal` + `CellBuf` Cache

Here is how parent containers (`Shell`, `DualPaneWidget`, `AppWidget`, and `ui.Host`) make this both ergonomic and fast:

1. **One Reusable Stateful `terminal` Per Parent (Wipe & Reuse):**
* Each parent owns a persistent virtual terminal instance (`vt`).
* To render a child widget into a slot of size `(w, h)`, the parent wipes/resets `vt` to `(w, h)`, feeds `child.Render(w, h)` into `vt`, and snapshots the resulting `CellBuf` (which now carries both the rendered `[]Cell` rows and the implicit `CursorX, CursorY, CursorVisible` extracted from `vt`!).


2. **Smart Caching on Settled Block Widgets (`Shell`):**
* Inside `Shell`, completed block widgets (`BannerWidget`, finished `CommandBlockWidget`s) don't even need `Shell` to re-wipe and re-run `vt` on every frame!


* `Shell` caches the resulting `CellBuf` alongside each block widget (keyed by `(width, outputLen, animLine)` or a `dirty` bool).


* On any normal frame (typing in `PromptInput` or streaming into the tail active block), `Shell` only wipes and runs its `vt` on the **changed** child (the active `CommandBlockWidget` and `PromptInput`), and stitches their `CellBuf` rows together with the cached `CellBuf`s of the visible non-ejected blocks!


* If the terminal width `w` changes (`SIGWINCH`), `Shell` simply loops over the non-ejected visible blocks, wiping and reusing that **single** `vt` instance at the new width `w` to refresh their cached `CellBuf`s.




3. **2D Composition in `AppWidget` & `DualPaneWidget`:**
* `DualPaneWidget` wipes its `vt` to `(halfW, h)`, feeds `leftPane.Render(halfW, h)`, blits those cells into columns `0 .. halfW-1`, then wipes `vt` again, feeds `rightPane.Render(halfW, h)`, and blits into columns `halfW .. w-1`.


* `AppWidget` renders `Shell` into its base `CellBuf`, and when `panelsVisible == true` (in native TUI mode), wipes its `vt` to `(w, H_panels)`, feeds `panels.Render(w, H_panels)`, and blits the result right over rows `0 .. H_panels-1` (and same for a centered modal `Dialog`)!





---

### Why This Redesign Is a Clean Sweep

| Aspect | Old Design | New Unified Design |
| --- | --- | --- |
| **`Widget.Render` Signature** | `Render(Rect, RenderContext) (CellBuf, CursorPos)` | `Render(w, h int) string` (carries text, ANSI, and cursor implicitly) |
| **Leaf Widget Code** | Manual `CellBuf` cell indexing + manual `CursorPos` coordinate math | Standard Go `strings.Builder` + ANSI styles + `\x1b[s`/`\x1b[u` for cursor |
| **`CommandBlock` vs `Widget**` | Custom struct with ad-hoc rendering in `Shell` | First-class `Widget` in `Shell`'s widget list (alongside `Banner`, `Wizard`, `PromptInput`) |
| **VT Emulator Overhead** | New emulator allocated on every `terminal.FromANSI` call | **1 cached & re-wiped `terminal` per parent**, plus cached `CellBuf` for settled blocks |
| **2D Composition & Diffing** | `CellBuf` blitting + `ui.Host` `spanDetect()` diffing | **Kept 100% intact!** Parents compose `CellBuf`s extracted from their reusable `terminal`, and `ui.Host` diffs `currBuf` vs `prevBuf` |

## Key principles of Redesign

Here is what the codebase currently does on both points:

---

### 1. Focus State & `Invalidate` on `RenderContext`

Currently, the codebase is **inconsistent**—`RenderContext` exists and carries `Focused` and `Invalidate` (`ui/widget.go:30`), **but** individual widgets also duplicated focus booleans onto their own structs:

* **What `RenderContext` already has (`ui/widget.go:30`):**
  ```go
  type RenderContext struct {
      Focused    bool
      Invalidate InvalidateFunc
  }
  ```
* **Where widgets duplicated focus state on the struct instead of relying purely on `ctx.Focused`:**
  * `PromptInput` has `p.focused bool` on the struct (`Focus()` / `Blur()`), and checks `focused := p.focused && ctx.Focused`.
  * `FilePaneWidget` has `w.active bool` on the struct (`SetActive(bool)`), and `DualPaneWidget` calls `w.Left.RenderBuf()` without even passing `RenderContext`!
  * `widgets.TextInput` and `widgets.ButtonRow` added `focused bool` / `Focused bool` fields on the struct, and `DialogWidget` mutates `field.Input.SetFocused(...)` and `buttons.Focused = ...` before calling `Render(..., ctx)`.
  * Worse, `AppWidget.Render` passes the exact same incoming `ctx` (with `ctx.Focused == true`) down to `w.sh.Render(r, ctx)`, `w.panels.Render(..., ctx)`, and `dlg.Render(..., ctx)`—even when a modal dialog is open and `Shell` and `DualPaneWidget` do **not** have focus!

**Your rule makes this much cleaner:**
* Widgets should **not** store a `focused` / `active` boolean field on themselves.
* Instead, `Render` takes `ctx RenderContext` (e.g. `Render(r Rect, ctx RenderContext) string` or `Render(w, h int, ctx RenderContext) string`), and **only the parent decides who is focused** by passing `ctx.Focused` down to its children:
  * In `AppWidget`, focus in `Shell` reflects whether typing is expected and allowed in the shell prompt. Showing the blue file panels splits navigation keys (`Up`/`Down`/`PgUp`/`PgDn`/`Tab`) to the panels while printable typing continues into the shell prompt, so **both `Shell` and `DualPaneWidget` receive `ctx.Focused = (dlg == nil)`** when the panels are visible. The shell keeps its visible input cursor while the active file pane highlights its selection bar.
  * When a modal popup/dialog (`dlg != nil`) is open, it takes focus away from both the shell and the panels: `AppWidget` passes `ctx.Focused = false` to `Shell` and `DualPaneWidget`, and `ctx.Focused = true` to `dlg`.
  * `DualPaneWidget` passes `ctx.Focused = (ctx.Focused && w.activePane == 0)` to `Left` and `ctx.Focused = (ctx.Focused && w.activePane == 1)` to `Right`.
  * `DialogWidget` passes `ctx.Focused = (ctx.Focused && i == w.focusedField && !w.focusedButton)` to `field.Input.Render(...)`, and `ctx.Focused = (ctx.Focused && w.focusedButton)` to `buttons.Render(...)`.
* And `ctx.Invalidate` stays on `RenderContext` just as it is today.

---

### 2. How `Measure` Works Currently (Parent Queries Desired Size, Then Decides)

Yes—**what you described is the intended contract in `ui/widget.go:45`, and it is already used that way in some places, but bypassed or half-used in others:**

1. **Where parent queries `Measure(c Constraints) Size` and then decides:**
   * **`DialogWidget` inside `AppWidget` (`app_widget.go:473`):**
     `AppWidget` queries `sz := dlg.Measure(ui.Constraints{MaxW: shellBuf.Width, MaxH: r.H})`. Then `AppWidget` decides where to place the dialog (`dx`, `dy`) and what rectangle `ui.Rect{X: dx, Y: dy, W: sz.W, H: sz.H}` to pass into `dlg.Render(...)`.
   * **`ButtonRow` inside `DialogWidget` (`dialog.go:363`, `480`):**
     `DialogWidget` queries `buttons.Measure(Constraints{MaxW: innerW, MaxH: height})` to find out how many rows the buttons want (1 row if they fit, or 2+ rows if they wrap), and then `DialogWidget` decides the rectangle `Rect{X: 2, Y: btnY, W: innerW, H: ...}` to pass to `buttons.Render(...)`.
   * **`AppWidget.Measure` (`app_widget.go:416`):**
     Queries `w.sh.Measure(c)` and `dlg.Measure(...)` to report its desired size upward.

2. **Where `Measure` is currently ignored or duplicated:**
   * **`Host.runFrame` (`ui/host.go:455`):**
     Look at lines 455–459 of `ui/host.go`:
     ```go
     _ = h.root.Measure(Constraints{MaxW: w, MaxH: ht})
     rendered, cursor := h.root.Render(
         terminal.Rect{X: 0, Y: 0, W: w, H: ht},
         RenderContext{Focused: true, Invalidate: h.Invalidate},
     )
     ```
     `Host` calls `h.root.Measure(...)` and **throws away the return value (`_ =`)**, always passing the full terminal `W: w, H: ht` to `h.root.Render(...)`, and then inspects `rendered.Height` afterward!
   * **`Shell.Render` (`shell_widget.go:1285`):**
     In `Shell.Measure`, `Shell` calls `s.input.Measure(...)` (`shell_widget.go:1145`). But in `Shell.Render`, it doesn't call `s.input.Measure(...)`—it passes `inputMaxH` directly to `s.input.Render(...)` and reads `inputBuf.Height` after rendering.
   * **`DualPaneWidget` (`filepane_widget.go:1322`):**
     Doesn't call `Left.Measure` or `Right.Measure`; it splits `w.Width` in half in `Layout` and calls `Left.RenderBuf()` / `Right.RenderBuf()`.

So keeping `Measure(c Constraints) Size` on `Widget`:
```go
type Widget interface {
    Measure(c Constraints) Size
    Render(r Rect, ctx RenderContext) string // or (w, h int, ctx RenderContext) string
    HandleEvent(e input.Event) bool
}
```
preserves the exact two-step layout model you want:
1. **Query:** The parent calls `child.Measure(Constraints{MaxW, MaxH})` to ask the child its desired `Size{W, H}` within those constraints.
2. **Decide & Render:** The parent decides the final `(w, h)` (and position `(x, y)`) to allocate to the child, calls `child.Render(..., ctx)` with that chosen size and child `RenderContext`, feeds the returned ANSI string into its reusable `vt` sized to `(w, h)`, and blits the resulting cells into its own buffer. And in `Host.runFrame`, `Host` can actually use `size := h.root.Measure(...)` instead of discarding it with `_ =`.

## Proposed Alpha Channel (historical review; superseded by implementation update above)

> The child should indeed fill the space, and that's important. But now please review again the suggestion how we should treat reset background/reset foreground bits on composited child widgets in a very different way here: these would mean take background/foreground from corresponding cell of a buffer passed inside. Include the plan to treat resets correctly with this bulletproof two stage.

Treating the foreground and background reset bits (`CharResetForegroundDefault` / `\x1b[39m` and `CharResetBackgroundDefault` / `\x1b[49m`, plus `\x1b[0m`) in a child widget's final VT snapshot as **per-cell fallback/inheritance from a passed-in target `CellBuf**`—while the child fills its rectangular slot with its own characters—is the exact missing piece that makes ANSI-string widgets truly composable.

Below is the complete review of why this works across every layer of Kabibi, followed by the concrete **Two-Stage Reset-as-Fallback Composition Plan** ready to drop directly into `# Widgets Followups` in `12-dialogs.md`.

---

## 1. Architectural Review: Why This Design Works So Well

### A. The Core Problem with Standard ANSI `\x1b[0m` in Nested Widgets

In a traditional terminal stream, `\x1b[0m` (or `\x1b[39m` / `\x1b[49m`) means *"reset to the physical terminal emulator's global default foreground and background"*.

* That works fine at the root `Shell` level where you *want* the user's native terminal background/transparency to show through.


* However, inside a styled container—such as a `DialogWidget` (`#1A1F26` dark-gray surface, `#E5E5E5` text) or a `FilePaneWidget` (`#0000A8` blue panel)—if a child widget like `Checkbox`, `ButtonRow`, `ProgressBar`, or `TextInput` emits `\x1b[38;2;255;215;0m[x]\x1b[0m Label text`, a literal `\x1b[0m` punches a black hole straight through the dialog's `#1A1F26` surface back to the host terminal's default background.


* Without your rule, every child widget would have to be passed its parent's exact background/foreground RGB values and manually re-emit `\x1b[38;2;...;48;2;...m` instead of `\x1b[0m` after every styled span. Worse, a child could never sit over a non-uniform background (like a striped table row, a gradient, or a partially dimmed region).



### B. How "Reset = Inherit from Target Buffer Cell" Solves It

By redefining `CharResetForegroundDefault` and `CharResetBackgroundDefault` in the **final settled VT snapshot** to mean *"take `FG` / `BG` from the corresponding `(x, y)` cell of the destination buffer passed into the compositor"*:

1. **Child Widgets Can Freely Use `\x1b[0m`, `\x1b[39m`, and `\x1b[49m`:**
A `Checkbox` simply outputs `"\x1b[38;2;255;215;0m[x]\x1b[0m " + label`. It does not know or care whether it is being rendered inside a dark-gray `DialogWidget` (`#1A1F26`), a deep-blue `FilePaneWidget` (`#0000A8`), an inline `WizardWidget` on the default terminal surface, or a selected row.


2. **The Child Still Fills Its Rectangular Space `(W × H)`:**
Because the child fills its `(W, H)` slot (any cells not explicitly written by the child's string sit in the VT snapshot as `' '` with `FG: Reset, BG: Reset`), the child's characters (including spaces that clear out old text) completely replace the underlying runes in that rectangle, **while** any cell with a Reset `FG` or Reset `BG` automatically adopts the pre-seeded `FG` and `BG` of that exact cell `(x, y)` in the parent buffer!
3. **Why the Two-Stage Separation is Essential:**
As you pointed out, an ANSI stream inside a widget (especially `CommandBlockWidget` with `\r` tickers like `[goal]…` $\rightarrow$ `\r      \r`, or a widget using `\x1b[2K` / cursor moves) may traverse and overwrite the same cells multiple times during a single render pass.


* **Stage 1 (Isolated VT Interpretation):** The reusable VT starts cleanly wiped to `' '` with `FG: Reset, BG: Reset` across `(w, h)`. Every intermediate overwrite, `\r`, `\x1b[K`, SGR color change, and `\x1b[0m` reset is fully resolved by the VT state machine in isolation.


* **Stage 2 (Post-Snapshot Per-Cell Fallback Blit):** Only after the VT finishes parsing the entire string do we inspect each cell `(x, y)` of the final snapshot. If the final cell's `FG` is `Reset`, it inherits `dstBuf[dstX+x, dstY+y].FG`; if its `BG` is `Reset`, it inherits `dstBuf[dstX+x, dstY+y].BG`.



---

## 2. Updated Plan Section for `12-dialogs.md` (`# Widgets Followups`)

Here is the drop-in replacement/addition for the **Widgets Followups** section of your plan incorporating the Two-Stage Reset-as-Fallback Composition model:

```markdown
### 4. Two-Stage VT Composition & Per-Cell Reset Inheritance ("Alpha" Fallback)

When a parent widget composes a child widget's ANSI string into a target rectangle `r = Rect{X, Y, W, H}` of a parent `CellBuf` (`dstBuf`), **foreground and background reset bits (`CharResetForegroundDefault` / `CharResetBackgroundDefault`) in the child's final VT snapshot do not reset to the physical terminal default—they inherit the `FG` and `BG` of the corresponding underlying cell in `dstBuf`!**

This must be executed in **two strict stages** so that multi-pass ANSI streams (such as `\r` carriage-return overwrites, `\x1b[K` line clears, or color changes followed by `\x1b[0m`) resolve accurately before compositing onto the parent surface:

#### Stage 1: Full VT Interpretation in Isolation
1. The parent takes its cached, reusable virtual terminal (`vt`) and wipes/resizes it to `(r.W, r.H)`.
2. Every cell in `vt` is initialized to the clean VT default state:
   - `Rune: ' '`
   - `FG: CharResetForegroundDefault`
   - `BG: CharResetBackgroundDefault`
   - `Attrs: 0`
3. The parent invokes `ansiStr := child.Render(r, childCtx)` and feeds `ansiStr` completely through `vt`.
4. During this stage, `vt` processes all cursor jumps, line wraps, `\r` overwrites, `\x1b[K` erases, explicit colors, and `\x1b[0m` / `\x1b[39m` / `\x1b[49m` resets purely within its own isolated `(r.W, r.H)` grid.
   - If the stream writes colored text at `(0, 0)` and later overwrites `(0, 0)` after emitting `\x1b[0m` (or clears the line with `\x1b[0m\x1b[2K`), the cell at `(0, 0)` in `vt`'s final state returns to `FG: CharResetForegroundDefault, BG: CharResetBackgroundDefault`.
   - Any cell in `(r.W, r.H)` that the child's stream never touched remains `' '` with `FG: CharResetForegroundDefault, BG: CharResetBackgroundDefault`.

#### Stage 2: Final Snapshot Per-Cell Fallback Blit onto `dstBuf`
Once `vt` has completely finished interpreting the child's ANSI string, the parent snapshots/reads the final settled `(r.W, r.H)` grid from `vt` and composites it into `dstBuf` at `(r.X, r.Y)`:

For every local coordinate `(x, y)` in `0..r.W-1` × `0..r.H-1`:
1. Let `src := vt.Cell(x, y)` and `dst := dstBuf.Cell(r.X + x, r.Y + y)`.
2. **Space-Filling Character Replacement:**
   - The child fills its allocated `(r.W, r.H)` slot: `out.Rune = src.Rune` (where untouched cells in `vt` are `' '`, cleanly wiping any prior runes in `dst` inside the child's rectangle) and `out.Attrs = src.Attrs`.
3. **Per-Cell Foreground Fallback (`FG`):**
   - If `src.FG == CharResetForegroundDefault` (the child left foreground in reset/default state at this cell):
     `out.FG = dst.FG` (inherit the pre-seeded foreground from `dstBuf` at `(r.X + x, r.Y + y)`).
   - Otherwise (the child explicitly set a foreground color at this cell):
     `out.FG = src.FG`.
4. **Per-Cell Background Fallback (`BG`):**
   - If `src.BG == CharResetBackgroundDefault` (the child left background in reset/default state at this cell):
     `out.BG = dst.BG` (inherit the pre-seeded background from `dstBuf` at `(r.X + x, r.Y + y)`).
   - Otherwise (the child explicitly set a background color at this cell):
     `out.BG = src.BG`.
5. Write `out` to `dstBuf.SetCell(r.X + x, r.Y + y, out)`.
6. **Implicit Cursor Extraction:**
   - If `childCtx.Focused` is `true` and `vt.CursorVisible()` is `true` (e.g., the child emitted `\x1b[s` + `\x1b[?25h` ... `\x1b[u`), the parent records the translated cursor position `(r.X + vt.CursorX(), r.Y + vt.CursorY())` as its active cursor.

---

#### How Parents Use Pre-Seeding with Two-Stage Composition

1. **`DialogWidget` (Pre-Seeded Surface `#1A1F26` BG / `#E5E5E5` FG):**
   - `DialogWidget` allocates/wipes its `(W, H)` buffer pre-seeded with `FG: #E5E5E5, BG: #1A1F26` and draws its gold border and title.
   - When `DialogWidget` renders child micro-widgets (`TextInput`, `Checkbox`, `ButtonRow`, `ProgressBar`) into sub-rectangles `r`:
     - Those micro-widgets simply emit standard ANSI strings using `\x1b[0m` / `\x1b[39m` / `\x1b[49m` after any highlighted token, hotkey, or progress bar fill.
     - In Stage 2, every reset FG/BG cell in the child snapshot automatically inherits `#E5E5E5` and `#1A1F26` from the pre-seeded dialog buffer—with zero hardcoded dialog colors inside `kabibi/ui/widgets`!
2. **`FilePaneWidget` / `DualPaneWidget` (Pre-Seeded `#0000A8` Blue Surface or Selection Bar):**
   - A pane or row pre-seeded with `#0000A8` (or `#00AFAF` on the active cursor bar) can compose any child indicator or widget on top of it, and `\x1b[0m` inside the child seamlessly restores the exact per-cell panel or selection bar color underneath.
3. **`Shell` & `ui.Host` (Root Terminal Default Surface):**
   - At the root `Shell` level, the target buffer is pre-seeded with `FG: CharResetForegroundDefault, BG: CharResetBackgroundDefault`.
   - When `BannerWidget`, `CommandBlockWidget`, `WizardWidget`, or `PromptInput` emits `\x1b[0m`, Stage 2 resolves `Reset -> dst.FG (Reset)`, preserving `CharResetForegroundDefault` / `CharResetBackgroundDefault` in the root `CellBuf` so `terminal.ToANSI(subBuf)` emits real terminal resets (`\x1b[0m`) to the user's host terminal!

```

---

## 3. Returning From Composite Parents

Look at how a composite widget like `DialogWidget` or `DualPaneWidget` fits into `Widget`:

* Leaf widgets (`TextInput`, `Checkbox`, `ButtonRow`, `ProgressBar`, `PromptInput`, `BannerWidget`, `CommandBlockWidget`) build a `string` directly and return it from `Render(r Rect, ctx RenderContext) string`.


* A composite parent (`DialogWidget`, `DualPaneWidget`, `Shell`, `AppWidget`) pre-seeds its own `CellBuf`, runs its children's `Render(...) string` through its reusable `vt` (Stage 1), and blits them onto its pre-seeded `CellBuf` with per-cell reset inheritance (Stage 2).



When `DialogWidget.Render(r Rect, ctx RenderContext) string` finishes composing its `CellBuf` and returns a `string` to `AppWidget` (or when `AppWidget` returns to `ui.Host`), `terminal.ToANSI(composedBuf)` serializes that composed `CellBuf` (plus `\x1b[s` / `\x1b[u` for its translated cursor) into an ANSI string—or if `Render` returns a `CellBuf` / writes into a parent-supplied `CellBuf`, it doesn't even need the `ToANSI` step between layers!

Important: since `DialogWidget` fills its entire `(r.W, r.H)` rectangle with explicit `#1A1F26` BG and `#E5E5E5` FG, even if `DialogWidget.Render` returns `terminal.ToANSI(dialogBuf)` as a `string` to `AppWidget`, `AppWidget` will run Stage 1 + Stage 2 on `DialogWidget`'s string over `AppWidget`'s `shellBuf`, and every cell of the dialog has explicit non-reset RGB colors so it paints opaque `#1A1F26` right over the shell, while any deliberately reset border/shadow cell on the outer edge of a widget would inherit the underlying `shellBuf` cell!

## Reset Fg/Bg Assessment

The **core idea is sound**: interpret a child’s ANSI string in isolation, then treat default-color markers in its final cells as “inherit this color channel from the destination cell.” That gives the child an opaque rectangular footprint—its spaces replace underlying text—while allowing reset foreground and background channels to retain the surface beneath it, including a nonuniform surface.

That is a useful compositing rule, but the section needs a few corrections before it is a reliable implementation plan.

### What works

- The two stages correctly separate ANSI interpretation from composition. Carriage returns, erase sequences, SGR resets, and overwrites should be resolved by the VT **before** the final cells are composited.
- Foreground and background inheritance should be independent. A child can specify its text color while inheriting the panel background, or vice versa.
- Preserving reset markers at the root lets the final serialized frame still use the host terminal’s default colors.

### Important corrections

1. **This is default-color fallback, not an alpha channel.**  
   The markers mean “inherit from the destination” during composition; they do not represent arbitrary transparency or opacity. Calling the feature “reset-as-fallback” or “per-channel inheritance” would be more precise.

2. **The document uses the markers as though they were color values.**  
   `CharResetForegroundDefault` and `CharResetBackgroundDefault` are attribute bits in a cell’s style, not values that can be compared to `src.FG` or copied into `out.FG`. The compositor must inspect style attributes, then choose the source or destination color **and carry the corresponding reset flag**. If the destination channel is itself terminal-default, that reset status must remain set.

3. **It removes the ability to request a literal terminal-default color inside a composed widget.**  
   With this rule, a child’s reset foreground always inherits the destination foreground—even if the child intended to use the physical terminal’s default foreground. If that distinction matters, the representation needs separate states for **inherit** and **terminal default**; the current reset bit cannot express both intentions.

4. **The assumed full-size VT snapshot needs to be guaranteed.**  
   `Terminal.Snapshot` returns the emulator’s rendered extent, which can be shorter than the allocated height. The compositor must pad or otherwise normalize snapshots to the child’s allocated rectangle before applying the “child fills the rectangle” rule.

5. **The wide-character case needs explicit handling.**  
   Continuation cells for wide graphemes have empty graphemes, and `CellBuf.Blit` currently skips empty cells. A full-slot replacement must still clear or correctly preserve overlapping wide-character cells at the destination; blindly treating every empty grapheme as a space would also be wrong.

6. **Keep cursor composition separate from color fallback.**  
   The proposal’s cursor step relies on inspecting cursor visibility and coordinates from the isolated VT, not on the `CellBuf` itself; the current API returns those separately. A parent needs to retain the focused child’s translated cursor while it composes subsequent children.

### Assessment Summary

This is a plausible solution to the background-bleed problem **if** the contract is explicitly: “In a child snapshot, reset foreground/background means inherit that channel from the corresponding destination cell; the child replaces the destination’s grapheme throughout its allocated rectangle.” It is not a general alpha model, and the distinction between inheritance and literal terminal-default color must be a conscious choice.

I’d add tests for independent FG/BG fallback, nonuniform destination colors, nested composition, root-default preservation, resets and erase/overwrite sequences, exact rectangle sizing, and wide-character overlap before adopting it. No changes were made.

---

## Performance Update: Cached Terminals & Pre-Seeded Surfaces

The composition path reuses each parent's cached `terminal.Terminal`. `Terminal.Reset(width, height)` resizes the existing emulator only when dimensions change, cancels any incomplete ANSI sequence, performs an in-place RIS reset, restores Unicode-core and line-feed/newline modes, hides the cursor, and clears scrollback. This retains the VT parser and screen allocations between frames while discarding prior frame state.

Parent destination buffers are initialized with the surface colors used by reset-channel inheritance: dialogs use `FG: #E5E5E5, BG: #1A1F26`; file panes and the dual-pane surface use `FG: #FFFFFF, BG: #0000A8`; shell and host surfaces retain terminal-default FG/BG markers. Child VT snapshots begin with blank cells carrying reset-default FG/BG markers, which inherit from the corresponding pre-seeded destination cells during composition.

`CommandBlock` keeps its settled `CellBuf` cache keyed by width and content state (`Prompt`, `Input`, output length, live-output generation, animation state, and status). `Shell.renderStackBlocksLocked` now assembles those cached cells directly, avoiding the previous per-block `CommandBlock.Render` → ANSI serialization → VT parse round-trip on every frame. Dirty blocks and width changes still regenerate the cache; the public widget API remains ANSI-string rendering.

`BenchmarkShellRenderSettledBlocks` compares cached-cell assembly with the previous ANSI/VT round-trip using 100 settled blocks. On the recorded run, cached-cell assembly measured **4.22 µs/op, 5.1 KB/op, 3 allocs/op**, compared with **3.63 ms/op, 2.85 MB/op, 18,502 allocs/op** for the ANSI/VT path. These figures are benchmark-specific, but confirm the avoided work is substantial.