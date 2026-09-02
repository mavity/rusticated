package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

// LiteRtLogs buffers the latest in-memory logs captured from the dynamic FFI sink logger.
// The kabibi application can inspect this slice at any time to display logs on-demand in the UI.
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

// libExt returns the native shared library extension for the host OS.
func libExt() string {
	switch HostOS() {
	case "windows":
		return ".dll"
	case "darwin":
		return ".dylib"
	default:
		return ".so"
	}
}

// runAIPrompt runs a stateless one-shot AI completion.
func runAIPrompt(userInput string, onToken func(string)) error {
	// Wrap callback to parse raw JSON tokens into clean text
	origOnToken := onToken
	onToken = func(raw string) {
		origOnToken(extractTokenText(raw))
	}

	cacheDir, err := cacheDirPath()
	if err != nil {
		return err
	}

	modelPath := resolveModelFile(cacheDir, ActiveModelName())
	libDir := filepath.Join(cacheDir, "lib")
	libPath := filepath.Join(libDir, "litert_lm_ext"+libExt())

	// 1. Initialize the Engine
	engine, err := NewLMEngine(libPath, modelPath, "cpu")
	if err != nil {
		return err
	}
	defer engine.Close()

	// 2. Start a Conversation
	conv, err := engine.NewConversation()
	if err != nil {
		return err
	}
	defer conv.Close()

	// 3. Serialize message safely to JSON to avoid injection risks
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payloadBytes, err := json.Marshal(Message{Role: "user", Content: userInput})
	if err != nil {
		return fmt.Errorf("failed to serialize message: %w", err)
	}

	// 4. Send the message stream
	return conv.SendMessageStream(string(payloadBytes), onToken)
}

// runAIPromptStateful drives a persistent multi-turn chat session.
// It stores the opaque handles in the conv Conversation struct for persistence.
func runAIPromptStateful(userInput string, conv *Conversation, onToken func(string)) error {
	if conv == nil {
		return fmt.Errorf("conversation is nil")
	}

	// Wrap callback to parse raw JSON tokens into clean text
	origOnToken := onToken
	onToken = func(raw string) {
		origOnToken(extractTokenText(raw))
	}

	cacheDir, err := cacheDirPath()
	if err != nil {
		return err
	}

	// On first call, lazy-initialize the underlying handles
	if conv.engine == 0 || conv.conv == 0 {
		modelPath := resolveModelFile(cacheDir, ActiveModelName())
		libDir := filepath.Join(cacheDir, "lib")
		libPath := filepath.Join(libDir, "litert_lm_ext"+libExt())

		engine, err := NewLMEngine(libPath, modelPath, "cpu")
		if err != nil {
			return err
		}

		lmConv, err := engine.NewConversation()
		if err != nil {
			engine.Close()
			return err
		}

		conv.engine = engine.RawEngine()
		conv.conv = lmConv.RawConv()
	}

	// Reconstruct the strongly-typed wrapper from the cached handles
	lmConv := NewLMConversationFromHandles(conv.engine, conv.conv)

	// Serialize message safely to JSON to avoid injection risks
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	payloadBytes, err := json.Marshal(Message{Role: "user", Content: userInput})
	if err != nil {
		return fmt.Errorf("failed to serialize message: %w", err)
	}

	return lmConv.SendMessageStream(string(payloadBytes), onToken)
}

// advanceConversation sends the user prompt to the LLM and appends the response to the conversation.
// It reuses the conversation's existing engine and conv handles, enabling persistent multi-turn chat.
// The conversation is modified in place as tokens arrive via onToken callback.
func advanceConversation(ctx context.Context, conv *Conversation, userPrompt string, onToken func(string)) error {
	if conv == nil {
		return nil // Silently skip if conversation not initialized
	}

	err := runAIPromptStateful(userPrompt, conv, func(token string) {
		if token != "" {
			onToken(token)
		}
	})

	return err
}

func (m *model) startStream(userInput string) {
	gen := m.streamGen
	msgIdx := len(m.conversation.Messages) - 1

	go func() {
		err := advanceConversation(context.Background(), m.conversation, userInput, func(token string) {
			if token == "" {
				return
			}
			m.streamMu.Lock()
			if m.streamGen != gen {
				m.streamMu.Unlock()
				return
			}
			m.conversation.Messages[msgIdx].Content += token
			if !m.firstTokenRecv {
				m.firstTokenRecv = true
				m.animStart = time.Now()
			}
			m.streamMu.Unlock()
			AppProgram.Send(aiRepaintMsg{})
		})

		m.streamMu.Lock()
		stale := m.streamGen != gen
		m.streamMu.Unlock()
		if stale {
			return
		}

		AppProgram.Send(aiDoneMsg{err: err})
	}()
}
