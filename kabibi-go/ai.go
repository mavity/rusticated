package main

import (
	"context"
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

// advanceConversation sends the user prompt to the LLM and appends the response to the conversation.
// It reuses the conversation's existing engine and conv handles, enabling persistent multi-turn chat.
// The conversation is modified in place as tokens arrive via onToken callback.
func advanceConversation(ctx context.Context, conv *Conversation, userPrompt string, onToken func(string)) error {
	if conv == nil {
		return nil // Silently skip if conversation not initialized
	}

	// Note: In a stateful conversation, the LLM engine already has history in conv.conv handle.
	// We just send the new user message and let the backend manage context.
	err := runAIPromptStateful(userPrompt, conv, func(token string) {
		if token != "" {
			onToken(token)
		}
	})

	return err
}

func (m *model) advanceChatCmd(userInput string) tea.Cmd {
	msgCh := make(chan tea.Msg, 64)
	m.aiMsgChan = msgCh

	go func() {
		defer close(msgCh)

		err := advanceConversation(context.Background(), m.conversation, userInput, func(token string) {
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
