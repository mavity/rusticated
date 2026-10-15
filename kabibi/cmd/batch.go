package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/mavity/rusticated/kabibi/ui/shell"
)

// RunBatchCommand executes a command or AI prompt in headless batch mode and exits.
func RunBatchCommand(cmdStr string, args []string) {
	exitCode, err := shell.ExecuteOneShotLine(context.Background(), cmdStr, false, os.Stdout)
	if err != nil && exitCode == 0 {
		exitCode = 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kabibi: %v\n", err)
	}
	os.Exit(exitCode)
}

// RunBatchFile executes a shell script file in headless batch mode and exits.
func RunBatchFile(filePath string, args []string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kabibi: %v\n", err)
		os.Exit(1)
	}
	exitCode, runErr := shell.ExecuteOneShotLine(context.Background(), string(data), false, os.Stdout)
	if runErr != nil && exitCode == 0 {
		exitCode = 1
	}
	os.Exit(exitCode)
}
