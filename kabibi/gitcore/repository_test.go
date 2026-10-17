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

func TestStageCommitAndLog(t *testing.T) {
	root := t.TempDir()
	gitRepo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	cfg, err := gitRepo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	cfg.User.Name = "Local User"
	cfg.User.Email = "local@example.test"
	if err := gitRepo.Storer.SetConfig(cfg); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}
	path := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(path, []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	opened, err := Open(root)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := opened.Stage([]string{"tracked.txt"}, false); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	hash, err := opened.Commit("initial commit", false, false, map[string]string{
		"GIT_AUTHOR_NAME":     "Kabibi Test",
		"GIT_AUTHOR_EMAIL":    "author@example.test",
		"GIT_COMMITTER_NAME":  "Kabibi Test",
		"GIT_COMMITTER_EMAIL": "author@example.test",
	})
	if err != nil {
		t.Fatalf("commit staged file: %v", err)
	}
	if len(hash) != 40 {
		t.Fatalf("commit hash = %q, want full SHA-1 hash", hash)
	}
	commits, err := opened.Log(10, "tracked.txt")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(commits) != 1 || commits[0].Hash != hash || commits[0].Message != "initial commit" ||
		commits[0].Author != "Kabibi Test <author@example.test>" {
		t.Fatalf("log = %#v, want initial commit %s", commits, hash)
	}
	if err := opened.Stage([]string{"../outside.txt"}, false); err == nil {
		t.Fatal("Stage() accepted a path outside the repository")
	}
}
