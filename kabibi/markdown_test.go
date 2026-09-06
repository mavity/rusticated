package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

type mockMarkdownSession struct {
	tokens []string
}

func (m mockMarkdownSession) SendMessage(ctx context.Context, userInput string, onToken func(string)) error {
	for _, tok := range m.tokens {
		onToken(tok)
	}
	return nil
}

func (m mockMarkdownSession) Close() error { return nil }

func TestRenderChatMarkdownProducesANSI(t *testing.T) {
	rendered := renderChatMarkdown("# Title\n\n- one\n- two\n\n**bold**", 40)
	if rendered == "" {
		t.Fatal("expected rendered markdown output")
	}
	if !strings.Contains(rendered, "Title") {
		t.Fatalf("rendered output missing content: %q", rendered)
	}
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected ANSI escape sequences in rendered markdown: %q", rendered)
	}
}

func TestRunPromptWithSessionDoesNotRenderMarkdown(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	err = runPromptWithSession(context.Background(), mockMarkdownSession{tokens: []string{"# Title\n\n- one\n"}}, "ignored")
	_ = w.Close()
	if err != nil {
		t.Fatalf("runPromptWithSession: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	got := string(out)
	if got != "# Title\n\n- one\n" {
		t.Fatalf("one-shot prompt output was altered: %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("one-shot prompt output should not contain ANSI markdown rendering: %q", got)
	}
}
