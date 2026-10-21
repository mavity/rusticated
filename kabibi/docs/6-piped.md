# Kabibi Architectural Plan: Three-Way Classifier, Subtle Inline Syntax Highlighting, and Headless Piped Shell

## 1. Executive Summary & Staged Roadmap

Kabibi is a hybrid bash shell and natural-language AI assistant. To unify interactive TTY operation, live prompt feedback, and non-TTY piped execution under a single deterministic model, implementation proceeds in **three sequential stages**:

1. **Stage 1 — Pre-Execution Three-Way Input Classifier & Multiline TTY Continuation (Prerequisite):**
   - Upgrade `ExecutionSession.ClassifyInput` from a post-hoc execution fallback into a deterministic **pre-execution classifier** that distinguishes:
     1. **`RouteEmpty`**: blank or comment-only input.
     2. **`RouteBuiltin`**: standalone `exit` or `quit`.
     3. **`RouteIncomplete`**: syntactically incomplete bash construct (e.g. `if true; then`, `for i in 1 2; do`, trailing `|`/`&&`/`\`, unclosed `{`/`(`/`'`, or unclosed here-doc) whose leading command token is a valid shell keyword, builtin, function, script, or executable.
     4. **`RouteShell`**: complete, syntactically valid bash command or compound block whose leading command resolves against the session's full command-resolution chain.
     5. **`RouteAI`**: natural-language prompt — either because the leading command token does not resolve as a shell command/keyword/assignment, or because the input contains a bash syntax error that is not a valid in-progress shell construct.
   - Integrate `RouteIncomplete` into interactive TTY mode (`Shell` + `PromptInput`) so pressing `Enter` on an incomplete shell block inserts a newline and continues editing rather than prematurely executing or routing to AI.

2. **Stage 2 — Classifier-Driven Subtle Inline Syntax Highlighting (`PromptInput`):**
   - Built **directly on the back of Stage 1's classifier** (not as an independent lexer pass).
   - Serves as immediate visual validation of the classifier while typing:
     - If the classifier routes the current buffer to **`RouteAI`**, the input is **not highlighted as bash** — it renders cleanly in the default terminal foreground style.
     - If the classifier routes the current buffer to **`RouteShell`**, **`RouteBuiltin`**, or **`RouteIncomplete`**, tokens are highlighted using a **muted, desaturated mid-luminance palette** (greyish-blue, greyish-green, greyish-orange, greyish-violet, slate-grey) calibrated to remain legible and understated on **both dark/black and light/white terminal backgrounds**.

3. **Stage 3 — Early-Fork Headless Piped Mode (`kabibi/piped.go` + `kabibi/main.go`):**
   - When `kabibi` is invoked with no one-shot flags and non-TTY standard streams (`!term.IsTerminal(os.Stdin.Fd())`), fork early in `kabibi/main.go` to `runPipedSession` — bypassing `app.Boot()`, `DualPaneWidget` (file panels permanently absent), and `ui.Host` (no raw mode, no 80ms spinner ticker).
   - Process standard input incrementally line-by-line using the **exact same Stage 1 three-way classifier**: accumulating multiline blocks while `RouteIncomplete`, executing complete `RouteShell` blocks immediately (without waiting for whole-stream EOF), and routing `RouteAI` prompts to `LazyAIRunner`.
   - Retain PTY execution (`pty.Start` and `ManagedPTY`) for child processes spawned by shell commands and AI `exec` tools.

---

## 2. Stage 1: Three-Way Pre-Execution Classifier & Multiline TTY Support

### 2.1 Why Pre-Execution Command Resolution Is Required

Previously, `ClassifyInput` only ran `syntax.NewParser().Parse`:
- Single-line plain-English prompts without punctuation (e.g. `Name a bridge in Paris`) parsed as valid bash (`CallExpr` with command `Name` and 4 arguments), returning `RouteShell` at classification time and only falling back to AI during `RunCommand` via `ErrUnknownCommandAtStart`.
- English prompts containing an apostrophe (e.g. `What's the largest file in this directory?`) caused `mvdan/sh` to report an unclosed single quote (`syntax.IsIncomplete(err) == true`).

To support both **multiline shell continuation** and **classifier-driven syntax highlighting**, the classifier must distinguish **before execution** whether the user is typing a real shell construct or a natural-language sentence.

### 2.2 Pre-Execution Command Resolution (`ExecutionSession.CanResolveCommand`)

Add a read-only resolution helper on `ExecutionSession` that mirrors the exact execution middleware chain in `shell.go` without spawning or mutating anything:

1. **Shell Keywords & Control Structures:**
   - Reserved words (`if`, `for`, `while`, `until`, `case`, `select`, `function`, `{`, `(`, `[[`, `((`, `!`, `time`, `coproc`).
2. **Pure Variable Assignments / Redirections:**
   - A `CallExpr` with `len(Assigns) > 0` and `len(Args) == 0` (e.g. `FOO=bar`), or bare redirections.
3. **Session & Interpreter Builtins:**
   - Lifecycle builtins (`exit`, `quit`, `help`).
   - Standard POSIX/Bash builtins via `interp.IsBuiltin(name)`.
   - User-defined shell functions registered in `es.runner.Funcs[name]`.
   - In-process nested shell names via `isNestedShellBuiltinName(name)` (`bash`, `sh`, `kabibi`).
   - Custom Kabibi builtins: `git` (`gitBuiltinMiddleware`), `web` and `cdp` (`webBuiltinMiddleware`), and all registered u-root coreutils via `LookupURootCommand(name)`.
4. **Scripts & Host Executables:**
   - Relative or absolute `.sh` or `#!` script files in `es.Dir()`.
   - Platform-aware host executables via `resolvePlatformExecutable(name, es.Dir(), es.EnvMap())` (honoring Windows `PATHEXT` and `PATH`) and `interp.LookPathDir`.

### 2.3 Classifier Decision Matrix (`ClassifyInput`)

Given raw input string $S$:

1. **Empty / Comment Check:**
   - If `strings.TrimSpace(S) == ""`, return `RouteEmpty`.
2. **Leading Token Pre-Check (Disambiguating English Contractions from Multiline Shell):**
   - Extract the first statement/command head token of $S$.
   - If the head token is a plain literal word that is **not** a bash keyword, **not** a variable assignment (`NAME=val`), and `es.CanResolveCommand(head) == false` (for example, `What's` in `What's the largest file?` or `Name` in `Name a bridge in Paris`), classify immediately as **`RouteAI`**.
   - This prevents English contractions (`What's`, `don't`, `I'm`) from being misclassified as unclosed single-quoted shell strings, and ensures `Name a bridge in Paris` is classified as `RouteAI` right away (so Stage 2 never highlights it as bash).
3. **Trailing Unescaped Backslash Check:**
   - Because `mvdan/sh` treats `"echo hi \\\n"` at EOF as a complete command, explicitly check whether $S$ ends with an unescaped trailing `\` (or `\\\n` continuation). If the leading command resolves, return **`RouteIncomplete`**.
4. **AST Parse (`syntax.NewParser(syntax.KeepComments(true)).Parse`):**
   - **Case A — `err == nil`:**
     - If `len(file.Stmts) == 0`: return `RouteEmpty` (comment-only input).
     - If standalone `exit` or `quit`: return `RouteBuiltin`.
     - Inspect the first top-level statement (`file.Stmts[0]`):
       - If it is a compound command (`IfClause`, `ForClause`, `WhileClause`, `CaseClause`, `Block`, `Subshell`, `FuncDecl`, `ArithmCmd`, `TestClause`, `DeclClause`, `TimeClause`, `CoprocClause`), return **`RouteShell`**.
       - If its leading `CallExpr` has assignments with no command word, or its literal command word satisfies `es.CanResolveCommand(head)` (or uses dynamic expansion like `$CMD`), return **`RouteShell`**.
       - Otherwise (the first command word is an unknown literal token like `Name` in `Name a bridge in Paris`), return **`RouteAI`**.
   - **Case B — `err != nil && syntax.IsIncomplete(err)`:**
     - Because Step 2 already verified that the leading token is a valid shell keyword, assignment, or resolvable command, an incomplete parse error (`unclosed quote`, `then must be followed by a statement list`, `| must be followed by a statement`, unclosed `{`/`(`/`<<EOF`) is genuinely an in-progress shell construct $\rightarrow$ return **`RouteIncomplete`**.
   - **Case C — `err != nil && !syntax.IsIncomplete(err)`:**
     - Definitive syntax error (e.g. `explain (in detail) why the build broke` or malformed syntax) $\rightarrow$ return **`RouteAI`**.
   - *Defense in depth:* Keep `ErrUnknownCommandAtStart` in `RunCommand` as a safety net in case an executable disappears between classification and execution.

### 2.4 Interactive TTY Multiline Continuation (`PromptInput` & `Shell`)

1. **`PromptInput` Newline Preservation:**
   - Update `PromptInput.SetValue` and `PromptInput.insertText` so embedded `\n` newlines are preserved (normalizing `\r\n` $\rightarrow$ `\n`) instead of flattening newlines to spaces. `buildVisualLayout` in `kabibi/ui/shell/prompt_input.go` already supports `\n` row breaks!
   - Add `InsertNewline()` on `PromptInput` to insert `\n` at `cursorPos`.
2. **`Enter` Key Handling in `Shell.handleKeyEvent`:**
   - When the user presses `Enter` in TTY mode:
     - Query `route, _ := s.execSess.ClassifyInput(s.input.Value())`.
     - If `route == RouteIncomplete`, call `s.input.InsertNewline()` and `s.invalidate()` instead of `s.submitAndExecuteAsync()`.
     - If `route` is `RouteShell`, `RouteAI`, `RouteBuiltin`, or `RouteEmpty`, submit and execute as normal.
   - Pressing `Ctrl+C` while editing a multiline buffer resets the entire buffer cleanly.

---

## 3. Stage 2: Classifier-Driven Subtle Inline Syntax Highlighting

### 3.1 Core Rule: Highlighting Follows the Classifier

Syntax highlighting in `PromptInput` is **strictly gated by the Stage 1 classifier**:
- `Shell` wires a classifier/highlighter callback into `PromptInput`.
- On every render/update of `PromptInput.Value()`:
  1. Run the Stage 1 classifier on `value`.
  2. **If `route == RouteAI`:** Do **not** highlight as bash. Every rune of `value` uses the default terminal foreground (`CharResetForegroundDefault`), visually confirming to the user that Kabibi sees the line as natural-language AI chat.
  3. **If `route` is `RouteShell`, `RouteBuiltin`, or `RouteIncomplete`:** Apply subtle bash token highlighting across the input runes.

### 3.2 Dual-Background Subtle Colour Palette

To avoid a loud or "childish" appearance and ensure legibility on **both black (`#000000`–`#1E1E1E`) and white (`#FFFFFF`–`#F5F5F5`) terminal backgrounds**, all highlight colors use **muted mid-luminance, desaturated RGB values** (approximate relative luminance $Y \approx 0.22\text{–}0.35$, yielding balanced contrast against both extremes):

| Token Category | Visual Description | Hex RGB | `terminal.Color` | Applies To |
| :--- | :--- | :--- | :--- | :--- |
| **Default / AI Text** | Terminal Default | — | `CharResetForegroundDefault` | Plain arguments, paths, and **entire buffer when `RouteAI`** |
| **Keywords & Builtins** | Greyish-Blue | `#5B82A6` | `0x5B82A6` | `if`, `then`, `else`, `fi`, `for`, `in`, `do`, `done`, `while`, `case`, `esac`, `echo`, `cd`, `export`, resolved command head |
| **Strings & Quotes** | Greyish-Green | `#5E8C6A` | `0x5E8C6A` | `'...'`, `"..."`, heredoc bodies |
| **Variables & Numbers** | Greyish-Orange | `#A67C52` | `0xA67C52` | `$VAR`, `${VAR}`, `$1`, `FOO=` assignment names, numeric literals |
| **Operators & Pipes** | Greyish-Violet | `#856B9E` | `0x856B9E` | `\|`, `&&`, `\|\|`, `;`, `>`, `>>`, `<`, `2>&1`, `(`, `)`, `{`, `}` |
| **Comments** | Muted Slate-Grey | `#76808A` | `0x76808A` | `# comment` |

### 3.3 Token Mapping into `buildVisualLayout`

- `buildVisualLayout` in `kabibi/ui/shell/prompt_input.go` accepts an optional per-rune style slice `[]terminal.Style` (or `StyleFunc`) for `value`:
  - The prompt prefix (`~/rustica፧`) always renders in its standard default style.
  - For `RouteShell` / `RouteBuiltin` / `RouteIncomplete`, lexer tokens (via `mvdan/sh/v3/syntax` AST walk or `chroma` bash lexer gated by the classifier) map byte/rune spans of `value` to the 5 subtle styles above.
  - Even when `route == RouteIncomplete` (for example, while the user is mid-way through typing `if [ -f foo ]; then` or an unclosed quote `'hello`), the lexer highlights the recognized keywords, operators, and partial string in real time.

---

## 4. Stage 3: Headless Piped Mode (`kabibi/piped.go`)

### 4.1 Entrypoint Routing & Non-TTY Guarantees (`kabibi/main.go`)

```mermaid
flowchart TD
    A["runCLI(args, stdout, stderr)"] --> B{"Explicit Flags?\n(-c, -p, --edit, help)"}
    B -- "-c / -p" --> C["runOneShotLine / runOneShotAIPrompt"]
    B -- "--edit" --> D["app.Boot() -> OpenEditor -> host.Run()"]
    B -- "help" --> E["printCLIHelp"]
    B -- "None" --> F{"Positional Script File?"}
    F -- "Yes (.sh / file)" --> G["runOneShotLine(fileContents)"]
    F -- "No" --> H{"term.IsTerminal(os.Stdin.Fd())?"}
    H -- "true (Interactive TTY)" --> I["app.Boot() -> host.Run()\n[Full TUI Mode: Dual-Pane Blue HUD, Stack + Highlighted Input]"]
    H -- "false (Piped / Redirected)" --> J["runPipedSession(os.Stdin, stdout, stderr, opts)\n[Headless Mode: No TUI, No Anim, No File Panels, Incremental Stream]"]
```

When `term.IsTerminal(int(os.Stdin.Fd()))` is `false`:
1. **No TUI or Raw Mode:** `app.Boot()` and `host.Run()` are never called; `term.MakeRaw` is never invoked; no cursor-positioning or screen-clearing ANSI sequences are emitted by Kabibi.
2. **File Panels Permanently Disabled:** `AppWidget` and `DualPaneWidget` are never instantiated; `Ctrl+O` and F-key dialogs do not exist in this mode.
3. **Animations Permanently Silenced:** Because `Shell` widget's 80ms `stackSpinnerFrames` ticker lives in `shell_widget.go`, bypassing `AppWidget`/`Shell` guarantees zero spinner frames are ever emitted. In `runPipedAI`, `onFeedback` is silenced by default (or logged as plain `[ai] ...` lines to `stderr` only when `--verbose` is set, stripping any `\r` spinner rewrites).

### 4.2 Incremental Line-by-Line & Block-Aware Stream Loop (`runPipedSession`)

Unlike a naive batch runner that slurps all of `stdin` until EOF before running the first command, `runPipedSession` uses `bufio.Reader` (`ReadString('\n')`, avoiding `bufio.Scanner`'s 64KB token limit) to process input **incrementally as lines arrive**:

1. Maintain an accumulator buffer `var pending strings.Builder`.
2. Read the next line from `stdin`:
   - If `pending.Len() == 0` and the line is blank or a `#` comment, skip it immediately.
   - Append the line to `pending`.
3. Classify `current := pending.String()` using Stage 1's `route, parsed := es.ClassifyInput(current)`:
   - **`RouteIncomplete`**:
     - If **not at EOF**, keep reading the next line into `pending` (allowing `if ... then ... fi`, `for ... do ... done`, trailing `\`, trailing `|`, and here-docs to span multiple lines naturally).
     - If **at EOF** (the stream ended while a shell construct was still unclosed), attempt `es.RunCommand` so `mvdan/sh` reports a deterministic syntax error and returns exit code `2` (rather than hanging or silently dropping the buffer).
   - **`RouteEmpty`**:
     - `pending.Reset()` and continue.
   - **`RouteBuiltin`** (`exit` / `quit`):
     - Return `lastExitCode` immediately.
   - **`RouteShell`**:
     - `pending.Reset()`.
     - Execute `rec, runErr := es.RunCommand(ctx, current, parsed, shell.SourceUserShell, writeStdout)`.
     - If `errors.Is(runErr, shell.ErrUnknownCommandAtStart)` (safety-net fallback), route `current` to `runPipedAI`.
     - Otherwise update `lastExitCode = rec.ExitCode`.
   - **`RouteAI`**:
     - `pending.Reset()`.
     - Execute `runPipedAI(ctx, aiRunner, strings.TrimSpace(current), stdout, stderr, opts.verbose)`.
     - Record the AI turn in `es` history so subsequent shell commands or AI turns in the same piped stream share full session context.

### 4.3 Retained PTY Execution for Child Processes & AI Tools

Even though Kabibi itself reads from a non-TTY `stdin` and streams to `stdout`/`stderr`:
1. **User Shell Child Processes (`runResolvedCommand` in `shell.go`):**
   - Continue to launch external executables in a PTY (`pty.Start` with `80x24` default geometry from `ExecutionSession`), pumping output chunks directly to `stdout`.
   - Because `ExecutionSession.ttyIn` is a closed pipe reader, unredirected child commands receive immediate EOF on their stdin rather than stealing remaining command lines from Kabibi's `stdin` script stream. Commands with explicit input redirection or pipelines (`echo foo | grep foo`, `cat <<EOF`) receive their redirected stdin normally.
2. **AI `exec` Tool Child Processes (`ExecuteAIToolCommand` in `exec_session.go`):**
   - Continue to use `ManagedPTY` (`pty.Start`) + virtual terminal settling (`terminal.FromANSI` / `CleanBytesToText`) and background promotion (`term_read`, `term_input`, `term_kill`).

---

## 5. Summary Comparison Across Modes

| Capability | Interactive TUI Mode (`isTTY == true`) | Headless Piped Mode (`isTTY == false`) |
| :--- | :--- | :--- |
| **Entrypoint** | `app.Boot()` $\rightarrow$ `host.Run()` | `runPipedSession()` in `kabibi/piped.go` |
| **Terminal Raw Mode** | Yes (`term.MakeRaw`) | No (standard streams) |
| **Blue File Panels (`Ctrl+O`)** | Available (hidden by default) | **Permanently disabled (not instantiated)** |
| **80ms Spinner Animation** | Active on running block | **Permanently silenced** |
| **Input Classification** | Shared 3-way `es.ClassifyInput` | Shared 3-way `es.ClassifyInput` |
| **Multiline `if`/`for`/quotes** | `Enter` inserts `\n` while `RouteIncomplete` | Accumulates lines while `RouteIncomplete` |
| **Inline Syntax Highlighting** | Subtle 5-color bash palette; disabled on `RouteAI` | N/A (no prompt echo) |
| **Child Process & AI PTY** | Enabled (`pty.Start` / `ManagedPTY`) | **Enabled (`pty.Start` / `ManagedPTY`)** |
| **Session State (`cd`, `export`)** | Persistent `ExecutionSession` | Persistent `ExecutionSession` |

---

## 6. Implementation & Verification Plan

### Step 1: Three-Way Classifier & Multiline TTY Continuation
- **Files:** `kabibi/ui/shell/exec_session.go`, `kabibi/ui/shell/prompt_input.go`, `kabibi/ui/shell/shell_widget.go`
- **Tasks:**
  1. Add `RouteIncomplete` to `InputRoute`.
  2. Implement `ExecutionSession.CanResolveCommand(name string) bool` covering builtins, functions, nested shells (`bash`/`sh`/`kabibi`), `git`/`web`/`cdp`, u-root commands, local scripts, and `PATH`/`PATHEXT` executables.
  3. Update `ExecutionSession.ClassifyInput` to return `RouteIncomplete`, `RouteShell`, `RouteAI`, `RouteBuiltin`, or `RouteEmpty` prior to execution.
  4. Preserve `\n` in `PromptInput` and update `Shell.handleKeyEvent` so pressing `Enter` when `ClassifyInput` returns `RouteIncomplete` inserts a newline instead of submitting.
- **Unit Tests (`kabibi/ui/shell/shell_ai_test.go`, `prompt_input_test.go`):**
  - Verify `if true; then`, `for i in 1 2; do`, `echo "unclosed`, `echo hi |`, and `echo hi \` classify as `RouteIncomplete`.
  - Verify `Name a bridge in Paris`, `What's the largest file?`, and `explain (in detail) why` classify immediately as `RouteAI`.
  - Verify pressing `Enter` in TTY `Shell` on `if true; then` inserts `\n` and waits until `fi` before executing.

### Step 2: Classifier-Driven Subtle Inline Syntax Highlighting
- **Files:** `kabibi/ui/shell/prompt_input.go`, `kabibi/ui/shell/shell_widget.go`
- **Tasks:**
  1. Add the muted dual-background palette (`#5B82A6`, `#5E8C6A`, `#A67C52`, `#856B9E`, `#76808A`) and classifier-backed rune styler to `PromptInput`.
  2. Ensure `RouteAI` inputs render completely unhighlighted (default foreground), while `RouteShell`, `RouteBuiltin`, and `RouteIncomplete` inputs highlight keywords, strings, variables, operators, and comments.
- **Unit Tests (`kabibi/ui/shell/prompt_input_test.go`):**
  - Verify `echo "hello" | cat $FOO` produces distinct subtle foreground colors on keyword, string, operator, and variable cells.
  - Verify `Name a bridge in Paris` and `What's the time?` render with default foreground style on every rune (`CharResetForegroundDefault`).
  - Verify incomplete bash (`if true; then`) still highlights `if`, `true`, `;`, and `then`.

### Step 3: Headless Piped Mode
- **Files:** `kabibi/piped.go`, `kabibi/main.go`, `kabibi/piped_test.go`
- **Tasks:**
  1. Implement `runPipedSession` and `runPipedAI` in `kabibi/piped.go` using incremental `bufio.Reader` line reading and Stage 1's `ClassifyInput`.
  2. Update `runCLI` in `kabibi/main.go` to check `term.IsTerminal(int(os.Stdin.Fd()))` and route non-TTY invocations to `runPipedSession`.
- **Unit Tests (`kabibi/piped_test.go`):**
  - Persistent state across piped lines (`export FOO=bar` followed by `echo $FOO`).
  - Multiline compound statements (`if`/`then`/`fi`, `for`/`do`/`done`, heredocs) streamed across lines.
  - Mixed bash commands and AI prompts in the same piped session.
  - Verification that no spinner animations or TUI escape sequences are emitted, and exit codes propagate accurately (including unclosed syntax at EOF returning non-zero).
