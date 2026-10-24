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

# Lessons of PI harness

This section specifies the next-generation architecture for Kabibi AI initialization, provider authentication, credential refresh, and cloud inference backends, derived from a critical analysis of Kabibi's existing codebase (`ai/`, `kabibi/ui/shell/`, `kabibi/credstore/`, `demo-go/server/`) and the working prototype in `external/pi/packages/ai`.

---

## 9. Critical Audit of Existing Kabibi AI & Shell Code

### 9.1 Why `OnboardingPrompter` and `ui/wizard` Are the Wrong Abstraction
In the current codebase:
- `kabibi/ui/shell/ai_onboarding.go` defines `OnboardingPrompter` (`Choose`, `Confirm`, `Input`) with two implementations: `TUIOnboardingPrompter` (which mounts a special-purpose `s.wizardWidget` onto `Shell` and intercepts events in `s.handleWizardEvent`) and `StdioOnboardingPrompter` (which reads lines from `io.Reader` and **ignores the `secret` boolean parameter**, echoing secret API keys in plain text on stdio).
- `kabibi/ui/shell/ai_setup.go` hardcodes the entire control flow (`configureAI`, `configureLocalAI`, `configureRemoteAI`, `chooseRemoteModel`) inside `shell`, forcing `LazyAIRunner` to know about specific providers, curated model lists, and API-key prompts.

**Flaws:**
1. **Violates encapsulation:** `Shell` has a special widget slot (`wizardWidget`) and custom event routing just for AI onboarding, while `LazyAIRunner` micromanages provider authentication steps.
2. **Too rigid for real provider auth:** Real authentication flows (device-code polling with live countdowns, local HTTP callback servers raced against manual URL paste, dynamic model catalog probing, multi-step progress) do not fit a synchronous `Choose`/`Confirm`/`Input` RPC interface without inventing a complex event/callback protocol.
3. **Already solved better by `CommandTerminal`:** Kabibi *already* has a clean, in-process virtual terminal abstraction (`CommandTerminal` in `kabibi/ui/shell/command_terminal.go` + `liveOutputState` in `kabibi/ui/shell/shell_widget.go`) used by built-in commands like `git log` / `git diff` (`runPager` in `kabibi/ui/shell/pager.go`) and `more` (`kabibi/ui/shell/uroot.go`).

### 9.2 Gaps in `CommandTerminal` & `Shell` When Running Inside an AI Turn
Tracing `kabibi/ui/shell/exec_session.go` and `kabibi/ui/shell/shell_widget.go` reveals three concrete mechanical gaps that must be addressed to use `CommandTerminal` for AI initialization and backend authentication:
1. **`CommandTerminal` is currently created only for `SourceUserShell` in `RunCommand`:**
   In `ExecutionSession.RunCommand` (`exec_session.go:466`), `commandTerminal` is only allocated when `source == SourceUserShell` and `es.terminalAttached` is true. When input routes to `RouteAI` (or falls back from `ErrUnknownCommandAtStart` to `runAIFallback` in `shell_widget.go:1777`), `s.blocks[blockIdx].liveOutput` is cleared (`nil`), and no `CommandTerminal` is attached to `es.activeTerminal`.
2. **`BlockAITurn` lacks a live virtual terminal:**
   During `runAIFallback`, `Shell` only wires `onFeedback` (which updates a single spinner line `s.blocks[blockIdx].AnimLine`) and `onContent` (which appends markdown text to `s.blocks[blockIdx].Output`). If an unconfigured AI runner or an expired provider credential needs interactive setup/login during an AI turn, there is no `CommandTerminal` or `liveOutputState` attached unless we explicitly open an interactive terminal session (either as a dedicated `BlockCommand` child block like `OnAICommandStart` or by attaching a `CommandTerminal` + `liveOutputState` for the setup/auth command).
3. **Headless / One-Shot (`kabibi -c` / `ExecuteOneShotLine`) Terminal Binding:**
   When `es.terminalAttached` is false (one-shot CLI mode), a `CommandTerminal` backed by `os.Stdin` / `out` (with ANSI/line-mode fallback or raw terminal mode when `os.Stdin` is a host TTY) allows the exact same built-in setup and provider auth code to run unmodified in both TUI and CLI modes.

### 9.3 Flaws in Current Remote Backend & Credential Store (`ai/backend_remote.go`, `ai/auth.go`, `kabibi/credstore`)
Kabibi's current remote backend code has only been tested against synthetic `httptest.NewServer` mocks (`ai/backend_remote_test.go`, `ai/auth_native_test.go`) and has never been exercised against live cloud providers:
1. **Static `APIKey` string in `RemoteEngineConfig`:** `LazyAIRunner` loads `credstore.Get(cfg.Provider, "api-key")` once at engine creation (`ai_runner.go:218`) and bakes it into `RemoteEngineConfig.APIKey`. Expiring OAuth tokens, per-request dynamic headers (e.g. GitHub Copilot `X-Initiator`, `Copilot-Vision-Request`), and token-derived `BaseURL` (Copilot `proxy-ep`) cannot work with a static string.
2. **No serialized read-modify-write in `credstore`:** `kabibi/credstore/credstore.go` only provides `Get`, `Set`, and `Delete` over `map[string]string` with an in-memory `sync.Mutex`. It cannot store structured credentials (`access`, `refresh`, `expires_at`, `client_id`, `account_id`, `enterprise_url`, `available_models`) and does not provide a locked `Modify(provider, fn)` transaction to prevent concurrent requests from double-refreshing a single-use rotating refresh token.
3. **Protocol mismatches with real provider auth:**
   - `buildOpenAIRequest` in `ai/backend_remote.go` only speaks `/v1/chat/completions`, whereas OpenAI's subscription OAuth (`Sign in with ChatGPT` / Codex) targets the OpenAI **Responses API** (`/v1/responses` or `chatgpt.com/backend-api/codex/responses`).
   - `buildAnthropicRequest` only sends `x-api-key`, whereas Anthropic OAuth tokens (`sk-ant-oat...`) require `Authorization: Bearer`, specific `anthropic-beta` flags, Claude Code identity headers, and system prompt prefixing.
   - `ValidateRemoteCredential` in `ai/auth.go` assumes a static API key and hits `/v1/models` with static headers.

---

## 10. Unified Virtual PTY (`CommandTerminal`) Architecture for AI Init & Auth

### 10.1 Core Principle: Narrow Visual Bottleneck, Autonomous Flows
Instead of `OnboardingPrompter` and `wizard.Widget`, **all interactive AI initialization (local vs. cloud selection, hardware tier selection, download consent, progress bars) and all provider authentication flows run inside a virtual terminal (`TerminalSession`)** modeled directly on `CommandTerminal` + `runPager`:

```go
// TerminalSession is the narrow visual & input bottleneck passed to AI init
// and to each backend's Authenticate / Setup flow.
type TerminalSession interface {
    io.Reader // Reads user input (stdin or raw control keys when subscribed)
    io.Writer // Writes ANSI/UTF-8 output to the virtual terminal / block
    Size() (cols, rows int)
    ResizeCh() <-chan struct{} // Optional notification on viewport resize
    IsTTY() bool               // True when backed by interactive Shell TUI or host TTY
    SubscribeRawInput() (readKey func(ctx context.Context, p []byte) (int, error), release func())
}
```

### 10.2 How `Shell` and Built-in Commands Host `TerminalSession`
1. **Built-in Command (`ai` / `ai setup` / `ai login [provider]` / `ai logout [provider]`):**
   - Registered as an in-process shell built-in middleware (alongside `gitBuiltinMiddleware` and `urootBuiltinMiddleware` in `kabibi/ui/shell/shell.go`).
   - When invoked from the shell prompt, `ExecutionSession.RunCommand` already attaches `CommandTerminal` and `liveOutputState`! The built-in command simply wraps `(CommandTerminal, hc.Stdout)` as a `TerminalSession` and runs the setup or provider login flow directly inside the command block.
2. **Automatic Trigger on First Unconfigured AI Turn (or Expired Auth):**
   - When `LazyAIRunner.ensureInitialized(ctx)` sees `!cfg.Configured` (or a provider reports unauthenticated), it requests a `TerminalSession` from its host (`ExecutionSession` / `Shell`).
   - In TUI mode (`es.terminalAttached == true`), `Shell` opens an interactive live terminal block (using the same `liveOutputState` + `es.activeTerminal` wiring as `RunCommand`), routes user keystrokes (`encodeKeyForPTY`) and paste events to `es.activeTerminal`, and freezes the block into scrollback/history when the setup/auth flow finishes—then seamlessly continues the AI turn.
   - In headless/one-shot mode (`es.terminalAttached == false`), `ExecutionSession` provides a stdio `TerminalSession` wrapping `os.Stdin` and `out`. If `os.Stdin` is closed/EOF, reads fail immediately with an actionable error.
3. **Retiring `OnboardingPrompter` and `wizardWidget`:**
   - Remove `s.wizardWidget` and `s.handleWizardEvent` from `Shell`.
   - Keep reusable ANSI menu/prompt/progress rendering helpers as library functions that read from `TerminalSession.SubscribeRawInput()` and write ANSI sequences to `TerminalSession` (just as `runPager` in `kabibi/ui/shell/pager.go` uses `pagerClearScreen`, `\r\x1b[2K`, and `term.ReadControlInputContext`).
   - Any backend is free to use those shared ANSI helpers (arrow-key selector, masked secret input, clickable/copyable URL box, live device-code countdown, multi-bar download progress) **or** render its own custom TUI/stdio interaction over the `TerminalSession`.

---

## 11. Credential Store & Locked Token Refresh Architecture (from `external/pi`)

### 11.1 Structured Credential Record & Locked `Modify`
Learned from `external/pi/packages/ai/src/auth/types.ts` (`CredentialStore`) and `resolve.ts` (`refreshStoredOAuthCredential`):

`kabibi/credstore` should store one JSON-serialized `Credential` per `providerId` in `ai-credentials.json` (native private file with `0600` / Windows protected DACL, or browser `localStorage`):

```go
type CredentialType string

const (
    CredentialAPIKey CredentialType = "api_key"
    CredentialOAuth  CredentialType = "oauth"
)

type Credential struct {
    Type      CredentialType    `json:"type"`
    // For type == "api_key":
    Key       string            `json:"key,omitempty"`
    // For type == "oauth":
    Access    string            `json:"access,omitempty"`
    Refresh   string            `json:"refresh,omitempty"`
    ExpiresAt int64             `json:"expires_at,omitempty"` // Unix millis
    // Provider-specific metadata (e.g. clientId, accountId, enterpriseUrl, availableModels):
    Metadata  map[string]any    `json:"metadata,omitempty"`
}
```

**Critical Concurrency & Rotation Invariant (`credstore.Modify`):**
- `Modify(ctx context.Context, providerID string, fn func(current *Credential) (*Credential, error)) (*Credential, error)` is the **only** write path.
- `Modify` acquires the store lock, reloads the latest on-disk state, passes `current` to `fn`, and if `fn` returns a non-nil `*Credential`, atomically writes and syncs the updated file before releasing the lock.
- **Double-checked locking on refresh (`resolve.ts:122-185`):**
  1. Before every remote model request, the backend checks if `time.Now().Add(5 * time.Minute).UnixMilli() >= cred.ExpiresAt`.
  2. If expiring soon, it calls `credstore.Modify(ctx, providerID, ...)`.
  3. Inside the lock, it re-checks `current`: if `current == nil` (logged out meanwhile) or `!expiresSoon(current)` (another goroutine/process already refreshed it), it returns `current` without making a network call.
  4. **Uncancellable rotation commit (`resolve.ts:113-117`):** Once the HTTP refresh call starts inside `Modify`, the provider may rotate the single-use `refresh_token` immediately. Therefore, the refresh HTTP call and the subsequent disk write use an independent bounded timeout (`15s`, `context.WithoutCancel(ctx)`) rather than aborting mid-flight if the caller cancels the prompt; otherwise a cancelled turn would discard the only valid refresh token.
  5. **No silent fallback (`resolve.ts:30-33`):** A stored credential *owns* the provider. If refresh fails, return an explicit auth error prompting re-login; never silently fall back to ambient environment variables.

---

## 12. Detailed Provider Specifications (Auth, Refresh, and Request Wire Protocols)

Each backend implements a self-contained provider interface:
- `Login(ctx context.Context, term TerminalSession) (*Credential, error)`
- `Refresh(ctx context.Context, cred *Credential) (*Credential, error)`
- `PrepareRequest(ctx context.Context, cred *Credential, cfg ConversationConfig, parts []PromptPart) (remoteRequest, error)`

Below is the exact specification for all five target providers, grounded in `external/pi/packages/ai/src/auth/oauth/*` and `external/pi/packages/ai/src/api/*`.

### 12.1 OpenRouter (`openrouter`)
- **Supported Auth Modes:**
  1. **OAuth PKCE (`Sign in with OpenRouter`)** — Reference: `external/pi/packages/ai/src/auth/oauth/openrouter.ts`.
  2. **Manual API Key** (`OPENROUTER_API_KEY` or pasted `sk-or-...`).
- **OAuth Login Recipe (in `TerminalSession`):**
  1. Generate a 32-byte random PKCE `verifier` (base64url) and `challenge = BASE64URL(SHA256(verifier))` (`S256`).
  2. Start an ephemeral loopback HTTP callback server on `127.0.0.1:0` with a random capability path `/oauth/callback/<uuid>` (since OpenRouter does not echo an OAuth `state` parameter, the unguessable path prevents stray local requests from hijacking the callback).
     - *Note on Rusticated:* As demonstrated in `demo-go/server/main.go`, `net/http` server listeners work natively on the Rusticated sysroot. In a browser host (`assets.HostOS() == "browser"`), if `net.Listen` is unavailable, skip the loopback listener and prompt for manual paste of the callback URL/code.
  3. Build `https://openrouter.ai/auth?callback_url=<redirectUri>&code_challenge=<challenge>&code_challenge_method=S256`.
  4. Display the URL in `TerminalSession` (and attempt to open the browser on desktop), while simultaneously racing the loopback server against a manual-input line/key reader in `TerminalSession` so headless/SSH users can paste the redirect URL or `code`.
  5. Exchange the code via `POST https://openrouter.ai/api/v1/auth/keys` with JSON body `{"code": code, "code_verifier": verifier, "code_challenge_method": "S256"}` (30s timeout).
  6. Extract `body.key` (a permanent `sk-or-...` API key) and store `Credential{Type: CredentialOAuth, Access: key, ExpiresAt: math.MaxInt64}`.
- **Refresh Logic:** No-op (the exchanged key is permanent until revoked by the user in OpenRouter).
- **Model Catalog & Request Wire Protocol:**
  - Can query `GET https://openrouter.ai/api/v1/models/user` with `Authorization: Bearer <key>` to list models permitted for that user's key/guardrails (`external/pi/packages/ai/src/providers/openrouter.ts:28-35`).
  - Uses OpenAI Chat Completions wire format (`POST https://openrouter.ai/api/v1/chat/completions`, `Authorization: Bearer <Access>`).

### 12.2 Anthropic (`anthropic`)
- **Supported Auth Modes:**
  1. **Claude Pro/Max Subscription OAuth** — Reference: `external/pi/packages/ai/src/auth/oauth/anthropic.ts` and `src/api/anthropic-messages.ts`.
  2. **Manual API Key** (`ANTHROPIC_API_KEY`, `sk-ant-api...`).
- **OAuth Login Recipe (in `TerminalSession`):**
  1. In `TerminalSession`, let the user choose between:
     - **Browser login (default):** Loopback server on `127.0.0.1:53692` (falling back to port `0` if `53692` is in use), `redirect_uri = http://localhost:<port>/callback`, raced against manual URL/code paste.
     - **Copy-code login (headless):** Uses `redirect_uri = https://platform.claude.com/oauth/code/callback`, where Anthropic displays `code#state` in the browser for the user to copy and paste into `TerminalSession`.
  2. Client ID: `9d1c250a-e61b-44d9-88ed-5944d1962f5e` (`anthropic.ts:13`).
  3. Scopes: `"org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"`.
  4. Generate PKCE (`verifier`, `challenge`), set `state = verifier`, and direct user to `https://claude.ai/oauth/authorize?code=true&client_id=...&response_type=code&redirect_uri=...&scope=...&code_challenge=...&code_challenge_method=S256&state=...`.
  5. Parse callback or manual input (`URL` with `?code=&state=` or `code#state`), verify `state == verifier`.
  6. Exchange via `POST https://platform.claude.com/v1/oauth/token` (JSON body: `grant_type: "authorization_code"`, `client_id`, `code`, `state`, `redirect_uri`, `code_verifier`).
  7. Store `Access: access_token`, `Refresh: refresh_token`, `ExpiresAt: now + expires_in*1000 - 5*60*1000`.
- **Refresh Logic:**
  - `POST https://platform.claude.com/v1/oauth/token` with JSON `{"grant_type": "refresh_token", "client_id": CLIENT_ID, "refresh_token": cred.Refresh}` (note: **omit `scope`** on refresh, verified by `anthropic-oauth.test.ts:145`).
  - Persist rotated `access_token`, `refresh_token`, and new `ExpiresAt`.
- **Critical Request Wire Protocol Differences for Anthropic OAuth (`anthropic-messages.ts:1024-1262`):**
  - **Kabibi's current `buildAnthropicRequest` (`ai/backend_remote.go:330`) will fail with OAuth tokens unless updated:**
  - Detect OAuth token (`cred.Type == CredentialOAuth` or `strings.Contains(token, "sk-ant-oat")`).
  - **Headers:** Instead of `x-api-key`, send `Authorization: Bearer <access_token>`, `anthropic-version: 2023-06-01`, `anthropic-beta: claude-code-20250219,oauth-2025-04-20` (plus `interleaved-thinking-2025-05-14` when thinking is enabled), `user-agent: claude-cli/2.1.280`, and `x-app: cli`.
  - **System Prompt:** Anthropic OAuth requires the first system text block to be `{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}`, followed by Kabibi's system prompt as a second text block in the `system` array (`anthropic-messages.ts:1246-1262`).
  - **Tool Name Casing:** Map tool names that collide case-insensitively with Claude Code built-ins (`Read`, `Write`, `Edit`, `Bash`, `Grep`, `Glob`, etc.) on the wire and map them back on `tool_use` blocks (`anthropic-messages.ts:95-126, 756-759`).

### 12.3 OpenAI (`openai` — API Key & Sign in with ChatGPT)
- **Supported Auth Modes:**
  1. **Sign in with ChatGPT (Subscription OAuth)** — Reference: `external/pi/packages/ai/src/auth/oauth/openai-chatgpt.ts` (current direct-token flow) and `openai-codex.ts` (device-code & Codex flow).
  2. **Manual API Key** (`OPENAI_API_KEY`, `sk-...`).
- **OAuth Login Recipe (in `TerminalSession`):**
  - **Option A — Direct API Token Sharing (`openai-chatgpt.ts`):**
    1. Generate or load a persistent installation UUID (`urn:uuid:<uuid>`).
    2. Start loopback server on `127.0.0.1:1455/auth/callback` (raced against manual paste of the full callback URL).
    3. Direct user to `https://auth.openai.com/api/accounts/authorize` with `client_id=dynamic_agent_client`, `agent_name_hint=Kabibi`, `ext_agent_host_id=urn:uuid:<uuid>`, `response_type=code`, `redirect_uri=http://127.0.0.1:1455/auth/callback`, `resource=https://api.openai.com/v1`, `scope=openid profile email offline_access resource.invoke chatgpt.tokens.use.direct`, `state`, `nonce`, and PKCE `S256`.
    4. Callback receives `code`, `state`, and a dynamically issued `client_id` (`oaiapp_...`).
    5. Exchange at `POST https://auth.openai.com/api/accounts/oauth/token` (`application/x-www-form-urlencoded`) with `grant_type=authorization_code`, `client_id=<issued_client_id>`, `code`, `code_verifier`, `redirect_uri`, `resource=https://api.openai.com/v1`.
    6. Verify `id_token` is non-empty and `scope` contains `chatgpt.tokens.use.direct`. Store `Access`, `Refresh`, `ExpiresAt` (3 min margin), and `Metadata["client_id"] = issuedClientId`.
  - **Option B — Headless Device Code (`openai-codex.ts:190-275`):**
    1. `POST https://auth.openai.com/api/accounts/deviceauth/usercode` with `{"client_id": "app_EMoamEEZ73f0CkXaXp7hrann"}`.
    2. Display `user_code` and `https://auth.openai.com/codex/device` in `TerminalSession`.
    3. Poll `POST https://auth.openai.com/api/accounts/deviceauth/token` (`403`/`404` or `deviceauth_authorization_pending` = pending; `slow_down` = +5s interval) until it returns `{authorization_code, code_verifier}`, then exchange at `https://auth.openai.com/oauth/token`.
- **Refresh Logic:**
  - `POST https://auth.openai.com/api/accounts/oauth/token` with `grant_type=refresh_token`, `client_id=<stored_client_id>`, `refresh_token=<cred.Refresh>`, `resource=https://api.openai.com/v1`.
  - **Must** receive and persist a rotated `refresh_token` (`openai-chatgpt-oauth.test.ts:162`).
- **Critical Request Wire Protocol Impact (`src/api/openai-responses.ts`):**
  - Tokens from `Sign in with ChatGPT` (`!strings.HasPrefix(token, "sk-")`) target the **OpenAI Responses API** (`POST https://api.openai.com/v1/responses`), **not** `/v1/chat/completions`, and reject fields like `temperature`, `max_output_tokens`, and `prompt_cache_retention` (`openai-responses.ts:49-55, 368-385`).
  - Therefore, `ai/backend_remote.go` needs a `buildOpenAIResponsesRequest` + Responses SSE accumulator (`response.output_text.delta`, `response.function_call_arguments.delta`, `response.output_item.done`) alongside `buildOpenAIRequest`, or should use `/v1/responses` for OpenAI models generally.

### 12.4 GitHub Copilot (`github-copilot`)
- **Supported Auth Modes:**
  1. **GitHub Device-Code OAuth (`github-copilot.ts`)** — Reference: `external/pi/packages/ai/src/auth/oauth/github-copilot.ts`.
  2. **Environment/Token** (`COPILOT_GITHUB_TOKEN`).
- **OAuth Login Recipe (in `TerminalSession`):**
  1. Prompt in `TerminalSession` for optional GitHub Enterprise domain (blank = `github.com`).
  2. `POST https://<domain>/login/device/code` (`client_id = "Iv1.b507a08c87ecfe98"`, `scope = "read:user"`).
  3. Validate `verification_uri` is `http(s)://`, display `user_code` and `verification_uri` in `TerminalSession`.
  4. Poll `POST https://<domain>/login/oauth/access_token` with `grant_type=urn:ietf:params:oauth:grant-type:device_code` using RFC 8628 device-code polling (`waitBeforeFirstPoll: true`, honoring server `interval` on `slow_down` to survive VM/WSL clock drift, `device-code.ts:81-90`).
  5. The resulting `ghu_...` token is stored as `cred.Refresh` (the long-lived GitHub token).
  6. Exchange `cred.Refresh` for a short-lived Copilot session token via `GET https://api.<domain>/copilot_internal/v2/token` with `Authorization: Bearer <ghu_token>` and `COPILOT_HEADERS` (`User-Agent: GitHubCopilotChat/0.35.0`, `Editor-Version: vscode/1.107.0`, `Editor-Plugin-Version: copilot-chat/0.35.0`, `Copilot-Integration-Id: vscode-chat`).
  7. Parse `proxy-ep=...` from the semicolon-delimited Copilot token (`tid=...;exp=...;proxy-ep=proxy.individual.githubcopilot.com;...`) and replace `^proxy\.` with `https://api.` to derive the account-specific `baseUrl` (`github-copilot.ts:62-80`).
  8. Fetch `GET <baseUrl>/models` (`X-GitHub-Api-Version: 2026-06-01`), filter to tool-capable models, and auto-enable unconfigured policy models via `POST <baseUrl>/models/<id>/policy` (`{"state": "enabled"}`), storing `availableModelIds` in credential metadata.
- **Refresh Logic:**
  - When `cred.Access` (the Copilot session token) is within 5 minutes of `expires_at`, call `GET https://api.<domain>/copilot_internal/v2/token` using `cred.Refresh` (`ghu_...` token) and refresh `availableModelIds` (`maxRetries: 0` during token refresh so rate-limiting doesn't block requests).
- **Request Wire Protocol (`src/providers/github-copilot.ts`, `src/api/github-copilot-headers.ts`):**
  - Sends requests to the token-derived `baseUrl` (`https://api.individual.githubcopilot.com` or enterprise equivalent) with `Authorization: Bearer <copilot_session_token>`, `COPILOT_HEADERS`, `Openai-Intent: conversation-edits`, `X-Initiator: "user"` (if last message is user) or `"agent"` (if following tool results/assistant), and `Copilot-Vision-Request: "true"` when images are present.
  - Claude models on Copilot use `/v1/messages` (Anthropic wire format with Bearer auth); GPT/Grok/Gemini models on Copilot use `/chat/completions` or `/responses`.

### 12.5 Google Gemini (`gemini`)
- **Supported Auth Modes:**
  - **API Key Only (`GEMINI_API_KEY` or interactive key input in `TerminalSession`)** — Reference: `external/pi/packages/ai/src/providers/google.ts`.
  - Per `external/pi` and project scope decisions, Gemini Developer API (`https://generativelanguage.googleapis.com/v1beta`) uses API keys cleanly without OAuth ceremony.
- **Setup & Request Flow (in `TerminalSession`):**
  1. Prompt for the Gemini API key in `TerminalSession` with masked input (`*` echo in raw mode), validate against `GET https://generativelanguage.googleapis.com/v1beta/models` (`x-goog-api-key`), let the user pick from discovered/curated Gemini models, and persist `Credential{Type: CredentialAPIKey, Key: apiKey}` via `credstore.Modify`.
  2. Existing `buildGeminiRequest` and `consumeGemini` in `ai/backend_remote.go` handle `streamGenerateContent?alt=sse`.

---

## 13. Shared Building Blocks vs. Provider-Owned Logic

To keep the codebase modular without forcing artificial unification:

### 13.1 Shared Reusable Building Blocks (`ai/authutil/` or `ai/` internal helpers)
1. **Terminal UI Micro-Helpers (`TerminalSession` utilities):**
   - `PromptSelect(ctx, term, title, options, defaultIdx)` — renders an interactive arrow-key / numbered selector using `\r\x1b[2K` / ANSI redraws (like `runPager`) when `term.IsTTY()` is true, or numbered line prompt when false.
   - `PromptLine(ctx, term, prompt, secret)` — reads a line; when `secret == true`, subscribes to raw control input so characters are masked (`*`) in both TUI and stdio TTY modes (fixing the existing `StdioOnboardingPrompter.Input` bug).
   - `RenderProgress(term, stage, detail, percent)` — renders carriage-return progress bars directly into the active command terminal.
2. **PKCE Generator (`GeneratePKCE() (verifier, challenge string, err error)`):**
   - Standard `crypto/rand` 32-byte base64url verifier + `crypto/sha256` base64url `S256` challenge (used by OpenRouter, Anthropic, and OpenAI).
3. **Loopback Callback Server + Manual Input Race (`WaitForLoopbackOrManualCode`):**
   - Adapted from `external/pi/packages/ai/src/auth/oauth/callback-server.ts`.
   - Starts an `http.Server` on `127.0.0.1:<port>` (using standard Go `net/http`, which works on both native and Rusticated sysroot as shown in `demo-go/server/main.go`), serves the HTML success/error page, calls `Close()` on all active connections on shutdown (`openai-chatgpt.ts:290-296`), and races the callback channel against `PromptLine` on `TerminalSession` (cancelling the prompt's child `ctx` as soon as the HTTP callback wins).
4. **RFC 8628 Device-Code Poller (`PollDeviceCodeFlow`):**
   - Adapted from `external/pi/packages/ai/src/auth/oauth/device-code.ts`.
   - Handles `waitBeforeFirstPoll`, deadline tracking, cancellation via `ctx.Done()`, `+5s` interval increment on `slow_down`, and server-supplied `interval` override (preventing infinite early polling under WSL/VM clock drift). Used by GitHub Copilot and OpenAI Device Auth.
5. **Locked Credential Store & Refresh Coordinator (`credstore.Modify` + `ResolveProviderAuth`):**
   - Adapted from `external/pi/packages/ai/src/auth/resolve.ts`.
   - Provides double-checked locking and uncancellable token-rotation persistence.

### 13.2 What Remains Strictly Provider-Owned
- Each provider owns its own `Login(ctx, term)` state machine, endpoints, client IDs, scopes, token exchange payload format (JSON vs. form-urlencoded), model discovery/enabling calls (e.g., Copilot `/models/<id>/policy`, OpenRouter `/models/user`), `Refresh(ctx, cred)` implementation, and `PrepareRequest` wire headers/body adjustments.
- Neither `Shell` nor `ai.Session` knows how any provider authenticates; they only attach a `TerminalSession` when setup or login is invoked and pass resolved request auth to the backend.

