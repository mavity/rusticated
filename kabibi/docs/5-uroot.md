# Master Plan: Submodule Fork and Full Adoption of u-root in Kabibi

## 1. Executive Summary & Architectural Vision

Kabibi integrates an in-process, bash-like execution environment powered by `mvdan.cc/sh/v3/interp`. Rather than shelling out to external OS binaries via `os/exec` (which fails in WebAssembly/`wasip1`, introduces platform-specific discrepancies between Windows, Linux, and macOS, and incurs heavy process-spawning latency), Kabibi's shell aims to provide a complete, self-contained Unix userland built directly into the binary.

Currently, Kabibi imports `mvdan.cc/sh/moreinterp/coreutils`, which in turn pulls 15 commands from `github.com/u-root/u-root/pkg/core` (`cat`, `chmod`, `cp`, `find`, `gzip`, `ls`, `mkdir`, `mktemp`, `mv`, `rm`, `shasum`, `tar`, `touch`, `xargs`, `base64`). However, over 100 essential commands remain stranded in `github.com/u-root/u-root/cmds/core/*` as standalone `package main` binaries.

Because these standalone commands rely on global flags, direct `os.Exit`/`log.Fatal` terminations, and hardcoded `os.Stdout`/`os.Stdin` streams, they cannot be safely consumed via shallow JIT overlays or regex rewriting without catastrophic runtime panics.

### The Strategy
We will **fork `u-root`**, attach the fork as a Git submodule directly into the Rustica repository workspace, and systematically promote the `cmds/core/*` utilities into reusable, reentrant library packages under `pkg/core/<cmd>` implementing u-root's native `core.Command` interface.

```
+-----------------------------------------------------------------------------------+
|                                 Rustica Repository                                |
|                                                                                   |
|  +-----------------------+              +--------------------------------------+  |
|  |        kabibi/        |              |          u-root/ (Git Submodule)     |  |
|  |                       |              |                                      |  |
|  |  ui/shell/shell.go    |              |  pkg/core/                           |  |
|  |    |                  |              |    +-- cat, ls, cp, mv (Existing)    |  |
|  |    +-- interp.Runner  |              |    +-- grep, sort, uniq, wc (New)    |  |
|  |          |            |              |    +-- head, tail, echo, which (New) |  |
|  |          v            |              |    +-- uname, date, sleep, cmp (New) |  |
|  |    uroot.ExecHandler  |-- replace -->|    +-- ...all 115 core commands      |  |
|  |                       |   directive  |                                      |  |
|  |  go.mod               |              |  cmds/core/ (Thin CLI wrappers)      |  |
|  +-----------------------+              +--------------------------------------+  |
+-----------------------------------------------------------------------------------+
```

---

## 2. Git Submodule & Workspace Wiring

### 2.1 Submodule Attachment
The forked repository (e.g. `https://github.com/mavity/u-root`) is attached at the root of the Rustica repository as `u-root`:

```bash
git submodule add https://github.com/mavity/u-root u-root
git submodule update --init --recursive
```

### 2.2 Go Module Replacement
In `kabibi/go.mod`, redirect the `github.com/u-root/u-root` dependency to the submodule:

```go
replace github.com/u-root/u-root => ../u-root
```

This ensures that:
1. Standard Go tooling (`go -C kabibi run .`, `go -C kabibi test ./...`) compiles directly against the submodule fork without dependency resolution conflicts.
2. The Mohabbat hydration pipeline (`mohabbat/patch_go_deps.go`) recognizes the local submodule and applies custom sysroot/wasip1 tag flips directly.
3. Every refactoring to a command is versioned, committed, and peer-reviewed in Git history with zero ambient drift.

---

## 3. The Target Standard: u-root's `core.Command` Interface

The upstream u-root repository already designed the target interface in [`pkg/core/command.go`](file:///c:/Users/mihai/rustica/u-root/pkg/core/command.go):

```go
package core

import (
	"context"
	"io"
)

type LookupEnvFunc func(string) (string, bool)

type Command interface {
	SetIO(stdin io.Reader, stdout io.Writer, stderr io.Writer)
	SetWorkingDir(workingDir string)
	SetLookupEnv(lookupEnv LookupEnvFunc)
	Run(args ...string) error
	RunContext(ctx context.Context, args ...string) error
}
```

And base state is embedded via [`pkg/core/base.go`](file:///c:/Users/mihai/rustica/u-root/pkg/core/base.go):

```go
type Base struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	WorkingDir string
	LookupEnv  LookupEnvFunc
}
```

### Why Every Command Must Match This Blueprint:
1. **Zero Global Flag Collisions**: Flags are parsed via a per-execution `flag.NewFlagSet(name, flag.ContinueOnError)` allocated inside `RunContext()`. This completely eliminates the `panic: flag redefined: <x>` bug when multiple commands share flag letters (e.g. `-u`, `-n`, `-r`, `-c`).
2. **Reentrancy**: Command execution never mutates package-level variables. Commands can be invoked sequentially or concurrently across multiple shell tabs without state leakage.
3. **Pipeline Streaming**: Shell pipelines (`grep foo | wc -l`) pass connected `io.PipeReader`/`io.PipeWriter` streams directly into `SetIO()`, avoiding OS pipes.
4. **Context Cancellation**: Commands check `ctx.Done()` during file scanning or streaming, enabling instant `Ctrl+C` interrupt handling without process killing.
5. **Virtual Directory Resolution**: Relative paths are resolved against `c.WorkingDir` rather than the process-wide OS directory via `c.ResolvePath(arg)`.

---

## 4. The Universal Refactoring Recipe

For every command `<cmd>` currently residing in `cmds/core/<cmd>/`, the following 8-step recipe transforms it into `pkg/core/<cmd>/`:

### Step 1: Package Placement
Create `pkg/core/<cmd>/<cmd>.go` in `u-root/`. Declare `package <cmd>`.

### Step 2: Command Struct & Constructor
```go
package <cmd>

import (
	"context"
	"flag"
	"fmt"
	"io"
	"github.com/u-root/u-root/pkg/core"
)

type command struct {
	core.Base
}

func New() core.Command {
	c := &command{}
	c.Init()
	return c
}
```

### Step 3: Flag Definitions in Local Scope
Convert all package-level `var _ = flag.Bool(...)` or `init()`-bound flags into a local struct inside `RunContext`:
```go
type flags struct {
	all     bool
	lines   int
	pattern string
}

func (c *command) RunContext(ctx context.Context, args ...string) error {
	var f flags
	fs := flag.NewFlagSet("<cmd>", flag.ContinueOnError)
	fs.SetOutput(c.Stderr)

	fs.BoolVar(&f.all, "a", false, "description")
	fs.IntVar(&f.lines, "n", 10, "number of lines")
	fs.StringVar(&f.pattern, "e", "", "pattern")

	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [options] [args]\n", "<cmd>")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	// proceed to business logic
}
```

### Step 4: Elimination of `os.Exit` and `log.Fatal`
- In `main()` / top-level logic:
  - Replace `os.Exit(0)` with `return nil`.
  - Replace `os.Exit(1)` with `return errors.New("...")` or `return interp.ExitStatus(1)`.
  - Replace `os.Exit(c)` with `return interp.ExitStatus(c)`.
  - Replace `log.Fatal(err)` with `fmt.Fprintln(c.Stderr, err); return err`.
- In helper functions (`usage()`, `fatal()`, loops):
  - Change signatures from `func helper(...)` to `func helper(...) error`.
  - Propagate errors up the call stack to prevent fallthrough into subsequent logic.

### Step 5: I/O Stream Decoupling
- Replace all `os.Stdout` with `c.Stdout`.
- Replace all `os.Stdin` with `c.Stdin`.
- Replace all `os.Stderr` with `c.Stderr`.
- Replace `fmt.Println(...)` with `fmt.Fprintln(c.Stdout, ...)`.
- Replace `log.Printf(...)` with `fmt.Fprintf(c.Stderr, ...)`.

### Step 6: Path & Environment Resolution
- Replace `os.Open(path)` with `os.Open(c.ResolvePath(path))`.
- Replace `os.Getenv(k)` with `c.Getenv(k)` or `c.LookupEnv(k)`.

### Step 7: Context Responsiveness
In any file-processing, line-scanning, or network loop:
```go
select {
case <-ctx.Done():
    return ctx.Err()
default:
}
```

### Step 8: Backward-Compatible CLI Thin Shim
Keep `cmds/core/<cmd>/main.go` as a 5-line forwarder so u-root can still build standalone binaries if needed:
```go
package main

import (
	"context"
	"os"
	cmdpkg "github.com/u-root/u-root/pkg/core/<cmd>"
)

func main() {
	cmd := cmdpkg.New()
	if err := cmd.RunContext(context.Background(), os.Args[1:]...); err != nil {
		os.Exit(1)
	}
}
```

---

## 5. Detailed Command Audit & Implementation Strategy

All 115 commands in `cmds/core/*` are categorized into 5 implementation tiers based on utility, complexity, and system dependencies:

### Tier 1: Core Shell & Text Utilities (Priority 1 — The Daily Drivers)
*These 22 utilities are the lifeblood of interactive shell scripts and day-to-day work.*

| Command | Current File | Key Quirks & Refactoring Specifics |
| :--- | :--- | :--- |
| `grep` | `cmds/core/grep/grep.go` | Already has `run(stdin, stdout, stderr, args)`. Promote to `pkg/core/grep`. Change `flag.ExitOnError` to `flag.ContinueOnError`. Return `interp.ExitStatus(1)` on no match (`errQuiet`). |
| `head` | `cmds/core/head/head.go` | Move `c := flag.Int("c", ...)` out of `main()`. Bind to `fs.IntVar` in `RunContext`. Use `c.ResolvePath` for file targets. |
| `tail` | `cmds/core/tail/tail.go` | Supports `-f` (follow). Must monitor `ctx.Done()` in polling loop so following tails break cleanly on `Ctrl+C`. |
| `sort` | `cmds/core/sort/sort.go` | Strip package-level `var (reverse = flag.Bool("r", ...); unique = flag.Bool("u", ...))`. Move to `flags` struct. Fix `-o` to write to `c.ResolvePath(outputFile)` instead of raw `os.Create`. |
| `uniq` | `cmds/core/uniq/uniq.go` | Strip package-level `var unique = flag.Bool("u", ...)`. Move into local flagset. Handle streaming input from `c.Stdin`. |
| `wc` | `cmds/core/wc/wc.go` | Strip package-level `lines = flag.Bool("l", ...)`, `words = flag.Bool("w", ...)`. Connect output reporting to `c.Stdout`. |
| `echo` | `cmds/core/echo/echo.go` | Strip package-level `noNewline = flag.Bool("n", ...)`. Support `\c` escape without `os.Exit(0)`. |
| `which` | `cmds/core/which/which.go` | Read `PATH` via `c.Getenv("PATH")` instead of `os.Getenv`. Use `c.WorkingDir` for relative path checks. Return exit status 1 if no match found. |
| `uname` | `cmds/core/uname/uname.go` | Move `all = flag.Bool("a", ...)` to local flags. On `wasip1` / non-Unix where `unix.Uname` fails or is unsupported, stub with synthetic `Utsname{Sysname: "wasip1", Machine: "wasm"}`. |
| `date` | `cmds/core/date/date.go` | Remove `init()` mutating `flag.Usage`. Read clock via injected `Clock` interface. Disallow system clock setting on `wasip1` with friendly error. |
| `sleep` | `cmds/core/sleep/sleep.go` | Instead of `time.Sleep(d)`, use `select { case <-time.After(d): case <-ctx.Done(): return ctx.Err() }` for cancellation. |
| `false` | `cmds/core/false/false.go` | Return `interp.ExitStatus(1)` directly. |
| `true` | `cmds/core/true/true.go` | Return `nil` directly. |
| `cmp` | `cmds/core/cmp/cmp.go` | Replace `os.Exit(2)` with `interp.ExitStatus(2)` on difference. Ensure `silent` mode suppresses stdout while returning status. |
| `comm` | `cmds/core/comm/comm.go` | Move flags `-1`, `-2`, `-3` into local flagset. Handle comparing stdin (`-`) with a file. |
| `seq` | `cmds/core/seq/seq.go` | Parse float/int arguments. Stream output to `c.Stdout` with context cancellation checks. |
| `tee` | `cmds/core/tee/tee.go` | Multi-writer forwarding from `c.Stdin` to `c.Stdout` and specified file paths. Support `-a` (append). |
| `tr` | `cmds/core/tr/tr.go` | Stream character translation from `c.Stdin` to `c.Stdout`. |
| `strings` | `cmds/core/strings/strings.go` | Move `-n` (min length) to local flag. Stream string detection from files/stdin. |
| `basename`| `cmds/core/basename/basename.go`| Strip directory and optional suffix; output to `c.Stdout`. |
| `dirname` | `cmds/core/dirname/dirname.go` | Return directory part of path; output to `c.Stdout`. |
| `yes` | `cmds/core/yes/yes.go` | Infinite loop outputting string. Must break on `ctx.Done()` or when `c.Stdout.Write` returns `io.ErrClosedPipe` / `syscall.EPIPE`. |

---

### Tier 2: System, Process & File Inspection Utilities (Priority 2)
*Utilities providing environment inspection, file metrics, and job control.*

| Command | Scope & Refactoring Notes |
| :--- | :--- |
| `df` | Inspects filesystem usage. On Linux uses `unix.Statfs`; on wasip1/Windows provide fallback reporting virtual mount points or memory stats. |
| `du` | Recursive directory size calculation. Must honor `c.ResolvePath` and traverse symlinks according to `-L`/`-H` flags. |
| `free` | Parses `/proc/meminfo`. Provide graceful fallback returning runtime `runtime.MemStats` when `/proc` is absent. |
| `id` | Reports user/group IDs. Bind to `os/user` where available; fallback to synthetic uid=0/gid=0 on wasip1. |
| `kill` | Process signaling. Map PID to OS processes; on wasip1 return error or map to internal shell job IDs. |
| `more` | Pager utility. Must use `c.Stdin`/`c.Stdout` and query terminal height via terminal size helpers. |
| `printenv` | Iterates `c.LookupEnv` or environment slice; outputs `KEY=VALUE`. |
| `ps` | Process list. Read host processes where supported, or list active Kabibi background shell jobs. |
| `pwd` | Returns `c.WorkingDir` directly (accounting for `-L` logical vs `-P` physical resolution). |
| `readlink` | Evaluates symbolic links via `os.Readlink(c.ResolvePath(p))`. |
| `realpath` | Resolves canonical absolute paths using `filepath.Abs` and symlink expansion relative to `c.WorkingDir`. |
| `sync` | Calls `syscall.Sync()` where supported; no-op on platforms lacking sync. |
| `time` | Measures execution duration of child arguments via `interp.Runner`. |
| `timeout` | Runs command with deadline derived from `context.WithTimeout(ctx, duration)`. |
| `truncate` | Resizes files to specified length using `os.Truncate(c.ResolvePath(p), size)`. |
| `ts` | Timestamps each line of `c.Stdin` and writes to `c.Stdout`. |
| `uptime` | Reads system uptime or application session uptime. |
| `hexdump` | Canonical hex/ASCII dump from files or `c.Stdin`. |
| `md5sum` | Cryptographic MD5 checksum computation and verification format (`-c`). |
| `hostname` | Inspects or sets hostname. Read hostname from OS or environment variable `HOSTNAME`. |
| `dmesg` | Kernel ring buffer reader. Stub gracefully on non-root or wasip1 systems. |
| `chroot` | Changes root directory; error on non-privileged environments. |

---

### Tier 3: Archival, Compression & Data Movement (Priority 3)
*File transfer and block data manipulation.*

| Command | Scope & Refactoring Notes |
| :--- | :--- |
| `dd` | Heavy refactoring: rewrite `usage()` (currently calls `log.Fatal`) to return usage error. Decouple `conv=`, `ibs=`, `obs=`, `status=` flags into local flagset. Ensure pipe section readers use `c.Stdin`/`c.Stdout`. |
| `cpio` | Archive creation and extraction. Stream bytes through `c.Stdin`/`c.Stdout`. |
| `netcat` | Network Swiss Army knife. Remove `log.Fatalf` in `evalParams`. Support TCP/UDP client and server modes. Bind `stdin`/`stdout` streams to connection socket. |
| `wget` | HTTP/HTTPS file downloader. Use `net/http` client respecting `ctx`. Stream download progress to `c.Stderr`. |
| `ping` | ICMP/IP echo. Requires raw sockets; on systems without raw socket capabilities, fallback to TCP dial ping or report permission error. |
| `backoff` | Repeats command with exponential backoff until success or timeout. |

---

### Tier 4: Network & Terminal Emulation (Priority 4)
*Terminal control and network stack inspection.*

| Command | Scope & Refactoring Notes |
| :--- | :--- |
| `ip` | Network interface and routing manager. Uses `netlink` on Linux. Provide build-tag gate `ip_linux.go` and fallback stub for wasip1. |
| `netstat` | Displays active network sockets and routing tables. |
| `dhclient` | DHCP client daemon. Non-applicable in browser/wasip1 userland; gate behind `//go:build linux`. |
| `stty` | Terminal mode settings (`termios`). Query/set attributes on `c.Stdin` if it implements `Fd()`. |
| `tty` | Prints the file name of the terminal connected to `c.Stdin`. |
| `gosh` / `elvish` | Interactive subshells; invoke nested `interp.Runner`. |
| `sshd` / `scp` | SSH server and copy tools using `golang.org/x/crypto/ssh`. |

---

### Tier 5: Linux/Kernel/Firmware Admin Tools (Priority 5 — Conditional Shims)
*Tools specifically targeting firmware, bootloaders, and physical machine initialization.*

These 35 utilities (`kexec`, `insmod`, `rmmod`, `lsmod`, `mount`, `umount`, `bind`, `losetup`, `mknod`, `mkfifo`, `pci`, `msr`, `lockmsrs`, `gpt`, `blkid`, `brctl`, `fusermount`, `hwclock`, `io`, `lddfiles`, `lsdrivers`, `lsfd`, `poweroff`, `shutdown`, `switch_root`, `unshare`, `update-rc.d`, `watchdog`, `watchdogd`, `init`) are deeply tied to Linux kernel syscalls (`mount(2)`, `sys_reboot`, `/dev/mem`, `/dev/cpu/*/msr`).

**Strategy for Tier 5**:
1. Implement them in `pkg/core/<cmd>` with Linux build tags (`//go:build linux`).
2. Provide a universal stub for wasip1/Windows/Darwin (`//go:build !linux`):
   ```go
   func (c *command) RunContext(ctx context.Context, args ...string) error {
       return fmt.Errorf("%s: command not supported on %s", "<cmd>", runtime.GOOS)
   }
   ```
This guarantees that **all 115 commands compile cleanly on every platform and wasip1 without linker errors**, while clearly communicating platform limitations to the user.

---

## 6. Wiring into Kabibi's Shell Pipeline

### 6.1 The Central Command Registry
In `kabibi/ui/shell/uroot.go`, establish an extensible registry of all u-root commands:

```go
package shell

import (
	"context"
	"fmt"
	"io"
	"github.com/u-root/u-root/pkg/core"

	// Tier 1 imports
	"github.com/u-root/u-root/pkg/core/cmp"
	"github.com/u-root/u-root/pkg/core/date"
	"github.com/u-root/u-root/pkg/core/echo"
	"github.com/u-root/u-root/pkg/core/falsecmd"
	"github.com/u-root/u-root/pkg/core/grep"
	"github.com/u-root/u-root/pkg/core/head"
	"github.com/u-root/u-root/pkg/core/seq"
	"github.com/u-root/u-root/pkg/core/sleep"
	"github.com/u-root/u-root/pkg/core/sort"
	"github.com/u-root/u-root/pkg/core/tail"
	"github.com/u-root/u-root/pkg/core/tee"
	"github.com/u-root/u-root/pkg/core/tr"
	"github.com/u-root/u-root/pkg/core/truecmd"
	"github.com/u-root/u-root/pkg/core/uname"
	"github.com/u-root/u-root/pkg/core/uniq"
	"github.com/u-root/u-root/pkg/core/wc"
	"github.com/u-root/u-root/pkg/core/which"
	"github.com/u-root/u-root/pkg/core/yes"
	// ... additional tiers
)

type commandFactory func() core.Command

var urootRegistry = map[string]commandFactory{
	"grep":      grep.New,
	"head":      head.New,
	"tail":      tail.New,
	"sort":      sort.New,
	"uniq":      uniq.New,
	"wc":        wc.New,
	"echo":      echo.New,
	"which":     which.New,
	"uname":     uname.New,
	"date":      date.New,
	"sleep":     sleep.New,
	"false":     falsecmd.New,
	"true":      truecmd.New,
	"cmp":       cmp.New,
	"seq":       seq.New,
	"tee":       tee.New,
	"tr":        tr.New,
	"yes":       yes.New,
}

func LookupURootCommand(name string) (commandFactory, bool) {
	fn, ok := urootRegistry[name]
	return fn, ok
}

func AllURootCommandNames() []string {
	names := make([]string, 0, len(urootRegistry))
	for name := range urootRegistry {
		names = append(names, name)
	}
	return names
}
```

### 6.2 ExecHandler Middleware Integration
In `kabibi/ui/shell/shell.go`, wire the u-root execution middleware directly before `hostExecMiddleware`:

```go
urootBuiltinMiddleware := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 {
			return nil
		}
		name := args[0]
		factory, exists := LookupURootCommand(name)
		if !exists {
			return next(ctx, args)
		}

		hc := interp.HandlerCtx(ctx)
		cmd := factory()
		cmd.SetIO(hc.Stdin, hc.Stdout, hc.Stderr)
		cmd.SetWorkingDir(hc.Dir)
		cmd.SetLookupEnv(func(k string) (string, bool) {
			v := hc.Env.Get(k)
			return v.String(), v.IsSet()
		})

		return cmd.RunContext(ctx, args[1:]...)
	}
}

opts = append(opts, interp.ExecHandlers(
	nestedShellAndScriptMiddleware,
	gitBuiltinMiddleware,
	webBuiltinMiddleware,
	coreutils.ExecHandler,       // Existing 15 commands from moreinterp
	urootBuiltinMiddleware,     // All newly ported u-root commands
	hostExecMiddleware,         // Host fallbacks
))
```

---

## 7. Phased Implementation Roadmap

```
  Phase 1: Foundation (Days 1-2)
  [Fork u-root] -> [Attach Submodule] -> [Add go.mod Replace] -> [Registry Scaffold]
        |
        v
  Phase 2: Tier 1 Daily Drivers (Days 3-7)
  [grep, head, tail, sort, uniq, wc, echo, which, uname, date, sleep, cmp, seq, tee, tr, yes]
        |
        v
  Phase 3: Tier 2 Inspection Utilities (Days 8-12)
  [df, du, free, id, kill, more, printenv, ps, pwd, readlink, realpath, sync, time, timeout, truncate]
        |
        v
  Phase 4: Tier 3 Data & Networking (Days 13-16)
  [dd, cpio, netcat, wget, ping, backoff]
        |
        v
  Phase 5: Tier 4 & Tier 5 Kernel Gates & Stubs (Days 17-20)
  [ip, stty, tty, sshd, and wasip1 fallback shims for kexec, mount, etc.]
        |
        v
  Phase 6: Shell Autocompletion & Test Matrix Verification (Days 21-22)
```

### Verification Criteria
1. **Zero Panics**: Every command passes a fuzz test running repeated invocations with overlapping and invalid flags without panic.
2. **Pipe Integrity**: `echo "foo\nbar" | grep foo | wc -l` produces identical output to host GNU/BSD coreutils.
3. **Interrupt Handling**: Running `sleep 100` or `yes` in the shell and pressing `Ctrl+C` immediately cancels the command and restores the prompt.
4. **Wasip1 Build Validation**: Building Kabibi with `GOOS=wasip1 GOARCH=wasm` succeeds without missing symbol errors.
