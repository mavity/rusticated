# MOHABBAT

A plan for building `mohab.bat` — a single polyglot file that bundles a
WASM payload with native Wasmtime hosts for all major 64-bit desktop
platforms, so the same file can be executed on Linux, Windows, and macOS.

This document is the working specification. It is intentionally plain and
exhaustive. Every known complication is called out where it belongs.

---

🧿👖🧿👖🧿🧿👖🧿🧿👖🌊🧿🌊🌊🌊🧿🌊🌊🌊🌊🌊🟦🌊🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🩵🟦🟦🩵🟦🩵🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🌊🟦🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊
🧿👖🧿👖👖🌊👮🧿🌊🧿🌊🌊🧿🌊🌊🌊🌊🧿🌊🌊🟦🌊🟦🌊🟦🌊🟦🟦🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🟦🟦🟦🟦🌊🟦🌊🟦🌊🟦🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊
🧿👖🌊🧿🌊🧿🧿👖🧿🌊🌊🌊🌊🌊🟦🌊🟦🌊🟦🧿🌊🟦🌊🟦🟦🟦🟦🟦🟦🟦🟦🟦🟦🩵🟦🩵🟦🩵🟦🟦🩵🟦🟦🩵🟦🟦🩵🟦🟦🟦🩵🟦🟦🩵🟦🟦🩵🟦🟦🟦🟦🟦🟦🌊🟦🌊🌊🟦🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊
🌊🌊👖🌊🧿🌊🌊🌊🌊🧿🌊🌊🌊🌊🌊🟦🌊🌊🌊🟦🧿🟦🌊🟦🟦🟦🟦🟦🟦🟦🌊🟦🩵🟦🟦🩵🟦🟦🩵🟦🟦🩵🟦🟦🟦🌊🟦🟦🩵🟦🟦🩵🌊🌊🟦🟦🟦🟦🟦🟦🟩🟦🌊🟦🌊🟦🌊🌊🟦🌊🟦🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊
🌊🧿🌊🌊🧿🌊🧿🌊🌊🌊🌊🟦🌊🟦🌊🟦🌊🟦🌊🟦🌊🟦🟦🟦🌊🟦🟦🟦🟩🟫🟡🟩🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🟦🟩🟡🟡⚫🟦🟦🩵🌊🟫🟡🟡🟩🟦🩵🟦🟦🟦🟦🟦🟦🌊🟦🌊🟦🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊
🌊🌊🌊🧿🌊🧿🌊🌊🌊🌊🌊🌊🌊🟦🌊🌊🟦🌊🟦🌊🟦🟦🌊🟦🟦🟦🟦🟩🟡🟡🟡🟩🟦🟦🩵🟦🟦🩵🟦🩵🟦🩵🌊🟫🟡🟡⚫🩵🟦🟦👖🟨🟡🟡🟦🟦🟦🟦🟦🟦🟦🌊🟦🟦🌊🟦👮⚫🟦🌊🌊🟦🌊🌊🌊🌊🌊🌊🌊🌊🌊
🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🟦🌊🟦🌊🌊🌊🟦🟦🌊🟦🟦🟦🟦🟦⚫⚫🟡🟡🟩🟦🩵🟦🩵🟦🟦🟦🟦🩵🟦🟦⚫🟡🟡⬛🟦🩵🟦🟦⬛🟡🟡🌊🩵🟦🟦🩵🟦🟦🟦🟦🌊🟦🌊🌊🟡⚫🌊🌊🌊🌊🌊🌊🟦🌊🌊🌊🌊🌊
🌊🌊🌊🌊🌊🌊🌊🧿🌊👖🟨🟩👖🟫🟩⚫🌊🟨🟩👖🟦🟦🟦🟦🟦🟦🟦🟦👖🟡🟡🟩🟦🟦🟦🟦🩵🟦🩵🟦🟦🟦🩵🟦🟡🟡⚫🟦🟦🩵🟦⚫🟡🟡🟦🟦🟦🟦🟦🟦🟩🌊🌊🌊🌊🟦👖🟡🟡🟩🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊🌊
🌊🌊🟦🌊🌊🌊⚫🟡🟡🟡🟡🟨🟡🟡🟡🟡🟡🟡🟡🟡🌊🟦🟦🌊🌊🌊🟩🟦🟦🟡🟡🟩🟦🟦🟩🟦🟦🌊🟩🟡🟡🌊🟦🟦🟡🟡⬛⚫🟩⚫🩵🌌🟡🟡⚫🟫🟩🌊🩵🟦🟫🟡🟡🟡🟫🟩🟨🟡🟡🟡🟡🟩🌊🌊🌊🌊🌊🌊🌊🌊🌊
🌊🌊🌊🟦🌊🌊🌊⬛⬛🟡🟡🟡🟫🟡🟡🟡🟫🟡🟡🟡🌊🟦🫐🟡🟡🟡🟡🟩🟦🟡🟡🟨🟡🟡🟡🟩⚫🟡🟨🟫🟡🟡🌊🩵🟡🟡🟫🟡🟡🟡⚫⬛🟡🟡🟡🟡🟡🟡🟦🟩🟡⚫⬛🟡🟡⚫⚫🟡🟡🟫⬛⚫🌊🌊🟦🌊🌊🌊🌊🌊🌊
🌊🟦🌊🌊🌊🟦👮🌊🌊🟡🟡🟡⬛🟩🟡🟡⚫🟩🟡🟡🟩🌊🟡🟡🟡🟡🟡🟨🌊🟡🟡🟡🟫🟡🟡🟨⬛🟫⚫⚫🟡🟡🫐🟦🟡🟡🟡⚫🟡🟡🟨⬛🟡🟡⚫🟫🟡🟡🟩🟨⬛👖⚫🟡🟡⚫🌊🟡🟡🟫🌊🌊🌊🌊🌊🌊🟦🌊🌊🌊🌊
🌊🌊🟦🌊🌊🌊🟦🌊🌊🟡🟡🟡🌊🟨🟡🟡🟩🟨🟡🟡🟩🌊🟡🟡🟡⬛⬛🟡⚫🟡🟡🟩⬛⬛🟡🟨⬛⚫🟩🟨🟡🟡🟩🟦🟡🟡⬛🫐🟨🟡🟡⚫🟡🟡🫐🫐🟡🟡🌊⚫👖👖🟡🟡🟡⚫🟩🟨🟡🟩🌊🟦🟩🌊🟦🌊🌊🌊🌊🌊🌊
🌊🌊🌊🟦🟦🌊🌊🟦🌊🟡🟡🟡🌊🟩🟡🟡🌊🟩🟡🟡🟩🟫🟡🟡⬛🌊🌊🟡⬛🟡🟡🟨🟦🟡🟡🟩🟦⚫🟡🟨🟡🟡⚫🩵🟡🟡⚫🟦🟩🟡🟡⚫🟡🟡🟦🟦🟡🟡⚫⚫🟡🟡⚫🟡🟡⚫🟦🟡🟡🟨🌊🌊🟦🌊🌊🟨🌊🌊🌊🌊🌊
🌊🟦🌊🌊🌊🟦🟦🌊🟦🟩🟡🟡🟩🟫🟡🟡🟩🟩🟡🟡🟫🟩🟡⚫🟦🟦⚫🟡⬛🟡🟡🟩🌊🟡🟡🟫🟩🟡🟨⚫🟡🟡🟩🟦🟡🟡⚫🩵🟡🟡🟡🟩🟡🟡🟦🟩🟡🟡⚫🟩🟡🟨👖🟡🟡🟫🟩🟨🟡🟡🌊🌊🌊🌊🌊⚫🌊🟩🌊🌊🌊
🌊🌊🟦🌊🟦🌊🟦🟦🌊🟡🟡🟡🟦🟩🟡🟡🌊👖🟡🟡🟫🟨🟡🟩🟦🟦🟡🟡⬛🟡🟡🟨🟩🟡🟡⬛🟡🟡🟫🟩🟡🟡🟩🟦🟡🟡⬛🟦🟩🟡🟨🟩🟡🟡🟦🌊🟡🟡🟩🟡🟡⚫🟦🟡🟡🟫🟦🟨🟡🟡🟩🌊🟦🟩👖⚫🌊🌊🌊🟦🌊
🌊🌊🌊🟦🌊🟦🌊🟦🌊🟡🟡🟡🌊🟩🟡🟡🟩🌊🟡🟡🟡🟩🟡🌊🟦🟫🟡🟡🫐🟡🟡🟫🟩🟡🟡⚫🟡🟡🟨🟩🟡🟡🟡🟩🟡🟡🟩⚫🟡🟡⬛🟡🟡🟡🌊🟩🟡🟡🌊🟡🟡🟡🟫🟡🟡🟡🌊🟩🟡🟡🟨🌊🌊🌊🟨🌊🌊🟦🌊🌊🌊
🌊🟦🌊🟦🌊🟦🟦🌊🌊🟡🟡🟡🟦🟩🟡🟡🟩🌊🟡🟡🟡⬛🟡🟡🟡🟡🟡🟫🟩🟡🟡🟩🟫🟡🟡⬛🟡🟡🟡🟡🟡🟡🟨🌊🟡🟡🟡🟡🟡🟡🌊🟡🟡🟡🟡🟡🟡🟨🟦🟩🟡🟡🟡🟡🟡🟡🫐⚫🟡🟡🟡🟡🟡🟡⚫🟩🌊🌊🌊🟩🌊
🌊🌊🟦🌊🟦🌊🌊🟩🟡🟡🟡🟡🟩🟨🟡🟡⚫🌊🟡🟡🟡🟨⬛🟡🟡🟡🟫🌊👖🟫🟫🟫🟩🟡🟡🟨⬛🟫🟫⬛🟫🟩🟫⚫🟫🟫⚫🟫🟨⚫🫐🟫🟫⚫🟫🟨🟫⚫🟦🫐⚫🟫⬛⚫⚫⚫🟩🌊⚫🟡🟡🟡🟡🟫🫐🌊🟦🌊🟦🌊🌊
🌊🟦🌊🟦🟦🟦⚫⬛⬛⬛⬛⬛🟦🫐⬛⬛🟦🟦🟩🟡🟡🟡🫐⬛⬛⬛⚫🟦🟩⬛⬛🌊🟦🟡🟡🟡🟡⚫⚫🌊⚫⬛👮🟦⚫⬛🟦⚫⬛🌊🩵🫐⚫🌊⚫⬛⚫🟦🩵🟦👖⚫🌊🌊⚫🫐🌊🟦⚫⬛⬛⬛⬛⚫🌊🟦🌊🌊🟩🌊🟦
🟦🌊🟦🌊🟦🌊🌊🌊🟦🌊🟦🟦🟦🟦🟦🟦🩵🟦⚫🟡🟡🟡🟡🌊🟦🟦🩵🟦🟫🌊🩵🟦🩵⬛🟨🟡⬛🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🩵🟦🟦🟦🟦🟦🟦🟦🟩🟦👖👮🌊🟦🌊🟩🌊🟦🌊🟦🌊
🌊🟦🟦🟦🌊🟦🟦🟦🟦🟦🟦🟦🟦🩵🟦🟦🟦🩵🟦🟡🟡🟡🟡🟡🟩🫐🟩🟨🟩🟦🩵🟦🩵🟦⬛⬛🌊🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🩵🟦🟦🟦🩵🟦🟦🟦🟩🟦🟦🌊🟦🌊🟦🟩🌊🟦🌊🌊🌊🟩🌊
🟦🟦🌊🟦🟦🟦🟦🟦🟦🟦🟦🟦🩵🟦🩵🟦🩵🟦🟦⬛🟡🟡🟡🟡🟡🟡🟡🟫👖🟦🩵🟦🩵🟦🟦🩵🟦🩵🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🩵🟦🟦🟦🟦🟦🟦🟩🟦🌊🟦🌊🟦🟩🟦🌊🟦🌊
🟦🟩🟦🟦🟦🟩🟦🟦🟦🟦🟦🟦🟦🟦🩵🟦🟦🩵🩵🟦⬛🟡🟡🟡🟡🟡⬛⚫🩵🟦🩵🟦🩵🩵🩵🟦🩵🩵🩵🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🟦🩵🟦🟦🩵🟦🟦🟩🟦🟦🌊🟦🌊🌊🟦🌊🌊🌊🟦🌊🌊
🟦🟦🟦🟦🟦🟦🩵🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦⬛⬛⬛⬛⬛🌊🟦🩵🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵🟦🩵🩵🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵🟦🟦🩵🟦🟦🟦🟦🟦🟦🟩🟦🌊🟦🟩🌊🟦🟩🌊🌊🟩🌊
🟦🟦🟩🟦🟦🟦🟦🟦🟦🩵🟦🟦🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🌊🌊🌊🩵🩵🟦🩵🩵🟦🩵🟦🩵🩵🩵🟦🩵🩵🟦🩵🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🩵🟦🩵🟦🟩🟦🟦🟦🌊🟦🌊🟦🌊🟦🌊🟦🌊🌊
🟦🟦🟦🟦🟩🟦🟦🩵🟦🟦🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🟩🌊🩵🩵🟦🩵🩵🩵🩵🟦🩵🟦🩵🩵🟦🩵🩵🩵🟦🩵🟦🩵🩵🟦🩵🟦🟦🩵🟦🩵🩵🩵🟦🩵🟦🩵🟦🩵🟦🟦🟦🟦🟦🟦🟦🌊🟩🟦🌊🟦🌊🌊🌊🟩🌊🟦🌊
🟦🟩🟦🟦🟦🟦🟦🟦🟩🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵👮⬛⬛⬛⬛👖🟫⚫⬛🌊🩵🩵🩵🩵🩵🩵🩵🩵🟦🩵🩵🩵🩵🟦🩵🟦🌊🟩👖🌊🩶👖🩶🟦🌊🩵🟦🩵🟦🩵🟦🩵🟦🟩🟦🟦🟩🟦🌊🟦🌊🌊🟩🟦🌊🌊🌊🌊🌊
🟦🟦🟦🟩🟦🟩🟦🟦🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦⬛⬛⬛⬛⬛⚫⬛⬛⚫🟫⬜⬜🩵🩵🩵🩵🩵🩵🩵🩵🩵🩵🩵🩵🟦⚫⚫⬛⬛⬛⬛⬛🟫🩶🟩👮🩵🟦🩵🟦🩵🟦🟦🟦🟦🟦🟦🌊🟦🌊🟦🌊🟦🌊🌊🟦🌊🟦🌊
🟦🟩🟦🟦🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵🟦🩵🟦🫐⬛⬛⬛⬛⬛⬛⬛⬛⚫🩶🩶🩶⬜⬜🩶🩶🩵🩵🩵🩵🩵🩵🩵🟦⬛⬛⬛⬛⬛⬛⬛⬛⚫🫐🌊🌊🩶🩵🟦🩵🟦🟦🩵🟦🟩🟦🟦🌊🟦🟩🌊🟦🌊🟩🟦🌊🌊🟩🌊
🟦🟦🩵🟦🟩🟦🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵🟦⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🩶⬜🩶👖🟫⬜⬜🩵⬜🩵🩵⬜🩵⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⚫🟩👖🩵🟦🩵🟦🩵🟦🟦🟦🟦🟩🟦🌊🟦🌊🟩🟦🌊🌊🌊🟦🌊🌊
🟦🟩🟦🟦🩵🟦🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵👮⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⚫⬛🟫🟫🟥🟫🟫🩷⬜🩵⬜🩵⬜🌊⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🌊🌊🩵🟦🩵🟦🟦🟩🟦🟦🟦🌊🟦🌊🟦🌊🌊🟦🌊🟩🌊🌊🟦
🟦🟦🩵🟦🟦🩵🟦🟩🟦🩵🟦🩵🩵🟦🩵🩵🩵🩵🩵🟦⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫⚫🟫🟫⬛🟫🟫🩷⬜⬜⬜⬜⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🩶👖🩵🟦🟦🩵🟦🟦🟦🟩🟦🟦🟩🟦🌊🟦🌊🌊🟦🌊🟦🌊🌊
🟦🟩🟦🟩🟦🩵🟦🩵🩵🟦🩵🟦🩵🩵🟦🩵🟦🟦🩵⬛⬛⬛⬛⬛⬛⬛⚫⬛⬛⬛⬛⬛🟫⬛🟫⬛🟫⚫🟫🟥🟧⬜🩶⚫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⚫🌊🩵🟦🩵🟦🟦🟦🟦🟦🟦🌊🟦🌊🟦🟩🟦🌊🟩🌊🟦🌊🟦
🟦🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵🩵🟦🩵🩵🟦🩵🩵🌊⬛⬛⬛⬛⬛⬛🌊🩵⬛⬛⬛⬛🟫⬛🟫⬛🟫🟫🟫⬛🟫🟫🩷🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🌊🩵🟦🩵🟦🟦🟩🟦🟩🟦🟦🟩🟦🌊🌊🟦🌊🟦🌊🟩🌊🌊
🟦🩵🟦🩵🩵🟦🩵🩵🩵🟦🩵🩵🩵🟦🩵🩵🟦🟦⬛⬛⬛⬛⬛⬛⬛🩵🩵🩶⬛⬛⬛🟫⬛⬛🟫⬛⬛🟫⬛⬛⚫⚫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛👖🩵🟦🟦🩵🟦🟦🟦🟦🟦🟩🟦🌊🟦🟩🌊🟦🌊🟦🌊🟦🌊
🩵🟦🩵🟦🩵🩵🟦🩵🟦🩵🩵🟦🩵🩵🟦🩵🩵🫐⬛⬛⬛⬛⬛⬛🌊🩵🩵🩵🩵🩶⬛🟫🟫⬛🟫⬛🟫⬛🟫🟫⬛⬛⬛⬛⬛🟫🟫⬛⬛⬛⬛⬛🟫🟫⬛⬛⬛⬛⬛👖🩵🟦🩵🟦🩵🟦🟩🟦🟦🟦🟦🟦🌊🟦🌊🟦🟩🌊🟦🌊🌊
🟦🟩🩵🩵🟦🩵🩵🟦🩵🩵🟦🩵🟦🩵🩵🟦🌊⚫⬛⬛⬛⬛⬛⬛🩵🩵🩶🩵🩵🩶⚫🟫⬛⬛⬛⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛🟥🟫⬛⬛🟫🟥🟥🟫🟥🟫⬛⬛⬛⬛🌊🩵🩵🟦🟦🟦🟦🟦🟦🟩🟦🌊🟩🟦🌊🟦🌊🌊🌊🌊🟩🟦
🩵🟦🩵🟦🩵🟦🩵🩵🟦🩵🩵🩵🩵🩵🟦🩵🟩⬛⬛⬛⬛⬛⬛🌊🩵🩵🩵🩵🩶⬛⬛🟫🟫🟫🟫🟫🟫🟫🟫🟫🟫🟫⬛⬛🟫🟥🟫⬛🟫🟥🟥🟫🟫⬛🟫🟫🟫🟦🟩🩵🟦🩵🟦🩵🟦🟩🟦🟦🟦🟦🟦🟦🌊🟦🟩🟦🌊🟦🌊🌊🌊
🟦🩵🟦🩵🩵🩵🟦🩵🩵🟦🩵🟦🩵🩵🩵🩵🟦⚫⬛⬛⬛⬛🫐🩵🩵🩵🩵🩶⬛⬛⬛⬛🟫🟫🟫🟫🟫🟫🟫🟫🟫🟫⬛🟥🟥🟫🟫🟫🟥🟥🟥🟥🟫🟫🟥🟫🟦🩵🟦🩵🟦🩵🟦🩵🟦🟦🟦🟩🟦🟩🟦🟩🟦🌊🟦🌊🟦🌊🟦🌊🟦
🩵🟦🩵🟦🩵🩵🩵🟦🩵🩵🩵🩵🟦🩵🟦🩵🩵🟦⬛⬛⬛⬛🩵🩵🩵🩶🩵🌊⬛⬛⬛⬛⬛🟫🟫🟫🟫🟫🟫🟫🟫⬛⬛🟥🟫🟥🟫🟫🟥🟥🟧🟥🟫🟥🟫🩶🩶🩵🩵🩵🩵🟦🩵🟦🟦🩵🟦🟦🟦🟦🟦🌊🟦🟩🌊🟦🌊🟩🌊🌊🌊
🟦🩵🩵🩵🟦🩵🟦🩵🩵🟦🩵🩵🩵🟦🩵🩵🩵🩵⬛⬛⬛🌌🩵🟦🩵🩵🩵⬛⬛⬛⬛⬛⬛🟫🟫🟫🟫🟫🟫🟫⬛⬛🟫🟥🟥🟫🟫🟥🟫🟥🟫⬛🟥🟥🟥🩶🩵🩵🟦🩵🟦🩵🟦🩵🟦🟦🟩🟦🟦🟩🟦🟦🌊🟦🌊🟦🌊🟦🌊🟦🌊
🩵🟦🟩🩵🩵🩵🩵🩵🟦🩵🩵🟦🩵🩵🟦🩵🟦🩶⬛⬛⬛🌊🩵🩵🩵🩵🩶⬛⬛⬛⬛⬛⬛⬛🟫🟫🟫🟫🟫🟫⬛⬛🟫🟫🟫🟥🟫🟫🟫🟥🟫🟫⬛🟫🩶⬜🩵🩵🩵🟦🩵🟦🟦🟦🟩🟦🟦🟦🟦🟦🌊🟦🟩🟦🌊🟩🟦🌊🌊🟦🌊
🟦🩵🩵🟦🩵🟦🩵🩵🩵🩵🩵🩵🟦🩵🩵🩵🩵🌊🟫⬛⬛🩶🩵🩵🩵🩵👖⚫🟫⬛⬛⬛⬛🟫🟫🟫🟫🟫🟫🟫🟫⬛🟫⬛⬛⬛⬛🟫🟫🟫🟥🟥🟫⬜🩵🩵🩶🩵🟦🩵🟦🩵🩵🟦🟦🩵🟦🟩🟦🟦🟩🟦🟦🟦🟦🌊🟦🟩🌊🌊🟦
🟦🩵🟦🩵🩵🩵🟦🩵🟦🩵🟦🩵🩵🩵🟦🩵🩵🟥🟫🟫⬛🩵🩵🩶🩵🩶⬛⬛⬛⬛⬛⬛⬛🟫⬛🟫🟫🟫🟫🟫⬛⬛⚫🟫⬛⬛⬛⬛⬛🟫🟫🟥🟥🩷⬜🩵🩵🩵🩵🟦🩵🟦🟦🩵🟦🟦🟦🟦🟦🟦🟦🟦🟩🟦🟩🟦🌊🟦🌊🟦🌊
🩵🟦🩵🟦🩵🩵🩵🩵🩵🩵🩵🩵🟦🩵🩵🩵🩵🟫🟫🟫🟥⬜🩵⬜🌊⬛⬛⬛⬛⬛⬛⬛🟫🟫🟫🟫🟫🟫⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛🟫⬛🟫🟡🩵⬜🩶🩵🟦🩵🟦🩵🟦🟦🩵🟦🟩🟦🟩🟦🟩🟦🟦🟦🟦🟩🟦🌊🟦🌊🟩
🟦🟩🩵🩵🟦🩵🟦🩵🟦🩵🟦🩵🩵🩵🟦🩵🩶🟫🟥🟫🩷⬜🩷🩶⬛⬛⬛⬛⬛⬛⬛🟫⬛🟫🟫🟫⬛🟫🟫⬛🟫🟫⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛🟥⬜🩵🩵🩶🩵🩵🩵🟦🩵🟦🩵🟦🟦🩵🟦🟦🟦🟩🟦🟩🟦🌊🟦🟩🌊🟦🌊
🟦🩵🟦🩵🟦🩵🟦🩵🩵🩵🩵🩵🟦🩵🩵🩵🩵🟥🟫🟥🟧🟡⚫⬛⬛⬛⬛⬛⬛⬛🟫🟫🟫🟫🟫⬛⬛⬛🟫🟫🟫⬛🟫⬛⬛🟫⬛⬛⬛⬛⬛🟫⬛🟫🟧⬜🩵🩵🟦🩵🩶🩵🩵🩵🟦🟩🟦🩵🟦🩵🟦🟦🟦🟦🟩🟦🟦🌊🟦🌊🟦
🟦🟩🟦🩵🩵🟦🩵🟦🩵🟦🩵🩵🩵🩵🩵🩵🩵🟫🟥🟫🟥🩶⬛⬛⬛⬛⬛⬛⬛🟫🟫🟫🟫⬛⬛⬛⬛⬛🟫🟧🟫🟫⬛🟫⬛⬛🟫⬛🟫⚫🟫⬛🟫⬛🟫🩶🩶🩵🟥🟥🩶🩶🩶🩶🩶🩵🩶🟩🩵🟦🟩🩵🟦🟩🟦🟦🟩🟦🌊🟩🌊
🟦🩵🟦🩵🟦🩵🩵🩵🟦🩵🩵🟦🩵🩵🩵⬜🩵🟥🟫🟥🟫🟥⬛⬛⬛⬛⬛⬛🟫⬛🟫⬛⬛⬛⬛🟫🟫🟫🟫🟫🩷🟫⬛🟫⬛🟫⬛⬛⬛🟫⬛⬛⬛🟫🟫🩶🩶🩶🟫🩷🟫🟥🟫🟥👖⚫🌃👖🌊🩶🟦🟦🩵🟦🟦🟩🟦🌊🟦🟦🌊
🟦🟦🟩🟦🩵🟦🩵🟦🩵🩵🩵🩵🩵🩵🩵🩵⬜🟫🟥🟫⬛🟫⬛⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛🟫🟫🟫⬛🟫⚫🟧🟫⬛⬛⬛⬛🟫⬛⬛🟫⬛⬛⬛⬛🟥🩷🟥🟧🩶🟫🟫🟫🌃⬛⬛🌌🌃🌊🌊🟩🟦🟩🟦🟩🟦🟦🌊🟩🌊🟦
🟦🩵🟦🩵🟦🩵🩵🩵🟦🩵🩵🩵🩵🩵⬜🩵🩵🩶🟥🟫⬛🟫⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛🟫⬛⚫🟫🟫🟫🟫⬛🟫⬛⬛⬛⬛⬛⬛⬛🟫⬛⬛🟫⬛⬛🟥🟫🟧🟥🟧🟥🟫🌃⬛⬛⬛🌌⚫⚫👮🌊🟦🟦🟦🟦🌊🟦🟦🌊🟦🌊
🟦🟩🟦🩵🟦🩵🟦🩵🩵🩵🩵🩵🩵⬜🩵⬜🩵⬜🟫🟫⬛⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛🟫🟫⬛🟫🟫🟫🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟥🟥🟫🟧🟫🟥🟫⬛⬛⬛⬛⬛🌃🌃🫐🫐🟩🟦🟩🟦🟩🟦🌊🟦🌊🌊
🟦🩵🟦🩵🟦🩵🩵🟦🩵🟦🩵🩵🩵🩵⬜🩵⬜🩶🟥🟫⬛⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛⬛🟫🟫⬛⬛🟫🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🟥🟧🟫🟫⚫⬛⬛⬛⬛⬛⬛🌃🫐🫐👮🌊🟦🟦🟦🟦🌊🟦🌊🟦🌊
🟦🟩🟦🩵🩵🟦🩵🩵🩵🩵🩵🩵⬜🩵⬜🩵⬜🩶🟫🟫⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟧🟫🟫🟫⬛⬛⬛⬛⬛⬛⬛⬛🌃🌃🫐🫐👖🟩🟦🌊🟩🟦🌊🟩🌊🟦
🟦🟦🩵🟦🩵🟦🩵🟦🩵🩵🩵🩵⬜🩵⬜🩵⬜🩶⬛⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛⬛👖🌊⚫⚫👖🟧⬛🟫🟫⬜🟥🟫🟫🟫🟫⬛⬛⬛⬛⬛⬛⬛🌌🌃🫐🫐🌊🟦🌊🟦🟦🌊🟦🟦🌊🌊
🟦🟩🟦🩵🟦🩵🩵🩵🩵🩵🩵🩵⬜🩵⬜⬜⬜🟨⚫⬛⬛🟫⬛🟫⬛⬛⬛⬛⬛🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⚫🩶🌊🩶🩶⬜⬜⬜⬜⬜🟡🩶🟫🟫🟥⚫⬛⬛⬛⬛⬛⬛⬛🌌🌃🫐🫐👖🟦🟩🟦🌊🟦🌊🌊🟦🌊
🟦🟦🟩🟦🩵🟦🩵🟦🩵🩵🩵⬜🩵⬜⬜🌊🟫🩶🟫🟫🟫🟫🟫⬛⬛⬛⬛⬛⬛⬛🟫⚫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🩶🌊🩵🟦🩶🟨🟧🟡⬜⬜⬜⬜🟥🟫🟫🩶⬛⚫🟫⬛⬛⬛⬛⬛🌃🌃🫐👮🌊🟦🟦🟦🟩🟦🌊🟦🌊🟦
🟩🟦🟦🩵🟦🩵🟦🩵🩵🩵⬜🩵⬜🩵⬜🟫⚫🟫🟫⬛🟫🟫🟫🟫🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🩶🩶🩵🩶🟨🟥🩶⬜⬜⬜⬜⬜🟫🟥🟫🩶🟫⚫⬛⬛⬛⬛⬛⬛🌌🌃🫐🫐🟩🟦🌊🟦🌊🟦🌊🟦🌊🌊
🟦🟦🟩🟦🩵🩵🩵🩵🩵🩵⬜🩵⬜⬜⬜⚫⚫🟫🟫🟫🟫🟫🟥🟫🟧🟧🟫🟫🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🩵🩶🟦🌊🟦⬜⬜⬜⬜⬜🟡⬜🟫🟫🟫🩶⬛⬛⬛⬛⬛⬛⬛⬛🌌🌃🫐🌊🟦🟦🟩🟦🌊🟦🌊🟩🟦🌊
🟦🟩🟦🩵🟦🟦🩵🩵🩵⬜🩵⬜🩵⬜🩶⬛🩶🩶🟫🟫🟫🟫⚫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛👖👖🩶🌊🌊🩶🟩🌊🩶⬜⬜⬜🟧🩶🩶🟫🟫🩶🫐⬛⬛⬛⬛⬛🌃🫐🫐🌊🌊🌊🟩🟦🟦🌊🟦🟩🟦🌊🌊🟦
🟦🟦🩵🟦🩵🩵🩵🩵🩵⬜⬜🩵⬜🩶⬛🟫⬜🟡🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🩶🟧🩶🩶🌊🟩🌊🌊🩵🌊🟦🟦⬜⬜🩷🟧🟫🟫🟫⬜⚫⬛⬛⬛⬛🌊🌊🌊🌊🌊🟩🟦🌊🟦🟩🟦🌊🟦🌊🟦🌊🌊
🟦🟩🟦🟩🩵🟦🩵🩵🩵⬜⬜⬜🩵🟫🩶⬜⬜⬜🩶⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🟧🟧⬜🩶🩶🌊🟦🌊🌊🩶🟦👖🌊🩶⬜⬜⬜🩶🩶🩶🟫⚫⬛⬛⬛⬛🌊🌊🟩🌊🌊🌊🌊🟦🟦🟦🌊🟦🌊🟦🌊🟦🌊
🟦🟦🩵🟦🩵🩵🩵🩶🩵🩵⬜⬜👖🩶⬜⬜⬜⬜🟨⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🟧🩷🟨🩶⬜🌊🌊🟩🌊🌊🌊🩵🌊🌊🟩🩷🟧🟧🟧🟫🟫🟫⚫⬛🌊🩶🌊⚫🫐🌊🌊🌊🟦🟩🟦🟩🟦🌊🟦🌊🌊🟦🌊🟦
🟩🟦🟩🟦🩵🟦🩵🩵🩵🩵⬜🩶🟫⬜⬜⬜⬜🟡👖⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟧🟧🟧🟨🩷🟡🩶🌊🌊🌊🟦🌊🌊🩶🌊🌊🩶🟨🟧🟧🟥🟧🟧🟫🟫🟫🩶🌊⬛⬛🌊🌊🌊🟩🟦🟦🟦🟦🟩🟦🌊🟦🌊🌊🌊🌊
🟦🟩🟦🩵🟦🩵🩶🩵🩶⬜🩵🟥⬜⬜⬜⬜⬜⬜⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟧🟥🟧🟧🟨🩷🟨🩷🩶🟩🌊🌊🟩🌊🌊🩵🌊🌊🩶🟨🟧🟧🟧🟧🟧🟧🟧🟫⬛⬛⬛⬛⬛⚫🌊🟩🟦🟩🟦🟦🌊🟦🌊🟦🌊🟦🟦🌊
🟦🟦🩵🟩🩶🩵🩵⬜⬜⬜🩶🩶⬜⬜⬜⬜⬜🟧⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🌊🩶🟫🟧🟧🟧🟧🟧🟨🩷🟡🟧🩶🩶🌊🌊🟦🌊👖🩶🌊🌊🟫🟧🟧🟧🟧🟥🟧🟧🟥🟫⬛⬛⬛⬛⬛⬛🫐🩶🟦🟦🟩🟦🟦🌊🟦🌊🟩🌊🌊🟦
🟩🟦🟩🩵🩵🩶🩵⬜🩵⬜🩶⬜⬜⬜⬜⬜⬜🟫⬛⬛⬛⬛⬛⬛🟫🩶🩶🩶🟩🩶🩶🟧🟧🟧🟧🟧🟨🟧🟨🩷🟨⬜⬜🌊🌊🟩🌊👖🟩🟩🌊🩶🟧🟫🟧🟧🟧🟧🩶🟧🟧⬛⬛⬛⬛⬛🌌👖🩵🟦🩵🟦🟩🟦🌊🟩🟦🌊🟦🌊🌊
🟦🟩🟦🩶🟦🩵🩶🩵🩶🩶⬜⬜⬜⬜⬜⬜🟡🌌⬛⬛⬛⬛⬛🩶⬜⬜🩵🩶🟦🩵🩶🩶🟧🟧🟧🟧🟧🟧🟨🟧⬜🟧🩵🩶🌊🌊🩶🫐🩶🟦👖👖🟧🟧🟫🟧🟫🟧🟧🟧🟥🟫👖⬛⬛⬛⬛🌊🩵🩶🟦🟦🌊🟦🌊🟦🌊🌊🌊🟦🌊
🩵🟦🩵🟦🩶🟦🩵🩶🩵⬜⬜⬜⬜⬜⬜⬜🩶⬛⬛⬛⬛⬛⬜⬜🩵🩶🩵🩶🟦🩶🟦🩶🟧🟧🟨🟧🟧🟧🟧🟧⬜🟧🩶⬜🌊👖🟩👖🫐🩶🫐🩶🟫🟧🟥🟫🟧🟧🟧🩶🟧🟫⬛⬛⬛⬛🌃🩵🩶🩵🟦🟩🟦🌊🟦🌊🟦🌊🟦🌊🌊
🟩🟦🟩🩵🩶🩵🩶🩵🟫👖🩶⬜⬜⬜⬜🟡⚫⬛⬛⬛⚫⬜⬜⬜🩵🩶🟦🌊🩵🌊🟩🟦🌊🟨🩷🟨🟧🟧🟧🟧🟥🟧🟧🩶🩵🌊🌊🌊🌊🟩🫐🌊🟫🟧🟫🟥🟧🟧🟧🟧🟥🟫⚫⬛⬛⬛🌊🩵🩶🩵🟦🟦🟦🟩🟦🌊🌊🟦🌊🌊🟦
🟦🩶🟦🟩🟦🩶🩵🩶⚫⚫🟫⬜⬜⬜⬜⬜⬛⬛⬛🫐🩵🩶🟦🩶🟦🩶🌊🟦🟩🟦🟦🩶🩵🩶🟧🟧🟧🟧🟧🟧🟧🟥🟧🩶🩶🩶🌊🌊🌊👖🟩🫐🟫🟫🟫🟫🟧🟧🩶🟧🟧👖⬛⬛⬛⬛🩶🩵🟦🩶🟦🟩🟦🌊🟦🌊🟦🌊🌊🌊🌊
🟩🟦🟩🟦🩶🟦🩶🌊⚫⚫👖⬜🩶⬜⬜🩶⬛⬛⚫🩶🩵🩶🩵🌊🟩🟦🩵🟦🩶🩶🩶🩵🩶🩶🟧🟨🟧🟥🟧🟫🟧🟧🟧🩶🌊⚫🟩👖🟩⚫👖⚫⚫⚫🟫🟧🟧🟧🟧🩶🟧⚫⬛⬛⬛🌊🟦🩶🟦🟦🟩🟦🌊🟦🌊🌊🌊🟦🌊🟦🌊
🟦🟩🟦🩶🟩🟦🟩⚫⚫⚫🟫🩶⬜⬜🩶🟫⬛⚫🩶🟦🩶🩵🩶🩵🩶🩶🩶🌊🩶🌊🌊🟩🟦🟦🩶🟨🟧🟧🟫🟧🟫🟧🟫⚫⬛⚫⬛⚫⚫⬛⬛⬛⚫🩶🩶🟨🩶🩶🩶🩷🟫⬛⬛⬛⬛🩶🟦🟩🟦🟦🌊🟦🌊🟦🌊🟦🌊🌊🌊🌊🌊
🟩🟦🟩🟦🟦🩶🌊⚫⚫⚫⚫⚫⚫⚫🩶⬛👖🩶🩶🩵🌊🩵🌊🟦🟦🟩🌊🌊🩵🌊🟦🌊🟦🟩🌊🩶🟧🟧🟥🟫🟫🟥⬛🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛👖🩶🩷🟨🩶🩶🩶⬛⬛⬛🟦🟩🟦🟦🟩🟦🟦🟩🟦🌊🟦🌊🟩🟦🌊🟦🌊
🟦🟩🟦🟩🟦🟩🫐⚫⚫⚫⚫⬛⬛⚫🫐🌊🟩🟦🌊🌊🩵🟦🌊🌊🩵🌊🟦🌊🟦🩵🌊🌊🟩🟦🌊🩶🟫🟫🟧🟫🟥🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⚫⚫🟫🟧🟧🟧🟧🟧🩶⬛⬛🌊🩶🟦🟦🟦🟦🟩🟦🟦🌊🟦🌊🌊🌊🌊🌊🌊🌊
🌊🟦🟩🟦🩶🫐🫐⚫🫐⚫⚫🟩🩶🟦🟩🌊🟦🟩🌊🌊🟦🟩🟦🌊🟦🟦🌊🌊🌊🩵🌊🌊🌊🌊🟩🩶🟫🟫🟫🟫🟥🟫🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🩶🟧🩶🟥👖⬛🌊🩶🟦🩶🟩🩶🟦🟦🟦🟩🟦🌊🟦🌊🟦🌊🟦🌊🌊
🟩🟦🟩🩵🌊🫐🫐⚫⚫🩶🩶🩶🩵🌊🌊🌊🟦🌊🟦🌊🟦🩵🌊🌊🟩🟦🌊🟩🌊🌊🩵🌊🌊🌊🌊🌊🩶🟧🟫🟥🟧🟧🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🟧🟧🟥🟧⚫🟩🟦🟦🟩🟦🟦🩵🟦🟩🟦🌊🟦🟩🌊🌊🌊🌊🌊🌊🌊
🌊🟦🩵🌊👖⚫🟩👖🩶🩶🟩🟦🩶🟦🟩🌊🟦🟩🌊🟩🌊🩵🌊🌊🌊🟦🟩🌊🌊🌊🟩🩶👖🫐👖🟩👖🟫🫐⬜🩵🩶🩵🟫⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🟧🩶🟧🩶🟫🩶🟦🩶🟦🟦🩶🟦🟩🟦🟦🟩🟦🌊🟦🌊🟦🌊🌊🟦🌊
🟩🟦🟩🌊👖🟩🟫🩶🟨🩶🩶🩶🟩🌊🟦🌊🟦🟦🌊🟦🌊🟦🟩🌊🟦🌊🟦🌊👖🌊👖🌊🩵⚫⚫⚫⚫⬛🩶🩵🩶⬜🩶🩵⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛⬛🟫🩷🟧🟥🟧🟦🩶🟦🩵🩶🩵🩶🟦🟦🩶🟦🟦🌊🟦🌊🌊🌊🌊🟩🌊🌊
🟦🟩🌊🟩👖🩶🩶🟧🩶🩶🌊🟩🟦🟦🌊🟩🌊🟩🌊🌊🌊🟩🟦🌊🌊👖🟩🌊🌊🫐👖⚫👖🩶🟩🌊🟩👖🩶🩶🩵⬜⬜⬜🌊⬛⬛⬛⬛⬛⬛⬛⬛👖🩶🟫🟨🩶🟧🩶🩶🩶🩵🩶🩵🩶🟦🟩🟦🌊🟩🌊🟦🌊🌊🟦🌊🌊🌊🌊🌊
🌊🟩🌊🩶🟫🩶🟫🩶🟫🩶🩶🌊🟩🩶🌊🌊🌊🟦🌊🟩🌊🌊🩶🌊🫐👖👖🌊🟩🫐⚫🫐⚫🫐👖⚫👖🩶🩵🩶⬜🩶🩵🩶🩶⬛⬛⬛⬛⬛⬛⬛⬛🟫🌊🩶🟥🟧🩶🟫🩶🩶🩶🟦🟦🟦🌊🟦🌊🟦🟦🟦🟩🌊🟦🌊🌊🟦🌊🌊🌊


## 1. Vocabulary

These names are fixed. Do not rename in code or docs.

- **vegetable** — any polyglot file produced by this pipeline. Extension
  is `.bat` (so Windows can execute it by double-click and CMD recognizes
  the header).
- **mohabbat** — the self-hosting vegetable that is also a builder. Its
  filename is `mohab.bat`.
- **brot** — a small `#![no_std]` native loader stub. One brot per target
  triple. Decompresses the payload in memory and hands control to a host.
- **washmhost** — the native Rust binary that embeds Wasmtime, exposes the
  rusticated ABI to a guest WASM module, and runs it. One washmhost per
  target triple.
- **brain** — `mohabbat.wasm`: the builder logic compiled to
  `wasm32-unknown-unknown`. Lives inside every mohabbat vegetable.
- **payload** — the user's WASM module (when building a non-mohabbat
  vegetable), or the brain (when building mohabbat itself).
- **pool** — the Brotli-compressed concatenation of all washmhosts plus
  the payload.
- **Modern Four** — the target matrix. Six triples, no 32-bit:
  - `x86_64-unknown-linux-musl`
  - `aarch64-unknown-linux-musl`
  - `x86_64-pc-windows-msvc`
  - `aarch64-pc-windows-msvc`
  - `x86_64-apple-darwin`
  - `aarch64-apple-darwin`

---

## 2. Vegetable file layout

A vegetable is a single file with three zones concatenated end to end.

```
[Zone A: polyglot script header   ]  text, small, executable by sh + cmd
[Zone B: brot table               ]  N native loader binaries, back to back
[Zone C: brotli pool              ]  one brotli stream, all hosts + payload
EOF
```

### Zone A — polyglot script header

A short hybrid script that both `cmd.exe` and POSIX `sh` interpret
correctly. Responsibilities:

1. Detect OS and CPU architecture using only built-in shell features.
2. Look up the absolute byte offset and length of the correct brot in
   Zone B.
3. Extract the range from the chosen brot's start offset to the end of the
   vegetable file (the concatenated brot + pool) to a single temp file.
   This produced file is the self-extracting runner.
4. `chmod +x` it on POSIX.
5. Execute it, forwarding all the user's CLI arguments.
6. Exit with the runner's exit code.

Complications to handle:

- Polyglot syntax: the header must be valid for both shells. Standard
  trick: open with a CMD label/goto that jumps over the sh portion, with
  the sh portion arranged so CMD never executes it. Several known
  templates exist; pick one and freeze it.
- Some sh extraction utilities differ across distros. Restrict to POSIX
  `dd`, `head`, `tail`. Avoid `tail -c +N` ambiguity by using `dd
  if=... bs=1 skip=... count=...` or `dd bs=1M` with computed counts.
- macOS `dd` and GNU `dd` differ in progress output; use `2>/dev/null`.
- Windows lacks built-in binary slicing. The header must invoke
  PowerShell to do the slice: `powershell -nop -c "..."` using
  `[IO.File]::OpenRead` + `Seek` + `Read`. PowerShell is present on every
  supported Windows (5.1+ on Win10/Win11). Do not depend on `pwsh`.
- The `.bat` extension is mandatory because:
  - CMD will refuse to run a file without a recognized extension.
  - POSIX shells do not care about extension when invoked with
    `sh file.bat` or when the file has the executable bit and the
    shebang-like first line is in the polyglot header.
- The header must NOT contain a UTF-8 BOM (CMD chokes on it).
- The header must end its CMD section with `goto :eof` and an explicit
  exit code propagation. The sh section must `exit $?`.
- The temp file path must be unique per run (PID + random) so concurrent
  invocations do not collide.

### Zone B — brot table

- Each brot is a complete native executable (ELF / PE / Mach-O).
- Brots are concatenated with no padding.
- Their offsets and lengths are baked into Zone A at assembly time.
- The Zone A header does not parse anything — it only slices by known
  numbers.

### Zone C — brotli pool

- Single brotli stream. Decoder is in the brot.
- Decompressed content is a fixed sequence: washmhost #1, washmhost #2,
  ..., washmhost #N, payload. Lengths and order are baked into each brot
  as constants (see §6 on patching).
- One stream rather than N streams: cross-binary redundancy between
  washmhosts is significant; joint compression matters.

---

## 3. Brot — the native loader

One `#![no_std]` Rust crate, six target builds. Located at `brot/`.

Responsibilities, in order:

1. Read its own executable image file (using `/proc/self/exe` on Linux,
   `_NSGetExecutablePath` on macOS, or `GetModuleFileNameW` on Windows).
   Because the Zone A header extracted the brot and the pool together,
   the temporary executable *is* the file containing the pool at its tail.
   There is no need to locate the original vegetable file.
2. Open its own executable file, seek to its tail, read the brotli pool. The
   pool's byte length is a compile-time constant patched in at assembly
   time. The pool starts at `file_size - POOL_LEN`.
3. Decompress the pool into a single owned buffer.
4. Slice out its own washmhost using compile-time constants
   `WASHMHOST_OFFSET` and `WASHMHOST_LEN`, then slice the payload using
   `PAYLOAD_OFFSET` and `PAYLOAD_LEN`.
5. Execute the decompressed washmhost entirely from memory as an in-process library without writing
   it to disk. (Linux: in-memory ELF loader; Windows: Reflective PE Loading).
6. Invoke the exported entry point of washmhost, passing a pointer and length to the sliced payload.
7. Forward argv to the washmhost entry point.
8. Wait for washmhost exit, propagate its exit code.

Constraints:

- `#![no_std]`, `#![no_main]`. Must define `_start` (Linux), `mainCRTStartup`
  (Windows), `start` (macOS).
- No `std::process::Command` (that needs std). `brot` uses `rusticated::fs::File` and `rusticated::io::Read` for opening its own executable and reading the pool tail. Raw OS calls are used only for the two tasks that have no `rusticated` equivalent: self-path discovery (`/proc/self/exe`, `_NSGetExecutablePath`, `GetModuleFileNameW`), and in-memory execution (Reflective PE/ELF loading). Both brot and washmhost run in the same `#![no_std]` process.
- Allocator: Use the `alloc` crate backed by `rusticated`'s `GlobalAllocator` (already wired in `rusticated/src/lib.rs`). No custom allocator is needed in `brot`.
- Brotli decoder: the `brotli-decompressor` crate has a `no_std` mode. Use it. `rusticated`'s `GlobalAllocator` satisfies the `alloc` requirement; no extra wiring needed in `brot`.
- Binary size target: under 200 KB per brot, ideally under 100 KB.
  `panic = "abort"`, `lto = "fat"`, `codegen-units = 1`, `opt-level =
  "z"`, `strip = "symbols"`.

### Brot metadata section

Constants the brot needs at runtime:

```
POOL_LEN          u64
WASHMHOST_OFFSET  u64    // offset inside decompressed pool
WASHMHOST_LEN     u64
PAYLOAD_OFFSET    u64
PAYLOAD_LEN       u64
```

These cannot be known when brot is compiled (the pool does not exist
yet). The brot reserves a dedicated linker section named `.mohabbat_meta`
(ELF/Mach-O) or `.mohmeta` (PE — PE section names are limited to 8 chars)
containing six zero-initialized `u64` slots in a fixed order, plus an
8-byte ASCII magic `MOHABBAT` so the patcher can find it without parsing
the section table.

The patcher's job is to locate the magic and overwrite the six u64s in
place. No relocations involved — these are plain data.

---

## 4. Washmhost — the Wasmtime embedding

Changes required:

- Must be `#![no_std]` using `wasmtime` with `default-features = false` and the `pulley` interpreter.
- Must rely on `rusticated` for the global allocator and standard library functions instead of the official Rust `std`.
- Exposed as a library (or a binary with a C-ABI entry point) which accepts the guest WASM payload via a memory pointer and length instead of reading `stdin`.
- Keep the existing rusticated ABI (`abi.rs` host imports).
- The washmhost binary is large (Wasmtime = several MB).
  This is the dominant size cost. Acceptable: a 60–80 MB pre-brotli pool
  compresses to ~10–15 MB.

---

## 5. Brain — `mohabbat.wasm`

The builder logic, compiled to `wasm32-unknown-unknown` against the
rusticated target. Located at `mohabbat/` (same crate, dual-built — see
§7).

Two operating modes, selected by CLI:

### Mode A — wrap an existing WASM

```
mohab.bat path/to/payload.wasm -o out.bat
```

Steps:

1. Read `payload.wasm`.
2. Decompress mohabbat's own pool (the brain extracts six washmhosts
   from the file it is running inside).
3. Build a new pool: `[washmhost1, ..., washmhostN, payload.wasm]`.
4. Run brotli encoder over the new pool.
5. Compute new offsets and `POOL_LEN`.
6. Read mohabbat's own brots (Zone B of the running vegetable).
7. Patch each brot's `.mohabbat_meta` section with new constants.
8. Generate Zone A header with new offset table.
9. Write `out.bat` = Zone A + patched brots + new brotli stream.
10. `chmod +x` on POSIX.

### Mode B — build a Rust project

```
mohab.bat path/to/cargo-project -o out.bat
```

Steps:

1. Spawn `cargo build --release --target wasm32-unknown-unknown` in the
   project directory.
2. Locate the produced `.wasm` (parse cargo metadata JSON or walk
   `target/wasm32-unknown-unknown/release/`).
3. Continue with Mode A starting from step 2.

Complications:

- Mode B requires the user to have a Rust toolchain installed. We do not
  bundle one. Document this.
- Cargo project detection: look for `Cargo.toml`. Reject workspaces with
  multiple binary crates unless `--bin` is given. Forward unknown flags
  to cargo.
- The brain runs inside washmhost, which means it uses rusticated's
  `process` and `fs` APIs to spawn cargo and read files. That is exactly
  the demonstration use case — eats its own dog food.

---

## 6. Bootstrapping problem and the build pipeline

To produce mohab.bat we need brots, washmhosts, and brain. To run
mohab.bat we need mohab.bat. The first one must come from a
non-mohabbat build path.

### The solution: `mohabbat/build.rs` does the first build.

The `mohabbat/` crate has an extremely flat layout and is composed of two active parts:

- **The Native Builder** (`build.rs`): orchestrates the first build of `mohab.bat`. It is executed by Cargo during a normal build of the crate. It is pure Rust, uses std, and shells out to cargo to build components.
- **The WASM Brain** (`src/main.rs`): the builder logic compiled to `wasm32-unknown-unknown` that lives inside every vegetable.

Rather than having a separate native binary target, the simple act of compiling the crate (`cargo build -p mohabbat`) triggers `build.rs` which performs the entire assembly as a side-effect, creating `mohab.bat` in the repository root.

### The first build, step by step

Triggered by `cargo build -p mohabbat` (native, on the developer's machine).

This single command does everything via `mohabbat/build.rs`. The `build.rs` prepares assets and stitches them into the final `.bat`.

#### Step 1 — workspace setup

`mohabbat/build.rs`:

1. Set `CARGO_TARGET_DIR=<workspace>/target/tree` for all sub-cargo
   invocations. This is mandatory to avoid Cargo lock deadlock with the
   outer build that is currently running build.rs. The name `target/tree`
   is fixed.
2. Probe which of the six target triples can actually be built on this
   host. Probing rules:
   - Linux musl x64/arm64: require the matching `rustup target add`
     installed. Verify a tiny test build succeeds.
   - Windows MSVC: only buildable from Windows hosts with the MSVC
     toolchain, or from any host with `xwin` / `cargo-xwin` set up.
     Detect by trying a tiny build.
   - macOS x64/arm64: only buildable from a macOS host, or from a
     Linux/Windows host with the macOS SDK and a cross linker (osxcross
     or similar). Without that, skip.
   - For each target, mark Available or Skipped with a reason.
3. Try to find a seed `mohab.bat` in the crate root or repo root.
   If present, prepare to borrow brots and washmhosts from it for any
   target marked Skipped. See §8.

#### Step 2 — build brots (for Available targets)

For each Available target:

```
cargo build -p brot --release --target <triple>
```

inside `target/tree/`. The brot binary lands at
`target/tree/<triple>/release/brot` (or `brot.exe`).

#### Step 3 — build washmhosts (for Available targets)

For each Available target:

```
cargo build -p washmhost --release --target <triple>
```

Output at `target/tree/<triple>/release/washmhost` (or `.exe`).

This is the slow step. Wasmtime + cranelift, six times. Expect 5–15
minutes on a cold build. Document this. Cache via cargo's normal
incremental story — `target/tree` is persistent.

#### Step 4 — build the brain

```
cargo build -p mohabbat --release --target wasm32-unknown-unknown
```

Output at `target/tree/wasm32-unknown-unknown/release/mohabbat.wasm`.

When `mohabbat` is compiled targeting WASM, `build.rs` immediately exits early (yielding no operations), and `src/main.rs` compiles the `#[cfg(target_arch = "wasm32")]` block which contains the real builder logic.

#### Step 5 — assemble brots and washmhosts per target

For each target slot (six slots, fixed order):

- If Available: use the freshly built brot and washmhost.
- If Skipped and seed is present: extract brot and washmhost from seed.
- If Skipped and no seed: the slot is empty. The final mohab.bat
  will refuse to run on that platform, with a clear error message
  emitted by the Zone A header.

#### Step 6 — build the pool

1. Concatenate washmhosts in fixed slot order, then append the brain.
   Skipped washmhost slots are simply absent (the slot is recorded as
   length-zero; that target's brot is also absent).
2. Run brotli encoder, quality 11. The compressor is `brotli` crate,
   used from native code in `mohabbat`'s main.
3. Record `POOL_LEN` and the offset/length of each washmhost and the
   payload (the brain) inside the decompressed pool.

#### Step 7 — patch each brot

For each present brot:

1. Open the brot file, find the `MOHABBAT` magic in the
   `.mohabbat_meta` / `.mohmeta` section.
2. Overwrite the six u64s with this target's values.

#### Step 8 — stitch

1. Generate Zone A. Header carries:
   - The slot table: for each of six slots, a (offset, length) pair
     pointing into Zone B. Length 0 means "this platform not supported
     by this vegetable".
   - The `POOL_LEN` for self-introspection (not strictly needed by Zone
     A, but useful for mohabbat-as-brain to discover its own pool tail).
2. Concatenate: Zone A text + Zone B brots + Zone C brotli stream.
3. Write `mohab.bat` at the repo root.
4. On POSIX, `chmod +x`.

Done. `cargo build -p mohabbat` has produced `mohab.bat`.

Note: Cargo will still follow through and compile `mohabbat/src/main.rs` into a native binary after `build.rs` finishes. We handle this by making `src/main.rs` conditionally compiled: when built natively, it's just a tiny stub CLI that prints `"mohab.bat generated at workspace root"`. The "real" main output is the side-effect `mohab.bat`. 

### Subsequent uses

Once `mohab.bat` exists, the developer can use it to make any other
vegetable — including a new `mohab.bat` — without invoking cargo on
the workspace at all. The brain inside `mohab.bat` does Mode A or
Mode B from §5.

The native `cargo build -p mohabbat` path is still useful for clean
rebuilds, for CI, and for refreshing the seed before publishing.

---

## 7. Repository layout

```
rusticated/
├── Cargo.toml                  workspace
├── src/                        rusticated library (custom std)
├── demo/                       existing demo crate
├── washmhost/                  Wasmtime embedding host (renamed)
├── brot/                       NEW: #![no_std] loader stub
│   ├── Cargo.toml
│   ├── build.rs                emits linker script for .mohabbat_meta
│   └── src/main.rs
├── mohabbat/                   NEW: builder, flattened
│   ├── Cargo.toml
│   ├── build.rs                THE BUILDER: probes, cross-builds, patches,
│   │                           and performs the first stitch natively.
│   └── src/
│       └── main.rs             THE BRAIN + NATIVE STUB.
│                               #[cfg(wasm32)]: The builder logic running in washmhost.
│                               #[cfg(not(wasm32))]: A tiny dummy CLI that prints success.
├── mohab.bat                produced artifact, NOT checked in!!
├── target/
│   └── tree/                   sandbox for sub-cargo builds
└── ... (existing files)
```

`brot` and `mohabbat` are workspace members. `mohab.bat` is committed
to git (it is the seed; see §8).

---

## 8. The seed and crates.io publishing

We want `cargo install mohabbat` (or whichever name we publish under) to
produce a working `mohabbat` native binary on a user's machine, even
when that user has no cross-compilation toolchains.

Strategy:

1. The repository checks in a `mohab.bat` that supports all six
   targets, produced on a CI machine that has every toolchain.
2. When packaged for crates.io, `mohab.bat` is included in the
   crate sources (`include = ["mohab.bat", ...]` in Cargo.toml).
3. On `cargo build -p mohabbat` (or download/install where a build runs):
   - `build.rs` tries to build brots and washmhosts locally for every target.
   - For targets where local build fails, borrows the corresponding
     brot and washmhost from the bundled `mohab.bat` seed.
   - Always rebuilds the brain locally so the user gets the current
     version of the builder logic.
4. `build.rs` writes the final stitched `mohab.bat` (placed in the repo root
   or crate extraction directory) which carries: current brain + mix of fresh 
   and borrowed native components.

Complications:

- Borrowing a washmhost from the seed locks the user to whatever
  Wasmtime version was in the seed. Acceptable, because they could not
  build a fresher one anyway.
- The borrowed washmhost and the fresh brain must agree on the
  rusticated ABI. The ABI is stable within a major version; bump
  mohabbat's major version when the ABI changes, and refuse to borrow
  across major versions (check a version u32 baked into both).
- Crate size: a 10–15 MB `.bat` is large for crates.io but acceptable.
  Crates.io permits up to 10 MB by default; request a larger limit if
  needed, or split the seed into a separate `mohabbat-seed` crate that
  `mohabbat` depends on at build time (then the seed crate can be much
  larger and updated independently).
- Reproducibility: the seed must be regenerated on a clean machine for
  each release. CI job does this.

---

## 9. Complications and how each is handled

A consolidated list. Most are mentioned above; this is the index.

### Polyglot script

- BOM-free file. Enforce in writer.
- `cmd` is line-oriented and case-insensitive; `sh` is not. The
  template is hand-written and frozen.
- PowerShell call must be `-NoProfile -ExecutionPolicy Bypass` to avoid
  user profiles slowing startup or scripts being blocked.
- Some Unix systems lack `/bin/sh -> bash`. Stick to POSIX sh syntax
  only. No `[[ ]]`, no arrays, no `local`.

### Architecture detection

- Linux: `uname -m` gives `x86_64` or `aarch64`. Map directly.
- macOS: `uname -m` gives `x86_64` on Intel Macs and `arm64` on Apple
  Silicon. Map both.
- Windows: `%PROCESSOR_ARCHITECTURE%` gives `AMD64` or `ARM64`. Also
  check `%PROCESSOR_ARCHITEW6432%` for WOW64 (32-bit cmd on 64-bit
  Windows — should be rare, but handle by preferring the 6432 var when
  set).
- Rosetta on macOS: an arm64 Mac running x86_64 mohabbat under Rosetta
  reports x86_64. That is correct — we will execute the x64 brot, and
  Rosetta will run it.
- Wine: a Windows brot under Wine on Linux. Out of scope; behavior is
  whatever Wine gives us.

### Brot binary geometry

- ELF: `.mohabbat_meta` section, custom name, marked `SHF_ALLOC` so it
  is loaded at runtime. Linker script in `brot/build.rs` emits it.
- PE: section names cap at 8 chars; use `.mohmeta`. Mark as readable
  data. PE has no concept of arbitrary named sections in the usual sense
  — actually it does, just rarely used. Add via `#[link_section =
  ".mohmeta"]` in Rust and accept that MSVC linker will preserve it.
- Mach-O: `__DATA,__mohabbat` segment/section. `#[link_section =
  "__DATA,__mohabbat"]` in Rust.
- Locating the magic at patch time: scan the whole brot file for the
  `MOHABBAT` 8-byte ASCII. There must be exactly one occurrence (the
  metadata struct). To guarantee uniqueness, the brot source must not
  embed the string `MOHABBAT` anywhere else; enforce by convention and
  by a build-time check in `build.rs`.

### Brotli decompressor in no_std

- `brotli-decompressor` works in `no_std + alloc`. `rusticated` supplies the global allocator. `brotli-decompressor` in `no_std + alloc` mode uses it directly.

### Spawning washmhost from brot (In-Memory Execution)

Because both `brot` and `washmhost` are `#![no_std]` and share the same `rusticated` base, we no longer need to spawn a separate process for the host. The host is loaded reflectively into the current `brot` process.

- Linux: Use a manual in-memory ELF loader or `dlopen` on a `memfd_create` backed file descriptor to map `washmhost` into memory and resolve its entry point.
- Windows: Implement a minimal Reflective PE Loader. Manually map the PE sections into memory, resolve imports (Kernel32, etc.), apply base relocations, and invoke the entry point. Do NOT write an .exe to disk.
- macOS: Use `NSCreateObjectFileImageFromMemory` and `NSLinkModule` to load the Mach-O binary from the memory buffer.
- Payload passing: Since the invocation is purely an in-process library call, the payload is simply passed by pointer and length as arguments to the `washmhost` entry point.

### Temp file lifecycle

- Runner path (Brot + Pool): write to `<tmp>/mohabbat-<pid>-<rand>.exe`
  (Windows) or `<tmp>/mohabbat-<pid>-<rand>` (POSIX).
- Cleanup: best-effort `unlink` (POSIX) or `DeleteFile` (Windows) after
  the runner naturally exits.
- Antivirus: Since washmhost runs reflectively from memory and never hits
  the disk as a new file (and no `fork` or `exec` happens), we bypass severe AV penalties that trigger when one temp file drops and executes another.

### Cargo lock during build

- `mohabbat/build.rs` shells out to `cargo` itself. The outer cargo
  holds a workspace lock on `target/`. Use `CARGO_TARGET_DIR=target/tree`
  to point inner cargos at a different directory and avoid the lock.
- Inner cargos must not call out to outer cargos; the build graph is
  one-way.

### Cross-compilation availability

- See §6 step 1. The build is best-effort. Partial mohabbats are valid
  and run on whichever platforms got built.
- `mohabbat/build.rs` must print a clear summary at the end:
  ```
  mohabbat target matrix:
    x86_64-linux-musl       BUILT
    aarch64-linux-musl      BUILT
    x86_64-windows-msvc     BORROWED from seed
    aarch64-windows-msvc    BORROWED from seed
    x86_64-apple-darwin     MISSING — vegetable will not run on this platform
    aarch64-apple-darwin    MISSING — vegetable will not run on this platform
  ```

### Brain ABI stability

- The brain calls rusticated host imports. The rusticated ABI is the
  contract between brain and washmhost. When it changes, brain and
  washmhost must be rebuilt together.
- Mohabbat's `mohab.bat` always pairs the brain and washmhosts that
  were assembled in the same build. There is no risk of mismatch within
  one vegetable. The risk is only when borrowing from a seed — see §8.

### Self-modification (mohabbat builds new mohabbat)

- Mode A on a `mohabbat.wasm` input produces a new mohab.bat. This
  is how the brain is updated without invoking cargo.
- The brain needs to read washmhosts out of the running vegetable. The
  brain knows it is running inside one because the Zone A header passes
  `vegetable_path=...` as the first env var or first CLI arg to
  washmhost, which forwards it to the guest.
- The brain reads the vegetable, slices Zone B and Zone C using the
  POOL_LEN exposed in Zone A header (or by re-parsing the slot table —
  the brain needs the slot table anyway to know how to rebuild Zone A).
- For the slot table to be discoverable from inside the running guest,
  Zone A must embed a small machine-readable manifest near the top:
  fixed-format comments that both sh and cmd ignore but a parser can
  read. Example:
  ```
  rem MOHABBAT_MANIFEST_BEGIN
  rem slots=6
  rem slot0=triple=x86_64-unknown-linux-musl,offset=1234,len=98765
  rem ...
  rem pool_len=12345678
  rem MOHABBAT_MANIFEST_END
  ```
  Comments work in both shells (`rem` in cmd; sh treats `rem ...` as
  a command name that fails — wrap the whole block so sh never reads it,
  which is already the case if it sits inside the cmd-only portion of
  the polyglot).

### Empty slots

- A slot with length 0 in Zone B means the vegetable does not support
  that platform. Zone A's detection logic must check for length 0 and
  print:
  ```
  mohabbat: this vegetable was built without support for <triple>.
  ```
  then exit with code 1.

### File size

- One washmhost: ~5–8 MB (release, stripped). Six washmhosts: ~30–50
  MB raw. Brotli quality 11 on similar binaries: ~25–35% of raw =
  ~10–15 MB. Plus six brots at ~100 KB each = ~600 KB. Plus the brain
  at ~2–5 MB inside the pool. Total mohab.bat: ~12–20 MB.
- Acceptable. Document it.

### Encoding pitfalls

- All offsets and lengths in the metadata struct are little-endian u64.
  Brot does not need to byte-swap on any of the six targets (all
  little-endian).
- The polyglot header text is ASCII only. No multibyte characters.

### Testing

- Unit tests for the patcher live in `mohabbat/src/logic.rs` (tested
  during `cargo test`). Build a tiny ELF/PE/Mach-O fixture containing 
  the magic, patch it, read it back, assert.
- Integration test: produce a tiny vegetable wrapping a hello-world
  WASM, run it on the host platform, assert exit code 0 and stdout.
- Cross-platform CI: GitHub Actions matrix with one job per Modern
  Four host. Each job builds mohabbat for its own platform, produces a
  vegetable, runs it. macOS and Windows runners cover those branches;
  Linux runners cover the musl branches.
- Seed update: a release job builds on all three host OSes, then
  combines the three partial mohab.bats into one complete seed by
  using mohabbat itself in Mode A (since each partial mohabbat can read
  the other partials' brots and washmhosts from their files). Final
  seed is committed by release tooling.

---

## 10. Order of work

A suggested implementation order. Each step is independently
verifiable.

1. DONE.
2. Create empty `brot/` crate. Add a `_start` for one platform (Linux
   x64). Make it print "brot" and exit. Confirm size < 100 KB.
3. Add the `.mohabbat_meta` section with the magic and six zero u64s.
   Write a stand-alone patcher utility in `mohabbat/src/logic.rs` that 
   opens the file, finds the magic, writes test values, verify by 
   reading them back from a modified `_start`.
4. Add brotli decoder, allocator, file read, decompression. Make brot
   dump the decompressed pool to stdout and exit. Drive it from a tiny
   test vegetable assembled by hand.
5. Add spawn-washmhost-and-pipe-payload. Use a stub washmhost that
   reads stdin and echoes a hash.
6. Port brot to the other five targets. Linux arm64 first
   (cross-compile from Linux is easy), then Windows MSVC x64 (cross
   from Windows or via `cargo-xwin`), then Windows arm64, then macOS
   x64 (needs macOS or osxcross), then macOS arm64.
7. Build the real washmhost statically for all six targets.
   Verify each runs the existing demo WASM.
8. Build the `build.rs` side of `mohabbat/` (no brain yet). Drive
   end-to-end: produce a vegetable that wraps the existing demo WASM.
   Run it on each host platform.
9. Write the brain. The brain or the stitcher is extremely simple: WASM content must be replaced inside the brotlified payload, but everything else stays *so much the same*. It's a matter of slicing, decompression, swapping and swift and decisive re-compression with the highest **ever** brotli level. Devastating.
10. Build mohab.bat from itself: `build.rs` produces the first one,
    then use it to wrap a fresh `mohabbat.wasm` and produce a second
    one. Diff the two to make sure self-replication is stable.
11. Wire the seed-borrow path. Verify partial builds work.
12. CI matrix and release pipeline.

---

## 11. Open questions

These need a decision before or during implementation. Listed so they
are not forgotten.

- Q1. Crate name on crates.io. `mohabbat`? Something else?
- Q2. Do we publish `brot` and `washmhost` as their own crates, or
  keep them workspace-internal? Argument for internal: nobody uses them
  outside this pipeline. Argument for publishing: lets others assemble
  custom vegetables.
- Q3. Brotli quality level. 11 is slowest and best. Acceptable for the
  build step? Probably yes — minutes once per release. Reconsider if
  the brain runs Mode A frequently in interactive use; fall back to
  quality 6 there.
- Q4. Should Zone A also support a `--extract` flag to dump the inner
  WASM out of a vegetable for inspection? Useful for debugging. Cheap
  to add. Default: yes.
- Q5. Do we want `mohab.bat --self-test` that runs the embedded
  brain against a tiny test payload to verify the vegetable is intact?
  Nice but not required for v1.
