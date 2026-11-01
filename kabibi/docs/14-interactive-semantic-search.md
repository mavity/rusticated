# Interactive Semantic File Search

## Goal

Extend the existing `ai list-files` command to support interactive TUI search
progress, using the existing shared file-search implementation. During semantic
ranking, the TUI must show a live, bounded list of the best matches and the file
currently being assessed. Keep `ai list-files` as the search entry point. The
AI tool and non-interactive CLI invocation keep their existing result format
and do not render TUI progress.

This is a refactor of the shared file-search core plus a TTY-specific caller
branch for the existing command, not a second implementation of file search.

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
   of the iterator through the `ListFiles` compatibility wrapper. Their result
   format remains unchanged and receives no progress output.
5. While semantic candidates are being assessed, render a replace-in-place
   progress view containing:
   - the number of top matches currently shown;
   - the number of eligible files assessed so far (and total, when known);
   - an approximate elapsed time per assessed file;
   - the current file being assessed; and
   - the current top matches, each with its semantic score.

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
   publish it to the UI. Fast searches may show only an initial progress state
   followed by the final result.
7. Respect the terminal's available height. Show up to the requested
   `max_results`, but reduce the number of progress-list rows when necessary
   to leave room for the status and current-file lines. Truncate long paths to
   the available width without corrupting terminal styling.
8. Publish typed progress state through a bounded, replaceable latest-value
   handoff. Never queue one item per iterator update. Each publication may
   request a redraw; host redraw coalescing is useful but is not the rate
   limiter.
9. Render progress as ephemeral UI state separate from captured command output
   and history. It may be attached transiently to the active command block, but
   must not pass through command `stdout`, `liveOutput.raw`, or the virtual
   terminal. Do not use alternate-screen controls or clear-screen escape
   sequences.
10. On iterator completion, obtain `Result()`, clear ephemeral progress, and
    send the normal final Markdown result through the existing command-output
    path exactly once. On cancellation or error, clear progress and surface the
    explicit failure/cancellation result; never treat partial matches as a
    successful final result.
11. Tie iterator context to the running command context, defer `Close()`, and
    clear transient progress on every exit path. Existing Ctrl+C cancellation
    must stop search work and remove stale progress.

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
Redraw coalescing in the host does not replace this publication throttle.

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
- Publish typed progress through the shell's UI update mechanism; do not write
  progress to command stdout, captured command output, `liveOutput.raw`, or the
  virtual terminal, and do not mutate UI state directly from a search goroutine.
- Clear ephemeral progress on completion, error, and cancellation; append only
  the final normal result to captured command output, exactly once.
- Preserve the repository's async-I/O and custom-sysroot build requirements;
  do not introduce host-command dependencies or new blocking I/O paths.

Fast glob, exact, or fuzzy paths that do not enter semantic ranking need not
show per-file semantic progress. The TUI should still display their completed
result normally.

## TTY ownership and implementation areas

The current TTY progress path writes formatted status text to command stdout.
That output is captured by `ExecutionSession.RunCommand`, appended to the
command block's live output, and fed to the block's virtual terminal.
`renderLiveBlock` renders that terminal snapshot, including scrollback.
Therefore progress written to stdout is command output, not transient UI state;
clear-screen sequences do not change that ownership. Do not solve this by
writing ANSI control sequences or changing the virtual terminal's contents.

Use the existing shell invalidation/update mechanism to publish transient
progress separately from captured command output. Host redraw coalescing remains
useful, but rate-limit progress publication before requesting UI updates.

Expected code areas:

- `tools/filelist/filelist.go`: implement the shared incremental search
  iterator and keep `ListFiles` as the silent compatibility wrapper.
- `ui/shell/tools_file.go`: keep the registered AI tool on `ListFiles`; do not
  format or publish TUI progress from the tool handler.
- `ui/shell/ai_onboarding.go`: in `runAIBuiltinTool`, add the TTY-only direct
  iterator path for canonical `list_files`; parse with the existing schema,
  resolve the embedder through the existing policy, and leave all other tools
  and non-TTY calls on `executeAITool`.
- `ui/shell/ai_tool_command.go` and the AI session/tool context: remove
  list-files progress callback plumbing. Remove a generic `ToolContext.Progress`
  field or optional callback parameter only if no other code uses it.
- `ui/shell/shell_widget.go`: hold the latest typed progress snapshot as
  transient active-block state, render it separately from command output, and
  clear it on every completion path.
- Replace alternate-screen progress tests with focused coverage for iterator
  consumption, throttled latest-value publication, ephemeral rendering, final
  output, cancellation, and errors (for example, in
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
  top matches, assessed count, approximate time per file, and current path.
- Progress is ephemeral UI state and never enters command output, scrollback,
  or the virtual terminal. The final normal result appears exactly once.
- Iterator update signals are not throttled by the search core and do not carry
  copies of the hot list.
- The displayed top count respects both `max_results` and available terminal
  height; resizing does not change the search's actual candidate set.
- Completion replaces progress with final results. Cancellation and errors
  are visibly distinguished from successful completion.
- Ctrl+C cancellation clears the transient progress and stops iterator work;
  no stale progress remains after any exit path.
- The requested blue-panel shortcuts open the same `ai list-files` search flow
  only while the panels are visible.

### Caller compatibility and end-to-end validation

- Existing AI-tool tests continue to verify the unchanged Markdown result and
  confirm that no TUI progress is mixed into tool output.
- Piped and one-shot invocations continue through `ListFiles` and emit no
  progress.
- Progress never enters `stdout`, `liveOutput.raw`, virtual-terminal contents,
  command output, or scrollback; no alternate-screen or clear-screen controls
  are used.
- Run the existing focused `filelist`, shell/tool, and app tests, plus the
  repository's documented Rusticated compile/test path.
- Manually run a semantic content search in the TUI using the normal configured
  profile and a query with a known relevant file in the repository. Verify
  that live rankings appear before completion and the final results are
  meaningful. An empty or altered profile and lexical-only matches are not
  evidence that semantic search works.
