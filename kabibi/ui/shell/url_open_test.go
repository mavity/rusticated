package shell

import (
	"bytes"
	"context"
	"testing"

	"ai"
	"github.com/charmbracelet/x/input"
)

func TestExtractLastOpenableURL(t *testing.T) {
	got := extractLastOpenableURL("\x1b[34mRead https://example.com/first. Then visit www.example.org/docs!\x1b[0m")
	if want := "https://www.example.org/docs"; got != want {
		t.Fatalf("extractLastOpenableURL() = %q, want %q", got, want)
	}
}

func TestExtractLastOpenableURLFromOSC8Link(t *testing.T) {
	want := "https://openrouter.ai/auth?callback_url=http%3A%2F%2F127.0.0.1%3A3000%2Foauth%2Fcallback&code_challenge=challenge"
	got := extractLastOpenableURL("Open this URL:\n" + ai.FormatClickableURL(want))
	if got != want {
		t.Fatalf("extractLastOpenableURL() = %q, want %q", got, want)
	}
}

func TestEmptyEnterOpensLatestOutputURL(t *testing.T) {
	want := "https://openrouter.ai/auth?callback_url=http%3A%2F%2F127.0.0.1%3A3000%2Foauth%2Fcallback&code_challenge=challenge"
	var opened string
	sh := New(ShellOptions{
		AIRunner: &fakeAIRunner{},
		OpenURL: func(target string) error {
			opened = target
			return nil
		},
	})
	defer sh.Close()

	sh.mu.Lock()
	sh.blocks = append(sh.blocks, CommandBlock{
		Kind:   BlockAITurn,
		Output: *bytes.NewBufferString("Open " + ai.FormatClickableURL(want)),
	})
	sh.mu.Unlock()
	before := len(sh.Blocks())

	sh.input.Focus()
	if !sh.handleKeyEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not handle the URL-opening action")
	}
	if opened != want {
		t.Fatalf("opened URL = %q, want %q", opened, want)
	}
	if got := len(sh.Blocks()); got != before {
		t.Fatalf("empty Enter submitted a command; block count = %d, want unchanged at %d", got, before)
	}
}

func TestTypedEnterRemainsPromptSubmission(t *testing.T) {
	runner := &fakeAIRunner{fn: func(_ context.Context, prompt string, _ ai.LogFeedback, _ ai.LogFeedback) error {
		if prompt != "please respond" {
			t.Errorf("AI prompt = %q, want %q", prompt, "please respond")
		}
		return nil
	}}
	opened := false
	sh := New(ShellOptions{
		AIRunner: runner,
		OpenURL: func(string) error {
			opened = true
			return nil
		},
	})
	defer sh.Close()
	sh.SetInput("please respond")
	if !sh.handleKeyEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not submit the typed response")
	}
	sh.WaitActive()
	if opened {
		t.Fatal("typed Enter unexpectedly opened the previous output URL")
	}
	if len(runner.prompts) != 1 || runner.prompts[0] != "please respond" {
		t.Fatalf("submitted prompts = %#v, want [please respond]", runner.prompts)
	}
}
