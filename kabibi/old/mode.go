package app

import "path/filepath"

// uiMode selects which top-level surface handles input and rendering.
type uiMode int

const (
	modeBrowser uiMode = iota
	modeEditor
	modeDialog
)

// browserPaneFocus resolves the currently selected browser pane. The app-level
// field remains a compatibility mirror for input routing, but the dual-pane
// widget is the owner of the actual left/right selection. When the app field is
// stale or unset, we fall back to the widget-owned state.
func (m *AppWidget) browserPaneFocus() pane {
	if m == nil {
		return leftPane
	}
	switch m.activePane {
	case leftPane, rightPane:
		return m.activePane
	default:
		if m.dualPane != nil && m.dualPane.ActivePaneIndex() == 1 {
			return rightPane
		}
		return leftPane
	}
}

// activePaneState returns the widget and directory for the currently focused file pane.
// It returns nil when the chat pane is active.
func (m *AppWidget) activePaneState() (*FilePaneWidget, string, pane) {
	if m == nil || m.dualPane == nil {
		return nil, "", m.activePane
	}
	if m.activePane == chatPane {
		return nil, "", chatPane
	}
	paneState := m.browserPaneFocus()
	if paneState == leftPane {
		w := m.dualPane.Left
		if w == nil {
			return nil, "", leftPane
		}
		return w, w.Dir(), leftPane
	}
	w := m.dualPane.Right
	if w == nil {
		return nil, "", rightPane
	}
	return w, w.Dir(), rightPane
}

// otherPaneDir returns the directory shown in the pane that is not active,
// used as the default destination for copy/move operations.
func (m *AppWidget) otherPaneDir() string {
	if m == nil || m.dualPane == nil {
		return ""
	}
	if m.browserPaneFocus() == leftPane {
		return m.dualPane.Right.Dir()
	}
	return m.dualPane.Left.Dir()
}

// selectedNames returns the marked file names from a slice (excluding "..").
func selectedNames(items []fileItem) []string {
	var names []string
	for _, fi := range items {
		if fi.selected && fi.name != ".." {
			names = append(names, fi.name)
		}
	}
	return names
}

// opSources resolves the set of source paths for a file operation in the active
// pane: the marked items if any, otherwise the highlighted item. ".." is skipped.
func (m *AppWidget) opSources() []string {
	w, dir, _ := m.activePaneState()
	if w == nil {
		return nil
	}
	marked := selectedNames(w.Items())
	if len(marked) > 0 {
		out := make([]string, 0, len(marked))
		for _, n := range marked {
			out = append(out, filepath.Join(dir, n))
		}
		return out
	}
	if fi, ok := w.SelectedItem(); ok && fi.name != ".." {
		return []string{filepath.Join(dir, fi.name)}
	}
	return nil
}
