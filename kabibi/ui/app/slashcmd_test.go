package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
)

func TestSlashCommandHintsAndUniqueCompletion(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()

	widget.Shell().SetInput("/set")
	buf, _ := renderAppANSIForTest(t, widget.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{}), 80, 24)
	var footer strings.Builder
	for x := 0; x < buf.Width; x++ {
		footer.WriteString(buf.Get(x, buf.Height-1).Grapheme)
	}
	if !strings.Contains(footer.String(), "/settings") {
		t.Fatalf("slash hints footer %q does not contain /settings", footer.String())
	}

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if got := widget.Shell().Input().Value(); got != "/settings " {
		t.Fatalf("input after unique completion = %q, want %q", got, "/settings ")
	}
}

func TestSlashCommandArrowSelectionAndDispatch(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	widget.Shell().SetInput("/m")

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyUp})
	_, selected := widget.slashMatches()
	if selected != 0 {
		t.Fatalf("Up changed slash selection to %d; want 0", selected)
	}
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyLeft})
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if widget.Dialog() == nil {
		t.Fatal("Left then Enter did not open /move dialog")
	}
	if widget.PanelsVisible() {
		t.Fatal("/move dialog unexpectedly opened blue panels")
	}
	if got := widget.Shell().Input().Value(); got != "" {
		t.Fatalf("input after running /move = %q, want empty", got)
	}
}

func TestSlashCommandTabCyclesMultipleMatches(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	widget.Shell().SetInput("/mov")

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if got := widget.Shell().Input().Value(); got != "/move " {
		t.Fatalf("input after unique completion = %q, want /move ", got)
	}
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if widget.Dialog() == nil {
		t.Fatal("Tab then Enter did not open /move dialog")
	}
	if widget.PanelsVisible() {
		t.Fatal("Tab then Enter unexpectedly opened blue panels")
	}
}

func TestSlashCopyOpensPathFormWithoutPanels(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	destDir := filepath.Join(dir, "destination")
	if err := os.WriteFile(src, []byte("copy me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	widget := NewWidget(AppWidgetOptions{WorkingDir: dir})
	defer widget.Close()

	widget.Shell().SetInput("/copy")
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	dlg := widget.Dialog()
	if dlg == nil {
		t.Fatal("/copy did not open a dialog")
	}
	if len(dlg.Fields) != 2 {
		t.Fatalf("/copy dialog has %d fields, want two path fields", len(dlg.Fields))
	}
	if widget.PanelsVisible() {
		t.Fatal("/copy unexpectedly opened blue panels")
	}
	dlg.Fields[0].Input.SetValue("source.txt")
	dlg.Fields[1].Input.SetValue("destination")
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	copied := filepath.Join(destDir, "source.txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(copied); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(copied); err != nil {
		t.Fatalf("copy did not create %q: %v", copied, err)
	}
}

func TestPathInputCompletesRelativeToShellDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	if err := widget.Shell().SetDir(dir); err != nil {
		t.Fatal(err)
	}

	pathInput := widget.newPathInput("sou")
	deadline := time.Now().Add(2 * time.Second)
	for pathInput.Value() != "source.txt" && time.Now().Before(deadline) {
		pathInput.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
		time.Sleep(time.Millisecond)
	}
	if got := pathInput.Value(); got != "source.txt" {
		t.Fatalf("path completion = %q, want %q", got, "source.txt")
	}
}

func TestFileCollisionKeepsProgressDialogAndResumes(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "source")
	dstDir := filepath.Join(root, "destination")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(srcDir, "same.txt")
	target := filepath.Join(dstDir, "same.txt")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	widget := NewWidget(AppWidgetOptions{WorkingDir: root})
	defer widget.Close()
	widget.startFileOp(filemanager.FileOp{Kind: filemanager.OpCopy, Sources: []string{source}, Dest: dstDir})
	progress := widget.Dialog()
	if progress == nil {
		t.Fatal("file operation did not open a progress dialog")
	}

	deadline := time.Now().Add(2 * time.Second)
	for !progress.Collision && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if widget.Dialog() != progress || !progress.Collision {
		t.Fatal("collision replaced the progress dialog instead of expanding it")
	}
	widget.HandleEvent(input.KeyPressEvent{Code: 's', Text: "s"})
	deadline = time.Now().Add(2 * time.Second)
	for widget.Dialog() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if widget.Dialog() != nil {
		t.Fatal("file operation did not resume and finish after Skip")
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "old" {
		t.Fatalf("collision skip changed destination to %q", contents)
	}
}

func TestSlashCommandEnterRunsPrefixWithFileArguments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	widget := NewWidget(AppWidgetOptions{WorkingDir: dir})
	defer widget.Close()
	widget.Shell().SetInput("/edi file.txt")

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if widget.Editor() == nil {
		t.Fatal("Enter did not run the selected /edit command with its path argument")
	}
	if got := widget.Shell().Input().Value(); got != "" {
		t.Fatalf("input after running /edit = %q, want empty", got)
	}
}

func TestSlashSettingsOpensDialogAndEscapeShowsPanels(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()

	widget.Shell().SetInput("/set")
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if widget.Dialog() == nil {
		t.Fatal("/settings did not open a dialog")
	}

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	widget.Shell().Input().Reset()
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	if !widget.PanelsVisible() {
		t.Fatal("Escape with empty input and hidden panels did not show the panels")
	}
}
