package widgets

import (
	"strings"
	"sync"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type TextInputStyles struct {
	Text       terminal.Style
	Focused    terminal.Style
	Selected   terminal.Style
	Overflow   terminal.Style
	Label      terminal.Style
	Background terminal.Style
}

type TextInputOptions struct {
	Value      string
	History    []string
	Complete   func(value string, cursor int) (string, int, bool)
	Styles     TextInputStyles
	OnChange   func(value string)
	OnChanged  func(value string)
	SelectAll  bool
	AllowPaste bool
}

type TextInput struct {
	value      []rune
	cursor     int
	anchor     int
	selecting  bool
	scrollCol  int
	history    []string
	historyIdx int
	complete   func(string, int) (string, int, bool)
	styles     TextInputStyles
	onChange   func(string)
	OnChanged  func(string)
	selectAll  bool
	allowPaste bool
}

func NewTextInput(opts TextInputOptions) *TextInput {
	w := &TextInput{
		value:      []rune(opts.Value),
		cursor:     len([]rune(opts.Value)),
		history:    append([]string(nil), opts.History...),
		historyIdx: -1,
		complete:   opts.Complete,
		styles:     opts.Styles,
		onChange:   opts.OnChange,
		OnChanged:  opts.OnChanged,
		selectAll:  opts.SelectAll,
		allowPaste: opts.AllowPaste,
	}
	if opts.SelectAll && len(w.value) > 0 {
		w.anchor = 0
		w.selecting = true
	}
	return w
}

func (w *TextInput) Value() string {
	if w == nil {
		return ""
	}
	return string(w.value)
}

func (w *TextInput) SetValue(value string) {
	if w == nil {
		return
	}
	w.value = []rune(value)
	w.cursor = len(w.value)
	w.anchor = w.cursor
	w.selecting = false
	w.scrollCol = 0
	w.changed()
}

func (w *TextInput) SetHistory(history []string) {
	if w == nil {
		return
	}
	w.history = append([]string(nil), history...)
	w.historyIdx = -1
}

func (w *TextInput) SetCompletion(complete func(value string, cursor int) (string, int, bool)) {
	if w == nil {
		return
	}
	w.complete = complete
}

func (w *TextInput) Measure(c Constraints) Size {
	width := c.MaxW
	if width <= 0 {
		width = 40
	}
	if c.MaxH > 0 && c.MaxH < 1 {
		return Size{}
	}
	return Size{W: width, H: 1}
}

func (w *TextInput) Render(r Rect, ctx RenderContext) string {
	width := r.W
	if width < 0 {
		width = 0
	}
	buf := terminal.NewCellBuf(width, 1)
	if w == nil || width == 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	if width == 1 {
		if len(w.value) > 0 {
			buf.Set(0, 0, terminal.Cell{Grapheme: "…", Style: styleOr(w.styles.Overflow, terminal.NewStyle(0xFFD700, 0x0D1014, terminal.CharBold))})
		}
		return ui.ANSIFrame(buf, CursorPos{Visible: ctx.Focused})
	}

	styles := w.styles
	textStyle := styleOr(styles.Text, terminal.NewStyle(0xE5E5E5, 0x1B2129, 0))
	focusedStyle := styleOr(styles.Focused, terminal.NewStyle(0xFFFFFF, 0x005F87, terminal.CharBold))
	selectedStyle := styleOr(styles.Selected, terminal.NewStyle(0x1A1F26, 0xFFD700, 0))
	overflowStyle := styleOr(styles.Overflow, terminal.NewStyle(0xFFD700, 0x1B2129, terminal.CharBold))
	background := styleOr(styles.Background, textStyle)
	buf.Fill(terminal.Rect{W: width, H: 1}, terminal.Cell{Grapheme: " ", Style: background})

	cursorCol := runeColumn(w.value, w.cursor)
	totalWidth := runeColumn(w.value, len(w.value))
	if w.scrollCol < 0 {
		w.scrollCol = 0
	}
	if w.scrollCol > totalWidth {
		w.scrollCol = totalWidth
	}

	leftOverflow := w.scrollCol > 0
	rightOverflow := totalWidth-w.scrollCol > width-boolInt(leftOverflow)
	available := width - boolInt(leftOverflow) - boolInt(rightOverflow)
	if available < 1 {
		available = 1
	}
	if cursorCol < w.scrollCol {
		w.scrollCol = cursorCol
		leftOverflow = w.scrollCol > 0
		rightOverflow = totalWidth-w.scrollCol > width-boolInt(leftOverflow)
		available = max(1, width-boolInt(leftOverflow)-boolInt(rightOverflow))
	}
	if cursorCol >= w.scrollCol+available {
		w.scrollCol = cursorCol - available + 1
		leftOverflow = w.scrollCol > 0
		rightOverflow = totalWidth-w.scrollCol > width-boolInt(leftOverflow)
		available = max(1, width-boolInt(leftOverflow)-boolInt(rightOverflow))
	}
	if leftOverflow && width > 0 {
		buf.Set(0, 0, terminal.Cell{Grapheme: "…", Style: overflowStyle})
	}
	if rightOverflow && width > 0 {
		buf.Set(width-1, 0, terminal.Cell{Grapheme: "…", Style: overflowStyle})
	}

	startX := boolInt(leftOverflow)
	col := 0
	selStart, selEnd := w.selectionBounds()
	for i, r := range w.value {
		rw := runeWidth(r)
		x := startX + col - w.scrollCol
		inSelection := w.selecting && i >= selStart && i < selEnd
		style := textStyle
		if ctx.Focused {
			style = focusedStyle
		}
		if inSelection {
			style = selectedStyle
		}
		if x >= startX && x+rw <= startX+available {
			buf.Set(x, 0, terminal.Cell{Grapheme: string(r), Style: style})
			for n := 1; n < rw; n++ {
				buf.Set(x+n, 0, terminal.Cell{Grapheme: "", Style: style})
			}
		}
		col += rw
	}
	cursorX := startX + cursorCol - w.scrollCol
	if cursorX < startX {
		cursorX = startX
	}
	if cursorX >= startX+available {
		cursorX = startX + available - 1
	}
	return ui.ANSIFrame(buf, CursorPos{X: cursorX, Y: 0, Visible: ctx.Focused})
}

func (w *TextInput) HandleEvent(event input.Event) bool {
	if w == nil {
		return false
	}
	switch ev := event.(type) {
	case input.PasteEvent:
		if !w.allowPaste {
			return false
		}
		w.insert(strings.ReplaceAll(strings.ReplaceAll(string(ev), "\r\n", " "), "\n", " "))
		return true
	case *input.PasteEvent:
		return ev != nil && w.HandleEvent(*ev)
	case input.KeyPressEvent:
		return w.handleKey(ev)
	case *input.KeyPressEvent:
		return ev != nil && w.handleKey(*ev)
	default:
		return false
	}
}

func (w *TextInput) handleKey(ev input.KeyPressEvent) bool {
	key := strings.ToLower(ev.String())
	switch key {
	case "left", "shift+left":
		w.moveCursor(w.cursor-1, strings.HasPrefix(key, "shift+"))
	case "right", "shift+right":
		w.moveCursor(w.cursor+1, strings.HasPrefix(key, "shift+"))
	case "home", "ctrl+a", "shift+home":
		if key == "ctrl+a" {
			w.anchor, w.cursor, w.selecting = 0, len(w.value), true
		} else {
			w.moveCursor(0, strings.HasPrefix(key, "shift+"))
		}
	case "end", "ctrl+e", "shift+end":
		w.moveCursor(len(w.value), strings.HasPrefix(key, "shift+"))
	case "ctrl+left":
		w.moveCursor(wordBoundary(w.value, w.cursor, -1), false)
	case "ctrl+right":
		w.moveCursor(wordBoundary(w.value, w.cursor, 1), false)
	case "backspace":
		if !w.deleteSelection() && w.cursor > 0 {
			w.value = append(w.value[:w.cursor-1], w.value[w.cursor:]...)
			w.cursor--
			w.changed()
		}
	case "delete":
		if !w.deleteSelection() && w.cursor < len(w.value) {
			w.value = append(w.value[:w.cursor], w.value[w.cursor+1:]...)
			w.changed()
		}
	case "ctrl+c":
		if start, end, ok := w.selectionBoundsOK(); ok {
			setClipboard(string(w.value[start:end]))
		}
	case "ctrl+x":
		if start, end, ok := w.selectionBoundsOK(); ok {
			setClipboard(string(w.value[start:end]))
			w.value = append(w.value[:start], w.value[end:]...)
			w.cursor = start
			w.selecting = false
			w.changed()
		}
	case "ctrl+v":
		value, err := clipboard.ReadAll()
		if err != nil {
			value = getClipboard()
		}
		if value != "" {
			w.insert(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", " "), "\n", " "))
		}
	case "ctrl+u":
		w.value = append([]rune(nil), w.value[w.cursor:]...)
		w.cursor = 0
		w.selecting = false
		w.changed()
	case "ctrl+k":
		w.value = w.value[:w.cursor]
		w.selecting = false
		w.changed()
	case "up", "alt+up", "ctrl+p":
		return w.recallHistory(-1)
	case "down", "alt+down", "ctrl+n":
		return w.recallHistory(1)
	case "tab":
		if w.complete != nil {
			if value, cursor, ok := w.complete(string(w.value), w.cursor); ok {
				w.value = []rune(value)
				w.cursor = clamp(cursor, 0, len(w.value))
				w.selecting = false
				w.changed()
				return true
			}
		}
		return false
	case "space", " ":
		w.insert(" ")
	default:
		if ev.Text != "" && !strings.Contains(key, "+") {
			w.insert(ev.Text)
		} else if len([]rune(key)) == 1 && key[0] >= 32 {
			w.insert(key)
		} else {
			return false
		}
	}
	return true
}

func (w *TextInput) insert(text string) {
	runes := []rune(text)
	if len(runes) == 0 {
		return
	}
	start, end, selected := w.selectionBoundsOK()
	if selected {
		w.value = append(w.value[:start], append(runes, w.value[end:]...)...)
		w.cursor = start + len(runes)
		w.selecting = false
	} else {
		w.value = append(w.value[:w.cursor], append(runes, w.value[w.cursor:]...)...)
		w.cursor += len(runes)
	}
	w.changed()
}

func (w *TextInput) moveCursor(pos int, extend bool) {
	pos = clamp(pos, 0, len(w.value))
	if extend {
		if !w.selecting {
			w.anchor = w.cursor
		}
		w.selecting = true
		w.cursor = pos
	} else {
		w.cursor = pos
		w.anchor = pos
		w.selecting = false
	}
}

func (w *TextInput) deleteSelection() bool {
	start, end, ok := w.selectionBoundsOK()
	if !ok {
		return false
	}
	w.value = append(w.value[:start], w.value[end:]...)
	w.cursor = start
	w.selecting = false
	w.changed()
	return true
}

func (w *TextInput) selectionBounds() (int, int) {
	start, end, ok := w.selectionBoundsOK()
	if !ok {
		return 0, 0
	}
	return start, end
}

func (w *TextInput) selectionBoundsOK() (int, int, bool) {
	if !w.selecting || w.anchor == w.cursor {
		return 0, 0, false
	}
	start, end := w.anchor, w.cursor
	if start > end {
		start, end = end, start
	}
	return start, end, true
}

func (w *TextInput) recallHistory(direction int) bool {
	if len(w.history) == 0 {
		return false
	}
	if w.historyIdx < 0 {
		if direction < 0 {
			w.historyIdx = len(w.history) - 1
		} else {
			return false
		}
	} else if direction < 0 {
		if w.historyIdx > 0 {
			w.historyIdx--
		}
	} else if w.historyIdx < len(w.history)-1 {
		w.historyIdx++
	} else {
		w.historyIdx = -1
		w.value = nil
		w.cursor = 0
		w.selecting = false
		w.changed()
		return true
	}
	w.value = []rune(w.history[w.historyIdx])
	w.cursor = len(w.value)
	w.selecting = false
	w.changed()
	return true
}

func (w *TextInput) changed() {
	if w.onChange != nil {
		w.onChange(string(w.value))
	}
	if w.OnChanged != nil {
		w.OnChanged(string(w.value))
	}
}

func wordBoundary(value []rune, cursor, direction int) int {
	isWord := func(r rune) bool {
		return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127
	}
	if direction < 0 {
		for cursor > 0 && !isWord(value[cursor-1]) {
			cursor--
		}
		for cursor > 0 && isWord(value[cursor-1]) {
			cursor--
		}
		return cursor
	}
	for cursor < len(value) && !isWord(value[cursor]) {
		cursor++
	}
	for cursor < len(value) && isWord(value[cursor]) {
		cursor++
	}
	return cursor
}

var (
	clipboardMu       sync.Mutex
	clipboardFallback string
)

func setClipboard(value string) {
	clipboardMu.Lock()
	clipboardFallback = value
	clipboardMu.Unlock()
	_ = clipboard.WriteAll(value)
}

func getClipboard() string {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	return clipboardFallback
}
