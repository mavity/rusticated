package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/u-root/u-root/cmds/core/backoff"
	base64cmd "github.com/u-root/u-root/cmds/core/base64"
	"github.com/u-root/u-root/cmds/core/basename"
	"github.com/u-root/u-root/cmds/core/cat"
	"github.com/u-root/u-root/cmds/core/chmod"
	"github.com/u-root/u-root/cmds/core/cmp"
	"github.com/u-root/u-root/cmds/core/comm"
	cpcmd "github.com/u-root/u-root/cmds/core/cp"
	cpiocmd "github.com/u-root/u-root/cmds/core/cpio"
	"github.com/u-root/u-root/cmds/core/date"
	ddcmd "github.com/u-root/u-root/cmds/core/dd"
	dfcmd "github.com/u-root/u-root/cmds/core/df"
	"github.com/u-root/u-root/cmds/core/dirname"
	dmesgcmd "github.com/u-root/u-root/cmds/core/dmesg"
	"github.com/u-root/u-root/cmds/core/du"
	falsecmd "github.com/u-root/u-root/cmds/core/false"
	"github.com/u-root/u-root/cmds/core/find"
	freecmd "github.com/u-root/u-root/cmds/core/free"
	goshcmd "github.com/u-root/u-root/cmds/core/gosh"
	"github.com/u-root/u-root/cmds/core/gpgv"
	"github.com/u-root/u-root/cmds/core/grep"
	gzipcmd "github.com/u-root/u-root/cmds/core/gzip"
	"github.com/u-root/u-root/cmds/core/head"
	"github.com/u-root/u-root/cmds/core/hexdump"
	"github.com/u-root/u-root/cmds/core/hostname"
	idcmd "github.com/u-root/u-root/cmds/core/id"
	ipcmd "github.com/u-root/u-root/cmds/core/ip"
	killcmd "github.com/u-root/u-root/cmds/core/kill"
	lddfilescmd "github.com/u-root/u-root/cmds/core/lddfiles"
	"github.com/u-root/u-root/cmds/core/ln"
	lscmd "github.com/u-root/u-root/cmds/core/ls"
	lsdriverscmd "github.com/u-root/u-root/cmds/core/lsdrivers"
	lsfdcmd "github.com/u-root/u-root/cmds/core/lsfd"
	lsmodcmd "github.com/u-root/u-root/cmds/core/lsmod"
	"github.com/u-root/u-root/cmds/core/md5sum"
	"github.com/u-root/u-root/cmds/core/mkdir"
	"github.com/u-root/u-root/cmds/core/mktemp"
	"github.com/u-root/u-root/cmds/core/more"
	"github.com/u-root/u-root/cmds/core/mv"
	netcatcmd "github.com/u-root/u-root/cmds/core/netcat"
	netstatcmd "github.com/u-root/u-root/cmds/core/netstat"
	"github.com/u-root/u-root/cmds/core/nohup"
	pcicmd "github.com/u-root/u-root/cmds/core/pci"
	"github.com/u-root/u-root/cmds/core/pidof"
	pingcmd "github.com/u-root/u-root/cmds/core/ping"
	"github.com/u-root/u-root/cmds/core/printenv"
	pscmd "github.com/u-root/u-root/cmds/core/ps"
	"github.com/u-root/u-root/cmds/core/pwd"
	"github.com/u-root/u-root/cmds/core/readlink"
	"github.com/u-root/u-root/cmds/core/realpath"
	"github.com/u-root/u-root/cmds/core/rm"
	scpcmd "github.com/u-root/u-root/cmds/core/scp"
	sedcmd "github.com/u-root/u-root/cmds/core/sed"
	"github.com/u-root/u-root/cmds/core/seq"
	"github.com/u-root/u-root/cmds/core/shasum"
	"github.com/u-root/u-root/cmds/core/sleep"
	"github.com/u-root/u-root/cmds/core/sort"
	sshdcmd "github.com/u-root/u-root/cmds/core/sshd"
	stringscmd "github.com/u-root/u-root/cmds/core/strings"
	"github.com/u-root/u-root/cmds/core/stty"
	synccmd "github.com/u-root/u-root/cmds/core/sync"
	"github.com/u-root/u-root/cmds/core/tail"
	tarcmd "github.com/u-root/u-root/cmds/core/tar"
	"github.com/u-root/u-root/cmds/core/tee"
	timecmd "github.com/u-root/u-root/cmds/core/time"
	"github.com/u-root/u-root/cmds/core/timeout"
	"github.com/u-root/u-root/cmds/core/touch"
	"github.com/u-root/u-root/cmds/core/tr"
	truecmd "github.com/u-root/u-root/cmds/core/true"
	"github.com/u-root/u-root/cmds/core/truncate"
	"github.com/u-root/u-root/cmds/core/ts"
	"github.com/u-root/u-root/cmds/core/tsort"
	"github.com/u-root/u-root/cmds/core/tty"
	"github.com/u-root/u-root/cmds/core/ufs"
	"github.com/u-root/u-root/cmds/core/uname"
	"github.com/u-root/u-root/cmds/core/uniq"
	uptimecmd "github.com/u-root/u-root/cmds/core/uptime"
	"github.com/u-root/u-root/cmds/core/wc"
	wgetcmd "github.com/u-root/u-root/cmds/core/wget"
	"github.com/u-root/u-root/cmds/core/which"
	"github.com/u-root/u-root/cmds/core/xargs"
	"github.com/u-root/u-root/cmds/core/yes"
	"github.com/u-root/u-root/pkg/core"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type commandRunner func(ctx context.Context, ioEnv core.IO, args ...string) int

var urootRegistry = map[string]commandRunner{
	// Pilot commands (Band 1A)
	"cat":  cat.Main,
	"grep": grep.Main,

	// Band 1B: Text Processing & Filters
	"base64":  base64cmd.Main,
	"cmp":     cmp.Main,
	"comm":    comm.Main,
	"head":    head.Main,
	"hexdump": hexdump.Main,
	"more":    runMore,
	"seq":     seq.Main,
	"sed":     sedcmd.Main,
	"sort":    sort.Main,
	"strings": stringscmd.Main,
	"tail":    tail.Main,
	"tee":     tee.Main,
	"tr":      tr.Main,
	"ts":      ts.Main,
	"tsort":   tsort.Main,
	"uniq":    uniq.Main,
	"wc":      wc.Main,
	"yes":     yes.Main,

	// Band 1C: File & Directory Operations
	"basename": basename.Main,
	"chmod":    chmod.Main,
	"cp":       cpcmd.Main,
	"dirname":  dirname.Main,
	"du":       du.Main,
	"find":     findcmd.Main,
	"ln":       ln.Main,
	"ls":       lscmd.Main,
	"mkdir":    mkdir.Main,
	"mktemp":   mktemp.Main,
	"mv":       mv.Main,
	"pwd":      pwd.Main,
	"readlink": readlink.Main,
	"realpath": realpath.Main,
	"rm":       rm.Main,
	"touch":    touch.Main,
	"truncate": truncate.Main,

	// Band 1D: Checksums, Archival & Compression
	"cpio":   cpiocmd.Main,
	"gpgv":   gpgv.Main,
	"gzcat":  gzipcmd.GzcatMain,
	"gzip":   gzipcmd.Main,
	"md5sum": md5sum.Main,
	"shasum": shasum.Main,
	"tar":    tarcmd.Main,

	// Band 1E: Shell Builtin & Control Utilities
	"backoff":  backoff.Main,
	"date":     date.Main,
	"false":    falsecmd.Main,
	"hostname": hostname.Main,
	"printenv": printenv.Main,
	"sleep":    sleep.Main,
	"time":     timecmd.Main,
	"timeout":  timeout.Main,
	"true":     truecmd.Main,
	"which":    which.Main,
	"xargs":    xargs.Main,

	// Band 2: Data Transfer, Subshells & Network Transport
	"dd":     ddcmd.Main,
	"gosh":   goshcmd.Main,
	"nc":     netcatcmd.Main,
	"netcat": netcatcmd.Main,
	"nohup":  nohup.Main,
	"scp":    scpcmd.Main,
	"sshd":   sshdcmd.Main,
	"ufs":    ufs.Main,
	"wget":   wgetcmd.Main,

	// Band 3: System Diagnostics, Process & Network Inspection
	"df":        dfcmd.Main,
	"dmesg":     dmesgcmd.Main,
	"free":      freecmd.Main,
	"id":        idcmd.Main,
	"ip":        ipcmd.Main,
	"kill":      killcmd.Main,
	"lddfiles":  lddfilescmd.Main,
	"lsdrivers": lsdriverscmd.Main,
	"lsfd":      lsfdcmd.Main,
	"lsmod":     lsmodcmd.Main,
	"netstat":   netstatcmd.Main,
	"pci":       pcicmd.Main,
	"pidof":     pidof.Main,
	"ping":      pingcmd.Main,
	"ps":        pscmd.Main,
	"stty":      stty.Main,
	"sync":      synccmd.Main,
	"tty":       tty.Main,
	"uname":     uname.Main,
	"uptime":    uptimecmd.Main,
}

func runMore(ctx context.Context, ioEnv core.IO, args ...string) int {
	if len(args) > 0 {
		return more.Main(ctx, ioEnv, args...)
	}
	session, source := execSessionFromContext(ctx)
	term := commandTerminalFromContext(ctx)
	if session == nil || source != SourceUserShell || term == nil || !ioEnv.StdoutIsTerminal {
		return more.Main(ctx, ioEnv, args...)
	}
	if err := runPager(ctx, term, ioEnv.Stdin, ioEnv.Stdout); err != nil {
		fmt.Fprintf(ioEnv.Stderr, "more: %v\n", err)
		return 1
	}
	return 0
}

// LookupURootCommand looks up a u-root commandRunner by command name.
func LookupURootCommand(name string) (commandRunner, bool) {
	fn, ok := urootRegistry[name]
	return fn, ok
}

// AllURootCommandNames returns a sorted slice of all registered u-root command names.
func AllURootCommandNames() []string {
	names := make([]string, 0, len(urootRegistry))
	for name := range urootRegistry {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func urootBuiltinMiddleware(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 {
			return nil
		}
		name := args[0]
		if filepath.Base(name) != name {
			return next(ctx, args)
		}
		runner, exists := LookupURootCommand(name)
		if !exists {
			return next(ctx, args)
		}

		hc := interp.HandlerCtx(ctx)
		stdoutIsTerminal := false
		terminalWidth := 0
		stdin := hc.Stdin
		var commandTerminal *CommandTerminal
		if session, source := execSessionFromContext(ctx); session != nil && source == SourceUserShell {
			tty := session.StreamTTY(ctx, hc)
			stdoutIsTerminal = tty.Stdout
			if tty.Stdin {
				commandTerminal = commandTerminalFromContext(ctx)
				if commandTerminal != nil {
					stdin = commandTerminal
				}
			}
			if stdoutIsTerminal {
				if commandTerminal == nil {
					commandTerminal = commandTerminalFromContext(ctx)
				}
				if commandTerminal != nil {
					terminalWidth, _ = commandTerminal.Size()
				}
			}
		}

		var echoRunner *interp.Runner
		var ioEnv core.IO
		ioEnv = core.IO{
			Stdin:            stdin,
			Stdout:           hc.Stdout,
			Stderr:           hc.Stderr,
			Dir:              hc.Dir,
			StdoutIsTerminal: stdoutIsTerminal,
			TerminalWidth:    terminalWidth,
			LookupEnv: func(k string) (string, bool) {
				if hc.Env != nil {
					v := hc.Env.Get(k)
					if v.IsSet() {
						return v.String(), true
					}
				}
				return lookupEnvValue(nil, k)
			},
			Setenv: func(k, v string) error {
				return os.Setenv(k, v)
			},
			Environ: func() []string {
				if hc.Env != nil {
					var envs []string
					hc.Env.Each(func(name string, vr expand.Variable) bool {
						if vr.IsSet() {
							envs = append(envs, name+"="+vr.String())
						}
						return true
					})
					return envs
				}
				return os.Environ()
			},
			Runner: func(runCtx context.Context, subIO core.IO, subArgs ...string) int {
				if len(subArgs) == 0 {
					return 0
				}
				if subArgs[0] == "echo" {
					if echoRunner == nil {
						var err error
						echoRunner, err = createRunnerWithEnv(runCtx, subIO.Stdin, subIO.Stdout, subIO.Stderr, subIO.Dir, hc.Env, nil)
						if err != nil {
							fmt.Fprintf(subIO.Stderr, "echo: failed to initialize shell builtin: %v\n", err)
							return 1
						}
					}

					echoRunner.Reset()
					callArgs := make([]*syntax.Word, 0, len(subArgs))
					for _, arg := range subArgs {
						callArgs = append(callArgs, &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Value: arg}}})
					}
					if err := echoRunner.Run(runCtx, &syntax.CallExpr{Args: callArgs}); err != nil {
						if status, ok := interp.IsExitStatus(err); ok {
							return int(status)
						}
						fmt.Fprintf(subIO.Stderr, "echo: %v\n", err)
						return 1
					}
					return 0
				}
				if r, ok := LookupURootCommand(subArgs[0]); ok {
					return r(runCtx, subIO, subArgs[1:]...)
				}
				if err := next(runCtx, subArgs); err != nil {
					if status, ok := interp.IsExitStatus(err); ok {
						return int(status)
					}
					return 1
				}
				return 0
			},
			IsBuiltin: func(cmdName string) bool {
				if cmdName == "echo" {
					return true
				}
				_, ok := LookupURootCommand(cmdName)
				return ok
			},
		}

		exitCode := runner(ctx, ioEnv, args[1:]...)
		if exitCode == 0 {
			return nil
		}
		return interp.ExitStatus(uint8(exitCode))
	}
}
