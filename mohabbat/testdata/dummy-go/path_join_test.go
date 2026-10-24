package main

import (
	"path/filepath"
	"testing"
)

func TestFilepathJoinWindowsStylePaths(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("requires a Windows-style path separator")
	}

	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{
			name:  "rooted filesystem root stays under base",
			parts: []string{`C:\base`, `/`},
			want:  `C:\base`,
		},
		{
			name:  "relative child joins under base",
			parts: []string{`C:\base`, `child`},
			want:  `C:\base\child`,
		},
		{
			name:  "rooted child stays under base",
			parts: []string{`C:\base`, `/child`},
			want:  `C:\base\child`,
		},
		{
			name:  "drive-relative element",
			parts: []string{`C:`, `child`},
			want:  `C:child`,
		},
		{
			name:  "drive-relative base with rooted child",
			parts: []string{`C:`, `/child`},
			want:  `C:\child`,
		},
		{
			name:  "rooted child stays under UNC base",
			parts: []string{`\\server\share`, `/child`},
			want:  `\\server\share\child`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := filepath.Join(test.parts...); got != test.want {
				t.Fatalf("filepath.Join(%q) = %q, want %q", test.parts, got, test.want)
			}
		})
	}

	if got, want := filepath.VolumeName(`\\server\share\child`), `\\server\share`; got != want {
		t.Fatalf("filepath.VolumeName(UNC path) = %q, want %q", got, want)
	}
}
