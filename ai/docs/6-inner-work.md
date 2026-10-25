> Historical note: The removed planner generated a response-length target, steps, three critiques, and a final plan through staged model calls.
> Its implementation was removed; inspect history around tag `v0.8.9`.

Transitioning `pope` from a single-pass chat wrapper into a reflective reasoning engine requires restructuring how turns are stored, how conversations are forked on the LiteRT-LM engine, and how synthetic dialogue is woven and stripped across stages.

Below is the architectural and algorithmic specification for the engineering team to implement.

---

## 1. The "Iceberg" Storage & Data Model Specification

Every completed turn in history becomes an iceberg: the **Spoken Surface** remains strictly compliant with LiteRT-LM's message schema, while the **Submerged Cognitive Trace** preserves the entire tree of goals, drafts, critiques, and plans attached to that turn.

### A. `CognitiveTrace` Data Structure (New Type in `ai` or `main`)

Attach a pointer to a `CognitiveTrace` struct directly onto `ai.PromptPart` (populated on the final `RoleModel` response part of a turn):

| Field | Type | Purpose |
| --- | --- | --- |
| `IsDirect` | `bool` | `true` if Stage 1 took the `"DIRECT:"` fast-path bypass. |
| `Goal` | `string` | The extracted underlying user goal and task analysis from Stage 1. |
| `InitialSteps` | `string` | The uninhibited first-pass step breakdown from Stage 2. |
| `CritiqueRelevancy` | `string` | Branch 3A output: evaluation of whether the proposed steps actually address the goal. |
| `CritiqueSafety` | `string` | Branch 3B output: evaluation of risks, destructive side effects, or edge-case traps. |
| `CritiqueGrounded` | `string` | Branch 3C output: identification of unverified assumptions and missing information. |
| `SynthesizedPlan` | `string` | The unified execution plan produced by the Stage 4 join. |
| `DraftIterations` | `[]string` | Pre-refinement drafts produced before final post-response polishing. |
| `TraceEmbeddings` | `[][]float32` | Optional 768-dim vectors computed by `Embedder` over the submerged thoughts during idle think-time.

 |

### B. Serialization Invariant in `ai/conversation.go`

* `buildMessageJSON` in `conversation_3.go` serializes only `Role`, `Text`, `ImageBase64`, `ToolName`, `ToolResponse`, and `ToolCalls`.


* It must completely ignore `CognitiveTrace` (just as it ignores `Embedding`), guaranteeing that seeding a conversation via `cfgSetMessages` only ever hands the native C++ runtime clean, spoken surface turns.


* Meanwhile, `HarnessSession.CanonicalHistory` and `HarnessSession.CompactedHistory` retain the full `PromptPart` structs with their attached `CognitiveTrace` pointers intact for future Contemplation passes.



---

## 2. Ephemeral Fork Lifecycle & Engine Rules

Because LiteRT-LM conversations maintain stateful internal KV caches in C++, history manipulation relies on spawning short-lived, isolated `ai.Conversation` instances from the shared `ai.Engine`:

1. **Base History Snapshot:**
At the start of `RunTurn`, before any manipulation begins, take a read-locked snapshot of the committed `s.CompactedHistory`. Call this `baseHistory`.


2. **Ephemeral Fork Helper (`runEphemeralTurn`):**
* Accepts: `seedHistory []ai.PromptPart`, `triggerTurn []ai.PromptPart`, `enableTools bool`, and `stagePrefix string`.
* Calls `s.engine.NewConversationWithConfig` with:


* `SystemMessage: s.systemMessage`

* `History: seedHistory`

* `Tools: nil` when `enableTools == false` (Stages 1–4), or `Tools: s.tools` when `enableTools == true` (Stage 5).




* **Why `Tools: nil` is mandatory in Stages 1–4:** If tool declarations are registered during contemplation stages, a 2B model asked *"What steps would you take?"* or *"Do we need to collect more information?"* will frequently emit a live `ToolCallEvent` mid-thought instead of describing the step in text. Stripping tools from the config during Stages 1–4 forces pure text reasoning.


* Immediately calls `ephemeralConv.Close()` as soon as the stage stream finishes so the native C++ conversation handle is released before the next stage begins.





---

## 3. Zero-ANSI Live Streamer Specification

To display the model's inner monologue in real time without polluting terminal scrollback or corrupting `>` file redirects with ANSI escape codes or per-token carriage returns:

### State Variables

Track four values across a turn's lifecycle:

* `maxRunes` (e.g., `76` runes total line width).
* `prevLineRunes` (visual rune count left on the current terminal line by the previous stage).
* `currLineRunes` (visual rune count printed so far by the active stage).
* `isCapped` (boolean flag indicating the current stage has hit `maxRunes` and printed `…`).

### Transition & Streaming Rules

1. **Starting a Stage (`StartStage(label)`):**
* If `prevLineRunes > 0`: write `\r`, followed by `prevLineRunes` space characters (`' '`), followed by `\r`.
* Reset `currLineRunes = 0` and `isCapped = false`.
* Print the stage tag (e.g., `"[goal] "`) and set `currLineRunes` to its rune length.


2. **Feeding Incoming Tokens (`Feed(tokenChunk)`):**
* Always append the raw, unmodified `tokenChunk` to the stage's internal capture buffer (used for history manipulation).
* If `isCapped == true`, return immediately (print nothing further for this stage).
* Iterate over runes in `tokenChunk`:
* Convert any `\n`, `\r`, or `\t` into a single space `' '`. Collapse consecutive spaces so whitespace never wastes horizontal budget.
* If `currLineRunes + 1 == maxRunes`: print a single Unicode ellipsis `'…'`, increment `currLineRunes`, set `isCapped = true`, and break.
* Otherwise, print the rune and increment `currLineRunes`.




3. **Ending a Stage (`EndStage()`):**
* Set `prevLineRunes = currLineRunes`. Do **not** print a newline.


4. **Wiping Before Spoken Output (`Wipe()`):**
* Right before printing real tool execution logs or the final spoken response, if `prevLineRunes > 0`, write `\r` + `prevLineRunes` spaces + `\r`, and reset `prevLineRunes = 0`.



---

## 4. The 7-Stage Narrative Manipulation & Execution Algorithm

Let $H_{\text{base}}$ be the committed `CompactedHistory` prior to the current turn, and $P$ be the user's actual prompt string (`userText` in `RunTurn`).

```text
H_base + User(P)
   │
   ▼
[Stage 1: Triage] ──("DIRECT:")──► Wipe Ticker & Stream Spoken Reply Immediately
   │
("GOAL:")
   ▼
[Stage 2: Steps] ──► Captures <steps>
   │
   ├──► [Stage 3A: Relevancy Fork] ──► Captures <relevancy>
   ├──► [Stage 3B: Safety Fork]    ──► Captures <safety>
   └──► [Stage 3C: Grounded Fork]  ──► Captures <grounded>
   │
   ▼
[Stage 4: Tribunal Join] ──► Synthesizes <plan>
   │
   ▼
[Stage 5: Let It Rip (Tools Enabled)] ──► Executes Tools & Produces <work_and_answer>
   │
   ▼
[Stage 6: Verification Twist] ──► Audits <work_and_answer> against <goal> & P
   │
   ▼
[Stage 7: Replant Iceberg] ──► Commits Clean Surface + Attached CognitiveTrace

```

### Stage 1: Triage & Goal Extraction (`DIRECT:` vs. `GOAL:`)

* **Seeded History (`History`):**
1. $H_{\text{base}}$
2. `RoleUser`: $P$
3. `RoleModel` (synthetic): `"Wait, let me think for a moment..."`


* Trigger Message (`SendStream` input):


* `RoleUser` (synthetic): `"Take your time. If this is trivial and you are completely confident you can answer immediately without planning or tools, prefix your response with 'DIRECT:' followed by your answer. Otherwise, do not answer yet—first analyze the situation and state the underlying goal and task required to address my prompt above, prefixing your analysis with 'GOAL:'."`


* **Stream Handling:**
* Buffer the first incoming characters (up to ~12 runes or the first colon) before printing anything to the terminal.
* **Fast Path (`DIRECT:`):** If the trimmed stream starts with `"DIRECT:"`, strip the `"DIRECT:"` prefix (and any leading space), bypass all remaining stages, stream the rest of the response directly to `fmt.Print`, and record `CognitiveTrace{IsDirect: true}`.


* **Reflective Path (`GOAL:` or unprefixed):** Strip `"GOAL:"` if present, initialize the live streamer with `StartStage("goal")`, feed the buffered and remaining tokens through the streamer, and capture the full text as `<goal>`.



### Stage 2: Step Ideation (`<steps>`)

* **Seeded History (`History`):**
1. $H_{\text{base}}$
2. `RoleUser`: $P$
3. `RoleModel` (synthetic): `"I need to think first, the goal here is probably: " + <goal>`


* **Trigger Message:**
* `RoleUser` (synthetic): `"What are the concrete steps you would take to achieve that goal?"`


* **Stream Handling:**
* Run with `StartStage("steps")` and `Tools: nil`. Capture the full output as `<steps>`.





### Stage 3: The Orthogonal Triple-Fork Critique (`<relevancy>`, `<safety>`, `<grounded>`)

Here we perform the **Role-Swapped Attribution twist**: `<steps>` is moved out of `RoleModel` and placed inside the synthetic `RoleUser` turn (`"I think you might take these steps below..."`) so the model critiques the steps as an external proposal rather than defending its own prior output.

All three branches share the exact same **Seeded History**:

1. $H_{\text{base}}$
2. `RoleUser`: $P$
3. `RoleModel` (synthetic): `"I need to think first, the goal here is probably: " + <goal>`

Execute three sequential ephemeral conversations against that seed:

* **Branch 3A (Relevancy):**
* **Trigger Message (`RoleUser`):** `"I think you might take these steps below, but are these actually relevant to the goal, or do they miss the point or include unnecessary detours?\n" + <steps>`
* **Streamer Tag:** `StartStage("relevancy")` $\rightarrow$ captures `<relevancy>`.


* **Branch 3B (Safety & Risk):**
* **Trigger Message (`RoleUser`):** `"I think you might take these steps below, but are these safe, or do they carry risks, edge cases, or destructive side effects?\n" + <steps>`
* **Streamer Tag:** `StartStage("safety")` $\rightarrow$ captures `<safety>`.


* **Branch 3C (Information Grounding):**
* **Trigger Message (`RoleUser`):** `"I think you might take these steps below, but are these well-informed, or are they making unverified assumptions where we need to collect more information first?\n" + <steps>`
* **Streamer Tag:** `StartStage("grounded")` $\rightarrow$ captures `<grounded>`.



(Note: When running Branches 3A–3C, include a concise summary list of available tool names inside `SystemMessage` so Branch 3C knows what information-gathering capabilities exist, while still keeping `ConversationConfig.Tools = nil` so it cannot fire tool calls yet.)

### Stage 4: The Tribunal Join & Plan Formulation (`<plan>`)

Now we manufacture a synthetic back-and-forth where the user proposed `<steps>` and the model itself delivered the combined three-angle critique:

* **Seeded History (`History`):**
1. $H_{\text{base}}$
2. `RoleUser`: $P$
3. `RoleModel` (synthetic): `"I need to think first, the goal here is probably: " + <goal>`
4. `RoleUser` (synthetic): `"Maybe these steps would get us there?\n" + <steps>`
5. `RoleModel` (synthetic): `"I have analysed those proposed steps from three angles:\n- Relevancy: " + <relevancy> + "\n- Safety & Risks: " + <safety> + "\n- Information Grounding: " + <grounded>`


* **Trigger Message:**
* `RoleUser` (synthetic): `"I see. Given that analysis, what do you propose as the final, concrete execution plan?"`


* **Stream Handling:**
* Run with `StartStage("plan")` and `Tools: nil`. Capture the full output as `<plan>`.





### Stage 5: Execution ("Let It Rip" — `<work_and_answer>`)

All intermediate critique turns (`<steps>`, `<relevancy>`, `<safety>`, `<grounded>`) are now folded away. Only the distilled `<goal>` and `<plan>` remain in the synthetic monologue:

* **Seeded History (`History`):**
1. $H_{\text{base}}$
2. `RoleUser`: $P$
3. `RoleModel` (synthetic): `"I need to think first. The goal here is: " + <goal> + "\nMy execution plan is:\n" + <plan>`


* **Trigger Message:**
* `RoleUser` (synthetic): `"I see what you mean. Please proceed with that plan now, using any necessary tools, and keep in mind the original request—answer it completely after your work is done:\n" + P`


* **Execution Mechanics:**
* Initialize this conversation with **`Tools: s.tools`**.


* Run the multi-turn tool execution loop currently in `RunTurn`:


* If the model emits `ToolCallEvent`, call `streamer.Wipe()`, execute the tools via `ExecuteTool`, record the `RoleModel` (with `ToolCalls`) and `RoleTool` (`ToolResponse`) parts into a local `executedToolParts []ai.PromptPart` slice, and feed `toolResponses` back into `SendStream`.


* Capture the final generated text answer as `<draft_answer>` (streaming it either into a verification buffer if Stage 6 is active, or directly to the terminal after `streamer.Wipe()`).





### Stage 6: Post-Execution Verification & Refinement

To catch cases where tool execution revealed unexpected data or the model got distracted during multi-step tool calls and forgot part of the original prompt $P$:

* **Seeded History (`History`):**
1. $H_{\text{base}}$
2. `RoleUser`: $P$
3. `executedToolParts...` (any real tool calls and responses that occurred during Stage 5)
4. `RoleModel` (synthetic): `<draft_answer>`


* **Trigger Message:**
* `RoleUser` (synthetic): `"Review your answer above against my original request (" + P + ") and your intended goal (" + <goal> + "). If it is accurate, complete, and directly answers the request, reply with 'OK'. Otherwise, provide the corrected, complete final response."`


* **Stream Handling:**
* Run with `StartStage("verify")`.
* If the response begins with `"OK"`, `streamer.Wipe()` and emit `<draft_answer>` as the final spoken response.
* Otherwise, `streamer.Wipe()` and stream the refined response directly to the user as `<final_answer>`.



---

## 5. Stage 7: Replanting the Iceberg & Syncing Session State

Once `<final_answer>` is produced, all synthetic scaffolding (`"Wait, let me think..."`, `<goal>`, `<steps>`, the 3 critiques, `<plan>`, and the verification nudge) is discarded from the conversation timeline and packed into the iceberg:

1. **Assemble the Spoken Turn Parts:**
Construct the clean sequence of `PromptPart` items that actually occurred "above the waterline":
* `ai.PromptPart{Role: ai.RoleUser, Text: P}`

* `executedToolParts...` (any real `RoleModel` tool calls + `RoleTool` responses executed in Stage 5)


* `ai.PromptPart{Role: ai.RoleModel, Text: finalAnswer, CognitiveTrace: trace}`



2. **Commit to Histories:**
Append those clean parts to both `s.CanonicalHistory` and `s.CompactedHistory` via `s.appendHistory(...)`.


3. **Re-initialize the Persistent Conversation (`s.conv`):**
Close the old `s.conv` and call `s.initWithHistory(s.CompactedHistory)` so the session's primary conversation handle is seeded strictly with the clean spoken transcript, ready for the next turn.


4. **Failure & Retry Semantics:**
* If any ephemeral stage fails with `context.Canceled`, abort the pipeline immediately, call `streamer.Wipe()`, and leave `CanonicalHistory` and `CompactedHistory` untouched (since commits now happen atomically at Stage 7 rather than at the top of `RunTurn`).


* If a stage hits `isContextOverflowError` or `ai.ErrRepetitionLoop`, trigger compaction on `baseHistory` first, refresh `baseHistory`, and restart the stage.




5. **Background Embedding of the Iceberg:**
When `RunTurn` exits and releases `s.turnGate`, `s.triggerBackgroundEmbedding()` wakes up the background worker. In addition to computing `part.Embedding` for the spoken turns, the worker can also embed `trace.Goal + "\n" + trace.SynthesizedPlan` into `trace.TraceEmbeddings`, making Pope's submerged reasoning searchable for future Contemplation passes at zero turn-time latency.