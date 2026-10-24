package gitcore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/mavity/rusticated/kabibi/internal/gitfs"
)

func initTestRepository(t *testing.T, root string, bare bool) *git.Repository {
	t.Helper()

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create test repository directory: %v", err)
	}
	worktreeFS := gitfs.New(root)
	var dotGitFS billy.Filesystem = worktreeFS
	var worktree billy.Filesystem
	if !bare {
		worktree = worktreeFS
		var err error
		dotGitFS, err = worktreeFS.Chroot(git.GitDirName)
		if err != nil {
			t.Fatalf("create test repository storage filesystem: %v", err)
		}
	}

	storer := filesystem.NewStorage(dotGitFS, cache.NewObjectLRUDefault())
	repo, err := git.Init(storer, worktree)
	if err != nil {
		t.Fatalf("initialize test repository: %v", err)
	}
	return repo
}

func TestOpenFindsRepositoryFromShellSubdirectory(t *testing.T) {
	root := t.TempDir()
	initTestRepository(t, root, false)
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
	initTestRepository(t, root, true)

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
	gitRepo := initTestRepository(t, root, false)
	var err error
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

func TestBackupDirtyFilesStoresLooseBlobs(t *testing.T) {
	root, opened, gitRepo := newCommittedRepository(t, "tracked.txt", "committed\n")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("modified raw bytes\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	backups, err := opened.BackupDirtyFiles([]string{"tracked.txt"})
	if err != nil {
		t.Fatalf("BackupDirtyFiles(): %v", err)
	}
	if len(backups) != 1 || backups["tracked.txt"] == "" {
		t.Fatalf("backups = %#v, want only tracked.txt", backups)
	}
	blob, err := gitRepo.BlobObject(plumbing.NewHash(backups["tracked.txt"]))
	if err != nil {
		t.Fatalf("read backup blob: %v", err)
	}
	reader, err := blob.Reader()
	if err != nil {
		t.Fatalf("open backup blob: %v", err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read backup blob: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close backup blob: %v", err)
	}
	if string(got) != "modified raw bytes\n" {
		t.Fatalf("backup blob content = %q, want exact raw worktree bytes", got)
	}
}

func TestDiffSeparatesStagedAndWorktreeChanges(t *testing.T) {
	root, opened, _ := newCommittedRepository(t, "tracked.txt", "committed\n")
	path := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(path, []byte("staged version\n"), 0o644); err != nil {
		t.Fatalf("write staged version: %v", err)
	}
	if err := opened.Stage([]string{"tracked.txt"}, false); err != nil {
		t.Fatalf("stage version: %v", err)
	}
	if err := os.WriteFile(path, []byte("worktree version\n"), 0o644); err != nil {
		t.Fatalf("write worktree version: %v", err)
	}

	staged, err := opened.Diff(true, "", "tracked.txt")
	if err != nil {
		t.Fatalf("staged Diff(): %v", err)
	}
	stagedPatch := FormatPatch(staged)
	if !strings.Contains(stagedPatch, "+staged version") || strings.Contains(stagedPatch, "+worktree version") {
		t.Fatalf("staged diff = %q, want staged content only", stagedPatch)
	}
	worktree, err := opened.Diff(false, "", "tracked.txt")
	if err != nil {
		t.Fatalf("worktree Diff(): %v", err)
	}
	worktreePatch := FormatPatch(worktree)
	if !strings.Contains(worktreePatch, "-staged version") || !strings.Contains(worktreePatch, "+worktree version") {
		t.Fatalf("worktree diff = %q, want index-to-worktree change", worktreePatch)
	}
}

func TestDiffThreeDotUsesMergeBaseAndRightSide(t *testing.T) {
	root, opened, gitRepo := newCommittedRepository(t, "tracked.txt", "base\n")
	base, err := gitRepo.Head()
	if err != nil {
		t.Fatalf("read base branch: %v", err)
	}
	baseBranch := base.Target()
	featureBranch := plumbing.NewBranchReferenceName("feature")
	if err := gitRepo.Storer.SetReference(plumbing.NewHashReference(featureBranch, base.Hash())); err != nil {
		t.Fatalf("create feature branch: %v", err)
	}
	if err := opened.worktree.Checkout(&git.CheckoutOptions{Branch: featureBranch}); err != nil {
		t.Fatalf("switch to feature branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\nfeature\n"), 0o644); err != nil {
		t.Fatalf("write feature change: %v", err)
	}
	if _, err := opened.worktree.Add("tracked.txt"); err != nil {
		t.Fatalf("stage feature change: %v", err)
	}
	featureHash, err := opened.worktree.Commit("feature change", &git.CommitOptions{
		Author: &object.Signature{Name: "Test User", Email: "test@example.test"},
	})
	if err != nil {
		t.Fatalf("commit feature change: %v", err)
	}
	if err := opened.worktree.Checkout(&git.CheckoutOptions{Branch: baseBranch}); err != nil {
		t.Fatalf("switch to base branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\nmain\n"), 0o644); err != nil {
		t.Fatalf("write base-branch change: %v", err)
	}
	if _, err := opened.worktree.Add("tracked.txt"); err != nil {
		t.Fatalf("stage base-branch change: %v", err)
	}
	mainHash, err := opened.worktree.Commit("base-branch change", &git.CommitOptions{
		Author: &object.Signature{Name: "Test User", Email: "test@example.test"},
	})
	if err != nil {
		t.Fatalf("commit base-branch change: %v", err)
	}

	files, err := opened.Diff(false, mainHash.String()+"..."+featureHash.String(), "tracked.txt")
	if err != nil {
		t.Fatalf("three-dot Diff(): %v", err)
	}
	patch := FormatPatch(files)
	if !strings.Contains(patch, "+feature") || strings.Contains(patch, "+main") {
		t.Fatalf("three-dot diff = %q, want only changes from merge base to right side", patch)
	}
}

func newCommittedRepository(t *testing.T, name, contents string) (string, *Repository, *git.Repository) {
	t.Helper()
	root := t.TempDir()
	gitRepo := initTestRepository(t, root, false)
	var err error
	cfg, err := gitRepo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	cfg.User.Name = "Test User"
	cfg.User.Email = "test@example.test"
	if err := gitRepo.Storer.SetConfig(cfg); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("write committed file: %v", err)
	}
	opened, err := Open(root)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := opened.Stage([]string{name}, false); err != nil {
		t.Fatalf("stage committed file: %v", err)
	}
	if _, err := opened.Commit("initial", false, false, map[string]string{
		"GIT_AUTHOR_NAME": "Test User", "GIT_AUTHOR_EMAIL": "test@example.test",
		"GIT_COMMITTER_NAME": "Test User", "GIT_COMMITTER_EMAIL": "test@example.test",
	}); err != nil {
		t.Fatalf("commit test file: %v", err)
	}
	return root, opened, gitRepo
}
