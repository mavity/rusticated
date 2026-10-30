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
   (or direct BRE/GNU ERE parser mode), and expose a compilation entrypoint in
   `regast` (e.g. `CompileWithFlags` / `CompileBytesWithFlags` or
   `CompileParsed`) so callers outside `regast/syntax` can enable that flag.
   * Intercept `\1`–`\9` in the top-level `case '\\':` switch of `parse()`
     (consuming a single digit `'1'`–`'9'`) rather than inside `parseEscape`,
     so `\10` is parsed as backreference `\1` followed by literal `'0'` (rather
     than a 2-digit octal escape) and bracket character classes (`[...]`, which
     call `parseEscape`) never emit `OpBackref`.
   * Record the referenced capture number in the `syntax.Regexp` node; do not
     infer references by scanning raw pattern text outside the parser.
   * Reject out-of-range backreferences (`\k` where `k > p.numCap`, the total
     number of capturing groups in the pattern) at the end of `parse()` as a
     compile-time syntax error (`ErrInvalidEscape` or dedicated error code),
     matching POSIX and GNU `sed`/`uutils/sed` behavior and guaranteeing that
     `re.MaxCap()` is never smaller than any `OpBackref.Cap`. Forward,
     currently-open (self-nested `(\1)`), and unmatched references to valid
     group numbers (`1 <= k <= p.numCap`) remain valid at compile time and
     fail to match at runtime until group `k` has a committed span.
   * Also add `EmptyBeginWord` (`\<`) and `EmptyEndWord` (`\>`) to
     `syntax.EmptyOp` (checking `!IsWordChar(r1) && IsWordChar(r2)` and
     `IsWordChar(r1) && !IsWordChar(r2)`), since GNU `sed` and `uutils/sed`
     rely on start/end-of-word assertions that cannot be translated to `\b`.
   * Place `OpBackref` at or before `OpCapture` in `syntax.Op` (or update the
     `sub.Op > OpCapture` precedence check in `writeRegexp`), and register
     `OpBackref` / `InstBackref` in `syntax/op_string.go`, `instOpNames`, and
     `dumpInst`.
   * Handle `OpBackref` explicitly in `(*Regexp).Equal` (comparing `Cap` and
     `FoldCase` rather than falling through to `return true`, which would treat
     `\1` and `\2` as identical in `simplify1` and parser factoring),
     `calcFlags` (returning `(flagI, 0)` when `FoldCase != 0` and `(0, flagI)`
     when `FoldCase == 0`, since a backreference's runtime text is always
     potentially fold-sensitive), `writeRegexp`, and `(*parser).calcSize`.
2. **Simplification, capture commit/restore, and repeated captures:** Account
   for `(*Regexp).Simplify()` expanding counted repetitions (such as
   `/(a){2,3}\1/` or `(?:(a)\1){2}`) into multiple `OpCapture` nodes that share
   the same `Cap` index:
   * Do not allow an inner or repeated capture opening (`b.cap[2*k]`) to create
     a torn `(new_start, old_end)` pair before its closing `InstCapture`
     commits; stage uncommitted open positions (`b.open[k]`, initialized to
     `-1`) separately from committed `(start, end)` spans in
     `b.cap[2*k : 2*k+2]`.
   * When closing `InstCapture` (`2*k+1`) commits `b.cap[2*k] = b.open[k]` and
     `b.cap[2*k+1] = pos`, clear `b.open[k] = -1` so `b.open[k] >= 0`
     accurately reflects whether group $k$ is currently open. On backtrack
     (`arg == true`), an opening `InstCapture` (`2*k`) restores the previous
     `b.open[k]` from `job.pos`, while a closing `InstCapture` (`2*k+1`) must
     restore `b.open[k] = b.cap[2*k]` (the start offset that closing
     instruction had just committed) **and** restore both previous committed
     `b.cap[2*k]` and `b.cap[2*k+1]` via a dedicated LIFO `b.capStack []int` on
     `bitState` (or widened `job` fields), since a single scalar `job.pos`
     cannot hold both `oldStart` and `oldEnd` when a repeated capture's later
     iteration fails after committing, and backtracking into an alternation
     inside group $k$ such as `((a|ab)b)\1` needs `b.open[k]` restored before
     re-closing.
   * Because `tryBacktrackPlain` never pushes `arg == false` branch jobs after
     initial entry (given `InstAltMatch` is disabled for `HasBackref`), every
     mutation to `b.cap` and `b.open` is strictly unwound in LIFO order by
     `InstCapture` (`arg == true`) jobs before any earlier `InstAlt` (`arg ==
     true`) branch is popped. Keep `b.cap` and `b.open` live on `bitState`
     without copying capture arrays onto each `job`.
   * For nested captures inside a repeated outer group (e.g. `((a)|b)+\2`),
     retain the prior iteration's committed binding when the inner capture is
     not entered on a later iteration, matching `regast`'s existing `RE2/Go`
     expectations in `testdata/repetition.dat` and `uutils/sed`'s
     `fancy-regex` behavior.
3. **Compilation, nullability, and static analysis:** Compile each `OpBackref`
   into a dedicated `InstBackref` instruction (carrying the capture index and
   active `FoldCase` flag), mark `syntax.Prog` with `HasBackref = true`, and
   record the referenced capture bit in `Prog.BackrefMask uint16` (bits `1..9`).
   * When compiling `OpBackref`, also bump `c.p.NumCap` to at least
     `2*re.Cap + 2` (and bounds-check `2*k+1 < len(b.cap)` in `InstBackref`),
     because `(*Regexp).Simplify()` rewrites zero-count repetitions like
     `(a){0}\1` to `OpEmptyMatch`, removing the `OpCapture` node before
     `Compile` runs.
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
   ordered plain backtracking path (`tryBacktrackPlain`) for every input size;
   do not send them through `compileOnePass` or the Thompson NFA `machine`, and
   disallow combining `HasNodeGroup` (`⦅...⦆`) with `HasBackref`.
   * In `bitState.reset`, gate the flat `b.visited` bitvector allocation on
     `!prog.HasNodeGroup && !prog.HasBackref` so large inputs on `HasBackref`
     programs never hit `make([]uint32, visitedSize, maxBacktrackVector/visitedBits)`
     with `visitedSize > cap` and panic.
   * When `prog.HasBackref` is true, allocate `b.cap` to at least `prog.NumCap`
     (and `b.open` to `prog.NumCap/2`) and `b.matchcap` to at least
     `max(ncap, 2)` even when the caller passes `ncap == 0` (`Match`) or
     `ncap == 2` (`FindIndex`, `ReplaceAllLiteral`), while leaving non-backref
     `ncap == 0` callers on their zero-length `b.cap` fast path.
   * In `InstMatch`, when `ncap == 0` (`Match`), return `true` immediately even
     for `HasBackref` and `longest` (since a valid match is already proven and
     `Match` discards `b.matchcap`); otherwise allow empty matches at `pos == 0`
     to update `b.matchcap` when `old == -1`, and in `re.backtrack`, slice
     `b.matchcap[:ncap]` when appending to `dstCap` so callers receive the
     exact submatch slice length they requested.
5. **Backreference matching and case folding (`[]byte` and `string` inputs):**
   At `InstBackref`, check that the referenced group has a committed valid span
   `[capStart, capEnd]`.
   * Support both `inputBytes` (`ib []byte`) and `inputString` (`is string`)
     entrypoints: for exact matching (`FoldCase == 0`), compare the slice
     `ib[capStart:capEnd]` or `is[capStart:capEnd]` against the subject at
     `pos` and advance `pos` by `capEnd - capStart`.
   * For case-insensitive matching (`FoldCase`), step rune-by-rune through the
     captured span `[capStart, capEnd)` and the subject at `pos` using
     byte-preserving rune decoding (`decodeByteRune` on `[]byte` inputs, and
     matching `inputString`'s rune decoding on `string` inputs): valid UTF-8
     runes compare under `unicode.SimpleFold` (allowing folded matches whose
     UTF-8 byte lengths differ, such as `K` and `k`), while Phase 1
     invalid-byte sentinels match only the identical raw byte and never fold.
     Advance `pos` by the subject bytes consumed.
   * Captures and match positions continue to refer to byte offsets in the
     original input slice, so invalid bytes remain exact and sentinels are never
     serialized.
6. **Capture-aware visited state:** Change backtracking's visited-state check
   for `HasBackref` programs. The flat `(pc, pos)` bitvector is insufficient
   because different capture bindings at the same `(pc, pos)` can lead to
   different outcomes, and sharing flat `(pc, pos)` bits across start positions
   in an unanchored sweep can falsely prune valid matches or corrupt `longest`
   selection.
   * Key the visited table by `(pc, pos, capStateID)`—interning, only for
     groups $k$ present in `prog.BackrefMask`, both the group's committed
     `(start, end)` span **and** its uncommitted open offset `open[k]` (`-1`
     when closed, `>= 0` when group $k$ is currently open at `pc`, so distinct
     paths inside an open referenced group such as `((a|ab)b)\1` are not
     falsely pruned before closing `InstCapture` commits), and scoping or
     resetting per start position.
   * This preserves distinct capture alternatives, keeps memory bounded without
     hashing full capture slices on every step, and guarantees that zero-width
     cycles (including empty-capture repetitions like `(a*)\1*` compiled via
     `(\1+)?`) terminate once `(pc, pos, capStateID)` repeats.
7. **Work limits, error-returning APIs, and `io.RuneReader` policy:** Count
   execution work across the entire search (not resetting an `O(end)` budget at
   each start position `pos`), including backtracker steps and bytes/runes
   compared by `InstBackref`.
   * When the work limit is exceeded inside `tryBacktrack`, set an explicit
     `b.limitExceeded` flag on `bitState`, immediately abort the outer
     start-position sweep in `re.backtrack`, and discard any partial
     `b.matchcap` recorded in `longest` mode.
   * Because standard `Regexp` methods (`Match`, `FindIndex`, `ReplaceAll`,
     etc.) do not return `error`, add explicit error-returning entrypoints
     (e.g. `TryFindSubmatchIndex` / `TryReplaceAll` or configurable `MaxSteps`
     search methods used by `sed`) that return a distinct `ErrStepLimit` error
     when `b.limitExceeded` is set; never report work-limit exhaustion as an
     ordinary non-match.
   * Reject `io.RuneReader` (`r != nil`) calls on `HasBackref` programs in
     `re.find` (returning `nil` / `false`, matching the existing `HasNodeGroup`
     guard), as `sed` operates exclusively on buffered `[]byte` slices.

Before implementation, define C/POSIX byte-mode vs. UTF-8 case folding and `sed`
default flag mapping (`DotNL` on by default for pattern space; `OneLine` on by
default unless the `s///m` / `M` multi-line flag is active). Do not silently
inherit behavior from another regex engine.

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