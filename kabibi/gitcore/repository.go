package gitcore

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
	"github.com/mavity/rusticated/kabibi/internal/gitfs"
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
	Hash        string
	Author      string
	AuthorName  string
	AuthorEmail string
	Date        time.Time
	Message     string
	FullMessage string
	Parents     []string
}

type BranchSummary struct {
	Name    string
	Hash    string
	Current bool
}

type ReflogEntry struct {
	Selector string
	OldHash  string
	NewHash  string
	Author   string
	Date     time.Time
	Message  string
}

type BlameLine struct {
	Hash    string
	Author  string
	Date    time.Time
	Content string
}

type DiffFile struct {
	Path      string
	Old       []byte
	New       []byte
	OldExists bool
	NewExists bool
	Binary    bool
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

	repo, err := openWithPathAwareFS(start, false)
	if errors.Is(err, git.ErrRepositoryNotExists) {
		repo, err = openWithPathAwareFS(start, true)
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

func openWithPathAwareFS(start string, detect bool) (*git.Repository, error) {
	path := start
	for {
		worktreeFS := gitfs.New(path)
		gitDirInfo, err := worktreeFS.Stat(git.GitDirName)

		var dotGitFS billy.Filesystem
		var worktree billy.Filesystem
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
			dotGitFS = worktreeFS
		} else {
			worktree = worktreeFS
			if gitDirInfo.IsDir() {
				dotGitFS, err = worktreeFS.Chroot(git.GitDirName)
			} else {
				dotGitFS, err = dotGitFileToFilesystem(path, worktreeFS)
			}
			if err != nil {
				return nil, err
			}
		}

		commonDirFS, err := commonDirectory(dotGitFS)
		if err != nil {
			return nil, err
		}
		repositoryFS := dotgit.NewRepositoryFilesystem(dotGitFS, commonDirFS)
		storer := filesystem.NewStorage(repositoryFS, cache.NewObjectLRUDefault())
		repo, err := git.Open(storer, worktree)
		if !detect || !errors.Is(err, git.ErrRepositoryNotExists) {
			return repo, err
		}

		parent := filepath.Dir(path)
		if parent == path {
			return nil, err
		}
		path = parent
	}
}

func dotGitFileToFilesystem(path string, fs billy.Filesystem) (billy.Filesystem, error) {
	data, err := readFilesystemFile(fs, git.GitDirName)
	if err != nil {
		return nil, err
	}

	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return nil, fmt.Errorf(".git file has no %s prefix", prefix)
	}

	gitDir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if filepath.IsAbs(gitDir) {
		return gitfs.New(gitDir), nil
	}
	return gitfs.New(fs.Join(path, gitDir)), nil
}

func commonDirectory(fs billy.Filesystem) (billy.Filesystem, error) {
	data, err := readFilesystemFile(fs, "commondir")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	path := strings.TrimSpace(string(data))
	var commonDir billy.Filesystem
	if filepath.IsAbs(path) {
		commonDir = gitfs.New(path)
	} else {
		commonDir = gitfs.New(filepath.Join(fs.Root(), path))
	}
	if _, err := commonDir.Stat(""); err != nil {
		if os.IsNotExist(err) {
			return nil, git.ErrRepositoryIncomplete
		}
		return nil, err
	}
	return commonDir, nil
}

func readFilesystemFile(fs billy.Filesystem, name string) ([]byte, error) {
	file, err := fs.Open(name)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return data, nil
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

// ConfigValue returns a value from this repository's resolved Git config.
func (r *Repository) ConfigValue(section, option string) (string, bool, error) {
	if r == nil || r.repo == nil {
		return "", false, errors.New("git repository is nil")
	}
	cfg, err := r.repo.ConfigScoped(config.SystemScope)
	if err != nil {
		return "", false, fmt.Errorf("read git configuration: %w", err)
	}
	options := cfg.Raw.Section(section).Options
	value, ok := options.Get(option), options.Has(option)
	return value, ok, nil
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
	return r.LogRevision(limit, filePath, "")
}

func (r *Repository) LogRevision(limit int, filePath, revision string) ([]CommitSummary, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	if limit <= 0 {
		return nil, errors.New("git log limit must be positive")
	}
	options := &git.LogOptions{Order: git.LogOrderCommitterTime}
	var excluded map[plumbing.Hash]bool
	if filePath != "" {
		options.FileName = &filePath
	}
	if revision != "" {
		if from, to, ranged := strings.Cut(revision, ".."); ranged {
			if from == "" || to == "" {
				return nil, fmt.Errorf("Git log range %q must include both revisions", revision)
			}
			hash, err := r.resolveCommitHash(to)
			if err != nil {
				return nil, err
			}
			options.From = hash
			fromHash, err := r.resolveCommitHash(from)
			if err != nil {
				return nil, err
			}
			excluded = r.commitAncestors(fromHash)
		} else {
			hash, err := r.resolveCommitHash(revision)
			if err != nil {
				return nil, err
			}
			options.From = hash
		}
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
		if excluded[commit.Hash] {
			continue
		}
		commits = append(commits, summarizeCommit(commit))
	}
	return commits, nil
}

// WalkLog visits commits in committer-time order without collecting the ordered
// log first. Range revisions may require an ancestry set before output begins.
// A non-positive limit means unlimited; returning an error from visit stops the
// walk and is propagated to the caller.
func (r *Repository) WalkLog(filePath, revision string, visit func(CommitSummary) error, limit int) error {
	if r == nil || r.repo == nil {
		return errors.New("git repository is nil")
	}
	if visit == nil {
		return errors.New("git log visitor is nil")
	}
	var excluded map[plumbing.Hash]bool
	if filePath != "" {
		filePath = filepath.ToSlash(filePath)
	}
	include := make([]plumbing.Hash, 0, 1)
	if revision != "" {
		if from, to, ranged := strings.Cut(revision, ".."); ranged {
			if from == "" {
				from = "HEAD"
			}
			if to == "" {
				to = "HEAD"
			}
			hash, err := r.resolveCommitHash(to)
			if err != nil {
				return err
			}
			include = append(include, hash)
			fromHash, err := r.resolveCommitHash(from)
			if err != nil {
				return err
			}
			excluded, err = r.commitAncestorSet(fromHash)
			if err != nil {
				return err
			}
		} else {
			hash, err := r.resolveCommitHash(revision)
			if err != nil {
				return err
			}
			include = append(include, hash)
		}
	} else {
		hash, err := r.resolveCommitHash("HEAD")
		if err != nil {
			return err
		}
		include = append(include, hash)
	}

	type queueItem struct {
		commit *object.Commit
		order  int
	}
	queue := make([]queueItem, 0, len(include))
	added := make(map[plumbing.Hash]bool)
	order := 0
	push := func(hash plumbing.Hash) error {
		if added[hash] {
			return nil
		}
		added[hash] = true
		commit, err := r.repo.CommitObject(hash)
		if err != nil {
			return fmt.Errorf("read Git log commit %s: %w", hash, err)
		}
		queue = append(queue, queueItem{commit: commit, order: order})
		order++
		return nil
	}
	for _, hash := range include {
		if err := push(hash); err != nil {
			return err
		}
	}
	count := 0
	for len(queue) > 0 && (limit <= 0 || count < limit) {
		best := 0
		for i := 1; i < len(queue); i++ {
			candidate, current := queue[i], queue[best]
			if candidate.commit.Committer.When.After(current.commit.Committer.When) ||
				(candidate.commit.Committer.When.Equal(current.commit.Committer.When) && candidate.order < current.order) {
				best = i
			}
		}
		commit := queue[best].commit
		queue = append(queue[:best], queue[best+1:]...)
		if excluded[commit.Hash] {
			continue
		}
		for _, parent := range commit.ParentHashes {
			if err := push(parent); err != nil {
				return err
			}
		}
		if filePath != "" {
			changed, err := r.commitChangesPath(commit, filePath)
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
		}
		if err := visit(summarizeCommit(commit)); err != nil {
			return err
		}
		count++
	}
	return nil
}

func summarizeCommit(commit *object.Commit) CommitSummary {
	summary := CommitSummary{
		Hash:        commit.Hash.String(),
		Author:      commit.Author.String(),
		AuthorName:  commit.Author.Name,
		AuthorEmail: commit.Author.Email,
		Date:        commit.Author.When,
		Message:     strings.TrimSpace(strings.SplitN(commit.Message, "\n", 2)[0]),
		FullMessage: commit.Message,
		Parents:     make([]string, len(commit.ParentHashes)),
	}
	for i, parent := range commit.ParentHashes {
		summary.Parents[i] = parent.String()
	}
	return summary
}

func (r *Repository) commitChangesPath(commit *object.Commit, filePath string) (bool, error) {
	currentTree, err := commit.Tree()
	if err != nil {
		return false, fmt.Errorf("read tree for Git log commit %s: %w", commit.Hash, err)
	}
	currentHash, currentExists, err := treePathHash(currentTree, filePath)
	if err != nil {
		return false, fmt.Errorf("read path %q from Git log commit %s: %w", filePath, commit.Hash, err)
	}
	var parentHash plumbing.Hash
	parentExists := false
	if len(commit.ParentHashes) > 0 {
		parent, err := r.repo.CommitObject(commit.ParentHashes[0])
		if err != nil {
			return false, fmt.Errorf("read parent of Git log commit %s: %w", commit.Hash, err)
		}
		parentTree, err := parent.Tree()
		if err != nil {
			return false, fmt.Errorf("read parent tree of Git log commit %s: %w", commit.Hash, err)
		}
		parentHash, parentExists, err = treePathHash(parentTree, filePath)
		if err != nil {
			return false, fmt.Errorf("read path %q from parent of Git log commit %s: %w", filePath, commit.Hash, err)
		}
	}
	return currentExists != parentExists || currentHash != parentHash, nil
}

func treePathHash(tree *object.Tree, filePath string) (plumbing.Hash, bool, error) {
	if file, err := tree.File(filePath); err == nil {
		return file.Hash, true, nil
	} else if !errors.Is(err, object.ErrFileNotFound) {
		return plumbing.ZeroHash, false, err
	}
	subtree, err := tree.Tree(strings.TrimSuffix(filePath, "/"))
	if errors.Is(err, object.ErrFileNotFound) {
		return plumbing.ZeroHash, false, nil
	}
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	return subtree.Hash, true, nil
}

func (r *Repository) commitAncestorSet(start plumbing.Hash) (map[plumbing.Hash]bool, error) {
	seen := make(map[plumbing.Hash]bool)
	pending := []plumbing.Hash{start}
	for len(pending) > 0 {
		hash := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[hash] {
			continue
		}
		seen[hash] = true
		commit, err := r.repo.CommitObject(hash)
		if err != nil {
			return nil, fmt.Errorf("read commit %s while walking Git log range: %w", hash, err)
		}
		pending = append(pending, commit.ParentHashes...)
	}
	return seen, nil
}

func (r *Repository) commitAncestors(start plumbing.Hash) map[plumbing.Hash]bool {
	seen := make(map[plumbing.Hash]bool)
	pending := []plumbing.Hash{start}
	for len(pending) > 0 {
		hash := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[hash] {
			continue
		}
		seen[hash] = true
		commit, err := r.repo.CommitObject(hash)
		if err == nil {
			pending = append(pending, commit.ParentHashes...)
		}
	}
	return seen
}

func (r *Repository) resolveCommitHash(revision string) (plumbing.Hash, error) {
	resolved, err := r.repo.ResolveRevision(plumbing.Revision(revision))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve Git revision %q: %w", revision, err)
	}
	return *resolved, nil
}

func (r *Repository) commit(revision string) (*object.Commit, error) {
	hash, err := r.resolveCommitHash(revision)
	if err != nil {
		return nil, err
	}
	commit, err := r.repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("read Git commit %q: %w", revision, err)
	}
	return commit, nil
}

func (r *Repository) Show(revision string) (*object.Commit, string, error) {
	if r == nil || r.repo == nil {
		return nil, "", errors.New("git repository is nil")
	}
	if revision == "" {
		revision = "HEAD"
	}
	commit, err := r.commit(revision)
	if err != nil {
		return nil, "", err
	}
	var patch string
	if len(commit.ParentHashes) > 0 {
		parent, err := r.repo.CommitObject(commit.ParentHashes[0])
		if err != nil {
			return nil, "", fmt.Errorf("read parent of Git commit %s: %w", commit.Hash, err)
		}
		files, err := r.compareTrees(parent.TreeHash, commit.TreeHash, "")
		if err != nil {
			return nil, "", err
		}
		patch = formatPatch(files)
	} else {
		files, err := r.treeFiles(commit.TreeHash, "")
		if err != nil {
			return nil, "", err
		}
		patch = formatPatch(changedFiles(map[string][]byte{}, files))
	}
	return commit, patch, nil
}

func (r *Repository) Diff(staged bool, revision, filePath string) ([]DiffFile, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	if revision != "" {
		return r.diffRevision(revision, filePath)
	}
	if r.worktree == nil {
		return nil, ErrBareRepository
	}
	head, err := r.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("read git HEAD for diff: %w", err)
	}
	headCommit, err := r.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("read git HEAD commit for diff: %w", err)
	}
	headFiles, err := r.treeFiles(headCommit.TreeHash, filePath)
	if err != nil {
		return nil, err
	}
	indexFiles, err := r.indexFiles(filePath)
	if err != nil {
		return nil, err
	}
	base, current := indexFiles, make(map[string][]byte, len(indexFiles))
	if staged {
		base, current = headFiles, indexFiles
	} else {
		for name, data := range indexFiles {
			current[name] = data
		}
		status, err := r.Status()
		if err != nil {
			return nil, err
		}
		for _, entry := range status {
			if filePath != "" && !pathMatches(entry.Path, filePath) {
				continue
			}
			if entry.WorktreeStatus == byte(git.Unmodified) {
				continue
			}
			if entry.WorktreeStatus == byte(git.Deleted) {
				delete(current, entry.Path)
				continue
			}
			target, err := r.resolveWorktreePath(entry.Path)
			if err != nil {
				return nil, err
			}
			data, readErr := os.ReadFile(target)
			if os.IsNotExist(readErr) {
				delete(current, entry.Path)
				continue
			}
			if readErr != nil {
				return nil, fmt.Errorf("read worktree file %q for diff: %w", entry.Path, readErr)
			}
			current[entry.Path] = data
		}
	}
	return changedFiles(base, current), nil
}

func (r *Repository) indexFiles(filePath string) (map[string][]byte, error) {
	index, err := r.repo.Storer.Index()
	if err != nil {
		return nil, fmt.Errorf("read git index: %w", err)
	}
	files := make(map[string][]byte)
	for _, entry := range index.Entries {
		name := filepath.ToSlash(entry.Name)
		if filePath != "" && !pathMatches(name, filePath) {
			continue
		}
		blob, err := r.repo.BlobObject(entry.Hash)
		if err != nil {
			return nil, fmt.Errorf("read indexed blob for %q: %w", name, err)
		}
		reader, err := blob.Reader()
		if err != nil {
			return nil, fmt.Errorf("open indexed file %q: %w", name, err)
		}
		content, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read indexed file %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close indexed file %q: %w", name, closeErr)
		}
		files[name] = content
	}
	return files, nil
}

func (r *Repository) diffRevision(revision, filePath string) ([]DiffFile, error) {
	if strings.Contains(revision, "...") {
		from, to, ranged := strings.Cut(revision, "...")
		if !ranged || strings.Contains(to, "...") {
			return nil, fmt.Errorf("invalid three-dot Git diff range %q", revision)
		}
		if from == "" {
			from = "HEAD"
		}
		if to == "" {
			to = "HEAD"
		}
		fromCommit, err := r.commit(from)
		if err != nil {
			return nil, err
		}
		toCommit, err := r.commit(to)
		if err != nil {
			return nil, err
		}
		mergeBases, err := fromCommit.MergeBase(toCommit)
		if err != nil {
			return nil, fmt.Errorf("find merge base for Git diff range %q: %w", revision, err)
		}
		if len(mergeBases) == 0 {
			return nil, fmt.Errorf("Git diff range %q has no merge base", revision)
		}
		return r.compareTrees(mergeBases[0].TreeHash, toCommit.TreeHash, filePath)
	}
	from, to, ranged := strings.Cut(revision, "..")
	if !ranged {
		commit, err := r.commit(revision)
		if err != nil {
			return nil, err
		}
		if len(commit.ParentHashes) == 0 {
			empty := make(map[string][]byte)
			tree, err := r.treeFiles(commit.TreeHash, filePath)
			if err != nil {
				return nil, err
			}
			return changedFiles(empty, tree), nil
		}
		parent, err := r.repo.CommitObject(commit.ParentHashes[0])
		if err != nil {
			return nil, fmt.Errorf("read parent of Git revision %q: %w", revision, err)
		}
		fromHash := parent.TreeHash
		toHash := commit.TreeHash
		return r.compareTrees(fromHash, toHash, filePath)
	}
	if from == "" {
		from = "HEAD"
	}
	if to == "" {
		to = "HEAD"
	}
	fromCommit, err := r.commit(from)
	if err != nil {
		return nil, err
	}
	toCommit, err := r.commit(to)
	if err != nil {
		return nil, err
	}
	return r.compareTrees(fromCommit.TreeHash, toCommit.TreeHash, filePath)
}

func (r *Repository) compareTrees(fromHash, toHash plumbing.Hash, filePath string) ([]DiffFile, error) {
	from, err := r.treeFiles(fromHash, filePath)
	if err != nil {
		return nil, err
	}
	to, err := r.treeFiles(toHash, filePath)
	if err != nil {
		return nil, err
	}
	return changedFiles(from, to), nil
}

func (r *Repository) treeFiles(treeHash plumbing.Hash, filePath string) (map[string][]byte, error) {
	tree, err := r.repo.TreeObject(treeHash)
	if err != nil {
		return nil, fmt.Errorf("read Git tree %s: %w", treeHash, err)
	}
	files := make(map[string][]byte)
	err = tree.Files().ForEach(func(file *object.File) error {
		name := filepath.ToSlash(file.Name)
		if filePath != "" && !pathMatches(name, filePath) {
			return nil
		}
		content, err := file.Contents()
		if err != nil {
			return fmt.Errorf("read Git tree file %q: %w", name, err)
		}
		files[name] = []byte(content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterate Git tree: %w", err)
	}
	return files, nil
}

func changedFiles(oldFiles, newFiles map[string][]byte) []DiffFile {
	names := make(map[string]struct{}, len(oldFiles)+len(newFiles))
	for name := range oldFiles {
		names[name] = struct{}{}
	}
	for name := range newFiles {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		oldData, oldExists := oldFiles[name]
		newData, newExists := newFiles[name]
		if oldExists != newExists || !bytesEqual(oldData, newData) {
			ordered = append(ordered, name)
		}
	}
	sort.Strings(ordered)
	files := make([]DiffFile, 0, len(ordered))
	for _, name := range ordered {
		oldData, oldExists := oldFiles[name]
		newData, newExists := newFiles[name]
		files = append(files, DiffFile{
			Path: name, Old: oldData, New: newData, OldExists: oldExists, NewExists: newExists,
			Binary: isBinary(oldData) || isBinary(newData),
		})
	}
	return files
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pathMatches(name, filter string) bool {
	name = filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	filter = filepath.ToSlash(filepath.Clean(filepath.FromSlash(filter)))
	if filter == "." || filter == "" {
		return true
	}
	return name == filter || strings.HasPrefix(name, strings.TrimSuffix(filter, "/")+"/")
}

func formatPatch(files []DiffFile) string {
	var out strings.Builder
	for _, file := range files {
		fmt.Fprintf(&out, "diff --git a/%s b/%s\n", file.Path, file.Path)
		switch {
		case !file.OldExists && file.NewExists:
			out.WriteString("new file mode 100644\n")
		case file.OldExists && !file.NewExists:
			out.WriteString("deleted file mode 100644\n")
		}
		oldName, newName := "a/"+file.Path, "b/"+file.Path
		if !file.OldExists {
			oldName = "/dev/null"
		}
		if !file.NewExists {
			newName = "/dev/null"
		}
		fmt.Fprintf(&out, "--- %s\n+++ %s\n", oldName, newName)
		if file.Binary {
			fmt.Fprintf(&out, "Binary files %s and %s differ\n", oldName, newName)
			continue
		}
		oldLines := patchLines(file.Old, file.OldExists)
		newLines := patchLines(file.New, file.NewExists)
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", patchStart(oldLines), len(oldLines), patchStart(newLines), len(newLines))
		for _, line := range oldLines {
			out.WriteByte('-')
			out.WriteString(line)
			out.WriteByte('\n')
		}
		for _, line := range newLines {
			out.WriteByte('+')
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func isBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func patchLines(data []byte, exists bool) []string {
	if !exists || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), "\n")
}

func patchStart(lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	return 1
}

func DiffStat(files []DiffFile) map[string]int {
	insertions, deletions := 0, 0
	for _, file := range files {
		if file.Binary {
			continue
		}
		deletions += len(patchLines(file.Old, file.OldExists))
		insertions += len(patchLines(file.New, file.NewExists))
	}
	return map[string]int{"files": len(files), "insertions": insertions, "deletions": deletions}
}

func FormatPatch(files []DiffFile) string {
	return formatPatch(files)
}

func (r *Repository) Branches() ([]BranchSummary, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	current, _ := r.CurrentBranch()
	iter, err := r.repo.References()
	if err != nil {
		return nil, fmt.Errorf("list Git references: %w", err)
	}
	defer iter.Close()
	var branches []BranchSummary
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name()
		if name.IsBranch() || name.IsRemote() {
			branches = append(branches, BranchSummary{
				Name:    name.Short(),
				Hash:    ref.Hash().String(),
				Current: name.IsBranch() && name.Short() == current,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterate Git branches: %w", err)
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })
	return branches, nil
}

func (r *Repository) Blame(filePath string) ([]BlameLine, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	if filePath == "" || filepath.IsAbs(filePath) || filepath.VolumeName(filePath) != "" {
		return nil, errors.New("git blame requires a repository-relative path")
	}
	head, err := r.repo.Head()
	if err != nil {
		return nil, fmt.Errorf("read git HEAD for blame: %w", err)
	}
	headCommit, err := r.repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("read git HEAD commit for blame: %w", err)
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read git HEAD tree for blame: %w", err)
	}
	if _, err := headTree.File(filepath.ToSlash(filePath)); err != nil {
		return nil, fmt.Errorf("read %q from git HEAD for blame: %w", filePath, err)
	}
	iter, err := r.repo.Log(&git.LogOptions{From: head.Hash(), Order: git.LogOrderCommitterTime})
	if err != nil {
		return nil, fmt.Errorf("read Git history for blame: %w", err)
	}
	defer iter.Close()
	type blameOrigin struct {
		hash   string
		author string
		date   time.Time
	}
	var history []*object.Commit
	for {
		commit, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Git history for blame: %w", err)
		}
		history = append(history, commit)
	}
	var lines []string
	origins := make([]blameOrigin, 0)
	for i := len(history) - 1; i >= 0; i-- {
		commit := history[i]
		tree, err := commit.Tree()
		if err != nil {
			return nil, fmt.Errorf("read tree for blame commit %s: %w", commit.Hash, err)
		}
		file, err := tree.File(filepath.ToSlash(filePath))
		if errors.Is(err, object.ErrFileNotFound) {
			lines = nil
			origins = nil
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %q in blame commit %s: %w", filePath, commit.Hash, err)
		}
		content, err := file.Contents()
		if err != nil {
			return nil, fmt.Errorf("read %q for blame: %w", filePath, err)
		}
		next := strings.Split(strings.TrimSuffix(strings.ReplaceAll(content, "\r\n", "\n"), "\n"), "\n")
		if content == "" {
			next = nil
		}
		oldPositions := make(map[string][]int)
		for pos, line := range lines {
			oldPositions[line] = append(oldPositions[line], pos)
		}
		nextOrigins := make([]blameOrigin, len(next))
		for pos, line := range next {
			if positions := oldPositions[line]; len(positions) > 0 {
				oldPos := positions[0]
				oldPositions[line] = positions[1:]
				if oldPos < len(origins) {
					nextOrigins[pos] = origins[oldPos]
					continue
				}
			}
			nextOrigins[pos] = blameOrigin{
				hash:   commit.Hash.String()[:7],
				author: commit.Author.Name,
				date:   commit.Author.When,
			}
		}
		lines, origins = next, nextOrigins
	}
	result := make([]BlameLine, len(lines))
	for i, line := range lines {
		result[i] = BlameLine{
			Hash:    origins[i].hash,
			Author:  origins[i].author,
			Date:    origins[i].date,
			Content: line,
		}
	}
	return result, nil
}

func (r *Repository) Reflog(revision string, limit int) ([]ReflogEntry, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	if limit <= 0 {
		return nil, errors.New("git reflog limit must be positive")
	}
	if revision == "" {
		revision = "HEAD"
	}
	if filepath.IsAbs(revision) || strings.Contains(revision, "..") || strings.ContainsAny(revision, `\:`) {
		return nil, fmt.Errorf("invalid Git reflog revision %q", revision)
	}
	rel := filepath.FromSlash(revision)
	gitDir, err := r.gitDir()
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(gitDir, "logs", rel)
	file, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("open Git reflog %q: %w", revision, err)
	}
	defer file.Close()
	var entries []ReflogEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 2)
		meta := strings.Fields(fields[0])
		if len(meta) < 4 {
			continue
		}
		seconds, _ := strconv.ParseInt(meta[len(meta)-2], 10, 64)
		entry := ReflogEntry{
			OldHash: meta[0],
			NewHash: meta[1],
			Author:  strings.Join(meta[2:len(meta)-2], " "),
			Date:    time.Unix(seconds, 0),
		}
		if len(fields) == 2 {
			entry.Message = fields[1]
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Git reflog %q: %w", revision, err)
	}
	result := make([]ReflogEntry, 0, limit)
	for i := len(entries) - 1; i >= 0 && len(result) < limit; i-- {
		entries[i].Selector = fmt.Sprintf("%s@{%d}", revision, len(entries)-1-i)
		result = append(result, entries[i])
	}
	return result, nil
}

func (r *Repository) gitDir() (string, error) {
	if r.IsBare() {
		return r.root, nil
	}
	path := filepath.Join(r.root, ".git")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect Git directory: %w", err)
	}
	if info.IsDir() {
		return path, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read Git directory pointer: %w", err)
	}
	line := strings.TrimSpace(string(data))
	gitDir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok || strings.TrimSpace(gitDir) == "" {
		return "", errors.New("invalid Git directory pointer")
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(r.root, gitDir)
	}
	abs, err := filepath.Abs(gitDir)
	if err != nil {
		return "", fmt.Errorf("resolve Git directory pointer: %w", err)
	}
	return abs, nil
}

func (r *Repository) StashList(limit int) ([]ReflogEntry, error) {
	entries, err := r.Reflog("refs/stash", limit)
	if os.IsNotExist(err) {
		return []ReflogEntry{}, nil
	}
	for i := range entries {
		entries[i].Selector = fmt.Sprintf("stash@{%d}", i)
	}
	return entries, err
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

func (r *Repository) BackupDirtyFiles(paths []string) (map[string]string, error) {
	if r == nil || r.repo == nil {
		return nil, errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return nil, ErrBareRepository
	}
	for _, path := range paths {
		if err := validateRepoPath(path); err != nil {
			return nil, err
		}
	}
	status, err := r.Status()
	if err != nil {
		return nil, err
	}
	backups := make(map[string]string)
	for _, entry := range status {
		if len(paths) > 0 && !matchesAnyPath(entry.Path, paths) {
			continue
		}
		target, err := r.resolveWorktreePath(entry.Path)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect dirty Git file %q for backup: %w", entry.Path, err)
		}
		var data []byte
		if info.Mode()&os.ModeSymlink != 0 {
			link, linkErr := os.Readlink(target)
			if linkErr != nil {
				return nil, fmt.Errorf("read dirty Git symlink %q for backup: %w", entry.Path, linkErr)
			}
			data = []byte(link)
		} else {
			data, err = os.ReadFile(target)
		}
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read dirty Git file %q for backup: %w", entry.Path, err)
		}
		blob := r.repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(data)))
		writer, err := blob.Writer()
		if err != nil {
			return nil, fmt.Errorf("create backup blob for %q: %w", entry.Path, err)
		}
		if _, err := writer.Write(data); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("write backup blob for %q: %w", entry.Path, err)
		}
		if err := writer.Close(); err != nil {
			return nil, fmt.Errorf("finish backup blob for %q: %w", entry.Path, err)
		}
		hash, err := r.repo.Storer.SetEncodedObject(blob)
		if err != nil {
			return nil, fmt.Errorf("store backup blob for %q: %w", entry.Path, err)
		}
		backups[filepath.ToSlash(entry.Path)] = hash.String()
	}
	return backups, nil
}

func validateRepoPath(path string) error {
	if strings.TrimSpace(path) == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return fmt.Errorf("Git path %q must stay within the repository", path)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Git path %q must stay within the repository", path)
	}
	return nil
}

func matchesAnyPath(name string, paths []string) bool {
	for _, path := range paths {
		if pathMatches(name, path) {
			return true
		}
	}
	return false
}

func (r *Repository) resolveWorktreePath(path string) (string, error) {
	if err := validateRepoPath(path); err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(r.root, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("resolve Git path %q: %w", path, err)
	}
	relative, err := filepath.Rel(r.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("Git path %q must stay within the repository", path)
	}
	rootReal, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree root: %w", err)
	}
	checkPath := target
	for {
		real, evalErr := filepath.EvalSymlinks(checkPath)
		if evalErr == nil {
			rel, relErr := filepath.Rel(rootReal, real)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return "", fmt.Errorf("Git path %q resolves outside the repository", path)
			}
			break
		}
		if !os.IsNotExist(evalErr) {
			return "", fmt.Errorf("resolve Git path %q: %w", path, evalErr)
		}
		parent := filepath.Dir(checkPath)
		if parent == checkPath {
			return "", fmt.Errorf("resolve Git path %q: %w", path, evalErr)
		}
		checkPath = parent
	}
	return target, nil
}

func (r *Repository) Unstage(paths []string, all bool) error {
	if r == nil || r.repo == nil {
		return errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return ErrBareRepository
	}
	if all && len(paths) > 0 {
		return errors.New("git unstage cannot combine all with explicit paths")
	}
	if !all && len(paths) == 0 {
		return errors.New("git unstage requires at least one path or the all option")
	}
	for _, path := range paths {
		if err := validateRepoPath(path); err != nil {
			return err
		}
	}
	head, err := r.repo.Head()
	if err != nil {
		return fmt.Errorf("read git HEAD for unstage: %w", err)
	}
	options := &git.ResetOptions{Mode: git.MixedReset, Commit: head.Hash()}
	if !all {
		options.Files = make([]string, len(paths))
		for i, path := range paths {
			options.Files[i] = filepath.ToSlash(path)
		}
	}
	if err := r.worktree.Reset(options); err != nil {
		return fmt.Errorf("unstage Git changes: %w", err)
	}
	return nil
}

func (r *Repository) CreateBranch(name, revision string, checkout bool) (string, error) {
	if r == nil || r.repo == nil {
		return "", errors.New("git repository is nil")
	}
	if err := validateBranchName(name); err != nil {
		return "", err
	}
	refName := plumbing.NewBranchReferenceName(name)
	if _, err := r.repo.Reference(refName, false); err == nil {
		return "", fmt.Errorf("Git branch %q already exists", name)
	}
	if revision == "" {
		revision = "HEAD"
	}
	hash, err := r.resolveCommitHash(revision)
	if err != nil {
		return "", err
	}
	if err := r.repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		return "", fmt.Errorf("create Git branch %q: %w", name, err)
	}
	if checkout {
		if r.worktree == nil {
			_ = r.repo.Storer.RemoveReference(refName)
			return "", ErrBareRepository
		}
		if err := r.worktree.Checkout(&git.CheckoutOptions{Branch: refName}); err != nil {
			_ = r.repo.Storer.RemoveReference(refName)
			return "", fmt.Errorf("switch to new Git branch %q: %w", name, err)
		}
	}
	return hash.String(), nil
}

func (r *Repository) DeleteBranch(name string, force bool) error {
	if r == nil || r.repo == nil {
		return errors.New("git repository is nil")
	}
	if err := validateBranchName(name); err != nil {
		return err
	}
	refName := plumbing.NewBranchReferenceName(name)
	ref, err := r.repo.Reference(refName, true)
	if err != nil {
		return fmt.Errorf("read Git branch %q: %w", name, err)
	}
	current, err := r.CurrentBranch()
	if err == nil && current == name {
		return fmt.Errorf("cannot delete the checked out Git branch %q", name)
	}
	if !force {
		head, err := r.repo.Head()
		if err != nil {
			return fmt.Errorf("read git HEAD before deleting branch: %w", err)
		}
		if !r.isAncestor(ref.Hash(), head.Hash()) {
			return fmt.Errorf("Git branch %q is not merged; use force to delete it", name)
		}
	}
	if err := r.repo.Storer.RemoveReference(refName); err != nil {
		return fmt.Errorf("delete Git branch %q: %w", name, err)
	}
	return nil
}

func validateBranchName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n~^:?*[\\") ||
		strings.Contains(name, "..") || strings.Contains(name, "@{") || name == "@" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid Git branch name %q", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("invalid Git branch name %q", name)
		}
		for _, r := range component {
			if r < 0x20 || r == 0x7f || r == ']' {
				return fmt.Errorf("invalid Git branch name %q", name)
			}
		}
	}
	return nil
}

func (r *Repository) isAncestor(candidate, descendant plumbing.Hash) bool {
	seen := make(map[plumbing.Hash]bool)
	pending := []plumbing.Hash{descendant}
	for len(pending) > 0 {
		hash := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if hash == candidate {
			return true
		}
		if seen[hash] {
			continue
		}
		seen[hash] = true
		commit, err := r.repo.CommitObject(hash)
		if err != nil {
			continue
		}
		pending = append(pending, commit.ParentHashes...)
	}
	return false
}

func (r *Repository) Switch(name string, force bool) (string, error) {
	if r == nil || r.repo == nil {
		return "", errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return "", ErrBareRepository
	}
	if strings.TrimSpace(name) == "" {
		return "", errors.New("git switch requires a branch or revision")
	}
	options := &git.CheckoutOptions{Force: force}
	if !strings.HasPrefix(name, "refs/") {
		branch := plumbing.NewBranchReferenceName(name)
		if _, err := r.repo.Reference(branch, true); err == nil {
			options.Branch = branch
		}
	}
	if options.Branch == "" {
		hash, err := r.resolveCommitHash(name)
		if err != nil {
			return "", err
		}
		options.Hash = hash
	}
	if err := r.worktree.Checkout(options); err != nil {
		return "", fmt.Errorf("switch Git checkout to %q: %w", name, err)
	}
	head, err := r.repo.Head()
	if err != nil {
		return "", fmt.Errorf("read git HEAD after switch: %w", err)
	}
	return head.Hash().String(), nil
}

func (r *Repository) Restore(paths []string, staged bool, revision string) error {
	if r == nil || r.repo == nil {
		return errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return ErrBareRepository
	}
	if len(paths) == 0 {
		return errors.New("git restore requires at least one path")
	}
	for _, path := range paths {
		if err := validateRepoPath(path); err != nil {
			return err
		}
	}
	if revision == "" {
		revision = "HEAD"
	}
	commit, err := r.commit(revision)
	if err != nil {
		return err
	}
	if staged {
		options := &git.ResetOptions{Mode: git.MixedReset, Commit: commit.Hash}
		options.Files = make([]string, len(paths))
		for i, path := range paths {
			options.Files[i] = filepath.ToSlash(path)
		}
		if err := r.worktree.Reset(options); err != nil {
			return fmt.Errorf("restore staged Git paths: %w", err)
		}
		return nil
	}
	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("read Git tree for restore: %w", err)
	}
	for _, path := range paths {
		target, err := r.resolveWorktreePath(path)
		if err != nil {
			return err
		}
		file, fileErr := tree.File(filepath.ToSlash(path))
		if errors.Is(fileErr, object.ErrFileNotFound) {
			if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) {
				return fmt.Errorf("remove restored Git path %q: %w", path, removeErr)
			}
			continue
		}
		if fileErr != nil {
			return fmt.Errorf("read Git restore source %q: %w", path, fileErr)
		}
		content, err := file.Contents()
		if err != nil {
			return fmt.Errorf("read Git restore source %q: %w", path, err)
		}
		if file.Mode == filemode.Symlink {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create directory for restored Git symlink %q: %w", path, err)
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("replace restored Git symlink %q: %w", path, err)
			}
			if err := os.Symlink(content, target); err != nil {
				return fmt.Errorf("restore Git symlink %q: %w", path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create directory for restored Git path %q: %w", path, err)
		}
		mode := os.FileMode(0o644)
		if file.Mode == filemode.Executable {
			mode = 0o755
		}
		if err := os.WriteFile(target, []byte(content), mode); err != nil {
			return fmt.Errorf("restore Git path %q: %w", path, err)
		}
	}
	return nil
}

func (r *Repository) Reset(revision, mode string) error {
	if r == nil || r.repo == nil {
		return errors.New("git repository is nil")
	}
	if r.worktree == nil {
		return ErrBareRepository
	}
	if revision == "" {
		revision = "HEAD"
	}
	hash, err := r.resolveCommitHash(revision)
	if err != nil {
		return err
	}
	options := &git.ResetOptions{Commit: hash}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "soft":
		options.Mode = git.SoftReset
	case "", "mixed":
		options.Mode = git.MixedReset
	case "hard":
		options.Mode = git.HardReset
	default:
		return fmt.Errorf("unsupported git reset mode %q; supported modes: soft, mixed, hard", mode)
	}
	if err := r.worktree.Reset(options); err != nil {
		return fmt.Errorf("reset Git repository to %q: %w", revision, err)
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
