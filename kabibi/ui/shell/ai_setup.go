package shell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type modelSetupAction struct {
	kind     string
	provider ai.RemoteProvider
	tierID   string
}

type searchableOnboardingPrompter interface {
	SearchChoose(ctx context.Context, title, message string, options []string, defaultChoice int) (int, error)
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
	actions, options, defaultChoice, err := modelSetupMenu(ctx, previous)
	if err != nil {
		return config.AIConfig{}, err
	}
	choice, err := searchChoose(ctx, prompter, term, "Choose an AI model", "Remote models send prompts and tool results to the selected provider or endpoint; provider billing and data-use policies may apply.", options, defaultChoice)
	if err != nil {
		return config.AIConfig{}, err
	}
	if choice < 0 || choice >= len(actions) {
		return config.AIConfig{}, fmt.Errorf("invalid AI model selection %d", choice)
	}
	action := actions[choice]
	switch action.kind {
	case "keep":
		return previous, nil
	case "reauth":
		return r.reauthenticateCurrentRemote(ctx, prompter, term, previous)
	case "local":
		return r.configureLocalAI(ctx, prompter, previous, action.tierID)
	case "remote":
		for _, entry := range supportedRemoteProviders {
			if entry.provider == action.provider {
				return r.authenticateAndSaveRemoteProvider(ctx, prompter, term, entry.provider, entry.label, previous)
			}
		}
		return config.AIConfig{}, fmt.Errorf("unsupported remote provider %q", action.provider)
	case "custom":
		return r.configureCustomEndpoint(ctx, prompter, previous)
	case "cancel":
		return config.AIConfig{}, errOnboardingCancelled
	default:
		return config.AIConfig{}, fmt.Errorf("unsupported AI model selection %q", action.kind)
	}
}

func modelSetupMenu(ctx context.Context, previous config.AIConfig) ([]modelSetupAction, []string, int, error) {
	actions := make([]modelSetupAction, 0, len(assets.ModelTiers())+len(supportedRemoteProviders)+4)
	options := make([]string, 0, cap(actions))
	if previous.Configured {
		current := "current configuration"
		if previous.Mode == "local" {
			if tier, ok := assets.LookupModelTier(previous.TierID); ok {
				current = tier.Name
			}
		} else if previous.Mode == "remote" {
			if previous.Provider == string(ai.RemoteCustom) {
				current = fmt.Sprintf("Custom (%s, %s) / %s", previous.Protocol, previous.BaseURL, previous.Model)
			} else {
				current = fmt.Sprintf("%s / %s", previous.Provider, previous.Model)
			}
		}
		options = append(options, "Keep current: "+current)
		actions = append(actions, modelSetupAction{kind: "keep"})
		if previous.Mode == "remote" {
			credential, err := storedCredentialForConfig(ctx, previous)
			if err != nil {
				return nil, nil, 0, err
			}
			if credential != nil && credential.SecretToken() != "" {
				options = append(options, "Re-authenticate current: "+current)
				actions = append(actions, modelSetupAction{kind: "reauth"})
			}
		}
	}

	for _, tier := range assets.ModelTiers() {
		if assets.HostOS() == "browser" && !tier.WebGPUSupported {
			continue
		}
		options = append(options, tier.Name)
		actions = append(actions, modelSetupAction{kind: "local", tierID: tier.ID})
	}
	for _, provider := range supportedRemoteProviders {
		options = append(options, strings.TrimSuffix(strings.Split(provider.label, " (")[0], " "))
		actions = append(actions, modelSetupAction{kind: "remote", provider: provider.provider})
	}
	options = append(options, "Custom endpoint", "Cancel")
	actions = append(actions, modelSetupAction{kind: "custom"}, modelSetupAction{kind: "cancel"})
	defaultChoice := 0
	if previous.Configured {
		defaultChoice = 0
	}
	return actions, options, defaultChoice, nil
}

func searchChoose(ctx context.Context, prompter OnboardingPrompter, term ai.TerminalSession, title, message string, options []string, defaultChoice int) (int, error) {
	if searchable, ok := prompter.(searchableOnboardingPrompter); ok {
		return searchable.SearchChoose(ctx, title, message, options, defaultChoice)
	}
	if term != nil {
		return ai.PromptSearchSelect(ctx, term, title, message, options, defaultChoice)
	}
	return prompter.Choose(ctx, title, message, options, defaultChoice)
}

func storedCredentialForConfig(ctx context.Context, cfg config.AIConfig) (*ai.Credential, error) {
	if cfg.Provider == "" {
		return nil, nil
	}
	credential, err := credstore.Read(ctx, credentialStoreID(cfg))
	if err != nil {
		return nil, fmt.Errorf("read stored %s credential: %w", cfg.Provider, err)
	}
	return credential, nil
}

func credentialStoreID(cfg config.AIConfig) string {
	if cfg.Provider != string(ai.RemoteCustom) {
		return cfg.Provider
	}
	identity := strings.TrimSpace(cfg.Protocol) + "\x00" + strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	digest := sha256.Sum256([]byte(identity))
	return "custom-" + hex.EncodeToString(digest[:])
}

func (r *LazyAIRunner) reauthenticateCurrentRemote(ctx context.Context, prompter OnboardingPrompter, term ai.TerminalSession, previous config.AIConfig) (config.AIConfig, error) {
	if previous.Provider == string(ai.RemoteCustom) {
		return r.reauthenticateCustomEndpoint(ctx, prompter, previous)
	}
	var selected remoteProviderEntry
	for _, entry := range supportedRemoteProviders {
		if string(entry.provider) == previous.Provider {
			selected = entry
			break
		}
	}
	if selected.provider == "" {
		return config.AIConfig{}, fmt.Errorf("cannot re-authenticate unsupported provider %q", previous.Provider)
	}
	credential, err := authenticateRemoteProvider(ctx, prompter, term, selected.provider, selected.label, previous.BaseURL)
	if err != nil {
		return config.AIConfig{}, err
	}
	if _, err := credstore.Modify(ctx, string(selected.provider), func(*credstore.Credential) (*credstore.Credential, error) {
		return credential, nil
	}); err != nil {
		return config.AIConfig{}, fmt.Errorf("save %s credential: %w", selected.label, err)
	}
	if err := config.SaveAI(previous); err != nil {
		return config.AIConfig{}, err
	}
	return previous, nil
}

func (r *LazyAIRunner) reauthenticateCustomEndpoint(ctx context.Context, prompter OnboardingPrompter, previous config.AIConfig) (config.AIConfig, error) {
	key, err := prompter.Input(ctx, "Endpoint API key", "Enter a replacement API key for "+previous.BaseURL, true)
	if err != nil {
		return config.AIConfig{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return config.AIConfig{}, fmt.Errorf("custom endpoint API key must not be empty")
	}
	credential := &ai.Credential{Type: ai.CredentialAPIKey, Key: key}
	if err := ai.ValidateRemoteCredential(ctx, ai.RemoteEngineConfig{
		Provider:   ai.RemoteCustom,
		Protocol:   ai.RemoteAPIProtocol(previous.Protocol),
		Credential: credential,
		BaseURL:    previous.BaseURL,
	}); err != nil {
		return config.AIConfig{}, err
	}
	if _, err := credstore.Modify(ctx, credentialStoreID(previous), func(*credstore.Credential) (*credstore.Credential, error) {
		return credential, nil
	}); err != nil {
		return config.AIConfig{}, fmt.Errorf("save custom endpoint credential: %w", err)
	}
	if err := config.SaveAI(previous); err != nil {
		return config.AIConfig{}, err
	}
	return previous, nil
}

func (r *LazyAIRunner) configureCustomEndpoint(ctx context.Context, prompter OnboardingPrompter, previous config.AIConfig) (config.AIConfig, error) {
	endpoint, err := prompter.Input(ctx, "Custom AI endpoint", "Enter the HTTPS endpoint URL (HTTP is allowed for localhost). Prompts and tool results will be sent to this endpoint.", false)
	if err != nil {
		return config.AIConfig{}, err
	}
	endpoint = strings.TrimSpace(endpoint)
	if err := ai.ValidateRemoteBaseURL(endpoint); err != nil {
		return config.AIConfig{}, fmt.Errorf("custom AI endpoint: %w", err)
	}

	protocol, detected := ai.DetectRemoteAPIProtocol(endpoint)
	if !detected {
		protocols := ai.RemoteAPIProtocols()
		options := make([]string, len(protocols))
		for i, candidate := range protocols {
			options[i] = remoteProtocolLabel(candidate)
		}
		choice, err := searchChoose(ctx, prompter, nil, "Choose endpoint API protocol", "The endpoint path does not identify a protocol unambiguously.", options, 0)
		if err != nil {
			return config.AIConfig{}, err
		}
		if choice < 0 || choice >= len(protocols) {
			return config.AIConfig{}, fmt.Errorf("invalid custom endpoint protocol selection %d", choice)
		}
		protocol = protocols[choice]
	}

	identity := config.AIConfig{Provider: string(ai.RemoteCustom), Protocol: string(protocol), BaseURL: endpoint}
	credentialID := credentialStoreID(identity)
	credential, err := reusableCustomCredential(ctx, identity)
	if err != nil {
		return config.AIConfig{}, err
	}
	if credential == nil {
		key, err := prompter.Input(ctx, "Endpoint API key", "Enter an optional API key, or leave empty for an unauthenticated endpoint", true)
		if err != nil {
			return config.AIConfig{}, err
		}
		key = strings.TrimSpace(key)
		if key != "" {
			credential = &ai.Credential{Type: ai.CredentialAPIKey, Key: key}
			if err := ai.ValidateRemoteCredential(ctx, ai.RemoteEngineConfig{
				Provider:   ai.RemoteCustom,
				Protocol:   protocol,
				Credential: credential,
				BaseURL:    endpoint,
			}); err != nil {
				return config.AIConfig{}, err
			}
		}
	}

	previousModel := ""
	if previous.Configured && previous.Mode == "remote" && previous.Provider == string(ai.RemoteCustom) &&
		previous.Protocol == string(protocol) && strings.TrimRight(previous.BaseURL, "/") == strings.TrimRight(endpoint, "/") {
		previousModel = previous.Model
	}
	engineConfig := ai.RemoteEngineConfig{
		Provider:   ai.RemoteCustom,
		Protocol:   protocol,
		Credential: credential,
		BaseURL:    endpoint,
	}
	model, contextTokens, err := chooseRemoteModelFromConfig(ctx, prompter, engineConfig, "", previousModel)
	if errors.Is(err, ai.ErrRemoteCredentialRejected) && credential == nil {
		key, inputErr := prompter.Input(ctx, "Endpoint API key required", "The endpoint rejected the unauthenticated model catalog request. Enter an API key.", true)
		if inputErr != nil {
			return config.AIConfig{}, inputErr
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return config.AIConfig{}, fmt.Errorf("custom endpoint requires an API key to access its model catalog")
		}
		credential = &ai.Credential{Type: ai.CredentialAPIKey, Key: key}
		engineConfig.Credential = credential
		if err := ai.ValidateRemoteCredential(ctx, engineConfig); err != nil {
			return config.AIConfig{}, err
		}
		model, contextTokens, err = chooseRemoteModelFromConfig(ctx, prompter, engineConfig, "", previousModel)
	}
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		model, err = enterRemoteModelID(ctx, prompter, "The endpoint did not provide a usable model catalog. Enter the exact model ID to use.")
		contextTokens = 0
	}
	if err != nil {
		return config.AIConfig{}, err
	}
	if credential != nil {
		if _, err := credstore.Modify(ctx, credentialID, func(*credstore.Credential) (*credstore.Credential, error) {
			return credential, nil
		}); err != nil {
			return config.AIConfig{}, fmt.Errorf("save custom endpoint credential: %w", err)
		}
	}

	cfg := config.AIConfig{
		Configured:    true,
		Mode:          "remote",
		Provider:      string(ai.RemoteCustom),
		Protocol:      string(protocol),
		Model:         model,
		ContextTokens: contextTokens,
		BaseURL:       endpoint,
		ConsentGiven:  true,
	}
	if err := config.SaveAI(cfg); err != nil {
		return config.AIConfig{}, err
	}
	return cfg, nil
}

func remoteProtocolLabel(protocol ai.RemoteAPIProtocol) string {
	switch protocol {
	case ai.RemoteProtocolOpenAIChatCompletions:
		return "OpenAI Chat Completions"
	case ai.RemoteProtocolOpenAIResponses:
		return "OpenAI Responses"
	case ai.RemoteProtocolAnthropicMessages:
		return "Anthropic Messages"
	case ai.RemoteProtocolGeminiGenerateContent:
		return "Gemini generateContent"
	case ai.RemoteProtocolCopilot:
		return "GitHub Copilot"
	default:
		return string(protocol)
	}
}

func reusableCustomCredential(ctx context.Context, cfg config.AIConfig) (*ai.Credential, error) {
	credential, err := credstore.Read(ctx, credentialStoreID(cfg))
	if err != nil {
		return nil, fmt.Errorf("read stored custom endpoint credential: %w", err)
	}
	if credential == nil || credential.SecretToken() == "" {
		return nil, nil
	}
	err = ai.ValidateRemoteCredential(ctx, ai.RemoteEngineConfig{
		Provider:   ai.RemoteCustom,
		Protocol:   ai.RemoteAPIProtocol(cfg.Protocol),
		Credential: credential,
		BaseURL:    cfg.BaseURL,
	})
	if err == nil {
		return credential, nil
	}
	if errors.Is(err, ai.ErrRemoteCredentialRejected) {
		return nil, nil
	}
	return nil, err
}

func (r *LazyAIRunner) configureLocalAI(ctx context.Context, prompter OnboardingPrompter, previous config.AIConfig, tierID string) (config.AIConfig, error) {
	cacheDir, err := assets.DefaultCacheDir()
	if err != nil {
		return config.AIConfig{}, err
	}
	tier, ok := assets.LookupModelTier(tierID)
	if !ok {
		return config.AIConfig{}, fmt.Errorf("unknown local model tier %q", tierID)
	}
	if assets.HostOS() == "browser" && !tier.WebGPUSupported {
		return config.AIConfig{}, fmt.Errorf("%s is not supported by this browser", tier.Name)
	}
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
	if total > 0 {
		downloads := make([]string, 0, 3)
		if modelSize > 0 {
			downloads = append(downloads, formatAIBytes(uint64(modelSize))+" of model weights")
		}
		if runtimeSize > 0 {
			downloads = append(downloads, formatAIBytes(uint64(runtimeSize))+" of runtime")
		}
		if embedderSize > 0 {
			downloads = append(downloads, formatAIBytes(uint64(embedderSize))+" of embeddings")
		}
		message := fmt.Sprintf(
			"Download %s (%d bytes) into %s? This includes %s. Nothing is downloaded unless you confirm.",
			formatAIBytes(uint64(total)), total, cacheDir, strings.Join(downloads, ", "),
		)
		confirmed, err := prompter.Confirm(ctx, "Confirm local AI downloads", message)
		if err != nil {
			return config.AIConfig{}, err
		}
		if !confirmed {
			return config.AIConfig{}, errOnboardingCancelled
		}
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
	choice, err := searchChoose(ctx, prompter, term, "Choose a cloud provider", "Cloud requests are sent to the selected provider and may incur usage or subscription charges.", options, defaultChoice)
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
	return chooseRemoteModelFromConfig(ctx, prompter, ai.RemoteEngineConfig{
		Provider:   provider,
		Credential: cred,
		BaseURL:    baseURL,
	}, preferredModel, previousModel)
}

func chooseRemoteModelFromConfig(ctx context.Context, prompter OnboardingPrompter, engineConfig ai.RemoteEngineConfig, preferredModel, previousModel string) (string, int, error) {
	models, err := ai.ListRemoteModelDetails(ctx, engineConfig)
	if err != nil {
		return "", 0, err
	}
	if len(models) == 0 {
		return "", 0, fmt.Errorf("%s returned no selectable models", engineConfig.Provider)
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
	choices = append(choices, "\x00Enter a model ID manually")
	choice, err := searchChoose(ctx, prompter, nil, "Choose a provider model", "Type to filter the provider catalog; select an exact model ID if it is not listed.", choices, defaultChoice)
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
	model, err := enterRemoteModelID(ctx, prompter, "Enter the provider model ID")
	return model, 0, err
}

func enterRemoteModelID(ctx context.Context, prompter OnboardingPrompter, message string) (string, error) {
	model, err := prompter.Input(ctx, "Model identifier", message, false)
	if err != nil {
		return "", err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("provider model ID must not be empty")
	}
	return model, nil
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
