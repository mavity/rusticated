package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai/assets"
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

type searchSelectTerm struct {
	keys  [][]byte
	index int
	out   strings.Builder
	cols  int
	rows  int
}

type browserURLPromptTerm struct {
	*searchSelectTerm
	openedURL string
	onOpen    func()
}

func (t *browserURLPromptTerm) OpenURL(target string) error {
	t.openedURL = target
	if t.onOpen != nil {
		t.onOpen()
	}
	return nil
}

func (s *searchSelectTerm) Read(p []byte) (int, error) {
	if s.index >= len(s.keys) {
		return 0, io.EOF
	}
	key := s.keys[s.index]
	s.index++
	return copy(p, key), nil
}

func (s *searchSelectTerm) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s *searchSelectTerm) Size() (int, int)            { return s.cols, s.rows }
func (s *searchSelectTerm) IsTTY() bool                 { return true }
func (s *searchSelectTerm) SubscribeRawInput() (func(context.Context, []byte) (int, error), func()) {
	return func(_ context.Context, p []byte) (int, error) { return s.Read(p) }, func() {}
}

func TestPromptLineOpensBrowserOnBlankEnterAndKeepsPastePromptActive(t *testing.T) {
	const target = "https://provider.example/oauth?state=xyz"
	const prompt = "URL above - press Enter to open browser, or paste authorization code/address"
	base := &searchSelectTerm{
		keys: [][]byte{[]byte("  "), []byte("\r\n"), []byte("authorization-code\n")},
		cols: 80,
		rows: 24,
	}
	term := &browserURLPromptTerm{searchSelectTerm: base}
	term.onOpen = func() {
		if !strings.HasSuffix(strings.TrimRight(base.out.String(), " "), prompt+":") {
			t.Errorf("browser opened after prompt line changed: %q", base.out.String())
		}
	}

	got, err := PromptLine(context.Background(), openURLOnEmptyEnter(term, target), "", prompt, false)
	if err != nil {
		t.Fatalf("PromptLine() error = %v", err)
	}
	if got != "authorization-code" {
		t.Fatalf("PromptLine() = %q, want authorization-code", got)
	}
	if term.openedURL != target {
		t.Fatalf("opened URL = %q, want %q", term.openedURL, target)
	}
	if strings.Count(base.out.String(), prompt) != 1 {
		t.Fatalf("prompt was displayed more than once: %q", base.out.String())
	}
}

func TestRemoteModelDisplayNameStripsDuplicatedNamespace(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{model: "XYZ/XYZ-model", want: "XYZ-model"},
		{model: "XYZ/xyz-model", want: "xyz-model"},
		{model: "meta-llama/llama-3.1", want: "meta-llama/llama-3.1"},
		{model: "vendor/model", want: "vendor/model"},
	}
	for _, test := range tests {
		if got := RemoteModelDisplayName(test.model); got != test.want {
			t.Errorf("RemoteModelDisplayName(%q) = %q, want %q", test.model, got, test.want)
		}
	}
}

func TestTerminalSelectorRecognizesProviderAndModelIcons(t *testing.T) {
	tests := []struct {
		label string
		want  terminalSelectorIcon
	}{
		{label: "Google Gemini", want: terminalSelectorIcon{glyph: "✦", color: 0x4E87F8, width: 1}},
		{label: "Qwen3-72B", want: terminalSelectorIcon{glyph: "🟌", color: 0x615CED, width: 2}},
		{label: "z.ai/glm-4", want: terminalSelectorIcon{glyph: "⁙", color: 0x3859FF, width: 1}},
		{label: "anthropic/claude-sonnet", want: terminalSelectorIcon{glyph: "✳", color: 0xD97757, width: 1}},
		{label: "openai/gpt-6-luna", want: terminalSelectorIcon{glyph: "꩜", color: 0x10A37F, width: 1}},
		{label: "meta-llama/llama-3", want: terminalSelectorIcon{glyph: "∞", color: 0x0081FB, width: 1}},
		{label: "mistral/large", want: terminalSelectorIcon{glyph: "𝕄", color: 0xFF7000, width: 1}},
		{label: "deepseek-r1", want: terminalSelectorIcon{glyph: "🐋", width: 2}},
		{label: "kimi-k2", want: terminalSelectorIcon{glyph: "𝗞", color: 0x18181B, width: 1}},
		{label: "OpenRouter", want: terminalSelectorIcon{glyph: "⇄", color: 0x6566F1, width: 1}},
		{label: "GitHub Copilot", want: terminalSelectorIcon{glyph: "🐙", width: 2}},
		{label: "openrouter/auto", want: terminalSelectorIcon{}},
	}
	for _, test := range tests {
		if got := terminalSelectorIconFor(test.label); got != test.want {
			t.Errorf("terminalSelectorIconFor(%q) = %+v, want %+v", test.label, got, test.want)
		}
	}
}

func TestPromptSearchSelectPlacesModelIconAfterKeepLoginMarkers(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{[]byte("\n")},
		cols: 80,
		rows: 24,
	}
	options := []string{"[Keep] openai/gpt-6-luna", "[Login] anthropic/claude-sonnet"}
	if _, err := PromptSearchSelect(context.Background(), term, "Models", "", options, 0); err != nil {
		t.Fatalf("PromptSearchSelect() error = %v", err)
	}
	output := term.out.String()
	for _, expected := range []string{
		" [Keep] \x1b[38;2;16;163;127m꩜\x1b[38;2;0;0;128m openai/gpt-6-luna",
		"[Login] \x1b[38;2;217;119;87m✳\x1b[39m anthropic/claude-sonnet",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("selector output omitted %q: %q", expected, output)
		}
	}
}

func TestPromptSelectUsesFilePanelSelectionColors(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{[]byte("\n")},
		cols: 80,
		rows: 24,
	}
	if _, err := PromptSelect(context.Background(), term, "Choose", "", []string{"XYZ/XYZ-model", "Second"}, 0); err != nil {
		t.Fatalf("PromptSelect() error = %v", err)
	}
	if !strings.Contains(term.out.String(), "\x1b[38;2;0;0;128;48;2;255;215;0m [1] XYZ-model ") {
		t.Fatalf("selector did not render the selected option in file-panel colors: %q", term.out.String())
	}
	if strings.Contains(term.out.String(), "XYZ/XYZ-model") {
		t.Fatalf("selector output retained the duplicated namespace: %q", term.out.String())
	}
}

func TestPromptSearchSelectFiltersAndAccepts(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{[]byte("c"), []byte("l"), []byte("a"), []byte("u"), []byte("d"), []byte("e"), []byte("\n")},
		cols: 80,
		rows: 24,
	}
	choice, err := PromptSearchSelect(context.Background(), term, "Models", "Choose a model.", []string{
		"gpt-6-luna",
		"claude-sonnet",
		"gemini-pro",
	}, 0)
	if err != nil {
		t.Fatalf("PromptSearchSelect() error = %v", err)
	}
	if choice != 1 {
		t.Fatalf("PromptSearchSelect() = %d, want filtered Claude model at index 1", choice)
	}
	if !strings.Contains(term.out.String(), "Search: claude") {
		t.Fatalf("selector output omitted live search query: %q", term.out.String())
	}
	if !strings.Contains(term.out.String(), "\x1b[3A\x1b[15G") {
		t.Fatalf("selector did not position the cursor after the search query: %q", term.out.String())
	}
}

func TestPromptSearchSelectRendersProviderIcons(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{[]byte("\n")},
		cols: 80,
		rows: 24,
	}
	if _, err := PromptSearchSelect(context.Background(), term, "Providers", "", []string{"OpenRouter", "GitHub Copilot"}, 0); err != nil {
		t.Fatalf("PromptSearchSelect() error = %v", err)
	}
	output := term.out.String()
	if !strings.Contains(output, "\x1b[38;2;0;0;128;48;2;255;215;0m \x1b[38;2;101;102;241m⇄\x1b[38;2;0;0;128m OpenRouter") {
		t.Errorf("selector omitted the colored OpenRouter provider icon: %q", output)
	}
	if !strings.Contains(output, "🐙 GitHub Copilot") {
		t.Errorf("selector omitted the Copilot provider icon: %q", output)
	}
}

func TestPromptSearchSelectBackspaceAndGridNavigation(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{
			[]byte("x"),
			[]byte("\x7f"),
			[]byte("\x1b[B"),
			[]byte("\n"),
		},
		cols: 48,
		rows: 12,
	}
	options := []string{"model-alpha", "model-bravo", "model-charlie", "model-delta"}
	choice, err := PromptSearchSelect(context.Background(), term, "Models", "", options, 0)
	if err != nil {
		t.Fatalf("PromptSearchSelect() error = %v", err)
	}
	if choice != 3 {
		t.Fatalf("PromptSearchSelect() = %d, want down-arrow row selection at index 3", choice)
	}
	if !strings.Contains(term.out.String(), "Search: x") || !strings.Contains(term.out.String(), "Search: ") {
		t.Fatalf("selector did not render query edits: %q", term.out.String())
	}
}

func TestPromptSearchSelectUsesCompactModelLabels(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{[]byte("\n")},
		cols: 40,
		rows: 12,
	}
	_, err := PromptSearchSelect(context.Background(), term, "Models", "", []string{
		"XYZ/XYZ-model",
		"vendor/model",
	}, 0)
	if err != nil {
		t.Fatalf("PromptSearchSelect() error = %v", err)
	}
	output := term.out.String()
	if !strings.Contains(output, " XYZ-model") {
		t.Fatalf("selector did not remove the duplicated namespace: %q", output)
	}
	if strings.Contains(output, "XYZ/XYZ-model") || strings.Contains(output, "▸") {
		t.Fatalf("selector output retained a duplicated namespace or selection marker: %q", output)
	}
}

func TestPromptSearchSelectKeepsManualActionAfterFilteringAllModels(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{[]byte("missing-model"), []byte("\n")},
		cols: 80,
		rows: 24,
	}
	options := []string{"model-alpha", "\x00Enter a model ID manually"}
	choice, err := PromptSearchSelect(context.Background(), term, "Models", "", options, 0)
	if err != nil {
		t.Fatalf("PromptSearchSelect() error = %v", err)
	}
	if choice != 1 {
		t.Fatalf("PromptSearchSelect() = %d, want the always-visible manual model action at index 1", choice)
	}
	if !strings.Contains(term.out.String(), "Enter a model ID manually") {
		t.Fatal("manual model action was not rendered")
	}
}

func TestPromptSearchSelectCompactOmitsDecorativeCaptions(t *testing.T) {
	term := &searchSelectTerm{
		keys: [][]byte{[]byte("c"), []byte("\n")},
		cols: 80,
		rows: 24,
	}
	choice, err := PromptSearchSelectCompact(context.Background(), term, "LLM model, select or search", []string{"claude-sonnet"}, 0)
	if err != nil {
		t.Fatalf("PromptSearchSelectCompact() error = %v", err)
	}
	if choice != 0 {
		t.Fatalf("PromptSearchSelectCompact() = %d, want model index 0", choice)
	}
	output := term.out.String()
	for _, caption := range []string{
		"LLM model, select or search",
		"> c",
		"claude-sonnet",
	} {
		if !strings.Contains(output, caption) {
			t.Errorf("compact selector output omitted %q: %q", caption, output)
		}
	}
	for _, caption := range []string{"Search:", "Type to search", "No matches.", "✓ claude-sonnet"} {
		if strings.Contains(output, caption) {
			t.Errorf("compact selector output contains unwanted caption %q: %q", caption, output)
		}
	}
	if !strings.Contains(output, "\x1b[38;2;0;0;128;48;2;255;215;0m \x1b[38;2;217;119;87m✳\x1b[38;2;0;0;128m claude-sonnet") {
		t.Errorf("compact selector did not render the selected model with its brand icon and file-panel selection background: %q", output)
	}
	if !strings.Contains(output, "\x1b[2A\x1b[4G") {
		t.Errorf("compact selector did not position the cursor after the search query: %q", output)
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
	if !strings.Contains(out.String(), "127.0.0.1:0") {
		// Verify the emitted callback URL never uses port 0 (which browsers block with ERR_UNSAFE_PORT).
	} else {
		t.Fatalf("OpenRouter emitted unsafe port 0 callback URL in output: %q", out.String())
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
	calls := acc.toolCalls()
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

func TestDefaultCacheDirAndUserHomeDirAreNativePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	cacheDir, err := assets.DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir() error = %v", err)
	}
	if assets.HostOS() == "windows" {
		if strings.HasPrefix(home, `/`) || strings.HasPrefix(home, `\`) {
			t.Fatalf("UserHomeDir() on Windows = %q, must not start with slash or backslash", home)
		}
		if strings.HasPrefix(cacheDir, `/`) || strings.HasPrefix(cacheDir, `\`) {
			t.Fatalf("DefaultCacheDir() on Windows = %q, must not start with slash or backslash", cacheDir)
		}
	}
}

func TestLocalGemmaLoadModelInfoAndBackendDiscoveryWhenCached(t *testing.T) {
	cacheDir, err := assets.DefaultCacheDir()
	if err != nil {
		t.Skipf("DefaultCacheDir unavailable: %v", err)
	}
	ctx := context.Background()
	if sz, err := assets.RuntimeDownloadSize(ctx, cacheDir); err != nil || sz != 0 {
		t.Skip("LiteRT-LM runtime is not cached locally; skipping live DLL model inspection test")
	}
	libPath, err := assets.EnsureRuntime(ctx, assets.FetchOptions{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("EnsureRuntime() error = %v", err)
	}
	var cachedTierID string
	for _, tier := range assets.ModelTiers() {
		if sz, err := assets.ModelTierDownloadSize(tier.ID, cacheDir); err == nil && sz == 0 {
			cachedTierID = tier.ID
			break
		}
	}
	if cachedTierID == "" {
		t.Skip("no Gemma 4 model tier is cached locally; skipping live LoadModelInfo test")
	}
	modelPath, err := assets.EnsureModelTier(ctx, cachedTierID, assets.FetchOptions{CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("EnsureModelTier(%q) error = %v", cachedTierID, err)
	}
	info, err := LoadModelInfo(libPath, modelPath)
	if err != nil {
		if strings.Contains(err.Error(), "not a valid Win32 application") {
			t.Skipf("cached litert_lm_ext.dll is for a different host architecture (e.g. amd64 vs arm64): %v", err)
		}
		t.Fatalf("LoadModelInfo(%q, %q) = (%+v, %v), want non-nil ModelInfo", libPath, modelPath, info, err)
	}
	if info == nil {
		t.Fatalf("LoadModelInfo(%q, %q) returned nil ModelInfo", libPath, modelPath)
	}
	if len(info.SupportedBackends) == 0 {
		t.Fatalf("LoadModelInfo(%q) returned empty SupportedBackends: %+v", modelPath, info)
	}
	t.Logf("Verified cached tier %s (%s): MaxContextTokens=%d SupportedBackends=%v",
		cachedTierID, modelPath, info.MaxContextTokens, info.SupportedBackends)

	// Also verify that even if a legacy path with a leading '\' or '/' before the drive letter
	// is passed to LoadModelInfo, normalizeHostNativePath strips it so LiteRT-LM still loads it.
	if assets.HostOS() == "windows" && len(modelPath) >= 3 && modelPath[1] == ':' {
		for _, mangled := range []string{
			`\` + modelPath,
			`/` + strings.ReplaceAll(modelPath, `\`, `/`),
		} {
			mInfo, mErr := LoadModelInfo(libPath, mangled)
			if mErr != nil || mInfo == nil || len(mInfo.SupportedBackends) == 0 {
				t.Fatalf("LoadModelInfo with mangled path %q failed: info=%+v err=%v", mangled, mInfo, mErr)
			}
		}
	}

	// Verify backend selection chooses GPU/NPU over CPU when supported by the model,
	// and that NewEngine succeeds with the selected backend.
	chosenBackend := "auto"
	for _, want := range []string{"npu", "gpu", "cpu"} {
		for _, b := range info.SupportedBackends {
			if strings.EqualFold(b, want) {
				chosenBackend = want
				break
			}
		}
		if chosenBackend != "auto" {
			break
		}
	}
	if chosenBackend == "cpu" && len(info.SupportedBackends) > 1 {
		t.Fatalf("chosenBackend selected cpu despite accelerator backends in %v", info.SupportedBackends)
	}
	eng, err := NewEngine(libPath, modelPath, chosenBackend, 2048)
	if err != nil || eng == nil {
		t.Fatalf("NewEngine(%q, %q, %q) failed: %v", libPath, modelPath, chosenBackend, err)
	}
	defer eng.Close()
	if got := eng.Backend(); got == "cpu" && chosenBackend != "cpu" {
		t.Fatalf("eng.Backend() = %q, expected accelerator backend %q", got, chosenBackend)
	}
	t.Logf("Successfully created local Gemma engine on backend=%q", eng.Backend())
}

func TestLoopbackListenerPortZeroAndCloseWhileAccepting(t *testing.T) {
	// 1. Verify net.Listen("tcp", "127.0.0.1:0") assigns a non-zero port on both native and Rusticated WASM.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen(127.0.0.1:0) error = %v", err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcpAddr.Port <= 0 {
		_ = ln.Close()
		t.Fatalf("ln.Addr() = %v (%T), want non-zero TCP port", ln.Addr(), ln.Addr())
	}

	// 2. Verify ln.Close() while Accept() is blocked in another goroutine returns promptly without deadlock.
	acceptErrCh := make(chan error, 1)
	go func() {
		conn, accErr := ln.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		acceptErrCh <- accErr
	}()
	time.Sleep(25 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- ln.Close()
	}()
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("ln.Close() error = %v", closeErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ln.Close() deadlocked while ln.Accept() was pending")
	}

	select {
	case accErr := <-acceptErrCh:
		if accErr == nil {
			t.Fatal("expected ln.Accept() to return an error after ln.Close()")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ln.Accept() did not unblock after ln.Close()")
	}
}

func TestOpenRouterOAuthLoopbackHTTPBrowserRedirect(t *testing.T) {
	origTokenURL := openRouterTokenURL
	defer func() { openRouterTokenURL = origTokenURL }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = decodeJSONRequest(r, &body)
		if body["code"] != "browser-redirect-code" || body["code_challenge_method"] != "S256" {
			http.Error(w, "bad exchange payload", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"key":"sk-or-from-loopback"}`)
	}))
	defer server.Close()
	openRouterTokenURL = server.URL

	// Use a blocking reader for manual input so the loopback HTTP callback wins the race,
	// and verify that WaitForCallbackOrManualInput returns promptly without deadlocking on Close().
	pr, pw := net.Pipe()
	defer pr.Close()
	defer pw.Close()

	var out strings.Builder
	callbackPageCh := make(chan error, 1)
	term := &callbackTriggerTerm{
		Reader: pr,
		Writer: &out,
		onWrite: func(text string) {
			if idx := strings.Index(text, "Listening for callback on http://"); idx >= 0 {
				rest := text[idx+len("Listening for callback on "):]
				if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
					cbURL := strings.TrimSpace(rest[:nl])
					go func() {
						resp, err := http.Get(cbURL + "?code=browser-redirect-code")
						if err != nil {
							callbackPageCh <- fmt.Errorf("GET OAuth callback: %w", err)
							return
						}
						defer resp.Body.Close()
						body, err := io.ReadAll(resp.Body)
						if err != nil {
							callbackPageCh <- fmt.Errorf("read OAuth callback page: %w", err)
							return
						}
						if resp.StatusCode != http.StatusOK {
							callbackPageCh <- fmt.Errorf("OAuth callback status = %d, want %d", resp.StatusCode, http.StatusOK)
							return
						}
						if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/html") {
							callbackPageCh <- fmt.Errorf("OAuth callback Content-Type = %q, want HTML", got)
							return
						}
						page := string(body)
						for _, text := range []string{"Login completed successfully", "You can close this page and return to Kabibi.", "aria-live=\"polite\""} {
							if !strings.Contains(page, text) {
								callbackPageCh <- fmt.Errorf("OAuth callback page does not contain %q", text)
								return
							}
						}
						callbackPageCh <- nil
					}()
				}
			}
		},
	}

	done := make(chan *Credential, 1)
	errCh := make(chan error, 1)
	go func() {
		cred, err := loginOpenRouterOAuth(context.Background(), term, server.Client())
		if err != nil {
			errCh <- err
			return
		}
		done <- cred
	}()

	select {
	case err := <-errCh:
		t.Fatalf("loginOpenRouterOAuth() error = %v", err)
	case cred := <-done:
		if cred == nil || cred.Access != "sk-or-from-loopback" {
			t.Fatalf("cred = %+v, want sk-or-from-loopback", cred)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loginOpenRouterOAuth() timed out or deadlocked")
	}
	select {
	case err := <-callbackPageCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth callback page response timed out")
	}
}

type callbackTriggerTerm struct {
	Reader  net.Conn
	Writer  *strings.Builder
	onWrite func(string)
	fired   bool
}

func (c *callbackTriggerTerm) Read(p []byte) (int, error) {
	return c.Reader.Read(p)
}

func (c *callbackTriggerTerm) Write(p []byte) (int, error) {
	n, err := c.Writer.Write(p)
	if !c.fired && c.onWrite != nil && strings.Contains(c.Writer.String(), "Listening for callback on http://") {
		c.fired = true
		c.onWrite(c.Writer.String())
	}
	return n, err
}

func (c *callbackTriggerTerm) Size() (int, int) { return 78, 20 }
func (c *callbackTriggerTerm) IsTTY() bool      { return false }
func (c *callbackTriggerTerm) SubscribeRawInput() (func(context.Context, []byte) (int, error), func()) {
	return func(ctx context.Context, p []byte) (int, error) {
		return c.Reader.Read(p)
	}, func() { _ = c.Reader.Close() }
}
