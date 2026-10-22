package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
)

type instantSearchFS struct {
	entries []filemanager.FSEntry
}

func (s *instantSearchFS) ReadDir(string) ([]filemanager.FSEntry, error) {
	return s.entries, nil
}

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

func TestEnterOnNonExecutableFileOpensEditor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	widget := NewWidget(AppWidgetOptions{
		WorkingDir:    dir,
		PanelsVisible: true,
	})
	defer widget.Close()

	widget.Panels().Left.SelectByName(filepath.Base(path))
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	if widget.Editor() == nil {
		t.Fatal("Enter on a non-executable file did not open the editor")
	}
}

func TestCtrlEnterInsertsSelectedFileAtPromptCursor(t *testing.T) {
	dir := t.TempDir()
	name := "file with spaces.txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	widget := NewWidget(AppWidgetOptions{
		WorkingDir:    dir,
		PanelsVisible: true,
	})
	defer widget.Close()
	widget.Panels().Left.SelectByName(name)

	inputText := "echo  after"
	cursor := len([]rune("echo "))
	widget.Shell().SetInput(inputText)
	widget.Shell().SetCursorPos(cursor)
	widget.HandleEvent(readKittyKeyPress(t, "\x1b[13;5u"))

	filename := shellQuoteFilename(name)
	want := "echo " + filename + " after"
	if got := widget.Shell().Input().Value(); got != want {
		t.Fatalf("Ctrl+Enter input = %q, want %q", got, want)
	}
	if got, wantCursor := widget.Shell().Input().CursorPos(), len([]rune("echo "+filename)); got != wantCursor {
		t.Fatalf("Ctrl+Enter cursor = %d, want %d", got, wantCursor)
	}

	fullPath, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	widget.Shell().SetInput(inputText)
	widget.Shell().SetCursorPos(cursor)
	widget.HandleEvent(readKittyKeyPress(t, "\x1b[13;6u"))

	quotedPath := shellQuoteFilename(fullPath)
	want = "echo " + quotedPath + " after"
	if got := widget.Shell().Input().Value(); got != want {
		t.Fatalf("Ctrl+Shift+Enter input = %q, want %q", got, want)
	}
	if got, wantCursor := widget.Shell().Input().CursorPos(), len([]rune("echo "+quotedPath)); got != wantCursor {
		t.Fatalf("Ctrl+Shift+Enter cursor = %d, want %d", got, wantCursor)
	}
}

func TestInstantSearchPanelKeyboardRouting(t *testing.T) {
	dir := t.TempDir()
	widget := NewWidget(AppWidgetOptions{
		WorkingDir:    dir,
		PanelsVisible: true,
		FSService: &instantSearchFS{entries: []filemanager.FSEntry{
			{Name: "alpha"},
			{Name: "test-dir", IsDir: true},
			{Name: "zeta"},
		}},
	})
	defer widget.Close()

	pane := widget.Panels().Left
	pane.SelectByName("alpha")
	altT := readKittyKeyPress(t, "\x1b[116;3:1;8224u")
	if altT.Code != 't' || altT.Mod != input.ModAlt || altT.Text != "†" {
		t.Fatalf("decoded Alt+t event = %#v, want key t with Alt and Option-produced text", altT)
	}
	widget.HandleEvent(altT)
	if !pane.SearchActive() {
		t.Fatal("Alt+t with panels visible did not activate instant search")
	}
	if item, _ := pane.SelectedItem(); item.Name() != "test-dir" {
		t.Fatalf("instant search selected %q, want test-dir", item.Name())
	}
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	if pane.SearchActive() {
		t.Fatal("Escape did not close instant search")
	}
	if item, _ := pane.SelectedItem(); item.Name() != "test-dir" {
		t.Fatalf("Escape changed the matched selection to %q", item.Name())
	}

	pane.SelectByName("alpha")
	widget.HandleEvent(input.KeyPressEvent{Code: 't', Mod: input.ModAlt})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyDown})
	if pane.SearchActive() {
		t.Fatal("Down did not close instant search")
	}
	if item, _ := pane.SelectedItem(); item.Name() != "alpha" {
		t.Fatalf("Down after closing search selected %q, want alpha", item.Name())
	}

	pane.SelectByName("alpha")
	widget.HandleEvent(input.KeyPressEvent{Code: 't', Mod: input.ModAlt})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if pane.SearchActive() {
		t.Fatal("Enter did not close instant search")
	}
	if filepath.Clean(pane.Dir()) != filepath.Clean(filepath.Join(dir, "test-dir")) {
		t.Fatalf("Enter did not activate the selected directory: %q", pane.Dir())
	}

	widget.SetPanelsVisible(false)
	widget.HandleEvent(input.KeyPressEvent{Code: 't', Mod: input.ModAlt})
	if pane.SearchActive() {
		t.Fatal("Alt+type activated instant search while panels were hidden")
	}
}

func readKittyKeyPress(t *testing.T, sequence string) input.KeyPressEvent {
	t.Helper()

	reader, err := input.NewReader(strings.NewReader(sequence), "", 0)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer reader.Close()

	events, err := reader.ReadEvents()
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ReadEvents returned %d events, want 1", len(events))
	}
	key, ok := events[0].(input.KeyPressEvent)
	if !ok {
		t.Fatalf("decoded event = %T, want input.KeyPressEvent", events[0])
	}
	return key
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
