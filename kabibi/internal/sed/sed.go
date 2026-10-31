package sed

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/u-root/u-root/pkg/core"
)

type options struct {
	quiet       bool
	extended    bool
	separate    bool
	nullData    bool
	sandbox     bool
	posix       bool
	unbuffered  bool
	debug       bool
	inPlace     bool
	followLinks bool
	lineLength  int
	backup      string
}

func Main(ctx context.Context, ioEnv core.IO, args ...string) int {
	stdin, stdout, stderr := ioEnv.Stdin, ioEnv.Stdout, ioEnv.Stderr
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	dir := ioEnv.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}

	opts, script, files, err := parseArgs(args, dir)
	if err != nil {
		fmt.Fprintf(stderr, "sed: %v\n", err)
		return 2
	}
	if script == "" && opts.debug {
		fmt.Fprintln(stderr, "sed: empty script")
		return 2
	}
	if script == "\x00HELP" {
		_, _ = io.WriteString(stdout, helpText)
		return 0
	}
	if script == "\x00VERSION" {
		_, _ = io.WriteString(stdout, "sed (kabibi) 1.0\n")
		return 0
	}
	program, err := compileScript(script, opts)
	if err != nil {
		fmt.Fprintf(stderr, "sed: %v\n", err)
		return 1
	}
	if opts.debug {
		if err := dumpProgram(stderr, program); err != nil {
			fmt.Fprintf(stderr, "sed: %v\n", err)
			return 1
		}
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	if opts.inPlace {
		for _, name := range files {
			if name == "-" {
				fmt.Fprintln(stderr, "sed: in-place editing is not supported for standard input")
				return 2
			}
			status, stopped, err := runInPlace(ctx, program, name, dir, opts, ioEnv)
			if err != nil {
				fmt.Fprintf(stderr, "sed: %v\n", err)
				return 1
			}
			if stopped || status != 0 {
				return status
			}
		}
		return 0
	}
	status, _, err := runFiles(ctx, program, files, dir, opts, stdin, stdout, ioEnv)
	if err != nil {
		fmt.Fprintf(stderr, "sed: %v\n", err)
		return 1
	}
	return status
}

const helpText = `Usage: sed [OPTION]... {script-only-if-no-other-script} [input-file]...

Stream editor. Scripts accept BRE by default; -E selects extended regular
expressions. Use -e SCRIPT or -f FILE to add scripts.

  -n, --quiet, --silent  suppress automatic printing
  -e SCRIPT              add SCRIPT
  -f FILE                add commands from FILE
  -E, -r                 use extended regular expressions
  -i[SUFFIX]             edit files in place, optionally keeping backups
  -s, --separate         treat each input file separately
  -z, --null-data        use NUL as the line delimiter
  -l N                    set the line-wrap length for the l command
  -b                       binary mode (ignored on this platform)
      --follow-symlinks   follow symlinks in in-place mode
      --sandbox          disable commands that access files or run commands
      --posix            disable GNU extensions
      --debug            print the compiled script
  -u, --unbuffered       flush output after each cycle
  -h, --help             display this help and exit
      --version          output version information and exit
`

func parseArgs(args []string, dir string) (options, string, []string, error) {
	var opts options
	var scripts []string
	var files []string
	opts.lineLength = 70
	stopFlags := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !stopFlags && arg == "--" {
			stopFlags = true
			continue
		}
		if !stopFlags && arg == "--help" || !stopFlags && arg == "-h" {
			return opts, "\x00HELP", nil, nil
		}
		if !stopFlags && arg == "--version" {
			return opts, "\x00VERSION", nil, nil
		}
		if !stopFlags && arg == "-n" || !stopFlags && arg == "--quiet" || !stopFlags && arg == "--silent" {
			opts.quiet = true
			continue
		}
		if !stopFlags && (arg == "-E" || arg == "-r" || arg == "--regexp-extended") {
			opts.extended = true
			continue
		}
		if !stopFlags && (arg == "-s" || arg == "--separate") {
			opts.separate = true
			continue
		}
		if !stopFlags && (arg == "-z" || arg == "--null-data") {
			opts.nullData = true
			continue
		}
		if !stopFlags && arg == "--zero-terminated" {
			opts.nullData = true
			continue
		}
		if !stopFlags && (arg == "-b" || arg == "--binary") {
			continue
		}
		if !stopFlags && arg == "--follow-symlinks" {
			opts.followLinks = true
			continue
		}
		if !stopFlags && arg == "-l" {
			i++
			if i >= len(args) {
				return opts, "", nil, fmt.Errorf("-l requires a line length")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return opts, "", nil, fmt.Errorf("invalid line length %q", args[i])
			}
			opts.lineLength = n
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "-l") && len(arg) > 2 {
			n, err := strconv.Atoi(arg[2:])
			if err != nil || n < 0 {
				return opts, "", nil, fmt.Errorf("invalid line length %q", arg[2:])
			}
			opts.lineLength = n
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "--line-length=") {
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--line-length="))
			if err != nil || n < 0 {
				return opts, "", nil, fmt.Errorf("invalid line length %q", strings.TrimPrefix(arg, "--line-length="))
			}
			opts.lineLength = n
			continue
		}
		if !stopFlags && arg == "--line-length" {
			i++
			if i >= len(args) {
				return opts, "", nil, fmt.Errorf("--line-length requires an argument")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return opts, "", nil, fmt.Errorf("invalid line length %q", args[i])
			}
			opts.lineLength = n
			continue
		}
		if !stopFlags && (arg == "-u" || arg == "--unbuffered") {
			opts.unbuffered = true
			continue
		}
		if !stopFlags && arg == "--sandbox" {
			opts.sandbox = true
			continue
		}
		if !stopFlags && arg == "--posix" {
			opts.posix = true
			continue
		}
		if !stopFlags && arg == "--debug" {
			opts.debug = true
			continue
		}
		if !stopFlags && (arg == "-e" || arg == "--expression") {
			i++
			if i >= len(args) {
				return opts, "", nil, fmt.Errorf("%s requires an argument", arg)
			}
			scripts = append(scripts, args[i])
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "--expression=") {
			scripts = append(scripts, strings.TrimPrefix(arg, "--expression="))
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "-e") && len(arg) > 2 {
			scripts = append(scripts, arg[2:])
			continue
		}
		if !stopFlags && (arg == "-f" || arg == "--file") {
			i++
			if i >= len(args) {
				return opts, "", nil, fmt.Errorf("%s requires an argument", arg)
			}
			name := args[i]
			if !filepath.IsAbs(name) {
				name = filepath.Join(dir, name)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return opts, "", nil, fmt.Errorf("read script %q: %w", args[i], err)
			}
			scripts = append(scripts, string(data))
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "--file=") {
			value := strings.TrimPrefix(arg, "--file=")
			name := value
			if !filepath.IsAbs(name) {
				name = filepath.Join(dir, name)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return opts, "", nil, fmt.Errorf("read script %q: %w", value, err)
			}
			scripts = append(scripts, string(data))
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "-f") && len(arg) > 2 {
			name := arg[2:]
			if !filepath.IsAbs(name) {
				name = filepath.Join(dir, name)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return opts, "", nil, fmt.Errorf("read script %q: %w", arg[2:], err)
			}
			scripts = append(scripts, string(data))
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && len(arg) > 2 {
			value, handled, err := parseShortCluster(arg, args, &i, &opts, &scripts, dir)
			if err != nil {
				return opts, "", nil, err
			}
			if handled {
				if value != "" {
					scripts = append(scripts, value)
				}
				continue
			}
		}
		if !stopFlags && (arg == "-i" || strings.HasPrefix(arg, "-i")) {
			opts.inPlace = true
			opts.separate = true
			opts.backup = strings.TrimPrefix(arg, "-i")
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "--in-place=") {
			opts.inPlace = true
			opts.separate = true
			opts.backup = strings.TrimPrefix(arg, "--in-place=")
			continue
		}
		if !stopFlags && arg == "--in-place" {
			opts.inPlace = true
			opts.separate = true
			continue
		}
		if !stopFlags && strings.HasPrefix(arg, "-") {
			return opts, "", nil, fmt.Errorf("unknown option %q", arg)
		}
		if len(scripts) == 0 {
			scripts = append(scripts, arg)
			continue
		}
		files = append(files, arg)
	}
	if len(scripts) == 0 {
		return opts, "", files, fmt.Errorf("missing script")
	}
	script := strings.Join(scripts, "\n")
	if strings.HasPrefix(script, "#n") && (len(script) == 2 || script[2] == '\n' || script[2] == '\r') {
		opts.quiet = true
	}
	return opts, script, files, nil
}

func parseShortCluster(arg string, args []string, index *int, opts *options, scripts *[]string, dir string) (string, bool, error) {
	for pos := 1; pos < len(arg); pos++ {
		switch arg[pos] {
		case 'n':
			opts.quiet = true
		case 'E', 'r':
			opts.extended = true
		case 's':
			opts.separate = true
		case 'z':
			opts.nullData = true
		case 'u':
			opts.unbuffered = true
		case 'b':
		case 'i':
			opts.inPlace = true
			opts.separate = true
			opts.backup = arg[pos+1:]
			return "", true, nil
		case 'l':
			value := arg[pos+1:]
			if value == "" {
				*index++
				if *index >= len(args) {
					return "", true, fmt.Errorf("-l requires a line length")
				}
				value = args[*index]
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return "", true, fmt.Errorf("invalid line length %q", value)
			}
			opts.lineLength = n
			return "", true, nil
		case 'e', 'f':
			value := arg[pos+1:]
			if value == "" {
				*index++
				if *index >= len(args) {
					return "", true, fmt.Errorf("-%c requires an argument", arg[pos])
				}
				value = args[*index]
			}
			if arg[pos] == 'e' {
				*scripts = append(*scripts, value)
				return "", true, nil
			}
			name := value
			if !filepath.IsAbs(name) {
				name = filepath.Join(dir, name)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return "", true, fmt.Errorf("read script %q: %w", value, err)
			}
			*scripts = append(*scripts, string(data))
			return "", true, nil
		default:
			return "", false, nil
		}
	}
	return "", true, nil
}
