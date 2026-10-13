package ai

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConvBackend struct {
	chunks   []string
	err      error
	called   atomic.Bool
	finished atomic.Bool
}

func (f *fakeConvBackend) SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error {
	f.called.Store(true)
	for _, c := range f.chunks {
		onChunk(c)
	}
	f.finished.Store(true)
	return f.err
}

func (f *fakeConvBackend) Close() error { return nil }

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

// Breaking out early must not leave the native side blocked on an unread
// channel, and must not return before the generation has finished.
func TestSendStreamEarlyBreakDrainsUntilGenerationFinishes(t *testing.T) {
	f := &fakeConvBackend{}
	for i := 0; i < 100; i++ { // well beyond the internal channel buffer
		f.chunks = append(f.chunks, textChunk("x"))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range newFakeConv(f).SendStream(context.Background(), []PromptPart{{Text: "hi"}}) {
			break
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("early break deadlocked: native callbacks blocked on an unread channel")
	}
	if !f.finished.Load() {
		t.Fatal("iterator returned while the backend was still generating")
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
