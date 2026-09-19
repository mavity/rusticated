package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// mockShellRunner implements ShellRunner for testing.
type mockShellRunner struct {
	runErr   error
	runCount int
	lastF    *syntax.File
	lastCtx  context.Context
	runFn    func(context.Context, *syntax.File) error
}

func (m *mockShellRunner) Run(ctx context.Context, f *syntax.File) error {
	m.runCount++
	m.lastCtx = ctx
	m.lastF = f
	if m.runFn != nil {
		return m.runFn(ctx, f)
	}
	return m.runErr
}

// recordingShellRunner records stdout/stderr for testing
type recordingShellRunner struct {
	runErr error
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func (r *recordingShellRunner) Run(ctx context.Context, f *syntax.File) error {
	if r.runErr != nil {
		r.stderr.WriteString("error: " + r.runErr.Error())
		return r.runErr
	}
	r.stdout.WriteString("execution succeeded")
	return nil
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmdStr  string
		wantErr bool
	}{
		{
			name:    "simple echo",
			cmdStr:  "echo hello",
			wantErr: false,
		},
		{
			name:    "command with pipes",
			cmdStr:  "echo hello | cat",
			wantErr: false,
		},
		{
			name:    "command with redirection",
			cmdStr:  "echo hello > /tmp/test.txt",
			wantErr: false,
		},
		{
			name:    "compound command",
			cmdStr:  "if true; then echo yes; fi",
			wantErr: false,
		},
		{
			name:    "empty command",
			cmdStr:  "",
			wantErr: false, // parseCommand returns empty file on error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := parseCommand(tt.cmdStr)
			if f == nil && !tt.wantErr {
				t.Error("parseCommand returned nil")
			}
		})
	}
}

func TestParseCommandReader(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "simple command from reader",
			input:   "echo test",
			wantErr: false,
		},
		{
			name:    "multiline script",
			input:   "echo line1\necho line2",
			wantErr: false,
		},
		{
			name:    "reader with name",
			input:   "echo hello",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := strings.NewReader(tt.input)
			f := parseCommandReader(reader, "test.sh")
			if f == nil && !tt.wantErr {
				t.Error("parseCommandReader returned nil")
			}
		})
	}
}

func TestMockShellRunner(t *testing.T) {
	ctx := context.Background()
	mock := &mockShellRunner{}

	cmdStr := "echo hello"
	f := parseCommand(cmdStr)
	err := mock.Run(ctx, f)

	if err != nil {
		t.Errorf("mock.Run failed: %v", err)
	}
	if mock.runCount != 1 {
		t.Errorf("Run called %d times, want 1", mock.runCount)
	}
}

func TestMockShellRunnerError(t *testing.T) {
	ctx := context.Background()
	testErr := errors.New("execution failed")
	mock := &mockShellRunner{runErr: testErr}

	cmdStr := "false"
	f := parseCommand(cmdStr)
	err := mock.Run(ctx, f)

	if err != testErr {
		t.Errorf("got error %v, want %v", err, testErr)
	}
}

func TestShellRunnerCustomFn(t *testing.T) {
	ctx := context.Background()
	executionLoggedCorrectly := false

	mock := &mockShellRunner{
		runFn: func(ctx context.Context, f *syntax.File) error {
			// Verify context is passed
			if ctx == nil {
				return errors.New("context is nil")
			}
			executionLoggedCorrectly = true
			return nil
		},
	}

	cmdStr := "echo test"
	f := parseCommand(cmdStr)
	err := mock.Run(ctx, f)

	if err != nil {
		t.Errorf("mock.Run failed: %v", err)
	}
	if !executionLoggedCorrectly {
		t.Error("custom runFn was not called correctly")
	}
}

func TestRecordingShellRunner(t *testing.T) {
	ctx := context.Background()
	recorder := &recordingShellRunner{}

	cmdStr := "echo test"
	f := parseCommand(cmdStr)
	err := recorder.Run(ctx, f)

	if err != nil {
		t.Errorf("recorder.Run failed: %v", err)
	}
	if !strings.Contains(recorder.stdout.String(), "succeeded") {
		t.Errorf("expected output not found, got %q", recorder.stdout.String())
	}
}

func TestRecordingShellRunnerError(t *testing.T) {
	ctx := context.Background()
	testErr := errors.New("test error")
	recorder := &recordingShellRunner{runErr: testErr}

	cmdStr := "false"
	f := parseCommand(cmdStr)
	err := recorder.Run(ctx, f)

	if err != testErr {
		t.Errorf("got error %v, want %v", err, testErr)
	}
	if !strings.Contains(recorder.stderr.String(), "error:") {
		t.Errorf("expected error output, got %q", recorder.stderr.String())
	}
}

func TestShellRunnerContextPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	receivedCtx := false
	mock := &mockShellRunner{
		runFn: func(receivedContext context.Context, f *syntax.File) error {
			receivedCtx = (receivedContext == ctx)
			return nil
		},
	}

	cmdStr := "echo test"
	f := parseCommand(cmdStr)
	_ = mock.Run(ctx, f)

	if !receivedCtx {
		t.Error("context not propagated correctly to runner")
	}
}

func TestParseCommandChaining(t *testing.T) {
	tests := []struct {
		name string
		cmds []string
	}{
		{
			name: "multiple commands chained",
			cmds: []string{
				"echo a",
				"echo b",
				"echo a && echo b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, cmd := range tt.cmds {
				f := parseCommand(cmd)
				if f == nil {
					t.Errorf("failed to parse command: %s", cmd)
				}
			}
		})
	}
}
