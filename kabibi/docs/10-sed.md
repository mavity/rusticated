# Porting `uutils/sed` to Go

Excellent, my research shows that uutils's sed is absolutely the best blueprint. I have researched more and come up with the plan.

You see, using **`uutils/sed` as a blueprint is vastly superior to porting directly from GNU `sed`’s C source**, both legally and architecturally. However, you will hit one major ecosystem hurdle in Go—**regular expression engines**—where `uutils/sed` relies on a dual-engine trick that doesn't natively map to Go's standard library.

## 1. Why `uutils/sed` is a Great Blueprint for Go

* **Clean License (MIT vs. GPLv3):** GNU `sed` is strictly GPLv3. Translating or closely deriving code from GNU `sed` makes your Go port a derivative work under the GPL. `uutils/sed` is **MIT-licensed**, giving you complete freedom to mirror its architecture, AST definitions, and test suites in a commercial or permissively licensed Go codebase.
* **Manageable Scope (~10k SLoC):** GNU `sed` is heavily entangled with `gnulib` macros, custom C memory allocators, and decades of legacy locale/DFA workarounds. `uutils/sed` is roughly **10,000 lines of modern, modular Rust** that cleanly separates CLI flag parsing, script compilation, and stream execution.
* **Already Solved the BRE-to-Modern-Regex Translation:** Standard `sed` defaults to POSIX Basic Regular Expressions (BRE), where `(` and `+` match literals and `\(` and `\+` are metacharacters. Neither Rust nor Go supports BRE natively. `uutils/sed` already contains battle-tested translation logic that converts GNU BRE/ERE quirks into modern regex syntax—which you can port almost 1:1 into Go.
* **Drop-in Test Harness:** You can directly reuse `uutils/sed`’s `./util/run-gnu-testsuite.sh` test shim and its integration benchmarks (`hanoi.sed`, `mandelbrot.sed`, `math.sed`) by simply pointing the test runner at your compiled Go binary.

---

## 2. How `uutils/sed` Maps to Go Idioms

| Architectural Layer | How `uutils/sed` Does It (Rust) | How to Port It to Go |
| --- | --- | --- |
| **AST & Command Representation** | Rust algebraic data types (`enum Command`, `enum Address`) compiled before reading any files. | Go tagged structs or an `interface{ Execute(*Context) }` slice. Since `sed` only has ~25 commands, a flat `Command` struct with an `OpCode` enum avoids interface pointer-chasing. |
| **Execution Cycle State** | A state machine holding the **Pattern Space**, **Hold Space**, append queue (`a`/`r` commands), line counters, and branch flags (`t`/`T`). | Map Pattern and Hold spaces to reusable `[]byte` buffers (or `bytes.Buffer`). Reuse underlying byte arrays via `buf = buf[:0]` between lines to prevent Go GC thrashing. |
| **Fast-Path I/O** | Uses `memchr` for SIMD newline scanning and Linux kernel zero-copy (`copy_file_range` / `splice`) when passing unedited ranges. | Go's `bytes.IndexByte` is already SIMD-accelerated on amd64/arm64, and `io.Copy` automatically uses `splice` / `copy_file_range` on Linux when copying between file descriptors. |

---

## 3. The Catch: Where Rust and Go Diverge

While the parser and VM loop port effortlessly, you will need a Go-specific strategy for three areas where `uutils/sed` leans on Rust crates:

### A. Regex Engine: Reuse `regast`

The repository already contains `mohabbat/regast`, a fork of Go's regexp
implementation, including its parser, compiler, one-pass engine, NFA engine,
and bit-state matcher. Extend that engine for byte-preserving matching rather
than creating another regex implementation or adding a second copy of the Go
engine. Keep the Go regexp syntax and matching semantics for valid UTF-8 input.

#### Phase 1: byte-preserving engine without backreferences

Make byte slices the canonical subject and pattern input for the regex engine.
Do not convert subject data to strings. In the default UTF-8 interpretation,
decode valid UTF-8 as Unicode characters and map each invalid input byte to a
distinct internal sentinel rune in a reserved range above `unicode.MaxRune`.
There are at most 256 such sentinels. They are engine-internal symbols and must
never be encoded into output; matches and captures refer to byte offsets in the
original slices.

Sentinels participate in ordinary matching as distinct symbols: `.` and
negated character classes can match them, repetitions and captures preserve
their exact byte spans, and explicit invalid-byte literals or escapes in a
pattern can match the corresponding byte. Unicode-positive character classes
must not classify sentinels as Unicode characters. Preserve sed's dot behavior
for embedded newlines. If C/POSIX byte mode is supported, it treats each input
byte as an ordinary character rather than applying UTF-8 decoding.

The mapping must be honored consistently by the parser, instruction compiler,
one-pass engine, NFA, bit-state matcher, character classes, boundary checks,
and pattern-prefix optimizations. This is more than changing `inputBytes.step`:
wildcard and complemented-class ranges currently stop at `unicode.MaxRune`, and
the pattern parser must preserve invalid bytes too.

Phase 1 is complete only when:

1. The existing Go regexp test corpus passes for the supported non-backreference
   syntax, with tests for this fork's additional regexp features handled
   separately.
2. Differential tests against Go's standard `regexp` agree on valid UTF-8
   inputs, including match selection, captures, empty matches, and byte
   indexes. Invalid-byte tests verify distinct matching, exact captures, and
   that no sentinel value can leak into output.
3. Repeated, interleaved benchmarks compare `regast` with Go's standard
   `regexp` on equivalent `[]byte` workloads. Report throughput/latency and
   allocations for valid UTF-8 and malformed-byte inputs; do not claim parity
   without measured results.

#### Phase 2: backreferences

Backreference support is explicitly deferred until Phase 1 is complete and
verified. Then detect backreferences from parsed regex syntax, not with a raw
substring scan, and evaluate a backreference-capable engine against respected
Go and Rust implementations. First assess whole-expression fallback. Consider
forking execution only around backreference-dependent portions if correctness
and measured performance justify the additional complexity. Preserve the same
byte identity and output guarantees in either design.

### B. Byte and Character Semantics

Pattern space, hold space, regex patterns, and regex subjects are byte slices.
In UTF-8 mode, valid multibyte characters match as Unicode characters while
invalid bytes remain individually identifiable through the Phase 1 sentinels.
This intentionally differs from Go's existing `[]byte` regexp behavior, which
decodes every invalid byte as the same `utf8.RuneError`.

The regex layer must return byte offsets into the original input. Replacement,
capture, and output code copies bytes from those original slices; it must never
serialize an internal sentinel as a Unicode encoding. Character-oriented sed
operations such as transliteration and case conversion need their own defined
encoding behavior and must not corrupt untouched bytes.

### C. CLI Flag Quirks (`-i` Optional Values)

GNU `sed` allows optional attached flag arguments, most notably `-i` (in-place without backup) vs. `-i.bak` (in-place with backup) vs. `-i .bak` (which GNU `sed` treats as *no* backup suffix, followed by an input file named `.bak`!).

* Neither Go's standard `flag` package nor `spf13/pflag` handles GNU's strict attached-only optional value semantics out of the box.
* Don't trust a 3rd-party Go flag library — look at the custom argument pre-processor logic from `uutils/sed` so `-e`, `-f`, and `-i[SUFFIX]` behave identically to GNU `sed`, but consider how our existing flags parsing can be uplifted if necessary, so any such custom work can be reused for other quirky commands.

What do you think? Reckon good?