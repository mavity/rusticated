package shell

import (
	"context"
	"fmt"
	"runtime"

	"ai"
)

type ShellHistoryInput struct {
	_               struct{} `doc:"Retrieve recent shell commands and their outputs from Kabibi's shell session, including commands the user ran between AI prompts." emoji:"🐚"`
	SinceLastAITurn bool     `json:"since_last_ai_turn,omitempty" doc:"If true, only return shell commands executed since the previous AI turn"`
	Count           int      `json:"count,omitempty" doc:"Maximum number of recent command records to return (default 10)"`
	IncludeOutput   *bool    `json:"include_output,omitempty" doc:"Whether to include captured stdout/stderr for each command (default true)"`
	Explanation     string   `json:"explanation" doc:"Brief explanation of why shell history is being inspected"`
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
		if len(records) > 0 {
			last := records[len(records)-1]
			lastCmd = map[string]any{
				"id":        last.ID,
				"command":   last.Command,
				"exit_code": last.ExitCode,
				"cwd":       last.Cwd,
			}
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
