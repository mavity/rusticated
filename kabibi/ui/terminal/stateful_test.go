package terminal

import (
	"reflect"
	"testing"
)

func TestTerminalStreamingChunksMatchSingleWrite(t *testing.T) {
	input := "A\x1b[31m界\x1b[0m\nB"
	want := NewTerminal(12, 4)
	if _, err := want.WriteString(input); err != nil {
		t.Fatal(err)
	}
	wantBuf, wantCursor := want.Snapshot()

	got := NewTerminal(12, 4)
	for i := range []byte(input) {
		if _, err := got.Write([]byte(input[i : i+1])); err != nil {
			t.Fatal(err)
		}
	}
	gotBuf, gotCursor := got.Snapshot()
	if !reflect.DeepEqual(gotBuf, wantBuf) {
		t.Fatalf("chunked writes produced different cells")
	}
	if gotCursor != wantCursor {
		t.Fatalf("chunked cursor = %+v, want %+v", gotCursor, wantCursor)
	}
}

func TestTerminalLineFeedNewLineMode(t *testing.T) {
	term := NewTerminal(12, 4)
	if _, err := term.WriteString("abc\nx"); err != nil {
		t.Fatal(err)
	}
	_, cursor := term.Snapshot()
	if cursor.X != 1 || cursor.Y != 1 {
		t.Fatalf("LNM cursor after bare LF = (%d,%d), want (1,1)", cursor.X, cursor.Y)
	}

	term = NewTerminal(12, 4)
	if _, err := term.WriteString("a\r"); err != nil {
		t.Fatal(err)
	}
	if _, err := term.WriteString("\nb"); err != nil {
		t.Fatal(err)
	}
	_, cursor = term.Snapshot()
	if cursor.X != 1 || cursor.Y != 1 {
		t.Fatalf("split CRLF cursor = (%d,%d), want (1,1)", cursor.X, cursor.Y)
	}
}

func TestTerminalHonorsInBandLineFeedModeReset(t *testing.T) {
	term := NewTerminal(12, 4)
	if _, err := term.WriteString("\x1b[20labc\nx"); err != nil {
		t.Fatal(err)
	}
	_, cursor := term.Snapshot()
	if cursor.X != 4 || cursor.Y != 1 {
		t.Fatalf("cursor after LNM reset = (%d,%d), want (4,1)", cursor.X, cursor.Y)
	}
}

func TestTerminalResizePreservesStateWithoutReplay(t *testing.T) {
	term := NewTerminal(8, 3)
	if _, err := term.WriteString("first\r\nsecond"); err != nil {
		t.Fatal(err)
	}
	before, _ := term.Snapshot()
	term.Resize(10, 4)
	width, height := term.Size()
	if width != 10 || height != 4 {
		t.Fatalf("Size() = (%d,%d), want (10,4)", width, height)
	}
	after, _ := term.Snapshot()
	if after.Height < before.Height {
		t.Fatalf("snapshot height after resize = %d, want at least prior height %d",
			after.Height, before.Height)
	}
	for y := 0; y < before.Height && y < after.Height; y++ {
		for x := 0; x < before.Width && x < after.Width; x++ {
			if before.Get(x, y) != after.Get(x, y) {
				t.Fatalf("cell (%d,%d) changed during resize: before=%+v after=%+v",
					x, y, before.Get(x, y), after.Get(x, y))
			}
		}
	}
	if _, err := term.WriteString("\r\nthird"); err != nil {
		t.Fatal(err)
	}
	afterWrite, _ := term.Snapshot()
	found := false
	for y := 0; y < afterWrite.Height; y++ {
		for x := 0; x < afterWrite.Width; x++ {
			if afterWrite.Get(x, y).Grapheme == "t" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("post-resize write was not present in the snapshot")
	}
}

func TestTerminalRenderANSIResetsBetweenExactSizeFrames(t *testing.T) {
	term := NewTerminal(4, 2)
	first, _, err := term.RenderANSI("\x1b[31mred", 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Width != 4 || first.Height != 2 {
		t.Fatalf("first frame size = %dx%d, want 4x2", first.Width, first.Height)
	}
	if first.Get(0, 0).Style.HasAttribute(CharResetForegroundDefault) {
		t.Fatal("explicit red foreground unexpectedly reset")
	}

	second, cursor, err := term.RenderANSI("B", 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if second.Width != 3 || second.Height != 3 {
		t.Fatalf("second frame size = %dx%d, want 3x3", second.Width, second.Height)
	}
	if second.Get(0, 0).Grapheme != "B" ||
		!second.Get(0, 0).Style.HasAttribute(CharResetForegroundDefault) ||
		!second.Get(0, 0).Style.HasAttribute(CharResetBackgroundDefault) {
		t.Fatalf("second frame first cell = %+v, want fresh default style", second.Get(0, 0))
	}
	if second.Get(0, 1).Grapheme != " " {
		t.Fatalf("untouched viewport cell = %q, want space", second.Get(0, 1).Grapheme)
	}
	if cursor.Visible {
		t.Fatal("fresh frame cursor should remain hidden unless the widget shows it")
	}
}

func TestTerminalAlternateScreenDoesNotReplaceMainScreen(t *testing.T) {
	term := NewTerminal(12, 4)
	if _, err := term.WriteString("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := term.WriteString("\x1b[?1049halt"); err != nil {
		t.Fatal(err)
	}
	if !term.IsAltScreen() {
		t.Fatal("alternate screen was not reported active")
	}
	alt, _ := term.Snapshot()
	if alt.Get(0, 0).Grapheme != "a" {
		t.Fatalf("alternate screen begins with %q, want %q", alt.Get(0, 0).Grapheme, "a")
	}
	if _, err := term.WriteString("\x1b[?1049l"); err != nil {
		t.Fatal(err)
	}
	if term.IsAltScreen() {
		t.Fatal("alternate screen remained active after exit")
	}
	main, _ := term.Snapshot()
	if main.Get(0, 0).Grapheme != "m" {
		t.Fatalf("main screen begins with %q, want %q", main.Get(0, 0).Grapheme, "m")
	}
}
