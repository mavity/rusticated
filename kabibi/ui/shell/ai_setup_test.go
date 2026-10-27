package shell

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"ai"
	"ai/assets"
	"github.com/charmbracelet/x/input"
	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/credstore"
	"github.com/mavity/rusticated/kabibi/ui"
)

type scriptedOnboardingPrompter struct {
	choices        []int
	defaultChoices []int
	optionSets     [][]string
	confirmations  []bool
	inputs         []string
}

func (p *scriptedOnboardingPrompter) Choose(_ context.Context, _, _ string, options []string, defaultChoice int) (int, error) {
	if len(p.choices) == 0 {
		return -1, errors.New("unexpected choice prompt")
	}
	choice := p.choices[0]
	p.choices = p.choices[1:]
	p.defaultChoices = append(p.defaultChoices, defaultChoice)
	p.optionSets = append(p.optionSets, append([]string(nil), options...))
	return choice, nil
}

func (p *scriptedOnboardingPrompter) Confirm(context.Context, string, string) (bool, error) {
	if len(p.confirmations) == 0 {
		return false, errors.New("unexpected confirmation prompt")
	}
	confirmed := p.confirmations[0]
	p.confirmations = p.confirmations[1:]
	return confirmed, nil
}

func (p *scriptedOnboardingPrompter) Input(context.Context, string, string, bool) (string, error) {
	if len(p.inputs) == 0 {
		return "", errors.New("unexpected input prompt")
	}
	value := p.inputs[0]
	p.inputs = p.inputs[1:]
	return value, nil
}

func TestConfigureAICancelDoesNotPersistConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_, options, _, err := modelSetupMenu(context.Background(), config.AIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewLazyAIRunner(nil)
	runner.SetOnboardingPrompter(&scriptedOnboardingPrompter{choices: []int{len(options) - 1}})

	_, err = runner.configureAI(context.Background())
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

func TestGitHubCopilotOpenURLPromptOpensVerificationPageOnEnter(t *testing.T) {
	const authURL = "https://github.com/login/device"
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
	if _, err := term.Write([]byte("Visit: " + authURL + "\n")); err != nil {
		t.Fatalf("write verification URL: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- ai.PromptOpenURL(ctx, term, "Press Enter to open the verification page.")
	}()
	time.Sleep(25 * time.Millisecond)
	if !s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter}) {
		t.Fatal("Enter was not handled while the browser-open prompt was active")
	}
	select {
	case got := <-opened:
		if got != authURL {
			t.Fatalf("opened URL = %q, want %q", got, authURL)
		}
	case err := <-done:
		t.Fatalf("PromptOpenURL() returned before opening the browser: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Enter did not open the verification page")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PromptOpenURL() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PromptOpenURL() did not finish after opening the page")
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

func TestChooseRemoteModelUsesCompleteCatalogAndPriorChoice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna","context_length":80000},{"id":"gpt-6-sol","context_length":120000},{"id":"gpt-6-astra"}]}`)
	}))
	defer server.Close()
	prompter := &scriptedOnboardingPrompter{choices: []int{2}}

	model, contextTokens, err := chooseRemoteModel(
		context.Background(),
		prompter,
		ai.RemoteOpenAI,
		&ai.Credential{Type: ai.CredentialAPIKey, Key: "test-token"},
		server.URL+"/models",
		"gpt-6-luna",
		"gpt-6-luna",
	)
	if err != nil {
		t.Fatalf("chooseRemoteModel() error = %v", err)
	}
	if model != "gpt-6-sol" {
		t.Fatalf("chooseRemoteModel() = %q, want gpt-6-sol", model)
	}
	if contextTokens != 120000 {
		t.Fatalf("chooseRemoteModel() context limit = %d, want 120000", contextTokens)
	}
	if len(prompter.optionSets) != 1 || len(prompter.optionSets[0]) != 4 {
		t.Fatalf("model options = %v, want all three catalog entries and the custom-ID option", prompter.optionSets)
	}
	if prompter.defaultChoices[0] != 1 {
		t.Fatalf("default choice = %d, want previous GPT-6 Luna selection at index 1", prompter.defaultChoices[0])
	}
}

func TestChooseRemoteModelDefaultsToGPT6LunaWhenAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6-sol"}]}`)
	}))
	defer server.Close()
	prompter := &scriptedOnboardingPrompter{choices: []int{0}}

	model, contextTokens, err := chooseRemoteModel(
		context.Background(),
		prompter,
		ai.RemoteOpenAI,
		&ai.Credential{Type: ai.CredentialAPIKey, Key: "test-token"},
		server.URL+"/models",
		"gpt-6-luna",
		"",
	)
	if err != nil {
		t.Fatalf("chooseRemoteModel() error = %v", err)
	}
	if model != "gpt-6-luna" {
		t.Fatalf("chooseRemoteModel() = %q, want gpt-6-luna", model)
	}
	if contextTokens != 0 {
		t.Fatalf("chooseRemoteModel() context limit = %d, want 0 for missing catalog metadata", contextTokens)
	}
	if len(prompter.defaultChoices) != 1 || prompter.defaultChoices[0] != 0 {
		t.Fatalf("default choices = %v, want GPT-6 Luna at index 0", prompter.defaultChoices)
	}
}

func TestConfigureCustomEndpointSelectsProtocolAndPersistsModel(t *testing.T) {
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("catalog request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unauthenticated endpoint received Authorization: %q", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"local-model","context_length":4096}]}`)
	}))
	defer server.Close()

	prompter := &scriptedOnboardingPrompter{
		choices: []int{0, 0},
		inputs:  []string{server.URL, ""},
	}
	cfg, err := NewLazyAIRunner(nil).configureCustomEndpoint(context.Background(), prompter, config.AIConfig{})
	if err != nil {
		t.Fatalf("configureCustomEndpoint() error = %v", err)
	}
	if !cfg.Configured || cfg.Provider != string(ai.RemoteCustom) ||
		cfg.Protocol != string(ai.RemoteProtocolOpenAIChatCompletions) ||
		cfg.Model != "local-model" || cfg.ContextTokens != 4096 || cfg.BaseURL != server.URL {
		t.Fatalf("custom configuration = %+v", cfg)
	}
	if len(prompter.optionSets) != 2 || len(prompter.optionSets[0]) != len(ai.RemoteAPIProtocols()) {
		t.Fatalf("prompt option sets = %v, want protocol and model selection menus", prompter.optionSets)
	}
	saved, err := config.LoadAI()
	if err != nil {
		t.Fatalf("LoadAI() error = %v", err)
	}
	if saved != cfg {
		t.Fatalf("saved configuration = %+v, want %+v", saved, cfg)
	}
	credential, err := credstore.Read(context.Background(), credentialStoreID(cfg))
	if err != nil {
		t.Fatalf("read endpoint credential: %v", err)
	}
	if credential != nil {
		t.Fatalf("unexpected credential saved for keyless endpoint: %+v", credential)
	}
}

func TestConfigureCustomEndpointDetectsProtocolFromURL(t *testing.T) {
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("catalog path = %q, want /v1/models", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"detected-model"}]}`)
	}))
	defer server.Close()

	prompter := &scriptedOnboardingPrompter{
		choices: []int{0},
		inputs:  []string{server.URL + "/v1/chat/completions", ""},
	}
	cfg, err := NewLazyAIRunner(nil).configureCustomEndpoint(context.Background(), prompter, config.AIConfig{})
	if err != nil {
		t.Fatalf("configureCustomEndpoint() error = %v", err)
	}
	if cfg.Protocol != string(ai.RemoteProtocolOpenAIChatCompletions) || cfg.Model != "detected-model" {
		t.Fatalf("configuration = %+v, want detected OpenAI Chat Completions protocol and model", cfg)
	}
	if len(prompter.optionSets) != 1 {
		t.Fatalf("prompt option sets = %v, want only model selection after URL detection", prompter.optionSets)
	}
}

func TestCustomCredentialIdentityIncludesEndpointAndProtocol(t *testing.T) {
	base := config.AIConfig{Provider: string(ai.RemoteCustom), Protocol: string(ai.RemoteProtocolOpenAIChatCompletions), BaseURL: "https://example.test/v1"}
	same := base
	same.BaseURL += "/"
	otherProtocol := base
	otherProtocol.Protocol = string(ai.RemoteProtocolOpenAIResponses)
	otherEndpoint := base
	otherEndpoint.BaseURL = "https://other.example.test/v1"
	if credentialStoreID(base) != credentialStoreID(same) {
		t.Fatal("trailing slash changed custom endpoint credential identity")
	}
	if credentialStoreID(base) == credentialStoreID(otherProtocol) {
		t.Fatal("different protocols shared a custom endpoint credential identity")
	}
	if credentialStoreID(base) == credentialStoreID(otherEndpoint) {
		t.Fatal("different endpoints shared a custom endpoint credential identity")
	}
}

func TestModelSetupMenuOffersReauthenticationOnlyForStoredCredential(t *testing.T) {
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())
	current := config.AIConfig{
		Configured: true,
		Mode:       "remote",
		Provider:   string(ai.RemoteCustom),
		Protocol:   string(ai.RemoteProtocolOpenAIResponses),
		Model:      "XYZ/XYZ-model",
		BaseURL:    "https://example.test/v1",
	}
	_, withoutCredential, _, err := modelSetupMenu(context.Background(), current)
	if err != nil {
		t.Fatalf("modelSetupMenu() without credential: %v", err)
	}
	if withoutCredential[0] != "[Keep] XYZ-model" {
		t.Fatalf("first menu option = %q, want keep-current action", withoutCredential[0])
	}
	if len(withoutCredential) > 1 && strings.HasPrefix(withoutCredential[1], "[Login]") {
		t.Fatal("reauthentication was offered without a stored credential")
	}

	stored := &ai.Credential{Type: ai.CredentialAPIKey, Key: "secret"}
	if _, err := credstore.Modify(context.Background(), credentialStoreID(current), func(*credstore.Credential) (*credstore.Credential, error) {
		return stored, nil
	}); err != nil {
		t.Fatalf("store custom endpoint credential: %v", err)
	}
	_, withCredential, _, err := modelSetupMenu(context.Background(), current)
	if err != nil {
		t.Fatalf("modelSetupMenu() with credential: %v", err)
	}
	if len(withCredential) < 2 || withCredential[1] != "[Login] XYZ-model" {
		t.Fatalf("second menu option = %v, want reauthentication action", withCredential)
	}
}

func TestRemoteSetupDoesNotAskForInterstitialConfirmation(t *testing.T) {
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())
	stored := &ai.Credential{Type: ai.CredentialAPIKey, Key: "test-token"}
	if _, err := credstore.Modify(context.Background(), string(ai.RemoteOpenAI), func(*credstore.Credential) (*credstore.Credential, error) {
		return stored, nil
	}); err != nil {
		t.Fatalf("store provider credential: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"test-model"}]}`)
	}))
	defer server.Close()

	prompter := &scriptedOnboardingPrompter{
		choices: []int{0},
	}
	previous := config.AIConfig{Configured: true, Mode: "remote", Provider: string(ai.RemoteOpenAI), BaseURL: server.URL + "/models"}
	cfg, err := NewLazyAIRunner(nil).authenticateAndSaveRemoteProvider(
		context.Background(),
		prompter,
		nil,
		ai.RemoteOpenAI,
		"OpenAI",
		previous,
	)
	if err != nil {
		t.Fatalf("authenticateAndSaveRemoteProvider() error = %v", err)
	}
	if cfg.Model != "test-model" || len(prompter.defaultChoices) != 1 {
		t.Fatalf("remote configuration = %+v, choices = %v", cfg, prompter.defaultChoices)
	}
}

func TestRemoteContextTokenBudgetUsesDeclaredLimitOrConservativeFallback(t *testing.T) {
	if got := remoteContextTokenBudget(128000); got != 128000 {
		t.Fatalf("remoteContextTokenBudget(128000) = %d, want 128000", got)
	}
	if got := remoteContextTokenBudget(0); got != 8192 {
		t.Fatalf("remoteContextTokenBudget(0) = %d, want conservative 8192-token fallback", got)
	}
	if got := remoteContextTokenBudget(-1); got != 8192 {
		t.Fatalf("remoteContextTokenBudget(-1) = %d, want conservative 8192-token fallback", got)
	}
}

func TestContextCompactionLimitScalesProportionally(t *testing.T) {
	for _, test := range []struct {
		maxContext int
		want       int
	}{
		{maxContext: 8192, want: 6553},
		{maxContext: 128000, want: 102400},
		{maxContext: 1048576, want: 838860},
	} {
		if got := contextCompactionLimit(test.maxContext); got != test.want {
			t.Errorf("contextCompactionLimit(%d) = %d, want %d", test.maxContext, got, test.want)
		}
	}
}

func TestConfigureAIKeepsCurrentConfigurationFromFirstMenu(t *testing.T) {
	t.Setenv("KABIBI_CONFIG_DIR", t.TempDir())
	if err := config.SaveAI(config.AIConfig{
		Configured:   true,
		Mode:         "remote",
		Provider:     string(ai.RemoteOpenAI),
		Model:        "gpt-6-luna",
		ConsentGiven: true,
	}); err != nil {
		t.Fatalf("save previous configuration: %v", err)
	}
	prompter := &scriptedOnboardingPrompter{choices: []int{0}}

	got, err := NewLazyAIRunner(nil).configureAIWithPrompter(context.Background(), prompter, nil)
	if err != nil {
		t.Fatalf("configureAIWithPrompter() error = %v", err)
	}
	if got.Provider != string(ai.RemoteOpenAI) || got.Model != "gpt-6-luna" {
		t.Fatalf("current config = %+v, want prior OpenAI selection", got)
	}
	if len(prompter.defaultChoices) != 1 || prompter.defaultChoices[0] != 0 {
		t.Fatalf("choice defaults = %v, want current selection at index 0", prompter.defaultChoices)
	}
}

func TestReusableRemoteCredentialKeepsWorkingStoredCredential(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("KABIBI_CONFIG_DIR", configDir)
	stored := &ai.Credential{Type: ai.CredentialAPIKey, Key: "working-token"}
	if _, err := credstore.Modify(context.Background(), string(ai.RemoteOpenAI), func(*credstore.Credential) (*credstore.Credential, error) {
		return stored, nil
	}); err != nil {
		t.Fatalf("store credential: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer working-token" {
			t.Errorf("Authorization = %q, want stored bearer token", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	got, err := reusableRemoteCredential(context.Background(), ai.RemoteOpenAI, server.URL+"/models")
	if err != nil {
		t.Fatalf("reusableRemoteCredential() error = %v", err)
	}
	if got == nil || got.SecretToken() != "working-token" {
		t.Fatalf("reusableRemoteCredential() = %#v, want existing credential", got)
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

	// 2. Wait for the first searchable model menu.
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

	waitForBlockText("Choose an AI model", 5*time.Second)
	tier, ok := assets.LookupModelTier(cachedTierID)
	if !ok {
		t.Fatalf("cached tier %q disappeared from the model catalog", cachedTierID)
	}
	for _, ch := range tier.Name {
		s.HandleEvent(input.KeyPressEvent{Code: ch, Text: string(ch)})
	}
	s.HandleEvent(input.KeyPressEvent{Code: input.KeyEnter})

	// 3. Wait for the AI turn to initialize the GPU/NPU engine and complete without error.
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
