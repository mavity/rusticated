package main

import (
	"os"
	"testing"
)

func TestRefreshPromptDefaultFallback(t *testing.T) {
	m := initialModel()
	m.shellW.SetValue("")

	if m.runner == nil {
		t.Skip("runner is nil, cannot test refreshPrompt")
	}

	m.refreshPrompt()

	got := m.shellW.Prompt
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

	if m.dualPane.Left.Width <= 0 {
		t.Errorf("recalculateLayout with chatOpen=true produced dualPane.Left width %d", m.dualPane.Left.Width)
	}
	if m.chatW.Width <= 0 {
		t.Errorf("recalculateLayout with chatOpen=true produced chatW width %d", m.chatW.Width)
	}
}

func TestRecalculateLayoutChatClosed(t *testing.T) {
	m := initialModel()
	m.width = 80
	m.height = 24
	m.chatOpen = false

	m.recalculateLayout()

	if m.dualPane.Left.Width <= 0 {
		t.Errorf("recalculateLayout with chatOpen=false produced dualPane.Left width %d", m.dualPane.Left.Width)
	}

	if m.chatW.Width > m.dualPane.Left.Width {
		t.Errorf("recalculateLayout with chatOpen=false should have smaller chatView width")
	}
}

func TestRecalculateLayoutSmallTerminal(t *testing.T) {
	m := initialModel()
	m.width = 40
	m.height = 10

	m.chatOpen = false
	m.recalculateLayout()

	if m.dualPane.Left.Width < 1 {
		t.Errorf("recalculateLayout on small terminal produced invalid dualPane.Left width")
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

func TestShellOutputReachesPlumeWidget(t *testing.T) {
	m := initialModel()
	m.width = 80
	m.height = 24
	m.recalculateLayout()

	initialLines := len(m.plumeW.Lines())

	msg := shellResultMsg{
		input:  "echo hello",
		output: []string{"hello"},
	}
	m.dispatch(msg)

	got := m.plumeW.Lines()
	if len(got) <= initialLines {
		t.Fatalf("plumeW not updated after shellResultMsg: had %d lines, still %d", initialLines, len(got))
	}
	found := false
	for _, line := range got {
		if line == "hello" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("plumeW lines do not contain shell output %q, got %v", "hello", got)
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

	got := m.shellW.Prompt
	if got == "" {
		t.Errorf("refreshPrompt produced empty prompt for /home/user")
	}
}

func TestRunShellCommandLsDoesNotPanic(t *testing.T) {
	m := initialModel()
	m.runShellCommand("ls")()
}

func TestRunShellCommandEchoDoesNotPanic(t *testing.T) {
	m := initialModel()

	var panicVal interface{}
	var result shellResultMsg
	func() {
		defer func() { panicVal = recover() }()
		msg := m.runShellCommand("echo test")()
		result, _ = msg.(shellResultMsg)
	}()

	if panicVal != nil {
		t.Fatalf("runShellCommand(\"echo test\") panicked: %v", panicVal)
	}
	if len(result.output) == 0 || result.output[0] != "test" {
		t.Errorf("echo test output %v", result.output)
	}
}

func TestRenderShellPromptUsesDarkGrayBackground(t *testing.T) {
	m := initialModel()
	m.width = 20
	m.height = 6
	m.shellW.SetValue("ls")
	m.refreshPrompt()

	buf := NewCellBuf(m.width, m.height)
	buf.Fill(Rect{X: 0, Y: 0, W: m.width, H: m.height}, Cell{BG: colorDarkGray})
	m.renderShellPromptInto(&buf)

	row := buf.Height - 1
	for x := 0; x < min(m.width, len(m.shellW.Prompt)+len(m.shellW.Value())); x++ {
		cell := buf.Get(x, row)
		if cell.FG != colorWhite {
			t.Fatalf("prompt text should be white on dark gray, got FG=%v BG=%v at x=%d", cell.FG, cell.BG, x)
		}
		if cell.BG != colorDarkGray {
			t.Fatalf("prompt text should use dark gray background, got FG=%v BG=%v at x=%d", cell.FG, cell.BG, x)
		}
	}
}
