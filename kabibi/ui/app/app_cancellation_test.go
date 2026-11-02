package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

type cancelInputAIRunner struct {
	started   chan struct{}
	cancelled chan struct{}
	result    chan error
}

func (r *cancelInputAIRunner) RunTurn(ctx context.Context, _ string, _, _, _ ai.LogFeedback) error {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	err := ctx.Err()
	r.result <- err
	return err
}

func (*cancelInputAIRunner) Close() {}

func TestRunningShellReceivesCtrlCWhilePanelsAreVisible(t *testing.T) {
	runner := &cancelInputAIRunner{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
		result:    make(chan error, 1),
	}
	widget := &AppWidget{
		sh:         shell.New(shell.ShellOptions{AIRunner: runner}),
		panels:     filemanager.NewDualPaneWidget(),
		compositor: terminal.NewTerminal(1, 1),
	}
	defer widget.Close()
	widget.SetPanelsVisible(true)

	widget.sh.SetInput("Tell me something interesting.")
	if !widget.sh.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("shell did not start the AI operation")
	}
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("AI operation did not start")
	}

	if !widget.PanelsVisible() {
		t.Fatal("blue panels should remain visible during the active operation")
	}
	if !widget.HandleEvent(input.KeyPressEvent{Code: 'c', Mod: input.ModCtrl}) {
		t.Fatal("AppWidget did not route Ctrl+C to the running shell")
	}
	select {
	case <-runner.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("running shell operation did not receive cancellation")
	}
	if err := <-runner.result; !errors.Is(err, context.Canceled) {
		t.Fatalf("operation error = %v, want context.Canceled", err)
	}
	widget.sh.WaitActive()
	if widget.sh.IsRunning() {
		t.Fatal("shell remained active after Ctrl+C")
	}
	if !widget.PanelsVisible() {
		t.Fatal("cancellation unexpectedly hid the blue panels")
	}
}
