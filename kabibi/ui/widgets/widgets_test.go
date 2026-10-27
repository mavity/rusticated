package widgets

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

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
	buf, cursor := inputWidget.Render(ui.Rect{W: 8, H: 1}, ui.RenderContext{Focused: true})

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
	buf, _ := inputWidget.Render(ui.Rect{W: 5, H: 1}, ui.RenderContext{Focused: true})
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
	buf, _ := row.Render(ui.Rect{W: 12, H: size.H}, ui.RenderContext{})
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
	buf, _ := progress.Render(ui.Rect{W: 10, H: 2}, ui.RenderContext{})
	if !strings.Contains(terminal.ToANSI(buf), "50%") {
		t.Fatal("progress output does not show percentage")
	}
}

