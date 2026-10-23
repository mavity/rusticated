package shell

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"mvdan.cc/sh/v3/interp"
)

func TestCommandTerminalInputRoutesToControlConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	term := newCommandTerminal(ctx, 80, 23)
	release := term.SubscribeControlInput()
	defer release()

	for i := 0; i < cap(term.keys); i++ {
		if err := term.WriteInput([]byte{byte(i)}); err != nil {
			t.Fatalf("WriteInput(%d) failed: %v", i, err)
		}
	}
	if err := term.WriteInput([]byte("overflow")); !errors.Is(err, ErrTerminalInputQueueFull) {
		t.Fatalf("overflow WriteInput error = %v, want %v", err, ErrTerminalInputQueueFull)
	}
	if len(term.stdin) != 0 {
		t.Fatalf("command stdin received %d control key chunks, want none", len(term.stdin))
	}

	keys := make([]byte, 1)
	if n, err := term.ReadControlInput(keys); err != nil || n != 1 || keys[0] != 0 {
		t.Fatalf("ReadControlInput() = %d, %v, %q; want first byte 0", n, err, keys)
	}
}

func TestCommandTerminalInputDefaultsToStdin(t *testing.T) {
	term := newCommandTerminal(context.Background(), 80, 23)
	for i := 0; i < cap(term.stdin)+1; i++ {
		err := term.WriteInput([]byte("x"))
		if i < cap(term.stdin) && err != nil {
			t.Fatalf("WriteInput(%d) failed: %v", i, err)
		}
		if i == cap(term.stdin) && !errors.Is(err, ErrTerminalInputQueueFull) {
			t.Fatalf("overflow WriteInput error = %v, want %v", err, ErrTerminalInputQueueFull)
		}
	}
	if len(term.keys) != 0 {
		t.Fatalf("control-input queue has %d chunks without a subscriber", len(term.keys))
	}
}

func TestCommandTerminalInputReadStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	term := newCommandTerminal(ctx, 80, 23)
	cancel()

	if _, err := term.ReadInput(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadInput() error = %v, want %v", err, context.Canceled)
	}
}

func TestStreamTTYReportsEffectiveStreamsIndependently(t *testing.T) {
	es, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	term := newCommandTerminal(context.Background(), 80, 23)
	ctx := withCommandTerminal(context.Background(), es, SourceUserShell, term)

	status := es.StreamTTY(ctx, interp.HandlerContext{
		Stdin:  es.ttyIn,
		Stdout: es.streamOut,
		Stderr: es.streamOut,
	})
	if status != (StreamTTYStatus{Stdin: true, Stdout: true, Stderr: true}) {
		t.Fatalf("unredirected stream status = %+v", status)
	}

	status = es.StreamTTY(ctx, interp.HandlerContext{
		Stdin:  es.ttyIn,
		Stdout: &bytes.Buffer{},
		Stderr: es.streamOut,
	})
	if status != (StreamTTYStatus{Stdin: true, Stderr: true}) {
		t.Fatalf("redirected stdout status = %+v", status)
	}

	status = es.StreamTTY(ctx, interp.HandlerContext{
		Stdin:  bytes.NewBuffer(nil),
		Stdout: es.streamOut,
		Stderr: es.streamOut,
	})
	if status != (StreamTTYStatus{Stdout: true, Stderr: true}) {
		t.Fatalf("piped stdin status = %+v", status)
	}
}

func TestSetViewportSizeResizesBackgroundPTYFrame(t *testing.T) {
	es, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proc := &ManagedPTY{
		notifyCh: make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	es.RegisterManagedPTY(proc)

	es.SetViewportSize(101, 50)
	if proc.Cols != 99 || proc.Rows != 20 {
		t.Fatalf("managed PTY frame = %dx%d, want 99x20", proc.Cols, proc.Rows)
	}

	proc.mu.Lock()
	proc.exited = true
	proc.mu.Unlock()
	es.SetViewportSize(120, 60)
	if proc.Cols != 99 || proc.Rows != 20 {
		t.Fatalf("exited managed PTY frame changed to %dx%d", proc.Cols, proc.Rows)
	}
}
