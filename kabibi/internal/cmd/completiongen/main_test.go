package main

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSynopsisPositionKinds(t *testing.T) {
	tests := []struct {
		name     string
		synopsis string
		want     []string
		variadic bool
	}{
		{"cat files", "Synopsis:\n\n\tcat [-u] [FILES]...", []string{"file"}, true},
		{"copy source destination", "Usage: cp [OPTION]... SOURCE... DEST", []string{"file", "file"}, true},
		{"grep pattern and files", "Usage: grep PATTERN [FILE...]", []string{"value", "file"}, true},
		{"option values are not positionals", "Synopsis:\n\n\tdate [-d DATE] [+FORMAT]", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, variadic := synopsisPositionKinds(test.synopsis)
			if !reflect.DeepEqual(got, test.want) || variadic != test.variadic {
				t.Fatalf("synopsisPositionKinds() = %v, %t; want %v, %t",
					got, variadic, test.want, test.variadic)
			}
		})
	}
}

func TestSynopsisChoices(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"stty synopsis", "Synopsis:\n\tstty [pretty | dump | size | load <file> | raw | <options>...]", []string{"pretty", "dump", "size", "load", "raw"}},
		{"ip help", "OBJECT := { address | help | link | route }", []string{"address", "help", "link", "route"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := synopsisChoices(test.text)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("synopsisChoices() = %v; want %v", got, test.want)
			}
		})
	}
}

func TestParseFlagCallPreservesValueTypes(t *testing.T) {
	tests := []struct {
		expression string
		pflag      bool
		want       flagInfo
	}{
		{`fs.BoolVar(&value, "verbose", false, "show details")`, false,
			flagInfo{name: "verbose", description: "show details"}},
		{`fs.StringVar(&value, "output", "", "output file")`, false,
			flagInfo{name: "output", takesValue: true, description: "output file"}},
		{`fs.StringVarP(&value, "output", "o", "", "output file")`, true,
			flagInfo{name: "output", shorthand: "o", pflag: true, takesValue: true, description: "output file"}},
	}
	for _, test := range tests {
		expression, err := parser.ParseExpr(test.expression)
		if err != nil {
			t.Fatal(err)
		}
		call := expression.(*ast.CallExpr)
		method := call.Fun.(*ast.SelectorExpr).Sel.Name
		got, ok := parseFlagCall(method, call.Args, test.pflag)
		if !ok || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parseFlagCall(%q) = %#v, %t; want %#v, true",
				test.expression, got, ok, test.want)
		}
	}
}

func TestParseCommandPackageExtractsExceptionalSyntax(t *testing.T) {
	for _, command := range []string{"grep", "ip", "kill", "rg", "stty"} {
		t.Run(command, func(t *testing.T) {
			dir := filepath.Join("..", "..", "..", "..", "u-root", "cmds", "core", command)
			info, err := parseCommandPackage(dir)
			if err != nil {
				t.Fatal(err)
			}
			requireOption := func(display string) flagInfo {
				t.Helper()
				for _, option := range info.flags {
					if option.display == display {
						return option
					}
				}
				t.Fatalf("no generated option %q in %#v", display, info.flags)
				return flagInfo{}
			}
			switch command {
			case "grep":
				if got := requireOption("--file"); got.description != "file path" ||
					!got.takesValue || got.optionalValue {
					t.Fatalf("--file description = %q; want file path", got.description)
				}
				if got := requireOption("--color"); !reflect.DeepEqual(got.choices, []string{"always", "never", "auto"}) ||
					!got.takesValue || !got.optionalValue {
					t.Fatalf("--color syntax = %#v; want optional enum value", got)
				}
				if got := requireOption("--context"); got.description == "file path" {
					t.Fatalf("--context was incorrectly classified as a path: %#v", got)
				}
				requireOption("-e")
				requireOption("-f")
			case "ip":
				if !hasString(info.choices, "address") || !hasString(info.choices, "route") {
					t.Fatalf("ip choices = %v; want address and route", info.choices)
				}
			case "kill":
				if got := requireOption("-s"); len(got.choices) < 10 || !hasString(got.choices, "SIGTERM") {
					t.Fatalf("kill -s choices = %v; want source signal names", got.choices)
				}
			case "rg":
				if got := requireOption("--file"); got.description != "file path" {
					t.Fatalf("--file description = %q; want file path", got.description)
				}
				if got := requireOption("-f"); got.description != "file path" ||
					!got.takesValue || !hasString(got.choices, "-") {
					t.Fatalf("-f syntax = %#v; want a file value with stdin choice", got)
				}
				if got := requireOption("--max-filesize"); got.description == "file path" {
					t.Fatalf("--max-filesize was incorrectly classified as a path: %#v", got)
				}
				requireOption("-g")
				requireOption("-t")
			case "stty":
				if !reflect.DeepEqual(info.choices, []string{"pretty", "dump", "size", "load", "raw"}) {
					t.Fatalf("stty choices = %v", info.choices)
				}
				if !reflect.DeepEqual(info.positional, []string{"value", "file", "value"}) {
					t.Fatalf("stty positional kinds = %v", info.positional)
				}
			}
		})
	}
}

func hasString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func TestParseRegistryIncludesEveryRegisteredRunner(t *testing.T) {
	registryPath := filepath.Join("..", "..", "..", "ui", "shell", "uroot.go")
	commands, err := parseRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 85 {
		t.Fatalf("registry has %d names, want 85", len(commands))
	}
	if commands["more"] != "" {
		t.Fatalf("wrapped command more unexpectedly maps to %q", commands["more"])
	}
}
