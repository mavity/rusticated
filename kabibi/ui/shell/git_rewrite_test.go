package shell

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestRunGitSubShellUsesInProcessShell(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code, err := runGitSubShell(
		context.Background(),
		t.TempDir(),
		map[string]string{"BISECT_VALUE": "value with spaces"},
		`printf '%s' "$BISECT_VALUE"`,
		nil,
		&stdout,
		&stderr,
	)
	if err != nil {
		t.Fatalf("run shell command: %v", err)
	}
	if code != 0 || stdout.String() != "value with spaces" {
		t.Fatalf("shell result = (exit %d, stdout %q, stderr %q)", code, stdout.String(), stderr.String())
	}
}

func TestRunGitSubShellPreservesExitStatus(t *testing.T) {
	code, err := runGitSubShell(context.Background(), t.TempDir(), nil, "exit 7", nil, nil, nil)
	if err != nil {
		t.Fatalf("run exit command: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit status = %d, want 7", code)
	}
}

func TestJoinShellArgumentsPreservesArgumentBoundaries(t *testing.T) {
	command := joinShellArguments([]string{"test-script", "a value", "it's fine"})
	var stdout bytes.Buffer
	code, err := runGitSubShell(context.Background(), t.TempDir(), nil,
		"printf '%s|' "+command, nil, &stdout, nil)
	if err != nil {
		t.Fatalf("run quoted command: %v", err)
	}
	if code != 0 || stdout.String() != "test-script|a value|it's fine|" {
		t.Fatalf("quoted command result = (exit %d, stdout %q), want preserved arguments", code, stdout.String())
	}
}

func TestBisectTermReadsConfiguredTerms(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "BISECT_TERMS"), []byte("regression\nfixed\n"), 0o600); err != nil {
		t.Fatalf("write custom bisect terms: %v", err)
	}
	for _, test := range []struct {
		code int
		want string
	}{
		{code: 0, want: "fixed"},
		{code: 1, want: "regression"},
		{code: 125, want: "skip"},
	} {
		if got := bisectTerm(gitDir, test.code); got != test.want {
			t.Errorf("bisectTerm(%d) = %q, want %q", test.code, got, test.want)
		}
	}
}

func TestGitBisectRunMarksEachCommitInProcess(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	author := &object.Signature{Name: "Kabibi Test", Email: "test@example.test"}
	var commits []string
	for i, content := range []string{"good\n", "middle\n", "bad\n"} {
		if err := os.WriteFile(filepath.Join(root, "state.txt"), []byte(content), 0o644); err != nil {
			t.Fatalf("write commit %d: %v", i, err)
		}
		if _, err := worktree.Add("state.txt"); err != nil {
			t.Fatalf("stage commit %d: %v", i, err)
		}
		hash, err := worktree.Commit("commit "+content, &git.CommitOptions{Author: author})
		if err != nil {
			t.Fatalf("create commit %d: %v", i, err)
		}
		commits = append(commits, hash.String())
	}

	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	start, err := session.RunCommand(context.Background(),
		"git bisect start "+commits[2]+" "+commits[0], nil, SourceUserShell, nil)
	if err != nil || start.ExitCode != 0 {
		t.Fatalf("start bisect: (exit %d, err %v, output %q)", start.ExitCode, err, start.Output)
	}
	result, err := session.RunCommand(context.Background(), "git bisect run true", nil, SourceUserShell, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("run bisect: (exit %d, err %v, output %q)", result.ExitCode, err, result.Output)
	}
	if !strings.Contains(result.Output, "bisect run success") {
		t.Fatalf("bisect output %q does not report successful completion", result.Output)
	}
}

func TestGitReplaceCreatesListsAndDeletesReplacementRef(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	author := &object.Signature{Name: "Kabibi Test", Email: "test@example.test"}
	var commits []string
	for _, content := range []string{"first\n", "second\n"} {
		if err := os.WriteFile(filepath.Join(root, "state.txt"), []byte(content), 0o644); err != nil {
			t.Fatalf("write commit content: %v", err)
		}
		if _, err := worktree.Add("state.txt"); err != nil {
			t.Fatalf("stage commit content: %v", err)
		}
		hash, err := worktree.Commit("commit "+content, &git.CommitOptions{Author: author})
		if err != nil {
			t.Fatalf("create commit: %v", err)
		}
		commits = append(commits, hash.String())
	}
	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	create, err := session.RunCommand(context.Background(),
		"git replace "+commits[0]+" "+commits[1], nil, SourceUserShell, nil)
	if err != nil || create.ExitCode != 0 {
		t.Fatalf("create replacement: (exit %d, err %v, output %q)", create.ExitCode, err, create.Output)
	}
	list, err := session.RunCommand(context.Background(), "git replace --list", nil, SourceUserShell, nil)
	if err != nil || list.ExitCode != 0 || !strings.Contains(list.Output, commits[0]) {
		t.Fatalf("list replacements: (exit %d, err %v, output %q)", list.ExitCode, err, list.Output)
	}
	remove, err := session.RunCommand(context.Background(),
		"git replace --delete "+commits[0], nil, SourceUserShell, nil)
	if err != nil || remove.ExitCode != 0 {
		t.Fatalf("delete replacement: (exit %d, err %v, output %q)", remove.ExitCode, err, remove.Output)
	}
	list, err = session.RunCommand(context.Background(), "git replace --list", nil, SourceUserShell, nil)
	if err != nil || list.ExitCode != 0 || strings.Contains(list.Output, commits[0]) {
		t.Fatalf("list after deletion: (exit %d, err %v, output %q)", list.ExitCode, err, list.Output)
	}
	graft, err := session.RunCommand(context.Background(),
		"git replace --graft "+commits[1], nil, SourceUserShell, nil)
	if err != nil || graft.ExitCode != 0 {
		t.Fatalf("graft commit: (exit %d, err %v, output %q)", graft.ExitCode, err, graft.Output)
	}
	replacementRef := plumbing.ReferenceName("refs/replace/" + commits[1])
	replacement, err := repo.Reference(replacementRef, false)
	if err != nil {
		t.Fatalf("read graft replacement ref: %v", err)
	}
	originalCommit, err := repo.CommitObject(plumbing.NewHash(commits[1]))
	if err != nil {
		t.Fatalf("read original commit: %v", err)
	}
	graftedCommit, err := repo.CommitObject(replacement.Hash())
	if err != nil {
		t.Fatalf("read grafted commit: %v", err)
	}
	if len(graftedCommit.ParentHashes) != 0 || graftedCommit.TreeHash != originalCommit.TreeHash ||
		graftedCommit.Message != originalCommit.Message {
		t.Fatalf("grafted commit = %#v, want same tree/message and no parents", graftedCommit)
	}
}

func TestGitNotesAddAppendListShowAndRemove(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}

	config, err := repo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	config.User.Name = "Kabibi Test"
	config.User.Email = "test@example.test"
	if err := repo.Storer.SetConfig(config); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	if _, err := worktree.Add("tracked.txt"); err != nil {
		t.Fatalf("stage tracked file: %v", err)
	}
	commitHash, err := worktree.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Kabibi Test", Email: "test@example.test"},
	})
	if err != nil {
		t.Fatalf("create commit: %v", err)
	}
	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	add, err := session.RunCommand(context.Background(),
		"git notes add -m first "+commitHash.String(), nil, SourceUserShell, nil)
	if err != nil || add.ExitCode != 0 {
		t.Fatalf("add note: (exit %d, err %v, output %q)", add.ExitCode, err, add.Output)
	}
	appendNote, err := session.RunCommand(context.Background(),
		"git notes append -m second "+commitHash.String(), nil, SourceUserShell, nil)
	if err != nil || appendNote.ExitCode != 0 {
		t.Fatalf("append note: (exit %d, err %v, output %q)", appendNote.ExitCode, err, appendNote.Output)
	}
	show, err := session.RunCommand(context.Background(),
		"git notes show "+commitHash.String(), nil, SourceUserShell, nil)
	if err != nil || show.ExitCode != 0 || show.Output != "first\nsecond" {
		t.Fatalf("show note: (exit %d, err %v, output %q)", show.ExitCode, err, show.Output)
	}
	list, err := session.RunCommand(context.Background(), "git notes list", nil, SourceUserShell, nil)
	if err != nil || list.ExitCode != 0 || !strings.Contains(list.Output, " "+commitHash.String()) {
		t.Fatalf("list notes: (exit %d, err %v, output %q)", list.ExitCode, err, list.Output)
	}
	remove, err := session.RunCommand(context.Background(),
		"git notes remove "+commitHash.String(), nil, SourceUserShell, nil)
	if err != nil || remove.ExitCode != 0 {
		t.Fatalf("remove note: (exit %d, err %v, output %q)", remove.ExitCode, err, remove.Output)
	}
}

func TestEncodeGitNotesTreeSortsEntries(t *testing.T) {
	repo, err := git.PlainInit(t.TempDir(), false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	root := &noteTreeNode{
		files: make(map[string]plumbing.Hash),
		dirs: map[string]*noteTreeNode{
			"bb": {
				files: map[string]plumbing.Hash{
					"z": plumbing.NewHash(strings.Repeat("2", 40)),
					"a": plumbing.NewHash(strings.Repeat("1", 40)),
				},
				dirs: make(map[string]*noteTreeNode),
			},
			"aa": {
				files: map[string]plumbing.Hash{
					"z": plumbing.NewHash(strings.Repeat("4", 40)),
					"a": plumbing.NewHash(strings.Repeat("3", 40)),
				},
				dirs: make(map[string]*noteTreeNode),
			},
		},
	}
	if _, err := encodeNoteTree(repo, root); err != nil {
		t.Fatalf("encode notes tree: %v", err)
	}
}

func TestGitNotesPruneRemovesNotesForUnreachableObjects(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	config, err := repo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	config.User.Name = "Kabibi Test"
	config.User.Email = "test@example.test"
	if err := repo.Storer.SetConfig(config); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	author := &object.Signature{Name: "Kabibi Test", Email: "test@example.test"}
	var commits []plumbing.Hash
	for _, content := range []string{"reachable\n", "unreachable\n"} {
		if err := os.WriteFile(filepath.Join(root, "state.txt"), []byte(content), 0o644); err != nil {
			t.Fatalf("write commit content: %v", err)
		}
		if _, err := worktree.Add("state.txt"); err != nil {
			t.Fatalf("stage commit content: %v", err)
		}
		hash, err := worktree.Commit("commit "+content, &git.CommitOptions{Author: author})
		if err != nil {
			t.Fatalf("create commit: %v", err)
		}
		commits = append(commits, hash)
	}
	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	for _, hash := range commits {
		add, err := session.RunCommand(context.Background(),
			"git notes add -m note "+hash.String(), nil, SourceUserShell, nil)
		if err != nil || add.ExitCode != 0 {
			t.Fatalf("add note for %s: (exit %d, err %v, output %q)", hash, add.ExitCode, err, add.Output)
		}
	}
	head, err := repo.Reference(plumbing.HEAD, false)
	if err != nil || head.Type() != plumbing.SymbolicReference {
		t.Fatalf("read symbolic HEAD: (%v, err %v)", head, err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(head.Target(), commits[0])); err != nil {
		t.Fatalf("move branch back to reachable commit: %v", err)
	}
	prune, err := session.RunCommand(context.Background(), "git notes prune", nil, SourceUserShell, nil)
	if err != nil || prune.ExitCode != 0 {
		t.Fatalf("prune notes: (exit %d, err %v, output %q)", prune.ExitCode, err, prune.Output)
	}
	list, err := session.RunCommand(context.Background(), "git notes list", nil, SourceUserShell, nil)
	if err != nil || list.ExitCode != 0 {
		t.Fatalf("list notes after prune: (exit %d, err %v, output %q)", list.ExitCode, err, list.Output)
	}
	if !strings.Contains(list.Output, " "+commits[0].String()) ||
		strings.Contains(list.Output, " "+commits[1].String()) {
		t.Fatalf("notes after prune = %q, want only reachable target %s", list.Output, commits[0])
	}
}
