package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai"
	"ai/assets"
	"github.com/mavity/rusticated/kabibi/config"
	"github.com/mavity/rusticated/kabibi/credstore"
)

func (r *LazyAIRunner) configureAI(ctx context.Context) (config.AIConfig, error) {
	prompter := r.onboarding
	if prompter == nil {
		return config.AIConfig{}, fmt.Errorf("AI is not configured; run Kabibi interactively to complete setup")
	}
	mode, err := prompter.Choose(ctx, "Set up Kabibi AI", "Choose where AI requests should run.", []string{
		"Local Gemma model",
		"Cloud provider (API key)",
		"Cancel",
	}, 0)
	if err != nil {
		return config.AIConfig{}, err
	}
	switch mode {
	case 0:
		return r.configureLocalAI(ctx, prompter)
	case 1:
		return r.configureRemoteAI(ctx, prompter)
	case 2:
		return config.AIConfig{}, errOnboardingCancelled
	default:
		return config.AIConfig{}, fmt.Errorf("invalid AI setup selection %d", mode)
	}
}

func (r *LazyAIRunner) configureLocalAI(ctx context.Context, prompter OnboardingPrompter) (config.AIConfig, error) {
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
	_, modelSize, err := assets.ModelTierAsset(tier.ID)
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

func (r *LazyAIRunner) configureRemoteAI(ctx context.Context, prompter OnboardingPrompter) (config.AIConfig, error) {
	providers := []ai.RemoteProvider{ai.RemoteOpenAI, ai.RemoteOpenRouter, ai.RemoteAnthropic, ai.RemoteGemini}
	options := []string{"OpenAI", "OpenRouter", "Anthropic Claude", "Google Gemini"}
	choice, err := prompter.Choose(ctx, "Choose a cloud provider", "Cloud requests are sent to the selected provider and may incur usage charges.", options, 1)
	if err != nil {
		return config.AIConfig{}, err
	}
	if choice < 0 || choice >= len(providers) {
		return config.AIConfig{}, fmt.Errorf("invalid cloud provider selection %d", choice)
	}
	provider := providers[choice]
	model, err := chooseRemoteModel(ctx, prompter, provider)
	if err != nil {
		return config.AIConfig{}, err
	}
	apiKey, err := prompter.Input(ctx, "Provider credential", "Enter the API key for "+options[choice], true)
	if err != nil {
		return config.AIConfig{}, err
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return config.AIConfig{}, fmt.Errorf("%s API key must not be empty", options[choice])
	}
	confirmed, err := prompter.Confirm(ctx, "Confirm cloud AI setup", "Kabibi will send prompts and tool results to "+options[choice]+". Provider billing and data-retention policies apply. Continue?")
	if err != nil {
		return config.AIConfig{}, err
	}
	if !confirmed {
		return config.AIConfig{}, errOnboardingCancelled
	}
	if err := ai.ValidateRemoteCredential(ctx, ai.RemoteEngineConfig{Provider: provider, Model: model, APIKey: apiKey}); err != nil {
		return config.AIConfig{}, err
	}
	if err := credstore.Set(string(provider), "api-key", apiKey); err != nil {
		return config.AIConfig{}, fmt.Errorf("save %s API key: %w", options[choice], err)
	}
	cfg := config.AIConfig{
		Configured:   true,
		Mode:         "remote",
		Provider:     string(provider),
		Model:        model,
		ConsentGiven: true,
	}
	if err := config.SaveAI(cfg); err != nil {
		return config.AIConfig{}, err
	}
	return cfg, nil
}

func chooseRemoteModel(ctx context.Context, prompter OnboardingPrompter, provider ai.RemoteProvider) (string, error) {
	var choices []string
	switch provider {
	case ai.RemoteOpenAI:
		choices = []string{"gpt-4.1-mini", "gpt-4.1", "Enter a model ID"}
	case ai.RemoteOpenRouter:
		choices = []string{"openrouter/auto", "openai/gpt-4.1-mini", "Enter a model ID"}
	case ai.RemoteAnthropic:
		choices = []string{"claude-3-5-haiku-latest", "claude-3-7-sonnet-latest", "Enter a model ID"}
	case ai.RemoteGemini:
		choices = []string{"gemini-2.5-flash", "gemini-2.5-pro", "Enter a model ID"}
	default:
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	choice, err := prompter.Choose(ctx, "Choose a provider model", "Select a curated model or enter a model identifier.", choices, 0)
	if err != nil {
		return "", err
	}
	if choice < 0 || choice >= len(choices) {
		return "", fmt.Errorf("invalid provider model selection %d", choice)
	}
	if choice != len(choices)-1 {
		return choices[choice], nil
	}
	model, err := prompter.Input(ctx, "Model identifier", "Enter the provider model ID", false)
	if err != nil {
		return "", err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", errors.New("provider model ID must not be empty")
	}
	return model, nil
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
