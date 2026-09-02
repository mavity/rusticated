package main

import (
	"os"
	"testing"
)

func TestRefreshPromptDefaultFallback(t *testing.T) {
	m := initialModel()
	m.shellInput.SetValue("")

	if m.runner == nil {
		t.Skip("runner is nil, cannot test refreshPrompt")
	}

	m.refreshPrompt()

	got := m.shellInput.Prompt
	if got == "" {
		t.Errorf("refreshPrompt with no PS1 produced empty prompt")
	}

	if !contains(got, "$") {
		t.Errorf("refreshPrompt with no PS1 should contain '$', got %q", got)
	}
}

func TestAddPlumeRingBuffer(t *testing.T) {
	m := initialModel()
	m.plume = []string{"initial"}

	for i := 0; i < 130; i++ {
		m.AddPlume("line")
	}

	if len(m.plume) > 120 {
		t.Errorf("AddPlume exceeded 120 lines, got %d", len(m.plume))
	}

	if len(m.plume) < 120 {
		t.Errorf("AddPlume should have ~120 lines, got %d", len(m.plume))
	}
}

func TestRecalculateLayout(t *testing.T) {
	m := initialModel()
	m.width = 80
	m.height = 24

	m.chatOpen = true
	m.recalculateLayout()

	if m.leftList.Width() <= 0 {
		t.Errorf("recalculateLayout with chatOpen=true produced leftList width %d", m.leftList.Width())
	}
	if m.chatView.Width <= 0 {
		t.Errorf("recalculateLayout with chatOpen=true produced chatView width %d", m.chatView.Width)
	}
}

func TestRecalculateLayoutChatClosed(t *testing.T) {
	m := initialModel()
	m.width = 80
	m.height = 24
	m.chatOpen = false

	m.recalculateLayout()

	if m.leftList.Width() <= 0 {
		t.Errorf("recalculateLayout with chatOpen=false produced leftList width %d", m.leftList.Width())
	}

	if m.chatView.Width > m.leftList.Width() {
		t.Errorf("recalculateLayout with chatOpen=false should have smaller chatView width")
	}
}

func TestRecalculateLayoutSmallTerminal(t *testing.T) {
	m := initialModel()
	m.width = 40
	m.height = 10

	m.chatOpen = false
	m.recalculateLayout()

	if m.leftList.Width() < 1 {
		t.Errorf("recalculateLayout on small terminal produced invalid leftList width")
	}
}

func contains(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestAddPlumeMultipleCalls(t *testing.T) {
	m := initialModel()
	m.plume = []string{}

	m.AddPlume("a", "b", "c")

	if len(m.plume) != 3 {
		t.Errorf("AddPlume with 3 lines produced %d lines", len(m.plume))
	}

	if m.plume[0] != "a" || m.plume[1] != "b" || m.plume[2] != "c" {
		t.Errorf("AddPlume lines not preserved, got %v", m.plume)
	}
}

func TestRefreshPromptWithPwd(t *testing.T) {
	if os.Getenv("PS1") != "" {
		t.Skip("PS1 env var already set, skipping")
	}

	m := initialModel()
	if m.runner == nil {
		t.Skip("runner is nil")
	}

	m.runner.Dir = "/home/user"
	m.refreshPrompt()

	got := m.shellInput.Prompt
	if got == "" {
		t.Errorf("refreshPrompt produced empty prompt for /home/user")
	}
}
