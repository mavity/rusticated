# kabibi-go File Manager Plan

Full-featured file manager for the kabibi-go dual-pane TUI (Bubble Tea).

## Goals
- File/directory operations: create, delete, copy, rename, move.
- Custom text editor with syntax highlighting, scrollbar, selection, system clipboard copy/paste.
- Neat progress reporting for file operations.

## Decisions
- **Editor**: custom highlighted editor (not `bubbles/textarea`), highlighting via `chroma`.
- **File ops**: direct `os` calls with measurable byte/file progress.
- **Clipboard**: system clipboard (`atotto/clipboard`) with OSC52 fallback.

## Existing base
- `fileItem{name,isDir}` in `types.go`; `loadDir()` in `model.go`; per-pane `leftDir`/`rightDir`.
- Key dispatch: `tea.KeyMsg` switch in `update.go`.
- Manual grid rendering: `renderPanelWithTitle` in `view.go`.
- Async pattern to mirror: asset-download `tea.Cmd` + channel + progress message.
- Embedded u-root coreutils shell (`shell.go`) — file ops use direct `os` calls instead.

## Architecture

### Mode routing (`mode.go`)
`mode` enum: `modeBrowser`, `modeEditor`, `modeDialog`. `Update`/`View` switch on the
active mode and delegate to per-mode handlers. Behavior-preserving refactor first.

### Rich items + multi-select (`types.go`, `view.go`)
`fileItem` gains `size int64`, `modTime time.Time`, `mode fs.FileMode`, `selected bool`.
`loadDir` fills them from `DirEntry.Info()`. Rendering shows a size/mark column and paints
selected rows. Keys: `Space` toggle select, `Ins` select+advance.

### Dialogs (`dialog.go`)
Reusable modal sub-model centered over the dimmed browser:
- Input dialog (`textinput`): mkdir / rename / new file.
- Confirm dialog: delete / overwrite.
- Progress dialog: driven by op messages, reuses `renderProgressBar`; `esc` cancels via `context.Context`.

### File operations (`fileops.go`) — direct `os` calls with progress
`fileOp{kind, sources, dest}` run as async `tea.Cmd` (mirrors asset-download channel pattern):
- Pre-walk to compute total bytes/files, then stream `fileOpProgressMsg{done,total,current}`,
  end with `fileOpDoneMsg{err}`.
- Copy via `io.CopyBuffer` in chunks; move = `os.Rename` with copy+delete fallback across volumes;
  recursive dir copy/delete.
- Guards: overwrite/delete confirm; block `..`; block copy-into-own-subtree.
- Keys (NC-style): `F5` copy, `F6` move, `F2` rename, `F7` mkdir, `F8`/`Del` delete.
  Default dest = other pane's dir; multi-select feeds `sources`.

### Custom highlighted editor (`editor.go`)
- Buffer as `[]string` lines; cursor `(row,col)`; selection anchor; own scroll offset.
- Rendered through a viewport with a manual scrollbar column and cursor/selection overlay.
- Syntax highlighting via `chroma/v2`: tokenize visible lines, map token types to lipgloss
  colors; lexer chosen by file extension.
- Editing: insert/delete, split/join lines, backspace/delete, Home/End/PgUp/PgDn, word moves.
- Clipboard: `atotto/clipboard` system copy/paste + OSC52 fallback; `Ctrl+C/X/V` on selection.
- Open on `F4`/`Enter`-on-text-file; save `Ctrl+S`; quit `Esc` (prompt if dirty).

### Cleanups
- Unify the two height calculations (`recalculateLayout` vs `View`).
- Remove dead code after the early return in the `assetErrorMsg` case in `update.go`.

## Dependencies
- `github.com/alecthomas/chroma/v2`
- `github.com/atotto/clipboard`
Fallback if they don't build under the rusticated sysroot: internal tokenizer + OSC52-only clipboard.

## Build order
1. Mode routing (no-op refactor).
2. Rich items + multi-select.
3. Dialogs (input/confirm/progress).
4. File ops + progress wiring.
5. Custom editor core.
6. Editor highlighting + clipboard.
7. Cleanups.

## Key bindings (browser)
Single characters go to the shell prompt, so file-manager actions use function
keys and Insert:

- `F2` rename, `F4` edit (open editor), `F5` copy, `F6` move, `F7` mkdir, `F8` delete.
- `Insert` toggle mark on the current item and move down (multi-select).
- Arrows/PgUp/PgDn/Home/End navigate; `Enter` opens dirs / runs files; `Tab` switches panes; double-`Tab` opens chat.
- Copy/move default destination is the other pane's directory. With items marked,
  operations apply to the marked set; otherwise to the highlighted item.

## Key bindings (editor)
- `Ctrl+S` save, `Esc` quit (press twice if unsaved), `Ctrl+C/X/V` copy/cut/paste,
  `Ctrl+A` select all.
- Arrows move; `Shift+Arrows` extend selection; `Ctrl+Left/Right` word moves;
  `Home`/`End`, `Ctrl+Home`/`Ctrl+End`, `PgUp`/`PgDn`.

## Notes / deviations
- Syntax highlighting uses `chroma/v2` (whole-buffer tokenisation, cached; skipped for buffers > 400 KB).
- Clipboard uses `atotto/clipboard` with an in-app register fallback if the system clipboard is unavailable.
- Dialogs/progress render as centred modal boxes composited over the browser (whole-line replacement).
- Wide runes are treated as one cell for cursor math in the editor (basic editing focus).
- Copy overwrites existing destinations (truncate); the confirm dialog is the safeguard.

