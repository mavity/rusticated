package ai

import (
	"encoding/json"
	"reflect"
	"testing"
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
		CognitiveTrace: &CognitiveTrace{
			Goal:            "Identify a bridge in Paris",
			SynthesizedPlan: "Answer directly with Pont Neuf",
		},
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
	if _, hasTrace := parsed["cognitive_trace"]; hasTrace || bytesContainsFold(msgJSON, "SynthesizedPlan") {
		t.Fatalf("buildMessageJSON leaked CognitiveTrace field: %s", msgJSON)
	}

	histJSON, err := buildHistoryJSON([]PromptPart{userPart})
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainsFold(histJSON, "SynthesizedPlan") || bytesContainsFold(histJSON, "Identify a bridge") {
		t.Fatalf("buildHistoryJSON leaked CognitiveTrace field: %s", histJSON)
	}
}

func bytesContainsFold(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		json.Valid([]byte(haystack)) &&
		containsSubstr(haystack, needle)
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
