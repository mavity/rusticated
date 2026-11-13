package widgets

import (
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type Button struct {
	Label    string
	Hotkey   string
	Disabled bool
	OnPress  func()
	OnClick  func()
}

func (b *Button) Activate() {
	if b == nil || b.Disabled {
		return
	}
	if b.OnClick != nil {
		b.OnClick()
	}
	if b.OnPress != nil {
		b.OnPress()
	}
}

type ButtonStyles struct {
	Normal   terminal.Style
	Focused  terminal.Style
	Disabled terminal.Style
}

type ButtonRow struct {
	Buttons []Button
	Items   []*Button
	Index   int
	Styles  ButtonStyles
}

func NewButtonRow(buttons []Button) *ButtonRow {
	return &ButtonRow{Buttons: append([]Button(nil), buttons...)}
}

func NewButtonPtrRow(buttons ...*Button) *ButtonRow {
	return &ButtonRow{Items: append([]*Button(nil), buttons...)}
}

type buttonPlacement struct {
	index int
	x     int
	y     int
	text  string
	width int
}

func (w *ButtonRow) buttonCount() int {
	if w == nil {
		return 0
	}
	if len(w.Items) > 0 {
		return len(w.Items)
	}
	return len(w.Buttons)
}

func (w *ButtonRow) buttonAt(index int) *Button {
	if w == nil || index < 0 {
		return nil
	}
	if len(w.Items) > 0 {
		if index >= len(w.Items) {
			return nil
		}
		return w.Items[index]
	}
	if index >= len(w.Buttons) {
		return nil
	}
	return &w.Buttons[index]
}

func (w *ButtonRow) layoutButtons(maxWidth int) ([]buttonPlacement, Size) {
	count := w.buttonCount()
	if count == 0 {
		return nil, Size{}
	}
	placements := make([]buttonPlacement, 0, count)
	x, y := 0, 0
	maxRowW := 0
	for i := 0; i < count; i++ {
		button := w.buttonAt(i)
		if button == nil {
			continue
		}
		text := "[ " + button.Label + " ]"
		textWidth := runewidth.StringWidth(text)
		if maxWidth > 0 && textWidth > maxWidth {
			text = "[" + button.Label + "]"
			textWidth = runewidth.StringWidth(text)
		}
		if maxWidth > 0 && x > 0 && x+textWidth > maxWidth {
			x = 0
			y++
		}
		drawWidth := textWidth
		if maxWidth > 0 && x+drawWidth > maxWidth {
			drawWidth = max(0, maxWidth-x)
		}
		placements = append(placements, buttonPlacement{
			index: i,
			x:     x,
			y:     y,
			text:  text,
			width: drawWidth,
		})
		if x+drawWidth > maxRowW {
			maxRowW = x + drawWidth
		}
		x += textWidth + 2
	}
	if len(placements) == 0 {
		return nil, Size{}
	}
	if maxWidth > 0 {
		totalRows := y + 1
		rowWidths := make([]int, totalRows)
		for _, p := range placements {
			if end := p.x + p.width; end > rowWidths[p.y] {
				rowWidths[p.y] = end
			}
		}
		for i := range placements {
			rowW := rowWidths[placements[i].y]
			if maxWidth > rowW {
				placements[i].x += (maxWidth - rowW) / 2
			}
		}
	}
	return placements, Size{W: maxRowW, H: y + 1}
}

func (w *ButtonRow) Measure(c Constraints) Size {
	_, size := w.layoutButtons(c.MaxW)
	if c.MaxW > 0 && size.W > c.MaxW {
		size.W = c.MaxW
	}
	if c.MaxH > 0 && size.H > c.MaxH {
		size.H = c.MaxH
	}
	return size
}

func (w *ButtonRow) Render(r Rect, ctx RenderContext) string {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if w == nil || width == 0 || height == 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	normal := styleOr(w.Styles.Normal, terminal.NewStyle(0xE5E5E5, 0x3D4856, 0))
	focused := styleOr(w.Styles.Focused, terminal.NewStyle(0x1A1F26, 0xFFD700, terminal.CharBold))
	disabled := styleOr(w.Styles.Disabled, terminal.NewStyle(0x8A95A5, 0, terminal.CharDim|terminal.CharResetBackgroundDefault))
	placements, _ := w.layoutButtons(width)
	for _, p := range placements {
		if p.y >= height {
			break
		}
		button := w.buttonAt(p.index)
		if button == nil {
			continue
		}
		style := normal
		if button.Disabled {
			style = disabled
		} else if p.index == w.Index && ctx.Focused {
			style = focused
		}
		col := p.x
		labelHotkey := -1
		if button.Hotkey != "" {
			prefixOffset := 2
			if strings.HasPrefix(p.text, "[") && !strings.HasPrefix(p.text, "[ ") {
				prefixOffset = 1
			}
			for j, r := range []rune(button.Label) {
				if strings.EqualFold(string(r), button.Hotkey) {
					labelHotkey = j + prefixOffset
					break
				}
			}
		}
		for textIndex, r := range []rune(p.text) {
			rw := runeWidth(r)
			if col+rw > width {
				break
			}
			cellStyle := style
			if textIndex == labelHotkey {
				cellStyle = style.AddAttribute(terminal.CharUnderline)
			}
			buf.Set(col, p.y, terminal.Cell{Grapheme: string(r), Style: cellStyle})
			for n := 1; n < rw; n++ {
				buf.Set(col+n, p.y, terminal.Cell{Grapheme: "", Style: cellStyle})
			}
			col += rw
		}
	}
	return ui.ANSIFrame(buf, CursorPos{})
}

func (w *ButtonRow) HandleEvent(event input.Event) bool {
	if w == nil || w.buttonCount() == 0 {
		return false
	}
	ev, ok := keyEvent(event)
	if !ok {
		return false
	}
	switch strings.ToLower(ev.String()) {
	case "left", "up", "shift+tab":
		w.move(-1)
		return true
	case "right", "down", "tab":
		w.move(1)
		return true
	case "enter", "space", " ":
		w.activate(w.Index)
		return true
	default:
		return w.ActivateHotkey(ev)
	}
}

func (w *ButtonRow) ActivateHotkey(ev input.KeyPressEvent) bool {
	if w == nil || w.buttonCount() == 0 {
		return false
	}
	key := strings.ToLower(ev.String())
	if ev.Text != "" {
		key = strings.ToLower(ev.Text)
	}
	count := w.buttonCount()
	for i := 0; i < count; i++ {
		button := w.buttonAt(i)
		if button != nil && !button.Disabled && button.Hotkey != "" && key == strings.ToLower(button.Hotkey) {
			w.Index = i
			w.activate(i)
			return true
		}
	}
	return false
}

func (w *ButtonRow) move(step int) {
	count := w.buttonCount()
	if count == 0 {
		return
	}
	for n := 0; n < count; n++ {
		w.Index = (w.Index + step + count) % count
		if button := w.buttonAt(w.Index); button != nil && !button.Disabled {
			return
		}
	}
}

func (w *ButtonRow) activate(index int) {
	button := w.buttonAt(index)
	if button == nil || button.Disabled {
		return
	}
	button.Activate()
}
