# Step 1 Plan: Restoring Kabibi Plume Shell & Inline AI Fallback

## 1. Objective & Scope

Reimplement Kabibi's foundational interactive layer on top of the new `ui.Host` / `terminal.CellBuf` architecture (`kabibi/ui/app`, `kabibi/ui/shell`) without importing legacy BubbleTea code from `kabibi/old`:

1. **Functional Plume Shell (`mvdan.cc/sh/v3`):** Connect the `Shell` widget's `CommandBlock` plume and scrollback ejection engine to a live, persistent `mvdan.cc/sh/v3/interp.Runner` session with u-root `coreutils` builtins and platform-aware executable resolution.
2. **Smart Dispatch (Shell First $\rightarrow$ AI Fallback):**
   - If the user's input is valid shell syntax and resolves to a shell construct, builtin, function, script, or executable, run it immediately in the shell.
   - If the user's input fails `mvdan.cc/sh/v3/syntax` parsing **or** fails executable/command lookup (`command not found`), seamlessly route the raw input as an AI chat prompt into `ai.Session`.
3. **Unified Plume Streaming:** Stream both `OnFeedback` (the `\r`-overwritten single-line deliberation ticker `[goal] …`, `[steps] …`, tool progress, and `🗜️` compaction notices) and `OnContent` (the final answer) directly into the active `CommandBlock.Output` buffer, rendered through `terminal.FromANSI` just like standard shell output.
4. **Kabibi-Native AI Tools & Shell Awareness:** Implement Kabibi-owned tools (`exec`, `read_file`, `write_file`, `shell_history`, `shell_state`) wired directly to Kabibi's shell session, current working directory (`cwd`), environment, and command history between AI turns.

---

## 2. Architecture Overview

```
┌────────────────────────────────────────────────────────────────────────────┐
│                                 ui.Host                                    │
│        (Raw mode, double-buffered CellBuf diffing, WriteToScrollback)      │
└─────────────────────────────────────┬──────────────────────────────────────┘
                                      │
                                      ▼
┌────────────────────────────────────────────────────────────────────────────┐
│                        ui/shell.Shell (ui.Widget)                          │
│  • CommandBlock slice (Prompt, Input, Output bytes.Buffer, Kind, ExitCode) │
│  • Plume viewport & scrollback ejection watermark                          │
│  • Line editor & live input routing                                        │
└──────────────┬──────────────────────────────────────────────┬──────────────┘
               │                                              │
               ▼                                              ▼
┌──────────────────────────────────────┐    ┌────────────────────────────────┐
│       shell.ExecutionSession         │    │          ai.Session            │
│  • Persistent mvdan interp.Runner    │◄───│  • 7-stage contemplation       │
│  • CWD & env tracking                │    │  • EmbeddingGemma 2 + RAG      │
│  • Syntax + command classifier       │    │  • OnFeedback / OnContent      │
│  • Monotonic CommandRecord log       │    │  • Kabibi-native tool suite    │
└──────────────────────────────────────┘    └────────────────────────────────┘
```

### Key Design Principles

* **Coordinator-Safe UI Updates:** `Shell.blocks` is mutated safely and redraws are triggered via `Host.Invalidate()`. Because `CommandBlock.Output` is passed through `terminal.FromANSI` (`vt.Emulator`), carriage-return (`\r`) overwrites emitted by `ai.Session`'s `OnFeedback` ticker naturally overwrite the current line inside the block without special UI modes.
* **Zero Legacy Coupling:** The obsolete `kabibi/ai` and `kabibi/ui/chat` legacy FFI wrappers have been removed. Wire `kabibi/go.mod` directly to `ai => ../ai` and `ai/assets`.
* **No Tool Sharing with Pope:** All AI tools are implemented inside Kabibi and bound to Kabibi's shell state (`cwd`, `env`, and `mvdan` runner).

---

## 3. Command vs. AI Fallback Classification Rules

When the user presses `Enter` on a non-empty input line $L$:

### Stage A: Fast Pre-Flight Classification (`ClassifyInput`)

1. **Explicit Shell / Builtin Check:**
   - Handle immediate UI/session builtins (`exit`, `quit`) cleanly.
2. **Syntax Parse (`mvdan.cc/sh/v3/syntax`):**
   - Parse $L$ with `syntax.NewParser().Parse(strings.NewReader(L), "")`.
   - **Case 1 — Syntax Error:** E.g., `"What's the largest file in this directory?"` (unmatched quote) or `" list files in (current) folder"`.
     - **Action:** Route immediately to `ai.Session.RunTurn(ctx, L)`. Do **not** print a bash syntax error.
3. **AST Head Inspection (when syntax is valid):**
   - Inspect the parsed `*syntax.File`:
   - **Case 2 — Compound Shell Construct:** If the statement uses shell control flow (`if`, `for`, `while`, `case`, subshells, blocks), variable assignments (`FOO=bar`), pipelines (`|`), redirections (`>`, `<`), or logical chains (`&&`, `||`, `;`) where the leading command is resolvable:
     - **Action:** Execute as a shell command.
   - **Case 3 — Simple Command (`*syntax.CallExpr`):** Extract the first literal word $W_0$ (the command name) and check resolvability in order:
     1. **Shell Builtin / Keyword:** `cd`, `pwd`, `echo`, `export`, `set`, `unset`, `alias`, `type`, `true`, `false`, `test`, `[`, `source`, `.`, `eval`, `exec`, `shift`, `wait`, etc.
     2. **User-Defined Shell Function:** Present in `runner.Funcs`.
     3. **u-root Coreutils Builtin:** Registered in `moreinterp/coreutils` (`ls`, `cat`, `grep`, `find`, `cp`, `mv`, `rm`, `mkdir`, `head`, `tail`, `wc`, `sort`, `uniq`, `touch`, `chmod`, etc.).
     4. **Explicit Path / Script / Host Executable:** Resolved against the shell's current `runner.Dir` and `runner.Env` (`PATH` + Windows `PATHEXT` / `.sh` / shebang via `resolvePlatformExecutable` and `interp.LookPathDir`).
   - **If $W_0$ resolves to any of (1)–(4):**
     - **Action:** Execute immediately in the `mvdan` shell runner.
   - **If $W_0$ does NOT resolve (Unknown Command / Natural Language):**
     - E.g., `Tell me why the last build failed` or `how do I undo the last commit`: syntax is a valid `CallExpr` with head `"Tell"` or `"how"`, neither of which is a builtin or executable on `PATH`.
     - **Action:** Route directly to `ai.Session.RunTurn(ctx, L)`.

### Stage B: Runtime Exec-Not-Found Safety Net

If a command passes pre-flight (for example, dynamic variable expansion `$CMD` or a single command whose lookup fails inside `ExecHandler` before emitting any stdout), and the runner fails solely with command-not-found on the root command:
- Suppress the raw `executable file not found` diagnostic and fall back to `ai.Session.RunTurn(ctx, L)`.
- If a valid command runs and a subcommand inside a script/pipeline fails later, record it as a normal shell failure (exit code 127) so genuine script errors are not masked.

---

## 4. Plume & Streaming Mechanics

### 1. `CommandBlock` Lifecycle & `FromANSI` Rendering

Currently in `kabibi/ui/shell/shell_widget.go`, `renderBlock` splits `block.Output.String()` on `\n` before calling `terminal.FromANSI(lines, width)`.
- **Important Refinement:** In `ai.Session`, `OnFeedback` uses `\r` + spaces + `\r` (without `\n`) to overwrite the single-line `[goal]`, `[expectation]`, `[steps]`, `[plan]`, and `[work]` ticker in place, and then wipes the line with `\r   \r` right before `OnContent` streams the final answer.
- If `renderBlock` naively splits on `\n` and `FromANSI` appends `\r\n` after every slice element, a live `\r` ticker works within a single line, **provided** we don't prematurely split or lose trailing `\r` state.
- Passing the raw byte stream (or preserving `\r` semantics inside `FromANSI`) ensures that:
  1. While the AI is deliberating, the user sees `[goal] ...` $\rightarrow$ `[steps] ...` $\rightarrow$ `[work] ...` updating live on the line below `$ <prompt>`.
  2. When a tool runs, `📖 main.go ... 1204 bytes read\n` stays on its own line in the plume, and `[work] ...` resumes on the next line.
  3. When Convergence begins, `streamer.Wipe()` clears `[work] ...` and `OnContent` streams the concise final response in its place.
  4. As the block grows beyond viewport height, the existing ejection engine (`previouslyEjected` watermark) automatically flushes top rows into `Host.WriteToScrollback`.

### 2. Interactive Cancellation (`Ctrl+C`)

- Each active turn (whether shell command or AI prompt) holds a cancellable `context.Context` stored on the `Shell`.
- Pressing `Ctrl+C` while a command or AI turn is `StatusActive` cancels that per-turn `ctx` (stopping the running subprocess or aborting `ai.Session.RunTurn`) and returns focus to the shell prompt instead of killing the entire Kabibi process.
- Pressing `Ctrl+C` on an idle prompt clears the current input line (or exits on `exit`/`quit`).

---

## 5. Shell History Tracking & Kabibi-Native AI Tools

### 1. Shell Activity Log (`CommandRecord`)

Every shell command executed by the user (and optionally tagged when executed by the AI's `exec` tool) is recorded in a ring/slice on the shell session:

```go
type CommandSource string

const (
	SourceUserShell CommandSource = "user_shell"
	SourceAITool    CommandSource = "ai_tool"
)

type CommandRecord struct {
	ID        int           `json:"id"`
	Source    CommandSource `json:"source"`
	Command   string        `json:"command"`
	Cwd       string        `json:"cwd"`
	Output    string        `json:"output"` // ANSI-stripped or bounded raw output
	ExitCode  int           `json:"exit_code"`
	StartedAt time.Time     `json:"started_at"`
	Duration  string        `json:"duration"`
}
```

The session also tracks `lastAITurnCommandID`: the highest `CommandRecord.ID` that existed at the end of the previous AI turn. This lets the AI immediately query **"what happened in the shell since the last AI prompt"**.

### 2. Kabibi Tool Suite (`kabibi/ui/shell/ai_tools.go` or `kabibi/tools`)

All tools are registered in `ai.SessionConfig.Tools` (`map[string]ai.Tool`). Remember `ai.NewSession` automatically injects the required `"explanation"` parameter into every tool schema.

| Tool Name | Purpose | Parameters (besides automatic `explanation`) | Behavior |
| :--- | :--- | :--- | :--- |
| **`shell_history`** | Retrieve past bash commands & outputs (especially commands run between AI prompts). | `since_last_ai_turn` (bool, optional): if true, returns all commands since the previous AI turn.<br>`count` (int, optional, default 10): number of recent commands to fetch.<br>`include_output` (bool, optional, default true): whether to include stdout/stderr. | Reads from the shell's `CommandRecord` log. Emits brief `🐚 history ... ok` feedback via `LogFeedback`. |
| **`shell_state`** | Inspect current shell working directory, OS, recent exit status, and selected environment variables. | `env_keys` ([]string, optional): specific env vars to inspect. | Reads live `runner.Dir`, `runtime.GOOS`, last command status/exit code, and requested env vars from `runner.Env`. |
| **`exec`** | Execute a command inside Kabibi's shell execution handling. | `command` (string, required): command line to execute in the shell. | Runs through Kabibi's `mvdan` shell runner rooted at the shell's current `cwd` and environment (updating `cwd`/env or running a sub-runner in `runner.Dir`). Streams `💻 <cmd> ... ok/failed` to `LogFeedback` and returns `{"output": ..., "exit_code": ..., "cwd": ...}`. |
| **`read_file`** | Read a local file relative to the shell's current working directory. | `path` (string, required)<br>`start_line` (int, optional)<br>`end_line` (int, optional) | Resolves relative paths against the shell's current `runner.Dir` (not the initial process `os.Getwd()`). Emits `📖 <file> ... <N> bytes read`. |
| **`write_file`** | Write or overwrite a local file relative to the shell's current working directory. | `path` (string, required)<br>`content` (string, required) | Resolves relative paths against the shell's current `runner.Dir`. Emits `📝 <file> ... ok`. |
| **`list_dir`** | List files and directories relative to the shell's current working directory. | `path` (string, optional, defaults to `.`) | Resolves against `runner.Dir` and returns structured entries (name, type, size). |

*(Note: Furthermore, when the user runs one or more shell commands and then asks an AI question, we can either let the model call `shell_history` or lightly annotate the system/context with the current `cwd` and the count/summary of commands run since the last AI prompt so even Stage 1 triage knows whether it needs `GOAL:` to inspect the failed command output.)*

---

## 6. Step-by-Step Implementation Phases

### Phase 1: Wire `ai` Module & Asset Bootstrapping into `kabibi`
1. Add `require ai v0.0.0-00010101000000-000000000000` and `replace ai => ../ai` to `kabibi/go.mod`.
2. Create an AI bootstrapper (using `ai/assets.EnsureRuntime`, `ai/assets.EnsureModel` for Gemma 4 E2B and EmbeddingGemma 2, `ai.NewEngine`, `ai.NewEmbedder`, and `ai.NewSession`) that initializes lazily on first AI fallback or asynchronously at startup so the shell prompt appears instantaneously.

### Phase 2: Complete `mvdan` Execution Engine & Plume Streaming in `kabibi/ui/shell`
1. Wire `createRunner` in `kabibi/ui/shell/shell.go` into a persistent `Shell` session state (`runner *interp.Runner`, `cwd string`, thread-safe active block writer).
2. Update `Shell.handleKeyPress("enter")` (and paste/special keys) in `kabibi/ui/shell/shell_widget.go` to replace the `[Command not yet implemented in Step 1]` placeholder with real asynchronous execution.
3. Ensure `renderBlock` / `terminal.FromANSI` handles live `\r` line overwrites and streaming byte chunks cleanly, calling `invalidate()` as output arrives.
4. Update the prompt dynamically (e.g. reflecting current directory or `$ `) and record every completed shell command into the `CommandRecord` history slice.

### Phase 3: Implement Syntax & Executable Classifier (`ClassifyInput`)
1. Implement `ClassifyInput(input string) InputRoute` in `kabibi/ui/shell`:
   - `RouteBuiltin` (`exit`, `quit`)
   - `RouteShell` (valid `mvdan` syntax + resolvable command/construct)
   - `RouteAI` (`syntax.NewParser` error OR unresolvable head command)
2. Add comprehensive unit tests in `kabibi/ui/shell` covering:
   - Valid builtins (`cd ..`, `echo "hi"`, `ls -la`) $\rightarrow$ `RouteShell`
   - Valid compound bash (`for i in 1 2; do echo $i; done`, `FOO=1 env`, `cat a.txt | grep x`) $\rightarrow$ `RouteShell`
   - Valid host executables (`go version`, `./script.sh`) $\rightarrow$ `RouteShell`
   - Syntax errors (`what's in this folder?`, `explain (this) error`) $\rightarrow$ `RouteAI`
   - Valid syntax but unknown command (`name a bridge in Paris`, `why did my test fail`) $\rightarrow$ `RouteAI`

### Phase 4: Implement Kabibi-Native AI Tools & Wire `ai.Session` Fallback
1. Implement Kabibi's dedicated tools (`exec`, `read_file`, `write_file`, `list_dir`, `shell_history`, `shell_state`) in `kabibi/ui/shell`, bound to the shell's live `cwd`, `env`, `runner`, and `CommandRecord` log.
2. Connect `RouteAI` to `ai.Session.RunTurn(ctx, input)` with:
   - `OnFeedback`: writes directly to the active `CommandBlock.Output` and calls `invalidate()`.
   - `OnContent`: writes directly to the active `CommandBlock.Output` and calls `invalidate()`.
3. Update `lastAITurnCommandID` after each AI turn completes.

### Phase 5: Verification & Tests
1. Unit test the classifier, shell command execution, `cwd` persistence across `cd` + AI `read_file`/`exec`, and `shell_history` retrieval of interstitial bash commands.
2. Verify snapshot tests in `kabibi/ui/app` and `go -C kabibi vet ./...` (both native and with the Rusticated WASM overlay).
