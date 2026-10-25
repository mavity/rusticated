package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalHistorySurvivesCompaction(t *testing.T) {
	s := &Session{
		compactionLimit: 4096,
		compressor: func(context.Context, string, string) (string, error) {
			return "summary of old exchange", nil
		},
	}
	raw := []PromptPart{
		{Role: RoleUser, Text: "old question"},
		{Role: RoleModel, Text: "old answer"},
		{Role: RoleUser, Text: "recent question 1"},
		{Role: RoleModel, Text: "recent answer 1"},
		{Role: RoleUser, Text: "recent question 2"},
		{Role: RoleModel, Text: "recent answer 2"},
	}
	s.appendHistory(raw...)

	if err := s.compactContext(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.CanonicalHistory, raw) {
		t.Fatalf("canonical history changed during compaction:\n got %#v\nwant %#v", s.CanonicalHistory, raw)
	}
	if len(s.CompactedHistory) >= len(raw) {
		t.Fatalf("compacted history was not reduced: %#v", s.CompactedHistory)
	}
	if !strings.Contains(s.CompactedHistory[0].Text, "summary of old exchange") {
		t.Fatalf("compacted history does not include summary: %#v", s.CompactedHistory)
	}

	next := PromptPart{Role: RoleUser, Text: "next question"}
	s.appendHistory(next)
	if got := s.CanonicalHistory[len(s.CanonicalHistory)-1]; !reflect.DeepEqual(got, next) {
		t.Fatalf("canonical history did not append raw next turn: %#v", got)
	}
	if got := s.CompactedHistory[len(s.CompactedHistory)-1]; !reflect.DeepEqual(got, next) {
		t.Fatalf("compacted history did not append next turn: %#v", got)
	}
}

func TestIsContextOverflowError(t *testing.T) {
	cases := []string{
		"LiteRT stream error: INVALID_ARGUMENT: Input token ids are too long. Exceeding the maximum number of tokens allowed: 26439 >= 16001",
		"Prefill input length exceeds available state entries",
		"Max number of tokens reached, context window out of bounds",
	}
	for _, msg := range cases {
		if !isContextOverflowError(fmtError(msg)) {
			t.Fatalf("expected %q to be recognized as context overflow", msg)
		}
	}
}

type fmtError string

func (e fmtError) Error() string { return string(e) }

type captureHistoryBackend struct {
	history []PromptPart
}

func (b *captureHistoryBackend) NewConversation(cfg ConversationConfig) (conversationBackend, error) {
	b.history = append([]PromptPart(nil), cfg.History...)
	return &captureHistoryConversation{}, nil
}

func (*captureHistoryBackend) Close() error { return nil }

type captureHistoryConversation struct{}

func (*captureHistoryConversation) SendStream(context.Context, []PromptPart, func(ModelEvent)) error {
	return nil
}

func (*captureHistoryConversation) Close() error { return nil }

func TestReplaceEngineRestoresCompactedHistory(t *testing.T) {
	history := []PromptPart{
		{Role: RoleUser, Text: "retained question"},
		{Role: RoleModel, Text: "retained answer"},
	}
	backend := &captureHistoryBackend{}
	engine := &Engine{impl: &engineImpl{backend: backend}}
	session := &Session{
		systemMessage:    "test system",
		CompactedHistory: history,
	}
	if err := session.ReplaceEngine(engine); err != nil {
		t.Fatalf("ReplaceEngine() error = %v", err)
	}
	if !reflect.DeepEqual(backend.history, history) {
		t.Fatalf("restored history = %#v, want %#v", backend.history, history)
	}
	session.Close()
}

func TestNewSessionCompilesDeterministicToolSchemas(t *testing.T) {
	originalProperties := map[string]any{
		"item": map[string]any{"type": "string", "description": "The item"},
	}
	session, err := NewSession(SessionConfig{
		Tools: map[string]Tool{
			"z_tool": {
				Description: "Second tool",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"count": map[string]any{"type": "integer"},
					},
					"required": []string{"count"},
				},
			},
			"a_tool": {
				Description: "First tool",
				Parameters:  originalProperties,
				Required:    []string{"item"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.tools) != 2 {
		t.Fatalf("compiled tools = %d, want 2", len(session.tools))
	}
	first, _ := session.tools[0]["function"].(map[string]any)
	if first["name"] != "a_tool" {
		t.Fatalf("first compiled tool = %v, want a_tool", first["name"])
	}
	for i, want := range []string{"a_tool", "z_tool"} {
		fn, _ := session.tools[i]["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		props, _ := params["properties"].(map[string]any)
		explanation, _ := props["explanation"].(map[string]any)
		if fn["name"] != want || explanation["type"] != "string" {
			t.Fatalf("compiled %s schema missing explanation: %#v", want, session.tools[i])
		}
		required, _ := params["required"].([]string)
		foundExplanation := false
		for _, name := range required {
			if name == "explanation" {
				foundExplanation = true
			}
		}
		if !foundExplanation {
			t.Fatalf("%s required fields omit explanation: %v", want, required)
		}
	}
	if _, mutated := originalProperties["explanation"]; mutated {
		t.Fatal("schema compilation mutated the caller's property map")
	}
}

func TestSessionToolDispatchReturnsErrorResults(t *testing.T) {
	session, err := NewSession(SessionConfig{Tools: map[string]Tool{
		"fail": {
			Execute: func(context.Context, ToolCall, LogFeedback) (any, error) {
				return nil, fmtError("failed execution")
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "missing", want: "unknown tool: missing"},
		{name: "fail", want: "failed execution"},
	} {
		result, _ := session.executeTool(context.Background(), nil, ToolCall{Name: test.name}).(map[string]any)
		if result["error"] != test.want {
			t.Fatalf("dispatch %q error = %v, want %q", test.name, result["error"], test.want)
		}
	}
}

type fakeEmbedder struct {
	calls int
	block chan struct{}
}

func (f *fakeEmbedder) Embed(parts []PromptPart) ([]float32, error) {
	if f.block != nil {
		<-f.block
	}
	f.calls++
	return []float32{float32(f.calls), float32(len(parts[0].Text))}, nil
}

func sessionTestTools() map[string]Tool {
	return map[string]Tool{
		"read_file": {
			Description: "Read a test file.",
			Parameters: map[string]any{
				"path": map[string]any{"type": "string", "description": "The file path to read"},
			},
			Required: []string{"path"},
			Execute: func(ctx context.Context, call ToolCall, feedback LogFeedback) (any, error) {
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(call.RawArguments, &args); err != nil {
					return nil, err
				}
				if feedback != nil {
					feedback("📖 reading test file")
				}
				data, err := os.ReadFile(args.Path)
				if err != nil {
					return nil, err
				}
				return map[string]any{"content": string(data)}, nil
			},
		},
	}
}

func TestBackgroundEmbedWorkerPopulatesHistoryAndYieldsToTurn(t *testing.T) {
	emb := &fakeEmbedder{}
	s := &Session{embedder: emb}
	defer s.Shutdown()

	s.appendHistory(
		PromptPart{Role: RoleUser, Text: "hello"},
		PromptPart{Role: RoleModel, Text: "world"},
	)

	s.FlushEmbeddings()

	if emb.calls != 2 {
		t.Fatalf("embed calls = %d, want 2", emb.calls)
	}
	for i, p := range s.CanonicalHistory {
		if len(p.Embedding) != 2 {
			t.Fatalf("CanonicalHistory[%d].Embedding = %v, want 2 elements", i, p.Embedding)
		}
	}
	for i, p := range s.CompactedHistory {
		if len(p.Embedding) != 2 {
			t.Fatalf("CompactedHistory[%d].Embedding = %v, want 2 elements", i, p.Embedding)
		}
	}

	// Simulate turnWaiting pre-empting background embedding between items.
	emb2 := &fakeEmbedder{}
	s2 := &Session{embedder: emb2}
	defer s2.Shutdown()

	s2.appendHistory(
		PromptPart{Role: RoleUser, Text: "turn 1"},
		PromptPart{Role: RoleModel, Text: "reply 1"},
	)
	s2.turnWaiting.Store(true)
	s2.processPendingEmbeddings(false)
	if emb2.calls != 0 {
		t.Fatalf("expected worker to yield when turnWaiting is true, got %d calls", emb2.calls)
	}

	s2.turnWaiting.Store(false)
	s2.processPendingEmbeddings(false)
	if emb2.calls != 2 {
		t.Fatalf("expected worker to embed 2 items once turnWaiting cleared, got %d", emb2.calls)
	}
}

func TestFormatEmbeddingSummary(t *testing.T) {
	vec := make([]float32, 768)
	vec[0] = 0.0412
	vec[1] = -0.0189
	vec[2] = 0.0731
	vec[767] = -0.0054

	got := formatEmbeddingSummary(vec)
	want := "dim=768 [+0.0412, -0.0189, +0.0731, ..., -0.0054] l2=0.0862 simhash=bfffffffffffffff"
	if got != want {
		t.Fatalf("formatEmbeddingSummary =\n got %q\nwant %q", got, want)
	}
}

func TestGroupIntoAtomicUnitsPreservesToolExchanges(t *testing.T) {
	history := []PromptPart{
		{Role: RoleUser, Text: "read two files", Embedding: []float32{1, 0}},
		{
			Role:      RoleModel,
			Text:      "reading",
			ToolCalls: []ToolCall{{Name: "read_file"}, {Name: "read_file"}},
			Embedding: []float32{0, 1},
		},
		{Role: RoleTool, ToolName: "read_file", ToolResponse: map[string]any{"content": "A"}, Embedding: []float32{0, 1}},
		{Role: RoleTool, ToolName: "read_file", ToolResponse: map[string]any{"content": "B"}, Embedding: []float32{0, 1}},
		{Role: RoleModel, Text: "done", Embedding: []float32{1, 0}},
	}
	units := groupIntoAtomicUnits(history)
	if len(units) != 3 {
		t.Fatalf("got %d units, want 3 (user, model+2 tools, model)", len(units))
	}
	if len(units[1].parts) != 3 {
		t.Fatalf("tool exchange unit has %d parts, want 3", len(units[1].parts))
	}
	if units[1].embedding[1] < 0.99 {
		t.Fatalf("tool exchange normalized embedding = %v, want ~[0, 1]", units[1].embedding)
	}
}

func TestRAGProjectionPinsRelevantAnchorsAndResurrectsFromCanonical(t *testing.T) {
	// Topic A vector: [1, 0], Topic B vector: [0, 1]
	vecA := []float32{1, 0}
	vecB := []float32{0, 1}

	s := &Session{
		compactionLimit: 4096,
		compressor: func(ctx context.Context, sys, user string) (string, error) {
			return "compressed-gap", nil
		},
	}

	// Build 12 units:
	// U0..U1: Topic A (early requirement & schema)
	// U2..U7: Topic B (6 units of tangent exploration)
	// U8..U11: Topic A returns in the last X/2 = 4 units!
	history := []PromptPart{
		{Role: RoleUser, Text: "Topic A schema definition", Embedding: vecA},
		{Role: RoleModel, Text: "Topic A schema ack", Embedding: vecA},
		{Role: RoleUser, Text: "Topic B tangent 1", Embedding: vecB},
		{Role: RoleModel, Text: "Topic B reply 1", Embedding: vecB},
		{Role: RoleUser, Text: "Topic B tangent 2", Embedding: vecB},
		{Role: RoleModel, Text: "Topic B reply 2", Embedding: vecB},
		{Role: RoleUser, Text: "Topic B tangent 3", Embedding: vecB},
		{Role: RoleModel, Text: "Topic B reply 3", Embedding: vecB},
		{Role: RoleUser, Text: "Back to Topic A step 1", Embedding: vecA},
		{Role: RoleModel, Text: "Working on Topic A step 1", Embedding: vecA},
		{Role: RoleUser, Text: "Back to Topic A step 2", Embedding: vecA},
		{Role: RoleModel, Text: "Working on Topic A step 2", Embedding: vecA},
	}

	projected := s.projectHistory(context.Background(), history)

	// U0..U1 (Topic A in discard zone) must be pinned verbatim, while U2..U3 (Topic B in discard zone) get summarized!
	foundPinnedTopicA := false
	foundSummary := false
	for _, p := range projected {
		if p.Text == "Topic A schema definition" || p.Text == "Topic A schema ack" {
			foundPinnedTopicA = true
		}
		if strings.HasPrefix(p.Text, "[Context summary]: ") {
			foundSummary = true
		}
	}
	if !foundPinnedTopicA {
		t.Fatalf("expected early Topic A anchor to be pinned verbatim in projected history: %#v", projected)
	}
	if !foundSummary {
		t.Fatalf("expected unpinned Topic B gap to be summarized into [Context summary]: %#v", projected)
	}
}

func TestCompressTextBlockStepDownAndEscapeHatch(t *testing.T) {
	var attempts int
	var budgets []string
	s := &Session{
		compressor: func(ctx context.Context, sys, user string) (string, error) {
			attempts++
			budgets = append(budgets, sys)
			// Always return a verbose 30-word output to trigger step-down retries and escape hatch
			return strings.Repeat("verboseword ", 30), nil
		},
	}

	input := strings.Repeat("inputword ", 40) // 40 words -> initial target = 16 words
	out := s.compressTextBlock(context.Background(), input)
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (initial + 2 retries)", attempts)
	}
	// After 3 attempts (16 -> 12 -> 10), escape hatch truncates to final wTarget (10 words)
	if gotWords := wordCount(out); gotWords > 12 {
		t.Fatalf("output word count = %d, want <= 12 (truncated by escape hatch)", gotWords)
	}
}

func TestSummarizeGapRecursiveHalving(t *testing.T) {
	var calls int
	s := &Session{
		compactionLimit: 100, // gapTokenBudget = 50 tokens (~200 bytes)
		compressor: func(ctx context.Context, sys, user string) (string, error) {
			calls++
			return fmt.Sprintf("part%d", calls), nil
		},
	}

	units := []atomicTurnUnit{
		{parts: []PromptPart{{Role: RoleUser, Text: strings.Repeat("a ", 80)}}},
		{parts: []PromptPart{{Role: RoleModel, Text: strings.Repeat("b ", 80)}}},
	}
	got := s.summarizeGap(context.Background(), units)
	if calls < 2 {
		t.Fatalf("expected recursive halving across units, got %d compressor calls", calls)
	}
	if got != "part1 part2" {
		t.Fatalf("summarizeGap = %q, want %q", got, "part1 part2")
	}
}

func TestCollapseUnpinnedLargeBlocksCachesAndPrefixesCompacted(t *testing.T) {
	var prompts []string
	var inputs []string
	s := &Session{
		compactionLimit: 1000, // gapTokenBudget = 500, largeBlockByteThreshold = 512 bytes
		compressor: func(ctx context.Context, sys, user string) (string, error) {
			prompts = append(prompts, sys)
			inputs = append(inputs, user)
			return "found port 8080 in config", nil
		},
	}

	largeOutput := strings.Repeat("config_line=1\n", 80) // > 512 bytes
	pinnedOutput := strings.Repeat("pinned_line=1\n", 80)

	units := []atomicTurnUnit{
		{
			parts: []PromptPart{
				{
					Role: RoleModel,
					ToolCalls: []ToolCall{
						{Name: "read_file", RawArguments: []byte(`{"path":"app.conf","explanation":"find server port"}`)},
					},
				},
				{
					Role:         RoleTool,
					ToolName:     "read_file",
					Explanation:  "find server port",
					ToolResponse: map[string]any{"content": largeOutput},
				},
			},
		},
		{
			parts: []PromptPart{
				{
					Role:         RoleTool,
					ToolName:     "read_file",
					Explanation:  "keep pinned",
					ToolResponse: map[string]any{"content": pinnedOutput},
				},
			},
		},
	}

	pinned := map[int]bool{1: true}
	s.collapseUnpinnedLargeBlocks(context.Background(), units, 2, pinned)

	// 1. Unpinned unit[0] must be collapsed with "COMPACTED: " prefix and guided by explanation.
	if len(prompts) != 1 || !strings.Contains(prompts[0], "find server port") {
		t.Fatalf("expected 1 compressor call with explanation 'find server port', got %v", prompts)
	}
	resp0, _ := units[0].parts[1].ToolResponse.(map[string]any)
	gotOut, _ := resp0["output"].(string)
	if gotOut != "COMPACTED: found port 8080 in config" {
		t.Fatalf("unpinned large tool output = %q, want %q", gotOut, "COMPACTED: found port 8080 in config")
	}

	// 2. Pinned unit[1] must remain untouched.
	resp1, _ := units[1].parts[0].ToolResponse.(map[string]any)
	if resp1["content"] != pinnedOutput {
		t.Fatalf("pinned unit tool output was modified")
	}

	// 3. Re-running on identical unpinned artifact must hit artifactCache with zero extra LLM calls.
	unitsAgain := []atomicTurnUnit{
		{
			parts: []PromptPart{
				{
					Role:         RoleTool,
					ToolName:     "read_file",
					Explanation:  "find server port",
					ToolResponse: map[string]any{"content": largeOutput},
				},
			},
		},
	}
	s.collapseUnpinnedLargeBlocks(context.Background(), unitsAgain, 1, nil)
	if len(prompts) != 1 {
		t.Fatalf("expected artifactCache hit (1 total call), got %d calls", len(prompts))
	}
}

func TestLiveStreamerZeroANSISpec(t *testing.T) {
	var out bytes.Buffer
	ls := newLiveStreamer(func(text string) { _, _ = out.WriteString(text) }, 20)

	ls.StartStage("goal")
	ls.Feed("hello\n  world   from   exceedingly long text")
	captured := ls.EndStage()

	if captured != "hello\n  world   from   exceedingly long text" {
		t.Fatalf("captured raw text = %q", captured)
	}
	// "[goal] " is 7 runes; with maxRunes=20, 12 runes of content + '…' = 20 runes total.
	if got := out.String(); got != "[goal] hello world …" {
		t.Fatalf("stage 1 visual output = %q, want %q", got, "[goal] hello world …")
	}

	out.Reset()
	ls.StartStage("steps")
	ls.Feed("short")
	_ = ls.EndStage()

	wantTransition := "\r" + strings.Repeat(" ", 20) + "\r[steps] short"
	if got := out.String(); got != wantTransition {
		t.Fatalf("stage transition = %q, want %q", got, wantTransition)
	}

	out.Reset()
	ls.Wipe()
	wantWipe := "\r" + strings.Repeat(" ", len("[steps] short")) + "\r"
	if got := out.String(); got != wantWipe {
		t.Fatalf("wipe output = %q, want %q", got, wantWipe)
	}
}

func TestRunTurnDirectAnswerUsesSingleCall(t *testing.T) {
	var out bytes.Buffer
	var modelCalls int
	s, err := NewSession(SessionConfig{
		SystemMessage: "You are Pope.",
		OnContent:     func(text string) { _, _ = out.WriteString(text) },
		OnFeedback:    func(text string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		modelCalls++
		if cfg.Tools != nil {
			t.Fatalf("unexpected tools in a session without registered tools")
		}
		onEvent(TextEvent{Text: "Pont Neuf is the oldest standing bridge in Paris."})
		return nil
	}

	if err := s.RunTurn(context.Background(), "Name a bridge in Paris"); err != nil {
		t.Fatal(err)
	}
	if modelCalls != 1 {
		t.Fatalf("model calls = %d, want one", modelCalls)
	}
	if got := out.String(); got != "Pont Neuf is the oldest standing bridge in Paris." {
		t.Fatalf("terminal output = %q", got)
	}
	if len(s.CanonicalHistory) != 2 {
		t.Fatalf("CanonicalHistory len = %d, want 2", len(s.CanonicalHistory))
	}
}

func TestRunTurnUnprefixedAnswerUsesSingleCall(t *testing.T) {
	var out bytes.Buffer
	var modelCalls int
	s, err := NewSession(SessionConfig{
		SystemMessage: "You are Pope.",
		OnContent:     func(text string) { _, _ = out.WriteString(text) },
		OnFeedback:    func(text string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		modelCalls++
		if cfg.Tools != nil {
			t.Fatalf("unexpected tools in a session without registered tools")
		}
		onEvent(TextEvent{Text: "Pont"})
		onEvent(TextEvent{Text: " Neuf."})
		return nil
	}

	if err := s.RunTurn(context.Background(), "Name a bridge in Paris"); err != nil {
		t.Fatal(err)
	}
	if modelCalls != 1 {
		t.Fatalf("model calls = %d, want one", modelCalls)
	}
	if got := out.String(); got != "Pont Neuf." {
		t.Fatalf("terminal output = %q, want %q", got, "Pont Neuf.")
	}
	if len(s.CanonicalHistory) != 2 || s.CanonicalHistory[1].Text != "Pont Neuf." {
		t.Fatalf("unexpected canonical history: %#v", s.CanonicalHistory)
	}
}

func TestRunTurnToolExecutionAndHistory(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "bridge.txt")
	if err := os.WriteFile(tmpFile, []byte("Pont Alexandre III"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathJSON, _ := json.Marshal(tmpFile)

	var out bytes.Buffer
	var feedback strings.Builder
	emb := &fakeEmbedder{}
	var modelCalls int

	s, err := NewSession(SessionConfig{
		Embedder:      emb,
		SystemMessage: "You are Pope.",
		Tools:         sessionTestTools(),
		OnContent:     func(text string) { _, _ = out.WriteString(text) },
		OnFeedback:    func(text string) { _, _ = feedback.WriteString(text) },
	})
	if err != nil {
		t.Fatal(err)
	}
	s.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		modelCalls++
		if len(cfg.Tools) == 0 {
			t.Fatal("tool-enabled turn must expose registered tools")
		}
		switch modelCalls {
		case 1:
			if len(cfg.History) != 0 || len(trigger) != 1 || trigger[0].Text != "Which bridge is in bridge.txt?" {
				t.Fatalf("initial model input = history %#v, trigger %#v", cfg.History, trigger)
			}
			onEvent(ToolCallEvent{ToolCalls: []ToolCall{{
				Name:         "read_file",
				RawArguments: json.RawMessage(`{"path":` + string(pathJSON) + `,"explanation":"read bridge name"}`),
			}}})
		case 2:
			if len(trigger) != 1 || trigger[0].Role != RoleTool {
				t.Fatalf("tool result turn = %#v, want one tool response", trigger)
			}
			onEvent(TextEvent{Text: "The bridge in the file is Pont Alexandre III."})
		default:
			t.Fatalf("unexpected model call %d", modelCalls)
		}
		return nil
	}
	defer s.Shutdown()

	if err := s.RunTurn(context.Background(), "Which bridge is in bridge.txt?"); err != nil {
		t.Fatal(err)
	}

	if modelCalls != 2 {
		t.Fatalf("model calls = %d, want 2", modelCalls)
	}
	if got := out.String(); got != "The bridge in the file is Pont Alexandre III." {
		t.Fatalf("spoken content = %q", got)
	}
	if status := feedback.String(); !strings.Contains(status, "📖 reading test file\n") {
		t.Fatalf("feedback lacks tool status: %q", status)
	}
	if strings.Contains(feedback.String(), "The bridge in the file is Pont Alexandre III.") {
		t.Fatal("spoken response leaked into impermanent feedback")
	}
	// The ordinary turn history retains the tool-call/response exchange.
	if len(s.CanonicalHistory) != 4 {
		t.Fatalf("CanonicalHistory len = %d, want 4: %#v", len(s.CanonicalHistory), s.CanonicalHistory)
	}
	finalPart := s.CanonicalHistory[3]
	if finalPart.Text != "The bridge in the file is Pont Alexandre III." {
		t.Fatalf("finalPart.Text = %q", finalPart.Text)
	}
	// Flush embeddings and verify ordinary history remains indexed.
	s.FlushEmbeddings()
	if len(s.CanonicalHistory[3].Embedding) == 0 {
		t.Fatalf("expected final answer embedding, got %#v", s.CanonicalHistory[3].Embedding)
	}
}

func TestSessionToolCanReplaceSessionHistory(t *testing.T) {
	// Verify that a tool created with NewSessionTool can modify or replace tc.Session
	// (e.g., an advanced memory tool re-compacting or resurfacing salient memories)
	// and the active turn immediately adopts the replaced Session's history!
	type MemoryRecallInput struct {
		_           struct{} `doc:"Resurface salient memories into session history." emoji:"🧲"`
		Query       string   `json:"query" doc:"Memory search query"`
		Explanation string   `json:"explanation" doc:"Why memory recall is needed"`
	}

	var observedHistoryInTurn2 []PromptPart
	memSession, err := NewSession(SessionConfig{
		SystemMessage: "You are an assistant.",
		Tools: map[string]Tool{
			"recall_memory": NewSessionTool(func(ctx context.Context, tc *ToolContext, in MemoryRecallInput) (any, error) {
				// Construct a replacement Session with resurfaced memory in CompactedHistory
				replacement := &Session{
					CanonicalHistory: append([]PromptPart{
						{Role: RoleUser, Text: "Salient memory: secret bridge is Pont Mirabeau"},
						{Role: RoleModel, Text: "Noted."},
					}, tc.Session.CanonicalHistory...),
					CompactedHistory: []PromptPart{
						{Role: RoleUser, Text: "Salient memory: secret bridge is Pont Mirabeau"},
						{Role: RoleModel, Text: "Noted."},
					},
				}
				tc.ReplaceSession(replacement)
				return map[string]any{"resurfaced": 1}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var turnStep int
	memSession.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		turnStep++
		switch turnStep {
		case 1:
			onEvent(ToolCallEvent{ToolCalls: []ToolCall{{
				Name:         "recall_memory",
				RawArguments: json.RawMessage(`{"query":"secret bridge","explanation":"fetch bridge from memory"}`),
			}}})
		case 2:
			observedHistoryInTurn2 = append([]PromptPart(nil), cfg.History...)
			onEvent(TextEvent{Text: "Pont Mirabeau"})
		}
		return nil
	}

	if err := memSession.RunTurn(context.Background(), "What is the secret bridge?"); err != nil {
		t.Fatal(err)
	}
	if len(observedHistoryInTurn2) == 0 || !strings.Contains(observedHistoryInTurn2[0].Text, "Pont Mirabeau") {
		t.Fatalf("expected replaced session history to be immediately visible on next tool turn, got: %#v", observedHistoryInTurn2)
	}
	if !strings.Contains(memSession.CompactedHistory[0].Text, "Pont Mirabeau") {
		t.Fatalf("expected memSession.CompactedHistory to retain replaced session state, got: %#v", memSession.CompactedHistory)
	}
}

func TestImmediateToolOverflowCompressesWithHeadTailOverlappedMiddleAndPurpose(t *testing.T) {
	type ReadFileInput struct {
		_           struct{} `doc:"Read a file."`
		Path        string   `json:"path" doc:"File path"`
		Explanation string   `json:"explanation" doc:"Purpose of reading the file"`
	}

	// Build a large 12,000-char payload with distinct HEAD, MIDDLE, and TAIL markers
	bigContent := "HEAD_MARKER_START " + strings.Repeat("alpha beta gamma delta ", 400) + " TAIL_MARKER_END"

	var compressorSysPrompts []string
	var compressorChunks []string
	var retriedToolResponse map[string]any
	var readFileCallCount int

	sess, err := NewSession(SessionConfig{
		SystemMessage:   "You are an assistant.",
		CompactionLimit: 1000, // gapTokenBudget = 500 -> chunkSize = 1000 runes
		Tools: map[string]Tool{
			"read_file": NewTool(func(ctx context.Context, in ReadFileInput, _ LogFeedback) (any, error) {
				readFileCallCount++
				return map[string]any{"content": bigContent}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.compressor = func(ctx context.Context, sysPrompt, userPrompt string) (string, error) {
		compressorSysPrompts = append(compressorSysPrompts, sysPrompt)
		compressorChunks = append(compressorChunks, userPrompt)
		return fmt.Sprintf("condensed_part_%d", len(compressorChunks)), nil
	}

	var step int
	sess.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		step++
		switch step {
		case 1:
			onEvent(ToolCallEvent{ToolCalls: []ToolCall{{
				ID:           "call_readme",
				Name:         "read_file",
				RawArguments: json.RawMessage(`{"path":"README.md","explanation":"extract architecture and vision for Ukrainian poem"}`),
			}}})
		case 2:
			// Raw 12 KB tool response triggers LiteRT context overflow.
			return fmt.Errorf("LiteRT stream error: INVALID_ARGUMENT: Input token ids are too long. Exceeding the maximum number of tokens allowed: 28085 >= 8192")
		case 3:
			// The compressed tool response is retried without rerunning the tool.
			// Verify the tool was NOT re-executed from scratch, and trigger[0] now holds the compressed map!
			if len(trigger) == 1 && trigger[0].Role == RoleTool {
				retriedToolResponse, _ = trigger[0].ToolResponse.(map[string]any)
			}
			onEvent(TextEvent{Text: "Код звучить як пісня."})
		}
		return nil
	}

	if err := sess.RunTurn(context.Background(), "Write a poem in Ukrainian"); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if readFileCallCount != 1 {
		t.Fatalf("read_file was called %d times, want 1 (must not restart the agentic turn from scratch)", readFileCallCount)
	}
	if len(compressorSysPrompts) < 2 {
		t.Fatalf("expected multiple overlapped middle chunks to be summarized, got %d chunks", len(compressorSysPrompts))
	}
	for i, sp := range compressorSysPrompts {
		if !strings.Contains(sp, "extract architecture and vision for Ukrainian poem") {
			t.Fatalf("compressor sysPrompt[%d] missing tool purpose note: %q", i, sp)
		}
	}
	// Verify adjacent chunks overlap
	if len(compressorChunks) >= 2 {
		first := compressorChunks[0]
		second := compressorChunks[1]
		tailOfFirst := first[len(first)-80:]
		if !strings.Contains(second, tailOfFirst) {
			t.Fatalf("expected chunk 1 and chunk 2 to overlap; tail of chunk 1 %q not found in chunk 2", tailOfFirst)
		}
	}
	if retriedToolResponse == nil {
		t.Fatal("expected retried tool response map to be passed to model")
	}
	if compressed, _ := retriedToolResponse["compressed_due_to_size"].(bool); !compressed {
		t.Fatalf("compressed_due_to_size = %v, want true", retriedToolResponse["compressed_due_to_size"])
	}
	headExcerpt, _ := retriedToolResponse["head_excerpt"].(string)
	tailExcerpt, _ := retriedToolResponse["tail_excerpt"].(string)
	midSummary, _ := retriedToolResponse["overlapped_middle_summary"].(string)
	notice, _ := retriedToolResponse["notice"].(string)
	if !strings.Contains(headExcerpt, "HEAD_MARKER_START") {
		t.Fatalf("head_excerpt missing HEAD_MARKER_START: %q", headExcerpt)
	}
	if !strings.Contains(tailExcerpt, "TAIL_MARKER_END") {
		t.Fatalf("tail_excerpt missing TAIL_MARKER_END: %q", tailExcerpt)
	}
	if !strings.Contains(midSummary, "condensed_part_1") || !strings.Contains(midSummary, "condensed_part_2") {
		t.Fatalf("overlapped_middle_summary missing condensed parts: %q", midSummary)
	}
	if !strings.Contains(notice, "Compression was necessary") || !strings.Contains(notice, "extract architecture and vision for Ukrainian poem") {
		t.Fatalf("notice missing compression explanation or purpose: %q", notice)
	}
}

func TestRecoverMalformedGemmaToolCalls(t *testing.T) {
	errStr := `LiteRT stream error: INVALID_ARGUMENT: Failed to parse tool calls from code block: call:readfile{explanation:<|"|>,path:<|"|>README.md<|"|>}
full response: <|tool_call>call:readfile{explanation:<|"|>,path:<|"|>README.md<|"|>}<tool_call|>`
	calls, _ := recoverMalformedGemmaToolCalls(errStr)
	if len(calls) == 0 {
		t.Fatal("expected recoverMalformedGemmaToolCalls to recover at least 1 call")
	}
	if calls[0].Name != "read_file" || calls[0].Arguments["path"] != "README.md" {
		t.Fatalf("recovered call = %+v, want read_file(path=README.md)", calls[0])
	}

	errStr2 := `<|tool_call>call:read_file{explanation:<|"|>Read the README.md file.<|"|>,<|"|>path:<|"|>,path:<|"|>README.md<|"|>,explanation:<|"|>}<tool_call|>`
	calls2, cleaned2 := recoverMalformedGemmaToolCalls(errStr2)
	if len(calls2) != 1 || calls2[0].Name != "read_file" || calls2[0].Arguments["path"] != "README.md" {
		t.Fatalf("recovered call 2 = %+v (cleaned=%q)", calls2, cleaned2)
	}
}
