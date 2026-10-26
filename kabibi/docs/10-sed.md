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

### A. The Regex Engine Problem (The Biggest Hurdle)

* **What `uutils/sed` does:** Rust’s standard `regex` crate (like Go’s `regexp`) uses a finite automaton and refuses to support backreferences (`\1`) in matching patterns. To get GNU compatibility without sacrificing speed, `uutils/sed` inspects the pattern: if there are no backreferences, it uses the fast `regex` crate (`[]byte` friendly); if the pattern contains `\1`–`\9`, it falls back to the **`fancy-regex`** crate (which adds a backtracking VM, though it requires valid UTF-8 strings—which is why `uutils/sed` notes that backreferences don't work on raw `LC_ALL=C` invalid UTF-8 bytes).
* **What you must do in Go:** Go’s standard `regexp` package (RE2 syntax) **strictly forbids backreferences in search patterns**. You should copy `uutils/sed`’s dual-engine architecture in Go:
1. **Fast path (`regexp`):** When the translated regex has no backreferences (99% of script commands), run Go's native `regexp` directly on `[]byte` slices.
2. **Backreference fallback:** When the lexer detects `\1`–`\9` in the *search* pattern, fall back to a pure-Go backtracking engine like **`[github.com/dlclark/regexp2](https://github.com/dlclark/regexp2)`** or **`modernc.org/regexp`**, or CGo-free PCRE (`modernc.org/libpcre`).



### B. `string` vs. `[]byte` (Avoiding UTF-8 Corruption)

Real-world shell scripts often run `sed` over files containing mixed encodings or arbitrary binary bytes (`LC_ALL=C`).

* In Go, converting arbitrary `[]byte` to `string` and iterating with `range` replaces invalid UTF-8 sequences with `\uFFFD`, silently corrupting the stream.
* Design your Pattern Space and Hold Space strictly around **`[]byte`**, only decoding runes via `utf8.DecodeRune` when executing character-level operations like the `y/source/dest/` transliteration command or `\U`/`\L` case conversion escapes.

### C. CLI Flag Quirks (`-i` Optional Values)

GNU `sed` allows optional attached flag arguments, most notably `-i` (in-place without backup) vs. `-i.bak` (in-place with backup) vs. `-i .bak` (which GNU `sed` treats as *no* backup suffix, followed by an input file named `.bak`!).

* Neither Go's standard `flag` package nor `spf13/pflag` handles GNU's strict attached-only optional value semantics out of the box.
* Don't trust a 3rd-party Go flag library — look at the custom argument pre-processor logic from `uutils/sed` so `-e`, `-f`, and `-i[SUFFIX]` behave identically to GNU `sed`, but consider how our existing flags parsing can be uplifted if necessary, so any such custom work can be reused for other quirky commands.

What do you think? Reckon good?