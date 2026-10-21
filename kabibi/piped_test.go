package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"ai"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

type pipedAIRunner struct {
	prompts []string
	runErr  error
}

func (r *pipedAIRunner) RunTurn(_ context.Context, prompt string, _ ai.LogFeedback, onContent ai.LogFeedback) error {
	r.prompts = append(r.prompts, prompt)
	if onContent != nil {
		onContent("AI response\n")
	}
	return r.runErr
}

func (r *pipedAIRunner) Close() {}

type signalBuffer struct {
	mu         sync.Mutex
	buffer     bytes.Buffer
	firstWrite chan struct{}
	once       sync.Once
}

func newSignalBuffer() *signalBuffer {
	return &signalBuffer{firstWrite: make(chan struct{})}
}

func (w *signalBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.buffer.Write(p)
	w.mu.Unlock()
	if n > 0 {
		w.once.Do(func() { close(w.firstWrite) })
	}
	return n, err
}

func (w *signalBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

type failedOutputWriter struct {
	err error
}

func (w failedOutputWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type shortOutputWriter struct{}

func (shortOutputWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func TestRunPipedReader_ExecutesIncrementallyAndRetainsShellState(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner := &pipedAIRunner{}
	input := "export KABIBI_PIPE_TEST=retained\necho $KABIBI_PIPE_TEST\nif true; then\necho multiline\nfi\n"

	if code := runPipedReader(context.Background(), strings.NewReader(input), &stdout, &stderr, cliOptions{}, es, runner); code != 0 {
		t.Fatalf("runPipedReader() = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "retained\nmultiline\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if len(runner.prompts) != 0 {
		t.Fatalf("unexpected AI prompts: %v", runner.prompts)
	}
}

func TestRunPipedReader_ExecutesBeforeEOF(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	reader, writer := io.Pipe()
	stdout := newSignalBuffer()
	var stderr bytes.Buffer
	runner := &pipedAIRunner{}
	finished := make(chan struct{})
	var code int
	go func() {
		code = runPipedReader(context.Background(), reader, stdout, &stderr, cliOptions{}, es, runner)
		close(finished)
	}()
	defer func() {
		_ = writer.Close()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("piped reader did not stop after stdin was closed")
		}
	}()

	if _, err := io.WriteString(writer, "echo first\n"); err != nil {
		t.Fatalf("write first command: %v", err)
	}
	select {
	case <-stdout.firstWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("first command produced no output before stdin reached EOF")
	}

	if _, err := io.WriteString(writer, "echo second\n"); err != nil {
		t.Fatalf("write second command: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close piped input: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("piped reader did not finish after stdin reached EOF")
	}
	if code != 0 {
		t.Fatalf("runPipedReader() = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "first\nsecond\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunPipedReader_ExecutesForLoopAndHereDocument(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner := &pipedAIRunner{}
	input := "for item in one two; do\nprintf '%s\\n' \"$item\"\ndone\n" +
		"cat <<EOF\nhere-document body\nEOF\n" +
		"echo after\n"

	if code := runPipedReader(context.Background(), strings.NewReader(input), &stdout, &stderr, cliOptions{}, es, runner); code != 0 {
		t.Fatalf("runPipedReader() = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "one\ntwo\nhere-document body\nafter\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if len(runner.prompts) != 0 {
		t.Fatalf("unexpected AI prompts: %v", runner.prompts)
	}
}

func TestRunPipedReader_RoutesUnknownCommandToAIAndContinues(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner := &pipedAIRunner{}
	input := "echo before\nName a bridge in Paris\necho after"

	if code := runPipedReader(context.Background(), strings.NewReader(input), &stdout, &stderr, cliOptions{}, es, runner); code != 0 {
		t.Fatalf("runPipedReader() = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got, want := runner.prompts, []string{"Name a bridge in Paris"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("AI prompts = %v, want %v", got, want)
	}
	if got, want := stdout.String(), "before\nAI response\nafter\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestRunPipedReader_ReturnsLastCommandExitCode(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{name: "failure is last command", input: "false\n", want: 1},
		{name: "later success replaces earlier failure", input: "false\necho recovered\n", want: 0},
		{name: "exit preserves prior status", input: "false\nexit\n", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es, err := shell.NewExecutionSession(t.TempDir())
			if err != nil {
				t.Fatalf("NewExecutionSession: %v", err)
			}
			var stdout, stderr bytes.Buffer
			code := runPipedReader(context.Background(), strings.NewReader(tt.input), &stdout, &stderr, cliOptions{}, es, &pipedAIRunner{})
			if code != tt.want {
				t.Fatalf("runPipedReader() = %d, want %d; stderr=%q", code, tt.want, stderr.String())
			}
		})
	}
}

func TestRunPipedReader_ReportsAIRunnerFailure(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner := &pipedAIRunner{runErr: errors.New("AI runner failed")}

	if code := runPipedReader(context.Background(), strings.NewReader("Name a bridge in Paris\n"), &stdout, &stderr, cliOptions{}, es, runner); code != 1 {
		t.Fatalf("runPipedReader() = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "AI runner failed") {
		t.Fatalf("stderr = %q, want AI runner failure", stderr.String())
	}
}

func TestRunPipedReader_ReportsOutputWriteFailures(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		writer io.Writer
	}{
		{name: "shell writer error", input: "echo output\n", writer: failedOutputWriter{err: errors.New("writer failed")}},
		{name: "shell short write", input: "echo output\n", writer: shortOutputWriter{}},
		{name: "AI writer error", input: "Name a bridge in Paris\n", writer: failedOutputWriter{err: errors.New("writer failed")}},
		{name: "AI short write", input: "Name a bridge in Paris\n", writer: shortOutputWriter{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es, err := shell.NewExecutionSession(t.TempDir())
			if err != nil {
				t.Fatalf("NewExecutionSession: %v", err)
			}
			var stderr bytes.Buffer
			code := runPipedReader(context.Background(), strings.NewReader(tt.input), tt.writer, &stderr, cliOptions{}, es, &pipedAIRunner{})
			if code != 1 {
				t.Fatalf("runPipedReader() = %d, want 1; stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "writing stdout") {
				t.Fatalf("stderr = %q, want output-write diagnostic", stderr.String())
			}
		})
	}
}

func TestRunPipedReader_IncompleteSyntaxAtEOFFails(t *testing.T) {
	for _, input := range []string{"if true; then\n", "echo unfinished \\"} {
		t.Run(input, func(t *testing.T) {
			es, err := shell.NewExecutionSession(t.TempDir())
			if err != nil {
				t.Fatalf("NewExecutionSession: %v", err)
			}
			var stdout, stderr bytes.Buffer
			runner := &pipedAIRunner{}

			if code := runPipedReader(context.Background(), strings.NewReader(input), &stdout, &stderr, cliOptions{}, es, runner); code != 2 {
				t.Fatalf("runPipedReader() = %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "incomplete shell input at end of stdin") {
				t.Fatalf("stderr = %q, want incomplete-input diagnostic", stderr.String())
			}
			if len(runner.prompts) != 0 {
				t.Fatalf("incomplete shell input was routed to AI: %v", runner.prompts)
			}
		})
	}
}
