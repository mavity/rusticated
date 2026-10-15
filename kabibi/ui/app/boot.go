package app

import (
	"github.com/mavity/rusticated/kabibi/ui"
)

func Boot() (*AppWidget, *ui.Host, error) {
	var h *ui.Host
	writeToScrollbackDelegate := func(text string) {
		if h != nil {
			h.WriteToScrollback(text)
		}
	}

	app := NewWidget(AppWidgetOptions{
		WriteToScrollback: writeToScrollbackDelegate,
		OnExit: func() {
			if h != nil {
				h.Stop()
			}
		},
	})

	// Create Host with Shell as root widget
	h = ui.NewHost(app)

	return app, h, nil
}
