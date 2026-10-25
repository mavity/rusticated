# Meta-Spec: Extract `ai.Session` & Flip Stage 1 Triage Polarity

> Historical note: The removed planner routed prompts, generated steps and critiques, and synthesized plans before tool execution.
> Its implementation was removed; inspect history around tag `v0.8.9`.

## Objective

1. **Flip Stage 1 Triage Polarity:** Make **either `DIRECT:` or no prefix** take the fast-path bypass straight to spoken output, while **only an explicit `GOAL:` prefix** triggers the 7-stage heavy reasoning pipeline.
2. **Extract `HarnessSession` into `package ai` as `ai.Session`:** Move the reflective chat and RAG-compaction engine out of `pope` into `ai/session.go` so `pope`, `kabibi`, and other applications share a declarative, self-contained chat session API without breaking any low-level `ai` primitives (`Engine`, `Conversation`, `Embedder`, `Interpreter`).

---

## Part 1: Stage 1 Triage Polarity Flip (`DIRECT:` or No Prefix = Fast Path)

To flip the polarity reliably without leaking internal goal-musings as spoken answers, update both the **prefix detector** in `runStage1Triage` and the **default framing** of the Stage 1 prompts.

### 1. Re-Anchoring the Stage 1 Prompts

Previously, `stage1SyntheticWait` (`"Wait, let me think for a moment..."`) and `stage1TriggerPrompt` instructed the model to analyze the goal by default, which caused small models to occasionally write a third-person goal analysis while forgetting the literal `"GOAL:"` prefix.

Invert the prompt's baseline posture so **answering directly is the default** and **`GOAL:` is an explicit interrupt**:

* **`stage1SyntheticWait` (`RoleModel`):**
  ```go
  const stage1SyntheticWait = "Let me consider how to respond..."
  ```

* **`stage1TriggerPrompt` (`RoleUser`):**
  ```go
  const stage1TriggerPrompt = "If you can answer my request above directly without running tools or multi-step planning, simply write your final answer now (with or without a 'DIRECT:' prefix). ONLY if this request requires tool execution, codebase inspection, or multi-step problem solving, do NOT answer yet—instead, you MUST begin your response with the exact prefix 'GOAL:' followed by a one-sentence description of the goal and task required."

  ```



### 2. Updating `decidePrefix` in `runStage1Triage` (Instant Token-1 Resolution)

1. **Wait Only While Matching a Partial Prefix:** 
Because valid prefixes can only ever be `"DIRECT:"` or `"GOAL:"`, buffer incoming tokens only while the trimmed uppercase text is still a partial prefix of one of those two literals and has not yet completed the colon:
```go
upper := strings.ToUpper(trimmed)
hasFullPrefix := strings.HasPrefix(upper, "DIRECT:") || strings.HasPrefix(upper, "GOAL:")
stillPossiblePrefix := !hasFullPrefix && (strings.HasPrefix("DIRECT:", upper) || strings.HasPrefix("GOAL:", upper))
if !finalFlush && stillPossiblePrefix {
    return // keep buffering until "GOAL:" or "DIRECT:" completes or diverges
}

```


If the user asks `"Name a bridge in Paris"` and token 1 is `"Pont"`, `"PONT"` immediately diverges from `"DIRECT:"` and `"GOAL:"` and streams on token 1 with zero buffering lag.
2. **Explicit `GOAL:` Check vs. Direct Default:**
* **If `strings.HasPrefix(upper, "GOAL:")`:**
* Set `directMode = false`.
* Strip `"GOAL:"` (and leading spaces), call `streamer.StartStage("goal")`, and feed the remaining text into `streamer.Feed(...)`.


* **Otherwise (`DIRECT:` or No Prefix):**
* Set `directMode = true` and call `streamer.Wipe()`.
* If `strings.HasPrefix(upper, "DIRECT:")`, strip `"DIRECT:"` and leading spaces.
* Otherwise (no prefix), keep `trimmed` intact so the first word is never clipped.
* Emit the text immediately via `OnContent` and record it into `fullDirect`.





| Model Stage 1 Output | Detected Path | Behavior |
| --- | --- | --- |
| `"DIRECT: Pont Alexandre III."` | **Direct Fast-Path** | Strips `"DIRECT:"`, streams `"Pont Alexandre III."` to `OnContent`; skips Stages 2–7. |
| `"Pont Alexandre III."` *(no prefix)* | **Direct Fast-Path** | Diverges on token 1; streams `"Pont Alexandre III."` intact to `OnContent`; skips Stages 2–7. |
| `"GOAL: Inspect the repo..."` | **Heavy Reasoning** | Strips `"GOAL:"`, starts `[goal]` ticker on `OnFeedback`, and runs Stages 2–7. |

---

## Part 2: Extracting `ai.Session` with Declarative Tools & Twin `LogFeedback`

### 1. Public Types in `ai/session.go`

```go
package ai

// PartEmbedder is satisfied by *ai.Embedder as well as test mocks.
type PartEmbedder interface {
	Embed(parts []PromptPart) ([]float32, error)
}

// LogFeedback receives streamed text fragments (either impermanent status or final content).
type LogFeedback func(text string)

// Tool defines a single capability registered with an ai.Session.
type Tool struct {
	Description string
	Parameters  map[string]any // JSON Schema property map (or full {"type":"object","properties":...})
	Required    []string
	Execute     func(ctx context.Context, call ToolCall, feedback LogFeedback) (any, error)
}

// SessionConfig configures a high-level reflective chat Session.
type SessionConfig struct {
	Engine          *Engine
	Embedder        PartEmbedder    // Optional: enables background embeddings & RAG compaction
	SystemMessage   string
	Tools           map[string]Tool // Declarative tool registry (tool name -> schema + handler)
	CompactionLimit int             // Token watermark for automatic compaction (default: 6000)
	OnContent       LogFeedback     // Receives final spoken response tokens
	OnFeedback      LogFeedback     // Receives impermanent ticker, tool, and compaction updates
	Verbose         bool
}

```

### 2. Automatic Tool Schema Compilation & Dispatch

1. **Deterministic Schema Compilation:**
* Sort the keys of `cfg.Tools` alphabetically so LiteRT-LM receives a deterministic tool declaration slice across turns.
* For each tool, construct the OpenAI-style declaration expected by `ConversationConfig.Tools`:
```go
map[string]any{
    "type": "function",
    "function": map[string]any{
        "name":        name,
        "description": tool.Description,
        "parameters":  compiledParams,
    },
}

```




2. **Automatic `"explanation"` Property Injection:**
* When building `compiledParams`, automatically inject the `"explanation"` property into `"properties"` if not already present:
```go
"explanation": map[string]any{
    "type":        "string",
    "description": "Brief explanation of why this tool is being called and what specific information is sought.",
}

```


* Ensure `"explanation"` is included in the `"required"` slice so the model always supplies intent for directional tool-output compaction and live feedback.


3. **Built-In Dispatch (`Execute`):**
* During Stage 6 (`runStage5Execution`), look up `tool, ok := s.toolRegistry[call.Name]`.
* If found, invoke `tool.Execute(ctx, call, s.emitFeedback)` followed by `s.emitFeedback("\n")`.
* If not found, return `map[string]any{"error": "unknown tool: " + call.Name}` so the model can recover cleanly.



### 3. Twin `LogFeedback` Routing (`OnFeedback` vs. `OnContent`)

Replace `out io.Writer` with two helper methods `s.emitFeedback(text string)` and `s.emitContent(text string)` (which safely no-op when the corresponding callback is `nil`):

| Callback | Purpose | What Flows Through It |
| --- | --- | --- |
| **`OnFeedback`** | **Impermanent / Status Output** | • `liveStreamer` stage snippets (`[goal] …`, `[steps] …`, `[work] …`) and `\r` + space wipes<br>

<br>• Live tool execution progress passed to `Tool.Execute` (plus trailing `\n`)<br>

<br>• Compaction status badges (`🗜️ 4200:1150\n`) |
| **`OnContent`** | **Permanent Spoken Answer** | • Streamed tokens from Stage 1 `DIRECT:` / unprefixed fast-path<br>

<br>• Streamed tokens from Stage 7 Convergence (`runStageConvergence`)<br>

<br>• Trailing `\n` when `Verbose` is enabled |

* **`liveStreamer` Adaptation:** `liveStreamer` takes `feedback LogFeedback` instead of `io.Writer`. `StartStage`, `Feed`, and `Wipe` emit their strings (`"[goal] "`, sanitized token runes, `"…"`, and `"\r     \r"`) directly through `feedback`.
* Right before the first spoken token is emitted in Stage 1 (fast-path) or Stage 7 (Convergence), `streamer.Wipe()` sends the final `\r<spaces>\r` through `OnFeedback`, and all answer tokens flow through `OnContent`.

### 4. Exported `ai.Session` API & Migration Checklist

1. **`ai/session.go`:**
* Move `HarnessSession` from `pope` to `ai` as `Session`.
* Expose `NewSession(cfg SessionConfig) (*Session, error)`, `(s *Session) Init() error`, `(s *Session) RunTurn(ctx context.Context, userText string) error`, `(s *Session) CompactContext(ctx context.Context) error`, `(s *Session) FlushEmbeddings()`, `(s *Session) Close()`, and `(s *Session) Shutdown()`.
* Keep `CanonicalHistory []PromptPart` and `CompactedHistory []PromptPart` exported on `Session`.
* Remove dead legacy helpers (`formatPartForSummary`, `splitHistoryForCompaction`, `buildCompactedHistory`, `compactHistory`).


2. **`pope` Integration:**
* Convert `pope`'s tools into `map[string]ai.Tool` and initialize `ai.NewSession(ai.SessionConfig{...})` in `pope/main.go` with `OnContent: func(s string) { fmt.Print(s) }` and `OnFeedback: func(s string) { fmt.Print(s) }`.
* Move session/compaction unit tests into `ai/session_test.go` (or keep `pope` integration tests referencing `ai.Session`).
