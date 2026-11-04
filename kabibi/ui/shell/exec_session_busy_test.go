package shell

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExecutionSessionRejectsReentryWhileRunning(t *testing.T) {
	es, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	if _, err := es.RunCommand(context.Background(), "busyprobe() { :; }", nil, SourceUserShell, nil); err != nil {
		t.Fatalf("define function: %v", err)
	}

	if err := es.beginRun(execBusyDesc(3, "npm test")); err != nil {
		t.Fatalf("beginRun on idle session: %v", err)
	}

	if err := es.beginRun("second"); !errors.Is(err, ErrShellBusy) || !strings.Contains(err.Error(), "term_kill with id=3") {
		t.Fatalf("second beginRun: got %v, want ErrShellBusy naming job 3", err)
	}
	if _, err := es.RunCommand(context.Background(), "echo hi", nil, SourceUserShell, nil); !errors.Is(err, ErrShellBusy) {
		t.Fatalf("RunCommand while running: got %v, want ErrShellBusy", err)
	}
	if _, _, _, _, err := es.ExecuteAIToolCommand(context.Background(), "echo hi", 1, false); !errors.Is(err, ErrShellBusy) {
		t.Fatalf("ExecuteAIToolCommand while running: got %v, want ErrShellBusy", err)
	}
	es.bgMu.Lock()
	jobs := len(es.bgProcs)
	es.bgMu.Unlock()
	if jobs != 0 {
		t.Fatalf("rejected exec left %d job(s) registered", jobs)
	}
	if err := es.SetDir(t.TempDir()); !errors.Is(err, ErrShellBusy) {
		t.Fatalf("SetDir while running: got %v, want ErrShellBusy", err)
	}
	if !es.CanResolveCommand("busyprobe") || !es.canResolveCommandFromCache("busyprobe") {
		t.Fatal("shell function not resolved from the admission snapshot while running")
	}

	es.endRun()

	rec, err := es.RunCommand(context.Background(), "echo idle-again", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("RunCommand after endRun: %v", err)
	}
	if !strings.Contains(rec.Output, "idle-again") {
		t.Fatalf("unexpected output %q", rec.Output)
	}
}
