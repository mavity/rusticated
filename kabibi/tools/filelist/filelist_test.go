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

type rankingBatchEmbedder struct {
	vectors map[string][]float32
}

func (e *rankingBatchEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *rankingBatchEmbedder) EmbedBatchContext(_ context.Context, parts []ai.PromptPart) ([][]float32, error) {
	vectors := make([][]float32, len(parts))
	for i, part := range parts {
		vectors[i] = e.vectors[part.Text]
	}
	return vectors, nil
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

func TestSearchIteratorUpdatesAndKeepsBoundedTopMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.go", "beta.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package sample\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	iter := NewSearchIterator(context.Background(), dir, Options{
		Query:      "unrelated concept",
		MaxResults: 1,
		Embedder: &rankingBatchEmbedder{vectors: map[string][]float32{
			"alpha.go": {0.6, 0.8},
			"beta.go":  {0.8, 0.6},
		}},
	})
	defer iter.Close()

	sawCandidate := false
	for range iter.Updates() {
		progress := iter.Current()
		if progress.Phase != "candidate" {
			continue
		}
		sawCandidate = true
		if len(progress.Matches) > 1 {
			t.Fatalf("progress returned %d matches with max-results=1", len(progress.Matches))
		}
	}
	result, err := iter.Result()
	if err != nil {
		t.Fatalf("SearchIterator.Result(): %v", err)
	}
	if !sawCandidate {
		t.Fatal("iterator emitted no candidate progress")
	}
	progress := iter.Current()
	if len(progress.Matches) != 1 || progress.Matches[0].Path != "beta.go" {
		t.Fatalf("final top matches = %#v, want beta.go", progress.Matches)
	}
	if !strings.Contains(result, "beta.go") {
		t.Fatalf("result does not include top match: %q", result)
	}
}

func TestSearchIteratorPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	iter := NewSearchIterator(ctx, t.TempDir(), Options{Query: "*"})
	defer iter.Close()
	for range iter.Updates() {
	}
	if _, err := iter.Result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchIterator.Result() error = %v, want context.Canceled", err)
	}
}
