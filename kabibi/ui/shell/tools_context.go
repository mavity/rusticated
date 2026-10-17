package shell

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"ai"
)

type ShellHistoryInput struct {
	_               struct{} `doc:"Retrieve recent shell commands and interaction blocks from Kabibi's shell session, including commands the user ran between AI prompts." emoji:"🐚"`
	SinceLastAITurn bool     `json:"since_last_ai_turn,omitempty" doc:"If true, only return shell commands executed since the previous AI turn"`
	Count           int      `json:"count,omitempty" doc:"Maximum number of recent command records to return (default 10)"`
	IncludeOutput   *bool    `json:"include_output,omitempty" doc:"Whether to include captured stdout/stderr for each command (default true)"`
	Explanation     string   `json:"explanation" doc:"Brief explanation of why shell history is being inspected"`
}

type ReadCommandOutputInput struct {
	_           struct{} `doc:"Read the full or sliced output of a specific shell command or interaction block by its numeric ID." emoji:"📜"`
	ID          int      `json:"id" doc:"1-based ID of the command record from shell_history"`
	StartLine   int      `json:"start_line,omitempty" doc:"Optional 1-based start line (inclusive)"`
	EndLine     int      `json:"end_line,omitempty" doc:"Optional 1-based end line (inclusive)"`
	Search      string   `json:"search,omitempty" doc:"Optional substring filter to return only matching lines with 1-based line numbers"`
	Explanation string   `json:"explanation" doc:"Brief explanation of why this command output is being inspected"`
}

type ShellStateInput struct {
	_           struct{} `doc:"Inspect Kabibi's current shell working directory, platform OS, last command status, and optional environment variables." emoji:"🧭"`
	EnvKeys     []string `json:"env_keys,omitempty" doc:"Optional list of environment variable names to inspect"`
	Explanation string   `json:"explanation" doc:"Brief explanation of why shell state is being inspected"`
}

func registerShellContextTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["shell_history"] = ai.NewTool(func(ctx context.Context, in ShellHistoryInput, feedback ai.LogFeedback) (any, error) {
		count := in.Count
		if count <= 0 {
			count = 10
		}
		includeOut := true
		if in.IncludeOutput != nil {
			includeOut = *in.IncludeOutput
		}
		if feedback != nil {
			feedback(ai.ToolLead(ShellHistoryInput{}, "history", 18))
		}
		records := execSess.QueryHistory(in.SinceLastAITurn, count, includeOut)
		if feedback != nil {
			feedback(fmt.Sprintf("%d command(s)", len(records)))
		}
		return map[string]any{
			"cwd":      execSess.Dir(),
			"commands": records,
		}, nil
	})

	tools["read_command_output"] = ai.NewTool(func(ctx context.Context, in ReadCommandOutputInput, feedback ai.LogFeedback) (any, error) {
		if in.ID <= 0 {
			return nil, fmt.Errorf("id must be a positive command record ID")
		}
		if feedback != nil {
			feedback(ai.ToolLead(ReadCommandOutputInput{}, fmt.Sprintf("#%d", in.ID), 18))
		}
		rec, ok := execSess.RecordByID(in.ID)
		if !ok {
			if feedback != nil {
				feedback("not found")
			}
			return nil, fmt.Errorf("command record #%d not found", in.ID)
		}

		raw := strings.ReplaceAll(rec.Output, "\r\n", "\n")
		trimmed := strings.TrimSuffix(raw, "\n")
		var lines []string
		if trimmed != "" {
			lines = strings.Split(trimmed, "\n")
		}

		var content string
		if in.Search != "" {
			needle := strings.ToLower(in.Search)
			var matches []string
			for i, ln := range lines {
				if strings.Contains(strings.ToLower(ln), needle) {
					matches = append(matches, fmt.Sprintf("%d: %s", i+1, ln))
				}
			}
			content = strings.Join(matches, "\n")
		} else if in.StartLine > 0 || in.EndLine > 0 {
			start := in.StartLine
			if start <= 0 {
				start = 1
			}
			end := in.EndLine
			if end <= 0 || end > len(lines) {
				end = len(lines)
			}
			if start <= len(lines) && start <= end {
				content = strings.Join(lines[start-1:end], "\n")
			}
		} else {
			content = rec.Output
		}

		if feedback != nil {
			feedback(fmt.Sprintf("%d lines", len(lines)))
		}
		return map[string]any{
			"id":          rec.ID,
			"kind":        rec.Kind,
			"source":      rec.Source,
			"command":     rec.Command,
			"cwd":         rec.Cwd,
			"exit_code":   rec.ExitCode,
			"total_lines": len(lines),
			"byte_len":    rec.ByteLen,
			"output":      truncateToolOutput(content),
		}, nil
	})

	tools["shell_state"] = ai.NewTool(func(ctx context.Context, in ShellStateInput, feedback ai.LogFeedback) (any, error) {
		if feedback != nil {
			feedback(ai.ToolLead(ShellStateInput{}, "shell_state", 18))
		}
		envMap := execSess.EnvMap()
		selectedEnv := map[string]string{}
		for _, k := range in.EnvKeys {
			if v, ok := lookupEnvValue(envMap, k); ok {
				selectedEnv[k] = v
			}
		}
		records := execSess.Records()
		var lastCmd any
		for i := len(records) - 1; i >= 0; i-- {
			if records[i].Kind == BlockBanner {
				continue
			}
			last := records[i]
			lastCmd = map[string]any{
				"id":        last.ID,
				"kind":      last.Kind,
				"command":   last.Command,
				"exit_code": last.ExitCode,
				"cwd":       last.Cwd,
			}
			break
		}
		if feedback != nil {
			feedback("ok")
		}
		return map[string]any{
			"cwd":          execSess.Dir(),
			"os":           runtime.GOOS,
			"env":          selectedEnv,
			"last_command": lastCmd,
		}, nil
	})
}
