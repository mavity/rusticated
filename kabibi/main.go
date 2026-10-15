package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/mavity/rusticated/kabibi/ui/app"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

func main() {
	var (
		cmdFlag     string
		promptFlag  string
		verboseFlag bool
	)

	flag.StringVar(&cmdFlag, "c", "", "Execute a command or AI prompt in batch mode and exit")
	flag.StringVar(&cmdFlag, "command", "", "Execute a command or AI prompt in batch mode and exit")
	flag.StringVar(&promptFlag, "p", "", "Execute a prompt in one-shot mode and exit")
	flag.StringVar(&promptFlag, "prompt", "", "Execute a prompt in one-shot mode and exit")
	flag.BoolVar(&verboseFlag, "v", false, "Verbose output")
	flag.BoolVar(&verboseFlag, "verbose", false, "Verbose output")
	flag.Parse()

	oneShotInput := cmdFlag
	if oneShotInput == "" {
		oneShotInput = promptFlag
	}
	if oneShotInput != "" {
		exitCode, err := shell.ExecuteOneShotLine(context.Background(), oneShotInput, verboseFlag, os.Stdout)
		if err != nil && exitCode == 0 {
			exitCode = 1
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "kabibi: %v\n", err)
		}
		os.Exit(exitCode)
	}

	// If a script file path was passed as positional argument, run it in batch mode
	if flag.NArg() > 0 {
		scriptPath := flag.Arg(0)
		data, err := os.ReadFile(scriptPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "kabibi: %v\n", err)
			os.Exit(1)
		}
		exitCode, err := shell.ExecuteOneShotLine(context.Background(), string(data), verboseFlag, os.Stdout)
		if err != nil && exitCode == 0 {
			exitCode = 1
		}
		os.Exit(exitCode)
	}

	appWidget, host, err := app.Boot()
	if err != nil {
		log.Fatalf("kabibi boot error: %v", err)
	}

	errRun := host.Run()
	_ = appWidget.Close()
	if errRun != nil {
		log.Fatalf("kabibi run error: %v", errRun)
	}
	os.Exit(0)
}
