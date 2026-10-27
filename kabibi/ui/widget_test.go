package ui

import (
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type noopWidget struct{}

func (noopWidget) Measure(c Constraints) Size { return Size{W: c.MaxW, H: c.MaxH} }

func (noopWidget) Render(r terminal.Rect, ctx RenderContext) string {
	buf := NewCellBuf(r.W, r.H)
	return ANSIFrame(buf, CursorPos{X: 0, Y: 0, Visible: false})
}

func (noopWidget) HandleEvent(e input.Event) bool { return false }

func TestNoopWidget_MeasuresExactBounds(t *testing.T) {
	want := Size{W: 7, H: 9}
	if got := (noopWidget{}).Measure(Constraints{MaxW: 7, MaxH: 9}); got != want {
		t.Fatalf("noopWidget.Measure() = %#v, want %#v", got, want)
	}
}

func TestANSIFrameRoundTripsWideGlyphAtRightMargin(t *testing.T) {
	buf := NewCellBuf(4, 1)
	style := terminal.NewStyle(0xFFFFFF, 0x000000, 0)
	buf.Set(2, 0, terminal.Cell{Grapheme: "界", Style: style})
	buf.Set(3, 0, terminal.Cell{Grapheme: "", Style: style})

	text := ANSIFrame(buf, CursorPos{})
	frame, _, _, err := terminal.NewTerminal(4, 1).RenderANSIForComposition(text, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := frame.Get(2, 0).Grapheme; got != "界" {
		t.Fatalf("right-margin wide glyph = %q, want 界", got)
	}
	if got := frame.Get(3, 0).Grapheme; got != "" {
		t.Fatalf("wide-glyph continuation = %q, want empty", got)
	}
}
