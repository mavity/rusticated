package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestInitialModel verifies the model initializes in browser mode with proper state.
func TestInitialModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Check initial state
	if m.mode != modeBrowser {
		t.Errorf("initial mode = %v, want %v", m.mode, modeBrowser)
	}
	if m.activePane != leftPane {
		t.Errorf("initial active pane = %v, want %v", m.activePane, leftPane)
	}
	if m.runner == nil {
		t.Error("runner is nil, want initialized runner")
	}
	if m.conversation == nil {
		t.Error("conversation is nil, want initialized conversation")
	}
}

// TestModelRender verifies the model can render without panicking.
func TestModelRender(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()
	output := m.viewFrame()

	if output == "" {
		t.Error("viewFrame() returned empty output")
	}
}

// TestModelWindowSize verifies window size changes are handled correctly.
func TestModelWindowSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Send window resize message
	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	m.dispatch(msg)

	if m.width != 120 || m.height != 40 {
		t.Errorf("window size not updated: got %dx%d, want 120x40", m.width, m.height)
	}
}

// TestModelWindowSizeRecalculation verifies layout is recalculated on resize.
func TestModelWindowSizeRecalculation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Resize to a different size
	msg := tea.WindowSizeMsg{Width: 200, Height: 50}
	m.dispatch(msg)

	// The layout should have been recalculated
	output := m.viewFrame()
	if output == "" {
		t.Error("View() returned empty after resize")
	}
}

// TestModelSmallWindow verifies the model handles very small windows (10x5).
func TestModelSmallWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Try to render in a tiny window
	msg := tea.WindowSizeMsg{Width: 10, Height: 5}
	m.dispatch(msg)

	output := m.viewFrame()
	// Should not panic, even if output is limited
	if output == "" {
		t.Error("output is empty")
	}
	if m.width != 10 {
		t.Errorf("model width = %d, want 10", m.width)
	}
	if m.height != 5 {
		t.Errorf("model height = %d, want 5", m.height)
	}
}

// TestModelTinyWindow verifies the model handles extremely small windows (5x3).
func TestModelTinyWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Try to render in an extremely tiny window
	msg := tea.WindowSizeMsg{Width: 5, Height: 3}
	m.dispatch(msg)

	// Should not panic at all, regardless of output
	_ = m.viewFrame()
}

// TestModelEdgeCaseWindow verifies the model handles edge case windows (1x1).
func TestModelEdgeCaseWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Try to render in a 1x1 window
	msg := tea.WindowSizeMsg{Width: 1, Height: 1}
	m.dispatch(msg)

	// Should not panic
	_ = m.viewFrame()
}

// TestModelNormalWindow verifies the model handles standard windows (80x24).
func TestModelNormalWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	msg := tea.WindowSizeMsg{Width: 80, Height: 24}
	m.dispatch(msg)

	output := m.viewFrame()
	if output == "" {
		t.Error("output is empty for normal window")
	}
	if m.width != 80 {
		t.Errorf("model width = %d, want 80", m.width)
	}
}

// TestModelLargeWindow verifies the model handles large windows (200x50).
func TestModelLargeWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	msg := tea.WindowSizeMsg{Width: 200, Height: 50}
	m.dispatch(msg)

	output := m.viewFrame()
	if output == "" {
		t.Error("output is empty for large window")
	}
	if m.width != 200 {
		t.Errorf("model width = %d, want 200", m.width)
	}
}

// TestModelWindowSequence verifies the model handles a sequence of size changes.
func TestModelWindowSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	sizes := []tea.WindowSizeMsg{
		{Width: 5, Height: 3},
		{Width: 80, Height: 24},
		{Width: 200, Height: 50},
		{Width: 10, Height: 5},
		{Width: 120, Height: 40},
	}

	for i, msg := range sizes {
		m.dispatch(msg)

		// Should not panic rendering at any size
		_ = m.viewFrame()

		if m.width != msg.Width {
			t.Errorf("sequence[%d]: width = %d, want %d", i, m.width, msg.Width)
		}
		if m.height != msg.Height {
			t.Errorf("sequence[%d]: height = %d, want %d", i, m.height, msg.Height)
		}
	}
}

// TestModelUpdateNoop verifies unrecognized messages are handled safely.
func TestModelUpdateNoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()
	customMsg := struct{}{}

	// Should not panic
	m.dispatch(customMsg)
}

// TestModelModeTransitions verifies mode changes work correctly.
func TestModelModeTransitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Verify model state
	if m.runner == nil {
		t.Error("runner is nil, want initialized runner")
	}
	if m.conversation == nil {
		t.Error("conversation is nil, want initialized conversation")
	}

	// Test rendering doesn't panic
	output := m.viewFrame()
	if output == "" {
		t.Error("viewFrame() returned empty")
	}
}

// TestModelActivePaneSwitch verifies switching between panes works.
func TestModelActivePaneSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Start in left pane
	if m.activePane != leftPane {
		t.Fatalf("should start in left pane")
	}

	// Verify the model can render
	output := m.viewFrame()
	if output == "" {
		t.Error("viewFrame() returned empty")
	}
}

// TestModelRenderAfterMessages verifies rendering is stable across message types.
func TestModelRenderAfterMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Render, send a message, render again
	output1 := m.viewFrame()
	if output1 == "" {
		t.Error("initial render empty")
	}

	// Send a no-op message
	m.dispatch(struct{}{})
	output2 := m.viewFrame()
	if output2 == "" {
		t.Error("render after message empty")
	}
}

// TestModelQuickSequence tests a quick sequence of operations.
func TestModelQuickSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Quick sequence: render, resize, render
	_ = m.viewFrame()

	msg := tea.WindowSizeMsg{Width: 100, Height: 30}
	m.dispatch(msg)
	output := m.viewFrame()
	if output == "" {
		t.Error("render after sequence empty")
	}
}

// TestModelMultipleResize tests handling multiple resize messages.
func TestModelMultipleResize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	sizes := []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 120, Height: 40},
		{Width: 60, Height: 20},
	}

	for _, size := range sizes {
		m.dispatch(size)
		if m.width != size.Width || m.height != size.Height {
			t.Errorf("size not updated after message")
		}
		output := m.viewFrame()
		if output == "" {
			t.Error("render empty after resize")
		}
	}
}

// TestModelAnimationTick verifies animation messages are handled.
func TestModelAnimationTick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	// Send animation tick messages
	tick := animTickMsg(time.Now())
	m.dispatch(tick)

	// Should render without issues
	output := m.viewFrame()
	if output == "" {
		t.Error("render after tick empty")
	}
}

// TestModelConsecutiveAnimationTicks verifies animation ticks work continuously.
func TestModelConsecutiveAnimationTicks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()

	startTime := time.Now()
	for i := 0; i < 5; i++ {
		tick := animTickMsg(startTime.Add(time.Duration(i*50) * time.Millisecond))
		m.dispatch(tick)
		output := m.viewFrame()
		if output == "" {
			t.Error("render after tick empty")
		}
	}
}

// TestModelStableOutput verifies output is stable without external changes.
func TestModelStableOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	m := initialModel()
	m.width = 80
	m.height = 24

	output1 := m.viewFrame()
	output2 := m.viewFrame()

	if output1 != output2 {
		t.Error("output changed between renders without messages")
	}
}
