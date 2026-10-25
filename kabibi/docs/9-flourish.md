# Kabibi Browser Visual Flourish Overview

The overall complexity is **moderate-to-high, but highly partitioned**.

Because the architectural boundaries are decoupled, the complexity doesn't compound into a fragile mess. It divides cleanly into two independent technical challenges:

---

### 1. The CRT / Glass Refraction Shader (Moderate Complexity)

The rendering pipeline is self-contained:

* **The Easy Part:** xterm.js’s WebGL addon renders into an offscreen `<canvas>`. Treating that canvas as a `sampler2D` texture in a post-processing WebGL2 pass is standard graphics programming.
* **The Shader Math:** A standard post-processing fragment shader over a screen-filling quad:
* **Spherical/Barrel Distortion:** Warping UVs ($UV_{curved} = UV + (UV - 0.5) \cdot r^2 \cdot k$).
* **Vignette & Scanlines:** Radial attenuation and a periodic sine function ($0.5 + 0.5 \sin(y \cdot \text{scale})$).
* **Glass Reflection & Refraction:** Adding a secondary blurred reflection gradient/tint over the top.


* **The Real Hurdle:** **Mouse coordinate inversion.** If the CRT curve is purely aesthetic/output-only, it’s trivial. But if the user clicks or drags text inside xterm.js, browser mouse coordinates `(clientX, clientY)` will not match the warped character cells unless you apply the inverse distortion transformation in the event listener before passing events into xterm.

---

### 2. The DOM File Panels & Animations (Low-to-Moderate Complexity)

This is standard modern web UI engineering:

* **DOM vs Canvas:** By building the blue panels as standard HTML/CSS (`position: fixed` or `position: absolute`), you bypass xterm.js entirely.
* **Visual Polish:** CSS transitions handle smooth selection highlights (`transform: translateY(...)`), drawer slide-downs (`transform: translateY(-100%)` $\rightarrow$ `translateY(0)`), and SVG file icons cleanly.
* **The Real Hurdle:** **Input multiplexing & synchronization.** Kabibi’s core Far Manager logic (directory state, cursor selection, split-pane routing) is driven from Go.
* If the DOM panel is purely a visual HUD mirror, the Go guest needs to stream lightweight state updates (e.g., active index, file list) over the eval bridge or host communication channel to update the DOM elements.
* You need clean focus management so navigation keys (arrows, Tab, Enter) route to the DOM panels when open, and fall through to xterm.js when hidden.



---

### Complexity Summary

| Component | Effort / Difficulty | Primary Risk / Edge Case |
| --- | --- | --- |
| **WebGL2 CRT Shader** | Medium (~150 LOC GLSL/JS) | Mouse/click hit-testing offset if text selection is needed on curved canvas. |
| **DOM File Panels HUD** | Low–Medium (CSS + DOM) | Managing z-index and CSS layout transitions without lagging terminal frame rates. |
| **Bridge & Key Routing** | Medium | Keeping Go WASM state and browser DOM/xterm focus strictly in sync. |

Overall, it is **very feasible**—the CRT shader and DOM HUD can be prototyped and tuned completely in isolation in plain HTML/JS before wiring them to Kabibi's guest engine.

---

# Kabibi Browser Visual Flourish Specification: Full-Screen Curved CRT, Physical Glass Refraction & Floating DOM Blue HUD


Here is a comprehensive, end-to-end architectural and implementation plan to build Kabibi’s **100% Full-Screen Curved CRT + Refractive Glass Shell + Floating DOM Blue Panels HUD**, engineered against the latest **Three.js r186 (`0.186.0`)** and **`@xterm/xterm` (`6.0.0`)** ecosystems and integrated directly with Kabibi’s `ui.Host` and `eval` virtual dylib bridge.


## 1. Architectural Vision & Core Invariants

1. **100% Full-Screen Viewport (`100vw × 100vh`) — Zero Sidebars:**
* There are no external browser sidebars, split webviews, or inspector chrome. The Three.js CRT viewport fills the entire browser window (`100vw × 100vh`, `overflow: hidden`).
* All diagnostic output, AI deliberation tickers (`[goal]…`, `[steps]…`, `[work]…`), tool statuses, and shell history live **inside the terminal stack itself**, exactly as designed in the Unified `CellBuf` & Plume architecture.




2. **Off-Screen `@xterm/xterm` (v6.0.0) + `@xterm/addon-webgl` (v0.11.0) Source:**
* `@xterm/xterm` renders at native high-DPI resolution into an off-screen/hidden container (`opacity: 0; pointer-events: none; position: fixed`) using its WebGL2 texture-atlas renderer (`preserveDrawingBuffer: true`) so glyph rasterization, ligatures, DEC mode 2026 synchronized output, and truecolor ANSI sequences emitted by `ui.Host` remain fast and accurate.




3. **Three.js r186 (`three@0.186.0`) Dual-Surface Physical CRT Assembly:**
* Uses `WebGPURenderer` (`import * as THREE from 'three/webgpu'`) with automatic WebGL2 fallback and **TSL (Three.js Shading Language)** (`import { ... } from 'three/tsl'`) plus `THREE.RenderPipeline` (which replaced `PostProcessing` / `EffectComposer` in Three.js r183–r186).
* **Inner Surface (Curved Phosphor CRT Mesh):** A high-subdivision bicubic/spherically bowed 3D surface (`PlaneGeometry(W, H, 128, 128)` displaced along $+Z$) mapped with a live `THREE.CanvasTexture` driven by xterm’s WebGL2 canvas, shaded with phosphor sub-pixel aperture grille, scanline modulation, and dynamic phosphor bloom.
* **Outer Surface (Thick Refractive & Reflective Front Glass Plate):** A second, slightly flatter curved 3D glass mesh positioned physically in front of the phosphor screen ($\Delta Z = +0.06$), using `THREE.MeshPhysicalNodeMaterial` with **real volumetric transmission (`transmission: 1.0`)**, **index of refraction (`ior: 1.48`)**, **chromatic dispersion (`dispersion: 2.5`)**, **thickness attenuation**, and **blurred internal back-reflection** of the terminal text itself.


4. **Crisp Floating DOM Blue Panels HUD (`z-index: 20`):**
* When Kabibi detects it is running in the browser (`syscall.GetPlatformInfo().OSName == "browser"`) and the user presses `Ctrl+O`, the Dual Blue File Manager Panels do **not** suffer CRT barrel distortion or scanline blurring.


* Instead, they render in a razor-sharp **HTML/CSS DOM Overlay** suspended directly *in front of* the Three.js CRT glass canvas, animating down from the top edge (`translateY(-105%)` $\rightarrow$ `translateY(0)`) with spring physics, distinct SVG file-type icons, and a gliding selection highlight pill (`transition: transform 110ms cubic-bezier(0.22, 1, 0.36, 1)`).
* Meanwhile, underneath the floating DOM panels, Kabibi’s `Shell` stack still executes the exact **Phase B Bottom-Aligned Illusion** (`sh.SetBottomAligned(true)`): the bottom $H_{\text{gap}}$ rows of the curved CRT screen stay uncovered right below the DOM panels, showing the `BlockBanner`, `~/path፧` `PromptInput`, and live command output sliding upward *behind* the DOM panels!





---

## 2. Technology Stack & 2026 Version Pinning

| Package | Version | Import / Entrypoint | Role in Kabibi Browser Host |
| --- | --- | --- | --- |
| **`three`** | **`0.186.0` (r186)** | `three/webgpu` & `three/tsl` | `WebGPURenderer` (with auto WebGL2 fallback), `MeshPhysicalNodeMaterial`, `RenderPipeline`, and TSL shader graph nodes. |
| **`@xterm/xterm`** | **`6.0.0`** | `@xterm/xterm` | Headless/off-screen VT emulator with DEC mode 2026 synchronized output support. |
| **`@xterm/addon-webgl`** | **`0.11.0`** | `@xterm/addon-webgl` | GPU texture-atlas renderer producing the source `<canvas>` bound to Three.js `CanvasTexture`. |
| **`@xterm/addon-fit`** | **`0.10.0`** | `@xterm/addon-fit` | Computes exact `{ cols, rows }` grid matching the full-screen viewport aspect ratio and font metrics. |

*Import Map for `index.html`:*

```html
<script type="importmap">
{
  "imports": {
    "three": "https://cdn.jsdelivr.net/npm/three@0.186.0/build/three.webgpu.js",
    "three/webgpu": "https://cdn.jsdelivr.net/npm/three@0.186.0/build/three.webgpu.js",
    "three/tsl": "https://cdn.jsdelivr.net/npm/three@0.186.0/build/three.tsl.js",
    "three/addons/": "https://cdn.jsdelivr.net/npm/three@0.186.0/examples/jsm/",
    "@xterm/xterm": "https://cdn.jsdelivr.net/npm/@xterm/xterm@6.0.0/+esm",
    "@xterm/addon-webgl": "https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@0.11.0/+esm",
    "@xterm/addon-fit": "https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/+esm"
  }
}
</script>

```

---

## 3. Layer Cake & Full-Screen Spatial Geometry

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Browser Window (100vw × 100vh, position: fixed, overflow: hidden, #050608)   │
│                                                                              │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │ Layer 3 (z-index: 20): Floating DOM Blue Panels HUD (#kabibi-dom-hud)  │  │
│  │  • Height: exactamente (H_panels / H_term) * 100vh                     │  │
│  │  • Slides down from top via cubic-bezier spring when Ctrl+O is active  │  │
│  │  • Crisp sub-pixel typography, SVG file icons, gliding cursor bar      │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │ Layer 2 (z-index: 10): Three.js r186 Full-Screen Canvas (#crt-stage)   │  │
│  │  • Outer Mesh: Curved Physical Glass Plate (IOR 1.48, Refraction,      │  │
│  │    Chromatic Dispersion, Blurred Back-Reflection of Phosphor Text)     │  │
│  │  • Inner Mesh: Bowed CRT Phosphor Screen (xterm CanvasTexture,         │  │
│  │    Scanlines, Aperture Grille, RenderPipeline Bloom)                   │  │
│  │  • Bottom Gap (H_gap rows): Uncovered by Layer 3! Displays Kabibi      │  │
│  │    Banner, ~/path፧ PromptInput, and AI/Shell Plume sliding under HUD   │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │ Layer 1 (z-index: -1, opacity: 0): Off-Screen @xterm/xterm + WebglAddon│  │
│  │  • Receives raw ANSI diffs from ui.Host (ToANSI) and keyboard focus    │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘

```

### 3.1 Off-Screen `@xterm/xterm` Composite Canvas Problem & Solution

A critical detail of `@xterm/xterm`:

* When `@xterm/addon-webgl` is loaded, xterm renders the character cells, background colors, and selection into the **WebGL2 `<canvas class="xterm-webgl-canvas">**` (or the main `.xterm-screen canvas`), while keeping `.xterm-helper-textarea` in the DOM for IME, paste, and keyboard capture.
* **Crucial WebGL2 Context Rule:** By default, WebGL2 clears its drawing buffer after compositing to the DOM unless `preserveDrawingBuffer: true` is set, OR we sample the canvas inside the same `requestAnimationFrame` tick immediately after `term.onRender`.
* **Zero-Flicker Hook:**
1. Intercept `HTMLCanvasElement.prototype.getContext` on the off-screen xterm container so when `WebglAddon` requests `'webgl2'`, we force `{ preserveDrawingBuffer: true, antialias: false, powerPreference: 'high-performance' }`.
2. Attach `term.onRender(() => { xtermCanvasTexture.needsUpdate = true; })` so Three.js only uploads the texture to the GPU when `ui.Host` actually flushes dirty rows or the cursor blinks!





---

## 4. Three.js r186 3D Scene, Curved Geometry & TSL Shaders

### 4.1 Full-Screen Camera & Aspect-Locked CRT Mesh Sizing

We want the curved terminal to fill the entire `100vw × 100vh` screen edge-to-edge with just enough bezel curvature at the corners to sell the physical CRT + glass depth without wasting screen real estate:

* Use a `THREE.PerspectiveCamera(fov = 32, aspect = window.innerWidth / window.innerHeight, 0.1, 100)` positioned at `z = 2.8`.
* At distance $D = 2.8$, the visible frustum height is:

$$H_{\text{world}} = 2 \cdot D \cdot \tan\left(\frac{\text{fov}}{2}\right), \qquad W_{\text{world}} = H_{\text{world}} \cdot \text{aspect}$$


* Whenever the browser window resizes, we update the geometry scale `(W_world * 0.965, H_world * 0.965, 1)` so the terminal occupies **96.5% of the full viewport**, framed by a subtle, dark anodized curved bezel around the outer 1.75% margin.

### 4.2 Mathematical Surface Bending (Phosphor Mesh vs. Front Glass Plate)

Instead of a cheap 2D post-processing UV warp (which cannot produce true 3D glass thickness or paralactic reflection as the eye/mouse moves), we bend **real 3D vertex geometry**:

* Given normalized plane coordinates $(u, v) \in [-1, 1]^2$:

$$r^2 = u^2 + v^2, \qquad z_{\text{bend}}(u, v) = -k_{\text{curve}} \cdot \left(u^2 + v^2 + 0.15 \cdot u^2 v^2\right)$$


* **Mesh 1 — Inner Phosphor Screen (`z_base = 0.0`, $k_{\text{curve}} = 0.14$):**
* Bowed backward at the edges and corners, bulging softly toward the viewer at the center $(0,0)$.
* Normals are recomputed analytically (`geometry.computeVertexNormals()`) so lighting and reflections curve naturally across the surface.


* **Mesh 2 — Outer Refractive Glass Plate (`z_base = +0.055`, $k_{\text{curve}} = 0.085$):**
* Positioned $0.055$ world units in front of the phosphor screen with a slightly shallower curvature ($k_{\text{curve}} = 0.085$).
* **Why differential curvature matters:** Because $k_{\text{glass}} < k_{\text{phosphor}}$, the optical air/glass gap $\Delta Z(u,v)$ between the phosphor and the front glass is thinner in the center ($0.055$) and thicker toward the corners ($0.11$). When `MeshPhysicalNodeMaterial` computes volumetric refraction through this varying thickness, the edges naturally exhibit richer glass refraction, chromatic dispersion, and internal reflection—just like a real Sony Trinitron / Tektronix glass faceplate!



### 4.3 TSL Shader Graph for the Inner Phosphor Screen (`MeshBasicNodeMaterial`)

Using `three/tsl` in r186:

```js
import * as THREE from 'three/webgpu';
import {
  texture, uv, vec2, vec3, float, sin, cos, mix, smoothstep, uniform
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

// 4. Corner shadow (tube mask vignette)
const edgeDist = vUv.sub(0.5).mul(2.0);
const vignette = smoothstep(1.35, 0.75, edgeDist.dot(edgeDist));

const phosphorMaterial = new THREE.MeshBasicNodeMaterial();
phosphorMaterial.colorNode = phosphorCol.rgb.mul(scanMask).mul(grille).mul(vignette);

```

### 4.4 TSL Shader Graph for the Outer Glass Plate (Refraction + Blurry Internal Reflection)

For the front glass sheet (`Mesh 2`), you specifically asked for **a bit of refraction and a bit of blurry reflection back**:

1. **Physical Refraction & Chromatic Dispersion:**
* `MeshPhysicalNodeMaterial` in Three.js r186 supports native screen-space volume refraction via `transmission: 1.0`, `ior: 1.48`, `thickness: 0.45`, and `dispersion: 1.8`.
* Because Three.js renders transmissive objects in a separate pass after opaque meshes, the glass mesh automatically samples the rendered inner phosphor screen behind it, bending rays along the curved glass surface normals and splitting RGB slightly at steep viewing angles near the corners.


2. **Blurry Reflection Back (Internal Double-Reflection / Ghosting + Ambient Studio Glint):**
* In a physical CRT with a laminated safety glass faceplate, bright phosphor characters reflect off the *inside* of the front glass and bounce back toward the viewer as a soft, out-of-focus ghost reflection offset by the glass thickness and viewing angle (`viewDir`).
* In TSL, we construct a custom `emissiveNode` on the glass material that samples a **multi-tap Gaussian-blurred mip** of `xtermTexture` at a view-dependent parallax offset $\Delta UV = \text{reflect}(\text{viewDir}, \text{normal}).xy \times 0.012$:



```js
import {
  texture, uv, normalView, positionViewDirection, reflect, vec2, vec3, float, pow, dot, max
} from 'three/tsl';

const vUv = uv();
const reflDir = reflect(positionViewDirection.negate(), normalView);
const parallaxOffset = reflDir.xy.mul(0.014); // Shifted by viewing angle on curved glass
const reflUv = vUv.add(parallaxOffset);

// 9-tap wide tent blur of the terminal texture for the blurry glass back-reflection
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

// Schlick Fresnel term so reflection is subtle dead-center (~3%) and stronger on curved edges (~18%)
const cosTheta = max(dot(normalView, positionViewDirection), 0.0);
const fresnel = float(0.03).add(float(0.18).mul(pow(float(1.0).sub(cosTheta), 3.5)));

// Subtle diagonal overhead softbox reflection across the curved glass
const studioGlint = pow(max( dot(normalView, vec3(-0.35, 0.55, 0.75)), 0.0 ), 16.0).mul(0.035);

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

### 4.5 Post-Processing via `THREE.RenderPipeline` (r186)

To give bright text and the `፧` prompt a warm phosphor halo without washing out readability:

```js
import { pass, bloom } from 'three/tsl';

const renderPipeline = new THREE.RenderPipeline(renderer);
const scenePass = pass(scene, camera);
const sceneColor = scenePass.getTextureNode('output');

// Subtle phosphor bloom (threshold 0.65, strength 0.22, radius 0.35)
const bloomPass = bloom(sceneColor, 0.22, 0.35, 0.65);
renderPipeline.outputNode = sceneColor.add(bloomPass);

```

### 4.6 Exact Raycast Mouse Coordinate Inversion (Clicking & Selecting on the Curved CRT)

Because the terminal sits on a curved 3D mesh behind glass, standard browser mouse coordinates `(e.clientX, e.clientY)` will not line up with xterm's flat off-screen grid unless mapped through 3D space:

1. Listen to `pointerdown`, `pointermove`, `pointerup`, and `wheel` on `#crt-stage`.
2. Convert `(e.clientX, e.clientY)` to Normalized Device Coordinates $\text{NDC} \in [-1, 1]^2$.
3. Run `raycaster.setFromCamera(ndc, camera)` against `phosphorMesh`.
4. Read `hit.uv` $(u, v) \in [0, 1]^2$ directly from the triangle intersection!
5. Map $(u, 1 - v)$ to the off-screen xterm container’s bounding box `(u * xtermWidth, (1 - v) * xtermHeight)` and dispatch a synthetic `MouseEvent` / `WheelEvent` to the off-screen `.xterm-screen` element.
6. **Result:** Even near the heavily curved corners of the CRT, clicking or dragging text selects the exact character cell under the user's cursor!

---

## 5. The Floating DOM Blue Panels HUD (Outside & Above the CRT)

When Kabibi runs in the browser, pressing `Ctrl+O` summons the dual-pane file manager in two coordinated layers:

### 5.1 How the Terminal & DOM HUD Coexist seamlessly (No Blank Void, Exact Fit)

Look at how cleanly this marries your **Unified `CellBuf` & Blue Panel Illusion (`3-blue.md`)**:

1. **In Go (`kabibi/ui/app/app_widget.go`):**
* When `Ctrl+O` toggles `w.panelsVisible = true`, `AppWidget` still executes the exact height calculation:



$$H_{\text{panels}} = H_{\text{term}} - H_{\text{gap}}$$



and sets `w.sh.SetBottomAligned(true)`.


* **Browser Mode Hook:** When `syscall.GetPlatformInfo().OSName == "browser"`:


* Instead of blitting the ASCII `DualPaneWidget` cells over rows `0 .. H_panels-1` of the terminal `CellBuf` (which would cause the blue panels to be drawn *twice*—once curved inside the CRT and once flat in the DOM!), `AppWidget.Render` leaves rows `0 .. H_panels-1` showing the **unmasked shell block history** (or dims them slightly) while emitting the structured panel state to the browser DOM via the `eval` bridge!
* **Wait—think about how cool that is:** Because the DOM blue panels have a semi-translucent deep-blue acrylic backdrop (`background: rgba(0, 10, 95, 0.86); backdrop-filter: blur(8px)`) and hang down from the top of the screen over rows `0 .. H_panels-1`, you can faintly see the curved CRT terminal output *physically sliding upward behind the frosted blue DOM panels* as commands run in the bottom gap!
* And when the user presses `Ctrl+O` again (`panelsVisible = false`), the DOM panels animate smoothly back up into the top bezel (`translateY(-105%)`), revealing the curved CRT shell underneath at full brightness!





### 5.2 DOM Structure & CSS Motion Architecture

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

### 5.3 The Three Visual Flourishes of the DOM Panels

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

## 6. Go $\leftrightarrow$ Browser State Synchronization Over the `eval` Bridge

Because Kabibi already has the pure `eval` virtual dylib (`syscall.DylibOpen("eval", 0)` / `EvalJS`) in `washmhost.js`, we do **not** need any new WASM ABI imports or custom host syscalls!

### 6.1 Go Side: `kabibi/ui/filemanager/dom_bridge.go`

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


3. **Delta-Only Push:**
* Whenever `AppWidget.Render` runs in browser mode and the `DOMHUDState` hash differs from the previous frame:
* If only `Cursor`, `ActivePane`, or `Marked` changed (e.g. the user pressed `Down`, `Up`, `Tab`, or `Insert`), Go calls:
```go
EvalJS(fmt.Sprintf("globalThis.__kabibi_hud?.updateCursor(%d, %d, %d, %d)",
    activePane, left.Cursor, left.ScrollCol, right.Cursor))

```


This costs **< 60 bytes** across the eval bridge and triggers the 95ms CSS gliding animation at 120fps!
* When a directory loads (`LoadDir`), panels toggle (`Ctrl+O`), or terminal dimensions change (`SIGWINCH`), Go serializes the full `DOMHUDState` JSON and calls:
```go
EvalJS(fmt.Sprintf("globalThis.__kabibi_hud?.syncState(%s)", strconv.Quote(string(stateJSON))))

```







### 6.2 Input Routing: Zero Desynchronization Between DOM HUD and Go `AppWidget`

How do keyboard and mouse events work when the DOM panels are open?

* Keyboard Events (100% Controlled by Go `AppWidget.HandleEvent`):


* The hidden `.xterm-helper-textarea` retains keyboard focus at all times!
* When the user presses `Tab`, `Up`, `Down`, `Left`, `Right`, `Insert`, `Enter`, `Ctrl+O`, `F5`, or types characters into `~/path፧`, `@xterm/xterm` sends the standard VT sequence straight into `washmhost.pushStdin(data)`.


* Go’s `AppWidget.HandleEvent` executes the **Far Manager Split Key-Routing Matrix (§5.1 of `3-blue.md`)**, updates the active `FilePaneWidget` or `PromptInput`, and immediately emits the 1-line `updateCursor(...)` or `syncState(...)` JS eval call on the same frame!


* **Why this is rock-solid:** We never duplicate Far Manager’s key-routing logic in JavaScript. Go remains the single source of truth for CWD sync, executable launching on `Enter`, and `PromptInput` routing.




* **Mouse Clicks & Hover on the DOM Blue Panels:**
* Clicking a file row in the DOM panel emits the corresponding VT sequence or synthetic key chord into `host.pushStdin(...)` (or invokes a lightweight `__kabibi_hud_action` queue read by Go), and double-clicking a directory or executable triggers `Enter`!



---

## 7. Phased Implementation Plan

### Phase 1: Full-Screen Off-Screen `@xterm/xterm` + `Three.js r186` CRT Stage (`mohabbat/washmhost/js/browser/`)

1. **Upgrade `index.html` Layout to 100% Full-Screen:**
* Remove any split panes, sidebars, or debug logs outside the terminal.
* Create the three stacked full-screen layers: `#xterm-offscreen-host`, `#crt-stage` (`<canvas>`), and `#kabibi-dom-hud`.


2. **Initialize `@xterm/xterm@6.0.0` + `@xterm/addon-webgl@0.11.0` + `@xterm/addon-fit@0.10.0`:**
* Configure xterm with `allowProposedApi: true`, JetBrains Mono / Fira Code font, and a rich dark CRT background (`#07090e`).
* Patch the off-screen container’s `getContext('webgl2')` call to enforce `preserveDrawingBuffer: true`.
* Compute full-screen `{ cols, rows }` via `FitAddon` and pass `getTtySize: () => ({ cols: term.cols, rows: term.rows })` to `washmhost`, triggering both signal `27` and `28` (`SIGWINCH`) on window resize.




3. **Build the `CRTRenderer` in Three.js r186 (`crt_stage.js`):**
* Initialize `WebGPURenderer` (`await renderer.init()`) and `RenderPipeline`.
* Construct `phosphorMesh` ($128\times128$ bowed grid, $k=0.14$) with the TSL scanline + aperture grille + halation node material bound to `new THREE.CanvasTexture(xtermWebglCanvas)`.
* Construct `glassMesh` ($64\times64$ bowed grid, $k=0.085$, $z=+0.055$) with `MeshPhysicalNodeMaterial` (`transmission: 1.0`, `ior: 1.48`, `dispersion: 1.6`) and the 9-tap parallax-shifted blurry internal reflection `emissiveNode`.
* Add subtle mouse-driven camera parallax ($\pm 1.2^\circ$ tilt) so moving the mouse gently shifts the overhead glass glint and internal reflection, making the glass thickness immediately palpable.


4. **Implement 3D Raycast Pointer & Selection Forwarding:**
* Raycast pointer events on `#crt-stage` onto `phosphorMesh`, invert `hit.uv` to off-screen xterm pixel coordinates, and forward `mousedown`/`mousemove`/`mouseup`/`wheel` events so text selection and scrolling work accurately across the curved surface.



### Phase 2: Floating DOM Blue Panels HUD & Self-Smuggling JS Adapter

1. **Smuggle HUD Controller from Go (`kabibi/ui/filemanager/dom_hud_js.go`):**
* Following the established `eval` bridge philosophy (where `washmhost.js` remains pure and agnostic, and Kabibi smuggles its own UI/AI adapters via `EvalJS`), Kabibi injects `globalThis.__kabibi_hud` on first startup when `OSName == "browser"`.


* The injected adapter builds `#kabibi-dom-hud`, injects the scoped CSS rules (top-drawer spring transition, `.hud-selection-pill` cubic-bezier glide, frosted deep-blue `#000868e0` surface, and responsive multi-column grid), and registers the inline SVG file-icon renderer.


2. **Implement Fast Cursor Glide & Multi-Column Virtualization:**
* In `__kabibi_hud.syncState(state)`, render the Left and Right panes with crisp box-drawing headers, path pills, and multi-column file cells matching `numCols` and `rowsPerCol`.
* In `__kabibi_hud.updateCursor(...)`, update `--cursor-x` and `--cursor-y` on `#hud-cursor-left` / `#hud-cursor-right` so the highlight bar glides smoothly between items without re-rendering DOM nodes.



### Phase 3: `AppWidget` Browser HUD Hook & Uncovered Bottom Gap Alignment (`kabibi/ui/app/app_widget.go`)

1. **Browser Mode Detection in `AppWidget`:**
* Add `isBrowserDOM bool` (set when `syscall.GetPlatformInfo().OSName == "browser"` and `EvalJS` is available).




2. **Coordinated `Render` Path (`AppWidget.Render`):**
* When `w.panelsVisible == true`:
* `w.sh.SetBottomAligned(true)` remains active so the `Shell` stack bottom-aligns into the full $W \times H_{\text{term}}$ `CellBuf`, placing the newest rows in the bottom gap ($H_{\text{panels}} \dots H_{\text{term}}-1$) and sliding older rows up through rows $0 \dots H_{\text{panels}}-1$.


* **If `!w.isBrowserDOM` (Native Terminal / SSH / VS Code Pseudoterminal):** Blit `w.panels.Render(...)` into rows $0 \dots H_{\text{panels}}-1$ of the `CellBuf` as standard TUI cells.


* **If `w.isBrowserDOM` (Full-Screen Browser CRT):** Do *not* overwrite rows $0 \dots H_{\text{panels}}-1$ of the CRT `CellBuf` with TUI panel cells; instead, dim the foreground/background of rows $0 \dots H_{\text{panels}}-1$ by 40% in the `CellBuf` (so the shell history glows softly behind the translucent DOM panels!) and push the `DOMHUDState` to `globalThis.__kabibi_hud`.


* When `w.panelsVisible == false`:
* Call `globalThis.__kabibi_hud.setVisible(false)` so the DOM panels retract smoothly upward out of view, and restore `w.sh.SetBottomAligned(false)`.







### Phase 4: Modal Dialogs (F2/F5/F6/F7/F8) & Verification

1. **Crisp DOM Modal Overlay (Optional Toggle or TUI-in-CRT):**
* Allow F5/F6/F7/F8 file operation dialogs and byte-rate progress bars to either render inside the DOM HUD layer (centered over the dual panes with crisp elevation shadows) when `panelsVisible == true`, or inside the CRT `CellBuf`.




2. **End-to-End Verification Matrix:**
* **60/120 FPS Budget Check:** Verify that when typing rapidly at the `~/path፧` prompt or streaming AI tokens, `xtermTexture.needsUpdate` only fires on dirty frames and `WebGPURenderer` maintains locked frame pacing.


* **Geometric Alignment Check:** Verify that on `1080p`, `1440p`, and `4K` full-screen viewports (and across window resizes), the bottom border of `#kabibi-dom-hud` lands cleanly between row $H_{\text{panels}}-1$ and row $H_{\text{panels}}$ of the curved CRT screen, leaving the 5-row `H_gap` (`BlockBanner` + `PromptInput` + blank footer) completely unobstructed below.


* **Smooth Cursor Glide Check:** Verify that holding `Down`/`Up` or pressing `Left`/`Right`/`PgDn`/`Home`/`End` in the active panel glides the cyan selection bar smoothly across rows and columns while `Tab` transitions active-pane focus and updates the CRT `~/path፧` prompt below on the exact same frame.

# Notes and adjustments

> Few notes for you:
>
> 1. We absolutely MUST review the xterm.js code to figure how to make it help us to generate off-screen surface. If xterm.js is not immediately fit right away, we can even vendor and patch it for that purpose.
> 2. Mouse and keyboard works in the current index.html integration and in Kabibi's browser code. But if that needs to adjust for off-screen full-screen setup, that needs to be explored.
> 3. For non-GPU fallback I propose we simply switch into direct-to-dom full-page rendering in xterm.js with no flourish.

Understood: the xterm.js off-screen surface is an explicit research requirement, and the existing browser input integration must be traced rather than presumed absent. I’ll inspect the actual `index.html`/Kabibi browser path and the pinned xterm.js renderer implementation, then check how the proposed non-GPU fallback fits. No code changes.

The notes above adjust the assessment. The existing integration is real, and your proposed direct-to-DOM fallback is a strong way to preserve a usable terminal when the flourish cannot run.

One correction to my previous review: I was wrong to imply there was no current browser integration. I overlooked the root `index.html`. It currently hosts xterm.js 5.3 beside the README and wires terminal input, output, and sizing to `washmhost`. `Kabibi’s VS Code extension` has another working terminal path through VS Code’s pseudoterminal. Neither is the proposed full-screen CRT yet—the root page’s layout is side-by-side, and the VS Code extension uses VS Code’s own terminal—but they establish input/output behavior we should preserve.

I also reviewed the pinned xterm.js 6.0 source. It gives us a concrete route to the off-screen surface:

- [`WebglRenderer.ts`](https://github.com/xtermjs/xterm.js/blob/6.0.0/addons/addon-webgl/src/WebglRenderer.ts) creates a screen-rendering canvas, requests its WebGL2 context, sizes it with terminal dimensions, and appends it to xterm’s screen element. The canvas is private, not exposed as a public output-surface API.
- The public `WebglAddon.textureAtlas` is only the **glyph atlas**, not the rendered terminal screen. The plan must not use it as `xtermTexture`.
- [`WebglAddon.ts`](https://github.com/xtermjs/xterm.js/blob/6.0.0/addons/addon-webgl/src/WebglAddon.ts) accepts `preserveDrawingBuffer` directly; for this version, `new WebglAddon(true)` is the route to try. That avoids the proposed global `getContext` monkey-patch.
- `Terminal.onRender` exists in xterm 6.0’s public API. We can test whether it is the right update signal for Three’s `CanvasTexture.needsUpdate`. The screen canvas can also be exposed through a small maintained xterm patch—for example, a documented `renderCanvas` getter on the addon—rather than relying on private selectors.
- xterm’s [`DomRenderer.ts`](https://github.com/xtermjs/xterm.js/blob/6.0.0/src/browser/renderer/dom/DomRenderer.ts) identifies the DOM renderer as its standard fallback, and disposing the WebGL addon restores the core renderer. That supports your proposal: if Three or the accelerated path is unavailable, display the same terminal directly in the page, without flourish.

So the off-screen CRT path is **plausible and worth prototyping**, not blocked. The key unknown is whether the exposed/patched xterm canvas can be sampled reliably and efficiently by Three while xterm remains laid out, focused, and interactive.

I’d revise the plan’s order accordingly:

1. **Prototype the xterm surface first:** use the root browser integration as the starting point, move it to a full-page layout, try xterm 6.0’s WebGL renderer with `preserveDrawingBuffer`, and expose the screen canvas intentionally if needed. Put real Kabibi ANSI output on a simple Three plane before adding curvature, glass, or HUD.
2. **Prove off-screen interaction:** preserve the current keyboard and resize paths, then test pointer events, selection, and mouse-reporting after the terminal surface is visually hidden behind the rendered screen. That’s a risk to investigate—not a reason to assume existing input fails.
3. **Implement the direct-DOM fallback:** on initialization failure or loss of the required rendering capability, reveal the same xterm instance as a full-page DOM terminal. Keep the guest running; avoid a second terminal or WASM restart.
4. **Only after those gates pass, add the DOM HUD and visual effects.** Measure frame time and resize behavior before investing in detailed refraction and reflection.

One choice the spec should make explicit: Three’s `WebGPURenderer` may use WebGL2 as a GPU backend, while your fallback is specifically non-GPU direct-to-DOM. The mode selection should distinguish those cases clearly. No files were changed.

> Now do you understand that if we render in WebGL2 with blurs and cuvatures, we need two more things: the camera should point at the curved screen from slightly bottom-right angle, subtly but like in stylish low-angle car posters, plus there should be very subtle blur that keeps most of the screen sharp but the part that is closer to the camera (right-bottom) should be slightly out of focus? Do you reckon you understand that?

Yes. I understand the intended look:

- The camera should view the curved screen from slightly below and to the right, with a restrained perspective—like a stylish low-angle car poster, not a dramatic skew that harms readability.
- The curvature and viewpoint should make the lower-right area feel closer to the camera.
- A subtle, depth-aware focus effect should keep most of the terminal sharp while softening that nearer lower-right area just a little. This is not a general blur or vignette.

The two effects should work together: the screen’s 3D depth and camera position determine where the soft focus falls. In WebGL2, that means using depth-aware post-processing and carefully tuning it so terminal text remains readable. I’ll treat crispness as the default for any DOM HUD unless it’s deliberately placed into that same 3D scene.

> We also want to pause the DOM HUD completely for now, leave it out for the phase 2. Does it make sense?

Yes. Pausing the DOM HUD for phase 2 makes sense: it removes a separate rendering and synchronization problem while we prove the core CRT effect.

I’d make the phase boundary explicit: phase 2 covers the xterm surface, curved screen, low-angle camera, subtle depth-of-field, and preserving terminal input and resize behavior. It excludes the DOM HUD entirely—no overlay, HUD-to-Go synchronization, or alignment work. The HUD can be reconsidered once the terminal rendering path is proven.

