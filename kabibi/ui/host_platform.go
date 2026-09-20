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
// Uses golang.org/x/term.MakeRaw for cross-platform compatibility across
// Linux, macOS, BSD, and Windows.
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
// Works on all platforms by delegating to golang.org/x/term.Restore.
func (h *Host) exitRawMode() {
	h.out.WriteString("\x1b[?1006l\x1b[?1000l")
	h.out.Flush()
	if h.platform.prev != nil {
		_ = xterm.Restore(os.Stdin.Fd(), h.platform.prev)
		h.platform.prev = nil
	}
}

// querySize returns the current terminal dimensions in columns and rows.
// Uses golang.org/x/term.GetSize for cross-platform TIOCGWINSZ handling (Unix)
// and Windows ConPTY query support.
func (h *Host) querySize() (w, ht int) {
	w, ht, _ = xterm.GetSize(os.Stdout.Fd())
	return
}

// handleSignals installs platform-specific signal handlers.
// On Unix platforms, this sets up SIGWINCH (window resize) and SIGTERM (graceful stop).
// On Windows, this is a no-op; the console host manages resize events.
// Implementation is delegated to platform-specific files via initSignalHandlers.
func (h *Host) handleSignals() (cleanup func()) {
	return initSignalHandlers(h)
}
