package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectedNames(t *testing.T) {
	tests := []struct {
		name  string
		items []fileItem
		want  []string
	}{
		{
			name:  "empty list",
			items: []fileItem{},
			want:  []string{},
		},
		{
			name: "no selected items",
			items: []fileItem{
				{name: "file1", isDir: false, selected: false},
				{name: "file2", isDir: false, selected: false},
			},
			want: []string{},
		},
		{
			name: "one selected",
			items: []fileItem{
				{name: "file1", isDir: false, selected: true},
				{name: "file2", isDir: false, selected: false},
			},
			want: []string{"file1"},
		},
		{
			name: "multiple selected",
			items: []fileItem{
				{name: "file1", isDir: false, selected: true},
				{name: "file2", isDir: false, selected: true},
				{name: "file3", isDir: false, selected: false},
			},
			want: []string{"file1", "file2"},
		},
		{
			name: "skips parent directory",
			items: []fileItem{
				{name: "..", isDir: true, selected: true},
				{name: "file1", isDir: false, selected: true},
			},
			want: []string{"file1"},
		},
		{
			name: "mixed dirs and files",
			items: []fileItem{
				{name: "dir1", isDir: true, selected: true},
				{name: "file1", isDir: false, selected: true},
				{name: "dir2", isDir: true, selected: false},
			},
			want: []string{"dir1", "file1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectedNames(tt.items)

			if len(got) != len(tt.want) {
				t.Errorf("selectedNames returned %d items, want %d", len(got), len(tt.want))
				return
			}

			for i, name := range got {
				if name != tt.want[i] {
					t.Errorf("selectedNames[%d] = %q, want %q", i, name, tt.want[i])
				}
			}
		})
	}
}

func TestOpSources(t *testing.T) {
	t.Run("marked items win over highlight", func(t *testing.T) {
		m := initialModel()
		m.activePane = leftPane
		m.dualPane.Left.SetItems([]fileItem{
			{name: "marked1", selected: true},
			{name: "marked2", selected: true},
			{name: "highlight", selected: false},
		})
		m.dualPane.Left.SetDir("/tmp")

		sources := m.opSources()
		if len(sources) != 2 {
			t.Errorf("opSources with 2 marked items returned %d sources", len(sources))
		}
	})

	t.Run("no marks falls back to highlight", func(t *testing.T) {
		m := initialModel()
		m.activePane = leftPane
		m.dualPane.Left.SetItems([]fileItem{
			{name: "file1", selected: false},
			{name: "highlight", selected: false},
		})
		m.dualPane.Left.Select(1)
		m.dualPane.Left.SetDir("/tmp")

		sources := m.opSources()
		if len(sources) != 1 {
			t.Errorf("opSources with no marks returned %d sources, want 1", len(sources))
		}
	})

	t.Run("skips parent directory", func(t *testing.T) {
		m := initialModel()
		m.activePane = leftPane
		m.dualPane.Left.SetItems([]fileItem{
			{name: "..", isDir: true, selected: false},
		})
		m.dualPane.Left.Select(0)
		m.dualPane.Left.SetDir("/tmp")

		sources := m.opSources()
		if len(sources) != 0 {
			t.Errorf("opSources on '..' should return empty, got %d sources", len(sources))
		}
	})

	t.Run("chat pane returns nil", func(t *testing.T) {
		m := initialModel()
		m.activePane = chatPane

		sources := m.opSources()
		if sources != nil {
			t.Errorf("opSources on chatPane should return nil, got %v", sources)
		}
	})
}

func TestActivePaneStateAndOtherPaneDir(t *testing.T) {
	m := initialModel()
	m.dualPane.Left.SetDir("/tmp/left")
	m.dualPane.Right.SetDir("/tmp/right")

	m.activePane = leftPane
	w, dir, pane := m.activePaneState()
	if w == nil || pane != leftPane || dir != "/tmp/left" {
		t.Fatalf("activePaneState(left) = (%v, %q, %v), want non-nil, %q, leftPane", w, dir, pane, "/tmp/left")
	}
	if got := m.otherPaneDir(); got != "/tmp/right" {
		t.Fatalf("otherPaneDir(left) = %q, want %q", got, "/tmp/right")
	}

	m.activePane = rightPane
	w, dir, pane = m.activePaneState()
	if w == nil || pane != rightPane || dir != "/tmp/right" {
		t.Fatalf("activePaneState(right) = (%v, %q, %v), want non-nil, %q, rightPane", w, dir, pane, "/tmp/right")
	}
	if got := m.otherPaneDir(); got != "/tmp/left" {
		t.Fatalf("otherPaneDir(right) = %q, want %q", got, "/tmp/left")
	}

	m.activePane = chatPane
	w, dir, pane = m.activePaneState()
	if w != nil || dir != "" || pane != chatPane {
		t.Fatalf("activePaneState(chat) = (%v, %q, %v), want (nil, %q, %v)", w, dir, pane, "", chatPane)
	}
}

func TestMoveCursorHorizontalTracksPaneAndBounds(t *testing.T) {
	m := initialModel()
	m.width = 80
	m.height = 12
	m.activePane = leftPane

	items := make([]fileItem, 30)
	for i := range items {
		items[i] = fileItem{name: filepath.Join("file", string(rune('a'+(i%26))))}
	}
	m.dualPane.Left.SetItems(items)
	m.dualPane.Left.Select(0)

	m.moveCursorHorizontal(1)
	if got := m.dualPane.Left.SelectedIndex(); got != 5 {
		t.Fatalf("moveCursorHorizontal(+1) = %d, want 5 for height=12", got)
	}

	m.moveCursorHorizontal(-2)
	if got := m.dualPane.Left.SelectedIndex(); got != 0 {
		t.Fatalf("moveCursorHorizontal(-2) clamped to %d, want 0", got)
	}

	m.dualPane.Left.Select(len(items) - 1)
	m.moveCursorHorizontal(1)
	if got := m.dualPane.Left.SelectedIndex(); got != len(items)-1 {
		t.Fatalf("moveCursorHorizontal(+1) at end should stay at end; got %d, want %d", got, len(items)-1)
	}

	m.activePane = chatPane
	m.dualPane.Left.Select(3)
	m.moveCursorHorizontal(1)
	if got := m.dualPane.Left.SelectedIndex(); got != 3 {
		t.Fatalf("moveCursorHorizontal while chat pane is active should not move index; got %d, want 3", got)
	}
}

func TestFmToggleMarkMovesSelectionAndSkipsParent(t *testing.T) {
	m := initialModel()
	m.activePane = leftPane
	m.dualPane.Left.SetItems([]fileItem{
		{name: "..", isDir: true},
		{name: "alpha.txt"},
		{name: "beta.txt"},
	})
	m.dualPane.Left.Select(1)

	m.fmToggleMark()
	if got := m.dualPane.Left.SelectedIndex(); got != 2 {
		t.Fatalf("fmToggleMark moved selection to %d, want 2", got)
	}
	if fi := m.dualPane.Left.Items()[1]; !fi.selected {
		t.Fatalf("alpha.txt should be marked after fmToggleMark; got %#v", fi)
	}
	if fi := m.dualPane.Left.Items()[0]; fi.name != ".." || fi.selected {
		t.Fatalf(".. should remain unmarked; got %#v", fi)
	}

	m.dualPane.Left.Select(0)
	m.fmToggleMark()
	if got := m.dualPane.Left.SelectedIndex(); got != 1 {
		t.Fatalf("fmToggleMark on parent entry should advance selection to 1, got %d", got)
	}
	if fi := m.dualPane.Left.Items()[0]; fi.name != ".." || fi.selected {
		t.Fatalf(".. should still remain unmarked after skipped toggle; got %#v", fi)
	}
}

func TestFmEditOpensOnlyRealFiles(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "alpha.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	m := initialModel()
	m.activePane = leftPane
	m.loadDir(leftPane, dir, "alpha.txt")
	if cmd := m.fmEdit(); cmd != nil {
		t.Fatalf("fmEdit on a real file should return nil while setting editor state, got %v", cmd)
	}
	if m.mode != modeEditor {
		t.Fatalf("fmEdit should open the editor; mode = %v, want %v", m.mode, modeEditor)
	}
	if m.editor == nil {
		t.Fatal("fmEdit should populate the editor model")
	}

	m = initialModel()
	m.activePane = leftPane
	m.loadDir(leftPane, dir, "docs")
	if cmd := m.fmEdit(); cmd != nil {
		t.Fatalf("fmEdit on a directory should return nil, got %v", cmd)
	}
	if m.mode == modeEditor {
		t.Fatalf("fmEdit on a directory should not switch to editor mode")
	}
}
