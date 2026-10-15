# Kabibi Zero-Plume / Immediate Line Ejection Model Implementation Plan

Here is the detailed implementation plan to transition Kabibi's shell and AI integration from the heavy multi-turn viewport buffer (`Shell` with `blocks []CommandBlock` and full-screen row ejection) to the clean **Zero-Plume / Immediate Line Ejection Model**.

---

## Phase 1: Streamlining the `Shell` Widget State (`shell_widget.go`)

1. **Strip Multi-Turn Block History:**
* Remove `blocks []CommandBlock` and `previouslyEjected` watermarks from the `Shell` struct. A shell does not need to store completed blocks in its rendering state anymore because completed turns live in native terminal scrollback.
* Reduce the shell's active state to: prompt string, input buffer (`inputValue`), cursor position (`cursorPos`), input history slice (`inputHistory`), running status (`running`), and active context cancellation (`activeCancel`).


2. **Fixed 1-Row Widget Height (`Measure` & `Render`):**
* Change `Measure` to always return height `1` (or dynamic height if multiline input is supported, but strictly bounded to the active prompt).
* Simplify `Render` to paint *only* the current prompt and input buffer into the `CellBuf`, returning a 1-line cell buffer and cursor position at the bottom of the terminal.



---

## Phase 2: Immediate Line Ejection & Plume Streaming (`submitAndExecuteAsync` & `LazyAIRunner`)

1. **Instant Command/Prompt Ejection on Enter:**
* When the user presses `Enter` on a non-empty command $L$, immediately format and emit the prompt line (e.g., `"$ " + rawInput + "\r\n"`) directly to the host scrollback via `s.opts.WriteToScrollback`.


2. **Zero-Plume Stream Writers for Shell & AI:**
* **For Shell Commands (`RunCommand`):** Pipe stdout/stderr through `SubprocessStreamWriter` (or a line-tokenizer callback) that calls `WriteToScrollback(line + "\r\n")` as each line completes.
* **For AI Turns (`LazyAIRunner` / `ai.Session`):**
* Bind `OnFeedback` to stream impermanent status updates (`[goal] …`, `[steps] …`, `[work] …`, and tool status) using `\r` overwrites if desired, or emit them cleanly.
* Bind `OnContent` to stream final answer tokens directly to `WriteToScrollback` (or accumulate and emit line-by-line).




3. **Turn Completion Cleanup:**
* The moment the shell command or AI turn finishes, the active execution flag (`s.running`) resets to `false`, the active cancel handle is cleared, and the input widget returns instantly to a clean 1-line `$ ` prompt.



---

## Phase 3: Preserving Native TTY Interactivity & `Ctrl+C` Safety

1. **`Ctrl+C` Interruption (`handleKeyEvent`):**
* When `running` is true, pressing `Ctrl+C` invokes `activeCancel()`, aborting the running `mvdan` command context or `ai.Session.RunTurn` immediately without terminating Kabibi.


* When idle, `Ctrl+C` clears the current input line.


2. **Clean Startup Sequence (`boot.go` / `app.go`):**
* Keep the startup newline injection (`StartupSequence`) so the initial prompt opens cleanly at the bottom of the screen without clearing the user's historical terminal session.





---

## Phase 4: Integrating the Kabibi-Tool Suite (`ai_tools.go`)

1. **Zero-Touch Tool Binding:**
* `BuildKabibiTools` already binds `exec`, `read_file`, `write_file`, `list_dir`, `shell_history`, and `shell_state` directly to `ExecutionSession`'s live `cwd`, `env`, and `CommandRecord` log.


* Because `ai.Session` automatically compiles tool declarations and injects the `"explanation"` schema property, no manual schema plumbing is required.




2. **Context Enrichment (`FormatPromptWithShellContext`):**
* Keep `FormatPromptWithShellContext` active so that any shell commands run between AI turns are summarized in the prompt sent to `ai.Session`, allowing Stage 1 triage to know when tool inspection (`shell_history`) is necessary.

# Retaining the Plume for File Manager (later feature)

> What if later we want to implement the plume, when we get back to the blue file panels? Should we make our plume toggle in/out in code rather than completely remove?

**Yes, absolutely.** Keeping the multi-turn rendering and ejection capabilities behind a code-level toggle (or mode configuration) rather than deleting them outright is the right architectural choice—especially since Kabibi's styling and layout files (`styles.go`) already reference full-screen file panels and split views.

A clean design allows you to enjoy the instant zero-plume shell experience today while keeping the infrastructure intact for when you bring back the rich file panels and interactive history views.

---

### How to Structure the Toggle Cleanly

Instead of deleting `blocks []CommandBlock` and the rendering logic, you can abstract them behind a configuration option on `ShellOptions`:

```go
type ShellMode int

const (
    ModeZeroPlume ShellMode = iota // Immediate line ejection, 1-line active prompt
    ModePlumeHistory               // Multi-block scrollable history / file panel integration
)

type ShellOptions struct {
    WriteToScrollback func(text string)
    OnExit            func()
    WorkingDir        string
    AIRunner          AIRunner
    Mode              ShellMode // <-- Toggle between zero-plume and full block history
}

```

#### Inside the `Shell` Widget:

* **When `Mode == ModeZeroPlume`:**
* Completed output lines bypass the local `blocks` buffer and are streamed instantly via `WriteToScrollback`.
* `Render()` restricts its viewport calculation strictly to the active 1-line input prompt (and transient `ai.Session` status line).


* **When `Mode == ModePlumeHistory`:**
* The shell falls back to accumulating `CommandBlock` history, managing the ejection watermark, and painting the full scrollable plume view.



### Why This Approach Wins

1. **Zero Dead Code Removal:** You don't throw away the carefully written `paintBlock`, `calculateVisualRows`, and ejection watermark code.
2. **Easy Testing:** You can toggle between modes for different testing scenarios or user preferences with a single flag.
3. **Future-Proof for File Panels:** When you return to the blue file panels and dual-pane views, you can switch the shell into `ModePlumeHistory` or embed it inside a split-screen container without having to rewrite the widget from scratch.