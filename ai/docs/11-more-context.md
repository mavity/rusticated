# More Context for AI Conversations

> Perfect! Now I want to introduce a neat feature: populate system prompt with environmental information: current environment like OS, platform (native, or Mohabbat and which specific host), whether it's browser or desktop, also what AI backend and LLM model was loaded, the size, any other interesting facts in succint way.
>
> Also on each turn I want the time of user sending the message be captured, and the time of AI responding -- that timings to be captured in the Go data type, but also reflected to the AI. It should not be reflected to the user, or preserved directly in the text as it's stored in Go data types, only in separate fields. I also want the time of user typing first character for each prompt to be captured, in addition to sending the prompt. And equally for AI the appearance of the first character of the output, then the last character. The capture when sent to the AI should look like:
>
> User:
> ```
> [23:12, typed for 14 seconds]
> Name a bridge in Paris
> ```
>
> Agent:
> ```
> [23:13, talked for 4 seconds]
> A famous bridge in Paris is Pont Neuf.
> ```
>
> However that is the text that AI sees. The text saved in the Golang data types should not include the timestamp markers, those Go should store strictly as fields on data types next to text. And let's capture also tool timings in the similar fashion.
>
> Note that the first time timestamp is provided, and any time it flips the date, the timestamp should include both date and time. But when the timestamp belongs to the same date as the previous timestamp, skip the date.
>
> Do you understand the task? It's about giving AI very rich context, so a user can lean on it too, asking 'What's the time?' or 'Is it Friday?' or 'What model are you using?' and so on.

### Proposed Design Plan

#### 1. Rich, Succinct Environmental System Prompt
When `LazyAIRunner.ensureInitialized` initializes the engine and model, we will gather and inject a concise `Environment` block into the system message (via `kabibiSystemMessage`) and expose it cleanly from `ai/assets` + `ai`:
- **Host / Platform Runtime**:
  - **Native**: `Native Go (<GOOS>/<GOARCH>)`, desktop/server environment.
  - **Mohabbat (Rusticated WASM)**: Inspected via `syscall.GetPlatformInfo()`, reporting the specific Mohabbat host (`washmhost` / `wazero` on desktop, `washmhost-node` on Node.js, or `washmhost` in `browser`), plus whether it is running in a **browser** or **desktop** environment, and the underlying OS/arch (`OSName`, `BuildPlatform`, `WasmPlatform`).
- **AI Engine & Model Details**:
  - **LLM Model**: Model name / filename (e.g., `gemma-4-E2B-it.litertlm` or `gemma-4-E2B-it-web.litertlm`), file size in human-readable form (e.g., `1.90 GB` / `2,038,755,678 bytes`, or web stream/cache size), and active **AI Backend** (`gpu` / `npu` / `cpu`, or `gpu (WebGPU GPU_ARTISAN)` in browser).
  - **Model Capabilities**: Configured context window (`maxTokens`) and raw model max context (`MaxContextTokens`, dynamic vs. static), function calling support, thinking support, and whether semantic RAG compaction (`EmbeddingGemma 2`) is active.
  - **Working Directory**: Initial shell working directory.

---

#### 2. Timing Fields on Go Data Types (Strictly Separate from Stored Text)

1. **`ai.PromptPart` (`ai/conversation.go`)**:
   - Keep `PromptPart.Text` (and `ToolResponse`) completely pure—**no timestamp strings are ever stored in `PromptPart.Text`**.
   - Add dedicated timing fields to `PromptPart`:
     ```go
     StartedAt   time.Time     `json:"-"` // User: first character typed; Model: first output token/char; Tool: tool execution start
     CompletedAt time.Time     `json:"-"` // User: prompt submitted/sent; Model: last output token/char; Tool: tool execution finish
     Duration    time.Duration `json:"-"` // Elapsed duration (CompletedAt - StartedAt, or explicit duration)
     ```
   - `FormatPartForEmbedding` continues using pure `p.Text` so embeddings aren't polluted by timestamps.

2. **`shell.PromptInput` (`kabibi/ui/shell/prompt_input.go`)**:
   - Track `firstInputAt time.Time`:
     - Recorded when the buffer transitions from empty (`""`) to non-empty on the first typed character or paste.
     - Cleared (`time.Time{}`) if the input buffer is emptied (e.g., via Backspace/Delete or `Reset()`), so if the user clears the line and starts typing again later, `firstInputAt` reflects the actual start of typing the submitted prompt.
   - Expose `FirstInputAt() time.Time` so `Shell` can capture it right before `Reset()`.

3. **`shell.CommandBlock` & `shell.CommandRecord` (`kabibi/ui/shell/shell.go`, `exec_session.go`)**:
   - Add `TypedAt time.Time`, `SubmittedAt time.Time`, `FirstOutputAt time.Time`, `CompletedAt time.Time` alongside `StartedAt` and `Duration` so both the UI block and shell history record carry the full timing breakdown in Go structs while keeping `Input` and `Output` text clean.
   - Pass user timing metadata (`typedAt`, `submittedAt`) into `AIRunner.RunTurn` (or via a `TurnTiming` / context option) so `Session` records them on the user `PromptPart` and records first-token / last-token timestamps on the model `PromptPart` (and start/end timestamps on each `RoleTool` `PromptPart`).

---

#### 3. Wire Formatting Shown to the AI (`buildHistoryJSON` / `buildMessageJSON` & Ephemeral Stages)

When serializing `PromptPart` messages for LiteRT-LM (`buildHistoryJSON` and `buildMessageJSON`), we format the text **seen by the AI** by prepending the header line when timing metadata is present, tracking the previous timestamp's calendar date (`YYYY-MM-DD`) across the conversation sequence:

- **Date-inclusion rule**:
  - On the **very first timestamp** in the conversation (or whenever the calendar date `YYYY-MM-DD` differs from the immediately preceding timestamp's date), include the date and weekday/date + time, e.g.:
    `2026-10-18 Sun 23:12` (or `2026-10-18 23:12` — let me know if you prefer a specific date layout!).
  - When the timestamp falls on the **same calendar date** as the previous timestamp, include only `HH:MM`:
    `23:12`
- **Role-specific phrasing**:
  - **User (`RoleUser`)**:
    ```text
    [23:12, typed for 14 seconds]
    Name a bridge in Paris
    ```
    *(Timestamp is submission time `CompletedAt`; duration is `CompletedAt.Sub(StartedAt)` rounded to seconds. If `StartedAt` wasn't अलग/available, e.g. batch mode or 0s, `[23:12, typed for 0 seconds]` or `[23:12]`.)*
  - **Agent (`RoleModel`)**:
    ```text
    [23:13, talked for 4 seconds]
    A famous bridge in Paris is Pont Neuf.
    ```
    *(Timestamp is response completion/start time; duration is `CompletedAt.Sub(StartedAt)` from first output character to last output character.)*
  - **Tool (`RoleTool`)**:
    - Include timing in the tool response payload or prefix (e.g., `ran for 2 seconds` at `[23:13, ran for 2 seconds]`) while keeping the underlying `PromptPart.ToolResponse` Go value untouched in `CanonicalHistory`.

