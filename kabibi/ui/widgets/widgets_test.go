package widgets

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func renderWidgetANSIForTest(t *testing.T, text string, width, height int) (terminal.CellBuf, ui.CursorPos) {
	t.Helper()
	frame, cursor, extent, err := terminal.NewTerminal(width, height).RenderANSIForComposition(text, width, height)
	if err != nil {
		t.Fatal(err)
	}
	buf := terminal.NewCellBuf(width, extent)
	copy(buf.Cells(), frame.Cells()[:extent*width])
	return buf, cursor
}

func TestTextInputPasteAndSelectionReplacement(t *testing.T) {
	inputWidget := NewTextInput(TextInputOptions{Value: "abcdef", AllowPaste: true})
	inputWidget.moveCursor(2, false)
	inputWidget.moveCursor(4, true)
	if !inputWidget.HandleEvent(input.PasteEvent("XYZ")) {
		t.Fatal("paste event was not handled")
	}
	if got, want := inputWidget.Value(), "abXYZef"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestTextInputHorizontalScrollAndEllipses(t *testing.T) {
	inputWidget := NewTextInput(TextInputOptions{Value: "0123456789abcdefghij"})
	inputWidget.moveCursor(10, false)
	buf, cursor := renderWidgetANSIForTest(t, inputWidget.Render(ui.Rect{W: 8, H: 1}, ui.RenderContext{Focused: true}), 8, 1)

	if got := buf.Get(0, 0).Grapheme; got != "…" {
		t.Fatalf("left overflow indicator = %q, want ellipsis", got)
	}
	if got := buf.Get(buf.Width-1, 0).Grapheme; got != "…" {
		t.Fatalf("right overflow indicator = %q, want ellipsis", got)
	}
	if cursor.X < 1 || cursor.X >= buf.Width-1 {
		t.Fatalf("cursor x = %d, want inside visible text area", cursor.X)
	}
}

func TestTextInputWideCharactersAndOverflow(t *testing.T) {
	inputWidget := NewTextInput(TextInputOptions{Value: "界abc界", SelectAll: true})
	buf, _ := renderWidgetANSIForTest(t, inputWidget.Render(ui.Rect{W: 5, H: 1}, ui.RenderContext{Focused: true}), 5, 1)
	var rendered strings.Builder
	for x := 0; x < buf.Width; x++ {
		rendered.WriteString(buf.Get(x, 0).Grapheme)
	}
	if !strings.Contains(rendered.String(), "…") {
		t.Fatalf("rendered cells %q contain no overflow indicator", rendered.String())
	}
}

func TestCheckboxTogglesOnlyOnSpace(t *testing.T) {
	check := &Checkbox{Label: "Follow links"}
	if check.HandleEvent(input.KeyPressEvent{Code: 'x', Text: "x"}) {
		t.Fatal("checkbox consumed unrelated key")
	}
	if !check.HandleEvent(input.KeyPressEvent{Code: input.KeySpace}) || !check.Checked {
		t.Fatal("space did not toggle checkbox on")
	}
	check.Disabled = true
	if check.HandleEvent(input.KeyPressEvent{Code: input.KeySpace}) || !check.Checked {
		t.Fatal("disabled checkbox changed state")
	}
}

func TestButtonRowNavigationAndHotkey(t *testing.T) {
	picked := ""
	row := NewButtonRow([]Button{
		{Label: "Apply", Hotkey: "a", OnPress: func() { picked = "apply" }},
		{Label: "Cancel", Hotkey: "c", OnPress: func() { picked = "cancel" }},
	})
	if !row.HandleEvent(input.KeyPressEvent{Code: input.KeyRight}) || row.Index != 1 {
		t.Fatalf("right navigation selected index %d, want 1", row.Index)
	}
	if !row.HandleEvent(input.KeyPressEvent{Code: 'a', Text: "a"}) || picked != "apply" {
		t.Fatalf("hotkey activation picked %q, want apply", picked)
	}
}

func TestButtonRowWrapsToAvailableHeight(t *testing.T) {
	row := NewButtonRow([]Button{
		{Label: "Overwrite"},
		{Label: "Skip"},
		{Label: "Cancel"},
	})
	size := row.Measure(ui.Constraints{MaxW: 12, MaxH: 3})
	if size.H < 2 {
		t.Fatalf("measured height = %d, want wrapped rows", size.H)
	}
	buf, _ := renderWidgetANSIForTest(t, row.Render(ui.Rect{W: 12, H: size.H}, ui.RenderContext{}), 12, size.H)
	if terminal.ToANSI(buf) == "" {
		t.Fatal("button row rendered empty output")
	}
}

func TestProgressBarRespondsToConstraints(t *testing.T) {
	progress := &ProgressBar{Current: "a/very/long/path/to/file", Done: 50, Total: 100, Rate: 10}
	size := progress.Measure(ui.Constraints{MaxW: 10, MaxH: 2})
	if size.W != 10 || size.H != 2 {
		t.Fatalf("Measure() = %+v, want 10x2", size)
	}
	buf, _ := renderWidgetANSIForTest(t, progress.Render(ui.Rect{W: 10, H: 2}, ui.RenderContext{}), 10, 2)
	if !strings.Contains(terminal.ToANSI(buf), "50%") {
		t.Fatal("progress output does not show percentage")
	}
}

func TestControlSemanticCallbacks(t *testing.T) {
	var changed string
	in := NewTextInput(TextInputOptions{
		OnChanged: func(v string) { changed = v },
	})
	in.HandleEvent(input.KeyPressEvent{Code: 'a', Text: "a"})
	if changed != "a" {
		t.Fatalf("TextInput.OnChanged = %q, want %q", changed, "a")
	}

	var toggled bool
	cb := &Checkbox{
		Label:     "Option",
		OnToggled: func(v bool) { toggled = v },
	}
	cb.HandleEvent(input.KeyPressEvent{Code: input.KeySpace})
	if !toggled || !cb.Checked {
		t.Fatalf("Checkbox.OnToggled = %v, Checked = %v; want true", toggled, cb.Checked)
	}

	clicked := false
	btn := &Button{
		Label:   "OK",
		OnClick: func() { clicked = true },
	}
	row := NewButtonPtrRow(btn)
	row.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if !clicked {
		t.Fatal("Button.OnClick was not called on Enter")
	}
}

func TestGridSymmetricTrackResolutionAndWrapping(t *testing.T) {
	in1 := NewTextInput(TextInputOptions{Value: "first"})
	in2 := NewTextInput(TextInputOptions{Value: "second"})
	explanation := &TextBlock{Text: "This is a multi-word explanation that wraps across multiple rows when narrow."}
	g := &Grid{
		Columns: []Track{
			{Kind: TrackFixed, Value: 10},
			{Kind: TrackFraction, Value: 1},
		},
		Children: []GridChild{
			{Row: 0, Column: 0, ColSpan: 2, Child: explanation},
			{Row: 1, Column: 0, Child: &Label{Text: "Source"}},
			{Row: 1, Column: 1, Child: in1},
			{Row: 2, Column: 0, Child: &Label{Text: "Target"}},
			{Row: 2, Column: 1, Child: in2},
		},
	}
	g.SetFocusedWidget(in2)

	size := g.Measure(ui.Constraints{MaxW: 30, MaxH: 20})
	if size.W != 30 {
		t.Fatalf("Grid.Measure width = %d, want 30", size.W)
	}
	if size.H < 5 {
		t.Fatalf("Grid.Measure height = %d, want >= 5 with wrapped TextBlock", size.H)
	}

	buf, cursor := renderWidgetANSIForTest(t, g.Render(ui.Rect{W: size.W, H: size.H}, ui.RenderContext{Focused: true}), size.W, size.H)
	if buf.Width != size.W || buf.Height != size.H {
		t.Fatalf("Grid.Render size = %dx%d, want %dx%d", buf.Width, buf.Height, size.W, size.H)
	}
	if !cursor.Visible {
		t.Fatal("expected visible cursor from focused second input")
	}
	if cursor.X < 10 {
		t.Fatalf("cursor.X = %d, want >= 10 (in column 1)", cursor.X)
	}
	if cursor.Y != size.H-1 {
		t.Fatalf("cursor.Y = %d, want %d (on row 2)", cursor.Y, size.H-1)
	}
}

func TestNestedFrameAndGridFocusAndRouting(t *testing.T) {
	cb1 := &Checkbox{Label: "Show hidden files"}
	cb2 := &Checkbox{Label: "Show status bar"}
	frame1 := &Frame{
		Title: "Panel settings",
		Child: &Grid{
			Children: []GridChild{
				{Row: 0, Child: cb1},
			},
		},
	}
	frame2 := &Frame{
		Title: "Display settings",
		Child: &Grid{
			Children: []GridChild{
				{Row: 0, Child: cb2},
			},
		},
	}
	root := &Grid{
		Children: []GridChild{
			{Row: 0, Child: frame1},
			{Row: 1, Child: frame2},
		},
	}
	if !root.SetFocusedWidget(cb2) {
		t.Fatal("SetFocusedWidget(cb2) returned false")
	}
	if !root.HandleEvent(input.KeyPressEvent{Code: input.KeySpace}) {
		t.Fatal("Space was not routed through root Grid -> frame2 -> inner Grid -> cb2")
	}
	if cb1.Checked || !cb2.Checked {
		t.Fatalf("Checkbox states = (%v, %v), want (false, true)", cb1.Checked, cb2.Checked)
	}

	size := root.Measure(ui.Constraints{MaxW: 40, MaxH: 20})
	buf, _ := renderWidgetANSIForTest(t, root.Render(ui.Rect{W: size.W, H: size.H}, ui.RenderContext{Focused: true}), size.W, size.H)
	out := terminal.ToANSI(buf)
	if !strings.Contains(out, "Panel settings") || !strings.Contains(out, "Display settings") || !strings.Contains(out, "[x] Show status bar") {
		t.Fatalf("rendered nested frames missing expected text: %q", out)
	}
}
