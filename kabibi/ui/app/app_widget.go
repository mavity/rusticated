package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/browser"
	"github.com/mavity/rusticated/kabibi/slashcmd"
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

	slashPrefix string
	slashIndex  int

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
	reportMetadataError := func(path string, err error) {
		if w.writeScrollback != nil {
			w.writeScrollback(fmt.Sprintf("Unable to refresh metadata for %q: %v\n", path, err))
		}
	}
	w.panels.Left.OnMetadataError = reportMetadataError
	w.panels.Right.OnMetadataError = reportMetadataError

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
	if !v && w.panels != nil {
		w.panels.Left.CloseSearch()
		w.panels.Right.CloseSearch()
	}
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

func (w *AppWidget) insertSelectedFileIntoPrompt(absolutePath bool) {
	if w.panels == nil {
		return
	}
	active := w.panels.ActivePane()
	if active == nil {
		return
	}
	item, ok := active.SelectedItem()
	if !ok || item.Name() == ".." {
		return
	}
	name := item.Name()
	if absolutePath {
		fullPath, err := filepath.Abs(filepath.Join(active.Dir(), name))
		if err != nil {
			if w.writeScrollback != nil {
				w.writeScrollback(fmt.Sprintf("Unable to resolve absolute path for %q: %v\n", name, err))
			}
			return
		}
		name = fullPath
	}

	inserted := []rune(shellQuoteFilename(name))
	promptInput := w.sh.Input()
	value := []rune(promptInput.Value())
	cursor := promptInput.CursorPos()
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(value) {
		cursor = len(value)
	}
	updated := make([]rune, 0, len(value)+len(inserted))
	updated = append(updated, value[:cursor]...)
	updated = append(updated, inserted...)
	updated = append(updated, value[cursor:]...)
	promptInput.SetValue(string(updated))
	promptInput.SetCursorPos(cursor + len(inserted))
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
	w.renderSlashHints(&shellBuf)

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

		inputValue := w.sh.Input().Value()
		_, _, isSlashInput := slashcmd.InputToken(inputValue)
		if isSlashInput || (inputValue == "" && keyEv.Text == "/") {
			if w.handleSlashKey(keyEv) {
				return true
			}
			return w.sh.HandleEvent(keyEv)
		}

		// When blue panels are visible, apply the Far Manager Split Key-Routing Table
		if panelsVis && w.panels != nil {
			active := w.panels.ActivePane()
			if active != nil && active.SearchActive() {
				switch keyStr {
				case "esc":
					active.CloseSearch()
					w.invalidate()
					return true
				case "enter":
					active.CloseSearch()
					active.ActivateSelected()
					w.invalidate()
					return true
				case "up", "down":
					active.CloseSearch()
					active.HandleEvent(keyEv)
					w.invalidate()
					return true
				default:
					if active.HandleSearchEvent(keyEv) {
						w.invalidate()
						return true
					}
					active.CloseSearch()
				}
			} else if active != nil && active.BeginSearch(keyEv) {
				w.invalidate()
				return true
			}

			hasInput := w.sh.Input().Value() != ""

			if keyEv.Code == input.KeyEnter && keyEv.Mod.Contains(input.ModCtrl) && keyEv.Mod.Contains(input.ModShift) {
				w.insertSelectedFileIntoPrompt(true)
				w.invalidate()
				return true
			}

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
				w.insertSelectedFileIntoPrompt(false)
				w.invalidate()
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

		if keyStr == "esc" {
			if w.sh.Input().Value() != "" {
				w.sh.Input().Reset()
				w.invalidate()
				return true
			}
			if !panelsVis {
				w.SetPanelsVisible(true)
				return true
			}
		}
	}

	return w.sh.HandleEvent(e)
}

func (w *AppWidget) slashMatches() ([]slashcmd.Command, int) {
	token, args, isSlash := slashcmd.InputToken(w.sh.Input().Value())
	if !isSlash || strings.Count(token, "/") != 1 {
		w.mu.Lock()
		w.slashPrefix = ""
		w.slashIndex = 0
		w.mu.Unlock()
		return nil, 0
	}
	matches := slashcmd.Matches(token, browser.Available())
	w.mu.Lock()
	if token != w.slashPrefix {
		w.slashPrefix = token
		w.slashIndex = 0
	}
	if len(matches) == 0 {
		w.slashIndex = 0
	} else if w.slashIndex >= len(matches) {
		w.slashIndex = len(matches) - 1
	}
	index := w.slashIndex
	w.mu.Unlock()
	if args != "" {
		return nil, index
	}
	return matches, index
}

func (w *AppWidget) renderSlashHints(buf *terminal.CellBuf) {
	matches, selected := w.slashMatches()
	if len(matches) == 0 || buf.Width == 0 || buf.Height == 0 {
		return
	}

	y := buf.Height - 1
	plain := terminal.NewStyle(0, 0, terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault)
	buf.Fill(terminal.Rect{Y: y, W: buf.Width, H: 1}, terminal.Cell{Grapheme: " ", Style: plain})

	var names strings.Builder
	for i, command := range matches {
		if i > 0 {
			names.WriteString("  ")
		}
		names.WriteString(command.Name)
	}
	showDescription := false
	description := "  " + matches[selected].Description
	if runewidth.StringWidth(names.String())+runewidth.StringWidth(description) <= buf.Width {
		showDescription = true
	}

	x := 0
	draw := func(text string, style terminal.Style) {
		for _, r := range text {
			width := runewidth.RuneWidth(r)
			if width <= 0 {
				continue
			}
			if x+width > buf.Width {
				return
			}
			buf.Set(x, y, terminal.Cell{Grapheme: string(r), Style: style})
			for offset := 1; offset < width; offset++ {
				buf.Set(x+offset, y, terminal.Cell{Grapheme: "", Style: style})
			}
			x += width
		}
	}
	muted := terminal.NewStyle(0x9AA9B8, 0, terminal.CharResetBackgroundDefault)
	for i, command := range matches {
		if i > 0 {
			draw("  ", muted)
		}
		style := muted
		if i == selected {
			style = terminal.NewStyle(0xFFFFFF, 0x005F87, terminal.CharBold)
		}
		draw(command.Name, style)
	}
	if showDescription {
		draw(description, muted)
	}
}

func (w *AppWidget) handleSlashKey(key input.KeyPressEvent) bool {
	value := w.sh.Input().Value()
	token, args, isSlash := slashcmd.InputToken(value)
	if !isSlash {
		return false
	}
	if strings.Count(token, "/") > 1 {
		return false
	}
	matches, selected := w.slashMatches()
	if args != "" {
		matches = slashcmd.Matches(token, browser.Available())
	}

	switch key.String() {
	case "esc":
		w.sh.Input().Reset()
		w.invalidate()
		return true
	case "left", "right":
		if args == "" && len(matches) > 1 {
			w.mu.Lock()
			if key.String() == "left" {
				w.slashIndex = (w.slashIndex - 1 + len(matches)) % len(matches)
			} else {
				w.slashIndex = (w.slashIndex + 1) % len(matches)
			}
			w.mu.Unlock()
			w.invalidate()
			return true
		}
	case "tab":
		if args == "" && len(matches) > 1 {
			w.mu.Lock()
			w.slashIndex = (selected + 1) % len(matches)
			w.mu.Unlock()
			w.invalidate()
			return true
		}
		if args == "" && len(matches) == 1 {
			w.acceptSlashMatch(matches[selected].Name, args, args == "")
			return true
		}
	case "enter":
		line := value
		if len(matches) > 0 {
			line = matches[selected].Name
			if args != "" {
				line += " " + args
			}
		}
		invocation, recognized, err := slashcmd.Parse(line)
		if recognized {
			w.sh.Input().Reset()
			if err != nil {
				w.showSlashError(err)
			} else {
				w.dispatchSlashCommand(invocation)
			}
			w.invalidate()
			return true
		}
	}
	return false
}

func (w *AppWidget) acceptSlashMatch(name, args string, addSpace bool) {
	value := w.sh.Input().Value()
	leading := len(value) - len(strings.TrimLeft(value, " \t\r\n"))
	updated := value[:leading] + name
	if args != "" {
		updated += " " + args
	} else if addSpace {
		updated += " "
	}
	w.sh.SetInput(updated)
	w.sh.Input().SetCursorPos(len([]rune(updated)))
	w.invalidate()
}

func (w *AppWidget) dispatchSlashCommand(invocation slashcmd.Invocation) {
	command, ok := slashcmd.Lookup(invocation.Name)
	if !ok {
		w.showSlashError(fmt.Errorf("unknown slash command %q", invocation.Name))
		return
	}
	if command.BrowserOnly && !browser.Available() {
		w.showSlashError(fmt.Errorf("%s is only available in the browser", command.Name))
		return
	}

	name := command.Name
	if name == "/remove" {
		name = "/delete"
	}
	switch name {
	case "/panels":
		if len(invocation.Args) != 0 {
			w.showSlashError(slashcmd.UsageError(name, ""))
			return
		}
		w.TogglePanels()
	case "/settings":
		if len(invocation.Args) != 0 {
			w.showSlashError(slashcmd.UsageError(name, ""))
			return
		}
		w.openSettingsDialog()
	case "/model":
		if len(invocation.Args) != 0 {
			w.showSlashError(slashcmd.UsageError(name, ""))
			return
		}
		w.launchModelSetup()
	case "/download":
		if len(invocation.Args) != 0 {
			w.showSlashError(slashcmd.UsageError(name, ""))
			return
		}
		if err := browser.Download(); err != nil {
			w.showSlashError(err)
		}
	case "/edit":
		if len(invocation.Args) == 0 {
			if w.showPanelsForFileAction() {
				return
			}
			if len(w.opSources()) == 0 {
				w.showSlashError(fmt.Errorf("no file is selected"))
				return
			}
			w.openEditorOnSelected()
			return
		}
		if len(invocation.Args) != 1 {
			w.showSlashError(slashcmd.UsageError(name, " [path]"))
			return
		}
		if err := w.OpenEditor(w.slashPath(invocation.Args[0]), false); err != nil {
			w.showSlashError(fmt.Errorf("open %q: %w", invocation.Args[0], err))
		}
	case "/copy", "/move":
		if len(invocation.Args) == 0 {
			if w.showPanelsForFileAction() {
				return
			}
			if len(w.opSources()) == 0 {
				w.showSlashError(fmt.Errorf("no file is selected"))
				return
			}
			kind := filemanager.OpCopy
			if name == "/move" {
				kind = filemanager.OpMove
			}
			w.openTransferDialog(kind)
			return
		}
		if len(invocation.Args) < 2 {
			w.showSlashError(slashcmd.UsageError(name, " <source>... <destination-directory>"))
			return
		}
		sources := make([]string, len(invocation.Args)-1)
		for i, path := range invocation.Args[:len(invocation.Args)-1] {
			sources[i] = w.slashPath(path)
		}
		dest := w.slashPath(invocation.Args[len(invocation.Args)-1])
		kind := filemanager.OpCopy
		if name == "/move" {
			kind = filemanager.OpMove
		}
		w.startFileOp(filemanager.FileOp{Kind: kind, Sources: sources, Dest: dest})
	case "/delete":
		if len(invocation.Args) == 0 {
			if w.showPanelsForFileAction() {
				return
			}
			if len(w.opSources()) == 0 {
				w.showSlashError(fmt.Errorf("no file is selected"))
				return
			}
			w.openDeleteDialog()
			return
		}
		sources := make([]string, len(invocation.Args))
		for i, path := range invocation.Args {
			sources[i] = w.slashPath(path)
		}
		w.openDeleteDialogForSources(sources)
	case "/rename":
		if len(invocation.Args) == 0 {
			if w.showPanelsForFileAction() {
				return
			}
			if len(w.opSources()) == 0 {
				w.showSlashError(fmt.Errorf("no file is selected"))
				return
			}
			w.openRenameDialog()
			return
		}
		if len(invocation.Args) != 2 {
			w.showSlashError(slashcmd.UsageError(name, " <source> <destination>"))
			return
		}
		oldPath := w.slashPath(invocation.Args[0])
		newPath := w.slashPath(invocation.Args[1])
		if err := os.Rename(oldPath, newPath); err != nil {
			w.showSlashError(fmt.Errorf("rename %q to %q: %w", oldPath, newPath, err))
			return
		}
		w.refreshPanels()
	default:
		w.showSlashError(fmt.Errorf("unsupported slash command %q", name))
	}
}

func (w *AppWidget) slashPath(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.sh.Dir(), path)
	}
	return filepath.Clean(path)
}

func (w *AppWidget) showPanelsForFileAction() bool {
	if !w.PanelsVisible() {
		w.SetPanelsVisible(true)
		return true
	}
	return false
}

func (w *AppWidget) openSettingsDialog() {
	dlg := dialog.NewChoiceDialog("Settings", "Choose an option:", []dialog.Choice{
		{Label: "AI provider and model", Hotkey: "a"},
	}, func(index int) {
		w.closeDialog()
		if index == 0 {
			w.launchModelSetup()
		}
	})
	dlg.OnCancel = w.closeDialog
	w.mu.Lock()
	w.dialog = dlg
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) launchModelSetup() {
	w.sh.SetInput("ai setup")
	w.sh.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
}

func (w *AppWidget) showSlashError(err error) {
	if err == nil {
		return
	}
	dlg := dialog.NewChoiceDialog("Slash Command Error", err.Error(), []dialog.Choice{
		{Label: "[OK]", Hotkey: "o"},
	}, func(int) {
		w.closeDialog()
	})
	dlg.OnCancel = w.closeDialog
	w.mu.Lock()
	w.dialog = dlg
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) refreshPanels() {
	if w.panels == nil {
		return
	}
	if w.panels.Left != nil {
		_ = w.panels.Left.Refresh()
	}
	if w.panels.Right != nil {
		_ = w.panels.Right.Refresh()
	}
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
	w.openDeleteDialogForSources(w.opSources())
}

func (w *AppWidget) openDeleteDialogForSources(sources []string) {
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
		opErr := filemanager.RunFileOp(
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
		if opErr != nil && !errors.Is(opErr, context.Canceled) {
			w.showSlashError(fmt.Errorf("%s: %w", op.Kind.Verb(), opErr))
		}
		w.invalidate()
	}()
}

func (w *AppWidget) openEditorOnSelected() {
	if w.panels == nil {
		w.showSlashError(fmt.Errorf("file panels are unavailable"))
		return
	}
	active := w.panels.ActivePane()
	if active == nil {
		w.showSlashError(fmt.Errorf("no active file panel"))
		return
	}
	item, ok := active.SelectedItem()
	if !ok || item.Name() == ".." {
		w.showSlashError(fmt.Errorf("no file is selected"))
		return
	}
	if item.IsDir() {
		w.showSlashError(fmt.Errorf("cannot edit a directory"))
		return
	}
	fullPath := filepath.Join(active.Dir(), item.Name())
	if err := w.OpenEditor(fullPath, false); err != nil {
		w.showSlashError(fmt.Errorf("open %q: %w", fullPath, err))
	}
}
