//go:build windows

package ui

// initSignalHandlers is a no-op on Windows; the console host manages resize events.
func initSignalHandlers(h *Host) func() {
	return func() {}
}
