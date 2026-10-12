package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

type engineImpl struct {
	backend engineBackend
}

func resolveLMBackendCandidates(backend string) []string {
	if backend != "" && backend != "auto" {
		if backend == "cpu" {
			return []string{"cpu"}
		}
		return []string{backend, "cpu"}
	}
	if env := os.Getenv("LITERTLM_BACKEND"); env != "" {
		if env == "cpu" {
			return []string{"cpu"}
		}
		return []string{env, "cpu"}
	}
	return []string{"gpu", "npu", "cpu"}
}

func newEngineImpl(libPath, modelPath, backend string, maxTokens int) (*engineImpl, error) {
	var lastErr error
	for _, cand := range resolveLMBackendCandidates(backend) {
		b, err := openBackend(libPath, modelPath, cand, maxTokens)
		if err == nil && b != nil {
			return &engineImpl{backend: b}, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("failed to initialize LiteRT-LM engine")
}

func loadModelInfoImpl(libPath, modelPath string) (*ModelInfo, error) {
	return inspectModel(libPath, modelPath)
}

func (e *engineImpl) Close() {
	if e != nil && e.backend != nil {
		_ = e.backend.Close()
	}
}

func (e *engineImpl) NewConversationWithConfig(cfg ConversationConfig) (*convImpl, error) {
	var systemJSON string
	if cfg.SystemMessage != "" {
		sysObj := map[string]string{
			"type": "text",
			"text": cfg.SystemMessage,
		}
		if data, err := json.Marshal(sysObj); err == nil {
			systemJSON = string(data)
		}
	}

	var toolsJSON string
	if len(cfg.Tools) > 0 {
		if data, err := json.Marshal(cfg.Tools); err == nil {
			toolsJSON = string(data)
		}
	}

	var historyJSON string
	if len(cfg.History) > 0 {
		if data, err := buildHistoryJSON(cfg.History); err == nil {
			historyJSON = data
		}
	}

	cBackend, err := e.backend.NewConversation(systemJSON, toolsJSON, historyJSON)
	if err != nil {
		return nil, err
	}
	return &convImpl{backend: cBackend}, nil
}

// buildHistoryJSON encodes seeded history as a LiteRT-LM message array. Tool
// responses are grouped into one message per consecutive run, mirroring how
// buildMessageJSON sends them live, and a model turn's ToolCalls are emitted
// as an OpenAI-style "tool_calls" array.
func buildHistoryJSON(history []PromptPart) (string, error) {
	var messages []map[string]any
	for i := 0; i < len(history); i++ {
		part := history[i]
		content := []any{}
		msg := map[string]any{}
		role := string(part.Role)
		if role == "" {
			role = "user"
		}
		if part.Role == RoleTool {
			for ; i < len(history) && history[i].Role == RoleTool; i++ {
				resp := history[i].ToolResponse
				if resp == nil {
					resp = history[i].Text
				}
				content = append(content, map[string]any{
					"type":     "tool_response",
					"name":     history[i].ToolName,
					"response": resp,
				})
			}
			i--
		} else {
			if part.ImageBase64 != "" {
				content = append(content, map[string]string{"type": "image", "blob": part.ImageBase64})
			}
			if part.Text != "" {
				content = append(content, map[string]string{"type": "text", "text": part.Text})
			}
			if len(part.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(part.ToolCalls))
				for _, tc := range part.ToolCalls {
					args := tc.Arguments
					if args == nil {
						args = map[string]any{}
					}
					calls = append(calls, map[string]any{
						"type": "function",
						"function": map[string]any{
							"name":      tc.Name,
							"arguments": args,
						},
					})
				}
				msg["tool_calls"] = calls
			}
		}
		msg["role"] = role
		msg["content"] = content
		messages = append(messages, msg)
	}
	data, err := json.Marshal(messages)
	return string(data), err
}

func (e *engineImpl) NewConversation() (*convImpl, error) {
	return e.NewConversationWithConfig(ConversationConfig{})
}

type convImpl struct {
	backend conversationBackend
}

func (c *convImpl) Close() {
	if c != nil && c.backend != nil {
		_ = c.backend.Close()
	}
}

func (c *convImpl) sendStream(ctx context.Context, payloadJSON string, onToken func(string)) error {
	if c == nil || c.backend == nil {
		return fmt.Errorf("conversation is closed")
	}
	return c.backend.SendStream(ctx, payloadJSON, onToken)
}
