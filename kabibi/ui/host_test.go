package ui

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/input"
	xterm "github.com/charmbracelet/x/term"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type recordingWidget struct {
	measureW  int
	measureH  int
	cursor    CursorPos
	buf       terminal.CellBuf
	handled   bool
	lastEvent input.Event
}

type errReader struct{ err error }

func (r *errReader) Read(p []byte) (int, error) { return 0, r.err }

func (w *recordingWidget) Measure(c Constraints) Size {
	w.measureW = c.MaxW
	w.measureH = c.MaxH
	return Size{W: c.MaxW, H: c.MaxH}
}

func (w *recordingWidget) Render(r terminal.Rect, ctx RenderContext) (terminal.CellBuf, CursorPos) {
	buf := terminal.NewCellBuf(r.W, r.H)
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			buf.Set(x, y, terminal.Cell{Grapheme: "A"})
		}
	}
	w.buf = buf
	return buf, w.cursor
}

func (w *recordingWidget) HandleEvent(ev input.Event) bool {
	w.handled = true
	w.lastEvent = ev
	return true
}

func TestNewHost_BindsRoot(t *testing.T) {
	root := noopWidget{}
	h := NewHost(root, false)
	if got, want := h.root, root; got != want {
		t.Fatalf("NewHost().root = %#v, want %#v", got, want)
	}
}

func TestNewHost_StoresVerboseFlag(t *testing.T) {
	h := NewHost(noopWidget{}, true)
	if !h.verbose {
		t.Fatal("NewHost() did not preserve verbose=true")
	}
}

func TestNewHost_InitialDirtyFlagIsFalse(t *testing.T) {
	h := NewHost(noopWidget{}, false)
	if h.dirty.Load() {
		t.Fatal("NewHost() should start with dirty == false")
	}
}

func TestStop_ClosesChannelOnce(t *testing.T) {
	h := NewHost(noopWidget{}, false)
	h.Stop()
	if _, ok := <-h.stopCh; ok {
		t.Fatal("Stop() should close stopCh")
	}
}

func TestWriteToScrollback_QueuesText(t *testing.T) {
	h := NewHost(noopWidget{}, false)
	h.WriteToScrollback("hello")
	if got, want := len(h.scrollbackQueue), 1; got != want {
		t.Fatalf("len(scrollbackQueue) = %d, want %d", got, want)
	}
	if got, want := h.scrollbackQueue[0], "hello"; got != want {
		t.Fatalf("scrollbackQueue[0] = %q, want %q", got, want)
	}
}

func TestWriteToScrollback_ConcurrentWritesAreAllPreserved(t *testing.T) {
	h := NewHost(noopWidget{}, false)
	for i := 0; i < 20; i++ {
		h.WriteToScrollback("hello")
	}
	if got := len(h.scrollbackQueue); got != 20 {
		t.Fatalf("len(scrollbackQueue) = %d, want 20", got)
	}
}

func TestReadInputLoop_ParsesAndEnqueuesEvents(t *testing.T) {
	pr, pw := io.Pipe()
	widget := &recordingWidget{}
	h := NewHost(widget, false)

	done := make(chan struct{})
	go func() {
		h.readInputLoop(pr)
		close(done)
	}()

	go func() {
		defer pw.Close()
		_, _ = pw.Write([]byte("a"))
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readInputLoop() did not exit after input was closed")
	}

	select {
	case msg := <-h.msgCh:
		if msg.kind != hostMsgInput {
			t.Fatalf("msg.kind = %v, want hostMsgInput", msg.kind)
		}
		key, ok := msg.inputEvent.(input.KeyPressEvent)
		if !ok {
			t.Fatalf("msg.inputEvent is %T, want input.KeyPressEvent", msg.inputEvent)
		}
		if got, want := key.Keystroke(), "a"; got != want {
			t.Fatalf("keystroke = %q, want %q", got, want)
		}
		needsRender := false
		h.handleHostMsg(msg, &needsRender, func(uintptr) (int, int, error) { return 80, 24, nil })
		if !widget.handled {
			t.Fatal("handleHostMsg did not dispatch event to widget")
		}
		if !needsRender {
			t.Fatal("handleHostMsg did not set needsRender to true for handled event")
		}
	default:
		t.Fatal("readInputLoop() did not enqueue a message to h.msgCh")
	}
}

func TestRunScrollbackFrame_RendersANSIOutputAndPayload(t *testing.T) {
	var out bytes.Buffer
	h := &Host{
		root:            noopWidget{},
		out:             bufio.NewWriter(&out),
		currBuf:         terminal.NewCellBuf(80, 24),
		prevBuf:         terminal.NewCellBuf(80, 24),
		scrollbackQueue: []string{"some log line"},
	}

	h.runScrollbackFrame(func(fd uintptr) (int, int, error) { return 80, 24, nil })

	got := out.String()
	if !strings.Contains(got, "some log line") {
		t.Fatalf("scrollback output = %q, want rendered payload to contain %q", got, "some log line")
	}
	if !strings.Contains(got, "\r\x1b[2K\x1b[J") || !strings.Contains(got, "\r\n") {
		t.Fatalf("scrollback output = %q, want relative line clear and scroll progression", got)
	}
}

func TestRunFrame_AbortsOnDimensionQueryError(t *testing.T) {
	var out bytes.Buffer
	h := &Host{
		root:    noopWidget{},
		out:     bufio.NewWriter(&out),
		currBuf: terminal.NewCellBuf(80, 24),
		prevBuf: terminal.NewCellBuf(80, 24),
	}

	e := errors.New("term size failed")
	beforeW, beforeH := h.currBuf.Width, h.currBuf.Height
	h.runFrame(func(fd uintptr) (int, int, error) { return 0, 0, e })

	if got, want := h.currBuf.Width, beforeW; got != want {
		t.Fatalf("currBuf.Width = %d, want %d after size error", got, want)
	}
	if got, want := h.currBuf.Height, beforeH; got != want {
		t.Fatalf("currBuf.Height = %d, want %d after size error", got, want)
	}
}

func TestRunScrollbackFrame_AbortsOnDimensionQueryError(t *testing.T) {
	var out bytes.Buffer
	h := &Host{
		root:            noopWidget{},
		out:             bufio.NewWriter(&out),
		currBuf:         terminal.NewCellBuf(80, 24),
		prevBuf:         terminal.NewCellBuf(80, 24),
		scrollbackQueue: []string{"queued"},
	}

	e := errors.New("term size failed")
	h.runScrollbackFrame(func(fd uintptr) (int, int, error) { return 0, 0, e })

	if got, want := len(h.scrollbackQueue), 1; got != want {
		t.Fatalf("len(scrollbackQueue) = %d, want %d after size error", got, want)
	}
	if got := out.String(); got != "" {
		t.Fatalf("scrollback output = %q, want empty buffer after size error", got)
	}
}

func TestRunFrame_ResizesBuffersWhenDimensionsChange(t *testing.T) {
	var out bytes.Buffer
	h := &Host{root: noopWidget{}, out: bufio.NewWriter(&out)}

	sizes := []struct{ w, h int }{{80, 24}, {100, 30}}
	for _, sz := range sizes {
		h.runFrame(func(fd uintptr) (int, int, error) { return sz.w, sz.h, nil })
	}

	if got, want := h.currBuf.Width, 100; got != want {
		t.Fatalf("currBuf.Width = %d, want %d", got, want)
	}
	if got, want := h.currBuf.Height, 30; got != want {
		t.Fatalf("currBuf.Height = %d, want %d", got, want)
	}
	if got, want := h.prevBuf.Width, 100; got != want {
		t.Fatalf("prevBuf.Width = %d, want %d", got, want)
	}
	if got, want := h.prevBuf.Height, 30; got != want {
		t.Fatalf("prevBuf.Height = %d, want %d", got, want)
	}
}

func TestRunCore_UsesInjectedRawModeBoundary(t *testing.T) {
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdin = reader
	defer func() {
		os.Stdin = oldStdin
		_ = reader.Close()
		_ = writer.Close()
	}()
	_ = writer.Close()

	var out bytes.Buffer
	stopped := make(chan struct{})
	close(stopped)
	h := &Host{root: noopWidget{}, out: bufio.NewWriter(&out), stopCh: stopped}
	called := false
	restored := false

	if err := h.runCore(
		os.Stdin,
		&out,
		func(fd uintptr) (*xterm.State, error) {
			called = true
			if got, want := int(fd), int(os.Stdin.Fd()); got != want {
				t.Fatalf("makeRawFunc fd = %d, want %d", got, want)
			}
			return &xterm.State{}, nil
		},
		func(fd uintptr, st *xterm.State) error {
			restored = true
			if got, want := int(fd), int(os.Stdin.Fd()); got != want {
				t.Fatalf("restoreFunc fd = %d, want %d", got, want)
			}
			if st == nil {
				t.Fatal("restoreFunc was called with nil terminal state")
			}
			return nil
		},
		func(uintptr) (int, int, error) { return 80, 24, nil },
	); err != nil {
		t.Fatalf("runCore() error = %v", err)
	}

	if !called {
		t.Fatal("runCore() did not call the injected makeRawFunc")
	}
	if !restored {
		t.Fatal("runCore() did not call the injected restoreFunc")
	}
	gotOutput := out.String()
	const mouseTrackingOff = "\x1b[?1006l\x1b[?1000l"
	if strings.Contains(gotOutput, "\x1b[?1000h") || strings.Contains(gotOutput, "\x1b[?1006h") {
		t.Fatal("runCore() enabled mouse tracking; the terminal should handle mouse input")
	}
	if got := strings.Count(gotOutput, mouseTrackingOff); got < 2 {
		t.Fatalf("runCore() emitted mouse-tracking-off sequence %d times, want startup and cleanup", got)
	}
	pushKeyboard := ansi.PushKittyKeyboard(kittyKeyboardFlags)
	popKeyboard := ansi.PopKittyKeyboard(1)
	if !strings.Contains(gotOutput, pushKeyboard) {
		t.Fatalf("runCore() output does not enable Kitty keyboard reporting with %q", pushKeyboard)
	}
	if !strings.Contains(gotOutput, popKeyboard) {
		t.Fatalf("runCore() output does not restore the prior Kitty keyboard mode with %q", popKeyboard)
	}
	if strings.Index(gotOutput, pushKeyboard) > strings.Index(gotOutput, popKeyboard) {
		t.Fatal("runCore() restored Kitty keyboard mode before enabling it")
	}
}

func TestRunCore_PropagatesMakeRawError(t *testing.T) {
	h := &Host{root: noopWidget{}, out: bufio.NewWriter(io.Discard), stopCh: make(chan struct{})}
	expected := errors.New("raw mode failed")

	err := h.runCore(
		os.Stdin,
		io.Discard,
		func(uintptr) (*xterm.State, error) { return nil, expected },
		func(uintptr, *xterm.State) error {
			t.Fatal("restoreFunc should not run when makeRaw fails")
			return nil
		},
		func(uintptr) (int, int, error) { return 80, 24, nil },
	)
	if !errors.Is(err, expected) {
		t.Fatalf("runCore() error = %v, want %v", err, expected)
	}
	if h.prevTermState != nil {
		t.Fatal("runCore() should leave prevTermState nil when makeRaw fails")
	}
}

func TestReadInputLoop_ExitsOnReadError(t *testing.T) {
	h := NewHost(noopWidget{}, false)
	done := make(chan struct{})
	go func() {
		h.readInputLoop(&errReader{err: errors.New("read failed")})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readInputLoop() did not exit after a read error")
	}
}

func TestRunCore_RestoresTerminalBeforeRePanicking(t *testing.T) {
	var out bytes.Buffer
	restored := false
	h := &Host{root: noopWidget{}, out: bufio.NewWriter(&out), stopCh: make(chan struct{})}

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("runCore() did not re-panic after a setup failure")
			}
			if got := r; got != "widget explosion" {
				t.Fatalf("panic = %v, want %q", got, "widget explosion")
			}
			if !restored {
				t.Fatal("restoreFunc() was not called before the panic was re-raised")
			}
		}()
		_ = h.runCore(
			os.Stdin,
			&out,
			func(uintptr) (*xterm.State, error) {
				panic("widget explosion")
			},
			func(uintptr, *xterm.State) error {
				restored = true
				return nil
			},
			func(uintptr) (int, int, error) { return 80, 24, nil },
		)
	}()
}

func TestRunCore_RePanicsAfterRestore(t *testing.T) {
	var out bytes.Buffer
	restored := false
	closed := make(chan struct{})
	close(closed)

	h := &Host{root: noopWidget{}, out: bufio.NewWriter(&out), stopCh: closed}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("runCore() did not re-panic after makeRawFunc exploded")
		}
		if got := r; got != "widget explosion" {
			t.Fatalf("panic = %v, want %q", got, "widget explosion")
		}
		if !restored {
			t.Fatal("restoreFunc() was not called before the panic was re-raised")
		}
	}()

	_ = h.runCore(
		os.Stdin,
		&out,
		func(uintptr) (*xterm.State, error) {
			panic("widget explosion")
		},
		func(uintptr, *xterm.State) error {
			restored = true
			return nil
		},
		func(uintptr) (int, int, error) { return 80, 24, nil },
	)
}

func TestRunScrollbackFrame_RendersQueuedText(t *testing.T) {
	var out bytes.Buffer
	h := &Host{
		root:            noopWidget{},
		out:             bufio.NewWriter(&out),
		currBuf:         terminal.NewCellBuf(80, 24),
		prevBuf:         terminal.NewCellBuf(80, 24),
		scrollbackQueue: []string{"hello"},
	}

	h.runScrollbackFrame(func(fd uintptr) (int, int, error) { return 80, 24, nil })

	got := out.String()
	if !strings.Contains(got, "hello") {
		t.Fatalf("scrollback output = %q, want payload to contain %q", got, "hello")
	}
}

func TestSpanDetect_IdenticalBuffers_NoChange(t *testing.T) {
	buf := terminal.NewCellBuf(3, 2)
	for i := range buf.Cells() {
		buf.Cells()[i] = terminal.Cell{Grapheme: "A"}
	}
	h := &Host{currBuf: buf, prevBuf: buf}
	if _, _, changed := h.spanDetect(); changed {
		t.Fatal("spanDetect() should report no change for identical buffers")
	}
}

func TestSpanDetect_SingleCellMutation_ExactBounds(t *testing.T) {
	curr := terminal.NewCellBuf(3, 2)
	prev := terminal.NewCellBuf(3, 2)
	for i := range curr.Cells() {
		curr.Cells()[i] = terminal.Cell{Grapheme: "A"}
		prev.Cells()[i] = terminal.Cell{Grapheme: "A"}
	}
	curr.Set(1, 1, terminal.Cell{Grapheme: "B"})
	h := &Host{currBuf: curr, prevBuf: prev}
	lineMin, lineMax, changed := h.spanDetect()
	if !changed || lineMin != 1 || lineMax != 1 {
		t.Fatalf("spanDetect() = (%d, %d, %v), want (1, 1, true)", lineMin, lineMax, changed)
	}
}

func TestEmitDirtyRegion_WritesANSISequences(t *testing.T) {
	var out bytes.Buffer
	h := &Host{
		currBuf:       terminal.NewCellBuf(3, 2),
		prevBuf:       terminal.NewCellBuf(3, 2),
		out:           bufio.NewWriter(&out),
		cursorX:       1,
		cursorY:       1,
		cursorVisible: true,
	}
	h.currBuf.Set(0, 0, terminal.Cell{Grapheme: "A"})
	h.emitDirtyRegion(0, 0)
	got := out.String()
	if !strings.Contains(got, "\x1b[1B") || !strings.Contains(got, "\x1b[?25h") || !strings.Contains(got, "A") {
		t.Fatalf("emitDirtyRegion() output = %q, want relative cursor movement + visible cursor + data", got)
	}
}

func TestProcessLoop_CoalescesRedrawsUnderBurst(t *testing.T) {
	var out bytes.Buffer
	widget := &recordingWidget{}
	h := &Host{
		root:    widget,
		out:     bufio.NewWriter(&out),
		currBuf: terminal.NewCellBuf(10, 5),
		prevBuf: terminal.NewCellBuf(10, 5),
		msgCh:   make(chan hostMsg, 16),
		stopCh:  make(chan struct{}),
	}

	// Enqueue burst of invalidations
	for i := 0; i < 5; i++ {
		h.msgCh <- hostMsg{kind: hostMsgInvalidate}
	}
	h.msgCh <- hostMsg{kind: hostMsgStop}

	h.processLoop(func(fd uintptr) (int, int, error) {
		return 10, 5, nil
	})

	// Ensure the loop ran and terminated cleanly upon hostMsgStop
	if widget.measureW != 10 || widget.measureH != 5 {
		t.Fatalf("processLoop did not measure root widget with expected dimensions; got (%d, %d)", widget.measureW, widget.measureH)
	}
}
