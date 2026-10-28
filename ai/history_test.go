package ai

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildHistoryJSONToolCallSequence(t *testing.T) {
	history := []PromptPart{
		{Role: RoleUser, Text: "read a.txt and b.txt"},
		{Role: RoleModel, Text: "Reading.", ToolCalls: []ToolCall{
			{Name: "read_file", Arguments: map[string]any{"path": "a.txt"}},
			{Name: "read_file", Arguments: map[string]any{"path": "b.txt"}},
		}},
		{Role: RoleTool, ToolName: "read_file", ToolResponse: map[string]any{"content": "A"}},
		{Role: RoleTool, ToolName: "read_file", ToolResponse: map[string]any{"content": "B"}},
		{Role: RoleModel, Text: "Done."},
	}

	raw, err := buildHistoryJSON(history)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []map[string]any
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatal(err)
	}

	var roles []string
	for _, m := range msgs {
		roles = append(roles, m["role"].(string))
	}
	if want := []string{"user", "model", "tool", "model"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}

	calls, ok := msgs[1]["tool_calls"].([]any)
	if !ok || len(calls) != 2 {
		t.Fatalf("model turn tool_calls = %v, want 2 calls", msgs[1]["tool_calls"])
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "read_file" || fn["arguments"].(map[string]any)["path"] != "a.txt" {
		t.Fatalf("unexpected first call: %v", calls[0])
	}

	if got := len(msgs[2]["content"].([]any)); got != 2 {
		t.Fatalf("tool message has %d responses, want 2", got)
	}
	if _, has := msgs[3]["tool_calls"]; has {
		t.Fatalf("plain model turn must not carry tool_calls")
	}
}

func TestBuildHistoryJSONToolOnlyModelTurnHasEmptyContent(t *testing.T) {
	raw, err := buildHistoryJSON([]PromptPart{
		{Role: RoleModel, ToolCalls: []ToolCall{{Name: "exec"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []struct {
		Content   []any `json:"content"`
		ToolCalls []any `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatal(err)
	}
	if msgs[0].Content == nil || len(msgs[0].Content) != 0 || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("got %s", raw)
	}
}

func TestFormatPartForEmbeddingAndJSONIsolation(t *testing.T) {
	userPart := PromptPart{
		Role:      RoleUser,
		Text:      "Name a bridge in Paris",
		Embedding: []float32{0.1, 0.2, 0.3},
	}
	if got := FormatPartForEmbedding(userPart); got != "task: search result | text: Name a bridge in Paris" {
		t.Fatalf("user format = %q", got)
	}

	modelToolPart := PromptPart{
		Role: RoleModel,
		Text: "Checking file",
		ToolCalls: []ToolCall{
			{Name: "read_file", RawArguments: json.RawMessage(`{"path":"main.go"}`)},
		},
	}
	if got := FormatPartForEmbedding(modelToolPart); got != "task: search result | text: Checking file\ntool_call read_file({\"path\":\"main.go\"})" {
		t.Fatalf("model tool format = %q", got)
	}

	toolRespPart := PromptPart{
		Role:         RoleTool,
		ToolName:     "read_file",
		ToolResponse: map[string]any{"content": "package main"},
	}
	if got := FormatPartForEmbedding(toolRespPart); got != `task: search result | text: tool_response read_file: {"content":"package main"}` {
		t.Fatalf("tool response format = %q", got)
	}

	msgJSON, err := buildMessageJSON([]PromptPart{userPart})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(msgJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, hasEmbedding := parsed["embedding"]; hasEmbedding {
		t.Fatalf("buildMessageJSON leaked embedding field: %s", msgJSON)
	}
	histJSON, err := buildHistoryJSON([]PromptPart{userPart})
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(histJSON)) {
		t.Fatalf("buildHistoryJSON returned invalid JSON: %s", histJSON)
	}
}

func TestTemporalWireFormattingAndDateFlipIsolation(t *testing.T) {
	day1Type := time.Date(2026, 10, 18, 23, 11, 46, 0, time.UTC)
	day1Send := time.Date(2026, 10, 18, 23, 12, 0, 0, time.UTC) // typed for 14 seconds
	day1TalkStart := time.Date(2026, 10, 18, 23, 12, 56, 0, time.UTC)
	day1TalkEnd := time.Date(2026, 10, 18, 23, 13, 0, 0, time.UTC) // talked for 4 seconds

	day2Type := time.Date(2026, 10, 19, 0, 4, 59, 0, time.UTC)
	day2Send := time.Date(2026, 10, 19, 0, 5, 0, 0, time.UTC) // typed for 1 second (date flipped!)

	history := []PromptPart{
		{
			Role:        RoleUser,
			Text:        "Name a bridge in Paris",
			StartedAt:   day1Type,
			CompletedAt: day1Send,
		},
		{
			Role:        RoleModel,
			Text:        "A famous bridge in Paris is Pont Neuf.",
			StartedAt:   day1TalkStart,
			CompletedAt: day1TalkEnd,
		},
		{
			Role:         RoleTool,
			ToolName:     "read_file",
			ToolResponse: map[string]any{"content": "bridge notes"},
			StartedAt:    day1TalkEnd,
			CompletedAt:  day1TalkEnd.Add(2 * time.Second),
		},
		{
			Role:        RoleUser,
			Text:        "And another one?",
			StartedAt:   day2Type,
			CompletedAt: day2Send,
		},
	}

	// Verify Go struct fields remain pure (no timestamp strings inside Text).
	if history[0].Text != "Name a bridge in Paris" {
		t.Fatalf("user PromptPart.Text mutated: %q", history[0].Text)
	}
	if got := FormatPartForEmbedding(history[0]); got != "task: search result | text: Name a bridge in Paris" {
		t.Fatalf("embedding text polluted by timestamp: %q", got)
	}

	raw, err := buildHistoryJSON(history)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []struct {
		Role    string `json:"role"`
		Content []struct {
			Type     string         `json:"type"`
			Text     string         `json:"text"`
			Name     string         `json:"name"`
			Response map[string]any `json:"response"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("len(msgs) = %d, want 4", len(msgs))
	}

	// 1. First timestamp includes full date + weekday + time
	wantUser1 := "[2026-10-18 Sun 23:12, typed for 14 seconds]\nName a bridge in Paris"
	if got := msgs[0].Content[0].Text; got != wantUser1 {
		t.Fatalf("msg[0] wire text = %q, want %q", got, wantUser1)
	}

	// 2. Second timestamp is on the same date (2026-10-18), so date is skipped
	wantModel1 := "[23:13, talked for 4 seconds]\nA famous bridge in Paris is Pont Neuf."
	if got := msgs[1].Content[0].Text; got != wantModel1 {
		t.Fatalf("msg[1] wire text = %q, want %q", got, wantModel1)
	}

	// 3. Tool timing on the same date (2026-10-18) skips date and leaves original map untouched
	wantToolTiming := "[23:13, ran for 2 seconds]"
	if got := msgs[2].Content[0].Response["timing"]; got != wantToolTiming {
		t.Fatalf("msg[2] tool timing = %v, want %q", got, wantToolTiming)
	}
	if _, mutated := history[2].ToolResponse.(map[string]any)["timing"]; mutated {
		t.Fatal("buildHistoryJSON mutated original PromptPart.ToolResponse map")
	}

	// 4. Fourth timestamp flips date to 2026-10-19, so date is included again
	wantUser2 := "[2026-10-19 Mon 00:05, typed for 1 second]\nAnd another one?"
	if got := msgs[3].Content[0].Text; got != wantUser2 {
		t.Fatalf("msg[3] wire text = %q, want %q", got, wantUser2)
	}
}

func TestRunTurnCapturesUserModelAndToolTimingsPurely(t *testing.T) {
	backend := &scriptedSessionBackend{
		conversation: &scriptedSessionConversation{
			responses: [][]ModelEvent{{TextEvent{Text: "Pont Neuf is the oldest standing bridge across the river Seine in Paris."}}},
		},
	}
	s, err := NewSession(SessionConfig{
		Engine: &Engine{impl: &engineImpl{backend: backend}},
		Tools: map[string]Tool{
			"check_time": {
				Description: "Check time",
				Parameters:  map[string]any{"q": map[string]any{"type": "string"}},
				Execute: func(ctx context.Context, call ToolCall, feedback LogFeedback) (any, error) {
					time.Sleep(5 * time.Millisecond)
					return map[string]any{"status": "ok"}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	typedAt := time.Date(2026, 10, 18, 23, 11, 46, 0, time.Local)
	sentAt := time.Date(2026, 10, 18, 23, 12, 0, 0, time.Local)
	ctx := WithTurnTiming(context.Background(), TurnTiming{
		TypedAt:     typedAt,
		SubmittedAt: sentAt,
	})
	if err := s.RunTurn(ctx, "Name a bridge in Paris"); err != nil {
		t.Fatal(err)
	}

	if len(s.CanonicalHistory) != 2 {
		t.Fatalf("len(CanonicalHistory) = %d, want 2", len(s.CanonicalHistory))
	}
	u := s.CanonicalHistory[0]
	m := s.CanonicalHistory[1]
	if u.Text != "Name a bridge in Paris" {
		t.Fatalf("stored user text = %q, want pure text without timestamp", u.Text)
	}
	if !u.StartedAt.Equal(typedAt) || !u.CompletedAt.Equal(sentAt) || u.Duration != 14*time.Second {
		t.Fatalf("user timings = (%v, %v, %v), want (%v, %v, 14s)", u.StartedAt, u.CompletedAt, u.Duration, typedAt, sentAt)
	}
	if strings.Contains(m.Text, "talked for") || strings.Contains(m.Text, "[") {
		t.Fatalf("stored model text = %q, want pure text without timestamp", m.Text)
	}
	if m.StartedAt.IsZero() || m.CompletedAt.IsZero() || m.CompletedAt.Before(m.StartedAt) {
		t.Fatalf("model timings not recorded: start=%v end=%v", m.StartedAt, m.CompletedAt)
	}
	if len(backend.conversation.sends) != 1 || len(backend.conversation.sends[0]) != 1 || !backend.conversation.sends[0][0].CompletedAt.Equal(sentAt) {
		t.Fatalf("initial model input did not carry timed user PromptPart: %#v", backend.conversation.sends)
	}
	wire, err := buildMessageJSON(backend.conversation.sends[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wire, "[2026-10-18 Sun 23:12, typed for 14 seconds]") {
		t.Fatalf("initial model input lacks user timing header: %s", wire)
	}
}

func TestFormatEnvironmentContextIncludesPlatformModelAndSize(t *testing.T) {
	ctxStr := FormatEnvironmentContext(EnvironmentConfig{
		ModelPath: "/cache/gemma-4-E2B-it.litertlm",
		ModelSize: 2038755678,
		ModelInfo: &ModelInfo{
			MaxContextTokens:        32768,
			IsDynamicContext:        true,
			SupportsThinking:        true,
			SupportsFunctionCalling: true,
		},
		Backend:     "gpu",
		MaxTokens:   8192,
		HasEmbedder: true,
		WorkingDir:  "/workspace/rustica",
	})
	for _, want := range []string{
		"Environment:",
		"Platform:",
		"gemma-4-E2B-it.litertlm",
		"1.90 GB / 2038755678 bytes",
		"backend: gpu",
		"context: 8192/32768 tokens, dynamic",
		"thinking: true",
		"tools: true",
		"rag_embedder: EmbeddingGemma-2",
		"Working Directory: /workspace/rustica",
	} {
		if !strings.Contains(ctxStr, want) {
			t.Fatalf("FormatEnvironmentContext missing %q in:\n%s", want, ctxStr)
		}
	}
}
