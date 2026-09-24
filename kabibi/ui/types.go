package ui

// ScrollbackWriter is the standardized callback type for writing managed plume lines
// to terminal scrollback when overflow occurs. All packages that manage dynamic line
// buffers (Shell, PlumeWidget) use this single contract.
type ScrollbackWriter func(text string)

// SubmitHandler is invoked when a user submits input via Enter in a text prompt.
// The handler receives the captured command/input string and is responsible for
// dispatching it to the appropriate subsystem (shell execution, chat submission, etc).
type SubmitHandler func(command string)

// PromptOptions configures an interactive text input prompt's behavior and callbacks.
type PromptOptions struct {
	PromptLabel string        // Label/prefix displayed before the input field (e.g., "$ ")
	OnSubmit    SubmitHandler // Callback fired when user presses Enter
}

// ShellOptions wires the Shell subsystem with callbacks for scrollback archival
// and command submission, establishing the boundary contract between Shell and Host.
type ShellOptions struct {
	WriteToScrollback ScrollbackWriter     // Callback for ejecting overflow plume lines to terminal scrollback
	OnCommandSubmit   func(command string) // Callback for capturing user-submitted shell commands
}
