package ai

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
)

type fakeConvBackend struct {
	chunks    []string
	err       error
	called    atomic.Bool
	finished  atomic.Bool
	closed    atomic.Bool
	ctxErrEnd atomic.Value // error observed on ctx once all chunks were pushed
}

func (f *fakeConvBackend) SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error {
	f.called.Store(true)
	for _, c := range f.chunks {
		onChunk(c)
	}
	f.ctxErrEnd.Store(struct{ error }{ctx.Err()})
	f.finished.Store(true)
	return f.err
}

func (f *fakeConvBackend) Close() error {
	f.closed.Store(true)
	return nil
}

func newFakeConv(f *fakeConvBackend) *Conversation {
	return &Conversation{impl: &convImpl{backend: f}}
}

func textChunk(s string) string {
	return `{"role":"assistant","content":[{"type":"text","text":"` + s + `"}]}`
}

func TestSendStreamYieldsEventsInOrder(t *testing.T) {
	f := &fakeConvBackend{chunks: []string{
		textChunk("a"),
		textChunk("b"),
		`{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"exec","arguments":{"command":"ls"}}}]}`,
	}}

	var got []ModelEvent
	for event, err := range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, event)
	}

	want := []ModelEvent{
		TextEvent{Text: "a"},
		TextEvent{Text: "b"},
		ToolCallEvent{ToolCalls: []ToolCall{{Name: "exec", Arguments: map[string]any{"command": "ls"}, RawArguments: json.RawMessage(`{"command":"ls"}`)}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestSendStreamYieldsBackendErrorOnceAfterEvents(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeConvBackend{chunks: []string{textChunk("a")}, err: boom}

	var events []ModelEvent
	var errs []error
	for event, err := range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		if err != nil {
			if event != nil {
				t.Fatalf("error item carried an event: %v", event)
			}
			errs = append(errs, err)
			continue
		}
		events = append(events, event)
	}

	if !reflect.DeepEqual(events, []ModelEvent{TextEvent{Text: "a"}}) {
		t.Fatalf("events = %#v", events)
	}
	if len(errs) != 1 || !errors.Is(errs[0], boom) {
		t.Fatalf("errs = %v, want exactly [boom]", errs)
	}
}

func TestSendStreamYieldsPayloadErrorWithoutCallingBackend(t *testing.T) {
	f := &fakeConvBackend{}
	parts := []PromptPart{{Role: RoleTool, ToolName: "x", ToolResponse: make(chan int)}}

	var errs []error
	for _, err := range newFakeConv(f).SendStream(context.Background(), parts) {
		errs = append(errs, err)
	}

	if len(errs) != 1 || errs[0] == nil {
		t.Fatalf("errs = %v, want exactly one error", errs)
	}
	if f.called.Load() {
		t.Fatal("backend was called despite an unencodable payload")
	}
}

// Breaking out cancels the native generation (through ctx) and invalidates the
// conversation. Chunks the backend still pushes afterwards must not reach the
// loop body, which would otherwise be called after the loop has exited.
func TestSendStreamEarlyBreakCancelsAndInvalidates(t *testing.T) {
	f := &fakeConvBackend{}
	for i := 0; i < 100; i++ {
		f.chunks = append(f.chunks, textChunk("x"))
	}

	n := 0
	for range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		n++
		break
	}

	if n != 1 {
		t.Fatalf("loop body ran %d times after break, want 1", n)
	}
	if got, _ := f.ctxErrEnd.Load().(struct{ error }); !errors.Is(got.error, context.Canceled) {
		t.Fatalf("backend ctx error = %v, want context.Canceled", got.error)
	}
	if !f.closed.Load() {
		t.Fatal("conversation was not invalidated by the break")
	}
}

func TestSendStreamOnClosedConversationYieldsErrClosed(t *testing.T) {
	for name, conv := range map[string]*Conversation{
		"nil":        nil,
		"no impl":    {},
		"no backend": {impl: &convImpl{}},
	} {
		var errs []error
		for event, err := range conv.SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
			if event != nil {
				t.Fatalf("%s: unexpected event %v", name, event)
			}
			errs = append(errs, err)
		}
		if len(errs) != 1 || !errors.Is(errs[0], ErrClosed) {
			t.Fatalf("%s: errs = %v, want exactly [ErrClosed]", name, errs)
		}
	}
}

// A conversation closed while a stream is running reports ErrClosed from the
// backend; the loop sees it once, after the events that preceded it.
func TestSendStreamYieldsErrClosedMidStream(t *testing.T) {
	f := &fakeConvBackend{chunks: []string{textChunk("a")}, err: ErrClosed}

	var events []ModelEvent
	var errs []error
	for event, err := range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		events = append(events, event)
	}

	if len(events) != 1 || len(errs) != 1 || !errors.Is(errs[0], ErrClosed) {
		t.Fatalf("events = %v, errs = %v, want one event then [ErrClosed]", events, errs)
	}
}

func TestParseChunkEvents(t *testing.T) {
	got := parseChunkEvents(`{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],` +
		`"tool_calls":[{"function":{"name":"read_file","arguments":"{\"path\":\"p\"}"}}]}`)
	want := []ModelEvent{
		TextEvent{Text: "a"},
		TextEvent{Text: "b"},
		ToolCallEvent{ToolCalls: []ToolCall{{Name: "read_file", Arguments: map[string]any{"path": "p"}, RawArguments: json.RawMessage(`{"path":"p"}`)}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}

	if got := parseChunkEvents("plain"); !reflect.DeepEqual(got, []ModelEvent{TextEvent{Text: "plain"}}) {
		t.Fatalf("non-JSON chunk: got %#v", got)
	}
	if got := parseChunkEvents(""); got != nil {
		t.Fatalf("empty chunk: got %#v, want nil", got)
	}
}

func TestSendStreamDetectsExactTandemLoopAndCancels(t *testing.T) {
	f := &fakeConvBackend{}
	for i := 0; i < 60; i++ {
		f.chunks = append(f.chunks, textChunk(" *the*"))
	}

	var events int
	var errs []error
	for event, err := range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if event != nil {
			events++
		}
	}

	if len(errs) != 1 || !errors.Is(errs[0], ErrRepetitionLoop) {
		t.Fatalf("errs = %v, want [ErrRepetitionLoop]", errs)
	}
	if events >= 30 {
		t.Fatalf("yielded %d events before catching loop, want early stop (< 30)", events)
	}
	if got, _ := f.ctxErrEnd.Load().(struct{ error }); !errors.Is(got.error, context.Canceled) {
		t.Fatalf("backend ctx error = %v, want context.Canceled", got.error)
	}
	if !f.closed.Load() {
		t.Fatal("conversation was not closed after loop detection")
	}
}

func TestSendStreamDetectsFuzzyInterleavedLoop(t *testing.T) {
	f := &fakeConvBackend{}
	patterns := []string{
		"We must honor the gravity of this feeling. ",
		"Tell me what this feeling looks like now. ",
		"We must honor the weight of this feeling. ",
	}
	for i := 0; i < 40; i++ {
		f.chunks = append(f.chunks, textChunk(patterns[i%len(patterns)]))
	}

	var errs []error
	for _, err := range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) != 1 || !errors.Is(errs[0], ErrRepetitionLoop) {
		t.Fatalf("errs = %v, want [ErrRepetitionLoop]", errs)
	}
	if !f.closed.Load() {
		t.Fatal("conversation was not closed after fuzzy loop detection")
	}
}

func TestSendStreamDetectsRepeatedToolCalls(t *testing.T) {
	callChunk := `{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"read_file","arguments":{"path":"main.go"}}}]}`
	f := &fakeConvBackend{
		chunks: []string{callChunk, callChunk, callChunk, callChunk},
	}

	var yielded int
	var errs []error
	for event, err := range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if event != nil {
			yielded++
		}
	}

	if yielded != 2 {
		t.Fatalf("yielded = %d, want 2 before 3rd identical tool call trips loop", yielded)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrRepetitionLoop) {
		t.Fatalf("errs = %v, want [ErrRepetitionLoop]", errs)
	}
	if !f.closed.Load() {
		t.Fatal("conversation was not closed after repeated tool call loop")
	}
}

func TestSendStreamAllowsNormalProseAndTables(t *testing.T) {
	prose := []string{
		textChunk("Paris has many famous bridges crossing the Seine River.\n"),
		textChunk("| Bridge | Opened | Notes |\n"),
		textChunk("| --- | --- | --- |\n"),
		textChunk("| Pont Neuf | 1607 | Oldest standing bridge across the Seine in Paris. |\n"),
		textChunk("| Pont Alexandre III | 1900 | Beaux-Arts style deck arch bridge with gilded statues. |\n"),
		textChunk("| Pont des Arts | 1984 | Pedestrian bridge connecting the Institut de France and the Louvre. |\n"),
		textChunk("| Pont de Bir-Hakeim | 1905 | Two-level steel viaduct carrying Metro Line 6 and road traffic. |\n"),
		textChunk("| Pont Mirabeau | 1896 | Metal arch bridge celebrated in Guillaume Apollinaire's poem. |\n"),
	}
	f := &fakeConvBackend{chunks: prose}

	var count int
	for _, err := range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
		if err != nil {
			t.Fatalf("unexpected error on normal prose/table: %v", err)
		}
		count++
	}
	if count != len(prose) {
		t.Fatalf("yielded %d chunks, want %d", count, len(prose))
	}
}
