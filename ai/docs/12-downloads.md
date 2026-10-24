# Kabibi LLM Onboarding, Hardware Calibration, and Multi-Provider Integration

Here is the complete, end-to-end engineering specification and implementation plan for **Kabibi LLM Onboarding, Hardware Calibration, and Multi-Provider Integration**.

---

## 1. Architectural Overview & Core Rules

Currently, `LazyAIRunner` in Kabibi calls `assets.EnsureRuntime`, `assets.EnsureDefaultLLMModel`, and `assets.EnsureDefaultEmbeddingModel` unconditionally upon the first natural-language turn (`RouteAI`). This plan replaces that opaque bootstrap with a **user-consented, hardware-aware onboarding pipeline** governed by six strict rules:

1. **Zero Silent Downloads:** No network transfer of `.litertlm` weights or runtime wheels happens without explicit user confirmation of the byte size and destination path.
2. **UI-Agnostic Onboarding Callback (`OnboardingPrompter`):** The AI bootstrap flow (`LazyAIRunner`) does not know how prompts are rendered or how input is collected; it invokes an injected callback interface. Two concrete callback implementations are provided:
   - **TUI Widget Callback (`isTTY=true` / interactive `Shell`):** Mounts a first-class `ui.Widget` (`WizardWidget` in `kabibi/ui/wizard`) inside `Shell`'s command block stack / prompt area (supporting arrow-key navigation, numbered selections, masked secret input `••••`, and progress bars) without touching `os.Stdin` directly.
   - **Stdio Line-Based Callback (`isTTY=false` / headless / one-shot / piped):** Emits plain stdio prompts and performs line-based reads from `stdin`, failing cleanly with an actionable error if `stdin` is closed, empty (`io.EOF`), or non-interactive.
3. **Implied `EmbeddingGemma 2` Bundling & At-Rest Coexistence:** Selecting any local Gemma 4 tier automatically includes `embeddinggemma-2-text-270m.litertlm` (`164,626,432` bytes, ~164 MB) in the total download footprint and single consent prompt. Because embeddings run as post-processing at rest (synchronized via `Session.turnGate`), memory-constrained hosts load/unload `Gemma 4` and `EmbeddingGemma 2` sequentially so peak resident memory never exceeds `max(Size(Gemma4), Size(EmbeddingGemma2))`.
4. **Universal Tagged Hardware Calibration:** Host memory, GPU VRAM, and NPU/unified memory capacities are exposed via a fixed-size `[16]MemorySlot` array in `syscall.PlatformInfo` (each slot carrying a 256-value `Kind` tag where `0` = none, plus a `!wasip1` native fallback for host `go test`), enabling Kabibi to recommend the optimal Gemma 4 variant (`E2B`, `E4B`, `12B`, `26B MoE`, or `31B Dense`) without userland shell-outs.
5. **First-Class Cloud Provider Support in Flat `package ai`:** Users can bypass local LLM downloads and authenticate via **OAuth 2.0 Device Flow** (GitHub Copilot, Google) or **Personal Access Tokens (PAT)** (OpenRouter, Anthropic Claude, Google Gemini, OpenAI). Credentials are persisted via `kabibi/credstore` (`0600` file on desktop; `localStorage` / `AsyncIndexedDBFS` in browser) with zero external keyring dependencies.
6. **Self-Contained Browser HTTP (`ai/http_browser.go`) & Explicit Failure Reporting:** Desktop uses standard `net/http`, while browser mode (`assets.HostOS() == "browser"`) uses an internal `EvalJS` (`/dev/eval`) HTTP and `ReadableStream` SSE client. If browser CORS policies or network restrictions block a cloud or OAuth endpoint, Kabibi fails fast and reports the exact cause to the user.

---

## 2. Package Layout & Component Boundaries

To avoid circular imports and preserve existing package conventions, `package ai` remains flat and `dload` remains strictly separated from browser `/dev/eval`:

```text
rusticated-go/syscall/
└── os_rusticated.go             # Extend PlatformInfo with [16]MemorySlot (256-value Kind tag)
mohabbat/washmhost/
├── env_os.go                    # Populate [16]MemorySlot on Linux, macOS, Windows
└── js/browser/index.js          # Populate browser MemorySlots in get_platform_info
ai/
├── assets/
│   ├── catalog.go               # Multi-tier Gemma 4 URL/size registry (Desktop + Web)
│   ├── sysinfo.go               # HardwareProfile & tier recommendation logic
│   ├── sysinfo_rusticated.go    # //go:build wasip1 — reads syscall.GetPlatformInfo().MemorySlots (+ WebGPU EvalJS)
│   └── sysinfo_native.go        # //go:build !wasip1 — native OS memory fallback for host `go test`
├── backend.go                   # Structured engineBackend / conversationBackend interfaces + LiteRT driver adapter
├── backend_native.go            # //go:build !wasip1 — purego LiteRT-LM FFI
├── backend_rusticated.go        # //go:build wasip1 && !js — dload satellite LiteRT-LM FFI (desktop only)
├── backend_web_rusticated.go    # //go:build wasip1 — browser /dev/eval @litert-lm/core WebGPU backend
├── backend_remote.go            # Remote HTTP/SSE engineBackend & conversationBackend (OpenRouter, Claude, Gemini, Copilot)
├── http_client.go               # Transport interface selecting net/http (desktop) vs EvalJS fetch/SSE (browser)
├── http_browser.go              # //go:build wasip1 — EvalJS fetch + ReadableStream SSE client with CORS detection
└── auth_device.go               # RFC 8628 OAuth Device Code polling loop & PAT validation helpers
kabibi/
├── config/
│   └── ai_config.go             # ai.json state & consent persistence
├── credstore/
│   └── credstore.go             # 0600 file store (desktop) / EvalJS localStorage or IndexedDB (browser)
└── ui/
    ├── wizard/
    │   └── wizard.go            # Reusable first-class ui.Widget for menus, confirm prompts, masked inputs
    └── shell/
        └── ai_onboarding.go     # OnboardingPrompter interface + TUI WizardWidget & Stdio line-reader implementations
```

---

## 3. Phase-by-Phase Implementation Plan

### Phase 1: Model Catalog & Tagged Hardware Calibration (`ai/assets` & `syscall.PlatformInfo`)

#### 1.1 Multi-Tier Gemma 4 Registry (`ai/assets/catalog.go`)

Replace the single hardcoded `DefaultLLMModelURL` assumption with a structured catalog supporting both native `.litertlm` and browser `-web.litertlm` targets:

| Tier ID | Display Name | Architecture | Approx. `.litertlm` Size | WebGPU (`-web`) Support | Min Recommended Memory |
| --- | --- | --- | --- | --- | --- |
| `gemma-4-e2b` | Gemma 4 E2B | 2B Dense + ViT | ~2.04 GB (`2,038,755,678` B) | Yes (`-web.litertlm`) | $\ge$ 4 GB RAM / 2 GB VRAM |
| `gemma-4-e4b` | Gemma 4 E4B | 4B Dense + ViT | ~2.80 GB | Yes (`-web.litertlm`) | $\ge$ 8 GB RAM / 4 GB VRAM |
| `gemma-4-12b` | Gemma 4 12B | 12B Dense | ~6.20 GB | Experimental | $\ge$ 16 GB RAM / 8 GB VRAM |
| `gemma-4-26b-a4b` | Gemma 4 26B MoE | 26B MoE (3.8B active) | ~14.20 GB | Desktop Only | $\ge$ 24 GB RAM / 16 GB VRAM |
| `gemma-4-31b` | Gemma 4 31B | 31B Dense | ~18.50 GB | Desktop Only | $\ge$ 32 GB RAM / 24 GB VRAM |

* **Bundled Embedder Constant:**
  `DefaultEmbeddingModelURL` (`embeddinggemma-2-text-270m.litertlm`, `164,626,432` bytes) is automatically included in the consent summary and download queue whenever any local `gemma-4-*` tier is chosen.

#### 1.2 Tagged Memory Slots in `syscall.PlatformInfo` (`rusticated-go` & `mohabbat/washmhost`)

Extend `syscall.PlatformInfo` with a fixed-size array of 16 tagged memory descriptors (16 bytes each = 256 bytes appended to the existing 676-byte struct, totaling 932 bytes):

```go
const MemorySlotCount = 16

// MemoryKindTag (uint8: 0..255, where 0 means unused/none)
const (
	MemKindNone         uint8 = 0 // Unused slot
	MemKindSystemTotal  uint8 = 1 // Total physical host RAM
	MemKindSystemAvail  uint8 = 2 // Currently available / free host RAM
	MemKindGPUDiscrete  uint8 = 3 // Dedicated discrete GPU VRAM
	MemKindGPUShared    uint8 = 4 // Shared / unified GPU memory (iGPU, WebGPU maxBuffer)
	MemKindNPUDedicated uint8 = 5 // Dedicated NPU / TPU memory
	MemKindNPUShared    uint8 = 6 // Shared NPU + GPU unified accelerator pool (e.g. Apple Silicon)
	MemKindSwapTotal    uint8 = 7 // Swap / pagefile total
)

type MemorySlot struct {
	Kind  uint8   // 0 = none, 1..255 = memory kind tag
	Flags uint8   // Bit 0 = unified with system RAM, Bit 1 = estimate
	_     [6]byte // Explicit 8-byte alignment padding
	Bytes uint64  // Capacity in bytes
}
```

* **Host Population in `washmhost`:**
  - **Linux (`washmhost`):** Populates `MemKindSystemTotal` and `MemKindSystemAvail` from `/proc/meminfo` (or `sysinfo`), and `MemKindGPUDiscrete` from `/sys/class/drm/card*/device/mem_info_vram_total` when present.
  - **macOS (`washmhost`):** Populates `MemKindSystemTotal` (`hw.memsize`), `MemKindSystemAvail`, and `MemKindNPUShared` (~70–75% unified Metal/ANE pool on Apple Silicon).
  - **Windows (`washmhost`):** Populates `MemKindSystemTotal` and `MemKindSystemAvail` via `GlobalMemoryStatusEx`, plus `MemKindGPUDiscrete` / `MemKindGPUShared` when available.
  - **Browser (`washmhost.js`):** Populates `MemKindSystemTotal` from `navigator.deviceMemory` (when exposed).

#### 1.3 Hardware Prober (`ai/assets/sysinfo.go`, `sysinfo_rusticated.go`, `sysinfo_native.go`)

Implement `ProbeHardware() HardwareProfile`:

```go
type HardwareProfile struct {
	HostOS             string       // "linux", "darwin", "windows", "browser"
	Slots              []MemorySlot // Non-zero tagged memory slots reported by host
	TotalRAMBytes      uint64
	AvailableRAMBytes  uint64
	AcceleratorBytes   uint64       // Best discrete GPU, NPU, or shared accelerator pool
	HasUnifiedMemory   bool
	WebGPUMaxBinding   uint64       // Populated when HostOS == "browser"
	HasWebGPU          bool
	ConstrainedMemory  bool         // True when Gemma 4 + EmbeddingGemma 2 should load/unload sequentially
	RecommendedTier    string       // e.g., "gemma-4-12b", or "" if local unsupported
}
```

* **Under `wasip1` (`sysinfo_rusticated.go`):**
  - Reads `syscall.GetPlatformInfo().MemorySlots` directly—zero OS-specific shell-outs in guest code.
  - When `HostOS() == "browser"`, refines WebGPU capabilities via `EvalJS` (`/dev/eval` in `ai/assets/eval_bridge_rusticated.go`, never `syscall.DylibOpen`):
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
  - If `webgpu == false` or `maxBuffer < 2.2 GB`, marks local browser tiers as unsupported (`RecommendedTier = ""`) and recommends Cloud APIs.
* **Under `!wasip1` (`sysinfo_native.go`):**
  - Provides native OS memory reading so `go test ./ai/...` and native builds work without `syscall.GetPlatformInfo()`.

---

### Phase 2: Configuration & Secret Persistence (`kabibi/config` & `kabibi/credstore`)

#### 2.1 Configuration Schema (`ai.json`)

Store non-sensitive onboarding state in Kabibi's config directory (`~/AppData/Local/kabibi/ai.json` on Windows, `~/.config/kabibi/ai.json` on POSIX and browser `AsyncIndexedDBFS`):

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

* **Pre-Existing Cache Migration:** If `ai.json` does not exist yet, check whether the default model and runtime are already present and valid in `DefaultCacheDir()`. If not present, `configured` defaults to `false` and blocks any automatic download until onboarding completes.

#### 2.2 Credential Management via `kabibi/credstore`

* Implement a small, self-contained `kabibi/credstore` package with `Get(service, user string) (string, error)`, `Set(service, user, secret string) error`, and `Delete(service, user string) error`:
  - **Desktop (`HostOS() != "browser"`):** Stores credentials in a `0600` permission-restricted file (`credentials.json` inside Kabibi's config directory) written atomically via `.tmp` + rename.
  - **Browser (`HostOS() == "browser"`):** Stores credentials in browser `localStorage` via `EvalJS` (or `0600` file backed by `AsyncIndexedDBFS`).
* All API tokens and OAuth tokens use:
  - `service`: `"kabibi:ai:" + provider` (e.g., `"kabibi:ai:openrouter"`, `"kabibi:ai:copilot"`, `"kabibi:ai:anthropic"`, `"kabibi:ai:gemini"`, `"kabibi:ai:openai"`).
  - `user`: `"default"` (or user-specified profile name).
* Secrets are **never** written to `ai.json`, shell history (`CommandRecord`), or LLM context.

---

### Phase 3: Callback-Driven Onboarding & Dual Implementations (`kabibi/ui/wizard` & `kabibi/ui/shell`)

#### 3.1 The UI-Agnostic Onboarding Callback (`OnboardingPrompter`)

`LazyAIRunner` (and the `ai` bootstrap flow) does not know whether it is running inside a full-screen TUI, a subshell, or a non-interactive script. Instead, `LazyAIRunner` holds an injected callback:

```go
// OnboardingRequest describes the hardware profile, available tiers, and download
// sizes needed for the user to configure AI and grant download consent.
type OnboardingRequest struct {
	Hardware   assets.HardwareProfile
	Tiers      []assets.ModelTier
	CacheDir   string
	RuntimeMB  int
	EmbedderMB int
}

// OnboardingPrompter runs the interactive or line-based onboarding flow and
// returns the user's chosen AI configuration (with consent_given set if approved).
type OnboardingPrompter func(ctx context.Context, req OnboardingRequest) (*config.AIConfig, error)
```

When `LazyAIRunner.ensureInitialized(ctx)` sees that AI is unconfigured (or requires download consent):
1. It invokes `r.prompter(ctx, req)` before calling `EnsureRuntime`, `EnsureModel`, or opening an engine.
2. If `r.prompter` returns an error (e.g., user cancelled with `Ctrl+C`/`Esc`, selected `[0] Shell-only mode`, or `stdin` reached `EOF` in non-interactive mode), `ensureInitialized` aborts without transferring a single byte.
3. If `r.prompter` succeeds, `LazyAIRunner` persists `ai.json`, executes the consented downloads (streaming progress to `r.emitFeedback`) or initializes the remote provider, and **immediately resumes and answers the user's original prompt**.

Additionally, `ai setup` (and `kabibi --ai-setup`) explicitly invokes the same onboarding flow at any time so users can switch models or providers.

#### 3.2 Implementation 1: Mounted TUI `WizardWidget` (`isTTY=true` in `Shell`)

When running inside the interactive `ui.Host` TUI:
* **Reusable `ui.Widget` (`kabibi/ui/wizard`):**
  - Implements `ui.Widget` (`Measure`, `Render`, `HandleEvent`).
  - Supports arrow-key (`Up`/`Down`) and number-key (`0`–`7`) menu selection, `[Y/n]` confirmation, masked secret input (`••••` with Backspace/Paste support so secrets never appear in scrollback), and cancellable OAuth polling status.
* **Mounting in `Shell`:**
  - When the background turn goroutine in `Shell` invokes the TUI `OnboardingPrompter`, `Shell` attaches the `WizardWidget` to the active command block (or active modal slot under `s.mu`), calls `s.invalidate()`, and blocks the turn goroutine on the wizard's result channel.
  - While the wizard is mounted, `Shell.handleKeyEvent` and `PasteEvent` route input directly to `wizard.HandleEvent(ev)` and `Shell.Render` blits `wizard.Render(...)` inside the stack above the footer.
  - Once the user completes or cancels the wizard, `Shell` unmounts the widget, calls `s.invalidate()`, and returns the result to `LazyAIRunner` so the turn continues seamlessly.

#### 3.3 Implementation 2: Stdio Line-Based Prompter (`isTTY=false` / One-Shot / Piped)

When running outside the TUI (`kabibi -c`, `kabibi -p`, `kabibi <prompt words...>`, or piped `stdin`):
* Renders the numbered menu and consent summary as plain text to `stdout`/`stderr` and reads line-delimited responses from `stdin` via a `bufio.Reader`.
* If `stdin` is closed, empty (`io.EOF`), or returns an invalid/non-interactive response when a choice or confirmation is required, it fails immediately with a clear error (`"AI is not configured; run 'kabibi' or 'kabibi --ai-setup' interactively to configure AI and approve model downloads"`), guaranteeing zero silent downloads and zero hangs in automated scripts.

#### 3.4 Step-by-Step UX Flow (Shared Across Both Prompters)

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

* If `Y`: Set `consent_given = true`, save `ai.json`, and run `EnsureRuntime`, `EnsureLLMModel(tier)`, and `EnsureDefaultEmbeddingModel` with live progress updates.
* If `n`: Return to the main menu without downloading a single byte.

**Step B2: OAuth Device Flow (If Option 6 Selected)**

1. Prompt for provider: `[1] GitHub Copilot` or `[2] Google Gemini OAuth`.
2. Initiate RFC 8628 Device Authorization Request (`POST https://github.com/login/device/code` or Google's device code endpoint) via `ai`'s HTTP transport.
   - If running in browser mode and the OAuth endpoint rejects cross-origin requests (CORS), fail immediately and display a clear explanation advising the user to use Option 7 (API Key / PAT) or a local WebGPU model in the browser.
3. Display the verification URL and user code, and attempt to open the browser (`xdg-open` / `open` / `rundll32` on desktop, or `window.open` via `EvalJS` in browser mode):
   ```text
   Authenticate with GitHub Copilot:
     1. Visit:  https://github.com/login/device
     2. Code:   WDJB-MJHT

   Waiting for authorization... (Press Ctrl+C / Esc to cancel)
   ```
4. Poll the token endpoint every `interval` seconds respecting `slow_down` and context cancellation.
5. On success, store the token via `credstore.Set("kabibi:ai:"+provider, "default", token)`, optionally ask whether to download `EmbeddingGemma 2` (164 MB) for local associative memory, and write `ai.json`.

**Step B3: Personal Access Token Flow (If Option 7 Selected)**

1. Prompt for provider: `[1] OpenRouter`, `[2] Anthropic Claude`, `[3] Google Gemini`, `[4] OpenAI`.
2. Read the key (masked as `•` in `WizardWidget`, or line-read in stdio mode).
3. Validate the key with a lightweight HTTP request (`GET /v1/models` or provider equivalent). If validation fails (including browser CORS rejection or `401 Unauthorized`), report the exact error and allow retry or menu return.
4. Save to `credstore.Set("kabibi:ai:"+provider, "default", apiKey)`.
5. Present a curated model picker for that provider (with an option to enter a custom model ID), optionally offer the 164 MB `EmbeddingGemma 2` local download on desktop, and save `ai.json`.

---

### Phase 4: Backend Serialization Boundary, Remote Providers, and At-Rest Embedder (`package ai`)

#### 4.1 Structured `engineBackend` & `conversationBackend` Boundary (`ai/backend.go`, `ai/engine.go`, `ai/conversation.go`)

1. **Tool Call IDs:**
   - Add `ID string` to `ToolCall` (`json:"id,omitempty"`) and `ToolCallID string` to `PromptPart` (`RoleTool` responses copy the originating `ToolCall.ID`).
2. **Structured Backend Signatures:**
   - Update `engineBackend` and `conversationBackend` in `ai/backend.go` so `engine.go` and `conversation.go` pass structured Go values rather than pre-serialized LiteRT-LM JSON strings:
     ```go
     type engineBackend interface {
         NewConversation(cfg ConversationConfig) (conversationBackend, error)
         Close() error
     }

     type conversationBackend interface {
         SendStream(ctx context.Context, parts []PromptPart, onEvent func(ModelEvent)) error
         Clone() (conversationBackend, error)
         Close() error
     }
     ```
   - Move `buildHistoryJSON`, `buildMessageJSON`, and `parseChunkEvents` into the LiteRT-LM backend layer (shared by `driverEngineBackend` for native/`dload` and `webEngineBackend` for browser `@litert-lm/core`), keeping LiteRT-LM wire details strictly inside the LiteRT backends.

#### 4.2 Remote Provider Backend (`ai/backend_remote.go` & `ai/http_browser.go`)

1. **Unified Tool & Message Translation:**
   - `ai.Session` continues to compile Kabibi's declarative tools (`exec`, `read_file`, `write_file`, `list_dir`, `shell_history`, `shell_state`, `deliberate`, and `browser_*`) with the auto-injected `explanation` property.
   - `backend_remote.go` translates `ConversationConfig` and `[]PromptPart` (including `ToolCall.ID` / `ToolCallID`) into:
     - **OpenAI / OpenRouter / Copilot format:** `tools: [{ type: "function", function: { name, description, parameters } }]` and `role: "tool"` messages with `tool_call_id`.
     - **Anthropic Messages API format:** `tools: [{ name, description, input_schema }]` and `tool_use` / `tool_result` content blocks with `tool_use_id` (including `anthropic-dangerous-direct-browser-access: true` when running in browser mode).
     - **Gemini GenerateContent format:** `tools: [{ functionDeclarations: [...] }]` and `functionCall` / `functionResponse` parts.
2. **Desktop `net/http` + Browser `EvalJS` SSE Transport (`ai/http_client.go`, `ai/http_browser.go`):**
   - On desktop (`assets.HostOS() != "browser"`), stream `text/event-stream` responses over standard `net/http`.
   - In browser mode (`assets.HostOS() == "browser"`), `ai/http_browser.go` uses `EvalJS` (`/dev/eval`) to execute `fetch()` and read SSE chunks from `response.body.getReader()`, surfacing any `TypeError: Failed to fetch` / CORS failures with a clear user-facing error message.
3. **`Conversation.Clone()` Support:**
   - Because cloud APIs are stateless per HTTP request (conversation state is the message array), `remoteConvBackend.Clone()` deep-copies the in-memory `ConversationConfig` and accumulated `[]PromptPart` history—making `deliberate`'s parallel contemplation forks work instantaneously.

#### 4.3 `EmbeddingGemma 2` Coexistence & On-Demand At-Rest Lifecycle (`ai/session.go` & `kabibi/ui/shell/ai_runner.go`)

1. **Sequential Load/Unload on Constrained Hosts:**
   - When `HardwareProfile.ConstrainedMemory` is true (e.g., $\le 8$ GB RAM or constrained accelerator memory), `LazyAIRunner` wraps the embedder in an on-demand `PartEmbedder` coordinated with `Session.turnGate`:
     - During active chat turns (`RunTurn`), the LLM `Engine` holds the accelerator while the `Embedder` remains unloaded.
     - During idle/at-rest background embedding passes (`processPendingEmbeddings` / `FlushEmbeddings`), if memory is too tight for both models simultaneously, weights can be loaded on demand and released before the next active turn.
2. **Graceful Fallback When `Embedder == nil`:**
   - If the user opted out of local downloads in remote mode (or in browser mode when local embedding is unavailable), `ai.Session` runs with `Embedder: nil`, cleanly skipping vector-similarity anchor selection and falling back to recency windowing and regex/literal tool search.

---

## 4. Execution & Testing Checklist

| Step | Deliverable | Verification Method |
| --- | --- | --- |
| **1** | `syscall.PlatformInfo` (`[16]MemorySlot`) & `ai/assets/sysinfo*.go` + `catalog.go` | Unit test `PlatformInfo` binary serialization (932-byte layout) in `washmhost` and `ProbeHardware()` tier selection across mocked 4 GB, 8 GB, 16 GB, 24 GB, and 32 GB profiles, plus browser `EvalJS` JSON parsing and `!wasip1` native fallback. |
| **2** | `kabibi/config/ai_config.go` & `kabibi/credstore` | Unit test `ai.json` load/save (verifying default `configured: false` blocks automatic downloads) and `credstore.Set`/`Get`/`Delete` (`0600` file permissions on desktop, `EvalJS` storage in browser). |
| **3** | `ai/backend.go` refactor & `ai/backend_remote.go` + `ai/http_browser.go` | Verify existing LiteRT-LM unit tests pass with structured `ConversationConfig`/`[]PromptPart` backends; test SSE chunk parsing, `ToolCall.ID` round-tripping, `Clone()` deep-copying, and CORS error reporting for OpenRouter, Anthropic, Gemini, and Copilot. |
| **4** | `ai/auth_device.go` | Test RFC 8628 device-code polling loop (including `authorization_pending`, `slow_down`, cancellation, and CORS failure handling) against a mock HTTP transport. |
| **5** | `kabibi/ui/wizard` & `kabibi/ui/shell/ai_onboarding.go` | Test both `OnboardingPrompter` callbacks: (a) TUI `WizardWidget` mounted in `Shell` (`isTTY=true`) handling arrow/number keys, masked PAT entry, and resuming the prompt after consent; (b) stdio line-based prompter (`isTTY=false`) completing on valid input and failing cleanly without downloading on `io.EOF`. |

