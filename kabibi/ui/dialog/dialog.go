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

type DialogKind int

const (
	DialogInput DialogKind = iota
	DialogConfirm
	DialogChoice
	DialogProgress

	dialogInput    = DialogInput
	dialogConfirm  = DialogConfirm
	dialogChoice   = DialogChoice
	dialogProgress = DialogProgress
)

type dialogKind = DialogKind

type DialogAction int

const (
	ActionMkdir DialogAction = iota
	ActionRename
	ActionNewFile
	ActionCopy
	ActionMove
	ActionDelete
	ActionSelectMask
	ActionUnselectMask
	ActionEdit

	actionMkdir        = ActionMkdir
	actionRename       = ActionRename
	actionNewFile      = ActionNewFile
	actionCopy         = ActionCopy
	actionMove         = ActionMove
	actionDelete       = ActionDelete
	actionSelectMask   = ActionSelectMask
	actionUnselectMask = ActionUnselectMask
)

type dialogAction = DialogAction

type Choice struct {
	Label  string
	Hotkey string
}

type dlgChoice = Choice

type InputField struct {
	Label string
	Input *widgets.TextInput
}

const (
	colorDlgBg     terminal.Color = 0x1A1F26
	colorDlgBorder terminal.Color = 0xFFD700
	colorDlgText   terminal.Color = 0xE5E5E5
	colorDlgMuted  terminal.Color = 0x8A95A5
	colorBtnBg     terminal.Color = 0xFFD700
	colorBtnFg     terminal.Color = 0x1A1F26
	colorBtnAltBg  terminal.Color = 0x2A323D
	colorBtnAltFg  terminal.Color = 0xE5E5E5
	colorBlack     terminal.Color = 0x000000
)

func makeCell(r rune, fg, bg terminal.Color, bold bool) Cell {
	var attrs terminal.CharAttributes
	if bold {
		attrs |= terminal.CharBold
	}
	return Cell{Grapheme: string(r), Style: terminal.NewStyle(fg, bg, attrs)}
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

type DialogWidget struct {
	Kind       DialogKind
	Action     DialogAction
	Title      string
	Prompt     string
	Choices    []Choice
	ChoiceIdx  int
	InputValue string
	InputWidth int
	Fields     []InputField
	Pending    *filemanager.FileOp

	Progress      filemanager.FileOpProgress
	CollisionPath string
	Collision     bool

	OnSubmitInput  func(action DialogAction, value string, pending *filemanager.FileOp)
	OnSubmitForm   func(action DialogAction, values []string, pending *filemanager.FileOp)
	OnConfirm      func(action DialogAction, pending *filemanager.FileOp)
	OnPickChoice   func(idx int)
	OnCancel       func()
	OnCollision    func(filemanager.CollisionChoice)

	Width  int
	Height int
	X      int
	Y      int

	buttons           *widgets.ButtonRow
	focusedField      int
	focusedButton     bool
	legacyInputMirror string
}

func NewInputDialog(action DialogAction, title, prompt, initial string, pending *filemanager.FileOp) *DialogWidget {
	d := &DialogWidget{
		Kind:       DialogInput,
		Action:     action,
		Title:      title,
		Prompt:     prompt,
		InputValue: initial,
		Pending:    pending,
		Width:      54,
		Height:     10,
	}
	d.Fields = []InputField{{Input: widgets.NewTextInput(widgets.TextInputOptions{
		Value: initial, SelectAll: initial != "", AllowPaste: true,
	})}}
	d.legacyInputMirror = initial
	d.setButtons([]Choice{{Label: "OK"}, {Label: "Cancel"}})
	return d
}

func NewFormDialog(action DialogAction, title, prompt string, labels, initial []string, pending *filemanager.FileOp) *DialogWidget {
	d := &DialogWidget{
		Kind:    DialogInput,
		Action:  action,
		Title:   title,
		Prompt:  prompt,
		Pending: pending,
		Width:   64,
		Height:  12,
	}
	for i, label := range labels {
		value := ""
		if i < len(initial) {
			value = initial[i]
		}
		d.Fields = append(d.Fields, InputField{
			Label: label,
			Input: widgets.NewTextInput(widgets.TextInputOptions{
				Value: value, SelectAll: value != "", AllowPaste: true,
			}),
		})
	}
	if len(d.Fields) > 0 {
		d.InputValue = d.Fields[0].Input.Value()
		d.legacyInputMirror = d.InputValue
	}
	d.setButtons([]Choice{{Label: "OK"}, {Label: "Cancel"}})
	return d
}

func NewConfirmDialog(action DialogAction, title, prompt string, pending *filemanager.FileOp) *DialogWidget {
	d := &DialogWidget{
		Kind:    DialogConfirm,
		Action:  action,
		Title:   title,
		Prompt:  prompt,
		Choices: []Choice{{Label: "Yes", Hotkey: "y"}, {Label: "No", Hotkey: "n"}},
		Pending: pending,
		Width:   54,
		Height:  10,
	}
	d.setButtons(d.Choices)
	d.ChoiceIdx = 1
	d.buttons.Index = 1
	d.focusedButton = true
	d.buttons.Focused = true
	return d
}

func NewChoiceDialog(title, prompt string, choices []Choice, onPick func(idx int)) *DialogWidget {
	d := &DialogWidget{
		Kind:         DialogChoice,
		Title:        title,
		Prompt:       prompt,
		Choices:      append([]Choice(nil), choices...),
		OnPickChoice: onPick,
		Width:        54,
		Height:       10,
	}
	d.setButtons(d.Choices)
	d.focusedButton = true
	d.buttons.Focused = true
	return d
}

func NewProgressDialog(title string, progress filemanager.FileOpProgress, onCancel func()) *DialogWidget {
	d := &DialogWidget{
		Kind:     DialogProgress,
		Title:    title,
		Progress: progress,
		OnCancel: onCancel,
		Width:    64,
		Height:   9,
	}
	d.setButtons([]Choice{{Label: "Cancel", Hotkey: "c"}})
	d.focusedButton = true
	d.buttons.Focused = true
	return d
}

func (w *DialogWidget) setButtons(choices []Choice) {
	w.Choices = append([]Choice(nil), choices...)
	buttons := make([]widgets.Button, len(choices))
	for i, choice := range choices {
		index := i
		buttons[i] = widgets.Button{
			Label: choice.Label, Hotkey: choice.Hotkey,
			OnPress: func() { w.activateButton(index) },
		}
	}
	w.buttons = widgets.NewButtonRow(buttons)
	if w.ChoiceIdx >= len(buttons) {
		w.ChoiceIdx = 0
	}
	w.buttons.Index = w.ChoiceIdx
}

func (w *DialogWidget) SetCollision(path string, onChoice func(filemanager.CollisionChoice)) {
	if w == nil {
		return
	}
	w.Collision = true
	w.CollisionPath = path
	w.OnCollision = onChoice
	w.setButtons([]Choice{
		{Label: "Overwrite", Hotkey: "o"},
		{Label: "Skip", Hotkey: "s"},
		{Label: "All", Hotkey: "a"},
		{Label: "Skip all", Hotkey: "k"},
		{Label: "Cancel", Hotkey: "c"},
	})
	w.ChoiceIdx = 0
	w.buttons.Index = 0
}

func (w *DialogWidget) ClearCollision() {
	if w == nil {
		return
	}
	w.Collision = false
	w.CollisionPath = ""
	w.OnCollision = nil
	w.setButtons([]Choice{{Label: "Cancel", Hotkey: "c"}})
}

func (w *DialogWidget) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	maxW := c.MaxW
	if maxW <= 0 {
		maxW = 80
	}
	maxH := c.MaxH
	if maxH <= 0 {
		maxH = 24
	}
	preferred := max(1, min(68, max(36, maxW*3/4)))
	width := min(maxW, preferred)
	if width < 1 {
		width = 1
	}
	compact := maxH < 10
	promptRows := wrapTextRows(w.Prompt, max(1, width-6))
	height := 4 + promptRows
	if compact {
		promptRows = 0
		height = 2
	}
	switch w.Kind {
	case DialogInput:
		if compact {
			height += len(w.Fields)
		} else {
			height += len(w.Fields) * 2
		}
	case DialogProgress:
		if compact {
			height++
		} else {
			height += 3
		}
		if w.Collision {
			if compact {
				height++
			} else {
				height += 2
			}
		}
	case DialogConfirm, DialogChoice:
		height += 1
	}
	btnSize := w.buttonRow().Measure(Constraints{MaxW: max(1, width-4), MaxH: maxH})
	height += max(1, btnSize.H)
	if w.Kind == DialogInput && len(w.Fields) == 0 {
		height += 2
	}
	height = min(height, maxH)
	if height < 1 {
		height = 1
	}
	return Size{W: width, H: height}
}

func (w *DialogWidget) Layout(r Rect) {
	if w == nil {
		return
	}
	w.X, w.Y, w.Width, w.Height = r.X, r.Y, r.W, r.H
}

func (w *DialogWidget) Render(r Rect, ctx RenderContext) (CellBuf, CursorPos) {
	if r.W > 0 || r.H > 0 {
		w.Layout(r)
	}
	buf, cursor := w.RenderBufWithContext(ctx)
	return buf, cursor
}

func (w *DialogWidget) RenderBuf() CellBuf {
	buf, _ := w.RenderBufWithContext(RenderContext{})
	return buf
}

func (w *DialogWidget) RenderBufWithContext(ctx RenderContext) (CellBuf, CursorPos) {
	if w == nil {
		return terminal.NewCellBuf(0, 0), CursorPos{}
	}
	width, height := w.Width, w.Height
	if width <= 0 {
		width = 54
	}
	if height <= 0 {
		height = 10
	}
	buf := terminal.NewCellBuf(width, height)
	buf.Fill(Rect{W: width, H: height}, makeCell(' ', colorDlgText, colorDlgBg, false))
	if width < 2 || height < 2 {
		return buf, CursorPos{}
	}
	w.drawBorder(&buf, width, height, w.Title)
	innerW := max(1, width-4)
	compact := height < 10
	y := 2
	if compact {
		y = 1
	}
	if !compact {
		for _, line := range wrapText(w.Prompt, innerW) {
			if y >= height-1 {
				break
			}
			drawText(&buf, 2, y, line, innerW, makeStyle(colorDlgText, colorDlgBg, false))
			y++
		}
	}
	var cursor CursorPos
	switch w.Kind {
	case DialogInput:
		w.syncLegacyInput()
		for i := range w.Fields {
			field := &w.Fields[i]
			if y >= height-2 {
				break
			}
			inputX := 2
			inputWidth := innerW
			if compact && field.Label != "" {
				labelWidth := min(runewidth.StringWidth(field.Label)+1, max(0, innerW-1))
				drawText(&buf, inputX, y, field.Label, labelWidth, makeStyle(colorDlgMuted, colorDlgBg, false))
				inputX += labelWidth
				inputWidth -= labelWidth
			} else if field.Label != "" {
				drawText(&buf, 2, y, field.Label, innerW, makeStyle(colorDlgMuted, colorDlgBg, false))
				y++
			}
			if y >= height-1 {
				break
			}
			field.Input.SetFocused(i == w.focusedField && !w.focusedButton)
			inputBuf, inputCursor := field.Input.Render(Rect{X: inputX, Y: y, W: inputWidth, H: 1}, ctx)
			buf.Blit(inputBuf, inputX, y)
			if i == w.focusedField && inputCursor.Visible {
				cursor = CursorPos{X: inputX + inputCursor.X, Y: y + inputCursor.Y, Visible: true}
			}
			y++
			if i == 0 {
				w.InputValue = field.Input.Value()
				w.legacyInputMirror = w.InputValue
			}
		}
	case DialogProgress:
		current := w.Progress.Current
		progressRows := 3
		if compact {
			current = fmt.Sprintf("%s %d%%", filepath.Base(current), pctOf(w.Progress.Done, w.Progress.Total))
			progressRows = 1
		}
		p := &widgets.ProgressBar{
			Current: current,
			Done: w.Progress.Done, Total: w.Progress.Total, Rate: w.Progress.BytesPerSec,
		}
		pbuf, _ := p.Render(Rect{X: 2, Y: y, W: innerW, H: min(progressRows, max(0, height-y-1))}, ctx)
		buf.Blit(pbuf, 2, y)
		y += pbuf.Height
		if w.Collision && y < height-1 {
			if !compact {
				y++
			}
			message := "File already exists: " + filepath.Base(w.CollisionPath)
			drawText(&buf, 2, y, message, innerW, makeStyle(0xFF6B6B, colorDlgBg, true))
			y++
		}
	}

	buttons := w.buttonRow()
	if buttons != nil && height > 2 {
		buttons.Index = w.ChoiceIdx
		buttons.Focused = w.focusedButton || w.Kind == DialogChoice || w.Kind == DialogConfirm || w.Kind == DialogProgress
		btnHeight := min(buttons.Measure(Constraints{MaxW: innerW, MaxH: height}).H, height-2)
		btnY := height - 1 - btnHeight
		if btnY < 1 {
			btnY = 1
		}
		btnBuf, _ := buttons.Render(Rect{X: 2, Y: btnY, W: innerW, H: height - 1 - btnY}, ctx)
		buf.Blit(btnBuf, 2, btnY)
	}
	return buf, cursor
}

func (w *DialogWidget) buttonRow() *widgets.ButtonRow {
	if w.buttons == nil {
		w.setButtons(w.Choices)
	}
	return w.buttons
}

func (w *DialogWidget) HandleEvent(event input.Event) bool {
	if w == nil {
		return false
	}
	ev, ok := keyEvent(event)
	if !ok {
		if w.Kind == DialogInput && len(w.Fields) > 0 {
			return w.currentField().Input.HandleEvent(event)
		}
		return false
	}
	key := strings.ToLower(ev.String())
	if key == "esc" {
		if w.Kind == DialogProgress && w.Collision {
			w.activateButton(len(w.Choices) - 1)
			return true
		}
		if w.Kind == DialogChoice && w.OnPickChoice != nil {
			w.OnPickChoice(-1)
		}
		if w.OnCancel != nil {
			w.OnCancel()
		}
		return true
	}
	if w.Kind == DialogProgress {
		if w.Collision {
			return w.handleButtons(ev)
		}
		if key == "enter" || key == "c" {
			if w.OnCancel != nil {
				w.OnCancel()
			}
		}
		return true
	}
	if w.Kind == DialogChoice || w.Kind == DialogConfirm {
		if key == "enter" && w.Kind == DialogConfirm {
			w.activateButton(w.ChoiceIdx)
			return true
		}
		return w.handleButtons(ev)
	}
	if w.Kind != DialogInput || len(w.Fields) == 0 {
		return true
	}
	w.syncLegacyInput()
	if w.focusedButton {
		switch key {
		case "tab":
			w.moveFocus(1)
			return true
		case "shift+tab", "up":
			w.moveFocus(-1)
			return true
		case "down":
			w.moveFocus(1)
			return true
		case "enter":
			w.activateButton(w.ChoiceIdx)
			return true
		default:
			return w.handleButtons(ev)
		}
	}
	field := w.currentField()
	switch key {
	case "enter":
		if w.focusedButton {
			w.activateButton(w.ChoiceIdx)
		} else {
			w.submit()
		}
		return true
	case "tab":
		if w.focusedButton {
			w.moveFocus(1)
			return true
		}
		if field.Input.HandleEvent(ev) {
			return true
		}
		w.moveFocus(1)
		return true
	case "shift+tab":
		w.moveFocus(-1)
		return true
	case "up", "down":
		if w.focusedButton {
			if key == "up" {
				w.moveFocus(-1)
			} else {
				w.moveFocus(1)
			}
			return true
		}
		if len(w.Fields) == 1 {
			if field.Input.HandleEvent(ev) {
				return true
			}
		}
		if key == "up" {
			w.moveFocus(-1)
		} else {
			w.moveFocus(1)
		}
		return true
	default:
		if field.Input.HandleEvent(ev) {
			w.InputValue = field.Input.Value()
			w.legacyInputMirror = w.InputValue
			return true
		}
		return true
	}
}

func (w *DialogWidget) handleButtons(ev input.KeyPressEvent) bool {
	buttons := w.buttonRow()
	if buttons == nil {
		return true
	}
	key := strings.ToLower(ev.String())
	if key == "esc" {
		if w.Kind == DialogChoice && w.OnPickChoice != nil {
			w.OnPickChoice(-1)
		}
		if w.OnCancel != nil {
			w.OnCancel()
		}
		return true
	}
	handled := buttons.HandleEvent(ev)
	w.ChoiceIdx = buttons.Index
	return handled || true
}

func (w *DialogWidget) activateButton(index int) {
	switch w.Kind {
	case DialogInput:
		if index == 0 {
			w.submit()
		} else {
			if w.OnCancel != nil {
				w.OnCancel()
			}
		}
	case DialogConfirm:
		if index == 0 {
			if w.OnConfirm != nil {
				w.OnConfirm(w.Action, w.Pending)
			}
		} else if w.OnCancel != nil {
			w.OnCancel()
		}
	case DialogChoice:
		if w.OnPickChoice != nil {
			w.OnPickChoice(index)
		}
	case DialogProgress:
		if w.Collision {
			choice := filemanager.ColCancel
			switch index {
			case 0:
				choice = filemanager.ColOverwrite
			case 1:
				choice = filemanager.ColSkip
			case 2:
				choice = filemanager.ColOverwriteAll
			case 3:
				choice = filemanager.ColSkipAll
			}
			callback := w.OnCollision
			w.ClearCollision()
			if callback != nil {
				callback(choice)
			}
		} else if w.OnCancel != nil {
			w.OnCancel()
		}
	}
}

func (w *DialogWidget) submit() {
	w.syncLegacyInput()
	values := make([]string, len(w.Fields))
	for i, field := range w.Fields {
		values[i] = strings.TrimSpace(field.Input.Value())
		if values[i] == "" {
			if w.OnCancel != nil {
				w.OnCancel()
			}
			return
		}
	}
	if w.OnSubmitForm != nil {
		w.OnSubmitForm(w.Action, values, w.Pending)
		return
	}
	if w.OnSubmitInput != nil && len(values) > 0 {
		w.OnSubmitInput(w.Action, values[0], w.Pending)
	}
}

func (w *DialogWidget) currentField() *InputField {
	if w.focusedField < 0 {
		w.focusedField = 0
	}
	if w.focusedField >= len(w.Fields) {
		w.focusedField = len(w.Fields) - 1
	}
	return &w.Fields[w.focusedField]
}

func (w *DialogWidget) syncLegacyInput() {
	if len(w.Fields) == 0 {
		return
	}
	field := w.Fields[0].Input
	if field == nil {
		field = widgets.NewTextInput(widgets.TextInputOptions{Value: w.InputValue, AllowPaste: true})
		w.Fields[0].Input = field
	}
	if w.InputValue != w.legacyInputMirror {
		field.SetValue(w.InputValue)
	}
	w.InputValue = field.Value()
	w.legacyInputMirror = w.InputValue
}

func (w *DialogWidget) moveFocus(step int) {
	total := len(w.Fields) + 1
	if w.focusedButton {
		w.focusedButton = false
		w.buttonRow().Focused = false
		w.focusedField = len(w.Fields) - 1
		if step > 0 {
			w.focusedButton = true
			w.buttonRow().Focused = true
			return
		}
		if len(w.Fields) > 0 {
			w.Fields[w.focusedField].Input.SetFocused(true)
		}
		return
	}
	if len(w.Fields) == 0 {
		w.focusedButton = true
		w.buttonRow().Focused = true
		return
	}
	w.Fields[w.focusedField].Input.SetFocused(false)
	next := (w.focusedField + step + total) % total
	if next == len(w.Fields) {
		w.focusedButton = true
		w.buttonRow().Focused = true
		return
	}
	w.focusedField = next
	w.Fields[w.focusedField].Input.SetFocused(true)
}

func (w *DialogWidget) drawBorder(buf *CellBuf, width, height int, title string) {
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

func wrapTextRows(text string, width int) int { return len(wrapText(text, width)) }

func wrapText(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var rows []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			rows = append(rows, "")
			continue
		}
		line := ""
		for _, word := range words {
			if line == "" {
				line = truncateStringToWidth(word, width)
				continue
			}
			candidate := line + " " + word
			if runewidth.StringWidth(candidate) <= width {
				line = candidate
			} else {
				rows = append(rows, line)
				line = truncateStringToWidth(word, width)
			}
		}
		rows = append(rows, line)
	}
	return rows
}

func makeStyle(fg, bg terminal.Color, bold bool) terminal.Style {
	attrs := terminal.CharAttributes(0)
	if bold {
		attrs |= terminal.CharBold
	}
	return terminal.NewStyle(fg, bg, attrs)
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

var _ Widget = (*DialogWidget)(nil)

func pctOf(done, total int64) int {
	if total <= 0 {
		return 0
	}
	return max(0, min(100, int(done*100/total)))
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

func renderProgressBarText(percentage int, width int) string {
	if width < 0 {
		width = 0
	}
	percentage = max(0, min(100, percentage))
	filled := width * percentage / 100
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
