package shell

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"ai"
)

type testManagedPTYHandle struct {
	mu     sync.Mutex
	writes []byte
	closed bool
}

func (p *testManagedPTYHandle) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (p *testManagedPTYHandle) Write(data []byte) (int, error) {
	p.mu.Lock()
	p.writes = append(p.writes, data...)
	p.mu.Unlock()
	return len(data), nil
}

func (p *testManagedPTYHandle) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func (*testManagedPTYHandle) Resize(int, int) error { return nil }
func (*testManagedPTYHandle) Wait() error           { return nil }
func (*testManagedPTYHandle) Fd() uintptr           { return 0 }

func TestRegisteredTerminalToolsUseManagedJobID(t *testing.T) {
	es, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	tools := BuildKabibiTools(es)
	execResult, err := tools["exec"].Execute(context.Background(), ai.ToolCall{
		Name:         "exec",
		RawArguments: json.RawMessage(`{"command":"while :; do :; done","timeout_sec":1}`),
	}, nil)
	if err != nil {
		t.Fatalf("registered exec tool: %v", err)
	}
	execMap, ok := execResult.(map[string]any)
	if !ok {
		t.Fatalf("registered exec result has type %T, want map[string]any", execResult)
	}
	if execMap["still_running"] != true {
		t.Fatalf("registered exec result = %#v, want a managed running job", execMap)
	}
	id, ok := execMap["pid_id"].(int)
	if !ok || id <= 0 {
		t.Fatalf("registered exec result pid_id = %#v, want positive int", execMap["pid_id"])
	}

	job, ok := es.LookupManagedPTY(id)
	if !ok {
		t.Fatalf("registered exec job %d was not found", id)
	}
	handle := &testManagedPTYHandle{}
	job.mu.Lock()
	job.ptyHandle = handle
	job.mu.Unlock()
	job.appendOutput([]byte("captured-managed-output\n"))

	readArgs, err := json.Marshal(map[string]any{
		"id":       id,
		"wait_ms":  1,
		"only_new": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	readResult, err := tools["term_read"].Execute(context.Background(), ai.ToolCall{
		Name:         "term_read",
		RawArguments: readArgs,
	}, nil)
	if err != nil {
		t.Fatalf("registered term_read: %v", err)
	}
	readMap, ok := readResult.(map[string]any)
	if !ok {
		t.Fatalf("registered term_read result = %#v", readResult)
	}
	readOutput, outputOK := readMap["output"].(string)
	if readMap["id"] != id || readMap["exited"] != false || !outputOK ||
		!strings.Contains(readOutput, "captured-managed-output") {
		t.Fatalf("registered term_read result = %#v", readResult)
	}

	inputArgs, err := json.Marshal(map[string]any{
		"id":      id,
		"keys":    "answer<enter>",
		"wait_ms": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputResult, err := tools["term_input"].Execute(context.Background(), ai.ToolCall{
		Name:         "term_input",
		RawArguments: inputArgs,
	}, nil)
	if err != nil {
		t.Fatalf("registered term_input: %v", err)
	}
	inputMap, ok := inputResult.(map[string]any)
	if !ok || inputMap["id"] != id || inputMap["exited"] != false {
		t.Fatalf("registered term_input result = %#v", inputResult)
	}
	handle.mu.Lock()
	gotInput := string(handle.writes)
	handle.mu.Unlock()
	if gotInput != "answer\r" {
		t.Fatalf("registered term_input wrote %q, want translated input %q", gotInput, "answer\\r")
	}

	unknownArgs, err := json.Marshal(map[string]any{"id": 999999})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools["term_kill"].Execute(context.Background(), ai.ToolCall{
		Name:         "term_kill",
		RawArguments: unknownArgs,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "managed process id=999999 not found") {
		t.Fatalf("registered term_kill unknown-id error = %v", err)
	}

	invalidSignalArgs, err := json.Marshal(map[string]any{"id": id, "signal": "SIGUSR1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tools["term_kill"].Execute(context.Background(), ai.ToolCall{
		Name:         "term_kill",
		RawArguments: invalidSignalArgs,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported signal") {
		t.Fatalf("registered term_kill invalid-signal error = %v", err)
	}
	select {
	case <-job.doneCh:
		t.Fatal("invalid term_kill signal canceled the managed job")
	default:
	}

	defaultSignalArgs, err := json.Marshal(map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	killResult, err := tools["term_kill"].Execute(context.Background(), ai.ToolCall{
		Name:         "term_kill",
		RawArguments: defaultSignalArgs,
	}, nil)
	if err != nil {
		t.Fatalf("registered term_kill with omitted signal (default SIGTERM): %v", err)
	}
	killMap, ok := killResult.(map[string]any)
	if !ok {
		t.Fatalf("registered term_kill result has type %T, want map[string]any", killResult)
	}
	if got, ok := killMap["id"].(int); !ok || got != id {
		t.Fatalf("term_kill result id = %#v, want %d", killMap["id"], id)
	}
	if killMap["exited"] != true {
		t.Fatalf("term_kill result = %#v, want exited=true", killMap)
	}
	for _, key := range []string{"output", "result", "exit_code"} {
		if _, ok := killMap[key]; !ok {
			t.Errorf("term_kill result is missing %q: %#v", key, killMap)
		}
	}
	if result, ok := killMap["result"].(string); !ok || !strings.Contains(result, "[status: EXITED (") {
		t.Errorf("term_kill result status = %#v, want exited status line", killMap["result"])
	}
	select {
	case <-job.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("registered term_kill did not wait for interpreter completion")
	}
	handle.mu.Lock()
	closed := handle.closed
	handle.mu.Unlock()
	if !closed {
		t.Fatal("registered term_kill did not close the managed PTY")
	}
}
