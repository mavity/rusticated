# Technical Direction Brief: Semantic Context Projection in Pope

Moving from Sliding-Window Compaction to Embedding-Driven Context Projection

### 1. The Challenge: Why Traditional Compaction Fails On-Device Models

Running compact on-device models (like **Gemma 4 E2B**) requires operating within a tight, fixed KV-cache budget. Until now, when a conversation approached its token limit—or when the model hit a KV overflow or repetition loop—we relied on standard **FIFO sliding-window compaction**: keep the last 4 messages verbatim and crush everything older into a single 2–3 sentence summary.

While simple, this creates two critical failure modes in real-world agent workflows:

1. **"Amnesia by Distance":** If a user defines a core requirement, schema, or file structure in Turn 2, explores a tangent in Turns 3–10, and returns to the original task in Turn 11, FIFO compaction flattens Turn 2 into a vague summary. The model loses exact code snippets, tool outputs, and constraints right when it needs them again.
2. **Compound Degradation ("Telephone Game"):** Summarizing a previously compacted history means summarizing a summary of a summary. Within a few compaction cycles, early facts evaporate into generic noise.

---

### 2. The Vision: Compaction as a Non-Destructive RAG Projection

Now that we have stabilized our lock-free LiteRT-LM streaming pipeline, added repetition-loop recovery (`ErrRepetitionLoop`), and integrated **EmbeddingGemma 2** to compute 768-dimensional embeddings in the background during user think-time (at zero user-perceived latency), we can fundamentally upgrade how `pope` manages memory.

Instead of treating compaction as a destructive queue, **we are treating active context as a dynamic, topic-driven projection of the full `CanonicalHistory**`:

* **Zero-Cost Topic Sensing:** Whenever compaction triggers, we compute the current conversation topic vector ($\vec{V}_{\text{topic}}$) purely via fast vector math—taking an exponentially decaying weighted average of the latest $X$ messages. No extra LLM calls required.
* **Self-Calibrating Relevance Boundary:** We measure the minimum cosine similarity among the most recent $X/2$ messages to establish a dynamic "on-topic floor." If the user just pivoted to a brand-new subject, this threshold naturally tightens so we don't drag in stale turns from dead topics.
* **Pinning High-Affinity Anchors:** We scan the older history and pin the top $X/4$ messages that beat our relevance threshold **verbatim** (preserving their exact text, tool calls, tool responses, and image payloads).


*  Reversible Memory: Because every projection is rebuilt cleanly from the untouched `CanonicalHistory`, the model can switch from Topic A $\rightarrow$ Topic B $\rightarrow$ back to Topic A, and **original verbatim turns from Topic A will automatically resurrect into the active context**.



---

### 3. Surgical Gap Compression

Only the low-affinity "glue" turns sitting *between* our pinned anchors get summarized. To make sure the summarizer itself never chokes or bloats the context, we enforce two strict mechanical guarantees:

1. **Guaranteed Shrinkage (Budget Step-Down):** Small models often produce "summaries" longer than the input. We enforce a strict word ceiling ($40\%$ of input words). If the model exceeds its budget, we progressively tighten the prompt constraint ($0.75\times$) and retry until the output is genuinely terse and telegraphic.
2. **Recursive Halving for Massive Payloads:** We pass raw tool outputs into compaction without brittle algorithmic string-slicing. If a historical gap is too large for the summarizer's KV window, the pipeline recursively splits the gap in half (respecting atomic tool-call/tool-response boundaries) until each piece fits cleanly.

---

### 4. Why This Matters for the Team

This isn't just a cleanup feature—it is what makes a 2B-parameter local model feel like a much larger frontier model over long sessions. By combining lock-free C++/WASM execution, background embedding generation, and vector-driven context projection, `pope` gains **long-horizon working memory** without increasing memory footprint or turn latency.

# Timeline and Planning

### 1. Architectural Foundations: Atomic Turn Grouping

LiteRT-LM enforces a strict conversation grammar: every model turn containing `ToolCalls` (`RoleModel`) must be followed immediately by its corresponding `RoleTool` responses. If selection or gap-splitting breaks this sequence, the seeded history causes `conversation_create` or `convSendStream` to fail with invalid message schema errors.

Before running vector math or slicing, define an **Atomic Turn Unit**:

1. **User Turn:** A standalone `RoleUser` prompt part.


2. **Assistant Turn:** A standalone `RoleModel` response without tool calls.


3. **Tool Exchange Turn:** A `RoleModel` part containing `ToolCalls`, plus all subsequent `RoleTool` response parts generated for those calls.



Every step in the RAG-compaction pipeline operates strictly on these **Units**, never raw unaligned `PromptPart` slice indices.

```text
CanonicalHistory:
[ U0: User ] ──► [ U1: Model+ToolCalls, ToolResponse ] ──► [ U2: Model ] ──► ... ──► [ UN-1: User ]

```

---

### 2. Pre-Compaction Sync: Flush Missing Embeddings

Compaction can trigger either:

* At the end of a successful turn when `totalTokensEst > compactionLimit`.


* Mid-turn during `RunTurn` when generation crashes with `exceeds available state entries` or `ErrRepetitionLoop`.



In the mid-turn case, the background worker has not yet embedded the newest turns.

* **Rule:** The very first action inside `compactContext` is calling `s.FlushEmbeddings()`.


* This synchronously computes any `nil` embeddings across `CanonicalHistory` so that every atomic unit has a normalized 768-dim vector before vector operations begin.


* If a unit consists of multiple parts (a tool exchange), its unit embedding $\vec{e}_U$ is the normalized sum of the constituent parts' embeddings.

---

### 3. Topic Vector Extraction & Dynamic Boundary Calibration

Let $N$ be the total number of atomic units in `CanonicalHistory`. Let $X$ be the size of the active recent tail (e.g., $X = 6$ or $8$ units).

```text
[ Discard / Search Zone (0 ... N-X-1) ]      [ Calibration (N-X/2 ... N-1) ]
                                            [ Active Tail Window (N-X ... N-1) ]

```

#### Step A: Exponentially-Weighted Topic Vector ($\vec{V}_{\text{topic}}$)

Extract the topic vector purely through fast vector arithmetic on the last $X$ units:

1. Assign exponential decay weights:

$$w_i = \gamma^i \quad (i = 0 \text{ for the newest unit } U_{N-1}, \, i = X-1 \text{ for the oldest in window})$$



A decay factor $\gamma \approx 0.85$ gives full weight ($1.0$) to the immediate turn, $0.85$ to the previous, $0.72$ to the one before, and so forth.
2. Sum the weighted vectors and L2-normalize:

$$\vec{u} = \sum_{i=0}^{X-1} w_i \vec{e}_{N-1-i}, \qquad \vec{V}_{\text{topic}} = \frac{\vec{u}}{\Vert{}\vec{u}\Vert{}_2}$$



#### Step B: Dynamic Boundary Calibration ($T_{\text{boundary}}$)

To determine how far "off-topic" is tolerated without admitting unrelated historical noise:

1. Compute the cosine similarities between $\vec{V}_{\text{topic}}$ and every unit in the last $X/2$ units:

$$S_{\text{active}} = \left\{ \cos(\vec{e}_k, \vec{V}_{\text{topic}}) \;\middle\vert{}\; k \in [N - X/2, \, N-1] \right\}$$


2. Find the minimum similarity:

$$T_{\text{boundary}} = \min(S_{\text{active}})$$



Because everything in the last $X/2$ turns is by definition on-topic to the immediate conversation, $T_{\text{boundary}}$ establishes the empirical outer perimeter of relevance. If the topic changed drastically in the last two turns, $S_{\text{active}}$ will be tightly clustered near $1.0$, automatically setting a high $T_{\text{boundary}}$ that rejects stale turns from the past.

---

### 4. Selection: Verbatim Anchors vs. Gap Slices

Partition the units into two zones:

* **Active Tail ($k \ge N - X$):** Always retained verbatim.
* **Discard / Search Zone ($0 \le k < N - X$):**
1. For each unit $U_k$, compute $\text{sim}_k = \cos(\vec{e}_k, \vec{V}_{\text{topic}})$.
2. Filter for units satisfying $\text{sim}_k \ge T_{\text{boundary}}$.
3. Sort qualifying units by $\text{sim}_k$ descending and select the top $K = \lfloor X/4 \rfloor$ units.
4. These top units are marked as **Pinned Anchors**. All other units in the discard zone are marked as **Unpinned Gap Units**.



Because selection is performed directly against `CanonicalHistory`, if the user switches from Topic A to Topic B and later returns to Topic A, previous Topic A messages will naturally score above $T_{\text{boundary}}$ and resurrect verbatim from `CanonicalHistory`.

---

### 5. Gap Compaction Pipeline

The unpinned units between pinned anchors form contiguous **Gaps**:

$$\text{Canonical} = \underbrace{[U_0 \dots U_2]}_{\text{Gap 0}} \;\longrightarrow\; \mathbf{Anchor_1} \;\longrightarrow\; \underbrace{[U_4 \dots U_9]}_{\text{Gap 1}} \;\longrightarrow\; \mathbf{Anchor_2} \;\longrightarrow\; \dots \;\longrightarrow\; \text{Active Tail}$$

#### A. Direct Gap Serialization

Format all parts in a gap into a plain text dialogue block:

* Pass `ToolName` and raw `ToolResponse` completely as-is. Zero algorithmic truncation.


* Include `Text` and `ToolCalls` verbatim.


* For image parts (`ImageBase64`), because this gap is unpinned, omit the binary base64 string and emit a concise textual descriptor: `[image: user attached image]`. (Pinned anchors keep their full `ImageBase64` intact).



#### B. Recursive Halving on Context Limits (`summarizeGap`)

To handle gaps that would exceed the model's KV cache limit:

1. Estimate the token length of the serialized gap text: $L_{\text{est}} = \text{len}(\text{gapText}) / 4$.
2. **If $L_{\text{est}} > \text{Budget}_{\text{context}}$:**
* If the gap contains $> 1$ units, split the unit slice at the midpoint: `mid := len(units) / 2`.
* Recursively invoke `summarizeGap(units[:mid])` and `summarizeGap(units[mid:])`.
* Concatenate the resulting summaries.
* If the gap contains only $1$ unit (e.g., an enormous 80 KB raw tool dump): split the serialized text block in half by character count and recurse on both string halves.



#### C. Word-Budget Enforcement & Contraction Retries

When a gap slice fits within the context budget, invoke Gemma 4 with a strict, decreasing word budget to eliminate verbose conversational fluff:

1. Calculate the input word count $W_{\text{in}} = \text{wordCount}(\text{gapText})$.
2. Set the initial word ceiling:

$$W_{\text{target}} = \max(15, \, \lfloor W_{\text{in}} \times 0.40 \rfloor)$$


3. **Execution & Shrinkage Loop (Max 2 Retries):**
* Prompt Gemma 4 using a dedicated, transient conversation instance:
> *"You are a conversation compressor. Output ONLY a terse factual record of actions, decisions, and tool results from the dialogue below. Maximum $W_{\text{target}}$ words. NO commentary, NO intros."*


* If the generated summary produces $W_{\text{out}} \ge W_{\text{in}}$ or exceeds $W_{\text{target}}$:
* Nudge the budget: $W_{\text{target}} \leftarrow \lfloor W_{\text{target}} \times 0.75 \rfloor$.
* Re-prompt with the stricter ceiling.


* **Escape Hatch:** If after 2 retries $W_{\text{out}}$ is still not smaller than $W_{\text{in}}$, take the shortest generated candidate and truncate at $W_{\text{target}}$ words.


4. The output is wrapped in a synthetic `PromptPart`:
```go
ai.PromptPart{
    Role: ai.RoleUser,
    Text: "[Context summary]: " + summaryText,
}

```



---

### 6. Assembly & Context Re-initialization

1. **Reconstruct `CompactedHistory` in Chronological Order:**
Interleave the compressed gap parts, the verbatim pinned anchor parts, and the active tail parts:

$$\text{CompactedHistory} = [\text{Summ}_0, \, \mathbf{Anchor_1}, \, \text{Summ}_1, \, \mathbf{Anchor_2}, \dots, \, U_{N-X}, \dots, \, U_{N-1}]$$


2. **Handle In-Flight Turn Retries (`compactContextForRetry`):**
* If compaction was triggered by mid-turn overflow or a repetition loop during turn execution, separate the active `turnParts` (the pending user prompt or tool responses) from the historical units before running the projection.


* The projection runs over historical units.
* Re-append the active `turnParts` to the tail of `CompactedHistory`.




3. **Re-initialize Native Engine Handle:**
* Close `s.conv` (`s.closeConv()`) to tear down the old LiteRT-LM conversation.


* Call `s.initWithHistory(priorCompacted)` to seed the fresh C++ instance with the newly projected context.


* Emit the status indicator (`🗜️ <beforeTokens>:<afterTokens>`) to terminal feedback.




4. **Resume Generation:**
`RunTurn` resumes its `continue` retry seamlessly using the re-anchored conversation.