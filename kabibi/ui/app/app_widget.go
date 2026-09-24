package app

import (
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

// AppWidget is the pure root UI state node implementing ui.Widget.
type AppWidget struct {
	sh              *shell.Shell
	writeScrollback func(string)
}

// AppWidgetOptions contains initial parameters and service bindings for AppWidget.
type AppWidgetOptions struct {
	WriteToScrollback func(string)
}

// NewWidget constructs a clean AppWidget instance.
func NewWidget(opts AppWidgetOptions) *AppWidget {
	w := &AppWidget{
		writeScrollback: opts.WriteToScrollback,
	}

	// Initialize child shell widget with a callback delegating to writeScrollback[cite: 1]
	w.sh = shell.New(shell.ShellOptions{
		WriteToScrollback: func(text string) {
			if w.writeScrollback != nil {
				w.writeScrollback(text)
			}
		},
	})

	return w
}

// SetScrollbackWriter binds or updates the scrollback callback post-construction.
func (w *AppWidget) SetScrollbackWriter(fn func(string)) {
	w.writeScrollback = fn
}

// ── ui.Widget Interface Implementation ────────────────────────────────────────

func (w *AppWidget) Measure(c ui.Constraints) ui.Size {
	return w.sh.Measure(c)
}

func (w *AppWidget) Render(r terminal.Rect, ctx ui.RenderContext) (terminal.CellBuf, ui.CursorPos) {
	return w.sh.Render(r, ctx)
}

func (w *AppWidget) HandleEvent(e input.Event) bool {
	return w.sh.HandleEvent(e)
}
