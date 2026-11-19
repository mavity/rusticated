# 17. Coordinating Chat Turns and Sequential Background Embeddings

## 1. Problem Statement

Today, background embedding and foreground chat turns contend on the same `Session` admission gate (`activeOperation`), and sequential fallback tears down and reloads the chat `*ai.Engine` for every single history item:

1. **False busy rejections on chat turns:** `Session.processPendingEmbeddings` acquires `s.activeOperation` via `beginOperation` for the entire background embedding loop (`ai/session.go`). Any `RunTurn` arriving while background embedding is active sees `activeOperation != nil` and fails immediately with `ErrSessionBusy`, even though `turnWaiting` was intended to let user turns take priority.
2. **Per-item engine teardown/reload:** When both the chat engine and embedding engine cannot remain open simultaneously (e.g. low-end mobile or browser GPUs), `adaptiveEmbedder.embedSequential` (`kabibi/ui/shell/adaptive_embedder.go`) suspends the conversation, closes `*ai.Engine`, opens `*ai.Embedder`, computes **one** embedding, closes `*ai.Embedder`, reopens `*ai.Engine`, and rebuilds the conversation—repeating this cycle for every pending `PromptPart` in `CanonicalHistory`.
3. **Stale engine pointer in `helper`:** `newKabibiHelperTool` (`kabibi/ui/shell/ai_helper.go`) captures the initial `*ai.Engine` by value at session creation, so any subsequent engine reopen leaves `helper` pointing at a closed engine.

## 2. Target Behavior

- **Foreground turns can intrude on background embedding:** If a chat turn (`RunTurn`) arrives while background embedding is running:
  - Do **not** interrupt an individual in-flight embedding calculation.
  - Signal background embedding to pause (`turnWaiting`) and wait for the current single embedding calculation (and any embedder cleanup) to finish.
  - If the chat engine was closed to free GPU resources for embedding, detect that it is closed (`nil`) and reinitialize the engine and conversation before running the LLM turn.
  - When the LLM turn finishes and releases the foreground slot, resume background embedding for any remaining unembedded history parts.
- **Keep fast-fail admission for concurrent/reentrant foreground calls:** A second concurrent `RunTurn`, a reentrant `RunTurn` from inside a callback, or a concurrent `CompactContext` / `FlushEmbeddings` while another foreground operation is active must still fail fast with `ErrSessionBusy` (and `ErrAIRunnerBusy` at `LazyAIRunner`).
- **Batch sequential engine close/reopen around the background pass:** In sequential mode, close the chat engine **once** at the start of a background embedding pass, keep the embedder open across pending items while no turn is waiting, and close the embedder when yielding or finishing—leaving the chat engine closed (`nil`) until the next `RunTurn` or `CompactContext` needs it.

## 3. Synchronization & State-Transition Design

### 3.1 `ai.Session`: Foreground Operations vs. Background Embedding

Keep `s.operationMu` as the single mutex guarding admission and lifecycle state. Separate foreground ownership from background worker state:

- **Foreground slot (`s.activeOperation *sessionOperation`):**
  - Owned exclusively by foreground operations: `RunTurnWithTiming`, `CompactContext`, `FlushEmbeddingsContext`, `Init`, `ReplaceEngine`, `SuspendConversation`, `Close`.
  - `beginOperation(ctx)` rejects `s.shuttingDown` (`ErrSessionClosed`) and `s.activeOperation != nil` (`ErrSessionBusy`).
- **Background worker state (under `s.operationMu`):**
  - `bgEmbedRunning bool`
  - `bgEmbedDone chan struct{}`
  - Before starting a background pass, `embedWorkerLoop` locks `operationMu`. If `s.shuttingDown`, `s.activeOperation != nil`, or `s.turnWaiting.Load()`, it returns without starting. Otherwise it sets `s.bgEmbedRunning = true` and `s.bgEmbedDone = make(chan struct{})`, then unlocks `operationMu`.
  - When the background pass finishes or yields, it locks `operationMu`, sets `s.bgEmbedRunning = false`, unlocks `operationMu`, and closes `bgDone`.

### 3.2 Foreground Intrusion Sequence (`RunTurnWithTiming`, `CompactContext`, `FlushEmbeddingsContext`)

1. Acquire `s.activeOperation` via `beginOperation(ctx)`.
   - Any concurrent or reentrant foreground caller fails fast here with `ErrSessionBusy` before touching `turnWaiting` or waiting.
   - While `s.activeOperation != nil`, `embedWorkerLoop` cannot start a new background pass.
2. Set `s.turnWaiting.Store(true)`.
3. Under `s.operationMu`, check `s.bgEmbedRunning` and snapshot `bgDone := s.bgEmbedDone`. Unlock `s.operationMu`.
4. If `bgDone != nil`, wait:
   ```go
   select {
   case <-bgDone:
   case <-ctx.Done():
       s.turnWaiting.Store(false)
       s.endOperation(op)
       return ctx.Err()
   }
   ```
   - The in-flight single embedding calculation is **never** interrupted; the foreground caller waits for `bgDone` unless `ctx` itself is cancelled/timed out.
5. Clear `s.turnWaiting.Store(false)`.
6. For operations that require the LLM (`RunTurnWithTiming`, `CompactContext`), if `s.engine == nil`, reinitialize the engine and rebuild the conversation from `CompactedHistory` (Section 3.3).
7. Execute the operation.
8. On exit (`defer`), call `s.endOperation(op)` and `s.triggerBackgroundEmbedding()` (for `RunTurnWithTiming` and `CompactContext`) so background embedding resumes any remaining work.

### 3.3 Lock Ordering & Deadlock Freedom

- `operationMu` is **never** held while waiting on `bgDone`, while calling `Embed`, or while calling the LLM.
- `historyMu` is **only** held briefly to read `CanonicalHistory[idx]` or write back the computed embedding (`applyEmbeddingLocked`), never across `Embed` or `bgDone` waits.
- `embedWorkerLoop` never calls `beginOperation` while `bgEmbedRunning` is true, so it can never wait on a foreground operation while a foreground operation waits on `bgDone`.

## 4. Engine Lifecycle & Sequential Mode (`adaptiveEmbedder` + `LazyAIRunner`)

### 4.1 Background Embedding Pass

1. **Coexistence mode (`resident != nil`):**
   - `s.engine` stays open.
   - `embedWorkerLoop` embeds pending `CanonicalHistory` items one by one, checking `s.turnWaiting.Load()` and `<-s.embedStop` between items.
2. **Sequential mode (`resident == nil`):**
   - Before entering the loop (only when at least one unembedded item exists in `CanonicalHistory`), suspend `s.conv` and close `r.engine` / `s.engine` **once** (setting `s.engine = nil` and `r.engine = nil` under `r.lifeMu`).
   - Open `*ai.Embedder` **once** for the pass.
   - Loop over pending items in `CanonicalHistory`:
     - Before each item, check `<-s.embedStop` and `s.turnWaiting.Load()`. If either is set, exit the loop.
     - Compute the embedding for item `idx` (uninterrupted) and apply it under `historyMu`.
   - On loop exit (completed all items, `turnWaiting`, or `embedStop`), close the temporary `*ai.Embedder` to free GPU resources, leaving `s.engine == nil` until the next `RunTurn` or `CompactContext` needs it.
   - Leaving `s.engine == nil` when idle also avoids reloading a large model when no further chat turn arrives.

### 4.2 On-Demand Engine Reinitialization

- Add `Engine() *Engine` on `*ai.Session` and an optional engine-reopen hook (e.g. `SessionConfig.ReopenEngine func(context.Context) (*Engine, error)` or runner-managed reopen) wired by `LazyAIRunner`.
- When `RunTurnWithTiming` or `CompactContext` sees `s.engine == nil`, it invokes the reopen hook, updates `r.engine` under `r.lifeMu`, and calls `s.replaceEngine(engine)` to recreate `s.conv` from `CompactedHistory`.
- If reopening fails, the error is returned to the caller and `s.engine` remains `nil` so a subsequent turn can retry or surface the failure cleanly.

### 4.3 In-Turn Embeddings & `helper` Tool

- Tool calls (e.g. `find_files`) and `compactContextForRetry` (`flushEmbeddingsInline`) run **inside** an active foreground turn (`s.activeOperation` is held, `bgEmbedRunning` is `false`, and `s.engine` is open).
- In sequential mode, an in-turn embedding call still suspends `s.conv`, closes `engine`, opens/runs/closes the embedder, reopens `engine`, and restores `s.conv` via `ReplaceEngineDuringOperation`.
- Update `newKabibiHelperTool` (`kabibi/ui/shell/ai_helper.go`) to read the current engine via `tc.Session.Engine()` (instead of capturing the initial `engine` pointer by value) so `helper` always uses the live reopened engine.

### 4.4 Shutdown & Cleanup

- `Session.Shutdown()` sets `s.shuttingDown = true`, cancels `s.activeOperation` (unblocking any foreground caller waiting on `bgDone`), and closes `s.embedStop`.
- `Session.WaitShutdown(ctx)` reads `s.embedDone` under `s.operationMu` (avoiding a race with `startEmbedWorker`) and waits for both `<-op.done` and `<-embedDone`.
- `LazyAIRunner.closeResources()` continues to call `sess.Shutdown()` and `sess.WaitShutdown(context.Background())` before detaching and closing `r.embedder` and `r.engine`.

## 5. Planned Changes & Verification

1. [ai/session.go](../session.go):
   - Separate `bgEmbedRunning`/`bgEmbedDone` from `activeOperation`; add foreground wait on `bgDone` (respecting `ctx.Done()`); add `Engine() *Engine`; add on-demand engine reinitialization when `s.engine == nil` in `RunTurnWithTiming` / `CompactContext`; read `embedDone` under `operationMu` in `WaitShutdown`.
2. [kabibi/ui/shell/adaptive_embedder.go](../../kabibi/ui/shell/adaptive_embedder.go) and [kabibi/ui/shell/ai_runner.go](../../kabibi/ui/shell/ai_runner.go):
   - Batch sequential background embedding (close engine once per pass, keep embedder open across items while `!turnWaiting`, close embedder on yield/exit, leave engine closed until needed) and wire on-demand `reopenEngine`.
3. [kabibi/ui/shell/ai_helper.go](../../kabibi/ui/shell/ai_helper.go):
   - Read live `tc.Session.Engine()` rather than a startup-captured `*ai.Engine`.
4. Tests in [ai/session_test.go](../session_test.go) and [kabibi/ui/shell/ai_runner_test.go](../../kabibi/ui/shell/ai_runner_test.go):
   - `RunTurn` intruding on an active background embedding item waits for that single item to finish (does not fail with `ErrSessionBusy`), reinitializes a closed engine, completes the turn, and lets background embedding resume remaining items.
   - Context cancellation while waiting on `bgDone` releases `activeOperation` and returns `ctx.Err()` without interrupting the in-flight embedding.
   - Concurrent and reentrant foreground `RunTurn` / `CompactContext` / `FlushEmbeddings` calls still fail fast with `ErrSessionBusy` / `ErrAIRunnerBusy`.
