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

