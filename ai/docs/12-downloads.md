# Kabibi AI Setup, Hardware Calibration, and Provider Support

This document records the implemented first-run AI setup flow and its current
scope. The user-approved provider scope is **API keys only**: Google/GitHub
OAuth and GitHub Copilot are deferred.

## 1. Runtime behavior and guarantees

1. **Explicit local-download consent.** An unconfigured runner prompts before
   fetching the LiteRT-LM runtime, local model, or embedding model. The prompt
   includes estimated transfer bytes and the cache directory. Cached assets are
   excluded from the estimate. Runtime wheel metadata may be queried to obtain
   the selected wheel's exact size, but the wheel and model files are not
   downloaded before confirmation.
2. **UI-independent prompting.** `LazyAIRunner` uses `OnboardingPrompter`.
   Kabibi's interactive Shell mounts a wizard widget; one-shot/headless
   execution uses the line-oriented stdio implementation. A missing or
   exhausted response is an error, not consent.
3. **Cloud setup does not download local model weights.** The available
   providers are OpenAI, OpenRouter, Anthropic, and Google Gemini. The setup
   validates the API key against the provider's model-list endpoint before
   saving it and the selected model.
4. **Explicit failures.** Download, configuration, credential-validation,
   browser network, and CORS errors are returned to the caller. Browser fetch
   failures explain that network or CORS policy may be responsible; there is no
   silent native-transport fallback.
5. **Hardware-aware choices.** Native and Rusticated platform memory slots
   inform model recommendations. The recommendation is advisory; users can
   select another supported tier.
6. **Adaptive local embedding.** On native local setups, Kabibi first tries to
   keep the LLM and EmbeddingGemma resident together. If embedder loading or
   embedding fails, it switches for that runner session to a sequential
   close/embed/reload path and restores the conversation from compacted
   history. A successful fallback is nonfatal. If the sequential embedding
   still fails, the error is surfaced and embeddings are disabled for that
   session; if the LLM cannot be restored, the next AI turn reports that
   failure.

## 2. Catalog and hardware ABI

Model artifact sizes below are exact bytes from the catalog. Native and
WebGPU artifacts can differ; tiers marked unsupported are not offered in the
browser even if a URL/size is retained in the catalog.

| Tier ID | Display name | Native bytes | WebGPU bytes | Browser support | Recommended minimum RAM / accelerator |
| --- | --- | ---: | ---: | --- | --- |
| `gemma-4-e2b` | Gemma 4 E2B | 2,588,147,712 | 2,008,432,640 | Yes | 4 GiB / 2 GiB |
| `gemma-4-e4b` | Gemma 4 E4B | 3,659,530,240 | 2,969,059,328 | Yes | 8 GiB / 4 GiB |
| `gemma-4-12b` | Gemma 4 12B | 6,883,278,368 | 5,986,074,624 | Yes | 16 GiB / 8 GiB |
| `gemma-4-26b-a4b` | Gemma 4 26B MoE | 15,786,524,672 | 15,786,524,672 | No | 24 GiB / 16 GiB |
| `gemma-4-31b` | Gemma 4 31B | 19,217,989,632 | 19,217,989,632 | No | 32 GiB / 24 GiB |

The default local embedder, `embeddinggemma-2-text-270m.litertlm`, is
164,626,432 bytes. It is included in the single local consent estimate on
desktop and omitted for browser-local setups, where the local embedder is not
supported.

`syscall.PlatformInfo` retains its 676-byte legacy prefix and appends sixteen
16-byte tagged memory slots, for a 932-byte total layout. Memory kinds
distinguish system total/available memory, GPU/NPU memory, unified memory, and
swap. Native `!wasip1` probes provide host-test support; Rusticated builds read
the ABI slots, and browser builds additionally probe WebGPU limits through
`EvalJS`.

## 3. Package boundaries

```text
ai/
├── assets/catalog.go             # Gemma model URLs, exact sizes, cache estimates
├── assets/sysinfo*.go            # Hardware profile and tier recommendations
├── backend.go                    # Structured engine/conversation interfaces
├── backend_native.go             # Native LiteRT-LM driver
├── backend_rusticated.go         # Rusticated dload LiteRT-LM driver
├── backend_web_rusticated.go     # Browser LiteRT-LM WebGPU backend
├── backend_remote.go             # OpenAI, OpenRouter, Anthropic, Gemini SSE backends
├── auth.go                       # API-key validation
├── http_client.go                # Shared remote streaming transport boundary
├── http_do_native.go             # Native non-streaming validation requests
├── http_do_browser.go            # Browser EvalJS validation requests
└── http_browser.go               # Browser EvalJS fetch/ReadableStream SSE
kabibi/
├── config/ai_config.go           # Non-secret setup configuration
├── credstore/                    # Restricted desktop files / browser localStorage
├── internal/privatefile/         # Platform-specific credential-file restrictions
└── ui/
    ├── wizard/wizard.go          # Reusable mounted prompt widget
    └── shell/
        ├── ai_onboarding.go      # TUI and stdio OnboardingPrompter implementations
        ├── ai_setup.go           # Local/cloud setup and consent flow
        └── adaptive_embedder.go  # Resident-first, sequential fallback policy
```

The AI module remains independent of Kabibi UI and persistence packages.
`ConversationConfig`, `PromptPart`, and `ModelEvent` cross the shared backend
boundary as typed Go values; provider and LiteRT-LM wire formats stay inside
their respective backends.

## 4. Setup flow

### 4.1 Configuration

`kabibi/config` stores non-secret state in `ai.json`. Fields are flat and
include `configured`, `mode`, `tier_id`, `provider`, `model`, `base_url`,
`embedder`, and `consent_given`. Credentials are never copied into this file,
shell history, or the conversation context.

On first AI use:

1. Load `ai.json`; a missing file means unconfigured.
2. Prompt for local Gemma, cloud API-key setup, or cancellation.
3. Persist local configuration only after explicit download consent, or persist
   remote configuration only after key validation and cloud-use confirmation.
4. Initialize the chosen engine and resume the original AI turn.

An existing configured local entry without recorded consent is rejected rather
than silently downloading. Existing cached assets are counted as zero-byte
transfers in the consent estimate.

### 4.2 Prompt implementations

`OnboardingPrompter` exposes `Choose`, `Confirm`, and `Input` methods.

- **Shell TUI:** `TUIOnboardingPrompter` mounts `ui/wizard.Widget`, routes key
  and paste input to it, masks secret text in the rendered widget, and returns
  the choice to the waiting runner.
- **One-shot/headless stdio:** `StdioOnboardingPrompter` prints numbered
  options and reads line-oriented responses. EOF/closed stdin returns an
  actionable error. A generic blocking `io.Reader` cannot be interrupted
  while its `Read` is blocked.

### 4.3 Local model consent

The wizard probes hardware and displays supported catalog tiers. The hardware
recommended tier is the default selection; if the probe has no recommendation,
the smallest available tier is selected by default. The confirmation shows the
transfer total, byte count, breakdown (model/runtime/embedder), and cache path.

`assets.RuntimeDownloadSize`, `ModelTierDownloadSize`, and
`EmbeddingModelDownloadSize` account for cached assets. Once the user confirms,
the configuration records consent and `EnsureRuntime`, `EnsureModelTier`, and
`EnsureDefaultEmbeddingModel` report progress through the runner callback.

### 4.4 Cloud API keys

The onboarding provider list is OpenAI, OpenRouter, Anthropic, and Gemini.
Each provider has curated model choices and a custom model-ID option. The API
key is validated with a provider model-list request. Validation establishes
that the credential is accepted; it does not guarantee that the chosen model
ID is enabled for that account.

Desktop credentials are stored separately in `ai-credentials.json` with
platform-appropriate private permissions (a current-user protected DACL on
Windows). Browser credentials use `localStorage` through the Rusticated
`EvalJS` bridge. Browser storage is origin-scoped, not an OS keychain.

## 5. Remote engine and browser transport

`backend_remote.go` translates structured conversation history, tools, tool
call IDs, and tool results into the OpenAI-compatible, Anthropic, and Gemini
request formats. Remote conversation cloning deep-copies the in-memory
conversation history so branched reasoning can be isolated.

Native requests use `net/http`. The browser implementation uses `EvalJS`
`fetch()` and `ReadableStream` for SSE, and a bounded response body for
credential validation. CORS/network failures are returned explicitly. Remote
base URLs must use HTTPS, except loopback HTTP URLs used for local tests.

## 6. Embedding residency policy and limits

Native local setup downloads the embedder as part of the consented installation
and attempts to load it while the LLM is resident. If the embedder loads and
works, both remain resident. If embedder initialization or inference fails,
the adaptive wrapper closes the active conversation and LLM engine, runs the
embedder, closes it, reloads the LLM engine, and recreates the conversation
from `Session.CompactedHistory`. `Session.turnGate` serializes this lifecycle
with generation and embedding work.

The fallback currently responds to any embedder load/inference error rather
than classifying only out-of-memory errors. It is session-scoped, not persisted.
It does not proactively detect or recover from a later LLM generation failure
caused by both resident models competing for memory. If a sequential embedding
failure occurs while the LLM has been restored, chat continues without further
embeddings and the failure is written to stderr. If LLM reinitialization or
conversation restoration fails, the error is retained and returned on the next
turn.

Cloud and browser-local modes run without a local embedder. `ai.Session`
continues with its ordinary history/recency behavior when `Embedder` is nil.

## 7. Deferred scope

- OAuth 2.0 device flow is not implemented. Google OAuth requires a registered
  application client ID; GitHub Copilot additionally needs a supported
  third-party token-exchange contract. The current selected scope is API keys
  only, so Copilot is not exposed as a provider.
- Reconfiguration through a dedicated `ai setup` command is not included in
  this first-run flow.
- Cloud model IDs are chosen from curated defaults or entered manually; setup
  validates the key, not the model's per-account availability.
- Browser credential persistence uses origin-scoped `localStorage` and is not
  encrypted by the application.

## 8. Verification

- `go test ./...` in `ai`: passed.
- `go test ./config ./credstore ./ui/shell ./ui/wizard` in `kabibi`: passed.
- Rusticated build-only validation via `go run . kabibi -o
  target/kabibi-onboarding-validate.wasm`: passed; the temporary output was
  removed after the check.
- Full `go test ./...` in `kabibi` still reports unrelated failures in
  `TestExecutableDoesNotImportCharmUIStack` and existing `ui/app` snapshots/
  layout expectations. The changed AI, config, credential-store, shell, and
  wizard packages pass their focused tests.
