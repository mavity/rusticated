package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestPlumeBufferAppendAndEvict(t *testing.T) {
	buf := NewPlumeBuffer(3)
	buf.Append("one", "two", "three")

	if len(buf.Entries) != 3 {
		t.Fatalf("expected 3 entries after append, got %d", len(buf.Entries))
	}

	buf.Append("four")
	if len(buf.Entries) != 3 {
		t.Fatalf("expected eviction to keep max entries at 3, got %d", len(buf.Entries))
	}
	if buf.Entries[0].Raw != "two" {
		t.Fatalf("expected oldest retained entry to be %q, got %q", "two", buf.Entries[0].Raw)
	}
}

func TestLayoutPlumeDividesHistory(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	slices := LayoutPlume(lines, 20, 4)
	count := len(slices.Exhaust) + len(slices.Occluded) + len(slices.Footer)
	if count != len(lines) {
		t.Fatalf("expected all lines to appear across slices, got %d total from %d lines", count, len(lines))
	}
}

func TestPlumeWidgetRenderRespectsWidth(t *testing.T) {
	w := NewPlumeWidget()
	w.SetLines([]string{"0123456789"})
	w.Layout(Rect{X: 0, Y: 0, W: 5, H: 3})

	buf := w.Render()
	if buf.Width != 5 || buf.Height != 3 {
		t.Fatalf("unexpected render size: %#v", buf)
	}

	out := Serialize(buf)
	if strings.Contains(out, "6789") {
		t.Fatalf("render should clip long text to width, got %q", ansi.Strip(out))
	}
}

func TestPlumeWidgetScrollUpShowsOlderLines(t *testing.T) {
	w := NewPlumeWidget()
	w.SetLines([]string{"line1", "line2", "line3", "line4", "line5"})
	w.Layout(Rect{X: 0, Y: 0, W: 20, H: 2}) // shows 2 lines at a time

	// Default (scrollTop=0): should show last 2 lines
	out := ansi.Strip(Serialize(w.Render()))
	if !strings.Contains(out, "line4") || !strings.Contains(out, "line5") {
		t.Fatalf("default scroll should show latest lines; got %q", out)
	}

	// Scroll up by 2: should show lines 2-3
	w.HandleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	out = ansi.Strip(Serialize(w.Render()))
	if strings.Contains(out, "line5") {
		t.Fatalf("after pgup, line5 should not be visible; got %q", out)
	}
}

func TestPlumeWidgetAppendResetsScroll(t *testing.T) {
	w := NewPlumeWidget()
	w.SetLines([]string{"a", "b", "c", "d", "e"})
	w.Layout(Rect{X: 0, Y: 0, W: 20, H: 2})
	w.HandleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if w.scrollTop == 0 {
		t.Skip("scrollTop didn't change; pgup step may equal 0 for height 2")
	}

	w.appendLine("new line")
	if w.scrollTop != 0 {
		t.Fatalf("appendLine should reset scrollTop to 0; got %d", w.scrollTop)
	}
}

func TestPlumeWidgetEndKeyResetsScroll(t *testing.T) {
	w := NewPlumeWidget()
	w.SetLines([]string{"a", "b", "c", "d", "e"})
	w.Layout(Rect{X: 0, Y: 0, W: 20, H: 2})
	w.HandleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	w.HandleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if w.scrollTop != 0 {
		t.Fatalf("End key should reset scrollTop to 0; got %d", w.scrollTop)
	}
}

func TestPlumeWidgetSnapshot(t *testing.T) {
	w := NewPlumeWidget()
	w.SetLines([]string{"cmd> ls", "alpha.go", "beta.go", "gamma.go", "delta.go"})
	assertWidgetSnapshot(t, "plume_widget", w, Rect{X: 0, Y: 0, W: 20, H: 3})
}
