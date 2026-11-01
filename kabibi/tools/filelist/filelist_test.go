package filelist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
		if len(vectors[i]) == 0 {
			vectors[i] = []float32{1, 0}
		}
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
	if !strings.Contains(outAll, "ui/  -") {
		t.Fatalf("expected directory entry with directory size marker, got:\n%s", outAll)
	}
	if !strings.Contains(outAll, "build.sh*") {
		t.Fatalf("expected executable marker on build.sh*, got:\n%s", outAll)
	}

	// 3. Content search finds previouslyEjected with line number
	outContent, err := ListFiles(dir, Options{Query: "previouslyEjected", SearchContent: true})
	if err != nil {
		t.Fatalf("ListFiles content search failed: %v", err)
	}
	if !strings.Contains(outContent, "ui/shell/shell.go") || !strings.Contains(outContent, `"// previouslyEjected marker"`) {
		t.Fatalf("expected the matching content quote in ui/shell/shell.go, got:\n%s", outContent)
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
	if embedder.batchCalls != 3 {
		t.Fatalf("query, candidate batch, and quote embedding calls = %d, want three", embedder.batchCalls)
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

func TestListFilesQuoteLimitOnlyEnrichesRequestedTopMatches(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alpha.go", "beta.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("relevant line in "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := ListFiles(dir, Options{
		Query:      "unrelated semantic phrase",
		MaxResults: 2,
		QuoteLimit: 1,
		Embedder: &rankingBatchEmbedder{vectors: map[string][]float32{
			"alpha.go": {0.6, 0.8},
			"beta.go":  {0.8, 0.6},
		}},
	})
	if err != nil {
		t.Fatalf("ListFiles(): %v", err)
	}
	lines := strings.Split(out, "\n")
	var rows []string
	for _, line := range lines {
		if strings.Contains(line, ".go") {
			rows = append(rows, line)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("result rows = %#v", rows)
	}
	if !strings.Contains(rows[0], `"relevant line in beta.go"`) || strings.Contains(rows[1], `"relevant line in alpha.go"`) {
		t.Fatalf("quote limit did not enrich only the top result: %#v", rows)
	}
}

func TestFormatMatchOrdersFieldsAndTruncatesQuote(t *testing.T) {
	match := Match{
		Path:     "src/preprocessor.go",
		Size:     4096,
		Score:    0.79,
		HasScore: true,
		Quote:    "  image   resize happens with a long descriptive line  ",
	}
	formatted := FormatMatch(match, 0)
	scoreAt := strings.Index(formatted, "0.79")
	pathAt := strings.Index(formatted, "src/preprocessor.go")
	sizeAt := strings.Index(formatted, "4.0K")
	quoteAt := strings.Index(formatted, `"image resize happens`)
	if scoreAt < 0 || pathAt < scoreAt || sizeAt < pathAt || quoteAt < sizeAt {
		t.Fatalf("formatted match fields are not score, path, size, quote: %q", formatted)
	}

	truncated := FormatMatch(match, 40)
	if !strings.HasSuffix(truncated, "…") {
		t.Fatalf("truncated match does not end in ellipsis: %q", truncated)
	}
	if got := FormatAveragePerFile(250 * time.Millisecond); got != "250.0ms" {
		t.Fatalf("FormatAveragePerFile() = %q, want 250.0ms", got)
	}
}

type quoteBisectionEmbedder struct {
	calls [][]string
}

func (e *quoteBisectionEmbedder) Embed([]ai.PromptPart) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (e *quoteBisectionEmbedder) EmbedBatchContext(_ context.Context, parts []ai.PromptPart) ([][]float32, error) {
	inputs := make([]string, len(parts))
	vectors := make([][]float32, len(parts))
	for i, part := range parts {
		inputs[i] = part.Text
		switch strings.TrimSpace(part.Text) {
		case "image resize and rotate":
			vectors[i] = []float32{0.6, 0.8}
		case "# unrelated heading", "other line":
			vectors[i] = []float32{0, 1}
		default:
			vectors[i] = []float32{1, 0}
		}
	}
	e.calls = append(e.calls, inputs)
	return vectors, nil
}

func TestRelevantQuoteBisectsAtLineBoundaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preprocessor.go")
	content := "# unrelated heading\nimage resize and rotate\nother line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	embedder := &quoteBisectionEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	quote, err := relevantQuote(context.Background(), fileEntry{
		relPath: "preprocessor.go",
		absPath: path,
		size:    int64(len(content)),
	}, []float32{1, 0}, scheduler, nil)
	if err != nil {
		t.Fatalf("relevantQuote(): %v", err)
	}
	if quote != "image resize and rotate" {
		t.Fatalf("quote = %q, want most similar whole line", quote)
	}
	wholeLines := map[string]bool{
		"# unrelated heading":     true,
		"image resize and rotate": true,
		"other line":              true,
	}
	for _, call := range embedder.calls {
		for _, input := range call {
			for _, line := range strings.Split(input, "\n") {
				if !wholeLines[line] {
					t.Fatalf("bisection split through a line: %q", input)
				}
			}
		}
	}
}

func TestRelevantQuoteReusesTheScoredChunkVector(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte("relevant source line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	embedder := &quoteBisectionEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	quote, err := relevantQuote(context.Background(), embedderReuseEntry(path), []float32{1, 0}, scheduler, nil)
	if err != nil {
		t.Fatalf("relevantQuote(): %v", err)
	}
	if quote != "relevant source line" {
		t.Fatalf("quote = %q", quote)
	}
	if len(embedder.calls) != 0 {
		t.Fatalf("quote extraction made %d embedding calls, want reused scored vector", len(embedder.calls))
	}
}

func TestSplitQuoteChunksRespectsByteLimitAndLineBoundaries(t *testing.T) {
	firstLine := strings.Repeat("a", maxQuoteChunkBytes-2)
	secondLine := "second line"
	chunks := splitQuoteChunks(firstLine + "\n" + secondLine)
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk.input) > maxQuoteChunkBytes {
			t.Errorf("chunk %d input has %d bytes, max %d", i, len(chunk.input), maxQuoteChunkBytes)
		}
	}
	if chunks[0].lines[0].source != firstLine || chunks[1].lines[0].source != secondLine {
		t.Fatalf("chunk boundaries split or changed source lines: %#v", chunks)
	}
}

func TestSplitQuoteChunksHandlesUnterminatedAndTruncatedUTF8Lines(t *testing.T) {
	unterminated := splitQuoteChunks("last line without newline")
	if len(unterminated) != 1 || unterminated[0].lines[0].source != "last line without newline" {
		t.Fatalf("unterminated final line was not preserved: %#v", unterminated)
	}

	longLine := strings.Repeat("x", maxQuoteChunkBytes-1) + "é" + "tail"
	chunks := splitQuoteChunks(longLine)
	if len(chunks) != 1 {
		t.Fatalf("chunk count = %d, want 1", len(chunks))
	}
	if !utf8.ValidString(chunks[0].lines[0].source) || !utf8.ValidString(chunks[0].input) {
		t.Fatal("truncated line is not valid UTF-8")
	}
	if len(chunks[0].input) > maxQuoteChunkBytes {
		t.Fatalf("chunk input has %d bytes, max %d", len(chunks[0].input), maxQuoteChunkBytes)
	}
}

func embedderReuseEntry(path string) fileEntry {
	return fileEntry{
		relPath:     "source.txt",
		absPath:     path,
		scoreText:   "relevant source line",
		scoreVector: []float32{1, 0},
	}
}
