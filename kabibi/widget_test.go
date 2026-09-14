package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type widgetStub struct {
	measure Size
	layout  Rect
	render  CellBuf
	lastKey tea.KeyMsg
	lastMsg tea.MouseMsg
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

func (w *widgetStub) Layout(r Rect) {
	w.layout = r
}

func (w *widgetStub) Render() CellBuf {
	return w.render
}

func (w *widgetStub) HandleKey(msg tea.KeyMsg) tea.Cmd {
	w.lastKey = msg
	return nil
}

func (w *widgetStub) HandleMouse(msg tea.MouseMsg) tea.Cmd {
	w.lastMsg = msg
	return nil
}

func TestWidgetContract(t *testing.T) {
	var _ Widget = (*widgetStub)(nil)
	var _ InputWidget = (*widgetStub)(nil)

	w := &widgetStub{measure: Size{W: 40, H: 10}, render: NewCellBuf(2, 2)}
	if got := w.Measure(Constraints{MaxW: 40, MaxH: 10}); got.W != 40 || got.H != 10 {
		t.Fatalf("Measure returned unexpected size: %#v", got)
	}

	w.Layout(Rect{X: 1, Y: 2, W: 5, H: 6})
	if w.layout != (Rect{X: 1, Y: 2, W: 5, H: 6}) {
		t.Fatalf("Layout did not remember the supplied bounds: %#v", w.layout)
	}

	if got := w.Render(); got.Width != 2 || got.Height != 2 {
		t.Fatalf("Render returned unexpected CellBuf: %#v", got)
	}

	if cmd := w.HandleKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("HandleKey should return nil for a no-op key handler")
	}
	if cmd := w.HandleMouse(tea.MouseMsg{X: 3, Y: 4}); cmd != nil {
		t.Fatal("HandleMouse should return nil for a no-op mouse handler")
	}
}
