package shell

import (
	"bytes"
	"io"
	"sync"
)

// SubprocessStreamWriter implements io.Writer for subprocess output ingestion.
// It accumulates bytes from subprocess stdout/stderr, tokenizes on line boundaries,
// and dispatches complete lines to registered callbacks for ANSI parsing and rendering.
type SubprocessStreamWriter struct {
	mu            sync.Mutex
	accumulator   *bytes.Buffer
	appendLines   func(lines []string) // Callback invoked with accumulated complete lines
	invalidate    func()               // Callback to trigger Host.Invalidate() after append
	viewportWidth int                  // Terminal width for FromANSI cell parsing
	onAppendError func(error)          // Optional error callback
}

// NewSubprocessStreamWriter creates a new stream writer for subprocess output.
// appendLines: callback fired when complete lines are accumulated and ready (should update PlumeWidget)
// invalidate: callback fired after line dispatch to trigger screen redraw
// viewportWidth: terminal width for ANSI cell parsing
func NewSubprocessStreamWriter(
	appendLines func(lines []string),
	invalidate func(),
	viewportWidth int,
) *SubprocessStreamWriter {
	if appendLines == nil {
		appendLines = func(lines []string) {} // no-op
	}
	if invalidate == nil {
		invalidate = func() {} // no-op
	}
	if viewportWidth <= 0 {
		viewportWidth = 80
	}
	return &SubprocessStreamWriter{
		accumulator:   &bytes.Buffer{},
		appendLines:   appendLines,
		invalidate:    invalidate,
		viewportWidth: viewportWidth,
	}
}

// Write implements io.Writer. It accumulates bytes and tokenizes on '\n' or '\r\n'.
// Complete lines are dispatched to the appendLines callback immediately.
func (w *SubprocessStreamWriter) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.accumulator.Write(p)
	n = len(p)

	// Tokenize accumulated bytes on line boundaries
	var completedLines []string
	var remaining bytes.Buffer

	for {
		line, err := w.accumulator.ReadBytes('\n')
		if err == io.EOF {
			// Partial line at end of buffer; keep in accumulator for next Write
			remaining.Write(line)
			break
		}
		if err != nil && w.onAppendError != nil {
			w.onAppendError(err)
			break
		}

		// Found complete line with '\n'; strip trailing newline(s)
		lineStr := string(bytes.TrimRight(line, "\r\n"))
		completedLines = append(completedLines, lineStr)
	}

	// Reset accumulator and restore incomplete line
	w.accumulator.Reset()
	w.accumulator.Write(remaining.Bytes())

	// Dispatch completed lines to callback
	if len(completedLines) > 0 {
		w.appendLines(completedLines)
		w.invalidate() // Request redraw
	}

	return n, nil
}

// SetViewportWidth updates the terminal width used for ANSI parsing.
// This should be called when terminal is resized.
func (w *SubprocessStreamWriter) SetViewportWidth(width int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if width > 0 {
		w.viewportWidth = width
	}
}

// Flush ensures any buffered incomplete line is retained for next Write.
// This is a no-op for SubprocessStreamWriter since incomplete lines are preserved.
func (w *SubprocessStreamWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Lines are kept in accumulator until '\n' arrives; nothing to flush
	return nil
}
