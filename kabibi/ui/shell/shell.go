package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"mvdan.cc/sh/moreinterp/coreutils"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// ErrUnknownCommandAtStart is returned when the very first command token at the
// start of a top-level user input line cannot be resolved as a builtin, function,
// virtual shell builtin (bash/sh/kabibi), coreutil, script, or host executable.
// Callers use this sentinel to fall back to AI without printing a shell error.
var ErrUnknownCommandAtStart = errors.New("unknown command at start of input")

type topLevelStartKey struct{}

type topLevelStartInfo struct {
	startOffset uint
}

func withTopLevelStartOffset(ctx context.Context, offset uint) context.Context {
	return context.WithValue(ctx, topLevelStartKey{}, &topLevelStartInfo{startOffset: offset})
}

func withoutTopLevelStart(ctx context.Context) context.Context {
	return context.WithValue(ctx, topLevelStartKey{}, (*topLevelStartInfo)(nil))
}

func isAtTopLevelStart(ctx context.Context, pos syntax.Pos) bool {
	if !pos.IsValid() {
		return false
	}
	info, ok := ctx.Value(topLevelStartKey{}).(*topLevelStartInfo)
	if !ok || info == nil {
		return false
	}
	return pos.Offset() == info.startOffset
}

func isNestedShellBuiltinName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".bat")
	base = strings.TrimSuffix(base, ".cmd")
	switch base {
	case "bash", "sh", "kabibi":
		return true
	default:
		return false
	}
}

// CommandBlockStatus represents the execution state of a command
type CommandBlockStatus int

const (
	StatusActive  CommandBlockStatus = iota // Command is actively running or input is being edited
	StatusSuccess                           // Command completed successfully
	StatusError                             // Command failed with non-zero exit
	StatusFailure                           // Command had an error (e.g., parse error)
)

// CommandBlock represents a single shell interaction: prompt + command + output + status
type CommandBlock struct {
	Prompt   string             // e.g., "$ "
	Input    string             // The command text (e.g., "ls -la")
	Output   bytes.Buffer       // Accumulated output from stdout/stderr
	Status   CommandBlockStatus // Execution status
	ExitCode int                // Exit code (0 = success, non-zero = failure)
}

// shellResultMsg contains the output of a command
type shellResultMsg struct {
	input  string
	output []string
	err    error
}

// ShellRunner is an interface for executing shell commands.
// *interp.Runner implements this interface.
type ShellRunner interface {
	Run(ctx context.Context, f *syntax.File) error
}

func parseCommand(input string) *syntax.File {
	return parseCommandReader(strings.NewReader(input), "")
}

func parseCommandReader(r io.Reader, name string) *syntax.File {
	parser := syntax.NewParser()
	f, err := parser.Parse(r, name)
	if err != nil {
		// Return empty file on error, the runner will handle it or we should
		return &syntax.File{}
	}
	return f
}

type platformExecLookup func(program string, cwd string, env map[string]string) (string, bool)

// platformExecLookupStrategies is intentionally extensible. The shell should keep
// using its native lookup behavior first, then apply platform-specific fallbacks
// such as Windows PATHEXT resolution before eventually expanding to more custom
// resolvers in future builds.
var platformExecLookupStrategies = []platformExecLookup{
	resolveWindowsExecutable,
	// Future platform-specific resolvers can be appended here.
}

func lookupEnvValue(env map[string]string, key string) (string, bool) {
	if env != nil {
		if v, ok := env[key]; ok {
			return v, true
		}
		for k, v := range env {
			if strings.EqualFold(k, key) {
				return v, true
			}
		}
	}
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	for _, pair := range os.Environ() {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func expandWindowsEnvVars(value string, env map[string]string) string {
	if value == "" {
		return value
	}
	for i := 0; i < 8; i++ {
		changed := false
		for start := 0; start < len(value); {
			begin := strings.Index(value[start:], "%")
			if begin < 0 {
				break
			}
			begin += start
			end := strings.Index(value[begin+1:], "%")
			if end < 0 {
				break
			}
			end += begin + 1
			name := value[begin+1 : end]
			if name == "" {
				start = end + 1
				continue
			}
			if replacement, ok := lookupEnvValue(env, name); ok {
				value = value[:begin] + replacement + value[end+1:]
				changed = true
				start = begin + len(replacement)
				continue
			}
			start = end + 1
		}
		if !changed {
			break
		}
	}
	return value
}

func isWindowsLikeEnv(env map[string]string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	if _, ok := lookupEnvValue(env, "PATHEXT"); ok {
		return true
	}
	for _, key := range []string{"GOOS", "GOHOSTOS", "OS"} {
		if value, ok := lookupEnvValue(env, key); ok {
			if strings.EqualFold(value, "windows") || strings.EqualFold(value, "Windows_NT") {
				return true
			}
		}
	}
	return false
}

func resolveWindowsExecutable(program string, cwd string, env map[string]string) (string, bool) {
	if !isWindowsLikeEnv(env) {
		return "", false
	}
	if program == "" {
		return "", false
	}

	pathValue, _ := lookupEnvValue(env, "PATH")
	pathValue = expandWindowsEnvVars(pathValue, env)
	pathextValue, _ := lookupEnvValue(env, "PATHEXT")
	pathextValue = expandWindowsEnvVars(pathextValue, env)
	if pathextValue == "" {
		pathextValue = ".COM;.EXE;.BAT;.CMD"
	}
	env = map[string]string{"PATH": pathValue, "PATHEXT": pathextValue}

	if filepath.IsAbs(program) || strings.ContainsAny(program, `/\\`) {
		if p, err := resolveWindowsCandidate(program, cwd, env); err == nil {
			return p, true
		}
		return "", false
	}

	for _, dir := range filepath.SplitList(env["PATH"]) {
		candidate := program
		if dir != "" {
			candidate = filepath.Join(dir, program)
		}
		if p, err := resolveWindowsCandidate(candidate, cwd, env); err == nil {
			return p, true
		}
	}
	return "", false
}

func resolveWindowsCandidate(candidate, cwd string, env map[string]string) (string, error) {
	if candidate == "" {
		return "", fmt.Errorf("empty candidate")
	}

	if filepath.Ext(candidate) != "" {
		if path, err := checkExecutableFile(candidate, cwd, env, true); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("candidate not executable: %s", candidate)
	}

	pathext, _ := lookupEnvValue(env, "PATHEXT")
	pathext = expandWindowsEnvVars(pathext, env)
	if pathext == "" {
		pathext = ".COM;.EXE;.BAT;.CMD"
	}
	for _, ext := range strings.Split(pathext, ";") {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		if ext[0] != '.' {
			ext = "." + ext
		}
		full := candidate + strings.ToLower(ext)
		if path, err := checkExecutableFile(full, cwd, env, false); err == nil {
			return path, nil
		}
		full = candidate + strings.ToUpper(ext)
		if path, err := checkExecutableFile(full, cwd, env, false); err == nil {
			return path, nil
		}
	}
	if path, err := checkExecutableFile(candidate, cwd, env, false); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("not found")
}

func checkExecutableFile(candidate, cwd string, env map[string]string, requireExecutable bool) (string, error) {
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cwd, candidate)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("is a directory")
	}
	if requireExecutable && !isWindowsLikeEnv(env) && info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("permission denied")
	}
	return candidate, nil
}

func resolvePlatformExecutable(program string, cwd string, env map[string]string) (string, bool) {
	for _, lookup := range platformExecLookupStrategies {
		if resolved, ok := lookup(program, cwd, env); ok {
			return resolved, true
		}
	}
	return "", false
}

func resolvePlatformCommand(program string, cwd string, env map[string]string) (string, bool) {
	return resolvePlatformExecutable(program, cwd, env)
}

func runResolvedCommand(ctx context.Context, hc interp.HandlerContext, program string, args []string) error {
	cmd := exec.CommandContext(ctx, program, args[1:]...)
	envPairs := make([]string, 0, len(os.Environ())+16)
	envPairs = append(envPairs, os.Environ()...)
	if hc.Env != nil {
		hc.Env.Each(func(name string, vr expand.Variable) bool {
			if vr.IsSet() {
				envPairs = append(envPairs, name+"="+vr.String())
			}
			return true
		})
	}
	cmd.Env = envPairs
	if hc.Dir != "" {
		cmd.Dir = hc.Dir
	}
	cmd.Stdin = hc.Stdin
	cmd.Stdout = hc.Stdout
	cmd.Stderr = hc.Stderr
	return cmd.Run()
}

func createRunner(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, dir string, args []string) (*interp.Runner, error) {
	return createRunnerWithEnv(ctx, stdin, stdout, stderr, dir, nil, args)
}

func createRunnerWithEnv(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, dir string, parentEnv expand.Environ, args []string) (*interp.Runner, error) {
	if dir == "" {
		dir, _ = os.Getwd()
	}

	var envPairs []string
	if parentEnv != nil {
		parentEnv.Each(func(name string, vr expand.Variable) bool {
			if vr.IsSet() {
				envPairs = append(envPairs, name+"="+vr.String())
			}
			return true
		})
	} else {
		envPairs = make([]string, 0, len(os.Environ())+2)
		envPairs = append(envPairs, os.Environ()...)
		if os.Getenv("PATH") != "" {
			envPairs = append(envPairs, "PATH="+os.Getenv("PATH"))
		}
		if os.Getenv("PATHEXT") != "" {
			envPairs = append(envPairs, "PATHEXT="+os.Getenv("PATHEXT"))
		}
	}

	opts := []interp.RunnerOption{
		interp.Dir(dir),
		interp.Env(expand.ListEnviron(envPairs...)),
		interp.StdIO(stdin, stdout, stderr),
		interp.Params(args...),
	}

	// 1. Middleware for in-process nested shells (bash, sh, kabibi) and script files (.sh / #!).
	nestedShellAndScriptMiddleware := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return nil
			}
			hc := interp.HandlerCtx(ctx)
			path := args[0]

			// Builtin nested shells: bash, sh, kabibi (when invoked as a command name, not an explicit path to another binary)
			if !strings.ContainsAny(path, `/\`) && isNestedShellBuiltinName(path) {
				return runNestedShellBuiltin(withoutTopLevelStart(ctx), hc, path, args[1:])
			}

			// Check if it is a script relative to current dir or absolute path
			absPath := path
			if !filepath.IsAbs(path) {
				absPath = filepath.Join(hc.Dir, path)
			}

			if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
				isScript := strings.HasSuffix(strings.ToLower(path), ".sh")
				if !isScript {
					f, _ := os.Open(absPath)
					if f != nil {
						buf := make([]byte, 2)
						_, _ = f.Read(buf)
						_ = f.Close()
						if string(buf) == "#!" {
							isScript = true
						}
					}
				}

				if isScript {
					return runNestedScriptFile(withoutTopLevelStart(ctx), hc, absPath, path, nil, args[1:])
				}
			}

			return next(ctx, args)
		}
	}

	// 2. Terminal middleware: platform-aware host executable resolution + PATH lookup.
	// If the command is not found anywhere and is at the very start of the top-level
	// input line, return ErrUnknownCommandAtStart without polluting stderr so the
	// caller can route the line to AI.
	hostExecMiddleware := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return nil
			}
			hc := interp.HandlerCtx(ctx)
			path := args[0]

			if resolved, ok := resolvePlatformExecutable(path, hc.Dir, envMapFromHandler(hc)); ok {
				return mapExecError(runResolvedCommand(ctx, hc, resolved, args), hc)
			}

			if resolved, err := interp.LookPathDir(hc.Dir, hc.Env, path); err == nil {
				return mapExecError(runResolvedCommand(ctx, hc, resolved, args), hc)
			}

			if isAtTopLevelStart(ctx, hc.Pos) {
				return ErrUnknownCommandAtStart
			}

			fmt.Fprintf(hc.Stderr, "%q: executable file not found in $PATH\n", path)
			return interp.ExitStatus(127)
		}
	}

	opts = append(opts, interp.ExecHandlers(
		nestedShellAndScriptMiddleware,
		coreutils.ExecHandler,
		hostExecMiddleware,
	))

	return interp.New(opts...)
}

func mapExecError(err error, hc interp.HandlerContext) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return interp.ExitStatus(uint8(exitErr.ExitCode()))
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		fmt.Fprintf(hc.Stderr, "%v\n", execErr)
		return interp.ExitStatus(127)
	}
	return err
}

func runNestedScriptFile(
	ctx context.Context,
	hc interp.HandlerContext,
	absPath string,
	displayName string,
	shellFlags []string,
	scriptArgs []string,
) error {
	f, err := os.Open(absPath)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "%s: %v\n", displayName, err)
		return interp.ExitStatus(127)
	}
	defer f.Close()

	parser := syntax.NewParser()
	file, err := parser.Parse(f, displayName)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "%s: %v\n", displayName, err)
		return interp.ExitStatus(2)
	}

	params := make([]string, 0, len(shellFlags)+1+len(scriptArgs))
	params = append(params, shellFlags...)
	params = append(params, "--")
	params = append(params, scriptArgs...)

	subRunner, err := createRunnerWithEnv(ctx, hc.Stdin, hc.Stdout, hc.Stderr, hc.Dir, hc.Env, params)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "%s: %v\n", displayName, err)
		return interp.ExitStatus(2)
	}
	return subRunner.Run(ctx, file)
}

func runNestedShellBuiltin(ctx context.Context, hc interp.HandlerContext, shellName string, rawArgs []string) error {
	var (
		shellFlags []string
		cmdStr     string
		hasCmdStr  bool
		readStdin  bool
		rest       []string
	)

	for i := 0; i < len(rawArgs); i++ {
		arg := rawArgs[i]
		if arg == "--" {
			rest = rawArgs[i+1:]
			break
		}
		if arg == "-" {
			rest = rawArgs[i+1:]
			break
		}
		if arg == "-c" || arg == "--command" {
			if i+1 >= len(rawArgs) {
				fmt.Fprintf(hc.Stderr, "%s: %s: option requires an argument\n", shellName, arg)
				return interp.ExitStatus(2)
			}
			cmdStr = rawArgs[i+1]
			hasCmdStr = true
			rest = rawArgs[i+2:]
			break
		}
		if arg == "-s" {
			readStdin = true
			continue
		}
		if arg == "-i" || arg == "-l" || arg == "--login" || arg == "--norc" || arg == "--noprofile" {
			// Accepted for compatibility with bash/sh invocations.
			continue
		}
		if (arg == "-o" || arg == "+o") && i+1 < len(rawArgs) {
			shellFlags = append(shellFlags, arg, rawArgs[i+1])
			i++
			continue
		}
		if len(arg) > 1 && (arg[0] == '-' || arg[0] == '+') {
			// Check if combined short flags ends with 'c' (e.g. -ec 'cmd')
			if arg[0] == '-' && !strings.HasPrefix(arg, "--") && strings.ContainsRune(arg[1:], 'c') {
				otherFlags := strings.ReplaceAll(arg[1:], "c", "")
				if otherFlags != "" {
					shellFlags = append(shellFlags, "-"+otherFlags)
				}
				if i+1 >= len(rawArgs) {
					fmt.Fprintf(hc.Stderr, "%s: -c: option requires an argument\n", shellName)
					return interp.ExitStatus(2)
				}
				cmdStr = rawArgs[i+1]
				hasCmdStr = true
				rest = rawArgs[i+2:]
				break
			}
			shellFlags = append(shellFlags, arg)
			continue
		}
		rest = rawArgs[i:]
		break
	}

	// Case 1: -c <command_string> [command_name [argument ...]]
	if hasCmdStr {
		name := shellName
		var scriptArgs []string
		if len(rest) > 0 {
			name = rest[0]
			scriptArgs = rest[1:]
		}
		parser := syntax.NewParser()
		file, err := parser.Parse(strings.NewReader(cmdStr), name)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "%s: %v\n", name, err)
			return interp.ExitStatus(2)
		}
		params := make([]string, 0, len(shellFlags)+1+len(scriptArgs))
		params = append(params, shellFlags...)
		params = append(params, "--")
		params = append(params, scriptArgs...)

		subRunner, err := createRunnerWithEnv(ctx, hc.Stdin, hc.Stdout, hc.Stderr, hc.Dir, hc.Env, params)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "%s: %v\n", shellName, err)
			return interp.ExitStatus(2)
		}
		return subRunner.Run(ctx, file)
	}

	// Case 2: <script_file> [argument ...] (when -s was not explicitly passed)
	if !readStdin && len(rest) > 0 {
		scriptPath := rest[0]
		absPath := scriptPath
		if !filepath.IsAbs(scriptPath) {
			absPath = filepath.Join(hc.Dir, scriptPath)
		}
		return runNestedScriptFile(ctx, hc, absPath, scriptPath, shellFlags, rest[1:])
	}

	// Case 3: Read commands from hc.Stdin (e.g. `echo "echo hi" | bash`, `sh -s arg1 arg2`, or bare `bash`)
	in := hc.Stdin
	if in == nil {
		return nil
	}
	parser := syntax.NewParser()
	file, err := parser.Parse(in, shellName)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "%s: %v\n", shellName, err)
		return interp.ExitStatus(2)
	}
	params := make([]string, 0, len(shellFlags)+1+len(rest))
	params = append(params, shellFlags...)
	params = append(params, "--")
	params = append(params, rest...)

	subRunner, err := createRunnerWithEnv(ctx, hc.Stdin, hc.Stdout, hc.Stderr, hc.Dir, hc.Env, params)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "%s: %v\n", shellName, err)
		return interp.ExitStatus(2)
	}
	return subRunner.Run(ctx, file)
}

func envMapFromHandler(hc interp.HandlerContext) map[string]string {
	env := map[string]string{}
	for _, pair := range os.Environ() {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	if hc.Env != nil {
		for _, key := range []string{"PATH", "PATHEXT"} {
			v := hc.Env.Get(key)
			if v.IsSet() {
				env[key] = v.String()
			}
		}
	}
	return env
}
