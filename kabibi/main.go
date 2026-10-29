package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/mavity/rusticated/kabibi/ui/app"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"github.com/spf13/pflag"
)

type cliOptions struct {
	command string
	prompt  string
	edit    string
	verbose bool
	help    bool

	positional []string
	aiPrompt   string
	aiFallback bool
}

const cliHelp = `Kabibi - terminal shell and developer assistant

Usage:
  kabibi [flags]
  kabibi help
  kabibi edit <file>
  kabibi <prompt words...>

Flags:
  -c, --command string   Execute a command or AI prompt in batch mode and exit
  -p, --prompt string    Execute a prompt in one-shot mode and exit
      --edit string      Open a file in the editor, save on exit, and close Kabibi
  -v, --verbose          Verbose diagnostics (includes prompts and tool I/O)
  -h, --help             Show this help

A single positional file path runs that file as a shell script. Other positional
arguments are joined with spaces and sent directly to the AI as a one-shot prompt.
For that prompt, Kabibi also includes this help text in the system context.

With no command-line input, Kabibi starts its TUI when stdin is a terminal.
When stdin is redirected, it reads shell commands and AI prompts incrementally
from stdin; an incomplete shell construct continues across lines.
`

func printCLIHelp(w io.Writer) {
	_, _ = io.WriteString(w, cliHelp)
}

func parseCLI(args []string, helpOut io.Writer) (cliOptions, error) {
	var opts cliOptions
	if len(args) == 1 && args[0] == "help" {
		opts.help = true
		printCLIHelp(helpOut)
		return opts, nil
	}

	fs := pflag.NewFlagSet("kabibi", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() { printCLIHelp(helpOut) }
	fs.StringVarP(&opts.command, "command", "c", "", "Execute a command or AI prompt in batch mode and exit")
	fs.StringVarP(&opts.prompt, "prompt", "p", "", "Execute a prompt in one-shot mode and exit")
	fs.StringVar(&opts.edit, "edit", "", "Open a file in the editor, save on exit, and close Kabibi")
	fs.BoolVarP(&opts.verbose, "verbose", "v", false, "Verbose diagnostics, including prompts and tool I/O")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			opts.help = true
			return opts, nil
		}
		opts.aiPrompt = strings.Join(args, " ")
		opts.aiFallback = true
		return opts, nil
	}

	opts.positional = fs.Args()
	if opts.edit == "" && len(opts.positional) == 2 && opts.positional[0] == "edit" {
		opts.edit = opts.positional[1]
		opts.positional = nil
	}
	if opts.edit == "" && len(opts.positional) == 1 && opts.positional[0] == "help" {
		opts.help = true
		printCLIHelp(helpOut)
	}
	if opts.command == "" && opts.prompt == "" && opts.edit == "" && !opts.help && len(opts.positional) > 1 {
		opts.aiPrompt = strings.Join(opts.positional, " ")
		opts.aiFallback = true
	}
	return opts, nil
}

func main() {
	verbose := os.Getenv("MOHABBAT_VERBOSE") == "1"
	for _, arg := range os.Args[1:] {
		if arg == "-v" || arg == "--verbose" {
			verbose = true
		}
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[kabibi] main entry: argc=%d\n", len(os.Args))
		for i, arg := range os.Args[1:] {
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(os.Stderr, "[kabibi] argv[%d]=%q\n", i+1, arg)
			} else {
				fmt.Fprintf(os.Stderr, "[kabibi] argv[%d]=<redacted value, %d bytes>\n", i+1, len(arg))
			}
		}
	}
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr, verbose))
}

func runCLI(args []string, stdout, stderr io.Writer, verbose bool) int {
	if verbose {
		fmt.Fprintf(stderr, "[kabibi] CLI parse begin: argc=%d\n", len(args))
	}
	opts, err := parseCLI(args, stdout)
	if err != nil {
		if verbose {
			fmt.Fprintf(stderr, "[kabibi] CLI parse failed: %v\n", err)
		}
		fmt.Fprintf(stderr, "kabibi: %v\n", err)
		return 2
	}
	verbose = verbose || opts.verbose || os.Getenv("MOHABBAT_VERBOSE") == "1"
	mode := "interactive"
	switch {
	case opts.help:
		mode = "help"
	case opts.edit != "":
		mode = "edit"
	case opts.command != "":
		mode = "command"
	case opts.prompt != "":
		mode = "prompt"
	case opts.aiFallback || len(opts.positional) == 1:
		mode = "one-shot"
	}
	if verbose {
		fmt.Fprintf(stderr, "[kabibi] CLI parsed: mode=%s verbose=%t positional_count=%d\n", mode, opts.verbose, len(opts.positional))
	}
	if opts.help {
		return 0
	}

	if opts.edit != "" {
		if verbose {
			fmt.Fprintln(stderr, "[kabibi] booting app for editor mode")
		}
		appWidget, host, err := app.Boot(verbose)
		if err != nil {
			if verbose {
				fmt.Fprintf(stderr, "[kabibi] editor app boot failed: %v\n", err)
			}
			fmt.Fprintf(stderr, "kabibi boot error: %v\n", err)
			return 1
		}
		defer appWidget.Close()
		appWidget.SetExitHandler(func() { host.Stop() })
		if err := appWidget.OpenEditor(opts.edit, true); err != nil {
			if verbose {
				fmt.Fprintf(stderr, "[kabibi] opening editor failed: %v\n", err)
			}
			fmt.Fprintf(stderr, "kabibi: %v\n", err)
			return 1
		}
		if err := host.Run(); err != nil {
			if verbose {
				fmt.Fprintf(stderr, "[kabibi] editor host run failed: %v\n", err)
			}
			fmt.Fprintf(stderr, "kabibi run error: %v\n", err)
			return 1
		}
		return 0
	}

	oneShotInput := opts.command
	if oneShotInput == "" {
		oneShotInput = opts.prompt
	}
	if oneShotInput != "" {
		return runOneShotLine(oneShotInput, opts.verbose, stdout, stderr)
	}

	if opts.aiFallback {
		return runOneShotAIPrompt(opts.aiPrompt, opts.verbose, stdout, stderr)
	}

	if len(opts.positional) == 1 {
		info, err := os.Stat(opts.positional[0])
		if err == nil && !info.IsDir() {
			data, readErr := os.ReadFile(opts.positional[0])
			if readErr != nil {
				fmt.Fprintf(stderr, "kabibi: %v\n", readErr)
				return 1
			}
			return runOneShotLine(string(data), opts.verbose, stdout, stderr)
		}
		return runOneShotAIPrompt(strings.Join(opts.positional, " "), opts.verbose, stdout, stderr)
	}

	if !term.IsTerminal(os.Stdin.Fd()) {
		opts.verbose = verbose
		return runPipedSession(os.Stdin, stdout, stderr, opts)
	}

	appWidget, host, err := app.Boot(verbose)
	if err != nil {
		if verbose {
			fmt.Fprintf(stderr, "[kabibi] interactive app boot failed: %v\n", err)
		}
		log.Printf("kabibi boot error: %v", err)
		return 1
	}
	if verbose {
		fmt.Fprintln(stderr, "[kabibi] interactive app boot succeeded; entering host.Run")
	}
	defer appWidget.Close()

	if err := host.Run(); err != nil {
		if verbose {
			fmt.Fprintf(stderr, "[kabibi] interactive host.Run failed: %v\n", err)
		}
		log.Printf("kabibi run error: %v", err)
		return 1
	}
	if verbose {
		fmt.Fprintln(stderr, "[kabibi] interactive host.Run returned normally")
	}
	return 0
}

func runOneShotAIPrompt(prompt string, verbose bool, stdout, stderr io.Writer) int {
	exitCode, err := shell.ExecuteOneShotPrompt(
		context.Background(),
		prompt,
		cliHelp,
		verbose,
		stdout,
	)
	if err != nil {
		fmt.Fprintf(stderr, "kabibi: %v\n", err)
		if exitCode == 0 {
			return 1
		}
	}
	return exitCode
}

func runOneShotLine(line string, verbose bool, stdout, stderr io.Writer) int {
	exitCode, err := shell.ExecuteOneShotLine(context.Background(), line, verbose, stdout)
	if err != nil {
		if exitCode == 0 {
			exitCode = 1
		}
		fmt.Fprintf(stderr, "kabibi: %v\n", err)
	}
	return exitCode
}
