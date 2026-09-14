package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// --- Basic navigation ---

func TestIntegrationTabCyclesPanes(t *testing.T) {
	ui := NewUITest(t, 80, 24)
	ui.DriveKeys() // capture initial state

	// Initially on left pane
	if ui.Model().activePane != leftPane {
		t.Fatal("should start on left pane")
	}

	// Tab to right pane
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyTab})
	if ui.Model().activePane != rightPane {
		t.Fatal("after first tab, should be on right pane")
	}

	// Tab to chat pane
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyTab})
	if ui.Model().activePane != chatPane {
		t.Fatal("after second tab, should be on chat pane")
	}

	// Tab back to left pane
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyTab})
	if ui.Model().activePane != leftPane {
		t.Fatal("after third tab, should cycle back to left pane")
	}
}

func TestIntegrationFileNavigationUpDown(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"apple.txt", "banana.txt", "cherry.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	ui := NewUITest(t, 80, 24)
	ui.Model().loadDir(leftPane, dir, "")
	ui.DriveKeys() // capture initial state

	// Frame 0 after loadDir: should show "..", "apple.txt", "banana.txt", "cherry.txt"
	// Default selection is ".."
	if !ui.AssertPlainContains(t, "apple") {
		t.Logf("Current plain text:\n%s", ui.PlainText())
		t.Fatal("expected apple.txt in initial frame")
	}

	// Down arrow twice: ".." -> "apple.txt" -> "banana.txt"
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown})

	t.Logf("After 2 Down presses:")
	t.Logf("  dualPane.Left.SelectedIndex: %d", ui.Model().dualPane.Left.SelectedIndex())
	t.Logf("  Current plain text:\n%s", ui.PlainText())

	if !ui.AssertPlainContains(t, "banana") {
		t.Fatal("expected banana.txt after 2 downs")
	}

	// Up: back to apple.txt
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyUp})
	plain := ui.PlainText()
	// apple should be highlighted/visible
	if !strings.Contains(plain, "apple") {
		t.Fatalf("expected apple.txt after up; got:\n%s", plain)
	}
}

func TestIntegrationEnterDirectory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ui := NewUITest(t, 80, 24)
	ui.Model().loadDir(leftPane, dir, "")
	ui.DriveKeys()

	// Navigate to subdir (down from "..")
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyDown})
	// Press Enter to descend
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyEnter})

	// Now we should see the contents of subdir
	if !ui.AssertPlainContains(t, "file.txt") {
		t.Fatal("expected file.txt after entering subdir")
	}

	// Verify widget dir changed
	if ui.Model().dualPane.Left.Dir() != subdir {
		t.Fatalf("left pane dir should be subdir, got %q", ui.Model().dualPane.Left.Dir())
	}
}

func TestIntegrationGoUpWithDotDot(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ui := NewUITest(t, 80, 24)
	ui.Model().loadDir(leftPane, dir, "sub")
	ui.DriveKeys() // capture after entering subdir

	// Now descend into sub
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter})
	// Should now be in subdir; select ".." and go back up
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyEnter}) // ".." is already selected

	// Should be back in parent
	if ui.Model().dualPane.Left.Dir() != dir {
		t.Fatalf("after going up with .., left pane dir should be parent; got %q", ui.Model().dualPane.Left.Dir())
	}
}

// --- Chat pane ---

func TestIntegrationChatInputAndEscape(t *testing.T) {
	ui := NewUITest(t, 80, 24)
	ui.Model().activePane = chatPane
	ui.DriveKeys()

	// Type in chat
	ui.DriveKeys(
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")},
	)

	// Chat widget should have the text
	if ui.Model().chatW.Input.Value() != "hi" {
		t.Fatalf("chat input should be 'hi', got %q", ui.Model().chatW.Input.Value())
	}

	// Escape should return to left pane
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if ui.Model().activePane != leftPane {
		t.Fatalf("escape from chat should return to leftPane, got %v", ui.Model().activePane)
	}
}

// --- File operations ---

func TestIntegrationFmEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ui := NewUITest(t, 80, 24)
	ui.Model().loadDir(leftPane, dir, "test.go")
	ui.DriveKeys()

	// Send 'e' key to open editor (assuming it's bound to fmEdit)
	// For now, call fmEdit directly
	ui.Model().fmEdit()

	if ui.Model().mode != modeEditor {
		t.Fatal("fmEdit should switch to editor mode")
	}
	if ui.Model().editor == nil {
		t.Fatal("fmEdit should populate editor")
	}
}

func TestIntegrationFmToggleMark(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	ui := NewUITest(t, 80, 24)
	ui.Model().loadDir(leftPane, dir, "")
	ui.DriveKeys()

	// Down to first file
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyDown})
	// Mark it
	ui.Model().fmToggleMark()

	// Check that a.txt is marked
	item := ui.Model().dualPane.Left.Items()[1]
	if !item.selected {
		t.Fatal("a.txt should be marked")
	}

	// Selection should have advanced
	if ui.Model().dualPane.Left.SelectedIndex() != 2 {
		t.Fatalf("after toggle mark, selection should be at 2, got %d", ui.Model().dualPane.Left.SelectedIndex())
	}
}

// --- Snapshot-based integration tests ---

func TestIntegrationInitialFrame(t *testing.T) {
	ui := NewUITest(t, 80, 24)
	ui.DriveKeys() // capture initial frame
	ui.AssertUISnapshot(t, "integration_initial_frame")
}

func TestIntegrationAfterTabToRightPane(t *testing.T) {
	ui := NewUITest(t, 80, 24)
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyTab})
	ui.AssertUISnapshot(t, "integration_right_pane_active")
}

func TestIntegrationFileNavigation3Steps(t *testing.T) {
	ui := NewUITest(t, 80, 24)
	// Start, tab to right, down down
	ui.DriveKeys(
		tea.KeyMsg{Type: tea.KeyTab},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown},
	)
	ui.AssertUISnapshot(t, "integration_after_navigation")
}

// --- Edge cases ---

func TestIntegrationNavigateOffRightEdge(t *testing.T) {
	ui := NewUITest(t, 80, 24)
	ui.Model().dualPane.Left.SetItems([]fileItem{
		{name: ".."},
		{name: "a.txt"},
	})
	ui.Model().dualPane.Left.Select(1)
	ui.DriveKeys()

	// Down at last item should clamp
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyDown})
	if ui.Model().dualPane.Left.SelectedIndex() != 1 {
		t.Fatalf("down at last item should clamp; got %d", ui.Model().dualPane.Left.SelectedIndex())
	}
}

func TestIntegrationNavigateOffLeftEdge(t *testing.T) {
	ui := NewUITest(t, 80, 24)
	ui.Model().dualPane.Left.SetItems([]fileItem{
		{name: ".."},
		{name: "a.txt"},
	})
	ui.Model().dualPane.Left.Select(0)
	ui.DriveKeys()

	// Up at first item should clamp
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyUp})
	if ui.Model().dualPane.Left.SelectedIndex() != 0 {
		t.Fatalf("up at first item should clamp; got %d", ui.Model().dualPane.Left.SelectedIndex())
	}
}

// --- Multi-step workflows ---

func TestIntegrationCompleteWorkflow(t *testing.T) {
	// Setup a real filesystem with nested structure
	dir := t.TempDir()
	proj := filepath.Join(dir, "myproject")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ui := NewUITest(t, 80, 24)
	ui.Model().loadDir(leftPane, dir, "")

	// 1. Initial view
	ui.DriveKeys()
	if !ui.AssertPlainContains(t, "myproject") {
		t.Fatal("should see myproject initially")
	}

	// 2. Down to myproject
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyDown})

	// 3. Enter myproject
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !ui.AssertPlainContains(t, "main.go") {
		t.Fatal("should see main.go after entering myproject")
	}

	// 4. Go back up via ".."
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !ui.AssertPlainContains(t, "myproject") {
		t.Fatal("should see myproject again after going up")
	}

	// 5. Tab to right pane and verify it's independent
	ui.DriveKeys(tea.KeyMsg{Type: tea.KeyTab})
	// Right pane should also be in dir, but we didn't load it
	// This tests that Tab switches context properly
	if ui.Model().activePane != rightPane {
		t.Fatal("activePane should be rightPane after Tab")
	}
}
