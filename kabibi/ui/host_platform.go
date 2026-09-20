//go:build windows

package ui

import (
	"os"

	xterm "github.com/charmbracelet/x/term"
)

// platformState holds the saved terminal state needed to restore cooked mode on exit.
type platformState struct {
	prev *xterm.State
}

// enterRawMode puts stdin into raw mode and enables SGR mouse tracking.
func (h *Host) enterRawMode() error {
	state, err := xterm.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return err
	}
	h.platform.prev = state
	h.out.WriteString("\x1b[?1000h\x1b[?1006h")
	h.out.Flush()
	return nil
}

// exitRawMode disables mouse tracking and restores the terminal to cooked mode.
func (h *Host) exitRawMode() {
	h.out.WriteString("\x1b[?1006l\x1b[?1000l")
	h.out.Flush()
	if h.platform.prev != nil {
		_ = xterm.Restore(os.Stdin.Fd(), h.platform.prev)
		h.platform.prev = nil
	}
}

// querySize returns the current terminal dimensions in columns and rows.
func (h *Host) querySize() (w, ht int) {
	w, ht, _ = xterm.GetSize(os.Stdout.Fd())
	return
}

// handleSignals is a no-op on Windows; the console host manages resize events.
func (h *Host) handleSignals() (cleanup func()) {
	return func() {}
}
