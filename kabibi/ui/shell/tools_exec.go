package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai"
)

func registerExecTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["exec"] = ai.Tool{
		Description: "Execute a command inside Kabibi's interactive shell runner (sharing its working directory and environment) and return its output and exit code.",
		Parameters: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command line to execute inside Kabibi's shell",
			},
		},
		Required: []string{"command"},
		Execute: func(ctx context.Context, call ai.ToolCall, feedback ai.LogFeedback) (any, error) {
			var in struct {
				Command string `json:"command"`
			}
			if len(call.RawArguments) > 0 {
				if err := json.Unmarshal(call.RawArguments, &in); err != nil {
					return nil, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			if strings.TrimSpace(in.Command) == "" {
				return nil, errors.New("command argument is required")
			}
			if feedback != nil {
				feedback("💻 " + compactToolLabel(in.Command, 22) + " ... ")
			}
			rec, runErr := execSess.RunCommand(ctx, in.Command, nil, SourceAITool, nil)
			if feedback != nil {
				if runErr != nil || rec.ExitCode != 0 {
					feedback("failed")
				} else {
					feedback("ok")
				}
			}
			res := map[string]any{
				"output":    truncateToolOutput(rec.Output),
				"exit_code": rec.ExitCode,
				"cwd":       rec.Cwd,
			}
			if runErr != nil {
				res["error"] = runErr.Error()
			}
			return res, nil
		},
	}
}
