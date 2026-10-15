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

// ShellMode controls whether the Shell widget operates in the lightweight
// Zero-Plume / Immediate Line Ejection model (1-row active prompt at the bottom
// of the screen, all completed output streamed directly to native scrollback)
// or the full multi-block Plume History model (used when integrating with
// full-screen file panels).
type ShellMode int

const (
	ModeZeroPlume    ShellMode = iota // Immediate line ejection, 1-line active prompt
	ModePlumeHistory                  // Multi-block scrollable history / file panel integration
)

// ShellOptions configures shell behavior and callbacks
type ShellOptions struct {
	WriteToScrollback func(text string) // Callback to write lines to the host's scrollback
	OnExit            func()            // Optional callback invoked when user runs 'exit' or 'quit'
	WorkingDir        string            // Optional initial working directory for the shell runner
	AIRunner          AIRunner          // Optional AI fallback runner (defaults to LazyAIRunner)
	Mode              ShellMode         // Toggle between ModeZeroPlume (default) and ModePlumeHistory
}

// Shell is a shell widget that implements the ui.Widget interface.
// In ModeZeroPlume (default), it renders a 1-row active prompt/status line and
// ejects all submitted commands and completed output lines immediately into
// native terminal scrollback. In ModePlumeHistory, it manages a history of
// CommandBlocks with viewport row ejection for composite TUI layouts.
type Shell struct {
	mu sync.Mutex

	mode ShellMode

	// History model (retained for ModePlumeHistory and block inspection)
	blocks []CommandBlock

	// Ejection watermark: how many visual rows have been flushed to native scrollback in ModePlumeHistory
	previouslyEjected int

	// Transient status line displayed in ModeZeroPlume while an AI turn or tool is active
	statusLine    string
	bannerFlushed bool
	exiting       bool

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
		mode:        opts.Mode,
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

// Mode returns the current ShellMode (ModeZeroPlume or ModePlumeHistory).
func (s *Shell) Mode() ShellMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// SetMode switches the shell between ModeZeroPlume and ModePlumeHistory.
func (s *Shell) SetMode(mode ShellMode) {
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	s.invalidate()
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

// IsRunning reports whether a foreground shell command or AI turn is currently active.
func (s *Shell) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
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
// In ModeZeroPlume, it also immediately ejects the submitted prompt line to WriteToScrollback.
func (s *Shell) SubmitCurrentInput() {
	s.mu.Lock()
	rawInput := s.inputValue
	prompt := s.prompt
	mode := s.mode
	writeSB := s.opts.WriteToScrollback
	block := CommandBlock{
		Prompt:   prompt,
		Input:    rawInput,
		Output:   bytes.Buffer{},
		Status:   StatusActive,
		ExitCode: 0,
	}
	s.blocks = append(s.blocks, block)
	s.inputValue = ""
	s.cursorPos = 0
	s.mu.Unlock()

	if mode == ModeZeroPlume && writeSB != nil {
		writeSB(prompt + rawInput + "\r\n")
	}
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
// In ModeZeroPlume, the shell widget occupies a fixed 1-row height for the active prompt/status line.
// In ModePlumeHistory, it requests the full available constraints.
func (s *Shell) Measure(c ui.Constraints) ui.Size {
	w := c.MaxW
	if w <= 0 {
		w = 80
	}
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	if mode == ModeZeroPlume {
		return ui.Size{W: w, H: 1}
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
// In ModeZeroPlume, Render returns a 1-row CellBuf containing the active prompt
// (or live AI/tool status line while running).
// In ModePlumeHistory, Render paints the multi-block history viewport and ejects
// overflowing rows via WriteToScrollback.
func (s *Shell) Render(rect ui.Rect, ctx ui.RenderContext) (ui.CellBuf, ui.CursorPos) {
	s.mu.Lock()
	s.ctx = ctx
	s.width = rect.W
	s.height = rect.H
	mode := s.mode

	if mode == ModeZeroPlume {
		needBanner := !s.bannerFlushed && s.opts.WriteToScrollback != nil && len(s.blocks) > 0 && s.blocks[0].Input == "" && s.blocks[0].Output.Len() > 0
		var bannerText string
		if needBanner {
			s.bannerFlushed = true
			bannerText = s.blocks[0].Output.String()
		}
		prompt := s.prompt
		inputValue := s.inputValue
		cursorPos := s.cursorPos
		running := s.running
		exiting := s.exiting
		statusLine := s.statusLine
		inputFocus := s.inputFocus && !running && !exiting
		s.mu.Unlock()

		if needBanner {
			s.emitScrollbackLine(strings.TrimSuffix(bannerText, "\n"))
		}

		w := rect.W
		if w <= 0 {
			w = 80
		}
		buf := ui.NewCellBuf(w, 1)
		inputStyle := terminal.NewStyle(0, 0, terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault)

		displayLine := prompt + inputValue
		if exiting {
			displayLine = ""
		} else if running {
			displayLine = statusLine
		}

		col := 0
		for _, ch := range displayLine {
			if col >= w {
				break
			}
			buf.Set(col, 0, terminal.Cell{
				Grapheme: string(ch),
				Style:    inputStyle,
			})
			col++
		}

		cursorX := len([]rune(prompt)) + cursorPos
		if cursorX > w-1 {
			cursorX = w - 1
		}
		if cursorX < 0 {
			cursorX = 0
		}

		return buf, ui.CursorPos{
			X:       cursorX,
			Y:       0,
			Visible: inputFocus,
		}
	}

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

func (s *Shell) emitScrollbackLine(line string) {
	if s.opts.WriteToScrollback != nil {
		s.opts.WriteToScrollback(line + "\r\n")
	}
}

func (s *Shell) submitAndExecuteAsync() {
	s.mu.Lock()
	rawInput := s.inputValue
	prompt := s.prompt
	mode := s.mode
	trimmed := strings.TrimSpace(rawInput)
	block := CommandBlock{
		Prompt:   prompt,
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
	s.statusLine = ""
	if trimmed != "" {
		s.inputHistory = append(s.inputHistory, rawInput)
	}

	if trimmed == "" || s.execSess == nil {
		s.blocks[blockIdx].Status = StatusSuccess
		s.mu.Unlock()
		if mode == ModeZeroPlume {
			s.emitScrollbackLine(prompt + rawInput)
		}
		s.invalidate()
		return
	}

	route, parsed := s.execSess.ClassifyInput(rawInput)
	if route == RouteEmpty {
		s.blocks[blockIdx].Status = StatusSuccess
		s.mu.Unlock()
		if mode == ModeZeroPlume {
			s.emitScrollbackLine(prompt + rawInput)
		}
		s.invalidate()
		return
	}
	if route == RouteBuiltin {
		s.blocks[blockIdx].Status = StatusSuccess
		s.exiting = true
		onExit := s.opts.OnExit
		s.mu.Unlock()
		if mode == ModeZeroPlume {
			s.emitScrollbackLine(prompt + rawInput)
		}
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

	// Phase 2.1: Instant Command/Prompt Ejection on Enter in ModeZeroPlume
	if mode == ModeZeroPlume {
		s.emitScrollbackLine(prompt + rawInput)
	}
	s.invalidate()

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			s.activeCancel = nil
			s.activeBlock = -1
			s.running = false
			s.statusLine = ""
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

		// Line-buffered stream helper for Zero-Plume mode
		var linePartial strings.Builder
		var lineMu sync.Mutex
		streamZeroPlumeLines := func(chunk string) {
			if chunk == "" {
				return
			}
			lineMu.Lock()
			linePartial.WriteString(chunk)
			bufStr := linePartial.String()
			lastNL := strings.LastIndexByte(bufStr, '\n')
			if lastNL < 0 {
				lineMu.Unlock()
				return
			}
			completed := bufStr[:lastNL]
			rest := bufStr[lastNL+1:]
			linePartial.Reset()
			linePartial.WriteString(rest)
			lineMu.Unlock()

			completed = strings.ReplaceAll(completed, "\r\n", "\n")
			for _, ln := range strings.Split(completed, "\n") {
				s.emitScrollbackLine(ln)
			}
		}
		flushZeroPlumeLines := func() {
			lineMu.Lock()
			rem := linePartial.String()
			linePartial.Reset()
			lineMu.Unlock()
			if rem != "" {
				rem = strings.TrimRight(rem, "\r\n")
				s.emitScrollbackLine(rem)
			}
		}

		emitOutputChunk := func(chunk string) {
			appendBlockText(chunk)
			if mode == ModeZeroPlume {
				streamZeroPlumeLines(chunk)
			}
		}

		if route == RouteShell {
			if trimmed == "help" {
				emitOutputChunk("Kabibi Plume Shell — valid bash commands run directly; natural language prompts fall back to AI.\n")
				if mode == ModeZeroPlume {
					flushZeroPlumeLines()
				}
				s.finishBlock(blockIdx, StatusSuccess, 0)
				return
			}

			rec, err := execSess.RunCommand(ctx, rawInput, parsed, SourceUserShell, func(chunk []byte) {
				emitOutputChunk(string(chunk))
			})
			if err != nil {
				if errors.Is(err, context.Canceled) {
					emitOutputChunk("^C\n")
					if mode == ModeZeroPlume {
						flushZeroPlumeLines()
					}
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
					lineMu.Lock()
					linePartial.Reset()
					lineMu.Unlock()
					s.runAIFallback(ctx, blockIdx, mode, rawInput, aiRunner, appendBlockText, streamZeroPlumeLines, flushZeroPlumeLines)
					return
				}
				if mode == ModeZeroPlume {
					flushZeroPlumeLines()
				}
				s.finishBlock(blockIdx, StatusError, rec.ExitCode)
				return
			}
			if mode == ModeZeroPlume {
				flushZeroPlumeLines()
			}
			s.finishBlock(blockIdx, StatusSuccess, 0)
			return
		}

		// RouteAI: execute as an AI chat prompt
		if aiRunner == nil {
			emitOutputChunk("AI session unavailable.\n")
			if mode == ModeZeroPlume {
				flushZeroPlumeLines()
			}
			s.finishBlock(blockIdx, StatusFailure, 1)
			return
		}
		s.runAIFallback(ctx, blockIdx, mode, rawInput, aiRunner, appendBlockText, streamZeroPlumeLines, flushZeroPlumeLines)
	}()
}

func (s *Shell) updateZeroPlumeFeedback(feedback string) {
	if feedback == "" {
		return
	}
	s.mu.Lock()
	cur := s.statusLine
	for i := 0; i < len(feedback); i++ {
		ch := feedback[i]
		switch ch {
		case '\r':
			cur = ""
		case '\n':
			trimmed := strings.TrimSpace(cur)
			if trimmed != "" && s.opts.WriteToScrollback != nil {
				lineToEmit := cur
				s.mu.Unlock()
				s.emitScrollbackLine(lineToEmit)
				s.mu.Lock()
			}
			cur = ""
		default:
			cur += string(ch)
		}
	}
	if strings.TrimSpace(cur) == "" {
		cur = ""
	}
	s.statusLine = cur
	s.mu.Unlock()
	s.invalidate()
}

func (s *Shell) runAIFallback(
	ctx context.Context,
	blockIdx int,
	mode ShellMode,
	prompt string,
	aiRunner AIRunner,
	appendBlockText func(string),
	streamZeroPlumeLines func(string),
	flushZeroPlumeLines func(),
) {
	err := aiRunner.RunTurn(
		ctx,
		prompt,
		func(feedback string) {
			appendBlockText(feedback)
			if mode == ModeZeroPlume {
				s.updateZeroPlumeFeedback(feedback)
			}
		},
		func(content string) {
			appendBlockText(content)
			if mode == ModeZeroPlume {
				s.mu.Lock()
				s.statusLine = ""
				s.mu.Unlock()
				streamZeroPlumeLines(content)
			}
		},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			appendBlockText("\n^C\n")
			if mode == ModeZeroPlume {
				streamZeroPlumeLines("\n^C\n")
				flushZeroPlumeLines()
			}
			s.finishBlock(blockIdx, StatusError, 130)
			return
		}
		errMsg := fmt.Sprintf("\n[ai error: %v]\n", err)
		appendBlockText(errMsg)
		if mode == ModeZeroPlume {
			streamZeroPlumeLines(errMsg)
			flushZeroPlumeLines()
		}
		s.finishBlock(blockIdx, StatusFailure, 1)
		return
	}
	if mode == ModeZeroPlume {
		flushZeroPlumeLines()
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
