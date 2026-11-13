package widgets

import (
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type CheckboxStyles struct {
	Normal   terminal.Style
	Focused  terminal.Style
	Disabled terminal.Style
}

type Checkbox struct {
	Label     string
	Hotkey    string
	Checked   bool
	Disabled  bool
	Styles    CheckboxStyles
	OnToggled func(bool)
}

func (w *Checkbox) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxH > 0 && c.MaxH < 1 {
		return Size{}
	}
	width := runewidth.StringWidth("[ ] " + w.Label)
	if c.MaxW > 0 && width > c.MaxW {
		width = c.MaxW
	}
	return Size{W: max(0, width), H: 1}
}

func (w *Checkbox) Render(r Rect, ctx RenderContext) string {
	buf := terminal.NewCellBuf(max(0, r.W), 1)
	if w == nil || r.W <= 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	style := styleOr(w.Styles.Normal, terminal.NewStyle(0xE5E5E5, 0, terminal.CharResetBackgroundDefault))
	if w.Disabled {
		style = styleOr(w.Styles.Disabled, terminal.NewStyle(0x8A95A5, 0, terminal.CharDim|terminal.CharResetBackgroundDefault))
	} else if ctx.Focused {
		style = styleOr(w.Styles.Focused, terminal.NewStyle(0x1A1F26, 0xFFD700, terminal.CharBold))
	}
	check := " "
	if w.Checked {
		check = "x"
	}
	writeText(&buf, 0, 0, truncateEnd("["+check+"] "+w.Label, buf.Width), style)
	return ui.ANSIFrame(buf, CursorPos{})
}

func (w *Checkbox) HandleEvent(event input.Event) bool {
	if w == nil || w.Disabled {
		return false
	}
	ev, ok := keyEvent(event)
	if !ok {
		return false
	}
	key := strings.ToLower(ev.String())
	if key != "space" && key != " " {
		if w.Hotkey == "" || !strings.EqualFold(key, w.Hotkey) {
			return false
		}
	}
	w.Checked = !w.Checked
	if w.OnToggled != nil {
		w.OnToggled(w.Checked)
	}
	return true
}
