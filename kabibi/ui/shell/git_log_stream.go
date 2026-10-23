package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/mavity/rusticated/kabibi/gitcore"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

type streamedLogOptions struct {
	revision string
	filePath string
	limit    int
	oneline  bool
}

func runStreamedGitLog(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) (bool, error) {
	options, ok := parseStreamedLogArgs(invocation.args)
	if !ok || !streamedLogInvocationSupported(ctx, hc, invocation) {
		return false, nil
	}

	repo, err := gitcore.Open(invocation.effectiveDir)
	if err != nil {
		return false, nil
	}
	if options.revision == "" || options.revision == "HEAD" {
		if _, err := repo.LogRevision(1, options.filePath, options.revision); err != nil {
			return false, nil
		}
	}
	produce := func(output io.Writer) error {
		written := 0
		return repo.WalkLog(options.filePath, options.revision, func(commit gitcore.CommitSummary) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !options.oneline && written > 0 {
				if _, err := io.WriteString(output, "\n"); err != nil {
					return err
				}
			}
			text := formatStreamedCommit(commit, options.oneline)
			if _, err := io.WriteString(output, text); err != nil {
				return err
			}
			written++
			return nil
		}, options.limit)
	}

	execSess, _ := execSessionFromContext(ctx)
	terminal := commandTerminalFromContext(ctx)
	if terminal == nil || execSess == nil || !execSess.StreamTTY(ctx, hc).Stdout ||
		invocation.noPager || hasNoPagerOption(invocation.args) {
		return true, produce(hc.Stdout)
	}

	pagerEnabled, err := gitLogPagerEnabled(execSess, hc, invocation, repo)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git log: %v\n", err)
		return true, interp.ExitStatus(1)
	}
	if !pagerEnabled {
		return true, produce(hc.Stdout)
	}

	reader, writer := io.Pipe()
	producerDone := make(chan error, 1)
	go func() {
		producerErr := produce(writer)
		_ = writer.CloseWithError(producerErr)
		producerDone <- producerErr
	}()

	pagerErr := runPager(ctx, terminal, reader, hc.Stdout)
	_ = reader.Close()
	producerErr := <-producerDone
	if pagerErr != nil {
		fmt.Fprintf(hc.Stderr, "git log pager: %v\n", pagerErr)
		return true, interp.ExitStatus(1)
	}
	if errors.Is(producerErr, io.ErrClosedPipe) {
		return true, nil
	}
	return true, producerErr
}

func runBufferedGitLogWithPager(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) (bool, error) {
	commandIndex := gitCommandIndex(invocation.args)
	if commandIndex >= len(invocation.args) || invocation.args[commandIndex] != "log" ||
		invocation.noPager || hasNoPagerOption(invocation.args) ||
		gitInvocationHasAlternateRepo(ctx, hc, invocation) {
		return false, nil
	}
	execSess, _ := execSessionFromContext(ctx)
	terminal := commandTerminalFromContext(ctx)
	if execSess == nil || terminal == nil || !execSess.StreamTTY(ctx, hc).Stdout {
		return false, nil
	}
	repo, err := gitcore.Open(invocation.effectiveDir)
	if err != nil {
		return false, nil
	}
	pagerEnabled, err := gitLogPagerEnabled(execSess, hc, invocation, repo)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git log: %v\n", err)
		return true, interp.ExitStatus(1)
	}
	if !pagerEnabled {
		return false, nil
	}

	var output bytes.Buffer
	bufferedHC := hc
	bufferedHC.Stdout = &output
	commandErr := runGoGit(ctx, bufferedHC, invocation)
	if ctx.Err() != nil {
		return true, commandErr
	}
	if err := runPager(ctx, terminal, &output, hc.Stdout); err != nil {
		fmt.Fprintf(hc.Stderr, "git log pager: %v\n", err)
		return true, interp.ExitStatus(1)
	}
	return true, commandErr
}

func streamedLogInvocationSupported(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) bool {
	if gitInvocationHasAlternateRepo(ctx, hc, invocation) {
		return false
	}
	for _, arg := range invocation.args {
		if arg == "--no-replace-objects" {
			return false
		}
	}
	for _, override := range invocation.config {
		key, _, ok := strings.Cut(override, "=")
		if !ok {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "core.pager", "pager.log":
		default:
			return false
		}
	}
	return true
}

func gitInvocationHasAlternateRepo(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) bool {
	if invocation.gitDir != "" || invocation.workTree != "" {
		return true
	}
	env := map[string]string{}
	if execSess, _ := execSessionFromContext(ctx); execSess != nil {
		env = execSess.EnvMap()
	}
	if hc.Env != nil {
		hc.Env.Each(func(name string, value expand.Variable) bool {
			if value.IsSet() {
				env[name] = value.String()
			}
			return true
		})
	}
	return env["GIT_DIR"] != "" || env["GIT_WORK_TREE"] != ""
}

func parseStreamedLogArgs(args []string) (streamedLogOptions, bool) {
	commandIndex := gitCommandIndex(args)
	if commandIndex >= len(args) || args[commandIndex] != "log" {
		return streamedLogOptions{}, false
	}
	options := streamedLogOptions{}
	args = args[commandIndex+1:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			if i+2 != len(args) || options.filePath != "" {
				return streamedLogOptions{}, false
			}
			options.filePath = args[i+1]
			if strings.HasPrefix(options.filePath, ":") || strings.ContainsAny(options.filePath, "*?[") {
				return streamedLogOptions{}, false
			}
			i = len(args)
		case arg == "--oneline":
			options.oneline = true
		case arg == "--pretty=medium" || arg == "--format=medium":
		case arg == "--no-pager" || arg == "-P":
		case arg == "-n" || arg == "--max-count":
			i++
			if i >= len(args) {
				return streamedLogOptions{}, false
			}
			limit, err := strconv.Atoi(args[i])
			if err != nil || limit <= 0 {
				return streamedLogOptions{}, false
			}
			options.limit = limit
		case strings.HasPrefix(arg, "-n") && len(arg) > 2:
			limit, err := strconv.Atoi(arg[2:])
			if err != nil || limit <= 0 {
				return streamedLogOptions{}, false
			}
			options.limit = limit
		case strings.HasPrefix(arg, "--max-count="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--max-count="))
			if err != nil || limit <= 0 {
				return streamedLogOptions{}, false
			}
			options.limit = limit
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			limit, err := strconv.Atoi(arg[1:])
			if err != nil || limit <= 0 {
				return streamedLogOptions{}, false
			}
			options.limit = limit
		default:
			if options.revision != "" {
				return streamedLogOptions{}, false
			}
			if strings.Contains(arg, "..") {
				return streamedLogOptions{}, false
			}
			options.revision = arg
		}
	}
	return options, true
}

func gitCommandIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			i++
		case "--no-pager", "-P", "--no-replace-objects", "--bare":
		default:
			return i
		}
	}
	return len(args)
}

func hasNoPagerOption(args []string) bool {
	for _, arg := range args {
		if arg == "--no-pager" || arg == "-P" {
			return true
		}
	}
	return false
}

func formatStreamedCommit(commit gitcore.CommitSummary, oneline bool) string {
	if oneline {
		hash := commit.Hash
		if len(hash) > 7 {
			hash = hash[:7]
		}
		return hash + " " + commit.Message + "\n"
	}

	var output strings.Builder
	fmt.Fprintf(&output, "commit %s\n", commit.Hash)
	if len(commit.Parents) > 1 {
		output.WriteString("Merge:")
		for _, parent := range commit.Parents {
			if len(parent) > 7 {
				parent = parent[:7]
			}
			fmt.Fprintf(&output, " %s", parent)
		}
		output.WriteByte('\n')
	}
	fmt.Fprintf(&output, "Author: %s <%s>\nDate:   %s\n\n",
		commit.AuthorName, commit.AuthorEmail, commit.Date.Format("Mon Jan 2 15:04:05 2006 -0700"))
	for _, line := range strings.Split(strings.TrimRight(commit.FullMessage, "\n"), "\n") {
		output.WriteString("    ")
		output.WriteString(line)
		output.WriteByte('\n')
	}
	return output.String()
}

func gitLogPagerEnabled(execSess *ExecutionSession, hc interp.HandlerContext, invocation gitInvocation, repo *gitcore.Repository) (bool, error) {
	env := execSess.EnvMap()
	if hc.Env != nil {
		hc.Env.Each(func(name string, value expand.Variable) bool {
			if value.IsSet() {
				env[name] = value.String()
			}
			return true
		})
	}
	if pager, ok := env["GIT_PAGER"]; ok {
		return !isPassThroughPager(pager), nil
	}
	for i := 0; i < len(invocation.config); i++ {
		key, value, ok := strings.Cut(invocation.config[i], "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "pager.log") &&
			strings.EqualFold(strings.TrimSpace(value), "false") {
			return false, nil
		}
	}
	for i := 0; i < len(invocation.config); i++ {
		key, value, ok := strings.Cut(invocation.config[i], "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "core.pager") {
			return !isPassThroughPager(value), nil
		}
	}
	pagerLog, found, err := repo.ConfigValue("pager", "log")
	if err != nil {
		return false, err
	}
	if found && strings.EqualFold(strings.TrimSpace(pagerLog), "false") {
		return false, nil
	}
	pager, found, err := repo.ConfigValue("core", "pager")
	if err != nil {
		return false, err
	}
	if found {
		return !isPassThroughPager(pager), nil
	}
	if pager, ok := env["PAGER"]; ok {
		return !isPassThroughPager(pager), nil
	}
	return true, nil
}

func isPassThroughPager(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return true
	}
	fields := strings.Fields(command)
	if len(fields) == 1 && fields[0] == ":" {
		return true
	}
	if !strings.EqualFold(fields[0], "cat") {
		return false
	}
	for _, arg := range fields[1:] {
		if arg != "-" {
			return false
		}
	}
	return true
}
