package shell

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFormatPrompt_DefaultFallback(t *testing.T) {
	cwd := filepath.Join(string(filepath.Separator), "home", "alice", "projects", "kabibi")
	env := map[string]string{
		"HOME": filepath.Join(string(filepath.Separator), "home", "alice"),
		"USER": "alice",
	}

	info := FormatPrompt("", cwd, env)
	expectedText := "~/projects/kabibi፧"
	if info.Text != expectedText {
		t.Fatalf("got text %q, want %q", info.Text, expectedText)
	}
	if info.Width != len([]rune(expectedText)) {
		t.Fatalf("got width %d, want %d", info.Width, len([]rune(expectedText)))
	}
}

func TestFormatPrompt_PS1Expansions(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "alice")
	cwd := filepath.Join(home, "work")
	env := map[string]string{
		"HOME": home,
		"USER": "alice",
	}
	fixedTime := time.Date(2026, 10, 16, 14, 30, 45, 0, time.UTC)

	tests := []struct {
		name      string
		ps1       string
		wantText  string
		wantWidth int
	}{
		{
			name:      "username and hostname",
			ps1:       `\u@\h:\w\$ `,
			wantText:  "alice@" + lookupHost(false) + ":~/work$ ",
			wantWidth: len([]rune("alice@" + lookupHost(false) + ":~/work$ ")),
		},
		{
			name:      "basename only",
			ps1:       `[\W]\$ `,
			wantText:  "[work]$ ",
			wantWidth: len([]rune("[work]$ ")),
		},
		{
			name:      "time formatting",
			ps1:       `\A \t `,
			wantText:  "14:30 14:30:45 ",
			wantWidth: 15,
		},
		{
			name:      "ansi color codes with zero-width brackets",
			ps1:       `\[\e[32m\]\w\[\e[0m\]\$ `,
			wantText:  "\x1b[32m~/work\x1b[0m$ ",
			wantWidth: len([]rune("~/work$ ")),
		},
		{
			name:      "literal backslash and unknown escapes",
			ps1:       `\\ \x `,
			wantText:  `\ \x `,
			wantWidth: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandPS1(tc.ps1, cwd, env, fixedTime)
			if got.Text != tc.wantText {
				t.Errorf("text: got %q, want %q", got.Text, tc.wantText)
			}
			if got.Width != tc.wantWidth {
				t.Errorf("width: got %d, want %d", got.Width, tc.wantWidth)
			}
		})
	}
}

func TestShell_DynamicPS1Update(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "child")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sh := New(ShellOptions{
		WorkingDir: dir,
		Prompt:     `\W\$ `,
	})

	// Initial prompt should be [dir basename]$
	p1, _ := sh.currentPromptLocked()
	expectedP1 := filepath.Base(dir) + "$ "
	if p1 != expectedP1 {
		t.Fatalf("initial prompt: got %q, want %q", p1, expectedP1)
	}

	// Change directory to child
	sh.ExecuteLine("cd child")

	p2, _ := sh.currentPromptLocked()
	expectedP2 := "child$ "
	if p2 != expectedP2 {
		t.Fatalf("after cd prompt: got %q, want %q", p2, expectedP2)
	}

	// Update PS1 dynamically inside the shell runner
	sh.ExecuteLine("export PS1='kabibi:\\W> '")

	p3, _ := sh.currentPromptLocked()
	expectedP3 := "kabibi:child> "
	if p3 != expectedP3 {
		t.Fatalf("after export PS1: got %q, want %q", p3, expectedP3)
	}
}
