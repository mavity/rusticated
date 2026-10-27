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
		if got := buf.Get(x, y).Grapheme; got != string(want) {
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
	cell := Cell{Grapheme: "X", Style: NewStyle(0xFF0000, 0, CharBold)}

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
			if buf.Get(x, y) != blankCell() {
				t.Errorf("in-bounds (%d,%d) corrupted by OOB Set", x, y)
			}
		}
	}
}

func TestCellBufSetGetRoundTrip(t *testing.T) {
	buf := NewCellBuf(3, 2)
	cell := Cell{Grapheme: "Z", Style: NewStyle(0x0000FF, 0xFF0000, CharItalic)}
	buf.Set(1, 1, cell)
	if got := buf.Get(1, 1); got != cell {
		t.Errorf("Get(1,1) after Set: want %v got %v", cell, got)
	}
	if got := buf.Get(0, 1); got != blankCell() {
		t.Error("adjacent cell unexpectedly non-zero after Set(1,1)")
	}
}

func TestCellBufFillInsideBounds(t *testing.T) {
	buf := NewCellBuf(5, 5)
	mark := Cell{Grapheme: "#"}
	buf.Fill(Rect{X: 1, Y: 1, W: 3, H: 2}, mark)
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			inside := x >= 1 && x < 4 && y >= 1 && y < 3
			got := buf.Get(x, y)
			if inside && got != mark {
				t.Errorf("Fill: (%d,%d) inside rect should be mark", x, y)
			}
			if !inside && got != blankCell() {
				t.Errorf("Fill: (%d,%d) outside rect should be blank", x, y)
			}
		}
	}
}

func TestCellBufFillNegativeOffsetClipping(t *testing.T) {
	buf := NewCellBuf(3, 3)
	mark := Cell{Grapheme: "+"}
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
	mark := Cell{Grapheme: "+"}
	buf.Fill(Rect{X: 2, Y: 2, W: 5, H: 5}, mark)
	if buf.Get(2, 2) != mark {
		t.Error("(2,2) should be filled")
	}
	if buf.Get(0, 0) != blankCell() {
		t.Error("(0,0) should remain blank")
	}
}

func TestCellBufBlitSkipsZeroCells(t *testing.T) {
	dst := NewCellBuf(3, 1)
	active := Cell{Grapheme: "A", Style: NewStyle(0xFF0000, 0, 0)}
	for x := 0; x < 3; x++ {
		dst.Set(x, 0, active)
	}

	src := NewCellBuf(3, 1)
	src.Set(0, 0, Cell{})
	src.Set(2, 0, Cell{})
	src.Set(1, 0, Cell{Grapheme: "B"})

	dst.Blit(src, 0, 0)

	if dst.Get(0, 0) != active {
		t.Error("zero src cell should not overwrite dst at (0,0)")
	}
	if dst.Get(1, 0).Grapheme != "B" {
		t.Error("non-zero src cell should overwrite dst at (1,0)")
	}
	if dst.Get(2, 0) != active {
		t.Error("zero src cell should not overwrite dst at (2,0)")
	}
}

func TestCellBufBlitWithResetFallbackUsesDestinationChannelsIndependently(t *testing.T) {
	dst := NewCellBuf(3, 1)
	dst.Set(0, 0, Cell{Grapheme: "x", Style: NewStyle(0x123456, 0x654321, CharItalic)})
	dst.Set(1, 0, Cell{Grapheme: "y", Style: NewStyle(0xABCDEF, 0x102030, 0)})
	dst.Set(2, 0, Cell{Grapheme: "z", Style: NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)})

	src := NewCellBuf(3, 1)
	src.Set(0, 0, Cell{Grapheme: "A", Style: NewStyle(0, 0x00FF00, CharResetForegroundDefault|CharBold)})
	src.Set(1, 0, Cell{Grapheme: "B", Style: NewStyle(0xFF0000, 0, CharResetBackgroundDefault)})
	src.Set(2, 0, Cell{Grapheme: "C", Style: NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)})

	dst.BlitWithResetFallback(src, 0, 0)

	first := dst.Get(0, 0)
	if first.Grapheme != "A" || first.Style.Foreground() != 0x123456 || first.Style.Background() != 0x00FF00 {
		t.Fatalf("first cell = %+v, want inherited fg and explicit bg", first)
	}
	if !first.Style.HasAttribute(CharBold) || first.Style.HasAttribute(CharResetForegroundDefault) {
		t.Fatalf("first cell attributes = %#x, want bold and concrete inherited fg", first.Style.Attributes())
	}
	second := dst.Get(1, 0)
	if second.Grapheme != "B" || second.Style.Foreground() != 0xFF0000 || second.Style.Background() != 0x102030 {
		t.Fatalf("second cell = %+v, want explicit fg and inherited bg", second)
	}
	third := dst.Get(2, 0)
	if third.Grapheme != "C" || !third.Style.HasAttribute(CharResetForegroundDefault) ||
		!third.Style.HasAttribute(CharResetBackgroundDefault) {
		t.Fatalf("third cell = %+v, want root-default channels preserved", third)
	}
}

func TestCellBufBlitWithResetFallbackClearsOverlappedWideGlyph(t *testing.T) {
	dst := NewCellBuf(3, 1)
	dst.Set(0, 0, Cell{Grapheme: "界"})
	dst.Set(1, 0, Cell{Grapheme: "", Style: NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)})
	dst.Set(2, 0, Cell{Grapheme: "z"})
	src := NewCellBuf(1, 1)
	src.Set(0, 0, Cell{Grapheme: "x", Style: NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)})

	dst.BlitWithResetFallback(src, 1, 0)

	if got := dst.Get(0, 0).Grapheme; got != " " {
		t.Fatalf("left half of replaced wide glyph = %q, want blank", got)
	}
	if got := dst.Get(1, 0).Grapheme; got != "x" {
		t.Fatalf("replacement cell = %q, want x", got)
	}
}

func TestCellBufBlitOutOfBoundsOffset(t *testing.T) {
	dst := NewCellBuf(3, 3)
	src := NewCellBuf(2, 2)
	src.Set(0, 0, Cell{Grapheme: "X"})
	dst.Blit(src, 10, 10)
	dst.Blit(src, -5, -5)
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			if dst.Get(x, y) != blankCell() {
				t.Errorf("dst(%d,%d) should be blank after OOB Blit", x, y)
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
	if buf.Get(0, 0).Grapheme != "A" {
		t.Errorf("Get(0,0): want 'A', got %q", buf.Get(0, 0).Grapheme)
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

func TestFromANSIUsesUnicodeCoreGraphemeWidth(t *testing.T) {
	const width = 12
	family := "\U0001F468\u200D\U0001F469\u200D\U0001F467\u200D\U0001F466"

	tests := []struct {
		name string
		buf  CellBuf
	}{
		{name: "string", buf: FromANSIString(family+"x", width)},
		{name: "lines", buf: FromANSI([]string{family + "x", "next"}, width)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.buf.Get(0, 0).Grapheme; got != family {
				t.Fatalf("first cell grapheme = %q, want %q", got, family)
			}
			if got := test.buf.Get(1, 0).Grapheme; got != "" {
				t.Fatalf("wide-grapheme continuation = %q, want empty", got)
			}
			if got := test.buf.Get(2, 0).Grapheme; got != "x" {
				t.Fatalf("cell after family grapheme = %q, want x", got)
			}
		})
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
		r := buf.Get(x, 0).Grapheme
		if r != "" && r != " " {
			t.Errorf("row 0 col %d should be blank, got %q", x, r)
		}
	}
	if buf.Get(0, 1).Grapheme != "A" {
		t.Errorf("row 1 col 0: want 'A', got %q", buf.Get(0, 1).Grapheme)
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
	if buf.Get(0, 0).Grapheme != "H" || buf.Get(1, 0).Grapheme != "i" {
		t.Errorf("rune mismatch: got %q %q, want 'H' 'i'", buf.Get(0, 0).Grapheme, buf.Get(1, 0).Grapheme)
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
		if got := buf.Get(x, 1).Grapheme; got != string(want) {
			t.Errorf("(%d,1): want %q, got %q", x, want, got)
		}
	}
	for x := 0; x < 2; x++ {
		r := buf.Get(x, 1).Grapheme
		if r != "" && r != " " {
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
		r := buf.Get(x, 0).Grapheme
		if r != "" && r != " " {
			t.Errorf("col %d: should be blank after cursor skip, got %q", x, r)
		}
	}
	if buf.Get(4, 0).Grapheme != "B" {
		t.Errorf("col 4: want 'B', got %q", buf.Get(4, 0).Grapheme)
	}
	if buf.Get(5, 0).Grapheme != "A" {
		t.Errorf("col 5: want 'A', got %q", buf.Get(5, 0).Grapheme)
	}
}

func TestFromANSIEraseInLine(t *testing.T) {
	// Write "Hello", move cursor left 1, erase to EOL.
	buf := FromANSI([]string{"Hello\x1b[1D\x1b[K"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	for i, want := range []rune("Hell") {
		if got := buf.Get(i, 0).Grapheme; got != string(want) {
			t.Errorf("col %d: want %q, got %q", i, want, got)
		}
	}
	r := buf.Get(4, 0).Grapheme
	if r != "" && r != " " {
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

func TestIsSpaceCharacter(t *testing.T) {
	cases := []struct {
		grapheme string
		want     bool
		desc     string
	}{
		{" ", true, "ASCII space"},
		{"\u00A0", true, "non-breaking space"},
		{"\u2003", true, "em space"},
		{"\u200A", true, "hair space"},
		{"\u205F", true, "mathematical space"},
		{"a", false, "letter"},
		{"", false, "empty (wide-char continuation)"},
		{" \u0301", false, "space + combining accent (visible grapheme)"},
		{"\u00A0\u0308", false, "NBSP + combining diaeresis (visible grapheme)"},
	}
	for _, tc := range cases {
		cell := Cell{Grapheme: tc.grapheme}
		if got := isSpaceCharacter(cell); got != tc.want {
			t.Errorf("isSpaceCharacter(%q) [%s]: want %v got %v", tc.grapheme, tc.desc, tc.want, got)
		}
	}
}

func TestIsBoxDrawingRanges(t *testing.T) {
	cases := []struct {
		g    string
		want bool
		desc string
	}{
		{string(rune(0x24FF)), false, "just before box drawing"},
		{string(rune(0x2500)), true, "first box drawing"},
		{string(rune(0x2550)), true, "mid box drawing"},
		{string(rune(0x257F)), true, "last box drawing"},
		{string(rune(0x2580)), true, "first block element"},
		{string(rune(0x2590)), true, "mid block element"},
		{string(rune(0x259F)), true, "last block element"},
		{string(rune(0x25A0)), false, "just after block elements"},
		{"A", false, "ASCII letter"},
		{"-", false, "hyphen"},
		{" ", false, "space"},
		{"", false, "empty string"},
		{string(rune(0x2500)) + "\u0301", false, "box drawing + combining accent (multi-rune)"},
	}
	for _, tc := range cases {
		if got := isBoxDrawing(tc.g); got != tc.want {
			t.Errorf("isBoxDrawing(%q) [%s]: want %v got %v", tc.g, tc.desc, tc.want, got)
		}
	}
}

func TestIsInlineWhitespace(t *testing.T) {
	cases := []struct {
		g    string
		want bool
		desc string
	}{
		{"", true, "empty string (null cell)"},
		{" ", true, "standard space"},
		{"\t", true, "horizontal tab"},
		{"\u00A0", true, "non-breaking space"},
		{"\r", false, "carriage return"},
		{"\n", false, "newline"},
		{"\f", false, "form feed"},
		{"\v", false, "vertical tab"},
		{"a", false, "letter"},
		{".", false, "period"},
		{"-", false, "hyphen"},
		{" \u0301", false, "space + combining accent (multi-rune)"},
	}
	for _, tc := range cases {
		if got := isInlineWhitespace(tc.g); got != tc.want {
			t.Errorf("isInlineWhitespace(%q) [%s]: want %v got %v", tc.g, tc.desc, tc.want, got)
		}
	}
}

func TestIsTextFusion(t *testing.T) {
	cases := []struct {
		g    string
		want bool
		desc string
	}{
		{"a", true, "ASCII letter"},
		{"Z", true, "uppercase letter"},
		{"\u00E9", true, "accented letter (é)"},
		{"\u4E2D", true, "CJK ideograph (中)"},
		{"5", true, "digit"},
		{"\u00B2", true, "superscript (No category)"},
		{"\U0001F600", false, "emoji (no longer text fusion)"},
		{",", true, "comma"},
		{"-", true, "hyphen"},
		{"\u2013", true, "en-dash"},
		{"\u2014", true, "em-dash"},
		{"\"", true, "double quote"},
		{"'", true, "apostrophe"},
		{"\u201C", true, "left curly double quote"},
		{"\u201D", true, "right curly double quote"},
		{"\u2018", true, "left curly single quote"},
		{"\u2019", true, "right curly single quote"},
		{"(", true, "left paren"},
		{")", true, "right paren"},
		{"[", true, "left bracket"},
		{"]", true, "right bracket"},
		{":", true, "colon"},
		{";", true, "semicolon"},
		{"/", true, "forward slash"},
		{"\\", true, "backslash"},
		{" ", false, "space"},
		{".", false, "period"},
		{"!", false, "exclamation"},
		{"?", false, "question mark"},
		{"@", false, "at sign"},
		{string(rune(0x2500)), false, "box drawing char"},
		{"", false, "empty string"},
		{"e\u0301", true, "letter + combining accent (grapheme cluster)"},
		{"\U0001F469\u200D\U0001F680", false, "emoji ZWJ sequence"},
	}
	for _, tc := range cases {
		if got := isTextFusion(tc.g); got != tc.want {
			t.Errorf("isTextFusion(%q) [%s]: want %v got %v", tc.g, tc.desc, tc.want, got)
		}
	}
}

func TestIsBulletPrefixPatterns(t *testing.T) {
	cases := []struct {
		s0, s1, s2 string
		want       bool
		desc       string
	}{
		{"-", " ", "x", true, "hyphen bullet"},
		{"*", " ", "a", true, "asterisk bullet"},
		{"\u2022", " ", "z", true, "• bullet"},
		{"+", " ", "1", true, "plus bullet"},
		{"\u2013", " ", "b", true, "en-dash bullet"},
		{"\u2014", " ", "c", true, "em-dash bullet"},
		{"-", "\t", "x", true, "tab as bullet spacer"},
		{"-", "x", "y", false, "s1 not whitespace"},
		{"-", " ", " ", false, "s2 is whitespace"},
		{"a", " ", "x", false, "s0 not bullet char"},
		{".", " ", "x", false, "period not bullet"},
	}
	for _, tc := range cases {
		if got := isBulletPrefix(tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("isBulletPrefix(%q,%q,%q) [%s]: want %v got %v",
				tc.s0, tc.s1, tc.s2, tc.desc, tc.want, got)
		}
	}
}

func TestIsDividerCharSet(t *testing.T) {
	for _, g := range []string{"-", "*", "=", "~", "_", "#"} {
		if !isDividerChar(g) {
			t.Errorf("isDividerChar(%q): want true", g)
		}
	}
	for _, g := range []string{"a", ".", "|", "+", "!", "/", "\\", " ", "\u2013"} {
		if isDividerChar(g) {
			t.Errorf("isDividerChar(%q): want false", g)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Phase 4b – isSoftWrap exclusion and inclusion pipeline
// ─────────────────────────────────────────────────────────────────────────────

func TestIsSoftWrapExclusionBoxDrawing(t *testing.T) {
	box := string(rune(0x2500))
	l := "a"
	cases := []struct {
		e1, e0, s0, s1, s2 string
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
		e1, e0, s0, s1, s2 string
		want               bool
		desc               string
	}{
		{"x", "a", "-", " ", "i", false, "hyphen bullet"},
		{"x", "a", "*", " ", "i", false, "asterisk bullet"},
		{"x", "a", "\u2022", " ", "i", false, "• bullet"},
		// s1 not whitespace → bullet exclusion skipped → P1 fires on 'a'/'-'
		{"x", "a", "-", "x", "i", true, "s1 not whitespace"},
		// s2 is whitespace → bullet exclusion skipped → P1 fires on 'a'/'-'
		{"x", "a", "-", " ", " ", true, "s2 is whitespace"},
		// exclusion 2 fires before P2a; without it P2a would return true
		{"a", " ", "-", " ", "i", false, "bullet overrides P2a"},
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
		e1, e0, s0, s1, s2 string
		want               bool
		desc               string
	}{
		{"x", "a", "-", "-", "-", false, "---"},
		{"x", "a", "=", "=", "=", false, "==="},
		{"x", "a", "*", "*", "*", false, "***"},
		{"x", "a", "~", "~", "~", false, "~~~"},
		{"x", "a", "_", "_", "_", false, "___"},
		{"x", "a", "#", "#", "#", false, "###"},
		// Only 2 repeated chars: not a divider sequence
		{"x", "a", "-", "-", "x", true, "-- then text"},
		// Mixed: not a divider
		{"x", "a", "-", "=", "-", true, "mixed symbols"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] want %v got %v", tc.desc, tc.want, got)
		}
	}
}

func TestIsSoftWrapExclusionIndentation(t *testing.T) {
	cases := []struct {
		e1, e0, s0, s1, s2 string
		want               bool
		desc               string
	}{
		{"x", "a", " ", " ", "x", false, "double space indent"},
		{"x", "a", " ", " ", " ", false, "all spaces (empty row)"},
		{"x", "a", "\t", "\t", "x", false, "double tab indent"},
		// Single leading space: s1 is non-whitespace → exclusion 4 does not fire → P2b fires
		{"x", "a", " ", "b", "c", true, "single leading space → P2b"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got != tc.want {
			t.Errorf("[%s] want %v got %v", tc.desc, tc.want, got)
		}
	}
}

func TestIsSoftWrapExclusionFullStop(t *testing.T) {
	cases := []struct {
		e1, e0, s0, s1, s2 string
		want               bool
		desc               string
	}{
		{"a", ".", "N", "e", "x", false, "sentence end"},
		{"d", ".", "W", "o", "r", false, "word.Word"},
		// Flanked decimal exception: both neighbouring chars are digits
		{"3", ".", "1", "4", "1", true, "flanked decimal 3./1..."},
		{"0", ".", "5", "0", "0", true, "flanked decimal 0./5..."},
		// Not flanked: one side is a non-digit
		{"a", ".", "5", "0", "0", false, "e1 not digit"},
		{"3", ".", "a", "b", "c", false, "s0 not digit"},
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
		e1, e0, s0, s1, s2 string
		want               bool
		desc               string
	}{
		{"x", "a", "b", "c", "d", true, "letter–letter"},
		{"x", "a", "1", "2", "3", true, "letter–digit"},
		{"x", "1", "a", "b", "c", true, "digit–letter"},
		{"x", ",", "a", "b", "c", true, "para-punct–letter"},
		{"x", "a", ",", "b", "c", true, "letter–para-punct"},
		// e0 not C_text: P1 and P2b fail; P2a also fails (e0 not S_inline)
		{"x", "!", "a", "b", "c", false, "e0='!' not C_text"},
		// s0 not C_text and not S_inline: no inclusion rule fires
		{"x", "a", "!", "b", "c", false, "s0='!' not C_text"},
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
		e1, e0, s0, s1, s2 string
		want               bool
		desc               string
	}{
		{"a", " ", "b", "c", "d", true, "letter–space–letter"},
		{"1", " ", "a", "b", "c", true, "digit–space–letter"},
		{" ", " ", "b", "c", "d", false, "e1 space: not C_text"},
		{"a", " ", "!", "b", "c", false, "s0 '!': not C_text"},
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
		e1, e0, s0, s1, s2 string
		want               bool
		desc               string
	}{
		{"x", "a", " ", "b", "c", true, "letter–space–letter"},
		{"x", "a", "\u00A0", "b", "c", true, "letter–NBSP–letter"},
		// e0 not C_text: P2b fails
		{"x", "!", " ", "b", "c", false, "e0 '!' not C_text"},
		// double space triggers exclusion 4 before P2b is reached
		{"x", "a", " ", " ", "b", false, "double space triggers exclusion 4"},
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
		e1, e0, s0, s1, s2 string
		desc               string
	}{
		{"x", "!", "?", "a", "b", "! and ? not in C_text"},
		{"x", "@", "a", "b", "c", "@ not in C_text"},
		{" ", " ", "a", "b", "c", "both trailing spaces: no inclusion rule fires"},
	}
	for _, tc := range cases {
		if got := isSoftWrap(tc.e1, tc.e0, tc.s0, tc.s1, tc.s2); got {
			t.Errorf("[%s] isSoftWrap(%q,%q,...): want false got true", tc.desc, tc.e1, tc.e0)
		}
	}
}

func TestIsSoftWrapGraphemeClusters(t *testing.T) {
	// Accented grapheme at a row boundary should fuse as text (P1).
	if got := isSoftWrap("x", "e\u0301", "a", "b", "c"); !got {
		t.Error("accented e0 should fuse with letter s0 via P1")
	}
	// Accented grapheme starting next row should fuse.
	if got := isSoftWrap("x", "a", "e\u0301", "b", "c"); !got {
		t.Error("accented s0 should fuse with letter e0 via P1")
	}
	// Emoji at boundary should hard-break (not text fusion).
	if got := isSoftWrap("x", "\U0001F600", "a", "b", "c"); got {
		t.Error("emoji e0 should NOT fuse (hard break)")
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

// ─────────────────────────────────────────────────────────────────────────────
// Phase 5 – ToANSI state machine & emission
// ─────────────────────────────────────────────────────────────────────────────

func TestToANSITrailingColoredSpacesRetained(t *testing.T) {
	// Spaces whose style carries an explicit non-default background must survive trimming.
	// 0x800000 = ansi16RGB[1] → tier 16, BG SGR 41.
	buf := NewCellBuf(5, 1)
	redBG := NewStyle(0, 0x800000, CharResetForegroundDefault)
	for x := 0; x < 5; x++ {
		buf.Set(x, 0, Cell{Grapheme: " ", Style: redBG})
	}
	out := ToANSI(buf)
	if !strings.Contains(out, "\x1b[41m") {
		t.Errorf("expected tier-16 bg SGR 41 in output, got %q", out)
	}
	// The whole row is spaces, so all columns are trailing and get trimmed.
	// The red is retained not as literal spaces but by carrying the active
	// background into the line erase (BCE): \x1b[41m stays in effect when
	// \x1b[K paints the remainder of the row red. No reset precedes the erase.
	want := "\x1b[41m\x1b[K"
	if out != want {
		t.Errorf("bg-colored full-row trailing spaces: want %q, got %q", want, out)
	}
}

func TestToANSISoftWrapSuppressesNewline(t *testing.T) {
	// CharSoftWrap on the last cell of row 0 must suppress the \r\n separator.
	const w = 5
	buf := NewCellBuf(w, 2)
	base := NewStyle(0, 0, CharResetForegroundDefault|CharResetBackgroundDefault)
	for i, ch := range "Hello" {
		s := base
		if i == w-1 {
			s = s.AddAttribute(CharSoftWrap)
		}
		buf.Set(i, 0, Cell{Grapheme: string(ch), Style: s})
	}
	for i, ch := range "World" {
		buf.Set(i, 1, Cell{Grapheme: string(ch), Style: base})
	}
	out := ToANSI(buf)
	if strings.Contains(out, "Hello\r\n") {
		t.Error("soft-wrapped row must not emit \\r\\n after its content")
	}
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "World") {
		t.Error("both row contents must appear in output")
	}
}

func TestToANSITier16ColorEmission(t *testing.T) {
	// ansi16RGB[0] = 0x000000 → FG SGR 30 (no 38;5 or 38;2).
	buf := NewCellBuf(1, 1)
	buf.Set(0, 0, Cell{Grapheme: "X", Style: NewStyle(0x000000, 0, CharResetBackgroundDefault)})
	out := ToANSI(buf)
	if !strings.Contains(out, "\x1b[30m") {
		t.Errorf("pure black fg: want \\x1b[30m, got %q", out)
	}
	if strings.Contains(out, "38;5") || strings.Contains(out, "38;2") {
		t.Errorf("tier-16 color must not emit 256-color or truecolor prefix: %q", out)
	}

	// ansi16RGB[15] = 0xFFFFFF → FG SGR 97 (index 15 ≥ 8 → 90 + (15-8) = 97).
	buf2 := NewCellBuf(1, 1)
	buf2.Set(0, 0, Cell{Grapheme: "Y", Style: NewStyle(0xFFFFFF, 0, CharResetBackgroundDefault)})
	out2 := ToANSI(buf2)
	if !strings.Contains(out2, "\x1b[97m") {
		t.Errorf("pure white fg: want \\x1b[97m, got %q", out2)
	}
}

func TestToANSIFGResetPreservesBGPlane(t *testing.T) {
	// Resetting FG (\x1b[39m) must not emit a new BG code.
	// 0xFF0000 = ansi16RGB[9]  → FG SGR 91; BG SGR 101
	// 0x0000FF = ansi16RGB[12] → FG SGR 94; BG SGR 104
	buf := NewCellBuf(3, 1)
	both := NewStyle(0xFF0000, 0x0000FF, 0)
	bgOnly := NewStyle(0, 0x0000FF, CharResetForegroundDefault)
	buf.Set(0, 0, Cell{Grapheme: "A", Style: both})
	buf.Set(1, 0, Cell{Grapheme: "B", Style: bgOnly})
	buf.Set(2, 0, Cell{Grapheme: "C", Style: bgOnly})
	out := ToANSI(buf)
	if strings.Count(out, "\x1b[39m") != 1 {
		t.Errorf("expected exactly one FG-reset code: %q", out)
	}
	// BG blue is set once for A and must not be re-emitted for B or C.
	if strings.Count(out, "\x1b[104m") != 1 {
		t.Errorf("expected exactly one BG-blue code \\x1b[104m: %q", out)
	}
}

func TestToANSIFromANSIRoundTrip(t *testing.T) {
	// A styled buffer round-tripped through ToANSI → FromANSI must preserve runes and key attrs.
	orig := FromANSI([]string{"\x1b[1;31mHi\x1b[0m"}, 10)
	if orig.Height == 0 {
		t.Fatal("expected non-empty original buffer")
	}
	s := strings.TrimSuffix(ToANSI(orig), "\r\n")
	rt := FromANSI([]string{s}, orig.Width)
	if rt.Height != orig.Height {
		t.Fatalf("height mismatch: orig %d rt %d", orig.Height, rt.Height)
	}
	for y := 0; y < orig.Height; y++ {
		for x := 0; x < orig.Width; x++ {
			a, b := orig.Get(x, y), rt.Get(x, y)
			if a.Grapheme != b.Grapheme {
				t.Errorf("(%d,%d) rune: orig %q rt %q", x, y, a.Grapheme, b.Grapheme)
			}
		}
	}
	// Key styled cells: 'H' and 'i' must retain bold and explicit fg.
	for _, x := range []int{0, 1} {
		a, b := orig.Get(x, 0), rt.Get(x, 0)
		if a.Style.HasAttribute(CharBold) != b.Style.HasAttribute(CharBold) {
			t.Errorf("(%d,0) CharBold mismatch: orig %v rt %v", x, a.Style.HasAttribute(CharBold), b.Style.HasAttribute(CharBold))
		}
		if a.Style.HasAttribute(CharResetForegroundDefault) != b.Style.HasAttribute(CharResetForegroundDefault) {
			t.Errorf("(%d,0) fg-default mismatch: orig %v rt %v", x, a.Style.HasAttribute(CharResetForegroundDefault), b.Style.HasAttribute(CharResetForegroundDefault))
		}
	}
}
