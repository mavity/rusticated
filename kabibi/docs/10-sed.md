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

Backreference support starts only after Phase 1 is complete and verified.

The planned implementation extends `regast` rather than adding a second regex
engine:

1. **Parser, AST methods, and dialect boundary:** Recognize backreferences
   while parsing the selected regex dialect via an explicit `syntax.Flags` bit
   (or direct BRE/GNU ERE parser mode) so `\1`–`\9` produce `OpBackref` AST
   nodes rather than colliding with `\1`–`\7` octal character escapes in
   `syntax/parse.go`. Record the referenced capture number in the
   `syntax.Regexp` node; do not infer references by scanning raw pattern text
   outside the parser. Handle `OpBackref` explicitly in `(*Regexp).Equal`
   (comparing `Cap` and `FoldCase` rather than falling through to `return true`,
   which would treat `\1` and `\2` as identical in `simplify1` and parser
   factoring), `calcFlags`, `writeRegexp`, and `(*parser).calcSize`.
2. **Simplification, capture commit/restore, and repeated captures:** Account
   for `(*Regexp).Simplify()` expanding counted repetitions (such as
   `/(a){2,3}\1/` or `(?:(a)\1){2}`) into multiple `OpCapture` nodes that share
   the same `Cap` index:
   * Do not allow an inner or repeated capture opening (`b.cap[2*k]`) to create
     a torn `(new_start, old_end)` pair before its closing `InstCapture`
     commits; stage uncommitted open positions (`b.open[k]`) separately from
     committed `(start, end)` spans in `b.cap[2*k : 2*k+2]`.
   * On backtrack (`arg == true`), an opening `InstCapture` (`2*k`) restores
     the previous `b.open[k]`, while a closing `InstCapture` (`2*k+1`) must
     restore **both** the previous committed `b.cap[2*k]` and `b.cap[2*k+1]`
     (since a single scalar `job.pos` cannot hold both `oldStart` and `oldEnd`
     when a repeated capture's later iteration fails after committing).
   * Define and enforce the bytecode mechanism for whether nested captures
     inside a repeated outer group (e.g. `((a)|b)+\2`) reset on each outer
     iteration or retain their prior iteration's binding according to target
     POSIX/GNU `sed` semantics (standard `InstCapture` only writes `cap[2*k]`
     when the inner group is actually entered).
3. **Compilation, nullability, and static analysis:** Compile each `OpBackref`
   into a dedicated `InstBackref` instruction (carrying the capture index and
   active `FoldCase` flag) and mark `syntax.Prog` with `HasBackref = true`.
   * Keep `frag.nullable = true` for `InstBackref` so `compiler.star` rewrites
     `\1*` via `c.quest(c.plus(...))` (`(\1+)?`), since a backreference can
     match the empty string whenever its referenced group matched empty.
   * Ensure `minInputLen` treats `OpBackref` as minimum length `0`, and ensure
     `Prog.skipNop`, `Prog.Prefix()`, and `Prog.StartCond()` never skip past
     `InstBackref` (keeping `prefixComplete == false` when an `InstBackref`
     is present). Skipping `compileOnePass` for `HasBackref` programs also
     ensures `makeOnePass` never rewrites `InstAlt` into `InstAltMatch`.
4. **Execution routing and capture register sizing:** Keep programs without
   references on their existing one-pass, flat-bitvector, and linear-time
   Thompson NFA execution paths. Route programs with `HasBackref` to the
   ordered backtracking path for every input size; do not send them through
   `compileOnePass` or the Thompson NFA `machine`.
   * In `bitState.reset`, always allocate `b.cap` (and `b.open`) to at least
     `prog.NumCap` and `b.matchcap` to at least `max(ncap, 2)` even when the
     caller passes `ncap == 0` (`Match`) or `ncap == 2` (`FindIndex`,
     `ReplaceAllLiteral`), so referenced groups are always recorded and
     `longest` comparisons never short-circuit on `len(b.cap) == 0`.
   * In `InstMatch`, allow empty matches at `pos == 0` to update `b.matchcap`
     when `old == -1`, and in `re.backtrack`, slice `b.matchcap[:ncap]` when
     appending to `dstCap` so callers receive the exact submatch slice length
     they requested.
5. **Backreference matching and case folding (`[]byte` and `string` inputs):**
   At `InstBackref`, check that the referenced group has a committed valid span
   `[capStart, capEnd]`.
   * Support both `inputBytes` (`ib []byte`) and `inputString` (`is string`)
     entrypoints: for exact matching (`FoldCase == 0`), compare the slice
     `ib[capStart:capEnd]` or `is[capStart:capEnd]` against the subject at
     `pos` and advance `pos` by `capEnd - capStart`.
   * For case-insensitive matching (`FoldCase`), step rune-by-rune through the
     captured span `[capStart, capEnd)` and the subject at `pos` using
     `i.step` (`decodeByteRune` on `[]byte` inputs): valid UTF-8 runes compare
     under `unicode.SimpleFold` (allowing folded matches whose UTF-8 byte
     lengths differ, such as `K` and `k`), while Phase 1 invalid-byte sentinels
     match only the identical raw byte and never fold. Advance `pos` by the
     subject bytes consumed.
   * Captures and match positions continue to refer to byte offsets in the
     original input slice, so invalid bytes remain exact and sentinels are never
     serialized.
6. **Capture-aware visited state:** Change backtracking's visited-state check
   for `HasBackref` programs. The flat `(pc, pos)` bitvector is insufficient
   because different capture bindings at the same `(pc, pos)` can lead to
   different outcomes, and sharing flat `(pc, pos)` bits across start positions
   in an unanchored sweep can falsely prune valid matches or corrupt `longest`
   selection.
   * Key the visited table by `(pc, pos, capStateID)`—interning, for every
     referenced group $k$, both its committed `(start, end)` span **and** its
     uncommitted open offset `open[k]` when group $k$ is currently open at `pc`
     (so distinct paths inside an open referenced group such as `((a|ab)b)\1`
     are not falsely pruned before closing `InstCapture` commits), and scoping
     or resetting per start position.
   * This preserves distinct capture alternatives, keeps memory bounded without
     hashing full capture slices on every step, and guarantees that zero-width
     cycles (including empty-capture repetitions like `(a*)\1*` compiled via
     `(\1+)?`) terminate once `(pc, pos, capStateID)` repeats.
7. **Work limits, error-returning APIs, and `io.RuneReader` policy:** Count
   execution work, including backtracker steps and bytes/runes compared by
   `InstBackref`. Because standard `Regexp` methods (`Match`, `FindIndex`,
   `ReplaceAll`, etc.) do not return `error`, add explicit error-returning
   entrypoints (e.g. `TryFindSubmatchIndex` / `TryReplaceAll` or configurable
   `MaxSteps` search methods used by `sed`) that return a distinct
   `ErrStepLimit` error when the work limit is reached; never report work-limit
   exhaustion as an ordinary non-match. Define an explicit policy for
   `io.RuneReader` entrypoints on `HasBackref` programs (either buffer unread
   stream bytes so captured spans can be re-read, or reject streaming reader
   calls on backreference programs in favor of buffered `[]byte` entrypoints).

Before implementation, define dialect-specific behavior for invalid (out-of-range
capture index), forward (not-yet-closed capture), currently-open (self-nested
`(\1)`), and unmatched capture references, as well as C/POSIX byte-mode vs.
UTF-8 case folding. Do not silently inherit behavior from another regex engine.

Verify the feature with tests for:
* Repeated captures (`/(a){2,3}\1/`, `/(?:(a)\1){2}/`) and nested captures
  (`((a)|b)+\2`)
* Alternate capture paths, empty captures, and unmatched/forward/self-nested
  captures
* Callers with `ncap == 0` (`Match`) and `ncap == 2` (`Find`, `FindIndex`,
  `ReplaceAllLiteral`), in both leftmost-first and POSIX leftmost-longest modes
* Case-insensitive backreferences (`FoldCase`), including multibyte UTF-8
  folding with differing byte widths and non-folding invalid-byte sentinels
* Malformed-byte capture/replay and zero-width cycles
* Work-limit exhaustion returning the distinct limit error rather than `nil` /
  `false`
* Unchanged behavior and benchmark performance for patterns without
  backreferences. Backreferences can require nonlinear work; do not claim a
  linear time bound for programs that use them.

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