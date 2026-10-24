# Codename Pope: AI CLI Harness

This document outlines the architecture and execution plan for "Pope," a standalone AI chat CLI harness built on top of the `ai` module. Pope serves to validate the `ai` module's native capabilities—specifically tool calling—before it gets re-integrated into `kabibi`.

## 1. Harness Concept & Goals

Pope is a simple, stateless-feeling CLI that exposes the local LiteRT-LM engine.
- **Cross-Platform:** Supports both native execution and WASI ("vegetable") builds, aligning with `kabibi`'s sandboxed environment architecture.
- **Dual-Mode UX:** 
  - One-shot execution via `-p` or `--prompt` flags.
  - Interactive chat REPL when run without a prompt.
- **I/O:** Uses plain `os.Stdin` and `os.Stdout`, avoiding TUI frameworks for maximum simplicity and debuggability.

## 2. Core API: Structured Event Stream

The public API emits incremental text and structured tool-call events. LiteRT-LM JSON serialization is contained inside its backend; remote provider backends translate the same structured conversation values into provider-specific wire formats.

**Core API types:**
```go
type ToolCall struct {
    ID        string
    Name      string
    Arguments map[string]any
}

type ConversationConfig struct {
    SystemMessage string
    Tools         []map[string]any
    History       []PromptPart
}
```

`PromptPart` carries `ToolCallID` on tool responses so backends preserve the originating call/response association. `ModelEvent` is implemented by `TextEvent` and `ToolCallEvent`; failures are reported by the stream iterator.

**Public API:**
```go
func LoadModelInfo(libPath, modelPath string) (*ModelInfo, error)
func (e *Engine) NewConversationWithConfig(cfg ConversationConfig) (*Conversation, error)
func (c *Conversation) SendStream(ctx context.Context, parts []PromptPart) iter.Seq2[ModelEvent, error]
```

*Note on stream concurrency:* Native LiteRT-LM callbacks run on the runtime's callback thread, and the iterator body is invoked directly from that callback in generation order. A slow iterator body delays later chunks. Breaking iteration or cancelling the context cancels native generation and invalidates the conversation; the iterator returns without waiting for native unwinding. Callers must replace, not reuse, a cancelled conversation. Stream errors are yielded once as `(nil, err)`.

*Note on JSON Schemas:* LiteRT-LM uses the OpenAI-style schema for tool declarations (an array of `{"type": "function", "function": {...}}` objects). When the model emits a tool call, the chunk arrives as a JSON object containing a `tool_calls` array.

## 3. Tool Execution & Harness Loop

Before starting the loop, Pope will use `LoadModelInfo` to introspect the model. It will use `SupportedBackends` to filter the user's requested `--backend` flag (falling back safely if unsupported) and check `SupportsFunctionCalling` to gracefully disable tools if the model doesn't support them natively.

Pope will range over the `SendStream` iterator and act as the executor for tool requests.

1. **Text Events:** Printed directly to `os.Stdout`.
2. **Tool Call Events:** Pope pauses UI output, executes the requested Go function, and feeds the result back to the model.
3. **Registry:** Initial un-gated tools available to the model:
   - File Access: `read_file(path)`, `write_file(path, content)`
   - System Execution: `exec(command)` (Using `os/exec`, which intercepts to the host when running in WASI mode).
4. **Feedback:** After execution, Pope constructs a `PromptPart` with `RoleTool` containing the JSON result, and triggers a new `SendStream` so the model can read the output and resume. The `ai` module must format this response using the expected LiteRT-LM tool response schema: `{"role": "tool", "content": [{"type": "tool_response", "name": "<tool_name>", "response": <result>}]}`.

## 4. Context Window Management (Compaction)

`ai.Session` owns logical turn history, compaction, and conversation replacement because LiteRT-LM conversation state is opaque and does not support deleting old turns in place. Callers provide a `CompactionLimit` in `SessionConfig`; `LazyAIRunner` derives a limit from model metadata where available. The Session projects compacted history into a replacement conversation seeded through the backend interface. Pope and other clients should use `ai.Session` rather than implementing a second history-compaction loop.

# Platform Abstraction

The shared engine and conversation orchestration is separated from platform-specific LiteRT-LM calls by a small Go backend interface.

## 1 Low-Level API Abstraction Shape

The interface exchanges typed conversation values and events; FFI pointers and handles remain inside platform implementations.

```go
type engineBackend interface {
    NewConversation(cfg ConversationConfig) (conversationBackend, error)
    Close() error
}

type conversationBackend interface {
    SendStream(ctx context.Context, parts []PromptPart, onEvent func(ModelEvent)) error
    Clone() (conversationBackend, error)
    Close() error
}
```

### Architectural Benefits:
1. **Typed shared orchestration:** Conversation history, tool-call IDs, and model events stay structured until the selected backend serializes provider-specific data.
2. **Opaque platform handles:** Low-level FFI pointers and handles do not cross the public conversation API.

## 2 Target Implementations

The `ai` module will provide two target implementations of this abstraction:

1. **Native (`backend_native.go`, `//go:build !wasip1`):**
   - Implements `engineBackend` and `conversationBackend` using `purego` and system `dlopen`.
   - String inputs are converted to NUL-terminated C pointers without copying where possible.
   - Pointers are passed directly in the shared 64-bit virtual address space.

2. **Rusticated Desktop / Mohabbat (`backend_rusticated.go`, `//go:build wasip1`):**
   - Implements `engineBackend` and `conversationBackend` using `syscall.Dylib*` machinery.
   - Allocations and IPC serialization to the `washmhost` satellite process are encapsulated entirely within this driver.
   - 64-bit host handles are stored safely inside the private struct without exposing them to the rest of the application.

## 3 Browser Backend: LiteRT-LM on Web (HTML/JS/WASM)

Investigation of the official `LiteRT-LM/js` package (`litertlm_web.cc` and `@litert-lm/core`) reveals that the Web runtime follows an almost identical architectural shape:
- Pre-conversation settings (`createCustom`) accept `preface_json` with `messages` and `tools`.
- Generation (`sendMessageAsync`) consumes a message JSON string and streams back JSON token chunk strings containing deltas or `tool_calls`.

The browser backend is implemented in `backend_web_rusticated.go` and uses the same structured conversation API:

3. **Web (`backend_web_rusticated.go`, `//go:build wasip1`):**
   - Implements `engineBackend` and `conversationBackend` on top of `@litert-lm/core` running in the browser / WebGPU environment.
   - The Rusticated `/dev/eval` bridge invokes the browser's `@litert-lm/core` WebGPU runtime; remote provider requests use an `EvalJS` fetch/SSE transport.
   - LiteRT-LM's JavaScript API uses JSON internally, but those wire details stay in the browser backend rather than the shared conversation API.
