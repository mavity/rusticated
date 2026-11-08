package shell

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"ai"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/mavity/rusticated/kabibi/tools/browser"
	"mvdan.cc/sh/v3/syntax"
)

type completionKind int

const (
	completionCommand completionKind = iota
	completionFile
	completionDirectory
	completionValue
)

type completionCandidate struct {
	Display string
	Insert  string
	Space   bool
}

type completionContext struct {
	commandPosition bool
	command         string
	args            []string
	prefix          string
	suffix          string
	tokenStart      int
	cursor          int
	redirect        bool
	quote           rune
}

type completionSpec struct {
	options            []string
	subcommands        []string
	paths              map[int]bool
	values             map[int][]string
	optionDescriptions map[string]string
	optionValues       map[string][]string
}

var completionSpecs = map[string]completionSpec{
	"cd":      {options: []string{"-L", "-P"}},
	"command": {options: []string{"-p", "-v", "-V"}},
	"chmod": {options: []string{"-R", "-c", "-f", "-v", "--reference"},
		values: map[int][]string{-1: {"a+r", "a-r", "a+w", "a+x", "u+rwx", "go-rwx"}}},
	"cp": {options: []string{"-f", "-i", "-n", "-p", "-R", "-r", "-v", "--reflink"},
		paths:        map[int]bool{-1: true},
		optionValues: map[string][]string{"--reflink": {"always", "auto", "never"}}},
	"dd": {options: []string{"if=", "of=", "bs=", "count=", "seek=", "skip=", "conv=", "oflag=", "status="},
		optionValues: map[string][]string{"conv=": {"ascii", "block", "fdatasync", "fsync", "noerror", "sync", "sparse", "swab", "unblock"},
			"oflag=":  {"append", "direct", "dsync", "sync"},
			"status=": {"none", "noxfer", "progress"}}},
	"find": {options: []string{"-name", "-iname", "-type", "-size", "-mtime", "-maxdepth", "-mindepth", "-exec", "-print", "-delete"},
		paths:  map[int]bool{-1: true},
		values: map[int][]string{1: {"b", "c", "d", "f", "l", "p", "s"}}},
	"git": {subcommands: []string{"add", "am", "apply", "bisect", "blame", "branch", "cat-file", "checkout", "cherry-pick", "clean", "clone", "commit", "commit-tree", "config", "count-objects", "describe", "diff", "diff-tree", "fetch", "for-each-ref", "format-patch", "grep", "hash-object", "init", "log", "ls-files", "ls-remote", "ls-tree", "merge", "merge-base", "merge-file", "mv", "notes", "pull", "push", "read-tree", "rebase", "reflog", "remote", "replace", "reset", "restore", "revert", "rev-list", "rev-parse", "rm", "show", "show-ref", "shortlog", "stash", "status", "submodule", "switch", "symbolic-ref", "tag", "update-index", "update-ref", "version", "worktree", "write-tree"}},
	"grep": {options: []string{"-e", "-f", "-i", "-n", "-r", "-v", "-w", "-x", "--color", "--exclude"},
		optionDescriptions: map[string]string{"-f": "read patterns from file", "--exclude": "exclude files matching path pattern"}},
	"ip": {subcommands: []string{"address", "addr", "link", "route", "neigh", "neighbor", "rule", "tunnel", "help"}},
	"kill": {options: []string{"-l", "-s", "-SIGNAL"},
		values:       map[int][]string{0: {"HUP", "INT", "QUIT", "KILL", "TERM", "USR1", "USR2", "STOP", "CONT"}},
		optionValues: map[string][]string{"-s": {"HUP", "INT", "QUIT", "KILL", "TERM", "USR1", "USR2", "STOP", "CONT"}}},
	"ls":    {options: []string{"-a", "-d", "-h", "-l", "-R", "-t"}, paths: map[int]bool{-1: true}},
	"mkdir": {options: []string{"-m", "-p", "-v"}, paths: map[int]bool{-1: true}},
	"mv":    {options: []string{"-f", "-i", "-n", "-v"}, paths: map[int]bool{-1: true}},
	"rg": {options: []string{"-e", "-f", "-g", "-i", "-n", "-v", "--files", "--hidden"},
		optionDescriptions: map[string]string{"-f": "read patterns from file", "-g": "glob file path"}},
	"rm":   {options: []string{"-f", "-i", "-r", "-R", "-v"}, paths: map[int]bool{-1: true}},
	"stty": {subcommands: []string{"-a", "-g", "raw", "sane", "echo", "-echo", "cooked", "size", "speed", "rows", "columns"}},
	"tar": {options: []string{"-c", "-x", "-t", "-f", "-v", "-z", "-C"},
		optionDescriptions: map[string]string{"-f": "archive file path", "-C": "change directory"}},
	"time": {options: []string{"-p"}},
	"timeout": {options: []string{"-k", "--kill-after", "-s", "--signal"},
		optionValues: map[string][]string{"-s": {"HUP", "INT", "KILL", "TERM"}, "--signal": {"HUP", "INT", "KILL", "TERM"}}},
	"wget":  {options: []string{"-O", "-q", "-v"}, optionDescriptions: map[string]string{"-O": "output file path"}},
	"xargs": {options: []string{"-0", "-d", "-E", "-I", "-L", "-n", "-P", "-r", "-t", "-x"}},
	"nohup": {},
}

var shellCompletionBuiltins = []string{
	".", ":", "[", "alias", "break", "builtin", "case", "cd", "command", "continue",
	"declare", "dirs", "echo", "eval", "exec", "exit", "export", "false", "for",
	"function", "getopts", "hash", "help", "if", "kill", "let", "mapfile", "printf",
	"pwd", "read", "readarray", "readonly", "return", "set", "shift", "shopt",
	"source", "test", "times", "trap", "true", "type", "typeset", "umask",
	"unalias", "unset", "wait", "while",
}

func (s *Shell) completeInput(value string, cursor int) []completionCandidate {
	if s.execSess == nil {
		return nil
	}
	ctx := completionContextAt(value, cursor)
	if ctx.redirect {
		return completePath(ctx, s.execSess.Dir(), false)
	}
	if ctx.commandPosition {
		return s.completeCommand(ctx)
	}
	if isNestedShellCommand(ctx.command) && hasCommandStringOption(ctx.command, ctx.args) {
		nested := completionContextAt(ctx.prefix, len([]rune(ctx.prefix)))
		return s.completeInput(ctx.prefix, nested.cursor)
	}
	if ctx.command == "web" {
		return s.completeWeb(ctx)
	}
	if ctx.command == "cdp" {
		return s.completeCDP(ctx)
	}
	if ctx.command == "ai" {
		return s.completeAI(ctx)
	}
	if ctx.command == "git" {
		if ctx.argsInGitBisectRun() {
			ctx.commandPosition = true
			return s.completeCommand(ctx)
		}
		return s.completeGit(ctx)
	}
	if delegatedCommandExpected(ctx.command, ctx.args, ctx.prefix) {
		return s.completeCommand(ctx)
	}
	if strings.HasPrefix(ctx.prefix, "-") {
		if spec, ok := completionSpecs[ctx.command]; ok {
			if values := spec.optionValues[ctx.prefix]; len(values) > 0 {
				return completeWords(ctx, values, true)
			}
			return completeWords(ctx, spec.options, true)
		}
	}
	if spec, ok := completionSpecs[ctx.command]; ok {
		if len(ctx.args) > 0 {
			option := ctx.args[len(ctx.args)-1]
			if values := spec.optionValues[option]; len(values) > 0 {
				return completeWords(ctx, values, true)
			}
			description := spec.optionDescriptions[option]
			if choices := completionDescriptionChoices(description); len(choices) > 0 {
				return completeWords(ctx, choices, true)
			}
			if inferCompletionValueKind(description) == completionFile {
				return completePath(ctx, s.execSess.Dir(), false)
			}
		}
		if ctx.command == "dd" {
			if equal := strings.IndexByte(ctx.prefix, '='); equal >= 0 {
				key, value := ctx.prefix[:equal+1], ctx.prefix[equal+1:]
				ctx.prefix = value
				if key == "if=" || key == "of=" {
					return completePath(ctx, s.execSess.Dir(), false)
				}
				return completeWords(ctx, spec.optionValues[key], true)
			}
			return completeWords(ctx, spec.options, false)
		}
		if ctx.command == "ip" {
			if len(ctx.args) == 1 {
				switch ctx.args[0] {
				case "address", "addr":
					return completeWords(ctx, []string{"add", "change", "del", "delete", "flush", "help", "replace", "show"}, true)
				case "link":
					return completeWords(ctx, []string{"add", "delete", "help", "set", "show"}, true)
				case "route":
					return completeWords(ctx, []string{"add", "change", "del", "delete", "get", "help", "list", "replace", "show"}, true)
				case "neigh", "neighbor":
					return completeWords(ctx, []string{"add", "change", "del", "delete", "flush", "help", "replace", "show"}, true)
				}
			}
		}
		if len(ctx.args) == 0 && len(spec.subcommands) > 0 {
			return completeWords(ctx, spec.subcommands, true)
		}
		argIndex := len(ctx.args) - 1
		if choices := spec.values[argIndex]; len(choices) > 0 {
			return completeWords(ctx, choices, true)
		}
		if spec.paths[argIndex] || spec.paths[-1] {
			return completePath(ctx, s.execSess.Dir(), ctx.command == "cd")
		}
	}
	if completionArgumentLikelyPath(ctx.command, ctx.args) {
		return completePath(ctx, s.execSess.Dir(), ctx.command == "cd" || ctx.command == "pushd")
	}
	return nil
}

func (s *Shell) completeAI(ctx completionContext) []completionCandidate {
	tools := BuildKabibiTools(s.execSess)
	if len(ctx.args) == 0 {
		var candidates []completionCandidate
		candidates = append(candidates, completeWords(ctx, aiControlSubcommands, true)...)
		names := make([]string, 0, len(tools))
		for name := range tools {
			names = append(names, name)
		}
		return append(candidates, completeNormalizedWords(ctx, names)...)
	}

	subcommand := normalizeAIToolName(ctx.args[0])
	if subcommand == aiControlLogin || subcommand == aiControlLogout {
		if len(ctx.args) == 1 {
			return completeWords(ctx, supportedRemoteProviderNames(), true)
		}
		return nil
	}
	tool, ok := tools[subcommand]
	if !ok {
		return nil
	}
	args := ctx.args[1:]
	state, err := scanAIToolArguments(tool, args, true)
	if err != nil {
		return nil
	}
	if valueSchema := aiInlineValueSchema(tool, ctx.prefix); valueSchema != nil {
		valuePrefix := ctx.prefix[strings.IndexByte(ctx.prefix, '=')+1:]
		return s.completeAIValue(ctx, valueSchema, valuePrefix)
	}
	if !strings.HasPrefix(ctx.prefix, "-") {
		if state.pendingValue != nil {
			return s.completeAIValue(ctx, state.pendingValue, ctx.prefix)
		}
	}
	if strings.HasPrefix(ctx.prefix, "-") {
		options := make([]string, 0, len(tool.Parameters))
		for name := range tool.Parameters {
			options = append(options, "--"+strings.ReplaceAll(name, "_", "-"))
		}
		return completeNormalizedWords(ctx, options)
	}
	if state.pendingBoolean != nil {
		return completeWords(ctx, []string{"true", "false"}, true)
	}
	return nil
}

func completeNormalizedWords(ctx completionContext, words []string) []completionCandidate {
	prefix := normalizeAIToolName(ctx.prefix)
	suffix := normalizeAIToolName(ctx.suffix)
	var candidates []completionCandidate
	for _, word := range words {
		normalized := normalizeAIToolName(word)
		if !strings.HasPrefix(normalized, prefix) || !strings.HasSuffix(normalized, suffix) ||
			len(normalized) < len(prefix)+len(suffix) {
			continue
		}
		insert := word[len(ctx.prefix) : len(word)-len(ctx.suffix)]
		candidates = append(candidates, completionCandidate{
			Display: word,
			Insert:  quoteCompletionFor(insert, ctx.quote),
			Space:   ctx.suffix == "",
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Display < candidates[j].Display
	})
	return candidates
}

func (s *Shell) completeAIValue(ctx completionContext, schema map[string]any, prefix string) []completionCandidate {
	valueContext := ctx
	valueContext.prefix = prefix
	if choices := aiSchemaChoices(schema); len(choices) > 0 {
		return completeWords(valueContext, choices, true)
	}
	if inferCompletionValueKind(aiSchemaDescription(schema)) == completionFile {
		return completePath(valueContext, s.execSess.Dir(), false)
	}
	return nil
}

func aiInlineValueSchema(tool ai.Tool, prefix string) map[string]any {
	option, _, found := strings.Cut(prefix, "=")
	if !found {
		return nil
	}
	return aiToolOptionSchema(tool, strings.TrimLeft(option, "-"))
}

func aiSchemaDescription(schema map[string]any) string {
	description, _ := schema["description"].(string)
	return description
}

func aiSchemaChoices(schema map[string]any) []string {
	switch values := schema["enum"].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		choices := make([]string, 0, len(values))
		for _, value := range values {
			if choice, ok := value.(string); ok {
				choices = append(choices, choice)
			}
		}
		if len(choices) > 0 {
			return choices
		}
	default:
	}
	return completionDescriptionChoices(aiSchemaDescription(schema))
}

func (ctx completionContext) argsInGitBisectRun() bool {
	return len(ctx.args) >= 2 && ctx.args[0] == "bisect" && ctx.args[1] == "run"
}

func (s *Shell) completeGit(ctx completionContext) []completionCandidate {
	state, err := parseGitInvocationState(s.execSess.Dir(), ctx.args, true)
	if err != nil {
		return nil
	}
	if valuePrefix, ok := gitInlinePathPrefix(ctx.prefix); ok {
		valueContext := ctx
		valueContext.prefix = valuePrefix
		return completePath(valueContext, state.invocation.effectiveDir, true)
	}
	if state.pendingOption != "" {
		switch state.pendingOption {
		case "-C":
			return completePath(ctx, state.invocation.effectiveDir, true)
		case "--git-dir", "--work-tree":
			return completePath(ctx, state.invocation.effectiveDir, false)
		case "-c":
			return completeWords(ctx, []string{"core.editor=", "core.pager=", "user.email=", "user.name="}, false)
		}
	}
	args := gitCommandArgs(state.invocation.args)
	if len(args) == 0 {
		if strings.HasPrefix(ctx.prefix, "-") {
			return completeWords(ctx, gitGlobalOptionNames(), true)
		}
		return completeWords(ctx, completionSpecs["git"].subcommands, false)
	}
	return s.completeGitSubcommand(ctx, args[0], args[1:])
}

func gitCommandArgs(args []string) []string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			i++
		case "--no-pager", "-P", "--no-replace-objects", "--bare":
		default:
			if strings.HasPrefix(args[i], "-") {
				continue
			}
			return args[i:]
		}
	}
	return nil
}

func gitInlinePathPrefix(prefix string) (string, bool) {
	for _, spec := range gitGlobalOptionSpecs {
		if spec.kind != gitOptionChangeDirectory && spec.kind != gitOptionGitDir && spec.kind != gitOptionWorkTree {
			continue
		}
		if spec.inlinePrefix && strings.HasPrefix(prefix, spec.name) && len(prefix) > len(spec.name) {
			return prefix[len(spec.name):], true
		}
		if spec.inlineEqual && strings.HasPrefix(prefix, spec.name+"=") {
			return strings.TrimPrefix(prefix, spec.name+"="), true
		}
	}
	return "", false
}

func (s *Shell) completeWeb(ctx completionContext) []completionCandidate {
	return s.completeWebSyntax(ctx, "web")
}

func (s *Shell) completeCDP(ctx completionContext) []completionCandidate {
	return s.completeWebSyntax(ctx, "cdp")
}

func (s *Shell) completeWebSyntax(ctx completionContext, command string) []completionCandidate {
	if strings.HasPrefix(ctx.prefix, "--nick=") {
		valueContext := ctx
		valueContext.prefix = strings.TrimPrefix(ctx.prefix, "--nick=")
		return completeWords(valueContext, browser.DefaultManager().Nicknames(), true)
	}

	global := parseWebGlobalOptions(ctx.args, true)
	if global.nicknameExpected {
		return completeWords(ctx, browser.DefaultManager().Nicknames(), true)
	}
	rest := global.args
	if command == "cdp" {
		if strings.HasPrefix(ctx.prefix, "-") {
			return completeWords(ctx, webNicknameOptions, false)
		}
		return nil
	}
	if len(rest) == 0 {
		if strings.HasPrefix(ctx.prefix, "-") {
			return completeWords(ctx, webNicknameOptions, false)
		}
		return completeWords(ctx, webSubcommandNames(), true)
	}
	spec, ok := webSubcommand(rest[0])
	if !ok {
		return nil
	}
	args := rest[1:]
	if strings.HasPrefix(ctx.prefix, "-") {
		options := append([]string(nil), webNicknameOptions...)
		options = append(options, webOptionNames(spec)...)
		if expected := webExpectedArgument(spec, args); expected != nil {
			for _, choice := range expected.choices {
				options = append(options, choice.value)
			}
		}
		return completeWords(ctx, options, false)
	}
	expected := webExpectedArgument(spec, args)
	if expected == nil {
		return nil
	}
	if len(expected.choices) > 0 {
		choices := make([]string, len(expected.choices))
		for i, choice := range expected.choices {
			choices[i] = choice.value
		}
		return completeWords(ctx, choices, true)
	}
	if expected.path {
		return completePath(ctx, s.execSess.Dir(), false)
	}
	return nil
}

func webExpectedArgument(spec webSubcommandSpec, args []string) *webArgumentSpec {
	state := parseWebArguments(spec, args)
	if state.next < len(spec.positionals) {
		return &spec.positionals[state.next]
	}
	if len(spec.positionals) > 0 && spec.positionals[len(spec.positionals)-1].variadic {
		return &spec.positionals[len(spec.positionals)-1]
	}
	return nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func isNestedShellCommand(command string) bool {
	switch command {
	case "bash", "sh", "kabibi":
		return true
	}
	return false
}

func hasCommandStringOption(_ string, args []string) bool {
	for _, arg := range args {
		if arg == "-c" || arg == "--command" {
			return true
		}
	}
	return false
}

func delegatedCommandExpected(command string, args []string, prefix string) bool {
	if strings.HasPrefix(prefix, "-") {
		return false
	}
	switch command {
	case "builtin", "command", "exec", "nohup", "time":
		return true
	case "timeout":
		return len(args) >= 1
	case "xargs":
		for _, arg := range args {
			if !strings.HasPrefix(arg, "-") {
				return true
			}
		}
	}
	return false
}

func (s *Shell) completeGitSubcommand(ctx completionContext, command string, args []string) []completionCandidate {
	if strings.HasPrefix(ctx.prefix, "-") {
		if command == "notes" {
			noteCommand := ""
			if len(args) > 0 {
				noteCommand = args[0]
			}
			return completeWords(ctx, gitNotesOptionNames(noteCommand), true)
		}
		if command == "replace" {
			return completeWords(ctx, gitReplaceOptionNames(), true)
		}
		options := map[string][]string{
			"add":      {"-A", "-f", "-i", "-p", "-u", "-v"},
			"bisect":   {"--no-checkout", "--no-progress"},
			"branch":   {"-a", "-d", "-D", "-f", "-m", "-M", "-r", "-v"},
			"checkout": {"-b", "-B", "-f", "-m", "-p", "-q", "--detach"},
			"clean":    {"-d", "-f", "-i", "-n", "-x", "-X"},
			"commit":   {"-a", "-m", "--amend", "--author", "--date", "--no-edit"},
			"diff":     {"--cached", "--check", "--name-only", "--stat", "--"},
			"log":      {"--all", "--oneline", "--stat", "--"},
			"merge":    {"--abort", "--continue", "--no-commit", "--squash"},
			"pull":     {"--ff-only", "--no-rebase", "--rebase"},
			"push":     {"--all", "--force", "--set-upstream", "--tags"},
			"restore":  {"--source", "--staged", "--worktree"},
			"reset":    {"--hard", "--mixed", "--soft"},
			"rm":       {"-f", "-r", "--cached"},
			"switch":   {"-c", "-C", "--detach"},
			"tag":      {"-a", "-d", "-f", "-l", "-m", "-s"},
		}
		return completeWords(ctx, options[command], true)
	}
	if command == "notes" && len(args) == 0 {
		return completeWords(ctx, gitNotesCommandNames(), true)
	}
	if command == "bisect" && len(args) == 0 {
		return completeWords(ctx, []string{"bad", "good", "new", "old", "reset", "run", "skip", "start", "terms"}, true)
	}
	if command == "remote" && len(args) == 0 {
		return completeWords(ctx, []string{"add", "get-url", "prune", "remove", "rename", "set-url", "show", "update"}, true)
	}
	if command == "worktree" && len(args) == 0 {
		return completeWords(ctx, []string{"add", "list", "lock", "move", "prune", "remove", "unlock"}, true)
	}
	switch command {
	case "add", "am", "apply", "clean", "diff", "grep", "mv", "restore", "rm":
		return completePath(ctx, s.execSess.Dir(), false)
	case "checkout", "switch":
		branches := completeGitBranches(ctx, s.execSess.Dir())
		return append(branches, completePath(ctx, s.execSess.Dir(), false)...)
	case "branch":
		return completeGitBranches(ctx, s.execSess.Dir())
	case "commit":
		return completePath(ctx, s.execSess.Dir(), false)
	case "worktree":
		if len(args) > 0 && args[0] == "add" {
			return completePath(ctx, s.execSess.Dir(), true)
		}
	}
	return nil
}

func completeGitBranches(ctx completionContext, cwd string) []completionCandidate {
	repo, err := gogit.PlainOpenWithOptions(cwd, &gogit.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil
	}
	iter, err := repo.Branches()
	if err != nil {
		logUnexpectedCompletionFSError("list Git branches", cwd, err)
		return nil
	}
	var branches []string
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		branches = append(branches, ref.Name().Short())
		return nil
	})
	if err != nil {
		logUnexpectedCompletionFSError("read Git branches", cwd, err)
		return nil
	}
	return completeWords(ctx, branches, true)
}

func (s *Shell) completeCommand(ctx completionContext) []completionCandidate {
	candidates := make(map[string]struct{})
	for _, name := range shellCompletionBuiltins {
		candidates[name] = struct{}{}
	}
	for _, name := range AllURootCommandNames() {
		candidates[name] = struct{}{}
	}
	for _, name := range []string{"bash", "cdp", "git", "help", "kabibi", "quit", "sh", "web"} {
		candidates[name] = struct{}{}
	}
	if s.execSess != nil {
		if isWindowsLikeEnv(s.execSess.EnvMap()) {
			ctx.prefix = strings.ToLower(ctx.prefix)
			ctx.suffix = strings.ToLower(ctx.suffix)
		}
		for _, name := range s.execSess.pathCommandNames(ctx.prefix) {
			candidates[name] = struct{}{}
		}
	}
	words := make([]string, 0, len(candidates))
	for name := range candidates {
		words = append(words, name)
	}
	sort.Strings(words)
	return completeWords(ctx, words, true)
}

func completionArgumentLikelyPath(command string, args []string) bool {
	switch command {
	case "cat", "cd", "cmp", "cp", "dd", "find", "head", "more", "pushd",
		"mv", "nano", "readlink", "realpath", "rm", "sed", "sh", "source", "tail", "tar",
		"tee", "touch", "truncate", "vi", "vim", "wc", "wget":
		return true
	case "chmod":
		return len(args) >= 1
	case "grep", "rg":
		return len(args) >= 1
	}
	if len(args) > 0 {
		last := args[len(args)-1]
		return strings.ContainsAny(last, `/\`) || strings.HasPrefix(last, ".") || strings.HasPrefix(last, "~")
	}
	return false
}

func inferCompletionValueKind(description string) completionKind {
	description = strings.ToLower(description)
	words := completionDescriptionWords(description)
	hasPathNoun := hasAnyCompletionWord(words, "file", "files", "path", "paths", "filename", "archive", "keyring", "config", "source", "destination")
	hasDirectoryNoun := hasAnyCompletionWord(words, "dir", "directory", "folder", "work-tree", "git-dir", "chdir")
	if hasDirectoryNoun && (!strings.Contains(description, "working directory") || !hasPathNoun ||
		strings.Contains(description, "directory path") || strings.Contains(description, "path to a directory") ||
		strings.Contains(description, "directory to ") || hasAnyCompletionWord(words, "folder", "work-tree", "git-dir", "chdir")) {
		return completionDirectory
	}
	if hasPathNoun {
		return completionFile
	}
	if hasDirectoryNoun {
		return completionDirectory
	}
	return completionValue
}

func completionDescriptionWords(description string) map[string]bool {
	words := make(map[string]bool)
	for _, word := range strings.FieldsFunc(description, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	}) {
		words[word] = true
	}
	return words
}

func hasAnyCompletionWord(words map[string]bool, candidates ...string) bool {
	for _, candidate := range candidates {
		if words[candidate] {
			return true
		}
	}
	return false
}

func completionDescriptionChoices(description string) []string {
	for start := strings.IndexByte(description, '('); start >= 0; {
		end := strings.IndexByte(description[start+1:], ')')
		if end < 0 {
			return nil
		}
		end += start + 1
		parts := strings.Split(description[start+1:end], "|")
		if len(parts) > 1 {
			choices := make([]string, 0, len(parts))
			valid := true
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" || strings.ContainsAny(part, " \t\r\n") {
					valid = false
					break
				}
				choices = append(choices, part)
			}
			if valid {
				return choices
			}
		}
		next := strings.IndexByte(description[end+1:], '(')
		if next < 0 {
			return nil
		}
		start = end + 1 + next
	}
	return nil
}

func completeWords(ctx completionContext, words []string, appendSpace bool) []completionCandidate {
	prefix := ctx.prefix
	suffix := ctx.suffix
	var candidates []completionCandidate
	for _, word := range words {
		if !strings.HasPrefix(word, prefix) || !strings.HasSuffix(word, suffix) ||
			len(word) < len(prefix)+len(suffix) {
			continue
		}
		insert := word[len(prefix) : len(word)-len(suffix)]
		candidates = append(candidates, completionCandidate{
			Display: word,
			Insert:  quoteCompletionFor(insert, ctx.quote),
			Space:   appendSpace && suffix == "",
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Display < candidates[j].Display
	})
	return candidates
}

func completePath(ctx completionContext, cwd string, directoriesOnly bool) []completionCandidate {
	prefix := unquoteCompletion(ctx.prefix)
	suffix := unquoteCompletion(ctx.suffix)
	dirPart, base := filepath.Split(prefix)
	searchDir := dirPart
	if searchDir == "" {
		searchDir = "."
	}
	if !filepath.IsAbs(searchDir) {
		searchDir = filepath.Join(cwd, searchDir)
	}
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		logUnexpectedCompletionFSError("read completion directory", searchDir, err)
		return nil
	}
	var candidates []completionCandidate
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(name, base) || !strings.HasSuffix(name, suffix) ||
			len(name) < len(base)+len(suffix) {
			continue
		}
		isDir := entry.IsDir()
		if directoriesOnly && !isDir {
			continue
		}
		insert := name[len(base) : len(name)-len(suffix)]
		display := filepath.ToSlash(filepath.Join(dirPart, name))
		candidates = append(candidates, completionCandidate{
			Display: display,
			Insert:  quoteCompletionFor(insert, ctx.quote),
			Space:   !isDir && suffix == "",
		})
		if isDir && suffix == "" {
			candidates[len(candidates)-1].Insert += quoteCompletionFor(string(os.PathSeparator), ctx.quote)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Display < candidates[j].Display
	})
	return candidates
}

func completionContextAt(value string, cursor int) completionContext {
	runes := []rune(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if completionExcludedAtCursor(value, cursor) {
		return completionContext{cursor: cursor}
	}
	start, end, quote := completionTokenBounds(runes, cursor)
	before := unquoteCompletion(string(runes[start:cursor]))
	after := unquoteCompletion(string(runes[cursor:end]))
	prefixText := string(runes[:start])
	tokens, redirect, commandPos := completionWordsBefore(prefixText)
	ctx := completionContext{
		commandPosition: commandPos,
		prefix:          before,
		suffix:          after,
		tokenStart:      start,
		cursor:          cursor,
		redirect:        redirect,
	}
	ctx.quote = quote
	if len(tokens) > 0 {
		ctx.command = tokens[0]
		ctx.args = tokens[1:]
	}
	if commandPos {
		ctx.command = ""
		ctx.args = nil
	}
	if isNestedShellCommand(ctx.command) && hasCommandStringOption(ctx.command, ctx.args) {
		return ctx
	}
	if call := completionCallAt(value, cursor); call != nil && len(call.Args) > 0 {
		tokenStartByte := len(string(runes[:start]))
		cursorByte := uint(len(string(runes[:cursor])))
		firstEnd := call.Args[0].End().Offset()
		if cursorByte <= firstEnd {
			ctx.commandPosition = true
			ctx.command = ""
			ctx.args = nil
		} else if command := call.Args[0].Lit(); command != "" {
			ctx.commandPosition = false
			ctx.command = command
			ctx.args = nil
			for _, arg := range call.Args[1:] {
				if arg.End().Offset() <= uint(tokenStartByte) {
					ctx.args = append(ctx.args, arg.Lit())
				}
			}
		}
	}
	return ctx
}

func completionExcludedAtCursor(value string, cursor int) bool {
	runes := []rune(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	offset := uint(len(string(runes[:cursor])))
	file, _ := syntax.NewParser(syntax.KeepComments(true)).Parse(strings.NewReader(value), "")
	if file == nil {
		return false
	}
	excluded := false
	syntax.Walk(file, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.Comment, *syntax.ArithmExp, *syntax.ParamExp:
			if node.Pos().Offset() <= offset && offset <= node.End().Offset() {
				excluded = true
				return false
			}
		}
		return !excluded
	})
	return excluded
}

func completionCallAt(value string, cursor int) *syntax.CallExpr {
	runes := []rune(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	cursorByte := uint(len(string(runes[:cursor])))
	file, _ := syntax.NewParser(syntax.KeepComments(false)).Parse(strings.NewReader(value), "")
	if file == nil {
		return nil
	}
	var selected *syntax.CallExpr
	selectedWidth := ^uint(0)
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		start, end := call.Pos().Offset(), call.End().Offset()
		if cursorByte < start || cursorByte > end {
			return true
		}
		if width := end - start; width <= selectedWidth {
			selected = call
			selectedWidth = width
		}
		return true
	})
	return selected
}

func completionTokenBounds(runes []rune, cursor int) (int, int, rune) {
	start, end := 0, cursor
	quote, escaped := rune(0), false
	for i := 0; i < cursor; i++ {
		r := runes[i]
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) || strings.ContainsRune(";|&(){}<>", r) {
			start = i + 1
		}
	}
	quoteAtCursor := quote
	escaped = false
	for i := cursor; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			escaped = false
			end = i + 1
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			end = i + 1
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			end = i + 1
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			end = i + 1
			continue
		}
		if unicode.IsSpace(r) || strings.ContainsRune(";|&(){}<>", r) {
			break
		}
		end = i + 1
	}
	return start, end, quoteAtCursor
}

func completionWordsBefore(prefix string) ([]string, bool, bool) {
	var words []string
	var word strings.Builder
	commandPos, redirect := true, false
	quote, escaped := rune(0), false
	flush := func() {
		if word.Len() == 0 {
			return
		}
		value := word.String()
		word.Reset()
		if redirect {
			redirect = false
			return
		}
		if commandPos && isAssignmentWord(value) {
			return
		}
		if commandPos && isCompletionShellKeyword(value) {
			switch value {
			case "if", "then", "else", "elif", "while", "until", "do", "!", "time", "coproc":
				commandPos = true
			default:
				commandPos = false
			}
			return
		}
		if commandPos {
			commandPos = false
		}
		words = append(words, value)
	}
	for _, r := range prefix {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if strings.ContainsRune(";|&()\n", r) {
			flush()
			words = nil
			commandPos = true
			redirect = false
			continue
		}
		if r == '>' || r == '<' {
			flush()
			redirect = true
			continue
		}
		word.WriteRune(r)
	}
	flush()
	return words, redirect, commandPos
}

func isCompletionShellKeyword(word string) bool {
	switch word {
	case "if", "then", "else", "elif", "while", "until", "do", "done", "fi",
		"case", "esac", "for", "select", "in", "function", "{", "}", "!", "time", "coproc":
		return true
	}
	return false
}

func quoteCompletion(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) || strings.ContainsRune(`\\'\"$`+"`;&|<>()[{}!*?[]", r) {
			out.WriteRune('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func quoteCompletionFor(value string, quote rune) string {
	switch quote {
	case '\'':
		return strings.ReplaceAll(value, "'", "'\\''")
	case '"':
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		value = strings.ReplaceAll(value, "$", `\$`)
		return strings.ReplaceAll(value, "`", "\\`")
	default:
		return quoteCompletion(value)
	}
}

func unquoteCompletion(value string) string {
	var out strings.Builder
	quote, escaped := rune(0), false
	for _, r := range value {
		if escaped {
			out.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				out.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		out.WriteRune(r)
	}
	if escaped {
		out.WriteRune('\\')
	}
	return out.String()
}
