package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"ai"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

type pipedAIRunner struct {
	prompts []string
}

func (r *pipedAIRunner) RunTurn(_ context.Context, prompt string, _ ai.LogFeedback, onContent ai.LogFeedback) error {
	r.prompts = append(r.prompts, prompt)
	if onContent != nil {
		onContent("AI response\n")
	}
	return nil
}

func (r *pipedAIRunner) Close() {}

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
