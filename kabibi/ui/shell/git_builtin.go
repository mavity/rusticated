package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrianliechti/go-git"
	"github.com/mavity/rusticated/kabibi/gitcore"
	"mvdan.cc/sh/v3/interp"
)

func gitBuiltinMiddleware(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 || args[0] != "git" {
			return next(ctx, args)
		}
		return runGitBuiltin(ctx, args[1:])
	}
}

func runGitBuiltin(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	if len(args) == 0 {
		fmt.Fprintln(hc.Stderr, "usage: git --version | status [-s|--short|--porcelain] | add [-A|--all] <path...> | commit [-a|--all] -m <message> [--amend]")
		return interp.ExitStatus(2)
	}
	switch args[0] {
	case "--help", "-h":
		if len(args) != 1 {
			fmt.Fprintf(hc.Stderr, "git --help: unsupported argument %q\n", args[1])
			return interp.ExitStatus(2)
		}
		writeGitHelp(hc)
		return nil
	case "--version":
		if len(args) != 1 {
			fmt.Fprintf(hc.Stderr, "git --version: unsupported argument %q\n", args[1])
			return interp.ExitStatus(2)
		}
		version, err := gitcore.LibraryVersion()
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git --version: %v\n", err)
			return interp.ExitStatus(1)
		}
		fmt.Fprintf(hc.Stdout, "git version %s (go-git)\n", version)
		return nil
	case "status":
		return runGitStatus(hc, args[1:])
	case "add":
		return runGitAdd(hc, args[1:])
	case "commit":
		return runGitCommit(ctx, hc, args[1:])
	default:
		return runGoGit(ctx, hc, args)
	}
}

func runGoGit(ctx context.Context, hc interp.HandlerContext, args []string) error {
	repo, err := gitcore.Open(hc.Dir)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git %s: %v\n", args[0], err)
		return interp.ExitStatus(128)
	}

	repoRoot := repo.Root()
	relDir, err := filepath.Rel(repoRoot, hc.Dir)
	if err != nil {
		relDir = "."
	}
	relDirSlash := "/" + filepath.ToSlash(relDir)
	if relDir == "." || relDir == "" {
		relDirSlash = "/"
	}

	execSess, _ := execSessionFromContext(ctx)
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if execSess != nil {
		for k, v := range execSess.EnvMap() {
			env[k] = v
		}
	}

	code, runErr := git.Run(ctx, git.Options{
		Args:   args,
		Dir:    relDirSlash,
		Env:    env,
		Stdin:  hc.Stdin,
		Stdout: hc.Stdout,
		Stderr: hc.Stderr,
		FS:     newOSFS(repoRoot),
	})
	if runErr != nil {
		return runErr
	}
	if code != 0 {
		return interp.ExitStatus(uint8(code))
	}
	return nil
}

func writeGitHelp(hc interp.HandlerContext) {
	fmt.Fprintln(hc.Stdout, `Kabibi built-in Git

Usage:
  git --version
  git --help
  git status [-s|--short|--porcelain]
  git add [-A|--all] <path...>
  git commit [-a|--all] -m <message> [--amend]
  git diff [--cached|--staged] [--stat] [<commit>...] [-- <path...>]
  git log [-n <N>] [--oneline] [--stat] [<revision>...] [-- <path...>]
  git branch [-a] [-d <name>] [<name>]
  git checkout [-b <name>] <branch|commit|path...>
  git switch [-c <name>] <branch>
  git restore [--staged] <path...>
  git reset [--soft|--mixed|--hard] [<commit>]
  git merge <branch>
  git stash [push|pop|list|drop]
  git tag [-d <name>] [<name>]
  git show [<object>]

Commands:
  --version               Show the go-git library version
  status                  Show repository status
  add                     Stage the specified paths
  commit                  Create a commit from staged changes
  diff                    Show changes between commits, commit and working tree, etc
  log                     Show commit logs
  branch                  List, create, or delete branches
  checkout                Switch branches or restore working tree files
  switch                  Switch branches
  restore                 Restore working tree files
  reset                   Reset current HEAD to the specified state
  merge                   Join two or more development histories together
  stash                   Stash changes in a dirty working directory
  tag                     Create, list, or delete tags
  show                    Show various types of objects

Unsupported commands and options return an error; host Git is not invoked.`)
}

func runGitStatus(hc interp.HandlerContext, args []string) error {
	short := false
	for _, arg := range args {
		switch arg {
		case "-s", "--short", "--porcelain":
			short = true
		default:
			fmt.Fprintf(hc.Stderr, "git status: unsupported option %q\n", arg)
			return interp.ExitStatus(2)
		}
	}

	repo, err := gitcore.Open(hc.Dir)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git status: %v\n", err)
		return interp.ExitStatus(128)
	}
	status, err := repo.Status()
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git status: %v\n", err)
		return interp.ExitStatus(128)
	}

	if short {
		for _, entry := range status {
			fmt.Fprintf(hc.Stdout, "%c%c %s\n", entry.IndexStatus, entry.WorktreeStatus, entry.Path)
		}
		return nil
	}
	branch, err := repo.CurrentBranch()
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git status: %v\n", err)
		return interp.ExitStatus(128)
	}
	fmt.Fprintf(hc.Stdout, "On branch %s\n", branch)
	if len(status) == 0 {
		fmt.Fprintln(hc.Stdout, "\nnothing to commit, working tree clean")
		return nil
	}
	fmt.Fprintln(hc.Stdout, "\nChanges (index/worktree):")
	for _, entry := range status {
		fmt.Fprintf(hc.Stdout, "  %c%c %s\n", entry.IndexStatus, entry.WorktreeStatus, entry.Path)
	}
	return nil
}

func runGitAdd(hc interp.HandlerContext, args []string) error {
	all := false
	var paths []string
	for _, arg := range args {
		switch arg {
		case "-A", "--all":
			all = true
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(hc.Stderr, "git add: unsupported option %q\n", arg)
				return interp.ExitStatus(2)
			}
			paths = append(paths, arg)
		}
	}
	repo, err := gitcore.Open(hc.Dir)
	if err == nil {
		err = repo.StageFrom(hc.Dir, paths, all)
	}
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git add: %v\n", err)
		if errors.Is(err, gitcore.ErrRepositoryNotFound) || errors.Is(err, gitcore.ErrBareRepository) {
			return interp.ExitStatus(128)
		}
		return interp.ExitStatus(1)
	}
	return nil
}

func runGitCommit(ctx context.Context, hc interp.HandlerContext, args []string) error {
	var (
		all     bool
		amend   bool
		message string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-a" || arg == "--all":
			all = true
		case arg == "--amend":
			amend = true
		case arg == "-m" || arg == "--message":
			i++
			if i == len(args) {
				fmt.Fprintln(hc.Stderr, "git commit: -m requires a message")
				return interp.ExitStatus(2)
			}
			message = args[i]
		case strings.HasPrefix(arg, "--message="):
			message = strings.TrimPrefix(arg, "--message=")
		case strings.HasPrefix(arg, "-m") && len(arg) > 2:
			message = strings.TrimPrefix(arg, "-m")
		case arg == "-am":
			all = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				message = args[i]
			}
		default:
			fmt.Fprintf(hc.Stderr, "git commit: unsupported option %q\n", arg)
			return interp.ExitStatus(2)
		}
	}
	if strings.TrimSpace(message) == "" {
		fmt.Fprintln(hc.Stderr, "git commit: -m requires a non-empty message")
		return interp.ExitStatus(2)
	}
	repo, err := gitcore.Open(hc.Dir)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git commit: %v\n", err)
		return interp.ExitStatus(128)
	}
	execSess, _ := execSessionFromContext(ctx)
	env := map[string]string{}
	if execSess != nil {
		env = execSess.EnvMap()
	}
	hash, err := repo.Commit(message, all, amend, env)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git commit: %v\n", err)
		if errors.Is(err, gitcore.ErrBareRepository) {
			return interp.ExitStatus(128)
		}
		return interp.ExitStatus(1)
	}
	branch, branchErr := repo.CurrentBranch()
	if branchErr != nil {
		fmt.Fprintf(hc.Stderr, "git commit: committed %s but could not read branch: %v\n", hash, branchErr)
		return interp.ExitStatus(1)
	}
	fmt.Fprintf(hc.Stdout, "[%s %.10s] %s\n", branch, hash, strings.SplitN(message, "\n", 2)[0])
	return nil
}
