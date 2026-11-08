package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai"
	"github.com/mavity/rusticated/kabibi/config"
)

func TestParseAIToolArguments(t *testing.T) {
	tools := BuildKabibiTools(nil)
	findFiles := tools["find_files"]
	for _, field := range []string{"directory", "find_paths", "find_content", "no_semantic", "max_results"} {
		if _, ok := findFiles.Parameters[field]; !ok {
			t.Errorf("find_files schema missing %q", field)
		}
	}
	findContentSchema, ok := findFiles.Parameters["find_content"].(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(findContentSchema["description"]), "Semantic search is on by default") {
		t.Errorf("find_files find_content description does not specify semantic-by-default search: %#v", findFiles.Parameters["find_content"])
	}
	findPathsSchema, ok := findFiles.Parameters["find_paths"].(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(findPathsSchema["description"]), "Never semantic") {
		t.Errorf("find_files find_paths description does not specify structural-only search: %#v", findFiles.Parameters["find_paths"])
	}
	for _, legacyField := range []string{"query", "search_content"} {
		if _, ok := findFiles.Parameters[legacyField]; ok {
			t.Errorf("find_files schema retains legacy field %q", legacyField)
		}
	}

	raw, err := parseAIToolArguments(findFiles, []string{
		"--FIND-CONTENT", "two words",
		"--find-paths", "**/*.go",
		"--directory", "nested",
		"--no-semantic", "false",
		"--max-results", "17",
	})
	if err != nil {
		t.Fatalf("parse find_files arguments: %v", err)
	}
	var listArgs map[string]any
	if err := json.Unmarshal(raw, &listArgs); err != nil {
		t.Fatal(err)
	}
	if listArgs["find_content"] != "two words" || listArgs["find_paths"] != "**/*.go" ||
		listArgs["directory"] != "nested" || listArgs["no_semantic"] != false ||
		listArgs["max_results"] != float64(17) {
		t.Fatalf("parsed find_files arguments = %#v", listArgs)
	}
	if _, err := parseAIToolArguments(findFiles, []string{"--query", "old syntax"}); err == nil {
		t.Fatal("find_files accepted the removed --query option")
	}
	if _, err := parseAIToolArguments(findFiles, []string{"--search-content", "true"}); err == nil {
		t.Fatal("find_files accepted the removed --search-content option")
	}
	raw, err = parseAIToolArguments(findFiles, []string{"--no-semantic"})
	if err != nil {
		t.Fatalf("parse no-semantic flag: %v", err)
	}
	if err := json.Unmarshal(raw, &listArgs); err != nil {
		t.Fatal(err)
	}
	if listArgs["no_semantic"] != true {
		t.Fatalf("no-semantic flag = %#v, want true", listArgs["no_semantic"])
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

func TestFindFilesNeedsEmbedder(t *testing.T) {
	tests := []struct {
		name  string
		input FindFilesInput
		want  bool
	}{
		{name: "paths only"},
		{name: "content query", input: FindFilesInput{FindContent: "browser tool"}, want: true},
		{name: "literal content query", input: FindFilesInput{FindContent: "browser tool", NoSemantic: true}},
		{name: "empty content even with no semantic", input: FindFilesInput{NoSemantic: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findFilesNeedsEmbedder(tt.input); got != tt.want {
				t.Fatalf("findFilesNeedsEmbedder(%+v) = %t, want %t", tt.input, got, tt.want)
			}
		})
	}
}

func TestRunAIBuiltinFindFilesPathsSkipsLocalEmbeddingRuntime(t *testing.T) {
	setTestConfigDirectory(t)
	if err := config.SaveAI(config.AIConfig{
		Configured:                   true,
		Mode:                         "local",
		ConsentGiven:                 true,
		LocalEmbeddingConsentVersion: localEmbeddingConsentVersion,
	}); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	term := newCommandTerminal(context.Background(), 80, 20)
	ctx := withCommandTerminal(context.Background(), execSess, SourceUserShell, term)
	var stdout, stderr bytes.Buffer
	terminal := newCommandTerminalSession(term, &stdout, true)
	err = runAIBuiltinTool(ctx, execSess, "find-files", nil, &stdout, &stderr, term, true, terminal)
	if err != nil {
		t.Fatalf("run default find-files: %v; stderr: %s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("default find-files produced no output")
	}
	if strings.Contains(stderr.String(), "embedding") {
		t.Fatalf("path-only find-files unexpectedly initialized embeddings: %s", stderr.String())
	}
}

func TestExecuteAIToolFindFilesAndMarkdownResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte("needle found\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	result, err := executeAITool(context.Background(), execSess, "find-files", []string{
		"--find-content", "needle",
		"--no-semantic",
	}, nil)
	if err != nil {
		t.Fatalf("execute find-files: %v", err)
	}
	for _, want := range []string{"### find_files", "result.txt", "line 1", "needle found", "\n  ```\n"} {
		if !strings.Contains(result, want) {
			t.Errorf("Markdown result missing %q:\n%s", want, result)
		}
	}
	if strings.Contains(result, "```text") {
		t.Fatalf("find_files result wrapped Markdown in a text fence:\n%s", result)
	}

	var toolsHelp strings.Builder
	tools := BuildKabibiTools(execSess)
	toolsHelp.WriteString(formatAIToolsHelp(tools))
	for _, want := range []string{"read-file", "edit-file", "write-file", "find-files", "exec", "term-read", "shell-history", "git-inspect", "browser-cdp"} {
		if !strings.Contains(toolsHelp.String(), want) {
			t.Errorf("AI tools help missing %q", want)
		}
	}
	if strings.Contains(toolsHelp.String(), "list-files") {
		t.Fatal("AI tools help retains the removed list-files tool")
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

func TestExecuteAIToolFindFilesUsesProvidedEmbedder(t *testing.T) {
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
	result, err := executeAITool(context.Background(), execSess, "find_files", []string{
		"--find-content", "where the browser canvas runtime starts",
	}, embedder)
	if err != nil {
		t.Fatalf("execute semantic find_files: %v", err)
	}
	for _, want := range []string{"mode: semantic-content", "* 1.00  `browser_worker.go`", "func startWorker() { launchCanvasRuntime() }", "Search stats: processed 1/1 files"} {
		if !strings.Contains(result, want) {
			t.Errorf("semantic find_files result missing %q:\n%s", want, result)
		}
	}
	if strings.Contains(result, "```text") || !strings.Contains(result, "```go") {
		t.Fatalf("semantic find_files did not preserve its Markdown code block:\n%s", result)
	}
	if embedder.queryCalls != 0 || embedder.batchCalls != 3 {
		t.Fatalf("embedder calls = (query %d, batch %d), want (0, 3) for query, candidate, and quote chunk", embedder.queryCalls, embedder.batchCalls)
	}
}

func TestExecuteAIToolFindFilesDirectoryScopesSearch(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(nested, "inside.txt"): "inside",
		filepath.Join(root, "outside.txt"):  "outside",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	for _, directory := range []string{"nested", nested} {
		result, err := executeAITool(context.Background(), execSess, "find_files", []string{
			"--directory", directory,
			"--find-paths", "**",
		}, nil)
		if err != nil {
			t.Fatalf("execute find_files with directory %q: %v", directory, err)
		}
		if !strings.Contains(result, "inside.txt") || strings.Contains(result, "outside.txt") {
			t.Errorf("find_files directory %q returned wrong entries:\n%s", directory, result)
		}
		if !strings.Contains(result, "(1 entries,") {
			t.Errorf("find_files directory %q result count is not scoped:\n%s", directory, result)
		}
	}
}

func TestFindFilesToolDoesNotSubscribeToStandaloneEscapeInput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()
	term := newCommandTerminal(context.Background(), 80, 20)
	ctx := withCommandTerminal(context.Background(), execSess, SourceUserShell, term)
	if err := term.WriteInput([]byte{0x1b}); err != nil {
		t.Fatalf("queue Escape without a control subscriber: %v", err)
	}

	result, err := executeAITool(ctx, execSess, "find-files", []string{"--find-paths", "**"}, nil)
	if err != nil {
		t.Fatalf("execute find_files tool: %v", err)
	}
	if !strings.Contains(result, "result.txt") {
		t.Fatalf("find_files tool result = %q", result)
	}
	if term.controlReaders != 0 {
		t.Fatal("AI find_files tool registered the standalone command's control-input subscription")
	}
	var key [1]byte
	if n, err := term.ReadInput(key[:]); err != nil || n != 1 || key[0] != 0x1b {
		t.Fatalf("tool consumed Escape input: ReadInput() = %d, %v, %q", n, err, key[:])
	}
}

func TestAIBuiltinMiddlewareDispatchesFindFiles(t *testing.T) {
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
		`ai find-files --find-content "needle" --no-semantic`,
		nil,
		SourceUserShell,
		nil,
	)
	if err != nil {
		t.Fatalf("run ai tool command: %v", err)
	}
	if record.ExitCode != 0 || !strings.Contains(record.Output, "result.txt") ||
		!strings.Contains(record.Output, "needle found") || strings.Contains(record.Output, "```text") {
		t.Fatalf("command result = (exit %d, %q)", record.ExitCode, record.Output)
	}

	for _, shellName := range []string{"sh", "bash", "kabibi"} {
		command := shellName + ` -c "ai find_files --find_content needle --no_semantic"`
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

func TestIsAIToolNameAcceptsNewToolAndRejectsLegacyListFiles(t *testing.T) {
	for _, name := range []string{"find_files", "find-files", "browser_cdp", "browser-cdp"} {
		if !IsAIToolName(name) {
			t.Errorf("IsAIToolName(%q) = false", name)
		}
	}
	for _, name := range []string{"list_files", "list-files"} {
		if IsAIToolName(name) {
			t.Errorf("IsAIToolName(%q) = true; legacy name must be removed", name)
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
