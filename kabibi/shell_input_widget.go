package main

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// ShellInputWidget owns the shell prompt prefix and single-line command input.
// The underlying textinput.Model is an implementation detail invisible to the parent.
type ShellInputWidget struct {
	Prompt string
	Width  int

	model textinput.Model
}

func NewShellInputWidget() *ShellInputWidget {
	ti := textinput.New()
	ti.Prompt = ""
	return &ShellInputWidget{model: ti}
}

func (w *ShellInputWidget) Value() string {
	if w == nil {
		return ""
	}
	return w.model.Value()
}

func (w *ShellInputWidget) SetValue(s string) {
	if w == nil {
		return
	}
	w.model.SetValue(s)
}

func (w *ShellInputWidget) Reset() {
	if w == nil {
		return
	}
	w.model.Reset()
}

func (w *ShellInputWidget) Focus() {
	if w == nil {
		return
	}
	w.model.Focus()
}

func (w *ShellInputWidget) Blur() {
	if w == nil {
		return
	}
	w.model.Blur()
}

func (w *ShellInputWidget) Focused() bool {
	if w == nil {
		return false
	}
	return w.model.Focused()
}

func (w *ShellInputWidget) Measure(c Constraints) Size {
	if c.MaxW <= 0 {
		c.MaxW = 80
	}
	return Size{W: c.MaxW, H: 1}
}

func (w *ShellInputWidget) Layout(r Rect) {
	if w == nil {
		return
	}
	w.Width = r.W
	promptLen := len([]rune(w.Prompt))
	w.model.Width = r.W - promptLen - 1
	if w.model.Width < 1 {
		w.model.Width = 1
	}
}

func (w *ShellInputWidget) Render() CellBuf {
	width := w.Width
	if width <= 0 {
		width = 80
	}
	buf := NewCellBuf(width, 1)
	buf.Fill(Rect{X: 0, Y: 0, W: width, H: 1}, Cell{FG: colorWhite, BG: colorDarkGray})
	text := w.Prompt + w.model.Value()
	x := 0
	for _, r := range text {
		if x >= width {
			break
		}
		if r == '\n' || r == '\r' {
			continue
		}
		buf.Set(x, 0, Cell{R: r, FG: colorWhite, BG: colorDarkGray})
		x++
	}
	if x < width {
		if w.model.Focused() {
			buf.Set(x, 0, Cell{R: ' ', FG: colorBlack, BG: colorWhite, Reverse: true})
		} else {
			buf.Set(x, 0, Cell{R: ' ', FG: colorWhite, BG: colorDarkGray})
		}
	}
	return buf
}

func (w *ShellInputWidget) HandleKey(msg tea.KeyMsg) tea.Cmd {
	if w == nil {
		return nil
	}
	updated, cmd := w.model.Update(msg)
	w.model = updated
	return cmd
}

func (w *ShellInputWidget) HandleMouse(msg tea.MouseMsg) tea.Cmd { return nil }

var _ Widget = (*ShellInputWidget)(nil)
var _ InputWidget = (*ShellInputWidget)(nil)
