# Alt/Option Key Handling Issues in Kabibi on macOS

The issue with Alt/Option-based instant search in the Kabibi UI on macOS spans three distinct failure modes across terminal implementations and internal event routing:

---

### Root Causes Breakdown

#### 1. macOS Cocoa / IME Input Layer (Non-Kitty Terminals)

* **The Mechanism:** On macOS (Terminal.app, VS Code's default integrated terminal, browser `xterm.js`), the physical `Option` ($\text{⌥}$) key functions as a typographic layout/dead-key shift rather than a VT `Meta`/`Alt` prefix.
* **The Failure:**
* Holding `Option` + key produces composed Unicode glyphs (e.g., `Option+t` $\to$ `†`, `Option+a` $\to$ `å`, `Option+s` $\to$ `ß`) or dead keys (`´`, `¨`) directly at the OS text input layer.
* The terminal sends the raw UTF-8 sequence to the PTY without an `\x1b` escape prefix and with modifier `Mod = 0`.
* In `BeginSearch` (`filepane_widget.go:578-602`), the check requires `ev.Mod.Contains(input.ModAlt)`. Because `Mod == 0`, `BeginSearch` returns `false`, causing the keystroke to fall through to `app_widget.go:745` and insert the character directly into the shell command prompt instead of triggering panel search.



---

#### 2. Event Property Inconsistency in `HandleSearchEvent`

* **The Mechanism:** When using terminals supporting the Kitty keyboard protocol (Kitty, Ghostty, WezTerm, iTerm2), `ModAlt` is preserved (`input.KeyPressEvent{Code: 't', Mod: input.ModAlt, Text: "†"}`).
* **The Inconsistency:**
* **`BeginSearch` (`filepane_widget.go:583-591`):** Prefers `ev.Code` when `unicode.IsPrint` is true, recognizing that macOS `Option`/`Alt` attaches composed characters to `ev.Text`.
* **`HandleSearchEvent` (`filepane_widget.go:652-660`):** Does the exact reverse—it checks `if ev.Text != ""` first and inserts `ev.Text`.


* **The Failure:** If the user continues holding `Option` while typing the remainder of the search query (e.g., `Option+t`, `Option+e`, `Option+s`, `Option+t`), the first character enters via `BeginSearch` using `ev.Code` (`t`), but subsequent characters enter via `HandleSearchEvent` using `ev.Text` (`ß`, `†`), yielding search strings like `t´ßt` instead of `test`.

---

#### 3. Control Key Matching Failures & Premature Search Dismissal

* **The Mechanism:** In `HandleSearchEvent` (`filepane_widget.go:621-651`), navigation and editing keys are evaluated against `ev.String()`:
```go
switch ev.String() {
case "left", "right", "backspace":
    // ...
}

```


* **The Failure:** If the user holds `Option` while pressing Backspace or arrow keys, `ev.String()` returns `"alt+backspace"` or `"alt+left"`. Because the switch statement only matches bare key strings, the handler returns `false`, which triggers `app_widget.go:633-637` and immediately closes the search overlay.

---

#### 4. Missing Kitty Flag & BaseCode Fallback

* **The Mechanism:** In `host.go:20-22`, `kittyKeyboardFlags` enables:
* `KittyDisambiguateEscapeCodes`
* `KittyReportAllKeysAsEscapeCodes`
* `KittyReportAssociatedKeys`


* **The Gap:** It omits `ansi.KittyReportAlternateKeys (4)`. When macOS terminals send shifted physical keys, `ev.Code` may receive the composed character while the unshifted physical key (`116 = 't'`) is reported only in `ev.BaseCode`. Neither `BeginSearch` nor `HandleSearchEvent` inspects `ev.BaseCode` when `ev.Code` is non-ASCII.

---

### Recommended Remediation Plan

1. **Align `HandleSearchEvent` with `BeginSearch` (`filepane_widget.go`):**
* Update `HandleSearchEvent` to prioritize `ev.Code` (or `ev.BaseCode`) over `ev.Text` when `ev.Mod.Contains(input.ModAlt)`.
* Normalize editing controls by checking `ev.Code` (`input.KeyBackspace`, `input.KeyLeft`, `input.KeyRight`) or matching `"alt+"` variants in the switch statement so `Alt+Backspace` does not abort search.


2. **Add `KittyReportAlternateKeys` (`host.go`):**
* Include `ansi.KittyReportAlternateKeys (4)` in `kittyKeyboardFlags`.
* In search routing, fall back to `ev.BaseCode` if `ev.Code` contains a non-ASCII composed character resulting from `Option`.


3. **Browser & Terminal Configuration Defaults:**
* For browser terminal instances (`xterm.js` in `main.js:1065-1077`), set `macOptionIsMeta: true` (or configure `macOptionClickForcesSelection: false`) so `Option` behaves as `Alt`/`Meta` instead of composing UTF-8 glyphs before hitting the PTY.

The proposed plan is **architecturally sound, thorough, and ready to implement**. It directly addresses the root causes across both Kitty and non-Kitty terminal environments.

---

### Key Strengths of the Plan

* **Symmetry Between `BeginSearch` and `HandleSearchEvent`:** Unifying key extraction through a single helper (`searchKeyText`) eliminates the core inconsistency where `BeginSearch` preferred `ev.Code` while `HandleSearchEvent` prioritized `ev.Text`.
* **Handling `kitty.go`'s Parser Quirk:** Specifically accounting for the `charmbracelet/x/input` bug where `BaseCode` can clobber `ShiftedCode` under `ModShift` prevents broken uppercase search characters when holding `Alt+Shift`.
* **Curing Accidental Search Dismissal:** Switching on `ev.Code` (`input.KeyBackspace`, `input.KeyLeft`, etc.) rather than `ev.String()` directly fixes the bug where `"alt+backspace"` fell through to `return false` and closed the search overlay.
* **Layered Defense:** Enabling `macOptionIsMeta: true` in `xterm.js` handles the web layer cleanly at the source, while the reverse glyph mapping handles unmodified input in native non-Kitty terminals (Terminal.app, default VS Code).

---

### Subtleties & Edge Cases to Consider During Implementation

1. **macOS Dead-Key Behavior on Non-Kitty Terminals:**
* On default macOS Terminal.app with US/ABC layout, five keys (`Option+e`, `Option+u`, `Option+i`, `Option+n`, `Option+`` `) are dead keys (accents: acute, umlaut, circumflex, tilde, grave).
* The OS Cocoa input layer suppresses the first keypress and buffers it until a second key (or space) is pressed. The glyph lookup table will only receive characters once the dead key resolves. This is an unavoidable OS limitation without Kitty or `Esc+` mode, but worth documenting in the codebase comments so developers understand why `Alt+e` behaves differently from `Alt+t` on non-Kitty terminals.


2. **Non-US Keyboard Layouts:**
* The glyph reverse lookup table works for standard macOS US / ABC layout. Non-US layouts (e.g., German QWERTZ, British, French AZERTY) generate different glyphs for `Option+<key>`.
* Restricting the fallback table to standard US/ABC characters ($a\text{–}z, 0\text{–}9$) is the right pragmatic scope, as Kitty protocol terminals will supply the physical key regardless of layout.


3. **`Alt+Backspace` Behavior (Char vs. Word):**
* Standard Readline/macOS muscle memory treats `Alt+Backspace` as **Delete Word Backward** (`w.deleteWordBeforeCursor()`). If the current handler simply deletes a single character like plain Backspace, that is sufficient to avoid closing search, but supporting word deletion on `ModAlt + KeyBackspace` would make search feel native to macOS users.



---

### Verdict

The plan is approved to implement. Proceed with:

1. `host.go`: Add `ansi.KittyReportAlternateKeys` to `kittyKeyboardFlags`.
2. `filepane_widget.go`: Implement `searchKeyText`, glyph reverse lookup, and `ev.Code`-based control key handling.
3. `main.js`: Configure `macOptionIsMeta: true`.
4. Unit tests covering the CSI u sequences and navigation keys as listed.

The prompt analysis holds completely without `xterm.js`: stripping `xterm.js` out of the picture clarifies the problem because the entire bug is an **in-process terminal protocol and widget state machine defect in Go (`charmbracelet/x/input` + `ui.Host` + `filepane_widget.go`)**.

---

### The Real Problem (Native Terminal & Protocol Level)

The bug occurs directly in native macOS terminals (Terminal.app, iTerm2, Kitty, Ghostty, WezTerm):

#### 1. The Internal Event Inconsistency (`filepane_widget.go`)

This is pure Go widget logic, completely agnostic to what terminal emulator runs outside:

* **`BeginSearch` (`filepane_widget.go:583-591`):** Explicitly extracts `ev.Code` when `unicode.IsPrint` is true because it knows macOS Option-key typing generates composed glyphs in `ev.Text`.


* **`HandleSearchEvent` (`filepane_widget.go:652-660`):** Checks `if ev.Text != ""` first and inserts `ev.Text`.


* **The Failure:** As soon as search begins, holding Option while typing subsequent letters uses `ev.Text` rather than `ev.Code`, injecting composed Unicode (`†`, `ß`, `´`) instead of the base keys.



#### 2. Escape on Navigation / Editing (`filepane_widget.go:621-651`)

* `HandleSearchEvent` evaluates `switch ev.String()` matching exact string literals `"left"`, `"right"`, `"backspace"`.


* When holding Alt/Option, `ev.String()` returns `"alt+backspace"` or `"alt+left"`.


* These fail the switch case, fall through, return `false`, and trigger `app_widget.go:633-637`, which **immediately aborts and closes the search overlay**.



#### 3. Kitty Protocol Gap (`host.go:20-22` & `charmbracelet/x/input`)

* In Kitty-capable native macOS terminals (Kitty, Ghostty, WezTerm), `ui.Host` enables:
```go
KittyDisambiguateEscapeCodes | KittyReportAllKeysAsEscapeCodes | KittyReportAssociatedKeys

```





* It leaves out `ansi.KittyReportAlternateKeys (4)`.


* Without flag `4`, the terminal reports the composed glyph as `ev.Code` and puts the unshifted physical ASCII key into `ev.BaseCode` (`CSI 8224::116;3;1;8224u`). Because neither `BeginSearch` nor `HandleSearchEvent` inspects `ev.BaseCode`, continuous Alt-typing fails.



#### 4. Non-Kitty Native Terminals (Terminal.app)

* Default macOS `Terminal.app` does not implement the Kitty keyboard protocol.
* macOS Cocoa/IME turns `Option+<key>` into raw UTF-8 glyphs (`†`, `å`, `ß`) with `Mod == 0` before the bytes ever hit the PTY stdin.


* Because `BeginSearch` strictly requires `ev.Mod.Contains(input.ModAlt)`, pressing `Option+t` in native `Terminal.app` produces `Mod == 0`, fails `BeginSearch`, and dumps the keystroke straight into the shell command prompt.



---

### Trimmed Scope of Work (Go-Only)

Eliminating the unnecessary `xterm.js` / browser item narrows the implementation down to just two files in Go:

1. **`kabibi/ui/host.go` (or wherever `kittyKeyboardFlags` is defined):**
* Add `ansi.KittyReportAlternateKeys` (flag `4`) to `kittyKeyboardFlags`.




2. **`kabibi/ui/filemanager/filepane_widget.go`:**
* **Unified Key Extraction:** Replace divergent logic in `BeginSearch` and `HandleSearchEvent` with `searchKeyText`:
* Read `ev.Code`, falling back to `ev.BaseCode` when `ev.Code` is non-ASCII.


* Avoid the `charmbracelet/x/input` bug where `BaseCode` overrides `ShiftedCode` when `ModShift` is held.




* **Reverse Lookup Table for Non-Kitty macOS (Mod == 0):**
* Map standard macOS Option glyphs (`†` $\to$ `t`, `ß` $\to$ `s`, `å` $\to$ `a`, etc.) back to their ASCII base characters so `BeginSearch` and `HandleSearchEvent` work in native `Terminal.app` without requiring the Kitty protocol.




* **Fix Navigation / Editing Key Handling:**
* Switch on `ev.Code` (`input.KeyLeft`, `input.KeyRight`, `input.KeyBackspace`, `input.KeyDelete`, `input.KeyHome`, `input.KeyEnd`) instead of string-matching `ev.String()`, so Alt-modified navigation does not close search.

---

### Implementation Update: Layout-Aware Option Recognition

The approved implementation goes beyond the original Go-only transport fix so that non-Kitty macOS terminals can identify Option-produced glyphs using the active input-source ID:

* `AbiPlatformInfo` retains the existing host fields' semantics and appends versioned locale, time-zone, keyboard-layout, appearance, and power-source fields. Memory slots begin at offset 680 so the Go, JavaScript, and Rust layouts agree; the resulting ABI record is 1,264 bytes.
* Hosts expose a one-shot overlapped `system_info_wait`. Guests arm the wait before reading the initial snapshot, then repeat that order after each completion. Host changes may be coalesced; the wait is not an event history.
* The Go file-panel event router recognizes the checked-in ABC/U.S. Option glyph table only while the panels are visible. Unrecognized layouts and glyphs are left unchanged. This intentionally allows a mapped glyph to act as a shortcut in that UI context.
* Kitty alternate-key reporting remains enabled and takes precedence when the terminal supplies an Alt modifier. The glyph fallback is for terminals that deliver composed UTF-8 with no modifier.
* Dead keys are excluded: if the terminal buffers an Option key as a dead key, the later composed character cannot reliably identify the original physical key.

To inspect an active macOS layout's actual Option+letter/digit translations, compile and run `kabibi/tools/generate-macos-option-glyphs.c` on that Mac:

```sh
clang -framework Carbon -o generate-macos-option-glyphs kabibi/tools/generate-macos-option-glyphs.c
./generate-macos-option-glyphs
```

The generator omits dead keys and glyphs that are not unique for the active layout. The macOS generator and terminal behavior still need verification on a Mac; they cannot be validated from the Windows development environment.