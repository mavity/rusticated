package shell_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui"
	"github.com/mavity/rusticated/kabibi/ui/shell"
	"github.com/mavity/rusticated/platform"
)

type processRegistrationSink struct {
	session    *shell.ExecutionSession
	registered chan bool
}

func (sink *processRegistrationSink) OnAICommandStart(string, string) (func([]byte), func(int, time.Duration)) {
	_, registered := sink.session.LookupManagedPTY(1)
	sink.registered <- registered
	return nil, nil
}

func TestExecutionSession_WithoutAttachedShell_IsNonInteractive(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession failed: %v", err)
	}

	var outputChunks []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := es.RunCommand(ctx, "echo PTY_TEST_456", nil, shell.SourceUserShell, func(chunk []byte) {
		outputChunks = append(outputChunks, string(chunk))
	})
	if err != nil {
		t.Fatalf("RunCommand returned error: %v", err)
	}
	if record.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", record.ExitCode)
	}
	combined := strings.Join(outputChunks, "")

	if !strings.Contains(combined, "PTY_TEST_456") {
		t.Fatalf("expected output to contain %q, got: %q", "PTY_TEST_456", combined)
	}

	if es.HasActivePTY() {
		t.Fatal("expected standalone execution session to remain non-interactive")
	}
}

func TestExecuteAIToolCommand_RegistersBeforeCreatingView(t *testing.T) {
	es, err := shell.NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession failed: %v", err)
	}
	sink := &processRegistrationSink{
		session:    es,
		registered: make(chan bool, 1),
	}
	es.SetAICommandUISink(sink)

	_, _, _, _, err = es.ExecuteAIToolCommand(context.Background(), "echo registration-order", 5, false)
	if err != nil {
		t.Fatalf("ExecuteAIToolCommand returned error: %v", err)
	}
	if !<-sink.registered {
		t.Fatal("managed process was not registered before its UI view was created")
	}
}

func TestInteractiveChildShell_LifecycleAndIO(t *testing.T) {
	var cmd string
	var promptChar string
	hostOS := platform.HostOS()
	if runtime.GOOS == "windows" || strings.EqualFold(hostOS, "windows") || strings.EqualFold(hostOS, "win32") {
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
		WorkingDir: os.TempDir(),
	})
	t.Cleanup(func() {
		_ = s.Close()
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

	const marker = "KABIBI_PTY_COMMAND_OUTPUT_7461"
	typeInput("echo " + marker + "\n")

	// 5. Require both the echoed input and the command's output.
	echoDeadline := time.Now().Add(4 * time.Second)
	var echoReceived bool
	for time.Now().Before(echoDeadline) {
		blocks := s.Blocks()
		if len(blocks) > 0 {
			out := blocks[len(blocks)-1].Output.String()
			if strings.Count(out, marker) >= 2 {
				echoReceived = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !echoReceived {
		blocks := s.Blocks()
		if len(blocks) == 0 {
			t.Fatal("expected shell output after sending echo command, got no blocks")
		}
		t.Fatalf("expected echoed command and command output to contain %q, got: %q",
			marker, blocks[len(blocks)-1].Output.String())
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
