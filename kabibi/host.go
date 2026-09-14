package main

import tea "github.com/charmbracelet/bubbletea"

// AppHost is the thin Bubble Tea adapter. It owns the app state and routes
// messages into the application widget, but it does not contain the app's
// file-browser logic itself.
type AppHost struct {
	app *AppWidget
}

func (h *AppHost) Init() tea.Cmd {
	// Keep normal terminal scrollback and do not capture mouse wheel/scroll events.
	// Those events compete with the user's terminal scroll and cause the app to repaint
	// itself in a way that fights the existing buffer history.
	return nil
}

func (h *AppHost) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return h, h.app.dispatch(msg)
}

func (h *AppHost) View() string {
	return h.app.viewFrame()
}
