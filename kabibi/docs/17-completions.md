# Kabibi shell completion and hints

**Status:** Approved for implementation; implementation is in progress.

**Implementation decision:** Classify the editable input with Bash syntax and
cursor position, resolve command names using actual shell/builtin dispatch and
a TTL-cached PATH index, then use runtime-registered command definitions for
argument completion. For most u-root commands, extract `FlagSet` construction
into reusable constructors. Runtime `Main` and Kabibi's completion registry use
the same constructors; Kabibi inspects a fresh definition without executing
the command. Kabibi-owned builtins, including AI, expose their existing
runtime argument definitions through the registry. Completion is useful and
advisory; it is not required to reproduce every command's full grammar.
Never scrape help, documentation, usage text, or source code to reconstruct
syntax. Do not fork go-git; limited delegated Git completion is the approved
exception to using the runtime definition directly.

## 1. Purpose

Add syntax-aware completion and hints to Kabibi's interactive TUI shell prompt.
The feature has two request modes:

1. **Explicit completion:** the user asks for completion with Tab or
   Ctrl+Space.
2. **Speculative completion:** after one second without an input edit, Kabibi
   searches in the background and displays a non-committing hint.

Completion operates only on the editable shell prompt. It does not intercept
input while a foreground command, editor, modal dialog, or higher-priority app
interaction owns the key.

## 2. Existing integration points and constraints

- `PromptInput` owns editable text, cursor position, wrapping, and cell
  rendering.
- `Shell` pins the prompt above one reserved footer row. That row is for
  completion choices; completion must not consume another permanent row.
- Shell execution uses the mvdan Bash parser. Completion uses shell syntax and
  cursor position, not splitting on spaces or regular-expression guesses.
- The execution session already keeps an in-memory PATH index with a current
  one-minute maximum age. Completion reuses that cache rather than creating a
  second PATH index.
- Supported commands have multiple owners: mvdan shell builtins, the u-root
  registry, and Kabibi-owned Git, AI, browser, and other middleware.
- With blue panels visible, Tab switches panes. Preserve that behavior.
  Ctrl+Space requests completion regardless of panel visibility.
- Completion must not execute a command, mutate the shell environment, or
  change the text Enter submits unless the user accepts a candidate.

## 3. Syntax and command-resolution design

### 3.1 Bash syntax and cursor context

Use the Bash parser to identify the word and syntactic role at the cursor:
command head, argument, redirection target, shell keyword position, or
unsupported/dynamic context. Recognize command positions after list and
pipeline operators (`;`, `&`, `&&`, `||`, `|`, `|&`), newlines, and
command-introducing reserved words such as `then`, `do`, `else`, and `elif`.
Recognize nested command lists and command substitutions. Do not mistake
assignment words before a command for arguments to that command.

The parser must tolerate incomplete interactive input, including a partial
command word, open quotes, a trailing operator, and a partial redirection. Do
not execute expansions to determine completion. A dynamically expanded command
head or other context that cannot be classified safely produces no candidates.
Redirection operands are path contexts, not command-name contexts.

### 3.2 Command names, runtime dispatch, and PATH

Offer reserved words only at positions where Bash grammar permits them.
Resolve command names using the same precedence as runtime dispatch:
implemented shell builtins and Kabibi-registered builtins/middleware take
precedence over host executables. Use the actual runtime registries and their
accepted aliases; do not blindly union every known name into executable
candidates. A collision such as a shell builtin and a u-root command must
resolve to the command the shell would actually invoke.

Bare host command names come from the execution session's PATH/PATHEXT index.
The cache key includes effective working directory, PATH, PATHEXT, and
platform mode. Reuse a matching snapshot until its current one-minute maximum
age expires; do not rescan PATH on each keystroke. A missing or stale snapshot
is refreshed asynchronously, coalescing concurrent refreshes for the same key.
A changed key must never return candidates from a different snapshot.

The PATH index honors platform executable rules, including Unix executable
mode and Windows PATHEXT. It caches candidate names, not command output. An
explicit path prefix such as `./`, `../`, or an absolute path uses filesystem
completion; a working-directory file is not a bare command candidate unless
the directory is in PATH.

### 3.3 Runtime argument-definition registry

The central completion registry is populated alongside runtime command
registration. It maps the command names and aliases accepted by dispatch to a
syntax provider. Providers are constructors/factories, not shared mutable
parser instances. Each completion request obtains a fresh definition so it
cannot observe or alter values retained from a previous invocation.

For the majority of u-root commands that use Go `flag.FlagSet` or `pflag`,
extract the existing flag construction from `Main` into a constructor. Runtime
`Main` calls that constructor for normal parsing. Kabibi's registry invokes
the same constructor to inspect the registered flags. The flag definitions
remain the source of option names, aliases/shorthands, whether values are
required or optional, boolean behavior, custom value types, and flag syntax.
For example, `cp -a` and `cp --archive` must come from the same pflag
definition, not a second completion-only list.

Each constructor creates a fresh FlagSet and fresh bound values. Do not store a
live FlagSet globally or retain pointers to values from a command invocation.
Register the syntax provider beside the runtime command handler so aliases and
dispatch precedence cannot drift. Preserve `pflag` semantics such as
shorthands and `NoOptDefVal`.

The parser/registry inspection surface must expose the actual definition
read-only, including whether the cursor is awaiting a flag value. This
inspection follows the parser's flag syntax, including `--name=value`, and
must not call value setters or mutate command state. If the underlying parser
API does not expose a needed property, extend that API at the definition
source; do not recover it by scraping source or creating a duplicate syntax
table.

A FlagSet does not necessarily describe positional roles, preprocessors such
as `dd`'s `key=value` syntax, or a delegated command's grammar. Do not require
every command to model those details, and do not introduce a universal
full-fidelity `CommandSyntax` state machine. Use completion-relevant
information already exposed by the runtime definition. Where it does not
identify the expected positional value, offer path candidates only when the
context provides reasonable evidence that the value is path-like; otherwise
offer no argument candidates. Completion is not command validation.

For builtins that do not use FlagSet, expose their existing runtime-owned
definition through the same registry:

- **AI:** expose the tool argument schemas consumed by
  `scanAIToolArguments`/`parseAIToolArguments`, plus control-command and
  provider definitions used by runtime dispatch.
- **`web`/`cdp`:** use existing shared subcommand/argument definitions where
  available, including runtime nickname and CDP-method providers.
- **Other Kabibi builtins:** use their existing parser/schema/provider when
  available; do not create a duplicate completion grammar solely to force
  complete coverage.
- **mvdan builtins:** resolve command position from implemented builtin
  dispatch. Recognized-but-unimplemented names are not advertised as
  supported commands. Argument completion may be partial.

### 3.4 Value kinds and description heuristics

Use an explicit value type or choice list when the runtime definition exposes
one. If it only identifies an unannotated string value, apply conservative
heuristics to the option name and its human-readable description:

- Directory terms such as `dir`, `directory`, `folder`, `work-tree`, `git-dir`,
  or `chdir` suggest directory candidates.
- File/path terms such as `file`, `path`, `filename`, `archive`, `keyring`,
  `config`, `source`, or `destination` suggest filesystem candidates.
- Parenthesized pipe lists such as `(none|xfer|progress)` suggest enum-like
  choices.
- Terms such as `pattern`, `regex`, `query`, `url`, `text`, `message`, `name`,
  `count`, `duration`, or `mode` without a path term remain ordinary strings.

These are value-kind heuristics only. They do not infer command grammar, and
they never override an explicit type or value list.

### 3.5 Git completion boundary

Do not fork go-git. Its delegated parser is private and has no public
parse-only facility. Maintain limited Kabibi-owned hints for delegated Git
commands: command names, selected common options, local branches in a few
positions, and paths in selected positions. This definition is intentionally
incomplete; do not promise comprehensive revision, tag, remote, refspec,
object-ID, config-value, enum, or per-command option-value completion.

Reuse Kabibi-owned runtime definitions where available: global Git options
through `gitGlobalOptionSpecs`/`parseGitInvocationState`, and notes/replace
syntax through their runtime definitions. Those Kabibi-owned forms are not
duplicated in the delegated Git hints.

## 4. Completion context and candidate resolution

### 4.1 Cursor-aware shell context

The resolver receives the complete input, rune-based cursor position, current
working directory, and execution-session environment/state. It identifies the
token replacement span and one of: command name, shell keyword, option name,
option value, positional argument, redirection target, or safely
unclassifiable context.

Do not treat expansions as literal paths or execute them. Preserve shell quote
context. When completion cannot classify the cursor safely, return no
candidates instead of guessing a different syntactic role.

### 4.2 Options, operands, and paths

For a registered builtin, use the registry's fresh runtime definition to
identify options, aliases, and pending flag values. Support long-option
`--name=value` forms and the parser's shorthand/optional-value conventions.
Boolean options that take no value must not trigger value completion.

For path-like values:

- Resolve relative paths against the shell's current working directory.
- Enumerate only the directory relevant to the typed path prefix; do not
  recursively walk the workspace.
- Preserve the typed directory prefix and shell quote context.
- Append `/` to directory candidates. Add a separating space to a completed
  file or command word only when the word is complete and no separator follows.
- Include hidden entries only when the typed basename begins with `.`.
- Escape or quote inserted characters so the result remains one shell word.
- Do not expand variables, command substitutions, or globs to search.

Each candidate has a display label and insertion edit (rune-based start/end
span and insertion text). Applying the edit preserves text before and after the
token, including text to the right of the cursor.

### 4.3 Resolution sequence

For either completion mode, resolve candidates without mutating shell state:

1. Parse Bash syntax and classify the cursor, including redirection and
   command-position rules.
2. If at a command position, offer grammar-valid shell keywords and resolve
   executable names through actual runtime dispatch registries and the cached
   PATH index.
3. If at an argument position, select the dispatched command's registered
   syntax provider and inspect its fresh definition. Offer known option names,
   expected flag values, explicit choices, or likely paths.
4. If the position is not represented by the command's runtime definition and
   no safe value-kind inference applies, return no candidates.

For example, if the registry exposes `--config` as a file-valued flag, typing
`tool --config ./co` offers matching filesystem entries. It does not require
Kabibi to model every positional argument accepted by `tool`.

## 5. User-visible completion behavior

The prompt has three completion states:

1. **Idle:** no completion is displayed.
2. **Speculative:** an uncommitted gray ghost suffix or gray candidate row is
   displayed.
3. **Active:** candidates are selectable and Enter accepts the selected
   candidate.

Every result is tied to the input generation, cursor position, working
directory, and relevant environment/cache key. Ignore late results after any
of those have changed.

### 5.1 Explicit request

When the shell prompt owns input:

- Tab starts explicit completion only when the blue panels are hidden. With
  panels visible, Tab retains its pane-switch behavior.
- Ctrl+Space starts explicit completion whether the blue panels are visible
  or hidden.
- Existing editor, dialog, active file-pane search, running-command, and other
  higher-priority input ownership remains unchanged.
- One candidate is applied immediately.
- Multiple candidates enter active state immediately; do not wait for a second
  Tab and do not insert a partial common prefix. Show candidates in the
  reserved row, select the first, use left/right to change selection, Tab to
  cycle, and Enter to apply the selected candidate. Escape dismisses the list
  without changing input.
- Zero candidates leave input and cursor unchanged.

An accepted directory candidate ends in `/` and does not append a space. An
accepted completed command/file/value appends a space only when the word is
complete and no existing right-hand character already separates the next
word.

### 5.2 Speculative request

After an input edit, wait until the user has made no further text edit for one
second, then request completion asynchronously. Cursor navigation alone does
not restart the timer. If the cursor moves, input changes, session state
changes, the prompt loses ownership, or a command starts, discard the
speculative result.

- One candidate: render its insertion text in gray at the exact insertion
  point. The ghost is presentation-only; it is not in `PromptInput.Value`,
  command history, command classification, or submitted input.
- Multiple candidates: render them in gray in the reserved row. Do not select
  one or change input.
- Zero candidates: render nothing.
- A speculative list is not active: arrows do not select it and Enter submits
  the text actually typed.
- Tab or Ctrl+Space promotes a speculative result to explicit behavior. For
  one candidate, accept it; for multiple candidates, activate the list and
  select its first entry.

For a unique ghost, Tab accepts it. Right Arrow accepts it only when the
cursor is at the end of actual input and there is no character to the right of
the insertion point. If an actual character is to the right, normal Right
Arrow movement takes precedence: discard the ghost, move right by one actual
character, and do not insert the suggestion. Enter always discards the ghost
and submits only actual input.

Any text edit dismisses a speculative result before applying the edit. In
active state, text edits dismiss the candidate list and then edit actual input.

### 5.3 Candidate-row rendering

Use the existing single reserved footer row beneath the prompt. Do not change
prompt wrapping or allocate a second permanent footer row.

- Speculative candidate rows use muted gray and have no selection marker.
- Active rows distinguish the selected candidate from others.
- Render candidates in deterministic order. If the list does not fit, keep the
  selected candidate visible, clip the rest with an omission indicator, and
  allow selection through the complete set.
- The row remains one visual line at all terminal widths and does not move the
  input cursor.

## 6. Scheduling, ownership, and failure behavior

Completion search must not block `HandleEvent`, rendering, command execution,
or the shell session lock. Run filesystem enumeration and expired PATH refresh
off the UI event path. Deliver results through the shell's existing UI
invalidation/message mechanism.

Use a monotonically increasing request/input generation or equivalent
cancellation token so late results cannot overwrite newer input state.
Completion failures use the repository's normal logging or diagnostic path;
do not turn an error into a success-shaped empty candidate list. A missing
PATH directory or unreadable completion directory may be skipped only for the
expected not-found/permission condition; unexpected errors remain observable.

Speculative work is advisory and may be dropped when superseded. Explicit
requests must still return deterministically for current input and must not
accept stale results.

## 7. Test and validation specification

Avoid timing-sensitive sleeps for the one-second behavior or TTL boundary; use
a controllable clock, timer seam, or directly testable state transition.

### 7.1 Runtime definitions and command coverage

- Enumerate supported command names from the same runtime registries used for
  dispatch, including aliases and precedence collisions.
- Verify u-root FlagSet constructors create fresh sets and fresh values and can
  be called by runtime parsing and completion without running a command.
- Verify the registry exposes the actual standard `flag` and `pflag`
  definitions, including aliases/shorthands, optional versus required values,
  booleans, custom values, and `--name=value`.
- Verify that completion inspection is read-only and does not invoke flag
  setters or retain state between requests.
- Verify AI completion uses the schemas consumed by runtime AI argument
  parsing, and that Git global/notes/replace completion reuses Kabibi runtime
  definitions. Test bounded delegated Git hints separately; do not assert
  complete parity with go-git's private parser.
- Preserve runtime behavior with focused parsing regressions, including help,
  invalid options, aliases, and repeated command invocations.

### 7.2 Syntax context and candidates

Use table-driven tests with input, cursor position, expected context,
candidate set, and replacement span. Cover:

- Empty input, partial command names, and command positions after `;`,
  newline, `&`, `&&`, `||`, `|`, and `|&`.
- Reserved-word bodies, nested command substitutions, and assignment prefixes.
- Quoted words, escaped spaces, open quotes, and cursor positions within a word.
- Option names, aliases, booleans, option values, and `--name=value`.
- Positional contexts where the runtime definition supplies useful metadata,
  redirection targets, and unsafe dynamic contexts.
- File/directory entries, hidden-file rules, relative and absolute paths,
  quote/escape insertion, and existing right-hand text.
- Description heuristic positives and negatives.

### 7.3 PATH cache

Use temporary PATH directories and an observable refresh seam. Prove that:

- Candidate names honor Unix executable mode, Windows PATHEXT, and the cache
  key's cwd/PATH/PATHEXT/platform fields.
- Repeated requests within max age do not rescan.
- A stale snapshot refreshes once after max age; concurrent requests coalesce.
- A changed key cannot return a different key's candidates.
- A refresh result cannot replace the cache after closure or after a newer key
  supersedes it.

### 7.4 Prompt and app integration

Test prompt state transitions with deterministic time and direct key events:

- The one-second threshold, stale-result rejection, unique ghost rendering,
  speculative multi-choice row, and zero-result no-op.
- Enter submits only actual text from either speculative state.
- Tab and Ctrl+Space promote/accept as specified.
- Left/right/Tab selection and Enter acceptance in active state.
- Ghost acceptance by Tab and by Right Arrow at end; right-arrow precedence
  when text exists to the right.
- Editing, cursor movement, escape, submit, and reset dismiss or invalidate
  completion as specified.
- Cell-level assertions for gray ghost text, speculative row, active
  selection, clipping, wrapping, and cursor placement.

At `AppWidget` level, verify Tab with panels hidden triggers completion, Tab
with panels visible still switches panes, and Ctrl+Space triggers completion
in both panel states. Verify modal/editor/search/running-command ownership is
unchanged. Use direct-event and VT-emulator test patterns; do not rely solely
on snapshots.

### 7.5 Validation commands

After implementation:

```powershell
go -C kabibi test ./ui/shell ./ui/app
```

Run focused tests for every changed u-root command package if its parser
definitions are modified, followed by the relevant module suites:

```powershell
go -C kabibi test ./...
go -C u-root test ./...
```

The u-root full suite is required only if u-root parser sources change. Follow
the repository's documented Go module workflows; do not substitute a different
build path for dependent Rusticated modules.

## 8. Acceptance criteria

1. Bash syntax and cursor position determine command/argument context without
   executing input.
2. Command names follow actual runtime dispatch and the PATH cache's
   one-minute maximum age.
3. The majority of u-root FlagSet-backed commands expose their existing flag
   definitions through fresh constructors shared by runtime parsing and the
   completion registry; no source-generation or help-scraping step exists.
4. Kabibi-owned definitions such as AI use the same runtime schemas, except
   the explicitly bounded delegated Git hints.
5. Completion offers useful option, enum, command, and path candidates where
   definitions or safe value-kind evidence exist; full-fidelity positional
   grammar for every command is not required.
6. Explicit and speculative UI behavior matches Section 5, including Enter/
   ghost and right-arrow/suffix precedence.
7. Tab routing with visible blue panels remains unchanged, Ctrl+Space works
   with either panel state, and the reserved row does not disturb prompt layout.
8. Focused and relevant module tests pass.

## 9. Approval boundary

The design in this document is the approved direction: Bash-based shell
context, runtime dispatch and cached PATH for command selection, and runtime
argument-definition exposure through a registry. For most u-root commands,
that means extracting their FlagSet constructors and invoking them both from
`Main` and completion. It does not authorize full command-grammar rewrites,
source/help scraping, or a go-git fork. Known parser limitations are
acceptable when they result in partial suggestions rather than guessed syntax.
