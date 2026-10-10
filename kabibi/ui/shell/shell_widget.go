package shell

import (
	"bytes"
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// ShellOptions configures shell behavior and callbacks
type ShellOptions struct {
	WriteToScrollback func(text string) // Callback to write lines to the host's scrollback
}

// Shell is a minimal shell widget that implements the ui.Widget interface.
// It manages a history of CommandBlocks (prompt+input+output) with automatic
// ejection of overflow rows to the native terminal scrollback buffer.
type Shell struct {
	// History model: structured CommandBlock entries
	blocks []CommandBlock

	// Ejection watermark: how many visual rows have been flushed to native scrollback
	previouslyEjected int

	// Input buffer (for currently active incomplete command)
	inputValue string
	inputFocus bool
	cursorPos  int

	// Layout
	width  int
	height int
	prompt string

	// Options
	opts ShellOptions

	// Context from last render pass
	ctx ui.RenderContext
}

// New creates a new Shell widget with the given options.
// If opts.WriteToScrollback is nil, scrollback is stored locally.
func New(opts ShellOptions) *Shell {
	s := &Shell{
		blocks:     []CommandBlock{},
		width:      80,
		height:     24,
		prompt:     "$ ",
		inputFocus: true,
		opts:       opts,
	}

	// Add initial welcome message as a pseudo-CommandBlock
	s.blocks = append(s.blocks, CommandBlock{
		Prompt: "",
		Input:  "",
		Output: *bytes.NewBufferString("Kabibi shell initialized. Type commands below."),
		Status: StatusSuccess,
	})

	return s
}

// Blocks returns a copy of the shell's command block history.
func (s *Shell) Blocks() []CommandBlock {
	res := make([]CommandBlock, len(s.blocks))
	copy(res, s.blocks)
	return res
}

// SetInput sets the input buffer and cursor position for testing/snapshot generation.
// This is a pragmatic helper for snapshot testing to avoid complex event construction.
func (s *Shell) SetInput(value string) {
	s.inputValue = value
	s.cursorPos = len([]rune(value))
}

// SetCursorPos sets the cursor position for testing/snapshot generation.
func (s *Shell) SetCursorPos(pos int) {
	if pos < 0 {
		s.cursorPos = 0
	} else if pos > len([]rune(s.inputValue)) {
		s.cursorPos = len([]rune(s.inputValue))
	} else {
		s.cursorPos = pos
	}
}

// SubmitCurrentInput adds the current input to history as a CommandBlock and clears the input buffer.
// This directly implements the "enter key" behavior for testing.
func (s *Shell) SubmitCurrentInput() {
	block := CommandBlock{
		Prompt:   s.prompt,
		Input:    s.inputValue,
		Output:   bytes.Buffer{},
		Status:   StatusActive,
		ExitCode: 0,
	}
	s.blocks = append(s.blocks, block)
	s.inputValue = ""
	s.cursorPos = 0
}

// Measure implements ui.Widget by reporting the shell's space requirements.
func (s *Shell) Measure(c ui.Constraints) ui.Size {
	w := c.MaxW
	if w <= 0 {
		w = 80
	}
	h := c.MaxH
	if h <= 0 {
		h = 24
	}
	return ui.Size{W: w, H: h}
}

// renderBlock converts a CommandBlock into a rendered CellBuf using FromANSI.
func renderBlock(block CommandBlock, width int) terminal.CellBuf {
	if width <= 0 {
		width = 80
	}
	promptInput := block.Prompt + block.Input
	lines := []string{promptInput}
	if block.Output.Len() > 0 {
		out := block.Output.String()
		out = strings.TrimSuffix(out, "\n")
		lines = append(lines, strings.Split(out, "\n")...)
	}
	buf := terminal.FromANSI(lines, width)
	if buf.Height == 0 {
		return terminal.NewCellBuf(width, 1)
	}
	return buf
}

// calculateVisualRows computes the total number of visual rows for a block at a given width.
func calculateVisualRows(block CommandBlock, width int) int {
	return renderBlock(block, width).Height
}

// totalVisualRows computes the cumulative height of all CommandBlocks in the history.
func (s *Shell) totalVisualRows() int {
	total := 0
	for _, block := range s.blocks {
		total += calculateVisualRows(block, s.width)
	}
	return total
}

// Render implements ui.Widget by painting the shell into a CellBuf.
// Implements the history ejection engine:
// 1. Calculate total visual rows and ejection target
// 2. Flush overflowing rows to WriteToScrollback callback
// 3. Render remaining active viewport to CellBuf
func (s *Shell) Render(rect ui.Rect, ctx ui.RenderContext) (ui.CellBuf, ui.CursorPos) {
	s.ctx = ctx
	s.width = rect.W
	s.height = rect.H

	// Allocate output buffer
	buf := ui.NewCellBuf(rect.W, rect.H)

	// ===== EJECTION ENGINE =====
	// Calculate total visual line height
	hTotal := s.totalVisualRows()

	// Calculate target ejection boundary: max(0, total_rows - viewport_height)
	hViewport := rect.H - 1 // Reserve 1 row for input prompt
	hTarget := hTotal - hViewport
	if hTarget < 0 {
		hTarget = 0
	}

	// If ejection boundary has advanced, flush delta to scrollback
	if hTarget > s.previouslyEjected && s.opts.WriteToScrollback != nil {
		// Render and emit the delta rows [previouslyEjected : hTarget]
		deltaRows := hTarget - s.previouslyEjected
		ejectionBuf := ui.NewCellBuf(rect.W, deltaRows)

		// Paint rows into ejection buffer
		currentRow := 0
		rowsSeen := 0
		for _, block := range s.blocks {
			blockRows := calculateVisualRows(block, rect.W)
			rowsEnd := rowsSeen + blockRows

			// Render this block's portion that falls within ejection range
			if rowsEnd > s.previouslyEjected && rowsSeen < hTarget {
				// Paint block into buffer
				currentRow = s.paintBlock(ejectionBuf, currentRow, block, rect.W,
					s.previouslyEjected-rowsSeen, hTarget-rowsSeen)
			}
			rowsSeen = rowsEnd
		}

		// Convert ejection buffer to ANSI and emit
		ansiOutput := terminal.ToANSI(ejectionBuf)
		s.opts.WriteToScrollback(ansiOutput + "\r\n")

		// Advance watermark
		s.previouslyEjected = hTarget
	}

	// ===== VIEWPORT RENDER =====
	// Paint viewport rows [previouslyEjected : previouslyEjected + hViewport]
	viewportStartRow := s.previouslyEjected
	viewportEndRow := s.previouslyEjected + hViewport

	bufferRow := 0
	rowsSeen := 0
	for _, block := range s.blocks {
		blockRows := calculateVisualRows(block, rect.W)
		rowsEnd := rowsSeen + blockRows

		if rowsEnd > viewportStartRow && rowsSeen < viewportEndRow {
			bufferRow = s.paintBlock(buf, bufferRow, block, rect.W,
				viewportStartRow-rowsSeen, viewportEndRow-rowsSeen)
		}
		rowsSeen = rowsEnd
	}

	// Render input line at bottom
	inputY := rect.H - 1
	inputLine := s.prompt + s.inputValue

	inputStyle := terminal.NewStyle(0, 0, terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault)
	col := 0
	for _, ch := range inputLine {
		if col >= rect.W {
			break
		}
		cell := terminal.Cell{
			Grapheme: string(ch),
			Style:    inputStyle,
		}
		buf.Set(col, inputY, cell)
		col++
	}

	// Cursor position: at prompt + cursorPos within input
	cursorX := len([]rune(s.prompt)) + s.cursorPos
	if cursorX > rect.W-1 {
		cursorX = rect.W - 1
	}

	cursor := ui.CursorPos{
		X:       cursorX,
		Y:       inputY,
		Visible: s.inputFocus,
	}

	return buf, cursor
}

// paintBlock renders a single CommandBlock into the buffer at the given starting row.
// Returns the final row written (for chaining to next block).
func (s *Shell) paintBlock(buf ui.CellBuf, startRow int, block CommandBlock, width, startOffset, endOffset int) int {
	blockBuf := renderBlock(block, width)
	if startOffset < 0 {
		startOffset = 0
	}
	if endOffset > blockBuf.Height {
		endOffset = blockBuf.Height
	}
	row := startRow
	for y := startOffset; y < endOffset && row < buf.Height; y++ {
		for x := 0; x < width && x < blockBuf.Width; x++ {
			buf.Set(x, row, blockBuf.Get(x, y))
		}
		row++
	}
	return row
}

func (s *Shell) invalidate() {
	if s.ctx.Invalidate != nil {
		s.ctx.Invalidate()
	}
}

// HandleEvent implements ui.Widget by processing input events.
// Handles keyboard input for the shell, mouse events, and window resizes.
func (s *Shell) HandleEvent(e input.Event) bool {
	switch evt := e.(type) {
	case input.KeyPressEvent:
		return s.handleKeyPress(evt.String())
	case *input.KeyPressEvent:
		if evt != nil {
			return s.handleKeyPress(evt.String())
		}
		return false

	case input.WindowSizeEvent:
		s.width = evt.Width
		s.height = evt.Height
		s.invalidate()
		return true
	case *input.WindowSizeEvent:
		if evt != nil {
			s.width = evt.Width
			s.height = evt.Height
			s.invalidate()
			return true
		}
		return false

	default:
		return false
	}
}

func (s *Shell) handleKeyPress(keyStr string) bool {
	if !s.inputFocus {
		return false
	}

	switch keyStr {
	case "enter":
		// Submit command
		if s.inputValue != "" || true { // Always create a block for interaction
			block := CommandBlock{
				Prompt:   s.prompt,
				Input:    s.inputValue,
				Output:   bytes.Buffer{},
				Status:   StatusActive,
				ExitCode: 0,
			}
			// Placeholder: add a response
			block.Output.WriteString("[Command not yet implemented in Step 1]")
			s.blocks = append(s.blocks, block)
			s.inputValue = ""
			s.cursorPos = 0
		}
		s.invalidate()
		return true

	case "backspace":
		// Delete character before cursor
		if s.cursorPos > 0 {
			runes := []rune(s.inputValue)
			runes = append(runes[:s.cursorPos-1], runes[s.cursorPos:]...)
			s.inputValue = string(runes)
			s.cursorPos--
			s.invalidate()
		}
		return true

	case "left":
		// Move cursor left
		if s.cursorPos > 0 {
			s.cursorPos--
			s.invalidate()
		}
		return true

	case "right":
		// Move cursor right
		if s.cursorPos < len([]rune(s.inputValue)) {
			s.cursorPos++
			s.invalidate()
		}
		return true

	case "home":
		// Move cursor to start
		if s.cursorPos != 0 {
			s.cursorPos = 0
			s.invalidate()
		}
		return true

	case "end":
		// Move cursor to end
		endPos := len([]rune(s.inputValue))
		if s.cursorPos != endPos {
			s.cursorPos = endPos
			s.invalidate()
		}
		return true

	case "space":
		// Insert space
		runes := []rune(s.inputValue)
		runes = append(runes[:s.cursorPos], append([]rune(" "), runes[s.cursorPos:]...)...)
		s.inputValue = string(runes)
		s.cursorPos++
		s.invalidate()
		return true

	default:
		// Insert printable character
		if len(keyStr) == 1 && keyStr[0] >= 32 && keyStr[0] < 127 {
			runes := []rune(s.inputValue)
			runes = append(runes[:s.cursorPos], append([]rune(keyStr), runes[s.cursorPos:]...)...)
			s.inputValue = string(runes)
			s.cursorPos++
			s.invalidate()
			return true
		}
	}
	return false
}

// AddLine adds output to the last CommandBlock, creating one if needed.
func (s *Shell) AddLine(line string) {
	if s != nil {
		if len(s.blocks) == 0 {
			s.blocks = append(s.blocks, CommandBlock{
				Prompt: s.prompt,
				Input:  "",
				Status: StatusSuccess,
			})
		}
		lastIdx := len(s.blocks) - 1
		s.blocks[lastIdx].Output.WriteString(line)
	}
}

// Verify Shell implements ui.Widget
var _ ui.Widget = (*Shell)(nil)
