package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/dialog"
	"github.com/mavity/rusticated/kabibi/ui/editor"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

const defaultPanelGapRows = 5

// AppWidget is the composite root UI coordinator implementing ui.Widget.
// It coordinates the bottom-up Shell stack, the DualPaneWidget blue HUD panels
// toggled via Ctrl+O, modal file-operation dialogs (F2/F5/F6/F7/F8), and the
// full-screen syntax-highlighted EditorWidget (F4).
type AppWidget struct {
	mu sync.Mutex

	sh                *shell.Shell
	panels            *filemanager.DualPaneWidget
	panelsVisible     bool
	fullScreenClaimed bool

	dialog *dialog.DialogWidget
	editor *editor.EditorWidget

	opCancel context.CancelFunc
	opResume chan filemanager.CollisionChoice

	writeScrollback func(string)
	onExit          func()
	ctx             ui.RenderContext
}

// AppWidgetOptions contains initial parameters and service bindings for AppWidget.
type AppWidgetOptions struct {
	WriteToScrollback func(string)
	OnExit            func()
	WorkingDir        string
	ShellMode         shell.ShellMode
	PanelsVisible     bool
	FSService         filemanager.FSService
}

// NewWidget constructs a clean AppWidget instance with wired Shell and DualPaneWidget.
func NewWidget(opts AppWidgetOptions) *AppWidget {
	w := &AppWidget{
		writeScrollback: opts.WriteToScrollback,
		onExit:          opts.OnExit,
	}

	w.sh = shell.New(shell.ShellOptions{
		WriteToScrollback: func(text string) {
			if w.writeScrollback != nil {
				w.writeScrollback(text)
			}
		},
		OnExit: func() {
			w.SetPanelsVisible(false)
			if w.onExit != nil {
				w.onExit()
			}
		},
		OnCommandFinished: func(cwd string) {
			w.onShellCommandFinished(cwd)
		},
		WorkingDir: opts.WorkingDir,
		Mode:       opts.ShellMode,
	})

	w.panels = filemanager.NewDualPaneWidget()
	if opts.FSService != nil {
		w.panels.SetFSService(opts.FSService)
	}

	initDir := w.sh.Dir()
	if initDir == "" {
		initDir = opts.WorkingDir
	}
	if initDir == "" {
		initDir, _ = os.Getwd()
	}
	if initDir != "" {
		_ = w.panels.Left.LoadDir(initDir)
		_ = w.panels.Right.LoadDir(initDir)
	}

	// Wire directory changes inside the active pane back to ExecutionSession.Dir()
	w.panels.Left.OnChangeDir = func(path string) {
		if w.panels.ActivePaneIndex() == 0 {
			_ = w.sh.SetDir(path)
		}
	}
	w.panels.Right.OnChangeDir = func(path string) {
		if w.panels.ActivePaneIndex() == 1 {
			_ = w.sh.SetDir(path)
		}
	}

	// Wire Enter on a file in the active pane: run executables, edit other files.
	w.panels.Left.OnOpenFile = func(path string) {
		w.handlePaneOpenFile(path)
	}
	w.panels.Right.OnOpenFile = func(path string) {
		w.handlePaneOpenFile(path)
	}

	if opts.PanelsVisible {
		w.SetPanelsVisible(true)
	}

	return w
}

// Panels returns the embedded DualPaneWidget for inspection and testing.
func (w *AppWidget) Panels() *filemanager.DualPaneWidget {
	return w.panels
}

// PanelsVisible reports whether the blue file panels HUD is currently visible.
func (w *AppWidget) PanelsVisible() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.panelsVisible
}

// SetPanelsVisible toggles the blue file manager panels on or off.
// When turned on, the shell stack switches to bottom-aligned full-viewport mode
// so the banner + prompt + blank footer sit in the bottom gap while older lines
// climb upward behind the blue panels.
// When turned off, the shell stack returns to top-aligned mode (anchored at row 0,
// immediately below the latest untouched row in terminal scrollback) and grows
// downward toward the bottom before ejecting upward.
func (w *AppWidget) SetPanelsVisible(v bool) {
	w.mu.Lock()
	w.panelsVisible = v
	w.fullScreenClaimed = v
	w.mu.Unlock()

	w.sh.SetBottomAligned(v)
	if v {
		if cwd := w.sh.Dir(); cwd != "" {
			if active := w.panels.ActivePane(); active != nil && active.Dir() != cwd {
				_ = active.LoadDir(cwd)
			}
		}
	}
	w.invalidate()
}

// TogglePanels flips the visibility of the dual blue panels (`Ctrl+O`).
func (w *AppWidget) TogglePanels() {
	w.SetPanelsVisible(!w.PanelsVisible())
}

// Dialog returns the currently active modal dialog, if any.
func (w *AppWidget) Dialog() *dialog.DialogWidget {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dialog
}

// Editor returns the currently active full-screen editor widget, if any.
func (w *AppWidget) Editor() *editor.EditorWidget {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.editor
}

// OpenEditor opens path in the full-screen editor. When exitOnClose is true,
// Escape saves pending edits and invokes the app's exit handler after closing.
func (w *AppWidget) OpenEditor(path string, exitOnClose bool) error {
	ed, err := editor.NewEditor(path)
	if err != nil {
		return err
	}
	ed.SaveOnExit = exitOnClose
	ed.OnCloseFunc = func() {
		w.mu.Lock()
		w.editor = nil
		onExit := w.onExit
		w.mu.Unlock()
		if dir := filepath.Dir(path); w.panels != nil {
			if active := w.panels.ActivePane(); active != nil && filepath.Clean(active.Dir()) == filepath.Clean(dir) {
				_ = active.Refresh()
			}
		}
		w.invalidate()
		if exitOnClose && onExit != nil {
			onExit()
		}
	}
	w.mu.Lock()
	w.editor = ed
	w.mu.Unlock()
	w.invalidate()
	return nil
}

// SetExitHandler binds or updates the exit callback post-construction.
func (w *AppWidget) SetExitHandler(fn func()) {
	w.onExit = fn
}

// Close releases resources held by the widget and its embedded shell.
func (w *AppWidget) Close() error {
	w.SetPanelsVisible(false)
	w.mu.Lock()
	cancel := w.opCancel
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if w.sh != nil {
		return w.sh.Close()
	}
	return nil
}

// SetScrollbackWriter binds or updates the scrollback callback post-construction.
func (w *AppWidget) SetScrollbackWriter(fn func(string)) {
	w.writeScrollback = fn
}

// Shell returns the embedded shell widget for testing/debugging access.
func (w *AppWidget) Shell() *shell.Shell {
	return w.sh
}

func (w *AppWidget) invalidate() {
	w.mu.Lock()
	inv := w.ctx.Invalidate
	w.mu.Unlock()
	if inv != nil {
		inv()
	}
}

func (w *AppWidget) onShellCommandFinished(cwd string) {
	if w.panels == nil {
		return
	}
	active := w.panels.ActivePane()
	other := w.panels.OtherPane()
	if active != nil {
		if cwd != "" && filepath.Clean(cwd) != filepath.Clean(active.Dir()) {
			_ = active.LoadDir(cwd)
		} else {
			_ = active.Refresh()
		}
	}
	if other != nil {
		_ = other.Refresh()
	}
	w.invalidate()
}

func shellQuoteFilename(name string) string {
	if !strings.ContainsAny(name, " \t\"'\\$&;|<>()*?[]{}!`") {
		return name
	}
	return "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
}

func (w *AppWidget) handlePaneOpenFile(fullPath string) {
	if !filemanager.IsExecutableFile(fullPath) {
		if err := w.OpenEditor(fullPath, false); err != nil && w.writeScrollback != nil {
			w.writeScrollback(fmt.Sprintf("Unable to open %q: %v\n", fullPath, err))
		}
		return
	}
	base := filepath.Base(fullPath)
	cmdLine := "./" + shellQuoteFilename(base)
	w.sh.SetInput(cmdLine)
	w.sh.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
}

// computePanelHeight calculates H_panels and H_gap so the bottom gap matches
// the initial 5-row banner + prompt + blank footer stack (expanding if PromptInput wraps)
// while respecting the 40% lower-area cap on short terminals.
func (w *AppWidget) computePanelHeight(totalW, totalH int) (panelH, gapH int) {
	if totalH <= 0 {
		totalH = 24
	}
	inputSz := w.sh.Input().Measure(ui.Constraints{MaxW: totalW, MaxH: totalH - 1})
	inputH := inputSz.H
	if inputH < 1 {
		inputH = 1
	}
	gapH = 3 + inputH + 1 // 3 banner/plume peek rows + PromptInput + 1 blank footer row
	if gapH < defaultPanelGapRows {
		gapH = defaultPanelGapRows
	}
	maxGap := int(float64(totalH) * 0.40)
	if maxGap < 3 {
		maxGap = 3
	}
	if gapH > maxGap {
		gapH = maxGap
	}
	panelH = totalH - gapH
	if panelH < 3 {
		panelH = 3
	}
	if panelH > totalH-2 {
		panelH = totalH - 2
	}
	if panelH < 0 {
		panelH = 0
	}
	return panelH, totalH - panelH
}

// ── ui.Widget Interface Implementation ────────────────────────────────────────

func (w *AppWidget) Measure(c ui.Constraints) ui.Size {
	w.mu.Lock()
	panelsVis := w.panelsVisible
	claimed := w.fullScreenClaimed
	ed := w.editor
	dlg := w.dialog
	w.mu.Unlock()

	maxW := c.MaxW
	if maxW <= 0 {
		maxW = 80
	}
	maxH := c.MaxH
	if maxH <= 0 {
		maxH = 24
	}

	if ed != nil || dlg != nil || panelsVis || claimed {
		return ui.Size{W: maxW, H: maxH}
	}
	return w.sh.Measure(c)
}

func (w *AppWidget) Render(r terminal.Rect, ctx ui.RenderContext) (terminal.CellBuf, ui.CursorPos) {
	w.mu.Lock()
	w.ctx = ctx
	panelsVis := w.panelsVisible
	ed := w.editor
	dlg := w.dialog
	w.mu.Unlock()

	if ed != nil {
		return ed.Render(r, ctx)
	}

	shellBuf, cursor := w.sh.Render(r, ctx)

	if panelsVis && w.panels != nil && r.W > 0 && r.H > 0 {
		panelH, _ := w.computePanelHeight(r.W, r.H)
		if panelH > 0 && panelH <= shellBuf.Height {
			panelBuf, _ := w.panels.Render(ui.Rect{X: 0, Y: 0, W: r.W, H: panelH}, ctx)
			shellBuf.Blit(panelBuf, 0, 0)
		}
	}

	if dlg != nil && shellBuf.Width > 0 && shellBuf.Height > 0 {
		sz := dlg.Measure(ui.Constraints{MaxW: shellBuf.Width, MaxH: shellBuf.Height})
		dx := (shellBuf.Width - sz.W) / 2
		dy := (shellBuf.Height - sz.H) / 2
		if dx < 0 {
			dx = 0
		}
		if dy < 0 {
			dy = 0
		}
		dlgBuf, dlgCur := dlg.Render(ui.Rect{X: dx, Y: dy, W: sz.W, H: sz.H}, ctx)
		shellBuf.Blit(dlgBuf, dx, dy)
		if dlgCur.Visible {
			cursor = ui.CursorPos{
				X:       dx + dlgCur.X,
				Y:       dy + dlgCur.Y,
				Visible: true,
			}
		} else {
			cursor.Visible = false
		}
	}

	return shellBuf, cursor
}

func (w *AppWidget) HandleEvent(e input.Event) bool {
	w.mu.Lock()
	ed := w.editor
	dlg := w.dialog
	panelsVis := w.panelsVisible
	w.mu.Unlock()

	// 1. Full-screen editor takeover has top priority
	if ed != nil {
		handled := ed.HandleEvent(e)
		if handled {
			w.invalidate()
		}
		return handled
	}

	// 2. Modal dialog has second priority
	if dlg != nil {
		handled := dlg.HandleEvent(e)
		if handled {
			w.invalidate()
		}
		return handled
	}

	// 3. Window resize always updates both Shell and Panels
	switch evt := e.(type) {
	case input.WindowSizeEvent:
		w.sh.HandleEvent(evt)
		w.invalidate()
		return true
	case *input.WindowSizeEvent:
		if evt != nil {
			w.sh.HandleEvent(*evt)
			w.invalidate()
			return true
		}
		return false
	}

	// 4. While a shell command or AI turn is actively running, Shell owns all input
	// (Ctrl+C cancellation and interactive PTY keystroke/paste forwarding).
	if w.sh.IsRunning() {
		return w.sh.HandleEvent(e)
	}

	// Extract key press if this is a keyboard event
	var keyEv input.KeyPressEvent
	var isKey bool
	switch k := e.(type) {
	case input.KeyPressEvent:
		keyEv = k
		isKey = true
	case *input.KeyPressEvent:
		if k != nil {
			keyEv = *k
			isKey = true
		}
	}

	if isKey {
		keyStr := keyEv.String()

		// Global Ctrl+O toggles the dual blue file manager panels
		if keyStr == "ctrl+o" {
			w.TogglePanels()
			return true
		}

		// When blue panels are visible, apply the Far Manager Split Key-Routing Table
		if panelsVis && w.panels != nil {
			hasInput := w.sh.Input().Value() != ""

			switch keyStr {
			case "esc":
				if hasInput {
					w.sh.Input().Reset()
					w.invalidate()
					return true
				}
				w.SetPanelsVisible(false)
				return true

			case "tab":
				w.panels.ToggleActivePane()
				if active := w.panels.ActivePane(); active != nil && active.Dir() != "" {
					_ = w.sh.SetDir(active.Dir())
				}
				w.invalidate()
				return true

			case "up", "down", "pgup", "pgdown", "insert":
				if active := w.panels.ActivePane(); active != nil {
					active.HandleEvent(keyEv)
					w.invalidate()
					return true
				}

			case "left", "right", "home", "end":
				if !hasInput {
					if active := w.panels.ActivePane(); active != nil {
						active.HandleEvent(keyEv)
						w.invalidate()
						return true
					}
				}
				return w.sh.HandleEvent(keyEv)

			case "ctrl+enter", "ctrl+f":
				if active := w.panels.ActivePane(); active != nil {
					if item, ok := active.SelectedItem(); ok && item.Name() != ".." {
						curVal := w.sh.Input().Value()
						quoted := shellQuoteFilename(item.Name())
						if curVal != "" && !strings.HasSuffix(curVal, " ") {
							curVal += " "
						}
						w.sh.SetInput(curVal + quoted)
						w.invalidate()
						return true
					}
				}
				return true

			case "enter":
				if hasInput {
					return w.sh.HandleEvent(keyEv)
				}
				if active := w.panels.ActivePane(); active != nil {
					active.ActivateSelected()
					w.invalidate()
					return true
				}

			case "f2":
				w.openRenameDialog()
				return true
			case "f4":
				w.openEditorOnSelected()
				return true
			case "f5":
				w.openTransferDialog(filemanager.OpCopy)
				return true
			case "f6":
				w.openTransferDialog(filemanager.OpMove)
				return true
			case "f7":
				w.openMkdirDialog()
				return true
			case "f8", "delete":
				if keyStr == "delete" && hasInput {
					return w.sh.HandleEvent(keyEv)
				}
				w.openDeleteDialog()
				return true
			}
		}
	}

	return w.sh.HandleEvent(e)
}

// ── File Manager Modal & Editor Actions (F2–F8) ───────────────────────────────

func (w *AppWidget) opSources() []string {
	if w.panels == nil {
		return nil
	}
	active := w.panels.ActivePane()
	if active == nil {
		return nil
	}
	dir := active.Dir()
	marked := active.MarkedNames()
	if len(marked) > 0 {
		out := make([]string, 0, len(marked))
		for _, name := range marked {
			out = append(out, filepath.Join(dir, name))
		}
		return out
	}
	if item, ok := active.SelectedItem(); ok && item.Name() != ".." {
		return []string{filepath.Join(dir, item.Name())}
	}
	return nil
}

func (w *AppWidget) closeDialog() {
	w.mu.Lock()
	w.dialog = nil
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) openMkdirDialog() {
	dlg := dialog.NewInputDialog(dialog.ActionMkdir, "Make Directory", "Create directory:", "", nil)
	dlg.OnCancel = w.closeDialog
	dlg.OnSubmitInput = w.onDialogSubmitInput
	w.mu.Lock()
	w.dialog = dlg
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) openRenameDialog() {
	active := w.panels.ActivePane()
	if active == nil {
		return
	}
	item, ok := active.SelectedItem()
	if !ok || item.Name() == ".." {
		return
	}
	dlg := dialog.NewInputDialog(dialog.ActionRename, "Rename", "Rename "+item.Name()+" to:", item.Name(), nil)
	dlg.OnCancel = w.closeDialog
	dlg.OnSubmitInput = w.onDialogSubmitInput
	w.mu.Lock()
	w.dialog = dlg
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) openTransferDialog(kind filemanager.FileOpKind) {
	sources := w.opSources()
	if len(sources) == 0 {
		return
	}
	dest := w.panels.OtherDir()
	if dest == "" {
		dest = w.panels.ActiveDir()
	}
	action := dialog.ActionCopy
	title := "Copy"
	if kind == filemanager.OpMove {
		action = dialog.ActionMove
		title = "Move"
	}
	prompt := fmt.Sprintf("%s %s to:", title, dialog.SummarizeSources(sources))
	pending := &filemanager.FileOp{
		Kind:    kind,
		Sources: sources,
		Dest:    dest,
	}
	dlg := dialog.NewInputDialog(action, title, prompt, dest, pending)
	dlg.OnCancel = w.closeDialog
	dlg.OnSubmitInput = w.onDialogSubmitInput
	w.mu.Lock()
	w.dialog = dlg
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) openDeleteDialog() {
	sources := w.opSources()
	if len(sources) == 0 {
		return
	}
	prompt := fmt.Sprintf("Delete %s?", dialog.SummarizeSources(sources))
	pending := &filemanager.FileOp{
		Kind:    filemanager.OpDelete,
		Sources: sources,
	}
	dlg := dialog.NewConfirmDialog(dialog.ActionDelete, "Delete", prompt, pending)
	dlg.OnCancel = w.closeDialog
	dlg.OnConfirm = func(_ dialog.DialogAction, op *filemanager.FileOp) {
		w.closeDialog()
		if op != nil {
			w.startFileOp(*op)
		}
	}
	w.mu.Lock()
	w.dialog = dlg
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) onDialogSubmitInput(action dialog.DialogAction, val string, pending *filemanager.FileOp) {
	w.closeDialog()
	active := w.panels.ActivePane()
	if active == nil {
		return
	}
	dir := active.Dir()
	switch action {
	case dialog.ActionMkdir:
		target := val
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, val)
		}
		if err := os.MkdirAll(target, 0o755); err == nil {
			_ = active.LoadDirFocus(dir, filepath.Base(target))
			if other := w.panels.OtherPane(); other != nil {
				_ = other.Refresh()
			}
		}
	case dialog.ActionRename:
		if item, ok := active.SelectedItem(); ok && item.Name() != ".." {
			oldPath := filepath.Join(dir, item.Name())
			newPath := val
			if !filepath.IsAbs(newPath) {
				newPath = filepath.Join(dir, val)
			}
			if err := os.Rename(oldPath, newPath); err == nil {
				_ = active.LoadDirFocus(dir, filepath.Base(newPath))
				if other := w.panels.OtherPane(); other != nil {
					_ = other.Refresh()
				}
			}
		}
	case dialog.ActionCopy, dialog.ActionMove:
		if pending != nil {
			dest := val
			if !filepath.IsAbs(dest) {
				dest = filepath.Join(dir, val)
			}
			pending.Dest = dest
			w.startFileOp(*pending)
		}
	}
	w.invalidate()
}

func (w *AppWidget) startFileOp(op filemanager.FileOp) {
	ctx, cancel := context.WithCancel(context.Background())
	resume := make(chan filemanager.CollisionChoice, 1)

	w.mu.Lock()
	if w.opCancel != nil {
		w.opCancel()
	}
	w.opCancel = cancel
	w.opResume = resume
	w.dialog = dialog.NewProgressDialog(op.Kind.Verb(), filemanager.FileOpProgress{}, func() {
		cancel()
		w.closeDialog()
	})
	w.mu.Unlock()
	w.invalidate()

	go func() {
		_ = filemanager.RunFileOp(
			ctx,
			op,
			func(prog filemanager.FileOpProgress) {
				w.mu.Lock()
				if w.dialog != nil && w.dialog.Kind == dialog.DialogProgress {
					w.dialog.Progress = prog
				}
				w.mu.Unlock()
				w.invalidate()
			},
			func(collidePath string) {
				choices := []dialog.Choice{
					{Label: "Overwrite", Hotkey: "o"},
					{Label: "Skip", Hotkey: "s"},
					{Label: "All", Hotkey: "a"},
					{Label: "Cancel", Hotkey: "c"},
				}
				w.mu.Lock()
				w.dialog = dialog.NewChoiceDialog(
					"File Exists",
					"Overwrite "+filepath.Base(collidePath)+"?",
					choices,
					func(idx int) {
						var choice filemanager.CollisionChoice
						switch idx {
						case 0:
							choice = filemanager.ColOverwrite
						case 1:
							choice = filemanager.ColSkip
						case 2:
							choice = filemanager.ColOverwriteAll
						default:
							choice = filemanager.ColCancel
						}
						w.mu.Lock()
						w.dialog = dialog.NewProgressDialog(op.Kind.Verb(), filemanager.FileOpProgress{}, func() {
							cancel()
							w.closeDialog()
						})
						w.mu.Unlock()
						w.invalidate()
						resume <- choice
					},
				)
				w.mu.Unlock()
				w.invalidate()
			},
			resume,
		)

		w.mu.Lock()
		w.opCancel = nil
		w.opResume = nil
		w.dialog = nil
		w.mu.Unlock()

		if w.panels != nil {
			if w.panels.Left != nil {
				_ = w.panels.Left.Refresh()
			}
			if w.panels.Right != nil {
				_ = w.panels.Right.Refresh()
			}
		}
		w.invalidate()
	}()
}

func (w *AppWidget) openEditorOnSelected() {
	if w.panels == nil {
		return
	}
	active := w.panels.ActivePane()
	if active == nil {
		return
	}
	item, ok := active.SelectedItem()
	if !ok || item.IsDir() || item.Name() == ".." {
		return
	}
	fullPath := filepath.Join(active.Dir(), item.Name())
	_ = w.OpenEditor(fullPath, false)
}
