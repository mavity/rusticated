package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ai"
	"github.com/charmbracelet/x/input"
	xterm "github.com/charmbracelet/x/term"
	"github.com/charmbracelet/x/vt"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/mavity/rusticated/kabibi/internal/gitfs"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

func initAppTestRepository(t *testing.T, root string) *git.Repository {
	t.Helper()

	worktreeFS := gitfs.New(root)
	dotGitFS, err := worktreeFS.Chroot(git.GitDirName)
	if err != nil {
		t.Fatalf("create test repository storage filesystem: %v", err)
	}
	storer := filesystem.NewStorage(dotGitFS, cache.NewObjectLRUDefault())
	repo, err := git.Init(storer, worktreeFS)
	if err != nil {
		t.Fatalf("initialize test repository: %v", err)
	}
	return repo
}

func TestPanelsHideDuringCommandAndRestorePreviousVisibility(t *testing.T) {
	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir(), PanelsVisible: true})
	defer widget.Close()

	commandDone := make(chan struct{})
	go func() {
		widget.Shell().ExecuteLine("sleep 0.2")
		close(commandDone)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for widget.PanelsVisible() && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}
	if widget.PanelsVisible() {
		t.Fatal("panels remained visible after command start")
	}
	if widget.Shell().BottomAligned() {
		t.Fatal("shell remained bottom-aligned while panels were hidden")
	}

	select {
	case <-commandDone:
	case <-time.After(5 * time.Second):
		t.Fatal("command did not finish")
	}
	deadline = time.Now().Add(5 * time.Second)
	for !widget.PanelsVisible() && time.Now().Before(deadline) {
		time.Sleep(1 * time.Millisecond)
	}
	if !widget.PanelsVisible() || !widget.Shell().BottomAligned() {
		t.Fatal("panels were not restored after command completion")
	}

	widget.SetPanelsVisible(false)
	commandDone = make(chan struct{})
	go func() {
		widget.Shell().ExecuteLine("sleep 0.2")
		close(commandDone)
	}()
	select {
	case <-commandDone:
	case <-time.After(5 * time.Second):
		t.Fatal("command with hidden panels did not finish")
	}
	if widget.PanelsVisible() || widget.Shell().BottomAligned() {
		t.Fatal("command changed panel visibility that was initially hidden")
	}
}

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

func (m *tuiMockAIRunner) RunTurn(ctx context.Context, prompt string, onFeedback, onToolFeedback, onContent ai.LogFeedback) error {
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
	appWidget.Shell().SetPrompt("፧")

	host = ui.NewHost(appWidget, false)

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

	// Initial stack height: 3 banner rows + 1 input row + 1 blank footer row = 5 rows.
	const initialStackH = 5
	if got := appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}); got.H != initialStackH {
		t.Fatalf("initial Measure() = %#v, want H=%d", got, initialStackH)
	}

	// Verify wrapped multi-line input in ModeStack: type a command longer than 80 columns
	// ("echo " + 85 'z' chars = 90 chars + prompt chars -> 2 visual input rows),
	// verify the Shell stack grows by 1 row (to initialStackH + 1 = 6 rows),
	// then delete it all with Ctrl+C and verify it shrinks back to initialStackH (5 rows),
	// opening a 1-row slack gap at the bottom that gets closed before any upward ejection.
	longPreview := "echo " + strings.Repeat("z", 85)
	if _, err := io.WriteString(inW, longPreview); err != nil {
		t.Fatalf("write long preview: %v", err)
	}
	waitWrapDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(waitWrapDeadline) {
		if appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}).H > initialStackH {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	wrappedH := appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}).H
	if wrappedH <= initialStackH {
		t.Fatalf("wrapped Measure().H = %d, want > %d", wrappedH, initialStackH)
	}
	// Clear wrapped input with Ctrl+C (\x03) so it shrinks back to initialStackH and clears the slack row
	if _, err := io.WriteString(inW, "\x03"); err != nil {
		t.Fatalf("write ctrl+c: %v", err)
	}
	waitShrinkDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(waitShrinkDeadline) {
		if appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}).H == initialStackH && appWidget.Shell().Input().Value() == "" {
			time.Sleep(25 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}); got.H != initialStackH {
		t.Fatalf("shrunk Measure() after Ctrl+C = %#v, want H=%d", got, initialStackH)
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

	// Verify the ordered substrings of rendered screen lines in the VT emulator
	wantLineSubstrings := []string{
		"KABIBI // RUSTICATED COMPUTING SUPERVISOR",
		"64K CELL MATRIX OK",
		"Kabibi shell initialized. Type commands below.",
		"፧mkdir sub && echo real_fs_content > sub/created.txt",
		"፧cd sub && cat created.txt",
		"real_fs_content",
		"፧Name a bridge in Paris",
		"Pont Alexandre III is a famous bridge in Paris.",
		"፧exit",
	}

	if len(nonEmptyScreenLines) != len(wantLineSubstrings) {
		t.Fatalf("rendered non-empty screen lines = %d, want %d:\ngot:  %#v\nwant: %#v",
			len(nonEmptyScreenLines), len(wantLineSubstrings), nonEmptyScreenLines, wantLineSubstrings)
	}
	for i := range wantLineSubstrings {
		if !strings.Contains(nonEmptyScreenLines[i], wantLineSubstrings[i]) {
			t.Errorf("screen line[%d] = %q, want substring %q", i, nonEmptyScreenLines[i], wantLineSubstrings[i])
		}
	}

	// Verify there are no vertical cursor jumps (\x1b[...;1H) in inline stack mode
	if strings.Contains(rawTermOut.String(), ";1H") {
		t.Errorf("Inline stack mode emitted absolute row jump sequence (;1H): %q", rawTermOut.String())
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
	appWidget.Shell().SetPrompt("፧")

	host = ui.NewHost(appWidget, false)

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
		"KABIBI // RUSTICATED COMPUTING SUPERVISOR",
		"64K CELL MATRIX OK",
		"Kabibi shell initialized. Type commands below.",
		"፧echo line_1 > f.txt && cat f.txt",
		"line_1",
		"፧echo line_2 > f.txt && cat f.txt",
		"line_2",
		"፧echo line_3 > f.txt && cat f.txt",
		"line_3",
		"፧echo line_4 > f.txt && cat f.txt",
		"line_4",
		"፧echo line_5 > f.txt && cat f.txt",
		"line_5",
		"፧echo line_6 > f.txt && cat f.txt",
		"line_6",
		"፧exit",
	}

	if len(nonEmptyAllLines) != len(wantFullHistoryAndViewport) {
		t.Fatalf("combined scrollback+viewport lines = %d, want %d:\ngot:  %#v\nwant: %#v",
			len(nonEmptyAllLines), len(wantFullHistoryAndViewport), nonEmptyAllLines, wantFullHistoryAndViewport)
	}
	for i := range wantFullHistoryAndViewport {
		if !strings.Contains(nonEmptyAllLines[i], wantFullHistoryAndViewport[i]) {
			t.Errorf("combined line[%d] = %q, want substring %q", i, nonEmptyAllLines[i], wantFullHistoryAndViewport[i])
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

	appWidget.Shell().SetPrompt("፧")

	host = ui.NewHost(appWidget, false)

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
		if strings.Contains(rawTermOut.String(), "፧") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Type "echo" -> cursor should move to columnIndex 5 ("፧" is 1 col + "echo" is 4 cols -> \r\x1b[5C)
	_, _ = io.WriteString(inW, "echo")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "\x1b[5C") || strings.Contains(rawTermOut.String(), "\x1b[24;6H") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(rawTermOut.String(), "\x1b[5C") && !strings.Contains(rawTermOut.String(), "\x1b[24;6H") {
		t.Fatalf("expected cursor at colIndex 5 after typing 'echo', got:\n%q", rawTermOut.String())
	}

	// Now type a single trailing space " " -> even though cell (5, 0) was already ' ',
	// the cursor MUST immediately advance to columnIndex 6 (\x1b[6C) without waiting for another char!
	_, _ = io.WriteString(inW, " ")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "\x1b[6C") || strings.Contains(rawTermOut.String(), "\x1b[24;7H") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(rawTermOut.String(), "\x1b[6C") && !strings.Contains(rawTermOut.String(), "\x1b[24;7H") {
		t.Fatalf("trailing space did not immediately advance terminal cursor to colIndex 6:\n%q", rawTermOut.String())
	}

	_, _ = io.WriteString(inW, "\x03exit\r")
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Host.RunWithIO did not exit")
	}
}

// TestAppTUI_ModeToggle_ZeroPlumeAndPlumeHistory verifies that ShellMode can be
// toggled cleanly between ModeStack (dynamic 3-part stack: 3 banner rows + input +
// 1 blank footer line) and ModePlumeHistory (full-viewport multi-block rendering
// retained for future file-panel integration).
func TestAppTUI_ModeToggle_ZeroPlumeAndPlumeHistory(t *testing.T) {
	workDir := t.TempDir()

	var scrollback []string
	wZero := NewWidget(AppWidgetOptions{
		WorkingDir:        workDir,
		ShellMode:         shell.ModeStack,
		WriteToScrollback: func(s string) { scrollback = append(scrollback, s) },
	})
	defer wZero.Close()
	wZero.Shell().SetPrompt("፧")

	// Initial ModeStack: 3 banner rows + 1 input row + 1 blank footer row = 5 rows; cursor on row 3 (input row)
	if got := wZero.Measure(ui.Constraints{MaxW: 80, MaxH: 24}); got != (ui.Size{W: 80, H: 5}) {
		t.Fatalf("ModeStack Measure() = %#v, want {W:80, H:5}", got)
	}

	bufZero, curZero := renderAppANSIForTest(t, wZero.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 24}, ui.RenderContext{Focused: true}), 80, 24)
	if bufZero.Height != 5 || curZero.Y != 3 {
		t.Fatalf("ModeStack Render() height=%d cursorY=%d, want height=5 cursorY=3", bufZero.Height, curZero.Y)
	}

	// Switch dynamically to ModePlumeHistory and verify full-viewport Measure & Render
	wZero.Shell().SetMode(shell.ModePlumeHistory)
	if got := wZero.Measure(ui.Constraints{MaxW: 80, MaxH: 24}); got != (ui.Size{W: 80, H: 24}) {
		t.Fatalf("ModePlumeHistory Measure() = %#v, want {W:80, H:24}", got)
	}
	// Verify dynamic wrapping in both modes when input exceeds 80 columns ("፧" + 170 'a' = 171 cols -> 3 input rows)
	wZero.Shell().SetInput(strings.Repeat("a", 170))
	if got := wZero.Measure(ui.Constraints{MaxW: 80, MaxH: 24}); got != (ui.Size{W: 80, H: 24}) {
		t.Fatalf("ModePlumeHistory Measure() with wrapped input = %#v, want {W:80, H:24}", got)
	}
	bufPlumeWrap, curPlumeWrap := renderAppANSIForTest(t, wZero.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 24}, ui.RenderContext{Focused: true}), 80, 24)
	// In ModePlumeHistory, chromeH = 3 input rows + 1 blank footer row = 4 rows at bottom -> cursor on row 22 (above footer row 23)
	if bufPlumeWrap.Height != 24 || curPlumeWrap.Y != 22 {
		t.Fatalf("ModePlumeHistory wrapped Render() height=%d cursorY=%d, want height=24 cursorY=22", bufPlumeWrap.Height, curPlumeWrap.Y)
	}

	wZero.Shell().SetMode(shell.ModeStack)
	// 3 banner rows + 3 wrapped input rows + 1 blank footer row = 7 rows
	if got := wZero.Measure(ui.Constraints{MaxW: 80, MaxH: 24}); got != (ui.Size{W: 80, H: 7}) {
		t.Fatalf("ModeStack wrapped Measure() = %#v, want {W:80, H:7}", got)
	}
	wZero.Shell().SetInput("")
	if got := wZero.Measure(ui.Constraints{MaxW: 80, MaxH: 24}); got != (ui.Size{W: 80, H: 5}) {
		t.Fatalf("ModeStack cleared Measure() = %#v, want {W:80, H:5}", got)
	}
}

// TestAppTUI_SlackGapRecovery_PushDownBeforeEjectingUp verifies that when user
// input wraps to multiple lines (expanding the allocated terminal footprint)
// and is then deleted back to 1 line (opening a bottom slack gap), subsequent
// command output expands downward to close that slack gap BEFORE any top rows
// are ejected into scrollback.
func TestAppTUI_SlackGapRecovery_PushDownBeforeEjectingUp(t *testing.T) {
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
	appWidget.Shell().SetPrompt("፧")

	host = ui.NewHost(appWidget, false)

	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()

	var rawTermOut syncBuffer
	// Viewport 80x9:
	// Initial stack = 3 banner rows + 1 input row + 1 blank footer = 5 rows.
	const termW, termH = 80, 9

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

	// 1. Type a 3-row wrapped input ("፧" + 170 'x' = 171 cols -> 3 rows on width 80).
	// Occupied stack height grows from 5 -> 7 rows (3 banner + 3 input + 1 footer).
	_, _ = io.WriteString(inW, strings.Repeat("x", 170))
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}).H == 7 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}).H; got != 7 {
		t.Fatalf("after 3-row input wrap, Measure().H = %d, want 7", got)
	}

	// 2. Delete wrapped input via Ctrl+C -> occupied stack shrinks back to 5 rows,
	// leaving a 2-row slack gap at the bottom of the 7-row allocated footprint.
	_, _ = io.WriteString(inW, "\x03")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}).H == 5 {
			time.Sleep(20 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 3. Run a command that adds 2 rows (1 prompt+cmd row + 1 output row -> occupied stack grows 5 -> 7).
	// This MUST push down into the 2-row slack gap with ZERO scrollback ejection!
	_, _ = io.WriteString(inW, "echo fill_gap\r")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(appWidget.Shell().Blocks()) >= 2 && !appWidget.Shell().IsRunning() {
			time.Sleep(25 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	scrollbackMu.Lock()
	ejectedDuringGapClose := len(scrollbackChunks)
	scrollbackMu.Unlock()
	if ejectedDuringGapClose != 0 {
		t.Fatalf("expected 0 scrollback ejections while closing bottom slack gap, got %d (%#v)",
			ejectedDuringGapClose, scrollbackChunks)
	}

	// 4. Run 2 more commands (4 more rows -> total occupied exceeds 9-row viewport) -> now top rows eject!
	for i := 1; i <= 2; i++ {
		_, _ = io.WriteString(inW, "echo overflow\r")
		wantBlocks := 2 + i
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if len(appWidget.Shell().Blocks()) >= wantBlocks && !appWidget.Shell().IsRunning() {
				time.Sleep(25 * time.Millisecond)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	scrollbackMu.Lock()
	ejectedAfterOverflow := len(scrollbackChunks)
	scrollbackMu.Unlock()
	if ejectedAfterOverflow == 0 {
		t.Fatal("expected top rows to eject after slack gap was closed and viewport overflowed")
	}

	_, _ = io.WriteString(inW, "exit\r")
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Host.RunWithIO did not exit")
	}
}

// TestAppTUI_BluePanels_CtrlO_HiddenConveyorAndFarKeyRouting verifies the full
// Blue Panels HUD lifecycle from docs/3-blue.md:
//  1. Ctrl+O summons DualPaneWidget at the top of the screen while keeping the
//     3-row BlockBanner + PromptInput + 1 blank footer line in the 5-row bottom gap.
//  2. Far Manager split key routing:
//     - Up/Down navigates files in the active blue pane even while PromptInput is live.
//     - Tab switches Left <-> Right pane and syncs ExecutionSession.Dir() (`\w፧`).
//     - Enter with empty PromptInput on a directory steps into it and updates `\w፧`.
//     - Enter with empty PromptInput on an executable script runs `./script.sh` in the shell!
//  3. The Hidden Conveyor Belt: running commands while panels are open slides output
//     lines upward behind the blue panels with ZERO scrollback ejection until the
//     hidden stack catches up to Row 0 at the physical top of the screen.
func TestAppTUI_BluePanels_CtrlO_HiddenConveyorAndFarKeyRouting(t *testing.T) {
	workDir := t.TempDir()
	subDir := filepath.Join(workDir, "alpha_dir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(subDir, "runme.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho executed_from_blue_panel\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join(subDir, "note.txt")
	if err := os.WriteFile(notePath, []byte("hello editor\n"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	appWidget.Shell().SetPrompt("፧")

	host = ui.NewHost(appWidget, false)

	inR, inW := io.Pipe()
	defer inR.Close()
	defer inW.Close()

	var rawTermOut syncBuffer
	// Viewport 80x14:
	// H_gap = 5 rows (3 banner/plume rows + 1 PromptInput + 1 blank footer row)
	// H_panels = 14 - 5 = 9 rows at the top of the screen.
	// Total block budget behind + below panels = 14 - 2 = 12 block rows.
	// Initial banner = 3 block rows -> 9 hidden rows behind the panels before any ejection!
	const termW, termH = 80, 14

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

	// 1. Press Ctrl+O (\x0f) to summon the dual blue panels
	_, _ = io.WriteString(inW, "\x0f")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if appWidget.PanelsVisible() && appWidget.Measure(ui.Constraints{MaxW: termW, MaxH: termH}).H == termH {
			time.Sleep(25 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !appWidget.PanelsVisible() {
		t.Fatal("Ctrl+O did not open the blue panels")
	}

	// Verify that no scrollback ejection happened just from opening the panels
	scrollbackMu.Lock()
	if len(scrollbackChunks) != 0 {
		t.Fatalf("expected 0 scrollback chunks on Ctrl+O open, got %d", len(scrollbackChunks))
	}
	scrollbackMu.Unlock()

	// 2. Navigate in the active left pane: items are ["..", "alpha_dir"].
	// Press Down (\x1b[B) to highlight "alpha_dir", then Enter (\r) with empty PromptInput to cd into alpha_dir!
	_, _ = io.WriteString(inW, "\x1b[B\r")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if filepath.Clean(appWidget.Shell().Dir()) == filepath.Clean(subDir) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := filepath.Clean(appWidget.Shell().Dir()); got != filepath.Clean(subDir) {
		t.Fatalf("Enter on alpha_dir in blue panel did not sync shell cwd: got %q, want %q", got, subDir)
	}

	// 3. In alpha_dir, items are ["..", "note.txt", "runme.sh"].
	// Press Down twice to highlight "runme.sh", then press Enter with empty PromptInput
	// to execute ./runme.sh in the shell stack below the panels!
	_, _ = io.WriteString(inW, "\x1b[B\x1b[B\r")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		blocks := appWidget.Shell().Blocks()
		if len(blocks) >= 2 && !appWidget.Shell().IsRunning() {
			time.Sleep(25 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	blocks := appWidget.Shell().Blocks()
	if len(blocks) < 2 || !strings.Contains(blocks[1].Output.String(), "executed_from_blue_panel") {
		t.Fatalf("Enter on executable runme.sh did not run in shell: blocks=%#v", blocks)
	}

	// 4. Verify Hidden Conveyor Belt:
	// Total block rows so far = 3 (banner) + 2 (./runme.sh + output) = 5 rows <= 12-row budget.
	// Even though the bottom gap below the panels is only 3 block rows tall, the older 2 banner
	// rows slid underneath the blue panels with ZERO scrollback ejection!
	scrollbackMu.Lock()
	ejectedWhileSlidingUnderPanels := len(scrollbackChunks)
	scrollbackMu.Unlock()
	if ejectedWhileSlidingUnderPanels != 0 {
		t.Fatalf("expected 0 scrollback ejections while blocks slide underneath blue panels, got %d", ejectedWhileSlidingUnderPanels)
	}

	// 5. Now run 5 more commands (10 more rows -> total 15 block rows > 12-row full-screen budget)
	// so the hidden conveyor belt catches up to Row 0 at the top of the screen and ejects!
	for i := 1; i <= 5; i++ {
		_, _ = io.WriteString(inW, fmt.Sprintf("echo conveyor_%d\r", i))
		wantBlocks := 2 + i
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if len(appWidget.Shell().Blocks()) >= wantBlocks && !appWidget.Shell().IsRunning() {
				time.Sleep(25 * time.Millisecond)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	scrollbackMu.Lock()
	ejectedAfterCatchingTop := len(scrollbackChunks)
	scrollbackMu.Unlock()
	if ejectedAfterCatchingTop == 0 {
		t.Fatal("expected scrollback ejection once hidden stack behind panels caught up to row 0")
	}

	// 6. Press Tab to switch to the right pane (which is still in workDir) and verify shell cwd syncs back to workDir
	_, _ = io.WriteString(inW, "\t")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if filepath.Clean(appWidget.Shell().Dir()) == filepath.Clean(workDir) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := filepath.Clean(appWidget.Shell().Dir()); got != filepath.Clean(workDir) {
		t.Fatalf("Tab to right pane did not sync shell cwd to %q: got %q", workDir, got)
	}

	// 7. Exit cleanly
	_, _ = io.WriteString(inW, "exit\r")
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Host.RunWithIO returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Host.RunWithIO did not exit")
	}
}

// TestAppTUI_BluePanels_FileOpsAndEditor verifies that when the blue panels are
// open, F7 (Mkdir), F2 (Rename), F4 (Editor open/edit/save/close), and F8 (Delete)
// operate against the real filesystem and refresh the panels cleanly.
func TestAppTUI_BluePanels_FileOpsAndEditor(t *testing.T) {
	workDir := t.TempDir()
	filePath := filepath.Join(workDir, "hello.txt")
	if err := os.WriteFile(filePath, []byte("initial_line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	appWidget := NewWidget(AppWidgetOptions{
		WorkingDir:    workDir,
		PanelsVisible: true,
	})
	defer appWidget.Close()
	appWidget.Shell().SetPrompt("፧")

	// Render once at 80x24
	_ = appWidget.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 24}, ui.RenderContext{Focused: true})

	// 1. F7: Make Directory "created_dir"
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyF7})
	if appWidget.Dialog() == nil {
		t.Fatal("F7 did not open Mkdir dialog")
	}
	for _, r := range "created_dir" {
		appWidget.HandleEvent(input.KeyPressEvent{Code: r, Text: string(r)})
	}
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if appWidget.Dialog() != nil {
		t.Fatal("Enter did not close Mkdir dialog")
	}
	if info, err := os.Stat(filepath.Join(workDir, "created_dir")); err != nil || !info.IsDir() {
		t.Fatalf("expected created_dir on disk, err=%v", err)
	}

	// 2. Select "hello.txt" in left pane and press F4 to open the editor
	appWidget.Panels().Left.SelectByName("hello.txt")
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyF4})
	if appWidget.Editor() == nil {
		t.Fatal("F4 on hello.txt did not open Editor")
	}
	// Type "!" into the editor, save with F2, and quit with Esc
	appWidget.HandleEvent(input.KeyPressEvent{Code: '!', Text: "!"})
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyF2})
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyEscape})
	if appWidget.Editor() != nil {
		t.Fatal("Esc after saving did not close Editor")
	}
	editedBytes, err := os.ReadFile(filePath)
	if err != nil || !strings.HasPrefix(string(editedBytes), "!initial_line") {
		t.Fatalf("editor save content = %q, err=%v; want prefix %q", string(editedBytes), err, "!initial_line")
	}

	// 3. F2: Rename "hello.txt" -> "renamed.txt"
	appWidget.Panels().Left.SelectByName("hello.txt")
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyF2})
	if appWidget.Dialog() == nil {
		t.Fatal("F2 did not open Rename dialog")
	}
	appWidget.Dialog().InputValue = "renamed.txt"
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
	if _, err := os.Stat(filepath.Join(workDir, "renamed.txt")); err != nil {
		t.Fatalf("expected renamed.txt after F2 rename: %v", err)
	}

	// 4. F8: Delete "renamed.txt"
	appWidget.Panels().Left.SelectByName("renamed.txt")
	appWidget.HandleEvent(input.KeyPressEvent{Code: input.KeyF8})
	if appWidget.Dialog() == nil {
		t.Fatal("F8 did not open Delete confirm dialog")
	}
	appWidget.HandleEvent(input.KeyPressEvent{Code: 'y', Text: "y"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(workDir, "renamed.txt")); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(workDir, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected renamed.txt to be deleted after F8 confirm, err=%v", err)
	}
}

// TestAppTUI_BluePanels_ShownThenWithdrawn_TopAnchoredDownwardGrowth verifies
// that when the blue panels are summoned via Ctrl+O (claiming the full screen)
// and then withdrawn via Ctrl+O before the shell stack has filled the screen:
//  1. The stack immediately top-aligns at row 0 (anchored right below the latest
//     untouched row in terminal history), leaving blank space at the bottom.
//  2. Subsequent command output grows downward toward the bottom into that space
//     with ZERO scrollback ejection until the stack hits the bottom of the screen.
//  3. Only after hitting the bottom does further output eject upward into scrollback.
func TestAppTUI_BluePanels_ShownThenWithdrawn_TopAnchoredDownwardGrowth(t *testing.T) {
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
	appWidget.Shell().SetPrompt("፧")

	// 1. Open panels (Ctrl+O) and render at 80x10 -> claims full screen and bottom-aligns stack
	appWidget.SetPanelsVisible(true)
	bufOpen, curOpen := renderAppANSIForTest(t, appWidget.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 10}, ui.RenderContext{Focused: true}), 80, 10)
	// When panels are open on 80x10, PromptInput sits at row 8 (above blank footer row 9)
	if bufOpen.Height != 10 || curOpen.Y != 8 {
		t.Fatalf("with panels open on 80x10: height=%d cursorY=%d, want height=10 cursorY=8", bufOpen.Height, curOpen.Y)
	}
	if !curOpen.Visible {
		t.Fatal("shell input cursor should remain visible while panels are open")
	}

	// 2. Withdraw panels (Ctrl+O again) -> stack must immediately anchor at row 0
	// (banner rows at 0..3, PromptInput at row 4, blank footer at row 5 -> occupied height 6)
	appWidget.SetPanelsVisible(false)
	bufWithdrawn, curWithdrawn := renderAppANSIForTest(t, appWidget.Render(ui.Rect{X: 0, Y: 0, W: 80, H: 10}, ui.RenderContext{Focused: true}), 80, 10)
	if curWithdrawn.Y != 4 {
		t.Fatalf("after withdrawing panels, expected PromptInput cursor at top-anchored row 4, got %d", curWithdrawn.Y)
	}
	if bufWithdrawn.Height != 6 {
		t.Fatalf("after withdrawing panels, expected Shell rendered height 6 (4 banner + 1 input + 1 footer), got %d", bufWithdrawn.Height)
	}

	// 3. Now run inside Host on 80x10: open panels, withdraw panels, run 2 commands
	// (4 rows -> occupied height grows 5 -> 7 -> 9 <= 10) and verify ZERO scrollback ejection!
	host = ui.NewHost(appWidget, false)
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

	// Summon panels with Ctrl+O, then withdraw with Ctrl+O
	_, _ = io.WriteString(inW, "\x0f")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if appWidget.PanelsVisible() {
			time.Sleep(20 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, _ = io.WriteString(inW, "\x0f")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !appWidget.PanelsVisible() {
			time.Sleep(20 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Run 2 commands (each adds 2 rows -> stack grows from 5 -> 7 -> 9 rows inside 10-row screen)
	for i := 1; i <= 2; i++ {
		_, _ = io.WriteString(inW, fmt.Sprintf("echo down_%d\r", i))
		wantBlocks := 1 + i
		deadline = time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if len(appWidget.Shell().Blocks()) >= wantBlocks && !appWidget.Shell().IsRunning() {
				time.Sleep(25 * time.Millisecond)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	scrollbackMu.Lock()
	ejectedBeforeHittingBottom := len(scrollbackChunks)
	scrollbackMu.Unlock()
	if ejectedBeforeHittingBottom != 0 {
		t.Fatalf("expected 0 scrollback ejections while growing downward after withdrawing panels, got %d", ejectedBeforeHittingBottom)
	}

	// Run 1 more command (adds 2 rows -> 11 rows > 10-row screen) -> hits bottom and ejects!
	_, _ = io.WriteString(inW, "echo hit_bottom_eject\r")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(appWidget.Shell().Blocks()) >= 4 && !appWidget.Shell().IsRunning() {
			time.Sleep(25 * time.Millisecond)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	scrollbackMu.Lock()
	ejectedAfterHittingBottom := len(scrollbackChunks)
	scrollbackMu.Unlock()
	if ejectedAfterHittingBottom == 0 {
		t.Fatal("expected scrollback ejection once top-anchored stack hit the bottom of the screen")
	}

	_, _ = io.WriteString(inW, "exit\r")
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Host.RunWithIO returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Host.RunWithIO did not exit")
	}
}

// TestAppTUI_ExitWithPanelsOpenRemovesPanelsAndRetainsHistory verifies that
// when the shell exits while blue panels are visible, the blue panels are
// immediately removed and the TUI is rerendered with only the shell and history
// so the final terminal viewport retains history without ephemeral panels.
func TestAppTUI_ExitWithPanelsOpenRemovesPanelsAndRetainsHistory(t *testing.T) {
	workDir := t.TempDir()

	var host *ui.Host
	appWidget := NewWidget(AppWidgetOptions{
		WorkingDir:    workDir,
		PanelsVisible: true,
		OnExit: func() {
			if host != nil {
				host.Stop()
			}
		},
	})
	defer appWidget.Close()
	appWidget.Shell().SetPrompt("፧")

	host = ui.NewHost(appWidget, false)

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

	// Wait for initial frame showing blue panels
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rawTermOut.String(), "left") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !appWidget.PanelsVisible() {
		t.Fatal("expected panels to be visible initially")
	}

	// Exit the shell while panels are open
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

	if appWidget.PanelsVisible() {
		t.Fatal("expected panelsVisible to be false after exit")
	}

	// Verify against VT emulator that the final screen has no blue panels
	emu := vt.NewEmulator(termW, termH)
	_, _ = emu.WriteString(rawTermOut.String())

	var finalLines []string
	for _, line := range extractEmulatorLines(emu, termW, termH) {
		if strings.TrimSpace(line) != "" {
			finalLines = append(finalLines, line)
		}
	}

	finalJoined := strings.Join(finalLines, "\n")
	if strings.Contains(finalJoined, "left") || strings.Contains(finalJoined, "right") || strings.Contains(finalJoined, "┬") {
		t.Fatalf("final terminal screen still contains panel artifacts:\n%s", finalJoined)
	}
	if !strings.Contains(finalJoined, "exit") {
		t.Fatalf("final terminal screen should retain shell exit command:\n%s", finalJoined)
	}
}

func TestAppTUI_EndToEnd_GitLogPagerQuit(t *testing.T) {
	const gitPagerPrompt = "--More-- (Space: page, Enter: line, b: back, q: quit)"

	workDir := t.TempDir()
	repo := initAppTestRepository(t, workDir)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("open Git worktree: %v", err)
	}
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(workDir, "history.txt"), []byte(fmt.Sprintf("%02d\n", i)), 0o644); err != nil {
			t.Fatalf("write commit %d: %v", i, err)
		}
		if _, err := worktree.Add("history.txt"); err != nil {
			t.Fatalf("stage commit %d: %v", i, err)
		}
		when := time.Unix(1_700_000_000+int64(i), 0)
		signature := &object.Signature{Name: "Kabibi Test", Email: "author@example.test", When: when}
		if _, err := worktree.Commit(fmt.Sprintf("commit-%02d", i), &git.CommitOptions{
			Author: signature, Committer: signature,
		}); err != nil {
			t.Fatalf("create commit %d: %v", i, err)
		}
	}

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
	appWidget.Shell().SetPrompt("፧")

	host = ui.NewHost(appWidget, false)
	defer host.Stop()

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

	waitUntil := func(timeout time.Duration, condition func() bool, description string) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s; terminal output: %q", description, rawTermOut.String())
	}
	waitUntil(3*time.Second, func() bool {
		return strings.Contains(rawTermOut.String(), "Preliminary version: be careful.")
	}, "initial TUI frame")

	if _, err := io.WriteString(inW, "export GIT_PAGER=less\r"); err != nil {
		t.Fatalf("set interactive pager: %v", err)
	}
	waitUntil(5*time.Second, func() bool {
		return len(appWidget.Shell().Blocks()) >= 2 && !appWidget.Shell().IsRunning()
	}, "pager environment setup command")

	if _, err := io.WriteString(inW, "git log\r"); err != nil {
		t.Fatalf("start git log: %v", err)
	}
	waitUntil(5*time.Second, func() bool {
		return strings.Contains(rawTermOut.String(), gitPagerPrompt)
	}, "Git log pager prompt")
	if !appWidget.Shell().IsRunning() {
		t.Fatal("Git log command finished before receiving pager input")
	}
	if output := rawTermOut.String(); !strings.Contains(output, "commit-19") {
		t.Fatal("first page did not display the newest commit")
	} else if strings.Contains(output, "commit-00") {
		t.Fatal("oldest commit was displayed before the pager was dismissed")
	}

	if _, err := io.WriteString(inW, "q"); err != nil {
		t.Fatalf("send pager quit key through TUI input: %v", err)
	}
	waitUntil(5*time.Second, func() bool {
		return len(appWidget.Shell().Blocks()) >= 3 && !appWidget.Shell().IsRunning()
	}, "Git log command to finish after q")
	if strings.Contains(rawTermOut.String(), "commit-00") {
		t.Fatal("oldest commit was displayed after quitting the first page")
	}

	if _, err := io.WriteString(inW, "exit\r"); err != nil {
		t.Fatalf("exit TUI: %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Host.RunWithIO returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Host.RunWithIO stalled after exiting")
	}
}
