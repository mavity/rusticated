package widgets

import (
	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type FrameStyles struct {
	Border terminal.Style
	Title  terminal.Style
	Fill   terminal.Style
}

type Frame struct {
	Title    string
	Child    ui.Widget
	PaddingX int
	PaddingY int
	Styles   FrameStyles

	compositor *terminal.Terminal
}

func (w *Frame) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if (c.MaxW > 0 && c.MaxW < 2) || (c.MaxH > 0 && c.MaxH < 2) {
		if w.Child == nil {
			return Size{W: max(0, c.MaxW), H: max(0, c.MaxH)}
		}
		return w.Child.Measure(c)
	}
	padX := max(0, w.PaddingX)
	padY := max(0, w.PaddingY)
	borderW := 2 + padX*2
	borderH := 2 + padY*2
	if c.MaxW > 0 && c.MaxW <= borderW {
		borderW = 2
	}
	if c.MaxH > 0 && c.MaxH <= borderH {
		borderH = 2
	}
	childMaxW := 0
	if c.MaxW > 0 {
		childMaxW = max(1, c.MaxW-borderW)
	}
	childMaxH := 0
	if c.MaxH > 0 {
		childMaxH = max(1, c.MaxH-borderH)
	}
	var childSize Size
	if w.Child != nil {
		childSize = w.Child.Measure(Constraints{MaxW: childMaxW, MaxH: childMaxH})
	}
	titleW := 0
	if w.Title != "" {
		titleW = runewidth.StringWidth(w.Title) + 4
	}
	totalW := max(childSize.W+borderW, titleW)
	totalH := childSize.H + borderH
	if c.MaxW > 0 && totalW > c.MaxW {
		totalW = c.MaxW
	}
	if c.MaxH > 0 && totalH > c.MaxH {
		totalH = c.MaxH
	}
	return Size{W: max(0, totalW), H: max(0, totalH)}
}

func (w *Frame) Render(r Rect, ctx RenderContext) string {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if w == nil || width == 0 || height == 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	if w.Styles.Fill != 0 {
		buf.Fill(Rect{W: width, H: height}, terminal.Cell{Grapheme: " ", Style: w.Styles.Fill})
	}
	if width < 2 || height < 2 {
		if w.Child != nil {
			childANSI := w.Child.Render(Rect{W: width, H: height}, ctx)
			cur := ctx.CompositeANSI(&buf, w.ansiTerminal(), Rect{W: width, H: height}, childANSI)
			return ui.ANSIFrame(buf, cur)
		}
		return ui.ANSIFrame(buf, CursorPos{})
	}
	borderStyle := styleOr(w.Styles.Border, terminal.NewStyle(0x8A95A5, 0, terminal.CharResetBackgroundDefault))
	titleStyle := styleOr(w.Styles.Title, terminal.NewStyle(0xFFD700, 0, terminal.CharBold|terminal.CharResetBackgroundDefault))
	for x := 1; x < width-1; x++ {
		buf.Set(x, 0, terminal.Cell{Grapheme: "─", Style: borderStyle})
		buf.Set(x, height-1, terminal.Cell{Grapheme: "─", Style: borderStyle})
	}
	for y := 1; y < height-1; y++ {
		buf.Set(0, y, terminal.Cell{Grapheme: "│", Style: borderStyle})
		buf.Set(width-1, y, terminal.Cell{Grapheme: "│", Style: borderStyle})
	}
	buf.Set(0, 0, terminal.Cell{Grapheme: "╭", Style: borderStyle})
	buf.Set(width-1, 0, terminal.Cell{Grapheme: "╮", Style: borderStyle})
	buf.Set(0, height-1, terminal.Cell{Grapheme: "╰", Style: borderStyle})
	buf.Set(width-1, height-1, terminal.Cell{Grapheme: "╯", Style: borderStyle})
	if w.Title != "" && width > 4 {
		titleText := " " + truncateEnd(w.Title, width-4) + " "
		writeText(&buf, 2, 0, titleText, titleStyle)
	}
	padX := max(0, w.PaddingX)
	padY := max(0, w.PaddingY)
	if width <= 2+padX*2 {
		padX = 0
	}
	if height <= 2+padY*2 {
		padY = 0
	}
	innerRect := Rect{
		X: 1 + padX,
		Y: 1 + padY,
		W: max(0, width-2-padX*2),
		H: max(0, height-2-padY*2),
	}
	var cursor CursorPos
	if w.Child != nil && innerRect.W > 0 && innerRect.H > 0 {
		childANSI := w.Child.Render(Rect{W: innerRect.W, H: innerRect.H}, ctx)
		cursor = ctx.CompositeANSI(&buf, w.ansiTerminal(), innerRect, childANSI)
	}
	return ui.ANSIFrame(buf, cursor)
}

func (w *Frame) HandleEvent(event input.Event) bool {
	if w == nil || w.Child == nil {
		return false
	}
	return w.Child.HandleEvent(event)
}

func (w *Frame) SetFocusedWidget(target ui.Widget) bool {
	if w == nil || w.Child == nil || target == nil {
		return false
	}
	if w.Child == target {
		return true
	}
	return setFocusedDescendant(w.Child, target)
}

func (w *Frame) ansiTerminal() *terminal.Terminal {
	if w.compositor == nil {
		w.compositor = terminal.NewTerminal(1, 1)
	}
	return w.compositor
}
