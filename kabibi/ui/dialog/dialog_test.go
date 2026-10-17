package dialog

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func TestOpenInputDialog(t *testing.T) {
	d := NewInputDialog(ActionMkdir, "Create Dir", "Enter directory name:", "newdir", nil)
	if d == nil {
		t.Fatal("NewInputDialog returned nil")
	}
	if d.Kind != DialogInput {
		t.Errorf("Kind = %d, want %d", d.Kind, DialogInput)
	}
	if d.Title != "Create Dir" {
		t.Errorf("Title = %q, want %q", d.Title, "Create Dir")
	}
	if d.Prompt != "Enter directory name:" {
		t.Errorf("Prompt = %q, want %q", d.Prompt, "Enter directory name:")
	}
	if d.InputValue != "newdir" {
		t.Errorf("InputValue = %q, want %q", d.InputValue, "newdir")
	}
}

func TestOpenConfirmDialog(t *testing.T) {
	d := NewConfirmDialog(ActionDelete, "Confirm", "Really delete?", nil)
	if d == nil {
		t.Fatal("NewConfirmDialog returned nil")
	}
	if d.Kind != DialogConfirm {
		t.Errorf("Kind = %d, want %d", d.Kind, DialogConfirm)
	}
}

func TestOpenChoiceDialog(t *testing.T) {
	choices := []Choice{
		{Label: "Overwrite", Hotkey: "o"},
		{Label: "Skip", Hotkey: "s"},
	}
	d := NewChoiceDialog("Collision", "File exists", choices, nil)
	if d == nil {
		t.Fatal("NewChoiceDialog returned nil")
	}
	if d.Kind != DialogChoice {
		t.Errorf("Kind = %d, want %d", d.Kind, DialogChoice)
	}
	if len(d.Choices) != 2 {
		t.Errorf("len(Choices) = %d, want 2", len(d.Choices))
	}
}

func TestUpdateChoiceHotkey(t *testing.T) {
	picked := -1
	d := NewChoiceDialog("Test", "Pick", []Choice{
		{Label: "First", Hotkey: "f"},
		{Label: "Second", Hotkey: "s"},
	}, func(idx int) {
		picked = idx
	})

	d.HandleEvent(input.KeyPressEvent{Code: 's', Text: "s"})
	if picked != 1 {
		t.Errorf("hotkey 's' picked = %d, want 1", picked)
	}
}

func TestUpdateChoiceArrows(t *testing.T) {
	d := NewChoiceDialog("Test", "Pick", []Choice{
		{Label: "First", Hotkey: "f"},
		{Label: "Second", Hotkey: "s"},
		{Label: "Third", Hotkey: "t"},
	}, nil)

	if d.ChoiceIdx != 0 {
		t.Fatalf("initial ChoiceIdx = %d, want 0", d.ChoiceIdx)
	}
	d.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	if d.ChoiceIdx != 1 {
		t.Errorf("after Down ChoiceIdx = %d, want 1", d.ChoiceIdx)
	}
	d.HandleEvent(input.KeyPressEvent{Code: input.KeyUp})
	if d.ChoiceIdx != 0 {
		t.Errorf("after Up ChoiceIdx = %d, want 0", d.ChoiceIdx)
	}
}

func TestUpdateChoiceEsc(t *testing.T) {
	handlerCalled := false
	d := NewChoiceDialog("Test", "Pick", []Choice{
		{Label: "OK", Hotkey: "o"},
		{Label: "Cancel", Hotkey: "c"},
	}, func(idx int) {
		handlerCalled = true
		if idx != -1 {
			t.Errorf("Esc should pass idx=-1, got %d", idx)
		}
	})

	d.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	if !handlerCalled {
		t.Errorf("Esc did not call handler")
	}
}

func TestAcceptInput(t *testing.T) {
	var submitted string
	d := NewInputDialog(ActionMkdir, "Create", "Name:", "", nil)
	d.OnSubmitInput = func(_ DialogAction, val string, _ *filemanager.FileOp) {
		submitted = val
	}
	for _, r := range "myname" {
		d.HandleEvent(input.KeyPressEvent{Code: r, Text: string(r)})
	}
	d.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if submitted != "myname" {
		t.Errorf("submitted = %q, want %q", submitted, "myname")
	}
}

func TestDialogWidgetRender(t *testing.T) {
	d := NewChoiceDialog("Collision", "File exists", []Choice{
		{Label: "Overwrite", Hotkey: "o"},
		{Label: "Skip", Hotkey: "s"},
	}, nil)

	buf := d.RenderBuf()
	if buf.Width <= 0 || buf.Height <= 0 {
		t.Fatalf("DialogWidget rendered empty buffer: %#v", buf)
	}

	out := terminal.ToANSI(buf)
	if !strings.Contains(out, "Overwrite") || !strings.Contains(out, "Skip") {
		t.Fatalf("DialogWidget output missing expected choices: %q", out)
	}
}
