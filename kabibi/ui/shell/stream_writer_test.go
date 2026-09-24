package shell

import (
	"strings"
	"sync"
	"testing"
)

// TestSubprocessStreamWriterLineTokenization verifies that Write() correctly
// tokenizes input on '\n' and '\r\n' boundaries and dispatches complete lines.
func TestSubprocessStreamWriterLineTokenization(t *testing.T) {
	tests := []struct {
		name     string
		writes   []string
		expected [][]string // expected line batches after each write
		endings  []string   // line ending types for each write
	}{
		{
			name:     "single line with LF",
			writes:   []string{"hello\n"},
			expected: [][]string{{"hello"}},
		},
		{
			name:     "single line with CRLF",
			writes:   []string{"hello\r\n"},
			expected: [][]string{{"hello"}},
		},
		{
			name:     "multiple lines in one write",
			writes:   []string{"line1\nline2\nline3\n"},
			expected: [][]string{{"line1", "line2", "line3"}},
		},
		{
			name:     "incomplete line followed by completion",
			writes:   []string{"partial", " line\n"},
			expected: [][]string{{"partial line"}}, // First write accumulates, second write completes and dispatches
		},
		{
			name:     "mixed line endings",
			writes:   []string{"line1\r\nline2\nline3\n"},
			expected: [][]string{{"line1", "line2", "line3"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var appendedLines [][]string
			sw := NewSubprocessStreamWriter(
				func(lines []string) {
					if lines != nil {
						// Copy to preserve the slice
						linesCopy := make([]string, len(lines))
						copy(linesCopy, lines)
						appendedLines = append(appendedLines, linesCopy)
					}
				},
				func() {}, // no-op invalidate
				80,
			)

			for _, writeData := range tc.writes {
				_, err := sw.Write([]byte(writeData))
				if err != nil {
					t.Fatalf("Write failed: %v", err)
				}
			}

			// Verify expected lines were dispatched
			if len(appendedLines) != len(tc.expected) {
				t.Fatalf("expected %d dispatch batches, got %d", len(tc.expected), len(appendedLines))
			}

			for i, expected := range tc.expected {
				if expected == nil && len(appendedLines[i]) == 0 {
					continue // Expected no lines, got none
				}
				if len(appendedLines[i]) != len(expected) {
					t.Fatalf("batch %d: expected %d lines, got %d: %v",
						i, len(expected), len(appendedLines[i]), appendedLines[i])
				}
				for j, expLine := range expected {
					if appendedLines[i][j] != expLine {
						t.Fatalf("batch %d line %d: expected %q, got %q",
							i, j, expLine, appendedLines[i][j])
					}
				}
			}
		})
	}
}

// TestSubprocessStreamWriterThreadSafety verifies that concurrent writes are
// properly serialized and do not corrupt internal state.
func TestSubprocessStreamWriterThreadSafety(t *testing.T) {
	var mu sync.Mutex
	var allLines []string

	sw := NewSubprocessStreamWriter(
		func(lines []string) {
			mu.Lock()
			defer mu.Unlock()
			allLines = append(allLines, lines...)
		},
		func() {}, // no-op invalidate
		80,
	)

	// Spawn multiple goroutines writing simultaneously
	const numGoroutines = 10
	const linesPerGoroutine = 5

	var wg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for line := 0; line < linesPerGoroutine; line++ {
				payload := []byte(strings.Repeat("x", 20) + "\n")
				_, err := sw.Write(payload)
				if err != nil {
					t.Errorf("Write failed: %v", err)
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify all lines were accumulated
	expectedLineCount := numGoroutines * linesPerGoroutine
	if len(allLines) != expectedLineCount {
		t.Fatalf("expected %d total lines, got %d", expectedLineCount, len(allLines))
	}

	// Verify no lines are corrupted
	for _, line := range allLines {
		if len(line) != 20 {
			t.Fatalf("expected line length 20, got %d: %q", len(line), line)
		}
		if line != strings.Repeat("x", 20) {
			t.Fatalf("line corrupted: %q", line)
		}
	}
}

// TestSubprocessStreamWriterIncompleteLineRetention verifies that incomplete
// lines (without newline terminator) are retained until the next Write.
func TestSubprocessStreamWriterIncompleteLineRetention(t *testing.T) {
	var dispatchedLines []string

	sw := NewSubprocessStreamWriter(
		func(lines []string) {
			dispatchedLines = append(dispatchedLines, lines...)
		},
		func() {}, // no-op invalidate
		80,
	)

	// Write incomplete line
	sw.Write([]byte("incomplete"))
	if len(dispatchedLines) != 0 {
		t.Fatalf("incomplete line should not be dispatched, but got: %v", dispatchedLines)
	}

	// Write continuation
	sw.Write([]byte(" line\n"))
	if len(dispatchedLines) != 1 {
		t.Fatalf("expected 1 complete line after continuation, got %d", len(dispatchedLines))
	}
	if dispatchedLines[0] != "incomplete line" {
		t.Fatalf("expected 'incomplete line', got %q", dispatchedLines[0])
	}
}

// TestSubprocessStreamWriterEmptyWrites verifies that empty writes are safe.
func TestSubprocessStreamWriterEmptyWrites(t *testing.T) {
	callCount := 0
	sw := NewSubprocessStreamWriter(
		func(lines []string) {
			callCount++
		},
		func() {}, // no-op invalidate
		80,
	)

	n, err := sw.Write([]byte{})
	if err != nil || n != 0 {
		t.Fatalf("empty write should return (0, nil), got (%d, %v)", n, err)
	}
	if callCount != 0 {
		t.Fatalf("empty write should not trigger callback, but callback was called")
	}
}

// TestSubprocessStreamWriterInvalidateTrigger verifies that invalidate callback
// is fired after dispatching lines.
func TestSubprocessStreamWriterInvalidateTrigger(t *testing.T) {
	invalidateCount := 0
	dispatchCount := 0

	sw := NewSubprocessStreamWriter(
		func(lines []string) {
			dispatchCount++
		},
		func() {
			invalidateCount++
		},
		80,
	)

	sw.Write([]byte("line1\nline2\n"))

	if dispatchCount != 1 {
		t.Fatalf("expected 1 dispatch, got %d", dispatchCount)
	}
	if invalidateCount != 1 {
		t.Fatalf("expected 1 invalidate call, got %d", invalidateCount)
	}

	// Write incomplete line - no invalidate should fire
	sw.Write([]byte("incomplete"))
	if invalidateCount != 1 {
		t.Fatalf("incomplete line should not trigger invalidate, but count is %d", invalidateCount)
	}
}

// TestSubprocessStreamWriterWhitespaceStripping verifies that trailing
// whitespace (especially CR) is correctly stripped from lines.
func TestSubprocessStreamWriterWhitespaceStripping(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"line\n", "line"},
		{"line\r\n", "line"},
		{"line \n", "line "},     // trailing spaces before newline are preserved
		{"line\t\r\n", "line\t"}, // tabs are preserved, CR/LF stripped
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			var result []string
			sw := NewSubprocessStreamWriter(
				func(lines []string) {
					result = append(result, lines...)
				},
				func() {}, // no-op invalidate
				80,
			)

			sw.Write([]byte(tc.input))

			if len(result) != 1 {
				t.Fatalf("expected 1 line, got %d", len(result))
			}
			if result[0] != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, result[0])
			}
		})
	}
}

// TestSubprocessStreamWriterViewportWidthUpdate verifies that SetViewportWidth
// updates the writer's configured width for future ANSI parsing.
func TestSubprocessStreamWriterViewportWidthUpdate(t *testing.T) {
	sw := NewSubprocessStreamWriter(
		func(lines []string) {},
		func() {},
		80,
	)

	if sw.viewportWidth != 80 {
		t.Fatalf("expected initial width 80, got %d", sw.viewportWidth)
	}

	sw.SetViewportWidth(120)

	if sw.viewportWidth != 120 {
		t.Fatalf("expected width 120 after update, got %d", sw.viewportWidth)
	}

	// Invalid widths should be rejected
	sw.SetViewportWidth(0)
	if sw.viewportWidth != 120 {
		t.Fatalf("expected width to remain 120 after invalid update, got %d", sw.viewportWidth)
	}
}

// BenchmarkSubprocessStreamWriterWrite measures throughput of Write() calls
// to baseline stream ingestion performance.
func BenchmarkSubprocessStreamWriterWrite(b *testing.B) {
	sw := NewSubprocessStreamWriter(
		func(lines []string) {}, // discard lines
		func() {},               // no-op invalidate
		80,
	)

	payload := []byte(strings.Repeat("x", 100) + "\n")

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sw.Write(payload)
	}
}

// BenchmarkSubprocessStreamWriterConcurrentWrites measures throughput under
// concurrent write pressure to verify lock contention is acceptable.
func BenchmarkSubprocessStreamWriterConcurrentWrites(b *testing.B) {
	sw := NewSubprocessStreamWriter(
		func(lines []string) {}, // discard lines
		func() {},               // no-op invalidate
		80,
	)

	payload := []byte(strings.Repeat("x", 100) + "\n")

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sw.Write(payload)
		}
	})
}
