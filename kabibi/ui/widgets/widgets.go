package widgets

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type (
	Constraints   = ui.Constraints
	Size          = ui.Size
	Rect          = ui.Rect
	CellBuf       = terminal.CellBuf
	CursorPos     = ui.CursorPos
	RenderContext = ui.RenderContext
)

func runeWidth(r rune) int {
	width := runewidth.RuneWidth(r)
	if width < 1 {
		return 1
	}
	return width
}

func runeColumn(value []rune, index int) int {
	col := 0
	for i := 0; i < index && i < len(value); i++ {
		col += runeWidth(value[i])
	}
	return col
}

func styleOr(style, fallback terminal.Style) terminal.Style {
	if style == 0 {
		return fallback
	}
	return style
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func keyEvent(event input.Event) (input.KeyPressEvent, bool) {
	switch ev := event.(type) {
	case input.KeyPressEvent:
		return ev, true
	case *input.KeyPressEvent:
		if ev != nil {
			return *ev, true
		}
	}
	return input.KeyPressEvent{}, false
}

func writeText(buf *CellBuf, x, y int, text string, style terminal.Style) {
	col := x
	for _, r := range text {
		rw := runeWidth(r)
		if col+rw > buf.Width || y < 0 || y >= buf.Height {
			return
		}
		buf.Set(col, y, terminal.Cell{Grapheme: string(r), Style: style})
		for n := 1; n < rw; n++ {
			buf.Set(col+n, y, terminal.Cell{Grapheme: "", Style: style})
		}
		col += rw
	}
}

func truncateEnd(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= maxWidth {
		return text
	}
	if maxWidth == 1 {
		return "…"
	}
	var b strings.Builder
	width := 0
	for _, r := range text {
		rw := runeWidth(r)
		if width+rw > maxWidth-1 {
			break
		}
		b.WriteRune(r)
		width += rw
	}
	return b.String() + "…"
}

func middleTruncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(text)
	leftWidth := (width - 1) / 2
	rightWidth := width - 1 - leftWidth
	var left, right strings.Builder
	for _, r := range runes {
		if runewidth.StringWidth(left.String())+runeWidth(r) > leftWidth {
			break
		}
		left.WriteRune(r)
	}
	for i := len(runes) - 1; i >= 0; i-- {
		r := runes[i]
		if runewidth.StringWidth(right.String())+runeWidth(r) > rightWidth {
			break
		}
		right.WriteRune(r)
	}
	return left.String() + "…" + reverse(right.String())
}

func reverse(value string) string {
	runes := []rune(value)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func percent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	return clamp(int(done*100/total), 0, 100)
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := "KMGTPE"
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return fmt.Sprintf("%.1f %cB", float64(value)/float64(div), units[exp])
}

var (
	_ ui.Widget = (*TextInput)(nil)
	_ ui.Widget = (*Checkbox)(nil)
	_ ui.Widget = (*ButtonRow)(nil)
	_ ui.Widget = (*ProgressBar)(nil)
	_ ui.Widget = (*Label)(nil)
	_ ui.Widget = (*TextBlock)(nil)
	_ ui.Widget = (*Grid)(nil)
	_ ui.Widget = (*Frame)(nil)
)
