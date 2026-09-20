package ui

import "github.com/mavity/rusticated/kabibi/ui/terminal"

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

// KeyModifier is a bitmask of modifier keys held during a key event.
type KeyModifier uint8

const (
	ModShift KeyModifier = 1 << iota
	ModAlt
	ModCtrl
)

// KeyEvent carries a decoded keyboard event.
type KeyEvent struct {
	Rune      rune
	Key       int
	Modifiers KeyModifier
}

// MouseAction classifies the kind of pointer event.
type MouseAction uint8

const (
	MousePress MouseAction = iota
	MouseRelease
	MouseMotion
	MouseWheel
)

// MouseEvent carries a decoded pointer event with screen coordinates.
type MouseEvent struct {
	X, Y      int
	Button    uint8
	Action    MouseAction
	Modifiers KeyModifier
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
type Widget interface {
	Measure(c Constraints) Size
	Render(r terminal.Rect, ctx RenderContext) (terminal.CellBuf, CursorPos)
	HandleKey(e KeyEvent) bool
	HandleMouse(e MouseEvent) bool
}
