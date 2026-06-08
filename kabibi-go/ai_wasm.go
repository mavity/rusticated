//go:build wasm

package main

import tea "github.com/charmbracelet/bubbletea"

func IsAISupported() bool {
	return false
}

func (m *model) runAIInference(userInput string) tea.Cmd {
	return func() tea.Msg {
		return aiDoneMsg{err: nil}
	}
}
