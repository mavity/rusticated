package main

import (
	"io"
)

// PtySession represents an active pseudo-terminal session.
// It implements io.ReadWriteCloser for duplex terminal I/O
// and provides a Resize method for updating terminal dimensions.
type PtySession interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}
