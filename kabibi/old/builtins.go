package app

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func shellHelpLines() []string {
	return []string{
		"Kabibi shell help:",
		"  help                  show this help",
		"  help kabibi           show kabibi command help",
		"  exit                  quit kabibi",
		"  cd <dir>              change the shell and active panel directory",
		"  any other command     run through the embedded shell",
		"",
		"Kabibi commands:",
		"  kabibi chat           open files + chat",
		"  kabibi files [on|off] toggle the blue file panels",
		"  kabibi copy | cp      open the copy dialog (F5)",
		"  kabibi move | mv      open the move dialog (F6)",
		"  kabibi delete         delete selected item(s) (F8)",
		"  kabibi help           show all kabibi subcommands",
	}
}

func kabibiHelpLines() []string {
	return []string{
		"kabibi builtin commands:",
		"  kabibi help",
		"  kabibi chat           open files + chat",
		"  kabibi chat off       leave chat and return to blue panels",
		"  kabibi files          toggle the blue file panels",
		"  kabibi files on       show the blue file panels",
		"  kabibi files off      hide the blue file panels",
		"  kabibi copy           synonym: kabibi cp",
		"  kabibi move           synonym: kabibi mv",
		"  kabibi delete         synonyms: kabibi remove, del, rm",
		"  kabibi rename         open the rename dialog (F2)",
		"  kabibi edit           open the editor for the selected file (F4)",
		"  kabibi mkdir          create a directory (F7)",
		"  kabibi mark           toggle mark on the selected item",
		"",
		"Inside chat:",
		"  /chat off             leave chat without Esc or function keys",
		"  /chat exit            synonym for /chat off",
	}
}

func parseBuiltinWords(input string) ([]string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, false
	}
	if strings.ContainsAny(trimmed, "|&;<>`$(){}[]\"'") {
		return nil, false
	}
	return strings.Fields(trimmed), true
}

func (m *OldAppWidget_BubbleTea) echoBuiltin(input string, lines ...string) tea.Cmd {
	plume := []string{m.shellW.Prompt + strings.TrimSpace(input)}
	plume = append(plume, lines...)
	return m.AddPlume(plume...)
}

func (m *OldAppWidget_BubbleTea) enterChatMode() {
	m.panelsVisible = true
	m.chatOpen = true
	m.activePane = chatPane
	m.lastTab = time.Time{}
	m.chatW.Input.Focus()
	m.shellW.Blur()
	m.recalculateLayout()
}

func (m *OldAppWidget_BubbleTea) leaveChatMode() {
	m.chatOpen = false
	m.panelsVisible = true
	m.activePane = leftPane
	m.lastTab = time.Time{}
	m.runner.Dir = m.dualPane.Left.Dir()
	m.refreshPrompt()
	m.chatW.Input.Blur()
	m.shellW.Focus()
	m.recalculateLayout()
	m.dualPane.SetActivePaneBridge(0)
}

func (m *OldAppWidget_BubbleTea) setFilesVisible(on bool) {
	m.panelsVisible = on
	m.recalculateLayout()
}

func (m *OldAppWidget_BubbleTea) beginExit() {
	m.quitting = true
	m.chatOpen = false
	m.panelsVisible = false
	m.activePane = leftPane
	m.mode = modeBrowser
	m.lastTab = time.Time{}
	if m.shellW != nil {
		m.shellW.Blur()
	}
	if m.chatW != nil {
		m.chatW.Input.Blur()
	}
	if m.dualPane != nil {
		m.dualPane.SetActivePaneBridge(0)
	}
	if m.plumeW != nil && m.width > 0 && m.height > 0 {
		m.plumeW.Layout(Rect{X: 0, Y: 0, W: m.width, H: m.height})
	}
}

func (m *OldAppWidget_BubbleTea) handleChatSlashCommand(input string) (bool, tea.Cmd) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "/chat off", "/chat exit":
		m.chatW.Input.Reset()
		m.leaveChatMode()
		return true, m.AddPlume("Left chat. Blue panels restored.")
	default:
		return false, nil
	}
}

func (m *OldAppWidget_BubbleTea) currentBrowserList() *FilePaneWidget {
	return m.activePaneWidget()
}

func (m *OldAppWidget_BubbleTea) selectedBrowserItem() (fileItem, bool) {
	w := m.activePaneWidget()
	if w == nil {
		return fileItem{}, false
	}
	return w.SelectedItem()
}

func (m *OldAppWidget_BubbleTea) hasTransferSelection() bool {
	w := m.activePaneWidget()
	if w == nil {
		return false
	}
	for _, fi := range w.Items() {
		if fi.selected && fi.name != ".." {
			return true
		}
	}
	fi, ok := w.SelectedItem()
	return ok && fi.name != ".."
}

func (m *OldAppWidget_BubbleTea) handleShellBuiltin(input string) (bool, tea.Cmd) {
	words, ok := parseBuiltinWords(input)
	if !ok || len(words) == 0 {
		return false, nil
	}

	head := strings.ToLower(words[0])
	switch head {
	case "help":
		if len(words) == 1 {
			return true, m.echoBuiltin(input, shellHelpLines()...)
		}
		if len(words) == 2 && strings.EqualFold(words[1], "kabibi") {
			return true, m.echoBuiltin(input, kabibiHelpLines()...)
		}
		return false, nil
	case "exit", "quit":
		m.beginExit()
		return true, func() tea.Msg { return tea.Quit() }
	case "kabibi":
		return true, m.handleKabibiBuiltin(input, words[1:])
	default:
		return false, nil
	}
}

func (m *OldAppWidget_BubbleTea) handleKabibiBuiltin(input string, args []string) tea.Cmd {
	if len(args) == 0 {
		return m.echoBuiltin(input, kabibiHelpLines()...)
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "help":
		return m.echoBuiltin(input, kabibiHelpLines()...)
	case "chat":
		if len(args) > 1 && strings.EqualFold(args[1], "off") {
			m.leaveChatMode()
			return m.echoBuiltin(input, "Chat hidden. Showing the blue panels.")
		}
		if len(args) > 1 && !strings.EqualFold(args[1], "on") {
			return m.echoBuiltin(input, "usage: kabibi chat [off]")
		}
		m.enterChatMode()
		return m.echoBuiltin(input, "Chat opened. Type /chat off or /chat exit inside chat to leave it.")
	case "files":
		if len(args) == 1 {
			m.setFilesVisible(!m.panelsVisible)
			state := "off"
			if m.panelsVisible {
				state = "on"
			}
			return m.echoBuiltin(input, fmt.Sprintf("Blue file panels %s.", state))
		}
		switch strings.ToLower(args[1]) {
		case "on":
			m.setFilesVisible(true)
			return m.echoBuiltin(input, "Blue file panels on.")
		case "off":
			m.setFilesVisible(false)
			return m.echoBuiltin(input, "Blue file panels off.")
		default:
			return m.echoBuiltin(input, "usage: kabibi files [on|off]")
		}
	case "copy", "cp":
		if !m.hasTransferSelection() {
			return m.echoBuiltin(input, "Nothing selected to copy.")
		}
		m.fmTransfer(opCopy)
		return m.echoBuiltin(input, "Copy dialog opened.")
	case "move", "mv":
		if !m.hasTransferSelection() {
			return m.echoBuiltin(input, "Nothing selected to move.")
		}
		m.fmTransfer(opMove)
		return m.echoBuiltin(input, "Move dialog opened.")
	case "delete", "remove", "del", "rm":
		if !m.hasTransferSelection() {
			return m.echoBuiltin(input, "Nothing selected to delete.")
		}
		m.fmDelete()
		return m.echoBuiltin(input, "Delete confirmation opened.")
	case "rename":
		fi, ok := m.selectedBrowserItem()
		if !ok || fi.name == ".." {
			return m.echoBuiltin(input, "Select one item to rename.")
		}
		m.fmRename()
		return m.echoBuiltin(input, "Rename dialog opened.")
	case "mkdir", "md":
		if m.currentBrowserList() == nil {
			return m.echoBuiltin(input, "No active file pane.")
		}
		m.fmMkdir()
		return m.echoBuiltin(input, "Create-directory dialog opened.")
	case "mark":
		fi, ok := m.selectedBrowserItem()
		if !ok {
			return m.echoBuiltin(input, "No item selected.")
		}
		m.fmToggleMark()
		state := "unmarked"
		if fi.selected {
			state = "marked"
		}
		return m.echoBuiltin(input, fmt.Sprintf("Item %s.", state))
	case "edit":
		fi, ok := m.selectedBrowserItem()
		if !ok || fi.isDir {
			return m.echoBuiltin(input, "Select a file to edit.")
		}
		m.fmEdit()
		return m.echoBuiltin(input, "Editor opened.")
	default:
		return m.echoBuiltin(input, fmt.Sprintf("Unknown kabibi command: %s. Try 'kabibi help'.", sub))
	}
}
