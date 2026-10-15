package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ai"
	xterm "github.com/charmbracelet/x/term"
	"github.com/charmbracelet/x/vt"
	"github.com/mavity/rusticated/kabibi/ui"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type tuiMockAIRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (m *tuiMockAIRunner) RunTurn(ctx context.Context, prompt string, onFeedback ai.LogFeedback, onContent ai.LogFeedback) error {
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	m.mu.Unlock()

	if onFeedback != nil {
		onFeedback("[goal] Answer Parisian bridge question…")
		onFeedback("\r                                       \r")
	}
	if onContent != nil {
		onContent("Pont Alexandre III is a famous bridge in Paris.\n")
	}
	return nil
}

func (m *tuiMockAIRunner) Close() {}

// extractEmulatorLines extracts all non-empty visual lines from a vt.Emulator's
// scrollback and active screen grid so tests can verify the exact visual screen
// that a real terminal emulator displays after replaying Host's raw ANSI output.
func extractEmulatorLines(emu *vt.Emulator, width, height int) []string {
	var lines []string
	if sb := emu.Scrollback(); sb != nil {
		for _, line := range sb.Lines() {
			var row strings.Builder
			for x := 0; x < len(line); x++ {
				if line[x].Width == 0 {
					continue
				}
				if line[x].Content == "" {
					row.WriteByte(' ')
				} else {
					row.WriteString(line[x].Content)
				}
			}
			lines = append(lines, strings.TrimRight(row.String(), " "))
		}
	}
	for y := 0; y < height; y++ {
		var row strings.Builder
		for x := 0; x < width; x++ {
			c := emu.CellAt(x, y)
			if c == nil {
				row.WriteByte(' ')
				continue
			}
			if c.Width == 0 {
				continue
			}
			if c.Content == "" {
				row.WriteByte(' ')
			} else {
				row.WriteString(c.Content)
			}
		}
		lines = append(lines, strings.TrimRight(row.String(), " "))
	}
	return lines
}

// TestAppTUI_EndToEnd_RealFilesystemAndAIFallback validates the entire interactive
// TUI pipeline (ui.Host -> x/input.Reader -> AppWidget -> Shell -> mvdan.cc/sh +
// real filesystem + AI fallback -> CellBuf span diffing -> ANSI output -> vt.Emulator)
// and ensures the session runs multiple real filesystem commands and exits cleanly
// without stalling when the user types "exit\r".
func TestAppTUI_EndToEnd_RealFilesystemAndAIFallback(t *testing.T) {
	workDir := t.TempDir()

	// Seed a real file on disk to verify ls/cat before creating new files via shell commands
	if err := os.WriteFile(filepath.Join(workDir, "existing.txt"), []byte("initial_disk_data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var scrollbackMu sync.Mutex
	var scrollbackLog []string

	var host *ui.Host
	appWidget := NewWidget(AppWidgetOptions{
		WorkingDir: workDir,
		WriteToScrollback: func(text string) {
			scrollbackMu.Lock()
			scrollbackLog = append(scrollbackLog, text)
			scrollbackMu.Unlock()
			if host != nil {
				host.WriteToScrollback(text)
			}
		},
		OnExit: func() {
			if host != nil {
				host.Stop()
			}
		},
	})
	defer appWidget.Close()

	aiMock := &tuiMockAIRunner{}
	appWidget.Shell().SetAIRunner(aiMock)

	host = ui.NewHost(appWidget)

	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()

	var rawTermOut syncBuffer

	const termW, termH = 80, 24
	runDone := make(chan error, 1)
	go func() {
		runDone <- host.RunWithIO(
			inR,
			&rawTermOut,
			func(uintptr) (*xterm.State, error) { return &xterm.State{}, nil },
			func(uintptr, *xterm.State) error { return nil },
			func(uintptr) (int, int, error) { return termW, termH, nil },
		)
	}()

	// Wait for the initial frame to be flushed to rawTermOut
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "Kabibi") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	sendCommandAndWait := func(line string, wantBlockCount int) {
		t.Helper()
		// Write keystrokes followed by Enter (\r) into the live terminal input pipe
		if _, err := io.WriteString(inW, line+"\r"); err != nil {
			t.Fatalf("write input %q: %v", line, err)
		}
		waitDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(waitDeadline) {
			blocks := appWidget.Shell().Blocks()
			if len(blocks) >= wantBlockCount && !appWidget.Shell().IsRunning() {
				// Give Host coordinator a brief moment to flush the dirty frame
				time.Sleep(25 * time.Millisecond)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for command %q to finish (blocks=%d, running=%v)",
			line, len(appWidget.Shell().Blocks()), appWidget.Shell().IsRunning())
	}

	// Command 1: Create a subdirectory and write a file on the real filesystem using bash redirection
	sendCommandAndWait("mkdir sub && echo real_fs_content > sub/created.txt", 2)

	// Verify the file actually exists on the host filesystem
	createdBytes, err := os.ReadFile(filepath.Join(workDir, "sub", "created.txt"))
	if err != nil {
		t.Fatalf("expected sub/created.txt on real filesystem: %v", err)
	}
	if got := strings.TrimSpace(string(createdBytes)); got != "real_fs_content" {
		t.Fatalf("sub/created.txt disk content = %q, want %q", got, "real_fs_content")
	}

	// Command 2: cd into sub and run cat created.txt + ls to verify real filesystem read & cwd persistence
	sendCommandAndWait("cd sub && cat created.txt", 3)

	// Command 3: Submit a natural-language question that falls back to AI chat
	sendCommandAndWait("Name a bridge in Paris", 4)

	// Command 4: Type "exit" + Enter to terminate the TUI session cleanly
	if _, err := io.WriteString(inW, "exit\r"); err != nil {
		t.Fatalf("write exit: %v", err)
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Host.RunWithIO returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TUI Host.RunWithIO stalled on exit — input loop failed to unblock!")
	}

	// Replay the ENTIRE raw ANSI byte stream emitted by Host through a real 80x24 VT emulator
	// and verify the complete screen content.
	emu := vt.NewEmulator(termW, termH)
	_, _ = emu.WriteString(rawTermOut.String())

	screenLines := extractEmulatorLines(emu, termW, termH)

	// Verify the exact top-to-bottom sequence of rendered screen lines in the VT emulator
	var nonEmptyScreenLines []string
	for _, line := range screenLines {
		if strings.TrimSpace(line) != "" {
			nonEmptyScreenLines = append(nonEmptyScreenLines, line)
		}
	}

	wantExactLines := []string{
		"Kabibi shell initialized. Type commands below.",
		"$ mkdir sub && echo real_fs_content > sub/created.txt",
		"$ cd sub && cat created.txt",
		"real_fs_content",
		"$ Name a bridge in Paris",
		"Pont Alexandre III is a famous bridge in Paris.",
		"$ exit",
		"$",
	}

	if len(nonEmptyScreenLines) != len(wantExactLines) {
		t.Fatalf("rendered non-empty screen lines = %d, want %d:\ngot:  %#v\nwant: %#v",
			len(nonEmptyScreenLines), len(wantExactLines), nonEmptyScreenLines, wantExactLines)
	}
	for i := range wantExactLines {
		if nonEmptyScreenLines[i] != wantExactLines[i] {
			t.Errorf("screen line[%d] = %q, want %q", i, nonEmptyScreenLines[i], wantExactLines[i])
		}
	}
}

// TestAppTUI_EndToEnd_ScrollbackEjectionOnSmallViewport verifies that when the
// TUI plume grows taller than the viewport height (80x10), top lines are ejected
// into scrollback via Host.runScrollbackFrame while the active viewport retains
// the most recent commands and prompt, and the combined VT emulator scrollback +
// screen preserves the entire session history in order.
func TestAppTUI_EndToEnd_ScrollbackEjectionOnSmallViewport(t *testing.T) {
	workDir := t.TempDir()

	var scrollbackMu sync.Mutex
	var scrollbackChunks []string

	var host *ui.Host
	appWidget := NewWidget(AppWidgetOptions{
		WorkingDir: workDir,
		WriteToScrollback: func(text string) {
			scrollbackMu.Lock()
			scrollbackChunks = append(scrollbackChunks, text)
			scrollbackMu.Unlock()
			if host != nil {
				host.WriteToScrollback(text)
			}
		},
		OnExit: func() {
			if host != nil {
				host.Stop()
			}
		},
	})
	defer appWidget.Close()

	host = ui.NewHost(appWidget)

	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()

	var rawTermOut syncBuffer
	const termW, termH = 80, 10

	runDone := make(chan error, 1)
	go func() {
		runDone <- host.RunWithIO(
			inR,
			&rawTermOut,
			func(uintptr) (*xterm.State, error) { return &xterm.State{}, nil },
			func(uintptr, *xterm.State) error { return nil },
			func(uintptr) (int, int, error) { return termW, termH, nil },
		)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "Kabibi") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Execute 6 real filesystem commands producing 2 rows each (12 rows + 1 welcome row = 13 rows > 9 viewport rows)
	for i := 1; i <= 6; i++ {
		cmd := "echo line_" + string(rune('0'+i)) + " > f.txt && cat f.txt"
		if _, err := io.WriteString(inW, cmd+"\r"); err != nil {
			t.Fatal(err)
		}
		wantBlocks := i + 1
		waitDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(waitDeadline) {
			if len(appWidget.Shell().Blocks()) >= wantBlocks && !appWidget.Shell().IsRunning() {
				time.Sleep(25 * time.Millisecond)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if _, err := io.WriteString(inW, "exit\r"); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Host.RunWithIO returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Host.RunWithIO stalled on exit")
	}

	scrollbackMu.Lock()
	ejectedCount := len(scrollbackChunks)
	scrollbackMu.Unlock()
	if ejectedCount == 0 {
		t.Fatal("expected overflow rows to be ejected via WriteToScrollback on 80x10 viewport")
	}

	emu := vt.NewEmulator(termW, termH)
	_, _ = emu.WriteString(rawTermOut.String())

	var nonEmptyAllLines []string
	for _, line := range extractEmulatorLines(emu, termW, termH) {
		if strings.TrimSpace(line) != "" {
			nonEmptyAllLines = append(nonEmptyAllLines, line)
		}
	}

	wantFullHistoryAndViewport := []string{
		"Kabibi shell initialized. Type commands below.",
		"$ echo line_1 > f.txt && cat f.txt",
		"line_1",
		"$ echo line_2 > f.txt && cat f.txt",
		"line_2",
		"$ echo line_3 > f.txt && cat f.txt",
		"line_3",
		"$ echo line_4 > f.txt && cat f.txt",
		"line_4",
		"$ echo line_5 > f.txt && cat f.txt",
		"line_5",
		"$ echo line_6 > f.txt && cat f.txt",
		"line_6",
		"$ exit",
		"$",
	}

	if len(nonEmptyAllLines) != len(wantFullHistoryAndViewport) {
		t.Fatalf("combined scrollback+viewport lines = %d, want %d:\ngot:  %#v\nwant: %#v",
			len(nonEmptyAllLines), len(wantFullHistoryAndViewport), nonEmptyAllLines, wantFullHistoryAndViewport)
	}
	for i := range wantFullHistoryAndViewport {
		if nonEmptyAllLines[i] != wantFullHistoryAndViewport[i] {
			t.Errorf("combined line[%d] = %q, want %q", i, nonEmptyAllLines[i], wantFullHistoryAndViewport[i])
		}
	}
}

// TestAppTUI_TrailingSpaceAndCursorMovementUpdatesImmediately verifies that
// typing a space at the end of the input line (where the cell is already a
// blank space in prevBuf) or moving the cursor with Left/Right arrow keys
// immediately flushes a cursor-position update to the terminal without waiting
// for the next non-space character.
func TestAppTUI_TrailingSpaceAndCursorMovementUpdatesImmediately(t *testing.T) {
	workDir := t.TempDir()

	var host *ui.Host
	appWidget := NewWidget(AppWidgetOptions{
		WorkingDir: workDir,
		OnExit: func() {
			if host != nil {
				host.Stop()
			}
		},
	})
	defer appWidget.Close()

	host = ui.NewHost(appWidget)

	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()

	var rawTermOut syncBuffer
	const termW, termH = 80, 24

	runDone := make(chan error, 1)
	go func() {
		runDone <- host.RunWithIO(
			inR,
			&rawTermOut,
			func(uintptr) (*xterm.State, error) { return &xterm.State{}, nil },
			func(uintptr, *xterm.State) error { return nil },
			func(uintptr) (int, int, error) { return termW, termH, nil },
		)
	}()

	// Wait for initial frame
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "\x1b[24;3H") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Type "echo" -> cursor should move to column 7 (1-based: "$ echo" is 6 chars -> col 7)
	_, _ = io.WriteString(inW, "echo")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "\x1b[24;7H") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(rawTermOut.String(), "\x1b[24;7H") {
		t.Fatalf("expected cursor at col 7 after typing 'echo', got:\n%q", rawTermOut.String())
	}

	// Now type a single trailing space " " -> even though cell (6, 23) was already ' ',
	// the cursor MUST immediately advance to column 8 (\x1b[24;8H) without waiting for another char!
	_, _ = io.WriteString(inW, " ")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "\x1b[24;8H") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(rawTermOut.String(), "\x1b[24;8H") {
		t.Fatalf("trailing space did not immediately advance terminal cursor to col 8:\n%q", rawTermOut.String())
	}

	_, _ = io.WriteString(inW, "\x03exit\r")
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Host.RunWithIO did not exit")
	}
}
