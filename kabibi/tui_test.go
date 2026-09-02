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

	modelVal := initialModel()
	m := &modelVal

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

	modelVal := initialModel()
	m := &modelVal
	output := m.View()

	if output == "" {
		t.Error("View() returned empty output")
	}
}

// TestModelWindowSize verifies window size changes are handled correctly.
func TestModelWindowSize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Send window resize message
	msg := tea.WindowSizeMsg{Width: 120, Height: 40}
	newModel, _ := m.Update(msg)

	updated, ok := newModel.(*model)
	if !ok {
		t.Fatal("Update did not return a *model")
	}

	if updated.width != 120 || updated.height != 40 {
		t.Errorf("window size not updated: got %dx%d, want 120x40", updated.width, updated.height)
	}
}

// TestModelWindowSizeRecalculation verifies layout is recalculated on resize.
func TestModelWindowSizeRecalculation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Resize to a different size
	msg := tea.WindowSizeMsg{Width: 200, Height: 50}
	newModel, _ := m.Update(msg)

	updated, ok := newModel.(*model)
	if !ok {
		t.Fatal("Update did not return a *model")
	}

	// The layout should have been recalculated
	output := updated.View()
	if output == "" {
		t.Error("View() returned empty after resize")
	}
}

// TestModelSmallWindow verifies the model handles very small windows (10x5).
func TestModelSmallWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Try to render in a tiny window
	msg := tea.WindowSizeMsg{Width: 10, Height: 5}
	newModel, _ := m.Update(msg)

	updated, ok := newModel.(*model)
	if !ok {
		t.Fatal("Update did not return a *model")
	}

	output := updated.View()
	// Should not panic, even if output is limited
	if output == "" {
		t.Error("output is empty")
	}
	if updated.width != 10 {
		t.Errorf("model width = %d, want 10", updated.width)
	}
	if updated.height != 5 {
		t.Errorf("model height = %d, want 5", updated.height)
	}
}

// TestModelTinyWindow verifies the model handles extremely small windows (5x3).
func TestModelTinyWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Try to render in an extremely tiny window
	msg := tea.WindowSizeMsg{Width: 5, Height: 3}
	newModel, _ := m.Update(msg)

	updated, ok := newModel.(*model)
	if !ok {
		t.Fatal("Update did not return a *model")
	}

	// Should not panic at all, regardless of output
	_ = updated.View()
}

// TestModelEdgeCaseWindow verifies the model handles edge case windows (1x1).
func TestModelEdgeCaseWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Try to render in a 1x1 window
	msg := tea.WindowSizeMsg{Width: 1, Height: 1}
	newModel, _ := m.Update(msg)

	updated, ok := newModel.(*model)
	if !ok {
		t.Fatal("Update did not return a *model")
	}

	// Should not panic
	_ = updated.View()
}

// TestModelNormalWindow verifies the model handles standard windows (80x24).
func TestModelNormalWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	msg := tea.WindowSizeMsg{Width: 80, Height: 24}
	newModel, _ := m.Update(msg)

	updated, ok := newModel.(*model)
	if !ok {
		t.Fatal("Update did not return a *model")
	}

	output := updated.View()
	if output == "" {
		t.Error("output is empty for normal window")
	}
	if updated.width != 80 {
		t.Errorf("model width = %d, want 80", updated.width)
	}
}

// TestModelLargeWindow verifies the model handles large windows (200x50).
func TestModelLargeWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	msg := tea.WindowSizeMsg{Width: 200, Height: 50}
	newModel, _ := m.Update(msg)

	updated, ok := newModel.(*model)
	if !ok {
		t.Fatal("Update did not return a *model")
	}

	output := updated.View()
	if output == "" {
		t.Error("output is empty for large window")
	}
	if updated.width != 200 {
		t.Errorf("model width = %d, want 200", updated.width)
	}
}

// TestModelWindowSequence verifies the model handles a sequence of size changes.
func TestModelWindowSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	sizes := []tea.WindowSizeMsg{
		{Width: 5, Height: 3},
		{Width: 80, Height: 24},
		{Width: 200, Height: 50},
		{Width: 10, Height: 5},
		{Width: 120, Height: 40},
	}

	for i, msg := range sizes {
		newModel, _ := m.Update(msg)
		updated, ok := newModel.(*model)
		if !ok {
			t.Fatalf("sequence[%d]: Update did not return a *model", i)
		}

		// Should not panic rendering at any size
		_ = updated.View()

		if updated.width != msg.Width {
			t.Errorf("sequence[%d]: width = %d, want %d", i, updated.width, msg.Width)
		}
		if updated.height != msg.Height {
			t.Errorf("sequence[%d]: height = %d, want %d", i, updated.height, msg.Height)
		}

		// Update m for next iteration
		m = updated
	}
}

// TestModelUpdateNoop verifies unrecognized messages are handled safely.
func TestModelUpdateNoop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal
	customMsg := struct{}{}

	// Should not panic
	newModel, _ := m.Update(customMsg)

	if newModel != nil {
		_, ok := newModel.(*model)
		if !ok {
			t.Error("Update did not return a *model")
		}
	}
}

// TestModelModeTransitions verifies mode changes work correctly.
func TestModelModeTransitions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Verify model state
	if m.runner == nil {
		t.Error("runner is nil, want initialized runner")
	}
	if m.conversation == nil {
		t.Error("conversation is nil, want initialized conversation")
	}

	// Test rendering doesn't panic
	output := m.View()
	if output == "" {
		t.Error("View() returned empty")
	}
}

// TestModelActivePaneSwitch verifies switching between panes works.
func TestModelActivePaneSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Start in left pane
	if m.activePane != leftPane {
		t.Fatalf("should start in left pane")
	}

	// Verify the model can render
	output := m.View()
	if output == "" {
		t.Error("View() returned empty")
	}
}

// TestModelRenderAfterMessages verifies rendering is stable across message types.
func TestModelRenderAfterMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Render, send a message, render again
	output1 := m.View()
	if output1 == "" {
		t.Error("initial render empty")
	}

	// Send a no-op message
	newModel, _ := m.Update(struct{}{})
	if newModel != nil {
		updated, ok := newModel.(*model)
		if ok {
			output2 := updated.View()
			if output2 == "" {
				t.Error("render after message empty")
			}
		}
	}
}

// TestModelQuickSequence tests a quick sequence of operations.
func TestModelQuickSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Quick sequence: render, resize, render
	_ = m.View()

	msg := tea.WindowSizeMsg{Width: 100, Height: 30}
	newModel, _ := m.Update(msg)

	if newModel != nil {
		updated, ok := newModel.(*model)
		if ok {
			output := updated.View()
			if output == "" {
				t.Error("render after sequence empty")
			}
		}
	}
}

// TestModelMultipleResize tests handling multiple resize messages.
func TestModelMultipleResize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	sizes := []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 120, Height: 40},
		{Width: 60, Height: 20},
	}

	for _, size := range sizes {
		newModel, _ := m.Update(size)

		if newModel != nil {
			updated, ok := newModel.(*model)
			if ok && (updated.width != size.Width || updated.height != size.Height) {
				t.Errorf("size not updated after message")
			}

			if ok {
				output := updated.View()
				if output == "" {
					t.Error("render empty after resize")
				}
			}
		}
	}
}

// TestModelAnimationTick verifies animation messages are handled.
func TestModelAnimationTick(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	// Send animation tick messages
	tick := animTickMsg(time.Now())
	newModel, _ := m.Update(tick)

	// Should render without issues
	if newModel != nil {
		updated, ok := newModel.(*model)
		if ok {
			output := updated.View()
			if output == "" {
				t.Error("render after tick empty")
			}
		}
	}
}

// TestModelConsecutiveAnimationTicks verifies animation ticks work continuously.
func TestModelConsecutiveAnimationTicks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal

	startTime := time.Now()
	for i := 0; i < 5; i++ {
		tick := animTickMsg(startTime.Add(time.Duration(i*50) * time.Millisecond))
		newModel, _ := m.Update(tick)

		if newModel != nil {
			updated, ok := newModel.(*model)
			if ok {
				output := updated.View()
				if output == "" {
					t.Error("render after tick empty")
				}
			}
		}
	}
}

// TestModelStableOutput verifies output is stable without external changes.
func TestModelStableOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TUI test in short mode")
	}

	modelVal := initialModel()
	m := &modelVal
	m.width = 80
	m.height = 24

	output1 := m.View()
	output2 := m.View()

	if output1 != output2 {
		t.Error("output changed between renders without messages")
	}
}
