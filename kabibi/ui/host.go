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
	inlineMode                         bool
	prevInlineHeight                   int
	allocHeight                        int // Peak allocated inline height (supports slack gap G = allocHeight - occupiedHeight)

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
		if h.inlineMode && h.hasRenderedOnce {
			// Move to the start of the line below the last occupied row (h.prevCursorY on exit),
			// clear any blank footer/slack rows below it, and show the cursor.
			h.out.WriteString("\r\n\x1b[2K\x1b[J\x1b[?25h")
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
		// report error?
		return
	}
	if w <= 0 || ht <= 0 {
		return
	}

	// Re-allocate both buffers on first use or after terminal resize in full-screen mode.
	if !h.inlineMode && (h.currBuf.Width != w || h.currBuf.Height != ht) {
		h.currBuf = terminal.NewCellBuf(w, ht)
		h.prevBuf = terminal.NewCellBuf(w, ht)
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

	// Inline Stack mode: render the active stack (banner + blocks + prompt + footer + any slack gap)
	// using relative cursor movement (\r / \x1b[A / \x1b[B) and per-row dirty diffing without
	// jumping to absolute screen coordinates unless full-screen panel mode is active.
	if rendered.Height > 0 && rendered.Height <= ht && (rendered.Height < ht || h.inlineMode) && ht > 1 {
		h.inlineMode = true
		occH := rendered.Height
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
			var normalizedLines []string
			for _, rawChunk := range sbLines {
				clean := strings.ReplaceAll(rawChunk, "\r\n", "\n")
				clean = strings.TrimSuffix(clean, "\n")
				normalizedLines = append(normalizedLines, strings.Split(clean, "\n")...)
			}
			scrollBuf := terminal.FromANSI(normalizedLines, w)

			h.currBuf = terminal.NewCellBuf(w, bufH)
			h.prevBuf = terminal.NewCellBuf(w, bufH)
			copy(h.currBuf.Cells()[:occH*w], rendered.Cells()[:occH*w])

			if h.hasRenderedOnce && h.prevCursorY > 0 {
				fmt.Fprintf(h.out, "\x1b[%dA", h.prevCursorY)
			}
			if scrollBuf.Height > 0 {
				h.out.WriteString("\r\x1b[2K\x1b[J")
				h.out.WriteString(terminal.ToANSI(scrollBuf))
				h.out.WriteString("\r\n")
			}
			h.prevInlineHeight = bufH
			h.renderInlineRows(bufH)
			h.positionInlineCursor(bufH - 1)
			copy(h.prevBuf.Cells(), h.currBuf.Cells())
			h.prevCursorX = h.cursorX
			h.prevCursorY = h.cursorY
			h.prevCursorVisible = h.cursorVisible
			h.hasRenderedOnce = true
			h.out.Flush()
			return
		}

		widthChanged := h.currBuf.Width != w
		forceFullInline := !h.hasRenderedOnce || widthChanged
		if widthChanged {
			// On width change, resize both buffers to the current occupied height.
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
				// When transitioning straight to full-screen HUD height (bufH == ht) from
				// a compact inline stack (h.prevInlineHeight < ht), push the prior external
				// terminal lines above row 0 by advancing (ht - h.prevInlineHeight) newlines
				// at the bottom of the old inline stack so the top rows are virgin screen space.
				if bufH == ht && h.prevInlineHeight > 0 && h.prevInlineHeight < ht {
					extraScroll := ht - h.prevInlineHeight
					lastOldRow := h.prevInlineHeight - 1
					if h.prevCursorY < lastOldRow {
						fmt.Fprintf(h.out, "\x1b[%dB", lastOldRow-h.prevCursorY)
					} else if h.prevCursorY > lastOldRow {
						fmt.Fprintf(h.out, "\x1b[%dA", h.prevCursorY-lastOldRow)
					}
					for i := 0; i < extraScroll; i++ {
						h.out.WriteString("\r\n")
					}
					fmt.Fprintf(h.out, "\x1b[%dA", ht-1)
					h.prevCursorY = 0
					h.prevInlineHeight = ht
					forceFullInline = true
				} else {
					copy(newPrev.Cells()[:copyRows*w], h.prevBuf.Cells()[:copyRows*w])
				}
			}
			h.currBuf = newCurr
			h.prevBuf = newPrev
		} else {
			blank := terminal.NewCellBuf(w, bufH)
			copy(h.currBuf.Cells(), blank.Cells())
		}

		// Copy the rendered occupied stack [0 : occH) into currBuf; slack rows [occH : bufH) remain blank.
		copy(h.currBuf.Cells()[:occH*w], rendered.Cells()[:occH*w])

		_, _, changed := h.spanDetect()
		if forceFullInline {
			changed = true
		}
		cursorChanged := forceFullInline ||
			h.cursorX != h.prevCursorX ||
			h.cursorY != h.prevCursorY ||
			h.cursorVisible != h.prevCursorVisible
		if !changed && !cursorChanged {
			return
		}

		curY := 0
		if h.hasRenderedOnce {
			curY = h.prevCursorY
		}

		if changed {
			curY = h.renderInlineDiff(curY, bufH, forceFullInline)
			terminal.SwapBufCells(&h.currBuf, &h.prevBuf)
		}
		h.positionInlineCursor(curY)
		h.out.Flush()
		h.prevInlineHeight = bufH
		h.prevCursorX = h.cursorX
		h.prevCursorY = h.cursorY
		h.prevCursorVisible = h.cursorVisible
		h.hasRenderedOnce = true
		return
	}
	h.inlineMode = false
	h.prevInlineHeight = 0
	h.allocHeight = 0

	// Copy rendered frame into currBuf; widgets must overwrite every cell they own.
	copy(h.currBuf.Cells(), rendered.Cells())

	// Span detection: find the minimum dirty row range.
	lineMin, lineMax, changed := h.spanDetect()
	cursorChanged := !h.hasRenderedOnce ||
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

func (h *Host) emitDirtyRegion(lineMin, lineMax int) {
	// 1. Reposition to the first dirty line.
	fmt.Fprintf(h.out, "\x1b[%d;1H", lineMin+1)

	// 2. Serialise only the dirty sub-slice; zero intermediate allocations.
	subBuf := terminal.NewSubBuf(h.currBuf, lineMin, lineMax-lineMin+1)
	h.out.WriteString(terminal.ToANSI(subBuf))

	// 3. Show or hide hardware cursor depending on active focus state.
	h.writeCursorSequence()
	h.out.Flush()
}

func (h *Host) emitCursorOnly() {
	h.writeCursorSequence()
	h.out.Flush()
}

func (h *Host) writeCursorSequence() {
	if h.cursorVisible {
		h.out.WriteString("\x1b[?25h")
		fmt.Fprintf(h.out, "\x1b[%d;%dH", h.cursorY+1, h.cursorX+1)
	} else {
		h.out.WriteString("\x1b[?25l")
	}
}

// rowChanged reports whether row y differs between h.currBuf and h.prevBuf.
func (h *Host) rowChanged(y int) bool {
	w := h.currBuf.Width
	if w <= 0 || y < 0 || y >= h.currBuf.Height || y >= h.prevBuf.Height {
		return true
	}
	start := y * w
	end := start + w
	curr := h.currBuf.Cells()[start:end]
	prev := h.prevBuf.Cells()[start:end]
	for i := 0; i < w; i++ {
		if curr[i] != prev[i] {
			return true
		}
	}
	return false
}

// moveInlineVertical moves the terminal cursor from curY to targetY within the
// inline stack region, using \r\n only when stepping onto virgin rows >= prevAlloc
// so existing terminal rows are never scrolled unintentionally.
func (h *Host) moveInlineVertical(curY, targetY int, prevAlloc *int) int {
	if targetY < curY {
		fmt.Fprintf(h.out, "\x1b[%dA", curY-targetY)
		return targetY
	}
	for curY < targetY {
		if curY+1 < *prevAlloc {
			// Within already-allocated rows: jump directly up to (*prevAlloc - 1) or targetY
			stepTo := targetY
			if *prevAlloc-1 < stepTo {
				stepTo = *prevAlloc - 1
			}
			if stepTo > curY {
				fmt.Fprintf(h.out, "\x1b[%dB", stepTo-curY)
				curY = stepTo
				continue
			}
		}
		// Stepping onto a newly allocated row at the bottom of the inline footprint
		h.out.WriteString("\r\n")
		curY++
		if curY+1 > *prevAlloc {
			*prevAlloc = curY + 1
		}
	}
	return curY
}

// writeInlineRow writes row y of h.currBuf at the current terminal line.
func (h *Host) writeInlineRow(y int) {
	h.out.WriteString("\r\x1b[2K")
	rowBuf := terminal.NewSubBuf(h.currBuf, y, 1)
	// Strip CharSoftWrap on the last column of the 1-row subBuf so ToANSI
	// treats each visual row independently.
	savedCell := rowBuf.Get(rowBuf.Width-1, 0)
	if savedCell.Style.HasAttribute(terminal.CharSoftWrap) {
		cleanCell := savedCell
		cleanCell.Style = cleanCell.Style.RemoveAttribute(terminal.CharSoftWrap)
		rowBuf.Set(rowBuf.Width-1, 0, cleanCell)
		h.out.WriteString(terminal.ToANSI(rowBuf))
		rowBuf.Set(rowBuf.Width-1, 0, savedCell)
	} else {
		h.out.WriteString(terminal.ToANSI(rowBuf))
	}
}

// renderInlineDiff incrementally updates only the rows in [0, totalH) that changed
// between h.prevBuf and h.currBuf (plus any newly allocated rows), returning the
// final row index where the terminal cursor is left.
func (h *Host) renderInlineDiff(curY, totalH int, forceAll bool) int {
	prevAlloc := h.prevInlineHeight
	if prevAlloc < 1 && totalH > 0 {
		prevAlloc = 1
	}
	for y := 0; y < totalH; y++ {
		if !forceAll && y < h.prevInlineHeight && !h.rowChanged(y) {
			continue
		}
		curY = h.moveInlineVertical(curY, y, &prevAlloc)
		h.writeInlineRow(y)
	}
	// Ensure any newly allocated trailing blank rows (e.g. initial blank footer line)
	// are physically opened in the terminal even if their cells are spaces.
	if totalH > prevAlloc {
		curY = h.moveInlineVertical(curY, totalH-1, &prevAlloc)
		h.writeInlineRow(totalH - 1)
	}
	return curY
}

// renderInlineRows writes each row of h.currBuf starting from row 0 of the inline
// region and leaves the terminal cursor on row inlineH-1.
func (h *Host) renderInlineRows(inlineH int) {
	for y := 0; y < inlineH; y++ {
		if y > 0 {
			h.out.WriteString("\r\n")
		}
		h.writeInlineRow(y)
	}
	if h.prevInlineHeight > inlineH {
		// Clear any stale wrapped rows below the shrunk inline widget.
		h.out.WriteString("\x1b[J")
	}
}

// positionInlineCursor moves the terminal cursor from fromRow (relative to the
// inline region top) to (h.cursorX, h.cursorY) and updates cursor visibility.
func (h *Host) positionInlineCursor(fromRow int) {
	dy := h.cursorY - fromRow
	if dy < 0 {
		fmt.Fprintf(h.out, "\x1b[%dA", -dy)
	} else if dy > 0 {
		fmt.Fprintf(h.out, "\x1b[%dB", dy)
	}
	if h.cursorVisible {
		h.out.WriteString("\x1b[?25h\r")
		if h.cursorX > 0 {
			fmt.Fprintf(h.out, "\x1b[%dC", h.cursorX)
		}
	} else {
		h.out.WriteString("\r\x1b[?25l")
	}
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

	// If the root widget already rendered and updated h.currBuf during the render
	// pass that queued this scrollback (e.g. Shell.Render ejecting overflow rows),
	// reuse h.currBuf directly when h.dirty is false; otherwise render fresh.
	var rendered terminal.CellBuf
	var cursor CursorPos
	if !h.hasRenderedOnce {
		_ = h.root.Measure(Constraints{MaxW: w, MaxH: ht})
		rendered, cursor = h.root.Render(
			terminal.Rect{X: 0, Y: 0, W: w, H: ht},
			RenderContext{Focused: true, Invalidate: h.Invalidate},
		)
		// Drain any extra scrollback chunks emitted during that initial Render
		h.scrollbackMu.Lock()
		if len(h.scrollbackQueue) > 0 {
			lines = append(lines, h.scrollbackQueue...)
			h.scrollbackQueue = nil
		}
		h.scrollbackMu.Unlock()
	} else {
		_ = h.root.Measure(Constraints{MaxW: w, MaxH: ht})
		rendered, cursor = h.root.Render(
			terminal.Rect{X: 0, Y: 0, W: w, H: ht},
			RenderContext{Focused: true, Invalidate: h.Invalidate},
		)
	}
	h.cursorX = cursor.X
	h.cursorY = cursor.Y
	h.cursorVisible = cursor.Visible

	// Normalize scrollback chunks into individual lines so multi-line payloads
	// advance the viewport by the exact number of visual rows they occupy.
	var normalizedLines []string
	for _, rawChunk := range lines {
		clean := strings.ReplaceAll(rawChunk, "\r\n", "\n")
		clean = strings.TrimSuffix(clean, "\n")
		normalizedLines = append(normalizedLines, strings.Split(clean, "\n")...)
	}

	// Width-constrained ANSI parsing: FromANSI renders scrollback lines
	// with word-wrapping and ANSI style handling directly into cells.
	scrollBuf := terminal.FromANSI(normalizedLines, w)

	// Inline Stack mode: move to top of the current inline region, emit the ejected
	// scrollback lines followed by \r\n, and redraw the active stack on the new
	// current lines without jumping to absolute screen coordinates.
	if rendered.Height > 0 && rendered.Height <= ht && (rendered.Height < ht || h.inlineMode) && ht > 1 {
		h.inlineMode = true
		occH := rendered.Height
		if occH > h.allocHeight {
			h.allocHeight = occH
		}
		if h.allocHeight > ht {
			h.allocHeight = ht
		}
		bufH := occH
		if h.allocHeight > bufH {
			bufH = h.allocHeight
		}
		h.currBuf = terminal.NewCellBuf(w, bufH)
		h.prevBuf = terminal.NewCellBuf(w, bufH)
		copy(h.currBuf.Cells()[:occH*w], rendered.Cells()[:occH*w])

		if h.hasRenderedOnce && h.prevCursorY > 0 {
			fmt.Fprintf(h.out, "\x1b[%dA", h.prevCursorY)
		}

		if scrollBuf.Height > 0 {
			h.out.WriteString("\r\x1b[2K\x1b[J")
			h.out.WriteString(terminal.ToANSI(scrollBuf))
			h.out.WriteString("\r\n")
		}
		h.prevInlineHeight = bufH
		h.renderInlineRows(bufH)
		h.positionInlineCursor(bufH - 1)
		copy(h.prevBuf.Cells(), h.currBuf.Cells())
		h.prevCursorX = h.cursorX
		h.prevCursorY = h.cursorY
		h.prevCursorVisible = h.cursorVisible
		h.hasRenderedOnce = true
		h.out.Flush()
		return
	}
	h.inlineMode = false
	h.prevInlineHeight = 0
	h.allocHeight = 0

	// Guard: Restore buffer allocation if dimensions changed since last frame.
	if h.currBuf.Width != w || h.currBuf.Height != ht {
		h.currBuf = terminal.NewCellBuf(w, ht)
		h.prevBuf = terminal.NewCellBuf(w, ht)
	}
	copy(h.currBuf.Cells(), rendered.Cells())

	// 1. Move to bottom row so newlines push existing UI into terminal scrollback history.
	fmt.Fprintf(h.out, "\x1b[%d;1H", ht)

	// 2. Emit parsed scrollback buffer rows with viewport advancement.
	if scrollBuf.Height > 0 {
		h.out.WriteString("\r\n")
		h.out.WriteString(terminal.ToANSI(scrollBuf))
	}

	// 3. Full UI redraw over the now-advanced viewport.
	h.out.WriteString("\x1b[H")
	h.out.WriteString(terminal.ToANSI(h.currBuf))

	// 4. Show or hide hardware cursor.
	h.writeCursorSequence()
	h.prevCursorX = h.cursorX
	h.prevCursorY = h.cursorY
	h.prevCursorVisible = h.cursorVisible
	h.hasRenderedOnce = true

	// 5. Sync prevBuf so the next spanDetect() correctly compares against the new screen state.
	copy(h.prevBuf.Cells(), h.currBuf.Cells())

	h.out.Flush()
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
