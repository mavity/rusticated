package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	RouteEmpty      InputRoute = iota // Blank or comment-only line
	RouteBuiltin                      // Session/UI lifecycle command (exit, quit)
	RouteIncomplete                   // Valid shell input that needs more bytes before it can run
	RouteShell                        // Execute in the mvdan shell runner
	RouteAI                           // Route as a natural-language prompt to ai.Session
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
	ID            int           `json:"id"`
	Kind          BlockKind     `json:"kind"`
	Source        CommandSource `json:"source"`
	Command       string        `json:"command"`
	Cwd           string        `json:"cwd"`
	Output        string        `json:"output"`
	ExitCode      int           `json:"exit_code"`
	TypedAt       time.Time     `json:"typed_at,omitempty"`
	StartedAt     time.Time     `json:"started_at"`
	FirstOutputAt time.Time     `json:"first_output_at,omitempty"`
	CompletedAt   time.Time     `json:"completed_at,omitempty"`
	Duration      string        `json:"duration"`
	LineCount     int           `json:"line_count"`
	ByteLen       int           `json:"byte_len"`
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
	aiCols    int
	aiRows    int

	terminalMu       sync.Mutex
	activeTerminal   *CommandTerminal
	terminalAttached bool

	bgMu       sync.Mutex
	bgProcs    map[int]*ManagedPTY
	nextProcID int
	uiSink     AICommandUISink

	pathCache *pathCommandCache
}

// AICommandUISink allows the Shell widget to display live `SourceAITool` blocks
// (rendered with the `W - 2` left gutter stripe) when the AI executes commands.
type AICommandUISink interface {
	OnAICommandStart(command string, cwd string) (onChunk func([]byte), onFinish func(exitCode int, elapsed time.Duration))
}

// NewExecutionSession creates a new persistent shell execution session rooted at dir.
func NewExecutionSession(dir string) (*ExecutionSession, error) {
	dw := &dynamicWriter{}
	ttyPipeR, ttyPipeW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	_ = ttyPipeW.Close()
	es := &ExecutionSession{
		ttyIn:        ttyPipeR,
		streamOut:    dw,
		nextRecordID: 1,
		ptyCols:      80,
		ptyRows:      23,
		aiCols:       78,
		aiRows:       20,
		bgProcs:      make(map[int]*ManagedPTY),
		nextProcID:   1,
	}
	r, err := createRunnerForSession(context.Background(), es, ttyPipeR, dw, dw, dir, nil, nil)
	if err != nil {
		_ = ttyPipeR.Close()
		return nil, err
	}

	// Ensure runner.Vars and runner.Funcs maps are initialized by doing a no-op reset.
	r.Reset()

	es.runner = r
	es.initialCwd = r.Dir
	es.pathCache = newPathCommandCache()
	es.requestPathCacheRefresh(true)
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

// Close releases background cache work associated with the session.
func (es *ExecutionSession) Close() error {
	if es != nil && es.pathCache != nil {
		es.pathCache.close()
	}
	return nil
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

// ClassifyInput determines how user input should be handled without executing it.
func (es *ExecutionSession) ClassifyInput(input string) (InputRoute, *syntax.File) {
	return es.classifyInput(input, es.CanResolveCommand)
}

// ClassifyInputForDisplay classifies prompt text without doing PATH filesystem
// lookups on the UI render path. Actual command submission uses ClassifyInput,
// which resolves commands against the current filesystem.
func (es *ExecutionSession) ClassifyInputForDisplay(input string) (InputRoute, *syntax.File) {
	return es.classifyInput(input, es.canResolveCommandFromCache)
}

func (es *ExecutionSession) classifyInput(input string, canResolve func(string) bool) (InputRoute, *syntax.File) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return RouteEmpty, nil
	}

	parser := syntax.NewParser(syntax.KeepComments(false))
	file, err := parser.Parse(strings.NewReader(input), "")
	if err != nil {
		if !syntax.IsIncomplete(err) {
			return RouteAI, nil
		}
		if es.hasShellHead(file, input, canResolve) {
			return RouteIncomplete, file
		}
		return RouteAI, file
	}
	if len(file.Stmts) == 0 {
		return RouteEmpty, file
	}
	if hasTrailingContinuation(input) && es.hasShellHead(file, input, canResolve) {
		return RouteIncomplete, file
	}

	// Check for clean exit/quit builtins when entered as a standalone simple command.
	if len(file.Stmts) == 1 && !file.Stmts[0].Negated && !file.Stmts[0].Background && len(file.Stmts[0].Redirs) == 0 {
		if call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr); ok && len(call.Assigns) == 0 && len(call.Args) == 1 {
			if lit := call.Args[0].Lit(); lit == "exit" || lit == "quit" {
				return RouteBuiltin, file
			}
		}
	}

	head, compound, dynamic := firstCommandHead(file)
	if compound || dynamic || head == "" || es.hasShellHead(file, input, canResolve) {
		return RouteShell, file
	}
	return RouteAI, file
}

var shellKeywordHeads = map[string]struct{}{
	"!": {}, "case": {}, "coproc": {}, "do": {}, "done": {}, "elif": {}, "else": {},
	"esac": {}, "fi": {}, "for": {}, "function": {}, "if": {}, "in": {},
	"select": {}, "then": {}, "time": {}, "until": {}, "while": {},
}

func isShellKeyword(word string) bool {
	_, ok := shellKeywordHeads[word]
	return ok
}

func isAssignmentWord(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	return ok && syntax.ValidName(name)
}

func leadingWord(input string) string {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimLeft(fields[0], "{(")
}

func startsShellCompound(input string) bool {
	trimmed := strings.TrimLeftFunc(input, unicode.IsSpace)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "(")
}

func hasTrailingContinuation(input string) bool {
	trimmed := strings.TrimSuffix(input, "\n")
	trimmed = strings.TrimSuffix(trimmed, "\r")
	backslashes := 0
	for i := len(trimmed) - 1; i >= 0 && trimmed[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func firstCommandHead(file *syntax.File) (head string, compound, dynamic bool) {
	if file == nil || len(file.Stmts) == 0 {
		return "", false, false
	}
	cmd := file.Stmts[0].Cmd
	switch cmd.(type) {
	case *syntax.IfClause, *syntax.ForClause, *syntax.WhileClause,
		*syntax.CaseClause, *syntax.Block, *syntax.Subshell, *syntax.FuncDecl:
		return "", true, false
	}
	if cmd == nil {
		return "", false, false
	}
	found := false
	dynamic = false
	syntax.Walk(cmd, func(node syntax.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		head = call.Args[0].Lit()
		dynamic = head == ""
		found = true
		return false
	})
	return head, false, dynamic
}

func (es *ExecutionSession) hasShellHead(file *syntax.File, input string, canResolve func(string) bool) bool {
	head, compound, dynamic := firstCommandHead(file)
	word := leadingWord(input)
	return compound || dynamic || startsShellCompound(input) || isShellKeyword(head) ||
		isShellKeyword(word) || isAssignmentWord(word) ||
		canResolve(head) || canResolve(word)
}

func (es *ExecutionSession) requestPathCacheRefresh(immediate bool) {
	if es == nil || es.pathCache == nil {
		return
	}
	cwd := es.Dir()
	env := es.EnvMap()
	path, _ := lookupEnvValue(env, "PATH")
	pathExt, _ := lookupEnvValue(env, "PATHEXT")
	es.pathCache.schedule(pathCacheKey{
		cwd:         cwd,
		path:        path,
		pathExt:     pathExt,
		windowsLike: isWindowsLikeEnv(env),
	}, immediate)
}

func (es *ExecutionSession) canResolveCommandFromCache(name string) bool {
	if es == nil || name == "" {
		return false
	}
	if isAlwaysResolvedCommand(name) {
		return true
	}

	es.runMu.Lock()
	cwd := es.Dir()
	env := es.EnvMap()
	hasFunction := false
	if es.runner != nil && es.runner.Funcs != nil {
		_, hasFunction = es.runner.Funcs[name]
	}
	es.runMu.Unlock()
	if hasFunction {
		return true
	}

	if strings.ContainsAny(name, `/\`) || filepath.IsAbs(name) {
		return canResolveLocalScript(name, cwd)
	}

	if es.pathCache == nil {
		return false
	}
	path, _ := lookupEnvValue(env, "PATH")
	pathExt, _ := lookupEnvValue(env, "PATHEXT")
	key := pathCacheKey{
		cwd:         cwd,
		path:        path,
		pathExt:     pathExt,
		windowsLike: isWindowsLikeEnv(env),
	}
	return es.pathCache.contains(key, name)
}

func isAlwaysResolvedCommand(name string) bool {
	if interp.IsBuiltin(name) || (!strings.ContainsAny(name, `/\`) && isNestedShellBuiltinName(name)) {
		return true
	}
	switch name {
	case "help", "git", "ai", "web", "cdp":
		return true
	}
	_, ok := LookupURootCommand(name)
	return ok
}

func canResolveLocalScript(name, cwd string) bool {
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if strings.HasSuffix(strings.ToLower(name), ".sh") {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	var marker [2]byte
	_, readErr := file.Read(marker[:])
	_ = file.Close()
	return readErr == nil && string(marker[:]) == "#!"
}

// CanResolveCommand reports whether name would be handled by Kabibi's shell
// command chain, without launching it or changing session state.
func (es *ExecutionSession) CanResolveCommand(name string) bool {
	if es == nil || name == "" {
		return false
	}
	if isAlwaysResolvedCommand(name) {
		return true
	}

	es.runMu.Lock()
	defer es.runMu.Unlock()

	cwd := es.Dir()
	env := es.EnvMap()
	if es.runner != nil && es.runner.Funcs != nil {
		if _, ok := es.runner.Funcs[name]; ok {
			return true
		}
	}

	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		if strings.HasSuffix(strings.ToLower(name), ".sh") {
			return true
		}
		if f, err := os.Open(path); err == nil {
			var marker [2]byte
			_, readErr := f.Read(marker[:])
			_ = f.Close()
			if readErr == nil && string(marker[:]) == "#!" {
				return true
			}
		}
	}

	if _, ok := resolvePlatformExecutable(name, cwd, env); ok {
		return true
	}
	if es.runner != nil && es.runner.Env != nil {
		if _, err := interp.LookPathDir(cwd, es.runner.Env, name); err == nil {
			return true
		}
	}
	return false
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
	var commandTerminal *CommandTerminal
	if source == SourceUserShell {
		es.terminalMu.Lock()
		attached := es.terminalAttached
		es.terminalMu.Unlock()
		if attached {
			cols, rows := es.PTYSize()
			commandTerminal = newCommandTerminal(ctx, cols, rows)
			es.terminalMu.Lock()
			es.activeTerminal = commandTerminal
			es.terminalMu.Unlock()
			defer func() {
				es.terminalMu.Lock()
				if es.activeTerminal == commandTerminal {
					es.activeTerminal = nil
				}
				es.terminalMu.Unlock()
			}()
			runCtx = withCommandTerminal(runCtx, es, source, commandTerminal)
		}
	}
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

// RecordBlockWithTimings appends a completed block along with detailed user typing
// and output timing milestones.
func (es *ExecutionSession) RecordBlockWithTimings(
	kind BlockKind,
	source CommandSource,
	command string,
	output string,
	exitCode int,
	typedAt time.Time,
	startedAt time.Time,
	firstOutputAt time.Time,
	completedAt time.Time,
) CommandRecord {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	rec := es.appendRecord(kind, source, command, es.Dir(), output, exitCode, startedAt)
	es.mu.Lock()
	if n := len(es.records); n > 0 && es.records[n-1].ID == rec.ID {
		es.records[n-1].TypedAt = typedAt
		es.records[n-1].FirstOutputAt = firstOutputAt
		es.records[n-1].CompletedAt = completedAt
		rec = es.records[n-1]
	}
	es.mu.Unlock()
	return rec
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
func (es *ExecutionSession) ResizePTY(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid PTY size %dx%d", cols, rows)
	}
	es.ptyMu.Lock()
	es.ptyCols = cols
	es.ptyRows = rows
	p := es.activePTY
	es.ptyMu.Unlock()
	var resizeErr error
	if p != nil {
		if err := p.Resize(cols, rows); err != nil {
			resizeErr = fmt.Errorf("resize foreground PTY to %dx%d: %w", cols, rows, err)
		}
	}
	es.terminalMu.Lock()
	term := es.activeTerminal
	es.terminalMu.Unlock()
	if term != nil {
		term.Resize(cols, rows)
	}
	return resizeErr
}

// PTYSize returns the current PTY dimensions configured on the session.
func (es *ExecutionSession) PTYSize() (cols, rows int) {
	es.ptyMu.Lock()
	defer es.ptyMu.Unlock()
	if es.ptyCols <= 0 {
		es.ptyCols = 80
	}
	if es.ptyRows <= 0 {
		es.ptyRows = 23
	}
	return es.ptyCols, es.ptyRows
}

// AIExecPTYSize returns the compact terminal frame used by AI exec processes.
func (es *ExecutionSession) AIExecPTYSize() (cols, rows int) {
	es.ptyMu.Lock()
	defer es.ptyMu.Unlock()
	if es.aiCols <= 0 {
		es.aiCols = 78
	}
	if es.aiRows <= 0 {
		es.aiRows = 20
	}
	return es.aiCols, es.aiRows
}

// SetCommandTerminalAttached enables interactive command-terminal input when
// this session is hosted by a Shell widget.
func (es *ExecutionSession) SetCommandTerminalAttached(attached bool) {
	if es == nil {
		return
	}
	es.terminalMu.Lock()
	es.terminalAttached = attached
	if !attached {
		es.activeTerminal = nil
	}
	es.terminalMu.Unlock()
}

// ActiveCommandTerminal returns the current foreground command terminal.
func (es *ExecutionSession) ActiveCommandTerminal() *CommandTerminal {
	if es == nil {
		return nil
	}
	es.terminalMu.Lock()
	defer es.terminalMu.Unlock()
	return es.activeTerminal
}

// WriteCommandTerminalInput forwards user input to the active logical terminal.
func (es *ExecutionSession) WriteCommandTerminalInput(data []byte) bool {
	return es.WriteCommandTerminalInputError(data) == nil
}

// WriteCommandTerminalInputError forwards input and preserves the reason it
// could not be queued for Shell-side diagnostics.
func (es *ExecutionSession) WriteCommandTerminalInputError(data []byte) error {
	term := es.ActiveCommandTerminal()
	if term == nil {
		return ErrNoActiveCommandTerminal
	}
	return term.WriteInput(data)
}

// SetViewportSize updates foreground and AI terminal frames from outer Shell dimensions.
func (es *ExecutionSession) SetViewportSize(width, height int) error {
	if es == nil {
		return nil
	}
	cols, rows := childPTYFrameSize(width, height)
	aiCols, aiRows := aiExecPTYFrameSize(width, height)

	es.ptyMu.Lock()
	foregroundChanged := es.ptyCols != cols || es.ptyRows != rows
	aiChanged := es.aiCols != aiCols || es.aiRows != aiRows
	es.aiCols, es.aiRows = aiCols, aiRows
	es.ptyMu.Unlock()
	var resizeErrors []error
	if foregroundChanged {
		if err := es.ResizePTY(cols, rows); err != nil {
			resizeErrors = append(resizeErrors, err)
		}
	}
	if !aiChanged {
		return errors.Join(resizeErrors...)
	}

	es.bgMu.Lock()
	procs := make([]*ManagedPTY, 0, len(es.bgProcs))
	for _, proc := range es.bgProcs {
		procs = append(procs, proc)
	}
	es.bgMu.Unlock()
	for _, proc := range procs {
		if err := proc.Resize(aiCols, aiRows); err != nil {
			resizeErrors = append(resizeErrors, fmt.Errorf("resize managed PTY #%d to %dx%d: %w",
				proc.ID, aiCols, aiRows, err))
		}
	}
	return errors.Join(resizeErrors...)
}

type StreamTTYStatus struct {
	Stdin  bool
	Stdout bool
	Stderr bool
}

// StreamTTY reports terminal status independently for each effective stream.
func (es *ExecutionSession) StreamTTY(ctx context.Context, hc interp.HandlerContext) StreamTTYStatus {
	term := commandTerminalFromContext(ctx)
	if es == nil || term == nil {
		return StreamTTYStatus{}
	}
	return StreamTTYStatus{
		Stdin:  hc.Stdin == es.ttyIn,
		Stdout: hc.Stdout == es.streamOut,
		Stderr: hc.Stderr == es.streamOut,
	}
}

// IsUnredirectedTTY reports whether the given mvdan handler context is attached
// directly to the interactive shell's unredirected standard input/output/error.
func (es *ExecutionSession) IsUnredirectedTTY(hc interp.HandlerContext) bool {
	if es == nil {
		return false
	}
	return es.ActiveCommandTerminal() != nil &&
		hc.Stdin == es.ttyIn && hc.Stdout == es.streamOut && hc.Stderr == es.streamOut
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

// SetAICommandUISink attaches a UI sink for live SourceAITool command blocks.
func (es *ExecutionSession) SetAICommandUISink(sink AICommandUISink) {
	if es == nil {
		return
	}
	es.bgMu.Lock()
	es.uiSink = sink
	es.bgMu.Unlock()
}

// LookupManagedPTY looks up a managed PTY process by its 1-based ID.
func (es *ExecutionSession) LookupManagedPTY(id int) (*ManagedPTY, bool) {
	if es == nil {
		return nil, false
	}
	es.bgMu.Lock()
	defer es.bgMu.Unlock()
	m, ok := es.bgProcs[id]
	return m, ok
}

// RegisterManagedPTY registers a ManagedPTY in the session's background process table.
func (es *ExecutionSession) RegisterManagedPTY(m *ManagedPTY) int {
	es.bgMu.Lock()
	defer es.bgMu.Unlock()
	if es.bgProcs == nil {
		es.bgProcs = make(map[int]*ManagedPTY)
	}
	if es.nextProcID <= 0 {
		es.nextProcID = 1
	}
	m.ID = es.nextProcID
	es.nextProcID++
	es.bgProcs[m.ID] = m
	return m.ID
}

// ExecuteAIToolCommand runs a command for the AI's `exec` tool with `Cols: max(20, termWidth - 2)`,
// live UI streaming into a `SourceAITool` CommandBlock, and automatic timeout promotion into `bgProcs`.
func (es *ExecutionSession) ExecuteAIToolCommand(
	ctx context.Context,
	command string,
	timeoutSec int,
	keepANSI bool,
) (formatted string, procID int, stillRunning bool, exitCode int, err error) {
	if timeoutSec <= 0 {
		timeoutSec = 15
	}
	cwd := es.Dir()
	ptyCols, ptyRows := es.AIExecPTYSize()

	es.bgMu.Lock()
	sink := es.uiSink
	es.bgMu.Unlock()

	startedAt := time.Now()
	m := &ManagedPTY{
		Command:   command,
		Cwd:       cwd,
		Cols:      ptyCols,
		Rows:      ptyRows,
		StartedAt: startedAt,
		notifyCh:  make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
	procID = es.RegisterManagedPTY(m)
	var uiOnFinish func(int, time.Duration)
	if sink != nil {
		m.onChunk, uiOnFinish = sink.OnAICommandStart(command, cwd)
	}

	m.onFinish = func(code int, elapsed time.Duration) {
		m.mu.Lock()
		rawOut := m.rawBuf.String()
		m.mu.Unlock()
		es.appendRecord(BlockCommand, SourceAITool, command, es.Dir(), rawOut, code, startedAt)
		if uiOnFinish != nil {
			uiOnFinish(code, elapsed)
		}
	}

	go es.runManagedExecution(m, command, cwd, ptyCols, ptyRows)

	timeoutDur := time.Duration(timeoutSec) * time.Second
	timer := time.NewTimer(timeoutDur)
	defer timer.Stop()

	select {
	case <-m.doneCh:
		outText, _, code, elapsed := m.Snapshot(false, keepANSI)
		finalCwd := es.Dir()
		footer := fmt.Sprintf("[exit: %d | elapsed: %.3fs | cwd: %s]", code, elapsed.Seconds(), filepath.ToSlash(finalCwd))
		if outText != "" {
			formatted = outText + "\n" + footer
		} else {
			formatted = footer
		}
		return formatted, procID, false, code, nil

	case <-timer.C:
		outText, exited, code, elapsed := m.Snapshot(false, keepANSI)
		if exited {
			finalCwd := es.Dir()
			footer := fmt.Sprintf("[exit: %d | elapsed: %.3fs | cwd: %s]", code, elapsed.Seconds(), filepath.ToSlash(finalCwd))
			if outText != "" {
				formatted = outText + "\n" + footer
			} else {
				formatted = footer
			}
			return formatted, procID, false, code, nil
		}
		footer := fmt.Sprintf("[status: STILL RUNNING (pid_id: %d) after %.3fs | use term_read, term_input, or term_kill with id=%d]",
			procID, elapsed.Seconds(), procID)
		if outText != "" {
			formatted = outText + "\n" + footer
		} else {
			formatted = footer
		}
		return formatted, procID, true, 0, nil

	case <-ctx.Done():
		_ = m.Kill("SIGTERM")
		return "", procID, false, 130, ctx.Err()
	}
}

func (es *ExecutionSession) runManagedExecution(m *ManagedPTY, command string, cwd string, ptyCols, ptyRows int) {
	es.runMu.Lock()

	parser := syntax.NewParser()
	parsed, parseErr := parser.Parse(strings.NewReader(command), "")
	if parseErr != nil {
		es.runMu.Unlock()
		m.appendOutput([]byte(parseErr.Error() + "\n"))
		m.markExited(2, parseErr)
		return
	}

	if cmd, ok := es.tryBuildDirectPTYCommandLocked(parsed, cwd); ok {
		es.runMu.Unlock()
		if es.runDirectCommandInPTY(m, cmd, ptyCols, ptyRows) {
			return
		}
		es.runMu.Lock()
	}

	runCtx := withExecSession(context.Background(), es, SourceAITool)
	es.streamOut.setTarget(func(p []byte) {
		m.appendOutput(p)
	})

	runErr := es.runner.Run(runCtx, parsed)
	es.streamOut.setTarget(nil)
	es.runMu.Unlock()

	code := 0
	if runErr != nil {
		if status, ok := interp.IsExitStatus(runErr); ok {
			code = int(status)
		} else {
			code = 1
			errLine := fmt.Sprintf("%v\n", runErr)
			m.appendOutput([]byte(errLine))
		}
	}
	m.markExited(code, runErr)
}

func (es *ExecutionSession) tryBuildDirectPTYCommandLocked(parsed *syntax.File, cwd string) (*exec.Cmd, bool) {
	if parsed == nil || len(parsed.Stmts) != 1 {
		return nil, false
	}
	st := parsed.Stmts[0]
	if st.Negated || st.Background || len(st.Redirs) > 0 {
		return nil, false
	}
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return nil, false
	}
	var litArgs []string
	for _, w := range call.Args {
		lit := w.Lit()
		if lit == "" {
			return nil, false
		}
		litArgs = append(litArgs, lit)
	}
	head := litArgs[0]
	if interp.IsBuiltin(head) || isNestedShellBuiltinName(head) {
		return nil, false
	}
	if es.runner != nil && es.runner.Funcs != nil {
		if _, ok := es.runner.Funcs[head]; ok {
			return nil, false
		}
	}
	switch head {
	case "base64", "cat", "chmod", "cp", "find", "gunzip", "gzcat", "gzip",
		"ls", "mkdir", "mktemp", "mv", "rm", "shasum", "tar", "touch", "xargs":
		return nil, false
	}
	if strings.HasSuffix(strings.ToLower(head), ".sh") {
		return nil, false
	}

	envMap := es.envMapLocked()
	resolved, ok := resolvePlatformExecutable(head, cwd, envMap)
	if !ok {
		if es.runner != nil && es.runner.Env != nil {
			if p, err := interp.LookPathDir(cwd, es.runner.Env, head); err == nil {
				resolved = p
				ok = true
			}
		}
	}
	if !ok {
		return nil, false
	}

	cmd := exec.Command(resolved, litArgs[1:]...)
	cmd.Dir = cwd
	envPairs := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envPairs = append(envPairs, k+"="+v)
	}
	cmd.Env = envPairs
	return cmd, true
}

func (es *ExecutionSession) runDirectCommandInPTY(m *ManagedPTY, cmd *exec.Cmd, cols, rows int) bool {
	p, err := pty.Start(cmd, pty.Options{
		Cols: cols,
		Rows: rows,
		Term: "xterm-256color",
	})
	if err != nil {
		return false
	}

	m.mu.Lock()
	m.ptyHandle = p
	m.cmd = cmd
	resizeCols, resizeRows := m.Cols, m.Rows
	m.mu.Unlock()
	if resizeCols > 0 && resizeRows > 0 && (resizeCols != cols || resizeRows != rows) {
		_ = p.Resize(resizeCols, resizeRows)
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, rerr := p.Read(buf)
			if n > 0 {
				m.appendOutput(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	waitErr := p.Wait()
	_ = p.Close()
	<-readDone

	code := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	m.markExited(code, waitErr)
	return true
}
