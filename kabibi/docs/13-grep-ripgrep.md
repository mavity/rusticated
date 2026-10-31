# grep and ripgrep Compatibility Plan

## Goal

Bring Kabibi's in-process `grep` and new `rg` command to at least 99% coverage
of practical command-line search scenarios on normal Linux and macOS systems.
Use GNU grep on Linux, the system BSD grep on macOS, and upstream ripgrep as
behavioral references. Implement the commands in Go under `u-root/cmds/core`,
with no runtime dependency on host utilities.

Current implementation status:

- `grep` is registered in Kabibi and supports shared BRE/ERE/fixed/Perl matching, common selection and output flags, streaming long records, recursive selection, context, NUL-delimited records, and per-file counts. Binary-file detection plus `-a`/`-I` and `--binary-files` modes are implemented; locale semantics and several GNU/BSD edge cases remain incomplete. Binary preflight on non-seekable input buffers the stream.
- `rg` is registered in Kabibi and supports shared regex matching, recursive traversal, basic `.gitignore`/`.ignore`/`.rgignore` rules, glob and common type filters, context and count/list output, JSON events, and NUL records. Search-zip decompression, multiline (`-U`) search, basic stats, case-insensitive globs, global Git excludes, `.git/info/exclude`, and leading PCRE lookaround assertions have been added. Full ignore/JSON/statistics parity and several ripgrep output and filtering options remain incomplete.
- `sed`'s `e` and `s///e` now use Kabibi's injected in-process shell runner rather than starting a host shell; broad environment, redirection, and GNU behavior comparisons remain outstanding.
- The requested 99% practical-compatibility target has not been demonstrated.
  A small differential corpus now passes against GNU grep 3.11, GNU sed 4.9,
  and ripgrep 15.0.0 on Windows/ARM64 (GNU references through WSL). This is
  only a baseline, not evidence of 99% parity; macOS BSD grep, Linux, and
  Rusticated runtime comparisons remain outstanding.

Keep both commands reentrant through `Main(ctx, ioEnv, args...)`; use the
provided streams and `ioEnv.ResolvePath`, honor cancellation, and preserve the
repository's Rusticated Go async-I/O and custom-sysroot build model.

## Numbered implementation plan

### 1. Define the compatibility matrix and test oracle

- Turn the requirements below into a table of command, arguments, input
  fixtures, stdout, stderr, and exit status.
- Run the same common `grep` cases against GNU grep on Linux and BSD grep on
  macOS; record platform-specific output only where the documented utilities
  actually differ.
- Use the upstream ripgrep CLI and user guide as the `rg` reference.
- Compare behavior, not implementation source. Pin reference versions in
  differential-test results so changes in host utilities do not silently
  change expectations.
- Require 100% success for the core scenarios in this plan and at least 99%
  parity across the complete practical-case corpus before calling the work
  complete. No skipped case counts as a pass.

### 2. Establish shared command parsing and status behavior

- Replace ad-hoc option handling with a parser that supports short-option
  clusters, long options, attached option values, repeated options, `--`, and
  patterns beginning with `-`.
- Implement `-e` and `-f` as repeatable pattern sources. Preserve multiple
  patterns as independent alternatives; an empty pattern file contains no
  patterns, and `-f -` reads patterns from standard input.
- Handle no file operands and `-` as standard input. For `rg`, default an
  omitted path to the current directory.
- Return status `0` for a match, `1` for no match, and `2` for usage, pattern,
  traversal, or I/O errors; preserve quiet-mode match status and map context
  cancellation to the repository's cancellation status.
- Print diagnostics to stderr, keep stdout machine-readable when selected, and
  treat a closed downstream pipe as normal early termination.

### 3. Make regex dialects explicit and shared

- Extract a reusable byte-oriented matcher from the existing `regast` work;
  keep sed's parser and execution engine separate from search command logic.
- Support grep's default BRE, `-E` ERE, and `-F` fixed strings. Implement
  `-G`/`-E` mode selection, case folding, word matching, whole-line matching,
  and multiple-pattern matching without translating one dialect as another.
- Support the practical PCRE mode used by GNU grep and ripgrep (`-P`) with
  common look-around and backreference patterns. Do not silently fall back to
  another dialect when a requested mode is unsupported or invalid.
- Give `rg` its documented default regex syntax, Unicode behavior, and
  `-F`/`-i`/`-s`/`-w`/`-x` controls. Add `-U` multiline matching and `-P`
  PCRE mode.
- Honor `LC_ALL`, `LC_CTYPE`, and `LC_COLLATE` for grep's `C`/`POSIX` and
  UTF-8 locale behavior, including character classes, ranges, and case
  matching. Keep ripgrep's Unicode regex behavior independent of the process
  locale.
- Test anchors, alternation, bracket expressions, escapes, word boundaries,
  Unicode case folding, invalid UTF-8 bytes, zero-width matches, and captures.
  Keep match and capture offsets in the original byte stream.

### 4. Complete practical GNU/BSD grep behavior

- Matching controls: `-i`, `-v`, `-w`, `-x`, `-G`, `-E`, `-F`, `-P`, `-e`,
  `-f`, and repeatable patterns.
- Output controls: `-c`, `-l`, `-L`, `-q`, `-s`, `-o`, `-n`, `-b`, `-H`,
  `-h`, `-m`, `-A`, `-B`, `-C`, `--color`, `--line-buffered`, and
  `--null`/`-Z`.
- File selection: `-r`/`-R`, `--include`, `--exclude`, and `--exclude-dir`.
  Make directory traversal, symlink following, displayed paths, and per-file
  output consistent with the selected mode.
- Binary and record handling: support binary detection, `-a`/`--text`,
  `-I`/`--binary-files=without-match`, GNU grep's `-z` NUL-delimited input,
  `-Z` NUL-delimited filenames, and line records without a final newline.
- Implement GNU extensions that are part of common Linux usage while retaining
  the common POSIX short-option behavior expected on macOS.

### 5. Replace line scanning with byte-safe streaming

- Remove the default `bufio.Scanner` token-size limit. Search arbitrarily long
  lines without loading whole files into memory.
- Preserve bytes in output and compute line numbers and byte offsets from the
  original input. Never stringify malformed input merely to match or print it.
- Correctly handle empty input, empty files, CRLF, embedded NUL, invalid UTF-8,
  long lines, missing final newline, and streams that fail mid-read.
- Count selected lines per file; implement `-o` as non-overlapping matching
  occurrences and handle zero-width matches without looping.
- Merge overlapping context ranges, mark context lines correctly, and ensure
  filename, line-number, byte-offset, and NUL separators compose correctly.

### 6. Implement ripgrep's recursive search and filtering

- Add `u-root/cmds/core/rg` and a reentrant `Main`; search files and directories
  without shelling out to `find`, `grep`, Git, or another host command.
- Default to recursive search of the current directory or requested paths.
- Implement `.gitignore`, `.ignore`, and `.rgignore` rules with directory
  scoping and negation. Apply `.rgignore` over `.ignore` over `.gitignore`;
  include `.git/info/exclude` and Git's configured global excludes file.
- By default skip hidden paths, ignored paths, NUL-detected binary files, and
  symlink targets. Add `--hidden`, `--no-ignore`, `--text`/`-a`, and
  `--follow`/`-L`. Repeated `-u` disables ignore rules, then includes hidden
  paths, then searches binary files (`-u`, `-uu`, `-uuu`).
- Implement manual filtering with `-g`/`--glob`, case-insensitive globs,
  `-t`/`--type`, `-T`/`--type-not`, `--type-add`, and type listing. Add
  practical traversal limits such as `--maxdepth` and `--max-filesize`.
- Define deterministic, relative path rendering and correct behavior for
  explicit files, directories, stdin, unreadable paths, loops, and broken
  symlinks.

### 7. Complete ripgrep's everyday CLI and output

- Implement `-n`, `--column`, `-H`/`-h`, `-l`/`--files-with-matches`,
  `--files-without-match`, `-c`, `--count-matches`, `-o`, `-q`, `-m`,
  `-A`/`-B`/`-C`, and `--context-separator`.
- Implement `--files`, `--no-heading`, `--null`, `--sort`, `--stats`, and
  `--json`.
- Implement `--null-data`/`-0` for NUL-delimited input records.
- Match ripgrep's filename-prefix defaults for stdin, one file, multiple files,
  and recursive paths. Support `--color=auto|always|never`, `NO_COLOR`, and
  terminal-aware output without injecting color into redirected data.
- Support `-z`/`--search-zip` for gzip, bzip2, xz, lz4, lzma, and zstd inputs
  using async-compatible Go readers; cover decompression errors explicitly.

### 8. Apply common shell recipes as acceptance tests

Treat these high-frequency workflows as supported behavior, not optional tips:

- Find files containing a literal string: `grep -R -l -F 'text' PATH`.
- Show nearby lines: `grep -C 3 PATTERN FILE`.
- Print only the matching token: `grep -o PATTERN FILE`.
- Search selected extensions or exclude generated/vendor directories using
  include, exclude, and glob options.
- Search ignored and hidden files intentionally with ripgrep's `-u` levels.
- Pass patterns and paths beginning with `-` safely using `-e` and `--`.
- Search many patterns from a file in one pass, rather than launching a new
  process or rescanning the input for every pattern.
- Verify pipelines, quoted metacharacters, filenames containing spaces,
  multiple files, and NUL-delimited filename output.

These scenarios come from common user workflows documented in the
[GNU grep manual](https://www.gnu.org/software/grep/manual/grep.html),
[POSIX grep specification](https://pubs.opengroup.org/onlinepubs/9799919799/utilities/grep.html),
[ripgrep user guide](https://github.com/BurntSushi/ripgrep/blob/master/GUIDE.md),
and high-traffic Stack Overflow discussions about
[context lines](https://stackoverflow.com/questions/9081/grep-show-lines-surrounding-each-match),
[finding files with matching content](https://stackoverflow.com/questions/16956810/find-all-files-containing-a-specific-text-string-on-linux),
[recursive extension filters](https://stackoverflow.com/questions/12516937/how-can-i-grep-recursively-but-only-in-files-with-certain-extensions),
[excluding directories](https://stackoverflow.com/questions/6565471/how-can-i-exclude-directories-from-grep-r),
and [printing only matches](https://stackoverflow.com/questions/1546711/can-grep-show-only-words-that-match-search-pattern).

### 9. Verify errors, edge cases, and integration

- Add table-driven tests for every supported option and meaningful option
  interaction; assert stdout, stderr, and exit status.
- Add fixtures for long/binary/malformed inputs, overlapping contexts,
  permission and traversal errors, symlinks, ignore precedence, glob negation,
  broken pipes, cancellation, and reentrant invocations.
- Differentially run the same fixtures against native GNU grep, macOS BSD
  grep, and upstream ripgrep on their respective CI platforms.
- Exercise `grep` and `rg` through Kabibi pipelines, redirections, and repeated
  shell invocations; confirm command output and statuses propagate correctly.
- Validate the normal Go test/build path and build/run the Kabibi vegetable
  using the repository's documented Rusticated workflow. Run representative
  search tests under that runtime so file I/O and cancellation use the custom
  sysroot rather than host-only behavior.

Current differential baseline: `u-root/cmds/core/searchcompat/differential_test.go`
compares stdout, stderr, status, and observed files. The strict run passed 8 GNU
grep cases (basic/fixed/inverted/only-matching/quiet/no-match/multiple-file/
NUL-record), 5 GNU sed cases (substitution/selection/deletion/`Q` status/
in-place backup), and 10 ripgrep cases (stdin/file defaults, fixed/line-number/
only-matching/multiline/recursive ignore-hidden, JSON events, and stats).
References were GNU grep 3.11 and GNU sed 4.9 through WSL, plus ripgrep 15.0.0.
Volatile elapsed-time fields are normalized; all other compared output is exact.
This intentionally small corpus leaves the exhaustive matrix above open.

### 10. Measure performance and close the plan

- A native Go 1.27.1 Windows/ARM64 comparison benchmark was added at
  `../../mohabbat/regast/compare_benchmark_test.go`. It compares compiled
  `regast` and standard-library `FindAllIndex` on prefix hits, rune-scan misses,
  and non-one-pass scans at 32 B, 4 KiB, and 1 MiB. Three 1-second samples
  indicate regast is about 1.4–2.1x slower on short inputs and about 15% slower
  on 1 MiB scan-heavy inputs; prefix-hit results at 1 MiB were noisy. The
  no-match rune scan allocated zero bytes in both engines. This is an engine
  baseline, not a full grep/rg or Rusticated benchmark; compilation is excluded,
  and no CPU profile was collected, so the exact source of the overhead is
  unconfirmed.
- Benchmark literal and regex searches over small files, large files, long
  lines, many files, and large ignored trees; record throughput, allocations,
  and peak memory against native references.
- Add cheap literal and fixed-string fast paths only after correctness tests
  pass; ensure every optimization preserves the same byte, output, and
  cancellation behavior.
- Close this plan only when all core tests pass, the practical differential
  corpus reaches at least 99% parity on Linux and macOS, no core test is
  skipped, shell integration passes, and Rusticated smoke tests succeed.
