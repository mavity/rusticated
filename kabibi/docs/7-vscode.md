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

> 1. Yes, kabibi/vscode sounds about right.
> 2. Yes definitely native VSCode Terminal, and both in web and native VSCode in the same fashion.
> 3. Yes I like this idea: /workspace as the mount point for the VSCode filesystem.

---

# Rigorous Architecture & Implementation Plan: `kabibi/vscode`

## 1. Design Goals & Hard Constraints

1. **Single Universal Web Extension**:
   - Lives in `kabibi/vscode/`.
   - Declares `"browser": "./extension.js"` (no `"main"` entry point) so Desktop VS Code, `vscode.dev`, `github.dev`, Codespaces Web, and OpenVSCode clones all run the exact same code inside the **Browser Extension Host WebWorker** (or Node Extension Host if loaded there, since standard `WebAssembly`, `TextEncoder`, `TextDecoder`, and `vscode.workspace.fs` exist in both!).
2. **Pure Raw JavaScript — Zero TypeScript / Zero Bundler**:
   - No `tsc`, no `webpack`, no `esbuild`, no transpilation step.
   - Type safety and IDE completions are provided via `// @ts-check` and JSDoc annotations (`/** @param {import('vscode').ExtensionContext} context */`), following the exact pattern of `external/vscode-previewer/vscode.w.e.b.js`.
3. **Native VS Code Terminal UI via `vscode.Pseudoterminal`**:
   - Integrates directly into VS Code's built-in Terminal panel (which runs xterm.js natively in both Desktop and Web VS Code).
   - Provides both a **Terminal Profile** (`kabibi.terminalProfile`, selectable from the `+ v` terminal dropdown) and a **Command Palette / keybinding command** (`kabibi.openTerminal` — `"Kabibi: Open Shell"`).
4. **Async Virtual Filesystem Mounted at `/workspace`**:
   - Complies strictly with **HARD RULE #1** (100% async I/O; zero blocking calls).
   - Mounts `vscode.workspace.workspaceFolders` into the guest at `/workspace` backed by `vscode.workspace.fs`, while keeping `/`, `/bin`, `/etc`, `/home/user`, `/tmp`, and `/var` backed by `AsyncIndexedDBFS` (with automatic `Map` fallback when `indexedDB` is unavailable).
5. **First-Class Vegetable Packaging**:
   - Packages the Kabibi vegetable (`kabibi.bat` or `kabibi.wasm`) and `washmhost.js` directly inside the `.vsix` extension bundle, with zero external build tooling required at runtime.

---

## 2. Directory Layout (`kabibi/vscode/`)

```text
kabibi/vscode/
├── package.json          # Extension manifest ("browser": "./extension.js", terminal profile & commands)
├── .vscodeignore         # Excludes node_modules/, test artifacts, and dev configs from .vsix
├── extension.js          # Raw JS CommonJS entry point (activate / deactivate, Pseudoterminal, commands)
├── vfs.js                # VSCodeMountedFS: hybrid AsyncIndexedDBFS + vscode.workspace.fs mount at /workspace
├── washmhost.js          # Staged copy of mohabbat/washmhost/js/browser/index.js (with UMD/CJS+ESM export)
├── kabibi.bat            # Staged Kabibi vegetable (or kabibi.wasm), unpacked in memory by unpackVegetable()
└── test/
    └── vfs.test.js       # Node-runnable unit tests for VSCodeMountedFS path routing, stat, read, write, readdir
```

---

## 3. Module Loading in the VS Code Web Extension Host (No Bundler)

### The Constraint
VS Code's Web Extension Host loads the `"browser"` entry point (`./extension.js`) using a CommonJS wrapper (`module`, `exports`, `require('vscode')`) fetched over `vscode-extension://` or `https://*.vscode-unpkg.net`. Inside that evaluation context:
- Static ES module `import ... from './washmhost.js'` cannot be used in the entry file because VS Code evaluates the `"browser"` script as CommonJS, not `<script type="module">`.
- `require('./ other.js')` works in Desktop VS Code, **but in `vscode.dev` (Browser Extension Host Worker)**, `require` is a minimal shim that only resolves `'vscode'` (it cannot synchronously fetch relative `.js` files over HTTP!).

### The Zero-Build Solution
To run in `vscode.dev` **without a bundler** while keeping clean source files:
1. **Option 1 (Single-File Polyglot/Concatenated `extension.js` or `importScripts` in Worker)**:
   - In a WebWorker (`vscode.dev`), `importScripts` or `fetch(vscode.Uri.joinPath(context.extensionUri, '...').toString())` can load sibling scripts, **or** even simpler:
   - Make `mohabbat/washmhost/js/browser/index.js` support both ES Modules and global/CommonJS attachment (or dynamically load it via `vscode.workspace.fs.readFile(vscode.Uri.joinPath(context.extensionUri, 'washmhost.js'))` on terminal open!).
   - Think about how clean `vscode.workspace.fs.readFile` is:
     - `vscode.workspace.fs.readFile(vscode.Uri.joinPath(context.extensionUri, '...'))` works in **every** VS Code environment (Desktop, `vscode.dev`, `github.dev`, Serverless Web) because `context.extensionUri` is always readable by `vscode.workspace.fs`!
     - Or, if `extension.js` is self-contained (or loads `washmhost.js` via `vscode.workspace.fs.readFile` / `import()`), there is zero dependency on `webpack`/`esbuild` and zero `require('./...')` failure in `vscode.dev`.
2. **Universal Export in `mohabbat/washmhost/js/browser/index.js`**:
   - Currently `mohabbat/washmhost/js/browser/index.js` uses `export const ...` and `export default async function washmhost(...)`, plus `if (typeof window !== 'undefined') window.washmhost = washmhost;`.
   - In a WebWorker (`self` / `globalThis`), `window` is `undefined`!
   - We should update the bottom of `mohabbat/washmhost/js/browser/index.js` to also attach to `globalThis`:
     ```js
     if (typeof globalThis !== 'undefined') {
       globalThis.washmhost = washmhost;
       globalThis.washmhostExports = {
         washmhost, HostState, AsyncIndexedDBFS, resolvePath, marshalStat,
         STAT_KIND_UNKNOWN, STAT_KIND_FILE, STAT_KIND_DIR, STAT_KIND_SYMLINK,
         FLAG_COMPLETED, mapErrno, unpackVegetable, makeBrowserImports, runEventLoop
       };
     }
     ```
   - This preserves 100% ES Module compatibility for `<script type="module">` and `node:test`, while allowing any Worker or non-ESM host to access `globalThis.washmhostExports`!

---

## 4. Packaging the Vegetable with the Extension

### How the Vegetable is Built & Staged
1. **`kabibi.bat` vs `kabibi.wasm`**:
   - `washmhost.js` (`unpackVegetable(buffer)`) **already knows how to unpack `kabibi.bat` directly in memory** using the embedded `brotli-wasm` / zstd decompressor WASM in Zone B!
   - Even better: `kabibi.bat` (~21 MB) is Zstd-compressed (`SpeedBestCompression`), whereas raw `kabibi.wasm` is ~36.7 MB.
   - Packaging `kabibi.bat` inside `kabibi/vscode/kabibi.bat` means:
     - The extension contains a **real, runnable Mohabbat vegetable** (so users on desktop can even run `./kabibi.bat` directly if they want, while in the browser/extension `washmhost.js` unpacks the compressed pool in memory!).
2. **Loading the Vegetable in `kabibi/vscode/extension.js`**:
   - When the user opens a Kabibi terminal, the extension loads the vegetable bytes using VS Code's universal extension asset API:
     ```js
     const vegUri = vscode.Uri.joinPath(context.extensionUri, 'kabibi.bat');
     const vegBytes = await vscode.workspace.fs.readFile(vegUri);
     ```
   - Why `vscode.workspace.fs.readFile(vegUri)` instead of `fetch()`?
     - In `vscode.dev` and Desktop VS Code, `context.extensionUri` uses scheme `vscode-extension:` or `https:` or `file:`. `vscode.workspace.fs.readFile` handles **all** of those schemes natively without CORS issues, CSP `connect-src` blocks, or `fetch('file://...')` failures!
   - **Caching the Unpacked WASM Payload / Compiled `WebAssembly.Module`**:
     - Decompressing `kabibi.bat` via `unpackVegetable(vegBytes)` and compiling `WebAssembly.compile(wasmBuffer)` takes ~200–400ms on first terminal launch.
     - We cache the unpacked `ArrayBuffer` (or compiled `WebAssembly.Module`) in a module-level promise (`cachedWasmBinaryPromise`) inside `extension.js` so opening a 2nd or 3rd Kabibi terminal tab is **instantaneous (< 10ms)**!
3. **Integration with `release/main.go` (and `package.json` scripts)**:
   - In `release/main.go` (after building `release/dist/kabibi.bat` and `release/dist/washmhost.js`), or via a dedicated step / `go run . kabibi -o kabibi/vscode/kabibi.bat`, we stage:
     - `release/dist/kabibi.bat` → `kabibi/vscode/kabibi.bat`
     - `mohabbat/washmhost/js/browser/index.js` → `kabibi/vscode/washmhost.js`
   - Running `npx @vscode/vsce package` in `kabibi/vscode/` then produces a self-contained `kabibi-vscode-<version>.vsix` containing `extension.js`, `vfs.js`, `washmhost.js`, and `kabibi.bat`.

---

## 5. Deep-Dive: `VSCodeMountedFS` (`/workspace` Virtual Filesystem Projection)

### 5.1 Mount Topology
- **System / Ephemeral Root (`/`, `/bin`, `/etc`, `/home/user`, `/tmp`, `/var`, `/usr`, `/dev`)**:
  - Backed by `AsyncIndexedDBFS('KabibiVSCodeFS')`.
  - Persists user dotfiles/history in `/home/user` across reloads via IndexedDB (in browser/web) or in-memory map (if running in a host without IndexedDB).
- **Workspace Mount (`/workspace`)**:
  - Backed live by `vscode.workspace.fs` and `vscode.workspace.workspaceFolders`.
  - **Single-folder workspace (`workspaceFolders.length === 1`)**:
    - `/workspace` maps directly to `workspaceFolders[0].uri`.
    - Example: `/workspace/README.md` ↔ `vscode.Uri.joinPath(workspaceFolders[0].uri, 'README.md')`.
  - **Multi-root workspace (`workspaceFolders.length > 1`)**:
    - `/workspace` is a synthetic directory listing each workspace folder by name (`/workspace/<folder.name>`).
    - `/workspace/<folder.name>/...` maps to `vscode.Uri.joinPath(folder.uri, '...')`.
  - **No workspace open (`!workspaceFolders || workspaceFolders.length === 0`)**:
    - `/workspace` falls back to a normal directory in `AsyncIndexedDBFS`, and Initial CWD defaults to `/home/user` (or `/workspace`).
  - Dynamic workspace changes (`vscode.workspace.onDidChangeWorkspaceFolders`) are reflected immediately because path resolution queries `vscode.workspace.workspaceFolders` live on each operation.

### 5.2 Crucial `washmhost.js` Filesystem Enhancements Required
Inspecting `mohabbat/washmhost/js/browser/index.js` lines 595–1025 reveals **four specific behaviors** in `washmhost.js` that we must refine so `VSCodeMountedFS` works cleanly and performantly with Kabibi and `u-root` coreutils:

1. **`path_stat` vs `path_open` (Avoid reading full file bytes just for `stat` / `ls -l` / Dual-Pane File Manager)**:
   - Currently in `mohabbat/washmhost/js/browser/index.js`:
     - Both `path_stat` (line 903) and `path_open` (line 868) call `fs.getEntry(p)`.
     - If `fs.getEntry(p)` always calls `vscode.workspace.fs.readFile(uri)`, then running `ls -la` or opening Kabibi's `Ctrl+O` Dual-Pane File Manager in a directory with 50 files would read the **entire byte content of all 50 files** just to stat their sizes and mtimes!
   - **Fix in `washmhost.js`**:
     - Have `path_stat` call `(fs.statEntry ? fs.statEntry(p) : fs.getEntry(p))`.
     - In `VSCodeMountedFS`:
       - `statEntry(p)` calls `vscode.workspace.fs.stat(uri)` (which returns `{ type, ctime, mtime, size }` in < 1ms without reading file contents!).
       - `getEntry(p)` (called by `path_open`) calls `vscode.workspace.fs.stat(uri)`, and only if `FileType.File`, calls `vscode.workspace.fs.readFile(uri)` to populate `entry.content`.

2. **`O_TRUNC` and `O_APPEND` handling in `path_open` and `write`**:
   - Let's check what `rusticated-go/syscall/fs_rusticated.go` passes to `rusticated_path_open(ov, pathPtr, pathLen, uint32(mode))`:
     - In `rusticated-go/syscall/syscall_rusticated.go` lines 211–219:
       - `O_RDONLY = 0`, `O_WRONLY = 1`, `O_RDWR = 2`
       - `O_CREAT = 0100` (`64`)
       - `O_EXCL = 0200` (`128`)
       - `O_TRUNC = 01000` (`512`)
       - `O_APPEND = 02000` (`1024`)
     - Look at `mohabbat/washmhost/js/browser/index.js` lines 645–663 (`write`) and 868–888 (`path_open`):
       - If an **existing** file is opened with `O_CREAT | O_TRUNC | O_WRONLY` (e.g. `echo hi > existing.txt` or saving a shorter file in Kabibi's F4 Editor!), `path_open` currently fetches the existing `entry` **without truncating `entry.content` or `entry.size`**, and `write` does `entry.size = Math.max(entry.size || 0, needed)`!
       - That means overwriting a 100-byte file with a 10-byte file leaves the remaining 90 bytes of old garbage at the end of the file!
   - **Fix in `washmhost.js`**:
     - In `path_open`:
       - When `flags & 512` (`O_TRUNC`) is set on an existing file, reset `entry.content = new Uint8Array(0)` and `entry.size = 0`, and persist `fs.putEntry(entry)`!
       - When `flags & 64` (`O_CREAT`) is set and `!entry`, create the file.
       - Store `flags` on the allocated handle (`offset: (flags & 1024) ? entry.size : 0`).
     - In `write`:
       - When `buf` is grown with `Math.max(needed, buf.length * 2)` for capacity, `entry.content` was being set to the oversized `buf` (`entry.content = buf`), which causes `vscode.workspace.fs.writeFile(uri, entry.content)` to write trailing zero bytes up to the doubled capacity!
       - Instead, `entry.content` passed to `fs.putEntry` (or `entry.content.subarray(0, entry.size)`) must always be sliced to `entry.size`!

3. **`set_cwd` and Initial Working Directory (`params.cwd`)**:
   - Currently `HostState` hardcodes `this.cwd = '/home/user'` (line 340).
   - We should allow `params.cwd` in `washmhost(params)` (defaulting to `'/workspace'` when a workspace folder is open in VS Code, or `'/home/user'` otherwise), and set `PWD=/workspace` in `params.env`.

4. **Recursive / Directory Renames & Removals in `VSCodeMountedFS`**:
   - `path_rename(src, dst)`:
     - When both `src` and `dst` are inside `/workspace`, use `vscode.workspace.fs.rename(srcUri, dstUri, { overwrite: true })` directly!
     - When moving across `/tmp` ↔ `/workspace`, copy bytes and remove source.
   - `path_remove(p)`:
     - In `/workspace`, call `vscode.workspace.fs.delete(uri, { recursive: false, useTrash: false })`.

---

## 6. Deep-Dive: Terminal Lifecycle, Signals (`SIGWINCH`), & TUI Fidelity

### 6.1 Fixing the Browser `SIGWINCH` Signal Number Mismatch
During our investigation, we uncovered a critical mismatch between the Go guest runtime and `mohabbat/washmhost/js/browser/index.js`:
- In `rusticated-go/syscall/syscall_rusticated.go` (lines 102–133), the `Signal` enum is:
  - `SIGINT = 2`
  - `SIGTERM = 15`
  - `SIGWINCH = 27` (0-indexed enum item 27!)
- Native `washmhost` (`mohabbat/washmhost/env_impl.go` line 211) correctly translates host `SIGWINCH` (`0x1c = 28`) to guest `signum = 27`.
- However, `mohabbat/washmhost/js/browser/index.js` (line 1604) and `index.html` (line 211) were calling `hostState.triggerSignal(28)` (`SIGPOLL`), so the Go guest's `signal.Notify(winchCh, syscall.SIGWINCH)` in `kabibi/ui/host_platform_signals_unix.go` **never received `SIGWINCH` in the browser!**
- **Fix**:
  - In `mohabbat/washmhost/js/browser/index.js` (and `KabibiPseudoterminal.setDimensions`), trigger signal `27` (or both `27` and `28` so both Go guests expecting `27` and Rust guests expecting `28` wake up on resize!).

### 6.2 `KabibiPseudoterminal` Wiring
Each VS Code terminal instance created via the `"Kabibi"` profile or `"Kabibi: Open Shell"` command creates an isolated `washmhost` instance:
```js
class KabibiPseudoterminal {
  constructor(context, vfs) {
    this.context = context;
    this.vfs = vfs;
    this.writeEmitter = new vscode.EventEmitter();
    this.closeEmitter = new vscode.EventEmitter();
    this.onDidWrite = this.writeEmitter.event;
    this.onDidClose = this.closeEmitter.event;
    this.cols = 80;
    this.rows = 24;
    this.host = null;
    this.decoder = new TextDecoder('utf-8');
  }
```
- **`open(initialDimensions)`**:
  - Sets `this.cols = initialDimensions?.columns || 80`, `this.rows = initialDimensions?.rows || 24`.
  - Loads cached unpacked WASM bytes from `kabibi.bat`.
  - Starts `washmhost({ wasm, fs: this.vfs, cwd: initialCwd, env: { TERM: 'xterm-256color', COLORTERM: 'truecolor', HOME: '/home/user', PWD: initialCwd, PATH: '/bin' }, stdout, stderr, getTtySize: () => ({ cols: this.cols, rows: this.rows }), onExit: (code) => this.closeEmitter.fire(code ?? 0) })`.
- **`handleInput(data)`**:
  - Forwards raw terminal input (`data`) directly to `this.host.pushStdin(data)`.
  - Because `kabibi/ui/host.go` puts the guest TTY into raw mode (`term.MakeRaw`) and uses `charmbracelet/x/input` to parse ANSI/VT sequences, arrow keys, `Ctrl+C`, `Ctrl+O` (Dual-Pane File Manager), `F4` (Editor), `F5/F6/F7/F8`, and UTF-8 text work out of the box!
- **`setDimensions(dimensions)`**:
  - Updates `this.cols = dimensions.columns`, `this.rows = dimensions.rows`.
  - Calls `this.host.triggerSignal(27)` (and `28`) so `kabibi/ui/host.go` immediately re-queries `term.GetSize` (`tty_get_size`) and redraws the stack/panels to the exact new terminal dimensions.
- **`close()`**:
  - If the user clicks the trash can icon on the VS Code Terminal tab, calls `this.host.triggerSignal(15)` (`SIGTERM`) and `this.host.terminate(0)` so Kabibi exits cleanly without leaked timers or pending operations.

---

## 7. Host Bridge Extras: Opening Files in VS Code from Kabibi

Because Kabibi runs in the Extension Host Worker and `washmhost.js` already provides `/dev/eval` (an async JS evaluation device at `/dev/eval`), we can expose a clean global helper in the worker scope (`globalThis.__kabibi_vscode`) or mount a virtual control device so that inside Kabibi:
- Editing or creating files in `/workspace` immediately reflects in VS Code's File Explorer and open editors.
- Optional convenience: we can seed `/bin/code` (a tiny shell script in `AsyncIndexedDBFS` at `/bin/code`) that writes the resolved path to `/dev/vscode-open` (or `/dev/eval`), allowing the user (or the Kabibi AI agent!) to type:
  ```sh
  code README.md
  ```
  inside the Kabibi terminal to open `/workspace/README.md` in a native VS Code editor tab via `vscode.window.showTextDocument(uri)`!

---

## 8. Step-by-Step Implementation & Verification Plan

1. **Step 1 — Refine `mohabbat/washmhost/js/browser/index.js`**:
   - Fix `O_TRUNC` / `O_APPEND` in `path_open` and slice `entry.content` to `entry.size` in `write` so overwritten files never retain trailing bytes or buffer-doubling padding.
   - Support optional `fs.statEntry(path)` in `path_stat` so statting files on a mounted VFS does not download full file contents.
   - Support `params.cwd` in `washmhost(params)`.
   - Trigger both `27` (Go `wasip1` `SIGWINCH`) and `28` (Unix/Rust `SIGWINCH`) on resize.
   - Export `washmhost` and helpers on `globalThis.washmhostExports` in addition to ES module `export` so WebWorkers without ESM bundlers can consume it directly.
   - Add unit tests in `mohabbat/washmhost/js/browser/index.test.js` covering `O_TRUNC`, `statEntry`, `params.cwd`, and signal `27`.

2. **Step 2 — Create `kabibi/vscode/` Raw-JS Web Extension**:
   - `kabibi/vscode/package.json`:
     - `"browser": "./extension.js"`
     - Contributes command `kabibi.openTerminal` (`"Kabibi: Open Shell"`) and terminal profile `kabibi.terminalProfile` (`"Kabibi"`).
   - `kabibi/vscode/vfs.js`:
     - Implements `VSCodeMountedFS` combining `AsyncIndexedDBFS` with `vscode.workspace.fs` mounted at `/workspace` (plus `/bin/code` helper to open files in VS Code editor tabs).
   - `kabibi/vscode/extension.js`:
     - Implements `activate`, `KabibiPseudoterminal`, vegetable caching via `vscode.workspace.fs.readFile`, and terminal profile/command registration.
   - `kabibi/vscode/.vscodeignore` and `.vscode/launch.json` for one-click Web Extension Host debugging.

3. **Step 3 — Stage Vegetable & Host Assets**:
   - Copy `mohabbat/washmhost/js/browser/index.js` → `kabibi/vscode/washmhost.js` (and `release/dist/washmhost.js`).
   - Stage `release/dist/kabibi.bat` → `kabibi/vscode/kabibi.bat` and update `release/main.go` so future release builds keep `kabibi/vscode/` in sync automatically.

4. **Step 4 — Verification & Testing**:
   - Run `node --test mohabbat/washmhost/js/browser/index.test.js`.
   - Write and run `node --test kabibi/vscode/test/vfs.test.js` testing `VSCodeMountedFS` against a mocked `vscode.workspace.fs` (verifying `/workspace` stat, read, write, truncate, mkdir, readdir, rename, delete, and fallback to `AsyncIndexedDBFS` for `/tmp` and `/home/user`).
   - Verify end-to-end boot of `kabibi.bat` through `washmhost.js` + `VSCodeMountedFS` in a headless test driving `KabibiPseudoterminal` (`ls /workspace`, `cat /workspace/...`, writing a file in `/workspace`, resizing terminal dimensions).
