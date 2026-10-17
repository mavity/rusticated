package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ai"
	git "github.com/go-git/go-git/v5"
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
