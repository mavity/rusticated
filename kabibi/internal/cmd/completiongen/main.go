package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const urootModule = "github.com/u-root/u-root"

type flagInfo struct {
	name          string
	display       string
	shorthand     string
	pflag         bool
	takesValue    bool
	optionalValue bool
	description   string
	choices       []string
}

type commandInfo struct {
	flags          []flagInfo
	positional     []string
	choices        []string
	variadic       bool
	unixNormalize  bool
	keyEqualsValue bool
	pflag          bool
}

func main() {
	output := flag.String("out", "uroot_completion_generated.go", "generated Go output")
	registry := flag.String("registry", "ui/shell/uroot.go", "u-root command registry")
	flag.Parse()
	if err := generate(*output, *registry); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(output, registryPath string) error {
	moduleDir, err := goListModuleDir(urootModule)
	if err != nil {
		return err
	}
	registry, err := parseRegistry(registryPath)
	if err != nil {
		return err
	}
	packageCache := make(map[string]commandInfo)
	commands := make(map[string]commandInfo, len(registry))
	for name, importPath := range registry {
		if importPath == "" {
			commands[name] = commandInfo{}
			continue
		}
		info, ok := packageCache[importPath]
		if !ok {
			relative := strings.TrimPrefix(importPath, urootModule)
			if relative == importPath {
				continue
			}
			info, err = parseCommandPackage(filepath.Join(moduleDir, filepath.FromSlash(strings.TrimPrefix(relative, "/"))))
			if err != nil {
				return fmt.Errorf("extract syntax for %s (%s): %w", name, importPath, err)
			}
			packageCache[importPath] = info
		}
		commands[name] = info
	}
	return writeGenerated(output, commands)
}

func goListModuleDir(module string) (string, error) {
	command := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("locate module %s: %w: %s", module, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func parseRegistry(path string) (map[string]string, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	commands := make(map[string]string)
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.VAR {
			continue
		}
		for _, raw := range general.Specs {
			spec, ok := raw.(*ast.ValueSpec)
			if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "urootRegistry" || len(spec.Values) != 1 {
				continue
			}
			registry, ok := spec.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, element := range registry.Elts {
				entry, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				name, ok := stringLiteral(entry.Key)
				if !ok {
					continue
				}
				commands[name] = ""
				runner, ok := entry.Value.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				packageName, ok := runner.X.(*ast.Ident)
				if !ok {
					continue
				}
				if importPath := imports[packageName.Name]; importPath != "" {
					commands[name] = importPath
				}
			}
		}
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("%s has no urootRegistry entries", path)
	}
	return commands, nil
}

func parseCommandPackage(dir string) (commandInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return commandInfo{}, err
	}
	info := commandInfo{}
	flags := make(map[string]flagInfo)
	var packageName string
	var signalNames []string
	signalNamesSeen := make(map[string]bool)
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return commandInfo{}, err
		}
		packageName = file.Name.Name
		if file.Doc != nil {
			kinds, variadic := synopsisPositionKinds(file.Doc.Text())
			if len(info.positional) == 0 && len(kinds) > 0 {
				info.positional = kinds
				info.variadic = variadic
			}
			if len(info.choices) == 0 {
				info.choices = synopsisChoices(file.Doc.Text())
			}
		}
		flagSets := make(map[string]bool)
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.FuncDecl:
				if typed.Name.Name == "convertArgs" {
					info.keyEqualsValue = true
				}
			case *ast.AssignStmt:
				for _, value := range typed.Rhs {
					if isFlagSetConstructor(value) {
						pflagSet := isPFlagSetConstructor(value)
						for _, lhs := range typed.Lhs {
							if identifier, ok := lhs.(*ast.Ident); ok {
								flagSets[identifier.Name] = pflagSet
							}
						}
					}
				}
			case *ast.CallExpr:
				if selector, ok := typed.Fun.(*ast.SelectorExpr); ok {
					if selector.Sel.Name == "ArgsToGoArgs" {
						info.unixNormalize = true
					}
					receiver, ok := selector.X.(*ast.Ident)
					if ok {
						pflagSet, isFlagSet := flagSets[receiver.Name]
						if option, ok := parseFlagCall(selector.Sel.Name, typed.Args, pflagSet); isFlagSet && ok {
							key := option.name + "\x00" + option.shorthand
							flags[key] = mergeFlagInfo(flags[key], option)
							info.pflag = info.pflag || pflagSet
						}
					}
				}
			case *ast.CaseClause:
				for _, label := range typed.List {
					name, ok := stringLiteral(label)
					if !ok || !isOptionName(name) {
						continue
					}
					addCaseOption(flags, name, typed.Body, true)
				}
			case *ast.ValueSpec:
				for _, declaredName := range typed.Names {
					if strings.EqualFold(declaredName.Name, "signames") {
						for _, signal := range stringSliceLiteral(typed.Values) {
							if !signalNamesSeen[signal] {
								signalNames = append(signalNames, signal)
								signalNamesSeen[signal] = true
							}
						}
					}
				}
			case *ast.BasicLit:
				if typed.Kind == token.STRING {
					if usage, err := strconv.Unquote(typed.Value); err == nil {
						if isOptionName(usage) {
							key := strings.TrimLeft(usage, "-") + "\x00" + usage
							if _, exists := flags[key]; !exists {
								flags[key] = flagInfo{
									name:    strings.TrimLeft(usage, "-"),
									display: usage,
								}
							}
						}
						if len(info.positional) == 0 {
							kinds, variadic := synopsisPositionKinds(usage)
							if len(kinds) > 0 {
								info.positional = kinds
								info.variadic = variadic
							}
						}
						if len(info.choices) == 0 {
							info.choices = synopsisChoices(usage)
						}
					}
				}
			}
			return true
		})
		collectParserCharOptions(file, flags)
	}
	if packageName == "kill" && len(signalNames) > 0 {
		flags["l\x00-l"] = flagInfo{name: "l", display: "-l"}
		flags["s\x00-s"] = flagInfo{
			name: "s", display: "-s", takesValue: true, choices: signalNames,
		}
		flags["signal\x00--signal"] = flagInfo{
			name: "signal", display: "--signal", takesValue: true, choices: signalNames,
		}
		for _, signal := range signalNames {
			flags[signal+"\x00-"+signal] = flagInfo{
				name: signal, display: "-" + signal,
			}
		}
	}
	for _, option := range flags {
		info.flags = append(info.flags, option)
	}
	sort.Slice(info.flags, func(i, j int) bool {
		if info.flags[i].name != info.flags[j].name {
			return info.flags[i].name < info.flags[j].name
		}
		return info.flags[i].shorthand < info.flags[j].shorthand
	})
	return info, nil
}

func addCaseOption(flags map[string]flagInfo, name string, body []ast.Stmt, inferValueDetails bool) {
	takesValue := caseConsumesValue(body)
	optionalValue := caseHasOptionalValue(body)
	takesValue = takesValue || optionalValue
	description := ""
	var choices []string
	if inferValueDetails {
		description = caseValueDescription(body)
		choices = caseValueChoices(body)
	}
	if takesValue && description == "" && looksLikeFileOption(name) {
		description = "file path"
	}
	option := flagInfo{
		name:          strings.TrimLeft(name, "-"),
		display:       name,
		takesValue:    takesValue,
		optionalValue: optionalValue,
		description:   description,
		choices:       choices,
	}
	key := option.name + "\x00" + option.display
	flags[key] = mergeFlagInfo(flags[key], option)
}

func collectParserCharOptions(file *ast.File, flags map[string]flagInfo) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		name := strings.ToLower(function.Name.Name)
		if !strings.HasPrefix(name, "parse") {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			clause, ok := node.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, label := range clause.List {
				optionName, ok := completionOptionLabel(label)
				if ok && isOptionName(optionName) {
					addCaseOption(flags, optionName, clause.Body, len(clause.List) == 1)
				}
			}
			return true
		})
	}
}

func completionOptionLabel(expression ast.Expr) (string, bool) {
	if value, ok := stringLiteral(expression); ok {
		return value, true
	}
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.CHAR {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil || len([]rune(value)) != 1 {
		return "", false
	}
	return "-" + value, true
}

func mergeFlagInfo(existing, incoming flagInfo) flagInfo {
	if incoming.name == "" {
		incoming.name = existing.name
	}
	if incoming.display == "" {
		incoming.display = existing.display
	}
	if incoming.shorthand == "" {
		incoming.shorthand = existing.shorthand
	}
	incoming.pflag = incoming.pflag || existing.pflag
	incoming.takesValue = incoming.takesValue || existing.takesValue
	incoming.optionalValue = incoming.optionalValue || existing.optionalValue
	if incoming.description == "" {
		incoming.description = existing.description
	}
	if len(incoming.choices) == 0 {
		incoming.choices = existing.choices
	}
	return incoming
}

func stringSliceLiteral(values []ast.Expr) []string {
	for _, value := range values {
		literal, ok := value.(*ast.CompositeLit)
		if !ok {
			continue
		}
		var stringsFound []string
		for _, element := range literal.Elts {
			if item, ok := stringLiteral(element); ok {
				stringsFound = append(stringsFound, item)
			}
		}
		return stringsFound
	}
	return nil
}

func looksLikeFileOption(name string) bool {
	lower := strings.ToLower(strings.TrimLeft(name, "-"))
	switch lower {
	case "file", "filename", "path", "dir", "directory", "reference",
		"input", "output", "source", "dest", "destination", "target",
		"target-directory", "work-tree", "git-dir", "config", "config-file",
		"input-file", "output-file", "file-path", "path-to-file":
		return true
	}
	return false
}

func caseHasOptionalValue(body []ast.Stmt) bool {
	hasOptionalValue := false
	for _, statement := range body {
		ast.Inspect(statement, func(node ast.Node) bool {
			if identifier, ok := node.(*ast.Ident); ok && identifier.Name == "hasValue" {
				hasOptionalValue = true
				return false
			}
			return !hasOptionalValue
		})
		if hasOptionalValue {
			break
		}
	}
	return hasOptionalValue && !caseConsumesValue(body)
}

func caseValueChoices(body []ast.Stmt) []string {
	seen := make(map[string]bool)
	var choices []string
	add := func(value string) {
		if value == "" || isOptionName(value) || seen[value] {
			return
		}
		seen[value] = true
		choices = append(choices, value)
	}
	for _, statement := range body {
		ast.Inspect(statement, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CaseClause:
				for _, label := range typed.List {
					if value, ok := stringLiteral(label); ok {
						add(value)
					}
				}
			case *ast.BinaryExpr:
				if typed.Op != token.EQL && typed.Op != token.NEQ {
					return true
				}
				if value, ok := stringLiteral(typed.X); ok {
					add(value)
				}
				if value, ok := stringLiteral(typed.Y); ok {
					add(value)
				}
			}
			return true
		})
	}
	return choices
}

func isFlagSetConstructor(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return selector.Sel.Name == "NewFlagSet"
}

func isPFlagSetConstructor(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "pflag"
}

func parseFlagCall(method string, args []ast.Expr, pflagSet bool) (flagInfo, bool) {
	shorthandMethod := strings.HasSuffix(method, "P")
	if shorthandMethod {
		method = strings.TrimSuffix(method, "P")
	}
	varValue := strings.HasSuffix(method, "Var")
	if varValue {
		method = strings.TrimSuffix(method, "Var")
	}

	switch method {
	case "Bool", "BoolFunc":
	case "String", "Int", "Int64", "Uint", "Uint64", "Float64", "Duration",
		"StringSlice", "StringArray", "IntSlice", "Count", "Var", "Func":
	default:
		return flagInfo{}, false
	}
	nameIndex, descriptionIndex := 0, 0
	switch {
	case method == "Var":
		nameIndex, descriptionIndex = 1, 2
	case varValue && shorthandMethod:
		nameIndex, descriptionIndex = 1, 4
	case varValue:
		nameIndex, descriptionIndex = 1, 3
	case shorthandMethod:
		nameIndex, descriptionIndex = 0, 3
	case method == "BoolFunc" || method == "Func":
		nameIndex, descriptionIndex = 0, 1
	default:
		descriptionIndex = 2
	}
	if nameIndex >= len(args) {
		return flagInfo{}, false
	}
	name, ok := stringLiteral(args[nameIndex])
	if !ok || name == "" {
		return flagInfo{}, false
	}
	info := flagInfo{
		name:       name,
		pflag:      pflagSet,
		takesValue: method != "Bool" && method != "BoolFunc",
	}
	if shorthandMethod && nameIndex+1 < len(args) {
		info.shorthand, _ = stringLiteral(args[nameIndex+1])
	}
	if descriptionIndex < len(args) {
		info.description, _ = stringLiteral(args[descriptionIndex])
	}
	return info, true
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func synopsisPositionKinds(comment string) ([]string, bool) {
	lower := strings.ToLower(comment)
	index := strings.Index(lower, "synopsis:")
	if index < 0 {
		index = strings.Index(lower, "usage:")
		if index < 0 {
			return nil, false
		}
	}
	headerLength := len("usage:")
	if strings.Contains(lower[index:], "synopsis:") {
		headerLength = len("synopsis:")
	}
	synopsis := strings.TrimSpace(comment[index+headerLength:])
	var kinds []string
	variadic := false
	firstField := true
	inOptionGroup := false
	for _, line := range strings.Split(synopsis, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		for _, field := range strings.Fields(line) {
			if firstField {
				firstField = false
				continue
			}
			if inOptionGroup {
				if strings.Contains(field, "]") {
					inOptionGroup = false
				}
				continue
			}
			if strings.HasPrefix(field, "[-") || strings.HasPrefix(field, "[+") {
				inOptionGroup = !strings.Contains(field, "]")
				continue
			}
			fieldVariadic := strings.Contains(field, "...")
			field = strings.Trim(field, "[](),")
			field = strings.TrimRight(field, ".")
			field = strings.Trim(field, "[](),")
			upper := strings.ToUpper(field)
			if field == "" || strings.HasPrefix(field, "-") ||
				strings.Contains(upper, "OPTION") ||
				!containsUppercaseLetter(field) {
				continue
			}
			if fieldVariadic {
				variadic = true
			}
			kinds = append(kinds, positionalKind(upper))
		}
	}
	if len(kinds) == 0 && len(synopsisChoices(comment)) > 0 {
		kinds = append(kinds, "value")
		for _, field := range strings.Fields(synopsis) {
			if strings.HasPrefix(field, "<") && strings.Contains(field, ">") {
				placeholder := field[strings.IndexByte(field, '<')+1 : strings.IndexByte(field, '>')]
				kinds = append(kinds, positionalKind(strings.ToUpper(placeholder)))
			}
		}
	}
	return kinds, variadic
}

func synopsisChoices(source string) []string {
	lower := strings.ToLower(source)
	start := -1
	if object := strings.Index(lower, "object :="); object >= 0 {
		start = strings.Index(source[object:], "{")
		if start >= 0 {
			start += object
		}
	} else if synopsis := strings.Index(lower, "synopsis:"); synopsis >= 0 {
		start = strings.Index(source[synopsis:], "[")
		if start >= 0 {
			start += synopsis
		}
	}
	if start < 0 {
		return nil
	}
	open, close := source[start], byte('}')
	if open == '[' {
		close = ']'
	}
	end := strings.IndexByte(source[start+1:], close)
	if end < 0 {
		return nil
	}
	content := source[start+1 : start+1+end]
	if !strings.Contains(content, "|") {
		return nil
	}
	var choices []string
	for _, alternative := range strings.Split(content, "|") {
		fields := strings.Fields(strings.TrimSpace(alternative))
		if len(fields) == 0 || strings.HasPrefix(fields[0], "<") {
			continue
		}
		choice := strings.Trim(fields[0], "[]{}(),")
		if choice == "" || strings.ContainsAny(choice, "<>") ||
			strings.EqualFold(choice, "options") {
			continue
		}
		choices = append(choices, choice)
	}
	return choices
}

func caseConsumesValue(body []ast.Stmt) bool {
	consumes := false
	for _, statement := range body {
		ast.Inspect(statement, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.IncDecStmt:
				consumes = true
			case *ast.CallExpr:
				if identifier, ok := typed.Fun.(*ast.Ident); ok &&
					(identifier.Name == "nextArg" || identifier.Name == "requireArg" ||
						identifier.Name == "value" || identifier.Name == "getValue") {
					consumes = true
				}
			}
			return !consumes
		})
		if consumes {
			return true
		}
	}
	return false
}

func caseValueDescription(body []ast.Stmt) string {
	isFileValue := false
	for _, statement := range body {
		ast.Inspect(statement, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.CallExpr:
				switch function := typed.Fun.(type) {
				case *ast.Ident:
					if function.Name == "addPatternFile" {
						isFileValue = true
					}
				case *ast.SelectorExpr:
					if function.Sel.Name == "Open" || function.Sel.Name == "OpenFile" ||
						function.Sel.Name == "ReadFile" {
						isFileValue = true
					}
				}
			}
			return !isFileValue
		})
		if isFileValue {
			return "file path"
		}
	}
	return ""
}

func isOptionName(name string) bool {
	if len(name) < 2 || !strings.HasPrefix(name, "-") || name == "--" {
		return false
	}
	for _, r := range strings.TrimLeft(name, "-") {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func containsUppercaseLetter(value string) bool {
	for _, r := range value {
		if r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func positionalKind(name string) string {
	for _, keyword := range []string{"FILE", "PATH", "DIR", "SOURCE", "DEST"} {
		if strings.Contains(name, keyword) {
			return "file"
		}
	}
	return "value"
}

func writeGenerated(path string, commands map[string]commandInfo) error {
	var source bytes.Buffer
	source.WriteString("// Code generated by completiongen; DO NOT EDIT.\n\npackage shell\n\n")
	source.WriteString("var generatedURootSyntax = map[string]generatedCommandSyntax{\n")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		info := commands[name]
		fmt.Fprintf(&source, "%q: {unixNormalize: %t, keyEqualsValue: %t, pflag: %t, variadic: %t, positionals: []string{",
			name, info.unixNormalize, info.keyEqualsValue, info.pflag, info.variadic)
		for _, kind := range info.positional {
			fmt.Fprintf(&source, "%q,", kind)
		}
		source.WriteString("}, choices: []string{")
		for _, choice := range info.choices {
			fmt.Fprintf(&source, "%q,", choice)
		}
		source.WriteString("}, options: []generatedOptionSyntax{")
		for _, option := range info.flags {
			fmt.Fprintf(&source, "{name:%q, display:%q, shorthand:%q, pflag:%t, takesValue:%t, optionalValue:%t, description:%q, choices: []string{",
				option.name, option.display, option.shorthand, option.pflag, option.takesValue,
				option.optionalValue, option.description)
			for _, choice := range option.choices {
				fmt.Fprintf(&source, "%q,", choice)
			}
			source.WriteString("}},")
		}
		source.WriteString("}},\n")
	}
	source.WriteString("}\n")
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return err
	}
	return os.WriteFile(path, formatted, 0o644)
}
