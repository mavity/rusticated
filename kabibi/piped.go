package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

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
