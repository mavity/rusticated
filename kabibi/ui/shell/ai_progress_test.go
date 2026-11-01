package shell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/tools/filelist"
	"github.com/mavity/rusticated/kabibi/ui"
)

func TestListFilesProgressUsesCommandBlockOutputAndPlumeEjection(t *testing.T) {
	var scrollback strings.Builder
	commandOutput := bytes.NewBufferString("normal command output\n")
	outputRenderer := newListFilesCommandOutput(6)
	s := &Shell{
		mode:        ModeStack,
		input:       NewPromptInput(PromptInputOptions{}),
		running:     true,
		activeBlock: 0,
		width:       64,
		height:      6,
		opts:        ShellOptions{WriteToScrollback: func(text string) { scrollback.WriteString(text) }},
		blocks: []CommandBlock{{
			Kind:   BlockCommand,
			Source: SourceUserShell,
			Prompt: "$ ",
			Input:  "ai list-files",
			Output: *commandOutput,
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
			{Path: "src/fourth.go", Score: 0.54},
		},
	}

	s.SetActiveCommandOutputRenderer(outputRenderer)
	outputRenderer.setProgress(progress)

	s.mu.Lock()
	gotOutput := s.blocks[0].Output.String()
	gotRenderer := s.blocks[0].outputRenderer
	s.mu.Unlock()
	if gotOutput != outputBefore {
		t.Fatalf("progress changed command output to %q", gotOutput)
	}
	if gotRenderer != outputRenderer {
		t.Fatal("command block did not retain the command-owned output renderer")
	}

	buf := renderBlockCached(&s.blocks[0], 64)
	if buf.Height != 8 {
		t.Fatalf("command block height = %d, want command, six progress rows, and output", buf.Height)
	}
	progress.Viewed++
	outputRenderer.setProgress(progress)
	s.InvalidateActiveCommandOutput()
	buf = renderBlockCached(&s.blocks[0], 64)
	var renderedBlock strings.Builder
	for _, cell := range buf.Cells() {
		renderedBlock.WriteString(cell.Grapheme)
	}
	if !strings.Contains(renderedBlock.String(), "viewed 43/100") {
		t.Fatalf("rendered command block did not use latest progress: %q", renderedBlock.String())
	}

	screen, _ := renderANSIForTest(t, s.Render(ui.Rect{W: 64, H: 6}, ui.RenderContext{}), 64, 6)
	var screenText strings.Builder
	for _, cell := range screen.Cells() {
		screenText.WriteString(cell.Grapheme)
	}
	for _, want := range []string{"src/fourth.go", "Matching: src/current.go", "normal command output"} {
		if !strings.Contains(screenText.String(), want) {
			t.Errorf("shell render %q does not contain %q", screenText.String(), want)
		}
	}
	for _, want := range []string{"Top 4 matches", "viewed 43/100", "src/first.go"} {
		if !strings.Contains(scrollback.String(), want) {
			t.Errorf("plume scrollback %q does not contain ejected progress %q", scrollback.String(), want)
		}
	}

	if !strings.Contains(s.Blocks()[0].Output.String(), "viewed 43/100") {
		t.Fatal("command block snapshot omitted the command-owned rendered output")
	}
}

func TestListFilesProgressQuoteLimitTracksViewport(t *testing.T) {
	if got, want := listFilesProgressMatchLimit(24-reservedFooterRows), 24-reservedFooterRows-2; got != want {
		t.Fatalf("progress match limit = %d, want %d", got, want)
	}
	if got, want := listFilesOutputMatchLimit(24-reservedFooterRows), 24-reservedFooterRows-3-3; got != want {
		t.Fatalf("output match limit = %d, want %d", got, want)
	}
	if got := listFilesProgressMatchLimit(1); got != 0 {
		t.Fatalf("progress match limit in a 2-row viewport = %d, want 0", got)
	}
	if got := listFilesOutputMatchLimit(1); got != 0 {
		t.Fatalf("output match limit in a 2-row viewport = %d, want 0", got)
	}
}

func TestCanceledListFilesProgressRemainsVisibleWhenCommandStops(t *testing.T) {
	outputRenderer := newListFilesCommandOutput(24)
	outputRenderer.setProgress(&filelist.Progress{
		Phase:       "candidate",
		Viewed:      42,
		Total:       100,
		CurrentPath: "src/current.go",
		Matches: []filelist.ProgressMatch{
			{Path: "src/image.go", Size: 572, Score: 0.91, HasScore: true},
		},
	})
	s := &Shell{
		mode:        ModeStack,
		input:       NewPromptInput(PromptInputOptions{}),
		width:       80,
		height:      24,
		activeBlock: -1,
		blocks: []CommandBlock{{
			Kind:           BlockCommand,
			Source:         SourceUserShell,
			Prompt:         "$ ",
			Input:          "ai list-files --query image",
			Status:         StatusError,
			outputRenderer: outputRenderer,
		}},
	}

	screen, _ := renderANSIForTest(t, s.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{}), 80, 24)
	var screenText strings.Builder
	for _, cell := range screen.Cells() {
		screenText.WriteString(cell.Grapheme)
	}
	for _, want := range []string{"src/image.go", "viewed 42/100", "Matching: src/current.go"} {
		if !strings.Contains(screenText.String(), want) {
			t.Errorf("retained progress render %q does not contain %q", screenText.String(), want)
		}
	}
}

func TestCanceledListFilesProgressUsesNormalBlockLayoutInBottomAlignedMode(t *testing.T) {
	outputRenderer := newListFilesCommandOutput(24)
	outputRenderer.setProgress(&filelist.Progress{
		Phase:       "candidate",
		Viewed:      42,
		Total:       100,
		CurrentPath: "src/current.go",
		Matches: []filelist.ProgressMatch{
			{Path: "src/image.go", Size: 572, Score: 0.91, HasScore: true},
		},
	})
	s := &Shell{
		mode:          ModeStack,
		bottomAligned: true,
		input:         NewPromptInput(PromptInputOptions{}),
		width:         80,
		height:        24,
		activeBlock:   -1,
		blocks: []CommandBlock{{
			Kind:           BlockCommand,
			Source:         SourceUserShell,
			Prompt:         "$ ",
			Input:          "ai list-files --query image",
			Status:         StatusError,
			outputRenderer: outputRenderer,
		}},
	}
	s.SetInput("next command")

	screen, cursor := renderANSIForTest(t, s.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{Focused: true}), 80, 24)
	if !cursor.Visible {
		t.Fatal("prompt cursor is not visible in bottom-aligned mode")
	}
	if want := 24 - reservedFooterRows - 1; cursor.Y != want {
		t.Fatalf("prompt cursor row = %d, want %d below retained progress", cursor.Y, want)
	}
	var screenText strings.Builder
	for _, cell := range screen.Cells() {
		screenText.WriteString(cell.Grapheme)
	}
	for _, want := range []string{"viewed 42/100", "src/image.go", "next command"} {
		if !strings.Contains(screenText.String(), want) {
			t.Errorf("bottom-aligned render %q does not contain %q", screenText.String(), want)
		}
	}
}

func TestEscapeCancelsActiveListFilesCommand(t *testing.T) {
	canceled := false
	s := &Shell{
		running:     true,
		activeBlock: 0,
		activeCancel: func() {
			canceled = true
		},
		blocks: []CommandBlock{{Input: `ai list-files --query "image encoding"`}},
	}

	if !s.handleKeyEvent(input.KeyPressEvent{Code: input.KeyEscape}) {
		t.Fatal("Escape was not handled for active ai list-files command")
	}
	if !canceled {
		t.Fatal("Escape did not cancel the active ai list-files command")
	}
}

type recordingCommandOutputSink struct {
	outputRenderer  CommandBlockOutputRenderer
	lastRenderer    *listFilesCommandOutput
	cleared         int
	invalidations   int
	width           int
	availableHeight int
	onProgress      func()
}

func (s *recordingCommandOutputSink) OnAICommandStart(string, string) (func([]byte), func(int, time.Duration)) {
	return nil, nil
}

func (s *recordingCommandOutputSink) SetActiveCommandOutputRenderer(renderer CommandBlockOutputRenderer) {
	s.outputRenderer = renderer
	if renderer == nil {
		s.cleared++
		return
	}
	if listFilesRenderer, ok := renderer.(*listFilesCommandOutput); ok {
		s.lastRenderer = listFilesRenderer
	}
}

func (s *recordingCommandOutputSink) InvalidateActiveCommandOutput() {
	s.invalidations++
	if s.onProgress != nil {
		onProgress := s.onProgress
		s.onProgress = nil
		onProgress()
	}
}

func (s *recordingCommandOutputSink) CommandOutputSize() (int, int) {
	return s.width, s.availableHeight
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
	sink := &recordingCommandOutputSink{width: 80, availableHeight: 19}
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
	if sink.invalidations == 0 {
		t.Fatal("interactive path did not invalidate its command output")
	}
	if sink.lastRenderer == nil {
		t.Fatal("interactive path did not install a list-files output renderer")
	}
	if rendered := sink.lastRenderer.Render(80); !strings.Contains(rendered, "func startWorker() {}") {
		t.Fatalf("final rendered progress did not retain the completed quote: %q", rendered)
	}
	if sink.cleared != 1 {
		t.Fatalf("output renderer clear calls = %d, want one", sink.cleared)
	}
}

func TestInteractiveListFilesDoesNotClearProgressOnCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "browser_worker.go"), []byte("func startWorker() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()
	ctx, cancel := context.WithCancel(context.Background())
	sink := &recordingCommandOutputSink{
		width:           80,
		availableHeight: 19,
		onProgress:      cancel,
	}
	execSess.SetAICommandUISink(sink)

	_, err = executeInteractiveListFiles(ctx, execSess,
		[]string{"--query", "unrelated semantic phrase", "--max-results", "1"},
		&commandTestEmbedder{},
		80,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeInteractiveListFiles() error = %v, want context cancellation", err)
	}
	if sink.invalidations == 0 || sink.lastRenderer == nil {
		t.Fatal("cancellation did not publish a renderable progress snapshot")
	}
	if sink.cleared != 0 {
		t.Fatalf("cancellation cleared output %d times, want it retained", sink.cleared)
	}
	if got := sink.lastRenderer.Render(80); got == "" {
		t.Fatal("canceled command did not retain visible renderer output")
	}
	cancel()
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
	sink := &recordingCommandOutputSink{width: 80, availableHeight: 8}
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
