package ui

import (
	"fmt"
	"log"
	"strings"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// Re-export terminal geometry types so sub-packages and tests can reference
// them without a terminal. qualifier.
type (
	CellBuf   = terminal.CellBuf
	Rect      = terminal.Rect
	CursorPos = terminal.CursorPos
)

// Re-export constructors that callers commonly need.
var NewCellBuf = terminal.NewCellBuf

// Constraints define the maximum available dimensions a widget may use.
type Constraints struct{ MaxW, MaxH int }

// Size defines the measured dimensions of a widget.
type Size struct{ W, H int }

// InvalidateFunc is called by a widget to request a host redraw.
type InvalidateFunc func()

// RenderContext is threaded top-down through the widget tree during Render.
type RenderContext struct {
	Focused    bool
	Invalidate InvalidateFunc
	OnError    func(error)
}

// ForChild returns context for a child whose focus is constrained by its parent.
func (c RenderContext) ForChild(focused bool) RenderContext {
	c.Focused = c.Focused && focused
	return c
}

// CompositeANSI interprets one child's ANSI output in an isolated terminal,
// then composites its exact allocation into dst using reset-color fallback.
func (c RenderContext) CompositeANSI(
	dst *CellBuf,
	term *terminal.Terminal,
	rect terminal.Rect,
	ansiText string,
) CursorPos {
	if dst == nil || term == nil || rect.W <= 0 || rect.H <= 0 {
		return CursorPos{}
	}
	child, cursor, _, err := term.RenderANSIForComposition(ansiText, rect.W, rect.H)
	if err != nil {
		if c.OnError != nil {
			c.OnError(err)
		} else {
			log.Printf("render child ANSI: %v", err)
		}
		return CursorPos{}
	}
	dst.BlitWithResetFallback(child, rect.X, rect.Y)
	if !c.Focused || !cursor.Visible {
		return CursorPos{}
	}
	cursor.X += rect.X
	cursor.Y += rect.Y
	return cursor
}

// ANSIFrame serializes an internally composed cell buffer and restores the
// selected cursor position as the final terminal state.
func ANSIFrame(buf CellBuf, cursor CursorPos) string {
	var out strings.Builder
	for y := 0; y < buf.Height; y++ {
		fmt.Fprintf(&out, "\x1b[%d;1H\x1b[0m", y+1)
		row := terminal.NewCellBuf(buf.Width+1, 1)
		for x := 0; x < buf.Width; x++ {
			row.Set(x, 0, buf.Get(x, y))
		}
		out.WriteString(terminal.ToANSI(row))
	}
	if cursor.Visible {
		fmt.Fprintf(&out, "\x1b[?25h\x1b[%d;%dH", cursor.Y+1, cursor.X+1)
	} else {
		out.WriteString("\x1b[?25l")
	}
	return out.String()
}

// Widget is the core layout and rendering contract.
//
// Measure declares desired space under constraints; parents choose the final
// allocation. Render returns ANSI text for that allocation. Focus and redraw
// invalidation are supplied through RenderContext. HandleEvent returns true
// when the event is consumed and should not bubble further.
//
// HandleEvent receives canonical x/input.Event types (KeyPressEvent, MouseClickEvent,
// PasteEvent, FocusEvent, WindowSizeEvent, and all other x/input protocols) without
// adapter boilerplate. Widgets explicitly invoke ctx.Invalidate() only when internal
// state actually mutates; the host does not auto-invalidate on event consumption.
type Widget interface {
	Measure(c Constraints) Size
	Render(r terminal.Rect, ctx RenderContext) string
	HandleEvent(e input.Event) bool
}
