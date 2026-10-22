package shell

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ai"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/mavity/rusticated/kabibi/gitcore"
	"github.com/mavity/rusticated/kabibi/tools/fabric"
	"github.com/mavity/rusticated/kabibi/tools/hashline"
)

type GitInspectInput struct {
	_           struct{} `doc:"Inspect the Git repository rooted at Kabibi's current shell directory." emoji:"🌿"`
	Action      string   `json:"action" doc:"Inspection: status, log, diff, show, blame, branches, stash_list, or reflog."`
	Limit       int      `json:"limit,omitempty" doc:"Maximum results for log or reflog (default 10, maximum 100)"`
	Path        string   `json:"path,omitempty" doc:"Repository-relative path filter for log, diff, or blame"`
	Revision    string   `json:"revision,omitempty" doc:"Git revision or A..B range for log, diff, show, or reflog"`
	Search      string   `json:"search,omitempty" doc:"Filter log entries by commit message and author using progressive fabric search"`
	Staged      bool     `json:"staged,omitempty" doc:"Inspect the staged diff instead of unstaged worktree changes"`
	Hashline    *bool    `json:"hashline,omitempty" doc:"Include LINE:HASH| anchors in diff output (default true)"`
	StartLine   int      `json:"start_line,omitempty" doc:"First 1-based line to return for blame"`
	EndLine     int      `json:"end_line,omitempty" doc:"Last 1-based line to return for blame"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why the repository is being inspected"`
}

type GitMutateInput struct {
	_           struct{} `doc:"Modify the Git repository rooted at Kabibi's current shell directory. Mutations execute without confirmation." emoji:"🛠️"`
	Action      string   `json:"action" doc:"Mutation: stage, unstage, commit, branch_create, branch_delete, switch, restore, reset, stash_push, stash_pop, cherry_pick, revert, or merge."`
	Paths       []string `json:"paths,omitempty" doc:"Repository-relative paths to stage, unstage, or restore"`
	All         bool     `json:"all,omitempty" doc:"Stage/unstage all changes, or include tracked changes in a commit"`
	Message     string   `json:"message,omitempty" doc:"Commit message for commit; reserved for stash_push"`
	Amend       bool     `json:"amend,omitempty" doc:"Replace the current commit with the staged changes"`
	Name        string   `json:"name,omitempty" doc:"Branch name for branch_create or branch_delete; target branch/revision for switch"`
	Revision    string   `json:"revision,omitempty" doc:"Starting revision for branch_create, restore/reset target, or switch target"`
	Checkout    bool     `json:"checkout,omitempty" doc:"Switch to the newly created branch"`
	Force       bool     `json:"force,omitempty" doc:"Force deletion of an unmerged branch"`
	Staged      bool     `json:"staged,omitempty" doc:"Restore the index rather than the worktree"`
	Mode        string   `json:"mode,omitempty" doc:"Reset mode: soft, mixed (default), or hard"`
	Explanation string   `json:"explanation,omitempty" doc:"Brief explanation of why the repository is being changed"`
}

func registerGitTools(tools map[string]ai.Tool, execSess *ExecutionSession) {
	tools["git_inspect"] = ai.NewSessionTool(func(ctx context.Context, tc *ai.ToolContext, in GitInspectInput) (any, error) {
		var feedback ai.LogFeedback
		var embedder ai.PartEmbedder
		if tc != nil {
			feedback = tc.Feedback
			if tc.Session != nil {
				embedder = tc.Session.Embedder()
			}
		}
		if execSess == nil {
			return nil, errors.New("Git inspection requires an active shell session")
		}
		action := strings.ToLower(strings.TrimSpace(in.Action))
		if !oneOf(action, "status", "log", "diff", "show", "blame", "branches", "stash_list", "reflog") {
			return nil, fmt.Errorf("unsupported git_inspect action %q; supported actions: status, log, diff, show, blame, branches, stash_list, reflog", in.Action)
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
			commits, err := repo.LogRevision(100, in.Path, in.Revision)
			if err != nil {
				if feedback != nil {
					feedback("failed")
				}
				return nil, err
			}
			if strings.TrimSpace(in.Search) != "" {
				lines := make([]string, len(commits))
				for i, commit := range commits {
					lines[i] = commit.Message + " " + commit.Author
				}
				cacheKey := repo.Root() + "/.git/log"
				contentVersion := int64(hashline.XXHash64String(strings.Join(lines, "\n")))
				search := fabric.SearchAndExpand(cacheKey, contentVersion, lines, in.Search, "auto", embedder, fabric.DefaultTotalBudget)
				matched := make(map[int]bool, len(search.MatchLines))
				for _, line := range search.MatchLines {
					matched[line-1] = true
				}
				filtered := make([]gitcore.CommitSummary, 0, len(matched))
				for i, commit := range commits {
					if matched[i] {
						filtered = append(filtered, commit)
					}
				}
				commits = filtered
			}
			if len(commits) > limit {
				commits = commits[:limit]
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
		switch action {
		case "diff":
			if in.Limit != 0 || in.Search != "" || in.StartLine != 0 || in.EndLine != 0 {
				return nil, errors.New("git_inspect diff does not accept limit, search, start_line, or end_line")
			}
			files, err := repo.Diff(in.Staged, in.Revision, in.Path)
			if err != nil {
				return nil, err
			}
			patch := gitcore.FormatPatch(files)
			useHashline := true
			if in.Hashline != nil {
				useHashline = *in.Hashline
			}
			anchors := make(map[string]string)
			if in.Revision == "" && !in.Staged && useHashline {
				for _, file := range files {
					if len(file.New) == 0 || file.Binary {
						continue
					}
					anchors[file.Path] = hashline.FormatHashlines(file.Path, file.New, nil, "modified worktree file", true)
				}
			}
			return map[string]any{"root": repo.Root(), "diff": patch, "files": anchors}, nil
		case "show":
			if in.Path != "" || in.Search != "" || in.Staged || in.StartLine != 0 || in.EndLine != 0 || in.Limit != 0 {
				return nil, errors.New("git_inspect show accepts only revision")
			}
			revision := firstNonBlank(in.Revision, "HEAD")
			commit, patch, err := repo.Show(revision)
			if err != nil {
				return nil, err
			}
			files, err := repo.Diff(false, revision, "")
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"root": repo.Root(),
				"commit": map[string]any{
					"hash": commit.Hash.String(), "author": commit.Author.String(), "date": commit.Author.When,
					"message": strings.TrimSpace(commit.Message), "parents": hashStrings(commit.ParentHashes),
				},
				"diff": patch,
				"stat": gitcore.DiffStat(files),
			}, nil
		case "blame":
			if strings.TrimSpace(in.Path) == "" || in.Revision != "" || in.Search != "" || in.Staged || in.Limit != 0 {
				return nil, errors.New("git_inspect blame requires path and accepts start_line/end_line only")
			}
			if in.StartLine < 0 || in.EndLine < 0 || (in.StartLine > 0 && in.EndLine > 0 && in.EndLine < in.StartLine) {
				return nil, errors.New("git blame line range is invalid")
			}
			lines, err := repo.Blame(in.Path)
			if err != nil {
				return nil, err
			}
			start, end := in.StartLine, in.EndLine
			if start <= 0 {
				start = 1
			}
			if end <= 0 || end > len(lines) {
				end = len(lines)
			}
			result := make([]map[string]any, 0)
			for i := start; i <= end && i <= len(lines); i++ {
				line := lines[i-1]
				result = append(result, map[string]any{
					"line": i, "hash": line.Hash, "author": line.Author, "date": line.Date,
					"content": hashline.FormatLine(i, line.Content, true),
				})
			}
			return map[string]any{"root": repo.Root(), "path": in.Path, "lines": result}, nil
		case "branches":
			if in.Path != "" || in.Revision != "" || in.Search != "" || in.Staged || in.StartLine != 0 || in.EndLine != 0 || in.Limit != 0 {
				return nil, errors.New("git_inspect branches accepts no filters")
			}
			branches, err := repo.Branches()
			if err != nil {
				return nil, err
			}
			result := make([]map[string]any, len(branches))
			for i, branch := range branches {
				result[i] = map[string]any{"name": branch.Name, "hash": branch.Hash, "current": branch.Current}
			}
			return map[string]any{"root": repo.Root(), "branches": result}, nil
		case "stash_list", "reflog":
			limit := in.Limit
			if limit == 0 {
				limit = 10
			}
			if limit < 1 || limit > 100 {
				return nil, fmt.Errorf("git %s limit must be between 1 and 100, got %d", action, limit)
			}
			var entries []gitcore.ReflogEntry
			if action == "stash_list" {
				if in.Revision != "" || in.Path != "" || in.Search != "" || in.Staged {
					return nil, errors.New("git_inspect stash_list accepts only limit")
				}
				entries, err = repo.StashList(limit)
			} else {
				if in.Path != "" || in.Search != "" || in.Staged {
					return nil, errors.New("git_inspect reflog accepts revision and limit")
				}
				entries, err = repo.Reflog(in.Revision, limit)
			}
			if err != nil {
				return nil, err
			}
			result := make([]map[string]any, len(entries))
			for i, entry := range entries {
				result[i] = map[string]any{
					"selector": entry.Selector, "old_hash": entry.OldHash, "new_hash": entry.NewHash,
					"author": entry.Author, "date": entry.Date, "message": entry.Message,
				}
			}
			return map[string]any{"root": repo.Root(), "entries": result}, nil
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
		if !oneOf(action, "stage", "unstage", "commit", "branch_create", "branch_delete", "switch", "checkout", "restore", "reset", "stash_push", "stash_pop", "cherry_pick", "revert", "merge") {
			return nil, fmt.Errorf("unsupported git_mutate action %q", in.Action)
		}
		if feedback != nil {
			feedback(ai.ToolLead(GitMutateInput{}, action, 18))
		}
		if oneOf(action, "stash_push", "stash_pop", "cherry_pick", "revert", "merge") {
			return executeGitToolMutation(ctx, execSess, action, in)
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
		case "unstage":
			if in.Message != "" || in.Amend || in.Name != "" || in.Revision != "" || in.Checkout || in.Force || in.Staged || in.Mode != "" {
				return nil, errors.New("git_mutate unstage accepts paths and all only")
			}
			if err := repo.Unstage(in.Paths, in.All); err != nil {
				return nil, err
			}
			return map[string]any{"success": true, "unstaged_paths": in.Paths, "all": in.All}, nil
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
		case "branch_create":
			if in.Name == "" || len(in.Paths) > 0 || in.All || in.Message != "" || in.Amend || in.Force || in.Staged || in.Mode != "" {
				return nil, errors.New("git_mutate branch_create requires name and accepts revision and checkout")
			}
			hash, err := repo.CreateBranch(in.Name, in.Revision, in.Checkout)
			if err != nil {
				return nil, err
			}
			return map[string]any{"success": true, "branch": in.Name, "commit": hash, "checked_out": in.Checkout}, nil
		case "branch_delete":
			if in.Name == "" || len(in.Paths) > 0 || in.All || in.Message != "" || in.Amend || in.Revision != "" || in.Checkout || in.Staged || in.Mode != "" {
				return nil, errors.New("git_mutate branch_delete requires name and accepts force")
			}
			if err := repo.DeleteBranch(in.Name, in.Force); err != nil {
				return nil, err
			}
			return map[string]any{"success": true, "branch": in.Name}, nil
		case "switch", "checkout":
			target := in.Name
			if target == "" {
				target = in.Revision
			}
			if len(in.Paths) > 0 || in.All || in.Message != "" || in.Amend || in.Staged || in.Mode != "" || in.Force || (in.Name != "" && in.Revision != "") {
				return nil, errors.New("git_mutate switch requires name or revision and does not force-discard worktree changes")
			}
			hash, err := repo.Switch(target, false)
			if err != nil {
				return nil, err
			}
			return map[string]any{"success": true, "revision": target, "commit": hash}, nil
		case "restore":
			if in.All || in.Message != "" || in.Amend || in.Name != "" || in.Checkout || in.Force || in.Mode != "" {
				return nil, errors.New("git_mutate restore accepts paths, staged, and revision")
			}
			backups := make(map[string]string)
			if !in.Staged {
				backups, err = repo.BackupDirtyFiles(in.Paths)
				if err != nil {
					return nil, err
				}
			}
			if err := repo.Restore(in.Paths, in.Staged, in.Revision); err != nil {
				return nil, err
			}
			note := formatBackupNote(backups)
			return map[string]any{"success": true, "restored_paths": in.Paths, "backups": backups, "note": note}, nil
		case "reset":
			if len(in.Paths) > 0 || in.All || in.Message != "" || in.Amend || in.Name != "" || in.Checkout || in.Force || in.Staged {
				return nil, errors.New("git_mutate reset accepts revision and mode")
			}
			mode := strings.ToLower(strings.TrimSpace(in.Mode))
			if mode == "" {
				mode = "mixed"
			}
			backups := make(map[string]string)
			if mode == "hard" {
				backups, err = repo.BackupDirtyFiles(nil)
				if err != nil {
					return nil, err
				}
			}
			if err := repo.Reset(in.Revision, mode); err != nil {
				return nil, err
			}
			note := formatBackupNote(backups)
			return map[string]any{"success": true, "revision": firstNonBlank(in.Revision, "HEAD"), "mode": mode, "backups": backups, "note": note}, nil
		default:
			return nil, fmt.Errorf("unsupported git_mutate action %q", in.Action)
		}
	})
}

func executeGitToolMutation(ctx context.Context, execSess *ExecutionSession, action string, input GitMutateInput) (any, error) {
	args := []string{"git"}
	switch action {
	case "stash_push":
		if len(input.Paths) > 0 && input.All {
			return nil, errors.New("git_mutate stash_push cannot combine paths with all")
		}
		if input.Amend || input.Name != "" || input.Revision != "" || input.Checkout || input.Force || input.Staged || input.Mode != "" {
			return nil, errors.New("git_mutate stash_push accepts paths, message, and all only")
		}
		args = append(args, "stash", "push")
		if input.Message != "" {
			args = append(args, "--message", input.Message)
		}
		if input.All {
			args = append(args, "--all")
		}
		if len(input.Paths) > 0 {
			args = append(args, "--")
			args = append(args, input.Paths...)
		}
	case "stash_pop":
		if len(input.Paths) > 0 || input.All || input.Message != "" || input.Amend || input.Name != "" || input.Revision != "" || input.Checkout || input.Force || input.Staged || input.Mode != "" {
			return nil, errors.New("git_mutate stash_pop accepts no additional options")
		}
		args = append(args, "stash", "pop")
	case "cherry_pick", "revert":
		revision := input.Revision
		if revision == "" {
			revision = input.Name
		}
		if revision == "" || len(input.Paths) > 0 || input.All || input.Message != "" || input.Amend || input.Checkout || input.Force || input.Staged || input.Mode != "" || (input.Name != "" && input.Revision != "") {
			return nil, fmt.Errorf("git_mutate %s requires one revision", action)
		}
		args = append(args, strings.ReplaceAll(action, "_", "-"), revision)
	case "merge":
		revision := input.Revision
		if revision == "" {
			revision = input.Name
		}
		if revision == "" || len(input.Paths) > 0 || input.All || input.Message != "" || input.Amend || input.Checkout || input.Force || input.Staged || input.Mode != "" || (input.Name != "" && input.Revision != "") {
			return nil, errors.New("git_mutate merge requires one branch or revision")
		}
		args = append(args, "merge", revision)
	default:
		return nil, fmt.Errorf("unsupported Git mutation %q", action)
	}
	command := joinShellArguments(args)
	record, err := execSess.RunCommand(ctx, command, nil, SourceAITool, nil)
	if err != nil {
		return nil, fmt.Errorf("git_mutate %s: %w", action, err)
	}
	if record.ExitCode != 0 {
		return nil, fmt.Errorf("git_mutate %s exited with status %d: %s", action, record.ExitCode, strings.TrimSpace(record.Output))
	}
	return map[string]any{"success": true, "action": action, "output": record.Output}, nil
}

func oneOf(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func firstNonBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatBackupNote(backups map[string]string) string {
	if len(backups) == 0 {
		return ""
	}
	hashes := make([]string, 0, len(backups))
	for _, hash := range backups {
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	return "[backup blob: " + strings.Join(hashes, ", ") + "]"
}

func hashStrings(hashes []plumbing.Hash) []string {
	result := make([]string, len(hashes))
	for i, hash := range hashes {
		result[i] = hash.String()
	}
	return result
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
