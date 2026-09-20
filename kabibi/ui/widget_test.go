package ui

import (
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type widgetStub struct {
	measure   Size
	render    terminal.CellBuf
	lastEvent input.Event
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

func (w *widgetStub) HandleEvent(e input.Event) bool { w.lastEvent = e; return false }

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

	// Test HandleEvent with key press event
	keyEvent := input.KeyPressEvent(input.Key{Code: 'a', Text: "a"})
	if w.HandleEvent(keyEvent) {
		t.Fatal("HandleEvent should return false for no-op stub")
	}
	if ke, ok := w.lastEvent.(input.KeyPressEvent); !ok || ke.Code != 'a' {
		t.Fatalf("HandleEvent did not record key event: got %v", w.lastEvent)
	}

	// Test HandleEvent with mouse click event
	mouseEvent := input.MouseClickEvent{X: 3, Y: 4, Button: input.MouseButton(1)}
	if w.HandleEvent(mouseEvent) {
		t.Fatal("HandleEvent should return false for no-op stub")
	}
	if me, ok := w.lastEvent.(input.MouseClickEvent); !ok || me.X != 3 || me.Y != 4 {
		t.Fatalf("HandleEvent did not record mouse event: got %v", w.lastEvent)
	}
}
