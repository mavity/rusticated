package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai"
)

type ExecInput struct {
	_           struct{} `doc:"Execute a command inside Kabibi's interactive shell/PTY runner (sharing its working directory and environment), returning cleaned terminal output and exit status or promoting long-running commands to a background PTY handle." emoji:"💻"`
	Command     string   `json:"command" doc:"The shell command line to execute inside Kabibi's shell"`
	TimeoutSec  int      `json:"timeout_sec,omitempty" doc:"Seconds to wait before promoting to background PTY (default 15)"`
	KeepANSI    bool     `json:"keep_ansi,omitempty" doc:"If true, preserve bold/underline attributes while stripping colors (default false)"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why this command is being run"`
}

type TermReadInput struct {
	_        struct{} `doc:"Read settled terminal output from a running or finished background PTY process started by exec." emoji:"📺"`
	ID       int      `json:"id" doc:"1-based pid_id returned by exec"`
	WaitMs   int      `json:"wait_ms,omitempty" doc:"Milliseconds to wait for new output or exit (default 2000, max 30000)"`
	OnlyNew  *bool    `json:"only_new,omitempty" doc:"If true (default), return only new output since the last read unless a full screen redraw occurred"`
	KeepANSI bool     `json:"keep_ansi,omitempty" doc:"If true, preserve bold/underline attributes (default false)"`
}

type TermInputInput struct {
	_      struct{} `doc:"Send keystrokes or symbolic keys (<enter>, <ctrl+c>, <ctrl+d>, <up>, <esc>, etc.) to a running background PTY process and return the resulting output." emoji:"⌨️"`
	ID     int      `json:"id" doc:"1-based pid_id returned by exec"`
	Keys   string   `json:"keys" doc:"Text or symbolic key tokens (e.g. 'y<enter>', '<ctrl+c>', '<up><enter>')"`
	WaitMs int      `json:"wait_ms,omitempty" doc:"Milliseconds to wait after sending input before reading output (default 1000)"`
}

type TermKillInput struct {
	_      struct{} `doc:"Terminate or interrupt a background PTY process started by exec and return its final output and exit status." emoji:"🛑"`
	ID     int      `json:"id" doc:"1-based pid_id returned by exec"`
	Signal string   `json:"signal,omitempty" doc:"Signal to send: 'SIGINT', 'SIGTERM' (default), or 'SIGKILL'"`
}

func formatTermStatusLine(m *ManagedPTY, exited bool, exitCode int, elapsed time.Duration) string {
	if exited {
		return fmt.Sprintf("[status: EXITED (%d) after %.3fs]", exitCode, elapsed.Seconds())
	}
	return fmt.Sprintf("[status: STILL RUNNING (pid_id: %d) after %.3fs | use term_read, term_input, or term_kill with id=%d]",
		m.ID, elapsed.Seconds(), m.ID)
}

func registerExecTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["exec"] = ai.NewTool(func(ctx context.Context, in ExecInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Command) == "" {
			return nil, errors.New("command argument is required")
		}
		if feedback != nil {
			feedback(ai.ToolLead(ExecInput{}, in.Command, 22))
		}

		formatted, procID, stillRunning, exitCode, runErr := execSess.ExecuteAIToolCommand(
			ctx,
			in.Command,
			in.TimeoutSec,
			in.KeepANSI,
		)
		if feedback != nil {
			if runErr != nil {
				feedback("failed")
			} else if stillRunning {
				feedback(fmt.Sprintf("running (id=%d)", procID))
			} else if exitCode != 0 {
				feedback(fmt.Sprintf("exit %d", exitCode))
			} else {
				feedback("ok")
			}
		}
		if runErr != nil {
			return nil, runErr
		}

		// Extract plain output without the trailing bracketed footer for backward-compatible callers
		plainOut := formatted
		if idx := strings.LastIndex(formatted, "\n["); idx >= 0 {
			plainOut = formatted[:idx]
		} else if strings.HasPrefix(formatted, "[") {
			plainOut = ""
		}

		res := map[string]any{
			"output":        truncateToolOutput(plainOut),
			"result":        truncateToolOutput(formatted),
			"exit_code":     exitCode,
			"cwd":           execSess.Dir(),
			"pid_id":        procID,
			"still_running": stillRunning,
		}
		return res, nil
	})

	tools["term_read"] = ai.NewTool(func(ctx context.Context, in TermReadInput, feedback ai.LogFeedback) (any, error) {
		m, ok := execSess.LookupManagedPTY(in.ID)
		if !ok {
			return nil, fmt.Errorf("managed process id=%d not found", in.ID)
		}
		if feedback != nil {
			feedback(ai.ToolLead(TermReadInput{}, fmt.Sprintf("#%d", in.ID), 18))
		}
		waitMs := in.WaitMs
		if waitMs <= 0 {
			waitMs = 2000
		}
		if waitMs > 30000 {
			waitMs = 30000
		}
		onlyNew := true
		if in.OnlyNew != nil {
			onlyNew = *in.OnlyNew
		}

		m.WaitForActivity(ctx, time.Duration(waitMs)*time.Millisecond)
		text, exited, code, elapsed := m.Snapshot(onlyNew, in.KeepANSI)
		statusLine := formatTermStatusLine(m, exited, code, elapsed)
		if feedback != nil {
			if exited {
				feedback(fmt.Sprintf("exited (%d)", code))
			} else {
				feedback("running")
			}
		}
		combined := statusLine
		if text != "" {
			combined = text + "\n" + statusLine
		}
		return map[string]any{
			"id":            m.ID,
			"output":        truncateToolOutput(text),
			"result":        truncateToolOutput(combined),
			"exited":        exited,
			"exit_code":     code,
			"still_running": !exited,
		}, nil
	})

	tools["term_input"] = ai.NewTool(func(ctx context.Context, in TermInputInput, feedback ai.LogFeedback) (any, error) {
		m, ok := execSess.LookupManagedPTY(in.ID)
		if !ok {
			return nil, fmt.Errorf("managed process id=%d not found", in.ID)
		}
		if feedback != nil {
			feedback(ai.ToolLead(TermInputInput{}, fmt.Sprintf("#%d %s", in.ID, in.Keys), 18))
		}
		if err := m.WriteKeys(in.Keys); err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		waitMs := in.WaitMs
		if waitMs <= 0 {
			waitMs = 1000
		}
		if waitMs > 30000 {
			waitMs = 30000
		}
		m.WaitForActivity(ctx, time.Duration(waitMs)*time.Millisecond)
		text, exited, code, elapsed := m.Snapshot(true, false)
		statusLine := formatTermStatusLine(m, exited, code, elapsed)
		if feedback != nil {
			feedback("ok")
		}
		combined := statusLine
		if text != "" {
			combined = text + "\n" + statusLine
		}
		return map[string]any{
			"id":            m.ID,
			"output":        truncateToolOutput(text),
			"result":        truncateToolOutput(combined),
			"exited":        exited,
			"exit_code":     code,
			"still_running": !exited,
		}, nil
	})

	tools["term_kill"] = ai.NewTool(func(ctx context.Context, in TermKillInput, feedback ai.LogFeedback) (any, error) {
		m, ok := execSess.LookupManagedPTY(in.ID)
		if !ok {
			return nil, fmt.Errorf("managed process id=%d not found", in.ID)
		}
		if feedback != nil {
			feedback(ai.ToolLead(TermKillInput{}, fmt.Sprintf("#%d", in.ID), 18))
		}
		sig := in.Signal
		if strings.TrimSpace(sig) == "" {
			sig = "SIGTERM"
		}
		_ = m.Kill(sig)
		text, exited, code, elapsed := m.Snapshot(false, false)
		statusLine := formatTermStatusLine(m, exited, code, elapsed)
		if feedback != nil {
			feedback("killed")
		}
		combined := statusLine
		if text != "" {
			combined = text + "\n" + statusLine
		}
		return map[string]any{
			"id":        m.ID,
			"output":    truncateToolOutput(text),
			"result":    truncateToolOutput(combined),
			"exited":    exited,
			"exit_code": code,
		}, nil
	})
}

