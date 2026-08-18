package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

var AppProgram *tea.Program

func main() {
	helpPtr := flag.Bool("help", false, "Show help")
	commandPtr := flag.String("c", "", "Run a single command string")
	promptPtr := flag.String("p", "", "Run an AI prompt to stdout")
	flag.StringVar(promptPtr, "prompt", "", "Run an AI prompt to stdout (alias for -p)")

	// Filter os.Args to remove elements that might be interpreted as flags but are actually metadata/setup
	// Washmhost/Mohabbat dev-run usually passes [wasm_path -- [args...]]
	// We want to skip everything until the first '--' OR skip known wrappers.
	filteredArgs := []string{os.Args[0]}
	foundSeparator := false
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--" && !foundSeparator {
			foundSeparator = true
			continue
		}
		if !foundSeparator {
			// Skip metadata if we haven't found the separator yet
			if arg == "kabibi-go" || arg == "-r" {
				continue
			}
		}
		filteredArgs = append(filteredArgs, arg)
	}
	os.Args = filteredArgs

	flag.Parse()

	if *helpPtr {
		fmt.Printf("Usage: kabibi-go [options] [script_file [args...]]\n\n")
		fmt.Printf("Options:\n")
		flag.PrintDefaults()
		fmt.Printf("\nDescription:\n")
		fmt.Printf("  kabibi-go is an AI-enhanced file manager and shell.\n")
		fmt.Printf("  If a command string (-c) or script file is provided, it runs in batch mode.\n")
		fmt.Printf("  If a prompt (-p) is provided, it directly streams AI inference to standard output.\n")
		fmt.Printf("  Otherwise, it starts in interactive TUI mode.\n")
		return
	}

	// Prompt mode: -p "prompt"
	if *promptPtr != "" {
		runPrompt(*promptPtr)
		return
	}

	// Batch mode: -c "command"
	if *commandPtr != "" {
		runBatchCommand(*commandPtr, nil)
		return
	}

	// Batch mode: script_file
	if flag.NArg() > 0 {
		runBatchFile(flag.Arg(0), flag.Args()[1:])
		return
	}

	m := initialModel()
	AppProgram = tea.NewProgram(&m)
	if _, err := AppProgram.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

func runBatchCommand(cmdStr string, args []string) {
	ctx := context.Background()
	r, err := createRunner(ctx, os.Stdin, os.Stdout, os.Stderr, "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating runner: %v\n", err)
		os.Exit(1)
	}
	r.Run(ctx, parseCommand(cmdStr))
}

func runBatchFile(filePath string, args []string) {
	ctx := context.Background()
	r, err := createRunner(ctx, os.Stdin, os.Stdout, os.Stderr, "", args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating runner: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Open(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening script: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	r.Run(ctx, parseCommandReader(f, filePath))
}

func runPrompt(prompt string) {
	ctx := context.Background()

	// Ensure assets exist (blocking, printing to stdout if no TUI chan provided)
	if err := ensureLiteRT(ctx, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing AI runtime: %v\n", err)
		os.Exit(1)
	}
	if err := ensureGemma(ctx, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing AI weights: %v\n", err)
		os.Exit(1)
	}

	err := runAIPrompt(prompt, func(token string) {
                fmt.Print(token)
        })
	fmt.Println()

	if err != nil {
		fmt.Fprintf(os.Stderr, "\nAI Error: %v\n", err)
		os.Exit(1)
	}
}
