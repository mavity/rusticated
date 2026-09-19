package cmd

import (
	"context"
	"fmt"
	"os"
)

// runPromptWithSession executes an AI prompt using the provided session.
// This is extracted for testing purposes - it accepts a mockable AISession interface.
func RunPromptWithSession(ctx context.Context, session AISession, prompt string) error {
	return session.SendMessage(ctx, prompt, func(token string) {
		fmt.Print(token)
	})
}

func RunPrompt(prompt string) {
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

	// Create a session and run the prompt
	session := NewLMSession(&Conversation{})
	err := RunPromptWithSession(ctx, session, prompt)
	session.Close()
	fmt.Println()

	if err != nil {
		fmt.Fprintf(os.Stderr, "\nAI Error: %v\n", err)
		os.Exit(1)
	}
}
