# Kabibi Shell Extension for VS Code

This document defines the intended first release of Kabibi for VS Code. It is
the product and implementation contract for the extension, not a record of the
design discussion.

## Product boundary

Kabibi is unusual in combining an interactive shell and an AI harness in one
user interaction. The VS Code product intentionally separates those concerns:

- The **visible Kabibi terminal** is a shell, not a Kabibi AI chat.
- **Copilot integration** is provided by extension-owned language model tools.
  Those tools use a shell owned by the invoking Copilot session.
- A visible terminal and a Copilot session never share a shell process, working
  directory, environment, runner, or background-job table. Different visible
  terminals and different Copilot sessions are isolated from one another too.

The extension must not attach Copilot to a particular visible terminal or
redirect terminal keystrokes to it.

## User-visible terminal

The extension contributes a Kabibi terminal profile and may provide a command
that opens that profile. The profile uses VS Code's terminal-provider API and a
`vscode.Pseudoterminal` backed by Kabibi and washmhost.

VS Code owns the terminal UI and rendering. The extension forwards terminal
input to Kabibi and emits Kabibi's output through the pseudoterminal events.
VS Code's integrated terminal renders that output.

**Do not bundle or launch Kabibi's browser terminal UI for this feature.** In
particular, do not bring forward Kabibi's xterm.js UI or its WebGL2 renderer.
There is no Kabibi webview terminal and no second terminal renderer: VS Code
provides the terminal surface.

Each visible terminal profile instance starts an independent Kabibi shell
session. It retains the normal Kabibi shell behavior and non-AI panels, including
the Ctrl+O blue-panel file manager.

## Kabibi AI is disabled in extension-launched shells

For the first VS Code release, every Kabibi shell launched by the extension
starts in an explicit AI-disabled mode. The extension passes the mode at startup;
it does not change the default behavior of Kabibi when launched outside VS Code.

In this mode:

- Shell parsing errors and unknown commands are reported as shell errors. They
  are not redirected to an AI fallback.
- `ai ...` commands do not enter Kabibi's AI command middleware, onboarding, or
  tool harness. They behave as unavailable shell commands.
- AI slash commands such as `/model` are not dispatched.
- Kabibi does not start its own AI session, configure providers, request
  credentials, or initialize AI-only services.
- Shell commands and the Ctrl+O file manager remain available.

This restriction applies to the shell processes used by both the visible
terminal and Copilot tool sessions. It does **not** disable the extension's
separate Copilot-facing tools.

## Copilot-facing tools

The extension contributes its own VS Code Language Model Tools. These are not
VS Code's built-in `run_in_terminal` tool and do not control a visible terminal.
The first release exposes Kabibi's terminal-tool contract:

- `exec`
- `term_read`
- `term_input`
- `term_kill`

Tool input and output should remain compatible with Kabibi's existing tool
definitions in [`ui/shell/tools_exec.go`](../ui/shell/tools_exec.go). Preserve
the structured status fields and textual result/footer. In particular, an
`exec` result must communicate its exit status, cwd, and background job ID when
applicable; subsequent terminal tools must target that ID.

### Copilot session ownership

Each Copilot session has its own persistent Kabibi shell. Its `exec` calls share
that session's cwd, environment, functions, and shell state. No state is shared
with a visible terminal or another Copilot session.

The shell and its managed jobs live for that Copilot session, not as one
extension-global shell. The extension's tool-call router must preserve that
ownership for every `exec` and `term_*` invocation.

### Background ejection

Ejection applies within one Copilot-owned shell session. It never transfers a
job between Copilot sessions or into a visible terminal.

When an `exec` exceeds its foreground timeout:

1. Kabibi ejects the still-running command into a managed background job.
2. The command keeps running with its original runner and state.
3. The foreground shell is available for the next `exec`, seeded with the
   shell-visible state at the instant of ejection.
4. Changes made later by the ejected job do not flow back into the foreground
   shell.
5. The tool result reports the job ID and directs Copilot to `term_read`,
   `term_input`, or `term_kill` for that job. A later `exec` never reuses it.

The implementation must use Kabibi's existing eject behavior rather than
reimplementing state copying or job ownership in JavaScript. Relevant behavior
and coverage are in
[`ui/shell/exec_session.go`](../ui/shell/exec_session.go) and
[`ui/shell/exec_session_eject_test.go`](../ui/shell/exec_session_eject_test.go).

### VS Code confirmation, cancellation, and errors

The extension uses VS Code's Language Model Tool API and its normal tool
acceptance flow. `prepareInvocation` supplies useful confirmation context,
including the command and explanation for `exec`, or the addressed job and
action for a `term_*` call. VS Code remains responsible for presenting and
handling approval and cancellation; the extension must not add a competing
Kabibi-specific confirmation prompt.

The invocation cancellation token is passed to the active tool operation. If
the user cancels a foreground invocation, the active command is cancelled using
Kabibi's cancellation path. Timeout is different: it ejects the command and
returns a job handle rather than cancelling it.

Errors must be returned as errors to VS Code, not disguised as successful tool
results. A rejected or cancelled tool invocation must not leave a shell marked
busy or accidentally affect a different session.

## Runtime and filesystem

The extension has two host targets. They share the product behavior and
workspace mapping, but use the appropriate washmhost runtime for their host.

| VS Code host | Runtime | Required behavior |
| --- | --- | --- |
| Web extension host (`vscode.dev`, `github.dev`, and similar) | The existing browser washmhost infrastructure | Run the Kabibi WASM guest using the browser host and its asynchronous virtual filesystem. Do not introduce a browser terminal UI. |
| Desktop extension host | Node-based washmhost where available | Use the Node host so host process spawning and desktop compatibility are available. Keep Kabibi's shell interpreter as the primary shell execution path; use host processes for commands that require them. |

Runtime selection belongs in the extension bootstrap. Do not force the browser
host on Desktop when the Node host can be used, and do not assume Node APIs exist
in a browser extension host.

The active VS Code workspace is mounted into Kabibi at `/workspace` through
`vscode.workspace.fs`. A single workspace folder maps to `/workspace`; a
multi-root workspace maps folders below `/workspace/<folder-name>`. Kabibi's
other virtual filesystems remain available for paths outside the workspace.

Shell state isolation does not imply filesystem isolation: shell sessions can
read and modify the same mounted workspace files. Each shell's cwd and
environment remain independent.

The browser host does not provide arbitrary OS process spawning. Its available
commands are therefore determined by Kabibi's interpreter, built-ins, and the
browser host capabilities. Desktop uses the Node washmhost for process
compatibility. This is an intentional host distinction, not a reason to replace
the browser runtime with a webview or a remote shell.

## Extension implementation

The existing extension is raw JavaScript with no bundler. Preserve the shared
workspace mount and the VS Code terminal-provider approach while adding the
Copilot tool surface and runtime selection.

The extension is responsible for:

1. Starting a shell host for each visible terminal instance.
2. Starting and routing to the persistent shell belonging to each Copilot
   session.
3. Providing a private tool-server back-channel between the VS Code tools and
   the corresponding Kabibi tool session. The channel must not expose a
   machine-wide unauthenticated listener.
4. Passing the AI-disabled startup mode to every extension-launched Kabibi
   shell.
5. Mapping tool confirmation and cancellation to VS Code's tool lifecycle.
6. Disposing shell hosts, tool connections, and background jobs when their
   owning terminal or Copilot session ends.

The tool server should invoke the existing terminal tools against the
session-owned `ExecutionSession`; it should not create a new shell for each
`exec`, or make the visible terminal the Copilot shell.

## Packaging and Marketplace release

The extension package must contain every runtime asset it loads. The release
process currently stages the Kabibi vegetable and washmhost browser assets into
`kabibi/vscode`; update that process when adding or selecting the Node host
assets so a clean release build produces a complete package.

The Marketplace icon must be a PNG generated from the existing
[`kabibi.svg`](../kabibi.svg). Reference the generated PNG from the extension
manifest; do not point the Marketplace manifest at the SVG.

Before publishing, complete and validate:

- Extension-local README, LICENSE, and CHANGELOG.
- Manifest license, keywords, a Marketplace-supported category, gallery
  presentation metadata, supported VS Code engine version, and icon.
- Correct runtime and WASM assets for both web and desktop hosts.
- `.vscodeignore` exclusions that omit tests and development files without
  excluding required runtime assets.
- Reproducible staging through the release build.
- VSIX packaging and inspection of its included files.

Publishing credentials and the actual Marketplace publish operation are
separate from building and validating the release artifact.

## Verification and release acceptance

### Shell-mode behavior

- Extension-launched Kabibi does not redirect parse errors or unknown commands
  to AI.
- `ai ...` and AI slash commands do not activate Kabibi's built-in AI behavior.
- Normal shell commands and Ctrl+O file-manager panels still work.
- Normal non-extension Kabibi startup retains its existing AI behavior.

### Terminal and host behavior

- VS Code terminal profiles start separate shell sessions and render entirely
  through the VS Code terminal.
- Resizing and input are forwarded through the terminal-provider API.
- Workspace reads and writes work for single-root and multi-root workspaces.
- Web tests run through browser washmhost without bundling or invoking Kabibi's
  terminal renderer.
- Desktop tests verify Node washmhost process spawning and workspace access.

### Copilot tool behavior

- Every Copilot session has persistent shell state isolated from other
  Copilots and visible terminals.
- `exec` returns its output, footer, exit status, and cwd.
- A timed-out `exec` ejects, returns a job ID, and permits another `exec` in the
  same Copilot shell.
- The ejected job can be read, sent input where supported, and terminated only
  through its returned ID.
- Ejected-job state changes do not leak into the foreground shell.
- VS Code confirmation and cancellation work without duplicate prompts or
  success-shaped error results.

Run the extension's Node tests, the relevant Kabibi tests and documented
Rusticated/custom-sysroot builds, the web-host smoke tests, and `vsce package`.
Inspect the resulting VSIX rather than relying only on tests that map staged
asset reads back to source files.

## Testing and Validation

My proposal is to test the **boundaries where this feature could accidentally become the wrong product**, not just the individual functions. The highest-risk changes are: disabling Kabibi’s AI only in extension-launched shells, routing each Copilot tool call to the right persistent shell, choosing the correct washmhost for each host, and packaging the actual assets the extension runs.

### Likely change hotspots

1. **AI-disabled startup mode.** The setting will need to reach every shell entry path, then gate parse-error fallback, `ai …` handling, and slash-command dispatch without disturbing normal Kabibi startup. Likely touchpoints include `main.go`, `piped.go`, `shell_widget.go`, `ai_onboarding.go`, and `app_widget.go`. This is the area most at risk of scattered, incomplete gates.

2. **Copilot tool routing and session lifecycle.** The extension needs to route `exec` and each `term_*` call to the Copilot session that owns that shell. That routing must preserve the existing behavior in `tools_exec.go` and `exec_session.go`, not recreate it in JavaScript.

3. **Extension host bootstrap.** `extension.js` will need distinct browser and Desktop host paths, while preserving workspace mounting through `vfs.js`. The VS Code terminal provider remains the only terminal UI: its output goes to VS Code to render.

4. **Build and release staging.** `release/main.go`, the extension manifest, and packaged host assets must agree. A passing source-level test is not enough if the VSIX omits a runtime file.

### Testing proposal

I’d make the test pyramid build from the shell outward:

- **Go behavior tests:** Test AI-disabled and normal startup side by side. With AI disabled, malformed input and unknown commands must produce shell errors, `ai …` and `/model` must not invoke Kabibi’s AI harness, and Ctrl+O must still open the file manager. Existing shell AI and slash-command tests are good places to extend.

- **Shell ejection tests:** Reuse the already substantial cases in `exec_session_eject_test.go`. Add coverage at the tool contract boundary so an `exec` timeout returns the correct job ID and footer, a later `exec` succeeds in the same Copilot shell, and `term_read`/`term_input`/`term_kill` affect only the addressed job. Use gates and channels for timing-sensitive cases rather than relying on sleeps.

- **Extension adapter tests:** Test that the declared tool names and input schemas match the handlers, that confirmation messages include the intended command or job, that approval invokes the handler, and that rejection or cancellation does not execute or falsely succeed. Exercise two Copilot sessions and a visible terminal concurrently; each must retain independent cwd, environment, runner, and job IDs.

- **Real host acceptance tests:** Extend the existing `vfs.test.js` style, but run the **built extension assets**, not only source files substituted by the test harness. In the web host, verify shell behavior, workspace reads/writes, resize/input, and the expected lack of arbitrary process spawning. On Desktop, verify the Node host can spawn an external command while Kabibi’s interpreter still handles normal shell syntax. Include two terminal instances to prove state isolation while confirming that both can access the shared workspace.

- **VS Code/Copilot acceptance:** Run a real VS Code extension-host smoke test for the terminal profile and tool registration. Then do a release-gate Copilot check: VS Code presents its normal confirmation; rejecting prevents execution; approving runs the command; canceling interrupts a foreground command; timeout ejects rather than cancels; and the same Copilot session can continue and manage the returned job. A real Copilot interaction should be a release check, not a CI dependency on account credentials or a live model.

- **VSIX validation:** Build from a clean staging state, package it, and inspect the VSIX contents. Verify the PNG generated from the SVG and all selected runtime assets are present; verify that no Kabibi browser terminal renderer or WebGL UI has slipped into the package.

The key pass/fail criterion is end-to-end: **the tested extension artifact** must preserve those behaviors across the VS Code tool API, the session router, the Kabibi shell, and the selected host—not merely pass unit tests for each piece in isolation.

### Manual Testing using AI with browser page automation
