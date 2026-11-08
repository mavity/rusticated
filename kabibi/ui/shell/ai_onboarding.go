package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"ai"
	"github.com/mavity/rusticated/kabibi/tools/filelist"
	"mvdan.cc/sh/v3/interp"
)

const (
	aiControlHelp   = "help"
	aiControlSetup  = "setup"
	aiControlLogin  = "login"
	aiControlLogout = "logout"
	aiControlStatus = "status"
)

var aiControlSubcommands = []string{
	aiControlSetup,
	aiControlLogin,
	aiControlLogout,
	aiControlStatus,
	aiControlHelp,
}

var errOnboardingCancelled = ai.ErrAuthCancelled

// OnboardingPrompter is retained as a backwards-compatible adapter that can
// drive a TerminalSession or be driven by legacy unit tests.
type OnboardingPrompter interface {
	Choose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error)
	Confirm(ctx context.Context, title, message string) (bool, error)
	Input(ctx context.Context, title, message string, secret bool) (string, error)
}

// commandTerminalSession adapts an in-process *CommandTerminal and output writer
// to ai.TerminalSession, following the exact pattern of runPager in pager.go.
type commandTerminalSession struct {
	term         *CommandTerminal
	writer       io.Writer
	isTTY        bool
	onEmptyEnter func() bool
	onOpenURL    func(string) error
}

func newCommandTerminalSession(term *CommandTerminal, writer io.Writer, isTTY bool) *commandTerminalSession {
	return &commandTerminalSession{
		term:   term,
		writer: writer,
		isTTY:  isTTY && term != nil,
	}
}

func (s *commandTerminalSession) Read(p []byte) (int, error) {
	if s == nil || s.term == nil {
		return 0, io.EOF
	}
	return s.term.ReadInput(p)
}

func (s *commandTerminalSession) Write(p []byte) (int, error) {
	if s == nil || s.writer == nil {
		return len(p), nil
	}
	return s.writer.Write(p)
}

func (s *commandTerminalSession) Size() (int, int) {
	if s == nil || s.term == nil {
		return 80, 23
	}
	return s.term.Size()
}

func (s *commandTerminalSession) IsTTY() bool {
	return s != nil && s.isTTY && s.term != nil
}

func (s *commandTerminalSession) HandleEmptyEnter() (bool, error) {
	return s != nil && s.isTTY && s.onEmptyEnter != nil && s.onEmptyEnter(), nil
}

func (s *commandTerminalSession) OpenURL(target string) error {
	if s == nil || !s.isTTY || s.onOpenURL == nil {
		return fmt.Errorf("browser URL opening is unavailable in this terminal session")
	}
	return s.onOpenURL(target)
}

func (s *commandTerminalSession) SubscribeRawInput() (func(context.Context, []byte) (int, error), func()) {
	if s == nil || s.term == nil {
		return func(context.Context, []byte) (int, error) { return 0, io.EOF }, func() {}
	}
	release := s.term.SubscribeControlInput()
	readKey := func(ctx context.Context, p []byte) (int, error) {
		return s.term.ReadControlInputContext(ctx, p)
	}
	return readKey, release
}

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(p []byte) (int, error) {
	return f(p)
}

// openInteractiveTerminalSession attaches a temporary in-process CommandTerminal
// and liveOutputState block inside Shell (just like OnAICommandStart + RunCommand)
// so AI initialization and provider authentication run inside a virtual PTY block.
func (s *Shell) openInteractiveTerminalSession(ctx context.Context, title string) (ai.TerminalSession, func(exitCode int)) {
	if s == nil {
		return &ai.StdioTerminalSession{Reader: strings.NewReader(""), Writer: io.Discard}, func(int) {}
	}
	execSess := s.execSess
	cols, rows := 78, 20
	var cwd string
	if execSess != nil {
		cols, rows = execSess.AIExecPTYSize()
		cwd = execSess.Dir()
	}

	cmdTerm := newCommandTerminal(ctx, cols, rows)
	var prevTerm *CommandTerminal
	if execSess != nil {
		execSess.terminalMu.Lock()
		prevTerm = execSess.activeTerminal
		execSess.activeTerminal = cmdTerm
		execSess.terminalMu.Unlock()
	}

	onChunk, onFinish := s.OnAICommandStart(title, cwd)
	s.mu.Lock()
	prevStatus := s.statusLine
	s.statusLine = "awaiting AI setup..."
	s.mu.Unlock()
	startedAt := time.Now()
	var closeOnce sync.Once
	closeFn := func(exitCode int) {
		closeOnce.Do(func() {
			s.mu.Lock()
			if s.statusLine == "awaiting AI setup..." {
				s.statusLine = prevStatus
			}
			s.mu.Unlock()
			if execSess != nil {
				execSess.terminalMu.Lock()
				if execSess.activeTerminal == cmdTerm {
					execSess.activeTerminal = prevTerm
				}
				execSess.terminalMu.Unlock()
			}
			if onFinish != nil {
				onFinish(exitCode, time.Since(startedAt))
			}
		})
	}

	writer := writeFunc(func(p []byte) (int, error) {
		if onChunk != nil {
			onChunk(p)
		}
		return len(p), nil
	})
	term := newCommandTerminalSession(cmdTerm, writer, true)
	term.onEmptyEnter = s.openLatestOutputURL
	term.onOpenURL = s.OpenURL
	return term, closeFn
}

// prompterTerminalAdapter wraps an OnboardingPrompter over an ai.TerminalSession.
type terminalOnboardingPrompter struct {
	term ai.TerminalSession
}

func newTerminalOnboardingPrompter(term ai.TerminalSession) *terminalOnboardingPrompter {
	return &terminalOnboardingPrompter{term: term}
}

func (p *terminalOnboardingPrompter) Choose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	return ai.PromptSelect(ctx, p.term, title, message, options, defaultChoice)
}

func (p *terminalOnboardingPrompter) SearchChoose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	return ai.PromptSearchSelect(ctx, p.term, title, message, options, defaultChoice)
}

func (p *terminalOnboardingPrompter) SearchChooseCompact(ctx context.Context, title string, options []string, defaultChoice int) (int, error) {
	return ai.PromptSearchSelectCompact(ctx, p.term, title, options, defaultChoice)
}

func (p *terminalOnboardingPrompter) Confirm(ctx context.Context, title, message string) (bool, error) {
	return ai.PromptConfirm(ctx, p.term, title, message)
}

func (p *terminalOnboardingPrompter) Input(ctx context.Context, title, message string, secret bool) (string, error) {
	return ai.PromptLine(ctx, p.term, title, message, secret)
}

// TUIOnboardingPrompter opens a virtual PTY terminal session on Shell when needed.
type TUIOnboardingPrompter struct {
	shell *Shell
}

func NewTUIOnboardingPrompter(shell *Shell) *TUIOnboardingPrompter {
	return &TUIOnboardingPrompter{shell: shell}
}

func (p *TUIOnboardingPrompter) openTerminal(ctx context.Context, title string) (ai.TerminalSession, func(int)) {
	if p == nil || p.shell == nil {
		return &ai.StdioTerminalSession{Reader: strings.NewReader(""), Writer: io.Discard}, func(int) {}
	}
	return p.shell.openInteractiveTerminalSession(ctx, title)
}

func (p *TUIOnboardingPrompter) Choose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	term, closeFn := p.openTerminal(ctx, "ai setup")
	choice, err := ai.PromptSelect(ctx, term, title, message, options, defaultChoice)
	if err != nil {
		closeFn(1)
		return -1, err
	}
	closeFn(0)
	return choice, nil
}

func (p *TUIOnboardingPrompter) SearchChoose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	term, closeFn := p.openTerminal(ctx, "ai setup")
	choice, err := ai.PromptSearchSelect(ctx, term, title, message, options, defaultChoice)
	if err != nil {
		closeFn(1)
		return -1, err
	}
	closeFn(0)
	return choice, nil
}

func (p *TUIOnboardingPrompter) SearchChooseCompact(ctx context.Context, title string, options []string, defaultChoice int) (int, error) {
	term, closeFn := p.openTerminal(ctx, "ai setup")
	choice, err := ai.PromptSearchSelectCompact(ctx, term, title, options, defaultChoice)
	if err != nil {
		closeFn(1)
		return -1, err
	}
	closeFn(0)
	return choice, nil
}

func (p *TUIOnboardingPrompter) Confirm(ctx context.Context, title, message string) (bool, error) {
	term, closeFn := p.openTerminal(ctx, "ai setup")
	ok, err := ai.PromptConfirm(ctx, term, title, message)
	if err != nil || !ok {
		closeFn(1)
		return ok, err
	}
	closeFn(0)
	return true, nil
}

func (p *TUIOnboardingPrompter) Input(ctx context.Context, title, message string, secret bool) (string, error) {
	term, closeFn := p.openTerminal(ctx, "ai setup")
	val, err := ai.PromptLine(ctx, term, title, message, secret)
	if err != nil {
		closeFn(1)
		return "", err
	}
	closeFn(0)
	return val, nil
}

type StdioOnboardingPrompter struct {
	reader *bufio.Reader
	writer io.Writer
}

func NewStdioOnboardingPrompter(reader io.Reader, writer io.Writer) *StdioOnboardingPrompter {
	if reader == nil {
		reader = strings.NewReader("")
	}
	if writer == nil {
		writer = io.Discard
	}
	return &StdioOnboardingPrompter{reader: bufio.NewReader(reader), writer: writer}
}

func (p *StdioOnboardingPrompter) terminalSession() ai.TerminalSession {
	if p == nil {
		return &ai.StdioTerminalSession{Reader: strings.NewReader(""), Writer: io.Discard}
	}
	return &ai.StdioTerminalSession{Reader: p.reader, Writer: p.writer, TTY: false}
}

func (p *StdioOnboardingPrompter) Choose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	return ai.PromptSelect(ctx, p.terminalSession(), title, message, options, defaultChoice)
}

func (p *StdioOnboardingPrompter) SearchChoose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	return ai.PromptSearchSelect(ctx, p.terminalSession(), title, message, options, defaultChoice)
}

func (p *StdioOnboardingPrompter) SearchChooseCompact(ctx context.Context, title string, options []string, defaultChoice int) (int, error) {
	return ai.PromptSearchSelectCompact(ctx, p.terminalSession(), title, options, defaultChoice)
}

func (p *StdioOnboardingPrompter) Confirm(ctx context.Context, title, message string) (bool, error) {
	return ai.PromptConfirm(ctx, p.terminalSession(), title, message)
}

func (p *StdioOnboardingPrompter) Input(ctx context.Context, title, message string, secret bool) (string, error) {
	return ai.PromptLine(ctx, p.terminalSession(), title, message, secret)
}

// aiBuiltinMiddleware intercepts AI control and direct tool commands and runs
// them in-process over CommandTerminal, just like `git log` in git_builtin.go.
func aiBuiltinMiddleware(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 || args[0] != "ai" {
			return next(ctx, args)
		}
		if execSess, _ := execSessionFromContext(ctx); execSess != nil && !execSess.AIEnabled() {
			hc := interp.HandlerCtx(ctx)
			fmt.Fprintln(hc.Stderr, "ai: unavailable in this shell")
			return interp.ExitStatus(127)
		}
		return runAIBuiltin(ctx, args[1:])
	}
}

func runAIBuiltin(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	ctx = withShellInvocationState(ctx, hc)
	execSess, _ := execSessionFromContext(ctx)
	cmdTerm := commandTerminalFromContext(ctx)
	isTTY := false
	if execSess != nil && cmdTerm != nil {
		isTTY = execSess.StreamTTY(ctx, hc).Stdout
	}
	var term ai.TerminalSession
	if cmdTerm != nil {
		commandTerm := newCommandTerminalSession(cmdTerm, hc.Stdout, isTTY)
		if execSess != nil {
			execSess.bgMu.Lock()
			uiSink := execSess.uiSink
			execSess.bgMu.Unlock()
			if opener, ok := uiSink.(ai.BrowserURLOpener); ok {
				commandTerm.onOpenURL = opener.OpenURL
			}
		}
		term = commandTerm
	} else {
		term = &ai.StdioTerminalSession{Reader: hc.Stdin, Writer: hc.Stdout, TTY: false}
	}

	sub := aiControlSetup
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case aiControlHelp, "--help", "-h":
		help := `Kabibi built-in AI control

  ai setup              Configure local Gemma or cloud AI provider
  ai login [provider]   Authenticate with a cloud provider (openrouter, anthropic, openai, github-copilot, gemini)
  ai logout [provider]  Remove stored credentials for a provider
  ai status             Show current AI configuration and stored credentials

` + formatAIToolsHelp(BuildKabibiTools(execSess))
		return writeAIBuiltinOutput(hc.Stdout, hc.Stderr, "ai help", help)
	case aiControlStatus:
		return runAIStatusBuiltin(ctx, hc.Stdout)
	case aiControlLogout:
		providerArg := ""
		if len(args) > 1 {
			providerArg = strings.ToLower(strings.TrimSpace(args[1]))
		}
		return runAILogoutBuiltin(ctx, term, providerArg)
	case aiControlLogin:
		providerArg := ""
		if len(args) > 1 {
			providerArg = strings.ToLower(strings.TrimSpace(args[1]))
		}
		runner := NewLazyAIRunner(execSess)
		if _, err := runner.configureRemoteProviderWithTerm(ctx, term, providerArg); err != nil {
			if errors.Is(err, errOnboardingCancelled) {
				fmt.Fprintln(hc.Stderr, "ai login: cancelled")
				return interp.ExitStatus(1)
			}
			fmt.Fprintf(hc.Stderr, "ai login: %v\n", err)
			return interp.ExitStatus(1)
		}
		return nil
	case aiControlSetup:
		runner := NewLazyAIRunner(execSess)
		if _, err := runner.configureAIWithTerm(ctx, term); err != nil {
			if errors.Is(err, errOnboardingCancelled) {
				fmt.Fprintln(hc.Stderr, "ai setup: cancelled")
				return interp.ExitStatus(1)
			}
			fmt.Fprintf(hc.Stderr, "ai setup: %v\n", err)
			return interp.ExitStatus(1)
		}
		return nil
	default:
		return runAIBuiltinTool(ctx, execSess, sub, args[1:], hc.Stdout, hc.Stderr, cmdTerm, isTTY, term)
	}
}

func runAIBuiltinTool(
	ctx context.Context,
	execSess *ExecutionSession,
	name string,
	args []string,
	stdout, stderr io.Writer,
	cmdTerm *CommandTerminal,
	isTTY bool,
	term ai.TerminalSession,
) error {
	if !IsAIToolName(name) {
		fmt.Fprintf(stderr, "ai: unknown subcommand %q (try 'ai --help')\n", name)
		return interp.ExitStatus(2)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		tools := BuildKabibiTools(execSess)
		return writeAIBuiltinOutput(stdout, stderr, "ai "+name+" --help", formatAIToolHelp(normalizeAIToolName(name), tools[normalizeAIToolName(name)]))
	}

	var err error
	isFindFiles := normalizeAIToolName(name) == "find_files"
	var findFilesInput FindFilesInput
	if isFindFiles && !(len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		findFilesInput, err = parseFindFilesInput(execSess, args)
		if err != nil {
			fmt.Fprintf(stderr, "ai find_files: %v\n", err)
			return interp.ExitStatus(1)
		}
	}

	interactiveFindFiles := isTTY && cmdTerm != nil && execSess != nil && isFindFiles
	if interactiveFindFiles {
		var stopEscapeInput func()
		ctx, stopEscapeInput = withEscapeCancellation(ctx, cmdTerm)
		defer stopEscapeInput()
	}

	needsEmbedder := false
	switch normalizeAIToolName(name) {
	case "find_files":
		needsEmbedder = findFilesNeedsEmbedder(findFilesInput)
	case "read_file":
		rawArguments, parseErr := parseAIToolArguments(BuildKabibiTools(execSess)["read_file"], args)
		if parseErr != nil {
			fmt.Fprintf(stderr, "ai read_file: %v\n", parseErr)
			return interp.ExitStatus(1)
		}
		var input ReadFileInput
		if parseErr := json.Unmarshal(rawArguments, &input); parseErr != nil {
			fmt.Fprintf(stderr, "ai read_file: decode arguments: %v\n", parseErr)
			return interp.ExitStatus(1)
		}
		needsEmbedder = strings.TrimSpace(input.Search) != ""
	case "git_inspect":
		rawArguments, parseErr := parseAIToolArguments(BuildKabibiTools(execSess)["git_inspect"], args)
		if parseErr != nil {
			fmt.Fprintf(stderr, "ai git_inspect: %v\n", parseErr)
			return interp.ExitStatus(1)
		}
		var input GitInspectInput
		if parseErr := json.Unmarshal(rawArguments, &input); parseErr != nil {
			fmt.Fprintf(stderr, "ai git_inspect: decode arguments: %v\n", parseErr)
			return interp.ExitStatus(1)
		}
		needsEmbedder = strings.EqualFold(strings.TrimSpace(input.Action), "log") &&
			strings.TrimSpace(input.Search) != ""
	}

	var embedder ai.PartEmbedder
	if needsEmbedder {
		setup := func() error {
			runner := NewLazyAIRunner(execSess)
			defer runner.Close()
			_, err := runner.configureAIWithTerm(ctx, term)
			return err
		}
		var closeEmbedder func()
		var diagnostic string
		embedder, closeEmbedder, diagnostic, err = resolveOneShotAIToolEmbedder(ctx, setup)
		if err != nil {
			fmt.Fprintf(stderr, "ai %s: %v\n", normalizeAIToolName(name), err)
			return interp.ExitStatus(1)
		}
		if closeEmbedder != nil {
			defer closeEmbedder()
		}
		if diagnostic != "" {
			fmt.Fprintf(stderr, "ai: %s\n", diagnostic)
		}
	}
	width, height := 0, 0
	if isTTY && cmdTerm != nil {
		width, height = cmdTerm.Size()
	}
	var result string
	if interactiveFindFiles {
		result, err = executeInteractiveFindFilesInput(ctx, execSess, findFilesInput, embedder, stdout, width, height)
	} else {
		result, err = executeAITool(ctx, execSess, name, args, embedder)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return interp.ExitStatus(1)
	}
	if isTTY && cmdTerm != nil {
		result, err = renderAssistantMarkdown(result, width)
		if err != nil {
			fmt.Fprintf(stderr, "ai %s: render Markdown: %v\n", normalizeAIToolName(name), err)
			return interp.ExitStatus(1)
		}
	}
	return writeAIBuiltinOutput(stdout, stderr, "ai "+normalizeAIToolName(name), result)
}

func executeInteractiveFindFilesWithControlInput(ctx context.Context, term *CommandTerminal, execSess *ExecutionSession, args []string, embedder ai.PartEmbedder, stdout io.Writer, width, availableHeight int) (string, error) {
	searchCtx, stop := withEscapeCancellation(ctx, term)
	defer stop()

	return executeInteractiveFindFiles(searchCtx, execSess, args, embedder, stdout, width, availableHeight)
}

func withEscapeCancellation(ctx context.Context, term *CommandTerminal) (context.Context, func()) {
	searchCtx, cancel := context.WithCancel(ctx)
	release := term.SubscribeControlInput()

	go func() {
		var key [64]byte
		for {
			n, err := term.ReadControlInputContext(searchCtx, key[:])
			if err != nil {
				return
			}
			if n == 1 && key[0] == 0x1b {
				cancel()
				return
			}
		}
	}()

	return searchCtx, func() {
		cancel()
		release()
	}
}

func parseFindFilesInput(execSess *ExecutionSession, args []string) (FindFilesInput, error) {
	tool := BuildKabibiTools(execSess)["find_files"]
	rawArguments, err := parseAIToolArguments(tool, args)
	if err != nil {
		return FindFilesInput{}, err
	}
	var input FindFilesInput
	if err := json.Unmarshal(rawArguments, &input); err != nil {
		return FindFilesInput{}, fmt.Errorf("decode arguments: %w", err)
	}
	return input, nil
}

func findFilesNeedsEmbedder(input FindFilesInput) bool {
	return strings.TrimSpace(input.FindContent) != "" && !input.NoSemantic
}

func executeInteractiveFindFiles(ctx context.Context, execSess *ExecutionSession, args []string, embedder ai.PartEmbedder, stdout io.Writer, width, availableHeight int) (result string, retErr error) {
	input, err := parseFindFilesInput(execSess, args)
	if err != nil {
		return "", fmt.Errorf("ai find_files: %w", err)
	}
	return executeInteractiveFindFilesInput(ctx, execSess, input, embedder, stdout, width, availableHeight)
}

func executeInteractiveFindFilesInput(ctx context.Context, execSess *ExecutionSession, input FindFilesInput, embedder ai.PartEmbedder, stdout io.Writer, width, availableHeight int) (result string, retErr error) {
	if width <= 0 {
		width = 80
	}
	outputLimit := listFilesOutputMatchLimit(availableHeight)
	if outputLimit == 0 {
		return "", fmt.Errorf("terminal is too short to show find-files results while leaving three rows free")
	}
	if input.MaxResults <= 0 || input.MaxResults > outputLimit {
		input.MaxResults = outputLimit
	}
	options := findFilesOptions(ctx, input, embedder)
	options.MaxLineWidth = width
	options.QuoteLimit = listFilesProgressMatchLimit(availableHeight)
	if options.QuoteLimit == 0 {
		options.QuoteLimit = -1
	}
	options.LiveQuotes = options.QuoteLimit > 0
	cwd := shellInvocationDir(ctx, execSess)
	iter := filelist.NewSearchIterator(ctx, findFilesDirectory(cwd, input), options)
	defer iter.Close()
	output := newListFilesProgressOutput(stdout, width, listFilesProgressOutputRows(availableHeight))
	defer func() {
		if ctx.Err() == nil {
			if clearErr := output.clear(); clearErr != nil {
				clearErr = fmt.Errorf("clear ai find_files progress: %w", clearErr)
				if retErr == nil {
					result = ""
					retErr = clearErr
				} else {
					retErr = errors.Join(retErr, clearErr)
				}
			}
		}
	}()
	if err := publishInteractiveListFilesProgress(iter, output); err != nil {
		return "", fmt.Errorf("ai find_files: publish progress: %w", err)
	}
	table, err := iter.Result()
	if err != nil {
		return "", fmt.Errorf("ai find_files: %w", err)
	}
	return formatAIToolResultMarkdown("find_files", table)
}

func publishInteractiveListFilesProgress(iter *filelist.SearchIterator, output *listFilesProgressOutput) error {
	const publishInterval = 500 * time.Millisecond
	updates := iter.Updates()
	var lastPublished time.Time
	dirty := false
	updatesClosed := false
	var timer *time.Timer
	var timerC <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	publish := func(now time.Time) error {
		progress := iter.Current()
		if err := output.publish(&progress); err != nil {
			return err
		}
		lastPublished = now
		dirty = false
		return nil
	}
	for !updatesClosed || dirty {
		if updatesClosed && dirty {
			if err := publish(time.Now()); err != nil {
				return err
			}
			continue
		}
		select {
		case _, ok := <-updates:
			if !ok {
				updatesClosed = true
				continue
			}
			dirty = true
			now := time.Now()
			if lastPublished.IsZero() || now.Sub(lastPublished) >= publishInterval {
				if err := publish(now); err != nil {
					return err
				}
				continue
			}
			if timerC == nil {
				timer = time.NewTimer(time.Until(lastPublished.Add(publishInterval)))
				timerC = timer.C
			}
		case now := <-timerC:
			timer = nil
			timerC = nil
			if dirty {
				if err := publish(now); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func writeAIBuiltinOutput(stdout, stderr io.Writer, label, output string) error {
	n, err := io.WriteString(stdout, output)
	if err != nil {
		fmt.Fprintf(stderr, "%s: write output: %v\n", label, err)
		return interp.ExitStatus(1)
	}
	if n != len(output) {
		fmt.Fprintf(stderr, "%s: write output: %v\n", label, io.ErrShortWrite)
		return interp.ExitStatus(1)
	}
	return nil
}

func init() {
	_ = os.Stdin
}
