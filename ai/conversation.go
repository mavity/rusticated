package ai

import (
	"context"
	"encoding/json"
	"iter"
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
// when replying to a tool call with RoleTool. ToolCalls records the calls a
// RoleModel turn made, so that seeded history replays the call/response pairs.
type PromptPart struct {
	Role         Role
	Text         string
	ImageBase64  string
	ToolName     string
	ToolResponse any
	ToolCalls    []ToolCall
}

// ModelEvent represents a valid item emitted during model generation. Failures
// are not events: SendStream reports them as the error of its iterator.
type ModelEvent interface {
	isModelEvent()
}

// TextEvent is emitted when incremental text tokens are generated.
type TextEvent struct {
	Text string
}

func (TextEvent) isModelEvent() {}

// ToolCall represents a single function call requested by the model.
// RawArguments is the arguments object exactly as the runtime emitted it, for
// decoding straight into a typed struct; Arguments is the same data as a map.
type ToolCall struct {
	Name         string          `json:"name"`
	Arguments    map[string]any  `json:"arguments"`
	RawArguments json.RawMessage `json:"-"`
}

// ToolCallEvent is emitted when the model calls one or more functions.
type ToolCallEvent struct {
	ToolCalls []ToolCall
}

func (ToolCallEvent) isModelEvent() {}

// ConversationConfig passes parameters to the engine when creating a conversation.
type ConversationConfig struct {
	SystemMessage string
	Tools         []map[string]any // OpenAI-style tool declarations
	History       []PromptPart     // Initial turns for seeding history
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

// Conversation is a single stateful chat session.
type Conversation struct {
	impl *convImpl
}

// streamItem is one element passed from the native callback thread to the
// goroutine ranging over SendStream: an event, or the terminal error.
type streamItem struct {
	event ModelEvent
	err   error
}

// SendStream sends a message and returns an iterator over the generated
// ModelEvents (TextEvent, ToolCallEvent). Events are yielded as (event, nil).
// A failure, including one before generation starts, is yielded once as
// (nil, err) and ends the iteration.
//
// The iterator does not return until the native generation has finished, even
// if the caller breaks out of the loop early: the remaining events are
// discarded. The conversation is therefore idle, and safe to reuse or Close,
// as soon as the loop exits. The loop body runs on the caller's goroutine, not
// on the native callback thread.
func (c *Conversation) SendStream(ctx context.Context, parts []PromptPart) iter.Seq2[ModelEvent, error] {
	return func(yield func(ModelEvent, error) bool) {
		payload, err := buildMessageJSON(parts)
		if err != nil {
			yield(nil, err)
			return
		}

		ch := make(chan streamItem, 16)
		go func() {
			defer close(ch)
			err := c.impl.sendStream(ctx, payload, func(raw string) {
				for _, event := range parseChunkEvents(raw) {
					ch <- streamItem{event: event}
				}
			})
			if err != nil {
				ch <- streamItem{err: err}
			}
		}()

		// Runs on normal completion (ch already closed), on early break, and if
		// the loop body panics. An unread channel would fill up and block the
		// native callback thread, so it must be drained until generation ends.
		defer func() {
			for range ch {
			}
		}()

		for item := range ch {
			if !yield(item.event, item.err) {
				return
			}
		}
	}
}

// Close releases the conversation. It is safe to call multiple times.
func (c *Conversation) Close() {
	if c != nil && c.impl != nil {
		c.impl.Close()
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

// parseChunkEvents parses a raw chunk from LiteRT-LM into its TextEvents and/or
// ToolCallEvent.
func parseChunkEvents(raw string) []ModelEvent {
	var chunk struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		ToolCalls []struct {
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}

	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		if raw != "" {
			return []ModelEvent{TextEvent{Text: raw}}
		}
		return nil
	}

	var events []ModelEvent
	for _, c := range chunk.Content {
		if c.Text != "" {
			events = append(events, TextEvent{Text: c.Text})
		}
	}

	if len(chunk.ToolCalls) > 0 {
		var calls []ToolCall
		for _, tc := range chunk.ToolCalls {
			raw := tc.Function.Arguments
			// The runtime may deliver the arguments object as a JSON-encoded string.
			if len(raw) > 0 && raw[0] == '"' {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					raw = json.RawMessage(s)
				}
			}
			args := make(map[string]any)
			_ = json.Unmarshal(raw, &args)
			calls = append(calls, ToolCall{
				Name:         tc.Function.Name,
				Arguments:    args,
				RawArguments: raw,
			})
		}
		if len(calls) > 0 {
			events = append(events, ToolCallEvent{ToolCalls: calls})
		}
	}
	return events
}
