package shell

import (
	"bytes"

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

// calculateVisualRows computes the total number of wrapped lines for a block at a given width.
// Each CommandBlock's visual footprint is: 1 prompt line + wrapped input + wrapped output.
func calculateVisualRows(block CommandBlock, width int) int {
	if width <= 0 {
		width = 80
	}

	rows := 0

	// Prompt + Input line wrapping
	promptInput := block.Prompt + block.Input
	if promptInput == "" {
		rows = 1 // At least 1 row for empty line
	} else {
		// Count wrapped lines for prompt+input
		rows = (len([]rune(promptInput)) + width - 1) / width
		if rows == 0 {
			rows = 1
		}
	}

	// Output line wrapping
	outputStr := block.Output.String()
	if outputStr != "" {
		outputLines := 0
		for _, line := range bytes.Split(block.Output.Bytes(), []byte{'\n'}) {
			if len(line) > 0 {
				outputLines += (len([]rune(string(line))) + width - 1) / width
			} else {
				outputLines++ // Empty line still counts
			}
		}
		rows += outputLines
	}

	return rows
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
	s.width = rect.W
	s.height = rect.H

	// Allocate output buffer
	buf := ui.NewCellBuf(rect.W, rect.H)

	// Fill with default background
	fillRect := ui.Rect{X: 0, Y: 0, W: rect.W, H: rect.H}
	fillCell := terminal.Cell{
		R:     ' ',
		Style: terminal.NewStyle(0, 0, 0),
	}
	buf.Fill(fillRect, fillCell)

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
		ejectionBuf.Fill(ui.Rect{X: 0, Y: 0, W: rect.W, H: deltaRows}, fillCell)

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
		s.opts.WriteToScrollback(ansiOutput)

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

	col := 0
	for _, ch := range inputLine {
		if col >= rect.W {
			break
		}
		cell := terminal.Cell{
			R:     ch,
			Style: terminal.NewStyle(0, 0, 0),
		}
		buf.Set(col, inputY, cell)
		col++
	}

	// Fill rest of input line with spaces
	for ; col < rect.W; col++ {
		cell := terminal.Cell{
			R:     ' ',
			Style: terminal.NewStyle(0, 0, 0),
		}
		buf.Set(col, inputY, cell)
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
	row := startRow
	currentRowInBlock := 0
	rowsInBlock := calculateVisualRows(block, width)

	// Skip rows before startOffset
	if startOffset > 0 {
		currentRowInBlock = startOffset
		if currentRowInBlock >= rowsInBlock {
			return row
		}
	}

	// Paint prompt + input
	promptInput := block.Prompt + block.Input
	if promptInput != "" {
		lines := wrapText(promptInput, width)
		for i := currentRowInBlock; i < len(lines) && currentRowInBlock < endOffset; i++ {
			s.paintLine(buf, row, lines[i], width)
			row++
			currentRowInBlock++
		}
	} else if currentRowInBlock == 0 && currentRowInBlock < endOffset {
		row++
		currentRowInBlock++
	}

	// Paint output
	outputStr := block.Output.String()
	if outputStr != "" {
		outputLines := wrapText(outputStr, width)
		for i := 0; i < len(outputLines) && currentRowInBlock < endOffset; i++ {
			if currentRowInBlock >= startOffset {
				s.paintLine(buf, row, outputLines[i], width)
				row++
			}
			currentRowInBlock++
		}
	}

	return row
}

// paintLine draws a single text line into the buffer at the given row.
func (s *Shell) paintLine(buf ui.CellBuf, row int, line string, width int) {
	col := 0
	for _, ch := range line {
		if col >= width {
			break
		}
		cell := terminal.Cell{
			R:     ch,
			Style: terminal.NewStyle(0, 0, 0),
		}
		buf.Set(col, row, cell)
		col++
	}
	// Fill rest of line with spaces
	for ; col < width; col++ {
		cell := terminal.Cell{
			R:     ' ',
			Style: terminal.NewStyle(0, 0, 0),
		}
		buf.Set(col, row, cell)
	}
}

// wrapText wraps text to the given width, splitting on newlines and wrapping long lines.
func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}

	var result []string
	for _, line := range bytes.Split([]byte(text), []byte{'\n'}) {
		lineStr := string(line)
		runes := []rune(lineStr)

		for len(runes) > 0 {
			if len(runes) <= width {
				result = append(result, string(runes))
				break
			}
			result = append(result, string(runes[:width]))
			runes = runes[width:]
		}
	}

	return result
}

// HandleEvent implements ui.Widget by processing input events.
// Handles keyboard input for the shell, mouse events, and window resizes.
func (s *Shell) HandleEvent(e input.Event) bool {
	switch evt := e.(type) {
	case *input.KeyPressEvent:
		if !s.inputFocus {
			return false
		}

		keyStr := evt.String()
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
			return true

		case "backspace":
			// Delete character before cursor
			if s.cursorPos > 0 {
				runes := []rune(s.inputValue)
				runes = append(runes[:s.cursorPos-1], runes[s.cursorPos:]...)
				s.inputValue = string(runes)
				s.cursorPos--
			}
			return true

		case "left":
			// Move cursor left
			if s.cursorPos > 0 {
				s.cursorPos--
			}
			return true

		case "right":
			// Move cursor right
			if s.cursorPos < len([]rune(s.inputValue)) {
				s.cursorPos++
			}
			return true

		case "home":
			// Move cursor to start
			s.cursorPos = 0
			return true

		case "end":
			// Move cursor to end
			s.cursorPos = len([]rune(s.inputValue))
			return true

		default:
			// Insert printable character
			if len(keyStr) == 1 && keyStr[0] >= 32 && keyStr[0] < 127 {
				runes := []rune(s.inputValue)
				runes = append(runes[:s.cursorPos], append([]rune(keyStr), runes[s.cursorPos:]...)...)
				s.inputValue = string(runes)
				s.cursorPos++
				return true
			}
		}
		return false

	case *input.WindowSizeEvent:
		// Handle terminal resize
		s.width = evt.Width
		s.height = evt.Height
		return true

	default:
		return false
	}
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
