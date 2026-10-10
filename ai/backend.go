package ai

import "context"

// engineBackend abstracts the platform-specific dynamic library execution for
// a LiteRT-LM engine. All cross-boundary payloads are passed as plain Go JSON strings.
type engineBackend interface {
	NewConversation(systemJSON, toolsJSON, historyJSON string) (conversationBackend, error)
	Close() error
}

// conversationBackend abstracts the platform-specific execution of an active chat turn.
type conversationBackend interface {
	SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error
	Close() error
}
