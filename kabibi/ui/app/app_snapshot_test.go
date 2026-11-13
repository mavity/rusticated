package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func renderAppANSIForTest(t *testing.T, text string, width, height int) (terminal.CellBuf, ui.CursorPos) {
	t.Helper()
	frame, cursor, extent, err := terminal.NewTerminal(width, height).RenderANSIForComposition(text, width, height)
	if err != nil {
		t.Fatal(err)
	}
	buf := terminal.NewCellBuf(width, extent)
	copy(buf.Cells(), frame.Cells()[:extent*width])
	return buf, cursor
}

func TestAppWidgetShellCursorRemainsVisibleWhilePanelsRouteNavigation(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir(), PanelsVisible: true})
	defer widget.Close()
	rect := ui.Rect{W: 80, H: 24}

	widget.HandleEvent(input.KeyPressEvent{Code: 'x', Text: "x"})
	if got := widget.Shell().Input().Value(); got != "x" {
		t.Fatalf("shell input after typing with panels visible = %q, want %q", got, "x")
	}
	_, cursor := renderAppANSIForTest(t, widget.Render(rect, ui.RenderContext{Focused: true}), rect.W, rect.H)
	if !cursor.Visible {
		t.Fatal("shell cursor should be visible while shell typing is allowed with panels visible")
	}

	widget.openSettingsDialog()
	_, cursor = renderAppANSIForTest(t, widget.Render(rect, ui.RenderContext{Focused: true}), rect.W, rect.H)
	if cursor.Visible {
		t.Fatal("shell cursor should be hidden while a modal dialog takes focus")
	}
}

func TestAppWidget_DialogShadowDarkensRightBottomAndHaloOverDefaultColors(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir(), PanelsVisible: false})
	defer widget.Close()
	widget.openMkdirDialog()

	rect := ui.Rect{W: 80, H: 24}
	theme := ui.Theme{
		DefaultForeground: 0xD0D0D0,
		DefaultBackground: 0xFAF4E8,
		HasDefaultFG:      true,
		HasDefaultBG:      true,
	}
	dlgSize := widget.Dialog().Measure(ui.Constraints{MaxW: rect.W, MaxH: rect.H})
	buf, _ := renderAppANSIForTest(t, widget.Render(rect, ui.RenderContext{
		Focused: true,
		Theme:   theme,
	}), rect.W, rect.H)

	dx := (buf.Width - dlgSize.W) / 2
	dy := (buf.Height - dlgSize.H) / 2
	if buf.Height >= dlgSize.H+2 && dy+dlgSize.H+2 > buf.Height {
		dy = buf.Height - dlgSize.H - 2
	}

	// Vertical levels: 1 down (darkest) < 2 down (much less dark) < 1 up (very light dark) < untouched
	down1 := buf.Get(dx+4, dy+dlgSize.H).Style.Background()
	down2 := buf.Get(dx+4, dy+dlgSize.H+1).Style.Background()
	up1 := buf.Get(dx+4, dy-1).Style.Background()
	if down1 != 0x7C7973 {
		t.Fatalf("first shadow row background = %#06x, want %#06x at 70%% opacity", down1, terminal.Color(0x7C7973))
	}
	if !(down1 < down2 && down2 < up1 && up1 < theme.DefaultBackground) {
		t.Fatalf("vertical shadow backgrounds (down1=%#06x, down2=%#06x, up1=%#06x) want strictly increasing toward %#06x",
			down1, down2, up1, theme.DefaultBackground)
	}

	// Horizontal 2:1 stretch with per-character gradient steps:
	// cols 1, 2, 3, 4 to the right must each be strictly lighter than the previous column,
	// and cols 1, 2 to the left must also step strictly toward DefaultBackground.
	right1 := buf.Get(dx+dlgRectRightOffset(dlgSize.W, 1), dy+2).Style.Background()
	right2 := buf.Get(dx+dlgRectRightOffset(dlgSize.W, 2), dy+2).Style.Background()
	right3 := buf.Get(dx+dlgRectRightOffset(dlgSize.W, 3), dy+2).Style.Background()
	right4 := buf.Get(dx+dlgRectRightOffset(dlgSize.W, 4), dy+2).Style.Background()
	left1 := buf.Get(dx-1, dy+2).Style.Background()
	left2 := buf.Get(dx-2, dy+2).Style.Background()
	if !(right1 < right2 && right2 < right3 && right3 < right4 && right4 < left1 && left1 < left2 && left2 < theme.DefaultBackground) {
		t.Fatalf("horizontal per-char shadow steps (right1..4=%#06x,%#06x,%#06x,%#06x left1..2=%#06x,%#06x) want strictly increasing toward %#06x",
			right1, right2, right3, right4, left1, left2, theme.DefaultBackground)
	}
}

func dlgRectRightOffset(width, dist int) int {
	return width + dist - 1
}

func assertAppSnapshot(t *testing.T, snapFile string, widget *AppWidget, scrollback *string, rect ui.Rect) {
	t.Helper()

	buf, cursor := renderAppANSIForTest(t, widget.Render(rect, ui.RenderContext{Focused: true}), rect.W, rect.H)
	if cursor.Visible && cursor.X >= 0 && cursor.X < buf.Width && cursor.Y >= 0 && cursor.Y < buf.Height {
		cell := buf.Get(cursor.X, cursor.Y)
		if cell.Grapheme == "" || cell.Grapheme == " " {
			cell.Grapheme = "\u0332" // Combining low line on blank cell
		} else {
			cell.Grapheme = cell.Grapheme + "\u0332" // Non-destructive underline on existing character
		}
		buf.Set(cursor.X, cursor.Y, cell)
	}

	sb := ""
	if scrollback != nil && len(*scrollback) > 0 {
		sb = *scrollback + "-------scrollback-------\r\n"
	}
	got := sb + terminal.ToANSI(buf)

	snapPath := filepath.Join("snapshot", snapFile)

	wantBytes, err := os.ReadFile(snapPath)
	if err != nil {
		// NEVER implement auto-update of snapshots
		// Snapshots must be manually updated, NEVER automatically.
		t.Fatalf("failed to read snapshot %s: %v", snapPath, err)
	}
	want := strings.ReplaceAll(string(wantBytes), "{ver}", shell.Version())

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

type deterministicSnapshotFS struct{}

func (deterministicSnapshotFS) ReadDir(path string) ([]filemanager.FSEntry, error) {
	return []filemanager.FSEntry{
		{Name: "docs", IsDir: true, Size: 4096, Mode: 0o755},
		{Name: "src", IsDir: true, Size: 4096, Mode: 0o755},
		{Name: "build.sh", IsExec: true, Size: 128, Mode: 0o755},
		{Name: "main.go", Size: 4096, Mode: 0o644},
		{Name: "README.md", Size: 2048, Mode: 0o644},
	}, nil
}

func newBluePanelsSnapshotWidget(scrollback *string) *AppWidget {
	w := NewWidget(AppWidgetOptions{
		WriteToScrollback: func(s string) {
			if scrollback != nil {
				*scrollback += s
			}
		},
		FSService: deterministicSnapshotFS{},
	})
	w.Shell().SetPrompt("፧")
	w.SetPanelsVisible(true)
	_ = w.Panels().Left.LoadDir("/workspace/left")
	_ = w.Panels().Right.LoadDir("/workspace/right")
	w.Panels().Left.Title = "left"
	w.Panels().Right.Title = "right"
	return w
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
// State: Fresh shell, startup banner, ፧ prompt ready for input, and blank footer below
func TestAppWidget_Snapshot_Initial_W80H24(t *testing.T) {
	var scrollback string
	widget := newSnapshotWidget(&scrollback)
	assertAppSnapshot(t, "initial_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}

func TestAppWidget_BluePanelsPreserveTerminalBackgroundOutsidePanels(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{PanelsVisible: true})
	defer widget.Close()

	buf, _ := renderAppANSIForTest(t, widget.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 24}, ui.RenderContext{}), 80, 24)

	panelCell := buf.Get(10, 8)
	if got := panelCell.Style.Background(); got != terminal.Color(0x0000A8) {
		t.Fatalf("panel background = %#06x, want blue %#06x", got, 0x0000A8)
	}

	panelBottomCell := buf.Get(10, 17)
	if got := panelBottomCell.Style.Background(); got != terminal.Color(0x0000A8) {
		t.Fatalf("panel bottom background = %#06x, want blue through row 17", got)
	}
	gapStartCell := buf.Get(10, 18)
	if !gapStartCell.Style.HasAttribute(terminal.CharResetBackgroundDefault) {
		t.Fatalf("row 18 background style = %#x, want terminal default below panels", gapStartCell.Style.Attributes())
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

// TestAppWidget_Snapshot_BluePanels_Open_W80H24 verifies the dual blue panels
// HUD over the top 18 rows with a 6-row bottom gap for the startup banner,
// spacer, ፧ prompt, and blank footer.
func TestAppWidget_Snapshot_BluePanels_Open_W80H24(t *testing.T) {
	var scrollback string
	widget := newBluePanelsSnapshotWidget(&scrollback)
	defer widget.Close()
	assertAppSnapshot(t, "blue_panels_open_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}

// TestAppWidget_Snapshot_BluePanels_TabAndMark_W80H24 verifies Tab switching
// active pane (Left -> Right), Down navigation, and Insert item marking (•).
func TestAppWidget_Snapshot_BluePanels_TabAndMark_W80H24(t *testing.T) {
	var scrollback string
	widget := newBluePanelsSnapshotWidget(&scrollback)
	defer widget.Close()

	// Mark "docs" in Left pane with Insert, then Tab to Right pane and move Down to "build.sh*"
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyInsert})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})

	assertAppSnapshot(t, "blue_panels_tab_mark_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}

// TestAppWidget_Snapshot_BluePanels_Withdrawn_W80H24 verifies that when panels
// are opened via Ctrl+O and then withdrawn via Ctrl+O, the stack anchors cleanly
// at row 0 without blue background bleed.
func TestAppWidget_Snapshot_BluePanels_Withdrawn_W80H24(t *testing.T) {
	var scrollback string
	widget := newBluePanelsSnapshotWidget(&scrollback)
	defer widget.Close()

	widget.SetPanelsVisible(false)
	assertAppSnapshot(t, "blue_panels_withdrawn_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}

// TestAppWidget_Snapshot_BluePanels_MkdirDialog_W80H24 verifies the F7 Mkdir
// modal dialog composited over the dual blue panels and bottom shell stack.
func TestAppWidget_Snapshot_BluePanels_MkdirDialog_W80H24(t *testing.T) {
	var scrollback string
	widget := newBluePanelsSnapshotWidget(&scrollback)
	defer widget.Close()

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyF7})
	typeString(widget, "new_folder")
	assertAppSnapshot(t, "blue_panels_mkdir_dialog_w80h24.snap", widget, &scrollback, ui.Rect{X: 0, Y: 0, W: 80, H: 24})
}
