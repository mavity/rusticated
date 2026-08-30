package main

import (
	"encoding/json"

	tea "github.com/charmbracelet/bubbletea"
)

// LiteRtLogs buffers the latest in-memory logs captured from the dynamic FFI sink logger.
// The kabibi-go application can inspect this slice at any time to display logs on-demand in the UI.
var LiteRtLogs []string

func IsAISupported() bool {
	return true
}

// extractTokenText parses a LiterTLM JSON token chunk and returns the text content.
// LiterTLM returns chunks like: {"role":"assistant","content":[{"type":"text","text":"Hello"}]}
// This extracts the "text" field from the first content item.
func extractTokenText(raw string) string {
	var msg struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return raw
	}
	if len(msg.Content) > 0 {
		return msg.Content[0].Text
	}
	return raw
}

func (m *model) runAIInference(userInput string) tea.Cmd {
	msgCh := make(chan tea.Msg, 64)
	m.aiMsgChan = msgCh

	go func() {
		defer close(msgCh)

		err := runAIPrompt(userInput, func(token string) {
			if token != "" {
				msgCh <- aiTokenMsg(token)
			}
		})

		if err != nil {
			msgCh <- aiDoneMsg{err: err}
		} else {
			msgCh <- aiDoneMsg{err: nil}
		}
	}()

	return m.watchAIChanCmd()
}
