# Change Log

## 0.8.30

- Run Desktop and remote Node washmhost instances directly in the VS Code
  extension host, with per-instance stdio, terminal, cwd, and exit handling.
- Remove Node-host child-process startup and the private TTY/workspace/addon
  environment-variable protocols; Desktop uses real filesystem paths.
- Keep the browser workspace filesystem adapter in `extension.js` and use the
  freshly built checkout vegetable for source-development launches.

## 0.8.29

- Fix Desktop terminal startup by omitting the redundant guest executable name
  from Node-host WASM arguments.

## 0.1.0

- Add a VS Code-owned `🐚︎ Kabibi` terminal profile with `🐚︎ Kb` terminal tabs.
- Add independent Copilot terminal tools with one-shot execution and managed
  background-job routing.
- Disable Kabibi's own AI behavior in extension-launched shells.
