package cmd

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

var AppProgram *tea.Program

func CliMain() {
	helpPtr := flag.Bool("help", false, "Show help")
	commandPtr := flag.String("c", "", "Run a single command string")
	promptPtr := flag.String("p", "", "Run an AI prompt to stdout")
	flag.StringVar(promptPtr, "prompt", "", "Run an AI prompt to stdout (alias for -p)")
	aiModelPtr := flag.String("ai-model", "", "Model to use (e.g. gemma-4-E2B-it)")

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
			if arg == "kabibi" || arg == "-r" {
				continue
			}
		}
		filteredArgs = append(filteredArgs, arg)
	}
	os.Args = filteredArgs

	flag.Parse()

	if *aiModelPtr != "" {
		SetActiveModel(*aiModelPtr)
	}

	if *helpPtr {
		fmt.Printf("Usage: kabibi [options] [script_file [args...]]\n\n")
		fmt.Printf("Options:\n")
		flag.PrintDefaults()
		fmt.Printf("\nDescription:\n")
		fmt.Printf("  kabibi is an AI-enhanced file manager and shell.\n")
		fmt.Printf("  If a command string (-c) or script file is provided, it runs in batch mode.\n")
		fmt.Printf("  If a prompt (-p) is provided, it directly streams AI inference to standard output.\n")
		fmt.Printf("  Otherwise, it starts in interactive TUI mode.\n")
		return
	}

	// Prompt mode: -p "prompt"
	if *promptPtr != "" {
		RunPrompt(*promptPtr)
		os.Exit(0)
	}

	// Batch mode: -c "command"
	if *commandPtr != "" {
		RunBatchCommand(*commandPtr, nil)
		os.Exit(0)
	}

	// Batch mode: script_file
	if flag.NArg() > 0 {
		RunBatchFile(flag.Arg(0), flag.Args()[1:])
		os.Exit(0)
	}

	m := initialModel()
	AppProgram = tea.NewProgram(&AppHost{app: m})
	if _, err := AppProgram.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
	os.Exit(0)
}
