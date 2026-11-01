package app

import (
	"strings"
	"testing"

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

func TestListFilesCommandQuotesQuery(t *testing.T) {
	command := listFilesCommand("what's in src?", true, 12)
	if !strings.Contains(command, "--query 'what'\\''s in src?'") {
		t.Fatalf("query was not shell-quoted: %q", command)
	}
	if !strings.Contains(command, "--search-content=true --max-results 12") {
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
		t.Fatal("Ctrl+F did not open the list-files dialog")
	}
	if dlg.Action != dialog.ActionListFiles {
		t.Fatalf("dialog action = %v, want ActionListFiles", dlg.Action)
	}
	if len(dlg.Fields) != 3 {
		t.Fatalf("dialog has %d fields, want query, content-search, and result-limit", len(dlg.Fields))
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
