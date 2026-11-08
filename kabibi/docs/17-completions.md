# Kabibi shell completion and hints

**Status:** Specification proposal; implementation is not authorized until this
document is reviewed and approved.

## 1. Purpose

Add syntax-aware completion and hints to Kabibi’s interactive TUI shell prompt.
The same command-argument definitions used to parse and execute supported
builtins must also drive completion. Completion must not maintain a second,
independently authored copy of option or operand syntax.

The feature has two request modes:

1. **Explicit completion:** the user asks for completion with Tab or Ctrl+Space.
2. **Speculative completion:** after one second without an input edit, Kabibi
   searches in the background and displays a non-committing hint.

Completion operates only on the editable shell prompt. It does not intercept
input while a foreground command owns the terminal, while an editor or modal
dialog owns input, or while an existing higher-priority app interaction owns
the key.

## 2. Existing integration points and constraints

- `PromptInput` owns editable text, cursor position, wrapping, and cell rendering.
- `Shell` handles shell submission and pins the prompt above one reserved footer
  row. That footer row is the completion-choice row; completion must not consume
  an additional permanent screen row.
- Shell execution uses the mvdan Bash parser. Completion must use shell syntax
  and cursor position, not a split-on-spaces or regular-expression guess.
- The execution session already keeps an in-memory PATH index with a current
  one-minute maximum age. Completion should extend/reuse that cache rather than
  create a second PATH index.
- Supported commands have multiple owners: mvdan shell builtins, the u-root
  registry, and Kabibi-owned Git, AI, browser, and other middleware.
- With the blue panels visible, Tab currently switches panes. Preserve that
  behavior. Ctrl+Space is the completion shortcut regardless of panel
  visibility.
- Completion searches are advisory. They must not execute a command, mutate the
  shell environment, or alter the command that Enter submits unless the user
  explicitly accepts a candidate.

## 3. Normative parser-source-of-truth requirement

### 3.1 One definition serves execution and completion

Each supported builtin must expose its argument grammar from the same
definition that its runtime argument parser consumes. A completion-only option
table that repeats the builtin’s flag names, aliases, value-taking behavior, or
operand positions is not acceptable.

The implementation may introduce a shared parser-definition/argument-parser
contract. It must support:

- Building a fresh parser definition without executing the command or causing
  command side effects.
- Parsing runtime arguments with the command’s existing behavior.
- Describing registered options, aliases, whether a value is required or
  optional, and the argument position/type where a value is expected.
- Describing positional operands and their order/repetition where the
  underlying argument parser does not represent them.
- Exposing the same syntax definition to the shell completion resolver.

The definition must be safe to instantiate repeatedly. It must not depend on
global mutable flag sets or retain pointers to a previous command invocation.

### 3.2 Adapting existing argument parsers

- **Go `flag` and `pflag`:** refactor inline setup into a side-effect-free
  builder and harvest the options registered in that actual `FlagSet`.
  Preserve aliases, shorthand forms, optional values such as `NoOptDefVal`,
  boolean no-value behavior, and custom flag value types. Do not copy those
  registrations into a separate completion map.
- **Positional operands:** standard flag libraries do not describe the meaning
  or path semantics of `Args()`. Add positional grammar to the shared parser
  definition and make runtime parsing and completion both consume it. Do not
  rely on parsing a help/usage string as the sole source of operand semantics.
- **Custom parsers:** Git, web/CDP, and any other hand-written argument parsers
  must be refactored so the same command grammar drives both argument parsing
  and completion. The runtime parser must not maintain one switch/table while
  the completer maintains another.
- **AI tools:** reuse the existing typed tool definitions and generated
  `ai.Tool` parameter schemas consumed by `parseAIToolArguments`; do not add a
  second tool-option schema for completion.
- **mvdan shell builtins:** include supported builtins in the inventory. Where
  the underlying implementation does not expose argument syntax, amend or wrap
  that parser at its definition site so runtime parsing and completion share
  the syntax. Do not silently omit those builtins or add a completion-only
  approximation.

### 3.3 Semantic value kinds and description heuristics

The parser definition should provide a semantic value kind when the parser
already distinguishes it (for example: boolean, enum, integer, path, file, or
directory). For parsers that only expose a string value, infer path-like
values from the human-readable option/operand description as a fallback.

The fallback should recognize clear path terms such as `file`, `path`,
`directory`, `folder`, `source`, `destination`, `target file`, and `archive`.
It must not classify generic values such as `query`, `pattern`, `text`,
`message`, `name`, or `URL` as paths solely because they are strings. An
explicit parser semantic kind takes precedence over the heuristic.

Descriptions are classification input, not a separately maintained syntax
source. If a description is ambiguous, completion should remain conservative
and offer no filesystem candidates rather than make a likely string value
look path-like.

## 4. Completion context and candidate resolution

### 4.1 Cursor-aware Bash syntax

The resolver receives the complete input, rune-based cursor position, current
working directory, and the execution session’s current environment/state. It
returns a completion context with the token replacement span and one of:

- command name;
- shell keyword;
- option name;
- option value;
- positional argument;
- redirection target;
- unsupported/dynamic context.

Use the Bash AST/token positions to identify the command and argument that
contain the cursor. Recognize command positions after list and pipeline
operators (`;`, `&`, `&&`, `||`, `|`, `|&`), newlines, and command-introducing
reserved words such as `then`, `do`, `else`, and `elif`. Recognize command
substitutions and nested command lists. Do not treat assignment words before a
command as that command’s arguments. Treat redirection operands as paths, not
as command names.

The parser must tolerate incomplete interactive input, including a partial
command word, open quotes, a trailing operator, and a partially typed
redirection. It must not execute expansions to determine completion. When the
syntax cannot be classified safely (for example, a dynamically expanded command
head), return no candidates rather than guessing another context.

Reserved words are candidates only where Bash grammar permits a reserved
word. Command-name resolution must also include registered Kabibi builtins,
supported mvdan builtins, shell functions/aliases when available from session
state, and host commands.

### 4.2 Host command lookup and PATH cache

Host command candidates come from the execution session’s PATH/PATHEXT context.
The cache key includes the effective working directory, PATH, PATHEXT, and
platform mode. Keep the existing in-memory cache and current one-minute
maximum age unless a later approved change selects another TTL.

- Reuse a matching snapshot while its age is below the maximum age.
- Do not rescan PATH on each keystroke or completion request.
- When a key has no snapshot or its snapshot reaches the maximum age, coalesce
  a refresh for that key. Do not run PATH scans on the UI event handler.
- A PATH/PATHEXT/cwd change selects a different cache key; it must not return
  candidates from a snapshot for a different key.
- Honor platform executable rules, including executable mode on Unix-like
  hosts and PATHEXT on Windows.
- Cache and return candidate names, not executable output or command results.

The PATH index is for bare command names. Explicit path prefixes such as `./`,
`../`, or an absolute path use filesystem path completion instead. A file in
the working directory is not a bare command candidate unless shell lookup
rules make it one (for example, the directory is in PATH).

### 4.3 Options, operands, and filesystem paths

For a registered builtin, the parser definition determines whether the cursor
is completing an option, an option value, or a positional operand. Support
long-option `--name=value` forms and aliases/shorthands according to the
builtin’s parser definition. Boolean options that take no value must not
incorrectly trigger value completion.

For path-like values:

- Resolve relative paths against the shell’s current working directory.
- Enumerate only the directory relevant to the typed path prefix; do not
  recursively walk the workspace.
- Preserve the typed directory prefix and shell quote context.
- Append `/` to directory candidates. Append a separating space to a completed
  file or command word only when the shell word is complete and no separator
  already follows it.
- Include hidden entries only when the typed basename begins with `.`.
- Escape or quote inserted characters so the resulting command remains one
  shell word.
- Do not expand variables, command substitutions, or globs to perform a
  completion search.

Each candidate is represented as a display label plus an insertion edit
(rune-based start/end span and insertion text). Applying the edit must preserve
all text before and after the token, including text to the right of the cursor.

## 5. User-visible completion state machine

The prompt has three completion states:

1. **Idle:** no completion is displayed.
2. **Speculative:** an uncommitted gray ghost suffix or gray candidate row is
   displayed.
3. **Active:** candidates are selectable and Enter accepts the selected
   candidate.

Every completion result is tied to the input generation, cursor position,
working directory, and relevant environment/cache key that produced it. Ignore
late results after any of those have changed.

### 5.1 Explicit request

When the shell prompt owns input:

- Tab starts explicit completion only when the blue panels are hidden. With
  panels visible, Tab retains its existing pane-switch behavior.
- Ctrl+Space starts explicit completion whether the blue panels are visible or
  hidden.
- Existing editor, dialog, active file-pane search, running-command, and other
  higher-priority input ownership remains unchanged.
- If there is exactly one candidate, apply it immediately.
- If there are multiple candidates, do not insert a partial common prefix.
  Enter active state and render the candidates in the reserved row below the
  input. Select the first candidate initially; left/right changes selection,
  Tab cycles forward, and Enter applies the selected candidate. Escape dismisses
  the list without changing the input.
- If there are no candidates, leave the input and cursor unchanged.

An accepted directory candidate ends in `/` and does not append a space. An
accepted completed command/file/value appends a space only when the parser says
the word is complete and no existing right-hand character already separates
the next word.

### 5.2 Speculative request

After an input edit, wait until the user has made no further text edit for one
second, then request completion asynchronously. Cursor navigation by itself
does not start a new one-second timer. If the cursor moves, the input changes,
the session state changes, the prompt loses ownership, or a command starts,
discard the speculative result.

- One candidate: render its insertion text in gray at the exact insertion
  point. The ghost is presentation-only; it is not part of `PromptInput.Value`,
  command history, command classification, or submitted input.
- Multiple candidates: render them in gray in the reserved row. Do not select
  one or change the input.
- Zero candidates: render nothing.
- A speculative list is not active: arrows do not select it and Enter submits
  the text actually typed.
- Tab or Ctrl+Space promotes a speculative result to explicit behavior. For a
  unique candidate, accept it; for multiple candidates, activate the list and
  select its first entry.

For a unique ghost, Tab accepts it. Right Arrow accepts it only when the cursor
is at the end of the actual input and there is no character to the right of the
insertion point. If any actual character is to the right, normal Right Arrow
movement takes precedence: discard the ghost, move the cursor right by one
actual character, and do not insert the suggestion. Enter always discards the
ghost and submits only the actual input.

Any text edit dismisses a speculative result before applying the edit. In
active state, text edits dismiss the candidate list and then edit the actual
input. This prevents stale suggestions from being accepted.

### 5.3 Candidate-row rendering

Use the existing single reserved footer row beneath the prompt input. Do not
change the prompt’s wrapping or allocate a second permanent footer row.

- Speculative candidate rows use a muted gray style and have no selection
  marker.
- Active rows visually distinguish the selected candidate from the others.
- Render candidates in deterministic order. If the full list does not fit,
  keep the selected candidate visible, clip the rest with an omission
  indicator, and allow selection to traverse the complete candidate set.
- The row must remain one visual line at all terminal widths and must not move
  the input cursor.

## 6. Scheduling, ownership, and failure behavior

Completion search must not block `HandleEvent`, rendering, command execution,
or the shell session lock. Run filesystem enumeration and expired PATH refresh
off the UI event path. Deliver results through the shell’s existing UI
invalidation/message mechanism.

Use a monotonically increasing request/input generation (or equivalent
cancellation token) so a late result cannot overwrite a newer input state.
Completion failures must be surfaced through the repository’s normal logging or
diagnostic path; do not convert an error into a success-shaped empty candidate
list. A missing PATH directory or unreadable completion directory may be
skipped only when the filesystem API reports the expected not-found/permission
condition; unexpected errors remain observable.

Speculative work is advisory and may be dropped when superseded. Explicit
requests must still return deterministically for the current input and must not
accept stale results.

## 7. Test and validation specification

Tests must prove the user-visible contract and the shared-parser invariant.
Avoid timing-sensitive sleeps for the one-second behavior or TTL boundary; use
a controllable clock, timer seam, or directly testable state transition.

### 7.1 Parser-source and builtin coverage

- Enumerate all supported builtins from the same command registries used for
  execution.
- Assert each supported command exposes a reusable parser definition without
  running the command.
- For standard `flag` and `pflag` builders, verify the extracted completion
  syntax reflects the actual registered definitions: aliases, optional versus
  required values, booleans, custom values, and `--name=value`.
- Verify positional grammar is available from the parser source for commands
  whose flag library only returns `Args()`.
- Exercise custom Git, web/CDP, and AI parsers to prove completion and runtime
  parsing use the same definitions.
- Preserve existing behavior with parser/execution regression cases, including
  help, invalid options, aliases, and repeated command invocations. No command
  may be executed merely to discover its syntax.

### 7.2 Syntax-context and candidate tests

Use table-driven tests with input, cursor position, expected context, candidate
set, and replacement span. Cover:

- Empty input and partial command names.
- Command positions after `;`, newline, `&`, `&&`, `||`, `|`, and `|&`.
- Reserved-word bodies and nested command substitutions.
- Assignment prefixes, quoted words, escaped spaces, open quotes, and cursor
  positions in the middle of a word.
- Option names, aliases, booleans, option values, and `--name=value`.
- Positional arguments and redirection targets.
- Unsupported dynamic contexts returning no unsafe guess.
- File/directory entries, hidden-file rules, relative and absolute paths,
  quote/escape insertion, and existing right-hand text.
- Description heuristic positives and negatives.

### 7.3 PATH cache tests

Use temporary PATH directories and an observable refresh seam. Prove that:

- Candidate names honor Unix executable mode, Windows PATHEXT, and the cache
  key’s cwd/PATH/PATHEXT/platform fields.
- Repeated requests within max age do not rescan.
- A stale snapshot refreshes once after max age; concurrent requests coalesce.
- A changed key cannot return a different key’s candidates.
- A refresh result cannot replace the cache after the cache is closed or after
  a newer key supersedes it.

### 7.4 Prompt and app integration tests

Test the prompt state machine with deterministic time and direct key events:

- The one-second threshold, stale-result rejection, unique ghost rendering,
  speculative multi-choice row, and zero-result no-op.
- Enter submits only actual text from either speculative state.
- Tab and Ctrl+Space promote/accept as specified.
- Left/right/Tab selection and Enter acceptance in active state.
- Ghost acceptance by Tab and by Right Arrow at end; right-arrow precedence
  with text to the right.
- Editing, cursor movement, escape, submit, and reset dismiss or invalidate
  completion as specified.
- Cell-level assertions for gray ghost text, speculative row, active selection,
  clipping, wrapping, and cursor placement.

At `AppWidget` level, verify Tab with panels hidden triggers completion, Tab
with panels visible still switches panes, and Ctrl+Space triggers completion in
both panel states. Also verify existing modal/editor/search/running-command
ownership is unchanged. Use the existing direct-event and VT-emulator test
patterns; do not rely solely on snapshots.

### 7.5 Validation commands

After implementation is approved:

```powershell
go -C kabibi test ./ui/shell ./ui/app
```

Run focused tests for every changed u-root command package if its parser
definitions are modified, followed by the full relevant module suites:

```powershell
go -C kabibi test ./...
go -C u-root test ./...
```

The u-root full suite is required only if u-root parser sources change. Use the
repository’s documented Go module workflows; do not substitute a different
build path for dependent Rusticated modules.

## 8. Acceptance criteria

The feature is ready for approval as implemented only when:

1. All supported builtin syntax used by completion comes from the same parser
   definitions used at runtime; no parallel completion-only option/operand
   table remains.
2. Cursor-aware command and argument contexts work across the specified Bash
   constructs without executing input.
3. PATH candidates use the in-memory cache and are not rescanned before the
   maximum age.
4. Path-like options and operands enumerate and insert safe candidates while
   ambiguous string values remain conservative.
5. Explicit and speculative UI behavior matches Section 5, including the
   Enter/ghost and right-arrow/suffix precedence rules.
6. Tab routing with visible blue panels remains unchanged, Ctrl+Space works
   with either panel state, and the reserved completion row does not disturb
   prompt layout.
7. Focused and full relevant module tests pass.

## 9. Approval boundary

This document defines the proposed behavior, parser contract, and validation
gates. It does not authorize implementation. Code changes and builds begin only
after the specification is reviewed and approved.