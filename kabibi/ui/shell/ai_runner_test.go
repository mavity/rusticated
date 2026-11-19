package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai"
)

func TestKabibiMainToolsExposeOnlyHelper(t *testing.T) {
	tools := buildKabibiMainTools(nil, nil, 0, false, nil)
	if len(tools) != 1 {
		t.Fatalf("main agent tool count = %d, want 1", len(tools))
	}
	helper, ok := tools["helper"]
	if !ok {
		t.Fatal("main agent tools do not include helper")
	}
	required := make(map[string]bool, len(helper.Required))
	for _, field := range helper.Required {
		required[field] = true
	}
	if !required["task"] || !required["description"] {
		t.Fatalf("helper required fields = %#v, want task and description", helper.Required)
	}

	result, err := helper.ExecuteWithContext(context.Background(), &ai.ToolContext{}, ai.ToolCall{
		Name:      "helper",
		Arguments: map[string]any{"task": "inspect", "description": ""},
	})
	if err == nil || result != nil {
		t.Fatalf("helper with empty description returned (%#v, %v), want an error", result, err)
	}
	if got, want := err.Error(), `helper received task is "inspect", description is empty, and explanation is missing.`; got != want {
		t.Fatalf("helper validation error = %q, want %q", got, want)
	}
}

func TestKabibiHelperValidationReportsEveryField(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "all missing",
			args: map[string]any{},
			want: "helper received task is missing, description is missing, and explanation is missing.",
		},
		{
			name: "blank and missing",
			args: map[string]any{
				"task":        "inspect",
				"description": "",
			},
			want: `helper received task is "inspect", description is empty, and explanation is missing.`,
		},
		{
			name: "all present",
			args: map[string]any{
				"task":        "inspect",
				"description": "look closely",
				"explanation": "find evidence",
			},
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			helper := newKabibiHelperTool(nil, nil, 0, false, nil)
			_, err := helper.ExecuteWithContext(context.Background(), &ai.ToolContext{}, ai.ToolCall{
				Name:      "helper",
				Arguments: test.args,
			})
			if test.want == "" {
				if err == nil {
					t.Fatal("helper returned no error, want engine initialization error")
				}
				return
			}
			if err == nil || err.Error() != test.want {
				t.Fatalf("helper error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestKabibiHelperSystemMessageExplainsSemanticFileSearch(t *testing.T) {
	const taskDescription = "Inspect the browser implementation and avoid unrelated files."
	got := kabibiHelperSystemMessage(taskDescription)
	for _, want := range []string{
		"find_files",
		"find_content",
		"Semantic content search is enabled by default",
		"find_paths only for structural globs",
		"read_file",
		taskDescription,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("helper system message missing %q:\n%s", want, got)
		}
	}
}

func TestKabibiHelperUsesSemanticContentSearchForConceptDiscovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "browser.go"), []byte("func startBrowserRuntime() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	execSess, err := NewExecutionSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode helper request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch requests.Add(1) {
		case 1:
			if len(request.Messages) < 2 || request.Messages[0].Role != "system" ||
				!strings.Contains(request.Messages[0].Content, "Semantic content search is enabled by default") ||
				!strings.Contains(request.Messages[0].Content, "task-specific discovery constraints") ||
				request.Messages[1].Role != "user" ||
				!strings.Contains(request.Messages[1].Content, "Find the browser runtime initialization") {
				t.Errorf("helper request messages do not include baseline and task instructions: %#v", request.Messages)
			}
			hasFindFiles := false
			for _, tool := range request.Tools {
				if tool.Function.Name == "find_files" {
					hasFindFiles = true
					break
				}
			}
			if !hasFindFiles {
				t.Errorf("helper request tools do not include find_files: %#v", request.Tools)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"find_files","arguments":"{\"find_content\":\"browser runtime initialization\",\"max_results\":4}"}}]}}]}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "data: [DONE]")
			_, _ = fmt.Fprintln(w)
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Found the browser runtime implementation."}}]}`)
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w, "data: [DONE]")
			_, _ = fmt.Fprintln(w)
		default:
			t.Errorf("unexpected helper request %d", requests.Load())
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	engine, err := ai.NewRemoteEngine(ai.RemoteEngineConfig{
		Provider:   ai.RemoteOpenAI,
		Model:      "test-model",
		APIKey:     "test-token",
		BaseURL:    server.URL + "/v1/chat/completions",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	embedder := &commandTestEmbedder{}
	parentSess, err := ai.NewSession(ai.SessionConfig{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	helper := newKabibiHelperTool(embedder, execSess, 0, false, nil)
	_, err = helper.ExecuteWithContext(context.Background(), &ai.ToolContext{Session: parentSess}, ai.ToolCall{
		Name: "helper",
		Arguments: map[string]any{
			"task":        "Find the browser runtime initialization",
			"description": "task-specific discovery constraints",
			"explanation": "locate the implementation",
		},
	})
	if err != nil {
		t.Fatalf("execute controlled helper session: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("helper made %d model requests, want a search call and final response", got)
	}
	if embedder.batchCalls == 0 {
		t.Fatalf("semantic content search made no embedding batches: query calls=%d, batch calls=%d",
			embedder.queryCalls, embedder.batchCalls)
	}
}

func TestLastHelperToolResponseUsesMostRecentToolPart(t *testing.T) {
	history := []ai.PromptPart{
		{Role: ai.RoleTool, ToolName: "read_file", ToolResponse: "first"},
		{Role: ai.RoleModel, Text: "intermediate"},
		{Role: ai.RoleTool, ToolName: "write_file", ToolResponse: "last"},
		{Role: ai.RoleModel, Text: "done"},
	}
	got := lastHelperToolResponse(history)
	if got == nil || got.Name != "write_file" || got.Response != "last" {
		t.Fatalf("lastHelperToolResponse() = %#v, want write_file response last", got)
	}
	if got := lastHelperToolResponse([]ai.PromptPart{{Role: ai.RoleModel, Text: "no tools"}}); got != nil {
		t.Fatalf("lastHelperToolResponse() = %#v, want nil without tool responses", got)
	}
}

func TestLazyAIRunnerRejectsConcurrentTurn(t *testing.T) {
	runner := NewLazyAIRunner(nil)
	_, err := runner.beginOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.endOperation()

	if err := runner.RunTurn(context.Background(), "nested", nil, nil); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("RunTurn() error = %v, want %v", err, ErrAIRunnerBusy)
	}
	if err := runner.SetVerbose(true); !errors.Is(err, ErrAIRunnerBusy) {
		t.Fatalf("SetVerbose() error = %v, want %v", err, ErrAIRunnerBusy)
	}
}

func TestLazyAIRunnerVerboseEnablesFindFilesDiagnostics(t *testing.T) {
	previousVerbose := ai.Verbose
	t.Cleanup(func() { ai.Verbose = previousVerbose })

	runner := NewLazyAIRunner(nil)
	if err := runner.SetVerbose(true); err != nil {
		t.Fatalf("SetVerbose(true): %v", err)
	}
	if options := findFilesOptions(context.Background(), FindFilesInput{}, nil); options.OnDiagnostic == nil {
		t.Fatal("verbose runner did not enable find-files diagnostics")
	}

	if err := runner.SetVerbose(false); err != nil {
		t.Fatalf("SetVerbose(false): %v", err)
	}
	if options := findFilesOptions(context.Background(), FindFilesInput{}, nil); options.OnDiagnostic != nil {
		t.Fatal("non-verbose runner enabled find-files diagnostics")
	}
}

func TestClampModelContextTokensIgnoresDynamicDefault(t *testing.T) {
	for _, test := range []struct {
		name string
		info *ai.ModelInfo
		want int
	}{
		{name: "unknown model info", want: 16384},
		{name: "dynamic default is not a maximum", info: &ai.ModelInfo{MaxContextTokens: 4096, IsDynamicContext: true}, want: 16384},
		{name: "fixed context is a maximum", info: &ai.ModelInfo{MaxContextTokens: 4096}, want: 4096},
		{name: "fixed context above request", info: &ai.ModelInfo{MaxContextTokens: 32768}, want: 16384},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := clampModelContextTokens(16384, test.info); got != test.want {
				t.Fatalf("clampModelContextTokens() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestLazyAIRunnerCloseCancelsActiveOperationWithoutWaiting(t *testing.T) {
	runner := NewLazyAIRunner(nil)
	ctx, err := runner.beginOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	closeReturned := make(chan struct{})
	go func() {
		runner.Close()
		close(closeReturned)
	}()
	select {
	case <-closeReturned:
	case <-time.After(time.Second):
		t.Fatal("Close() waited for the active operation")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close() did not cancel the active operation")
	}

	runner.endOperation()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.WaitClosed(waitCtx); err != nil {
		t.Fatalf("WaitClosed() error = %v", err)
	}
}

func TestEphemeralLineWriterRewritesAndWipesStatus(t *testing.T) {
	var buf bytes.Buffer
	ephemeral := NewEphemeralLineWriter(&buf)

	if err := ephemeral.Feedback("📝 bridge.txt ... "); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Feedback("📝 bridge.txt ... ok"); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Feedback("📖 bridge.txt ... 20 bytes read"); err != nil {
		t.Fatal(err)
	}
	if err := ephemeral.Wipe(); err != nil {
		t.Fatal(err)
	}

	want := "📝 bridge.txt ..." +
		"\r📝 bridge.txt ... ok" +
		"\r📖 bridge.txt ... 20 bytes read" +
		"\r                               \r"
	if got := buf.String(); got != want {
		t.Fatalf("EphemeralLineWriter output = %q, want %q", got, want)
	}
}

func TestAdaptiveEmbedderBackgroundBatchAndHelperUsesLiveReopenedEngine(t *testing.T) {
	execSess, err := NewExecutionSession(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer execSess.Close()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"find_files\",\"arguments\":\"{\\\"find_paths\\\":\\\"*.txt\\\",\\\"explanation\\\":\\\"check files\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"found files\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	makeEngine := func() *ai.Engine {
		eng, err := ai.NewRemoteEngine(ai.RemoteEngineConfig{
			Provider:   ai.RemoteOpenAI,
			Model:      "test-model",
			APIKey:     "test-token",
			BaseURL:    server.URL + "/v1/chat/completions",
			HTTPClient: server.Client(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return eng
	}

	runner := NewLazyAIRunner(execSess)
	initialEngine := makeEngine()
	runner.lifeMu.Lock()
	runner.engine = initialEngine
	runner.lifeMu.Unlock()

	var reopenCount atomic.Int32
	adaptive := newAdaptiveEmbedder(
		runner,
		nil,
		func() (*ai.Embedder, error) {
			return nil, errors.New("no native embedder in unit test")
		},
		func() (*ai.Engine, error) {
			reopenCount.Add(1)
			return makeEngine(), nil
		},
	)
	sess, err := ai.NewSession(ai.SessionConfig{
		Engine: initialEngine,
		ReopenEngine: func(context.Context) (*ai.Engine, error) {
			next := makeEngine()
			runner.lifeMu.Lock()
			runner.engine = next
			runner.lifeMu.Unlock()
			return next, nil
		},
		Embedder: adaptive,
	})
	if err != nil {
		t.Fatal(err)
	}
	adaptive.BindSession(sess)
	defer sess.Shutdown()

	// Entering a sequential background batch via FlushEmbeddings closes initialEngine
	// once and leaves runner.engine and sess.Engine() nil until a foreground turn reopens it.
	sess.CanonicalHistory = []ai.PromptPart{{Role: ai.RoleUser, Text: "embed me"}}
	if err := sess.FlushEmbeddings(); err != nil {
		t.Fatalf("FlushEmbeddings() error = %v", err)
	}
	if sess.Engine() != nil {
		t.Fatal("expected sess.Engine() to be nil after sequential embedding batch")
	}
	runner.lifeMu.Lock()
	runnerEng := runner.engine
	runner.lifeMu.Unlock()
	if runnerEng != nil {
		t.Fatal("expected runner.engine to be nil after entering sequential background batch")
	}

	// Simulate reopening the engine on a subsequent foreground turn, then verify
	// newKabibiHelperTool picks up the live reopened engine via tc.Session.Engine().
	reopened := makeEngine()
	defer reopened.Close()
	if err := sess.ReplaceEngine(reopened); err != nil {
		t.Fatal(err)
	}
	if sess.Engine() != reopened {
		t.Fatal("sess.Engine() did not update to reopened engine")
	}

	helper := newKabibiHelperTool(&commandTestEmbedder{}, execSess, 0, false, nil)
	res, err := helper.ExecuteWithContext(context.Background(), &ai.ToolContext{Session: sess}, ai.ToolCall{
		Name: "helper",
		Arguments: map[string]any{
			"task":        "List txt files",
			"description": "verify live reopened engine is used",
			"explanation": "confirm the reopened engine is active",
		},
	})
	if err != nil {
		t.Fatalf("helper.ExecuteWithContext() error = %v", err)
	}
	outMap, ok := res.(helperResult)
	if !ok || outMap.FinalText != "found files" {
		t.Fatalf("helper result = %#v, want FinalText=%q", res, "found files")
	}
	_ = reopenCount
}
