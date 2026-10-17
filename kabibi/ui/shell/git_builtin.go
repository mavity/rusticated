package shell

import (
	"context"
	"fmt"

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
		fmt.Fprintln(hc.Stderr, "usage: git status [-s|--short|--porcelain]")
		return interp.ExitStatus(2)
	}
	if args[0] != "status" {
		fmt.Fprintf(hc.Stderr, "git: unsupported subcommand %q\n", args[0])
		return interp.ExitStatus(2)
	}

	short := false
	for _, arg := range args[1:] {
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
