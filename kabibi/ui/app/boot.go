package app

import (
	"github.com/mavity/rusticated/kabibi/browser"
	"github.com/mavity/rusticated/kabibi/ui"
)

func Boot(verbose bool) (*AppWidget, *ui.Host, error) {
	if err := browser.Install(); err != nil {
		return nil, nil, err
	}

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
	h = ui.NewHost(app, verbose)

	return app, h, nil
}
