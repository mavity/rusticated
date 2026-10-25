package shell

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"ai"
	"ai/assets"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/ui"
)

type scriptedOnboardingPrompter struct {
	choices []int
}

func (p *scriptedOnboardingPrompter) Choose(context.Context, string, string, []string, int) (int, error) {
	if len(p.choices) == 0 {
		return -1, errors.New("unexpected choice prompt")
	}
	choice := p.choices[0]
	p.choices = p.choices[1:]
	return choice, nil
}

func (*scriptedOnboardingPrompter) Confirm(context.Context, string, string) (bool, error) {
	return false, errors.New("unexpected confirmation prompt")
}

func (*scriptedOnboardingPrompter) Input(context.Context, string, string, bool) (string, error) {
	return "", errors.New("unexpected input prompt")
}

func TestConfigureAICancelDoesNotPersistConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	runner := NewLazyAIRunner(nil)
	runner.SetOnboardingPrompter(&scriptedOnboardingPrompter{choices: []int{2}})

	_, err := runner.configureAI(context.Background())
	if !errors.Is(err, errOnboardingCancelled) {
		t.Fatalf("configureAI() error = %v, want onboarding cancellation", err)
	}
	cfg, err := config.LoadAI()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Configured || cfg.ConsentGiven {
		t.Fatalf("cancelled setup persisted configuration: %+v", cfg)
	}
}

func TestFormatAIBytes(t *testing.T) {
	if got := formatAIBytes(1 << 30); got != "1.00 GiB" {
		t.Fatalf("formatAIBytes(1 GiB) = %q", got)
	}
	if got := formatAIBytes(0); got != "0 B" {
		t.Fatalf("formatAIBytes(0) = %q", got)
	}
}

func TestAIBuiltinStatusAndHelpOverCommandTerminal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	es, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatalf("NewExecutionSession() error = %v", err)
	}
	es.SetCommandTerminalAttached(true)

	rec, err := es.RunCommand(context.Background(), "ai status", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("RunCommand('ai status') error = %v", err)
	}
	if rec.ExitCode != 0 || !strings.Contains(rec.Output, "unconfigured") {
		t.Fatalf("ai status output = %q (exit %d)", rec.Output, rec.ExitCode)
	}

	recHelp, err := es.RunCommand(context.Background(), "ai --help", nil, SourceUserShell, nil)
	if err != nil {
		t.Fatalf("RunCommand('ai --help') error = %v", err)
	}
	if recHelp.ExitCode != 0 || !strings.Contains(recHelp.Output, "ai login") {
		t.Fatalf("ai --help output = %q (exit %d)", recHelp.Output, recHelp.ExitCode)
	}
}

func TestShellInteractiveTerminalSessionPasteAndComplete(t *testing.T) {
	s := New(ShellOptions{
		WorkingDir: t.TempDir(),
	})
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simulate an active AI turn opening a virtual PTY TerminalSession (like OpenRouter OAuth login)
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	term, closeFn := s.openInteractiveTerminalSession(ctx, "ai setup")
	defer closeFn(0)

	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		val, err := ai.PromptLine(ctx, term, "OpenRouter OAuth", "Paste redirect URL or code", false)
		if err != nil {
			errCh <- err
			return
		}
		lineCh <- val
	}()

	// Wait briefly for PromptLine to subscribe to raw control input, then send a PasteEvent + Enter
	time.Sleep(25 * time.Millisecond)
	pasted := "http://127.0.0.1:43210/oauth/callback/uuid-1?code=pasted-code-xyz\n"
	if !s.HandleEvent(input.PasteEvent(pasted)) {
		t.Fatal("Shell.HandleEvent(PasteEvent) returned false while TerminalSession was active")
	}

	select {
	case err := <-errCh:
		t.Fatalf("PromptLine() error = %v", err)
	case got := <-lineCh:
		if got != strings.TrimSpace(pasted) {
			t.Fatalf("PromptLine() = %q, want %q", got, strings.TrimSpace(pasted))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptLine() hung after PasteEvent")
	}
}

func TestShellInteractiveTerminalSessionEmptyEnterOpensAuthURL(t *testing.T) {
	const authURL = "https://openrouter.ai/auth?authorize=oauth-test"
	opened := make(chan string, 1)
	s := New(ShellOptions{
		WorkingDir: t.TempDir(),
		OpenURL: func(target string) error {
			opened <- target
			return nil
		},
	})
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	term, closeFn := s.openInteractiveTerminalSession(ctx, "ai setup")
	defer closeFn(0)
	if _, err := term.Write([]byte("Open this URL in your browser:\n" + authURL + "\n")); err != nil {
		t.Fatalf("write authorization URL: %v", err)
	}

	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		value, err := ai.PromptLine(ctx, term, "OpenRouter OAuth", "Paste redirect URL or code", false)
		if err != nil {
			errCh <- err
			return
		}
		lineCh <- value
	}()

	time.Sleep(25 * time.Millisecond)
	if !s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("empty Enter was not handled while the auth prompt was active")
	}
	select {
	case got := <-opened:
		if got != authURL {
			t.Fatalf("opened URL = %q, want %q", got, authURL)
		}
	case err := <-errCh:
		t.Fatalf("PromptLine() returned before opening the auth URL: %v", err)
	case <-time.After(time.Second):
		t.Fatal("empty Enter did not open the authorization URL")
	}

	select {
	case got := <-lineCh:
		t.Fatalf("empty Enter submitted %q instead of keeping the auth prompt open", got)
	case err := <-errCh:
		t.Fatalf("PromptLine() returned after opening the auth URL: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	pasted := "http://127.0.0.1:43210/oauth/callback/uuid-1?code=pasted-code-xyz"
	if !s.HandleEvent(input.PasteEvent(pasted + "\n")) {
		t.Fatal("Shell.HandleEvent(PasteEvent) returned false while TerminalSession was active")
	}
	select {
	case err := <-errCh:
		t.Fatalf("PromptLine() error = %v", err)
	case got := <-lineCh:
		if got != pasted {
			t.Fatalf("PromptLine() = %q, want %q", got, pasted)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PromptLine() hung after the authorization URL was opened and response pasted")
	}
}

func TestEndToEnd_ShellUnconfiguredPrompt_InteractiveLocalGemmaOnboardingAndTurn(t *testing.T) {
	if runtime.GOARCH != "wasm" {
		t.Skip("End-to-end local Gemma Shell test runs under Rusticated WASM (washmhost + dload satellite)")
	}
	// Check that cached LiteRT-LM runtime and a cached Gemma 4 tier exist on this machine
	cacheDir, err := assets.DefaultCacheDir()
	if err != nil {
		t.Skipf("DefaultCacheDir unavailable: %v", err)
	}
	ctx := context.Background()
	if sz, err := assets.RuntimeDownloadSize(ctx, cacheDir); err != nil || sz != 0 {
		t.Skip("LiteRT-LM runtime is not cached locally; skipping full local Gemma E2E test")
	}
	cachedTierIdx := -1
	var cachedTierID string
	for i, tier := range assets.ModelTiers() {
		if sz, err := assets.ModelTierDownloadSize(tier.ID, cacheDir); err == nil && sz == 0 {
			cachedTierIdx = i
			cachedTierID = tier.ID
			break
		}
	}
	if cachedTierIdx < 0 {
		t.Skip("no Gemma 4 model tier is cached locally; skipping full local Gemma E2E test")
	}

	// Isolate config.AIConfigPath() in a temp config directory without overriding USERPROFILE
	// (overriding USERPROFILE causes Direct3D/Qualcomm GPU shader cache D3DSCache to open locked
	// .dxcache files inside t.TempDir()).
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())

	s := New(ShellOptions{
		WorkingDir: t.TempDir(),
	})
	defer s.Close()
	s.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{Focused: true})

	// 1. User types a natural language question into the unconfigured Shell and presses Enter.
	for _, ch := range "Reply with the single word PONG." {
		s.HandleEvent(input.KeyPressEvent{Code: ch, Text: string(ch)})
	}
	s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	// 2. Wait for the interactive virtual PTY setup block to render "Set up Kabibi AI"
	waitForBlockText := func(substr string, timeout time.Duration) string {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			buf, _ := s.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{Focused: true})
			var screen strings.Builder
			for y := 0; y < buf.Height; y++ {
				for x := 0; x < buf.Width; x++ {
					g := buf.Get(x, y).Grapheme
					if g == "" {
						screen.WriteByte(' ')
					} else {
						screen.WriteString(g)
					}
				}
				screen.WriteByte('\n')
			}
			txt := screen.String()
			if strings.Contains(txt, substr) {
				return txt
			}
			time.Sleep(25 * time.Millisecond)
		}
		buf, _ := s.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{Focused: true})
		var dump strings.Builder
		for y := 0; y < buf.Height; y++ {
			for x := 0; x < buf.Width; x++ {
				g := buf.Get(x, y).Grapheme
				if g == "" {
					dump.WriteByte(' ')
				} else {
					dump.WriteString(g)
				}
			}
			dump.WriteByte('\n')
		}
		t.Fatalf("timed out waiting for %q on Shell screen; current screen:\n%s", substr, dump.String())
		return ""
	}

	waitForBlockText("Set up Kabibi AI", 5*time.Second)

	// Select [1] Local Gemma model
	s.HandleEvent(input.KeyPressEvent{Code: '1', Text: "1"})
	s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	// 3. Wait for "Choose a local model" menu and select the cached tier (1-based number key + Enter)
	waitForBlockText("Choose a local model", 5*time.Second)
	tierKey := rune('1' + cachedTierIdx)
	s.HandleEvent(input.KeyPressEvent{Code: tierKey, Text: string(tierKey)})
	s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	// 4. Wait for "Confirm local AI downloads" and verify the cache path has NO leading '\C:\' or '/C:/'
	confirmScreen := waitForBlockText("Confirm local AI downloads", 5*time.Second)
	if strings.Contains(confirmScreen, `\C:\`) || strings.Contains(confirmScreen, `/C:/`) {
		t.Fatalf("Confirm screen contains malformed leading slash before Windows drive:\n%s", confirmScreen)
	}
	// Select [1] Yes, continue
	s.HandleEvent(input.KeyPressEvent{Code: '1', Text: "1"})
	s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	// 5. Wait for the AI turn to initialize the GPU/NPU engine and complete the turn without error!
	deadline := time.Now().Add(90 * time.Second)
	for s.IsRunning() && time.Now().Before(deadline) {
		s.Render(ui.Rect{W: 80, H: 24}, ui.RenderContext{Focused: true})
		time.Sleep(50 * time.Millisecond)
	}
	if s.IsRunning() {
		t.Fatal("Shell AI turn did not finish within 90s after completing local Gemma onboarding")
	}

	blocks := s.Blocks()
	if len(blocks) == 0 {
		t.Fatal("expected Shell blocks after AI turn")
	}
	var aiBlock *CommandBlock
	for i := range blocks {
		if blocks[i].Kind == BlockAITurn {
			aiBlock = &blocks[i]
		}
	}
	if aiBlock == nil {
		t.Fatalf("no BlockAITurn found in blocks: %+v", blocks)
	}
	if strings.Contains(aiBlock.Output.String(), "[ai error:") || aiBlock.Status == StatusFailure || aiBlock.Status == StatusError {
		t.Fatalf("E2E local Gemma turn failed! Status=%v Output=%q", aiBlock.Status, aiBlock.Output.String())
	}
	if strings.TrimSpace(aiBlock.Output.String()) == "" {
		t.Fatalf("E2E local Gemma turn produced empty output; blocks=%+v", blocks)
	}

	// Verify ai.json was persisted with the chosen local tier
	savedCfg, err := config.LoadAI()
	if err != nil {
		t.Fatalf("LoadAI() error = %v", err)
	}
	if !savedCfg.Configured || savedCfg.Mode != "local" || savedCfg.TierID != cachedTierID || !savedCfg.ConsentGiven {
		t.Fatalf("unexpected saved AIConfig: %+v", savedCfg)
	}
}
