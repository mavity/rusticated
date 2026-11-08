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

### 3.2 Adapting existing argument parsers: four unified methods

All builtin families expose a single `CommandSyntax` state machine that both
parses runtime invocations and steps through tokens up to the cursor during
completion:

```go
type ParseState interface {
	Step(args []string, cursorIdx int) (Expectation, error)
}
```

#### Method 1: Enhanced `FlagSet` / `pflag` builders (`u-root` flag commands)

For the 74 `u-root` commands backed by `flag.FlagSet` or `pflag.FlagSet`,
extract flag construction into a side-effect-free builder that attaches typed
values and positional tail specifications:

- **Unix dash normalization:** because `u-root` runs `unixflag.ArgsToGoArgs`,
  single-letter flags render as `-f` and multi-character flags render as
  `--family`, while preserving `pflag` shorthand and `NoOptDefVal` optional
  values.
- **Typed `flag.Value` wrappers for enums and paths:** replace post-parse
  `switch` validation (such as `ls --color` or `cp --reflink`) with reusable
  `flag.Value` types that both validate at runtime and expose choices or path
  kinds to completion:

```go
fs.Var(uflag.Enum(&c.colorMode, "never", "always", "auto", "never"),
	"color", "colorize output")
fs.Var(uflag.DirPath(&c.targetDir), "target-directory", "destination directory")
```

- **Positional and command-delegation tail specs:** attach positional arity and
  roles directly to the builder so runtime bounds-checking and completion share
  one definition:

```go
spec.Positionals(uflag.Files("source", uflag.Variadic), uflag.File("dest"))
spec.DelegatesCommand() // for time, timeout, nohup, xargs
```

- **`key=value` flag adaptation (`dd`):** mark `dd`’s builder with
  `KeyEqualsValue` style so its `FlagSet` (`if`, `of`, `conv`, `oflag`,
  `status`) emits `if=`/`of=` (`KindFile`) and `conv=`/`status=` (`KindEnum`
  from `convMap`/`flagMap`).

#### Method 2: Cursor-driven dry-run parsing for stateful grammars

For commands where options, subcommands, and operands interleave (`ip`, `grep`,
`rg`, `chmod`, `kill`, `stty`, `find`), run the command’s actual parser in a
side-effect-free `ParseOnly` mode up to the cursor word index:

- **`ip` recursive-descent expectation harvesting:** `ip`’s parser already
  records `cmd.ExpectedValues` at every token via `nextToken(expectedValues...)`.
  Decoupling netlink handle creation from token parsing lets completion read
  `cmd.ExpectedValues` directly at the cursor:

```go
tok := cmd.nextToken("address", "link", "route", "neighbor", "help")
if cmd.DryRunAtCursor() { return cmd.ExpectedValues }
```

- **Context-dependent positional shifts (`grep`, `rg`, `chmod`):** running
  `parseArgs` over tokens before the cursor reveals whether `-e`/`-f`/`--files`
  (in `grep`/`rg`) or `--reference` (in `chmod`) was already supplied,
  automatically shifting the first positional argument from pattern/mode
  (`KindString`) to `KindFile`.
- **Signal and setting maps (`kill`, `stty`):** drive `kill` and `stty` from
  their runtime `signums` map and setting table (`pretty`, `dump`, `size`,
  `raw`, `load` $\to$ `KindFile`).

#### Method 3: Declarative `SubcommandSpec` trees (`git`, `web`/`cdp`, `ai`, nested shells)

Subcommand-based builtins replace ad-hoc `switch` dispatchers with a tree of
`SubcommandSpec` nodes consumed by both execution and completion:

- **`web` and `cdp`:** share global `--nick`/`-n` (`KindDynamic` querying active
  browser nicknames) and subcommand specs (`open` with `--back|--forward|--reload`,
  `shot` with `KindFile` and `--adorn|--full`, `ax`, `click`, `type`, `eval`,
  `cdp`, `logs`):

```go
webSpec.Sub("shot", Aliases("screenshot"),
	Positional("output", KindFile), Flag("--adorn"), Flag("--full"))
```

- **`ai` control and tools:** complete `setup`, `login`, `logout`, `status`,
  `help`, plus all tool names from `BuildKabibiTools(nil)`. For `login`/`logout`,
  complete from the runtime provider slice; for tools, inspect `tool.Parameters`
  (boolean flags, enum lists, and path properties).
- **Two-layer `git` grammar (Kabibi + `go-git`):**
  - Kabibi’s outer parser defines `-C`, `--git-dir`, `--work-tree` (`KindDir`),
    `-c`, `--no-pager`, `-P`, `--bare`, and updates `effectiveDir` before
    delegating completion to the subcommand.
  - Kabibi-owned subcommands (`notes`, `replace`, `bisect run` $\to$
    `KindCommand`, `commit --author/--date`) and `go-git`’s 54 subcommands use
    a shared `git.CommandSpec` registry instead of the disconnected `help` map,
    attaching dynamic read-only repo providers (`KindGitBranch`, `KindGitRef`,
    `KindGitRemote`, `KindFile`).

#### Method 4: First-class builtin specs in local `mvdan.cc/sh/v3/interp`

In the local `mvdan/sh` fork, separate `IsImplementedBuiltin` from the 17
unimplemented `IsBuiltin` names and back `flagParser` with per-builtin specs:

- **Directory/file builtins:** `cd` and `pushd` (`-n`, `KindDir`); `source` and
  `.` (`KindFile`).
- **Command/builtin delegators:** `command` (`-v`) and `exec` transition to
  `KindCommand`; `builtin` transitions to `KindBuiltin`; `type` and `help`
  complete commands, builtins, and keywords.
- **Environment, alias, and option tables:** `export`, `unset`, `read`,
  `readonly`, `declare`, `typeset` complete `KindEnvVar`; `alias`/`unalias`
  complete runner aliases; `shopt` completes directly from `posixOptsTable`
  (under `-o`) or `bashOptsTable`:

```go
shoptSpec := BuiltinSpec{Flags: "-s -u -o",
	Dynamic: completeShoptFromRunnerTables}
```

### 3.3 Semantic value kinds and description heuristics

Each option or positional expectation resolves to an explicit `ValueKind`:
`KindNone`, `KindBool`, `KindString`, `KindInt`, `KindEnum`, `KindFile`,
`KindDir`, `KindCommand`, `KindBuiltin`, `KindEnvVar`, `KindGitRef`, or
`KindDynamic`.

When a legacy or third-party parser only exposes an unannotated string value,
infer `KindDir`, `KindFile`, or `KindEnum` from the option name and
human-readable description as a conservative fallback:

- **Directory match:** `\b(dir|directory|folder|work-tree|git-dir|chdir)\b`
  $\to$ `KindDir`.
- **File/path match:** `\b(file|files|path|paths|filename|archive|keyring|config|source|destination)\b`
  $\to$ `KindFile`.
- **Inline enum match:** parenthesized pipe lists such as `(none|xfer|progress)`
  in `dd` $\to$ `KindEnum`.
- **Negative guard:** terms such as `pattern`, `regex`, `query`, `url`, `text`,
  `message`, `name`, `count`, `duration`, or `mode` without a path noun remain
  `KindString` and offer no filesystem candidates.

An explicit parser `ValueKind` always takes precedence over the heuristic.

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

### 4.4 End-to-end resolution pipeline

When explicit or speculative completion runs, it resolves candidates in two
stages without mutating shell state:

1. **AST cursor classification:** parse the input with `syntax.KeepComments(true)`
   (recovering from unclosed quotes or trailing operators) and locate the node
   at the cursor rune offset:
   - Redirection operand (`>`, `>>`, `<`, `2>`) $\to$ `KindFile`.
   - Command head (statement start, after `;`, `|`, `|&`, `&`, `&&`, `||`, `(`,
     `then`, `do`, `else`, `elif`) $\to$ path completion if the token contains
     `/` or `\`, otherwise union of valid Bash keywords, session functions and
     aliases, implemented `mvdan` builtins, Kabibi builtins (`git`, `ai`, `web`,
     `cdp`, `bash`, `sh`, `kabibi`, `quit`), the 85 `u-root` commands, and
     TTL-cached `PATH` executables.
2. **Builtin `ParseState.Step` transition:** resolve the command head using
   exact runtime dispatch precedence (`mvdan` builtin $\to$ nested shell $\to$
   `git` $\to$ `ai` $\to$ `web`/`cdp` $\to$ `u-root`) and step its parser over
   the words preceding the cursor:
   - If the parser reaches a `KindCommand` tail (`time`, `timeout`, `nohup`,
     `xargs`, `command`, `exec`, `git bisect run`), recursively re-enter Stage 1
     on the remaining slice:

```go
if exp.Kind == KindCommand {
	return resolveCommandSlice(ctx, args[exp.SubSliceIdx:], cursorInSlice)
}
```

   - Otherwise, complete the active `Expectation` (`OptionSpec` names,
     `KindEnum` choices, `KindFile`/`KindDir` entries, `KindGitRef` refs,
     `KindEnvVar` variables, or `KindDynamic` callback results).

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

# Feasibility of the syntax inference


## Complete builtin/parser-source audit

The parser-source requirement in `17-completions.md` is the right invariant, but the current parsers do not provide a single reusable grammar for most builtins. In particular, harvesting a `flag.FlagSet` covers option declarations—not positional argument meaning—and several major command families do not use a `FlagSet` parser at all.

**Correction:** the u-root registry has **85 entries**, not 92. I initially overcounted it; I verified the count against the registry sections.

### 1. u-root: all 85 registered commands

The full registry is in `uroot.go`. Its entries, grouped as registered, are:

- **Pilot:** `cat`, `grep`, `rg`
- **Text/filter:** `base64`, `cmp`, `comm`, `head`, `hexdump`, `more`, `seq`, `sed`, `sort`, `strings`, `tail`, `tee`, `tr`, `ts`, `tsort`, `uniq`, `wc`, `yes`
- **File/directory:** `basename`, `chmod`, `cp`, `dirname`, `du`, `find`, `ln`, `ls`, `mkdir`, `mktemp`, `mv`, `pwd`, `readlink`, `realpath`, `rm`, `touch`, `truncate`
- **Archive/compression:** `cpio`, `gpgv`, `gzcat`, `gzip`, `md5sum`, `shasum`, `tar`
- **Utility/control:** `backoff`, `date`, `false`, `hostname`, `printenv`, `sleep`, `time`, `timeout`, `true`, `which`, `xargs`
- **Transport/subshell:** `dd`, `gosh`, `nc`, `netcat`, `nohup`, `scp`, `sshd`, `ufs`, `wget`
- **Diagnostics/network:** `df`, `dmesg`, `free`, `id`, `ip`, `kill`, `lddfiles`, `lsdrivers`, `lsfd`, `lsmod`, `netstat`, `pci`, `pidof`, `ping`, `ps`, `stty`, `sync`, `tty`, `uname`, `uptime`

**Parser inventory and fixes:**

- **73 commands use Go `flag.FlagSet`**; `cp` uses `pflag`. Examples of the actual flag setup are in `cat.go`, `dd.go`, `cp.go`, `ls.go`, and `wget.go`.
  - These flag definitions are constructed in command code, not exposed by the registry as reusable parser objects. `Main` cannot be run just to inspect them because it can perform command side effects.
  - **Fix:** extract side-effect-free flag-set builders and use them for runtime parsing and completion. Add the separate positional grammar the flag library does not express; do not infer operand order or meaning from `Args()` or help text.
  - The `pflag` case needs an adapter for its shorthand and optional/no-value conventions; do not treat it as standard `flag` metadata.

- **Flag parsing plus additional grammar:** `dd`, `find`, `xargs`, `sed`, `tr`, `ip`, `time`, `timeout`, `scp`, `printenv`, `sleep`, `tar`, `cp`, `ln`, and `mv`.
  - For `dd`, the user-facing syntax is `key=value`; the `FlagSet` sees arguments only after conversion. Completion must share the conversion/parser, not suggest Go-style `--` flags. See `dd.go`.
  - `find` needs its starting-path position; `xargs`, `time`, and `timeout` need to transition to a delegated command and its arguments. `ip` has subcommands. `sed` and `tr` have script/set-expression grammars. `scp` has local/remote source and destination forms. `cp`, `ln`, and `mv` need source/destination cardinality. `tar`, `printenv`, and `sleep` also need positional roles beyond flags.
  - **Fix:** make those additional grammar states part of the same parser used by runtime, with completion callbacks for the next argument role. Command-delegating parsers must hand off command position to command-name completion after their options.

- **Handwritten/non-`FlagSet` parsers:** `grep`, `rg`, `chmod`, `kill`, and `stty`; `rg.go`, `chmod.go`, `kill.go`, and `stty.go` illustrate the exceptions.
  - `grep`/`rg` need pattern-versus-file roles; `chmod` needs mode-expression then path; `kill` needs signal/PID roles; `stty` has setting subcommands, including `load <file>`.
  - **Fix:** expose/refactor each parser as a reusable stateful grammar; a completion-only option map would duplicate its real parser.

- **Delegated command/shell grammar:** `gosh` and `nohup`. Their syntax cannot be fully described by option flags; completion must transition into the nested shell or command grammar.

- **Aliases/wrappers and no-argument commands:** `nc` shares `netcat`’s implementation; `gzcat` uses the gzip implementation family; `more` wraps `more.Main`; `false` and `true` take no operands. Reuse the target implementation’s grammar rather than defining a second one. The alias/wrapper assignments are visible in `uroot.go`.

**Dispatch collisions matter:** `false`, `true`, and `pwd` are also mvdan builtins, which execute before the registered u-root handler. `time` is a shell grammar keyword, not an ordinary `IsBuiltin` entry, and is also registered in u-root. Completion must reflect actual parser/dispatch precedence; it must not blindly union registry names as executable command candidates.

### 2. mvdan shell builtins

The local mvdan fork’s `interp.IsBuiltin` recognizes 61 names, but that API classifies names—it does not expose their argument syntax. Runtime parsing is a handwritten switch in `builtin.go`; even its `flagParser` only consumes tokens and registers no option metadata (`flagParser`). Builtins are dispatched before external execution handlers in `runner.go`.

The complete recognized-name inventory is:

- POSIX regular: `alias`, `bg`, `cd`, `command`, `false`, `fc`, `fg`, `getopts`, `hash`, `jobs`, `newgrp`, `pwd`, `read`, `true`, `umask`, `unalias`, `wait`
- POSIX special: `break`, `:`, `continue`, `.`, `eval`, `exec`, `exit`, `export`, `readonly`, `return`, `set`, `shift`, `times`, `trap`, `unset`
- Bash: `source`, `bind`, `builtin`, `caller`, `compgen`, `complete`, `compopt`, `declare`, `typeset`, `dirs`, `disown`, `enable`, `history`, `help`, `let`, `local`, `logout`, `mapfile`, `readarray`, `popd`, `pushd`, `shopt`, `suspend`, `type`, `ulimit`
- Utilities: `echo`, `printf`, `test`, `[`

The runtime switch implements many of these, including `cd`, `read`, `getopts`, `type`, `command`, `test`/`[`, `trap`, and `mapfile`/`readarray`. But **17 names are recognized by `IsBuiltin` and have no corresponding implementation case**: `bg`, `fc`, `fg`, `jobs`, `newgrp`, `bind`, `caller`, `compgen`, `complete`, `compopt`, `disown`, `enable`, `history`, `local`, `logout`, `suspend`, `ulimit`. They fall through to “unsupported builtin.” They should not be presented as supported completable builtins unless runtime support and its parser are added.

`export`, `readonly`, `declare`, `typeset`, and `let` are parser-level forms, not ordinary argv parsers. Completion must follow their shell AST/assignment grammar rather than apply a command-flag schema.

**Fix:** add a shared parser/completion definition at the interpreter implementation site and make runtime consume it. This must support semantic next-argument roles for the builtins whose syntax is not a flat option list: e.g. `test` expressions; `eval` shell text; `source` file plus positional parameters; `command`/`builtin` delegated command names; `read` variable names; and `trap` action/signal pairs. Do not use a generic flag schema for those. The recognized-but-unimplemented names need an explicit unsupported status, not guessed argument grammars.

### 3. Git: two parser owners, neither currently exposes a sufficient golden source

Kabibi intercepts `git` and parses global options itself in `git_builtin.go`, then delegates most commands to the pinned `go-git` dependency (`go.mod`). That dependency’s runtime dispatcher maps subcommands to private command methods ([upstream git.go](https://github.com/adrianliechti/go-git/blob/92d90da1a03594ec62f56cea36e6908c53acf2ab/git.go#L254)). Its separate help/usage/options table is not consumed by those runtime parsers and covers only some commands ([upstream help.go](https://github.com/adrianliechti/go-git/blob/92d90da1a03594ec62f56cea36e6908c53acf2ab/help.go#L16)). **That help table is not a golden parser source.**

The delegated command set includes `init`, `clone`, `add`, `mv`, `rm`, `restore`, `clean`, `stash`, `commit`, `status`, `log`, `show`, `diff`, `grep`, `blame`, `shortlog`, `describe`, `branch`, `checkout`, `switch`, `reset`, `merge`, `rebase`, `cherry-pick`, `revert`, `bisect`, `format-patch`, `apply`, `am`, `tag`, `remote`, `fetch`, `pull`, `push`, `submodule`, `worktree`, `ls-remote`, `reflog`, `config`, `version`, and plumbing commands including `rev-parse`, `rev-list`, `cat-file`, `hash-object`, `ls-files`, `ls-tree`, `show-ref`, `for-each-ref`, `update-ref`, `symbolic-ref`, `write-tree`, `commit-tree`, `read-tree`, `update-index`, `diff-tree`, `merge-base`, `merge-file`, and `count-objects`; `help` is handled specially by the dependency.

Kabibi adds more parser surfaces:

- Global Git options: `-C`, `-c`, `--git-dir`, `--work-tree`, pager flags, and `--bare`.
- `commit --author` and `--date` are parsed by Kabibi before delegation.
- Kabibi implements `replace`, `notes` (`list/show/add/append/remove/prune`), and `bisect run` itself. `replace` has list/delete/force/graft forms; notes has `--ref` plus subcommand-specific arguments; `bisect run` changes to command position. See `git_notes_replace.go`, `git_rewrite.go`, and `git_notes_replace.go`.
- SSH `clone` and `ls-remote` have a local parser/intercept, then may fall back to the dependency parser (`git_remote_ssh.go`).
- `log` and `diff` have parsers for a limited streaming/pager optimization subset. Those do not define the complete command grammar (`git_log_stream.go`).

**Fix:** the dependency needs a public shared parser/command-spec API (upstream change or maintained fork). Its command runtime must consume that API, and completion must query it; copying the private help map into Kabibi would leave two sources. Kabibi’s global options and its local intercepts must likewise become shared runtime/completion parsers, including the intercepted/fallback cases. The limited log/diff optimization parsers must not be used as grammar sources.

### 4. Browser and AI commands

- **`web` and `cdp`:** `tools_browser.go` hand-parses `--nick`/`-n`, subcommands, and arguments. `web` has `open`/`nav`/`navigate`, `ax`, `click`, `type`, `shot`/`screenshot`, `eval`, `cdp`, and `logs`; argument roles include URL, target, text, output path, JavaScript, and JSON. The browser AI tools’ typed definitions do not serve as this shell parser.
  - **Fix:** define each shell action’s argument grammar once and make the dispatch parser and completer use it. Add semantic providers for browser nicknames and CDP methods; mark only screenshot output as path-like.

- **AI tools:** this is the good existing case. Typed definitions build `ai.Tool` schemas in `ai_tools.go`, and `parseAIToolArguments` consumes those schemas. Reuse them for completion. Explicit path metadata or the documented description heuristic can provide value semantics; do not add a second option table.
- **AI control commands:** `ai setup/login/logout/status` use a separate switch in `ai_onboarding.go`, not the tool schema. `login`/`logout` accept provider values, while `setup`/`status` do not take operands.
  - **Fix:** register the control subcommands and provider values with the parser the runtime actually uses; do not infer provider candidates from help prose.
- **Nested shells:** `bash`, `sh`, and `kabibi` are Kabibi-defined command handlers with a manual flag loop in `shell.go`. It handles `-c`/`--command`, `-s`, compatibility flags, `-o`/`+o`, script/command arguments, and forwards other flags.
  - **Fix:** share that parser’s state with completion and transition to shell-command or script-path completion at the appropriate position.
- **App-level `quit`:** it is handled as an exact standalone input by `exec_session.go`, not by a builtin argument parser. Treat it as a no-argument app command; `exit` also has the normal shell builtin’s status argument.

### Bottom line

The doc’s shared-definition contract can cover these cases, but “extract flags from a `FlagSet`” is only the first layer. u-root positional grammars, handwritten parsers, mvdan builtins, Git’s dependency parser plus Kabibi intercepts, browser commands, AI control commands, and nested shells all need parser-owned completion state that runtime also consumes. Runtime registration must distinguish actual implementations from names that are merely recognized, and honor dispatch precedence/aliases rather than merge command-name registries blindly.

