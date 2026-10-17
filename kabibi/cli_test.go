package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestExecutableDoesNotImportCharmUIStack(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps .: %v\n%s", err, output)
	}

	for _, dependency := range strings.Fields(string(output)) {
		switch dependency {
		case "github.com/charmbracelet/bubbletea",
			"github.com/charmbracelet/bubbles",
			"github.com/charmbracelet/glamour",
			"github.com/charmbracelet/lipgloss":
			t.Errorf("executable still imports Charm UI dependency %q", dependency)
		}
	}
}

func TestParseCLIHelpFormsMatch(t *testing.T) {
	var flagHelp, positionalHelp bytes.Buffer
	flagOpts, err := parseCLI([]string{"--help"}, &flagHelp)
	if err != nil {
		t.Fatalf("parseCLI(--help): %v", err)
	}
	positionalOpts, err := parseCLI([]string{"help"}, &positionalHelp)
	if err != nil {
		t.Fatalf("parseCLI(help): %v", err)
	}
	if !flagOpts.help || !positionalOpts.help {
		t.Fatalf("help flags: --help=%v positional=%v", flagOpts.help, positionalOpts.help)
	}
	if flagHelp.String() != positionalHelp.String() {
		t.Fatalf("help outputs differ:\n--help:\n%s\nhelp:\n%s", flagHelp.String(), positionalHelp.String())
	}
	for _, want := range []string{"--command", "--prompt", "--edit", "--verbose"} {
		if !strings.Contains(flagHelp.String(), want) {
			t.Errorf("help output missing %q", want)
		}
	}
}

func TestParseCLIEditorForms(t *testing.T) {
	for _, args := range [][]string{
		{"--edit", "notes.txt"},
		{"--edit=notes.txt"},
		{"edit", "notes.txt"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			opts, err := parseCLI(args, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseCLI(%q): %v", args, err)
			}
			if opts.edit != "notes.txt" {
				t.Fatalf("edit path = %q, want notes.txt", opts.edit)
			}
			if opts.aiFallback || len(opts.positional) != 0 {
				t.Fatalf("editor args also routed as prompt: %#v", opts)
			}
		})
	}
}

func TestParseCLIPositionalPromptAndUnknownFlagFallback(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "multiple positional words", args: []string{"Name", "a", "bridge", "in", "Paris"}, want: "Name a bridge in Paris"},
		{name: "unknown option and words", args: []string{"--unexpected", "one", "two"}, want: "--unexpected one two"},
		{name: "malformed recognized option", args: []string{"--verbose=maybe"}, want: "--verbose=maybe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseCLI(tt.args, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("parseCLI(%q): %v", tt.args, err)
			}
			if !opts.aiFallback || opts.aiPrompt != tt.want {
				t.Fatalf("AI fallback = (%v, %q), want (true, %q)", opts.aiFallback, opts.aiPrompt, tt.want)
			}
		})
	}
}

func TestParseCLISinglePositionalPreservedForScriptLookup(t *testing.T) {
	opts, err := parseCLI([]string{"script.sh"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if opts.aiFallback || len(opts.positional) != 1 || opts.positional[0] != "script.sh" {
		t.Fatalf("single positional = %#v, want script path", opts)
	}
}

func TestParseCLIPromptAndCommandOptions(t *testing.T) {
	opts, err := parseCLI([]string{"--verbose", "-p", "say hello"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseCLI: %v", err)
	}
	if !opts.verbose || opts.prompt != "say hello" || opts.aiFallback {
		t.Fatalf("parsed options = %#v, want verbose prompt mode", opts)
	}
}
