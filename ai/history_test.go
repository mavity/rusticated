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
