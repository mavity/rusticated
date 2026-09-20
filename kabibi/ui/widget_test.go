package ui

import (
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type noopWidget struct{}

func (noopWidget) Measure(c Constraints) Size { return Size{W: c.MaxW, H: c.MaxH} }

func (noopWidget) Render(r terminal.Rect, ctx RenderContext) (terminal.CellBuf, CursorPos) {
	buf := NewCellBuf(r.W, r.H)
	return buf, CursorPos{X: 0, Y: 0, Visible: false}
}

func (noopWidget) HandleEvent(e input.Event) bool { return false }

func TestNoopWidget_MeasuresExactBounds(t *testing.T) {
	want := Size{W: 7, H: 9}
	if got := (noopWidget{}).Measure(Constraints{MaxW: 7, MaxH: 9}); got != want {
		t.Fatalf("noopWidget.Measure() = %#v, want %#v", got, want)
	}
}
