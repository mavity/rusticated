package shell

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/ui/terminal"
)

func TestSubmittedCommandRetainsSyntaxStylesInPlume(t *testing.T) {
	input := `echo "hello"`
	block := CommandBlock{
		Kind:        BlockCommand,
		Source:      SourceUserShell,
		Prompt:      "$ ",
		Input:       input,
		inputStyles: bashInputStyles(input),
		Output:      bytes.Buffer{},
		Status:      StatusSuccess,
	}

	buf := renderBlockCached(&block, 40)
	if got := buf.Get(2, 0).Style.Foreground(); got != bashKeywordStyle.Foreground() {
		t.Errorf("submitted command keyword color = #%06X, want #%06X", got, bashKeywordStyle.Foreground())
	}
	hello := len([]rune(`$ echo "`))
	if got := buf.Get(hello, 0).Style.Foreground(); got != bashStringStyle.Foreground() {
		t.Errorf("submitted string color = #%06X, want #%06X", got, bashStringStyle.Foreground())
	}
}

func TestInteractiveShellCommandIsStyledWithoutAnimatingProgrammaticExecution(t *testing.T) {
	sh := New(ShellOptions{Prompt: "$ "})
	defer sh.Close()

	sh.ExecuteLine(`echo "hello"`)

	sh.mu.Lock()
	defer sh.mu.Unlock()
	block := &sh.blocks[len(sh.blocks)-1]
	if block.submitAnim != nil {
		t.Fatal("programmatic ExecuteLine unexpectedly started the interactive submission animation")
	}
	buf := renderBlockCached(block, 40)
	promptWidth := visualWidth(ansiRegex.ReplaceAllString(block.Prompt, ""))
	if got := buf.Get(promptWidth, 0).Style.Foreground(); got != bashKeywordStyle.Foreground() {
		t.Errorf("live command keyword color = #%06X, want #%06X", got, bashKeywordStyle.Foreground())
	}
	stringOffset := promptWidth + len([]rune(`echo "`))
	if got := buf.Get(stringOffset, 0).Style.Foreground(); got != bashStringStyle.Foreground() {
		t.Errorf("live command string color = #%06X, want #%06X", got, bashStringStyle.Foreground())
	}
}

func TestNodeVersionReturnsToInteractivePrompt(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node is not available: %v", err)
	}

	sh := New(ShellOptions{})
	defer sh.Close()
	sh.input.SetValue("node --version")
	if !sh.handleKeyEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not submit node --version")
	}

	deadline := time.Now().Add(5 * time.Second)
	for sh.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sh.IsRunning() {
		blocks := sh.Blocks()
		output := blocks[len(blocks)-1].Output.String()
		sh.Close()
		t.Fatalf("node --version did not return to the prompt; output=%q", output)
	}

	blocks := sh.Blocks()
	if output := blocks[len(blocks)-1].Output.String(); !strings.Contains(output, "v") {
		t.Fatalf("node --version output = %q, want a version string", output)
	}
	sh.mu.Lock()
	animationActive := sh.blocks[len(sh.blocks)-1].submitAnim != nil
	sh.mu.Unlock()
	if animationActive {
		t.Fatal("submission animation remained active after command completion")
	}
}

func TestCommandOutputStopsSubmissionAnimationImmediately(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node is not available: %v", err)
	}

	sh := New(ShellOptions{})
	defer sh.Close()
	sh.SetInput(`node -e "setTimeout(function(){console.log('ready'); setTimeout(function(){},1500)},100)"`)
	if !sh.handleKeyEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not submit the test command")
	}

	var lastOutput string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		blocks := sh.Blocks()
		if len(blocks) > 0 {
			lastOutput = blocks[len(blocks)-1].Output.String()
			if strings.Contains(lastOutput, "ready") {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(lastOutput, "ready") {
		t.Fatalf("command did not produce expected output: %q", lastOutput)
	}

	sh.mu.Lock()
	block := &sh.blocks[len(sh.blocks)-1]
	animationActive := block.submitAnim != nil
	blockSnapshot := *block
	cancel := sh.activeCancel
	done := sh.doneCh
	sh.mu.Unlock()
	if animationActive {
		t.Fatal("submission animation continued after command output arrived")
	}
	buf := renderBlockCached(&blockSnapshot, 80)
	promptWidth := visualWidth(ansiRegex.ReplaceAllString(blockSnapshot.Prompt, ""))
	style := buf.Get(promptWidth, 0).Style
	if len(blockSnapshot.inputStyles) == 0 {
		t.Fatal("submitted command did not retain syntax styles")
	}
	if got, want := style.Foreground(), blockSnapshot.inputStyles[0].Foreground(); got != want {
		t.Fatalf("stopped animation command foreground = #%06X, want syntax color #%06X", got, want)
	}
	if !style.HasAttribute(terminal.CharResetBackgroundDefault) {
		t.Fatal("stopped animation command background was not reset to the terminal default")
	}
	if cancel == nil || done == nil {
		t.Fatal("command unexpectedly ended before the silent-running interval was checked")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not stop after cancellation")
	}
}

func TestCommandSubmitAnimationTickerStopsAfterOneCycle(t *testing.T) {
	sh := New(ShellOptions{})
	defer sh.Close()

	animation := &commandSubmitAnimation{startedAt: time.Now()}
	sh.mu.Lock()
	sh.blocks = append(sh.blocks, CommandBlock{
		Kind:       BlockCommand,
		Input:      "echo done",
		submitAnim: animation,
	})
	blockIdx := len(sh.blocks) - 1
	sh.mu.Unlock()

	done := make(chan struct{})
	go func() {
		sh.runCommandSubmitAnimation(blockIdx, animation)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	sh.mu.Lock()
	stillRunning := sh.blocks[blockIdx].submitAnim == animation
	sh.mu.Unlock()
	if !stillRunning {
		t.Fatal("submission animation stopped before completing its first cycle")
	}

	select {
	case <-done:
	case <-time.After(2 * submitAnimationDuration):
		t.Fatal("submission animation continued after its first cycle")
	}

	sh.mu.Lock()
	stillRunning = sh.blocks[blockIdx].submitAnim == animation
	sh.mu.Unlock()
	if stillRunning {
		t.Fatal("submission animation remained active after its first cycle")
	}
}

func TestCommandSubmitAnimationTickerStopsWhenOutputArrives(t *testing.T) {
	sh := New(ShellOptions{})
	defer sh.Close()

	animation := &commandSubmitAnimation{startedAt: time.Now()}
	sh.mu.Lock()
	sh.blocks = append(sh.blocks, CommandBlock{
		Kind:       BlockCommand,
		Input:      "echo done",
		submitAnim: animation,
	})
	blockIdx := len(sh.blocks) - 1
	sh.mu.Unlock()

	done := make(chan struct{})
	go func() {
		sh.runCommandSubmitAnimation(blockIdx, animation)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	sh.stopCommandSubmitAnimation(blockIdx)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("submission animation ticker did not stop after output")
	}
}

func TestSilentCommandCompletionStopsSubmissionAnimation(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node is not available: %v", err)
	}

	sh := New(ShellOptions{})
	defer sh.Close()
	sh.SetInput(`node -e "setTimeout(function(){},100)"`)
	if !sh.handleKeyEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter did not submit the silent test command")
	}
	sh.WaitActive()

	sh.mu.Lock()
	block := &sh.blocks[len(sh.blocks)-1]
	blockSnapshot := *block
	animationActive := block.submitAnim != nil
	sh.mu.Unlock()
	if animationActive {
		t.Fatal("submission animation remained active after silent command completion")
	}

	buf := renderBlockCached(&blockSnapshot, 80)
	promptWidth := visualWidth(ansiRegex.ReplaceAllString(blockSnapshot.Prompt, ""))
	style := buf.Get(promptWidth, 0).Style
	if len(blockSnapshot.inputStyles) == 0 {
		t.Fatal("submitted command did not retain syntax styles")
	}
	if got, want := style.Foreground(), blockSnapshot.inputStyles[0].Foreground(); got != want {
		t.Fatalf("completed command foreground = #%06X, want syntax color #%06X", got, want)
	}
	if !style.HasAttribute(terminal.CharResetBackgroundDefault) {
		t.Fatal("completed command background was not reset to the terminal default")
	}
}

func TestCommandSubmitAnimationUsesCellPositionsAndPhases(t *testing.T) {
	const prompt = "$ "
	const input = `echo 你好`
	width := 12
	styles := bashInputStyles(input)
	layout := buildVisualLayout(prompt, input, len([]rune(input)), width, styles)
	if len(layout.inputCells) != len([]rune(input)) {
		t.Fatalf("input cell positions = %d, want %d", len(layout.inputCells), len([]rune(input)))
	}
	if got := layout.inputCells[len(layout.inputCells)-2].width; got != 2 {
		t.Fatalf("wide character cell width = %d, want 2", got)
	}

	startedAt := time.Unix(1, 0)
	animation := &commandSubmitAnimation{startedAt: startedAt}
	renderAt := func(elapsed time.Duration) terminal.CellBuf {
		buf := terminal.FromANSIString(prompt+input+"\n", width)
		applyCommandInputStyles(&buf, layout.inputCells, styles)
		renderCommandSubmitAnimation(&buf, layout, styles, animation, startedAt.Add(elapsed))
		return buf
	}

	wiping := renderAt(0)
	last := layout.inputCells[len(layout.inputCells)-1]
	if got := wiping.Get(last.x, last.y).Grapheme; got != "·" {
		t.Fatalf("initial wipe glyph = %q, want middle dot", got)
	}

	restoring := renderAt(submitWipeDuration)
	for _, position := range layout.inputCells {
		if position.width > 0 && restoring.Get(position.x, position.y).Grapheme != " " {
			t.Fatalf("command was not fully wiped at the restoration boundary")
		}
	}

	dark := renderAt(submitWipeDuration + submitRestoreDuration)
	normal := renderAt(submitWipeDuration + submitRestoreDuration + submitColorDuration/2)
	keyword := layout.inputCells[0]
	if dark.Get(keyword.x, keyword.y).Style.Foreground() >= bashKeywordStyle.Foreground() {
		t.Fatalf("dark flash color #%06X is not darker than syntax color #%06X",
			dark.Get(keyword.x, keyword.y).Style.Foreground(), bashKeywordStyle.Foreground())
	}
	if got := normal.Get(keyword.x, keyword.y).Style.Foreground(); got != bashKeywordStyle.Foreground() {
		t.Fatalf("mid-flash color = #%06X, want original syntax color #%06X", got, bashKeywordStyle.Foreground())
	}

	white := renderAt(submitWipeDuration + submitRestoreDuration + submitColorDuration)
	if got := white.Get(0, 0).Style.Background(); got != 0xFFFFFF {
		t.Fatalf("background at flash transition = #%06X, want white", got)
	}
	finalFrame := renderAt(submitAnimationDuration + 50*time.Millisecond)
	if got := finalFrame.Get(last.x, last.y).Grapheme; got != "好" {
		t.Fatalf("animation replayed after one cycle: final glyph = %q, want original input", got)
	}
}
