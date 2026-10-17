package gitcore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
)

func TestOpenFindsRepositoryFromShellSubdirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	nested := filepath.Join(root, "src", "internal")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "tracked-later.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	repo, err := Open(nested)
	if err != nil {
		t.Fatalf("Open(%q): %v", nested, err)
	}
	if repo.Root() != root {
		t.Fatalf("repository root = %q, want %q", repo.Root(), root)
	}
	if repo.IsBare() {
		t.Fatal("worktree repository was reported as bare")
	}

	status, err := repo.Status()
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if len(status) != 1 || status[0].Path != "src/tracked-later.go" || status[0].WorktreeStatus != byte(git.Untracked) {
		t.Fatalf("status = %#v, want the untracked source file", status)
	}
}

func TestOpenSupportsBareRepository(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bare.git")
	if _, err := git.PlainInit(root, true); err != nil {
		t.Fatalf("initialize bare repository: %v", err)
	}

	repo, err := Open(root)
	if err != nil {
		t.Fatalf("Open(%q): %v", root, err)
	}
	if !repo.IsBare() {
		t.Fatal("bare repository was reported as a worktree")
	}
	if _, err := repo.Status(); !errors.Is(err, ErrBareRepository) {
		t.Fatalf("Status() error = %v, want ErrBareRepository", err)
	}
}

func TestOpenRejectsDirectoryOutsideRepository(t *testing.T) {
	_, err := Open(t.TempDir())
	if !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("Open() error = %v, want ErrRepositoryNotFound", err)
	}
}

func TestOpenRejectsFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open() accepted a file path")
	}
}
