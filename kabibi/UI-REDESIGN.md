# Requirement

Please consider how we can break kabibi (see [kabibi/README.md](README.md)) user interface into individual models alongside TUI (terminal UI) widgets? 

You need to look at the current architecture and propose a tree of container-subcontainer-grandsubcontainer-... where reasonable. Divide and conquer only works where the semantic separation of concerns is innate. That's why editor is naturally breakable as an own model.

I think we are still confused about the way 'plume' is rendered in the shell. It may not fully materialised so you might be not wrong but just not informed by the decisions and designs made before. The point is that the 'plume' is the combined output of the commands that were run in the shell so far, to certain maximum extent (which is quite large, several screens perhaps up to 10-15 screens up or few hundred lines). The plume is like an exhaust from the main shell processing. When the plume grows beyond the limit of X screens or Y lines whatever we decide, chunk of it will be ejected into the terminal as dead output and no longer managed. What's 'managed' in this case? It's to do with the reflow of the lines on screen size change. When kabibi processes a change of the screen width, it is meant to recalculate and the redraw the whole content from the top of the plume as it was down, reflowing the width of the buffer and recalculating how much height is required, redrawing everything.

That of course means that the calculation of what part of the plume is rendered below the panel, above the panel and peeking just below is dynamic, and should be completely precise rather than approximate. The whole of the managed plume needs to be laid, then the height of the panels derived, then from that the plume is broken into top/overlap/bottom and rendered. It is likely that bubbletea does not in fact have a notion of dynamic layout like HTML or graphic UIs do, expecting it to be static. In that case we should produce a clean and well defined protocol how parent and child can negotiate the sizes. Although it might not need to be a generic protocol, because for most cases the layout is probably simply driven from the parent to child.

And Let me be clear: we will not repurpose bubbletea notion of model for non-widget units of logic. Plume layout logic is not a model, it will not be forced into one. Nor will we repurpose bubbletea messages for negotiating layouts. The parent model has reference to child models, it will invoke methods like normal code does.

Another point worth noting: there is no need to isolate every little piece of UI into a separate model.

And sizing by the way also has to be handled for some other widgets: shell input because it can on occasion overflow into multiple lines, plus prompt might take dynamic horizontal size too, also the chat panel with its input of dynamic size, and plume-like chat history plus the scrollbar another riddler!

The models are going to be split by the visual logic, not by other concerns such as conjectured data ownership. That makes it clearer now.

Think how React elements are designed when used outside of next level frameworks: they own their visuals, their UI interactions, their behaviours and their data. Or WPF 3rd party components like grids etc. Parent element can pass the subset of the data to the child, and do some limited programmatic interactions up and down. But the key is composability and isolation alongside that seam. The child has a limited API surface to the parent, through which parent can drive it. Responsibilities are rather segregated and the elements are not trying to affect global state around that strict surface.

That doesn't exclude editor from having complex state, because an editor element does need to have quite a complex state, and it does need to drive the highlighting for example, or selection or copy paste, or scrolling etc. The parent of the editor should not really be exposed to the bulk of it, only to things on the need-to-know basis.

The application model does not need to get involved or even know how editor deals with syntax highlight for example. Or how file panel is handling its scroll position or the current selected file entry highlight.

Do you follow the philosophy?

I have analysed and contemplated these ideas, and decided to produce this problem statement which we could take as a foundational part of our refactoring.

Please review and consider:

### **Problem Statement: Kabibi Composite TUI & Spatial Cell Matrix Engine**

#### **1. Context & Background**
Kabibi combines a dual-pane file manager, an embedded shell engine (mvdan.cc/sh), an interactive text editor, and an AI chat assistant into a single portable application executing over a unified terminal surface.

Currently, the terminal user interface logic suffers from structural coupling and rendering fragility. Visual representation, input handling, and layout calculations across background file viewports, dynamic shell output histories (the plume), shell inputs, and modal popups are intermingled in top-level view logic.

#### **2. Core Problems & Architectural Flaws**

- Lack of Visual & Component Isolation: Sub-components (such as individual file panes, scrollable file lists, prompt inputs, and dialog overlays) lack clear isolation seams. This makes individual UI components difficult to unit test, maintain, or evolve independently.

- Layout Mathematics & ANSI Escape Fragility: Composing overlapping regions (e.g., rendering floating modal dialogs, editor surfaces, or slicing the live shell plume around panel bounds) by concatenating formatted ANSI escape streams is inherently fragile. Slicing or stitching string-formatted views causes visual tearing, character corruption, and broken color reset state sequences.

- Inflexible Layout Sizing & Input Routing: Parent viewports lack a structured, non-intrusive protocol to negotiate geometry (width/height dimensions) with child widgets, or to selectively forward keyboard and mouse inputs (such as SGR 1006 scroll events) to focused or hovered regions without triggering global state side effects.

#### **3. Refactoring Objectives**

##### **Goal A: Maintainable & Testable Component Tree**
Decompose the visual surface into an explicit, hierarchical tree of composable components.

- Interactive widgets (such as the text editor, file panes, shell prompt, and modal dialogs) must own their local internal state, keyboard/mouse interaction logic, and internal visual boundaries.

- Pure structural layout rules (such as computing panel geometry slots or calculating visible plume line slices) must remain cleanly segregated from business logic.

- Components must support optional input handling and optional layout sizing negotiation, allowing simple static elements to stay lightweight while interactive widgets handle focused updates.

##### **Goal B: Precise Spatial 2D Compositing (Zero-Tearing Overlays)**
Eliminate ANSI escape string generation as the intermediate currency between sub-widgets.

- Bounded Local Outputs: Each widget in the visual hierarchy must render its local visual output strictly as a bounded 2D rectangular surface of attributed character cells (runes and visual styling).

- Hierarchical Assembly: Parent components receive the 2D cell grids produced by their children, stamp (blit) them into assigned sub-rectangle coordinates, and pass the combined rectangular surface up to their parents.

- Clean Layering & Overlaps: Overlapping elements (modal popups, active editor takeovers, or plume history slices) are composed by overwriting target cell coordinates on the composite 2D grid, guaranteeing zero ANSI escape sequence damage, line bleeding, or visual artifacts.

- Single Output Serialization Boundary: Terminal output serialization is strictly isolated to the root application boundary. The final, fully composited 2D screen grid covering the terminal surface is converted to an ANSI stream exactly once per render frame before flushing to standard output.

#### **4. Target Orchestration Workflow**

1. Geometry Allocation: The root application determines terminal dimensions and propagates explicit rectangular bounding slots down through parent containers to child widgets.

- Local Rendering: Sub-widgets construct their visual representation locally into a bounded 2D cell rectangular surface sized to their target bounds.

- Hierarchical Compositing: Parent components assemble their children’s 2D cell surfaces—drawing borders, title chrome, and positioning sub-panes—and return the composite rectangular cell grid up the chain.

- Input Dispatch: Input events (keyboard focus state, mouse wheel coordinates) flow top-down, allowing parent containers to route messages based on focused regions, active modal overlays, or spatial coordinate hit-testing.

- Boundary Emission: The top-level surface receives the complete screen-sized 2D cell matrix and serializes it to an ANSI byte stream for the terminal.


# Design Specification: Kabibi Composite TUI Architecture

## 1. Executive Summary & Objectives
The purpose of this specification is to define the architectural refactoring of Kabibi’s terminal user interface. The primary goals are:

- **Maintainability & Testability:** Decompose the current monolithic implementation (`model.go`, `view.go`) into a clear, hierarchical tree of composable widgets where each unit owns its visual rendering, operational state, and local interactions.
- **Precise Spatial Compositing:** Eliminate fragile ANSI escape string concatenation between sub-components. Shift toward a hierarchical assembly model where components produce bounded rectangular cell-based outputs that parent containers compose, layer, and serialize to ANSI strictly once at the root output boundary.

## 2. Component Architecture & Code Mapping
The Kabibi user interface is structured as an explicit composition tree. The mapping from the existing monolithic implementation to the target component hierarchy is defined as follows:

Plaintext

```
AppModel (Root Application & Terminal Lifecycle)
├── DualPaneModel (Browser Area Container)
│   ├── FilePaneModel [Left Pane] (Chrome, Headers, Directory Path)
│   │   └── FileListView (Scrollable Item Matrix & Key Navigation)
│   └── FilePaneModel [Right Pane]
│       └── FileListView
├── ShellArea (Shell Subsystem Container)
│   ├── PlumeModel (Live Shell History Buffer, Reflow Math, Eviction)
│   └── PromptInputModel (Interactive Command Prompt & Line Input)
├── ChatArea (AI Assistant Subsystem Container)
│   ├── ChatModel (Unfocused Scroll Viewport & Streaming Badge)
│   └── ChatInputModel (Multi-Line Prompt Input)
└── Active Modals & Overlays (Pointer Stack)
    ├── EditorModel (Full-Screen Editor Takeover)
    └── Dialog Models (Confirm, Choice, Input Popups)
```

### Code Migration Mapping

- **`model.go` (Lines 83–170):** Splits out into `AppModel` for terminal lifecycle and window size coordination (`tea.WindowSizeMsg`), delegating sub-state down to specialized component models.
- **`view.go` (Lines 298–603):** Replaces the monolithic switchboard layout with parent-driven composition methods, where containers assemble child rectangular cell surfaces.
- **`editor.go` (Lines 55–100):** Retains its self-contained model structure (`EditorModel`), managing its own text buffers, cursor position (`cursorX`/`cursorY`), selection state, and scrolling offset while integrating into the top-down input and compositing tree.

## 3. The Core Component Contract (The 4 Pillars)
Every component within the visual tree adheres to a uniform contract:

1. **Input Handling (Optional):** Interactive components process terminal events (`tea.KeyMsg`, `tea.MouseMsg`) via a standard update method. Static layout wrappers omit input handling.
2. **State Segregation:**

- **Structural State:** Passed down explicitly from parent containers (e.g., directory paths, file metadata, slot boundary rectangles).
- **Operational State:** Managed exclusively and opaquely inside the child component (e.g., text cursors, scroll offsets, selection ranges).
3. **Child Brokerage & Geometry Allocation:** Parent containers compute screen slot geometry and distribute bounding boxes downward via explicit setup methods, avoiding implicit message-passing for layout negotiation.
4. **Rectangular Cell-Based Output:** Components render their local visual state strictly as a bounded 2D rectangular surface of attributed character cells, returning the surface to their parent container.

## 4. Spatial Composition & Stacking Semantics
Compositing operates through hierarchical assembly rather than string manipulation:

- **Base Layout Stacking:** The root application allocates rectangular slots for the Browser Area, Shell Area (Plume + Prompt), and Chat Area. Parent containers blit child cell surfaces into their allocated regions.
- **Non-Destructive Modals & Overlays:** When an editor or modal dialog is active, it captures input focus exclusively and stamps its rectangular cell grid over the active workspace. However, structural layout rules are respected: the **top managed slice of the plume remains visible peeking above the editor or modal**, while file viewports and shell prompts beneath them are covered.
- **Continuous Background Execution:** Background components (such as a running shell command stream) continue to execute and paint updates into their underlying buffers even while obscured by active modals in the $Z$-order.

## 5. Event & Input Dispatch Rules
Input routing is managed deterministically by parent models and the application root:

- **Focus-Based Keyboard Routing:** Keyboard events (`tea.KeyMsg`) are directed strictly along the active focus chain determined by top-level state enums or container focus flags.
- **Spatial Coordinate Mouse Routing (SGR 1006):** Terminal mouse wheel and motion events report precise cell coordinates $(X, Y)$. Parent dispatchers perform bounding-box hit-testing against sub-widget regions (e.g., checking if coordinates fall within the chat panel or plume history bounds) to route scroll events directly to target buffers without stealing keyboard focus from active input lines.
- **Modal Interception:** When an overlay or modal dialog is active, normal input propagation is short-circuited; all incoming key events target the top-most modal instance exclusively until dismissed.

## 6. Invalidation, Redraw Propagation & Loop Coalescing
To ensure high-performance rendering without UI flickering or redundant CPU cycles:

- **Bottom-Up Invalidation Requests:** When a component's internal state changes or a timer expires, it issues an explicit invalidation signal upward through the parent hierarchy.
- **Parent Filtering & Suppression:** Parent containers inspect the signal; if a parent has temporarily hidden or clipped a child component, the invalidation request is intercepted and suppressed. Otherwise, it propagates upward toward the root.
- **Root-Level Coalescing Loop:** The ultimate target of propagated invalidations is the application infrastructure. Redraw requests are queued on a dedicated background loop worker, coalescing rapid successive updates (such as high-frequency asynchronous token streaming) into a single unified render pass per frame boundary.

# Sizing, caching and layout clarifications

## Sizing: sometimes it's non-trivial

Plume, AI output, textual inputs in shell and AI chat panels all have dynamic sizing requirements.

### Plume

Plume consists of typed (or sometimes pasted!) user commands and shell outputs, including outputs from external processes when run from the shell.

That means a good chunk of it is ANSI based, not cell-based. The history that makes up the Plume state will be stored precisely to the actual raw state for each of those, i.e. ANSI inputs will be retained.

The sizing of Plume necessarily will require re-parsing of the ANSI sequences and 'rendering' them into a cell grid, taking into account horizontal bounds at the time of the rendering.

The Plume can cache that rendered cell grid to avoid redundant re-parsing and rendering on subsequent layout passes, and reuse it if layout sizing or rendering is invoked again on the same bounded width and same content.

When the Plume is required to size itself, it returns back to the parent widget (the app) the computed diemensions which in this case really is all about computed height.

The parent then takes that height into account to size/lay other widgets out. By adding the Plume height to the current shell typed input height the app gets the full managed height it will render. From that it will need to allocate the space upwards for the panels and a gap below the panels where few lines of the previous terminal output are visible.

The file panels are meant to display at the top of the screen area and extend down covering majority of the screen but not all (the existing formula already exists in the kabibi code, we can retain the formula).

When the file panels are not visible fine, the Plume still takes that much space and is rendered unobscured. Toggling file panels on or off should not affect the space allocated to the Plume itself nor produce any janky scrolling up and down.

Note that the editor being visible works almost the same way, except the editor covers full height of the screen viewport rather than file panels hang shorter with the knees and feet poking below the shorts.

### AI chat history

AI chat outputs are Markdown by default, which is also not cell-based. User inputs can be considered either plain text or Markdown, so they are technically not cell-based also.

This lands AI chat output into a very similar category as the Plume: the width bound drives the rendering of the raw source content (Markdown and plain text) into a constrained cell grid with parsing and text line overflows dependent on the content and the available width.

The handling of the sizing would be very much similar to the Plume here.

Note that AI chat does not have an unlimited plume visually, because the parent widget (chat panel) will clip the history and use scrollbars to viewport it into a bounded height not just bounded width.

But also just as with the Plume, the parent takes into account not only the height of the history, but also the height of the input area which is technically dynamic, because a user can type or paste more than one line of text.

### Textual inputs

These appear in shell bottom area, AI chat prompt, and may appear in some popups.

Editor text input behaves quite differently in terms of sizing so it is not considered in this section.

Textual inputs in all these cases are currently defaults to single-line height, expanding dynamically as the user types or pastes more lines of text. Shift-Enter is expected to be interpreted as typing a line break rather than submitting the input which is Enter's function.

In all current cases the vertical expansion of such textual input is necessarily restrained. The input height in the shell is constrained by the space available at the bottom of the viewport below the blue file panels. The input height in the AI chat is constrained to be no more than half of the AI panel inner height (which is derived from certain logic currently).

When a text input becomes insufficient, a scrollbar and potentially other scroll affordances should appear.

The negotiation for the height between the parent and the child would mean the text input can report a desired size under available constraints passed, and the parent can take that desired size into account, but it is the parent who ultimately decides the final height actually allocated to the text input for rendering.

This two-part negotiation process (pre-existing constraints passed to calculating the desired size, then the parent deciding the final allocated size) is conventional approach in UI frameworks.

## Caching of the render output

Widgets MAY cache their previous render outputs where that does not produce a different output.

For example, that could be useful in the Plume case where the overal size of the raw ANSI content can be large and re-parsing on it every re-render is expensive.

The Plume can capture its whole render output together with the constraints it was generated under and the actual data. If another render (or another sizing request) lands and the constraints and the data are identical, the Plume simply returns the cached render output.

The invalidation mechanism discussed above is not meant to interfere with the caching. The invalidation is effectively a request to re-render and whether the cache is invalid is a different matter to the actual re-render request.

# Step by step plan

Incremental refactor in six phases: the cell engine is built first as a new dependency-free module; widgets are extracted one at a time without breaking existing code; then in Phase 5 the ANSI string compositor is replaced by a single `Serialize` call at the root boundary.

---

**Phase 0 — Cell Engine Foundation** (`cell.go` + `cell_test.go`) — *no existing files change*

1. Define `Cell{R rune, FG/BG lipgloss.Color, Bold/Italic/Reverse bool}`, `Rect{X,Y,W,H}`, `CellBuf{Width, Height int, cells []Cell}`
2. Methods: `NewCellBuf(w,h)`, `Blit(src, dstX, dstY)`, `Fill(r Rect, c Cell)`, `Row(y) []Cell`
3. `Serialize(buf CellBuf) string` — the **one** ANSI serialization point; emits only SGR deltas. Replaces `overlayBox`, `forceBackground`, and the raw `\x1b[0m` splicing in `view.go:560-636`

Test: Blit, Fill, Serialize round-trip at 3×2 and with overlapping regions

---

**Phase 1 — Widget Interface** (`widget.go`) — *additive only*

4. `Constraints{MaxW, MaxH int}`, `Size{W, H int}`
5. `Widget` interface: `Measure(Constraints) Size` / `Layout(Rect)` / `Render() CellBuf`
6. `InputWidget` extends `Widget` with `HandleKey(tea.KeyMsg) tea.Cmd` / `HandleMouse(tea.MouseMsg) tea.Cmd`
7. `InvalidateFunc` callback type for bottom-up dirty signaling

---

**Phase 2 — FilePaneWidget** (`filepane_widget.go`) — *parallel to existing code*

8. `FilePaneWidget` — ports `renderPanelWithTitle` (`view.go:155-283`) to write cells instead of lipgloss strings; owns scroll position and active-highlight state
9. `DualPaneWidget` — holds `[2]FilePaneWidget`, blits them side-by-side
10. Key handling ported from the `tea.KeyMsg` switch in `update.go:246-400`

**`renderPanelWithTitle` is kept untouched** until Phase 5 wire-in.

Test: empty pane, 5-file pane, active highlight, narrow multi-column

---

**Phase 3 — PlumeWidget** (`plume_widget.go`) — *parallel to existing code*

11. `PlumeEntry{raw []byte, isANSI bool}`, `PlumeBuffer` with `Append` / `Evict` — replaces the `[]string plume` field; keeps the 120-line cap (or raise to 300)
12. `PlumeLayout` — pure function: `LayoutPlume(entries, width, panelRows) PlumeSlices{Exhaust, Occluded, Footer []CellBuf}` — replaces the index arithmetic in `view.go:432-458`
13. `PlumeWidget` implements `Widget`: `Measure` returns computed height; render cache keyed by `(width uint, contentVersion uint64)`

Test: layout math matches current slice arithmetic exactly; cache hit/miss; ANSI re-parse at two widths; eviction at cap

---

**Phase 4 — ChatWidget** (`chat_widget.go`) — *parallel to existing code*

14. `ChatWidget`: owns `*Conversation`, scroll offset, render cache; ports the open-chat branch of `viewFrame` (`view.go:397-467`) to cell output
15. `ChatInputWidget`: wraps `textinput.Model`; `Measure` reports 1..N rows; `HandleKey` owns Shift-Enter multi-line logic
16. `ChatWidget` composes `ChatInputWidget` internally; parent sees one aggregate `Measure` result

Phase 6 will do exact Markdown→cell; Phase 4 uses plain-text cells stripped of ANSI.

---

**Phase 5 — Wire-in & Replace `viewFrame`** (edit `view.go`, `model.go`, `types.go`, `update.go`)

This is the only phase that modifies existing view logic.

17. Add `dualPane DualPaneWidget`, `plumeW PlumeWidget`, `chatW ChatWidget` fields to `model` in `types.go:83`
18. Extend `recalculateLayout` (`model.go:283`) to call `Layout(rect)` on each new widget alongside the existing size calls — dual-path during transition
19. Implement `viewFrame2()` in `view.go`:
    - `plume.Measure(Constraints{MaxW: m.width})` → derive panel rect using existing formula
    - Blit `dualPane.Render()`, `plumeW.Render()` slices, `chatW.Render()` into a screen-sized `CellBuf`
    - Modal/dialog overlay via `Blit` instead of `overlayBox`
    - Return `ansiDualColor(Serialize(screenBuf))`
20. Gate on `m.useNewCompositor bool` for side-by-side visual testing (`go run ./kabibi -new-compositor`)
21. Add new dispatch path in `update.go` that calls `dualPane.HandleKey` / `chatW.HandleKey` when flag is set
22. After visual confirmation: delete `viewFrame`, rename `viewFrame2 → viewFrame`; remove flag, old fields, `overlayBox`, `forceBackground`, `renderPanelWithTitle`

All existing tests pass after every step.

---

**Phase 6 — Cleanup & Stretch Goals** (independent of each other)

23. `DialogWidget` ports `dialogBox`/`progressBox` from `dialog.go` to cell output
24. Replace `viewport.Model` chat with `ChatWidget`'s own scroll state; drop the bubbles/viewport import
25. `PromptInputWidget` multi-line expansion: `Measure` reports dynamic height; parent clamps
26. Exact Markdown→cell conversion for chat history (same ANSI-reparse pattern as Plume)
27. Mouse SGR 1006 hit-testing and spatial scroll routing

---

**Relevant files**

- `view.go` — Phase 5 rewrites this (780 lines)
- `model.go` — `recalculateLayout`, `syncChatView`, `AddPlume`; Phases 3/5
- `types.go` — `model` struct; Phase 5 adds widget fields
- `update.go` — key routing; Phase 5 adds new dispatch path
- `editor.go` — untouched until Phase 6
- `dialog.go` — string-based through Phase 5; Phase 6 converts
- `styles.go` — `ansiDualColor` stays; `forceBackground` deleted Phase 5

New files: `cell.go`, `cell_test.go`, `widget.go`, `filepane_widget.go`, `filepane_widget_test.go`, `plume_widget.go`, `plume_widget_test.go`, `chat_widget.go`, `chat_widget_test.go`

---

**Verification**

1. `go test `rustica`.` green after every phase
2. After Phase 5 step 20: visual run at 80×24 and 200×50 with `-new-compositor` flag
3. After Phase 5 step 22: grep confirms `overlayBox` / `forceBackground` / `renderPanelWithTitle` are gone from non-test files
4. Overlay correctness: compare `Serialize(screenBuf)` cell-by-cell for a 10×5 dialog overlay vs. expected
5. Plume eviction invariant: existing `TestAddPlume` + new `TestPlumeEviction`
6. Width reflow: `TestPlumeLayout` at widths 40/80/200 — `Exhaust+Occluded+Footer` height equals computed plume height

---

**Decisions / Scope**

- Widgets are plain Go structs; **not** Bubble Tea models
- Layout negotiation is direct method calls (`Measure`/`Layout`); **not** Bubble Tea messages
- `PlumeBuffer.maxLines` stays at 120 initially (can be tuned separately)
- Phases 2–4 run alongside existing code without regressions; Phase 5 is the cutover
- Mouse routing, multi-line shell input, exact Markdown rendering: Phase 6

# Completion of the Plan

## Plan: Phase 6 — Cleanup & Stretch Goals

Phase 6 breaks into six independently verifiable sub-phases. The ordering respects one hard dependency chain: **6C (scroll state) → 6D (Markdown→cell)** and **6A (legacy cutover) → last** (because `forceBackground` and `overlayBox` are still live until 6B and 6D retire them). The others — 6B, 6C, 6E — can proceed in parallel.

---

**Steps**

### Phase 6A — Legacy Cutover *(finalizes Phase 5 step 22; blocks on 6D for `forceBackground`)*
1. Remove `-new-compositor` flag from `main.go` and `useNewCompositor bool` from `types.go`
2. In `view.go`: rename `viewFrame2()` → `viewFrame()`, delete the old `viewFrame()` body, collapse `View()` to one line
3. Delete `overlayBox()` and `renderPanelWithTitle()` from `view.go` — neither is called from the new path
4. Defer deleting `forceBackground()` until 6D is done — still called by `syncChatView()` in `model.go` (line 229) and `renderAssistantChatBlock()` in `markdown.go` (line 89)

### Phase 6B — DialogWidget *(parallel with 6C, 6E; additive new file)*
5. Create `dialog_widget.go` — `DialogWidget` struct with render-only fields: kind, title, prompt, choices, choiceIdx, input value/cursor, op progress fields, box width
6. Implement `DialogWidget.Render() CellBuf` — fill dialog bg, draw rounded border cells, title row, prompt row, button row (active = yellow/black, inactive = gray/gray), progress bar cells using existing `renderProgressBar()` logic
7. Add `func (m *model) buildDialogWidget() *DialogWidget` — reads `m.dialog`, `m.opActive`, etc.; returns nil if no dialog is active
8. In `viewFrame()`: replace the three `overlayBox(base, m.dialogBox(), ...)` / `overlayBox(base, m.progressBox(), ...)` calls with `buf.Blit(w.Render(), centerX, centerY)` using the widget's own dimensions to center
9. After visual confirmation: delete `dialogBox()`, `progressBox()`, `overlayBox()` from `dialog.go` and `view.go`

### Phase 6C — ChatWidget Scroll State *(parallel with 6B, 6E; removes `viewport.Model`)*
10. Add `ScrollOffset int` to `ChatWidget` in `chat_widget.go`; `Render()` clips history rows using `[totalRows-availableH-offset : totalRows-offset]`; clamps to `[0, max]`
11. Add scroll bindings to `ChatWidget.HandleKey()`: `pgup/pgdown` adjust offset; `home/end` jump to extremes; any new message resets offset to 0
12. In `types.go`: delete `chatView viewport.Model` field and the `"github.com/charmbracelet/bubbles/viewport"` import
13. In `model.go`: remove `vp := viewport.New(0, 0)`, the `chatView: vp` initializer, and gut `syncChatView()` to a no-op (or delete it and all callers in `update.go`)

### Phase 6D — Markdown→Cell *(depends on 6C; upgrades `ChatWidget.Render()`)*
14. Add `renderANSIIntoBuf(dst CellBuf, x, y int, text string, width int, fg, bg lipgloss.Color) (endY int)` to `cell.go` — walks the ANSI string, tracks current FG/BG/Bold state via the existing `sgrRe` pattern from `styles.go`, writes cells with word-wrap at `width`, returns the next free row
15. In `ChatWidget.Render()`: replace `stripANSI()` + `renderLineIntoBuf()` with: call `renderAssistantChatBlock(msg.Content, width)` for assistant turns (gives glamour-formatted ANSI), then `renderANSIIntoBuf(buf, 0, row, rendered, ...)` for both assistant and user turns
16. After confirmation: delete `forceBackground()` (last callsites gone), delete `syncChatView()` (already dead from 6C), delete `renderAssistantChatBlock()` if nothing else calls it

### Phase 6E — PromptInputWidget *(parallel with 6B, 6C; additive new file)*
17. Create `prompt_widget.go` — `PromptInputWidget{Prompt, Value, CursorPos, Width, Height string}`; no model reference
18. `Measure()` returns `{W: c.MaxW, H: 1}` for now (structure ready for multi-line)
19. `Render()` writes prompt prefix + value chars as cells; cursor cell gets `Reverse: true`
20. Wire into `viewFrame()`: blit at `(0, m.height-1)` instead of `promptStyle.Copy()...Render(m.shellInput.View())`

### Phase 6F — Mouse SGR 1006 Routing *(stretch; depends on 6C for authoritative widget bounds)*
21. Add `tea.WithMouseCellMotion()` to `tea.NewProgram` in `main.go`
22. In `update.go`: add `case tea.MouseMsg:` block; hit-test `msg.X/Y` against widget `X, Y, Width, Height` fields; if modal active, swallow
23. Chat widget rect → `m.chatW.HandleMouse(msg)` (wheel up/down adjusts `ScrollOffset`)
24. Left/right pane rects → `m.dualPane.Left/Right.HandleMouse(msg)` (scroll moves selection)

---

**Relevant files**
- `view.go` — 6A removes legacy; 6B adds dialog blit; 6E adds prompt blit
- `dialog.go` — 6B adds `buildDialogWidget()`; old `dialogBox/progressBox` survive until confirmed
- `chat_widget.go` — 6C adds scroll state; 6D upgrades Markdown rendering
- `markdown.go` — 6D; `renderAssistantChatBlock` eventually deleted
- `types.go` — 6C removes `viewport.Model`
- `model.go` — 6C guts `syncChatView()`
- `cell.go` — 6D adds `renderANSIIntoBuf`
- `main.go` — 6F adds mouse option
- `update.go` — 6F adds mouse dispatch
- `dialog_widget.go`, `prompt_widget.go` — new files for 6B, 6E

**Verification**
1. `go test .` green after every sub-phase
2. 6B: all three dialog kinds (input, choice, progress) visible and interactive
3. 6C: chat panel scrolls; new messages auto-bottom; `bubbles/viewport` import gone
4. 6D: glamour Markdown (bold, code fences, bullet lists) renders in chat with colors
5. 6A final: `grep -rn 'overlayBox\|renderPanelWithTitle\|forceBackground\|useNewCompositor' kabibi/*.go` returns empty

**Decisions**
- `forceBackground()` must outlive 6D — delete it only as the last step of 6D
- `renderANSIIntoBuf` lives in `cell.go` unless it exceeds ~80 lines, then `ansi_cells.go`
- Multi-line shell input (PromptInputWidget height > 1) is explicitly deferred past Phase 6F
- 6F is a true stretch goal; it has no other Phase 6 items blocking on it

**Further considerations**
1. `renderANSIIntoBuf` (step 14) is the only genuinely novel primitive in Phase 6; the ANSI state-machine is similar to the existing `stripANSI()` walk but must track and *emit* color state rather than discard it. It warrants its own focused unit test before being wired into ChatWidget.
2. `buildDialogWidget()` (6B, step 7) is a pure value constructor — it should not mutate any model fields. The input cursor position needs to be read from `m.dialog.input.Position()` if the bubbles textinput exposes it, otherwise track separately.

