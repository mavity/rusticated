package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// InputRoute identifies how a user-submitted input line should be handled.
type InputRoute int

const (
	RouteEmpty   InputRoute = iota // Blank or comment-only line
	RouteBuiltin                   // Session/UI lifecycle command (exit, quit)
	RouteShell                     // Execute in the mvdan shell runner
	RouteAI                        // Route as a natural-language prompt to ai.Session
)

// CommandSource distinguishes commands entered directly by the user from
// commands executed by the AI agent's exec tool.
type CommandSource string

const (
	SourceUserShell CommandSource = "user_shell"
	SourceAITool    CommandSource = "ai_tool"
)

// CommandRecord stores the outcome of a single shell command execution for
// inspection by the user or the AI agent's shell_history tool.
type CommandRecord struct {
	ID        int           `json:"id"`
	Source    CommandSource `json:"source"`
	Command   string        `json:"command"`
	Cwd       string        `json:"cwd"`
	Output    string        `json:"output"`
	ExitCode  int           `json:"exit_code"`
	StartedAt time.Time     `json:"started_at"`
	Duration  string        `json:"duration"`
}

const maxHistoryRecordOutput = 32768

// coreutilNames enumerates the commands provided by mvdan.cc/sh/moreinterp/coreutils.
var coreutilNames = map[string]bool{
	"base64": true,
	"cat":    true,
	"chmod":  true,
	"cp":     true,
	"find":   true,
	"gunzip": true,
	"gzcat":  true,
	"gzip":   true,
	"ls":     true,
	"mkdir":  true,
	"mktemp": true,
	"mv":     true,
	"rm":     true,
	"shasum": true,
	"tar":    true,
	"touch":  true,
	"xargs":  true,
}

// dynamicWriter routes stdout/stderr writes from the persistent interp.Runner
// to the currently active command buffer in a thread-safe manner.
type dynamicWriter struct {
	mu     sync.Mutex
	target func(p []byte)
}

func (w *dynamicWriter) setTarget(fn func(p []byte)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.target = fn
}

func (w *dynamicWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	fn := w.target
	w.mu.Unlock()
	if fn != nil {
		cp := make([]byte, len(p))
		copy(cp, p)
		fn(cp)
	}
	return len(p), nil
}

// ExecutionSession manages the persistent mvdan.cc/sh/v3 runner, working
// directory, environment variables, command classification, and history records.
type ExecutionSession struct {
	mu                  sync.Mutex
	runMu               sync.Mutex
	runner              *interp.Runner
	streamOut           *dynamicWriter
	initialCwd          string
	records             []CommandRecord
	nextRecordID        int
	lastAITurnCommandID int
}

// NewExecutionSession creates a new persistent shell execution session rooted at dir.
func NewExecutionSession(dir string) (*ExecutionSession, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil || dir == "" {
			dir = "."
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	dw := &dynamicWriter{}
	r, err := createRunner(context.Background(), strings.NewReader(""), dw, dw, dir, nil)
	if err != nil {
		return nil, err
	}

	// Ensure runner.Vars and runner.Funcs maps are initialized by doing a no-op reset.
	r.Reset()

	return &ExecutionSession{
		runner:       r,
		streamOut:    dw,
		initialCwd:   dir,
		nextRecordID: 1,
	}, nil
}

// Dir returns the shell runner's current working directory.
func (es *ExecutionSession) Dir() string {
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.runner != nil && es.runner.Dir != "" {
		return es.runner.Dir
	}
	return es.initialCwd
}

// EnvMap returns a snapshot of the shell's current environment variables,
// layering runner.Vars on top of runner.Env and os.Environ().
func (es *ExecutionSession) EnvMap() map[string]string {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.envMapLocked()
}

func (es *ExecutionSession) envMapLocked() map[string]string {
	env := make(map[string]string)
	for _, pair := range os.Environ() {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	if es.runner != nil {
		if es.runner.Env != nil {
			es.runner.Env.Each(func(name string, vr expand.Variable) bool {
				if vr.IsSet() {
					env[name] = vr.String()
				}
				return true
			})
		}
		for name, vr := range es.runner.Vars {
			if vr.IsSet() {
				env[name] = vr.String()
			}
		}
	}
	return env
}

// ClassifyInput determines whether a line of user input should be handled as an
// immediate builtin, executed in the mvdan shell runner, or routed to AI chat.
func (es *ExecutionSession) ClassifyInput(input string) (InputRoute, *syntax.File) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return RouteEmpty, nil
	}

	// 1. Parse with mvdan.cc/sh/v3/syntax (LangBash by default).
	parser := syntax.NewParser(syntax.KeepComments(false))
	file, err := parser.Parse(strings.NewReader(input), "")
	if err != nil {
		// Any bash syntax error routes immediately to AI fallback.
		return RouteAI, nil
	}
	if len(file.Stmts) == 0 {
		return RouteEmpty, file
	}

	// Check for clean exit/quit builtins when entered as a standalone simple command.
	if len(file.Stmts) == 1 && !file.Stmts[0].Negated && !file.Stmts[0].Background && len(file.Stmts[0].Redirs) == 0 {
		if call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Assigns) == 0 && len(call.Args) == 1 {
			if lit := call.Args[0].Lit(); lit == "exit" || lit == "quit" {
				return RouteBuiltin, file
			}
		}
	}

	es.mu.Lock()
	defer es.mu.Unlock()

	if es.areStmtsExecutableLocked(file.Stmts) {
		return RouteShell, file
	}
	return RouteAI, nil
}

func (es *ExecutionSession) areStmtsExecutableLocked(stmts []*syntax.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	for _, stmt := range stmts {
		if !es.isStmtExecutableLocked(stmt) {
			return false
		}
	}
	return true
}

func (es *ExecutionSession) isStmtExecutableLocked(stmt *syntax.Stmt) bool {
	if stmt == nil {
		return false
	}
	if stmt.Cmd == nil {
		// Standalone redirections (e.g., "> file.txt") are valid shell statements.
		return len(stmt.Redirs) > 0
	}

	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		// Pure variable assignments (e.g., FOO=bar) are valid shell commands.
		if len(cmd.Args) == 0 {
			return len(cmd.Assigns) > 0
		}
		// If the user explicitly attached redirections, backgrounding, negation, or
		// inline env assignments (e.g. "FOO=1 ./run"), check the command or allow shell constructs.
		head := cmd.Args[0]
		if lit := head.Lit(); lit != "" {
			return es.isCommandNameResolvableLocked(lit)
		}
		// Dynamic head word (e.g., "$CMD", "$(which go)", or quoted executable):
		// treat explicit shell expansions/quotes at the head as shell intent.
		return hasShellExpansionOrQuoting(head)

	case *syntax.BinaryCmd:
		// For pipelines (|), logical chains (&&, ||), both sides should begin with
		// a valid shell statement so natural language with accidental operators doesn't misfire,
		// or at minimum the left-hand command must be a valid shell command.
		return es.isStmtExecutableLocked(cmd.X)

	case *syntax.Subshell, *syntax.Block, *syntax.IfClause, *syntax.WhileClause,
		*syntax.ForClause, *syntax.CaseClause, *syntax.ArithmCmd, *syntax.TestClause,
		*syntax.DeclClause, *syntax.FuncDecl, *syntax.TimeClause, *syntax.CoprocClause,
		*syntax.LetClause:
		return true

	default:
		return true
	}
}

func hasShellExpansionOrQuoting(w *syntax.Word) bool {
	if w == nil {
		return false
	}
	for _, part := range w.Parts {
		switch part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted, *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst:
			return true
		}
	}
	return false
}

func (es *ExecutionSession) isCommandNameResolvableLocked(name string) bool {
	if name == "" {
		return false
	}
	switch name {
	case "exit", "quit", "help":
		return true
	}

	// 1. POSIX / Bash builtin recognized by mvdan/sh.
	if interp.IsBuiltin(name) {
		return true
	}

	// 2. User-defined shell function in the active runner.
	if es.runner != nil && es.runner.Funcs != nil {
		if _, ok := es.runner.Funcs[name]; ok {
			return true
		}
	}

	// 3. Embedded u-root coreutils builtin.
	if coreutilNames[name] {
		return true
	}

	cwd := es.initialCwd
	if es.runner != nil && es.runner.Dir != "" {
		cwd = es.runner.Dir
	}
	envMap := es.envMapLocked()

	// 4. Explicit relative or absolute path (e.g. ./script.sh, ../bin/tool, C:\tool.exe).
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		candidate := name
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(cwd, candidate)
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return true
		}
		if _, ok := resolvePlatformExecutable(name, cwd, envMap); ok {
			return true
		}
		return false
	}

	// 5. Platform-specific executable lookup (including Windows PATHEXT & %VAR% expansion).
	if _, ok := resolvePlatformExecutable(name, cwd, envMap); ok {
		return true
	}

	// 6. Standard PATH lookup via mvdan interp.LookPathDir.
	if es.runner != nil && es.runner.Env != nil {
		if _, err := interp.LookPathDir(cwd, es.runner.Env, name); err == nil {
			return true
		}
	}
	envPairs := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envPairs = append(envPairs, k+"="+v)
	}
	if _, err := interp.LookPathDir(cwd, expand.ListEnviron(envPairs...), name); err == nil {
		return true
	}

	return false
}

// RunCommand executes a shell command string (or pre-parsed syntax.File) inside
// the persistent mvdan runner, streaming raw output chunks to onChunk and recording
// a CommandRecord in the session history.
func (es *ExecutionSession) RunCommand(
	ctx context.Context,
	command string,
	parsed *syntax.File,
	source CommandSource,
	onChunk func(chunk []byte),
) (CommandRecord, error) {
	es.runMu.Lock()
	defer es.runMu.Unlock()

	start := time.Now()
	if parsed == nil {
		parser := syntax.NewParser()
		var err error
		parsed, err = parser.Parse(strings.NewReader(command), "")
		if err != nil {
			rec := es.appendRecord(source, command, es.Dir(), err.Error()+"\n", 2, start)
			if onChunk != nil {
				onChunk([]byte(err.Error() + "\n"))
			}
			return rec, err
		}
	}

	var outBuf bytes.Buffer
	var outMu sync.Mutex
	es.streamOut.setTarget(func(p []byte) {
		outMu.Lock()
		outBuf.Write(p)
		outMu.Unlock()
		if onChunk != nil {
			onChunk(p)
		}
	})
	defer es.streamOut.setTarget(nil)

	runErr := es.runner.Run(ctx, parsed)
	exitCode := 0
	if runErr != nil {
		if status, ok := interp.IsExitStatus(runErr); ok {
			exitCode = int(status)
		} else {
			exitCode = 1
			errLine := fmt.Sprintf("%v\n", runErr)
			outMu.Lock()
			if !strings.Contains(outBuf.String(), runErr.Error()) {
				outBuf.WriteString(errLine)
				if onChunk != nil {
					onChunk([]byte(errLine))
				}
			}
			outMu.Unlock()
		}
	}

	outMu.Lock()
	rawOut := outBuf.String()
	outMu.Unlock()

	rec := es.appendRecord(source, command, es.Dir(), rawOut, exitCode, start)
	return rec, runErr
}

func (es *ExecutionSession) appendRecord(
	source CommandSource,
	command string,
	cwd string,
	output string,
	exitCode int,
	startedAt time.Time,
) CommandRecord {
	es.mu.Lock()
	defer es.mu.Unlock()

	trimmedOut := output
	if len(trimmedOut) > maxHistoryRecordOutput {
		trimmedOut = trimmedOut[:maxHistoryRecordOutput] + "\n...[truncated]"
	}

	rec := CommandRecord{
		ID:        es.nextRecordID,
		Source:    source,
		Command:   command,
		Cwd:       cwd,
		Output:    trimmedOut,
		ExitCode:  exitCode,
		StartedAt: startedAt,
		Duration:  time.Since(startedAt).Round(time.Millisecond).String(),
	}
	es.nextRecordID++
	es.records = append(es.records, rec)
	return rec
}

// Records returns a copy of all recorded shell commands.
func (es *ExecutionSession) Records() []CommandRecord {
	es.mu.Lock()
	defer es.mu.Unlock()
	out := make([]CommandRecord, len(es.records))
	copy(out, es.records)
	return out
}

// QueryHistory returns command records filtered by sinceLastAITurn and capped at count.
func (es *ExecutionSession) QueryHistory(sinceLastAITurn bool, count int, includeOutput bool) []CommandRecord {
	es.mu.Lock()
	defer es.mu.Unlock()

	var filtered []CommandRecord
	for _, r := range es.records {
		if sinceLastAITurn && r.ID <= es.lastAITurnCommandID {
			continue
		}
		cp := r
		if !includeOutput {
			cp.Output = ""
		}
		filtered = append(filtered, cp)
	}

	if count <= 0 {
		count = 10
	}
	if len(filtered) > count {
		filtered = filtered[len(filtered)-count:]
	}
	return filtered
}

// MarkAITurnCompleted updates the watermark of commands seen by the AI session.
func (es *ExecutionSession) MarkAITurnCompleted() {
	es.mu.Lock()
	defer es.mu.Unlock()
	if len(es.records) > 0 {
		es.lastAITurnCommandID = es.records[len(es.records)-1].ID
	}
}

// LastAITurnCommandID returns the ID of the last command record at the end of the previous AI turn.
func (es *ExecutionSession) LastAITurnCommandID() int {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.lastAITurnCommandID
}
