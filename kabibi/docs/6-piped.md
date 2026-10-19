# Piped Mode in `kabibi`

An investigation of `kabibi`'s current architecture reveals how the TUI and terminal modes interact, and how to cleanly separate **Full-TUI ANSI mode** from **Piped / Non-TTY mode**.

---

### 1. Current State & Problem

Currently in [`kabibi/main.go`](file:///c:/Users/mihai/rustica/kabibi/main.go#L99-L165):
1. **Interactive path (`args` without `-c`, `-p`, `--edit`, or script file)**:
   - Always invokes `app.Boot()` and `host.Run()`.
   - `host.Run()` invokes `term.MakeRaw(os.Stdin.Fd())`.
   - When run in a pipe (e.g., `echo "cargo check" | kabibi` or a CI pipe/headless runner), `term.MakeRaw` fails (`ENOTTY`), causing `kabibi` to exit immediately with an error.
2. **Visual layout**:
   - `app.Boot()` mounts `AppWidget` (dual-pane blue Norton/Midnight Commander HUD, F-key hints, and full-screen cell buffers).
   - In a redirected pipe, sending ANSI escapes for clear screen, alternate buffer, or dual-pane blue rendering corrupts downstream tools and logs.

---

### 2. Architectural Comparison

| Dimension | Full-TUI ANSI Mode (`isTTY == true`) | Piped / Non-TTY Mode (`isTTY == false`) |
| :--- | :--- | :--- |
| **Detection** | `term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())` | `!term.IsTerminal(os.Stdin.Fd())` (or redirected stdout) |
| **Terminal State** | Raw mode (`term.MakeRaw`), mouse tracking, cursor positioning | Standard cooked streams (`io.Reader` / `io.Writer`) |
| **UI Components** | `DualPaneWidget` (blue panels), status lines, animated spinners | **None**; clean stream I/O directly to `Stdout` / `Stderr` |
| **Command Loop** | `ui.Host` event loop (`input.Reader` key events -> `AppWidget`) | `bufio.Scanner` / reader loop processing lines until `io.EOF` |
| **Bash Execution** | Persistent `ExecutionSession` (retains state, `$PWD`, env vars) | Persistent `ExecutionSession` (same session across lines) |
| **AI Fallback** | `LazyAIRunner` with TUI block rendering & spinners | `LazyAIRunner` streaming plain text to `Stdout` / `Stderr` |

---

### 3. Proposed Design for Piped Mode

Instead of initializing `app.Boot()` and `ui.Host`, when `!term.IsTerminal(os.Stdin.Fd())`, `runCLI` routes directly to a headless piped session:

1. **TTY Detection in `runCLI`**:
   ```go
   isStdinTTY := term.IsTerminal(os.Stdin.Fd())
   if !isStdinTTY {
       return runPipedSession(os.Stdin, stdout, stderr, opts.verbose)
   }
   ```
2. **Persistent `runPipedSession`**:
   - Creates a single `shell.NewExecutionSession("")` so environment variables (`export FOO=bar`), `$PWD` (`cd ...`), and functions persist across piped lines.
   - Creates `shell.NewLazyAIRunner(es)`.
   - Reads lines with `bufio.Scanner` (or syntax-aware parsing) from `stdin`:
     - **Empty/comment lines**: ignored.
     - **`RouteBuiltin` (`exit` / `quit`)**: breaks the loop and terminates cleanly.
     - **`RouteShell`**: executes via `es.RunCommand(...)`, streaming stdout/stderr directly without ANSI framing.
     - **`RouteAI` or `ErrUnknownCommandAtStart`**: transparently routes to `runOneShotAI(...)`, streaming plain text to stdout.
   - If an error occurs and is fatal (or standard non-zero exit in scripts), returns the appropriate exit code.

3. **Advantages**:
   - Shell scripts and piped input (`cat script.sh | kabibi` or `echo "git status" | kabibi`) work naturally.
   - AI queries also work over pipes (`echo "explain this error" | kabibi`).
   - Zero ANSI pollution, no blue panels, no spinner frames emitted into piped data.
   - Completely preserves the existing rich interactive TUI when running in an actual terminal.

