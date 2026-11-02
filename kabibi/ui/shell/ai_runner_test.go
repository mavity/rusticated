package shell

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"ai"
)

func TestLazyAIRunnerRejectsConcurrentTurn(t *testing.T) {
	runner := NewLazyAIRunner(nil)
	_, err := runner.beginOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.endOperation()

	if err := runner.RunTurn(context.Background(), "nested", nil, nil); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("RunTurn() error = %v, want %v", err, ErrAIRunnerBusy)
	}
	if err := runner.SetVerbose(true); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("SetVerbose() error = %v, want %v", err, ErrAIRunnerBusy)
	}
}

func TestClampModelContextTokensIgnoresDynamicDefault(t *testing.T) {
	for _, test := range []struct {
		name string
		info *ai.ModelInfo
		want int
	}{
		{name: "unknown model info", want: 16384},
		{name: "dynamic default is not a maximum", info: &ai.ModelInfo{MaxContextTokens: 4096, IsDynamicContext: true}, want: 16384},
		{name: "fixed context is a maximum", info: &ai.ModelInfo{MaxContextTokens: 4096}, want: 4096},
		{name: "fixed context above request", info: &ai.ModelInfo{MaxContextTokens: 32768}, want: 16384},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := clampModelContextTokens(16384, test.info); got != test.want {
				t.Fatalf("clampModelContextTokens() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestLazyAIRunnerCloseCancelsActiveOperationWithoutWaiting(t *testing.T) {
	runner := NewLazyAIRunner(nil)
	ctx, err := runner.beginOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	closeReturned := make(chan struct{})
	go func() {
		runner.Close()
		close(closeReturned)
	}()
	select {
	case <-closeReturned:
	case <-time.After(time.Second):
		t.Fatal("Close() waited for the active operation")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel the active operation")
	}

	runner.endOperation()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.WaitClosed(waitCtx); err != nil {
		t.Fatalf("WaitClosed() error = %v", err)
	}
}

func TestEphemeralLineWriterRewritesAndWipesStatus(t *testing.T) {
	var buf bytes.Buffer
	ephemeral := NewEphemeralLineWriter(&buf)

	if err := ephemeral.Feedback("📝 bridge.txt ... "); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Feedback("📝 bridge.txt ... ok"); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Feedback("📖 bridge.txt ... 20 bytes read"); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Wipe(); err != nil {
		t.Fatal(err)
	}

	want := "📝 bridge.txt ..." +
		"\r📝 bridge.txt ... ok" +
		"\r📖 bridge.txt ... 20 bytes read" +
		"\r                               \r"
	if got := buf.String(); got != want {
		t.Fatalf("EphemeralLineWriter output = %q, want %q", got, want)
	}
}

