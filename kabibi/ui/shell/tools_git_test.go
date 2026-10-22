package shell

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/mavity/rusticated/kabibi/tools/hashline"
)

func TestGitInspectStatusUsesShellWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	execSess, err := NewExecutionSession(nested)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}

	tool, ok := BuildKabibiTools(execSess)["git_inspect"]
	if !ok {
		t.Fatal("git_inspect tool was not registered")
	}
	result, err := tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_inspect",
		RawArguments: json.RawMessage(`{"action":"status"}`),
	}, nil)
	if err != nil {
		t.Fatalf("git_inspect status: %v", err)
	}
	response := result.(map[string]any)
	if response["root"] != root || response["clean"] != false {
		t.Fatalf("git_inspect status response = %#v", response)
	}
}

func TestGitInspectRejectsUnsupportedAction(t *testing.T) {
	tool := BuildKabibiTools(nil)["git_inspect"]
	_, err := tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_inspect",
		RawArguments: json.RawMessage(`{"action":"rebase"}`),
	}, nil)
	if err == nil {
		t.Fatal("git_inspect accepted unsupported action")
	}
}

func TestGitMutateStagesAndCommits(t *testing.T) {
	root := t.TempDir()
	gitRepo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	cfg, err := gitRepo.Config()
	if err != nil {
		t.Fatalf("read repository config: %v", err)
	}
	cfg.User.Name = "Kabibi Test"
	cfg.User.Email = "author@example.test"
	if err := gitRepo.Storer.SetConfig(cfg); err != nil {
		t.Fatalf("configure repository identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	tools := BuildKabibiTools(execSess)

	stageResult, err := tools["git_mutate"].Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"stage","paths":["new.txt"]}`),
	}, nil)
	if err != nil {
		t.Fatalf("git_mutate stage: %v", err)
	}
	if stageResult.(map[string]any)["success"] != true {
		t.Fatalf("git_mutate stage response = %#v", stageResult)
	}

	commitResult, err := tools["git_mutate"].Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"commit","message":"add new file"}`),
	}, nil)
	if err != nil {
		t.Fatalf("git_mutate commit: %v", err)
	}
	if commitResult.(map[string]any)["success"] != true {
		t.Fatalf("git_mutate commit response = %#v", commitResult)
	}
}

func TestGitMutateBranchLifecycle(t *testing.T) {
	root, gitRepo := newGitToolTestRepository(t, "tracked.txt", "committed\n")
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	tool := BuildKabibiTools(execSess)["git_mutate"]
	originalHead, err := gitRepo.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatalf("read initial HEAD: %v", err)
	}
	defaultBranch := originalHead.Target()
	_, err = tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"branch_create","name":"feature","checkout":true}`),
	}, nil)
	if err != nil {
		t.Fatalf("create and switch branch: %v", err)
	}
	head, err := gitRepo.Reference(plumbing.HEAD, false)
	if err != nil || head.Target() != plumbing.NewBranchReferenceName("feature") {
		t.Fatalf("HEAD after branch create = %v, error = %v", head, err)
	}

	if _, err := tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"branch_delete","name":"feature","force":true}`),
	}, nil); err == nil {
		t.Fatal("branch_delete removed the checked-out branch")
	}
	if _, err := tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"switch","name":"` + defaultBranch.Short() + `"}`),
	}, nil); err != nil {
		t.Fatalf("switch back to original branch: %v", err)
	}
	if _, err := tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"branch_delete","name":"feature"}`),
	}, nil); err != nil {
		t.Fatalf("delete merged branch: %v", err)
	}
	if _, err := gitRepo.Reference(plumbing.NewBranchReferenceName("feature"), true); err == nil {
		t.Fatal("feature branch still exists after deletion")
	}
}

func TestGitMutateStashPushAndPop(t *testing.T) {
	root, repo := newGitToolTestRepository(t, "tracked.txt", "committed\n")
	path := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(path, []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("modify worktree: %v", err)
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	tool := BuildKabibiTools(execSess)["git_mutate"]
	result, err := tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"stash_push","message":"save changes"}`),
	}, nil)
	if err != nil {
		t.Fatalf("stash push: %v", err)
	}
	if result.(map[string]any)["success"] != true {
		t.Fatalf("stash push result = %#v", result)
	}
	stashed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stashed worktree file: %v", err)
	}
	if string(stashed) != "committed\n" {
		t.Fatalf("worktree after stash = %q, want committed contents", stashed)
	}
	result, err = tool.Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"stash_pop"}`),
	}, nil)
	if err != nil {
		t.Fatalf("stash pop: %v", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read popped worktree file: %v", err)
	}
	if string(restored) != "dirty\n" {
		t.Fatalf("worktree after stash pop = %q, want stashed changes", restored)
	}
	if _, err := repo.Head(); err != nil {
		t.Fatalf("read repository HEAD: %v", err)
	}
}

func TestGitMutateCherryPickAppliesCommit(t *testing.T) {
	root, repo := newGitToolTestRepository(t, "base.txt", "base\n")
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("read initial HEAD: %v", err)
	}
	baseBranch := head.Name()
	if err := worktree.Checkout(&git.CheckoutOptions{Create: true, Branch: plumbing.NewBranchReferenceName("feature")}); err != nil {
		t.Fatalf("create feature branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	if _, err := worktree.Add("feature.txt"); err != nil {
		t.Fatalf("stage feature file: %v", err)
	}
	signature := &object.Signature{Name: "Kabibi Test", Email: "test@example.test", When: time.Now()}
	cherryHash, err := worktree.Commit("feature commit", &git.CommitOptions{Author: signature, Committer: signature})
	if err != nil {
		t.Fatalf("commit feature: %v", err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: baseBranch}); err != nil {
		t.Fatalf("switch to base branch: %v", err)
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	result, err := BuildKabibiTools(execSess)["git_mutate"].Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"cherry_pick","revision":"` + cherryHash.String() + `"}`),
	}, nil)
	if err != nil {
		t.Fatalf("cherry-pick feature: %v", err)
	}
	if result.(map[string]any)["success"] != true {
		t.Fatalf("cherry-pick result = %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(root, "feature.txt"))
	if err != nil || string(content) != "feature\n" {
		t.Fatalf("cherry-picked file = %q, error = %v", content, err)
	}
}

func TestGitInspectDiffIncludesHashlineAnchors(t *testing.T) {
	root, gitRepo := newGitToolTestRepository(t, "tracked.txt", "before\n")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}

	result, err := BuildKabibiTools(execSess)["git_inspect"].Execute(context.Background(), ai.ToolCall{
		Name:         "git_inspect",
		RawArguments: json.RawMessage(`{"action":"diff"}`),
	}, nil)
	if err != nil {
		t.Fatalf("git_inspect diff: %v", err)
	}
	response := result.(map[string]any)
	if !strings.Contains(response["diff"].(string), "+after") {
		t.Fatalf("diff response = %#v, want changed content", response)
	}
	anchors := response["files"].(map[string]string)
	if !strings.Contains(anchors["tracked.txt"], "[tracked.txt#") ||
		!strings.Contains(anchors["tracked.txt"], "1:"+hashlineForTest("after")+"|after") {
		t.Fatalf("diff anchors = %#v", anchors)
	}
	if _, err := gitRepo.Head(); err != nil {
		t.Fatalf("read test repository HEAD: %v", err)
	}
}

func TestGitInspectShowDefaultsToHeadCommit(t *testing.T) {
	root, _ := newGitToolTestRepository(t, "tracked.txt", "committed\n")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatalf("modify worktree file: %v", err)
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	result, err := BuildKabibiTools(execSess)["git_inspect"].Execute(context.Background(), ai.ToolCall{
		Name:         "git_inspect",
		RawArguments: json.RawMessage(`{"action":"show"}`),
	}, nil)
	if err != nil {
		t.Fatalf("git_inspect show: %v", err)
	}
	response := result.(map[string]any)
	if !strings.Contains(response["diff"].(string), "+committed") ||
		strings.Contains(response["diff"].(string), "+uncommitted") {
		t.Fatalf("show response diff = %q, want the HEAD commit rather than worktree changes", response["diff"])
	}
}

func TestGitMutateRestoreReturnsRecoverableBlobBackup(t *testing.T) {
	root, gitRepo := newGitToolTestRepository(t, "tracked.txt", "committed\n")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("uncommitted bytes\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	result, err := BuildKabibiTools(execSess)["git_mutate"].Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"restore","paths":["tracked.txt"]}`),
	}, nil)
	if err != nil {
		t.Fatalf("git_mutate restore: %v", err)
	}
	response := result.(map[string]any)
	backups := response["backups"].(map[string]string)
	hash := backups["tracked.txt"]
	if hash == "" || !strings.Contains(response["note"].(string), hash) {
		t.Fatalf("restore response = %#v, want backup hash and note", response)
	}
	blob, err := gitRepo.BlobObject(plumbing.NewHash(hash))
	if err != nil {
		t.Fatalf("read loose backup blob: %v", err)
	}
	reader, err := blob.Reader()
	if err != nil {
		t.Fatalf("open loose backup blob: %v", err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read loose backup blob: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close loose backup blob: %v", err)
	}
	if string(data) != "uncommitted bytes\n" {
		t.Fatalf("loose backup data = %q", data)
	}
	restored, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(restored) != "committed\n" {
		t.Fatalf("restored file = %q, want committed content", restored)
	}
}

func TestGitMutateHardResetBacksUpDirtyFiles(t *testing.T) {
	root, gitRepo := newGitToolTestRepository(t, "tracked.txt", "committed\n")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("dirty tracked\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("dirty untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	execSess, err := NewExecutionSession(root)
	if err != nil {
		t.Fatalf("create execution session: %v", err)
	}
	result, err := BuildKabibiTools(execSess)["git_mutate"].Execute(context.Background(), ai.ToolCall{
		Name:         "git_mutate",
		RawArguments: json.RawMessage(`{"action":"reset","mode":"hard"}`),
	}, nil)
	if err != nil {
		t.Fatalf("git_mutate hard reset: %v", err)
	}
	response := result.(map[string]any)
	backups := response["backups"].(map[string]string)
	if backups["tracked.txt"] == "" || backups["untracked.txt"] == "" {
		t.Fatalf("hard reset backups = %#v, want both dirty files", backups)
	}
	tracked, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read reset tracked file: %v", err)
	}
	if string(tracked) != "committed\n" {
		t.Fatalf("hard reset tracked content = %q", tracked)
	}
	blob, err := gitRepo.BlobObject(plumbing.NewHash(backups["untracked.txt"]))
	if err != nil {
		t.Fatalf("read untracked backup blob: %v", err)
	}
	reader, err := blob.Reader()
	if err != nil {
		t.Fatalf("open untracked backup blob: %v", err)
	}
	untracked, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read untracked backup blob: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close untracked backup blob: %v", err)
	}
	if string(untracked) != "dirty untracked\n" {
		t.Fatalf("untracked backup blob = %q", untracked)
	}
}

func newGitToolTestRepository(t *testing.T, name, contents string) (string, *git.Repository) {
	t.Helper()
	root := t.TempDir()
	gitRepo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("write committed file: %v", err)
	}
	worktree, err := gitRepo.Worktree()
	if err != nil {
		t.Fatalf("open test worktree: %v", err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatalf("stage committed file: %v", err)
	}
	signature := &object.Signature{Name: "Kabibi Test", Email: "test@example.test", When: time.Now()}
	if _, err := worktree.Commit("initial", &git.CommitOptions{Author: signature, Committer: signature}); err != nil {
		t.Fatalf("commit test file: %v", err)
	}
	return root, gitRepo
}

func hashlineForTest(line string) string {
	return hashline.ComputeLineHash(line)
}
