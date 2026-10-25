package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CredentialType discriminates between static API keys and refreshable OAuth tokens.
type CredentialType string

const (
	CredentialAPIKey CredentialType = "api_key"
	CredentialOAuth  CredentialType = "oauth"
)

// Credential represents a stored provider credential (API key or OAuth token set).
type Credential struct {
	Type      CredentialType `json:"type"`
	Key       string         `json:"key,omitempty"`
	Access    string         `json:"access,omitempty"`
	Refresh   string         `json:"refresh,omitempty"`
	ExpiresAt int64          `json:"expires_at,omitempty"` // Unix milliseconds
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func (c *Credential) SecretToken() string {
	if c == nil {
		return ""
	}
	if c.Type == CredentialOAuth {
		return c.Access
	}
	return c.Key
}

func (c *Credential) MetaString(key string) string {
	if c == nil || c.Metadata == nil {
		return ""
	}
	v, _ := c.Metadata[key].(string)
	return v
}

func (c *Credential) MetaStrings(key string) []string {
	if c == nil || c.Metadata == nil {
		return nil
	}
	switch v := c.Metadata[key].(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// CredentialStore is the persistence contract used to read and atomically
// refresh stored credentials across concurrent turns.
type CredentialStore interface {
	Read(ctx context.Context, providerID string) (*Credential, error)
	Modify(ctx context.Context, providerID string, fn func(current *Credential) (*Credential, error)) (*Credential, error)
	Delete(ctx context.Context, providerID string) error
}

const (
	defaultOAuthMinimumValidity = 5 * time.Minute
	defaultOAuthRefreshTimeout  = 15 * time.Second
)

// ResolveProviderCredential resolves the stored credential for provider, refreshing
// an expiring OAuth credential inside store.Modify using double-checked locking
// and an uncancellable rotation commit (matching external/pi/packages/ai/src/auth/resolve.ts).
func ResolveProviderCredential(ctx context.Context, provider RemoteProvider, store CredentialStore, client *http.Client) (*Credential, error) {
	if store == nil {
		return nil, fmt.Errorf("no credential store configured for %s", provider)
	}
	stored, err := store.Read(ctx, string(provider))
	if err != nil {
		return nil, fmt.Errorf("read %s credential: %w", provider, err)
	}
	if stored == nil {
		return nil, fmt.Errorf("no stored credential for %s; run `ai login %s` or `ai setup`", provider, provider)
	}
	if stored.Type != CredentialOAuth {
		if strings.TrimSpace(stored.Key) == "" {
			return nil, fmt.Errorf("stored %s API key is empty", provider)
		}
		return stored, nil
	}

	expiresSoon := func(c *Credential) bool {
		if c == nil || c.Type != CredentialOAuth {
			return false
		}
		if c.ExpiresAt <= 0 || c.ExpiresAt == math.MaxInt64 {
			return false
		}
		return time.Now().Add(defaultOAuthMinimumValidity).UnixMilli() >= c.ExpiresAt
	}

	if !expiresSoon(stored) {
		return stored, nil
	}

	refreshed, err := store.Modify(ctx, string(provider), func(current *Credential) (*Credential, error) {
		if current == nil || current.Type != CredentialOAuth {
			return nil, nil
		}
		if !expiresSoon(current) {
			return nil, nil
		}
		// Uncancellable rotation commit: once refresh starts, the provider may rotate
		// the single-use refresh token immediately. Bound only by timeout so a caller
		// abort does not discard the rotated token before persistence.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultOAuthRefreshTimeout)
		defer cancel()
		next, refreshErr := RefreshProviderOAuth(refreshCtx, provider, current, client)
		if refreshErr != nil {
			return nil, fmt.Errorf("OAuth refresh failed for %s: %w", provider, refreshErr)
		}
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	if refreshed == nil {
		return nil, fmt.Errorf("credential for %s was removed during refresh", provider)
	}
	return refreshed, nil
}

// RefreshProviderOAuth executes the provider-specific OAuth refresh flow.
func RefreshProviderOAuth(ctx context.Context, provider RemoteProvider, cred *Credential, client *http.Client) (*Credential, error) {
	if cred == nil {
		return nil, errors.New("cannot refresh nil credential")
	}
	switch provider {
	case RemoteOpenRouter:
		return cred, nil
	case RemoteAnthropic:
		return refreshAnthropicOAuth(ctx, cred, client)
	case RemoteOpenAI:
		return refreshOpenAIOAuth(ctx, cred, client)
	case RemoteCopilot:
		return refreshGitHubCopilotOAuth(ctx, cred, client)
	default:
		return nil, fmt.Errorf("provider %s does not support OAuth token refresh", provider)
	}
}

// AuthenticateProvider runs the interactive authentication flow for provider over term.
func AuthenticateProvider(ctx context.Context, provider RemoteProvider, term TerminalSession, client *http.Client) (*Credential, error) {
	switch provider {
	case RemoteOpenRouter:
		return loginOpenRouter(ctx, term, client)
	case RemoteAnthropic:
		return loginAnthropic(ctx, term, client)
	case RemoteOpenAI:
		return loginOpenAI(ctx, term, client)
	case RemoteCopilot:
		return loginGitHubCopilot(ctx, term, client)
	case RemoteGemini:
		return loginGemini(ctx, term, client)
	default:
		return nil, fmt.Errorf("unsupported remote AI provider %q", provider)
	}
}

// CuratedModelsForProvider returns the default selectable models for provider,
// filtering to credential-specific available models when present (e.g. GitHub Copilot).
func CuratedModelsForProvider(provider RemoteProvider, cred *Credential) []string {
	if cred != nil {
		if avail := cred.MetaStrings("available_models"); len(avail) > 0 {
			return avail
		}
	}
	switch provider {
	case RemoteOpenAI:
		return []string{"gpt-6-luna", "gpt-6-sol", "gpt-6-astra"}
	case RemoteOpenRouter:
		return []string{"openai/gpt-6-luna", "openai/gpt-6-sol", "openai/gpt-6-astra"}
	case RemoteAnthropic:
		return []string{"claude-3-5-haiku-latest", "claude-3-7-sonnet-latest", "claude-sonnet-4-20250514"}
	case RemoteGemini:
		return []string{"gemini-2.5-flash", "gemini-2.5-pro"}
	case RemoteCopilot:
		return []string{"gpt-6-luna", "gpt-6-sol", "gpt-6-astra"}
	default:
		return nil
	}
}

// ============================================================================
// 1. OpenRouter (OAuth PKCE + Manual API Key)
// Reference: external/pi/packages/ai/src/auth/oauth/openrouter.ts
// ============================================================================

var (
	openRouterAuthorizeURL = "https://openrouter.ai/auth"
	openRouterTokenURL     = "https://openrouter.ai/api/v1/auth/keys"
)

func loginOpenRouter(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	method, err := PromptSelect(ctx, term, "Authenticate with OpenRouter", "Choose how to connect OpenRouter.", []string{
		"Sign in with OpenRouter (OAuth PKCE)",
		"Enter OpenRouter API key manually",
	}, 0)
	if err != nil {
		return nil, err
	}
	if method == 1 {
		return promptAndValidateAPIKey(ctx, term, RemoteOpenRouter, "OpenRouter", client)
	}
	return loginOpenRouterOAuth(ctx, term, client)
}

func startLoopbackCallbackServerWithFallback[T any](ctx context.Context, preferredPort int, opts LoopbackCallbackOptions[T]) (*LoopbackCallbackServer[T], error) {
	candidates := []int{preferredPort, 3000, 3001, 53692, 53693, 8088, 8089, 14555}
	var lastErr error
	for _, port := range candidates {
		if port <= 0 {
			continue
		}
		opts.Port = port
		srv, err := StartLoopbackCallbackServer(ctx, opts)
		if err == nil {
			return srv, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("unable to bind any loopback OAuth callback port")
	}
	return nil, lastErr
}

func loginOpenRouterOAuth(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	randID, err := randomUUIDv4()
	if err != nil {
		return nil, err
	}
	cbPath := "/oauth/callback/" + randID

	callback, cbErr := StartLoopbackCallbackServer(ctx, LoopbackCallbackOptions[*Credential]{
		ProviderName: "OpenRouter",
		Host:         "127.0.0.1",
		Port:         0,
		Path:         cbPath,
		Complete: func(_ *http.Request, code string) (*Credential, error) {
			return exchangeOpenRouterCode(ctx, code, verifier, client)
		},
	})
	if cbErr != nil || callback == nil {
		callback, cbErr = startLoopbackCallbackServerWithFallback(ctx, 3000, LoopbackCallbackOptions[*Credential]{
			ProviderName: "OpenRouter",
			Host:         "127.0.0.1",
			Path:         cbPath,
			Complete: func(_ *http.Request, code string) (*Credential, error) {
				return exchangeOpenRouterCode(ctx, code, verifier, client)
			},
		})
	}
	redirectURI := "http://127.0.0.1:3000" + cbPath
	if cbErr == nil && callback != nil {
		redirectURI = callback.RedirectURI
	}

	authURL, err := url.Parse(openRouterAuthorizeURL)
	if err != nil {
		if callback != nil {
			callback.Close()
		}
		return nil, err
	}
	q := url.Values{}
	q.Set("callback_url", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL.RawQuery = q.Encode()

	_, _ = fmt.Fprintf(term, "\n\x1b[1;36mOpenRouter OAuth Sign-In\x1b[0m\n")
	if callback != nil {
		_, _ = fmt.Fprintf(term, "Listening for callback on %s\n", callback.RedirectURI)
	}
	_, _ = fmt.Fprintf(term, "Open this URL in your browser:\n%s\n\n", FormatClickableURL(authURL.String()))

	// 3. Paste events (Ctrl+V / bracketed paste) arrive as raw multi-byte strings
	// without going through handleKeyEvent. When running and no OS PTY is active,
	//HandleEvent routes PasteEvent to writeCommandTerminalInput, which feeds
	// ReadControlInputContext when SubscribeRawInput is active.
	return WaitForCallbackOrManualInput(
		ctx,
		term,
		callback,
		"Complete sign-in in browser, or paste the authorization code / redirect URL here",
		func(input string) (*Credential, error) {
			code, _ := parseOAuthCodeAndState(input)
			if code == "" {
				return nil, errors.New("missing OpenRouter authorization code")
			}
			_, _ = fmt.Fprintln(term, "Exchanging authorization code for an OpenRouter API key...")
			cred, exErr := exchangeOpenRouterCode(ctx, code, verifier, client)
			if exErr != nil {
				_, _ = fmt.Fprintf(term, "\x1b[31m✗ %v\x1b[0m\n", exErr)
				return nil, exErr
			}
			return cred, nil
		},
	)
}

func exchangeOpenRouterCode(ctx context.Context, code, verifier string, client *http.Client) (*Credential, error) {
	bodyBytes, err := json.Marshal(map[string]string{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})
	if err != nil {
		return nil, err
	}
	exCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := remoteHTTPRequest(exCtx, client, remoteRequest{
		method: http.MethodPost,
		url:    openRouterTokenURL,
		headers: map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/json",
		},
		body: bodyBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("OpenRouter OAuth key exchange failed: %w", err)
	}
	parsed, _ := decodeJSONMap(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := extractOAuthErrorDetail(parsed)
		if detail != "" {
			return nil, fmt.Errorf("OpenRouter OAuth key exchange failed (HTTP %d): %s", resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("OpenRouter OAuth key exchange failed (HTTP %d)", resp.StatusCode)
	}
	key, _ := parsed["key"].(string)
	if key == "" {
		return nil, errors.New(`OpenRouter OAuth response carries no "key"`)
	}
	return &Credential{
		Type:      CredentialOAuth,
		Access:    key,
		Refresh:   "",
		ExpiresAt: math.MaxInt64,
	}, nil
}

// ============================================================================
// 2. Anthropic (Claude Pro/Max OAuth + Manual API Key)
// Reference: external/pi/packages/ai/src/auth/oauth/anthropic.ts
// ============================================================================

const (
	anthropicOAuthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	anthropicPreferredPort     = 53692
	anthropicCallbackPath      = "/callback"
	anthropicCopyCodeRedirect  = "https://platform.claude.com/oauth/code/callback"
	anthropicOAuthScopes       = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	anthropicClaudeCodeVersion = "2.1.280"
)

var (
	anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	anthropicTokenURL     = "https://platform.claude.com/v1/oauth/token"
)

func loginAnthropic(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	method, err := PromptSelect(ctx, term, "Authenticate with Anthropic", "Choose how to connect Anthropic Claude.", []string{
		"Claude Pro/Max subscription (Browser OAuth)",
		"Claude Pro/Max subscription (Copy-code headless OAuth)",
		"Enter Anthropic API key manually",
	}, 0)
	if err != nil {
		return nil, err
	}
	switch method {
	case 0:
		return loginAnthropicOAuthBrowser(ctx, term, client)
	case 1:
		return loginAnthropicOAuthCopyCode(ctx, term, client)
	case 2:
		return promptAndValidateAPIKey(ctx, term, RemoteAnthropic, "Anthropic Claude", client)
	default:
		return nil, fmt.Errorf("invalid Anthropic login selection %d", method)
	}
}

func loginAnthropicOAuthBrowser(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	callback, err := StartLoopbackCallbackServer(ctx, LoopbackCallbackOptions[string]{
		ProviderName: "Anthropic",
		Host:         "127.0.0.1",
		Port:         anthropicPreferredPort,
		Path:         anthropicCallbackPath,
		RedirectHost: "localhost",
		State:        verifier,
		Complete: func(_ *http.Request, code string) (string, error) {
			return code, nil
		},
	})
	if err != nil || callback == nil {
		callback, _ = StartLoopbackCallbackServer(ctx, LoopbackCallbackOptions[string]{
			ProviderName: "Anthropic",
			Host:         "127.0.0.1",
			Port:         0,
			Path:         anthropicCallbackPath,
			RedirectHost: "localhost",
			State:        verifier,
			Complete: func(_ *http.Request, code string) (string, error) {
				return code, nil
			},
		})
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", anthropicPreferredPort, anthropicCallbackPath)
	if callback != nil {
		redirectURI = callback.RedirectURI
	}

	authURL := buildAnthropicAuthorizeURL(challenge, verifier, redirectURI)
	_, _ = fmt.Fprintf(term, "\n\x1b[1;36mAnthropic Claude OAuth Sign-In\x1b[0m\n")
	_, _ = fmt.Fprintf(term, "Complete login in your browser:\n%s\n\n", FormatClickableURL(authURL))

	code, err := WaitForCallbackOrManualInput(
		ctx,
		term,
		callback,
		"Complete login in browser, or paste the authorization code / redirect URL here",
		func(input string) (string, error) {
			parsedCode, parsedState := parseOAuthCodeAndState(input)
			if parsedState != "" && parsedState != verifier {
				return "", errors.New("OAuth state mismatch")
			}
			if parsedCode == "" {
				return "", errors.New("missing Anthropic authorization code")
			}
			return parsedCode, nil
		},
	)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintln(term, "Exchanging authorization code for Anthropic OAuth tokens...")
	return exchangeAnthropicOAuthCode(ctx, code, verifier, verifier, redirectURI, client)
}

func loginAnthropicOAuthCopyCode(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	authURL := buildAnthropicAuthorizeURL(challenge, verifier, anthropicCopyCodeRedirect)
	_, _ = fmt.Fprintf(term, "\n\x1b[1;36mAnthropic Claude Copy-Code Sign-In\x1b[0m\n")
	_, _ = fmt.Fprintf(term, "Open this URL in your browser, sign in, and copy the code shown:\n%s\n\n", FormatClickableURL(authURL))

	input, err := PromptLine(ctx, term, "", "Paste the code Anthropic shows (code#state)", false)
	if err != nil {
		return nil, err
	}
	code, state := parseOAuthCodeAndState(input)
	if state != "" && state != verifier {
		return nil, errors.New("OAuth state mismatch")
	}
	if code == "" {
		return nil, errors.New("missing Anthropic authorization code")
	}
	if state == "" {
		state = verifier
	}
	_, _ = fmt.Fprintln(term, "Exchanging authorization code for Anthropic OAuth tokens...")
	return exchangeAnthropicOAuthCode(ctx, code, state, verifier, anthropicCopyCodeRedirect, client)
}

func buildAnthropicAuthorizeURL(challenge, state, redirectURI string) string {
	q := url.Values{}
	q.Set("code", "true")
	q.Set("client_id", anthropicOAuthClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", anthropicOAuthScopes)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	return anthropicAuthorizeURL + "?" + q.Encode()
}

func exchangeAnthropicOAuthCode(ctx context.Context, code, state, verifier, redirectURI string, client *http.Client) (*Credential, error) {
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     anthropicOAuthClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return nil, err
	}
	return postAnthropicTokenRequest(ctx, payload, client)
}

func refreshAnthropicOAuth(ctx context.Context, cred *Credential, client *http.Client) (*Credential, error) {
	if cred == nil || cred.Refresh == "" {
		return nil, errors.New("missing Anthropic refresh token")
	}
	// Note: scope is intentionally omitted from refresh token requests (verified by anthropic-oauth.test.ts).
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     anthropicOAuthClientID,
		"refresh_token": cred.Refresh,
	})
	if err != nil {
		return nil, err
	}
	return postAnthropicTokenRequest(ctx, payload, client)
}

func postAnthropicTokenRequest(ctx context.Context, payload []byte, client *http.Client) (*Credential, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := remoteHTTPRequest(reqCtx, client, remoteRequest{
		method: http.MethodPost,
		url:    anthropicTokenURL,
		headers: map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/json",
		},
		body: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("Anthropic OAuth token request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Anthropic OAuth token request failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(resp.Body, &tokenData); err != nil {
		return nil, fmt.Errorf("Anthropic OAuth returned invalid JSON: %w", err)
	}
	if tokenData.AccessToken == "" || tokenData.RefreshToken == "" || tokenData.ExpiresIn <= 0 {
		return nil, errors.New("Anthropic OAuth token response missing required fields")
	}
	return &Credential{
		Type:      CredentialOAuth,
		Access:    tokenData.AccessToken,
		Refresh:   tokenData.RefreshToken,
		ExpiresAt: time.Now().UnixMilli() + tokenData.ExpiresIn*1000 - int64(5*time.Minute/time.Millisecond),
	}, nil
}

func isAnthropicOAuthToken(cred *Credential, apiKey string) bool {
	if cred != nil && cred.Type == CredentialOAuth {
		return true
	}
	return strings.Contains(apiKey, "sk-ant-oat")
}

// Claude Code canonical tool names for Anthropic OAuth stealth compatibility
// Reference: external/pi/packages/ai/src/api/anthropic-messages.ts:95-126
var claudeCodeCanonicalTools = []string{
	"Read", "Write", "Edit", "Bash", "Grep", "Glob",
	"AskUserQuestion", "EnterPlanMode", "ExitPlanMode", "KillShell",
	"NotebookEdit", "Skill", "Task", "TaskOutput", "TodoWrite",
	"WebFetch", "WebSearch",
}

func toClaudeCodeToolName(name string) string {
	for _, cc := range claudeCodeCanonicalTools {
		if strings.EqualFold(name, cc) {
			return cc
		}
	}
	return name
}

func fromClaudeCodeToolName(name string, declaredTools []map[string]any) string {
	for _, decl := range declaredTools {
		if fn, ok := decl["function"].(map[string]any); ok {
			if orig, ok := fn["name"].(string); ok && strings.EqualFold(orig, name) {
				return orig
			}
		}
	}
	return name
}

// ============================================================================
// 3. OpenAI (Sign in with ChatGPT + Device Code + Manual API Key)
// Reference: external/pi/packages/ai/src/auth/oauth/openai-chatgpt.ts & openai-codex.ts
// ============================================================================

const (
	openAIDynamicClientID   = "dynamic_agent_client"
	openAICodexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAIChatGPTResource   = "https://api.openai.com/v1"
	openAICallbackPort      = 1455
	openAICallbackPath      = "/auth/callback"
	openAIDirectTokenScope  = "chatgpt.tokens.use.direct"
	openAIChatGPTScopes     = "openid profile email offline_access resource.invoke " + openAIDirectTokenScope
	openAIChatGPTExpirySkew = 3 * time.Minute
)

var (
	openAIChatGPTAuthorizeURL = "https://auth.openai.com/api/accounts/authorize"
	openAIChatGPTTokenURL     = "https://auth.openai.com/api/accounts/oauth/token"
	openAIDeviceUserCodeURL   = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	openAIDeviceTokenURL      = "https://auth.openai.com/api/accounts/deviceauth/token"
	openAIDeviceVerifyURI     = "https://auth.openai.com/codex/device"
	openAIDeviceRedirectURI   = "https://auth.openai.com/deviceauth/callback"
	openAICodexTokenURL       = "https://auth.openai.com/oauth/token"
)

func loginOpenAI(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	method, err := PromptSelect(ctx, term, "Authenticate with OpenAI", "Choose how to connect OpenAI.", []string{
		"Sign in with ChatGPT (Browser OAuth)",
		"Sign in with ChatGPT (Headless Device Code)",
		"Enter OpenAI API key manually",
	}, 0)
	if err != nil {
		return nil, err
	}
	switch method {
	case 0:
		return loginOpenAIChatGPTBrowser(ctx, term, client)
	case 1:
		return loginOpenAIDeviceCode(ctx, term, client)
	case 2:
		return promptAndValidateAPIKey(ctx, term, RemoteOpenAI, "OpenAI", client)
	default:
		return nil, fmt.Errorf("invalid OpenAI login selection %d", method)
	}
}

type openAICallbackAuth struct {
	Code     string
	ClientID string
}

func loginOpenAIChatGPTBrowser(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	nonce, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	deviceUUID, err := randomUUIDv4()
	if err != nil {
		return nil, err
	}
	hostID := "urn:uuid:" + strings.ToLower(deviceUUID)
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", openAICallbackPort, openAICallbackPath)

	callback, _ := StartLoopbackCallbackServer(ctx, LoopbackCallbackOptions[openAICallbackAuth]{
		ProviderName: "OpenAI",
		Host:         "127.0.0.1",
		Port:         openAICallbackPort,
		Path:         openAICallbackPath,
		State:        state,
		Complete: func(r *http.Request, code string) (openAICallbackAuth, error) {
			clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
			if clientID == "" {
				return openAICallbackAuth{}, errors.New("OpenAI OAuth registration callback did not contain an issued client ID")
			}
			return openAICallbackAuth{Code: code, ClientID: clientID}, nil
		},
	})

	q := url.Values{}
	q.Set("client_id", openAIDynamicClientID)
	q.Set("agent_name_hint", "Kabibi")
	q.Set("ext_agent_host_id", hostID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("resource", openAIChatGPTResource)
	q.Set("scope", openAIChatGPTScopes)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("nonce", nonce)
	authURL := openAIChatGPTAuthorizeURL + "?" + q.Encode()

	_, _ = fmt.Fprintf(term, "\n\x1b[1;36mOpenAI ChatGPT Sign-In\x1b[0m\n")
	_, _ = fmt.Fprintf(term, "Complete sign-in in your browser:\n%s\n\n", FormatClickableURL(authURL))

	res, err := WaitForCallbackOrManualInput(
		ctx,
		term,
		callback,
		"Complete login in browser, or paste the final redirect URL here",
		func(input string) (openAICallbackAuth, error) {
			parsed, parseErr := url.Parse(strings.TrimSpace(input))
			if parseErr != nil || parsed.Scheme == "" {
				return openAICallbackAuth{}, errors.New("paste the full callback URL from the browser")
			}
			if errMsg := parsed.Query().Get("error"); errMsg != "" {
				return openAICallbackAuth{}, fmt.Errorf("ChatGPT authorization failed: %s", errMsg)
			}
			if parsed.Query().Get("state") != state {
				return openAICallbackAuth{}, errors.New("OAuth state mismatch")
			}
			code := parsed.Query().Get("code")
			if code == "" {
				return openAICallbackAuth{}, errors.New("missing authorization code")
			}
			clientID := strings.TrimSpace(parsed.Query().Get("client_id"))
			if clientID == "" {
				return openAICallbackAuth{}, errors.New("OpenAI OAuth registration callback did not contain an issued client ID")
			}
			return openAICallbackAuth{Code: code, ClientID: clientID}, nil
		},
	)
	if err != nil {
		return nil, err
	}

	_, _ = fmt.Fprintln(term, "Exchanging authorization code for OpenAI tokens...")
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", res.ClientID)
	form.Set("code", res.Code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	form.Set("resource", openAIChatGPTResource)
	return requestOpenAIChatGPTToken(ctx, form, res.ClientID, true, client)
}

func requestOpenAIChatGPTToken(ctx context.Context, form url.Values, clientID string, requireIDToken bool, client *http.Client) (*Credential, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := remoteHTTPRequest(reqCtx, client, remoteRequest{
		method: http.MethodPost,
		url:    openAIChatGPTTokenURL,
		headers: map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/x-www-form-urlencoded",
		},
		body: []byte(form.Encode()),
	})
	if err != nil {
		return nil, fmt.Errorf("OpenAI OAuth token request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenAI OAuth token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	var tok struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
		IDToken      string  `json:"id_token"`
		Scope        string  `json:"scope"`
	}
	if err := json.Unmarshal(resp.Body, &tok); err != nil {
		return nil, fmt.Errorf("OpenAI OAuth token response invalid JSON: %w", err)
	}
	if requireIDToken && strings.TrimSpace(tok.IDToken) == "" {
		return nil, errors.New("OpenAI OAuth token response did not contain an ID token")
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return nil, errors.New("OpenAI OAuth token response has invalid access_token")
	}
	if strings.TrimSpace(tok.RefreshToken) == "" {
		return nil, errors.New("OpenAI OAuth token response has invalid refresh_token")
	}
	if tok.ExpiresIn <= 0 {
		return nil, errors.New("OpenAI OAuth token response has invalid expires_in")
	}
	scopes := strings.Fields(tok.Scope)
	hasDirect := false
	for _, s := range scopes {
		if s == openAIDirectTokenScope {
			hasDirect = true
			break
		}
	}
	if !hasDirect {
		return nil, fmt.Errorf("OpenAI OAuth grant did not include %s", openAIDirectTokenScope)
	}
	return &Credential{
		Type:      CredentialOAuth,
		Access:    tok.AccessToken,
		Refresh:   tok.RefreshToken,
		ExpiresAt: time.Now().UnixMilli() + int64(tok.ExpiresIn*1000) - int64(openAIChatGPTExpirySkew/time.Millisecond),
		Metadata: map[string]any{
			"client_id": clientID,
			"scopes":    scopes,
			"flow":      "chatgpt",
		},
	}, nil
}

func loginOpenAIDeviceCode(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	bodyBytes, _ := json.Marshal(map[string]string{"client_id": openAICodexClientID})
	resp, err := remoteHTTPRequest(ctx, client, remoteRequest{
		method:  http.MethodPost,
		url:     openAIDeviceUserCodeURL,
		headers: map[string]string{"Content-Type": "application/json"},
		body:    bodyBytes,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenAI device code request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	var dev struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     any    `json:"interval"`
	}
	if err := json.Unmarshal(resp.Body, &dev); err != nil || dev.DeviceAuthID == "" || dev.UserCode == "" {
		return nil, fmt.Errorf("invalid OpenAI device code response: %s", string(resp.Body))
	}
	intervalSec := 5
	switch v := dev.Interval.(type) {
	case float64:
		intervalSec = int(v)
	case string:
		if n, convErr := strconv.Atoi(strings.TrimSpace(v)); convErr == nil {
			intervalSec = n
		}
	}

	_, _ = fmt.Fprintf(term, "\n\x1b[1;36mOpenAI Device Code Sign-In\x1b[0m\n")
	_, _ = fmt.Fprintf(term, "Visit: \x1b[4;34m%s\x1b[0m\n", openAIDeviceVerifyURI)
	_, _ = fmt.Fprintf(term, "Enter code: \x1b[1;33m%s\x1b[0m\n", dev.UserCode)
	_, _ = fmt.Fprintln(term, "Waiting for authorization...")

	type devCodeSuccess struct {
		AuthorizationCode string
		CodeVerifier      string
	}
	polled, err := PollDeviceCodeFlow(ctx, DeviceCodePollOptions[devCodeSuccess]{
		IntervalSeconds:  intervalSec,
		ExpiresInSeconds: 15 * 60,
		Poll: func(pCtx context.Context) (DeviceCodePollResult[devCodeSuccess], error) {
			pBody, _ := json.Marshal(map[string]string{
				"device_auth_id": dev.DeviceAuthID,
				"user_code":      dev.UserCode,
			})
			pResp, pErr := remoteHTTPRequest(pCtx, client, remoteRequest{
				method:  http.MethodPost,
				url:     openAIDeviceTokenURL,
				headers: map[string]string{"Content-Type": "application/json"},
				body:    pBody,
			})
			if pErr != nil {
				return DeviceCodePollResult[devCodeSuccess]{}, pErr
			}
			if pResp.StatusCode >= 200 && pResp.StatusCode < 300 {
				var okBody struct {
					AuthorizationCode string `json:"authorization_code"`
					CodeVerifier      string `json:"code_verifier"`
				}
				if json.Unmarshal(pResp.Body, &okBody) == nil && okBody.AuthorizationCode != "" && okBody.CodeVerifier != "" {
					return DeviceCodePollResult[devCodeSuccess]{
						Status: DevicePollComplete,
						Value:  devCodeSuccess{AuthorizationCode: okBody.AuthorizationCode, CodeVerifier: okBody.CodeVerifier},
					}, nil
				}
				return DeviceCodePollResult[devCodeSuccess]{Status: DevicePollFailed, Message: "invalid OpenAI device token response"}, nil
			}
			if pResp.StatusCode == http.StatusForbidden || pResp.StatusCode == http.StatusNotFound {
				return DeviceCodePollResult[devCodeSuccess]{Status: DevicePollPending}, nil
			}
			parsed, _ := decodeJSONMap(pResp.Body)
			errCode := extractOAuthErrorCode(parsed)
			switch errCode {
			case "deviceauth_authorization_pending":
				return DeviceCodePollResult[devCodeSuccess]{Status: DevicePollPending}, nil
			case "slow_down":
				return DeviceCodePollResult[devCodeSuccess]{Status: DevicePollSlowDown}, nil
			default:
				return DeviceCodePollResult[devCodeSuccess]{
					Status:  DevicePollFailed,
					Message: fmt.Sprintf("OpenAI device auth failed (%d): %s", pResp.StatusCode, strings.TrimSpace(string(pResp.Body))),
				}, nil
			}
		},
	})
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", openAICodexClientID)
	form.Set("code", polled.AuthorizationCode)
	form.Set("code_verifier", polled.CodeVerifier)
	form.Set("redirect_uri", openAIDeviceRedirectURI)
	return requestOpenAICodexToken(ctx, form, client)
}

func requestOpenAICodexToken(ctx context.Context, form url.Values, client *http.Client) (*Credential, error) {
	resp, err := remoteHTTPRequest(ctx, client, remoteRequest{
		method:  http.MethodPost,
		url:     openAICodexTokenURL,
		headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		body:    []byte(form.Encode()),
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenAI Codex token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	var tok struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(resp.Body, &tok); err != nil || tok.AccessToken == "" || tok.RefreshToken == "" || tok.ExpiresIn <= 0 {
		return nil, errors.New("OpenAI Codex token response missing fields")
	}
	meta := map[string]any{
		"client_id": openAICodexClientID,
		"flow":      "codex",
	}
	if accountID := extractOpenAIChatGPTAccountID(tok.AccessToken); accountID != "" {
		meta["account_id"] = accountID
	}
	return &Credential{
		Type:      CredentialOAuth,
		Access:    tok.AccessToken,
		Refresh:   tok.RefreshToken,
		ExpiresAt: time.Now().UnixMilli() + int64(tok.ExpiresIn*1000) - int64(openAIChatGPTExpirySkew/time.Millisecond),
		Metadata:  meta,
	}, nil
}

func extractOpenAIChatGPTAccountID(jwtToken string) string {
	parts := strings.Split(jwtToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	if authObj, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if id, ok := authObj["chatgpt_account_id"].(string); ok {
			return id
		}
	}
	return ""
}

func refreshOpenAIOAuth(ctx context.Context, cred *Credential, client *http.Client) (*Credential, error) {
	if cred == nil || cred.Refresh == "" {
		return nil, errors.New("missing OpenAI refresh token")
	}
	flow := cred.MetaString("flow")
	clientID := cred.MetaString("client_id")
	if flow == "codex" {
		if clientID == "" {
			clientID = openAICodexClientID
		}
		form := url.Values{}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", cred.Refresh)
		form.Set("client_id", clientID)
		return requestOpenAICodexToken(ctx, form, client)
	}
	if clientID == "" {
		return nil, errors.New("stored OpenAI OAuth credential does not contain an issued client ID; reconnect ChatGPT")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", cred.Refresh)
	form.Set("resource", openAIChatGPTResource)
	return requestOpenAIChatGPTToken(ctx, form, clientID, false, client)
}

func isOpenAIChatGPTOAuth(cred *Credential, _ string) bool {
	return cred != nil && cred.Type == CredentialOAuth
}

// ============================================================================
// 4. GitHub Copilot (Device-Code OAuth + Session Token + Dynamic Proxy Endpoint)
// Reference: external/pi/packages/ai/src/auth/oauth/github-copilot.ts
// ============================================================================

const (
	copilotClientID   = "Iv1.b507a08c87ecfe98"
	copilotAPIVersion = "2026-06-01"
)

var copilotStaticHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

var copilotProxyEpPattern = regexp.MustCompile(`proxy-ep=([^;]+)`)

func normalizeGitHubDomain(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "github.com", nil
	}
	raw := trimmed
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("invalid GitHub Enterprise URL/domain %q", input)
	}
	return parsed.Hostname(), nil
}

func getGitHubCopilotURLs(domain string) (deviceCodeURL, accessTokenURL, copilotTokenURL string) {
	if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		base := strings.TrimRight(domain, "/")
		return base + "/login/device/code", base + "/login/oauth/access_token", base + "/copilot_internal/v2/token"
	}
	return fmt.Sprintf("https://%s/login/device/code", domain),
		fmt.Sprintf("https://%s/login/oauth/access_token", domain),
		fmt.Sprintf("https://api.%s/copilot_internal/v2/token", domain)
}

// DeriveGitHubCopilotBaseURL extracts `proxy-ep=...` from a Copilot session token
// and converts `proxy.<host>` to `https://api.<host>`.
func DeriveGitHubCopilotBaseURL(token string, enterpriseDomain string) string {
	if token != "" {
		if m := copilotProxyEpPattern.FindStringSubmatch(token); len(m) == 2 {
			proxyHost := strings.TrimSpace(m[1])
			if strings.HasPrefix(proxyHost, "http://") || strings.HasPrefix(proxyHost, "https://") {
				return strings.TrimRight(proxyHost, "/")
			}
			apiHost := strings.TrimPrefix(proxyHost, "proxy.")
			if apiHost != proxyHost {
				apiHost = "api." + apiHost
			}
			return "https://" + apiHost
		}
	}
	if enterpriseDomain != "" && enterpriseDomain != "github.com" {
		if strings.HasPrefix(enterpriseDomain, "http://") || strings.HasPrefix(enterpriseDomain, "https://") {
			return strings.TrimRight(enterpriseDomain, "/")
		}
		return "https://copilot-api." + enterpriseDomain
	}
	return "https://api.individual.githubcopilot.com"
}

func loginGitHubCopilot(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	domainInput, err := PromptLine(ctx, term, "Authenticate with GitHub Copilot", "GitHub Enterprise domain (leave blank for github.com)", false)
	if err != nil {
		return nil, err
	}
	domain, err := normalizeGitHubDomain(domainInput)
	if err != nil {
		return nil, err
	}
	var enterpriseDomain string
	if domain != "github.com" {
		enterpriseDomain = domain
	}

	deviceCodeURL, accessTokenURL, _ := getGitHubCopilotURLs(domain)
	form := url.Values{}
	form.Set("client_id", copilotClientID)
	form.Set("scope", "read:user")
	resp, err := remoteHTTPRequest(ctx, client, remoteRequest{
		method: http.MethodPost,
		url:    deviceCodeURL,
		headers: map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/x-www-form-urlencoded",
			"User-Agent":   copilotStaticHeaders["User-Agent"],
		},
		body: []byte(form.Encode()),
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub device code request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	var dev struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := json.Unmarshal(resp.Body, &dev); err != nil || dev.DeviceCode == "" || dev.UserCode == "" || dev.VerificationURI == "" || dev.ExpiresIn <= 0 {
		return nil, errors.New("invalid GitHub device code response fields")
	}
	parsedURI, err := url.Parse(dev.VerificationURI)
	if err != nil || (parsedURI.Scheme != "https" && parsedURI.Scheme != "http") {
		return nil, errors.New("untrusted verification_uri in GitHub device code response")
	}

	_, _ = fmt.Fprintf(term, "\n\x1b[1;36mGitHub Copilot Device Sign-In\x1b[0m\n")
	_, _ = fmt.Fprintf(term, "Visit: \x1b[4;34m%s\x1b[0m\n", parsedURI.String())
	_, _ = fmt.Fprintf(term, "Enter code: \x1b[1;33m%s\x1b[0m\n", dev.UserCode)
	_, _ = fmt.Fprintln(term, "Waiting for GitHub authorization...")
	if err := PromptOpenURL(ctx, term, "Press Enter to open the GitHub verification page in your browser (Esc to cancel)."); err != nil {
		return nil, err
	}

	githubAccessToken, err := PollDeviceCodeFlow(ctx, DeviceCodePollOptions[string]{
		IntervalSeconds:  dev.Interval,
		ExpiresInSeconds: dev.ExpiresIn,
		WaitBeforeFirst:  true,
		Poll: func(pCtx context.Context) (DeviceCodePollResult[string], error) {
			pForm := url.Values{}
			pForm.Set("client_id", copilotClientID)
			pForm.Set("device_code", dev.DeviceCode)
			pForm.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
			pResp, pErr := remoteHTTPRequest(pCtx, client, remoteRequest{
				method: http.MethodPost,
				url:    accessTokenURL,
				headers: map[string]string{
					"Accept":       "application/json",
					"Content-Type": "application/x-www-form-urlencoded",
					"User-Agent":   copilotStaticHeaders["User-Agent"],
				},
				body: []byte(pForm.Encode()),
			})
			if pErr != nil {
				return DeviceCodePollResult[string]{}, pErr
			}
			parsed, _ := decodeJSONMap(pResp.Body)
			if tok, ok := parsed["access_token"].(string); ok && tok != "" {
				return DeviceCodePollResult[string]{Status: DevicePollComplete, Value: tok}, nil
			}
			if errCode, ok := parsed["error"].(string); ok && errCode != "" {
				switch errCode {
				case "authorization_pending":
					return DeviceCodePollResult[string]{Status: DevicePollPending}, nil
				case "slow_down":
					iv := 0
					if n, ok := parsed["interval"].(float64); ok {
						iv = int(n)
					}
					return DeviceCodePollResult[string]{Status: DevicePollSlowDown, IntervalSeconds: iv}, nil
				default:
					desc, _ := parsed["error_description"].(string)
					if desc != "" {
						return DeviceCodePollResult[string]{Status: DevicePollFailed, Message: fmt.Sprintf("device flow failed: %s: %s", errCode, desc)}, nil
					}
					return DeviceCodePollResult[string]{Status: DevicePollFailed, Message: "device flow failed: " + errCode}, nil
				}
			}
			return DeviceCodePollResult[string]{Status: DevicePollFailed, Message: "invalid GitHub device token response"}, nil
		},
	})
	if err != nil {
		return nil, err
	}

	_, _ = fmt.Fprintln(term, "Exchanging GitHub token for Copilot session token and model catalog...")
	cred, err := exchangeGitHubCopilotSessionToken(ctx, githubAccessToken, enterpriseDomain, client)
	if err != nil {
		return nil, err
	}
	availModels, policyModels, err := fetchGitHubCopilotModelCatalog(ctx, cred.Access, enterpriseDomain, client)
	if err == nil {
		if len(policyModels) > 0 {
			_, _ = fmt.Fprintln(term, "Enabling Copilot model policies...")
			enabled := enableGitHubCopilotModelPolicies(ctx, cred.Access, enterpriseDomain, policyModels, client)
			availModels = mergeUniqueStrings(availModels, enabled)
		}
		if len(availModels) > 0 {
			cred.Metadata["available_models"] = availModels
		}
	}
	return cred, nil
}

func exchangeGitHubCopilotSessionToken(ctx context.Context, githubRefreshToken, enterpriseDomain string, client *http.Client) (*Credential, error) {
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	_, _, copilotTokenURL := getGitHubCopilotURLs(domain)
	headers := map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + githubRefreshToken,
	}
	for k, v := range copilotStaticHeaders {
		headers[k] = v
	}
	resp, err := remoteHTTPRequest(ctx, client, remoteRequest{
		method:  http.MethodGet,
		url:     copilotTokenURL,
		headers: headers,
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Copilot token exchange failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	var tok struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.Body, &tok); err != nil || tok.Token == "" || tok.ExpiresAt <= 0 {
		return nil, errors.New("invalid Copilot token response fields")
	}
	meta := map[string]any{}
	if enterpriseDomain != "" {
		meta["enterprise_url"] = enterpriseDomain
	}
	return &Credential{
		Type:      CredentialOAuth,
		Access:    tok.Token,
		Refresh:   githubRefreshToken,
		ExpiresAt: tok.ExpiresAt*1000 - int64(5*time.Minute/time.Millisecond),
		Metadata:  meta,
	}, nil
}

func refreshGitHubCopilotOAuth(ctx context.Context, cred *Credential, client *http.Client) (*Credential, error) {
	if cred == nil || cred.Refresh == "" {
		return nil, errors.New("missing GitHub Copilot refresh token")
	}
	enterpriseDomain := cred.MetaString("enterprise_url")
	next, err := exchangeGitHubCopilotSessionToken(ctx, cred.Refresh, enterpriseDomain, client)
	if err != nil {
		return nil, err
	}
	avail, _, err := fetchGitHubCopilotModelCatalog(ctx, next.Access, enterpriseDomain, client)
	if err != nil {
		return nil, err
	}
	next.Metadata["available_models"] = avail
	return next, nil
}

func fetchGitHubCopilotModelCatalog(ctx context.Context, copilotToken, enterpriseDomain string, client *http.Client) (available []string, policyUnconfigured []string, err error) {
	baseURL := DeriveGitHubCopilotBaseURL(copilotToken, enterpriseDomain)
	allowPolicyFallback := baseURL == "https://api.individual.githubcopilot.com"
	headers := map[string]string{
		"Accept":               "application/json",
		"Authorization":        "Bearer " + copilotToken,
		"X-GitHub-Api-Version": copilotAPIVersion,
	}
	for k, v := range copilotStaticHeaders {
		headers[k] = v
	}
	resp, err := remoteHTTPRequest(ctx, client, remoteRequest{
		method:  http.MethodGet,
		url:     baseURL + "/models",
		headers: headers,
	})
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("Copilot models catalog returned %s: %s", resp.Status, strings.TrimSpace(string(resp.Body)))
	}
	var payload struct {
		Data []struct {
			ID                 string `json:"id"`
			ModelPickerEnabled bool   `json:"model_picker_enabled"`
			Policy             *struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities *struct {
				Supports *struct {
					ToolCalls *bool `json:"tool_calls"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return nil, nil, errors.New("invalid Copilot models response")
	}

	type modelEntry struct {
		id            string
		pickerEnabled bool
		policyState   string
	}
	var entries []modelEntry
	for _, item := range payload.Data {
		if item.ID == "" {
			continue
		}
		if item.Capabilities != nil && item.Capabilities.Supports != nil && item.Capabilities.Supports.ToolCalls != nil && !*item.Capabilities.Supports.ToolCalls {
			continue
		}
		state := ""
		if item.Policy != nil {
			state = item.Policy.State
		}
		entries = append(entries, modelEntry{id: item.ID, pickerEnabled: item.ModelPickerEnabled, policyState: state})
	}
	var pickerIDs []string
	for _, e := range entries {
		if e.pickerEnabled && e.policyState != "disabled" {
			pickerIDs = append(pickerIDs, e.id)
		}
	}
	useFallback := allowPolicyFallback && len(pickerIDs) == 0
	if len(pickerIDs) > 0 || !allowPolicyFallback {
		available = pickerIDs
	} else {
		for _, e := range entries {
			if e.policyState == "enabled" {
				available = append(available, e.id)
			}
		}
	}
	for _, e := range entries {
		if e.policyState == "unconfigured" && (e.pickerEnabled || useFallback) {
			policyUnconfigured = append(policyUnconfigured, e.id)
		}
	}
	return available, policyUnconfigured, nil
}

func enableGitHubCopilotModelPolicies(ctx context.Context, copilotToken, enterpriseDomain string, modelIDs []string, client *http.Client) []string {
	baseURL := DeriveGitHubCopilotBaseURL(copilotToken, enterpriseDomain)
	var enabled []string
	for _, id := range modelIDs {
		headers := map[string]string{
			"Content-Type":       "application/json",
			"Authorization":      "Bearer " + copilotToken,
			"openai-intent":      "chat-policy",
			"x-interaction-type": "chat-policy",
		}
		for k, v := range copilotStaticHeaders {
			headers[k] = v
		}
		resp, err := remoteHTTPRequest(ctx, client, remoteRequest{
			method:  http.MethodPost,
			url:     baseURL + "/models/" + id + "/policy",
			headers: headers,
			body:    []byte(`{"state":"enabled"}`),
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			break
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			enabled = append(enabled, id)
		}
	}
	return enabled
}

// BuildCopilotDynamicHeaders returns per-request Copilot headers (`X-Initiator`,
// `Openai-Intent`, and `Copilot-Vision-Request`) matching github-copilot-headers.ts.
func BuildCopilotDynamicHeaders(parts []PromptPart) map[string]string {
	initiator := "user"
	if len(parts) > 0 && parts[len(parts)-1].Role != RoleUser {
		initiator = "agent"
	}
	hasVision := false
	for _, p := range parts {
		if p.ImageBase64 != "" {
			hasVision = true
			break
		}
	}
	headers := map[string]string{
		"X-Initiator":   initiator,
		"Openai-Intent": "conversation-edits",
	}
	for k, v := range copilotStaticHeaders {
		headers[k] = v
	}
	if hasVision {
		headers["Copilot-Vision-Request"] = "true"
	}
	return headers
}

// ============================================================================
// 5. Google Gemini (API Key Only)
// Reference: external/pi/packages/ai/src/providers/google.ts
// ============================================================================

func loginGemini(ctx context.Context, term TerminalSession, client *http.Client) (*Credential, error) {
	return promptAndValidateAPIKey(ctx, term, RemoteGemini, "Google Gemini", client)
}

func promptAndValidateAPIKey(ctx context.Context, term TerminalSession, provider RemoteProvider, displayName string, client *http.Client) (*Credential, error) {
	apiKey, err := PromptLine(ctx, term, displayName+" API Key", "Enter API key for "+displayName, true)
	if err != nil {
		return nil, err
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("%s API key must not be empty", displayName)
	}
	_, _ = fmt.Fprintf(term, "Validating %s credential...\n", displayName)
	if err := ValidateRemoteCredential(ctx, RemoteEngineConfig{
		Provider:   provider,
		Model:      "validation",
		APIKey:     apiKey,
		HTTPClient: client,
	}); err != nil {
		return nil, err
	}
	return &Credential{
		Type: CredentialAPIKey,
		Key:  apiKey,
	}, nil
}

func extractOAuthErrorDetail(body map[string]any) string {
	for _, k := range []string{"error_description", "message", "detail", "error"} {
		if s, ok := body[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if errObj, ok := body["error"].(map[string]any); ok {
		if s, ok := errObj["message"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func extractOAuthErrorCode(body map[string]any) string {
	if s, ok := body["error"].(string); ok {
		return s
	}
	if errObj, ok := body["error"].(map[string]any); ok {
		if s, ok := errObj["code"].(string); ok {
			return s
		}
	}
	return ""
}

func mergeUniqueStrings(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	for _, s := range b {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
