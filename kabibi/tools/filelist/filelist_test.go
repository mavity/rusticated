package filelist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai"
)

type testBatchEmbedder struct {
	batchCalls int
	err        error
}

func (e *testBatchEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *testBatchEmbedder) EmbedBatchContext(_ context.Context, parts []ai.PromptPart) ([][]float32, error) {
	e.batchCalls++
	if e.err != nil {
		return nil, e.err
	}
	vectors := make([][]float32, len(parts))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestListFiles_GlobExecutableAndContentSearch(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "ui", "shell")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(subDir, "shell.go"), []byte("package shell\n// previouslyEjected marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build.sh"), []byte("#!/bin/sh\necho build\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Recursive doublestar glob '**/*.go'
	out, err := ListFiles(dir, Options{Query: "**/*.go"})
	if err != nil {
		t.Fatalf("ListFiles glob failed: %v", err)
	}
	if !strings.Contains(out, "ui/shell/shell.go") || !strings.Contains(out, "mode: glob") {
		t.Fatalf("unexpected glob output:\n%s", out)
	}

	// 2. Top-level '*' shows `d` for directory and `x ... *` for build.sh
	outAll, err := ListFiles(dir, Options{Query: "*"})
	if err != nil {
		t.Fatalf("ListFiles '*' failed: %v", err)
	}
	if !strings.Contains(outAll, "d       -  ui/") {
		t.Fatalf("expected directory entry 'd       -  ui/', got:\n%s", outAll)
	}
	if !strings.Contains(outAll, "build.sh*") {
		t.Fatalf("expected executable marker on build.sh*, got:\n%s", outAll)
	}

	// 3. Content search finds previouslyEjected with line number
	outContent, err := ListFiles(dir, Options{Query: "previouslyEjected", SearchContent: true})
	if err != nil {
		t.Fatalf("ListFiles content search failed: %v", err)
	}
	if !strings.Contains(outContent, "ui/shell/shell.go") || !strings.Contains(outContent, "[match: L2") {
		t.Fatalf("expected content match at L2 in ui/shell/shell.go, got:\n%s", outContent)
	}
}

func TestListFilesSemanticSearchBatchesAndPropagatesErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "alpha.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	embedder := &testBatchEmbedder{}
	out, err := ListFiles(dir, Options{Query: "zzxyvvq", Embedder: embedder})
	if err != nil {
		t.Fatalf("semantic ListFiles(): %v", err)
	}
	if !strings.Contains(out, "mode: semantic") || !strings.Contains(out, "src/alpha.go") {
		t.Fatalf("semantic search output = %q", out)
	}
	if embedder.batchCalls != 1 {
		t.Fatalf("path batch calls = %d, want one", embedder.batchCalls)
	}

	embedFailure := errors.New("embedding service unavailable")
	embedder = &testBatchEmbedder{err: embedFailure}
	_, err = ListFiles(dir, Options{Query: "zzxyvvq", Embedder: embedder})
	if !errors.Is(err, embedFailure) {
		t.Fatalf("ListFiles() error = %v, want %v", err, embedFailure)
	}
}
