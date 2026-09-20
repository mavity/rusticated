//go:build !windows

package ui

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// platformState holds the termios snapshot taken before raw mode was applied.
type platformState struct {
	savedTermios syscall.Termios
	saved        bool
}

// enterRawMode configures stdin for byte-at-a-time raw I/O using native
// termios syscalls and enables SGR 1006 extended mouse tracking.
func (h *Host) enterRawMode() error {
	var t syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(os.Stdin.Fd()),
		ioctlGetTermios,
		uintptr(unsafe.Pointer(&t))); errno != 0 {
		return errno
	}
	h.platform.savedTermios = t
	h.platform.saved = true

	// cfmakeraw: disable line buffering, echo, signal generation, and output processing.
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK |
		syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(os.Stdin.Fd()),
		ioctlSetTermios,
		uintptr(unsafe.Pointer(&t))); errno != 0 {
		return errno
	}

	h.out.WriteString("\x1b[?1000h\x1b[?1006h")
	h.out.Flush()
	return nil
}

// exitRawMode disables mouse tracking and restores the saved termios state.
func (h *Host) exitRawMode() {
	h.out.WriteString("\x1b[?1006l\x1b[?1000l")
	h.out.Flush()
	if !h.platform.saved {
		return
	}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(os.Stdin.Fd()),
		ioctlSetTermios,
		uintptr(unsafe.Pointer(&h.platform.savedTermios)))
	h.platform.saved = false
}

// querySize returns terminal dimensions via TIOCGWINSZ ioctl; falls back to 80×24.
func (h *Host) querySize() (w, ht int) {
	var ws syscall.Winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(os.Stdout.Fd()),
		syscall.TIOCGWINSZ,
		uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// handleSignals installs SIGWINCH (window resize) and SIGTERM (graceful stop)
// listeners, returning a cleanup function that deregisters both channels.
func (h *Host) handleSignals() (cleanup func()) {
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
