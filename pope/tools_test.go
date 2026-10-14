package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ai"
)

func TestToolsUseDeclarativeSchemas(t *testing.T) {
	want := map[string]struct {
		description string
		required    []string
		properties  []string
	}{
		"exec": {
			description: "Execute a shell command on the host system and return its output.",
			required:    []string{"command", "explanation"},
			properties:  []string{"command", "explanation"},
		},
		"read_file": {
			description: "Read the contents of a local file at the specified path.",
			required:    []string{"path", "explanation"},
			properties:  []string{"path", "explanation"},
		},
		"write_file": {
			description: "Write or overwrite content to a local file at the specified path.",
			required:    []string{"path", "content", "explanation"},
			properties:  []string{"path", "content", "explanation"},
		},
	}
	if len(Tools) != len(want) {
		t.Fatalf("Tools has %d entries, want %d", len(Tools), len(want))
	}
	for name, expected := range want {
		tool, ok := Tools[name]
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		if tool.Description != expected.description {
			t.Errorf("%s description = %q, want %q", name, tool.Description, expected.description)
		}
		if !reflect.DeepEqual(tool.Required, expected.required) {
			t.Errorf("%s required = %v, want %v", name, tool.Required, expected.required)
		}
		if len(tool.Parameters) != len(expected.properties) {
			t.Errorf("%s properties = %v, want keys %v", name, tool.Parameters, expected.properties)
			continue
		}
		for _, property := range expected.properties {
			if _, ok := tool.Parameters[property]; !ok {
				t.Errorf("%s missing property %q", name, property)
			}
		}
	}
}

func TestPopeToolExecution(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "sub", "a.txt")

	call := func(name, args string) (any, error) {
		tool, ok := Tools[name]
		if !ok {
			t.Fatalf("unknown test tool %q", name)
		}
		return tool.Execute(ctx, ai.ToolCall{Name: name, RawArguments: json.RawMessage(args)}, nil)
	}
	pathArg, _ := json.Marshal(file)

	got, err := call("write_file", `{"path":`+string(pathArg)+`,"content":"héllo"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]any{"success": true, "bytes_written": len("héllo")}); !reflect.DeepEqual(got, want) {
		t.Fatalf("write_file = %v, want %v", got, want)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "héllo" {
		t.Fatalf("file = %q, %v", data, err)
	}

	got, err = call("read_file", `{"path":`+string(pathArg)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]any{"content": "héllo"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("read_file = %v, want %v", got, want)
	}

	if _, err := call("read_file", `{}`); err == nil || err.Error() != "path argument is required" {
		t.Fatalf("missing path error = %v", err)
	}
	if _, err := call("read_file", `{"path":`+string(pathArg)+`x`); err == nil || !strings.HasPrefix(err.Error(), "invalid arguments:") {
		t.Fatalf("malformed args error = %v", err)
	}
	if _, err := call("read_file", `{"path":5}`); err == nil || !strings.HasPrefix(err.Error(), "invalid arguments:") {
		t.Fatalf("wrong-type args error = %v", err)
	}
	if _, err := call("exec", `{}`); err == nil || err.Error() != "command argument is required" {
		t.Fatalf("no command error = %v", err)
	}

	res, err := call("exec", `{"command":"echo hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.(map[string]any)
	if out, _ := m["output"].(string); !strings.Contains(out, "hi") || m["error"] != nil {
		t.Fatalf("exec = %v", res)
	}
	res, err = call("exec", `{"command":"exit 3"}`)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = res.(map[string]any)
	if m["error"] == nil {
		t.Fatalf("failing exec must report its error alongside its output, got %v", res)
	}
}

func TestToolFeedbackIsCompactAndLineFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a-long-file-name.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	var feedback strings.Builder
	got, err := Tools["read_file"].Execute(context.Background(), ai.ToolCall{
		Name:         "read_file",
		RawArguments: json.RawMessage(`{"path":` + mustJSON(t, path) + `}`),
	}, func(text string) {
		if strings.ContainsAny(text, "\r\n") {
			t.Fatalf("feedback contains newline: %q", text)
		}
		feedback.WriteString(text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]any{"content": "hello"}) {
		t.Fatalf("read_file = %v", got)
	}
	if want := "📖 a-long-file-na… ... 5 bytes read"; feedback.String() != want {
		t.Fatalf("feedback = %q, want %q", feedback.String(), want)
	}
	if len([]rune(toolLead(ExecInput{}, strings.Repeat("界", 100), 18))) > 25 {
		t.Fatalf("exec lead exceeds compact convention: %q", toolLead(ExecInput{}, strings.Repeat("界", 100), 18))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
