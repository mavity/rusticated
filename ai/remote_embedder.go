package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRemoteEmbeddingBatchSize = 64
	remoteEmbeddingCacheLimit   = 4096
)

type remoteEmbeddingCandidate struct {
	id                    string
	pricePerMillionTokens float64
}

type RemoteEmbedder struct {
	config RemoteEngineConfig
	model  string
	cache  map[string][]float32
	mu     sync.Mutex
	limit  chan struct{}
}

func ResolveRemoteEmbedder(ctx context.Context, cfg RemoteEngineConfig) (*RemoteEmbedder, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch cfg.Provider {
	case RemoteOpenAI, RemoteOpenRouter, RemoteGemini:
	default:
		return nil, fmt.Sprintf("%s has no configured embedding API", cfg.Provider)
	}

	if cfg.Provider == RemoteOpenAI && cfg.Credential != nil && cfg.Credential.Type == CredentialOAuth {
		return nil, "OpenAI ChatGPT OAuth is not supported by the standard embeddings API; use an OpenAI API key to enable embeddings"
	}
	if cfg.Provider == RemoteOpenAI && cfg.Credentials != nil {
		stored, err := cfg.Credentials.Read(ctx, string(cfg.Provider))
		if err != nil {
			return nil, fmt.Sprintf("cannot inspect %s embedding credentials: %v", cfg.Provider, err)
		}
		if stored != nil && stored.Type == CredentialOAuth {
			return nil, "OpenAI ChatGPT OAuth is not supported by the standard embeddings API; use an OpenAI API key to enable embeddings"
		}
	}
	if cfg.Credentials != nil {
		credential, err := ResolveProviderCredential(ctx, cfg.Provider, cfg.Credentials, cfg.HTTPClient)
		if err != nil {
			return nil, fmt.Sprintf("cannot resolve %s embedding credentials: %v", cfg.Provider, err)
		}
		cfg.Credential = credential
		cfg.APIKey = credential.SecretToken()
	} else if cfg.APIKey == "" && cfg.Credential != nil {
		cfg.APIKey = cfg.Credential.SecretToken()
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Sprintf("no %s credential is available for embeddings", cfg.Provider)
	}

	candidates, err := discoverRemoteEmbeddingCandidates(ctx, cfg)
	if err != nil {
		return nil, fmt.Sprintf("cannot determine available %s embedding models: %v", cfg.Provider, err)
	}
	model := chooseRemoteEmbeddingModel(cfg.Provider, candidates)
	if model == "" {
		return nil, fmt.Sprintf("no supported %s embedding model is available", cfg.Provider)
	}

	return &RemoteEmbedder{
		config: cfg,
		model:  model,
		cache:  make(map[string][]float32),
		limit:  make(chan struct{}, 2),
	}, fmt.Sprintf("using %s embedding model %s", cfg.Provider, model)
}

func discoverRemoteEmbeddingCandidates(ctx context.Context, cfg RemoteEngineConfig) ([]remoteEmbeddingCandidate, error) {
	switch cfg.Provider {
	case RemoteOpenAI:
		models, err := ListRemoteModelDetails(ctx, cfg)
		if err != nil {
			return nil, err
		}
		available := make(map[string]bool, len(models))
		for _, model := range models {
			available[model.ID] = true
		}
		return availableCandidates(available, []remoteEmbeddingCandidate{
			{id: "text-embedding-3-small", pricePerMillionTokens: 0.02},
			{id: "text-embedding-ada-002", pricePerMillionTokens: 0.10},
			{id: "text-embedding-3-large", pricePerMillionTokens: 0.13},
		}), nil
	case RemoteOpenRouter:
		return discoverOpenRouterEmbeddingCandidates(ctx, cfg)
	case RemoteGemini:
		return discoverGeminiEmbeddingCandidates(ctx, cfg)
	default:
		return nil, fmt.Errorf("%s embeddings are not supported", cfg.Provider)
	}
}

func availableCandidates(available map[string]bool, known []remoteEmbeddingCandidate) []remoteEmbeddingCandidate {
	candidates := make([]remoteEmbeddingCandidate, 0, len(known))
	for _, candidate := range known {
		if available[candidate.id] {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func chooseRemoteEmbeddingModel(provider RemoteProvider, candidates []remoteEmbeddingCandidate) string {
	preferred := ""
	switch provider {
	case RemoteOpenAI:
		preferred = "text-embedding-3-small"
	case RemoteOpenRouter:
		preferred = "openai/text-embedding-3-small"
	case RemoteGemini:
		preferred = "gemini-embedding-001"
	}
	for _, candidate := range candidates {
		if candidate.id == preferred {
			return candidate.id
		}
	}
	cheapest := remoteEmbeddingCandidate{pricePerMillionTokens: math.Inf(1)}
	for _, candidate := range candidates {
		if candidate.id != "" && candidate.pricePerMillionTokens >= 0 &&
			!math.IsNaN(candidate.pricePerMillionTokens) &&
			candidate.pricePerMillionTokens < cheapest.pricePerMillionTokens {
			cheapest = candidate
		}
	}
	if math.IsInf(cheapest.pricePerMillionTokens, 1) {
		return ""
	}
	return cheapest.id
}

func discoverOpenRouterEmbeddingCandidates(ctx context.Context, cfg RemoteEngineConfig) ([]remoteEmbeddingCandidate, error) {
	endpoint, err := remoteModelsEndpoint(cfg)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse OpenRouter model catalog URL: %w", err)
	}
	query := parsed.Query()
	query.Set("output_modalities", "embeddings")
	parsed.RawQuery = query.Encode()
	response, err := remoteHTTPRequest(ctx, cfg.HTTPClient, remoteRequest{
		method: http.MethodGet,
		url:    parsed.String(),
		headers: map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + cfg.APIKey,
		},
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, remoteStatusError(response.Status, response.Body)
	}

	var payload struct {
		Data []struct {
			ID           string `json:"id"`
			Architecture struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt string `json:"prompt"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode OpenRouter embedding catalog: %w", err)
	}
	candidates := make([]remoteEmbeddingCandidate, 0, len(payload.Data))
	for _, model := range payload.Data {
		if model.ID == "" || !containsString(model.Architecture.InputModalities, "text") ||
			!containsString(model.Architecture.OutputModalities, "embeddings") {
			continue
		}
		price, err := strconv.ParseFloat(model.Pricing.Prompt, 64)
		if err != nil || price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			continue
		}
		candidates = append(candidates, remoteEmbeddingCandidate{
			id:                    model.ID,
			pricePerMillionTokens: price * 1_000_000,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].pricePerMillionTokens != candidates[j].pricePerMillionTokens {
			return candidates[i].pricePerMillionTokens < candidates[j].pricePerMillionTokens
		}
		return candidates[i].id < candidates[j].id
	})
	return candidates, nil
}

func discoverGeminiEmbeddingCandidates(ctx context.Context, cfg RemoteEngineConfig) ([]remoteEmbeddingCandidate, error) {
	endpoint, err := remoteModelsEndpoint(cfg)
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for page := 0; page < maxRemoteModelCatalogPages; page++ {
		pageURL, err := remoteModelsPageURL(endpoint, RemoteGemini, cursor)
		if err != nil {
			return nil, err
		}
		response, err := remoteHTTPRequest(ctx, cfg.HTTPClient, remoteRequest{
			method: http.MethodGet,
			url:    pageURL,
			headers: map[string]string{
				"Accept":         "application/json",
				"x-goog-api-key": cfg.APIKey,
			},
		})
		if err != nil {
			return nil, err
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, remoteStatusError(response.Status, response.Body)
		}
		var payload struct {
			Models []struct {
				Name                       string   `json:"name"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return nil, fmt.Errorf("decode Gemini embedding catalog: %w", err)
		}
		for _, model := range payload.Models {
			if model.Name == "" || (!containsString(model.SupportedGenerationMethods, "embedContent") &&
				!containsString(model.SupportedGenerationMethods, "batchEmbedContents")) {
				continue
			}
			available[strings.TrimPrefix(model.Name, "models/")] = true
		}
		next := payload.NextPageToken
		if next == "" {
			return availableCandidates(available, []remoteEmbeddingCandidate{
				{id: "gemini-embedding-001", pricePerMillionTokens: 0.15},
			}), nil
		}
		if _, exists := seenCursors[next]; exists {
			return nil, fmt.Errorf("Gemini embedding catalog repeated pagination cursor %q", next)
		}
		seenCursors[next] = struct{}{}
		cursor = next
	}
	return nil, fmt.Errorf("Gemini embedding catalog exceeded %d pages", maxRemoteModelCatalogPages)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (e *RemoteEmbedder) CacheKey() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s:%s:v1", e.config.Provider, e.model)
}

func (e *RemoteEmbedder) CacheIdentity() string {
	return e.CacheKey()
}

func (e *RemoteEmbedder) Embed(parts []PromptPart) ([]float32, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return e.EmbedContext(ctx, parts)
}

func (e *RemoteEmbedder) EmbedContext(ctx context.Context, parts []PromptPart) ([]float32, error) {
	if e == nil {
		return nil, ErrClosed
	}
	text, role := combineRemoteEmbeddingParts(parts)
	if text == "" {
		return nil, fmt.Errorf("ai: no embeddable content in PromptParts")
	}
	vectors, err := e.embedTexts(ctx, []remoteEmbeddingInput{{text: text, role: role}})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (e *RemoteEmbedder) EmbedBatchContext(ctx context.Context, parts []PromptPart) ([][]float32, error) {
	if e == nil {
		return nil, ErrClosed
	}
	inputs := make([]remoteEmbeddingInput, 0, len(parts))
	for _, part := range parts {
		text := formatEmbeddingPartContent(part)
		if text == "" {
			return nil, fmt.Errorf("ai: no embeddable content in batch item")
		}
		inputs = append(inputs, remoteEmbeddingInput{text: text, role: part.Role})
	}
	return e.embedTexts(ctx, inputs)
}

type remoteEmbeddingInput struct {
	text string
	role Role
}

func combineRemoteEmbeddingParts(parts []PromptPart) (string, Role) {
	texts := make([]string, 0, len(parts))
	role := RoleModel
	for _, part := range parts {
		if part.Role == RoleUser {
			role = RoleUser
		}
		if text := formatEmbeddingPartContent(part); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n"), role
}

func (e *RemoteEmbedder) embedTexts(ctx context.Context, inputs []remoteEmbeddingInput) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("ai: no embedding inputs")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([][]float32, len(inputs))
	var missing []remoteEmbeddingInput
	var missingIndices []int
	for i, input := range inputs {
		if strings.TrimSpace(input.text) == "" {
			return nil, fmt.Errorf("ai: embedding input %d is empty", i)
		}
		key := e.CacheKey() + "\x00" + string(input.role) + "\x00" + input.text
		if vector, ok := e.cached(key); ok {
			results[i] = vector
			continue
		}
		missing = append(missing, input)
		missingIndices = append(missingIndices, i)
	}

	for start := 0; start < len(missing); start += maxRemoteEmbeddingBatchSize {
		end := start + maxRemoteEmbeddingBatchSize
		if end > len(missing) {
			end = len(missing)
		}
		batch, err := e.requestBatch(ctx, missing[start:end])
		if err != nil {
			return nil, err
		}
		if len(batch) != end-start {
			return nil, fmt.Errorf("remote embedding batch returned %d vectors for %d inputs", len(batch), end-start)
		}
		for j, vector := range batch {
			index := missingIndices[start+j]
			results[index] = vector
			key := e.CacheKey() + "\x00" + string(missing[start+j].role) + "\x00" + missing[start+j].text
			e.cacheVector(key, vector)
		}
	}
	return results, nil
}

func (e *RemoteEmbedder) cached(key string) ([]float32, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	vector, ok := e.cache[key]
	return append([]float32(nil), vector...), ok
}

func (e *RemoteEmbedder) cacheVector(key string, vector []float32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.cache) >= remoteEmbeddingCacheLimit {
		clear(e.cache)
	}
	e.cache[key] = append([]float32(nil), vector...)
}

func (e *RemoteEmbedder) requestBatch(ctx context.Context, inputs []remoteEmbeddingInput) ([][]float32, error) {
	select {
	case e.limit <- struct{}{}:
		defer func() { <-e.limit }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	switch e.config.Provider {
	case RemoteOpenAI, RemoteOpenRouter:
		return e.requestOpenAICompatibleBatch(ctx, inputs)
	case RemoteGemini:
		return e.requestGeminiBatch(ctx, inputs)
	default:
		return nil, fmt.Errorf("remote embeddings are not supported for %s", e.config.Provider)
	}
}

func (e *RemoteEmbedder) requestOpenAICompatibleBatch(ctx context.Context, inputs []remoteEmbeddingInput) ([][]float32, error) {
	endpoint, err := openAICompatibleEmbeddingURL(e.config)
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(inputs))
	for i, input := range inputs {
		texts[i] = input.text
	}
	body, err := json.Marshal(map[string]any{
		"model": e.model,
		"input": texts,
	})
	if err != nil {
		return nil, fmt.Errorf("encode embedding request: %w", err)
	}
	response, err := remoteHTTPRequest(ctx, e.config.HTTPClient, remoteRequest{
		method: http.MethodPost,
		url:    endpoint,
		headers: map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + e.config.APIKey,
			"Content-Type":  "application/json",
		},
		body: body,
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, remoteStatusError(response.Status, response.Body)
	}
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s embedding response: %w", e.config.Provider, err)
	}
	vectors := make([][]float32, len(inputs))
	seen := make([]bool, len(inputs))
	for _, item := range payload.Data {
		if item.Index < 0 || item.Index >= len(inputs) || seen[item.Index] {
			return nil, fmt.Errorf("%s embedding response contains invalid index %d", e.config.Provider, item.Index)
		}
		vector, err := normalizeRemoteVector(item.Embedding)
		if err != nil {
			return nil, fmt.Errorf("%s embedding response item %d: %w", e.config.Provider, item.Index, err)
		}
		vectors[item.Index] = vector
		seen[item.Index] = true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("%s embedding response omitted input index %d", e.config.Provider, i)
		}
	}
	return vectors, nil
}

func (e *RemoteEmbedder) requestGeminiBatch(ctx context.Context, inputs []remoteEmbeddingInput) ([][]float32, error) {
	endpoint, err := geminiEmbeddingURL(e.config, e.model)
	if err != nil {
		return nil, err
	}
	requests := make([]map[string]any, len(inputs))
	for i, input := range inputs {
		taskType := "RETRIEVAL_DOCUMENT"
		if input.role == RoleUser {
			taskType = "RETRIEVAL_QUERY"
		}
		requests[i] = map[string]any{
			"model":   "models/" + e.model,
			"content": map[string]any{"parts": []map[string]string{{"text": input.text}}},
			"config":  map[string]string{"taskType": taskType},
		}
	}
	body, err := json.Marshal(map[string]any{
		"requests": requests,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Gemini embedding request: %w", err)
	}
	response, err := remoteHTTPRequest(ctx, e.config.HTTPClient, remoteRequest{
		method: http.MethodPost,
		url:    endpoint,
		headers: map[string]string{
			"Accept":         "application/json",
			"x-goog-api-key": e.config.APIKey,
			"Content-Type":   "application/json",
		},
		body: body,
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, remoteStatusError(response.Status, response.Body)
	}
	var payload struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		return nil, fmt.Errorf("decode Gemini embedding response: %w", err)
	}
	if len(payload.Embeddings) != len(inputs) {
		return nil, fmt.Errorf("Gemini embedding response returned %d vectors for %d inputs", len(payload.Embeddings), len(inputs))
	}
	vectors := make([][]float32, len(inputs))
	for i, item := range payload.Embeddings {
		vector, err := normalizeRemoteVector(item.Values)
		if err != nil {
			return nil, fmt.Errorf("Gemini embedding response item %d: %w", i, err)
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func openAICompatibleEmbeddingURL(cfg RemoteEngineConfig) (string, error) {
	var base, providerPath string
	switch cfg.Provider {
	case RemoteOpenAI:
		base = cfg.BaseURL
		providerPath = "/v1"
		if base == "" {
			return "https://api.openai.com/v1/embeddings", nil
		}
	case RemoteOpenRouter:
		base = cfg.BaseURL
		providerPath = "/api/v1"
		if base == "" {
			return "https://openrouter.ai/api/v1/embeddings", nil
		}
	default:
		return "", fmt.Errorf("OpenAI-compatible embeddings are not supported for %s", cfg.Provider)
	}
	if err := validateRemoteBaseURL(base); err != nil {
		return "", err
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages", "/models", "/embeddings"} {
		path = strings.TrimSuffix(path, suffix)
	}
	if !strings.HasSuffix(path, providerPath) {
		path = strings.TrimRight(path, "/") + providerPath
	}
	parsed.Path = strings.TrimRight(path, "/") + "/embeddings"
	parsed.RawQuery = ""
	return parsed.String(), nil
}

func geminiEmbeddingURL(cfg RemoteEngineConfig, model string) (string, error) {
	base := cfg.BaseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta/models"
	}
	if err := validateRemoteBaseURL(base); err != nil {
		return "", err
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/models") {
		path += "/" + url.PathEscape(model)
	} else if strings.HasSuffix(path, "/v1beta") {
		path += "/models/" + url.PathEscape(model)
	} else if marker := strings.LastIndex(path, "/models/"); marker >= 0 {
		path = path[:marker] + "/models/" + url.PathEscape(model)
	} else {
		path += "/v1beta/models/" + url.PathEscape(model)
	}
	parsed.Path = path + ":batchEmbedContents"
	parsed.RawQuery = ""
	return parsed.String(), nil
}

func normalizeRemoteVector(values []float64) ([]float32, error) {
	if len(values) == 0 {
		return nil, errors.New("empty vector")
	}
	var normSquared float64
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("vector contains a non-finite value")
		}
		normSquared += value * value
	}
	if normSquared == 0 || math.IsInf(normSquared, 0) {
		return nil, errors.New("vector has invalid norm")
	}
	inverseNorm := 1 / math.Sqrt(normSquared)
	vector := make([]float32, len(values))
	for i, value := range values {
		vector[i] = float32(value * inverseNorm)
	}
	return vector, nil
}
