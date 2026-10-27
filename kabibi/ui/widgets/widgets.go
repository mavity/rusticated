package widgets

import (
	"fmt"
	"strings"
	"sync"

	"github.com/atotto/clipboard"
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

type TextInputStyles struct {
	Text       terminal.Style
	Focused    terminal.Style
	Selected   terminal.Style
	Overflow   terminal.Style
	Label      terminal.Style
	Background terminal.Style
}

type TextInputOptions struct {
	Value      string
	History    []string
	Complete   func(value string, cursor int) (string, int, bool)
	Styles     TextInputStyles
	OnChange   func(value string)
	SelectAll  bool
	AllowPaste bool
}

type TextInput struct {
	value      []rune
	cursor     int
	anchor     int
	selecting  bool
	scrollCol  int
	focused    bool
	history    []string
	historyIdx int
	complete   func(string, int) (string, int, bool)
	styles     TextInputStyles
	onChange   func(string)
	selectAll  bool
	allowPaste bool
	ctx        RenderContext
}

func NewTextInput(opts TextInputOptions) *TextInput {
	w := &TextInput{
		value:      []rune(opts.Value),
		cursor:     len([]rune(opts.Value)),
		focused:    true,
		history:    append([]string(nil), opts.History...),
		historyIdx: -1,
		complete:   opts.Complete,
		styles:     opts.Styles,
		onChange:   opts.OnChange,
		selectAll:  opts.SelectAll,
		allowPaste: opts.AllowPaste,
	}
	if opts.SelectAll && len(w.value) > 0 {
		w.anchor = 0
		w.selecting = true
	}
	return w
}

func (w *TextInput) Value() string {
	if w == nil {
		return ""
	}
	return string(w.value)
}

func (w *TextInput) SetValue(value string) {
	if w == nil {
		return
	}
	w.value = []rune(value)
	w.cursor = len(w.value)
	w.anchor = w.cursor
	w.selecting = false
	w.scrollCol = 0
	w.changed()
}

func (w *TextInput) SetFocused(focused bool) {
	if w == nil || w.focused == focused {
		return
	}
	w.focused = focused
	if !focused {
		w.selecting = false
	}
	w.invalidate()
}

func (w *TextInput) Focused() bool { return w != nil && w.focused }

func (w *TextInput) Measure(c Constraints) Size {
	width := c.MaxW
	if width <= 0 {
		width = 40
	}
	if c.MaxH > 0 && c.MaxH < 1 {
		return Size{}
	}
	return Size{W: width, H: 1}
}

func (w *TextInput) Render(r Rect, ctx RenderContext) (CellBuf, CursorPos) {
	width := r.W
	if width < 0 {
		width = 0
	}
	buf := terminal.NewCellBuf(width, 1)
	if w == nil || width == 0 {
		return buf, CursorPos{}
	}
	w.ctx = ctx
	if width == 1 {
		if len(w.value) > 0 {
			buf.Set(0, 0, terminal.Cell{Grapheme: "…", Style: styleOr(w.styles.Overflow, terminal.NewStyle(0xFFD700, 0x0D1014, terminal.CharBold))})
		}
		return buf, CursorPos{Visible: w.focused && ctx.Focused}
	}

	styles := w.styles
	textStyle := styleOr(styles.Text, terminal.NewStyle(0xE5E5E5, 0x0D1014, 0))
	focusedStyle := styleOr(styles.Focused, terminal.NewStyle(0xFFFFFF, 0x005F87, terminal.CharBold))
	selectedStyle := styleOr(styles.Selected, terminal.NewStyle(0x1A1F26, 0xFFD700, 0))
	overflowStyle := styleOr(styles.Overflow, terminal.NewStyle(0xFFD700, 0x0D1014, terminal.CharBold))
	background := styleOr(styles.Background, textStyle)
	buf.Fill(terminal.Rect{W: width, H: 1}, terminal.Cell{Grapheme: " ", Style: background})

	cursorCol := runeColumn(w.value, w.cursor)
	totalWidth := runeColumn(w.value, len(w.value))
	if w.scrollCol < 0 {
		w.scrollCol = 0
	}
	if w.scrollCol > totalWidth {
		w.scrollCol = totalWidth
	}

	leftOverflow := w.scrollCol > 0
	rightOverflow := totalWidth-w.scrollCol > width-boolInt(leftOverflow)
	available := width - boolInt(leftOverflow) - boolInt(rightOverflow)
	if available < 1 {
		available = 1
	}
	if cursorCol < w.scrollCol {
		w.scrollCol = cursorCol
		leftOverflow = w.scrollCol > 0
		rightOverflow = totalWidth-w.scrollCol > width-boolInt(leftOverflow)
		available = max(1, width-boolInt(leftOverflow)-boolInt(rightOverflow))
	}
	if cursorCol >= w.scrollCol+available {
		w.scrollCol = cursorCol - available + 1
		leftOverflow = w.scrollCol > 0
		rightOverflow = totalWidth-w.scrollCol > width-boolInt(leftOverflow)
		available = max(1, width-boolInt(leftOverflow)-boolInt(rightOverflow))
	}
	if leftOverflow && width > 0 {
		buf.Set(0, 0, terminal.Cell{Grapheme: "…", Style: overflowStyle})
	}
	if rightOverflow && width > 0 {
		buf.Set(width-1, 0, terminal.Cell{Grapheme: "…", Style: overflowStyle})
	}

	startX := boolInt(leftOverflow)
	col := 0
	selStart, selEnd := w.selectionBounds()
	for i, r := range w.value {
		rw := runeWidth(r)
		x := startX + col - w.scrollCol
		inSelection := w.selecting && i >= selStart && i < selEnd
		style := textStyle
		if w.focused {
			style = focusedStyle
		}
		if inSelection {
			style = selectedStyle
		}
		if x >= startX && x+rw <= startX+available {
			buf.Set(x, 0, terminal.Cell{Grapheme: string(r), Style: style})
			for n := 1; n < rw; n++ {
				buf.Set(x+n, 0, terminal.Cell{Grapheme: "", Style: style})
			}
		}
		col += rw
	}
	cursorX := startX + cursorCol - w.scrollCol
	if cursorX < startX {
		cursorX = startX
	}
	if cursorX >= startX+available {
		cursorX = startX + available - 1
	}
	return buf, CursorPos{X: cursorX, Y: 0, Visible: w.focused && ctx.Focused}
}

func (w *TextInput) HandleEvent(event input.Event) bool {
	if w == nil || !w.focused {
		return false
	}
	switch ev := event.(type) {
	case input.PasteEvent:
		if !w.allowPaste {
			return false
		}
		w.insert(strings.ReplaceAll(strings.ReplaceAll(string(ev), "\r\n", " "), "\n", " "))
		return true
	case *input.PasteEvent:
		return ev != nil && w.HandleEvent(*ev)
	case input.KeyPressEvent:
		return w.handleKey(ev)
	case *input.KeyPressEvent:
		return ev != nil && w.handleKey(*ev)
	default:
		return false
	}
}

func (w *TextInput) handleKey(ev input.KeyPressEvent) bool {
	key := strings.ToLower(ev.String())
	switch key {
	case "left", "shift+left":
		w.moveCursor(w.cursor-1, strings.HasPrefix(key, "shift+"))
	case "right", "shift+right":
		w.moveCursor(w.cursor+1, strings.HasPrefix(key, "shift+"))
	case "home", "ctrl+a", "shift+home":
		if key == "ctrl+a" {
			w.anchor, w.cursor, w.selecting = 0, len(w.value), true
		} else {
			w.moveCursor(0, strings.HasPrefix(key, "shift+"))
		}
	case "end", "ctrl+e", "shift+end":
		w.moveCursor(len(w.value), strings.HasPrefix(key, "shift+"))
	case "ctrl+left":
		w.moveCursor(wordBoundary(w.value, w.cursor, -1), false)
	case "ctrl+right":
		w.moveCursor(wordBoundary(w.value, w.cursor, 1), false)
	case "backspace":
		if !w.deleteSelection() && w.cursor > 0 {
			w.value = append(w.value[:w.cursor-1], w.value[w.cursor:]...)
			w.cursor--
			w.changed()
		}
	case "delete":
		if !w.deleteSelection() && w.cursor < len(w.value) {
			w.value = append(w.value[:w.cursor], w.value[w.cursor+1:]...)
			w.changed()
		}
	case "ctrl+c":
		if start, end, ok := w.selectionBoundsOK(); ok {
			setClipboard(string(w.value[start:end]))
		}
	case "ctrl+x":
		if start, end, ok := w.selectionBoundsOK(); ok {
			setClipboard(string(w.value[start:end]))
			w.value = append(w.value[:start], w.value[end:]...)
			w.cursor = start
			w.selecting = false
			w.changed()
		}
	case "ctrl+v":
		value, err := clipboard.ReadAll()
		if err != nil {
			value = getClipboard()
		}
		if value != "" {
			w.insert(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", " "), "\n", " "))
		}
	case "ctrl+u":
		w.value = append([]rune(nil), w.value[w.cursor:]...)
		w.cursor = 0
		w.selecting = false
		w.changed()
	case "ctrl+k":
		w.value = w.value[:w.cursor]
		w.selecting = false
		w.changed()
	case "up":
		w.recallHistory(-1)
	case "down":
		w.recallHistory(1)
	case "tab":
		if w.complete != nil {
			if value, cursor, ok := w.complete(string(w.value), w.cursor); ok {
				w.value = []rune(value)
				w.cursor = clamp(cursor, 0, len(w.value))
				w.selecting = false
				w.changed()
				return true
			}
		}
		return false
	case "space", " ":
		w.insert(" ")
	default:
		if ev.Text != "" && !strings.Contains(key, "+") {
			w.insert(ev.Text)
		} else if len([]rune(key)) == 1 && key[0] >= 32 {
			w.insert(key)
		} else {
			return false
		}
	}
	w.invalidate()
	return true
}

func (w *TextInput) insert(text string) {
	runes := []rune(text)
	if len(runes) == 0 {
		return
	}
	start, end, selected := w.selectionBoundsOK()
	if selected {
		w.value = append(w.value[:start], append(runes, w.value[end:]...)...)
		w.cursor = start + len(runes)
		w.selecting = false
	} else {
		w.value = append(w.value[:w.cursor], append(runes, w.value[w.cursor:]...)...)
		w.cursor += len(runes)
	}
	w.changed()
	w.invalidate()
}

func (w *TextInput) moveCursor(pos int, extend bool) {
	pos = clamp(pos, 0, len(w.value))
	if extend {
		if !w.selecting {
			w.anchor = w.cursor
		}
		w.selecting = true
		w.cursor = pos
	} else {
		w.cursor = pos
		w.anchor = pos
		w.selecting = false
	}
	w.invalidate()
}

func (w *TextInput) deleteSelection() bool {
	start, end, ok := w.selectionBoundsOK()
	if !ok {
		return false
	}
	w.value = append(w.value[:start], w.value[end:]...)
	w.cursor = start
	w.selecting = false
	w.changed()
	return true
}

func (w *TextInput) selectionBounds() (int, int) {
	start, end, ok := w.selectionBoundsOK()
	if !ok {
		return 0, 0
	}
	return start, end
}

func (w *TextInput) selectionBoundsOK() (int, int, bool) {
	if !w.selecting || w.anchor == w.cursor {
		return 0, 0, false
	}
	start, end := w.anchor, w.cursor
	if start > end {
		start, end = end, start
	}
	return start, end, true
}

func (w *TextInput) recallHistory(direction int) {
	if len(w.history) == 0 {
		return
	}
	if w.historyIdx < 0 {
		if direction < 0 {
			w.historyIdx = len(w.history) - 1
		} else {
			return
		}
	} else if direction < 0 {
		if w.historyIdx > 0 {
			w.historyIdx--
		}
	} else if w.historyIdx < len(w.history)-1 {
		w.historyIdx++
	} else {
		w.historyIdx = -1
		w.value = nil
		w.cursor = 0
		w.selecting = false
		w.changed()
		return
	}
	w.value = []rune(w.history[w.historyIdx])
	w.cursor = len(w.value)
	w.selecting = false
	w.changed()
}

func (w *TextInput) changed() {
	if w.onChange != nil {
		w.onChange(string(w.value))
	}
	w.invalidate()
}

func (w *TextInput) invalidate() {
	if w.ctx.Invalidate != nil {
		w.ctx.Invalidate()
	}
}

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

func wordBoundary(value []rune, cursor, direction int) int {
	isWord := func(r rune) bool {
		return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127
	}
	if direction < 0 {
		for cursor > 0 && !isWord(value[cursor-1]) {
			cursor--
		}
		for cursor > 0 && isWord(value[cursor-1]) {
			cursor--
		}
		return cursor
	}
	for cursor < len(value) && !isWord(value[cursor]) {
		cursor++
	}
	for cursor < len(value) && isWord(value[cursor]) {
		cursor++
	}
	return cursor
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

type CheckboxStyles struct {
	Normal   terminal.Style
	Focused  terminal.Style
	Disabled terminal.Style
}

type Checkbox struct {
	Label    string
	Checked  bool
	Disabled bool
	Focused  bool
	Styles   CheckboxStyles
}

func (w *Checkbox) Measure(c Constraints) Size {
	return Size{W: max(0, c.MaxW), H: 1}
}

func (w *Checkbox) Render(r Rect, _ RenderContext) (CellBuf, CursorPos) {
	buf := terminal.NewCellBuf(max(0, r.W), 1)
	if w == nil || r.W <= 0 {
		return buf, CursorPos{}
	}
	style := styleOr(w.Styles.Normal, terminal.NewStyle(0xE5E5E5, 0x1A1F26, 0))
	if w.Disabled {
		style = styleOr(w.Styles.Disabled, terminal.NewStyle(0x8A95A5, 0x1A1F26, terminal.CharDim))
	} else if w.Focused {
		style = styleOr(w.Styles.Focused, terminal.NewStyle(0x1A1F26, 0xFFD700, terminal.CharBold))
	}
	for i, r := range []rune("[ ] " + w.Label) {
		if i >= buf.Width {
			break
		}
		buf.Set(i, 0, terminal.Cell{Grapheme: string(r), Style: style})
	}
	check := " "
	if w.Checked {
		check = "x"
	}
	if buf.Width > 1 {
		buf.Set(1, 0, terminal.Cell{Grapheme: check, Style: style})
	}
	return buf, CursorPos{}
}

func (w *Checkbox) HandleEvent(event input.Event) bool {
	if w == nil || w.Disabled {
		return false
	}
	ev, ok := keyEvent(event)
	if !ok || (ev.String() != "space" && ev.String() != " ") {
		return false
	}
	w.Checked = !w.Checked
	return true
}

type Button struct {
	Label    string
	Hotkey   string
	Disabled bool
	OnPress  func()
}

type ButtonStyles struct {
	Normal   terminal.Style
	Focused  terminal.Style
	Disabled terminal.Style
}

type ButtonRow struct {
	Buttons []Button
	Index   int
	Focused bool
	Styles  ButtonStyles
}

func NewButtonRow(buttons []Button) *ButtonRow {
	return &ButtonRow{Buttons: append([]Button(nil), buttons...)}
}

func (w *ButtonRow) Measure(c Constraints) Size {
	if w == nil || len(w.Buttons) == 0 {
		return Size{}
	}
	width := 0
	for _, button := range w.Buttons {
		width += runewidth.StringWidth(button.Label) + 4
	}
	if len(w.Buttons) > 1 {
		width += (len(w.Buttons) - 1) * 2
	}
	maxWidth := c.MaxW
	if maxWidth <= 0 {
		maxWidth = width
	}
	rows := max(1, (width+maxWidth-1)/maxWidth)
	return Size{W: min(width, maxWidth), H: rows}
}

func (w *ButtonRow) Render(r Rect, ctx RenderContext) (CellBuf, CursorPos) {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if w == nil || width == 0 || height == 0 {
		return buf, CursorPos{}
	}
	normal := styleOr(w.Styles.Normal, terminal.NewStyle(0xE5E5E5, 0x2A323D, 0))
	focused := styleOr(w.Styles.Focused, terminal.NewStyle(0x1A1F26, 0xFFD700, terminal.CharBold))
	disabled := styleOr(w.Styles.Disabled, terminal.NewStyle(0x8A95A5, 0x1A1F26, terminal.CharDim))
	x, y := 0, 0
	for i, button := range w.Buttons {
		text := "[ " + button.Label + " ]"
		textWidth := runewidth.StringWidth(text)
		if x > 0 && x+textWidth > width {
			x = 0
			y++
		}
		if y >= height {
			break
		}
		style := normal
		if button.Disabled {
			style = disabled
		} else if i == w.Index && w.Focused {
			style = focused
		}
		col := x
		labelHotkey := -1
		if button.Hotkey != "" {
			for j, r := range []rune(button.Label) {
				if strings.EqualFold(string(r), button.Hotkey) {
					labelHotkey = j + 2
					break
				}
			}
		}
		for textIndex, r := range []rune(text) {
			rw := runeWidth(r)
			if col+rw > width {
				break
			}
			cellStyle := style
			if textIndex == labelHotkey {
				cellStyle = style.AddAttribute(terminal.CharUnderline)
			}
			buf.Set(col, y, terminal.Cell{Grapheme: string(r), Style: cellStyle})
			for n := 1; n < rw; n++ {
				buf.Set(col+n, y, terminal.Cell{Grapheme: "", Style: cellStyle})
			}
			col += rw
		}
		x += textWidth + 2
	}
	return buf, CursorPos{Visible: false}
}

func (w *ButtonRow) HandleEvent(event input.Event) bool {
	if w == nil || len(w.Buttons) == 0 {
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
		key := strings.ToLower(ev.String())
		if ev.Text != "" {
			key = strings.ToLower(ev.Text)
		}
		for i, button := range w.Buttons {
			if button.Hotkey != "" && key == strings.ToLower(button.Hotkey) {
				w.activate(i)
				return true
			}
		}
	}
	return false
}

func (w *ButtonRow) move(step int) {
	if len(w.Buttons) == 0 {
		return
	}
	for n := 0; n < len(w.Buttons); n++ {
		w.Index = (w.Index + step + len(w.Buttons)) % len(w.Buttons)
		if !w.Buttons[w.Index].Disabled {
			return
		}
	}
}

func (w *ButtonRow) activate(index int) {
	if index < 0 || index >= len(w.Buttons) || w.Buttons[index].Disabled {
		return
	}
	if w.Buttons[index].OnPress != nil {
		w.Buttons[index].OnPress()
	}
}

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
	height := 3
	if c.MaxH > 0 && c.MaxH < height {
		height = c.MaxH
	}
	return Size{W: max(0, c.MaxW), H: height}
}

func (w *ProgressBar) Render(r Rect, _ RenderContext) (CellBuf, CursorPos) {
	width, height := max(0, r.W), max(0, r.H)
	buf := terminal.NewCellBuf(width, height)
	if w == nil || width == 0 || height == 0 {
		return buf, CursorPos{}
	}
	labelStyle := styleOr(w.Styles.Label, terminal.NewStyle(0xE5E5E5, 0x1A1F26, 0))
	filledStyle := styleOr(w.Styles.Filled, terminal.NewStyle(0xFFD700, 0x1A1F26, 0))
	emptyStyle := styleOr(w.Styles.Empty, terminal.NewStyle(0x8A95A5, 0x1A1F26, 0))
	metaStyle := styleOr(w.Styles.Metadata, terminal.NewStyle(0x8A95A5, 0x1A1F26, 0))
	writeText(&buf, 0, 0, middleTruncate(w.Current, width), labelStyle)
	if height > 1 {
		pct := percent(w.Done, w.Total)
		suffix := fmt.Sprintf(" %3d%%", pct)
		barWidth := max(0, width-runewidth.StringWidth(suffix))
		filled := barWidth * pct / 100
		for x := 0; x < barWidth; x++ {
			r := '░'
			style := emptyStyle
			if x < filled {
				r = '█'
				style = filledStyle
			}
			buf.Set(x, 1, terminal.Cell{Grapheme: string(r), Style: style})
		}
		writeText(&buf, barWidth, 1, suffix, labelStyle)
	}
	if height > 2 {
		meta := fmt.Sprintf("%s / %s  %s/s", formatBytes(w.Done), formatBytes(w.Total), formatBytes(w.Rate))
		writeText(&buf, 0, 2, middleTruncate(meta, width), metaStyle)
	}
	return buf, CursorPos{}
}

func (w *ProgressBar) HandleEvent(input.Event) bool { return false }

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

var clipboardMu sync.Mutex
var clipboardFallback string

func setClipboard(value string) {
	clipboardMu.Lock()
	clipboardFallback = value
	clipboardMu.Unlock()
	_ = clipboard.WriteAll(value)
}

func getClipboard() string {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	return clipboardFallback
}

var _ ui.Widget = (*TextInput)(nil)
var _ ui.Widget = (*Checkbox)(nil)
var _ ui.Widget = (*ButtonRow)(nil)
var _ ui.Widget = (*ProgressBar)(nil)
