package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/dialog"
)

func TestListFilesSearchShortcut(t *testing.T) {
	tests := []struct {
		name string
		key  input.KeyPressEvent
		want bool
	}{
		{name: "ctrl-f", key: input.KeyPressEvent{Code: 'f', Mod: input.ModCtrl}, want: true},
		{name: "cmd-f", key: input.KeyPressEvent{Code: 'f', Mod: input.ModMeta}, want: true},
		{name: "alt-f7", key: input.KeyPressEvent{Code: input.KeyF7, Mod: input.ModAlt}, want: true},
		{name: "mod-f7 ctrl", key: input.KeyPressEvent{Code: input.KeyF7, Mod: input.ModCtrl}, want: true},
		{name: "mod-f7 meta", key: input.KeyPressEvent{Code: input.KeyF7, Mod: input.ModMeta}, want: true},
		{name: "plain-f7", key: input.KeyPressEvent{Code: input.KeyF7}, want: false},
		{name: "plain-f", key: input.KeyPressEvent{Code: 'f'}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isListFilesSearchShortcut(tt.key); got != tt.want {
				t.Fatalf("isListFilesSearchShortcut(%v) = %t, want %t", tt.key, got, tt.want)
			}
		})
	}
}

func TestFindFilesCommandQuotesArguments(t *testing.T) {
	command := findFilesCommand("**/*.go", "what's in src?", 12)
	if !strings.Contains(command, "--find-paths '**/*.go'") {
		t.Fatalf("path filter was not included: %q", command)
	}
	if !strings.Contains(command, "--find-content 'what'\\''s in src?' --max-results 12") {
		t.Fatalf("command options = %q", command)
	}
}

func TestListFilesSearchShortcutOpensParameterDialog(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir(), PanelsVisible: true})
	defer widget.Close()
	widget.Shell().SetInput("/help")

	if !widget.HandleEvent(input.KeyPressEvent{Code: 'f', Mod: input.ModCtrl}) {
		t.Fatal("Ctrl+F was not handled")
	}
	widget.mu.Lock()
	dlg := widget.dialog
	widget.mu.Unlock()
	if dlg == nil {
		t.Fatal("Ctrl+F did not open the find-files dialog")
	}
	if dlg.Action != dialog.ActionListFiles {
		t.Fatalf("dialog action = %v, want ActionListFiles", dlg.Action)
	}
	if len(dlg.Fields) != 3 {
		t.Fatalf("dialog has %d fields, want path-filter, content-query, and result-limit", len(dlg.Fields))
	}
}

func TestPlainF7StillOpensMkdirDialog(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir(), PanelsVisible: true})
	defer widget.Close()

	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyF7}) {
		t.Fatal("F7 was not handled")
	}
	widget.mu.Lock()
	dlg := widget.dialog
	widget.mu.Unlock()
	if dlg == nil || dlg.Action != dialog.ActionMkdir {
		t.Fatalf("F7 dialog = %#v, want ActionMkdir", dlg)
	}
}

func TestCtrlSpaceCompletesWithPanelsVisibleAndTabCycles(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir(), PanelsVisible: true})
	defer widget.Close()
	widget.Shell().SetInput("git st")

	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeySpace, Mod: input.ModCtrl}) {
		t.Fatal("Ctrl+Space was not handled")
	}
	if !widget.Shell().Input().CompletionActive() {
		t.Fatal("Ctrl+Space did not activate explicit completion")
	}
	deadline := time.Now().Add(time.Second)
	for !widget.Shell().Input().CompletionMenuVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !widget.Shell().Input().CompletionMenuVisible() {
		t.Fatal("explicit completion did not produce multiple candidates")
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab}) {
		t.Fatal("Tab did not cycle active completion while panels were visible")
	}
	if got, want := widget.Shell().Input().Value(), "git status "; got != want {
		t.Fatalf("first inline preview = %q, want %q", got, want)
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
		t.Fatal("Escape did not exit active completion")
	}
	if got, want := widget.Shell().Input().Value(), "git st"; got != want {
		t.Fatalf("Escape-restored input = %q, want %q", got, want)
	}
}

func TestEscapeDismissesCompletionBeforeClearingInput(t *testing.T) {
	for _, panelsVisible := range []bool{false, true} {
		widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir(), PanelsVisible: panelsVisible})
		widget.Shell().SetInput("git st")
		if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeySpace, Mod: input.ModCtrl}) {
			_ = widget.Close()
			t.Fatal("Ctrl+Space was not handled")
		}

		deadline := time.Now().Add(time.Second)
		for !widget.Shell().Input().CompletionMenuVisible() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !widget.Shell().Input().CompletionMenuVisible() {
			_ = widget.Close()
			t.Fatalf("completion menu did not appear with panels visible=%t", panelsVisible)
		}
		if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyTab}) {
			_ = widget.Close()
			t.Fatal("Tab did not preview the first completion")
		}
		if got, want := widget.Shell().Input().Value(), "git status "; got != want {
			_ = widget.Close()
			t.Fatalf("inline preview = %q, want %q", got, want)
		}
		if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
			_ = widget.Close()
			t.Fatal("Escape did not dismiss completion")
		}
		if got, want := widget.Shell().Input().Value(), "git st"; got != want {
			_ = widget.Close()
			t.Fatalf("completion Escape input = %q, want %q", got, want)
		}
		if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
			_ = widget.Close()
			t.Fatal("Escape without completion did not clear input")
		}
		if got := widget.Shell().Input().Value(); got != "" {
			_ = widget.Close()
			t.Fatalf("second Escape left input %q, want empty", got)
		}
		if err := widget.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEscapeDismissesSpeculativeCompletionBeforeClearingInput(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir()})
	defer widget.Close()
	widget.Shell().SetInput("git st")

	deadline := time.Now().Add(2 * time.Second)
	for !widget.Shell().Input().CompletionVisible() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !widget.Shell().Input().CompletionVisible() {
		t.Fatal("speculative completion did not appear")
	}
	if widget.Shell().Input().CompletionActive() {
		t.Fatal("speculative completion became active without an explicit request")
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
		t.Fatal("Escape did not dismiss speculative completion")
	}
	if got, want := widget.Shell().Input().Value(), "git st"; got != want {
		t.Fatalf("speculative Escape input = %q, want %q", got, want)
	}
	if widget.Shell().Input().CompletionVisible() {
		t.Fatal("speculative completion remained visible after Escape")
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
		t.Fatal("Escape without completion did not clear input")
	}
	if got := widget.Shell().Input().Value(); got != "" {
		t.Fatalf("Escape without completion left input %q, want empty", got)
	}
}
