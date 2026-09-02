package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOpenInputDialog(t *testing.T) {
	m := initialModel()
	m.openInputDialog(actionMkdir, "Create Dir", "Enter directory name:", "newdir")

	if m.dialog == nil {
		t.Fatalf("openInputDialog did not create dialog")
	}

	if m.dialog.kind != dialogInput {
		t.Errorf("openInputDialog created kind %d, want %d", m.dialog.kind, dialogInput)
	}

	if m.dialog.title != "Create Dir" {
		t.Errorf("openInputDialog title = %q, want %q", m.dialog.title, "Create Dir")
	}

	if m.dialog.prompt != "Enter directory name:" {
		t.Errorf("openInputDialog prompt = %q, want %q", m.dialog.prompt, "Enter directory name:")
	}

	if m.mode != modeDialog {
		t.Errorf("openInputDialog did not set mode to modeDialog")
	}
}

func TestOpenConfirmDialog(t *testing.T) {
	m := initialModel()
	m.openConfirmDialog(actionDelete, "Confirm", "Really delete?", nil)

	if m.dialog == nil {
		t.Fatalf("openConfirmDialog did not create dialog")
	}

	if m.dialog.kind != dialogConfirm {
		t.Errorf("openConfirmDialog created kind %d, want %d", m.dialog.kind, dialogConfirm)
	}

	if m.mode != modeDialog {
		t.Errorf("openConfirmDialog did not set mode to modeDialog")
	}
}

func TestOpenChoiceDialog(t *testing.T) {
	m := initialModel()
	choices := []dlgChoice{
		{label: "Overwrite", hotkey: "o"},
		{label: "Skip", hotkey: "s"},
	}
	m.openChoiceDialog("Collision", "File exists", choices, nil)

	if m.dialog == nil {
		t.Fatalf("openChoiceDialog did not create dialog")
	}

	if m.dialog.kind != dialogChoice {
		t.Errorf("openChoiceDialog created kind %d, want %d", m.dialog.kind, dialogChoice)
	}

	if len(m.dialog.choices) != 2 {
		t.Errorf("openChoiceDialog has %d choices, want 2", len(m.dialog.choices))
	}
}

func TestCloseDialog(t *testing.T) {
	m := initialModel()
	m.dialog = &dialogState{kind: dialogInput}
	m.mode = modeDialog

	m.closeDialog()

	if m.dialog != nil {
		t.Errorf("closeDialog did not clear dialog")
	}

	if m.mode != modeBrowser {
		t.Errorf("closeDialog did not reset mode to modeBrowser")
	}
}

func TestUpdateChoiceHotkey(t *testing.T) {
	m := initialModel()
	m.openChoiceDialog("Test", "Pick", []dlgChoice{
		{label: "First", hotkey: "f"},
		{label: "Second", hotkey: "s"},
	}, nil)

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}}
	_, _ = m.updateChoice(msg)

	if m.dialog != nil {
		t.Errorf("updateChoice with matching hotkey did not close dialog")
	}
}

func TestUpdateChoiceArrows(t *testing.T) {
	m := initialModel()
	m.openChoiceDialog("Test", "Pick", []dlgChoice{
		{label: "First", hotkey: "f"},
		{label: "Second", hotkey: "s"},
		{label: "Third", hotkey: "t"},
	}, nil)

	d := m.dialog
	if d.choiceIdx != 0 {
		t.Fatalf("initial choiceIdx not 0")
	}

	msg := tea.KeyMsg{Type: tea.KeyDown}
	_, _ = m.updateChoice(msg)

	if d.choiceIdx != 1 {
		t.Errorf("updateChoice down: choiceIdx = %d, want 1", d.choiceIdx)
	}

	msg = tea.KeyMsg{Type: tea.KeyUp}
	_, _ = m.updateChoice(msg)

	if d.choiceIdx != 0 {
		t.Errorf("updateChoice up: choiceIdx = %d, want 0", d.choiceIdx)
	}
}

func TestUpdateChoiceEsc(t *testing.T) {
	handlerCalled := false
	m := initialModel()
	m.openChoiceDialog("Test", "Pick", []dlgChoice{
		{label: "OK", hotkey: "o"},
	}, func(m *model, idx int) (tea.Model, tea.Cmd) {
		handlerCalled = true
		if idx != -1 {
			t.Errorf("Esc should pass idx=-1, got %d", idx)
		}
		return m, nil
	})

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	_, _ = m.updateChoice(msg)

	if m.dialog != nil {
		t.Errorf("Esc did not close dialog")
	}
	if !handlerCalled {
		t.Errorf("Esc did not call handler")
	}
}

func TestAcceptInput(t *testing.T) {
	m := initialModel()
	m.openInputDialog(actionMkdir, "Create", "Name:", "test")
	m.dialog.input.SetValue("myname")

	_, _ = m.acceptInput()

	if m.dialog != nil {
		t.Errorf("acceptInput did not close dialog")
	}

	if m.mode != modeBrowser {
		t.Errorf("acceptInput did not reset mode")
	}
}
