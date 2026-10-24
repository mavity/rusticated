# Kabibi LLM Onboarding, Hardware Calibration, and Multi-Provider Integration

Here is the complete, end-to-end engineering specification and implementation plan for **Kabibi LLM Onboarding, Hardware Calibration, and Multi-Provider Integration**.

---

## 1. Architectural Overview & Core Rules

Currently, `LazyAIRunner` in Kabibi calls `assets.EnsureRuntime`, `assets.EnsureDefaultLLMModel`, and `assets.EnsureModel` unconditionally upon the first natural-language turn (`RouteAI`). This plan replaces that opaque bootstrap with a **user-consented, hardware-aware onboarding pipeline** governed by four strict rules:

1. **Zero Silent Downloads:** No network transfer of `.litertlm` weights or runtime wheels happens without explicit user confirmation of the byte size and destination path.
2. **Implied `EmbeddingGemma 2` Bundling:** Selecting any local Gemma 4 tier automatically includes `embeddinggemma-2-text-270m.litertlm` (~164 MB) in the total download footprint and single consent prompt.


3. **Hardware-Calibrated Local Tiers:** Kabibi probes host memory and GPU/WebGPU limits at runtime to recommend the optimal Gemma 4 variant (`E2B`, `E4B`, `12B`, `26B MoE`, or `31B Dense`).


4. **First-Class Cloud Provider Support:** Users can bypass local downloads entirely and authenticate via **OAuth 2.0 Device Flow** (GitHub Copilot, Google) or **Personal Access Tokens (PAT)** (OpenRouter, Anthropic Claude, Google Gemini, OpenAI), persisted via `[github.com/zalando/go-keyring](https://github.com/zalando/go-keyring)` (which Mohabbat transparently overlays in WASM).



---

## 2. Package Layout & New Components

```text
ai/
├── assets/
│   ├── catalog.go           # Multi-tier Gemma 4 URL/size registry (Desktop + Web)
│   └── sysinfo.go           # Cross-platform RAM, VRAM, and WebGPU limit prober
├── providers/
│   ├── provider.go          # Unified engineBackend & conversationBackend interfaces
│   ├── litert/              # Existing native & web LiteRT-LM backends
│   └── remote/              # HTTP/SSE streaming client for OpenRouter, Claude, Gemini, Copilot
└── auth/
    ├── device_flow.go       # RFC 8628 OAuth Device Code loop (Copilot / Google)
    └── pat.go               # Masked terminal input & keyring persistence
kabibi/
├── config/
│   └── ai_config.go         # ~/.config/kabibi/ai.json state & consent persistence
└── ui/shell/
    └── ai_onboarding.go     # Interactive inline TTY onboarding wizard & `ai setup` builtin

```

---

## 3. Phase-by-Phase Implementation Plan

### Phase 1: Model Catalog & Hardware Calibration (`ai/assets`)

#### 1.1 Multi-Tier Gemma 4 Registry (`ai/assets/catalog.go`)

Replace the single hardcoded `DefaultLLMModelURL` constant with a structured catalog supporting both native `.litertlm` and browser `-web.litertlm` targets:

| Tier ID | Display Name | Architecture | Approx. `.litertlm` Size | WebGPU (`-web`) Support | Min Recommended Memory |
| --- | --- | --- | --- | --- | --- |
| `gemma-4-e2b` | Gemma 4 E2B | 2B Dense + ViT | ~2.04 GB

 | Yes

 | $\ge$ 4 GB RAM / 2 GB VRAM |
| `gemma-4-e4b` | Gemma 4 E4B | 4B Dense + ViT | ~2.80 GB

 | Yes

 | $\ge$ 8 GB RAM / 4 GB VRAM |
| `gemma-4-12b` | Gemma 4 12B | 12B Dense | ~6.20 GB | Experimental | $\ge$ 16 GB RAM / 8 GB VRAM |
| `gemma-4-26b-a4b` | Gemma 4 26B MoE | 26B MoE (3.8B active) | ~14.20 GB | Desktop Only | $\ge$ 24 GB RAM / 16 GB VRAM |
| `gemma-4-31b` | Gemma 4 31B | 31B Dense | ~18.50 GB | Desktop Only | $\ge$ 32 GB RAM / 24 GB VRAM |

* **Bundled Embedder Constant:**
`DefaultEmbeddingModelURL` (`embeddinggemma-2-text-270m.litertlm`, `164,626,432` bytes) is automatically appended to the download queue whenever any `gemma-4-*` tier is chosen.



#### 1.2 Hardware Prober (`ai/assets/sysinfo.go`)

Implement `ProbeHardware() HardwareProfile`:

```go
type HardwareProfile struct {
    HostOS           string // "linux", "darwin", "windows", "browser"
    TotalRAMBytes    uint64
    AvailableVRAM    uint64 // 0 if unknown/shared
    WebGPUMaxBinding uint64 // Populated when HostOS == "browser"
    HasWebGPU        bool
    RecommendedTier  string // e.g., "gemma-4-12b"
}

```

* **Native Desktop (`HostOS() != "browser"`):**
* **Linux:** Read `/proc/meminfo` (`MemTotal`) and check `/sys/class/drm/card*/device/mem_info_vram_total` (or `nvidia-smi` fallback if present).
* **macOS:** Query `sysctl hw.memsize` (Apple Silicon unified memory allows ~70% allocation to Metal).
* **Windows:** Call `GlobalMemoryStatusEx` (or read host environment metrics exposed by `washmhost`).


* **Browser (`HostOS() == "browser"`):**
* Use the existing virtual `eval` dylib tunnel (`syscall.DylibOpen("eval")`) to execute a single probe snippet:


```javascript
(async () => {
  const mem = navigator.deviceMemory || 4;
  if (!navigator.gpu) return JSON.stringify({ ramGB: mem, webgpu: false });
  const adapter = await navigator.gpu.requestAdapter();
  return JSON.stringify({
    ramGB: mem,
    webgpu: !!adapter,
    maxBinding: adapter ? adapter.limits.maxStorageBufferBindingSize : 0,
    maxBuffer: adapter ? adapter.limits.maxBufferSize : 0
  });
})()

```


* If `webgpu == false` or `maxBuffer < 2.2 GB`, mark local browser tiers as unsupported and recommend Cloud APIs.



---

### Phase 2: Configuration & Secret Persistence (`kabibi/config` & `keyring`)

#### 2.1 Configuration Schema (`~/.config/kabibi/ai.json`)

Store non-sensitive onboarding state in the user's config directory (backed by host disk on desktop, or `AsyncIndexedDBFS` in browser):

```json
{
  "configured": true,
  "consent_given": true,
  "mode": "local",
  "local": {
    "tier": "gemma-4-12b",
    "embedder": "embeddinggemma-2-270m"
  },
  "remote": {
    "provider": "openrouter",
    "model": "anthropic/claude-3.7-sonnet",
    "account": "default"
  }
}

```

#### 2.2 Credential Management via `zalando/go-keyring`

Following the settled Mohabbat overlay architecture:

* Kabibi imports standard `[github.com/zalando/go-keyring](https://github.com/zalando/go-keyring)`.


* All API tokens and OAuth tokens are stored under:
* `service`: `"kabibi:ai:" + provider` (e.g., `"kabibi:ai:openrouter"`, `"kabibi:ai:copilot"`, `"kabibi:ai:anthropic"`, `"kabibi:ai:gemini"`).
* `user`: `"default"` (or user-specified profile name).


* Secrets are **never** written to `~/.config/kabibi/ai.json`, shell history (`CommandRecord`), or LLM context.



---

### Phase 3: Interactive Onboarding Wizard (`kabibi/ui/shell/ai_onboarding.go`)

#### 3.1 Trigger Points

1. **Lazy First-Use Gate:** When `ClassifyInput` routes a prompt to `RouteAI` and `ai.json` has `configured == false` (and no pre-existing weights in `~/.cache/kabibi/litert_cache/`):


* Intercept the turn before calling `EnsureRuntime` or `EnsureModel`.


* Launch `RunOnboardingWizard(ctx, ioEnv)` directly in the active shell stream.


* Once the wizard completes (and downloads/authenticates), immediately resume and answer the user's original prompt!


2. **Explicit Shell Command:** Register `ai setup` (or `kabibi --ai-setup`) so users can re-run onboarding or switch providers at any time.



#### 3.2 Step-by-Step Terminal UX Flow

**Step A: Main Selection Menu**

```text
╭─ Kabibi AI Onboarding ──────────────────────────────────────────────────────╮
│ Hardware Detected: 24.0 GB RAM • GPU Acceleration Available                 │
╰─────────────────────────────────────────────────────────────────────────────╯
Select how Kabibi should run AI features:

  Local On-Device (100% private, offline, includes EmbeddingGemma 2 [164 MB]):
    [1] Gemma 4 E2B      (~2.04 GB + 164 MB)  Fast & light (4 GB+ RAM)
    [2] Gemma 4 E4B      (~2.80 GB + 164 MB)  Balanced (8 GB+ RAM)
  ★ [3] Gemma 4 12B      (~6.20 GB + 164 MB)  Recommended for your hardware
    [4] Gemma 4 26B MoE  (~14.2 GB + 164 MB)  Fast MoE workstation (24 GB+ RAM)
    [5] Gemma 4 31B      (~18.5 GB + 164 MB)  Dense workstation (32 GB+ RAM)

  External Cloud Provider (0 MB local LLM download):
    [6] Sign in with OAuth (GitHub Copilot / Google)
    [7] Enter API Key / PAT (OpenRouter, Claude, Gemini, OpenAI)

  Disable:
    [0] Shell-only mode (do not configure AI now)

Choice [0-7] (Enter for 3): 

```

**Step B1: Local Consent & Consolidated Download (If Options 1–5 Selected)**

```text
You selected: Gemma 4 12B + EmbeddingGemma 2
  • Gemma 4 12B weights:        6,204 MB
  • EmbeddingGemma 2 weights:     164 MB
  • LiteRT-LM runtime library:     42 MB
  • Total download size:        6,410 MB
  • Cache location:             ~/.cache/kabibi/litert_cache/

Authorize download of 6.41 GB? [Y/n]: 

```

* If `Y`: Set `consent_given = true`, save config, and run `EnsureRuntime`, `EnsureLLMModel(tier)`, and `EnsureEmbeddingModel` with live `\r` progress bar updates streamed to the terminal.


* If `n`: Return to the main menu without downloading a single byte.

**Step B2: OAuth Device Flow (If Option 6 Selected)**

1. Prompt for provider: `[1] GitHub Copilot` or `[2] Google Gemini OAuth`.
2. Initiate RFC 8628 Device Authorization Request (`POST [https://github.com/login/device/code](https://github.com/login/device/code)`).
3. Print instructions and attempt to open the system browser (via `xdg-open` / `open` / `rundll32` on desktop, or `window.open` via the `eval` dylib in browser mode):


```text
Authenticate with GitHub Copilot:
  1. Visit:  https://github.com/login/device
  2. Code:   WDJB-MJHT

Waiting for authorization... (Press Ctrl+C to cancel)

```


4. Poll the token endpoint every `interval` seconds.
5. On success, store the token via `keyring.Set("kabibi:ai:copilot", "default", token)`, optionally ask whether to download `EmbeddingGemma 2` (164 MB) for local associative memory, and write `ai.json`.



**Step B3: Personal Access Token Flow (If Option 7 Selected)**

1. Prompt for provider: `[1] OpenRouter`, `[2] Anthropic Claude`, `[3] Google Gemini`, `[4] OpenAI`.
2. Read the key with terminal echo disabled (rendering `•` per character or silent raw mode) so it never leaks to scrollback.


3. Validate the key with a lightweight HTTP request (`GET /v1/models` or equivalent).
4. Save to `keyring.Set("kabibi:ai:"+provider, "default", apiKey)`.


5. Present a curated model picker for that provider (with an option to type any custom model ID) and save to `ai.json`.

---

### Phase 4: Remote Provider Backend (`ai/providers/remote`)

To make remote LLMs drop-in compatible with `ai.Session`, implement `engineBackend` and `conversationBackend` in `ai/providers/remote`:

1. **Unified Interface Compatibility:**
* `ai.Session` continues to compile Kabibi's declarative tools (`exec`, `read_file`, `write_file`, `list_dir`, `shell_history`, `shell_state`, `deliberate`, and `browser_*`) with the auto-injected `explanation` property.


* The remote backend translates `ai.ToolDeclaration` into:
* **OpenAI / OpenRouter / Copilot format:** `tools: [{ type: "function", function: { name, description, parameters } }]`.
* **Anthropic Messages API format:** `tools: [{ name, description, input_schema }]`.
* **Gemini GenerateContent format:** `tools: [{ functionDeclarations: [...] }]`.




2. **SSE Streaming & Tool Call Reassembly:**
* Stream `text/event-stream` responses over standard `net/http` (which works natively on desktop and maps over `rusticated-go`'s fetch overlay in WASM).


* Emit delta text chunks and assembled tool calls in the exact JSON chunk format expected by `ai.Session`.




3. **`Conversation.Clone()` Support:**
* Since cloud APIs are stateless per HTTP request (history is passed in the `messages` array), `Clone()` simply deep-copies the in-memory `[]Message` slice and tool config—making `deliberate` work instantaneously with zero extra plumbing.




4. **Embedder Behavior in Remote Mode:**
* If the user opted into the 164 MB `EmbeddingGemma 2` download (or already had it cached from a local tier), `ai.Session` uses local `EmbeddingGemma 2` for zero-latency compaction and semantic search.


* If `Embedder == nil` (user opted out of local downloads and provider has no embeddings endpoint, e.g., Anthropic), `ai.Session` cleanly skips vector-similarity anchor selection and falls back to recency windowing and regex/literal tool search.



---

## 4. Execution & Testing Checklist

| Step | Deliverable | Verification Method |
| --- | --- | --- |
| **1** | `ai/assets/catalog.go` & `sysinfo.go` | Unit test `ProbeHardware()` tier selection across mocked 4 GB, 8 GB, 16 GB, 24 GB, and 32 GB profiles, plus browser `eval` JSON parsing. |
| **2** | `kabibi/config/ai_config.go` | Unit test config load/save, verifying default `configured: false` state blocks automatic downloads. |
| **3** | `ai/auth` (`pat.go` & `device_flow.go`) | Test RFC 8628 polling loop against a mock HTTP server and verify `keyring.Set` / `keyring.Get` storage. |
| **4** | `ai/providers/remote` | Test SSE chunk parsing, tool-call JSON reassembly, and `Clone()` deep-copying for OpenRouter, Anthropic, Gemini, and Copilot formats. |
| **5** | `kabibi/ui/shell/ai_onboarding.go` | E2E terminal test: type `"Name a bridge in Paris"` on a clean profile $\rightarrow$ verify onboarding menu appears before any HTTP download $\rightarrow$ select option $\rightarrow$ verify prompt completes. |


# Notes

These six notes cut straight through the junior team's objections and turn every single "workaround" into a clean, foundational architectural primitive. Let's reassess each point in detail, including the real trap hiding inside Note #2 (and how to make the proxy trick work cleanly).

---

### 1. First-Class `ui.Widget` with a High-Level API (Not Hard-Wired Shell Logic)

**Assessment: Much cleaner and 100% aligned with `ui.Host` + `AppWidget`.**

In Kabibi's UI architecture, `ui.Host` runs the unified `CellBuf` diff engine (`spanDetect` + relative cursor moves), and composite containers (`AppWidget` / `Shell`) delegate rendering and key routing to child `ui.Widget` instances (just like `PromptInput`, `DualPaneWidget`, and the ported `dialog.go` / `editor.go`).

Instead of cluttering `Shell` with onboarding state machines or low-level key handlers:

* We build a reusable, self-contained **`ui.Widget`** (e.g., `ui/wizard` or `ui/form`) that handles its own rendering, arrow-key navigation, radio/list selection, masked password input (`••••`), and progress bars inside the `CellBuf` stack.


* It exposes a **high-level declarative Go API** to the caller:
```go
result, err := wizard.Run(ctx, WizardSpec{...})

```


* When `Shell` (or `ai setup`) invokes it, `Shell` simply mounts the widget as the active child slot in its layout stack—forwarding `KeyEvent`s to `activeWidget.HandleKey(ev)` and blitting `activeWidget.Render(w, h)` right above the footer—until the widget resolves its channel/promise. Zero `os.Stdin` races, zero custom key-routing spaghetti in `Shell`!



---

### 2. Emulated Single-Host HTTP Proxy on the Rusticated Network ABI (Browser Mode)

**Assessment: Brilliant idea—with one critical TLS/`CONNECT` subtlety we must get right.**

Let's compare how Go's standard `net/http` behaves when `HTTP_PROXY` / `HTTPS_PROXY` is advertised (e.g., `[http://127.0.0.1:8080](http://127.0.0.1:8080)` or `[http://proxy.rusticated:80](http://proxy.rusticated:80)`):

* **For plain `http://` URLs:** Go `net/http` connects to the proxy over TCP and sends `GET [http://example.com/path](http://example.com/path) HTTP/1.1`. That is plain text HTTP.
* **The Catch — For `https://` URLs (OpenRouter, Anthropic, Gemini, HuggingFace):**
When `HTTPS_PROXY` is set, Go's `net/http.Transport` sends `CONNECT openrouter.ai:443 HTTP/1.1` to the proxy, and **then Go immediately starts a raw binary TLS 1.3 handshake (`crypto/tls`) over that TCP stream** expecting raw end-to-end encrypted TCP packets from `openrouter.ai:443`!


* In a browser tab, JavaScript **cannot** open a raw TCP socket to `openrouter.ai:443` to forward raw TLS records; the browser only gives JS `fetch()` (where the browser *itself* terminates TLS).



**How to make the Single-Host Proxy trick work effortlessly:**
We have two ways to make `net/http` work transparently over that single virtual host (`127.0.0.1:8080`) in `washmhost/js/browser/index.js`:

1. **Option 2A — Virtual Plain-HTTP Reverse Gateway (Simplest — ~60 LOC in JS):**
* The browser `washmhost.js` emulates TCP listener `127.0.0.1:80` (already resolved by `net_lookup`!).
* Inside the guest, when `net/http` sends an HTTP request to `[http://127.0.0.1:80/https://openrouter.ai/api/v1/](http://127.0.0.1:80/https://openrouter.ai/api/v1/)...` (or via a 5-line `http.RoundTripper` wrapper on `http.DefaultTransport` that rewrites scheme `https://host/path` $\rightarrow$ plain HTTP `GET /POST` to `127.0.0.1:80` with `X-Target-URL: https://...`), Go speaks **plain unencrypted HTTP/1.1 over the emulated TCP socket** to `127.0.0.1:80`.
* The JS virtual host parses the HTTP request from `sys_write`, executes browser `fetch(targetUrl, { method, headers, body })` (so the **browser** handles TLS + CORS + streaming `ReadableStream`), and streams the HTTP/1.1 response status, headers, and SSE chunks back through `sys_read`!




2. **Option 2B — MITM `CONNECT` Termination in the Virtual Proxy (Harder):**
* If you want `HTTPS_PROXY=[http://127.0.0.1:8080](http://127.0.0.1:8080)` with zero `RoundTripper` hook, the JS proxy would have to terminate Go's TLS handshake after `CONNECT` using a dummy leaf cert and force `net_cert_verify` in browser mode to accept it—which means bundling a TLS server implementation in JS.





**Verdict on #2:** **Option 2A** gives you 100% standard `net/http` (including streaming `resp.Body.Read` for SSE and model downloads!) across the entire Go userland (`ai`, `u-root`'s `wget`/`curl`, etc.) with **~60 lines of JS in `washmhost.js**` and no `EvalJS` hacks in `ai`!

---

### 3. Moving Serialization Below the Backend Boundary

**Assessment: Confirmed & Locked In.**

* `Engine.NewConversationWithConfig(cfg ConversationConfig)` and `Conversation.SendStream(ctx, parts []PromptPart)` will pass structured `ConversationConfig` and `[]PromptPart` directly into `engineBackend` and `conversationBackend`.


* We add `ID string` to `ToolCall` and `ToolCallID string` to `PromptPart`.


* Each backend (`backend_rusticated.go` for native LiteRT-LM, `backend_web_rusticated.go` for browser LiteRT-LM, and `backend_remote.go` for cloud APIs) owns its own wire JSON marshaling and unmarshaling.

---

### 4. Status of `syscall.DylibOpen`

**Assessment: Clarifying the two different things we discussed previously.**

In Source [4] (our earlier keyring chat), we retired the idea of **abusing `DylibOpen` for non-DLL platform services like Keyring or Browser `eval**`. Specifically:

1. **For Browser `eval`:** `DylibOpen("eval")` *was* retired and replaced by the virtual character device **`/dev/eval`** (`EvalJS` in `ai/assets/eval_bridge_rusticated.go`). So nothing in browser mode touches `DylibOpen` anymore.


2. **For Native C Shared Libraries on Desktop (`litert_lm_ext.dll` / `.so` / `.dylib`):** `syscall.DylibOpen` / `sys_dylib_open` still exists in `rusticated-go/syscall` and `washmhost/env_dylib.go` strictly to talk to the isolated **`washmhost --dylib-satellite`** process that loads the native LiteRT-LM C++ shared library via `purego`.



* **Bottom line for our plan:** We never reference `syscall.DylibOpen` for browser `eval`, keyring, or hardware probing.

---

### 5. Platform Extensions: Universal, OS-Agnostic Resource Metrics

**Assessment: 100% Agreed. No OS-specific gruel (`wmic`, `/proc/meminfo`, `sysctl`) in userland.**

If a guest application (whether Kabibi choosing an LLM tier, `u-root`'s `free`/`top` commands, or a build tool sizing worker pools) needs to know the machine's capacity, that is a **universal, OS-independent functional capability** of any computing host:

* **Total & Available System Memory (bytes):** `TotalMemory uint64`, `AvailableMemory uint64`
* **Compute Accelerator Memory / Tier (bytes & capabilities):** `AcceleratorMemory uint64` (VRAM on discrete GPU, unified Metal/NPU pool on Apple Silicon, or `maxBufferSize` on WebGPU).

Because every single host target (`linux`, `darwin`, `windows`, and `browser`) has these exact concepts, exposing them cleanly in `syscall.PlatformInfo` (or a universal `sys_resource_info` / `GetSystemResources()` call in the rusticated ABI) means:

* Userland Go code writes **one single 5-line function** with zero OS switches, zero shell-outs, and zero `/proc` parsing.

---

### 6. `EmbeddingGemma 2` Coexistence via Post-Processing at Rest (Load-on-Idle / Unload-on-Wake)

**Assessment: This resolves the memory ceiling on both low-RAM desktops and browsers!**

As designed in our Associative Memory workshop, `EmbeddingGemma 2` (270M, ~164 MB) and offline cluster summarization do their heavy lifting **at rest during the wall-clock quiescence / "sleep" phase**, *not* simultaneously with live token generation.

* Even on constrained hardware (e.g., 4–8 GB RAM or browser WebGPU with strict buffer limits), `EmbeddingGemma 2` and `Gemma 4` **never need to occupy GPU/WASM memory at the same instant** if memory is tight:


1. When the user selects any local Gemma 4 tier during onboarding, we **always bundle `EmbeddingGemma 2` (+164 MB)** into the single consented download.
2. During active chat turns, `Gemma 4` holds the accelerator.
3. During quiescence/at-rest post-processing (or on memory-constrained hosts), the session can lazily load `EmbeddingGemma 2` to compute pending unit embeddings, and—if `AvailableMemory` is tight—unload/park weights between phases so peak resident memory never exceeds `max(Size(Gemma4), Size(EmbeddingGemma2))`.





---

### Summary of the Revised Direction

1. **Onboarding UI:** A reusable, high-level `ui.Widget` (`WizardWidget`) mounted cleanly in `Shell` (no `os.Stdin` touching, no hard-wired shell key loops).


2. **Browser Networking:** Emulated single-host HTTP proxy on the existing Rusticated `net_open`/`sys_read`/`sys_write` ABI in `washmhost.js` so standard Go `net/http` works transparently in the browser.


3. **Backend Boundary:** Structured `ConversationConfig` and `[]PromptPart` (with `ToolCall.ID`) passed directly to backends; LiteRT-LM JSON serialization lives strictly inside the LiteRT backends.


4. **No `DylibOpen` for Browser/Platform Services:** Retired for non-C-FFI uses.


5. **OS-Agnostic Host Resource ABI:** Expose universal memory/accelerator capacity metrics in the Rusticated platform info ABI rather than per-OS userland hacks.
6. **Universal `EmbeddingGemma 2` Coexistence:** Always bundled with local Gemma 4; operates at rest with sequential load/unload fallback when host memory is constrained.

# Notes: comments

> I agree on the HTTP stack it becomes too complex, maybe for now we embed custom browser-http inside the ai package and switch to it when the brain is operating inside browser. Success? Yes. Easier but less future proof. OK.
>
> I confirm that we've implemented dload satellite instead of washmhost. Is that dload not fully integrated yet? It is implemented as a side load for ai package only, not as part of rusticated baseline platform. But it should not touch browser/eval flow.
>
> For external (GPU/NPU/other) memory I suggest we produce a tagged set of several values (maybe 5-16 values for future extension) and for each we allow 256-value tag that is meant to explain what KIND of memory that value represents (0 means none). That way we can pass both GPU and NPU and even allocate one for shared GPU/NPU too. The browser might even try to show that if it can (but less important as browser logic already has eval to do all that if needed).

These decisions lock every single piece into place with zero over-engineering. Let's review how these three pieces fit together right now:

---

### 1. In-Package Browser HTTP Client Inside `ai` (`ai/http_browser.go`)

**Verdict: Pragmatic, self-contained, and works today.**

Instead of building a virtual TCP-to-HTTP proxy in `washmhost.js`, `ai` keeps a small internal HTTP/SSE transport abstraction:

* **Desktop (`assets.HostOS() != "browser"`):** Uses standard Go `net/http` (`http.DefaultClient`).


* **Browser (`assets.HostOS() == "browser"`):** Uses `assets.EvalJS` (`/dev/eval`) to call browser `fetch()` for JSON requests (e.g., `/v1/models` validation) and a tiny `ReadableStream` line-reader queue (just like `__rustica_ai` already does for LiteRT-LM) for SSE token streams (`text/event-stream`).


* **Result:** Zero changes to `washmhost.js` or `rusticated-go` networking, and both `ai/assets` and remote cloud LLM backends work identically in desktop and browser modes.



---

### 2. `dload` Satellite Status & Isolation from Browser/Eval

**Clarification on how `dload` is currently wired:**

You are spot-on:

1. **`syscall.DylibOpen` is gone from the Rusticated baseline platform.** Instead, native C dynamic library loading for LiteRT-LM lives strictly as an **`ai`-internal side-load** via `ai/dload` (`ai/dload/client_rusticated.go` + the native helper binary `ai/cmd/dload` / `dload_helper`), which spawns a native child process and talks to it over stdin/stdout pipes (`magic: "DLOD"`).
2. **Complete Separation from Browser/Eval:**
* When `assets.HostOS() != "browser"`, `ai/backend_rusticated.go` uses `ai/dload` to spawn the native `dload` satellite and load `litert_lm_ext` (`.dll` / `.so` / `.dylib`).


* When `assets.HostOS() == "browser"`, `ai/backend_web_rusticated.go` and `ai/assets/fetch_browser.go` use **`/dev/eval`** (`EvalJS` in `ai/assets/eval_bridge_rusticated.go`) and **never** touch `ai/dload`.



---

### 3. Tagged Memory Array in `PlatformInfo` (OS-Agnostic Resource Reporting)

**Design for the Tagged Memory Slots:**

Adding a fixed-size array of tagged memory descriptors to `syscall.PlatformInfo` (in `rusticated-go/syscall/os_rusticated.go` and `mohabbat/washmhost`) is clean, fixed-size (no pointers or dynamic allocations across the WASI boundary), and future-proof:

```go
const MemorySlotCount = 8 // (or 16)

// MemoryKindTag (uint8: 0..255)
const (
    MemKindNone          uint8 = 0 // Unused slot
    MemKindSystemTotal   uint8 = 1 // Total physical host RAM
    MemKindSystemAvail   uint8 = 2 // Currently available / free host RAM
    MemKindGPUDiscrete   uint8 = 3 // Dedicated discrete GPU VRAM
    MemKindGPUShared     uint8 = 4 // Shared / unified GPU memory (e.g. Apple Silicon Metal, iGPU, WebGPU maxBuffer)
    MemKindNPUDedicated  uint8 = 5 // Dedicated NPU / TPU memory
    MemKindNPUShared     uint8 = 6 // Shared NPU + GPU / Unified accelerator pool
    MemKindSwapTotal     uint8 = 7 // Swap / pagefile total
)

type MemorySlot struct {
    Kind  uint8  // 0 = none, 1..255 = memory kind tag
    Flags uint8  // Reserved (e.g. bit 0 = unified with system RAM, bit 1 = estimate)
    _     [6]byte // Explicit 8-byte alignment padding
    Bytes uint64 // Capacity in bytes
}

```

#### Wire Layout inside `PlatformInfo`:

* Each `MemorySlot` is exactly **16 bytes** (`1 + 1 + 6 + 8`).
* An array of **8 slots** (`[8]MemorySlot`) adds **128 bytes** (or **16 slots** = **256 bytes**) at the end of `PlatformInfo` (`676` $\rightarrow$ `804` or `932` bytes).
* **How hosts populate it:**
* **Linux `washmhost`:** Slot 0 = `MemKindSystemTotal` (`Sysinfo.Totalram`), Slot 1 = `MemKindSystemAvail` (`/proc/meminfo` `MemAvailable`), Slot 2 = `MemKindGPUDiscrete` (from `/sys/class/drm/card*/device/mem_info_vram_total` if present).
* **macOS `washmhost`:** Slot 0 = `MemKindSystemTotal` (`hw.memsize`), Slot 1 = `MemKindSystemAvail`, Slot 2 = `MemKindNPUShared` (~75% of unified memory available to Metal/ANE).
* **Windows `washmhost`:** Slot 0 = `MemKindSystemTotal` (`GlobalMemoryStatusEx` `ullTotalPhys`), Slot 1 = `MemKindSystemAvail` (`ullAvailPhys`), Slot 2 = `MemKindGPUDiscrete` (DXGI adapter `DedicatedVideoMemory`), Slot 3 = `MemKindGPUShared` (`SharedSystemMemory`).
* **Browser `washmhost.js`:** Can populate Slot 0 = `MemKindSystemTotal` (`navigator.deviceMemory * 1 GiB`) and Slot 1 = `MemKindGPUShared` (if WebGPU limits are pre-probed), while `ai/assets` can also refine WebGPU limits via `EvalJS`.



---

### 4. All 6 Points Consolidated for the Final Plan

1. **Onboarding UI (`kabibi/ui/wizard`):** A reusable, high-level `ui.Widget` (`WizardWidget`) mounted cleanly as a child widget in `Shell`, exposing a declarative Go API so `Shell` doesn't hard-code onboarding interaction states or fight `ui.Host` for `os.Stdin`.


2. **Browser HTTP inside `ai` (`ai/http_browser.go`):** Self-contained `EvalJS`-backed HTTP & SSE helper inside `package ai` used when `HostOS() == "browser"`, while desktop uses `net/http`.


3. **Backend Serialization Boundary (`package ai`):** Stay flat in `package ai`; pass structured `ConversationConfig` and `[]PromptPart` (with `ToolCall.ID`) into `engineBackend` and `conversationBackend`; LiteRT-LM JSON serialization lives inside the LiteRT backends (`backend_rusticated.go` via `ai/dload` and `backend_web_rusticated.go` via `/dev/eval`), while `backend_remote.go` formats OpenRouter/Anthropic/Gemini/Copilot requests directly.


4. **Platform Hardware Discovery:** Extend `syscall.PlatformInfo` with `[8]MemorySlot` (or `[16]MemorySlot`) using a 256-value `Kind` tag (`0` = none, system RAM, discrete GPU, shared GPU/NPU, dedicated NPU, etc.).


5. **`EmbeddingGemma 2` Coexistence at Rest:** Selecting any local Gemma 4 tier automatically implies `EmbeddingGemma 2` (+164 MB); because embeddings run as post-processing at rest, `ai.Session` can load/unload between generation and idle embedding passes on memory-constrained systems.


6. **Credentials (`kabibi/credstore`):** Clean local credential store (`0600` file on desktop / `localStorage` via `EvalJS` in browser) with zero unimplemented `go-keyring` build dependencies.



