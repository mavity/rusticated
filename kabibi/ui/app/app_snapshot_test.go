package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// TestAppWidget_Snapshot_Initial_W80H24 verifies initial state.
// Viewport: 80×24
// State: Fresh shell, welcome message, prompt ready for input
func TestAppWidget_Snapshot_Initial_W80H24(t *testing.T) {
	var scrollback string
	widget := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) { scrollback += s },
	})

	rect := ui.Rect{X: 0, Y: 0, W: 80, H: 24}
	buf, cursor := widget.Render(rect, ui.RenderContext{})

	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		if cell.R == 0 {
			cell.R = ' '
		}
		cell.R = '\u0332' // Combining low line (non-destructive)
		buf.Set(cursor.X, cursor.Y, cell)
	}

	payload := scrollback + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", "initial_w80h24.snap")
	wantPayload, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}

	if payload != string(wantPayload) {
		t.Errorf("payload mismatch for %s\n--- WANT ---\n%q\n--- GOT ---\n%q",
			snapPath, string(wantPayload), payload)
	}
}

// TestAppWidget_Snapshot_TypingCommand_W80H24 verifies typing behavior.
func TestAppWidget_Snapshot_TypingCommand_W80H24(t *testing.T) {
	var scrollback string
	widget := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) { scrollback += s },
	})

	widget.Shell().SetInput("echo hello")

	rect := ui.Rect{X: 0, Y: 0, W: 80, H: 24}
	buf, cursor := widget.Render(rect, ui.RenderContext{})

	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		if cell.R == 0 {
			cell.R = ' '
		}
		cell.R = '\u0332' // Combining low line (non-destructive)
		buf.Set(cursor.X, cursor.Y, cell)
	}

	payload := scrollback + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", "typing_echo_w80h24.snap")
	wantPayload, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}

	if payload != string(wantPayload) {
		t.Errorf("payload mismatch for %s\n--- WANT ---\n%q\n--- GOT ---\n%q",
			snapPath, string(wantPayload), payload)
	}
}

// TestAppWidget_Snapshot_CommandSubmission_W80H24 verifies command submission and scrollback.
func TestAppWidget_Snapshot_CommandSubmission_W80H24(t *testing.T) {
	var scrollback string
	widget := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) { scrollback += s },
	})

	widget.Shell().SetInput("hello")
	widget.Shell().SubmitCurrentInput()

	rect := ui.Rect{X: 0, Y: 0, W: 80, H: 24}
	buf, cursor := widget.Render(rect, ui.RenderContext{})

	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		if cell.R == 0 {
			cell.R = ' '
		}
		cell.R = '\u0332' // Combining low line (non-destructive)
		buf.Set(cursor.X, cursor.Y, cell)
	}

	payload := scrollback + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", "submit_hello_w80h24.snap")
	wantPayload, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}

	if payload != string(wantPayload) {
		t.Errorf("payload mismatch for %s\n--- WANT ---\n%q\n--- GOT ---\n%q",
			snapPath, string(wantPayload), payload)
	}
}

// TestAppWidget_Snapshot_EjectionOverflow_W80H24 verifies watermark ejection.
func TestAppWidget_Snapshot_EjectionOverflow_W80H24(t *testing.T) {
	var scrollback string
	widget := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) { scrollback += s },
	})

	sh := widget.Shell()
	for i := 0; i < 5; i++ {
		sh.SetInput("cmd")
		sh.SubmitCurrentInput()
	}

	rect := ui.Rect{X: 0, Y: 0, W: 80, H: 24}
	buf, cursor := widget.Render(rect, ui.RenderContext{})

	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		if cell.R == 0 {
			cell.R = ' '
		}
		cell.R = '\u0332' // Combining low line (non-destructive)
		buf.Set(cursor.X, cursor.Y, cell)
	}

	payload := scrollback + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", "ejection_overflow_w80h24.snap")
	wantPayload, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}

	if payload != string(wantPayload) {
		t.Errorf("payload mismatch for %s\n--- WANT ---\n%q\n--- GOT ---\n%q",
			snapPath, string(wantPayload), payload)
	}
}

// TestAppWidget_Snapshot_LineWrapping_W20H10 verifies wrapping at narrow width.
func TestAppWidget_Snapshot_LineWrapping_W20H10(t *testing.T) {
	var scrollback string
	widget := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) { scrollback += s },
	})

	widget.Shell().SetInput("this is a long command")

	rect := ui.Rect{X: 0, Y: 0, W: 20, H: 10}
	buf, cursor := widget.Render(rect, ui.RenderContext{})

	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		if cell.R == 0 {
			cell.R = ' '
		}
		cell.R = '\u0332' // Combining low line (non-destructive)
		buf.Set(cursor.X, cursor.Y, cell)
	}

	payload := scrollback + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", "wrapping_w20h10.snap")
	wantPayload, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}

	if payload != string(wantPayload) {
		t.Errorf("payload mismatch for %s\n--- WANT ---\n%q\n--- GOT ---\n%q",
			snapPath, string(wantPayload), payload)
	}
}

// TestAppWidget_Snapshot_CursorMovement_W80H24 verifies cursor positioning.
func TestAppWidget_Snapshot_CursorMovement_W80H24(t *testing.T) {
	var scrollback string
	widget := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) { scrollback += s },
	})

	sh := widget.Shell()
	sh.SetInput("hello")
	sh.SetCursorPos(3)

	rect := ui.Rect{X: 0, Y: 0, W: 80, H: 24}
	buf, cursor := widget.Render(rect, ui.RenderContext{})

	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		if cell.R == 0 {
			cell.R = ' '
		}
		cell.R = '\u0332' // Combining low line (non-destructive)
		buf.Set(cursor.X, cursor.Y, cell)
	}

	payload := scrollback + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", "cursor_left_hello_w80h24.snap")
	wantPayload, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}

	if payload != string(wantPayload) {
		t.Errorf("payload mismatch for %s\n--- WANT ---\n%q\n--- GOT ---\n%q",
			snapPath, string(wantPayload), payload)
	}
}
