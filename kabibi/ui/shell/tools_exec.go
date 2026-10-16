package shell

import (
	"context"
	"errors"
	"strings"

	"ai"
)

type ExecInput struct {
	_           struct{} `doc:"Execute a command inside Kabibi's interactive shell runner (sharing its working directory and environment) and return its output and exit code." emoji:"💻"`
	Command     string   `json:"command" doc:"The shell command line to execute inside Kabibi's shell"`
	Explanation string   `json:"explanation" doc:"Brief explanation of why this command is being run and what information is sought"`
}

func registerExecTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["exec"] = ai.NewTool(func(ctx context.Context, in ExecInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Command) == "" {
			return nil, errors.New("command argument is required")
		}
		if feedback != nil {
			feedback(ai.ToolLead(ExecInput{}, in.Command, 22))
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
	})
}
