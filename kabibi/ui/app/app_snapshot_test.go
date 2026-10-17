package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func assertAppSnapshot(t *testing.T, snapFile string, widget *AppWidget, scrollback *string, rect ui.Rect) {
	t.Helper()

	buf, cursor := widget.Render(rect, ui.RenderContext{})
	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		cell.Grapheme = "\u0332" // Combining low line (non-destructive)
		buf.Set(cursor.X, cursor.Y, cell)
	}

	sb := ""
	if scrollback != nil && len(*scrollback) > 0 {
		sb = *scrollback + "-------scrollback-------\r\n"
	}
	got := sb + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", snapFile)
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		if err := os.WriteFile(snapPath, []byte(got), 0o644); err != nil {
			t.Fatalf("failed to write snapshot %s: %v", snapPath, err)
		}
	}

	wantBytes, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}
	want := string(wantBytes)

	if got != want {
		fmt.Fprintf(os.Stderr,
			"\n\x1b[1;33m=== SNAPSHOT MISMATCH: %s ===\x1b[0m\n"+
				"\x1b[1;31mWANT -------------------------------------------------------------------------\x1b[0m\n"+
				"%s\x1b[0m\n"+
				"\x1b[1;32mGOT --------------------------------------------------------------------------\x1b[0m\n"+
				"%s\x1b[0m\n"+
				"\x1b[1;33m------------------------------------------------------------------------------\x1b[0m\n\n",
			snapPath, want, got)
		t.Errorf("payload mismatch for %s (see rendered TTY diff above)", snapPath)
	}
}

func newSnapshotWidget(scrollback *string) *AppWidget {
	w := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) {
			if scrollback != nil {
				*scrollback += s
			}
		},
	})
	w.Shell().SetPrompt("፧")
	return w
}

// TestAppWidget_Snapshot_Initial_W80H24 verifies initial state.
// Viewport: 80×24
// State: Fresh shell, retro banner, ፧ prompt ready for input, and 1 blank footer line below
func TestAppWidget_Snapshot_Initial_W80H24(t *testing.T) {
	var scrollback string
	widget := newSnapshotWidget(&scrollback)
	assertAppSnapshot(t, "initial_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}

func TestAppWidget_BluePanelsPreserveTerminalBackgroundOutsidePanels(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{PanelsVisible: true})
	defer widget.Close()

	buf, _ := widget.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 24}, ui.RenderContext{})

	panelCell := buf.Get(10, 8)
	if got := panelCell.Style.Background(); got != terminal.Color(0x0000A8) {
		t.Fatalf("panel background = %#06x, want blue %#06x", got, 0x0000A8)
	}

	gapCell := buf.Get(79, 23)
	if !gapCell.Style.HasAttribute(terminal.CharResetBackgroundDefault) {
		t.Fatalf("bottom gap background style = %#x, want terminal default background", gapCell.Style.Attributes())
	}
}

// typeString simulates keyboard typing by dispatching canonical input.KeyPressEvents.
func typeString(w ui.Widget, s string) {
	for _, r := range s {
		switch r {
		case ' ':
			w.HandleEvent(input.KeyPressEvent{Code: ' ', Text: " "})
		default:
			w.HandleEvent(input.KeyPressEvent{Code: r, Text: string(r)})
		}
	}
}

// TestAppWidget_Snapshot_TypingCommand_W80H24 verifies typing behavior via input.KeyPressEvents.
func TestAppWidget_Snapshot_TypingCommand_W80H24(t *testing.T) {
	var scrollback string
	widget := newSnapshotWidget(&scrollback)

	invalidated := 0
	widget.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 24}, ui.RenderContext{
		Focused:    true,
		Invalidate: func() { invalidated++ },
	})

	typeString(widget, "echo hello")

	if invalidated != len("echo hello") {
		t.Fatalf("expected %d invalidations, got %d", len("echo hello"), invalidated)
	}

	assertAppSnapshot(t, "typing_echo_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}

// TestAppWidget_Snapshot_CommandSubmission_W80H24 verifies command submission and scrollback.
func TestAppWidget_Snapshot_CommandSubmission_W80H24(t *testing.T) {
	var scrollback string
	widget := newSnapshotWidget(&scrollback)
	widget.Shell().SetInput("hello")
	widget.Shell().SubmitCurrentInput()
	assertAppSnapshot(t, "submit_hello_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}

// TestAppWidget_Snapshot_EjectionOverflow_W80H10 verifies watermark ejection into scrollback.
func TestAppWidget_Snapshot_EjectionOverflow_W80H10(t *testing.T) {
	var scrollback string
	widget := newSnapshotWidget(&scrollback)
	sh := widget.Shell()
	for i := 1; i <= 12; i++ {
		sh.SetInput(fmt.Sprintf("cmd %d", i))
		sh.SubmitCurrentInput()
	}
	assertAppSnapshot(t, "ejection_overflow_w80h10.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 10})
}

// TestAppWidget_Snapshot_LineWrapping_W20H10 verifies wrapping at narrow width.
func TestAppWidget_Snapshot_LineWrapping_W20H10(t *testing.T) {
	var scrollback string
	widget := newSnapshotWidget(&scrollback)
	widget.Shell().SetInput("this is a long command")
	assertAppSnapshot(t, "wrapping_w20h10.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 20, H: 10})
}

// TestAppWidget_Snapshot_CursorMovement_W80H24 verifies cursor positioning.
func TestAppWidget_Snapshot_CursorMovement_W80H24(t *testing.T) {
	var scrollback string
	widget := newSnapshotWidget(&scrollback)
	sh := widget.Shell()
	sh.SetInput("hello")
	sh.SetCursorPos(3)
	assertAppSnapshot(t, "cursor_left_hello_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}
