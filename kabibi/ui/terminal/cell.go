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
	// If either w or h is negative, let it panic
	sz := w * h
	if w < 0 && h < 0 {
		sz = -sz
	}
	return CellBuf{Width: w, Height: h, cells: make([]Cell, sz)}
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

// colorPlaneState tracks the active color for one ANSI color plane (fg or bg).
type colorPlaneState struct {
	isDefault   bool
	activeRGB   Color
	activeIndex byte
}

// ansiEncoderState is the mutable serialization state carried across cell transitions.
type ansiEncoderState struct {
	activeAttrs CharAttributes
	fg          colorPlaneState
	bg          colorPlaneState
}

func newEncoderState() ansiEncoderState {
	return ansiEncoderState{
		fg: colorPlaneState{isDefault: true},
		bg: colorPlaneState{isDefault: true},
	}
}

// classifyColor determines the tier and palette index for an explicit (non-default) color.
func classifyColor(rgb Color) (colorTier, byte) {
	for i, c := range ansi16RGB {
		if rgb == c {
			return tier16, byte(i)
		}
	}
	r, g, b := byte(rgb>>16), byte(rgb>>8), byte(rgb)
	idx := rgbTo256(r, g, b)
	if ansi256ToRGB(idx) == rgb {
		return tier256, idx
	}
	return tierTruecolor, idx
}

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

// emitColorPlane emits the minimal SGR escape to transition one color plane to a new state.
func emitColorPlane(sb *strings.Builder, plane *colorPlaneState, rgb Color, isDefault, isFG bool) {
	if isDefault {
		if !plane.isDefault {
			if isFG {
				sb.WriteString("\x1b[39m")
			} else {
				sb.WriteString("\x1b[49m")
			}
			plane.isDefault = true
		}
		return
	}

	tier, index := classifyColor(rgb)
	r, g, b := byte(rgb>>16), byte(rgb>>8), byte(rgb)

	switch tier {
	case tier16:
		if !plane.isDefault && rgb == plane.activeRGB {
			return
		}
		var sgr int
		if isFG {
			if index < 8 {
				sgr = 30 + int(index)
			} else {
				sgr = 90 + int(index-8)
			}
		} else {
			if index < 8 {
				sgr = 40 + int(index)
			} else {
				sgr = 100 + int(index-8)
			}
		}
		sb.WriteString("\x1b[")
		writeInt(sb, sgr)
		sb.WriteByte('m')
		plane.isDefault = false
		plane.activeRGB = rgb
		plane.activeIndex = index

	case tier256:
		if !plane.isDefault && rgb == plane.activeRGB && index == plane.activeIndex {
			return
		}
		if isFG {
			sb.WriteString("\x1b[38;5;")
		} else {
			sb.WriteString("\x1b[48;5;")
		}
		writeInt(sb, int(index))
		sb.WriteByte('m')
		plane.isDefault = false
		plane.activeRGB = rgb
		plane.activeIndex = index

	case tierTruecolor:
		if !plane.isDefault && rgb == plane.activeRGB {
			return
		}
		if !plane.isDefault && index == plane.activeIndex {
			// Same 256-color approximation active: emit truecolor only.
			if isFG {
				sb.WriteString("\x1b[38;2;")
			} else {
				sb.WriteString("\x1b[48;2;")
			}
			writeInt(sb, int(r))
			sb.WriteByte(';')
			writeInt(sb, int(g))
			sb.WriteByte(';')
			writeInt(sb, int(b))
			sb.WriteByte('m')
			plane.isDefault = false
			plane.activeRGB = rgb
		} else {
			// Dual declaration: 256-color for legacy terminals + truecolor for modern.
			if isFG {
				sb.WriteString("\x1b[38;5;")
				writeInt(sb, int(index))
				sb.WriteString("m\x1b[38;2;")
			} else {
				sb.WriteString("\x1b[48;5;")
				writeInt(sb, int(index))
				sb.WriteString("m\x1b[48;2;")
			}
			writeInt(sb, int(r))
			sb.WriteByte(';')
			writeInt(sb, int(g))
			sb.WriteByte(';')
			writeInt(sb, int(b))
			sb.WriteByte('m')
			plane.isDefault = false
			plane.activeRGB = rgb
			plane.activeIndex = index
		}
	}
}

// ToANSI outputs a minimal ANSI byte stream reproducing the buffer visual state.
// Single-pass forward iteration with pendingSpaces deferral eliminates the
// backward trailing-whitespace scan and any intermediate allocations.
func ToANSI(buf CellBuf) string {
	if buf.Width <= 0 || buf.Height <= 0 {
		return ""
	}

	var sb strings.Builder
	sb.Grow(buf.Width * buf.Height)

	enc := newEncoderState()
	const textAttrMask = CharBold | CharDim | CharItalic | CharUnderline | CharBlink

	for y := 0; y < buf.Height; y++ {
		rowBase := y * buf.Width
		pendingSpaces := 0
		lastActiveX := -1

		for x := 0; x < buf.Width; x++ {
			cell := buf.cells[rowBase+x]

			if isTrimmableSpace(cell) {
				pendingSpaces++
				continue
			}

			// Flush deferred spaces before emitting active content.
			if pendingSpaces > 0 {
				flushPendingSpaces(&sb, &enc, pendingSpaces)
				pendingSpaces = 0
			}

			// Attribute delta.
			targetAttrs := cell.Style.Attributes()
			targetTextAttrs := targetAttrs & textAttrMask
			activeTextAttrs := enc.activeAttrs & textAttrMask

			if activeTextAttrs&^targetTextAttrs != 0 {
				sb.WriteString("\x1b[0m")
				enc.activeAttrs = 0
				enc.fg = colorPlaneState{isDefault: true}
				enc.bg = colorPlaneState{isDefault: true}
				activeTextAttrs = 0
			}

			if needed := targetTextAttrs &^ activeTextAttrs; needed != 0 {
				first := true
				sb.WriteString("\x1b[")
				for _, p := range [...]struct {
					flag CharAttributes
					code byte
				}{
					{CharBold, '1'}, {CharDim, '2'}, {CharItalic, '3'},
					{CharUnderline, '4'}, {CharBlink, '5'},
				} {
					if needed&p.flag != 0 {
						if !first {
							sb.WriteByte(';')
						}
						sb.WriteByte(p.code)
						first = false
					}
				}
				sb.WriteByte('m')
				enc.activeAttrs = targetTextAttrs
			}

			emitColorPlane(&sb, &enc.fg, cell.Style.Foreground(),
				targetAttrs&CharResetForegroundDefault != 0, true)
			emitColorPlane(&sb, &enc.bg, cell.Style.Background(),
				targetAttrs&CharResetBackgroundDefault != 0, false)

			r := cell.R
			if r == 0 {
				r = ' '
			}
			sb.WriteRune(r)
			lastActiveX = x
		}

		// Row termination: trailing spaces held in pendingSpaces are implicitly trimmed
		// unless CharSoftWrap on the last cell marks them as structural wrap padding.
		lastCell := buf.cells[rowBase+buf.Width-1]
		hasSoftWrap := lastCell.Style.HasAttribute(CharSoftWrap)

		if pendingSpaces > 0 && hasSoftWrap {
			flushPendingSpaces(&sb, &enc, pendingSpaces)
			// suppress \r\n: soft-wrap continuation
		} else if lastActiveX == buf.Width-1 && hasSoftWrap {
			// suppress \r\n: active content reached the soft-wrap edge
		} else {
			sb.WriteString("\r\n")
		}
	}

	return sb.String()
}

// isTrimmableSpace reports whether a cell can be held as a deferred space counter
// rather than written immediately. Spaces are trimmable when they carry no visible
// decoration that would be lost by deferral.
func isTrimmableSpace(cell Cell) bool {
	return (cell.R == ' ' || cell.R == 0) &&
		cell.Style.HasAttribute(CharResetBackgroundDefault) &&
		!cell.Style.HasAttribute(CharUnderline)
}

// flushPendingSpaces writes count deferred space characters, emitting a BG/underline
// safety escape first if the active encoder state would corrupt the spaces visually.
func flushPendingSpaces(sb *strings.Builder, enc *ansiEncoderState, count int) {
	if enc.activeAttrs&CharUnderline != 0 {
		// Full reset needed: underline on a space cell is a visible decoration.
		sb.WriteString("\x1b[0m")
		enc.activeAttrs = 0
		enc.fg = colorPlaneState{isDefault: true}
		enc.bg = colorPlaneState{isDefault: true}
	} else if !enc.bg.isDefault {
		// BG-only clear: spaces would inherit the active background color.
		sb.WriteString("\x1b[49m")
		enc.bg = colorPlaneState{isDefault: true}
	}
	for range count {
		sb.WriteByte(' ')
	}
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
	if cell == nil || cell.IsZero() {
		defaultStyle := NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)
		return Cell{R: ' ', Style: defaultStyle}
	}

	r := ' '
	if content := cell.Content; content != "" {
		runes := []rune(content)
		if len(runes) > 0 {
			r = runes[0]
		}
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
