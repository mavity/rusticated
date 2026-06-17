<!--
function __README__() { /*
-->
# Mohabbat

मोहब्बत &mdash; love.

Mohabbat is a tool for building a single binary capable of running anywhere: Windows, Linux, macOS*, even on Android**.

We call such binaries **🍆vegetables** and Mohabbat comes with Rust and Go integration.

<sup>* macOS is not yet supported, but it will be in the future.</sup>
<sup>** Android support is experimental.</sup>

<pre class=splash>
  __  __
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

</pre>

## Terminology

- **🍆vegetable** — a polyglot file produced by Mohabbat, typicall with extension `*.bat`.
- **Mohabbat** — the self-hosting vegetable that is also a builder. Its
  filename is `mohab.bat`.
- **rusticated** — a set of APIs and libraries exposing those to create rich modern platform in WASM.
- **brot** — a small Rust loader that unpacks the payload and launches it.
- **washmhost** — a wrapper around Wazero, exposing the
  rusticated ABI to a guest WASM module, and runs it.
- **brain** — WASM module compressed and embedded in a 🍆vegetable.
- **pool** — the Brotli-compressed concatenation of all washmhosts plus
  the brain.
- **Modern Five** — the target matrix that Mohabbat addresses: Linux x64/ARM, Windows x64/ARM, macOS ARM.

## What it is and how it works

This project includes Rust and Go code orchestrating the development of Mohabbat vegetables.

At launch time a vegetable extracts from its own file a binary runtime capable of running WASM, the WASM brain that is the actual app code, and runs it.

The custom set of low-level APIs (rusticated platform) can be loosely considered analogoous to WASI but asynchronous and richer. Unlike WASI, for rusticated secure sandboxing is explicitly **not** a goal.

# Building and running

**Mohabbat-go** is the main orchestrator for building the rusticated support libraries, and 🍆vegetables themselves.

It can be run both as a native binary, and as a 🍆vegetable too! Naturally, the name of the 🍆vegetable is `mohab.bat`.

Every example below can be run as `go -C mohabbat-go run . <...>` or as `mohab.bat <...>`. Of course to achieve the latter you need to get that mohab.bat vegetable built first, hence the native build is the first step.

## Build the core libraries and mohab.bat

```bash
go -C mohabbat-go run .
```

or

```
mohab.bat
```

This rebuilds the core libraries (Rust and Go) and the mohab.bat vegetable itself.

## Building 🍆🍆vegetables

```bash
go -C mohabbat-go run . demo -o demo.bat
go -C mohabbat-go run . demo/loch -o loch.bat
go -C mohabbat-go run . kabibi -o kabibi.bat
go -C mohabbat-go run . kabibi -o kabibi-go.bat
go -C mohabbat-go run . demo-go -o demo-go.bat
go -C mohabbat-go run . demo-go/trivial -o trivial.bat
```

or

```bash
mohab.bat demo -o demo.bat
mohab.bat demo/loch -o loch.bat
mohab.bat kabibi -o kabibi.bat
mohab.bat demo-go -o demo-go.bat
mohab.bat demo-go/trivial -o trivial.bat
```

You can run a 🍆vegetable directly on any machine from the currently supported targets.

Note that building of a vegetable takes a few minutes due to extreme brotli compression. Makes them small though: 5-6Mb.

## Run projects on rusticated

For development spending few minutes to compress a vegetable only to see a bug is not ideal. There is a way to skip the vegetable build and run the project directly on rusticated WASM host.

```bash
go -C mohabbat-go run . demo -r
go -C mohabbat-go run . demo/loch -r
go -C mohabbat-go run . kabibi -r
go -C mohabbat-go run . demo-go -r
go -C mohabbat-go run . demo-go/trivial -r
```

or

```
mohab.bat demo -r
mohab.bat demo/loch -r
mohab.bat kabibi -r
mohab.bat demo-go -r
mohab.bat demo-go/trivial -r
```


# 🍆Vegetable file layout

A 🍆vegetable is a single file with three zones concatenated end to end.

```
[Zone A: polyglot script header   ]  text, small, executable by sh + cmd
[Zone B: brot table               ]  N native loader binaries, back to back
[Zone C: brotli pool              ]  one brotli stream, all hosts + payload
EOF
```

## Zone A — polyglot script header

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

## Zone B — brot

- Each brot is a complete native executable (ELF / PE / Mach-O).
- Brots are concatenated with no padding.
- Their offsets and lengths are baked into Zone A at assembly time.
- The Zone A header does not parse anything — it only slices by known
  numbers.

Brot is built in Rust with precautions making it tiny and slim. That means no-std (no Rust standard library, relying strictly on direct OS syscalls), no-main (no Rust preamble, relying on target OS entry point conventions) and no CRT (C runtime sometimes is linked in by Rust apps, which is completely unnecessary in this case). The brot is a single binary that decompresses the pool and launches the washmhost.

Brot is carefully coded to avoid some common traps that pull in unnecessary general Rust boilerplate. For example, brot implements a rudimentary simplistic memory allocator, which is enough for its narrow goal.

The Rust project for brot is designed to be cross-compiled for all supported targets with parameters passed to remove fluff and achieve highest size optimisation. The brot compilation options are a part of mohabbat-go builder.

## Zone C — brotli pool

- Single brotli stream. Decoder is in the brot.
- Decompressed content is a fixed sequence: washmhost #1, washmhost #2,
  ..., washmhost #N, payload. Lengths and order are baked into each brot
  as constants (see §6 on patching).
- One stream rather than N streams: cross-binary redundancy between
  washmhosts is significant; joint compression matters.

# Washmhost

The WASM host runtime uses Wazero, a pure-Go WASM runtime. There are no clever tricks in the host: it simply runs Wazero in the way it's intended, and implements **host functions** that constitute rusticated WASM ABI*.

<sup>*ABI = Application Binary Interface, a fancy way to say very rigid and low-level API.</sup>

The host is compiled for all supported targets, and is embedded in the brotli pool. The brot decompresses the pool and launches the host.

# Rusticated Overlay-go

In order for Go code to run on rusticated WASM ABI, the way Go reads and writes files, accessess network or environment must be re-implemented on top of that rusticated WASM ABI.

This is what overlay-go does. Go allows 'overlaying' or swapping system libraries with custom implementations. Our **overlay-go** was carefully designed to fit with **Go 1.26.4** and makes normal unmodified Go code to run inside that WASM without any changes or limitations.

The beauty of Go on rusticated is that its *green threads* (goroutines) are perfectly fit to rusticated WASM async model. That means while one goroutine is waiting for a file or network read, another goroutine can run and do something else. This is a very important feature of Go that makes WASM hosting very efficient and viable.

# Example projects

[demo-go](demo-go) — a simple Go project that runs on rusticated, demonstrating file I/O, network and timeouts showing cancelling io tasks.

[demo-go/trivial](demo-go/trivial) — a very trivial Go project that's barely hello world.

[kabibi-go](kabibi-go) — a flagship project that fuses shell (mvdan.cc/sh) file manager and AI chat bot. It's a work in progress and best works in native, making it fully fit onto Mohabbat rusticated platform is our current goal.

# Rusticated sysroot

For Rust to have the same freedom as Go on rusticated, sacrifices have to be made.

Unlike Go, many Rust's system APIs are synchoronous and blocking. The language supports async/await, but it's still not a default for I/O APIs. In that sense it's worth mentioning [Compio](https://crates.io/crates/compio) library that's the current industry answer to the problem.

The Rust team are moving Rust to async model too but that may take years. Our rusticated sysroot jumps the gun now, rewriting system libraries in a way that completely removes blocking APIs and replaces them with asynchronous. It's much more drastic than what Compio does: normal file I/O and stdio is completely absent on rusticated and replaced with async.

## Expample projects

That means many third-party libraries will fail to build. But the project contains a set of demos for at least few basic examples:

[demo](demo) — reading and writing files, reading/writing from terminal, using timeouts and concurrency to cancel io.

[lock](demo/loch) — a simple prototype of a two-panel file manager UI on ratatui, with file operations and navigation.

[kabibi](kabibi) — a bit more complex prototype for the same, including mock shell and mock side panel with typing and interaction.

<!--
*/ }
-->

