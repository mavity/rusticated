package main

import (
	"testing"
)

func TestExtractTokenText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "valid token with text",
			raw:  `{"role":"assistant","content":[{"type":"text","text":"Hello"}]}`,
			want: "Hello",
		},
		{
			name: "multiple content items (takes first)",
			raw:  `{"content":[{"type":"text","text":"First"},{"type":"text","text":"Second"}]}`,
			want: "First",
		},
		{
			name: "missing text field returns empty",
			raw:  `{"content":[{"type":"text"}]}`,
			want: "",
		},
		{
			name: "empty content array",
			raw:  `{"content":[]}`,
			want: `{"content":[]}`,
		},
		{
			name: "malformed JSON",
			raw:  `{invalid json`,
			want: `{invalid json`,
		},
		{
			name: "plain text (not JSON)",
			raw:  "just plain text",
			want: "just plain text",
		},
		{
			name: "empty string",
			raw:  "",
			want: "",
		},
		{
			name: "empty text field",
			raw:  `{"content":[{"type":"text","text":""}]}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTokenText(tt.raw)
			if got != tt.want {
				t.Errorf("extractTokenText(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestLibExt(t *testing.T) {
	ext := libExt()

	if ext == "" {
		t.Errorf("libExt() returned empty string")
	}

	if ext != ".dll" && ext != ".so" && ext != ".dylib" {
		t.Errorf("libExt() = %q, want one of [.dll, .so, .dylib]", ext)
	}
}
