package main

import (
	"path/filepath"

	"github.com/charmbracelet/bubbles/list"
)

// uiMode selects which top-level surface handles input and rendering.
type uiMode int

const (
	modeBrowser uiMode = iota
	modeEditor
	modeDialog
)

// activePaneState returns the list and directory for the currently focused file pane.
// It returns nil when the chat pane is active.
func (m *model) activePaneState() (*list.Model, string, pane) {
	switch m.activePane {
	case leftPane:
		return &m.leftList, m.leftDir, leftPane
	case rightPane:
		return &m.rightList, m.rightDir, rightPane
	default:
		return nil, "", m.activePane
	}
}

// otherPaneDir returns the directory shown in the pane that is not active,
// used as the default destination for copy/move operations.
func (m *model) otherPaneDir() string {
	if m.activePane == leftPane {
		return m.rightDir
	}
	return m.leftDir
}

// selectedNames returns the marked file names in a pane (excluding "..").
func selectedNames(l *list.Model) []string {
	var names []string
	for _, it := range l.Items() {
		if fi, ok := it.(fileItem); ok && fi.selected && fi.name != ".." {
			names = append(names, fi.name)
		}
	}
	return names
}

// opSources resolves the set of source paths for a file operation in the active
// pane: the marked items if any, otherwise the highlighted item. ".." is skipped.
func (m *model) opSources() []string {
	l, dir, p := m.activePaneState()
	if l == nil {
		return nil
	}
	marked := selectedNames(l)
	if len(marked) > 0 {
		out := make([]string, 0, len(marked))
		for _, n := range marked {
			out = append(out, filepath.Join(dir, n))
		}
		return out
	}
	if fi, ok := l.SelectedItem().(fileItem); ok && fi.name != ".." {
		_ = p
		return []string{filepath.Join(dir, fi.name)}
	}
	return nil
}
