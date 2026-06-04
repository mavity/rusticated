package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()
		switch key {
		case "ctrl+c", "esc":
			return m, tea.Quit
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
					m.chatLines = append(m.chatLines, "AI: I am a mock AI. You said: "+input)
					m.chatView.SetContent(strings.Join(m.chatLines, "\n"))
					m.chatInput.Reset()
					m.chatView.GotoBottom()
				}
			} else {
				input := m.shellInput.Value()
				if input != "" {
					m.plume = append(m.plume, "$ "+input)
					m.plume = append(m.plume, "Command executed (mock).")
					m.shellInput.Reset()
					return m, nil
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
						newPath := filepath.Join(*curDir, item.name)
						m.loadDir(p, newPath)
					} else {
						// Mock shell execution for files
						m.plume = append(m.plume, fmt.Sprintf("$ run %s", item.name))
						m.plume = append(m.plume, fmt.Sprintf("Executed %s successfully.", item.name))
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

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalculateLayout()
	}

	return m, nil
}
