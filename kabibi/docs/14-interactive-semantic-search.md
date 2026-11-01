# Interactive Semantic File Search

## Goal

Extend the existing `ai list-files` command to support interactive TUI search
progress, using the existing shared file-search implementation. During semantic
ranking, the TUI must show a live, bounded list of the best matches and the file
currently being assessed. Keep `ai list-files` as the search entry point. The AI tool and non-interactive CLI invocation
keep their existing result format and do not render TUI progress.

This is a refactor of the shared file-search core plus a new TUI caller, not a
second implementation of file search.

## Current state

- `kabibi/tools/filelist.ListFiles` owns query normalization, traversal,
  glob/fuzzy matching, semantic filename ranking, semantic content ranking,
  and final text formatting.
- `kabibi/ui/shell/tools_file.go` calls it for the AI `list_files` tool and the
  backward-compatible `list_dir` alias.
- `list_files` supports `query`, `search_content`, and `max_results`.
- Semantic ranking currently embeds all candidates through
  `ai.EmbedBatchWithContext`, then filters and sorts the complete score set.
  The batch API returns all vectors at once, so the current implementation
  cannot publish genuine per-file ranking progress.
- The existing `ai list-files` command invokes the registered AI tool; there is
  no separate TUI search command.

Relevant implementation: [`filelist.go`](../tools/filelist/filelist.go),
[`tools_file.go`](../ui/shell/tools_file.go), and
[`app_widget.go`](../ui/app/app_widget.go).

## User experience

1. Use the existing `ai list-files` command as the sole search entry point; do
   not register a new slash command. Preserve its existing option-based
   invocation.
2. The interactive TUI flow opens a parameter dialog before starting the
   search. It collects the existing `list_files` parameters: query,
   `search_content`, and `max_results`. Use the same defaults and validation as
   the AI tool rather than introducing a separate search contract.
3. Run the search against the current shell working directory and the
   configured embedder through the same registered `list_files` tool and
   shared `filelist` implementation.
4. While semantic candidates are being assessed, render a replace-in-place
   progress view containing:
   - the number of top matches currently shown;
   - the number of eligible files assessed so far (and total, when known);
   - an approximate elapsed time per assessed file;
   - the current file being assessed; and
   - the current top matches, each with its semantic score.
5. Use a presentation similar to:

   ```text
   Top N matches, viewed Y, approximately Z ms per file:
   - path/file1 [semantic: 0.79]
   - path/file2 [semantic: 0.60]
   Matching: currently-matching-file
   ```

6. Refresh progress no more often than twice per second during active ranking.
   The initial state may be shown when ranking starts; the completed result
   replaces the progress view when the command finishes.
7. Respect the terminal's available height. Show up to the requested
   `max_results`, but reduce the number of progress-list rows when necessary
   to leave room for the status and current-file lines. Truncate long paths to
   the available width without corrupting terminal styling.
8. When search completes, show the normal final `ListFiles` result. On
   cancellation or error, report that state explicitly; do not present partial
   matches as a completed successful search.

The blue-panel shortcuts `Ctrl+F`, `Cmd+F`, `Alt+F7`, and `Mod+F7` should open
the `ai list-files` parameter flow rather than implement separate search
behavior. They must not trigger when the panels are hidden.

## Shared search-core design

### Progress and iterator contract

Expose shared search as a pull-based iterator (or equivalent typed event
sequence). Each iteration yields only a lightweight update signal, not a copy
of the current match list. After an update, the consumer calls a method on the
iterator to read its current state. That state includes:

- assessed-candidate count;
- total eligible-candidate count when available;
- current candidate path;
- elapsed time and/or average time per assessed candidate; and
- the current top-ranked matches, including each path and final score.

The current-match accessor always returns the latest top-ranked list, not the
matches from an older update and not every candidate assessed so far. Return a
coherent snapshot so the consumer can render it without observing mutation
mid-read. The iterator should keep its internal hot list bounded by the
requested result count; callers choose how much of that list to display.

The iterator has a distinct completion/error result. Once exhausted, the
consumer obtains the normal final `ListFiles` result; progress notifications
must not be mistaken for completed search results.

Keep TUI widgets, ANSI formatting, and terminal dimensions out of the
`filelist` package. The existing `ListFiles` API remains as a compatibility
wrapper that drains the iterator silently and returns the same final result
and errors. AI-tool and piped callers use that wrapper (or equivalently drain
updates without rendering them), so their output remains unchanged.

The iterator must honor its search context while advancing. Cancellation must
interrupt traversal and embedding through that context; abandoning or closing
an iterator must not leave background work running. Keep terminal rendering
and cancellation controls in the caller, not the search core. Prefer a pull
contract that needs no producer goroutine; if an implementation uses a
background producer, it must ensure cancellation and early consumer exit
cannot leak the producer or block it indefinitely.

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

Yield a lightweight update signal whenever observable search state changes,
including when a candidate becomes current and when its final score updates the
hot list. Do not debounce these signals in the search core. The signal must not
copy or materialize the hot list; consumers read a coherent current-state
snapshot only when they need to render or otherwise inspect it.

The TUI is responsible for throttling redraws to at most twice per second while
search is active. It may coalesce pending update signals and read the latest
state at redraw time. If signals cross a goroutine boundary, use bounded or
coalescing delivery so a fast search cannot build an unbounded update queue.

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
- Route iterator updates to the TUI through its event/update mechanism; do not
  mutate UI state directly from a background search goroutine.
- Preserve the repository's async-I/O and custom-sysroot build requirements;
  do not introduce host-command dependencies or new blocking I/O paths.

Fast glob, exact, or fuzzy paths that do not enter semantic ranking need not
show per-file semantic progress. The TUI should still display their completed
result normally.

## Acceptance criteria

### Search-core tests

- With a delayed fake embedder and multiple candidates, the iterator yields at
  least one update signal before all candidates have been scored.
- After each signal, reading the iterator's current state reports the latest
  current candidate, assessed count, and top-ranked list; signals do not carry
  copies of that list.
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

- The interactive `ai list-files` flow opens the parameter dialog, validates
  the existing parameters, and runs in the current shell directory; no
  separate search slash command or alias is registered.
- Search progress replaces itself instead of accumulating output and includes
  top matches, assessed count, approximate time per file, and current path.
- Progress refresh is throttled to no more than twice per second.
- Iterator update signals are not throttled by the search core and do not carry
  copies of the hot list; the TUI coalesces them and reads current state when
  rendering.
- The displayed top count respects both `max_results` and available terminal
  height; resizing does not change the search's actual candidate set.
- Completion replaces progress with final results. Cancellation and errors
  are visibly distinguished from successful completion.
- The requested blue-panel shortcuts open the same `ai list-files` search flow
  only while the panels are visible.

### Caller compatibility and end-to-end validation

- Existing AI-tool tests continue to verify the unchanged Markdown result and
  confirm that no TUI progress is mixed into tool output.
- Run the existing focused `filelist`, shell/tool, and app tests, plus the
  repository's documented Rusticated compile/test path.
- Manually run a semantic content search in the TUI using the normal configured
  profile and a query with a known relevant file in the repository. Verify
  that live rankings appear before completion and the final results are
  meaningful. An empty or altered profile and lexical-only matches are not
  evidence that semantic search works.
