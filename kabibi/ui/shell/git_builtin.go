package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrianliechti/go-git"
	"github.com/mavity/rusticated/kabibi/gitcore"
	"github.com/mavity/rusticated/platform"
	"mvdan.cc/sh/v3/interp"
)

type gitInvocation struct {
	args         []string
	effectiveDir string
	config       []string
	gitDir       string
	workTree     string
	noPager      bool
}

type gitInvocationParseState struct {
	invocation    gitInvocation
	pendingOption string
}

type gitGlobalOptionKind uint8

const (
	gitOptionChangeDirectory gitGlobalOptionKind = iota
	gitOptionConfig
	gitOptionGitDir
	gitOptionWorkTree
	gitOptionNoPager
	gitOptionPassThrough
)

type gitGlobalOptionSpec struct {
	name         string
	aliases      []string
	kind         gitGlobalOptionKind
	inlinePrefix bool
	inlineEqual  bool
}

var gitGlobalOptionSpecs = []gitGlobalOptionSpec{
	{name: "-C", kind: gitOptionChangeDirectory, inlinePrefix: true},
	{name: "-c", kind: gitOptionConfig, inlinePrefix: true},
	{name: "--git-dir", kind: gitOptionGitDir, inlineEqual: true},
	{name: "--work-tree", kind: gitOptionWorkTree, inlineEqual: true},
	{name: "--no-pager", aliases: []string{"-P"}, kind: gitOptionNoPager},
	{name: "--no-replace-objects", kind: gitOptionPassThrough},
	{name: "--bare", kind: gitOptionPassThrough},
}

const (
	gitHelpOptionLong     = "--help"
	gitHelpOptionShort    = "-h"
	gitVersionOptionLong  = "--version"
	gitVersionOptionShort = "-v"
)

func gitGlobalOptionNames() []string {
	names := make([]string, 0, len(gitGlobalOptionSpecs)+4)
	for _, spec := range gitGlobalOptionSpecs {
		names = append(names, spec.name)
		names = append(names, spec.aliases...)
	}
	names = append(names, gitVersionOptionLong, gitVersionOptionShort, gitHelpOptionLong, gitHelpOptionShort)
	return names
}

func matchGitGlobalOption(arg string) (gitGlobalOptionSpec, string, bool, bool) {
	for _, spec := range gitGlobalOptionSpecs {
		if arg == spec.name {
			return spec, "", false, true
		}
		for _, alias := range spec.aliases {
			if arg == alias {
				return spec, "", false, true
			}
		}
		if spec.inlinePrefix && strings.HasPrefix(arg, spec.name) && len(arg) > len(spec.name) {
			return spec, arg[len(spec.name):], true, true
		}
		if spec.inlineEqual && strings.HasPrefix(arg, spec.name+"=") {
			return spec, strings.TrimPrefix(arg, spec.name+"="), true, true
		}
	}
	return gitGlobalOptionSpec{}, "", false, false
}

func gitBuiltinMiddleware(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 || args[0] != "git" {
			return next(ctx, args)
		}
		return runGitBuiltin(ctx, args[1:])
	}
}

func runGitBuiltin(ctx context.Context, args []string) error {
	hc := interp.HandlerCtx(ctx)
	invocation, err := parseGitInvocation(hc.Dir, args)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git: %v\n", err)
		return interp.ExitStatus(2)
	}
	if len(invocation.args) == 0 {
		writeGitHelp(hc)
		return nil
	}
	if handled, err := runSSHRemoteBuiltin(ctx, hc, invocation); handled {
		return err
	}
	if handled, err := runStreamedGitLog(ctx, hc, invocation); handled {
		return err
	}
	if handled, err := runBufferedGitLogWithPager(ctx, hc, invocation); handled {
		return err
	}
	if handled, err := runBufferedGitDiffWithPager(ctx, hc, invocation); handled {
		return err
	}

	switch invocation.args[0] {
	case gitHelpOptionLong, gitHelpOptionShort:
		if len(invocation.args) == 1 {
			writeGitHelp(hc)
			return nil
		}
	case "replace":
		return runGitReplace(hc, invocation)
	case "notes":
		return runGitNotes(ctx, hc, invocation)
	case "bisect":
		if len(invocation.args) > 1 && invocation.args[1] == "run" {
			return runGitBisectRun(ctx, hc, invocation)
		}
	case gitVersionOptionLong, gitVersionOptionShort:
		if len(invocation.args) == 1 {
			version, err := gitcore.LibraryVersion()
			if err != nil {
				fmt.Fprintf(hc.Stderr, "git --version: %v\n", err)
				return interp.ExitStatus(1)
			}
			fmt.Fprintf(hc.Stdout, "git version %s (go-git)\n", version)
			return nil
		}
	}
	return runGoGit(ctx, hc, invocation)
}

func parseGitInvocation(cwd string, args []string) (gitInvocation, error) {
	state, err := parseGitInvocationState(cwd, args, false)
	return state.invocation, err
}

func parseGitInvocationState(cwd string, args []string, allowIncomplete bool) (gitInvocationParseState, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return gitInvocationParseState{}, fmt.Errorf("resolve current directory: %w", err)
		}
	}
	state := gitInvocationParseState{invocation: gitInvocation{effectiveDir: filepath.Clean(cwd)}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			state.invocation.args = append(state.invocation.args, args[i:]...)
			return state, nil
		}
		spec, value, inline, matched := matchGitGlobalOption(arg)
		if !matched {
			state.invocation.args = append(state.invocation.args, args[i:]...)
			return state, nil
		}
		if spec.kind == gitOptionNoPager {
			state.invocation.noPager = true
			state.invocation.args = append(state.invocation.args, arg)
			continue
		}
		if spec.kind == gitOptionPassThrough {
			state.invocation.args = append(state.invocation.args, arg)
			continue
		}
		if !inline {
			i++
			if i >= len(args) {
				if allowIncomplete {
					state.pendingOption = spec.name
					return state, nil
				}
				switch spec.kind {
				case gitOptionChangeDirectory:
					return gitInvocationParseState{}, fmt.Errorf("-C requires a directory")
				case gitOptionConfig:
					return gitInvocationParseState{}, fmt.Errorf("-c requires a key=value pair")
				default:
					return gitInvocationParseState{}, fmt.Errorf("%s requires a path", spec.name)
				}
			}
			value = args[i]
		}
		switch spec.kind {
		case gitOptionChangeDirectory:
			state.invocation.effectiveDir = resolveShellPath(state.invocation.effectiveDir, value)
		case gitOptionConfig:
			state.invocation.config = append(state.invocation.config, value)
			state.invocation.args = append(state.invocation.args, "-c", value)
		case gitOptionGitDir:
			state.invocation.gitDir = resolveShellPath(state.invocation.effectiveDir, value)
		case gitOptionWorkTree:
			state.invocation.workTree = resolveShellPath(state.invocation.effectiveDir, value)
		}
	}
	return state, nil
}

func buildGitEnv(execSess *ExecutionSession, hc interp.HandlerContext, fsys *osFS, config []string) (map[string]string, error) {
	env := shellInvocationEnvMap(withShellInvocationState(context.Background(), hc), execSess)
	if env["HOME"] == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve Git HOME: %w", err)
		}
		env["HOME"] = home
	}

	for _, key := range []string{"HOME", "GIT_CONFIG_GLOBAL", "GIT_DIR", "GIT_WORK_TREE"} {
		value := env[key]
		if value == "" || !filepath.IsAbs(value) {
			continue
		}
		virtual, err := fsys.toVirtualPath(value)
		if err != nil {
			return nil, fmt.Errorf("map Git %s into its filesystem: %w", key, err)
		}
		env[key] = virtual
	}
	for _, pair := range config {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("Git config override %q must be key=value", pair)
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "user.name":
			if env["GIT_AUTHOR_NAME"] == "" {
				env["GIT_AUTHOR_NAME"] = value
			}
			if env["GIT_COMMITTER_NAME"] == "" {
				env["GIT_COMMITTER_NAME"] = value
			}
		case "user.email":
			if env["GIT_AUTHOR_EMAIL"] == "" {
				env["GIT_AUTHOR_EMAIL"] = value
			}
			if env["GIT_COMMITTER_EMAIL"] == "" {
				env["GIT_COMMITTER_EMAIL"] = value
			}
		}
	}
	if platform.HostOS() == "windows" && env["EMAIL"] == "" {
		env["EMAIL"] = env["GIT_AUTHOR_EMAIL"]
	}
	return env, nil
}

func runGoGit(ctx context.Context, hc interp.HandlerContext, invocation gitInvocation) error {
	fsys, virtualDir, err := newHostFS(invocation.effectiveDir)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git: %v\n", err)
		return interp.ExitStatus(128)
	}
	execSess, _ := execSessionFromContext(ctx)
	env, err := buildGitEnv(execSess, hc, fsys, invocation.config)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git: %v\n", err)
		return interp.ExitStatus(1)
	}
	if invocation.gitDir != "" {
		env["GIT_DIR"], err = fsys.toVirtualPath(invocation.gitDir)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git: %v\n", err)
			return interp.ExitStatus(2)
		}
	}
	if invocation.workTree != "" {
		env["GIT_WORK_TREE"], err = fsys.toVirtualPath(invocation.workTree)
		if err != nil {
			fmt.Fprintf(hc.Stderr, "git: %v\n", err)
			return interp.ExitStatus(2)
		}
	}
	commandArgs, err := prepareGitAuthorArgs(invocation.args, env)
	if err != nil {
		fmt.Fprintf(hc.Stderr, "git: %v\n", err)
		return interp.ExitStatus(2)
	}
	commandArgs = normalizeGitStatusArgs(commandArgs)

	authEnv := make(map[string]string, len(env))
	for key, value := range env {
		authEnv[key] = value
	}
	if env["HOME"] != "" {
		authEnv["HOME"] = fsys.toOSPath(env["HOME"])
	}
	var stderrCapture bytes.Buffer
	code, runErr := git.Run(ctx, git.Options{
		Args:       commandArgs,
		Dir:        virtualDir,
		Env:        env,
		Stdin:      hc.Stdin,
		Stdout:     hc.Stdout,
		Stderr:     io.MultiWriter(hc.Stderr, &stderrCapture),
		FS:         fsys,
		HTTPClient: gitcore.NewAuthenticatedHTTPClient(authEnv),
	})
	if runErr != nil {
		return runErr
	}
	if code != 0 {
		if code == 128 && strings.Contains(stderrCapture.String(), "not a git repository") {
			fmt.Fprintln(hc.Stderr, "hint: git repository not found")
		}
		return interp.ExitStatus(uint8(code))
	}
	return nil
}

func normalizeGitStatusArgs(args []string) []string {
	if len(args) < 2 || args[0] != "status" {
		return args
	}
	short, porcelain, untrackedConfigured := false, false, false
	for _, arg := range args[1:] {
		switch arg {
		case "-s", "--short", "-sb", "-bs":
			short = true
		case "--porcelain", "--porcelain=v1":
			porcelain = true
		case "-u", "-uall", "-uno", "-unormal":
			untrackedConfigured = true
		default:
			if strings.HasPrefix(arg, "--untracked-files=") {
				untrackedConfigured = true
			}
		}
	}
	if !short || (porcelain && untrackedConfigured) {
		return args
	}
	// Preserve repository-rooted paths and individual untracked files in short output.
	normalized := make([]string, 0, len(args)+2)
	normalized = append(normalized, args[0])
	if !porcelain {
		normalized = append(normalized, "--porcelain")
	}
	if !untrackedConfigured {
		normalized = append(normalized, "--untracked-files=all")
	}
	return append(normalized, args[1:]...)
}

func writeGitHelp(hc interp.HandlerContext) {
	fmt.Fprintln(hc.Stdout, `Kabibi built-in Git

Usage:
  git --version
  git --help
  git status
  git add <pathspec>...
  git commit [options]
  git diff [options]
  git log [options]
  git branch [options]
  git checkout [options]
  git switch [options]
  git restore [options]
  git reset [options]
  git merge [options]
  git rebase [options]
  git bisect [options]
  git filter-branch [options]
  git replace [options]
  git notes [options]
  git fetch | pull | push | clone | ls-remote
  git stash | tag | show | reflog | worktree | submodule

Commands are implemented in-process; host Git is not invoked.`)
}

func prepareGitAuthorArgs(args []string, env map[string]string) ([]string, error) {
	if len(args) < 2 || args[0] != "commit" {
		return args, nil
	}
	prepared := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--author" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("commit --author requires a value")
			}
			i++
			arg = args[i]
		} else if strings.HasPrefix(arg, "--author=") {
			arg = strings.TrimPrefix(arg, "--author=")
		} else if arg == "--date" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("commit --date requires a value")
			}
			i++
			env["GIT_AUTHOR_DATE"] = args[i]
			continue
		} else if strings.HasPrefix(arg, "--date=") {
			env["GIT_AUTHOR_DATE"] = strings.TrimPrefix(arg, "--date=")
			continue
		} else {
			prepared = append(prepared, arg)
			continue
		}
		open := strings.LastIndex(arg, "<")
		close := strings.LastIndex(arg, ">")
		if open < 0 || close < open {
			return nil, fmt.Errorf("commit --author must be in 'Name <email>' format")
		}
		name, email := strings.TrimSpace(arg[:open]), strings.TrimSpace(arg[open+1:close])
		if name == "" || email == "" {
			return nil, fmt.Errorf("commit --author must include both name and email")
		}
		env["GIT_AUTHOR_NAME"] = name
		env["GIT_AUTHOR_EMAIL"] = email
	}
	return prepared, nil
}
