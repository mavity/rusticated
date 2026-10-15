package shell

import (
	"strings"

	"ai"
)

const maxKabibiToolOutput = 32768

func truncateToolOutput(s string) string {
	if len(s) > maxKabibiToolOutput {
		return s[:maxKabibiToolOutput] + "\n...[truncated]"
	}
	return s
}

func compactToolLabel(s string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(s))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

// BuildKabibiTools assembles Kabibi's AI tool registry from its dedicated
// file, exec, and shell-context tool buckets.
func BuildKabibiTools(execSess *ExecutionSession) map[string]ai.Tool {
	tools := make(map[string]ai.Tool, 6)
	registerExecTools(tools, execSess)
	registerFileTools(tools, execSess)
	registerShellContextTools(tools, execSess)
	return tools
}
