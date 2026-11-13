package dialog

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func renderDialogANSIForTest(t *testing.T, text string, width, height int) (terminal.CellBuf, terminal.CursorPos) {
	t.Helper()
	frame, cursor, extent, err := terminal.NewTerminal(width, height).RenderANSIForComposition(text, width, height)
	if err != nil {
		t.Fatal(err)
	}
	buf := terminal.NewCellBuf(width, extent)
	copy(buf.Cells(), frame.Cells()[:extent*width])
	return buf, cursor
}

func bufText(buf terminal.CellBuf) string {
	var output strings.Builder
	for y := 0; y < buf.Height; y++ {
		for x := 0; x < buf.Width; x++ {
			output.WriteString(buf.Get(x, y).Grapheme)
		}
		output.WriteByte('\n')
	}
	return output.String()
}

func TestMkdirDialogAcceptsInputAndRendersLabel(t *testing.T) {
	var submitted string
	d := NewMkdirDialog(nil, func(dir string) {
		submitted = dir
	}, nil)
	for _, r := range "myname" {
		d.HandleEvent(input.KeyPressEvent{Code: r, Text: string(r)})
	}
	size := d.Measure(Constraints{MaxW: 80, MaxH: 24})
	buf, cursor := renderDialogANSIForTest(t, d.Render(Rect{W: size.W, H: size.H}, RenderContext{Focused: true}), size.W, size.H)
	text := bufText(buf)
	if !strings.Contains(text, "Create directory:") {
		t.Fatalf("MkdirDialog missing prompt label: %q", text)
	}
	if !cursor.Visible {
		t.Fatal("MkdirDialog input cursor should be visible when focused")
	}

	d.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if submitted != "myname" {
		t.Fatalf("submitted = %q, want %q", submitted, "myname")
	}
}

func TestDeleteConfirmDialogDefaultsToCancelAndSupportsHotkeys(t *testing.T) {
	confirmed := false
	canceled := false
	d := NewDeleteConfirmDialog("target.txt", func() {
		confirmed = true
	}, func() {
		canceled = true
	})
	if d.Buttons.Index != 1 {
		t.Fatalf("initial button index = %d, want 1 (Cancel)", d.Buttons.Index)
	}

	d.HandleEvent(input.KeyPressEvent{Code: 'y', Text: "y"})
	if !confirmed || canceled {
		t.Fatalf("hotkey 'y' confirmed=%v canceled=%v, want true/false", confirmed, canceled)
	}
}

func TestCopyPathDialogTabFocusAndSubmit(t *testing.T) {
	var gotSrc, gotDst string
	d := NewCopyPathDialog(nil, nil, func(source, destination string) {
		gotSrc = source
		gotDst = destination
	}, nil)
	d.Source.SetValue("a")
	d.Destination.SetValue("b")

	if !d.HandleEvent(input.KeyPressEvent{Code: input.KeyTab}) {
		t.Fatal("Tab was not consumed")
	}
	d.Destination.SetValue("x")
	d.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if gotSrc != "a" || gotDst != "x" {
		t.Fatalf("submitted = (%q, %q), want (\"a\", \"x\")", gotSrc, gotDst)
	}
}

func TestConcreteDialogsMeasureWithinSmallAndLargeConstraints(t *testing.T) {
	dialogs := []Widget{
		NewMkdirDialog(nil, nil, nil),
		NewRenameSelectedDialog("old.txt", nil, nil, nil),
		NewRenamePathDialog(nil, nil, nil, nil),
		NewCopySelectedDialog("file.txt", nil, nil, nil),
		NewCopyPathDialog(nil, nil, nil, nil),
		NewMoveSelectedDialog("file.txt", nil, nil, nil),
		NewMovePathDialog(nil, nil, nil, nil),
		NewDeletePathDialog(nil, nil, nil),
		NewDeleteConfirmDialog("file.txt", nil, nil),
		NewEditDialog(nil, nil, nil),
		NewMountDialog(nil, "/workspace", nil, nil),
		NewFindFilesDialog(nil, "**", "", "20", nil, nil),
		NewSettingsDialog(SettingsValues{}, nil, nil),
		NewErrorDialog("Slash Command Error", "unknown slash command \"/set\"", nil),
		NewProgressDialog("Copying", filemanager.FileOpProgress{Current: "a.txt", Done: 5, Total: 10}, nil),
	}

	for _, dlg := range dialogs {
		for _, constraints := range []Constraints{
			{MaxW: 24, MaxH: 8},
			{MaxW: 80, MaxH: 24},
			{MaxW: 120, MaxH: 40},
		} {
			size := dlg.Measure(constraints)
			if size.W > constraints.MaxW || size.H > constraints.MaxH || size.W < 1 || size.H < 1 {
				t.Fatalf("%T.Measure(%+v) = %+v, outside constraints", dlg, constraints, size)
			}
			buf, _ := renderDialogANSIForTest(t, dlg.Render(Rect{W: size.W, H: size.H}, RenderContext{Focused: true}), size.W, size.H)
			if buf.Width != size.W || buf.Height != size.H {
				t.Fatalf("%T.Render size = %dx%d, want %dx%d", dlg, buf.Width, buf.Height, size.W, size.H)
			}
		}
	}
}

func TestDialogStableWidthDoesNotResizeOnTyping(t *testing.T) {
	d := NewMkdirDialog(nil, nil, nil)
	before := d.Measure(Constraints{MaxW: 80, MaxH: 24})
	for _, r := range "a_very_long_directory_name_that_scrolls_horizontally_inside_input_without_changing_dialog_width" {
		d.HandleEvent(input.KeyPressEvent{Code: r, Text: string(r)})
	}
	after := d.Measure(Constraints{MaxW: 80, MaxH: 24})
	if before != after {
		t.Fatalf("dialog resized while typing: before=%+v after=%+v", before, after)
	}
}

func TestSettingsDialogFramesAndCheckboxToggles(t *testing.T) {
	var submitted SettingsValues
	d := NewSettingsDialog(SettingsValues{ShowPanels: false}, func(v SettingsValues) {
		submitted = v
	}, nil)
	size := d.Measure(Constraints{MaxW: 80, MaxH: 24})
	buf, _ := renderDialogANSIForTest(t, d.Render(Rect{W: size.W, H: size.H}, RenderContext{Focused: true}), size.W, size.H)
	text := bufText(buf)
	if !strings.Contains(text, "Panel settings") || !strings.Contains(text, "Display settings") {
		t.Fatalf("SettingsDialog missing grouped frame titles: %q", text)
	}

	d.HandleEvent(input.KeyPressEvent{Code: input.KeySpace})
	d.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if !submitted.ShowPanels {
		t.Fatalf("SettingsDialog did not submit toggled ShowPanels checkbox: %+v", submitted)
	}
}

func TestProgressCollisionFrameExpandsInPlaceAndResolves(t *testing.T) {
	d := NewProgressDialog("Copying", filemanager.FileOpProgress{Current: "dir/file.txt", Done: 5, Total: 10}, nil)
	before := d.Measure(Constraints{MaxW: 100, MaxH: 24})
	var selected filemanager.CollisionChoice
	d.SetCollision("target/file.txt", func(choice filemanager.CollisionChoice) {
		selected = choice
	})
	after := d.Measure(Constraints{MaxW: 100, MaxH: 24})
	if !d.Collision || after.H <= before.H || after.W <= before.W {
		t.Fatalf("collision state did not expand into CollisionFrame profile: before=%+v after=%+v", before, after)
	}
	buf, _ := renderDialogANSIForTest(t, d.Render(Rect{W: after.W, H: after.H}, RenderContext{Focused: true}), after.W, after.H)
	text := bufText(buf)
	if !strings.Contains(text, "File already exists:") || !strings.Contains(text, "file.txt") || !strings.Contains(text, "Skip all") {
		t.Fatalf("ProgressDialog collision frame missing expected text: %q", text)
	}

	d.HandleEvent(input.KeyPressEvent{Code: 'k', Text: "k"})
	if d.Collision {
		t.Fatal("collision state was not cleared after selecting Skip all")
	}
	if selected != filemanager.ColSkipAll {
		t.Fatalf("collision choice = %v, want skip all", selected)
	}
}
