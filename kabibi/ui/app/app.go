package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

// App is the process supervisor that owns the terminal lifecycle, signal traps,
// and the double-buffered UI engine. It resolves the circular dependency between
// Host and Shell via closure-captured host pointer binding.
type App struct {
	host  *ui.Host
	shell *shell.Shell
}

// New creates a new application supervisor with properly wired Host and Shell.
// The circular dependency is resolved by:
// 1. Creating Shell with a callback that captures the host pointer (initially nil)
// 2. Creating Host with Shell as root
// 3. Assigning the host pointer so the callback becomes live
func New() *App {
	// This pointer will be assigned after Host is created, resolving circular wiring
	var hostPtr *ui.Host

	// Create Shell with a callback that captures hostPtr via closure
	sh := shell.New(shell.ShellOptions{
		WriteToScrollback: func(text string) {
			if hostPtr != nil {
				hostPtr.WriteToScrollback(text)
			}
		},
	})

	// Create Host with Shell as root widget
	h := ui.NewHost(sh)

	// NOW assign the host pointer so callbacks become live
	hostPtr = h

	return &App{
		host:  h,
		shell: sh,
	}
}

// Run executes the application lifecycle:
// 1. Runs cooked-mode startup sequence (terminal height queries, newline injection)
// 2. Enters raw mode and starts the double-buffered event loop
// 3. Returns when user exits or error occurs
func (a *App) Run() error {
	// Cooked-mode startup: query terminal height and emit H-1 newlines to push
	// the shell prompt into native scrollback without requiring a clear screen.
	if err := a.startupSequence(); err != nil {
		return fmt.Errorf("startup sequence failed: %w", err)
	}

	// Enter raw mode and run the double-buffered UI engine
	return a.host.Run()
}

// startupSequence performs the cooked-mode viewport shift before entering raw mode.
// Queries terminal height H and emits H-1 newlines to os.Stdout to shift the
// shell prompt into the terminal's native scrollback history.
func (a *App) startupSequence() error {
	// Query terminal size from stdout
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
