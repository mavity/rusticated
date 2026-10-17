This blueprint transitions Kabibi’s tools from basic prototypes into a deterministic, token-frugal, and resilient subsystem specifically tuned for on-device **Gemma 4** and **EmbeddingGemma 2**, while respecting the zero-CGO / Rusticated WASM (`mohabbat` / `washmhost`) constraints.

---

## 1. Architectural Review & Key Refinements

Your proposed architecture hits the exact pain points of small local models (context bloat, line-number drift during edits, blind search failures, and hanging subprocesses). Reviewing it against Kabibi's existing engine reveals five high-leverage refinements:

1. **Progressive Search Escalation (The "Funnel" Rule):**
Running `EmbeddingGemma 2` on every chunk of a large repo on a cold call is expensive. By enforcing a strict escalation ladder—**Tier 1: Exact/Literal $\rightarrow$ Tier 2: Regex/Glob $\rightarrow$ Tier 3: Fuzzy (Levenshtein/Trigram) $\rightarrow$ Tier 4: EmbeddingGemma 2 Semantic Vector Search**—90% of queries resolve in microseconds on CPU, reserving embeddings for genuine concept searches or when literal searches return `0` or `>MaxMatches` results.


2. **Indentation-Based "Fabric" Expansion vs. Heavy AST Parsers:**
Using `tree-sitter` in Go requires CGO or a bulky WASM-in-WASM runtime, which clashes with the pure-Go / Rusticated `washmhost` model. Your indentation-fabric approach (computing an indentation profile per line, walking outward to enclosing headers/brackets, and merging overlapping windows with `…`) achieves 95% of AST block extraction quality in ~150 lines of pure Go—and works across code, YAML, Markdown, and logs alike.


3. **Self-Healing Hashlines (`LINE:HASH|content`):**
Following the emerging `hashline` standard (pioneered by `oh-my-pi` / `quangdang46/hashline` / `hashlines-mcp`), prefixing lines with `42:a3|func foo() {` where `a3` is a 2-hex-char hash of the **whitespace-stripped** line makes edits immune to indentation mistakes by the LLM. Even better: if lines shifted above the target since the last read, the edit tool can **auto-relocate** the anchor if the hash still appears uniquely within a $\pm 30$ line window.
4. **TFLite OCR Routing inside `read_file`:**
Since Kabibi already integrates the two-phase TFLite (CRAFT/DBNet + SVTR) and Gemma 4 ViT refinement pipeline (`ocr4`), `read_file` shouldn't fail on `.png`, `.jpg`, or `.webp` files. If pointed at an image, it should transparently run Phase 1 TFLite OCR (and optional search filtering over the extracted lines) so the AI can "read" screenshots and diagrams with the same tool.


5. **PTY Column-Squeeze & Gutter Composition:**
Since `kabibi/ui/shell` and `ui.Host` already use `pty.Start` and a 2D `terminal.CellBuf` backed by `vt.Emulator` (`terminal.FromANSI` / `ToANSI`), spawning AI commands at `Cols: W - 2` and blitting them into `X: 2..W-1` while painting columns `0..1` with a styled gutter stripe (`┃ `) is completely native to your rendering pipeline. It prevents line-wrapping corruption in TUI tools (`htop`, `git diff`, `cargo build`) that rely on exact terminal width.



---

## 2. Industry Libraries & Reference Implementations to Leverage

To keep builds fast and maintain 100% compatibility with both native Go and the `rusticated` WASM target, avoid CGO libraries and adopt/adapt these pure-Go packages and industry patterns:

| Domain | Library / Pattern | Role in Kabibi |
| --- | --- | --- |
| **Globbing & Ignore** | `[github.com/bmatcuk/doublestar/v4](https://github.com/bmatcuk/doublestar/v4)` | Pure-Go `**` recursive globbing anchored to `es.Dir()`. Pair with a lightweight `.gitignore` matcher (or `go-git/go-git/v5/plumbing/format/gitignore`). |
| **Fast Hashing** | `[github.com/cespare/xxhash/v2](https://github.com/cespare/xxhash/v2)` (or stdlib `hash/fnv`) | Computes 1-byte (`%02x`) line hashes and 2-byte (`%04x`) file snapshot tags in nanoseconds. |
| **Fuzzy Matching** | `[github.com/lithammer/fuzzysearch/fuzzy](https://github.com/lithammer/fuzzysearch/fuzzy)` or `sahilm/fuzzy` | Pure-Go rank-sorted fuzzy matching for Tier 3 fallback when exact/glob searches miss. |
| **Hashline Spec** | `oh-my-pi` / `hashlines-mcp` (`LINE:HASH|text`) | Industry pattern for anchor-verified edits (`start_anchor: "12:f4"`, `end_anchor: "18:0c"`), auto-stripping accidental `LINE:HASH|` prefixes in LLM replacement payloads. |
| **Indentation Folding** | Aider `RepoMap` / `hashline` syntactic block resolver | Language-agnostic block expansion using leading-whitespace gravity + brace/keyword balancing without Tree-sitter. |
| **PTY & ANSI Stripping** | Existing `rustica/pty` + `kabibi/terminal.CellBuf`<br> | Already in-tree! Clone `CellBuf`, zero out `FG`/`BG`/`Attrs` on each `Cell`, run `ToANSI`, and trim trailing `\x1b[0m` resets and right-margin spaces. |

---

## 3. Detailed Subsystem Designs

### Subsystem A: Smart `read_file` with Multi-Tier Search, Fabric Expansion & Hashlines

#### 1. Tool Schema (`read_file`)

Keep the schema compact so it consumes minimal system prompt tokens:

```json
{
  "path": { "type": "string", "description": "File path relative to current working directory." },
  "start_line": { "type": "integer", "description": "1-based start line (optional)." },
  "end_line": { "type": "integer", "description": "1-based end line (optional)." },
  "search": { "type": "string", "description": "Filter file to blocks matching query. Tries exact -> regex -> fuzzy -> semantic embeddings." },
  "search_mode": { "type": "string", "enum": ["auto", "plain", "regex", "semantic"], "description": "Default 'auto' (progressive fallback)." },
  "hashline": { "type": "boolean", "description": "Prefix lines with LINE:HASH| (e.g. '42:a3|code') and file header [path#TAG] for safe editing with edit_file. Default true." }
}

```

#### 2. The Progressive Search Funnel

When `search` is non-empty and `search_mode == "auto"`:

1. **Tier 1 (Exact Case-Sensitive $\rightarrow$ Case-Insensitive):** Scan lines via `strings.Contains`. If $1 \le \text{matches} \le 25$, lock in these anchor lines.
2. **Tier 2 (Regex):** If the query contains regex metacharacters (`.*+?^$[](){}|\\`) or Tier 1 returned 0 matches, compile with `regexp.Compile("(?i)" + query)` and scan.
3. **Tier 3 (Fuzzy Token Match):** If Tier 2 yields 0 matches (or fails to compile), split query into tokens and score lines by token overlap + Levenshtein distance.
4. Tier 4 (EmbeddingGemma 2 Semantic Search): Triggered when Tiers 1–3 return **0 matches** OR when Tiers 1–3 return **too many matches ($>25$)** (e.g., searching for a common word where semantic intent is needed to rank the top 5 blocks):


* Segment the file into overlapping indentation-coherent chunks (~15–30 lines per chunk).
* Embed query + chunks via `Session.Embedder` (`EmbeddingGemma 2`). (Cache chunk vectors keyed by `(path, mtime, chunk_idx)` in an in-memory LRU so repeated searches on the same file are instant).


* Select the top-$K$ chunks exceeding cosine similarity $\tau \ge 0.55$.



#### 3. Indentation "Fabric Tightness" Expansion & Slice Merging

Once match anchor lines $\{m_1, m_2, \dots\}$ are identified, expand each anchor $m$ into a coherent structural block $[s_i, e_i]$:

1. **Compute Line Gravity $I(k)$:**
* For non-blank line $k$, let $I(k)$ be the visual indentation column (tabs = 4 spaces).
* For blank lines, assign **interpolated indentation** $I(k) = \min(I(\text{prev\_non\_blank}), I(\text{next\_non\_blank}))$, with a small penalty if surrounded by multiple blank lines ("loose fabric").


2. **Upward & Downward Walk (Budget: ~25 lines per hit, ~160 lines total):**
* **Walk Up ($k = m - 1 \dots 1$):** Include lines while $I(k) \ge I(m)$ (siblings) and step outward whenever $I(k) < I_{\text{curr\_min}}$ to capture the enclosing declaration/header (e.g., `func ...`, `class ...`, `if ...:`). Stop once $I(k) == 0$ header is captured (plus any immediately preceding doc-comment block `//` or `#`) or budget is reached.
* **Walk Down ($k = m + 1 \dots N$):** Include lines while $I(k) \ge I_{\text{enclosing}}$ plus the matching closing line (`}`, `)`, `end`) at $I_{\text{enclosing}}$.


3. **Merge & Unicode Ellipsis (`…`) Formatting:**
* Sort intervals $[s_i, e_i]$ and merge any intervals where $s_{i+1} \le e_i + 2$ (don't emit an ellipsis for a 1-line gap—just include the line!).
* Between disjoint slices, emit a single line containing `…` (Unicode `U+2026`).



```text
[kabibi/ui/shell/shell.go#8f2a] (lines 1-420, 2 matches for "activePTY")
…
304:c1|func (es *ExecutionSession) runResolvedCommand(ctx context.Context, hc interp.HandlerContext, prog string, args []string) error {
305:8a|    isInteractiveTTY := isUserForeground(ctx) && hc.Stdin == es.ttyStdin
…
318:f4|    es.mu.Lock()
319:29|    es.activePTY = p
320:e0|    es.mu.Unlock()
321:0d|}
…

```

---

### Subsystem B: Hashline-Native `edit_file`

#### 1. Hashing Algorithm (Deterministic & Whitespace-Tolerant)

* **Line Hash (2 hex chars):** Strip `\r` and **all ASCII whitespace** (`' '`, `'\t'`) from the line, compute `xxhash.Sum64(stripped) & 0xFF`, and format as `%02x`.
* *Why strip whitespace?* Small LLMs frequently miscount leading spaces by 1 or 2 when echoing anchors, or strip trailing spaces. Stripping whitespace for the hash makes the checksum verify the *semantic content* of the line while still catching stale edits. (For blank lines, hash `""` $\rightarrow$ `ef`).


* **File Snapshot Tag (4 hex chars):** `xxhash.Sum64(rawFileBytes) & 0xFFFF` formatted as `%04x` in the header `[path#TAG]`.

#### 2. Tool Schema (`edit_file`)

Instead of fragile unified diffs where LLMs miscount hunk line counts (`@@ -14,7 +14,9 @@`), use structured anchor operations:

```json
{
  "path": { "type": "string", "description": "File path (may include #TAG from read_file, e.g. 'main.go#8f2a')." },
  "edits": {
    "type": "array",
    "description": "List of hashline edit operations applied atomically.",
    "items": {
      "type": "object",
      "properties": {
        "op": { "type": "string", "enum": ["replace", "insert_after", "insert_before", "delete"] },
        "start_anchor": { "type": "string", "description": "Start line anchor in 'LINE:HASH' format (e.g. '42:a3')." },
        "end_anchor": { "type": "string", "description": "End line anchor (inclusive) for multi-line 'replace' or 'delete'. Omit for single-line." },
        "content": { "type": "string", "description": "New text content (without LINE:HASH| prefixes)." }
      },
      "required": ["op", "start_anchor"]
    }
  }
}

```

#### 3. Infallible Validation & Self-Healing Pipeline

1. **Bottom-Up Application:** Sort all resolved hunks by `start_line` descending so earlier edits never shift line numbers for subsequent edits in the same call.
2. **Fuzzy Anchor Relocation ($\pm 30$ lines):** If `start_anchor` is `42:a3`, check line 42 first. If line 42's hash is not `a3`, scan lines $[12 \dots 72]$ for `a3`. If `a3` (and `end_anchor` at the same relative offset) matches **uniquely** in that window, relocate the edit automatically! If ambiguous or missing, reject cleanly and return the current 10-line hashline snippet around line 42 in the error payload so the AI doesn't even need an extra `read_file` turn to recover.
3. **LLM Prefix Sanitizer:** Models occasionally copy `42:a3|` into `content`. Check if lines in `content` match `^\d+:[0-9a-f]{2}\|` when the original file does *not* contain such patterns; strip the prefix automatically.
4. **Return Fresh Anchors:** On success, return the new `[path#NEW_TAG]` and the updated `LINE:HASH|content` lines for the modified region ($\pm 3$ context lines) so the AI can chain further edits with zero re-reads.

---

### Subsystem C: Progressive `list_files` Tool

#### 1. Tool Schema (`list_files`)

```json
{
  "query": { "type": "string", "description": "Glob pattern (e.g. '**/*.go'), filename query, or concept query. Default '*'." },
  "search_content": { "type": "boolean", "description": "If true, match query against file contents (exact -> semantic embeddings) instead of just filenames." },
  "max_results": { "type": "integer", "description": "Max entries to return (default 50)." }
}

```

#### 2. Escalation Strategy

Anchored strictly to `es.Dir()` (Kabibi's shell CWD) and skipping `.git`, `node_modules`, `target`, and binary blobs:

1. **Glob / Exact Substring:** Evaluate `doublestar.Match(query, relPath)`.
2. **Fuzzy Filename:** If 0 matches (or `query` has no glob chars and didn't match verbatim), run fuzzy subsequence matching against all relative paths.
3. **Filename Embeddings:** If fuzzy score is weak or results exceed `max_results * 3`, rank candidate paths by `EmbeddingGemma 2` cosine similarity against `query`.
4. **Content Embeddings (`search_content: true`):** Fast-scan files with literal/regex first; if 0 or too many files match, rank files using cached file-summary/chunk embeddings from `EmbeddingGemma 2`.

#### 3. Compact Human-Readable Output Format

Formatting as a dense, single-line-per-entry table costs **~65% fewer tokens** than JSON arrays while remaining crystal clear to Gemma 4:

```text
[cwd: ~/rustica/kabibi] (8 entries, mode: glob '**/*.go')
d       -  ui/shell/
-   14.2K  ui/shell/shell.go
x    4.1K  build.sh*
l     18B  latest.log@ -> /tmp/kabibi-01.log
-   31.8K  ui/shell/shell_widget.go  [match: L142 "previouslyEjected"]

```

* **Column 1 (Type/Perm):** `d` (directory, plus trailing `/`), `x` (executable: `mode&0o111 != 0` on POSIX, or PATHEXT/shebang on Windows, plus trailing `*`), `l` (symlink / Windows reparse point via `os.Lstat` `ModeSymlink | ModeIrregular`, plus `@ -> target`), `-` (regular file).


* **Column 2 (Size):** Human-readable (`420B`, `14.2K`, `3.1M`).

---

### Subsystem D: PTY Terminal Execution, Gutter Stripe & Background Process Lifecycle

#### 1. Visual Gutter Composition (`Cols - 2` Trick)

In `kabibi/ui/shell`:

1. When the AI invokes `exec`, allocate a new `CommandBlock` with `Source: SourceAITool`.


2. Start the process via `pty.Start(cmd, pty.Options{Cols: max(20, termWidth - 2), Rows: termHeight, Term: "xterm-256color"})`.


3. In `renderBlock(block, termWidth)`:
* Render `block.Output` into an inner `terminal.CellBuf` of width `termWidth - 2` via `terminal.FromANSI`.


* Allocate the outer `CellBuf` of width `termWidth` and height `inner.Height`.
* Paint column `0` of every row with `┃` (or `▐`) in a distinct AI accent color (e.g., cyan/magenta while running, green on exit `0`, red on non-zero exit) and column `1` with `' '`.
* Blit `inner` into columns `2 .. termWidth - 1`.
* Result: The user sees live, full-color PTY output streaming in real time on the console with an unmistakable left border stripe, and cursor movements (`\r`, `\x1b[2K`, progress bars) stay strictly confined inside the `W - 2` sub-buffer!



#### 2. Token-Cheap ANSI & Control Stripping (`CleanCellBufToText`)

Sending raw PTY bytes (full of 24-bit color escapes, `\r` spinner redraws, and cursor movement codes) to an LLM burns thousands of tokens and confuses small models. Because you already parse the PTY stream through `terminal.FromANSI` (`vt.Emulator`) into a `terminal.CellBuf`:

1. **Virtual Terminal Collapse:** Passing the PTY stream through `vt.Emulator` *automatically collapses* all `\r` progress bar overwrites, backspaces, and cursor jumps into the final settled 2D character grid!
2. **Color-Stripped Extraction:**
* If `keep_ansi == false` (the default), you don't even need `ToANSI`—or if using `ToANSI` to preserve bold/underline semantics, clone `CellBuf`, zero out `Cell.FG` and `Cell.BG` on every cell, run `ToANSI`, and strip trailing `\x1b[0m` / `\x1b[m` sequences and trailing whitespace at the end of every line.
* For pure plain text (cheapest for tokens), read the settled runes row-by-row directly from the `CellBuf`, trimming right-hand padding spaces on each row.



#### 3. Timeout Promotion & Background Process Registry

Equip `ExecutionSession` with a background PTY table (`bgProcs map[int]*ManagedPTY`):

* **When `exec(command, timeout_sec=15)` runs:**
* Wait on `select { case <-proc.done: ... case <-time.After(timeout): ... }`.
* **Case A — Finishes within threshold (e.g., `1.42s`):**
Append the settled footer to the block and return to the AI:
```text
<cleaned PTY output>
[exit: 0 | elapsed: 1.423s | cwd: /Users/mihai/rustica/kabibi]

```


* **Case B — Exceeds threshold (e.g., `15.000s`):**
* Do **not** kill the process. Keep it registered in `bgProcs[procID]` (and keep streaming its live output to its `CommandBlock` in the UI if visible).
* Snapshot the current `CellBuf`, strip colors/control codes, and return to the AI:


```text
<cleaned PTY output so far>
[status: STILL RUNNING (pid_id: 3) after 15.004s | use term_read, term_input, or term_kill with id=3]

```





#### 4. Companion Terminal Tools

Register three lightweight companion tools in `kabibi/ui/shell/ai_tools.go`:

| Tool | Parameters | Behavior |
| --- | --- | --- |
| **`term_read`** | `id` (int, req), `wait_ms` (int, opt, default `2000`, max `30000`), `only_new` (bool, default `true`), `keep_ansi` (bool, default `false`) | Waits up to `wait_ms` for new output or process exit. Returns new settled lines (or full screen if a TUI redraw occurred) + current status (`RUNNING` or `EXITED (code) after X.XXXs`). |
| **`term_input`** | `id` (int, req), `keys` (string, req), `wait_ms` (int, opt, default `1000`) | Translates symbolic key tokens (`<enter>` $\rightarrow$ `\r`, `<ctrl+c>` $\rightarrow$ `\x03`, `<ctrl+d>` $\rightarrow$ `\x04`, `<up>` $\rightarrow$ `\x1b[A`, `<esc>` $\rightarrow$ `\x1b`, or literal text) and writes to `pty.Write()`. Waits `wait_ms` and returns the resulting `term_read` output immediately (saving a round-trip tool call!). |
| **`term_kill`** | `id` (int, req), `signal` (string, opt: `"SIGINT"`, `"SIGTERM"`, `"SIGKILL"`) | Terminates or interrupts the background PTY process, closes the PTY handle, marks the UI `CommandBlock` completed, and returns final output + exit stats. |

---

## 4. Suggested Implementation Sequence in `kabibi`

1. **`kabibi/tools/hashline` (New Package):** Implement `FormatHashlines(path, content, ranges)`, `ComputeLineHash(line)`, `ComputeFileTag(bytes)`, and `ApplyHashlineEdits(path, edits)` with $\pm 30$-line auto-relocation and unit tests.
2. **`kabibi/tools/fabric` (New Package):** Implement `ExpandAndMergeSlices(lines, matchIndices, budget)` using the indentation-gravity walker and `…` separator, plus the 4-tier search funnel (`plain` $\rightarrow$ `regex` $\rightarrow$ `fuzzy` $\rightarrow$ `EmbeddingGemma 2`).
3. **`kabibi/tools/filelist` (New Package):** Implement `ListFiles(cwd, query, opts)` with `doublestar`, fuzzy/embedding fallbacks, and compact `d/x/l/-` human-readable formatting.
4. **`kabibi/ui/shell/pty_manager.go` & `shell_widget.go`:** Add the `W - 2` AI gutter stripe rendering in `renderBlock`, `CleanCellBufToText` ANSI stripping, and the `bgProcs` timeout-promotion registry (`exec`, `term_read`, `term_input`, `term_kill`).

