package shell

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	rgcmd "github.com/u-root/u-root/cmds/core/rg"
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

type urootCommand struct {
	runner      commandRunner
	description string
}

var urootRegistry = map[string]urootCommand{
	// Pilot commands (Band 1A)
	"cat":  {cat.Main, "Concatenate files and print them to standard output."},
	"grep": {grep.Main, "Search input for lines matching a pattern."},
	"rg":   {rgcmd.Main, "Search files recursively for a pattern."},

	// Band 1B: Text Processing & Filters
	"base64":  {base64cmd.Main, "Encode or decode data using Base64."},
	"cmp":     {cmp.Main, "Compare two files byte by byte."},
	"comm":    {comm.Main, "Compare sorted files line by line."},
	"head":    {head.Main, "Print the first lines of files."},
	"hexdump": {hexdump.Main, "Display file contents in hexadecimal or other formats."},
	"more":    {runMore, "View text one screen at a time."},
	"seq":     {seq.Main, "Print a sequence of numbers."},
	"sed":     {sedcmd.Main, "Transform text using sed expressions."},
	"sort":    {sort.Main, "Sort lines of text."},
	"strings": {stringscmd.Main, "Extract printable strings from files."},
	"tail":    {tail.Main, "Print the last lines of files."},
	"tee":     {tee.Main, "Copy standard input to files and standard output."},
	"tr":      {tr.Main, "Translate or delete characters from standard input."},
	"ts":      {ts.Main, "Prefix input lines with timestamps."},
	"tsort":   {tsort.Main, "Topologically sort a directed graph from edge pairs."},
	"uniq":    {uniq.Main, "Filter or count adjacent duplicate lines."},
	"wc":      {wc.Main, "Count lines, words, bytes, or characters."},
	"yes":     {yes.Main, "Repeatedly print a string."},

	// Band 1C: File & Directory Operations
	"basename": {basename.Main, "Print the final component of a path."},
	"chmod":    {chmod.Main, "Change file permissions."},
	"cp":       {cpcmd.Main, "Copy files and directories."},
	"dirname":  {dirname.Main, "Print the directory component of a path."},
	"du":       {du.Main, "Estimate disk usage of files and directories."},
	"find":     {findcmd.Main, "Find files in a directory tree."},
	"ln":       {ln.Main, "Create hard or symbolic links."},
	"ls":       {lscmd.Main, "List directory contents."},
	"mkdir":    {mkdir.Main, "Create directories."},
	"mktemp":   {mktemp.Main, "Create a temporary file or directory."},
	"mv":       {mv.Main, "Move or rename files and directories."},
	"pwd":      {pwd.Main, "Print the current working directory."},
	"readlink": {readlink.Main, "Print or resolve a symbolic link."},
	"realpath": {realpath.Main, "Print canonical paths."},
	"rm":       {rm.Main, "Remove files or directories."},
	"touch":    {touch.Main, "Create files or update file timestamps."},
	"truncate": {truncate.Main, "Set a file's size."},

	// Band 1D: Checksums, Archival & Compression
	"cpio":   {cpiocmd.Main, "Archive or extract files in cpio format."},
	"gpgv":   {gpgv.Main, "Verify OpenPGP signatures."},
	"gzcat":  {gzipcmd.GzcatMain, "Decompress gzip data to standard output."},
	"gzip":   {gzipcmd.Main, "Compress or decompress files using gzip."},
	"md5sum": {md5sum.Main, "Compute or verify MD5 checksums."},
	"shasum": {shasum.Main, "Compute or verify SHA checksums."},
	"tar":    {tarcmd.Main, "Create, list, or extract tar archives."},

	// Band 1E: Shell Builtin & Control Utilities
	"backoff":  {backoff.Main, "Retry a command with increasing delays."},
	"date":     {date.Main, "Display or set the system date and time."},
	"false":    {falsecmd.Main, "Exit with a non-zero status."},
	"hostname": {hostname.Main, "Display or set the system host name."},
	"printenv": {printenv.Main, "Print environment variables."},
	"sleep":    {sleep.Main, "Pause for a specified duration."},
	"time":     {timecmd.Main, "Measure a command's execution time."},
	"timeout":  {timeout.Main, "Run a command with a time limit."},
	"true":     {truecmd.Main, "Exit successfully."},
	"which":    {which.Main, "Locate a command in PATH."},
	"xargs":    {xargs.Main, "Build and run commands from standard input."},

	// Band 2: Data Transfer, Subshells & Network Transport
	"dd":     {ddcmd.Main, "Copy and convert data between files or devices."},
	"gosh":   {goshcmd.Main, "Start the Go shell."},
	"nc":     {netcatcmd.Main, "Create network connections and transfer data."},
	"netcat": {netcatcmd.Main, "Create network connections and transfer data."},
	"nohup":  {nohup.Main, "Run a command immune to hangup signals."},
	"scp":    {scpcmd.Main, "Copy files to or from a remote host."},
	"sshd":   {sshdcmd.Main, "Run an SSH server."},
	"ufs":    {ufs.Main, "Run a u-root filesystem server."},
	"wget":   {wgetcmd.Main, "Download files over HTTP or HTTPS."},

	// Band 3: System Diagnostics, Process & Network Inspection
	"df":        {dfcmd.Main, "Report filesystem space usage."},
	"dmesg":     {dmesgcmd.Main, "Display kernel ring-buffer messages."},
	"free":      {freecmd.Main, "Show memory usage."},
	"id":        {idcmd.Main, "Display user and group IDs."},
	"ip":        {ipcmd.Main, "Show or configure network interfaces and routes."},
	"kill":      {killcmd.Main, "Send a signal to processes."},
	"lddfiles":  {lddfilescmd.Main, "List shared libraries required by files."},
	"lsdrivers": {lsdriverscmd.Main, "List system drivers."},
	"lsfd":      {lsfdcmd.Main, "List open file descriptors."},
	"lsmod":     {lsmodcmd.Main, "List loaded kernel modules."},
	"netstat":   {netstatcmd.Main, "Display network connections and statistics."},
	"pci":       {pcicmd.Main, "List PCI devices."},
	"pidof":     {pidof.Main, "Find process IDs by command name."},
	"ping":      {pingcmd.Main, "Send ICMP echo requests to a host."},
	"ps":        {pscmd.Main, "Display process status."},
	"stty":      {stty.Main, "Read or change terminal settings."},
	"sync":      {synccmd.Main, "Flush filesystem buffers to storage."},
	"tty":       {tty.Main, "Print the current terminal device."},
	"uname":     {uname.Main, "Print system information."},
	"uptime":    {uptimecmd.Main, "Show how long the system has been running."},
}

func init() {
	commandSyntaxRegistry["cat"] = func() runtimeFlagSet {
		return standardPathFlagSet(cat.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["base64"] = func() runtimeFlagSet {
		return standardPathFlagSet(base64cmd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["cp"] = func() runtimeFlagSet {
		return pflagPathSet(cpcmd.NewFlagSetForCompletion())
	}
	commandSyntaxRegistry["ls"] = func() runtimeFlagSet {
		return standardPathFlagSet(lscmd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["basename"] = func() runtimeFlagSet {
		return standardFlagSet(basename.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["dirname"] = func() runtimeFlagSet {
		return standardPathFlagSet(dirname.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["yes"] = func() runtimeFlagSet {
		return standardFlagSet(yes.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["sleep"] = func() runtimeFlagSet {
		return standardFlagSet(sleep.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["printenv"] = func() runtimeFlagSet {
		return standardFlagSet(printenv.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["pwd"] = func() runtimeFlagSet {
		return standardFlagSet(pwd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["uptime"] = func() runtimeFlagSet {
		return standardFlagSet(uptimecmd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["head"] = func() runtimeFlagSet {
		return standardPathFlagSet(head.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["tail"] = func() runtimeFlagSet {
		return standardPathFlagSet(tail.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["wc"] = func() runtimeFlagSet {
		return standardPathFlagSet(wc.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["strings"] = func() runtimeFlagSet {
		return standardPathFlagSet(stringscmd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["seq"] = func() runtimeFlagSet {
		return standardFlagSet(seq.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["hexdump"] = func() runtimeFlagSet {
		return standardPathFlagSet(hexdump.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["cmp"] = func() runtimeFlagSet {
		return standardPathFlagSet(cmp.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["comm"] = func() runtimeFlagSet {
		return standardPathFlagSet(comm.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["sort"] = func() runtimeFlagSet {
		return standardPathFlagSet(sort.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["uniq"] = func() runtimeFlagSet {
		return standardPathFlagSet(uniq.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["tr"] = func() runtimeFlagSet {
		return standardFlagSet(tr.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["tee"] = func() runtimeFlagSet {
		return standardPathFlagSet(tee.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["time"] = func() runtimeFlagSet {
		return standardFlagSet(timecmd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["timeout"] = func() runtimeFlagSet {
		return standardFlagSet(timeout.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["which"] = func() runtimeFlagSet {
		return standardFlagSet(which.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["hostname"] = func() runtimeFlagSet {
		return standardFlagSet(hostname.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["readlink"] = func() runtimeFlagSet {
		return standardPathFlagSet(readlink.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["realpath"] = func() runtimeFlagSet {
		return standardPathFlagSet(realpath.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["rm"] = func() runtimeFlagSet {
		return standardPathFlagSet(rm.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["mkdir"] = func() runtimeFlagSet {
		return standardPathFlagSet(mkdir.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["md5sum"] = func() runtimeFlagSet {
		return standardPathFlagSet(md5sum.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["id"] = func() runtimeFlagSet {
		return standardFlagSet(idcmd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["uname"] = func() runtimeFlagSet {
		return standardFlagSet(uname.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["mktemp"] = func() runtimeFlagSet {
		return standardFlagSet(mktemp.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["truncate"] = func() runtimeFlagSet {
		return standardPathFlagSet(truncate.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["touch"] = func() runtimeFlagSet {
		return standardPathFlagSet(touch.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["free"] = func() runtimeFlagSet {
		return standardFlagSet(freecmd.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["pidof"] = func() runtimeFlagSet {
		return standardFlagSet(pidof.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["date"] = func() runtimeFlagSet {
		return standardFlagSet(date.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["ln"] = func() runtimeFlagSet {
		return standardPathFlagSet(ln.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["mv"] = func() runtimeFlagSet {
		return standardPathFlagSet(mv.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["gpgv"] = func() runtimeFlagSet {
		return standardPathFlagSet(gpgv.NewFlagSetForCompletion(), true)
	}
	commandSyntaxRegistry["tar"] = func() runtimeFlagSet {
		return standardPathFlagSet(tarcmd.NewFlagSetForCompletion(), true)
	}
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
	command, ok := urootRegistry[name]
	return command.runner, ok
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
		stdinIsTerminal := false
		terminalWidth := 0
		stdin := hc.Stdin
		var commandTerminal *CommandTerminal
		if session, source := execSessionFromContext(ctx); session != nil && source == SourceUserShell {
			tty := session.StreamTTY(ctx, hc)
			stdoutIsTerminal = tty.Stdout
			stdinIsTerminal = tty.Stdin
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
			StdinIsTerminal:  stdinIsTerminal,
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
			ShellRunner: func(runCtx context.Context, subIO core.IO, command string) (int, error) {
				file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
				if err != nil {
					return 2, err
				}
				shellRunner, err := createRunnerWithEnv(runCtx, subIO.Stdin, subIO.Stdout, subIO.Stderr, subIO.Dir, hc.Env, nil)
				if err != nil {
					return 1, err
				}
				err = shellRunner.Run(runCtx, file)
				if err == nil {
					return 0, nil
				}
				if status, ok := interp.IsExitStatus(err); ok {
					return int(status), nil
				}
				return 1, err
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
