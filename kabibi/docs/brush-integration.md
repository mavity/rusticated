# Brush integration plan

The goal is to bring brush into `kabibi` in a clean, staged way.
We will use `rusticated` as the async runtime and build the integration in three stages.
The eventual structure is:

- `/kabibi`
- `/suklay`  ← a brush submodule inside kabibi

## Stage 1: Migrate brush onto rusticated with a simple backend

This stage is about making brush run on `rusticated` and nothing more.
We do not yet try to render shell content inside the kabibi UI.
The shell should run on a plain black terminal using a simple backend.

What to do:
- Add the brush crates to the workspace or make them path dependencies inside kabibi.
- Remove Tokio runtime setup from `brush-shell`.
- Replace `tokio::runtime::Builder`, `runtime.block_on(...)`, and `std::process::exit` control flow.
- Make `brush-core` use rusticated async primitives instead of `tokio::process`, `tokio::net`, and tokio signal helpers.
- Replace the terminal and stdin backend assumptions in `brush-interactive` with a simple basic backend that can run with rusticated I/O.
- Keep the shell output as plain text on a terminal screen.
- Do not attempt ratatui rendering or kabibi widget integration yet.

Success criteria for stage 1:
- brush can build and run using the rusticated async runtime.
- brush can accept input and print output on a black terminal.
- the shell works with a simple basic backend and no advanced terminal features.

## Stage 2: Add raw tty support using rusticated terminal APIs

This stage makes the shell interactive using the raw tty features provided by rusticated.
We keep brush as a shell process, but the terminal becomes more like a real interactive shell.

What to do:
- Use rusticated raw tty APIs in `kabibi` and in the brush backend.
- Add support for terminal resize events, raw input mode, and direct tty write.
- Keep the brush input path simple and deterministic.
- Avoid using the existing `reedline` terminal backend if it depends on Tokio or blocking std I/O.
- Keep the UI still separate from kabibi rendering.

Success criteria for stage 2:
- brush can run in raw tty mode on rusticated.
- it can react to resize events and user keys in real time.
- the shell still uses a simple backend, but now with real terminal I/O.

## Stage 3: Custom brush backend inside kabibi with ratatui rendering

This is the final stage.
brush gets a custom backend that renders into kabibi's `ratatui` widgets, and receives input from kabibi's input stream.

What to do:
- Implement a custom brush input backend for kabibi.
- Push keypresses from kabibi into brush instead of letting brush read stdin directly.
- Capture brush output and render it through kabibi's `ratatui` layout.
- Keep shell state and application state separate, but connected.
- Make `/suklay` the brush submodule that lives inside `/kabibi` and provides the shell logic.
- Ensure kabibi can show file panels, prompt, AI chat, and shell output together.

Success criteria for stage 3:
- brush is embedded in kabibi as a shell widget.
- brush input is filtered through kabibi’s key event loop.
- brush output appears inside the kabibi UI, not as raw terminal text.

## Important aspects to keep in mind

- The first stage is mandatory. No excuses. We must get brush running on rusticated first.
- Keep the backend simple at first.
- Do not mix Tokio or blocking std I/O into the runtime layer.
- The path is: migrate brush to rusticated → add raw tty support → embed brush into kabibi.
- Keep `kabibi` and `suklay` clearly separated until stage 3.
- Use plain terminal behavior in stage 1, raw tty in stage 2, and ratatui rendering in stage 3.
- Test at every stage with a minimal shell session before moving on.

## Practical next steps

1. Audit `brush-shell/src/entry.rs`, `brush-interactive/src/*`, and `brush-core/src/sys/*`.
2. Replace Tokio runtime and async primitives with rusticated equivalents.
3. Build a minimal shell binary on black terminal and verify it runs.
4. Add raw tty support for resize and raw mode.
5. Implement the custom kabibi backend and renderer.

This plan keeps the work manageable and avoids trying to solve the full UI integration too early.
