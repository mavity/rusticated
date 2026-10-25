# Kabibi browser terminal

`KabibiBrowser.mount(options)` embeds a normal xterm.js terminal in an existing caller-owned parent. It looks for `options.parent`, then `options.parentId`, then `#kabibi-terminal`. If none resolves, it creates a fixed full-viewport host. Existing parents are not resized or restyled; their dimensions are owned by the caller.

The mount options accept the xterm.js `Terminal` and `FitAddon` constructors, a raw WASM or full vegetable URL/binary in `wasm`, and optional `hostOptions`. The browser `washmhost` detects a vegetable and extracts its embedded WASM guest before launch. If no artifact is supplied, `onData`, `onStdout`, and the returned terminal can be used to connect a caller-managed process. `dispose()` releases the terminal, observers, host, and any super-demo resources.

The standard mode uses xterm's normal renderer. CRT mode is opt-in with `mode: 'super-demo'` and `superDemoDependencies: { THREE, WebglAddon }`. The super-demo uses a bounded 1,440-cell grid whose rows and columns vary proportionally with the host aspect ratio, then runs a colored, screen-filling shell display test that is regenerated after resizes. Its CRT surface preserves the terminal image's aspect ratio, crops the xterm WebGL canvas to its actual render viewport, upscales the captured pixels 2×, and darkens one of every four upscaled image rows before display. A tinted screen surface, curved geometry, and diagonal glass highlight make the CRT treatment visible without stretching glyphs. Append `?flat=1` to `super-demo.html` to inspect the image and scanline pass without curvature, blur, grille, glass, or reflection. xterm's built-in cursor blink is a hard on/off blink; its WebGL renderer does not expose a fade envelope. The CRT canvas and redraw notification also use xterm.js private implementation details; keep xterm.js pinned to the version tested by the demo, and expect the CRT layer to fall back to xterm's direct renderer if those details or GPU support are unavailable. The fallback uses the same terminal instance.

The two example pages are `demo.html` and `super-demo.html`. They use a full vegetable by default. From the repository root, build it beside those pages with:

```powershell
go run . kabibi -o kabibi/browser/kabibi.bat
```

Use the existing Rusticated Go server project to serve the repository (in a separate terminal):

```powershell
go run . demo-go/server -r . --addr 127.0.0.1:8080
```

Then open `http://127.0.0.1:8080/kabibi/browser/demo.html` or `http://127.0.0.1:8080/kabibi/browser/super-demo.html`. Pass `?artifact=<url>` to load another raw WASM or vegetable artifact; `?wasm=<url>` remains accepted as an alias. No Node-based HTTP server is needed. The browser runtime JavaScript is in standalone files, embedded into the Go package with `//go:embed`, and the browser behavior tests run with:

```powershell
node --test .\browser\main.test.cjs
```
