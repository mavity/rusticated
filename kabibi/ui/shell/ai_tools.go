package shell

import (
	"ai"
)

const maxKabibiToolOutput = 32768

func truncateToolOutput(s string) string {
	if len(s) > maxKabibiToolOutput {
		return s[:maxKabibiToolOutput] + "\n...[truncated]"
	}
	return s
}

// BuildKabibiTools assembles Kabibi's AI tool registry from its dedicated
// file, exec, and shell-context tool buckets.
func BuildKabibiTools(execSess *ExecutionSession) map[string]ai.Tool {
	tools := make(map[string]ai.Tool, 7)
	registerExecTools(tools, execSess)
	registerFileTools(tools, execSess)
	registerShellContextTools(tools, execSess)
	return tools
}
