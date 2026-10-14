package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ai"
)

func TestCanonicalHistorySurvivesCompaction(t *testing.T) {
	s := &HarnessSession{}
	raw := []ai.PromptPart{
		{Role: ai.RoleUser, Text: "old question"},
		{Role: ai.RoleModel, Text: "old answer"},
		{Role: ai.RoleUser, Text: "recent question 1"},
		{Role: ai.RoleModel, Text: "recent answer 1"},
		{Role: ai.RoleUser, Text: "recent question 2"},
		{Role: ai.RoleModel, Text: "recent answer 2"},
	}
	s.appendHistory(raw...)

	s.CompactedHistory = compactHistory(s.CompactedHistory, "summary of old exchange")

	if !reflect.DeepEqual(s.CanonicalHistory, raw) {
		t.Fatalf("canonical history changed during compaction:\n got %#v\nwant %#v", s.CanonicalHistory, raw)
	}
	wantCompacted := []ai.PromptPart{
		{Role: ai.RoleUser, Text: "[Previous conversation summary]: summary of old exchange"},
		{Role: ai.RoleUser, Text: "recent question 1"},
		{Role: ai.RoleModel, Text: "recent answer 1"},
		{Role: ai.RoleUser, Text: "recent question 2"},
		{Role: ai.RoleModel, Text: "recent answer 2"},
	}
	if !reflect.DeepEqual(s.CompactedHistory, wantCompacted) {
		t.Fatalf("compacted history = %#v, want %#v", s.CompactedHistory, wantCompacted)
	}

	next := ai.PromptPart{Role: ai.RoleUser, Text: "next question"}
	s.appendHistory(next)
	if got := s.CanonicalHistory[len(s.CanonicalHistory)-1]; !reflect.DeepEqual(got, next) {
		t.Fatalf("canonical history did not append raw next turn: %#v", got)
	}
	if got := s.CompactedHistory[len(s.CompactedHistory)-1]; !reflect.DeepEqual(got, next) {
		t.Fatalf("compacted history did not append next turn: %#v", got)
	}
}

func TestCompactHistoryShortSliceUnchanged(t *testing.T) {
	short := []ai.PromptPart{
		{Role: ai.RoleUser, Text: "q1"},
		{Role: ai.RoleModel, Text: "a1"},
	}
	got := compactHistory(short, "summary")
	if !reflect.DeepEqual(got, short) {
		t.Fatalf("compactHistory modified short slice: got %#v, want %#v", got, short)
	}
}

func TestHistorySizeBeforeAndAfterCompaction(t *testing.T) {
	raw := []ai.PromptPart{
		{Role: ai.RoleUser, Text: "old question that is quite long"},
		{Role: ai.RoleModel, Text: "old answer that is also quite long"},
		{Role: ai.RoleUser, Text: "q1"},
		{Role: ai.RoleModel, Text: "a1"},
		{Role: ai.RoleUser, Text: "q2"},
		{Role: ai.RoleModel, Text: "a2"},
	}
	before := historySize(raw)
	compacted := compactHistory(raw, "short")
	after := historySize(compacted)
	if before <= after {
		t.Fatalf("expected before (%d) > after (%d)", before, after)
	}
}

func TestSplitHistoryCompactsLargeRecentToolOutputs(t *testing.T) {
	big := make([]byte, 32768)
	for i := range big {
		big[i] = 'a'
	}
	history := []ai.PromptPart{
		{Role: ai.RoleUser, Text: "explain"},
		{Role: ai.RoleModel, ToolCalls: []ai.ToolCall{{Name: "read_file"}}},
		{Role: ai.RoleTool, ToolName: "read_file", ToolResponse: map[string]any{"content": string(big)}},
	}
	older, recent := splitHistoryForCompaction(history, 4096)
	if len(older) == 0 {
		t.Fatal("expected oversized history to be moved into older for summarization")
	}
	if historySize(recent) > 8192 {
		t.Fatalf("recent size = %d, want <= 8192", historySize(recent))
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

type fakeEmbedder struct {
	calls int
	block chan struct{}
}

func (f *fakeEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	if f.block != nil {
		<-f.block
	}
	f.calls++
	return []float32{float32(f.calls), float32(len(parts[0].Text))}, nil
}

func TestBackgroundEmbedWorkerPopulatesHistoryAndYieldsToTurn(t *testing.T) {
	emb := &fakeEmbedder{}
	s := &HarnessSession{embedder: emb}
	defer s.Shutdown()

	s.appendHistory(
		ai.PromptPart{Role: ai.RoleUser, Text: "hello"},
		ai.PromptPart{Role: ai.RoleModel, Text: "world"},
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
	s2 := &HarnessSession{embedder: emb2}
	defer s2.Shutdown()

	s2.appendHistory(
		ai.PromptPart{Role: ai.RoleUser, Text: "turn 1"},
		ai.PromptPart{Role: ai.RoleModel, Text: "reply 1"},
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
	history := []ai.PromptPart{
		{Role: ai.RoleUser, Text: "read two files", Embedding: []float32{1, 0}},
		{
			Role:      ai.RoleModel,
			Text:      "reading",
			ToolCalls: []ai.ToolCall{{Name: "read_file"}, {Name: "read_file"}},
			Embedding: []float32{0, 1},
		},
		{Role: ai.RoleTool, ToolName: "read_file", ToolResponse: map[string]any{"content": "A"}, Embedding: []float32{0, 1}},
		{Role: ai.RoleTool, ToolName: "read_file", ToolResponse: map[string]any{"content": "B"}, Embedding: []float32{0, 1}},
		{Role: ai.RoleModel, Text: "done", Embedding: []float32{1, 0}},
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

	s := &HarnessSession{
		compactionLimit: 4096,
		compressor: func(ctx context.Context, sys, user string) (string, error) {
			return "compressed-gap", nil
		},
	}

	// Build 12 units:
	// U0..U1: Topic A (early requirement & schema)
	// U2..U7: Topic B (6 units of tangent exploration)
	// U8..U11: Topic A returns in the last X/2 = 4 units!
	history := []ai.PromptPart{
		{Role: ai.RoleUser, Text: "Topic A schema definition", Embedding: vecA},
		{Role: ai.RoleModel, Text: "Topic A schema ack", Embedding: vecA},
		{Role: ai.RoleUser, Text: "Topic B tangent 1", Embedding: vecB},
		{Role: ai.RoleModel, Text: "Topic B reply 1", Embedding: vecB},
		{Role: ai.RoleUser, Text: "Topic B tangent 2", Embedding: vecB},
		{Role: ai.RoleModel, Text: "Topic B reply 2", Embedding: vecB},
		{Role: ai.RoleUser, Text: "Topic B tangent 3", Embedding: vecB},
		{Role: ai.RoleModel, Text: "Topic B reply 3", Embedding: vecB},
		{Role: ai.RoleUser, Text: "Back to Topic A step 1", Embedding: vecA},
		{Role: ai.RoleModel, Text: "Working on Topic A step 1", Embedding: vecA},
		{Role: ai.RoleUser, Text: "Back to Topic A step 2", Embedding: vecA},
		{Role: ai.RoleModel, Text: "Working on Topic A step 2", Embedding: vecA},
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
	s := &HarnessSession{
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
	s := &HarnessSession{
		compactionLimit: 100, // gapTokenBudget = 50 tokens (~200 bytes)
		compressor: func(ctx context.Context, sys, user string) (string, error) {
			calls++
			return fmt.Sprintf("part%d", calls), nil
		},
	}

	units := []atomicTurnUnit{
		{parts: []ai.PromptPart{{Role: ai.RoleUser, Text: strings.Repeat("a ", 80)}}},
		{parts: []ai.PromptPart{{Role: ai.RoleModel, Text: strings.Repeat("b ", 80)}}},
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
	s := &HarnessSession{
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
			parts: []ai.PromptPart{
				{
					Role: ai.RoleModel,
					ToolCalls: []ai.ToolCall{
						{Name: "read_file", RawArguments: []byte(`{"path":"app.conf","explanation":"find server port"}`)},
					},
				},
				{
					Role:         ai.RoleTool,
					ToolName:     "read_file",
					Explanation:  "find server port",
					ToolResponse: map[string]any{"content": largeOutput},
				},
			},
		},
		{
			parts: []ai.PromptPart{
				{
					Role:         ai.RoleTool,
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
			parts: []ai.PromptPart{
				{
					Role:         ai.RoleTool,
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
