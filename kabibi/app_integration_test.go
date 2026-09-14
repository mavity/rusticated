package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// UITest drives a model through a sequence of inputs and captures frames.
type UITest struct {
	model  *AppWidget
	frames []string // captured app view outputs
}

func appRender(m *AppWidget) string {
	if m == nil {
		return ""
	}
	return m.viewFrame()
}

func appApply(m *AppWidget, msg tea.Msg) *AppWidget {
	if m == nil {
		return nil
	}
	m.dispatch(msg)
	return m
}

// NewUITest creates a new UITest with a model at fixed size.
func NewUITest(t *testing.T, w, h int) *UITest {
	m := initialModel()
	m.width = w
	m.height = h
	m.recalculateLayout()
	return &UITest{model: m, frames: []string{}}
}

// RealFSService is a real filesystem implementation of FSService for integration tests.
type RealFSService struct{}

func (r *RealFSService) ReadDir(path string) ([]FSEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]FSEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, FSEntry{Name: e.Name(), IsDir: e.IsDir()})
	}
	return result, nil
}

// DriveKeys feeds a sequence of key messages through Update, capturing View() after each.
func (ut *UITest) DriveKeys(keys ...tea.KeyMsg) {
	for i, key := range keys {
		stateBefore := ut.model.activePane
		ut.model = appApply(ut.model, key)
		stateAfter := ut.model.activePane
		ut.frames = append(ut.frames, appRender(ut.model))

		// Debug: log state changes for unexpected cases
		if stateBefore != stateAfter {
			// State changed as expected
		} else if key.Type == tea.KeyTab {
			panic(fmt.Sprintf("DriveKeys[%d]: Tab key pressed but activePane didn't change (before=%v, after=%v)", i, stateBefore, stateAfter))
		}
	}
}

// CurrentFrame returns the most recent View() output (ANSI).
func (ut *UITest) CurrentFrame() string {
	if len(ut.frames) == 0 {
		return appRender(ut.model)
	}
	return ut.frames[len(ut.frames)-1]
}

// FrameAt returns the View() output at index i, or "" if out of bounds.
func (ut *UITest) FrameAt(i int) string {
	if i < 0 || i >= len(ut.frames) {
		return ""
	}
	return ut.frames[i]
}

// PlainText strips ANSI codes from the current frame and returns plain text.
func (ut *UITest) PlainText() string {
	return ansi.Strip(ut.CurrentFrame())
}

// PlainTextAt strips ANSI codes from frame i.
func (ut *UITest) PlainTextAt(i int) string {
	return ansi.Strip(ut.FrameAt(i))
}

// AssertPlainContains checks that the current frame's plain text contains s.
func (ut *UITest) AssertPlainContains(t *testing.T, s string) bool {
	t.Helper()
	plain := ut.PlainText()
	if !strings.Contains(plain, s) {
		t.Errorf("current frame plain text should contain %q; got:\n%q", s, plain)
		return false
	}
	return true
}

// AssertPlainNotContains checks that the current frame's plain text does NOT contain s.
func (ut *UITest) AssertPlainNotContains(t *testing.T, s string) bool {
	t.Helper()
	plain := ut.PlainText()
	if strings.Contains(plain, s) {
		t.Errorf("current frame plain text should NOT contain %q; got:\n%q", s, plain)
		return false
	}
	return true
}

// NormalizeANSI preserves ANSI escape codes but normalizes whitespace for stable comparisons.
// This ensures snapshots capture visual output (colors, styles) while handling minor formatting differences.
func NormalizeANSI(s string) string {
	// Strategy: Preserve ANSI codes, but normalize surrounding text.
	// We do NOT strip ANSI codes - that defeats the purpose of E2E visual testing.

	// Normalize line endings
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// Remove trailing whitespace from each line (terminal output often has padding)
	// But be careful not to break ANSI codes themselves
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		// Find the last non-whitespace character
		// We need to be smart: if the line ends with ANSI reset (\x1b[0m), keep it
		// Otherwise, trim trailing spaces

		// Strip trailing spaces and tabs, but keep reset codes
		line = strings.TrimRight(line, " \t")

		lines[i] = line
	}

	return strings.Join(lines, "\n")
}

// AssertUISnapshot compares the current frame's normalized ANSI against a named snapshot.
func (ut *UITest) AssertUISnapshot(t *testing.T, name string) {
	t.Helper()
	normalized := NormalizeANSI(ut.CurrentFrame())
	assertSnapshot(t, name, normalized)
}

// AssertFrameSnapshot snapshots frame i.
func (ut *UITest) AssertFrameSnapshot(t *testing.T, name string, i int) {
	t.Helper()
	normalized := NormalizeANSI(ut.FrameAt(i))
	assertSnapshot(t, name, normalized)
}

// Model returns the underlying model for low-level assertions.
func (ut *UITest) Model() *AppWidget {
	return ut.model
}

// SetModel updates the underlying model (for direct Update calls).
func (ut *UITest) SetModel(m *AppWidget) {
	ut.model = m
}
