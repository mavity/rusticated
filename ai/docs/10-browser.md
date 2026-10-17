# Kabibi Browser Integration Guide

This CDP suite is a massive multiplier for an on-device model like **Gemma 4 E2B**. Most browser agents fail with compact models because they dump 40,000 tokens of raw HTML into context, click at `(0,0)` before SPAs finish hydrating, or get blocked immediately by bot-detection telemetry.

By shifting **settling (#8)**, **AX-tree trimming & diffing (#1, #9)**, **network delta summarization (#7)**, and **biomimetic input kinematics (#2, #3)** into the deterministic Go tool layer, every browser action becomes an atomic **Action $\rightarrow$ Settle $\rightarrow$ State Delta + Network Delta** transaction that costs only a few hundred tokens per turn.

---

## 1. Critical Rusticated / Mohabbat Trap: The Ghost Process Watchdog

Before touching CDP libraries, there is one **critical architectural interaction** with Mohabbat (`brot` / `washmhost`) that we must handle explicitly:

* **The Trap:** As documented in the Mohabbat architecture, `brot` enforces a strict **OS-Enforced Cascading Shutdown** (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` on Windows and `PR_SET_PDEATHSIG` on Linux) so child processes die when `washmhost` exits.


* **Conflict with Requirement #5:** You want the browser and its tabs to **remain running after Kabibi exits**. If `washmhost` spawns `chrome.exe` normally, Windows will assassinate the entire browser the moment the user closes Kabibi.


* **The Fix:**
1. **Process Breakaway on Spawn:** Launch the browser with `CREATE_BREAKAWAY_FROM_JOB | DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP` on Windows (if the Job Object permits breakaway, or spawn via WMI/Explorer shell delegate `cmd /c start`) and `SysProcAttr{Setsid: true}` on POSIX so Chrome lives in its own independent session outside `brot`’s teardown cascade.


2. **Deterministic DevTools Discovery File:** Launch Chrome/Edge with `--user-data-dir=<cache>/kabibi-browser-profile` and `--remote-debugging-port=0` (or a deterministic port like `9222` backed by a discovery file `<user-data-dir>/DevToolsActivePort` which Chrome writes automatically!).
3. **Re-attachment Across Runs:** When a new or concurrent Kabibi instance starts, it checks `<user-data-dir>/DevToolsActivePort` first. If the WebSocket on `127.0.0.1:<port>` responds to `GET /json/version`, Kabibi attaches to the already-running browser over localhost HTTP/WebSocket without spawning a new process!



---

## 2. Pure-Go CDP Stack Strategy

Since CDP is simply JSON-RPC 2.0 over a localhost WebSocket (`ws://127.0.0.1:<port>/devtools/page/<targetId>`), it requires **zero CGO** and works natively over `rusticated-go`’s network overlay.

| Option | Assessment for Kabibi |
| --- | --- |
| **Custom Thin CDP Client (~300 LOC) + `[github.com/chromedp/cdproto](https://github.com/chromedp/cdproto)` (or `go-rod/rod`)** | **Recommended.** A thin WebSocket JSON-RPC multiplexer (using `nhooyr.io/websocket` or `gobwas/ws`) gives you first-class raw `cdp(method, params)` (#00) for free, while letting you subscribe to `Network.*`, `Page.*`, and `DOM.*` event streams per tab without fighting `chromedp`'s rigid context-cancellation lifecycle. |

### Multi-OS Browser Binary Finder (Chrome & Edge)

Search in priority order (preferring Chrome/Edge stable, then Beta/Canary/Chromium) and verify executable existence:

* **Windows:**
* `%ProgramFiles%\Google\Chrome\Application\chrome.exe`, `%ProgramFiles(x86)%\Google\Chrome\Application\chrome.exe`, `%LocalAppData%\Google\Chrome\Application\chrome.exe`
* `%ProgramFiles(x86)%\Microsoft\Edge\Application\msedge.exe`, `%ProgramFiles%\Microsoft\Edge\Application\msedge.exe`, `%LocalAppData%\Microsoft\Edge\Application\msedge.exe`
* Brave / Vivaldi / Chromium paths in `%LocalAppData%` as tertiary fallbacks.


* **macOS:**
* `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` (and `~/Applications/...`)
* `/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge`
* `/Applications/Chromium.app/...`, `/Applications/Brave Browser.app/...`


* **Linux:**
* `$PATH` lookup + `/usr/bin/`, `/opt/google/chrome/chrome`, `/opt/microsoft/msedge/msedge`, `/snap/bin/chromium`, Flatpak export paths (`google-chrome-stable`, `microsoft-edge-stable`, `chromium-browser`).



---

## 3. Reviewing & Refining Your 10 Browser Tools

### Requirement #5: Mandatory `nickname` & Tab Ownership (Collision-Free Concurrency)

How do we persist `nickname` across separate Kabibi processes so Agent `"researcher"` and Agent `"tester"` never clobber each other even if a page navigates to a new domain?

* **Mechanism 1 (`Target.setAutoAttach` + `window.name` + `Page.addScriptToEvaluateOnNewDocument`):**
When Kabibi claims or creates a tab (`Target.createTarget`) for `nickname: "shopping"`, it evaluates:
1. `Page.addScriptToEvaluateOnNewDocument` injecting `window.__kabibi_nick = "shopping";` so even cross-origin navigations inside that tab retain the nickname!
2. Writes a lightweight registry file `<user-data-dir>/kabibi_tabs.json` mapping `{"shopping": "<targetId>"}`.
3. **Optional Visual Badge in Tab Title:** Inject a tiny title prefix observer (`document.title = "[🤖 shopping] " + document.title`) or favicon badge so the **human user** looking at the browser immediately sees which tab belongs to which Kabibi agent!



---

### Requirement #1 & #9: Heuristically-Trimmed AX Tree & Post-Action Diffing

Raw `Accessibility.getFullAXTree` is noisy, but with four passes it becomes the most token-efficient representation in the industry:

1. **Tag-Name Fallback & Role Pruning:**
* Resolve `backendDOMNodeId` via `DOM.describeNode` (or batched `DOMSnapshot.captureSnapshot`) so any node with `role="generic"` or `role="none"` and an empty name is either collapsed into its single child or labeled with its actual semantic HTML tag (`<header>`, `<nav>`, `<article>`, `<form>`, `<section>`).


2. **Repetition Folding (Lists/Tables/Grids):**
* If a container has $>5$ structurally identical sibling subtrees (e.g., 50 product cards or search results), emit the first 3 with full detail, and if they don't match the current search/focus, summarize or mark the pattern gently unless the AI requests full expansion.


3. **Non-Obvious Geometry Jump Detection:**
* Fetch bounding boxes via `DOMSnapshot.captureSnapshot`. As you walk the AX tree in document order, track the flow cursor $(X_{\text{prev}}, Y_{\text{prev}})$.
* Flag elements whose physical position violates tree hierarchy:
* `[fixed top-right (1180, 16)]` (sticky headers, floating carts, modals)
* `[jump +640px down]` or `[jump -420px up / visual-reorder]`
* `[offscreen / scrolled-out]` or `[occluded by #cookie-banner]` (computable via `DOM.getNodeForLocation` at the element's center!).




4. **Stable Short Ref IDs (`@e12` or Hashline-Style `@14:a3`):**
* Assign every interactive or landmark AX node a short deterministic ref like `[#e14:f2 button "Sign in" (840,310)]` where `f2` is a 1-byte hash of `(role, name, tag, domPath)`.
* **Why this matters for Diffing (#9):**
Before running `click`, `type`, `navigate`, or `js_eval`, snapshot `AXTree_Before`. After **Settling (#8)** finishes, snapshot `AXTree_After`.
* Compute a tree diff keyed by node hash/path:
* **If $<35\%$ of nodes changed** (e.g., clicking "Filter" opened a dropdown or updated a table): Output **only the changed subtrees**, anchored by their parent path!
```text
[ax_diff on tab "shop" | settled in 340ms | 1 subtree updated]
in [#e04 main] > [#e09 region "Filters"]:
  + [#e41 listbox "Sort by" (210,180)]
    + [#e42 option "Price: Low to High" selected]
    + [#e43 option "Newest Arrivals"]

```


* **If $\ge 35\%$ of the tree changed or URL navigated:** Output `[full_page_change: /checkout]` followed by the full trimmed AX tree.





---

### Requirement #2 & #3: Biomimetic Mouse & Keyboard Physics

Anti-bot systems (Cloudflare Turnstile, DataDome, Akamai) check for linear mouse interpolation, zero `mouseMoved` history before `mousePressed`, dead-center `(x + w/2, y + h/2)` clicks, and constant $0\text{ms}$ `keyDown`$\rightarrow$`keyUp` durations.

#### 1. Humane Click (`browser_click`)

* **Target Resolution Escalation:**
Tries CSS selector first (`DOM.querySelector`) $\rightarrow$ `@ref` ID from AX tree $\rightarrow$ Exact AX `name`/`role` $\rightarrow$ Fuzzy AX `name` $\rightarrow$ **EmbeddingGemma 2** semantic match against AX node names/descriptions!


* **2D Gaussian Target Point with Safe Margin:**
Given bounding box $(x, y, w, h)$, clamp to the inner $60\%$ safe box and sample target $(x^*, y^*)$ from a truncated 2D Gaussian centered at $(x + 0.48w, y + 0.52h)$ (humans slightly favor upper-left of center).
* **Fitts’s Law Cubic Bézier + Ornstein-Uhlenbeck Micro-Tremor Trajectory:**
* Start from the tab's persisted `lastMousePos` (or if first interaction on tab, enter from a random point on the top/side viewport perimeter as if moving from the address bar or OS taskbar).
* Generate a cubic Bézier curve with 1–2 randomized control points (slight arc) and a subtle **overshoot-and-correct** hook when distance $D > 300\text{px}$.
* Sample points at 60–120Hz ($8\text{--}16\text{ms}$ ticks) using a minimum-jerk velocity profile ($v(t) \propto 30t^2(1-t)^2$: accelerates smoothly, peaks mid-flight, decelerates as it approaches the target).
* Dispatch `Input.dispatchMouseEvent(type: "mouseMoved", x, y)` along the path—this naturally triggers browser `mouseover` / `mouseenter` / `mouseout` / `mouseleave` events on every intermediate DOM element crossed!


* **Hover-to-Click Reaction Dwell & Hold:**
* Pause at $(x^*, y^*)$ for a log-normal visual confirmation dwell ($65\text{--}160\text{ms}$).
* Dispatch `mousePressed` (with tiny $0\text{--}1\text{px}$ press jitter) $\rightarrow$ hold for $55\text{--}115\text{ms}$ $\rightarrow$ dispatch `mouseReleased`.



#### 2. Humane Type (`browser_type`)

* **Lead-In Focus:** Executes the full biomimetic click on the target input field first, waits a brief hand-repositioning pause ($140\text{--}280\text{ms}$), and starts typing.
* **Digraph-Aware Keystroke Cadence:**
* Maintain a compact lookup of physical QWERTY key coordinates $(row, col, hand)$.
* **Alternating-hand digraphs** (e.g., `e` $\rightarrow$ `n`): faster flight time ($45\text{--}95\text{ms}$) and occasional slight **rollover overlap** (next key `keyDown` occurs $5\text{--}15\text{ms}$ *before* previous key `keyUp`!).
* **Same-finger / distant keys:** slower flight time ($110\text{--}210\text{ms}$).
* **Word & Punctuation Rhythm:** Spaces have a distinct pre-word rhythm ($80\text{--}150\text{ms}$); after `,`, `.`, `?`, `;`, or `<Enter>`, inject a stochastic cognitive linger ($220\text{--}550\text{ms}$).


* **Explicit Shift & Symbol Chord Decomposition:**
Never inject uppercase or shifted symbols (`A`, `*`, `@`, `?`) as synthetic `insertText`. Instead, decompose them into realistic hardware chords:
1. `Input.dispatchKeyEvent(type: "rawKeyDown", key: "Shift", code: "ShiftLeft", modifiers: 8)`
2. Delay $25\text{--}65\text{ms}$
3. `Input.dispatchKeyEvent(type: "keyDown", key: "*", code: "Digit8", text: "*", modifiers: 8)`
4. Hold $50\text{--}95\text{ms}$
5. `Input.dispatchKeyEvent(type: "keyUp", key: "*", code: "Digit8", modifiers: 8)`
6. Delay $15\text{--}45\text{ms}$
7. `Input.dispatchKeyEvent(type: "keyUp", key: "Shift", code: "ShiftLeft", modifiers: 0)`



---

### Requirement #7 & #8: Dual-Signal Settling & Token-Cheap Network Summary

#### 1. The Settling Engine (#8)

After any action (`navigate`, `click`, `type`, `js_eval`), enter the `WaitForSettle(ctx, tab)` loop:

* **Initial Grace Delay:** Wait $200\text{ms}$ so synchronous JS handlers and `fetch()` calls can kick off.
* **Signal A — Network Quiescence:**
Track active requests via `Network.requestWillBeSent` minus (`Network.loadingFinished` + `Network.loadingFailed`). Ignore long-lived streaming types (`EventSource`, `WebSocket`, or requests open $>3\text{s}$ that look like analytics long-polling). Network is settled when `inFlightDocumentOrXHR == 0` for a continuous quiet window of **$250\text{ms}$**.
* **Signal B — AX / DOM Layout Quiescence:**
Poll the lightweight AX/DOM structural hash every **$75\text{ms}$**. Visual state is settled when two consecutive snapshots ($150\text{ms}$) have `0` structural/bounding-box diffs.
* **Shared Deadline:** Cap total settle wait at `max_settle_ms` (default `4000ms` for clicks/typing, `8000ms` for navigation). If a live ticker/carousel keeps mutating 1 node forever, exit cleanly on timeout and mark `[settled: partial (1 animated region)]`.

#### 2. High-Signal Network Activity Summary (#7)

During the action + settle window, record all network events in a per-action buffer and append a compressed summary block to the tool output:

* **Origin Stripping:** If page is on `[https://api.example.com/v1/users](https://api.example.com/v1/users)`, format same-origin URLs as `/v1/users`.
* **Static Asset Grouping:** Collapse `Document`, `Stylesheet`, `Image`, `Font`, `Script` `GET 200` requests into a single counter line:
`GET [14 static assets: 6 img, 5 js, 3 css -> 200 OK (412KB)]`
* **API / XHR / Fetch / Non-200 Inspection:**
For `Fetch`/`XHR` or any request with a payload or status $\ge 400$, show method, path, status, and truncated request/response bodies (capped at ~200 chars with `…`):
```text
[net: 16 reqs in 310ms]
  POST /api/v2/cart/add -> 200 (84ms)
    req: {"sku":"KB-99","qty":1}
    res: {"ok":true,"cart_total":49.99,"items":1}
  GET  /api/v2/recommendations?sku=KB-99 -> 503 (120ms)
    res: {"error":"service temporarily unavailable"}
  GET  [14 static assets -> 200 OK]

```


*Think about how powerful this is:* If a button click silently fails in a React app because an API returned `403` or `500`, the AI sees the exact JSON error immediately in the `browser_click` response without ever opening DevTools!

---

### Requirement #4: Screenshot with Pure-Go Browser Chrome Adornment

* **Capture:** Use `Page.captureScreenshot` (supports full viewport, `captureBeyondViewport: true` for full page, or `clip` to a target element's bounding box).
* **Pure-Go Chrome Frame Renderer (`adorn: true`):**
Using only Go's standard `image/draw` and `golang.org/x/image/font/basicfont` (or an embedded bitmap/TTF font), prepend a clean **44px top browser bar** onto the PNG before saving:
* Draws the classic macOS/modern traffic light dots (`🔴 🟡 🟢`), navigation glyphs (`← → ↻`), a rounded address pill displaying `🔒 [https://current-domain.com/path](https://current-domain.com/path)`, and a right-hand badge `[🤖 nickname]`.
* Also optionally feeds into Kabibi's `ocr4` TFLite pipeline if the AI wants text extracted from a canvas/WebGL element!





---

### Bonus Answer to Your Open Question: *"Can we get it obeying anything besides AI tools?"*

Since Kabibi is both an interactive shell (`mvdan.cc/sh`) and an AI supervisor, you can expose the **exact same engine** to the human user in two effortless ways:

1. **A `web` / `cdp` Shell Builtin:** Let the user (or shell scripts!) run `web open https://...`, `web shot out.png --adorn`, `web ax`, or `web click "Sign in"` right from the `~/path፧` prompt!


2. **Live Console / Network Tail in a Shell Block:** The user can run `web logs --nick main` in the Kabibi shell to watch the live network/console stream from the AI's browser tab inside a terminal block while chatting with the AI.

---

## 4. Proposed Tool Consolidation (7 Tools Total)

To keep tool schemas cheap in Gemma 4's context window, we can expose your 10 capabilities as **7 laser-focused tools** (where #7 Network Summary, #8 Settling, and #9 AX Diff run automatically inside every interactive tool):

| Tool Name | Covers Your Points | Key Parameters (`nickname` required on all) |
| --- | --- | --- |
| **`browser_navigate`** | #0, #5, #7, #8, #9 | `nickname`, `url`, `back`/`forward`/`reload` (opt) |
| **`browser_ax`** | #1, #5 | `nickname`, `search` (opt: exact $\rightarrow$ regex $\rightarrow$ EmbeddingGemma 2), `root_selector` (opt) |
| **`browser_click`** | #2, #5, #7, #8, #9 | `nickname`, `target` (CSS, `@ref`, or AX name), `button` (`left`/`right`/`double`) |
| **`browser_type`** | #3, #5, #7, #8, #9 | `nickname`, `target` (CSS, `@ref`, or AX name), `text`, `clear_first` (bool), `press_enter` (bool) |
| **`browser_screenshot`** | #4, #5 | `nickname`, `save_path`, `target` (opt element), `full_page` (bool), `adorn_chrome` (bool) |
| **`browser_eval`** | #6, #5, #7, #8, #9 | `nickname`, `expression` (JS snippet, auto-awaited if Promise) |
| **`browser_cdp`** | Raw CDP escape hatch | `nickname`, `method` (e.g. `"Network.getCookies"`), `params` (JSON object) |

> I think the tab relationship needs to be stored in browser itself somehow. Look if CDP allows some kind of storage in-browser. If not, consider alternatives, maybe open hidden about:blank or web worker or something of the sort, and use some sort of localStorage in that context? Think!
>
> Also don't include angle brackets in HTML tags, header is fine no need to make it three times more token as \<header\>.
>
> Repetition of structurally identically siblings is completely out of question. The repetition we allow to fold is when parent contains the same name/text as child, or all the children textually are in the parent in their lexical order (that means parent's text is redundant and repeats the children's) - in that case parent simply loses text designation and AI reads the text from individual children. We absolutely need to retain a set of 50 elements inside the parent, because those are actual data entries that matter.
>
> Agree: if the next element's nearest point to the previous element is out of certain distance, reflect both's bounds inside the AX tree. And use heuristics to find if elements' width and height matches its text plus-minus a small space - if so, only represent location not actual size.
>
> Stable short ref IDs - rejected. These are going to undergo constant churn in many modern frameworks. Let the AI ask what to click as a user would ask 'Click <button caption>'. AX tree tends to reflect human-approachable content.
>
> To the network traffic collapsion: also for any URLs outside of the current origin, group them per origin and allow 2 URLs max, otherwise show ellipsis and count.
>
> And for any long URLs truncate.
>
> The frame renderer - we will not accept clunky frames. Either it should be rendered a-la iPhone 1 skeumorphic plus rounded borders plus blur shadow, or we don't want it. I think HTML already provides a way to generate precise artwork and CSS comes with all tools included. But if it's excessive we can leave it aside for now. Corny MSPaint-like pseudo-screenshots will not be accepted.

I think the tab relationship needs to be stored in browser itself somehow. Look if CDP allows some kind of storage in-browser. If not, consider alternatives, maybe open hidden about:blank or web worker or something of the sort, and use some sort of localStorage in that context? Think!

Also don't include angle brackets in HTML tags, header is fine no need to make it three times more token as <header>.

Repetition of structurally identically siblings is completely out of question. The repetition we allow to fold is when parent contains the same name/text as child, or all the children textually are in the parent in their lexical order (that means parent's text is redundant and repeats the children's) - in that case parent simply loses text designation and AI reads the text from individual children. We absolutely need to retain a set of 50 elements inside the parent, because those are actual data entries that matter.

Agree: if the next element's nearest point to the previous element is out of certain distance, reflect both's bounds inside the AX tree. And use heuristics to find if elements' width and height matches its text plus-minus a small space - if so, only represent location not actual size.

Stable short ref IDs - rejected. These are going to undergo constant churn in many modern frameworks. Let the AI ask what to click as a user would ask 'Click <button caption>'. AX tree tends to reflect human-approachable content.

To the network traffic collapsion: also for any URLs outside of the current origin, group them per origin and allow 2 URLs max, otherwise show ellipsis and count.

And for any long URLs truncate.

The frame renderer - we will not accept clunky frames. Either it should be rendered a-la iPhone 1 skeumorphic plus rounded borders plus blur shadow, or we don't want it. I think HTML already provides a way to generate precise artwork and CSS comes with all tools included. But if it's excessive we can leave it aside for now. Corny MSPaint-like pseudo-screenshots will not be accepted.

