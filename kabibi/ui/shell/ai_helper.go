package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai"
)

type helperInput struct {
	_           struct{} `doc:"AI helper for repository and computer tasks. For codebase discovery it has find_files with semantic content search; ask it to search concepts with find_content, optionally restricted by structural find_paths. Provide a specific task and constraints."`
	Task        string   `json:"task" doc:"Be specific and literal, what do you actually need done?"`
	Description string   `json:"description" doc:"Detailed requirements, constraints, and context for the AI helper, describing why and how you want the task done."`
}

const defaultKabibiHelperSystemPrompt = `You are Kabibi's task helper. Investigate the repository using the available tools and base conclusions on inspected evidence.

For codebase discovery, use find_files with find_content set to a natural-language description of the behavior, implementation, symbol, or concept. Semantic content search is enabled by default. Use find_paths only for structural globs, paths, or directory prefixes; combine it with find_content to scope a semantic search. Do not treat path-only results or a filename search as evidence that file contents were searched. Use read_file on promising results before reporting conclusions.`

func kabibiHelperSystemMessage(description string) string {
	return defaultKabibiHelperSystemPrompt + "\n\nTask-specific requirements:\n" + strings.TrimSpace(description)
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
	engine *ai.Engine,
	embedder ai.PartEmbedder,
	execSess *ExecutionSession,
	compactionLimit int,
	verbose bool,
	onFeedback ai.LogFeedback,
) map[string]ai.Tool {
	return map[string]ai.Tool{
		"helper": newKabibiHelperTool(engine, embedder, execSess, compactionLimit, verbose, onFeedback),
	}
}

func newKabibiHelperTool(
	engine *ai.Engine,
	embedder ai.PartEmbedder,
	execSess *ExecutionSession,
	compactionLimit int,
	verbose bool,
	onFeedback ai.LogFeedback,
) ai.Tool {
	return ai.NewSessionTool(func(ctx context.Context, _ *ai.ToolContext, in helperInput) (any, error) {
		if strings.TrimSpace(in.Task) == "" {
			return nil, errors.New("helper task is required")
		}
		if strings.TrimSpace(in.Description) == "" {
			return nil, errors.New("helper description is required")
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
