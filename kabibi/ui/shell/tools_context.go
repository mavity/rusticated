package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"ai"
)

func registerShellContextTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["shell_history"] = ai.Tool{
		Description: "Retrieve recent shell commands and their outputs from Kabibi's shell session, including commands the user ran between AI prompts.",
		Parameters: map[string]any{
			"since_last_ai_turn": map[string]any{
				"type":        "boolean",
				"description": "If true, only return shell commands executed since the previous AI turn",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Maximum number of recent command records to return (default 10)",
			},
			"include_output": map[string]any{
				"type":        "boolean",
				"description": "Whether to include captured stdout/stderr for each command (default true)",
			},
		},
		Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
			in := struct {
				SinceLastAITurn bool  `json:"since_last_ai_turn"`
				Count           int   `json:"count"`
				IncludeOutput   *bool `json:"include_output"`
			}{
				Count: 10,
			}
			if len(call.RawArguments) > 0 {
				if err := json.Unmarshal(call.RawArguments, &in); err != nil {
					return nil, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			includeOut := true
			if in.IncludeOutput != nil {
				includeOut = *in.IncludeOutput
			}
			if feedback != nil {
				feedback("🐚 history ... ")
			}
			records := execSess.QueryHistory(in.SinceLastAITurn, in.Count, includeOut)
			if feedback != nil {
				feedback(fmt.Sprintf("%d command(s)", len(records)))
			}
			return map[string]any{
				"cwd":      execSess.Dir(),
				"commands": records,
			}, nil
		},
	}

	tools["shell_state"] = ai.Tool{
		Description: "Inspect Kabibi's current shell working directory, platform OS, last command status, and optional environment variables.",
		Parameters: map[string]any{
			"env_keys": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional list of environment variable names to inspect",
			},
		},
		Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
			var in struct {
				EnvKeys []string `json:"env_keys"`
			}
			if len(call.RawArguments) > 0 {
				if err := json.Unmarshal(call.RawArguments, &in); err != nil {
					return nil, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			if feedback != nil {
				feedback("🧭 shell_state ... ")
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
		},
	}
}
