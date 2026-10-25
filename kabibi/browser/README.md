# Kabibi browser terminal

`KabibiBrowser.mount(options)` embeds a normal xterm.js terminal in an existing caller-owned parent. It looks for `options.parent`, then `options.parentId`, then `#kabibi-terminal`. If none resolves, it creates a fixed full-viewport host. Existing parents are not resized or restyled; their dimensions are owned by the caller.

The mount options accept the xterm.js `Terminal` and `FitAddon` constructors, a raw WASM or full vegetable URL/binary in `wasm`, and optional `hostOptions`. The browser `washmhost` detects a vegetable and extracts its embedded WASM guest before launch. If no artifact is supplied, `onData`, `onStdout`, and the returned terminal can be used to connect a caller-managed process. `dispose()` releases the terminal, observers, host, and any super-demo resources.

The standard mode uses xterm's normal renderer. CRT mode is opt-in with `window.__super_demo__ = true` (or `mode: 'super-demo'` and `superDemoDependencies: { THREE, WebglAddon }`). When `washmhostExports.loadVegetable` boots `kabibi.bat` headlessly, Kabibi's embedded browser runtime (`KabibiBrowser.bootFromGuest()`) dynamically loads xterm.js, checks whether `window.__super_demo__` is set and WebGL2 is supported, and either initializes the Three.js CRT pipeline or falls back to a flat full-page auto-sized xterm.js terminal without flourish. The super-demo uses a bounded 1,440-cell grid whose rows and columns vary proportionally with the host aspect ratio. Its CRT surface preserves the terminal image's aspect ratio, crops the xterm WebGL canvas to its actual render viewport, upscales the captured pixels 2×, and darkens one of every four upscaled image rows before display. A tinted screen surface, curved geometry, and diagonal glass highlight make the CRT treatment visible without stretching glyphs. Append `?flat=1` to `index.html` to inspect the image and scanline pass without curvature, blur, grille, glass, or reflection, or `?super_demo=0` for the unadorned flat terminal. xterm's built-in cursor blink is a hard on/off blink; its WebGL renderer does not expose a fade envelope. The CRT canvas and redraw notification also use xterm.js private implementation details; keep xterm.js pinned to the version tested by the demo, and expect the CRT layer to fall back to xterm's direct renderer if those details or GPU support are unavailable. The fallback uses the same terminal instance.

Super-demo mode includes an always-visible on-screen keyboard for Esc, Tab, arrows, and F1–F12. It uses one row on wider displays and two rows on narrow displays; key and label sizing scales with the available space. For Kabibi-owned fullscreen hosts, the terminal tracks the browser's visual viewport as browser chrome appears or disappears. Existing caller-owned parents remain untouched and must provide their own responsive dimensions. The root demo page opts into safe-area insets so the on-screen keyboard stays clear of device edges.

The repository root `index.html` boots `washmhost` (`./washmhost.js` in packaged distributions, falling back to `./mohabbat/washmhost/js/browser/index.js` in a dev checkout), sets `window.__super_demo__ = true`, and loads `./kabibi.bat` or `./release/dist/kabibi.bat`. From the repository root, build the vegetable with:

```powershell
go run . kabibi -o release/dist/kabibi.bat
```

Use the existing Rusticated Go server project to serve the repository (in a separate terminal):

```powershell
go run . demo-go/server -r . --addr 127.0.0.1:8080
```

Then open `http://127.0.0.1:8080/index.html`. Pass `?artifact=<url>` to load another raw WASM or vegetable artifact; `?wasm=<url>` remains accepted as an alias. No Node-based HTTP server is needed. The browser runtime JavaScript is in standalone files, embedded into the Go package with `//go:embed`, and the browser behavior tests run with:

```powershell
node --test .\kabibi\browser\main.test.cjs
```

In browser terminals, right-click opens Kabibi's Copy/Paste/Select all menu.
Ctrl+V and Cmd+V read from the browser clipboard when its permission is
available; terminal-emulator paste events continue to work in native TUI use.
