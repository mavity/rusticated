package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
	"pty"
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
// commands executed by the AI agent's exec tool or system blocks.
type CommandSource string

const (
	SourceUserShell CommandSource = "user_shell"
	SourceAITool    CommandSource = "ai_tool"
	SourceSystem    CommandSource = "system"
)

// BlockKind classifies each interaction block in the shell history store.
type BlockKind string

const (
	BlockBanner  BlockKind = "banner"
	BlockCommand BlockKind = "command"
	BlockAITurn  BlockKind = "ai_turn"
)

// CommandRecord stores the full outcome of a single shell or AI interaction block
// for inspection by the user or the AI agent's shell_history / read_command_output tools.
type CommandRecord struct {
	ID        int           `json:"id"`
	Kind      BlockKind     `json:"kind"`
	Source    CommandSource `json:"source"`
	Command   string        `json:"command"`
	Cwd       string        `json:"cwd"`
	Output    string        `json:"output"`
	ExitCode  int           `json:"exit_code"`
	StartedAt time.Time     `json:"started_at"`
	Duration  string        `json:"duration"`
	LineCount int           `json:"line_count"`
	ByteLen   int           `json:"byte_len"`
}

const maxHistoryPreviewOutput = 32768

// shellTTYStdin is a sentinel reader used to detect when a command inside
// mvdan/sh is running with unredirected foreground standard input.
type shellTTYStdin struct{}

func (shellTTYStdin) Read([]byte) (int, error) {
	return 0, io.EOF
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
// directory, environment variables, command classification, active PTY, and history records.
type ExecutionSession struct {
	mu                  sync.Mutex
	runMu               sync.Mutex
	runner              *interp.Runner
	ttyIn               io.Reader
	streamOut           *dynamicWriter
	initialCwd          string
	records             []CommandRecord
	nextRecordID        int
	lastAITurnCommandID int

	ptyMu     sync.Mutex
	activePTY pty.PTY
	ptyCols   int
	ptyRows   int
}

// NewExecutionSession creates a new persistent shell execution session rooted at dir.
func NewExecutionSession(dir string) (*ExecutionSession, error) {
	dw := &dynamicWriter{}
	ttyIn := shellTTYStdin{}
	es := &ExecutionSession{
		ttyIn:        ttyIn,
		streamOut:    dw,
		nextRecordID: 1,
		ptyCols:      80,
		ptyRows:      24,
	}
	r, err := createRunnerForSession(context.Background(), es, ttyIn, dw, dw, dir, nil, nil)
	if err != nil {
		return nil, err
	}

	// Ensure runner.Vars and runner.Funcs maps are initialized by doing a no-op reset.
	r.Reset()

	es.runner = r
	es.initialCwd = r.Dir
	return es, nil
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

// SetDir updates the shell runner's working directory and PWD variable when
// synchronized from an external UI component (such as the active blue file panel).
func (es *ExecutionSession) SetDir(dir string) error {
	if es == nil || dir == "" {
		return nil
	}
	cleaned := filepath.Clean(dir)
	info, err := os.Stat(cleaned)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", cleaned)
	}
	es.mu.Lock()
	defer es.mu.Unlock()
	if es.runner != nil {
		es.runner.Dir = cleaned
		if es.runner.Vars != nil {
			es.runner.Vars["PWD"] = expand.Variable{
				Set:      true,
				Exported: true,
				Kind:     expand.String,
				Str:      cleaned,
			}
		}
	}
	es.initialCwd = cleaned
	return nil
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

// ClassifyInput determines whether a line of user input is empty, an immediate
// lifecycle builtin (exit/quit), a syntactically valid shell command to hand to
// mvdan/sh, or a syntax error that should route directly to AI chat.
func (es *ExecutionSession) ClassifyInput(input string) (InputRoute, *syntax.File) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return RouteEmpty, nil
	}

	// Parse with mvdan.cc/sh/v3/syntax (LangBash by default).
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

	return RouteShell, file
}

func firstNonSpaceOffset(s string) (uint, bool) {
	for i, r := range s {
		if !unicode.IsSpace(r) {
			return uint(i), true
		}
	}
	return 0, false
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
			rec := es.appendRecord(BlockCommand, source, command, es.Dir(), err.Error()+"\n", 2, start)
			if onChunk != nil {
				onChunk([]byte(err.Error() + "\n"))
			}
			return rec, err
		}
	}

	runCtx := withExecSession(ctx, es, source)
	if source == SourceUserShell {
		if offset, ok := firstNonSpaceOffset(command); ok {
			runCtx = withTopLevelStartOffset(runCtx, offset)
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

	runErr := es.runner.Run(runCtx, parsed)
	if errors.Is(runErr, ErrUnknownCommandAtStart) {
		// Do not record a shell history entry when an unrecognized leading word
		// is being handed off to AI fallback.
		return CommandRecord{ExitCode: 127}, ErrUnknownCommandAtStart
	}

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

	rec := es.appendRecord(BlockCommand, source, command, es.Dir(), rawOut, exitCode, start)
	return rec, runErr
}

// RecordBlock appends a completed banner, command, or AI turn block to the unified
// session history store without truncating the stored output.
func (es *ExecutionSession) RecordBlock(
	kind BlockKind,
	source CommandSource,
	command string,
	output string,
	exitCode int,
	startedAt time.Time,
) CommandRecord {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return es.appendRecord(kind, source, command, es.Dir(), output, exitCode, startedAt)
}

func countOutputLines(s string) int {
	if s == "" {
		return 0
	}
	norm := strings.ReplaceAll(s, "\r\n", "\n")
	norm = strings.TrimSuffix(norm, "\n")
	if norm == "" {
		return 1
	}
	return strings.Count(norm, "\n") + 1
}

func (es *ExecutionSession) appendRecord(
	kind BlockKind,
	source CommandSource,
	command string,
	cwd string,
	output string,
	exitCode int,
	startedAt time.Time,
) CommandRecord {
	es.mu.Lock()
	defer es.mu.Unlock()

	if kind == "" {
		kind = BlockCommand
	}
	rec := CommandRecord{
		ID:        es.nextRecordID,
		Kind:      kind,
		Source:    source,
		Command:   command,
		Cwd:       cwd,
		Output:    output,
		ExitCode:  exitCode,
		StartedAt: startedAt,
		Duration:  time.Since(startedAt).Round(time.Millisecond).String(),
		LineCount: countOutputLines(output),
		ByteLen:   len(output),
	}
	es.nextRecordID++
	es.records = append(es.records, rec)
	return rec
}

// SetActivePTY registers or clears the currently running foreground child PTY.
func (es *ExecutionSession) SetActivePTY(p pty.PTY) {
	es.ptyMu.Lock()
	es.activePTY = p
	cols, rows := es.ptyCols, es.ptyRows
	es.ptyMu.Unlock()
	if p != nil && cols > 0 && rows > 0 {
		_ = p.Resize(cols, rows)
	}
}

// HasActivePTY reports whether a foreground child process is currently attached to a PTY.
func (es *ExecutionSession) HasActivePTY() bool {
	es.ptyMu.Lock()
	defer es.ptyMu.Unlock()
	return es.activePTY != nil
}

// WriteActivePTY forwards raw terminal bytes to the currently running foreground PTY, if any.
func (es *ExecutionSession) WriteActivePTY(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	es.ptyMu.Lock()
	p := es.activePTY
	es.ptyMu.Unlock()
	if p == nil {
		return false
	}
	_, err := p.Write(data)
	return err == nil
}

// ResizePTY updates the terminal dimensions for future and active PTY sessions.
func (es *ExecutionSession) ResizePTY(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	es.ptyMu.Lock()
	es.ptyCols = cols
	es.ptyRows = rows
	p := es.activePTY
	es.ptyMu.Unlock()
	if p != nil {
		_ = p.Resize(cols, rows)
	}
}

// PTYSize returns the current PTY dimensions configured on the session.
func (es *ExecutionSession) PTYSize() (cols, rows int) {
	es.ptyMu.Lock()
	defer es.ptyMu.Unlock()
	if es.ptyCols <= 0 {
		es.ptyCols = 80
	}
	if es.ptyRows <= 0 {
		es.ptyRows = 24
	}
	return es.ptyCols, es.ptyRows
}

// IsUnredirectedTTY reports whether the given mvdan handler context is attached
// directly to the interactive shell's unredirected standard input/output/error.
func (es *ExecutionSession) IsUnredirectedTTY(hc interp.HandlerContext) bool {
	if es == nil {
		return false
	}
	return hc.Stdin == es.ttyIn && hc.Stdout == es.streamOut && hc.Stderr == es.streamOut
}

// RecordByID looks up a recorded block/command by its 1-based ID.
func (es *ExecutionSession) RecordByID(id int) (CommandRecord, bool) {
	es.mu.Lock()
	defer es.mu.Unlock()
	for _, r := range es.records {
		if r.ID == id {
			return r, true
		}
	}
	return CommandRecord{}, false
}

// Records returns a copy of all recorded shell commands and AI turns (excluding the startup banner).
func (es *ExecutionSession) Records() []CommandRecord {
	es.mu.Lock()
	defer es.mu.Unlock()
	out := make([]CommandRecord, 0, len(es.records))
	for _, r := range es.records {
		if r.Kind == BlockBanner {
			continue
		}
		out = append(out, r)
	}
	return out
}

// AllRecords returns a copy of all recorded blocks, including BlockBanner.
func (es *ExecutionSession) AllRecords() []CommandRecord {
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
		if r.Kind == BlockBanner {
			continue
		}
		if sinceLastAITurn && r.ID <= es.lastAITurnCommandID {
			continue
		}
		cp := r
		if !includeOutput {
			cp.Output = ""
		} else if len(cp.Output) > maxHistoryPreviewOutput {
			cp.Output = cp.Output[:maxHistoryPreviewOutput] + "\n...[truncated; use read_command_output with id to inspect full output]"
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
