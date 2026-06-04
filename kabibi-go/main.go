package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	m := initialModel()
	p := tea.NewProgram(&m)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}

	// At the end, we want to clear the screen of the "active" TUI and print the history
	if fm, ok := finalModel.(model); ok {
		// Output the final plume history to the terminal permanently
		fmt.Print("\033[H\033[2J") // Clear screen
		for _, line := range fm.plume {
			if strings.TrimSpace(line) != "" {
				fmt.Println(line)
			}
		}
	}
}
