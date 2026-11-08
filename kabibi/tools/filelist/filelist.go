// Package filelist implements the shared list_files search and result formatting.
package filelist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"ai"
	"github.com/mattn/go-runewidth"
)

const (
	DefaultMaxResults = 50
	maxWalkEntries    = 4000
	maxContentBytes   = 512 * 1024
	maxEmbedFileBytes = 256 * 1024
	maxConcurrent     = 4
)

type Options struct {
	Context       context.Context
	Query         string
	SearchContent bool
	MaxResults    int
	Embedder      ai.PartEmbedder
	MaxLineWidth  int
	QuoteLimit    int
	LiveQuotes    bool
	OnDiagnostic  func(string)
}

type fileEntry struct {
	relPath     string
	absPath     string
	isDir       bool
	isSymlink   bool
	isExec      bool
	size        int64
	linkTarget  string
	quote       string
	score       float64
	hasScore    bool
	scoreText   string
	scoreVector []float32
}

type Match struct {
	Path     string
	Size     int64
	IsDir    bool
	Score    float64
	HasScore bool
	Quote    string
}

var defaultSkippedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"target":       true,
	".hg":          true,
	".svn":         true,
	"__pycache__":  true,
	".next":        true,
}

type candidateResult struct {
	index  int
	vector []float32
	err    error
}

var errBinaryContentCandidate = errors.New("semantic content candidate is binary")

func ListFiles(cwd string, opts Options) (string, error) {
	iter := NewSearchIterator(opts.Context, cwd, opts)
	defer iter.Close()
	for range iter.Updates() {
	}
	return iter.Result()
}

func listFiles(ctx context.Context, cwd string, opts Options, progress *progressTracker) (string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve search directory %q: %w", cwd, err)
	}
	if info, err := os.Stat(absCwd); err != nil {
		return "", fmt.Errorf("stat search directory %q: %w", absCwd, err)
	} else if !info.IsDir() {
		return "", fmt.Errorf("search path %q is not a directory", absCwd)
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = DefaultMaxResults
	}
	if opts.MaxLineWidth <= 0 {
		opts.MaxLineWidth = 160
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		query = "*"
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	searchRoot, relPrefix := absCwd, ""
	if !opts.SearchContent && query != "*" && !hasGlobChars(query) {
		candidate := filepath.Join(absCwd, filepath.FromSlash(query))
		info, statErr := os.Stat(candidate)
		if statErr == nil && info.IsDir() {
			searchRoot = candidate
			relPrefix = strings.TrimSuffix(filepath.ToSlash(query), "/")
			query = "*"
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("stat query directory %q: %w", candidate, statErr)
		}
	}
	ignorePatterns, err := loadGitignorePatterns(absCwd)
	if err != nil {
		return "", err
	}
	entries, err := collectCandidateFiles(ctx, searchRoot, relPrefix, ignorePatterns, query == "*")
	if err != nil {
		return "", err
	}

	var scheduler *concurrentEmbedder
	var cache *embeddingCache
	cacheIdentity := ""
	if opts.Embedder != nil {
		scheduler = newConcurrentEmbedder(ctx, opts.Embedder)
		scheduler.diagnostic = progress.logVerbose
		defer scheduler.Close()
		if identified, ok := opts.Embedder.(ai.PartEmbedderCacheIdentity); ok {
			cacheIdentity, ok = embeddingCacheIdentity(identified)
			if ok {
				cache, err = newEmbeddingCache()
				if err != nil {
					return "", err
				}
			} else {
				progress.addWarning("Persistent semantic cache disabled: embedder has no stable cache identity.")
			}
		} else {
			progress.addWarning("Persistent semantic cache disabled: embedder has no stable cache identity.")
		}
	}
	quoteLimit := opts.MaxResults
	if opts.QuoteLimit > 0 {
		quoteLimit = min(opts.QuoteLimit, opts.MaxResults)
	} else if opts.QuoteLimit < 0 {
		quoteLimit = 0
	}
	var quoteWork *liveQuoteManager
	if opts.LiveQuotes && scheduler != nil && quoteLimit > 0 {
		quoteWork = newLiveQuoteManager(ctx, scheduler, progress, quoteLimit)
		progress.setQuoteManager(quoteWork)
		defer quoteWork.Close()
	}

	var matched []fileEntry
	modeLabel := ""
	if opts.SearchContent {
		matched, modeLabel, err = filterByContent(ctx, entries, query, opts.MaxResults, scheduler, progress, cache, cacheIdentity)
	} else {
		matched, modeLabel, err = filterByFilename(ctx, entries, query, opts.MaxResults, scheduler, progress, cache, cacheIdentity)
	}
	if err != nil {
		return "", err
	}
	matched = matched[:min(len(matched), opts.MaxResults)]

	if quoteWork != nil {
		if err := quoteWork.Finish(matched); err != nil {
			return "", err
		}
		matched = mergeProgressQuotes(matched, progress.matches())
	} else if err := enrichSemanticQuotes(ctx, matched, progress.queryEmbedding(), scheduler, progress, quoteLimit); err != nil {
		return "", err
	}

	displayCwd := compactHomePath(absCwd)
	var output strings.Builder
	fmt.Fprintln(&output, truncateOutputLine(fmt.Sprintf("[cwd: %s] (%d entries, mode: %s)", displayCwd, len(matched), modeLabel), opts.MaxLineWidth))
	for _, entry := range matched {
		fmt.Fprintln(&output, FormatMatch(toMatch(entry), opts.MaxLineWidth))
	}
	stats := progress.current()
	if stats.Viewed > 0 {
		fmt.Fprintf(&output, "\n%s", truncateOutputLine(fmt.Sprintf("Search stats: processed %d/%d files; about %s/file", stats.Viewed, stats.Total, FormatAveragePerFile(stats.AvgPerFile)), opts.MaxLineWidth))
	}
	for _, warning := range progress.warningMessages() {
		fmt.Fprintf(&output, "\nWarning: %s", truncateOutputLine(warning, opts.MaxLineWidth))
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}

func mergeProgressQuotes(entries, progressEntries []fileEntry) []fileEntry {
	quotes := make(map[string]string, len(progressEntries))
	for _, entry := range progressEntries {
		quotes[entry.relPath] = entry.quote
	}
	for index := range entries {
		if quote, ok := quotes[entries[index].relPath]; ok {
			entries[index].quote = quote
		}
	}
	return entries
}

func truncateOutputLine(line string, maxWidth int) string {
	if maxWidth > 0 && runewidth.StringWidth(line) > maxWidth {
		return runewidth.Truncate(line, maxWidth, "…")
	}
	return line
}

func toMatch(entry fileEntry) Match {
	name := entry.relPath
	if entry.isSymlink && entry.linkTarget != "" {
		name += " -> " + entry.linkTarget
	}
	return Match{Path: name, Size: entry.size, IsDir: entry.isDir, Score: entry.score, HasScore: entry.hasScore, Quote: entry.quote}
}

func FormatMatch(match Match, maxWidth int) string {
	var output strings.Builder
	if match.HasScore {
		fmt.Fprintf(&output, "%.2f  ", match.Score)
	}
	output.WriteString(cleanMatchField(match.Path))
	if match.IsDir {
		output.WriteByte('/')
	}
	if match.Size > 0 {
		fmt.Fprintf(&output, "  %s", formatFileSize(match.Size))
	}
	if quote := cleanMatchField(match.Quote); quote != "" {
		fmt.Fprintf(&output, "  %q", quote)
	}
	return truncateOutputLine(output.String(), maxWidth)
}

func formatFileSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, suffix := range units {
		value /= unit
		if value < unit || suffix == "TB" {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%d B", size)
}

func cleanMatchField(value string) string {
	var clean strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			clean.WriteByte(' ')
		} else {
			clean.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(clean.String()), " ")
}

func FormatAveragePerFile(duration time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(duration)/float64(time.Millisecond))
}

func collectCandidateFiles(ctx context.Context, root, relPrefix string, ignorePatterns []string, shallowOnly bool) ([]fileEntry, error) {
	var entries []fileEntry
	appendEntry := func(fullPath, relative string, dirEntry fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dirEntry.Name() == ".git" || isIgnored(relative, dirEntry.IsDir(), ignorePatterns) {
			return nil
		}
		info, err := os.Lstat(fullPath)
		if err != nil {
			return fmt.Errorf("stat candidate %q: %w", fullPath, err)
		}
		entry := fileEntry{relPath: relative, absPath: fullPath, isDir: info.IsDir(), size: info.Size()}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.isSymlink = true
			target, err := os.Readlink(fullPath)
			if err != nil {
				return fmt.Errorf("read symlink %q: %w", fullPath, err)
			}
			entry.linkTarget = filepath.ToSlash(target)
		}
		if !entry.isDir && !entry.isSymlink {
			entry.isExec, err = detectExecutable(fullPath, info.Mode())
			if err != nil {
				return fmt.Errorf("inspect executable candidate %q: %w", fullPath, err)
			}
		}
		entries = append(entries, entry)
		return nil
	}
	if shallowOnly {
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("read search directory %q: %w", root, err)
		}
		for _, dirEntry := range dirEntries {
			relative := dirEntry.Name()
			if relPrefix != "" {
				relative = relPrefix + "/" + relative
			}
			if defaultSkippedDirs[dirEntry.Name()] && dirEntry.IsDir() {
				continue
			}
			if err := appendEntry(filepath.Join(root, dirEntry.Name()), relative, dirEntry); err != nil {
				return nil, err
			}
		}
		sortEntries(entries)
		return entries, nil
	}

	err := filepath.WalkDir(root, func(fullPath string, dirEntry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("walk search path %q: %w", fullPath, walkErr)
		}
		if fullPath == root {
			return nil
		}
		relative, err := filepath.Rel(root, fullPath)
		if err != nil {
			return fmt.Errorf("make candidate path relative: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if relPrefix != "" {
			relative = relPrefix + "/" + relative
		}
		if dirEntry.IsDir() && defaultSkippedDirs[dirEntry.Name()] {
			return filepath.SkipDir
		}
		if isIgnored(relative, dirEntry.IsDir(), ignorePatterns) {
			if dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := appendEntry(fullPath, relative, dirEntry); err != nil {
			return err
		}
		if len(entries) >= maxWalkEntries {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	sortEntries(entries)
	return entries, nil
}

func detectExecutable(absPath string, mode fs.FileMode) (bool, error) {
	switch strings.ToLower(filepath.Ext(absPath)) {
	case ".exe", ".bat", ".cmd", ".com", ".sh":
		return true, nil
	}
	if runtime.GOOS != "windows" && mode&0o111 != 0 {
		return true, nil
	}
	file, err := os.Open(absPath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	var header [2]byte
	n, err := file.Read(header[:])
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return n == len(header) && header[0] == '#' && header[1] == '!', nil
}

func sortEntries(entries []fileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return entries[i].relPath < entries[j].relPath
	})
}

func filterByFilename(ctx context.Context, entries []fileEntry, query string, maxResults int, embedder *concurrentEmbedder, progress *progressTracker, cache *embeddingCache, identity string) ([]fileEntry, string, error) {
	if query == "*" {
		return entries, "glob '*'", nil
	}
	normalized := filepath.ToSlash(query)
	lowerQuery := strings.ToLower(normalized)
	var globMatches []fileEntry
	for _, entry := range entries {
		if MatchDoublestar(normalized, entry.relPath) || MatchDoublestar(normalized, path.Base(entry.relPath)) {
			globMatches = append(globMatches, entry)
			continue
		}
		if !hasGlobChars(normalized) && strings.Contains(strings.ToLower(entry.relPath), lowerQuery) {
			globMatches = append(globMatches, entry)
		}
	}
	if len(globMatches) > 0 && len(globMatches) <= maxResults*3 {
		return globMatches, fmt.Sprintf("glob %q", query), nil
	}
	var fuzzyMatches []fileEntry
	for _, entry := range entries {
		if score := fuzzyPathScore(lowerQuery, strings.ToLower(entry.relPath)); score >= 0.45 {
			entry.score = score
			fuzzyMatches = append(fuzzyMatches, entry)
		}
	}
	sort.Slice(fuzzyMatches, func(i, j int) bool {
		if fuzzyMatches[i].score != fuzzyMatches[j].score {
			return fuzzyMatches[i].score > fuzzyMatches[j].score
		}
		return fuzzyMatches[i].relPath < fuzzyMatches[j].relPath
	})
	if len(fuzzyMatches) > 0 && (embedder == nil || (fuzzyMatches[0].score >= 0.70 && len(fuzzyMatches) <= maxResults*3)) {
		return fuzzyMatches, fmt.Sprintf("fuzzy %q", query), nil
	}
	if embedder != nil {
		candidates := entries
		if len(globMatches) > maxResults*3 {
			candidates = globMatches
		}
		semantic, err := rankPathsByEmbedding(ctx, candidates, query, embedder, maxResults, progress, cache, identity)
		if err != nil {
			return nil, "", fmt.Errorf("semantic filename search: %w", err)
		}
		if len(semantic) > 0 {
			return semantic, fmt.Sprintf("semantic %q", query), nil
		}
	}
	if len(globMatches) > 0 {
		return globMatches, fmt.Sprintf("glob %q", query), nil
	}
	return fuzzyMatches, fmt.Sprintf("fuzzy %q", query), nil
}

func filterByContent(ctx context.Context, entries []fileEntry, query string, maxResults int, embedder *concurrentEmbedder, progress *progressTracker, cache *embeddingCache, identity string) ([]fileEntry, string, error) {
	lowerQuery := strings.ToLower(query)
	re, reErr := regexp.Compile("(?i)" + query)
	var matched []fileEntry
	var textCandidates []fileEntry
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if entry.isDir || entry.isSymlink || entry.size > maxContentBytes {
			continue
		}
		data, err := os.ReadFile(entry.absPath)
		if err != nil {
			return nil, "", fmt.Errorf("read content candidate %q: %w", entry.absPath, err)
		}
		if isBinaryBlob(data) {
			continue
		}
		textCandidates = append(textCandidates, entry)
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			hit := strings.Contains(strings.ToLower(line), lowerQuery)
			if !hit && reErr == nil {
				hit = re.MatchString(line)
			}
			if hit {
				entry.quote = strings.TrimSpace(line)
				matched = append(matched, entry)
				break
			}
		}
	}
	if len(matched) > 0 && (len(matched) <= maxResults || embedder == nil) {
		return matched, fmt.Sprintf("content %q", query), nil
	}
	if embedder == nil {
		return matched, fmt.Sprintf("content %q", query), nil
	}
	candidates := textCandidates
	if len(matched) > maxResults {
		candidates = matched
	}
	eligible := make([]fileEntry, 0, len(candidates))
	for _, entry := range candidates {
		if !entry.isDir && !entry.isSymlink && entry.size <= maxEmbedFileBytes {
			eligible = append(eligible, entry)
		}
	}
	if len(eligible) == 0 {
		return matched, fmt.Sprintf("content %q", query), nil
	}
	semantic, err := rankContentByEmbedding(ctx, eligible, query, embedder, maxResults, progress, cache, identity)
	if err != nil {
		return nil, "", fmt.Errorf("semantic content search: %w", err)
	}
	if len(semantic) > 0 {
		return semantic, fmt.Sprintf("semantic-content %q", query), nil
	}
	return matched, fmt.Sprintf("content %q", query), nil
}

func isBinaryBlob(data []byte) bool {
	check := data[:min(len(data), 512)]
	return bytes.IndexByte(check, 0) >= 0
}

func rankPathsByEmbedding(ctx context.Context, entries []fileEntry, query string, embedder *concurrentEmbedder, maxResults int, progress *progressTracker, cache *embeddingCache, identity string) ([]fileEntry, error) {
	parts := make([]ai.PromptPart, len(entries))
	for index, entry := range entries {
		parts[index] = ai.PromptPart{Role: ai.RoleModel, Text: filepath.Base(entry.relPath)}
	}
	return rankEmbeddingCandidates(ctx, entries, parts, query, "filename", embeddingRecipeName, embedder, maxResults, progress, cache, identity)
}

func rankContentByEmbedding(ctx context.Context, entries []fileEntry, query string, embedder *concurrentEmbedder, maxResults int, progress *progressTracker, cache *embeddingCache, identity string) ([]fileEntry, error) {
	parts := make([]ai.PromptPart, len(entries))
	return rankEmbeddingCandidates(ctx, entries, parts, query, "content", embeddingRecipeFile, embedder, maxResults, progress, cache, identity)
}

func contentEmbeddingPart(entry fileEntry) (ai.PromptPart, error) {
	data, err := os.ReadFile(entry.absPath)
	if err != nil {
		return ai.PromptPart{}, fmt.Errorf("read semantic content candidate %q: %w", entry.absPath, err)
	}
	if isBinaryBlob(data) {
		return ai.PromptPart{}, fmt.Errorf("%w: %q", errBinaryContentCandidate, entry.absPath)
	}
	sample := data[:min(len(data), 1500)]
	return ai.PromptPart{Role: ai.RoleModel, Text: filepath.Base(entry.relPath) + "\n" + string(sample)}, nil
}

func rankEmbeddingCandidates(ctx context.Context, entries []fileEntry, parts []ai.PromptPart, query, kind, recipe string, embedder *concurrentEmbedder, maxResults int, progress *progressTracker, cache *embeddingCache, identity string) ([]fileEntry, error) {
	if len(parts) != len(entries) {
		return nil, fmt.Errorf("embed candidate input count %d does not match file count %d", len(parts), len(entries))
	}
	if err := progress.update("query", "", 0, len(entries)); err != nil {
		return nil, err
	}
	queryStarted := time.Now()
	progress.logVerbose("embedding start phase=query items=1")
	queryVector, err := embedder.Embed(ctx, ai.PromptPart{Role: ai.RoleUser, Text: query}, false)
	if err != nil {
		progress.logVerbose("embedding failed phase=query duration=%s error=%q", time.Since(queryStarted), err)
		return nil, err
	}
	progress.logVerbose("embedding finish phase=query duration=%s dimensions=%d",
		time.Since(queryStarted), len(queryVector))
	if len(queryVector) == 0 {
		return nil, fmt.Errorf("query embedder returned an empty vector")
	}
	progress.setQueryEmbedding(queryVector)
	viewed := 0
	publish := func(phase, current string) error {
		return progress.update(phase, current, viewed, len(entries))
	}
	results := make(chan candidateResult, maxConcurrent)
	nextIndex, inFlight := 0, 0
	start := func(index int) error {
		if err := publish("candidate", entries[index].relPath); err != nil {
			return err
		}
		inFlight++
		go func() {
			vector, err := cachedCandidateEmbedding(ctx, entries[index], parts[index], kind, recipe, embedder, cache, identity, len(queryVector), progress)
			results <- candidateResult{index: index, vector: vector, err: err}
		}()
		return nil
	}
	for nextIndex < len(entries) && inFlight < maxConcurrent {
		if err := start(nextIndex); err != nil {
			return nil, err
		}
		nextIndex++
	}
	for inFlight > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := <-results
		inFlight--
		if errors.Is(result.err, errBinaryContentCandidate) {
			viewed++
			if err := publish("candidate", entries[result.index].relPath); err != nil {
				return nil, err
			}
			if nextIndex < len(entries) {
				if err := start(nextIndex); err != nil {
					return nil, err
				}
				nextIndex++
			}
			continue
		}
		if result.err != nil {
			return nil, result.err
		}
		if len(result.vector) == 0 || len(result.vector) != len(queryVector) {
			return nil, fmt.Errorf("candidate %q embedding has %d dimensions; query embedding has %d", entries[result.index].relPath, len(result.vector), len(queryVector))
		}
		entry := entries[result.index]
		entry.score = cosineSim(queryVector, result.vector)
		if math.IsNaN(entry.score) || math.IsInf(entry.score, 0) {
			return nil, fmt.Errorf("candidate %q has non-finite similarity", entry.relPath)
		}
		if entry.score >= 0.50 {
			entry.hasScore = true
			entry.scoreText = parts[result.index].Text
			entry.scoreVector = append([]float32(nil), result.vector...)
			progress.addMatch(entry, maxResults)
		}
		viewed++
		if err := publish("candidate", entry.relPath); err != nil {
			return nil, err
		}
		if nextIndex < len(entries) {
			if err := start(nextIndex); err != nil {
				return nil, err
			}
			nextIndex++
		}
	}
	return progress.matches(), nil
}

func cachedCandidateEmbedding(ctx context.Context, entry fileEntry, part ai.PromptPart, kind, recipe string, embedder *concurrentEmbedder, cache *embeddingCache, identity string, dimension int, progress *progressTracker) ([]float32, error) {
	var signature []byte
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		info, statErr := os.Lstat(entry.absPath)
		if statErr != nil {
			return nil, fmt.Errorf("stat semantic candidate %q: %w", entry.absPath, statErr)
		}
		signature = entrySignature(entry.absPath, info)
		if cache != nil {
			vector, ok, cacheErr := cache.get(signature, identity, kind, recipe, dimension)
			if cacheErr != nil {
				progress.logVerbose("embedding cache error kind=%s path=%q error=%q", kind, entry.relPath, cacheErr)
				return nil, fmt.Errorf("read cached embedding for %q: %w", entry.relPath, cacheErr)
			}
			if ok {
				progress.logVerbose("embedding cache hit kind=%s path=%q", kind, entry.relPath)
				return vector, nil
			}
			progress.logVerbose("embedding cache miss kind=%s path=%q", kind, entry.relPath)
		} else {
			progress.logVerbose("embedding cache disabled kind=%s path=%q", kind, entry.relPath)
		}
		if kind != "content" || part.Text != "" {
			break
		}
		part, err = contentEmbeddingPart(entry)
		if err != nil {
			return nil, err
		}
		afterRead, statErr := os.Lstat(entry.absPath)
		if statErr != nil {
			return nil, fmt.Errorf("restat semantic candidate %q: %w", entry.absPath, statErr)
		}
		afterSignature := entrySignature(entry.absPath, afterRead)
		if equalBytes(signature, afterSignature) {
			break
		}
		if attempt == 1 {
			return nil, fmt.Errorf("semantic candidate %q changed repeatedly while being read", entry.relPath)
		}
		part.Text = ""
	}
	embedStarted := time.Now()
	progress.logVerbose("embedding start phase=candidate kind=%s path=%q input_bytes=%d",
		kind, entry.relPath, len(part.Text))
	vector, err := embedder.Embed(ctx, part, false)
	if err != nil {
		progress.logVerbose("embedding failed phase=candidate kind=%s path=%q duration=%s error=%q",
			kind, entry.relPath, time.Since(embedStarted), err)
		return nil, err
	}
	progress.logVerbose("embedding finish phase=candidate kind=%s path=%q duration=%s dimensions=%d",
		kind, entry.relPath, time.Since(embedStarted), len(vector))
	if len(vector) != dimension {
		return nil, fmt.Errorf("candidate %q embedding has %d dimensions; query embedding has %d", entry.relPath, len(vector), dimension)
	}
	if cache != nil {
		latest, statErr := os.Lstat(entry.absPath)
		if statErr != nil {
			return nil, fmt.Errorf("restat semantic candidate %q: %w", entry.absPath, statErr)
		}
		if !equalBytes(signature, entrySignature(entry.absPath, latest)) {
			return vector, nil
		}
		if err := cache.put(signature, identity, kind, recipe, vector); err != nil {
			progress.addWarning(fmt.Sprintf("Could not persist semantic embedding for %q: %v", entry.relPath, err))
		}
	}
	return vector, nil
}

func MatchDoublestar(pattern, name string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	patternParts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")
	var match func(int, int) bool
	match = func(pi, ni int) bool {
		if pi == len(patternParts) {
			return ni == len(nameParts)
		}
		if patternParts[pi] == "**" {
			for next := ni; next <= len(nameParts); next++ {
				if match(pi+1, next) {
					return true
				}
			}
			return false
		}
		if ni >= len(nameParts) {
			return false
		}
		ok, err := path.Match(patternParts[pi], nameParts[ni])
		return err == nil && ok && match(pi+1, ni+1)
	}
	return match(0, 0)
}

func hasGlobChars(value string) bool {
	return strings.ContainsAny(value, "*?[")
}

func loadGitignorePatterns(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .gitignore in %q: %w", root, err)
	}
	var patterns []string
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, nil
}

func isIgnored(name string, isDir bool, patterns []string) bool {
	ignored := false
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" || strings.HasPrefix(pattern, "#") {
			continue
		}
		negate := strings.HasPrefix(pattern, "!")
		if negate {
			pattern = strings.TrimPrefix(pattern, "!")
		}
		directoryOnly := strings.HasSuffix(pattern, "/")
		pattern = strings.Trim(pattern, "/")
		if directoryOnly && !isDir {
			continue
		}
		matched := MatchDoublestar(pattern, name)
		if !strings.Contains(pattern, "/") {
			for _, component := range strings.Split(name, "/") {
				matched = matched || MatchDoublestar(pattern, component)
			}
		}
		if matched {
			ignored = !negate
		}
	}
	return ignored
}

func fuzzyPathScore(query, candidate string) float64 {
	if query == "" {
		return 1
	}
	queryRunes, candidateRunes := []rune(query), []rune(candidate)
	if len(queryRunes) == 0 || len(candidateRunes) == 0 {
		return 0
	}
	q, c := 0, 0
	consecutive, bestConsecutive := 0, 0
	for q < len(queryRunes) && c < len(candidateRunes) {
		if queryRunes[q] == candidateRunes[c] {
			q++
			consecutive++
			bestConsecutive = max(bestConsecutive, consecutive)
		} else {
			consecutive = 0
		}
		c++
	}
	if q != len(queryRunes) {
		return 0
	}
	coverage := float64(len(queryRunes)) / float64(len(candidateRunes))
	contiguity := float64(bestConsecutive) / float64(len(queryRunes))
	return min(1.0, coverage*0.55+contiguity*0.45)
}

func insertTopMatch(top []fileEntry, candidate fileEntry, limit int) []fileEntry {
	index := sort.Search(len(top), func(i int) bool {
		if top[i].score != candidate.score {
			return top[i].score < candidate.score
		}
		return top[i].relPath > candidate.relPath
	})
	top = append(top, fileEntry{})
	copy(top[index+1:], top[index:])
	top[index] = candidate
	if limit > 0 && len(top) > limit {
		top = top[:limit]
	}
	return top
}

func cosineSim(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		left, right := float64(a[i]), float64(b[i])
		dot += left * right
		normA += left * left
		normB += right * right
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func compactHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if relative, relErr := filepath.Rel(home, path); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return filepath.Join("~", relative)
		}
	}
	return path
}
