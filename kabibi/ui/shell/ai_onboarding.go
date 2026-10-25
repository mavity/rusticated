package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"ai"
	"mvdan.cc/sh/v3/interp"
)

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

func (s *commandTerminalSession) HandleEmptyEnter() bool {
	return s != nil && s.isTTY && s.onEmptyEnter != nil && s.onEmptyEnter()
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

func (p *StdioOnboardingPrompter) Confirm(ctx context.Context, title, message string) (bool, error) {
	return ai.PromptConfirm(ctx, p.terminalSession(), title, message)
}

func (p *StdioOnboardingPrompter) Input(ctx context.Context, title, message string, secret bool) (string, error) {
	return ai.PromptLine(ctx, p.terminalSession(), title, message, secret)
}

// aiBuiltinMiddleware intercepts `ai` commands (`ai setup`, `ai login [provider]`,
// `ai logout [provider]`, `ai status`) and runs them in-process over CommandTerminal,
// just like `git log` in git_builtin.go.
func aiBuiltinMiddleware(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 || args[0] != "ai" {
			return next(ctx, args)
		}
		return runAIBuiltin(ctx, args[1:])
	}
}

func runAIBuiltin(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	execSess, _ := execSessionFromContext(ctx)
	cmdTerm := commandTerminalFromContext(ctx)
	isTTY := false
	if execSess != nil && cmdTerm != nil {
		isTTY = execSess.StreamTTY(ctx, hc).Stdout
	}
	var term ai.TerminalSession
	if cmdTerm != nil {
		term = newCommandTerminalSession(cmdTerm, hc.Stdout, isTTY)
	} else {
		term = &ai.StdioTerminalSession{Reader: hc.Stdin, Writer: hc.Stdout, TTY: false}
	}

	sub := "setup"
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case "help", "--help", "-h":
		fmt.Fprintln(hc.Stdout, `Kabibi built-in AI control

Usage:
  ai setup              Configure local Gemma or cloud AI provider
  ai login [provider]   Authenticate with a cloud provider (openrouter, anthropic, openai, github-copilot, gemini)
  ai logout [provider]  Remove stored credentials for a provider
  ai status             Show current AI configuration and stored credentials`)
		return nil
	case "status":
		return runAIStatusBuiltin(ctx, hc.Stdout)
	case "logout":
		providerArg := ""
		if len(args) > 1 {
			providerArg = strings.ToLower(strings.TrimSpace(args[1]))
		}
		return runAILogoutBuiltin(ctx, term, providerArg)
	case "login":
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
	case "setup":
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
		fmt.Fprintf(hc.Stderr, "ai: unknown subcommand %q (try 'ai --help')\n", sub)
		return interp.ExitStatus(2)
	}
}

func init() {
	_ = os.Stdin
}
