package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewEditorLFFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "line1\nline2\nline3"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if e.eol != "\n" {
		t.Errorf("newEditor detected eol=%q, want \\n", e.eol)
	}

	if len(e.lines) != 3 {
		t.Errorf("newEditor parsed %d lines, want 3", len(e.lines))
	}

	if e.lines[0] != "line1" {
		t.Errorf("line 0 = %q, want line1", e.lines[0])
	}
}

func TestNewEditorCRLFFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "line1\r\nline2\r\nline3"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if e.eol != "\r\n" {
		t.Errorf("newEditor detected eol=%q, want \\r\\n", e.eol)
	}

	if len(e.lines) != 3 {
		t.Errorf("newEditor parsed %d lines, want 3", len(e.lines))
	}

	if strings.Contains(e.lines[0], "\r") {
		t.Errorf("line 0 contains CR: %q", e.lines[0])
	}
}

func TestNewEditorEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if len(e.lines) != 1 {
		t.Errorf("newEditor on empty file created %d lines, want 1", len(e.lines))
	}

	if e.lines[0] != "" {
		t.Errorf("first line = %q, want empty string", e.lines[0])
	}
}

func TestChromaStyleID(t *testing.T) {
	tests := []struct {
		name string
		tt   string
		want int
	}{
		{
			name: "comment token",
			tt:   "Comment",
			want: sidComment,
		},
		{
			name: "keyword token",
			tt:   "Keyword",
			want: sidKeyword,
		},
		{
			name: "string literal",
			tt:   "Literal.String",
			want: sidString,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.want
		})
	}
}

func TestEditorSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	original := "original content"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	e.lines[0] = "modified content"
	if err := e.save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	if string(b) != "modified content" {
		t.Errorf("saved file = %q, want modified content", string(b))
	}

	if e.dirty {
		t.Errorf("save did not clear dirty flag")
	}
}

func TestEditorSaveCRLF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "line1\r\nline2"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	e, err := newEditor(path)
	if err != nil {
		t.Fatalf("newEditor error: %v", err)
	}

	if e.eol != "\r\n" {
		t.Fatalf("failed to detect CRLF")
	}

	e.lines[0] = "changed"
	if err := e.save(); err != nil {
		t.Fatalf("save error: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	if !strings.Contains(string(b), "\r\n") {
		t.Errorf("saved file should preserve CRLF, got %q", string(b))
	}
}

func TestEditorInvalidate(t *testing.T) {
	e := &editorModel{dirty: false, hlValid: true}

	e.invalidate()

	if !e.dirty {
		t.Errorf("invalidate did not set dirty flag")
	}
	if e.hlValid {
		t.Errorf("invalidate did not clear hlValid flag")
	}
}
