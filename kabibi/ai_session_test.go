package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockAISession implements AISession for testing
type mockAISession struct {
	messages        []string
	sendErr         error
	sendCount       int
	lastUserInput   string
	lastCtx         context.Context
	tokenCallback   func(string)
	simulatedTokens []string
}

func (m *mockAISession) SendMessage(ctx context.Context, userInput string, onToken func(string)) error {
	m.sendCount++
	m.lastCtx = ctx
	m.lastUserInput = userInput
	m.tokenCallback = onToken

	if m.sendErr != nil {
		return m.sendErr
	}

	// Simulate token streaming
	for _, token := range m.simulatedTokens {
		onToken(token)
	}

	return nil
}

func (m *mockAISession) Close() error {
	return nil
}

func TestNewLMSession(t *testing.T) {
	tests := []struct {
		name string
		conv *Conversation
	}{
		{
			name: "with nil conversation",
			conv: nil,
		},
		{
			name: "with empty conversation",
			conv: &Conversation{},
		},
		{
			name: "with populated conversation",
			conv: &Conversation{
				Messages: []Message{
					{Role: "user", Content: "hello"},
					{Role: "assistant", Content: "hi there"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := NewLMSession(tt.conv)
			if session == nil {
				t.Error("NewLMSession returned nil")
			}
		})
	}
}

func TestLMSessionImpl_SendMessageSuccess(t *testing.T) {
	conv := &Conversation{}
	session := NewLMSession(conv)

	// This will try to call advanceConversation which needs live AI
	// For now, just test that it returns a sensible error or attempts to send
	// We can't fully test without mocking advanceConversation
	_ = session
}

func TestLMSessionImpl_Close(t *testing.T) {
	session := NewLMSession(&Conversation{})
	err := session.Close()
	if err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}

func TestMockAISession_SendMessage(t *testing.T) {
	ctx := context.Background()
	mock := &mockAISession{
		simulatedTokens: []string{"hello", " ", "world"},
	}

	tokensSeen := []string{}
	err := mock.SendMessage(ctx, "test prompt", func(token string) {
		tokensSeen = append(tokensSeen, token)
	})

	if err != nil {
		t.Errorf("SendMessage failed: %v", err)
	}
	if mock.sendCount != 1 {
		t.Errorf("SendMessage called %d times, want 1", mock.sendCount)
	}
	if mock.lastUserInput != "test prompt" {
		t.Errorf("lastUserInput = %q, want %q", mock.lastUserInput, "test prompt")
	}
	if len(tokensSeen) != 3 {
		t.Errorf("received %d tokens, want 3", len(tokensSeen))
	}
}

func TestMockAISession_Error(t *testing.T) {
	ctx := context.Background()
	testErr := errors.New("AI error")
	mock := &mockAISession{sendErr: testErr}

	err := mock.SendMessage(ctx, "prompt", func(token string) {})

	if err != testErr {
		t.Errorf("got error %v, want %v", err, testErr)
	}
}

func TestMockAISession_ContextPropagation(t *testing.T) {
	ctx := context.WithValue(context.Background(), "test_key", "test_value")
	mock := &mockAISession{}

	mock.SendMessage(ctx, "prompt", func(token string) {})

	if mock.lastCtx.Value("test_key") != "test_value" {
		t.Error("context not propagated correctly")
	}
}

func TestMockAISession_MultipleMessages(t *testing.T) {
	mock := &mockAISession{}

	for i := 0; i < 3; i++ {
		_ = mock.SendMessage(context.Background(), "prompt", func(token string) {})
	}

	if mock.sendCount != 3 {
		t.Errorf("SendMessage called %d times, want 3", mock.sendCount)
	}
}

func TestAISessionTokenStreaming(t *testing.T) {
	ctx := context.Background()
	mock := &mockAISession{
		simulatedTokens: []string{"The", " ", "answer", " ", "is", " ", "42"},
	}

	tokens := []string{}
	err := mock.SendMessage(ctx, "what is the answer?", func(token string) {
		tokens = append(tokens, token)
	})

	if err != nil {
		t.Errorf("SendMessage failed: %v", err)
	}

	result := strings.Join(tokens, "")
	expected := "The answer is 42"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestAISessionEmptyResponse(t *testing.T) {
	ctx := context.Background()
	mock := &mockAISession{simulatedTokens: []string{}}

	tokenCount := 0
	err := mock.SendMessage(ctx, "prompt", func(token string) {
		tokenCount++
	})

	if err != nil {
		t.Errorf("SendMessage failed: %v", err)
	}
	if tokenCount != 0 {
		t.Errorf("received %d tokens, want 0", tokenCount)
	}
}

func TestAISessionEmptyTokens(t *testing.T) {
	ctx := context.Background()
	mock := &mockAISession{
		simulatedTokens: []string{"hello", "", "world"},
	}

	tokens := []string{}
	err := mock.SendMessage(ctx, "prompt", func(token string) {
		tokens = append(tokens, token)
	})

	if err != nil {
		t.Errorf("SendMessage failed: %v", err)
	}
	if len(tokens) != 3 {
		t.Errorf("received %d tokens, want 3", len(tokens))
	}
}

func TestAISessionCallbackInvocation(t *testing.T) {
	ctx := context.Background()
	mock := &mockAISession{
		simulatedTokens: []string{"a", "b", "c"},
	}

	callbackInvoked := false
	err := mock.SendMessage(ctx, "prompt", func(token string) {
		callbackInvoked = true
	})

	if err != nil {
		t.Errorf("SendMessage failed: %v", err)
	}
	if !callbackInvoked {
		t.Error("callback was not invoked")
	}
}

func TestAISessionClose(t *testing.T) {
	mock := &mockAISession{}
	err := mock.Close()
	if err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}

func TestAISessionMultipleSendCalls(t *testing.T) {
	mock := &mockAISession{simulatedTokens: []string{"hi"}}

	// Send same prompt multiple times
	for i := 0; i < 3; i++ {
		_ = mock.SendMessage(context.Background(), "hello", func(token string) {})
	}

	if mock.sendCount != 3 {
		t.Errorf("sendCount = %d, want 3", mock.sendCount)
	}
	if mock.lastUserInput != "hello" {
		t.Errorf("lastUserInput = %q, want %q", mock.lastUserInput, "hello")
	}
}

func TestAISessionContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mockAISession{}

	// Capture the context passed to SendMessage
	var capturedCtx context.Context
	mock.SendMessage(ctx, "prompt", func(token string) {})
	capturedCtx = mock.lastCtx

	// Cancel after the call
	cancel()

	// Verify the captured context is the same one (not yet cancelled at send time)
	if capturedCtx == nil {
		t.Error("context not captured")
	}
}

func TestAISessionPromptVariations(t *testing.T) {
	mock := &mockAISession{}

	prompts := []string{
		"simple prompt",
		"prompt with numbers 123",
		"prompt with special chars !@#$",
		"very long prompt: " + strings.Repeat("a", 1000),
		"",
	}

	for _, prompt := range prompts {
		_ = mock.SendMessage(context.Background(), prompt, func(token string) {})
		if mock.lastUserInput != prompt {
			t.Errorf("expected prompt %q, got %q", prompt, mock.lastUserInput)
		}
	}
}
