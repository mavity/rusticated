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

## 5. Master Command-by-Command Checklist (All 119 Commands)

### Band 1: Core Portable Text, File & Shell Utilities (55 Commands)
*100% cross-platform (Linux, macOS, Windows, Rusticated). Pure algorithmic or standard file/stream I/O.*

#### 1A. Pilot Commands (First 3 to Validate Architecture End-to-End)
- [x] **`echo`** (`cmds/core/echo`): Convert `-n`, `-e`, `-E` to local FlagSet; support `\c` early return without `os.Exit`.
- [x] **`cat`** (`cmds/core/cat`): Stream files and `-` (`ioEnv.Stdin`) to `ioEnv.Stdout`; resolve paths via `ioEnv.ResolvePath`.
- [x] **`grep`** (`cmds/core/grep`): Local flags (`-i`, `-v`, `-r`, `-c`, `-n`, `-q`, `-F`, `-h`, `-l`); check `ctx.Done()` in scanner; return `1` on no match.

#### 1B. Text Processing & Filters
- [x] **`head`** (`cmds/core/head`): Local `-c` and `-n` flags; resolve file paths; stream to `ioEnv.Stdout`.
- [x] **`tail`** (`cmds/core/tail`): Local `-c`, `-n`, `-f` flags; monitor `ctx.Done()` in `-f` follow loop.
- [ ] **`sort`** (`cmds/core/sort`): Local `-r`, `-u`, `-f`, `-b`, `-n`, `-C`, `-o` flags; resolve `-o` output file via `ioEnv.ResolvePath`.
- [ ] **`uniq`** (`cmds/core/uniq`): Local `-u`, `-d`, `-c`, `-i`, `-f`, `-s` flags; stream `ioEnv.Stdin` to `ioEnv.Stdout`.
- [ ] **`wc`** (`cmds/core/wc`): Local `-l`, `-w`, `-c`, `-m`, `-L` flags; write counts to `ioEnv.Stdout`.
- [ ] **`tr`** (`cmds/core/tr`): Local `-d`, `-s`, `-c`/`-C` flags; character translation from `ioEnv.Stdin` to `ioEnv.Stdout`.
- [ ] **`tee`** (`cmds/core/tee`): Local `-a`, `-i` flags; multi-writer from `ioEnv.Stdin` to `ioEnv.Stdout` + resolved files.
- [ ] **`seq`** (`cmds/core/seq`): Local `-f`, `-s`, `-w` flags; check `ctx.Done()` in emission loop.
- [ ] **`comm`** (`cmds/core/comm`): Local `-1`, `-2`, `-3` flags; support `-` for `ioEnv.Stdin`.
- [ ] **`cmp`** (`cmds/core/cmp`): Local `-s`, `-l`, `-n` flags; return `0` (equal), `1` (differ), `2` (error).
- [ ] **`strings`** (`cmds/core/strings`): `package stringscmd`; local `-n`, `-t` flags; check `ctx.Done()` while scanning.
- [ ] **`hexdump`** (`cmds/core/hexdump`): Local `-C`, `-n`, `-s`, `-v` flags; stream hex output to `ioEnv.Stdout`.
- [ ] **`ts`** (`cmds/core/ts`): Prepend timestamps to each line from `ioEnv.Stdin` to `ioEnv.Stdout`.
- [ ] **`tsort`** (`cmds/core/tsort`): Topological sort from file or `ioEnv.Stdin` to `ioEnv.Stdout`.
- [ ] **`base64`** (`cmds/core/base64`): Local `-d` flag; encode/decode between `ioEnv.Stdin`/files and `ioEnv.Stdout`.
- [ ] **`yes`** (`cmds/core/yes`): Loop writing to `ioEnv.Stdout`; terminate cleanly on `ctx.Done()` or broken pipe write error.
- [ ] **`more`** (`cmds/core/more`): Pager reading files and `ioEnv.Stdin` to `ioEnv.Stdout`.
- [ ] **`man`** (`cmds/core/man`): Render embedded manual data to `ioEnv.Stdout`.

#### 1C. File & Directory Operations
- [ ] **`ls`** (`cmds/core/ls`): Local `-a`, `-l`, `-h`, `-d`, `-R`, `-F`, `-Q`, `-S` flags; resolve target paths via `ioEnv.ResolvePath`.
- [ ] **`cp`** (`cmds/core/cp`): Local `-r`/`-R`, `-a`, `-f`, `-i`, `-v`, `-P` flags; check `ctx.Done()` across file copies.
- [ ] **`mv`** (`cmds/core/mv`): Local `-f`, `-i`, `-n`, `-u` flags; resolve source and destination paths via `ioEnv.ResolvePath`.
- [ ] **`rm`** (`cmds/core/rm`): Local `-r`/`-R`, `-f`, `-i`, `-v`, `-d` flags; resolve paths via `ioEnv.ResolvePath`.
- [ ] **`mkdir`** (`cmds/core/mkdir`): Local `-p`, `-m`, `-v` flags; create directories relative to `ioEnv.Dir`.
- [ ] **`mktemp`** (`cmds/core/mktemp`): Local `-d`, `-u`, `-q`, `-p`, `-t` flags; fallback temp dir via `ioEnv.Getenv("TMPDIR")`.
- [ ] **`touch`** (`cmds/core/touch`): Local `-a`, `-m`, `-c`, `-d`, `-r`, `-t` flags; resolve paths via `ioEnv.ResolvePath`.
- [ ] **`truncate`** (`cmds/core/truncate`): Local `-s`, `-c` flags; resize target files relative to `ioEnv.Dir`.
- [ ] **`ln`** (`cmds/core/ln`): Local `-s`, `-f`, `-v`, `-n`, `-r` flags; works on Linux, macOS, Windows (symlinks/hardlinks), and Rusticated.
- [ ] **`chmod`** (`cmds/core/chmod`): Local `-R`, `-v`, `-c` flags; support octal and symbolic modes.
- [ ] **`find`** (`cmds/core/find`): Local `-name`, `-type`, `-mode`, `-l`, `-d` flags; respect `ctx.Done()` during walk.
- [ ] **`du`** (`cmds/core/du`): Local `-a`, `-c`, `-d`, `-h`, `-s`, `-L`, `-P` flags; resolve paths relative to `ioEnv.Dir`.
- [ ] **`readlink`** (`cmds/core/readlink`): Local `-f`, `-n`, `-v` flags; evaluate symlinks relative to `ioEnv.Dir`.
- [ ] **`realpath`** (`cmds/core/realpath`): Local `-s`, `-q` flags; resolve canonical path relative to `ioEnv.Dir`.
- [ ] **`basename`** (`cmds/core/basename`): Strip directory and suffix; output to `ioEnv.Stdout`.
- [ ] **`dirname`** (`cmds/core/dirname`): Output parent directory component to `ioEnv.Stdout`.
- [ ] **`pwd`** (`cmds/core/pwd`): Local `-L`, `-P` flags; output `ioEnv.Dir` (or resolved physical path) to `ioEnv.Stdout`.

#### 1D. Checksums, Archival & Compression
- [ ] **`shasum`** (`cmds/core/shasum`): Local `-a` (1, 256, 512), `-b`, `-c`, `-t` flags; check `ctx.Done()` while hashing.
- [ ] **`md5sum`** (`cmds/core/md5sum`): Local `-c`, `-b`, `-t`, `-q`, `-s` flags; check `ctx.Done()` while hashing.
- [ ] **`gzip`** (`cmds/core/gzip`): Also registered as `gzcat`; local `-d`, `-c`, `-f`, `-k`, `-l`, `-q`, `-r`, `-v`, `-1`..`-9` flags.
- [ ] **`tar`** (`cmds/core/tar`): Local `-c`, `-x`, `-t`, `-f`, `-v`, `-z`, `-C` flags; resolve paths via `ioEnv.ResolvePath`.
- [ ] **`cpio`** (`cmds/core/cpio`): Archive in/out/pass-through streaming over `ioEnv.Stdin`/`ioEnv.Stdout`.
- [ ] **`gpgv`** (`cmds/core/gpgv`): OpenPGP signature verification writing results to `ioEnv.Stdout`/`ioEnv.Stderr`.

#### 1E. Shell Builtin & Control Utilities
- [ ] **`true`** (`cmds/core/true`): `package truecmd`; return `0`.
- [ ] **`false`** (`cmds/core/false`): `package falsecmd`; return `1`.
- [ ] **`sleep`** (`cmds/core/sleep`): Wait on `select { case <-time.After(d): return 0; case <-ctx.Done(): return 130 }`.
- [ ] **`which`** (`cmds/core/which`): Local `-a` flag; check `ioEnv.Getenv("PATH")` and registered Kabibi builtins.
- [ ] **`printenv`** (`cmds/core/printenv`): Read from `ioEnv.LookupEnv` / environment; output to `ioEnv.Stdout`.
- [ ] **`date`** (`cmds/core/date`): Local `-u`, `-d`, `-r`, `-I` flags; formatting works everywhere; clock setting (`settimeofday`) supported on privileged Linux/macOS and fails gracefully elsewhere.
- [ ] **`hostname`** (`cmds/core/hostname`): Read hostname via `os.Hostname()` everywhere; setting hostname (`Sethostname`) supported on privileged Unix and fails gracefully elsewhere.
- [ ] **`xargs`** (`cmds/core/xargs`): Local `-n`, `-I`, `-0`, `-t`, `-p`, `-r` flags; dispatch sub-commands through runner callback.
- [ ] **`time`** (`cmds/core/time`): `package timecmd`; measure execution duration of target command and report to `ioEnv.Stderr`.
- [ ] **`timeout`** (`cmds/core/timeout`): Derive `context.WithTimeout(ctx, d)` and execute target command.
- [ ] **`backoff`** (`cmds/core/backoff`): Retry target command with exponential backoff respecting `ctx.Done()`.

---

### Band 2: Data Transfer, Subshells & Network Transport (8 Commands)
*Cross-platform over standard I/O and Rusticated's existing `net_open`/`net_accept`/`read`/`write` ABI.*

- [ ] **`dd`** (`cmds/core/dd`): Convert `if=`, `of=`, `bs=`, `ibs=`, `obs=`, `count=`, `seek=`, `skip=`, `conv=`, `status=` into local FlagSet; replace `usage()` `log.Fatal` with error return; stream between `ioEnv.Stdin`/`ioEnv.Stdout` or resolved files; check `ctx.Done()`.
- [ ] **`wget`** (`cmds/core/wget`): HTTP/HTTPS download using `http.NewRequestWithContext(ctx, ...)`; write output to resolved path or `ioEnv.Stdout`.
- [ ] **`netcat`** (`cmds/core/netcat`): Also registered as `nc`; TCP/UDP/Unix stream client and listener; replace `log.Fatalf` in parameter evaluation; bind `ioEnv.Stdin`/`ioEnv.Stdout` to connection; cancel on `ctx.Done()`.
- [ ] **`scp`** (`cmds/core/scp`): SCP source/sink protocol over `ioEnv.Stdin`/`ioEnv.Stdout`.
- [ ] **`sshd`** (`cmds/core/sshd`): SSH server daemon using `golang.org/x/crypto/ssh`; bind listener with `ctx` cancellation; on Windows/wasip1 without pty exec, support key auth and clean session error reporting.
- [ ] **`gosh`** (`cmds/core/gosh`): Nested `mvdan.cc/sh/v3/interp` shell execution bound to `ioEnv` and `ctx`.
- [ ] **`nohup`** (`cmds/core/nohup`): Ignore `SIGHUP` and redirect output to `nohup.out` (via `ioEnv.ResolvePath`) before running target command.
- [ ] **`ufs`** (`cmds/core/ufs`): 9P2000 file server over TCP listener; respect `ctx.Done()` to shut down listener cleanly.

---

### Band 3: System Diagnostics, Process & Network Inspection (20 Commands)
*Requires OS-specific implementations: Linux (`/proc`, `/sys`, `AF_NETLINK`), macOS (`sysctl`, Mach VM, IOKit via `purego`), Windows (`kernel32.dll`, `iphlpapi.dll`), and Rusticated.*

- [ ] **`uname`** (`cmds/core/uname`):
  * **Linux / macOS**: `unix.Uname`.
  * **Windows**: Query OS version/arch via `kernel32.dll` (`GetVersionExW` / `GetNativeSystemInfo`) or runtime info.
  * **Rusticated**: Query `syscall.GetPlatformInfo()` (`AbiPlatformInfo`) to report real host/guest details.
- [ ] **`id`** (`cmds/core/id`):
  * **Linux / macOS**: `os/user` + `syscall.Getuid`/`Getgid`/`Getgroups`.
  * **Windows / Rusticated**: Use `os/user.Current()` and map SID/username cleanly.
- [ ] **`df`** (`cmds/core/df`):
  * **Linux**: Parse `/proc/mounts` + `unix.Statfs`.
  * **macOS**: `unix.Getfsstat` + `unix.Statfs`.
  * **Windows**: Enumerate logical drives (`GetLogicalDriveStringsW`) + `GetDiskFreeSpaceExW` in `kernel32.dll`.
  * **Rusticated**: Host `/proc/mounts` when on Linux; fallback to stat on working directory.
- [ ] **`free`** (`cmds/core/free`):
  * **Linux / Rusticated+Linux**: Parse `/proc/meminfo`.
  * **macOS**: Query `sysctl("hw.memsize")` and `host_statistics64` (`vm_statistics64`) via `purego`.
  * **Windows**: Call `GlobalMemoryStatusEx` in `kernel32.dll`.
- [ ] **`uptime`** (`cmds/core/uptime`):
  * **Linux / Rusticated+Linux**: Parse `/proc/uptime` and `/proc/loadavg`.
  * **macOS**: Query `sysctl("kern.boottime")` and `sysctl("vm.loadavg")` via `purego`.
  * **Windows**: Call `GetTickCount64` in `kernel32.dll`.
- [ ] **`ps`** (`cmds/core/ps`):
  * **Linux / Rusticated+Linux**: Walk `/proc/<pid>/stat` and `/proc/<pid>/status`.
  * **macOS**: Query `sysctl` (`CTL_KERN`, `KERN_PROC`, `KERN_PROC_ALL`) via `purego`.
  * **Windows**: Call `CreateToolhelp32Snapshot` / `Process32FirstW` / `Process32NextW` in `kernel32.dll`.
- [ ] **`pidof`** (`cmds/core/pidof`):
  * Shares the cross-platform process enumeration backend with `ps` (Linux `/proc`, macOS `sysctl`, Windows Toolhelp32).
- [ ] **`kill`** (`cmds/core/kill`):
  * **Linux / macOS**: `syscall.Kill(pid, sig)`.
  * **Windows**: `OpenProcess` + `TerminateProcess` via `os.FindProcess(pid).Kill()`.
  * **Rusticated**: Route to `process_signal` ABI when targeting child handles, or host kill on Unix.
- [ ] **`sync`** (`cmds/core/sync`): `package synccmd`;
  * **Linux / macOS**: `unix.Sync()` / `unix.Fsync`.
  * **Windows**: `FlushFileBuffers` on specified files (or no-op if no files).
- [ ] **`tty`** (`cmds/core/tty`):
  * **Linux / macOS**: Resolve `/dev` device corresponding to stdin's `stat.Ino`/`stat.Dev`.
  * **Windows**: Check console mode on stdin (`CONIN$`) or stdout (`CONOUT$`).
  * **Rusticated**: Check console mode or return `not a tty`.
- [ ] **`stty`** (`cmds/core/stty`):
  * **Linux / macOS**: `termios` `ioctl` (`TCGETS`/`TCSETS`, `TIOCGWINSZ`).
  * **Windows**: `GetConsoleMode` / `GetConsoleScreenBufferInfo`.
  * **Rusticated**: Fixed size fallback.
- [ ] **`ping`** (`cmds/core/ping`):
  * **Linux / macOS**: `golang.org/x/net/icmp` (`ip4:icmp` / `udp4` unprivileged ping socket).
  * **Windows**: Use `IcmpCreateFile` / `IcmpSendEcho` in `iphlpapi.dll` (works without Administrator privileges) or `golang.org/x/net/icmp`.
  * **Rusticated**: Raw/ICMP socket via `washmhost`.
- [ ] **`ip`** (`cmds/core/ip`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `cmds/core/ip` implementation using `netlink` (`addr`, `link`, `route`, `neigh`, `rule`, `tunnel`).
  * **macOS**: Interface inspection via `getifaddrs`/`net.Interfaces()`, routing via `AF_ROUTE` / `sysctl(NET_RT_DUMP)`.
  * **Windows**: Interface & address inspection via `GetAdaptersAddresses` (`net.Interfaces()`) and `GetIpForwardTable` (`iphlpapi.dll`); state mutations fail gracefully.
- [ ] **`netstat`** (`cmds/core/netstat`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `cmds/core/netstat` reading `/proc/net/{tcp,tcp6,udp,udp6,unix,route}`.
  * **macOS**: Query `sysctl` (`net.inet.tcp.pcblist`, `net.inet.udp.pcblist`, `NET_RT_DUMP`).
  * **Windows**: Query `GetExtendedTcpTable` and `GetExtendedUdpTable` in `iphlpapi.dll`.
- [ ] **`pci`** (`cmds/core/pci`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `cmds/core/pci` walking `/sys/bus/pci/devices` and decoding config space.
  * **macOS**: Enumerate `IOPCIDevice` entries via Apple `IOKit` framework (`purego`).
  * **Windows**: Enumerate `PCI\*` devices via `SetupDiGetClassDevsW` (`setupapi.dll`) or fail gracefully if unavailable.
- [ ] **`lsmod`** (`cmds/core/lsmod`):
  * **Linux / Rusticated+Linux**: Read and format `/proc/modules`.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`lsdrivers`** (`cmds/core/lsdrivers`):
  * **Linux / Rusticated+Linux**: Authentic `/sys/bus` driver inspection.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`lsfd`** (`cmds/core/lsfd`):
  * **Linux / Rusticated+Linux**: Authentic `/proc/<pid>/fd` walk.
  * **macOS / Windows**: Clean unsupported error (`return 1`).
- [ ] **`lddfiles`** (`cmds/core/lddfiles`):
  * Uses Go's portable `debug/elf` to resolve ELF dynamic library dependencies on any OS when given ELF binaries.
- [ ] **`dmesg`** (`cmds/core/dmesg`):
  * **Linux**: `unix.Klogctl` (`SYS_SYSLOG`) with fallback to reading `/dev/kmsg`.
  * **macOS**: Query `sysctl("kern.msgbuf")` via `purego`.
  * **Windows**: Clean unsupported error (`return 1`).

---

### Band 4: Kernel, Storage & Boot Administration (36 Commands)
*Authentic implementations retained in full for Linux (and macOS where applicable), with clean non-panicking fallbacks on unsupported hosts per our relaxation policy.*

#### 4A. Filesystem Mounting & Namespaces
- [ ] **`mount`** (`cmds/core/mount`):
  * **Linux / Rusticated+Linux**: Full authentic u-root `mount` (`pkg/mount`, `/proc/mounts`, `unix.Mount`).
  * **macOS / Rusticated+macOS**: List mounts via `getfsstat` and invoke Darwin `mount(2)` via `purego` / `unix.Mount`.
  * **Windows**: Allowed relaxation — clean error (`"mount: not supported on windows"`) and `return 1`.
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
