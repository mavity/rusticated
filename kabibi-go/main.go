package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

var AppProgram *tea.Program

func main() {
	m := initialModel()
	AppProgram = tea.NewProgram(&m)
	if _, err := AppProgram.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}
