package terminal

import (
	"image/color"
	"strings"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// Cell represents a single terminal character and its visual state.
type Cell struct {
	R     rune
	Style Style
}

// Rect describes a bounded 2D region.
type Rect struct {
	X, Y, W, H int
}

type Color uint32
type CharAttributes uint16

// Layout: fR(8) fG(8) fB(8) attributes(16) bR(8) bG(8) bB(8)
type Style uint64

// Char flag bit definitions
const (
	CharResetForegroundDefault CharAttributes = 1 << iota
	CharResetBackgroundDefault
	CharBold
	CharDim
	CharItalic
	CharUnderline
	CharBlink
	CharSoftWrap
)

func NewStyle(fg, bg Color, attributes CharAttributes) Style {
	return Style(
		uint64(fg&0xFFFFFF)<<40 |
			uint64(attributes)<<24 |
			uint64(bg&0xFFFFFF),
	)
}

func (s Style) Foreground() Color {
	return Color(uint32(s>>40) & 0xFFFFFF)
}

func (s Style) Background() Color {
	return Color(uint32(s) & 0xFFFFFF)
}

func (s Style) Attributes() CharAttributes {
	return CharAttributes(uint16(s >> 24))
}

func (s Style) HasAttribute(f CharAttributes) bool {
	return (s.Attributes() & f) != 0
}

// Fluent Setters (Immutable copies)

func (s Style) WithForeground(c Color) Style {
	return (s &^ (Style(0xFFFFFF) << 40)) | (Style(c&0xFFFFFF) << 40)
}

func (s Style) WithBackground(c Color) Style {
	return (s &^ Style(0xFFFFFF)) | (Style(c & 0xFFFFFF))
}

func (s Style) WithAttributes(attributes CharAttributes) Style {
	return (s &^ (Style(0xFFFF) << 24)) | (Style(attributes) << 24)
}

func (s Style) AddAttribute(attribute CharAttributes) Style {
	return s | (Style(attribute) << 24)
}

func (s Style) RemoveAttribute(attribute CharAttributes) Style {
	return s &^ (Style(attribute) << 24)
}

// CellBuf is a bounded 2D buffer of styled terminal cells.
type CellBuf struct {
	Width  int
	Height int
	cells  []Cell
}

func NewCellBuf(w, h int) CellBuf {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return CellBuf{Width: w, Height: h, cells: make([]Cell, w*h)}
}

func (b *CellBuf) index(x, y int) int {
	if x < 0 || y < 0 || x >= b.Width || y >= b.Height {
		return -1
	}
	return y*b.Width + x
}

func (b *CellBuf) Get(x, y int) Cell {
	idx := b.index(x, y)
	if idx < 0 {
		return Cell{}
	}
	return b.cells[idx]
}

func (b *CellBuf) Set(x, y int, c Cell) {
	idx := b.index(x, y)
	if idx >= 0 {
		b.cells[idx] = c
	}
}

func (b *CellBuf) Fill(r Rect, c Cell) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			if r.X+x < 0 || r.Y+y < 0 || r.X+x >= b.Width || r.Y+y >= b.Height {
				continue
			}
			b.Set(r.X+x, r.Y+y, c)
		}
	}
}

func (b *CellBuf) Blit(src CellBuf, x, y int) {
	for sy := 0; sy < src.Height; sy++ {
		for sx := 0; sx < src.Width; sx++ {
			if sx < 0 || sy < 0 {
				continue
			}
			cell := src.Get(sx, sy)
			if cell == (Cell{}) {
				continue
			}
			b.Set(x+sx, y+sy, cell)
		}
	}
}

// Cells returns the backing cell slice for direct comparison and copy operations.
func (b CellBuf) Cells() []Cell { return b.cells }

// NewSubBuf returns a CellBuf that shares the backing cells of src for rows
// [startRow, startRow+rows). This is a zero-copy view; the caller must not
// resize or swap the returned buffer.
func NewSubBuf(src CellBuf, startRow, rows int) CellBuf {
	return CellBuf{
		Width:  src.Width,
		Height: rows,
		cells:  src.cells[startRow*src.Width : (startRow+rows)*src.Width],
	}
}

// SwapBufCells exchanges the backing arrays of two CellBufs in O(1), avoiding
// an O(W*H) copy when double-buffering frames.
func SwapBufCells(a, b *CellBuf) { a.cells, b.cells = b.cells, a.cells }

// ansi16RGB maps standard 16-color ANSI indices (0–15) to their canonical RGB values.
var ansi16RGB = [16]Color{
	0x000000, 0x800000, 0x008000, 0x808000, 0x000080, 0x800080, 0x008080, 0xc0c0c0,
	0x808080, 0xff0000, 0x00ff00, 0xffff00, 0x0000ff, 0xff00ff, 0x00ffff, 0xffffff,
}

type colorTier uint8

const (
	tier16        colorTier = iota // standard 16-color ANSI palette (indices 0–15)
	tier256                        // 256-color cube / grayscale ramp (indices 16–255)
	tierTruecolor                  // arbitrary 24-bit RGB
)

// writeInt writes a non-negative integer ≤999 to sb with no heap allocation.
func writeInt(sb *strings.Builder, n int) {
	if n < 10 {
		sb.WriteByte(byte('0') + byte(n))
		return
	}
	if n < 100 {
		sb.WriteByte(byte('0') + byte(n/10))
		sb.WriteByte(byte('0') + byte(n%10))
		return
	}
	sb.WriteByte(byte('0') + byte(n/100))
	sb.WriteByte(byte('0') + byte((n/10)%10))
	sb.WriteByte(byte('0') + byte(n%10))
}

// ToANSI outputs a minimal ANSI byte stream reproducing the buffer visual state.
// Single-pass forward iteration with pendingSpaces deferral eliminates the
// backward trailing-whitespace scan and any intermediate allocations.
func ToANSI(buf CellBuf) string {
	var sb strings.Builder
	sb.Grow(buf.Width * buf.Height / 2)

	activeStyle := NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)

	for y := 0; y < buf.Height; y++ {
		pendingSpaces := 0
		for x := 0; x < buf.Width; x++ {
			cell := buf.Get(x, y)

			wideCharContinuation := cell.R == 0
			if wideCharContinuation {
				pendingSpaces = 0
				// no emitting of anything
				continue
			} else {
				styleChanged := cell.Style != activeStyle
				if styleChanged {
					// Flush accumulated pending spaces before style transition
					if pendingSpaces > 0 {
						fillWithSpaces(&sb, pendingSpaces)
						pendingSpaces = 0
					}
					// Emit style delta SGR sequences
					emitStyleDelta(&sb, activeStyle, cell.Style)
					activeStyle = cell.Style
				} else {
					if isSpaceCharacter(cell) {
						pendingSpaces++
						// no emitting of anything
						continue
					} else {
						// Flush accumulated pending spaces before emitting non-space
						if pendingSpaces > 0 {
							fillWithSpaces(&sb, pendingSpaces)
							pendingSpaces = 0
						}
					}
				}

				sb.WriteRune(cell.R)
			}
		}

		// Row termination: inspect soft wrap and pending spaces
		if buf.Height > 0 {
			lastCellIdx := y*buf.Width + buf.Width - 1
			lastCell := buf.cells[lastCellIdx]
			hasSoftWrap := lastCell.Style.HasAttribute(CharSoftWrap)

			if hasSoftWrap {
				// Soft wrap: flush	accumulated pending spaces as literal spaces, suppress \r\n and \x1b[K
				if pendingSpaces > 0 {
					fillWithSpaces(&sb, pendingSpaces)
				}
				// Continuing on next line: no line clear, no newline
			} else {
				// Hard break: ignore pending spaces, but emit line clear and newline
				sb.WriteString("\x1b[K\r\n")
			}
		}
	}

	return sb.String()
}

func fillWithSpaces(sb *strings.Builder, count int) {
	for i := 0; i < count; i++ {
		sb.WriteByte(' ')
	}
}

func isSpaceCharacter(cell Cell) bool {
	r := cell.R
	// space, non-breaking space, mathematical space
	if r == ' ' || r == 0x00A0 || r == 0x205F {
		return true
	}
	// unicode variety spaces (N-space, M-space, hair-space and everything in between)
	if r >= 0x2000 && r <= 0x200A {
		return true
	}
	return false
}

// emitStyleDelta emits minimal SGR escape sequences to transition from oldStyle to newStyle.
func emitStyleDelta(sb *strings.Builder, oldStyle, newStyle Style) {
	oldAttrs := oldStyle.Attributes()
	newAttrs := newStyle.Attributes()

	// Compute text attributes (excluding reset flags)
	textOldAttrs := oldAttrs &^ (CharResetForegroundDefault | CharResetBackgroundDefault)
	textNewAttrs := newAttrs &^ (CharResetForegroundDefault | CharResetBackgroundDefault)

	// If any text attributes were removed, emit full reset
	if (textOldAttrs &^ textNewAttrs) != 0 {
		sb.WriteString("\x1b[0m")
		oldStyle = 0
		oldAttrs = 0
		textOldAttrs = 0
	}

	// Emit text attribute additions
	var codes []int
	if (textNewAttrs&CharBold) != 0 && (textOldAttrs&CharBold) == 0 {
		codes = append(codes, 1)
	}
	if (textNewAttrs&CharDim) != 0 && (textOldAttrs&CharDim) == 0 {
		codes = append(codes, 2)
	}
	if (textNewAttrs&CharItalic) != 0 && (textOldAttrs&CharItalic) == 0 {
		codes = append(codes, 3)
	}
	if (textNewAttrs&CharUnderline) != 0 && (textOldAttrs&CharUnderline) == 0 {
		codes = append(codes, 4)
	}
	if (textNewAttrs&CharBlink) != 0 && (textOldAttrs&CharBlink) == 0 {
		codes = append(codes, 5)
	}

	if len(codes) > 0 {
		sb.WriteString("\x1b[")
		for i, code := range codes {
			if i > 0 {
				sb.WriteByte(';')
			}
			writeInt(sb, code)
		}
		sb.WriteByte('m')
	}

	// Emit foreground color if changed
	fg := newStyle.Foreground() & 0xFFFFFF
	newHasResetFG := (newAttrs & CharResetForegroundDefault) != 0
	oldHasResetFG := (oldAttrs & CharResetForegroundDefault) != 0
	oldFG := oldStyle.Foreground() & 0xFFFFFF

	// Case 1: Transitioning from explicit color to default foreground
	if newHasResetFG && !oldHasResetFG {
		sb.WriteString("\x1b[39m")
	} else if !newHasResetFG && (oldHasResetFG || fg != oldFG) {
		// Case 2: Transitioning to explicit foreground color (from default or from different color)
		// Try to emit as basic 16-color palette
		if colorIdx := colorToAnsi16(fg); colorIdx >= 0 {
			sb.WriteString("\x1b[")
			if colorIdx < 8 {
				writeInt(sb, 30+colorIdx)
			} else {
				writeInt(sb, 90+colorIdx-8)
			}
			sb.WriteByte('m')
		} else {
			// Emit as 256-color and truecolor for full compatibility
			r, g, b := byte(fg>>16), byte(fg>>8), byte(fg)
			c256 := rgbTo256(r, g, b)
			sb.WriteString("\x1b[38;5;")
			writeInt(sb, int(c256))
			sb.WriteString("m\x1b[38;2;")
			writeInt(sb, int(r))
			sb.WriteByte(';')
			writeInt(sb, int(g))
			sb.WriteByte(';')
			writeInt(sb, int(b))
			sb.WriteByte('m')
		}
	}

	// Emit background color if changed
	bg := newStyle.Background() & 0xFFFFFF
	newHasResetBG := (newAttrs & CharResetBackgroundDefault) != 0
	oldHasResetBG := (oldAttrs & CharResetBackgroundDefault) != 0
	oldBG := oldStyle.Background() & 0xFFFFFF

	// Case 1: Transitioning from explicit color to default background
	if newHasResetBG && !oldHasResetBG {
		sb.WriteString("\x1b[49m")
	} else if !newHasResetBG && (oldHasResetBG || bg != oldBG) {
		// Case 2: Transitioning to explicit background color (from default or from different color)
		// Try to emit as basic 16-color palette
		if colorIdx := colorToAnsi16(bg); colorIdx >= 0 {
			sb.WriteString("\x1b[")
			if colorIdx < 8 {
				writeInt(sb, 40+colorIdx)
			} else {
				writeInt(sb, 100+colorIdx-8)
			}
			sb.WriteByte('m')
		} else {
			// Emit as 256-color and truecolor for full compatibility
			r, g, b := byte(bg>>16), byte(bg>>8), byte(bg)
			c256 := rgbTo256(r, g, b)
			sb.WriteString("\x1b[48;5;")
			writeInt(sb, int(c256))
			sb.WriteString("m\x1b[48;2;")
			writeInt(sb, int(r))
			sb.WriteByte(';')
			writeInt(sb, int(g))
			sb.WriteByte(';')
			writeInt(sb, int(b))
			sb.WriteByte('m')
		}
	}
}

// colorToAnsi16 attempts to map an RGB color to a standard 16-color ANSI index.
// Returns the index (0-15) if the color matches a standard palette entry, or -1 otherwise.
func colorToAnsi16(rgb Color) int {
	for i, paletteColor := range ansi16RGB {
		if rgb == paletteColor {
			return i
		}
	}
	return -1
}

func rgbTo256(r, g, b byte) byte {
	if r == g && g == b {
		if r < 8 {
			return 16
		}
		if r > 248 {
			return 231
		}
		return byte(232 + int(float32(r-8)/247.0*23.0))
	}

	rIdx := int(float32(r) / 255.0 * 5.0)
	gIdx := int(float32(g) / 255.0 * 5.0)
	bIdx := int(float32(b) / 255.0 * 5.0)

	return byte(16 + (36 * rIdx) + (6 * gIdx) + bIdx)
}

// FromANSI renders ANSI-decorated text lines into an exact-fit CellBuf.
// The resulting buffer height is precisely sized to the visual output,
// eliminating untouched trailing viewport space while preserving intentional
// leading blank lines. Intermediate row slice allocations are eliminated by
// direct streaming conversion into a single flat CellBuf allocation.
func FromANSI(lines []string, width int) CellBuf {
	if shouldUseLogicalTextFallback(lines) {
		return logicalTextCellBuf(lines, width)
	}

	term := vt.NewEmulator(width, 24)

	for i, line := range lines {
		_, _ = term.WriteString(line)
		if i < len(lines)-1 {
			_, _ = term.WriteString("\r\n")
		}
	}

	// Phase 1: Calculate exact grid height (no intermediate allocations)
	height := calculateGridHeight(term)
	if height == 0 {
		return NewCellBuf(width, 0)
	}

	// Phase 2 & 3: Single allocation + direct streaming population
	return populateCellBufDirect(term, width, height)
}

func shouldUseLogicalTextFallback(lines []string) bool {
	for _, line := range lines {
		if strings.ContainsRune(line, '\x1b') {
			continue
		}
		for _, r := range line {
			if isEastAsianRune(r) {
				return true
			}
		}
	}
	return false
}

func isEastAsianRune(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func logicalTextCellBuf(lines []string, width int) CellBuf {
	maxCols := 0
	for _, line := range lines {
		cols := len([]rune(line))
		if cols > maxCols {
			maxCols = cols
		}
	}
	if maxCols == 0 {
		return NewCellBuf(width, 0)
	}

	buf := NewCellBuf(max(width, maxCols), len(lines))
	for y, line := range lines {
		for x, r := range []rune(line) {
			buf.Set(x, y, Cell{R: r, Style: NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)})
		}
	}
	return buf
}

// calculateGridHeight computes the exact visual height of the rendered terminal output.
// It performs two fast checks:
//  1. If scrollback exists (buffer overflowed past 24 rows), height = scrollback lines + 24.
//  2. If no scrollback, scans Touched() backwards to find the highest modified row index,
//     then height = Y_max + 1. This preserves intentional leading blank lines.
//
// Time complexity: O(24) or O(height of touched viewport), no intermediate allocations.
func calculateGridHeight(term *vt.Emulator) int {
	// Check for scrollback: if present, total height is scrollback + active viewport.
	if sb := term.Scrollback(); sb.Len() > 0 {
		return sb.Len() + term.Height()
	}

	// No scrollback: scan touched rows backwards for the highest touched index.
	touched := term.Touched()
	if touched == nil || len(touched) == 0 {
		return 0
	}

	lastTouchedIdx := -1
	for y := len(touched) - 1; y >= 0; y-- {
		if touched[y] != nil {
			lastTouchedIdx = y
			break
		}
	}

	if lastTouchedIdx < 0 {
		return 0
	}

	return lastTouchedIdx + 1
}

// populateCellBufDirect constructs and populates a CellBuf by streaming cells directly
// from the terminal emulator into a single flat allocation. Soft wrap classification
// uses the exclusion/inclusion pipeline defined in isSoftWrap.
func populateCellBufDirect(term *vt.Emulator, width, height int) CellBuf {
	buf := NewCellBuf(width, height)

	sb := term.Scrollback()
	var scrolledLines []uv.Line
	scrollbackCount := 0
	if sb != nil {
		scrolledLines = sb.Lines()
		scrollbackCount = len(scrolledLines)
	}

	for y := 0; y < height; y++ {
		base := y * width
		if y < scrollbackCount {
			line := scrolledLines[y]
			for x := 0; x < width; x++ {
				var uvCell *uv.Cell
				if x < len(line) {
					uvCell = &line[x]
				}
				buf.cells[base+x] = convertUVCellToCell(uvCell)
			}
		} else {
			viewportY := y - scrollbackCount
			for x := 0; x < width; x++ {
				buf.cells[base+x] = convertUVCellToCell(term.CellAt(x, viewportY))
			}
		}

		if y+1 < height {
			e0 := buf.cells[base+width-1].R
			e1 := rune(' ')
			if width >= 2 {
				e1 = buf.cells[base+width-2].R
			}
			s0 := rowRuneAt(y+1, 0, scrollbackCount, scrolledLines, term)
			s1 := rowRuneAt(y+1, 1, scrollbackCount, scrolledLines, term)
			s2 := rowRuneAt(y+1, 2, scrollbackCount, scrolledLines, term)
			if isSoftWrap(e1, e0, s0, s1, s2) {
				buf.cells[base+width-1].Style = buf.cells[base+width-1].Style.AddAttribute(CharSoftWrap)
			}
		}
	}

	return buf
}

// rowRuneAt returns the rune at column x of output row y, reading from scrollback
// or the active viewport as appropriate.
func rowRuneAt(y, x, scrollbackCount int, scrolledLines []uv.Line, term *vt.Emulator) rune {
	if y < scrollbackCount {
		line := scrolledLines[y]
		if x >= len(line) {
			return ' '
		}
		cell := &line[x]
		if cell.IsZero() || cell.Content == "" {
			return ' '
		}
		if runes := []rune(cell.Content); len(runes) > 0 {
			return runes[0]
		}
		return ' '
	}
	cell := term.CellAt(x, y-scrollbackCount)
	if cell == nil || cell.IsZero() || cell.Content == "" {
		return ' '
	}
	if runes := []rune(cell.Content); len(runes) > 0 {
		return runes[0]
	}
	return ' '
}

// isSoftWrap applies the exclusion and inclusion pipelines to decide whether the
// boundary between row N (ending e1, e0) and row N+1 (starting s0, s1, s2) should
// suppress the \r\n separator. Returns true for soft wrap, false for hard break.
func isSoftWrap(e1, e0, s0, s1, s2 rune) bool {
	// Exclusion 1: box-drawing or block-element glyphs.
	if isBoxDrawing(e0) || isBoxDrawing(e1) || isBoxDrawing(s0) || isBoxDrawing(s1) {
		return false
	}
	// Exclusion 2: bullet-list prefix on the next row.
	if isBulletPrefix(s0, s1, s2) {
		return false
	}
	// Exclusion 3: line divider (≥3 identical divider symbols).
	if isDividerChar(s0) && s0 == s1 && s1 == s2 {
		return false
	}
	// Exclusion 4: multi-space indentation or empty next row.
	if isInlineWhitespace(s0) && isInlineWhitespace(s1) {
		return false
	}
	// Exclusion 5: sentence-ending full stop; exception for flanked decimals.
	if e0 == '.' {
		if unicode.IsDigit(e1) && unicode.IsDigit(s0) {
			return true // e.g. "3." / "1415" is a split decimal, not a sentence end
		}
		return false
	}

	// Inclusion P1: direct text-to-text fusion.
	if isTextFusion(e0) && isTextFusion(s0) {
		return true
	}
	// Inclusion P2a: row ends in inline whitespace after text content.
	if isTextFusion(e1) && isInlineWhitespace(e0) && isTextFusion(s0) {
		return true
	}
	// Inclusion P2b: row text runs into leading inline whitespace on next row.
	if isTextFusion(e0) && isInlineWhitespace(s0) && isTextFusion(s1) {
		return true
	}

	return false
}

// isBoxDrawing reports whether r is a Box Drawing (U+2500–U+257F) or Block Elements (U+2580–U+259F) rune.
func isBoxDrawing(r rune) bool {
	return (r >= 0x2500 && r <= 0x257F) || (r >= 0x2580 && r <= 0x259F)
}

// isInlineWhitespace reports whether r is whitespace that is not a hard line break.
func isInlineWhitespace(r rune) bool {
	if r == 0 || r == ' ' {
		return true
	}
	return unicode.IsSpace(r) && r != '\r' && r != '\n' && r != '\f' && r != '\v'
}

// isTextFusion reports whether r belongs to C_text: letters (\p{L}), numbers (\p{N}),
// emoji symbols/modifiers (So/Sk), or intra-paragraph punctuation (P_para).
func isTextFusion(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsNumber(r) {
		return true
	}
	// Box drawing and block elements share the So category with emoji; exclude them explicitly.
	if !isBoxDrawing(r) && (unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r)) {
		return true
	}
	switch r {
	case ',', '-', '\u2013', '\u2014',
		'"', '\u201C', '\u201D',
		'\'', '\u2018', '\u2019',
		'(', ')', '[', ']',
		':', ';', '/', '\\':
		return true
	}
	return false
}

// isBulletPrefix reports whether s0, s1, s2 form a bullet-list item start.
func isBulletPrefix(s0, s1, s2 rune) bool {
	switch s0 {
	case '-', '*', '\u2022', '+', '\u2013', '\u2014':
	default:
		return false
	}
	return isInlineWhitespace(s1) && !isInlineWhitespace(s2)
}

// isDividerChar reports whether r can form a repeated-divider run.
func isDividerChar(r rune) bool {
	switch r {
	case '-', '*', '=', '~', '_', '#':
		return true
	}
	return false
}

func convertUVCellToCell(cell *uv.Cell) Cell {
	if cell == nil {
		return Cell{}
	}
	// UV marks wide-character continuation columns as zero-width placeholders.
	// Those cells are not visible and must not be serialized as spaces or
	// merged into the row's printable content.
	if cell.Width == 0 || cell.Content == "" {
		return Cell{}
	}

	var r rune
	if runes := []rune(cell.Content); len(runes) > 0 {
		r = runes[0]
	}
	if r == 0 {
		return Cell{}
	}

	return Cell{R: r, Style: convertUVStyleToStyle(cell.Style)}
}

func convertUVStyleToStyle(style uv.Style) Style {
	var fg, bg Color
	var attrs CharAttributes

	if style.Fg == nil {
		attrs |= CharResetForegroundDefault
	} else {
		fg = parseUVColor(style.Fg)
	}

	if style.Bg == nil {
		attrs |= CharResetBackgroundDefault
	} else {
		bg = parseUVColor(style.Bg)
	}

	if style.Attrs&uv.AttrBold != 0 {
		attrs |= CharBold
	}
	if style.Attrs&uv.AttrFaint != 0 {
		attrs |= CharDim
	}
	if style.Attrs&uv.AttrItalic != 0 {
		attrs |= CharItalic
	}
	if style.Underline != uv.UnderlineNone {
		attrs |= CharUnderline
	}
	if style.Attrs&uv.AttrBlink != 0 || style.Attrs&uv.AttrRapidBlink != 0 || style.Attrs&uv.AttrReverse != 0 {
		attrs |= CharBlink
	}

	return NewStyle(fg, bg, attrs)
}

func parseUVColor(c color.Color) Color {
	r, g, b, _ := color.RGBAModel.Convert(c).RGBA()
	return Color(uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(b>>8))
}

func ansi256ToRGB(code byte) Color {
	if code < 16 {
		return ansi16RGB[code]
	}
	if code >= 232 {
		gray := uint8(8 + (int(code)-232)*10)
		return Color(uint32(gray)<<16 | uint32(gray)<<8 | uint32(gray))
	}
	idx := int(code) - 16
	rVal := []uint8{0, 95, 135, 175, 215, 255}[(idx/36)%6]
	gVal := []uint8{0, 95, 135, 175, 215, 255}[(idx/6)%6]
	bVal := []uint8{0, 95, 135, 175, 215, 255}[idx%6]
	return Color(uint32(rVal)<<16 | uint32(gVal)<<8 | uint32(bVal))
}
