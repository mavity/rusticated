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
