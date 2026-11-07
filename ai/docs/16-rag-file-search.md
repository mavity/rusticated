# RAG File Search

## Goal

Make semantic fallback in `list_files` reuse candidate embeddings across
searches and process restarts. A cache hit must avoid reopening a candidate
file solely to recreate its embedding. Preserve the existing lexical and
fuzzy search behavior.

This plan covers semantic filename and content search in `list_files`. It does
not add background workspace indexing, prompt-time context injection, or
semantic range search.

## Design

Use one persistent cache and one index API for candidate embeddings. Store it
under the user's cache directory, outside the searched files. Use a binary
format; do not use JSON or create a Git repository. Store hash-named binary
vector records in one cache namespace; there is no separate metadata index.
The filename is the hash of the entry signature and embedding configuration;
the record stores only its format marker, dimension, checksum, and vector.

Do not store source-file contents. The cache stores entry signatures and
embedding vectors only. Query embeddings and quote embeddings remain ephemeral
per-search work and are not stored by this cache.

Persistent cache writes must avoid disk stalls: ensure the cache directory is
created at initialization, and publish newly embedded vectors via atomic file
replacement without issuing a synchronous `fsync` on every individual record.

## Entry identity and cache key

Define an entry signature from:

- the basename;
- file size;
- modification time in nanoseconds, plus change and birth/creation times when
  the filesystem reports them; and
- the complete file mode/attribute bits.

Use the portable modification time and include optional timestamps only when
the platform marks them valid. Do not include access time: searching can
change it and thereby invalidate the cache on every run. Encode the signature
fields unambiguously and hash the encoding for a compact lookup key. Do not
hash file contents to establish identity or copy signature fields into source
content or vector records.

The cache key also distinguishes the embedding kind and the embedding
configuration that affects vector compatibility: provider/model, dimensions,
input recipe, and format version. These are parts of the same cache key, not
separate stores. The binary vector record contains a format marker, vector
dimension, checksum, and little-endian float32 values.

On each semantic search, obtain the current signature before opening a file
for candidate embedding. If the matching vector exists, reuse it. On a miss,
compute the candidate embedding using the current search input and store the
vector under that signature and embedding configuration. For semantic
filename search, the candidate input is the basename. For semantic content
search, it is the basename, a newline, and the first 1500 bytes of the file
converted to a Go string. The recipe is versioned.

This identity deliberately has metadata-only freshness. If bytes change but
basename, size, timestamps, and bits do not, the old vector may be reused.
If any signature field changes, the old vector is not a hit, even if the
bytes are unchanged. Directory paths are not part of the key. Identical
signatures may share vectors; this is a consequence of the chosen identity.

## Search behavior

1. Keep the existing glob, exact, and fuzzy filename matching behavior.
   Consult the candidate cache only when semantic filename ranking runs.
2. Keep the existing exact/regex content scan and fallback behavior. Consult
   the candidate cache only when semantic content ranking runs.
3. Embed the query for each search. Reuse candidate vectors on cache hits and
   compute only missing candidate vectors. Cache hits return immediately and
   bypass the embedder queue completely; only cache misses and uncached quote
   requests enter the embedder queue.
4. Do not claim that a cache hit eliminates all file reads in content search:
   the existing exact/regex scan still reads candidate contents. The cache
   avoids reading a candidate again solely to build its semantic embedding.
5. Cache failures must be surfaced as errors, not treated as successful hits.
   A cache write failure must not discard search results already computed;
   include a visible warning with the result. Preserve cancellation and
   existing search-result limits.

Because the cache key excludes directory paths, candidate embedding inputs
must not depend on directory paths. Keep full-path lexical and fuzzy matching
unchanged; use the basename for semantic filename candidate vectors and
content candidate vectors.

## Embedder batch queue and fair priority ordering

1. **Single task pool:** The embedder queue maintains all pending embedding
   work in one unified pool of unique tasks keyed by embedding input. Tasks
   are never divided into separate high/low queues.
2. **Waiter demand counts:** Each unique task in the pool records two active
   waiter counts: `highCount` and `lowCount`. Multiple callers requesting
   the same embedding input coalesce onto the same task and increment the
   corresponding count.
3. **Deterministic 4:1 quota and carryover:** The worker maintains a running
   4:1 remainder across batches (modulo $4+1 = 5$). When forming a batch of
   capacity $B$:
   - The worker calculates quota $N$ (high tasks) and quota $M$ (low tasks)
     taking into account the carryover remainder, and updates the remainder.
   - The worker selects $N$ tasks from the pool ordered by `highCount`
     descending, breaking ties by `lowCount` descending.
   - The worker selects $M$ tasks from the remaining pool ordered by
     `lowCount` descending, breaking ties by `highCount` descending.
   - Selected tasks are removed from the pool and batched into embedder
     inference.
   - Tasks are never segregated or filtered prior to ordering. If tasks of a
     priority class are scarce or absent, the selection rule proceeds
     unaffected.
4. **Context cancellation and task ejection:** Every enqueue call passes a
   cancellable `context.Context`. When a caller's context cancels, the
   corresponding count (`highCount` or `lowCount`) on that task is
   decremented. If both `highCount == 0 && lowCount == 0`, the task is
   immediately ejected from the pool before the worker can pick it up.

## Live top-N quote lifecycle and task ejection

1. **Dynamic top-N contenders:** During candidate scoring, the search
   incrementally updates the top-$N$ highest scoring candidate files.
2. **Cancellable extraction goroutines:** When a candidate enters the top
   $N$, a dedicated goroutine begins quote extraction (chunking and
   bisection) bound to a cancellable `context.Context`. Quote embedding
   requests enter the embedder queue as low-priority work.
3. **Immediate demotion ejection:** When a newly scored candidate displaces an
   existing candidate from the top $N$, the displaced candidate's quote
   context is cancelled immediately. The embedder queue processes the
   cancellation by decrementing `lowCount` for the pending quote tasks.
   Tasks that drop to zero active waiters are purged from the pool on the spot.
   This guarantees the embedder worker never performs batch inference on
   quote chunks for files that have fallen out of the top $N$.

## Implementation boundaries

- Add cache access through the existing `filelist` search flow; callers should
  not open or manipulate cache storage directly.
- Obtain all signature fields from filesystem metadata without reading file
  contents. Do not introduce a content hash on the cache-hit path.
- If an embedder does not expose a stable cache identity, disable persistent
  reuse and report that fact rather than risk mixing vectors from different
  models.
- Keep cache reads, writes, and embedding work compatible with the project's
  async I/O and Rusticated build constraints.
- Do not add automatic eviction in this implementation. Obsolete signatures
  remain cached; deleting `<user-cache>/kabibi/filelist/semantic-v1` clears
  them. This initial policy permits unbounded growth.

## Validation

- A repeated semantic search reuses candidate vectors after reopening the
  cache and does not reopen unchanged candidates solely for embedding.
- Changing each signature field causes a cache miss; unchanged signature
  reuses its vector, including the documented case where bytes changed
  without metadata changing.
- Optional change/birth timestamps are included only when valid; access time
  is excluded.
- Different embedding kinds, models/providers, dimensions, recipes, and
  format versions cannot reuse incompatible vectors.
- Semantic filename vectors use basenames, while existing full-path lexical
  and fuzzy matching remains unchanged.
- Content search preserves its existing exact/regex behavior and avoids the
  second candidate read for embedding on a cache hit.
- Cache writes publish via atomic rename without issuing synchronous `fsync`
  on individual vector records.
- Embedder batch queue verifies deterministic 4:1 quota derivation with
  modulo-5 remainder tracking across variable batch sizes.
- Task selection verifies primary count sorting and opposite-count secondary
  tie-breaking from the single task pool.
- Request context cancellation decrements task demand counts and purges
  zero-waiter tasks from the queue immediately.
- Live top-$N$ displacement cancels active quote extractions and confirms
  displaced quote tasks are purged from the queue before batch formation.
- No source contents or JSON cache files are persisted. Cache read/write
  failures, embedding failures, cancellation, and cache reopen behavior have
  explicit tests.
- Run the relevant native and Rusticated AMD64 `filelist` and shell tests.

### Minimum embedding performance acceptance

The real-embedder validation must also include the following performance
acceptance tests. These tests exercise the production embedding path and model;
fake embedders are not substitutes. Each test must log the measured duration
and enough model/runtime and execution-mode information to interpret the
result. Run them in native AMD64 and Rusticated AMD64 validation.

1. **Real semantic grouping.** Embed `duck`, `chicken`, `iron`, and `copper`.
   Compute and report the full cosine-similarity matrix. Assert that duck and
   chicken are mutual nearest neighbors and iron and copper are mutual nearest
   neighbors.
2. **Warm single-input latency.** Embed `London`, `Paris`, `Tokyo`, and
   `Lagos` sequentially. Log each call's duration. Require every call after
   the first to complete in under one second.
3. **Real batches.** Embed four currency terms in one batch, then four color
   terms in a separate batch. Log both batch durations and validate that all
   eight vectors are non-empty, finite, and dimensionally consistent. Require
   the second batch to complete in under three seconds.
4. **Long-input latency.** Embed a 4,915-byte source slice to warm the path,
   then embed a different 4,915-byte slice from the test source. Log both
   durations and require the second, warm embedding to complete in under
   three seconds.

These are acceptance thresholds, not merely benchmark reporting: exceeding a
threshold or failing a semantic/vector assertion fails the performance test.
The test output must retain all measured durations on failure. Do not silently
skip a required execution mode because the model or runtime is unavailable;
record the explicit unsupported-platform reason and treat it as an unmet
acceptance requirement until resolved.

## Follow-up

- No automatic eviction is implemented. Define a cache-size and eviction
  policy before adding automatic deletion.

## Implementation plan


### Step 1: Streamline Persistent Cache in `embedding_cache.go`
1. **Initialize directory once**: Ensure `os.MkdirAll(c.dir, 0o700)` is done during `newEmbeddingCache()` rather than on every `put` call.
2. **Remove synchronous disk flush**: In `embeddingCache.put`, remove `temp.Sync()` (`fsync`). Close the temporary file and atomically rename it to the target `.vec` path.

---

### Step 2: Implement Single-Pool 4:1 Queue in `embed_scheduler.go`
1. **Single Task Pool**:
   - Represent each unique embedding input as an `embeddingTask` with `highCount int` and `lowCount int` tracking active waiters, a list of waiter channels/contexts, and an arrival `sequence` for stable tie-breaking.
   - Remove the artificial queue capacity limit (`maxQueuedEmbeddings = 256`, `errEmbeddingQueueFull`).
   - Store active tasks in a unified map `tasks map[string]*embeddingTask`.
2. **Enqueue & Waiter Registration**:
   - On `Embed(ctx, part, lowPriority, order)`:
     - Find or create the task in `tasks`.
     - Increment `task.lowCount++` or `task.highCount++` according to priority.
     - Register the waiter and signal the worker.
3. **Context Cancellation & Task Ejection**:
   - If `ctx.Done()` fires while waiting:
     - Under lock, locate the task and remove the waiter.
     - Decrement `task.lowCount` or `task.highCount`.
     - If `!task.inFlight && task.highCount == 0 && task.lowCount == 0`, immediately delete the task from `tasks`.
4. **Deterministic 4:1 Batch Selection (`nextBatch`)**:
   - The worker maintains a running remainder $R \in [0, 4]$ across batches.
   - Given available batch slots $K \le B$:
     - Compute high quota $N = (K \times 4 + R) / 5$.
     - Compute low quota $M = K - N$.
     - Update carryover remainder $R \leftarrow (K \times 4 + R) \pmod 5$.
   - Select up to $N$ tasks ordered by: `highCount` descending, then `lowCount` descending, then `sequence` ascending.
   - From remaining tasks, select up to $M$ tasks ordered by: `lowCount` descending, then `highCount` descending, then `sequence` ascending.
   - Mark selected tasks as in-flight and execute batch embedding outside the queue lock.
   - Dispatch results to all waiters on each task and remove completed tasks.

---

### Step 3: Validate Dynamic Top-$N$ Quote Ejection in `live_quotes.go`
- Verify `liveQuoteManager.sync()`: when an older candidate is displaced from the visible top $N$, its `job.cancel()` is called immediately.
- Ensure that cancellation propagates cleanly through `relevantQuote` $\to$ `embedQuoteInputs` $\to$ `embedder.Embed`, triggering immediate decrement and task ejection in the scheduler before the worker can pick it up.

---

### Step 4: Unit & Integration Tests
1. **`embed_scheduler_test.go`**:
   - Test 4:1 remainder carryover across variable batch formations (verifying exact $N$ and $M$ allocations across multiple consecutive batches).
   - Test two-tier sorting: high quota sorts by `(highCount, lowCount)`, low quota sorts by `(lowCount, highCount)`.
   - Test context cancellation and task ejection: cancelling waiters decrements counts, and reaching $(0, 0)$ purges the task from the pool.
   - Test coalesced waiters: task with 1 high and 1 low waiter keeps running if 1 cancels, satisfied if completed.
2. **`live_quotes_test.go` / `filelist_search_test.go`**:
   - Test candidate demotion: displacing a candidate in the top $N$ cancels quote extraction and verifies that pending quote tasks are ejected.
3. **`embedding_cache_test.go`**:
   - Verify cache initialization and atomic replacement without `temp.Sync()`.


#### Further tests

Here is a comprehensive testing and validation plan that tests the individual complex mechanisms, their composition into subsystem blocks, and the entire system end-to-end under both native and Rusticated environments.

### Level 1: Unit Validation — Discrete Algorithmic Mechanics

These tests isolate the individual building blocks in direct unit tests without concurrency or I/O noise:

1. **4:1 Quota Arithmetic & Modulo-5 Carryover:**
   - **Exhaustive Quota Sweep**: Test batch capacities $B \in [1, 16]$ across all possible initial remainder states $R \in [0, 4]$. Assert that $N + M = \min(B, \text{tasks})$, $R_{\text{next}} = (B \times 4 + R) \pmod 5$, and that over a cycle of 5 items, exactly 4 high slots and 1 low slot are allocated.
   - **Remainder Carryover Progression**: Sequence successive batches of capacity 1 (yielding $N, M$: 0/1, 1/0, 1/0, 1/0, 1/0, etc.) and capacity 2 (yielding $N, M$: 1/1, 2/0, 2/0, 1/1, 2/0, etc.) to verify that remainder carries strictly across consecutive batch formations.

2. **Deterministic Task Ordering & Tie-Breaking:**
   - **Primary vs Secondary Count Ordering**: Construct a pool with mixed tasks: `A(high: 3, low: 1)`, `B(high: 3, low: 2)`, `C(high: 1, low: 5)`, `D(high: 2, low: 0)`.
     - High quota ($N=2$) must select `B` then `A` (`high` ties broken by `low`).
     - Low quota ($M=2$) from remainder must select `C` then `D` (`low` ties broken by `high`).
   - **Sequence Preservation**: When both `high` and `low` counts are equal, assert strict FIFO selection by task insertion sequence.
   - **Exhaustion / Asymmetric Pools**: When the pool has zero high tasks, the high quota selection drains gracefully and yields to low tasks without stalling or creating empty batches. Vice-versa when the pool has zero low tasks.

3. **Persistent Cache I/O Unit Verification:**
   - **Initialization**: Verify `os.MkdirAll` is executed at cache construction time.
   - **No Disk Stalls**: Confirm `embeddingCache.put` writes and atomically renames the temporary record without calling `Sync()` (`fsync`).

---

### Level 2: Queue Concurrency & Lifecycle Validation

These tests exercise concurrency, racing cancellations, and multi-waiter coalescing in `embed_scheduler.go`:

1. **Multi-Waiter Coalescing & Count Accounting:**
   - Enqueue identical input text from 3 high-priority and 2 low-priority callers. Assert only 1 unique task exists in the pool with `highCount = 3` and `lowCount = 2`.
   - Complete the batch: assert all 5 callers receive the identical vector and no error.
   - Enqueue a task with 1 high and 1 low waiter. Cancel the high waiter: assert `highCount` becomes 0, `lowCount` remains 1, the task remains pending in the pool, and the low waiter completes successfully when the batch runs.

2. **Immediate Task Ejection on Zero Waiters:**
   - Enqueue 5 unique tasks. Cancel 2 before the worker forms a batch.
   - Assert their waiter counts drop to 0 and they are immediately removed from the active task pool.
   - Assert the worker selects only the 3 remaining tasks into the next batch and does not dispatch inference for the purged 2.

3. **Cancellation Races:**
   - **Cancel during Selection/In-Flight**: A caller context cancels while the batch is currently executing inference. Assert the cancellation returns `context.Canceled` to the cancelling caller, but sibling callers on the same coalesced task receive the valid embedding, and the embedder backend is not left in a broken state.
   - **Queue Close**: Calling `scheduler.Close()` immediately fails all pending waiters with cancellation and unblocks any waiting goroutines.

---

### Level 3: Subsystem Integration Validation

These tests verify the interplay between search, caching, quote extraction, and the embedder queue:

1. **Cache Bypass Verification:**
   - Seed `embeddingCache` with vectors for 5 out of 10 candidate files.
   - Run semantic ranking with a mock embedder tracking calls.
   - Assert: exactly 5 cache hits occur and **zero** requests enter the scheduler for those 5 files; exactly 5 cache misses enter the scheduler as high-priority tasks.

2. **Dynamic Top-$N$ Quote Displacement & Ejection:**
   - Configure a mock embedder with an artificial delay.
   - Search a dataset where File 1 initially enters the top $N$ and triggers quote extraction (enqueuing quote chunks as low-priority tasks).
   - Before the quote batch executes, File 2 and File 3 score higher and displace File 1 out of the top $N$.
   - Assert: File 1's quote context is cancelled, its quote tasks in the embedder queue are decremented to zero and purged, and the mock embedder **never receives** inference calls for File 1's quote chunks.

3. **High vs Low Interleaving during Search:**
   - Run a search where candidate files are streaming in (generating high-priority cache misses) concurrently with active quote extractions (generating low-priority tasks).
   - Instrument batch formation to log batch contents: verify that batches interleave high candidate tasks and low quote tasks according to the 4:1 carryover policy rather than starvation of either class.

---

### Level 4: End-to-End Real Embedder Acceptance

These tests run the full production pipeline with the real `embeddinggemma-2-text-270m.litertlm` model:

1. **Live Search Latency & Diagnostics Test:**
   - Run a real verbose semantic content search across the test dataset.
   - Validate diagnostics: verify reported candidate cache hits, cache misses, batch formations, and quote extractions.
   - Assert that candidate cache hits execute at disk/page-cache speed without queuing delays.

2. **Production Performance Acceptance Suite:**
   - Execute the 4 required production acceptance benchmarks:
     1. Semantic grouping: `duck`/`chicken` vs `iron`/`copper` mutual nearest neighbors.
     2. Warm single-input latency: `London`, `Paris`, `Tokyo`, `Lagos` sequential under 1.0s.
     3. Real batch latency: 4 currency terms + 4 color terms under 3.0s.
     4. Long-input latency: 4,915-byte source slice warm embedding under 3.0s.

3. **Native AMD64 and Rusticated Parity:**
   - Run the full suite under native Go (`GOARCH=amd64 go test`).
   - Run the `filelist` and search suite under Rusticated (`go run . -t kabibi` or targeted Rusticated test runner) to verify no async host stalls, deadlocks, or WASM ABI regressions.