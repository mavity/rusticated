package shell

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLazyAIRunnerRejectsConcurrentTurn(t *testing.T) {
	runner := NewLazyAIRunner(nil)
	_, err := runner.beginOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.endOperation()

	if err := runner.RunTurn(context.Background(), "nested", nil, nil, nil); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("RunTurn() error = %v, want %v", err, ErrAIRunnerBusy)
	}
	if err := runner.SetVerbose(true); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("SetVerbose() error = %v, want %v", err, ErrAIRunnerBusy)
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
