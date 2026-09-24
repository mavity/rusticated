package shell

import (
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// Type aliases to provide shorthand access to ui types within the shell package.
// These allow shell widgets to use Rect, Size, Constraints, etc. without
// qualifying them with ui.

type (
	// Geometry and layout types
	Rect        = ui.Rect
	Size        = ui.Size
	Constraints = ui.Constraints
	CursorPos   = ui.CursorPos

	// Rendering types
	CellBuf = terminal.CellBuf
	Cell    = terminal.Cell

	// Widget interface
	Widget = ui.Widget

	// Event types
	InputEvent = input.Event
)

// Convenience constructors
var (
	NewCellBuf = ui.NewCellBuf
)

// Local widget interface for input widgets with additional methods
type InputWidget interface {
	Widget
	Value() string
	SetValue(s string)
	Reset()
	Focus()
	Blur()
	Focused() bool
}
