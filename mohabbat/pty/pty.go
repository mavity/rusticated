package pty

import "io"

// PTY represents the master side of a pseudo-terminal session.
// It implements io.ReadWriteCloser for bidirectional terminal I/O
// and provides a Resize method for adjusting terminal dimensions.
type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() error
	Fd() uintptr
}

// Options configure the initial state of the spawned pseudo-terminal.
type Options struct {
	Cols int    // Initial width in columns (default: 80)
	Rows int    // Initial height in rows (default: 24)
	Term string // Value for TERM environment variable (e.g. "xterm-256color")
}
