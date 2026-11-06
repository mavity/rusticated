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
embedding vectors only. Query embeddings remain per-search work and are not
stored by this cache.

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
   compute only missing candidate vectors.
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