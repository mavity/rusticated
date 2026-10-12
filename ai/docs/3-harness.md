# Codename Pope: AI CLI Harness

This document outlines the architecture and execution plan for "Pope," a standalone AI chat CLI harness built on top of the `ai` module. Pope serves to validate the `ai` module's native capabilities—specifically tool calling—before it gets re-integrated into `kabibi`.

## 1. Harness Concept & Goals

Pope is a simple, stateless-feeling CLI that exposes the local LiteRT-LM engine.
- **Cross-Platform:** Supports both native execution and WASI ("vegetable") builds, aligning with `kabibi`'s sandboxed environment architecture.
- **Dual-Mode UX:** 
  - One-shot execution via `-p` or `--prompt` flags.
  - Interactive chat REPL when run without a prompt.
- **I/O:** Uses plain `os.Stdin` and `os.Stdout`, avoiding TUI frameworks for maximum simplicity and debuggability.

## 2. Core API Redesign: The Event Stream

Through investigation of the LiteRT-LM C++ implementation, we established that the C API streams JSON snapshots of the `Message` state during generation. Text streams as deltas, while tool calls are internally buffered by the engine and emitted as fully-formed JSON objects. 

To handle this elegantly, the `ai.go` API will move away from a basic text callback to an idiomatic, heterogeneous Go channel stream.

**New API Types:**
```go
// ModelEvent represents a valid item emitted during model generation.
// Failures are not events: SendStream reports them as the iterator's error.
type ModelEvent interface {
    isModelEvent()
}

type TextEvent struct { Text string }
type ToolCallEvent struct { ToolCalls []ToolCall }

type ToolCall struct {
    Name      string
    Arguments map[string]any
}

// ConversationConfig passes state to the engine BEFORE creating a conversation.
type ConversationConfig struct {
    SystemMessage string
    Tools         []map[string]any
    History       []PromptPart
}

// ModelInfo provides introspection into a LiteRT-LM file's capabilities.
type ModelInfo struct {
    MaxContextTokens       int
    IsDynamicContext       bool
    SupportsThinking       bool
    SupportsFunctionCalling bool
    SupportedBackends      []string // e.g., ["npu", "gpu", "cpu"]
}
```

**New Interface:**
```go
func LoadModelInfo(libPath, modelPath string) (*ModelInfo, error)
func (e *Engine) NewConversationWithConfig(cfg ConversationConfig) (*Conversation, error)
func (c *Conversation) SendStream(ctx context.Context, parts []PromptPart) iter.Seq2[ModelEvent, error]
```

**Required Binding Updates:**
*CRUCIAL ABI WARNING:* The LiteRT-LM 0.18.0 wheel uses the v1.0.0 C-API signatures for all `litert_lm_loaded_file_*` functions. They return an `int32` status code and write their actual result into an output pointer (e.g., `func(file uintptr, out *bool) int32`). Binding these to directly return a boolean/integer (the older ABI style) will corrupt memory or crash the process.

The `purego` implementations (`engine_native.go` and `engine_wasip1.go`) will be updated to expose the following C-API configuration functions, enabling the engine to output tool JSON natively:
- `litert_lm_conversation_config_set_tools`
- `litert_lm_conversation_config_set_system_message`
- `litert_lm_conversation_config_set_stream_tool_calls` (must be enabled for the engine to stream tool calls)
- `litert_lm_conversation_config_set_messages` (required for seeding compacted history)
- `litert_lm_loaded_file_*` family of functions to populate `ModelInfo` without fully loading the engine.

*Note on Stream Concurrency:* The native `litert_lm_conversation_send_message_stream` returns as soon as the stream has started; chunks then arrive on a native background thread, and the terminal chunk (`is_final`, which also carries the error if generation failed) completes the request. The backend therefore blocks until that terminal chunk arrives. `SendStream` runs the backend call in a goroutine and hands events to the caller's goroutine, so the loop body never runs on the native callback thread. A failure (including a non-zero start status) is yielded once as `(nil, err)` and ends the iteration. The iterator does not return until generation has finished, even if the caller breaks out of the loop early: the remaining events are discarded, so the conversation is idle and safe to reuse or close as soon as the loop exits. Context cancellation is not honored mid-generation; the C API's `litert_lm_conversation_cancel_process` is not bound yet.

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

To handle the limitations of local model context windows, Pope will implement a basic on-demand compaction strategy. It will dynamically set the context limit by querying `ModelInfo.MaxContextTokens` during initialization rather than hardcoding a limit (e.g., 8k for Gemma).

*Crucial API Detail:* The LiteRT-LM C API manages conversation state opaquely and does not allow splicing or deleting historical turns (there is no `litert_lm_conversation_get_history` or equivalent). Therefore, Pope must track the logical turn history in Go.

When the conversation history exceeds a token threshold:
1. Keep the System prompt and the most recent `N` turns intact.
2. Extract the older turns and feed them into a background/offline prompt to generate a concise summary.
3. Destroy the active C conversation and create a new one using `litert_lm_conversation_config_set_messages` to seed it with a single "Summary of previous conversation" message followed by the recent `N` turns.

# Platform Abstraction

Currently, significant high-level orchestration logic (JSON encoding/decoding, turn history tracking, event stream fan-out, and speculative decoding retries) is duplicated between `engine_native.go` and `engine_wasip1.go`. To resolve this, the architecture will separate platform-independent engine logic from low-level dynamic library execution through a thin, idiomatic Go interface.

## 1 Low-Level API Abstraction Shape

Rather than attempting to virtualize generic memory pointers or create a synthetic FFI layer (which introduces awkward memory ownership and 32-bit vs. 64-bit `uintptr` truncation hazards across WASM and native), the abstraction operates at the boundary of LiteRT-LM domain operations.

All inputs across the boundary are passed as plain Go strings (since LiteRT-LM expects JSON across C, Web, and Python APIs), and platform handles are kept opaque inside interface implementations:

```go
type engineBackend interface {
    NewConversation(systemJSON, toolsJSON, historyJSON string) (conversationBackend, error)
    Close() error
}

type conversationBackend interface {
    SendStream(ctx context.Context, messageJSON string, onChunk func(string)) error
    Close() error
}
```

Constructors and static inspection functions:
```go
// Implemented separately per target platform
func openBackend(libPath, modelPath, backend string) (engineBackend, error)
func inspectModel(libPath, modelPath string) (*ModelInfo, error)
```

### Architectural Benefits:
1. **Single `engine.go`:** All high-level business logic (backend candidate resolution, `PromptPart` JSON serialization, `ModelEvent` channel dispatching, and compaction loops) collapses into a single, unified `engine.go` without build tags.
2. **Type Safety & Portability:** No `uintptr` handles exist in the unified layer, avoiding 32-bit pointer truncation bugs under `GOARCH=wasm` where host handles are 64-bit integers.

## 2 Target Implementations

The `ai` module will provide two target implementations of this abstraction:

1. **Native (`backend_native.go`, `//go:build !wasip1 && !js`):**
   - Implements `engineBackend` and `conversationBackend` using `purego` and system `dlopen`.
   - String inputs are converted to NUL-terminated C pointers without copying where possible.
   - Pointers are passed directly in the shared 64-bit virtual address space.

2. **Rusticated Desktop / Mohabbat (`backend_wasip1.go`, `//go:build wasip1 && !js`):**
   - Implements `engineBackend` and `conversationBackend` using `syscall.Dylib*` machinery.
   - Allocations and IPC serialization to the `washmhost` satellite process are encapsulated entirely within this driver.
   - 64-bit host handles are stored safely inside the private struct without exposing them to the rest of the application.

## 3 Future Work: LiteRT-LM on Web (HTML/JS/WASM)

Investigation of the official `LiteRT-LM/js` package (`litertlm_web.cc` and `@litert-lm/core`) reveals that the Web runtime follows an almost identical architectural shape:
- Pre-conversation settings (`createCustom`) accept `preface_json` with `messages` and `tools`.
- Generation (`sendMessageAsync`) consumes a message JSON string and streams back JSON token chunk strings containing deltas or `tool_calls`.

Because the higher-level `engine.go` already operates entirely on JSON strings and `ModelEvent` channels, support for the browser will be added via a third implementation:

3. **Web (`backend_web.go`, `//go:build js || wasm`):**
   - Implements `engineBackend` and `conversationBackend` on top of `@litert-lm/core` running in the browser / WebGPU environment.
   - **Rusticated ABI Extension:** This implementation will rely on a planned rusticated ABI extension (e.g. `eval_platform(...)` or host import hooks) that allows the WASM guest to evaluate JavaScript expressions and pass strings directly to the host JS runtime.
   - Because the JS API already works with JSON strings, this implementation requires **zero pointer conversions and zero manual memory allocation**, allowing the identical `engine.go` and `pope` harness to run seamlessly in the browser.

