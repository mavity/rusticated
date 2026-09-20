//go:build !windows

package ui

import (
	"os"
	"os/signal"
	"syscall"
)

// initSignalHandlers sets up SIGWINCH and SIGTERM listeners for Unix platforms.
func initSignalHandlers(h *Host) func() {
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)

	termCh := make(chan os.Signal, 1)
	signal.Notify(termCh, syscall.SIGTERM)

	go func() {
		for {
			select {
			case <-winchCh:
				h.Invalidate()
			case <-termCh:
				h.Stop()
			case <-h.stopCh:
				return
			}
		}
	}()

	return func() {
		signal.Stop(winchCh)
		signal.Stop(termCh)
	}
}
