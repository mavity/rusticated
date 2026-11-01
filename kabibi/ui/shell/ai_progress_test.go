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

func newTestListFilesProgressOutput(state *liveOutputState, captured *bytes.Buffer, width, maxRows int) *listFilesProgressOutput {
	writer := writeFunc(func(p []byte) (int, error) {
		n, err := captured.Write(p)
		if n > 0 {
			state.enqueue(p[:n])
		}
		return n, err
	})
	return newListFilesProgressOutput(writer, width, maxRows)
}

func publishTestProgress(t *testing.T, state *liveOutputState, output *listFilesProgressOutput, progress *filelist.Progress) {
	t.Helper()
	if err := output.publish(progress); err != nil {
		t.Fatalf("publish progress: %v", err)
	}
	if err := state.drain(false); err != nil {
		t.Fatalf("drain progress output: %v", err)
	}
}

func TestListFilesProgressUsesCommandANSIOutputAndPlumeEjection(t *testing.T) {
	var ansiOutput bytes.Buffer
	var scrollback strings.Builder
	state := newLiveOutputState(64, 20)
	output := newTestListFilesProgressOutput(state, &ansiOutput, 64, 6)
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
	publishTestProgress(t, state, output, progress)

	s := &Shell{
		mode:        ModeStack,
		input:       NewPromptInput(PromptInputOptions{}),
		running:     true,
		activeBlock: 0,
		width:       64,
		height:      6,
		opts:        ShellOptions{WriteToScrollback: func(text string) { scrollback.WriteString(text) }},
		blocks: []CommandBlock{{
			Kind:       BlockCommand,
			Source:     SourceUserShell,
			Prompt:     "$ ",
			Input:      "ai list-files",
			Status:     StatusActive,
			liveOutput: state,
		}},
	}
	buf := renderBlockCached(&s.blocks[0], 64)
	if buf.Height != 8 {
		t.Fatalf("command block height = %d, want prompt, six progress rows, and its terminal cursor row", buf.Height)
	}

	progress.Viewed++
	progress.CurrentPath = "src/latest.go"
	publishTestProgress(t, state, output, progress)
	buf = renderBlockCached(&s.blocks[0], 64)
	var renderedBlock strings.Builder
	for _, cell := range buf.Cells() {
		renderedBlock.WriteString(cell.Grapheme)
	}
	if !strings.Contains(renderedBlock.String(), "Matching: src/latest.go") {
		t.Fatalf("command block did not render the latest ANSI progress frame: %q", renderedBlock.String())
	}
	if strings.Contains(renderedBlock.String(), "Matching: src/current.go") {
		t.Fatalf("ANSI redraw left stale progress in the command block: %q", renderedBlock.String())
	}
	screen, _ := renderANSIForTest(t, s.Render(ui.Rect{W: 64, H: 6}, ui.RenderContext{}), 64, 6)
	var screenText strings.Builder
	for _, cell := range screen.Cells() {
		screenText.WriteString(cell.Grapheme)
	}
	for _, want := range []string{"src/fourth.go", "viewed 43/100"} {
		if !strings.Contains(screenText.String(), want) {
			t.Errorf("shell render %q does not contain %q", screenText.String(), want)
		}
	}
	if !strings.Contains(ansiOutput.String(), "\x1b[2K") || !strings.Contains(ansiOutput.String(), "\x1b[6A") {
		t.Fatalf("progress output did not use ANSI line clearing and cursor movement: %q", ansiOutput.String())
	}
	if scrollback.Len() == 0 {
		t.Fatal("normal block rendering did not eject progress into scrollback")
	}
}

func TestListFilesProgressQuoteLimitTracksViewport(t *testing.T) {
	if got, want := listFilesProgressMatchLimit(24-reservedFooterRows), 24-reservedFooterRows-4; got != want {
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
	state := newLiveOutputState(80, 23)
	var captured bytes.Buffer
	output := newTestListFilesProgressOutput(state, &captured, 80, 22)
	publishTestProgress(t, state, output, &filelist.Progress{
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
			Kind:       BlockCommand,
			Source:     SourceUserShell,
			Prompt:     "$ ",
			Input:      "ai list-files --query image",
			Status:     StatusError,
			liveOutput: state,
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
	state := newLiveOutputState(80, 23)
	var captured bytes.Buffer
	output := newTestListFilesProgressOutput(state, &captured, 80, 22)
	publishTestProgress(t, state, output, &filelist.Progress{
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
			Kind:       BlockCommand,
			Source:     SourceUserShell,
			Prompt:     "$ ",
			Input:      "ai list-files --query image",
			Status:     StatusError,
			liveOutput: state,
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

type cancelingProgressWriter struct {
	cancel context.CancelFunc
	writes [][]byte
}

func (w *cancelingProgressWriter) Write(p []byte) (int, error) {
	copy := append([]byte(nil), p...)
	w.writes = append(w.writes, copy)
	if len(w.writes) == 1 {
		w.cancel()
	}
	return len(p), nil
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
	state := newLiveOutputState(80, 19)
	var progressOutput bytes.Buffer
	progressWriter := writeFunc(func(p []byte) (int, error) {
		n, err := progressOutput.Write(p)
		if n > 0 {
			state.enqueue(p[:n])
		}
		return n, err
	})

	result, err := executeInteractiveListFiles(context.Background(), execSess,
		[]string{"--query", "unrelated semantic phrase", "--max-results", "1"},
		&commandTestEmbedder{},
		progressWriter,
		80,
		19,
	)
	if err != nil {
		t.Fatalf("executeInteractiveListFiles(): %v", err)
	}
	if !strings.Contains(result, "1.00  browser_worker.go") || !strings.Contains(result, "Search stats: processed 1/1 files; about ") {
		t.Fatalf("semantic result is missing the leading score or final stats: %q", result)
	}
	if got := progressOutput.String(); !strings.Contains(got, "func startWorker() {}") || !strings.Contains(got, "\x1b[2K") {
		t.Fatalf("interactive path did not write quote progress through ANSI stdout: %q", got)
	}
	if got := progressOutput.String(); !strings.Contains(got, "\x1b[3A\r\x1b[2K") {
		t.Fatalf("successful search did not clear its progress rows before final output: %q", got)
	}
	renderedResult, err := renderAssistantMarkdown(result, 80)
	if err != nil {
		t.Fatalf("renderAssistantMarkdown(): %v", err)
	}
	state.enqueue([]byte(renderedResult))
	if err := state.drain(false); err != nil {
		t.Fatalf("drain final command output: %v", err)
	}
	block := CommandBlock{
		Kind:       BlockCommand,
		Source:     SourceUserShell,
		Prompt:     "$ ",
		Input:      "ai list-files",
		Status:     StatusSuccess,
		liveOutput: state,
	}
	buf := renderBlockCached(&block, 80)
	var blockText strings.Builder
	for _, cell := range buf.Cells() {
		blockText.WriteString(cell.Grapheme)
	}
	if !strings.Contains(blockText.String(), "browser_worker.go") || strings.Contains(blockText.String(), "Preparing query embedding") {
		t.Fatalf("final result did not replace the progress frame through normal terminal output: %q", blockText.String())
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
	writer := &cancelingProgressWriter{cancel: cancel}

	_, err = executeInteractiveListFiles(ctx, execSess,
		[]string{"--query", "unrelated semantic phrase", "--max-results", "1"},
		&commandTestEmbedder{},
		writer,
		80,
		19,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeInteractiveListFiles() error = %v, want context cancellation", err)
	}
	if len(writer.writes) == 0 {
		t.Fatal("cancellation did not publish progress to stdout")
	}
	if lastWrite := string(writer.writes[len(writer.writes)-1]); !strings.Contains(lastWrite, "Preparing query embedding") &&
		!strings.Contains(lastWrite, "Matching:") && !strings.Contains(lastWrite, "Finding quote:") {
		t.Fatalf("canceled command cleared instead of retaining the last progress frame: %q", lastWrite)
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
	var progressOutput bytes.Buffer

	result, err := executeInteractiveListFiles(context.Background(), execSess,
		[]string{"--query", "*", "--max-results", "10"},
		nil,
		&progressOutput,
		80,
		8,
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
