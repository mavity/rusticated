package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mavity/rusticated/kabibi/browser"
	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/slashcmd"
	"github.com/mavity/rusticated/kabibi/ui/filemanager"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"mvdan.cc/sh/v3/syntax"
)

func runPipedSession(stdin io.Reader, stdout, stderr io.Writer, opts cliOptions) int {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	es, err := shell.NewExecutionSession("")
	if err != nil {
		fmt.Fprintf(stderr, "kabibi: session init: %v\n", err)
		return 1
	}

	aiRunner := shell.NewLazyAIRunner(es)
	aiRunner.SetVerbose(opts.verbose)
	defer func() {
		aiRunner.FlushEmbeddings()
		aiRunner.Close()
	}()

	return runPipedReader(context.Background(), stdin, stdout, stderr, opts, es, aiRunner)
}

func runPipedReader(
	ctx context.Context,
	stdin io.Reader,
	stdout, stderr io.Writer,
	opts cliOptions,
	es *shell.ExecutionSession,
	aiRunner shell.AIRunner,
) int {
	reader := bufio.NewReader(stdin)
	var pending strings.Builder
	lastExitCode := 0

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			pending.WriteString(line)
		}
		if pending.Len() > 0 {
			input := pending.String()
			if strings.HasSuffix(input, "\n") || readErr == io.EOF {
				token, _, isSlash := slashcmd.InputToken(input)
				if isSlash && strings.Count(token, "/") == 1 {
					pending.Reset()
					lastExitCode = runPipedSlash(ctx, input, reader, stdout, stderr, es)
					if readErr == io.EOF {
						return lastExitCode
					}
					continue
				}
			}
			route, parsed := es.ClassifyInput(input)

			if route != shell.RouteIncomplete || readErr == io.EOF {
				switch route {
				case shell.RouteIncomplete:
					fmt.Fprintln(stderr, "kabibi: incomplete shell input at end of stdin")
					return 2
				case shell.RouteEmpty:
					pending.Reset()
				case shell.RouteBuiltin:
					return lastExitCode
				case shell.RouteShell:
					pending.Reset()
					lastExitCode = runPipedShell(ctx, input, parsed, stdout, stderr, opts.verbose, es, aiRunner)
				case shell.RouteAI:
					pending.Reset()
					lastExitCode = runPipedAI(ctx, aiRunner, strings.TrimSpace(input), stdout, stderr, opts.verbose, es)
				}
			}
		}

		if readErr == io.EOF {
			return lastExitCode
		}
		if readErr != nil {
			fmt.Fprintf(stderr, "kabibi: reading stdin: %v\n", readErr)
			return 1
		}
	}
}

func runPipedSlash(
	ctx context.Context,
	line string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	es *shell.ExecutionSession,
) int {
	invocation, recognized, err := slashcmd.Parse(line)
	if !recognized {
		return 0
	}
	if err != nil {
		return pipedSlashError(stderr, err)
	}
	command, ok := slashcmd.Lookup(invocation.Name)
	if !ok {
		return pipedSlashError(stderr, fmt.Errorf("unknown slash command %q", invocation.Name))
	}
	if command.BrowserOnly && !browser.Available() {
		return pipedSlashError(stderr, fmt.Errorf("%s is only available in the browser", command.Name))
	}

	name := command.Name
	if name == "/remove" {
		name = "/delete"
	}
	switch name {
	case "/settings":
		if len(invocation.Args) != 0 {
			return pipedSlashError(stderr, slashcmd.UsageError(name, ""))
		}
		path, pathErr := config.AIConfigPath()
		if pathErr != nil {
			return pipedSlashError(stderr, fmt.Errorf("/settings requires the TUI; unable to resolve the AI configuration path: %w", pathErr))
		}
		return pipedSlashError(stderr, fmt.Errorf("/settings requires the TUI; edit AI settings at %s", path))
	case "/model":
		if len(invocation.Args) != 0 {
			return pipedSlashError(stderr, slashcmd.UsageError(name, ""))
		}
		if err := shell.RunAISetup(ctx, es, stdin, stdout); err != nil {
			return pipedSlashError(stderr, fmt.Errorf("/model: %w", err))
		}
		return 0
	case "/panels", "/edit":
		if len(invocation.Args) != 0 {
			return pipedSlashError(stderr, slashcmd.UsageError(name, ""))
		}
		return pipedSlashError(stderr, fmt.Errorf("%s requires the interactive TUI", name))
	case "/download":
		if len(invocation.Args) != 0 {
			return pipedSlashError(stderr, slashcmd.UsageError(name, ""))
		}
		if err := browser.Download(); err != nil {
			return pipedSlashError(stderr, err)
		}
		return 0
	case "/copy", "/move":
		if len(invocation.Args) < 2 {
			return pipedSlashError(stderr, slashcmd.UsageError(name, " <source>... <destination-directory>"))
		}
		sources := make([]string, len(invocation.Args)-1)
		for i, path := range invocation.Args[:len(invocation.Args)-1] {
			sources[i] = pipedSlashPath(es.Dir(), path)
		}
		op := filemanager.FileOp{
			Kind:        filemanager.OpCopy,
			Sources:     sources,
			Dest:        pipedSlashPath(es.Dir(), invocation.Args[len(invocation.Args)-1]),
			NoOverwrite: true,
		}
		if name == "/move" {
			op.Kind = filemanager.OpMove
		}
		if err := filemanager.RunFileOp(ctx, op, nil, nil, nil); err != nil {
			return pipedSlashError(stderr, err)
		}
		return 0
	case "/delete":
		if len(invocation.Args) < 2 || invocation.Args[0] != "--yes" {
			return pipedSlashError(stderr, slashcmd.UsageError(name, " --yes <path>..."))
		}
		sources := make([]string, len(invocation.Args)-1)
		for i, path := range invocation.Args[1:] {
			sources[i] = pipedSlashPath(es.Dir(), path)
		}
		if err := filemanager.RunFileOp(ctx, filemanager.FileOp{
			Kind:        filemanager.OpDelete,
			Sources:     sources,
			NoOverwrite: true,
		}, nil, nil, nil); err != nil {
			return pipedSlashError(stderr, err)
		}
		return 0
	case "/rename":
		if len(invocation.Args) != 2 {
			return pipedSlashError(stderr, slashcmd.UsageError(name, " <source> <destination>"))
		}
		source := pipedSlashPath(es.Dir(), invocation.Args[0])
		dest := pipedSlashPath(es.Dir(), invocation.Args[1])
		if _, err := os.Lstat(dest); err == nil {
			return pipedSlashError(stderr, fmt.Errorf("destination already exists: %s", dest))
		} else if !os.IsNotExist(err) {
			return pipedSlashError(stderr, fmt.Errorf("check rename destination %s: %w", dest, err))
		}
		if err := os.Rename(source, dest); err != nil {
			return pipedSlashError(stderr, fmt.Errorf("rename %s to %s: %w", source, dest, err))
		}
		return 0
	default:
		return pipedSlashError(stderr, fmt.Errorf("unsupported slash command %q", name))
	}
}

func pipedSlashPath(cwd, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

func pipedSlashError(stderr io.Writer, err error) int {
	if writeErr := slashcmd.WriteError(stderr, err); writeErr != nil {
		return 1
	}
	return 1
}

func runPipedShell(
	ctx context.Context,
	input string,
	parsed *syntax.File,
	stdout, stderr io.Writer,
	verbose bool,
	es *shell.ExecutionSession,
	aiRunner shell.AIRunner,
) int {
	var writeErr error
	rec, runErr := es.RunCommand(ctx, input, parsed, shell.SourceUserShell, func(chunk []byte) {
		if writeErr == nil {
			var n int
			n, writeErr = stdout.Write(chunk)
			if writeErr == nil && n != len(chunk) {
				writeErr = io.ErrShortWrite
			}
		}
	})
	if writeErr != nil {
		fmt.Fprintf(stderr, "kabibi: writing stdout: %v\n", writeErr)
		return 1
	}
	if errors.Is(runErr, shell.ErrUnknownCommandAtStart) {
		return runPipedAI(ctx, aiRunner, input, stdout, stderr, verbose, es)
	}
	if runErr != nil {
		if rec.ExitCode != 0 {
			return rec.ExitCode
		}
		return 1
	}
	return rec.ExitCode
}

func runPipedAI(
	ctx context.Context,
	aiRunner shell.AIRunner,
	prompt string,
	stdout, stderr io.Writer,
	verbose bool,
	es *shell.ExecutionSession,
) int {
	var output strings.Builder
	var outputErr error
	var feedbackErr error
	var endedWithNewline bool
	var mu sync.Mutex
	startedAt := time.Now()
	err := aiRunner.RunTurn(
		ctx,
		prompt,
		func(feedback string) {
			if !verbose || feedback == "" {
				return
			}
			line := feedback
			if index := strings.LastIndexByte(line, '\r'); index >= 0 {
				line = line[index+1:]
			}
			line = strings.TrimSpace(line)
			if line != "" {
				mu.Lock()
				if feedbackErr == nil {
					_, feedbackErr = fmt.Fprintf(stderr, "[ai] %s\n", line)
				}
				mu.Unlock()
			}
		},
		func(content string) {
			if content == "" {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			output.WriteString(content)
			endedWithNewline = strings.HasSuffix(content, "\n")
			if outputErr == nil {
				var n int
				n, outputErr = io.WriteString(stdout, content)
				if outputErr == nil && n != len(content) {
					outputErr = io.ErrShortWrite
				}
			}
		},
	)
	mu.Lock()
	outputText := output.String()
	if outputErr == nil && output.Len() > 0 && !endedWithNewline {
		_, outputErr = io.WriteString(stdout, "\n")
		if outputErr == nil {
			output.WriteByte('\n')
			outputText = output.String()
		}
	}
	mu.Unlock()

	exitCode := 0
	if err != nil {
		exitCode = 1
		fmt.Fprintf(stderr, "kabibi: %v\n", err)
	}
	if outputErr != nil {
		fmt.Fprintf(stderr, "kabibi: writing stdout: %v\n", outputErr)
		exitCode = 1
	}
	if feedbackErr != nil {
		fmt.Fprintf(stderr, "kabibi: writing stderr: %v\n", feedbackErr)
		exitCode = 1
	}
	es.RecordBlock(shell.BlockAITurn, shell.SourceUserShell, prompt, outputText, exitCode, startedAt)
	if exitCode == 0 {
		es.MarkAITurnCompleted()
	}
	return exitCode
}
