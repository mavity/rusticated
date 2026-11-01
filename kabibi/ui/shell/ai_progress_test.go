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
			{Path: "src/first.go", Size: 2048, Score: 0.91, HasScore: true, Quote: "image resize line"},
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
	if maxRows != 2+len(progress.Matches) {
		t.Fatalf("progress uses %d rows with %d matches in a 24-row viewport, want all matches and two status rows", maxRows, len(progress.Matches))
	}
	manyMatches := *gotProgress
	manyMatches.Matches = make([]filelist.ProgressMatch, 50)
	if got := listFilesProgressRows(&manyMatches, 24); got != 24-reservedFooterRows {
		t.Fatalf("progress uses %d rows for a 50-match list in a 24-row viewport, want all %d available rows", got, 24-reservedFooterRows)
	}
	if got := listFilesProgressRows(&manyMatches, 8); got != 8-reservedFooterRows {
		t.Fatalf("progress uses %d rows in an 8-row viewport, want all %d available rows", got, 8-reservedFooterRows)
	}
	buf := renderListFilesProgress(gotProgress, 64, maxRows)
	if buf.Height > maxRows {
		t.Fatalf("rendered progress height = %d, max rows = %d", buf.Height, maxRows)
	}
	var rendered strings.Builder
	for _, cell := range buf.Cells() {
		rendered.WriteString(cell.Grapheme)
	}
	for _, want := range []string{"viewed 42/100", "0.91  src/first.go", `"image resize line"`, "Matching:"} {
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

func TestListFilesProgressQuoteLimitTracksViewport(t *testing.T) {
	s := &Shell{height: 24}
	if got, want := s.ListFilesProgressMatchLimit(), 24-reservedFooterRows-2; got != want {
		t.Fatalf("progress match limit = %d, want %d", got, want)
	}
	if got, want := s.ListFilesOutputMatchLimit(), 24-reservedFooterRows-3-3; got != want {
		t.Fatalf("output match limit = %d, want %d", got, want)
	}
	s.height = 2
	if got := s.ListFilesProgressMatchLimit(); got != 0 {
		t.Fatalf("progress match limit in a 2-row viewport = %d, want 0", got)
	}
	if got := s.ListFilesOutputMatchLimit(); got != 0 {
		t.Fatalf("output match limit in a 2-row viewport = %d, want 0", got)
	}
}

type recordingListFilesProgressSink struct {
	progress         []*filelist.Progress
	cleared          int
	outputMatchLimit int
}

func (s *recordingListFilesProgressSink) OnAICommandStart(string, string) (func([]byte), func(int, time.Duration)) {
	return nil, nil
}

func (s *recordingListFilesProgressSink) ListFilesProgressMatchLimit() int {
	return 1
}

func (s *recordingListFilesProgressSink) ListFilesOutputMatchLimit() int {
	return s.outputMatchLimit
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
	sink := &recordingListFilesProgressSink{outputMatchLimit: 10}
	execSess.SetAICommandUISink(sink)

	result, err := executeInteractiveListFiles(context.Background(), execSess,
		[]string{"--query", "unrelated semantic phrase", "--max-results", "1"},
		&commandTestEmbedder{},
		80,
	)
	if err != nil {
		t.Fatalf("executeInteractiveListFiles(): %v", err)
	}
	if !strings.Contains(result, "1.00  browser_worker.go") || !strings.Contains(result, "Search stats: processed 1/1 files; about ") {
		t.Fatalf("semantic result is missing the leading score or final stats: %q", result)
	}
	if len(sink.progress) == 0 || sink.progress[0].Phase == "" {
		t.Fatalf("interactive path did not publish progress: %#v", sink.progress)
	}
	lastProgress := sink.progress[len(sink.progress)-1]
	if len(lastProgress.Matches) == 0 || lastProgress.Matches[0].Quote != "func startWorker() {}" {
		t.Fatalf("final throttled progress did not retain the completed quote: %#v", lastProgress)
	}
	if sink.cleared != 1 {
		t.Fatalf("progress clear calls = %d, want one", sink.cleared)
	}
}

func TestInteractiveListFilesCapsResultsToOutputFrame(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()
	sink := &recordingListFilesProgressSink{outputMatchLimit: 2}
	execSess.SetAICommandUISink(sink)

	result, err := executeInteractiveListFiles(context.Background(), execSess,
		[]string{"--query", "*", "--max-results", "10"},
		nil,
		80,
	)
	if err != nil {
		t.Fatalf("executeInteractiveListFiles(): %v", err)
	}
	matchCount := 0
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if strings.Contains(result, name) {
			matchCount++
		}
	}
	if matchCount != 2 {
		t.Fatalf("result contains %d matches, want output-frame limit of 2: %q", matchCount, result)
	}
}
