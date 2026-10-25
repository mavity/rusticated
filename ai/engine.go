package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type engineImpl struct {
	backend     engineBackend
	backendName string
	modelPath   string
	maxTokens   int
}

func resolveLMBackendCandidates(backend string) []string {
	if isBrowserHost() {
		if backend != "" && backend != "auto" {
			return []string{backend}
		}
		return []string{"gpu"}
	}
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
	if maxTokens <= 0 {
		maxTokens = 8192
	}
	var lastErr error
	for _, cand := range resolveLMBackendCandidates(backend) {
		tokensToTry := []int{maxTokens}
		if cand != "cpu" {
			// Before falling back from an accelerator (gpu/npu) to cpu, step down
			// the context window size in notches in case only the larger KV cache
			// allocation exceeded accelerator memory.
			for _, fallbackTok := range []int{16384, 12288, 8192} {
				if fallbackTok < maxTokens {
					tokensToTry = append(tokensToTry, fallbackTok)
				}
			}
		}
		for _, tok := range tokensToTry {
			b, err := openBackend(libPath, modelPath, cand, tok)
			if err == nil && b != nil {
				return &engineImpl{
					backend:     b,
					backendName: cand,
					modelPath:   modelPath,
					maxTokens:   tok,
				}, nil
			}
			lastErr = err
		}
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
	cBackend, err := e.backend.NewConversation(cfg)
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
	var lastDate string
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
				content = append(content, map[string]any{
					"type":     "tool_response",
					"name":     history[i].ToolName,
					"response": formatWireToolResponse(history[i], &lastDate),
				})
			}
			i--
		} else {
			if part.ImageBase64 != "" {
				content = append(content, map[string]string{"type": "image", "blob": part.ImageBase64})
			}
			if part.Text != "" || (len(part.ToolCalls) == 0 && (!part.StartedAt.IsZero() || !part.CompletedAt.IsZero())) {
				wireText := formatWirePartText(part, &lastDate)
				if wireText != "" {
					content = append(content, map[string]string{"type": "text", "text": wireText})
				}
			} else if len(part.ToolCalls) > 0 && (!part.StartedAt.IsZero() || !part.CompletedAt.IsZero()) {
				// Advance lastDate even if a tool-only model turn has no text part.
				_ = formatPartTemporalHeader(part, &lastDate)
			}
			if len(part.ToolCalls) > 0 {
				calls := make([]map[string]any, 0, len(part.ToolCalls))
				for _, tc := range part.ToolCalls {
					args := tc.Arguments
					if args == nil {
						args = map[string]any{}
					}
					calls = append(calls, map[string]any{
						"id":   tc.ID,
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

func (c *convImpl) Clone() (*convImpl, error) {
	if c == nil || c.backend == nil {
		return nil, ErrClosed
	}
	cloned, err := c.backend.Clone()
	if err != nil {
		return nil, err
	}
	return &convImpl{backend: cloned}, nil
}

func (c *convImpl) Close() {
	if c != nil && c.backend != nil {
		_ = c.backend.Close()
	}
}

func (c *convImpl) sendStream(ctx context.Context, parts []PromptPart, onEvent func(ModelEvent)) error {
	if c == nil || c.backend == nil {
		return ErrClosed
	}
	return c.backend.SendStream(ctx, parts, onEvent)
}

type embedderImpl struct {
	backend embedderBackend
}

func newEmbedderImpl(libPath, modelPath, backend string) (*embedderImpl, error) {
	var lastErr error
	for _, cand := range resolveLMBackendCandidates(backend) {
		b, err := openEmbedderBackend(libPath, modelPath, cand)
		if err == nil && b != nil {
			return &embedderImpl{backend: b}, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("failed to initialize LiteRT-LM embedder")
}

func (e *embedderImpl) Close() {
	if e != nil && e.backend != nil {
		_ = e.backend.Close()
	}
}

func (e *embedderImpl) Embed(parts []PromptPart) ([]float32, error) {
	if e == nil || e.backend == nil {
		return nil, ErrClosed
	}
	inputs := buildEmbedInputs(parts)
	if len(inputs) == 0 {
		return nil, fmt.Errorf("ai: no embeddable content in PromptParts")
	}
	return e.backend.ComputeEmbedding(inputs)
}

// FormatPartForEmbedding formats a single PromptPart into its canonical text
// representation with the EmbeddingGemma 2 asymmetric task prefix.
func FormatPartForEmbedding(p PromptPart) string {
	var body strings.Builder
	switch p.Role {
	case RoleTool:
		body.WriteString("tool_response ")
		body.WriteString(p.ToolName)
		if p.Explanation != "" {
			body.WriteString(" (")
			body.WriteString(p.Explanation)
			body.WriteString(")")
		}
		if p.ToolResponse != nil {
			if b, err := json.Marshal(p.ToolResponse); err == nil {
				body.WriteString(": ")
				body.Write(b)
			}
		} else if p.Text != "" {
			body.WriteString(": ")
			body.WriteString(p.Text)
		}
	case RoleModel:
		if p.Text != "" {
			body.WriteString(p.Text)
		}
		for _, tc := range p.ToolCalls {
			if body.Len() > 0 {
				body.WriteByte('\n')
			}
			args := string(tc.RawArguments)
			if args == "" && len(tc.Arguments) > 0 {
				if b, err := json.Marshal(tc.Arguments); err == nil {
					args = string(b)
				}
			}
			body.WriteString("tool_call ")
			body.WriteString(tc.Name)
			body.WriteByte('(')
			body.WriteString(args)
			body.WriteByte(')')
		}
	default:
		body.WriteString(p.Text)
	}
	text := strings.TrimSpace(body.String())
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "task: ") {
		return text
	}
	return "task: search result | text: " + text
}

func buildEmbedInputs(parts []PromptPart) []embedInput {
	var inputs []embedInput
	var textParts []string
	for _, p := range parts {
		if p.ImageBase64 != "" {
			if raw, err := base64.StdEncoding.DecodeString(p.ImageBase64); err == nil && len(raw) > 0 {
				inputs = append(inputs, embedInput{
					dataType: inputDataTypeImage,
					data:     raw,
				})
			}
		}
		if formatted := FormatPartForEmbedding(p); formatted != "" {
			textParts = append(textParts, formatted)
		}
	}
	if len(textParts) > 0 {
		combined := strings.Join(textParts, "\n")
		inputs = append(inputs, embedInput{
			dataType: inputDataTypeText,
			data:     []byte(combined),
		})
	}
	return inputs
}
