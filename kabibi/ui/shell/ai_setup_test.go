package shell

import (
	"context"
	"errors"
	"testing"

	"github.com/mavity/rusticated/kabibi/config"
)

type scriptedOnboardingPrompter struct {
	choices []int
}

func (p *scriptedOnboardingPrompter) Choose(context.Context, string, string, []string, int) (int, error) {
	if len(p.choices) == 0 {
		return -1, errors.New("unexpected choice prompt")
	}
	choice := p.choices[0]
	p.choices = p.choices[1:]
	return choice, nil
}

func (*scriptedOnboardingPrompter) Confirm(context.Context, string, string) (bool, error) {
	return false, errors.New("unexpected confirmation prompt")
}

func (*scriptedOnboardingPrompter) Input(context.Context, string, string, bool) (string, error) {
	return "", errors.New("unexpected input prompt")
}

func TestConfigureAICancelDoesNotPersistConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	runner := NewLazyAIRunner(nil)
	runner.SetOnboardingPrompter(&scriptedOnboardingPrompter{choices: []int{2}})

	_, err := runner.configureAI(context.Background())
	if !errors.Is(err, errOnboardingCancelled) {
		t.Fatalf("configureAI() error = %v, want onboarding cancellation", err)
	}
	cfg, err := config.LoadAI()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Configured || cfg.ConsentGiven {
		t.Fatalf("cancelled setup persisted configuration: %+v", cfg)
	}
}

func TestFormatAIBytes(t *testing.T) {
	if got := formatAIBytes(1 << 30); got != "1.00 GiB" {
		t.Fatalf("formatAIBytes(1 GiB) = %q", got)
	}
	if got := formatAIBytes(0); got != "0 B" {
		t.Fatalf("formatAIBytes(0) = %q", got)
	}
}
