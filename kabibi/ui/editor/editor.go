package editor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/input"
	"github.com/mattn/go-runewidth"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

const editorTabWidth = 4

const (
	colorYellow   = lipgloss.Color("#ffd700")
	colorNavy     = lipgloss.Color("#000080")
	colorDarkGray = lipgloss.Color("#3a3a3a")
	colorWhite    = lipgloss.Color("#e5e5e5")
)

func truncateStringToWidth(s string, maxWidth int) string {
	displayWidth := runewidth.StringWidth(s)
	if displayWidth <= maxWidth {
		return s
	}
	effectiveWidth := maxWidth - 1
	if effectiveWidth <= 0 {
		return ""
	}
	width := 0
	var b strings.Builder
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if width+w > effectiveWidth {
			break
		}
		b.WriteRune(r)
		width += w
	}
	return strings.TrimRight(b.String(), " ")
}

func expandTabs(s string, tabWidth int) string {
	var b strings.Builder
	column := 0
	for _, r := range s {
		if r == '\t' {
			spaces := tabWidth - (column % tabWidth)
			for i := 0; i < spaces; i++ {
				b.WriteByte(' ')
				column++
			}
		} else {
			b.WriteRune(r)
			column += runewidth.RuneWidth(r)
		}
	}
	return b.String()
}

// Style identifiers for highlighted spans. Kept as small ints so runs of the
// same colour can be grouped when rendering a row.
const (
	sidDefault = iota
	sidKeyword
	sidString
	sidNumber
	sidComment
	sidFunc
	sidType
	sidOperator
)

var (
	editorBgColor = lipgloss.Color("#000078") // exact same blue family as panels, just a bit darker
	editorSelBg   = lipgloss.Color("#2d4f67")
	editorGutterS = lipgloss.NewStyle().Foreground(lipgloss.Color("#4a5568")).Background(editorBgColor)
	editorBarS    = lipgloss.NewStyle().Foreground(lipgloss.Color("#1a1f26")).Background(editorBgColor)

	editorStyles = []lipgloss.Style{
		sidDefault:  lipgloss.NewStyle().Foreground(lipgloss.Color("#d5dbe5")).Background(editorBgColor),
		sidKeyword:  lipgloss.NewStyle().Foreground(lipgloss.Color("#c792ea")).Background(editorBgColor),
		sidString:   lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a")).Background(editorBgColor),
		sidNumber:   lipgloss.NewStyle().Foreground(lipgloss.Color("#ff9e64")).Background(editorBgColor),
		sidComment:  lipgloss.NewStyle().Foreground(lipgloss.Color("#5c6773")).Background(editorBgColor).Italic(true),
		sidFunc:     lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Background(editorBgColor),
		sidType:     lipgloss.NewStyle().Foreground(lipgloss.Color("#2ac3de")).Background(editorBgColor),
		sidOperator: lipgloss.NewStyle().Foreground(lipgloss.Color("#89ddff")).Background(editorBgColor),
	}
)

type styledSpan struct {
	text string
	sid  int
}

type editorModel struct {
	path  string
	eol   string
	lines []string

	cx, cy  int // cursor rune column, row
	prefCol int // preferred column for vertical moves
	top     int // first visible row
	left    int // horizontal scroll in visual columns

	width, height int
	dirty         bool
	status        string
	confirmQuit   bool

	sel    bool
	ax, ay int // selection anchor

	clip string // in-app clipboard fallback

	lexer   chroma.Lexer
	hl      [][]styledSpan
	hlValid bool

	// OnClose is called by the parent when the editor should close.
	OnClose     func() tea.Cmd
	OnCloseFunc func()
}

type EditorWidget = editorModel

func NewEditor(path string) (*editorModel, error) {
	return newEditor(path)
}

func newEditor(path string) (*editorModel, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(b)
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}

	lx := lexers.Match(filepath.Base(path))
	if lx == nil {
		lx = lexers.Fallback
	}
	lx = chroma.Coalesce(lx)

	return &editorModel{path: path, eol: eol, lines: lines, lexer: lx}, nil
}

func (e *editorModel) save() error {
	content := strings.Join(e.lines, "\n")
	if e.eol == "\r\n" {
		content = strings.ReplaceAll(content, "\n", "\r\n")
	}
	if err := os.WriteFile(e.path, []byte(content), 0o644); err != nil {
		return err
	}
	e.dirty = false
	return nil
}

// invalidate marks buffer content as changed for highlighting and dirty state.
func (e *editorModel) invalidate() {
	e.dirty = true
	e.hlValid = false
	e.confirmQuit = false
}

func chromaStyleID(tt chroma.TokenType) int {
	switch {
	case tt.InCategory(chroma.Comment):
		return sidComment
	case tt.InCategory(chroma.Keyword):
		return sidKeyword
	case tt.InSubCategory(chroma.LiteralString):
		return sidString
	case tt.InSubCategory(chroma.LiteralNumber):
		return sidNumber
	case tt.InCategory(chroma.Literal):
		return sidString
	case tt == chroma.NameFunction || tt == chroma.NameFunctionMagic:
		return sidFunc
	case tt == chroma.NameClass || tt == chroma.NameNamespace || tt == chroma.KeywordType || tt == chroma.NameBuiltin:
		return sidType
	case tt.InCategory(chroma.Operator):
		return sidOperator
	default:
		return sidDefault
	}
}

func (e *editorModel) ensureHighlight() {
	if e.hlValid {
		return
	}
	e.hl = make([][]styledSpan, len(e.lines))
	content := strings.Join(e.lines, "\n")

	// Skip highlighting very large buffers to stay responsive.
	if e.lexer == nil || len(content) > 400_000 {
		e.hlValid = true
		return
	}

	it, err := e.lexer.Tokenise(nil, content)
	if err != nil {
		e.hlValid = true
		return
	}

	lineIdx := 0
	for _, tok := range it.Tokens() {
		sid := chromaStyleID(tok.Type)
		parts := strings.Split(tok.Value, "\n")
		for pi, part := range parts {
			if pi > 0 {
				lineIdx++
			}
			if lineIdx >= len(e.hl) {
				break
			}
			if part != "" {
				e.hl[lineIdx] = append(e.hl[lineIdx], styledSpan{text: part, sid: sid})
			}
		}
	}
	e.hlValid = true
}

// styleIDsForLine returns a per-rune style-id slice for the given line.
func (e *editorModel) styleIDsForLine(y int) []int {
	runes := []rune(e.lines[y])
	ids := make([]int, len(runes))
	col := 0
	if y < len(e.hl) {
		for _, sp := range e.hl[y] {
			for range sp.text {
				if col < len(ids) {
					ids[col] = sp.sid
				}
				col++
			}
		}
	}
	return ids
}

func visualColumn(runes []rune, idx int) int {
	col := 0
	for i := 0; i < idx && i < len(runes); i++ {
		if runes[i] == '\t' {
			col += editorTabWidth - (col % editorTabWidth)
		} else {
			col += runeCellWidth(runes[i])
		}
	}
	return col
}

func runeCellWidth(r rune) int {
	w := runewidth.RuneWidth(r)
	if w < 1 {
		return 1
	}
	return w
}

// ---- selection helpers ----

func (e *editorModel) selBounds() (sy, sx, ey, ex int) {
	sy, sx, ey, ex = e.ay, e.ax, e.cy, e.cx
	if sy > ey || (sy == ey && sx > ex) {
		sy, sx, ey, ex = ey, ex, sy, sx
	}
	return
}

func (e *editorModel) lineSelection(y int) (start, end int, has bool) {
	if !e.sel {
		return 0, 0, false
	}
	sy, sx, ey, ex := e.selBounds()
	if sy == ey && sx == ex {
		return 0, 0, false
	}
	if y < sy || y > ey {
		return 0, 0, false
	}
	runes := []rune(e.lines[y])
	start = 0
	if y == sy {
		start = sx
	}
	end = len(runes)
	if y == ey {
		end = ex
	}
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	return start, end, end > start
}

func (e *editorModel) selectionText() string {
	if !e.sel {
		return ""
	}
	sy, sx, ey, ex := e.selBounds()
	if sy == ey {
		runes := []rune(e.lines[sy])
		return string(clampSlice(runes, sx, ex))
	}
	var b strings.Builder
	first := []rune(e.lines[sy])
	b.WriteString(string(clampSlice(first, sx, len(first))))
	b.WriteString("\n")
	for y := sy + 1; y < ey; y++ {
		b.WriteString(e.lines[y])
		b.WriteString("\n")
	}
	last := []rune(e.lines[ey])
	b.WriteString(string(clampSlice(last, 0, ex)))
	return b.String()
}

func clampSlice(r []rune, a, b int) []rune {
	if a < 0 {
		a = 0
	}
	if b > len(r) {
		b = len(r)
	}
	if a > b {
		a = b
	}
	return r[a:b]
}

func (e *editorModel) deleteSelection() {
	if !e.sel {
		return
	}
	sy, sx, ey, ex := e.selBounds()
	firstRunes := []rune(e.lines[sy])
	lastRunes := []rune(e.lines[ey])
	newLine := string(clampSlice(firstRunes, 0, sx)) + string(clampSlice(lastRunes, ex, len(lastRunes)))
	e.lines[sy] = newLine
	if ey > sy {
		e.lines = append(e.lines[:sy+1], e.lines[ey+1:]...)
	}
	e.cy, e.cx = sy, sx
	e.sel = false
	e.invalidate()
}

func (e *editorModel) deleteSelectionIfAny() bool {
	if e.sel {
		sy, sx, ey, ex := e.selBounds()
		if sy != ey || sx != ex {
			e.deleteSelection()
			return true
		}
	}
	e.sel = false
	return false
}

// ---- editing primitives ----

func (e *editorModel) curRunes() []rune { return []rune(e.lines[e.cy]) }

func (e *editorModel) insertRune(r rune) {
	runes := e.curRunes()
	if e.cx > len(runes) {
		e.cx = len(runes)
	}
	runes = append(runes[:e.cx], append([]rune{r}, runes[e.cx:]...)...)
	e.lines[e.cy] = string(runes)
	e.cx++
	e.prefCol = e.cx
	e.invalidate()
}

func (e *editorModel) insertText(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	parts := strings.Split(s, "\n")
	for i, part := range parts {
		for _, r := range part {
			e.insertRuneNoHL(r)
		}
		if i < len(parts)-1 {
			e.newlineNoHL()
		}
	}
	e.invalidate()
}

func (e *editorModel) insertRuneNoHL(r rune) {
	runes := e.curRunes()
	if e.cx > len(runes) {
		e.cx = len(runes)
	}
	runes = append(runes[:e.cx], append([]rune{r}, runes[e.cx:]...)...)
	e.lines[e.cy] = string(runes)
	e.cx++
}

func (e *editorModel) newlineNoHL() {
	runes := e.curRunes()
	if e.cx > len(runes) {
		e.cx = len(runes)
	}
	left := string(runes[:e.cx])
	right := string(runes[e.cx:])
	e.lines[e.cy] = left
	rest := append([]string{right}, e.lines[e.cy+1:]...)
	e.lines = append(e.lines[:e.cy+1], rest...)
	e.cy++
	e.cx = 0
}

func (e *editorModel) newline() {
	e.newlineNoHL()
	e.prefCol = 0
	e.invalidate()
}

func (e *editorModel) backspace() {
	if e.cx > 0 {
		runes := e.curRunes()
		runes = append(runes[:e.cx-1], runes[e.cx:]...)
		e.lines[e.cy] = string(runes)
		e.cx--
	} else if e.cy > 0 {
		prev := []rune(e.lines[e.cy-1])
		e.cx = len(prev)
		e.lines[e.cy-1] = string(prev) + e.lines[e.cy]
		e.lines = append(e.lines[:e.cy], e.lines[e.cy+1:]...)
		e.cy--
	}
	e.prefCol = e.cx
	e.invalidate()
}

func (e *editorModel) deleteForward() {
	runes := e.curRunes()
	if e.cx < len(runes) {
		runes = append(runes[:e.cx], runes[e.cx+1:]...)
		e.lines[e.cy] = string(runes)
	} else if e.cy < len(e.lines)-1 {
		e.lines[e.cy] = e.lines[e.cy] + e.lines[e.cy+1]
		e.lines = append(e.lines[:e.cy+1], e.lines[e.cy+2:]...)
	}
	e.invalidate()
}

// ---- movement ----

func (e *editorModel) clampCursor() {
	if e.cy < 0 {
		e.cy = 0
	}
	if e.cy > len(e.lines)-1 {
		e.cy = len(e.lines) - 1
	}
	n := len([]rune(e.lines[e.cy]))
	if e.cx < 0 {
		e.cx = 0
	}
	if e.cx > n {
		e.cx = n
	}
}

func (e *editorModel) startSelectIfNeeded(extend bool) {
	if extend {
		if !e.sel {
			e.sel = true
			e.ay, e.ax = e.cy, e.cx
		}
	} else {
		e.sel = false
	}
}

func (e *editorModel) moveLeft(extend bool) {
	e.startSelectIfNeeded(extend)
	if e.cx > 0 {
		e.cx--
	} else if e.cy > 0 {
		e.cy--
		e.cx = len([]rune(e.lines[e.cy]))
	}
	e.prefCol = e.cx
}

func (e *editorModel) moveRight(extend bool) {
	e.startSelectIfNeeded(extend)
	n := len(e.curRunes())
	if e.cx < n {
		e.cx++
	} else if e.cy < len(e.lines)-1 {
		e.cy++
		e.cx = 0
	}
	e.prefCol = e.cx
}

func (e *editorModel) moveVertical(delta int, extend bool) {
	e.startSelectIfNeeded(extend)
	e.cy += delta
	if e.cy < 0 {
		e.cy = 0
	}
	if e.cy > len(e.lines)-1 {
		e.cy = len(e.lines) - 1
	}
	n := len(e.curRunes())
	e.cx = e.prefCol
	if e.cx > n {
		e.cx = n
	}
}

func (e *editorModel) moveHome(extend bool) {
	e.startSelectIfNeeded(extend)
	e.cx = 0
	e.prefCol = 0
}

func (e *editorModel) moveEnd(extend bool) {
	e.startSelectIfNeeded(extend)
	e.cx = len(e.curRunes())
	e.prefCol = e.cx
}

func isWordRune(r rune) bool {
	return r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127
}

func (e *editorModel) moveWord(dir int, extend bool) {
	e.startSelectIfNeeded(extend)
	runes := e.curRunes()
	if dir > 0 {
		for e.cx < len(runes) && !isWordRune(runes[e.cx]) {
			e.cx++
		}
		for e.cx < len(runes) && isWordRune(runes[e.cx]) {
			e.cx++
		}
	} else {
		for e.cx > 0 && !isWordRune(runes[e.cx-1]) {
			e.cx--
		}
		for e.cx > 0 && isWordRune(runes[e.cx-1]) {
			e.cx--
		}
	}
	e.prefCol = e.cx
}

func (e *editorModel) selectAll() {
	e.sel = true
	e.ay, e.ax = 0, 0
	e.cy = len(e.lines) - 1
	e.cx = len([]rune(e.lines[e.cy]))
}

func (e *editorModel) triggerClose() tea.Cmd {
	if e.OnCloseFunc != nil {
		e.OnCloseFunc()
	}
	if e.OnClose != nil {
		return e.OnClose()
	}
	return nil
}

// HandleKey processes a key event for the editor, calling OnClose when the
// editor should be dismissed.
func (e *editorModel) HandleKey(msg tea.KeyMsg) tea.Cmd {
	if e == nil {
		return nil
	}
	return e.handleKeyName(msg.String(), msg.Runes)
}

func (e *editorModel) handleKeyName(key string, runes []rune) tea.Cmd {
	if e.confirmQuit {
		switch key {
		case "f2", "y", "Y", "enter":
			if err := e.save(); err != nil {
				e.confirmQuit = false
				e.status = "save failed: " + err.Error()
				return nil
			}
			return e.triggerClose()
		case "n", "N":
			return e.triggerClose()
		case "esc":
			e.confirmQuit = false
			e.status = ""
			return nil
		default:
			return nil
		}
	}

	switch key {
	case "esc":
		if e.dirty {
			e.confirmQuit = true
			e.status = "Save changes?  [Y] save   [N] discard   [Esc] cancel"
			return nil
		}
		return e.triggerClose()
	case "f2", "ctrl+s":
		if err := e.save(); err != nil {
			e.status = "save failed: " + err.Error()
		} else {
			e.status = "Saved " + filepath.Base(e.path)
		}
		return nil
	case "ctrl+c":
		if txt := e.selectionText(); txt != "" {
			e.clip = txt
			_ = clipboard.WriteAll(txt)
			e.status = "Copied"
		}
		return nil
	case "ctrl+x":
		if txt := e.selectionText(); txt != "" {
			e.clip = txt
			_ = clipboard.WriteAll(txt)
			e.deleteSelection()
			e.status = "Cut"
		}
		return nil
	case "ctrl+v":
		txt, err := clipboard.ReadAll()
		if err != nil || txt == "" {
			txt = e.clip
		}
		if txt != "" {
			e.deleteSelectionIfAny()
			e.insertText(txt)
		}
		return nil
	case "ctrl+a":
		e.selectAll()
		return nil
	case "enter":
		e.deleteSelectionIfAny()
		e.newline()
		return nil
	case "backspace":
		if !e.deleteSelectionIfAny() {
			e.backspace()
		}
		return nil
	case "delete":
		if !e.deleteSelectionIfAny() {
			e.deleteForward()
		}
		return nil
	case "tab":
		e.deleteSelectionIfAny()
		e.insertRune('\t')
		return nil
	case "left":
		e.moveLeft(false)
	case "right":
		e.moveRight(false)
	case "up":
		e.moveVertical(-1, false)
	case "down":
		e.moveVertical(1, false)
	case "shift+left":
		e.moveLeft(true)
	case "shift+right":
		e.moveRight(true)
	case "shift+up":
		e.moveVertical(-1, true)
	case "shift+down":
		e.moveVertical(1, true)
	case "ctrl+left":
		e.moveWord(-1, false)
	case "ctrl+right":
		e.moveWord(1, false)
	case "home":
		e.moveHome(false)
	case "end":
		e.moveEnd(false)
	case "shift+home":
		e.moveHome(true)
	case "shift+end":
		e.moveEnd(true)
	case "pgup":
		e.moveVertical(-(e.pageRows()), false)
	case "pgdown":
		e.moveVertical(e.pageRows(), false)
	case "ctrl+home":
		e.startSelectIfNeeded(false)
		e.cy, e.cx, e.prefCol = 0, 0, 0
	case "ctrl+end":
		e.startSelectIfNeeded(false)
		e.cy = len(e.lines) - 1
		e.cx = len(e.curRunes())
		e.prefCol = e.cx
	case " ", "space":
		e.deleteSelectionIfAny()
		e.insertRune(' ')
	default:
		if len(runes) > 0 {
			e.deleteSelectionIfAny()
			for _, r := range runes {
				e.insertRune(r)
			}
		}
	}

	e.clampCursor()
	return nil
}

// Measure implements ui.Widget.
func (e *editorModel) Measure(c ui.Constraints) ui.Size {
	w := c.MaxW
	if w <= 0 {
		w = e.width
	}
	if w <= 0 {
		w = 80
	}
	h := c.MaxH
	if h <= 0 {
		h = e.height
	}
	if h <= 0 {
		h = 24
	}
	return ui.Size{W: w, H: h}
}

// Render implements ui.Widget by rendering the editor view into a CellBuf.
func (e *editorModel) Render(r terminal.Rect, _ ui.RenderContext) (terminal.CellBuf, ui.CursorPos) {
	if r.W > 0 {
		e.width = r.W
	}
	if r.H > 0 {
		e.height = r.H
	}
	if e.width <= 0 || e.height <= 0 {
		return terminal.NewCellBuf(0, 0), ui.CursorPos{Visible: false}
	}
	viewStr := e.View()
	lines := strings.Split(strings.ReplaceAll(viewStr, "\r\n", "\n"), "\n")
	buf := terminal.FromANSI(lines, e.width)
	out := terminal.NewCellBuf(e.width, e.height)
	out.Blit(buf, 0, 0)
	return out, ui.CursorPos{Visible: false}
}

// HandleEvent implements ui.Widget.
func (e *editorModel) HandleEvent(ev input.Event) bool {
	if e == nil {
		return false
	}
	switch k := ev.(type) {
	case input.KeyPressEvent:
		var runes []rune
		if k.Text != "" {
			runes = []rune(k.Text)
		} else if len(k.String()) == 1 {
			runes = []rune(k.String())
		}
		e.handleKeyName(k.String(), runes)
		return true
	case *input.KeyPressEvent:
		if k == nil {
			return false
		}
		return e.HandleEvent(*k)
	case input.PasteEvent:
		e.deleteSelectionIfAny()
		e.insertText(string(k))
		e.clampCursor()
		return true
	case *input.PasteEvent:
		if k == nil {
			return false
		}
		return e.HandleEvent(*k)
	}
	return false
}

var _ ui.Widget = (*editorModel)(nil)

func (e *editorModel) pageRows() int {
	r := e.height - 2
	if r < 1 {
		r = 1
	}
	return r
}

// ---- rendering ----

func (e *editorModel) View() string {
	e.ensureHighlight()

	w, h := e.width, e.height
	if w <= 0 || h <= 0 {
		return ""
	}
	contentRows := h - 2
	if contentRows < 1 {
		contentRows = 1
	}

	gutterW := len(fmt.Sprint(len(e.lines))) + 1
	if gutterW < 4 {
		gutterW = 4
	}
	scrollbarW := 1
	textW := w - gutterW - scrollbarW
	if textW < 1 {
		textW = 1
	}

	// Keep cursor visible.
	if e.cy < e.top {
		e.top = e.cy
	}
	if e.cy >= e.top+contentRows {
		e.top = e.cy - contentRows + 1
	}
	if e.top < 0 {
		e.top = 0
	}
	curVis := visualColumn(e.curRunes(), e.cx)
	if curVis < e.left {
		e.left = curVis
	}
	if curVis >= e.left+textW {
		e.left = curVis - textW + 1
	}
	if e.left < 0 {
		e.left = 0
	}

	thumbStart, thumbEnd := scrollbarThumb(len(e.lines), contentRows, e.top)

	var rows []string
	for r := 0; r < contentRows; r++ {
		y := e.top + r
		var gutter, content string
		if y < len(e.lines) {
			gutter = editorGutterS.Width(gutterW).Align(lipgloss.Right).Render(fmt.Sprint(y+1) + " ")
			content = e.renderRow(y, textW, curVis)
		} else {
			gutter = editorGutterS.Width(gutterW).Render("")
			content = editorStyles[sidDefault].Width(textW).Render("")
		}
		sb := "│"
		if r >= thumbStart && r < thumbEnd {
			sb = "█"
		}
		scroll := editorGutterS.Render(sb)
		rows = append(rows, gutter+content+scroll)
	}

	title := e.titleBar(w)
	status := e.statusBar(w)
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, append(rows, status)...)...)
}

func (e *editorModel) renderRow(y, textW, curVis int) string {
	runes := []rune(e.lines[y])
	ids := e.styleIDsForLine(y)
	selStart, selEnd, hasSel := e.lineSelection(y)
	isCursorRow := y == e.cy

	// Expand to visual cells (tabs -> spaces). Wide runes are treated as one
	// cell for cursor math; acceptable for basic editing of code files.
	type cell struct {
		r   rune
		sid int
		sel bool
	}
	var cells []cell
	for i, r := range runes {
		sid := sidDefault
		if i < len(ids) {
			sid = ids[i]
		}
		sel := hasSel && i >= selStart && i < selEnd
		if r == '\t' {
			n := editorTabWidth - (len(cells) % editorTabWidth)
			for k := 0; k < n; k++ {
				cells = append(cells, cell{r: ' ', sid: sid, sel: sel})
			}
		} else {
			cells = append(cells, cell{r: r, sid: sid, sel: sel})
		}
	}

	var b strings.Builder
	var runText strings.Builder
	runSid := -1
	runSel := false
	flush := func() {
		if runText.Len() > 0 {
			st := editorStyles[runSid]
			if runSel {
				st = st.Background(editorSelBg)
			}
			b.WriteString(st.Render(runText.String()))
			runText.Reset()
		}
	}

	for col := 0; col < textW; col++ {
		idx := e.left + col
		r := ' '
		sid := sidDefault
		sel := false
		if idx < len(cells) {
			r = cells[idx].r
			sid = cells[idx].sid
			sel = cells[idx].sel
		}
		cursor := isCursorRow && idx == curVis
		if cursor {
			flush()
			runSid = -1
			st := editorStyles[sid]
			if sel {
				st = st.Background(editorSelBg)
			}
			b.WriteString(st.Reverse(true).Render(string(r)))
			continue
		}
		if sid != runSid || sel != runSel {
			flush()
			runSid = sid
			runSel = sel
		}
		runText.WriteRune(r)
	}
	flush()
	return b.String()
}

func (e *editorModel) titleBar(w int) string {
	name := e.path
	if e.dirty {
		name += " *"
	}
	lang := ""
	if e.lexer != nil {
		lang = e.lexer.Config().Name
	}
	left := " " + truncateStringToWidth(name, w-len(lang)-6)
	style := lipgloss.NewStyle().Background(colorYellow).Foreground(colorNavy).Bold(true).Width(w)
	right := lang + " "
	pad := w - runewidth.StringWidth(left) - runewidth.StringWidth(right)
	if pad < 1 {
		pad = 1
	}
	return style.Render(left + strings.Repeat(" ", pad) + right)
}

func (e *editorModel) statusBar(w int) string {
	pos := fmt.Sprintf(" Ln %d, Col %d ", e.cy+1, e.cx+1)
	hint := "F2 save  Ctrl+C/X/V clip  Esc quit "
	msg := e.status
	style := lipgloss.NewStyle().Background(colorDarkGray).Foreground(colorWhite).Width(w)
	left := pos
	if msg != "" {
		left += "· " + msg + " "
	}
	pad := w - runewidth.StringWidth(left) - runewidth.StringWidth(hint)
	if pad < 1 {
		pad = 1
	}
	return style.Render(truncateStringToWidth(left, w-runewidth.StringWidth(hint)-1) + strings.Repeat(" ", pad) + hint)
}

// scrollbarThumb returns [start,end) rows of the scrollbar thumb.
func scrollbarThumb(total, visible, top int) (int, int) {
	if total <= visible || total == 0 {
		return 0, visible
	}
	size := visible * visible / total
	if size < 1 {
		size = 1
	}
	maxTop := total - visible
	start := 0
	if maxTop > 0 {
		start = top * (visible - size) / maxTop
	}
	end := start + size
	if end > visible {
		end = visible
		start = end - size
	}
	if start < 0 {
		start = 0
	}
	return start, end
}
