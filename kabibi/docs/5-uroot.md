# Master Plan: In-Place Reentrant Modernization of u-root in Kabibi

## 1. Executive Summary & Lessons Learned

Kabibi integrates an in-process, bash-like execution environment powered by `mvdan.cc/sh/v3/interp`. Rather than shelling out to external OS binaries via `os/exec` (which fails in WebAssembly/`wasip1`, introduces platform discrepancies across Windows, Linux, and macOS, and incurs process-spawning overhead), Kabibi embeds a complete, self-contained Unix userland directly into the binary.

### Why the Previous Attempt Went Awry
Our core agreement is **forking `u-root` and applying minimal, manual in-place tweaks** so we have direct compile-time control over turning commands into reentrant functions. The first attempt diverged in three ways:
1. **Fixating on `pkg/core` instead of `cmds/core/*`**: Upstream `u-root` had an abandoned experiment in `pkg/core` covering only 15 commands. Trying to move all ~119 commands into `pkg/core` detached the work from the authentic command implementations in `cmds/core/*`.
2. **Stubbing instead of adapting (Tiers 4 & 5)**: Instead of adapting the real implementations of the ~40 system/network/Linux utilities (`mount`, `ip`, `pci`, `kexec`, `insmod`, etc.) to run natively where supported with graceful fallbacks where not, the implementation stubbed them out unconditionally (`"command not supported on <OS>"`), losing their functionality even on Linux.
3. **Destructive replacement of `cmds/core/*`**: The authentic source and test files in `cmds/core/<cmd>/` were wiped in the working tree and replaced with 21-line dummy wrappers.

### The Corrected Strategy
1. **Preserve Reference Work (`../uroot-try`)**: Save the existing working-tree work (which solved local `flag.FlagSet` extraction, stream decoupling, and context cancellation for ~74 commands) in a temporary side directory `../uroot-try/` for reference.
2. **Reset Submodule to Clean Upstream `HEAD`**: Hard-reset `u-root/` to upstream `HEAD`, restoring 100% of the authentic `cmds/core/*` source files, OS-specific files (`*_linux.go`, `*_unix.go`, `*_windows.go`), and unit tests.
3. **Modify `cmds/core/<cmd>` In-Place, Command by Command**: Convert each command in `cmds/core/<cmd>` directly into an importable package (`package <cmd>`) exposing a reentrant `Main(...) int` entrypoint. We are **not** building standalone CLI binaries; our sole target is working cleanly inside Kabibi across all target platforms.

```
+-----------------------------------------------------------------------------------+
|                                 Rustica Repository                                |
|                                                                                   |
|  +-----------------------+              +--------------------------------------+  |
|  |        kabibi/        |              |          u-root/ (Git Submodule)     |  |
|  |                       |              |                                      |  |
|  |  ui/shell/shell.go    |              |  cmds/core/<cmd>/                    |  |
|  |    |                  |              |    package <cmd>                     |  |
|  |    +-- interp.Runner  |              |    func Main(ctx, io, args...) int   |  |
|  |          |            |              |      +-- local flag.NewFlagSet       |  |
|  |          v            |              |      +-- return int (no os.Exit)     |  |
|  |  ui/shell/uroot.go    |-- replace -->|      +-- native Linux/macOS/Windows  |  |
|  |  (urootRegistry)      |   directive  |      +-- Rusticated ABI integration  |  |
|  +-----------------------+              +--------------------------------------+  |
+-----------------------------------------------------------------------------------+
```

---

## 2. Target Standard: In-Place `Main(...) int` Convention

Every command in `u-root/cmds/core/<cmd>/` is modified in-place with minimal diff footprint according to the following rules:

### 2.1 Package Naming
In Go, `package main` cannot be imported by another package. Each command directory `cmds/core/<cmd>/` changes its declaration from `package main` to `package <cmd>`.
* **Reserved Words & Punctuation Exceptions**:
  * `cmds/core/false` $\to$ `package falsecmd`
  * `cmds/core/true` $\to$ `package truecmd`
  * `cmds/core/init` $\to$ `package initcmd`
  * `cmds/core/io` $\to$ `package iocmd`
  * `cmds/core/strings` $\to$ `package stringscmd`
  * `cmds/core/time` $\to$ `package timecmd`
  * `cmds/core/sync` $\to$ `package synccmd`
  * `cmds/core/switch_root` $\to$ `package switchroot`
  * `cmds/core/update-rc.d` $\to$ `package updatercd`

### 2.2 The Shared `IO` Contract & `Main` Signature
A lightweight shared execution context struct (e.g. in `u-root/pkg/uroot/io.go` or `u-root/pkg/core/io.go`) supplies the command's virtualized process environment without touching OS globals:

```go
package core

import (
	"context"
	"io"
	"path/filepath"
)

type LookupEnvFunc func(key string) (string, bool)

type IO struct {
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	Dir       string
	LookupEnv LookupEnvFunc
}

// ResolvePath resolves relative paths against io.Dir without mutating process cwd.
func (ioEnv IO) ResolvePath(p string) string {
	if p == "" || filepath.IsAbs(p) || ioEnv.Dir == "" {
		return p
	}
	return filepath.Join(ioEnv.Dir, p)
}

func (ioEnv IO) Getenv(key string) string {
	if ioEnv.LookupEnv != nil {
		if v, ok := ioEnv.LookupEnv(key); ok {
			return v
		}
	}
	return ""
}
```

Every command in `cmds/core/<cmd>` replaces `func main()` with:

```go
func Main(ctx context.Context, ioEnv core.IO, args ...string) (exitCode int)
```

### 2.3 The 7 Mandatory Reentrancy & Safety Rules

1. **Zero Panic Guarantee**:
   Every `Main` begins with a defensive recovery guard so neither syntax errors, bad flags, nor unexpected nil dereferences can crash Kabibi:
   ```go
   defer func() {
       if r := recover(); r != nil {
           fmt.Fprintf(ioEnv.Stderr, "<cmd>: internal panic: %v\n", r)
           exitCode = 1
       }
   }()
   ```
2. **Local `flag.FlagSet` Only (Zero Shared State)**:
   Never use `flag.CommandLine`, package-level `flag.Bool(...)`, or package-level `flag.Usage`. Allocate a local `flag.NewFlagSet("<cmd>", flag.ContinueOnError)` inside `Main` (or `run`), bind flags to a stack-allocated struct, and direct flag errors/usage to `ioEnv.Stderr`:
   ```go
   fs := flag.NewFlagSet("<cmd>", flag.ContinueOnError)
   fs.SetOutput(ioEnv.Stderr)
   ```
   * Handle `errors.Is(err, flag.ErrHelp)` by returning `0`.
   * Handle other flag parse errors by returning `2` (or `1` per command convention).
3. **Eliminate Global Mutable Caches**:
   Any helper package with package-level caches (such as `uidCache` and `gidCache` in `u-root/pkg/ls/lookup.go`) must either be protected by a `sync.RWMutex` or moved to per-invocation state so concurrent pipelines (`ls -l | ...`) never race.
4. **Return-Based Exit Control**:
   * Replace `os.Exit(code)` with `return code`.
   * Replace `log.Fatal(...)` / `log.Fatalf(...)` with `fmt.Fprintf(ioEnv.Stderr, ...)` followed by `return 1`.
5. **Stream Decoupling**:
   * Replace `os.Stdin`, `os.Stdout`, `os.Stderr` with `ioEnv.Stdin`, `ioEnv.Stdout`, `ioEnv.Stderr`.
   * Replace `fmt.Print*` with `fmt.Fprint*(ioEnv.Stdout, ...)`.
6. **Virtual Working Directory & Environment**:
   * Never call `os.Chdir`. Resolve all file path arguments through `ioEnv.ResolvePath(path)`.
   * Read environment variables through `ioEnv.Getenv(key)` / `ioEnv.LookupEnv(key)`.
7. **Context Cancellation (`ctx.Done()`)**:
   * All loops, file walks, `sleep` waits, `tail -f` pollers, and infinite emitters (`yes`) must select on `ctx.Done()` and return `130` (or non-zero exit status) immediately on interrupt.

---

## 3. Target Platform Matrix & Relaxation Policy

Every command must compile and behave predictably across all four target environments:

| Target Platform | Requirement & Implementation Mechanism |
| :--- | :--- |
| **1. Linux (Native)** | **100% Functional.** Uses native Linux syscalls (`unix.Mount`, `unix.Reboot`, `unix.Unshare`, `unix.KexecLoad`, `ioctl`), Netlink sockets (`AF_NETLINK`), and `/proc`, `/sys`, `/dev`. |
| **2. macOS (Native)** | **Fully Functional** for all portable, POSIX/BSD, and hardware/process inspection tools. Uses Darwin `golang.org/x/sys/unix`, `sysctl` (`KERN_PROC_ALL`, `KERN_BOOTTIME`, `kern.msgbuf`), Mach VM (`host_statistics64`), routing sockets, and IOKit via `purego`. |
| **3. Windows (Native)** | **Fully Functional** for all ~75 portable utilities plus adapted system inspection tools via Win32 APIs (`kernel32.dll` for `GlobalMemoryStatusEx`, `CreateToolhelp32Snapshot`, `GetDiskFreeSpaceExW`, `GetTickCount64`; `iphlpapi.dll` for `GetAdaptersAddresses`, `GetExtendedTcpTable`, `IcmpSendEcho`). |
| **4. Rusticated (`wasip1` / `washmhost`)** | **Fully Functional** matching the capabilities of the underlying host OS:<br>• **Portable & `/proc`/`/sys` tools**: Run directly over existing Rusticated `path_open`, `dir_read`, `read`, `write` ABIs.<br>• **Transport Networking (`netcat`, `wget`)**: Run directly over Rusticated `net_open`, `net_accept`, `read`, `write`.<br>• **Rusticated+Linux**: Extra syscalls (`mount`, `reboot`, `ioctl`, `AF_NETLINK`) routed through `washmhost` host imports.<br>• **Rusticated+macOS**: Darwin-specific inspection routed via host dylib / `washmhost`. |

### Allowed Relaxations (Case-by-Case Basis)
Where a command targets a kernel subsystem that fundamentally does not exist on the host OS, it is allowed to fail gracefully (printing a clear diagnostic to `ioEnv.Stderr` and returning exit code `1`, never panicking or failing compilation):
* **Example (`mount`)**: Expected to work on Linux, macOS, and Rusticated running on Linux/macOS. Allowed to fail gracefully on Windows and Rusticated running on Windows.
* **Example (`kexec`, `insmod`, `rmmod`, `unshare`, `switch_root`)**: Linux kernel-specific subsystems. Must work on Linux (and Rusticated+Linux); allowed to fail gracefully on macOS and Windows.

---

## 4. Execution Methodology: Command-by-Command Workflow

While commands are grouped into logical bands below to organize similar technical challenges, **actual implementation proceeds strictly one command at a time**.

### Phase 0: Workspace Preparation (Prerequisite)
- [x] **0.1**: Copy current `u-root/pkg/core/*` working-tree files into `../uroot-try/` as a reference archive.
- [x] **0.2**: Reset `u-root` submodule to clean upstream `HEAD` (`git checkout -- . && git clean -fd`).
- [x] **0.3**: Fix concurrent map hazard in [`u-root/pkg/ls/lookup.go`](file:///c:/Users/mihai/rustic/u-root/pkg/ls/lookup.go) (`uidCache` / `gidCache` mutex protection) and retain `wasip1` build tags in `pkg/ls` and `pkg/cpio`.
- [x] **0.4**: Add `core.IO` helper struct in `u-root/pkg/core` and update `kabibi/ui/shell/uroot.go` adapter to invoke `Main(ctx, ioEnv, args...) int`.

### Per-Command Definition of Done (DoD)
A command checkbox `[x]` may **only** be ticked when all 5 criteria are met:
1. **In-Place Edit**: Authentic files in `u-root/cmds/core/<cmd>/` are modified in-place to `package <cmd>` and `func Main(ctx context.Context, ioEnv core.IO, args ...string) int`.
2. **Reentrancy Verified**: Local `flag.FlagSet`, zero `os.Exit`/`log.Fatal`, all paths resolved via `ioEnv.ResolvePath`, all I/O via `ioEnv`, `ctx.Done()` respected.
3. **Platform Coverage**: Builds and works across Linux, macOS, Windows, and Rusticated (`GOOS=wasip1 GOARCH=wasm`), with any OS-specific files (`_linux.go`, `_darwin.go`, `_windows.go`, `_other.go`) properly gated.
4. **Command Unit Tests**: Existing unit tests in `u-root/cmds/core/<cmd>/*_test.go` are updated and passing.
5. **Kabibi Integration Test**: Wired into [`kabibi/ui/shell/uroot.go`](file:///c:/Users/mihai/rustic/kabibi/ui/shell/uroot.go) and verified via [`kabibi/ui/shell/uroot_test.go`](file:///c:/Users/mihai/rustic/kabibi/ui/shell/uroot_test.go) (including double-invocation flag collision check and functional test).

---

## 5. Master Command-by-Command Checklist (All 120 Commands)

### Band 1: Core Portable Text, File & Shell Utilities (56 Commands)
*100% cross-platform (Linux, macOS, Windows, Rusticated). Pure algorithmic or standard file/stream I/O.*

#### 1A. Pilot Commands (First 3 to Validate Architecture End-to-End)
- [x] **`echo`** (mvdan/sh shell builtin; u-root echo deliberately not registered): Use one selected implementation for ordinary shell commands and commands launched by Kabibi's `xargs`.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1A_Pilot$' -v` — passed; covers ordinary shell `echo`, `cat`, and `grep`.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band1E_ShellControlAndBuiltins/xargs' -v` — passed; `xargs` child `echo` uses mvdan/sh.
  - `go run . kabibi -r -c "echo 'a\nb' | hexdump"` — bytes `61 5c 6e 62 0a`; default shell echo preserves the literal `\n`.
  - `go run . kabibi -r -c "printf '%s\0' 'a\nb' | xargs -0 | hexdump"` and `go run . kabibi -r -c "printf '%s\0' 'a\nb' | xargs -0 echo | hexdump"` — both emitted `61 5c 6e 62 0a`; xargs passes the argument unchanged to mvdan/sh's echo builtin.
  - Bash comparison: `echo` default and `-E` preserve `\n`; `-e` expands it; `-e ...\c` emits the prefix without a newline. mvdan/sh still differs for `-e ...\c` by printing `\c` literally; the u-root echo implementation previously differed by adding a newline after truncation.
- [x] **`cat`** (`cmds/core/cat`): Stream files and `-` (`ioEnv.Stdin`) to `ioEnv.Stdout`; resolve paths via `ioEnv.ResolvePath`.
  - `go -C u-root test ./cmds/core/cat -v` — passed; covers files, stdin, interleaved `-`, missing files, I/O errors, cancellation, and reentrancy.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1A_Pilot$/cat$' -v` — passed; covers concatenated files, piped stdin through `-`, and missing-file status.
  - `go run . kabibi -r -c "echo piped | cat"` — printed `piped` from pipeline stdin.
  - `go run . kabibi -r -c "echo piped | cat -"` — printed `piped` from pipeline stdin with explicit `-`.
  - `go run . kabibi -r -c "cat README.md | head -n 1"` — read the relative file and printed `<!--`.
  - `go run . kabibi -r -c "cat definitely_nonexistent_file_xyz.txt"` — reported the missing path and exited nonzero (1).
- [x] **`grep`** (`cmds/core/grep`): Pilot implementation with local flags (`-i`, `-v`, `-r`, `-c`, `-n`, `-q`, `-F`, `-h`, `-l`); check `ctx.Done()` in scanner; return `1` on no match. The broader GNU/BSD grep and ripgrep compatibility work is tracked in [13-grep-ripgrep.md](13-grep-ripgrep.md).
  - `go -C u-root test ./cmds/core/grep -v` — passed; covers stdin/files, recursive search, all listed flags, cancellation, and reentrancy.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1A_Pilot$/grep$' -v` — passed; shell integration covered basic match, `-i`, `-v`, `-c`, `-n`, `-q`, and `-F`.
  - `go run . kabibi -r -c "printf 'Apple\nbanana\nAPRICOT\n' | grep -i -n ap"` — printed numbered case-insensitive matches (`1:Apple`, `3:APRICOT`).
  - `go run . kabibi -r -c "printf 'Apple\nbanana\nAPRICOT\n' | grep -v banana"` — printed `Apple` and `APRICOT`.
  - `go run . kabibi -r -c "printf 'Apple\nbanana\nAPRICOT\n' | grep -c banana"` — printed `1`.
  - `go run . kabibi -r -c "printf 'a.b\naxb\n' | grep -F 'a.b'"` — matched only the literal `a.b`.
  - `go run . kabibi -r -c "grep -n '^# Mohabbat' README.md"` — read the relative file and printed `4:# Mohabbat`.
  - `go run . kabibi -r -c "printf 'Apple\nbanana\n' | grep -q strawberry"` — no match; exited 1.

#### 1B. Text Processing & Filters
- [x] **`head`** (`cmds/core/head`): Local `-c` and `-n` flags; resolve file paths; stream to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/head -v` — passed; covers default/requested lines, bytes, stdin, files, invalid counts, I/O errors, and cancellation.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/head$' -v` — passed; covers `-n`, `-c`, and piped stdin.
  - `go run . kabibi -r -c "head -n 3 README.md"` — printed the first three lines from the relative file.
  - `go run . kabibi -r -c "head -c 5 README.md"` — printed the first five bytes (`<!--` plus newline).
  - `go run . kabibi -r -c "printf 'a\nb\nc\n' | head -n 1"` — printed only `a` from stdin.
- [x] **`tail`** (`cmds/core/tail`): Local `-c`, `-n`, `-f` flags; monitor `ctx.Done()` in `-f` follow loop.
  - `go -C u-root test ./cmds/core/tail -v` — passed; covers line/byte counts, stdin/files, `-f` append and cancellation, broken pipes, and invalid counts.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/tail$' -v` — passed; covers `-n` on a file and piped stdin.
  - `go run . kabibi -r -c "printf 'one\ntwo\nthree\n' | tail -n 2"` — printed the final two lines (`two`, `three`).
  - `go run . kabibi -r -c "printf 'one\ntwo\nthree\n' | tail -c 5"` — printed the final five bytes (`hree` plus newline).
  - `go run . kabibi -r -c "tail -n 3 README.md"` — read the relative file and printed its final three lines.
- [x] **`sort`** (`cmds/core/sort`): Local `-r`, `-u`, `-f`, `-b`, `-n`, `-C`, `-o` flags; resolve `-o` output file via `ioEnv.ResolvePath`.
  - `go -C u-root test ./cmds/core/sort -v` — passed; covers stdin/files, all listed flags and combinations, ordered checks, and relative `-o` paths.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/sort$' -v` — passed; covers default, `-r`, `-u`, `-n`, and `-o`.
  - `go run . kabibi -r -c "printf 'banana\napple\ncherry\napple\n' | sort -ru"` — printed unique lines in reverse order (`cherry`, `banana`, `apple`).
  - `go run . kabibi -r -c "printf '10\n2\n1\n' | sort -n"` — printed numeric order (`1`, `2`, `10`).
  - `go run . kabibi -r -c "printf 'b\nA\na\n' | sort -f"` — sorted case-insensitively, preserving the tie order of `A` and `a`.
  - `go run . kabibi -r -c "printf 'a\nb\n' | sort -C"` — ordered input passed with no output (exit 0).
  - `go run . kabibi -r -c "printf 'b\na\n' | sort -C"` — unordered input was rejected (exit 1).
  - `go run . kabibi -r -c "printf 'b\na\n' | sort -o sort-probe.txt; cat sort-probe.txt; rm sort-probe.txt"` — wrote and read back sorted output (`a`, `b`), then removed the probe file.
- [x] **`uniq`** (`cmds/core/uniq`): Local `-u`, `-d`, `-c`, `-i`, `-f`, `-s` flags; stream `ioEnv.Stdin` to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/uniq -v` — passed; covers stdin/files, all flags, long lines, read/write errors, and missing final newline.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/uniq$' -v` — passed; shell integration covered default, `-c`, `-d`, `-u`, `-i`, `-f`, and `-s`.
  - `go run . kabibi -r -c "printf 'a\na\nb\nb\nb\nc\n' | uniq -c"` — printed counts `2`, `3`, and `1` for the adjacent groups.
  - `go run . kabibi -r -c "printf 'one same\n two same\nthree other\n' | uniq -f 1"` — compared after the first field and printed `one same`, `three other`.
  - `go run . kabibi -r -c "printf 'xapple\nyapple\nzberry\n' | uniq -s 1"` — compared after the first character and printed `xapple`, `zberry`.
  - `go run . kabibi -r -c "printf 'a\nA\nb\n' | uniq -i"` — treated adjacent `a` and `A` as duplicates.
- [x] **`wc`** (`cmds/core/wc`): Local `-l`, `-w`, `-c`, `-m`, `-L` flags; write counts to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/wc -v` — passed; covers files/stdin, counts, UTF-8, display widths, tab stops, graphemes, ANSI styling, CRLF, and output errors.
  - `go -C kabibi test ./ui/terminal` — passed; verifies both CellBuf VT paths enable Unicode Core mode and retain a ZWJ family emoji as a width-2 grapheme.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/wc$' -v` — passed; covers `-l`, `-w`, `-c`, `-m`, `-L`, wide characters, tabs, and graphemes through shell integration.
  - `go run . kabibi -r -c "printf 'a b\nccc\n' | wc -l -w -c -m -L"` — printed `2 3 8 8 3`.
  - `go run . kabibi -r -c "printf 'é\n' | wc -c"` — counted 3 UTF-8 bytes.
  - `go run . kabibi -r -c "printf 'é\n' | wc -m"` — counted 2 Unicode code points (including newline).
  - `go run . kabibi -r -c "printf '界\n' | wc -L"` — reported 2 display columns.
  - `go run . kabibi -r -c "printf 'a\tb\n' | wc -L"` — reported 9 columns with tab expansion to the next 8-column stop.
  - `go run . kabibi -r -c "printf '👨‍👩‍👧‍👦\n' | wc -L"` — reported 2 columns for the grapheme cluster.
- [x] **`tr`** (`cmds/core/tr`): Local `-d`, `-s`, `-c`/`-C` flags; character translation from `ioEnv.Stdin` to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/tr -v` — passed; covers character classes, deletion, complement, squeeze, replacement characters, and output errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/tr$' -v` — passed; shell integration covered translation, deletion, squeeze, and complement deletion.
  - `go run . kabibi -r -c "printf 'hello\n' | tr a-z A-Z"` — translated to `HELLO`.
  - `go run . kabibi -r -c "printf 'h1e2l3l4o\n' | tr -d '0-9'"` — deleted digits, leaving `hello`.
  - `go run . kabibi -r -c "printf 'goooood\n' | tr -s o"` — squeezed repeated `o` characters to `god`.
  - `go run . kabibi -r -c "printf 'banana\n' | tr -cd a"` and `go run . kabibi -r -c "printf 'banana' | tr -Cd a"` — complement deletion retained only `aaa` with both `-c` and `-C`.
  - `go run . kabibi -r -c "printf 'abcda\n' | tr -c ab XY"` — printed `XYYYXY`; the final `Y` is the newline, also mapped by complement translation. Without an input newline, `go run . kabibi -r -c "printf 'abcda' | tr -c ab XY"` printed `XYYYX`.
  - `go run . kabibi -r -c "printf 'aaaabbbb\n' | tr -s a X"` — squeezed the translation output set, yielding `Xbbbb`.
- [ ] **`tee`** (`cmds/core/tee`): Local `-a`, `-i` flags; multi-writer from `ioEnv.Stdin` to `ioEnv.Stdout` + resolved files.
  - `go -C u-root test ./cmds/core/tee -v` — passed; covers output/append, flag parsing, relative paths, and write errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/tee$' -v` — passed; covers multiple output files, stdout, and append.
  - `go run . kabibi -r -c "printf 'first\n' | tee tee-probe.txt; printf 'second\n' | tee -a tee-probe.txt; cat tee-probe.txt; rm tee-probe.txt"` — tee passed both streams through, appended to the file, read back both lines, and removed the probe file.
  - `go run . kabibi -r -c "printf 'ignored\n' | tee -i tee-ignore-probe.txt; cat tee-ignore-probe.txt; rm tee-ignore-probe.txt"` — `-i` completed a non-interrupt operation and wrote/read the expected data.
  - Retested with `go -C u-root test ./cmds/core/tee -v` and `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/tee$' -v` — both passed, but neither test sends Ctrl-C or SIGINT.
  - **Finding:** `-i` registers `os.Interrupt` with `signal.Notify`, but in Kabibi the in-process shell routes Ctrl-C through context cancellation. `runTee` returns on `ctx.Done()` regardless of `-i`, so the flag does not suppress that cancellation path. Ctrl-C behavior remains untested and this item stays unchecked.
- [x] **`seq`** (`cmds/core/seq`): Local `-f`, `-s`, `-w` flags; check `ctx.Done()` in emission loop.
  - `go -C u-root test ./cmds/core/seq -v` — passed; covers separators, formats, widths, decimal values, negative increments, context cancellation, errors, and output failures.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/seq$' -v` — passed; covers basic sequences, `-s`, `-f`, and negative increments after positional operands.
  - `go run . kabibi -r -c "seq -s , 1 3"` — printed `1,2,3`.
  - `go run . kabibi -r -c "seq -f %02g 1 3"` — printed zero-padded `01`, `02`, `03`.
  - `go run . kabibi -r -c "seq -w -10 2 -2"` — printed `-10`, `-08`, `-06`, `-04`, `-02`.
  - `go run . kabibi -r -c "seq 3 -1 -1"` — printed `3`, `2`, `1`, `0`, `-1`.
  - `go run . kabibi -r -c "seq 3 -1 1"` — printed `3`, `2`, `1`.
  - `go run . kabibi -r -c "seq 1 -1 -3"` — printed `1`, `0`, `-1`, `-2`, `-3`.
  - `go run . kabibi -r -c "seq -s , 1 -1 -3"` — printed `1,0,-1,-2,-3`.
  - `go run . kabibi -r -c "seq -w -10 2 -2"` — retained zero-padding and printed `-10`, `-08`, `-06`, `-04`, `-02`.
- [ ] **`sed`** (`cmds/core/sed`): Implement a reentrant, cross-platform stream editor with broad POSIX and GNU sed behavior; expose `Main(ctx, ioEnv, args...)`, use `ioEnv` for streams and resolved paths, honor cancellation, and never depend on a host `sed`. Use [uutils/sed](https://github.com/uutils/sed) as a permissively licensed Rust reference and GNU sed's manual/test suite as the compatibility oracle; uutils currently implements POSIX features while GNU compatibility and extensions remain ongoing, so do not treat it as GNU-complete.
  - The command is implemented in `u-root/cmds/core/sed` and registered in Kabibi's `urootRegistry`; compatibility work below remains open.
  - `go -C u-root test ./cmds/core/sed -count=1` — passing package suite covers regex substitution, ranges/branches, pattern and hold spaces, multiline/NUL input, GNU options, file I/O/in-place edits, and invalid scripts.
  - `go -C kabibi test ./ui/shell -run '^TestURootSedCommand$' -count=1` — passing shell integration exercises `sed` in a pipeline.
  - GNU differential suite: run the upstream GNU sed tests through a documented compatibility harness; record pass/fail/skip counts and investigate each failure rather than claiming full compatibility from package tests alone.
  - Cross-platform differential probes compare matching GNU sed results for basic and extended regular expressions, escaped delimiters, backreferences, replacement `&`, and first/global/nth substitutions.
  - Script-language probes cover `-e`/`-f`, numeric and regex addresses, address ranges and negation, `-n`, `p`, `d`, `q`/`Q`, labels and branches, and pattern/hold-space commands (`h`, `H`, `g`, `G`, `x`).
  - Multiline probes cover `N`, `D`, and `P`, end-of-file behavior, empty input, multiple files, and input without a final newline.
  - GNU-extension probes cover `-E`, case-insensitive and multiline regex flags, `z`, `-z`, in-place editing `-i` with and without backups, and documented symlink behavior.
  - `e` command probes cover both forms: executing pattern space into pattern space, and executing a supplied command with immediate output. Verify trailing-newline handling and command failures against GNU sed.
  - `s///e` probes verify replacement-command expansion, output trimming, conditional execution only after a successful substitution, and interaction/order with `p`.
  - Kabibi shell-execution probes verify `e` and `s///e` can run shell builtins, registered u-root commands, and Kabibi's `git` handler through the in-process nested-shell path, with cwd, environment, redirected streams, and exit status preserved.
  - In-place/error probes verify relative and spaced paths, backup contents, atomic replacement on failure, malformed scripts, invalid options, missing files, and nonzero exit status without corrupting originals.
  - Validate `GOOS=wasip1 GOARCH=wasm` using the repository's documented Rusticated sysroot/build workflow, then run Rusticated+Windows and raw Windows smoke tests for representative substitutions, scripts, pipelines, `e`, in-place edits, and failure cases.
- [x] **`comm`** (`cmds/core/comm`): Local `-1`, `-2`, `-3` flags; support `-` for `ioEnv.Stdin`.
  - `go -C u-root test ./cmds/core/comm -v` — passed; covers column suppression, stdin, long lines, file errors, and output errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/comm$' -v` — passed; covers `-12`, `-23`, and stdin with `-`.
  - `go run . kabibi -r -c "printf 'a\nb\nd\n' > comm-left.txt; printf 'b\nc\nd\n' > comm-right.txt; comm -12 comm-left.txt comm-right.txt; comm -23 comm-left.txt comm-right.txt; printf 'a\nb\n' | comm - comm-right.txt; rm comm-left.txt comm-right.txt"` — printed shared lines `b`, `d`, first-only line `a`, then compared stdin with the second file; removed both probe files.
- [x] **`cmp`** (`cmds/core/cmp`): Local `-s`, `-l`, `-n` flags; return `0` (equal), `1` (differ), `2` (error).
  - `go -C u-root test ./cmds/core/cmp -v` — passed; covers equality, differing bytes/lengths, offsets, `-s`, `-l`, `-L`, `-n`, and file errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/cmp$' -v` — passed; covers equal/different files, silent status, and `-n` boundaries.
  - `go run . kabibi -r -c "printf 'same' > cmp-left.txt; printf 'sameX' > cmp-right.txt; if cmp -s cmp-left.txt cmp-left.txt; then echo equal_exit_0; else echo equal_failed; fi; if cmp -n 4 cmp-left.txt cmp-right.txt; then echo prefix_equal_exit_0; else echo prefix_failed; fi; if cmp -s cmp-left.txt cmp-right.txt; then echo differ_failed; else echo differ_exit_1; fi; cmp cmp-left.txt cmp-right.txt; rm cmp-left.txt cmp-right.txt"` — confirmed equal status 0, equal four-byte prefix status 0, difference status 1, diagnostic at byte 5, then removed both probe files.
- [x] **`strings`** (`cmds/core/strings`): `package stringscmd`; local `-n`, `-t` flags; check `ctx.Done()` while scanning.
  - `go -C u-root test ./cmds/core/strings -v` — passed; covers minimum lengths, non-printable delimiters, long runs, decimal/octal/hex offsets, and output errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/strings$' -v` — passed; covers binary input, `-n`, and decimal offsets.
  - `go run . kabibi -r -c "printf '\0\0hello\0\0bye\0world!\0' | strings"` — found the default-length runs `hello` and `world!`.
  - `go run . kabibi -r -c "printf '\0\0hello\0\0bye\0world!\0' | strings -n 6"` — emitted only `world!`.
  - `go run . kabibi -r -c "printf '\0\0hello\0\0bye\0world!\0' | strings -t d"` — reported decimal start offsets `2` for `hello` and `13` for `world!`.
- [x] **`hexdump`** (`cmds/core/hexdump`): Conventional format by default (eight native-endian 16-bit hex words per row); `-C` selects canonical hex+ASCII; repeated rows are squeezed to `*` unless `-v` is supplied. This follows the [util-linux hexdump manual](https://github.com/util-linux/util-linux/blob/master/text-utils/hexdump.1.adoc). Upstream [Go u-root](https://github.com/u-root/u-root/blob/main/cmds/core/hexdump/hexdump.go) instead always emits canonical output and has no `-C`/`-v` flags. The Rust [uutils/coreutils](https://github.com/uutils/coreutils) project has no `hexdump` command (it is not part of GNU coreutils); it provides `od` with selectable output formats.
  - `go -C u-root test ./cmds/core/hexdump -v` — passed; covers stdin/files, offsets, limits, missing files, write errors, both output formats, repeated rows, and `-v`.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/hexdump$' -v` — passed; confirms the default word format, canonical `-C`, and combined `-s`/`-n`/`-v`.
  - `go run . kabibi -r -c "printf 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' | hexdump"` — emitted one row, `*`, and final offset `0000020`; adding `-v` emitted both rows.
  - `go run . kabibi -r -c "printf 'ABC' | hexdump"` — emitted default native-endian words `4241 0043`; adding `-C` emitted canonical bytes `41 42 43` with `|ABC|`.
  - `go run . kabibi -r -c "printf 'ABCDEF' | hexdump -C -s 1 -n 2 -v"` — displayed only `42 43` (`BC`) at offset `00000001`, then final offset `00000003`.
- [x] **`ts`** (`cmds/core/ts`): Prepend timestamps to each line from `ioEnv.Stdin` to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/ts -v` — passed; covers timestamped lines, invalid operands, and output errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/ts$' -v` — passed; shell integration confirmed input lines are timestamped.
  - `go run . kabibi -r -c "printf 'hello\nworld\n' | ts"` — prefixed both lines with relative timestamps.
  - `go run . kabibi -r -c "printf 'hello\nworld\n' | ts -R"` — prefixed both lines with signed relative timestamps.
- [x] **`tsort`** (`cmds/core/tsort`): Topological sort from file or `ioEnv.Stdin` to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/tsort -v` — passed; covers DAG orderings, cycles, malformed input, file/stdin input, long tokens, and output errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/tsort$' -v` — passed; shell integration checked a DAG and a cycle.
  - `go run . kabibi -r -c "printf 'a b b c c d' | tsort"` — emitted a valid order `a`, `b`, `c`, `d`.
  - `go run . kabibi -r -c "printf 'a b b a' | tsort"` — reported the cycle and returned exit 1.
  - `go run . kabibi -r -c "printf 'a b c' | tsort"` — reported odd token count and returned exit 1.
- [x] **`base64`** (`cmds/core/base64`): Local `-d` flag; encode/decode between `ioEnv.Stdin`/files and `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/base64 -v` — passed; covers stdin/files, encode/decode, invalid data, write errors, usage, and cancellation.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/base64$' -v` — passed; shell integration verified encoding and decoding.
  - `go run . kabibi -r -c "printf 'testing 123' | base64"` — encoded to `dGVzdGluZyAxMjM=`.
  - `go run . kabibi -r -c "printf 'dGVzdGluZyAxMjM=' | base64 -d"` — decoded to `testing 123`.
  - `go run . kabibi -r -c "printf 'dGVzdGluZyAxMjM=' > base64-probe.txt; base64 -d base64-probe.txt; rm base64-probe.txt"` — decoded file input and removed the probe.
  - `go run . kabibi -r -c "printf '!!!!' | base64 -d"` — rejected invalid input and exited 1.
  - `go run . kabibi -r -c "printf '%%%!' | base64 -d"` — not a valid base64 test: `printf` interpreted `%!` as a format error; the follow-up `!!!!` probe above correctly reached the decoder.
- [x] **`yes`** (`cmds/core/yes`): Loop writing to `ioEnv.Stdout`; terminate cleanly on `ctx.Done()` or broken pipe write error.
  - `go -C u-root test ./cmds/core/yes -v` — passed; covers argument output, broken-pipe handling, cancellation, and help.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/yes$' -v` — passed; bounded `yes foo` with `head -n 3`.
  - `go run . kabibi -r -c "yes foo | head -n 3"` — printed exactly three `foo` lines and terminated.
  - `go run . kabibi -r -c "yes | head -n 2"` — printed exactly two default `y` lines and terminated.
- [x] **`more`** (`cmds/core/more`): Pager reading files and `ioEnv.Stdin` to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/more -v` — passed; covers files, page boundaries, stdin passthrough, path resolution, invalid page sizes, and help.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1B_TextProcessing$/more$' -v` — passed; shell integration verified piped input.
  - `go run . kabibi -r -c "printf 'row1\nrow2\nrow3\n' | more"` — passed all three stdin lines through.
  - `go run . kabibi -r -c "printf 'file1\nfile2\n' > more-probe.txt; more more-probe.txt; rm more-probe.txt"` — read the relative file and printed both lines, then removed the probe.
  - `go run . kabibi -r -c "more -n 0"` — rejected a zero page size and exited 1.
- **Intentionally excluded from Kabibi:** `man` (`cmds/core/man`). Its embedded manual data is empty; registering the stub would shadow a host `man` command. Unregistered commands fall through to the next exec handler, while the upstream u-root package remains unchanged.

#### 1C. File & Directory Operations
- [x] **`ls`** (`cmds/core/ls`): GNU-style per-directory listing, sorting and recursion; supports hidden-file policies, long/inode/block formats, name quoting, sort modes, directory grouping, column/row/comma layouts, color and optional icons/OSC-8 hyperlinks. Terminal width/capabilities are passed only for an unredirected interactive shell command; redirected output remains plain unless explicitly forced. Target paths resolve through `ioEnv.ResolvePath`.
  - `go -C u-root test ./cmds/core/ls ./pkg/ls ./pkg/core -count=1` — passed; covers sorting, recursion, dot entries, metadata formats, redirection safety, `NO_COLOR`, display widths, and optional decorations.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/ls$' -count=1` — passed; shell integration covers default visibility, `-a`, and `-d`.
  - `go run . kabibi -r -c "mkdir -p ls-probe/subdir; printf 'x' > ls-probe/file.txt; printf 'longer' > ls-probe/.hidden.txt; printf 'y' > ls-probe/subdir/inner; ls ls-probe; ls -a ls-probe; ls -lhS ls-probe; ls -d ls-probe; ls -F ls-probe; ls -Q ls-probe; ls -R ls-probe; rm -r ls-probe"` — Rusticated smoke test verifies hidden entries, long/human-readable size sorting, directory-only output, suffixes, quoting, recursive headers, and cleanup.
- [x] **`cp`** (`cmds/core/cp`): GNU-style copy engine and option parsing for archive/recursive and symlink modes, metadata preservation, overwrite/update policies, backups, target/parent paths, hard/symbolic links, sparse/reflink modes, special files, and cancellation-aware file copies.
  - `go -C u-root test ./cmds/core/cp ./pkg/cp -count=1` — passed on Windows; covers CLI precedence, file/tree copies, links, metadata, backups, sparse data integrity, cancellation, and error handling.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/cp_mv_rm$' -count=1` — passed.
  - Linux test binaries for `./cmds/core/cp` and `./pkg/cp` cross-compiled successfully with `GOOS=linux GOARCH=amd64`; Linux-only FIFO/xattr/sparse tests were not executed on Linux in this environment.
  - `go run . kabibi -r -c "mkdir cp-probe; printf 'archive\n' > cp-probe/src; cp -a cp-probe/src cp-probe/dst; cat cp-probe/dst; printf '\0\0\0' > cp-probe/zero; cp --sparse=always cp-probe/zero cp-probe/zero-copy; wc -c cp-probe/zero-copy; rm -r cp-probe"` — Rusticated smoke test printed `archive`, preserved the three-byte all-zero file with `--sparse=always`, and cleaned up its fixture.
  - `go run . kabibi -r -c "mkdir -p timestamp-abi-check; printf data > timestamp-abi-check/source; touch -d 2001-01-01T00:00:00Z timestamp-abi-check/source; cp -a timestamp-abi-check/source timestamp-abi-check/copy; ls -l timestamp-abi-check/source timestamp-abi-check/copy; rm -r timestamp-abi-check"` — timestamp setting succeeded through Rusticated and `cp -a` preserved the displayed `Jan 1 00:00` mtime on the copy.
  - Platform limits: `--one-file-system` requires Linux device IDs; special files, ownership, xattrs, reflinks, and SELinux contexts depend on platform support. Archive mode treats unsupported metadata preservation as best-effort. Policy-default `-Z` is unavailable; an explicit SELinux label requires supported SELinux xattrs.
- [x] **`mv`** (`cmds/core/mv`): Supports `-f`, `-i`, `-n`, and `-u`, with last-option-wins overwrite policy, prompted confirmation through injected stdin/stderr, and source/destination resolution via `ioEnv.ResolvePath`.
  - `go -C u-root test ./cmds/core/mv -count=1` — passed; covers forced replacement, interactive accept/decline, overwrite-option precedence, moves, `-u`/`-n`, path resolution, and errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/cp_mv_rm$' -count=1` — passed; shell integration verifies `-f` and piped `-i` confirmation/decline.
  - `go run . kabibi -r -c "mkdir mv-overwrite-probe; printf 'new-force' > mv-overwrite-probe/source; printf 'old-force' > mv-overwrite-probe/dest; mv -f mv-overwrite-probe/source mv-overwrite-probe/dest; cat mv-overwrite-probe/dest; printf 'new-no' > mv-overwrite-probe/source; printf 'old-no' > mv-overwrite-probe/dest; printf 'n\\n' | mv -i mv-overwrite-probe/source mv-overwrite-probe/dest; cat mv-overwrite-probe/dest; printf 'y\\n' | mv -i mv-overwrite-probe/source mv-overwrite-probe/dest; cat mv-overwrite-probe/dest; rm -r mv-overwrite-probe"` — Rusticated smoke test verified force overwrite, declined and confirmed prompts, and cleanup.
- [x] **`rm`** (`cmds/core/rm`): Supports `-r`/`-R`, `-f`, `-i`, `-v`, and `-d`; non-recursive removal refuses directories unless `-d` requests empty-directory removal. Paths resolve through `ioEnv.ResolvePath`.
  - `go -C u-root test ./cmds/core/rm -count=1` — passed; covers files, recursion, force, prompts, verbose output, and empty/non-empty directory behavior.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/cp_mv_rm$' -count=1` — passed; shell integration covers ordinary removal, `-f`, and `-d`.
  - `go run . kabibi -r -c "mkdir -p rm-probe/tree/sub; printf 'remove me\n' > rm-probe/file.txt; printf 'child\n' > rm-probe/tree/sub/child.txt; rm -v rm-probe/file.txt; printf 'n\n' | rm -i rm-probe/tree/sub/child.txt; cat rm-probe/tree/sub/child.txt; printf 'y\n' | rm -i rm-probe/tree/sub/child.txt; rm -R rm-probe"` — verified verbose output, declined and confirmed prompts, recursive directory deletion, and cleanup.
  - `go run . kabibi -r -c "rm -f nonexistent-rm-probe.txt"` — ignored the missing path and succeeded.
  - `go run . kabibi -r -c "mkdir -p rm-directory-probe/empty rm-directory-probe/nonempty; printf 'child' > rm-directory-probe/nonempty/file; rm -d rm-directory-probe/empty; if rm -d rm-directory-probe/nonempty; then echo unexpected-success; else echo nonempty-preserved; fi; if rm rm-directory-probe/nonempty; then echo unexpected-default-success; else echo default-directory-protected; fi; rm -r rm-directory-probe"` — Rusticated smoke test removed the empty directory, refused the non-empty directory both with `-d` and without it, then cleaned up.
- [x] **`mkdir`** (`cmds/core/mkdir`): Local `-p`, `-m`, `-v` flags; create directories relative to `ioEnv.Dir`.
  - `go -C u-root test ./cmds/core/mkdir -count=1` — passed; covers creation, existing paths, `-p`, `-v`, mode validation, and octal permission bits.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/mkdir_mktemp$' -count=1` — passed; shell integration covers ordinary and nested directory creation.
  - `go run . kabibi -r -c "mkdir -p -m 700 mkdir-mode-probe/a/b; ls -ld mkdir-mode-probe/a/b; rm -r mkdir-mode-probe"` — succeeded and cleaned up; on Rusticated+Windows `ls` showed `drwxrwxrwx`. No virtual POSIX mode layer is added: the host filesystem's representable mode semantics are authoritative. Windows `os.Chmod` uses only the owner-write bit to control the read-only attribute ([Go `os.Chmod` documentation](https://pkg.go.dev/os#Chmod)).
  - Linux FAT/VFAT synthesizes modes from mount options (`uid`, `gid`, `umask`, `dmask`, `fmask`) ([kernel documentation](https://www.kernel.org/doc/html/latest/filesystems/vfat.html)). The kernel can persist the FAT read-only attribute as write permission; unsupported arbitrary read/execute or mixed write-bit changes are ignored by the Linux FAT `setattr` path ([kernel source](https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git/tree/fs/fat/file.c)). macOS `mount_msdos` and `mount_exfat` likewise document mount-time owner/group and permission masks ([`mount_msdos(8)`](https://manpagez.com/man/8/mount_msdos/), [`mount_exfat(8)`](https://manpagez.com/man/8/mount_exfat/)).
  - `mkdir -m` now returns errors reported by the host's `os.Chmod`; host-successful no-op or reduced mode behavior remains native filesystem behavior.
- [x] **`mktemp`** (`cmds/core/mktemp`): Local `-d`, `-u`, `-q`, `-p`, `-t` flags; fallback temp dir via `ioEnv.Getenv("TMPDIR")`.
  - `go -C u-root test ./cmds/core/mktemp -count=1` — passed; covers default file/directory creation, prefixes/suffixes, non-creating dry-run output, `-t` with injected `TMPDIR`, and quiet failure status/diagnostics.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/mkdir_mktemp$' -count=1` — passed; shell integration checked default file and directory creation.
  - `go run . kabibi -r -c 'mkdir -p mktemp-probe; tmp=$(mktemp -p mktemp-probe sample); printf content > "$tmp"; echo file; cat "$tmp"; echo directory; dir=$(mktemp -d -p mktemp-probe dir); ls -d "$dir"; rm "$tmp"; rm -r "$dir"; rm -r mktemp-probe'` — created and read a file, created a directory under the chosen prefix, and cleaned up.
  - Rusticated smoke verified `mktemp -u -p ...` prints a candidate without creating it, and `TMPDIR=... mktemp -t sample` creates the file under that directory; all fixtures were cleaned up.
  - `-q` now suppresses the diagnostic while preserving a non-zero exit status for creation failures. Dry-run warns through injected stderr unless `-q` is also set.
- [x] **`touch`** (`cmds/core/touch`): Local `-a`, `-m`, `-c`, `-d`, `-r`, `-t` flags; resolve paths via `ioEnv.ResolvePath`.
  - `go -C u-root test ./cmds/core/touch -count=1` — passed; covers RFC3339 and legacy `-t` timestamp parsing, reference atime/mtime copying, `-a`/`-m` selection, and file creation behavior.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/touch_truncate$' -count=1` — passed; integration test confirms file creation and truncate behavior.
  - Rusticated smoke test verified `touch -t 200101020304.05` sets the requested time and `touch -r README.md` copies the reference timestamps; fixtures were cleaned up. The timestamp ABI is provided by `path_set_times`.
  - Touch package tests cross-compiled for Linux, macOS, and wasip1; the package also builds for Android. Reference access times are implemented for Linux/Android, macOS, Windows, and wasip1; other GOOS targets return an explicit unsupported error when `-r` requires atime.
- [x] **`truncate`** (`cmds/core/truncate`): Local `-s`, `-c` flags; resize target files relative to `ioEnv.Dir`, propagating stat and truncate errors.
  - `go -C u-root test ./cmds/core/truncate -count=1` — passed; covers validation, positive/negative sizes, block sizing, truncate failures, and `-c` on missing files.
  - The numbered ABI is implemented as `handle_truncate`; the Rust declarations, Go host, Node host, browser `AsyncFS`, and Rusticated Go `Truncate`/`Ftruncate` mappings are integrated.
  - `go run . kabibi -r -c "truncate -s 8 __rusticated-truncate-probe-2241.tmp; ls -l __rusticated-truncate-probe-2241.tmp; truncate -s 2 __rusticated-truncate-probe-2241.tmp; ls -l __rusticated-truncate-probe-2241.tmp; truncate -s 0 __rusticated-truncate-probe-2241.tmp; ls -l __rusticated-truncate-probe-2241.tmp; rm __rusticated-truncate-probe-2241.tmp"` — Rusticated reported sizes 8, 2, and 0 bytes; the probe was removed.
  - `go run . kabibi -r -c "truncate -s 1 ."` — failed with nonzero status and an `Is a directory` diagnostic, confirming errors are not reported as success.
  - Size suffix parsing remains separate from the file-size ABI; rejection of lowercase `1k` alone is not evidence of a GNU compatibility gap.
- [x] **`ln`** (`cmds/core/ln`): Supports symbolic and hard links, force/verbose behavior, relative symbolic targets (`-r`), `-n`, `-P`/`-L` hard-link source semantics, and `--target-kind=file|directory` for dangling links whose target kind cannot be inferred. Paths use the Rusticated link ABI where available; unsupported host/filesystem operations return errors rather than falling back to copies or another link type.
  - `go -C u-root test ./cmds/core/ln -count=1` — passed; covers link creation, target-kind selection, `-n`, and physical/logical hard-link behavior.
  - `go -C mohabbat\washmhost test -run '^TestPathLinkOperations$' -count=1` — passed; covers hard links, following a source symlink, symbolic links, and readlink buffer sizing.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/ln_readlink_realpath$' -count=1` — passed.
  - Rusticated shell smoke test passed for hard-link creation, file symlink creation, `readlink`, opening through symlinks, and relative symlink resolution; its temporary fixture was removed.
  - Node host tests passed (7/7) and browser host tests passed (23/23). Browser hard links explicitly return `ENOTSUP` because its current filesystem does not provide shared file identity. Windows symlink creation may also be denied by host policy; such failures are reported rather than silently substituted.
- [ ] **`chmod`** (`cmds/core/chmod`): Local `-R`, `-v`, `-c` flags; support octal and symbolic modes.
  - `go -C u-root test ./cmds/core/chmod -v` — passed; native tests cover octal and symbolic modes, multiple paths, and reference modes.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/chmod_find_du_pwd$' -v` — passed; host-backed integration confirms `chmod 0755` returns successfully.
  - `go run . kabibi -r -c "mkdir -p chmod-probe/sub; printf x > chmod-probe/file; chmod 0600 chmod-probe/file; ls -l chmod-probe/file; chmod u+x chmod-probe/file; ls -l chmod-probe/file; chmod -R 0700 chmod-probe; ls -l chmod-probe chmod-probe/sub; rm -r chmod-probe"` — each chmod returned success, but `ls -l` continued to show `-rw-rw-rw-` and `drwxrwxrwx`, with no changes. **Finding:** mode changes are no-ops in Rusticated/WASI despite success status.
  - `go run . kabibi -r -c "chmod -v 0644 README.md"` — parsed `-v` as the mode and rejected it. **Finding:** the checklist-listed verbose flag is not implemented.
  - `go run . kabibi -r -c "chmod -c 0644 README.md"` — parsed `-c` as the mode and rejected it. **Finding:** the checklist-listed change-reporting flag is not implemented.
- [x] **`find`** (`cmds/core/find`): Local `-name`, `-type`, `-mode`, `-l`, `-d` flags; respect `ctx.Done()` during walk.
  - `go -C u-root test ./cmds/core/find -v` — passed; native package tests cover matching and long output.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/chmod_find_du_pwd$' -v` — passed; shell integration confirms `find -name`.
  - `go run . kabibi -r -c "mkdir -p find-probe/sub; printf x > find-probe/file.txt; echo name; find -name file.txt find-probe; echo type; find -type d find-probe; echo mode; find -mode 0666 find-probe; echo long; find -l find-probe; echo debug; find -d -name file.txt find-probe; rm -r find-probe"` — all filters returned expected matches; `-l` displayed metadata and `-d` emitted debug logs; cleanup succeeded. The implementation checks `ctx.Done()` while consuming traversal results.
- [ ] **`du`** (`cmds/core/du`): Local `-a`, `-c`, `-d`, `-h`, `-s`, `-L`, `-P` flags; resolve paths relative to `ioEnv.Dir`.
  - `go -C u-root test ./cmds/core/du -v` — passed; native tests cover block accounting, `-a`, `-k`, `-s`, `-H`, `-L`, and relative symlinks.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/chmod_find_du_pwd$' -v` — passed; host-backed integration confirms `du -s`.
  - `go run . kabibi -r -c "mkdir -p du-probe/sub; printf x > du-probe/file; printf yy > du-probe/sub/child; echo all; du -a du-probe; echo summary; du -s du-probe; echo kilobytes; du -k du-probe; echo human; du -h du-probe; echo follow-command; du -H du-probe; rm -r du-probe"` — `-a`, `-s`, `-k`, `-h`, and `-H` ran and reported 512-byte block totals as expected; cleanup succeeded. (WASI symlink support is separately unavailable, so following links could not be exercised.)
  - `go run . kabibi -r -c "du -c README.md"` — rejected `-c` as undefined.
  - `go run . kabibi -r -c "du -d 1 README.md"` — rejected `-d` as undefined.
  - `go run . kabibi -r -c "du -P README.md"` — rejected `-P` as undefined. **Finding:** all three flags listed in the checklist are missing; this entry remains unchecked.
- [ ] **`readlink`** (`cmds/core/readlink`): Local `-f`, `-n`, `-v` flags; evaluate symlinks relative to `ioEnv.Dir`.
  - `go -C u-root test ./cmds/core/readlink -v` — passed; native package tests cover symlink read output, newline behavior, and verbose errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/ln_readlink_realpath$' -v` — passed; host-backed shell integration verifies reading a symlink target.
  - Rusticated shell smoke test verified reading the target of a created symlink. The link ABI now provides readlink support; `-f` and the remaining GNU-compatible edge cases still need dedicated Rusticated coverage.
- [ ] **`realpath`** (`cmds/core/realpath`): Local `-s`, `-q` flags; resolve canonical path relative to `ioEnv.Dir`.
  - `go -C u-root test ./cmds/core/realpath -v` — passed; native test covers path canonicalization and symlinks.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/ln_readlink_realpath$' -v` — passed; host-backed integration verifies resolving a symlink path.
  - `go run . kabibi -r -c "realpath README.md; realpath -q missing-realpath-probe"` — resolved `README.md` to its absolute path; quiet mode suppressed the missing-path diagnostic and returned failure.
  - `go run . kabibi -r -c "realpath -s README.md"` — rejected `-s` as undefined. **Finding:** the checklist-listed `-s` flag is missing; this item remains unchecked.
- [x] **`basename`** (`cmds/core/basename`): Strip directory and suffix; output to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/basename ./cmds/core/dirname -v` — passed; native tests cover suffix stripping and directory extraction.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/basename_dirname$' -v` — passed; host-backed shell integration covers suffix and multiple-name handling.
  - `go run . kabibi -r -c "basename /path/to/my_file.tar.gz .tar.gz; basename -a /a/b.txt /c/d.txt; dirname /var/log/syslog; dirname /single; dirname relative/file"` — basename produced `my_file`, `b.txt`, and `d.txt` as expected.
- [ ] **`dirname`** (`cmds/core/dirname`): Output parent directory component to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/basename ./cmds/core/dirname -v` — passed; native tests cover directory extraction.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/basename_dirname$' -v` — passed; host-backed shell integration passes for `/var/log/syslog`.
  - `go run . kabibi -r -c "basename /path/to/my_file.tar.gz .tar.gz; basename -a /a/b.txt /c/d.txt; dirname /var/log/syslog; dirname /single; dirname relative/file"` — returned `\var\log` for `/var/log/syslog` and `\` for `/single`, rather than POSIX `/var/log` and `/`. **Finding:** Rusticated dirname exposes Windows separators for Unix-style input; item remains unchecked.
- [ ] **`pwd`** (`cmds/core/pwd`): Local `-L`, `-P` flags; output `ioEnv.Dir` (or resolved physical path) to `ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/pwd -v` — passed; native tests cover physical and logical paths.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1C_FileAndDirOps$/chmod_find_du_pwd$' -v` — passed; shell integration confirms `pwd` produces a non-empty directory.
  - The previous forced-dispatch probe is obsolete; ordinary `pwd` is handled by mvdan/sh's builtin, so the shell probe does not verify the registered u-root `pwd` implementation.
  - Ordinary `pwd` is handled by mvdan/sh's builtin, so the shell probe does not verify the registered u-root `pwd` implementation. This duplicate still needs a case-by-case implementation decision.

#### 1D. Checksums, Archival & Compression
- [ ] **`shasum`** (`cmds/core/shasum`): Local `-a` (1, 256, 512), `-b`, `-c`, `-t` flags; check `ctx.Done()` while hashing.
  - `go -C u-root test ./cmds/core/shasum ./cmds/core/md5sum -v` — passed; native tests cover SHA-1/224/256/384/512 variants, stdin, files, and MD5 check mode.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1D_ArchivalAndChecksums$/(shasum|md5sum)$' -v` — passed; shell integration verifies SHA-256 file hashing, SHA-1 stdin hashing, and MD5 verification.
  - `go run . kabibi -r -c "printf abc > hash-probe; shasum -a 256 hash-probe; printf abc | shasum -a 1; rm hash-probe"` — produced the known SHA-256 and SHA-1 digests; cleanup succeeded.
  - `go run . kabibi -r -c "shasum -b README.md; shasum -c README.md; shasum -t README.md"` — rejected `-b`, `-c`, and `-t` as undefined. **Finding:** these checklist-listed flags are missing. The implementation also checks cancellation only between files; `shaGenerator` copies the stream without observing `ctx.Done()` during hashing.
- [ ] **`md5sum`** (`cmds/core/md5sum`): Local `-c`, `-b`, `-t`, `-q`, `-s` flags; check `ctx.Done()` while hashing.
  - `go -C u-root test ./cmds/core/shasum ./cmds/core/md5sum -v` — passed; native tests cover MD5 stdin, files, and checksum verification.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1D_ArchivalAndChecksums$/(shasum|md5sum)$' -v` — passed; shell integration verified a known MD5 digest and `-c`.
  - `go run . kabibi -r -c "printf abc > hash-probe; md5sum hash-probe; printf '900150983cd24fb0d6963f7d28e17f72  hash-probe\\n' > hash-check.md5; md5sum -c hash-check.md5; rm hash-probe; rm hash-check.md5"` — produced the known MD5 digest, reported `hash-probe: OK`, and cleaned up.
  - `go run . kabibi -r -c "md5sum -b README.md; md5sum -t README.md; md5sum -q README.md; md5sum -s README.md"` — rejected all four flags as undefined. **Finding:** the checklist-listed `-b`, `-t`, `-q`, and `-s` flags are missing; this entry remains unchecked.
- [ ] **`gzip`** (`cmds/core/gzip`): Also registered as `gzcat`; local `-d`, `-c`, `-f`, `-k`, `-l`, `-q`, `-r`, `-v`, `-1`..`-9` flags.
  - `go -C u-root test ./cmds/core/gzip -v` — passed; native smoke test covers level selection and compression/decompression.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1D_ArchivalAndChecksums$/gzip_and_gzcat$' -v` — passed; shell integration verifies `-k`, `gzcat`, and `-d`.
  - `go run . kabibi -r -c "mkdir gzip-probe; printf 'payload\\n' > gzip-probe/data; gzip -k gzip-probe/data; ls gzip-probe; gzcat gzip-probe/data.gz; gzip -t gzip-probe/data.gz; gzip -d gzip-probe/data.gz; cat gzip-probe/data; rm -r gzip-probe"` — kept the original while creating `.gz`, `gzcat` returned payload, integrity test passed, decompression restored content, and cleanup succeeded.
  - `go run . kabibi -r -c "gzip -l README.md"` — rejected `-l` as undefined.
  - `go run . kabibi -r -c "gzip -r README.md"` — rejected `-r` as undefined. **Finding:** the listed list and recursive flags are not implemented; this item remains unchecked.
- [ ] **`tar`** (`cmds/core/tar`): Local `-c`, `-x`, `-t`, `-f`, `-v`, `-z`, `-C` flags; resolve paths via `ioEnv.ResolvePath`.
  - `go -C u-root test ./cmds/core/tar -v` — passed; native tests cover create, list, extract, and argument validation.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1D_ArchivalAndChecksums$/tar$' -v` — passed; host-backed shell integration creates, lists, and extracts an archive.
  - `go run . kabibi -r -c "mkdir -p tar-probe/out; printf 'tar payload\\n' > tar-probe/file; tar -cvf tar-probe/archive.tar tar-probe/file; tar -tvf tar-probe/archive.tar; tar -xvf tar-probe/archive.tar tar-probe/out; ls -R tar-probe/out; cat tar-probe/out/tar-probe/file; rm -r tar-probe"` — create and list reported `tar-probe\file`; extract returned success and printed the entry, but `tar-probe/out` remained empty and the subsequent `cat` failed. **Finding:** Rusticated extraction silently omits the archived file; item remains unchecked.
  - `go run . kabibi -r -c "tar -czf tar-probe.tar README.md"` — rejected `-z` as undefined.
  - `go run . kabibi -r -c "tar -cf tar-probe.tar -C . README.md"` — rejected `-C` as undefined. **Finding:** both checklist-listed flags are missing.
- [ ] **`cpio`** (`cmds/core/cpio`): Archive in/out/pass-through streaming over `ioEnv.Stdin`/`ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/cpio -v` — passed; native tests cover archive creation, listing, extraction, and directory hard-link records.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1D_ArchivalAndChecksums$/cpio$' -v` — passed; host-backed shell integration creates an archive and lists its contents.
  - `go run . kabibi -r -c "mkdir cpio-probe; cd cpio-probe; printf 'cpio payload\\n' > file; echo file | cpio o > archive.cpio; cpio t < archive.cpio; rm file; cpio i < archive.cpio; cat file; cd ..; rm -r cpio-probe"` — Rusticated could not read source data for archive creation (`Not implemented on wasip1`); listing/extraction then failed on `ReadAt`, and deleting the source also reported I/O error. Retried with `go run . kabibi -r -c "rm -r cpio-probe"`; cleanup succeeded. **Finding:** cpio's file-backed operations fail in this Rusticated/WASI environment.
  - `go run . kabibi -r -c "cpio p output"` — rejected operation with usage. **Finding:** the listed pass-through operation is not implemented; this item remains unchecked.
- [x] **`gpgv`** (`cmds/core/gpgv`): OpenPGP signature verification writing results to `ioEnv.Stdout`/`ioEnv.Stderr`.
  - `go -C u-root test ./cmds/core/gpgv -v` — passed; covers valid signatures, invalid key/signature files, missing inputs, and usage errors.
  - `go -C kabibi test ./ui/shell -run '^TestURoot_Band1D_ArchivalAndChecksums$/gpgv$' -v` — passed; confirms nonzero status when arguments are missing.
  - `go run . kabibi -r -c "gpgv u-root/cmds/core/gpgv/testdata/key.pub u-root/cmds/core/gpgv/testdata/datafile.sig u-root/cmds/core/gpgv/testdata/datafile.txt"` — verified the checked-in signature and printed `OK`.

#### 1E. Shell Builtin & Control Utilities
- [ ] **`true`** (`cmds/core/true`): `package truecmd`; return `0`.
- [ ] **`false`** (`cmds/core/false`): `package falsecmd`; return `1`.
- [ ] **`sleep`** (`cmds/core/sleep`): Wait on `select { case <-time.After(d): return 0; case <-ctx.Done(): return 130 }`.
- [ ] **`which`** (`cmds/core/which`): Local `-a` flag; check `ioEnv.Getenv("PATH")` and registered Kabibi builtins.
- [ ] **`printenv`** (`cmds/core/printenv`): Read from `ioEnv.LookupEnv` / environment; output to `ioEnv.Stdout`.
- [ ] **`date`** (`cmds/core/date`): Local `-u`, `-d`, `-r`, `-I` flags; formatting works everywhere; clock setting (`settimeofday`) supported on privileged Linux/macOS and fails gracefully elsewhere.
- [ ] **`hostname`** (`cmds/core/hostname`): Read hostname via `os.Hostname()` everywhere; setting hostname (`Sethostname`) supported on privileged Unix and fails gracefully elsewhere.
- [ ] **`xargs`** (`cmds/core/xargs`): Local `-n`, `-I`, `-0`, `-t`, `-p`, `-r` flags; dispatch sub-commands through runner callback.
  - `go -C u-root test ./cmds/core/xargs -v` — passed; covers default `echo`, argument batching, `-0`, command-not-found, and interactive behavior.
  - `go run . kabibi -r -c "printf '%s\n' 'one two' | xargs | hexdump"` and `bash -c "printf '%s\n' 'one two' | xargs | od -An -tx1"` — both emitted `one two\n`.
  - `go run . kabibi -r -c "printf '%s\0' 'a\nb' | xargs -0 | hexdump"` — bytes `61 5c 6e 62 0a`; Kabibi's runner passes the child command to mvdan/sh's echo builtin without altering its argument.
  - `go run . kabibi -r -c "printf '%s\0' 'a\nb' | xargs -0 echo | hexdump"` — also emitted `61 5c 6e 62 0a`; explicit child `echo` uses the same path.
  - `bash -c "printf '%s\0' 'a\nb' | xargs -0 | od -An -tx1"` — bytes `61 5c 6e 62 0a`; host `xargs` preserves the argument and Bash's external `echo` leaves `\n` literal by default.
  - `go run . kabibi -r -c "printf '%s\0' 'a\cb' | xargs -0 | hexdump"` — bytes `61 5c 63 62 0a`; mvdan/sh leaves `\c` literal by default.
  - `bash -c "printf '%s\0' 'a\cb' | xargs -0 | od -An -tx1"` — bytes `61 5c 63 62 0a`; host `xargs` plus external `echo` prints the literal `\c`.
  - Dispatch comparison: mvdan/sh does not provide an `xargs` builtin; Kabibi's `xargs` is the u-root command. Its child-command callback now routes `echo` invocations to mvdan/sh, and the u-root echo command is not registered in Kabibi.
- [x] **`time`** (`cmds/core/time`): `package timecmd`; measure execution duration of target command and report to `ioEnv.Stderr`.
  - `go -C u-root test ./cmds/core/time -v` — passed; covers no-argument output, runner-based timing, and missing command.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band1E_ShellControlAndBuiltins/time$' -v` — passed; now separately checks mvdan's `time` shell syntax and the u-root utility via `command time`.
  - `go run . kabibi -r -c "time echo shell_keyword_probe"` — passed; shell keyword executed and printed mvdan's timing format.
  - `go run . kabibi -r -c "command time echo uroot_time_probe"` — passed; u-root `time` executed and printed `real`, `user`, and `sys` timing lines.
  - Dispatch note: bare `time` is parsed as mvdan/sh shell timing syntax; `command time` reaches the registered u-root utility. Both work, but they are distinct code paths.
- [x] **`timeout`** (`cmds/core/timeout`): Derive `context.WithTimeout(ctx, d)` and execute target command.
  - `go -C u-root test ./cmds/core/timeout -v` — passed; includes successful and timed-out runner cases.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band1E_ShellControlAndBuiltins/timeout$' -v` — passed; checks quick command success and exit status 124 after timeout.
  - `go run . kabibi -r -c "timeout 500ms echo timeout_ok"` — passed; printed `timeout_ok`.
  - `go run . kabibi -r -c "timeout -t 20ms sleep 1"` — timed out as intended with shell status 124 (the `go run` wrapper reports a non-zero child exit as its own failure).
  - `go run . kabibi -r -c 'timeout -t 20ms sleep 1; echo exit:$?'` — passed; printed `exit:124`, confirming status propagation.
- [x] **`backoff`** (`cmds/core/backoff`): Retry target command with exponential backoff respecting `ctx.Done()`.
  - `go -C u-root test ./cmds/core/backoff -v` — passed; unit test runner fails once then succeeds on retry.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band1E_ShellControlAndBuiltins/backoff$' -v` — passed; checks successful command dispatch.
  - `go run . kabibi -r -c "backoff -t 500ms true"` — passed; completed successfully.
  - `go run . kabibi -r -c "backoff -t 50ms false"` — failed with status 1 and reported the child command failure, as expected when the command does not succeed before the deadline.

---

### Band 2: Data Transfer, Subshells & Network Transport (8 Commands)
*Cross-platform over standard I/O and Rusticated's existing `net_open`/`net_accept`/`read`/`write` ABI.*

- [x] **`dd`** (`cmds/core/dd`): Convert `if=`, `of=`, `bs=`, `ibs=`, `obs=`, `count=`, `seek=`, `skip=`, `conv=`, `status=` into local FlagSet; replace `usage()` `log.Fatal` with error return; stream between `ioEnv.Stdin`/`ioEnv.Stdout` or resolved files; check `ctx.Done()`.
  - `go -C u-root test ./cmds/core/dd -v` — passed; covers stream/file copies, block sizes, skip/count, truncation, seek, `/dev/zero`, and conversions.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/dd$' -v` — passed; shell integration covers file copy, skip/count, `/dev/zero`, and piped input.
  - `go run . kabibi -r -c "printf abcde | dd ibs=3 obs=2 status=xfer"` — transferred `abcde` and reported 5 bytes copied.
  - **Fixed during retest:** `ddWithCtx` copied data correctly when `ibs != obs` but failed to increment the byte counter used by transfer status, reporting `0 bytes copied`. The regression test now checks byte counts with both input blocks larger and smaller than output blocks.
- [x] **`wget`** (`cmds/core/wget`): HTTP/HTTPS download using `http.NewRequestWithContext(ctx, ...)`; write output to resolved path or `ioEnv.Stdout`.
  - `go -C u-root test ./pkg/curl ./cmds/core/wget -v` — passed; covers HTTP/HTTPS scheme handling, output paths/stdout, redirects, error statuses, and closing downloaded readers.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/wget$' -v` — passed; covers file output, stdout output, and HTTP 404 handling.
  - `go run . kabibi -r -c "wget -O - https://example.com"` — passed on Rusticated; fetched and printed the Example Domain response.
  - **Fixed during retest:** `curl.FetchWithoutCache` wrapped HTTP response bodies and local-file readers in a reader-only wrapper, hiding their `io.Closer`. The wrapper now preserves close behavior, and `wget` closes the fetched reader and reports close errors.
- [ ] **`netcat`** (`cmds/core/netcat`): Also registered as `nc`; TCP/UDP/Unix stream client and listener; replace `log.Fatalf` in parameter evaluation; bind `ioEnv.Stdin`/`ioEnv.Stdout` to connection; cancel on `ctx.Done()`.
  - `go -C u-root test ./cmds/core/netcat` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/netcat$' -v` — passed; host-backed client and listener transfer tests passed.
  - `go -C u-root test ./cmds/core/netcat -run 'TestListenMode|TestParseRemoteAddr' -v` — passed; includes context cancellation and empty-peer-address coverage.
  - `go run . kabibi -r -c "timeout 3s netcat -l 127.0.0.1 48763 & sleep 100ms; printf 'netcat_probe\n' | netcat -w 1 127.0.0.1 48763; wait"` — exceeded the 15-second process timeout; its process tree was terminated. No output was captured.
  - `go run . kabibi -r -c "timeout 2s netcat -l 127.0.0.1 48764"` — exceeded the 60-second process timeout; its process tree was terminated. No output was captured.
  - **Fixed during retest:** `parseRemoteAddr` now skips reverse lookup when the peer address is empty, avoiding the observed `lookup : unrecognized address` warning. Listener shutdown is now tied to `ctx.Done()` and its native cancellation test passes, but the Rusticated listener still does not terminate in the bounded probes; WASI accept/cancellation behavior remains unresolved, so this item stays unchecked.
- [ ] **`scp`** (`cmds/core/scp`): SCP source/sink protocol over `ioEnv.Stdin`/`ioEnv.Stdout`.
  - `go -C u-root test ./cmds/core/scp` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/scp$' -v` — passed; validation and bidirectional in-memory source/sink transfer passed.
  - Rusticated end-to-end source/sink transfer was not verified.
- [ ] **`sshd`** (`cmds/core/sshd`): SSH server daemon using `golang.org/x/crypto/ssh`; bind listener with `ctx` cancellation; on Windows/wasip1 without pty exec, support key auth and clean session error reporting.
  - `go -C u-root test ./cmds/core/sshd` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/sshd$' -v` — passed; host-backed key-authenticated session and context shutdown passed.
  - `go run . kabibi -r -c "sshd -keys nonexistent_keys"` — exited 1 with `sshd: SSH daemon is not supported on wasip1`; Rusticated SSH serving is not implemented.
- [x] **`gosh`** (`cmds/core/gosh`): Nested `mvdan.cc/sh/v3/interp` shell execution bound to `ioEnv` and `ctx`.
  - `go -C u-root test ./cmds/core/gosh` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/gosh$' -v` — passed; command strings, environment expansion, script files, and exit status propagation passed.
  - `go run . kabibi -r -c "gosh -c 'echo nested_shell_ok'"` — passed on Rusticated; printed `nested_shell_ok`.
- [x] **`nohup`** (`cmds/core/nohup`): Ignore `SIGHUP` and redirect output to `nohup.out` (via `ioEnv.ResolvePath`) before running target command.
  - `go -C u-root test ./cmds/core/nohup` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/nohup$' -v` — passed; missing-command status, output, and child exit status passed.
  - `go run . kabibi -r -c "nohup echo nohup_ok"` — passed on Rusticated; printed `nohup_ok`.
- [ ] **`ufs`** (`cmds/core/ufs`): 9P2000 file server over TCP listener; respect `ctx.Done()` to shut down listener cleanly.
  - `go -C u-root test ./cmds/core/ufs` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band2_DataTransferAndNetwork/ufs$' -v` — passed; host-backed 9P file transfer and context shutdown passed.
  - `go run . kabibi -r -c "ufs -help"` — exited 1 with `ufs: 9P file server is not supported on wasip1`; Rusticated serving is not implemented.

---

### Band 3: System Diagnostics, Process & Network Inspection (20 Commands)
*Requires OS-specific implementations: Linux (`/proc`, `/sys`, `AF_NETLINK`), macOS (`sysctl`, Mach VM, IOKit via `purego`), Windows (`kernel32.dll`, `iphlpapi.dll`), and Rusticated.*

- [ ] **`uname`** (`cmds/core/uname`):
  * **Linux / macOS**: `unix.Uname`.
  * **Windows**: Query OS version/arch via `kernel32.dll` (`GetVersionExW` / `GetNativeSystemInfo`) or runtime info.
  * **Rusticated**: Query `syscall.GetPlatformInfo()` (`AbiPlatformInfo`) to report real host/guest details.
  - `go -C u-root test ./cmds/core/uname -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/uname$' -v` — passed; default, `-a`, individual/combined flags, help, and invalid-flag behavior passed.
  - `go run . kabibi -r -c "uname -a"` — passed on Rusticated; printed `Windows_NT rusticated unknown washmhost 0.8.2 aarch64`.
  - **Fixed during retest:** Rusticated uname now reads host OS and architecture plus Rusticated runtime metadata from `syscall.GetPlatformInfo()` rather than reporting `wasip1/wasm`. The ABI currently supplies no host OS version, so the release field is honestly reported as `unknown`; this checklist item remains unchecked pending host-version support.
- [ ] **`id`** (`cmds/core/id`):
  * **Linux / macOS**: `os/user` + `syscall.Getuid`/`Getgid`/`Getgroups`.
  * **Windows / Rusticated**: Use `os/user.Current()` and map SID/username cleanly.
  - `go -C u-root test ./cmds/core/id -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/id$' -v` — passed; default, numeric/name UID, GID, group list, conflicting flags, and help passed on the host.
  - `go run . kabibi -r -c "id; id -u; id -un; id -g; id -G"` — failed on Rusticated; each invocation printed `id: id: unknown user or uid:` and the overall exit code was 1.
  - Rusticated checks found `/etc/passwd` and `/etc/group` unreadable. `id` falls back to `os/user.Current()`, which did not resolve the current account; the command does not use shell-provided user environment values as an identity source. Accurate numeric UID/GID mapping needs a Rusticated identity source, so this item remains unchecked.
- [ ] **`df`** (`cmds/core/df`):
  * **Linux**: Parse `/proc/mounts` + `unix.Statfs`.
  * **macOS**: `unix.Getfsstat` + `unix.Statfs`.
  * **Windows**: Enumerate logical drives (`GetLogicalDriveStringsW`) + `GetDiskFreeSpaceExW` in `kernel32.dll`.
  * **Rusticated**: Host `/proc/mounts` when on Linux; fallback to stat on working directory.
  - `go -C u-root test ./cmds/core/df -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/df$' -v` — passed on the host; default, MB header, directory argument, mutually exclusive units, and help passed.
  - `go run . kabibi -r -c "df; df -m; df .; df -k -m"` — Rusticated printed the synthetic `/dev/root` row for default, `-m`, and `.`; conflicting `-k -m` exited 1 as expected.
  - **Finding:** the `wasip1` fallback uses fixed synthetic capacity values and its device-number helper returns the same value for every path; `df -m` consequently prints unchanged KB-sized numbers under the `1M-blocks` header, and a path-specific result is not based on filesystem capacity. This item remains unchecked.
- [ ] **`free`** (`cmds/core/free`):
  * **Linux / Rusticated+Linux**: Parse `/proc/meminfo`.
  * **macOS**: Query `sysctl("hw.memsize")` and `host_statistics64` (`vm_statistics64`) via `purego`.
  * **Windows**: Call `GlobalMemoryStatusEx` in `kernel32.dll`.
  - `go -C u-root test ./cmds/core/free -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/free$' -v` — passed on the host; default, MB, human-readable, JSON, conflicting-unit, and help behavior passed.
  - `go run . kabibi -r -c "free; free -m; free -json"` — exited 0 on Rusticated, but returned fixed emulated values (1 GiB total, 512 MiB used/free, no swap) rather than host memory statistics; this item remains unchecked.
- [ ] **`uptime`** (`cmds/core/uptime`):
  * **Linux / Rusticated+Linux**: Parse `/proc/uptime` and `/proc/loadavg`.
  * **macOS**: Query `sysctl("kern.boottime")` and `sysctl("vm.loadavg")` via `purego`.
  * **Windows**: Call `GetTickCount64` in `kernel32.dll`.
  - `go -C u-root test ./cmds/core/uptime -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/uptime$' -v` — passed on the host; output and flag behavior passed.
  - `go run . kabibi -r -c "uptime"` — exited 0 on Rusticated and printed `23:32:05 up 0 days, 1 hours, 0 min, loadaverage: 0.00, 0.00, 0.00`.
  - **Finding:** the non-Linux fallback returns a hard-coded one-hour uptime and zero load averages when `/proc` is unavailable; this is not live Windows/Rusticated data, so the item remains unchecked.
- [ ] **`ps`** (`cmds/core/ps`):
  * **Linux / Rusticated+Linux**: Walk `/proc/<pid>/stat` and `/proc/<pid>/status`.
  * **macOS**: Query `sysctl` (`CTL_KERN`, `KERN_PROC`, `KERN_PROC_ALL`) via `purego`.
  * **Windows**: Call `CreateToolhelp32Snapshot` / `Process32FirstW` / `Process32NextW` in `kernel32.dll`.
  - `go -C u-root test ./cmds/core/ps -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/ps$' -v` — passed on the host; default, `aux`, `-A`, `-x`, and help behavior passed.
  - `go run . kabibi -r -c "ps; ps aux; ps -A"` — exited 0 on Rusticated, but each mode listed only the synthetic PID 3 `rusticated` process; host process enumeration is not implemented in the non-Windows/non-Linux fallback, so this item remains unchecked.
- [ ] **`pidof`** (`cmds/core/pidof`):
  * Shares the cross-platform process enumeration backend with `ps` (Linux `/proc`, macOS `sysctl`, Windows Toolhelp32).
  - `go -C u-root test ./cmds/core/pidof -v` — passed; includes fallback-name coverage.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/pidof$' -v` — passed on the host; current-process lookup, `-s`, missing-process status, and help passed.
  - `go run . kabibi -r -c "pidof rusticated"` — passed on Rusticated after aligning its fallback process name with `ps`; printed PID `3`.
  - `go run . kabibi -r -c "pidof nonexistentprogram12345"` — exited 1 as expected.
  - **Fixed during retest:** when `/proc` is absent, Rusticated `pidof` now uses the same synthetic `rusticated` process name as `ps` instead of the WASM executable argument. It still reports only that synthetic guest process rather than host processes; this item remains unchecked.
- [ ] **`kill`** (`cmds/core/kill`):
  * **Linux / macOS**: `syscall.Kill(pid, sig)`.
  * **Windows**: `OpenProcess` + `TerminateProcess` via `os.FindProcess(pid).Kill()`.
  * **Rusticated**: Route to `process_signal` ABI when targeting child handles, or host kill on Unix.
  - `go -C kabibi test ./ui/shell -run 'TestKillIsRoutedAsCommand|TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/kill' -v` — passed; confirms mvdan/sh no longer classifies `kill` as a builtin and shell `kill -l` reaches u-root.
  - `go run . kabibi -r -c "kill -l"` — passed on Rusticated; printed the u-root signal list.
  - No process was signalled in these probes; actual process signalling remains unverified.
- [ ] **`sync`** (`cmds/core/sync`): `package synccmd`;
  * **Linux / macOS**: `unix.Sync()` / `unix.Fsync`.
  * **Windows**: `FlushFileBuffers` on specified files (or no-op if no files).
  - `go -C u-root test ./cmds/core/sync -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/sync$' -v` — passed on the host; global sync, an existing file, `-d`, and missing-file failure passed.
  - `go run . kabibi -r -c "sync; echo sync:$?; sync nonexistent_file_xyz_123; echo missing:$?"` — Rusticated completed with exit code 0 and printed `sync:True` and `missing:True`.
  - **Finding:** the unsupported-platform implementation is an unconditional no-op, so even a missing path reports success on Rusticated; this item remains unchecked.
- [x] **`tty`** (`cmds/core/tty`):
  * **Linux / macOS**: Resolve `/dev` device corresponding to stdin's `stat.Ino`/`stat.Dev`.
  * **Windows**: Check console mode on stdin (`CONIN$`) or stdout (`CONOUT$`).
  * **Rusticated**: Check console mode or return `not a tty`.
  - `go -C u-root test ./cmds/core/tty -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/tty$' -v` — passed on the host; non-terminal input, silent mode, and help passed.
  - `go run . kabibi -r -c "tty"` — printed `not a tty` and returned 1 on Rusticated, as expected when stdin is not a terminal.
  - `go run . kabibi -r -c "tty -s"` — emitted no stdout and returned 1, as required for silent non-terminal detection.
- [ ] **`stty`** (`cmds/core/stty`):
  * **Linux / macOS**: `termios` `ioctl` (`TCGETS`/`TCSETS`, `TIOCGWINSZ`).
  * **Windows**: `GetConsoleMode` / `GetConsoleScreenBufferInfo`.
  * **Rusticated**: Fixed size fallback.
  - `go -C u-root test ./cmds/core/stty -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/stty$' -v` — passed on the host; default state, JSON dump, and size output passed.
  - `go run . kabibi -r -c "stty; stty dump; stty size"` — exited 0 on Rusticated, but all three invocations printed the same fixed `rows:24 cols:80 speed:38400` line; `dump` and `size` forms are not implemented by the fallback, so this item remains unchecked.
- [ ] **`ping`** (`cmds/core/ping`):
  * **Linux / macOS**: `golang.org/x/net/icmp` (`ip4:icmp` / `udp4` unprivileged ping socket).
  * **Windows**: Use `IcmpCreateFile` / `IcmpSendEcho` in `iphlpapi.dll` (works without Administrator privileges) or `golang.org/x/net/icmp`.
  * **Rusticated**: Raw/ICMP socket via `washmhost`.
  - `go -C u-root test ./cmds/core/ping -v` — passed; the raw-ping VM test was skipped.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/ping$' -v` — passed on the host; version/help, missing-argument handling, and loopback ping passed.
  - `go run . kabibi -r -c "ping -V"` — passed on Rusticated; printed `u-root ping`.
  - `go run . kabibi -r -c "ping"` — exited 1 with usage as expected.
  - `go run . kabibi -r -c "ping -c 1 127.0.0.1"` — exited 1 with `not implemented on wasip1/wasm`; only loopback was targeted, and WASI ICMP sockets are unavailable, so this item remains unchecked.
- [ ] **`ip`** (`cmds/core/ip`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `cmds/core/ip` implementation using `netlink` (`addr`, `link`, `route`, `neigh`, `rule`, `tunnel`).
  * **macOS**: Interface inspection via `getifaddrs`/`net.Interfaces()`, routing via `AF_ROUTE` / `sysctl(NET_RT_DUMP)`.
  * **Windows**: Interface & address inspection via `GetAdaptersAddresses` (`net.Interfaces()`) and `GetIpForwardTable` (`iphlpapi.dll`); state mutations fail gracefully.
  - `go -C u-root test ./cmds/core/ip -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/ip$' -v` — passed on the host; help, link, address, route, and invalid-object handling passed.
  - `go run . kabibi -r -c "ip link"`, `go run . kabibi -r -c "ip addr"`, and `go run . kabibi -r -c "ip route"` — all exited 0 on Rusticated but printed no interfaces, addresses, or routes; WASI `net.Interfaces()` provides no host network inventory, so this item remains unchecked.
- [ ] **`netstat`** (`cmds/core/netstat`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `cmds/core/netstat` reading `/proc/net/{tcp,tcp6,udp,udp6,unix,route}`.
  * **macOS**: Query `sysctl` (`net.inet.tcp.pcblist`, `net.inet.udp.pcblist`, `NET_RT_DUMP`).
  * **Windows**: Query `GetExtendedTcpTable` and `GetExtendedUdpTable` in `iphlpapi.dll`.
  - `go -C u-root test ./cmds/core/netstat -v` — passed; Windows netstat tests ran.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/netstat$' -v` — passed on the host; help, interface, route, and socket-table cases passed.
  - `go run . kabibi -r -c "netstat -i"`, `go run . kabibi -r -c "netstat -r"`, and `go run . kabibi -r -c "netstat -a"` — all exited 0 on Rusticated but printed headers only; no interface, route, or socket rows were available, and `-a` silently fell through to the interface view. This item remains unchecked.
- [ ] **`pci`** (`cmds/core/pci`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `cmds/core/pci` walking `/sys/bus/pci/devices` and decoding config space.
  * **macOS**: Enumerate `IOPCIDevice` entries via Apple `IOKit` framework (`purego`).
  * **Windows**: Enumerate `PCI\*` devices via `SetupDiGetClassDevsW` (`setupapi.dll`) or fail gracefully if unavailable.
  - `go -C u-root test ./cmds/core/pci -v` — passed on Windows; enumerated PCI devices, including the NVMe controller and Qualcomm Wi-Fi adapter.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/pci$' -v` — passed on the host.
  - `go run . kabibi -r -c "pci -h"` — passed on Rusticated and printed usage.
  - `go run . kabibi -r -c "pci"` — exited 1 with `pci: not available on this platform`; this is an explicit WASI unsupported fallback, not PCI enumeration, so the item remains unchecked.
- [ ] **`lsmod`** (`cmds/core/lsmod`):
  * **Linux / Rusticated+Linux**: Read and format `/proc/modules`.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
  - `go -C u-root test ./cmds/core/lsmod -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/lsmod$' -v` — passed on Windows; `lsmod` exited 1 with an explicit `/proc/modules` open error.
  - `go run . kabibi -r -c "lsmod -help"` — passed on Rusticated.
  - `go run . kabibi -r -c "lsmod"` — exited 1 with `open /proc/modules: No such file or directory`; Rusticated does not expose the module list, so this item remains unchecked.
- [ ] **`lsdrivers`** (`cmds/core/lsdrivers`):
  * **Linux / Rusticated+Linux**: Authentic `/sys/bus` driver inspection.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
  - `go -C u-root test ./cmds/core/lsdrivers -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/lsdrivers$' -v` — passed on Windows; command reports `/sys/bus` is unavailable.
  - `go run . kabibi -r -c "lsdrivers -help"` — passed on Rusticated.
  - `go run . kabibi -r -c "lsdrivers"` — exited 1 with `open /sys/bus: No such file or directory`; no driver inventory is available, so this item remains unchecked.
- [ ] **`lsfd`** (`cmds/core/lsfd`):
  * **Linux / Rusticated+Linux**: Authentic `/proc/<pid>/fd` walk.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
  - `go -C u-root test ./cmds/core/lsfd -v` — passed on Windows; Linux `/proc` enumeration was skipped.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/lsfd$' -v` — passed on Windows; command reports `/proc` is unavailable.
  - `go run . kabibi -r -c "lsfd -help"` — passed on Rusticated.
  - `go run . kabibi -r -c "lsfd"` — exited 1 with `open /proc: No such file or directory`; no descriptor inventory is available, so this item remains unchecked.
- [ ] **`lddfiles`** (`cmds/core/lddfiles`):
  * Uses Go's portable `debug/elf` to resolve ELF dynamic library dependencies on any OS when given ELF binaries.
  - `go -C u-root test ./cmds/core/lddfiles -v` — passed.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/lddfiles$' -v` — passed on the host; help and missing-file error handling passed.
  - `go run . kabibi -r -c "lddfiles -help"` — passed on Rusticated.
  - `go run . kabibi -r -c "lddfiles go.mod"` — exited 0 and printed the resolved input path, but `go.mod` is not an ELF and this only verifies path handling.
  - `go run . kabibi -r -c "lddfiles definitely_nonexistent_binary_xyz"` — exited 1 with an explicit `stat` error. No valid ELF input was tested, so dependency resolution remains unverified and this item remains unchecked.
- [ ] **`dmesg`** (`cmds/core/dmesg`):
  * **Linux**: `unix.Klogctl` (`SYS_SYSLOG`) with fallback to reading `/dev/kmsg`.
  * **macOS**: Query `sysctl("kern.msgbuf")` via `purego`.
  * **Windows**: Clean unsupported error (`return 1`).
  - `go -C u-root test ./cmds/core/dmesg -v` — passed; the command returned the expected unsupported-platform error on Windows.
  - `go -C kabibi test ./ui/shell -run 'TestURoot_Band3_SystemDiagnosticsProcessAndNetwork/dmesg$' -v` — passed; the shell integration accepts the explicit unsupported result.
  - `go run . kabibi -r -c "dmesg -help"` — passed on Rusticated.
  - `go run . kabibi -r -c "dmesg"` — exited 1 with `dmesg: unsupported on this operating system`; no kernel messages are available, so this item remains unchecked.

---

### Band 4: Kernel, Storage & Boot Administration (36 Commands)
*Authentic implementations retained in full for Linux (and macOS where applicable), with clean non-panicking fallbacks on unsupported hosts per our relaxation policy.*

#### 4A. Filesystem Mounting & Namespaces
- [ ] **`mount`** (`cmds/core/mount`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `mount` (`pkg/mount`, `/proc/mounts`, `unix.Mount`).
  * **macOS / Rusticated+macOS**: List mounts via `getfsstat` and invoke Darwin `mount(2)` via `purego` / `unix.Mount`.
  * **Windows**: Allowed relaxation — clean error (`"mount: not supported on windows"`) and `return 1`.
  - `go -C u-root test ./cmds/core/mount -v` — could not build on Windows: the Linux-only command implementation is excluded, but `mount_test.go` is included and refers to undefined `mountOptions`, `errUsage`, `errMountPath`, `command`, and `cmd`.
  - No focused Kabibi shell integration test for `mount` was found.
  - `go run . kabibi -r -c "mount -help"` — Rusticated invocation returned a natural-language help response rather than command usage, indicating the u-root command was not executed; no mount operation was attempted. A prior probe failed at runtime initialization, but a `true` control probe and the retry succeeded.
  - The read-only `mount` inventory probe was not run because Rusticated command dispatch was not established; this item remains unchecked.
- [ ] **`umount`** (`cmds/core/umount`):
  * **Linux / macOS**: Authentic `unix.Unmount` (`MNT_FORCE`, `MNT_DETACH` / Darwin flags).
  * **Windows**: Clean unsupported error (`return 1`).
- [ ] **`unmount`** (`cmds/core/unmount`): Alias/sibling to `umount`; same platform support.
- [ ] **`bind`** (`cmds/core/bind`): Plan 9 style bind-mount wrapper over `mount`; works on Linux, clean fallback elsewhere.
- [ ] **`mount9p`** (`cmds/core/mount9p`): 9P mount helper; works on Linux, clean fallback elsewhere.
- [ ] **`fusermount`** (`cmds/core/fusermount`): FUSE mount helper; works on Linux, clean fallback elsewhere.
- [ ] **`chroot`** (`cmds/core/chroot`):
  * **Linux / macOS**: `unix.Chroot(ioEnv.ResolvePath(dir))` + command execution.
  * **Windows**: Clean unsupported error (`return 1`).
- [ ] **`unshare`** (`cmds/core/unshare`):
  * **Linux**: Full authentic `unix.Unshare` namespace isolation.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`switch_root`** (`cmds/core/switch_root`): `package switchroot`;
  * **Linux**: Authentic initramfs `switch_root` implementation.
  * **macOS / Windows**: Clean unsupported error (`return 1`).

#### 4B. Block Devices, Partitions & Special Nodes
- [ ] **`blkid`** (`cmds/core/blkid`):
  * Uses `u-root/pkg/mount/block` to inspect filesystem UUIDs/labels. Works natively on Linux `/sys/class/block` and on any OS when passed explicit disk image files.
- [ ] **`gpt`** (`cmds/core/gpt`):
  * Uses `u-root/pkg/boot/gpt` to read/write GUID Partition Tables on block devices or disk image files across all OSes.
- [ ] **`losetup`** (`cmds/core/losetup`):
  * **Linux**: Authentic `/dev/loop-control` and `/dev/loopN` `ioctl` implementation.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`mknod`** (`cmds/core/mknod`):
  * **Linux / macOS**: Authentic `unix.Mknod`.
  * **Windows**: Clean unsupported error (`return 1`).
- [ ] **`mkfifo`** (`cmds/core/mkfifo`):
  * **Linux / macOS**: Authentic `unix.Mkfifo`.
  * **Windows**: Clean unsupported error (`return 1`).

#### 4C. Network Daemons & Link Configuration
- [ ] **`dhclient`** (`cmds/core/dhclient`):
  * **Linux**: Authentic DHCPv4/DHCPv6 client configuring interfaces via netlink.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`brctl`** (`cmds/core/brctl`):
  * **Linux**: Authentic Ethernet bridge administration (`pkg/brctl`).
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`slattach`** (`cmds/core/slattach`):
  * **Linux**: Authentic serial line network interface attachment (`ioctl`).
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`ntpdate`** (`cmds/core/ntpdate`):
  * Query NTP time via `u-root/pkg/ntp` (works on all platforms with `-q` query mode; setting system clock works on privileged Linux/macOS and fails gracefully on Windows).

#### 4D. Kernel Modules, Bootloaders & Power Management
- [ ] **`insmod`** (`cmds/core/insmod`):
  * **Linux**: Authentic `pkg/kmod` (`finit_module` / `init_module`).
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`rmmod`** (`cmds/core/rmmod`):
  * **Linux**: Authentic `pkg/kmod` (`delete_module`).
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`kexec`** (`cmds/core/kexec`):
  * **Linux**: Authentic `kexec` kernel loader.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`reboot`** (`cmds/core/reboot`):
  * **Linux / macOS**: Authentic `unix.Reboot` / `halt` syscall.
  * **Windows**: Clean unsupported error (`return 1`).
- [ ] **`poweroff`** (`cmds/core/poweroff`):
  * **Linux / macOS**: Authentic poweroff via `unix.Reboot`.
  * **Windows**: Clean unsupported error (`return 1`).
- [ ] **`shutdown`** (`cmds/core/shutdown`):
  * **Linux / macOS**: Authentic shutdown (`reboot` / `poweroff` / `halt`).
  * **Windows**: Clean unsupported error (`return 1`).
- [ ] **`init`** (`cmds/core/init`): `package initcmd`; Linux PID 1 initramfs initialization; clean error when not PID 1 or non-Linux.
- [ ] **`sluinit`** (`cmds/core/sluinit`): Sabotage Linux uinit; clean error on non-Linux.
- [ ] **`service`** (`cmds/core/service`): System V `/etc/init.d` script runner; works on Unix, clean error if `/etc/init.d` absent.
- [ ] **`update-rc.d`** (`cmds/core/update-rc.d`): `package updatercd`; SysV init link manager; portable file/symlink logic.

#### 4E. Hardware Registers, Tracing & Watchdogs
- [ ] **`msr`** (`cmds/core/msr`):
  * **Linux (x86)**: Authentic x86 Model-Specific Register reader/writer (`/dev/cpu/*/msr`).
  * **Other**: Clean unsupported error (`return 1`).
- [ ] **`lockmsrs`** (`cmds/core/lockmsrs`):
  * **Linux (x86)**: Authentic Intel MSR lock verification.
  * **Other**: Clean unsupported error (`return 1`).
- [ ] **`io`** (`cmds/core/io`): `package iocmd`;
  * **Linux**: Authentic `/dev/mem` and x86 port I/O (`pkg/memio`) reader/writer.
  * **Other**: Clean unsupported error (`return 1`).
- [ ] **`rsdp`** (`cmds/core/rsdp`):
  * **Linux**: Authentic ACPI RSDP scanner (`pkg/acpi`).
  * **Other**: Clean unsupported error (`return 1`).
- [ ] **`hwclock`** (`cmds/core/hwclock`):
  * **Linux**: Authentic `/dev/rtc` hardware clock reader/writer (`pkg/rtc`).
  * **Other**: Clean unsupported error (`return 1`).
- [ ] **`watchdog`** (`cmds/core/watchdog`):
  * **Linux**: Authentic `/dev/watchdog` pet/control CLI (`pkg/watchdog`).
  * **Other**: Clean unsupported error (`return 1`).
- [ ] **`watchdogd`** (`cmds/core/watchdogd`):
  * **Linux**: Authentic `/dev/watchdog` monitoring daemon (`pkg/watchdogd`).
  * **Other**: Clean unsupported error (`return 1`).
- [ ] **`strace`** (`cmds/core/strace`):
  * **Linux**: Authentic `pkg/strace` (`ptrace` syscall tracer).
  * **Other**: Clean unsupported error (`return 1`).
