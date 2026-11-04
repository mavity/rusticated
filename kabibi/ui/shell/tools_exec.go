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
	return formatTermStatusLineForID(m, exited, exitCode, elapsed, m.ID)
}

func formatTermStatusLineForID(m *ManagedPTY, exited bool, exitCode int, elapsed time.Duration, id int) string {
	if exited {
		return fmt.Sprintf("[status: EXITED (%d) after %.3fs]", exitCode, elapsed.Seconds())
	}
	return fmt.Sprintf("[status: STILL RUNNING (pid_id: %d) after %.3fs | use term_read, term_input, or term_kill with id=%d]",
		id, elapsed.Seconds(), id)
}

func registerExecTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["exec"] = ai.NewTool(func(ctx context.Context, in ExecInput, feedback ai.LogFeedback) (any, error) {
		if strings.TrimSpace(in.Command) == "" {
			return nil, errors.New("command argument is required")
		}
		lead := ai.ToolLead(ExecInput{}, in.Command, 22)
		if feedback != nil {
			feedback(lead)
		}

		formatted, procID, stillRunning, exitCode, runErr := execSess.ExecuteAIToolCommand(
			ctx,
			in.Command,
			in.TimeoutSec,
			in.KeepANSI,
		)
		if feedback != nil {
			if runErr != nil {
				feedback(lead + "failed")
			} else if stillRunning {
				feedback(lead + fmt.Sprintf("running (id=%d)", procID))
			} else if exitCode != 0 {
				feedback(lead + fmt.Sprintf("exit %d", exitCode))
			} else {
				feedback(lead + "ok")
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
		lead := ai.ToolLead(TermReadInput{}, fmt.Sprintf("#%d", in.ID), 18)
		if feedback != nil {
			feedback(lead)
		}
		result, err := readManagedPTY(ctx, m, in.ID, in)
		if err != nil {
			return nil, err
		}
		resultMap := result
		if feedback != nil {
			if resultMap["exited"] == true {
				feedback(lead + fmt.Sprintf("exited (%v)", resultMap["exit_code"]))
			} else {
				feedback(lead + "running")
			}
		}
		return resultMap, nil
	})

	tools["term_input"] = ai.NewTool(func(ctx context.Context, in TermInputInput, feedback ai.LogFeedback) (any, error) {
		m, ok := execSess.LookupManagedPTY(in.ID)
		if !ok {
			return nil, fmt.Errorf("managed process id=%d not found", in.ID)
		}
		lead := ai.ToolLead(TermInputInput{}, fmt.Sprintf("#%d %s", in.ID, in.Keys), 18)
		if feedback != nil {
			feedback(lead)
		}
		result, err := inputManagedPTY(ctx, m, in.ID, in)
		if err != nil {
			if feedback != nil {
				feedback(lead + "failed")
			}
			return nil, err
		}
		if feedback != nil {
			feedback(lead + "ok")
		}
		return result, nil
	})

	tools["term_kill"] = ai.NewTool(func(ctx context.Context, in TermKillInput, feedback ai.LogFeedback) (any, error) {
		m, ok := execSess.LookupManagedPTY(in.ID)
		if !ok {
			return nil, fmt.Errorf("managed process id=%d not found", in.ID)
		}
		lead := ai.ToolLead(TermKillInput{}, fmt.Sprintf("#%d", in.ID), 18)
		if feedback != nil {
			feedback(lead)
		}
		result, err := killManagedPTY(m, in.ID, in)
		if err != nil {
			if feedback != nil {
				feedback(lead + "failed")
			}
			return nil, err
		}
		if feedback != nil {
			feedback(lead + "killed")
		}
		return result, nil
	})
}

func readManagedPTY(ctx context.Context, m *ManagedPTY, responseID int, in TermReadInput) (map[string]any, error) {
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
	return managedPTYResult(m, responseID, text, exited, code, elapsed, true), nil
}

func inputManagedPTY(ctx context.Context, m *ManagedPTY, responseID int, in TermInputInput) (map[string]any, error) {
	if err := m.WriteKeys(in.Keys); err != nil {
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
	return managedPTYResult(m, responseID, text, exited, code, elapsed, true), nil
}

func killManagedPTY(m *ManagedPTY, responseID int, in TermKillInput) (map[string]any, error) {
	signal := in.Signal
	if strings.TrimSpace(signal) == "" {
		signal = "SIGTERM"
	}
	if err := m.Kill(signal); err != nil {
		return nil, err
	}
	text, exited, code, elapsed := m.Snapshot(false, false)
	return managedPTYResult(m, responseID, text, exited, code, elapsed, false), nil
}

func managedPTYResult(
	m *ManagedPTY,
	responseID int,
	text string,
	exited bool,
	code int,
	elapsed time.Duration,
	includeStillRunning bool,
) map[string]any {
	status := formatTermStatusLineForID(m, exited, code, elapsed, responseID)
	combined := status
	if text != "" {
		combined = text + "\n" + status
	}
	result := map[string]any{
		"id":        responseID,
		"output":    truncateToolOutput(text),
		"result":    truncateToolOutput(combined),
		"exited":    exited,
		"exit_code": code,
	}
	if includeStillRunning {
		result["still_running"] = !exited
	}
	return result
}
