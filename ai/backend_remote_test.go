package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitHubCopilotRetriesWithResponsesAPIWhenModelRejectsCompletions(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/chat/completions":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"model is not accessible via chat completions","code":"unsupported_api_for_model"}}`)
		case "/responses":
			if r.Header.Get("Authorization") != "Bearer copilot-token" {
				t.Errorf("Authorization = %q, want Copilot bearer token", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Copilot-Integration-Id") != "vscode-chat" {
				t.Errorf("Copilot-Integration-Id = %q, want vscode-chat", r.Header.Get("Copilot-Integration-Id"))
			}
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Responses request: %v", err)
			}
			if request["model"] != "gpt-6-luna" || request["input"] == nil || request["messages"] != nil {
				t.Errorf("Responses request = %#v, want model/input and no messages", request)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `event: response.output_text.delta`)
			_, _ = fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"hello"}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `event: response.completed`)
			_, _ = fmt.Fprintln(w, `data: {"type":"response.completed"}`)
			_, _ = fmt.Fprintln(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	backend := &remoteConversationBackend{
		engine: &remoteEngineBackend{cfg: RemoteEngineConfig{
			Provider:   RemoteCopilot,
			Model:      "gpt-6-luna",
			Credential: &Credential{Type: CredentialOAuth, Access: "copilot-token"},
			BaseURL:    server.URL,
		}},
		config: ConversationConfig{SystemMessage: "system"},
	}
	var text strings.Builder
	err := backend.SendStream(context.Background(), []PromptPart{{Role: RoleUser, Text: "hello"}}, func(event ModelEvent) {
		if delta, ok := event.(TextEvent); ok {
			text.WriteString(delta.Text)
		}
	})
	if err != nil {
		t.Fatalf("SendStream() error = %v", err)
	}
	if !strings.EqualFold(text.String(), "hello") {
		t.Fatalf("streamed text = %q, want hello", text.String())
	}
	if !reflect.DeepEqual(paths, []string{"/chat/completions", "/responses"}) {
		t.Fatalf("request paths = %v, want chat/completions then responses", paths)
	}
}

func TestCustomRemoteEndpointBuildsProtocolSpecificRequests(t *testing.T) {
	tests := []struct {
		name     string
		protocol RemoteAPIProtocol
		baseURL  string
		apiKey   string
		wantURL  string
		wire     remoteWireKind
		wantAuth string
	}{
		{
			name:     "OpenAI Chat Completions",
			protocol: RemoteProtocolOpenAIChatCompletions,
			baseURL:  "https://example.test/v1",
			wantURL:  "https://example.test/v1/chat/completions",
			wire:     wireOpenAICompletions,
		},
		{
			name:     "OpenAI Responses",
			protocol: RemoteProtocolOpenAIResponses,
			baseURL:  "https://example.test/v1/responses",
			apiKey:   "secret",
			wantURL:  "https://example.test/v1/responses",
			wire:     wireOpenAIResponses,
			wantAuth: "Bearer secret",
		},
		{
			name:     "Anthropic Messages",
			protocol: RemoteProtocolAnthropicMessages,
			baseURL:  "https://example.test/v1/messages",
			apiKey:   "secret",
			wantURL:  "https://example.test/v1/messages",
			wire:     wireAnthropicMessages,
		},
		{
			name:     "Gemini generateContent",
			protocol: RemoteProtocolGeminiGenerateContent,
			baseURL:  "https://example.test/v1beta",
			apiKey:   "secret",
			wantURL:  "https://example.test/v1beta/models/test-model:streamGenerateContent?alt=sse",
			wire:     wireGeminiContent,
		},
		{
			name:     "GitHub Copilot",
			protocol: RemoteProtocolCopilot,
			baseURL:  "https://example.test/api",
			apiKey:   "secret",
			wantURL:  "https://example.test/api/chat/completions",
			wire:     wireOpenAICompletions,
			wantAuth: "Bearer secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := RemoteEngineConfig{
				Provider: RemoteCustom,
				Protocol: test.protocol,
				Model:    "test-model",
				APIKey:   test.apiKey,
				BaseURL:  test.baseURL,
			}
			engine, err := NewRemoteEngine(remote)
			if err != nil {
				t.Fatalf("NewRemoteEngine() error = %v", err)
			}
			engine.Close()
			request, wire, err := buildRemoteRequestWithWire(remote, ConversationConfig{}, []PromptPart{{Role: RoleUser, Text: "hello"}})
			if err != nil {
				t.Fatalf("buildRemoteRequestWithWire() error = %v", err)
			}
			if request.url != test.wantURL {
				t.Errorf("request URL = %q, want %q", request.url, test.wantURL)
			}
			if wire != test.wire {
				t.Errorf("wire protocol = %q, want %q", wire, test.wire)
			}
			if got := request.headers["Authorization"]; got != test.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, test.wantAuth)
			}
			if test.protocol == RemoteProtocolAnthropicMessages {
				if got := request.headers["x-api-key"]; got != test.apiKey {
					t.Errorf("x-api-key = %q, want configured key", got)
				}
			}
			if test.protocol == RemoteProtocolGeminiGenerateContent {
				if got := request.headers["x-goog-api-key"]; got != test.apiKey {
					t.Errorf("x-goog-api-key = %q, want configured key", got)
				}
			}
		})
	}
}

func TestCustomRemoteCatalogUsesSelectedProtocolAndOptionalAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("catalog request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected Authorization header on unauthenticated catalog request: %q", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"model-a","context_length":4096},{"id":"model-b"}]}`)
	}))
	defer server.Close()

	models, err := ListRemoteModelDetails(context.Background(), RemoteEngineConfig{
		Provider: RemoteCustom,
		Protocol: RemoteProtocolOpenAIResponses,
		BaseURL:  server.URL + "/v1/responses",
	})
	if err != nil {
		t.Fatalf("ListRemoteModelDetails() error = %v", err)
	}
	if len(models) != 2 || models[0].ID != "model-a" || models[0].MaxInputTokens != 4096 {
		t.Fatalf("catalog models = %+v, want model-a with its context limit and model-b", models)
	}
}

func TestCustomRemoteEndpointCanStreamWithoutAnAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("generation request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected Authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`)
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "data: [DONE]")
		_, _ = fmt.Fprintln(w)
	}))
	defer server.Close()

	engine, err := NewRemoteEngine(RemoteEngineConfig{
		Provider: RemoteCustom,
		Protocol: RemoteProtocolOpenAIChatCompletions,
		Model:    "test-model",
		BaseURL:  server.URL + "/v1",
	})
	if err != nil {
		t.Fatalf("NewRemoteEngine() error = %v", err)
	}
	defer engine.Close()
	conversation, err := engine.NewConversationWithConfig(ConversationConfig{})
	if err != nil {
		t.Fatalf("NewConversationWithConfig() error = %v", err)
	}
	defer conversation.Close()
	var text strings.Builder
	for event, streamErr := range conversation.SendStream(context.Background(), []PromptPart{{Role: RoleUser, Text: "hello"}}) {
		if streamErr != nil {
			t.Fatalf("SendStream() error = %v", streamErr)
		}
		if delta, ok := event.(TextEvent); ok {
			text.WriteString(delta.Text)
		}
	}
	if text.String() != "ok" {
		t.Fatalf("streamed text = %q, want %q", text.String(), "ok")
	}
}

func TestCustomCopilotEndpointRetriesWithResponsesProtocol(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/chat/completions":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"unsupported API for model","code":"unsupported_api_for_model"}}`)
		case "/api/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `event: response.output_text.delta`)
			_, _ = fmt.Fprintln(w, `data: {"type":"response.output_text.delta","delta":"copilot"}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `event: response.completed`)
			_, _ = fmt.Fprintln(w, `data: {"type":"response.completed"}`)
			_, _ = fmt.Fprintln(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	engine, err := NewRemoteEngine(RemoteEngineConfig{
		Provider: RemoteCustom,
		Protocol: RemoteProtocolCopilot,
		Model:    "gpt-6-luna",
		BaseURL:  server.URL + "/api",
	})
	if err != nil {
		t.Fatalf("NewRemoteEngine() error = %v", err)
	}
	defer engine.Close()
	conversation, err := engine.NewConversationWithConfig(ConversationConfig{})
	if err != nil {
		t.Fatalf("NewConversationWithConfig() error = %v", err)
	}
	defer conversation.Close()
	var text strings.Builder
	for event, streamErr := range conversation.SendStream(context.Background(), []PromptPart{{Role: RoleUser, Text: "hello"}}) {
		if streamErr != nil {
			t.Fatalf("SendStream() error = %v", streamErr)
		}
		if delta, ok := event.(TextEvent); ok {
			text.WriteString(delta.Text)
		}
	}
	if text.String() != "copilot" {
		t.Fatalf("streamed text = %q, want %q", text.String(), "copilot")
	}
	if !reflect.DeepEqual(paths, []string{"/api/chat/completions", "/api/responses"}) {
		t.Fatalf("request paths = %v, want chat/completions then responses", paths)
	}
}

func TestRemoteOpenAIStreamPreservesToolCallIDsInHistory(t *testing.T) {
	var requests atomic.Int32
	var toolCallID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var request struct {
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(request.Tools) != 1 {
			t.Errorf("request tools = %d, want 1", len(request.Tools))
		}
		switch requests.Add(1) {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Checking"}}]}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]}}]}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "data: [DONE]")
			_, _ = fmt.Fprintln(w)
		case 2:
			for _, message := range request.Messages {
				if message["role"] == "tool" {
					toolCallID, _ = message["tool_call_id"].(string)
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Done"}}]}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "data: [DONE]")
			_, _ = fmt.Fprintln(w)
		default:
			t.Errorf("unexpected request number")
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	engine, err := NewRemoteEngine(RemoteEngineConfig{
		Provider:   RemoteOpenAI,
		Model:      "test-model",
		APIKey:     "test-token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := engine.NewConversationWithConfig(ConversationConfig{
		SystemMessage: "system",
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name": "read_file", "parameters": map[string]any{"type": "object"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conv.Close()

	var first []ModelEvent
	for event, err := range conv.SendStream(context.Background(), []PromptPart{{Role: RoleUser, Text: "read a.txt"}}) {
		if err != nil {
			t.Fatal(err)
		}
		first = append(first, event)
	}
	if len(first) != 2 || first[0] != (TextEvent{Text: "Checking"}) {
		t.Fatalf("first turn events = %#v", first)
	}
	callEvent, ok := first[1].(ToolCallEvent)
	if !ok || len(callEvent.ToolCalls) != 1 || callEvent.ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool event = %#v", first[1])
	}

	for event, err := range conv.SendStream(context.Background(), []PromptPart{{
		Role: RoleTool, ToolName: "read_file", ToolCallID: "call-1", ToolResponse: map[string]any{"text": "contents"},
	}}) {
		if err != nil {
			t.Fatal(err)
		}
		if event != (TextEvent{Text: "Done"}) {
			t.Fatalf("second turn event = %#v", event)
		}
	}
	if toolCallID != "call-1" {
		t.Fatalf("tool response ID in follow-up request = %q, want call-1", toolCallID)
	}
}

func TestRemoteProviderRequestTranslation(t *testing.T) {
	cfg := ConversationConfig{
		SystemMessage: "system",
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "lookup",
				"description": "find a file",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	}
	parts := []PromptPart{
		{Role: RoleUser, Text: "request"},
		{Role: RoleModel, ToolCalls: []ToolCall{{ID: "call-1", Name: "lookup", Arguments: map[string]any{"path": "a"}}}},
		{Role: RoleTool, ToolName: "lookup", ToolCallID: "call-1", ToolResponse: map[string]any{"found": true}},
	}
	tests := []struct {
		provider RemoteProvider
		wantURL  string
	}{
		{RemoteOpenAI, "api.openai.com"},
		{RemoteAnthropic, "api.anthropic.com"},
		{RemoteGemini, "generativelanguage.googleapis.com"},
		{RemoteOpenRouter, "openrouter.ai"},
	}
	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			request, err := buildRemoteRequest(RemoteEngineConfig{
				Provider: tt.provider, Model: "model", APIKey: "key",
			}, cfg, parts)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(request.url, tt.wantURL) {
				t.Fatalf("request URL = %q, want host %q", request.url, tt.wantURL)
			}
			var body map[string]any
			if err := json.Unmarshal(request.body, &body); err != nil {
				t.Fatal(err)
			}
			if body["stream"] != true && tt.provider != RemoteGemini {
				t.Fatalf("provider request is not streaming: %s", request.body)
			}
			switch tt.provider {
			case RemoteOpenAI, RemoteOpenRouter:
				messages := body["messages"].([]any)
				if messages[2].(map[string]any)["tool_calls"] == nil {
					t.Fatalf("OpenAI request omitted tool call: %s", request.body)
				}
				if messages[3].(map[string]any)["tool_call_id"] != "call-1" {
					t.Fatalf("OpenAI request omitted tool response ID: %s", request.body)
				}
			case RemoteAnthropic:
				tools := body["tools"].([]any)
				if tools[0].(map[string]any)["input_schema"] == nil {
					t.Fatalf("Anthropic request omitted input schema: %s", request.body)
				}
			case RemoteGemini:
				tools := body["tools"].([]any)
				if tools[0].(map[string]any)["functionDeclarations"] == nil {
					t.Fatalf("Gemini request omitted function declarations: %s", request.body)
				}
			}
		})
	}
}

func TestRemoteEngineRejectsInsecureNonLoopbackBaseURL(t *testing.T) {
	if _, err := NewRemoteEngine(RemoteEngineConfig{
		Provider: RemoteOpenAI, Model: "model", APIKey: "secret", BaseURL: "http://example.com",
	}); err == nil {
		t.Fatal("remote engine accepted a non-loopback HTTP URL")
	}
}

func TestAnthropicAndGeminiStreamEvents(t *testing.T) {
	var anthEvents []ModelEvent
	anth := newRemoteAccumulator(RemoteAnthropic, func(event ModelEvent) {
		anthEvents = append(anthEvents, event)
	})
	if err := anth.consume("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`); err != nil {
		t.Fatal(err)
	}
	if err := anth.consume("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"x\"}"}}`); err != nil {
		t.Fatal(err)
	}
	anthCalls := anth.toolCalls()
	if err := anth.finish(anthCalls); err != nil {
		t.Fatal(err)
	}
	if len(anthEvents) != 1 {
		t.Fatalf("Anthropic events = %#v", anthEvents)
	}
	if got := anthEvents[0].(ToolCallEvent).ToolCalls[0]; got.ID != "toolu_1" || got.Arguments["path"] != "x" {
		t.Fatalf("Anthropic tool call = %#v", got)
	}

	var gemEvents []ModelEvent
	gem := newRemoteAccumulator(RemoteGemini, func(event ModelEvent) {
		gemEvents = append(gemEvents, event)
	})
	if err := gem.consume("", `{"candidates":[{"content":{"parts":[{"text":"hello"},{"functionCall":{"name":"lookup","args":{"path":"y"}}}]}}]}`); err != nil {
		t.Fatal(err)
	}
	gemCalls := gem.toolCalls()
	if err := gem.finish(gemCalls); err != nil {
		t.Fatal(err)
	}
	if len(gemEvents) != 2 || gemCalls[0].ID == "" {
		t.Fatalf("Gemini output = events %#v, calls %#v", gemEvents, gemCalls)
	}
}

func TestRemoteMalformedToolArgumentsArePreservedForDispatch(t *testing.T) {
	var events []ModelEvent
	acc := newRemoteAccumulator(RemoteAnthropic, func(event ModelEvent) {
		events = append(events, event)
	})
	if err := acc.consume("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_bad","name":"lookup","input":{}}}`); err != nil {
		t.Fatal(err)
	}
	if err := acc.consume("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{not-json}"}}`); err != nil {
		t.Fatal(err)
	}

	calls := acc.toolCalls()
	if err := acc.finish(calls); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one tool-call event", events)
	}
	call := events[0].(ToolCallEvent).ToolCalls[0]
	if call.ID != "toolu_bad" || call.Name != "lookup" {
		t.Fatalf("tool call identity = (%q, %q), want (toolu_bad, lookup)", call.ID, call.Name)
	}
	if call.Arguments != nil || string(call.RawArguments) != "{not-json}" {
		t.Fatalf("malformed call = %#v, want raw args retained and no decoded map", call)
	}
	requestArguments, err := remoteToolArguments(call)
	if err != nil {
		t.Fatal(err)
	}
	if string(requestArguments) != "{}" {
		t.Fatalf("serialized arguments = %s, want schema-valid empty object for the error exchange", requestArguments)
	}
}
