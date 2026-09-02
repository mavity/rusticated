package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"mvdan.cc/sh/moreinterp/coreutils"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

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
	cmd := exec.Command(program, args[1:]...)
	cmd.Env = os.Environ()
	if hc.Dir != "" {
		cmd.Dir = hc.Dir
	}
	cmd.Stdin = hc.Stdin
	cmd.Stdout = hc.Stdout
	cmd.Stderr = hc.Stderr
	return cmd.Run()
}

func createRunner(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, dir string, args []string) (*interp.Runner, error) {
	if dir == "" {
		dir, _ = os.Getwd()
	}

	envPairs := make([]string, 0, len(os.Environ())+2)
	envPairs = append(envPairs, os.Environ()...)
	if os.Getenv("PATH") != "" {
		envPairs = append(envPairs, "PATH="+os.Getenv("PATH"))
	}
	if os.Getenv("PATHEXT") != "" {
		envPairs = append(envPairs, "PATHEXT="+os.Getenv("PATHEXT"))
	}

	opts := []interp.RunnerOption{
		interp.Dir(dir),
		interp.Env(expand.ListEnviron(envPairs...)),
		interp.StdIO(stdin, stdout, stderr),
		interp.Params(args...),
	}

	r, err := interp.New(opts...)
	if err != nil {
		return nil, err
	}

	// Custom ExecHandler for recursive script execution, u-root builtins, and
	// platform-aware fallback lookups for host executables such as Windows .bat/.cmd.
	h := func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)

		if len(args) == 0 {
			return nil
		}

		path := args[0]
		// 1. Check if it is a script (heuristically or by looking at it)
		// We try to find it relative to current dir
		absPath := path
		if !filepath.IsAbs(path) {
			absPath = filepath.Join(hc.Dir, path)
		}

		if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
			// Check if it's a shell script (extension or shebang)
			isScript := strings.HasSuffix(path, ".sh")
			if !isScript {
				// Peek for shebang
				f, _ := os.Open(absPath)
				if f != nil {
					buf := make([]byte, 2)
					f.Read(buf)
					f.Close()
					if string(buf) == "#!" {
						isScript = true
					}
				}
			}

			if isScript {
				// RECURSION: Spawn a new runner for the script
				subArgs := args[1:]
				subRunner, err := createRunner(ctx, hc.Stdin, hc.Stdout, hc.Stderr, hc.Dir, subArgs)
				if err != nil {
					return err
				}

				f, err := os.Open(absPath)
				if err != nil {
					return err
				}
				defer f.Close()

				return subRunner.Run(ctx, parseCommandReader(f, path))
			}
		}

		// 2. On Windows-style guest environments, prefer the platform-aware resolver so
		// PATH/PATHEXT and %VAR% expansion work before the interpreter emits the default
		// not-found diagnostic for .bat/.cmd/.exe paths. This still leaves native shell
		// builtins and script execution to the interpreter when our fallback does not apply.
		if resolved, ok := resolvePlatformExecutable(path, hc.Dir, envMapFromHandler(hc)); ok {
			return runResolvedCommand(ctx, hc, resolved, args)
		}

		// 3. Let the interpreter do its normal PATH lookup for builtins and scripts.
		coreHandler := coreutils.ExecHandler(interp.DefaultExecHandler(0))
		if err := coreHandler(ctx, args); err == nil {
			return nil
		}
		return coreHandler(ctx, args)
	}

	interp.ExecHandler(h)(r)
	return r, nil
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

func (m *model) runShellCommand(input string) tea.Cmd {
	return func() tea.Msg {
		parser := syntax.NewParser()
		f, err := parser.Parse(strings.NewReader(input), "")
		if err != nil {
			return shellResultMsg{
				input:  input,
				output: []string{fmt.Sprintf("Parse error: %v", err)},
			}
		}

		var sb strings.Builder
		m.shellOut.SetTarget(&sb)
		defer m.shellOut.SetTarget(nil)

		err = m.runner.Run(context.Background(), f)

		res := shellResultMsg{
			input: input,
			err:   err,
		}

		outputStr := strings.TrimSpace(sb.String())
		if outputStr != "" {
			res.output = strings.Split(outputStr, "\n")
		}

		return res
	}
}
