package cmd

import (
	"fmt"
	"os"
)

// RunPrompt is STUBBED FOR STEP 1 - interactive TUI only
// TODO: Re-implement prompt mode by exporting ui/chat or ui/shell symbols
func RunPrompt(prompt string) {
	fmt.Fprintf(os.Stderr, "Prompt mode not yet available (stubbed for Step 1)\n")
	os.Exit(1)
}

// RunPromptWithSession is for testing - left as placeholder
func RunPromptWithSession(ctx interface{}, session interface{}, prompt string) error {
	fmt.Fprintf(os.Stderr, "RunPromptWithSession not yet available\n")
	os.Exit(1)
	return nil
}

