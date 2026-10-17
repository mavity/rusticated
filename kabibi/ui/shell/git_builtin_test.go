package shell

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
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
