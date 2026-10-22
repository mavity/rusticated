# Kabibi Dual Blue File Manager HUD & Unified CellBuf Rendering Plan (`3-blue.md`)

## 1. Architectural Vision & The Unified `CellBuf` Rendering Model

There is **no separate "inline mode"** in Kabibi. From the very first startup frame to full-screen blue panels, modal dialogs, and the syntax-highlighted editor, `ui.Host` uses **one single rendering engine**:

1. **Widgets render into a 2D `terminal.CellBuf`:**
   - Before `Ctrl+O` is pressed (and while the session output is still compact), the root widget's rendered `CellBuf` simply has a smaller height $H_{\text{buf}} \le H_{\text{term}}$ (starting at $H_{\text{buf}} = H_{\text{banner}} + H_{\text{input}} + 1 = 5$ rows).
   - As commands run, output streams in, or `PromptInput` wraps across lines, $H_{\text{buf}}$ grows row by row ($5 \rightarrow 6 \rightarrow 7 \dots \rightarrow H_{\text{term}}$).
   - When `Ctrl+O` summons the blue panels (or once the stack naturally reaches $H_{\text{term}}$), $H_{\text{buf}} = H_{\text{term}}$.
2. **Anchored Where the Cursor Found It, Shifting Downward Until It Hits Bottom:**
   - When Kabibi starts, the occupied area of height $H_{\text{buf}}$ begins **wherever the terminal cursor was when the user launched Kabibi**—whether at the top of the screen, in the middle, or near the bottom.
   - Because `Host` positions relative to its tracked cursor row (`prevCursorY`) within the active `CellBuf` and only emits `\r\n` when advancing onto newly claimed rows at the bottom of the growing `CellBuf`, the occupied area naturally **extends and shifts lower and lower on each turn**:
     - If the user started near the top or middle of the terminal, each new line of output extends the bottom of the occupied area downward toward the physical bottom of the terminal window without disturbing external lines above the starting point.
     - Whenever a newly claimed row at the bottom of the growing `CellBuf` reaches the physical bottom row of the terminal window, the terminal's native `\r\n` linefeed scrolls the window upward by 1 row, shifting the occupied area's top anchor down in screen space until the occupied area occupies the full $H_{\text{term}}$ rows at the bottom of the screen.
     - Once $H_{\text{buf}} = H_{\text{term}}$ (the occupied area has reached its maximum height and rests at the bottom of the terminal), any further growth ejects the oldest top rows out of the stack via `WriteToScrollback`, keeping the active viewport anchored at the bottom.
### The Single Unconditional Law of Bottom Gravity (Zero Special Cases)

Let us trace **why** that happens physically in a terminal emulator so we don't invent ad-hoc rules:

Suppose the terminal is $H_{\text{term}} = 24$ rows tall, and our stack has already reached the bottom of the screen (`allocHeight = 24`), with 22 rows of non-ejected blocks + 1 `PromptInput` row + 1 blank footer row ($22 + 1 + 1 = 24$).

Now the user types a long command into `PromptInput`, so `PromptInput` wraps from $1 \rightarrow 4$ rows ($H_{\text{chrome}}$ grows from $2 \rightarrow 5$):
- Because `PromptInput` is at the bottom of the 24-row screen, expanding `PromptInput` by 3 rows **pushes the top 3 rows of the screen out the top of the terminal into native scrollback** (`previouslyEjected` advances by 3, leaving $22 - 3 = 19$ non-ejected block rows on screen above the 4-row `PromptInput` + 1 footer row: $19 + 4 + 1 = 24$).
- Next, the user **deletes** that long input text so `PromptInput` shrinks back from $4 \rightarrow 1$ row ($H_{\text{chrome}}$ returns from $5 \rightarrow 2$)!
- How many non-ejected rows do we have left on screen now?
  - Those 3 rows that were pushed into native terminal scrollback when the input wrapped **cannot be pulled back out of the terminal emulator's scrollback**!
  - So we now have only $19$ non-ejected block rows + $1$ `PromptInput` row + $1$ footer row = **$21$ occupied rows** inside our $24$-row allocated screen ($H_{\text{occ}} = 21 < H_{\text{alloc}} = 24$).
  - That opens a **3-row slack gap at the bottom** of the screen (rows `21, 22, 23` are now blank below the footer row `20`), and `PromptInput` has been pulled **up** to row `19`!
- Now the user runs a command that prints new output lines:
  - Because $H_{\text{occ}} = 21 < H_{\text{term}} = 24$, the new output lines increase $H_{\text{unejected}}$ from $19 \rightarrow 20 \rightarrow 21 \rightarrow 22$, which pushes `PromptInput` and the footer **downward** across rows `21, 22, 23` (gravitating back down to the bottom of the screen) with **zero scrollback ejection** until $H_{\text{occ}}$ reaches $24$ again!

Notice that this means we do **not** need a collection of special-case rules ("before hitting bottom vs. after hitting bottom", "input wrap vs. command output"). Everything is governed by **one unconditional mathematical law** on every frame:

#### The Three Equations of Bottom Gravity

Let:
- $H_{\text{blocks}}$ = total visual rows of all `s.blocks` at width $W$.
- $E$ (`previouslyEjected`) = monotonic watermark of block visual rows already ejected into native terminal scrollback.
- $H_{\text{chrome}} = H_{\text{input}} + 1$ = current height of `PromptInput` + the 1 blank footer row.
- $H_{\text{budget}} = \max(0,\ H_{\text{term}} - H_{\text{chrome}})$ = maximum block rows that can fit on the terminal screen alongside the current `PromptInput` and footer.

On **every** frame, without exception:

1. **Unconditional Watermark Advance (When Total Stack Exceeds $H_{\text{term}}$):**
   $$E_{\text{next}} = \max\big(E,\ H_{\text{blocks}} - H_{\text{budget}}\big)$$
   - If $E_{\text{next}} > E$, the $\Delta_{\text{eject}} = E_{\text{next}} - E$ rows $[E : E_{\text{next}})$ are emitted via `WriteToScrollback` and $E \leftarrow E_{\text{next}}$.
   - If $H_{\text{blocks}} - E \le H_{\text{budget}}$, then $E_{\text{next}} = E$ and **nothing is ejected**.

2. **Occupied Height vs. Allocated Canvas Height (Automatic Downward Gravity):**
   - After step 1, the remaining non-ejected block rows are:
     $$H_{\text{unejected}} = H_{\text{blocks}} - E \le H_{\text{budget}}$$
   - The occupied height of the stack is:
     $$H_{\text{occ}} = H_{\text{unejected}} + H_{\text{chrome}} \le H_{\text{term}}$$
   - And the allocated terminal canvas height is:
     $$H_{\text{alloc}} = \min\big(H_{\text{term}},\ \max(H_{\text{alloc\_prev}},\ H_{\text{occ}})\big)$$
   - Look at what happens automatically in **every** scenario under these two equations:
     - **At startup:** $H_{\text{occ}} = 5$, $H_{\text{alloc}} = 5$. As output is generated, $H_{\text{occ}}$ and $H_{\text{alloc}}$ grow together ($5 \rightarrow 6 \rightarrow \dots \rightarrow H_{\text{term}}$), shifting the prompt lower and lower until $H_{\text{alloc}} = H_{\text{term}}$.
     - **Wrapping input then deleting it BEFORE hitting the bottom** (e.g. $H_{\text{occ}}: 5 \rightarrow 8 \rightarrow 5$, so $H_{\text{alloc}} = 8$): rows `5..7` of `currBuf` are blank ($G = 3$). New output increases $H_{\text{occ}}$ from $5 \rightarrow 6 \rightarrow 7 \rightarrow 8$, gravitating downward into rows `5..7` while $E_{\text{next}} = E$ (zero ejection).
     - **Wrapping input then deleting it AFTER hitting the bottom** (e.g. $H_{\text{alloc}} = 24$; wrapping input to 4 rows advances $E$ by 3 so $H_{\text{unejected}} = 19$; deleting the input shrinks $H_{\text{chrome}}$ back to 2 so $H_{\text{occ}} = 19 + 2 = 21 < 24$): rows `21..23` of `currBuf` are blank ($G = 3$). When new output arrives, $H_{\text{blocks}} - E$ grows from $19 \rightarrow 20 \rightarrow 21 \rightarrow 22 \le H_{\text{budget}} (22)$, so $E_{\text{next}} = E$ (zero ejection!) and $H_{\text{occ}}$ grows from $21 \rightarrow 22 \rightarrow 23 \rightarrow 24$, **gravitating back down to the bottom of the screen**!

3. **One-Time Span Diff Redraw (`spanDetect` + `ToANSI(subBuf)`):**
   - Every frame—regardless of whether $H_{\text{alloc}} = 5$ or $H_{\text{alloc}} = H_{\text{term}}$—`Host.runFrame` copies the rendered `CellBuf` into `h.currBuf` (`W × H_alloc`, with any trailing rows `[H_occ : H_alloc)` blank), runs `h.spanDetect()` against `h.prevBuf` to find the single minimal dirty row span `[lineMin, lineMax]`, moves relatively from `prevCursorY` to `lineMin`, serializes `terminal.NewSubBuf(h.currBuf, lineMin, lineMax-lineMin+1)` **once** via `terminal.ToANSI(subBuf)`, positions the cursor at `(cursorX, cursorY)`, and swaps `currBuf` and `prevBuf` in $O(1)$.

---

## 2. Step-by-Step Mechanics of the Unified `CellBuf` & Blue Panel Illusion

### 2.1 Phase A: Startup & Natural Downward Growth (Before `Ctrl+O`)

1. **Initial Footprint Where the Cursor Starts:**
   - Suppose the user launches `kabibi` when the terminal cursor is on physical screen row $R_0$ (which may be row 0 at the top, row 10 in the middle, or row 22 near the bottom of a 24-row terminal).
   - On frame 1, `Shell` renders a `CellBuf` of height $H_{\text{occ}} = 5$:
     - Rows `0..2`: `BlockBanner` (3 rows)
     - Row `3`: `PromptInput` (`~/path፧`)
     - Row `4`: Reserved blank footer row (1 row)
   - `Host` sets `allocHeight = 5`, allocates `currBuf` and `prevBuf` of size $W \times 5$, and emits `ToANSI(currBuf)` once starting at the current cursor line.
   - If $R_0 + 5 \le H_{\text{term}}$, the 5-row box sits right at rows $R_0 \dots R_0 + 4$. If $R_0$ was near the bottom (e.g., row 22 of 24), the 4 `\r\n` linefeeds inside `ToANSI(currBuf)` naturally scroll the terminal up by 2 rows so the 5-row box fits at the bottom (`rows 19..23`).
2. **Single-Pass Diff Redraws Within `allocHeight`:**
   - When the user types characters on the prompt row (`row 3`), `spanDetect()` finds `lineMin = 3, lineMax = 3`.
   - `Host` moves from `prevCursorY` (`3`) to `lineMin` (`3`, 0 vertical move), emits `\r` + `ToANSI(NewSubBuf(currBuf, 3, 1))`, and places the cursor at `cursorX`.
3. **Growing Downward & Closing the Slack Gap First:**
   - When a command runs and produces 2 lines of output, $H_{\text{occ}}$ grows from $5 \rightarrow 8$ rows (`allocHeight` grows from $5 \rightarrow 8$).
   - If the user previously wrapped `PromptInput` to 3 rows (`allocHeight = 7`) and deleted the text back to 1 row (`H_occ = 5`), `currBuf` and `prevBuf` retain height `allocHeight = 7` with rows `5..6` filled with blank cells (the 2-row slack gap $G = 2$).
   - When subsequent output increases $H_{\text{occ}}$ from $5 \rightarrow 7$, it fills rows `5..6` of the existing 7-row `CellBuf` first (pushing down into the slack gap with zero scrollback ejection).
   - Only when $H_{\text{occ}}$ grows past `allocHeight` (up to $H_{\text{term}}$) does `allocHeight` increase, stepping onto new lines below via `\r\n`.
4. **Hitting the Bottom & Ejecting Upward:**
   - Once `allocHeight` reaches $H_{\text{term}}$, the occupied area fills the entire terminal height and is physically at the bottom of the window.
   - From that point on, whenever total non-ejected block rows exceed $H_{\text{term}} - H_{\text{chrome}}$, `Shell.Render` ejects the overflowing top rows $[\text{previouslyEjected} : \text{targetEjected}]$ via `WriteToScrollback`, and `Host` scrolls the terminal by those ejected lines before diff-rendering the updated $W \times H_{\text{term}}$ `CellBuf`.

---

### 2.2 Phase B: The Five Mechanics of the Blue Panel Illusion (`Ctrl+O`)

The spatial illusion of the dual blue panels works by combining that exact same `CellBuf` diff engine with a one-time full-height expansion when `Ctrl+O` is pressed:

```text
┌───────────────────────────┬───────────────────────────┐ ▲
│ Left Panel                │ Right Panel (Active/CWD)  │ │ Rows 0 .. H_panels-1
│ ..                        │ ..                        │ │ Over-painted by DualPaneWidget
│ ▶ build.sh*               │   main.go                 │ │ (Behind this: hidden block rows)
└───────────────────────────┴───────────────────────────┘ ▼
│ KABIBI // RUSTICATED COMPUTING SUPERVISOR v1.0        │ ▲
│ 64K CELL MATRIX OK • MVDAN/SH + LITERT-LM READY       │ │ Rows H_panels .. H_term-1
│ Kabibi shell initialized. Type commands below.        │ │ Bottom Gap (H_gap = 5 rows):
│ ~/rustica፧ _                                          │ │ Visible tail of Shell stack
│                                                       │ ▼ + PromptInput + 1 Blank Footer
```

1. **The Perfect Fit at the Bottom ($H_{\text{gap}}$):**
   - On startup, Kabibi's `BlockBanner` (3 rows), `PromptInput` (`~/path፧`, 1 row), and reserved blank footer row (1 row) occupy $H_{\text{gap}} = 5$ rows (or up to $3 + H_{\text{input}} + 1$ if `PromptInput` is currently wrapped, capped at $\lfloor 0.40 \times H_{\text{term}} \rfloor$).
   - When the user hits `Ctrl+O` to summon the dual blue panels, `DualPaneWidget` hangs down from row 0 at the top of the `CellBuf` with height:
     $$H_{\text{panels}} = H_{\text{term}} - H_{\text{gap}}$$
   - The uncovered bottom gap of height $H_{\text{gap}}$ beneath the blue panels **exactingly matches** the height of the initial banner + prompt + footer stack.
2. **The Ejection & Slapped-On-Top Illusion:**
   - To claim the full screen height $H_{\text{term}}$ for the panels + bottom stack when `allocHeight < H_term`, `Host` advances `allocHeight` from its current height to $H_{\text{term}}$ by emitting $H_{\text{term}} - \text{allocHeight}$ linefeeds (`\r\n`) at the bottom of the current occupied region.
   - This pushes any prior external terminal lines all the way up into native terminal scrollback above row 0.
   - In the very same flush, `AppWidget` renders a $W \times H_{\text{term}}$ `CellBuf` where `DualPaneWidget` paints rows $0 \dots H_{\text{panels}}-1$ and `BlockBanner` + `PromptInput` + footer sit in rows $H_{\text{panels}} \dots H_{\text{term}}-1$.
   - Because the blue panels immediately paint over the top $H_{\text{panels}}$ rows and Kabibi's banner sits snugly in the gap directly underneath them, the user never sees a blank void—visually, it looks as though the blue panels were simply slapped *on top* of the terminal like a floating HUD.
3. **Sliding Underneath the Panels (The Hidden Buffer):**
   - While `panelsVisible == true`, `Shell` renders into the full $W \times H_{\text{term}}$ `CellBuf` with its non-ejected block slice positioned so the latest rows sit directly above `PromptInput` in the bottom gap:
     - If $H_{\text{collected}} < H_{\text{term}} - H_{\text{chrome}}$, the collected block rows occupy rows:
       $$Y = (H_{\text{term}} - H_{\text{chrome}} - H_{\text{collected}}) \dots (H_{\text{term}} - H_{\text{chrome}} - 1)$$
     - `AppWidget` then blits `DualPaneWidget` over rows $0 \dots H_{\text{panels}}-1$ of that same `CellBuf`.
   - As the user runs commands in the shell below the panels, new output lines enter just above `PromptInput` and push `BlockBanner` and older lines **upward into rows $0 \dots H_{\text{panels}}-1$** (underneath the blue panels).
   - Because those rows are still within the $H_{\text{term}} - H_{\text{chrome}}$ block budget of the full-screen `CellBuf`, **`Shell` does not eject anything to `WriteToScrollback` yet**—the lines are simply climbing through the masked vertical territory hidden behind `DualPaneWidget`.
4. **Catching Up to the Top:**
   - Only after the user generates enough shell output to completely traverse the height of the blue panels ($H_{\text{unedjected}} > H_{\text{term}} - H_{\text{chrome}}$, so the oldest non-ejected block row reaches row 0 at the physical top of the screen) does `hTarget = totalBlockRows - (H_term - chromeH)` exceed `previouslyEjected`.
   - From that point on, overflowing lines at row 0 eject into native terminal scrollback via `WriteToScrollback`, while `Host` redraws the $W \times H_{\text{term}}$ `CellBuf` with the blue panels remaining stationary on rows $0 \dots H_{\text{panels}}-1$.
5. **Withdrawing the Panels (`Ctrl+O` Again) — Same Top-Anchored Rule:**
   - When the user withdraws the blue panels (`panelsVisible` becomes `false`), we follow the **exact same top-anchored rule** as when panels were never opened:
     - The oldest non-ejected block row (`BlockBanner` or whatever row is at `previouslyEjected`) anchors right at **row 0** (immediately below the latest untouched row in native terminal scrollback, which was pushed above row 0 when `Ctrl+O` first opened the panels).
     - All non-ejected blocks + `PromptInput` + the 1 blank footer row stack **top-down from row 0 toward the bottom** ($0 \dots H_{\text{occ}}-1$), and any remaining rows below the footer ($H_{\text{occ}} \dots H_{\text{term}}-1$) are blank slack rows at the bottom!
     - As new output is generated with panels hidden, the stack grows **downward** from row $H_{\text{occ}}$ toward row $H_{\text{term}}-1$ (closing the bottom slack space first); once $H_{\text{occ}}$ hits the bottom of the screen ($H_{\text{term}}$), further output ejects upward into native scrollback just like normal.
   - **Why this unifies everything:**
     - **Panels OFF (`!panelsVisible`):** The stack is **always top-aligned at row 0** (anchored directly below the latest untouched row in terminal scrollback) and grows downward toward the bottom (`0 .. H_occ-1`), with any slack rows at the bottom (`H_occ .. allocHeight-1`).
     - **Panels ON (`panelsVisible`):** The non-ejected stack is **bottom-aligned** (`(H_term - H_chrome - H_collected) .. H_term-1`) so its tail peeks out in the gap below the blue panels while its older rows climb upward behind `DualPaneWidget` (`0 .. H_panels-1`) toward row 0!

---

## 3. Unified `ui.Host` Frame & Diff Engine Specification (`kabibi/ui/host.go`)

To eliminate `inlineMode` completely and have **one** `CellBuf` diff path in `ui.Host`:

### 3.1 State Tracked by `ui.Host`

```go
type Host struct {
    root Widget

    currBuf terminal.CellBuf // W x allocHeight
    prevBuf terminal.CellBuf // W x allocHeight

    allocHeight       int  // Current allocated height of the active region (1 .. ht)
    cursorX, cursorY  int  // Target cursor position within currBuf (0 <= cursorY < allocHeight)
    cursorVisible     bool
    prevCursorX       int
    prevCursorY       int  // Current physical cursor row relative to top of currBuf (0 .. allocHeight-1)
    prevCursorVisible bool
    hasRenderedOnce   bool
    ...
}
```

### 3.2 Relative Cursor Motion Helper (`moveToRow`)

Because the active region of height `allocHeight` starts wherever the cursor was on startup (and may be smaller than $H_{\text{term}}$ before `Ctrl+O`), `Host` never emits absolute screen row jumps (`\x1b[row;1H`), which would jump to the physical top of the terminal window instead of row 0 of `currBuf`. Instead, moving from `fromRow` to `toRow` inside `currBuf` is always relative:

- If `toRow < fromRow`: emit `\x1b[%dA` (`fromRow - toRow`) and `\r`.
- If `toRow > fromRow`: emit `\x1b[%dB` (`toRow - fromRow`) and `\r`.
- If `toRow == fromRow`: emit `\r`.

### 3.3 Growing `allocHeight` Downward (`growAllocation`)

When `rendered.Height > h.allocHeight` (or on frame 1 when `h.allocHeight == 0`):
- Let `newAlloc = min(ht, rendered.Height)`.
- On frame 1 (`!h.hasRenderedOnce`):
  - Allocate `currBuf` and `prevBuf` of size `w x newAlloc`.
  - Copy `rendered` into `currBuf`.
  - Emit `\r\x1b[2K` + `terminal.ToANSI(currBuf)` once (which uses `\r\n` between rows `0 .. newAlloc-1`, naturally extending the occupied area downward from the starting cursor row and scrolling the terminal only if it touches the bottom of the screen).
  - Move from row `newAlloc - 1` to `(cursorX, cursorY)`, swap `currBuf`/`prevBuf`, and set `h.allocHeight = newAlloc`.
- On subsequent frames when `newAlloc > h.allocHeight`:
  - First move from `h.prevCursorY` to the bottom of the existing allocated region (`h.allocHeight - 1`).
  - Emit `newAlloc - h.allocHeight` linefeeds (`\r\n`) to open the new bottom rows in the terminal (if the region is already at the bottom of the terminal window, this scrolls the window up so the region shifts down to claim those rows).
  - Our cursor is now at row `newAlloc - 1` of the expanded region.
  - Resize `currBuf` and `prevBuf` to `w x newAlloc`:
    - If `rendered.Height == ht` because `Ctrl+O` jumped straight from a compact stack (`oldAlloc < ht`) to full-screen (`newAlloc = ht`), clear `prevBuf` so the entire `w x ht` buffer is painted in one `ToANSI(subBuf)` pass.
    - Otherwise (normal stack growth by $\Delta$ rows), copy the existing `oldAlloc` rows of `prevBuf` into the top `oldAlloc` rows of the resized `prevBuf`.
  - Update `h.prevCursorY = newAlloc - 1` and `h.allocHeight = newAlloc`.

### 3.4 Single-Pass `spanDetect()` + `emitDirtyRegion(lineMin, lineMax)`

Once `currBuf` (`w x allocHeight`) is populated with `rendered` (and any slack rows `[rendered.Height : allocHeight)` are filled with blank cells):

1. Run `lineMin, lineMax, changed := h.spanDetect()`.
2. If `changed`:
   - Move relatively from `h.prevCursorY` to `lineMin` (`h.moveToRow(h.prevCursorY, lineMin)`).
   - Clear the starting line if `lineMin == lineMax` (`\x1b[2K`), and emit the single contiguous dirty sub-buffer **once**:
     ```go
     subBuf := terminal.NewSubBuf(h.currBuf, lineMin, lineMax-lineMin+1)
     h.out.WriteString(terminal.ToANSI(subBuf))
     ```
   - Because `ToANSI(subBuf)` emits `lineMax - lineMin` `\r\n` transitions, the terminal cursor is now at row `lineMax`.
   - Move relatively from `lineMax` to `(h.cursorX, h.cursorY)` and set cursor visibility (`\x1b[?25h` / `\x1b[?25l`).
   - Swap `h.currBuf` and `h.prevBuf` via `terminal.SwapBufCells(&h.currBuf, &h.prevBuf)`.
3. If `!changed && cursorChanged`:
   - Move relatively from `h.prevCursorY` to `(h.cursorX, h.cursorY)` and update cursor visibility.

### 3.5 Unified Scrollback Ejection (`WriteToScrollback`)

When `allocHeight == ht` and `Shell.Render` ejects $\Delta$ overflowing rows at the top of the stack:
1. Move from `h.prevCursorY` to row `0` (top of the viewport).
2. Clear to end of screen (`\r\x1b[2K\x1b[J`), emit `terminal.ToANSI(scrollBuf)` + `\r\n`, and then emit `terminal.ToANSI(h.currBuf)` (which advances the terminal scrollback by `scrollBuf.Height` rows and paints the new `w x ht` frame in one pass).
3. Position the cursor at `(h.cursorX, h.cursorY)` from row `ht - 1` and sync `h.prevBuf`.

---

## 4. Current Codebase Inventory & Gaps to Bridge

| Package / File | Current State | What Needs to Be Done |
|----------------|---------------|-----------------------|
| `kabibi/ui/host.go` | Currently has a split between an `inlineMode` branch (`renderInlineDiff` / `renderInlineRows`) and the standard `spanDetect` + `emitDirtyRegion` branch. | Remove `inlineMode`, `renderInlineDiff`, and `renderInlineRows`. Unify `runFrame` and `runScrollbackFrame` around the single `allocHeight`-aware `CellBuf` + `spanDetect()` + one-time `ToANSI(subBuf)` diff redraw described in §3. |
| `kabibi/ui/shell/shell_widget.go` | Supports `ModeStack` and `ModePlumeHistory`; always places collected blocks starting at row `0`. | Add `bottomAligned bool` (`SetBottomAligned(bool)`) and `OnCommandFinished func(cwd string)`: when `panelsVisible == true` (`bottomAligned == true`), place collected blocks immediately above `PromptInput` (`row = (maxH - chromeH) - collectedRows`) so they slide behind the blue panels; when `panelsVisible == false` (`bottomAligned == false`), place collected blocks at `row = 0` (anchored to the latest untouched row in terminal scrollback) and add downward toward the bottom! |
| `kabibi/ui/filemanager/filepane_widget.go` | Has `FilePaneWidget` and `DualPaneWidget` with multi-column pagination (`innerW / 18`), `..` parent entry, sorting, selection bar, item marking (`•`), and single-column divider `│`, but uses legacy `tea.KeyMsg` and `Cell{R, FG, BG}`. | Port to canonical `ui.Widget` (`terminal.CellBuf`, `terminal.Cell`, `ui.FilePanelStyle` / `ui.SelectedFileStyle` from `ui/styles.go`, and `github.com/charmbracelet/x/input.Event`). |
| `kabibi/ui/filemanager/fileops.go` | Complete recursive `copyPath`, `movePath`, `deletePath`, byte-rate progress accounting (`opSink`), and overwrite collision resolution. | Decouple from `tea.Cmd` / `tea.Msg`; drive progress updates via callbacks + `Invalidate()` on `AppWidget`. |
| `kabibi/ui/dialog/dialog.go` & `kabibi/ui/editor/editor.go` | Modal dialogs (`mkdir`, `rename`, `copy`, `move`, `delete`, mask) and Chroma-highlighted editor written against `tea.KeyMsg` / `lipgloss`. | Port to native `ui.Widget` (`Measure`, `Render`, `HandleEvent`) so modals and `F4` editor composite cleanly into `AppWidget`'s `CellBuf`. |
| `kabibi/ui/app/app_widget.go` | Thin wrapper around `sh *shell.Shell`. | Upgrade `AppWidget` into the composite coordinator owning `sh *shell.Shell`, `panels *filemanager.DualPaneWidget`, `panelsVisible bool`, `fullScreenClaimed bool`, and Far Manager split key routing. |

---

## 5. Far Manager Split Key-Routing & Executable Launching

### 5.1 Complete Input Routing Matrix (`AppWidget.HandleEvent`)

When no modal dialog or full-screen editor is open:

1. **If `Shell.IsRunning()` is `true`:**
   - All key and paste events route directly to `Shell.HandleEvent` (`Ctrl+C` cancels the running command/AI turn; other keys forward to `es.WriteActivePTY` if an interactive PTY child is running).
2. **If `Ctrl+O` is pressed (`keyStr == "ctrl+o"`):**
   - Toggles `w.panelsVisible = !w.panelsVisible`.
   - Sets `w.sh.SetBottomAligned(w.panelsVisible)`:
     - When `w.panelsVisible == true`, `Shell` bottom-aligns the stack into thefull `H_term` viewport so the newest rows sit in the gap below the panels and older rows climb behind the panels.
     - When `w.panelsVisible == false`, `Shell` top-aligns the stack at row `0` (immediately below the latest untouched row in terminal history) and grows downward toward the bottom, closing any blank space below before ejecting upward!
   - On first open, syncs the active pane's directory to `w.sh.ExecutionSession().Dir()`.
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
| **`enter`** | `!hasInput` | **Active Panel Action (see §5.2 below):**<br>• If `..` or directory: step into directory (`LoadDir`) and sync `ExecutionSession` `cwd`.<br>• If executable file (or `.sh` / shebang / `PATHEXT` / mode `0111`): execute `./<name>` in `Shell`!<br>• Otherwise: no-op (or open in editor if configured). |
| **`f2` / `f4` / `f5` / `f6` / `f7` / `f8` / `delete`** | Always | Trigger file manager operation (`Rename`, `Edit`, `Copy`, `Move`, `Mkdir`, `Delete`). |
| **Printable characters, `space`, `backspace`** | Always | Route to `PromptInput` via `w.sh.HandleEvent(e)` so typing a command works seamlessly at any time. |

### 5.2 Pressing `Enter` on an Executable in the Active Panel

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
     - Format the invocation command as `./<quotedName>`.
     - Set `w.sh.SetInput("./" + quoteIfNeeded(item.name))` and trigger `w.sh.HandleEvent(KeyPressEvent{Code: KeyEnter})` so the executable runs as a real `BlockCommand` in the shell stack below the panels (with full PTY support and output streaming underneath the panels)!

### 5.3 Bidirectional CWD Synchronization (`ExecutionSession` $\leftrightarrow$ Active Panel)

1. **`ExecutionSession.SetDir(dir string)`:**
   - Add a thread-safe `SetDir(dir string) error` method on `ExecutionSession` that cleans `dir`, verifies it is an existing directory via `os.Stat`, and updates `es.runner.Dir = dir` (and `PWD` in `es.runner.Vars`).
   - Whenever the user navigates directories inside the active blue panel (`Enter` on a folder or `..`), or presses `Tab` to switch the active panel between Left and Right, `AppWidget` calls `es.SetDir(activePane.Dir())`.
   - Because `PromptInput`'s prompt closure reads `es.Dir()`, the bottom prompt (`~/new/path፧`) updates instantaneously.
2. **Shell $\rightarrow$ Active Panel Hook (`OnCommandFinished`):**
   - Add an optional `OnCommandFinished func(cwd string)` callback to `ShellOptions` (invoked when a shell command or AI turn completes).
   - When `cd foo`, `mkdir bar`, `rm baz`, or an AI file tool finishes, `AppWidget` receives `OnCommandFinished(newCwd)`:
     - If `newCwd != activePane.Dir()`, the active pane loads `newCwd`.
     - Both Left and Right panes reload their directory entries (preserving their current selected filename if it still exists) so any newly created/deleted files appear immediately in the blue panels.

---

## 6. Phased Implementation Roadmap

### Phase 1: Unify `ui.Host` Around One `CellBuf` + `spanDetect` Diff Engine (`kabibi/ui/host.go`)
1. Remove `inlineMode`, `renderInlineDiff`, and `renderInlineRows` from `kabibi/ui/host.go`.
2. Implement the unified `allocHeight`-aware `CellBuf` diff engine (§3):
   - Starts at the current cursor location with `allocHeight = rendered.Height`.
   - Grows `allocHeight` downward via `\r\n` as `rendered.Height` increases (shifting the occupied region down until it hits the bottom of the terminal at `allocHeight == ht`).
   - Uses `spanDetect()` + a single `ToANSI(NewSubBuf(currBuf, lineMin, lineMax-lineMin+1))` write positioned relatively via `moveToRow` on every frame.

### Phase 2: Port `FilePaneWidget` & `DualPaneWidget` to Native `ui.Widget` (`kabibi/ui/filemanager/`)
1. Replace legacy `tea.KeyMsg` and `Cell{R, FG, BG}` in `kabibi/ui/filemanager/filepane_widget.go` with canonical `ui.Widget` (`Measure(ui.Constraints) ui.Size`, `Render(ui.Rect, ui.RenderContext) (ui.CellBuf, ui.CursorPos)`, `HandleEvent(input.Event) bool`).
2. Use `terminal.Cell` and the palette in `kabibi/ui/styles.go`, adding `ExecutableStyle` (bright green `#00FF87` on `#0000A8` blue, with `*` indicator).
3. Preserve multi-column pagination (`numCols = max(1, innerW / 18)`), `..` parent navigation (focusing the previous child folder name when ascending via `..`), and the single-column `│` divider between Left and Right panes.
4. Update `kabibi/ui/filemanager/filepane_widget_test.go` so all unit tests pass against `ui.Widget` and `terminal.CellBuf`.

### Phase 3: Upgrade `Shell` for Bottom-Aligned Full-Screen Stack & CWD Sync (`kabibi/ui/shell/`)
1. Add `SetBottomAligned(v bool)` and `OnCommandFinished func(cwd string)` to `Shell`:
   - When `bottomAligned == true`, `Shell.Render` places the collected block rows immediately above `PromptInput` (`Y = (maxH - chromeH) - collectedRows`) rather than at `Y = 0`, while keeping the ejection threshold at `totalBlockRows - (maxH - chromeH)`.
2. Add `ExecutionSession.SetDir(dir string)` and `Shell.SetDir(dir string)` for instant panel-to-shell directory synchronization.

### Phase 4: Composite `AppWidget` & Far Manager Split Key Router (`kabibi/ui/app/`)
1. Expand `AppWidget` in `kabibi/ui/app/app_widget.go` to coordinate `sh *shell.Shell` and `panels *filemanager.DualPaneWidget`:
   - When `!panelsVisible`: `sh.SetBottomAligned(false)` is active; `Shell` anchors its non-ejected blocks at row `0` (right below the latest untouched terminal history row) and adds downward toward the bottom, only ejecting at the top once the stack reaches the bottom (`allocHeight == H_term`).
   - When `panelsVisible`: `sh.SetBottomAligned(true)` is active; `AppWidget` renders `sh` into the full `rect` ($W \times H_{\text{term}}$) with blocks bottom-aligned above `PromptInput`, and blits `panels` over rows `0 .. H_panels-1`.
2. Implement the Far Manager Split Key-Routing table (§5.1), executable launching on `Enter` (§5.2), and bidirectional CWD synchronization (§5.3).

### Phase 5: Port Modal Dialogs, File Ops & Editor (`F2`–`F8`) + E2E Verification
1. Port `kabibi/ui/dialog/dialog.go`, `kabibi/ui/filemanager/fileops.go`, and `kabibi/ui/editor/editor.go` to native `ui.Widget`.
2. Add unit and VT-emulator E2E tests in `kabibi/ui/filemanager/filepane_widget_test.go` and `kabibi/ui/app/app_tui_e2e_test.go` covering:
   - Starting at any cursor row and shifting downward as output grows until hitting the bottom of the screen.
   - `Ctrl+O` summoning the blue panels over the top $H_{\text{panels}}$ rows with `BlockBanner` + `PromptInput` + blank footer fitting in the bottom gap.
   - Output sliding underneath the blue panels and only ejecting into scrollback once it catches up to row 0.
   - Far Manager split key routing, directory navigation, and running executables with `Enter`.
