# Kabibi Shell Extension for VS Code

This document defines the intended first release of Kabibi for VS Code. It is
the product and implementation contract for the extension, not a record of the
design discussion.

## VS Code API target

Target stable VS Code 1.99 or newer. The Language Model Tools API is stable
earlier, but extension-contributed tools became available in Agent mode in
1.99. Keep `engines.vscode` and the VS Code API typings aligned with the
minimum release.

Use stable public APIs only. The stable tool API does not expose a chat-session
identifier or a chat-session-ended event. Do not depend on proposed/private
chat APIs, and do not treat `toolInvocationToken` as a shell-session key.
Copilot shell routing therefore uses the explicit tool-session handle described
below; it is not inferred from VS Code chat identity.

## Product boundary

Kabibi is unusual in combining an interactive shell and an AI harness in one
user interaction. The VS Code product intentionally separates those concerns:

- The **visible Kabibi terminal** is a shell, not a Kabibi AI chat.
- **Copilot integration** is provided by extension-owned language model tools.
  Those tools use an explicit, extension-owned tool shell.
- A visible terminal and a tool shell never share a shell process, working
  directory, environment, runner, or background-job table. Different visible
  terminals and different tool shells are separate. Tool-shell routing uses
  the explicit handle below; VS Code does not enforce chat-to-handle binding.

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
applicable. The extension adds a `shell_id` routing field around the Kabibi
tool contract: the first `exec` may omit it and creates a tool shell; its result
returns the new handle. Later `exec` calls and every `term_*` call must carry
that handle. `term_*` calls also carry the existing shell-local job ID.

### Tool-shell ownership and lifetime

The first `exec` without a `shell_id` creates a logical tool shell and returns
an opaque `shell_id` in both structured and textual result content. Subsequent
calls must pass that handle so they continue the same cwd, environment,
functions, shell state, and managed-job table. A missing, unknown, or expired
handle on `term_*` is an explicit tool error; it must never fall back to an
extension-global shell or a visible terminal. A later `exec` without a handle
starts a new isolated tool shell and reports that it did so.

VS Code does not provide a stable chat identity to bind to the handle. The
Copilot conversation is expected to retain and pass its own handle from prior
tool results. This provides explicit routing, not an API-enforced identity
boundary: a caller that is given another shell's handle can address that
shell. Never claim that VS Code itself guarantees chat-to-shell binding.

There is no reliable chat-end callback. A tool shell may outlive the chat that
created it; treat it as abandoned rather than waiting for a session-disposal
event. Reclaim inactive tool shells through a bounded, extension-owned idle
lease, terminating their managed jobs on expiry. The lease duration is an
implementation constant covered by tests, not a user-facing setting. Extension
host deactivation terminates every remaining tool shell and job.

### Background ejection

Ejection applies within one tool shell identified by `shell_id`. It never
transfers a job between tool shells or into a visible terminal.

When an `exec` exceeds its foreground timeout:

1. Kabibi ejects the still-running command into a managed background job.
2. The command keeps running with its original runner and state.
3. The foreground shell is available for the next `exec`, seeded with the
   shell-visible state at the instant of ejection.
4. Changes made later by the ejected job do not flow back into the foreground
   shell.
5. The tool result reports the `shell_id` and job ID and directs Copilot to
   `term_read`, `term_input`, or `term_kill` with both IDs. A later `exec`
   never reuses the job ID.

The implementation must use Kabibi's existing eject behavior rather than
reimplementing state copying or job ownership in JavaScript. Relevant behavior
and coverage are in
[`ui/shell/exec_session.go`](../ui/shell/exec_session.go) and
[`ui/shell/exec_session_eject_test.go`](../ui/shell/exec_session_eject_test.go).

### VS Code confirmation, cancellation, and errors

The extension uses VS Code's Language Model Tool API and its normal tool
acceptance flow. `prepareInvocation` supplies useful confirmation context,
including the command and explanation for `exec`, or the addressed job and
action for a `term_*` call. It must be side-effect-free; shell creation and
command execution happen only in `invoke`. Returning confirmation messages
requests VS Code's normal confirmation UI, but remembered approvals or
auto-approval policies may bypass a fresh prompt. The extension must not add a
competing Kabibi-specific prompt or claim that confirmation is a sandbox.

The invocation cancellation token is passed to the active tool operation. If
the user cancels a foreground invocation, the active command is cancelled using
Kabibi's cancellation path. Cancelling a `term_read` must not kill its job.
Timeout is different: it ejects the command and returns a job handle rather than
cancelling it. Cancellation is per invocation, not a chat-session-end signal.

Errors must be returned as errors to VS Code, not disguised as successful tool
results. A rejected or cancelled tool invocation must not leave a shell marked
busy or accidentally affect a different session.

## Runtime and filesystem

The extension selects its host from the available VS Code extension host. The
same product behavior and workspace mapping apply, but the washmhost runtime
must match the selected host.

| VS Code host | Runtime | Required behavior |
| --- | --- | --- |
| Browser-only extension host (`vscode.dev`, `github.dev`, and similar) | The existing browser washmhost infrastructure | Run the Kabibi WASM guest using the browser host and its asynchronous virtual filesystem. Do not introduce a browser terminal UI or claim arbitrary OS process spawning. |
| Desktop local Node extension host | Node-based washmhost | Use the Node host for process spawning and desktop compatibility. Keep Kabibi's shell interpreter as the primary shell execution path; use host processes for commands that require them. |
| Remote workspace Node extension host (SSH, WSL, containers, Codespaces) | Node-based washmhost on the workspace host | Run shell commands where the workspace lives. Do not assume a browser UI implies a browser extension host when a remote Node host is available. |

Runtime selection belongs in the extension bootstrap. Do not force the browser
host on Desktop when the Node host can be used, and do not assume Node APIs exist
in a browser extension host. Prefer the workspace host when available so
external processes and workspace files are on the same machine; browser-only
VS Code uses the browser entry point. Test host placement explicitly for
desktop, browser-only, and remote-workspace configurations.

Set `extensionKind` to prefer the workspace host when it exists (for example,
`["workspace", "ui"]`). This lets a remote workspace use its remote Node host,
while browser-only VS Code still uses the browser entry point.

The active VS Code workspace is mounted into Kabibi at `/workspace` through
`vscode.workspace.fs`. A single workspace folder maps to `/workspace`; a
multi-root workspace maps folders below `/workspace/<folder-name>`. Kabibi's
other virtual filesystems remain available for paths outside the workspace.
Multi-root folder names can collide; the mount mapping must disambiguate them
deterministically rather than silently resolving the first match.

Shell state isolation does not imply filesystem isolation: shell sessions can
read and modify the same mounted workspace files. Each shell's cwd and
environment remain independent.

The browser host does not provide arbitrary OS process spawning. Its available
commands are therefore determined by Kabibi's interpreter, built-ins, and the
browser host capabilities. Desktop uses the Node washmhost for process
compatibility. This is an intentional host distinction, not a reason to replace
the browser runtime with a webview or a remote shell.

The extension executes shell commands and modifies workspace files. Declare
and test an explicit Workspace Trust policy; do not imply that tool approval or
the browser host sandboxes commands.

## Extension implementation

The existing extension is raw JavaScript with no bundler. Preserve the shared
workspace mount and the VS Code terminal-provider approach while adding the
Copilot tool surface and runtime selection.

The extension is responsible for:

1. Starting a shell host for each visible terminal instance.
2. Creating a tool shell on the first `exec` and routing later calls by its
   explicit `shell_id`.
3. Providing a private tool-server back-channel between the VS Code tools and
   the corresponding Kabibi tool session. The channel must not expose a
   machine-wide unauthenticated listener.
4. Passing the AI-disabled startup mode to every extension-launched Kabibi
   shell.
5. Mapping tool confirmation and cancellation to VS Code's tool lifecycle.
6. Disposing visible-terminal hosts when their terminals close; expiring
   abandoned tool shells and their jobs through the bounded idle lease; and
   disposing all remaining hosts on extension deactivation. Do not depend on a
   Copilot chat-ended event.

The tool server should invoke the existing terminal tools against the
`ExecutionSession` selected by `shell_id`; it should not create a new shell for
every `exec` that carries a valid handle, or make the visible terminal the
Copilot shell. The extension's routing envelope must not change Kabibi's
underlying `exec`/`term_*` behavior.

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
- Language Model Tool contributions and Agent-mode discoverability metadata
  (`toolReferenceName` and `canBeReferencedInPrompt`) for all four tools.
- Explicit Workspace Trust support metadata. The public Marketplace category
  list does not include `Terminal`; use a listed category unless Marketplace
  validation confirms otherwise.
- Correct runtime and WASM assets for both web and desktop hosts.
- `.vscodeignore` exclusions that omit tests and development files without
  excluding required runtime assets.
- Reproducible staging through the release build.
- VSIX packaging and inspection of its included files.
- A Node.js version compatible with the selected tooling. The current
  `@vscode/test-web` line requires Node 20 or newer; current `@vscode/vsce` 4.x
  requires Node 22 or newer.

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

- A first `exec` creates a unique tool shell handle; later calls carrying that
  handle preserve its state. A separate conversation's first call receives a
  different handle.
- Missing, unknown, and expired handles fail explicitly. No call falls back to
  another tool shell or a visible terminal.
- `exec` returns its output, footer, exit status, and cwd.
- A timed-out `exec` ejects, returns a job ID, and permits another `exec` in the
  same tool shell when called with the same `shell_id`.
- The ejected job can be read, sent input where supported, and terminated only
  through its returned `shell_id` and job ID.
- Ejected-job state changes do not leak into the foreground shell.
- VS Code confirmation and cancellation use the native tool lifecycle without
  duplicate prompts or success-shaped error results. Tests account for
  remembered or auto-approved tool permissions.
- Idle expiry terminates an abandoned tool shell and its jobs; extension
  deactivation terminates all remaining shells.

Run the extension's Node tests, the relevant Kabibi tests and documented
Rusticated/custom-sysroot builds, the web-host smoke tests, and `vsce package`.
Inspect the resulting VSIX rather than relying only on tests that map staged
asset reads back to source files. Test against VS Code 1.99 or newer, and
include a current stable release in the release gate.

## Testing and Validation

The tests target the **boundaries where this feature could accidentally become
the wrong product**, not just individual functions. The highest-risk changes
are: disabling Kabibi’s AI only in extension-launched shells, routing calls by
explicit tool-shell handle, choosing the correct washmhost for each host, and
packaging the actual assets the extension runs.

### Likely change hotspots

1. **AI-disabled startup mode.** The setting will need to reach every shell entry path, then gate parse-error fallback, `ai …` handling, and slash-command dispatch without disturbing normal Kabibi startup. Likely touchpoints include `main.go`, `piped.go`, `shell_widget.go`, `ai_onboarding.go`, and `app_widget.go`. This is the area most at risk of scattered, incomplete gates.

2. **Copilot tool routing and shell lifetime.** The extension needs to create a tool shell on first `exec`, return its opaque handle, and route each later `exec` and `term_*` call by that handle. This is explicit tool-session routing, not VS Code chat identity. The router must preserve behavior in `tools_exec.go` and `exec_session.go`, not recreate it in JavaScript.

3. **Extension host bootstrap.** `extension.js` will need distinct browser and Desktop host paths, while preserving workspace mounting through `vfs.js`. The VS Code terminal provider remains the only terminal UI: its output goes to VS Code to render.

4. **Build and release staging.** `release/main.go`, the extension manifest, and packaged host assets must agree. A passing source-level test is not enough if the VSIX omits a runtime file.

### Testing proposal

I’d make the test pyramid build from the shell outward:

- **Go behavior tests:** Test AI-disabled and normal startup side by side. With AI disabled, malformed input and unknown commands must produce shell errors, `ai …` and `/model` must not invoke Kabibi’s AI harness, and Ctrl+O must still open the file manager. Existing shell AI and slash-command tests are good places to extend.

- **Shell ejection tests:** Reuse the already substantial cases in `exec_session_eject_test.go`. Add coverage at the tool contract boundary so an `exec` timeout returns the correct job ID and footer, a later `exec` succeeds in the same Copilot shell, and `term_read`/`term_input`/`term_kill` affect only the addressed job. Use gates and channels for timing-sensitive cases rather than relying on sleeps.

- **Extension adapter tests:** Test that the declared tool names and input schemas match the handlers, that confirmation messages include the intended command or shell/job handle, that approval invokes the handler, and that rejection or cancellation does not execute or falsely succeed. Exercise two conversations and a visible terminal concurrently: each first `exec` must receive a distinct handle, and calls using each handle must retain independent cwd, environment, runner, and job IDs. Test missing, unknown, expired, and deliberately mismatched handles. The API does not provide a chat identity, so these tests validate handle routing, not an unforgeable binding between a VS Code conversation and a shell.

- **Real host acceptance tests:** Extend the existing `vfs.test.js` style, but run the **built extension assets**, not only source files substituted by the test harness. In the browser host, verify shell behavior, workspace reads/writes, resize/input, and the expected lack of arbitrary process spawning. On Desktop and remote Node hosts, verify the Node host can spawn an external command where the workspace lives while Kabibi’s interpreter still handles normal shell syntax. Include duplicate-named multi-root folders, two visible terminals, and both tool-shell handles. Verify PTY input with the packaged native addon on each supported OS/architecture.

- **VS Code/Copilot acceptance:** Run a real VS Code extension-host smoke test for the terminal profile and tool registration in Agent mode on VS Code 1.99 or newer. Then do a release-gate Copilot check: under default approval settings, confirmation can be accepted or rejected; cancellation interrupts a foreground command; timeout ejects rather than cancels; and a later call using the returned `shell_id` can continue and manage the returned job. Remembered approvals may suppress later prompts. A real Copilot interaction should be a release check, not a CI dependency on account credentials or a live model.

- **VSIX validation:** Build from a clean staging state, package it, and inspect the VSIX contents. Verify the PNG generated from the SVG and all selected runtime assets are present; verify that no Kabibi browser terminal renderer or WebGL UI has slipped into the package. Run Marketplace validation for categories and metadata before publishing.

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
2. Start `vscode-test-web` with `--browser none`, `--quality stable`, and a
   pinned VS Code commit. Open the served URL in the integrated browser frame;
   do not launch a second Chromium instance for this run. Record the exact
   command and URL.
3. Use a disposable workspace containing:
   - `README.md` with a known, unique line such as `KABIBI_WORKSPACE_FIXTURE`.
   - `alpha/seed.txt` and `beta/seed.txt`, each with distinct contents.
   - Multi-root folders with distinct names and seed files, plus a case with
     duplicate folder names that must be disambiguated.
4. Record the extension build/commit, VS Code version, browser, host type (Web
   or Desktop), workspace layout, and test date. For Copilot tests, record
   whether the test used a signed-in Copilot account and the tool-approval
   policy (default, remembered, or auto-approved).
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

#### Copilot tools and tool-shell behavior

These tests require Copilot Chat in agent mode with the extension's tools
available on VS Code 1.99 or newer. Use a disposable workspace and harmless
commands. If Copilot is not available in the Web test host, record the Web case
as blocked; a Desktop run does not establish Web-host tool discovery.

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

Run under default approval settings with no remembered approval for these
tools. A remembered or auto-approved policy may legitimately suppress the
confirmation UI; that is VS Code policy, not an extension-specific prompt.

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

**COP-03 — Explicit tool-shell handles preserve state**

1. In conversation A, invoke `exec` without `shell_id` and set
   `/workspace/alpha` and `KABIBI_COPILOT_OWNER=alpha`.
2. Record the returned `shell_id`; in a later call from A, pass it and run
   `pwd` and print the variable.
3. In a separate conversation B, invoke `exec` without a handle, set a
   different cwd and value, and record its returned handle.
4. Continue B with its handle, then recheck A with A's handle.
5. Run the checks in a visible Kabibi terminal too.

**Pass:** A and B receive different handles; later calls with each handle retain
only that shell's cwd and variable. Calls without a handle create a new shell
instead of silently reusing one. The visible terminal remains independent.
This validates explicit handle routing, not a VS Code-enforced binding between
a chat and its handle.

**Screenshot:** capture both returned handles and the verified outputs for A, B,
and the visible terminal with their distinct chat contexts apparent.

**COP-04 — Ejection preserves foreground state and isolates the job**

Use a unique release-file name for each run. In conversation A, call `exec`
with its `shell_id` and `timeout_sec: 1`. The fixture must wait using a
yield-capable async shell operation; do not use a pure busy-spin loop, which can
starve the browser host.

The fixture should set `KABIBI_EJECT_VISIBLE=before`, wait for a unique release
signal, then set `KABIBI_EJECT_BACKGROUND=after`, change cwd, and print
`KABIBI_BACKGROUND_FINISHED`.

1. Confirm `exec` returns with `still_running: true`, its job ID, current
   output, `shell_id`, and a footer directing Copilot to the corresponding
   `term_*` tools with both IDs.
2. Before releasing the job, call `exec` again with A's `shell_id`. Confirm it
   runs promptly and sees `/workspace/alpha` and
   `KABIBI_EJECT_VISIBLE=before`.
3. Have that foreground `exec` write the unique release file.
4. Use `term_read` with A's `shell_id` and the returned job ID until the job
   reports exit and `KABIBI_BACKGROUND_FINISHED`.
5. In another `exec` using A's handle, confirm cwd remains `/workspace/alpha`,
   `KABIBI_EJECT_VISIBLE` is still `before`, and
   `KABIBI_EJECT_BACKGROUND` is absent.
6. In B and in a visible terminal, confirm neither A's handle nor its job ID
   was returned or implicitly imported.

**Pass:** timeout ejects instead of cancelling; A's next `exec` works while the
job runs; the job is controllable only with A's `shell_id` and returned job ID;
state after ejection does not leak from the job or into another shell. Confirm
that the browser host remains responsive while the fixture waits.

**Screenshots:** capture the `exec` result with the returned ID; then capture
the subsequent foreground command and the final `term_read` output/status. A
screenshot of only the timeout notice is insufficient.

**COP-05 — Kill an ejected job**

1. In A, start a yielding, otherwise nonterminating fixture with `timeout_sec: 1`
   and its `shell_id`.
2. Record the returned job ID and invoke `term_kill` with both IDs.
3. Use `term_read` with both IDs to verify the job is exited.
4. Run a new `exec` with A's handle and confirm the foreground shell is usable.
5. Confirm another conversation using its own handle and the visible terminal
   remain unaffected.

**Pass:** the addressed job stops; the tool reports its final status; the
foreground shell continues; no unrelated job is killed.

**Screenshot:** capture the kill result and the subsequent read showing the
same job ID as exited, followed by a successful new `exec`.

**COP-06 — Interactive background input on Desktop**

This case uses the Desktop Node washmhost and a known interactive process
fixture that echoes received input.

1. Start the fixture through `exec` with a short timeout so it ejects; record
   the returned `shell_id` and job ID.
2. Send a unique line with `term_input` using both IDs.
3. Read the output using `term_read` with both IDs.

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
4. Run another `exec` with the same `shell_id` and confirm it is usable.
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

**DESK-02 — Re-run terminal and tool-shell isolation**

Repeat WEB-07 and COP-03 on Desktop. This ensures selecting the Node host does
not accidentally introduce shared process state between terminals and
independently addressed tool shells.

#### Close-out and evidence

1. Close a visible terminal while a long-running foreground command is active.
   Confirm its host and command are disposed and a newly opened terminal starts
   with a fresh cwd and environment while workspace files remain.
2. In a test build with an accelerated idle lease, leave a tool shell idle and
   verify that expiry invalidates its handle and terminates its jobs. Verify
   that a new conversation's first `exec` creates a different handle. The
   extension must not claim to observe the chat ending.
3. Deactivate the extension host with tool shells and jobs still present;
   confirm all are terminated.
4. Repeat any failed case from a fresh host and record whether it is
   reproducible.
5. Save screenshots under the matching test IDs above. Each image must show the
   completed observable behavior; attach a short evidence note listing the
   checked command output or external state. Never count an image of a click,
   prompt, or open panel alone as a passing test.
