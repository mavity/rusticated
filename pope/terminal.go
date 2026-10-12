package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// runTerminalLoop is the interactive REPL.
func runTerminalLoop(ctx context.Context, session *HarnessSession) {
	fmt.Println("Pope CLI AI Harness (Ctrl+C or 'exit' to quit)")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		if err := session.RunTurn(ctx, line); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
}
