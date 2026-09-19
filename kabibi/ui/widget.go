package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// Constraints define the maximum available dimensions a widget may use.
type Constraints struct {
	MaxW int
	MaxH int
}

// Size defines the measured dimensions of a widget.
type Size struct {
	W int
	H int
}

// Rect describes a widget's bounds within a parent surface.
// It is intentionally identical to the cell engine's Rect and is kept here
// for the widget API surface to avoid depending on a separate geometry package.
type Widget interface {
	Measure(Constraints) Size
	Layout(terminal.Rect)
	Render() terminal.CellBuf
}

// InputWidget extends Widget with direct event routing support.
type InputWidget interface {
	Widget
	HandleKey(tea.KeyMsg) tea.Cmd
	HandleMouse(tea.MouseMsg) tea.Cmd
}

// InvalidateFunc is used by child widgets to signal a dirty region to parents.
type InvalidateFunc func()
