package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai"
	"github.com/mavity/rusticated/kabibi/gitcore"
)

type GitInspectInput struct {
	_           struct{} `doc:"Inspect the Git repository rooted at Kabibi's current shell directory." emoji:"🌿"`
	Action      string   `json:"action" doc:"Inspection to perform. Currently supported: status."`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why the repository is being inspected"`
}

func registerGitTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["git_inspect"] = ai.NewTool(func(ctx context.Context, in GitInspectInput, feedback ai.LogFeedback) (any, error) {
		if execSess == nil {
			return nil, errors.New("Git inspection requires an active shell session")
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action != "status" {
			return nil, fmt.Errorf("unsupported git_inspect action %q; supported action: status", in.Action)
		}
		if feedback != nil {
			feedback(ai.ToolLead(GitInspectInput{}, action, 18))
		}

		repo, err := gitcore.Open(execSess.Dir())
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		branch, err := repo.CurrentBranch()
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		status, err := repo.Status()
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		entries := make([]map[string]any, len(status))
		for i, entry := range status {
			entries[i] = map[string]any{
				"path":            entry.Path,
				"index_status":    string(entry.IndexStatus),
				"worktree_status": string(entry.WorktreeStatus),
				"original_path":   entry.OriginalPath,
			}
		}
		if feedback != nil {
			feedback(fmt.Sprintf("%d changed path(s)", len(entries)))
		}
		return map[string]any{
			"root":   repo.Root(),
			"branch": branch,
			"clean":  len(entries) == 0,
			"status": entries,
		}, nil
	})
}
