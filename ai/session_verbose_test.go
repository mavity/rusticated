package ai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type verboseTestBackend struct{}

func (verboseTestBackend) NewConversation(ConversationConfig) (conversationBackend, error) {
	return verboseTestConversation{}, nil
}

func (verboseTestBackend) Close() error { return nil }

type verboseTestConversation struct{}

func (verboseTestConversation) SendStream(context.Context, []PromptPart, func(ModelEvent)) error {
	return nil
}

func (verboseTestConversation) Close() error { return nil }

func TestVerboseLogsConversationConfigAndToolCalls(t *testing.T) {
	session, err := NewSession(SessionConfig{
		Engine:        &Engine{impl: &engineImpl{backend: verboseTestBackend{}}},
		SystemMessage: "Full system prompt.",
		Verbose:       true,
		Tools: map[string]Tool{
			"lookup": {
				Description: "Look up a document.",
				Parameters: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Schema-only detail.",
					},
				},
				Execute: func(context.Context, ToolCall, LogFeedback) (any, error) {
					return map[string]any{"answer": "Full tool result."}, nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got := captureVerboseStderr(t, func() {
		if err := session.Init(); err != nil {
			t.Fatal(err)
		}
		if err := session.Init(); err != nil {
			t.Fatal(err)
		}
		session.executeToolCalls(context.Background(), nil, []ToolCall{{
			ID:           "call-1",
			Name:         "lookup",
			Arguments:    map[string]any{"query": "Full tool input."},
			RawArguments: json.RawMessage(`{"query":"Full tool input."}`),
		}})
	})

	if strings.Count(got, "conversation system prompt:") != 1 {
		t.Fatalf("system prompt log count = %d, want 1; output:\n%s",
			strings.Count(got, "conversation system prompt:"), got)
	}
	for _, want := range []string{
		"Full system prompt.",
		"lookup: Look up a document.",
		"call-1",
		"Full tool input.",
		"Full tool result.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose output missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Schema-only detail.", `"type": "string"`, `"parameters"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("verbose tool listing included schema detail %q:\n%s", unwanted, got)
		}
	}
}

func TestVerboseConversationDiagnosticsDisabledByDefault(t *testing.T) {
	session, err := NewSession(SessionConfig{
		Engine: &Engine{impl: &engineImpl{backend: verboseTestBackend{}}},
		Tools: map[string]Tool{
			"noop": {
				Description: "A test tool.",
				Execute: func(context.Context, ToolCall, LogFeedback) (any, error) {
					return "done", nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	got := captureVerboseStderr(t, func() {
		if err := session.Init(); err != nil {
			t.Fatal(err)
		}
		session.executeToolCalls(context.Background(), nil, []ToolCall{{Name: "noop"}})
	})
	if got != "" {
		t.Fatalf("verbose output with Verbose=false = %q, want empty", got)
	}
}

func captureVerboseStderr(t *testing.T, run func()) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "verbose-stderr-")
	if err != nil {
		t.Fatal(err)
	}
	previousStderr := os.Stderr
	os.Stderr = file
	defer func() {
		os.Stderr = previousStderr
		if err := file.Close(); err != nil {
			t.Errorf("close captured stderr: %v", err)
		}
	}()

	run()
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}
