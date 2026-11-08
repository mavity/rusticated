package shell

import (
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

const (
	defaultPromptMaxLines       = 6
	defaultPromptMaxScreenRatio = 0.75
	speculativeCompletionDelay  = time.Second
)

type promptCompletionState struct {
	candidates  []completionCandidate
	speculative bool
	active      bool
	pending     bool
	selected    int
	insertion   string
}

// PromptInputOptions configures a PromptInput widget.
type PromptInputOptions struct {
	// Prompt returns the current prompt prefix string (e.g. "፧" or a dynamic path/status prompt).
	// Defaults to returning "፧" when nil.
	Prompt func() string

	// MaxLines caps the maximum number of visual rows the input may expand to (defaults to 6).
	MaxLines int

	// MaxScreenRatio caps the visual height as a fraction of the available screen height (defaults to 0.75).
	MaxScreenRatio float64

	// StyleInput returns one terminal style per input rune. Nil styles use terminal defaults.
	StyleInput func(value string) []terminal.Style

	// Complete returns candidates for the current input and rune cursor position.
	Complete func(value string, cursor int) []completionCandidate
}

func (p *PromptInput) cursorMoved() {
	p.mu.Lock()
	p.completion = nil
	p.completionGeneration++
	if p.completionTimer != nil {
		p.completionTimer.Stop()
		p.completionTimer = nil
	}
	p.mu.Unlock()
	p.invalidate()
}

func (p *PromptInput) dismissCompletion() bool {
	p.mu.Lock()
	if p.completion == nil {
		p.mu.Unlock()
		return false
	}
	p.completion = nil
	p.completionGeneration++
	if p.completionTimer != nil {
		p.completionTimer.Stop()
		p.completionTimer = nil
	}
	p.mu.Unlock()
	p.invalidate()
	return true
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
	styleInput     func(value string) []terminal.Style
	complete       func(value string, cursor int) []completionCandidate

	value                string
	cursorPos            int
	history              []string
	historyIdx           int
	scrollRow            int
	firstInputAt         time.Time
	completion           *promptCompletionState
	completionTimer      *time.Timer
	completionGeneration uint64

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
		styleInput:     opts.StyleInput,
		complete:       opts.Complete,
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
	clean := strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	p.mu.Lock()
	p.value = clean
	p.cursorPos = len([]rune(clean))
	if clean == "" {
		p.scrollRow = 0
		p.firstInputAt = time.Time{}
	} else if p.firstInputAt.IsZero() {
		p.firstInputAt = time.Now()
	}
	p.mu.Unlock()
	p.inputChanged()
}

// Reset clears the input value, resets the cursor to 0, and exits history navigation.
func (p *PromptInput) Reset() {
	p.mu.Lock()
	p.value = ""
	p.cursorPos = 0
	p.historyIdx = -1
	p.scrollRow = 0
	p.firstInputAt = time.Time{}
	p.mu.Unlock()
	p.clearCompletionTimer()
	p.invalidate()
}

func (p *PromptInput) Close() {
	p.clearCompletionTimer()
}

// FirstInputAt returns the timestamp when the first character of the current
// input buffer was typed (or zero if the buffer is empty).
func (p *PromptInput) FirstInputAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.firstInputAt
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
	n := len([]rune(p.value))
	if pos < 0 {
		p.cursorPos = 0
	} else if pos > n {
		p.cursorPos = n
	} else {
		p.cursorPos = pos
	}
	p.mu.Unlock()
	p.cursorMoved()
}

// InsertNewline inserts a shell continuation newline at the current cursor.
func (p *PromptInput) InsertNewline() {
	p.mu.Lock()
	runes := []rune(p.value)
	if p.cursorPos < 0 {
		p.cursorPos = 0
	} else if p.cursorPos > len(runes) {
		p.cursorPos = len(runes)
	}
	runes = append(runes, 0)
	copy(runes[p.cursorPos+1:], runes[p.cursorPos:])
	runes[p.cursorPos] = '\n'
	p.value = string(runes)
	p.cursorPos++
	if p.firstInputAt.IsZero() {
		p.firstInputAt = time.Now()
	}
	p.mu.Unlock()
	p.inputChanged()
}

func (p *PromptInput) CompletionActive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completion != nil && p.completion.active
}

func (p *PromptInput) CompletionMenuVisible() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return completionHasMenu(p.completion)
}

func (p *PromptInput) CompletionVisible() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.completion != nil
}

func (p *PromptInput) RequestCompletion() bool {
	p.mu.Lock()
	if state := p.completion; state != nil {
		if state.active {
			if !state.pending && len(state.candidates) > 0 {
				state.selected = (state.selected + 1) % len(state.candidates)
			}
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		if state.speculative && len(state.candidates) > 1 {
			state.speculative = false
			state.active = true
			state.selected = 0
			p.mu.Unlock()
			p.invalidate()
			return true
		}
		if state.speculative && len(state.candidates) == 1 {
			candidate := state.candidates[0]
			p.mu.Unlock()
			p.applyCompletion(candidate)
			return true
		}
	}
	provider := p.complete
	value, cursor := p.value, p.cursorPos
	if provider == nil {
		p.mu.Unlock()
		return false
	}
	p.completionGeneration++
	generation := p.completionGeneration
	if p.completionTimer != nil {
		p.completionTimer.Stop()
		p.completionTimer = nil
	}
	p.completion = &promptCompletionState{active: true, pending: true}
	p.mu.Unlock()

	p.invalidate()
	go func() {
		candidates := provider(value, cursor)
		p.mu.Lock()
		if generation != p.completionGeneration || p.value != value || p.cursorPos != cursor {
			p.mu.Unlock()
			return
		}
		if len(candidates) == 0 {
			p.completion = nil
			p.mu.Unlock()
			p.invalidate()
			return
		}
		if len(candidates) == 1 {
			p.applyCompletionLocked(candidates[0])
			p.completion = nil
			p.mu.Unlock()
			p.inputChanged()
			return
		}
		p.completion = &promptCompletionState{
			candidates: candidates,
			active:     true,
			selected:   0,
		}
		p.mu.Unlock()
		p.invalidate()
	}()
	return true
}

func (p *PromptInput) AcceptCompletion() bool {
	p.mu.Lock()
	state := p.completion
	if state == nil || !state.active || state.selected < 0 ||
		state.selected >= len(state.candidates) {
		p.mu.Unlock()
		return false
	}
	candidate := state.candidates[state.selected]
	p.mu.Unlock()
	p.applyCompletion(candidate)
	return true
}

func (p *PromptInput) applyCompletion(candidate completionCandidate) {
	p.mu.Lock()
	p.applyCompletionLocked(candidate)
	p.mu.Unlock()
	p.inputChanged()
}

func (p *PromptInput) applyCompletionLocked(candidate completionCandidate) {
	runes := []rune(p.value)
	insert := []rune(candidate.Insert)
	if candidate.Space {
		insert = append(insert, ' ')
	}
	runes = append(runes, make([]rune, len(insert))...)
	copy(runes[p.cursorPos+len(insert):], runes[p.cursorPos:len(runes)-len(insert)])
	copy(runes[p.cursorPos:], insert)
	p.value = string(runes)
	p.cursorPos += len(insert)
}

func (p *PromptInput) inputChanged() {
	p.mu.Lock()
	p.completion = nil
	p.completionGeneration++
	generation := p.completionGeneration
	if p.completionTimer != nil {
		p.completionTimer.Stop()
		p.completionTimer = nil
	}
	provider := p.complete
	value, cursor := p.value, p.cursorPos
	if provider != nil && value != "" {
		p.completionTimer = time.AfterFunc(speculativeCompletionDelay, func() {
			candidates := provider(value, cursor)
			if len(candidates) == 0 {
				return
			}
			p.mu.Lock()
			if generation != p.completionGeneration || p.value != value || p.cursorPos != cursor {
				p.mu.Unlock()
				return
			}
			p.completion = &promptCompletionState{
				candidates:  candidates,
				speculative: true,
			}
			p.mu.Unlock()
			p.invalidate()
		})
	}
	p.mu.Unlock()
	p.invalidate()
}

func (p *PromptInput) clearCompletionTimer() {
	p.mu.Lock()
	p.completionGeneration++
	if p.completionTimer != nil {
		p.completionTimer.Stop()
		p.completionTimer = nil
	}
	p.completion = nil
	p.mu.Unlock()
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
	inputCells []inputCell
}

type inputCell struct {
	x, y, width int
}

var bashInputLexer = lexers.Get("bash")

var (
	bashKeywordStyle  = terminal.NewStyle(0x5B82A6, 0, terminal.CharResetBackgroundDefault)
	bashStringStyle   = terminal.NewStyle(0x5E8C6A, 0, terminal.CharResetBackgroundDefault)
	bashNumberStyle   = terminal.NewStyle(0xA67C52, 0, terminal.CharResetBackgroundDefault)
	bashOperatorStyle = terminal.NewStyle(0x856B9E, 0, terminal.CharResetBackgroundDefault)
	bashCommentStyle  = terminal.NewStyle(0x76808A, 0, terminal.CharResetBackgroundDefault)
)

func bashInputStyles(value string) []terminal.Style {
	runeCount := len([]rune(value))
	styles := make([]terminal.Style, runeCount)
	defaultStyle := terminal.NewStyle(
		0,
		0,
		terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault|terminal.CharBold,
	)
	for i := range styles {
		styles[i] = defaultStyle
	}
	if bashInputLexer == nil {
		return styles
	}
	iter, err := bashInputLexer.Tokenise(nil, value)
	if err != nil {
		return styles
	}
	runeOffset := 0
	for _, token := range iter.Tokens() {
		style := defaultStyle
		switch {
		case token.Type.InCategory(chroma.Comment):
			style = bashCommentStyle
		case token.Type.InCategory(chroma.Keyword), token.Type == chroma.NameBuiltin:
			style = bashKeywordStyle
		case token.Type.InSubCategory(chroma.LiteralString):
			style = bashStringStyle
		case token.Type.InSubCategory(chroma.LiteralNumber),
			strings.Contains(strings.ToLower(token.Type.String()), "variable"):
			style = bashNumberStyle
		case token.Type.InCategory(chroma.Operator), token.Type.InCategory(chroma.Punctuation):
			style = bashOperatorStyle
		}
		style = style.AddAttribute(terminal.CharBold)
		for range token.Value {
			if runeOffset < len(styles) {
				styles[runeOffset] = style
			}
			runeOffset++
		}
	}
	return styles
}

func aiPromptStyles(value string) []terminal.Style {
	style := terminal.NewStyle(
		0,
		0,
		terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault|
			terminal.CharBold|terminal.CharItalic,
	)
	styles := make([]terminal.Style, len([]rune(value)))
	for i := range styles {
		styles[i] = style
	}
	return styles
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
func buildVisualLayout(prompt, value string, cursorPos, width int, inputStyles []terminal.Style) visualLayout {
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

	putRune := func(r rune, runeStyle terminal.Style) inputCell {
		if r == '\n' {
			rows = append(rows, newRow())
			softWraps = append(softWraps, false)
			row++
			col = 0
			return inputCell{y: row}
		}
		rw := runeCellWidth(r)
		if rw > width {
			rw = 1
		}
		if col+rw > width {
			advanceWrap()
		}
		position := inputCell{x: col, y: row, width: rw}
		rows[row][col] = terminal.Cell{
			Grapheme: string(r),
			Style:    runeStyle,
		}
		for k := 1; k < rw && col+k < width; k++ {
			rows[row][col+k] = terminal.Cell{
				Grapheme: "",
				Style:    runeStyle,
			}
		}
		col += rw
		return position
	}

	for _, r := range prompt {
		putRune(r, style)
	}

	valRunes := []rune(value)
	if cursorPos < 0 {
		cursorPos = 0
	}
	if cursorPos > len(valRunes) {
		cursorPos = len(valRunes)
	}

	cursorX, cursorY := -1, -1
	inputCells := make([]inputCell, 0, len(valRunes))

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
		runeStyle := style
		if i < len(inputStyles) {
			runeStyle = inputStyles[i]
		}
		inputCells = append(inputCells, putRune(r, runeStyle))
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
		inputCells: inputCells,
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
	completion := clonePromptCompletion(p.completion)
	capH := p.maxAllowedHeight(c.MaxH)
	p.mu.Unlock()

	prompt := "$ "
	if promptFn != nil {
		prompt = promptFn()
	}

	displayValue, displayCursor, styles := promptCompletionDisplay(value, cursorPos, completion, nil)
	layout := buildVisualLayout(prompt, displayValue, displayCursor, w, styles)
	h := layout.totalLines
	if completionHasMenu(completion) {
		if capH > 1 {
			h = min(h, capH-1) + 1
		}
	}
	if h < 1 {
		h = 1
	}
	if h > capH {
		h = capH
	}
	return ui.Size{W: w, H: h}
}

// Render implements ui.Widget by returning ANSI for the visible slice of wrapped rows.
func (p *PromptInput) Render(rect ui.Rect, ctx ui.RenderContext) string {
	w := rect.W
	if w <= 0 {
		w = 80
	}

	p.mu.Lock()
	p.ctx = ctx
	promptFn := p.promptFn
	styleInput := p.styleInput
	value := p.value
	cursorPos := p.cursorPos
	completion := clonePromptCompletion(p.completion)
	scrollRow := p.scrollRow
	capH := p.maxAllowedHeight(rect.H)
	p.mu.Unlock()

	prompt := "$ "
	if promptFn != nil {
		prompt = promptFn()
	}

	var inputStyles []terminal.Style
	if styleInput != nil {
		inputStyles = styleInput(value)
	}
	displayValue, displayCursor, inputStyles := promptCompletionDisplay(value, cursorPos, completion, inputStyles)
	layout := buildVisualLayout(prompt, displayValue, displayCursor, w, inputStyles)
	menuRow := completionHasMenu(completion) && capH > 1
	inputMaxH := capH
	if menuRow {
		inputMaxH--
	}
	if rect.H > 0 {
		if menuRow && rect.H > 1 {
			if inputMaxH > rect.H-1 {
				inputMaxH = rect.H - 1
			}
		} else if inputMaxH > rect.H {
			inputMaxH = rect.H
			menuRow = false
		}
	}
	if inputMaxH < 1 {
		inputMaxH = 1
		menuRow = false
	}
	inputVisH := min(layout.totalLines, inputMaxH)
	visH := inputVisH
	if menuRow {
		visH++
	}

	// Adjust vertical scroll window so the cursor row stays visible when capped.
	if layout.totalLines <= inputVisH {
		scrollRow = 0
	} else {
		if layout.cursorY < scrollRow {
			scrollRow = layout.cursorY
		} else if layout.cursorY >= scrollRow+inputVisH {
			scrollRow = layout.cursorY - inputVisH + 1
		}
		maxScroll := layout.totalLines - inputVisH
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
	for y := 0; y < inputVisH; y++ {
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
	if menuRow {
		renderCompletionMenu(&buf, inputVisH, completion)
	}

	curY := layout.cursorY - scrollRow
	if curY < 0 {
		curY = 0
	} else if curY >= inputVisH {
		curY = inputVisH - 1
	}

	return ui.ANSIFrame(buf, ui.CursorPos{
		X:       layout.cursorX,
		Y:       curY,
		Visible: ctx.Focused,
	})
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
	clean := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	p.mu.Lock()
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
	if p.value != "" && p.firstInputAt.IsZero() {
		p.firstInputAt = time.Now()
	}
	p.mu.Unlock()
	p.inputChanged()
	return true
}

func (p *PromptInput) handleKeyEvent(ev input.KeyPressEvent) bool {
	keyStr := ev.String()

	// Parent handles submission and cancellation/interrupt.
	if keyStr == "enter" || keyStr == "ctrl+c" {
		return false
	}
	if keyStr == "esc" && p.dismissCompletion() {
		return true
	}

	p.mu.Lock()
	completion := p.completion
	if completion != nil && completion.active && (keyStr == "left" || keyStr == "right") {
		if keyStr == "left" && completion.selected > 0 {
			completion.selected--
		} else if keyStr == "right" && completion.selected+1 < len(completion.candidates) {
			completion.selected++
		}
		p.mu.Unlock()
		p.invalidate()
		return true
	}
	speculativeRight := completion != nil && completion.speculative && keyStr == "right"
	hasTextRight := p.cursorPos < len([]rune(p.value))
	acceptSpeculative := speculativeRight && !hasTextRight && len(completion.candidates) == 1
	var speculativeCandidate completionCandidate
	if acceptSpeculative {
		speculativeCandidate = completion.candidates[0]
	}
	p.mu.Unlock()
	if acceptSpeculative {
		p.applyCompletion(speculativeCandidate)
		return true
	}

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
			if p.value == "" {
				p.firstInputAt = time.Time{}
			}
			p.mu.Unlock()
			p.inputChanged()
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
			if p.value == "" {
				p.firstInputAt = time.Time{}
			}
			p.mu.Unlock()
			p.inputChanged()
			return true
		}
		p.mu.Unlock()
		return true

	case "left":
		p.mu.Lock()
		if p.cursorPos > 0 {
			p.cursorPos--
			p.mu.Unlock()
			p.cursorMoved()
			return true
		}
		p.mu.Unlock()
		return true

	case "right":
		p.mu.Lock()
		if p.cursorPos < len([]rune(p.value)) {
			p.cursorPos++
			p.mu.Unlock()
			p.cursorMoved()
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
			if p.value != "" && p.firstInputAt.IsZero() {
				p.firstInputAt = time.Now()
			}
			p.mu.Unlock()
			p.inputChanged()
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
				if p.value != "" && p.firstInputAt.IsZero() {
					p.firstInputAt = time.Now()
				}
			} else {
				p.historyIdx = -1
				p.value = ""
				p.firstInputAt = time.Time{}
			}
			p.cursorPos = len([]rune(p.value))
			p.mu.Unlock()
			p.inputChanged()
			return true
		}
		p.mu.Unlock()
		return true

	case "home", "ctrl+a":
		p.mu.Lock()
		if p.cursorPos != 0 {
			p.cursorPos = 0
			p.mu.Unlock()
			p.cursorMoved()
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
			p.cursorMoved()
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

func completionHasMenu(state *promptCompletionState) bool {
	return state != nil && len(state.candidates) > 1
}

func clonePromptCompletion(state *promptCompletionState) *promptCompletionState {
	if state == nil {
		return nil
	}
	copy := *state
	copy.candidates = append([]completionCandidate(nil), state.candidates...)
	return &copy
}

func promptCompletionDisplay(value string, cursor int, state *promptCompletionState, styles []terminal.Style) (string, int, []terminal.Style) {
	if state == nil || !state.speculative || len(state.candidates) != 1 {
		return value, cursor, styles
	}
	insert := []rune(state.candidates[0].Insert)
	if state.candidates[0].Space {
		insert = append(insert, ' ')
	}
	if len(insert) == 0 {
		return value, cursor, styles
	}
	runes := []rune(value)
	if cursor < 0 || cursor > len(runes) {
		cursor = len(runes)
	}
	display := append([]rune(nil), runes[:cursor]...)
	display = append(display, insert...)
	display = append(display, runes[cursor:]...)
	if len(styles) < len(runes) {
		styles = append(styles, make([]terminal.Style, len(runes)-len(styles))...)
	}
	ghostStyle := terminal.NewStyle(0x808080, 0, terminal.CharResetBackgroundDefault)
	baseStyles := append([]terminal.Style(nil), styles...)
	styles = append(append([]terminal.Style(nil), baseStyles[:cursor]...), make([]terminal.Style, len(insert))...)
	for i := cursor; i < cursor+len(insert); i++ {
		styles[i] = ghostStyle
	}
	styles = append(styles, baseStyles[cursor:]...)
	return string(display), cursor, styles
}

func renderCompletionMenu(buf *ui.CellBuf, row int, state *promptCompletionState) {
	if row < 0 || row >= buf.Height || state == nil {
		return
	}
	x := 0
	for i, candidate := range state.candidates {
		label := candidate.Display
		if i > 0 {
			label = "  " + label
		}
		style := terminal.NewStyle(0x808080, 0, terminal.CharResetBackgroundDefault)
		if state.active && i == state.selected {
			style = terminal.NewStyle(0x00FFFF, 0, terminal.CharResetBackgroundDefault|terminal.CharBold)
		}
		for _, r := range label {
			width := runeCellWidth(r)
			if x+width > buf.Width {
				return
			}
			buf.Set(x, row, terminal.Cell{Grapheme: string(r), Style: style})
			for offset := 1; offset < width; offset++ {
				buf.Set(x+offset, row, terminal.Cell{Style: style})
			}
			x += width
		}
	}
}

func (p *PromptInput) invalidate() {
	p.mu.Lock()
	inv := p.ctx.Invalidate
	p.mu.Unlock()
	if inv != nil {
		inv()
	}
}
