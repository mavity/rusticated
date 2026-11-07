package shell

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"ai"
)

func TestKabibiMainToolsExposeOnlyHelper(t *testing.T) {
	tools := buildKabibiMainTools(nil, nil, nil, 0, false, nil)
	if len(tools) != 1 {
		t.Fatalf("main agent tool count = %d, want 1", len(tools))
	}
	helper, ok := tools["helper"]
	if !ok {
		t.Fatal("main agent tools do not include helper")
	}
	required := make(map[string]bool, len(helper.Required))
	for _, field := range helper.Required {
		required[field] = true
	}
	if !required["task"] || !required["description"] {
		t.Fatalf("helper required fields = %#v, want task and description", helper.Required)
	}

	result, err := helper.ExecuteWithContext(context.Background(), &ai.ToolContext{}, ai.ToolCall{
		Name:      "helper",
		Arguments: map[string]any{"task": "inspect", "description": ""},
	})
	if err == nil || result != nil {
		t.Fatalf("helper with empty description returned (%#v, %v), want an error", result, err)
	}
}

func TestLastHelperToolResponseUsesMostRecentToolPart(t *testing.T) {
	history := []ai.PromptPart{
		{Role: ai.RoleTool, ToolName: "read_file", ToolResponse: "first"},
		{Role: ai.RoleModel, Text: "intermediate"},
		{Role: ai.RoleTool, ToolName: "write_file", ToolResponse: "last"},
		{Role: ai.RoleModel, Text: "done"},
	}
	got := lastHelperToolResponse(history)
	if got == nil || got.Name != "write_file" || got.Response != "last" {
		t.Fatalf("lastHelperToolResponse() = %#v, want write_file response last", got)
	}
	if got := lastHelperToolResponse([]ai.PromptPart{{Role: ai.RoleModel, Text: "no tools"}}); got != nil {
		t.Fatalf("lastHelperToolResponse() = %#v, want nil without tool responses", got)
	}
}

func TestLazyAIRunnerRejectsConcurrentTurn(t *testing.T) {
	runner := NewLazyAIRunner(nil)
	_, err := runner.beginOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.endOperation()

	if err := runner.RunTurn(context.Background(), "nested", nil, nil); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("RunTurn() error = %v, want %v", err, ErrAIRunnerBusy)
	}
	if err := runner.SetVerbose(true); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("SetVerbose() error = %v, want %v", err, ErrAIRunnerBusy)
	}
}

func TestLazyAIRunnerVerboseEnablesListFilesDiagnostics(t *testing.T) {
	previousVerbose := ai.Verbose
	t.Cleanup(func() { ai.Verbose = previousVerbose })

	runner := NewLazyAIRunner(nil)
	if err := runner.SetVerbose(true); err != nil {
		t.Fatalf("SetVerbose(true): %v", err)
	}
	if options := listFilesOptions(context.Background(), ListFilesInput{}, nil); options.OnDiagnostic == nil {
		t.Fatal("verbose runner did not enable list-files diagnostics")
	}

	if err := runner.SetVerbose(false); err != nil {
		t.Fatalf("SetVerbose(false): %v", err)
	}
	if options := listFilesOptions(context.Background(), ListFilesInput{}, nil); options.OnDiagnostic != nil {
		t.Fatal("non-verbose runner enabled list-files diagnostics")
	}
}

func TestClampModelContextTokensIgnoresDynamicDefault(t *testing.T) {
	for _, test := range []struct {
		name string
		info *ai.ModelInfo
		want int
	}{
		{name: "unknown model info", want: 16384},
		{name: "dynamic default is not a maximum", info: &ai.ModelInfo{MaxContextTokens: 4096, IsDynamicContext: true}, want: 16384},
		{name: "fixed context is a maximum", info: &ai.ModelInfo{MaxContextTokens: 4096}, want: 4096},
		{name: "fixed context above request", info: &ai.ModelInfo{MaxContextTokens: 32768}, want: 16384},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := clampModelContextTokens(16384, test.info); got != test.want {
				t.Fatalf("clampModelContextTokens() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestLazyAIRunnerCloseCancelsActiveOperationWithoutWaiting(t *testing.T) {
	runner := NewLazyAIRunner(nil)
	ctx, err := runner.beginOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	closeReturned := make(chan struct{})
	go func() {
		runner.Close()
		close(closeReturned)
	}()
	select {
	case <-closeReturned:
	case <-time.After(time.Second):
		t.Fatal("Close() waited for the active operation")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel the active operation")
	}

	runner.endOperation()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.WaitClosed(waitCtx); err != nil {
		t.Fatalf("WaitClosed() error = %v", err)
	}
}

func TestEphemeralLineWriterRewritesAndWipesStatus(t *testing.T) {
	var buf bytes.Buffer
	ephemeral := NewEphemeralLineWriter(&buf)

	if err := ephemeral.Feedback("📝 bridge.txt ... "); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Feedback("📝 bridge.txt ... ok"); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Feedback("📖 bridge.txt ... 20 bytes read"); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Wipe(); err != nil {
		t.Fatal(err)
	}

	want := "📝 bridge.txt ..." +
		"\r📝 bridge.txt ... ok" +
		"\r📖 bridge.txt ... 20 bytes read" +
		"\r                               \r"
	if got := buf.String(); got != want {
		t.Fatalf("EphemeralLineWriter output = %q, want %q", got, want)
	}
}
