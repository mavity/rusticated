package shell

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVSCodeToolServerExecUsesFreshSessions(t *testing.T) {
	initialDir := t.TempDir()
	server := NewVSCodeToolServer(initialDir, time.Minute)

	first, err := server.Dispatch(context.Background(), "exec", json.RawMessage(`{"command":"cd /"}`))
	if err != nil {
		t.Fatalf("first exec: %v", err)
	}
	firstResult := first.(map[string]any)
	if firstResult["cwd"] == initialDir {
		t.Fatalf("first exec did not change its working directory: %v", firstResult["cwd"])
	}

	second, err := server.Dispatch(context.Background(), "exec", json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatalf("second exec: %v", err)
	}
	secondResult := second.(map[string]any)
	if secondResult["cwd"] != initialDir {
		t.Fatalf("second exec inherited the first exec cwd: got %v, want %q", secondResult["cwd"], initialDir)
	}
}

func TestVSCodeToolServerServeReportsProtocolErrors(t *testing.T) {
	server := NewVSCodeToolServer(t.TempDir(), time.Minute)
	var output strings.Builder
	input := strings.NewReader("{\"type\":\"invoke\",\"request_id\":\"req-1\",\"name\":\"missing\",\"arguments\":{}}\n")

	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d protocol lines, want ready and error: %q", len(lines), output.String())
	}
	var ready vscodeToolResponse
	if err := json.Unmarshal([]byte(lines[0]), &ready); err != nil {
		t.Fatalf("decode ready response: %v", err)
	}
	if ready.Type != "ready" {
		t.Fatalf("first response type = %q, want ready", ready.Type)
	}
	var response vscodeToolResponse
	if err := json.Unmarshal([]byte(lines[1]), &response); err != nil {
		t.Fatalf("decode invocation response: %v", err)
	}
	if response.RequestID != "req-1" || response.Error == "" || response.Result != nil {
		t.Fatalf("unexpected error response: %+v", response)
	}
}

func TestVSCodeToolServerAllocatesDistinctVisibleIDs(t *testing.T) {
	server := NewVSCodeToolServer("", time.Minute)
	server.mu.Lock()
	first, err := server.allocateIDLocked()
	if err != nil {
		server.mu.Unlock()
		t.Fatalf("allocate first ID: %v", err)
	}
	server.jobs[first] = &vscodeToolJob{}
	second, err := server.allocateIDLocked()
	server.mu.Unlock()
	if err != nil {
		t.Fatalf("allocate second ID: %v", err)
	}
	if first == second {
		t.Fatalf("allocated colliding visible IDs: %d", first)
	}
}
