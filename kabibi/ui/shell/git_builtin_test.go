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
