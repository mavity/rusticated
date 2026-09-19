package terminal

import (
	"strings"
	"testing"
)

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
	// Colors must survive attribute mutation.
	if s.Foreground() != 0xFF0000 || s.Background() != 0x00FF00 {
		t.Error("attribute mutation changed color fields")
	}
}

func TestStyleDefaultResetFlagsCoexistWithColors(t *testing.T) {
	// Reset flags occupy the attributes field; they must not corrupt the color fields.
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
		{-1, -1, 0, 0},
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
	// OOB Get must return zero Cell; OOB Set must be a silent no-op.
	buf := NewCellBuf(3, 2)
	cell := Cell{R: 'X', Style: NewStyle(0xFF0000, 0, CharBold)}

	oob := [][2]int{{-1, 0}, {0, -1}, {3, 0}, {0, 2}, {-1, -1}, {99, 99}}
	for _, c := range oob {
		x, y := c[0], c[1]
		if got := buf.Get(x, y); got != (Cell{}) {
			t.Errorf("Get(%d,%d): want zero Cell, got non-zero", x, y)
		}
		buf.Set(x, y, cell) // must not panic; inner state must be unchanged
	}
	// All in-bounds cells must remain zero after the OOB Sets above.
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
	// Rect starting before origin: only the in-bounds sub-region is filled.
	buf := NewCellBuf(3, 3)
	mark := Cell{R: '+'}
	buf.Fill(Rect{X: -1, Y: -1, W: 3, H: 3}, mark) // effective area: x∈[0,1], y∈[0,1]
	if buf.Get(0, 0) != mark {
		t.Error("(0,0) should be filled by clipped rect")
	}
	if buf.Get(2, 2) == mark {
		t.Error("(2,2) should NOT be filled (outside clipped area)")
	}
}

func TestCellBufFillOverflowClipping(t *testing.T) {
	// Rect extending past the bottom-right edge: only (2,2) lands inside a 3x3 buf.
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
	// Zero cells in src must not overwrite active cells in dst.
	dst := NewCellBuf(3, 1)
	active := Cell{R: 'A', Style: NewStyle(0xFF0000, 0, 0)}
	for x := 0; x < 3; x++ {
		dst.Set(x, 0, active)
	}

	src := NewCellBuf(3, 1)
	src.Set(1, 0, Cell{R: 'B'}) // only column 1 is non-zero

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
	// Blit with an offset that places src entirely outside dst must not panic or mutate dst.
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

// assertGridRunes checks that the flat string expected maps onto the buf grid
// using the layout i = y*W + x (left-to-right, top-to-bottom).
func assertGridRunes(t *testing.T, buf CellBuf, expected string) {
	t.Helper()
	for i, want := range []rune(expected) {
		x, y := i%buf.Width, i/buf.Width
		if got := buf.Get(x, y).R; got != want {
			t.Errorf("grid[%d] (%d,%d): want %q got %q", i, x, y, want, got)
		}
	}
}

func TestFromANSIEmptyInputs(t *testing.T) {
	if buf := FromANSI(nil, 10); buf.Width != 10 || buf.Height != 0 {
		t.Errorf("nil lines: want 10×0, got %d×%d", buf.Width, buf.Height)
	}
	if buf := FromANSI([]string{}, 10); buf.Width != 10 || buf.Height != 0 {
		t.Errorf("empty slice: want 10×0, got %d×%d", buf.Width, buf.Height)
	}
	// A single empty string produces no touched rows → height 0.
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
	// "Hello World" (11 chars) at width 5 → 3 rows: "Hello", " Worl", "d".
	buf := FromANSI([]string{"Hello World"}, 5)
	if buf.Height != 3 {
		t.Fatalf("want height 3, got %d", buf.Height)
	}
	assertGridRunes(t, buf, "Hello World")
}

func TestFromANSIExactWidthBoundary(t *testing.T) {
	// A string whose length exactly equals width must produce exactly 1 row.
	const w = 10
	buf := FromANSI([]string{strings.Repeat("x", w)}, w)
	if buf.Height != 1 {
		t.Errorf("exact width: want height 1, got %d", buf.Height)
	}
}

func TestFromANSIOneCharOverWidth(t *testing.T) {
	// A string one char longer than width must produce exactly 2 rows.
	const w = 10
	buf := FromANSI([]string{strings.Repeat("x", w+1)}, w)
	if buf.Height != 2 {
		t.Errorf("width+1 chars: want height 2, got %d", buf.Height)
	}
}

func TestFromANSILeadingNewlinePreservesEmptyRow(t *testing.T) {
	// The top empty row must be kept; trimTopEmptyLines must not apply.
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
	// 30 one-character lines exceed the 24-row viewport, pushing 6 into scrollback.
	// Total height must equal the full line count.
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
	// \x1b[48;2;10;20;30m = direct 24-bit background (10, 20, 30) = 0x0A141E.
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
	// Cells written under \x1b[31m carry explicit fg; cells after \x1b[0m carry reset-default.
	buf := FromANSI([]string{"\x1b[31mRed\x1b[0m Plain"}, 20)
	if buf.Height == 0 {
		t.Fatal("expected non-empty buffer")
	}
	for x := 0; x < 3; x++ { // 'R','e','d'
		if buf.Get(x, 0).Style.HasAttribute(CharResetForegroundDefault) {
			t.Errorf("col %d: should have explicit fg inside colored span", x)
		}
	}
	for x := 3; x <= 8; x++ { // ' ','P','l','a','i','n'
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
	// Columns before the text on row 1 must be blank.
	for x := 0; x < 2; x++ {
		r := buf.Get(x, 1).R
		if r != 0 && r != ' ' {
			t.Errorf("(%d,1) before text: want blank, got %q", x, r)
		}
	}
}

func TestFromANSICursorRelativeHorizontalMove(t *testing.T) {
	// Move right 5, write 'A', move left 2, write 'B'.
	// Expected: cols 0-3 blank, col 4 = 'B', col 5 = 'A'.
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
	// Cells 0-3 keep H,e,l,l; cell 4 (formerly 'o') and beyond are cleared.
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
	// A string longer than width causes the first row to be auto-wrapped.
	// The rightmost cell of that row must carry CharSoftWrap.
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
	// Explicitly newlined lines that don't fill the right margin must NOT carry CharSoftWrap.
	const w = 10
	buf := FromANSI([]string{"12345", "67890"}, w) // each line is 5 chars at width 10
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
