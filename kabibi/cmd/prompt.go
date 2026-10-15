package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/mavity/rusticated/kabibi/ui/shell"
)

// RunPrompt executes a one-shot AI or shell prompt and exits.
func RunPrompt(prompt string) {
	exitCode, err := shell.ExecuteOneShotLine(context.Background(), prompt, false, os.Stdout)
	if err != nil && exitCode == 0 {
		exitCode = 1
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kabibi: %v\n", err)
	}
	os.Exit(exitCode)
}

// RunPromptWithSession is for testing.
func RunPromptWithSession(ctx interface{}, session interface{}, prompt string) error {
	_, err := shell.ExecuteOneShotLine(context.Background(), prompt, false, os.Stdout)
	return err
}
