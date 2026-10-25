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
	anthCalls, err := anth.toolCalls()
	if err != nil {
		t.Fatal(err)
	}
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
	gemCalls, err := gem.toolCalls()
	if err != nil {
		t.Fatal(err)
	}
	if err := gem.finish(gemCalls); err != nil {
		t.Fatal(err)
	}
	if len(gemEvents) != 2 || gemCalls[0].ID == "" {
		t.Fatalf("Gemini output = events %#v, calls %#v", gemEvents, gemCalls)
	}
}
