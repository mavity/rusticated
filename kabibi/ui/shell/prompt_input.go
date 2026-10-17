package shell

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

const (
	defaultPromptMaxLines       = 6
	defaultPromptMaxScreenRatio = 0.75
)

// PromptInputOptions configures a PromptInput widget.
type PromptInputOptions struct {
	// Prompt returns the current prompt prefix string (e.g. "፧" or a dynamic path/status prompt).
	// Defaults to returning "፧" when nil.
	Prompt func() string

	// MaxLines caps the maximum number of visual rows the input may expand to (defaults to 6).
	MaxLines int

	// MaxScreenRatio caps the visual height as a fraction of the available screen height (defaults to 0.75).
	MaxScreenRatio float64
}

// PromptInput is a self-contained, reusable prompt + command-line input widget
// that wraps long single-line commands across multiple visual rows, shrinks back
// to 1 row when text is deleted, and caps its height to min(MaxLines, floor(MaxScreenRatio * MaxH)).
// Action keys (Enter, Ctrl+C) are intentionally left unhandled so the parent widget
// retains full control over command submission, cancellation, and buffer resets.
type PromptInput struct {
	mu sync.Mutex

	promptFn       func() string
	maxLines       int
	maxScreenRatio float64

	value      string
	cursorPos  int
	focused    bool
	history    []string
	historyIdx int
	scrollRow  int

	ctx ui.RenderContext
}

// NewPromptInput creates a new PromptInput widget with the given options.
func NewPromptInput(opts PromptInputOptions) *PromptInput {
	promptFn := opts.Prompt
	if promptFn == nil {
		promptFn = func() string { return "፧" }
	}
	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = defaultPromptMaxLines
	}
	ratio := opts.MaxScreenRatio
	if ratio <= 0 || ratio > 1.0 {
		ratio = defaultPromptMaxScreenRatio
	}
	return &PromptInput{
		promptFn:       promptFn,
		maxLines:       maxLines,
		maxScreenRatio: ratio,
		focused:        true,
		historyIdx:     -1,
	}
}

// SetPrompt updates the dynamic prompt provider function.
func (p *PromptInput) SetPrompt(fn func() string) {
	p.mu.Lock()
	if fn == nil {
		fn = func() string { return "፧" }
	}
	p.promptFn = fn
	p.mu.Unlock()
	p.invalidate()
}

// PromptText evaluates and returns the current prompt string.
func (p *PromptInput) PromptText() string {
	p.mu.Lock()
	fn := p.promptFn
	p.mu.Unlock()
	if fn == nil {
		return "፧"
	}
	return fn()
}

// Value returns the current command input string.
func (p *PromptInput) Value() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.value
}

// SetValue replaces the current input text and moves the cursor to the end.
func (p *PromptInput) SetValue(s string) {
	clean := strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	p.mu.Lock()
	p.value = clean
	p.cursorPos = len([]rune(clean))
	if clean == "" {
		p.scrollRow = 0
	}
	p.mu.Unlock()
}

// Reset clears the input value, resets the cursor to 0, and exits history navigation.
func (p *PromptInput) Reset() {
	p.mu.Lock()
	p.value = ""
	p.cursorPos = 0
	p.historyIdx = -1
	p.scrollRow = 0
	p.mu.Unlock()
}

// CursorPos returns the current rune cursor index within Value().
func (p *PromptInput) CursorPos() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cursorPos
}

// SetCursorPos clamps and sets the rune cursor index within Value().
func (p *PromptInput) SetCursorPos(pos int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len([]rune(p.value))
	if pos < 0 {
		p.cursorPos = 0
	} else if pos > n {
		p.cursorPos = n
	} else {
		p.cursorPos = pos
	}
}

// Focus gives keyboard focus to the input widget.
func (p *PromptInput) Focus() {
	p.mu.Lock()
	p.focused = true
	p.mu.Unlock()
}

// Blur removes keyboard focus from the input widget.
func (p *PromptInput) Blur() {
	p.mu.Lock()
	p.focused = false
	p.mu.Unlock()
}

// Focused reports whether the input widget currently has focus.
func (p *PromptInput) Focused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.focused
}

// AddHistory appends a non-empty command line to the input history and resets history navigation.
func (p *PromptInput) AddHistory(entry string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.TrimSpace(entry) != "" {
		p.history = append(p.history, entry)
	}
	p.historyIdx = -1
}

// History returns a copy of the recorded command history slice.
func (p *PromptInput) History() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.history))
	copy(out, p.history)
	return out
}

// maxAllowedHeight computes the height cap given the available screen height MaxH:
// min(maxLines, floor(maxScreenRatio * MaxH)), with a minimum of 1 row.
func (p *PromptInput) maxAllowedHeight(maxH int) int {
	capH := p.maxLines
	if capH <= 0 {
		capH = defaultPromptMaxLines
	}
	if maxH > 0 {
		screenCap := int(float64(maxH) * p.maxScreenRatio)
		if screenCap < 1 {
			screenCap = 1
		}
		if screenCap < capH {
			capH = screenCap
		}
	}
	if capH < 1 {
		capH = 1
	}
	return capH
}

// visualLayout holds the wrapped visual rows and cursor coordinates for a given width.
type visualLayout struct {
	rows       [][]terminal.Cell
	softWraps  []bool
	cursorX    int
	cursorY    int
	totalLines int
}

func runeCellWidth(r rune) int {
	w := runewidth.RuneWidth(r)
	if w <= 0 {
		return 1
	}
	return w
}

// buildVisualLayout lays out prompt + input runes into wrapped rows of width W
// and computes the exact visual (cursorX, cursorY) cell coordinates.
func buildVisualLayout(prompt, value string, cursorPos, width int) visualLayout {
	if width <= 0 {
		width = 80
	}

	style := terminal.NewStyle(0, 0, terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault)
	blank := terminal.Cell{Grapheme: " ", Style: style}

	newRow := func() []terminal.Cell {
		r := make([]terminal.Cell, width)
		for i := range r {
			r[i] = blank
		}
		return r
	}

	rows := [][]terminal.Cell{newRow()}
	softWraps := []bool{false}
	col := 0
	row := 0

	advanceWrap := func() {
		softWraps[row] = true
		rows = append(rows, newRow())
		softWraps = append(softWraps, false)
		row++
		col = 0
	}

	putRune := func(r rune) {
		if r == '\n' {
			rows = append(rows, newRow())
			softWraps = append(softWraps, false)
			row++
			col = 0
			return
		}
		rw := runeCellWidth(r)
		if rw > width {
			rw = 1
		}
		if col+rw > width {
			advanceWrap()
		}
		rows[row][col] = terminal.Cell{
			Grapheme: string(r),
			Style:    style,
		}
		for k := 1; k < rw && col+k < width; k++ {
			rows[row][col+k] = terminal.Cell{
				Grapheme: "",
				Style:    style,
			}
		}
		col += rw
	}

	for _, r := range prompt {
		putRune(r)
	}

	valRunes := []rune(value)
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(valRunes) {
		cursorPos = len(valRunes)
	}

	cursorX, cursorY := -1, -1

	for i, r := range valRunes {
		if i == cursorPos {
			rw := runeCellWidth(r)
			if rw > width {
				rw = 1
			}
			if col+rw > width {
				advanceWrap()
			}
			cursorX = col
			cursorY = row
		}
		putRune(r)
	}

	if cursorPos == len(valRunes) {
		// When the cursor sits at the end of the input and the current row is
		// completely full (col == width), wrap onto a new visual row so the
		// cursor sits at column 0 of the next line and the user sees where the
		// next character will appear.
		if col >= width {
			advanceWrap()
		}
		cursorX = col
		cursorY = row
	}

	if cursorX < 0 {
		cursorX = 0
		cursorY = 0
	}
	if cursorX >= width {
		cursorX = width - 1
	}

	return visualLayout{
		rows:       rows,
		softWraps:  softWraps,
		cursorX:    cursorX,
		cursorY:    cursorY,
		totalLines: len(rows),
	}
}

// Measure implements ui.Widget by computing the wrapped visual height of
// <prompt><input> at c.MaxW, clamped to min(MaxLines, floor(MaxScreenRatio * c.MaxH)).
func (p *PromptInput) Measure(c ui.Constraints) ui.Size {
	w := c.MaxW
	if w <= 0 {
		w = 80
	}

	p.mu.Lock()
	promptFn := p.promptFn
	value := p.value
	cursorPos := p.cursorPos
	capH := p.maxAllowedHeight(c.MaxH)
	p.mu.Unlock()

	prompt := "$ "
	if promptFn != nil {
		prompt = promptFn()
	}

	layout := buildVisualLayout(prompt, value, cursorPos, w)
	h := layout.totalLines
	if h < 1 {
		h = 1
	}
	if h > capH {
		h = capH
	}
	return ui.Size{W: w, H: h}
}

// Render implements ui.Widget by painting the visible slice of wrapped rows into
// a CellBuf and returning the cursor position relative to the buffer origin.
func (p *PromptInput) Render(rect ui.Rect, ctx ui.RenderContext) (ui.CellBuf, ui.CursorPos) {
	w := rect.W
	if w <= 0 {
		w = 80
	}

	p.mu.Lock()
	p.ctx = ctx
	promptFn := p.promptFn
	value := p.value
	cursorPos := p.cursorPos
	focused := p.focused && ctx.Focused
	scrollRow := p.scrollRow
	capH := p.maxAllowedHeight(rect.H)
	p.mu.Unlock()

	prompt := "$ "
	if promptFn != nil {
		prompt = promptFn()
	}

	layout := buildVisualLayout(prompt, value, cursorPos, w)

	visH := layout.totalLines
	if visH < 1 {
		visH = 1
	}
	if visH > capH {
		visH = capH
	}
	if rect.H > 0 && visH > rect.H {
		visH = rect.H
	}

	// Adjust vertical scroll window so the cursor row stays visible when capped.
	if layout.totalLines <= visH {
		scrollRow = 0
	} else {
		if layout.cursorY < scrollRow {
			scrollRow = layout.cursorY
		} else if layout.cursorY >= scrollRow+visH {
			scrollRow = layout.cursorY - visH + 1
		}
		maxScroll := layout.totalLines - visH
		if scrollRow > maxScroll {
			scrollRow = maxScroll
		}
		if scrollRow < 0 {
			scrollRow = 0
		}
	}

	p.mu.Lock()
	p.scrollRow = scrollRow
	p.mu.Unlock()

	buf := ui.NewCellBuf(w, visH)
	for y := 0; y < visH; y++ {
		srcRow := scrollRow + y
		if srcRow >= len(layout.rows) {
			break
		}
		rowCells := layout.rows[srcRow]
		for x := 0; x < w && x < len(rowCells); x++ {
			cell := rowCells[x]
			if x == w-1 && layout.softWraps[srcRow] && y+1 < visH {
				cell.Style = cell.Style.AddAttribute(terminal.CharSoftWrap)
			}
			buf.Set(x, y, cell)
		}
	}

	curY := layout.cursorY - scrollRow
	if curY < 0 {
		curY = 0
	} else if curY >= visH {
		curY = visH - 1
	}

	return buf, ui.CursorPos{
		X:       layout.cursorX,
		Y:       curY,
		Visible: focused,
	}
}

// HandleEvent implements ui.Widget for text editing, cursor movement, and history navigation.
// Enter and Ctrl+C return false so the parent widget handles submission and cancellation.
func (p *PromptInput) HandleEvent(e input.Event) bool {
	switch evt := e.(type) {
	case input.KeyPressEvent:
		return p.handleKeyEvent(evt)
	case *input.KeyPressEvent:
		if evt != nil {
			return p.handleKeyEvent(*evt)
		}
		return false

	case input.PasteEvent:
		return p.insertText(string(evt))
	case *input.PasteEvent:
		if evt != nil {
			return p.insertText(string(*evt))
		}
		return false

	default:
		return false
	}
}

func (p *PromptInput) insertText(text string) bool {
	if text == "" {
		return false
	}
	clean := strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")
	p.mu.Lock()
	if !p.focused {
		p.mu.Unlock()
		return false
	}
	runes := []rune(p.value)
	ins := []rune(clean)
	if p.cursorPos < 0 {
		p.cursorPos = 0
	} else if p.cursorPos > len(runes) {
		p.cursorPos = len(runes)
	}
	runes = append(runes[:p.cursorPos], append(ins, runes[p.cursorPos:]...)...)
	p.value = string(runes)
	p.cursorPos += len(ins)
	p.mu.Unlock()
	p.invalidate()
	return true
}

func (p *PromptInput) handleKeyEvent(ev input.KeyPressEvent) bool {
	keyStr := ev.String()

	// Parent handles submission and cancellation/interrupt.
	if keyStr == "enter" || keyStr == "ctrl+c" {
		return false
	}

	p.mu.Lock()
	if !p.focused {
		p.mu.Unlock()
		return false
	}
	p.mu.Unlock()

	switch keyStr {
	case "backspace":
		p.mu.Lock()
		if p.cursorPos > 0 {
			runes := []rune(p.value)
			if p.cursorPos > len(runes) {
				p.cursorPos = len(runes)
			}
			runes = append(runes[:p.cursorPos-1], runes[p.cursorPos:]...)
			p.value = string(runes)
			p.cursorPos--
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "delete":
		p.mu.Lock()
		runes := []rune(p.value)
		if p.cursorPos < len(runes) {
			runes = append(runes[:p.cursorPos], runes[p.cursorPos+1:]...)
			p.value = string(runes)
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "left":
		p.mu.Lock()
		if p.cursorPos > 0 {
			p.cursorPos--
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "right":
		p.mu.Lock()
		if p.cursorPos < len([]rune(p.value)) {
			p.cursorPos++
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "up":
		p.mu.Lock()
		if len(p.history) > 0 {
			if p.historyIdx < 0 {
				p.historyIdx = len(p.history) - 1
			} else if p.historyIdx > 0 {
				p.historyIdx--
			}
			p.value = p.history[p.historyIdx]
			p.cursorPos = len([]rune(p.value))
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "down":
		p.mu.Lock()
		if p.historyIdx >= 0 {
			if p.historyIdx < len(p.history)-1 {
				p.historyIdx++
				p.value = p.history[p.historyIdx]
			} else {
				p.historyIdx = -1
				p.value = ""
			}
			p.cursorPos = len([]rune(p.value))
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "home", "ctrl+a":
		p.mu.Lock()
		if p.cursorPos != 0 {
			p.cursorPos = 0
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "end", "ctrl+e":
		p.mu.Lock()
		endPos := len([]rune(p.value))
		if p.cursorPos != endPos {
			p.cursorPos = endPos
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		p.mu.Unlock()
		return true

	case "space":
		return p.insertText(" ")

	default:
		if ev.Text != "" {
			return p.insertText(ev.Text)
		}
		if len(keyStr) == 1 && keyStr[0] >= 32 && keyStr[0] < 127 {
			return p.insertText(keyStr)
		}
	}
	return false
}

func (p *PromptInput) invalidate() {
	p.mu.Lock()
	inv := p.ctx.Invalidate
	p.mu.Unlock()
	if inv != nil {
		inv()
	}
}
