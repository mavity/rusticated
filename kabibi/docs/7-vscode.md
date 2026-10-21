> I wonder how we could embed our Kabibi shell as a web VSCode extension. That means it should be available both on VSCode proper and vscode.dev and any other web-hosted browser VSCode and clone. Those insulate extensions in subdomains and such, and run them exclusively in browser only, no desktop. But they do give access to the virtual filesystem, so that would be lovely to integrate (I believe washmhost.js already provides 'mounting' feature which we can leverage to project virtual filesystem into our WASM guest).
>
> I've checked out external/inert.sh and external/vscode-previewer that contained some prior art that might be helpful. Please investigate.

I will inspect `external/inert.sh`, `external/vscode-previewer`, and the browser/JS host implementation (`washmhost.js`) to see how the prior art works and how Kabibi's WASM guest and virtual filesystem mounting can be integrated into a VS Code Web extension.

Find files matching `**/*`

Read , lines 1 to 250
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\inert.sh\\iframe.l.i.v.e.js",
  "view_range": [
    1,
    250
  ]
}

Find files matching `**/*`

Find files matching `*.{js,ts,html,go}`

I'll inspect the contents of `external/inert.sh`, `external/vscode-previewer`, and the browser/JS host implementation in `mohabbat` to see what prior art and VFS mounting mechanisms already exist.

Find files matching `**/*`

Find files matching `**/*.{js,ts,html,go}`

Read , lines 240 to 445
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\inert.sh\\iframe.l.i.v.e.js",
  "view_range": [
    240,
    445
  ]
}

Read 
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\vscode-previewer\\package.json"
}

Read , lines 1 to 250
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\vscode-previewer\\vscode.w.e.b.js",
  "view_range": [
    1,
    250
  ]
}

Read 
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\vscode-previewer\\README.md"
}

Read , lines 1 to 250
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    1,
    250
  ]
}

Read , lines 206 to 582
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\vscode-previewer\\vscode.w.e.b.js",
  "view_range": [
    206,
    582
  ]
}

Read , lines 188 to 650
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    188,
    650
  ]
}

Read , lines 651 to 1150
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    651,
    1150
  ]
}

Read , lines 1151 to 1629
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    1151,
    1629
  ]
}

Read , lines 374 to 582
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\vscode-previewer\\vscode.w.e.b.js",
  "view_range": [
    374,
    582
  ]
}

Read , lines 444 to 650
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    444,
    650
  ]
}

Read , lines 870 to 1150
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    870,
    1150
  ]
}

Read , lines 1329 to 1629
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    1329,
    1629
  ]
}

Read , lines 1070 to 1200
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    1070,
    1200
  ]
}

Read , lines 1550 to 1629
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.js",
  "view_range": [
    1550,
    1629
  ]
}

Find files matching `**/*`

Find files matching `**/*.{js,html}`

Read 
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\inert.sh\\index.html"
}

Read 
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\inert.sh\\iframe.l.i.v.e.html"
}

Read 
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\vscode-previewer\\index.html"
}

Read , lines 1 to 250
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\index.html",
  "view_range": [
    1,
    250
  ]
}

Read , lines 1 to 250
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\release\\dist\\index.html",
  "view_range": [
    1,
    250
  ]
}

Read , lines 1 to 250
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\inert.sh\\test.js",
  "view_range": [
    1,
    250
  ]
}

Read 
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\external\\vscode-previewer\\.vscode\\launch.json"
}

Read , lines 250 to 550
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\index.html",
  "view_range": [
    250,
    550
  ]
}

Read , lines 1 to 250
Completed with input: {
  "path": "C:\\Users\\mihai\\rustica\\mohabbat\\washmhost\\js\\browser\\index.test.js",
  "view_range": [
    1,
    250
  ]
}

Here is what `external/vscode-previewer/`, `external/inert.sh/`, and `mohabbat/washmhost/js/browser/index.js` already give us, and how they fit together to run Kabibi as a universal Web Extension across Desktop VS Code, `vscode.dev`, `github.dev`, and OpenVSCode clones.

---

### 1. What the Prior Art Establishes

#### A. `external/vscode-previewer/package.json` & `vscode.w.e.b.js`
1. **Pure Web Extension entry point (`"browser": "./vscode.w.e.b.js"`)**:
   - Declares `"browser"` (instead of `"main"`), which instructs both Desktop VS Code and `vscode.dev` to run the extension inside the **Browser Extension Host WebWorker**.
   - Requires **zero Node.js or native desktop APIs**, so it works identically in `vscode.dev`, `github.dev`, Codespaces web, and desktop VS Code.
2. **Native VS Code Terminal UI via `vscode.Pseudoterminal` + `terminal.profiles`**:
   - Registers a terminal profile (`vscode.window.registerTerminalProfileProvider`) and a launch command (`vscode.window.createTerminal({ name, pty })`).
   - Why this is huge for Kabibi: **VS Code already runs its own xterm.js instance in the Terminal panel!**
   - The `Pseudoterminal` interface gives us:
     - `open(initialDimensions: TerminalDimensions | undefined)` → initial `{ columns, rows }`
     - `onDidWrite: Event<string>` → emits raw ANSI sequences directly to VS Code's xterm.js
     - `handleInput(data: string)` → receives raw keystrokes/VT sequences from VS Code's xterm.js
     - `setDimensions(dimensions: TerminalDimensions)` → receives `{ columns, rows }` on terminal resize!
     - `onDidClose: Event<number | void>` → closes the terminal tab when Kabibi exits (`onExit(code)`).
   - That means we don't even need a separate Webview or external `*-ifrwrk` subdomain just to host xterm.js—WebAssembly (`WebAssembly.instantiate`) and `indexedDB` are natively available right inside the VS Code Web Extension Worker!
3. **Optional Isolated Webview / Iframe Runtime (`external/inert.sh/iframe.l.i.v.e.js`)**:
   - `vscode-previewer` and `inert.sh` also demonstrate spinning up a hidden `WebviewView` (`webPreviewer.runtimeFrameView`) + signed `postMessage` channel to an isolated subdomain (`https://<hash>-ifrwrk.iframe.live`) with a Service Worker injecting COOP/COEP headers (`Cross-Origin-Embedder-Policy: require-corp`, `Cross-Origin-Opener-Policy: cross-origin`).
   - We only need that iframe/ServiceWorker bridge if a guest needs `SharedArrayBuffer` or Service Worker `fetch` interception (e.g., previewing web apps served from Kabibi). For running Kabibi itself, the Web Extension Host Worker can instantiate `kabibi.wasm` (or `kabibi.bat`) directly!

---

### 2. How `mohabbat/washmhost/js/browser/index.js` Already Supports Custom FS Mounting

Looking at `washmhost`:
```js
export default async function washmhost(params = {}) {
  const hostState = new HostState();
  const fs = params.fs || new AsyncIndexedDBFS(params.dbName || 'RusticatedFS');
```
`washmhost` already accepts a pluggable asynchronous `params.fs` object!
Every filesystem syscall in `makeBrowserImports` (`path_open`, `path_stat`, `dir_read`, `write`, `path_chmod`, `path_remove`, `path_mkdir`, `path_rename`) simply awaits the 6 async methods on `fs`:
- `await fs.init()`
- `await fs.seed(extraEntries)`
- `await fs.getEntry(path)` → `{ path, type: 'file' | 'dir', content: Uint8Array, size, mode, mtime }`
- `await fs.putEntry(entry)`
- `await fs.removeEntry(path)`
- `await fs.readdir(dirPath)` → `Array<entry>`

And notice how well this aligns with **HARD RULE #1**:
- Because `rusticated` uses **async I/O** (`overlapped` + `awaitOverlapped` in WASM guest, completed asynchronously via `hostState.enqueue(...)` in JS), **every guest file operation in Kabibi can transparently await `vscode.workspace.fs` promises** without `SharedArrayBuffer` or synchronous blocking!

---

### 3. Proposed Architecture: Kabibi Web Extension + `vscode.workspace.fs` Mount

We can build a clean, self-contained VS Code Web Extension (e.g., in `kabibi/vscode-ext/` or similar) with three components:

#### Component 1: `VSCodeMountedFS` (Composite Virtual Filesystem for `washmhost.js`)
Wraps `AsyncIndexedDBFS` (for `/bin`, `/etc`, `/tmp`, `/home/user`, `/dev`) and mounts `vscode.workspace.workspaceFolders` at `/workspace` (or `/workspace/<folderName>` if multi-root, with `cwd` starting in `/workspace`):
- **`getEntry(path)`**:
  - For paths under `/workspace`, maps the guest path to the workspace folder's `vscode.Uri` via `vscode.Uri.joinPath(folderUri, relPath)`.
  - Calls `vscode.workspace.fs.stat(uri)`:
    - If `FileType.Directory`, returns `{ path, type: 'dir', mode: 0o755, mtime: stat.mtime }`.
    - If `FileType.File`:
      - For `path_stat`, we only need metadata (`stat.size`, `stat.mtime`)—no need to read file bytes on `stat`! (We can either add an optional `fs.statEntry(path)` fast-path to `washmhost.js` so `path_stat` doesn't pull full file contents over `vscode.workspace.fs.readFile`, and only call `readFile(uri)` on `path_open`).
- **`putEntry(entry)`**:
  - For `/workspace/...`:
    - If `entry.type === 'dir'`, calls `vscode.workspace.fs.createDirectory(uri)`.
    - If `entry.type === 'file'`, calls `vscode.workspace.fs.writeFile(uri, entry.content)`.
      - *Optimization*: In `washmhost.js`, `write` currently calls `fs.putEntry(entry)` on every write chunk, or we can flush on `write` / `handle_close` so writing a file in Kabibi immediately updates the file in VS Code's editor and virtual workspace (whether backed by local disk, GitHub repos on `github.dev`, or Remote tunnels).
- **`removeEntry(path)`**:
  - Calls `vscode.workspace.fs.delete(uri, { recursive: false, useTrash: false })`.
- **`readdir(dirPath)`**:
  - Calls `vscode.workspace.fs.readDirectory(uri)` and maps `[name, FileType]` pairs into `{ path, type: 'dir' | 'file' }`.

#### Component 2: `KabibiPseudoterminal` (`vscode.Pseudoterminal` <-> `washmhost`)
Connects a VS Code Terminal tab directly to a `washmhost` instance running `kabibi`:
- **`open(initialDimensions)`**:
  - Tracks `cols = initialDimensions?.columns || 80`, `rows = initialDimensions?.rows || 24`.
  - Fetches `kabibi.wasm` (or `kabibi.bat`) from the extension's packaged assets via `vscode.workspace.fs.readFile(vscode.Uri.joinPath(context.extensionUri, 'kabibi.wasm'))` (or `fetch(asWebviewUri)`).
  - Launches `washmhost`:
    - `stdout: (bytes) => writeEmitter.fire(decoder.decode(bytes, { stream: true }))`
    - `stderr: (bytes) => writeEmitter.fire(decoder.decode(bytes, { stream: true }))`
    - `getTtySize: () => ({ cols, rows })`
    - `onExit: (code) => closeEmitter.fire(code)`
- **`handleInput(data)`**:
  - Calls `host.pushStdin(data)` (passes raw keystrokes, arrows, Ctrl+O for Kabibi's dual blue panels, F4 for editor, etc. straight into Kabibi!).
- **`setDimensions(dims)`**:
  - Updates `cols = dims.columns`, `rows = dims.rows` and calls `host.triggerSignal(28)` (`SIGWINCH`, or `27` depending on `rusticated` signal mapping—let's check: in `rusticated-go/syscall/syscall_rusticated.go`, `SIGWINCH` is `27`!).
  - *Important detail spotted!* Look at `rusticated-go/syscall/syscall_rusticated.go` vs `mohabbat/washmhost/js/browser/index.js`:
    - In `syscall_rusticated.go`, `SIGWINCH` is enum index **`27`** (and Go `washmhost` `env_impl.go` line 211 sends `signum = 27` for `sigwinch`).
    - In `mohabbat/washmhost/js/browser/index.js` line 1604 (and `index.html` line 211), the browser host was triggering `28` instead of `27`! Fixing or triggering both `27` and `28` ensures Kabibi's TUI immediately resizes when the VS Code terminal pane is dragged.

#### Component 3: Extension Integration Extras (`/dev/eval` or VS Code Commands)
- Because Kabibi runs inside the Extension Host Worker, `/dev/eval` (or a `/dev/vscode` proxy file in `VSCodeMountedFS`) can even let shell commands inside Kabibi open files in the native VS Code editor (`vscode.window.showTextDocument`), trigger VS Code commands, or use the HTML previewer from `vscode-previewer`!

---

### Questions to Agree on the Plan Before Any Code Changes

1. **Where should the extension live?**
   - Should we create a dedicated directory (e.g., `vscode-kabibi/` or `kabibi/vscode/`) or evolve `external/vscode-previewer`?
2. **Terminal UI vs. Webview**:
   - Do you want Kabibi to open as a native **VS Code Terminal tab** via `Pseudoterminal` + Terminal Profile (just like `JS Terminal` in `vscode-previewer`, which works in `vscode.dev` and Desktop VS Code), or also offer an editor/sidebar Webview panel?
3. **Workspace Mount Point**:
   - Should the active VS Code workspace folder be mounted directly at `/workspace` (with initial `cwd = '/workspace'`), while `/home/user`, `/tmp`, `/etc` stay backed by `AsyncIndexedDBFS`?+]

