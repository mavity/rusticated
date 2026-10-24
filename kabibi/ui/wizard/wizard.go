package wizard

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type Kind int

const (
	Choose Kind = iota
	Confirm
	Text
	Secret
)

type Prompt struct {
	Title    string
	Message  string
	Kind     Kind
	Options  []string
	Default  int
}

type Answer struct {
	Choice   int
	Accepted bool
	Text     string
}

type Widget struct {
	mu       sync.Mutex
	prompt   Prompt
	selected int
	value    []rune
	complete bool
	onAnswer func(Answer)
	onCancel func()
}

func New(prompt Prompt, onAnswer func(Answer), onCancel func()) *Widget {
	return &Widget{
		prompt:   prompt,
		selected: prompt.Default,
		onAnswer: onAnswer,
		onCancel: onCancel,
	}
}

func (w *Widget) Measure(c ui.Constraints) ui.Size {
	width := min(c.MaxW, 72)
	if width < 1 {
		width = 1
	}
	height := 5 + strings.Count(w.prompt.Message, "\n")
	if w.prompt.Kind == Choose {
		height += len(w.prompt.Options)
	}
	if height > c.MaxH {
		height = c.MaxH
	}
	if height < 1 {
		height = 1
	}
	return ui.Size{W: width, H: height}
}

func (w *Widget) Render(rect ui.Rect, _ ui.RenderContext) (terminal.CellBuf, ui.CursorPos) {
	size := w.Measure(ui.Constraints{MaxW: rect.W, MaxH: rect.H})
	buf := ui.NewCellBuf(size.W, size.H)
	fill := terminal.Cell{Grapheme: " ", Style: terminal.NewStyle(0xE5E5E5, 0x1A1F26, 0)}
	buf.Fill(terminal.Rect{W: size.W, H: size.H}, fill)
	if size.W < 2 || size.H < 2 {
		return buf, ui.CursorPos{}
	}
	border := terminal.NewStyle(0xFFD700, 0x1A1F26, 0)
	for x := 1; x < size.W-1; x++ {
		buf.Set(x, 0, terminal.Cell{Grapheme: "─", Style: border})
		buf.Set(x, size.H-1, terminal.Cell{Grapheme: "─", Style: border})
	}
	for y := 1; y < size.H-1; y++ {
		buf.Set(0, y, terminal.Cell{Grapheme: "│", Style: border})
		buf.Set(size.W-1, y, terminal.Cell{Grapheme: "│", Style: border})
	}
	for _, corner := range []struct {
		x, y int
		r    string
	}{{0, 0, "╭"}, {size.W - 1, 0, "╮"}, {0, size.H - 1, "╰"}, {size.W - 1, size.H - 1, "╯"}} {
		buf.Set(corner.x, corner.y, terminal.Cell{Grapheme: corner.r, Style: border})
	}

	w.mu.Lock()
	prompt := w.prompt
	selected := w.selected
	value := string(w.value)
	w.mu.Unlock()
	row := 1
	row = drawLine(&buf, row, prompt.Title, 0xFFD700, 0x1A1F26, size.H-1)
	for _, line := range strings.Split(prompt.Message, "\n") {
		row = drawLine(&buf, row, line, 0xE5E5E5, 0x1A1F26, size.H-1)
	}
	cursor := ui.CursorPos{}
	switch prompt.Kind {
	case Choose:
		for i, option := range prompt.Options {
			prefix := "  "
			fg := terminal.Color(0xE5E5E5)
			if i == selected {
				prefix = "> "
				fg = 0xFFD700
			}
			row = drawLine(&buf, row, prefix+option, fg, 0x1A1F26, size.H-1)
		}
	case Confirm:
		row = drawLine(&buf, row, "Y / N", 0xFFD700, 0x1A1F26, size.H-1)
	case Text, Secret:
		display := value
		if prompt.Kind == Secret {
			display = strings.Repeat("•", len([]rune(value)))
		}
		row = drawLine(&buf, row, "> "+display, 0xFFD700, 0x1A1F26, size.H-1)
		cursor = ui.CursorPos{X: min(size.W-2, 3+runewidth.StringWidth(display)), Y: min(row-1, size.H-2), Visible: true}
	}
	return buf, cursor
}

func drawLine(buf *terminal.CellBuf, row int, text string, fg, bg terminal.Color, maxRow int) int {
	if row >= maxRow || buf.Width < 3 {
		return row
	}
	x := 1
	for _, r := range text {
		width := runewidth.RuneWidth(r)
		if width < 1 {
			continue
		}
		if x+width > buf.Width-1 {
			break
		}
		buf.Set(x, row, terminal.Cell{Grapheme: string(r), Style: terminal.NewStyle(fg, bg, 0)})
		x += width
	}
	return row + 1
}

func (w *Widget) HandleEvent(event input.Event) bool {
	if w == nil {
		return false
	}
	switch ev := event.(type) {
	case input.PasteEvent:
		w.mu.Lock()
		if !w.complete && (w.prompt.Kind == Text || w.prompt.Kind == Secret) {
			w.value = append(w.value, []rune(string(ev))...)
		}
		w.mu.Unlock()
		return true
	case *input.PasteEvent:
		if ev == nil {
			return false
		}
		return w.HandleEvent(*ev)
	case input.KeyPressEvent:
		return w.handleKey(ev)
	case *input.KeyPressEvent:
		return ev != nil && w.handleKey(*ev)
	default:
		return false
	}
}

func (w *Widget) handleKey(ev input.KeyPressEvent) bool {
	w.mu.Lock()
	if w.complete {
		w.mu.Unlock()
		return true
	}
	key := strings.ToLower(ev.String())
	switch w.prompt.Kind {
	case Choose:
		switch key {
		case "up":
			if w.selected > 0 {
				w.selected--
			}
			w.mu.Unlock()
			return true
		case "down":
			if w.selected < len(w.prompt.Options)-1 {
				w.selected++
			}
			w.mu.Unlock()
			return true
		case "esc":
			w.complete = true
			cancel := w.onCancel
			w.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return true
		case "enter":
			answer := Answer{Choice: w.selected}
			w.complete = true
			accept := w.onAnswer
			w.mu.Unlock()
			if accept != nil {
				accept(answer)
			}
			return true
		}
		if ev.Text != "" && len([]rune(ev.Text)) == 1 {
			r := []rune(ev.Text)[0]
			if r >= '1' && r <= '9' && int(r-'1') < len(w.prompt.Options) {
				w.selected = int(r - '1')
				answer := Answer{Choice: w.selected}
				w.complete = true
				accept := w.onAnswer
				w.mu.Unlock()
				if accept != nil {
					accept(answer)
				}
				return true
			}
		}
	case Confirm:
		if key == "esc" || key == "n" {
			w.complete = true
			cancel := w.onCancel
			w.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return true
		}
		if key == "y" || key == "enter" {
			w.complete = true
			accept := w.onAnswer
			w.mu.Unlock()
			if accept != nil {
				accept(Answer{Accepted: true})
			}
			return true
		}
	case Text, Secret:
		switch key {
		case "esc":
			w.complete = true
			cancel := w.onCancel
			w.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return true
		case "enter":
			answer := Answer{Text: string(w.value)}
			w.complete = true
			accept := w.onAnswer
			w.mu.Unlock()
			if accept != nil {
				accept(answer)
			}
			return true
		case "backspace", "delete":
			if len(w.value) > 0 {
				w.value = w.value[:len(w.value)-1]
			}
			w.mu.Unlock()
			return true
		case "ctrl+u":
			w.value = nil
			w.mu.Unlock()
			return true
		default:
			if ev.Text != "" && !strings.ContainsAny(ev.Text, "\r\n") {
				w.value = append(w.value, []rune(ev.Text)...)
			}
			w.mu.Unlock()
			return true
		}
	}
	w.mu.Unlock()
	return true
}
