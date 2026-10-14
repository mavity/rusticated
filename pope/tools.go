package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"ai"
)

type LogFeedback = ai.LogFeedback

const maxToolOutput = 32768

func truncateOutput(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n...[truncated]"
	}
	return s
}

func compactString(s string, maxLen int) string {
	runes := []rune(strings.TrimSpace(s))
	if maxLen <= 0 || len(runes) <= maxLen {
		return string(runes)
	}
	if maxLen == 1 {
		return "…"
	}
	return string(runes[:maxLen-1]) + "…"
}

func toolLead(input any, primaryArg string, maxArgLen int) string {
	in := reflect.TypeOf(input)
	for i := range in.NumField() {
		field := in.Field(i)
		if field.Name == "_" {
			return field.Tag.Get("emoji") + " " + compactString(primaryArg, maxArgLen) + " ... "
		}
	}
	panic(fmt.Sprintf("tool input %v has no metadata field", in))
}

func decodeToolCall[T any](call ai.ToolCall) (T, error) {
	var input T
	raw := call.RawArguments
	if len(raw) == 0 && call.Arguments != nil {
		var err error
		raw, err = json.Marshal(call.Arguments)
		if err != nil {
			return input, err
		}
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return input, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	return input, nil
}

func adaptTool[T any](execute func(context.Context, T, LogFeedback) (any, error)) func(context.Context, ai.ToolCall, ai.LogFeedback) (any, error) {
	return func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
		input, err := decodeToolCall[T](call)
		if err != nil {
			return nil, err
		}
		return execute(ctx, input, feedback)
	}
}

// Tools is Pope's declarative capability registry. Session adds any missing
// explanation property to the compiled JSON Schema.
var Tools = map[string]ai.Tool{
	"exec": {
		Description: "Execute a shell command on the host system and return its output.",
		Parameters: map[string]any{
			"command": map[string]any{"type": "string", "description": "The shell command to run"},
			"explanation": map[string]any{
				"type": "string", "description": "Brief explanation of why this command is being run and what information is sought",
			},
		},
		Required: []string{"command", "explanation"},
		Execute:  adaptTool(Exec),
	},
	"read_file": {
		Description: "Read the contents of a local file at the specified path.",
		Parameters: map[string]any{
			"path": map[string]any{"type": "string", "description": "The file path to read"},
			"explanation": map[string]any{
				"type": "string", "description": "Brief explanation of why this file is being read and what information is sought",
			},
		},
		Required: []string{"path", "explanation"},
		Execute:  adaptTool(ReadFile),
	},
	"write_file": {
		Description: "Write or overwrite content to a local file at the specified path.",
		Parameters: map[string]any{
			"path":    map[string]any{"type": "string", "description": "The file path to write to"},
			"content": map[string]any{"type": "string", "description": "The text content to write"},
			"explanation": map[string]any{
				"type": "string", "description": "Brief explanation of why this file is being written",
			},
		},
		Required: []string{"path", "content", "explanation"},
		Execute:  adaptTool(WriteFile),
	},
}
