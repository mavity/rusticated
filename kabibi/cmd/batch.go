package cmd

import (
	"fmt"
	"os"
)

// RunBatchCommand is STUBBED FOR STEP 1 - interactive TUI only
// TODO: Re-implement batch mode by exporting ui/shell symbols
func RunBatchCommand(cmdStr string, args []string) {
	fmt.Fprintf(os.Stderr, "Batch command mode not yet available (stubbed for Step 1)\n")
	os.Exit(1)
}

// RunBatchFile is STUBBED FOR STEP 1 - interactive TUI only
// TODO: Re-implement batch file mode by exporting ui/shell symbols
func RunBatchFile(filePath string, args []string) {
	fmt.Fprintf(os.Stderr, "Batch file mode not yet available (stubbed for Step 1)\n")
	os.Exit(1)
}

