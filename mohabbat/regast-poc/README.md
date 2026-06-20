# regast — node-aware regular expressions for Go source

## What this is

`regast` is a small, self-contained Go package that does regex matching on Go
source code, with one extra power: a pattern can contain a **node-group**.

A node-group matches a *whole AST node* (an identifier, an expression, a
statement, a block, ...). The pattern inside the node-group is matched against
that node's text, but:

- comments and the exact amount/form of whitespace are ignored,
- whitespace only matters where it would change the tokens (the AST): a space
  between two identifier characters means "two separate tokens", and the lack
  of one means "the same token". Around operators and punctuation, where
  spacing never changes the AST, it is freely ignorable.

This lets us write transforms that are structurally safe but do not force the
author to think about exact spacing or comments. It replaces the brittle
`strings.ReplaceAll` / line-splitting / hand-written `regexp` patching we do
today in `patch_go_deps.go` and friends.

The package lives entirely under `mohabbat/regast` and depends only on the Go
standard library (`go/scanner`, `go/parser`, `go/ast`, `go/token`).

## Scope of the first version (MVP)

- Go source only. No other languages.
- One file at a time.
- Match and replace. No fancy query language.
- The file must *parse* (be syntactically valid Go). It does **not** need to
  compile or type-check, so platform-specific and build-tagged files are fine.

---

## The two parts

### Part 1 — the preprocessor

Before any matching, we scan the source once and produce two small tables.

1. **Comment spans.** Run `go/scanner` in comment-emitting mode over the raw
   bytes and record the byte range `[start, end)` of every comment. A byte is
   then **significant** when it is neither whitespace nor inside a comment:

   - `significant(i)` — is byte `i` part of real program text?

2. **Node spans.** Run `go/parser` (with `ParseComments` and `AllErrors`) and
   walk the AST with `ast.Inspect`. For every node record its byte range
   `[start, end)`. We do **not** record the node type — we have no way to use
   it and do not need it.

   Store the node spans in one flat slice, sorted by `start`. Because Go ASTs
   are shallow and ordered, a plain binary search (`sort.Search`) finds all
   nodes that begin at a given offset. Several nodes can begin at the same
   offset (e.g. `x` and `x.foo()` both start at `x`); we keep all of them.

All offsets are **byte** offsets (UTF-8). `go/token` positions convert to byte
offsets through the `token.FileSet`. We keep the original raw bytes untouched —
they are what replacement output is built from. We never feed normalized text
back out; normalization only affects *match decisions*.

Parsing uses `go/parser`'s error-tolerant mode: even when a file has errors it
returns a best-effort partial AST, and we take node spans from whatever it
gives us — this is the same recovering parser IDE tooling builds on. Only if
the result is too broken to yield usable spans do we fail and report the file
as unparseable. We never silently skip.

### Part 2 — the matcher

We reuse Go's regex *front end* (the hard, well-tested part) and write our own
*executor* (where the new behaviour lives).

- **Reused, lightly modified:** a vendored copy of `regexp/syntax` — the
  parser, the AST (`Regexp`/`Op`), and the compiler that turns the AST into a
  program (`Prog` of `Inst`).
- **Written fresh:** the executor in `regast` that walks the compiled program.

We do *not* reuse Go's `regexp` runtime engines (`exec.go`'s Pike VM,
`backtrack.go`, `onepass.go`). They are built around plain byte stepping and
are awkward to extend with nested, bounded sub-matches. A purpose-built
recursive backtracking executor over the compiled program is simpler to reason
about and easy to extend. Source files are small, so backtracking cost is not a
practical concern; we still add a step cap as a guard.

---

## Pattern syntax

Everything outside node-groups is ordinary regex over the raw source bytes.

A node-group is written with a distinct bracket pair so it never collides with
normal regex syntax:

```
⦃  ...inner pattern...  ⦄
```

(`⦃` is U+2983 LEFT WHITE CURLY BRACKET and `⦄` is U+2984 RIGHT WHITE CURLY
BRACKET. The ASCII pair `({ })` from the original sketch was **rejected during
implementation**: its closer `})` is indistinguishable from a normal `}`
(repetition close) followed by `)` (group close), e.g. inside `⦃(a{2})b⦄` —
so ASCII delimiters cannot be split without fully re-parsing the regex. The
Unicode brackets never appear in regex syntax and are trivially unambiguous.)

Inside a node-group the inner pattern is lexed in a "spacing-aware" mode:

- A run of whitespace in the pattern is **not** a literal space. It marks a
  **token boundary** that is enforced only between identifier characters (see
  below).
- Matching literal whitespace inside a node-group is out of scope for the MVP.
- Everything else is normal regex.

### Whitespace rules inside a node-group

Whitespace is ignored *where it does not change the AST*. Whitespace changes the
AST only when it sits between two **word characters** (identifier/number
characters) — there it decides whether they are one token or two. Everywhere
else (around operators, dots, parentheses, commas, …) spacing never changes the
tokens, so it is freely ignorable on both the pattern and the source side.

The rule, applied at each point where the matcher moves from one matched
character to the next:

- **Both sides are word characters:** the pattern's spacing must agree with the
  source's. A space in the pattern requires the source tokens to be separated;
  no space requires them to be the same token. (This is what keeps `a b` ≠ `ab`
  and `ab` ≠ `a b`.)
- **Otherwise:** no constraint. Any amount of whitespace or comments — or none —
  is accepted, in the pattern and in the source independently.

Worked examples:

| pattern   | matches `a.b` | matches `a . b` | matches `a/*x*/.b` |
|-----------|---------------|-----------------|--------------------|
| `a\.b`    | yes           | yes             | yes                |
| `a \. b`  | yes           | yes             | yes                |

Spacing around the `.` is irrelevant because `.` is not a word character, so all
four combinations match (they are the same AST). By contrast:

| pattern | matches `xy` (one ident) | matches `x y` (two idents) |
|---------|--------------------------|----------------------------|
| `xy`    | yes                      | no                         |
| `x y`   | no                       | yes                        |

Here both sides are word characters, so the pattern's spacing must match the
source's. `a + b` matches both `a+b` and `a + b`, because the junctions around
`+` are not word–word.

How it works mechanically: while matching inside a node-group the executor skips
insignificant bytes (whitespace/comments) freely, and at each junction between
two consumed characters it applies the rule above using the actual characters on
each side. A whitespace run in the pattern is compiled to a zero-width GAP
marker that records "the pattern had a space here" for the next junction check.

---

## How node-groups are realized

The original plan proposed forking the vendored `regexp/syntax` parser to add an
`OpNodeGroup` operator and new `InstNodeGroup` / `InstGap` instructions. During
implementation this was replaced by a simpler, less invasive approach that keeps
the vendored `regexp/syntax` package **completely pristine** — which means the
upstream test mandate is satisfied automatically.

Instead of forking the parser, `regast` *lowers* a node-aware pattern into an
ordinary regex string before handing it to the stock parser/compiler:

- Each node-group `⦃ X ⦄` becomes a capturing group wrapping three private-use
  marker runes around the lowered inner pattern:
  `( \x{E010} lower(X) \x{E011} )` — `E010` = ENTER, `E011` = EXIT.
- Inside a node-group, each run of whitespace becomes a single GAP marker rune
  `\x{E012}` (leading/trailing whitespace trimmed).
- Outside node-groups the pattern is passed through unchanged, so plain patterns
  behave like ordinary regex.

The lowered string is parsed and compiled by the **unmodified** vendored
`syntax` package. The markers survive compilation as single-rune instructions.
The custom executor (below) intercepts those marker runes and applies the
node-group, gap, and significance rules. Because everything lives in one
compiled program, capture-group numbering is global: explicit groups and
node-groups are numbered left-to-right together, so `$1`, `$2`, … work across
both.

Files:
- [lower.go](lower.go) — pattern lowering and node-group bracket scanning.
- [exec.go](exec.go) — the executor and marker interception.
- [preprocess.go](preprocess.go) — significance and node tables.
- [regast.go](regast.go) — `Compile`, `Find`, `FindAll`, `Replace`.

The inner pattern is matched under different rules (bounded region, gap and
significance handling) purely through executor state, not separate compilation.

---

## The executor

A recursive backtracking walk of the program with one cursor into the source
and one into the program. Standard regex ops (literal, char class,
alternation, repetition, capture groups, anchors) behave normally over raw
bytes.

Three new behaviours, triggered when the executor reaches one of the marker
runes:

### 1. ENTER marker at source position `pos`

1. Binary-search the node table for every node whose `start == pos`.
2. For each candidate node `[pos, end)` (largest first), run the following
   inner instructions bounded to `end`, under the node-group whitespace rules
   (below). The match must **land exactly at `end`**: when the EXIT marker is
   reached, skipping any trailing insignificant bytes must arrive at `end` and
   no further.
3. Several candidates (nested or same-start nodes) are just alternatives — try
   each; the first that lets the rest of the outer pattern succeed wins. This
   is the same ambiguity regex already handles with alternation/backtracking.
4. On success the outer cursor jumps to `end` and matching continues.

Each node-group is wrapped in a capturing group, so its span `[pos, end)` is
recorded and a replacement can refer back to the node's **raw** text (comments
and all).

### 2. Consuming instructions inside a node-group

A consuming instruction (literal, `.`, character class) first skips any
insignificant bytes (whitespace/comments) from the current position, then
matches the next significant byte. Before matching it applies the
**word-character junction rule**: if the previously consumed character and this
one are both identifier characters, the pattern's spacing (whether a GAP marker
was crossed) must agree with whether the source actually separated them;
otherwise there is no constraint. Capture-group boundaries recorded inside a
node-group are snapped to significant bytes, so a group never includes
surrounding whitespace or comments.

### 3. GAP marker (zero-width)

A run of whitespace in the pattern compiles to a GAP marker. It consumes nothing;
it just records that the pattern had a space, which the next consuming
instruction uses for the word-character junction rule above.

A step counter caps total work to keep a pathological pattern from running away.

---

## Public API (first cut)

```go
package regast

// Source is a preprocessed file: raw bytes + comment table + node table.
type Source struct { /* ... */ }

func Preprocess(filename string, src []byte) (*Source, error)

// Pattern is a compiled node-aware regex.
type Pattern struct { /* ... */ }

func Compile(pattern string) (*Pattern, error)

func (p *Pattern) Find(s *Source) (loc []int, ok bool)
func (p *Pattern) FindAll(s *Source) [][]int
func (p *Pattern) Replace(s *Source, repl string) ([]byte, error)
```

- `loc`/`FindAll` return byte offsets into the original source.
- `Replace` supports normal `$1` group references; a node-group is a group, so
  `$1` expands to the node's raw text. Output is built from the untouched
  source bytes.

---

## Build order (status: MVP implemented)

1. **Done.** Vendored a pristine `regexp/syntax` under
   `mohabbat/regast/syntax` together with every upstream test; all pass
   unchanged. Wrote the recursive executor; plain regex (no node-groups) is
   checked against stdlib behaviour in tests.
2. **Done.** Preprocessor: comment table via `go/scanner`, node table via
   `go/parser` + `ast.Inspect`. `significant` and node-start lookups are
   unit-tested. (A token table / `tokenStart` proved unnecessary — significance
   is computed directly as "not whitespace and not in a comment".)
3. **Done (as lowering).** Node-group `⦃ ⦄` parsing and the ENTER / EXIT / GAP
   markers, realized by lowering over the pristine `syntax` package rather than
   forking the parser (see "How node-groups are realized").
4. **Done.** The three executor behaviours; the `ab` / `a b` / `a/* */b` gap
   cases and nested / same-start nodes are tested.
5. **Done.** `Replace` with `$1` node references.
6. **Pending.** Port one real transform from `patch_go_deps.go` to `regast` as
   proof, keeping the old code until the new path is trusted.

---

## Testing mandate (non-negotiable)

- **Keep every upstream test.** All unit tests from `regexp/syntax` are vendored
  alongside the code and must keep passing unchanged. They are the safety net
  proving our edits did not break standard regex behaviour. A failing or deleted
  upstream test blocks the change.
- **Full coverage of the new behaviour.** Every use case and edge case of
  node-groups, the whitespace rule, and the executor must have explicit tests.
  The primary evidence is a **golden table** of `{input, pattern, replacement,
  expected output}` cases that double as usage documentation
  ([regast_test.go](regast_test.go), `TestGolden`). No behaviour ships untested.
  This includes at minimum:
  - the word-character rule both ways (`x y` ≠ `xy`, `ab` ≠ `a b`) and the
    free-spacing cases around operators (`a + b` = `a+b`);
  - comments and arbitrary spacing standing in for each other;
  - nested node-groups and several nodes sharing one start offset;
  - the "land exactly at `end`" rule, including greedy patterns that must not
    spill past `end`, and same-start nodes (identifier vs selector);
  - round-trip fidelity of `Replace` (raw text preserved, `$1` node references
    expand to raw source) and capture boundaries that exclude whitespace;
  - plain-regex parity with the standard library;
  - unparseable / partially-parseable input handling;
  - CRLF input on Windows.
- **Coverage is enforced, not aspirational.** Treat anything less than complete
  case coverage as an incomplete change.

---

## Edge cases and risks (tracked deliberately)

- **Unparseable files.** We use `go/parser`'s error-tolerant mode and take node
  spans from its best-effort partial AST (the same recovering parser IDE tools
  rely on). Only when the result is too broken to yield usable spans do we fail
  and report the file as unparseable — never a silent skip.
- **Byte vs rune offsets.** Everything is byte-based UTF-8 to match both
  `go/token` and `regexp` defaults; no mixing.
- **Same-start and nested nodes.** Handled as alternatives; the binary search
  returns all of them.
- **Trailing comments/whitespace in a node.** The "land exactly at `end`" rule
  allows skipping only insignificant trailing bytes, never spilling into the
  next node.
- **CRLF line endings (Windows).** `go/scanner` handles them; offsets are byte
  offsets into the exact bytes we were given, which are also the bytes we edit.
- **Performance.** Files are small and ASTs shallow; backtracking plus a step
  cap keeps worst cases bounded.
- **Replacement fidelity.** Matching normalizes (ignores comments/space) but
  output is always rebuilt from raw source; we never emit normalized text.
