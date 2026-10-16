package main

import (
	"ai"
)

type LogFeedback = ai.LogFeedback

const maxToolOutput = 32768

func truncateOutput(s string) string {
	if len(s) > maxToolOutput {
		return s[:maxToolOutput] + "\n...[truncated]"
	}
	return s
}

func toolLead(input any, primaryArg string, maxArgLen int) string {
	return ai.ToolLead(input, primaryArg, maxArgLen)
}

// Tools is Pope's declarative capability registry derived via struct reflection
// on each tool's typed input struct.
var Tools = map[string]ai.Tool{
	"exec":       ai.NewTool(Exec),
	"read_file":  ai.NewTool(ReadFile),
	"write_file": ai.NewTool(WriteFile),
}
