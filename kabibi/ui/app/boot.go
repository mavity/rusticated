package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/mavity/rusticated/kabibi/ui"
)

func Boot() (*AppWidget, *ui.Host, error) {

	err := pageUpInit(term.GetSize)
	if err != nil {
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
	})

	// Create Host with Shell as root widget
	h = ui.NewHost(app)

	return app, h, nil
}

func pageUpInit(
	getSizeFunc func(fd uintptr) (w int, ht int, err error),
) error {
	// Query terminal size from stdout
	w, h, err := getSizeFunc(os.Stdout.Fd())
	if err != nil {
		return err
	}

	if h <= 0 || w <= 0 {
		return fmt.Errorf("invalid terminal size: width=%d, height=%d (possibly not a terminal)", w, h)
	}

	payload := "\r" + strings.Repeat("\n", h-1)
	_, errWrite := os.Stdout.WriteString(payload)
	if errWrite != nil {
		return errWrite
	}

	return nil
}
