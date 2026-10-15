# Architecture Plan: Harmonizing Deliberation as an Agentic Tool (`deliberate`)

## 1. Investigation Findings: What the Junior Developer Got Right & Wrong

### What Is Actually True in `litert_lm_ext.dll`
1. **`litert_lm_conversation_clone` DOES exist in the C binary:**
   - Disassembly of `litert_lm_ext.dll` (`0x180004d60 <litert_lm_conversation_clone>`) confirms its exact C ABI signature:
     ```c
     LiteRtLmConversation* litert_lm_conversation_clone(LiteRtLmConversation* conv);
     ```
   - Under the hood (`third_party/odml/litert_lm/c/conversation.cc` $\rightarrow$ `runtime/conversation/conversation.cc`), it clones the C++ `Conversation` state and its underlying `Session` KV cache (`ShallowClone`), returning a new `LiteRtLmConversation*` handle (or `NULL` if cloning is unsupported by a specific backend).

### What the Junior Developer Got Wrong (Critical Traps to Avoid)
1. **Trap 1 — Tools Cannot Be Mutated on a Cloned `LiteRtLmConversation*`:**
   - The junior's writeup claimed: *"On the cloned child handle, tools are explicitly stripped (`Tools: nil`)."*
   - In LiteRT-LM's C API, tools are bound on `LiteRtLmConversationConfig` via `litert_lm_conversation_config_set_tools` **before** `litert_lm_conversation_create`. There is **no** `litert_lm_conversation_set_tools(conv, ...)` function in the DLL. A cloned conversation inherits the parent's `JsonPreface` (including its declared tools).
2. **Trap 2 — Mid-Tool-Call State in the Parent Conversation:**
   - When the primary model emits a `ToolCallEvent` (`deliberate(goal: "...")`), the parent `Conversation`'s last turn in its C++ history is an unresolved `RoleModel` `tool_call` waiting for a `RoleTool` `tool_response`!
   - If you clone the parent conversation *after* it emitted `tool_call(deliberate)` and send a `RoleUser` contemplation prompt (`"What are the concrete steps..."`) into the clone instead of a `RoleTool` response, the Jinja chat template / C++ state machine either violates turn alternation or still has tools active and tries to emit another tool call mid-thought.

---

## 2. How to Satisfy the Split Cleanly & Efficiently

To get **both** zero-cost KV cloning (`litert_lm_conversation_clone`) **and** clean tool-free (`Tools: nil`) contemplation forks without corrupting turn alternation:

### A. Expose `Conversation.Clone()` at the FFI & `ai` Package Layer
1. **`LMDriver` (`ai/backend.go`, `ai/backend_native.go`, `ai/backend_rusticated.go`):**
   - Bind `litert_lm_conversation_clone` in both `purego` (native) and `syscall.DylibSym` (`wasip1` rusticated):
     ```go
     convClone(conv uint64) uint64
     ```
2. **`conversationBackend` & `ai.Conversation` (`ai/backend.go`, `ai/engine.go`, `ai/conversation.go`):**
   - Add `Clone() (conversationBackend, error)` on `drvConversationBackend` and `Clone() (*Conversation, error)` on `ai.Conversation`.
3. **Pre-Turn Baseline Clone (`baseConv`):**
   - Right before sending the user's turn on the main conversation (or maintaining a clean `Tools: nil` shadow conversation / pre-tool checkpoint), we can clone the conversation state (`Clone()`), or when `deliberate` is called:
   - Provide `ToolContext.CloneConversation()` for direct native handle cloning **and** `ToolContext.NewIsolatedConversation(cfg)` (or automatic fallback if a backend returns `NULL` on clone or when `Tools: nil` isolation is required).
   - Even better: inside `deliberate`, if we use a cloned conversation, we can either:
     - Clone a `Tools: nil` base conversation that tracks the committed history prefix (so every contemplation fork — Expectation, Steps, 3x Critique, Plan — clones from the pre-filled `Tools: nil` prefix in $O(1)$ without re-prefilling the conversation history!), **or**
     - Fall back seamlessly to `Engine.NewConversationWithConfig` when `Engine` is nil/mocked in tests or if native clone is unavailable.

Let's trace that!
If `Session` maintains or creates a `Tools: nil` conversation seeded with `baseHistory + User(P)` at the start of `deliberate(goal)`:
1. We create **one** `Tools: nil` root contemplation conversation `rootDelibConv` with `baseHistory + User(P)`.
2. For **Stage 1 (Expectation)**, we `Clone()` `rootDelibConv` $\rightarrow$ zero re-prefill of `baseHistory + User(P)`!
3. We feed `Model("I need to think first, the goal here is: " + goal)` + `User("What are the concrete steps...")` into `rootDelibConv`.
4. Now `rootDelibConv` has `baseHistory + User(P) + Model(goal) + User(steps_prompt) + Model(steps)` already prefilled in its KV cache with `Tools: nil`!
5. For **Stages 3A (Relevancy), 3B (Safety), and 3C (Groundedness)**, all three critique branches share the exact same prefix (`baseHistory + User(P) + Model(goal)`). By cloning `rootDelibConv` right after `Model(goal)` via `litert_lm_conversation_clone`, **all 3 critique branches and the steps branch share the prefilled KV cache with zero re-prefill tax and guaranteed `Tools: nil`!**

That combines the best of both worlds:
- **True `litert_lm_conversation_clone` KV-cache reuse** across the branched contemplation tree (Expectation, Steps, Relevancy, Safety, Groundedness, Plan).
- **Strict `Tools: nil` isolation** so the 2B model never emits recursive tool calls during internal contemplation.

---

## 3. Public API Design in `package ai`

### 1. `ToolContext` & Updated `Tool` Signature (`ai/session.go`)

```go
// ToolContext provides explicit runtime and session capabilities to tool handlers.
type ToolContext struct {
	Session      *Session
	Engine       *Engine
	Conversation *Conversation // Active conversation handle (when available)
	Feedback     LogFeedback   // Impermanent status/ticker stream
}

// CloneConversation creates a lightweight branched conversation handle using
// LiteRT-LM's native litert_lm_conversation_clone C API.
func (tc *ToolContext) CloneConversation() (*Conversation, error)
```

To keep existing tools ergonomic while exposing `ToolContext` cleanly:
- Add `ExecuteWithContext func(ctx context.Context, tc *ToolContext, call ToolCall) (any, error)` on `ai.Tool` (or update `Tool` so if `ExecuteWithContext` is set it is called with `ToolContext`, and if `Execute` is set it continues to work seamlessly).
- Even cleaner: provide `NewDeliberateTool()` as a first-class built-in capability in `package ai` (automatically registered on `ai.Session` unless disabled, or registered via `ai.DeliberateTool()`).

### 2. The `deliberate` Tool Contract

* **Tool Name:** `"deliberate"`
* **Description:** `"Deliberate on a complex task before acting: calibrates expected response detail, breaks down initial steps, critiques relevancy/safety/groundedness, and synthesizes a concrete execution plan. Call this tool FIRST whenever a request requires multi-step problem solving, codebase inspection, or tool orchestration."`
* **Parameters:**
  - `goal` (`string`, required): `"One-sentence description of the underlying goal and task required to address the user's request."`
  - `explanation` (`string`, required — auto-injected): `"Brief explanation of why deliberation is needed."`
* **Execution Flow Inside `deliberate`:**
  1. Initializes the `liveStreamer` on `tc.Feedback`.
  2. Streams `[goal] <goal>` onto the ticker.
  3. Creates a `Tools: nil` contemplation conversation on `tc.Engine` seeded with `baseHistory + User(userText)`, and uses `Conversation.Clone()` (`litert_lm_conversation_clone`) to fork each branch without re-prefilling `baseHistory`:
     - `[expectation]` $\rightarrow$ derives `ExpectedDetail` (`<detail_level>`).
     - Advances the base contemplation conversation with `Model("I need to think first, the goal here is probably: " + effectiveGoal)` and clones it for:
     - `[steps]` $\rightarrow$ derives `InitialSteps` (`<steps>`).
     - `[relevancy]` $\rightarrow$ critiques relevancy of `<steps>`.
     - `[safety]` $\rightarrow$ critiques safety/edge cases of `<steps>`.
     - `[grounded]` $\rightarrow$ critiques unverified assumptions in `<steps>`.
     - `[plan]` $\rightarrow$ tribunal synthesis producing `SynthesizedPlan` (`<plan>`).
  4. Wipes the ticker (`streamer.Wipe()`) and records the populated `*CognitiveTrace` on the active turn so it attaches to the final `RoleModel` turn in `CanonicalHistory` (and triggers background embedding of `Goal + SynthesizedPlan`).
  5. Returns a structured tool response to the primary model:
     ```json
     {
       "goal": "...",
       "expected_detail": "...",
       "plan": "...",
       "directive": "Proceed with this plan now using any necessary tools, then answer the user's request directly while strictly obeying expected_detail (no prefaces, no mention of internal phases)."
     }
     ```

### 3. Harmonized `Session.RunTurn` Flow

Instead of hardcoding a mandatory 7-stage pipeline on every turn before the model can even see its tools:

1. **Primary Agentic Loop (`RunTurn`):**
   - `Session` runs a standard tool-enabled conversation (`s.tools`, which includes `deliberate` alongside the application's tools like `exec`, `read_file`, `shell_history`, etc.).
   - **Fast Path (No `deliberate` called):**
     - If the user asks `"Name a bridge in Paris"`, the model simply answers directly (e.g. `"Pont Alexandre III."`), streaming tokens via `OnContent` immediately in a **single pass** with zero extra stages!
     - `CognitiveTrace{IsDirect: true}` is attached to the committed `RoleModel` turn.
   - **Deliberative Path (Model calls `deliberate(goal: "...")` — or Stage 1 `GOAL:` trigger):**
     - When the model invokes the `deliberate` tool (or if `deliberate` is invoked during tool dispatch), the `deliberate` tool executes the multi-stage contemplation tree using cloned `Tools: nil` conversations, streams `[goal]` $\rightarrow$ `[expectation]` $\rightarrow$ `[steps]` $\rightarrow$ `[relevancy]` $\rightarrow$ `[safety]` $\rightarrow$ `[grounded]` $\rightarrow$ `[plan]` on `OnFeedback`, populates `CognitiveTrace`, and returns the synthesized plan + `expected_detail` constraint as the tool response.
     - The model then executes any needed operational tools (`read_file`, `exec`, `shell_history`, etc.) in the same agentic loop and emits its final answer constrained by `expected_detail` (or runs the Convergence pass if `ExpectedDetail` was calibrated and the draft exceeds it).

---

## 4. Implementation Steps

1. **FFI Binding for `litert_lm_conversation_clone`:**
   - Add `convClone(conv uint64) uint64` to `LMDriver` in `ai/backend.go`.
   - Register `litert_lm_conversation_clone` in `ai/backend_native.go` (`purego.RegisterLibFunc`) and `ai/backend_rusticated.go` (`syscall.DylibSym` + `callDylib`).
   - Expose `(c *Conversation) Clone() (*Conversation, error)` in `ai/conversation.go`.
2. **`ToolContext` & `deliberate` Tool in `ai/session.go`:**
   - Define `ToolContext` with `CloneConversation()` and `RunContemplationStage(...)`.
   - Implement the built-in `deliberate` tool (`DeliberateToolName = "deliberate"`) using `Conversation.Clone()` on a `Tools: nil` contemplation root (with automatic fallback to `NewConversationWithConfig` if `Clone()` returns an error or in unit tests using `ephemeralStream`).
   - Automatically include `deliberate` in `Session`'s tool registry so both `pope` and `kabibi` inherit it automatically.
3. **Streamline `Session.RunTurn`:**
   - Unify `RunTurn` around the primary agentic conversation loop where `deliberate` is a callable tool (while preserving `CognitiveTrace` attachment, background trace embeddings, and context-overflow RAG compaction).
4. **Tests & Verification:**
   - Add unit tests in `ai/session_test.go` verifying:
     - Direct answers without `deliberate` stream in 1 pass.
     - Calling `deliberate(goal: "...")` runs the contemplation stages (`expectation`, `steps`, 3 critiques, `plan`), uses `Clone()` when available, strips tools on contemplation forks, populates `CognitiveTrace`, and guides subsequent tool calls (`read_file`, etc.) and final response.
   - Verify `go -C ai test`, `go -C pope test`, `go -C kabibi test`, native `go vet`, WASM overlay `go vet`, and live smoke tests.
