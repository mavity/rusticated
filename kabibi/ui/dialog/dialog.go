package dialog

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
	"github.com/mavity/rusticated/kabibi/ui/widgets"
)

type (
	Constraints   = ui.Constraints
	Size          = ui.Size
	Rect          = ui.Rect
	CellBuf       = terminal.CellBuf
	Cell          = terminal.Cell
	CursorPos     = ui.CursorPos
	RenderContext = ui.RenderContext
	Widget        = ui.Widget
)

var NewCellBuf = terminal.NewCellBuf

const (
	colorDlgBg     terminal.Color = 0x2D3642
	colorDlgBorder terminal.Color = 0xFFD700
	colorDlgText   terminal.Color = 0xE5E5E5
	colorDlgMuted  terminal.Color = 0x9AA6B8
	colorBtnBg     terminal.Color = 0xFFD700
	colorBtnFg     terminal.Color = 0x1A1F26
	colorBtnAltBg  terminal.Color = 0x3D4856
	colorBtnAltFg  terminal.Color = 0xE5E5E5
	colorWarnText  terminal.Color = 0xFF6B6B
)

func makeCell(r rune, fg, bg terminal.Color, bold bool) Cell {
	var attrs terminal.CharAttributes
	if bold {
		attrs |= terminal.CharBold
	}
	return Cell{Grapheme: string(r), Style: terminal.NewStyle(fg, bg, attrs)}
}

func makeStyle(fg, bg terminal.Color, bold bool) terminal.Style {
	var attrs terminal.CharAttributes
	if bold {
		attrs |= terminal.CharBold
	}
	return terminal.NewStyle(fg, bg, attrs)
}

func truncateStringToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	var b strings.Builder
	width := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if rw <= 0 {
			continue
		}
		if width+rw > maxWidth-1 {
			break
		}
		b.WriteRune(r)
		width += rw
	}
	return b.String() + "…"
}

func stableDialogWidth(availW, minWidth, maxWidth int) int {
	if availW <= 0 {
		availW = 80
	}
	target := availW * 3 / 4
	if target < minWidth {
		target = minWidth
	}
	if maxWidth > 0 && target > maxWidth {
		target = maxWidth
	}
	if target > availW {
		target = availW
	}
	if target < 1 {
		target = 1
	}
	return target
}

func dialogInnerSpan(width int) (innerX, innerW int) {
	if width >= 6 {
		return 2, width - 4
	}
	if width >= 3 {
		return 1, width - 2
	}
	return 0, max(1, width)
}

func drawDialogBorder(buf *CellBuf, width, height int, title string) {
	border := makeStyle(colorDlgBorder, colorDlgBg, false)
	for x := 1; x < width-1; x++ {
		buf.Set(x, 0, Cell{Grapheme: "─", Style: border})
		buf.Set(x, height-1, Cell{Grapheme: "─", Style: border})
	}
	for y := 1; y < height-1; y++ {
		buf.Set(0, y, Cell{Grapheme: "│", Style: border})
		buf.Set(width-1, y, Cell{Grapheme: "│", Style: border})
	}
	buf.Set(0, 0, Cell{Grapheme: "╭", Style: border})
	buf.Set(width-1, 0, Cell{Grapheme: "╮", Style: border})
	buf.Set(0, height-1, Cell{Grapheme: "╰", Style: border})
	buf.Set(width-1, height-1, Cell{Grapheme: "╯", Style: border})
	if title == "" || width <= 4 {
		return
	}
	titleText := " " + title + " "
	titleWidth := runewidth.StringWidth(titleText)
	if titleWidth > width-4 {
		titleText = truncateStringToWidth(titleText, width-4)
		titleWidth = runewidth.StringWidth(titleText)
	}
	start := max(1, (width-titleWidth)/2)
	drawText(buf, start, 0, titleText, min(titleWidth, width-start-1), makeStyle(colorBtnFg, colorDlgBorder, true))
}

func drawText(buf *CellBuf, x, y int, text string, maxWidth int, style terminal.Style) {
	if y < 0 || y >= buf.Height || maxWidth <= 0 {
		return
	}
	col := x
	for _, r := range text {
		width := runewidth.RuneWidth(r)
		if width <= 0 {
			continue
		}
		if col+width > x+maxWidth || col+width > buf.Width {
			return
		}
		buf.Set(col, y, Cell{Grapheme: string(r), Style: style})
		for i := 1; i < width; i++ {
			buf.Set(col+i, y, Cell{Grapheme: "", Style: style})
		}
		col += width
	}
}

type dialogShell struct {
	Title      string
	MinWidth   int
	MaxWidth   int
	Layout     *widgets.Grid
	FocusOrder []ui.Widget
	FocusIndex int
	OnSubmit   func()
	OnCancel   func()

	Width      int
	Height     int
	X          int
	Y          int
	compositor *terminal.Terminal
}

func (s *dialogShell) Measure(c Constraints) Size {
	if s == nil || s.Layout == nil {
		return Size{}
	}
	availW := c.MaxW
	if availW <= 0 {
		availW = 80
	}
	availH := c.MaxH
	if availH <= 0 {
		availH = 24
	}
	width := stableDialogWidth(availW, s.MinWidth, s.MaxWidth)
	_, innerW := dialogInnerSpan(width)
	chromeH := 2
	if availH < 3 {
		chromeH = 0
	}
	gridSize := s.Layout.Measure(Constraints{MaxW: innerW, MaxH: max(1, availH-chromeH)})
	var height int
	if availH >= gridSize.H+5 {
		height = gridSize.H + 5
	} else if availH >= gridSize.H+4 {
		height = gridSize.H + 4
	} else if availH >= gridSize.H+2 {
		height = gridSize.H + 2
	} else {
		height = availH
	}
	height = max(1, min(height, availH))
	return Size{W: width, H: height}
}

func (s *dialogShell) Render(r Rect, ctx RenderContext) string {
	if s == nil {
		return ""
	}
	if r.W <= 0 || r.H <= 0 {
		sz := s.Measure(Constraints{MaxW: r.W, MaxH: r.H})
		if r.W <= 0 {
			r.W = sz.W
		}
		if r.H <= 0 {
			r.H = sz.H
		}
	}
	s.X, s.Y, s.Width, s.Height = r.X, r.Y, r.W, r.H
	buf, cursor := s.RenderBufWithContext(ctx)
	return ui.ANSIFrame(buf, cursor)
}

func (s *dialogShell) RenderBuf() CellBuf {
	buf, _ := s.RenderBufWithContext(RenderContext{Focused: true})
	return buf
}

func (s *dialogShell) RenderBufWithContext(ctx RenderContext) (CellBuf, CursorPos) {
	if s == nil {
		return terminal.NewCellBuf(0, 0), CursorPos{}
	}
	width, height := s.Width, s.Height
	if width <= 0 || height <= 0 {
		sz := s.Measure(Constraints{})
		if width <= 0 {
			width = sz.W
		}
		if height <= 0 {
			height = sz.H
		}
	}
	buf := terminal.NewCellBuf(width, height)
	buf.Fill(Rect{W: width, H: height}, makeCell(' ', colorDlgText, colorDlgBg, false))
	if width < 2 || height < 2 || s.Layout == nil {
		return buf, CursorPos{}
	}
	drawDialogBorder(&buf, width, height, s.Title)
	s.syncFocus()
	innerX, innerW := dialogInnerSpan(width)
	innerY := 1
	maxInnerH := max(1, height-2)
	gridSize := s.Layout.Measure(Constraints{MaxW: innerW, MaxH: maxInnerH})
	innerH := maxInnerH
	if height >= gridSize.H+5 {
		innerY = 2
		innerH = gridSize.H
		sepY := innerY + gridSize.H - 1
		if sepY > innerY && sepY < height-2 {
			// Leave a clean 1-row visual gap above the bottom button bar by rendering
			// top content at innerY and the final row 1 line lower.
			innerH = gridSize.H
		}
	} else if height >= gridSize.H+4 {
		innerY = 2
		innerH = gridSize.H
	} else if height >= gridSize.H+2 {
		innerY = 1
		innerH = gridSize.H
	}
	gridANSI := s.Layout.Render(Rect{W: innerW, H: innerH}, ctx)
	cursor := ctx.CompositeANSI(&buf, s.ansiTerminal(), Rect{X: innerX, Y: innerY, W: innerW, H: innerH}, gridANSI)
	if height >= gridSize.H+5 && gridSize.H >= 2 {
		// Shift the bottom action row down by 1 line so there is a 1-row spacer
		// between the form fields and the centered action buttons.
		btnSrcY := innerY + gridSize.H - 1
		btnDstY := btnSrcY + 1
		if btnDstY < height-1 {
			for x := innerX; x < innerX+innerW; x++ {
				buf.Set(x, btnDstY, buf.Get(x, btnSrcY))
				buf.Set(x, btnSrcY, makeCell(' ', colorDlgText, colorDlgBg, false))
			}
			if cursor.Visible && cursor.Y == btnSrcY {
				cursor.Y = btnDstY
			}
		}
	}
	return buf, cursor
}

func (s *dialogShell) HandleEvent(event input.Event) bool {
	if s == nil || s.Layout == nil {
		return false
	}
	s.syncFocus()
	ev, isKey := keyEvent(event)
	if !isKey {
		return s.Layout.HandleEvent(event)
	}
	key := strings.ToLower(ev.String())
	if key == "esc" {
		if s.OnCancel != nil {
			s.OnCancel()
		}
		return true
	}

	focused := s.focusedWidget()
	switch ctrl := focused.(type) {
	case *widgets.TextInput:
		switch key {
		case "enter":
			if s.OnSubmit != nil {
				s.OnSubmit()
			}
			return true
		case "tab":
			if ctrl.HandleEvent(ev) {
				return true
			}
			s.moveFocus(1)
			return true
		case "shift+tab":
			s.moveFocus(-1)
			return true
		case "up":
			if s.textInputCount() == 1 && ctrl.HandleEvent(ev) {
				return true
			}
			s.moveFocus(-1)
			return true
		case "down":
			if s.textInputCount() == 1 && ctrl.HandleEvent(ev) {
				return true
			}
			s.moveFocus(1)
			return true
		default:
			ctrl.HandleEvent(ev)
			return true
		}

	case *widgets.Checkbox:
		switch key {
		case "enter":
			if s.OnSubmit != nil {
				s.OnSubmit()
			}
			return true
		case "tab", "down":
			s.moveFocus(1)
			return true
		case "shift+tab", "up":
			s.moveFocus(-1)
			return true
		default:
			if ctrl.HandleEvent(ev) {
				return true
			}
			s.activateHotkeys(ev)
			return true
		}

	case *widgets.ButtonRow:
		if len(s.FocusOrder) > 1 {
			switch key {
			case "tab", "down":
				s.moveFocus(1)
				return true
			case "shift+tab", "up":
				s.moveFocus(-1)
				return true
			}
		}
		if ctrl.HandleEvent(ev) {
			return true
		}
		s.activateHotkeys(ev)
		return true

	default:
		if s.Layout.HandleEvent(ev) {
			return true
		}
		s.activateHotkeys(ev)
		return true
	}
}

func (s *dialogShell) activateHotkeys(ev input.KeyPressEvent) bool {
	for _, w := range s.FocusOrder {
		switch ctrl := w.(type) {
		case *widgets.ButtonRow:
			if ctrl.ActivateHotkey(ev) {
				return true
			}
		case *widgets.Checkbox:
			if ctrl.HandleEvent(ev) {
				return true
			}
		}
	}
	return false
}

func (s *dialogShell) textInputCount() int {
	count := 0
	for _, w := range s.FocusOrder {
		if _, ok := w.(*widgets.TextInput); ok {
			count++
		}
	}
	return count
}

func (s *dialogShell) focusedWidget() ui.Widget {
	if len(s.FocusOrder) == 0 {
		return nil
	}
	if s.FocusIndex < 0 {
		s.FocusIndex = 0
	}
	if s.FocusIndex >= len(s.FocusOrder) {
		s.FocusIndex = len(s.FocusOrder) - 1
	}
	return s.FocusOrder[s.FocusIndex]
}

func (s *dialogShell) syncFocus() {
	if s.Layout == nil {
		return
	}
	s.Layout.SetFocusedWidget(s.focusedWidget())
}

func (s *dialogShell) moveFocus(step int) {
	if len(s.FocusOrder) == 0 {
		return
	}
	s.FocusIndex = (s.FocusIndex + step + len(s.FocusOrder)) % len(s.FocusOrder)
	s.syncFocus()
}

func (s *dialogShell) ansiTerminal() *terminal.Terminal {
	if s.compositor == nil {
		s.compositor = terminal.NewTerminal(1, 1)
	}
	return s.compositor
}

func newDefaultPathInput(initial string) *widgets.TextInput {
	return widgets.NewTextInput(widgets.TextInputOptions{
		Value:      initial,
		SelectAll:  initial != "",
		AllowPaste: true,
	})
}

func ensureInput(in *widgets.TextInput, initial string) *widgets.TextInput {
	if in != nil {
		return in
	}
	return newDefaultPathInput(initial)
}

// ── 1. MkdirDialog ────────────────────────────────────────────────────────────

type MkdirDialog struct {
	Prompt    *widgets.Label
	Directory *widgets.TextInput
	Create    *widgets.Button
	Cancel    *widgets.Button
	Buttons   *widgets.ButtonRow
	Layout    *widgets.Grid

	OnSubmit func(directory string)
	OnCancel func()

	shell dialogShell
}

func NewMkdirDialog(directory *widgets.TextInput, onSubmit func(string), onCancel func()) *MkdirDialog {
	d := &MkdirDialog{
		Prompt:    &widgets.Label{Text: "Create directory:"},
		Directory: ensureInput(directory, ""),
		OnSubmit:  onSubmit,
		OnCancel:  onCancel,
	}
	d.Create = &widgets.Button{Label: "Create", Hotkey: "c", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Create, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Prompt},
			{Row: 1, Child: d.Directory},
			{Row: 2, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Make Directory",
		MinWidth:   36,
		MaxWidth:   60,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Directory, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *MkdirDialog) submit() {
	val := strings.TrimSpace(d.Directory.Value())
	if val == "" {
		d.cancel()
		return
	}
	if d.OnSubmit != nil {
		d.OnSubmit(val)
	}
}

func (d *MkdirDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *MkdirDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *MkdirDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *MkdirDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *MkdirDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 2. RenameDialog ───────────────────────────────────────────────────────────

type RenameDialog struct {
	Prompt       *widgets.Label
	SourceLabel  *widgets.Label
	Source       *widgets.TextInput
	NewPathLabel *widgets.Label
	NewPath      *widgets.TextInput
	Rename       *widgets.Button
	Cancel       *widgets.Button
	Buttons      *widgets.ButtonRow
	Layout       *widgets.Grid

	OnSubmitSelected func(newName string)
	OnSubmitPaths    func(source, newPath string)
	OnCancel         func()

	shell dialogShell
}

func NewRenameSelectedDialog(selectedName string, newPath *widgets.TextInput, onSubmit func(newName string), onCancel func()) *RenameDialog {
	d := &RenameDialog{
		Prompt:           &widgets.Label{Text: "Rename " + selectedName + " to:"},
		NewPath:          ensureInput(newPath, selectedName),
		OnSubmitSelected: onSubmit,
		OnCancel:         onCancel,
	}
	d.Rename = &widgets.Button{Label: "Rename", Hotkey: "r", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Rename, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Prompt},
			{Row: 1, Child: d.NewPath},
			{Row: 2, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Rename",
		MinWidth:   36,
		MaxWidth:   60,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.NewPath, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func NewRenamePathDialog(source, newPath *widgets.TextInput, onSubmit func(source, newPath string), onCancel func()) *RenameDialog {
	d := &RenameDialog{
		SourceLabel:   &widgets.Label{Text: "Source path"},
		Source:        ensureInput(source, ""),
		NewPathLabel:  &widgets.Label{Text: "New path"},
		NewPath:       ensureInput(newPath, ""),
		OnSubmitPaths: onSubmit,
		OnCancel:      onCancel,
	}
	d.Rename = &widgets.Button{Label: "Rename", Hotkey: "r", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Rename, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.SourceLabel},
			{Row: 1, Child: d.Source},
			{Row: 2, Child: d.NewPathLabel},
			{Row: 3, Child: d.NewPath},
			{Row: 4, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Rename",
		MinWidth:   48,
		MaxWidth:   72,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Source, d.NewPath, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *RenameDialog) submit() {
	if d.Source != nil {
		src := strings.TrimSpace(d.Source.Value())
		dst := strings.TrimSpace(d.NewPath.Value())
		if src == "" || dst == "" {
			d.cancel()
			return
		}
		if d.OnSubmitPaths != nil {
			d.OnSubmitPaths(src, dst)
		}
		return
	}
	dst := strings.TrimSpace(d.NewPath.Value())
	if dst == "" {
		d.cancel()
		return
	}
	if d.OnSubmitSelected != nil {
		d.OnSubmitSelected(dst)
	}
}

func (d *RenameDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *RenameDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *RenameDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *RenameDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *RenameDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 3. CopyDialog ─────────────────────────────────────────────────────────────

type CopyDialog struct {
	Prompt           *widgets.Label
	SourceLabel      *widgets.Label
	Source           *widgets.TextInput
	DestinationLabel *widgets.Label
	Destination      *widgets.TextInput
	Copy             *widgets.Button
	Cancel           *widgets.Button
	Buttons          *widgets.ButtonRow
	Layout           *widgets.Grid

	OnSubmitSelected func(destination string)
	OnSubmitPaths    func(source, destination string)
	OnCancel         func()

	shell dialogShell
}

func NewCopySelectedDialog(sourceSummary string, destination *widgets.TextInput, onSubmit func(destination string), onCancel func()) *CopyDialog {
	d := &CopyDialog{
		Prompt:           &widgets.Label{Text: fmt.Sprintf("Copy %s to:", sourceSummary)},
		Destination:      ensureInput(destination, "."),
		OnSubmitSelected: onSubmit,
		OnCancel:         onCancel,
	}
	d.Copy = &widgets.Button{Label: "Copy", Hotkey: "c", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Copy, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Prompt},
			{Row: 1, Child: d.Destination},
			{Row: 2, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Copy",
		MinWidth:   48,
		MaxWidth:   72,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Destination, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func NewCopyPathDialog(source, destination *widgets.TextInput, onSubmit func(source, destination string), onCancel func()) *CopyDialog {
	d := &CopyDialog{
		SourceLabel:      &widgets.Label{Text: "Source path"},
		Source:           ensureInput(source, ""),
		DestinationLabel: &widgets.Label{Text: "Destination"},
		Destination:      ensureInput(destination, "."),
		OnSubmitPaths:    onSubmit,
		OnCancel:         onCancel,
	}
	d.Copy = &widgets.Button{Label: "Copy", Hotkey: "c", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Copy, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.SourceLabel},
			{Row: 1, Child: d.Source},
			{Row: 2, Child: d.DestinationLabel},
			{Row: 3, Child: d.Destination},
			{Row: 4, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Copy",
		MinWidth:   48,
		MaxWidth:   72,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Source, d.Destination, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *CopyDialog) submit() {
	if d.Source != nil {
		src := strings.TrimSpace(d.Source.Value())
		dst := strings.TrimSpace(d.Destination.Value())
		if src == "" || dst == "" {
			d.cancel()
			return
		}
		if d.OnSubmitPaths != nil {
			d.OnSubmitPaths(src, dst)
		}
		return
	}
	dst := strings.TrimSpace(d.Destination.Value())
	if dst == "" {
		d.cancel()
		return
	}
	if d.OnSubmitSelected != nil {
		d.OnSubmitSelected(dst)
	}
}

func (d *CopyDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *CopyDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *CopyDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *CopyDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *CopyDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 4. MoveDialog ─────────────────────────────────────────────────────────────

type MoveDialog struct {
	Prompt           *widgets.Label
	SourceLabel      *widgets.Label
	Source           *widgets.TextInput
	DestinationLabel *widgets.Label
	Destination      *widgets.TextInput
	Move             *widgets.Button
	Cancel           *widgets.Button
	Buttons          *widgets.ButtonRow
	Layout           *widgets.Grid

	OnSubmitSelected func(destination string)
	OnSubmitPaths    func(source, destination string)
	OnCancel         func()

	shell dialogShell
}

func NewMoveSelectedDialog(sourceSummary string, destination *widgets.TextInput, onSubmit func(destination string), onCancel func()) *MoveDialog {
	d := &MoveDialog{
		Prompt:           &widgets.Label{Text: fmt.Sprintf("Move %s to:", sourceSummary)},
		Destination:      ensureInput(destination, "."),
		OnSubmitSelected: onSubmit,
		OnCancel:         onCancel,
	}
	d.Move = &widgets.Button{Label: "Move", Hotkey: "m", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Move, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Prompt},
			{Row: 1, Child: d.Destination},
			{Row: 2, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Move",
		MinWidth:   48,
		MaxWidth:   72,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Destination, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func NewMovePathDialog(source, destination *widgets.TextInput, onSubmit func(source, destination string), onCancel func()) *MoveDialog {
	d := &MoveDialog{
		SourceLabel:      &widgets.Label{Text: "Source path"},
		Source:           ensureInput(source, ""),
		DestinationLabel: &widgets.Label{Text: "Destination"},
		Destination:      ensureInput(destination, "."),
		OnSubmitPaths:    onSubmit,
		OnCancel:         onCancel,
	}
	d.Move = &widgets.Button{Label: "Move", Hotkey: "m", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Move, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.SourceLabel},
			{Row: 1, Child: d.Source},
			{Row: 2, Child: d.DestinationLabel},
			{Row: 3, Child: d.Destination},
			{Row: 4, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Move",
		MinWidth:   48,
		MaxWidth:   72,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Source, d.Destination, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *MoveDialog) submit() {
	if d.Source != nil {
		src := strings.TrimSpace(d.Source.Value())
		dst := strings.TrimSpace(d.Destination.Value())
		if src == "" || dst == "" {
			d.cancel()
			return
		}
		if d.OnSubmitPaths != nil {
			d.OnSubmitPaths(src, dst)
		}
		return
	}
	dst := strings.TrimSpace(d.Destination.Value())
	if dst == "" {
		d.cancel()
		return
	}
	if d.OnSubmitSelected != nil {
		d.OnSubmitSelected(dst)
	}
}

func (d *MoveDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *MoveDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *MoveDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *MoveDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *MoveDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 5. DeletePathDialog ───────────────────────────────────────────────────────

type DeletePathDialog struct {
	PathLabel *widgets.Label
	Path      *widgets.TextInput
	Continue  *widgets.Button
	Cancel    *widgets.Button
	Buttons   *widgets.ButtonRow
	Layout    *widgets.Grid

	OnSubmit func(path string)
	OnCancel func()

	shell dialogShell
}

func NewDeletePathDialog(path *widgets.TextInput, onSubmit func(path string), onCancel func()) *DeletePathDialog {
	d := &DeletePathDialog{
		PathLabel: &widgets.Label{Text: "Path to delete"},
		Path:      ensureInput(path, ""),
		OnSubmit:  onSubmit,
		OnCancel:  onCancel,
	}
	d.Continue = &widgets.Button{Label: "Continue", Hotkey: "c", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Continue, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.PathLabel},
			{Row: 1, Child: d.Path},
			{Row: 2, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Delete",
		MinWidth:   36,
		MaxWidth:   60,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Path, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *DeletePathDialog) submit() {
	val := strings.TrimSpace(d.Path.Value())
	if val == "" {
		d.cancel()
		return
	}
	if d.OnSubmit != nil {
		d.OnSubmit(val)
	}
}

func (d *DeletePathDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *DeletePathDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *DeletePathDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *DeletePathDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *DeletePathDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 6. DeleteConfirmDialog ────────────────────────────────────────────────────

type DeleteConfirmDialog struct {
	Prompt  *widgets.TextBlock
	Delete  *widgets.Button
	Cancel  *widgets.Button
	Buttons *widgets.ButtonRow
	Layout  *widgets.Grid

	OnConfirm func()
	OnCancel  func()

	shell dialogShell
}

func NewDeleteConfirmDialog(sourceSummary string, onConfirm func(), onCancel func()) *DeleteConfirmDialog {
	d := &DeleteConfirmDialog{
		Prompt:    &widgets.TextBlock{Text: fmt.Sprintf("Delete %s?", sourceSummary)},
		OnConfirm: onConfirm,
		OnCancel:  onCancel,
	}
	d.Delete = &widgets.Button{Label: "Delete", Hotkey: "d", OnClick: d.confirm}
	d.Cancel = &widgets.Button{Label: "Cancel", Hotkey: "c", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Delete, d.Cancel)
	d.Buttons.Index = 1 // Cancel is initially selected
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Prompt},
			{Row: 1, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Delete",
		MinWidth:   36,
		MaxWidth:   60,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Buttons},
		OnSubmit:   d.confirm,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *DeleteConfirmDialog) confirm() {
	if d.OnConfirm != nil {
		d.OnConfirm()
	}
}

func (d *DeleteConfirmDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *DeleteConfirmDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *DeleteConfirmDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *DeleteConfirmDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *DeleteConfirmDialog) HandleEvent(event input.Event) bool {
	if ev, ok := keyEvent(event); ok {
		switch strings.ToLower(ev.String()) {
		case "y":
			d.confirm()
			return true
		case "n":
			d.cancel()
			return true
		}
	}
	return d.shell.HandleEvent(event)
}

// ── 7. EditDialog ─────────────────────────────────────────────────────────────

type EditDialog struct {
	PathLabel *widgets.Label
	Path      *widgets.TextInput
	Open      *widgets.Button
	Cancel    *widgets.Button
	Buttons   *widgets.ButtonRow
	Layout    *widgets.Grid

	OnSubmit func(path string)
	OnCancel func()

	shell dialogShell
}

func NewEditDialog(path *widgets.TextInput, onSubmit func(path string), onCancel func()) *EditDialog {
	d := &EditDialog{
		PathLabel: &widgets.Label{Text: "File path"},
		Path:      ensureInput(path, ""),
		OnSubmit:  onSubmit,
		OnCancel:  onCancel,
	}
	d.Open = &widgets.Button{Label: "Open", Hotkey: "o", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Open, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.PathLabel},
			{Row: 1, Child: d.Path},
			{Row: 2, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Edit File",
		MinWidth:   36,
		MaxWidth:   60,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Path, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *EditDialog) submit() {
	val := strings.TrimSpace(d.Path.Value())
	if val == "" {
		d.cancel()
		return
	}
	if d.OnSubmit != nil {
		d.OnSubmit(val)
	}
}

func (d *EditDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *EditDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *EditDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *EditDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *EditDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 8. MountDialog ────────────────────────────────────────────────────────────

type MountDialog struct {
	Interpretation *widgets.TextBlock
	GuestPathLabel *widgets.Label
	GuestPath      *widgets.TextInput
	Mount          *widgets.Button
	Cancel         *widgets.Button
	Buttons        *widgets.ButtonRow
	Layout         *widgets.Grid

	OnSubmit func(guestPath string)
	OnCancel func()

	shell dialogShell
}

func NewMountDialog(guestPath *widgets.TextInput, defaultPath string, onSubmit func(guestPath string), onCancel func()) *MountDialog {
	d := &MountDialog{
		Interpretation: &widgets.TextBlock{
			Text: "Choose a guest path (relative paths use the current directory; ~ expands to home):",
		},
		GuestPathLabel: &widgets.Label{Text: "Guest path"},
		GuestPath:      ensureInput(guestPath, defaultPath),
		OnSubmit:       onSubmit,
		OnCancel:       onCancel,
	}
	d.Mount = &widgets.Button{Label: "Mount", Hotkey: "m", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Mount, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Interpretation},
			{Row: 1, Child: d.GuestPathLabel},
			{Row: 2, Child: d.GuestPath},
			{Row: 3, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Mount Directory",
		MinWidth:   48,
		MaxWidth:   80,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.GuestPath, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *MountDialog) submit() {
	val := strings.TrimSpace(d.GuestPath.Value())
	if val == "" {
		d.cancel()
		return
	}
	if d.OnSubmit != nil {
		d.OnSubmit(val)
	}
}

func (d *MountDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *MountDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *MountDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *MountDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *MountDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 9. FindFilesDialog ────────────────────────────────────────────────────────

type FindFilesDialog struct {
	Explanation      *widgets.TextBlock
	FindPathsLabel   *widgets.Label
	FindPaths        *widgets.TextInput
	FindContentLabel *widgets.Label
	FindContent      *widgets.TextInput
	MaxResultsLabel  *widgets.Label
	MaxResults       *widgets.TextInput
	Search           *widgets.Button
	Cancel           *widgets.Button
	Buttons          *widgets.ButtonRow
	Layout           *widgets.Grid

	OnSubmit func(findPaths, findContent, maxResults string)
	OnCancel func()

	shell dialogShell
}

func NewFindFilesDialog(findPaths *widgets.TextInput, defaultPaths, defaultContent, defaultMaxResults string, onSubmit func(findPaths, findContent, maxResults string), onCancel func()) *FindFilesDialog {
	d := &FindFilesDialog{
		Explanation: &widgets.TextBlock{
			Text: "Filter paths structurally and/or search file content semantically:",
		},
		FindPathsLabel:   &widgets.Label{Text: "Find paths"},
		FindPaths:        ensureInput(findPaths, defaultPaths),
		FindContentLabel: &widgets.Label{Text: "Find content"},
		FindContent:      newDefaultPathInput(defaultContent),
		MaxResultsLabel:  &widgets.Label{Text: "Maximum results"},
		MaxResults:       newDefaultPathInput(defaultMaxResults),
		OnSubmit:         onSubmit,
		OnCancel:         onCancel,
	}
	d.Search = &widgets.Button{Label: "Search", Hotkey: "s", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.Search, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Explanation},
			{Row: 1, Child: d.FindPathsLabel},
			{Row: 2, Child: d.FindPaths},
			{Row: 3, Child: d.FindContentLabel},
			{Row: 4, Child: d.FindContent},
			{Row: 5, Child: d.MaxResultsLabel},
			{Row: 6, Child: d.MaxResults},
			{Row: 7, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Find Files",
		MinWidth:   52,
		MaxWidth:   76,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.FindPaths, d.FindContent, d.MaxResults, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *FindFilesDialog) submit() {
	findPaths := strings.TrimSpace(d.FindPaths.Value())
	findContent := strings.TrimSpace(d.FindContent.Value())
	maxResults := strings.TrimSpace(d.MaxResults.Value())
	if findPaths == "" && findContent == "" {
		d.cancel()
		return
	}
	if d.OnSubmit != nil {
		d.OnSubmit(findPaths, findContent, maxResults)
	}
}

func (d *FindFilesDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *FindFilesDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *FindFilesDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *FindFilesDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *FindFilesDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 10. SettingsDialog ────────────────────────────────────────────────────────

type SettingsValues struct {
	ShowPanels      bool
	ShowHiddenFiles bool
	ConfigureAI     bool
}

type SettingsDialog struct {
	ShowPanels      *widgets.Checkbox
	ShowHiddenFiles *widgets.Checkbox
	ConfigureAI     *widgets.Checkbox
	PanelFrame      *widgets.Frame
	DisplayFrame    *widgets.Frame
	OK              *widgets.Button
	Cancel          *widgets.Button
	Buttons         *widgets.ButtonRow
	Layout          *widgets.Grid

	OnSubmit func(SettingsValues)
	OnCancel func()

	shell dialogShell
}

func NewSettingsDialog(initial SettingsValues, onSubmit func(SettingsValues), onCancel func()) *SettingsDialog {
	d := &SettingsDialog{
		ShowPanels: &widgets.Checkbox{
			Label:   "Show file panels",
			Hotkey:  "p",
			Checked: initial.ShowPanels,
		},
		ShowHiddenFiles: &widgets.Checkbox{
			Label:   "Show hidden files",
			Hotkey:  "h",
			Checked: initial.ShowHiddenFiles,
		},
		ConfigureAI: &widgets.Checkbox{
			Label:   "Configure AI provider and model",
			Hotkey:  "a",
			Checked: initial.ConfigureAI,
		},
		OnSubmit: onSubmit,
		OnCancel: onCancel,
	}
	d.PanelFrame = &widgets.Frame{
		Title: "Panel settings",
		Child: &widgets.Grid{
			Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
			Children: []widgets.GridChild{
				{Row: 0, Child: d.ShowPanels},
				{Row: 1, Child: d.ShowHiddenFiles},
			},
		},
	}
	d.DisplayFrame = &widgets.Frame{
		Title: "Display settings",
		Child: &widgets.Grid{
			Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
			Children: []widgets.GridChild{
				{Row: 0, Child: d.ConfigureAI},
			},
		},
	}
	d.OK = &widgets.Button{Label: "OK", Hotkey: "o", OnClick: d.submit}
	d.Cancel = &widgets.Button{Label: "Cancel", Hotkey: "c", OnClick: d.cancel}
	d.Buttons = widgets.NewButtonPtrRow(d.OK, d.Cancel)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.PanelFrame},
			{Row: 1, Child: d.DisplayFrame},
			{Row: 2, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      "Settings",
		MinWidth:   36,
		MaxWidth:   56,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.ShowPanels, d.ShowHiddenFiles, d.ConfigureAI, d.Buttons},
		OnSubmit:   d.submit,
		OnCancel:   d.cancel,
	}
	d.shell.syncFocus()
	return d
}

func (d *SettingsDialog) submit() {
	if d.OnSubmit != nil {
		d.OnSubmit(SettingsValues{
			ShowPanels:      d.ShowPanels.Checked,
			ShowHiddenFiles: d.ShowHiddenFiles.Checked,
			ConfigureAI:     d.ConfigureAI.Checked,
		})
	}
}

func (d *SettingsDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *SettingsDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *SettingsDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *SettingsDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *SettingsDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 11. ErrorDialog ───────────────────────────────────────────────────────────

type ErrorDialog struct {
	Message *widgets.TextBlock
	OK      *widgets.Button
	Buttons *widgets.ButtonRow
	Layout  *widgets.Grid

	OnClose func()

	shell dialogShell
}

func NewErrorDialog(title, message string, onClose func()) *ErrorDialog {
	if title == "" {
		title = "Error"
	}
	d := &ErrorDialog{
		Message: &widgets.TextBlock{Text: message},
		OnClose: onClose,
	}
	d.OK = &widgets.Button{Label: "OK", Hotkey: "o", OnClick: d.close}
	d.Buttons = widgets.NewButtonPtrRow(d.OK)
	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.Message},
			{Row: 1, Child: d.Buttons},
		},
	}
	d.shell = dialogShell{
		Title:      title,
		MinWidth:   48,
		MaxWidth:   80,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.Buttons},
		OnSubmit:   d.close,
		OnCancel:   d.close,
	}
	d.shell.syncFocus()
	return d
}

func (d *ErrorDialog) close() {
	if d.OnClose != nil {
		d.OnClose()
	}
}

func (d *ErrorDialog) Measure(c Constraints) Size              { return d.shell.Measure(c) }
func (d *ErrorDialog) Render(r Rect, ctx RenderContext) string { return d.shell.Render(r, ctx) }
func (d *ErrorDialog) RenderBuf() CellBuf                      { return d.shell.RenderBuf() }
func (d *ErrorDialog) HandleEvent(event input.Event) bool      { return d.shell.HandleEvent(event) }

// ── 12. ProgressDialog ────────────────────────────────────────────────────────

type ProgressDialog struct {
	Progress      filemanager.FileOpProgress
	Collision     bool
	CollisionPath string

	CurrentItem     *widgets.Text
	ProgressBar     *widgets.ProgressBar
	TransferStats   *widgets.Text
	Cancel          *widgets.Button
	CancelRow       *widgets.ButtonRow
	CollisionPrompt *widgets.Text
	ConflictingItem *widgets.Text
	Overwrite       *widgets.Button
	Skip            *widgets.Button
	OverwriteAll    *widgets.Button
	SkipAll         *widgets.Button
	CollisionCancel *widgets.Button
	CollisionRow1   *widgets.ButtonRow
	CollisionRow2   *widgets.ButtonRow
	CollisionFrame  *widgets.Frame
	Layout          *widgets.Grid

	OnCancel    func()
	OnCollision func(filemanager.CollisionChoice)

	shell dialogShell
}

func NewProgressDialog(title string, progress filemanager.FileOpProgress, onCancel func()) *ProgressDialog {
	d := &ProgressDialog{
		Progress:      progress,
		CurrentItem:   &widgets.Text{},
		ProgressBar:   &widgets.ProgressBar{},
		TransferStats: &widgets.Text{Style: makeStyle(colorDlgMuted, colorDlgBg, false)},
		OnCancel:      onCancel,
	}
	d.Cancel = &widgets.Button{Label: "Cancel", Hotkey: "c", OnClick: d.cancel}
	d.CancelRow = widgets.NewButtonPtrRow(d.Cancel)

	d.CollisionPrompt = &widgets.Text{
		Text:  "File already exists:",
		Style: makeStyle(colorWarnText, colorDlgBg, true),
	}
	d.ConflictingItem = &widgets.Text{}
	d.Overwrite = &widgets.Button{
		Label:   "Overwrite",
		Hotkey:  "o",
		OnClick: func() { d.resolveCollision(filemanager.ColOverwrite) },
	}
	d.Skip = &widgets.Button{
		Label:   "Skip",
		Hotkey:  "s",
		OnClick: func() { d.resolveCollision(filemanager.ColSkip) },
	}
	d.OverwriteAll = &widgets.Button{
		Label:   "Overwrite all",
		Hotkey:  "a",
		OnClick: func() { d.resolveCollision(filemanager.ColOverwriteAll) },
	}
	d.SkipAll = &widgets.Button{
		Label:   "Skip all",
		Hotkey:  "k",
		OnClick: func() { d.resolveCollision(filemanager.ColSkipAll) },
	}
	d.CollisionCancel = &widgets.Button{
		Label:   "Cancel",
		Hotkey:  "c",
		OnClick: func() { d.resolveCollision(filemanager.ColCancel) },
	}
	d.CollisionRow1 = widgets.NewButtonPtrRow(d.Overwrite, d.Skip, d.OverwriteAll)
	d.CollisionRow2 = widgets.NewButtonPtrRow(d.SkipAll, d.CollisionCancel)
	d.CollisionFrame = &widgets.Frame{
		Title: "Conflict",
		Child: &widgets.Grid{
			Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
			Children: []widgets.GridChild{
				{Row: 0, Child: d.CollisionPrompt},
				{Row: 1, Child: d.ConflictingItem},
				{Row: 2, Child: d.CollisionRow1},
				{Row: 3, Child: d.CollisionRow2},
			},
		},
	}

	d.Layout = &widgets.Grid{
		Columns: []widgets.Track{{Kind: widgets.TrackFraction, Value: 1}},
		Children: []widgets.GridChild{
			{Row: 0, Child: d.CurrentItem},
			{Row: 1, Child: d.ProgressBar},
			{Row: 2, Child: d.TransferStats},
			{Row: 3, Child: d.CancelRow},
		},
	}
	d.shell = dialogShell{
		Title:      title,
		MinWidth:   48,
		MaxWidth:   72,
		Layout:     d.Layout,
		FocusOrder: []ui.Widget{d.CancelRow},
		OnSubmit:   d.cancel,
		OnCancel:   d.cancelOrAbortCollision,
	}
	d.syncProgressState()
	d.shell.syncFocus()
	return d
}

func (d *ProgressDialog) SetProgress(progress filemanager.FileOpProgress) {
	if d == nil {
		return
	}
	d.Progress = progress
	d.syncProgressState()
}

func (d *ProgressDialog) SetCollision(path string, onChoice func(filemanager.CollisionChoice)) {
	if d == nil {
		return
	}
	d.Collision = true
	d.CollisionPath = path
	d.OnCollision = onChoice
	d.ConflictingItem.Text = filepath.Base(path)
	if d.ConflictingItem.Text == "" {
		d.ConflictingItem.Text = path
	}
	d.CollisionRow1.Index = 0
	d.CollisionRow2.Index = 0
	d.Layout.Children[3].Child = d.CollisionFrame
	d.shell.MinWidth = 60
	d.shell.MaxWidth = 88
	d.shell.FocusOrder = []ui.Widget{d.CollisionRow1, d.CollisionRow2}
	d.shell.FocusIndex = 0
	d.shell.syncFocus()
}

func (d *ProgressDialog) ClearCollision() {
	if d == nil {
		return
	}
	d.Collision = false
	d.CollisionPath = ""
	d.OnCollision = nil
	d.ConflictingItem.Text = ""
	d.Layout.Children[3].Child = d.CancelRow
	d.shell.MinWidth = 48
	d.shell.MaxWidth = 72
	d.shell.FocusOrder = []ui.Widget{d.CancelRow}
	d.shell.FocusIndex = 0
	d.shell.syncFocus()
}

func (d *ProgressDialog) resolveCollision(choice filemanager.CollisionChoice) {
	callback := d.OnCollision
	d.ClearCollision()
	if callback != nil {
		callback(choice)
	}
}

func (d *ProgressDialog) cancel() {
	if d.OnCancel != nil {
		d.OnCancel()
	}
}

func (d *ProgressDialog) cancelOrAbortCollision() {
	if d.Collision {
		d.resolveCollision(filemanager.ColCancel)
		return
	}
	d.cancel()
}

func (d *ProgressDialog) syncProgressState() {
	if d == nil {
		return
	}
	d.CurrentItem.Text = d.Progress.Current
	d.ProgressBar.Done = d.Progress.Done
	d.ProgressBar.Total = d.Progress.Total
	d.ProgressBar.Rate = d.Progress.BytesPerSec
	d.TransferStats.Text = fmt.Sprintf(
		"%s / %s  %s/s",
		FormatBytes(d.Progress.Done),
		FormatBytes(d.Progress.Total),
		FormatBytes(d.Progress.BytesPerSec),
	)
}

func (d *ProgressDialog) Measure(c Constraints) Size {
	d.syncProgressState()
	return d.shell.Measure(c)
}

func (d *ProgressDialog) Render(r Rect, ctx RenderContext) string {
	d.syncProgressState()
	return d.shell.Render(r, ctx)
}

func (d *ProgressDialog) RenderBuf() CellBuf {
	d.syncProgressState()
	return d.shell.RenderBuf()
}

func (d *ProgressDialog) HandleEvent(event input.Event) bool {
	if d == nil {
		return false
	}
	d.syncProgressState()
	return d.shell.HandleEvent(event)
}

func keyEvent(event input.Event) (input.KeyPressEvent, bool) {
	switch ev := event.(type) {
	case input.KeyPressEvent:
		return ev, true
	case *input.KeyPressEvent:
		if ev != nil {
			return *ev, true
		}
	}
	return input.KeyPressEvent{}, false
}

func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), units[exp])
}

func SummarizeSources(sources []string) string {
	if len(sources) == 1 {
		return filepath.Base(sources[0])
	}
	return fmt.Sprintf("%d items", len(sources))
}

var (
	_ Widget = (*MkdirDialog)(nil)
	_ Widget = (*RenameDialog)(nil)
	_ Widget = (*CopyDialog)(nil)
	_ Widget = (*MoveDialog)(nil)
	_ Widget = (*DeletePathDialog)(nil)
	_ Widget = (*DeleteConfirmDialog)(nil)
	_ Widget = (*EditDialog)(nil)
	_ Widget = (*MountDialog)(nil)
	_ Widget = (*FindFilesDialog)(nil)
	_ Widget = (*SettingsDialog)(nil)
	_ Widget = (*ErrorDialog)(nil)
	_ Widget = (*ProgressDialog)(nil)
)
