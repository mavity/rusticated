# Kabibi browser terminal

`KabibiBrowser.mount(options)` embeds a normal xterm.js terminal in an existing caller-owned parent. It looks for `options.parent`, then `options.parentId`, then `#kabibi-terminal`. If none resolves, it creates a fixed full-viewport host. Existing parents are not resized or restyled; their dimensions are owned by the caller.

The mount options accept the xterm.js `Terminal`, `FitAddon`, and optional `UnicodeGraphemesAddon` constructors (`@xterm/addon-unicode-graphemes@0.4.0`), a raw WASM or full vegetable URL/binary in `wasm`, and optional `hostOptions`. When `UnicodeGraphemesAddon` is loaded (automatically fetched by `KabibiBrowser.loadBrowserDependencies()` / `bootFromGuest()`), Kabibi enables `allowProposedApi: true`, `customGlyphs: true`, `rescaleOverlappingGlyphs: true`, native color-emoji font fallbacks (`"Apple Color Emoji"`, `"Segoe UI Emoji"`, `"Noto Color Emoji"`), and explicitly activates `term.unicode.activeVersion = '15-graphemes'` so ZWJ emoji clusters, skin-tone modifiers, and variation selectors occupy single character cells matching Kabibi's Go `CellBuf` width math. The browser `washmhost` detects a vegetable and extracts its embedded WASM guest before launch. If no artifact is supplied, `onData`, `onStdout`, and the returned terminal can be used to connect a caller-managed process. `dispose()` returns a promise; in CRT mode it waits for the shutdown animation before releasing the terminal, observers, host, and rendering resources.

The standard mode uses xterm's normal renderer. CRT mode is opt-in with `window.__super_demo__ = true` (or `mode: 'super-demo'` and `superDemoDependencies: { WebglAddon }`). When `washmhostExports.loadVegetable` boots `kabibi.bat` headlessly, Kabibi's embedded browser runtime (`KabibiBrowser.bootFromGuest()`) dynamically loads xterm.js, checks whether `window.__super_demo__` is set and WebGL2 is supported, and either initializes the raw WebGL2 CRT pipeline or falls back to a flat full-page auto-sized xterm.js terminal without flourish. The super-demo uses a bounded 1,440-cell grid whose rows and columns vary proportionally with the host aspect ratio. Its CRT surface preserves the terminal image's aspect ratio, crops the xterm WebGL canvas to its actual render viewport, applies a subtle warm sepia tint to captured terminal pixels before scanlines, upscales the captured pixels 2×, and darkens one of every four upscaled image rows before display. A tinted screen surface, curved paraboloid geometry, thick leaded-glass tangent-frame 3D Snell refraction with subtle Newtonian spectral dispersion, outward-curved multi-ring blurred internal phosphor glyph reflection/halation, sub-surface matte illumination from a vertically elongated 3D ellipsoid light source coupled with a higher-Fresnel spherical accent light that oscillates left-close to right-far, and outer-surface Fresnel sheen make the CRT treatment visible without stretching glyphs. A two-pass separable Gaussian post-process blurs the fully composited screen and glass/reflection layers in linear light while storing its RGBA8 intermediate targets as sRGB, preserving more dark-tone precision than 8-bit linear storage; the final display output remains sRGB. Blur has a maximum 8-pixel radius and a squared distance falloff from a focal point directly above the display's lower-right corner at camera height. The closest and farthest distances are measured on the curved mesh. Append `?flat=1` to `index.html` to inspect the image and scanline pass without curvature, blur, grille, glass, or reflection, or `?super_demo=0` for the unadorned flat terminal. xterm's built-in cursor blink is a hard on/off blink; its WebGL renderer does not expose a fade envelope. The CRT canvas and redraw notification also use xterm.js private implementation details; keep xterm.js pinned to the version tested by the demo, and expect the CRT layer to fall back to xterm's direct renderer if those details or GPU support are unavailable. The fallback uses the same terminal instance.

Super-demo mode includes an always-visible, IBM-PC-inspired on-screen keyboard for Ctrl, Esc, Tab, arrows, and F1–F12. The angular, beveled keycaps use one row on wider displays and wrap on narrow displays; key and label sizing scales with available space. Ctrl is a sticky toggle: tap it once to apply Ctrl to physical or on-screen key presses, then tap it again to turn the modifier off. The Ctrl cap displays the Kabibi SVG logo. For Kabibi-owned fullscreen hosts, the terminal tracks the browser's visual viewport as browser chrome appears or disappears. Existing caller-owned parents remain untouched and must provide their own responsive dimensions. The root demo page opts into safe-area insets so the on-screen keyboard stays clear of device edges.

In CRT mode, power-on is requested after the terminal host is ready and begins
no sooner than 800 ms after the first completed WebGL frame. The bezel remains
visible with the moving glass reflections during this delay. The curved
phosphor surface develops a soft, centre-weighted glow and vignetted line,
then a vertically expanding white flare reveals the live terminal over about
1200 ms. Host exit and `dispose()` both wait 500 ms before collapsing only the
phosphor image to a centre line and dot over about 800 ms; the dot fades over
another second. The glass reflections and fixed bezel remain visible throughout.

In super-demo mode, the page title is updated to `Kabibi v<version>` when the
startup banner arrives; disposing the terminal restores the title that was
present before Kabibi mounted.

The repository root `index.html` boots `washmhost` (`./washmhost.js` in packaged distributions, falling back to `./mohabbat/washmhost/js/browser/index.js` in a dev checkout), sets `window.__super_demo__ = true`, and loads `./kabibi.bat` or `./release/dist/kabibi.bat`. From the repository root, build the vegetable with:

```powershell
go run . kabibi -o release/dist/kabibi.bat
```

Use the existing Rusticated Go server project to serve the repository (in a separate terminal):

```powershell
go run . demo-go/server -r . --addr 127.0.0.1:8080
```

Then open `http://127.0.0.1:8080/index.html`. Pass `?artifact=<url>` to load another raw WASM or vegetable artifact; `?wasm=<url>` remains accepted as an alias. No Node-based HTTP server is needed. Kabibi's browser UI and directory-mount implementation live in `kabibi/browser/main.js` and are embedded in the Go package with `//go:embed`; directory mounting does not require a separate deployment script. Browser behavior tests run with:

```powershell
node --test .\kabibi\browser\main.test.cjs .\mohabbat\washmhost\js\browser\index.test.js .\kabibi\vscode\test\vfs.test.js
```

In browser terminals, right-click opens Kabibi's Copy/Paste/Select all menu.
Ctrl+V and Cmd+V read from the browser clipboard when its permission is
available; terminal-emulator paste events continue to work in native TUI use.

Browser terminals also support `/mount` and Ctrl+M/Cmd+M; Ctrl+O remains the
dual-pane file-manager toggle. The browser asks for read/write access to a
selected directory, then Kabibi opens its TUI path dialog with the current
shell directory as the default guest mount path. Relative paths resolve from
the current shell directory, and `~` / `~/...` expand to the guest home
directory. Kabibi installs the directory-mount adapter from its
embedded browser runtime on the active guest filesystem. Mounted directories
are available to that guest's shell commands and dual-pane file manager for the
current tab session; their handles are not persisted across reloads. Mounting
requires the browser File System Access API in a secure context. If it is
unavailable, the picker is not opened.
The browser API does not expose Unix permissions, symbolic-link creation, or
setting file timestamps; permission bits are guest-side metadata, while
unsupported link/time operations report an error.
