package ui

import (
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// Re-export terminal geometry types so sub-packages and tests can reference
// them without a terminal. qualifier.
type (
	CellBuf = terminal.CellBuf
	Rect    = terminal.Rect
)

// Re-export constructors that callers commonly need.
var NewCellBuf = terminal.NewCellBuf

// Constraints define the maximum available dimensions a widget may use.
type Constraints struct{ MaxW, MaxH int }

// Size defines the measured dimensions of a widget.
type Size struct{ W, H int }

// CursorPos describes a hardware cursor location relative to a widget's origin.
type CursorPos struct {
	X, Y    int
	Visible bool
}

// InvalidateFunc is called by a widget to request a host redraw.
type InvalidateFunc func()

// RenderContext is threaded top-down through the widget tree during Render.
type RenderContext struct {
	Focused    bool
	Invalidate InvalidateFunc
}

// Widget is the core layout and rendering contract.
//
// Measure declares space requirements given constraints. Render paints into a
// fresh CellBuf sized to the supplied Rect and returns a cursor position
// relative to the widget's local origin. HandleKey and HandleMouse return true
// when the event is consumed and should not bubble further.
//
// Event types are canonical x/input primitives, eliminating translation layers
// and ensuring full protocol depth (Kitty keyboard, bracketed paste, focus events,
// advanced pointer tracking) is available without boilerplate.
type Widget interface {
	Measure(c Constraints) Size
	Render(r terminal.Rect, ctx RenderContext) (terminal.CellBuf, CursorPos)
	HandleKey(e input.KeyPressEvent) bool
	HandleMouse(e input.MouseEvent) bool
}
