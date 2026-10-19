package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/input"
	"github.com/charmbracelet/x/term"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// hostMsgKind represents the variant of a host coordination message.
type hostMsgKind int

const (
	hostMsgInput hostMsgKind = iota
	hostMsgInvalidate
	hostMsgScrollback
	hostMsgResize
	hostMsgStop
)

// hostMsg is a compound message passed to the Host's coordinator loop.
type hostMsg struct {
	kind       hostMsgKind
	inputEvent input.Event
	scrollback string
}

// Host owns the terminal I/O loop, double-buffered frame engine, and widget tree.
//
// Thread safety: Invalidate may be called from any goroutine. All I/O is
// serialised through the central event loop goroutine via msgCh.
type Host struct {
	root Widget

	// Double-buffered frames. currBuf holds the frame being built; prevBuf holds
	// the frame currently visible on screen. SwapBufCells exchanges their backing
	// arrays in O(1) after each flush, recycling both allocations indefinitely.
	currBuf terminal.CellBuf
	prevBuf terminal.CellBuf

	dirty atomic.Bool
	mu    sync.Mutex

	out *bufio.Writer

	// Logical cursor position returned by the focused widget tree.
	cursorX, cursorY                   int
	cursorVisible                      bool
	prevCursorX, prevCursorY           int
	prevCursorVisible, hasRenderedOnce bool
	allocHeight                        int // Peak allocated height of the active CellBuf region (1 .. ht)

	scrollbackMu    sync.Mutex
	scrollbackQueue []string

	msgCh         chan hostMsg
	stopCh        chan struct{}
	prevTermState *term.State // holds the saved terminal state needed to restore cooked mode on exit.
}

// handleSignals installs platform-specific signal handlers.
// On Unix platforms, this sets up SIGWINCH (window resize) and SIGTERM (graceful stop).
// On Windows, this is a no-op; the console host manages resize events.
// Implementation is delegated to platform-specific files via initSignalHandlers.
func (h *Host) handleSignals() (cleanup func()) {
	return initSignalHandlers(h)
}

// NewHost creates a Host bound to the given root widget. Call Run to start the event loop.
func NewHost(root Widget) *Host {
	return &Host{
		root:   root,
		out:    bufio.NewWriterSize(os.Stdout, 64*1024),
		msgCh:  make(chan hostMsg, 256),
		stopCh: make(chan struct{}),
	}
}

// Invalidate requests a redraw. Concurrent calls are coalesced: if a frame is
// already pending, the call returns immediately without spawning additional work.
func (h *Host) Invalidate() {
	h.invalidateCore(term.GetSize)
}

func (h *Host) invalidateCore(
	_ func(fd uintptr) (w int, ht int, err error),
) {
	if h.dirty.Swap(true) {
		return // already scheduled; coalesced
	}
	h.sendMsg(hostMsg{kind: hostMsgInvalidate})
}

// Run enters terminal raw mode, delivers an initial frame, and blocks on the
// input loop until Stop is called or stdin closes.
func (h *Host) Run() error {
	return h.runCore(os.Stdin, os.Stdout, term.MakeRaw, term.Restore, term.GetSize)
}

// RunWithIO runs the full Host event loop against custom input/output streams,
// raw-mode hooks, and size query function. This allows deterministic end-to-end
// TUI tests to drive the exact same Host loop as production without mocking widgets.
func (h *Host) RunWithIO(
	in io.Reader,
	out io.Writer,
	makeRawFunc func(uintptr) (*term.State, error),
	restoreFunc func(uintptr, *term.State) error,
	getSizeFunc func(fd uintptr) (w, ht int, err error),
) error {
	return h.runCore(in, out, makeRawFunc, restoreFunc, getSizeFunc)
}

func (h *Host) runCore(
	in io.Reader,
	out io.Writer,
	makeRawFunc func(uintptr) (*term.State, error),
	restoreFunc func(uintptr, *term.State) error,
	getSizeFunc func(fd uintptr) (w, ht int, err error),
) (err error) {
	if in == nil {
		in = os.Stdin
	}
	if out != nil {
		h.out = bufio.NewWriterSize(out, 64*1024)
	} else if h.out == nil {
		h.out = bufio.NewWriterSize(os.Stdout, 64*1024)
	}
	if getSizeFunc == nil {
		getSizeFunc = term.GetSize
	}

	// enterRawMode puts stdin into raw mode. Mouse tracking stays disabled so
	// the terminal handles normal mouse and scrollback behavior itself.
	// Uses golang.org/x/term.MakeRaw for cross-platform compatibility across
	// Linux, macOS, BSD, and Windows.
	state := &term.State{}

	var inputReader *input.Reader

	defer func() {
		if inputReader != nil {
			_ = inputReader.Cancel()
			_ = inputReader.Close()
		}
		if h.hasRenderedOnce {
			// Move to the start of the line below the last occupied row (h.prevCursorY on exit),
			// clear any blank footer/slack rows below it, reset SGR color, and show the cursor.
			h.out.WriteString("\x1b[0m\r\n\x1b[2K\x1b[J\x1b[?25h")
		}
		h.out.WriteString("\x1b[?1006l\x1b[?1000l")
		h.out.Flush()
		if restoreFunc != nil && state != nil {
			_ = restoreFunc(os.Stdin.Fd(), state)
		}
		h.prevTermState = nil
		if r := recover(); r != nil {
			panic(r)
		}
	}()

	if makeRawFunc != nil {
		state, err = makeRawFunc(os.Stdin.Fd())
		if err != nil {
			state = nil
			return err
		}
	}
	h.prevTermState = state
	h.out.WriteString("\x1b[?1006l\x1b[?1000l")
	h.out.Flush()

	// exitRawMode is already deferred; recover catches widget panics and re-panics
	// after the terminal has been restored to cooked mode.
	defer func() {
		if r := recover(); r != nil {
			panic(r)
		}
	}()

	// Ctrl+C on all platforms → graceful stop.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			h.Stop()
		case <-h.stopCh:
		}
	}()

	// Platform-specific signals (SIGTERM on Unix, SIGWINCH resize).
	cleanupSignals := h.handleSignals()
	defer cleanupSignals()

	// Initial render
	h.Invalidate()

	r, err := input.NewReader(in, "", 0)
	if err == nil {
		inputReader = r
	}

	// Start reading input in background producer goroutine
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		if inputReader != nil {
			h.readInputLoopWithReader(inputReader)
		}
	}()

	// Run central event coordinator loop on this main host goroutine
	h.processLoop(getSizeFunc)

	// Cancel and close the input reader so readInputLoop unblocks immediately when Stop() is called
	if inputReader != nil {
		_ = inputReader.Cancel()
		_ = inputReader.Close()
		inputReader = nil
	}
	return nil
}

// processLoop is the single coordinator loop that owns all access to h.root.
func (h *Host) processLoop(getSizeFunc func(fd uintptr) (w, ht int, err error)) {
	for {
		select {
		case msg, ok := <-h.msgCh:
			if !ok {
				return
			}
			if msg.kind == hostMsgStop {
				return
			}

			needsRender := false
			h.handleHostMsg(msg, &needsRender, getSizeFunc)

			// Drain any immediately pending messages to coalesce redraws
			for {
				select {
				case nextMsg, ok := <-h.msgCh:
					if !ok || nextMsg.kind == hostMsgStop {
						if needsRender {
							h.runFrame(getSizeFunc)
						}
						return
					}
					h.handleHostMsg(nextMsg, &needsRender, getSizeFunc)
				default:
					goto drained
				}
			}

		drained:
			if needsRender {
				h.runFrame(getSizeFunc)
			}
		case <-h.stopCh:
			// Drain any final messages queued just before Stop() was invoked
			needsRender := false
			for {
				select {
				case msg, ok := <-h.msgCh:
					if !ok || msg.kind == hostMsgStop {
						if needsRender {
							h.runFrame(getSizeFunc)
						}
						return
					}
					h.handleHostMsg(msg, &needsRender, getSizeFunc)
				default:
					if needsRender {
						h.runFrame(getSizeFunc)
					}
					return
				}
			}
		}
	}
}

func (h *Host) handleHostMsg(
	msg hostMsg,
	needsRender *bool,
	getSizeFunc func(fd uintptr) (w, ht int, err error),
) {
	switch msg.kind {
	case hostMsgInput:
		if h.root.HandleEvent(msg.inputEvent) {
			*needsRender = true
		}
	case hostMsgInvalidate:
		*needsRender = true
	case hostMsgResize:
		*needsRender = true
	case hostMsgScrollback:
		h.runScrollbackFrame(getSizeFunc)
	}
}

func (h *Host) sendMsg(msg hostMsg) {
	if h.stopCh != nil {
		select {
		case <-h.stopCh:
			return
		default:
		}
	}
	if h.msgCh == nil {
		return
	}
	select {
	case h.msgCh <- msg:
	default:
		// Queue full fallback
		go func() {
			if h.stopCh != nil {
				select {
				case <-h.stopCh:
					return
				case h.msgCh <- msg:
				}
			} else {
				h.msgCh <- msg
			}
		}()
	}
}

// Stop signals the Run loop to exit.
func (h *Host) Stop() {
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
		h.sendMsg(hostMsg{kind: hostMsgStop})
	}
}

// WriteToScrollback queues a line for scrollback archival and schedules a
// combined scroll-push + full-redraw cycle.
func (h *Host) WriteToScrollback(text string) {
	h.writeToScrollbackCore(text, term.GetSize)
}

func (h *Host) writeToScrollbackCore(
	text string,
	_ func(fd uintptr) (w, ht int, err error),
) {
	h.scrollbackMu.Lock()
	h.scrollbackQueue = append(h.scrollbackQueue, text)
	h.scrollbackMu.Unlock()

	h.sendMsg(hostMsg{kind: hostMsgScrollback, scrollback: text})
}

// ── Frame engine ─────────────────────────────────────────────────────────────

func (h *Host) runFrame(
	getSizeFunc func(fd uintptr) (w, ht int, err error),
) {
	h.dirty.Store(false)

	h.mu.Lock()
	defer h.mu.Unlock()

	w, ht, err := getSizeFunc(os.Stdout.Fd())
	if err != nil {
		return
	}
	if w <= 0 || ht <= 0 {
		return
	}

	// Measure → Render: let the widget hierarchy declare space needs before layout.
	_ = h.root.Measure(Constraints{MaxW: w, MaxH: ht})
	rendered, cursor := h.root.Render(
		terminal.Rect{X: 0, Y: 0, W: w, H: ht},
		RenderContext{Focused: true, Invalidate: h.Invalidate},
	)
	h.cursorX = cursor.X
	h.cursorY = cursor.Y
	h.cursorVisible = cursor.Visible

	occH := rendered.Height
	if occH < 1 {
		occH = 1
	}
	if occH > ht {
		occH = ht
	}

	// If callers pre-populated currBuf/prevBuf before first render (e.g. unit tests),
	// honor that initial allocation height.
	if h.allocHeight == 0 && h.currBuf.Height > 0 && !h.hasRenderedOnce {
		h.allocHeight = h.currBuf.Height
	}
	prevAlloc := h.allocHeight
	if occH > h.allocHeight {
		h.allocHeight = occH
	}
	if h.allocHeight > ht {
		h.allocHeight = ht
	}
	bufH := h.allocHeight
	if occH > bufH {
		bufH = occH
	}

	// Check if Render() enqueued scrollback ejection lines during this frame.
	h.scrollbackMu.Lock()
	sbLines := h.scrollbackQueue
	h.scrollbackQueue = nil
	h.scrollbackMu.Unlock()

	if len(sbLines) > 0 {
		h.flushScrollbackAndFrame(w, ht, occH, bufH, rendered, sbLines)
		return
	}

	widthChanged := h.currBuf.Width != w
	forceFull := !h.hasRenderedOnce || widthChanged
	if widthChanged {
		bufH = occH
		h.allocHeight = occH
		h.currBuf = terminal.NewCellBuf(w, bufH)
		h.prevBuf = terminal.NewCellBuf(w, bufH)
	} else if h.currBuf.Height != bufH {
		newCurr := terminal.NewCellBuf(w, bufH)
		newPrev := terminal.NewCellBuf(w, bufH)
		if h.hasRenderedOnce && h.prevBuf.Width == w {
			copyRows := h.prevBuf.Height
			if copyRows > bufH {
				copyRows = bufH
			}
			// If we are expanding the allocated footprint downward (bufH > prevAlloc),
			// step to the bottom of the old footprint and emit (bufH - prevAlloc) linefeeds
			// so the terminal opens (or scrolls) the new bottom rows cleanly before diffing.
			if prevAlloc > 0 && bufH > prevAlloc {
				extraRows := bufH - prevAlloc
				lastOldRow := prevAlloc - 1
				var expSb strings.Builder
				appendRelativeRow(&expSb, h.prevCursorY, lastOldRow)
				expSb.WriteString("\x1b[0m")
				for i := 0; i < extraRows; i++ {
					expSb.WriteString("\r\n")
				}
				h.out.WriteString(expSb.String())
				h.prevCursorY = bufH - 1
			}
			// If jumping straight to full-screen HUD height (bufH == ht) from a smaller stack,
			// leave newPrev blank so the entire W x ht composite buffer is painted in one pass.
			if !(bufH == ht && prevAlloc > 0 && prevAlloc < ht) {
				copy(newPrev.Cells()[:copyRows*w], h.prevBuf.Cells()[:copyRows*w])
			} else {
				forceFull = true
			}
		}
		h.currBuf = newCurr
		h.prevBuf = newPrev
	} else {
		blank := terminal.NewCellBuf(w, bufH)
		copy(h.currBuf.Cells(), blank.Cells())
	}

	// Copy the rendered occupied stack [0 : occH) into currBuf; any slack rows [occH : bufH) remain blank.
	copy(h.currBuf.Cells()[:occH*w], rendered.Cells()[:occH*w])

	// Span detection: find the minimum dirty row range.
	lineMin, lineMax, changed := h.spanDetect()
	if forceFull {
		lineMin = 0
		lineMax = bufH - 1
		changed = true
	}
	cursorChanged := forceFull ||
		h.cursorX != h.prevCursorX ||
		h.cursorY != h.prevCursorY ||
		h.cursorVisible != h.prevCursorVisible

	if !changed && !cursorChanged {
		return
	}

	if changed {
		h.emitDirtyRegion(lineMin, lineMax)
		// Buffer swap recycles both backing arrays without an O(W*H) copy.
		terminal.SwapBufCells(&h.currBuf, &h.prevBuf)
	} else {
		h.emitCursorOnly()
	}

	h.prevCursorX = h.cursorX
	h.prevCursorY = h.cursorY
	h.prevCursorVisible = h.cursorVisible
	h.hasRenderedOnce = true
}

// spanDetect performs a forward scan (find iMin) and backward scan (find iMax)
// over the flat cell arrays to locate the tightest changed index span.
// The row bounds follow directly from array indices and buffer width.
func (h *Host) spanDetect() (lineMin, lineMax int, changed bool) {
	curr := h.currBuf.Cells()
	prev := h.prevBuf.Cells()
	n := len(curr)
	if n == 0 || len(prev) != n {
		if n > 0 {
			return 0, h.currBuf.Height - 1, true
		}
		return 0, 0, false
	}

	iMin := -1
	for i := 0; i < n; i++ {
		if curr[i] != prev[i] {
			iMin = i
			break
		}
	}
	if iMin < 0 {
		return 0, 0, false
	}

	iMax := iMin
	for i := n - 1; i > iMin; i-- {
		if curr[i] != prev[i] {
			iMax = i
			break
		}
	}

	W := h.currBuf.Width
	return iMin / W, iMax / W, true
}

// appendRelativeRow formats relative vertical cursor movement from fromRow to toRow
// within h.currBuf into sb and returns the cursor to column 0.
func appendRelativeRow(sb *strings.Builder, fromRow, toRow int) {
	dy := toRow - fromRow
	if dy < 0 {
		fmt.Fprintf(sb, "\x1b[%dA", -dy)
	} else if dy > 0 {
		fmt.Fprintf(sb, "\x1b[%dB", dy)
	}
	sb.WriteString("\r")
}

// moveRelativeRow moves the terminal cursor vertically from fromRow to toRow
// within h.currBuf and returns the cursor to column 0.
func (h *Host) moveRelativeRow(fromRow, toRow int) {
	var sb strings.Builder
	appendRelativeRow(&sb, fromRow, toRow)
	h.out.WriteString(sb.String())
}

func (h *Host) emitDirtyRegion(lineMin, lineMax int) {
	// 1. Reposition relatively from the previous cursor row to lineMin.
	fromRow := 0
	if h.hasRenderedOnce {
		fromRow = h.prevCursorY
	}

	// 2. Serialise the dirty sub-slice in a single ToANSI pass.
	// Temporarily strip CharSoftWrap on the final row of subBuf so ToANSI always
	// ends on row lineMax (emitting exactly lineMax - lineMin newlines).
	subH := lineMax - lineMin + 1
	subBuf := terminal.NewSubBuf(h.currBuf, lineMin, subH)
	savedSoft := make([]terminal.Cell, subH)
	hasSoft := make([]bool, subH)
	if subBuf.Width > 0 {
		for y := 0; y < subH; y++ {
			c := subBuf.Get(subBuf.Width-1, y)
			if c.Style.HasAttribute(terminal.CharSoftWrap) {
				savedSoft[y] = c
				hasSoft[y] = true
				clean := c
				clean.Style = clean.Style.RemoveAttribute(terminal.CharSoftWrap)
				subBuf.Set(subBuf.Width-1, y, clean)
			}
		}
	}
	subAnsi := terminal.ToANSI(subBuf)
	if subBuf.Width > 0 {
		for y := 0; y < subH; y++ {
			if hasSoft[y] {
				subBuf.Set(subBuf.Width-1, y, savedSoft[y])
			}
		}
	}

	// 3. Assemble the entire rerender into ONE atomic string chunk:
	// - Cursor movement to lineMin
	// - SGR reset (\x1b[0m) so BCE uses default background color
	// - Clear line (\x1b[2K)
	// - The ToANSI serialized cells
	// - Cursor positioning to (h.cursorX, h.cursorY) and visibility
	var sb strings.Builder
	sb.Grow(len(subAnsi) + 64)
	appendRelativeRow(&sb, fromRow, lineMin)
	sb.WriteString("\x1b[0m\x1b[2K")
	sb.WriteString(subAnsi)
	appendCursorPos(&sb, lineMax, h.cursorX, h.cursorY, h.cursorVisible)

	// 4. Emit the entire frame in a single WriteString and Flush.
	h.out.WriteString(sb.String())
	h.out.Flush()
}

func (h *Host) emitCursorOnly() {
	fromRow := 0
	if h.hasRenderedOnce {
		fromRow = h.prevCursorY
	}
	var sb strings.Builder
	appendCursorPos(&sb, fromRow, h.cursorX, h.cursorY, h.cursorVisible)
	h.out.WriteString(sb.String())
	h.out.Flush()
}

// appendCursorPos formats cursor movement from fromRow to (cursorX, cursorY) and visibility into sb.
func appendCursorPos(sb *strings.Builder, fromRow, cursorX, cursorY int, cursorVisible bool) {
	dy := cursorY - fromRow
	if dy < 0 {
		fmt.Fprintf(sb, "\x1b[%dA", -dy)
	} else if dy > 0 {
		fmt.Fprintf(sb, "\x1b[%dB", dy)
	}
	if cursorVisible {
		sb.WriteString("\x1b[?25h\r")
		if cursorX > 0 {
			fmt.Fprintf(sb, "\x1b[%dC", cursorX)
		}
	} else {
		sb.WriteString("\r\x1b[?25l")
	}
}

func (h *Host) positionCursorFrom(fromRow int) {
	var sb strings.Builder
	appendCursorPos(&sb, fromRow, h.cursorX, h.cursorY, h.cursorVisible)
	h.out.WriteString(sb.String())
}

func (h *Host) flushScrollbackAndFrame(
	w, ht, occH, bufH int,
	rendered terminal.CellBuf,
	lines []string,
) {
	var normalizedLines []string
	for _, rawChunk := range lines {
		clean := strings.ReplaceAll(rawChunk, "\r\n", "\n")
		clean = strings.TrimSuffix(clean, "\n")
		normalizedLines = append(normalizedLines, strings.Split(clean, "\n")...)
	}
	scrollBuf := terminal.FromANSI(normalizedLines, w)

	h.currBuf = terminal.NewCellBuf(w, bufH)
	h.prevBuf = terminal.NewCellBuf(w, bufH)
	copy(h.currBuf.Cells()[:occH*w], rendered.Cells()[:occH*w])

	var sb strings.Builder
	if h.hasRenderedOnce && h.prevCursorY > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", h.prevCursorY)
	}
	if scrollBuf.Height > 0 {
		sb.WriteString("\x1b[0m\r\x1b[2K\x1b[J")
		sb.WriteString(terminal.ToANSI(scrollBuf))
		sb.WriteString("\r\n")
	}
	if sb.Len() > 0 {
		h.out.WriteString(sb.String())
	}

	h.hasRenderedOnce = false
	h.emitDirtyRegion(0, bufH-1)
	copy(h.prevBuf.Cells(), h.currBuf.Cells())
	h.prevCursorX = h.cursorX
	h.prevCursorY = h.cursorY
	h.prevCursorVisible = h.cursorVisible
	h.hasRenderedOnce = true
}

// ── Scrollback archival ───────────────────────────────────────────────────────

func (h *Host) runScrollbackFrame(
	getSizeFunc func(fd uintptr) (w int, ht int, err error),
) {
	h.dirty.Store(false)

	h.mu.Lock()
	defer h.mu.Unlock()

	// Early validation: query window dimensions first to prevent data loss.
	w, ht, err := getSizeFunc(os.Stdout.Fd())
	if err != nil || w <= 0 || ht <= 0 {
		return
	}

	// Drain the queue only after validation succeeds.
	h.scrollbackMu.Lock()
	lines := h.scrollbackQueue
	h.scrollbackQueue = nil
	h.scrollbackMu.Unlock()
	if len(lines) == 0 {
		return
	}

	_ = h.root.Measure(Constraints{MaxW: w, MaxH: ht})
	rendered, cursor := h.root.Render(
		terminal.Rect{X: 0, Y: 0, W: w, H: ht},
		RenderContext{Focused: true, Invalidate: h.Invalidate},
	)
	// Drain any extra scrollback chunks emitted during Render
	h.scrollbackMu.Lock()
	if len(h.scrollbackQueue) > 0 {
		lines = append(lines, h.scrollbackQueue...)
		h.scrollbackQueue = nil
	}
	h.scrollbackMu.Unlock()

	h.cursorX = cursor.X
	h.cursorY = cursor.Y
	h.cursorVisible = cursor.Visible

	occH := rendered.Height
	if occH < 1 {
		occH = 1
	}
	if occH > ht {
		occH = ht
	}
	if h.allocHeight == 0 && h.currBuf.Height > 0 && !h.hasRenderedOnce {
		h.allocHeight = h.currBuf.Height
	}
	if occH > h.allocHeight {
		h.allocHeight = occH
	}
	if h.allocHeight > ht {
		h.allocHeight = ht
	}
	bufH := h.allocHeight
	if occH > bufH {
		bufH = occH
	}

	h.flushScrollbackAndFrame(w, ht, occH, bufH, rendered, lines)
}

// ── Input loop ────────────────────────────────────────────────────────────────

func (h *Host) readInputLoop(in io.Reader) {
	// Use x/input.Reader for robust, protocol-complete event handling.
	// Production passes os.Stdin; tests use an io.Pipe so the terminal boundary
	// stays real without injecting fake global terminal state.
	r, err := input.NewReader(in, "", 0)
	if err != nil {
		return
	}
	defer r.Close()
	h.readInputLoopWithReader(r)
}

func (h *Host) readInputLoopWithReader(r *input.Reader) {
	for {
		if h.stopCh != nil {
			select {
			case <-h.stopCh:
				return
			default:
			}
		}

		resultCh := make(chan struct {
			events []input.Event
			err    error
		}, 1)
		go func() {
			events, err := r.ReadEvents()
			resultCh <- struct {
				events []input.Event
				err    error
			}{events: events, err: err}
		}()

		select {
		case <-h.stopCh:
			return
		case res := <-resultCh:
			if res.err == io.EOF {
				return
			}
			if res.err != nil {
				return
			}

			// Post canonical input.Events to coordinator loop.
			for _, ev := range res.events {
				h.sendMsg(hostMsg{kind: hostMsgInput, inputEvent: ev})
			}
		}
	}
}
