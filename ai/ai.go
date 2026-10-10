// Package ai is a clean, idiomatic Go API for local LiteRT-LM model
// interaction. It fully encapsulates purego, C-pointers, WASI pointer math,
// and the LiteRT JSON schema, exposing high-level LLM (Engine/Conversation)
// and generic vision (Interpreter) primitives.
package ai

import (
	"context"
	"encoding/json"
)

// Role identifies the author of a prompt part.
type Role string

const (
	RoleUser   Role = "user"
	RoleModel  Role = "model"
	RoleSystem Role = "system"
	RoleTool   Role = "tool"
)

// PromptPart is a single typed fragment of a multimodal message. Set Text for
// a text part, ImageBase64 for an image part, or ToolName and ToolResponse
// when replying to a tool call with RoleTool.
type PromptPart struct {
	Role         Role
	Text         string
	ImageBase64  string
	ToolName     string
	ToolResponse any
}

// ModelEvent represents an item emitted during model generation.
type ModelEvent interface {
	isModelEvent()
}

// TextEvent is emitted when incremental text tokens are generated.
type TextEvent struct {
	Text string
}

func (TextEvent) isModelEvent() {}

// ToolCall represents a single function call requested by the model.
type ToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolCallEvent is emitted when the model calls one or more functions.
type ToolCallEvent struct {
	ToolCalls []ToolCall
}

func (ToolCallEvent) isModelEvent() {}

// ErrorEvent is emitted if generation fails during streaming.
type ErrorEvent struct {
	Err error
}

func (ErrorEvent) isModelEvent() {}

// ModelInfo provides introspection into a LiteRT-LM file's capabilities.
type ModelInfo struct {
	MaxContextTokens        int
	IsDynamicContext        bool
	SupportsThinking        bool
	SupportsFunctionCalling bool
	SupportedBackends       []string
}

// LoadModelInfo inspects a .litertlm file and extracts its capabilities,
// context limits, and supported backends without initializing the full engine.
func LoadModelInfo(libPath, modelPath string) (*ModelInfo, error) {
	return loadModelInfoImpl(libPath, modelPath)
}

// ConversationConfig passes parameters to the engine when creating a conversation.
type ConversationConfig struct {
	SystemMessage string
	Tools         []map[string]any // OpenAI-style tool declarations
	History       []PromptPart     // Initial turns for seeding history
}

// Engine is a loaded LiteRT-LM model ready to spawn conversations.
type Engine struct {
	impl *engineImpl
}

// NewEngine loads the native runtime library and model, selecting a backend
// (e.g. "gpu", "npu", "cpu", "auto" or ""). If maxTokens > 0, it overrides the default KV cache size.
func NewEngine(libPath, modelPath, backend string, maxTokens int) (*Engine, error) {
	impl, err := newEngineImpl(libPath, modelPath, backend, maxTokens)
	if err != nil {
		return nil, err
	}
	return &Engine{impl: impl}, nil
}

// NewConversationWithConfig starts an isolated conversation on the engine with
// custom system instructions, tool definitions, and/or seeded history.
func (e *Engine) NewConversationWithConfig(cfg ConversationConfig) (*Conversation, error) {
	impl, err := e.impl.NewConversationWithConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Conversation{impl: impl}, nil
}

// NewConversation starts an isolated conversation on the engine with default settings.
func (e *Engine) NewConversation() (*Conversation, error) {
	return e.NewConversationWithConfig(ConversationConfig{})
}

// Close releases the engine. It is safe to call multiple times.
func (e *Engine) Close() {
	if e != nil && e.impl != nil {
		e.impl.Close()
	}
}

// Conversation is a single stateful chat session.
type Conversation struct {
	impl *convImpl
}

// Send submits a multimodal message and streams back decoded text tokens via
// onToken. The LiteRT JSON schema is handled internally: callers receive and
// provide only plain text and image bytes.
func (c *Conversation) Send(ctx context.Context, parts []PromptPart, onToken func(string)) error {
	payload, err := buildMessageJSON(parts)
	if err != nil {
		return err
	}
	return c.impl.sendStream(ctx, payload, func(raw string) {
		if onToken != nil {
			onToken(extractTokenText(raw))
		}
	})
}

// SendText is a convenience wrapper for a single user text turn.
func (c *Conversation) SendText(ctx context.Context, text string, onToken func(string)) error {
	return c.Send(ctx, []PromptPart{{Role: RoleUser, Text: text}}, onToken)
}

// SendStream sends a message and returns an asynchronous channel of ModelEvents
// (TextEvent, ToolCallEvent, or ErrorEvent).
func (c *Conversation) SendStream(ctx context.Context, parts []PromptPart) (<-chan ModelEvent, error) {
	payload, err := buildMessageJSON(parts)
	if err != nil {
		return nil, err
	}
	ch := make(chan ModelEvent, 16)
	go func() {
		defer close(ch)
		err := c.impl.sendStream(ctx, payload, func(raw string) {
			parseAndEmitEvents(raw, ch)
		})
		if err != nil {
			ch <- ErrorEvent{Err: err}
		}
	}()
	return ch, nil
}

// Close releases the conversation. It is safe to call multiple times.
func (c *Conversation) Close() {
	if c != nil && c.impl != nil {
		c.impl.Close()
	}
}

// Interpreter is a loaded TFLite model for generic/vision inference.
type Interpreter struct {
	impl *interpreterImpl
}

// NewInterpreter loads the TFLite C runtime library and the given model.
func NewInterpreter(libPath, modelPath string) (*Interpreter, error) {
	impl, err := newInterpreterImpl(libPath, modelPath)
	if err != nil {
		return nil, err
	}
	return &Interpreter{impl: impl}, nil
}

// SetInput copies float32 data into input tensor at index.
func (i *Interpreter) SetInput(index int, data []float32) error {
	return i.impl.SetInput(index, data)
}

// GetOutput returns the float32 data and shape of output tensor at index.
func (i *Interpreter) GetOutput(index int) ([]float32, []int32, error) {
	return i.impl.GetOutput(index)
}

// Invoke runs inference over the currently set inputs.
func (i *Interpreter) Invoke() error {
	return i.impl.Invoke()
}

// Close releases the interpreter. It is safe to call multiple times.
func (i *Interpreter) Close() {
	if i != nil && i.impl != nil {
		i.impl.Close()
	}
}

// buildMessageJSON encodes a slice of PromptParts into a single LiteRT-LM
// message. Per the litert-lm message schema, content is an array of typed
// parts: images use {"type":"image","blob":"<base64>"}, text uses
// {"type":"text","text":"..."}, and tool responses use
// {"type":"tool_response","name":"...","response":...}.
func buildMessageJSON(parts []PromptPart) (string, error) {
	role := RoleUser
	if len(parts) > 0 && parts[0].Role != "" {
		role = parts[0].Role
	}
	content := make([]any, 0, len(parts))
	for _, p := range parts {
		if p.Role == RoleTool || role == RoleTool {
			resp := p.ToolResponse
			if resp == nil {
				resp = p.Text
			}
			content = append(content, map[string]any{
				"type":     "tool_response",
				"name":     p.ToolName,
				"response": resp,
			})
			continue
		}
		if p.ImageBase64 != "" {
			content = append(content, map[string]string{"type": "image", "blob": p.ImageBase64})
		}
		if p.Text != "" {
			content = append(content, map[string]string{"type": "text", "text": p.Text})
		}
	}
	msg := map[string]any{
		"role":    string(role),
		"content": content,
	}
	out, err := json.Marshal(msg)
	return string(out), err
}

// parseAndEmitEvents parses a raw chunk from LiteRT-LM and delivers TextEvent
// and/or ToolCallEvent to ch.
func parseAndEmitEvents(raw string, ch chan<- ModelEvent) {
	var chunk struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		ToolCalls []struct {
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments any    `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}

	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		if raw != "" {
			ch <- TextEvent{Text: raw}
		}
		return
	}

	for _, c := range chunk.Content {
		if c.Text != "" {
			ch <- TextEvent{Text: c.Text}
		}
	}

	if len(chunk.ToolCalls) > 0 {
		var calls []ToolCall
		for _, tc := range chunk.ToolCalls {
			args := make(map[string]any)
			switch a := tc.Function.Arguments.(type) {
			case string:
				_ = json.Unmarshal([]byte(a), &args)
			case map[string]any:
				args = a
			}
			calls = append(calls, ToolCall{
				Name:      tc.Function.Name,
				Arguments: args,
			})
		}
		if len(calls) > 0 {
			ch <- ToolCallEvent{ToolCalls: calls}
		}
	}
}

// extractTokenText parses a raw streaming token emitted by litert_lm and
// returns just its text.
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
