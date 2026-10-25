package slashcmd

import (
	"reflect"
	"testing"
)

func TestMatchesFiltersBrowserCommands(t *testing.T) {
	nonBrowser := Matches("/de", false)
	if len(nonBrowser) != 1 || nonBrowser[0].Name != "/delete" {
		t.Fatalf("non-browser /de matches = %#v", nonBrowser)
	}

	browser := Matches("/d", true)
	names := make([]string, len(browser))
	for i, command := range browser {
		names[i] = command.Name
	}
	want := []string{"/delete", "/download"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("browser /d matches = %#v, want %#v", names, want)
	}
}

func TestInputToken(t *testing.T) {
	for _, tt := range []struct {
		input string
		token string
		args  string
		slash bool
	}{
		{input: "/copy", token: "/copy", slash: true},
		{input: "  /copy  one two", token: "/copy", args: "one two", slash: true},
		{input: "echo /copy", slash: false},
	} {
		token, args, isSlash := InputToken(tt.input)
		if token != tt.token || args != tt.args || isSlash != tt.slash {
			t.Errorf("InputToken(%q) = (%q, %q, %t), want (%q, %q, %t)",
				tt.input, token, args, isSlash, tt.token, tt.args, tt.slash)
		}
	}
}

func TestParseSlashCommand(t *testing.T) {
	invocation, recognized, err := Parse(`/copy "file with spaces.txt" target`)
	if err != nil {
		t.Fatal(err)
	}
	if !recognized {
		t.Fatal("slash command was not recognized")
	}
	if invocation.Name != "/copy" {
		t.Fatalf("command name = %q, want /copy", invocation.Name)
	}
	if want := []string{"file with spaces.txt", "target"}; !reflect.DeepEqual(invocation.Args, want) {
		t.Fatalf("arguments = %#v, want %#v", invocation.Args, want)
	}
}

func TestParseSlashCommandRejectsShellOperatorsAndExpansions(t *testing.T) {
	for _, line := range []string{
		"/copy source target | cat",
		"/copy $SOURCE target",
		"/copy source > output",
	} {
		_, recognized, err := Parse(line)
		if !recognized || err == nil {
			t.Errorf("Parse(%q) = recognized %t, error %v; want recognized with error", line, recognized, err)
		}
	}
}
