package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
)

func TestSlashCommandHintsAndUniqueCompletion(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()

	widget.Shell().SetInput("/set")
	buf, _ := widget.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{})
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
	if !widget.PanelsVisible() {
		t.Fatal("Left then Enter did not select and run /move")
	}
	if got := widget.Shell().Input().Value(); got != "" {
		t.Fatalf("input after running /move = %q, want empty", got)
	}
}

func TestSlashCommandTabCyclesMultipleMatches(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{})
	defer widget.Close()
	widget.Shell().SetInput("/m")

	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab})
	if got := widget.Shell().Input().Value(); got != "/m" {
		t.Fatalf("input after cycling matches = %q, want /m", got)
	}
	widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if !widget.PanelsVisible() {
		t.Fatal("Tab then Enter did not run the selected /move command")
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
