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
	Action      string   `json:"action" doc:"Inspection to perform: status or log."`
	Limit       int      `json:"limit,omitempty" doc:"Maximum commits for the log action (default 10, maximum 100)"`
	Path        string   `json:"path,omitempty" doc:"Repository-relative path filter for the log action"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why the repository is being inspected"`
}

type GitMutateInput struct {
	_           struct{} `doc:"Modify the Git repository rooted at Kabibi's current shell directory. Mutations execute without confirmation." emoji:"🛠️"`
	Action      string   `json:"action" doc:"Mutation to perform: stage or commit."`
	Paths       []string `json:"paths,omitempty" doc:"Repository-relative paths to stage"`
	All         bool     `json:"all,omitempty" doc:"Stage all additions, modifications, and deletions, or include tracked changes in a commit"`
	Message     string   `json:"message,omitempty" doc:"Commit message for the commit action"`
	Amend       bool     `json:"amend,omitempty" doc:"Replace the current commit with the staged changes"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why the repository is being changed"`
}

func registerGitTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["git_inspect"] = ai.NewTool(func(ctx context.Context, in GitInspectInput, feedback ai.LogFeedback) (any, error) {
		if execSess == nil {
			return nil, errors.New("Git inspection requires an active shell session")
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action != "status" && action != "log" {
			return nil, fmt.Errorf("unsupported git_inspect action %q; supported actions: status, log", in.Action)
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
		if action == "log" {
			limit := in.Limit
			if limit == 0 {
				limit = 10
			}
			if limit < 1 || limit > 100 {
				if feedback != nil {
					feedback("failed")
				}
				return nil, fmt.Errorf("git log limit must be between 1 and 100, got %d", limit)
			}
			commits, err := repo.Log(limit, in.Path)
			if err != nil {
				if feedback != nil {
					feedback("failed")
				}
				return nil, err
			}
			results := make([]map[string]any, len(commits))
			for i, commit := range commits {
				results[i] = map[string]any{
					"hash":    commit.Hash,
					"author":  commit.Author,
					"date":    commit.Date,
					"message": commit.Message,
				}
			}
			if feedback != nil {
				feedback(fmt.Sprintf("%d commit(s)", len(commits)))
			}
			return map[string]any{"root": repo.Root(), "commits": results}, nil
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
		entries := statusResults(status)
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

	tools["git_mutate"] = ai.NewTool(func(ctx context.Context, in GitMutateInput, feedback ai.LogFeedback) (any, error) {
		if execSess == nil {
			return nil, errors.New("Git mutation requires an active shell session")
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if action != "stage" && action != "commit" {
			return nil, fmt.Errorf("unsupported git_mutate action %q; supported actions: stage, commit", in.Action)
		}
		if feedback != nil {
			feedback(ai.ToolLead(GitMutateInput{}, action, 18))
		}
		repo, err := gitcore.Open(execSess.Dir())
		if err != nil {
			if feedback != nil {
				feedback("failed")
			}
			return nil, err
		}
		switch action {
		case "stage":
			if in.Message != "" || in.Amend {
				if feedback != nil {
					feedback("failed")
				}
				return nil, errors.New("git_mutate stage does not accept message or amend")
			}
			if err := repo.Stage(in.Paths, in.All); err != nil {
				if feedback != nil {
					feedback("failed")
				}
				return nil, err
			}
			if feedback != nil {
				feedback("staged")
			}
			return map[string]any{"success": true, "staged_paths": in.Paths, "all": in.All}, nil
		case "commit":
			if len(in.Paths) > 0 {
				if feedback != nil {
					feedback("failed")
				}
				return nil, errors.New("git_mutate commit does not accept paths; stage paths first")
			}
			hash, err := repo.Commit(in.Message, in.All, in.Amend, execSess.EnvMap())
			if err != nil {
				if feedback != nil {
					feedback("failed")
				}
				return nil, err
			}
			if feedback != nil {
				feedback("committed " + hash[:10])
			}
			return map[string]any{"success": true, "commit": hash}, nil
		default:
			return nil, fmt.Errorf("unsupported git_mutate action %q", in.Action)
		}
	})
}

func statusResults(status []gitcore.StatusEntry) []map[string]any {
	entries := make([]map[string]any, len(status))
	for i, entry := range status {
		entries[i] = map[string]any{
			"path":            entry.Path,
			"index_status":    string(entry.IndexStatus),
			"worktree_status": string(entry.WorktreeStatus),
			"original_path":   entry.OriginalPath,
		}
	}
	return entries
}
