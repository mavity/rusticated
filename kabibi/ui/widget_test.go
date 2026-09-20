package ui

import (
	"testing"

	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type widgetStub struct {
	measure   Size
	render    terminal.CellBuf
	lastKey   KeyEvent
	lastMouse MouseEvent
}

func (w *widgetStub) Measure(c Constraints) Size {
	if c.MaxW > 0 {
		w.measure.W = c.MaxW
	}
	if c.MaxH > 0 {
		w.measure.H = c.MaxH
	}
	return w.measure
}

func (w *widgetStub) Render(r terminal.Rect, ctx RenderContext) (terminal.CellBuf, CursorPos) {
	return w.render, CursorPos{}
}

func (w *widgetStub) HandleKey(e KeyEvent) bool     { w.lastKey = e; return false }
func (w *widgetStub) HandleMouse(e MouseEvent) bool { w.lastMouse = e; return false }

func TestWidgetContract(t *testing.T) {
	var _ Widget = (*widgetStub)(nil)

	w := &widgetStub{measure: Size{W: 40, H: 10}, render: terminal.NewCellBuf(2, 2)}

	got := w.Measure(Constraints{MaxW: 40, MaxH: 10})
	if got.W != 40 || got.H != 10 {
		t.Fatalf("Measure returned unexpected size: %#v", got)
	}

	buf, cur := w.Render(terminal.Rect{X: 0, Y: 0, W: 40, H: 10}, RenderContext{})
	if buf.Width != 2 || buf.Height != 2 {
		t.Fatalf("Render returned unexpected CellBuf dimensions: %dx%d", buf.Width, buf.Height)
	}
	if cur.Visible {
		t.Fatal("stub cursor should default to hidden")
	}

	if w.HandleKey(KeyEvent{Rune: 'a'}) {
		t.Fatal("HandleKey should return false for no-op stub")
	}
	if w.lastKey.Rune != 'a' {
		t.Fatalf("HandleKey did not record event: got %v", w.lastKey)
	}

	if w.HandleMouse(MouseEvent{X: 3, Y: 4, Action: MousePress}) {
		t.Fatal("HandleMouse should return false for no-op stub")
	}
	if w.lastMouse.X != 3 || w.lastMouse.Y != 4 {
		t.Fatalf("HandleMouse did not record event: got %v", w.lastMouse)
	}
}
