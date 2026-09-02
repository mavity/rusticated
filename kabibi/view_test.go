package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestExpandTabs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		tabWidth int
		want     string
	}{
		{
			name:     "no tabs",
			input:    "hello",
			tabWidth: 4,
			want:     "hello",
		},
		{
			name:     "tab at start",
			input:    "\thello",
			tabWidth: 4,
			want:     "    hello",
		},
		{
			name:     "tab in middle",
			input:    "ab\tcd",
			tabWidth: 4,
			want:     "ab  cd",
		},
		{
			name:     "multiple tabs",
			input:    "a\tb\tc",
			tabWidth: 4,
			want:     "a   b   c",
		},
		{
			name:     "tab at column boundary",
			input:    "abcd\txy",
			tabWidth: 4,
			want:     "abcd    xy",
		},
		{
			name:     "empty string",
			input:    "",
			tabWidth: 4,
			want:     "",
		},
		{
			name:     "only tab",
			input:    "\t",
			tabWidth: 4,
			want:     "    ",
		},
		{
			name:     "tab width 2",
			input:    "a\tb",
			tabWidth: 2,
			want:     "a b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTabs(tt.input, tt.tabWidth)
			if got != tt.want {
				t.Errorf("expandTabs(%q, %d) = %q, want %q", tt.input, tt.tabWidth, got, tt.want)
			}
		})
	}
}

func TestDirTitleName(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "relative current dir",
			path: ".",
			want: " . ",
		},
		{
			name: "normal directory",
			path: "mydir",
			want: " mydir ",
		},
		{
			name: "nested path",
			path: "/home/user/projects",
			want: " projects ",
		},
		{
			name: "root slash",
			path: "/",
			want: " / ",
		},
		{
			name: "path with trailing slash",
			path: "/home/user/",
			want: " user ",
		},
		{
			name: "single directory",
			path: "home",
			want: " home ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dirTitleName(tt.path)
			if got != tt.want {
				t.Errorf("dirTitleName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestTruncateStringToWidth(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{
			name:  "no truncation needed",
			input: "hello",
			width: 10,
			want:  "hello",
		},
		{
			name:  "exact width",
			input: "hello",
			width: 5,
			want:  "hello",
		},
		{
			name:  "truncate with ellipsis",
			input: "hello world",
			width: 6,
			want:  "hello",
		},
		{
			name:  "empty string",
			input: "",
			width: 5,
			want:  "",
		},
		{
			name:  "width too small",
			input: "hello",
			width: 1,
			want:  "",
		},
		{
			name:  "width 2",
			input: "hello",
			width: 2,
			want:  "h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateStringToWidth(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("truncateStringToWidth(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

func TestRenderProgressBar(t *testing.T) {
	bg := lipgloss.Color("#3A3A3A")

	tests := []struct {
		name       string
		percentage int
		width      int
		finished   bool
		check      func(string) bool
	}{
		{
			name:       "0 percent",
			percentage: 0,
			width:      10,
			finished:   false,
			check:      func(s string) bool { return len(s) > 0 },
		},
		{
			name:       "100 percent",
			percentage: 100,
			width:      10,
			finished:   false,
			check:      func(s string) bool { return len(s) > 0 },
		},
		{
			name:       "50 percent",
			percentage: 50,
			width:      10,
			finished:   false,
			check:      func(s string) bool { return len(s) > 0 },
		},
		{
			name:       "finished",
			percentage: 100,
			width:      10,
			finished:   true,
			check:      func(s string) bool { return len(s) > 0 },
		},
		{
			name:       "negative percentage clamped",
			percentage: -10,
			width:      10,
			finished:   false,
			check:      func(s string) bool { return len(s) > 0 },
		},
		{
			name:       "over 100 clamped",
			percentage: 150,
			width:      10,
			finished:   false,
			check:      func(s string) bool { return len(s) > 0 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderProgressBar(tt.percentage, tt.width, bg, tt.finished)
			if !tt.check(got) {
				t.Errorf("renderProgressBar(%d, %d, ..., %v) returned unexpected result: %q", tt.percentage, tt.width, tt.finished, got)
			}
		})
	}
}
