package shell_test

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/shell"
)

func TestExecutionSession_InteractivePTY(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession failed: %v", err)
	}

	var cmdStr string
	var inputCmd string
	var expectedMarker string

	if runtime.GOOS == "windows" {
		cmdStr = "cmd.exe"
		inputCmd = "echo PTY_TEST_456\r\nexit\r\n"
		expectedMarker = "PTY_TEST_456"
	} else {
		path, err := exec.LookPath("sh")
		if err != nil {
			t.Skipf("sh not found in PATH: %v", err)
		}
		cmdStr = path
		inputCmd = "echo PTY_TEST_456\nexit\n"
		expectedMarker = "PTY_TEST_456"
	}

	var outputChunks []string
	chunkChan := make(chan string, 100)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, rerr := es.RunCommand(ctx, cmdStr, nil, shell.SourceUserShell, func(chunk []byte) {
			chunkChan <- string(chunk)
		})
		done <- rerr
	}()

	// Wait for the child process to start and attach to PTY
	deadline := time.Now().Add(3 * time.Second)
	for !es.HasActivePTY() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if !es.HasActivePTY() {
		t.Fatal("expected es.HasActivePTY() to be true while interactive child process is running")
	}

	// Forward input into the active child PTY
	time.Sleep(100 * time.Millisecond)
	if ok := es.WriteActivePTY([]byte(inputCmd)); !ok {
		t.Fatal("WriteActivePTY returned false")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunCommand returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for interactive command to finish after exit")
	}

	// Drain chunks
	close(chunkChan)
	for ch := range chunkChan {
		outputChunks = append(outputChunks, ch)
	}
	combined := strings.Join(outputChunks, "")

	if !strings.Contains(combined, expectedMarker) {
		t.Fatalf("expected PTY output to contain %q, got: %q", expectedMarker, combined)
	}

	if es.HasActivePTY() {
		t.Fatal("expected es.HasActivePTY() to be false after child process exited")
	}
}

func TestInteractiveChildShell_LifecycleAndIO(t *testing.T) {
	var cmd string
	var promptChar string
	if runtime.GOOS == "windows" {
		cmd = "cmd"
		promptChar = ">"
	} else {
		path, err := exec.LookPath("sh")
		if err != nil {
			t.Skipf("sh not found in PATH: %v", err)
		}
		cmd = path
		promptChar = "$"
	}

	s := shell.New(shell.ShellOptions{
		WorkingDir: t.TempDir(),
	})
	s.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{Focused: true})

	es := s.ExecutionSession()
	if es == nil {
		t.Fatal("expected non-nil ExecutionSession")
	}

	// 1. Launch the interactive shell command into Kabibi input
	for _, ch := range cmd {
		s.HandleEvent(input.KeyPressEvent{Code: ch, Text: string(ch)})
	}
	s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	// 2. Wait for the child shell process to start and attach to PTY
	deadline := time.Now().Add(3 * time.Second)
	for !es.HasActivePTY() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !es.HasActivePTY() {
		t.Fatal("expected child shell to attach to PTY")
	}
	if !s.IsRunning() {
		t.Fatal("expected shell widget to be in running state")
	}

	// 3. Receive the spawned shell prompt and validate that it doesn't just exit but awaits input
	promptDeadline := time.Now().Add(4 * time.Second)
	var promptReceived bool
	for time.Now().Before(promptDeadline) {
		blocks := s.Blocks()
		if len(blocks) > 0 {
			out := blocks[len(blocks)-1].Output.String()
			if strings.Contains(out, promptChar) || strings.Contains(out, "Microsoft Windows") || strings.Contains(out, "#") {
				promptReceived = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !promptReceived {
		t.Fatalf("did not receive spawned shell prompt within timeout; blocks: %+v", s.Blocks())
	}

	// Ensure the shell doesn't exit prematurely - verify it remains running and awaiting input
	time.Sleep(300 * time.Millisecond)
	if !s.IsRunning() || !es.HasActivePTY() {
		t.Fatal("child shell exited prematurely instead of awaiting input")
	}

	// 4. Type `echo 123 <Enter>` into the input via key events
	typeInput := func(str string) {
		for _, ch := range str {
			switch ch {
			case '\r':
				continue
			case '\n':
				s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})
			default:
				s.HandleEvent(input.KeyPressEvent{Code: ch, Text: string(ch)})
			}
		}
	}

	typeInput("echo 123\n")

	// 5. Verify that "123" appears in the output
	echoDeadline := time.Now().Add(4 * time.Second)
	var echoReceived bool
	for time.Now().Before(echoDeadline) {
		blocks := s.Blocks()
		if len(blocks) > 0 {
			out := blocks[len(blocks)-1].Output.String()
			if strings.Contains(out, "123") {
				echoReceived = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !echoReceived {
		t.Fatalf("expected output to contain '123', got: %q", s.Blocks()[len(s.Blocks())-1].Output.String())
	}

	// 6. Type `exit <Enter>` into that input
	typeInput("exit\n")

	// 7. Verify that the shell exits within 2-5 seconds
	exitDeadline := time.Now().Add(5 * time.Second)
	for (s.IsRunning() || es.HasActivePTY()) && time.Now().Before(exitDeadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if s.IsRunning() {
		t.Fatal("shell widget still running 5 seconds after typing exit")
	}
	if es.HasActivePTY() {
		t.Fatal("active PTY still present 5 seconds after typing exit")
	}

	// Check exit code
	blocks := s.Blocks()
	last := blocks[len(blocks)-1]
	if last.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", last.ExitCode)
	}
}
