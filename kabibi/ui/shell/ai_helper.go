package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai"
)

type helperInput struct {
	_           struct{} `doc:"Service for access to files, running commands and other computer tasks. Always give helper a specific task and constraints."`
	Task        string   `json:"task" doc:"Be specific and literal, what do you actually need done?"`
	Description string   `json:"description" doc:"Detailed requirements, constraints, and context for the AI helper, describing why and how you want the task done."`
}

const defaultKabibiHelperSystemPrompt = `You are meticulous and thorough helper. Use any tools necessary to carry out the task effectively.

Carefully execute the task, and follow exact instructions.`

func kabibiHelperSystemMessage(description string) string {
	return defaultKabibiHelperSystemPrompt + "\n\nThe absolute unconditional mandate:\n" + strings.TrimSpace(description)
}

type helperToolResponse struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
}

type helperResult struct {
	FinalText        string              `json:"final_text"`
	LastToolResponse *helperToolResponse `json:"last_tool_response,omitempty"`
}

func lastHelperToolResponse(history []ai.PromptPart) *helperToolResponse {
	for i := len(history) - 1; i >= 0; i-- {
		part := history[i]
		if part.Role == ai.RoleTool {
			return &helperToolResponse{Name: part.ToolName, Response: part.ToolResponse}
		}
	}
	return nil
}

func buildKabibiMainTools(
	embedder ai.PartEmbedder,
	execSess *ExecutionSession,
	compactionLimit int,
	verbose bool,
	onFeedback ai.LogFeedback,
) map[string]ai.Tool {
	return map[string]ai.Tool{
		"helper": newKabibiHelperTool(embedder, execSess, compactionLimit, verbose, onFeedback),
	}
}

func newKabibiHelperTool(
	embedder ai.PartEmbedder,
	execSess *ExecutionSession,
	compactionLimit int,
	verbose bool,
	onFeedback ai.LogFeedback,
) ai.Tool {
	return ai.NewSessionTool(func(ctx context.Context, tc *ai.ToolContext, in helperInput) (any, error) {
		if strings.TrimSpace(in.Task) == "" {
			return nil, errors.New("helper task is required")
		}
		if strings.TrimSpace(in.Description) == "" {
			return nil, errors.New("helper description is required")
		}
		var engine *ai.Engine
		if tc != nil && tc.Session != nil {
			engine = tc.Session.Engine()
		}
		if engine == nil {
			return nil, errors.New("helper engine is not initialized")
		}

		var finalText strings.Builder
		helper, err := ai.NewSession(ai.SessionConfig{
			Engine:          engine,
			Embedder:        embedder,
			SystemMessage:   kabibiHelperSystemMessage(in.Description),
			Tools:           BuildKabibiTools(execSess),
			CompactionLimit: compactionLimit,
			OnContent:       func(text string) { finalText.WriteString(text) },
			OnFeedback:      onFeedback,
			Verbose:         verbose,
		})
		if err != nil {
			return nil, fmt.Errorf("create helper session: %w", err)
		}
		if err := helper.Init(); err != nil {
			shutdownErr := helper.WaitShutdown(context.Background())
			return nil, fmt.Errorf("initialize helper session: %w", errors.Join(err, shutdownErr))
		}

		runErr := helper.RunTurn(ctx, in.Task)
		shutdownErr := helper.WaitShutdown(context.Background())
		if runErr != nil {
			return nil, fmt.Errorf("run helper task: %w", errors.Join(runErr, shutdownErr))
		}
		if shutdownErr != nil {
			return nil, fmt.Errorf("close helper session: %w", shutdownErr)
		}

		return helperResult{
			FinalText:        strings.TrimSpace(finalText.String()),
			LastToolResponse: lastHelperToolResponse(helper.CanonicalHistory),
		}, nil
	})
}
