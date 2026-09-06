package main

import (
	"strings"
	"testing"
)

func newBuiltinTestModel(t *testing.T) model {
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

	m.chatInput.SetValue("/chat off")
	handled, _ = m.handleChatSlashCommand(m.chatInput.Value())
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
	m.chatInput.SetValue("/chat exit")
	handled, _ = m.handleChatSlashCommand(m.chatInput.Value())
	if !handled {
		t.Fatal("/chat exit was not handled")
	}
	if m.chatOpen {
		t.Fatal("expected /chat exit to close chat")
	}
}
