package shell

import (
	"flag"
	"sort"
	"strings"

	"github.com/spf13/pflag"
)

type runtimeFlagSet struct {
	standard              *flag.FlagSet
	pflag                 *pflag.FlagSet
	unixNormalize         bool
	keyEqualsValue        bool
	positionalPaths       bool
	positionalDescription string
}

type runtimeOptionSyntax struct {
	name          string
	display       string
	shorthand     string
	pflag         bool
	takesValue    bool
	optionalValue bool
	description   string
}

type runtimeCommandSyntax struct {
	unixNormalize         bool
	keyEqualsValue        bool
	pflag                 bool
	positionalPaths       bool
	positionalDescription string
	options               []runtimeOptionSyntax
}

type runtimeFlagSetFactory func() runtimeFlagSet

var commandSyntaxRegistry = map[string]runtimeFlagSetFactory{}

func standardFlagSet(fs *flag.FlagSet, unixNormalize bool) runtimeFlagSet {
	return runtimeFlagSet{standard: fs, unixNormalize: unixNormalize}
}

func pflagSet(fs *pflag.FlagSet) runtimeFlagSet {
	return runtimeFlagSet{pflag: fs}
}

func standardPathFlagSet(fs *flag.FlagSet, unixNormalize bool) runtimeFlagSet {
	definition := standardFlagSet(fs, unixNormalize)
	definition.positionalPaths = true
	definition.positionalDescription = "Filesystem path."
	return definition
}

func pflagPathSet(fs *pflag.FlagSet) runtimeFlagSet {
	definition := pflagSet(fs)
	definition.positionalPaths = true
	definition.positionalDescription = "Filesystem path."
	return definition
}

func inspectRuntimeFlagSet(definition runtimeFlagSet) runtimeCommandSyntax {
	syntax := runtimeCommandSyntax{
		unixNormalize:         definition.unixNormalize,
		keyEqualsValue:        definition.keyEqualsValue,
		pflag:                 definition.pflag != nil,
		positionalPaths:       definition.positionalPaths,
		positionalDescription: definition.positionalDescription,
	}
	if definition.standard != nil {
		definition.standard.VisitAll(func(flag *flag.Flag) {
			display := "--" + flag.Name
			if len(flag.Name) == 1 {
				display = "-" + flag.Name
			}
			takesValue := true
			if boolFlag, ok := flag.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
				takesValue = false
			}
			syntax.options = append(syntax.options, runtimeOptionSyntax{
				name:        flag.Name,
				display:     display,
				takesValue:  takesValue,
				description: flag.Usage,
			})
		})
	}
	if definition.pflag != nil {
		definition.pflag.VisitAll(func(flag *pflag.Flag) {
			takesValue := flag.Value.Type() != "bool"
			optionalValue := takesValue && flag.NoOptDefVal != ""
			syntax.options = append(syntax.options, runtimeOptionSyntax{
				name:          flag.Name,
				display:       "--" + flag.Name,
				shorthand:     flag.Shorthand,
				pflag:         true,
				takesValue:    takesValue,
				optionalValue: optionalValue,
				description:   flag.Usage,
			})
		})
	}
	sort.Slice(syntax.options, func(i, j int) bool {
		return syntax.options[i].name < syntax.options[j].name
	})
	return syntax
}

func completeRuntimeFlagSet(ctx completionContext, cwd string, syntax runtimeCommandSyntax) ([]completionCandidate, bool) {
	options := runtimeOptionNames(syntax)
	optionsEnabled := true
	var pending *runtimeOptionSyntax
	for _, arg := range ctx.args {
		if pending != nil {
			pending = nil
			continue
		}
		if arg == "--" {
			optionsEnabled = false
			continue
		}
		if optionsEnabled {
			if option, _, inline, matched := matchRuntimeOption(syntax, arg); matched {
				if option.takesValue && !inline && !option.optionalValue {
					pending = option
				}
				continue
			}
			if isRuntimeOptionLike(arg, syntax) {
				continue
			}
		}
		if !syntax.pflag {
			optionsEnabled = false
		}
	}

	if pending != nil {
		return completeRuntimeValue(ctx, cwd, *pending), true
	}
	if name, valuePrefix, ok := runtimeInlineValue(syntax, ctx.prefix); ok {
		option, found := runtimeOptionByName(syntax, name)
		if found && option.takesValue {
			valueContext := ctx
			valueContext.prefix = valuePrefix
			return completeRuntimeValue(valueContext, cwd, *option), true
		}
	}
	if optionsEnabled && strings.HasPrefix(ctx.prefix, "-") || syntax.keyEqualsValue {
		return completeRuntimeOptions(ctx, syntax, options), true
	}
	if syntax.positionalPaths || looksLikePath(ctx.prefix) {
		description := syntax.positionalDescription
		if description == "" && looksLikePath(ctx.prefix) {
			description = "Path."
		}
		return describeCompletionCandidates(completePath(ctx, cwd, false), description), true
	}
	return nil, true
}

func completeRuntimeOptions(ctx completionContext, syntax runtimeCommandSyntax, names []string) []completionCandidate {
	candidates := completeWords(ctx, names, false)
	for i := range candidates {
		if option, _, _, matched := matchRuntimeOption(syntax, candidates[i].Display); matched {
			candidates[i].Description = option.description
		}
	}
	return candidates
}

func looksLikePath(value string) bool {
	return strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~")
}

func runtimeOptionNames(syntax runtimeCommandSyntax) []string {
	names := make([]string, 0, len(syntax.options)*2)
	for _, option := range syntax.options {
		if syntax.keyEqualsValue {
			names = append(names, option.name+"=")
			continue
		}
		names = append(names, option.display)
		if option.shorthand != "" {
			names = append(names, "-"+option.shorthand)
		}
	}
	return names
}

func matchRuntimeOption(syntax runtimeCommandSyntax, token string) (*runtimeOptionSyntax, string, bool, bool) {
	name, value, inline := strings.Cut(token, "=")
	if syntax.keyEqualsValue && !strings.HasPrefix(name, "-") {
		option, ok := runtimeOptionByName(syntax, name)
		return option, value, inline, ok
	}
	candidate := strings.TrimLeft(name, "-")
	option, ok := runtimeOptionByName(syntax, candidate)
	if ok {
		return option, value, inline, true
	}
	for i := range syntax.options {
		if syntax.options[i].shorthand != "" && candidate == syntax.options[i].shorthand {
			return &syntax.options[i], value, inline, true
		}
	}
	return nil, value, inline, false
}

func runtimeOptionByName(syntax runtimeCommandSyntax, name string) (*runtimeOptionSyntax, bool) {
	for i := range syntax.options {
		if syntax.options[i].name == name {
			return &syntax.options[i], true
		}
	}
	return nil, false
}

func runtimeInlineValue(syntax runtimeCommandSyntax, prefix string) (string, string, bool) {
	name, value, ok := strings.Cut(prefix, "=")
	if !ok {
		return "", "", false
	}
	if syntax.keyEqualsValue {
		return name, value, true
	}
	option, _, _, found := matchRuntimeOption(syntax, name)
	if found {
		return option.name, value, true
	}
	return "", "", false
}

func isRuntimeOptionLike(token string, syntax runtimeCommandSyntax) bool {
	if strings.HasPrefix(token, "-") {
		return true
	}
	if syntax.keyEqualsValue {
		name, _, inline := strings.Cut(token, "=")
		if inline {
			_, ok := runtimeOptionByName(syntax, name)
			return ok
		}
	}
	return false
}

func completeRuntimeValue(ctx completionContext, cwd string, option runtimeOptionSyntax) []completionCandidate {
	if choices := completionDescriptionChoices(option.description); len(choices) > 0 {
		return describeCompletionCandidates(completeWords(ctx, choices, true), option.description)
	}
	switch inferCompletionValueKind(option.description) {
	case completionFile:
		return describeCompletionCandidates(completePath(ctx, cwd, false), option.description)
	case completionDirectory:
		return describeCompletionCandidates(completePath(ctx, cwd, true), option.description)
	default:
		return nil
	}
}
