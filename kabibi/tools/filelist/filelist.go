// Package filelist implements the progressive list_files tool with `**` doublestar
// globbing, .gitignore filtering, fuzzy/semantic fallbacks, and compact single-line
// human-readable formatting (`d`, `x`, `l`, `-`).
package filelist

import (
	"bytes"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"ai"
)

const DefaultMaxResults = 50

// Options configures a ListFiles query.
type Options struct {
	Query         string
	SearchContent bool
	MaxResults    int
	Embedder      ai.PartEmbedder
}

type fileEntry struct {
	relPath    string
	absPath    string
	isDir      bool
	isSymlink  bool
	isExec     bool
	size       int64
	linkTarget string
	matchNote  string
	score      float64
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

// ListFiles walks cwd, applies the progressive escalation ladder (glob/exact ->
// fuzzy -> semantic embeddings, or content search when opts.SearchContent is true),
// and formats a compact human-readable table.
func ListFiles(cwd string, opts Options) (string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			cwd = "."
		}
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		absCwd = filepath.Clean(cwd)
	}

	maxRes := opts.MaxResults
	if maxRes <= 0 {
		maxRes = DefaultMaxResults
	}

	query := strings.TrimSpace(opts.Query)
	if query == "" {
		query = "*"
	}

	// If query points to a specific subdirectory prefix without glob chars (and not search_content),
	// check if it's an existing directory so `list_files(query="ui/shell")` lists its contents.
	searchRoot := absCwd
	relPrefix := ""
	if !opts.SearchContent && query != "*" && !hasGlobChars(query) {
		cand := filepath.Join(absCwd, filepath.FromSlash(query))
		if info, err := os.Stat(cand); err == nil && info.IsDir() {
			searchRoot = cand
			relPrefix = strings.TrimSuffix(filepath.ToSlash(query), "/")
			query = "*"
		}
	}

	ignorePatterns := loadGitignorePatterns(absCwd)
	allEntries, err := collectCandidateFiles(searchRoot, relPrefix, ignorePatterns, query == "*")
	if err != nil {
		return "", err
	}

	var matched []fileEntry
	modeLabel := ""

	if opts.SearchContent {
		matched, modeLabel = filterByContent(allEntries, query, maxRes, opts.Embedder)
	} else {
		matched, modeLabel = filterByFilename(allEntries, query, maxRes, opts.Embedder)
	}

	if len(matched) > maxRes {
		matched = matched[:maxRes]
	}

	displayCwd := compactHomePath(absCwd)
	var sb strings.Builder
	fmt.Fprintf(&sb, "[cwd: %s] (%d entries, mode: %s)\n", displayCwd, len(matched), modeLabel)

	for _, e := range matched {
		typeChar := "-"
		sizeStr := formatHumanSize(e.size)
		nameDisplay := e.relPath

		if e.isDir {
			typeChar = "d"
			sizeStr = "-"
			if !strings.HasSuffix(nameDisplay, "/") {
				nameDisplay += "/"
			}
		} else if e.isSymlink {
			typeChar = "l"
			if e.linkTarget != "" {
				nameDisplay += "@ -> " + e.linkTarget
			} else {
				nameDisplay += "@"
			}
		} else if e.isExec {
			typeChar = "x"
			nameDisplay += "*"
		}

		if e.matchNote != "" {
			fmt.Fprintf(&sb, "%s  %6s  %s  %s\n", typeChar, sizeStr, nameDisplay, e.matchNote)
		} else {
			fmt.Fprintf(&sb, "%s  %6s  %s\n", typeChar, sizeStr, nameDisplay)
		}
	}

	return strings.TrimSuffix(sb.String(), "\n"), nil
}

func collectCandidateFiles(root string, relPrefix string, ignorePatterns []string, shallowOnly bool) ([]fileEntry, error) {
	var entries []fileEntry
	const maxWalkEntries = 4000

	if shallowOnly {
		dirEntries, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, de := range dirEntries {
			name := de.Name()
			if name == ".git" {
				continue
			}
			rel := name
			if relPrefix != "" {
				rel = relPrefix + "/" + name
			}
			if isIgnored(rel, de.IsDir(), ignorePatterns) {
				continue
			}
			full := filepath.Join(root, name)
			entries = append(entries, buildFileEntry(full, rel, de))
		}
		sortEntries(entries)
		return entries, nil
	}

	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if relPrefix != "" {
			relSlash = relPrefix + "/" + relSlash
		}

		if d.IsDir() {
			if defaultSkippedDirs[d.Name()] || isIgnored(relSlash, true, ignorePatterns) {
				return filepath.SkipDir
			}
		} else if isIgnored(relSlash, false, ignorePatterns) {
			return nil
		}

		entries = append(entries, buildFileEntry(p, relSlash, d))
		if len(entries) >= maxWalkEntries {
			return fs.SkipAll
		}
		return nil
	})

	sortEntries(entries)
	return entries, nil
}

func buildFileEntry(absPath string, relSlash string, d fs.DirEntry) fileEntry {
	fe := fileEntry{
		relPath: relSlash,
		absPath: absPath,
		isDir:   d.IsDir(),
	}

	linfo, err := os.Lstat(absPath)
	if err == nil {
		mode := linfo.Mode()
		if mode&(os.ModeSymlink|os.ModeIrregular) != 0 && !linfo.IsDir() {
			fe.isSymlink = true
			if target, lerr := os.Readlink(absPath); lerr == nil {
				fe.linkTarget = filepath.ToSlash(target)
			}
		}
		if !fe.isDir {
			fe.size = linfo.Size()
			fe.isExec = detectExecutable(absPath, mode)
		}
	}
	return fe
}

func detectExecutable(absPath string, mode os.FileMode) bool {
	ext := strings.ToLower(filepath.Ext(absPath))
	switch ext {
	case ".exe", ".bat", ".cmd", ".com", ".sh":
		return true
	}
	if runtime.GOOS != "windows" && mode&0o111 != 0 {
		return true
	}
	// Check for shebang (#!) in small/medium files
	f, err := os.Open(absPath)
	if err != nil {
		return false
	}
	var hdr [2]byte
	n, _ := f.Read(hdr[:])
	_ = f.Close()
	return n == 2 && hdr[0] == '#' && hdr[1] == '!'
}

func sortEntries(entries []fileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return entries[i].relPath < entries[j].relPath
	})
}

func filterByFilename(entries []fileEntry, query string, maxRes int, embedder ai.PartEmbedder) ([]fileEntry, string) {
	if query == "*" {
		return entries, "glob '*'"
	}

	normQ := filepath.ToSlash(query)
	lowerQ := strings.ToLower(normQ)

	// 1. Glob / Exact Substring
	var globMatches []fileEntry
	for _, e := range entries {
		if MatchDoublestar(normQ, e.relPath) || MatchDoublestar(normQ, path.Base(e.relPath)) {
			globMatches = append(globMatches, e)
			continue
		}
		if !hasGlobChars(normQ) && strings.Contains(strings.ToLower(e.relPath), lowerQ) {
			globMatches = append(globMatches, e)
		}
	}
	if len(globMatches) > 0 && len(globMatches) <= maxRes*3 {
		return globMatches, fmt.Sprintf("glob %q", query)
	}

	// 2. Fuzzy Filename Subsequence / Token Match
	var fuzzyMatches []fileEntry
	for _, e := range entries {
		if sc := fuzzyPathScore(lowerQ, strings.ToLower(e.relPath)); sc >= 0.45 {
			cp := e
			cp.score = sc
			fuzzyMatches = append(fuzzyMatches, cp)
		}
	}
	sort.Slice(fuzzyMatches, func(i, j int) bool {
		if fuzzyMatches[i].score != fuzzyMatches[j].score {
			return fuzzyMatches[i].score > fuzzyMatches[j].score
		}
		return fuzzyMatches[i].relPath < fuzzyMatches[j].relPath
	})

	if len(fuzzyMatches) > 0 && (embedder == nil || (fuzzyMatches[0].score >= 0.70 && len(fuzzyMatches) <= maxRes*3)) {
		return fuzzyMatches, fmt.Sprintf("fuzzy %q", query)
	}

	// 3. Semantic Filename Embeddings via EmbeddingGemma 2
	if embedder != nil {
		candidates := entries
		if len(globMatches) > maxRes*3 {
			candidates = globMatches
		}
		if sem := rankPathsByEmbedding(candidates, query, embedder); len(sem) > 0 {
			return sem, fmt.Sprintf("semantic %q", query)
		}
	}

	if len(globMatches) > 0 {
		return globMatches, fmt.Sprintf("glob %q", query)
	}
	return fuzzyMatches, fmt.Sprintf("fuzzy %q", query)
}

func filterByContent(entries []fileEntry, query string, maxRes int, embedder ai.PartEmbedder) ([]fileEntry, string) {
	lowerQ := strings.ToLower(query)
	re, reErr := regexp.Compile("(?i)" + query)

	var matched []fileEntry
	for _, e := range entries {
		if e.isDir || e.size > 512*1024 {
			continue
		}
		data, err := os.ReadFile(e.absPath)
		if err != nil || isBinaryBlob(data) {
			continue
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for idx, ln := range lines {
			hit := strings.Contains(strings.ToLower(ln), lowerQ)
			if !hit && reErr == nil {
				hit = re.MatchString(ln)
			}
			if hit {
				cp := e
				preview := strings.TrimSpace(ln)
				if len(preview) > 48 {
					preview = preview[:45] + "..."
				}
				cp.matchNote = fmt.Sprintf("[match: L%d %q]", idx+1, preview)
				matched = append(matched, cp)
				break
			}
		}
	}

	if len(matched) > 0 && (len(matched) <= maxRes || embedder == nil) {
		return matched, fmt.Sprintf("content %q", query)
	}

	if embedder != nil {
		qVec, err := embedder.Embed([]ai.PromptPart{{Text: query}})
		if err == nil && len(qVec) > 0 {
			candidates := entries
			if len(matched) > maxRes {
				candidates = matched
			}
			var sem []fileEntry
			for _, e := range candidates {
				if e.isDir || e.size > 256*1024 {
					continue
				}
				data, err := os.ReadFile(e.absPath)
				if err != nil || isBinaryBlob(data) {
					continue
				}
				sample := string(data)
				if len(sample) > 1500 {
					sample = sample[:1500]
				}
				vec, err := embedder.Embed([]ai.PromptPart{{Text: e.relPath + "\n" + sample}})
				if err != nil || len(vec) == 0 {
					continue
				}
				sim := cosineSim(qVec, vec)
				if sim >= 0.50 {
					cp := e
					cp.score = sim
					if cp.matchNote == "" {
						cp.matchNote = fmt.Sprintf("[semantic: %.2f]", sim)
					}
					sem = append(sem, cp)
				}
			}
			sort.Slice(sem, func(i, j int) bool {
				return sem[i].score > sem[j].score
			})
			if len(sem) > 0 {
				return sem, fmt.Sprintf("semantic-content %q", query)
			}
		}
	}

	return matched, fmt.Sprintf("content %q", query)
}

func isBinaryBlob(b []byte) bool {
	check := b
	if len(check) > 512 {
		check = check[:512]
	}
	return bytes.IndexByte(check, 0) >= 0
}

func rankPathsByEmbedding(entries []fileEntry, query string, embedder ai.PartEmbedder) []fileEntry {
	qVec, err := embedder.Embed([]ai.PromptPart{{Text: query}})
	if err != nil || len(qVec) == 0 {
		return nil
	}
	var scored []fileEntry
	for _, e := range entries {
		vec, err := embedder.Embed([]ai.PromptPart{{Text: e.relPath}})
		if err != nil || len(vec) == 0 {
			continue
		}
		sim := cosineSim(qVec, vec)
		if sim >= 0.50 {
			cp := e
			cp.score = sim
			scored = append(scored, cp)
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	return scored
}

func cosineSim(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func fuzzyPathScore(query, target string) float64 {
	if query == "" {
		return 1.0
	}
	if strings.Contains(target, query) {
		return 0.90 + 0.10*(float64(len(query))/float64(len(target)))
	}
	base := path.Base(target)
	if strings.Contains(base, query) {
		return 0.95
	}
	// Subsequence match
	qi := 0
	consecutive := 0
	maxConsec := 0
	for ti := 0; ti < len(target) && qi < len(query); ti++ {
		if target[ti] == query[qi] {
			qi++
			consecutive++
			if consecutive > maxConsec {
				maxConsec = consecutive
			}
		} else {
			consecutive = 0
		}
	}
	if qi < len(query) {
		return 0
	}
	return 0.45 + 0.40*(float64(maxConsec)/float64(len(query)))
}

func hasGlobChars(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// MatchDoublestar evaluates a slash-normalized glob pattern supporting `**`, `*`, `?`, and `[...]`
// in pure Go without external dependencies.
func MatchDoublestar(pattern, name string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	patSegs := splitSlash(pattern)
	nameSegs := splitSlash(name)
	return matchSegments(patSegs, nameSegs)
}

func splitSlash(s string) []string {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		// Collapse consecutive "**"
		for len(pat) > 1 && pat[1] == "**" {
			pat = pat[1:]
		}
		if len(pat) == 1 {
			return true
		}
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	matched, err := path.Match(pat[0], name[0])
	if err != nil || !matched {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}

func loadGitignorePatterns(cwd string) []string {
	data, err := os.ReadFile(filepath.Join(cwd, ".gitignore"))
	if err != nil {
		return nil
	}
	var patterns []string
	for _, ln := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, "!") {
			continue
		}
		patterns = append(patterns, strings.TrimSuffix(ln, "/"))
	}
	return patterns
}

func isIgnored(relSlash string, isDir bool, patterns []string) bool {
	base := path.Base(relSlash)
	for _, pat := range patterns {
		if pat == base || MatchDoublestar(pat, relSlash) || MatchDoublestar(pat, base) {
			return true
		}
		if isDir && strings.HasPrefix(relSlash, strings.TrimPrefix(pat, "/")+"/") {
			return true
		}
	}
	return false
}

func formatHumanSize(sz int64) string {
	if sz < 1024 {
		return fmt.Sprintf("%dB", sz)
	}
	kb := float64(sz) / 1024.0
	if kb < 1024.0 {
		return fmt.Sprintf("%.1fK", kb)
	}
	mb := kb / 1024.0
	if mb < 1024.0 {
		return fmt.Sprintf("%.1fM", mb)
	}
	gb := mb / 1024.0
	return fmt.Sprintf("%.1fG", gb)
}

func compactHomePath(p string) string {
	slash := filepath.ToSlash(p)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		homeSlash := filepath.ToSlash(home)
		if strings.EqualFold(slash, homeSlash) {
			return "~"
		}
		if len(slash) > len(homeSlash) && strings.EqualFold(slash[:len(homeSlash)], homeSlash) && slash[len(homeSlash)] == '/' {
			return "~" + slash[len(homeSlash):]
		}
	}
	return slash
}
