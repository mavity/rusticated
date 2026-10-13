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

// legacyDeclarations is the hand-written schema the reflected one replaced.
const legacyDeclarations = `[
 {"type":"function","function":{"name":"exec","description":"Execute a shell command on the host system and return its output.",
  "parameters":{"type":"object","properties":{"command":{"type":"string","description":"The shell command to run"}},"required":["command"]}}},
 {"type":"function","function":{"name":"read_file","description":"Read the contents of a local file at the specified path.",
  "parameters":{"type":"object","properties":{"path":{"type":"string","description":"The file path to read"}},"required":["path"]}}},
 {"type":"function","function":{"name":"write_file","description":"Write or overwrite content to a local file at the specified path.",
  "parameters":{"type":"object","properties":{"path":{"type":"string","description":"The file path to write to"},"content":{"type":"string","description":"The text content to write"}},"required":["path","content"]}}}
]`

func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGetToolDeclarationsMatchesLegacySchema(t *testing.T) {
	var want any
	if err := json.Unmarshal([]byte(legacyDeclarations), &want); err != nil {
		t.Fatal(err)
	}
	if got := roundTrip(t, GetToolDeclarations()); !reflect.DeepEqual(got, want) {
		t.Fatalf("declarations differ\n got: %v\nwant: %v", got, want)
	}
}

func TestExecuteTool(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "sub", "a.txt")

	call := func(name, args string) any {
		return ExecuteTool(ctx, ai.ToolCall{Name: name, RawArguments: json.RawMessage(args)})
	}
	pathArg, _ := json.Marshal(file)

	got := call("write_file", `{"path":`+string(pathArg)+`,"content":"héllo"}`)
	if want := (map[string]any{"success": true, "bytes_written": len("héllo")}); !reflect.DeepEqual(got, want) {
		t.Fatalf("write_file = %v, want %v", got, want)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "héllo" {
		t.Fatalf("file = %q, %v", data, err)
	}

	if got := call("read_file", `{"path":`+string(pathArg)+`}`); !reflect.DeepEqual(got, map[string]any{"content": "héllo"}) {
		t.Fatalf("read_file = %v", got)
	}

	errOf := func(v any) string {
		m, _ := v.(map[string]any)
		s, _ := m["error"].(string)
		return s
	}
	if e := errOf(call("read_file", `{}`)); e != "path argument is required" {
		t.Fatalf("missing path error = %q", e)
	}
	if e := errOf(call("read_file", `{"path":`+string(pathArg)+`x`)); !strings.HasPrefix(e, "invalid arguments:") {
		t.Fatalf("malformed args error = %q", e)
	}
	if e := errOf(call("read_file", `{"path":5}`)); !strings.HasPrefix(e, "invalid arguments:") {
		t.Fatalf("wrong-type args error = %q", e)
	}
	if e := errOf(call("nope", `{}`)); e != "unknown tool: nope" {
		t.Fatalf("unknown tool error = %q", e)
	}
	if e := errOf(ExecuteTool(ctx, ai.ToolCall{Name: "exec"})); e != "command argument is required" {
		t.Fatalf("no arguments error = %q", e)
	}

	res, _ := call("exec", `{"command":"echo hi"}`).(map[string]any)
	if out, _ := res["output"].(string); !strings.Contains(out, "hi") || res["error"] != nil {
		t.Fatalf("exec = %v", res)
	}
	res, _ = call("exec", `{"command":"exit 3"}`).(map[string]any)
	if res["error"] == nil {
		t.Fatalf("failing exec must report its error alongside its output, got %v", res)
	}
}

func TestToolInputTypeRejectsBadSignature(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for a malformed tool")
		}
	}()
	toolInputType("bad", func(string) {})
}
