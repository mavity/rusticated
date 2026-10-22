package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func runGitSubShell(
	ctx context.Context,
	dir string,
	env map[string]string,
	cmdStr string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	envPairs := make([]string, 0, len(env))
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		envPairs = append(envPairs, name+"="+env[name])
	}
	parentEnv := expand.ListEnviron(envPairs...)
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	runner, err := createRunnerWithEnv(ctx, stdin, stdout, stderr, dir, parentEnv, nil)
	if err != nil {
		return 1, err
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(cmdStr), "git-shell-command")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "git shell command parse error: %v\n", err)
		return 2, nil
	}
	if err := runner.Run(withoutTopLevelStart(ctx), file); err == nil {
		return 0, nil
	} else if status, ok := interp.IsExitStatus(err); ok {
		return int(status), nil
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 1, err
	} else {
		return 1, err
	}
}

func runGitBisectRun(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) error {
	if len(invocation.args) < 3 {
		fmt.Fprintln(hc.Stderr, "usage: git bisect run <command> [<arg>...]")
		return interp.ExitStatus(2)
	}
	gitDir := invocation.gitDir
	if gitDir == "" {
		resolvedGitDir, err := resolveGitDir(invocation.effectiveDir)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git bisect run: %v\n", err)
			return interp.ExitStatus(1)
		}
		gitDir = resolvedGitDir
	}
	if _, err := os.Stat(filepath.Join(gitDir, "BISECT_START")); err != nil {
		fmt.Fprintln(hc.Stderr, "You need to start by \"git bisect start\"")
		return interp.ExitStatus(1)
	}

	cmdLine := joinShellArguments(invocation.args[2:])
	execSess, _ := execSessionFromContext(ctx)
	fsys, _, err := newHostFS(invocation.effectiveDir)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git bisect run: %v\n", err)
		return interp.ExitStatus(1)
	}
	env, err := buildGitEnv(execSess, hc, fsys, invocation.config)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git bisect run: %v\n", err)
		return interp.ExitStatus(1)
	}
	if home := env["HOME"]; home != "" {
		env["HOME"] = fsys.toOSPath(home)
	}
	if invocation.gitDir != "" {
		env["GIT_DIR"] = invocation.gitDir
	}
	if invocation.workTree != "" {
		env["GIT_WORK_TREE"] = invocation.workTree
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hc.Stdout, "running %s\n", cmdLine)
		code, runErr := runGitSubShell(ctx, invocation.effectiveDir, env, cmdLine, hc.Stdin, hc.Stdout, hc.Stderr)
		if runErr != nil {
			return runErr
		}
		if code < 0 || code >= 128 {
			return interp.ExitStatus(1)
		}

		before, err := readGitHead(ctx, hc, invocation)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git bisect run: %v\n", err)
			return interp.ExitStatus(1)
		}
		stepAction := bisectTerm(gitDir, code)
		var output bytes.Buffer
		stepInvocation := invocation
		stepInvocation.args = []string{"bisect", stepAction}
		stepHC := hc
		stepHC.Stdout = io.MultiWriter(hc.Stdout, &output)
		stepErr := runGoGit(ctx, stepHC, stepInvocation)
		if stepErr != nil {
			return stepErr
		}
		result := output.String()
		if strings.Contains(result, " is the first ") && strings.Contains(result, " commit\n") {
			fmt.Fprintln(hc.Stdout, "bisect run success")
			return nil
		}
		after, err := readGitHead(ctx, hc, invocation)
		if err != nil || after == before {
			fmt.Fprintln(hc.Stderr, "git bisect run: bisect did not advance to another commit")
			return interp.ExitStatus(1)
		}
	}
}

func joinShellArguments(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return strings.Join(quoted, " ")
}

func readGitHead(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) (string, error) {
	var output bytes.Buffer
	headInvocation := invocation
	headInvocation.args = []string{"rev-parse", "HEAD"}
	headHC := hc
	headHC.Stdout = &output
	if err := runGoGit(ctx, headHC, headInvocation); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func resolveGitDir(workTree string) (string, error) {
	gitPath := filepath.Join(workTree, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	if info.IsDir() {
		return gitPath, nil
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", fmt.Errorf("read worktree .git file: %w", err)
	}
	line := strings.TrimSpace(string(data))
	gitDir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", fmt.Errorf("invalid worktree .git file")
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workTree, gitDir)
	}
	return filepath.Clean(gitDir), nil
}

func bisectTerm(gitDir string, code int) string {
	switch code {
	case 125:
		return "skip"
	case 0:
		return readBisectTerm(gitDir, 1, "good")
	default:
		return readBisectTerm(gitDir, 0, "bad")
	}
}

func readBisectTerm(gitDir string, index int, fallback string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "BISECT_TERMS"))
	if err != nil {
		return fallback
	}
	fields := strings.Fields(string(data))
	if index >= len(fields) || fields[index] == "" {
		return fallback
	}
	return fields[index]
}
