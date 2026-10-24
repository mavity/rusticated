//go:build !wasip1

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRemoteCredentialSendsProviderAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("request = %s %s, want GET /models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	err := ValidateRemoteCredential(context.Background(), RemoteEngineConfig{
		Provider: RemoteOpenAI,
		Model:    "test-model",
		APIKey:   "test-token",
		BaseURL:  server.URL + "/models",
	})
	if err != nil {
		t.Fatalf("ValidateRemoteCredential() error = %v", err)
	}
}

func TestValidateRemoteCredentialRejectsUnauthorizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, "invalid credential")
	}))
	defer server.Close()

	err := ValidateRemoteCredential(context.Background(), RemoteEngineConfig{
		Provider: RemoteOpenAI,
		Model:    "test-model",
		APIKey:   "test-token",
		BaseURL:  server.URL + "/models",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("ValidateRemoteCredential() error = %v, want HTTP 401", err)
	}
}

func TestValidateRemoteCredentialRejectsInsecureRemoteEndpoint(t *testing.T) {
	err := ValidateRemoteCredential(context.Background(), RemoteEngineConfig{
		Provider: RemoteOpenAI,
		Model:    "test-model",
		APIKey:   "test-token",
		BaseURL:  "http://example.com/v1/models",
	})
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("ValidateRemoteCredential() error = %v, want insecure endpoint rejection", err)
	}
}

type memCredStore struct {
	cred        *Credential
	modifyCalls int
}

func (m *memCredStore) Read(context.Context, string) (*Credential, error) {
	if m.cred == nil {
		return nil, nil
	}
	cp := *m.cred
	return &cp, nil
}

func (m *memCredStore) Modify(_ context.Context, _ string, fn func(current *Credential) (*Credential, error)) (*Credential, error) {
	m.modifyCalls++
	var cur *Credential
	if m.cred != nil {
		cp := *m.cred
		cur = &cp
	}
	next, err := fn(cur)
	if err != nil {
		return nil, err
	}
	if next != nil {
		cp := *next
		m.cred = &cp
	}
	return m.cred, nil
}

func (m *memCredStore) Delete(context.Context, string) error {
	m.cred = nil
	return nil
}

func TestOpenRouterOAuthPKCEExchangeAndManualInput(t *testing.T) {
	origTokenURL := openRouterTokenURL
	defer func() { openRouterTokenURL = origTokenURL }()

	var gotCode, gotVerifier, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = decodeJSONRequest(r, &body)
		gotCode = body["code"]
		gotVerifier = body["code_verifier"]
		gotMethod = body["code_challenge_method"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"key":"sk-or-v1-minted"}`)
	}))
	defer server.Close()
	openRouterTokenURL = server.URL

	var out strings.Builder
	term := &StdioTerminalSession{
		Reader: strings.NewReader("1\nhttp://127.0.0.1:9999/oauth/callback/test?code=or-code-123\n"),
		Writer: &out,
		TTY:    false,
	}
	cred, err := loginOpenRouter(context.Background(), term, server.Client())
	if err != nil {
		t.Fatalf("loginOpenRouter() error = %v", err)
	}
	if cred.Type != CredentialOAuth || cred.Access != "sk-or-v1-minted" {
		t.Fatalf("cred = %+v, want sk-or-v1-minted", cred)
	}
	if gotCode != "or-code-123" || gotMethod != "S256" || len(gotVerifier) < 32 {
		t.Fatalf("exchange body = code:%q method:%q verifier:%q", gotCode, gotMethod, gotVerifier)
	}
}

func TestAnthropicOAuthCopyCodeRefreshAndWireHeaders(t *testing.T) {
	origTokenURL := anthropicTokenURL
	defer func() { anthropicTokenURL = origTokenURL }()

	var refreshHasScope bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = decodeJSONRequest(r, &body)
		w.Header().Set("Content-Type", "application/json")
		if body["grant_type"] == "refresh_token" {
			_, refreshHasScope = body["scope"]
			_, _ = fmt.Fprint(w, `{"access_token":"sk-ant-oat-refreshed","refresh_token":"ant-refresh-2","expires_in":3600}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"access_token":"sk-ant-oat-initial","refresh_token":"ant-refresh-1","expires_in":3600}`)
	}))
	defer server.Close()
	anthropicTokenURL = server.URL

	var out strings.Builder
	term := &StdioTerminalSession{
		Reader: strings.NewReader("2\ncopied-auth-code\n"),
		Writer: &out,
		TTY:    false,
	}
	cred, err := loginAnthropic(context.Background(), term, server.Client())
	if err != nil {
		t.Fatalf("loginAnthropic() error = %v", err)
	}
	if cred.Access != "sk-ant-oat-initial" || cred.Refresh != "ant-refresh-1" {
		t.Fatalf("cred = %+v", cred)
	}

	// Force expiry and test ResolveProviderCredential double-checked refresh
	cred.ExpiresAt = 1
	store := &memCredStore{cred: cred}
	resolved, err := ResolveProviderCredential(context.Background(), RemoteAnthropic, store, server.Client())
	if err != nil {
		t.Fatalf("ResolveProviderCredential() error = %v", err)
	}
	if resolved.Access != "sk-ant-oat-refreshed" || resolved.Refresh != "ant-refresh-2" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if refreshHasScope {
		t.Fatal("Anthropic refresh request must omit scope")
	}

	// Verify Anthropic OAuth request wire format (Bearer header, beta flags, Claude Code system prompt & tool casing)
	req, wire, err := buildRemoteRequestWithWire(RemoteEngineConfig{
		Provider:   RemoteAnthropic,
		Model:      "claude-sonnet-4-20250514",
		Credential: resolved,
	}, ConversationConfig{
		SystemMessage: "Kabibi system prompt",
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "bash",
				"description": "exec bash",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	}, []PromptPart{{Role: RoleUser, Text: "hi"}})
	if err != nil {
		t.Fatalf("buildRemoteRequestWithWire() error = %v", err)
	}
	if wire != wireAnthropicMessages {
		t.Fatalf("wire = %q, want anthropic-messages", wire)
	}
	if got := req.headers["Authorization"]; got != "Bearer sk-ant-oat-refreshed" {
		t.Fatalf("Authorization = %q, want Bearer sk-ant-oat-refreshed", got)
	}
	if _, hasXAPIKey := req.headers["x-api-key"]; hasXAPIKey {
		t.Fatal("Anthropic OAuth request must not send x-api-key")
	}
	if !strings.Contains(req.headers["anthropic-beta"], "oauth-2025-04-20") {
		t.Fatalf("anthropic-beta = %q, want oauth-2025-04-20", req.headers["anthropic-beta"])
	}
	bodyMap, err := decodeJSONMap(req.body)
	if err != nil {
		t.Fatal(err)
	}
	sysBlocks, ok := bodyMap["system"].([]any)
	if !ok || len(sysBlocks) != 2 {
		t.Fatalf("Anthropic OAuth system blocks = %#v, want 2 blocks", bodyMap["system"])
	}
	toolsArr := bodyMap["tools"].([]any)
	if toolName := toolsArr[0].(map[string]any)["name"]; toolName != "Bash" {
		t.Fatalf("Anthropic OAuth tool name = %v, want canonical 'Bash'", toolName)
	}
}

func TestOpenAIChatGPTOAuthAndResponsesWireProtocol(t *testing.T) {
	origTokenURL := openAIChatGPTTokenURL
	defer func() { openAIChatGPTTokenURL = origTokenURL }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			_, _ = fmt.Fprint(w, `{"access_token":"chatgpt-oauth-token-2","refresh_token":"chatgpt-refresh-2","expires_in":3600,"scope":"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"access_token":"chatgpt-oauth-token-1","refresh_token":"chatgpt-refresh-1","id_token":"id-tok","expires_in":3600,"scope":"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}`)
	}))
	defer server.Close()
	openAIChatGPTTokenURL = server.URL

	cred := &Credential{
		Type:      CredentialOAuth,
		Access:    "chatgpt-oauth-token-1",
		Refresh:   "chatgpt-refresh-1",
		ExpiresAt: 1,
		Metadata:  map[string]any{"client_id": "oaiapp_test", "flow": "chatgpt"},
	}
	store := &memCredStore{cred: cred}
	resolved, err := ResolveProviderCredential(context.Background(), RemoteOpenAI, store, server.Client())
	if err != nil {
		t.Fatalf("ResolveProviderCredential() error = %v", err)
	}
	if resolved.Access != "chatgpt-oauth-token-2" || resolved.Refresh != "chatgpt-refresh-2" {
		t.Fatalf("resolved = %+v", resolved)
	}

	req, wire, err := buildRemoteRequestWithWire(RemoteEngineConfig{
		Provider:   RemoteOpenAI,
		Model:      "gpt-4.1",
		Credential: resolved,
	}, ConversationConfig{
		SystemMessage: "sys",
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "read a file",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	}, []PromptPart{
		{Role: RoleUser, Text: "read it"},
		{Role: RoleModel, ToolCalls: []ToolCall{{ID: "call_9", Name: "read_file", Arguments: map[string]any{"path": "x"}}}},
		{Role: RoleTool, ToolCallID: "call_9", ToolName: "read_file", ToolResponse: map[string]any{"ok": true}},
	})
	if err != nil {
		t.Fatalf("buildRemoteRequestWithWire() error = %v", err)
	}
	if wire != wireOpenAIResponses || !strings.HasSuffix(req.url, "/v1/responses") {
		t.Fatalf("wire=%q url=%q, want openai-responses /v1/responses", wire, req.url)
	}
	var events []ModelEvent
	acc := newRemoteAccumulatorWithWire(RemoteOpenAI, wireOpenAIResponses, nil, func(ev ModelEvent) {
		events = append(events, ev)
	})
	if err := acc.consume("", `{"type":"response.output_text.delta","delta":"Hello "}`); err != nil {
		t.Fatal(err)
	}
	if err := acc.consume("", `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`); err != nil {
		t.Fatal(err)
	}
	if err := acc.consume("", `{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":\"main.go\"}"}`); err != nil {
		t.Fatal(err)
	}
	calls, err := acc.toolCalls()
	if err != nil {
		t.Fatal(err)
	}
	if err := acc.finish(calls); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "read_file" || calls[0].Arguments["path"] != "main.go" {
		t.Fatalf("Responses tool calls = %+v", calls)
	}
}

func TestGitHubCopilotDeviceFlowRefreshAndDynamicHeaders(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_, _ = fmt.Fprintf(w, `{"device_code":"dev-1","user_code":"ABCD-1234","verification_uri":"%s/verify","interval":1,"expires_in":60}`, serverURL)
		case "/login/oauth/access_token":
			_, _ = fmt.Fprint(w, `{"access_token":"ghu_test_refresh"}`)
		case "/copilot_internal/v2/token":
			_, _ = fmt.Fprintf(w, `{"token":"tid=1;exp=9999999999;proxy-ep=%s","expires_at":9999999999}`, serverURL)
		case "/models":
			_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-4.1","model_picker_enabled":true,"capabilities":{"supports":{"tool_calls":true}}},{"id":"claude-sonnet-4","model_picker_enabled":false,"policy":{"state":"unconfigured"},"capabilities":{"supports":{"tool_calls":true}}}]}`)
		case "/models/claude-sonnet-4/policy":
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	cred, err := exchangeGitHubCopilotSessionToken(context.Background(), "ghu_test_refresh", serverURL, server.Client())
	if err != nil {
		t.Fatalf("exchangeGitHubCopilotSessionToken() error = %v", err)
	}
	refreshed, err := refreshGitHubCopilotOAuth(context.Background(), cred, server.Client())
	if err != nil {
		t.Fatalf("refreshGitHubCopilotOAuth() error = %v", err)
	}
	avail := refreshed.MetaStrings("available_models")
	if len(avail) != 1 || avail[0] != "gpt-4.1" {
		t.Fatalf("available_models = %v, want [gpt-4.1]", avail)
	}

	req, wire, err := buildRemoteRequestWithWire(RemoteEngineConfig{
		Provider:   RemoteCopilot,
		Model:      "claude-sonnet-4",
		Credential: refreshed,
	}, ConversationConfig{SystemMessage: "sys"}, []PromptPart{
		{Role: RoleUser, Text: "hi"},
		{Role: RoleModel, ToolCalls: []ToolCall{{ID: "c1", Name: "test", Arguments: map[string]any{}}}},
		{Role: RoleTool, ToolCallID: "c1", ToolName: "test", ToolResponse: "ok"},
	})
	if err != nil {
		t.Fatalf("buildRemoteRequestWithWire(Copilot) error = %v", err)
	}
	if wire != wireAnthropicMessages {
		t.Fatalf("Copilot claude model wire = %q, want anthropic-messages", wire)
	}
	if got := req.headers["X-Initiator"]; got != "agent" {
		t.Fatalf("X-Initiator = %q, want agent after tool message", got)
	}
	if got := req.headers["Copilot-Integration-Id"]; got != "vscode-chat" {
		t.Fatalf("Copilot-Integration-Id = %q", got)
	}
}

func decodeJSONRequest(r *http.Request, dst any) error {
	data, err := readRemoteResponse(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

