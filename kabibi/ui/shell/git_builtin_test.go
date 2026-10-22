package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/mavity/rusticated/kabibi/gitcore"
)

func TestGitBuiltinStatusUsesExecutionSessionDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}

	nested := filepath.Join(root, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "new.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	session, err := NewExecutionSession(nested)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	record, err := session.RunCommand(context.Background(), "git status --short", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("run git status: %v", err)
	}
	if record.ExitCode != 0 {
		t.Fatalf("git status exit code = %d, output: %q", record.ExitCode, record.Output)
	}
	if !strings.Contains(record.Output, "?? src/new.go") {
		t.Fatalf("git status output = %q, want untracked file from repository root", record.Output)
	}
}

func TestGitBuiltinVersionReportsGoGitModuleVersion(t *testing.T) {
	version, err := gitcore.LibraryVersion()
	if err != nil {
		t.Fatalf("read go-git version: %v", err)
	}
	session, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	record, err := session.RunCommand(context.Background(), "git --version", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("run git --version: %v", err)
	}
	want := "git version " + version + " (go-git)"
	if record.ExitCode != 0 || strings.TrimSpace(record.Output) != want {
		t.Fatalf("git --version result = (exit %d, %q), want %q", record.ExitCode, record.Output, want)
	}
}

func TestGitBuiltinHelpShowsSupportedCommands(t *testing.T) {
	session, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	record, err := session.RunCommand(context.Background(), "git --help", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("run git --help: %v", err)
	}
	for _, expected := range []string{"Usage:", "git --version", "git status", "git add", "git commit", "host Git is not invoked"} {
		if !strings.Contains(record.Output, expected) {
			t.Errorf("git --help output %q does not contain %q", record.Output, expected)
		}
	}
	if record.ExitCode != 0 {
		t.Fatalf("git --help exit code = %d, output: %q", record.ExitCode, record.Output)
	}
}

func TestGitBuiltinReportsMissingRepository(t *testing.T) {
	session, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	record, err := session.RunCommand(context.Background(), "git status --short", nil, SourceUserShell, nil)
	if err == nil {
		t.Fatal("git status outside a repository unexpectedly succeeded")
	}
	if record.ExitCode != 128 || !strings.Contains(record.Output, "git repository not found") {
		t.Fatalf("git status result = (exit %d, %q), want missing-repository error", record.ExitCode, record.Output)
	}
}

func TestGitBuiltinSupportsInitAndGlobalConfigOutsideRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workingDir := t.TempDir()
	repoDir := filepath.Join(workingDir, "new-repo")
	session, err := NewExecutionSession(workingDir)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}

	for _, command := range []string{
		"git -C '" + repoDir + "' init",
		"git config --global user.name 'Kabibi Global'",
		"git config --global user.email 'global@example.test'",
	} {
		record, err := session.RunCommand(context.Background(), command, nil, SourceUserShell, nil)
		if err != nil || record.ExitCode != 0 {
			t.Fatalf("%s: (exit %d, err %v, output %q)", command, record.ExitCode, err, record.Output)
		}
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
		t.Fatalf("git init did not create repository outside current directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); err != nil {
		t.Fatalf("git config --global did not use HOME: %v", err)
	}
}

func TestGitBuiltinForwardsFullStatusAddAndCommitOptions(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	cfg.User.Name = "Kabibi Test"
	cfg.User.Email = "author@example.test"
	if err := repo.Storer.SetConfig(cfg); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}
	trackedPath := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(trackedPath, []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	for _, command := range []string{
		"git add tracked.txt",
		"git commit -m initial",
	} {
		record, err := session.RunCommand(context.Background(), command, nil, SourceUserShell, nil)
		if err != nil || record.ExitCode != 0 {
			t.Fatalf("%s: (exit %d, err %v, output %q)", command, record.ExitCode, err, record.Output)
		}
	}

	if err := os.WriteFile(trackedPath, []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	addRecord, err := session.RunCommand(context.Background(), "git add -u", nil, SourceUserShell, nil)
	if err != nil || addRecord.ExitCode != 0 {
		t.Fatalf("git add -u: (exit %d, err %v, output %q)", addRecord.ExitCode, err, addRecord.Output)
	}
	statusRecord, err := session.RunCommand(context.Background(), "git status --porcelain=v1 --untracked-files=all", nil, SourceUserShell, nil)
	if err != nil || statusRecord.ExitCode != 0 || !strings.Contains(statusRecord.Output, "M  tracked.txt") {
		t.Fatalf("extended git status = (exit %d, err %v, output %q)", statusRecord.ExitCode, err, statusRecord.Output)
	}
	commitRecord, err := session.RunCommand(context.Background(), "git commit --allow-empty -m extended --author='Inline Author <inline@example.test>'", nil, SourceUserShell, nil)
	if err != nil || commitRecord.ExitCode != 0 {
		t.Fatalf("extended git commit: (exit %d, err %v, output %q)", commitRecord.ExitCode, err, commitRecord.Output)
	}
}

func TestGitBuiltinHonorsInlineEnvironmentAndGlobalConfigFlags(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("contents\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}

	addRecord, err := session.RunCommand(context.Background(), "git -C . add file.txt", nil, SourceUserShell, nil)
	if err != nil || addRecord.ExitCode != 0 {
		t.Fatalf("git -C add: (exit %d, err %v, output %q)", addRecord.ExitCode, err, addRecord.Output)
	}
	commitRecord, err := session.RunCommand(context.Background(),
		`GIT_AUTHOR_NAME="Inline Author" GIT_AUTHOR_EMAIL=inline@example.test GIT_COMMITTER_NAME="Inline Committer" GIT_COMMITTER_EMAIL=committer@example.test git -c user.name=Override -c user.email=override@example.test commit -m inline`,
		nil, SourceUserShell, nil)
	if err != nil || commitRecord.ExitCode != 0 {
		t.Fatalf("inline git commit: (exit %d, err %v, output %q)", commitRecord.ExitCode, err, commitRecord.Output)
	}
	opened, err := git.PlainOpen(root)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	head, err := opened.Head()
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	commit, err := opened.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("read commit: %v", err)
	}
	if commit.Author.Name != "Inline Author" || commit.Author.Email != "inline@example.test" {
		t.Fatalf("commit author = %q <%s>, want inline identity", commit.Author.Name, commit.Author.Email)
	}
	if commit.Committer.Name != "Inline Committer" || commit.Committer.Email != "committer@example.test" {
		t.Fatalf("commit committer = %q <%s>, want inline identity", commit.Committer.Name, commit.Committer.Email)
	}
}

func TestGitBuiltinAddAndCommit(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	cfg.User.Name = "Kabibi Test"
	cfg.User.Email = "author@example.test"
	if err := repo.Storer.SetConfig(cfg); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}
	cwd := filepath.Join(root, "sub")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	session, err := NewExecutionSession(cwd)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	addRecord, err := session.RunCommand(context.Background(), "git add .", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("run git add: %v", err)
	}
	if addRecord.ExitCode != 0 {
		t.Fatalf("git add exit code = %d, output: %q", addRecord.ExitCode, addRecord.Output)
	}
	commitRecord, err := session.RunCommand(context.Background(), "git commit -m 'add new file'", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("run git commit: %v", err)
	}
	if commitRecord.ExitCode != 0 || !strings.Contains(commitRecord.Output, "add new file") {
		t.Fatalf("git commit result = (exit %d, %q)", commitRecord.ExitCode, commitRecord.Output)
	}
}

func TestGitBuiltinDiffLogBranchCheckout(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	cfg.User.Name = "Kabibi Test"
	cfg.User.Email = "author@example.test"
	if err := repo.Storer.SetConfig(cfg); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}

	testFile := filepath.Join(root, "file.txt")
	if err := os.WriteFile(testFile, []byte("line1\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	session, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}

	// 1. Stage and commit first commit
	if _, err := session.RunCommand(context.Background(), "git add file.txt", nil, SourceUserShell, nil); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := session.RunCommand(context.Background(), "git commit -m 'initial commit'", nil, SourceUserShell, nil); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// 2. Modify file and test git diff
	if err := os.WriteFile(testFile, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("modify file: %v", err)
	}
	diffRecord, err := session.RunCommand(context.Background(), "git diff", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("git diff error: %v", err)
	}
	if diffRecord.ExitCode != 0 {
		t.Fatalf("git diff exit code = %d, output: %q", diffRecord.ExitCode, diffRecord.Output)
	}
	if !strings.Contains(diffRecord.Output, "+line2") {
		t.Fatalf("git diff expected to contain '+line2', got: %q", diffRecord.Output)
	}

	// 3. Test git log
	logRecord, err := session.RunCommand(context.Background(), "git log --oneline", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("git log error: %v", err)
	}
	if logRecord.ExitCode != 0 {
		t.Fatalf("git log exit code = %d, output: %q", logRecord.ExitCode, logRecord.Output)
	}
	if !strings.Contains(logRecord.Output, "initial commit") {
		t.Fatalf("git log expected to contain 'initial commit', got: %q", logRecord.Output)
	}

	// 4. Test git branch and checkout -b
	branchRecord, err := session.RunCommand(context.Background(), "git checkout -b feature-test", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("git checkout -b error: %v", err)
	}
	if branchRecord.ExitCode != 0 {
		t.Fatalf("git checkout -b exit code = %d, output: %q", branchRecord.ExitCode, branchRecord.Output)
	}

	branchesRecord, err := session.RunCommand(context.Background(), "git branch", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("git branch error: %v", err)
	}
	if !strings.Contains(branchesRecord.Output, "feature-test") {
		t.Fatalf("git branch output expected to contain 'feature-test', got: %q", branchesRecord.Output)
	}
}
