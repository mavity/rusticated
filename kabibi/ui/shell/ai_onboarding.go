package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/wizard"
)

var errOnboardingCancelled = errors.New("AI onboarding cancelled")

type wizardResult struct {
	answer wizard.Answer
	err    error
}

type OnboardingPrompter interface {
	Choose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error)
	Confirm(ctx context.Context, title, message string) (bool, error)
	Input(ctx context.Context, title, message string, secret bool) (string, error)
}

type TUIOnboardingPrompter struct {
	shell *Shell
}

func NewTUIOnboardingPrompter(shell *Shell) *TUIOnboardingPrompter {
	return &TUIOnboardingPrompter{shell: shell}
}

func (p *TUIOnboardingPrompter) Choose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	answer, err := p.prompt(ctx, wizard.Prompt{
		Title: title, Message: message, Kind: wizard.Choose, Options: append([]string(nil), options...), Default: defaultChoice,
	})
	return answer.Choice, err
}

func (p *TUIOnboardingPrompter) Confirm(ctx context.Context, title, message string) (bool, error) {
	answer, err := p.prompt(ctx, wizard.Prompt{Title: title, Message: message, Kind: wizard.Confirm})
	return answer.Accepted, err
}

func (p *TUIOnboardingPrompter) Input(ctx context.Context, title, message string, secret bool) (string, error) {
	kind := wizard.Text
	if secret {
		kind = wizard.Secret
	}
	answer, err := p.prompt(ctx, wizard.Prompt{Title: title, Message: message, Kind: kind})
	return answer.Text, err
}

func (p *TUIOnboardingPrompter) prompt(ctx context.Context, prompt wizard.Prompt) (wizard.Answer, error) {
	if p == nil || p.shell == nil {
		return wizard.Answer{}, fmt.Errorf("interactive onboarding requires a shell widget")
	}
	return p.shell.promptWizard(ctx, prompt)
}

func (s *Shell) promptWizard(ctx context.Context, prompt wizard.Prompt) (wizard.Answer, error) {
	result := make(chan wizardResult, 1)
	var once sync.Once
	widget := wizard.New(prompt, func(answer wizard.Answer) {
		once.Do(func() { result <- wizardResult{answer: answer} })
	}, func() {
		once.Do(func() { result <- wizardResult{err: errOnboardingCancelled} })
	})
	s.mu.Lock()
	if s.wizardWidget != nil {
		s.mu.Unlock()
		return wizard.Answer{}, fmt.Errorf("another onboarding prompt is already active")
	}
	s.wizardWidget = widget
	s.mu.Unlock()
	s.invalidate()

	var response wizardResult
	select {
	case response = <-result:
	case <-ctx.Done():
		response.err = ctx.Err()
	}
	s.mu.Lock()
	if s.wizardWidget == widget {
		s.wizardWidget = nil
	}
	s.mu.Unlock()
	s.invalidate()
	return response.answer, response.err
}

func (s *Shell) handleWizardEvent(event input.Event) bool {
	s.mu.Lock()
	widget := s.wizardWidget
	s.mu.Unlock()
	if widget == nil {
		return false
	}
	consumed := widget.HandleEvent(event)
	if consumed {
		s.invalidate()
	}
	return consumed
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

func (p *StdioOnboardingPrompter) Choose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if _, err := fmt.Fprintf(p.writer, "\n%s\n%s\n", title, message); err != nil {
		return -1, fmt.Errorf("write AI onboarding prompt: %w", err)
	}
	for i, option := range options {
		if _, err := fmt.Fprintf(p.writer, "  [%d] %s\n", i+1, option); err != nil {
			return -1, fmt.Errorf("write AI onboarding option: %w", err)
		}
	}
	var promptErr error
	if defaultChoice >= 0 && defaultChoice < len(options) {
		_, promptErr = fmt.Fprintf(p.writer, "Choice [1-%d, default %d]: ", len(options), defaultChoice+1)
	} else {
		_, promptErr = fmt.Fprintf(p.writer, "Choice [1-%d]: ", len(options))
	}
	if promptErr != nil {
		return -1, fmt.Errorf("write AI choice prompt: %w", promptErr)
	}
	line, err := p.readLine()
	if err != nil {
		return -1, err
	}
	if line == "" && defaultChoice >= 0 && defaultChoice < len(options) {
		return defaultChoice, nil
	}
	choice, err := strconv.Atoi(line)
	if err != nil || choice < 1 || choice > len(options) {
		return -1, fmt.Errorf("invalid choice %q; enter a number from 1 to %d", line, len(options))
	}
	return choice - 1, nil
}

func (p *StdioOnboardingPrompter) Confirm(ctx context.Context, title, message string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(p.writer, "\n%s\n%s [y/N]: ", title, message); err != nil {
		return false, fmt.Errorf("write AI confirmation prompt: %w", err)
	}
	line, err := p.readLine()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "", "n", "no":
		return false, nil
	case "y", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("invalid confirmation %q; enter y or n", line)
	}
}

func (p *StdioOnboardingPrompter) Input(ctx context.Context, title, message string, _ bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintf(p.writer, "\n%s\n%s: ", title, message); err != nil {
		return "", fmt.Errorf("write AI input prompt: %w", err)
	}
	return p.readLine()
}

func (p *StdioOnboardingPrompter) readLine() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", fmt.Errorf("AI setup needs an interactive response from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}
