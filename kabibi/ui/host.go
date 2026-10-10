package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	cursorX, cursorY int
	cursorVisible    bool

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
	return h.runCore(term.MakeRaw, term.Restore)
}

func (h *Host) runCore(
	makeRawFunc func(uintptr) (*term.State, error),
	restoreFunc func(uintptr, *term.State) error,
) (err error) {

	// enterRawMode puts stdin into raw mode and enables SGR mouse tracking.
	// Uses golang.org/x/term.MakeRaw for cross-platform compatibility across
	// Linux, macOS, BSD, and Windows.
	state := &term.State{}

	defer func() {
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

	state, err = makeRawFunc(os.Stdin.Fd())
	if err != nil {
		state = nil
		return err
	}
	h.prevTermState = state
	h.out.WriteString("\x1b[?1000h\x1b[?1006h")
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

	// Start reading input in background producer goroutine
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		h.readInputLoop(os.Stdin)
	}()

	// Run central event coordinator loop on this main host goroutine
	h.processLoop(term.GetSize)
	<-inputDone
	return nil
}

// processLoop is the single coordinator loop that owns all access to h.root.
func (h *Host) processLoop(getSizeFunc func(fd uintptr) (w, ht int, err error)) {
	for {
		select {
		case <-h.stopCh:
			return
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
	h.mu.Lock()
	h.scrollbackQueue = append(h.scrollbackQueue, text)
	h.mu.Unlock()

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

	// Re-allocate both buffers on first use or after terminal resize.
	if h.currBuf.Width != w || h.currBuf.Height != ht {
		h.currBuf = terminal.NewCellBuf(w, ht)
		h.prevBuf = terminal.NewCellBuf(w, ht)
	}

	// Measure → Render: let the widget hierarchy declare space needs before layout.
	h.root.Measure(Constraints{MaxW: w, MaxH: ht})
	rendered, cursor := h.root.Render(
		terminal.Rect{X: 0, Y: 0, W: w, H: ht},
		RenderContext{Focused: true, Invalidate: h.Invalidate},
	)
	h.cursorX = cursor.X
	h.cursorY = cursor.Y
	h.cursorVisible = cursor.Visible

	// Copy rendered frame into currBuf; widgets must overwrite every cell they own.
	copy(h.currBuf.Cells(), rendered.Cells())

	// Span detection: find the minimum dirty row range.
	lineMin, lineMax, changed := h.spanDetect()
	if !changed {
		return
	}

	h.emitDirtyRegion(lineMin, lineMax)

	// Buffer swap recycles both backing arrays without an O(W*H) copy.
	terminal.SwapBufCells(&h.currBuf, &h.prevBuf)
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
	if h.cursorVisible {
		h.out.WriteString("\x1b[?25h")
		fmt.Fprintf(h.out, "\x1b[%d;%dH", h.cursorY+1, h.cursorX+1)
	} else {
		h.out.WriteString("\x1b[?25l")
	}

	h.out.Flush()
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
	lines := h.scrollbackQueue
	h.scrollbackQueue = nil
	if len(lines) == 0 {
		return
	}

	// Guard: Restore buffer allocation if dimensions changed since last frame.
	if h.currBuf.Width != w || h.currBuf.Height != ht {
		h.currBuf = terminal.NewCellBuf(w, ht)
		h.prevBuf = terminal.NewCellBuf(w, ht)
	}

	// Fresh render: paint current widget state before emitting the scrollback payload.
	h.root.Measure(Constraints{MaxW: w, MaxH: ht})
	rendered, cursor := h.root.Render(
		terminal.Rect{X: 0, Y: 0, W: w, H: ht},
		RenderContext{Focused: true, Invalidate: h.Invalidate},
	)
	h.cursorX = cursor.X
	h.cursorY = cursor.Y
	h.cursorVisible = cursor.Visible
	copy(h.currBuf.Cells(), rendered.Cells())

	// Width-constrained ANSI parsing: FromANSI renders scrollback lines
	// with word-wrapping and ANSI style handling directly into cells.
	scrollBuf := terminal.FromANSI(lines, w)

	// 1. Move to bottom row so newlines push existing UI into terminal scrollback history.
	fmt.Fprintf(h.out, "\x1b[%d;1H", ht)

	// 2. Emit parsed scrollback buffer rows with viewport advancement.
	h.out.WriteString(terminal.ToANSI(scrollBuf))

	// 3. Full UI redraw over the now-advanced viewport.
	h.out.WriteString("\x1b[H")
	h.out.WriteString(terminal.ToANSI(h.currBuf))

	// 4. Show or hide hardware cursor.
	if h.cursorVisible {
		h.out.WriteString("\x1b[?25h")
		fmt.Fprintf(h.out, "\x1b[%d;%dH", h.cursorY+1, h.cursorX+1)
	} else {
		h.out.WriteString("\x1b[?25l")
	}

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

	for {
		if h.stopCh != nil {
			select {
			case <-h.stopCh:
				_ = r.Close()
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
			_ = r.Close()
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
