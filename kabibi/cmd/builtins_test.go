package cmd

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newBuiltinTestModel(t *testing.T) *AppWidget {
	t.Helper()
	m := initialModel()
	m.width = 120
	m.height = 40
	m.recalculateLayout()
	return m
}

func TestParseBuiltinWordsRejectsShellMetacharacters(t *testing.T) {
	if _, ok := parseBuiltinWords("kabibi files && pwd"); ok {
		t.Fatal("expected shell metacharacters to bypass builtin parsing")
	}
	words, ok := parseBuiltinWords("  kabibi   files off  ")
	if !ok {
		t.Fatal("expected simple kabibi command to parse")
	}
	joined := strings.Join(words, " ")
	if joined != "kabibi files off" {
		t.Fatalf("got %q", joined)
	}
}

func TestHelpBuiltinShowsKabibiCommands(t *testing.T) {
	m := newBuiltinTestModel(t)
	handled, _ := m.handleShellBuiltin("help")
	if !handled {
		t.Fatal("help builtin was not handled")
	}
	joined := strings.Join(m.plume, "\n")
	if !strings.Contains(joined, "kabibi chat") {
		t.Fatalf("help output missing kabibi chat: %q", joined)
	}
	if !strings.Contains(joined, "kabibi files [on|off]") {
		t.Fatalf("help output missing kabibi files: %q", joined)
	}
}

func TestKabibiFilesBuiltinExplicitAndToggle(t *testing.T) {
	m := newBuiltinTestModel(t)
	m.panelsVisible = true

	handled, _ := m.handleShellBuiltin("kabibi files off")
	if !handled {
		t.Fatal("kabibi files off was not handled")
	}
	if m.panelsVisible {
		t.Fatal("expected panelsVisible to be false after kabibi files off")
	}

	handled, _ = m.handleShellBuiltin("kabibi files")
	if !handled {
		t.Fatal("kabibi files toggle was not handled")
	}
	if !m.panelsVisible {
		t.Fatal("expected panelsVisible to toggle back on")
	}
}

func TestHiddenFilePanelsDoNotRender(t *testing.T) {
	m := newBuiltinTestModel(t)
	m.panelsVisible = false
	m.recalculateLayout()

	buf := m.rootCellBuf()
	if got := buf.Get(5, 3).BG; got != colorDarkGray {
		t.Fatalf("hidden panels should not render blue cells; got BG=%v want %v", got, colorDarkGray)
	}
}

func TestCollapsedChatStillRendersPeek(t *testing.T) {
	m := newBuiltinTestModel(t)
	m.chatOpen = false
	m.panelsVisible = true
	m.recalculateLayout()

	if m.chatW == nil {
		t.Fatal("chat widget missing")
	}
	if m.chatW.X <= 0 {
		t.Fatalf("collapsed chat should have a right-side peek, got X=%d", m.chatW.X)
	}
	buf := m.rootCellBuf()
	if got := buf.Get(m.chatW.X, 0).R; got == 0 {
		t.Fatal("collapsed chat peek should still render a border")
	}
}

func TestKabibiChatAndChatOff(t *testing.T) {
	m := newBuiltinTestModel(t)
	m.panelsVisible = false

	handled, _ := m.handleShellBuiltin("kabibi chat")
	if !handled {
		t.Fatal("kabibi chat was not handled")
	}
	if !m.chatOpen || m.activePane != chatPane {
		t.Fatal("expected kabibi chat to open chat pane")
	}
	if !m.panelsVisible {
		t.Fatal("expected kabibi chat to restore file panels")
	}

	m.chatW.Input.SetValue("/chat off")
	handled, _ = m.handleChatSlashCommand(m.chatW.Input.Value())
	if !handled {
		t.Fatal("/chat off was not handled")
	}
	if m.chatOpen {
		t.Fatal("expected /chat off to close chat")
	}
	if m.activePane != leftPane {
		t.Fatal("expected /chat off to return to the left pane")
	}
	if !m.panelsVisible {
		t.Fatal("expected /chat off to leave blue panels visible")
	}

	m.enterChatMode()
	m.chatW.Input.SetValue("/chat exit")
	handled, _ = m.handleChatSlashCommand(m.chatW.Input.Value())
	if !handled {
		t.Fatal("/chat exit was not handled")
	}
	if m.chatOpen {
		t.Fatal("expected /chat exit to close chat")
	}
}

func TestExitBuiltinReturnsQuitCommand(t *testing.T) {
	m := newBuiltinTestModel(t)

	handled, cmd := m.handleShellBuiltin("exit")
	if !handled {
		t.Fatal("exit builtin was not handled")
	}
	if cmd == nil {
		t.Fatal("exit builtin should return a quit command")
	}
	if got := cmd(); got == nil {
		t.Fatal("quit command returned a nil message")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit command should emit tea.QuitMsg")
	}
}

func TestQuitViewRendersPlumeOnly(t *testing.T) {
	m := newBuiltinTestModel(t)
	m.chatOpen = true
	m.panelsVisible = true
	m.activePane = chatPane
	m.plumeW.SetLines([]string{"hello", "goodbye"})

	m.beginExit()
	if !m.quitting {
		t.Fatal("beginExit() did not set quitting state")
	}
	if m.chatOpen || m.panelsVisible || m.activePane != leftPane {
		t.Fatalf("exit state = chatOpen=%v panelsVisible=%v activePane=%v; want chatOpen=false panelsVisible=false activePane=leftPane", m.chatOpen, m.panelsVisible, m.activePane)
	}

	buf := m.rootCellBuf()
	if buf.Width != m.width || buf.Height != m.height {
		t.Fatalf("quit render size = %dx%d, want %dx%d", buf.Width, buf.Height, m.width, m.height)
	}
	if got := buf.Get(0, 0).BG; got != colorDarkGray {
		t.Fatalf("quit render background at 0,0 = %v, want %v", got, colorDarkGray)
	}
}
