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

### Manual acceptance runbook

These are behavioral acceptance tests, not screenshot tasks. A test passes only
when its stated result is observed and checked. Screenshots document that result;
they do not replace checking command output, filesystem state, session state, or
tool results.

#### Test setup and evidence rules

1. Build and stage the release candidate so the test uses the same WASM and
   washmhost assets that will be packaged. Do not rely on the unit-test mapping
   that substitutes source files for missing staged assets.
2. Start the VS Code Web test host and open its served URL in the integrated
   browser frame. Use that same VS Code instance for the manual run; a second
   Chromium instance is not required.
3. Use a disposable workspace containing:
   - `README.md` with a known, unique line such as `KABIBI_WORKSPACE_FIXTURE`.
   - `alpha/seed.txt` and `beta/seed.txt`, each with distinct contents.
   - For the multi-root test, two folders with distinct names and seed files.
4. Record the extension build/commit, VS Code version, browser, host type (Web
   or Desktop), workspace layout, and test date. For Copilot tests, record
   whether the test used a signed-in Copilot account.
5. After each test, record the exact input, observed output or external state,
   pass/fail, and any error. Capture screenshots only after the outcome is
   visible—for example, after a file exists and its contents are verified, not
   merely after issuing the write command.

#### Web-host terminal

**WEB-01 — Open Kabibi in the VS Code terminal**

1. Open a Kabibi terminal using the contributed profile or its open command.
2. Wait for Kabibi's shell prompt, then run `printf 'KABIBI_TERMINAL_OK\n'`.
3. Confirm the command output appears in the VS Code integrated terminal and
   that typing another command works.
4. Confirm there is no Kabibi browser terminal page, webview, xterm.js UI, or
   WebGL2 renderer layered over or replacing the VS Code terminal.

**Pass:** the VS Code terminal owns the visible surface and renders the shell
output; the shell remains interactive.

**Screenshot:** after `KABIBI_TERMINAL_OK` is visible, capture the VS Code
window with the terminal tab, prompt, and result. This documents the actual
rendering surface, not just that the terminal was opened.

**WEB-02 — Workspace reads and writes**

1. Run `cat /workspace/README.md` and confirm the unique fixture line is read.
2. Run `printf 'KABIBI_WRITE_OK\n' > /workspace/kabibi-vscode-write.txt`.
3. Refresh or inspect the VS Code Explorer and open the new file in the editor.
4. Confirm the editor contains exactly `KABIBI_WRITE_OK` followed by a newline.
5. Re-read the file from Kabibi and confirm the same contents.

**Pass:** both directions agree: Kabibi reads the VS Code workspace, and a
Kabibi write becomes visible through VS Code's workspace APIs.

**Screenshot:** capture after the new file is visible in the Explorer and its
verified contents are open in the editor, with the successful shell command
visible if practical.

**WEB-03 — Multi-root workspace mapping**

1. Open a workspace with roots named `alpha` and `beta`.
2. Run `cat /workspace/alpha/seed.txt` and
   `cat /workspace/beta/seed.txt`.
3. Create one file under each root and verify each appears in the corresponding
   VS Code folder.
4. Try a nonexistent root name and confirm the operation fails rather than
   silently reading or writing another root.

**Pass:** each named root maps to its own VS Code workspace folder, and invalid
root names fail clearly.

**Screenshot:** capture the two folders and their distinct created files after
the writes have been verified.

**WEB-04 — Shell operation and terminal resizing**

1. Run ordinary shell operations that exercise the interpreter, such as
   `pwd`, variable assignment/export, a pipeline, and file redirection.
2. Resize the VS Code terminal narrower and taller, then run another command.
3. Confirm input still reaches Kabibi, output is still legible, and terminal
   state has not reset during resize.

**Pass:** the shell remains usable and preserves its cwd/environment across
terminal resizes.

**Screenshot:** capture a completed command after resize at the new dimensions;
do not use a screenshot of the resize gesture alone.

**WEB-05 — Kabibi's AI is disabled without disabling the shell**

1. Enter a known invalid one-line shell construct. Confirm Kabibi reports a
   parser error and does not hand the text to an AI.
2. Run a uniquely named nonexistent command. Confirm a normal shell
   command-not-found error, with no AI prompt, generated answer, or provider
   setup.
3. Run `ai status`. Confirm it does not enter Kabibi's AI builtin or onboarding
   flow.
4. Enter `/model`. Confirm no model picker or AI slash-command action opens.
5. Run a valid shell command afterward and confirm the session is still usable.

**Pass:** all four AI entry paths remain inactive, while normal shell execution
continues. The expected failure text may vary by host; the decisive check is
that no Kabibi AI flow runs.

**Screenshot:** capture the shell's resulting error/output after the invalid
command and `ai status`, with no AI dialog or generated response. Pair this
with the test log stating that no AI flow was entered.

**WEB-06 — Ctrl+O file manager remains available**

1. Press Ctrl+O at the shell prompt.
2. Browse to the fixture files and select or preview one.
3. Exit the panel and run another shell command.

**Pass:** the blue-panel file manager opens, shows the real mounted workspace,
and returns control to the shell without losing the session.

**Screenshot:** capture the populated file-manager panel showing the known
fixture names, then capture or record the successful command after returning
to the shell.

**WEB-07 — Visible terminal sessions are isolated**

1. Open two Kabibi terminal instances and arrange them so both outputs are
   visible.
2. In terminal A, run `cd /workspace/alpha` and
   `export KABIBI_TERMINAL_OWNER=alpha`.
3. In terminal B, run `pwd` and `echo "$KABIBI_TERMINAL_OWNER"`.
4. In terminal B, set a different cwd and value. Recheck terminal A.

**Pass:** A retains only A's cwd and environment; B retains only B's. Both can
still access the shared workspace files.

**Screenshot:** capture both terminals after printing their respective cwd and
owner values. The screenshot should show the observed independent state, not
just two open terminal tabs.

#### Copilot tools and per-session behavior

These tests require Copilot Chat in agent mode with the extension's tools
available. Use a disposable workspace and harmless commands. If Copilot is not
available in the Web test host, record that limitation and perform this section
in a VS Code host where the tools are available; do not treat tool unit tests as
a substitute for this acceptance run.

**COP-01 — Tool exposure**

1. Open the Copilot tool picker or tool list.
2. Confirm the extension exposes `exec`, `term_read`, `term_input`, and
   `term_kill` as Kabibi tools.
3. Confirm these are the extension's custom tools and do not attach Copilot to
   a visible Kabibi terminal.

**Pass:** all four tools are discoverable and identifiable as Kabibi extension
tools.

**Screenshot:** capture the actual Copilot tool list with all four tools
visible.

**COP-02 — VS Code approval and rejection**

1. Ask Copilot to use `exec` to create a uniquely named marker file.
2. When VS Code presents its normal tool-approval UI, verify it shows the
   command and explanation.
3. Reject the invocation.
4. From a Kabibi terminal, verify the marker does not exist.
5. Repeat with a different marker and approve it.
6. Verify the approved file exists and contains the exact requested contents.

**Pass:** rejection prevents the side effect; approval performs it; the tool
result reports the command outcome. Verify filesystem state independently of
the chat response.

**Screenshots:** capture the approval UI with the proposed command; after
rejection, capture the resulting chat state and shell check showing the marker
is absent; after approval, capture the created file and verified contents.

**COP-03 — Copilot shell state persists only within its owner**

1. In Copilot session A, invoke `exec` with
   `cd /workspace/alpha && export KABIBI_COPILOT_OWNER=alpha`.
2. In a later tool call from A, run `pwd` and print the variable.
3. In a separate Copilot session B, run the same checks.
4. Run the checks in a visible Kabibi terminal too.

**Pass:** later calls in A see A's cwd and variable; B and the visible terminal
do not. The sessions may see the same workspace files but must not share shell
state.

**Screenshot:** capture the verified outputs for A, B, and the visible terminal
with their distinct session labels or chat contexts apparent.

**COP-04 — Ejection preserves foreground state and isolates the job**

Use a unique release-file name for each run. In Copilot session A, call
`exec` with `timeout_sec: 1` and a command equivalent to:

```sh
cd /workspace/alpha
export KABIBI_EJECT_VISIBLE=before
while ! test -f /tmp/kabibi-eject-release-UNIQUE; do :; done
export KABIBI_EJECT_BACKGROUND=after
cd /tmp
printf 'KABIBI_BACKGROUND_FINISHED\n'
```

1. Confirm `exec` returns with `still_running: true`, its job ID, current
   output, and a footer directing Copilot to the corresponding `term_*` tools.
2. Before releasing the job, call `exec` again in A. Confirm it runs promptly
   and sees `/workspace/alpha` and `KABIBI_EJECT_VISIBLE=before`.
3. Have that foreground `exec` write the unique release file.
4. Use `term_read` with the returned ID until the job reports exit and
   `KABIBI_BACKGROUND_FINISHED`.
5. In another `exec` in A, confirm cwd remains `/workspace/alpha`,
   `KABIBI_EJECT_VISIBLE` is still `before`, and
   `KABIBI_EJECT_BACKGROUND` is absent.
6. In B and in a visible terminal, confirm neither the job ID nor its shell
   state was imported.

**Pass:** timeout ejects instead of cancelling; A's next `exec` works while the
job runs; the job is controllable only by its returned ID; state after ejection
does not leak from the job or into another shell.

**Screenshots:** capture the `exec` result with the returned ID; then capture
the subsequent foreground command and the final `term_read` output/status. A
screenshot of only the timeout notice is insufficient.

**COP-05 — Kill an ejected job**

1. In A, start a bounded-to-the-session but otherwise nonterminating shell loop
   with `timeout_sec: 1`.
2. Record the returned ID and invoke `term_kill` for that ID.
3. Use `term_read` to verify the job is exited.
4. Run a new `exec` in A and confirm the foreground shell is usable.
5. Confirm another Copilot session and visible terminal remain unaffected.

**Pass:** the addressed job stops; the tool reports its final status; the
foreground shell continues; no unrelated job is killed.

**Screenshot:** capture the kill result and the subsequent read showing the
same job ID as exited, followed by a successful new `exec`.

**COP-06 — Interactive background input on Desktop**

This case uses the Desktop Node washmhost and a known interactive process
fixture that echoes received input.

1. Start the fixture through `exec` with a short timeout so it ejects.
2. Send a unique line with `term_input` using the returned ID.
3. Read the output using `term_read`.

**Pass:** only the addressed process receives the input and emits the unique
echo. On the Web host, where arbitrary OS process spawning is unavailable,
`term_input` must report an honest unsupported/no-interactive-PTY result for a
job without an input-capable PTY; it must not claim success.

**Screenshot:** on Desktop, capture the `term_input` result and the matching
echo in the subsequent `term_read` result.

**COP-07 — VS Code cancellation is not timeout ejection**

1. Start a foreground command that remains active, with a timeout long enough
   not to eject during the test.
2. Cancel it through VS Code's normal tool cancellation UI.
3. Confirm the invocation reports cancellation rather than a successful
   background handle.
4. Run another `exec` in the same Copilot session and confirm it is usable.
5. Confirm no command continues silently in the background.

**Pass:** user cancellation stops the foreground operation; it is not reported
as timeout ejection, and no unrelated session is affected.

**Screenshot:** capture the cancelled invocation's final state and the
successful follow-up tool call. Verify process/session state independently.

#### Desktop host

**DESK-01 — Node washmhost process execution**

1. Open the extension in Desktop VS Code and start a Kabibi terminal.
2. Run a controlled external-process fixture supplied by the test setup. It
   must print unique stdout and stderr markers and exit with a known nonzero
   code.
3. Verify both streams, the exit code, and cwd/environment propagation.
4. Run shell-interpreter syntax and confirm it continues to be handled by
   Kabibi rather than being incorrectly passed through as one OS command.

**Pass:** this demonstrates the Desktop extension selected Node washmhost and
retained Kabibi's shell-first execution behavior.

**Screenshot:** capture the completed external command with both output markers
and its reported exit status visible.

**DESK-02 — Re-run terminal and Copilot isolation**

Repeat WEB-07 and COP-03 on Desktop. This ensures selecting the Node host does
not accidentally introduce shared process state between terminals and
Copilot-owned shells.

#### Close-out and evidence

1. Close a visible terminal that owns an active bounded job. Confirm its host
   and job are disposed and a newly opened terminal starts with a fresh cwd and
   environment while workspace files remain.
2. End a Copilot session with an active job. Confirm its tool session is
   retired and a new Copilot session starts with independent shell state.
3. Repeat any failed case from a fresh host and record whether it is
   reproducible.
4. Save screenshots under the matching test IDs above. Each image must show the
   completed observable behavior; attach a short evidence note listing the
   checked command output or external state. Never count an image of a click,
   prompt, or open panel alone as a passing test.
