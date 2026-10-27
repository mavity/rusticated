# AI model onboarding and command panel behavior

## Status

Implemented. The focused AI/onboarding/panel tests pass, and the Kabibi
vegetable builds through the documented root build path. The broader
`ui/shell` and `ui/app` suites still report unrelated failures, so they are not
reported as passing.

## Previous behavior

`/model` starts `ai setup`. The setup flow first asks whether to use local Gemma
or a cloud provider, then asks for a tier or provider. Local setup separately
asks for download consent. Remote setup asks for provider consent, performs
authentication when needed, and then opens a separate model-selection prompt.
The shared terminal selector renders one option per line and has no search.

When blue file panels are visible, ordinary command execution currently leaves
them visible while the command runs. Panel visibility is controlled by the app
widget, while command execution and completion are managed by the shell.

## Intended `/model` experience

Replace the multi-step top-level wizard with a contextual first menu:

- If AI is configured, show the current model/configuration as an immediate
  action to keep it and exit without changing anything.
- If credentials exist for the current remote configuration, offer a
  re-authenticate action that retains the current provider, endpoint, and model.
- Show each supported Gemma 4 tier, subject to host capability: E2B, E4B, 12B,
  26B MoE, and 31B.
- Show one entry per supported cloud provider: OpenRouter, Anthropic, OpenAI,
  GitHub Copilot, and Gemini.
- Show Custom endpoint and Cancel.

Selecting a cloud provider does not add a separate confirmation prompt. The
model menu discloses that prompts and tool results go to the selected provider
and that provider billing/data-use policies may apply. Reuse a valid saved
credential without asking the user to authenticate again. After authentication,
present the provider's model catalog in a searchable picker rather than adding
another wizard layer. Preserve the existing preferred/current-model selection
as the initial highlight. Keep an explicit way to enter an exact model ID when
it is absent from the catalog.

The model picker should use the available terminal width for multiple columns,
and limit its visible list height to terminal height minus a small fixed area
(approximately 2-3 rows for search/status and key help). Typing filters
immediately; Backspace edits the query; Up/Down moves the selection; Enter
accepts; Escape cancels. The cursor and selected item remain visible while the
filtered list scrolls. Resize must recalculate columns and visible rows.
Non-TTY/stdin setup retains a simple line-oriented flow rather than requiring
raw key input.

Provider entries show provider icons, while catalog entries use icons inferred
from the model family, including Qwen and GLM/z.ai/Zhipu AI. Keep and Login
entries place the current model's icon after the action label. Repeated
namespace/model prefixes are removed from the display label without changing
the model ID saved to configuration. X/Twitter family models are excluded from
every provider catalog and cannot be selected through manual model-ID entry
or the keep-current action.

## Gemma selection and download consent

Check the selected model tier and all other assets required for that host
(runtime and, where applicable, embedding model) before asking for consent.

- If nothing needs downloading, save/select the tier immediately without a
  confirmation prompt.
- If any required assets are missing, ask once and report only the actual
  remaining download sizes and total. Never show a consent prompt claiming a
  zero-byte download.
- Keep current cache integrity/error handling and host capability checks.

## Custom endpoint

Accept an endpoint URL and support all existing remote request protocols:
OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, Gemini
generateContent, and the GitHub Copilot protocol. Reuse existing request
adapters and provider-specific authentication where applicable; represent the
custom endpoint/protocol explicitly in persisted configuration rather than
misidentifying it as a built-in provider.

Attempt protocol detection only when the endpoint's response or URL provides
reliable evidence. If detection is ambiguous, ask the user to select a protocol
before requesting credentials or sending authenticated requests. Validate the
URL and credential using the selected protocol, then use the same searchable
model picker. Persist the endpoint, protocol, model, and credential association
so later selection and re-authentication use the same identity.

## Blue-panel behavior during commands

Make panel visibility follow command intent centrally, instead of handling
`/model` as a special case:

- Before any ordinary submitted shell/AI command starts, if the panels are
  visible, hide them for the duration of execution.
- Restore the prior panel visibility when execution finishes, fails, or is
  cancelled.
- Exempt commands whose purpose is to show or operate on the file panels,
  including `/panels`, `/copy`, `/move`, `/edit`, `/delete` (and `/remove`),
  and `/rename`. These commands retain their existing panel-opening and
  file-operation behavior.
- Do not change visibility for commands submitted while panels were already
  hidden.
- Preserve existing callbacks and ensure restoration happens once on every
  completion path.

The implementation belongs at the app/shell command lifecycle boundary so
typed commands, `/model`-launched setup, and AI turns receive consistent
behavior. Slash commands that are handled directly by the app remain governed
by their explicit panel behavior.

## Implementation sequence

1. Add a reusable searchable, responsive terminal picker and focused tests for
   filtering, selection, scrolling, resize, cancellation, and non-TTY behavior.
2. Refactor `/model` and `ai setup` to use the contextual first menu and
   streamline local and remote selection while preserving provider safeguards.
3. Add custom endpoint protocol persistence, detection/selection, validation,
   authentication, and model-catalog integration for all listed protocols.
4. Add cache-aware, accurate Gemma consent behavior.
5. Add centralized temporary panel hiding/restoration with explicit exceptions
   for panel/file operations.
6. Update user-facing AI setup documentation and run targeted Go tests, browser
   or platform-specific checks where relevant, and the documented custom
   vegetable build.

## Acceptance criteria

- Existing configuration can be kept from the first menu without further
  prompts; re-authentication is offered only when a stored credential exists.
- All supported Gemma tiers and cloud providers are represented, with host
  constraints respected.
- Cached required assets produce no download confirmation; missing required
  assets produce one accurate consent prompt with no zero-byte message.
- Provider models can be searched and chosen from a multi-column list that
  fits the terminal; filtering and keyboard controls work without an extra
  text-entry screen.
- Custom endpoint setup supports every listed protocol, asks for a protocol
  when it cannot reliably detect one, and does not send credentials before the
  protocol is selected.
- Blue panels hide only during ordinary command execution and return to their
  previous state afterward; panel/file commands keep their intended behavior.
- Cancellation, errors, and command failures do not leave panel visibility or
  AI configuration in a partial state.