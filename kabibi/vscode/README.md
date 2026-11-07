# Kabibi Shell for VS Code

Kabibi provides an interactive shell in VS Code's integrated terminal and
separate terminal tools for Copilot Agent mode. The extension does not attach
Copilot to a visible Kabibi, CMD, or Bash terminal.

## Features

- Open **🐚︎ Kabibi** from the terminal profile picker or with
  **🐚︎ Kabibi: Open Shell**. Running terminal tabs use the short label
  **🐚︎ Kb**.
- Use `exec` to run a command in a fresh shell execution session.
- Use `term_read`, `term_input`, and `term_kill` only with a background job ID
  returned by `exec`.
- Read and write the browser workspace at `/workspace`; Desktop and remote
  Node hosts use the workspace's actual filesystem paths.

Normally completed `exec` calls discard their shell session. A command that
outlives its foreground timeout is ejected into a managed background job; its
ID remains valid until it exits, is terminated, or the extension's idle lease
expires.

Kabibi's own AI assistant and AI fallback are disabled in extension-launched
shells. This does not disable Copilot's separate Kabibi tools.

## Requirements

- Visual Studio Code 1.99 or newer.
- Copilot Chat in Agent mode to use the contributed language model tools.
- Workspace Trust for shell execution. Kabibi does not run in untrusted
  workspaces because shell commands can modify files and start processes.

Browser-only VS Code hosts use Kabibi's browser washmhost and virtual
filesystem. Desktop and remote Node extension hosts instantiate Node washmhost
directly in the extension-host process and use real workspace paths. The
Rusticated `process_spawn` ABI remains responsible for guest-requested command
processes. The available native PTY addon depends on host platform and
architecture.

## Development and packaging

Run the extension's Node tests with `npm test`. Runtime assets, native PTY
addons, and the 128px Marketplace PNG generated from the repository's
`kabibi.svg` are staged by the repository release builder before `vsce package`
is run. Manual acceptance fixtures and screenshots are excluded from the VSIX.

From the repository root, rebuild the vegetable and launch a separate VS Code
instance with the source extension:

```powershell
$repo = (Get-Location).Path
go run . kabibi -o release\dist\kabibi.bat
$userData = Join-Path $repo 'target\vscode-ext\user'
$extensions = Join-Path $repo 'target\vscode-ext\extensions'
$extension = Join-Path $repo 'kabibi\vscode'
code "--user-data-dir=$userData" "--extensions-dir=$extensions" "--extensionDevelopmentPath=$extension" "$repo"
```

The extension detects the repository's `../../release/dist/kabibi.bat` at
startup and uses it instead of the staged vegetable. Pass an absolute
`--extensionDevelopmentPath`; the isolated extensions directory is intentionally
not where the source extension is installed. Installed extensions continue to
use their packaged `kabibi.bat`.

See the repository's [VS Code implementation and acceptance plan](../docs/7-vscode.md)
for the host matrix and exhaustive manual validation procedure.
