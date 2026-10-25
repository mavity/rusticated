package shell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai"
	"github.com/mavity/rusticated/kabibi/tools/hashline"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func TestModernTools_ReadFileHashlineFabricAndEditFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "service.go")
	srcContent := strings.Join([]string{
		"package service",
		"",
		"func Alpha() string {",
		"\tx := \"hello\"",
		"\treturn x",
		"}",
		"",
		"func Beta() string {",
		"\ttargetMarker := \"initial\"",
		"\treturn targetMarker",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(srcPath, []byte(srcContent), 0o644); err != nil {
		t.Fatal(err)
	}

	es, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	tools := BuildKabibiTools(es)

	// 1. read_file with search="targetMarker" returns fabric-expanded block with LINE:HASH| format
	readOut, err := tools["read_file"].Execute(context.Background(), ai.ToolCall{
		Name:         "read_file",
		RawArguments: json.RawMessage(`{"path":"service.go","search":"targetMarker"}`),
	}, nil)
	if err != nil {
		t.Fatalf("read_file search failed: %v", err)
	}
	readMap := readOut.(map[string]any)
	hashlinesStr := readMap["hashlines"].(string)
	if !strings.Contains(hashlinesStr, "func Beta() string {") {
		t.Fatalf("expected fabric expansion to include enclosing Beta header:\n%s", hashlinesStr)
	}
	if strings.Contains(hashlinesStr, "func Alpha()") {
		t.Fatalf("expected Alpha() to be folded out of search slice:\n%s", hashlinesStr)
	}

	// 2. edit_file using LINE:HASH anchor replaces line 9 cleanly
	line9Hash := hashline.ComputeLineHash("\ttargetMarker := \"initial\"")
	editArgs, _ := json.Marshal(map[string]any{
		"path": "service.go#" + readMap["tag"].(string),
		"edits": []map[string]any{
			{
				"op":           "replace",
				"start_anchor": "9:" + line9Hash,
				"content":      "\ttargetMarker := \"updated_via_hashline\"",
			},
		},
	})
	editOut, err := tools["edit_file"].Execute(context.Background(), ai.ToolCall{
		Name:         "edit_file",
		RawArguments: editArgs,
	}, nil)
	if err != nil {
		t.Fatalf("edit_file failed: %v", err)
	}
	editMap := editOut.(map[string]any)
	if editMap["applied_hunks"].(int) != 1 {
		t.Fatalf("expected 1 applied hunk, got %v", editMap)
	}

	updatedBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedBytes), "updated_via_hashline") {
		t.Fatalf("expected file to contain updated_via_hashline:\n%s", string(updatedBytes))
	}

	// 3. list_files returns compact d/x/l/- table
	listOut, err := tools["list_files"].Execute(context.Background(), ai.ToolCall{
		Name:         "list_files",
		RawArguments: json.RawMessage(`{"query":"*.go"}`),
	}, nil)
	if err != nil {
		t.Fatalf("list_files failed: %v", err)
	}
	listTable := listOut.(string)
	if !strings.Contains(listTable, "service.go") {
		t.Fatalf("expected list_files table to include service.go:\n%s", listTable)
	}
}

func TestModernTools_GutterStripeAndExecLifecycle(t *testing.T) {
	dir := t.TempDir()
	sh := New(ShellOptions{
		WorkingDir: dir,
	})
	defer sh.Close()

	tools := BuildKabibiTools(sh.ExecutionSession())

	// 1. Fast exec finishes within timeout and creates a SourceAITool block with a background-colored space gutter.
	execOut, err := tools["exec"].Execute(context.Background(), ai.ToolCall{
		Name:         "exec",
		RawArguments: json.RawMessage(`{"command":"echo gutter_test_ok","timeout_sec":5}`),
	}, nil)
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	execMap := execOut.(map[string]any)
	if execMap["still_running"].(bool) {
		t.Fatalf("expected fast command to finish, got still_running=true")
	}
	if !strings.Contains(execMap["result"].(string), "[exit: 0 |") {
		t.Fatalf("expected settled footer in result, got %q", execMap["result"].(string))
	}

	// Verify the UI Shell created a SourceAITool block and rendered column 0 as a non-printable colored space.
	blocks := sh.Blocks()
	var foundAIBlock *CommandBlock
	for i := range blocks {
		if blocks[i].Source == SourceAITool {
			foundAIBlock = &blocks[i]
			break
		}
	}
	if foundAIBlock == nil {
		t.Fatal("expected SourceAITool CommandBlock to be added to Shell")
	}
	buf := renderBlock(*foundAIBlock, 80)
	if got := buf.Get(0, 0); got.Grapheme != " " || got.Style.Background() != terminal.Color(0x00ff00) {
		t.Fatalf("expected col 0 of SourceAITool block to be a green background-colored space, got %#v", got)
	}
	if got := buf.Get(1, 0).Grapheme; got != " " {
		t.Fatalf("expected col 1 of SourceAITool block to be ' ', got %q", got)
	}

	// 2. Verify term_read on the completed pid_id works
	pidID := execMap["pid_id"].(int)
	readArgs, _ := json.Marshal(map[string]any{
		"id":       pidID,
		"wait_ms":  50,
		"only_new": false,
	})
	trOut, err := tools["term_read"].Execute(context.Background(), ai.ToolCall{
		Name:         "term_read",
		RawArguments: readArgs,
	}, nil)
	if err != nil {
		t.Fatalf("term_read failed: %v", err)
	}
	trMap := trOut.(map[string]any)
	if !trMap["exited"].(bool) {
		t.Fatalf("expected exited=true from term_read, got %v", trMap)
	}
}
