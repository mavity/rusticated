package ui

import (
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type widgetStub struct {
	measure   Size
	render    terminal.CellBuf
	lastKey   input.KeyPressEvent
	lastMouse input.MouseClickEvent
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

func (w *widgetStub) HandleKey(e input.KeyPressEvent) bool     { w.lastKey = e; return false }
func (w *widgetStub) HandleMouse(e input.MouseEvent) bool {
	// Convert to concrete type for storage
	if click, ok := e.(input.MouseClickEvent); ok {
		w.lastMouse = click
	}
	return false
}

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

	// Create a key press event with Code 'a'
	keyEvent := input.KeyPressEvent(input.Key{Code: 'a', Text: "a"})
	if w.HandleKey(keyEvent) {
		t.Fatal("HandleKey should return false for no-op stub")
	}
	if w.lastKey.Code != 'a' {
		t.Fatalf("HandleKey did not record event: got %v", w.lastKey)
	}

	// Create a mouse click event at (3, 4)
	mouseEvent := input.MouseClickEvent{X: 3, Y: 4, Button: input.MouseButton(1)}
	if w.HandleMouse(mouseEvent) {
		t.Fatal("HandleMouse should return false for no-op stub")
	}
	if w.lastMouse.X != 3 || w.lastMouse.Y != 4 {
		t.Fatalf("HandleMouse did not record event: got %v", w.lastMouse)
	}
}
