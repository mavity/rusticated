package cmd

import (
	"context"
	"fmt"
	"os"
)

func RunBatchCommand(cmdStr string, args []string) {
	ctx := context.Background()
	r, err := createRunner(ctx, os.Stdin, os.Stdout, os.Stderr, "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating runner: %v\n", err)
		os.Exit(1)
	}
	if err := r.Run(ctx, parseCommand(cmdStr)); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func RunBatchFile(filePath string, args []string) {
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

	if err := r.Run(ctx, parseCommandReader(f, filePath)); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
