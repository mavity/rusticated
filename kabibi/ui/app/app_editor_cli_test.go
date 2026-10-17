package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/x/input"
)

func TestOpenEditorSaveAndExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new-file.txt")
	exited := false
	widget := NewWidget(AppWidgetOptions{
		OnExit: func() { exited = true },
	})
	defer widget.Close()

	if err := widget.OpenEditor(path, true); err != nil {
		t.Fatalf("OpenEditor: %v", err)
	}
	editor := widget.Editor()
	if editor == nil {
		t.Fatal("OpenEditor did not install editor")
	}

	editor.HandleEvent(input.KeyPressEvent{Code: 'x', Text: "saved from editor"})
	editor.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not saved on editor exit: %v", err)
	}
	if string(got) != "saved from editor" {
		t.Fatalf("saved file = %q, want %q", got, "saved from editor")
	}
	if widget.Editor() != nil {
		t.Fatal("editor remained open after Escape")
	}
	if !exited {
		t.Fatal("CLI editor close did not invoke app exit handler")
	}
}

func TestOpenEditorSaveFailureKeepsEditorOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "file.txt")
	exited := false
	widget := NewWidget(AppWidgetOptions{
		OnExit: func() { exited = true },
	})
	defer widget.Close()

	if err := widget.OpenEditor(path, true); err != nil {
		t.Fatalf("OpenEditor: %v", err)
	}
	editor := widget.Editor()
	editor.HandleEvent(input.KeyPressEvent{Code: 'x', Text: "cannot save"})
	editor.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})

	if widget.Editor() == nil {
		t.Fatal("editor closed after save failure")
	}
	if exited {
		t.Fatal("app exited after editor save failure")
	}
}
