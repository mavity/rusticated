package terminal

import (
	"image/color"
	"strings"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// Cell represents a single terminal character and its visual state.
type Cell struct {
	Grapheme string
	Style    Style
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
	cells := make([]Cell, w*h)
	blank := blankCell()
	for i := range cells {
		cells[i] = blank
	}
	return CellBuf{Width: w, Height: h, cells: cells}
}

// blankCell returns a rendered blank: a single space with default colors and no attributes.
func blankCell() Cell {
	return Cell{Grapheme: " ", Style: NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)}
}

// noContent reports whether a grapheme carries no drawable content. Empty content marks a
// wide-character continuation column (the trailing half of a double-width cell).
func noContent(s string) bool { return s == "" }

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
			if noContent(cell.Grapheme) {
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

			wideCharContinuation := noContent(cell.Grapheme)
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

					if isSpaceCharacter(cell) {
						pendingSpaces = 1
						continue
					}
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

				sb.WriteString(cell.Grapheme)
			}
		}

		// Row termination: inspect soft wrap and pending spaces
		if buf.Height > 0 {
			lastCellIdx := y*buf.Width + buf.Width - 1
			lastCell := buf.cells[lastCellIdx]
			hasSoftWrap := lastCell.Style.HasAttribute(CharSoftWrap)

			if hasSoftWrap {
				// Soft wrap: flush accumulated pending spaces as literal spaces, suppress \r\n and \x1b[K
				if pendingSpaces > 0 {
					fillWithSpaces(&sb, pendingSpaces)
				}
				// Continuing on next line: no line clear, no newline
			} else if y == buf.Height-1 {
				// Last row: clear to end of line if spaces were trimmed, but suppress \r\n to prevent terminal scroll
				if pendingSpaces > 0 {
					sb.WriteString("\x1b[K")
				}
			} else {
				// Hard break: ignore pending spaces, but emit line clear and newline
				if pendingSpaces > 0 {
					sb.WriteString("\x1b[K\r\n")
				} else {
					sb.WriteString("\r\n")
				}
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

// isSpaceCharacter reports whether the cell's grapheme is exactly one whitespace
// code point suitable for trail-wiping collapse. Matching is byte-level so a space
// carrying a combining mark (which adds bytes) is correctly rejected as visible.
func isSpaceCharacter(cell Cell) bool {
	g := cell.Grapheme
	switch len(g) {
	case 1:
		// ASCII space.
		return g[0] == ' '
	case 2:
		// Non-breaking space U+00A0 = C2 A0.
		return g[0] == 0xC2 && g[1] == 0xA0
	case 3:
		if g[0] != 0xE2 {
			return false
		}
		// U+2000..U+200A = E2 80 80..8A (en/em/hair spaces et al.).
		if g[1] == 0x80 && g[2] >= 0x80 && g[2] <= 0x8A {
			return true
		}
		// U+205F mathematical space = E2 81 9F.
		return g[1] == 0x81 && g[2] == 0x9F
	}
	return false
}

// fontMask selects the SGR font attributes (excludes buffer-internal and color-default flags).
const fontMask = CharBold | CharDim | CharItalic | CharUnderline | CharBlink

// sgrBuf is a fixed-capacity, stack-resident scratch buffer for building one style delta
// without heap allocation. 192 bytes comfortably exceeds the longest possible sequence
// (reset + all attributes + double-wrapped fg and bg).
type sgrBuf struct {
	b [192]byte
	n int
}

func (p *sgrBuf) str(s string) { p.n += copy(p.b[p.n:], s) }
func (p *sgrBuf) ch(c byte)    { p.b[p.n] = c; p.n++ }
func (p *sgrBuf) num(v int) {
	switch {
	case v < 10:
		p.b[p.n] = byte('0' + v)
		p.n++
	case v < 100:
		p.b[p.n] = byte('0' + v/10)
		p.b[p.n+1] = byte('0' + v%10)
		p.n += 2
	default:
		p.b[p.n] = byte('0' + v/100)
		p.b[p.n+1] = byte('0' + (v/10)%10)
		p.b[p.n+2] = byte('0' + v%10)
		p.n += 3
	}
}

// ansi256RGB returns the canonical RGB value for a 256-color palette index.
func ansi256RGB(idx byte) Color {
	if idx < 16 {
		return ansi16RGB[idx]
	}
	if idx >= 232 {
		v := 8 + int(idx-232)*10
		return Color(v<<16 | v<<8 | v)
	}
	i := int(idx) - 16
	conv := func(c int) int {
		if c == 0 {
			return 0
		}
		return 55 + c*40
	}
	r := conv(i / 36)
	g := conv((i / 6) % 6)
	b := conv(i % 6)
	return Color(r<<16 | g<<8 | b)
}

// emitStyleDelta emits minimal SGR escape sequences to transition from oldStyle to newStyle.
// Minimality is over the combined byte sequence; per terminal class (truecolor and 256-color)
// the resulting parser state is driven to its correct target. It builds two candidates — an
// incremental delta and a full-reset-then-rebuild — and emits whichever is shorter.
func emitStyleDelta(sb *strings.Builder, oldStyle, newStyle Style) {
	oldAttrs := oldStyle.Attributes()
	newAttrs := newStyle.Attributes()

	oldFgDef := oldStyle.HasAttribute(CharResetForegroundDefault)
	oldBgDef := oldStyle.HasAttribute(CharResetBackgroundDefault)
	newFgDef := newStyle.HasAttribute(CharResetForegroundDefault)
	newBgDef := newStyle.HasAttribute(CharResetBackgroundDefault)
	oldFg, oldBg := oldStyle.Foreground(), oldStyle.Background()
	newFg, newBg := newStyle.Foreground(), newStyle.Background()

	// Candidate A: incremental delta from old to new.
	var inc sgrBuf
	writeAttrDelta(&inc, oldAttrs, newAttrs)
	writeColorDelta(&inc, 0, oldFgDef, oldFg, newFgDef, newFg)
	writeColorDelta(&inc, 10, oldBgDef, oldBg, newBgDef, newBg)

	// Candidate B: full reset to a known baseline (no attrs, default fg/bg), then rebuild new.
	var rst sgrBuf
	rst.str("\x1b[0m")
	writeAttrDelta(&rst, 0, newAttrs)
	writeColorDelta(&rst, 0, true, 0, newFgDef, newFg)
	writeColorDelta(&rst, 10, true, 0, newBgDef, newBg)

	if rst.n < inc.n {
		sb.Write(rst.b[:rst.n])
	} else {
		sb.Write(inc.b[:inc.n])
	}
}

// collectAttrCodes fills out with the ordered SGR codes needed to move the font attribute set
// from oldAttrs to newAttrs, using targeted off-codes rather than a global reset, and returns
// the count. out must have room for 9 codes.
func collectAttrCodes(oldAttrs, newAttrs CharAttributes, out *[9]int) int {
	oldF := oldAttrs & fontMask
	newF := newAttrs & fontMask
	removed := oldF &^ newF
	added := newF &^ oldF

	n := 0
	if removed&(CharBold|CharDim) != 0 {
		out[n] = 22 // SGR 22 clears both bold and dim
		n++
		if newF&CharBold != 0 {
			added |= CharBold
		}
		if newF&CharDim != 0 {
			added |= CharDim
		}
	}
	if removed&CharItalic != 0 {
		out[n] = 23
		n++
	}
	if removed&CharUnderline != 0 {
		out[n] = 24
		n++
	}
	if removed&CharBlink != 0 {
		out[n] = 25
		n++
	}
	if added&CharBold != 0 {
		out[n] = 1
		n++
	}
	if added&CharDim != 0 {
		out[n] = 2
		n++
	}
	if added&CharItalic != 0 {
		out[n] = 3
		n++
	}
	if added&CharUnderline != 0 {
		out[n] = 4
		n++
	}
	if added&CharBlink != 0 {
		out[n] = 5
		n++
	}
	return n
}

// writeAttrDelta appends the minimal font-attribute SGR sequence for oldAttrs -> newAttrs.
func writeAttrDelta(p *sgrBuf, oldAttrs, newAttrs CharAttributes) {
	var codes [9]int
	n := collectAttrCodes(oldAttrs, newAttrs, &codes)
	if n == 0 {
		return
	}
	p.str("\x1b[")
	for i := 0; i < n; i++ {
		if i > 0 {
			p.ch(';')
		}
		p.num(codes[i])
	}
	p.ch('m')
}

// writeColorDelta appends the minimal double-wrapped color sequence for one plane so that a
// truecolor terminal lands on the exact RGB while a 256-color terminal lands on its projection.
// off is 0 for foreground and 10 for background (every bg SGR code is its fg counterpart + 10).
// Redundant halves are elided: the RGB half when a palette code already reproduces the color
// exactly, and the palette half when old and new share the same 256 projection.
func writeColorDelta(p *sgrBuf, off int, oldDef bool, oldRGB Color, newDef bool, newRGB Color) {
	newRGB &= 0xFFFFFF
	oldRGB &= 0xFFFFFF

	if newDef {
		if !oldDef {
			p.str("\x1b[")
			p.num(39 + off)
			p.ch('m')
		}
		return
	}
	if !oldDef && newRGB == oldRGB {
		return
	}

	// Exact 16-palette match: shortest code, exact for both classes.
	if idx := colorToAnsi16(newRGB); idx >= 0 {
		p.str("\x1b[")
		if idx < 8 {
			p.num(30 + off + idx)
		} else {
			p.num(90 + off + idx - 8)
		}
		p.ch('m')
		return
	}

	r, g, b := byte(newRGB>>16), byte(newRGB>>8), byte(newRGB)
	n := rgbTo256(r, g, b)

	// 256 palette reproduces the color exactly: RGB half is redundant for both classes.
	if ansi256RGB(n) == newRGB {
		p.str("\x1b[")
		writeExtPrefix(p, off, true)
		p.num(int(n))
		p.ch('m')
		return
	}

	// Approximate 256 projection: emit the palette half only if the projection changed; the RGB
	// half is required here because every RGB-redundant case has already returned above.
	need256 := oldDef
	if !oldDef {
		or, og, ob := byte(oldRGB>>16), byte(oldRGB>>8), byte(oldRGB)
		need256 = rgbTo256(or, og, ob) != n
	}
	if need256 {
		p.str("\x1b[")
		writeExtPrefix(p, off, true)
		p.num(int(n))
		p.ch('m')
	}
	p.str("\x1b[")
	writeExtPrefix(p, off, false)
	p.num(int(r))
	p.ch(';')
	p.num(int(g))
	p.ch(';')
	p.num(int(b))
	p.ch('m')
}

// writeExtPrefix appends the extended-color SGR prefix: "38;5;"/"38;2;" for foreground (off==0)
// or "48;5;"/"48;2;" for background (off==10). ext256 selects the 5 (256-color) vs 2 (truecolor).
func writeExtPrefix(p *sgrBuf, off int, ext256 bool) {
	if off == 0 {
		p.str("38;")
	} else {
		p.str("48;")
	}
	if ext256 {
		p.str("5;")
	} else {
		p.str("2;")
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
			e0 := buf.cells[base+width-1].Grapheme
			e1 := " "
			if width >= 2 {
				e1 = buf.cells[base+width-2].Grapheme
			}
			s0 := rowGraphemeAt(y+1, 0, scrollbackCount, scrolledLines, term)
			s1 := rowGraphemeAt(y+1, 1, scrollbackCount, scrolledLines, term)
			s2 := rowGraphemeAt(y+1, 2, scrollbackCount, scrolledLines, term)
			if isSoftWrap(e1, e0, s0, s1, s2) {
				buf.cells[base+width-1].Style = buf.cells[base+width-1].Style.AddAttribute(CharSoftWrap)
			}
		}
	}

	return buf
}

// rowGraphemeAt returns the full grapheme at column x of output row y, reading from
// scrollback or the active viewport as appropriate. Empty or zero cells return " ".
func rowGraphemeAt(y, x, scrollbackCount int, scrolledLines []uv.Line, term *vt.Emulator) string {
	if y < scrollbackCount {
		line := scrolledLines[y]
		if x >= len(line) {
			return " "
		}
		cell := &line[x]
		if cell.IsZero() || cell.Content == "" {
			return " "
		}
		return cell.Content
	}
	cell := term.CellAt(x, y-scrollbackCount)
	if cell == nil || cell.IsZero() || cell.Content == "" {
		return " "
	}
	return cell.Content
}

// isASCIIDigit reports whether the grapheme is a single ASCII digit 0-9.
func isASCIIDigit(g string) bool {
	return len(g) == 1 && g[0] >= '0' && g[0] <= '9'
}

// isSoftWrap applies the exclusion and inclusion pipelines to decide whether the
// boundary between row N (ending e1, e0) and row N+1 (starting s0, s1, s2) should
// suppress the \r\n separator. Returns true for soft wrap, false for hard break.
func isSoftWrap(e1, e0, s0, s1, s2 string) bool {
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
	if e0 == "." {
		if isASCIIDigit(e1) && isASCIIDigit(s0) {
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

// isBoxDrawing reports whether the grapheme is a Box Drawing (U+2500–U+257F) or
// Block Elements (U+2580–U+259F) glyph. Both ranges encode as 3-byte sequences
// prefixed with E2, so matching is done at the byte level.
func isBoxDrawing(g string) bool {
	if len(g) != 3 || g[0] != 0xE2 {
		return false
	}
	b1, b2 := g[1], g[2]
	// U+2500..U+257F = E2 94 80..BF, E2 95 80..BF.
	if (b1 == 0x94 || b1 == 0x95) && b2 >= 0x80 && b2 <= 0xBF {
		return true
	}
	// U+2580..U+259F = E2 96 80..9F.
	return b1 == 0x96 && b2 >= 0x80 && b2 <= 0x9F
}

// isInlineWhitespace reports whether the grapheme is whitespace that is not a hard
// line break. An empty grapheme (null / untouched cell) counts as whitespace. The
// admitted set is tab, NBSP, the U+2000–U+200A block, U+202F and U+205F; hard-break
// controls (\r \n \f \v) are excluded.
func isInlineWhitespace(g string) bool {
	if g == "" || g == " " {
		return true
	}
	switch len(g) {
	case 1:
		// Horizontal tab only; \r \n \f \v and other ASCII are not inline whitespace.
		return g[0] == '\t'
	case 2:
		// Non-breaking space U+00A0 = C2 A0.
		return g[0] == 0xC2 && g[1] == 0xA0
	case 3:
		if g[0] != 0xE2 {
			return false
		}
		// U+2000..U+200A = E2 80 80..8A.
		if g[1] == 0x80 && g[2] >= 0x80 && g[2] <= 0x8A {
			return true
		}
		// U+202F narrow NBSP = E2 80 AF; U+205F mathematical space = E2 81 9F.
		if g[1] == 0x80 && g[2] == 0xAF {
			return true
		}
		return g[1] == 0x81 && g[2] == 0x9F
	}
	return false
}

// isTextFusion reports whether the grapheme belongs to C_text: letters (\p{L}),
// numbers (\p{N}), or intra-paragraph punctuation (P_para). ASCII is classified
// with a byte fast-path; non-ASCII decodes the base rune for the Unicode tables.
// Emoji and other symbols are deliberately excluded (they hard-break at boundaries).
func isTextFusion(g string) bool {
	if g == "" {
		return false
	}
	b0 := g[0]
	if b0 < 0x80 {
		if (b0 >= 'a' && b0 <= 'z') || (b0 >= 'A' && b0 <= 'Z') || (b0 >= '0' && b0 <= '9') {
			return true
		}
		switch b0 {
		case ',', '-', '"', '\'', '(', ')', '[', ']', ':', ';', '/', '\\':
			return true
		}
		return false
	}
	// Non-ASCII: the base rune dictates the linguistic category of the whole cluster.
	r, _ := utf8.DecodeRuneInString(g)
	if unicode.IsLetter(r) || unicode.IsNumber(r) {
		return true
	}
	switch r {
	case '\u2013', '\u2014', // en-dash, em-dash
		'\u201C', '\u201D', // curly double quotes
		'\u2018', '\u2019': // curly single quotes
		return true
	}
	return false
}

// isBulletPrefix reports whether s0, s1, s2 form a bullet-list item start.
func isBulletPrefix(s0, s1, s2 string) bool {
	switch s0 {
	case "-", "*", "+", // ASCII bullets
		"\u2022", "\u2013", "\u2014": // • en-dash em-dash
	default:
		return false
	}
	return isInlineWhitespace(s1) && !isInlineWhitespace(s2)
}

// isDividerChar reports whether the grapheme can form a repeated-divider run.
// All divider characters are single-byte ASCII.
func isDividerChar(g string) bool {
	if len(g) != 1 {
		return false
	}
	switch g[0] {
	case '-', '*', '=', '~', '_', '#':
		return true
	}
	return false
}

func convertUVCellToCell(cell *uv.Cell) Cell {
	if cell == nil {
		return blankCell()
	}
	// UV marks wide-character continuation columns as zero-width placeholders
	// (Width == 0): the trailing half of a double-width cell. These carry no
	// content so ToANSI and Blit skip them.
	if cell.Width == 0 {
		return Cell{}
	}
	// A written-but-empty cell renders as a blank space, preserving its style.
	if cell.Content == "" {
		return Cell{Grapheme: " ", Style: convertUVStyleToStyle(cell.Style)}
	}
	// Preserve the full grapheme cluster (multi-code-point sequences intact).
	return Cell{Grapheme: cell.Content, Style: convertUVStyleToStyle(cell.Style)}
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
