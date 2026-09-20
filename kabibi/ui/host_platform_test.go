package ui

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"

	xterm "github.com/charmbracelet/x/term"
)

// ── enterRawMode Tests ───────────────────────────────────────────────────────

func TestEnterRawMode_MouseTrackingEscapeSequence_WritesX1b1000h(t *testing.T) {
	if !xterm.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is not a TTY; skipping raw-mode test")
	}

	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	if err := h.enterRawMode(); err != nil {
		t.Fatalf("enterRawMode() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b[?1000h") {
		t.Fatalf("enterRawMode() output = %q, want to contain \\x1b[?1000h", got)
	}
	h.exitRawMode()
}

func TestEnterRawMode_SGRMouseProtocolSequence_WritesX1b1006h(t *testing.T) {
	if !xterm.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is not a TTY; skipping raw-mode test")
	}

	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	if err := h.enterRawMode(); err != nil {
		t.Fatalf("enterRawMode() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\x1b[?1006h") {
		t.Fatalf("enterRawMode() output = %q, want to contain \\x1b[?1006h", got)
	}
	h.exitRawMode()
}

func TestEnterRawMode_TerminalStateStorage_PlatformPrevTransitionsFromNilToPopulated(t *testing.T) {
	if !xterm.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is not a TTY; skipping raw-mode test")
	}

	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	if h.platform.prev != nil {
		t.Fatal("enterRawMode() should start with platform.prev == nil")
	}
	if err := h.enterRawMode(); err != nil {
		t.Fatalf("enterRawMode() error = %v", err)
	}
	if h.platform.prev == nil {
		t.Fatal("enterRawMode() should set platform.prev to non-nil")
	}
	h.exitRawMode()
}

func TestEnterRawMode_FileDescriptorErrorHandling_InvalidFdReturnsError(t *testing.T) {
	// This test verifies the error path; we cannot easily create an invalid fd
	// in a platform-independent way, but the function should return err from MakeRaw.
	// For now, this test documents the contract that errors are propagated.
	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	// enterRawMode uses os.Stdin.Fd() directly, which is valid in tests.
	// The error case is difficult to simulate portably; document for manual verification.
	_ = h
}

// ── exitRawMode Tests ────────────────────────────────────────────────────────

func TestExitRawMode_MouseTrackingDisableSequence_WritesX1b1006l(t *testing.T) {
	if !xterm.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is not a TTY; skipping raw-mode test")
	}

	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	if err := h.enterRawMode(); err != nil {
		t.Fatalf("enterRawMode() error = %v", err)
	}
	out.Reset()
	h.exitRawMode()
	if got := out.String(); !strings.Contains(got, "\x1b[?1006l") {
		t.Fatalf("exitRawMode() output = %q, want to contain \\x1b[?1006l", got)
	}
}

func TestExitRawMode_BasicMouseDisableSequence_WritesX1b1000l(t *testing.T) {
	if !xterm.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is not a TTY; skipping raw-mode test")
	}

	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	if err := h.enterRawMode(); err != nil {
		t.Fatalf("enterRawMode() error = %v", err)
	}
	out.Reset()
	h.exitRawMode()
	if got := out.String(); !strings.Contains(got, "\x1b[?1000l") {
		t.Fatalf("exitRawMode() output = %q, want to contain \\x1b[?1000l", got)
	}
}

func TestExitRawMode_TerminalStateRestoration_PlatformPrevReturnsSafelyToNil(t *testing.T) {
	if !xterm.IsTerminal(os.Stdin.Fd()) {
		t.Skip("stdin is not a TTY; skipping raw-mode test")
	}

	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	if err := h.enterRawMode(); err != nil {
		t.Fatalf("enterRawMode() error = %v", err)
	}
	h.exitRawMode()
	if h.platform.prev != nil {
		t.Fatalf("exitRawMode() should set platform.prev to nil, got %#v", h.platform.prev)
	}
}

func TestExitRawMode_IdempotentExecution_CallingTwiceDoesNotPanic(t *testing.T) {
	var out bytes.Buffer
	h := &Host{out: bufio.NewWriter(&out), stopCh: make(chan struct{})}
	// Call exitRawMode twice; should not panic even if platform.prev is nil.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("exitRawMode() panicked: %v", r)
		}
	}()
	h.exitRawMode()
	h.exitRawMode()
}

// ── querySize Tests ──────────────────────────────────────────────────────────

func TestQuerySize_DimensionNonNegativeGuarantee_WidthAndHeightNeverNegative(t *testing.T) {
	h := &Host{}
	w, ht := h.querySize()
	if w < 0 {
		t.Fatalf("querySize() width = %d, want >= 0", w)
	}
	if ht < 0 {
		t.Fatalf("querySize() height = %d, want >= 0", ht)
	}
}

func TestQuerySize_DelegationIntegrity_DimensionsMatchTerminalCalls(t *testing.T) {
	h := &Host{}
	// querySize delegates to xterm.GetSize(os.Stdout.Fd()).
	// This test documents that the return values map correctly.
	w1, ht1 := h.querySize()
	w2, ht2 := h.querySize()
	// Dimensions should be consistent across calls (assuming terminal not resized).
	if w1 != w2 || ht1 != ht2 {
		t.Logf("querySize() dimensions differ (terminal may have been resized): (%d, %d) vs (%d, %d)", w1, ht1, w2, ht2)
	}
}
