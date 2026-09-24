package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

// StartupSequence performs the cooked-mode viewport clear before entering raw mode.
// It queries the terminal height H and emits H-1 newlines to os.Stdout to shift
// the shell prompt into the terminal's native scrollback history without requiring
// a full screen clear escape sequence.
//
// This function should be called before entering raw mode (before tea.Program.Run()
// or Host.Run()). It is idempotent and safe to call multiple times.
func StartupSequence() error {
	// Query terminal height
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err != nil || h <= 0 {
		// Fall back to default height on error
		h = 24
	}
	if w <= 0 {
		w = 80
	}

	// Emit H-1 newlines to push shell lines up into native scrollback
	if h > 1 {
		payload := strings.Repeat("\n", h-1)
		_, err := os.Stdout.WriteString(payload)
		if err != nil {
			return fmt.Errorf("startup sequence write failed: %w", err)
		}
		// Flush stdout to force the terminal emulator to scroll
		os.Stdout.Sync()
	}

	return nil
}
