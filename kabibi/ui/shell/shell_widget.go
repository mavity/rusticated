package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"ai"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// ShellMode controls whether the Shell widget operates in the dynamic Stack Widget
// model (growing from the 3-part banner + input + blank footer up to the terminal
// height before ejecting top rows into native scrollback) or full-viewport
// Plume History model (used when integrating with full-screen file panels).
type ShellMode int

const (
	ModeStack        ShellMode   = iota // Dynamic growing stack with slack-gap recovery & scrollback ejection (default)
	ModePlumeHistory                    // Full-screen viewport history for file panel integration
	ModeZeroPlume    = ModeStack        // Backward-compatible alias for the default inline stack mode
)

const reservedFooterRows = 1

var stackSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// formatStartupBanner constructs the pompous retro diagnostic startup banner block.
func formatStartupBanner(sessionErr error) string {
	ver := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		ver = info.Main.Version
	}

	lines := []string{
		"🐚 K A B I B I : wearable all-weather shell",
		"   v" + ver,
		"",
		"Preliminary version: be careful.",
	}
	if sessionErr != nil {
		lines = append(lines, "Shell initialization failed: "+sessionErr.Error())
	}
	return strings.Join(lines, "\n")
}

// ShellOptions configures shell behavior and callbacks
type ShellOptions struct {
	WriteToScrollback func(text string) // Callback to write lines to the host's scrollback
	OnExit            func()            // Optional callback invoked when user runs 'exit' or 'quit'
	OnCommandFinished func(cwd string)  // Optional callback invoked after a command or AI turn finishes
	WorkingDir        string            // Optional initial working directory for the shell runner
	AIRunner          AIRunner          // Optional AI fallback runner (defaults to LazyAIRunner)
	Mode              ShellMode         // Toggle between ModeStack (default) and ModePlumeHistory
	Prompt            string            // Optional custom prompt template or fixed prompt (supports PS1 escapes if execSess present)
}

// Shell is a dynamic stack shell widget that implements the ui.Widget interface.
// In ModeStack (default), it starts as a compact stack (BlockBanner + PromptInput +
// 1 reserved blank footer line), grows downward as commands/animations/input wrap,
// closes any bottom slack gap before ejecting, and ejects overflowing top rows
// into native terminal scrollback.
type Shell struct {
	mu sync.Mutex

	mode ShellMode

	// Unified append-only block store (BlockBanner, BlockCommand, BlockAITurn)
	blocks []CommandBlock

	// Ejection watermark: how many visual block rows have been flushed to native scrollback
	previouslyEjected int

	// Peak occupied stack height (in rows) observed at the current terminal width.
	// Used to compute bottom slack gap G = peakOccupiedHeight - desiredOccupiedHeight
	// so the stack pushes down to close any gap before ejecting upward.
	peakOccupiedHeight int

	// Transient status / animation state for the active block
	statusTextRaw string
	statusLine    string
	animFrame     int
	bannerFlushed bool
	exiting       bool
	bottomAligned bool // When true, non-ejected blocks are bottom-aligned above PromptInput inside full viewport height

	// Segregated prompt + command-line input widget
	input *PromptInput

	// Layout
	width          int
	height         int
	prompt         string
	promptTemplate string

	// Execution & AI Fallback
	execSess     *ExecutionSession
	sessionErr   error
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
	execSess, sessionErr := NewExecutionSession(opts.WorkingDir)
	aiRunner := opts.AIRunner
	if aiRunner == nil && execSess != nil {
		aiRunner = NewLazyAIRunner(execSess)
	}

	promptTmpl := opts.Prompt

	s := &Shell{
		mode:           opts.Mode,
		blocks:         []CommandBlock{},
		width:          80,
		height:         24,
		prompt:         "፧",
		promptTemplate: promptTmpl,
		activeBlock:    -1,
		execSess:       execSess,
		sessionErr:     sessionErr,
		aiRunner:       aiRunner,
		opts:           opts,
	}
	s.input = NewPromptInput(PromptInputOptions{
		Prompt: func() string {
			s.mu.Lock()
			p := s.prompt
			s.mu.Unlock()
			return p
		},
		StyleInput: s.styleInput,
	})
	s.refreshPrompt()
	if execSess != nil {
		execSess.SetAICommandUISink(s)
	}

	// Add initial pompous retro startup banner as BlockBanner (block 0)
	bannerText := formatStartupBanner(sessionErr)
	var bannerID int
	var cwd string
	if execSess != nil {
		cwd = execSess.Dir()
		rec := execSess.RecordBlock(BlockBanner, SourceSystem, "", bannerText, 0, time.Now())
		bannerID = rec.ID
	} else {
		bannerID = 1
	}
	s.blocks = append(s.blocks, CommandBlock{
		ID:        bannerID,
		Kind:      BlockBanner,
		Source:    SourceSystem,
		Cwd:       cwd,
		Prompt:    "",
		Input:     "",
		Output:    *bytes.NewBufferString(bannerText),
		Status:    StatusSuccess,
		StartedAt: time.Now(),
	})

	return s
}

// Mode returns the current ShellMode (ModeZeroPlume or ModePlumeHistory).
func (s *Shell) Mode() ShellMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// SetMode switches the shell between ModeStack and ModePlumeHistory.
func (s *Shell) SetMode(mode ShellMode) {
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	s.invalidate()
}

// SetBottomAligned configures whether the shell renders its non-ejected blocks
// bottom-aligned above PromptInput inside the full rect.H viewport (used when
// the blue file panels are active or have claimed the full-screen HUD).
func (s *Shell) SetBottomAligned(v bool) {
	s.mu.Lock()
	s.bottomAligned = v
	s.mu.Unlock()
	s.invalidate()
}

// BottomAligned reports whether the shell is currently bottom-aligned inside the full viewport.
func (s *Shell) BottomAligned() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bottomAligned
}

// SetOnCommandFinished sets or updates the callback invoked after a command or AI turn completes.
func (s *Shell) SetOnCommandFinished(fn func(cwd string)) {
	s.mu.Lock()
	s.opts.OnCommandFinished = fn
	s.mu.Unlock()
}

// SetDir updates the underlying ExecutionSession working directory and refreshes the prompt.
func (s *Shell) SetDir(dir string) error {
	s.mu.Lock()
	es := s.execSess
	s.mu.Unlock()
	if es == nil {
		return nil
	}
	if err := es.SetDir(dir); err != nil {
		return err
	}
	s.refreshPrompt()
	s.invalidate()
	return nil
}

// Dir returns the current shell working directory.
func (s *Shell) Dir() string {
	s.mu.Lock()
	es := s.execSess
	s.mu.Unlock()
	if es == nil {
		return ""
	}
	return es.Dir()
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
			ID:            s.blocks[i].ID,
			Kind:          s.blocks[i].Kind,
			Source:        s.blocks[i].Source,
			Cwd:           s.blocks[i].Cwd,
			Prompt:        s.blocks[i].Prompt,
			Input:         s.blocks[i].Input,
			Output:        *bytes.NewBuffer(append([]byte(nil), s.blocks[i].Output.Bytes()...)),
			AnimLine:      s.blocks[i].AnimLine,
			Status:        s.blocks[i].Status,
			ExitCode:      s.blocks[i].ExitCode,
			TypedAt:       s.blocks[i].TypedAt,
			StartedAt:     s.blocks[i].StartedAt,
			FirstOutputAt: s.blocks[i].FirstOutputAt,
			CompletedAt:   s.blocks[i].CompletedAt,
			Duration:      s.blocks[i].Duration,
		}
	}
	return res
}

// Input returns the embedded PromptInput widget.
func (s *Shell) Input() *PromptInput {
	return s.input
}

func (s *Shell) styleInput(value string) []terminal.Style {
	if s.execSess == nil {
		return nil
	}
	route, _ := s.execSess.ClassifyInput(value)
	switch route {
	case RouteShell, RouteBuiltin, RouteIncomplete:
		return bashInputStyles(value)
	default:
		return nil
	}
}

// SetInput sets the input buffer and cursor position for testing/snapshot generation.
// This is a pragmatic helper for snapshot testing to avoid complex event construction.
func (s *Shell) SetInput(value string) {
	s.input.SetValue(value)
}

// SetCursorPos sets the cursor position for testing/snapshot generation.
func (s *Shell) SetCursorPos(pos int) {
	s.input.SetCursorPos(pos)
}

// SetPrompt sets a static prompt string or custom PS1 template.
func (s *Shell) SetPrompt(prompt string) {
	s.mu.Lock()
	s.prompt = prompt
	s.promptTemplate = prompt
	s.mu.Unlock()
	s.invalidate()
}

// currentPromptLocked evaluates the current prompt string and visual display width.
func (s *Shell) currentPromptLocked() (string, int) {
	if s.execSess != nil {
		envMap := s.execSess.EnvMap()
		tmpl := s.promptTemplate
		if ps1, ok := envMap["PS1"]; ok && ps1 != "" {
			tmpl = ps1
		}
		if tmpl != "" {
			info := FormatPrompt(tmpl, s.execSess.Dir(), envMap)
			return info.Text, info.Width
		}
		// Default fallback when neither template nor PS1 is provided
		info := FormatPrompt(`\w፧`, s.execSess.Dir(), envMap)
		return info.Text, info.Width
	}

	p := s.prompt
	if p == "" {
		p = "፧"
	}
	return p, len([]rune(p))
}

// refreshPrompt evaluates shell environment and directory state outside the
// render path, then publishes a cached prompt for PromptInput to read.
func (s *Shell) refreshPrompt() {
	s.mu.Lock()
	es := s.execSess
	tmpl := s.promptTemplate
	prompt := s.prompt
	s.mu.Unlock()

	if es != nil {
		envMap := es.EnvMap()
		if ps1, ok := envMap["PS1"]; ok && ps1 != "" {
			tmpl = ps1
		}
		if tmpl == "" {
			tmpl = `\w፧`
		}
		prompt = FormatPrompt(tmpl, es.Dir(), envMap).Text
	} else if prompt == "" {
		prompt = "፧"
	}

	s.mu.Lock()
	s.prompt = prompt
	s.mu.Unlock()
}

// SubmitCurrentInput adds the current input to history as a CommandBlock and clears the input buffer.
func (s *Shell) SubmitCurrentInput() {
	rawInput := s.input.Value()
	typedAt := s.input.FirstInputAt()
	prompt := s.input.PromptText()
	s.input.AddHistory(rawInput)
	s.input.Reset()

	s.mu.Lock()
	var cwd string
	if s.execSess != nil {
		cwd = s.execSess.Dir()
	}
	startedAt := time.Now()
	if typedAt.IsZero() {
		typedAt = startedAt
	}
	block := CommandBlock{
		Kind:      BlockCommand,
		Source:    SourceUserShell,
		Cwd:       cwd,
		Prompt:    prompt,
		Input:     rawInput,
		Output:    bytes.Buffer{},
		Status:    StatusActive,
		ExitCode:  0,
		TypedAt:   typedAt,
		StartedAt: startedAt,
	}
	s.blocks = append(s.blocks, block)
	s.mu.Unlock()
}

// ExecuteLine submits and executes a line through the Shell's smart classifier
// (Shell first -> AI fallback) and waits for completion.
func (s *Shell) ExecuteLine(line string) {
	s.input.SetValue(line)
	s.submitAndExecuteAsync()
	s.WaitActive()
}

// renderBlockCached renders a CommandBlock into a CellBuf, reusing its cached
// CellBuf when width, prompt/input, output length, and animation line are unchanged.
func renderBlockCached(block *CommandBlock, width int) terminal.CellBuf {
	if width <= 0 {
		width = 80
	}
	outLen := block.Output.Len()
	if block.cachePopulated &&
		block.cachedWidth == width &&
		block.cachedPrompt == block.Prompt &&
		block.cachedInput == block.Input &&
		block.cachedOutLen == outLen &&
		block.cachedAnim == block.AnimLine &&
		block.cachedKind == block.Kind &&
		block.cachedSource == block.Source &&
		block.cachedStatus == block.Status {
		return block.cachedBuf
	}

	var rawText string
	if block.Kind == BlockBanner || (block.Prompt == "" && block.Input == "") {
		rawText = block.Output.String()
	} else {
		rawText = block.Prompt + block.Input + "\n" + block.Output.String()
	}
	if block.Status == StatusActive && block.AnimLine != "" {
		if rawText != "" && !strings.HasSuffix(rawText, "\n") {
			rawText += "\n"
		}
		rawText += block.AnimLine
	}

	var buf terminal.CellBuf
	if block.Source == SourceAITool && width > 4 {
		innerW := width - 2
		inner := terminal.FromANSIString(rawText, innerW)
		inner = trimTrailingWhitespaceRows(inner)
		if inner.Height == 0 {
			inner = terminal.NewCellBuf(innerW, 1)
		}
		buf = terminal.NewCellBuf(width, inner.Height)
		var stripeColor terminal.Color = 0x00ffff // cyan while active
		switch block.Status {
		case StatusSuccess:
			stripeColor = 0x00ff00 // green on exit 0
		case StatusError, StatusFailure:
			stripeColor = 0xff0000 // red on non-zero exit
		}
		stripeStyle := terminal.NewStyle(stripeColor, 0, terminal.CharResetBackgroundDefault)
		spaceStyle := terminal.NewStyle(0, 0, terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault)
		for y := 0; y < inner.Height; y++ {
			buf.Set(0, y, terminal.Cell{Grapheme: "┃", Style: stripeStyle})
			buf.Set(1, y, terminal.Cell{Grapheme: " ", Style: spaceStyle})
		}
		buf.Blit(inner, 2, 0)
	} else {
		buf = terminal.FromANSIString(rawText, width)
		if buf.Height == 0 {
			buf = terminal.NewCellBuf(width, 1)
		}
	}

	block.cachedWidth = width
	block.cachedPrompt = block.Prompt
	block.cachedInput = block.Input
	block.cachedOutLen = outLen
	block.cachedAnim = block.AnimLine
	block.cachedKind = block.Kind
	block.cachedSource = block.Source
	block.cachedStatus = block.Status
	block.cachedBuf = buf
	block.cachePopulated = true
	return buf
}

// OnAICommandStart implements AICommandUISink so commands executed by the AI's `exec`
// tool create a dedicated `SourceAITool` CommandBlock rendered with the `W - 2` left gutter stripe.
func (s *Shell) OnAICommandStart(command string, cwd string) (onChunk func([]byte), onFinish func(exitCode int, elapsed time.Duration)) {
	s.mu.Lock()
	startedAt := time.Now()
	parentIndex := -1
	if s.running && s.activeBlock >= 0 && s.activeBlock < len(s.blocks) &&
		s.blocks[s.activeBlock].Kind == BlockAITurn {
		parentIndex = s.activeBlock
	}
	block := CommandBlock{
		Kind:      BlockCommand,
		Source:    SourceAITool,
		Cwd:       cwd,
		Prompt:    "$ ",
		Input:     command,
		Output:    bytes.Buffer{},
		Status:    StatusActive,
		ExitCode:  0,
		StartedAt: startedAt,
	}
	if parentIndex >= 0 {
		block.aiTurnParentIndex = parentIndex
		block.hasAITurnParent = true
	}
	s.blocks = append(s.blocks, block)
	idx := len(s.blocks) - 1
	s.mu.Unlock()
	s.invalidate()

	onChunk = func(p []byte) {
		if len(p) == 0 {
			return
		}
		now := time.Now()
		s.mu.Lock()
		if idx >= 0 && idx < len(s.blocks) {
			if s.blocks[idx].FirstOutputAt.IsZero() {
				s.blocks[idx].FirstOutputAt = now
			}
			s.blocks[idx].Output.Write(p)
		}
		s.mu.Unlock()
		s.invalidate()
	}
	onFinish = func(exitCode int, elapsed time.Duration) {
		now := time.Now()
		s.mu.Lock()
		if idx >= 0 && idx < len(s.blocks) {
			st := StatusSuccess
			if exitCode != 0 {
				st = StatusError
			}
			s.blocks[idx].Status = st
			s.blocks[idx].ExitCode = exitCode
			s.blocks[idx].CompletedAt = now
			s.blocks[idx].Duration = elapsed
		}
		s.mu.Unlock()
		s.invalidate()
	}
	return onChunk, onFinish
}

// renderBlock converts a CommandBlock value into a rendered CellBuf using FromANSI.
func renderBlock(block CommandBlock, width int) terminal.CellBuf {
	return renderBlockCached(&block, width)
}

// calculateVisualRows computes the total number of visual rows for a block at a given width.
func calculateVisualRows(block CommandBlock, width int) int {
	return renderBlockCached(&block, width).Height
}

func (s *Shell) renderStackBlocksLocked(width int) []terminal.CellBuf {
	toolBlocks := make(map[int][]int)
	isToolBlock := make([]bool, len(s.blocks))
	for i := range s.blocks {
		block := &s.blocks[i]
		parent := block.aiTurnParentIndex
		if block.hasAITurnParent && parent >= 0 && parent < len(s.blocks) &&
			s.blocks[parent].Kind == BlockAITurn {
			toolBlocks[parent] = append(toolBlocks[parent], i)
			isToolBlock[i] = true
		}
	}

	rendered := make([]terminal.CellBuf, 0, len(s.blocks)+len(toolBlocks))
	for i := range s.blocks {
		if isToolBlock[i] {
			continue
		}
		block := &s.blocks[i]
		children := toolBlocks[i]
		if len(children) == 0 {
			rendered = append(rendered, renderBlockCached(block, width))
			continue
		}

		if block.Prompt != "" || block.Input != "" {
			promptBlock := CommandBlock{
				Kind:   block.Kind,
				Source: block.Source,
				Prompt: block.Prompt,
				Input:  block.Input,
				Status: block.Status,
			}
			rendered = append(rendered, renderBlockCached(&promptBlock, width))
		}
		for _, childIndex := range children {
			rendered = append(rendered, renderBlockCached(&s.blocks[childIndex], width))
		}
		if block.Output.Len() > 0 || block.AnimLine != "" {
			responseBlock := *block
			responseBlock.Prompt = ""
			responseBlock.Input = ""
			rendered = append(rendered, renderBlockCached(&responseBlock, width))
		}
	}
	return rendered
}

// computeStackHeight calculates the current occupied height of the stack
// (visible non-ejected block rows + visible input + 1 blank footer row) for Measure.
func (s *Shell) computeStackHeight(w, maxH int) int {
	if w <= 0 {
		w = 80
	}
	if maxH <= 0 {
		maxH = 24
	}

	s.mu.Lock()
	var chromeH int
	inputVisible := !s.running && !s.exiting
	if !s.exiting {
		s.mu.Unlock()
		if inputVisible {
			inputSize := s.input.Measure(ui.Constraints{MaxW: w, MaxH: maxH - reservedFooterRows})
			inputH := inputSize.H
			if inputH < 1 {
				inputH = 1
			}
			chromeH = inputH
		}
		chromeH += reservedFooterRows
		if chromeH > maxH {
			chromeH = maxH
		}
		s.mu.Lock()
	}
	totalBlockRows := 0
	for i := range s.blocks {
		totalBlockRows += renderBlockCached(&s.blocks[i], w).Height
	}
	previouslyEjected := s.previouslyEjected
	s.mu.Unlock()

	budget := maxH - chromeH
	if budget < 0 {
		budget = 0
	}

	unejected := totalBlockRows - previouslyEjected
	if unejected < 0 {
		unejected = 0
	}
	visBlockRows := unejected
	if visBlockRows > budget {
		visBlockRows = budget
	}
	occH := visBlockRows + chromeH
	if occH < 1 {
		occH = 1
	}
	if occH > maxH {
		occH = maxH
	}
	return occH
}

// Measure implements ui.Widget by reporting the shell's space requirements.
// In ModeStack (default), the shell reports its exact occupied stack height:
// visible non-ejected blocks + visible PromptInput + 1 reserved blank footer line,
// growing from (H_banner + 1 + 1) up to c.MaxH.
// In ModePlumeHistory, it requests the full available constraints.
func (s *Shell) Measure(c ui.Constraints) ui.Size {
	w := c.MaxW
	if w <= 0 {
		w = 80
	}
	maxH := c.MaxH
	if maxH <= 0 {
		maxH = 24
	}
	s.mu.Lock()
	mode := s.mode
	bottomAligned := s.bottomAligned
	s.mu.Unlock()
	if mode == ModeStack && !bottomAligned {
		occH := s.computeStackHeight(w, maxH)
		return ui.Size{W: w, H: occH}
	}
	return ui.Size{W: w, H: maxH}
}

// Render implements ui.Widget by painting the shell stack into a CellBuf.
// Uses bottom-up viewport fitting: pins the visible PromptInput + 1 reserved blank footer
// row at the bottom, walks backwards across s.blocks from newest to oldest to
// fill the remaining viewport budget, closes any bottom slack gap before ejecting,
// and ejects overflowing top rows via WriteToScrollback.
func (s *Shell) Render(rect ui.Rect, ctx ui.RenderContext) (ui.CellBuf, ui.CursorPos) {
	w := rect.W
	if w <= 0 {
		w = 80
	}
	maxH := rect.H
	if maxH <= 0 {
		maxH = 24
	}

	s.mu.Lock()
	widthChanged := s.width != w
	s.ctx = ctx
	s.width = w
	s.height = maxH
	if widthChanged {
		s.peakOccupiedHeight = 0
	}
	if s.execSess != nil {
		s.execSess.ResizePTY(w, maxH)
	}
	mode := s.mode
	bottomAligned := s.bottomAligned
	exiting := s.exiting
	running := s.running

	// Render and cache each block under the lock so cachedBuf is retained across frames.
	// AI-turn tool output is placed between the prompt and response/status rows.
	blockBufs := s.renderStackBlocksLocked(w)
	totalBlockRows := 0
	for _, b := range blockBufs {
		totalBlockRows += b.Height
	}
	previouslyEjected := s.previouslyEjected
	s.mu.Unlock()

	// 1. Pin Bottom Chrome: visible PromptInput + 1 blank reserved footer row (omitted when exiting)
	var inputBuf terminal.CellBuf
	var inputCursor ui.CursorPos
	var chromeH int
	if !exiting {
		if !running {
			inputMaxH := maxH - reservedFooterRows
			if inputMaxH < 1 {
				inputMaxH = 1
			}
			inputBuf, inputCursor = s.input.Render(ui.Rect{X: 0, Y: 0, W: w, H: inputMaxH}, ctx)
			chromeH = inputBuf.Height
		}
		footerH := reservedFooterRows
		if chromeH+footerH > maxH {
			footerH = maxH - chromeH
			if footerH < 0 {
				footerH = 0
			}
		}
		chromeH += footerH
	}

	// 2. Remaining Block Budget
	hBudget := maxH - chromeH
	if hBudget < 0 {
		hBudget = 0
	}

	// 3. Ejection Check (only when total non-ejected rows exceed the terminal's block budget,
	// meaning any bottom slack gap G has already been completely closed and we hit maxH)
	hTarget := totalBlockRows - hBudget
	if hTarget < 0 {
		hTarget = 0
	}
	if hTarget > previouslyEjected {
		if s.opts.WriteToScrollback != nil {
			deltaRows := hTarget - previouslyEjected
			ejectionBuf := ui.NewCellBuf(w, deltaRows)
			currentRow := 0
			rowsSeen := 0
			for _, bBuf := range blockBufs {
				blockRows := bBuf.Height
				rowsEnd := rowsSeen + blockRows
				if rowsEnd > previouslyEjected && rowsSeen < hTarget {
					currentRow = copyBlockRows(ejectionBuf, currentRow, bBuf, w,
						previouslyEjected-rowsSeen, hTarget-rowsSeen)
				}
				rowsSeen = rowsEnd
			}
			ansiOutput := terminal.ToANSI(ejectionBuf)
			s.opts.WriteToScrollback(ansiOutput + "\r\n")
		}
		s.mu.Lock()
		if hTarget > s.previouslyEjected {
			s.previouslyEjected = hTarget
		}
		s.mu.Unlock()
		previouslyEjected = hTarget
	}

	// 4. Bottom-Up Backward Walk across rendered block segments (newest to oldest)
	// after excluding previouslyEjected rows.
	type blockSlice struct {
		buf      terminal.CellBuf
		startRow int
		endRow   int
	}
	var collected []blockSlice
	remaining := hBudget
	rowsBeforeBlock := totalBlockRows
	for i := len(blockBufs) - 1; i >= 0 && remaining > 0; i-- {
		bBuf := blockBufs[i]
		rowsBeforeBlock -= bBuf.Height
		blockGlobalStart := rowsBeforeBlock
		blockGlobalEnd := blockGlobalStart + bBuf.Height
		if blockGlobalEnd <= previouslyEjected {
			break
		}
		localStart := 0
		if previouslyEjected > blockGlobalStart {
			localStart = previouslyEjected - blockGlobalStart
		}
		availRows := bBuf.Height - localStart
		if availRows <= 0 {
			continue
		}
		if availRows <= remaining {
			collected = append(collected, blockSlice{
				buf:      bBuf,
				startRow: localStart,
				endRow:   bBuf.Height,
			})
			remaining -= availRows
		} else {
			// Partially fits: take the tail `remaining` rows of this block
			collected = append(collected, blockSlice{
				buf:      bBuf,
				startRow: bBuf.Height - remaining,
				endRow:   bBuf.Height,
			})
			remaining = 0
			break
		}
	}

	collectedRows := hBudget - remaining
	occH := collectedRows + chromeH
	if occH < 1 {
		occH = 1
	}

	s.mu.Lock()
	if occH > s.peakOccupiedHeight {
		s.peakOccupiedHeight = occH
	}
	if s.peakOccupiedHeight > maxH {
		s.peakOccupiedHeight = maxH
	}
	s.mu.Unlock()

	bufH := occH
	if mode == ModePlumeHistory || bottomAligned {
		bufH = maxH
	}
	buf := ui.NewCellBuf(w, bufH)

	// Paint collected block slices in chronological order (reverse of backward walk).
	// When bottomAligned is true (HUD panels mode), non-ejected blocks sit immediately
	// above PromptInput at row (maxH - chromeH - collectedRows) so they climb upward
	// behind the blue panels before reaching row 0!
	bufferRow := 0
	if bottomAligned && !exiting {
		bufferRow = (maxH - chromeH) - collectedRows
		if bufferRow < 0 {
			bufferRow = 0
		}
	}
	for i := len(collected) - 1; i >= 0; i-- {
		sl := collected[i]
		bufferRow = copyBlockRows(buf, bufferRow, sl.buf, w, sl.startRow, sl.endRow)
	}

	// Position of PromptInput:
	// In compact ModeStack, PromptInput sits immediately below the collected blocks (at row collectedRows),
	// followed by the 1 reserved blank footer line at row (occH - 1).
	// In ModePlumeHistory or bottomAligned HUD mode, PromptInput + footer are pinned to the bottom of maxH.
	inputTopY := collectedRows
	if mode == ModePlumeHistory || (bottomAligned && !exiting) {
		inputTopY = maxH - chromeH
		if inputTopY < 0 {
			inputTopY = 0
		}
	}
	var cursor ui.CursorPos
	if !exiting && !running && inputBuf.Height > 0 {
		for y := 0; y < inputBuf.Height && inputTopY+y < buf.Height; y++ {
			for x := 0; x < w && x < inputBuf.Width; x++ {
				buf.Set(x, inputTopY+y, inputBuf.Get(x, y))
			}
		}
		cursor = ui.CursorPos{
			X:       inputCursor.X,
			Y:       inputTopY + inputCursor.Y,
			Visible: inputCursor.Visible,
		}
	} else if running && s.execSess != nil && s.execSess.HasActivePTY() && bufferRow > 0 {
		// When an interactive child command is running in a live PTY, place the cursor on the active row
		activeRow := bufferRow - 1
		if activeRow < 0 {
			activeRow = 0
		}
		cursorX := 0
		if activeRow < buf.Height {
			// Find rightmost non-blank column on the active line
			for col := w - 1; col >= 0; col-- {
				if g := buf.Get(col, activeRow).Grapheme; g != "" && g != " " {
					cursorX = col + 1
					break
				}
			}
			if cursorX >= w {
				cursorX = w - 1
			}
		}
		cursor = ui.CursorPos{
			X:       cursorX,
			Y:       activeRow,
			Visible: true,
		}
	} else {
		lastRow := collectedRows - 1
		if lastRow < 0 {
			lastRow = 0
		}
		cursor = ui.CursorPos{
			X:       0,
			Y:       lastRow,
			Visible: false,
		}
	}

	return buf, cursor
}

// copyBlockRows copies rows [startOffset, endOffset) from blockBuf into dst starting at startRow.
func copyBlockRows(dst ui.CellBuf, startRow int, blockBuf terminal.CellBuf, width, startOffset, endOffset int) int {
	if startOffset < 0 {
		startOffset = 0
	}
	if endOffset > blockBuf.Height {
		endOffset = blockBuf.Height
	}
	row := startRow
	for y := startOffset; y < endOffset && row < dst.Height; y++ {
		for x := 0; x < width && x < blockBuf.Width; x++ {
			dst.Set(x, row, blockBuf.Get(x, y))
		}
		row++
	}
	return row
}

// paintBlock renders a single CommandBlock into the buffer at the given starting row.
func (s *Shell) paintBlock(buf ui.CellBuf, startRow int, block CommandBlock, width, startOffset, endOffset int) int {
	blockBuf := renderBlockCached(&block, width)
	return copyBlockRows(buf, startRow, blockBuf, width, startOffset, endOffset)
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
		s.mu.Lock()
		running := s.running
		execSess := s.execSess
		s.mu.Unlock()
		if running {
			if execSess != nil && execSess.WriteActivePTY([]byte(string(evt))) {
				return true
			}
			return false
		}
		return s.input.HandleEvent(evt)
	case *input.PasteEvent:
		if evt != nil {
			s.mu.Lock()
			running := s.running
			execSess := s.execSess
			s.mu.Unlock()
			if running {
				if execSess != nil && execSess.WriteActivePTY([]byte(string(*evt))) {
					return true
				}
				return false
			}
			return s.input.HandleEvent(*evt)
		}
		return false

	case input.WindowSizeEvent:
		s.mu.Lock()
		s.width = evt.Width
		s.height = evt.Height
		execSess := s.execSess
		s.mu.Unlock()
		if execSess != nil {
			execSess.ResizePTY(evt.Width, evt.Height)
		}
		s.invalidate()
		return true
	case *input.WindowSizeEvent:
		if evt != nil {
			s.mu.Lock()
			s.width = evt.Width
			s.height = evt.Height
			execSess := s.execSess
			s.mu.Unlock()
			if execSess != nil {
				execSess.ResizePTY(evt.Width, evt.Height)
			}
			s.invalidate()
			return true
		}
		return false

	default:
		return false
	}
}

func encodeKeyForPTY(ev input.KeyPressEvent) []byte {
	// First check control character modifiers (ctrl+a through ctrl+z)
	s := strings.ToLower(ev.String())
	if strings.HasPrefix(s, "ctrl+") && len(s) == 6 {
		ch := s[5]
		if ch >= 'a' && ch <= 'z' {
			return []byte{ch - 'a' + 1}
		}
		switch ch {
		case '@', ' ':
			return []byte{0x00}
		case '[':
			return []byte{0x1b}
		case '\\':
			return []byte{0x1c}
		case ']':
			return []byte{0x1d}
		case '^':
			return []byte{0x1e}
		case '_':
			return []byte{0x1f}
		}
	}

	switch ev.Code {
	case input.KeyEnter:
		return []byte("\r")
	case input.KeyTab:
		return []byte("\t")
	case input.KeyBackspace:
		return []byte{0x7f}
	case input.KeyEscape:
		return []byte{0x1b}
	case input.KeyUp:
		return []byte("\x1b[A")
	case input.KeyDown:
		return []byte("\x1b[B")
	case input.KeyRight:
		return []byte("\x1b[C")
	case input.KeyLeft:
		return []byte("\x1b[D")
	case input.KeyHome:
		return []byte("\x1b[H")
	case input.KeyEnd:
		return []byte("\x1b[F")
	case input.KeyPgUp:
		return []byte("\x1b[5~")
	case input.KeyPgDown:
		return []byte("\x1b[6~")
	case input.KeyDelete:
		return []byte("\x1b[3~")
	case input.KeySpace:
		return []byte(" ")
	}
	if ev.Text != "" {
		return []byte(ev.Text)
	}
	raw := ev.String()
	if len(raw) == 1 {
		return []byte(raw)
	}
	return nil
}

func (s *Shell) handleKeyEvent(ev input.KeyPressEvent) bool {
	keyStr := ev.String()

	// If a command is actively running, prioritize forwarding keystrokes (including Ctrl+C) to the child PTY
	s.mu.Lock()
	running := s.running
	execSess := s.execSess
	s.mu.Unlock()
	if running {
		if execSess != nil && execSess.HasActivePTY() {
			if raw := encodeKeyForPTY(ev); len(raw) > 0 {
				return execSess.WriteActivePTY(raw)
			}
		}
		// If running without active PTY (e.g. AI turn), Ctrl+C cancels the active turn
		if keyStr == "ctrl+c" {
			s.mu.Lock()
			cancel := s.activeCancel
			s.mu.Unlock()
			if cancel != nil {
				cancel()
				return true
			}
		}
		return false
	}

	// When not running, Ctrl+C clears current input
	if keyStr == "ctrl+c" {
		if s.input.Value() != "" {
			s.input.Reset()
			s.invalidate()
			return true
		}
		return false
	}
	if !s.input.Focused() {
		return false
	}

	if keyStr == "enter" {
		if s.execSess != nil {
			if route, _ := s.execSess.ClassifyInput(s.input.Value()); route == RouteIncomplete {
				s.input.InsertNewline()
				s.invalidate()
				return true
			}
		}
		s.submitAndExecuteAsync()
		return true
	}

	if keyStr == "ctrl+d" && s.input.Value() == "" {
		s.input.SetValue("exit")
		s.submitAndExecuteAsync()
		return true
	}

	return s.input.HandleEvent(ev)
}

func (s *Shell) emitScrollbackLine(line string) {
	if s.opts.WriteToScrollback != nil {
		s.opts.WriteToScrollback(line + "\r\n")
	}
}

func (s *Shell) submitAndExecuteAsync() {
	rawInput := s.input.Value()
	typedAt := s.input.FirstInputAt()
	prompt := s.input.PromptText()
	s.input.AddHistory(rawInput)
	s.input.Reset()

	s.mu.Lock()
	trimmed := strings.TrimSpace(rawInput)
	var cwd string
	if s.execSess != nil {
		cwd = s.execSess.Dir()
	}
	startedAt := time.Now()
	if typedAt.IsZero() {
		typedAt = startedAt
	}
	block := CommandBlock{
		Kind:      BlockCommand,
		Source:    SourceUserShell,
		Cwd:       cwd,
		Prompt:    prompt,
		Input:     rawInput,
		Output:    bytes.Buffer{},
		Status:    StatusActive,
		ExitCode:  0,
		TypedAt:   typedAt,
		StartedAt: startedAt,
	}
	s.blocks = append(s.blocks, block)
	blockIdx := len(s.blocks) - 1
	s.statusTextRaw = ""
	s.statusLine = ""
	s.animFrame = 0

	if trimmed == "" {
		s.blocks[blockIdx].Status = StatusSuccess
		s.blocks[blockIdx].CompletedAt = startedAt
		s.mu.Unlock()
		s.invalidate()
		return
	}
	if s.execSess == nil {
		message := "Shell unavailable"
		if s.sessionErr != nil {
			message += ": " + s.sessionErr.Error()
		}
		fmt.Fprintln(&s.blocks[blockIdx].Output, message)
		s.blocks[blockIdx].Status = StatusFailure
		s.blocks[blockIdx].ExitCode = 1
		s.blocks[blockIdx].CompletedAt = time.Now()
		s.mu.Unlock()
		s.invalidate()
		return
	}

	route, parsed := s.execSess.ClassifyInput(rawInput)
	if route == RouteEmpty {
		s.blocks[blockIdx].Status = StatusSuccess
		s.blocks[blockIdx].CompletedAt = startedAt
		s.mu.Unlock()
		s.invalidate()
		return
	}
	if route == RouteBuiltin {
		s.blocks[blockIdx].Status = StatusSuccess
		s.blocks[blockIdx].CompletedAt = startedAt
		s.exiting = true
		onExit := s.opts.OnExit
		s.mu.Unlock()
		s.invalidate()
		if onExit != nil {
			onExit()
		}
		return
	}
	if route == RouteAI {
		s.blocks[blockIdx].Kind = BlockAITurn
	}

	ctx := ai.WithTurnTiming(context.Background(), ai.TurnTiming{
		TypedAt:     typedAt,
		SubmittedAt: startedAt,
	})
	ctx, cancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	s.activeCancel = cancel
	s.activeBlock = blockIdx
	s.running = true
	s.doneCh = doneCh
	aiRunner := s.aiRunner
	execSess := s.execSess
	s.mu.Unlock()

	s.invalidate()

	// Animated artifact ticker goroutine: drives smooth spinner updates while active
	animDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-animDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				if !s.running || s.activeBlock != blockIdx || blockIdx < 0 || blockIdx >= len(s.blocks) {
					s.mu.Unlock()
					return
				}
				s.animFrame = (s.animFrame + 1) % len(stackSpinnerFrames)
				frameGlyph := stackSpinnerFrames[s.animFrame]
				if s.statusLine != "" {
					s.blocks[blockIdx].AnimLine = frameGlyph + " " + s.statusLine
					s.mu.Unlock()
					s.invalidate()
				} else if s.blocks[blockIdx].Kind == BlockAITurn && s.blocks[blockIdx].Output.Len() == 0 {
					s.blocks[blockIdx].AnimLine = frameGlyph + " thinking…"
					s.mu.Unlock()
					s.invalidate()
				} else {
					s.mu.Unlock()
				}
			}
		}
	}()

	go func() {
		defer func() {
			close(animDone)
			cancel()
			s.mu.Lock()
			if blockIdx >= 0 && blockIdx < len(s.blocks) {
				s.blocks[blockIdx].AnimLine = ""
			}
			s.activeCancel = nil
			s.activeBlock = -1
			s.running = false
			s.statusTextRaw = ""
			s.statusLine = ""
			onDone := s.opts.OnCommandFinished
			var finalCwd string
			if s.execSess != nil {
				finalCwd = s.execSess.Dir()
			}
			close(doneCh)
			s.doneCh = nil
			s.mu.Unlock()
			s.refreshPrompt()
			if onDone != nil {
				onDone(finalCwd)
			}
			s.invalidate()
		}()

		appendBlockText := func(text string) {
			if text == "" {
				return
			}
			now := time.Now()
			s.mu.Lock()
			if blockIdx >= 0 && blockIdx < len(s.blocks) {
				if s.blocks[blockIdx].FirstOutputAt.IsZero() {
					s.blocks[blockIdx].FirstOutputAt = now
				}
				s.blocks[blockIdx].Output.WriteString(text)
			}
			s.mu.Unlock()
			s.invalidate()
		}

		if route == RouteShell {
			if trimmed == "help" {
				helpText := "Kabibi Plume Shell — valid bash commands run directly; natural language prompts fall back to AI.\n"
				appendBlockText(helpText)
				rec := execSess.RecordBlock(BlockCommand, SourceUserShell, rawInput, helpText, 0, startedAt)
				s.finishBlockWithRecord(blockIdx, rec.ID, BlockCommand, StatusSuccess, 0, startedAt)
				return
			}

			rec, err := execSess.RunCommand(ctx, rawInput, parsed, SourceUserShell, func(chunk []byte) {
				appendBlockText(string(chunk))
			})
			if err != nil {
				if errors.Is(err, context.Canceled) {
					appendBlockText("^C\n")
					s.finishBlockWithRecord(blockIdx, rec.ID, BlockCommand, StatusError, 130, startedAt)
					return
				}
				// If mvdan/sh reports that the command token at the very start of the
				// user's input line was not found, route the input seamlessly to AI.
				if errors.Is(err, ErrUnknownCommandAtStart) && aiRunner != nil {
					s.mu.Lock()
					s.blocks[blockIdx].Kind = BlockAITurn
					s.blocks[blockIdx].Output.Reset()
					s.mu.Unlock()
					s.runAIFallback(ctx, blockIdx, rawInput, aiRunner, execSess, startedAt, appendBlockText)
					return
				}
				s.finishBlockWithRecord(blockIdx, rec.ID, BlockCommand, StatusError, rec.ExitCode, startedAt)
				return
			}
			s.finishBlockWithRecord(blockIdx, rec.ID, BlockCommand, StatusSuccess, 0, startedAt)
			return
		}

		// RouteAI: execute as an AI chat prompt
		if aiRunner == nil {
			msg := "AI session unavailable.\n"
			appendBlockText(msg)
			rec := execSess.RecordBlock(BlockAITurn, SourceUserShell, rawInput, msg, 1, startedAt)
			s.finishBlockWithRecord(blockIdx, rec.ID, BlockAITurn, StatusFailure, 1, startedAt)
			return
		}
		s.runAIFallback(ctx, blockIdx, rawInput, aiRunner, execSess, startedAt, appendBlockText)
	}()
}

func (s *Shell) updateActiveArtifactFeedback(blockIdx int, feedback string) {
	if feedback == "" {
		return
	}
	s.mu.Lock()
	var raw strings.Builder
	raw.WriteString(s.statusTextRaw)
	for i := 0; i < len(feedback); i++ {
		ch := feedback[i]
		if ch == '\r' {
			raw.Reset()
		} else {
			raw.WriteByte(ch)
		}
	}
	s.statusTextRaw = raw.String()
	cur := strings.Join(strings.Fields(s.statusTextRaw), " ")
	s.statusLine = cur
	if blockIdx >= 0 && blockIdx < len(s.blocks) {
		if cur != "" {
			glyph := stackSpinnerFrames[s.animFrame%len(stackSpinnerFrames)]
			s.blocks[blockIdx].AnimLine = glyph + " " + cur
		} else {
			s.blocks[blockIdx].AnimLine = ""
		}
	}
	s.mu.Unlock()
	s.invalidate()
}

func (s *Shell) runAIFallback(
	ctx context.Context,
	blockIdx int,
	prompt string,
	aiRunner AIRunner,
	execSess *ExecutionSession,
	startedAt time.Time,
	appendBlockText func(string),
) {
	err := aiRunner.RunTurn(
		ctx,
		prompt,
		func(feedback string) {
			s.updateActiveArtifactFeedback(blockIdx, feedback)
		},
		func(content string) {
			s.mu.Lock()
			s.statusTextRaw = ""
			s.statusLine = ""
			if blockIdx >= 0 && blockIdx < len(s.blocks) {
				s.blocks[blockIdx].AnimLine = ""
			}
			s.mu.Unlock()
			appendBlockText(content)
		},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			appendBlockText("\n^C\n")
			var recID int
			if execSess != nil {
				s.mu.Lock()
				out := s.blocks[blockIdx].Output.String()
				typedAt := s.blocks[blockIdx].TypedAt
				firstOut := s.blocks[blockIdx].FirstOutputAt
				s.mu.Unlock()
				recID = execSess.RecordBlockWithTimings(BlockAITurn, SourceUserShell, prompt, out, 130, typedAt, startedAt, firstOut, time.Now()).ID
			}
			s.finishBlockWithRecord(blockIdx, recID, BlockAITurn, StatusError, 130, startedAt)
			return
		}
		errMsg := fmt.Sprintf("\n[ai error: %v]\n", err)
		appendBlockText(errMsg)
		var recID int
		if execSess != nil {
			s.mu.Lock()
			out := s.blocks[blockIdx].Output.String()
			typedAt := s.blocks[blockIdx].TypedAt
			firstOut := s.blocks[blockIdx].FirstOutputAt
			s.mu.Unlock()
			recID = execSess.RecordBlockWithTimings(BlockAITurn, SourceUserShell, prompt, out, 1, typedAt, startedAt, firstOut, time.Now()).ID
		}
		s.finishBlockWithRecord(blockIdx, recID, BlockAITurn, StatusFailure, 1, startedAt)
		return
	}
	var recID int
	if execSess != nil {
		s.mu.Lock()
		out := s.blocks[blockIdx].Output.String()
		typedAt := s.blocks[blockIdx].TypedAt
		firstOut := s.blocks[blockIdx].FirstOutputAt
		s.mu.Unlock()
		recID = execSess.RecordBlockWithTimings(BlockAITurn, SourceUserShell, prompt, out, 0, typedAt, startedAt, firstOut, time.Now()).ID
	}
	s.finishBlockWithRecord(blockIdx, recID, BlockAITurn, StatusSuccess, 0, startedAt)
}

func (s *Shell) finishBlockWithRecord(
	blockIdx int,
	id int,
	kind BlockKind,
	status CommandBlockStatus,
	exitCode int,
	startedAt time.Time,
) {
	now := time.Now()
	s.mu.Lock()
	if blockIdx >= 0 && blockIdx < len(s.blocks) {
		if id > 0 {
			s.blocks[blockIdx].ID = id
		}
		if kind != "" {
			s.blocks[blockIdx].Kind = kind
		}
		s.blocks[blockIdx].AnimLine = ""
		s.blocks[blockIdx].Status = status
		s.blocks[blockIdx].ExitCode = exitCode
		s.blocks[blockIdx].CompletedAt = now
		if !startedAt.IsZero() {
			s.blocks[blockIdx].Duration = now.Sub(startedAt)
		}
	}
	s.mu.Unlock()
}

func (s *Shell) finishBlock(blockIdx int, status CommandBlockStatus, exitCode int) {
	s.finishBlockWithRecord(blockIdx, 0, "", status, exitCode, time.Time{})
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
