package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/mavity/rusticated/kabibi/internal/gitfs"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

func initShellTestRepository(t *testing.T, root string) *git.Repository {
	t.Helper()

	worktreeFS := gitfs.New(root)
	dotGitFS, err := worktreeFS.Chroot(git.GitDirName)
	if err != nil {
		t.Fatalf("create test repository storage filesystem: %v", err)
	}
	storer := filesystem.NewStorage(dotGitFS, cache.NewObjectLRUDefault())
	repo, err := git.Init(storer, worktreeFS)
	if err != nil {
		t.Fatalf("initialize test repository: %v", err)
	}
	return repo
}

func TestParseStreamedLogArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want streamedLogOptions
		ok   bool
	}{
		{
			name: "oneline with limit and path",
			args: []string{"-c", "core.pager=less", "log", "--oneline", "-n", "3", "HEAD~3", "--", "src/main.go"},
			want: streamedLogOptions{oneline: true, limit: 3, revision: "HEAD~3", filePath: "src/main.go"},
			ok:   true,
		},
		{
			name: "default format",
			args: []string{"log", "--max-count=2"},
			want: streamedLogOptions{limit: 2},
			ok:   true,
		},
		{
			name: "unsupported ordering falls back",
			args: []string{"log", "--topo-order"},
			ok:   false,
		},
		{
			name: "multiple paths fall back",
			args: []string{"log", "--", "a", "b"},
			ok:   false,
		},
		{
			name: "zero count falls back",
			args: []string{"log", "--max-count=0"},
			ok:   false,
		},
		{
			name: "symmetric difference falls back",
			args: []string{"log", "left...right"},
			ok:   false,
		},
		{
			name: "two-dot range falls back",
			args: []string{"log", "HEAD~3..HEAD"},
			ok:   false,
		},
		{
			name: "full-hash pretty format falls back",
			args: []string{"log", "--pretty=oneline"},
			ok:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseStreamedLogArgs(test.args)
			if ok != test.ok || got != test.want {
				t.Fatalf("parseStreamedLogArgs(%q) = (%+v, %v), want (%+v, %v)",
					test.args, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestIsPassThroughPager(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: "", want: true},
		{command: "cat", want: true},
		{command: "cat -", want: true},
		{command: "cat -n", want: false},
		{command: "less", want: false},
		{command: ":", want: true},
	}
	for _, test := range tests {
		if got := isPassThroughPager(test.command); got != test.want {
			t.Errorf("isPassThroughPager(%q) = %v, want %v", test.command, got, test.want)
		}
	}
}

func TestStreamedGitLogFallsBackForUnbornHead(t *testing.T) {
	root := t.TempDir()
	initShellTestRepository(t, root)
	handled, err := runStreamedGitLog(context.Background(), interp.HandlerContext{
		Dir:    root,
		Stdout: io.Discard,
		Stderr: io.Discard,
	}, gitInvocation{effectiveDir: root, args: []string{"log"}})
	if err != nil {
		t.Fatalf("runStreamedGitLog() error = %v", err)
	}
	if handled {
		t.Fatal("streamed Git log handled an unborn HEAD instead of falling back")
	}
}

func TestBufferedGitDiffPagerAndTTYBypass(t *testing.T) {
	root := t.TempDir()
	repo := initShellTestRepository(t, root)
	var err error
	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.User.Name = "Kabibi Test"
	cfg.User.Email = "author@example.test"
	if err := repo.Storer.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	fileContents := strings.Builder{}
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&fileContents, "line-%d\n", i)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "diff.txt"), []byte(fileContents.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(filepath.ToSlash(filepath.Join("nested", "diff.txt"))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		name := filepath.Join("nested", fmt.Sprintf("later-%02d.txt", i))
		if err := os.WriteFile(filepath.Join(root, name), []byte("original\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := worktree.Add(filepath.ToSlash(name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := worktree.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: time.Unix(1_700_000_000, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "diff.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		name := filepath.Join("nested", fmt.Sprintf("later-%02d.txt", i))
		if err := os.WriteFile(filepath.Join(root, name), []byte("changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatal(err)
	}
	term := newCommandTerminal(context.Background(), 80, 3)
	ctx := withCommandTerminal(context.Background(), session, SourceUserShell, term)
	output := &pagerInputWriter{term: term, keys: []byte{'q'}}
	session.streamOut.setTarget(func(p []byte) {
		if _, err := output.Write(p); err != nil {
			t.Errorf("capture paged diff: %v", err)
		}
	})
	invocation := gitInvocation{
		effectiveDir: root,
		config:       []string{"core.pager=less"},
		args:         []string{"-c", "core.pager=less", "diff"},
	}
	handled, err := runBufferedGitDiffWithPager(ctx, interp.HandlerContext{
		Dir: root, Stdout: session.streamOut, Stderr: io.Discard,
	}, invocation)
	session.streamOut.setTarget(nil)
	if err != nil || !handled {
		t.Fatalf("interactive git diff = (handled %v, err %v)", handled, err)
	}
	paged := strings.ReplaceAll(output.String(), pagerClearScreen, "")
	paged = strings.ReplaceAll(paged, "\r\x1b[2K"+pagerPrompt, "")
	paged = strings.ReplaceAll(paged, "\r\x1b[2K", "")
	if output.prompts != 1 || strings.Contains(paged, "-line-") {
		t.Fatalf("interactive diff output prompts=%d, included later diff lines=%v: %q",
			output.prompts, strings.Contains(paged, "-line-"), paged)
	}

	handled, err = runBufferedGitDiffWithPager(ctx, interp.HandlerContext{
		Dir: root, Stdout: &bytes.Buffer{}, Stderr: io.Discard,
	}, invocation)
	if err != nil || handled {
		t.Fatalf("redirected git diff = (handled %v, err %v), want normal unpaged path", handled, err)
	}
	invocation.noPager = true
	handled, err = runBufferedGitDiffWithPager(ctx, interp.HandlerContext{
		Dir: root, Stdout: session.streamOut, Stderr: io.Discard,
	}, invocation)
	if err != nil || handled {
		t.Fatalf("--no-pager git diff = (handled %v, err %v), want normal unpaged path", handled, err)
	}

	session.SetCommandTerminalAttached(true)
	if err := session.SetViewportSize(80, 3); err != nil {
		t.Fatal(err)
	}
	type commandResult struct {
		exitCode int
		err      error
	}
	done := make(chan commandResult, 1)
	inputErr := make(chan error, 1)
	go func() {
		quitSent := false
		record, err := session.RunCommand(context.Background(), "git diff", nil, SourceUserShell, func(chunk []byte) {
			if !quitSent && bytes.Contains(chunk, []byte(pagerPrompt)) {
				quitSent = true
				inputErr <- session.WriteCommandTerminalInputError([]byte{'q'})
			}
		})
		done <- commandResult{exitCode: record.ExitCode, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil || result.exitCode != 0 {
			t.Fatalf("interactive RunCommand git diff = (exit %d, err %v)", result.exitCode, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interactive RunCommand git diff did not return after pager quit")
	}
	select {
	case err := <-inputErr:
		if err != nil {
			t.Fatalf("send pager quit key: %v", err)
		}
	default:
		t.Fatal("interactive RunCommand git diff did not display a pager prompt")
	}
}

func TestStreamedGitLogPagesBeforeWalkingFullHistory(t *testing.T) {
	root := t.TempDir()
	repo := initShellTestRepository(t, root)
	var err error

	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.User.Name = "Kabibi Test"
	cfg.User.Email = "author@example.test"
	if err := repo.Storer.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		if err := os.WriteFile(filepath.Join(root, "history.txt"), []byte(fmt.Sprintf("%d\n", i)), 0o644); err != nil {
			t.Fatalf("write commit %d: %v", i, err)
		}
		if _, err := worktree.Add("history.txt"); err != nil {
			t.Fatalf("stage commit %d: %v", i, err)
		}
		when := time.Unix(int64(1_700_000_000+i), 0)
		if _, err := worktree.Commit(fmt.Sprintf("commit-%02d", i), &git.CommitOptions{
			Author:    &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: when},
			Committer: &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: when},
		}); err != nil {
			t.Fatalf("create commit %d: %v", i, err)
		}
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	sideBranch := plumbing.NewBranchReferenceName("side")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(sideBranch, head.Hash())); err != nil {
		t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: sideBranch}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "side.txt"), []byte("side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("side.txt"); err != nil {
		t.Fatal(err)
	}
	sideTime := time.Unix(1_700_000_031, 0)
	sideHash, err := worktree.Commit("side branch", &git.CommitOptions{
		Author:    &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: sideTime},
		Committer: &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: sideTime},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: plumbing.Master}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("main.txt"); err != nil {
		t.Fatal(err)
	}
	mainTime := sideTime
	mainHash, err := worktree.Commit("main branch", &git.CommitOptions{
		Author:    &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: mainTime},
		Committer: &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: mainTime},
	})
	if err != nil {
		t.Fatal(err)
	}
	mergeTime := time.Unix(1_700_000_032, 0)
	if _, err := worktree.Commit("merge branches", &git.CommitOptions{
		Parents:           []plumbing.Hash{mainHash, sideHash},
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: mergeTime},
		Committer:         &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: mergeTime},
	}); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"log"},
		{"log", "--oneline"},
		{"log", "--oneline", "-n", "3"},
		{"log", "--oneline", "HEAD~3"},
		{"log", "--oneline", "--", "history.txt"},
	} {
		var streamed, buffered, stderr bytes.Buffer
		handled, err := runStreamedGitLog(context.Background(), interp.HandlerContext{
			Dir: root, Stdout: &streamed, Stderr: &stderr,
		}, gitInvocation{effectiveDir: root, args: args})
		if err != nil || !handled {
			t.Fatalf("stream log %q = (handled %v, err %v)", args, handled, err)
		}
		err = runGoGit(context.Background(), interp.HandlerContext{
			Dir: root, Stdout: &buffered, Stderr: &stderr,
		}, gitInvocation{effectiveDir: root, args: args})
		if err != nil {
			t.Fatalf("buffered log %q: %v", args, err)
		}
		if streamed.String() != buffered.String() {
			streamLines, bufferedLines := strings.Split(streamed.String(), "\n"), strings.Split(buffered.String(), "\n")
			maxLines := len(streamLines)
			if len(bufferedLines) > maxLines {
				maxLines = len(bufferedLines)
			}
			for i := 0; i < maxLines; i++ {
				var streamLine, bufferedLine string
				if i < len(streamLines) {
					streamLine = streamLines[i]
				}
				if i < len(bufferedLines) {
					bufferedLine = bufferedLines[i]
				}
				if streamLine != bufferedLine {
					t.Errorf("streamed log %q first differs at line %d: streamed %q, buffered %q (line counts %d/%d)",
						args, i+1, streamLine, bufferedLine, len(streamLines), len(bufferedLines))
					break
				}
			}
		}
	}

	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	session.runner.Vars["GIT_PAGER"] = expand.Variable{
		Set: true, Exported: true, Kind: expand.String, Str: "less",
	}
	term := newCommandTerminal(context.Background(), 80, 3)
	ctx := withCommandTerminal(context.Background(), session, SourceUserShell, term)
	output := &pagerInputWriter{term: term, keys: []byte{'q'}}
	firstPageLines := -1
	output.onPrompt = func(current string) {
		if firstPageLines < 0 {
			firstPageLines = strings.Count(current, "\n")
		}
	}
	session.streamOut.setTarget(func(p []byte) {
		if _, err := output.Write(p); err != nil {
			t.Errorf("capture streamed Git log output: %v", err)
		}
	})
	defer session.streamOut.setTarget(nil)

	handled, err := runStreamedGitLog(ctx, interp.HandlerContext{
		Dir:    root,
		Stdout: session.streamOut,
		Stderr: io.Discard,
	}, gitInvocation{effectiveDir: root, args: []string{"log", "--oneline"}})
	if err != nil {
		t.Fatalf("runStreamedGitLog() error = %v", err)
	}
	if !handled {
		t.Fatal("runStreamedGitLog() did not handle ordinary git log")
	}
	if firstPageLines != 2 {
		t.Fatalf("lines produced before first pager prompt = %d, want 2", firstPageLines)
	}
	if output.prompts != 1 {
		t.Fatalf("pager prompts = %d, want quit after first page", output.prompts)
	}

	noPagerOutput := &pagerInputWriter{term: term, keys: []byte{'q'}}
	session.streamOut.setTarget(func(p []byte) {
		if _, err := noPagerOutput.Write(p); err != nil {
			t.Errorf("capture no-pager Git log output: %v", err)
		}
	})
	noPagerHandled, err := runStreamedGitLog(ctx, interp.HandlerContext{
		Dir:    root,
		Stdout: session.streamOut,
		Stderr: io.Discard,
	}, gitInvocation{effectiveDir: root, args: []string{"log", "-P", "--oneline"}})
	session.streamOut.setTarget(nil)
	if err != nil || !noPagerHandled {
		t.Fatalf("no-pager Git log = (handled %v, err %v)", noPagerHandled, err)
	}
	if noPagerOutput.prompts != 0 || strings.Count(noPagerOutput.String(), "\n") != 33 {
		t.Fatalf("disabled pager emitted %d prompts and %d lines; want all 33 lines without prompts",
			noPagerOutput.prompts, strings.Count(noPagerOutput.String(), "\n"))
	}

	fallbackTerm := newCommandTerminal(context.Background(), 80, 3)
	fallbackCtx := withCommandTerminal(context.Background(), session, SourceUserShell, fallbackTerm)
	fallbackOutput := &pagerInputWriter{term: fallbackTerm, keys: []byte{'q'}}
	session.streamOut.setTarget(func(p []byte) {
		if _, err := fallbackOutput.Write(p); err != nil {
			t.Errorf("capture fallback Git log output: %v", err)
		}
	})
	fallbackHandled, err := runBufferedGitLogWithPager(fallbackCtx, interp.HandlerContext{
		Dir:    root,
		Stdout: session.streamOut,
		Stderr: io.Discard,
	}, gitInvocation{effectiveDir: root, args: []string{"log", "--reverse", "--oneline"}})
	session.streamOut.setTarget(nil)
	if err != nil || !fallbackHandled {
		t.Fatalf("fallback pager = (handled %v, err %v)", fallbackHandled, err)
	}
	fallbackText := strings.ReplaceAll(fallbackOutput.String(), "\r\x1b[2K"+pagerPrompt, "")
	fallbackText = strings.ReplaceAll(fallbackText, "\r\x1b[2K", "")
	if got := strings.Count(fallbackText, "\n"); got != 2 {
		t.Fatalf("fallback pager displayed %d lines before quit, want 2", got)
	}

	session.SetCommandTerminalAttached(true)
	if err := session.SetViewportSize(80, 3); err != nil {
		t.Fatal(err)
	}
	type commandResult struct {
		exitCode int
		err      error
	}
	done := make(chan commandResult, 1)
	inputErr := make(chan error, 1)
	var commandOutput bytes.Buffer
	go func() {
		quitSent := false
		record, err := session.RunCommand(context.Background(), "git log --oneline", nil, SourceUserShell, func(chunk []byte) {
			_, _ = commandOutput.Write(chunk)
			if !quitSent && bytes.Contains(chunk, []byte(pagerPrompt)) {
				quitSent = true
				inputErr <- session.WriteCommandTerminalInputError([]byte{'q'})
			}
		})
		done <- commandResult{exitCode: record.ExitCode, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil || result.exitCode != 0 {
			t.Fatalf("interactive RunCommand git log = (exit %d, err %v)", result.exitCode, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interactive RunCommand git log did not return after pager quit")
	}
	select {
	case err := <-inputErr:
		if err != nil {
			t.Fatalf("send git log pager quit key: %v", err)
		}
	default:
		t.Fatal("interactive RunCommand git log did not display a pager prompt")
	}
	if !strings.Contains(commandOutput.String(), pagerPrompt) {
		t.Fatal("interactive RunCommand git log output did not contain the pager prompt")
	}
}
