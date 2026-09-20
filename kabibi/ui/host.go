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
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// Host owns the terminal I/O loop, double-buffered frame engine, and widget tree.
//
// Thread safety: Invalidate may be called from any goroutine. All I/O is
// serialised through mu, which is held only during the actual frame dispatch.
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

	stopCh   chan struct{}
	platform platformState // platform-specific raw mode state
}

// NewHost creates a Host bound to the given root widget. Call Run to start the event loop.
func NewHost(root Widget) *Host {
	return &Host{
		root:   root,
		out:    bufio.NewWriterSize(os.Stdout, 64*1024),
		stopCh: make(chan struct{}),
	}
}

// Invalidate requests a redraw. Concurrent calls are coalesced: if a frame is
// already pending, the call returns immediately without spawning additional work.
func (h *Host) Invalidate() {
	if h.dirty.Swap(true) {
		return // already scheduled; coalesced
	}
	go h.runFrame()
}

// Run enters terminal raw mode, delivers an initial frame, and blocks on the
// input loop until Stop is called or stdin closes.
func (h *Host) Run() error {
	if err := h.enterRawMode(); err != nil {
		return err
	}
	defer h.exitRawMode()

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

	h.Invalidate()
	h.readInputLoop()
	return nil
}

// Stop signals the Run loop to exit.
func (h *Host) Stop() {
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
	}
}

// WriteToScrollback queues a line for scrollback archival and schedules a
// combined scroll-push + full-redraw cycle.
func (h *Host) WriteToScrollback(text string) {
	h.mu.Lock()
	h.scrollbackQueue = append(h.scrollbackQueue, text)
	h.mu.Unlock()

	if h.dirty.Swap(true) {
		return
	}
	go h.runScrollbackFrame()
}

// ── Frame engine ─────────────────────────────────────────────────────────────

func (h *Host) runFrame() {
	h.dirty.Store(false)

	h.mu.Lock()
	defer h.mu.Unlock()

	w, ht := h.querySize()
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

func (h *Host) runScrollbackFrame() {
	h.dirty.Store(false)

	h.mu.Lock()
	defer h.mu.Unlock()

	// Early validation: query window dimensions first to prevent data loss.
	w, ht := h.querySize()
	if w <= 0 || ht <= 0 {
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

func (h *Host) readInputLoop() {
	// Use x/input.Reader for robust, protocol-complete event handling.
	// The reader handles stream chunking, UTF-8 decoding, Kitty keyboard protocol,
	// bracketed paste, SGR mouse tracking, focus events, and key releases.
	r, err := input.NewReader(os.Stdin, "", 0)
	if err != nil {
		return
	}
	defer r.Close()

	for {
		select {
		case <-h.stopCh:
			return
		default:
		}

		// Read and parse all available events from stdin.
		events, err := r.ReadEvents()
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}

		// Dispatch canonical input.Event to widget tree.
		// Widgets invoke ctx.Invalidate() explicitly when internal state mutates;
		// the host does not auto-invalidate on event consumption.
		for _, ev := range events {
			h.root.HandleEvent(ev)
		}
	}
}
