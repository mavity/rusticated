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

func (*captureHistoryConversation) Clone() (conversationBackend, error) {
	return &captureHistoryConversation{}, nil
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

func TestRunTurnFastPathDirectBypass(t *testing.T) {
	var out bytes.Buffer
	var stageCalls int
	s, err := NewSession(SessionConfig{
		SystemMessage: "You are Pope.",
		OnContent:     func(text string) { _, _ = out.WriteString(text) },
		OnFeedback:    func(text string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		stageCalls++
		if cfg.Tools != nil {
			t.Fatalf("Stage 1 must have Tools: nil")
		}
		onEvent(TextEvent{Text: "DIR"})
		onEvent(TextEvent{Text: "ECT: "})
		onEvent(TextEvent{Text: "Pont Neuf is the oldest standing bridge in Paris."})
		return nil
	}

	if err := s.RunTurn(context.Background(), "Name a bridge in Paris"); err != nil {
		t.Fatal(err)
	}
	if stageCalls != 1 {
		t.Fatalf("stageCalls = %d, want 1 (fast path bypasses stages 2-6)", stageCalls)
	}
	if got := out.String(); got != "Pont Neuf is the oldest standing bridge in Paris." {
		t.Fatalf("terminal output = %q", got)
	}
	if len(s.CanonicalHistory) != 2 {
		t.Fatalf("CanonicalHistory len = %d, want 2", len(s.CanonicalHistory))
	}
	modelPart := s.CanonicalHistory[1]
	if modelPart.Text != "Pont Neuf is the oldest standing bridge in Paris." {
		t.Fatalf("modelPart.Text = %q", modelPart.Text)
	}
	if modelPart.CognitiveTrace == nil || !modelPart.CognitiveTrace.IsDirect {
		t.Fatalf("expected CognitiveTrace{IsDirect: true}, got %#v", modelPart.CognitiveTrace)
	}
}

func TestRunTurnFastPathUnprefixedStreamsImmediately(t *testing.T) {
	var out bytes.Buffer
	var stageCalls int
	s, err := NewSession(SessionConfig{
		SystemMessage: "You are Pope.",
		OnContent:     func(text string) { _, _ = out.WriteString(text) },
		OnFeedback:    func(text string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		stageCalls++
		if cfg.Tools != nil {
			t.Fatalf("Stage 1 must have Tools: nil")
		}
		onEvent(TextEvent{Text: "Pont"})
		if got := out.String(); got != "Pont" {
			t.Fatalf("unprefixed response was not streamed immediately: got %q", got)
		}
		onEvent(TextEvent{Text: " Neuf."})
		return nil
	}

	if err := s.RunTurn(context.Background(), "Name a bridge in Paris"); err != nil {
		t.Fatal(err)
	}
	if stageCalls != 1 {
		t.Fatalf("stageCalls = %d, want 1 (unprefixed fast path bypasses deliberation)", stageCalls)
	}
	if got := out.String(); got != "Pont Neuf." {
		t.Fatalf("terminal output = %q, want %q", got, "Pont Neuf.")
	}
	if len(s.CanonicalHistory) != 2 || s.CanonicalHistory[1].Text != "Pont Neuf." {
		t.Fatalf("unexpected canonical history: %#v", s.CanonicalHistory)
	}
}

func TestRunTurnFull7StageReflectivePipelineAndIcebergReplant(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "bridge.txt")
	if err := os.WriteFile(tmpFile, []byte("Pont Alexandre III"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathJSON, _ := json.Marshal(tmpFile)

	var out bytes.Buffer
	var feedback strings.Builder
	emb := &fakeEmbedder{}
	var stageIdx int
	var stage3SysMsgs []string

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
		stageIdx++
		switch stageIdx {
		case 1: // Stage 1: Triage -> GOAL:
			if cfg.Tools != nil {
				t.Fatal("Stage 1 must have Tools == nil")
			}
			onEvent(TextEvent{Text: "GOAL: Read bridge.txt and report the bridge name."})
		case 2: // Stage 2: Expectation Calibration -> <detail_level>
			if cfg.Tools != nil {
				t.Fatal("Stage 2 (expectation) must have Tools == nil")
			}
			onEvent(TextEvent{Text: "A single bridge name, maximum one short sentence."})
		case 3: // Stage 3: Steps
			if cfg.Tools != nil {
				t.Fatal("Stage 3 (steps) must have Tools == nil")
			}
			onEvent(TextEvent{Text: "1. Call read_file on bridge.txt. 2. State the bridge."})
		case 4, 5, 6: // Stage 4A, 4B, 4C
			if cfg.Tools != nil {
				t.Fatalf("Stage 4 critique branch %d must have Tools == nil", stageIdx-3)
			}
			stage3SysMsgs = append(stage3SysMsgs, cfg.SystemMessage)
			if !strings.HasPrefix(trigger[0].Text, "I think you might take these steps below") {
				t.Fatalf("Stage 4 role-swapped attribution missing: %q", trigger[0].Text)
			}
			onEvent(TextEvent{Text: fmt.Sprintf("critique-%d", stageIdx-3)})
		case 7: // Stage 5: Tribunal Join
			if cfg.Tools != nil {
				t.Fatal("Stage 5 (plan) must have Tools == nil")
			}
			onEvent(TextEvent{Text: "Read bridge.txt using read_file and answer concisely."})
		case 8: // Stage 6 turn 1: emits tool call
			if len(cfg.Tools) == 0 {
				t.Fatal("Stage 6 (work) must have Tools enabled")
			}
			onEvent(ToolCallEvent{ToolCalls: []ToolCall{{
				Name:         "read_file",
				RawArguments: json.RawMessage(`{"path":` + string(pathJSON) + `,"explanation":"read bridge name"}`),
			}}})
		case 9: // Stage 6 turn 2: receives tool output and produces verbose work_and_answer
			onEvent(TextEvent{Text: "Phase 1 complete. I read bridge.txt and found Pont Alexandre III."})
		case 10: // Stage 7: Convergence & Re-Formulation Flip
			if cfg.Tools != nil {
				t.Fatal("Stage 7 (convergence) must have Tools == nil")
			}
			// Verify seeded history contains only User(P) and Model(workAndAnswer), without raw tool turns
			if len(cfg.History) != 2 || cfg.History[1].Text != "Phase 1 complete. I read bridge.txt and found Pont Alexandre III." {
				t.Fatalf("Stage 7 convergence seeded history = %#v", cfg.History)
			}
			if !strings.Contains(trigger[0].Text, "A single bridge name, maximum one short sentence.") {
				t.Fatalf("Stage 7 convergence trigger missing detail_level: %q", trigger[0].Text)
			}
			onEvent(TextEvent{Text: "The bridge in the file is Pont Alexandre III."})
		default:
			t.Fatalf("unexpected stage call %d", stageIdx)
		}
		return nil
	}
	defer s.Shutdown()

	if err := s.RunTurn(context.Background(), "Which bridge is in bridge.txt?"); err != nil {
		t.Fatal(err)
	}

	if stageIdx != 10 {
		t.Fatalf("stageIdx = %d, want 10", stageIdx)
	}
	if got := out.String(); got != "The bridge in the file is Pont Alexandre III." {
		t.Fatalf("spoken content = %q", got)
	}
	if status := feedback.String(); !strings.Contains(status, "[goal]") || !strings.Contains(status, "📖 reading test file\n") {
		t.Fatalf("feedback lacks stage/tool status: %q", status)
	}
	if strings.Contains(feedback.String(), "The bridge in the file is Pont Alexandre III.") {
		t.Fatal("spoken response leaked into impermanent feedback")
	}
	for _, sm := range stage3SysMsgs {
		if !strings.Contains(sm, "read_file") {
			t.Fatalf("Critique SystemMessage missing available tool names: %q", sm)
		}
	}

	// Replant Iceberg: Verify only the clean spoken surface is in CanonicalHistory & CompactedHistory
	// [0] User, [1] Model+ToolCalls, [2] ToolResponse, [3] Model final answer with CognitiveTrace
	if len(s.CanonicalHistory) != 4 {
		t.Fatalf("CanonicalHistory len = %d, want 4: %#v", len(s.CanonicalHistory), s.CanonicalHistory)
	}
	finalPart := s.CanonicalHistory[3]
	if finalPart.Text != "The bridge in the file is Pont Alexandre III." {
		t.Fatalf("finalPart.Text = %q", finalPart.Text)
	}
	tr := finalPart.CognitiveTrace
	if tr == nil || tr.IsDirect {
		t.Fatalf("missing or direct CognitiveTrace: %#v", tr)
	}
	if tr.Goal != "Read bridge.txt and report the bridge name." ||
		tr.ExpectedDetail != "A single bridge name, maximum one short sentence." ||
		tr.InitialSteps != "1. Call read_file on bridge.txt. 2. State the bridge." ||
		tr.CritiqueRelevancy != "critique-1" ||
		tr.CritiqueSafety != "critique-2" ||
		tr.CritiqueGrounded != "critique-3" ||
		tr.SynthesizedPlan != "Read bridge.txt using read_file and answer concisely." ||
		tr.RawWorkAndAnswer != "Phase 1 complete. I read bridge.txt and found Pont Alexandre III." {
		t.Fatalf("unexpected CognitiveTrace fields: %#v", tr)
	}

	// Flush embeddings and verify both spoken parts and submerged CognitiveTrace are embedded
	s.FlushEmbeddings()
	if len(tr.TraceEmbeddings) != 1 || len(tr.TraceEmbeddings[0]) == 0 {
		t.Fatalf("expected TraceEmbeddings to be populated by background embedder, got %#v", tr.TraceEmbeddings)
	}
}

func TestDeliberateToolAndNativeCloneConversation(t *testing.T) {
	// 1. Verify ToolContext.CloneConversation delegates to Conversation.Clone (litert_lm_conversation_clone)
	d := newFakeDrv()
	d.onSend = func(d *fakeDrv, ud uint64) {
		go func() {
			d.emit(ud, `{"role":"assistant","content":[{"type":"text","text":"cloned reply"}]}`, false, "")
			d.emit(ud, "", true, "")
		}()
	}
	baseConv := &Conversation{impl: &convImpl{backend: newTestBackend(d)}}
	tc := &ToolContext{Conversation: baseConv}

	cloned, err := tc.CloneConversation(context.Background())
	if err != nil {
		t.Fatalf("CloneConversation failed: %v", err)
	}
	defer cloned.Close()

	var clonedText strings.Builder
	for ev, err := range cloned.SendStream(context.Background(), []PromptPart{{Role: RoleUser, Text: "ping"}}) {
		if err != nil {
			t.Fatalf("cloned SendStream error: %v", err)
		}
		if te, ok := ev.(TextEvent); ok {
			clonedText.WriteString(te.Text)
		}
	}
	if got := clonedText.String(); got != "cloned reply" {
		t.Fatalf("cloned conversation output = %q, want %q", got, "cloned reply")
	}

	// 2. Verify invoking the built-in "deliberate" tool directly via executeTool / ToolContext
	var feedback strings.Builder
	s, err := NewSession(SessionConfig{
		SystemMessage: "You are an assistant.",
		Tools:         sessionTestTools(),
		OnFeedback:    func(text string) { _, _ = feedback.WriteString(text) },
	})
	if err != nil {
		t.Fatal(err)
	}

	var subStage int
	s.ephemeralStream = func(ctx context.Context, cfg ConversationConfig, trigger []PromptPart, onEvent func(ModelEvent) bool) error {
		subStage++
		if cfg.Tools != nil {
			t.Fatalf("deliberate sub-stage %d must strip tools (Tools: nil)", subStage)
		}
		switch subStage {
		case 1: // expectation
			onEvent(TextEvent{Text: "One concise sentence."})
		case 2: // steps
			onEvent(TextEvent{Text: "Step 1: inspect file."})
		case 3, 4, 5: // 3 critiques
			onEvent(TextEvent{Text: "Looks good."})
		case 6: // plan
			onEvent(TextEvent{Text: "Read file and report."})
		}
		return nil
	}

	trace := &CognitiveTrace{}
	baseHist := []PromptPart{}
	delibTC := &ToolContext{
		Session:     s,
		Feedback:    s.emitFeedback,
		baseHistory: &baseHist,
		userText:    "Check config",
		trace:       trace,
	}
	res := s.executeTool(context.Background(), delibTC, ToolCall{
		Name:         DeliberateToolName,
		RawArguments: json.RawMessage(`{"goal":"Verify config settings","explanation":"need structured plan"}`),
	})
	out, ok := res.(*DeliberateOutput)
	if !ok || out == nil {
		t.Fatalf("expected *DeliberateOutput from deliberate tool, got %#v", res)
	}
	if out.ExpectedDetail != "One concise sentence." || out.Plan != "Read file and report." {
		t.Fatalf("unexpected DeliberateOutput: %#v", out)
	}
	if trace.Goal != "Verify config settings" || trace.SynthesizedPlan != "Read file and report." {
		t.Fatalf("expected CognitiveTrace to be populated by deliberate tool, got %#v", trace)
	}

	// 3. Verify that a tool created with NewSessionTool can modify or replace tc.Session
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
		case 1: // Stage 1: GOAL
			onEvent(TextEvent{Text: "GOAL: Recall the secret bridge."})
		case 2: // Expectation
			onEvent(TextEvent{Text: "Just the bridge name."})
		case 3: // Steps
			onEvent(TextEvent{Text: "Call recall_memory."})
		case 4, 5, 6: // Critiques
			onEvent(TextEvent{Text: "OK"})
		case 7: // Plan
			onEvent(TextEvent{Text: "Use recall_memory."})
		case 8: // Stage 6 turn 1: call recall_memory
			onEvent(ToolCallEvent{ToolCalls: []ToolCall{{
				Name:         "recall_memory",
				RawArguments: json.RawMessage(`{"query":"secret bridge","explanation":"fetch bridge from memory"}`),
			}}})
		case 9: // Stage 6 turn 2: verify cfg.History now contains the resurfaced memory from the replaced Session!
			observedHistoryInTurn2 = append([]PromptPart(nil), cfg.History...)
			onEvent(TextEvent{Text: "Pont Mirabeau"})
		case 10: // Stage 7: Convergence
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
