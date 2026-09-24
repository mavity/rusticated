package shell

import (
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// ShellOptions configures shell behavior and callbacks
type ShellOptions struct {
	WriteToScrollback func(text string) // Callback to write lines to the host's scrollback
}

// Shell is a minimal shell widget that implements the ui.Widget interface.
// It displays a scrollback buffer and a command input line without any Bubble Tea dependencies.
type Shell struct {
	// Scrollback lines
	lines []string

	// Input buffer
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
	return &Shell{
		lines:      []string{"Kabibi shell initialized. Type commands below."},
		width:      80,
		height:     24,
		prompt:     "$ ",
		inputFocus: true,
		opts:       opts,
	}
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

// Render implements ui.Widget by painting the shell into a CellBuf.
// Layouts scrollback above and input prompt below.
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

	// Render scrollback (all rows except bottom 1)
	scrollbackHeight := rect.H - 1
	if scrollbackHeight > 0 {
		// Calculate which lines to display
		startLine := len(s.lines) - scrollbackHeight
		if startLine < 0 {
			startLine = 0
		}

		// Render lines into buffer
		displayRow := 0
		for lineIdx := startLine; lineIdx < len(s.lines) && displayRow < scrollbackHeight; lineIdx++ {
			line := s.lines[lineIdx]
			col := 0
			for _, ch := range line {
				if col >= rect.W {
					break
				}
				cell := terminal.Cell{
					R:     ch,
					Style: terminal.NewStyle(0, 0, 0),
				}
				buf.Set(col, displayRow, cell)
				col++
			}
			displayRow++
		}
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

	// Cursor position: at input line, after prompt + value
	cursorX := len([]rune(s.prompt)) + len([]rune(s.inputValue))
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
			if s.inputValue != "" {
				s.lines = append(s.lines, s.prompt+s.inputValue)
				s.lines = append(s.lines, "[Command not yet implemented in Step 1]")
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

// AddLine adds a line to the scrollback buffer.
func (s *Shell) AddLine(line string) {
	if s != nil {
		s.lines = append(s.lines, line)
		// Keep scrollback bounded
		if len(s.lines) > 1000 {
			s.lines = s.lines[len(s.lines)-1000:]
		}
	}
}

// Verify Shell implements ui.Widget
var _ ui.Widget = (*Shell)(nil)
