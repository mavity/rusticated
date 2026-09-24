package main

// Message represents a single conversation message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Conversation holds the state of an AI conversation session.
// Engine and conv are opaque handles to the LiteRT backend.
type Conversation struct {
	engine   uint64
	conv     uint64
	Messages []Message
}

// ScrollbackWriter is a callback function for writing lines to the scrollback/plume
type ScrollbackWriter func(text string)

// SubmitHandler is called when the user submits a shell command
type SubmitHandler func(command string)

// ShellOptions configures shell behavior and callbacks
type ShellOptions struct {
	WriteToScrollback ScrollbackWriter // Callback to write output to scrollback
	SubmitCommand     SubmitHandler    // Callback when command is submitted
}

// PromptOptions configures AI prompt behavior and callbacks
type PromptOptions struct {
	WriteToScrollback ScrollbackWriter
}
