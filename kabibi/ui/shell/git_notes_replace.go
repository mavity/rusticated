package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"mvdan.cc/sh/v3/interp"
)

type gitNotesOptionSyntax struct {
	name       string
	takesValue bool
}

type gitNotesCommandSyntax struct {
	name    string
	options []gitNotesOptionSyntax
}

var gitNotesRefOption = gitNotesOptionSyntax{name: "--ref", takesValue: true}

var gitNoteMutationOptions = []gitNotesOptionSyntax{
	{name: "-f"},
	{name: "--force"},
	{name: "-m", takesValue: true},
	{name: "--message", takesValue: true},
}

var gitNotesCommands = []gitNotesCommandSyntax{
	{name: "add", options: gitNoteMutationOptions},
	{name: "append", options: gitNoteMutationOptions},
	{name: "list"},
	{name: "prune"},
	{name: "remove"},
	{name: "show"},
}

type gitReplaceOptionSyntax struct {
	name string
	mode string
}

var gitReplaceOptions = []gitReplaceOptionSyntax{
	{name: "-l", mode: "list"},
	{name: "--list", mode: "list"},
	{name: "-d", mode: "delete"},
	{name: "--delete", mode: "delete"},
	{name: "-f", mode: "force"},
	{name: "--force", mode: "force"},
	{name: "-g", mode: "graft"},
	{name: "--graft", mode: "graft"},
}

func gitNotesCommandNames() []string {
	names := make([]string, len(gitNotesCommands))
	for i, command := range gitNotesCommands {
		names[i] = command.name
	}
	return names
}

func gitReplaceOptionNames() []string {
	names := make([]string, len(gitReplaceOptions))
	for i, option := range gitReplaceOptions {
		names[i] = option.name
	}
	return names
}

func matchGitReplaceOption(arg string) (string, bool) {
	for _, option := range gitReplaceOptions {
		if arg == option.name {
			return option.mode, true
		}
	}
	return "", false
}

func gitNotesOptionNames(command string) []string {
	options := []string{gitNotesRefOption.name}
	for _, spec := range gitNotesCommands {
		if spec.name == command {
			for _, option := range spec.options {
				options = append(options, option.name)
			}
			break
		}
	}
	return options
}

func matchGitNotesOption(arg string, specs []gitNotesOptionSyntax) (gitNotesOptionSyntax, string, bool, bool) {
	for _, spec := range specs {
		if arg == spec.name {
			return spec, "", false, true
		}
		if spec.takesValue && strings.HasPrefix(arg, spec.name+"=") {
			return spec, strings.TrimPrefix(arg, spec.name+"="), true, true
		}
	}
	return gitNotesOptionSyntax{}, "", false, false
}

func runGitReplace(hc interp.HandlerContext, invocation gitInvocation) error {
	args := invocation.args[1:]
	if len(args) == 0 {
		fmt.Fprintln(hc.Stderr, "usage: git replace [-f] <object> <replacement>")
		return interp.ExitStatus(2)
	}

	repositoryPath := invocation.effectiveDir
	if invocation.gitDir != "" {
		repositoryPath = invocation.gitDir
	}
	repo, err := git.PlainOpenWithOptions(repositoryPath, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace: %v\n", err)
		return interp.ExitStatus(128)
	}
	mode, isOption := matchGitReplaceOption(args[0])
	if isOption && mode == "list" {
		pattern := ""
		if len(args) > 1 {
			pattern = args[1]
		}
		if len(args) > 2 {
			fmt.Fprintln(hc.Stderr, "usage: git replace [-l] [<pattern>]")
			return interp.ExitStatus(2)
		}
		return listGitReplacements(hc.Stdout, hc.Stderr, repo, pattern)
	}
	if isOption && mode == "delete" {
		if len(args) < 2 {
			fmt.Fprintln(hc.Stderr, "usage: git replace -d <object>...")
			return interp.ExitStatus(2)
		}
		for _, revision := range args[1:] {
			hash, err := resolveGitObject(repo, revision)
			if err != nil {
				fmt.Fprintf(hc.Stderr, "git replace: %v\n", err)
				return interp.ExitStatus(1)
			}
			refName := plumbing.ReferenceName("refs/replace/" + hash.String())
			if err := repo.Storer.RemoveReference(refName); err != nil {
				fmt.Fprintf(hc.Stderr, "git replace: delete %s: %v\n", hash, err)
				return interp.ExitStatus(1)
			}
		}
		return nil
	}

	force := false
	if isOption && mode == "force" {
		force = true
		args = args[1:]
	}
	if len(args) > 0 {
		mode, isOption = matchGitReplaceOption(args[0])
	}
	if len(args) > 0 && isOption && mode == "graft" {
		return runGitReplaceGraft(hc, repo, args[1:], force)
	}
	if len(args) != 2 {
		fmt.Fprintln(hc.Stderr, "usage: git replace [-f] <object> <replacement>")
		return interp.ExitStatus(2)
	}
	original, err := resolveGitObject(repo, args[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace: %v\n", err)
		return interp.ExitStatus(1)
	}
	replacement, err := resolveGitObject(repo, args[1])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace: %v\n", err)
		return interp.ExitStatus(1)
	}
	originalObject, err := repo.Storer.EncodedObject(plumbing.AnyObject, original)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace: read object %s: %v\n", original, err)
		return interp.ExitStatus(1)
	}
	replacementObject, err := repo.Storer.EncodedObject(plumbing.AnyObject, replacement)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace: read object %s: %v\n", replacement, err)
		return interp.ExitStatus(1)
	}
	if originalObject.Type() != replacementObject.Type() {
		fmt.Fprintln(hc.Stderr, "git replace: objects must be of the same type")
		return interp.ExitStatus(1)
	}

	refName := plumbing.ReferenceName("refs/replace/" + original.String())
	if !force {
		if _, err := repo.Storer.Reference(refName); err == nil {
			fmt.Fprintf(hc.Stderr, "git replace: replacement for %s already exists\n", original)
			return interp.ExitStatus(1)
		}
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, replacement)); err != nil {
		fmt.Fprintf(hc.Stderr, "git replace: write replacement ref: %v\n", err)
		return interp.ExitStatus(1)
	}
	return nil
}

func runGitReplaceGraft(hc interp.HandlerContext, repo *git.Repository, args []string, force bool) error {
	var positional []string
	for _, arg := range args {
		switch arg {
		case "-f", "--force":
			force = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintln(hc.Stderr, "usage: git replace [-f] --graft <commit> [<parent>...]")
				return interp.ExitStatus(2)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) == 0 {
		fmt.Fprintln(hc.Stderr, "usage: git replace [-f] --graft <commit> [<parent>...]")
		return interp.ExitStatus(2)
	}
	targetHash, err := resolveGitObject(repo, positional[0])
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace --graft: %v\n", err)
		return interp.ExitStatus(1)
	}
	target, err := repo.CommitObject(targetHash)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace --graft: target %s is not a commit: %v\n", targetHash, err)
		return interp.ExitStatus(1)
	}
	parents := make([]plumbing.Hash, 0, len(positional)-1)
	seen := make(map[plumbing.Hash]struct{}, len(positional)-1)
	for _, revision := range positional[1:] {
		parentHash, err := resolveGitObject(repo, revision)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git replace --graft: %v\n", err)
			return interp.ExitStatus(1)
		}
		if parentHash == targetHash {
			fmt.Fprintln(hc.Stderr, "git replace --graft: a commit cannot be its own parent")
			return interp.ExitStatus(1)
		}
		if _, exists := seen[parentHash]; exists {
			fmt.Fprintf(hc.Stderr, "git replace --graft: duplicate parent %s\n", parentHash)
			return interp.ExitStatus(1)
		}
		if _, err := repo.CommitObject(parentHash); err != nil {
			fmt.Fprintf(hc.Stderr, "git replace --graft: parent %s is not a commit: %v\n", parentHash, err)
			return interp.ExitStatus(1)
		}
		seen[parentHash] = struct{}{}
		parents = append(parents, parentHash)
	}
	refName := plumbing.ReferenceName("refs/replace/" + targetHash.String())
	if !force {
		if _, err := repo.Storer.Reference(refName); err == nil {
			fmt.Fprintf(hc.Stderr, "git replace --graft: replacement for %s already exists\n", targetHash)
			return interp.ExitStatus(1)
		}
	}
	replacement := *target
	replacement.Hash = plumbing.ZeroHash
	replacement.ParentHashes = parents
	replacement.PGPSignature = ""
	encoded := repo.Storer.NewEncodedObject()
	if err := replacement.Encode(encoded); err != nil {
		fmt.Fprintf(hc.Stderr, "git replace --graft: encode replacement commit: %v\n", err)
		return interp.ExitStatus(1)
	}
	replacementHash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git replace --graft: store replacement commit: %v\n", err)
		return interp.ExitStatus(1)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, replacementHash)); err != nil {
		fmt.Fprintf(hc.Stderr, "git replace --graft: write replacement ref: %v\n", err)
		return interp.ExitStatus(1)
	}
	return nil
}

func listGitReplacements(output, stderr io.Writer, repo *git.Repository, pattern string) error {
	iter, err := repo.Storer.IterReferences()
	if err != nil {
		fmt.Fprintf(stderr, "git replace: list refs: %v\n", err)
		return interp.ExitStatus(1)
	}
	defer iter.Close()
	prefix := "refs/replace/"
	var replacements []string
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if !strings.HasPrefix(string(ref.Name()), prefix) {
			return nil
		}
		objectName := strings.TrimPrefix(string(ref.Name()), prefix)
		if pattern != "" {
			matches, err := path.Match(pattern, objectName)
			if err != nil {
				return err
			}
			if !matches {
				return nil
			}
		}
		replacements = append(replacements, objectName)
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "git replace: list refs: %v\n", err)
		return interp.ExitStatus(1)
	}
	sort.Strings(replacements)
	for _, replacement := range replacements {
		fmt.Fprintln(output, replacement)
	}
	return nil
}

func resolveGitObject(repo *git.Repository, revision string) (plumbing.Hash, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision(revision))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("cannot resolve %q: %w", revision, err)
	}
	return *hash, nil
}

type gitNote struct {
	hash    plumbing.Hash
	content string
}

type noteTreeNode struct {
	files map[string]plumbing.Hash
	dirs  map[string]*noteTreeNode
}

func runGitNotes(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) error {
	repositoryPath := invocation.effectiveDir
	if invocation.gitDir != "" {
		repositoryPath = invocation.gitDir
	}
	repo, err := git.PlainOpenWithOptions(repositoryPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git notes: %v\n", err)
		return interp.ExitStatus(128)
	}
	fsys, _, err := newHostFS(invocation.effectiveDir)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git notes: %v\n", err)
		return interp.ExitStatus(1)
	}
	execSess, _ := execSessionFromContext(ctx)
	env, err := buildGitEnv(execSess, hc, fsys, invocation.config)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git notes: %v\n", err)
		return interp.ExitStatus(1)
	}
	if invocation.gitDir == "" && env["GIT_DIR"] != "" {
		repositoryPath = fsys.toOSPath(env["GIT_DIR"])
	}
	if invocation.gitDir != "" {
		repositoryPath = invocation.gitDir
	}
	repo, err = git.PlainOpenWithOptions(repositoryPath, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git notes: %v\n", err)
		return interp.ExitStatus(128)
	}
	args := invocation.args[1:]
	if len(args) == 0 {
		args = []string{"list"}
	}
	refName := plumbing.ReferenceName("refs/notes/commits")
	for i := 0; i < len(args); {
		spec, value, inline, matched := matchGitNotesOption(args[i], []gitNotesOptionSyntax{gitNotesRefOption})
		if matched {
			start, end := i, i+1
			if !inline {
				if end >= len(args) {
					return gitNotesOptionError(hc, spec.name)
				}
				value = args[end]
				end++
			}
			refName = plumbing.ReferenceName(value)
			args = append(args[:start], args[end:]...)
			i = start
			continue
		}
		i++
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	if !gitNotesCommandKnown(args[0]) {
		fmt.Fprintf(hc.Stderr, "git notes: unsupported subcommand %q\n", args[0])
		return interp.ExitStatus(2)
	}
	if value := env["GIT_NOTES_REF"]; value != "" && refName == "refs/notes/commits" {
		refName = plumbing.ReferenceName(value)
	}

	notes, parent, err := readGitNotes(repo, refName)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git notes: %v\n", err)
		return interp.ExitStatus(1)
	}
	switch args[0] {
	case "list":
		return listGitNotes(hc.Stdout, hc.Stderr, repo, notes, args[1:])
	case "show":
		if len(args) > 2 {
			fmt.Fprintln(hc.Stderr, "usage: git notes show [<object>]")
			return interp.ExitStatus(2)
		}
		hash, err := noteTarget(repo, args[1:])
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git notes: %v\n", err)
			return interp.ExitStatus(1)
		}
		note, ok := notes[hash]
		if !ok {
			fmt.Fprintf(hc.Stderr, "error: no note found for object %s\n", hash)
			return interp.ExitStatus(1)
		}
		_, err = io.WriteString(hc.Stdout, note.content)
		if err != nil {
			return err
		}
		return nil
	case "add", "append":
		force, message, target, err := parseGitNoteMutation(args[1:])
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git notes %s: %v\n", args[0], err)
			return interp.ExitStatus(2)
		}
		hash, err := noteTarget(repo, target)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git notes %s: %v\n", args[0], err)
			return interp.ExitStatus(1)
		}
		existing, hasNote := notes[hash]
		if args[0] == "add" && hasNote && !force {
			fmt.Fprintf(hc.Stderr, "error: note for object %s already exists\n", hash)
			return interp.ExitStatus(1)
		}
		if args[0] == "append" && hasNote {
			if existing.content != "" && !strings.HasSuffix(existing.content, "\n") {
				existing.content += "\n"
			}
			message = existing.content + message
		}
		noteHash, err := writeGitNoteBlob(repo, message)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git notes: write note: %v\n", err)
			return interp.ExitStatus(1)
		}
		notes[hash] = gitNote{hash: noteHash, content: message}
		if err := writeGitNotes(repo, refName, parent, notes, env); err != nil {
			fmt.Fprintf(hc.Stderr, "git notes: update notes ref: %v\n", err)
			return interp.ExitStatus(1)
		}
		return nil
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(hc.Stderr, "usage: git notes remove <object>...")
			return interp.ExitStatus(2)
		}
		for _, revision := range args[1:] {
			hash, err := resolveGitObject(repo, revision)
			if err != nil {
				fmt.Fprintf(hc.Stderr, "git notes remove: %v\n", err)
				return interp.ExitStatus(1)
			}
			if _, ok := notes[hash]; !ok {
				fmt.Fprintf(hc.Stderr, "error: no note found for object %s\n", hash)
				return interp.ExitStatus(1)
			}
			delete(notes, hash)
		}
		if err := writeGitNotes(repo, refName, parent, notes, env); err != nil {
			fmt.Fprintf(hc.Stderr, "git notes: update notes ref: %v\n", err)
			return interp.ExitStatus(1)
		}
		return nil
	case "prune":
		if len(args) != 1 {
			fmt.Fprintln(hc.Stderr, "usage: git notes prune")
			return interp.ExitStatus(2)
		}
		reachable, err := reachableGitObjects(repo)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git notes prune: %v\n", err)
			return interp.ExitStatus(1)
		}
		pruned := false
		for target := range notes {
			if _, ok := reachable[target]; !ok {
				delete(notes, target)
				pruned = true
			}
		}
		if pruned {
			if err := writeGitNotes(repo, refName, parent, notes, env); err != nil {
				fmt.Fprintf(hc.Stderr, "git notes prune: update notes ref: %v\n", err)
				return interp.ExitStatus(1)
			}
		}
		return nil
	default:
		return interp.ExitStatus(2)
	}
}

func gitNotesCommandKnown(name string) bool {
	for _, command := range gitNotesCommands {
		if command.name == name {
			return true
		}
	}
	return false
}

func gitNotesOptionError(hc interp.HandlerContext, option string) error {
	fmt.Fprintf(hc.Stderr, "git notes: %s requires a value\n", option)
	return interp.ExitStatus(2)
}

func reachableGitObjects(repo *git.Repository) (map[plumbing.Hash]struct{}, error) {
	iter, err := repo.References()
	if err != nil {
		return nil, fmt.Errorf("list references: %w", err)
	}
	defer iter.Close()

	reachable := make(map[plumbing.Hash]struct{})
	pending := make([]plumbing.Hash, 0)
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		resolved := ref
		if ref.Type() == plumbing.SymbolicReference {
			resolved, err = repo.Reference(ref.Name(), true)
			if err != nil {
				return fmt.Errorf("resolve reference %s: %w", ref.Name(), err)
			}
		}
		pending = append(pending, resolved.Hash())
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterate references: %w", err)
	}

	for len(pending) > 0 {
		hash := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if hash.IsZero() {
			continue
		}
		if _, seen := reachable[hash]; seen {
			continue
		}
		encoded, err := repo.Storer.EncodedObject(plumbing.AnyObject, hash)
		if err != nil {
			return nil, fmt.Errorf("read reachable object %s: %w", hash, err)
		}
		reachable[hash] = struct{}{}
		switch encoded.Type() {
		case plumbing.CommitObject:
			commit, err := repo.CommitObject(hash)
			if err != nil {
				return nil, fmt.Errorf("decode reachable commit %s: %w", hash, err)
			}
			pending = append(pending, commit.TreeHash)
			pending = append(pending, commit.ParentHashes...)
		case plumbing.TreeObject:
			tree, err := repo.TreeObject(hash)
			if err != nil {
				return nil, fmt.Errorf("decode reachable tree %s: %w", hash, err)
			}
			for _, entry := range tree.Entries {
				pending = append(pending, entry.Hash)
			}
		case plumbing.TagObject:
			tag, err := repo.TagObject(hash)
			if err != nil {
				return nil, fmt.Errorf("decode reachable tag %s: %w", hash, err)
			}
			pending = append(pending, tag.Target)
		case plumbing.BlobObject:
		default:
			return nil, fmt.Errorf("reachable object %s has unsupported type %s", hash, encoded.Type())
		}
	}
	return reachable, nil
}

func parseGitNoteMutation(args []string) (bool, string, []string, error) {
	force := false
	var message string
	var target []string
	for i := 0; i < len(args); i++ {
		spec, value, inline, matched := matchGitNotesOption(args[i], gitNoteMutationOptions)
		if !matched {
			target = append(target, args[i])
			continue
		}
		switch spec.name {
		case "-f", "--force":
			force = true
		default:
			if !inline {
				if i+1 >= len(args) {
					return false, "", nil, fmt.Errorf("%s requires a message", spec.name)
				}
				i++
				value = args[i]
			}
			message = value
		}
	}
	if message == "" {
		return false, "", nil, fmt.Errorf("a non-empty -m/--message is required")
	}
	if len(target) > 1 {
		return false, "", nil, fmt.Errorf("expected at most one object")
	}
	return force, message, target, nil
}

func noteTarget(repo *git.Repository, args []string) (plumbing.Hash, error) {
	if len(args) > 1 {
		return plumbing.ZeroHash, fmt.Errorf("expected at most one object")
	}
	revision := "HEAD"
	if len(args) == 1 {
		revision = args[0]
	}
	return resolveGitObject(repo, revision)
}

func readGitNotes(repo *git.Repository, refName plumbing.ReferenceName) (map[plumbing.Hash]gitNote, plumbing.Hash, error) {
	notes := make(map[plumbing.Hash]gitNote)
	ref, err := repo.Storer.Reference(refName)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return notes, plumbing.ZeroHash, nil
		}
		return nil, plumbing.ZeroHash, err
	}
	commitHash := ref.Hash()
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	tree, err := repo.TreeObject(commit.TreeHash)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		path, entry, err := walker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, plumbing.ZeroHash, err
		}
		if entry.Mode == filemode.Dir {
			continue
		}
		objectHash, ok := parseNotePath(path)
		if !ok {
			continue
		}
		blob, err := repo.BlobObject(entry.Hash)
		if err != nil {
			return nil, plumbing.ZeroHash, err
		}
		reader, err := blob.Reader()
		if err != nil {
			return nil, plumbing.ZeroHash, err
		}
		content, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, plumbing.ZeroHash, readErr
		}
		if closeErr != nil {
			return nil, plumbing.ZeroHash, closeErr
		}
		notes[objectHash] = gitNote{hash: entry.Hash, content: string(content)}
	}
	return notes, commitHash, nil
}

func parseNotePath(path string) (plumbing.Hash, bool) {
	path = strings.ReplaceAll(path, "/", "")
	if len(path) != len(plumbing.ZeroHash.String()) {
		return plumbing.ZeroHash, false
	}
	hash := plumbing.NewHash(path)
	return hash, !hash.IsZero() && strings.EqualFold(hash.String(), path)
}

func listGitNotes(output, stderr io.Writer, repo *git.Repository, notes map[plumbing.Hash]gitNote, args []string) error {
	var filter plumbing.Hash
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: git notes list [<object>]")
		return interp.ExitStatus(2)
	}
	if len(args) == 1 {
		hash, err := resolveGitObject(repo, args[0])
		if err != nil {
			fmt.Fprintf(stderr, "git notes list: %v\n", err)
			return interp.ExitStatus(1)
		}
		filter = hash
	}
	targets := make([]plumbing.Hash, 0, len(notes))
	for target := range notes {
		if filter.IsZero() || target == filter {
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].String() < targets[j].String() })
	for _, target := range targets {
		fmt.Fprintf(output, "%s %s\n", notes[target].hash, target)
	}
	return nil
}

func writeGitNoteBlob(repo *git.Repository, content string) (plumbing.Hash, error) {
	encoded := repo.Storer.NewEncodedObject()
	encoded.SetType(plumbing.BlobObject)
	writer, err := encoded.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := io.WriteString(writer, content); err != nil {
		_ = writer.Close()
		return plumbing.ZeroHash, err
	}
	if err := writer.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(encoded)
}

func writeGitNotes(repo *git.Repository, refName plumbing.ReferenceName, parent plumbing.Hash, notes map[plumbing.Hash]gitNote, env map[string]string) error {
	root := &noteTreeNode{files: make(map[string]plumbing.Hash), dirs: make(map[string]*noteTreeNode)}
	for target, note := range notes {
		path := target.String()
		dir, filename := path[:2], path[2:]
		child := root.dirs[dir]
		if child == nil {
			child = &noteTreeNode{files: make(map[string]plumbing.Hash), dirs: make(map[string]*noteTreeNode)}
			root.dirs[dir] = child
		}
		child.files[filename] = note.hash
	}
	treeHash, err := encodeNoteTree(repo, root)
	if err != nil {
		return err
	}
	config, err := repo.Config()
	if err != nil {
		return err
	}
	author := gitSignature(env["GIT_AUTHOR_NAME"], env["GIT_AUTHOR_EMAIL"], config.User.Name, config.User.Email)
	committer := gitSignature(env["GIT_COMMITTER_NAME"], env["GIT_COMMITTER_EMAIL"], config.User.Name, config.User.Email)
	if author.Name == "" || author.Email == "" || committer.Name == "" || committer.Email == "" {
		return fmt.Errorf("Git user.name and user.email are required to write notes")
	}
	now := time.Now()
	author.When = now
	committer.When = now
	commit := &object.Commit{
		Author:    author,
		Committer: committer,
		Message:   "Notes updated by Kabibi",
		TreeHash:  treeHash,
	}
	if !parent.IsZero() {
		commit.ParentHashes = []plumbing.Hash{parent}
	}
	encoded := repo.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		return err
	}
	commitHash, err := repo.Storer.SetEncodedObject(encoded)
	if err != nil {
		return err
	}
	return repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash))
}

func gitSignature(name, email, configName, configEmail string) object.Signature {
	if name == "" {
		name = configName
	}
	if email == "" {
		email = configEmail
	}
	if name == "" {
		name = os.Getenv("GIT_COMMITTER_NAME")
	}
	if email == "" {
		email = os.Getenv("GIT_COMMITTER_EMAIL")
	}
	return object.Signature{Name: name, Email: email}
}

func encodeNoteTree(repo *git.Repository, node *noteTreeNode) (plumbing.Hash, error) {
	entries := make([]object.TreeEntry, 0, len(node.files)+len(node.dirs))
	for filename, hash := range node.files {
		entries = append(entries, object.TreeEntry{Name: filename, Mode: filemode.Regular, Hash: hash})
	}
	for dirname, child := range node.dirs {
		hash, err := encodeNoteTree(repo, child)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: dirname, Mode: filemode.Dir, Hash: hash})
	}
	sort.Sort(object.TreeEntrySorter(entries))
	tree := &object.Tree{Entries: entries}
	encoded := repo.Storer.NewEncodedObject()
	if err := tree.Encode(encoded); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(encoded)
}
