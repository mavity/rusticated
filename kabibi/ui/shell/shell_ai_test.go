package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type fakeAIRunner struct {
	prompts []string
	fn      func(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error
}

func (f *fakeAIRunner) RunTurn(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error {
	f.prompts = append(f.prompts, prompt)
	if f.fn != nil {
		return f.fn(ctx, prompt, onFeedback, onContent)
	}
	if onContent != nil {
		onContent("AI response for: " + prompt)
	}
	return nil
}

func (f *fakeAIRunner) Close() {}

func TestClassifyInput_BashSyntaxAndCommandResolution(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "myscript.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatalf("NewExecutionSession: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  InputRoute
	}{
		{
			name:  "empty input",
			input: "   ",
			want:  RouteEmpty,
		},
		{
			name:  "exit builtin",
			input: "exit",
			want:  RouteBuiltin,
		},
		{
			name:  "quit builtin",
			input: "quit",
			want:  RouteBuiltin,
		},
		{
			name:  "posix builtin echo",
			input: `echo "hello world"`,
			want:  RouteShell,
		},
		{
			name:  "posix builtin cd",
			input: "cd ..",
			want:  RouteShell,
		},
		{
			name:  "u-root coreutil ls",
			input: "ls -la",
			want:  RouteShell,
		},
		{
			name:  "u-root coreutil cat in pipeline",
			input: "cat file.txt | wc",
			want:  RouteShell,
		},
		{
			name:  "variable assignment",
			input: "FOO=bar",
			want:  RouteShell,
		},
		{
			name:  "for loop construct",
			input: "for i in 1 2 3; do echo $i; done",
			want:  RouteShell,
		},
		{
			name:  "relative script in cwd",
			input: "./myscript.sh",
			want:  RouteShell,
		},
		{
			name:  "host executable go",
			input: "go version",
			want:  RouteShell,
		},
		{
			name:  "syntax error unmatched apostrophe",
			input: "What's the largest file in this directory?",
			want:  RouteAI,
		},
		{
			name:  "syntax error parens in natural language",
			input: "explain (in detail) why the build broke",
			want:  RouteAI,
		},
		{
			name:  "valid syntax is handed to mvdan/sh first (fallback decided at execution time)",
			input: "Name a bridge in Paris",
			want:  RouteShell,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := es.ClassifyInput(tc.input)
			if got != tc.want {
				t.Fatalf("ClassifyInput(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestShell_NestedBuiltins_BashShKabibi_AndFallbackPosition(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "greet.sh")
	if err := os.WriteFile(scriptPath, []byte("echo \"from_script:$1:$2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	aiMock := &fakeAIRunner{}
	sh := New(ShellOptions{
		WorkingDir: dir,
		AIRunner:   aiMock,
	})
	defer sh.Close()

	// 1. bash -c, sh -c, and kabibi -c execute in-process and support nesting + exported vars
	sh.ExecuteLine(`export SHARED_VAR=42; bash -c 'sh -c "kabibi -c '\''echo nested:\$SHARED_VAR:\$1'\'' _ arg_one"'`)
	blocks := sh.Blocks()
	last := blocks[len(blocks)-1]
	if got := strings.TrimSpace(last.Output.String()); got != "nested:42:arg_one" {
		t.Fatalf("nested bash/sh/kabibi output = %q, want %q", got, "nested:42:arg_one")
	}
	if len(aiMock.prompts) != 0 {
		t.Fatalf("expected 0 AI prompts, got %v", aiMock.prompts)
	}

	// 2. Running a script file via `sh greet.sh alpha beta` works in-process
	sh.ExecuteLine(`sh greet.sh alpha beta`)
	blocks = sh.Blocks()
	last = blocks[len(blocks)-1]
	if got := strings.TrimSpace(last.Output.String()); got != "from_script:alpha:beta" {
		t.Fatalf("script via sh output = %q, want %q", got, "from_script:alpha:beta")
	}

	// 3. Piping commands into `bash` via stdin works in-process
	sh.ExecuteLine(`echo 'echo piped_into_bash' | bash`)
	blocks = sh.Blocks()
	last = blocks[len(blocks)-1]
	if got := strings.TrimSpace(last.Output.String()); got != "piped_into_bash" {
		t.Fatalf("piped bash output = %q, want %q", got, "piped_into_bash")
	}

	// 4. `echo start; unknown_word_xyz` is treated as Bash (NOT routed to AI) because
	// unknown_word_xyz is not at the start of the line.
	sh.ExecuteLine(`echo start; unknown_word_xyz`)
	blocks = sh.Blocks()
	last = blocks[len(blocks)-1]
	if len(aiMock.prompts) != 0 {
		t.Fatalf("expected `echo start; unknown_word_xyz` to stay in Bash without AI fallback, got AI prompts %v", aiMock.prompts)
	}
	if last.ExitCode != 127 {
		t.Fatalf("expected exit code 127 for trailing unknown command, got %d (output=%q)", last.ExitCode, last.Output.String())
	}
	if !strings.Contains(last.Output.String(), "start") {
		t.Fatalf("expected output to contain 'start', got %q", last.Output.String())
	}

	// 5. `bash -c "unknown_word_xyz"` is treated as Bash (NOT routed to AI) because
	// the failure happens inside the nested shell, while outer `bash` is valid.
	sh.ExecuteLine(`bash -c "unknown_word_xyz"`)
	blocks = sh.Blocks()
	last = blocks[len(blocks)-1]
	if len(aiMock.prompts) != 0 {
		t.Fatalf("expected `bash -c unknown_word_xyz` to stay in Bash without AI fallback, got AI prompts %v", aiMock.prompts)
	}
	if last.ExitCode != 127 {
		t.Fatalf("expected exit code 127 for nested unknown command, got %d (output=%q)", last.ExitCode, last.Output.String())
	}

	// 6. Leading whitespace before an unknown word at the start of the line STILL routes to AI
	sh.ExecuteLine(`   Tell me something interesting`)
	if len(aiMock.prompts) != 1 || aiMock.prompts[0] != "   Tell me something interesting" {
		t.Fatalf("expected leading-whitespace unknown command to route to AI, got %v", aiMock.prompts)
	}
}

func TestShell_ExecutesValidBashAndFallsBackToAI(t *testing.T) {
	dir := t.TempDir()
	aiMock := &fakeAIRunner{
		fn: func(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error {
			// Simulate ai.Session live ticker with \r overwrites and wipe followed by spoken content
			onFeedback("[goal] Identify a famous Parisian bridge…")
			onFeedback("\r                                         \r[work] Thinking…")
			onFeedback("\r                                         \r")
			onContent("Pont Alexandre III.")
			return nil
		},
	}

	sh := New(ShellOptions{
		WorkingDir: dir,
		AIRunner:   aiMock,
	})
	defer sh.Close()

	// 1. Run a real shell command via Enter key handling
	sh.SetInput("echo hello_from_mvdan")
	sh.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	sh.WaitActive()

	blocks := sh.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (welcome + echo), got %d", len(blocks))
	}
	if got := strings.TrimSpace(blocks[1].Output.String()); got != "hello_from_mvdan" {
		t.Fatalf("shell command output = %q, want %q", got, "hello_from_mvdan")
	}
	if len(aiMock.prompts) != 0 {
		t.Fatalf("expected 0 AI calls for valid shell command, got %d", len(aiMock.prompts))
	}

	// 2. Run a natural language prompt that triggers AI fallback
	sh.ExecuteLine("Name a bridge in Paris")

	if len(aiMock.prompts) != 1 || aiMock.prompts[0] != "Name a bridge in Paris" {
		t.Fatalf("expected AI fallback with prompt %q, got %#v", "Name a bridge in Paris", aiMock.prompts)
	}

	blocks = sh.Blocks()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	// Verify that when rendered through renderBlock / FromANSI, the \r ticker was overwritten
	// by the final answer "Pont Alexandre III." without leftover "[goal]" or "[work]" text.
	rendered := renderBlock(blocks[2], 80)
	ansi := terminal.ToANSI(rendered)
	if !strings.Contains(ansi, "Pont") || !strings.Contains(ansi, "Alexandre") {
		t.Fatalf("rendered AI block missing final answer: %q", ansi)
	}
	if strings.Contains(ansi, "[goal]") || strings.Contains(ansi, "[work]") {
		t.Fatalf("rendered AI block leaked wiped ticker text: %q", ansi)
	}
}

func TestKabibiTools_RespectShellCwdAndInterstitialHistory(t *testing.T) {
	rootDir := t.TempDir()
	subDir := filepath.Join(rootDir, "subproject")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	es, err := NewExecutionSession(rootDir)
	if err != nil {
		t.Fatal(err)
	}

	// User runs commands in the shell between AI prompts, including changing directory
	if _, err := es.RunCommand(context.Background(), "echo first_cmd", nil, SourceUserShell, nil); err != nil {
		t.Fatal(err)
	}
	// Use filepath.ToSlash so bash doesn't strip Windows backslashes in unquoted paths
	if _, err := es.RunCommand(context.Background(), "cd "+filepath.ToSlash(subDir), nil, SourceUserShell, nil); err != nil {
		t.Fatal(err)
	}

	if got := filepath.Clean(es.Dir()); got != filepath.Clean(subDir) {
		t.Fatalf("expected shell cwd %q after cd, got %q", subDir, got)
	}

	tools := BuildKabibiTools(es)

	// 1. write_file with a relative path must write inside subDir (the shell's current cwd)
	writeTool := tools["write_file"]
	_, err = writeTool.Execute(context.Background(), ai.ToolCall{
		Name:         "write_file",
		RawArguments: json.RawMessage(`{"path":"notes.txt","content":"line1\nline2\nline3","explanation":"create notes"}`),
	}, nil)
	if err != nil {
		t.Fatalf("write_file failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(subDir, "notes.txt")); err != nil {
		t.Fatalf("expected notes.txt in shell cwd %q: %v", subDir, err)
	}

	// 2. read_file with relative path and line range reads from subDir
	readTool := tools["read_file"]
	readOut, err := readTool.Execute(context.Background(), ai.ToolCall{
		Name:         "read_file",
		RawArguments: json.RawMessage(`{"path":"notes.txt","start_line":2,"end_line":3,"explanation":"read lines 2-3"}`),
	}, nil)
	if err != nil {
		t.Fatalf("read_file failed: %v", err)
	}
	readMap := readOut.(map[string]any)
	if got := readMap["content"]; got != "line2\nline3" {
		t.Fatalf("read_file content = %q, want %q", got, "line2\nline3")
	}

	// 3. shell_history retrieves the interstitial user commands since last AI turn
	histTool := tools["shell_history"]
	histOut, err := histTool.Execute(context.Background(), ai.ToolCall{
		Name:         "shell_history",
		RawArguments: json.RawMessage(`{"since_last_ai_turn":true,"explanation":"check recent commands"}`),
	}, nil)
	if err != nil {
		t.Fatalf("shell_history failed: %v", err)
	}
	histMap := histOut.(map[string]any)
	cmds := histMap["commands"].([]CommandRecord)
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands since last AI turn, got %d", len(cmds))
	}
	if !strings.Contains(cmds[0].Output, "first_cmd") {
		t.Fatalf("expected first command output to contain 'first_cmd', got %q", cmds[0].Output)
	}

	// Mark AI turn completed and verify since_last_ai_turn is now empty until another command runs
	es.MarkAITurnCompleted()
	if got := es.QueryHistory(true, 10, true); len(got) != 0 {
		t.Fatalf("expected 0 commands after MarkAITurnCompleted, got %d", len(got))
	}

	// 4. exec tool executes inside Kabibi's shell runner in subDir
	execTool := tools["exec"]
	execOut, err := execTool.Execute(context.Background(), ai.ToolCall{
		Name:         "exec",
		RawArguments: json.RawMessage(`{"command":"cat notes.txt","explanation":"read notes via shell"}`),
	}, nil)
	if err != nil {
		t.Fatalf("exec tool failed: %v", err)
	}
	execMap := execOut.(map[string]any)
	if got := strings.TrimSpace(execMap["output"].(string)); got != "line1\nline2\nline3" {
		t.Fatalf("exec tool output = %q, want %q", got, "line1\nline2\nline3")
	}
}

func TestEndToEnd_ShellFallbackIntoAISessionWithTools(t *testing.T) {
	dir := t.TempDir()
	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}

	var feedbackLog strings.Builder
	var contentLog strings.Builder

	aiSess, err := ai.NewSession(ai.SessionConfig{
		SystemMessage: defaultKabibiSystemPrompt,
		Tools:         BuildKabibiTools(es),
		OnFeedback:    func(s string) { feedbackLog.WriteString(s) },
		OnContent:     func(s string) { contentLog.WriteString(s) },
	})
	if err != nil {
		t.Fatal(err)
	}

	sh := New(ShellOptions{
		WorkingDir: dir,
		AIRunner: &customSessionAIRunner{
			execSess: es,
			aiSess:   aiSess,
		},
	})
	defer sh.Close()

	// Step 1: User runs a real bash command in the shell that fails or produces output
	sh.ExecuteLine("echo build_failed_missing_header")

	// Verify it was recorded in shell history
	if recs := sh.ExecutionSession().Records(); len(recs) != 1 {
		t.Fatalf("expected 1 shell record, got %d", len(recs))
	}

	// Render check to ensure widget renders cleanly at 80x24
	_, _ = sh.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 24}, ui.RenderContext{Focused: true})
}

type customSessionAIRunner struct {
	execSess *ExecutionSession
	aiSess   *ai.Session
}

func (c *customSessionAIRunner) RunTurn(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error {
	return c.aiSess.RunTurn(ctx, prompt)
}

func (c *customSessionAIRunner) Close() {
	c.aiSess.Shutdown()
}
