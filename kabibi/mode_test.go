package main

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
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
			listItems := make([]list.Item, len(tt.items))
			for i, item := range tt.items {
				listItems[i] = item
			}

			l := list.New(listItems, list.NewDefaultDelegate(), 80, 10)
			got := selectedNames(&l)

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

		items := []list.Item{
			fileItem{name: "marked1", selected: true},
			fileItem{name: "marked2", selected: true},
			fileItem{name: "highlight", selected: false},
		}
		m.leftList.SetItems(items)
		m.leftDir = "/tmp"

		sources := m.opSources()
		if len(sources) != 2 {
			t.Errorf("opSources with 2 marked items returned %d sources", len(sources))
		}
	})

	t.Run("no marks falls back to highlight", func(t *testing.T) {
		m := initialModel()
		m.activePane = leftPane

		items := []list.Item{
			fileItem{name: "file1", selected: false},
			fileItem{name: "highlight", selected: false},
		}
		m.leftList.SetItems(items)
		m.leftList.Select(1)
		m.leftDir = "/tmp"

		sources := m.opSources()
		if len(sources) != 1 {
			t.Errorf("opSources with no marks returned %d sources, want 1", len(sources))
		}
	})

	t.Run("skips parent directory", func(t *testing.T) {
		m := initialModel()
		m.activePane = leftPane

		items := []list.Item{
			fileItem{name: "..", isDir: true, selected: false},
		}
		m.leftList.SetItems(items)
		m.leftList.Select(0)
		m.leftDir = "/tmp"

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
