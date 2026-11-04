package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/mavity/rusticated/kabibi/gitcore"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func TestManagedEjectHelperProcess(t *testing.T) {
	if os.Getenv("KABIBI_EJECT_HELPER") != "1" {
		return
	}
	readyPath := os.Getenv("KABIBI_EJECT_READY")
	releasePath := os.Getenv("KABIBI_EJECT_RELEASE")
	if readyPath == "" || releasePath == "" {
		fmt.Fprintln(os.Stderr, "helper requires ready and release paths")
		os.Exit(2)
	}
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	delayOutput := os.Getenv("KABIBI_EJECT_DELAY_OUTPUT") == "1"
	if !delayOutput {
		fmt.Println("helper-ready")
	}
	for {
		if _, err := os.Stat(releasePath); err == nil {
			if delayOutput {
				fmt.Println("helper-ready")
			}
			return
		} else if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunnerPausedInTracksInterpreterAncestry(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		expected  []bool
		unordered bool
	}{
		{name: "root", command: "mark", expected: []bool{true}},
		{name: "subshell", command: "( mark )", expected: []bool{true}},
		{name: "command substitution", command: "echo $(mark)", expected: []bool{true}},
		{name: "function", command: "f() { mark; }; f", expected: []bool{true}},
		{name: "pipeline", command: "mark | mark", expected: []bool{false, true}, unordered: true},
		{name: "background", command: "mark &", expected: []bool{false}},
		{name: "subshell inside background", command: "{ ( mark ); } &", expected: []bool{false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := make(chan bool, len(tt.expected)+1)
			var root *interp.Runner
			handler := func(ctx context.Context, args []string) error {
				if len(args) > 0 && args[0] == "mark" {
					hits <- interp.HandlerCtx(ctx).PausedIn(root)
				}
				return nil
			}
			var err error
			root, err = interp.New(
				interp.Dir(t.TempDir()),
				interp.Env(expand.ListEnviron(os.Environ()...)),
				interp.ExecHandler(handler),
			)
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader(tt.command), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := root.Run(context.Background(), file); err != nil {
				t.Fatalf("run command: %v", err)
			}
			gotValues := make([]bool, 0, len(tt.expected))
			for i := range tt.expected {
				select {
				case got := <-hits:
					gotValues = append(gotValues, got)
				case <-time.After(5 * time.Second):
					t.Fatalf("timed out waiting for handler call %d", i)
				}
			}
			if tt.unordered {
				trueCount := 0
				for _, got := range gotValues {
					if got {
						trueCount++
					}
				}
				if trueCount != 1 {
					t.Errorf("pipeline PausedIn(root) results = %v, want one synchronous and one concurrent stage", gotValues)
				}
			} else {
				for i, want := range tt.expected {
					if got := gotValues[i]; got != want {
						t.Errorf("handler call %d PausedIn(root) = %t, want %t", i, got, want)
					}
				}
			}
		})
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "wasip1" {
		t.Run("process substitution", func(t *testing.T) {
			hits := make(chan bool, 1)
			var root *interp.Runner
			root, err := interp.New(
				interp.Dir(t.TempDir()),
				interp.Env(expand.ListEnviron(os.Environ()...)),
				interp.ExecHandler(func(ctx context.Context, args []string) error {
					if len(args) > 0 && args[0] == "mark" {
						hits <- interp.HandlerCtx(ctx).PausedIn(root)
					}
					return nil
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader("cat <(mark)"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := root.Run(context.Background(), file); err != nil {
				t.Fatalf("run process substitution: %v", err)
			}
			select {
			case got := <-hits:
				if got {
					t.Fatal("process-substitution handler was incorrectly considered paused in the root")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for process-substitution handler")
			}
		})
	}
}

func TestRunnerDetachRestoresTemporaryState(t *testing.T) {
	t.Run("prefix assignment", func(t *testing.T) {
		runner, output, detached := newDetachTestRunner(t)
		runDetachTestScript(t, runner, "X=old")
		runDetachTestScript(t, runner, "X=new eject-marker")
		if detached() == nil {
			t.Fatal("marker handler did not detach the runner")
		}
		runDetachTestScript(t, detached(), `echo "${X-unset}"`)
		if got := strings.TrimSpace(output.String()); got != "old" {
			t.Fatalf("detached X = %q, want old", got)
		}
	})

	t.Run("function scope and parameters", func(t *testing.T) {
		runner, output, detached := newDetachTestRunner(t)
		runDetachTestScript(t, runner, "set -- top")
		runDetachTestScript(t, runner, "keep() { echo function-ok; }")
		runDetachTestScript(t, runner, "f() { local L=local; G=updated; eject-marker; }")
		runDetachTestScript(t, runner, "f a b")
		if detached() == nil {
			t.Fatal("marker handler did not detach the runner")
		}
		runDetachTestScript(t, detached(), `echo "$#|$1|${L-unset}|$G"`)
		if got := strings.TrimSpace(output.String()); got != "1|top|unset|updated" {
			t.Fatalf("detached function state = %q, want %q", got, "1|top|unset|updated")
		}
		runDetachTestScript(t, detached(), "keep")
		if !strings.Contains(output.String(), "function-ok") {
			t.Fatalf("detached function definition was not preserved: %q", output.String())
		}
	})

	t.Run("last exit status", func(t *testing.T) {
		runner, output, detached := newDetachTestRunner(t)
		runDetachTestScript(t, runner, "false")
		runDetachTestScript(t, runner, "eject-marker")
		if detached() == nil {
			t.Fatal("marker handler did not detach the runner")
		}
		runDetachTestScript(t, detached(), "echo $?")
		if got := strings.TrimSpace(output.String()); got != "1" {
			t.Fatalf("detached last exit status = %q, want 1", got)
		}
	})

	t.Run("traps, aliases, directory stack, and options", func(t *testing.T) {
		dir := t.TempDir()
		runner, output, detached := newDetachTestRunner(t)
		originalDir := runner.Dir
		runDetachTestScript(t, runner, "set -o expand_aliases")
		runDetachTestScript(t, runner, "set -o pipefail")
		runDetachTestScript(t, runner, "alias ll='echo alias-ok'")
		runDetachTestScript(t, runner, "pushd "+strconv.Quote(dir))
		runDetachTestScript(t, runner, "trap 'echo trap-ok' EXIT")
		runDetachTestScript(t, runner, "eject-marker")
		if detached() == nil {
			t.Fatal("marker handler did not detach the runner")
		}
		runDetachTestScript(t, detached(), "ll")
		if !strings.Contains(output.String(), "alias-ok") {
			t.Fatalf("detached alias did not expand: %q", output.String())
		}
		if got := filepath.Clean(detached().Dir); got != filepath.Clean(dir) {
			t.Fatalf("detached directory = %q, want %q", got, dir)
		}
		runDetachTestScript(t, detached(), "popd")
		if got := filepath.Clean(detached().Dir); got != filepath.Clean(originalDir) {
			t.Fatalf("detached directory stack returned to %q, want %q", got, originalDir)
		}
		if err := detached().Run(context.Background(), mustParseDetachStmt(t, "false | true")); !errors.Is(err, interp.ExitStatus(1)) {
			t.Fatalf("detached pipefail status = %v, want exit status 1", err)
		}
		runDetachTestScript(t, detached(), "exit 0")
		if !strings.Contains(output.String(), "trap-ok") {
			t.Fatalf("detached exit trap did not run: %q", output.String())
		}
	})
}

func newDetachTestRunner(t *testing.T) (*interp.Runner, *bytes.Buffer, func() *interp.Runner) {
	t.Helper()
	output := &bytes.Buffer{}
	var root, detached *interp.Runner
	var err error
	root, err = interp.New(
		interp.Dir(t.TempDir()),
		interp.Env(expand.ListEnviron(os.Environ()...)),
		interp.Interactive(true),
		interp.StdIO(strings.NewReader(""), output, output),
		interp.ExecHandler(func(_ context.Context, args []string) error {
			if len(args) > 0 && args[0] == "eject-marker" {
				detached = root.Detach()
				return nil
			}
			return fmt.Errorf("unexpected external command %q", args)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return root, output, func() *interp.Runner { return detached }
}

func runDetachTestScript(t *testing.T, runner *interp.Runner, script string) {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatalf("parse %q: %v", script, err)
	}
	for _, stmt := range file.Stmts {
		if err := runner.Run(context.Background(), stmt); err != nil {
			if _, isStatus := interp.IsExitStatus(err); !isStatus {
				t.Fatalf("run %q: %v", script, err)
			}
		}
	}
}

func mustParseDetachStmt(t *testing.T, script string) *syntax.Stmt {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Stmts) != 1 {
		t.Fatalf("expected one statement in %q, got %d", script, len(file.Stmts))
	}
	return file.Stmts[0]
}

type ejectTestSink struct {
	started  chan struct{}
	chunks   chan string
	finished chan struct{}
}

func (s *ejectTestSink) OnAICommandStart(string, string) (func([]byte), func(int, time.Duration)) {
	close(s.started)
	return func(p []byte) {
			s.chunks <- string(p)
		}, func(int, time.Duration) {
			close(s.finished)
		}
}

func TestExecuteAIToolCommandEjectsIntoIndependentShell(t *testing.T) {
	skipNativeProcessEjectTest(t)
	root := t.TempDir()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	for _, dir := range []string{dirA, dirB} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dirA, "foreground-only.txt"), []byte("foreground\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "background-only.txt"), []byte("background\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRepo, err := git.PlainInit(dirB, false)
	if err != nil {
		t.Fatalf("initialize background repository: %v", err)
	}
	coreRepo, err := gitcore.Open(dirB)
	if err != nil {
		t.Fatalf("open background repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := coreRepo.Stage([]string{"tracked.txt"}, false); err != nil {
		t.Fatalf("stage initial repository file: %v", err)
	}
	if _, err := coreRepo.Commit("initial", false, false, map[string]string{
		"GIT_AUTHOR_NAME": "Initial Author", "GIT_AUTHOR_EMAIL": "initial@example.test",
		"GIT_COMMITTER_NAME": "Initial Author", "GIT_COMMITTER_EMAIL": "initial@example.test",
	}); err != nil {
		t.Fatalf("create initial background commit: %v", err)
	}
	startShellMockCDP(t)
	readyPath := filepath.Join(root, "ready")
	releasePath := filepath.Join(root, "release")
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	es, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	if _, err := es.RunCommand(
		context.Background(),
		"export GIT_AUTHOR_NAME='Foreground Author'; export GIT_AUTHOR_EMAIL=foreground@example.test; "+
			"export GIT_COMMITTER_NAME='Foreground Author'; export GIT_COMMITTER_EMAIL=foreground@example.test",
		nil,
		SourceUserShell,
		nil,
	); err != nil {
		t.Fatalf("set foreground Git identity: %v", err)
	}
	sink := &ejectTestSink{
		started:  make(chan struct{}),
		chunks:   make(chan string, 16),
		finished: make(chan struct{}),
	}
	es.SetAICommandUISink(sink)

	command := strings.Join([]string{
		"cd " + strconv.Quote(dirA),
		"job() { X=tmp KABIBI_EJECT_HELPER=1 KABIBI_EJECT_READY=" + strconv.Quote(readyPath) +
			" KABIBI_EJECT_RELEASE=" + strconv.Quote(releasePath) +
			" " + strconv.Quote(exe) + " -test.run=^TestManagedEjectHelperProcess$; cd " + strconv.Quote(dirB) +
			"; export Z=1; export GIT_AUTHOR_NAME='Background Author'; export GIT_AUTHOR_EMAIL=background@example.test; " +
			"export GIT_COMMITTER_NAME='Background Author'; export GIT_COMMITTER_EMAIL=background@example.test; " +
			"echo job:$PWD:X=$X:Z=$Z; ai list_files --query '*' --max-results 20; " +
			"ai git_inspect --action status; ai git_mutate --action stage --paths background-only.txt; " +
			"ai git_mutate --action commit --message 'background ejection commit'; " +
			"ai browser_screenshot --nickname main --save-path background.png; }",
		"job",
	}, "; ")
	type execResult struct {
		formatted string
		id        int
		running   bool
		code      int
		err       error
	}
	resultCh := make(chan execResult, 1)
	go func() {
		formatted, id, running, code, err := es.ExecuteAIToolCommand(context.Background(), command, 2, false)
		resultCh <- execResult{formatted: formatted, id: id, running: running, code: code, err: err}
	}()

	select {
	case <-sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("AI command UI block was not started")
	}
	readySeen := false
	deadline := time.After(15 * time.Second)
	for !readySeen {
		select {
		case chunk := <-sink.chunks:
			readySeen = strings.Contains(chunk, "helper-ready")
		case result := <-resultCh:
			t.Fatalf("exec returned before helper became ready: %+v", result)
		case <-deadline:
			t.Fatal("helper did not reach its safe external-command wait")
		}
	}

	var result execResult
	select {
	case result = <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatal("exec did not return after its timeout")
	}
	if result.err != nil {
		t.Fatalf("ExecuteAIToolCommand: %v", result.err)
	}
	if !result.running || result.id == 0 {
		t.Fatalf("exec result = %+v, want a running managed job", result)
	}
	rec, err := es.RunCommand(context.Background(), `echo "$PWD|${X-unset}|${Z-unset}"`, nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("foreground shell did not accept a command after eject: %v", err)
	}
	wantForeground := filepath.Clean(dirA) + "|unset|unset"
	if got := strings.TrimSpace(rec.Output); got != wantForeground {
		t.Fatalf("foreground state = %q, want %q", got, wantForeground)
	}

	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, ok := es.LookupManagedPTY(result.id)
	if !ok {
		t.Fatalf("ejected job %d is not registered", result.id)
	}
	select {
	case <-job.doneCh:
	case <-time.After(10 * time.Second):
		_ = job.Kill("SIGKILL")
		t.Fatal("ejected job did not finish after release")
	}
	output, exited, code, _ := job.Snapshot(false, false)
	if !exited || code != 0 {
		t.Fatalf("ejected job status: exited=%t code=%d output=%q", exited, code, output)
	}
	if !strings.Contains(strings.ReplaceAll(output, `\`, "/"), "job:"+filepath.ToSlash(dirB)+":X=:Z=1") {
		t.Fatalf("background continuation did not retain its own state: %q", output)
	}
	normalizedOutput := strings.ReplaceAll(output, `\`, "/")
	if !strings.Contains(normalizedOutput, "background-only.txt") ||
		strings.Contains(normalizedOutput, "foreground-only.txt") {
		t.Fatalf("registered AI list_files result did not use only the ejected job cwd: %q", output)
	}
	if !strings.Contains(normalizedOutput, filepath.ToSlash(dirB)) {
		t.Fatalf("registered AI git_inspect result did not report the ejected repository root %q: %q", dirB, output)
	}
	if _, err := os.Stat(filepath.Join(dirB, "background.png")); err != nil {
		t.Fatalf("registered AI browser_screenshot did not save under the ejected job cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirA, "background.png")); !os.IsNotExist(err) {
		t.Fatalf("registered AI browser_screenshot unexpectedly wrote to the foreground cwd: err=%v", err)
	}
	head, err := gitRepo.Head()
	if err != nil {
		t.Fatalf("read background repository HEAD: %v", err)
	}
	commit, err := gitRepo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("read background repository commit: %v", err)
	}
	if commit.Author.Name != "Background Author" || commit.Author.Email != "background@example.test" ||
		commit.Message != "background ejection commit" {
		t.Fatalf("ejected Git commit identity/message = %#v / %q, want background identity", commit.Author, commit.Message)
	}
	rec, err = es.RunCommand(context.Background(), `echo "$PWD|${X-unset}|${Z-unset}"`, nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("foreground shell after background completion: %v", err)
	}
	if got := strings.TrimSpace(rec.Output); got != wantForeground {
		t.Fatalf("background job changed foreground state: got %q, want %q", got, wantForeground)
	}
	select {
	case <-sink.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("AI command UI block did not finish")
	}
}

func TestExecuteAIToolCommandDefersEjectFromConcurrentPipeline(t *testing.T) {
	skipNativeProcessEjectTest(t)
	root := t.TempDir()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	for _, dir := range []string{dirA, dirB} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	readyPath := filepath.Join(root, "pipeline-ready")
	releasePath := filepath.Join(root, "pipeline-release")
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	es, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	command := strconv.Quote(exe) + " -test.run=^TestManagedEjectHelperProcess$"
	command = "KABIBI_EJECT_HELPER=1 KABIBI_EJECT_DELAY_OUTPUT=1 KABIBI_EJECT_READY=" + strconv.Quote(readyPath) +
		" KABIBI_EJECT_RELEASE=" + strconv.Quote(releasePath) +
		" " + command + " | while read line; do cd " + strconv.Quote(dirB) + "; done; echo after-pipeline"
	command = "cd " + strconv.Quote(dirA) + "; " + command

	type execResult struct {
		id      int
		running bool
		err     error
	}
	resultCh := make(chan execResult, 1)
	go func() {
		_, id, running, _, err := es.ExecuteAIToolCommand(context.Background(), command, 1, false)
		resultCh <- execResult{id: id, running: running, err: err}
	}()
	deadline := time.After(15 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		select {
		case result := <-resultCh:
			t.Fatalf("pipeline command finished before helper became ready: %+v", result)
		case <-deadline:
			t.Fatal("pipeline helper did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var result execResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline timeout did not return")
	}
	if result.err != nil {
		t.Fatalf("ExecuteAIToolCommand: %v", result.err)
	}
	if !result.running || result.id == 0 {
		t.Fatalf("pipeline result = %+v, want a running job", result)
	}
	if _, err := es.RunCommand(context.Background(), "echo should-still-be-busy", nil, SourceUserShell, nil); !errors.Is(err, ErrShellBusy) {
		t.Fatalf("foreground shell admission during unsafe pipeline = %v, want ErrShellBusy", err)
	}

	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, ok := es.LookupManagedPTY(result.id)
	if !ok {
		t.Fatalf("pipeline job %d is not registered", result.id)
	}
	select {
	case <-job.doneCh:
	case <-time.After(10 * time.Second):
		_ = job.Kill("SIGKILL")
		t.Fatal("pipeline job did not finish after release")
	}
	rec, err := es.RunCommand(context.Background(), `echo "$PWD"`, nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("foreground shell remained busy after safe pipeline point: %v", err)
	}
	if got := strings.TrimSpace(rec.Output); got != filepath.Clean(dirA) {
		t.Fatalf("foreground cwd = %q, want %q", got, filepath.Clean(dirA))
	}
	output, exited, code, _ := job.Snapshot(false, false)
	if !exited || code != 0 || !strings.Contains(output, "after-pipeline") {
		t.Fatalf("pipeline job status: exited=%t code=%d output=%q", exited, code, output)
	}
}

func TestExecuteAIToolCommandEjectsFromSynchronousSubshell(t *testing.T) {
	skipNativeProcessEjectTest(t)
	root := t.TempDir()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	for _, dir := range []string{dirA, dirB} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	readyPath := filepath.Join(root, "subshell-ready")
	releasePath := filepath.Join(root, "subshell-release")
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	es, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	command := "cd " + strconv.Quote(dirA) + "; ( cd " + strconv.Quote(dirB) +
		"; KABIBI_EJECT_HELPER=1 KABIBI_EJECT_READY=" + strconv.Quote(readyPath) +
		" KABIBI_EJECT_RELEASE=" + strconv.Quote(releasePath) +
		" " + strconv.Quote(exe) + " -test.run=^TestManagedEjectHelperProcess$; echo job:$PWD )"
	resultCh := make(chan struct {
		id      int
		running bool
		err     error
	}, 1)
	go func() {
		_, id, running, _, err := es.ExecuteAIToolCommand(context.Background(), command, 1, false)
		resultCh <- struct {
			id      int
			running bool
			err     error
		}{id, running, err}
	}()
	deadline := time.After(15 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		select {
		case result := <-resultCh:
			t.Fatalf("subshell command ended before helper became ready: %+v", result)
		case <-deadline:
			t.Fatal("subshell helper did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var result struct {
		id      int
		running bool
		err     error
	}
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("subshell timeout did not return")
	}
	if result.err != nil {
		t.Fatalf("ExecuteAIToolCommand: %v", result.err)
	}
	if !result.running || result.id == 0 {
		t.Fatalf("subshell result = %+v, want a running job", result)
	}
	rec, err := es.RunCommand(context.Background(), `echo "$PWD"`, nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("foreground shell was not released from subshell eject: %v", err)
	}
	if got := strings.TrimSpace(rec.Output); got != filepath.Clean(dirA) {
		t.Fatalf("foreground cwd = %q, want %q", got, filepath.Clean(dirA))
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, ok := es.LookupManagedPTY(result.id)
	if !ok {
		t.Fatalf("subshell job %d is not registered", result.id)
	}
	select {
	case <-job.doneCh:
	case <-time.After(10 * time.Second):
		_ = job.Kill("SIGKILL")
		t.Fatal("subshell job did not finish after release")
	}
	output, exited, code, _ := job.Snapshot(false, false)
	if !exited || code != 0 || !strings.Contains(output, "job:"+filepath.Clean(dirB)) {
		t.Fatalf("subshell job status: exited=%t code=%d output=%q", exited, code, output)
	}
}

func TestExecuteAIToolCommandEjectsFromCommandSubstitution(t *testing.T) {
	skipNativeProcessEjectTest(t)
	root := t.TempDir()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	for _, dir := range []string{dirA, dirB} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	readyPath := filepath.Join(root, "subst-ready")
	releasePath := filepath.Join(root, "subst-release")
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	es, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	command := "cd " + strconv.Quote(dirA) + "; X=$(cd " + strconv.Quote(dirB) +
		"; KABIBI_EJECT_HELPER=1 KABIBI_EJECT_READY=" + strconv.Quote(readyPath) +
		" KABIBI_EJECT_RELEASE=" + strconv.Quote(releasePath) +
		" " + strconv.Quote(exe) + " -test.run=^TestManagedEjectHelperProcess$; echo inner:$PWD); echo job:$PWD:X=$X"
	resultCh := make(chan struct {
		id      int
		running bool
		err     error
	}, 1)
	go func() {
		_, id, running, _, err := es.ExecuteAIToolCommand(context.Background(), command, 1, false)
		resultCh <- struct {
			id      int
			running bool
			err     error
		}{id, running, err}
	}()
	deadline := time.After(15 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		select {
		case result := <-resultCh:
			t.Fatalf("command-substitution command ended before helper became ready: %+v", result)
		case <-deadline:
			t.Fatal("command-substitution helper did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var result struct {
		id      int
		running bool
		err     error
	}
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("command-substitution timeout did not return")
	}
	if result.err != nil {
		t.Fatalf("ExecuteAIToolCommand: %v", result.err)
	}
	if !result.running || result.id == 0 {
		t.Fatalf("command-substitution result = %+v, want a running job", result)
	}
	rec, err := es.RunCommand(context.Background(), `echo "$PWD|${X-unset}"`, nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("foreground shell was not released from command-substitution eject: %v", err)
	}
	if got, want := strings.TrimSpace(rec.Output), filepath.Clean(dirA)+"|unset"; got != want {
		t.Fatalf("foreground command-substitution state = %q, want %q", got, want)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, ok := es.LookupManagedPTY(result.id)
	if !ok {
		t.Fatalf("command-substitution job %d is not registered", result.id)
	}
	select {
	case <-job.doneCh:
	case <-time.After(10 * time.Second):
		_ = job.Kill("SIGKILL")
		t.Fatal("command-substitution job did not finish after release")
	}
	output, exited, code, _ := job.Snapshot(false, false)
	if !exited || code != 0 ||
		!strings.Contains(strings.ReplaceAll(output, `\`, "/"), "job:"+filepath.ToSlash(dirA)+":X=helper-ready") ||
		!strings.Contains(strings.ReplaceAll(output, `\`, "/"), "inner:"+filepath.ToSlash(dirB)) {
		t.Fatalf("command-substitution job status: exited=%t code=%d output=%q", exited, code, output)
	}
}

func TestManagedPTYKillCancelsInterpreterRun(t *testing.T) {
	es, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	formatted, id, stillRunning, _, err := es.ExecuteAIToolCommand(context.Background(), "while :; do :; done", 1, false)
	if err != nil {
		t.Fatalf("start loop: %v", err)
	}
	if !stillRunning || id == 0 {
		t.Fatalf("loop result %q id=%d stillRunning=%t; want managed background job", formatted, id, stillRunning)
	}
	job, ok := es.LookupManagedPTY(id)
	if !ok {
		t.Fatalf("managed job %d not found", id)
	}
	if err := job.Kill("SIGTERM"); err != nil {
		t.Fatalf("kill interpreted loop: %v", err)
	}
	select {
	case <-job.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("interpreter did not stop after cancellation")
	}
	if _, err := es.RunCommand(context.Background(), "echo shell-free", nil, SourceUserShell, nil); err != nil {
		t.Fatalf("shell remained busy after cancellation: %v", err)
	}
}

func TestDirectPTYAdmissionRemainsUntilEjectOrExit(t *testing.T) {
	skipNativeProcessEjectTest(t)
	root := t.TempDir()
	readyPath := filepath.Join(root, "direct-ready")
	releasePath := filepath.Join(root, "direct-release")
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	t.Setenv("KABIBI_EJECT_HELPER", "1")
	t.Setenv("KABIBI_EJECT_READY", readyPath)
	t.Setenv("KABIBI_EJECT_RELEASE", releasePath)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	es, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	command := strconv.Quote(exe) + " -test.run=^TestManagedEjectHelperProcess$"
	resultCh := make(chan struct {
		id      int
		running bool
		err     error
	}, 1)
	go func() {
		_, id, running, _, err := es.ExecuteAIToolCommand(context.Background(), command, 5, false)
		resultCh <- struct {
			id      int
			running bool
			err     error
		}{id, running, err}
	}()

	deadline := time.After(15 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		select {
		case result := <-resultCh:
			t.Fatalf("direct command ended before ready: %+v", result)
		case <-deadline:
			t.Fatal("direct command did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if _, err := es.RunCommand(context.Background(), "echo must-be-busy", nil, SourceUserShell, nil); !errors.Is(err, ErrShellBusy) {
		t.Fatalf("foreground command while direct PTY is active: got %v, want ErrShellBusy", err)
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !result.running || result.id == 0 {
		t.Fatalf("direct command was not promoted: %+v", result)
	}
	if _, err := es.RunCommand(context.Background(), "echo foreground-ready", nil, SourceUserShell, nil); err != nil {
		t.Fatalf("foreground admission was not released after direct PTY eject: %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	job, ok := es.LookupManagedPTY(result.id)
	if !ok {
		t.Fatalf("direct managed job %d not found", result.id)
	}
	select {
	case <-job.doneCh:
	case <-time.After(10 * time.Second):
		_ = job.Kill("SIGKILL")
		t.Fatal("direct PTY job did not finish")
	}
}

func TestTimeoutWaitsForFinalStatusAfterRunAdmissionEnds(t *testing.T) {
	es, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	runEnded := make(chan struct{})
	timeoutSelected := make(chan struct{})
	releaseFinish := make(chan struct{})
	defer func() {
		select {
		case <-releaseFinish:
		default:
			close(releaseFinish)
		}
	}()
	es.runHooks = &managedRunHooks{
		afterRunEnd: func(*ManagedPTY) {
			close(runEnded)
			<-releaseFinish
		},
		beforeTimeoutEject: func(*ManagedPTY) {
			close(timeoutSelected)
		},
	}

	type result struct {
		formatted string
		running   bool
		code      int
		err       error
	}
	resultCh := make(chan result, 1)
	go func() {
		formatted, _, running, code, err := es.ExecuteAIToolCommand(context.Background(), "echo completed", 1, false)
		resultCh <- result{formatted: formatted, running: running, code: code, err: err}
	}()

	select {
	case <-runEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("interpreter did not reach the post-admission completion gate")
	}
	select {
	case <-timeoutSelected:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout path did not start while completion was gated")
	}
	select {
	case got := <-resultCh:
		t.Fatalf("timeout returned before markExited completed: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFinish)
	got := <-resultCh
	if got.err != nil {
		t.Fatalf("ExecuteAIToolCommand: %v", got.err)
	}
	if got.running || got.code != 0 || !strings.Contains(got.formatted, "[exit: 0") {
		t.Fatalf("completion/timeout result = %+v, want completed exit status", got)
	}
}

func TestCompletionWinsAfterTimeoutSelectsBeforeEject(t *testing.T) {
	skipNativeProcessEjectTest(t)
	root := t.TempDir()
	readyPath := filepath.Join(root, "race-ready")
	releasePath := filepath.Join(root, "race-release")
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	t.Setenv("KABIBI_EJECT_HELPER", "1")
	t.Setenv("KABIBI_EJECT_READY", readyPath)
	t.Setenv("KABIBI_EJECT_RELEASE", releasePath)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	es, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()

	timeoutJob := make(chan *ManagedPTY, 1)
	resumeTimeout := make(chan struct{})
	defer func() {
		select {
		case <-resumeTimeout:
		default:
			close(resumeTimeout)
		}
	}()
	es.runHooks = &managedRunHooks{
		beforeTimeoutEject: func(job *ManagedPTY) {
			timeoutJob <- job
			<-resumeTimeout
		},
	}

	type result struct {
		formatted string
		running   bool
		code      int
		err       error
	}
	resultCh := make(chan result, 1)
	go func() {
		formatted, _, running, code, err := es.ExecuteAIToolCommand(
			context.Background(),
			strconv.Quote(exe)+" -test.run=^TestManagedEjectHelperProcess$",
			1,
			false,
		)
		resultCh <- result{formatted: formatted, running: running, code: code, err: err}
	}()

	readyDeadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		select {
		case <-readyDeadline:
			t.Fatal("direct command did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}

	var job *ManagedPTY
	select {
	case job = <-timeoutJob:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout path did not pause before ejecting")
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("interpreter did not finish while timeout was paused")
	}
	close(resumeTimeout)

	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("ExecuteAIToolCommand: %v", got.err)
		}
		if got.running || got.code != 0 || !strings.Contains(got.formatted, "helper-ready") {
			t.Fatalf("completion/timeout result = %+v, want completed command", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteAIToolCommand did not return after completion")
	}
}

func skipNativeProcessEjectTest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "wasip1" {
		t.Skip("requires native subprocesses and PTYs")
	}
}
