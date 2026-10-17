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

// Choice is a single FAR-style button in a choice dialog.
type Choice struct {
	Label  string // shown text, e.g. "Overwrite"
	Hotkey string // single lowercase letter that resolves immediately
}

type dlgChoice = Choice

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
	return Cell{
		Grapheme: string(r),
		Style:    terminal.NewStyle(fg, bg, attrs),
	}
}

func truncateStringToWidth(s string, maxWidth int) string {
	displayWidth := runewidth.StringWidth(s)
	if displayWidth <= maxWidth {
		return s
	}
	effectiveWidth := maxWidth - 1
	if effectiveWidth <= 0 {
		return ""
	}
	width := 0
	var b strings.Builder
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if width+w > effectiveWidth {
			break
		}
		b.WriteRune(r)
		width += w
	}
	return strings.TrimRight(b.String(), " ")
}

// DialogWidget renders modal dialog content into a CellBuf and handles keyboard
// input directly via ui.Widget.
type DialogWidget struct {
	Kind       DialogKind
	Action     DialogAction
	Title      string
	Prompt     string
	Choices    []Choice
	ChoiceIdx  int
	InputValue string
	InputWidth int
	Pending    *filemanager.FileOp

	// Progress state when Kind == DialogProgress
	Progress filemanager.FileOpProgress

	// Callbacks
	OnSubmitInput func(action DialogAction, value string, pending *filemanager.FileOp)
	OnConfirm     func(action DialogAction, pending *filemanager.FileOp)
	OnPickChoice  func(idx int)
	OnCancel      func()

	Width  int
	Height int
	X      int
	Y      int
}

func NewInputDialog(action DialogAction, title, prompt, initial string, pending *filemanager.FileOp) *DialogWidget {
	return &DialogWidget{
		Kind:       DialogInput,
		Action:     action,
		Title:      title,
		Prompt:     prompt,
		InputValue: initial,
		Pending:    pending,
		Width:      54,
		Height:     10,
	}
}

func NewConfirmDialog(action DialogAction, title, prompt string, pending *filemanager.FileOp) *DialogWidget {
	return &DialogWidget{
		Kind:   DialogConfirm,
		Action: action,
		Title:  title,
		Prompt: prompt,
		Choices: []Choice{
			{Label: "[Y]es", Hotkey: "y"},
			{Label: "[N]o", Hotkey: "n"},
		},
		Pending: pending,
		Width:   54,
		Height:  10,
	}
}

func NewChoiceDialog(title, prompt string, choices []Choice, onPick func(idx int)) *DialogWidget {
	return &DialogWidget{
		Kind:         DialogChoice,
		Title:        title,
		Prompt:       prompt,
		Choices:      append([]Choice(nil), choices...),
		OnPickChoice: onPick,
		Width:        54,
		Height:       10,
	}
}

func NewProgressDialog(title string, progress filemanager.FileOpProgress, onCancel func()) *DialogWidget {
	return &DialogWidget{
		Kind:     DialogProgress,
		Title:    title,
		Progress: progress,
		OnCancel: onCancel,
		Width:    58,
		Height:   10,
	}
}

func (w *DialogWidget) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxW > 0 && c.MaxW < 24 {
		return Size{W: c.MaxW, H: 1}
	}
	wW := w.Width
	if wW <= 0 {
		wW = 54
	}
	if c.MaxW > 0 && wW > c.MaxW {
		wW = c.MaxW
	}
	if wW < 24 {
		wW = 24
	}
	return Size{W: wW, H: 10}
}

func (w *DialogWidget) Layout(r Rect) {
	if w == nil {
		return
	}
	w.X = r.X
	w.Y = r.Y
	w.Width = r.W
	w.Height = r.H
}

func (w *DialogWidget) Render(r Rect, _ RenderContext) (CellBuf, CursorPos) {
	if r.W > 0 || r.H > 0 {
		w.Layout(r)
	}
	buf := w.RenderBuf()
	if w != nil && w.Kind == DialogInput {
		curX := 3 + runewidth.StringWidth(w.InputValue)
		if curX >= buf.Width-2 {
			curX = buf.Width - 3
		}
		return buf, CursorPos{X: curX, Y: 6, Visible: true}
	}
	return buf, CursorPos{Visible: false}
}

func (w *DialogWidget) RenderBuf() CellBuf {
	if w == nil {
		return NewCellBuf(0, 0)
	}
	width := w.Width
	if width <= 0 {
		width = 54
	}
	height := w.Height
	if height <= 0 {
		height = 10
	}
	buf := NewCellBuf(width, height)
	buf.Fill(Rect{X: 0, Y: 0, W: width, H: height}, makeCell(' ', colorDlgText, colorDlgBg, false))

	for x := 1; x < width-1; x++ {
		buf.Set(x, 0, makeCell('─', colorDlgBorder, colorDlgBg, false))
		buf.Set(x, height-1, makeCell('─', colorDlgBorder, colorDlgBg, false))
	}
	for y := 1; y < height-1; y++ {
		buf.Set(0, y, makeCell('│', colorDlgBorder, colorDlgBg, false))
		buf.Set(width-1, y, makeCell('│', colorDlgBorder, colorDlgBg, false))
	}
	if width >= 2 && height >= 2 {
		buf.Set(0, 0, makeCell('╭', colorDlgBorder, colorDlgBg, false))
		buf.Set(width-1, 0, makeCell('╮', colorDlgBorder, colorDlgBg, false))
		buf.Set(0, height-1, makeCell('╰', colorDlgBorder, colorDlgBg, false))
		buf.Set(width-1, height-1, makeCell('╯', colorDlgBorder, colorDlgBg, false))
	}

	innerW := width - 2
	if w.Title != "" && innerW > 0 {
		titleText := truncateStringToWidth(" "+w.Title+" ", innerW)
		colOff := 0
		for _, r := range titleText {
			if colOff >= innerW {
				break
			}
			buf.Set(1+colOff, 1, makeCell(r, colorBtnFg, colorDlgBorder, true))
			colOff += runewidth.RuneWidth(r)
		}
	}
	if w.Prompt != "" && innerW > 2 {
		promptText := truncateStringToWidth(w.Prompt, innerW-2)
		colOff := 0
		for _, r := range promptText {
			if colOff >= innerW-2 {
				break
			}
			buf.Set(3+colOff, 3, makeCell(r, colorDlgText, colorDlgBg, false))
			colOff += runewidth.RuneWidth(r)
		}
	}

	if w.Kind == DialogProgress && innerW > 4 {
		cur := filepath.Base(w.Progress.Current)
		colOff := 0
		for _, r := range truncateStringToWidth(cur, innerW-4) {
			buf.Set(3+colOff, 3, makeCell(r, colorDlgText, colorDlgBg, false))
			colOff += runewidth.RuneWidth(r)
		}
		barW := innerW - 10
		if barW < 6 {
			barW = 6
		}
		pct := pctOf(w.Progress.Done, w.Progress.Total)
		barLine := fmt.Sprintf("%s %3d%%", renderProgressBarText(pct, barW), pct)
		colOff = 0
		for _, r := range barLine {
			if 3+colOff >= width-2 {
				break
			}
			buf.Set(3+colOff, 5, makeCell(r, colorDlgBorder, colorDlgBg, false))
			colOff += runewidth.RuneWidth(r)
		}
		totals := fmt.Sprintf("%s / %s  %s/s",
			FormatBytes(w.Progress.Done), FormatBytes(w.Progress.Total), FormatBytes(w.Progress.BytesPerSec))
		colOff = 0
		for _, r := range truncateStringToWidth(totals, innerW-4) {
			buf.Set(3+colOff, 7, makeCell(r, colorDlgMuted, colorDlgBg, false))
			colOff += runewidth.RuneWidth(r)
		}
		return buf
	}

	if w.Kind == DialogInput {
		label := "Input"
		for i, r := range label {
			if i >= innerW-2 {
				break
			}
			buf.Set(3+i, 5, makeCell(r, colorDlgMuted, colorDlgBg, false))
		}
		for x := 3; x < width-3; x++ {
			buf.Set(x, 6, makeCell(' ', colorDlgText, colorBlack, false))
		}
		inputValue := w.InputValue
		if runewidth.StringWidth(inputValue) > innerW-6 {
			inputValue = truncateStringToWidth(inputValue, innerW-6)
		}
		colOff := 0
		for _, r := range inputValue {
			if 3+colOff >= width-3 {
				break
			}
			buf.Set(3+colOff, 6, makeCell(r, colorDlgText, colorBlack, false))
			colOff += runewidth.RuneWidth(r)
		}
	}

	if len(w.Choices) > 0 {
		btnY := height - 3
		for i, choice := range w.Choices {
			text := " " + choice.Label + " "
			start := 3 + i*14
			if start >= width-2 {
				continue
			}
			colOff := 0
			for _, r := range text {
				if start+colOff >= width-1 {
					break
				}
				fg := colorBtnAltFg
				bg := colorBtnAltBg
				bold := false
				if i == w.ChoiceIdx {
					fg = colorBtnFg
					bg = colorBtnBg
					bold = true
				}
				buf.Set(start+colOff, btnY, makeCell(r, fg, bg, bold))
				colOff += runewidth.RuneWidth(r)
			}
		}
	}
	return buf
}

func (w *DialogWidget) HandleEvent(e input.Event) bool {
	if w == nil {
		return false
	}
	var ev input.KeyPressEvent
	switch k := e.(type) {
	case input.KeyPressEvent:
		ev = k
	case *input.KeyPressEvent:
		if k == nil {
			return false
		}
		ev = *k
	default:
		return false
	}

	key := ev.String()

	if w.Kind == DialogProgress {
		if key == "esc" {
			if w.OnCancel != nil {
				w.OnCancel()
			}
			return true
		}
		return true
	}

	if w.Kind == DialogChoice {
		switch key {
		case "esc":
			if w.OnPickChoice != nil {
				w.OnPickChoice(-1)
			}
			if w.OnCancel != nil {
				w.OnCancel()
			}
			return true
		case "enter", " ", "space":
			if w.OnPickChoice != nil {
				w.OnPickChoice(w.ChoiceIdx)
			}
			return true
		case "left", "up":
			if w.ChoiceIdx > 0 {
				w.ChoiceIdx--
			}
			return true
		case "right", "down", "tab":
			if w.ChoiceIdx < len(w.Choices)-1 {
				w.ChoiceIdx++
			}
			return true
		}
		lower := strings.ToLower(key)
		if ev.Text != "" {
			lower = strings.ToLower(ev.Text)
		}
		for i, c := range w.Choices {
			if c.Hotkey != "" && lower == strings.ToLower(c.Hotkey) {
				if w.OnPickChoice != nil {
					w.OnPickChoice(i)
				}
				return true
			}
		}
		return true
	}

	if w.Kind == DialogConfirm {
		switch key {
		case "esc", "n", "N":
			if w.OnCancel != nil {
				w.OnCancel()
			}
			return true
		case "enter", "y", "Y":
			if w.ChoiceIdx == 1 && key == "enter" {
				if w.OnCancel != nil {
					w.OnCancel()
				}
				return true
			}
			if w.OnConfirm != nil {
				w.OnConfirm(w.Action, w.Pending)
			}
			return true
		case "left", "up":
			w.ChoiceIdx = 0
			return true
		case "right", "down", "tab":
			if len(w.Choices) > 1 {
				w.ChoiceIdx = 1
			}
			return true
		}
		return true
	}

	// DialogInput
	switch key {
	case "esc":
		if w.OnCancel != nil {
			w.OnCancel()
		}
		return true
	case "enter":
		val := strings.TrimSpace(w.InputValue)
		if val == "" {
			if w.OnCancel != nil {
				w.OnCancel()
			}
			return true
		}
		if w.OnSubmitInput != nil {
			w.OnSubmitInput(w.Action, val, w.Pending)
		}
		return true
	case "backspace":
		runes := []rune(w.InputValue)
		if len(runes) > 0 {
			w.InputValue = string(runes[:len(runes)-1])
		}
		return true
	case " ", "space":
		w.InputValue += " "
		return true
	default:
		if ev.Text != "" {
			w.InputValue += ev.Text
			return true
		}
		if len(key) == 1 {
			w.InputValue += key
			return true
		}
	}
	return true
}

var _ Widget = (*DialogWidget)(nil)

func pctOf(done, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int((done * 100) / total)
	if p > 100 {
		p = 100
	}
	if p < 0 {
		p = 0
	}
	return p
}

// FormatBytes renders a byte count in human-friendly units.
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
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// SummarizeSources produces a short human description for confirm prompts.
func SummarizeSources(sources []string) string {
	if len(sources) == 1 {
		return filepath.Base(sources[0])
	}
	return fmt.Sprintf("%d items", len(sources))
}

func renderProgressBarText(percentage int, width int) string {
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}
	filled := (width * percentage) / 100
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	var bar strings.Builder
	for i := 0; i < filled; i++ {
		bar.WriteRune('█')
	}
	for i := filled; i < width; i++ {
		bar.WriteRune('░')
	}
	return bar.String()
}
