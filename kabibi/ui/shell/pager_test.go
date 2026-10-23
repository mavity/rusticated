package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/u-root/u-root/pkg/core"
)

type pagerInputWriter struct {
	mu       sync.Mutex
	output   bytes.Buffer
	term     *CommandTerminal
	keys     []byte
	prompts  int
	onPrompt func(string)
}

func (w *pagerInputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.output.Write(p)
	if err != nil {
		return n, err
	}
	if bytes.Contains(p, []byte(pagerPrompt)) {
		if w.onPrompt != nil {
			w.onPrompt(w.output.String())
		}
		if w.prompts >= len(w.keys) {
			return n, nil
		}
		key := w.keys[w.prompts]
		w.prompts++
		if err := w.term.WriteInput([]byte{key}); err != nil {
			return n, err
		}
	}
	return n, nil
}

func (w *pagerInputWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func TestRunPagerSupportsPageBackAndQuit(t *testing.T) {
	term := newCommandTerminal(context.Background(), 20, 3)
	output := &pagerInputWriter{
		term: term,
		keys: []byte{' ', 'b', ' ', 'q'},
	}
	err := runPager(context.Background(), term, strings.NewReader("1\n2\n3\n4\n5\n6\n"), output)
	if err != nil {
		t.Fatalf("runPager() error = %v", err)
	}
	got := strings.ReplaceAll(output.String(), pagerClearScreen, "")
	got = strings.ReplaceAll(got, "\r\x1b[2K"+pagerPrompt, "")
	got = strings.ReplaceAll(got, "\r\x1b[2K", "")
	if want := "1\n2\n3\n4\n1\n2\n3\n4\n"; got != want {
		t.Fatalf("pager output = %q, want %q", got, want)
	}
	if output.prompts != 4 {
		t.Fatalf("pager prompts = %d, want 4", output.prompts)
	}
	if len(term.stdin) != 0 {
		t.Fatalf("pager keys leaked into command stdin: %d chunks", len(term.stdin))
	}
}

func TestRunPagerDoesNotPromptForShortOutput(t *testing.T) {
	term := newCommandTerminal(context.Background(), 20, 4)
	var output bytes.Buffer
	if err := runPager(context.Background(), term, strings.NewReader("short\n"), &output); err != nil {
		t.Fatalf("runPager() error = %v", err)
	}
	if got := output.String(); got != pagerClearScreen+"short\n" {
		t.Fatalf("pager output = %q, want %q", got, pagerClearScreen+"short\n")
	}
}

func TestRunPagerLineAdvanceAfterBackwardPageUsesOneLine(t *testing.T) {
	term := newCommandTerminal(context.Background(), 20, 3)
	output := &pagerInputWriter{
		term: term,
		keys: []byte{' ', 'b', '\n', 'q'},
	}
	if err := runPager(context.Background(), term, strings.NewReader("1\n2\n3\n4\n5\n"), output); err != nil {
		t.Fatalf("runPager() error = %v", err)
	}
	got := strings.ReplaceAll(output.String(), pagerClearScreen, "")
	got = strings.ReplaceAll(got, "\r\x1b[2K"+pagerPrompt, "")
	got = strings.ReplaceAll(got, "\r\x1b[2K", "")
	if want := "1\n2\n3\n4\n1\n2\n3\n"; got != want {
		t.Fatalf("pager output = %q, want %q", got, want)
	}
}

func TestRunPagerStopsWhenCallerContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term := newCommandTerminal(context.Background(), 20, 3)
	output := &pagerInputWriter{term: term}
	output.onPrompt = func(string) { cancel() }
	err := runPager(ctx, term, strings.NewReader("1\n2\n3\n"), output)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runPager() error = %v, want context cancellation", err)
	}
}

func TestRunMorePagesPipeWithControllingInput(t *testing.T) {
	session, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	term := newCommandTerminal(context.Background(), 20, 3)
	ctx := withCommandTerminal(context.Background(), session, SourceUserShell, term)
	output := &pagerInputWriter{term: term, keys: []byte{'q'}}
	code := runMore(ctx, core.IO{
		Stdin:            strings.NewReader("one\ntwo\nthree\n"),
		Stdout:           output,
		Stderr:           io.Discard,
		StdoutIsTerminal: true,
	})
	if code != 0 {
		t.Fatalf("runMore() exit status = %d", code)
	}
	got := strings.ReplaceAll(output.String(), pagerClearScreen, "")
	got = strings.ReplaceAll(got, "\r\x1b[2K"+pagerPrompt, "")
	got = strings.ReplaceAll(got, "\r\x1b[2K", "")
	if got != "one\ntwo\n" {
		t.Fatalf("runMore() output = %q, want first page only", got)
	}
	if len(term.stdin) != 0 {
		t.Fatalf("more control key leaked to command stdin: %d chunks", len(term.stdin))
	}
}
