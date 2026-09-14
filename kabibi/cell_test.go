package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestCellBufFillAndBlit(t *testing.T) {
	buf := NewCellBuf(4, 2)
	fill := Cell{R: 'X', FG: lipgloss.Color("#ff0000"), BG: lipgloss.Color("#000000"), Bold: true}
	buf.Fill(Rect{X: 1, Y: 0, W: 2, H: 1}, fill)

	if got := buf.Get(1, 0); got.R != 'X' || got.FG != fill.FG || got.BG != fill.BG || !got.Bold {
		t.Fatalf("fill did not write expected cell: %#v", got)
	}
	if got := buf.Get(0, 0); got.R != 0 {
		t.Fatalf("expected untouched cell to be zero value, got %#v", got)
	}

	src := NewCellBuf(2, 1)
	src.Fill(Rect{X: 0, Y: 0, W: 2, H: 1}, Cell{R: 'A'})
	buf.Blit(src, 0, 1)

	if got := buf.Get(0, 1); got.R != 'A' {
		t.Fatalf("blit did not copy source pixel: %#v", got)
	}
}

func TestCellBufSerializeIncludesStyleAndRowBreaks(t *testing.T) {
	buf := NewCellBuf(2, 2)
	buf.Fill(Rect{X: 0, Y: 0, W: 2, H: 1}, Cell{R: 'A', FG: lipgloss.Color("#ff0000"), BG: lipgloss.Color("#000000")})
	buf.Fill(Rect{X: 0, Y: 1, W: 2, H: 1}, Cell{R: 'B', Reverse: true})

	out := Serialize(buf)
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("serialize output should contain ANSI styling, got %q", out)
	}
	if got := strings.Count(out, "\n"); got != 1 {
		t.Fatalf("expected one newline between two rows, got %d in %q", got, out)
	}
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Fatalf("serialize output should contain rendered characters: %q", out)
	}
}
