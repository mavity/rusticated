package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"ai"
)

// runTerminalLoop is the interactive REPL.
func runTerminalLoop(ctx context.Context, session *ai.Session) {
	fmt.Println("Pope CLI AI Harness (Ctrl+C or 'exit' to quit)")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		promptReadyAt := time.Now()
		if !scanner.Scan() {
			break
		}
		submittedAt := time.Now()
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		turnCtx := ai.WithTurnTiming(ctx, ai.TurnTiming{
			TypedAt:     promptReadyAt,
			SubmittedAt: submittedAt,
		})
		if err := session.RunTurn(turnCtx, line); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
}

