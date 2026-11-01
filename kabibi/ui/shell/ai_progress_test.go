package shell

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mavity/rusticated/kabibi/tools/filelist"
	"github.com/mavity/rusticated/kabibi/ui"
)

func TestListFilesProgressIsEphemeralAndBounded(t *testing.T) {
	var scrollback strings.Builder
	var commandOutput bytes.Buffer
	for i := 0; i < 30; i++ {
		commandOutput.WriteString("normal command output\n")
	}
	s := &Shell{
		mode:        ModeStack,
		input:       NewPromptInput(PromptInputOptions{}),
		running:     true,
		activeBlock: 0,
		opts:        ShellOptions{WriteToScrollback: func(text string) { scrollback.WriteString(text) }},
		blocks: []CommandBlock{{
			Kind:   BlockCommand,
			Source: SourceUserShell,
			Prompt: "$ ",
			Input:  "ai list-files",
			Output: commandOutput,
			Status: StatusActive,
		}},
	}
	outputBefore := s.blocks[0].Output.String()
	progress := &filelist.Progress{
		Phase:       "candidate",
		Viewed:      42,
		Total:       100,
		CurrentPath: "src/current.go",
		AvgPerFile:  13 * time.Millisecond,
		Matches: []filelist.ProgressMatch{
			{Path: "src/first.go", Score: 0.91},
			{Path: "src/second.go", Score: 0.78},
			{Path: "src/third.go", Score: 0.66},
		},
	}

	s.SetListFilesProgress(progress)

	s.mu.Lock()
	gotOutput := s.blocks[0].Output.String()
	gotProgress := cloneListFilesProgress(s.blocks[0].listFilesProgress)
	s.mu.Unlock()
	if gotOutput != outputBefore {
		t.Fatalf("progress changed command output to %q", gotOutput)
	}
	if gotProgress == nil || len(gotProgress.Matches) != len(progress.Matches) {
		t.Fatalf("stored progress = %#v", gotProgress)
	}

	maxRows := listFilesProgressRows(gotProgress, 24)
	if maxRows > 8 {
		t.Fatalf("progress uses %d rows in a 24-row viewport, want at most 8", maxRows)
	}
	buf := renderListFilesProgress(gotProgress, 64, maxRows)
	if buf.Height > maxRows {
		t.Fatalf("rendered progress height = %d, max rows = %d", buf.Height, maxRows)
	}
	var rendered strings.Builder
	for _, cell := range buf.Cells() {
		rendered.WriteString(cell.Grapheme)
	}
	for _, want := range []string{"viewed 42/100", "src/first.go", "Matching:"} {
		if !strings.Contains(rendered.String(), want) {
			t.Errorf("rendered progress %q does not contain %q", rendered.String(), want)
		}
	}

	screen, _ := renderANSIForTest(t, s.Render(ui.Rect{W: 64, H: 24}, ui.RenderContext{}), 64, 24)
	var screenText strings.Builder
	for _, cell := range screen.Cells() {
		screenText.WriteString(cell.Grapheme)
	}
	for _, want := range []string{"src/first.go", "Matching:", "normal command output"} {
		if !strings.Contains(screenText.String(), want) {
			t.Errorf("shell render %q does not contain %q", screenText.String(), want)
		}
	}
	if strings.Contains(scrollback.String(), "src/first.go") {
		t.Fatal("transient search progress was written to scrollback")
	}

	s.SetListFilesProgress(nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocks[0].listFilesProgress != nil {
		t.Fatal("progress was not cleared")
	}
}

type recordingListFilesProgressSink struct {
	progress []*filelist.Progress
	cleared  int
}

func (s *recordingListFilesProgressSink) OnAICommandStart(string, string) (func([]byte), func(int, time.Duration)) {
	return nil, nil
}

func (s *recordingListFilesProgressSink) SetListFilesProgress(progress *filelist.Progress) {
	if progress == nil {
		s.cleared++
		return
	}
	copy := *progress
	copy.Matches = append([]filelist.ProgressMatch(nil), progress.Matches...)
	s.progress = append(s.progress, &copy)
}

func TestInteractiveListFilesPublishesAndClearsProgress(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "browser_worker.go"), []byte("func startWorker() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()
	sink := &recordingListFilesProgressSink{}
	execSess.SetAICommandUISink(sink)

	result, err := executeInteractiveListFiles(context.Background(), execSess,
		[]string{"--query", "unrelated semantic phrase", "--max-results", "1"},
		&commandTestEmbedder{},
	)
	if err != nil {
		t.Fatalf("executeInteractiveListFiles(): %v", err)
	}
	if !strings.Contains(result, "[semantic: 1.00]") {
		t.Fatalf("semantic result is missing its score: %q", result)
	}
	if len(sink.progress) == 0 || sink.progress[0].Phase == "" {
		t.Fatalf("interactive path did not publish progress: %#v", sink.progress)
	}
	if sink.cleared != 1 {
		t.Fatalf("progress clear calls = %d, want one", sink.cleared)
	}
}
