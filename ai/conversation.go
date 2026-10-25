package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sync/atomic"
	"time"
)

// ErrRepetitionLoop is yielded when the model falls into a repetitive generation loop.
var ErrRepetitionLoop = errors.New("ai: model stuck in repetitive generation loop")

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
	ToolCallID   string
	Explanation  string
	ToolResponse any
	ToolCalls    []ToolCall
	Embedding    []float32
	StartedAt    time.Time     `json:"-"`
	CompletedAt  time.Time     `json:"-"`
	Duration     time.Duration `json:"-"`
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
	ID           string          `json:"id,omitempty"`
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

// SendStream sends a message and returns an iterator over the generated
// ModelEvents (TextEvent, ToolCallEvent). Events are yielded as (event, nil).
// A failure, including one before generation starts, is yielded once as
// (nil, err) and ends the iteration. A closed conversation yields
// (nil, ErrClosed); so does closing it while the stream is running.
//
// The loop body runs directly on the native runtime's callback thread, in
// generation order, so a slow body delays the chunks after it, and a panic in
// it is fatal. Breaking out of the loop, like cancelling ctx, cancels the
// native generation and invalidates the conversation for good: it must be
// replaced (LiteRT-LM leaves a cancelled conversation unusable). The iterator
// returns without waiting for the native runtime to finish unwinding.
func (c *Conversation) SendStream(ctx context.Context, parts []PromptPart) iter.Seq2[ModelEvent, error] {
	return func(yield func(ModelEvent, error) bool) {
		if c == nil || c.impl == nil {
			yield(nil, ErrClosed)
			return
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// stopped and looped are written on the callback thread and read on this goroutine.
		var stopped atomic.Bool
		var looped atomic.Bool
		var detector loopDetector
		err := c.impl.sendStream(ctx, parts, func(event ModelEvent) {
			if stopped.Load() {
				return
			}
			switch e := event.(type) {
			case TextEvent:
				if detector.feedText(e.Text) {
					looped.Store(true)
					stopped.Store(true)
					cancel()
					return
				}
			case ToolCallEvent:
				if detector.feedToolCalls(e.ToolCalls) {
					looped.Store(true)
					stopped.Store(true)
					cancel()
					return
				}
			}
			if !yield(event, nil) {
				stopped.Store(true)
				cancel()
			}
		})
		if looped.Load() {
			c.Close()
			yield(nil, ErrRepetitionLoop)
			return
		}
		if stopped.Load() {
			c.Close()
			return
		}
		if err != nil {
			yield(nil, err)
		}
	}
}

type loopDetector struct {
	buf       []byte
	lastTool  string
	toolCount int
}

const (
	loopWindowMax             = 1024
	minRepeatSpan             = 64
	entropyWindowSize         = 384
	maxSummaryExcerpt         = 4096
	shortRepeatMinSpan        = 128
	shortRepeatMinRepetitions = 8
	shortRepeatMaxPeriod      = 16
)

func (d *loopDetector) feedText(s string) bool {
	d.buf = append(d.buf, s...)
	if len(d.buf) > loopWindowMax {
		d.buf = d.buf[len(d.buf)-loopWindowMax:]
	}
	n := len(d.buf)

	// 1. Short-period tandem repeat check uses a higher threshold because
	// repeated short formatting fragments can be legitimate output.
	if n >= shortRepeatMinSpan {
		for l := 1; l <= shortRepeatMaxPeriod && l*shortRepeatMinRepetitions <= n; l++ {
			reps := shortRepeatMinSpan / l
			if shortRepeatMinSpan%l != 0 {
				reps++
			}
			if reps < shortRepeatMinRepetitions {
				reps = shortRepeatMinRepetitions
			}
			span := l * reps
			if span > n {
				continue
			}
			tail := d.buf[n-span:]
			unit := tail[span-l:]
			match := true
			for r := 0; r < reps-1; r++ {
				if !bytes.Equal(tail[r*l:(r+1)*l], unit) {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}

	if n < minRepeatSpan {
		return false
	}

	// 2. General tandem repeat check: catches longer phrase/sentence loops and
	// interleaved AB-AB-AB for periods L up to n/3.
	maxL := n / 3
	for l := 3; l <= maxL; l++ {
		reps := minRepeatSpan / l
		if reps < 3 {
			reps = 3
		}
		span := l * reps
		if span > n {
			continue
		}
		tail := d.buf[n-span:]
		unit := tail[span-l:]
		match := true
		for r := 0; r < reps-1; r++ {
			if !bytes.Equal(tail[r*l:(r+1)*l], unit) {
				match = false
				break
			}
		}
		if match && !hasShorterPeriod(unit) {
			return true
		}
	}

	// 2. Fuzzy / interleaved repetition via sliding-window shingle uniqueness ratio.
	if n >= entropyWindowSize {
		win := d.buf[n-entropyWindowSize:]
		if lowComplexity(win) {
			return true
		}
	}
	return false
}

func hasShorterPeriod(unit []byte) bool {
	maxPeriod := min(shortRepeatMaxPeriod, len(unit)-1)
	for period := 1; period <= maxPeriod; period++ {
		if len(unit)%period != 0 {
			continue
		}
		repeated := true
		for i := period; i < len(unit); i++ {
			if unit[i] != unit[i%period] {
				repeated = false
				break
			}
		}
		if repeated {
			return true
		}
	}
	return false
}

func (d *loopDetector) feedToolCalls(calls []ToolCall) bool {
	for _, call := range calls {
		sig := call.Name + ":" + string(call.RawArguments)
		if sig == d.lastTool {
			d.toolCount++
			if d.toolCount >= 3 {
				return true
			}
		} else {
			d.lastTool = sig
			d.toolCount = 1
		}
	}
	return false
}

// lowComplexity measures what fraction of win consists of novel 8-byte shingles.
// Genuine prose/code/tables stay > 0.30; fuzzy interleaved loops drop < 0.18.
func lowComplexity(win []byte) bool {
	const k = 8
	var seen [128]uint64
	unique := 0
	total := len(win) - k + 1
	for i := 0; i < total; i++ {
		h := uint64(14695981039346656037)
		for j := 0; j < k; j++ {
			h ^= uint64(win[i+j])
			h *= 1099511628211
		}
		slot := h & 127
		if seen[slot] != h {
			seen[slot] = h
			unique++
		}
	}
	return unique*100/total < 18
}

// Close releases the conversation. It is safe to call multiple times.
func (c *Conversation) Close() {
	if c != nil && c.impl != nil {
		c.impl.Close()
	}
}

// formatSeconds formats a whole-second duration count as "1 second" or "N seconds".
func formatSeconds(secs int) string {
	if secs < 0 {
		secs = 0
	}
	if secs == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", secs)
}

// formatPartTemporalHeader formats a PromptPart's timing metadata into the bracketed
// header shown to the AI (e.g. "[2026-10-18 Sun 23:12, typed for 14 seconds]" on the
// first timestamp or date flip, and "[23:12, typed for 14 seconds]" on the same date).
// Returns "" when neither StartedAt nor CompletedAt is set.
func formatPartTemporalHeader(p PromptPart, lastDate *string) string {
	ts := p.CompletedAt
	if ts.IsZero() {
		ts = p.StartedAt
	}
	if ts.IsZero() {
		return ""
	}

	dateKey := ts.Format("2006-01-02")
	var timeStr string
	if lastDate == nil || *lastDate == "" || *lastDate != dateKey {
		timeStr = ts.Format("2006-01-02 Mon 15:04")
		if lastDate != nil {
			*lastDate = dateKey
		}
	} else {
		timeStr = ts.Format("15:04")
	}

	dur := p.Duration
	if dur <= 0 && !p.StartedAt.IsZero() && !p.CompletedAt.IsZero() && p.CompletedAt.After(p.StartedAt) {
		dur = p.CompletedAt.Sub(p.StartedAt)
	}
	if dur < 0 {
		dur = 0
	}
	secs := int(dur.Round(time.Second).Seconds())

	verb := "typed"
	switch p.Role {
	case RoleModel:
		verb = "talked"
	case RoleTool:
		verb = "ran"
	}
	return fmt.Sprintf("[%s, %s for %s]", timeStr, verb, formatSeconds(secs))
}

func formatWirePartText(p PromptPart, lastDate *string) string {
	hdr := formatPartTemporalHeader(p, lastDate)
	if hdr == "" {
		return p.Text
	}
	if p.Text == "" {
		return hdr
	}
	return hdr + "\n" + p.Text
}

func formatWireToolResponse(p PromptPart, lastDate *string) any {
	resp := p.ToolResponse
	if resp == nil {
		resp = p.Text
	}
	hdr := formatPartTemporalHeader(p, lastDate)
	if hdr == "" {
		return resp
	}
	switch v := resp.(type) {
	case map[string]any:
		cp := make(map[string]any, len(v)+1)
		for k, val := range v {
			cp[k] = val
		}
		cp["timing"] = hdr
		return cp
	case string:
		if v == "" {
			return hdr
		}
		return hdr + "\n" + v
	default:
		return map[string]any{
			"timing": hdr,
			"result": resp,
		}
	}
}

// buildMessageJSON encodes a slice of PromptParts into a single LiteRT-LM
// message. Per the litert-lm message schema, content is an array of typed
// parts: images use {"type":"image","blob":"<base64>"}, text uses
// {"type":"text","text":"..."}, and tool responses use
// {"type":"tool_response","name":"...","response":...}.
func buildMessageJSON(parts []PromptPart) (string, error) {
	var lastDate string
	return buildMessageJSONWithDate(parts, &lastDate)
}

func buildMessageJSONWithDate(parts []PromptPart, lastDate *string) (string, error) {
	role := RoleUser
	if len(parts) > 0 && parts[0].Role != "" {
		role = parts[0].Role
	}
	content := make([]any, 0, len(parts))
	for _, p := range parts {
		if p.Role == RoleTool || role == RoleTool {
			content = append(content, map[string]any{
				"type":         "tool_response",
				"name":         p.ToolName,
				"tool_call_id": p.ToolCallID,
				"response":     formatWireToolResponse(p, lastDate),
			})
			continue
		}
		if p.ImageBase64 != "" {
			content = append(content, map[string]string{"type": "image", "blob": p.ImageBase64})
		}
		if p.Text != "" || (!p.StartedAt.IsZero() || !p.CompletedAt.IsZero()) {
			if p.Text != "" || len(p.ToolCalls) == 0 {
				wireText := formatWirePartText(p, lastDate)
				if wireText != "" {
					content = append(content, map[string]string{"type": "text", "text": wireText})
				}
			}
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
			ID       string `json:"id"`
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
				ID:           tc.ID,
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
