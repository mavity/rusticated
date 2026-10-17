package gitcore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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

type CommitSummary struct {
	Hash    string
	Author  string
	Date    time.Time
	Message string
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

func (r *Repository) Log(limit int, filePath string) ([]CommitSummary, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	if limit <= 0 {
		return nil, errors.New("git log limit must be positive")
	}
	options := &git.LogOptions{Order: git.LogOrderCommitterTime}
	if filePath != "" {
		options.FileName = &filePath
	}
	iter, err := r.repo.Log(options)
	if err != nil {
		return nil, fmt.Errorf("read git log: %w", err)
	}
	defer iter.Close()

	commits := make([]CommitSummary, 0, limit)
	for len(commits) < limit {
		commit, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read git log entry: %w", err)
		}
		commits = append(commits, CommitSummary{
			Hash:    commit.Hash.String(),
			Author:  commit.Author.String(),
			Date:    commit.Author.When,
			Message: strings.TrimSpace(strings.SplitN(commit.Message, "\n", 2)[0]),
		})
	}
	return commits, nil
}

func (r *Repository) Stage(paths []string, all bool) error {
	return r.StageFrom(r.Root(), paths, all)
}

func (r *Repository) StageFrom(baseDir string, paths []string, all bool) error {
	if r == nil || r.repo == nil {
		return errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return ErrBareRepository
	}
	if all && len(paths) > 0 {
		return errors.New("git add cannot combine repository-wide all with explicit paths")
	}
	if all {
		if err := r.worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return fmt.Errorf("stage all git changes: %w", err)
		}
		return nil
	}
	if len(paths) == 0 {
		return errors.New("git add requires at least one path or the all option")
	}
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return fmt.Errorf("resolve git add base directory %q: %w", baseDir, err)
	}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
			return fmt.Errorf("git add path %q must stay within the repository", path)
		}
		target, err := filepath.Abs(filepath.Join(base, filepath.FromSlash(path)))
		if err != nil {
			return fmt.Errorf("resolve git add path %q: %w", path, err)
		}
		relative, err := filepath.Rel(r.root, target)
		if err != nil {
			return fmt.Errorf("resolve repository-relative git add path %q: %w", path, err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return fmt.Errorf("git add path %q must stay within the repository", path)
		}
		if _, err := r.worktree.Add(filepath.ToSlash(relative)); err != nil {
			return fmt.Errorf("stage %q: %w", path, err)
		}
	}
	return nil
}

func (r *Repository) Commit(message string, all, amend bool, env map[string]string) (string, error) {
	if r == nil || r.repo == nil {
		return "", errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return "", ErrBareRepository
	}
	if strings.TrimSpace(message) == "" {
		return "", errors.New("git commit message is required")
	}
	if all && amend {
		return "", errors.New("git commit cannot combine all and amend")
	}
	author, committer, err := r.commitSignatures(env)
	if err != nil {
		return "", err
	}
	hash, err := r.worktree.Commit(message, &git.CommitOptions{
		All:       all,
		Amend:     amend,
		Author:    author,
		Committer: committer,
	})
	if err != nil {
		return "", fmt.Errorf("create git commit: %w", err)
	}
	return hash.String(), nil
}

func (r *Repository) commitSignatures(env map[string]string) (*object.Signature, *object.Signature, error) {
	authorName := firstNonEmpty(
		env["GIT_AUTHOR_NAME"],
	)
	authorEmail := firstNonEmpty(
		env["GIT_AUTHOR_EMAIL"],
	)
	committerName := firstNonEmpty(
		env["GIT_COMMITTER_NAME"],
	)
	committerEmail := firstNonEmpty(
		env["GIT_COMMITTER_EMAIL"],
	)
	if authorName == "" || authorEmail == "" || committerName == "" || committerEmail == "" {
		local, err := r.repo.Config()
		if err != nil {
			return nil, nil, fmt.Errorf("read repository Git config: %w", err)
		}
		authorName = firstNonEmpty(authorName, local.Author.Name, local.User.Name)
		authorEmail = firstNonEmpty(authorEmail, local.Author.Email, local.User.Email)
		committerName = firstNonEmpty(committerName, local.Committer.Name, local.User.Name)
		committerEmail = firstNonEmpty(committerEmail, local.Committer.Email, local.User.Email)
	}
	if authorName == "" || authorEmail == "" || committerName == "" || committerEmail == "" {
		global, err := config.LoadConfig(config.GlobalScope)
		if err != nil {
			return nil, nil, fmt.Errorf("read global Git config: %w", err)
		}
		authorName = firstNonEmpty(authorName, global.Author.Name, global.User.Name)
		authorEmail = firstNonEmpty(authorEmail, global.Author.Email, global.User.Email)
		committerName = firstNonEmpty(committerName, global.Committer.Name, global.User.Name)
		committerEmail = firstNonEmpty(committerEmail, global.Committer.Email, global.User.Email)
	}
	if authorName == "" || authorEmail == "" || committerName == "" || committerEmail == "" {
		return nil, nil, errors.New("Git commit identity is not configured; set GIT_AUTHOR_NAME/GIT_AUTHOR_EMAIL and GIT_COMMITTER_NAME/GIT_COMMITTER_EMAIL, or configure user.name and user.email in repository or global Git config")
	}

	now := time.Now()
	return &object.Signature{Name: authorName, Email: authorEmail, When: now},
		&object.Signature{Name: committerName, Email: committerEmail, When: now}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
