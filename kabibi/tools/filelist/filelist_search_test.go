package filelist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai"
)

type searchTestEmbedder struct {
	calls atomic.Int32
	delay time.Duration
	err   error
	mu    sync.Mutex
	texts []string
}

type dimensionSearchEmbedder struct {
	candidate []float32
}

type binaryMutationSearchEmbedder struct {
	path  string
	mu    sync.Mutex
	texts []string
}

func (e *binaryMutationSearchEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	if len(parts) > 0 && parts[0].Role == ai.RoleUser {
		if err := os.WriteFile(e.path, []byte{0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0}, 0o600); err != nil {
			return nil, err
		}
	} else if len(parts) > 0 {
		e.mu.Lock()
		e.texts = append(e.texts, parts[0].Text)
		e.mu.Unlock()
	}
	return []float32{1, 0}, nil
}

func (e dimensionSearchEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	if len(parts) > 0 && parts[0].Role == ai.RoleUser {
		return []float32{1, 0}, nil
	}
	return e.candidate, nil
}

func (e *searchTestEmbedder) Embed(parts []ai.PromptPart) ([]float32, error) {
	e.calls.Add(1)
	if len(parts) > 0 {
		e.mu.Lock()
		e.texts = append(e.texts, parts[0].Text)
		e.mu.Unlock()
	}
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	if e.err != nil {
		return nil, e.err
	}
	if len(parts) > 0 && parts[0].Role == ai.RoleUser {
		return []float32{1, 0}, nil
	}
	return []float32{1, 0}, nil
}

func TestSemanticFilenameEmbedsBasenameNotDirectoryPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "needle.go")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &searchTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	progress := testProgressTracker(t)
	_, err = rankPathsByEmbedding(context.Background(), []fileEntry{{relPath: "nested/needle.go", absPath: path, size: info.Size()}}, "semantic query", scheduler, 5, progress, nil, "")
	closeTestProgress(progress)
	if err != nil {
		t.Fatalf("rankPathsByEmbedding(): %v", err)
	}
	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	if len(embedder.texts) != 2 || embedder.texts[1] != "needle.go" {
		t.Fatalf("embedded texts = %q, want query and basename only", embedder.texts)
	}
}

func TestSemanticFilenameCacheAvoidsRepeatedCandidateEmbedding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "needle.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := fileEntry{relPath: "needle.go", absPath: path, size: info.Size()}
	cache := &embeddingCache{dir: t.TempDir()}
	embedder := &searchTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()

	firstProgress := testProgressTracker(t)
	first, err := rankPathsByEmbedding(context.Background(), []fileEntry{entry}, "concept query", scheduler, 5, firstProgress, cache, "provider:model")
	if err != nil {
		t.Fatalf("first rankPathsByEmbedding(): %v", err)
	}
	closeTestProgress(firstProgress)
	if len(first) != 1 {
		t.Fatalf("first result count = %d, want 1", len(first))
	}

	secondProgress := testProgressTracker(t)
	second, err := rankPathsByEmbedding(context.Background(), []fileEntry{entry}, "concept query", scheduler, 5, secondProgress, cache, "provider:model")
	if err != nil {
		t.Fatalf("second rankPathsByEmbedding(): %v", err)
	}
	closeTestProgress(secondProgress)
	if len(second) != 1 {
		t.Fatalf("second result count = %d, want 1", len(second))
	}
	if calls := embedder.calls.Load(); calls != 3 {
		t.Fatalf("embed calls = %d, want query twice and candidate once", calls)
	}
	if err := os.WriteFile(path, []byte("package sample with changed size\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry.size = updated.Size()
	thirdProgress := testProgressTracker(t)
	_, err = rankPathsByEmbedding(context.Background(), []fileEntry{entry}, "concept query", scheduler, 5, thirdProgress, cache, "provider:model")
	closeTestProgress(thirdProgress)
	if err != nil {
		t.Fatalf("rankPathsByEmbedding() after metadata change: %v", err)
	}
	if calls := embedder.calls.Load(); calls != 5 {
		t.Fatalf("embed calls after metadata change = %d, want a fresh query and candidate embedding", calls)
	}
}

func TestSemanticContentSearchUsesCachedVectorBeforeEmbeddingInputRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.txt")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := fileEntry{relPath: "content.txt", absPath: path, size: info.Size()}
	cache := &embeddingCache{dir: t.TempDir()}
	signature := entrySignature(path, info)
	if err := cache.put(signature, "provider:model", "content", embeddingRecipeFile, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	embedder := &searchTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	progress := testProgressTracker(t)
	matches, err := rankContentByEmbedding(context.Background(), []fileEntry{entry}, "concept query", scheduler, 5, progress, cache, "provider:model")
	closeTestProgress(progress)
	if err != nil {
		t.Fatalf("rankContentByEmbedding(): %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("result count = %d, want 1", len(matches))
	}
	if calls := embedder.calls.Load(); calls != 1 {
		t.Fatalf("embed calls = %d, want query only on cached candidate hit", calls)
	}
}

func TestSemanticContentInputUsesBasenameAndFirst1500Bytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	content := strings.Repeat("x", 1600)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	part, err := contentEmbeddingPart(fileEntry{relPath: "nested/sample.txt", absPath: path})
	if err != nil {
		t.Fatalf("contentEmbeddingPart(): %v", err)
	}
	want := "sample.txt\n" + content[:1500]
	if part.Text != want {
		t.Fatalf("content sample length = %d, want %d", len(part.Text), len(want))
	}
}

func TestFilterByContentSkipsBinarySemanticCandidates(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "audio_sample.wav")
	if err := os.WriteFile(binaryPath, []byte{0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0}, 0o600); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textPath, []byte("unrelated readable text"), 0o600); err != nil {
		t.Fatal(err)
	}
	binaryInfo, err := os.Lstat(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	textInfo, err := os.Lstat(textPath)
	if err != nil {
		t.Fatal(err)
	}

	embedder := &searchTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	progress := testProgressTracker(t)
	matches, mode, err := filterByContent(context.Background(), []fileEntry{
		{relPath: "audio_sample.wav", absPath: binaryPath, size: binaryInfo.Size()},
		{relPath: "notes.txt", absPath: textPath, size: textInfo.Size()},
	}, "Budapest", 5, scheduler, progress, nil, "", false)
	closeTestProgress(progress)
	if err != nil {
		t.Fatalf("filterByContent(): %v", err)
	}
	if mode != `semantic-content "Budapest"` {
		t.Fatalf("search mode = %q, want semantic content", mode)
	}
	if len(matches) != 1 || matches[0].relPath != "notes.txt" {
		t.Fatalf("matches = %#v, want only notes.txt", matches)
	}
	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	for _, text := range embedder.texts {
		if strings.Contains(text, "audio_sample.wav") {
			t.Fatalf("binary candidate was embedded as text: %q", text)
		}
	}
}

func TestSemanticContentRankingSkipsCandidateThatBecomesBinary(t *testing.T) {
	dir := t.TempDir()
	changingPath := filepath.Join(dir, "changing.txt")
	if err := os.WriteFile(changingPath, []byte("text when the search starts"), 0o600); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textPath, []byte("unrelated readable text"), 0o600); err != nil {
		t.Fatal(err)
	}
	changingInfo, err := os.Lstat(changingPath)
	if err != nil {
		t.Fatal(err)
	}
	textInfo, err := os.Lstat(textPath)
	if err != nil {
		t.Fatal(err)
	}

	embedder := &binaryMutationSearchEmbedder{path: changingPath}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	progress := testProgressTracker(t)
	matches, err := rankContentByEmbedding(context.Background(), []fileEntry{
		{relPath: "changing.txt", absPath: changingPath, size: changingInfo.Size()},
		{relPath: "notes.txt", absPath: textPath, size: textInfo.Size()},
	}, "Budapest", scheduler, 5, progress, nil, "")
	closeTestProgress(progress)
	if err != nil {
		t.Fatalf("rankContentByEmbedding(): %v", err)
	}
	if len(matches) != 1 || matches[0].relPath != "notes.txt" {
		t.Fatalf("matches = %#v, want only notes.txt", matches)
	}
	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	for _, text := range embedder.texts {
		if strings.Contains(text, "changing.txt") {
			t.Fatalf("candidate that became binary was embedded as text: %q", text)
		}
	}
}

func TestSemanticSearchPublishesIncrementalProgressAndHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	for index := range 8 {
		path := filepath.Join(dir, strings.Repeat("x", index+1)+".go")
		if err := os.WriteFile(path, []byte("package sample"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	iter := NewSearchIterator(ctx, dir, Options{Query: "semantic concept", MaxResults: 3, Embedder: &searchTestEmbedder{delay: 20 * time.Millisecond}})
	seenCandidate := false
	deadline := time.After(time.Second)
	for !seenCandidate {
		select {
		case <-iter.Updates():
			progress := iter.Current()
			seenCandidate = progress.Phase == "candidate" && progress.Viewed == 0
		case <-deadline:
			cancel()
			iter.Close()
			t.Fatal("search did not publish an unscored candidate state")
		}
	}
	progress := iter.Current()
	if progress.Viewed >= 8 {
		cancel()
		iter.Close()
		t.Fatalf("candidate update reported %d viewed files before scoring", progress.Viewed)
	}
	cancel()
	_, err := iter.Result()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Result() error = %v, want context cancellation", err)
	}
	iter.Close()
}

func TestListFilesPreservesLexicalAndContentSearch(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "needle.go"), []byte("findMeInContent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	filename, err := ListFiles(dir, Options{Query: "**/needle.go"})
	if err != nil {
		t.Fatalf("filename ListFiles(): %v", err)
	}
	if !strings.Contains(filename, "nested/needle.go") || strings.Contains(filename, "ignored.txt") {
		t.Fatalf("filename result = %q", filename)
	}
	content, err := ListFiles(dir, Options{Query: "findMeInContent", SearchContent: true})
	if err != nil {
		t.Fatalf("content ListFiles(): %v", err)
	}
	if !strings.Contains(content, "nested/needle.go") || !strings.Contains(content, "findMeInContent") || !strings.Contains(content, "line 1") {
		t.Fatalf("content result = %q", content)
	}
}

func TestFindPathsIsStructuralAndDoesNotEmbed(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"src/handler.go": "package src",
		"docs/guide.md":  "guide",
		"README.md":      "readme",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name         string
		pattern      string
		wantPath     string
		unwantedPath string
	}{
		{name: "glob", pattern: "**/*.go", wantPath: "src/handler.go", unwantedPath: "docs/guide.md"},
		{name: "exact path", pattern: "src/handler.go", wantPath: "src/handler.go", unwantedPath: "docs/guide.md"},
		{name: "directory prefix", pattern: "docs", wantPath: "docs/guide.md", unwantedPath: "src/handler.go"},
		{name: "exact filename", pattern: "README.md", wantPath: "README.md", unwantedPath: "src/handler.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			embedder := &searchTestEmbedder{}
			result, err := ListFiles(dir, Options{
				FindPaths: test.pattern,
				Embedder:  embedder,
			})
			if err != nil {
				t.Fatalf("ListFiles(): %v", err)
			}
			if !strings.Contains(result, test.wantPath) || strings.Contains(result, test.unwantedPath) {
				t.Fatalf("structural path result = %q", result)
			}
			if !strings.Contains(result, `mode: paths "`+test.pattern+`"`) {
				t.Fatalf("structural path mode is missing for %q: %s", test.pattern, result)
			}
			if got := embedder.calls.Load(); got != 0 {
				t.Fatalf("path search made %d embedding calls, want none", got)
			}
		})
	}
}

func TestFindPathsRestrictsSemanticContentSearch(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"src/selected.go": "selected browser runtime implementation",
		"docs/outside.md": "outside-only semantic marker",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	embedder := &searchTestEmbedder{}
	result, err := ListFiles(dir, Options{
		FindPaths:   "src/**",
		FindContent: "browser runtime startup",
		MaxResults:  5,
		Embedder:    embedder,
	})
	if err != nil {
		t.Fatalf("ListFiles(): %v", err)
	}
	if !strings.Contains(result, "src/selected.go") || strings.Contains(result, "docs/outside.md") {
		t.Fatalf("combined path/content result = %q", result)
	}
	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	for _, text := range embedder.texts {
		if strings.Contains(text, "outside-only semantic marker") {
			t.Fatalf("content outside find_paths was embedded: %q", text)
		}
	}
}

func TestFindContentReusesCachedCandidateEmbedding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.txt")
	if err := os.WriteFile(path, []byte("a unique cached semantic candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &cacheTestEmbedder{}
	cache, err := newEmbeddingCache()
	if err != nil {
		t.Fatalf("newEmbeddingCache(): %v", err)
	}
	recordPath := cache.recordPath(
		entrySignature(path, info),
		embedder.CacheIdentity(),
		"content",
		embeddingRecipeFile,
		2,
	)
	defer os.Remove(recordPath)
	if err := cache.put(entrySignature(path, info), embedder.CacheIdentity(), "content", embeddingRecipeFile, []float32{1, 0}); err != nil {
		t.Fatalf("prewarm candidate embedding cache: %v", err)
	}

	result, err := ListFiles(dir, Options{
		FindPaths:   "**",
		FindContent: "semantic cache reuse query",
		QuoteLimit:  -1,
		Embedder:    embedder,
	})
	if err != nil {
		t.Fatalf("ListFiles(): %v", err)
	}
	if !strings.Contains(result, `mode: semantic-content "semantic cache reuse query"`) ||
		!strings.Contains(result, "cached.txt") {
		t.Fatalf("cached semantic result = %q", result)
	}
	if got := embedder.calls.Load(); got != 1 {
		t.Fatalf("embedding calls = %d, want query only after cached candidate reuse", got)
	}
}

func TestFindContentUsesSemanticSearchAlongsideLiteralMatches(t *testing.T) {
	dir := t.TempDir()
	exactPath := filepath.Join(dir, "exact.txt")
	relatedPath := filepath.Join(dir, "related.txt")
	if err := os.WriteFile(exactPath, []byte("needle in exact file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relatedPath, []byte("related concept in another file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	embedder := &searchTestEmbedder{}
	result, err := ListFiles(dir, Options{
		FindPaths:   "**",
		FindContent: "needle",
		MaxResults:  5,
		Embedder:    embedder,
	})
	if err != nil {
		t.Fatalf("ListFiles(): %v", err)
	}
	if !strings.Contains(result, `mode: semantic-content "needle"`) ||
		!strings.Contains(result, "exact.txt") || !strings.Contains(result, "related.txt") {
		t.Fatalf("semantic content result = %q", result)
	}
	if calls := embedder.calls.Load(); calls < 4 {
		t.Fatalf("embedding calls = %d, want query, both candidates, and quote work", calls)
	}
}

func TestFindContentCanDisableSemanticEmbeddings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "match.txt")
	if err := os.WriteFile(path, []byte("literal needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	embedder := &searchTestEmbedder{err: errors.New("embedder must not be called")}
	result, err := ListFiles(dir, Options{
		FindPaths:   "**",
		FindContent: "needle",
		NoSemantic:  true,
		Embedder:    embedder,
	})
	if err != nil {
		t.Fatalf("ListFiles(): %v", err)
	}
	if !strings.Contains(result, `mode: literal-content "needle"`) || !strings.Contains(result, "match.txt") {
		t.Fatalf("literal content result = %q", result)
	}
	if calls := embedder.calls.Load(); calls != 0 {
		t.Fatalf("no-semantic search made %d embedding calls, want none", calls)
	}
}

func TestFindContentRequiresEmbedderUnlessDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ListFiles(dir, Options{FindPaths: "**", FindContent: "concept"})
	if err == nil || !strings.Contains(err.Error(), "semantic content search requires an embedder") {
		t.Fatalf("ListFiles() error = %v, want missing-embedder error", err)
	}
}

func TestListFilesSurfacesEmbeddingFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("nothing related"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("embed failed")
	_, err := ListFiles(dir, Options{Query: "unmatched semantic query", Embedder: &searchTestEmbedder{err: wantErr}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ListFiles() error = %v, want %v", err, wantErr)
	}
}

func TestListFilesEmitsEmbeddingDiagnosticsWithoutPayloadText(t *testing.T) {
	dir := t.TempDir()
	const privateText = "do not print this document payload"
	if err := os.WriteFile(filepath.Join(dir, "candidate.txt"), []byte(privateText+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var diagnosticsMu sync.Mutex
	var diagnostics []string
	_, err := ListFiles(dir, Options{
		Query:         "unmatched semantic query",
		SearchContent: true,
		MaxResults:    1,
		Embedder:      &searchTestEmbedder{},
		OnDiagnostic: func(message string) {
			diagnosticsMu.Lock()
			diagnostics = append(diagnostics, message)
			diagnosticsMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("ListFiles(): %v", err)
	}

	diagnosticsMu.Lock()
	joined := strings.Join(diagnostics, "\n")
	diagnosticsMu.Unlock()
	for _, want := range []string{
		"embedding start phase=query",
		"embedding finish phase=query",
		"embedding cache disabled kind=content",
		"embedding start phase=candidate",
		"embedding finish phase=candidate",
		"embedding batch start",
		"embedding batch finish",
		"embedding start phase=quote",
		"quote result path=",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, privateText) {
		t.Fatalf("diagnostics exposed candidate content: %q", joined)
	}
}

func TestListFilesSurfacesEmptyAndMismatchedEmbeddings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		candidate []float32
	}{
		{name: "empty", candidate: nil},
		{name: "wrong dimension", candidate: []float32{1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ListFiles(dir, Options{Query: "semantic concept not in path", Embedder: dimensionSearchEmbedder{candidate: test.candidate}})
			if err == nil {
				t.Fatal("ListFiles() unexpectedly succeeded")
			}
		})
	}
}

func TestSemanticSearchReportsCacheReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("package sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := fileEntry{relPath: "sample.go", absPath: path, size: info.Size()}
	cache := &embeddingCache{dir: t.TempDir()}
	recordPath := cache.recordPath(entrySignature(path, info), "provider:model", "filename", embeddingRecipeName, 2)
	if err := os.Mkdir(recordPath, 0o700); err != nil {
		t.Fatal(err)
	}
	embedder := &searchTestEmbedder{}
	scheduler := newConcurrentEmbedder(context.Background(), embedder)
	defer scheduler.Close()
	progress := testProgressTracker(t)
	_, err = rankPathsByEmbedding(context.Background(), []fileEntry{entry}, "semantic query", scheduler, 5, progress, cache, "provider:model")
	closeTestProgress(progress)
	if err == nil || !strings.Contains(err.Error(), "read cached embedding") {
		t.Fatalf("rankPathsByEmbedding() error = %v, want explicit cache read error", err)
	}
}

func TestListFilesHandlesCachelessEmbedder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	embedder := &searchTestEmbedder{}
	result, err := ListFiles(dir, Options{Query: "concept not in path", Embedder: embedder})
	if err != nil {
		t.Fatalf("ListFiles(): %v", err)
	}
	if !strings.Contains(result, "Persistent semantic cache disabled") {
		t.Fatalf("result missing cache identity warning: %q", result)
	}
}

func testProgressTracker(t *testing.T) *progressTracker {
	t.Helper()
	ctx := context.Background()
	iter := &SearchIterator{ctx: ctx, updates: make(chan struct{}), done: make(chan struct{})}
	progress := &progressTracker{iter: iter}
	go func() {
		defer close(iter.done)
		for range iter.updates {
		}
	}()
	return progress
}

func closeTestProgress(progress *progressTracker) {
	close(progress.iter.updates)
	<-progress.iter.done
}
