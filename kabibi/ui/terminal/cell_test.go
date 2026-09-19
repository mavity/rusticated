package terminal

import (
	"strings"
	"testing"
)

// assertGridRunes checks the flat string against the CellBuf using i = y*W + x layout.
func assertGridRunes(t *testing.T, buf CellBuf, expected string) {
	t.Helper()
	for i, want := range []rune(expected) {
		x, y := i%buf.Width, i/buf.Width
		if got := buf.Get(x, y).R; got != want {
			t.Errorf("grid[%d] (%d,%d): want %q got %q", i, x, y, want, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 1a – Style bit-packing
// ─────────────────────────────────────────────────────────────────────────────

func TestStyleColorRoundTrip(t *testing.T) {
	cases := []struct {
		fg, bg Color
	}{
		{0x000000, 0x000000},
		{0xFFFFFF, 0xFFFFFF},
		{0x123456, 0xABCDEF},
		{0xFF0000, 0x00FF00},
		{0x0000FF, 0xFF00FF},
	}
	for _, tc := range cases {
		s := NewStyle(tc.fg, tc.bg, 0)
		if got := s.Foreground(); got != tc.fg {
			t.Errorf("fg 0x%06X: round-trip got 0x%06X", tc.fg, got)
		}
		if got := s.Background(); got != tc.bg {
			t.Errorf("bg 0x%06X: round-trip got 0x%06X", tc.bg, got)
		}
	}
}

func TestStyleColorIsolation(t *testing.T) {
	base := NewStyle(0xFF0000, 0x00FF00, CharBold|CharItalic)

	if s := base.WithForeground(0x123456); s.Background() != 0x00FF00 || s.Attributes() != CharBold|CharItalic {
		t.Error("WithForeground polluted background or attributes")
	}
	if s := base.WithBackground(0xABCDEF); s.Foreground() != 0xFF0000 || s.Attributes() != CharBold|CharItalic {
		t.Error("WithBackground polluted foreground or attributes")
	}
	if s := base.WithAttributes(CharUnderline); s.Foreground() != 0xFF0000 || s.Background() != 0x00FF00 {
		t.Error("WithAttributes polluted color fields")
	}
}

func TestStyleAttributeFlags(t *testing.T) {
	allFlags := []CharAttributes{
		CharResetForegroundDefault,
		CharResetBackgroundDefault,
		CharBold, CharDim, CharItalic,
		CharUnderline, CharBlink, CharSoftWrap,
	}
	for _, f := range allFlags {
		s := NewStyle(0, 0, 0).AddAttribute(f)
		if !s.HasAttribute(f) {
			t.Errorf("AddAttribute(0x%04X): flag not set", f)
		}
		for _, other := range allFlags {
			if other != f && s.HasAttribute(other) {
				t.Errorf("AddAttribute(0x%04X): leaked into flag 0x%04X", f, other)
			}
		}
		if s.RemoveAttribute(f).HasAttribute(f) {
			t.Errorf("RemoveAttribute(0x%04X): flag still set", f)
		}
	}
}

func TestStyleMultipleFlags(t *testing.T) {
	combo := CharBold | CharItalic | CharUnderline | CharSoftWrap
	s := NewStyle(0xFF0000, 0x00FF00, combo)
	if s.Attributes() != combo {
		t.Errorf("Attributes: want 0x%04X got 0x%04X", combo, s.Attributes())
	}
	s = s.RemoveAttribute(CharItalic)
	want := CharBold | CharUnderline | CharSoftWrap
	if s.Attributes() != want {
		t.Errorf("after RemoveAttribute(CharItalic): want 0x%04X got 0x%04X", want, s.Attributes())
	}
	if s.Foreground() != 0xFF0000 || s.Background() != 0x00FF00 {
		t.Error("attribute mutation changed color fields")
	}
}

func TestStyleDefaultResetFlagsCoexistWithColors(t *testing.T) {
	s := NewStyle(0x123456, 0xABCDEF, CharResetForegroundDefault|CharResetBackgroundDefault)
	if !s.HasAttribute(CharResetForegroundDefault) {
		t.Error("CharResetForegroundDefault not set")
	}
	if !s.HasAttribute(CharResetBackgroundDefault) {
		t.Error("CharResetBackgroundDefault not set")
	}
	if s.Foreground() != 0x123456 {
		t.Errorf("Foreground: want 0x123456 got 0x%06X", s.Foreground())
	}
	if s.Background() != 0xABCDEF {
		t.Errorf("Background: want 0xABCDEF got 0x%06X", s.Background())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 1b – CellBuf spatial primitives
// ─────────────────────────────────────────────────────────────────────────────

func TestNewCellBufDimensions(t *testing.T) {
	cases := []struct{ w, h, wantW, wantH int }{
		{5, 3, 5, 3},
		{0, 0, 0, 0},
		{10, 0, 10, 0},
		{0, 5, 0, 5},
	}
	for _, tc := range cases {
		buf := NewCellBuf(tc.w, tc.h)
		if buf.Width != tc.wantW || buf.Height != tc.wantH {
			t.Errorf("NewCellBuf(%d,%d): got %dx%d want %dx%d",
				tc.w, tc.h, buf.Width, buf.Height, tc.wantW, tc.wantH)
		}
	}
}

func TestCellBufOutOfBoundsSilent(t *testing.T) {
	buf := NewCellBuf(3, 2)
	cell := Cell{R: 'X', Style: NewStyle(0xFF0000, 0, CharBold)}

	oob := [][2]int{{-1, 0}, {0, -1}, {3, 0}, {0, 2}, {-1, -1}, {99, 99}}
	for _, c := range oob {
		x, y := c[0], c[1]
		if got := buf.Get(x, y); got != (Cell{}) {
			t.Errorf("Get(%d,%d): want zero Cell, got non-zero", x, y)
		}
		buf.Set(x, y, cell)
	}
	for y := 0; y < buf.Height; y++ {
		for x := 0; x < buf.Width; x++ {
			if buf.Get(x, y) != (Cell{}) {
				t.Errorf("in-bounds (%d,%d) corrupted by OOB Set", x, y)
			}
		}
	}
}

func TestCellBufSetGetRoundTrip(t *testing.T) {
	buf := NewCellBuf(3, 2)
	cell := Cell{R: 'Z', Style: NewStyle(0x0000FF, 0xFF0000, CharItalic)}
	buf.Set(1, 1, cell)
	if got := buf.Get(1, 1); got != cell {
		t.Errorf("Get(1,1) after Set: want %v got %v", cell, got)
	}
	if got := buf.Get(0, 1); got != (Cell{}) {
		t.Error("adjacent cell unexpectedly non-zero after Set(1,1)")
	}
}

func TestCellBufFillInsideBounds(t *testing.T) {
	buf := NewCellBuf(5, 5)
	mark := Cell{R: '#'}
	buf.Fill(Rect{X: 1, Y: 1, W: 3, H: 2}, mark)
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			inside := x >= 1 && x < 4 && y >= 1 && y < 3
			got := buf.Get(x, y)
			if inside && got != mark {
				t.Errorf("Fill: (%d,%d) inside rect should be mark", x, y)
			}
			if !inside && got != (Cell{}) {
				t.Errorf("Fill: (%d,%d) outside rect should be zero", x, y)
			}
		}
	}
}

func TestCellBufFillNegativeOffsetClipping(t *testing.T) {
	buf := NewCellBuf(3, 3)
	mark := Cell{R: '+'}
	buf.Fill(Rect{X: -1, Y: -1, W: 3, H: 3}, mark)
	if buf.Get(0, 0) != mark {
		t.Error("(0,0) should be filled by clipped rect")
	}
	if buf.Get(2, 2) == mark {
		t.Error("(2,2) should NOT be filled (outside clipped area)")
	}
}

func TestCellBufFillOverflowClipping(t *testing.T) {
	buf := NewCellBuf(3, 3)
	mark := Cell{R: '+'}
	buf.Fill(Rect{X: 2, Y: 2, W: 5, H: 5}, mark)
	if buf.Get(2, 2) != mark {
		t.Error("(2,2) should be filled")
	}
	if buf.Get(0, 0) != (Cell{}) {
		t.Error("(0,0) should remain zero")
	}
}

func TestCellBufBlitSkipsZeroCells(t *testing.T) {
	dst := NewCellBuf(3, 1)
	active := Cell{R: 'A', Style: NewStyle(0xFF0000, 0, 0)}
	for x := 0; x < 3; x++ {
		dst.Set(x, 0, active)
	}

	src := NewCellBuf(3, 1)
	src.Set(1, 0, Cell{R: 'B'})

	dst.Blit(src, 0, 0)

	if dst.Get(0, 0) != active {
		t.Error("zero src cell should not overwrite dst at (0,0)")
	}
	if dst.Get(1, 0).R != 'B' {
		t.Error("non-zero src cell should overwrite dst at (1,0)")
	}
	if dst.Get(2, 0) != active {
		t.Error("zero src cell should not overwrite dst at (2,0)")
	}
}

func TestCellBufBlitOutOfBoundsOffset(t *testing.T) {
	dst := NewCellBuf(3, 3)
	src := NewCellBuf(2, 2)
	src.Set(0, 0, Cell{R: 'X'})
	dst.Blit(src, 10, 10)
	dst.Blit(src, -5, -5)
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			if dst.Get(x, y) != (Cell{}) {
				t.Errorf("dst(%d,%d) should be zero after OOB Blit", x, y)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 1c – rgbTo256 color quantization
// ─────────────────────────────────────────────────────────────────────────────

func TestRgbTo256Grayscale(t *testing.T) {
	cases := []struct {
		v    byte
		want byte
		desc string
	}{
		{0, 16, "pure black (r < 8)"},
		{7, 16, "darkest gray below threshold"},
		{8, 232, "first grayscale ramp entry"},
		{128, 243, "mid-gray"},
		{248, 254, "last grayscale ramp entry"},
		{249, 231, "first value above ramp threshold maps to cube white"},
		{255, 231, "pure white (r > 248)"},
	}
	for _, tc := range cases {
		if got := rgbTo256(tc.v, tc.v, tc.v); got != tc.want {
			t.Errorf("rgbTo256(%d,%d,%d) [%s]: want %d got %d",
				tc.v, tc.v, tc.v, tc.desc, tc.want, got)
		}
	}
}

func TestRgbTo256ColorCubePrimaries(t *testing.T) {
	cases := []struct {
		r, g, b byte
		want    byte
		desc    string
	}{
		{255, 0, 0, 196, "pure red"},
		{0, 255, 0, 46, "pure green"},
		{0, 0, 255, 21, "pure blue"},
		{255, 255, 0, 226, "yellow"},
		{255, 0, 255, 201, "magenta"},
		{0, 255, 255, 51, "cyan"},
	}
	for _, tc := range cases {
		if got := rgbTo256(tc.r, tc.g, tc.b); got != tc.want {
			t.Errorf("rgbTo256(%d,%d,%d) [%s]: want %d got %d",
				tc.r, tc.g, tc.b, tc.desc, tc.want, got)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 2 – FromANSI structural & height-sizing correctness
// ─────────────────────────────────────────────────────────────────────────────

func TestFromANSIEmptyInputs(t *testing.T) {
	if buf := FromANSI(nil, 10); buf.Width != 10 || buf.Height != 0 {
		t.Errorf("nil lines: want 10×0, got %d×%d", buf.Width, buf.Height)
	}
	if buf := FromANSI([]string{}, 10); buf.Width != 10 || buf.Height != 0 {
		t.Errorf("empty slice: want 10×0, got %d×%d", buf.Width, buf.Height)
	}
	if buf := FromANSI([]string{""}, 10); buf.Height != 0 {
		t.Errorf("single empty string: want height 0, got %d", buf.Height)
	}
}

func TestFromANSISingleChar(t *testing.T) {
	buf := FromANSI([]string{"A"}, 10)
	if buf.Width != 10 || buf.Height != 1 {
		t.Fatalf("want 10×1, got %d×%d", buf.Width, buf.Height)
	}
	if buf.Get(0, 0).R != 'A' {
		t.Errorf("Get(0,0): want 'A', got %q", buf.Get(0, 0).R)
	}
}

func TestFromANSIWrapsToMultipleRows(t *testing.T) {
	// "Hello World" (11 chars) at width 5 → 3 rows.
	buf := FromANSI([]string{"Hello World"}, 5)
	if buf.Height != 3 {
		t.Fatalf("want height 3, got %d", buf.Height)
	}
	assertGridRunes(t, buf, "Hello World")
}

func TestFromANSIExactWidthBoundary(t *testing.T) {
	const w = 10
	buf := FromANSI([]string{strings.Repeat("x", w)}, w)
	if buf.Height != 1 {
		t.Errorf("exact width: want height 1, got %d", buf.Height)
	}
}

func TestFromANSIOneCharOverWidth(t *testing.T) {
	const w = 10
	buf := FromANSI([]string{strings.Repeat("x", w+1)}, w)
	if buf.Height != 2 {
		t.Errorf("width+1 chars: want height 2, got %d", buf.Height)
	}
}

func TestFromANSILeadingNewlinePreservesEmptyRow(t *testing.T) {
	buf := FromANSI([]string{"\r\nABC"}, 10)
	if buf.Height != 2 {
		t.Fatalf("leading newline: want height 2, got %d", buf.Height)
	}
	for x := 0; x < buf.Width; x++ {
		r := buf.Get(x, 0).R
		if r != 0 && r != ' ' {
			t.Errorf("row 0 col %d should be blank, got %q", x, r)
		}
	}
	if buf.Get(0, 1).R != 'A' {
		t.Errorf("row 1 col 0: want 'A', got %q", buf.Get(0, 1).R)
	}
}

func TestFromANSIScrollbackTotalHeight(t *testing.T) {
	// 30 one-character lines exceed the 24-row viewport by 6; total height must be 30.
	const lineCount = 30
	lines := make([]string, lineCount)
	for i := range lines {
		lines[i] = "x"
	}
	buf := FromANSI(lines, 10)
	if buf.Height != lineCount {
		t.Errorf("scrollback height: want %d, got %d", lineCount, buf.Height)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 3 – Control-sequence & ANSI state-machine interaction
// ─────────────────────────────────────────────────────────────────────────────

func TestFromANSIBoldColorSGR(t *testing.T) {
	// \x1b[1;31m = bold + ANSI red foreground (index 1 → RGB 0x800000).
	buf := FromANSI([]string{"\x1b[1;31mHi\x1b[0m"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	if buf.Get(0, 0).R != 'H' || buf.Get(1, 0).R != 'i' {
		t.Errorf("rune mismatch: got %q %q, want 'H' 'i'", buf.Get(0, 0).R, buf.Get(1, 0).R)
	}
	for _, x := range []int{0, 1} {
		c := buf.Get(x, 0)
		if !c.Style.HasAttribute(CharBold) {
			t.Errorf("col %d: expected CharBold", x)
		}
		if c.Style.HasAttribute(CharResetForegroundDefault) {
			t.Errorf("col %d: expected explicit fg, got reset-default", x)
		}
		if c.Style.Foreground() != Color(0x800000) {
			t.Errorf("col %d: fg want 0x800000 got 0x%06X", x, c.Style.Foreground())
		}
	}
}

func TestFromANSI256ColorForeground(t *testing.T) {
	// \x1b[38;5;196m = 256-color index 196 → xterm RGB 0xFF0000.
	buf := FromANSI([]string{"\x1b[38;5;196mX"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	c := buf.Get(0, 0)
	if c.Style.HasAttribute(CharResetForegroundDefault) {
		t.Error("expected explicit fg, got reset-default")
	}
	if c.Style.Foreground() != Color(0xFF0000) {
		t.Errorf("256-color fg: want 0xFF0000, got 0x%06X", c.Style.Foreground())
	}
}

func TestFromANSITruecolorBackground(t *testing.T) {
	// \x1b[48;2;10;20;30m = direct 24-bit background 0x0A141E.
	const wantBg = Color(0x0A141E)
	buf := FromANSI([]string{"\x1b[48;2;10;20;30mX"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	c := buf.Get(0, 0)
	if c.Style.HasAttribute(CharResetBackgroundDefault) {
		t.Error("expected explicit bg, got reset-default")
	}
	if c.Style.Background() != wantBg {
		t.Errorf("truecolor bg: want 0x%06X, got 0x%06X", wantBg, c.Style.Background())
	}
}

func TestFromANSISGRResetIsolation(t *testing.T) {
	// Cells inside \x1b[31m carry explicit fg; cells after \x1b[0m carry reset-default.
	buf := FromANSI([]string{"\x1b[31mRed\x1b[0m Plain"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	for x := 0; x < 3; x++ {
		if buf.Get(x, 0).Style.HasAttribute(CharResetForegroundDefault) {
			t.Errorf("col %d: should have explicit fg inside colored span", x)
		}
	}
	for x := 3; x <= 8; x++ {
		if !buf.Get(x, 0).Style.HasAttribute(CharResetForegroundDefault) {
			t.Errorf("col %d: should have CharResetForegroundDefault after SGR reset", x)
		}
	}
}

func TestFromANSICursorAbsolutePositioning(t *testing.T) {
	// \x1b[2;3H moves cursor to row 2, col 3 (1-indexed) = (x=2, y=1) 0-indexed.
	buf := FromANSI([]string{"\x1b[2;3HText"}, 20)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	for i, want := range []rune("Text") {
		x := 2 + i
		if got := buf.Get(x, 1).R; got != want {
			t.Errorf("(%d,1): want %q, got %q", x, want, got)
		}
	}
	for x := 0; x < 2; x++ {
		r := buf.Get(x, 1).R
		if r != 0 && r != ' ' {
			t.Errorf("(%d,1) before text: want blank, got %q", x, r)
		}
	}
}

func TestFromANSICursorRelativeHorizontalMove(t *testing.T) {
	// Move right 5, write 'A', move left 2, write 'B'.
	// Expected: cols 0–3 blank, col 4 = 'B', col 5 = 'A'.
	buf := FromANSI([]string{"\x1b[5CA\x1b[2DB"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	for x := 0; x < 4; x++ {
		r := buf.Get(x, 0).R
		if r != 0 && r != ' ' {
			t.Errorf("col %d: should be blank after cursor skip, got %q", x, r)
		}
	}
	if buf.Get(4, 0).R != 'B' {
		t.Errorf("col 4: want 'B', got %q", buf.Get(4, 0).R)
	}
	if buf.Get(5, 0).R != 'A' {
		t.Errorf("col 5: want 'A', got %q", buf.Get(5, 0).R)
	}
}

func TestFromANSIEraseInLine(t *testing.T) {
	// Write "Hello", move cursor left 1, erase to EOL.
	buf := FromANSI([]string{"Hello\x1b[1D\x1b[K"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	for i, want := range []rune("Hell") {
		if got := buf.Get(i, 0).R; got != want {
			t.Errorf("col %d: want %q, got %q", i, want, got)
		}
	}
	r := buf.Get(4, 0).R
	if r != 0 && r != ' ' {
		t.Errorf("col 4 after erase: want blank, got %q", r)
	}
}

func TestFromANSISoftWrapMarkedOnAutoWrappedRow(t *testing.T) {
	// A string longer than width causes the first row to auto-wrap.
	const w = 10
	buf := FromANSI([]string{strings.Repeat("x", w+5)}, w)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	if !buf.Get(w-1, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("auto-wrapped row: cell at (w-1, 0) should have CharSoftWrap")
	}
}

func TestFromANSIHardBreakNoSoftWrap(t *testing.T) {
	// Short explicit lines that don't reach the right margin must not get CharSoftWrap.
	const w = 10
	buf := FromANSI([]string{"12345", "67890"}, w)
	if buf.Height != 2 {
		t.Fatalf("want height 2, got %d", buf.Height)
	}
	if buf.Get(w-1, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0: should NOT have CharSoftWrap (line ends before right margin)")
	}
	if buf.Get(w-1, 1).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 1: should NOT have CharSoftWrap (line ends before right margin)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 4a – Soft-wrap character classifiers
// ─────────────────────────────────────────────────────────────────────────────

func TestIsBoxDrawingRanges(t *testing.T) {
	cases := []struct {
		r    rune
		want bool
	}{
		{0x24FF, false}, // just before box drawing
		{0x2500, true},  // first box drawing
		{0x2550, true},  // mid box drawing
		{0x257F, true},  // last box drawing
		{0x2580, true},  // first block element
		{0x2590, true},  // mid block element
		{0x259F, true},  // last block element
		{0x25A0, false}, // just after block elements
		{'A', false},
		{'-', false},
		{' ', false},
	}
	for _, tc := range cases {
		if got := isBoxDrawing(tc.r); got != tc.want {
			t.Errorf("isBoxDrawing(U+%04X): want %v got %v", tc.r, tc.want, got)
		}
	}
}

func TestIsInlineWhitespace(t *testing.T) {
	cases := []struct {
		r    rune
		want bool
	}{
		{0, true},        // null / empty cell
		{' ', true},      // standard space
		{'\t', true},     // horizontal tab
		{'\u00A0', true}, // non-breaking space
		{'\r', false},
		{'\n', false},
		{'\f', false},
		{'\v', false},
		{'a', false},
		{'.', false},
		{'-', false},
	}
	for _, tc := range cases {
		if got := isInlineWhitespace(tc.r); got != tc.want {
			t.Errorf("isInlineWhitespace(%q): want %v got %v", tc.r, tc.want, got)
		}
	}
}

func TestIsTextFusion(t *testing.T) {
	cases := []struct {
		r    rune
		want bool
		desc string
	}{
		{'a', true, "ASCII letter"},
		{'Z', true, "uppercase letter"},
		{'é', true, "accented letter"},
		{'中', true, "CJK ideograph"},
		{'5', true, "digit"},
		{'²', true, "superscript (No category)"},
		{'\U0001F600', true, "emoji (So category)"},
		{',', true, "comma"},
		{'-', true, "hyphen"},
		{'\u2013', true, "en-dash"},
		{'\u2014', true, "em-dash"},
		{'"', true, "double quote"},
		{'\'', true, "apostrophe"},
		{'\u201C', true, "left curly double quote"},
		{'\u201D', true, "right curly double quote"},
		{'\u2018', true, "left curly single quote"},
		{'\u2019', true, "right curly single quote"},
		{'(', true, "left paren"},
		{')', true, "right paren"},
		{'[', true, "left bracket"},
		{']', true, "right bracket"},
		{':', true, "colon"},
		{';', true, "semicolon"},
		{'/', true, "forward slash"},
		{'\\', true, "backslash"},
		{' ', false, "space"},
		{'.', false, "period"},
		{'!', false, "exclamation"},
		{'?', false, "question mark"},
		{'@', false, "at sign"},
		{0x2500, false, "box drawing char"},
	}
	for _, tc := range cases {
		if got := isTextFusion(tc.r); got != tc.want {
			t.Errorf("isTextFusion(%q) [%s]: want %v got %v", tc.r, tc.desc, tc.want, got)
		}
	}
}

func TestIsBulletPrefixPatterns(t *testing.T) {
	cases := []struct {
		s0, s1, s2 rune
		want       bool
		desc       string
	}{
		{'-', ' ', 'x', true, "hyphen bullet"},
		{'*', ' ', 'a', true, "asterisk bullet"},
		{'\u2022', ' ', 'z', true, "• bullet"},
		{'+', ' ', '1', true, "plus bullet"},
		{'\u2013', ' ', 'b', true, "en-dash bullet"},
		{'\u2014', ' ', 'c', true, "em-dash bullet"},
		{'-', '\t', 'x', true, "tab as bullet spacer"},
		{'-', 'x', 'y', false, "s1 not whitespace"},
		{'-', ' ', ' ', false, "s2 is whitespace"},
		{'a', ' ', 'x', false, "s0 not bullet char"},
		{'.', ' ', 'x', false, "period not bullet"},
	}
	for _, tc := range cases {
		if got := isBulletPrefix(tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("isBulletPrefix(%q,%q,%q) [%s]: want %v got %v",
				tc.s0, tc.s1, tc.s2, tc.desc, tc.want, got)
		}
	}
}

func TestIsDividerCharSet(t *testing.T) {
	for _, r := range []rune{'-', '*', '=', '~', '_', '#'} {
		if !isDividerChar(r) {
			t.Errorf("isDividerChar(%q): want true", r)
		}
	}
	for _, r := range []rune{'a', '.', '|', '+', '!', '/', '\\', ' ', '\u2013'} {
		if isDividerChar(r) {
			t.Errorf("isDividerChar(%q): want false", r)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 4b – isSoftWrap exclusion and inclusion pipeline
// ─────────────────────────────────────────────────────────────────────────────

func TestIsSoftWrapExclusionBoxDrawing(t *testing.T) {
	const box = rune(0x2500)
	const l = rune('a')
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
	}{
		{box, l, l, l, l, false}, // e1 is box drawing
		{l, box, l, l, l, false}, // e0 is box drawing
		{l, l, box, l, l, false}, // s0 is box drawing
		{l, l, l, box, l, false}, // s1 is box drawing
		// s2 is NOT checked by exclusion 1; P1 fires on letter/letter
		{l, l, l, l, box, true},
	}
	for i, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("case %d: want %v got %v", i, tc.want, got)
		}
	}
}

func TestIsSoftWrapExclusionBulletPrefix(t *testing.T) {
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
		desc               string
	}{
		{'x', 'a', '-', ' ', 'i', false, "hyphen bullet"},
		{'x', 'a', '*', ' ', 'i', false, "asterisk bullet"},
		{'x', 'a', '\u2022', ' ', 'i', false, "• bullet"},
		// s1 not whitespace → bullet exclusion skipped → P1 fires on 'a'/'-'
		{'x', 'a', '-', 'x', 'i', true, "s1 not whitespace"},
		// s2 is whitespace → bullet exclusion skipped → P1 fires on 'a'/'-'
		{'x', 'a', '-', ' ', ' ', true, "s2 is whitespace"},
		// exclusion 2 fires before P2a; without it P2a would return true
		{'a', ' ', '-', ' ', 'i', false, "bullet overrides P2a"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] isSoftWrap(%q,%q,%q,%q,%q): want %v got %v",
				tc.desc, tc.e1, tc.e0, tc.s0, tc.s1, tc.s2, tc.want, got)
		}
	}
}

func TestIsSoftWrapExclusionLineDivider(t *testing.T) {
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
		desc               string
	}{
		{'x', 'a', '-', '-', '-', false, "---"},
		{'x', 'a', '=', '=', '=', false, "==="},
		{'x', 'a', '*', '*', '*', false, "***"},
		{'x', 'a', '~', '~', '~', false, "~~~"},
		{'x', 'a', '_', '_', '_', false, "___"},
		{'x', 'a', '#', '#', '#', false, "###"},
		// Only 2 repeated chars: not a divider sequence
		{'x', 'a', '-', '-', 'x', true, "-- then text"},
		// Mixed: not a divider
		{'x', 'a', '-', '=', '-', true, "mixed symbols"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] want %v got %v", tc.desc, tc.want, got)
		}
	}
}

func TestIsSoftWrapExclusionIndentation(t *testing.T) {
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
		desc               string
	}{
		{'x', 'a', ' ', ' ', 'x', false, "double space indent"},
		{'x', 'a', ' ', ' ', ' ', false, "all spaces (empty row)"},
		{'x', 'a', '\t', '\t', 'x', false, "double tab indent"},
		// Single leading space: s1 is non-whitespace → exclusion 4 does not fire → P2b fires
		{'x', 'a', ' ', 'b', 'c', true, "single leading space → P2b"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] want %v got %v", tc.desc, tc.want, got)
		}
	}
}

func TestIsSoftWrapExclusionFullStop(t *testing.T) {
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
		desc               string
	}{
		{'a', '.', 'N', 'e', 'x', false, "sentence end"},
		{'d', '.', 'W', 'o', 'r', false, "word.Word"},
		// Flanked decimal exception: both neighbouring chars are digits
		{'3', '.', '1', '4', '1', true, "flanked decimal 3./1..."},
		{'0', '.', '5', '0', '0', true, "flanked decimal 0./5..."},
		// Not flanked: one side is a non-digit
		{'a', '.', '5', '0', '0', false, "e1 not digit"},
		{'3', '.', 'a', 'b', 'c', false, "s0 not digit"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] isSoftWrap(%q,%q,%q,...): want %v got %v",
				tc.desc, tc.e1, tc.e0, tc.s0, tc.want, got)
		}
	}
}

func TestIsSoftWrapInclusionP1(t *testing.T) {
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
		desc               string
	}{
		{'x', 'a', 'b', 'c', 'd', true, "letter–letter"},
		{'x', 'a', '1', '2', '3', true, "letter–digit"},
		{'x', '1', 'a', 'b', 'c', true, "digit–letter"},
		{'x', ',', 'a', 'b', 'c', true, "para-punct–letter"},
		{'x', 'a', ',', 'b', 'c', true, "letter–para-punct"},
		// e0 not C_text: P1 and P2b fail; P2a also fails (e0 not S_inline)
		{'x', '!', 'a', 'b', 'c', false, "e0='!' not C_text"},
		// s0 not C_text and not S_inline: no inclusion rule fires
		{'x', 'a', '!', 'b', 'c', false, "s0='!' not C_text"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] isSoftWrap(%q,%q,%q,...): want %v got %v",
				tc.desc, tc.e1, tc.e0, tc.s0, tc.want, got)
		}
	}
}

func TestIsSoftWrapInclusionP2a(t *testing.T) {
	// P2a: e1∈C_text, e0∈S_inline, s0∈C_text
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
		desc               string
	}{
		{'a', ' ', 'b', 'c', 'd', true, "letter–space–letter"},
		{'1', ' ', 'a', 'b', 'c', true, "digit–space–letter"},
		{' ', ' ', 'b', 'c', 'd', false, "e1 space: not C_text"},
		{'a', ' ', '!', 'b', 'c', false, "s0 '!': not C_text"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] want %v got %v", tc.desc, tc.want, got)
		}
	}
}

func TestIsSoftWrapInclusionP2b(t *testing.T) {
	// P2b: e0∈C_text, s0∈S_inline, s1∈C_text
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		want               bool
		desc               string
	}{
		{'x', 'a', ' ', 'b', 'c', true, "letter–space–letter"},
		{'x', 'a', '\u00A0', 'b', 'c', true, "letter–NBSP–letter"},
		// e0 not C_text: P2b fails
		{'x', '!', ' ', 'b', 'c', false, "e0 '!' not C_text"},
		// double space triggers exclusion 4 before P2b is reached
		{'x', 'a', ' ', ' ', 'b', false, "double space triggers exclusion 4"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] want %v got %v", tc.desc, tc.want, got)
		}
	}
}

func TestIsSoftWrapFallsThrough(t *testing.T) {
	// No exclusion fires and no inclusion rule matches → hard break.
	cases := []struct {
		e1, e0, s0, s1, s2 rune
		desc               string
	}{
		{'x', '!', '?', 'a', 'b', "! and ? not in C_text"},
		{'x', '@', 'a', 'b', 'c', "@ not in C_text"},
		{' ', ' ', 'a', 'b', 'c', "both trailing spaces: no inclusion rule fires"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got {
			t.Errorf("[%s] isSoftWrap(%q,%q,...): want false got true", tc.desc, tc.e1, tc.e0)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 4c – FromANSI soft-wrap content-detection integration
// ─────────────────────────────────────────────────────────────────────────────

func TestFromANSISoftWrapP1AtRightEdge(t *testing.T) {
	// "Hello" fills width=5 exactly → e0='o', s0='w' → P1 fires.
	buf := FromANSI([]string{"Hello", "world"}, 5)
	if buf.Height != 2 {
		t.Fatalf("want height 2, got %d", buf.Height)
	}
	if !buf.Get(4, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0 right edge: want CharSoftWrap (P1 text fusion)")
	}
}

func TestFromANSISoftWrapP2aOneTrailingSpace(t *testing.T) {
	// "Hello" at width=6 leaves one trailing space → e1='o', e0=' ', s0='w' → P2a fires.
	buf := FromANSI([]string{"Hello", "world"}, 6)
	if buf.Height != 2 {
		t.Fatalf("want height 2, got %d", buf.Height)
	}
	if !buf.Get(5, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0 right edge: want CharSoftWrap (P2a trailing space)")
	}
}

func TestFromANSIHardBreakBulletListNextRow(t *testing.T) {
	// Row 0 ends flush with "text" (e0='t'); row 1 starts "- item" → exclusion 2 fires.
	// Without the bullet guard, P1 would fire since isTextFusion('t') && isTextFusion('-').
	buf := FromANSI([]string{"text", "- item"}, 4)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	if buf.Get(3, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0: bullet prefix on next row must suppress CharSoftWrap")
	}
}

func TestFromANSIHardBreakLineDividerNextRow(t *testing.T) {
	// Row 1 starts "---" → exclusion 3 fires.
	buf := FromANSI([]string{"text", "---"}, 4)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	if buf.Get(3, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0: line divider on next row must suppress CharSoftWrap")
	}
}

func TestFromANSIHardBreakDoubleIndentNextRow(t *testing.T) {
	// Row 1 starts "  ok" (two leading spaces) → exclusion 4 fires.
	buf := FromANSI([]string{"text", "  ok"}, 4)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	if buf.Get(3, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0: double-space indent on next row must suppress CharSoftWrap")
	}
}

func TestFromANSIHardBreakSentenceEndPeriod(t *testing.T) {
	// "end." → e0='.', e1='d'; next row starts with a letter → exclusion 5 fires.
	buf := FromANSI([]string{"end.", "Next"}, 4)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	if buf.Get(3, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0: sentence-ending period must suppress CharSoftWrap")
	}
}

func TestFromANSISoftWrapFlankDecimal(t *testing.T) {
	// "3." / "14": e0='.', e1='3', s0='1' → flanked decimal exception → soft wrap.
	buf := FromANSI([]string{"3.", "14"}, 2)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	if !buf.Get(1, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0: flanked decimal must set CharSoftWrap")
	}
}

func TestFromANSIHardBreakBoxDrawingAtEdge(t *testing.T) {
	// Row 0 ends with U+2500 (─) → exclusion 1 fires.
	buf := FromANSI([]string{"a\u2500", "text"}, 2)
	if buf.Height < 2 {
		t.Fatalf("want height >= 2, got %d", buf.Height)
	}
	if buf.Get(1, 0).Style.HasAttribute(CharSoftWrap) {
		t.Error("row 0: box-drawing char at right edge must suppress CharSoftWrap")
	}
}
