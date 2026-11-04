# Persistent Embedding Cache for `list-files`

This plan covers persistent reuse of file embeddings by Kabibi's `list-files`
search. It does not prescribe automatic scanning or conversation-context
injection.

## Scope

The cache is keyed by each file's full absolute path. It is stored in the
user's cache area, not in the searched directory or its `.git` directory.

On a cache hit, `list-files` reuses the saved embedding when the file's
recorded metadata and embedding configuration still match. On a miss or
stale entry, that file's embedding is queued for background calculation at
the appropriate priority. A successful calculation is saved for later
searches.

Cache semantic filename search and semantic content search. They use different
embedding inputs, so a path's cached filename vector must not be mistaken for
its content vector. Both remain associated with the candidate's full absolute
path. Filename search includes directory entries as candidates, so directory
paths can have filename vectors too; content search considers regular files.

File metadata, including size and available timestamps, is used to validate
the entry. The cache does not hash file contents to determine whether an entry
is valid. This is a fast metadata-based check; edits that preserve the
fingerprint can therefore leave a stale vector.

Existing lexical matching remains available and does not require an embedding.
This plan does not include `read_file` search, `fabric`, prompt-time workspace
scanning, or automatic injection and retention of conversation context.

## Search and cache behavior

Each candidate's absolute path identifies its cache entry. Different paths
have different entries, even if they have the same basename or contents.

Cache state is per file, not per directory or search. A single search can
contain valid hits, missing entries, and entries invalidated by metadata
changes. Use valid cached vectors immediately; do not make a valid hit wait
for another file's embedding. Return available results without waiting for
missing embeddings. Calculate misses in the background, persist successful
results, and make them available to subsequent searches. Thus an initial
result can be partial, and a directory can be partially indexed indefinitely.

Only a completed, valid embedding may be stored. If a file changes while its
embedding is queued or being calculated, the result must not be recorded as
valid for the newer file state. Cache read, write, and embedding errors must
be surfaced according to the caller's existing error behavior; they must not
be reported as successful cache hits.

The cache is persistent across `list-files` calls and process restarts. Query
embeddings are not part of this plan unless explicitly added later.

## Implementation behavior

The persistent path store is in the user's cache directory under Kabibi's
`list_files_embeddings` area. Entries are keyed by semantic mode and cleaned
full absolute path, and the cache identity also includes the embedding model
identity and a cache format version.

The current metadata fingerprint is file size, file mode, and modification
time in nanoseconds. It is checked before reuse, and again after background
embedding before persistence. No source-content hash is computed. A same-size
edit that preserves the mode and modification time can therefore leave a
stale vector.

The persistent cache owns the low-priority background scheduler. The long-lived
AI runner owns the cache with its embedder; runner shutdown cancels unfinished
cache work. Direct one-shot CLI calls print their search result before waiting
for queued fills to finish, so the process can exit without losing pending
cache work. Interactive one-shot tool calls drain asynchronously when their
embedder is closed. Query vectors remain foreground work and are not persisted.
Cache read, write, and embedding errors are reported through the existing
embedding diagnostics.

The older Git-backed file index remains a separate storage API; `list-files`
uses the absolute-path store instead.

## Validation plan

### Cache behavior tests

Use temporary directories and a temporary cache with a deterministic fake
embedder injected into the `list-files` path. These tests run regardless of
whether LiteRT-LM artifacts are installed.

Implement these as focused tests rather than one test that only checks that
search returns output. Seed mixed cache states explicitly; do not assume that
all files in a directory are cold or all are warm.

1. **Mixed hit, miss, and stale search.** Seed a valid hit, leave a second
   path absent, and seed a third path with stale metadata. Assert the valid
   vector is used in the immediate result, only absent and stale candidate
   paths are queued, and the stale vector is never used. Filename semantic
   searches also cache candidate directories.
2. **Non-blocking result.** Hold the fake embedder after the misses are
   queued. Assert that the initial search result arrives before the embedder
   is released and includes the usable cached matches.
3. **Background completion and later reuse.** Release the fake embedder,
   verify each successful vector is stored under its absolute path, and
   repeat the search. Assert the newly populated entries are now hits and
   produce the expected fuller ranking.
4. **Warm repeat and query accounting.** Repeat with all candidate entries
   valid. Assert no candidate-file embedding calls occur. Account separately
   for the per-query embedding, which is not cached.
5. **Cache survives reopening.** Reopen the cache after background work
   completes. Assert its vectors are reused by the next search.
6. **Metadata invalidation is selective.** Change each metadata field chosen
   for the fingerprint in table-driven cases. Assert only the changed path
   becomes stale while other paths remain usable. Include same-size content
   edits with a changed timestamp; do not make a content hash part of the
   validity check.
7. **Absolute paths do not alias.** Place same-named and identical-content
   files in distinct directories. Assert distinct entries and that changing
   one path does not invalidate the other.
8. **Lexical behavior is unchanged.** Exercise exact, glob, and fuzzy
   filename matches with empty, mixed, and fully populated caches. Assert the
   same lexical results and no unnecessary embedding requests.
9. **Embedding failures do not poison the cache.** Test backend errors,
   empty or malformed vectors, and cache read/write errors. Assert failed
   results are not treated as hits and follow the selected error behavior.
10. **Queue lifetime and shutdown.** Cancel the initiating search while
    work is pending and verify cache filling continues. Shut down the queue
    and assert that unfinished results are not stored as valid.
11. **Files changing during work are safe.** Block the fake embedder, mutate
    the source metadata, then release it. Assert the old result is not marked
    valid for the changed file.
12. **Batching, priority, and deduplication are observable.** Record batch
    contents and start order. Assert misses are batched within the declared
    limit, foreground work outranks background fill, and concurrent requests
    for the same path do not trigger duplicate calculations.
13. **Embedding modes remain distinct.** Run filename and content semantic
    searches over the same mixed cache state. Verify each mode uses its own
    candidate input and vector, and that completing one mode does not turn a
    miss in the other mode into a false hit.

Use a controllable fake embedder to make calls, vectors, failures, delays,
batch boundaries, and completion order deterministic. Keep these tests
independent of real model weights and the native runtime.

The current fake-embedder tests cover mixed filename hit/stale/miss state,
non-blocking return, background fill, warm reuse, filename/content separation,
cache reopening, selective metadata invalidation, and rejection/reporting of
empty vectors. They also verify a changed metadata version is queued while the
prior version is still pending, and that only the current version is persisted.
Queue cancellation, batch-priority/deduplication, and cache read/write failure
cases remain to be covered.

### Cached real-embedder tests

Keep the low-level real-runtime suite as a baseline, and add an end-to-end
real-embedder suite for `list-files`. Both must be cache-only: neither test
may download or otherwise fetch prerequisites.

Keep the existing low-level baseline as its own cached-artifact-gated test.
For the new `list-files` acceptance checks, use one top-level gated test that
checks the already-cached LiteRT-LM runtime and embedding weights once. If any
prerequisite is missing or corrupted, report each specific diagnostic
(including whether the runtime wheel/library or weights are missing or
corrupted) and finish green as a single diagnostic-only pass. If all
prerequisites are valid, run the named real-embedder subtests below. Do not
create a separate stub test for each missing artifact or stop after merely
asserting that `list-files` returned output.

The low-level baseline exercises:

1. **Real semantic grouping.** Embed duck, chicken, iron, and copper; compute
   the full cosine-similarity matrix; assert duck/chicken and iron/copper are
   each other's closest cross-pair matches.
2. **Warm single-input latency.** Embed London, Paris, Tokyo, and Lagos
   sequentially. Log every duration and require each call after the first to
   complete in under one second.
3. **Real batches.** Embed four currency terms, then four color terms, as
   separate batches. Log both durations, check all vectors, and require the
   second batch to complete in under three seconds.
4. **Long-input latency.** Warm with a 4,915-byte source slice, then embed a
   same-sized slice from the test source. Log both durations and require the
   measured warm slice to complete in under three seconds.

The end-to-end `list-files` suite uses this explicit fixture for filename and
content semantic searches:

| Relative path | File contents |
|---|---|
| `avian/duck.txt` | `A duck is a waterfowl with webbed feet.` |
| `avian/chicken.txt` | `A chicken is a domesticated bird with feathers and a beak.` |
| `materials/iron.txt` | `Iron is a metal used to make steel.` |
| `materials/copper.txt` | `Copper is a conductive metal used in electrical wiring.` |

For filename search, query `waterfowl`; assert it reaches semantic filename
ranking and ranks `avian/duck.txt` ahead of the metal files. For content
search, query `duck waterfowl`; assert it reaches semantic content ranking
(the full query does not occur literally in any file) and ranks
`avian/duck.txt` ahead of the unrelated metal files. In both cases, assert
the reported paths, finite scores, and the expected semantic mode. This
fixture avoids relying on the output merely being nonempty.

Seed and validate entries independently by absolute path and semantic mode;
do not treat either search as an all-cold or all-warm operation.

1. **Mixed-state filename search.** Seed valid filename vectors for
   `avian/duck.txt` and `materials/iron.txt`, leave
   `avian/chicken.txt` absent, and give `materials/copper.txt` stale metadata.
   Search for `waterfowl`. Assert the call returns without waiting for the
   missing vectors, uses the valid hits in its available result, and queues
   only absent and stale file entries plus uncached directory candidates.
   Log response latency.
2. **Filename cache completion.** Wait for background work using an explicit
   queue-idle/test synchronization hook, not a timing sleep. Search again.
   Assert the full expected ranking, no candidate inference for now-valid
   entries, and log the subsequent-search latency.
3. **Mixed-state content search.** Seed a valid content vector for
   `avian/duck.txt`, leave `avian/chicken.txt` absent, and give
   `materials/copper.txt` stale metadata. Search for `duck waterfowl`.
   Assert the available result uses the valid content vector, the call does
   not wait for missing vectors, only missing/stale content entries are
   queued, and filename vectors are not used as content hits. Log response
   latency.
4. **Content cache completion.** Wait for background completion and repeat
   the content query. Assert the full expected ranking, no candidate
   inference for valid content entries, and log the subsequent-search
   latency.
5. **Reopened-cache searches.** Reopen the persistent cache after background
   work completes and run both queries. Assert filename and content vectors
   are reused independently and both result lists remain stable.
6. **Selective real invalidation.** Change the size and timestamp of
   `avian/duck.txt`, search again in both modes, and assert its respective
   stale vectors are queued while unchanged paths remain usable. After
   background completion, verify the new vectors are stored with the new
   metadata.
7. **Batch and timing path.** Record actual embedder calls through a
   transparent counting wrapper. Assert background misses use supported
   batches, foreground work takes priority over background fills, and
   per-stage timings are logged.

The real end-to-end checks use the actual default embedding model and
`list-files`, with a temporary corpus and cache. They are gated by cached
runtime/model artifacts and never download prerequisites. The current suite
covers mixed filename and content states, background completion, warm reuse,
cache reopening, metadata invalidation, finite reported scores, and observed
batch sizes. It runs as a diagnostic-only pass when the runtime or weights are
unavailable; the separate low-level acceptance suite remains unchanged.

Queue cancellation, batch-priority/deduplication, source mutation while a
background embedding is running, cache read/write failure injection, and
explicit real-embedder priority assertions remain additional test coverage,
not implementation decisions.
