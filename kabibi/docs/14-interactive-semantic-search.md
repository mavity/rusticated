# Interactive Semantic File Search

> Historical implementation plan. Its `list_files` and `ai list-files`
> references describe the earlier interface and are superseded by
> [`find_files` API](../../ai/docs/16.1-find-files-tool-api.md). The current
> tool and CLI do not accept the old name or options.

## Goal

Extend the existing `ai list-files` command to support interactive TUI search
progress, using the existing shared file-search implementation. During semantic
ranking, the TUI must show a live, bounded list of the best matches and the file
currently being assessed. Keep `ai list-files` as the search entry point. The
AI tool and non-interactive CLI invocation do not render TUI progress. Both
callers and the TUI use shared match formatting and include search statistics.

This is a refactor of the shared file-search core plus a TTY-specific caller
branch for the existing command, not a second implementation of file search.

## Current state

- `kabibi/tools/filelist.ListFiles` owns query normalization, traversal,
  glob/fuzzy matching, semantic filename ranking, semantic content ranking,
  and final text formatting.
- `kabibi/ui/shell/tools_file.go` calls it for the AI `list_files` tool and the
  backward-compatible `list_dir` alias.
- `list_files` supports `query`, `search_content`, and `max_results`.
- `ListFiles` drains a shared search iterator; semantic ranking uses bounded
  batches and maintains a bounded top-match set as candidate scores arrive.
- Semantic content ranking currently embeds a bounded sample of each candidate
  file. It does not yet extract a relevant quote from the best line.
- The existing `ai list-files` command invokes the registered AI tool; there is
  no separate TUI search command.

Relevant implementation: [`filelist.go`](../tools/filelist/filelist.go),
[`tools_file.go`](../ui/shell/tools_file.go), and
[`app_widget.go`](../ui/app/app_widget.go).

## User experience

1. Keep the existing `ai list-files` / `ai list_files` command, option names,
   aliases, and parameter schema. Do not add a separate search command or slash
   command.
2. In the interactive TUI, the parameter dialog collects the existing
   `list_files` parameters: query, `search_content`, and `max_results`. Use the
   existing tool schema, defaults, and validation; committing the dialog runs
   the existing command path with those values.
3. Use the interactive progress path only for canonical `list_files` when the
   shell command has an attached TTY. Parse options using the existing tool
   schema and resolve the configured embedder as today, then run the shared
   search iterator directly. Search the current shell working directory.
4. AI-tool execution and non-TTY/one-shot invocations remain silent consumers
   of the iterator through the `ListFiles` compatibility wrapper. They receive
   no TUI progress output.
5. While semantic candidates are being assessed, render a replace-in-place
   progress view containing:
   - the number of top matches currently shown;
   - the number of eligible files assessed so far (and total, when known);
   - an approximate elapsed time per assessed file;
   - the current file being assessed; and
   - the current top matches, each with its semantic score, path, size, and
     relevant one-line quote when available.

   Use one shared match formatter for progress rows and final results. Put the
   similarity score first when one exists, followed by path, size, and quote.
   Reuse a literal content-match line for non-semantic content results; do not
   invent a cosine score for glob, exact filename, or fuzzy matches. Use the
   actual TTY width for TUI output and a reasonable fixed maximum for piped/AI
   output, truncating long rows with an ellipsis.

   For semantic matches, split candidate text into embedder-sized chunks on
   whole-line boundaries. Reuse an existing chunk embedding where its input
   matches exactly. Otherwise compare chunks with the query embedding, then
   bisect the best chunk at whole-line boundaries until one line remains.
   This is a bounded heuristic, not a guarantee of the globally closest line.
   Route embedding requests through a bounded, context-aware scheduler with a
   blocking `Embed(ctx, text, lowPriority, order)` API. `lowPriority` is a boolean;
   there are exactly two buckets, high and low. When both buckets have work,
   schedule exactly four high-priority requests for each low-priority request
   (initial ratio 4:1; keep the ratio easy to tune). If one bucket is empty,
   serve the other. A live quote task owns a stable order handle whose rank is
   refreshed whenever the visible match list changes. Re-evaluate queued low
   priority work at dispatch time using rank-weighted accumulated demand, with
   enqueue order as a tie-breaker. Return to the scheduler between quote chunk
   and bisection requests so each embedding is scheduled fairly against waiting
   work.

   Queue and deduplicate quote work only for visible TUI top matches. Give each
   match's quote task a child context and cancel it when the match leaves the
   displayed top set. The caller owns this cancellation; the scheduler remains
   generic and accepts text, context, the low-priority flag, and an optional
   order handle. A canceled queued request should be discarded, and a canceled
   in-flight request must not publish its result. Treat that cancellation
   locally as an obsolete quote, not as failure of the overall search. Search-level cancellation
   (including Ctrl+C) cancels both scoring and quote requests. AI/non-TTY output
   may enrich its returned top `max_results` matches after scoring completes.

   Use a presentation similar to:

   ```text
   Top N matches, viewed Y, approximately Z ms per file:
   - path/file1 [semantic: 0.79]
   - path/file2 [semantic: 0.60]
   Matching: currently-matching-file
   ```

6. Consume every iterator update, but do not read/copy the current state or
   request a redraw for every update. On the first eligible update, and then no
   more frequently than once every 500 ms, read the latest iterator state and
   publish it to the UI. Coalesce intermediate updates into the latest pending
   state rather than dropping them: when the next publication is allowed, it
   must include any completed quote still present in the top set. Flush the
   latest state once on completion before clearing progress and showing the
   final result. On Ctrl+C or Escape cancellation, retain the last published
   progress snapshot in the command block after the search stops. Fast searches
   may show only an initial progress state followed by the final result.
   Keep the processed/total file count and average milliseconds per scored file
   visible during search and append the same statistics to the final AI and TUI
   results. Measure candidate-scoring time separately from query embedding and
   quote extraction.
7. Respect the terminal's available height. Show up to the requested
   `max_results`, but reduce the number of progress-list rows when necessary
   to leave room for the status and current-file lines. Truncate long paths to
   the available width without corrupting terminal styling. Cap final TUI
   results so the complete command output fits in its output frame, leaving
   three rows free; do not apply this display cap to AI or piped output.
8. Publish typed progress state through a bounded, replaceable latest-value
   handoff. Never queue one item per iterator update. Each publication may
   request a redraw; host redraw coalescing is useful but is not the rate
   limiter.
9. Keep progress updates replaceable and rate-limited, but render them through
   the command's ordinary stdout stream. The TTY command formats the latest
   typed snapshot as rows and uses ANSI cursor movement and line erasure to
   redraw those rows in place. The normal live terminal parses that output and
   the ordinary command block handles measurement, plume ejection, and
   scrollback. Do not mutate shell or terminal state directly, and do not use
   alternate-screen or full-screen clear controls.
10. On iterator completion, obtain `Result()`, erase the progress rows, and send
    the normal final Markdown result through stdout exactly once. On
    non-cancellation error, erase progress and surface the explicit failure;
    never treat partial matches as a successful final result. On cancellation,
    retain the last progress frame while surfacing the cancellation result.
11. Tie iterator context to the running command context and defer `Close()`.
    Ctrl+C and Escape cancel `ai list-files`; keep its last published snapshot
    visible after the command stops. Other exit paths clear transient progress.

The blue-panel shortcuts `Ctrl+F`, `Cmd+F`, `Alt+F7`, and `Mod+F7` should open
the same `ai list-files` parameter flow rather than implement separate search
behavior. They must not trigger when the panels are hidden.

## Shared search-core design

### Progress and iterator contract

Expose shared search as an iterator with an `Updates()` stream, a current-state
accessor, and a final `Result()`. Each update is a lightweight signal, not a
copy of the current match list. The TUI consumes every signal but only calls
the state accessor when publishing a progress update. The accessor returns a
coherent current-state snapshot containing:

- assessed-candidate count;
- total eligible-candidate count when available;
- current candidate path;
- elapsed time and/or average time per assessed candidate; and
- the current top-ranked matches, including each path and final score.

The current-match accessor always returns the latest top-ranked list, not the
matches from an older update and not every candidate assessed so far. The
iterator should keep its internal hot list bounded by the requested result
count; callers choose how much of that list to display.

The iterator has a distinct completion/error result. Once exhausted, the
consumer obtains the normal final `ListFiles` result; progress notifications
must not be mistaken for completed search results.

Keep TUI widgets, ANSI formatting, and terminal dimensions out of the
`filelist` package. The existing `ListFiles` API remains as a compatibility
wrapper that drains the iterator silently and returns the same final result
and errors. Registered AI-tool execution, piped shell execution, and one-shot
execution use that wrapper and do not render progress.

The iterator must honor its search context while advancing. Cancellation must
interrupt traversal and embedding through that context. Tie any update
producer to the iterator's context; abandoning or closing an iterator must
cancel its work, close its update stream, and not leave a producer blocked or
running. Keep terminal rendering and cancellation controls in the caller, not
the search core.

The search core should maintain a hot top-results set as candidate scores
become available. A candidate enters that set only after its score is final;
then it may displace a lower-ranked candidate. Do not discard an unscored
candidate or use a provisional score to exclude it from later ranking.
Maintain at most the requested result count in the hot set. The TUI may render
a smaller prefix according to its available height, but that display limit
must not reduce the search candidate set or the final result count.

Progress snapshots and the final result must use the same score threshold and
ranking order. Keep tie ordering consistent between progress and final output.

### Incremental embedding

Semantic ranking must publish results while candidate scoring is in progress,
not only after one opaque all-candidate batch has returned. Preserve embedding
batching where practical by using bounded batches: score each returned batch,
update the hot set, and make progress available before processing the next
batch. Use individual candidate calls where the embedder cannot provide
incremental batches. Do not submit the complete candidate set as one batch
when doing so prevents live updates.

Compute the query embedding once. Report a candidate as current while it is
being read/embedded, then increment the assessed count when its score is
available. Calculate the approximate per-file duration from semantic candidate
assessment time; do not count query embedding as an assessed file.

Yield a lightweight update signal through `Updates()` whenever observable
search state changes, including when a candidate becomes current and when its
final score updates the hot list. Do not debounce these signals in the search
core. The signal must not copy or materialize the hot list.

The interactive caller consumes all update signals but rate-limits publication
to the UI: on the first eligible update, and then at most once per 500 ms, it
reads the latest state and replaces the pending progress snapshot. The
latest-value handoff must be bounded and replaceable, not an unbounded queue.
Updates during the throttle interval mark the state dirty; publish the latest
dirty snapshot at the next allowed time, and flush it once on completion.
Redraw coalescing in the host does not replace this publication throttle.

### Prioritized embedding and quote cancellation

Use a bounded concurrent embedder scheduler behind a context-aware blocking
API such as `Embed(ctx, text, lowPriority, order)`. The low-priority flag is boolean;
do not introduce multiple priority levels. Scoring passes `false` (high
priority), and relevant-quote work passes `true` (low priority).

When both buckets have queued requests, schedule strictly four high-priority
requests for every one low-priority request (initial 4:1 ratio, with the ratio
easy to tune). If one bucket is empty, use the other. Within the selected
bucket, choose the text chunk with the greatest accumulated outstanding
request weight. High-priority requests contribute one unit each. Low-priority
requests with an order handle contribute `1 / (rank + 1)` each; requests without
a handle contribute one unit. This initial rank-weighting formula is tunable.
Re-read current ranks when selecting queued work, so a promoted match's
outstanding quote requests can move ahead of lower-ranked matches. Use enqueue
order to break ties.

Track that accumulated weight per text chunk and per request context, separately
for the high and low buckets. A quote task keeps a stable order handle and
updates its rank from `liveQuoteManager.sync`; every queued embedding request
from that task observes the latest rank. Coalesce identical role-and-text
requests into one embedding operation and deliver its result to every
non-canceled waiter for that chunk.
When a request completes or its context is canceled, remove only that
request-context's contribution. If the context is canceled, discard its queued
requests; if the same chunk still has outstanding requests under other
contexts, retain and schedule that remaining demand. An in-flight result for a
canceled request must not be delivered to its canceled waiter. This prevents
obsolete quote work from retaining scheduling priority while preserving
non-canceled callers' interest in a shared chunk.

The scheduler does not distinguish quote requests from scoring requests for
cancellation purposes. The search caller owns per-quote child contexts and
cancels a quote's context when its file is displaced from the displayed top
set. This cancels that quote's queued work and makes its wait return a
cancellation error; the quote operation unwinds and drops the obsolete result.
That per-quote cancellation must not cancel scoring or fail the overall search.
Only search-level cancellation, such as Ctrl+C, cancels scoring as well as all
quote work. If an embedding is already in flight, its eventual result must be
ignored after its context is canceled.

Do not try to kill or abandon suspended Go goroutines. Quote extraction remains
sequential within its task and returns normally on cancellation. Bound the
number of quote workers and pending quote jobs; deduplicate by current match
identity and discard queued work for matches no longer displayed.

### Preserve search behavior

- Keep the existing glob, exact, fuzzy, semantic-fallback, and content-search
  precedence.
- Keep current ignore handling, candidate eligibility, walk bounds, content
  size/binary checks, similarity threshold, and embedding policy unchanged.
- Do not add a lexical shortlist, prefilter, or other recall-reducing candidate
  gate as part of this work.
- Keep AI `list_files`, `list_dir`, and one-shot `kabibi ai list_files ...`
  callers on the shared implementation. They must not receive TUI progress
  text or ANSI output; their result formatting remains unchanged.
- Honor context cancellation throughout scoring and progress delivery. Surface
  embedding and filesystem errors through the existing error path.
- Publish typed progress through the iterator's latest-state notifications.
  The interactive command writes ANSI redraws to stdout; the standard command
  stream carries them into the live terminal. Do not mutate `liveOutput.raw`,
  the virtual terminal, or shell UI state directly from the search goroutine.
- Erase progress on completion and non-cancellation error; on cancellation
  leave the last ANSI-rendered frame visible. Append the final normal result
  only on successful completion, exactly once.
- Preserve the repository's async-I/O and custom-sysroot build requirements;
  do not introduce host-command dependencies or new blocking I/O paths.

Fast glob, exact, or fuzzy paths that do not enter semantic ranking need not
show per-file semantic progress. The TUI should still display their completed
result normally.

## TTY ownership and implementation areas

The interactive `ai list-files` command owns the latest typed progress snapshot
and formats it as ANSI redraws on the command's stdout. The existing command
stream feeds those bytes to the block's live terminal, which interprets cursor
movement and line erasure during its ordinary render cycle. This keeps progress
in the normal block layout, measurement, plume ejection, and scrollback path
without a list-files-specific shell renderer or UI invalidation hook. Rate-limit
progress publication before writing; host redraw coalescing is not the rate
limiter.

Expected code areas:

- `tools/filelist/filelist.go`: implement the shared incremental search
  iterator and keep `ListFiles` as the silent compatibility wrapper.
- `ui/shell/tools_file.go`: keep the registered AI tool on `ListFiles`; do not
  format or publish TUI progress from the tool handler.
- `ui/shell/ai_onboarding.go`: in `runAIBuiltinTool`, add the TTY-only direct
  iterator path for canonical `list_files`; parse with the existing schema,
  resolve the embedder through the existing policy, and leave all other tools
  and non-TTY calls on `executeAITool`. Pass the command's stdout and terminal
  dimensions to the progress path.
- `ui/shell/ai_list_files_output.go`: format progress rows and emit ANSI
  cursor/line operations through the provided command stdout writer.
- `ui/shell/ai_tool_command.go` and the AI session/tool context: remove
  list-files progress callback plumbing. Remove a generic `ToolContext.Progress`
  field or optional callback parameter only if no other code uses it.
- `ui/shell/shell_widget.go`: no list-files-specific rendering hooks; verify
  the existing live-terminal path renders the command's ANSI output normally.
- Replace alternate-screen progress tests with focused coverage for iterator
  consumption, throttled latest-value publication, block rendering/ejection,
  final output, cancellation, and errors (for example, in
  `ui/shell/ai_progress_test.go`).

## Acceptance criteria

### Search-core tests

- With a delayed fake embedder and multiple candidates, the iterator yields at
  least one update signal before all candidates have been scored.
- Update signals do not carry copies of the hot list; reading the iterator's
  current state reports the latest current candidate, assessed count, and
  top-ranked list.
- The hot match list is ordered, bounded by `max_results`, and updated as
  better scores arrive; a newly higher-ranked candidate displaces a lower
  match.
- Progress and final output agree on scores, threshold, and ranking.
- Draining the iterator through `ListFiles` returns the same results as before
  and emits no progress output.
- Cancelling or closing the iterator stops its work without leaving a producer
  blocked or running in the background.
- Cancellation, embedding errors, empty vectors, and vector-count mismatches
  remain explicit errors and do not produce success-shaped results.
- Tests establish that semantic recall is not reduced by a new prefilter or
  shortlist.

### TUI tests

- The interactive TTY `ai list-files` flow uses the existing parameter schema,
  validates the parameters, and runs in the current shell directory; no
  separate search command or slash-command alias is registered.
- The interactive caller consumes all iterator signals but publishes progress
  at most once per 500 ms after the first eligible update, using latest-value
  replacement rather than an unbounded queue.
- Search progress replaces itself instead of accumulating output and includes
  top matches, assessed count, approximate milliseconds per file, current path,
  and any quotes already computed for visible matches.
- A controlled embedder test proves a visible quote can complete while file
  scoring is still underway, the strict 4:1 high/low schedule and within-bucket
  rank-weighted accumulated demand are honored, rank changes reorder queued
  quote work, demoted matches remove only their own context's contribution, and
  Ctrl+C cancels both.
- Progress publication remains capped at 2 Hz without losing a completed quote
  that remains in the latest top-match state, including a final pending update.
- AI and TUI final match rows share score/path/size/quote formatting, and final
  results include processed-file and per-file timing statistics.
- Quote extraction follows line-boundary chunks and bisection, reuses matching
  scored chunks when possible, and does not delay candidate scoring. TUI quote
  work is limited to the currently displayable top matches.
- Progress is emitted as ANSI through command stdout, interpreted by the
  command's live terminal, and follows ordinary plume ejection/scrollback. The
  final normal result appears exactly once.
- Iterator update signals are not throttled by the search core and do not carry
  copies of the hot list.
- The displayed top count respects both `max_results` and available terminal
  height; resizing does not change the search's actual candidate set.
- Completion clears progress and renders final results. Cancellation and errors
  are visibly distinguished from successful completion.
- Ctrl+C or Escape cancellation stops iterator work and retains the last
  published progress snapshot; normal completion and non-cancellation errors
  clear progress.
- The requested blue-panel shortcuts open the same `ai list-files` search flow
  only while the panels are visible.

### Caller compatibility and end-to-end validation

- Existing AI-tool tests continue to verify the unchanged Markdown result and
  confirm that no TUI progress is mixed into tool output.
- Piped and one-shot invocations continue through `ListFiles` and emit no
  progress.
- Progress is emitted through child-command `stdout` and interpreted by its
  live terminal; the command does not mutate `liveOutput.raw` or virtual
  terminal state directly. It uses ordinary plume ejection/scrollback and no
  alternate-screen or full-screen clear controls.
- Run the existing focused `filelist`, shell/tool, and app tests, plus the
  repository's documented Rusticated compile/test path.
- Manually run a semantic content search in the TUI using the normal configured
  profile and a query with a known relevant file in the repository. Verify
  that live rankings appear before completion and the final results are
  meaningful. An empty or altered profile and lexical-only matches are not
  evidence that semantic search works.
