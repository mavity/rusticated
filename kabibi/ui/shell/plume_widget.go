package shell

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// PlumeEntry retains the raw command/output text for a single plume line while
// tracking whether the payload contained ANSI escape sequences.
type PlumeEntry struct {
	Raw    string
	IsANSI bool
}

// PlumeBuffer keeps a bounded managed history of shell lines. The buffer is
// intentionally independent from the Bubble Tea model layer.
type PlumeBuffer struct {
	Entries    []PlumeEntry
	MaxEntries int
	version    uint64
}

func NewPlumeBuffer(maxEntries int) *PlumeBuffer {
	if maxEntries <= 0 {
		maxEntries = 120
	}
	return &PlumeBuffer{MaxEntries: maxEntries}
}

func (b *PlumeBuffer) Append(lines ...string) {
	if b == nil {
		return
	}
	if b.MaxEntries <= 0 {
		b.MaxEntries = 120
	}
	for _, line := range lines {
		b.Entries = append(b.Entries, PlumeEntry{Raw: line, IsANSI: hasANSI(line)})
		if len(b.Entries) > b.MaxEntries {
			b.Entries = b.Entries[1:]
		}
	}
	if len(lines) > 0 {
		b.version++
	}
}

func (b *PlumeBuffer) Lines() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.Entries))
	for _, entry := range b.Entries {
		out = append(out, entry.Raw)
	}
	return out
}

// PlumeSlices groups the managed plume into the three geometry bands used by the
// screen compositor: content above the panels, content obscured by the panels,
// and content at the footer/footer peeking region.
type PlumeSlices struct {
	Exhaust  []CellBuf
	Occluded []CellBuf
	Footer   []CellBuf
}

func LayoutPlume(entries []string, width, panelRows int) PlumeSlices {
	if width <= 0 {
		return PlumeSlices{}
	}
	if panelRows < 1 {
		panelRows = 1
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		line := strings.ReplaceAll(entry, "\n", "")
		line = strings.ReplaceAll(line, "\r", "")
		if line == "" {
			line = " "
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return PlumeSlices{}
	}

	footerStart := len(lines) - panelRows
	if footerStart < 0 {
		footerStart = 0
	}
	occludedStart := footerStart - panelRows
	if occludedStart < 0 {
		occludedStart = 0
	}
	exhaustStart := occludedStart - panelRows
	if exhaustStart < 0 {
		exhaustStart = 0
	}

	out := PlumeSlices{}
	out.Exhaust = renderPlumeSlice(lines[exhaustStart:occludedStart], width)
	out.Occluded = renderPlumeSlice(lines[occludedStart:footerStart], width)
	out.Footer = renderPlumeSlice(lines[footerStart:], width)
	return out
}

func renderPlumeSlice(lines []string, width int) []CellBuf {
	out := make([]CellBuf, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderPlumeLine(line, width))
	}
	return out
}

func renderPlumeLine(line string, width int) CellBuf {
	if width <= 0 {
		return NewCellBuf(0, 0)
	}
	clean := stripANSI(line)
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, "\t", "    ")
	if clean == "" {
		clean = " "
	}
	if runewidth.StringWidth(clean) > width {
		clean = truncateStringToWidth(clean, width)
	}
	buf := NewCellBuf(width, 1)
	buf.Fill(Rect{X: 0, Y: 0, W: width, H: 1}, Cell{BG: colorDarkGray})
	col := 0
	for _, r := range clean {
		w := runewidth.RuneWidth(r)
		if col+w > width {
			break
		}
		buf.Set(col, 0, Cell{R: r, FG: colorWhite, BG: colorDarkGray})
		col += w
	}
	return buf
}

func hasANSI(s string) bool {
	return strings.Contains(s, "\x1b[") || strings.Contains(s, "\x1b]") || strings.Contains(s, "\x1b(") || strings.Contains(s, "\x1b)")
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		switch s[i+1] {
		case '[':
			i += 2
			for i < len(s) {
				if s[i] >= 0x40 && s[i] <= 0x7e {
					break
				}
				i++
			}
		case ']':
			i += 2
			for i < len(s) {
				if s[i] == '\a' {
					break
				}
				if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		case '(', ')', '*', '+', '-', '.', '/':
			i += 2
		default:
			i++
		}
	}
	return b.String()
}

// PlumeWidget renders a bounded shell history viewport using the same managed
// history invariants as the legacy plume while keeping the rendering path in a
// cell buffer instead of raw strings.
type PlumeWidget struct {
	MaxEntries int
	Width      int
	Height     int
	X          int
	Y          int

	scrollTop   int // rows from bottom; 0 = show latest
	buffer      *PlumeBuffer
	cache       CellBuf
	cacheW      int
	cacheH      int
	cacheVer    uint64
	cacheScroll int
}

func NewPlumeWidget() *PlumeWidget {
	return &PlumeWidget{MaxEntries: 120, buffer: NewPlumeBuffer(120)}
}

func (w *PlumeWidget) SetLines(lines []string) {
	if w == nil {
		return
	}
	if w.MaxEntries <= 0 {
		w.MaxEntries = 120
	}
	w.buffer = NewPlumeBuffer(w.MaxEntries)
	if len(lines) > 0 {
		w.buffer.Append(lines...)
	}
	w.scrollTop = 0
}

func (w *PlumeWidget) appendLine(line string) {
	if w == nil {
		return
	}
	if w.buffer == nil {
		w.buffer = NewPlumeBuffer(w.MaxEntries)
	}
	if w.MaxEntries <= 0 {
		w.MaxEntries = 120
	}
	if w.buffer.MaxEntries != w.MaxEntries {
		w.buffer.MaxEntries = w.MaxEntries
	}
	w.buffer.Append(line)
	w.scrollTop = 0
}

func (w *PlumeWidget) Lines() []string {
	if w == nil {
		return nil
	}
	if w.buffer == nil {
		return nil
	}
	return w.buffer.Lines()
}

func (w *PlumeWidget) Measure(c Constraints) Size {
	if w == nil {
		return Size{}
	}
	if c.MaxW <= 0 {
		c.MaxW = 80
	}
	if c.MaxH <= 0 {
		c.MaxH = 10
	}
	h := len(w.Lines())
	if h < 1 {
		h = 1
	}
	if h > c.MaxH {
		h = c.MaxH
	}
	return Size{W: c.MaxW, H: h}
}

func (w *PlumeWidget) Layout(r Rect) {
	if w == nil {
		return
	}
	w.X = r.X
	w.Y = r.Y
	w.Width = r.W
	w.Height = r.H
}

func (w *PlumeWidget) Render() CellBuf {
	if w == nil {
		return NewCellBuf(0, 0)
	}
	if w.Width <= 0 || w.Height <= 0 {
		return NewCellBuf(w.Width, w.Height)
	}
	version := uint64(0)
	if w.buffer != nil {
		version = w.buffer.version
	}
	if w.cacheW == w.Width && w.cacheH == w.Height && w.cacheVer == version && w.cacheScroll == w.scrollTop && w.cache.Width == w.Width && w.cache.Height == w.Height {
		return w.cache
	}

	buf := NewCellBuf(w.Width, w.Height)
	buf.Fill(Rect{X: 0, Y: 0, W: w.Width, H: w.Height}, Cell{BG: colorDarkGray})
	lines := w.Lines()
	if len(lines) == 0 {
		w.cache = buf
		w.cacheW = w.Width
		w.cacheH = w.Height
		w.cacheVer = version
		w.cacheScroll = w.scrollTop
		return buf
	}

	maxScroll := len(lines) - w.Height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if w.scrollTop > maxScroll {
		w.scrollTop = maxScroll
	}
	end := len(lines) - w.scrollTop
	if end < 0 {
		end = 0
	}
	start := end - w.Height
	if start < 0 {
		start = 0
	}
	for y, lineIdx := 0, start; y < w.Height && lineIdx < end; y, lineIdx = y+1, lineIdx+1 {
		row := renderPlumeLine(lines[lineIdx], w.Width)
		for x := 0; x < w.Width; x++ {
			buf.Set(x, y, row.Get(x, 0))
		}
	}
	w.cache = buf
	w.cacheW = w.Width
	w.cacheH = w.Height
	w.cacheVer = version
	w.cacheScroll = w.scrollTop
	return buf
}

// HandleKey handles pgup/pgdown/home/end to scroll the plume history viewport.
func (w *PlumeWidget) HandleKey(msg tea.KeyMsg) tea.Cmd {
	if w == nil {
		return nil
	}
	step := w.Height / 2
	if step < 1 {
		step = 1
	}
	switch msg.String() {
	case "pgup":
		w.scrollTop += step
	case "pgdown":
		if w.scrollTop > step {
			w.scrollTop -= step
		} else {
			w.scrollTop = 0
		}
	case "home":
		w.scrollTop = 1<<31 - 1 // clamped to maxScroll in Render
	case "end":
		w.scrollTop = 0
	}
	return nil
}
func (w *PlumeWidget) HandleMouse(msg tea.MouseMsg) tea.Cmd { return nil }

var _ Widget = (*PlumeWidget)(nil)
var _ InputWidget = (*PlumeWidget)(nil)

func (w *PlumeWidget) formatLine(line string) string {
	return sanitizePlumeLine(line)
}

func sanitizePlumeLine(line string) string {
	clean := stripANSI(line)
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, "\t", "    ")
	if clean == "" {
		return " "
	}
	return clean
}

func plumbEmptyCell() Cell {
	return Cell{FG: lipgloss.Color(""), BG: colorDarkGray}
}
