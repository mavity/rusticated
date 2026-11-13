package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/browser"
	"github.com/mavity/rusticated/kabibi/slashcmd"
	"github.com/mavity/rusticated/kabibi/tools/filelist"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/dialog"
	"github.com/mavity/rusticated/kabibi/ui/editor"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
	"github.com/mavity/rusticated/kabibi/ui/widgets"
	"github.com/mavity/rusticated/platform"
)

const defaultPanelGapRows = 6

// AppWidget is the composite root UI coordinator implementing ui.Widget.
// It coordinates the bottom-up Shell stack, the DualPaneWidget blue HUD panels
// toggled via Ctrl+O, modal file-operation dialogs (F2/F5/F6/F7/F8), and the
// full-screen syntax-highlighted EditorWidget (F4).
type AppWidget struct {
	mu sync.Mutex

	sh                        *shell.Shell
	panels                    *filemanager.DualPaneWidget
	panelsVisible             bool
	fullScreenClaimed         bool
	closed                    bool
	disableAI                 bool
	restorePanelsAfterCommand bool

	dialog ui.Widget
	editor *editor.EditorWidget

	opCancel context.CancelFunc
	opResume chan filemanager.CollisionChoice

	pathHistory []string

	writeScrollback func(string)
	onExit          func()
	ctx             ui.RenderContext
	compositor      *terminal.Terminal
	budapest        *budapestScene
	budapestTimer   *time.Timer
}

// AppWidgetOptions contains initial parameters and service bindings for AppWidget.
type AppWidgetOptions struct {
	WriteToScrollback func(string)
	OnExit            func()
	WorkingDir        string
	ShellMode         shell.ShellMode
	PanelsVisible     bool
	FSService         filemanager.FSService
	DisableAI         bool
}

// NewWidget constructs a clean AppWidget instance with wired Shell and DualPaneWidget.
func NewWidget(opts AppWidgetOptions) *AppWidget {
	startPlatformInfoWatcher()
	w := &AppWidget{
		writeScrollback: opts.WriteToScrollback,
		onExit:          opts.OnExit,
		disableAI:       opts.DisableAI,
		compositor:      terminal.NewTerminal(1, 1),
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
		OnCommandStarted: func() {
			w.onShellCommandStarted()
		},
		WorkingDir: opts.WorkingDir,
		DisableAI:  opts.DisableAI,
		Mode:       opts.ShellMode,
		CompletionProvider: func(value string, cursor int) ([]shell.CompletionCandidate, bool) {
			return slashCompletionCandidates(value, cursor, browser.Available())
		},
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

	if EnableBudapestBackground {
		w.budapest = newBudapestScene()
	}

	return w
}

func (w *AppWidget) scheduleBudapestRedraw() {
	if !EnableBudapestBackground {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.budapestTimer == nil {
		w.budapestTimer = time.AfterFunc(budapestRedrawDelay, func() {
			w.mu.Lock()
			closed := w.closed
			ed := w.editor
			inv := w.ctx.Invalidate
			w.mu.Unlock()
			if !closed && ed == nil && inv != nil {
				inv()
			}
		})
		return
	}
	w.budapestTimer.Reset(budapestRedrawDelay)
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

// Dialog returns the currently active modal dialog widget, if any.
func (w *AppWidget) Dialog() ui.Widget {
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
	w.mu.Lock()
	w.closed = true
	w.restorePanelsAfterCommand = false
	cancel := w.opCancel
	timer := w.budapestTimer
	w.budapestTimer = nil
	w.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	w.SetPanelsVisible(false)
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
	w.mu.Lock()
	restorePanels := w.restorePanelsAfterCommand && !w.closed
	w.restorePanelsAfterCommand = false
	w.mu.Unlock()
	if restorePanels {
		w.SetPanelsVisible(true)
	}
	w.invalidate()
}

func (w *AppWidget) onShellCommandStarted() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	restorePanels := w.panelsVisible
	w.restorePanelsAfterCommand = restorePanels
	w.mu.Unlock()
	if restorePanels {
		w.SetPanelsVisible(false)
	}
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

// computePanelHeight calculates H_panels and H_gap so the bottom gap contains
// the initial banner + prompt + blank footer stack with one extra spacer row
// (expanding if PromptInput wraps), while respecting the 40% lower-area cap on short terminals.
func (w *AppWidget) computePanelHeight(totalW, totalH int) (panelH, gapH int) {
	if totalH <= 0 {
		totalH = 24
	}
	inputSz := w.sh.Input().Measure(ui.Constraints{MaxW: totalW, MaxH: totalH - 1})
	inputH := inputSz.H
	if inputH < 1 {
		inputH = 1
	}
	footerH := 1 // blank footer row
	if w.sh.Input().CompletionMenuVisible() {
		footerH = 0
	}
	gapH = 3 + inputH + 1 + footerH // 3 banner/plume peek rows + PromptInput + spacer + footer
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

	if EnableBudapestBackground || ed != nil || panelsVis || claimed {
		return ui.Size{W: maxW, H: maxH}
	}
	if dlg != nil {
		shellSize := w.sh.Measure(c)
		dialogSize := dlg.Measure(ui.Constraints{MaxW: maxW, MaxH: maxH})
		height := max(shellSize.H, dialogSize.H+3)
		if height > maxH {
			height = maxH
		}
		return ui.Size{W: maxW, H: height}
	}
	return w.sh.Measure(c)
}

func (w *AppWidget) Render(r terminal.Rect, ctx ui.RenderContext) string {
	w.mu.Lock()
	w.ctx = ctx
	panelsVis := w.panelsVisible
	ed := w.editor
	dlg := w.dialog
	w.mu.Unlock()

	if ed != nil {
		return ed.Render(r, ctx.ForChild(true))
	}

	shellANSI := w.sh.Render(r, ctx.ForChild(dlg == nil))
	shellFrame, cursor, shellHeight, err := w.compositor.RenderANSIForComposition(shellANSI, r.W, r.H)
	if err != nil {
		if ctx.OnError != nil {
			ctx.OnError(err)
		} else {
			log.Printf("render shell ANSI: %v", err)
		}
		return ""
	}
	targetH := shellHeight
	if EnableBudapestBackground && r.H > targetH {
		targetH = r.H
	}
	shellBuf := terminal.NewCellBuf(r.W, targetH)
	copy(shellBuf.Cells(), shellFrame.Cells()[:shellHeight*r.W])

	var dlgSize ui.Size
	if dlg != nil && shellBuf.Width > 0 && shellBuf.Height > 0 {
		dlgSize = dlg.Measure(ui.Constraints{MaxW: shellBuf.Width, MaxH: r.H})
		neededH := min(r.H, dlgSize.H+3)
		if !panelsVis && shellBuf.Height < neededH {
			expanded := terminal.NewCellBuf(shellBuf.Width, neededH)
			expanded.Blit(shellBuf, 0, 0)
			shellBuf = expanded
		}
	}

	if EnableBudapestBackground && w.budapest != nil {
		w.budapest.applyBudapestProjection(&shellBuf, r.W, r.H, ctx.Theme)
	}

	if panelsVis && w.panels != nil && r.W > 0 && r.H > 0 {
		panelH, _ := w.computePanelHeight(r.W, r.H)
		if panelH > 0 && panelH <= shellBuf.Height {
			panelANSI := w.panels.Render(ui.Rect{X: 0, Y: 0, W: r.W, H: panelH}, ctx.ForChild(dlg == nil))
			ctx.CompositeANSI(&shellBuf, w.compositor, terminal.Rect{W: r.W, H: panelH}, panelANSI)
		}
	}

	if dlg != nil && shellBuf.Width > 0 && shellBuf.Height > 0 {
		dx := (shellBuf.Width - dlgSize.W) / 2
		dy := (shellBuf.Height - dlgSize.H) / 2
		if shellBuf.Height >= dlgSize.H+2 && dy+dlgSize.H+2 > shellBuf.Height {
			dy = shellBuf.Height - dlgSize.H - 2
		}
		if dx < 0 {
			dx = 0
		}
		if dy < 0 {
			dy = 0
		}
		applyDialogShadow(&shellBuf, terminal.Rect{X: dx, Y: dy, W: dlgSize.W, H: dlgSize.H}, ctx.Theme)
		dlgANSI := dlg.Render(ui.Rect{X: 0, Y: 0, W: dlgSize.W, H: dlgSize.H}, ctx.ForChild(true))
		cursor = ctx.CompositeANSI(&shellBuf, w.compositor, terminal.Rect{X: dx, Y: dy, W: dlgSize.W, H: dlgSize.H}, dlgANSI)
	}

	out := ui.ANSIFrame(shellBuf, cursor)
	if EnableBudapestBackground {
		w.scheduleBudapestRedraw()
	}
	return out
}

func applyDialogShadow(buf *terminal.CellBuf, dlgRect terminal.Rect, theme ui.Theme) {
	if buf == nil || dlgRect.W <= 0 || dlgRect.H <= 0 {
		return
	}
	// Monospace cells are ~2:1 (height:width), so the shadow stretches 2x as
	// many columns horizontally (4 cols right, 2 cols left) as rows vertically
	// (2 rows down, 1 row up). The dark core is tightly bound to the dialog edge
	// with a deep initial darkness and a fast non-linear falloff outward.
	var (
		rightSteps = [5]float64{1.0, 0.25, 0.58, 0.78, 0.90} // cols 1..4 right (deep start, fast falloff)
		leftSteps  = [3]float64{1.0, 0.91, 0.96}             // cols 1..2 left (very light halo)
		downSteps  = [3]float64{1.0, 0.28, 0.78}             // rows 1..2 down (deep row 1, fast drop to row 2)
		upStep     = 0.91                                    // row 1 up
	)
	minX := max(0, dlgRect.X-2)
	maxX := min(buf.Width-1, dlgRect.X+dlgRect.W+3)
	minY := max(0, dlgRect.Y-1)
	maxY := min(buf.Height-1, dlgRect.Y+dlgRect.H+1)

	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			if x >= dlgRect.X && x < dlgRect.X+dlgRect.W && y >= dlgRect.Y && y < dlgRect.Y+dlgRect.H {
				continue
			}
			topDist := 0
			if y < dlgRect.Y {
				topDist = dlgRect.Y - y
			}
			bottomDist := 0
			if y >= dlgRect.Y+dlgRect.H {
				bottomDist = y - (dlgRect.Y + dlgRect.H) + 1
			}
			leftDist := 0
			if x < dlgRect.X {
				leftDist = dlgRect.X - x
			}
			rightDist := 0
			if x >= dlgRect.X+dlgRect.W {
				rightDist = x - (dlgRect.X + dlgRect.W) + 1
			}

			factor := 1.0
			switch {
			case topDist == 1:
				switch {
				case leftDist == 2:
					factor = 0.95
				case leftDist == 1:
					factor = 0.92
				case rightDist == 0:
					factor = upStep
				case rightDist <= 4:
					// Smooth 1-character corner taper along top-right
					darkness := (1.0 - upStep) * (1.0 - rightSteps[rightDist]) / (1.0 - rightSteps[1])
					factor = 1.0 - darkness
				}

			case bottomDist == 0 && leftDist > 0:
				if leftDist <= 2 {
					factor = leftSteps[leftDist]
					if y == dlgRect.Y {
						factor = 1.0 - (1.0-factor)*0.65
					}
				}

			case bottomDist == 0 && rightDist > 0:
				if rightDist <= 4 {
					factor = rightSteps[rightDist]
					if y == dlgRect.Y {
						// Soften the top-most row of the right shadow while keeping per-column steps
						factor = 1.0 - (1.0-factor)*0.55
					}
				}

			case bottomDist >= 1 && bottomDist <= 2:
				baseV := downSteps[bottomDist]
				vDarkness := 1.0 - baseV
				var hWeight float64
				switch {
				case leftDist == 2:
					hWeight = 0.08
				case leftDist == 1:
					hWeight = 0.28
				case x == dlgRect.X:
					hWeight = 0.72
				case x == dlgRect.X+1:
					hWeight = 0.92
				case rightDist == 0:
					hWeight = 1.0
				case rightDist <= 4:
					hWeight = (1.0 - rightSteps[rightDist]) / (1.0 - rightSteps[1])
				}
				if hWeight > 0 {
					factor = 1.0 - vDarkness*hWeight*0.7
				}
			}

			if factor < 1.0 {
				buf.Set(x, y, darkenShadowCell(buf.Get(x, y), theme, factor))
			}
		}
	}
}

const dialogShadowOpacity = 0.7

func darkenShadowCell(cell terminal.Cell, theme ui.Theme, factor float64) terminal.Cell {
	style := cell.Style
	attrs := style.Attributes()
	fg := style.Foreground()
	bg := style.Background()
	if attrs&terminal.CharResetForegroundDefault != 0 {
		fg = theme.EffectiveForeground()
		attrs &^= terminal.CharResetForegroundDefault
	}
	if attrs&terminal.CharResetBackgroundDefault != 0 {
		bg = theme.EffectiveBackground()
		attrs &^= terminal.CharResetBackgroundDefault
	}
	factor = 1 - dialogShadowOpacity*(1-factor)
	return terminal.Cell{
		Grapheme: cell.Grapheme,
		Style:    terminal.NewStyle(scaleRGB(fg, factor), scaleRGB(bg, factor), attrs),
	}
}

func scaleRGB(c terminal.Color, factor float64) terminal.Color {
	if factor <= 0 {
		return 0
	}
	if factor >= 1 {
		return c
	}
	r := uint8(float64((c>>16)&0xFF) * factor)
	g := uint8(float64((c>>8)&0xFF) * factor)
	b := uint8(float64(c&0xFF) * factor)
	return terminal.Color(uint32(r)<<16 | uint32(g)<<8 | uint32(b))
}

func (w *AppWidget) HandleEvent(e input.Event) bool {
	w.mu.Lock()
	ed := w.editor
	dlg := w.dialog
	panelsVis := w.panelsVisible
	w.mu.Unlock()

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
	if isKey && keyEv.Text == browser.MountRequestKey {
		if ed != nil || dlg != nil || w.sh.IsRunning() {
			if err := browser.CancelMountSelection(); err != nil {
				log.Printf("cancel browser directory selection: %v", err)
			}
			return true
		}
		name, err := browser.SelectedMountDirectoryName()
		if err != nil {
			w.showSlashError(err)
			return true
		}
		if name != "" {
			w.openMountPathDialog()
		}
		return true
	}

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

	if isKey && panelsVis && w.panels != nil {
		keyEv = filemanager.NormalizeMacOptionGlyph(keyEv, platform.KeyboardLayoutID())
		e = keyEv
	}

	if isKey {
		keyStr := keyEv.String()

		// Global Ctrl+O toggles the dual blue file manager panels
		if keyStr == "ctrl+o" {
			w.TogglePanels()
			return true
		}
		if panelsVis && w.panels != nil && isListFilesSearchShortcut(keyEv) {
			if active := w.panels.ActivePane(); active != nil {
				active.CloseSearch()
			}
			w.openListFilesDialog()
			return true
		}

		inputValue := w.sh.Input().Value()
		_, _, isSlashInput := slashcmd.InputToken(inputValue)
		if isSlashInput || (inputValue == "" && keyEv.Text == "/") {
			if keyStr == "esc" && w.sh.Input().CompletionVisible() {
				return w.sh.HandleEvent(keyEv)
			}
			if (keyStr == "esc" || keyStr == "enter") && w.handleSlashKey(keyEv) {
				return true
			}
			if !(panelsVis && keyStr == "tab") {
				return w.sh.HandleEvent(keyEv)
			}
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

			if keyStr == "ctrl+space" ||
				keyStr == "tab" && w.sh.Input().CompletionActive() ||
				keyStr == "esc" && w.sh.Input().CompletionVisible() {
				return w.sh.HandleEvent(keyEv)
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

			case "ctrl+enter":
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

		if keyStr == "esc" && w.sh.Input().CompletionVisible() {
			return w.sh.HandleEvent(keyEv)
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

func (w *AppWidget) handleSlashKey(key input.KeyPressEvent) bool {
	value := w.sh.Input().Value()
	token, _, isSlash := slashcmd.InputToken(value)
	if !isSlash {
		return false
	}
	if strings.Count(token, "/") > 1 {
		return false
	}

	switch key.String() {
	case "esc":
		w.sh.Input().Reset()
		w.invalidate()
		return true
	case "enter":
		invocation, recognized, err := slashcmd.Parse(value)
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
	if w.disableAI && (command.Name == "/model" || command.Name == "/settings") {
		w.showSlashError(fmt.Errorf("%s is unavailable in this shell", command.Name))
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
	case "/mount":
		if len(invocation.Args) != 0 {
			w.showSlashError(slashcmd.UsageError(name, ""))
			return
		}
		directoryName, err := browser.SelectMountDirectory()
		if err != nil {
			w.showSlashError(err)
			return
		}
		if directoryName != "" {
			w.openMountPathDialog()
		}
	case "/download":
		if len(invocation.Args) != 0 {
			w.showSlashError(slashcmd.UsageError(name, ""))
			return
		}
		if err := browser.Download(); err != nil {
			w.showSlashError(err)
		}
	case "/mkdir":
		if len(invocation.Args) == 0 {
			w.openMkdirDialog()
			return
		}
		if len(invocation.Args) != 1 {
			w.showSlashError(slashcmd.UsageError(name, " [path]"))
			return
		}
		target := w.slashPath(invocation.Args[0])
		if err := os.MkdirAll(target, 0o755); err != nil {
			w.showSlashError(fmt.Errorf("create directory %q: %w", target, err))
			return
		}
		w.refreshPanels()
	case "/edit":
		if len(invocation.Args) == 0 {
			if w.PanelsVisible() {
				if len(w.opSources()) == 0 {
					w.showSlashError(fmt.Errorf("no file is selected"))
					return
				}
				w.openEditorOnSelected()
				return
			}
			w.openEditPathDialog()
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
			if w.PanelsVisible() && len(w.opSources()) > 0 {
				w.openDeleteDialog()
			} else {
				w.openDeletePathDialog()
			}
			return
		}
		sources := make([]string, len(invocation.Args))
		for i, path := range invocation.Args {
			sources[i] = w.slashPath(path)
		}
		w.openDeleteDialogForSources(sources)
	case "/rename":
		if len(invocation.Args) == 0 {
			if w.PanelsVisible() && len(w.opSources()) > 0 {
				w.openRenameDialog()
			} else {
				w.openRenamePathDialog()
			}
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

func (w *AppWidget) setDialog(dlg ui.Widget) {
	w.mu.Lock()
	w.dialog = dlg
	w.mu.Unlock()
	w.invalidate()
}

func (w *AppWidget) openSettingsDialog() {
	dlg := dialog.NewSettingsDialog(
		dialog.SettingsValues{
			ShowPanels:  w.PanelsVisible(),
			ConfigureAI: !w.disableAI,
		},
		func(values dialog.SettingsValues) {
			w.closeDialog()
			if values.ShowPanels != w.PanelsVisible() {
				w.SetPanelsVisible(values.ShowPanels)
			}
			if values.ConfigureAI && !w.disableAI {
				w.launchModelSetup()
			}
			w.invalidate()
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) launchModelSetup() {
	w.sh.SetInput("ai setup")
	w.sh.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
}

func (w *AppWidget) showSlashError(err error) {
	if err == nil {
		return
	}
	dlg := dialog.NewErrorDialog("Slash Command Error", err.Error(), w.closeDialog)
	w.setDialog(dlg)
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

func (w *AppWidget) newPathInput(value string) *widgets.TextInput {
	var completionMu sync.Mutex
	var pendingKey, resultKey string
	var pending, resultReady bool
	var resultValue string
	var resultCursor int
	var resultOK bool
	complete := func(value string, cursor int) (string, int, bool) {
		key := fmt.Sprintf("%d:%s", cursor, value)
		completionMu.Lock()
		if resultReady && resultKey == key {
			resultReady = false
			completed, completedCursor, ok := resultValue, resultCursor, resultOK
			completionMu.Unlock()
			return completed, completedCursor, ok
		}
		if pending && pendingKey == key {
			completionMu.Unlock()
			return value, cursor, true
		}
		pending, pendingKey = true, key
		completionMu.Unlock()
		go func() {
			completed, completedCursor, ok := w.completePath(value, cursor)
			completionMu.Lock()
			if pendingKey == key {
				pending = false
				resultReady = true
				resultKey = key
				resultValue = completed
				resultCursor = completedCursor
				resultOK = ok
			}
			completionMu.Unlock()
		}()
		return value, cursor, true
	}
	return widgets.NewTextInput(widgets.TextInputOptions{
		Value: value, History: append([]string(nil), w.pathHistory...),
		Complete: complete, AllowPaste: true, SelectAll: value != "",
	})
}

func (w *AppWidget) completePath(value string, cursor int) (string, int, bool) {
	runes := []rune(value)
	if cursor < 0 || cursor > len(runes) {
		return value, cursor, false
	}
	prefix := string(runes[:cursor])
	dirPart, base := filepath.Split(prefix)
	searchDir := dirPart
	if searchDir == "" {
		searchDir = "."
	}
	searchDir = w.resolveShellPath(searchDir)
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return value, cursor, false
	}
	matches := make([]string, 0, 4)
	for _, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(base)) {
			name := entry.Name()
			if entry.IsDir() {
				name += string(filepath.Separator)
			}
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return value, cursor, false
	}
	common := matches[0]
	if len(matches) > 1 {
		for _, match := range matches[1:] {
			commonRunes := []rune(common)
			matchRunes := []rune(match)
			n := 0
			for n < len(commonRunes) && n < len(matchRunes) && commonRunes[n] == matchRunes[n] {
				n++
			}
			common = string(commonRunes[:n])
		}
		if len([]rune(common)) <= len([]rune(base)) {
			return value, cursor, false
		}
	}
	completed := string(runes[:cursor-len([]rune(base))]) + common + string(runes[cursor:])
	newCursor := cursor - len([]rune(base)) + len([]rune(common))
	return completed, newCursor, true
}

func (w *AppWidget) rememberPaths(values ...string) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(w.pathHistory) > 0 && w.pathHistory[len(w.pathHistory)-1] == value {
			continue
		}
		w.pathHistory = append(w.pathHistory, value)
		if len(w.pathHistory) > 50 {
			w.pathHistory = append([]string(nil), w.pathHistory[len(w.pathHistory)-50:]...)
		}
	}
}

func (w *AppWidget) resolveShellPath(path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.sh.Dir(), path)
	}
	return filepath.Clean(path)
}

func (w *AppWidget) resolveMountGuestPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("mount path cannot be empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for mount path %q: %w", path, err)
		}
		if home == "" {
			return "", fmt.Errorf("resolve home directory for mount path %q: home directory is empty", path)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	} else if !filepath.IsAbs(path) {
		cwd := w.sh.Dir()
		if cwd == "" {
			return "", fmt.Errorf("resolve relative mount path %q: current directory is empty", path)
		}
		path = filepath.Join(cwd, path)
	}

	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("mount path %q does not resolve to an absolute path", path)
	}
	return path, nil
}

func isListFilesSearchShortcut(key input.KeyPressEvent) bool {
	if (key.Code == 'f' || key.Code == 'F') &&
		(key.Mod.Contains(input.ModCtrl) || key.Mod.Contains(input.ModMeta)) {
		return true
	}
	return key.Code == input.KeyF7 &&
		(key.Mod.Contains(input.ModAlt) || key.Mod.Contains(input.ModCtrl) || key.Mod.Contains(input.ModMeta))
}

func findFilesCommand(findPaths, findContent string, maxResults int) string {
	var command strings.Builder
	command.WriteString("ai find-files")
	if findPaths != "" {
		command.WriteString(" --find-paths '")
		command.WriteString(strings.ReplaceAll(findPaths, "'", "'\\''"))
		command.WriteByte('\'')
	}
	if findContent != "" {
		command.WriteString(" --find-content '")
		command.WriteString(strings.ReplaceAll(findContent, "'", "'\\''"))
		command.WriteByte('\'')
	}
	command.WriteString(" --max-results ")
	command.WriteString(strconv.Itoa(maxResults))
	return command.String()
}

func (w *AppWidget) openListFilesDialog() {
	dlg := dialog.NewFindFilesDialog(
		w.newPathInput("**"),
		"**",
		"",
		strconv.Itoa(filelist.DefaultMaxResults),
		func(findPaths, findContent, maxResultsText string) {
			maxResults, err := strconv.Atoi(maxResultsText)
			if err != nil {
				w.showSlashError(fmt.Errorf("maximum results must be an integer"))
				return
			}
			command := findFilesCommand(findPaths, findContent, maxResults)
			w.closeDialog()
			w.sh.SetInput(command)
			w.sh.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) openMkdirDialog() {
	dlg := dialog.NewMkdirDialog(
		w.newPathInput(""),
		func(dir string) {
			w.rememberPaths(dir)
			w.closeDialog()
			target := w.resolveShellPath(dir)
			if err := os.MkdirAll(target, 0o755); err != nil {
				w.showSlashError(fmt.Errorf("create directory %q: %w", target, err))
				return
			}
			w.refreshPanels()
			w.invalidate()
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) openMountPathDialog() {
	defaultPath := w.sh.Dir()
	dlg := dialog.NewMountDialog(
		w.newPathInput(defaultPath),
		defaultPath,
		func(val string) {
			w.rememberPaths(val)
			w.closeDialog()
			mountPath, err := w.resolveMountGuestPath(val)
			if err != nil {
				w.showSlashError(err)
				return
			}
			if err := browser.MountDirectory(mountPath); err != nil {
				w.showSlashError(err)
				return
			}
			w.refreshPanels()
			w.invalidate()
		},
		func() {
			w.closeDialog()
			if err := browser.CancelMountSelection(); err != nil {
				w.showSlashError(err)
			}
		},
	)
	w.setDialog(dlg)
}

func (w *AppWidget) openRenameDialog() {
	var sources []string
	if w.PanelsVisible() {
		sources = w.opSources()
	}
	if len(sources) == 0 {
		w.openRenamePathDialog()
		return
	}
	if len(sources) > 1 {
		w.showSlashError(fmt.Errorf("rename accepts one source at a time"))
		return
	}
	oldPath := sources[0]
	name := filepath.Base(oldPath)
	dlg := dialog.NewRenameSelectedDialog(
		name,
		w.newPathInput(name),
		func(val string) {
			w.rememberPaths(val)
			w.closeDialog()
			newPath := w.resolveShellPath(val)
			if err := os.Rename(oldPath, newPath); err != nil {
				w.showSlashError(fmt.Errorf("rename %q to %q: %w", oldPath, newPath, err))
				return
			}
			w.refreshPanels()
			w.invalidate()
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) openRenamePathDialog() {
	dlg := dialog.NewRenamePathDialog(
		w.newPathInput(""),
		w.newPathInput(""),
		func(src, dst string) {
			w.rememberPaths(src, dst)
			oldPath, newPath := w.resolveShellPath(src), w.resolveShellPath(dst)
			w.closeDialog()
			if err := os.Rename(oldPath, newPath); err != nil {
				w.showSlashError(fmt.Errorf("rename %q to %q: %w", oldPath, newPath, err))
				return
			}
			w.refreshPanels()
			w.invalidate()
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) openTransferDialog(kind filemanager.FileOpKind) {
	var sources []string
	dest := "."
	if w.PanelsVisible() {
		sources = w.opSources()
		dest = w.panels.OtherDir()
		if dest == "" {
			dest = w.panels.ActiveDir()
		}
	}
	submitSelected := func(val string) {
		w.rememberPaths(val)
		w.closeDialog()
		w.startFileOp(filemanager.FileOp{
			Kind:    kind,
			Sources: sources,
			Dest:    w.resolveShellPath(val),
		})
	}
	submitPaths := func(src, dst string) {
		w.rememberPaths(src, dst)
		w.closeDialog()
		w.startFileOp(filemanager.FileOp{
			Kind:    kind,
			Sources: []string{w.resolveShellPath(src)},
			Dest:    w.resolveShellPath(dst),
		})
	}
	if kind == filemanager.OpMove {
		if len(sources) == 0 {
			w.setDialog(dialog.NewMovePathDialog(w.newPathInput(""), w.newPathInput(dest), submitPaths, w.closeDialog))
		} else {
			w.setDialog(dialog.NewMoveSelectedDialog(dialog.SummarizeSources(sources), w.newPathInput(dest), submitSelected, w.closeDialog))
		}
		return
	}
	if len(sources) == 0 {
		w.setDialog(dialog.NewCopyPathDialog(w.newPathInput(""), w.newPathInput(dest), submitPaths, w.closeDialog))
	} else {
		w.setDialog(dialog.NewCopySelectedDialog(dialog.SummarizeSources(sources), w.newPathInput(dest), submitSelected, w.closeDialog))
	}
}

func (w *AppWidget) openDeleteDialog() {
	sources := w.opSources()
	if !w.PanelsVisible() || len(sources) == 0 {
		w.openDeletePathDialog()
		return
	}
	w.openDeleteDialogForSources(sources)
}

func (w *AppWidget) openDeletePathDialog() {
	dlg := dialog.NewDeletePathDialog(
		w.newPathInput(""),
		func(value string) {
			w.rememberPaths(value)
			w.openDeleteDialogForSources([]string{w.resolveShellPath(value)})
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) openDeleteDialogForSources(sources []string) {
	if len(sources) == 0 {
		return
	}
	op := filemanager.FileOp{
		Kind:    filemanager.OpDelete,
		Sources: sources,
	}
	dlg := dialog.NewDeleteConfirmDialog(
		dialog.SummarizeSources(sources),
		func() {
			w.closeDialog()
			w.startFileOp(op)
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) openEditPathDialog() {
	dlg := dialog.NewEditDialog(
		w.newPathInput(""),
		func(val string) {
			w.rememberPaths(val)
			w.closeDialog()
			if err := w.OpenEditor(w.resolveShellPath(val), false); err != nil {
				w.showSlashError(fmt.Errorf("open %q: %w", val, err))
			}
			w.invalidate()
		},
		w.closeDialog,
	)
	w.setDialog(dlg)
}

func (w *AppWidget) startFileOp(op filemanager.FileOp) {
	ctx, cancel := context.WithCancel(context.Background())
	resume := make(chan filemanager.CollisionChoice, 1)
	progressDialog := dialog.NewProgressDialog(op.Kind.Verb(), filemanager.FileOpProgress{}, func() {
		cancel()
		w.closeDialog()
	})

	w.mu.Lock()
	if w.opCancel != nil {
		w.opCancel()
	}
	w.opCancel = cancel
	w.opResume = resume
	w.dialog = progressDialog
	w.mu.Unlock()
	w.invalidate()

	go func() {
		opErr := filemanager.RunFileOp(
			ctx,
			op,
			func(prog filemanager.FileOpProgress) {
				w.mu.Lock()
				if w.dialog == progressDialog {
					progressDialog.SetProgress(prog)
				}
				w.mu.Unlock()
				w.invalidate()
			},
			func(collidePath string) {
				w.mu.Lock()
				if w.dialog == progressDialog {
					progressDialog.SetCollision(collidePath, func(choice filemanager.CollisionChoice) {
						resume <- choice
					})
				}
				w.mu.Unlock()
				w.invalidate()
			},
			resume,
		)

		w.mu.Lock()
		if w.opResume == resume {
			w.opCancel = nil
			w.opResume = nil
		}
		if w.dialog == progressDialog {
			w.dialog = nil
		}
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
