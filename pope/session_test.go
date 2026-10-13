package main

import (
	"reflect"
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
