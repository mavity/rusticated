package widgets

import (
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type Label struct {
	Text  string
	Style terminal.Style
}

func (w *Label) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxH > 0 && c.MaxH < 1 {
		return Size{}
	}
	width := runewidth.StringWidth(w.Text)
	if c.MaxW > 0 && width > c.MaxW {
		width = c.MaxW
	}
	return Size{W: max(0, width), H: 1}
}

func (w *Label) Render(r Rect, _ RenderContext) string {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if w == nil || width == 0 || height == 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	style := styleOr(w.Style, terminal.NewStyle(0, 0, terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault))
	writeText(&buf, 0, 0, truncateEnd(w.Text, width), style)
	return ui.ANSIFrame(buf, CursorPos{})
}

func (w *Label) HandleEvent(input.Event) bool { return false }

type TextBlock struct {
	Text  string
	Style terminal.Style
}

type Text = TextBlock

func (w *TextBlock) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxH > 0 && c.MaxH < 1 {
		return Size{}
	}
	wrapWidth := c.MaxW
	if wrapWidth <= 0 {
		for _, line := range strings.Split(w.Text, "\n") {
			if lw := runewidth.StringWidth(line); lw > wrapWidth {
				wrapWidth = lw
			}
		}
		if wrapWidth <= 0 {
			wrapWidth = 1
		}
	}
	lines := WrapText(w.Text, wrapWidth)
	maxW := 0
	for _, line := range lines {
		if lw := runewidth.StringWidth(line); lw > maxW {
			maxW = lw
		}
	}
	if c.MaxW > 0 && maxW > c.MaxW {
		maxW = c.MaxW
	}
	height := len(lines)
	if c.MaxH > 0 && height > c.MaxH {
		height = c.MaxH
	}
	return Size{W: max(0, maxW), H: max(0, height)}
}

func (w *TextBlock) Render(r Rect, _ RenderContext) string {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if w == nil || width == 0 || height == 0 {
		return ui.ANSIFrame(buf, CursorPos{})
	}
	style := styleOr(w.Style, terminal.NewStyle(0, 0, terminal.CharResetForegroundDefault|terminal.CharResetBackgroundDefault))
	lines := WrapText(w.Text, width)
	for y := 0; y < len(lines) && y < height; y++ {
		line := lines[y]
		if y == height-1 && len(lines) > height {
			line = truncateEnd(line+"…", width)
		}
		writeText(&buf, 0, y, truncateEnd(line, width), style)
	}
	return ui.ANSIFrame(buf, CursorPos{})
}

func (w *TextBlock) HandleEvent(input.Event) bool { return false }

func WrapText(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var rows []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			rows = append(rows, "")
			continue
		}
		line := ""
		for _, word := range words {
			if line == "" {
				line = truncateEnd(word, width)
				continue
			}
			candidate := line + " " + word
			if runewidth.StringWidth(candidate) <= width {
				line = candidate
			} else {
				rows = append(rows, line)
				line = truncateEnd(word, width)
			}
		}
		rows = append(rows, line)
	}
	return rows
}
