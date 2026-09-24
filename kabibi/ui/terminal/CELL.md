Confirm that you understand how this specification applies to the existing ToANSI code.

DO NOT make any changes
DO explain how the spec needs to be applied
DO NOT produce code snippets, explain ONLY verbally

# Technical Specification: `CellBuf` Terminal Engine (`FromANSI` & `ToANSI`)

This specification codifies the complete bidirectional serialization and state-ingestion pipeline for `CellBuf`. The primary architecture maintains a 2D rectangular visual state representation of terminal screens, producing minimal ANSI escape streams with exact visual idempotency.

---

## 1. Architectural Model & Guarantees

* **Canonical Visual Grid**: `CellBuf` represents a bounded $W \times H$ array of packed `Cell` structures (`Rune` + 64-bit `Style`), capturing the explicit rendered appearance rather than a raw text log.


* **Minimal ANSI Serialization**: `ToANSI` outputs the shortest valid ANSI sequence capable of reconstructing the visual grid.


* **Visual Idempotency**: `ToANSI` guarantees that rendering over a dirty or pre-existing terminal line will wipe stale trailing characters and active style artifacts without emitting redundant whitespace bytes.

---

## 2. Ingestion Pipeline Specification (`FromANSI`)

`FromANSI` converts raw ANSI streams into a single flat `CellBuf` allocation without post-hoc slice trimming or intermediate `uv.Line` heap allocations.

### 2.1 Spatial Bounds & Height Determination

Grid height $H$ is calculated upfront before allocation:

* **Scrollback Case**: Query emulator scrollback count $S$. If $S > 0$, total height is strictly set to $H = S + 24$. Viewport scanning is bypassed.


* **Viewport Scan Case**: If $S = 0$, scan the emulator's `Touched()` row array in reverse from index 23 down to 0. Find the highest row index $Y_{\text{max}}$ containing rendered content.


* If no rows were touched, $H = 0$.


* Otherwise, $H = Y_{\text{max}} + 1$.





### 2.2 Allocation & Direct Streaming Populate

1. Allocate the backing cells slice once: `make([]Cell, width * H)`.


2. Loop row $y$ from $0$ to $H - 1$:
* Source line resolution: $y < S$ reads scrollback line $y$; $y \ge S$ reads active viewport row $(y - S)$.




3. Direct write into flat index `idx = y * width + x`:
* Map foreground/background RGB and text attributes (Bold, Dim, Italic, Underline, Blink) bitwise into the 64-bit `Style` mask.


* Set default reset flags (`CharResetForegroundDefault`, `CharResetBackgroundDefault`) when explicit colors are omitted.





### 2.3 Double-Wide Character Ingestion

* Full-width runes (Unicode column width = 2) occupy two physical cell positions in the grid.
* The primary cell at column $x$ receives the full-width rune and packed `Style`.
* The continuation cell at column $x+1$ is populated with a zero-value rune (`0`) and carries an internal continuation attribute (`CharWideContinuation`) while matching the primary cell's `Style`.

### 2.4 Soft-Wrap Classification & Persistence Pipeline

During ingestion, `FromANSI` evaluates row boundaries to infer whether row $y$ wrapped implicitly into row $y+1$. If affirmed, `CharSoftWrap` is set on the cell at index `y * width + (width - 1)`.

#### Character Classes

* Inline Whitespace ($\mathcal{S}_{inline}$): `' '`, `0`, or non-printable inline whitespace (excluding `\r`, `\n`).


* Text Fusion Set ($\mathcal{C}_{text}$): Letters ($\mathcal{L}$), Numbers ($\mathcal{N}$), Emoji symbols/modifiers ($\mathcal{S}_o, \mathcal{S}_k$ excluding Box Drawing), and Paragraph Punctuation $\mathcal{P}_{para} = \{ \text{`,`}, \text{`-`}, \text{`–`}, \text{`—`}, \text{`"`}, \text{`'`}, \text{`(`}, \text{`)`}, \text{`[`}, \text{`]`}, \text{`:`}, \text{`;`}, \text{`/`}, \text{`\`} \}$.



#### Exclusion Pipeline (Hard-Break Mandates)

Evaluated in sequence; if **any** match, `CharSoftWrap` is set to `false`:

1. **Box-Drawing & UI Artifacts**: Boundary cells contain Unicode Box Drawing (`U+2500`–`U+257F`) or Block Elements (`U+2580`–`U+259F`).


2. **Bullet-List Prefixes**: $Row_{N+1}$starts with$S_0 \in \{ \text{`-`}, \text{`*`}, \text{`•`}, \text{`+`}, \text{`–`}, \text{`—`} \}$, $S_1 \in \mathcal{S}_{inline}$, $S_2 \notin \mathcal{S}_{inline}$.


3. **Line Dividers**: $Row_{N+1}$ starts with $\ge 3$ repeated divider symbols (e.g., `***`, `---`, `===`).


4. **Indentation / Blank Boundaries**: $Row_{N+1}$ begins with multi-space indentation or is completely empty.


5. **Sentence-Ending Full Stop**: $Row_N$ ends with `.`, **UNLESS** flanked by digits ($E_1 \in \text{Digits} \land S_0 \in \text{Digits}$, e.g., `3.` $\rightarrow$ `1415`).



#### Inclusion Pipeline (Soft-Wrap Affirmations)

If no exclusion triggered, `CharSoftWrap` is set to `true` if any rule matches:

* **Rule P1**: $E_0 \in \mathcal{C}_{text} \land S_0 \in \mathcal{C}_{text}$.


* **Rule P2a**: $E_1 \in \mathcal{C}_{text} \land E_0 \in \mathcal{S}_{inline} \land S_0 \in \mathcal{C}_{text}$.


* **Rule P2b**: $E_0 \in \mathcal{C}_{text} \land S_0 \in \mathcal{S}_{inline} \land S_1 \in \mathcal{C}_{text}$.



---

## 3. Serialization Pipeline Specification (`ToANSI`)

`ToANSI` performs a single-pass forward sweep across each row $y \in [0, H-1]$ using a `pendingSpaces` integer accumulator.

```
                     [ Start Row y ]
                            │
                            ▼
               [ Initialize pendingSpaces = 0 ]
                            │
                            ▼
              [ Loop x from 0 to Width - 1 ]
                            │
            ┌───────────────┴───────────────┐
            ▼                               ▼
 [ Candidate Space Cell ]        [ Necessary Token / Cell ]
  - Rune is ' ' or 0              - Rune is non-space OR
  - Style matches prev cell       - Style transition occurs
            │                               │
            ▼                               ▼
   [ pendingSpaces++ ]           [ Flush pendingSpaces using ]
                                 [ active style context     ]
                                            │
                                            ▼
                                 [ Emit SGR & Rune Byte(s)  ]
                                            │
                            ┌───────────────┘
                            ▼
               [ Loop Finished x == Width ]
                            │
            ┌───────────────┴───────────────┐
            ▼                               ▼
  [ CharSoftWrap == true ]        [ CharSoftWrap == false ]
  - Flush pendingSpaces using     - Discard pendingSpaces
    active style context          - Emit \x1b[K (Clear Line)
  - Suppress \r\n                 - Emit \r\n

```

### 3.1 Inner Sweep Mechanics

1. **Candidate Space Condition**: A cell at $(x, y)$ is deferred (`pendingSpaces++`) if:
* Rune $r \in \{ \text{` '`}, 0 \}$
* Style $s$ matches the style context of the preceding cell (maintains style continuity without introducing visual transitions).


2. **Double-Wide Continuation Handling**:
* Cells flagged with `CharWideContinuation` are skipped during rune emission to prevent double printing.


3. **Necessary Token Encounter**:
* If `pendingSpaces > 0`, flush those spaces first:
* Emit `pendingSpaces` literal space characters (`' '`), **copying and preserving the active style context** of the preceding text.
* Reset `pendingSpaces = 0`.


* Compare cell style $s$ with `currentStyle` state machine. Emit minimal delta SGR sequences (`\x1b[...]`).
* Emit the rune UTF-8 byte(s). Update `currentStyle = s`.



### 3.2 Row Termination & Idempotency Rules

Upon completing column $x = W - 1$, inspect `pendingSpaces` and the persisted `CharSoftWrap` attribute of the last cell:

| Row End Condition | `pendingSpaces` Action | Line-Clear Emission (`\x1b[K`) | Row Terminator |
| --- | --- | --- | --- |
| **`CharSoftWrap == true`** | Flush `pendingSpaces` as literal spaces under active style context

 | Omitted | Suppressed (No `\r\n`)

 |
| **`CharSoftWrap == false`** (`pendingSpaces > 0`) | **Discard/Omit** `pendingSpaces` completely

 | **Emitted** (`\x1b[K`) | Emitted (`\r\n`)

 |
| **`CharSoftWrap == false`** (`pendingSpaces == 0`, text at edge) | N/A | **Emitted** (`\x1b[K`) if style reset needed | Emitted (`\r\n`)

 |
| **Empty Row** | Discard | Emitted (`\x1b[K`) | Emitted (`\r\n`)

 |

#### Rationale for `\x1b[K`

Emitting `\x1b[K` (Erase from Cursor to End of Line) when trimming trailing spaces guarantees:

1. Pre-existing characters from earlier draws on the terminal screen are wiped without printing physical spaces.
2. Lingering background colors or attributes active in the terminal parser are cut off before the cursor drops to the next line.

---

## 4. Verification & Assertion Requirements

### 4.1 Ingestion Tests (`FromANSI`)

* **Single Allocation**: Assert heap allocations during `FromANSI` equal exactly $1$.
* **Exclusion Pipeline**:
* Verify `End of line.` followed by `Next sentence` sets `CharSoftWrap = false` (Period rule).


* Verify `Value: 3.` followed by `1415` sets `CharSoftWrap = true` (Flanked decimal exception).


* Verify `Item 1` followed by `- Item 2` sets `CharSoftWrap = false` (Bullet prefix exclusion).


* Verify `Top border` containing `\u2500` sets `CharSoftWrap = false` (Box drawing exclusion).





### 4.2 Serialization Tests (`ToANSI`)

* **Style Continuity Spanning Spaces**: Assert that `"A   B"` with a red background emits red spaces without resetting to default background mid-span.
* **Trailing Space Trimming with `\x1b[K**`: Assert `ToANSI` on `"Text   "` at $W=10$ outputs `"\x1b[...]Text\x1b[K\r\n"`.
* **Soft-Wrap Flush**: Assert `ToANSI` on a row ending in 2 spaces with `CharSoftWrap = true` outputs `"Text  "` with **no** `\x1b[K]` and **no** `\r\n`.


* **Double-Wide Alignment**: Assert full-width CJK characters preserve 2-column spacing without emitting extra continuation runes or corrupting `pendingSpaces`.