package ui

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type recordingWidget struct {
	measureW int
	measureH int
	cursor   CursorPos
	buf      terminal.CellBuf
	handled  bool
}

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

func (w *recordingWidget) HandleEvent(input.Event) bool {
	w.handled = true
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
