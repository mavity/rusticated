package main

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"mvdan.cc/sh/v3/syntax"
)

// shellResultMsg contains the output of a command
type shellResultMsg struct {
	input  string
	output []string
	err    error
}

func (m *model) runShellCommand(input string) tea.Cmd {
	return func() tea.Msg {
		parser := syntax.NewParser()
		f, err := parser.Parse(strings.NewReader(input), "")
		if err != nil {
			return shellResultMsg{
				input:  input,
				output: []string{fmt.Sprintf("Parse error: %v", err)},
			}
		}

		var sb strings.Builder
		m.shellOut.SetTarget(&sb)
		defer m.shellOut.SetTarget(nil)
		
		err = m.runner.Run(context.Background(), f)

		res := shellResultMsg{
			input: input,
			err:   err,
		}

		outputStr := strings.TrimSpace(sb.String())
		if outputStr != "" {
			res.output = strings.Split(outputStr, "\n")
		}

		return res
	}
}
