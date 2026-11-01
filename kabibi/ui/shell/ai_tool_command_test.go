package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai"
)

func TestParseAIToolArguments(t *testing.T) {
	tools := BuildKabibiTools(nil)

	raw, err := parseAIToolArguments(tools["list_files"], []string{
		"--QUERY", "two words",
		"--search-content",
		"--max-results", "17",
	})
	if err != nil {
		t.Fatalf("parse list_files arguments: %v", err)
	}
	var listArgs map[string]any
	if err := json.Unmarshal(raw, &listArgs); err != nil {
		t.Fatal(err)
	}
	if listArgs["query"] != "two words" || listArgs["search_content"] != true || listArgs["max_results"] != float64(17) {
		t.Fatalf("parsed list_files arguments = %#v", listArgs)
	}

	raw, err = parseAIToolArguments(tools["git_mutate"], []string{
		"--action", "stage",
		"--paths", "one.go",
		"--paths", "two.go",
	})
	if err != nil {
		t.Fatalf("parse git_mutate arguments: %v", err)
	}
	var gitArgs map[string]any
	if err := json.Unmarshal(raw, &gitArgs); err != nil {
		t.Fatal(err)
	}
	if got := gitArgs["paths"].([]any); len(got) != 2 || got[0] != "one.go" || got[1] != "two.go" {
		t.Fatalf("parsed git paths = %#v", got)
	}

	raw, err = parseAIToolArguments(tools["browser_cdp"], []string{
		"--nickname", "main",
		"--method", "Page.getFrameTree",
		"--params", `{"include":true}`,
	})
	if err != nil {
		t.Fatalf("parse browser_cdp arguments: %v", err)
	}
	var cdpArgs map[string]any
	if err := json.Unmarshal(raw, &cdpArgs); err != nil {
		t.Fatal(err)
	}
	if got := cdpArgs["params"].(map[string]any)["include"]; got != true {
		t.Fatalf("parsed CDP params = %#v", cdpArgs["params"])
	}
}

func TestExecuteAIToolAliasAndMarkdownResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte("needle found\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	result, err := executeAITool(context.Background(), execSess, "list-files", []string{
		"--query", "needle",
		"--search-content",
	}, nil)
	if err != nil {
		t.Fatalf("execute list-files: %v", err)
	}
	for _, want := range []string{"### list_files", "```text", "result.txt", "[match: L1"} {
		if !strings.Contains(result, want) {
			t.Errorf("Markdown result missing %q:\n%s", want, result)
		}
	}

	var toolsHelp strings.Builder
	tools := BuildKabibiTools(execSess)
	toolsHelp.WriteString(formatAIToolsHelp(tools))
	for _, want := range []string{"read-file", "edit-file", "write-file", "list-files", "exec", "term-read", "shell-history", "git-inspect", "browser-cdp"} {
		if !strings.Contains(toolsHelp.String(), want) {
			t.Errorf("AI tools help missing %q", want)
		}
	}
	if len(tools) != 21 {
		t.Errorf("registered tool count = %d, want 21", len(tools))
	}
}

type commandTestEmbedder struct {
	queryCalls int
	batchCalls int
}

func (e *commandTestEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	e.queryCalls++
	return []float32{1, 0}, nil
}

func (e *commandTestEmbedder) EmbedBatchContext(_ context.Context, parts []ai.PromptPart) ([][]float32, error) {
	e.batchCalls++
	vectors := make([][]float32, len(parts))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestExecuteAIToolListFilesUsesProvidedEmbedder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "browser_worker.go"), []byte("func startWorker() { launchCanvasRuntime() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	embedder := &commandTestEmbedder{}
	result, err := executeAITool(context.Background(), execSess, "list_files", []string{
		"--query", "where the browser canvas runtime starts",
		"--search-content",
	}, embedder)
	if err != nil {
		t.Fatalf("execute semantic list_files: %v", err)
	}
	for _, want := range []string{"mode: semantic-content", "browser_worker.go", "[semantic: 1.00]"} {
		if !strings.Contains(result, want) {
			t.Errorf("semantic list_files result missing %q:\n%s", want, result)
		}
	}
	if embedder.queryCalls != 1 || embedder.batchCalls != 1 {
		t.Fatalf("embedder calls = (query %d, batch %d), want (1, 1)", embedder.queryCalls, embedder.batchCalls)
	}
}

func TestAIBuiltinMiddlewareDispatchesTool(t *testing.T) {
	setTestConfigDirectory(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte("needle found\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	record, err := execSess.RunCommand(
		context.Background(),
		`ai list-files --query "needle" --search-content`,
		nil,
		SourceUserShell,
		nil,
	)
	if err != nil {
		t.Fatalf("run ai tool command: %v", err)
	}
	if record.ExitCode != 0 || !strings.Contains(record.Output, "result.txt") || !strings.Contains(record.Output, "```text") {
		t.Fatalf("command result = (exit %d, %q)", record.ExitCode, record.Output)
	}

	for _, shellName := range []string{"sh", "bash", "kabibi"} {
		command := shellName + ` -c "ai list_files --query needle --search_content"`
		record, err := execSess.RunCommand(context.Background(), command, nil, SourceUserShell, nil)
		if err != nil {
			t.Fatalf("run tool through %s: %v", shellName, err)
		}
		if record.ExitCode != 0 || !strings.Contains(record.Output, "result.txt") {
			t.Fatalf("%s tool result = (exit %d, %q)", shellName, record.ExitCode, record.Output)
		}
	}
}

func TestAIBuiltinMiddlewareRendersForAttachedTTY(t *testing.T) {
	setTestConfigDirectory(t)
	execSess, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()
	execSess.SetCommandTerminalAttached(true)

	record, err := execSess.RunCommand(
		context.Background(),
		`ai shell-state --explanation "inspect current state"`,
		nil,
		SourceUserShell,
		nil,
	)
	if err != nil {
		t.Fatalf("run TUI tool command: %v", err)
	}
	if record.ExitCode != 0 || !strings.Contains(record.Output, "\x1b[") {
		t.Fatalf("TUI command output = (exit %d, %q), want ANSI-rendered Markdown", record.ExitCode, record.Output)
	}
	if strings.Contains(record.Output, "```json") {
		t.Fatalf("TUI command output retained Markdown fences: %q", record.Output)
	}
}

func TestRunAIBuiltinToolRendersMarkdownForTTY(t *testing.T) {
	setTestConfigDirectory(t)
	execSess, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	var stdout, stderr bytes.Buffer
	err = runAIBuiltinTool(
		context.Background(),
		execSess,
		"shell-state",
		[]string{"--explanation", "check current state"},
		&stdout,
		&stderr,
		newCommandTerminal(context.Background(), 80, 20),
		true,
		nil,
	)
	if err != nil {
		t.Fatalf("run TUI tool: %v; stderr=%q", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("TUI result was not converted to ANSI: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "```json") {
		t.Fatalf("TUI result retained Markdown fences: %q", stdout.String())
	}
}

func TestIsAIToolNameAcceptsDashAndUnderscore(t *testing.T) {
	for _, name := range []string{"list_files", "list-files", "browser_cdp", "browser-cdp"} {
		if !IsAIToolName(name) {
			t.Errorf("IsAIToolName(%q) = false", name)
		}
	}
	if IsAIToolName("not-a-tool") {
		t.Fatal("unknown tool name was accepted")
	}
}

func TestParseAIToolArgumentsRejectsMissingRequiredOption(t *testing.T) {
	tools := BuildKabibiTools(nil)
	_, err := parseAIToolArguments(tools["read_file"], nil)
	if err == nil || !strings.Contains(err.Error(), "--path is required") {
		t.Fatalf("parse missing required argument error = %v", err)
	}
}
