package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"mvdan.cc/sh/moreinterp/coreutils"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// shellResultMsg contains the output of a command
type shellResultMsg struct {
	input  string
	output []string
	err    error
}

func parseCommand(input string) *syntax.File {
	return parseCommandReader(strings.NewReader(input), "")
}

func parseCommandReader(r io.Reader, name string) *syntax.File {
	parser := syntax.NewParser()
	f, err := parser.Parse(r, name)
	if err != nil {
		// Return empty file on error, the runner will handle it or we should
		return &syntax.File{}
	}
	return f
}

func createRunner(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, dir string, args []string) (*interp.Runner, error) {
	if dir == "" {
		dir, _ = os.Getwd()
	}

	opts := []interp.RunnerOption{
		interp.Dir(dir),
		interp.StdIO(stdin, stdout, stderr),
		interp.Params(args...),
	}

	r, err := interp.New(opts...)
	if err != nil {
		return nil, err
	}

	// Custom ExecHandler for recursive script execution and u-root builtins
	h := func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)

		if len(args) == 0 {
			return nil
		}

		path := args[0]
		// 1. Check if it is a script (heuristically or by looking at it)
		// We try to find it relative to current dir
		absPath := path
		if !filepath.IsAbs(path) {
			absPath = filepath.Join(hc.Dir, path)
		}

		if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
			// Check if it's a shell script (extension or shebang)
			isScript := strings.HasSuffix(path, ".sh")
			if !isScript {
				// Peek for shebang
				f, _ := os.Open(absPath)
				if f != nil {
					buf := make([]byte, 2)
					f.Read(buf)
					f.Close()
					if string(buf) == "#!" {
						isScript = true
					}
				}
			}

			if isScript {
				// RECURSION: Spawn a new runner for the script
				subArgs := args[1:]
				subRunner, err := createRunner(ctx, hc.Stdin, hc.Stdout, hc.Stderr, hc.Dir, subArgs)
				if err != nil {
					return err
				}

				f, err := os.Open(absPath)
				if err != nil {
					return err
				}
				defer f.Close()

				return subRunner.Run(ctx, parseCommandReader(f, path))
			}
		}

		// 2. Delegate to moreinterp/coreutils (u-root backed)
		coreHandler := coreutils.ExecHandler(interp.DefaultExecHandler(0))
		return coreHandler(ctx, args)
	}

	interp.ExecHandler(h)(r)
	return r, nil
}

func (m *model) runShellCommand(input string) tea.Cmd {
	return func() tea.Msg {
		parser := syntax.NewParser()
		f, err := parser.Parse(strings.NewReader(input), "")
		if err != nil {
			return shellResultMsg{
				input:  input,
				output: []string{fmt.Sprintf("Parse error: %v", err)},
			}
		}

		var sb strings.Builder
		m.shellOut.SetTarget(&sb)
		defer m.shellOut.SetTarget(nil)

		err = m.runner.Run(context.Background(), f)

		res := shellResultMsg{
			input: input,
			err:   err,
		}

		outputStr := strings.TrimSpace(sb.String())
		if outputStr != "" {
			res.output = strings.Split(outputStr, "\n")
		}

		return res
	}
}
