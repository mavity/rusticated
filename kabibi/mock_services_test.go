package main

import "context"

type mockFSService struct {
	entries []FSEntry
	err     error
}

func (m *mockFSService) ReadDir(_ string) ([]FSEntry, error) {
	return m.entries, m.err
}

type mockAIService struct {
	tokens []string
	err    error
}

func (m *mockAIService) SendMessage(_ context.Context, _ string, onToken func(string)) error {
	for _, tok := range m.tokens {
		onToken(tok)
	}
	return m.err
}
