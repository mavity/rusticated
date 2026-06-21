package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type aiTokenMsg string
type aiDoneMsg struct{ err error }
type assetProgressMsg struct {
	Stage   string
	Percent int
	Details string
}
type assetReadyMsg struct {
	Stage string
}
type assetErrorMsg struct {
	Stage string
	err   error
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case aiTokenMsg:
		if len(m.chatLines) > 0 {
			m.chatLines[len(m.chatLines)-1] += string(msg)
			m.syncChatView()
		}
		return m, m.watchAIChanCmd()

	case aiDoneMsg:
		m.isThinking = false
		m.aiMsgChan = nil
		if msg.err != nil {
			m.chatLines = append(m.chatLines, "System Error: "+msg.err.Error())
			m.syncChatView()
		}
		if len(m.pendingPrompts) > 0 {
			next := m.pendingPrompts[0]
			m.pendingPrompts = m.pendingPrompts[1:]
			m.isThinking = true
			return m, m.runAIInference(next)
		}
		return m, nil

	case assetProgressMsg:
		m.isDownloading = true
		switch msg.Stage {
		case "litertlm":
			m.litertDownloadPercent = msg.Percent
			m.litertDownloadDetails = msg.Details
		case "gemma":
			m.gemmaDownloadPercent = msg.Percent
			m.gemmaDownloadDetails = msg.Details
		}
		m.syncChatView()
		return m, m.watchAssetProgressCmd()

	case assetReadyMsg:
		switch msg.Stage {
		case "litertlm":
			m.litertReady = true
			m.litertDownloadPercent = 100
			m.litertDownloadDetails = "ready"
		case "gemma":
			m.gemmaReady = true
			m.gemmaDownloadPercent = 100
			m.gemmaDownloadDetails = "ready"
		}

		allDone := (m.litertReady || strings.HasPrefix(m.litertDownloadDetails, "Error:")) &&
			(m.gemmaReady || strings.HasPrefix(m.gemmaDownloadDetails, "Error:"))

		if allDone {
			if m.litertReady && m.gemmaReady {
				m.isDownloading = false
				m.assetsReady = true
				m.assetProgress = nil
				m.assetDone = nil
				m.syncChatView()
				if len(m.pendingPrompts) > 0 && !m.isThinking {
					next := m.pendingPrompts[0]
					m.pendingPrompts = m.pendingPrompts[1:]
					m.isThinking = true
					return m, m.runAIInference(next)
				}
			}
			return m, nil
		}
		m.syncChatView()
		return m, m.watchAssetProgressCmd()

	case assetErrorMsg:
		switch msg.Stage {
		case "litertlm":
			m.litertDownloadDetails = "Error: " + msg.err.Error()
		case "gemma":
			m.gemmaDownloadDetails = "Error: " + msg.err.Error()
		}
		m.chatLines = append(m.chatLines, fmt.Sprintf("AI runtime error (%s): %v", msg.Stage, msg.err))
		m.syncChatView()

		// Still watch progress if the other one is not done yet.
		// Note: we consider it "done" if ready or if error details are set.
		allDone := (m.litertReady || strings.HasPrefix(m.litertDownloadDetails, "Error:")) &&
			(m.gemmaReady || strings.HasPrefix(m.gemmaDownloadDetails, "Error:"))

		if allDone {
			return m, nil
		}
		return m, m.watchAssetProgressCmd()
		m.syncChatView()
		return m, nil

	case shellResultMsg:
		var plumeLines []string
		plumeLines = append(plumeLines, "$ "+msg.input)
		if len(msg.output) > 0 {
			plumeLines = append(plumeLines, msg.output...)
		}
		if msg.err != nil {
			plumeLines = append(plumeLines, "Error: "+msg.err.Error())
		}
		return m, m.AddPlume(plumeLines...)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalculateLayout()

	case tea.KeyMsg:
		key := msg.String()
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if !m.chatOpen {
				m.quitting = true
				return m, tea.Quit
			}
			m.chatOpen = false
			m.activePane = leftPane
			m.chatInput.Blur()
			m.shellInput.Focus()
			m.recalculateLayout()
			m.updateDelegates()
			return m, nil
		case "tab":
			if m.chatOpen {
				m.chatOpen = false
				m.activePane = leftPane
				m.chatInput.Blur()
				m.shellInput.Focus()
				m.recalculateLayout()
				m.updateDelegates()
				return m, nil
			}

			// Handle double-tab logic (300ms)
			now := time.Now()
			if now.Sub(m.lastTab) < 300*time.Millisecond {
				m.chatOpen = true
				m.activePane = chatPane
				m.chatInput.Focus()
				m.shellInput.Blur()
				m.lastTab = time.Time{} // Reset
				m.recalculateLayout()
				m.updateDelegates()
				return m, nil
			}
			m.lastTab = now

			// Single tab: toggle between left/right pane
			if m.activePane == leftPane {
				m.activePane = rightPane
			} else {
				m.activePane = leftPane
			}
			m.updateDelegates()
		case "enter":
			if m.chatOpen {
				input := m.chatInput.Value()
				if input != "" {
					m.chatLines = append(m.chatLines, "User: "+input)
					m.chatLines = append(m.chatLines, "AI: ")
					m.syncChatView()
					m.chatInput.Reset()
					m.chatView.GotoBottom()

					if !m.assetsReady || m.isThinking {
						m.pendingPrompts = append(m.pendingPrompts, input)
						m.syncChatView()
						return m, nil
					}

					m.isThinking = true
					return m, m.runAIInference(input)
				}
			} else {
				input := m.shellInput.Value()
				if input != "" {
					m.shellInput.Reset()
					return m, m.runShellCommand(input)
				}

				var curList *list.Model
				var curDir *string
				var p pane
				if m.activePane == leftPane {
					curList = &m.leftList
					curDir = &m.leftDir
					p = leftPane
				} else {
					curList = &m.rightList
					curDir = &m.rightDir
					p = rightPane
				}

				if item, ok := curList.SelectedItem().(fileItem); ok {
					if item.isDir {
						if item.name == ".." {
							oldDir := *curDir
							newPath := filepath.Dir(oldDir)
							m.loadDir(p, newPath, filepath.Base(oldDir))
						} else {
							newPath := filepath.Join(*curDir, item.name)
							m.loadDir(p, newPath, "..")
						}
						return m, nil
					} else {
						// Execute the file through the shell
						filePath := filepath.Join(*curDir, item.name)
						// For now, let's just try to echo the path or something safe
						// but actually, we should try to execute it.
						return m, m.runShellCommand(filePath)
					}
				}
			}
		case "up", "down", "left", "right", "pgup", "pgdown", "home", "end":
			// If chat is open, cursor keys go to chat
			if m.chatOpen && m.activePane == chatPane {
				m.chatInput, cmd = m.chatInput.Update(msg)
				return m, cmd
			}

			// Otherwise they move file manager selection
			var l *list.Model
			if m.activePane == leftPane {
				l = &m.leftList
			} else {
				l = &m.rightList
			}

			switch key {
			case "up":
				l.CursorUp()
			case "down":
				l.CursorDown()
			case "left":
				m.moveCursorHorizontal(-1)
			case "right":
				m.moveCursorHorizontal(1)
			case "pgup":
				m.moveCursorHorizontal(-3) // 3 columns
			case "pgdown":
				m.moveCursorHorizontal(3) // 3 columns
			case "home":
				l.Select(0)
			case "end":
				l.Select(len(l.Items()) - 1)
			}
			m.updateDelegates()
		default:
			if m.chatOpen {
				m.chatInput, cmd = m.chatInput.Update(msg)
			} else {
				m.shellInput, cmd = m.shellInput.Update(msg)
			}
			return m, cmd
		}
	}

	return m, nil
}
