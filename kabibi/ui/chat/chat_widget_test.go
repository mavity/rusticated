package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestChatWidgetRenderIncludesConversation(t *testing.T) {
	w := NewChatWidget()
	w.SetConversation(&Conversation{Messages: []Message{
		{Role: "user", Content: "hello there"},
		{Role: "assistant", Content: "hi there!"},
	}})
	w.Layout(Rect{X: 0, Y: 0, W: 20, H: 6})

	buf := w.Render()
	if buf.Width != 20 || buf.Height != 6 {
		t.Fatalf("unexpected render size: %#v", buf)
	}

	plain := ansi.Strip(Serialize(buf))
	if !strings.Contains(plain, "hello") || !strings.Contains(plain, "hi there") {
		t.Fatalf("render should include conversation text, got %q", plain)
	}
}

func TestChatInputWidgetMeasureAndShiftEnter(t *testing.T) {
	w := NewChatInputWidget()
	w.SetValue("first line")
	if got := w.Measure(Constraints{MaxW: 12, MaxH: 4}); got.W != 12 || got.H != 1 {
		t.Fatalf("Measure returned unexpected size: %#v", got)
	}

	w.HandleKey(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if !strings.Contains(w.Value(), "\n") {
		t.Fatal("Alt+Enter should add a newline to the input")
	}

	if got := w.Measure(Constraints{MaxW: 12, MaxH: 4}); got.H != 2 {
		t.Fatalf("Measure should expand for a multi-line input, got %#v", got)
	}
}

func TestChatWidgetSubmitAddsBothMessages(t *testing.T) {
	w := NewChatWidget()
	w.SetConversation(&Conversation{})
	w.SetService(&mockAIService{tokens: []string{"hello ", "world"}})

	cmd := w.Submit(context.Background(), "hi")
	if cmd == nil {
		t.Fatal("Submit should return a non-nil cmd when service is set")
	}
	cmd() // run the cmd to completion (mock is synchronous)

	msgs := w.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after Submit, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Fatalf("first message should be user/hi; got %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hello world" {
		t.Fatalf("second message should be assistant/hello world; got %+v", msgs[1])
	}
}

func TestChatWidgetSubmitResetsScroll(t *testing.T) {
	w := NewChatWidget()
	w.SetConversation(&Conversation{})
	w.SetService(&mockAIService{tokens: []string{"ok"}})
	w.scrollTop = 5 // simulate user having scrolled up

	cmd := w.Submit(context.Background(), "test")
	if cmd != nil {
		cmd()
	}

	if w.scrollTop != 0 {
		t.Fatalf("Submit should reset scrollTop to 0; got %d", w.scrollTop)
	}
}

func TestChatWidgetSubmitDoesNotBlockUIWhileAssetBootstrapRuns(t *testing.T) {
	oldLiteRT := ensureLiteRTFunc
	oldGemma := ensureGemmaFunc
	defer func() {
		ensureLiteRTFunc = oldLiteRT
		ensureGemmaFunc = oldGemma
	}()

	ensureLiteRTFunc = func(context.Context, chan<- assetProgressMsg) error {
		time.Sleep(250 * time.Millisecond)
		return nil
	}
	ensureGemmaFunc = func(context.Context, chan<- assetProgressMsg) error {
		time.Sleep(250 * time.Millisecond)
		return nil
	}

	w := NewChatWidget()
	w.SetConversation(&Conversation{})
	w.SetService(&mockAIService{tokens: []string{"ok"}})

	returned := make(chan tea.Cmd, 1)
	go func() {
		returned <- w.Submit(context.Background(), "test")
	}()

	select {
	case cmd := <-returned:
		if cmd == nil {
			t.Fatal("Submit should return a command even while bootstrap is still running")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Submit blocked the UI while bootstrap was still running")
	}
}

func TestChatWidgetScrollKeys(t *testing.T) {
	w := NewChatWidget()
	w.Layout(Rect{X: 0, Y: 0, W: 30, H: 10})

	w.HandleKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if w.scrollTop <= 0 {
		t.Fatal("pgup should increase scrollTop")
	}

	w.HandleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if w.scrollTop != 0 {
		t.Fatalf("end key should reset scrollTop to 0; got %d", w.scrollTop)
	}
}

func TestChatWidgetSnapshot(t *testing.T) {
	w := NewChatWidget()
	w.SetConversation(&Conversation{Messages: []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}})
	assertWidgetSnapshot(t, "chat_widget", w, Rect{X: 0, Y: 0, W: 30, H: 10})
}
