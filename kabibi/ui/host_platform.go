package ui

import (
	"os"

	xterm "github.com/charmbracelet/x/term"
)

// platformState holds the saved terminal state needed to restore cooked mode on exit.
type platformState struct {
	prev *xterm.State
}

// enterRawMode puts stdin into raw mode, capturing the previous state so that
// exitRawMode can restore it. Uses charmbracelet/x/term which delegates to the
// OS-native termios / console API per platform.
func (h *Host) enterRawMode() error {
	state, err := xterm.MakeRaw(os.Stdin.Fd())
	if err != nil {
		return err
	}
	h.platform.prev = state
	return nil
}

// exitRawMode restores the terminal to the state captured by enterRawMode.
func (h *Host) exitRawMode() {
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
