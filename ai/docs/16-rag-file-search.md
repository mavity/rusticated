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
   Pending tasks that drop to zero active waiters are purged before batch
   selection. If a task has already been selected or submitted for inference,
   cancellation stops further quote work and prevents publishing its result
   when possible, but cannot guarantee that already-started inference is
   undone.

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

Validation is layered so failures can be localized: cache and queue unit tests
verify individual rules; controlled integration tests verify their
composition; end-to-end search tests verify the production flow. Concurrency
tests use explicit gates/channels to establish ordering, not sleeps or
timing-sensitive assumptions.

### Cache unit tests

- Verify signature encoding and invalidation for each required field,
  optional timestamp validity, access-time exclusion, basename identity, and
  the documented same-metadata/content-change case.
- Verify that embedding kind, stable model/provider identity, dimensions,
  recipe, and format version cannot reuse incompatible vectors.
- Verify valid round trips, cache reopen, corrupted/truncated/trailing data,
  checksum and dimension errors, and explicit read/write failures.
- Verify cache initialization creates the cache directory, and writes publish
  complete records by atomic replacement without per-record `MkdirAll` or
  synchronous `fsync`. A write failure must not discard an already computed
  search result.

### Queue algorithm unit tests

- Define `K` as the number of distinct pending tasks selected for the next
  batch, capped by the embedder batch capacity; do not advance the remainder
  for an empty batch. For non-empty batches, verify
  `N = floor((4*K + R)/5)`, `M = K - N`, and
  `R_next = (4*K + R) mod 5`.
- Exhaustively test capacities 1 through 16 and each starting remainder
  `R` from 0 through 4. With enough pending tasks, verify five selected slots
  account for four high-order selections and one low-order selection.
- Verify carryover across variable batch sizes. Starting at `R=0`, capacity
  1 yields `0/1, 1/0, 1/0, 1/0, 1/0`; capacity 2 yields
  `1/1, 2/0, 1/1, 2/0, 2/0` (high/low), returning to the starting remainder
  after each five-slot cycle.
- Verify a single unified task pool: select `N` tasks by
  `highCount` descending then `lowCount` descending, followed by `M`
  remaining tasks by `lowCount` descending then `highCount` descending.
  Break complete ties by insertion sequence. Assert every task is selected
  at most once, including tasks with both counts nonzero.
- Verify the exact ordering when all tasks have zero high demand or zero low
  demand. Do not expect quota reassignment or priority-pool draining; ordering
  still applies to the whole set.
- Verify fewer pending tasks than batch capacity, empty queues, and partially
  filled batches without losing or duplicating tasks or advancing the
  remainder for an empty batch.

### Queue concurrency and lifecycle tests

- Coalesce concurrent callers for the same embedding key. Assert one queued
  task, exact high/low waiter counts, one backend computation, and the same
  result delivered to every live waiter.
- Cancel individual waiters and verify only their priority count decreases.
  A task with another live waiter remains queued and completes for that
  waiter; a pending task with no waiters is removed and never submitted.
- Use controlled gates to test cancellation immediately before selection,
  after selection but before backend invocation, and after inference starts.
  Before selection, zero-waiter tasks must be ejected. After selection, a
  cancelled caller must not receive a result; live coalesced callers must
  still complete. Do not assert that cancellation can undo inference already
  submitted to the backend.
- Test close/cancellation with pending and in-flight tasks, ensuring all
  callers terminate, no result is delivered to cancelled callers, and the
  worker exits. Run these tests under the race detector where supported.
- Verify selection and queue-state locks are not held while invoking the
  embedder, delivering results, or waiting for caller cancellation.

### Cache/queue/search integration tests

- Seed cache hits among candidate files and use an instrumented embedder or
  queue boundary to prove hits bypass the queue while only misses are
  submitted. Verify the expected candidate inputs and stable ranking.
- For content search, prove a cache hit avoids the embedding-only file read
  while preserving the existing exact/regex scan behavior. Verify misses read
  and embed the specified content recipe.
- Exercise live quote promotion, rank changes, and top-$N$ demotion with a
  gated fake embedder. Demotion must cancel extraction promptly and remove
  that quote's pending waiter demand. If the task is shared with another live
  caller, it must remain queued with the remaining count; if its last waiter
  cancels before selection, it must not reach inference.
- Test the selection race separately: once a quote task is selected or sent
  to the backend, demotion cannot guarantee that inference is undone. Verify
  cancellation prevents further quote work and results are not published for
  a demoted file.
- Verify repeated top-$N$ updates do not duplicate jobs, leak goroutines, or
  prevent `Finish`/`Close` from completing.

### End-to-end and platform validation

- Run cold and warm semantic filename and content searches against a
  controlled fixture. Confirm result behavior, cache persistence/reopen,
  candidate hit/miss diagnostics, query embedding behavior, and quote
  promotion/demotion. The warm run must not recompute cached candidate
  embeddings; do not impose a page-cache timing threshold.
- Run the relevant `filelist` and shell tests natively on AMD64 and under
  Rusticated AMD64. Include explicit watchdogs for liveness-sensitive
  operations and retain failure diagnostics. A timeout or unavailable
  required execution mode is a failed acceptance item, not a passing skip.
- Keep the real-embedder performance acceptance tests below as separate
  production-path checks; fake embedders do not replace them.

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
- Ensure that cancellation propagates cleanly through `relevantQuote` $\to$ `embedQuoteInputs` $\to$ `embedder.Embed`, decrementing the waiter count and ejecting a zero-waiter task if it is still pending. Test selection/inference races separately; cancellation cannot undo work already submitted to the backend.

---

### Step 4: Unit & Integration Tests
Implement the cache, queue, and live-quote tests defined in the validation
sections above. Keep algorithm tests deterministic and isolated; use gated
fake embedders for concurrency and integration tests; reserve the real model
for the production-path performance and end-to-end acceptance runs.