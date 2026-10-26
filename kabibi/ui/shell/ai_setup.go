package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"ai"
	"ai/assets"
	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/credstore"
)

type remoteProviderEntry struct {
	provider ai.RemoteProvider
	label    string
}

// RunAISetup runs the existing setup flow with caller-supplied text streams.
func RunAISetup(ctx context.Context, es *ExecutionSession, stdin io.Reader, stdout io.Writer) error {
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	runner := NewLazyAIRunner(es)
	defer runner.Close()
	_, err := runner.configureAIWithTerm(ctx, &ai.StdioTerminalSession{
		Reader: stdin,
		Writer: stdout,
		TTY:    false,
	})
	return err
}

var supportedRemoteProviders = []remoteProviderEntry{
	{provider: ai.RemoteOpenRouter, label: "OpenRouter (OAuth PKCE or API key)"},
	{provider: ai.RemoteAnthropic, label: "Anthropic Claude (Claude Pro/Max OAuth or API key)"},
	{provider: ai.RemoteOpenAI, label: "OpenAI (Sign in with ChatGPT OAuth or API key)"},
	{provider: ai.RemoteCopilot, label: "GitHub Copilot (Device-code OAuth)"},
	{provider: ai.RemoteGemini, label: "Google Gemini (API key)"},
}

func (r *LazyAIRunner) configureAI(ctx context.Context) (config.AIConfig, error) {
	if r.openTerm != nil {
		term, closeFn := r.openTerm(ctx, "ai setup")
		cfg, err := r.configureAIWithTerm(ctx, term)
		if err != nil {
			closeFn(1)
			return config.AIConfig{}, err
		}
		closeFn(0)
		return cfg, nil
	}
	prompter := r.onboarding
	if prompter == nil {
		return config.AIConfig{}, fmt.Errorf("AI is not configured; run `ai setup` or run Kabibi interactively to complete setup")
	}
	if tuiPrompter, ok := prompter.(*TUIOnboardingPrompter); ok && tuiPrompter.shell != nil {
		term, closeFn := tuiPrompter.openTerminal(ctx, "ai setup")
		cfg, err := r.configureAIWithTerm(ctx, term)
		if err != nil {
			closeFn(1)
			return config.AIConfig{}, err
		}
		closeFn(0)
		return cfg, nil
	}
	if stdioPrompter, ok := prompter.(*StdioOnboardingPrompter); ok {
		return r.configureAIWithTerm(ctx, stdioPrompter.terminalSession())
	}
	return r.configureAIWithPrompter(ctx, prompter, nil)
}

func (r *LazyAIRunner) configureAIWithTerm(ctx context.Context, term ai.TerminalSession) (config.AIConfig, error) {
	if term == nil {
		return config.AIConfig{}, fmt.Errorf("AI setup requires an active terminal session")
	}
	return r.configureAIWithPrompter(ctx, newTerminalOnboardingPrompter(term), term)
}

func (r *LazyAIRunner) configureAIWithPrompter(ctx context.Context, prompter OnboardingPrompter, term ai.TerminalSession) (config.AIConfig, error) {
	previous, err := config.LoadAI()
	if err != nil {
		return config.AIConfig{}, err
	}
	defaultMode := 0
	if previous.Configured && previous.Mode == "remote" {
		defaultMode = 1
	}
	mode, err := prompter.Choose(ctx, "Set up Kabibi AI", "Choose where AI requests should run.", []string{
		"Local Gemma model",
		"Cloud provider (OAuth / API key)",
		"Cancel",
	}, defaultMode)
	if err != nil {
		return config.AIConfig{}, err
	}
	switch mode {
	case 0:
		return r.configureLocalAI(ctx, prompter, previous)
	case 1:
		return r.configureRemoteAI(ctx, prompter, term, previous)
	case 2:
		return config.AIConfig{}, errOnboardingCancelled
	default:
		return config.AIConfig{}, fmt.Errorf("invalid AI setup selection %d", mode)
	}
}

func (r *LazyAIRunner) configureLocalAI(ctx context.Context, prompter OnboardingPrompter, previous config.AIConfig) (config.AIConfig, error) {
	profile, err := assets.ProbeHardware()
	if err != nil {
		return config.AIConfig{}, fmt.Errorf("inspect hardware for local AI setup: %w", err)
	}
	cacheDir, err := assets.DefaultCacheDir()
	if err != nil {
		return config.AIConfig{}, err
	}
	tiers := assets.ModelTiers()
	options := make([]string, 0, len(tiers))
	available := make([]assets.ModelTier, 0, len(tiers))
	defaultChoice := 0
	for _, tier := range tiers {
		if assets.HostOS() == "browser" && !tier.WebGPUSupported {
			continue
		}
		label := tier.Name
		if profile.RecommendedTier == tier.ID {
			label += " (recommended)"
			defaultChoice = len(options)
		}
		options = append(options, label)
		available = append(available, tier)
		if previous.Configured && previous.Mode == "local" && previous.TierID == tier.ID {
			defaultChoice = len(options) - 1
		}
	}
	if len(options) == 0 {
		return config.AIConfig{}, fmt.Errorf("no local AI model is supported by this host")
	}
	hardwareSummary := fmt.Sprintf("Detected RAM: %s; accelerator memory: %s.", formatAIBytes(profile.TotalRAMBytes), formatAIBytes(profile.AcceleratorBytes))
	if assets.HostOS() == "browser" {
		hardwareSummary += fmt.Sprintf(" WebGPU: %t; maximum buffer: %s.", profile.HasWebGPU, formatAIBytes(profile.WebGPUMaxBuffer))
	}
	choice, err := prompter.Choose(ctx, "Choose a local model", hardwareSummary, options, defaultChoice)
	if err != nil {
		return config.AIConfig{}, err
	}
	if choice < 0 || choice >= len(available) {
		return config.AIConfig{}, fmt.Errorf("invalid local model selection %d", choice)
	}
	tier := available[choice]
	modelSize, err := assets.ModelTierDownloadSize(tier.ID, cacheDir)
	if err != nil {
		return config.AIConfig{}, err
	}
	runtimeSize, err := assets.RuntimeDownloadSize(ctx, cacheDir)
	if err != nil {
		return config.AIConfig{}, fmt.Errorf("estimate runtime download: %w", err)
	}
	embedder := assets.HostOS() != "browser"
	var embedderSize int64
	if embedder {
		embedderSize, err = assets.EmbeddingModelDownloadSize(cacheDir)
		if err != nil {
			return config.AIConfig{}, fmt.Errorf("estimate embedding model download: %w", err)
		}
		if embedderSize > 0 && runtimeSize == 0 && modelSize == 0 {
			// If the runtime and LLM are already cached offline, don't force an embedder
			// download unless the user wants to fetch from the network.
			embedder = false
			embedderSize = 0
		}
	}
	total := runtimeSize + modelSize + embedderSize
	message := fmt.Sprintf(
		"Download up to %s (%d bytes) into %s? This includes %s of model weights, %s of runtime, and %s of embeddings. Nothing is downloaded unless you confirm.",
		formatAIBytes(uint64(total)), total, cacheDir, formatAIBytes(uint64(modelSize)), formatAIBytes(uint64(runtimeSize)), formatAIBytes(uint64(embedderSize)),
	)
	confirmed, err := prompter.Confirm(ctx, "Confirm local AI downloads", message)
	if err != nil {
		return config.AIConfig{}, err
	}
	if !confirmed {
		return config.AIConfig{}, errOnboardingCancelled
	}
	cfg := config.AIConfig{
		Configured:   true,
		Mode:         "local",
		TierID:       tier.ID,
		Embedder:     embedder,
		ConsentGiven: true,
	}
	if err := config.SaveAI(cfg); err != nil {
		return config.AIConfig{}, err
	}
	return cfg, nil
}

func (r *LazyAIRunner) configureRemoteAI(ctx context.Context, prompter OnboardingPrompter, term ai.TerminalSession, previous config.AIConfig) (config.AIConfig, error) {
	options := make([]string, len(supportedRemoteProviders))
	defaultChoice := 0
	for i, p := range supportedRemoteProviders {
		options[i] = p.label
		if previous.Configured && previous.Mode == "remote" && previous.Provider == string(p.provider) {
			defaultChoice = i
		}
	}
	choice, err := prompter.Choose(ctx, "Choose a cloud provider", "Cloud requests are sent to the selected provider and may incur usage or subscription charges.", options, defaultChoice)
	if err != nil {
		return config.AIConfig{}, err
	}
	if choice < 0 || choice >= len(supportedRemoteProviders) {
		return config.AIConfig{}, fmt.Errorf("invalid cloud provider selection %d", choice)
	}
	entry := supportedRemoteProviders[choice]
	return r.authenticateAndSaveRemoteProvider(ctx, prompter, term, entry.provider, entry.label, previous)
}

func (r *LazyAIRunner) configureRemoteProviderWithTerm(ctx context.Context, term ai.TerminalSession, providerArg string) (config.AIConfig, error) {
	prompter := newTerminalOnboardingPrompter(term)
	previous, err := config.LoadAI()
	if err != nil {
		return config.AIConfig{}, err
	}
	if providerArg == "" {
		return r.configureRemoteAI(ctx, prompter, term, previous)
	}
	for _, entry := range supportedRemoteProviders {
		if string(entry.provider) == providerArg || (providerArg == "copilot" && entry.provider == ai.RemoteCopilot) {
			return r.authenticateAndSaveRemoteProvider(ctx, prompter, term, entry.provider, entry.label, previous)
		}
	}
	return config.AIConfig{}, fmt.Errorf("unknown provider %q (supported: openrouter, anthropic, openai, github-copilot, gemini)", providerArg)
}

func (r *LazyAIRunner) authenticateAndSaveRemoteProvider(
	ctx context.Context,
	prompter OnboardingPrompter,
	term ai.TerminalSession,
	provider ai.RemoteProvider,
	label string,
	previous config.AIConfig,
) (config.AIConfig, error) {
	confirmed, err := prompter.Confirm(ctx, "Confirm cloud AI setup", "Kabibi will send prompts and tool results to "+label+". Provider billing and data-retention policies apply. Continue?")
	if err != nil {
		return config.AIConfig{}, err
	}
	if !confirmed {
		return config.AIConfig{}, errOnboardingCancelled
	}

	baseURL := ""
	if previous.Configured && previous.Mode == "remote" && previous.Provider == string(provider) {
		baseURL = previous.BaseURL
	}
	cred, err := reusableRemoteCredential(ctx, provider, baseURL)
	if err != nil {
		return config.AIConfig{}, err
	}
	if cred == nil {
		cred, err = authenticateRemoteProvider(ctx, prompter, term, provider, label, baseURL)
		if err != nil {
			return config.AIConfig{}, err
		}
	}
	previousModel := ""
	if previous.Configured && previous.Mode == "remote" && previous.Provider == string(provider) && previous.Model != "" {
		previousModel = previous.Model
	}
	model, contextTokens, err := chooseRemoteModel(ctx, prompter, provider, cred, baseURL, ai.DefaultRemoteModel(provider), previousModel)
	if errors.Is(err, ai.ErrRemoteCredentialRejected) {
		cred, err = authenticateRemoteProvider(ctx, prompter, term, provider, label, baseURL)
		if err == nil {
			model, contextTokens, err = chooseRemoteModel(ctx, prompter, provider, cred, baseURL, ai.DefaultRemoteModel(provider), previousModel)
		}
	}
	if err != nil {
		return config.AIConfig{}, err
	}

	if _, err := credstore.Modify(ctx, string(provider), func(*credstore.Credential) (*credstore.Credential, error) {
		return cred, nil
	}); err != nil {
		return config.AIConfig{}, fmt.Errorf("save %s credential: %w", label, err)
	}

	cfg := config.AIConfig{
		Configured:    true,
		Mode:          "remote",
		Provider:      string(provider),
		Model:         model,
		ContextTokens: contextTokens,
		BaseURL:       baseURL,
		ConsentGiven:  true,
	}
	if err := config.SaveAI(cfg); err != nil {
		return config.AIConfig{}, err
	}
	if term != nil {
		_, _ = fmt.Fprintf(term, "\x1b[32m✓ Configured %s with model %s\x1b[0m\n", provider, model)
	}
	return cfg, nil
}

func reusableRemoteCredential(ctx context.Context, provider ai.RemoteProvider, baseURL string) (*ai.Credential, error) {
	cred, err := credstore.Read(ctx, string(provider))
	if err != nil {
		return nil, fmt.Errorf("read stored %s credential: %w", provider, err)
	}
	if cred == nil || cred.SecretToken() == "" {
		return nil, nil
	}
	engineConfig := ai.RemoteEngineConfig{Provider: provider, Credential: cred, BaseURL: baseURL}
	if err := ai.ValidateRemoteCredential(ctx, engineConfig); err == nil {
		return cred, nil
	} else if !errors.Is(err, ai.ErrRemoteCredentialRejected) {
		return nil, err
	}

	refreshed, refreshErr := ai.ResolveProviderCredential(ctx, provider, credstore.NewStore(), nil)
	if refreshErr == nil && refreshed != nil {
		if err := ai.ValidateRemoteCredential(ctx, ai.RemoteEngineConfig{Provider: provider, Credential: refreshed, BaseURL: baseURL}); err == nil {
			return refreshed, nil
		} else if !errors.Is(err, ai.ErrRemoteCredentialRejected) {
			return nil, err
		}
	}
	return nil, nil
}

func authenticateRemoteProvider(ctx context.Context, prompter OnboardingPrompter, term ai.TerminalSession, provider ai.RemoteProvider, label, baseURL string) (*ai.Credential, error) {
	if term != nil {
		cred, err := ai.AuthenticateProvider(ctx, provider, term, nil)
		if err != nil {
			return nil, err
		}
		return cred, nil
	}
	apiKey, err := prompter.Input(ctx, "Provider credential", "Enter the API key for "+label, true)
	if err != nil {
		return nil, err
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%s API key must not be empty", label)
	}
	cred := &ai.Credential{Type: ai.CredentialAPIKey, Key: apiKey}
	if err := ai.ValidateRemoteCredential(ctx, ai.RemoteEngineConfig{Provider: provider, Credential: cred, BaseURL: baseURL}); err != nil {
		return nil, err
	}
	return cred, nil
}

func chooseRemoteModel(ctx context.Context, prompter OnboardingPrompter, provider ai.RemoteProvider, cred *ai.Credential, baseURL, preferredModel, previousModel string) (string, int, error) {
	models, err := ai.ListRemoteModelDetails(ctx, ai.RemoteEngineConfig{Provider: provider, Credential: cred, BaseURL: baseURL})
	if err != nil {
		return "", 0, err
	}
	if len(models) == 0 {
		return "", 0, fmt.Errorf("%s returned no selectable models", provider)
	}
	modelChoices := append([]ai.RemoteModelInfo(nil), models...)
	choices := make([]string, 0, len(models)+2)
	for _, model := range models {
		choices = append(choices, model.ID)
	}
	defaultChoice := 0
	defaultModel := previousModel
	if defaultModel == "" {
		defaultModel = preferredModel
	}
	foundDefault := false
	for i, model := range models {
		if model.ID == defaultModel {
			defaultChoice = i
			foundDefault = true
			break
		}
	}
	if previousModel != "" && !foundDefault {
		choices = append(choices, previousModel+" (previously selected; not in provider catalog)")
		modelChoices = append(modelChoices, ai.RemoteModelInfo{ID: previousModel})
		defaultChoice = len(modelChoices) - 1
	}
	choices = append(choices, "Enter a model ID")
	choice, err := prompter.Choose(ctx, "Choose a provider model", "Select a model from the provider catalog or enter a model identifier.", choices, defaultChoice)
	if err != nil {
		return "", 0, err
	}
	if choice < 0 || choice >= len(choices) {
		return "", 0, fmt.Errorf("invalid provider model selection %d", choice)
	}
	if choice < len(modelChoices) {
		model := modelChoices[choice]
		return model.ID, model.MaxInputTokens, nil
	}
	model, err := prompter.Input(ctx, "Model identifier", "Enter the provider model ID", false)
	if err != nil {
		return "", 0, err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", 0, errors.New("provider model ID must not be empty")
	}
	return model, 0, nil
}

func runAIStatusBuiltin(ctx context.Context, out io.Writer) error {
	cfg, err := config.LoadAI()
	if err != nil {
		return err
	}
	if !cfg.Configured {
		fmt.Fprintln(out, "AI status: unconfigured (run `ai setup` or enter a prompt)")
		return nil
	}
	if cfg.Mode == "local" {
		fmt.Fprintf(out, "AI mode: local (tier=%s, embedder=%t)\n", cfg.TierID, cfg.Embedder)
	} else {
		fmt.Fprintf(out, "AI mode: remote (provider=%s, model=%s)\n", cfg.Provider, cfg.Model)
	}
	infos, err := credstore.List(ctx)
	if err == nil && len(infos) > 0 {
		fmt.Fprintln(out, "Stored credentials:")
		for _, info := range infos {
			fmt.Fprintf(out, "  - %s (%s)\n", info.ProviderID, info.Type)
		}
	}
	return nil
}

func runAILogoutBuiltin(ctx context.Context, term ai.TerminalSession, providerArg string) error {
	if providerArg == "" {
		infos, err := credstore.List(ctx)
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			_, _ = fmt.Fprintln(term, "No stored AI credentials.")
			return nil
		}
		opts := make([]string, len(infos))
		for i, info := range infos {
			opts[i] = fmt.Sprintf("%s (%s)", info.ProviderID, info.Type)
		}
		idx, err := ai.PromptSelect(ctx, term, "Log out of provider", "Select a stored credential to remove.", opts, 0)
		if err != nil {
			return err
		}
		providerArg = infos[idx].ProviderID
	}
	if providerArg == "copilot" {
		providerArg = string(ai.RemoteCopilot)
	}
	if err := credstore.Remove(ctx, providerArg); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(term, "Removed stored credential for %s.\n", providerArg)
	return nil
}

func formatAIBytes(size uint64) string {
	if size <= 0 {
		return "0 B"
	}
	const unit uint64 = 1 << 30
	if size >= unit {
		return fmt.Sprintf("%.2f GiB", float64(size)/float64(unit))
	}
	return fmt.Sprintf("%.2f MiB", float64(size)/(1<<20))
}

func modelPathOrRemote(cfg config.AIConfig, modelPath string) string {
	if cfg.Mode == "remote" {
		return cfg.Provider + ":" + cfg.Model
	}
	return modelPath
}
