# 11. Environmental System Context & Temporal Turn Telemetry (`11-more-context.md`)

> Historical note: The removed planner routed prompts, generated steps and critiques, and synthesized plans through staged model calls.
> Its implementation was removed; inspect history around tag `v0.8.9`.

## 1. Objective & Design Principles

We want the AI assistant in both **Kabibi** and **Pope** to have rich, accurate situational awareness of:
1. **Where and how it is running (Environmental Context):**
   - Operating system & CPU architecture (`windows`, `linux`, `darwin`, `browser`; `amd64`, `arm64`, `wasm`).
   - Execution platform: **Native** binary vs. **Mohabbat** (Rusticated WASM) and the specific Mohabbat host (`washmhost` / `wazero`, `washmhost-node` / `node`, or `washmhost` in `browser`).
   - Form factor: **Browser** vs. **Desktop**.
   - Loaded AI Engine & LLM model: model name/filename (e.g. `gemma-4-E2B-it.litertlm` or `gemma-4-E2B-it-web.litertlm`), model size in bytes / human-readable GB/MB, active LiteRT-LM backend (`gpu`, `npu`, `cpu`, or `gpu (WebGPU GPU_ARTISAN)`), allocated context window (`maxTokens`) vs. model capability (`MaxContextTokens`, dynamic/static), thinking & tool-calling support, and whether semantic RAG compaction (`EmbeddingGemma 2`) is active.
2. **When each interaction happened and how long it took (Temporal Turn Telemetry):**
   - **User Turn:** Capture when the user typed the first character (`StartedAt`) and when the user submitted the prompt (`CompletedAt`).
   - **AI Turn:** Capture when the first output character arrived (`StartedAt`) and when the final output character arrived (`CompletedAt`).
   - **Tool Execution:** Capture when each tool invocation started (`StartedAt`) and finished (`CompletedAt`).
   - **Strict Data Separation Invariant:** In Go data structures (`ai.PromptPart`, `shell.CommandBlock`, `shell.CommandRecord`), `.Text`, `.Input`, `.Output`, and `.ToolResponse` store **pure, untimestamped content** so UI rendering, history assertions, and vector embeddings (`FormatPartForEmbedding`) are never polluted by timestamp strings.
   - **Wire Projection to the AI:** Only when serializing conversation turns for the LLM wire payload (`buildHistoryJSON`, `buildMessageJSON`, and tool-enabled turn inputs) does the `ai` package prepend the formatted temporal header:
     - **First timestamp in a sequence, or whenever the calendar date (`YYYY-MM-DD`) flips from the previous timestamp:** include the date, weekday, and `HH:MM`:
       - `[2026-10-18 Sun 23:12, typed for 14 seconds]`
     - **Subsequent timestamps on the same calendar date:** omit the date and show only `HH:MM`:
       - `[23:12, typed for 14 seconds]`
       - `[23:13, talked for 4 seconds]`
       - `[23:13, ran for 2 seconds]`

---

## 2. Architectural Division of Responsibilities (`ai` vs. Apps)

### 2.1 Shared `ai/assets` Package (`ai/assets/`)
Provides platform and runtime host introspection so neither `kabibi` nor `pope` needs `#ifdef` / build-tag `wasip1` plumbing:
- **`RuntimePlatformInfo` struct & `InspectRuntimePlatform() RuntimePlatformInfo`** (in `platform_native.go` and `platform_rusticated.go`):
  - `OS`: e.g. `"windows"`, `"linux"`, `"darwin"`, `"browser"`
  - `Arch`: e.g. `"amd64"`, `"arm64"`, `"wasm"`
  - `ExecutionMode`: `"native"` or `"mohabbat"`
  - `HostRuntime`: `"native"`, `"washmhost (wazero)"`, `"washmhost-node (node)"`, or `"washmhost (browser)"`
  - `EnvironmentKind`: `"browser"` or `"desktop"`
  - `BuildPlatform`: host build target when running under Mohabbat
- **`ModelAssetSize(modelPath string) int64`**:
  - Returns file size via `os.Stat(modelPath)` on desktop/Node, or queries browser OPFS/CacheStorage provenance when running in the browser (`DefaultLLMModelSize` / cached size fallback).

### 2.2 Shared `ai` Package (`ai/`)
Owns the canonical data structures, wire serialization, session-level turn timing capture, and reusable environment-prompt builder:

1. **Engine Backend Introspection (`ai/ai.go`, `ai/engine.go`):**
   - `Engine` records the resolved backend name (`e.Backend() string`) that succeeded during `newEngineImpl` (e.g. `"gpu"`, `"npu"`, `"cpu"`), plus `e.ModelPath() string` and `e.MaxTokens() int`.

2. **Succinct Environment Summary Helper (`ai/env_context.go`):**
   - `type EnvironmentConfig struct`:
     - `ModelPath string`
     - `ModelSize int64`
     - `ModelInfo *ModelInfo`
     - `Backend string`
     - `MaxTokens int`
     - `HasEmbedder bool`
     - `WorkingDir string`
     - `ExtraLines []string`
   - `func FormatEnvironmentContext(cfg EnvironmentConfig) string`:
     - Produces a compact, high-signal block suitable for appending to any app's system prompt (both Kabibi and Pope), e.g.:
       ```text
       Environment:
       - Platform: native (windows/amd64, desktop) | Mohabbat WASM via washmhost (browser/wasm, browser)
       - LLM Model: gemma-4-E2B-it.litertlm (1.90 GB, backend: gpu, context: 8192/32768 tokens, thinking: true, tools: true, rag_embedder: EmbeddingGemma-2)
       - Working Directory: C:\Users\mihai\rustica
       ```

3. **Timing Fields on `PromptPart` (`ai/conversation.go`):**
   ```go
   type PromptPart struct {
       Role           Role
       Text           string
       ImageBase64    string
       ToolName       string
       Explanation    string
       ToolResponse   any
       ToolCalls      []ToolCall
       Embedding      []float32
       StartedAt      time.Time     `json:"-"`
       CompletedAt    time.Time     `json:"-"`
       Duration       time.Duration `json:"-"`
   }
   ```
   - `PromptPart.Text` and `PromptPart.ToolResponse` remain 100% free of timestamp prefixes.
   - `FormatPartForEmbedding(p PromptPart)` continues reading only `p.Text`, `p.ToolCalls`, and `p.ToolResponse` so semantic search embeddings remain pure.

4. **Turn Timing Plug in `Session` (`ai/session.go`):**
   - `type TurnTiming struct`:
     - `TypedAt time.Time` (when the user typed the first character)
     - `SubmittedAt time.Time` (when the user pressed Enter / submitted the prompt)
   - `func WithTurnTiming(ctx context.Context, timing TurnTiming) context.Context` and `func (s *Session) RunTurnWithTiming(ctx context.Context, userText string, timing TurnTiming) error`:
     - `Session.RunTurn(ctx, userText)` delegates to `RunTurnWithTiming(ctx, userText, TurnTimingFromContext(ctx))` so existing callers remain source-compatible while timing-aware callers can either pass `WithTurnTiming(ctx, ...)` or call `RunTurnWithTiming`.
   - Inside `RunTurnWithTiming`:
     - If `timing.SubmittedAt.IsZero()`, defaults to `time.Now()`.
     - If `timing.TypedAt.IsZero()` or `timing.TypedAt.After(timing.SubmittedAt)`, defaults `TypedAt = timing.SubmittedAt`.
     - Constructs the user `PromptPart` with `StartedAt: timing.TypedAt`, `CompletedAt: timing.SubmittedAt`, `Duration: timing.SubmittedAt.Sub(timing.TypedAt)`.
     - Records the AI turn start and completion around the tool-enabled model interaction, storing `StartedAt`, `CompletedAt`, and `Duration` on the final `RoleModel` `PromptPart`.
     - Tracks tool execution in `runStage5Execution`: wraps each `s.executeTool(ctx, tc, call)` call with `toolStart := time.Now()` and `toolEnd := time.Now()`, storing `StartedAt: toolStart`, `CompletedAt: toolEnd`, `Duration: toolEnd.Sub(toolStart)` on each `RoleTool` `PromptPart` (and on the intermediate `RoleModel` tool-call turn).

5. **Temporal Wire Formatting (`ai/conversation.go`, `ai/engine.go`, `ai/session.go`):**
   - Helper `formatPartTemporalPrefix(p PromptPart, lastDate *string) string`:
     - Determines the effective timestamp `ts` for the part (`p.CompletedAt` if non-zero, else `p.StartedAt`). If both are zero, returns `""` (preserving exact backward compatibility for untimed unit tests!).
     - Computes `dateKey := ts.Format("2006-01-02")`.
     - If `*lastDate == ""` or `*lastDate != dateKey`, formats time as `ts.Format("2006-01-02 Mon 15:04")` and updates `*lastDate = dateKey`.
     - Otherwise (same date as previous timestamp), formats time as `ts.Format("15:04")`.
     - Computes duration `dur := p.Duration` (or `p.CompletedAt.Sub(p.StartedAt)` if `dur == 0` and both timestamps are non-zero; clamped to `>= 0`). Rounds to whole seconds (`secs := int(dur.Round(time.Second).Seconds())`).
     - Formats action phrase by `Role`:
       - `RoleUser`: `fmt.Sprintf("[%s, typed for %s]", timeStr, formatSeconds(secs))`
       - `RoleModel`: `fmt.Sprintf("[%s, talked for %s]", timeStr, formatSeconds(secs))`
       - `RoleTool`: `fmt.Sprintf("[%s, ran for %s]", timeStr, formatSeconds(secs))`
     - Where `formatSeconds(1)` is `"1 second"` and `formatSeconds(n)` is `fmt.Sprintf("%d seconds", n)`.
   - In `buildHistoryJSON(history []PromptPart)`:
     - Maintains `var lastDate string` across the entire `history` slice.
     - For `RoleUser` and `RoleModel`, prepends `prefix + "\n"` to the wire copy of `part.Text` (or adds a text part if `part.Text == ""` on a tool-calling turn, or leaves tool-only model turns clean if appropriate) without mutating `history[i].Text`.
     - For `RoleTool`, attaches `"timing": prefix` inside a shallow copy of the wire `tool_response` map (or wraps non-map responses) without mutating `history[i].ToolResponse`.
   - In `buildMessageJSON(parts []PromptPart)` (and `buildMessageJSONWithDate(parts []PromptPart, lastDate *string)`):
     - Applies the same wire formatting for live messages.
   - In a standard tool-enabled turn:
     - Sends the timed user part (`PromptPart{Role: RoleUser, Text: userText, StartedAt: userTypedAt, CompletedAt: userSubmittedAt, Duration: ...}`) directly as the first model input so the LLM sees the current turn's `[YYYY-MM-DD Day HH:MM, typed for X seconds]` header.

---

## 3. Application-Side Changes

### 3.1 Kabibi (`kabibi/`)

1. **Capturing First Keypress in `PromptInput` (`kabibi/ui/shell/prompt_input.go`):**
   - Add `firstInputAt time.Time` to `PromptInput`.
   - Whenever text is inserted (`insertText`) or modified (`handleKeyEvent`), if `p.value` becomes non-empty and `p.firstInputAt.IsZero()`, set `p.firstInputAt = time.Now()`.
   - Whenever `p.value` becomes empty (`""`) via `backspace`, `delete`, `down` (history end), `SetValue("")`, or `Reset()`, reset `p.firstInputAt = time.Time{}`.
   - Add `FirstInputAt() time.Time` method to read the captured timestamp before `Reset()`.

2. **Storing Timings in `CommandBlock` & `CommandRecord` (`kabibi/ui/shell/shell.go`, `exec_session.go`, `shell_widget.go`):**
   - Add `TypedAt time.Time`, `SubmittedAt time.Time`, `FirstOutputAt time.Time`, `CompletedAt time.Time` to `CommandBlock` and `CommandRecord`.
   - In `Shell.submitAndExecuteAsync()`:
     - Capture `typedAt := s.input.FirstInputAt()` before `s.input.Reset()`.
     - Capture `submittedAt := time.Now()`.
     - Record `FirstOutputAt` when the first output chunk is written to `s.blocks[blockIdx].Output`, and `CompletedAt` when the block finishes.
   - Pass `ai.WithTurnTiming(ctx, ai.TurnTiming{TypedAt: typedAt, SubmittedAt: submittedAt})` into `aiRunner.RunTurn(...)`.

3. **Populating Environmental Context in `LazyAIRunner` (`kabibi/ui/shell/ai_runner.go`):**
   - In `ensureInitialized`:
     - Build `envCtx := ai.FormatEnvironmentContext(ai.EnvironmentConfig{...})` with the loaded `modelPath`, `assets.ModelAssetSize(modelPath)`, `info`, ` engine.Backend()`, `maxTokens`, `partEmb != nil`, and `r.execSess.Dir()`.
     - Pass `envCtx` into `kabibiSystemMessage(r.systemContext, envCtx)` so Kabibi's system prompt always includes the succinct environmental summary.

### 3.2 Pope (`pope/`)

Yes—Pope should also benefit from both features!
1. **Environmental System Prompt (`pope/main.go`):**
   - Append `ai.FormatEnvironmentContext(...)` to `cfg.SystemFlag` when creating `ai.NewSession` so Pope also knows its OS, platform (native vs. Mohabbat), loaded model, model size, active backend, and working directory.
2. **REPL & One-Shot Turn Timings (`pope/main.go`, `pope/terminal.go`):**
   - In `runTerminalLoop` (`pope/terminal.go`), record when the prompt `> ` is ready / line is submitted and pass `ai.WithTurnTiming` (or let `Session.RunTurn` automatically stamp `SubmittedAt = time.Now()` and track AI response and tool timings).
