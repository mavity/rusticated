package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// ShellOptions configures shell behavior and callbacks
type ShellOptions struct {
	WriteToScrollback func(text string) // Callback to write lines to the host's scrollback
	OnExit            func()            // Optional callback invoked when user runs 'exit' or 'quit'
	WorkingDir        string            // Optional initial working directory for the shell runner
	AIRunner          AIRunner          // Optional AI fallback runner (defaults to LazyAIRunner)
}

// Shell is a shell widget that implements the ui.Widget interface.
// It manages a history of CommandBlocks (prompt+input+output) with automatic
// ejection of overflow rows to the native terminal scrollback buffer, executing
// valid shell commands via mvdan.cc/sh and routing syntax/command-not-found
// inputs to the integrated AI session.
type Shell struct {
	mu sync.Mutex

	// History model: structured CommandBlock entries
	blocks []CommandBlock

	// Ejection watermark: how many visual rows have been flushed to native scrollback
	previouslyEjected int

	// Input buffer (for currently active incomplete command)
	inputValue   string
	inputFocus   bool
	cursorPos    int
	inputHistory []string
	historyIdx   int

	// Layout
	width  int
	height int
	prompt string

	// Execution & AI Fallback
	execSess     *ExecutionSession
	aiRunner     AIRunner
	activeCancel context.CancelFunc
	activeBlock  int
	running      bool
	doneCh       chan struct{}

	// Options
	opts ShellOptions

	// Context from last render pass
	ctx ui.RenderContext
}

// New creates a new Shell widget with the given options.
// If opts.WriteToScrollback is nil, scrollback is stored locally.
func New(opts ShellOptions) *Shell {
	execSess, _ := NewExecutionSession(opts.WorkingDir)
	aiRunner := opts.AIRunner
	if aiRunner == nil && execSess != nil {
		aiRunner = NewLazyAIRunner(execSess)
	}

	s := &Shell{
		blocks:      []CommandBlock{},
		width:       80,
		height:      24,
		prompt:      "$ ",
		inputFocus:  true,
		historyIdx:  -1,
		activeBlock: -1,
		execSess:    execSess,
		aiRunner:    aiRunner,
		opts:        opts,
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

// ExecutionSession returns the underlying mvdan shell execution session.
func (s *Shell) ExecutionSession() *ExecutionSession {
	return s.execSess
}

// SetAIRunner overrides the AI fallback runner (useful for testing).
func (s *Shell) SetAIRunner(r AIRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aiRunner = r
}

// Close releases resources held by the shell and its AI session.
func (s *Shell) Close() error {
	s.mu.Lock()
	cancel := s.activeCancel
	aiRunner := s.aiRunner
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if aiRunner != nil {
		aiRunner.Close()
	}
	return nil
}

// WaitActive blocks until any currently executing command or AI turn finishes.
func (s *Shell) WaitActive() {
	s.mu.Lock()
	ch := s.doneCh
	s.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// Blocks returns a copy of the shell's command block history.
func (s *Shell) Blocks() []CommandBlock {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make([]CommandBlock, len(s.blocks))
	for i := range s.blocks {
		res[i] = CommandBlock{
			Prompt:   s.blocks[i].Prompt,
			Input:    s.blocks[i].Input,
			Output:   *bytes.NewBuffer(append([]byte(nil), s.blocks[i].Output.Bytes()...)),
			Status:   s.blocks[i].Status,
			ExitCode: s.blocks[i].ExitCode,
		}
	}
	return res
}

// SetInput sets the input buffer and cursor position for testing/snapshot generation.
// This is a pragmatic helper for snapshot testing to avoid complex event construction.
func (s *Shell) SetInput(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputValue = value
	s.cursorPos = len([]rune(value))
}

// SetCursorPos sets the cursor position for testing/snapshot generation.
func (s *Shell) SetCursorPos(pos int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pos < 0 {
		s.cursorPos = 0
	} else if pos > len([]rune(s.inputValue)) {
		s.cursorPos = len([]rune(s.inputValue))
	} else {
		s.cursorPos = pos
	}
}

// SubmitCurrentInput adds the current input to history as a CommandBlock and clears the input buffer.
// This directly implements the synchronous "enter key" block creation for snapshot testing.
func (s *Shell) SubmitCurrentInput() {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// ExecuteLine submits and executes a line through the Shell's smart classifier
// (Shell first -> AI fallback) and waits for completion.
func (s *Shell) ExecuteLine(line string) {
	s.mu.Lock()
	s.inputValue = line
	s.cursorPos = len([]rune(line))
	s.mu.Unlock()
	s.submitAndExecuteAsync()
	s.WaitActive()
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
		out = strings.ReplaceAll(out, "\r\n", "\n")
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

// Render implements ui.Widget by painting the shell into a CellBuf.
// Implements the history ejection engine:
// 1. Calculate total visual rows and ejection target
// 2. Flush overflowing rows to WriteToScrollback callback
// 3. Render remaining active viewport to CellBuf
func (s *Shell) Render(rect ui.Rect, ctx ui.RenderContext) (ui.CellBuf, ui.CursorPos) {
	s.mu.Lock()
	s.ctx = ctx
	s.width = rect.W
	s.height = rect.H

	blocksSnapshot := make([]CommandBlock, len(s.blocks))
	for i := range s.blocks {
		blocksSnapshot[i] = CommandBlock{
			Prompt:   s.blocks[i].Prompt,
			Input:    s.blocks[i].Input,
			Output:   *bytes.NewBuffer(append([]byte(nil), s.blocks[i].Output.Bytes()...)),
			Status:   s.blocks[i].Status,
			ExitCode: s.blocks[i].ExitCode,
		}
	}
	previouslyEjected := s.previouslyEjected
	prompt := s.prompt
	inputValue := s.inputValue
	cursorPos := s.cursorPos
	inputFocus := s.inputFocus && !s.running
	s.mu.Unlock()

	// Allocate output buffer
	buf := ui.NewCellBuf(rect.W, rect.H)

	// ===== EJECTION ENGINE =====
	// Calculate total visual line height
	hTotal := 0
	for _, block := range blocksSnapshot {
		hTotal += calculateVisualRows(block, rect.W)
	}

	// Calculate target ejection boundary: max(0, total_rows - viewport_height)
	hViewport := rect.H - 1 // Reserve 1 row for input prompt
	hTarget := hTotal - hViewport
	if hTarget < 0 {
		hTarget = 0
	}

	// If ejection boundary has advanced, flush delta to scrollback
	if hTarget > previouslyEjected && s.opts.WriteToScrollback != nil {
		// Render and emit the delta rows [previouslyEjected : hTarget]
		deltaRows := hTarget - previouslyEjected
		ejectionBuf := ui.NewCellBuf(rect.W, deltaRows)

		// Paint rows into ejection buffer
		currentRow := 0
		rowsSeen := 0
		for _, block := range blocksSnapshot {
			blockRows := calculateVisualRows(block, rect.W)
			rowsEnd := rowsSeen + blockRows

			// Render this block's portion that falls within ejection range
			if rowsEnd > previouslyEjected && rowsSeen < hTarget {
				currentRow = s.paintBlock(ejectionBuf, currentRow, block, rect.W,
					previouslyEjected-rowsSeen, hTarget-rowsSeen)
			}
			rowsSeen = rowsEnd
		}

		// Convert ejection buffer to ANSI and emit
		ansiOutput := terminal.ToANSI(ejectionBuf)
		s.opts.WriteToScrollback(ansiOutput + "\r\n")

		// Advance watermark
		s.mu.Lock()
		if hTarget > s.previouslyEjected {
			s.previouslyEjected = hTarget
		}
		s.mu.Unlock()
		previouslyEjected = hTarget
	}

	// ===== VIEWPORT RENDER =====
	// Paint viewport rows [previouslyEjected : previouslyEjected + hViewport]
	viewportStartRow := previouslyEjected
	viewportEndRow := previouslyEjected + hViewport

	bufferRow := 0
	rowsSeen := 0
	for _, block := range blocksSnapshot {
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
	inputLine := prompt + inputValue

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
	cursorX := len([]rune(prompt)) + cursorPos
	if cursorX > rect.W-1 {
		cursorX = rect.W - 1
	}

	cursor := ui.CursorPos{
		X:       cursorX,
		Y:       inputY,
		Visible: inputFocus,
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
	s.mu.Lock()
	inv := s.ctx.Invalidate
	s.mu.Unlock()
	if inv != nil {
		inv()
	}
}

// HandleEvent implements ui.Widget by processing input events.
// Handles keyboard input for the shell, paste events, mouse events, and window resizes.
func (s *Shell) HandleEvent(e input.Event) bool {
	switch evt := e.(type) {
	case input.KeyPressEvent:
		return s.handleKeyEvent(evt)
	case *input.KeyPressEvent:
		if evt != nil {
			return s.handleKeyEvent(*evt)
		}
		return false

	case input.PasteEvent:
		return s.insertText(string(evt))
	case *input.PasteEvent:
		if evt != nil {
			return s.insertText(string(*evt))
		}
		return false

	case input.WindowSizeEvent:
		s.mu.Lock()
		s.width = evt.Width
		s.height = evt.Height
		s.mu.Unlock()
		s.invalidate()
		return true
	case *input.WindowSizeEvent:
		if evt != nil {
			s.mu.Lock()
			s.width = evt.Width
			s.height = evt.Height
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		return false

	default:
		return false
	}
}

func (s *Shell) insertText(text string) bool {
	if text == "" {
		return false
	}
	// Strip newlines in single-line input mode
	clean := strings.ReplaceAll(strings.ReplaceAll(text, "\r", " "), "\n", " ")
	s.mu.Lock()
	if !s.inputFocus || s.running {
		s.mu.Unlock()
		return false
	}
	runes := []rune(s.inputValue)
	ins := []rune(clean)
	runes = append(runes[:s.cursorPos], append(ins, runes[s.cursorPos:]...)...)
	s.inputValue = string(runes)
	s.cursorPos += len(ins)
	s.mu.Unlock()
	s.invalidate()
	return true
}

func (s *Shell) handleKeyEvent(ev input.KeyPressEvent) bool {
	keyStr := ev.String()

	// Ctrl+C cancels an active command/AI turn or clears current input
	if keyStr == "ctrl+c" {
		s.mu.Lock()
		if s.running && s.activeCancel != nil {
			cancel := s.activeCancel
			s.mu.Unlock()
			cancel()
			return true
		}
		if s.inputValue != "" {
			s.inputValue = ""
			s.cursorPos = 0
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return false
	}

	s.mu.Lock()
	if !s.inputFocus || s.running {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	switch keyStr {
	case "enter":
		s.submitAndExecuteAsync()
		return true

	case "backspace":
		s.mu.Lock()
		if s.cursorPos > 0 {
			runes := []rune(s.inputValue)
			runes = append(runes[:s.cursorPos-1], runes[s.cursorPos:]...)
			s.inputValue = string(runes)
			s.cursorPos--
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "delete":
		s.mu.Lock()
		runes := []rune(s.inputValue)
		if s.cursorPos < len(runes) {
			runes = append(runes[:s.cursorPos], runes[s.cursorPos+1:]...)
			s.inputValue = string(runes)
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "left":
		s.mu.Lock()
		if s.cursorPos > 0 {
			s.cursorPos--
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "right":
		s.mu.Lock()
		if s.cursorPos < len([]rune(s.inputValue)) {
			s.cursorPos++
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "up":
		s.mu.Lock()
		if len(s.inputHistory) > 0 {
			if s.historyIdx < 0 {
				s.historyIdx = len(s.inputHistory) - 1
			} else if s.historyIdx > 0 {
				s.historyIdx--
			}
			s.inputValue = s.inputHistory[s.historyIdx]
			s.cursorPos = len([]rune(s.inputValue))
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "down":
		s.mu.Lock()
		if s.historyIdx >= 0 {
			if s.historyIdx < len(s.inputHistory)-1 {
				s.historyIdx++
				s.inputValue = s.inputHistory[s.historyIdx]
			} else {
				s.historyIdx = -1
				s.inputValue = ""
			}
			s.cursorPos = len([]rune(s.inputValue))
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "home", "ctrl+a":
		s.mu.Lock()
		if s.cursorPos != 0 {
			s.cursorPos = 0
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "end", "ctrl+e":
		s.mu.Lock()
		endPos := len([]rune(s.inputValue))
		if s.cursorPos != endPos {
			s.cursorPos = endPos
			s.mu.Unlock()
			s.invalidate()
			return true
		}
		s.mu.Unlock()
		return true

	case "space":
		return s.insertText(" ")

	default:
		if ev.Text != "" {
			return s.insertText(ev.Text)
		}
		if len(keyStr) == 1 && keyStr[0] >= 32 && keyStr[0] < 127 {
			return s.insertText(keyStr)
		}
	}
	return false
}

func (s *Shell) submitAndExecuteAsync() {
	s.mu.Lock()
	rawInput := s.inputValue
	trimmed := strings.TrimSpace(rawInput)
	block := CommandBlock{
		Prompt:   s.prompt,
		Input:    rawInput,
		Output:   bytes.Buffer{},
		Status:   StatusActive,
		ExitCode: 0,
	}
	s.blocks = append(s.blocks, block)
	blockIdx := len(s.blocks) - 1
	s.inputValue = ""
	s.cursorPos = 0
	s.historyIdx = -1
	if trimmed != "" {
		s.inputHistory = append(s.inputHistory, rawInput)
	}

	if trimmed == "" || s.execSess == nil {
		s.blocks[blockIdx].Status = StatusSuccess
		s.mu.Unlock()
		s.invalidate()
		return
	}

	route, parsed := s.execSess.ClassifyInput(rawInput)
	if route == RouteEmpty {
		s.blocks[blockIdx].Status = StatusSuccess
		s.mu.Unlock()
		s.invalidate()
		return
	}
	if route == RouteBuiltin {
		s.blocks[blockIdx].Status = StatusSuccess
		onExit := s.opts.OnExit
		s.mu.Unlock()
		s.invalidate()
		if onExit != nil {
			onExit()
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	s.activeCancel = cancel
	s.activeBlock = blockIdx
	s.running = true
	s.doneCh = doneCh
	aiRunner := s.aiRunner
	execSess := s.execSess
	s.mu.Unlock()

	s.invalidate()

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			s.activeCancel = nil
			s.activeBlock = -1
			s.running = false
			close(doneCh)
			s.doneCh = nil
			s.mu.Unlock()
			s.invalidate()
		}()

		appendBlockText := func(text string) {
			if text == "" {
				return
			}
			s.mu.Lock()
			if blockIdx >= 0 && blockIdx < len(s.blocks) {
				s.blocks[blockIdx].Output.WriteString(text)
			}
			s.mu.Unlock()
			s.invalidate()
		}

		if route == RouteShell {
			if trimmed == "help" {
				appendBlockText("Kabibi Plume Shell — valid bash commands run directly; natural language prompts fall back to AI.\n")
				s.finishBlock(blockIdx, StatusSuccess, 0)
				return
			}

			rec, err := execSess.RunCommand(ctx, rawInput, parsed, SourceUserShell, func(chunk []byte) {
				appendBlockText(string(chunk))
			})
			if err != nil {
				if errors.Is(err, context.Canceled) {
					appendBlockText("^C\n")
					s.finishBlock(blockIdx, StatusError, 130)
					return
				}
				// Stage B safety net: if a dynamic command failed solely because the
				// command executable was not found (exit code 127) and produced no stdout,
				// route it seamlessly to AI fallback.
				if rec.ExitCode == 127 && aiRunner != nil {
					s.mu.Lock()
					s.blocks[blockIdx].Output.Reset()
					s.mu.Unlock()
					s.runAIFallback(ctx, blockIdx, rawInput, aiRunner, appendBlockText)
					return
				}
				s.finishBlock(blockIdx, StatusError, rec.ExitCode)
				return
			}
			s.finishBlock(blockIdx, StatusSuccess, 0)
			return
		}

		// RouteAI: execute as an AI chat prompt in the plume
		if aiRunner == nil {
			appendBlockText("AI session unavailable.\n")
			s.finishBlock(blockIdx, StatusFailure, 1)
			return
		}
		s.runAIFallback(ctx, blockIdx, rawInput, aiRunner, appendBlockText)
	}()
}

func (s *Shell) runAIFallback(
	ctx context.Context,
	blockIdx int,
	prompt string,
	aiRunner AIRunner,
	appendBlockText func(string),
) {
	err := aiRunner.RunTurn(
		ctx,
		prompt,
		func(feedback string) {
			appendBlockText(feedback)
		},
		func(content string) {
			appendBlockText(content)
		},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			appendBlockText("\n^C\n")
			s.finishBlock(blockIdx, StatusError, 130)
			return
		}
		appendBlockText(fmt.Sprintf("\n[ai error: %v]\n", err))
		s.finishBlock(blockIdx, StatusFailure, 1)
		return
	}
	s.finishBlock(blockIdx, StatusSuccess, 0)
}

func (s *Shell) finishBlock(blockIdx int, status CommandBlockStatus, exitCode int) {
	s.mu.Lock()
	if blockIdx >= 0 && blockIdx < len(s.blocks) {
		s.blocks[blockIdx].Status = status
		s.blocks[blockIdx].ExitCode = exitCode
	}
	s.mu.Unlock()
}

// AddLine adds output to the last CommandBlock, creating one if needed.
func (s *Shell) AddLine(line string) {
	if s != nil {
		s.mu.Lock()
		if len(s.blocks) == 0 {
			s.blocks = append(s.blocks, CommandBlock{
				Prompt: s.prompt,
				Input:  "",
				Status: StatusSuccess,
			})
		}
		lastIdx := len(s.blocks) - 1
		s.blocks[lastIdx].Output.WriteString(line)
		s.mu.Unlock()
	}
}

// Verify Shell implements ui.Widget
var _ ui.Widget = (*Shell)(nil)
