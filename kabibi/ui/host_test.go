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
			buf.Set(x, y, terminal.Cell{R: 'A'})
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
	h := NewHost(root)
	if got, want := h.root, root; got != want {
		t.Fatalf("NewHost().root = %#v, want %#v", got, want)
	}
}

func TestNewHost_InitialDirtyFlagIsFalse(t *testing.T) {
	h := NewHost(noopWidget{})
	if h.dirty.Load() {
		t.Fatal("NewHost() should start with dirty == false")
	}
}

func TestStop_ClosesChannelOnce(t *testing.T) {
	h := NewHost(noopWidget{})
	h.Stop()
	if _, ok := <-h.stopCh; ok {
		t.Fatal("Stop() should close stopCh")
	}
}

func TestWriteToScrollback_QueuesText(t *testing.T) {
	h := NewHost(noopWidget{})
	h.WriteToScrollback("hello")
	if got, want := len(h.scrollbackQueue), 1; got != want {
		t.Fatalf("len(scrollbackQueue) = %d, want %d", got, want)
	}
	if got, want := h.scrollbackQueue[0], "hello"; got != want {
		t.Fatalf("scrollbackQueue[0] = %q, want %q", got, want)
	}
}

func TestWriteToScrollback_ConcurrentWritesAreAllPreserved(t *testing.T) {
	h := NewHost(noopWidget{})
	for i := 0; i < 20; i++ {
		h.WriteToScrollback("hello")
	}
	if got := len(h.scrollbackQueue); got != 20 {
		t.Fatalf("len(scrollbackQueue) = %d, want 20", got)
	}
}

func TestReadInputLoop_ParsesAndDispatchesEvents(t *testing.T) {
	pr, pw := io.Pipe()
	widget := &recordingWidget{}
	h := NewHost(widget)

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

	if !widget.handled {
		t.Fatal("readInputLoop() did not dispatch an event to the widget")
	}
	key, ok := widget.lastEvent.(input.KeyPressEvent)
	if !ok {
		t.Fatalf("readInputLoop() dispatched %T, want input.KeyPressEvent", widget.lastEvent)
	}
	if got, want := key.Keystroke(), "a"; got != want {
		t.Fatalf("keystroke = %q, want %q", got, want)
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
	if !strings.Contains(got, "\x1b[H") || !strings.Contains(got, "\x1b[24;1H") {
		t.Fatalf("scrollback output = %q, want ansi cursor movement and viewport reset", got)
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
	); err != nil {
		t.Fatalf("runCore() error = %v", err)
	}

	if !called {
		t.Fatal("runCore() did not call the injected makeRawFunc")
	}
	if !restored {
		t.Fatal("runCore() did not call the injected restoreFunc")
	}
}

func TestRunCore_PropagatesMakeRawError(t *testing.T) {
	h := &Host{root: noopWidget{}, out: bufio.NewWriter(io.Discard), stopCh: make(chan struct{})}
	expected := errors.New("raw mode failed")

	err := h.runCore(
		func(uintptr) (*xterm.State, error) { return nil, expected },
		func(uintptr, *xterm.State) error {
			t.Fatal("restoreFunc should not run when makeRaw fails")
			return nil
		},
	)
	if !errors.Is(err, expected) {
		t.Fatalf("runCore() error = %v, want %v", err, expected)
	}
	if h.prevTermState != nil {
		t.Fatal("runCore() should leave prevTermState nil when makeRaw fails")
	}
}

func TestReadInputLoop_ExitsOnReadError(t *testing.T) {
	h := NewHost(noopWidget{})
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
			func(uintptr) (*xterm.State, error) {
				panic("widget explosion")
			},
			func(uintptr, *xterm.State) error {
				restored = true
				return nil
			},
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
		func(uintptr) (*xterm.State, error) {
			panic("widget explosion")
		},
		func(uintptr, *xterm.State) error {
			restored = true
			return nil
		},
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
		buf.Cells()[i] = terminal.Cell{R: 'A'}
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
		curr.Cells()[i] = terminal.Cell{R: 'A'}
		prev.Cells()[i] = terminal.Cell{R: 'A'}
	}
	curr.Set(1, 1, terminal.Cell{R: 'B'})
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
	h.currBuf.Set(0, 0, terminal.Cell{R: 'A'})
	h.emitDirtyRegion(0, 0)
	got := out.String()
	if !strings.Contains(got, "\x1b[1;1H") || !strings.Contains(got, "\x1b[?25h") || !strings.Contains(got, "A") {
		t.Fatalf("emitDirtyRegion() output = %q, want cursor movement + visible cursor + data", got)
	}
}
