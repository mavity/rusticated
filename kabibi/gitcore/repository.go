package gitcore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

var (
	ErrRepositoryNotFound = errors.New("git repository not found")
	ErrBareRepository     = errors.New("operation requires a working tree")
)

type Repository struct {
	root     string
	repo     *git.Repository
	worktree *git.Worktree
}

type StatusEntry struct {
	Path           string
	IndexStatus    byte
	WorktreeStatus byte
	OriginalPath   string
}

func Open(path string) (*Repository, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("git repository start directory is empty")
	}

	start, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve git repository start directory %q: %w", path, err)
	}
	info, err := os.Stat(start)
	if err != nil {
		return nil, fmt.Errorf("inspect git repository start directory %q: %w", start, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("git repository start path %q is not a directory", start)
	}

	repo, err := git.PlainOpenWithOptions(start, &git.PlainOpenOptions{
		EnableDotGitCommonDir: true,
	})
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = git.PlainOpenWithOptions(start, &git.PlainOpenOptions{
			DetectDotGit:          true,
			EnableDotGitCommonDir: true,
		})
	}
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return nil, fmt.Errorf("%w from %q", ErrRepositoryNotFound, start)
		}
		return nil, fmt.Errorf("open git repository from %q: %w", start, err)
	}

	worktree, err := repo.Worktree()
	root := start
	if err == nil {
		root = worktree.Filesystem.Root()
	} else if !errors.Is(err, git.ErrIsBareRepository) {
		return nil, fmt.Errorf("open git worktree from %q: %w", start, err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve git repository root %q: %w", root, err)
	}

	return &Repository{root: filepath.Clean(root), repo: repo, worktree: worktree}, nil
}

func (r *Repository) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

func (r *Repository) IsBare() bool {
	return r != nil && r.worktree == nil
}

func (r *Repository) CurrentBranch() (string, error) {
	if r == nil || r.repo == nil {
		return "", errors.New("git repository is nil")
	}
	head, err := r.repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return "", fmt.Errorf("read git HEAD: %w", err)
	}
	if head.Type() == plumbing.SymbolicReference && head.Target().IsBranch() {
		return head.Target().Short(), nil
	}
	return "(detached)", nil
}

func (r *Repository) Status() ([]StatusEntry, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return nil, ErrBareRepository
	}
	status, err := r.worktree.Status()
	if err != nil {
		return nil, fmt.Errorf("read git status: %w", err)
	}

	entries := make([]StatusEntry, 0, len(status))
	for path, file := range status {
		if file.Staging == git.Unmodified && file.Worktree == git.Unmodified {
			continue
		}
		entries = append(entries, StatusEntry{
			Path:           filepath.ToSlash(path),
			IndexStatus:    byte(file.Staging),
			WorktreeStatus: byte(file.Worktree),
			OriginalPath:   filepath.ToSlash(file.Extra),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}
