# Kabibi Browser Terminal & Super-Demo CRT Specification

This specification separates Kabibi’s normal browser terminal from its optional CRT super-demo. The standard browser experience is an embeddable xterm.js terminal; it is not full-screen by default and has no CRT blur or flourish. The CRT is an explicit super-demo presentation built on the same terminal and host integration. The DOM File Panels HUD remains deferred to Phase 2.

## 1. Modes, Mounting Contract & Core Invariants

### 1.1 Default Mode: Mount a Normal Terminal in the Caller’s Parent

Kabibi’s default browser mode creates and runs xterm.js inside a parent supplied by its caller. Parent selection follows a documented precedence:

1. An explicit DOM element passed in the Kabibi browser options.
2. An explicit parent element ID passed in those options.
3. A documented conventional parent ID used by the embedding page.
4. If no parent resolves, create a parent container, append it to `document.body`, and lock that newly created host to the full viewport.

When an existing parent is found, Kabibi must not move it, resize it to full-screen, or take over surrounding page layout. xterm should fill the parent’s available content box and fit its terminal rows and columns to that size. Observe the parent’s size so fitting follows container resizes, not just window resizes. The caller retains responsibility for the parent’s dimensions. Input, output, TTY sizing, and resize notifications continue through the existing browser host path.

Default mode uses ordinary xterm.js rendering, with no Three.js stage, off-screen framebuffer capture, curvature, blur, or CRT-specific event translation. It remains usable as a normal embedded terminal inside a larger page.

### 1.2 Super-Demo Mode: Explicitly Opt Into the CRT Presentation

The CRT / blur / flourish is enabled only when the embedding page explicitly selects super-demo mode, either by passing a mode option to Kabibi or by loading a dedicated super-demo page that does so. The implementation may choose whichever is simpler, but the default mode must never infer super-demo from being in a browser or from the host being full-screen.

Super-demo mode uses the same Kabibi terminal guest and host callbacks while rendering xterm’s screen surface through Three.js. When it creates its own parent, that parent is full-screen. If super-demo is embedded in a caller-provided parent, it uses that parent’s bounds rather than silently taking over the browser viewport.

### 1.3 Super-Demo CRT Visual Requirements

* **Off-Screen xterm Surface:** The current implementation pins `@xterm/xterm` 6.0.0 and uses `new WebglAddon(true)` (`preserveDrawingBuffer: true`). It reads the addon's private `WebglRenderer._canvas` and subscribes to the private `RenderService.onRender` notification to refresh a Three.js `CanvasTexture`. These private xterm.js APIs are version-sensitive; if either is unavailable, the CRT path must fall back rather than treating the glyph `textureAtlas` as the screen.
* **3D Curved Geometry & Glass:** Three.js bends inner phosphor and outer refractive/reflective glass meshes. The glass effects should be restrained and keep terminal text readable.
* **Subtle Bottom-Right Low-Angle Camera:** The `PerspectiveCamera` views the curved screen from a slightly below-and-right position, with restrained perspective inspired by low-angle car posters. The composition should make the lower-right appear closer without compromising readability.
* **Near-Field Depth-of-Field:** A subtle camera-space depth mask in the phosphor shader applies a small multi-sample blur only to the nearer lower-right area, leaving most terminal text sharp.
* **Pointer Translation:** Mouse events on the rendered surface are raycast onto the CRT geometry and mapped to xterm screen coordinates, including drag tracking outside the canvas. Keyboard input remains on the same focused terminal; preserve selection, clipboard, TTY resize, and mouse-reporting behavior.
* **Non-GPU Fallback:** If the accelerated CRT path cannot run or loses its context, show the same terminal instance using direct-to-DOM xterm.js inside the selected parent, with no flourish and no loss of guest state.

### 1.4 JavaScript Source, Go Embedding & Node Tests

Kabibi browser JavaScript must be authored in standalone `.js` files, not maintained as large quoted or backtick-escaped Go string literals. Go packages should embed the relevant JavaScript assets at build time with `//go:embed` and pass or evaluate those sources through the existing browser host mechanism.

Keep browser runtime code and DOM-independent logic in JavaScript files that can also be exercised outside the browser. Use Node.js’s built-in test runner (`node --test`) for focused tests of mount-parent resolution, default-versus-super-demo mode selection, fallback decisions, and other testable behavior. Tests should not require a browser page or Go build. Avoid introducing a transpilation or bundling step unless module loading through the current `EvalJS` mechanism proves it necessary; document that constraint if a bundle is required.

### 1.5 DOM HUD Phase Boundary

The Floating DOM Blue Panels HUD and Go-to-DOM state synchronization remain paused and are not part of the CRT super-demo phase. In both default and super-demo modes, the existing TUI file panels render inside the terminal via their normal VT/ANSI output. The DOM HUD can be reconsidered as a later phase after terminal rendering, pointer behavior, and fallback are proven.

The overall complexity is **moderate-to-high, but highly partitioned**. The standard embedded terminal is the baseline; the CRT is a separate, explicitly enabled super-demo mode. The CRT pipeline is proven independently before any DOM overlay work.

---

### Super-Demo Track: The 3D Curved CRT, Low-Angle Camera & Near-Field Depth-of-Field (Active Scope)

The super-demo rendering pipeline builds on Kabibi’s browser host integration. It is opt-in and must not change the default embedded xterm.js behavior:

* **Off-Screen `@xterm/xterm` Surface:** The implementation uses xterm.js 6.0.0's private `WebglRenderer._canvas` and `RenderService.onRender` hook with `new WebglAddon(true)` (`preserveDrawingBuffer: true`) to give Three.js a live `CanvasTexture` source. This is an explicit version-sensitive dependency, not a public xterm contract; the glyph sprite sheet (`WebglAddon.textureAtlas`) is not the screen.
* **3D Curved Geometry, Stylized Glass & Low-Angle Camera:** Instead of a flat 2D post-processing quad, Three.js bends two 3D meshes (the inner phosphor screen and outer refractive/reflective glass faceplate). The `PerspectiveCamera` views the curved screen from a **subtle bottom-right low angle**—like a stylish low-angle car poster—so the lower-right corner curves closer toward the lens while preserving clean terminal legibility.
* **Subtle Near-Field Depth-of-Field (DoF) Blur:** Coupled with the low-angle camera and 3D surface curvature, the phosphor shader applies a subtle camera-space depth mask and multi-sample blur to the closer lower-right region while keeping the main reading area sharp.
* **Off-Screen Input & Raycast Mouse Inversion:** Preserve the existing keyboard, clipboard, and resize wiring. In super-demo mode, mouse events on the rendered canvas are raycast onto the 3D phosphor mesh and mapped back into xterm’s screen coordinates; drag events continue through the same mapping when the pointer leaves the canvas.
* **Non-GPU Direct-to-DOM Fallback:** When the super-demo GPU path is unavailable or loses context, dispose `WebglAddon` and reveal xterm.js’s built-in `DomRenderer` on the same `Terminal` instance. The terminal fills its selected parent, but only fills the viewport when that parent was created by Kabibi as a full-screen host.

---

### Phase 2 (Paused / Deferred): The DOM File Panels HUD & Animations

To keep Phase 1 focused strictly on the off-screen xterm surface, 3D CRT optics, camera framing, depth-of-field, and input fidelity, the **Floating DOM Blue Panels HUD is paused and deferred to Phase 2**:

* **Current Behavior:** Pressing `Ctrl+O` renders Kabibi’s Dual Blue File Manager Panels inside the terminal via standard ANSI output—requiring zero Go-to-DOM synchronization in either browser mode.
* **Phase 2 Behavior (Future):** Once the embedded terminal and super-demo CRT paths are complete, the blue panels can optionally be promoted to a crisp HTML/CSS DOM overlay above the CRT canvas with CSS spring transitions, SVG icons, and `eval`-bridge cursor synchronization.

---

### Complexity Summary

| Phase & Component | Status | Effort / Difficulty | Primary Risk / Edge Case |
| --- | --- | --- | --- |
| **Default embedded xterm mode** | **Required baseline** | Low–Medium | Resolve caller parent and fit to its content box without taking over page layout. |
| **Standalone JS assets & Node tests** | **Required foundation** | Low–Medium | Keep embedded JS compatible with `EvalJS` while independently testable. |
| **Super-demo xterm surface & fallback** | **Active** | Medium | Expose framebuffer and redraw notifications without breaking xterm’s rendering lifecycle. |
| **Super-demo curved CRT, camera & DoF** | **Active** | Medium–High | Tune perspective and shallow blur while preserving text legibility. |
| **Super-demo raycast input** | **Active** | Medium | Prevent raw document-level drag events from bypassing coordinate translation. |
| **Floating DOM File Panels HUD** | **Paused / Deferred** | Low–Medium | Later synchronization and perspective alignment; excluded from current phase. |

---

# Super-Demo Rendering Design


The following sections describe the opt-in super-demo CRT presentation, engineered against **Three.js r186 (`0.186.0`)** and **`@xterm/xterm` (`6.0.0`)** (with optional vendoring/patching of `@xterm/xterm` and `@xterm/addon-webgl`). They do not redefine Kabibi’s default embedded terminal mode.


## 2. Super-Demo Architecture & Core Invariants

1. **Super-Demo Viewport Uses Its Selected Parent:**
* A dedicated super-demo page is the preferred low-coupling option; an explicit `super-demo` mode flag is also acceptable. Do not repurpose or silently alter Kabibi’s default embedding page.
* If the caller supplies a parent, the super-demo stage fills that parent. If Kabibi creates its own parent because none was found, it locks that host to the full viewport (`100vw × 100vh`, `overflow: hidden`).
* All diagnostic output, AI deliberation tickers (`[goal]…`, `[steps]…`, `[work]…`), tool statuses, Dual Blue File Manager Panels (in Phase 1), and shell history live **inside the terminal stack itself**, exactly as designed in the Unified `CellBuf` & Plume architecture.

2. **Off-Screen `@xterm/xterm` (v6.0.0) + `@xterm/addon-webgl` (v0.11.0) Framebuffer Source:**
* In super-demo mode, `@xterm/xterm` renders at native high-DPI resolution inside a background container covering the selected host (`position: absolute; inset: 0; opacity: 0; pointer-events: none`) using `new WebglAddon(true)` (`preserveDrawingBuffer: true`) so glyph rasterization, ligatures, DEC mode 2026 synchronized output, and truecolor ANSI sequences emitted by `ui.Host` remain fast and accurate. The host is full-viewport only when Kabibi created it.
* Rather than misusing the public `webglAddon.textureAtlas` getter (which in xterm.js 6.0.0 returns only the 2D character glyph sprite sheet `this._charAtlas?.pages[0].canvas`), we capture the actual rendered terminal screen canvas (`WebglRenderer._canvas`) and trigger texture uploads from `RenderService.onRender`—vendoring and patching xterm.js / `@xterm/addon-webgl` if needed to expose a clean public `renderCanvas` getter and frame-render callback.

3. **Three.js r186 (`three@0.186.0`) Dual-Surface Curved CRT, Low-Angle Camera & Near-Field Depth-of-Field:**
* Uses `WebGPURenderer` (`import * as THREE from 'three/webgpu'`) with WebGL2 backend support and **TSL (Three.js Shading Language)** (`import { ... } from 'three/tsl'`) plus `THREE.RenderPipeline`.
* **Inner Surface (Curved Phosphor CRT Mesh):** A high-subdivision bowed 3D surface (`PlaneGeometry(W, H, 128, 128)` displaced along $-Z$ toward the edges) mapped with a live `THREE.CanvasTexture` driven by xterm’s WebGL2 screen canvas, shaded with phosphor sub-pixel aperture grille, scanline modulation, and dynamic phosphor bloom.
* **Outer Surface (Curved Refractive & Reflective Front Glass Plate):** A second, slightly flatter curved 3D glass mesh positioned in front of the phosphor screen ($\Delta Z = +0.055$), using `THREE.MeshPhysicalNodeMaterial` with volumetric transmission (`transmission: 1.0`), index of refraction (`ior: 1.48`), chromatic dispersion (`dispersion: 1.6`), and a stylized multi-tap blurred internal back-reflection of the terminal text.
* **Stylish Low-Angle Bottom-Right Camera Pose:** The `PerspectiveCamera` views the curved CRT assembly from a **subtle bottom-right low angle**—reminiscent of stylish low-angle car posters—so the lower-right quadrant of the curved screen sits physically closer to the camera while the screen remains comfortably readable edge-to-edge.
* **Subtle Depth-of-Field (Near-Field Soft Focus on Bottom-Right):** A depth-aware post-processing pass in `THREE.RenderPipeline` keeps the focal plane locked across the main body of the screen so almost all terminal text stays razor-sharp, while applying a **very subtle near-field out-of-focus blur to the closer bottom-right region** of the curved screen.

4. **Zero-Flourish Direct-to-DOM Non-GPU Fallback (Super-Demo Only):**
* If GPU/WebGL2 initialization fails or the WebGL context is lost and not restored, the super-demo falls back to direct-to-DOM rendering in xterm.js with no flourish.
* Because calling `webglAddon.dispose()` in xterm.js 6.0.0 automatically restores the core `DomRenderer` (`renderService.setRenderer(_core._createRenderer())`) on the live `Terminal` instance, fallback disposes the 3D stage and `WebglAddon` and makes `#xterm-offscreen-host` visible within the selected parent, preserving the running guest, scrollback buffer, and `term.onData` input stream without interruption. This becomes full-page only when Kabibi created a full-screen host.

5. **Phase 2 (Paused): Floating DOM Blue Panels HUD (`z-index: 20`):**
* The Floating DOM Blue Panels HUD (§6–§7 below) is explicitly **paused for the current CRT phase** and reserved for Phase 2 once the super-demo off-screen xterm surface, low-angle curved CRT, near-field depth-of-field, raycast mouse forwarding, and direct-to-DOM fallback are proven.





---

## 3. Super-Demo Technology Stack & Version Pinning

| Package | Version | Import / Entrypoint | Role in Kabibi Browser Host |
| --- | --- | --- | --- |
| **`three`** | **`0.186.0` (r186)** | `three/webgpu` & `three/tsl` | `WebGPURenderer` (with auto WebGL2 fallback), `MeshPhysicalNodeMaterial`, `RenderPipeline`, and TSL shader graph nodes. |
| **`@xterm/xterm`** | **`6.0.0`** | `@xterm/xterm` | Terminal emulator; DOM rendering is the default, with the WebGL renderer used only by super-demo. |
| **`@xterm/addon-webgl`** | **`0.19.0`** | `@xterm/addon-webgl` | Super-demo GPU renderer whose screen canvas is sampled by Three.js `CanvasTexture`. |
| **`@xterm/addon-fit`** | **`0.11.0`** | `@xterm/addon-fit` | Computes `{ cols, rows }` from the selected parent’s available bounds and font metrics. |
| **`@xterm/addon-unicode-graphemes`** | **`0.4.0`** | `@xterm/addon-unicode-graphemes` | Registers and activates `'15-graphemes'` UAX #29 grapheme cluster segmentation (`allowProposedApi: true`). |

*Import map for the dedicated super-demo page (or super-demo-only entrypoint):*

```html
<script type="importmap">
{
  "imports": {
    "three": "https://cdn.jsdelivr.net/npm/three@0.186.0/build/three.webgpu.js",
    "three/webgpu": "https://cdn.jsdelivr.net/npm/three@0.186.0/build/three.webgpu.js",
    "three/tsl": "https://cdn.jsdelivr.net/npm/three@0.186.0/build/three.tsl.js",
    "three/addons/": "https://cdn.jsdelivr.net/npm/three@0.186.0/examples/jsm/",
    "@xterm/xterm": "https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/+esm",
    "@xterm/addon-webgl": "https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@0.19.0/+esm",
    "@xterm/addon-fit": "https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.11.0/+esm",
    "@xterm/addon-unicode-graphemes": "https://cdn.jsdelivr.net/npm/@xterm/addon-unicode-graphemes@0.4.0/+esm"
  }
}
</script>
```

---

## 4. Super-Demo Layer Cake, Off-Screen `@xterm/xterm` Internals & Non-GPU Fallback

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Selected Super-Demo Parent (viewport-sized only if Kabibi created it)        │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │ Layer 3 (z-index: 20) [PHASE 2 — PAUSED]:                              │  │
│  │  • Floating DOM Blue Panels HUD (#kabibi-dom-hud)                      │  │
│  │  • Omitted in Phase 1 (Blue Panels render natively inside Layer 1/2)   │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │ Layer 2 (z-index: 10): Three.js r186 Canvas (#crt-stage)               │  │
│  │  • Camera: Subtle bottom-right low angle (stylish car-poster framing)  │  │
│  │  • Outer Mesh: Curved Glass Plate (IOR 1.48, Refraction, Dispersion,   │  │
│  │    Stylized Blurred Back-Reflection of Phosphor Text)                  │  │
│  │  • Inner Mesh: Bowed CRT Phosphor Screen (xterm CanvasTexture,         │  │
│  │    Scanlines, Aperture Grille, RenderPipeline Bloom + Near-Field DoF)  │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │ Layer 1 (z-index: 0, opacity: 0, inset: 0): Off-Screen @xterm/xterm    │  │
│  │  • Selected-parent layout so IntersectionObserver & ResizeObserver run│  │
│  │  • Receives raw ANSI diffs from ui.Host (ToANSI) and keyboard focus    │  │
│  │  • Direct-to-DOM Fallback: switches to opacity: 1, z-index: 10 if      │  │
│  │    GPU/WebGL2 is unavailable or lost, disposing WebglAddon & Layer 2   │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 4.1 `@xterm/xterm` (v6.0.0) & `@xterm/addon-webgl` Source Audit for Off-Screen Rendering

Direct inspection of the pinned `@xterm/xterm@6.0.0` source (`addons/addon-webgl/src/WebglAddon.ts`, `addons/addon-webgl/src/WebglRenderer.ts`, `addons/addon-webgl/src/DevicePixelObserver.ts`, and `src/browser/services/RenderService.ts`) establishes four implementation rules for capturing the off-screen terminal surface—and shows *exactly* what to vendor/patch if we want a clean first-class API:

1. **`new WebglAddon(true)` Native `preserveDrawingBuffer` Support (`WebglAddon.ts` / `WebglRenderer.ts`):**
   * `WebglAddon`’s constructor is `constructor(private _preserveDrawingBuffer?: boolean)` and forwards `preserveDrawingBuffer` directly to `this._canvas.getContext('webgl2', { antialias: false, depth: false, preserveDrawingBuffer })` inside `WebglRenderer`.
   * Instantiating `new WebglAddon(true)` preserves the WebGL2 drawing buffer natively across frames without needing a global `HTMLCanvasElement.prototype.getContext` monkey-patch.

2. **Screen Framebuffer Canvas vs. `textureAtlas` (`WebglRenderer.ts`):**
   * The public `webglAddon.textureAtlas` getter returns `this._charAtlas?.pages[0].canvas`, which is only the **2D glyph sprite sheet**, *not* the rendered terminal screen.
   * The actual rendered terminal framebuffer is `WebglRenderer._canvas` (`private _canvas: HTMLCanvasElement`), appended to `this._core.screenElement!` (`div.xterm-screen`) and sized to `this.dimensions.device.canvas.{width,height}` on resize and DPI changes.
   * **Access / Vendored Patch:** We can either read `(webglAddon as any)._renderer?._canvas` (or `term.element.querySelector('.xterm-screen canvas')`), or vendor/patch `@xterm/addon-webgl` to expose a public `get renderCanvas(): HTMLCanvasElement | undefined { return this._renderer?.canvas; }` getter on `WebglAddon`.

3. **Catching All Frame Redraws (`RenderService.ts` vs. `Terminal.onRender`):**
   * In `src/browser/CoreBrowserTerminal.ts`, public `term.onRender` only forwards `this._renderService.onRenderedViewportChange`.
   * In `src/browser/services/RenderService.ts` (`_renderRows`), `_onRenderedViewportChange` is skipped whenever `_isNextRenderRedrawOnly` is `true`—which is the case for **cursor blink frames** (`_requestRedrawCursor`), **mouse text selection highlights** (`handleSelectionChanged`), and **focus/blur cursor style updates** (`refreshRows(..., true)`).
   * However, `RenderService._onRender` fires unconditionally at the end of every `_renderRows` WebGL draw pass.
   * **Zero-Missed-Frame Hook:** Subscribe to `(term as any)._core._renderService.onRender(() => { xtermTexture.needsUpdate = true; })` (or expose `onDidRenderFrame` in our vendored xterm/addon patch) so Three.js uploads `xtermTexture` on **both** ANSI content writes and redraw-only frames (cursor blink and selection drag).

4. **Keeping `IntersectionObserver` & `ResizeObserver` Active (`RenderService.ts` & `DevicePixelObserver.ts`):**
   * `RenderService._registerIntersectionObserver` observes `screenElement` with `new IntersectionObserver(..., { threshold: 0 })`. If `#xterm-offscreen-host` is placed outside the viewport (`left: -9999px`), set to `display: none`, or collapsed to `0×0`, `entry.isIntersecting` becomes `false`, setting `this._isPaused = true` and **silently halting all `refreshRows` and `handleResize` calls**.
   * Similarly, `observeDevicePixelDimensions` in `DevicePixelObserver.ts` observes `_canvas` with `ResizeObserver` (`box: ['device-pixel-content-box']`) and ignores `0×0` sizes.
   * **Layout Rule (Super-Demo Only):** `#xterm-offscreen-host` **must** remain laid out, nonzero-sized, and intersecting within the selected parent (`position: absolute; inset: 0; width: 100%; height: 100%; opacity: 0; pointer-events: none`). Use a full-viewport fixed host only when Kabibi created the parent itself. Because `opacity: 0` elements retain layout boxes, xterm’s `IntersectionObserver`, `ResizeObserver`, and `FitAddon.fit()` continue to operate; never hide this host with `display: none` or move it off-screen.

### 4.2 Direct-to-DOM Non-GPU Fallback (Zero Flourish, Zero State Loss)

Look at `WebglAddon.activate` in `addons/addon-webgl/src/WebglAddon.ts`:

```ts
this._register(toDisposable(() => {
  if ((this._terminal as any)._core._store._isDisposed) {
    return;
  }
  const renderService: IRenderService = (this._terminal as any)._core._renderService;
  renderService.setRenderer((this._terminal as any)._core._createRenderer());
  renderService.handleResize(terminal.cols, terminal.rows);
}));
```

When `webglAddon.dispose()` is called, xterm.js automatically creates its built-in `DomRenderer` (`_core._createRenderer()`), attaches it to the existing `RenderService`, and triggers a full resize and redraw on the **same `Terminal` instance**.

Therefore, our non-GPU fallback path is completely seamless:

1. **When Fallback Triggers:**
   * `WebglAddon` throws during WebGL2 context creation, OR
   * Three.js `WebGPURenderer` (with WebGL2 backend) fails to initialize or detects a software/unsupported GPU, OR
   * `webglAddon.onContextLoss` fires after its 3000ms restoration timeout (`WebglRenderer.ts`).
2. **Fallback Transition (`activateDirectDomFallback()`):**
   * Call `webglAddon?.dispose()` (restoring xterm’s `DomRenderer` in-place) and tear down the Three.js `#crt-stage` canvas.
   * Switch `#xterm-offscreen-host` to direct visibility within its parent (`opacity: 1; pointer-events: auto; z-index: 10`). This fills the viewport only when the selected parent is Kabibi’s auto-created full-screen host.
   * Call `fitAddon.fit()` and `term.focus()`.
   * The running `washmhost` guest, `term.onData` keyboard stream, scrollback buffer, and resize handler continue uninterrupted in a crisp direct-to-DOM terminal with no flourish.

---

## 5. Three.js r186 3D Scene, Low-Angle Camera, Curved Geometry & TSL Shaders

### 5.1 Stylish Low-Angle Bottom-Right Camera Pose & Aspect-Locked Mesh Sizing

Instead of viewing the screen dead-on from `(0, 0, D)`, the camera is positioned at a **subtle bottom-right low angle**—like a stylish low-angle car poster—so you feel the 3D sculptural bow of the CRT faceplate and the closer lower-right corner without sacrificing terminal readability:

* **Camera Pose (Subtle Bottom-Right Low Angle):**
  * Use `THREE.PerspectiveCamera(fov = 30, aspect = window.innerWidth / window.innerHeight, 0.1, 100)`.
  * Place the camera slightly to the **right (`+X`)** and **below (`-Y`)** center, looking back toward the screen center:
    $$\mathbf{P}_{\text{cam}} = (x = +0.36,\; y = -0.24,\; z = 2.68), \qquad \mathbf{P}_{\text{target}} = (x = +0.03,\; y = -0.02,\; z = 0.0)$$
  * Optional subtle mouse parallax ($\pm 0.04$ world units around $\mathbf{P}_{\text{cam}}$) shifts the glass specular glint and internal reflection gently as the pointer moves.
* **Aspect-Locked Viewport Framing:**
  * At focal distance $D = \|\mathbf{P}_{\text{cam}} - \mathbf{P}_{\text{target}}\| \approx 2.71$, the visible frustum dimensions at the target plane are:
    $$H_{\text{world}} = 2 \cdot D \cdot \tan\left(\frac{\text{fov}}{2}\right), \qquad W_{\text{world}} = H_{\text{world}} \cdot \text{aspect}$$
  * On window resize, update the mesh scale to `(W_world * 0.94, H_world * 0.94, 1)` so the angled curved screen fills the viewport cleanly without clipping its closer bottom-right corner or receding top-left corner.

### 5.2 Mathematical Surface Bending (Phosphor Mesh vs. Front Glass Plate)

Rather than a 2D post-processing UV warp (which cannot produce 3D perspective foreshortening from the low-angle camera or varying glass thickness), we bend **real 3D vertex geometry**:

* Given normalized plane coordinates $(u, v) \in [-1, 1]^2$:

$$r^2 = u^2 + v^2, \qquad z_{\text{bend}}(u, v) = -k_{\text{curve}} \cdot \left(u^2 + v^2 + 0.15 \cdot u^2 v^2\right)$$

* **Mesh 1 — Inner Phosphor Screen (`z_base = 0.0`, $k_{\text{curve}} = 0.14$):**
  * Bowed backward toward the edges and corners, bulging softly toward the viewer at $(0,0)$.
  * Normals are recomputed analytically (`geometry.computeVertexNormals()`) so lighting and reflections curve naturally across the surface.
* **Mesh 2 — Outer Refractive Glass Plate (`z_base = +0.055`, $k_{\text{curve}} = 0.085$):**
  * Positioned $0.055$ world units in front of the phosphor screen with a slightly shallower curvature ($k_{\text{curve}} = 0.085$).
  * **Differential Curvature Gap:** Because $k_{\text{glass}} < k_{\text{phosphor}}$, the optical gap $\Delta Z(u,v) = 0.055 + (k_{\text{phosphor}} - k_{\text{glass}})(u^2 + v^2 + 0.15 u^2 v^2)$ between the phosphor and the front glass is thinner in the center ($0.055$), widens to $0.11$ at the edge midpoints ($\pm 1, 0$), and reaches $\approx 0.173$ at the corners ($\pm 1, \pm 1$). Combined with the bottom-right low-angle camera, this gives the nearer bottom-right glass edge tangible depth, refraction, and internal reflection.

### 5.3 TSL Shader Graph for the Inner Phosphor Screen (`MeshBasicNodeMaterial`)

Using `three/tsl` in r186 (note the portable increasing-edge `smoothstep(0.75, 1.35, ...)` inverted via `float(1.0).sub(...)` so WebGL2 GLSL and WebGPU WGSL both evaluate it identically):

```js
import * as THREE from 'three/webgpu';
import {
  texture, uv, vec2, vec3, float, sin, mix, smoothstep, uniform
} from 'three/tsl';

const crtResolution = uniform(new THREE.Vector2(canvas.width, canvas.height));
const scanlineIntensity = uniform(0.16);

// 1. Base terminal texture sample + subtle 1px phosphor halation (horizontal bleed)
const vUv = uv();
const texel = vec2(1.0).div(crtResolution);
const centerSample = texture(xtermTexture, vUv);
const leftSample   = texture(xtermTexture, vUv.sub(vec2(texel.x, 0.0)));
const rightSample  = texture(xtermTexture, vUv.add(vec2(texel.x, 0.0)));
const phosphorCol  = centerSample.mul(0.76).add(leftSample.add(rightSample).mul(0.12));

// 2. Scanlines locked to physical terminal pixel rows
const scanWave = sin(vUv.y.mul(crtResolution.y).mul(Math.PI)).mul(0.5).add(0.5);
const scanMask = mix(float(1.0).sub(scanlineIntensity), float(1.0), scanWave);

// 3. Aperture grille (RGB subpixel triad modulation)
const triadPhase = vUv.x.mul(crtResolution.x).mul(Math.PI * 2.0 / 3.0);
const grille = vec3(
  sin(triadPhase).mul(0.04).add(0.96),
  sin(triadPhase.add(2.094)).mul(0.04).add(0.96),
  sin(triadPhase.add(4.188)).mul(0.04).add(0.96)
);

// 4. Corner shadow (tube mask vignette, using increasing smoothstep bounds)
const edgeDist = vUv.sub(0.5).mul(2.0);
const vignette = float(1.0).sub(smoothstep(0.75, 1.35, edgeDist.dot(edgeDist)));

const phosphorMaterial = new THREE.MeshBasicNodeMaterial();
phosphorMaterial.colorNode = phosphorCol.rgb.mul(scanMask).mul(grille).mul(vignette);
```

### 5.4 TSL Shader Graph for the Outer Glass Plate (Refraction + Stylized Blurry Internal Reflection)

For the front glass sheet (`Mesh 2`), we combine screen-space refraction with a stylized blurry internal reflection of the phosphor text:

1. **Refraction & Chromatic Dispersion:**
   * `MeshPhysicalNodeMaterial` in Three.js r186 provides screen-space transmissive refraction via `transmission: 1.0`, `ior: 1.48`, `thickness: 0.42`, and `dispersion: 1.6`.
   * Because Three.js renders transmissive objects in a separate pass after opaque meshes, the glass mesh samples the rendered inner phosphor screen behind it, bending rays along the curved glass normals.
2. **Stylized Blurry Back-Reflection (Internal Ghosting + Overhead Studio Glint):**
   * To evoke bright phosphor characters reflecting off the inside of the curved faceplate and bouncing back toward the low-angle camera as a soft ghost image, we construct a custom `emissiveNode` on the glass material that samples a **9-tap tent-blurred neighborhood** of `xtermTexture` at a view-dependent parallax offset $\Delta UV = \text{reflect}(-\text{viewDir}, \text{normal}).xy \times 0.014$:

```js
import {
  texture, uv, normalView, positionViewDirection, reflect, vec2, vec3, float, pow, dot, max
} from 'three/tsl';

const vUv = uv();
const reflDir = reflect(positionViewDirection.negate(), normalView);
const parallaxOffset = reflDir.xy.mul(0.014); // Shifted by low-angle view & curved glass
const reflUv = vUv.add(parallaxOffset);

// 9-tap tent blur of the terminal texture for the blurry glass back-reflection
const blurRadius = vec2(0.0035, 0.0055);
const blurredReflection =
  texture(xtermTexture, reflUv).mul(0.25)
  .add(texture(xtermTexture, reflUv.add(vec2( blurRadius.x,  0.0))).mul(0.125))
  .add(texture(xtermTexture, reflUv.add(vec2(-blurRadius.x,  0.0))).mul(0.125))
  .add(texture(xtermTexture, reflUv.add(vec2( 0.0,  blurRadius.y))).mul(0.125))
  .add(texture(xtermTexture, reflUv.add(vec2( 0.0, -blurRadius.y))).mul(0.125))
  .add(texture(xtermTexture, reflUv.add(vec2( blurRadius.x,  blurRadius.y))).mul(0.0625))
  .add(texture(xtermTexture, reflUv.add(vec2(-blurRadius.x,  blurRadius.y))).mul(0.0625))
  .add(texture(xtermTexture, reflUv.add(vec2( blurRadius.x, -blurRadius.y))).mul(0.0625))
  .add(texture(xtermTexture, reflUv.add(vec2(-blurRadius.x, -blurRadius.y))).mul(0.0625));

// Schlick Fresnel term so reflection is subtle near normal incidence (~3%) and stronger on grazing curves (~18%)
const cosTheta = max(dot(normalView, positionViewDirection), 0.0);
const fresnel = float(0.03).add(float(0.18).mul(pow(float(1.0).sub(cosTheta), 3.5)));

// Subtle diagonal overhead softbox reflection across the curved glass
const studioGlint = pow(max(dot(normalView, vec3(-0.35, 0.55, 0.75)), 0.0), 16.0).mul(0.035);

const glassMaterial = new THREE.MeshPhysicalNodeMaterial({
  transmission: 1.0,
  ior: 1.48,
  thickness: 0.42,
  roughness: 0.06,
  dispersion: 1.6,
  clearcoat: 1.0,
  clearcoatRoughness: 0.04,
  attenuationColor: new THREE.Color('#eef7ff'),
  attenuationDistance: 4.0,
});
glassMaterial.emissiveNode = blurredReflection.rgb.mul(fresnel).add(vec3(studioGlint));
```

### 5.5 Post-Processing (`THREE.RenderPipeline`): Phosphor Bloom + Subtle Near-Field Depth-of-Field (Bottom-Right Soft Focus)

Because the camera views the curved screen from a **slightly bottom-right low angle** ($\mathbf{P}_{\text{cam}} = (+0.36, -0.24, 2.68)$), the 3D distance from the camera lens varies continuously across the screen surface:

* The **center, upper-left, upper-right, and bottom-left** regions sit near or slightly beyond the focal plane ($d_{\text{view}} \in [2.62, 2.95]$) and must remain **100% sharp**.
* The **bottom-right region** of the curved screen is physically closer to the camera lens ($d_{\text{view}} \in [2.35, 2.58]$).
* Using a depth-aware near-field Circle-of-Confusion ($\text{CoC}$) factor in `THREE.RenderPipeline`, we keep the entire main working area crisp while applying a **very subtle macro-lens out-of-focus blur ($\le 1.8\text{px}$ radius)** exclusively to that closer bottom-right zone:

```js
import { pass, bloom, uv, vec2, float, smoothstep, mix } from 'three/tsl';

const renderPipeline = new THREE.RenderPipeline(renderer);
const scenePass = pass(scene, camera);
const sceneColor = scenePass.getTextureNode('output');
const viewZNode  = scenePass.getViewZNode(); // Positive camera-space distance along -Z

// 1. Subtle phosphor bloom (threshold 0.65, strength 0.20, radius 0.32)
const bloomPass = bloom(sceneColor, 0.20, 0.32, 0.65);
const litScene  = sceneColor.add(bloomPass);

// 2. Asymmetric Near-Field Depth-of-Field (Bottom-Right Closer to Camera)
// Fragments with viewZ >= 2.58 (center, top, left) have nearCoC = 0.0 (razor-sharp).
// Fragments closer than 2.58 (the nearer bottom-right quadrant) ramp smoothly up to nearCoC = 1.0.
const nearCoC = float(1.0).sub(smoothstep(2.36, 2.58, viewZNode.negate()));

// Subtle 9-tap disc/tent kernel scaled by nearCoC (max ~1.6px softening in bottom-right)
const vUv = uv();
const dofStep = vec2(1.5 / window.innerWidth, 1.5 / window.innerHeight).mul(nearCoC);
const nearBlurred =
  sceneColor.sample(vUv).mul(0.28)
  .add(sceneColor.sample(vUv.add(vec2( dofStep.x,  0.0))).mul(0.12))
  .add(sceneColor.sample(vUv.add(vec2(-dofStep.x,  0.0))).mul(0.12))
  .add(sceneColor.sample(vUv.add(vec2( 0.0,  dofStep.y))).mul(0.12))
  .add(sceneColor.sample(vUv.add(vec2( 0.0, -dofStep.y))).mul(0.12))
  .add(sceneColor.sample(vUv.add(vec2( dofStep.x * 0.7,  dofStep.y * 0.7))).mul(0.06))
  .add(sceneColor.sample(vUv.add(vec2(-dofStep.x * 0.7,  dofStep.y * 0.7))).mul(0.06))
  .add(sceneColor.sample(vUv.add(vec2( dofStep.x * 0.7, -dofStep.y * 0.7))).mul(0.06))
  .add(sceneColor.sample(vUv.add(vec2(-dofStep.x * 0.7, -dofStep.y * 0.7))).mul(0.06))
  .add(bloomPass);

renderPipeline.outputNode = mix(litScene, nearBlurred, nearCoC.mul(0.65));
```

### 5.6 Off-Screen Keyboard & Exact Raycast Mouse Coordinate Inversion

In the current root `index.html`, keyboard (`term.onData` $\rightarrow$ `hostState.pushStdin`) and terminal resize (`FitAddon` $\rightarrow$ `SIGWINCH`) already work with `washmhost`. Adapting input to the full-screen 3D CRT requires respecting how `@xterm/xterm@6.0.0` binds mouse and keyboard listeners internally (`src/browser/CoreBrowserTerminal.ts`, `src/browser/services/SelectionService.ts`, and `src/browser/input/Mouse.ts`):

1. **Keyboard Focus Preservation (`.xterm-helper-textarea`):**
   * `CoreBrowserTerminal._bindKeys()` attaches all `keydown`, `keypress`, `keyup`, `input`, `composition*`, and `paste` listeners to `this.textarea` (`.xterm-helper-textarea`), and `copy`/`paste` to `this.element`.
   * Because `#xterm-offscreen-host` stays in the live viewport with `opacity: 0; pointer-events: none`, calling `term.focus()` on startup and on any pointer interaction with `#crt-stage` keeps `.xterm-helper-textarea` focused so all keyboard shortcuts (`Ctrl+O`, `Tab`, arrows, function keys), IME composition, and clipboard copy/paste work identically to direct DOM mode.

2. **How xterm.js Computes Mouse Cell Coordinates (`Mouse.ts` & `MouseService.ts`):**
   * Both `SelectionService` (text selection) and `CoreBrowserTerminal.bindMouse()` (VT mouse tracking sequences when enabled by TUI apps) call `getCoordsRelativeToElement(window, event, this.screenElement)`:
     ```ts
     const rect = element.getBoundingClientRect();
     return [
       event.clientX - rect.left - leftPadding,
       event.clientY - rect.top - topPadding
     ];
     ```
   * Crucially, when `mousedown` fires on `term.element`, **both** `SelectionService._addMouseDownListeners()` and `CoreBrowserTerminal.bindMouse()` attach their drag (`mousemove`) and release (`mouseup`) listeners directly to **`document`**, not `term.element`!
   * If a raw browser `mousemove` or `mouseup` event on `#crt-stage` is allowed to bubble up to `document`, xterm’s `document` listeners will process the **unwarped** `(e.clientX, e.clientY)` coordinates before or alongside any synthetic event.

3. **3D Raycast Inversion & Event Forwarding Protocol:**
   * Listen to `mousedown`, `mousemove`, `mouseup`, `wheel`, `dblclick`, and `contextmenu` on `#crt-stage` (and capture pointer during drag).
   * For any native mouse event on `#crt-stage`, call `e.stopPropagation()` so raw unwarped `mousemove`/`mouseup` events do not reach xterm’s `document` drag listeners directly.
   * Convert `(e.clientX, e.clientY)` to Normalized Device Coordinates $\text{NDC} \in [-1, 1]^2$ and run `raycaster.setFromCamera(ndc, camera)` against `phosphorMesh` (accounting for both the low-angle camera pose and the 3D surface curvature).
   * Read `hit.uv` $(u, v) \in [0, 1]^2$ from the mesh intersection and map it into `term._core.screenElement.getBoundingClientRect()`:
     $$\text{clientX}_{\text{mapped}} = \text{rect.left} + u \cdot \text{rect.width}, \qquad \text{clientY}_{\text{mapped}} = \text{rect.top} + (1 - v) \cdot \text{rect.height}$$
   * Dispatch a synthetic `MouseEvent` / `WheelEvent` with `(clientX: clientX_mapped, clientY: clientY_mapped, bubbles: true, cancelable: true, button, buttons, detail, shiftKey, ctrlKey, altKey, metaKey)` onto `term.element`. Because `bubbles: true` is set on the synthetic event (while the raw `#crt-stage` event was stopped), both `term.element` listeners (`mousedown`, `wheel`) and `document` listeners (`mousemove`, `mouseup` during selection or VT mouse drag) receive the exact raycast-corrected coordinates.

---

## 6. Phase 2 (Paused / Deferred): The Floating DOM Blue Panels HUD

> **Phase Boundary Note:** Everything in **§6 and §7** is **paused for the current super-demo phase** and deferred to **Phase 2**. During the super-demo phase, Kabibi’s Dual Blue File Manager Panels (`Ctrl+O`) render inside the terminal via standard VT/ANSI output with zero DOM HUD overlay and zero changes to Go’s `AppWidget.Render`.

When Phase 2 is activated later, pressing `Ctrl+O` in browser mode will summon the dual-pane file manager in two coordinated layers:

### 6.1 How the Terminal & DOM HUD Coexist (Phase 2 Design)

Look at how this pairs with the **Unified `CellBuf` & Blue Panel Illusion (`3-blue.md`)**:

1. **In Go (`kabibi/ui/app/app_widget.go`):**
* When `Ctrl+O` toggles `w.panelsVisible = true`, `AppWidget` still executes the exact height calculation:

$$H_{\text{panels}} = H_{\text{term}} - H_{\text{gap}}$$

and sets `w.sh.SetBottomAligned(true)`.

* **Browser Mode Hook (Phase 2):** When `syscall.GetPlatformInfo().OSName == "browser"` and the Phase 2 DOM HUD is enabled:
* Instead of blitting the ASCII `DualPaneWidget` cells over rows `0 .. H_panels-1` of the terminal `CellBuf` (which would cause the blue panels to be drawn *twice*—once curved inside the CRT and once flat in the DOM), `AppWidget.Render` leaves rows `0 .. H_panels-1` showing the **unmasked shell block history** (dimmed by 40%) while emitting the structured panel state to the browser DOM via the `eval` bridge.
* Because the DOM blue panels have a semi-translucent deep-blue acrylic backdrop (`background: rgba(0, 10, 95, 0.86); backdrop-filter: blur(8px)`) and hang down from the top of the screen over rows `0 .. H_panels-1`, you faintly see the curved CRT terminal output sliding upward behind the frosted blue DOM panels as commands run in the bottom gap.
* When the user presses `Ctrl+O` again (`panelsVisible = false`), the DOM panels animate smoothly back up (`translateY(-105%)`), revealing the curved CRT shell underneath at full brightness.
* **Note on Low-Angle Camera Alignment in Phase 2:** Because Phase 1 positions the camera at a subtle bottom-right low angle, the projected 2D screen boundary of row $H_{\text{panels}}$ on the curved mesh is slightly perspective-tilted rather than a flat horizontal line. In Phase 2, `#kabibi-dom-hud` can either project the 3D coordinates of row $H_{\text{panels}}$ through `camera.project(...)` or apply a matching CSS 3D `perspective` / `matrix3d` transform so the HUD remains crisp while respecting the low-angle framing.





### 6.2 DOM Structure & CSS Motion Architecture

```html
<div id="kabibi-dom-hud" class="hud-hidden" aria-hidden="true">
  <div class="hud-bezel-shadow"></div>
  <div class="hud-dual-grid">
    <!-- Left Pane -->
    <section class="hud-pane" id="hud-pane-left" data-active="false">
      <header class="hud-pane-header">
        <span class="hud-path-badge">/home/user/src</span>
        <span class="hud-sort-badge">Name ▲</span>
      </header>
      <div class="hud-columns-viewport">
        <!-- Smoothly gliding selection cursor pill -->
        <div class="hud-selection-pill" id="hud-cursor-left"></div>
        <div class="hud-columns-grid" id="hud-grid-left"></div>
      </div>
      <footer class="hud-pane-footer" id="hud-footer-left"></footer>
    </section>

    <!-- Right Pane -->
    <section class="hud-pane" id="hud-pane-right" data-active="true">
      <header class="hud-pane-header">
        <span class="hud-path-badge">~/rustica</span>
        <span class="hud-sort-badge">Name ▲</span>
      </header>
      <div class="hud-columns-viewport">
        <div class="hud-selection-pill" id="hud-cursor-right"></div>
        <div class="hud-columns-grid" id="hud-grid-right"></div>
      </div>
      <footer class="hud-pane-footer" id="hud-footer-right"></footer>
    </section>
  </div>
</div>

```

### 6.3 The Three Visual Flourishes of the DOM Panels

1. **Top-Drawer Spring Animation (`Ctrl+O`):**
* The height of `#kabibi-dom-hud` is dynamically synchronized to the exact pixel row where `H_panels` ends on the CRT screen:

$$\text{hudHeightPx} = \text{crtScreenTopPx} + \left(\frac{H_{\text{panels}}}{H_{\text{term}}}\right) \times \text{crtScreenHeightPx}$$


* Opening (`Ctrl+O` ON):
```css
#kabibi-dom-hud {
  position: fixed;
  top: 0; left: 2.2vw; right: 2.2vw;
  height: var(--hud-height-px);
  z-index: 20;
  transform: translate3d(0, -104%, 0) scaleY(0.96);
  opacity: 0;
  transform-origin: top center;
  transition:
    transform 340ms cubic-bezier(0.16, 1, 0.3, 1),
    opacity 180ms ease-out;
  will-change: transform, opacity;
}
#kabibi-dom-hud.hud-visible {
  transform: translate3d(0, 0, 0) scaleY(1);
  opacity: 1;
}

```




2. **Gliding Selection Cursor Highlight (Zero Jump):**
* Instead of toggling a CSS `.selected` background class on static `<div>` rows (which causes an instantaneous, jarring jump), each pane owns a single floating `<div class="hud-selection-pill">` positioned at `top: 0; left: 0`.
* Every file item `<div class="hud-file-item" data-index="i">` is laid out in a multi-column CSS grid matching `DualPaneWidget`’s pagination (`numCols = max(1, innerW / 18)`, `itemsPerCol = innerH`).


* When the selected index changes (via `Up`, `Down`, `Left`, `Right`, `PgUp`, `PgDn`, `Home`, `End`, or mouse click), JS reads `targetEl.offsetLeft` and `targetEl.offsetTop` (or calculates `(col * colWidth, row * rowHeight)` in $O(1)$ arithmetic) and updates:
```css
.hud-selection-pill {
  position: absolute;
  width: var(--col-width);
  height: var(--row-height);
  background: linear-gradient(90deg, #00c8dc 0%, #00a8c8 100%);
  box-shadow: 0 0 14px rgba(0, 210, 235, 0.45), inset 0 1px 0 rgba(255, 255, 255, 0.4);
  border-radius: 3px;
  transform: translate3d(var(--cursor-x), var(--cursor-y), 0);
  transition:
    transform 95ms cubic-bezier(0.22, 1, 0.36, 1),
    width 95ms ease;
  pointer-events: none;
  z-index: 1;
}

```


* When moving between files (or jumping across columns with `Left`/`Right`), the cyan bar **glides** smoothly across the blue panel while the text of the active item inverts cleanly to `#000040` bold.


3. **Distinct Crisp Vector SVG File Icons:**
* A zero-dependency inline SVG icon registry maps each entry in `FilePaneWidget` to a crisp $14\times14\text{px}$ vector icon rendered beside the filename:
* `..` (Parent Dir): Up-level return arrow (`#ffd75f`)
* Directory (`isDir`): Classic dual-tone folder glyph (`#ffffff` / `#87d7ff`)
* Executable (`isExecutable`, `*`): Terminal lightning/gear badge in bright emerald (`#00ff87`)


* `.go`: Gopher/Go cyan hexagon badge (`#00add8`)
* `.rs` / `.toml`: Rust gear badge (`#dea584`)
* `.js` / `.ts` / `.json`: Amber/blue code brackets (`#f7df1e` / `#3178c6`)
* `.md` / `.txt`: Document lines icon (`#d0d0d0`)
* `.bat` / `.wasm`: Mohabbat 🍆 / WASM cube icon


* Marked item (`•`, via `Insert` or `+`/`-` glob): Glowing gold check/dot indicator + `#ffff55` filename tint.







---

## 7. Phase 2 (Paused / Deferred): Go $\leftrightarrow$ Browser State Synchronization Over the `eval` Bridge

> **Phase Boundary Note:** This synchronization bridge is only required for the **Phase 2 Floating DOM Blue Panels HUD**. Phase 1 requires **no changes** to Go’s `AppWidget` or `filemanager` packages, as all terminal UI (including `Ctrl+O` Blue Panels) renders through the standard VT/ANSI stream into the off-screen `@xterm/xterm` surface.

Because Kabibi already has the pure `eval` virtual dylib (`syscall.DylibOpen("eval", 0)` / `EvalJS`) in `mohabbat/washmhost/js/browser/index.js`, Phase 2 will not need any new WASM ABI imports or custom host syscalls.

### 7.1 Go Side: `kabibi/ui/filemanager/dom_bridge.go` (Phase 2)

1. Detect browser mode once at startup via `syscall.GetPlatformInfo().OSName == "browser"`.
2. Define a compact JSON state payload representing `DualPaneWidget` + modal dialogs:

```go
type DOMHUDState struct {
    Visible    bool          `json:"visible"`
    ActivePane int           `json:"activePane"` // 0 = Left, 1 = Right
    PanelsH    int           `json:"panelsH"`    // Rows occupied by panels
    TermH      int           `json:"termH"`      // Total terminal rows
    Left       DOMPaneState  `json:"left"`
    Right      DOMPaneState  `json:"right"`
    Modal      *DOMModalState `json:"modal,omitempty"` // F2/F5/F6/F7/F8/+/- dialogs
}

type DOMPaneState struct {
    Dir        string        `json:"dir"`
    Cursor     int           `json:"cursor"`
    ScrollCol  int           `json:"scrollCol"`
    NumCols    int           `json:"numCols"`
    RowsPerCol int           `json:"rowsPerCol"`
    Items      []DOMFileItem `json:"items"`
    StatusText string        `json:"statusText"`
}

type DOMFileItem struct {
    Name   string `json:"name"`
    Size   int64  `json:"size"`
    IsDir  bool   `json:"isDir"`
    IsExec bool   `json:"isExec"`
    Marked bool   `json:"marked"`
}
```

3. **Change-Driven Delta Push (Phase 2):**
* Whenever `AppWidget.Render` runs in browser mode (triggered by UI state invalidations) and the `DOMHUDState` hash differs from the previous render:
* If only `Cursor`, `ActivePane`, or `Marked` changed (e.g. the user pressed `Down`, `Up`, `Tab`, or `Insert`), Go calls:
```go
EvalJS(fmt.Sprintf("globalThis.__kabibi_hud?.updateCursor(%d, %d, %d, %d)",
    activePane, left.Cursor, left.ScrollCol, right.Cursor))
```
This sends `< 60 bytes` across the `eval` bridge and triggers the 95ms CSS gliding transition on the browser side.
* When a directory loads (`LoadDir`), panels toggle (`Ctrl+O`), or terminal dimensions change (`SIGWINCH`), Go serializes the full `DOMHUDState` JSON and calls:
```go
EvalJS(fmt.Sprintf("globalThis.__kabibi_hud?.syncState(%s)", strconv.Quote(string(stateJSON))))
```

### 7.2 Input Routing: Zero Desynchronization Between DOM HUD and Go `AppWidget` (Phase 2)

* **Keyboard Events (100% Controlled by Go `AppWidget.HandleEvent`):**
  * The hidden `.xterm-helper-textarea` retains keyboard focus at all times.
  * When the user presses `Tab`, `Up`, `Down`, `Left`, `Right`, `Insert`, `Enter`, `Ctrl+O`, `F5`, or types characters into `~/path፧`, `@xterm/xterm` sends the standard VT sequence straight into `hostState.pushStdin(data)`.
  * Go’s `AppWidget.HandleEvent` executes the **Far Manager Split Key-Routing Matrix (§5.1 of `3-blue.md`)**, updates the active `FilePaneWidget` or `PromptInput`, and emits the `updateCursor(...)` or `syncState(...)` `EvalJS` call on the resulting render pass.
* **Mouse Clicks & Hover on the DOM Blue Panels:**
  * Clicking a file row in the DOM panel emits the corresponding VT sequence or synthetic key chord into `hostState.pushStdin(...)` (or invokes a lightweight `__kabibi_hud_action` queue read by Go), and double-clicking a directory or executable triggers `Enter`.

---

## 8. Phased Implementation Plan

### Phase 1 (Active): Default Embed Contract & Opt-In Super-Demo CRT

1. **Implement and Preserve Kabibi’s Default Browser Mount Contract:**
   * Accept an explicit parent element or parent ID; when neither is supplied, search for the documented conventional parent ID.
   * If a parent resolves, mount ordinary xterm.js in that parent and fit to its content box without claiming full-screen layout.
   * If no parent resolves, create a host, append it to `document.body`, and make that newly created host full-screen. Preserve working `washmhost` input/output, TTY sizing, focus, and resize behavior.

2. **Keep JavaScript in Standalone Files and Embed It for Go:**
   * Move Kabibi’s browser runtime and adapter code out of quoted/backtick Go literals into authored `.js` source files.
   * Embed the required scripts with Go’s `//go:embed` directive and evaluate them through the existing browser host mechanism. Keep default-mode mount logic separate from the super-demo entrypoint so CRT dependencies are not loaded or initialized by default.
   * Add `node --test` tests for DOM-independent mode selection, parent resolution, full-screen host creation decisions, and fallback state selection. Keep the source directly testable in Node; add bundling only if the current `EvalJS` loading mechanism requires it.

3. **Add an Explicit Super-Demo Entrypoint and Its Stage:**
   * Prefer a dedicated super-demo page that explicitly opts into the CRT mode, avoiding changes to Kabibi’s default embedding behavior. An explicit Kabibi mode parameter is an acceptable alternative.
   * In super-demo, use the caller’s selected parent bounds if one was supplied. If no parent exists, create and lock a full-viewport host.
   * Create the off-screen xterm host and `#crt-stage` within that selected host. Keep xterm laid out, nonzero-sized, and intersecting so its observers and fitting continue to work.

4. **Initialize `@xterm/xterm@6.0.0` + `@xterm/addon-webgl@0.11.0` + `@xterm/addon-fit@0.10.0` (Vendor/Patch as Needed):**
   * Instantiate `const webglAddon = new WebglAddon(true)` so `WebglRenderer` requests its WebGL2 context with `preserveDrawingBuffer: true`.
   * Acquire the rendered terminal screen canvas (`WebglRenderer._canvas`, exposed via a clean `webglAddon.renderCanvas` getter if vendored/patched, rather than `webglAddon.textureAtlas`).
   * Hook `(term as any)._core._renderService.onRender` (or a vendored `onDidRenderFrame` event) to set `xtermCanvasTexture.needsUpdate = true` on every content render, cursor blink, and selection highlight change.
   * Compute `{ cols, rows }` from the selected parent’s bounds via `FitAddon.fit()`. Observe parent and window resizing, then notify the host of the updated TTY size and trigger `SIGWINCH` (`28`) via `hostState.triggerSignal(28)`.

5. **Implement Non-GPU Direct-to-DOM Fallback (`activateDirectDomFallback()`):**
   * If WebGL2 or Three.js GPU initialization fails, the GPU path is unusably slow, or `webglAddon.onContextLoss` fires, call `webglAddon.dispose()` (which automatically restores xterm.js’s core `DomRenderer` on the live `Terminal`), tear down `#crt-stage`, and set `#xterm-offscreen-host` to `opacity: 1; pointer-events: auto; z-index: 10` for direct-to-DOM rendering within the selected parent, with zero flourish.

6. **Build the `CRTRenderer` in Three.js r186 (`crt_stage.js`):**
   * Initialize `WebGPURenderer` (`await renderer.init()`) and `RenderPipeline`.
   * Position the `PerspectiveCamera` at the **subtle bottom-right low angle** ($\mathbf{P}_{\text{cam}} = (+0.36, -0.24, 2.68)$ looking at $(+0.03, -0.02, 0.0)$) so the curved screen is framed like a stylish low-angle car poster with the bottom-right region closer to the lens.
   * Construct `phosphorMesh` ($128\times128$ bowed grid, $k=0.14$) with the TSL scanline + aperture grille + halation + vignette node material bound to `new THREE.CanvasTexture(xtermRenderCanvas)`.
   * Construct `glassMesh` ($64\times64$ bowed grid, $k=0.085$, $z=+0.055$) with `MeshPhysicalNodeMaterial` (`transmission: 1.0`, `ior: 1.48`, `dispersion: 1.6`) and the 9-tap parallax-shifted blurry internal reflection `emissiveNode`.
   * Configure `THREE.RenderPipeline` with both **phosphor bloom** and the **depth-aware near-field Depth-of-Field pass** (`viewZNode`) so most of the screen remains razor-sharp while the closer bottom-right area softens subtly.

7. **Implement 3D Raycast Pointer, Selection & Keyboard Focus Forwarding:**
   * Keep `.xterm-helper-textarea` focused on pointer interaction so `term.onData` keyboard input, IME, and clipboard copy/paste work seamlessly.
   * On `#crt-stage`, call `e.stopPropagation()` on raw mouse events (preventing unwarped coordinates from reaching xterm’s `document`-level drag listeners), raycast through the low-angle camera onto `phosphorMesh`, map `hit.uv` $(u, 1-v)$ into `screenElement.getBoundingClientRect()`, and dispatch synthetic `MouseEvent` / `WheelEvent` instances (`bubbles: true`) onto `term.element`.

8. **Phase 1 Verification Matrix:**
   * **Off-Screen Surface & Frame Pacing Check:** Verify that `xtermCanvasTexture` updates on ANSI output, cursor blink, and mouse selection drag without flicker or missed redraws, and maintains smooth 60/120 FPS pacing.
   * **Low-Angle Framing & Near-Field DoF Check:** Verify that the bottom-right low-angle camera pose and near-field blur soften only the closer bottom-right corner subtly while keeping the main terminal workspace and prompt sharp and readable.
   * **Input & Raycast Accuracy Check:** Verify that keyboard input (`Ctrl+O` TUI panels, shell commands, vim/less pagers) and mouse text selection/dragging hit the exact character cell across both flat center regions and curved edges.
   * **Direct-to-DOM Fallback Check:** Simulate WebGL context loss / non-GPU fallback and verify that `webglAddon.dispose()` switches cleanly to direct-to-DOM xterm.js rendering inside the selected parent without dropping the active `washmhost` session. Confirm it is full-page only when Kabibi created the host itself.

---

### Phase 2 (Paused / Deferred): Floating DOM Blue Panels HUD & `eval` Bridge Integration

1. **Smuggle HUD Controller from Go (`kabibi/ui/filemanager/dom_hud_js.go`):**
   * Inject `globalThis.__kabibi_hud` via `EvalJS` when `OSName == "browser"` and Phase 2 HUD mode is enabled.
   * Build `#kabibi-dom-hud`, scoped CSS rules (top-drawer spring transition, `.hud-selection-pill` cubic-bezier glide, frosted deep-blue surface, and multi-column grid), and inline SVG file-icon registry.

2. **`AppWidget` Browser HUD Hook (`kabibi/ui/app/app_widget.go`):**
   * When `w.panelsVisible == true` in Phase 2 browser HUD mode, keep `w.sh.SetBottomAligned(true)`, dim rows $0 \dots H_{\text{panels}}-1$ of the CRT `CellBuf` instead of drawing duplicate TUI panel cells, and push `DOMHUDState` deltas via `EvalJS`.
   * Align `#kabibi-dom-hud` with the projected 3D screen boundary of row $H_{\text{panels}}$ under the low-angle camera.

3. **Optional Crisp DOM Modal Overlay (F2/F5/F6/F7/F8):**
   * Render file operation modals either inside `#kabibi-dom-hud` when panels are open or inside the CRT `CellBuf`.

# CRITICAL implementation rules

> I want to say, this CRT blurry presentation should be a separate mode, which is not what Kabibi in browser does by default.
>
> The default should look for the parent (by id or convention or explicit parameter) to add xterm.js and simply operate inside it. And if the parent is not found, add one and lock it for full screen.
>
> However the CRT/blurry/flourish should be flipped on for running in the super-demo mode.
>
> We can code both of those inside the Kabibi code, or have the super-demo mode in super-demo page whichever easier.
>
> Also I want to be clear: currently Kabibi's JS code is awkwardly embedded in Golang source code with quotes and backticks. We want as much as possible of that out in JS files, that are puled into Golang at build time with //go:... directives. That allows us to test JS too, in node.js built-in tester separately from the page.

Yes—both are salient architectural requirements, not implementation details.

1. **Kabibi’s default browser mode is an embeddable terminal.** It should use a caller-supplied parent when provided, otherwise find the agreed parent element by ID or convention. Only if no parent exists should it create a container and make it full-screen. CRT, blur, and other flourish effects must be **opt-in for super-demo mode**, not the default browser presentation. Super-demo can be a separate page or an explicit mode in Kabibi; that choice can follow whichever fits the existing structure best.

2. **Browser JavaScript should live in JavaScript files.** Kabibi should load those files into Go at build time with `//go:embed`, rather than maintaining large JS snippets as quoted or backtick-escaped Go strings. Keeping the JS independently testable with Node’s built-in test runner should be part of the design.

I’ll treat these as constraints when revising the spec: separate the default embed path from the super-demo CRT path, and define a JS module/embedding/testing structure.

# Flourish polish

> Now we need to remove move the HTML part of our demo into the root of repository index.html, and cleanup the rest.
>
> By that I mean that the index.html should refer to washmhost/js script next to index.html itself. At start it should verify if the washmhost was loaded. If it wasn't, assume that it runs from dev environment and should instead be loaded from './mohabbat/washmhost/js/browser/index.js' relative to index.html
>
> When washmhost is loaded, the embedded in index.html script should trigger a function inside it that would boot washmhost and request to load a vegetable kabibi.bat from next to index.html, or if not then from './release/dist/kabibi.bat'.
>
> The logic for loading a vegetable should be implemented inside washmhost itself, as part of the API.  Washmhost/JS should only load vegetables in a headless mode (no terminal). The logic inside Kabibi/browser mode should enable super-demo mode if certain parameter is set before it's loaded, for example __super_demo__ is set on window object. Naturally the code embedded inside index.html should set it before asking washmhost to load kabibi.bat.
>
> The loading of xterm.js THREE.hs and wiring it to full-screen and all that should move inside Kabibi/browser support logic. That logic should verify if WebGL2 is supported and instantiate the rendering pipeline and then present Kabibi into it. If WebGL2 is not supported, that Kabibi/browser logic needs to instantiate flat full-page xterm.js with auto-sized terminal and run inside that without the flourish.

Here is the implementation plan for moving the demo boot into the repository root `index.html`, keeping `index.js` purely headless, and driving all terminal/CRT presentation from inside the Kabibi WASM guest via `kabibi/browser`.

---

### 1. Root `index.html` — Minimal Boot & Fallback Loader
Replace the legacy split-pane README/xterm page in `index.html` with a minimal HTML entrypoint that contains **no xterm.js or Three.js tags**:
1. **Load `washmhost` with dev fallback:**
   - Include `<script src="./washmhost.js"></script>` first (which resolves in a staged distribution like `release/dist/index.html`).
   - In the inline bootstrap script, check whether `globalThis.washmhostExports` is defined. If not, assume a development environment and dynamically inject `<script src="./mohabbat/washmhost/js/browser/index.js"></script>` and wait for `onload`.
2. **Set the super-demo flag & request vegetable boot:**
   - Set `window.__super_demo__ = true` before booting the vegetable (while also honoring optional URL overrides like `?flat=1` or `?super_demo=0` if needed for diagnostics).
   - Call the vegetable loader API on `globalThis.washmhostExports` with candidate paths `['./kabibi.bat', './release/dist/kabibi.bat']` (also honoring an optional `?artifact=` / `?wasm=` query parameter override before falling back to those defaults).

---

### 2. Headless Vegetable Loader API in `index.js`
Extend `index.js` while keeping `washmhost` completely headless (zero DOM or terminal dependencies):
1. **Candidate-based vegetable loading inside `washmhost` / new helper `loadVegetable(candidates, options)` on `washmhostExports`:**
   - Accept either a single URL/binary or an array of candidate URLs (e.g., `['./kabibi.bat', './release/dist/kabibi.bat']`) in `params.wasm` (and expose a convenience method such as `washmhostExports.loadVegetable(candidates, options)` that delegates to `washmhost`).
   - Iterate through candidate URLs sequentially via `fetch`, unpack the first reachable vegetable via `unpackVegetable`, and boot the guest headlessly.
2. **Dynamic Terminal Attachment on the Running Headless Host:**
   - Currently, `makeBrowserImports` captures `params.stdout`, `params.stderr`, `params.getTtySize`, and `params.setTtySize` once at instantiation time. When `washmhost` boots a vegetable headlessly, no terminal exists yet until the guest calls `Install` via `EvalJS`.
   - Store mutable I/O and TTY delegates on the controller / `HostState` (along with a small early-stdout buffer for any bytes written before a terminal attaches) and expose `attachTerminal({ stdout, stderr, getTtySize, setTtySize, onExit })` (plus `pushStdin` and `triggerSignal`) on the active host handle—accessible to the guest's `EvalJS` runtime via `globalThis.washmhostExports.getActiveHost()` (or `host.attachTerminal(...)`).
   - Existing callers that pass `stdout`/`stderr`/`getTtySize` up front (such as `extension.js` in VS Code and existing unit tests in `index.test.js`) continue to work unchanged.

---

### 3. Self-Contained Browser & CRT Boot Inside `kabibi/browser`
Move all stylesheet/module loading, WebGL2 capability detection, full-screen host creation, and `washmhost` wiring into `main.js`, `super-demo.js`, and `install_wasip1.go`:
1. **Guard against non-DOM hosts (such as the VS Code extension host):**
   - In `Install` / `main.js`, check whether we are in a browser DOM environment (`typeof window !== 'undefined' && typeof document !== 'undefined'`) and no external host terminal was pre-attached (so `extension.js` is unaffected).
2. **Mode Detection via `window.__super_demo__`:**
   - In `install_wasip1.go`, detect `Boolean(globalThis.__super_demo__ || globalThis.__kabibiBrowserMode === "super-demo")`.
   - Evaluate `main.js` (and `super-demo.js` when super-demo mode is enabled), then invoke an async bootstrap function (`KabibiBrowser.bootFromGuest({ superDemo })`) whose returned `Promise` is awaited by `ai.EvalJS` before `Boot` constructs `ui.Host` and queries initial terminal size.
3. **Dynamic Dependency Loading & WebGL2 Check Inside `kabibi/browser`:**
   - Ensure the `@xterm/xterm@6.0.0/css/xterm.css` stylesheet and base page styling (`margin: 0; height: 100%`) are injected if not already present.
   - Dynamically `import()` `@xterm/xterm@6.0.0/+esm` and `@xterm/addon-fit@0.11.0/+esm`.
   - If `superDemo` is enabled:
     - Probe WebGL2 support first (checking `canvas.getContext('webgl2')` and rejecting software rasterizers).
     - If WebGL2 is supported, dynamically `import()` `@xterm/addon-webgl@0.19.0/+esm` and `three@0.180.0/build/three.module.js`, instantiate the CRT rendering pipeline via `super-demo.js`, and attach it to the active `washmhost` session.
     - If WebGL2 is **not** supported (or if Three.js / WebGL addon loading or initialization fails, or context is lost), fall back cleanly to flat, full-page auto-sized xterm.js (`FitAddon.fit()`) with zero flourish.
   - If `superDemo` is not enabled:
     - Resolve the caller parent (`#kabibi-terminal` or explicit parent) or create a full-page container if none exists, mount flat auto-sized xterm.js, and attach it to the active `washmhost` session.

---

### 4. Cleanup & Tests
1. **Remove obsolete demo files in `kabibi/browser`:**
   - Remove `demo.html`, `super-demo.html`, and the local untracked/sideloaded `kabibi.bat` in `kabibi/browser`.
   - Update `README.md` and `release/main.go` (if needed) to reflect that root `index.html` is the single demo page.
2. **Unit & Browser Validation:**
   - Add unit tests in `index.test.js` covering headless candidate vegetable loading (`./kabibi.bat` fallback to `./release/dist/kabibi.bat`) and `attachTerminal` rebinding.
   - Update `main.test.cjs` to test `__super_demo__` detection, WebGL2 probing + automatic fallback to flat full-page auto-sized xterm.js when WebGL2 is unavailable, and guest-driven mounting against an active `washmhost` session.
   - Rebuild the `kabibi.bat` vegetable and dogfood root `index.html` via `demo-go/server`.

