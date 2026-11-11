package widgets

import (
	"fmt"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type ProgressBarStyles struct {
	Filled   terminal.Style
	Empty    terminal.Style
	Label    terminal.Style
	Metadata terminal.Style
}

type ProgressBar struct {
	Current string
	Done    int64
	Total   int64
	Rate    int64
	Styles  ProgressBarStyles
}

func (w *ProgressBar) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	height := 3
	if w.Current == "" {
		height = 1
	}
	if c.MaxH > 0 && c.MaxH < height {
		height = c.MaxH
	}
	return Size{W: max(0, c.MaxW), H: height}
}

func (w *ProgressBar) Render(r Rect, _ RenderContext) string {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if w == nil || width == 0 || height == 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	labelStyle := styleOr(w.Styles.Label, terminal.NewStyle(0xE5E5E5, 0, terminal.CharResetBackgroundDefault))
	filledStyle := styleOr(w.Styles.Filled, terminal.NewStyle(0xFFD700, 0, terminal.CharResetBackgroundDefault))
	emptyStyle := styleOr(w.Styles.Empty, terminal.NewStyle(0x8A95A5, 0, terminal.CharResetBackgroundDefault))
	metaStyle := styleOr(w.Styles.Metadata, terminal.NewStyle(0x8A95A5, 0, terminal.CharResetBackgroundDefault))
	if height == 1 && w.Current == "" {
		pct := percent(w.Done, w.Total)
		suffix := fmt.Sprintf(" %3d%%", pct)
		barWidth := max(0, width-runewidth.StringWidth(suffix))
		filled := barWidth * pct / 100
		for x := 0; x < barWidth; x++ {
			ch := '░'
			style := emptyStyle
			if x < filled {
				ch = '█'
				style = filledStyle
			}
			buf.Set(x, 0, terminal.Cell{Grapheme: string(ch), Style: style})
		}
		writeText(&buf, barWidth, 0, suffix, labelStyle)
		return ui.ANSIFrame(buf, CursorPos{})
	}
	writeText(&buf, 0, 0, middleTruncate(w.Current, width), labelStyle)
	if height > 1 {
		pct := percent(w.Done, w.Total)
		suffix := fmt.Sprintf(" %3d%%", pct)
		barWidth := max(0, width-runewidth.StringWidth(suffix))
		filled := barWidth * pct / 100
		for x := 0; x < barWidth; x++ {
			ch := '░'
			style := emptyStyle
			if x < filled {
				ch = '█'
				style = filledStyle
			}
			buf.Set(x, 1, terminal.Cell{Grapheme: string(ch), Style: style})
		}
		writeText(&buf, barWidth, 1, suffix, labelStyle)
	}
	if height > 2 {
		meta := fmt.Sprintf("%s / %s  %s/s", formatBytes(w.Done), formatBytes(w.Total), formatBytes(w.Rate))
		writeText(&buf, 0, 2, middleTruncate(meta, width), metaStyle)
	}
	return ui.ANSIFrame(buf, CursorPos{})
}

func (w *ProgressBar) HandleEvent(input.Event) bool { return false }
