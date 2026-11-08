package shell

import (
	"sort"
	"strings"
)

type generatedOptionSyntax struct {
	name          string
	display       string
	shorthand     string
	pflag         bool
	takesValue    bool
	optionalValue bool
	description   string
	choices       []string
}

type generatedCommandSyntax struct {
	unixNormalize  bool
	keyEqualsValue bool
	pflag          bool
	variadic       bool
	positionals    []string
	choices        []string
	options        []generatedOptionSyntax
}

func completeGeneratedSyntax(ctx completionContext, cwd string, syntax generatedCommandSyntax) ([]completionCandidate, bool) {
	options := generatedSyntaxOptionNames(syntax)
	position := 0
	optionsEnabled := true
	var pending *generatedOptionSyntax
	for _, arg := range ctx.args {
		if pending != nil {
			pending = nil
			continue
		}
		if arg == "--" {
			if !syntax.pflag {
				optionsEnabled = false
			}
			continue
		}
		if optionsEnabled {
			if option, _, inline, matched := matchGeneratedOption(syntax, arg); matched {
				if option.takesValue && !inline && !option.optionalValue {
					pending = option
				}
				continue
			}
			if isGeneratedOptionLike(arg, syntax) {
				continue
			}
		}
		optionsEnabled = false
		position++
	}

	if pending != nil {
		return completeGeneratedValue(ctx, cwd, *pending), true
	}
	if name, valuePrefix, ok := generatedInlineValue(syntax, ctx.prefix); ok {
		option, found := generatedOptionByName(syntax, name)
		if found && option.takesValue {
			valueContext := ctx
			valueContext.prefix = valuePrefix
			return completeGeneratedValue(valueContext, cwd, *option), true
		}
	}
	if position == 0 && len(syntax.choices) > 0 && !strings.HasPrefix(ctx.prefix, "-") {
		return completeWords(ctx, syntax.choices, true), true
	}
	if optionsEnabled && strings.HasPrefix(ctx.prefix, "-") || syntax.keyEqualsValue {
		return completeWords(ctx, options, false), true
	}
	if kind, ok := generatedPositionalKind(syntax, position); ok {
		switch kind {
		case "file":
			return completePath(ctx, cwd, false), true
		case "directory":
			return completePath(ctx, cwd, true), true
		}
	}
	if looksLikePath(ctx.prefix) {
		return completePath(ctx, cwd, false), true
	}
	return nil, true
}

func generatedSyntaxOptionNames(syntax generatedCommandSyntax) []string {
	names := make([]string, 0, len(syntax.options)*2)
	for _, option := range syntax.options {
		if syntax.keyEqualsValue {
			names = append(names, option.name+"=")
			continue
		}
		if option.display != "" {
			names = append(names, option.display)
		} else if option.pflag {
			names = append(names, "--"+option.name)
		} else if syntax.unixNormalize && len(option.name) == 1 {
			names = append(names, "-"+option.name)
		} else {
			names = append(names, "--"+option.name)
		}
		if option.shorthand != "" {
			names = append(names, "-"+option.shorthand)
		}
	}
	return names
}

func matchGeneratedOption(syntax generatedCommandSyntax, token string) (*generatedOptionSyntax, string, bool, bool) {
	name, value, inline := strings.Cut(token, "=")
	if syntax.keyEqualsValue && !strings.HasPrefix(name, "-") {
		option, ok := generatedOptionByName(syntax, name)
		return option, value, inline, ok
	}
	candidate := strings.TrimLeft(name, "-")
	option, ok := generatedOptionByName(syntax, candidate)
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

func generatedOptionByName(syntax generatedCommandSyntax, name string) (*generatedOptionSyntax, bool) {
	for i := range syntax.options {
		if syntax.options[i].name == name {
			return &syntax.options[i], true
		}
	}
	return nil, false
}

func generatedInlineValue(syntax generatedCommandSyntax, prefix string) (string, string, bool) {
	name, value, ok := strings.Cut(prefix, "=")
	if !ok {
		return "", "", false
	}
	if syntax.keyEqualsValue {
		return name, value, true
	}
	option, _, _, found := matchGeneratedOption(syntax, name)
	if found {
		return option.name, value, true
	}
	return "", "", false
}

func isGeneratedOptionLike(token string, syntax generatedCommandSyntax) bool {
	if strings.HasPrefix(token, "-") {
		return true
	}
	if syntax.keyEqualsValue {
		name, _, inline := strings.Cut(token, "=")
		if inline {
			_, ok := generatedOptionByName(syntax, name)
			return ok
		}
	}
	return false
}

func completeGeneratedValue(ctx completionContext, cwd string, option generatedOptionSyntax) []completionCandidate {
	choices := option.choices
	if len(choices) == 0 {
		choices = completionDescriptionChoices(option.description)
	}
	var candidates []completionCandidate
	if len(choices) > 0 {
		candidates = append(candidates, completeWords(ctx, choices, true)...)
	}
	switch inferCompletionValueKind(option.description) {
	case completionFile:
		candidates = append(candidates, completePath(ctx, cwd, false)...)
	case completionDirectory:
		candidates = append(candidates, completePath(ctx, cwd, true)...)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Display < candidates[j].Display
	})
	return candidates
}

func generatedPositionalKind(syntax generatedCommandSyntax, position int) (string, bool) {
	if position < len(syntax.positionals) {
		return syntax.positionals[position], true
	}
	if syntax.variadic && len(syntax.positionals) > 0 {
		return syntax.positionals[len(syntax.positionals)-1], true
	}
	return "", false
}

func looksLikePath(value string) bool {
	return strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~")
}
