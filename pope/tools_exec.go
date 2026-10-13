package main

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
)

type ExecInput struct {
	_       struct{} `doc:"Execute a shell command on the host system and return its output."`
	Command string   `doc:"The shell command to run"`
}

func Exec(ctx context.Context, in ExecInput) (any, error) {
	if in.Command == "" {
		return nil, errors.New("command argument is required")
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", in.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", in.Command)
	}
	out, err := cmd.CombinedOutput()
	// A failing command is a normal outcome: the model gets its output and the error together.
	result := map[string]any{"output": truncateOutput(string(out))}
	if err != nil {
		result["error"] = err.Error()
	}
	return result, nil
}
