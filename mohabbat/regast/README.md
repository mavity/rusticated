# regast — node-aware regular expressions for Go source

## What this is

`regast` is a small, self-contained Go package that does regex matching on Go
source code, with one extra power: a pattern can contain a **node-group**.

A node-group matches a *whole AST node* (an identifier, an expression, a
statement, a block, ...). The pattern inside the node-group is matched against
that node's text, but:

- comments and the exact amount/form of whitespace are ignored,
- but a space in the pattern requires a gap in the source at that point, and no
  space forbids one (an exact correspondence, not loose tolerance).

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

1. **Token spans.** Run `go/scanner` in comment-emitting mode over the raw
   bytes. Record the byte range `[start, end)` of every real token (comments
   excluded). Comments and whitespace are *not* counted, so any byte not inside
   a token span is "insignificant".

   From this we answer one cheap question:
   - `significant(i)` — is byte `i` inside a real (non-comment) token?

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
normal regex groups or character classes:

```
({  ...inner pattern...  })
```

(`(` immediately followed by `{` is not legal in normal regex — `{` as the
first character of a group is meaningless — so `({` is safe to special-case.
A Unicode pair such as `⟬ ⟭` can be added later as an alias.)

Inside a node-group the inner pattern is lexed in a "spacing-aware" mode:

- A run of whitespace in the pattern is **not** a literal space. It becomes a
  **gap assertion** (see below).
- Matching literal whitespace inside a node-group is out of scope for the MVP —
  whitespace exists only to assert gaps.
- Everything else is normal regex.

### Whitespace rules inside a node-group

Whitespace in the pattern is an **exact gap marker**. Its *form and amount*
don't matter (one space, many spaces, a newline, or a comment are all equal),
but its *presence or absence* must match the source exactly. The rule is a
strict correspondence:

- **Space in the pattern ⟺ a gap in the source.** Where the pattern has
  whitespace, the source must have at least one insignificant byte (whitespace
  or comment) at that point.
- **No space in the pattern ⟺ no gap in the source.** Where the pattern has no
  whitespace, the two significant bytes must be directly contiguous in the
  source.

This holds in *both* directions — there is no "skip whitespace by default".

Worked examples:

| pattern | source `ab` | source `a b` | source `a/* x */b` |
|---------|-------------|--------------|--------------------|
| `ab`    | match       | no match     | no match           |
| `a b`   | no match    | match        | match              |

`a b` never matches `ab` (the pattern demands a gap the source lacks). `ab`
never matches `a b` (the source has a gap the pattern forbids). A comment counts
as a gap exactly like whitespace.

How it works mechanically: consuming instructions only ever match **significant**
bytes and never step over insignificant ones, so adjacency is enforced for free.
The executor crosses insignificant bytes **only** at an explicit gap marker
(`InstGap`), which also requires that at least one insignificant byte was
actually there.

---

## Changes to the vendored front end

Small, surgical additions:

- **AST (`syntax/regexp.go`):** add one operator, `OpNodeGroup`, holding the
  compiled-or-to-be-compiled inner pattern as a sub-expression.
- **Parser (`syntax/parse.go`):** recognise `({` and `})`; parse the inner
  pattern in spacing-aware mode; emit an `OpNodeGroup` node. Whitespace runs in
  that mode become a new zero-width marker instead of literal spaces.
- **Program (`syntax/prog.go`):** add `InstNodeGroup` (carries a reference to
  the inner sub-program) and a zero-width `InstGap` for the gap assertion.
- **Compiler (`syntax/compile.go`):** compile `OpNodeGroup` into an
  `InstNodeGroup` that points at the separately compiled inner sub-program;
  compile pattern whitespace into `InstGap`.

The inner pattern is compiled into its **own** program so the executor can run
it under different rules (bounded region, node-group whitespace rules).

---

## The executor

A recursive backtracking walk of the program with one cursor into the source
and one into the program. Standard regex ops (literal, char class,
alternation, repetition, capture groups, anchors) behave normally over raw
bytes.

Three new behaviours:

### 1. `InstNodeGroup` at source position `pos`

1. Binary-search the node table for every node whose `start == pos`.
2. For each candidate node `[pos, end)`, try matching the inner program over
   the slice `[pos, end)` under the node-group whitespace rules (below). The match
   must **land exactly at `end`**: after the inner program finishes, skipping
   any trailing insignificant bytes must reach `end` and no further.
3. Several candidates (nested or same-start nodes) are just alternatives — try
   each; the first that lets the rest of the outer pattern succeed wins. This
   is the same ambiguity regex already handles with alternation/backtracking.
4. On success the outer cursor jumps to `end` and matching continues.

The node-group also records the matched span `[pos, end)` as a capture, so a
replacement can refer back to the node's **raw** text (comments and all).

### 2. Consuming instructions match significant bytes only

A consuming instruction (literal, `.`, character class) matches the byte at the
current position **only if it is significant**. It never steps over whitespace
or comments. This makes "no space in the pattern = no gap in the source"
automatic: a literal simply fails if it lands on an insignificant byte.

### 3. `InstGap` (zero-width)

Advance `pos` over insignificant bytes:

```
start := pos
while pos < end and not significant(pos): pos++
```

then require that at least one byte was skipped (`pos > start`), else this path
fails. This is the "a gap must exist here" assertion. It is the *only* place the
executor crosses insignificant bytes.

A step counter caps total work to keep a pathological pattern from running away.

---

## Public API (first cut)

```go
package regast

// Source is a preprocessed file: raw bytes + token table + node table.
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

## Build order

1. Vendor `regexp/syntax` under `mohabbat/regast/syntax` **together with every
   unit test from the upstream package**; keep those tests passing unchanged.
   Write the recursive executor; confirm plain regex (no node-groups) matches
   stdlib behaviour.
2. Build the preprocessor: token table via `go/scanner`, node table via
   `go/parser` + `ast.Inspect`. Unit-test `significant`, `tokenStart`, and
   node-start lookups against small fixtures.
3. Add `({ })` parsing, `OpNodeGroup`, `InstNodeGroup`, `InstDelimiter`.
4. Add the three executor behaviours; test the `a b` / `ab` / `a/* */b` cases
   and nested / same-start nodes.
5. Add `Replace` with `$1` node references.
6. Port one real transform from `patch_go_deps.go` to `regast` as proof, keep
   the old code until the new path is trusted.

---

## Testing mandate (non-negotiable)

- **Keep every upstream test.** All unit tests from `regexp/syntax` are vendored
  alongside the code and must keep passing unchanged. They are the safety net
  proving our edits did not break standard regex behaviour. A failing or deleted
  upstream test blocks the change.
- **Full coverage of the new behaviour.** Every use case and edge case of
  node-groups, the gap rule, and the executor must have explicit tests. No
  behaviour ships untested. This includes at minimum:
  - the gap correspondence table above (`ab`/`a b` × `ab`/`a b`/`a/* */b`);
  - comments standing in for whitespace, including multi-line and trailing;
  - nested node-groups and several nodes sharing one start offset;
  - the "land exactly at `end`" rule, including trailing comments/whitespace
    inside a node and greedy patterns that must not spill past `end`;
  - byte offsets returned by `Find`/`FindAll` and round-trip fidelity of
    `Replace` (raw text preserved, `$1` node references expand to raw source);
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
